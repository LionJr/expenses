package main

import (
	"sync"
	"sync/atomic"
)

// Registration and requests may finish concurrently. Once failed, registration
// must never restore readiness; notify the supervisor exactly once without
// blocking an HTTP handler during shutdown.
type webhookLifecycle struct {
	mu     sync.Mutex
	failed bool
	ready  *atomic.Bool
	errors chan<- error
}

func (w *webhookLifecycle) registered() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.failed {
		w.ready.Store(true)
	}
}

func (w *webhookLifecycle) fail(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed {
		return
	}
	w.failed = true
	w.ready.Store(false)
	select {
	case w.errors <- err:
	default:
	}
}
