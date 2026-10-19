package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/LionJr/expenses/internal/botstate"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/agent"
)

type memoryStore struct {
	state, reply string
	session      json.RawMessage
	starts       int
	attempts     int
	retryAt      time.Time
	errorCode    int
	finishError  error
	isChoice     bool
}

func (s *memoryStore) UpdateUser(context.Context, int64, string, string) error { return nil }
func (s *memoryStore) LoadSession(context.Context, int64) (json.RawMessage, error) {
	return s.session, nil
}
func (s *memoryStore) StartUpdate(context.Context, int64, int64) (string, string, error) {
	s.starts++
	if s.state == "" {
		s.state = "processing"
	}
	return s.state, s.reply, nil
}
func (s *memoryStore) FinishUpdate(_ context.Context, id int64, reply string, b json.RawMessage, _ int64) error {
	if s.finishError != nil {
		return s.finishError
	}
	s.state = "done"
	s.reply = reply
	s.session = b
	var metadata struct {
		Choice *botstate.Choice `json:"choice"`
	}
	_ = json.Unmarshal(b, &metadata)
	s.isChoice = metadata.Choice != nil && metadata.Choice.UpdateID == id && metadata.Choice.State == botstate.AwaitingDelivery
	return nil
}
func (s *memoryStore) MarkSent(_ context.Context, id int64) error {
	s.state = "sent"
	if s.isChoice {
		s.session = markChoiceSent(s.session, id)
	}
	return nil
}

type responder struct{ calls int }

func (r *responder) Reply(_ context.Context, _, _ int64, _ string, s agent.Session) (string, agent.Session, error) {
	r.calls++
	return "Готово", s, nil
}
func update(t *testing.T, body string) Update {
	t.Helper()
	var u Update
	if e := json.Unmarshal([]byte(body), &u); e != nil {
		t.Fatal(e)
	}
	return u
}
func TestAllowlistPrivateChatAndDuplicate(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends++; fmt.Fprint(w, `{"ok":true,"result":{}}`) }))
	defer server.Close()
	store := &memoryStore{}
	a := &responder{}
	b := Bot{BaseURL: server.URL, Token: "secret", HTTP: server.Client(), Allowed: map[int64]bool{42: true}, Store: store, Agent: a}
	for _, body := range []string{`{"update_id":1,"message":{"from":{"id":99},"chat":{"id":99,"type":"private"},"text":"кофе 1"}}`, `{"update_id":1,"message":{"from":{"id":42},"chat":{"id":-1,"type":"group"},"text":"кофе 1"}}`} {
		if e := b.Handle(context.Background(), update(t, body)); e != nil {
			t.Fatal(e)
		}
	}
	if store.starts != 0 || a.calls != 0 || sends != 0 {
		t.Fatal("unauthorized update reached storage/LLM")
	}
	u := update(t, `{"update_id":2,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"кофе 1"}}`)
	for i := 0; i < 2; i++ {
		if e := b.Handle(context.Background(), u); e != nil {
			t.Fatal(e)
		}
	}
	if a.calls != 1 || sends != 1 || store.state != "sent" {
		t.Fatal("duplicate update reprocessed")
	}
}
func TestDeliveryFailureUsesSavedReply(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		if sends == 1 {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"ok":false,"error_code":500}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	defer server.Close()
	store := &memoryStore{}
	a := &responder{}
	b := Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{42: true}, Store: store, Agent: a}
	u := update(t, `{"update_id":2,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"кофе 1"}}`)
	if e := b.Handle(context.Background(), u); e != nil {
		t.Fatal(e)
	}
	if store.state != "done" || store.attempts != 1 {
		t.Fatal("transient failure must remain in durable outbox")
	}
	if e := b.deliverPending(context.Background()); e != nil {
		t.Fatal(e)
	}
	if a.calls != 1 || sends != 2 {
		t.Fatal("delivery retry reran agent")
	}
}

func (s *memoryStore) PendingDeliveries(context.Context, int) ([]botstate.Delivery, error) {
	if s.state == "done" {
		return []botstate.Delivery{{UpdateID: 2, UserID: 42, Reply: s.reply, Attempts: s.attempts, ChoicePrompt: s.isChoice}}, nil
	}
	return nil, nil
}
func (s *memoryStore) FailDelivery(_ context.Context, id int64, code int, permanent bool, retryAt time.Time) error {
	s.attempts++
	s.retryAt = retryAt
	s.errorCode = code
	if permanent || s.attempts >= 3 {
		s.state = "failed"
		if s.isChoice {
			s.session = clearFailedChoice(s.session, id)
		}
	}
	return nil
}

func markChoiceSent(data json.RawMessage, id int64) json.RawMessage {
	var session agent.Session
	if json.Unmarshal(data, &session) != nil {
		return data
	}
	if session.Choice != nil && session.Choice.UpdateID == id && session.Choice.State == botstate.AwaitingDelivery {
		session.Choice.State = botstate.AwaitingSelection
	}
	updated, _ := json.Marshal(session)
	return updated
}
func choiceCurrent(data json.RawMessage, id int64) bool {
	var session agent.Session
	return json.Unmarshal(data, &session) == nil && session.Choice != nil && session.Choice.UpdateID == id && session.Choice.State == botstate.AwaitingDelivery
}
func (s *memoryStore) ChoicePromptCurrent(_ context.Context, id, user int64) (bool, error) {
	return choiceCurrent(s.session, id), nil
}
func (s *memoryStore) SupersedeDelivery(context.Context, int64) error {
	if s.state == "done" {
		s.state = "superseded"
	}
	return nil
}

func (s *memoryStore) MarkChoiceSent(ctx context.Context, id, messageID int64) error {
	if err := s.MarkSent(ctx, id); err != nil {
		return err
	}
	s.session = withMessageID(s.session, id, messageID)
	return nil
}
func withMessageID(data json.RawMessage, id, messageID int64) json.RawMessage {
	var session agent.Session
	_ = json.Unmarshal(data, &session)
	if session.Choice != nil && session.Choice.UpdateID == id {
		session.Choice.MessageID = messageID
	}
	updated, _ := json.Marshal(session)
	return updated
}

func clearFailedChoice(data json.RawMessage, id int64) json.RawMessage {
	var session agent.Session
	if json.Unmarshal(data, &session) != nil {
		return data
	}
	if session.Choice != nil && session.Choice.UpdateID == id && session.Choice.State == botstate.AwaitingDelivery {
		session.Choice = nil
		session.Pending = nil
		session.Candidates = nil
		session.CandidateTotal = 0
	}
	updated, _ := json.Marshal(session)
	return updated
}
