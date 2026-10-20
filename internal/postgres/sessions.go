package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

func (s *Store) LoadSession(ctx context.Context, id int64) (json.RawMessage, error) {
	var b []byte
	err := s.DB.QueryRowContext(ctx, "SELECT data FROM bot_sessions WHERE user_id=$1 AND expires_at>now()", id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return b, err
}
func (s *Store) SaveSession(ctx context.Context, id int64, b json.RawMessage) error {
	if err := validateJSONText(b); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, "INSERT INTO bot_sessions(user_id,data,expires_at) VALUES($1,$2::jsonb,now()+interval '30 minutes') ON CONFLICT(user_id) DO UPDATE SET data=EXCLUDED.data,expires_at=EXCLUDED.expires_at", id, string(b))
	return err
}
