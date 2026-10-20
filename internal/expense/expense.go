package expense

import "time"

type User struct {
	ID       int64
	Name     string
	Username string
}
type Expense struct {
	Version   int64
	ID        int64
	UserID    int64
	Amount    int64
	Category  string
	SpentAt   string
	Comment   string
	CreatedAt time.Time
	UpdatedAt time.Time
}
type Page struct {
	Items       []Expense
	Total       int64
	TotalAmount int64
}
