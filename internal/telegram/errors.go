package telegram

import (
	"errors"
	"fmt"
	"time"
)

// APIError deliberately excludes Telegram's description and the token-bearing URL.
type APIError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Telegram API error %d (retry after %s)", e.Code, e.RetryAfter)
}
func fatal(err error) bool {
	var api *APIError
	return errors.As(err, &api) && (api.Code == 401 || api.Code == 409 || api.Code == 404)
}
