package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/LionJr/expenses/internal/expensesvc"
	"github.com/LionJr/expenses/internal/postgres"
	"github.com/LionJr/expenses/migrations"
)

func currencySchema(t *testing.T, parent *postgres.Store, dsn, name string) string {
	t.Helper()
	ctx := t.Context()
	if _, err := postgres.DBForTest(parent).ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA %q`, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := postgres.DBForTest(parent).ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA %q CASCADE`, name)); err != nil {
			t.Error(err)
		}
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", name)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func checkCurrencyBinding(t *testing.T, parent *postgres.Store, dsn string) {
	for _, code := range []expense.Currency{expense.TMT, expense.RUB, expense.USD, expense.EUR, expense.AZN} {
		t.Run(code.Code(), func(t *testing.T) {
			ctx := t.Context()
			isolated := currencySchema(t, parent, dsn, "currency_"+strings.ToLower(code.Code()))
			store, err := postgres.Open(ctx, isolated, []int64{101}, code)
			if err != nil {
				t.Fatal(err)
			}
			other := expense.USD
			if code == other {
				other = expense.TMT
			}
			// Pinning applies even before the first write, so two deployments
			// cannot run with differing interpretations of one database.
			if changed, err := postgres.Open(ctx, isolated, []int64{101}, other); !errors.Is(err, postgres.ErrCurrencyMismatch) {
				if changed != nil {
					changed.Close()
				}
				store.Close()
				t.Fatalf("mismatched startup accepted: %v", err)
			}
			value, err := expensesvc.New(store).Create(ctx, expense.Input{UserID: 101, Amount: 1234, Category: "еда", SpentAt: "2026-10-24", Comment: "currency test"})
			store.Close()
			if err != nil || value.Amount != 1234 {
				t.Fatal("amount was converted", err)
			}
			reopened, err := postgres.Open(ctx, isolated, []int64{101}, code)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			page, err := expensesvc.New(reopened).List(ctx, expense.Filter{UserID: 101})
			if err != nil || page.TotalAmount != 1234 || page.Total != 1 {
				t.Fatal("restart lost or converted amounts", err)
			}
		})
	}
	t.Run("concurrent first deployments agree on one currency", func(t *testing.T) {
		isolated := currencySchema(t, parent, dsn, "currency_race")
		results := make(chan error, 2)
		for _, code := range []expense.Currency{expense.TMT, expense.USD} {
			go func() {
				store, err := postgres.Open(t.Context(), isolated, []int64{101}, code)
				if store != nil {
					store.Close()
				}
				results <- err
			}()
		}
		successes, conflicts := 0, 0
		for range 2 {
			err := <-results
			switch {
			case err == nil:
				successes++
			case errors.Is(err, postgres.ErrCurrencyMismatch):
				conflicts++
			default:
				t.Fatal(err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatalf("successful=%d conflicts=%d", successes, conflicts)
		}
	})
	t.Run("deleted legacy expenses retain RUB interpretation", func(t *testing.T) {
		ctx := t.Context()
		isolated := currencySchema(t, parent, dsn, "currency_legacy_journal")
		legacy, err := sql.Open("pgx", isolated)
		if err != nil {
			t.Fatal(err)
		}
		defer legacy.Close()
		initial, err := migrations.Files.ReadFile("001_init.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = legacy.ExecContext(ctx, string(initial)); err != nil {
			t.Fatal(err)
		}
		if _, err = legacy.ExecContext(ctx, `CREATE TABLE schema_migrations(version TEXT PRIMARY KEY);
            INSERT INTO schema_migrations VALUES('001_init.sql');
            INSERT INTO users(id) VALUES(101);
            INSERT INTO expense_operations(operation_key,user_id,result) VALUES('old',101,'{"operation":"delete_expense","deleted_id":"1"}')`); err != nil {
			t.Fatal(err)
		}
		if changed, err := postgres.Open(ctx, isolated, []int64{101}, expense.TMT); !errors.Is(err, postgres.ErrCurrencyMismatch) {
			if changed != nil {
				changed.Close()
			}
			t.Fatalf("legacy journal interpreted as TMT: %v", err)
		}
		store, err := postgres.Open(ctx, isolated, []int64{101}, expense.RUB)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		result, err := store.OperationResult(ctx, "old", 101)
		if err != nil || !strings.Contains(string(result), "delete_expense") {
			t.Fatal("legacy receipt lost", err)
		}
	})
}
