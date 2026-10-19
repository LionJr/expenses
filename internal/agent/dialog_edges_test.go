package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

func persistedSession(t *testing.T, s Session) Session {
	t.Helper()
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var restored Session
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	return restored
}

func pendingDelete() Session {
	return Session{Candidates: []expense.Expense{{ID: 14}, {ID: 7}}, CandidateTotal: 2,
		Pending: &PendingAction{Kind: "delete_expense", IntentValidated: true},
		Choice:  &botstate.Choice{UpdateID: 1, State: botstate.AwaitingSelection}}
}

func TestInvalidNumericChoicesPreservePendingAction(t *testing.T) {
	for _, text := range []string{"0", "3", "11", "999999999999999999999999", "#999", "id 999"} {
		t.Run(text, func(t *testing.T) {
			repo, llm := &fakeRepo{}, &script{err: errors.New("offline")}
			a := newAgent(repo, llm)
			reply, session, err := a.Reply(context.Background(), 42, 2, text, pendingDelete())
			if err != nil || !strings.Contains(reply, "Такого номера") || session.Pending == nil || session.Choice == nil || len(llm.seen) != 0 || repo.mutations != 0 {
				t.Fatalf("invalid choice lost state: %q session=%+v err=%v", reply, session, err)
			}
			_, _, err = a.Reply(context.Background(), 42, 3, "2", persistedSession(t, session))
			if err != nil || repo.mutations != 1 || repo.id != 7 {
				t.Fatalf("valid retry failed: %+v %v", repo, err)
			}
		})
	}
}

func TestEditInvalidAmountPreservesDraft(t *testing.T) {
	for _, value := range []string{"0", "-10", "−10", "12,345", "92233720368547758.08"} {
		t.Run(value, func(t *testing.T) {
			repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14}, {ID: 7}}, Total: 2}}
			llm := &script{err: errors.New("offline")}
			a := newAgent(repo, llm)
			reply, session, err := a.Reply(context.Background(), 42, 1, value, Session{Edit: &EditDraft{Search: "такси", Field: "amount"}})
			if err != nil || session.Edit == nil || repo.mutations != 0 || len(llm.seen) != 0 || !strings.Contains(reply, "Сумма должна") {
				t.Fatalf("invalid amount lost draft: %q session=%+v err=%v", reply, session, err)
			}
			_, session, err = a.Reply(context.Background(), 42, 2, "1", persistedSession(t, session))
			if err != nil || session.Pending == nil || session.Pending.Patch.Amount == nil || *session.Pending.Patch.Amount != 100 {
				t.Fatalf("numeric amount confused with ordinal: %+v err=%v", session, err)
			}
		})
	}
}

func TestCancelAndNewRequestClearEditDraft(t *testing.T) {
	for _, text := range []string{"отмена", "Отмена!", "покажи мои расходы за такси", "удали такси"} {
		t.Run(text, func(t *testing.T) {
			repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14}, {ID: 7}}, Total: 2}}
			llm := &script{err: errors.New("offline")}
			_, s, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, text, Session{Edit: &EditDraft{Search: "кофе", Field: "amount"}})
			if err != nil || s.Edit != nil || repo.mutations != 0 || len(llm.seen) != 0 {
				t.Fatalf("new request did not cancel draft: %+v err=%v", s, err)
			}
			if strings.HasPrefix(strings.ToLower(text), "отмена") && (s.Pending != nil || s.Choice != nil) {
				t.Fatal("cancel retained choice")
			}
		})
	}
}

func TestNaturalCancellationClearsActionAndRejectsOldChoice(t *testing.T) {
	for _, text := range []string{"no", "NO!", "нет", "Нет.", "не надо", "не   нужно", "передумал", "передумала", "стоп", "cancel", "отмена", "не удаляй"} {
		for _, draft := range []bool{false, true} {
			name := text + "/choice"
			if draft {
				name = text + "/draft"
			}
			t.Run(name, func(t *testing.T) {
				repo, llm := &fakeRepo{}, &script{err: errors.New("must not be called")}
				session := pendingDelete()
				if draft {
					session = Session{Edit: &EditDraft{Search: "такси", Field: "amount"}}
				}
				a := newAgent(repo, llm)
				reply, session, err := a.Reply(context.Background(), 42, 2, text, session)
				if err != nil || reply != "Запрос отменён." || session.Edit != nil || session.Pending != nil || session.Choice != nil || len(session.Candidates) != 0 || repo.mutations != 0 || len(llm.seen) != 0 {
					t.Fatalf("cancellation failed: %q session=%+v err=%v", reply, session, err)
				}
				reply, _, err = a.Reply(context.Background(), 42, 3, "1", persistedSession(t, session))
				if err != nil || !strings.Contains(reply, "Нет активного списка") || repo.mutations != 0 || len(llm.seen) != 0 {
					t.Fatalf("old selection survived cancellation: %q err=%v", reply, err)
				}
			})
		}
	}
}

func TestCancellationDoesNotSwallowQualifiedRequests(t *testing.T) {
	for _, text := range []string{"нет, измени сумму на 500", "no coffee 350", "стоп такси 100", "отмени удаление и покажи расходы"} {
		if cancelReply(text) {
			t.Errorf("qualified request swallowed: %q", text)
		}
	}
}

func TestSimpleMutationEmptyAndSingleMatch(t *testing.T) {
	for _, text := range []string{"удали такси", "измени такси на 12,34"} {
		for _, found := range []bool{false, true} {
			t.Run(text+map[bool]string{false: "/empty", true: "/single"}[found], func(t *testing.T) {
				repo := &fakeRepo{}
				if found {
					repo.page = expense.Page{Items: []expense.Expense{{ID: 14}}, Total: 1}
				}
				llm := &script{err: errors.New("offline")}
				reply, session, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, text, Session{})
				want := 0
				if found {
					want = 1
				}
				if err != nil || repo.mutations != want || len(llm.seen) != 0 || session.Pending != nil || session.Choice != nil || repo.filter.UserID != 42 {
					t.Fatalf("incorrect single/empty mutation: %q %+v err=%v", reply, repo, err)
				}
				if found && (repo.id != 14 || !strings.Contains(reply, "Выполнено")) {
					t.Fatal("wrong mutation target")
				}
				if found && strings.HasPrefix(text, "измени") && (repo.patch.Amount == nil || *repo.patch.Amount != 1234) {
					t.Fatal("wrong amount")
				}
			})
		}
	}
}

func TestQualifiedRequestsDoNotUseSimpleSearch(t *testing.T) {
	for _, text := range []string{"удали такси за вчера", "удали такси кроме последнего", "измени такси на 500 вчера", "покажи расходы за неделю", "покажи расходы за сегодня", "покажи расходы на еду"} {
		t.Run(text, func(t *testing.T) {
			repo := &fakeRepo{}
			llm := &script{responses: []Message{call("clarify_request", `{"field":"request"}`)}}
			_, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, text, Session{})
			if err != nil || len(llm.seen) != 1 || repo.mutations != 0 || repo.filter.Search != "" {
				t.Fatalf("qualified request bypassed model: %+v %v", repo, err)
			}
		})
	}
}

func TestExplicitProhibitionCannotBecomeMutation(t *testing.T) {
	for _, text := range []string{"не удаляй такси", "не меняй такси", "Не трогай последний расход!"} {
		t.Run(text, func(t *testing.T) {
			repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14}}, Total: 1}}
			llm := &script{responses: []Message{call("list_expenses", `{"search":"такси"}`), call("delete_expense", `{"id":"14"}`)}}
			reply, s, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, text, pendingDelete())
			if err != nil || repo.mutations != 0 || len(llm.seen) != 0 || s.Pending != nil || !strings.Contains(reply, "Ничего не изменено") {
				t.Fatalf("prohibition reached mutation path: %q, mutations=%d err=%v", reply, repo.mutations, err)
			}
		})
	}
}
