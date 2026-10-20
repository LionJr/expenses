package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensewire"
)

// Execute atomically journals the result alongside the change. Replays never apply it twice.
func (s *Store) Execute(ctx context.Context, key string, owner int64, prepare func() (expense.Command, error)) (json.RawMessage, error) {
	if owner <= 0 || !s.allowed[owner] {
		return nil, expense.ErrNotFound
	}

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	selectLockQuery := `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	if _, err = tx.ExecContext(ctx, selectLockQuery, key); err != nil {
		return nil, err
	}

	var saved []byte
	selectExpenseOperationsQuery := `SELECT result
	                                 FROM expense_operations
	                                 WHERE operation_key = $1 AND user_id = $2`
	err = tx.GetContext(ctx, &saved, selectExpenseOperationsQuery, key, owner)
	if err == nil {
		return saved, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	command, err := prepare()
	if err != nil {
		return nil, err
	}
	// Bind writes to the authenticated owner, independently of command data.
	command.Input.UserID = owner
	result, err := executeCommand(ctx, tx, owner, command)
	if err != nil {
		return nil, err
	}

	saved, err = json.Marshal(result)
	if err != nil {
		return nil, err
	}

	insertExpenseOperationsQuery := `INSERT INTO expense_operations (operation_key, user_id, result, created_at)
	                                 VALUES ($1, $2, $3::jsonb, $4)`
	if _, err = tx.ExecContext(ctx, insertExpenseOperationsQuery, key, owner, string(saved), time.Now().UTC()); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// OperationResult is checked before re-running state-dependent agent logic.
func (s *Store) OperationResult(ctx context.Context, key string, owner int64) (json.RawMessage, error) {
	if owner <= 0 || !s.allowed[owner] {
		return nil, expense.ErrNotFound
	}

	var result []byte
	query := `SELECT result
	          FROM expense_operations
	          WHERE operation_key = $1 AND user_id = $2`
	err := s.db.GetContext(ctx, &result, query, key, owner)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return result, err
}

// Receipts retain the existing flat JSON format for persisted operation results.
type expenseReceipt struct {
	Operation expense.Operation `json:"operation"`
	expensewire.Expense
}

type deleteReceipt struct {
	Operation expense.Operation `json:"operation"`
	DeletedID int64             `json:"deleted_id,string"`
}

func executeCommand(ctx context.Context, q queryer, owner int64, command expense.Command) (any, error) {
	var result expense.Expense
	var err error
	switch command.Operation {
	case expense.Create:
		result, err = create(ctx, q, command.Input)
	case expense.Update:
		result, err = update(ctx, q, owner, command.ID, command.Patch)
	case expense.Delete:
		if err = remove(ctx, q, owner, command.ID); err != nil {
			return nil, err
		}
		return deleteReceipt{Operation: expense.Delete, DeletedID: command.ID}, nil
	default:
		return nil, expense.Invalid("неизвестная операция")
	}
	if err != nil {
		return nil, err
	}
	return expenseReceipt{Operation: command.Operation, Expense: expensewire.FromExpense(result)}, nil
}
