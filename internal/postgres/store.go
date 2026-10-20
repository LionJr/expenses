package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/LionJr/expenses/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Store struct {
	DB      *sql.DB
	Allowed map[int64]bool
}
type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func Open(ctx context.Context, dsn string, ids []int64) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	s := &Store{DB: db, Allowed: map[int64]bool{}}
	for _, id := range ids {
		s.Allowed[id] = true
	}
	for attempt := 0; attempt < 15; attempt++ {
		err = db.PingContext(ctx)
		if err == nil {
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
	return s, nil
}
func (s *Store) migrate(ctx context.Context, ids []int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(65102933)"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY)"); err != nil {
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
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", name.Name()).Scan(&exists); err != nil {
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
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", name.Name()); err != nil {
			return err
		}
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, "INSERT INTO users(id) VALUES($1) ON CONFLICT DO NOTHING", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Explicit transactions keep a canceled write from completing in autocommit
// after its caller has already received a deadline error. BeginTx also arranges
// rollback on context cancellation; Commit checks the transaction context.
func inTransaction(ctx context.Context, db *sql.DB, operation func(queryer) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = operation(tx); err != nil {
		return err
	}
	return tx.Commit()
}
