package main

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/telegram"
)

func TestHealthRequiresSuccessfulWebhookRegistrationWithoutDatabaseQueries(t *testing.T) {
	var ready atomic.Bool
	handler := readiness(&ready)
	for _, state := range []bool{false, true, false} {
		ready.Store(state)
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("GET", "/healthz", nil))
		want := 503
		if state {
			want = 200
		}
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("state=%v status=%d", state, w.Code)
		}
	}
}

type fatalWebhookStore struct {
	telegram.Storage
	err error
}

func (s fatalWebhookStore) WithBotLease(context.Context, func(context.Context) error) error {
	return s.err
}

func TestRuntimeWebhookFailureDropsHealthAndNotifiesSupervisor(t *testing.T) {
	for _, code := range []int{401, 404, 409, 429, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			var ready atomic.Bool
			failures := make(chan error, 1)
			lifecycle := &webhookLifecycle{ready: &ready, errors: failures}
			lifecycle.registered()
			bot := &telegram.Bot{
				Allowed: map[int64]bool{42: true},
				Store:   fatalWebhookStore{err: fmt.Errorf("private-token: %w", &telegram.APIError{Code: code})},
				OnFatal: lifecycle.fail,
			}
			handler := bot.WebhookHandler("test-secret", time.Second)
			for i := 0; i < 2; i++ {
				r := httptest.NewRequest("POST", telegram.WebhookPath, strings.NewReader(`{"update_id":1,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"}}}`))
				r.Header.Set("X-Telegram-Bot-Api-Secret-Token", "test-secret")
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != 503 {
					t.Fatalf("failed webhook acknowledged: %d", w.Code)
				}
			}
			fatal := code == 401 || code == 404 || code == 409
			if fatal {
				select {
				case err := <-failures:
					var api *telegram.APIError
					if !errors.As(err, &api) || api.Code != code || strings.Contains(err.Error(), "private-token") {
						t.Fatalf("unsafe supervisor notification: %v", err)
					}
				default:
					t.Fatal("fatal error not propagated")
				}
				// A late registration completion must not undo a runtime failure.
				lifecycle.registered()
			}
			if len(failures) != 0 {
				t.Fatal("duplicate or transient failure notified supervisor")
			}
			w := httptest.NewRecorder()
			readiness(&ready)(w, httptest.NewRequest("GET", "/healthz", nil))
			if fatal && w.Code != 503 || !fatal && w.Code != 200 {
				t.Fatalf("fatal=%v health=%d", fatal, w.Code)
			}
		})
	}
}

func TestWebhookRegistrationCannotRaceFailureBackToReady(t *testing.T) {
	var ready atomic.Bool
	failures := make(chan error, 1)
	lifecycle := &webhookLifecycle{ready: &ready, errors: failures}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Go(lifecycle.registered)
		wg.Go(func() { lifecycle.fail(&telegram.APIError{Code: 401}) })
	}
	wg.Wait()
	if ready.Load() || len(failures) != 1 {
		t.Fatalf("ready=%v notifications=%d", ready.Load(), len(failures))
	}
}
