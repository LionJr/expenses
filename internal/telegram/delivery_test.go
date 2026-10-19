package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/agent"
	"github.com/LionJr/expenses/internal/botstate"
)

func TestDeliveryPermanentFailureIsTerminal(t *testing.T) {
	for _, code := range []int{400, 403} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			sends := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sends++
				w.WriteHeader(code)
				fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"secret-provider-description"}`, code)
			}))
			defer server.Close()
			store := &memoryStore{}
			a := &responder{}
			b := Bot{BaseURL: server.URL, Token: "secret", HTTP: server.Client(), Allowed: map[int64]bool{42: true}, Store: store, Agent: a}
			u := update(t, `{"update_id":2,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"кофе 1"}}`)
			if err := b.Handle(context.Background(), u); err != nil {
				t.Fatal(err)
			}
			if store.state != "failed" || store.errorCode != code || store.attempts != 1 {
				t.Fatalf("failure not persisted: %+v", store)
			}
			if err := b.Handle(context.Background(), u); err != nil {
				t.Fatal(err)
			}
			if err := b.deliverPending(context.Background()); err != nil {
				t.Fatal(err)
			}
			if sends != 1 || a.calls != 1 {
				t.Fatal("terminal delivery was retried")
			}
		})
	}
}
func TestTransientDeliveryRetriesAreBounded(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		w.WriteHeader(503)
		fmt.Fprint(w, `{"ok":false,"error_code":503}`)
	}))
	defer server.Close()
	store := &memoryStore{}
	a := &responder{}
	b := Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{42: true}, Store: store, Agent: a}
	u := update(t, `{"update_id":2,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"кофе 1"}}`)
	if err := b.Handle(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	// Drive scheduled outbox attempts explicitly; this fake does not wait on clocks.
	for i := 0; i < 4; i++ {
		if err := b.deliverPending(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if store.state != "failed" || store.attempts != 3 || sends != 3 || a.calls != 1 {
		t.Fatal("retry limit or reply reuse failed")
	}
}
func TestRetryAfterIsPersistedWithoutSleepingPolling(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		w.WriteHeader(429)
		fmt.Fprint(w, `{"ok":false,"error_code":429,"parameters":{"retry_after":60}}`)
	}))
	defer server.Close()
	store := &memoryStore{}
	b := Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{42: true}, Store: store, Agent: &responder{}}
	u := update(t, `{"update_id":2,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"кофе 1"}}`)
	before := time.Now()
	if err := b.Handle(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if store.retryAt.Before(before.Add(60*time.Second)) || store.state != "done" || store.errorCode != 429 {
		t.Fatal("Retry-After was not persisted")
	}
	if err := b.Handle(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if sends != 1 {
		t.Fatal("duplicate update bypassed retry schedule")
	}
}
func TestPollingFatalErrorsSurface(t *testing.T) {
	for _, code := range []int{401, 404, 409} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				fmt.Fprintf(w, `{"ok":false,"error_code":%d}`, code)
			}))
			defer server.Close()
			b := Bot{BaseURL: server.URL, Token: "secret-token", HTTP: server.Client(), Store: &memoryStore{}}
			err := b.Run(context.Background())
			var api *APIError
			if !errors.As(err, &api) || api.Code != code || strings.Contains(err.Error(), "secret-token") {
				t.Fatal("fatal error lost or leaked credentials")
			}
		})
	}
}

type updateStore struct {
	sessions map[int64]json.RawMessage
	rows     map[int64]*memoryStore
	users    map[int64]int64
}

func (s *updateStore) UpdateUser(context.Context, int64, string, string) error { return nil }
func (s *updateStore) LoadSession(_ context.Context, user int64) (json.RawMessage, error) {
	return s.sessions[user], nil
}
func (s *updateStore) StartUpdate(ctx context.Context, id, user int64) (string, string, error) {
	if s.rows[id] == nil {
		s.rows[id] = &memoryStore{}
		s.users[id] = user
	}
	return s.rows[id].StartUpdate(ctx, id, user)
}
func (s *updateStore) FinishUpdate(ctx context.Context, id int64, reply string, data json.RawMessage, user int64) error {
	if err := s.rows[id].FinishUpdate(ctx, id, reply, data, user); err != nil {
		return err
	}
	if s.sessions == nil {
		s.sessions = map[int64]json.RawMessage{}
	}
	s.sessions[user] = append(json.RawMessage(nil), data...)
	var session agent.Session
	_ = json.Unmarshal(data, &session)
	var choiceID int64
	if session.Choice != nil {
		choiceID = session.Choice.UpdateID
	}
	for otherID, row := range s.rows {
		if s.users[otherID] == user && otherID != choiceID && row.state == "done" && row.isChoice {
			row.state = "superseded"
		}
	}
	return nil
}
func (s *updateStore) MarkSent(ctx context.Context, id int64) error {
	if err := s.rows[id].MarkSent(ctx, id); err != nil {
		return err
	}
	if s.rows[id].isChoice {
		user := s.users[id]
		s.sessions[user] = markChoiceSent(s.sessions[user], id)
	}
	return nil
}
func (s *updateStore) FailDelivery(ctx context.Context, id int64, code int, permanent bool, at time.Time) error {
	if err := s.rows[id].FailDelivery(ctx, id, code, permanent, at); err != nil {
		return err
	}
	if s.rows[id].isChoice && s.rows[id].state == "failed" {
		user := s.users[id]
		s.sessions[user] = clearFailedChoice(s.sessions[user], id)
	}
	return nil
}
func (s *updateStore) PendingDeliveries(context.Context, int) ([]botstate.Delivery, error) {
	var out []botstate.Delivery
	for id, row := range s.rows {
		if row.state == "done" && !row.retryAt.After(time.Now()) {
			out = append(out, botstate.Delivery{UpdateID: id, UserID: s.users[id], Reply: row.reply, Attempts: row.attempts, ChoicePrompt: row.isChoice})
		}
	}
	return out, nil
}
func TestUndeliverableChatDoesNotBlockOtherUserOrOffset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var mu sync.Mutex
	offsetAdvanced := false
	sends := map[int64]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			var request struct {
				Offset int64 `json:"offset"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request.Offset >= 3 {
				mu.Lock()
				offsetAdvanced = true
				mu.Unlock()
				fmt.Fprint(w, `{"ok":true,"result":[]}`)
				cancel()
				return
			}
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"кофе 1"}},{"update_id":2,"message":{"from":{"id":99},"chat":{"id":99,"type":"private"},"text":"кофе 2"}}]}`)
			return
		}
		var message struct {
			Chat int64 `json:"chat_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&message)
		mu.Lock()
		sends[message.Chat]++
		mu.Unlock()
		if message.Chat == 42 {
			w.WriteHeader(403)
			fmt.Fprint(w, `{"ok":false,"error_code":403}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()
	store := &updateStore{rows: map[int64]*memoryStore{}, users: map[int64]int64{}}
	a := &responder{}
	b := Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{42: true, 99: true}, Store: store, Agent: a}
	if err := b.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !offsetAdvanced || sends[42] != 1 || sends[99] != 1 || a.calls != 2 || store.rows[1].state != "failed" || store.rows[2].state != "sent" {
		t.Fatalf("blocked another user: offset=%v sends=%v calls=%d", offsetAdvanced, sends, a.calls)
	}
}

func TestRevokedUserDoesNotReceivePendingReply(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends++; fmt.Fprint(w, `{"ok":true,"result":{}}`) }))
	defer server.Close()
	store := &memoryStore{state: "done", reply: "private data"}
	b := Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{}, Store: store}
	if err := b.deliverPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sends != 0 || store.state != "failed" {
		t.Fatal("revoked user received a reply")
	}
}

func (s *updateStore) ChoicePromptCurrent(_ context.Context, id, user int64) (bool, error) {
	return choiceCurrent(s.sessions[user], id), nil
}
func (s *updateStore) SupersedeDelivery(ctx context.Context, id int64) error {
	return s.rows[id].SupersedeDelivery(ctx, id)
}

func (s *updateStore) MarkChoiceSent(ctx context.Context, id, messageID int64) error {
	if err := s.MarkSent(ctx, id); err != nil {
		return err
	}
	user := s.users[id]
	s.sessions[user] = withMessageID(s.sessions[user], id, messageID)
	return nil
}
