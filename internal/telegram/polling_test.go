package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCallPreservesStatusForMalformedResponse(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusConflict, http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusOK} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				fmt.Fprint(w, "<html>upstream error</html>")
			}))
			defer server.Close()
			bot := Bot{BaseURL: server.URL, HTTP: server.Client()}
			err := bot.call(context.Background(), "getUpdates", nil, nil)
			var api *APIError
			if code == http.StatusOK {
				if err == nil || errors.As(err, &api) {
					t.Fatalf("expected malformed-response error, got %v", err)
				}
			} else if !errors.As(err, &api) || api.Code != code {
				t.Fatalf("HTTP status lost: %v", err)
			}
		})
	}
}

func TestRunBoundsIdlePollingForOutboxRetries(t *testing.T) {
	var timeout int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Timeout int `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		timeout = request.Timeout
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "not JSON")
	}))
	defer server.Close()
	bot := Bot{BaseURL: server.URL, HTTP: server.Client(), Store: &memoryStore{}}
	if err := bot.Run(context.Background()); !fatal(err) {
		t.Fatalf("expected fatal HTTP error, got %v", err)
	}
	if timeout <= 0 || timeout > 3 {
		t.Fatalf("polling timeout delays outbox retries: %d seconds", timeout)
	}
}
