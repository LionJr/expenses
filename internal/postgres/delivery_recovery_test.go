package postgres_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/LionJr/expenses/internal/postgres"
)

func checkFailedDeliveryRecovery(t *testing.T, s *postgres.Store) {
	ctx := context.Background()
	db := postgres.DBForTest(s)
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id) VALUES(303) ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	defer db.ExecContext(ctx, `DELETE FROM users WHERE id=303`)
	var before int
	if err := db.GetContext(ctx, &before, `SELECT count(*) FROM expense_operations`); err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		name, state, reply string
		user               int64
		code               int
		choice, want       bool
	}{
		{"network", "failed", "saved receipt", 101, 0, false, true},
		{"timeout", "failed", "saved receipt", 101, 408, false, true},
		{"throttled", "failed", "saved receipt", 101, 429, false, true},
		{"provider", "failed", "saved receipt", 101, 500, false, true},
		{"bad request", "failed", "saved receipt", 101, 400, false, false},
		{"blocked chat", "failed", "saved receipt", 101, 403, false, false},
		{"sent", "sent", "saved receipt", 101, 503, false, false},
		{"pending", "done", "saved receipt", 101, 503, false, false},
		{"expired choice context", "failed", "choose", 101, 503, true, false},
		{"revoked user", "failed", "private receipt", 303, 503, false, false},
		{"empty", "failed", "", 101, 503, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := int64(97000 + i)
			_, err := db.ExecContext(ctx, `INSERT INTO telegram_updates(update_id,user_id,state,reply,delivery_attempts,last_error_code,is_choice_prompt)
                VALUES($1,$2,$3,$4,3,$5,$6)`, id, tc.user, tc.state, tc.reply, tc.code, tc.choice)
			if err != nil {
				t.Fatal(err)
			}
			defer db.ExecContext(ctx, `DELETE FROM telegram_updates WHERE update_id=$1`, id)
			queued, err := s.RetryFailedDelivery(ctx, id)
			if err != nil || queued != tc.want {
				t.Fatalf("queued=%v err=%v", queued, err)
			}
			var state, reply string
			var attempts int
			var code sql.NullInt64
			if err := db.QueryRowContext(ctx, `SELECT state,reply,delivery_attempts,last_error_code FROM telegram_updates WHERE update_id=$1`, id).Scan(&state, &reply, &attempts, &code); err != nil {
				t.Fatal(err)
			}
			if reply != tc.reply {
				t.Fatal("saved reply changed")
			}
			if tc.want {
				if state != "done" || attempts != 0 || code.Valid {
					t.Fatalf("recovery metadata: %s %d %+v", state, attempts, code)
				}
			} else if state != tc.state || attempts != 3 || !code.Valid || code.Int64 != int64(tc.code) {
				t.Fatal("ineligible delivery changed")
			}
			if queued, err := s.RetryFailedDelivery(ctx, id); err != nil || queued {
				t.Fatalf("requeued active delivery: %v %v", queued, err)
			}
		})
	}
	if queued, err := s.RetryFailedDelivery(ctx, 97999); err != nil || queued {
		t.Fatalf("missing delivery: %v %v", queued, err)
	}
	var after int
	if err := db.GetContext(ctx, &after, `SELECT count(*) FROM expense_operations`); err != nil || before != after {
		t.Fatal("reply recovery changed expense operations")
	}
}
