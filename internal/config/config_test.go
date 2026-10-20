package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "42,42,99")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("APP_TIMEZONE", "Europe/Moscow")
	t.Setenv("LLM_TIMEOUT", "30s")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "")
	t.Setenv("LLM_BASE_URL", "https://api.groq.com/openai/v1")
	c, e := Load()
	if e != nil || len(c.Allowed) != 2 || c.Allowed[0] != 42 || c.Allowed[1] != 99 || c.Location == nil || c.Location.String() != "Europe/Moscow" || c.LLMTimeout != 30*time.Second || c.HTTPRequestTimeout != 5*time.Second {
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

func TestLoadInvalidEnvironment(t *testing.T) {
	for _, test := range []struct{ key, value, message string }{
		{"DATABASE_URL", "", "DATABASE_URL is required"},
		{"APP_TIMEZONE", "invalid/timezone", "APP_TIMEZONE:"},
		{"LLM_TIMEOUT", "invalid", "LLM_TIMEOUT must be between 1s and 2m"},
		{"LLM_TIMEOUT", "500ms", "LLM_TIMEOUT must be between 1s and 2m"},
		{"LLM_TIMEOUT", "3m", "LLM_TIMEOUT must be between 1s and 2m"},
		{"HTTP_REQUEST_TIMEOUT", "invalid", "HTTP_REQUEST_TIMEOUT must be a positive duration"},
		{"HTTP_REQUEST_TIMEOUT", "0s", "HTTP_REQUEST_TIMEOUT must be a positive duration"},
		{"HTTP_REQUEST_TIMEOUT", "-1s", "HTTP_REQUEST_TIMEOUT must be a positive duration"},
		{"TELEGRAM_ALLOWED_USER_IDS", "42,invalid", "invalid TELEGRAM_ALLOWED_USER_IDS"},
		{"TELEGRAM_ALLOWED_USER_IDS", "0", "invalid TELEGRAM_ALLOWED_USER_IDS"},
		{"TELEGRAM_ALLOWED_USER_IDS", "-1", "invalid TELEGRAM_ALLOWED_USER_IDS"},
		{"TELEGRAM_ALLOWED_USER_IDS", " , ", "TELEGRAM_ALLOWED_USER_IDS is required"},
		{"LLM_BASE_URL", "file:///tmp/model", "invalid LLM_BASE_URL"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://example")
			t.Setenv("APP_TIMEZONE", "UTC")
			t.Setenv("LLM_TIMEOUT", "30s")
			t.Setenv("HTTP_REQUEST_TIMEOUT", "")
			t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "42")
			t.Setenv("LLM_BASE_URL", "https://example.com/v1")
			t.Setenv("TELEGRAM_BOT_TOKEN", "")
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("expected %q, got %v", test.message, err)
			}
		})
	}
}

func TestValidateUsesInitializedConfig(t *testing.T) {
	t.Setenv("APP_TIMEZONE", "invalid/timezone")
	t.Setenv("LLM_TIMEOUT", "invalid")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "invalid")
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "invalid")
	c := Config{
		DatabaseURL:        "postgres://example",
		LLMURL:             "https://example.com/v1",
		Location:           time.UTC,
		LLMTimeout:         30 * time.Second,
		HTTPRequestTimeout: 5 * time.Second,
		Allowed:            []int64{42},
	}
	if err := validate(c); err != nil {
		t.Fatalf("validation read environment instead of config: %v", err)
	}
}

func TestLoadCustomHTTPRequestTimeout(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "42")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("APP_TIMEZONE", "UTC")
	t.Setenv("LLM_TIMEOUT", "30s")
	t.Setenv("LLM_BASE_URL", "https://example.com/v1")
	t.Setenv("HTTP_REQUEST_TIMEOUT", "750ms")
	c, err := Load()
	if err != nil || c.HTTPRequestTimeout != 750*time.Millisecond {
		t.Fatalf("HTTP timeout: %v, error: %v", c.HTTPRequestTimeout, err)
	}
}
