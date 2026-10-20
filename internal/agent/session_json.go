package agent

import (
	"encoding/json"

	"github.com/LionJr/expenses/internal/expensewire"
)

func (s Session) MarshalJSON() ([]byte, error) {
	type plain Session
	return json.Marshal(struct {
		*plain
		Candidates []expensewire.Expense `json:"candidates"`
	}{plain: (*plain)(&s), Candidates: expensewire.FromExpenses(s.Candidates)})
}
func (s *Session) UnmarshalJSON(data []byte) error {
	type plain Session
	var value struct {
		plain
		Candidates []expensewire.Expense `json:"candidates"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = Session(value.plain)
	s.Candidates = expensewire.Expenses(value.Candidates)
	return nil
}
func (p PendingAction) MarshalJSON() ([]byte, error) {
	type plain PendingAction
	return json.Marshal(struct {
		*plain
		Patch expensewire.Patch `json:"patch"`
	}{plain: (*plain)(&p), Patch: expensewire.FromPatch(p.Patch)})
}
func (p *PendingAction) UnmarshalJSON(data []byte) error {
	type plain PendingAction
	var value struct {
		plain
		Patch expensewire.Patch `json:"patch"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*p = PendingAction(value.plain)
	p.Patch = value.Patch.Domain()
	return nil
}
