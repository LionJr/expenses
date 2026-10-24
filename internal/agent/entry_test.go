package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/expense"
)

func TestCategoryReplyCreatesWithoutAnotherModelCall(t *testing.T) {
	for _, category := range expense.Categories() {
		t.Run(category, func(t *testing.T) {
			repo := &fakeRepo{}
			llm := &script{responses: []Message{call("clarify_request", `{"field":"category"}`)}, err: errors.New("model must not be called again")}
			a := newAgent(repo, llm)
			answer, session, err := a.Reply(context.Background(), 42, 1, "парикмахер 40", Session{})
			if err != nil || session.Request == nil || !strings.Contains(answer, "Укажите категорию") || repo.mutations != 0 {
				t.Fatalf("missing category draft: %q %+v %v", answer, session, err)
			}
			// Clarifications can arrive after midnight or a process restart.
			a.Now = func() time.Time { return time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC) }
			answer, session, err = a.Reply(context.Background(), 42, 2, "  "+strings.ToUpper(category)+"  ", persistedSession(t, session))
			if err != nil || !strings.Contains(answer, "Выполнено: добавлено") || session.Request != nil || len(llm.seen) != 1 || repo.mutations != 1 {
				t.Fatalf("category not completed: %q %+v %v", answer, session, err)
			}
			if repo.input.Amount != 4000 || repo.input.Category != category || repo.input.Comment != "парикмахер" || repo.input.SpentAt != "2026-10-17" || repo.input.UserID != 42 || repo.key != mutationKey(2) {
				t.Fatalf("wrong expense: %+v key=%s", repo.input, repo.key)
			}
		})
	}
}

func TestInvalidCategoryPreservesDraft(t *testing.T) {
	repo, llm := &fakeRepo{}, &script{err: errors.New("offline")}
	a := newAgent(repo, llm)
	session := Session{Request: &RequestDraft{Text: "парикмахер 40", Field: "category", Permissions: requestPermissions{Mutation: toolCreateExpense}}}
	answer, session, err := a.Reply(context.Background(), 42, 2, "неизвестная", session)
	if err != nil || !strings.Contains(answer, "Укажите категорию") || session.Request == nil || repo.mutations != 0 || len(llm.seen) != 0 {
		t.Fatalf("invalid category lost draft: %q %+v %v", answer, session, err)
	}
	answer, session, err = a.Reply(context.Background(), 42, 3, "другое", persistedSession(t, session))
	if err != nil || !strings.Contains(answer, "Выполнено") || session.Request != nil || repo.mutations != 1 || repo.input.SpentAt != "2026-10-17" {
		t.Fatalf("valid retry failed: %q %+v %v", answer, session, err)
	}
}

func TestCategoryReplyDoesNotOverrideNewIntent(t *testing.T) {
	for _, text := range []string{"отмена", "не добавляй парикмахер", "покажи расходы", "kofe 115"} {
		t.Run(text, func(t *testing.T) {
			repo, llm := &fakeRepo{}, &script{responses: []Message{call("clarify_request", `{"field":"request"}`)}}
			a := newAgent(repo, llm)
			session := Session{Request: &RequestDraft{Text: "парикмахер 40", Field: "category", Permissions: requestPermissions{Mutation: toolCreateExpense}}}
			_, session, err := a.Reply(context.Background(), 42, 2, text, session)
			if err != nil || session.Request != nil || (repo.mutations > 0 && repo.input.Comment != "kofe") {
				t.Fatalf("old draft applied to new intent: %+v %+v %v", repo.input, session, err)
			}
		})
	}
}

func TestTransliteratedEntriesDoNotNeedModel(t *testing.T) {
	for _, test := range []struct {
		text, comment, category string
		amount                  int64
	}{
		{"kofe 115", "kofe", "еда", 11500},
		{"Doner 75", "Doner", "еда", 7500},
		{"döner 75", "döner", "еда", 7500},
		{"taksi 35", "taksi", "транспорт", 3500},
		{"koynek 700", "koynek", "покупки", 70000},
		{"köýnek 700,50", "köýnek", "покупки", 70050},
	} {
		t.Run(test.text, func(t *testing.T) {
			repo, llm := &fakeRepo{}, &script{err: errors.New("offline")}
			answer, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, test.text, Session{})
			if err != nil || !strings.Contains(answer, "Выполнено") || len(llm.seen) != 0 || repo.mutations != 1 || repo.input.Amount != test.amount || repo.input.Category != test.category || repo.input.Comment != test.comment || repo.input.SpentAt != "2026-10-17" {
				t.Fatalf("entry failed: %q %+v %v", answer, repo.input, err)
			}
		})
	}
}

func TestEntryShortcutDoesNotGuessComplexRequests(t *testing.T) {
	for _, text := range []string{"kofe 115 вчера", "kofe 115 и taksi 25", "не добавляй kofe 115", "kofe 0", "kofe -10", "kofe 12.345", "kofe 92233720368547758.08", "сколько kofe 115", "парикмахер 40"} {
		t.Run(text, func(t *testing.T) {
			repo, llm := &fakeRepo{}, &script{responses: []Message{call("clarify_request", `{"field":"request"}`)}}
			_, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, text, Session{})
			if err != nil || repo.mutations != 0 {
				t.Fatalf("unexpected mutation for %q: %+v %v", text, repo.input, err)
			}
		})
	}
}

func TestCategoryCreationStorageFailureIsRetryable(t *testing.T) {
	repo := &fakeRepo{mutationError: errors.New("storage offline")}
	session := Session{Request: &RequestDraft{Text: "парикмахер 40", Field: "category", Permissions: requestPermissions{Mutation: toolCreateExpense}}}
	answer, _, err := newAgent(repo, &script{}).Reply(context.Background(), 42, 2, "другое", session)
	var processing *ProcessingError
	if !errors.As(err, &processing) || answer != "" || repo.mutations != 0 {
		t.Fatalf("storage error not retryable: %q %v", answer, err)
	}
}
