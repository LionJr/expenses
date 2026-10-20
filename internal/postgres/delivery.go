package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/LionJr/expenses/internal/botstate"
)

func (s *Store) PendingDeliveries(ctx context.Context, limit int) ([]botstate.Delivery, error) {
	query := `SELECT update_id, user_id, reply, delivery_attempts, is_choice_prompt
	          FROM telegram_updates
	          WHERE state = 'done' AND retry_at <= $2
	          ORDER BY retry_at, update_id
	          LIMIT $1`
	var rows []deliveryRow
	if err := s.db.SelectContext(ctx, &rows, query, limit, time.Now().UTC()); err != nil {
		return nil, err
	}
	pending := make([]botstate.Delivery, len(rows))
	for i, row := range rows {
		pending[i] = row.delivery()
	}
	return pending, nil
}

// FailDelivery records a failed delivery attempt and schedules a retry.
// Permanent errors or three failed attempts make the delivery terminal.
func (s *Store) FailDelivery(ctx context.Context, id int64, code int, permanent bool, retryAt time.Time) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var row deliveryStateRow
	query := `UPDATE telegram_updates
	          SET delivery_attempts = delivery_attempts + 1,
	              last_error_code = $2,
	              retry_at = $4,
	              state = CASE WHEN $3 OR delivery_attempts + 1 >= 3
	                  THEN 'failed'::telegram_update_state
	                  ELSE 'done'::telegram_update_state END
	          WHERE update_id = $1 AND state = 'done'
	          RETURNING user_id, is_choice_prompt, state`
	if err = tx.GetContext(ctx, &row, query, id, code, permanent, retryAt.UTC()); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}

	if row.ChoicePrompt && row.State == "failed" {
		query = `UPDATE bot_sessions
		         SET data = data - 'choice' - 'candidates' - 'pending' - 'candidate_total'
		         WHERE user_id = $1
		             AND data->'choice'->>'update_id' = $2
		             AND data->'choice'->>'state' = $3`
		if _, err = tx.ExecContext(ctx, query, row.UserID, strconv.FormatInt(id, 10), string(botstate.AwaitingDelivery)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
