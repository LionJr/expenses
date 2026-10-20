package agent

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

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensesvc"
	"github.com/LionJr/expenses/internal/expensewire"
)

// fakeRepo records the service/storage boundary and returns supplied search results.
// It does not implement SQL filtering or durable idempotency; those contracts
// are exercised against real PostgreSQL in internal/postgres integration tests.
type fakeRepo struct {
	filter           expense.Filter
	page             expense.Page
	saved            json.RawMessage
	lookupError      error
	lookupCalls      int
	mutationError    error
	mutations        int
	mutationAttempts int
	listCalls        int
	listError        error
	owner            int64
	key              string
	input            expense.Input
	patch            expense.Patch
	id               int64
}

func (f *fakeRepo) OperationResult(_ context.Context, key string, user int64) (json.RawMessage, error) {
	f.lookupCalls++
	return f.saved, f.lookupError
}
func (f *fakeRepo) List(_ context.Context, filter expense.Filter) (expense.Page, error) {
	f.listCalls++
	f.filter = filter
	f.owner = filter.UserID
	return f.page, f.listError
}
func (f *fakeRepo) Execute(_ context.Context, key string, owner int64, prepare func() (expense.Command, error)) (json.RawMessage, error) {
	command, err := prepare()
	if err != nil {
		return nil, err
	}
	name, id, i, p := string(command.Operation), command.ID, command.Input, command.Patch
	f.mutationAttempts++
	if f.mutationError != nil {
		return nil, f.mutationError
	}
	f.mutations++
	f.owner = owner
	f.key = key
	f.input = i
	f.patch = p
	f.id = id
	if name == "delete_expense" {
		return json.Marshal(map[string]any{"operation": name, "deleted_id": fmt.Sprint(id)})
	}
	result := expense.Expense{ID: id, UserID: owner, Amount: i.Amount, Category: i.Category, SpentAt: i.SpentAt, Comment: i.Comment}
	if name == "create_expense" {
		result.ID = 1
	} else {
		for _, candidate := range f.page.Items {
			if candidate.ID == id {
				result = candidate
				break
			}
		}
		result.ID, result.UserID = id, owner
		if p.Amount != nil {
			result.Amount = *p.Amount
		}
		if p.Category != nil {
			result.Category = *p.Category
		}
		if p.SpentAt != nil {
			result.SpentAt = *p.SpentAt
		}
		if p.Comment != nil {
			result.Comment = *p.Comment
		}
	}
	data, err := json.Marshal(expensewire.FromExpense(result))
	if err != nil {
		return nil, err
	}
	var receipt map[string]any
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, err
	}
	receipt["operation"] = name
	return json.Marshal(receipt)
}

type script struct {
	responses []Message
	err       error
	seen      [][]Message
}

func (s *script) Complete(_ context.Context, m []Message, _ []Tool) (Message, error) {
	s.seen = append(s.seen, append([]Message(nil), m...))
	if len(s.responses) == 0 {
		return Message{}, s.err
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	return r, nil
}
func call(name, args string) Message {
	return Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "call1", Type: "function", Function: Function{Name: name, Arguments: args}}}}
}
func newAgent(f *fakeRepo, s *script) *Agent {
	return &Agent{Store: expensesvc.New(f), LLM: s, Location: time.FixedZone("UTC+3", 3*3600), Timeout: time.Second, Now: func() time.Time { return time.Date(2026, 10, 16, 22, 30, 0, 0, time.UTC) }}
}
func TestCreateAndDateContext(t *testing.T) {
	f := &fakeRepo{}
	s := &script{responses: []Message{call("create_expense", `{"amount_rubles":"350,50","category":"еда","spent_at":"2026-10-17","comment":"кофе"}`), {Content: "Записано."}}}
	a := newAgent(f, s)
	reply, _, _ := a.Reply(context.Background(), 42, 7, "кофе 350,50", Session{})
	if f.mutations != 1 || f.owner != 42 || f.input.Amount != 35050 || f.key != "telegram:7:mutation:1" {
		t.Fatalf("wrong mutation: %+v", f)
	}
	if !strings.Contains(reply, "Выполнено: добавлено: 350,50 ₽") || !strings.Contains(s.seen[0][0].Content, "Сегодня 2026-10-17") {
		t.Fatal("missing response or local date")
	}
}
func TestMutationConfirmationDoesNotUseGeneratedText(t *testing.T) {
	for _, generated := range []string{"Создан **\u200b**\u200b**", "Запрос некорректен. Уточните, нужно ли создать расход."} {
		f := &fakeRepo{}
		s := &script{responses: []Message{call("create_expense", `{"amount_rubles":"350","category":"еда","spent_at":"2026-10-17","comment":"кофе"}`), {Content: generated}}}
		reply, session, err := newAgent(f, s).Reply(context.Background(), 42, 7, "кофе 350", Session{})
		want := "Выполнено: добавлено: 350,00 ₽ · еда · 2026-10-17 · кофе"
		if err != nil || reply != want || f.mutations != 1 || len(s.seen) != 1 {
			t.Fatalf("unexpected confirmation: %q, calls=%d, err=%v", reply, len(s.seen), err)
		}
		if len(session.History) != 2 || session.History[1].Content != want {
			t.Fatal("history must retain only the authoritative confirmation")
		}
	}
}
func TestRejectUnknownAndOwnerArguments(t *testing.T) {
	for _, args := range []string{`{"amount_rubles":"1","category":"еда","spent_at":"2026-10-17","user_id":"99"}`, `{"amount_rubles":1,"category":"еда","spent_at":"2026-10-17"}`, `{"amount_rubles":"1.001","category":"еда","spent_at":"2026-10-17"}`, `null`} {
		f := &fakeRepo{}
		s := &script{responses: []Message{call("create_expense", args), {Content: "Уточните."}}}
		newAgent(f, s).Reply(context.Background(), 42, 7, "кофе 1", Session{})
		if f.mutations != 0 {
			t.Fatal("unsafe mutation")
		}
	}
}
func TestAmbiguityAndOrdinal(t *testing.T) {
	candidates := []expense.Expense{{ID: 11, UserID: 42, Amount: 100}, {ID: 22, UserID: 42, Amount: 200}}
	f := &fakeRepo{page: expense.Page{Items: candidates, Total: 2}}
	s := &script{responses: []Message{call("list_expenses", `{"search":"такси"}`), call("delete_expense", `{"id":"22"}`), {Content: "Какую?"}}}
	reply1, session, _ := newAgent(f, s).Reply(context.Background(), 42, 1, "удали такси", Session{})
	if !strings.Contains(reply1, "(#11)") || !strings.Contains(reply1, "(#22)") || session.Pending == nil {
		t.Fatal("missing deterministic choices and pending action")
	}
	if f.mutations != 0 || len(session.Candidates) != 2 {
		t.Fatal("ambiguous mutation allowed or missing candidates")
	}
	if len(s.seen) != 0 || f.filter.Search != "такси" || f.filter.UserID != 42 {
		t.Fatal("simple deletion must use a fresh owner-scoped search without LLM")
	}
	// Round-trip through persisted JSON; the pending action must remain executable
	// even when the provider is unavailable on the next turn.
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var restored Session
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	restored.Choice.State = botstate.AwaitingSelection
	s2 := &script{err: errors.New("LLM offline")}
	reply, session, _ := newAgent(f, s2).Reply(context.Background(), 42, 2, "вторую", restored)
	if len(s2.seen) != 0 || !strings.Contains(reply, "Выполнено") || session.Pending != nil {
		t.Fatal("ordinal should execute trusted pending action without LLM")
	}

	if f.mutations != 1 || f.id != 22 {
		t.Fatal("ordinal selection failed")
	}
}
func TestSimpleUpdateClarifiesThenAppliesSelectedAmount(t *testing.T) {
	f := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14}, {ID: 7}}, Total: 2}}
	provider := &script{err: errors.New("offline")}
	a := newAgent(f, provider)
	reply, session, err := a.Reply(context.Background(), 42, 1, "измени такси", Session{Pending: &PendingAction{Kind: "delete_expense"}})
	if err != nil || !strings.Contains(reply, "Что изменить") || session.Pending != nil || f.mutations != 0 || len(provider.seen) != 0 {
		t.Fatalf("missing safe clarification: %q, err=%v", reply, err)
	}
	data, _ := json.Marshal(session)
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	reply, session, err = a.Reply(context.Background(), 42, 2, "сумму", session)
	if err != nil || !strings.Contains(reply, "Укажите новую сумму") || session.Edit == nil || f.mutations != 0 {
		t.Fatalf("lost edit context: %q, err=%v", reply, err)
	}
	reply, session, err = a.Reply(context.Background(), 42, 3, "500,50", session)
	if err != nil || !strings.HasPrefix(reply, "Какой расход изменить?\nОтветьте номером") || !strings.Contains(reply, "\n\n1.") || session.Pending == nil || f.mutations != 0 {
		t.Fatalf("missing update selection: %q, err=%v", reply, err)
	}
	data, _ = json.Marshal(session)
	if err := json.Unmarshal(data, &session); err != nil {
		t.Fatal(err)
	}
	session.Choice.State = botstate.AwaitingSelection
	_, _, err = a.Reply(context.Background(), 42, 4, "2", session)
	if err != nil || f.mutations != 1 || f.id != 7 || f.patch.Amount == nil || *f.patch.Amount != 50050 || len(provider.seen) != 0 {
		t.Fatalf("wrong update: id=%d patch=%+v err=%v", f.id, f.patch, err)
	}
}

func TestSimpleListCancelsEditAndUsesFreshOwnerSearch(t *testing.T) {
	f := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14}, {ID: 7}}, Total: 2}}
	provider := &script{err: errors.New("offline")}
	reply, session, err := newAgent(f, provider).Reply(context.Background(), 42, 1, "покажи мои расходы за такси", Session{Edit: &EditDraft{Search: "кофе"}, Pending: &PendingAction{Kind: "delete_expense"}})
	if err != nil || f.filter.Search != "такси" || f.filter.UserID != 42 || session.Edit != nil || session.Pending != nil || !strings.Contains(reply, "(#14)") || len(provider.seen) != 0 || f.mutations != 0 {
		t.Fatalf("incorrect fresh list: %q session=%+v err=%v", reply, session, err)
	}
}

func TestSearchAloneDoesNotCreatePendingDeletion(t *testing.T) {
	f := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 11}, {ID: 22}}, Total: 2}}
	s := &script{responses: []Message{call("list_expenses", `{"search":"такси"}`), {Content: "Какую поездку удалить?"}}}
	reply, session, err := newAgent(f, s).Reply(context.Background(), 42, 1, "удали такси за неделю", Session{})
	if err != nil || (strings.Contains(reply, "Какой расход удалить") || strings.Contains(reply, "Ответьте номером")) || !strings.Contains(reply, "(#22)") {
		t.Fatalf("unexpected selection instructions: %q, err=%v", reply, err)
	}
	if f.mutations != 0 || session.Pending != nil || session.Choice == nil {
		t.Fatal("search must preserve selectable candidates without mutating expenses")
	}
}

func TestHistoricalListRequiresFreshSearchBeforeSelection(t *testing.T) {
	f := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14}, {ID: 7}, {ID: 4}}, Total: 3}}
	s := &script{responses: []Message{
		{Content: "Выберите расход:\n1. такси (#14)\n2. такси (#7)\n3. такси (#4)"},
		call("list_expenses", `{"search":"такси"}`),
		call("delete_expense", `{"id":"14"}`),
	}}
	reply, session, err := newAgent(f, s).Reply(context.Background(), 42, 1, "удали такси за неделю", Session{})
	if err != nil || session.Pending == nil || session.Choice == nil || len(session.Candidates) != 3 || !strings.Contains(reply, "(#4)") || f.mutations != 0 {
		t.Fatalf("missing trusted selection: %q, session=%+v err=%v", reply, session, err)
	}
	data, _ := json.Marshal(session)
	var restored Session
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	restored.Choice.State = botstate.AwaitingSelection
	reply, _, err = newAgent(f, &script{err: errors.New("offline")}).Reply(context.Background(), 42, 2, "3", restored)
	if err != nil || f.mutations != 1 || f.id != 4 || !strings.Contains(reply, "Выполнено") {
		t.Fatalf("third selection failed: %q, id=%d err=%v", reply, f.id, err)
	}
}

func TestUnverifiedListIsNeverShown(t *testing.T) {
	f := &fakeRepo{}
	s := &script{responses: []Message{{Content: "1. такси (#14)"}, {Content: "1. такси (#14)"}}}
	reply, session, err := newAgent(f, s).Reply(context.Background(), 42, 1, "удали такси за неделю", Session{})
	if err != nil || strings.Contains(reply, "#14") || session.Choice != nil || f.mutations != 0 {
		t.Fatalf("unverified list exposed: %q, err=%v", reply, err)
	}
}

func TestUpdateRequiresFoundCandidateAndMatchingOldAmount(t *testing.T) {
	for _, tc := range []struct {
		name      string
		session   Session
		wantWrite bool
	}{
		{name: "not found"},
		{name: "old amount differs", session: Session{Candidates: []expense.Expense{{ID: 22, UserID: 42, Amount: 50000}}, CandidateTotal: 1}},
		{name: "old amount matches", session: Session{Candidates: []expense.Expense{{ID: 22, UserID: 42, Amount: 35000}}, CandidateTotal: 1}, wantWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{}
			mutated := false
			_, err := newAgent(repo, &script{}).execute(context.Background(), 42, 1,
				call("update_expense", `{"id":"22","amount_rubles":"450"}`).ToolCalls[0],
				"такси было не 350, а 450", &tc.session, 0, &mutated)
			if tc.wantWrite {
				if err != nil || repo.mutations != 1 || !mutated || repo.patch.ExpectedAmount == nil || *repo.patch.ExpectedAmount != 35000 {
					t.Fatalf("valid correction rejected: %+v %v", repo, err)
				}
			} else if err == nil || repo.mutationAttempts != 0 || mutated {
				t.Fatalf("unverified correction reached storage: %+v %v", repo, err)
			}
		})
	}
}
func TestStepLimit(t *testing.T) {
	f := &fakeRepo{}
	s := &script{}
	for i := 0; i < 10; i++ {
		s.responses = append(s.responses, call("summarize_expenses", `{}`))
	}
	newAgent(f, s).Reply(context.Background(), 42, 1, "отчёт", Session{})
	if len(s.seen) != 6 {
		t.Fatal("unbounded loop")
	}
}
func TestLLMRetryAndCancellation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing key")
		}
		if requests < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Готово"}}]}`)
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL, Key: "secret", Model: "fake", HTTP: server.Client()}
	m, e := c.Complete(context.Background(), []Message{{Role: "user", Content: "test"}}, Tools())
	if e != nil || m.Content != "Готово" || requests != 3 {
		t.Fatalf("retry failed: %v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.Complete(ctx, nil, Tools()); e == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestToolSchemasHaveRequiredArrays(t *testing.T) {
	data, e := json.Marshal(Tools())
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), `"required":null`) {
		t.Fatal("invalid JSON schema required:null")
	}
}
func TestReplayReceiptUsesRecordedOperation(t *testing.T) {
	got := receipt("delete_expense", json.RawMessage(`{"operation":"create_expense","id":"1","amount":"35000","category":"еда","spent_at":"2026-10-17"}`))
	if !strings.Contains(got, "добавлено") || strings.Contains(got, "удалён") {
		t.Fatal(got)
	}
}

func TestShowExpensesRequiresList(t *testing.T) {
	for _, text := range []string{"Покажи траты за неделю", "Покажи расходы на здоровье за этот месяц", "Показать расходы связанные с здоровьем в этом месяце"} {
		f := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 2, Amount: 185000, Category: "здоровье", SpentAt: "2026-10-18", Comment: "Стоматолог"}}, Total: 1}}
		s := &script{responses: []Message{call("summarize_expenses", `{}`), call("list_expenses", `{}`), {Content: "Только итог: 1850"}}}
		reply, _, err := newAgent(f, s).Reply(context.Background(), 42, 1, text, Session{})
		if err != nil || !strings.Contains(reply, "Стоматолог (#2)") || len(s.seen) != 3 {
			t.Fatalf("expected item list for %q, got %q, err=%v", text, reply, err)
		}
		if !strings.Contains(s.seen[1][len(s.seen[1])-1].Content, "list_expenses") {
			t.Fatal("summary tool must instruct the model to use list_expenses")
		}
	}
}

func TestSummaryUsesDatabaseResult(t *testing.T) {
	f := &fakeRepo{page: expense.Page{Total: 2, TotalAmount: 255025}}
	s := &script{responses: []Message{call("summarize_expenses", `{"category":"здоровье"}`), {Content: "**\u200b** Итого 99999 рублей"}}}
	reply, _, err := newAgent(f, s).Reply(context.Background(), 42, 1, "Сколько потратил на здоровье", Session{})
	if err != nil || reply != "Количество расходов: 2. Итого: 2550,25 ₽." || f.filter.UserID != 42 || f.filter.Category != "здоровье" {
		t.Fatalf("wrong summary: %q, err=%v", reply, err)
	}
}

func TestEmptyListDoesNotUseModelClaims(t *testing.T) {
	f := &fakeRepo{}
	s := &script{responses: []Message{call("list_expenses", `{}`), {Content: "Найден расход на 999 рублей"}}}
	reply, _, err := newAgent(f, s).Reply(context.Background(), 42, 1, "Покажи траты за неделю", Session{})
	if err != nil || reply != "По заданным фильтрам расходов не найдено." {
		t.Fatalf("wrong empty list: %q, err=%v", reply, err)
	}
}

func TestExplicitExpenseIDLookup(t *testing.T) {
	for _, found := range []bool{false, true} {
		f := &fakeRepo{}
		if found {
			f.page = expense.Page{Items: []expense.Expense{{ID: 7, UserID: 42, Amount: 150090}}, Total: 1}
		}
		provider := &script{err: errors.New("must not call LLM")}
		reply, session, err := newAgent(f, provider).Reply(context.Background(), 42, 10, "Покажи расход #7", Session{})
		if err != nil || len(provider.seen) != 0 || f.filter.ID != 7 || f.filter.UserID != 42 || f.filter.Limit != 1 {
			t.Fatalf("wrong lookup: %+v %v", f.filter, err)
		}
		if found {
			if !strings.Contains(reply, "(#7)") || session.Choice == nil {
				t.Fatalf("missing own expense: %s", reply)
			}
		} else if reply != "Расход не найден среди ваших расходов." || len(session.Candidates) != 0 {
			t.Fatalf("wrong missing response: %s", reply)
		}
	}
}

func TestForeignExplicitIDMutationNeverCallsLLM(t *testing.T) {
	for _, text := range []string{"Измени сумму расхода #8 на 1 рубль", "Удали расход #8"} {
		f := &fakeRepo{}
		provider := &script{err: errors.New("provider unavailable")}
		reply, session, err := newAgent(f, provider).Reply(context.Background(), 42, 10, text, Session{Candidates: []expense.Expense{{ID: 7}}, CandidateTotal: 1})
		if err != nil || reply != "Расход не найден среди ваших расходов." || f.filter.ID != 8 || f.filter.UserID != 42 || f.mutations != 0 || len(provider.seen) != 0 || len(session.Candidates) != 0 {
			t.Fatalf("unsafe or misleading foreign ID handling: reply=%q filter=%+v err=%v", reply, f.filter, err)
		}
	}
}

func TestExplicitIDCannotMutateDifferentCandidate(t *testing.T) {
	f := &fakeRepo{}
	s := Session{Candidates: []expense.Expense{{ID: 7, UserID: 42, Amount: 100}}, CandidateTotal: 1}
	mutated := false
	_, err := newAgent(f, &script{}).execute(context.Background(), 42, 1, call("delete_expense", `{"id":"7"}`).ToolCalls[0], "Удали расход #8", &s, 0, &mutated)
	if err == nil || f.mutations != 0 {
		t.Fatal("explicit target was replaced")
	}
}

func TestInvalidExpenseEntryDoesNotReachModel(t *testing.T) {
	for _, text := range []string{"кофе -100", "кофе 0", "кофе 0,00", "такси −100 вчера", "кофе 1,001"} {
		f := &fakeRepo{}
		provider := &script{responses: []Message{call("delete_expense", `{"id":"1"}`)}}
		reply, session, err := newAgent(f, provider).Reply(context.Background(), 42, 1, text, Session{Pending: &PendingAction{Kind: "delete_expense"}})
		if err != nil || !strings.Contains(reply, "Сумма расхода должна") || f.mutations != 0 || len(provider.seen) != 0 || session.Pending != nil {
			t.Fatalf("invalid entry accepted: %q -> %q, %v", text, reply, err)
		}
	}
}

func TestLatestCorrectionMismatchIsDeterministic(t *testing.T) {
	f := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 9, UserID: 42, Amount: 60015, Category: "здоровье", Comment: "лекарства 3"}}, Total: 6}}
	provider := &script{err: errors.New("LLM unavailable")}
	reply, _, err := newAgent(f, provider).Reply(context.Background(), 42, 1, "последнее было не 99999, а 100", Session{})
	if err != nil || !strings.Contains(reply, "Прежняя сумма не совпадает") || !strings.Contains(reply, "600,15 ₽") || !strings.Contains(reply, "(#9)") || len(provider.seen) != 0 || f.mutations != 0 || !f.filter.Latest || f.filter.UserID != 42 {
		t.Fatalf("wrong mismatch handling: %q %v", reply, err)
	}
}

// execute exercises the real tool boundary without running the model loop.
func (a *Agent) execute(ctx context.Context, user, update int64, call ToolCall, text string, s *Session, selected int64, mutated *bool) (json.RawMessage, error) {
	return a.executeAuthorized(ctx, user, update, call, text, s, selected, mutated, permissionsFor(text))
}

func (*fakeRepo) Users(context.Context) ([]expense.User, error) { panic("unexpected users query") }
func (*fakeRepo) Create(context.Context, expense.Input) (expense.Expense, error) {
	panic("unexpected panel create")
}
func (*fakeRepo) Update(context.Context, int64, int64, expense.Patch) (expense.Expense, error) {
	panic("unexpected panel update")
}
func (*fakeRepo) Delete(context.Context, int64, int64) error { panic("unexpected panel delete") }
