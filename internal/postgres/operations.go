package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensewire"
)

// Execute atomically journals the result alongside the change. Replays never apply it twice.
func (s *Store) Execute(ctx context.Context, key string, owner int64, prepare func() (expense.Command, error)) (json.RawMessage, error) {
	if owner <= 0 || !s.Allowed[owner] {
		return nil, expense.ErrNotFound
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", key); err != nil {
		return nil, err
	}
	var saved []byte
	err = tx.QueryRowContext(ctx, "SELECT result FROM expense_operations WHERE operation_key=$1 AND user_id=$2", key, owner).Scan(&saved)
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
	var result any
	switch command.Operation {
	case expense.Create:
		result, err = create(ctx, tx, command.Input)
	case expense.Update:
		result, err = update(ctx, tx, owner, command.ID, command.Patch)
	case expense.Delete:
		err = remove(ctx, tx, owner, command.ID)
		result = map[string]any{"deleted_id": strconv.FormatInt(command.ID, 10)}
	default:
		return nil, expense.Invalid("неизвестная операция")
	}
	if err != nil {
		return nil, err
	}
	saved, err = json.Marshal(operationPayload(result))
	if err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(saved, &envelope); err != nil {
		return nil, err
	}
	envelope["operation"], _ = json.Marshal(command.Operation)
	saved, err = json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO expense_operations(operation_key,user_id,result) VALUES($1,$2,$3::jsonb)", key, owner, string(saved)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return saved, nil
}

// OperationResult is checked before re-running state-dependent agent logic.
func (s *Store) OperationResult(ctx context.Context, key string, owner int64) (json.RawMessage, error) {
	if owner <= 0 || !s.Allowed[owner] {
		return nil, expense.ErrNotFound
	}
	var result []byte
	err := s.DB.QueryRowContext(ctx, "SELECT result FROM expense_operations WHERE operation_key=$1 AND user_id=$2", key, owner).Scan(&result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return result, err
}

func operationPayload(result any) any {
	if e, ok := result.(expense.Expense); ok {
		return expensewire.FromExpense(e)
	}
	return result
}
