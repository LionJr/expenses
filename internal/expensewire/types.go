// Package expensewire owns the stable JSON representation used by HTTP,
// model results and durable bot state. Domain types have no transport tags.
package expensewire

import (
	"time"

	"github.com/LionJr/expenses/internal/expense"
)

type User struct {
	ID       int64  `json:"id,string"`
	Name     string `json:"name"`
	Username string `json:"username"`
}
type Expense struct {
	Version   int64     `json:"version,string"`
	ID        int64     `json:"id,string"`
	UserID    int64     `json:"user_id,string"`
	Amount    int64     `json:"amount,string"`
	Category  string    `json:"category"`
	SpentAt   string    `json:"spent_at"`
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Input struct {
	UserID   int64  `json:"user_id,string"`
	Amount   int64  `json:"amount,string"`
	Category string `json:"category"`
	SpentAt  string `json:"spent_at"`
	Comment  string `json:"comment"`
}
type Patch struct {
	ExpectedVersion *int64  `json:"expected_version,string,omitempty"`
	ExpectedAmount  *int64  `json:"-"`
	Amount          *int64  `json:"amount,string"`
	Category        *string `json:"category"`
	SpentAt         *string `json:"spent_at"`
	Comment         *string `json:"comment"`
}
type Page struct {
	Items       []Expense `json:"items"`
	Total       int64     `json:"total"`
	TotalAmount int64     `json:"total_amount,string"`
}

func FromExpense(v expense.Expense) Expense { return Expense(v) }
func (v Expense) Domain() expense.Expense   { return expense.Expense(v) }
func (v Input) Domain() expense.Input       { return expense.Input(v) }
func (v Patch) Domain() expense.Patch       { return expense.Patch(v) }
func FromPatch(v expense.Patch) Patch       { return Patch(v) }
func FromExpenses(items []expense.Expense) []Expense {
	if items == nil {
		return nil
	}
	result := make([]Expense, len(items))
	for i, v := range items {
		result[i] = FromExpense(v)
	}
	return result
}
func Expenses(items []Expense) []expense.Expense {
	if items == nil {
		return nil
	}
	result := make([]expense.Expense, len(items))
	for i, v := range items {
		result[i] = v.Domain()
	}
	return result
}
func FromPage(v expense.Page) Page {
	return Page{Items: FromExpenses(v.Items), Total: v.Total, TotalAmount: v.TotalAmount}
}
func (v Page) Domain() expense.Page {
	return expense.Page{Items: Expenses(v.Items), Total: v.Total, TotalAmount: v.TotalAmount}
}
func FromUsers(items []expense.User) []User {
	if items == nil {
		return nil
	}
	result := make([]User, len(items))
	for i, v := range items {
		result[i] = User(v)
	}
	return result
}
