package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/LionJr/expenses/internal/panelauth"
)

func (s *Store) MutatePanelAccount(ctx context.Context, id int64, create bool, fn func(*panelauth.Account) error) error {
	if !s.allowed[id] {
		return panelauth.ErrNotFound
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if create {
		if _, err = tx.ExecContext(ctx, `INSERT INTO panel_accounts(user_id,data) VALUES ($1,'{}') ON CONFLICT DO NOTHING`, id); err != nil {
			return err
		}
	}
	var data []byte
	if err = tx.GetContext(ctx, &data, `SELECT data FROM panel_accounts WHERE user_id=$1 FOR UPDATE`, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return panelauth.ErrNotFound
		}
		return err
	}
	var account panelauth.Account
	if err = json.Unmarshal(data, &account); err != nil {
		return err
	}
	if err = fn(&account); err != nil {
		return err
	}
	data, err = json.Marshal(account)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE panel_accounts SET data=$2 WHERE user_id=$1`, id, string(data)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreatePanelSession(ctx context.Context, session panelauth.Session) error {
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Expired sessions are removed on login, without background queries.
	var version string
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT data->>'version',(data->>'active')::boolean FROM panel_accounts WHERE user_id=$1 FOR UPDATE`, session.UserID).Scan(&version, &active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return panelauth.ErrCredentials
		}
		return err
	}
	if !active || version != session.Version {
		return panelauth.ErrCredentials
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM panel_sessions WHERE expires_at <= $1`, time.Now().UTC()); err != nil {
		return err
	}
	// Bound retained sessions even if a family member repeatedly signs in.
	if _, err = tx.ExecContext(ctx, `DELETE FROM panel_sessions WHERE token_hash IN (
        SELECT token_hash FROM panel_sessions WHERE user_id=$1 ORDER BY expires_at DESC OFFSET 9
    )`, session.UserID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO panel_sessions(token_hash,user_id,version,expires_at) VALUES($1,$2,$3,$4)`, session.Hash, session.UserID, session.Version, session.Expires); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FindPanelSession(ctx context.Context, hash string, now time.Time) (panelauth.Session, error) {
	var session panelauth.Session
	err := s.db.QueryRowContext(ctx, `SELECT s.user_id,s.version,s.expires_at
        FROM panel_sessions s JOIN panel_accounts a ON a.user_id=s.user_id
        WHERE s.token_hash=$1 AND s.expires_at>$2 AND a.data->>'active'='true'
            AND a.data->>'version'=s.version`, hash, now).Scan(&session.UserID, &session.Version, &session.Expires)
	if errors.Is(err, sql.ErrNoRows) {
		err = panelauth.ErrCredentials
	}
	session.Hash = hash
	return session, err
}

func (s *Store) DeletePanelSession(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM panel_sessions WHERE token_hash=$1`, hash)
	return err
}
