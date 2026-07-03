package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsAdvertiseIPToLoopback(t *testing.T) {
	t.Setenv("TELESRV_ADVERTISE_IP", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdvertiseIP != "127.0.0.1" {
		t.Fatalf("AdvertiseIP = %q, want loopback default", cfg.AdvertiseIP)
	}
}

func TestLoadUsesExplicitAdvertiseIP(t *testing.T) {
	t.Setenv("TELESRV_ADVERTISE_IP", "203.0.113.10")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdvertiseIP != "203.0.113.10" {
		t.Fatalf("AdvertiseIP = %q, want explicit env", cfg.AdvertiseIP)
	}
}

func TestLoadBusinessAIProvider(t *testing.T) {
	t.Setenv("TELESRV_BUSINESS_AI_PROVIDER", "echo")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BusinessAIProvider != "echo" {
		t.Fatalf("BusinessAIProvider = %q, want echo", cfg.BusinessAIProvider)
	}
}

func TestLoadBusinessAIProviderDefaultsToEcho(t *testing.T) {
	t.Setenv("TELESRV_BUSINESS_AI_PROVIDER", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BusinessAIProvider != "echo" {
		t.Fatalf("BusinessAIProvider = %q, want echo", cfg.BusinessAIProvider)
	}
}

func TestLoadAIProviders(t *testing.T) {
	t.Setenv("TELESRV_AI_PROVIDERS", "local,openai,gemini")
	t.Setenv("TELESRV_AI_OPENAI_API_KEY", "openai-key")
	t.Setenv("TELESRV_AI_OPENAI_MODEL", "gpt-test")
	t.Setenv("TELESRV_AI_GEMINI_API_KEY", "gemini-key")
	t.Setenv("TELESRV_AI_GEMINI_BASE_URL", "https://gemini.example")
	t.Setenv("TELESRV_AI_GEMINI_TEMPERATURE", "0.6")
	t.Setenv("TELESRV_AI_GEMINI_OMIT_TEMPERATURE", "true")
	t.Setenv("TELESRV_AI_GEMINI_THINKING", "disabled")
	t.Setenv("TELESRV_AI_TIMEOUT", "3s")
	t.Setenv("TELESRV_AI_RATE_LIMIT", "7")
	t.Setenv("TELESRV_AI_RATE_WINDOW", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AIProviders) != 3 {
		t.Fatalf("AIProviders len = %d, want 3", len(cfg.AIProviders))
	}
	if cfg.AIProviders[0].Kind != "local" {
		t.Fatalf("AIProviders[0].Kind = %q, want local", cfg.AIProviders[0].Kind)
	}
	if cfg.AIProviders[1].Kind != "openai_responses" || cfg.AIProviders[1].APIKey != "openai-key" || cfg.AIProviders[1].Model != "gpt-test" {
		t.Fatalf("openai provider = %#v", cfg.AIProviders[1])
	}
	if cfg.AIProviders[2].Kind != "gemini" || cfg.AIProviders[2].BaseURL != "https://gemini.example" || cfg.AIProviders[2].Temperature != 0.6 || !cfg.AIProviders[2].OmitTemperature || cfg.AIProviders[2].Thinking != "disabled" {
		t.Fatalf("gemini provider = %#v", cfg.AIProviders[2])
	}
	if cfg.AITimeout != 3*time.Second || cfg.AIRateLimit != 7 || cfg.AIRateWindow != 30*time.Second {
		t.Fatalf("AI timing/rate config = %v/%d/%v", cfg.AITimeout, cfg.AIRateLimit, cfg.AIRateWindow)
	}
}

func TestLoadReadsEnvStyleConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telesrv.env")
	writeConfigFile(t, path, `
TELESRV_MAPBOX_TOKEN="file-token"
TELESRV_POSTGRES_MAX_CONNS=77
TELESRV_WEBSOCKET_ALLOWED_ORIGINS=https://one.example, https://two.example
TELESRV_CALL_RING_TIMEOUT=2m
TELESRV_STICKER_WEB_ADDR=127.0.0.1:2401
TELESRV_STICKER_WEB_PUBLIC_URL=https://packs.example.test
`)
	t.Setenv("TELESRV_CONFIG", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MapboxToken != "file-token" {
		t.Fatalf("MapboxToken = %q, want file-token", cfg.MapboxToken)
	}
	if cfg.PostgresMaxConns != 77 {
		t.Fatalf("PostgresMaxConns = %d, want 77", cfg.PostgresMaxConns)
	}
	if got, want := cfg.WebSocketAllowedOrigins, []string{"https://one.example", "https://two.example"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("WebSocketAllowedOrigins = %#v, want %#v", got, want)
	}
	if cfg.CallRingTimeout != 2*time.Minute {
		t.Fatalf("CallRingTimeout = %v, want 2m", cfg.CallRingTimeout)
	}
	if cfg.StickerWebAddr != "127.0.0.1:2401" {
		t.Fatalf("StickerWebAddr = %q, want 127.0.0.1:2401", cfg.StickerWebAddr)
	}
	if cfg.StickerWebPublicURL != "https://packs.example.test" {
		t.Fatalf("StickerWebPublicURL = %q, want https://packs.example.test", cfg.StickerWebPublicURL)
	}
}

func TestLoadEnvironmentOverridesConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telesrv.env")
	writeConfigFile(t, path, `TELESRV_MAPBOX_TOKEN=file-token`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TELESRV_MAPBOX_TOKEN", "env-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MapboxToken != "env-token" {
		t.Fatalf("MapboxToken = %q, want env-token", cfg.MapboxToken)
	}
}

func TestLoadExplicitMissingConfigFileErrors(t *testing.T) {
	t.Setenv("TELESRV_CONFIG", filepath.Join(t.TempDir(), "missing.env"))

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with explicit missing config file, want error")
	}
}

func TestLoadRejectsNonTelesrvConfigKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telesrv.env")
	writeConfigFile(t, path, `MAPBOX_TOKEN=file-token`)
	t.Setenv("TELESRV_CONFIG", path)

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with unsupported config key, want error")
	}
}

func writeConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
}
