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
	rows, err := s.DB.QueryContext(ctx, "SELECT update_id,user_id,reply,delivery_attempts,is_choice_prompt FROM telegram_updates WHERE state='done' AND retry_at<=now() ORDER BY retry_at,update_id LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pending := []botstate.Delivery{}
	for rows.Next() {
		var d botstate.Delivery
		if err = rows.Scan(&d.UpdateID, &d.UserID, &d.Reply, &d.Attempts, &d.ChoicePrompt); err != nil {
			return nil, err
		}
		pending = append(pending, d)
	}
	return pending, rows.Err()
}

// Failed deliveries are terminal after three attempts or a permanent rejection.
// Transient failures remain in the outbox without holding the polling offset.
func (s *Store) FailDelivery(ctx context.Context, id int64, code int, permanent bool, retryAt time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var user int64
	var isPrompt bool
	var state string
	err = tx.QueryRowContext(ctx, `UPDATE telegram_updates
 SET delivery_attempts=delivery_attempts+1,last_error_code=$2,retry_at=$4,
 state=CASE WHEN $3 OR delivery_attempts+1>=3 THEN 'failed'::telegram_update_state ELSE 'done'::telegram_update_state END
 WHERE update_id=$1 AND state='done' RETURNING user_id,is_choice_prompt,state`, id, code, permanent, retryAt).Scan(&user, &isPrompt, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if isPrompt && state == "failed" {
		_, err = tx.ExecContext(ctx, `UPDATE bot_sessions SET data=data-'choice'-'candidates'-'pending'-'candidate_total'
   WHERE user_id=$1 AND data->'choice'->>'update_id'=$2 AND data->'choice'->>'state'=$3`, user, strconv.FormatInt(id, 10), string(botstate.AwaitingDelivery))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
