package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegistrationPermanentErrorsFailWithoutRetryOrSecretLeak(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 409} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(code)
				fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"secret-token"}`, code)
			}))
			defer server.Close()
			b := &Bot{BaseURL: server.URL, Token: "secret-token", HTTP: server.Client()}
			err := b.registerWebhook(t.Context(), "https://family.example", testWebhookSecret, func(context.Context, time.Duration) error { t.Fatal("permanent error retried"); return nil })
			var api *APIError
			if !errors.As(err, &api) || api.Code != code || calls != 1 || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("unsafe permanent error: %v calls=%d", err, calls)
			}
		})
	}
}

func TestRegistrationHonorsRetryAfterAndRetriesTransientErrors(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.WriteHeader(429)
			fmt.Fprint(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":120}}`)
		case 2:
			w.WriteHeader(503)
			fmt.Fprint(w, `{"ok":false,"error_code":503}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":true}`)
		}
	}))
	defer server.Close()
	b := &Bot{BaseURL: server.URL, Token: "secret-token", HTTP: server.Client()}
	var delays []time.Duration
	err := b.registerWebhook(t.Context(), "https://family.example", testWebhookSecret, func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil })
	if err != nil || calls != 3 || len(delays) != 2 || delays[0] != 120*time.Second || delays[1] != 10*time.Second {
		t.Fatalf("incorrect backoff: calls=%d delays=%v error=%v", calls, delays, err)
	}
}

func TestRegistrationCancellationStopsRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(500)
		fmt.Fprint(w, `{"ok":false,"error_code":500}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	b := &Bot{BaseURL: server.URL, HTTP: server.Client()}
	err := b.registerWebhook(ctx, "https://family.example", testWebhookSecret, func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation ignored: %v calls=%d", err, calls)
	}
}
