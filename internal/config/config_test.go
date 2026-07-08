package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaultsAdvertiseIPToLoopback(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_ADVERTISE_IP", "")
	t.Setenv("TELESRV_PUBLIC_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdvertiseIP != "127.0.0.1" {
		t.Fatalf("AdvertiseIP = %q, want loopback default", cfg.AdvertiseIP)
	}
	if cfg.PublicBaseURL != "https://telesrv.net" {
		t.Fatalf("PublicBaseURL = %q, want https://telesrv.net", cfg.PublicBaseURL)
	}
}

func TestLoadUsesExplicitAdvertiseIP(t *testing.T) {
	disableDefaultConfigFile(t)
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
	disableDefaultConfigFile(t)
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
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_BUSINESS_AI_PROVIDER", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BusinessAIProvider != "echo" {
		t.Fatalf("BusinessAIProvider = %q, want echo", cfg.BusinessAIProvider)
	}
}

func TestLoadLoginEmailDefaultsDisabled(t *testing.T) {
	disableDefaultConfigFile(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LoginEmailEnable {
		t.Fatal("LoginEmailEnable = true, want false")
	}
	if cfg.LoginEmailRequireSetup {
		t.Fatal("LoginEmailRequireSetup = true, want false")
	}
	if cfg.AuthCodeTTL != 5*time.Minute || cfg.AuthCodeMaxAttempts != 5 || cfg.LoginEmailCodeLength != 6 {
		t.Fatalf("auth/login email defaults = %v/%d/%d", cfg.AuthCodeTTL, cfg.AuthCodeMaxAttempts, cfg.LoginEmailCodeLength)
	}
}

func TestLoadLoginEmailSMTPConfig(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_LOGIN_EMAIL_ENABLE", "true")
	t.Setenv("TELESRV_LOGIN_EMAIL_REQUIRE_SETUP", "true")
	t.Setenv("TELESRV_AUTH_CODE_TTL", "3m")
	t.Setenv("TELESRV_AUTH_CODE_MAX_ATTEMPTS", "4")
	t.Setenv("TELESRV_LOGIN_EMAIL_CODE_LENGTH", "7")
	t.Setenv("TELESRV_SMTP_HOST", "smtp.example.test")
	t.Setenv("TELESRV_SMTP_PORT", "2525")
	t.Setenv("TELESRV_SMTP_USERNAME", "smtp-user")
	t.Setenv("TELESRV_SMTP_PASSWORD", "smtp-pass")
	t.Setenv("TELESRV_SMTP_FROM", "noreply@example.test")
	t.Setenv("TELESRV_SMTP_TLS", "none")
	t.Setenv("TELESRV_SMTP_TIMEOUT", "2s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.LoginEmailEnable || !cfg.LoginEmailRequireSetup {
		t.Fatalf("login email flags = %v/%v, want true/true", cfg.LoginEmailEnable, cfg.LoginEmailRequireSetup)
	}
	if cfg.AuthCodeTTL != 3*time.Minute || cfg.AuthCodeMaxAttempts != 4 || cfg.LoginEmailCodeLength != 7 {
		t.Fatalf("auth/login email config = %v/%d/%d", cfg.AuthCodeTTL, cfg.AuthCodeMaxAttempts, cfg.LoginEmailCodeLength)
	}
	if cfg.SMTPHost != "smtp.example.test" || cfg.SMTPPort != 2525 || cfg.SMTPUsername != "smtp-user" || cfg.SMTPPassword != "smtp-pass" || cfg.SMTPFrom != "noreply@example.test" || cfg.SMTPTLSMode != "none" || cfg.SMTPTimeout != 2*time.Second {
		t.Fatalf("smtp config = %#v", cfg)
	}
}

func TestLoadLoginEmailRequiresSMTPWhenEnabled(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_LOGIN_EMAIL_ENABLE", "true")

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with login email enabled but no SMTP host")
	}
}

func TestLoadKeepsAdminAndRtmpDefaultPortsSeparate(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_ADMIN_UI_ADDR", "")
	t.Setenv("TELESRV_LIVESTREAM_RTMP_ADDR", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdminUIAddr != "127.0.0.1:2600" {
		t.Fatalf("AdminUIAddr = %q, want 127.0.0.1:2600", cfg.AdminUIAddr)
	}
	if cfg.LiveStreamRtmpAddr != ":2400" {
		t.Fatalf("LiveStreamRtmpAddr = %q, want :2400", cfg.LiveStreamRtmpAddr)
	}
	if cfg.AdminUIAddr == "127.0.0.1"+cfg.LiveStreamRtmpAddr {
		t.Fatalf("Admin UI and RTMP defaults conflict on %s", cfg.AdminUIAddr)
	}
}

func TestLoadAIProviders(t *testing.T) {
	disableDefaultConfigFile(t)
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
TELESRV_PUBLIC_BASE_URL=links.example.test/root
TELESRV_PUBLIC_LINK_WEB_ADDR=127.0.0.1:2401
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
	if cfg.PublicLinkWebAddr != "127.0.0.1:2401" {
		t.Fatalf("PublicLinkWebAddr = %q, want 127.0.0.1:2401", cfg.PublicLinkWebAddr)
	}
	if cfg.PublicBaseURL != "https://links.example.test/root" {
		t.Fatalf("PublicBaseURL = %q, want https://links.example.test/root", cfg.PublicBaseURL)
	}
}

func TestLoadNormalizesLocalPublicBaseURL(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_PUBLIC_BASE_URL", "http://127.0.0.1:2401/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicBaseURL != "http://127.0.0.1:2401" {
		t.Fatalf("PublicBaseURL = %q, want http://127.0.0.1:2401", cfg.PublicBaseURL)
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

func disableDefaultConfigFile(t *testing.T) {
	t.Helper()
	t.Setenv("TELESRV_CONFIG", "")
}
