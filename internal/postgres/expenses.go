package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/LionJr/expenses/internal/expense"
)

func (s *Store) List(ctx context.Context, f expense.Filter) (expense.Page, error) {
	return list(ctx, s.db, f)
}
func list(ctx context.Context, q queryer, f expense.Filter) (expense.Page, error) {
	var p expense.Page
	query := expenseListQuery(f.Latest)
	var rows []expensePageRow
	err := q.SelectContext(ctx, &rows, query, f.UserID, f.Category, f.From, f.To, f.Search, f.Limit, f.Offset, f.ID)
	if err != nil {
		return p, err
	}
	for _, row := range rows {
		p.Total = row.Total
		p.TotalAmount, err = row.totalAmount()
		if err != nil {
			return p, err
		}
		if row.ID.Valid {
			p.Items = append(p.Items, row.expense())
		}
	}
	return p, nil
}

func (s *Store) Create(ctx context.Context, i expense.Input) (expense.Expense, error) {
	if !s.allowed[i.UserID] {
		return expense.Expense{}, expense.Invalid("участник не разрешён")
	}
	var result expense.Expense
	err := inTransaction(ctx, s.db, func(q queryer) error {
		var err error
		result, err = create(ctx, q, i)
		return err
	})
	return result, err
}
func create(ctx context.Context, q queryer, i expense.Input) (expense.Expense, error) {
	insertExpensesQuery := `INSERT INTO expenses (user_id, amount, category, spent_at, comment, created_at, updated_at)
	                        VALUES ($1, $2, $3, $4::date, $5, $6, $6)
	                        RETURNING id, user_id, amount, category, to_char(spent_at, 'YYYY-MM-DD') AS spent_at,
	                                  comment, created_at, updated_at, version`
	return getExpense(ctx, q, insertExpensesQuery, i.UserID, i.Amount, i.Category, i.SpentAt, i.Comment, time.Now().UTC())
}

func (s *Store) Update(ctx context.Context, owner, id int64, p expense.Patch) (expense.Expense, error) {
	var result expense.Expense
	err := inTransaction(ctx, s.db, func(q queryer) error {
		var err error
		result, err = update(ctx, q, owner, id, p)
		return err
	})
	return result, err
}
func update(ctx context.Context, q queryer, owner, id int64, p expense.Patch) (expense.Expense, error) {
	query := `UPDATE expenses
	          SET amount = COALESCE($3, amount),
	              category = COALESCE($4, category),
	              spent_at = COALESCE($5::date, spent_at),
	              comment = COALESCE($6, comment),
	              updated_at = $9,
	              version = version + 1
	          WHERE id = $1
	              AND ($2::bigint = 0 OR user_id = $2)
	              AND ($7::bigint IS NULL OR amount = $7)
	              AND ($8::bigint IS NULL OR version = $8)
	          RETURNING id, user_id, amount, category, to_char(spent_at, 'YYYY-MM-DD') AS spent_at,
	                    comment, created_at, updated_at, version`
	e, err := getExpense(ctx, q, query, id, owner, p.Amount, p.Category, p.SpentAt, p.Comment, p.ExpectedAmount, p.ExpectedVersion, time.Now().UTC())
	if errors.Is(err, expense.ErrNotFound) && p.ExpectedVersion != nil {
		var exists bool
		query = `SELECT EXISTS (
		             SELECT 1
		             FROM expenses
		             WHERE id = $1 AND ($2::bigint = 0 OR user_id = $2)
		         )`
		if lookupErr := q.GetContext(ctx, &exists, query, id, owner); lookupErr != nil {
			return e, lookupErr
		}
		if exists {
			return e, expense.ErrConflict
		}
	}
	return e, err
}

func (s *Store) Delete(ctx context.Context, owner, id int64) error {
	return inTransaction(ctx, s.db, func(q queryer) error { return remove(ctx, q, owner, id) })
}
func remove(ctx context.Context, q queryer, owner, id int64) error {
	query := `DELETE FROM expenses
	          WHERE id = $1 AND ($2::bigint = 0 OR user_id = $2)`
	r, err := q.ExecContext(ctx, query, id, owner)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return expense.ErrNotFound
	}
	return err
}
