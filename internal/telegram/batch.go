package telegram

import (
	"context"
	"sync"
)

// handleBatch waits for all workers before the next poll or outbox pass, so
// delivery acknowledgements cannot race with a user's session transitions.
// A failed user's remaining messages wait for replay; other users still finish.
// Completed updates beyond the first failure are deduplicated on the next poll.
func (b *Bot) handleBatch(ctx context.Context, updates []Update) (int, error) {
	groups := make(map[int64][]int)
	for i, u := range updates {
		var user int64
		if u.Message != nil && u.Message.From != nil && b.Allowed[u.Message.From.ID] {
			user = u.Message.From.ID
		}
		groups[user] = append(groups[user], i)
	}
	failures := make([]error, len(updates))
	var workers sync.WaitGroup
	for _, indices := range groups {
		workers.Go(func() {
			var err error
			for _, i := range indices {
				if err == nil {
					err = b.Handle(ctx, updates[i])
				}
				failures[i] = err
			}
		})
	}
	workers.Wait()
	completed := len(updates)
	var first error
	for i, err := range failures {
		if err != nil && first == nil {
			completed, first = i, err
		}
	}
	// Fatal transport errors must surface even if an earlier update had a
	// retryable failure. No worker is left running when Run shuts down.
	for _, err := range failures {
		if fatal(err) {
			return completed, err
		}
	}
	return completed, first
}
