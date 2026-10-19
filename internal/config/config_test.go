package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "42,42,99")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("APP_TIMEZONE", "Europe/Moscow")
	t.Setenv("LLM_TIMEOUT", "30s")
	t.Setenv("LLM_BASE_URL", "https://api.groq.com/openai/v1")
	c, e := Load()
	if e != nil || len(c.Allowed) != 2 {
		t.Fatalf("config: %+v %v", c, e)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("LLM_API_KEY", "")
	if _, e = Load(); e == nil {
		t.Fatal("missing key accepted")
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "-1")
	if _, e = Load(); e == nil {
		t.Fatal("negative user accepted")
	}
}
