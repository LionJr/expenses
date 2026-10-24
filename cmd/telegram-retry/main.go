// telegram-retry requeues one saved reply abandoned by an older retry policy.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/LionJr/expenses/internal/config"
	"github.com/LionJr/expenses/internal/postgres"
)

func main() {
	id := flag.Int64("update", 0, "Telegram update ID from the delivery log")
	flag.Parse()
	if err := run(*id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Saved reply queued; the next bot outbox pass will attempt delivery.")
}

func run(id int64) error {
	if id <= 0 {
		return fmt.Errorf("provide a positive -update ID")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, cfg.DatabaseURL, cfg.Allowed)
	if err != nil {
		return fmt.Errorf("database unavailable; check connection settings")
	}
	defer store.Close()
	queued, err := store.RetryFailedDelivery(ctx, id)
	if err != nil {
		return fmt.Errorf("could not queue the saved reply")
	}
	if !queued {
		return fmt.Errorf("no eligible failed reply: only transient failures for allowed users and ordinary replies can be retried")
	}
	return nil
}
