package botstate

import "errors"

var ErrWorkerBusy = errors.New("Telegram worker is already processing an update")

// Delivery is a durable reply waiting for Telegram delivery. UserID is also the
// chat ID: the bot accepts messages only from matching private chats.
type Delivery struct {
	UpdateID     int64
	UserID       int64
	Reply        string
	Attempts     int
	ChoicePrompt bool
}
