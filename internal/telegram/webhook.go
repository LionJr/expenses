package telegram

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

const WebhookPath = "/telegram/webhook"

func (b *Bot) SetWebhook(ctx context.Context, origin, secret string) error {
	return b.call(ctx, "setWebhook", map[string]any{
		"url":                  origin + WebhookPath,
		"secret_token":         secret,
		"max_connections":      1,
		"allowed_updates":      []string{"message"},
		"drop_pending_updates": false,
	}, nil)
}

// WebhookHandler completes work within the HTTP request. Telegram retries a
// non-2xx response, including saved replies awaiting their next delivery time.
// There is no background DB polling, allowing Neon to suspend when idle.
// Run one instance, with max_connections=1, to preserve conversation order.
func (b *Bot) WebhookHandler(secret string, timeout time.Duration) http.Handler {
	gate := make(chan struct{}, 1)
	want := sha256.Sum256([]byte(secret))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		got := sha256.Sum256([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")))
		if secret == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var update Update
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := decoder.Decode(&update); err != nil || update.ID <= 0 {
			http.Error(w, "Invalid update", http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			http.Error(w, "Invalid update", http.StatusBadRequest)
			return
		}
		m := update.Message
		if m == nil || m.From == nil || m.From.IsBot || m.Chat.Type != "private" || m.Chat.ID != m.From.ID {
			w.WriteHeader(http.StatusOK)
			return
		}
		if !b.Allowed[m.From.ID] {
			slog.Warn("Telegram access denied", "user_id", m.From.ID, "update_id", update.ID)
			w.WriteHeader(http.StatusOK)
			return
		}
		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		pending := false
		err := b.Store.WithBotLease(ctx, func(ctx context.Context) error {
			if err := b.deliverPending(ctx); err != nil {
				return err
			}
			if err := b.Handle(ctx, update); err != nil {
				return err
			}
			var err error
			pending, err = b.Store.DeliveryPending(ctx, update.ID)
			return err
		})
		if err != nil || pending {
			if fatal(err) && b.OnFatal != nil {
				var api *APIError
				errors.As(err, &api)
				b.OnFatal(&APIError{Code: api.Code})
			}
			// Do not log transport/storage errors here: they may contain credentials.
			slog.Warn("Telegram webhook deferred", "update_id", update.ID, "pending_delivery", pending)
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}
