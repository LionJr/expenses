// panel-invite creates a personal setup link when Telegram is not enabled.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/LionJr/expenses/internal/config"
	"github.com/LionJr/expenses/internal/panelauth"
	"github.com/LionJr/expenses/internal/postgres"
)

func main() {
	id := flag.Int64("user", 0, "Allowed numeric Telegram user ID")
	flag.Parse()
	if err := run(*id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(id int64) error {
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
	service, err := panelauth.New(store, cfg.PublicURL, cfg.PanelAuthKey, cfg.PanelMasterID)
	if err != nil {
		return err
	}
	link, err := service.Invite(ctx, id)
	if err != nil {
		return err
	}
	fmt.Println(link) // Explicit operator output; never logged by the web server.
	return nil
}
