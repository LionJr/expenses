package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/LionJr/expenses/internal/expense"
)

const columns = "id,user_id,amount,category,to_char(spent_at,'YYYY-MM-DD'),comment,created_at,updated_at,version"

func scan(row interface{ Scan(...any) error }) (expense.Expense, error) {
	var e expense.Expense
	err := row.Scan(&e.ID, &e.UserID, &e.Amount, &e.Category, &e.SpentAt, &e.Comment, &e.CreatedAt, &e.UpdatedAt, &e.Version)
	if errors.Is(err, sql.ErrNoRows) {
		err = expense.ErrNotFound
	}
	e.CreatedAt = e.CreatedAt.UTC()
	e.UpdatedAt = e.UpdatedAt.UTC()
	return e, dataError(err)
}
func (s *Store) List(ctx context.Context, f expense.Filter) (expense.Page, error) {
	return list(ctx, s.DB, f)
}
func list(ctx context.Context, q queryer, f expense.Filter) (expense.Page, error) {
	p := expense.Page{Items: []expense.Expense{}}
	if err := f.Validate(); err != nil {
		return p, err
	}
	// One statement gives the page and its aggregate the same MVCC snapshot.
	where := ` WHERE ($1::bigint=0 OR user_id=$1) AND ($2='' OR category=$2) AND ($3='' OR spent_at>=NULLIF($3,'')::date) AND ($4='' OR spent_at<=NULLIF($4,'')::date) AND ($5='' OR strpos(lower(comment || ' ' || category),lower($5))>0) AND ($8::bigint=0 OR id=$8)`
	order := "spent_at DESC,id DESC"
	if f.Latest {
		order = "created_at DESC,id DESC"
	}
	query := `WITH filtered AS (SELECT * FROM expenses` + where + `), page AS (SELECT * FROM filtered ORDER BY ` + order + ` LIMIT $6 OFFSET $7), totals AS (SELECT count(*) AS n,COALESCE(sum(amount),0)::text AS amount FROM filtered) SELECT totals.n,totals.amount, page.id,page.user_id,page.amount,page.category,to_char(page.spent_at,'YYYY-MM-DD'),page.comment,page.created_at,page.updated_at,page.version FROM totals LEFT JOIN page ON true ORDER BY `
	if f.Latest {
		query += "page.created_at DESC,page.id DESC"
	} else {
		query += "page.spent_at DESC,page.id DESC"
	}
	rows, err := q.QueryContext(ctx, query, f.UserID, f.Category, f.From, f.To, f.Search, f.Limit, f.Offset, f.ID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var sum string
		var id, uid, amount, version sql.NullInt64
		var cat, date, comment sql.NullString
		var created, updated sql.NullTime
		if err = rows.Scan(&p.Total, &sum, &id, &uid, &amount, &cat, &date, &comment, &created, &updated, &version); err != nil {
			return p, err
		}
		p.TotalAmount, err = strconv.ParseInt(sum, 10, 64)
		if err != nil {
			return p, expense.Invalid("сумма отчёта превышает допустимый диапазон")
		}
		if id.Valid {
			p.Items = append(p.Items, expense.Expense{ID: id.Int64, UserID: uid.Int64, Amount: amount.Int64, Category: cat.String, SpentAt: date.String, Comment: comment.String, CreatedAt: created.Time.UTC(), UpdatedAt: updated.Time.UTC(), Version: version.Int64})
		}
	}
	return p, rows.Err()
}
func (s *Store) Create(ctx context.Context, i expense.Input) (expense.Expense, error) {
	if !s.Allowed[i.UserID] {
		return expense.Expense{}, expense.Invalid("участник не разрешён")
	}
	var result expense.Expense
	err := inTransaction(ctx, s.DB, func(q queryer) error {
		var err error
		result, err = create(ctx, q, i)
		return err
	})
	return result, err
}
func create(ctx context.Context, q queryer, i expense.Input) (expense.Expense, error) {
	if err := i.Validate(); err != nil {
		return expense.Expense{}, err
	}
	return scan(q.QueryRowContext(ctx, "INSERT INTO expenses(user_id,amount,category,spent_at,comment) VALUES($1,$2,$3,$4::date,$5) RETURNING "+columns, i.UserID, i.Amount, i.Category, i.SpentAt, i.Comment))
}
func (s *Store) Update(ctx context.Context, owner, id int64, p expense.Patch) (expense.Expense, error) {
	var result expense.Expense
	err := inTransaction(ctx, s.DB, func(q queryer) error {
		var err error
		result, err = update(ctx, q, owner, id, p)
		return err
	})
	return result, err
}
func update(ctx context.Context, q queryer, owner, id int64, p expense.Patch) (expense.Expense, error) {
	if err := p.Validate(); err != nil {
		return expense.Expense{}, err
	}
	if id <= 0 {
		return expense.Expense{}, expense.Invalid("неверный ID")
	}
	e, err := scan(q.QueryRowContext(ctx, `UPDATE expenses SET amount=COALESCE($3,amount),category=COALESCE($4,category),spent_at=COALESCE($5::date,spent_at),comment=COALESCE($6,comment),updated_at=now(),version=version+1 WHERE id=$1 AND ($2::bigint=0 OR user_id=$2) AND ($7::bigint IS NULL OR amount=$7) AND ($8::bigint IS NULL OR version=$8) RETURNING `+columns, id, owner, p.Amount, p.Category, p.SpentAt, p.Comment, p.ExpectedAmount, p.ExpectedVersion))
	if errors.Is(err, expense.ErrNotFound) && p.ExpectedVersion != nil {
		var exists bool
		if lookupErr := q.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM expenses WHERE id=$1 AND ($2::bigint=0 OR user_id=$2))", id, owner).Scan(&exists); lookupErr != nil {
			return e, lookupErr
		}
		if exists {
			return e, expense.ErrConflict
		}
	}
	return e, err
}
func (s *Store) Delete(ctx context.Context, owner, id int64) error {
	return inTransaction(ctx, s.DB, func(q queryer) error { return remove(ctx, q, owner, id) })
}
func remove(ctx context.Context, q queryer, owner, id int64) error {
	r, err := q.ExecContext(ctx, "DELETE FROM expenses WHERE id=$1 AND ($2::bigint=0 OR user_id=$2)", id, owner)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return expense.ErrNotFound
	}
	return err
}
