package config

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	DatabaseURL, Addr, BotToken, LLMURL, LLMKey, LLMModel string
	TelegramURL                                           string
	Allowed                                               []int64
	Location                                              *time.Location
	LLMTimeout                                            time.Duration
	HTTPRequestTimeout                                    time.Duration
	TelegramMode, WebhookSecret, PublicURL                string
	PanelAuthKey                                          string
	PanelMasterID                                         int64
	IsRender                                              bool
}

func Load() (Config, error) {
	c, err := initialize()
	if err != nil {
		return c, err
	}
	return c, validate(c)
}

func initialize() (Config, error) {
	c := Config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		Addr:          env("HTTP_ADDR", ":"+env("PORT", "8080")),
		BotToken:      os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramURL:   "https://api.telegram.org",
		LLMURL:        env("LLM_BASE_URL", "https://api.groq.com/openai/v1"),
		LLMKey:        os.Getenv("LLM_API_KEY"),
		LLMModel:      env("LLM_MODEL", "llama-3.3-70b-versatile"),
		TelegramMode:  env("TELEGRAM_MODE", "polling"),
		WebhookSecret: os.Getenv("TELEGRAM_WEBHOOK_SECRET"),
		PublicURL:     strings.TrimRight(env("PUBLIC_BASE_URL", env("RENDER_EXTERNAL_URL", "http://localhost:"+env("PORT", "8080"))), "/"),
		PanelAuthKey:  os.Getenv("PANEL_AUTH_KEY"),
		IsRender:      os.Getenv("RENDER") == "true" || os.Getenv("RENDER_EXTERNAL_URL") != "",
	}
	var err error
	c.Location, err = time.LoadLocation(env("APP_TIMEZONE", "Europe/Moscow"))
	if err != nil {
		return c, fmt.Errorf("APP_TIMEZONE: %w", err)
	}
	c.LLMTimeout, err = time.ParseDuration(env("LLM_TIMEOUT", "30s"))
	if err != nil {
		return c, fmt.Errorf("LLM_TIMEOUT must be between 1s and 2m")
	}
	c.HTTPRequestTimeout, err = time.ParseDuration(env("HTTP_REQUEST_TIMEOUT", "5s"))
	if err != nil {
		return c, fmt.Errorf("HTTP_REQUEST_TIMEOUT must be a positive duration")
	}
	seen := map[int64]bool{}
	for _, v := range strings.Split(os.Getenv("TELEGRAM_ALLOWED_USER_IDS"), ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		id, e := strconv.ParseInt(v, 10, 64)
		if e != nil {
			return c, fmt.Errorf("invalid TELEGRAM_ALLOWED_USER_IDS")
		}
		if !seen[id] {
			c.Allowed = append(c.Allowed, id)
			seen[id] = true
		}
	}
	c.PanelMasterID, err = strconv.ParseInt(strings.TrimSpace(os.Getenv("PANEL_MASTER_USER_ID")), 10, 64)
	if err != nil {
		return c, fmt.Errorf("PANEL_MASTER_USER_ID must be one positive Telegram user ID from TELEGRAM_ALLOWED_USER_IDS")
	}
	return c, nil
}
func validate(c Config) error {
	if c.TelegramMode != "" && c.TelegramMode != "polling" && c.TelegramMode != "webhook" {
		return fmt.Errorf("TELEGRAM_MODE must be polling or webhook")
	}
	key, err := hex.DecodeString(c.PanelAuthKey)
	if err != nil || len(key) != 32 || repeatedPattern(c.PanelAuthKey) {
		return fmt.Errorf("PANEL_AUTH_KEY must be a random 64-character hexadecimal key; generate with openssl rand -hex 32")
	}
	u, err := url.Parse(c.PublicURL)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
		return fmt.Errorf("PUBLIC_BASE_URL must be an HTTPS origin (HTTP is allowed only for localhost)")
	}
	if c.IsRender && u.Scheme != "https" {
		return fmt.Errorf("Render PUBLIC_BASE_URL must use HTTPS")
	}
	if c.IsRender || c.TelegramMode == "webhook" {
		dbURL, err := url.Parse(c.DatabaseURL)
		if err != nil || dbURL.Host == "" || (dbURL.Scheme != "postgres" && dbURL.Scheme != "postgresql") {
			return fmt.Errorf("cloud DATABASE_URL must be a PostgreSQL URL with TLS enabled")
		}
		params, queryErr := url.ParseQuery(dbURL.RawQuery)
		if queryErr != nil || len(params["sslmode"]) != 1 || params.Get("sslmode") != "verify-full" {
			return fmt.Errorf("cloud DATABASE_URL requires sslmode=verify-full to verify the database server identity")
		}
		// These overrides can change the target or TLS behavior after validation.
		for _, key := range []string{"ssl", "host", "hostaddr", "port", "service", "servicefile"} {
			if _, ok := params[key]; ok {
				return fmt.Errorf("cloud DATABASE_URL contains unsupported connection overrides")
			}
		}
		llmURL, err := url.Parse(c.LLMURL)
		if err != nil || llmURL.Scheme != "https" {
			return fmt.Errorf("cloud LLM_BASE_URL must use HTTPS")
		}
	}
	if c.TelegramMode == "webhook" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return fmt.Errorf("PUBLIC_BASE_URL (or RENDER_EXTERNAL_URL) must be an HTTPS origin")
		}
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`).MatchString(c.WebhookSecret) {
			return fmt.Errorf("TELEGRAM_WEBHOOK_SECRET must contain 32-256 letters, digits, underscores or hyphens")
		}
		if repeatedPattern(c.WebhookSecret) || c.WebhookSecret == c.PanelAuthKey || c.WebhookSecret == c.BotToken || c.WebhookSecret == c.LLMKey {
			return fmt.Errorf("TELEGRAM_WEBHOOK_SECRET must be random and different from other credentials")
		}
		if c.BotToken == "" {
			return fmt.Errorf("webhook mode requires TELEGRAM_BOT_TOKEN")
		}
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.LLMTimeout < time.Second || c.LLMTimeout > 2*time.Minute {
		return fmt.Errorf("LLM_TIMEOUT must be between 1s and 2m")
	}
	if c.HTTPRequestTimeout <= 0 {
		return fmt.Errorf("HTTP_REQUEST_TIMEOUT must be a positive duration")
	}
	for _, id := range c.Allowed {
		if id <= 0 {
			return fmt.Errorf("invalid TELEGRAM_ALLOWED_USER_IDS")
		}
	}
	if len(c.Allowed) == 0 {
		return fmt.Errorf("TELEGRAM_ALLOWED_USER_IDS is required")
	}
	masterAllowed := false
	for _, id := range c.Allowed {
		if id == c.PanelMasterID && id > 0 {
			masterAllowed = true
		}
	}
	if !masterAllowed {
		return fmt.Errorf("PANEL_MASTER_USER_ID must be one positive Telegram user ID from TELEGRAM_ALLOWED_USER_IDS")
	}
	u, e := url.Parse(c.LLMURL)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("invalid LLM_BASE_URL")
	}
	if c.BotToken != "" && (c.LLMKey == "" || c.LLMModel == "") {
		return fmt.Errorf("LLM_API_KEY and LLM_MODEL are required when bot is enabled")
	}
	return nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
