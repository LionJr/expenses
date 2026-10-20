package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/expense"
)

func (s *Store) StartUpdate(ctx context.Context, id, user int64) (string, string, error) {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO telegram_updates(update_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING", id, user)
	if err != nil {
		return "", "", err
	}
	var state, reply string
	err = s.DB.QueryRowContext(ctx, "SELECT state,reply FROM telegram_updates WHERE update_id=$1 AND user_id=$2", id, user).Scan(&state, &reply)
	return state, reply, err
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
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE telegram_updates SET state='done',reply=$2,is_choice_prompt=$3 WHERE update_id=$1", id, reply, isPrompt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO bot_sessions(user_id,data,expires_at) VALUES($1,$2::jsonb,now()+interval '30 minutes') ON CONFLICT(user_id) DO UPDATE SET data=EXCLUDED.data,expires_at=EXCLUDED.expires_at", user, string(session)); err != nil {
		return err
	}

	// Cancel only obsolete choice prompts. Receipts and ordinary replies remain
	// deliverable, while a waiting prompt survives non-mutating delivery warnings.
	if _, err = tx.ExecContext(ctx, "UPDATE telegram_updates SET state='superseded' WHERE user_id=$1 AND state='done' AND is_choice_prompt AND update_id<>$2", user, choiceID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) MarkSent(ctx context.Context, id int64) error { return s.markSent(ctx, id, 0) }
func (s *Store) MarkChoiceSent(ctx context.Context, id, messageID int64) error {
	return s.markSent(ctx, id, messageID)
}
func (s *Store) markSent(ctx context.Context, id, messageID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var user int64
	var isPrompt bool
	err = tx.QueryRowContext(ctx, "UPDATE telegram_updates SET state='sent' WHERE update_id=$1 AND state='done' RETURNING user_id,is_choice_prompt", id).Scan(&user, &isPrompt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if isPrompt {
		if messageID <= 0 {
			return expense.Invalid("нет ID отправленного списка")
		}
		_, err = tx.ExecContext(ctx, `UPDATE bot_sessions SET data=jsonb_set(jsonb_set(data,'{choice,state}',to_jsonb($3::text),false),'{choice,message_id}',to_jsonb($5::text),true)
   WHERE user_id=$1 AND expires_at>now() AND data->'choice'->>'update_id'=$2 AND data->'choice'->>'state'=$4`, user, strconv.FormatInt(id, 10), string(botstate.AwaitingSelection), string(botstate.AwaitingDelivery), strconv.FormatInt(messageID, 10))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) ChoicePromptCurrent(ctx context.Context, id, user int64) (bool, error) {
	var current bool
	err := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bot_sessions WHERE user_id=$1 AND expires_at>now() AND data->'choice'->>'update_id'=$2 AND data->'choice'->>'state'=$3)`, user, strconv.FormatInt(id, 10), string(botstate.AwaitingDelivery)).Scan(&current)
	return current, err
}
func (s *Store) SupersedeDelivery(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE telegram_updates SET state='superseded' WHERE update_id=$1 AND state='done'", id)
	return err
}
