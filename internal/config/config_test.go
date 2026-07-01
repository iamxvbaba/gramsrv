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
	t.Setenv("TELESRV_ADVERTISE_IP", "192.0.2.10")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdvertiseIP != "192.0.2.10" {
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

func TestLoadProductionBackpressureDefaults(t *testing.T) {
	t.Setenv("TELESRV_CONFIG", "")
	t.Setenv("TELESRV_OUTBOX_BATCH", "")
	t.Setenv("TELESRV_OUTBOX_INTERVAL", "")
	t.Setenv("TELESRV_OUTBOX_WORKERS", "")
	t.Setenv("TELESRV_CATCHUP_RATE_LIMIT", "")
	t.Setenv("TELESRV_CATCHUP_RATE_WINDOW", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OutboxWorkers != 1 {
		t.Fatalf("OutboxWorkers = %d, want 1 (single worker preserves per-user pts order)", cfg.OutboxWorkers)
	}
	if cfg.OutboxBatch != 200 {
		t.Fatalf("OutboxBatch = %d, want 200", cfg.OutboxBatch)
	}
	if cfg.OutboxInterval != 50*time.Millisecond {
		t.Fatalf("OutboxInterval = %v, want 50ms", cfg.OutboxInterval)
	}
	if cfg.CatchupRateLimit != 120 {
		t.Fatalf("CatchupRateLimit = %d, want 120", cfg.CatchupRateLimit)
	}
	if cfg.CatchupRateWindow != time.Minute {
		t.Fatalf("CatchupRateWindow = %v, want 1m", cfg.CatchupRateWindow)
	}
}

func TestLoadStickerSeedDefaultsCoverOfficialCatalog(t *testing.T) {
	t.Setenv("TELESRV_STICKER_SEED_DIR", "")
	t.Setenv("TELESRV_STICKER_SEED_MAX_SETS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StickerSeedDir != "data/sticker-seed" {
		t.Fatalf("StickerSeedDir = %q, want data/sticker-seed", cfg.StickerSeedDir)
	}
	if cfg.StickerSeedMaxSets != 300 {
		t.Fatalf("StickerSeedMaxSets = %d, want 300", cfg.StickerSeedMaxSets)
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
