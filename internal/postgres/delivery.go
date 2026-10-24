package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/LionJr/expenses/internal/botstate"
)

// DeliveryPending includes future retries: webhook acknowledgements must wait
// until the reply is sent or delivery has reached a terminal state.
func (s *Store) DeliveryPending(ctx context.Context, id int64) (bool, error) {
	var pending bool
	err := s.db.GetContext(ctx, &pending, `SELECT EXISTS (SELECT 1 FROM telegram_updates WHERE update_id = $1 AND state = 'done')`, id)
	return pending, err
}

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
// Only permanent errors make the delivery terminal. Transient replies survive
// any number of attempts and are retried on later outbox passes.
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
	              state = CASE WHEN $3
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

// RetryFailedDelivery recovers a saved ordinary reply abandoned by the old
// attempt limit. Never replay a mutation, a revoked user's reply, a permanent
// rejection or a choice prompt whose session may already have been cleared.
func (s *Store) RetryFailedDelivery(ctx context.Context, id int64) (bool, error) {
	var user int64
	err := s.db.GetContext(ctx, &user, `SELECT user_id FROM telegram_updates WHERE update_id=$1`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !s.allowed[user] {
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, `UPDATE telegram_updates
        SET state='done', delivery_attempts=0, retry_at=$3, last_error_code=NULL
        WHERE update_id=$1 AND user_id=$2 AND state='failed'
            AND NOT is_choice_prompt AND reply<>''
            AND (last_error_code IN (0,408,429) OR last_error_code BETWEEN 500 AND 599)`, id, user, time.Now().UTC())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}
