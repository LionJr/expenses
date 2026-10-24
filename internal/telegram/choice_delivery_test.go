package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/agent"
	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensesvc"
)

type choiceRepo struct {
	deleted   int64
	mutations int
}

func (*choiceRepo) OperationResult(context.Context, string, int64) (json.RawMessage, error) {
	return nil, nil
}
func (*choiceRepo) List(context.Context, expense.Filter) (expense.Page, error) {
	return expense.Page{Items: []expense.Expense{{ID: 33, UserID: 42, Amount: 300}, {ID: 11, UserID: 42, Amount: 100}, {ID: 22, UserID: 42, Amount: 200}}, Total: 3}, nil
}
func (r *choiceRepo) Execute(_ context.Context, _ string, _ int64, prepare func() (expense.Command, error)) (json.RawMessage, error) {
	command, err := prepare()
	if err != nil {
		return nil, err
	}
	kind, id := string(command.Operation), command.ID
	r.mutations++
	r.deleted = id
	return json.Marshal(map[string]string{"operation": kind, "deleted_id": fmt.Sprint(id)})
}

type choiceProvider struct{ calls int }

func (p *choiceProvider) Complete(context.Context, []agent.Message, []agent.Tool) (agent.Message, error) {
	p.calls++
	switch p.calls {
	case 1:
		return agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "find", Type: "function", Function: agent.Function{Name: "list_expenses", Arguments: `{"search":"такси"}`}}}}, nil
	case 2:
		return agent.Message{Role: "assistant", ToolCalls: []agent.ToolCall{{ID: "delete", Type: "function", Function: agent.Function{Name: "delete_expense", Arguments: `{"id":"33"}`}}}}, nil
	default:
		return agent.Message{Role: "assistant", Content: "Запрос отменён."}, nil
	}
}
func newChoiceStore() *updateStore {
	return &updateStore{rows: map[int64]*memoryStore{}, users: map[int64]int64{}, sessions: map[int64]json.RawMessage{}}
}
func choiceUpdate(t *testing.T, updateID, messageID int64, text string) Update {
	t.Helper()
	b, err := json.Marshal(map[string]any{"update_id": updateID, "message": map[string]any{"message_id": messageID, "from": map[string]int64{"id": 42}, "chat": map[string]any{"id": 42, "type": "private"}, "text": text}})
	if err != nil {
		t.Fatal(err)
	}
	return update(t, string(b))
}
func sessionAt(t *testing.T, s *updateStore) agent.Session {
	t.Helper()
	var out agent.Session
	if err := json.Unmarshal(s.sessions[42], &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestChoiceWaitsForDeliveryAndRejectsEarlierQueuedReply(t *testing.T) {
	store := newChoiceStore()
	repo := &choiceRepo{}
	provider := &choiceProvider{}
	// Previously delivered list A is [11,22]; list B will be [33,11,22].
	store.sessions[42], _ = json.Marshal(agent.Session{Candidates: []expense.Expense{{ID: 11}, {ID: 22}}, CandidateTotal: 2, Choice: &botstate.Choice{UpdateID: 5, MessageID: 50, State: botstate.AwaitingSelection}})
	promptAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if strings.HasPrefix(payload.Text, "Какой расход удалить?") {
			promptAttempts++
			if promptAttempts == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				fmt.Fprint(w, `{"ok":false,"error_code":503}`)
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":{"message_id":105}}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":106}}`)
	}))
	defer server.Close()
	responder := &agent.Agent{Store: expensesvc.New(repo), LLM: provider, Timeout: time.Second, Location: time.UTC}
	bot := Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{42: true}, Store: store, Agent: responder}
	if err := bot.Handle(context.Background(), choiceUpdate(t, 10, 100, "удали такси")); err != nil {
		t.Fatal(err)
	}
	if s := sessionAt(t, store); s.Choice.State != botstate.AwaitingDelivery || store.rows[10].state != "done" {
		t.Fatal("failed prompt became selectable")
	}
	if err := bot.Handle(context.Background(), choiceUpdate(t, 11, 101, "вторую")); err != nil {
		t.Fatal(err)
	}
	if repo.mutations != 0 || provider.calls != 0 || store.rows[10].state != "done" {
		t.Fatal("invisible list changed ordinal target")
	}
	// Retry sends B. A different reply already queued before that delivery must
	// still be rejected even though its handler now sees await_selection.
	store.rows[10].retryAt = time.Now().Add(-time.Second)
	if err := bot.deliverPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := sessionAt(t, store); s.Choice.State != botstate.AwaitingSelection || s.Choice.MessageID != 105 {
		t.Fatal("delivery did not activate its exact prompt")
	}
	if err := bot.Handle(context.Background(), choiceUpdate(t, 12, 103, "вторую")); err != nil {
		t.Fatal(err)
	}
	if repo.mutations != 0 || provider.calls != 0 {
		t.Fatal("queued pre-delivery reply authorized deletion")
	}
	if err := bot.Handle(context.Background(), choiceUpdate(t, 13, 110, "вторую")); err != nil {
		t.Fatal(err)
	}
	if repo.mutations != 1 || repo.deleted != 11 || provider.calls != 0 || sessionAt(t, store).Choice != nil {
		t.Fatal("post-delivery choice did not execute exactly once")
	}
}
func TestNewRequestSupersedesOldChoicePrompt(t *testing.T) {
	store := newChoiceStore()
	repo := &choiceRepo{}
	provider := &choiceProvider{}
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		if sends == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"ok":false,"error_code":503}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":105}}`)
	}))
	defer server.Close()
	bot := Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{42: true}, Store: store, Agent: &agent.Agent{Store: expensesvc.New(repo), LLM: provider, Timeout: time.Second, Location: time.UTC}}
	if err := bot.Handle(context.Background(), choiceUpdate(t, 10, 100, "удали такси")); err != nil {
		t.Fatal(err)
	}
	stale := botstate.Delivery{UpdateID: 10, UserID: 42, Reply: store.rows[10].reply, ChoicePrompt: true}
	if err := bot.Handle(context.Background(), choiceUpdate(t, 11, 101, "отмена")); err != nil {
		t.Fatal(err)
	}
	if store.rows[10].state != "superseded" || sessionAt(t, store).Choice != nil {
		t.Fatal("old choice was not canceled")
	}
	before := sends
	if err := bot.deliver(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	if err := bot.deliverPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sends != before || repo.mutations != 0 {
		t.Fatal("superseded prompt was delivered or executed")
	}
}
func TestMissingPromptMessageIDNeverActivatesChoiceDuringRetries(t *testing.T) {
	store := newChoiceStore()
	repo := &choiceRepo{}
	provider := &choiceProvider{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{}}`) }))
	defer server.Close()
	bot := Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{42: true}, Store: store, Agent: &agent.Agent{Store: expensesvc.New(repo), LLM: provider, Timeout: time.Second, Location: time.UTC}}
	if err := bot.Handle(context.Background(), choiceUpdate(t, 10, 100, "удали такси")); err != nil {
		t.Fatal(err)
	}
	if sessionAt(t, store).Choice.State != botstate.AwaitingDelivery || store.rows[10].state != "done" {
		t.Fatal("missing Telegram ID activated prompt")
	}
	for i := 0; i < 4; i++ {
		store.rows[10].retryAt = time.Now().Add(-time.Second)
		if err := bot.deliverPending(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if store.rows[10].state != "done" || sessionAt(t, store).Choice.State != botstate.AwaitingDelivery {
		t.Fatal("temporary missing-ID response discarded or activated the choice")
	}
	if err := bot.Handle(context.Background(), choiceUpdate(t, 11, 110, "вторую")); err != nil {
		t.Fatal(err)
	}
	if repo.mutations != 0 || provider.calls != 0 {
		t.Fatal("undelivered prompt allowed a stale choice")
	}
	if err := bot.Handle(context.Background(), choiceUpdate(t, 12, 111, "/start")); err != nil {
		t.Fatal(err)
	}
	if store.rows[10].state != "superseded" || sessionAt(t, store).Choice != nil || sessionAt(t, store).Pending != nil {
		t.Fatal("new conversation did not cancel undelivered choice")
	}
}

func (*choiceRepo) Users(context.Context) ([]expense.User, error) { panic("unexpected users query") }
func (*choiceRepo) Create(context.Context, expense.Input) (expense.Expense, error) {
	panic("unexpected panel create")
}
func (*choiceRepo) Update(context.Context, int64, int64, expense.Patch) (expense.Expense, error) {
	panic("unexpected panel update")
}
func (*choiceRepo) Delete(context.Context, int64, int64) error { panic("unexpected panel delete") }
