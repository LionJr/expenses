package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

func (s *Store) StartUpdate(ctx context.Context, id, user int64) (string, string, error) {
	insertTelegramUpdatesQuery := `INSERT INTO telegram_updates (update_id, user_id, retry_at, created_at)
	                               VALUES ($1, $2, $3, $3)
	                               ON CONFLICT DO NOTHING`
	_, err := s.db.ExecContext(ctx, insertTelegramUpdatesQuery, id, user, time.Now().UTC())
	if err != nil {
		return "", "", err
	}
	var row updateRow
	selectTelegramUpdatesQuery := `SELECT state, reply
	                               FROM telegram_updates
	                               WHERE update_id = $1 AND user_id = $2`
	err = s.db.GetContext(ctx, &row, selectTelegramUpdatesQuery, id, user)
	return row.State, row.Reply, err
}

func (s *Store) FinishUpdate(ctx context.Context, id int64, reply string, session json.RawMessage, user int64) error {
	if err := expense.Text(reply); err != nil {
		return err
	}
	if err := validateJSONText(session); err != nil {
		return err
	}
	var metadata struct {
		Choice *botstate.Choice `json:"choice"`
	}
	if err := json.Unmarshal(session, &metadata); err != nil {
		return err
	}
	var choiceID int64
	if metadata.Choice != nil {
		choiceID = metadata.Choice.UpdateID
	}

	isPrompt := metadata.Choice != nil && choiceID == id && metadata.Choice.State == botstate.AwaitingDelivery

	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	updateTelegramUpdatesQuery := `UPDATE telegram_updates
	                               SET state = 'done', reply = $2, is_choice_prompt = $3
	                               WHERE update_id = $1`
	if _, err = tx.ExecContext(ctx, updateTelegramUpdatesQuery, id, reply, isPrompt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, upsertSession, user, string(session), time.Now().UTC().Add(30*time.Minute)); err != nil {
		return err
	}

	// Cancel only obsolete choice prompts. Receipts and ordinary replies remain
	// deliverable, while a waiting prompt survives non-mutating delivery warnings.
	supersedePromptsQuery := `UPDATE telegram_updates
	                          SET state = 'superseded'
	                          WHERE user_id = $1 AND state = 'done'
	                              AND is_choice_prompt AND update_id <> $2`
	if _, err = tx.ExecContext(ctx, supersedePromptsQuery, user, choiceID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkSent(ctx context.Context, id int64) error { return s.markSent(ctx, id, 0) }

func (s *Store) MarkChoiceSent(ctx context.Context, id, messageID int64) error {
	return s.markSent(ctx, id, messageID)
}

func (s *Store) ChoicePromptCurrent(ctx context.Context, id, user int64) (bool, error) {
	var current bool
	query := `SELECT EXISTS (
	              SELECT 1
	              FROM bot_sessions
	              WHERE user_id = $1 AND expires_at > $4
	                  AND data->'choice'->>'update_id' = $2
	                  AND data->'choice'->>'state' = $3
	          )`
	err := s.db.GetContext(ctx, &current, query, user, strconv.FormatInt(id, 10), string(botstate.AwaitingDelivery), time.Now().UTC())
	return current, err
}

func (s *Store) SupersedeDelivery(ctx context.Context, id int64) error {
	query := `UPDATE telegram_updates
	          SET state = 'superseded'
	          WHERE update_id = $1 AND state = 'done'`
	_, err := s.db.ExecContext(ctx, query, id)
	return err
}

func (s *Store) markSent(ctx context.Context, id, messageID int64) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var row deliveryStateRow
	updateTelegramUpdatesQuery := `UPDATE telegram_updates
	                               SET state = 'sent'
	                               WHERE update_id = $1 AND state = 'done'
	                               RETURNING user_id, is_choice_prompt`
	err = tx.GetContext(ctx, &row, updateTelegramUpdatesQuery, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.ChoicePrompt {
		if messageID <= 0 {
			return expense.Invalid("нет ID отправленного списка")
		}
		updateBotSessionsQuery := `UPDATE bot_sessions
		                           SET data = jsonb_set(
		                               jsonb_set(data, '{choice,state}', to_jsonb($3::text), false),
		                               '{choice,message_id}', to_jsonb($5::text), true
		                           )
		                           WHERE user_id = $1 AND expires_at > $6
		                               AND data->'choice'->>'update_id' = $2
		                               AND data->'choice'->>'state' = $4`
		_, err = tx.ExecContext(ctx, updateBotSessionsQuery, row.UserID, strconv.FormatInt(id, 10), string(botstate.AwaitingSelection), string(botstate.AwaitingDelivery), strconv.FormatInt(messageID, 10), time.Now().UTC())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
