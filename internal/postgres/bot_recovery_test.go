package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/agent"
	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensesvc"
	"github.com/LionJr/expenses/internal/postgres"
	"github.com/LionJr/expenses/internal/telegram"
	"github.com/LionJr/expenses/migrations"
)

type failFinishOnce struct {
	*postgres.Store
	fail bool
}

func (s *failFinishOnce) FinishUpdate(ctx context.Context, id int64, reply string, data json.RawMessage, user int64) error {
	if s.fail {
		s.fail = false
		return errors.New("simulated crash before session persistence")
	}
	return s.Store.FinishUpdate(ctx, id, reply, data, user)
}

type scriptedProvider struct {
	responses []agent.Message
	calls     int
}

func (p *scriptedProvider) Complete(context.Context, []agent.Message, []agent.Tool) (agent.Message, error) {
	p.calls++
	if len(p.responses) == 0 {
		return agent.Message{}, errors.New("provider must not be called during recovery")
	}
	m := p.responses[0]
	p.responses = p.responses[1:]
	return m, nil
}
func toolMessage(name, args string) agent.Message {
	return agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "tool1", Type: "function", Function: agent.Function{Name: name, Arguments: args}}}}
}

func checkBotRecovery(t *testing.T, s *postgres.Store) {
	ctx := context.Background()
	for n, kind := range []string{"update_expense", "delete_expense"} {
		t.Run(kind, func(t *testing.T) {
			e, err := expensesvc.New(s).Create(ctx, expense.Input{UserID: 101, Amount: 35000, Category: "еда", SpentAt: "2026-10-17", Comment: "recovery"})
			if err != nil {
				t.Fatal(err)
			}
			updateID := int64(8001 + n)
			args := fmt.Sprintf(`{"id":"%d"}`, e.ID)
			text := "удали последний расход"
			if kind == "update_expense" {
				args = fmt.Sprintf(`{"id":"%d","amount_rubles":"450","expected_amount_rubles":"350"}`, e.ID)
				text = "последнее было не 350, а 450"
			}
			provider := &scriptedProvider{responses: []agent.Message{toolMessage("list_expenses", `{"selection":"latest_created","limit":1}`), toolMessage(kind, args), {Role: "assistant", Content: "Готово"}}}
			responder := &agent.Agent{Store: expensesvc.New(s), LLM: provider, Location: time.UTC, Timeout: time.Second}
			store := &failFinishOnce{Store: s, fail: true}
			sends := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends++; fmt.Fprint(w, `{"ok":true,"result":{}}`) }))
			defer server.Close()
			bot := &telegram.Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{101: true}, Store: store, Agent: responder}
			data, _ := json.Marshal(map[string]any{"update_id": updateID, "message": map[string]any{"from": map[string]any{"id": 101}, "chat": map[string]any{"id": 101, "type": "private"}, "text": text}})
			var update telegram.Update
			if err = json.Unmarshal(data, &update); err != nil {
				t.Fatal(err)
			}
			if err = bot.Handle(ctx, update); err == nil {
				t.Fatal("expected interrupted session persistence")
			}
			if provider.calls != 2 || sends != 0 {
				t.Fatalf("operation not completed before crash: calls=%d sends=%d", provider.calls, sends)
			}
			if err = bot.Handle(ctx, update); err != nil {
				t.Fatal(err)
			}
			if provider.calls != 2 || sends != 1 {
				t.Fatalf("recovery re-ran LLM or sent twice: calls=%d sends=%d", provider.calls, sends)
			}
			state, reply, err := s.StartUpdate(ctx, updateID, 101)
			if err != nil || state != "sent" {
				t.Fatalf("recovery state=%s err=%v", state, err)
			}
			expected := "Выполнено: изменено: 450,00"
			if kind == "delete_expense" {
				expected = "Выполнено: расход удалён"
			}
			if len(reply) < len(expected) || reply[:len(expected)] != expected {
				t.Fatalf("incorrect replay receipt: %s", reply)
			}
			key := fmt.Sprintf("telegram:%d:mutation:1", updateID)
			saved, err := s.OperationResult(ctx, key, 101)
			if err != nil || len(saved) == 0 {
				t.Fatal("committed operation missing")
			}
			foreign, err := s.OperationResult(ctx, key, 202)
			if err != nil || len(foreign) != 0 {
				t.Fatal("operation result leaked across owners")
			}
			var count int
			if err = postgres.DBForTest(s).QueryRowContext(ctx, "SELECT count(*) FROM expense_operations WHERE operation_key=$1", key).Scan(&count); err != nil || count != 1 {
				t.Fatal("duplicate operation")
			}
		})
	}
}
func checkDeliveryOutbox(t *testing.T, s *postgres.Store) {
	ctx := context.Background()
	const id int64 = 9001
	if _, _, err := s.StartUpdate(ctx, id, 101); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishUpdate(ctx, id, "готово", json.RawMessage(`{}`), 101); err != nil {
		t.Fatal(err)
	}
	contains := func() bool {
		t.Helper()
		pending, err := s.PendingDeliveries(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range pending {
			if d.UpdateID == id {
				return true
			}
		}
		return false
	}
	if !contains() {
		t.Fatal("saved reply not in outbox")
	}
	if err := s.FailDelivery(ctx, id, http.StatusTooManyRequests, false, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if contains() {
		t.Fatal("Retry-After deadline ignored")
	}
	if pending, err := s.DeliveryPending(ctx, id); err != nil || !pending {
		t.Fatalf("webhook must not acknowledge future retry: pending=%v error=%v", pending, err)
	}
	if err := s.FailDelivery(ctx, id, http.StatusServiceUnavailable, false, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if !contains() {
		t.Fatal("due retry not available")
	}
	if err := s.FailDelivery(ctx, id, http.StatusServiceUnavailable, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !contains() {
		t.Fatal("three temporary errors discarded the saved reply")
	}
	if pending, err := s.DeliveryPending(ctx, id); err != nil || !pending {
		t.Fatalf("temporary failure must remain retryable: pending=%v error=%v", pending, err)
	}
	state, _, err := s.StartUpdate(ctx, id, 101)
	if err != nil || state != "done" {
		t.Fatalf("pending state=%s err=%v", state, err)
	}
	var attempts, code int
	if err = postgres.DBForTest(s).QueryRowContext(ctx, "SELECT delivery_attempts,last_error_code FROM telegram_updates WHERE update_id=$1", id).Scan(&attempts, &code); err != nil || attempts != 3 || code != http.StatusServiceUnavailable {
		t.Fatal("delivery metadata not persisted")
	}
	if err := s.MarkSent(ctx, id); err != nil {
		t.Fatal(err)
	}
}

func checkBaselineReopen(t *testing.T, s *postgres.Store, dsn string) {
	ctx := context.Background()
	// A separate schema verifies that the complete baseline can be reopened
	// without replaying DDL or changing expenses and queued replies.
	if _, err := postgres.DBForTest(s).ExecContext(ctx, "CREATE SCHEMA upgrade_test"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := postgres.DBForTest(s).ExecContext(ctx, "DROP SCHEMA upgrade_test CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", "upgrade_test")
	parsed.RawQuery = query.Encode()
	old, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	initial, err := migrations.Files.ReadFile("001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.ExecContext(ctx, string(initial)); err != nil {
		t.Fatal(err)
	}

	if _, err = old.ExecContext(ctx, `CREATE TABLE schema_migrations(version TEXT PRIMARY KEY);
 INSERT INTO schema_migrations VALUES('001_init.sql');
 INSERT INTO users(id) VALUES(101);
 INSERT INTO expenses(user_id,amount,category,spent_at,comment) VALUES(101,35000,'еда','2026-10-17','existing expense');
 INSERT INTO telegram_updates(update_id,user_id,state,reply) VALUES(1,101,'done','existing reply'),(2,101,'done','Найдено расходов: 2')`); err != nil {
		t.Fatal(err)
	}
	upgraded, err := postgres.Open(ctx, parsed.String(), []int64{101})
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var stateType string
	if err := postgres.DBForTest(upgraded).QueryRowContext(ctx, `SELECT udt_name FROM information_schema.columns
        WHERE table_schema = current_schema() AND table_name = 'telegram_updates' AND column_name = 'state'`).Scan(&stateType); err != nil || stateType != "telegram_update_state" {
		t.Fatalf("state was not upgraded to enum: %s %v", stateType, err)
	}
	page, err := expensesvc.New(upgraded).List(ctx, expense.Filter{UserID: 101})
	if err != nil || page.Total != 1 || page.TotalAmount != 35000 || page.Items[0].Version != 1 {
		t.Fatalf("upgrade lost expenses: %+v %v", page, err)
	}
	pending, err := upgraded.PendingDeliveries(ctx, 20)
	if err != nil || len(pending) != 2 || pending[0].Reply != "existing reply" || pending[0].Attempts != 0 {
		t.Fatalf("upgrade lost reply: %+v %v", pending, err)
	}

	if err = upgraded.FailDelivery(ctx, 1, http.StatusForbidden, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	state, _, err := upgraded.StartUpdate(ctx, 1, 101)
	if err != nil || state != "failed" {
		t.Fatal("new delivery state unavailable after upgrade")
	}
}
