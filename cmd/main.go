package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LionJr/expenses/internal/agent"
	"github.com/LionJr/expenses/internal/config"
	"github.com/LionJr/expenses/internal/expensesvc"
	"github.com/LionJr/expenses/internal/httpapi"
	"github.com/LionJr/expenses/internal/postgres"
	"github.com/LionJr/expenses/internal/telegram"
)

func main() {
	if err := run(); err != nil {
		slog.Error("application stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	store, err := postgres.Open(startup, cfg.DatabaseURL, cfg.Allowed)
	if err != nil {
		return err
	}
	defer store.Close()

	expenses := expensesvc.New(store)
	api := &httpapi.API{Store: expenses, Timezone: cfg.Location.String(), RequestTimeout: cfg.HTTPRequestTimeout}
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	botErrors := make(chan error, 1)
	botDone := make(chan struct{})
	if cfg.BotToken != "" {
		allowed := make(map[int64]bool, len(cfg.Allowed))
		for _, id := range cfg.Allowed {
			allowed[id] = true
		}
		a := &agent.Agent{
			Store: expenses,
			LLM: &agent.Client{
				BaseURL: cfg.LLMURL,
				Key:     cfg.LLMKey,
				Model:   cfg.LLMModel,
				HTTP:    &http.Client{Timeout: cfg.LLMTimeout},
			},
			Location: cfg.Location,
			Timeout:  cfg.LLMTimeout,
		}
		bot := &telegram.Bot{
			BaseURL: cfg.TelegramURL,
			Token:   cfg.BotToken,
			HTTP:    &http.Client{Timeout: 35 * time.Second},
			Allowed: allowed,
			Store:   store,
			Agent:   a,
		}
		go func() {
			defer close(botDone)
			botErrors <- bot.Run(ctx)
		}()
		slog.Info("Telegram polling enabled")
	} else {
		close(botDone)
		slog.Info("Telegram disabled: configure TELEGRAM_BOT_TOKEN and LLM_API_KEY to enable")
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("panel listening", "address", cfg.Addr)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err = <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	case err = <-botErrors:
		if errors.Is(err, context.Canceled) {
			err = nil
		} else if err != nil {
			err = fmt.Errorf("telegram polling stopped: %w", err)
		}
	case <-ctx.Done():
	}

	stop()

	shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()

	shutdownErr := server.Shutdown(shutdown)
	select {
	case <-botDone:
	case <-shutdown.Done():
		shutdownErr = errors.Join(shutdownErr, shutdown.Err())
	}
	return errors.Join(err, shutdownErr)
}
