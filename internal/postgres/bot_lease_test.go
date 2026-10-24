package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/agent"
	"github.com/LionJr/expenses/internal/botstate"
	"github.com/LionJr/expenses/internal/postgres"
	"github.com/LionJr/expenses/internal/telegram"
)

type leasedResponder struct {
	calls   atomic.Int32
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (a *leasedResponder) Reply(ctx context.Context, _, _ int64, _ string, _ agent.Session) (string, agent.Session, error) {
	a.calls.Add(1)
	a.once.Do(func() { close(a.entered) })
	select {
	case <-a.release:
		return "lease-only-response", agent.Session{}, nil
	case <-ctx.Done():
		return "", agent.Session{}, ctx.Err()
	}
}

func checkWebhookWorkerLease(t *testing.T, s *postgres.Store, dsn string) {
	ctx := context.Background()
	secondStore, err := postgres.Open(ctx, dsn, []int64{101, 202})
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	var replies atomic.Int32
	transport := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Text == "lease-only-response" {
			replies.Add(1)
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":10000}}`)
	}))
	defer transport.Close()
	a := &leasedResponder{entered: make(chan struct{}), release: make(chan struct{})}
	newHandler := func(store *postgres.Store) http.Handler {
		b := &telegram.Bot{BaseURL: transport.URL, HTTP: transport.Client(), Allowed: map[int64]bool{101: true, 202: true}, Store: store, Agent: a}
		return b.WebhookHandler("worker-test-secret", 5*time.Second)
	}
	first, second := newHandler(s), newHandler(secondStore)
	request := func(handler http.Handler) int {
		r := httptest.NewRequest("POST", telegram.WebhookPath, strings.NewReader(`{"update_id":300001,"message":{"from":{"id":101},"chat":{"id":101,"type":"private"},"text":"lease-check"}}`))
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "worker-test-secret")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	done := make(chan int, 1)
	go func() { done <- request(first) }()
	select {
	case <-a.entered:
	case <-time.After(3 * time.Second):
		close(a.release)
		t.Fatal("first worker did not begin")
	}
	if status := request(second); status != 503 || a.calls.Load() != 1 {
		close(a.release)
		t.Fatalf("other process entered active work: status=%d calls=%d", status, a.calls.Load())
	}
	close(a.release)
	if status := <-done; status != 200 {
		t.Fatalf("first process failed: %d", status)
	}
	if status := request(second); status != 200 {
		t.Fatalf("retry after release failed: %d", status)
	}
	if a.calls.Load() != 1 || replies.Load() != 1 {
		t.Fatalf("duplicate processing: agent=%d replies=%d", a.calls.Load(), replies.Load())
	}

	t.Run("crashed lease expires and stale release is fenced", func(t *testing.T) {
		if _, err := postgres.DBForTest(s).ExecContext(ctx, `INSERT INTO telegram_worker_lease(singleton,owner,expires_at) VALUES(true,'crashed',clock_timestamp()+interval '1 minute')`); err != nil {
			t.Fatal(err)
		}
		deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := secondStore.WithBotLease(deadline, func(context.Context) error { t.Fatal("live lease stolen"); return nil }); !errors.Is(err, botstate.ErrWorkerBusy) {
			t.Fatalf("expected busy: %v", err)
		}
		if _, err := postgres.DBForTest(s).ExecContext(ctx, `UPDATE telegram_worker_lease SET expires_at=clock_timestamp()-interval '1 second'`); err != nil {
			t.Fatal(err)
		}
		if err := secondStore.WithBotLease(deadline, func(context.Context) error {
			// Simulate a successor claiming after expiry. An old owner's release
			// must not delete a different ownership generation.
			_, err := postgres.DBForTest(s).ExecContext(ctx, `UPDATE telegram_worker_lease SET owner='successor'`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var owner string
		if err := postgres.DBForTest(s).QueryRowContext(ctx, `SELECT owner FROM telegram_worker_lease`).Scan(&owner); err != nil || owner != "successor" {
			t.Fatalf("stale owner deleted successor: %s %v", owner, err)
		}
		if _, err := postgres.DBForTest(s).ExecContext(ctx, `DELETE FROM telegram_worker_lease`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("request cancellation releases ownership", func(t *testing.T) {
		request, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.WithBotLease(request, func(context.Context) error { cancel(); return request.Err() })
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		fresh, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if err := secondStore.WithBotLease(fresh, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("cancelled request retained lease: %v", err)
		}
	})
}
