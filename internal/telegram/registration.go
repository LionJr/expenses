package telegram

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// RegisterWebhook fails on permanent API errors and honors Telegram backoff.
// All exposed errors are sanitized by call/APIError; no token-bearing URLs.
func (b *Bot) RegisterWebhook(ctx context.Context, origin, secret string) error {
	return b.registerWebhook(ctx, origin, secret, func(ctx context.Context, delay time.Duration) error {
		if !pause(ctx, delay) {
			return ctx.Err()
		}
		return nil
	})
}

func (b *Bot) registerWebhook(ctx context.Context, origin, secret string, wait func(context.Context, time.Duration) error) error {
	delay := 5 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := b.SetWebhook(request, origin, secret)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := 0
		var api *APIError
		if errors.As(err, &api) {
			code = api.Code
			if code >= 400 && code < 500 && code != http.StatusTooManyRequests && code != http.StatusRequestTimeout {
				return err
			}
		}
		backoff := delay
		if api != nil && api.RetryAfter > backoff {
			backoff = api.RetryAfter
		}
		slog.Warn("Telegram webhook registration deferred", "error_code", code, "retry_after_ms", backoff.Milliseconds())
		if err = wait(ctx, backoff); err != nil {
			return err
		}
		delay = min(delay*2, time.Minute)
	}
}
