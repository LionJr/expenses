package config

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const testAuthKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestLoad(t *testing.T) {
	t.Setenv("PANEL_MASTER_USER_ID", "42")
	t.Setenv("PANEL_AUTH_KEY", testAuthKey)
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
		{"PANEL_MASTER_USER_ID", "", "PANEL_MASTER_USER_ID"},
		{"PANEL_MASTER_USER_ID", "42,99", "PANEL_MASTER_USER_ID"},
		{"PANEL_MASTER_USER_ID", "0", "PANEL_MASTER_USER_ID"},
		{"PANEL_MASTER_USER_ID", "-1", "PANEL_MASTER_USER_ID"},
		{"PANEL_MASTER_USER_ID", "99", "PANEL_MASTER_USER_ID"},
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
			t.Setenv("PANEL_MASTER_USER_ID", "42")
			t.Setenv("PANEL_AUTH_KEY", testAuthKey)
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
		PanelAuthKey:       testAuthKey,
		PublicURL:          "http://localhost:8080",
		PanelMasterID:      42,
	}
	if err := validate(c); err != nil {
		t.Fatalf("validation read environment instead of config: %v", err)
	}
}

func TestLoadCustomHTTPRequestTimeout(t *testing.T) {
	t.Setenv("PANEL_MASTER_USER_ID", "42")
	t.Setenv("PANEL_AUTH_KEY", testAuthKey)
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

func TestRenderWebhookConfiguration(t *testing.T) {
	t.Setenv("PANEL_MASTER_USER_ID", "42")
	t.Setenv("PANEL_AUTH_KEY", testAuthKey)
	t.Setenv("DATABASE_URL", "postgres://example/expenses?sslmode=verify-full")
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "42")
	t.Setenv("TELEGRAM_BOT_TOKEN", "token")
	t.Setenv("LLM_API_KEY", "key")
	t.Setenv("LLM_BASE_URL", "https://example.com/v1")
	t.Setenv("TELEGRAM_MODE", "webhook")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("PUBLIC_BASE_URL", "")
	t.Setenv("RENDER_EXTERNAL_URL", "https://family.onrender.com")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("PORT", "10000")
	c, err := Load()
	if err != nil || c.Addr != ":10000" || c.PublicURL != "https://family.onrender.com" {
		t.Fatalf("Render config: %+v, %v", c, err)
	}
	for _, test := range []struct{ key, value string }{
		{"TELEGRAM_MODE", "invalid"},
		{"TELEGRAM_WEBHOOK_SECRET", ""},
		{"TELEGRAM_WEBHOOK_SECRET", "base64+with/slash=012345678901234567890"},
		{"PANEL_AUTH_KEY", ""},
		{"PANEL_AUTH_KEY", "invalid"},
		{"PANEL_AUTH_KEY", strings.Repeat("a", 64)},
		{"TELEGRAM_BOT_TOKEN", ""},
		{"PUBLIC_BASE_URL", "http://family.example"},
		{"PUBLIC_BASE_URL", "https://user:pass@family.example"},
		{"PUBLIC_BASE_URL", "https://family.example/path"},
		{"PUBLIC_BASE_URL", "https://family.example?key=value"},
		{"DATABASE_URL", "postgres://example/expenses?sslmode=disable"},
		{"DATABASE_URL", "postgres://example/expenses?sslmode=require"},
		{"DATABASE_URL", "postgres://example/expenses?sslmode=verify-ca"},
		{"DATABASE_URL", "postgres://example/expenses?sslmode=verify-full&sslmode=disable"},
		{"DATABASE_URL", "postgres://example/expenses?sslmode=verify-full&host=other.example"},
		{"DATABASE_URL", "postgres://example/expenses"},
		{"LLM_BASE_URL", "http://example.com/v1"},
		{"TELEGRAM_WEBHOOK_SECRET", strings.Repeat("a", 64)},
		{"TELEGRAM_WEBHOOK_SECRET", testAuthKey},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid %s accepted", test.key)
			}
		})
	}
}

func TestRenderRequiresPanelAuthKeyWithoutBot(t *testing.T) {
	t.Setenv("PANEL_MASTER_USER_ID", "42")
	t.Setenv("DATABASE_URL", "postgres://example/expenses?sslmode=verify-full")
	t.Setenv("TELEGRAM_ALLOWED_USER_IDS", "42")
	t.Setenv("LLM_BASE_URL", "https://example.com/v1")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_MODE", "polling")
	t.Setenv("RENDER", "true")
	t.Setenv("PANEL_AUTH_KEY", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PANEL_AUTH_KEY") {
		t.Fatalf("public unauthenticated deployment accepted: %v", err)
	}
}

func TestCloudDatabaseTLSVerifiesCertificateAndHostname(t *testing.T) {
	c := Config{DatabaseURL: "postgres://test:synthetic@db.example/expenses?sslmode=verify-full", LLMURL: "https://api.example/v1", Location: time.UTC, LLMTimeout: 30 * time.Second, HTTPRequestTimeout: 5 * time.Second, Allowed: []int64{42}, PanelMasterID: 42, PanelAuthKey: testAuthKey, PublicURL: "https://family.example", IsRender: true}
	if err := validate(c); err != nil {
		t.Fatal(err)
	}
	parsed, err := pgconn.ParseConfig(c.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.TLSConfig == nil || parsed.TLSConfig.InsecureSkipVerify || parsed.TLSConfig.ServerName != "db.example" {
		t.Fatal("database server identity not verified")
	}
	for _, query := range []string{"sslmode=require", "sslmode=verify-ca", "sslmode=verify-full&sslmode=disable", "sslmode=verify-full&ssl=false", "sslmode=verify-full&service=other"} {
		c.DatabaseURL = "postgres://test:synthetic@db.example/expenses?" + query
		if err := validate(c); err == nil {
			t.Fatalf("insecure override accepted: %s", query)
		}
	}
}
