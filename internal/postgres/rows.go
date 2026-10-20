package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

// Storage tags stay here; application models do not depend on SQL columns.
type expenseRow struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	Amount    int64     `db:"amount"`
	Category  string    `db:"category"`
	SpentAt   string    `db:"spent_at"`
	Comment   string    `db:"comment"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
	Version   int64     `db:"version"`
}

func getExpense(ctx context.Context, q queryer, query string, args ...any) (expense.Expense, error) {
	var row expenseRow
	err := q.GetContext(ctx, &row, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		err = expense.ErrNotFound
	}
	return expense.Expense{
		ID:        row.ID,
		UserID:    row.UserID,
		Amount:    row.Amount,
		Category:  row.Category,
		SpentAt:   row.SpentAt,
		Comment:   row.Comment,
		CreatedAt: row.CreatedAt.UTC(),
		UpdatedAt: row.UpdatedAt.UTC(),
		Version:   row.Version,
	}, dataError(err)
}

// The LEFT JOIN emits one row with NULL expense fields even for an empty page.
// Totals remain available when no expenses match or the offset is past the end.
type expensePageRow struct {
	Total       int64          `db:"total"`
	AmountTotal string         `db:"total_amount"`
	ID          sql.NullInt64  `db:"id"`
	UserID      sql.NullInt64  `db:"user_id"`
	Amount      sql.NullInt64  `db:"amount"`
	Version     sql.NullInt64  `db:"version"`
	Category    sql.NullString `db:"category"`
	SpentAt     sql.NullString `db:"spent_at"`
	Comment     sql.NullString `db:"comment"`
	CreatedAt   sql.NullTime   `db:"created_at"`
	UpdatedAt   sql.NullTime   `db:"updated_at"`
}

func (r expensePageRow) totalAmount() (int64, error) {
	amount, err := strconv.ParseInt(r.AmountTotal, 10, 64)
	if err != nil {
		return amount, expense.Invalid("сумма отчёта превышает допустимый диапазон")
	}
	return amount, nil
}

func (r expensePageRow) expense() expense.Expense {
	return expense.Expense{
		ID:        r.ID.Int64,
		UserID:    r.UserID.Int64,
		Amount:    r.Amount.Int64,
		Category:  r.Category.String,
		SpentAt:   r.SpentAt.String,
		Comment:   r.Comment.String,
		CreatedAt: r.CreatedAt.Time.UTC(),
		UpdatedAt: r.UpdatedAt.Time.UTC(),
		Version:   r.Version.Int64,
	}
}

type userRow struct {
	ID       int64  `db:"id"`
	Name     string `db:"name"`
	Username string `db:"username"`
}

type deliveryRow struct {
	UpdateID     int64  `db:"update_id"`
	UserID       int64  `db:"user_id"`
	Reply        string `db:"reply"`
	Attempts     int    `db:"delivery_attempts"`
	ChoicePrompt bool   `db:"is_choice_prompt"`
}

func (r deliveryRow) delivery() botstate.Delivery { return botstate.Delivery(r) }

type updateRow struct {
	State string `db:"state"`
	Reply string `db:"reply"`
}

type deliveryStateRow struct {
	UserID       int64  `db:"user_id"`
	ChoicePrompt bool   `db:"is_choice_prompt"`
	State        string `db:"state"`
}
