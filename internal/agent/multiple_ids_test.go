package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/LionJr/expenses/internal/expense"
)

func TestMultipleExplicitIDsCannotReachModelOrStorage(t *testing.T) {
	for _, text := range []string{
		"удали расходы #14 и #7",
		"измени расходы #14 и #7 на 500",
		"удали #14, но #7 не трогай",
		"покажи расходы #14 и #7",
		"удали #14 и #14",
		"удали #0 и #14",
		"удали #999999999999999999999999 и #14",
	} {
		t.Run(text, func(t *testing.T) {
			repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14, UserID: 42, Amount: 100}}, Total: 1}}
			llm := &script{responses: []Message{call("list_expenses", `{}`), call("delete_expense", `{"id":"14"}`)}}
			a := newAgent(repo, llm)
			reply, session, err := a.Reply(context.Background(), 42, 2, text, pendingDelete())
			if err != nil || !strings.Contains(reply, "Укажите только один расход") || repo.listCalls != 0 || repo.mutationAttempts != 0 || len(llm.seen) != 0 || session.Pending != nil || session.Request != nil || session.Choice != nil || len(session.Candidates) != 0 {
				t.Fatalf("ambiguous targets were processed: reply=%q session=%+v repo=%+v err=%v", reply, session, repo, err)
			}
			_, _, err = a.Reply(context.Background(), 42, 3, "1", persistedSession(t, session))
			if err != nil || repo.mutationAttempts != 0 || len(llm.seen) != 0 {
				t.Fatal("old pending action survived ambiguous request")
			}
		})
	}
}

func TestMergedClarificationCannotIntroduceSecondID(t *testing.T) {
	repo, llm := &fakeRepo{}, &script{}
	session := Session{Request: &RequestDraft{Text: "измени дату расхода #14", Field: "date", Permissions: permissionsFor(expense.TMT, "измени дату расхода #14")}}
	reply, session, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, "вчера #7", persistedSession(t, session))
	if err != nil || !strings.Contains(reply, "Укажите только один расход") || session.Request != nil || len(llm.seen) != 0 || repo.mutationAttempts != 0 {
		t.Fatalf("second ID accepted through clarification: %q %v", reply, err)
	}
}

func TestToolBoundaryRejectsMultipleExplicitTargets(t *testing.T) {
	for _, tool := range []Message{call("list_expenses", `{}`), call("delete_expense", `{"id":"14"}`), call("update_expense", `{"id":"14","amount_decimal":"500"}`)} {
		repo := &fakeRepo{}
		session := Session{Candidates: []expense.Expense{{ID: 14, UserID: 42, Amount: 100}}, CandidateTotal: 1}
		mutated := false
		text := "удали расходы #14 и #7"
		if tool.ToolCalls[0].Function.Name == "update_expense" {
			text = "измени расходы #14 и #7 на 500"
		}
		if tool.ToolCalls[0].Function.Name == "list_expenses" {
			text = "покажи расходы #14 и #7"
		}
		_, err := newAgent(repo, &script{}).execute(context.Background(), 42, 1, tool.ToolCalls[0], text, &session, 14, &mutated)
		if err == nil || mutated || repo.mutationAttempts != 0 || repo.listCalls != 0 {
			t.Fatalf("tool bypassed multiple-target guard: %s %v", tool.ToolCalls[0].Function.Name, err)
		}
	}
}

func TestSingleExplicitTargetStillDeletes(t *testing.T) {
	repo := &fakeRepo{page: expense.Page{Items: []expense.Expense{{ID: 14, UserID: 42, Amount: 100}}, Total: 1}}
	llm := &script{responses: []Message{call("list_expenses", `{}`), call("delete_expense", `{"id":"14"}`)}}
	reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "удали расход #14", Session{})
	if err != nil || repo.mutations != 1 || repo.id != 14 || !strings.Contains(reply, "расход удалён") {
		t.Fatalf("valid single target rejected: %q %v", reply, err)
	}
}

func TestCommittedMultipleTargetRequestRecoversWithoutRepeating(t *testing.T) {
	repo := &fakeRepo{saved: json.RawMessage(`{"operation":"delete_expense","deleted_id":"14"}`)}
	llm := &script{}
	reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 1, "удали расходы #14 и #7", Session{})
	if err != nil || !strings.Contains(reply, "Выполнено: расход удалён") || repo.mutationAttempts != 0 || repo.listCalls != 0 || len(llm.seen) != 0 {
		t.Fatalf("committed receipt not recovered: %q %v", reply, err)
	}
}
