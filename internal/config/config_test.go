package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"telesrv/internal/domain"
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
	if cfg.PublicAppScheme != "telesrv" {
		t.Fatalf("PublicAppScheme = %q, want telesrv", cfg.PublicAppScheme)
	}
	if cfg.PublicAppLinkBase != "" {
		t.Fatalf("PublicAppLinkBase = %q, want disabled", cfg.PublicAppLinkBase)
	}
	if cfg.PublicWebBaseURL != "https://weba.telesrv.net" {
		t.Fatalf("PublicWebBaseURL = %q, want https://weba.telesrv.net", cfg.PublicWebBaseURL)
	}
	if cfg.PublicAppName != "telesrv" {
		t.Fatalf("PublicAppName = %q, want telesrv", cfg.PublicAppName)
	}
	if cfg.CallRegistryMaxEntries != 10_000 {
		t.Fatalf("CallRegistryMaxEntries = %d, want 10000", cfg.CallRegistryMaxEntries)
	}
	if cfg.PremiumPromoSeedDir != "data/premium-promo" {
		t.Fatalf("PremiumPromoSeedDir = %q, want data/premium-promo", cfg.PremiumPromoSeedDir)
	}
}

func TestLoadPremiumPromoSeedDirOverride(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_PREMIUM_PROMO_SEED_DIR", `D:\seed\premium-promo`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PremiumPromoSeedDir != `D:\seed\premium-promo` {
		t.Fatalf("PremiumPromoSeedDir = %q", cfg.PremiumPromoSeedDir)
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

func TestLoadStrictDCCheck(t *testing.T) {
	t.Run("defaults off", func(t *testing.T) {
		disableDefaultConfigFile(t)
		t.Setenv("TELESRV_STRICT_DC_CHECK", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.StrictDCCheck {
			t.Fatal("StrictDCCheck = true, want default false")
		}
	})

	t.Run("explicitly enabled", func(t *testing.T) {
		disableDefaultConfigFile(t)
		t.Setenv("TELESRV_STRICT_DC_CHECK", "true")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.StrictDCCheck {
			t.Fatal("StrictDCCheck = false, want true")
		}
	})
}

func TestLoadMTProtoAdmissionAndRPCBudgets(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_MTPROTO_MAX_CONNECTIONS", "12345")
	t.Setenv("TELESRV_MTPROTO_MAX_CONNECTIONS_PER_IP", "234")
	t.Setenv("TELESRV_MTPROTO_MAX_CONCURRENT_HANDSHAKES", "45")
	t.Setenv("TELESRV_MTPROTO_RPC_MAX_INFLIGHT", "7")
	t.Setenv("TELESRV_MTPROTO_RPC_QUEUE_SIZE", "19")
	t.Setenv("TELESRV_MTPROTO_RPC_TIMEOUT", "9s")
	t.Setenv("TELESRV_MTPROTO_RPC_GLOBAL_WORKERS", "33")
	t.Setenv("TELESRV_MTPROTO_RPC_GLOBAL_MAX_TASKS", "444")
	t.Setenv("TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES", "555555")
	t.Setenv("TELESRV_MTPROTO_RPC_RESULT_CACHE_MAX_ENTRIES", "555")
	t.Setenv("TELESRV_MTPROTO_RPC_RESULT_CACHE_MAX_BYTES", "70000000")
	t.Setenv("TELESRV_MTPROTO_RPC_RESULT_CACHE_AUTH_MAX_ENTRIES", "444")
	t.Setenv("TELESRV_MTPROTO_RPC_RESULT_CACHE_AUTH_MAX_BYTES", "40000000")
	t.Setenv("TELESRV_MTPROTO_RPC_RESULT_CACHE_SESSION_MAX_ENTRIES", "333")
	t.Setenv("TELESRV_MTPROTO_RPC_RESULT_CACHE_SESSION_MAX_BYTES", "20000000")
	t.Setenv("TELESRV_MTPROTO_RPC_RESULT_PENDING_PER_AUTH", "222")
	t.Setenv("TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES", "777777")
	t.Setenv("TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE", "88")
	t.Setenv("TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE", "22")
	t.Setenv("TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES", "888888")
	t.Setenv("TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES", "999999")
	t.Setenv("TELESRV_TEMP_KEY_CACHE_MAX_ENTRIES", "666")
	t.Setenv("TELESRV_TEMP_KEY_CACHE_TTL", "17m")
	t.Setenv("TELESRV_ORPHAN_AUTH_KEY_RETENTION", "36h")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MTProtoMaxConnections != 12345 || cfg.MTProtoMaxConnectionsPerIP != 234 || cfg.MTProtoMaxConcurrentHandshakes != 45 {
		t.Fatalf("admission config = %d/%d/%d", cfg.MTProtoMaxConnections, cfg.MTProtoMaxConnectionsPerIP, cfg.MTProtoMaxConcurrentHandshakes)
	}
	if cfg.MTProtoRPCMaxInflight != 7 || cfg.MTProtoRPCQueueSize != 19 || cfg.MTProtoRPCTimeout != 9*time.Second ||
		cfg.MTProtoRPCGlobalWorkers != 33 || cfg.MTProtoRPCGlobalMaxTasks != 444 || cfg.MTProtoRPCGlobalMaxBytes != 555555 {
		t.Fatalf("rpc budget config = %d/%d/%v/%d/%d/%d", cfg.MTProtoRPCMaxInflight, cfg.MTProtoRPCQueueSize, cfg.MTProtoRPCTimeout, cfg.MTProtoRPCGlobalWorkers, cfg.MTProtoRPCGlobalMaxTasks, cfg.MTProtoRPCGlobalMaxBytes)
	}
	if cfg.MTProtoRPCResultCacheMaxEntries != 555 || cfg.MTProtoRPCResultCacheMaxBytes != 70000000 ||
		cfg.MTProtoRPCResultCacheAuthMaxEntries != 444 || cfg.MTProtoRPCResultCacheAuthMaxBytes != 40000000 ||
		cfg.MTProtoRPCResultCacheSessionMaxEntries != 333 || cfg.MTProtoRPCResultCacheSessionMaxBytes != 20000000 ||
		cfg.MTProtoRPCResultPendingPerAuth != 222 {
		t.Fatalf("rpc result cache config = global:%d/%d auth:%d/%d session:%d/%d pending/auth:%d",
			cfg.MTProtoRPCResultCacheMaxEntries, cfg.MTProtoRPCResultCacheMaxBytes,
			cfg.MTProtoRPCResultCacheAuthMaxEntries, cfg.MTProtoRPCResultCacheAuthMaxBytes,
			cfg.MTProtoRPCResultCacheSessionMaxEntries, cfg.MTProtoRPCResultCacheSessionMaxBytes,
			cfg.MTProtoRPCResultPendingPerAuth)
	}
	if cfg.MTProtoInboundFrameGlobalMaxBytes != 777777 {
		t.Fatalf("inbound frame budget config = %d", cfg.MTProtoInboundFrameGlobalMaxBytes)
	}
	if cfg.MTProtoOutboundQueueSize != 88 || cfg.MTProtoOutboundControlQueueSize != 22 || cfg.MTProtoOutboundTrackedGlobalMaxBytes != 888888 || cfg.MTProtoOutboundWriteGlobalMaxBytes != 999999 {
		t.Fatalf("outbound config = %d/%d/%d/%d", cfg.MTProtoOutboundQueueSize, cfg.MTProtoOutboundControlQueueSize, cfg.MTProtoOutboundTrackedGlobalMaxBytes, cfg.MTProtoOutboundWriteGlobalMaxBytes)
	}
	if cfg.TempKeyResolveCacheMaxEntries != 666 || cfg.TempKeyResolveCacheTTL != 17*time.Minute || cfg.OrphanAuthKeyRetention != 36*time.Hour {
		t.Fatalf("auth key resource config = %d/%v/%v", cfg.TempKeyResolveCacheMaxEntries, cfg.TempKeyResolveCacheTTL, cfg.OrphanAuthKeyRetention)
	}
}

func TestLoadRPCResultFairBudgetDefaults(t *testing.T) {
	disableDefaultConfigFile(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MTProtoRPCResultCacheMaxEntries != 1<<18 || cfg.MTProtoRPCResultCacheMaxBytes != 64<<20 ||
		cfg.MTProtoRPCResultCacheAuthMaxEntries != 1<<15 || cfg.MTProtoRPCResultCacheAuthMaxBytes != 32<<20 ||
		cfg.MTProtoRPCResultCacheSessionMaxEntries != 1<<14 || cfg.MTProtoRPCResultCacheSessionMaxBytes != 16<<20 ||
		cfg.MTProtoRPCResultPendingPerAuth != 1<<11 {
		t.Fatalf("rpc_result fair defaults = global:%d/%d auth:%d/%d session:%d/%d pending/auth:%d",
			cfg.MTProtoRPCResultCacheMaxEntries, cfg.MTProtoRPCResultCacheMaxBytes,
			cfg.MTProtoRPCResultCacheAuthMaxEntries, cfg.MTProtoRPCResultCacheAuthMaxBytes,
			cfg.MTProtoRPCResultCacheSessionMaxEntries, cfg.MTProtoRPCResultCacheSessionMaxBytes,
			cfg.MTProtoRPCResultPendingPerAuth)
	}
}

func TestLoadRejectsInvalidRPCResultFairBudgets(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "entry hierarchy", key: "TELESRV_MTPROTO_RPC_RESULT_CACHE_MAX_ENTRIES", value: "1024"},
		{name: "byte below outbound body", key: "TELESRV_MTPROTO_RPC_RESULT_CACHE_SESSION_MAX_BYTES", value: "16700000"},
		{name: "byte hierarchy", key: "TELESRV_MTPROTO_RPC_RESULT_CACHE_AUTH_MAX_BYTES", value: "70000000"},
		{name: "pending hierarchy", key: "TELESRV_MTPROTO_RPC_RESULT_PENDING_PER_AUTH", value: "9000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoadOutboxPoisonPolicy(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_OUTBOX_POISON_RETENTION", "2m")
	t.Setenv("TELESRV_OUTBOX_POISON_CLEANUP_INTERVAL", "7s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OutboxPoisonRetention != 2*time.Minute || cfg.OutboxPoisonCleanupInterval != 7*time.Second {
		t.Fatalf("outbox poison policy = %v/%v, want 2m/7s", cfg.OutboxPoisonRetention, cfg.OutboxPoisonCleanupInterval)
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
	if cfg.AuthCodeTTL != 5*time.Minute || cfg.AuthCodeMaxAttempts != 5 || cfg.LoginEmailCodeLength != 6 ||
		cfg.PhoneCodeLength != 5 || cfg.PhoneCodeDeliveryProvider != "development" || cfg.EmailCodeDeliveryProvider != "smtp" ||
		cfg.AuthCodePhoneRateLimit != 5 || cfg.AuthCodeAuthKeyRateLimit != 20 || cfg.AuthCodeRateWindow != 10*time.Minute {
		t.Fatalf("auth/login email defaults = ttl=%v attempts=%d length=%d phone_limit=%d key_limit=%d window=%v",
			cfg.AuthCodeTTL, cfg.AuthCodeMaxAttempts, cfg.LoginEmailCodeLength,
			cfg.AuthCodePhoneRateLimit, cfg.AuthCodeAuthKeyRateLimit, cfg.AuthCodeRateWindow)
	}
}

func TestLoadLoginEmailSMTPConfig(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_LOGIN_EMAIL_ENABLE", "true")
	t.Setenv("TELESRV_LOGIN_EMAIL_REQUIRE_SETUP", "true")
	t.Setenv("TELESRV_AUTH_CODE_TTL", "3m")
	t.Setenv("TELESRV_AUTH_CODE_MAX_ATTEMPTS", "4")
	t.Setenv("TELESRV_AUTH_CODE_PHONE_RATE_LIMIT", "3")
	t.Setenv("TELESRV_AUTH_CODE_AUTH_KEY_RATE_LIMIT", "9")
	t.Setenv("TELESRV_AUTH_CODE_RATE_WINDOW", "2m")
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
	if cfg.AuthCodeTTL != 3*time.Minute || cfg.AuthCodeMaxAttempts != 4 || cfg.LoginEmailCodeLength != 7 ||
		cfg.AuthCodePhoneRateLimit != 3 || cfg.AuthCodeAuthKeyRateLimit != 9 || cfg.AuthCodeRateWindow != 2*time.Minute {
		t.Fatalf("auth/login email config = ttl=%v attempts=%d length=%d phone_limit=%d key_limit=%d window=%v",
			cfg.AuthCodeTTL, cfg.AuthCodeMaxAttempts, cfg.LoginEmailCodeLength,
			cfg.AuthCodePhoneRateLimit, cfg.AuthCodeAuthKeyRateLimit, cfg.AuthCodeRateWindow)
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

func TestLoadLoginEmailWebhookDoesNotRequireSMTP(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_LOGIN_EMAIL_ENABLE", "true")
	t.Setenv("TELESRV_EMAIL_CODE_DELIVERY_PROVIDER", "webhook")
	t.Setenv("TELESRV_OTP_WEBHOOK_URL", "https://otp.example.test/v1/deliveries")
	t.Setenv("TELESRV_OTP_WEBHOOK_SECRET", "test-secret")
	t.Setenv("TELESRV_OTP_WEBHOOK_TIMEOUT", "3s")
	t.Setenv("TELESRV_SMTP_HOST", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EmailCodeDeliveryProvider != "webhook" || cfg.OTPWebhookURL != "https://otp.example.test/v1/deliveries" ||
		cfg.OTPWebhookSecret != "test-secret" || cfg.OTPWebhookTimeout != 3*time.Second {
		t.Fatalf("webhook config = %#v", cfg)
	}
}

func TestLoadPhoneWebhookRequiresValidURL(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_PHONE_CODE_DELIVERY_PROVIDER", "webhook")
	t.Setenv("TELESRV_OTP_WEBHOOK_URL", "relative/path")

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with relative OTP webhook URL")
	}
}

func TestLoadPhoneWebhookConfig(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_PHONE_CODE_DELIVERY_PROVIDER", "webhook")
	t.Setenv("TELESRV_PHONE_CODE_LENGTH", "7")
	t.Setenv("TELESRV_OTP_WEBHOOK_URL", "http://127.0.0.1:8080/otp")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PhoneCodeDeliveryProvider != "webhook" || cfg.PhoneCodeLength != 7 {
		t.Fatalf("phone webhook config = %#v", cfg)
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

func TestLoadTranslationConfig(t *testing.T) {
	t.Setenv("TELESRV_CONFIG", "")
	t.Setenv("TELESRV_TRANSLATION_ENABLED", "true")
	t.Setenv("TELESRV_TRANSLATION_PROVIDERS", "openai,gemini")
	t.Setenv("TELESRV_TRANSLATION_TIMEOUT", "9s")
	t.Setenv("TELESRV_TRANSLATION_RATE_LIMIT", "17")
	t.Setenv("TELESRV_TRANSLATION_RATE_WINDOW", "2m")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.TranslationEnabled || len(cfg.TranslationProviders) != 2 || cfg.TranslationProviders[0] != "openai" || cfg.TranslationProviders[1] != "gemini" {
		t.Fatalf("translation providers = %#v", cfg.TranslationProviders)
	}
	if cfg.TranslationTimeout != 9*time.Second || cfg.TranslationRateLimit != 17 || cfg.TranslationRateWindow != 2*time.Minute {
		t.Fatalf("translation limits = %v/%d/%v", cfg.TranslationTimeout, cfg.TranslationRateLimit, cfg.TranslationRateWindow)
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
TELESRV_PUBLIC_APP_SCHEME=example-chat
TELESRV_PUBLIC_APP_LINK_BASE=OWPG://Tenant.Example.Test/
TELESRV_PUBLIC_WEB_BASE_URL=web.example.test/client
TELESRV_PUBLIC_APP_NAME=Example Chat
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
	if cfg.PublicAppScheme != "example-chat" {
		t.Fatalf("PublicAppScheme = %q, want example-chat", cfg.PublicAppScheme)
	}
	if cfg.PublicAppLinkBase != "owpg://tenant.example.test" {
		t.Fatalf("PublicAppLinkBase = %q, want owpg://tenant.example.test", cfg.PublicAppLinkBase)
	}
	if cfg.PublicWebBaseURL != "https://web.example.test/client" {
		t.Fatalf("PublicWebBaseURL = %q, want https://web.example.test/client", cfg.PublicWebBaseURL)
	}
	if cfg.PublicAppName != "Example Chat" {
		t.Fatalf("PublicAppName = %q, want Example Chat", cfg.PublicAppName)
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

func TestLoadTelegramLoginConfig(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_PUBLIC_LINK_WEB_ADDR", "127.0.0.1:2401")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_ENABLE", "true")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_ISSUER", "http://192.0.2.25:2401/")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP", "true")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_SIGNING_KEYS_FILE", "secrets/signing.json")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_CODE_KEYS_FILE", "secrets/codes.json")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_SECRET_PEPPER_FILE", "secrets/pepper")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_REQUEST_TTL", "7m")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_CODE_TTL", "90s")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_ID_TOKEN_TTL", "45m")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_TRUSTED_PROXY_CIDRS", "127.0.0.0/8,10.0.0.0/8")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_RETENTION", "48h")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_SWEEP_INTERVAL", "30s")
	t.Setenv("TELESRV_TELEGRAM_LOGIN_SWEEP_BATCH", "73")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.TelegramLoginEnabled || cfg.TelegramLoginIssuer != "http://192.0.2.25:2401" || !cfg.TelegramLoginAllowHTTP {
		t.Fatalf("telegram login endpoint config = enabled:%v issuer:%q allow_http:%v", cfg.TelegramLoginEnabled, cfg.TelegramLoginIssuer, cfg.TelegramLoginAllowHTTP)
	}
	if cfg.TelegramLoginSigningKeysFile != "secrets/signing.json" || cfg.TelegramLoginCodeKeysFile != "secrets/codes.json" || cfg.TelegramLoginSecretPepperFile != "secrets/pepper" {
		t.Fatalf("telegram login secret files = %q / %q / %q", cfg.TelegramLoginSigningKeysFile, cfg.TelegramLoginCodeKeysFile, cfg.TelegramLoginSecretPepperFile)
	}
	if cfg.TelegramLoginRequestTTL != 7*time.Minute || cfg.TelegramLoginCodeTTL != 90*time.Second || cfg.TelegramLoginIDTokenTTL != 45*time.Minute ||
		cfg.TelegramLoginRetention != 48*time.Hour || cfg.TelegramLoginSweepInterval != 30*time.Second || cfg.TelegramLoginSweepBatch != 73 {
		t.Fatalf("telegram login durations/batch = %v / %v / %v / %v / %v / %d", cfg.TelegramLoginRequestTTL, cfg.TelegramLoginCodeTTL,
			cfg.TelegramLoginIDTokenTTL, cfg.TelegramLoginRetention, cfg.TelegramLoginSweepInterval, cfg.TelegramLoginSweepBatch)
	}
	if len(cfg.TelegramLoginTrustedProxyCIDRs) != 2 || cfg.TelegramLoginTrustedProxyCIDRs[1] != "10.0.0.0/8" {
		t.Fatalf("trusted proxy CIDRs = %#v", cfg.TelegramLoginTrustedProxyCIDRs)
	}
}

func TestValidateTelegramLoginConfigRejectsUnsafeOrUnboundedSettings(t *testing.T) {
	valid := Config{
		TelegramLoginEnabled: true, PublicLinkWebAddr: "127.0.0.1:2401", TelegramLoginIssuer: "https://login.example.test",
		TelegramLoginSigningKeysFile: "signing.json", TelegramLoginCodeKeysFile: "codes.json", TelegramLoginSecretPepperFile: "pepper",
		TelegramLoginRequestTTL: 5 * time.Minute, TelegramLoginCodeTTL: 2 * time.Minute, TelegramLoginIDTokenTTL: time.Hour,
		TelegramLoginRetention: 7 * 24 * time.Hour, TelegramLoginSweepInterval: 5 * time.Minute, TelegramLoginSweepBatch: 500,
	}
	if err := validateTelegramLoginConfig(valid); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "missing listener", mutate: func(c *Config) { c.PublicLinkWebAddr = "" }},
		{name: "issuer path", mutate: func(c *Config) { c.TelegramLoginIssuer = "https://login.example.test/oauth" }},
		{name: "http disabled", mutate: func(c *Config) { c.TelegramLoginIssuer = "http://192.0.2.25:2401" }},
		{name: "missing key file", mutate: func(c *Config) { c.TelegramLoginSigningKeysFile = "" }},
		{name: "request ttl too long", mutate: func(c *Config) { c.TelegramLoginRequestTTL = 16 * time.Minute }},
		{name: "code ttl too short", mutate: func(c *Config) { c.TelegramLoginCodeTTL = 29 * time.Second }},
		{name: "id token ttl too long", mutate: func(c *Config) { c.TelegramLoginIDTokenTTL = 25 * time.Hour }},
		{name: "retention too short", mutate: func(c *Config) { c.TelegramLoginRetention = 59 * time.Minute }},
		{name: "sweep unbounded", mutate: func(c *Config) { c.TelegramLoginSweepBatch = 1001 }},
		{name: "invalid proxy CIDR", mutate: func(c *Config) { c.TelegramLoginTrustedProxyCIDRs = []string{"10.0.0.0/33"} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			if err := validateTelegramLoginConfig(cfg); err == nil {
				t.Fatal("unsafe Telegram Login config was accepted")
			}
		})
	}
}

func TestValidateTelegramLoginConfigAcceptsHTTPHostAndIPWhenEnabled(t *testing.T) {
	valid := Config{
		TelegramLoginEnabled: true, TelegramLoginAllowHTTP: true, PublicLinkWebAddr: "127.0.0.1:2401",
		TelegramLoginSigningKeysFile: "signing.json", TelegramLoginCodeKeysFile: "codes.json", TelegramLoginSecretPepperFile: "pepper",
		TelegramLoginRequestTTL: 5 * time.Minute, TelegramLoginCodeTTL: 2 * time.Minute, TelegramLoginIDTokenTTL: time.Hour,
		TelegramLoginRetention: 7 * 24 * time.Hour, TelegramLoginSweepInterval: 5 * time.Minute, TelegramLoginSweepBatch: 500,
	}
	for _, issuer := range []string{"http://login.example.test:3000", "http://192.0.2.25:2401", "http://[2001:db8::25]:2401"} {
		cfg := valid
		cfg.TelegramLoginIssuer = issuer
		if err := validateTelegramLoginConfig(cfg); err != nil {
			t.Fatalf("issuer %q was rejected: %v", issuer, err)
		}
	}
}

func TestLoadRejectsInvalidPublicBaseURL(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_PUBLIC_BASE_URL", "https://links.example.test/root?tenant=one")

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with a query-bearing public base URL")
	}
}

func TestLoadRejectsInvalidPublicLinkClientConfig(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "official scheme", key: "TELESRV_PUBLIC_APP_SCHEME", value: "tg"},
		{name: "malformed scheme", key: "TELESRV_PUBLIC_APP_SCHEME", value: "bad scheme"},
		{name: "app link base official scheme", key: "TELESRV_PUBLIC_APP_LINK_BASE", value: "tg://links.example.test"},
		{name: "app link base missing host", key: "TELESRV_PUBLIC_APP_LINK_BASE", value: "owpg://"},
		{name: "app link base path", key: "TELESRV_PUBLIC_APP_LINK_BASE", value: "owpg://links.example.test/root"},
		{name: "app link base query", key: "TELESRV_PUBLIC_APP_LINK_BASE", value: "owpg://links.example.test?tenant=one"},
		{name: "invalid web base", key: "TELESRV_PUBLIC_WEB_BASE_URL", value: "file:///tmp/client"},
		{name: "empty app name after trim", key: "TELESRV_PUBLIC_APP_NAME", value: "   "},
		{name: "control in app name", key: "TELESRV_PUBLIC_APP_NAME", value: "bad\nname"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(tc.key, tc.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load succeeded with %s=%q", tc.key, tc.value)
			}
		})
	}
}

func TestLoadExplicitEmptyEnvironmentDisablesNullableListeners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telesrv.env")
	writeConfigFile(t, path, `
TELESRV_DEBUG_ADDR=127.0.0.1:6060
TELESRV_BOT_API_ADDR=127.0.0.1:8081
TELESRV_ADMIN_API_ADDR=127.0.0.1:2599
TELESRV_PUBLIC_LINK_WEB_ADDR=127.0.0.1:2401
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TELESRV_DEBUG_ADDR", "")
	t.Setenv("TELESRV_BOT_API_ADDR", "")
	t.Setenv("TELESRV_ADMIN_API_ADDR", "")
	t.Setenv("TELESRV_PUBLIC_LINK_WEB_ADDR", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DebugAddr != "" || cfg.BotAPIAddr != "" || cfg.AdminAPIAddr != "" || cfg.PublicLinkWebAddr != "" {
		t.Fatalf("nullable listeners were not disabled: debug=%q bot=%q admin=%q public=%q", cfg.DebugAddr, cfg.BotAPIAddr, cfg.AdminAPIAddr, cfg.PublicLinkWebAddr)
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

func TestValidateStarGiftConfigRejectsNegativeInternalTONGrant(t *testing.T) {
	cfg := Config{
		StarGiftSweepInterval:         time.Second,
		StarGiftSweepBatch:            1,
		StarGiftTONStartingGrant:      -1,
		StarGiftStarsProceedsPermille: 1000,
		StarGiftTONProceedsPermille:   1000,
	}
	if err := validateStarGiftConfig(cfg); err == nil {
		t.Fatal("negative internal TON starting grant was accepted")
	}
}

func TestLoadAccountRatingDefaults(t *testing.T) {
	disableDefaultConfigFile(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.RatingEnabled {
		t.Fatal("RatingEnabled = false, want the feature on by default")
	}
	if cfg.RatingPendingDelay != 24*time.Hour || cfg.RatingRecomputeInterval != 15*time.Minute ||
		cfg.RatingRecomputeBatch != 500 || cfg.RatingStaleAfter != 6*time.Hour {
		t.Fatalf("rating worker defaults = %v/%v/%d/%v, want 24h/15m/500/6h",
			cfg.RatingPendingDelay, cfg.RatingRecomputeInterval, cfg.RatingRecomputeBatch, cfg.RatingStaleAfter)
	}
	if got, want := cfg.AccountRatingWeights(), domain.DefaultAccountRatingWeights(); got != want {
		t.Fatalf("rating weights = %#v, want the domain defaults %#v", got, want)
	}
	if cfg.CollectibleUsernameURLTemplate != "" {
		t.Fatalf("CollectibleUsernameURLTemplate = %q, want empty (derived from the public base URL)",
			cfg.CollectibleUsernameURLTemplate)
	}
}

func TestLoadAccountRatingOverrides(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_RATING_ENABLED", "false")
	t.Setenv("TELESRV_RATING_PENDING_DELAY", "1h")
	t.Setenv("TELESRV_RATING_RECOMPUTE_INTERVAL", "90s")
	t.Setenv("TELESRV_RATING_RECOMPUTE_BATCH", "42")
	t.Setenv("TELESRV_RATING_STALE_AFTER", "30m")
	t.Setenv("TELESRV_RATING_WEIGHT_STARS_RECEIVED_PERMILLE", "500")
	t.Setenv("TELESRV_RATING_WEIGHT_MESSAGE_SENT", "0")
	t.Setenv("TELESRV_RATING_ACTIVITY_CAP", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RatingEnabled {
		t.Fatal("RatingEnabled = true, want the explicit override")
	}
	weights := cfg.AccountRatingWeights()
	if weights.StarsReceivedPermille != 500 || weights.PerMessageSent != 0 || weights.ActivityCap != 0 {
		t.Fatalf("weights = %#v, want the overridden values", weights)
	}
	if weights.StarsSpentPermille != domain.DefaultAccountRatingWeights().StarsSpentPermille {
		t.Fatalf("unset weight = %d, want the domain default", weights.StarsSpentPermille)
	}
	if cfg.RatingPendingDelay != time.Hour || cfg.RatingRecomputeInterval != 90*time.Second ||
		cfg.RatingRecomputeBatch != 42 || cfg.RatingStaleAfter != 30*time.Minute {
		t.Fatalf("rating worker overrides = %v/%v/%d/%v",
			cfg.RatingPendingDelay, cfg.RatingRecomputeInterval, cfg.RatingRecomputeBatch, cfg.RatingStaleAfter)
	}
}

func TestLoadRejectsInvalidAccountRatingConfig(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "negative stars weight", key: "TELESRV_RATING_WEIGHT_STARS_RECEIVED_PERMILLE", value: "-1"},
		{name: "negative moderation weight", key: "TELESRV_RATING_WEIGHT_MODERATION_CASE", value: "-150"},
		{name: "negative scam penalty", key: "TELESRV_RATING_WEIGHT_SCAM_PENALTY", value: "-1"},
		{name: "negative activity cap", key: "TELESRV_RATING_ACTIVITY_CAP", value: "-5000"},
		{name: "negative pending delay", key: "TELESRV_RATING_PENDING_DELAY", value: "-1h"},
		{name: "zero recompute interval", key: "TELESRV_RATING_RECOMPUTE_INTERVAL", value: "0s"},
		{name: "zero stale horizon", key: "TELESRV_RATING_STALE_AFTER", value: "0s"},
		{name: "zero recompute batch", key: "TELESRV_RATING_RECOMPUTE_BATCH", value: "0"},
		{name: "oversized recompute batch", key: "TELESRV_RATING_RECOMPUTE_BATCH", value: "20000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoadCollectibleUsernameURLTemplate(t *testing.T) {
	t.Run("absolute template accepted", func(t *testing.T) {
		disableDefaultConfigFile(t)
		t.Setenv("TELESRV_COLLECTIBLE_USERNAME_URL_TEMPLATE", " https://frag.example/u/{username} ")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.CollectibleUsernameURLTemplate != "https://frag.example/u/{username}" {
			t.Fatalf("template = %q, want the trimmed value", cfg.CollectibleUsernameURLTemplate)
		}
	})

	for _, invalid := range []string{"/nft/{username}", "ftp://frag.example/{username}", "https://user:pass@frag.example/{username}"} {
		t.Run("rejects "+invalid, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv("TELESRV_COLLECTIBLE_USERNAME_URL_TEMPLATE", invalid)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted template %q", invalid)
			}
		})
	}
}

func TestLoadVerificationDefaults(t *testing.T) {
	disableDefaultConfigFile(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.VerificationEnabled {
		t.Fatal("VerificationEnabled = false, want the feature shipped on")
	}
	if cfg.VerificationAllowUserTargets {
		t.Fatal("VerificationAllowUserTargets = true, want user targets opt-in")
	}
	if cfg.VerificationRejectCooldown != 720*time.Hour {
		t.Fatalf("VerificationRejectCooldown = %v, want 720h", cfg.VerificationRejectCooldown)
	}
	if cfg.VerificationApplyRateLimit != 3 || cfg.VerificationApplyRateWindow != 24*time.Hour {
		t.Fatalf("apply rate = %d/%v, want 3/24h", cfg.VerificationApplyRateLimit, cfg.VerificationApplyRateWindow)
	}
	if cfg.VerificationBotRateLimit != 30 || cfg.VerificationBotRateWindow != time.Minute {
		t.Fatalf("bot rate = %d/%v, want 30/1m", cfg.VerificationBotRateLimit, cfg.VerificationBotRateWindow)
	}
	if cfg.VerificationNotifyInterval != 15*time.Second || cfg.VerificationNotifyBatch != 50 {
		t.Fatalf("notify = %v/%d, want 15s/50", cfg.VerificationNotifyInterval, cfg.VerificationNotifyBatch)
	}
	if cfg.VerificationMaxActivePerUser != 3 {
		t.Fatalf("VerificationMaxActivePerUser = %d, want 3", cfg.VerificationMaxActivePerUser)
	}
}

func TestLoadVerificationOverrides(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_VERIFICATION_ENABLED", "false")
	t.Setenv("TELESRV_VERIFICATION_ALLOW_USER_TARGETS", "true")
	t.Setenv("TELESRV_VERIFICATION_REJECT_COOLDOWN", "48h")
	t.Setenv("TELESRV_VERIFICATION_APPLY_RATE_LIMIT", "7")
	t.Setenv("TELESRV_VERIFICATION_APPLY_RATE_WINDOW", "12h")
	t.Setenv("TELESRV_VERIFICATION_BOT_RATE_LIMIT", "0")
	t.Setenv("TELESRV_VERIFICATION_BOT_RATE_WINDOW", "0s")
	t.Setenv("TELESRV_VERIFICATION_NOTIFY_INTERVAL", "5s")
	t.Setenv("TELESRV_VERIFICATION_NOTIFY_BATCH", "200")
	t.Setenv("TELESRV_VERIFICATION_MAX_ACTIVE_PER_USER", "0")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VerificationEnabled || !cfg.VerificationAllowUserTargets {
		t.Fatalf("enabled=%v allowUserTargets=%v", cfg.VerificationEnabled, cfg.VerificationAllowUserTargets)
	}
	if cfg.VerificationRejectCooldown != 48*time.Hour {
		t.Fatalf("cooldown = %v", cfg.VerificationRejectCooldown)
	}
	if cfg.VerificationApplyRateLimit != 7 || cfg.VerificationApplyRateWindow != 12*time.Hour {
		t.Fatalf("apply rate = %d/%v", cfg.VerificationApplyRateLimit, cfg.VerificationApplyRateWindow)
	}
	// A zero limit disables the budget, so a zero window is accepted with it.
	if cfg.VerificationBotRateLimit != 0 || cfg.VerificationBotRateWindow != 0 {
		t.Fatalf("bot rate = %d/%v", cfg.VerificationBotRateLimit, cfg.VerificationBotRateWindow)
	}
	if cfg.VerificationNotifyInterval != 5*time.Second || cfg.VerificationNotifyBatch != 200 {
		t.Fatalf("notify = %v/%d", cfg.VerificationNotifyInterval, cfg.VerificationNotifyBatch)
	}
	if cfg.VerificationMaxActivePerUser != 0 {
		t.Fatalf("maxActive = %d, want the cap disabled", cfg.VerificationMaxActivePerUser)
	}
}

func TestLoadRejectsInvalidVerificationConfig(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{"TELESRV_VERIFICATION_REJECT_COOLDOWN", "-1h"},
		{"TELESRV_VERIFICATION_REJECT_COOLDOWN", "9000h"},
		{"TELESRV_VERIFICATION_APPLY_RATE_LIMIT", "-1"},
		{"TELESRV_VERIFICATION_APPLY_RATE_WINDOW", "0s"},
		{"TELESRV_VERIFICATION_APPLY_RATE_WINDOW", "-5m"},
		{"TELESRV_VERIFICATION_BOT_RATE_LIMIT", "-2"},
		{"TELESRV_VERIFICATION_BOT_RATE_WINDOW", "0s"},
		{"TELESRV_VERIFICATION_NOTIFY_INTERVAL", "0s"},
		{"TELESRV_VERIFICATION_NOTIFY_BATCH", "0"},
		{"TELESRV_VERIFICATION_NOTIFY_BATCH", "501"},
		{"TELESRV_VERIFICATION_MAX_ACTIVE_PER_USER", "-1"},
		{"TELESRV_VERIFICATION_MAX_ACTIVE_PER_USER", "51"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoadAdminRBACDefaults(t *testing.T) {
	disableDefaultConfigFile(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AdminUIPermissions) != 1 || cfg.AdminUIPermissions[0] != "*" {
		t.Fatalf("AdminUIPermissions = %v, want the wildcard default", cfg.AdminUIPermissions)
	}
	if len(cfg.AdminScopedTokens) != 0 {
		t.Fatalf("AdminScopedTokens = %+v, want none by default", cfg.AdminScopedTokens)
	}
}

func TestLoadAdminScopedTokens(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_ADMIN_UI_PERMISSIONS", "users.read, verification:decide")
	t.Setenv("TELESRV_ADMIN_SCOPED_TOKENS", "ops:tok-ops-1:users.read,users.write; audit:tok-audit-2:verification.*")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AdminUIPermissions) != 2 ||
		cfg.AdminUIPermissions[0] != "users.read" || cfg.AdminUIPermissions[1] != "verification:decide" {
		t.Fatalf("AdminUIPermissions = %v", cfg.AdminUIPermissions)
	}
	if len(cfg.AdminScopedTokens) != 2 {
		t.Fatalf("AdminScopedTokens = %+v, want 2 entries", cfg.AdminScopedTokens)
	}
	first := cfg.AdminScopedTokens[0]
	if first.Name != "ops" || first.Token != "tok-ops-1" ||
		len(first.Permissions) != 2 || first.Permissions[0] != "users.read" || first.Permissions[1] != "users.write" {
		t.Fatalf("first scoped token = %+v", first)
	}
	second := cfg.AdminScopedTokens[1]
	if second.Name != "audit" || second.Token != "tok-audit-2" ||
		len(second.Permissions) != 1 || second.Permissions[0] != "verification.*" {
		t.Fatalf("second scoped token = %+v", second)
	}
}

func TestLoadRejectsInvalidAdminRBACConfig(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"missing permissions field": {"TELESRV_ADMIN_SCOPED_TOKENS": "ops:tok-ops-1"},
		"too many fields":           {"TELESRV_ADMIN_SCOPED_TOKENS": "ops:tok:extra:users.read"},
		"empty name":                {"TELESRV_ADMIN_SCOPED_TOKENS": ":tok-ops-1:users.read"},
		"empty token":               {"TELESRV_ADMIN_SCOPED_TOKENS": "ops::users.read"},
		"no permissions listed":     {"TELESRV_ADMIN_SCOPED_TOKENS": "ops:tok-ops-1:"},
		"invalid permission":        {"TELESRV_ADMIN_SCOPED_TOKENS": "ops:tok-ops-1:users read"},
		"duplicate name":            {"TELESRV_ADMIN_SCOPED_TOKENS": "ops:tok-a:users.read;OPS:tok-b:users.read"},
		"duplicate token":           {"TELESRV_ADMIN_SCOPED_TOKENS": "ops:tok-a:users.read;audit:tok-a:users.read"},
		"reuses the admin api token": {
			"TELESRV_ADMIN_API_TOKEN":     "tok-a",
			"TELESRV_ADMIN_SCOPED_TOKENS": "ops:tok-a:users.read",
		},
		"invalid ui permission": {"TELESRV_ADMIN_UI_PERMISSIONS": "users/read"},
	} {
		t.Run(name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted %v", env)
			}
		})
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
