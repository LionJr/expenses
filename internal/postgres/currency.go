package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/LionJr/expenses/internal/expense"
)

var ErrCurrencyMismatch = errors.New("database currency differs from APP_CURRENCY")

// The first configured process pins the database currency. A concurrent or
// later process with a different code cannot start and reinterpret its amounts.
func (s *Store) bindCurrency(ctx context.Context, currency expense.Currency) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_currency(singleton,code) VALUES(true,$1) ON CONFLICT DO NOTHING`, currency.Code()); err != nil {
		return err
	}
	var stored string
	if err = tx.GetContext(ctx, &stored, `SELECT code FROM application_currency WHERE singleton FOR UPDATE`); err != nil {
		return err
	}
	if stored != currency.Code() {
		return fmt.Errorf("%w: database=%s configured=%s; use the database currency or a separate database; no conversion was performed", ErrCurrencyMismatch, stored, currency.Code())
	}
	return tx.Commit()
}
