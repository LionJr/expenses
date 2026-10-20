package telegram

import (
	"errors"
	"fmt"
	"net/http"
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
	return errors.As(err, &api) && (api.Code == http.StatusUnauthorized || api.Code == http.StatusConflict || api.Code == http.StatusNotFound)
}
