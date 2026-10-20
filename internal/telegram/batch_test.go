package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LionJr/expenses/internal/agent"
)

type replyFunc func(context.Context, int64, int64, string, agent.Session) (string, agent.Session, error)

func (f replyFunc) Reply(ctx context.Context, user, update int64, text string, s agent.Session) (string, agent.Session, error) {
	return f(ctx, user, update, text, s)
}
func batchUpdates(t *testing.T) []Update {
	t.Helper()
	var out []Update
	for _, v := range []struct{ id, user int64 }{{1, 42}, {2, 99}, {3, 42}} {
		out = append(out, update(t, fmt.Sprintf(`{"update_id":%d,"message":{"from":{"id":%d},"chat":{"id":%d,"type":"private"},"text":"test"}}`, v.id, v.user, v.user)))
	}
	return out
}
func batchBot(t *testing.T, responder Responder) *Bot {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ok":true,"result":{}}`) }))
	t.Cleanup(server.Close)
	return &Bot{BaseURL: server.URL, HTTP: server.Client(), Allowed: map[int64]bool{42: true, 99: true}, Store: newChoiceStore(), Agent: responder}
}

func TestBatchProcessesOtherUserWhilePreservingSessionOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	slowStarted, fastFinished, secondStarted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	b := batchBot(t, replyFunc(func(ctx context.Context, user, id int64, _ string, s agent.Session) (string, agent.Session, error) {
		switch id {
		case 1:
			close(slowStarted)
			select {
			case <-release:
			case <-ctx.Done():
				return "", s, ctx.Err()
			}
			s.History = append(s.History, agent.Message{Role: "assistant", Content: "first completed"})
		case 2:
			select {
			case <-slowStarted:
			case <-ctx.Done():
				return "", s, ctx.Err()
			}
			close(fastFinished)
		case 3:
			close(secondStarted)
			if len(s.History) != 1 || s.History[0].Content != "first completed" {
				return "", s, errors.New("previous session missing")
			}
		}
		return "done", s, nil
	}))
	type result struct {
		completed int
		err       error
	}
	done := make(chan result, 1)
	go func() { n, err := b.handleBatch(ctx, batchUpdates(t)); done <- result{n, err} }()
	select {
	case <-fastFinished:
	case <-ctx.Done():
		t.Fatal("slow user blocked other user")
	}
	select {
	case <-secondStarted:
		t.Fatal("same-user requests overlapped")
	default:
	}
	close(release)
	select {
	case r := <-done:
		if r.completed != 3 || r.err != nil {
			t.Fatalf("batch: %+v", r)
		}
	case <-ctx.Done():
		t.Fatal("workers did not finish")
	}
}

func TestBatchFailureKeepsOffsetAndReplaysWithoutRepeatingOtherUser(t *testing.T) {
	transient := errors.New("retryable storage failure")
	var mu sync.Mutex
	calls := map[int64]int{}
	fail := true
	b := batchBot(t, replyFunc(func(_ context.Context, _, id int64, _ string, s agent.Session) (string, agent.Session, error) {
		mu.Lock()
		defer mu.Unlock()
		calls[id]++
		if id == 1 && fail {
			return "", s, transient
		}
		return "done", s, nil
	}))
	updates := batchUpdates(t)
	n, err := b.handleBatch(context.Background(), updates)
	if n != 0 || !errors.Is(err, transient) || calls[1] != 1 || calls[2] != 1 || calls[3] != 0 {
		t.Fatalf("first batch: %d %v %v", n, err, calls)
	}
	fail = false
	n, err = b.handleBatch(context.Background(), updates)
	if n != 3 || err != nil || calls[1] != 2 || calls[2] != 1 || calls[3] != 1 {
		t.Fatalf("replay: %d %v %v", n, err, calls)
	}
}

func TestBatchSurfacesFatalErrorAfterEarlierRetryableFailure(t *testing.T) {
	b := batchBot(t, replyFunc(func(_ context.Context, user, _ int64, _ string, s agent.Session) (string, agent.Session, error) {
		if user == 42 {
			return "", s, errors.New("retryable")
		}
		return "", s, &APIError{Code: http.StatusUnauthorized}
	}))
	n, err := b.handleBatch(context.Background(), batchUpdates(t))
	if n != 0 || !fatal(err) {
		t.Fatalf("fatal error masked: %d %v", n, err)
	}
}

func TestBatchCancellationWaitsForWorkersAndSkipsLaterUserMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 2)
	var mu sync.Mutex
	calls := map[int64]bool{}
	b := batchBot(t, replyFunc(func(ctx context.Context, _, id int64, _ string, s agent.Session) (string, agent.Session, error) {
		mu.Lock()
		calls[id] = true
		mu.Unlock()
		started <- struct{}{}
		<-ctx.Done()
		return "", s, ctx.Err()
	}))
	done := make(chan error, 1)
	go func() { _, err := b.handleBatch(ctx, batchUpdates(t)); done <- err }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("workers not started")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || calls[3] {
			t.Fatalf("cancellation: %v %v", err, calls)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workers leaked")
	}
}
