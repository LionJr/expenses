package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/agent"
	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/httpapi"
	"github.com/LionJr/expenses/internal/postgres"
	"github.com/LionJr/expenses/internal/telegram"
)

func privateUpdate(t *testing.T, id, messageID int64, text string) telegram.Update {
	t.Helper()
	data, err := json.Marshal(map[string]any{"update_id": id, "message": map[string]any{"message_id": messageID, "from": map[string]int64{"id": 101}, "chat": map[string]any{"id": 101, "type": "private"}, "text": text}})
	if err != nil {
		t.Fatal(err)
	}
	var update telegram.Update
	if err = json.Unmarshal(data, &update); err != nil {
		t.Fatal(err)
	}
	return update
}
func loadChoiceSession(t *testing.T, s *postgres.Store) agent.Session {
	t.Helper()
	data, err := s.LoadSession(context.Background(), 101)
	if err != nil {
		t.Fatal(err)
	}
	var session agent.Session
	if err = json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	return session
}
func checkChoiceDeliveryChain(t *testing.T, s *postgres.Store) {
	ctx := context.Background()
	create := func(amount int64) expense.Expense {
		t.Helper()
		e, err := s.Create(ctx, expense.Input{UserID: 101, Amount: amount, Category: "транспорт", SpentAt: "2026-10-17", Comment: "choice-chain"})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	first, second := create(100), create(200)
	old := agent.Session{Candidates: []expense.Expense{second, first}, CandidateTotal: 2, Choice: &botstate.Choice{UpdateID: 9100, MessageID: 50, State: botstate.AwaitingSelection}}
	data, _ := json.Marshal(old)
	if err := s.SaveSession(ctx, 101, data); err != nil {
		t.Fatal(err)
	}
	newest := create(300)
	provider := &scriptedProvider{responses: []agent.Message{toolMessage("list_expenses", `{"search":"choice-chain"}`), toolMessage("delete_expense", fmt.Sprintf(`{"id":"%d"}`, newest.ID))}}
	var cancelRun context.CancelFunc
	promptAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			fmt.Fprint(w, `{"ok":true,"result":[]}`)
			cancelRun()
			return
		}
		var request struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if strings.HasPrefix(request.Text, "Какой расход удалить?") {
			promptAttempts++
			if promptAttempts == 1 {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"ok":false,"error_code":503}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":105}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":106}}`)
	}))
	defer server.Close()
	bot := &telegram.Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{101: true}, Store: s, Agent: &agent.Agent{Store: s, LLM: provider, Location: time.UTC, Timeout: time.Second}}
	if err := bot.Handle(ctx, privateUpdate(t, 9101, 100, "удали расходы choice-chain")); err != nil {
		t.Fatal(err)
	}
	session := loadChoiceSession(t, s)
	if session.Choice == nil || session.Choice.State != botstate.AwaitingDelivery || session.Pending == nil || session.Candidates[1].ID != second.ID {
		t.Fatal("pending list was not persisted correctly")
	}
	if err := bot.Handle(ctx, privateUpdate(t, 9102, 101, "вторую")); err != nil {
		t.Fatal(err)
	}
	page, err := s.List(ctx, expense.Filter{UserID: 101, Search: "choice-chain"})
	if err != nil || page.Total != 3 {
		t.Fatal("undelivered list authorized deletion")
	}
	if _, err = s.DB.ExecContext(ctx, "UPDATE telegram_updates SET retry_at=now() WHERE update_id=9101"); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	cancelRun = cancel
	defer cancel()
	if err = bot.Run(runCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("outbox run: %v", err)
	}
	session = loadChoiceSession(t, s)
	if session.Choice.State != botstate.AwaitingSelection || session.Choice.MessageID != 105 {
		t.Fatal("delivery acknowledgement did not activate list")
	}
	if err = bot.Handle(ctx, privateUpdate(t, 9103, 103, "вторую")); err != nil {
		t.Fatal(err)
	}
	page, err = s.List(ctx, expense.Filter{UserID: 101, Search: "choice-chain"})
	if err != nil || page.Total != 3 {
		t.Fatal("queued pre-delivery reply authorized deletion")
	}
	if err = bot.Handle(ctx, privateUpdate(t, 9104, 110, "вторую")); err != nil {
		t.Fatal(err)
	}
	page, err = s.List(ctx, expense.Filter{UserID: 101, Search: "choice-chain"})
	if err != nil || page.Total != 2 {
		t.Fatal("post-delivery selection failed")
	}
	for _, e := range page.Items {
		if e.ID == second.ID {
			t.Fatal("wrong ordinal target deleted")
		}
	}
	if provider.calls != 2 || promptAttempts != 2 || loadChoiceSession(t, s).Choice != nil {
		t.Fatal("choice executed through provider or state not cleared")
	}
	saved, err := s.OperationResult(ctx, "telegram:9104:mutation:1", 101)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		DeletedID string `json:"deleted_id"`
	}
	if err = json.Unmarshal(saved, &result); err != nil || result.DeletedID != fmt.Sprint(second.ID) {
		t.Fatalf("wrong committed delete: %s %v", saved, err)
	}
}
func checkChoiceAcknowledgement(t *testing.T, s *postgres.Store) {
	ctx := context.Background()
	finish := func(id int64) {
		t.Helper()
		if _, _, err := s.StartUpdate(ctx, id, 101); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(agent.Session{Candidates: []expense.Expense{{ID: 1}}, CandidateTotal: 1, Choice: &botstate.Choice{UpdateID: id, State: botstate.AwaitingDelivery}})
		if err := s.FinishUpdate(ctx, id, "Найдено расходов: 1", data, 101); err != nil {
			t.Fatal(err)
		}
	}
	finish(9201)
	if err := s.MarkSent(ctx, 9201); !errors.Is(err, expense.ErrInvalid) {
		t.Fatal("missing message ID must not acknowledge a choice")
	}
	state, _, err := s.StartUpdate(ctx, 9201, 101)
	if err != nil || state != "done" || loadChoiceSession(t, s).Choice.State != botstate.AwaitingDelivery {
		t.Fatal("failed acknowledgement was not rolled back")
	}
	if err = s.MarkChoiceSent(ctx, 9201, 100); err != nil {
		t.Fatal(err)
	}
	if session := loadChoiceSession(t, s); session.Choice.State != botstate.AwaitingSelection || session.Choice.MessageID != 100 {
		t.Fatal("acknowledgement was not atomic")
	}
	finish(9202)
	finish(9203)
	state, _, err = s.StartUpdate(ctx, 9202, 101)
	if err != nil || state != "superseded" {
		t.Fatal("new prompt did not supersede old one")
	}
	if err = s.MarkChoiceSent(ctx, 9202, 101); err != nil {
		t.Fatal(err)
	}
	session := loadChoiceSession(t, s)
	if session.Choice.UpdateID != 9203 || session.Choice.State != botstate.AwaitingDelivery {
		t.Fatal("stale acknowledgement activated newer list")
	}
	current, err := s.ChoicePromptCurrent(ctx, 9202, 101)
	if err != nil || current {
		t.Fatal("superseded prompt is current")
	}
	current, err = s.ChoicePromptCurrent(ctx, 9203, 101)
	if err != nil || !current {
		t.Fatal("new prompt missing")
	}
	if _, err = s.DB.ExecContext(ctx, "UPDATE bot_sessions SET expires_at=now()-interval '1 minute' WHERE user_id=101"); err != nil {
		t.Fatal(err)
	}
	current, err = s.ChoicePromptCurrent(ctx, 9203, 101)
	if err != nil || current {
		t.Fatal("expired choice remains current")
	}
	if err = s.SupersedeDelivery(ctx, 9203); err != nil {
		t.Fatal(err)
	}
	state, _, err = s.StartUpdate(ctx, 9203, 101)
	if err != nil || state != "superseded" {
		t.Fatal("expired delivery was not retired")
	}
	finish(9204)
	if err = s.FailDelivery(ctx, 9204, 403, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if session = loadChoiceSession(t, s); session.Choice != nil || len(session.Candidates) != 0 || session.Pending != nil {
		t.Fatal("terminal delivery did not cancel selection")
	}

}

func checkAPILockDeadline(t *testing.T, s *postgres.Store, id int64) {
	ctx := context.Background()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var locked int64
	if err = tx.QueryRowContext(ctx, "SELECT id FROM expenses WHERE id=$1 FOR UPDATE", id).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	handler := (&httpapi.API{Store: s, RequestTimeout: 20 * time.Millisecond}).Handler()
	request := httptest.NewRequest("PATCH", fmt.Sprintf("/api/expenses/%d", id), strings.NewReader(`{"comment":"must not overwrite locked row"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusGatewayTimeout {
		t.Fatalf("locked update did not time out correctly: %d %s", response.Code, response.Body)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var comment string
	if err = s.DB.QueryRowContext(ctx, "SELECT comment FROM expenses WHERE id=$1", id).Scan(&comment); err != nil {
		t.Fatalf("read after canceled update: %v", err)
	}
	if comment == "must not overwrite locked row" {
		t.Fatal("timed out update was applied")
	}

}
