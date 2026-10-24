package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
)

// "EXPM" in ASCII identifies the expenses migration lock.
const migrationsLockID int64 = 0x4558504D

// Store persists expense input normalized and validated by expensesvc.
// It owns transactions, owner checks and database constraints.
type Store struct {
	db      *sqlx.DB
	allowed map[int64]bool
}

// Close releases the database connection pool.
func (s *Store) Close() error {
	return s.db.Close()
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	GetContext(context.Context, any, string, ...any) error
	SelectContext(context.Context, any, string, ...any) error
}

func Open(ctx context.Context, dsn string, ids []int64, configured ...expense.Currency) (*Store, error) {
	currency := expense.TMT
	if len(configured) > 1 {
		return nil, fmt.Errorf("configure one application currency")
	}
	if len(configured) == 1 {
		var err error
		currency, err = expense.ParseCurrency(configured[0].Code())
		if err != nil {
			return nil, err
		}
	}
	db, err := sqlx.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	s := &Store{db: db, allowed: map[int64]bool{}}
	for _, id := range ids {
		s.allowed[id] = true
	}
	for attempt := 0; attempt < 15; attempt++ {
		if err = db.PingContext(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			db.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("database connection: %w", err)
	}
	if err = s.migrate(ctx, ids); err != nil {
		db.Close()
		return nil, err
	}
	if err = s.bindCurrency(ctx, currency); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context, ids []int64) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	selectLockQuery := `SELECT pg_advisory_xact_lock($1)`
	if _, err = tx.ExecContext(ctx, selectLockQuery, migrationsLockID); err != nil {
		return err
	}

	createSchemaMigrationsQuery := `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY)`
	if _, err = tx.ExecContext(ctx, createSchemaMigrationsQuery); err != nil {
		return err
	}

	names, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	for _, name := range names {
		if name.IsDir() {
			continue
		}
		var exists bool
		selectSchemaMigrationsQuery := `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`
		if err = tx.GetContext(ctx, &exists, selectSchemaMigrationsQuery, name.Name()); err != nil {
			return err
		}
		if exists {
			continue
		}
		data, e := migrations.Files.ReadFile(name.Name())
		if e != nil {
			return e
		}
		if _, err = tx.ExecContext(ctx, string(data)); err != nil {
			return fmt.Errorf("migration %s: %w", name.Name(), err)
		}
		insertSchemaMigrationsQuery := `INSERT INTO schema_migrations (version)
		                                VALUES ($1)`
		if _, err = tx.ExecContext(ctx, insertSchemaMigrationsQuery, name.Name()); err != nil {
			return err
		}
	}

	for _, id := range ids {
		insertUsersQuery := `INSERT INTO users (id)
		                     VALUES ($1)
		                     ON CONFLICT DO NOTHING`
		if _, err = tx.ExecContext(ctx, insertUsersQuery, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Explicit transactions keep a canceled write from completing in autocommit
// after its caller has already received a deadline error. BeginTxx wraps
// database/sql BeginTx, which arranges rollback on context cancellation;
// Commit checks the transaction context.
func inTransaction(ctx context.Context, db *sqlx.DB, operation func(queryer) error) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = operation(tx); err != nil {
		return err
	}
	return tx.Commit()
}
