package expense

// Operation identifies a durable expense operation. Values are kept stable for
// existing operation receipts; adapters translate their commands to this type.
type Operation string

const (
	Create Operation = "create_expense"
	Update Operation = "update_expense"
	Delete Operation = "delete_expense"
)

// Command is independent of Telegram updates and model tool arguments.
type Command struct {
	ID        int64
	Operation Operation
	Input     Input
	Patch     Patch
}

func (c Command) Validate() error {
	switch c.Operation {
	case Create:
		return c.Input.Validate()
	case Update:
		if c.ID <= 0 {
			return Invalid("неверный ID")
		}
		return c.Patch.Validate()
	case Delete:
		if c.ID <= 0 {
			return Invalid("неверный ID")
		}
		return nil
	default:
		return Invalid("неизвестная операция")
	}
}
