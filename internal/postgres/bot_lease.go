package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/LionJr/expenses/internal/botstate"
)

// WithBotLease serializes the whole webhook/outbox pass across deployments.
// A request must have a finite deadline; the TTL is its remaining budget plus
// a safety margin, so a crashed process cannot retain the worker forever.
// All work must honor ctx; existing DB/LLM/Telegram calls use that context.
func (s *Store) WithBotLease(ctx context.Context, work func(context.Context) error) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return fmt.Errorf("Telegram worker requires a request deadline")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	owner := hex.EncodeToString(bytes[:])
	var claimed string
	err := s.db.QueryRowContext(ctx, `INSERT INTO telegram_worker_lease(singleton,owner,expires_at)
        VALUES(true,$1,clock_timestamp()+$2::bigint * interval '1 millisecond')
        ON CONFLICT(singleton) DO UPDATE SET owner=EXCLUDED.owner,expires_at=EXCLUDED.expires_at
        WHERE telegram_worker_lease.expires_at<=clock_timestamp()
        RETURNING owner`, owner, (remaining + 30*time.Second).Milliseconds()).Scan(&claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return botstate.ErrWorkerBusy
	}
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		// Fencing the release prevents an expired owner's cleanup from deleting
		// a successor's claim. Failed cleanup is recovered by lease expiry.
		_, _ = s.db.ExecContext(cleanup, `DELETE FROM telegram_worker_lease WHERE singleton AND owner=$1`, owner)
	}()
	return work(ctx)
}
