package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

func TestChoiceRequiresDeliveryAcknowledgement(t *testing.T) {
	for _, state := range []botstate.ChoiceState{botstate.AwaitingDelivery, ""} {
		t.Run(string(state), func(t *testing.T) {
			repo := &fakeRepo{}
			llm := &script{err: errors.New("must not be called")}
			s := Session{Candidates: []expense.Expense{{ID: 11}, {ID: 22}}, CandidateTotal: 2, Pending: &PendingAction{Kind: "delete_expense", IntentValidated: true}, Choice: &botstate.Choice{UpdateID: 1, State: state}}
			reply, s, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, "вторую", s)
			if err != nil || repo.mutations != 0 || len(llm.seen) != 0 || !strings.Contains(reply, "ещё не отправлен") || s.Choice.UpdateID != 1 {
				t.Fatalf("undelivered list became actionable: %s %v", reply, err)
			}
		})
	}
}
func TestLegacyChoiceIsReissuedBeforeMutation(t *testing.T) {
	repo := &fakeRepo{}
	llm := &script{err: errors.New("must not be called")}
	s := Session{Candidates: []expense.Expense{{ID: 11}, {ID: 22}}, CandidateTotal: 2, Pending: &PendingAction{Kind: "delete_expense", IntentValidated: true}}
	reply, s, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, "вторую", s)
	if err != nil || repo.mutations != 0 || len(llm.seen) != 0 || s.Choice == nil || s.Choice.UpdateID != 2 || s.Choice.State != botstate.AwaitingDelivery || !strings.Contains(reply, "(#22)") {
		t.Fatal("legacy session was not safely reissued")
	}
}
func TestExpiredChoiceDoesNotInvokeModel(t *testing.T) {
	repo := &fakeRepo{}
	llm := &script{err: errors.New("must not be called")}
	reply, _, err := newAgent(repo, llm).Reply(context.Background(), 42, 2, "вторую", Session{})
	if err != nil || repo.mutations != 0 || len(llm.seen) != 0 || !strings.Contains(reply, "Нет активного списка") {
		t.Fatal("expired choice reached model")
	}
}
func TestLatestShortcutUsesWholeUnqualifiedRequest(t *testing.T) {
	for _, text := range []string{"удали последний расход", "удали последнюю поездку", "Покажи последнюю трату!", "последнее было не 350, а 450", "последняя трата была не 350.50, а 450.50", "измени последний расход на 450", "пожалуйста, удали последний расход", "удали последний расход, пожалуйста"} {
		if !latestRequested(text) {
			t.Errorf("clear request not recognized: %q", text)
		}
	}
	for _, text := range []string{"удали такси, последнее не трогай", "удали такси, кроме последнего", "последнее не удаляй, удали такси", "удали последний расход, но не последний", "удали не последнюю поездку", "удали предпоследнюю поездку", "не надо удалять последний расход", "если я скажу удали последний расход", "удали последнюю поездку кроме вчерашней", "удали последний расход без удаления последнего", "не последний расход", "последний расход не удалять", "удали такси и покажи последний расход"} {
		if latestRequested(text) {
			t.Errorf("qualified request authorized latest: %q", text)
		}
	}
}
