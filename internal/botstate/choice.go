package botstate

// Choice binds the ordered candidates to the update that sends their list.
// A stored list is not selectable until delivery is acknowledged atomically.
type Choice struct {
	UpdateID  int64       `json:"update_id,string"`
	MessageID int64       `json:"message_id,string,omitempty"`
	State     ChoiceState `json:"state"`
}
type ChoiceState string

const (
	AwaitingDelivery  ChoiceState = "await_delivery"
	AwaitingSelection ChoiceState = "await_selection"
)
