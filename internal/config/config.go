package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	DatabaseURL, Addr, BotToken, LLMURL, LLMKey, LLMModel string
	Allowed                                               []int64
	Location                                              *time.Location
	LLMTimeout                                            time.Duration
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
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Addr:        env("HTTP_ADDR", ":8080"),
		BotToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		LLMURL:      env("LLM_BASE_URL", "https://api.groq.com/openai/v1"),
		LLMKey:      os.Getenv("LLM_API_KEY"),
		LLMModel:    env("LLM_MODEL", "llama-3.3-70b-versatile"),
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
	return c, nil
}
func validate(c Config) error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.LLMTimeout < time.Second || c.LLMTimeout > 2*time.Minute {
		return fmt.Errorf("LLM_TIMEOUT must be between 1s and 2m")
	}
	for _, id := range c.Allowed {
		if id <= 0 {
			return fmt.Errorf("invalid TELEGRAM_ALLOWED_USER_IDS")
		}
	}
	if len(c.Allowed) == 0 {
		return fmt.Errorf("TELEGRAM_ALLOWED_USER_IDS is required")
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
