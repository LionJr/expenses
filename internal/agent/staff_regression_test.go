package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LionJr/expenses/internal/expense"
)

func TestMutationMustMatchCurrentRequest(t *testing.T) {
	for _, text := range []string{"покажи траты за неделю", "сколько потратил на еду в этом месяце", "измени дату расхода #14 на вчера", "если я скажу удали последний расход", "не добавляй кофе 350", "расскажи как удалить кофе 350"} {
		for _, wrong := range []Message{call("delete_expense", `{"id":"14"}`), call("create_expense", `{"amount_decimal":"1","category":"еда","spent_at":"2026-10-17"}`)} {
			t.Run(text+"/"+wrong.ToolCalls[0].Function.Name, func(t *testing.T) {
				repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14, UserID: 42, Amount: 10000}}, Total: 1}}
				llm := &script{responses: []Message{call("list_expenses", `{}`), wrong}}
				_, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, text, Session{})
				if err != nil || repo.mutations != 0 {
					t.Fatalf("request permitted wrong write: %q %+v %v", text, repo, err)
				}
			})
		}
	}
}

func TestUpgradeDoesNotExecuteUnvalidatedLegacyAction(t *testing.T) {
	repo := &fakeRepo{}
	llm := &script{responses: []Message{call("delete_expense", `{"id":"14"}`)}}
	s := pendingDelete()
	s.Pending.IntentValidated = false
	reply, s, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, "1", persistedSession(t, s))
	if err != nil || repo.mutations != 0 || len(llm.seen) != 0 || s.Pending != nil || !strings.Contains(reply, "Выбран расход #14") {
		t.Fatalf("legacy action executed: %q %v", reply, err)
	}
}

func TestUpdateMustMatchRequestedFields(t *testing.T) {
	for _, test := range []struct{ text, args string }{
		{"измени дату расхода #14 на вчера", `{"id":"14","amount_decimal":"450"}`},
		{"измени сумму расхода #14 на 450", `{"id":"14","amount_decimal":"450","comment":"unrequested"}`},
		{"измени комментарий расхода #14 на подарок", `{"id":"14","spent_at":"2026-10-16"}`},
	} {
		repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14, UserID: 42}}, Total: 1}}
		llm := &script{responses: []Message{call("list_expenses", `{}`), call("update_expense", test.args)}}
		_, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, test.text, Session{})
		if err != nil || repo.mutations != 0 {
			t.Fatalf("extra field allowed for %q: %+v %v", test.text, repo, err)
		}
	}
	for _, test := range []struct{ text, args string }{
		{"измени дату расхода #14 на вчера", `{"id":"14","spent_at":"2026-10-16"}`},
		{"измени комментарий расхода #14 на подарок", `{"id":"14","comment":"подарок"}`},
	} {
		repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14, UserID: 42}}, Total: 1}}
		llm := &script{responses: []Message{call("list_expenses", `{}`), call("update_expense", test.args)}}
		_, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, test.text, Session{})
		if err != nil || repo.mutations != 1 {
			t.Fatalf("valid update rejected: %q %+v %v", test.text, repo, err)
		}
	}
}

func TestToolsMatchApplicationPermissions(t *testing.T) {
	for _, tc := range []struct{ text, wantMutation string }{
		{"покажи траты за неделю", ""}, {"кофе 350", "create_expense"},
		{"измени дату #14 на вчера", "update_expense"}, {"удали расход #14", "delete_expense"}, {"привет", ""},
	} {
		t.Run(tc.text, func(t *testing.T) {
			found := ""
			for _, tool := range permittedTools(expense.TMT, permissionsFor(expense.TMT, tc.text)) {
				switch tool.Function.Name {
				case "create_expense", "update_expense", "delete_expense":
					if found != "" {
						t.Fatal("multiple mutation tools exposed")
					}
					found = tool.Function.Name
				}
			}
			if found != tc.wantMutation {
				t.Fatalf("mutation tool=%q want=%q", found, tc.wantMutation)
			}
		})
	}
}

func TestFieldPermissionUsesLabelsBeforeValue(t *testing.T) {
	if p := permissionsFor(expense.TMT, "измени\tкомментарий\tна\tдату"); p.Fields != fieldComment {
		t.Fatalf("value granted field permission: %+v", p)
	}
	if p := permissionsFor(expense.TMT, "измени датчик на 500"); p.Fields != fieldAmount {
		t.Fatalf("expense name confused with date field: %+v", p)
	}
	if p := permissionsFor(expense.TMT, "купил кофе за триста"); p.Mutation != "create_expense" {
		t.Fatal("explicit creation intent rejected")
	}
	if p := permissionsFor(expense.TMT, "не купил кофе 350"); p.Mutation != "" {
		t.Fatal("negative expense report permitted creation")
	}
}

func TestOldAmountMustValidateWholeToken(t *testing.T) {
	for _, amount := range []string{"350,555", "350.555", "350,509", "350.55.5", "350,,55", "350e3", "350_55", "350abc", "1 000", "+350", "-350", "−350", "92233720368547758.08"} {
		t.Run(amount, func(t *testing.T) {
			repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14, Amount: 35055}}, Total: 1}}
			llm := &script{responses: []Message{call("list_expenses", `{"search":"такси"}`), call("update_expense", `{"id":"14","amount_decimal":"450","expected_amount_decimal":"350.555"}`)}}
			reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "такси было не "+amount+", а 450", Session{})
			if err != nil || repo.mutations != 0 || len(llm.seen) != 0 || !strings.Contains(reply, "Прежняя сумма должна") {
				t.Fatalf("invalid old amount accepted: %q %+v %v", reply, repo, err)
			}
		})
	}
}

func TestPreviousAmountRecognizesPunctuationAndCurrency(t *testing.T) {
	for text, want := range map[string]int64{"такси было не 350, а 450": 35000, "такси было не 350,50, а 450": 35050, "такси было не 350ман., а 450": 35000, "такси было не 350,50 TMT, а 450": 35050, "такси было не 350,50 манатов, а 450": 35050, "такси было не 350,50 manat, а 450": 35050} {
		token, ok := previousAmount(expense.TMT, text)
		got, err := expense.ParseAmount(token)
		if !ok || err != nil || got != want {
			t.Fatalf("%q: token=%q amount=%d err=%v", text, token, got, err)
		}
	}
}

func TestManatEntriesKeepOwnerAndDecimalPrecision(t *testing.T) {
	for _, text := range []string{"кофе 12,34", "кофе 12,34 TMT", "кофе 12.34 tmt", "кофе 12,34 манат", "кофе 12,34 маната", "кофе 12,34 манатов", "кофе 12,34ман.", "кофе 12,34 manat"} {
		t.Run(text, func(t *testing.T) {
			repo := &fakeRepo{}
			llm := &script{responses: []Message{call("create_expense", `{"amount_decimal":"12.34","category":"еда","spent_at":"2026-10-17","comment":"кофе"}`)}}
			reply, _, err := newAgent(repo, llm).Reply(t.Context(), 42, 7, text, Session{})
			if err != nil || repo.mutations != 1 || repo.owner != 42 || repo.input.Amount != 1234 || !strings.Contains(reply, "12,34 TMT") {
				t.Fatalf("wrong TMT expense: reply=%q owner=%d amount=%d writes=%d err=%v", reply, repo.owner, repo.input.Amount, repo.mutations, err)
			}
			if len(llm.seen) != 1 || !strings.Contains(llm.seen[0][0].Content, "TMT (Туркменский манат)") {
				t.Fatal("LLM did not receive the TMT currency context")
			}
		})
	}
}

func TestCreateAmountClarificationSurvivesPersistence(t *testing.T) {
	for _, value := range []string{"350", "350,50"} {
		t.Run(value, func(t *testing.T) {
			repo := &fakeRepo{}
			llm := &script{responses: []Message{call("clarify_request", `{"field":"amount"}`), call("create_expense", `{"amount_decimal":"`+value+`","category":"еда","spent_at":"2026-10-17","comment":"кофе"}`)}}
			a := newAgent(repo, llm)
			_, session, err := a.Reply(context.Background(), 42, 1, "кофе", Session{})
			if err != nil || session.Request == nil {
				t.Fatalf("missing draft: %+v %v", session, err)
			}
			session = persistedSession(t, session)
			reply, session, err := a.Reply(context.Background(), 42, 2, "0", session)
			if err != nil || session.Request == nil || repo.mutations != 0 || !strings.Contains(reply, "положительную сумму") {
				t.Fatal("invalid amount lost draft")
			}
			_, session, err = a.Reply(context.Background(), 42, 3, value, persistedSession(t, session))
			want, _ := expense.ParseAmount(value)
			if err != nil || repo.mutations != 1 || repo.input.Amount != want || session.Request != nil {
				t.Fatalf("amount confused with ordinal: %+v %v", session, err)
			}
		})
	}
}

func TestCreateDateClarificationRetainsPermission(t *testing.T) {
	repo := &fakeRepo{}
	llm := &script{responses: []Message{call("clarify_request", `{"field":"date"}`), call("create_expense", `{"amount_decimal":"350","category":"еда","spent_at":"2026-10-16","comment":"кофе"}`)}}
	a := newAgent(repo, llm)
	_, s, err := a.Reply(context.Background(), 42, 1, "кофе 350", Session{})
	if err != nil || s.Request == nil {
		t.Fatal("date draft missing")
	}
	_, _, err = a.Reply(context.Background(), 42, 2, "вчера", persistedSession(t, s))
	if err != nil || repo.mutations != 1 || repo.input.SpentAt != "2026-10-16" {
		t.Fatalf("date continuation failed: %+v %v", repo, err)
	}
}

func TestClarificationContextCannotGrowWithoutBound(t *testing.T) {
	repo := &fakeRepo{}
	llm := &script{}
	s := Session{Request: &RequestDraft{Text: strings.Repeat("я", 1990), Field: "date", Permissions: requestPermissions{ReadOnly: true}}}
	reply, s, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "за прошлую неделю", s)
	if err != nil || s.Request != nil || len(llm.seen) != 0 || repo.mutations != 0 || !strings.Contains(reply, "Уточнений слишком много") {
		t.Fatalf("unbounded clarification retained: %q %v", reply, err)
	}
}

func TestClarificationCannotUpgradeReadPermission(t *testing.T) {
	repo := &fakeRepo{}
	llm := &script{responses: []Message{call("clarify_request", `{"field":"amount"}`), call("create_expense", `{"amount_decimal":"350","category":"еда","spent_at":"2026-10-17"}`)}}
	a := newAgent(repo, llm)
	_, s, err := a.Reply(context.Background(), 42, 1, "сколько потратил на кофе", Session{})
	if err != nil || s.Request == nil {
		t.Fatal("draft missing")
	}
	_, _, err = a.Reply(context.Background(), 42, 2, "350", persistedSession(t, s))
	if err != nil || repo.mutations != 0 {
		t.Fatal("read clarification permitted creation")
	}
}

func TestNewIntentAndCancellationClearCreationDraft(t *testing.T) {
	for _, text := range []string{"нет", "не добавляй кофе 350", "покажи траты за неделю"} {
		repo := &fakeRepo{}
		llm := &script{responses: []Message{call("clarify_request", `{"field":"request"}`)}}
		a := newAgent(repo, llm)
		draft := Session{Request: &RequestDraft{Text: "кофе", Field: "amount"}}
		_, s, err := a.Reply(context.Background(), 42, 1, text, draft)
		if err != nil || s.Request != nil || repo.mutations != 0 {
			t.Fatalf("new intent retained creation: %q %+v %v", text, s, err)
		}
		reply, _, err := a.Reply(context.Background(), 42, 2, "350", persistedSession(t, s))
		if err != nil || !strings.Contains(reply, "Нет активного списка") || repo.mutations != 0 {
			t.Fatal("old draft survived cancellation")
		}
	}
}

func TestNaturalMutationAliases(t *testing.T) {
	for input, want := range map[string]string{"удали аренду": "аренда", "удали стоматолога": "стоматолог", "удали покупку": "покупк"} {
		if got := simpleDeleteSearch(input); got != want {
			t.Fatalf("%q: %q want %q", input, got, want)
		}
	}
	if simpleDeleteSearch("удали холодильник") != "" {
		t.Fatal("unknown inflection bypasses interpretation")
	}
}

func TestInvalidTextNeverReachesMutationOrPersistedHistory(t *testing.T) {
	repo := &fakeRepo{}
	llm := &script{responses: []Message{call("create_expense", `{"amount_decimal":"1","category":"еда","spent_at":"2026-10-17","comment":"bad\u0000comment"}`)}}
	reply, s, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "кофе 1", Session{})
	if err != nil || repo.mutations != 0 || strings.ContainsRune(reply, 0) {
		t.Fatal("unsafe model text accepted")
	}
	data, _ := json.Marshal(s)
	if strings.Contains(string(data), `\u0000`) {
		t.Fatal("unsafe history persisted")
	}
	_, s, err = newAgent(repo, &script{}).Reply(context.Background(), 42, 2, "кофе\x00 1", Session{})
	data, _ = json.Marshal(s)
	if err != nil || strings.Contains(string(data), `\u0000`) {
		t.Fatal("unsafe incoming text persisted")
	}
}

func TestInvalidEditCannotBecomePendingAction(t *testing.T) {
	repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14, UserID: 42}, {ID: 7, UserID: 42}}, Total: 2}}
	llm := &script{responses: []Message{
		call("list_expenses", `{"search":"такси"}`),
		call("update_expense", `{"id":"14","category":"нет"}`),
	}}
	reply, session, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "измени категорию такси на нет", Session{})
	if err != nil || session.Pending != nil || repo.mutations != 0 || repo.mutationAttempts != 0 || !strings.Contains(reply, "неизвестная категория") {
		t.Fatalf("invalid edit staged: reply=%q session=%+v mutations=%d err=%v", reply, session, repo.mutations, err)
	}
}
