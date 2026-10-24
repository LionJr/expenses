package agent

import (
	"strings"
	"testing"

	"github.com/LionJr/expenses/internal/expense"
)

func TestConfiguredCurrencyFlowsThroughCreateReportsAndRecovery(t *testing.T) {
	for _, code := range []expense.Currency{expense.TMT, expense.RUB, expense.USD, expense.EUR, expense.AZN} {
		t.Run(code.Code(), func(t *testing.T) {
			repo := &fakeRepo{}
			llm := &script{responses: []Message{call("create_expense", `{"amount_decimal":"12.34","category":"еда","spent_at":"2026-10-17","comment":"кофе"}`)}}
			a := newAgent(repo, llm)
			a.Currency = code
			reply, _, err := a.Reply(t.Context(), 42, 1, "кофе 12,34 "+code.Code(), Session{})
			if err != nil || repo.mutations != 1 || repo.input.Amount != 1234 || repo.owner != 42 || !strings.Contains(reply, "12,34 "+code.Code()) {
				t.Fatalf("wrong configured create: %q %v", reply, err)
			}
			if !strings.Contains(llm.seen[0][0].Content, code.Code()+" ("+code.Name()+")") {
				t.Fatal("wrong model currency context")
			}
			// A committed mutation is replayed without a new LLM/write.
			repo.saved = []byte(`{"operation":"create_expense","id":"1","amount":"1234","category":"еда","spent_at":"2026-10-17"}`)
			reply, _, err = a.Reply(t.Context(), 42, 1, "кофе 12,34 "+code.Code(), Session{})
			if err != nil || repo.mutations != 1 || len(llm.seen) != 1 || !strings.Contains(reply, "12,34 "+code.Code()) {
				t.Fatal("recovery lost currency or repeated mutation")
			}
			repo.saved = nil
			repo.page = expense.Page{Total: 1, TotalAmount: 1234}
			llm.responses = []Message{call("summarize_expenses", `{}`), {Content: ""}}
			reply, _, err = a.Reply(t.Context(), 42, 2, "сколько потратил", Session{})
			if err != nil || reply != "Количество расходов: 1. Итого: 12,34 "+code.Code()+"." || repo.filter.UserID != 42 {
				t.Fatalf("wrong configured report: %q %v", reply, err)
			}
			question, err := clarification(code, "amount")
			if err != nil || !strings.Contains(question, code.Code()) {
				t.Fatal("clarification lost configured currency")
			}
		})
	}
}

func TestForeignCurrencyCannotAuthorizeWriteOrReusePendingAction(t *testing.T) {
	for _, code := range []expense.Currency{expense.TMT, expense.RUB, expense.USD, expense.EUR, expense.AZN} {
		for _, other := range []expense.Currency{expense.TMT, expense.RUB, expense.USD, expense.EUR, expense.AZN} {
			if code == other {
				continue
			}
			repo, llm := &fakeRepo{}, &script{}
			a := newAgent(repo, llm)
			a.Currency = code
			session := Session{Request: &RequestDraft{Text: "кофе", Field: "amount"}}
			reply, result, err := a.Reply(t.Context(), 42, 3, "добавь кофе 10 "+other.Code(), session)
			if err != nil || repo.mutations != 0 || len(llm.seen) != 0 || result.Request != nil || !strings.Contains(reply, "Приложение использует "+code.Code()) {
				t.Fatalf("%s accepted %s: %q %v", code, other, reply, err)
			}
		}
	}
}
