package postgres

import (
	"encoding/json"
	"errors"

	"github.com/LionJr/expenses/internal/expense"
	"github.com/jackc/pgx/v5/pgconn"
)

// Only definite statement rejections are input errors. Connection errors and
// uncertain commit outcomes retain their original type and remain recoverable.
func dataError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "22021", "22P05", "22003", "22007", "22008", "23514", "23502", "23503":
			return expense.Invalid("данные не соответствуют допустимому формату")
		}
	}
	return err
}

func validateJSONText(data json.RawMessage) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return expense.Invalid("некорректный контекст")
	}
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case string:
			return expense.Text(x)
		case []any:
			for _, child := range x {
				if err := walk(child); err != nil {
					return err
				}
			}
		case map[string]any:
			for key, child := range x {
				if err := expense.Text(key); err != nil {
					return err
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}
