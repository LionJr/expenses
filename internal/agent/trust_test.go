package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

func TestUnbackedModelClaimsAreNotShownOrRemembered(t *testing.T) {
	for _, test := range []struct{ request, claim string }{
		{"кофе 350", "Добавил кофе: 350 рублей."},
		{"сколько потратил на еду в этом месяце", "Итого: 99999 рублей."},
		{"удали такси за вчера", "Все вчерашние поездки удалены."},
		{"измени дату расхода на вчера", "Дата успешно изменена."},
		{"покажи расходы за октябрь", "Расходов за октябрь нет."},
	} {
		t.Run(test.request, func(t *testing.T) {
			repo := &fakeRepo{}
			llm := &script{responses: []Message{{Content: test.claim}, {Content: test.claim}}}
			reply, session, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, test.request, Session{})
			want, _ := clarification("request")
			if err != nil || reply != want || repo.mutations != 0 || repo.filter.UserID != 0 || len(llm.seen) != 2 {
				t.Fatalf("unbacked claim accepted: %q mutations=%d calls=%d err=%v", reply, repo.mutations, len(llm.seen), err)
			}
			for _, message := range session.History {
				if message.Role == "assistant" && strings.Contains(message.Content, test.claim) {
					t.Fatal("unbacked claim persisted in conversation")
				}
			}
		})
	}
}

func TestMissingToolCallCanRecoverThroughVerifiedOperation(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		repo := &fakeRepo{}
		llm := &script{responses: []Message{
			{Content: "Добавил расход на 99999 рублей."},
			call("create_expense", `{"amount_rubles":"350","category":"еда","spent_at":"2026-10-17","comment":"кофе"}`),
		}}
		reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "кофе 350", Session{})
		if err != nil || repo.mutations != 1 || repo.input.Amount != 35000 || strings.Contains(reply, "99999") || !strings.HasPrefix(reply, "Выполнено: добавлено: 350,00") {
			t.Fatalf("operation recovery failed: %q err=%v", reply, err)
		}
	})
	t.Run("summary", func(t *testing.T) {
		repo := &fakeRepo{page: expense.Page{Total: 2, TotalAmount: 12345}}
		llm := &script{responses: []Message{
			{Content: "Итого: 99999 рублей."},
			call("summarize_expenses", `{"category":"еда","date_from":"2026-10-01","date_to":"2026-10-17"}`),
			{Content: "Итого: 99999 рублей."},
		}}
		reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "сколько потратил на еду в этом месяце", Session{})
		if err != nil || reply != "Количество расходов: 2. Итого: 123,45 ₽." || repo.filter.UserID != 42 {
			t.Fatalf("summary recovery failed: %q err=%v", reply, err)
		}
	})
}

func TestClarificationUsesValidatedFieldAndApplicationQuestion(t *testing.T) {
	for _, field := range []string{"amount", "date", "category", "comment", "expense", "single_expense", "request"} {
		t.Run(field, func(t *testing.T) {
			repo := &fakeRepo{}
			message := call("clarify_request", `{"field":"`+field+`"}`)
			message.Content = "Добавил расход на 99999 рублей."
			llm := &script{responses: []Message{message}}
			reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "уточнение", Session{})
			want, _ := clarification(field)
			if err != nil || reply != want || repo.mutations != 0 || len(llm.seen) != 1 {
				t.Fatalf("unsafe clarification: %q err=%v", reply, err)
			}
		})
	}
	for _, args := range []string{`{"field":"Добавил расход на 99999 рублей."}`, `{"field":"amount","question":"Добавил расход на 99999 рублей."}`, `{}`, `{"field":null}`} {
		message := call("clarify_request", args)
		mutated := false
		if _, err := newAgent(&fakeRepo{}, &script{}).execute(context.Background(), 42, 1, message.ToolCalls[0], "кофе", &Session{}, 0, &mutated); err == nil {
			t.Fatalf("untrusted clarification accepted: %s", args)
		}
	}
}

func TestReadListSelectionCannotReachMutationOrModel(t *testing.T) {
	for _, tc := range []struct {
		text  string
		valid bool
	}{{"0", false}, {"3", false}, {"11", false}, {"#999", false}, {"id 999", false}, {"2", true}, {"вторую", true}, {"#7", true}} {
		text := tc.text
		t.Run(text, func(t *testing.T) {
			repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14}}, Total: 1}}
			llm := &script{responses: []Message{call("list_expenses", `{"search":"такси"}`), call("delete_expense", `{"id":"14"}`)}}
			session := Session{Candidates: []expense.Expense{{ID: 14}, {ID: 7}}, CandidateTotal: 2,
				Choice:  &botstate.Choice{UpdateID: 1, State: botstate.AwaitingSelection},
				History: []Message{{Role: "user", Content: "удали такси"}, {Role: "assistant", Content: "Отменено."}, {Role: "user", Content: "покажи такси"}}}
			reply, session, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, text, persistedSession(t, session))
			if err != nil || len(llm.seen) != 0 || repo.mutations != 0 || len(session.Candidates) != 2 || session.Choice == nil {
				t.Fatalf("read selection reached model or lost list: %q err=%v", reply, err)
			}
			if !tc.valid {
				if !strings.Contains(reply, "Такого номера") {
					t.Fatal(reply)
				}
			} else if !strings.Contains(reply, "Выбран расход #7") {
				t.Fatal("wrong read candidate: " + reply)
			}
		})
	}
}

func TestMonthsAndUnknownWordsDoNotBypassPeriodInterpretation(t *testing.T) {
	for _, word := range []string{"январь", "февраль", "март", "апрель", "май", "июнь", "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь", "неделю", "выходные", "квартал", "праздники"} {
		t.Run(word, func(t *testing.T) {
			repo := &fakeRepo{}
			llm := &script{responses: []Message{call("clarify_request", `{"field":"date"}`)}}
			_, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "покажи расходы за "+word, Session{})
			if err != nil || len(llm.seen) != 1 || repo.filter.Search != "" || repo.filter.UserID != 0 {
				t.Fatalf("period became a comment search: %+v err=%v", repo.filter, err)
			}
		})
	}
	t.Run("October date filter", func(t *testing.T) {
		repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14, SpentAt: "2026-10-17"}}, Total: 1}}
		llm := &script{responses: []Message{call("list_expenses", `{"date_from":"2026-10-01","date_to":"2026-10-31"}`), {Content: "Ничего нет."}}}
		reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "покажи расходы за октябрь", Session{})
		if err != nil || repo.filter.From != "2026-10-01" || repo.filter.To != "2026-10-31" || repo.filter.Search != "" || !strings.Contains(reply, "(#14)") {
			t.Fatalf("incorrect period list: %q %+v err=%v", reply, repo.filter, err)
		}
	})
}
