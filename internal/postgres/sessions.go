package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const upsertSession = `INSERT INTO bot_sessions (user_id, data, expires_at)
                       VALUES ($1, $2::jsonb, $3)
                       ON CONFLICT (user_id) DO UPDATE
                       SET data = EXCLUDED.data, expires_at = EXCLUDED.expires_at`

func (s *Store) LoadSession(ctx context.Context, id int64) (json.RawMessage, error) {
	var data []byte
	selectBotSessionsQuery := `SELECT data
	                           FROM bot_sessions
	                           WHERE user_id = $1 AND expires_at > $2`
	err := s.db.GetContext(ctx, &data, selectBotSessionsQuery, id, time.Now().UTC())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return data, err
}

func (s *Store) SaveSession(ctx context.Context, id int64, b json.RawMessage) error {
	if err := validateJSONText(b); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, upsertSession, id, string(b), time.Now().UTC().Add(30*time.Minute))
	return err
}
