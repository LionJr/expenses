package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/agent"
	"github.com/LionJr/expenses/internal/botstate"
)

const testWebhookSecret = "0123456789abcdef0123456789abcdef"
const testWebhookUpdate = `{"update_id":2,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"кофе 350"}}`

func webhookRequest(handler http.Handler, method, secret, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, WebhookPath, strings.NewReader(body))
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestWebhookRejectsInvalidAndUnauthorizedRequests(t *testing.T) {
	store := &memoryStore{}
	a := &responder{}
	b := &Bot{Store: store, Agent: a, Allowed: map[int64]bool{42: true}}
	handler := b.WebhookHandler(testWebhookSecret, time.Second)
	for _, test := range []struct {
		name, method, secret, body string
		status                     int
	}{
		{"missing secret", "POST", "", testWebhookUpdate, 403},
		{"wrong secret", "POST", "wrong", testWebhookUpdate, 403},
		{"wrong method", "GET", testWebhookSecret, "", 405},
		{"bad JSON", "POST", testWebhookSecret, "{", 400},
		{"missing ID", "POST", testWebhookSecret, `{}`, 400},
		{"extra JSON", "POST", testWebhookSecret, testWebhookUpdate + `{}`, 400},
		{"large body", "POST", testWebhookSecret, `{"update_id":2,"padding":"` + strings.Repeat("x", 1<<20) + `"}`, 400},
		{"other user", "POST", testWebhookSecret, strings.ReplaceAll(testWebhookUpdate, "42", "99"), 200},
		{"group chat", "POST", testWebhookSecret, strings.ReplaceAll(testWebhookUpdate, "private", "group"), 200},
		{"non-message update", "POST", testWebhookSecret, `{"update_id":2,"edited_message":{}}`, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := webhookRequest(handler, test.method, test.secret, test.body).Code; got != test.status {
				t.Fatalf("status %d, want %d", got, test.status)
			}
		})
	}
	if store.starts != 0 || a.calls != 0 {
		t.Fatal("rejected request reached storage/LLM")
	}
}

// Use the production outbox scheduling behavior, unlike the original polling
// fixture which makes retries immediately eligible for convenience.
type scheduledWebhookStore struct{ memoryStore }

func (s *scheduledWebhookStore) PendingDeliveries(ctx context.Context, limit int) ([]botstate.Delivery, error) {
	if s.retryAt.After(time.Now()) {
		return nil, nil
	}
	return s.memoryStore.PendingDeliveries(ctx, limit)
}

func TestWebhookRetryUsesSavedReplyAndRespectsRetryAfter(t *testing.T) {
	for _, code := range []int{500, 408, 429, 403} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			sends := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				if sends == 1 {
					w.WriteHeader(code)
					fmt.Fprintf(w, `{"ok":false,"error_code":%d,"parameters":{"retry_after":60}}`, code)
					return
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":9}}`)
			}))
			defer server.Close()
			store := &scheduledWebhookStore{}
			a := &responder{}
			b := &Bot{BaseURL: server.URL, HTTP: server.Client(), Store: store, Agent: a, Allowed: map[int64]bool{42: true}}
			handler := b.WebhookHandler(testWebhookSecret, time.Second)
			first := webhookRequest(handler, "POST", testWebhookSecret, testWebhookUpdate)
			if code == 403 {
				if first.Code != 200 || store.state != "failed" {
					t.Fatalf("permanent failure: status=%d state=%s", first.Code, store.state)
				}
				return
			}
			if first.Code != 503 || store.state != "done" {
				t.Fatalf("lost retry: status=%d state=%s", first.Code, store.state)
			}
			// Simulate a process restart: only the persistent store survives.
			handler = b.WebhookHandler(testWebhookSecret, time.Second)
			if got := webhookRequest(handler, "POST", testWebhookSecret, testWebhookUpdate).Code; got != 503 || sends != 1 {
				t.Fatalf("ignored retry_at: status=%d sends=%d", got, sends)
			}
			store.retryAt = time.Now().Add(-time.Second)
			if got := webhookRequest(handler, "POST", testWebhookSecret, testWebhookUpdate).Code; got != 200 {
				t.Fatalf("retry status=%d", got)
			}
			if got := webhookRequest(handler, "POST", testWebhookSecret, testWebhookUpdate).Code; got != 200 {
				t.Fatalf("duplicate status=%d", got)
			}
			if a.calls != 1 || sends != 2 || store.state != "sent" {
				t.Fatalf("duplicate work: LLM=%d sends=%d state=%s", a.calls, sends, store.state)
			}
		})
	}
}

func TestWebhookKeepsRetryingAfterThreeTransientFailures(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		if sends <= 4 {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"ok":false,"error_code":500}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":9}}`)
	}))
	defer server.Close()
	store := &scheduledWebhookStore{}
	a := &responder{}
	b := &Bot{BaseURL: server.URL, HTTP: server.Client(), Store: store, Agent: a, Allowed: map[int64]bool{42: true}}
	for i := 0; i < 5; i++ {
		store.retryAt = time.Now().Add(-time.Second)
		before := time.Now()
		// The handler's in-memory state may disappear between attempts.
		handler := b.WebhookHandler(testWebhookSecret, time.Second)
		w := webhookRequest(handler, "POST", testWebhookSecret, testWebhookUpdate)
		if i < 4 {
			minimum := 5 * time.Second * time.Duration(1<<i)
			if w.Code != 503 || store.state != "done" || store.retryAt.Before(before.Add(minimum)) {
				t.Fatalf("attempt=%d status=%d state=%s retry=%s", i, w.Code, store.state, store.retryAt)
			}
			if got := webhookRequest(handler, "POST", testWebhookSecret, testWebhookUpdate).Code; got != 503 || sends != i+1 {
				t.Fatalf("backoff bypassed: status=%d sends=%d", got, sends)
			}
		} else if w.Code != 200 || store.state != "sent" {
			t.Fatalf("recovery status=%d state=%s", w.Code, store.state)
		}
	}
	if a.calls != 1 || sends != 5 {
		t.Fatalf("duplicate processing: LLM=%d sends=%d", a.calls, sends)
	}
}

type blockingWebhookResponder struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingWebhookResponder) Reply(ctx context.Context, _, _ int64, _ string, session agent.Session) (string, agent.Session, error) {
	close(b.entered)
	select {
	case <-b.release:
		return "Готово", session, nil
	case <-ctx.Done():
		return "", session, ctx.Err()
	}
}

func TestWebhookSerializesOverlappingRetries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()
	a := &blockingWebhookResponder{entered: make(chan struct{}), release: make(chan struct{})}
	b := &Bot{BaseURL: server.URL, HTTP: server.Client(), Store: &memoryStore{}, Agent: a, Allowed: map[int64]bool{42: true}}
	handler := b.WebhookHandler(testWebhookSecret, time.Second)
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- webhookRequest(handler, "POST", testWebhookSecret, testWebhookUpdate) }()
	<-a.entered
	if got := webhookRequest(handler, "POST", testWebhookSecret, testWebhookUpdate).Code; got != 503 {
		t.Fatalf("overlapping update accepted: %d", got)
	}
	close(a.release)
	if got := (<-finished).Code; got != 200 {
		t.Fatalf("first update failed: %d", got)
	}
}

func TestIndependentWebhookHandlersShareWorkerOwnership(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends++; fmt.Fprint(w, `{"ok":true,"result":{}}`) }))
	defer server.Close()
	a := &blockingWebhookResponder{entered: make(chan struct{}), release: make(chan struct{})}
	store := &updateStore{rows: map[int64]*memoryStore{}, users: map[int64]int64{}}
	firstBot := &Bot{BaseURL: server.URL, HTTP: server.Client(), Store: store, Agent: a, Allowed: map[int64]bool{42: true}}
	secondBot := &Bot{BaseURL: server.URL, HTTP: server.Client(), Store: store, Agent: a, Allowed: map[int64]bool{42: true}}
	first, second := firstBot.WebhookHandler(testWebhookSecret, 5*time.Second), secondBot.WebhookHandler(testWebhookSecret, 5*time.Second)
	done := make(chan int, 1)
	go func() { done <- webhookRequest(first, "POST", testWebhookSecret, testWebhookUpdate).Code }()
	<-a.entered
	if w := webhookRequest(second, "POST", testWebhookSecret, testWebhookUpdate); w.Code != 503 {
		t.Fatal("another handler processed an active update")
	}
	close(a.release)
	if status := <-done; status != 200 {
		t.Fatalf("first handler failed: %d", status)
	}
	if w := webhookRequest(second, "POST", testWebhookSecret, testWebhookUpdate); w.Code != 200 {
		t.Fatal("retry did not recover")
	}
	if sends != 1 {
		t.Fatalf("duplicate update sent %d replies", sends)
	}
}

func TestSetWebhookPreservesPendingUpdatesAndSerialDelivery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botsecret/setWebhook" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		var payload struct {
			URL         string   `json:"url"`
			Secret      string   `json:"secret_token"`
			Connections int      `json:"max_connections"`
			Drop        bool     `json:"drop_pending_updates"`
			Updates     []string `json:"allowed_updates"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.URL != "https://expenses.example/telegram/webhook" || payload.Secret != testWebhookSecret || payload.Connections != 1 || payload.Drop || len(payload.Updates) != 1 || payload.Updates[0] != "message" {
			t.Errorf("unsafe registration: %+v", payload)
		}
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}))
	defer server.Close()
	b := &Bot{BaseURL: server.URL, HTTP: server.Client(), Token: "secret"}
	if err := b.SetWebhook(context.Background(), "https://expenses.example", testWebhookSecret); err != nil {
		t.Fatal(err)
	}
}
