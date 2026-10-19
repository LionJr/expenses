package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPermanentDataErrorsDoNotMaskUncertainOutcomes(t *testing.T) {
	for _, code := range []string{"22021", "22P05", "23514", "23503"} {
		if !errors.Is(dataError(&pgconn.PgError{Code: code}), expense.ErrInvalid) {
			t.Fatal(code)
		}
	}
	for _, err := range []error{context.DeadlineExceeded, errors.New("commit response lost"), &pgconn.PgError{Code: "40001"}, &pgconn.PgError{Code: "08006"}} {
		if dataError(err) != err {
			t.Fatalf("uncertain error changed: %v", err)
		}
	}
}

func TestJSONTextValidationDistinguishesEscapedLiteral(t *testing.T) {
	for _, raw := range []string{`{"text":"bad\u0000text"}`, `{"bad\u0000key":1}`, `{"items":["bad\u0000text"]}`} {
		if !errors.Is(validateJSONText([]byte(raw)), expense.ErrInvalid) {
			t.Fatal("accepted " + raw)
		}
	}
	if err := validateJSONText([]byte(`{"text":"literal \\u0000"}`)); err != nil {
		t.Fatal(err)
	}
}
