package agent

import (
	"encoding/json"
	"testing"

	"github.com/LionJr/expenses/internal/botstate"
)

func TestStoredSessionJSONRemainsCompatible(t *testing.T) {
	// Existing sessions use strings for identifiers, money and version fields.
	// Keep this literal independent of the current DTO encoder.
	const stored = `{"history":[{"role":"user","content":"измени кофе на 450"}],"candidates":[{"id":"9007199254740993","user_id":"42","amount":"35000","version":"7","category":"еда","spent_at":"2026-10-17","comment":"кофе","created_at":"2026-10-17T10:00:00Z","updated_at":"2026-10-17T10:00:00Z"}],"candidate_total":2,"pending":{"intent_validated":true,"kind":"update_expense","patch":{"expected_version":"7","amount":"45000","category":null,"spent_at":null,"comment":""},"expected_amount":"35000"},"choice":{"update_id":"81","message_id":"91","state":"await_selection"}}`
	var s Session
	if err := json.Unmarshal([]byte(stored), &s); err != nil {
		t.Fatal(err)
	}
	assert := func(s Session) {
		t.Helper()
		if len(s.Candidates) != 1 || s.Candidates[0].ID != 9007199254740993 || s.Candidates[0].Amount != 35000 || s.Candidates[0].Version != 7 {
			t.Fatalf("candidate changed: %+v", s.Candidates)
		}
		if s.Pending == nil || s.Pending.Kind != "update_expense" || !s.Pending.IntentValidated || s.Pending.Patch.Amount == nil || *s.Pending.Patch.Amount != 45000 || s.Pending.Patch.ExpectedVersion == nil || *s.Pending.Patch.ExpectedVersion != 7 || s.Pending.ExpectedAmount == nil || *s.Pending.ExpectedAmount != 35000 || s.Pending.Patch.Comment == nil || *s.Pending.Patch.Comment != "" {
			t.Fatalf("pending action changed: %+v", s.Pending)
		}
		if s.Choice == nil || s.Choice.UpdateID != 81 || s.Choice.MessageID != 91 || s.Choice.State != botstate.AwaitingSelection {
			t.Fatalf("choice changed: %+v", s.Choice)
		}
	}
	assert(s)
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Candidates []map[string]any `json:"candidates"`
		Pending    struct {
			Patch map[string]any `json:"patch"`
		} `json:"pending"`
	}
	if err = json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Candidates[0]["id"] != "9007199254740993" || wire.Candidates[0]["amount"] != "35000" || wire.Pending.Patch["amount"] != "45000" || wire.Pending.Patch["expected_version"] != "7" {
		t.Fatalf("wire format changed: %s", data)
	}
	if _, ok := wire.Pending.Patch["expected_amount"]; ok {
		t.Fatal("internal precondition leaked into patch JSON")
	}
	var restored Session
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	assert(restored)
}
