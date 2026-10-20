// Package expensesvc provides the expense use cases shared by the panel and bot.
package expensesvc

import (
	"context"
	"encoding/json"

	"github.com/LionJr/expenses/internal/expense"
)

type Repository interface {
	Users(context.Context) ([]expense.User, error)
	List(context.Context, expense.Filter) (expense.Page, error)
	Create(context.Context, expense.Input) (expense.Expense, error)
	Update(context.Context, int64, int64, expense.Patch) (expense.Expense, error)
	Delete(context.Context, int64, int64) error
	Execute(context.Context, string, int64, expense.Command) (json.RawMessage, error)
	OperationResult(context.Context, string, int64) (json.RawMessage, error)
}

type Service struct{ repository Repository }

func New(repository Repository) *Service { return &Service{repository: repository} }

func (s *Service) Users(ctx context.Context) ([]expense.User, error) { return s.repository.Users(ctx) }
func (s *Service) List(ctx context.Context, f expense.Filter) (expense.Page, error) {
	f = f.Normalize()
	if err := f.Validate(); err != nil {
		return expense.Page{}, err
	}
	return s.repository.List(ctx, f)
}
func (s *Service) Create(ctx context.Context, i expense.Input) (expense.Expense, error) {
	if err := (expense.Command{Operation: expense.Create, Input: i}).Validate(); err != nil {
		return expense.Expense{}, err
	}
	return s.repository.Create(ctx, i)
}
func (s *Service) Update(ctx context.Context, owner, id int64, p expense.Patch) (expense.Expense, error) {
	if err := (expense.Command{Operation: expense.Update, ID: id, Patch: p}).Validate(); err != nil {
		return expense.Expense{}, err
	}
	return s.repository.Update(ctx, owner, id, p)
}
func (s *Service) Delete(ctx context.Context, owner, id int64) error {
	if err := (expense.Command{Operation: expense.Delete, ID: id}).Validate(); err != nil {
		return err
	}
	return s.repository.Delete(ctx, owner, id)
}
func (s *Service) OperationResult(ctx context.Context, key string, owner int64) (json.RawMessage, error) {
	return s.repository.OperationResult(ctx, key, owner)
}
func (s *Service) Execute(ctx context.Context, key string, owner int64, c expense.Command) (json.RawMessage, error) {
	if owner <= 0 {
		return nil, expense.ErrNotFound
	}
	// The repository checks for a committed receipt before validating a new
	// command, within the same lock/transaction that protects the mutation.
	return s.repository.Execute(ctx, key, owner, c)
}
