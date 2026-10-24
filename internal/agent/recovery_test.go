package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

func TestLatestRequiresExplicitSelection(t *testing.T) {
	for _, text := range []string{"удали такси", "удали не последнюю поездку", "удали предпоследнюю поездку", "удали такси, последнее не трогай", "удали такси, кроме последнего", "последнее не удаляй, удали такси", "удали последнюю поездку"} {
		t.Run(text, func(t *testing.T) {
			repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 22, UserID: 42, Amount: 200}}, Total: 2}}
			llm := &script{responses: []Message{call("list_expenses", `{"search":"такси","selection":"latest_created","limit":1}`), call("delete_expense", `{"id":"22"}`), {Content: "Готово"}}}
			_, s, _ := newAgent(repo, llm).Reply(context.Background(), 42, 10, text, Session{})
			want := 0
			if text == "удали последнюю поездку" {
				want = 1
			}
			if repo.mutations != want {
				t.Fatalf("mutations=%d want=%d", repo.mutations, want)
			}
			if want == 0 && s.LatestID != 0 {
				t.Fatal("unauthorized latest selection persisted")
			}
		})
	}
}
func TestReadSelectionCannotInferMutationFromHistory(t *testing.T) {
	repo := &fakeRepo{}
	llm := &script{responses: []Message{call("delete_expense", `{"id":"123456789"}`), {Content: "Готово"}}}
	s := Session{Choice: &botstate.Choice{UpdateID: 9, State: botstate.AwaitingSelection}, Candidates: []expense.Expense{{ID: 987654321, UserID: 42}, {ID: 123456789, UserID: 42}}, CandidateTotal: 2, History: []Message{{Role: "user", Content: "удали такси"}, {Role: "assistant", Content: "1. Такси 350 TMT\n2. Такси 450 TMT"}}}
	reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 10, "вторую", s)
	if err != nil || repo.mutations != 0 || len(llm.seen) != 0 || !strings.Contains(reply, "Выбран расход #123456789") {
		t.Fatalf("read selection inferred mutation from old request: %q err=%v", reply, err)
	}
}
func TestReplayUsesCommittedOperationBeforeLookupOrLLM(t *testing.T) {
	repo := &fakeRepo{saved: json.RawMessage(`{"operation":"update_expense","id":"22","user_id":"42","amount":"45000","category":"еда","spent_at":"2026-10-17"}`)}
	llm := &script{err: errors.New("must not be called")}
	s := Session{Choice: &botstate.Choice{UpdateID: 9, State: botstate.AwaitingSelection}, Candidates: []expense.Expense{{ID: 22, Amount: 45000}}, CandidateTotal: 1, Pending: &PendingAction{Kind: "update_expense"}}
	reply, s, _ := newAgent(repo, llm).Reply(context.Background(), 42, 10, "последнее было не 350, а 450", s)
	if repo.lookupCalls != 1 || repo.mutations != 0 || len(llm.seen) != 0 {
		t.Fatal("replay re-ran agent")
	}
	if !strings.Contains(reply, "450,00") || strings.Contains(reply, "не выполнены") || s.Pending != nil {
		t.Fatal(reply)
	}
}
func TestPendingUpdateSurvivesPersistence(t *testing.T) {
	repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 11, Amount: 35000}, {ID: 22, Amount: 35000}}, Total: 2}}
	llm := &script{responses: []Message{call("list_expenses", `{"search":"такси"}`), call("update_expense", `{"id":"11","amount_decimal":"450","expected_amount_decimal":"350"}`)}}
	_, s, _ := newAgent(repo, llm).Reply(context.Background(), 42, 10, "такси было не 350, а 450", Session{})
	if s.Pending == nil || repo.mutations != 0 {
		t.Fatal("missing pending update")
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	s.Choice.State = botstate.AwaitingSelection
	llm = &script{err: errors.New("offline")}
	newAgent(repo, llm).Reply(context.Background(), 42, 11, "вторую", s)
	if len(llm.seen) != 0 || repo.mutations != 1 || repo.id != 22 || repo.patch.Amount == nil || *repo.patch.Amount != 45000 || repo.patch.ExpectedAmount == nil || *repo.patch.ExpectedAmount != 35000 {
		t.Fatalf("lost pending update: %+v", repo)
	}
}
func TestPendingInvalidChoiceAndNewRequest(t *testing.T) {
	repo := &fakeRepo{}
	llm := &script{err: errors.New("offline")}
	s := Session{Choice: &botstate.Choice{UpdateID: 9, State: botstate.AwaitingSelection}, Candidates: []expense.Expense{{ID: 11}, {ID: 22}}, CandidateTotal: 2, Pending: &PendingAction{Kind: "delete_expense", IntentValidated: true}}
	reply, s, _ := newAgent(repo, llm).Reply(context.Background(), 42, 10, "третью", s)
	if !strings.Contains(reply, "Такого номера") || s.Pending == nil || repo.mutations != 0 || len(llm.seen) != 0 {
		t.Fatal("invalid choice lost pending state")
	}
	llm = &script{responses: []Message{call("create_expense", `{"amount_decimal":"50","category":"еда","spent_at":"2026-10-17"}`), {Content: "Готово"}}}
	_, s, _ = newAgent(repo, llm).Reply(context.Background(), 42, 11, "кофе 50", s)
	if repo.mutations != 1 || repo.input.Amount != 5000 || s.Pending != nil {
		t.Fatal("new intent did not cancel pending action")
	}
}
func TestProviderDiagnosticsAreStructuredAndRedacted(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"secret-response-body"}`))
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL, Key: "secret-api-key", Model: "fake", HTTP: server.Client()}
	_, err := client.Complete(context.Background(), nil, Tools(expense.TMT))
	var provider *ProviderError
	if !errors.As(err, &provider) || provider.Status != 401 || provider.Attempts != 1 || provider.Kind != "authentication" {
		t.Fatalf("lost diagnostics: %v", err)
	}
	a := newAgent(&fakeRepo{}, &script{err: err})
	a.Reply(context.Background(), 42, 99, "private-expense-text", Session{})
	for _, field := range []string{`"update_id":99`, `"error_kind":"authentication"`, `"status":401`, `"attempts":1`, `"duration_ms"`} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("missing %s: %s", field, &logs)
		}
	}
	for _, secret := range []string{"secret-response-body", "secret-api-key", "private-expense-text"} {
		if strings.Contains(logs.String(), secret) || strings.Contains(err.Error(), secret) {
			t.Fatalf("diagnostics leaked %s", secret)
		}
	}
}
func TestStorageErrorsDoNotLeakExpenseDetails(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	repo := &fakeRepo{mutationError: errors.New("SQL includes secret-expense-comment")}
	llm := &script{responses: []Message{call("create_expense", `{"amount_decimal":"50","category":"еда","spent_at":"2026-10-17"}`), {Content: "Готово"}}}
	reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 99, "добавь private-expense-text 50", Session{})
	var processing *ProcessingError
	if !errors.As(err, &processing) || strings.Contains(err.Error(), "secret-expense-comment") {
		t.Fatal("storage failure must remain retryable and redacted")
	}
	if !strings.Contains(logs.String(), `"error_kind":"storage"`) || !strings.Contains(logs.String(), `"operation":"create_expense"`) {
		t.Fatal(logs.String())
	}
	if strings.Contains(logs.String(), "secret-expense-comment") || strings.Contains(reply, "secret-expense-comment") {
		t.Fatal("storage diagnostics leaked data")
	}
}

func TestRecoveryLookupFailureLeavesUpdateRetryable(t *testing.T) {
	repo := &fakeRepo{lookupError: errors.New("temporary database outage")}
	llm := &script{err: errors.New("must not be called")}
	a := newAgent(repo, llm)
	reply, _, err := a.Reply(context.Background(), 42, 10, "последнее было не 350, а 450", Session{})
	if err == nil || reply != "" || len(llm.seen) != 0 || repo.mutations != 0 {
		t.Fatal("uncertain state must not be finalized")
	}
	repo.lookupError = nil
	repo.saved = json.RawMessage(`{"operation":"delete_expense","deleted_id":"22"}`)
	reply, _, err = a.Reply(context.Background(), 42, 10, "последнее было не 350, а 450", Session{})
	if err != nil || !strings.Contains(reply, "расход удалён") || len(llm.seen) != 0 {
		t.Fatal("retry did not recover stored operation")
	}
}
func TestListsAreRenderedInCandidateOrder(t *testing.T) {
	repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 11, Amount: 35000, Comment: "first"}, {ID: 22, Amount: 45000, Comment: "second"}}, Total: 2}}
	llm := &script{responses: []Message{call("list_expenses", `{}`), {Content: "1. second\n2. first"}}}
	reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 10, "покажи расходы", Session{})
	if err != nil || !strings.Contains(reply, "1. 350,00") || !strings.Contains(reply, "2. 450,00") {
		t.Fatal("model reordered choices: " + reply)
	}
}
