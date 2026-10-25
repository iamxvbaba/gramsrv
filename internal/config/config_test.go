package config

import (
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestLoadDefaultsAdvertiseIPToLoopback(t *testing.T) {
	t.Setenv("TELESRV_ADVERTISE_IP", "")
	t.Setenv("TELESRV_PUBLIC_BASE_URL", "")
	t.Setenv("TELESRV_SEND_RATE_LIMIT", "")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdvertiseIP != "127.0.0.1" {
		t.Fatalf("AdvertiseIP = %q, want loopback default", cfg.AdvertiseIP)
	}
	if cfg.AdvertisePort != 2398 {
		t.Fatalf("AdvertisePort = %d, want listen port 2398", cfg.AdvertisePort)
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
	if cfg.PublicAppName != "Telesrv" {
		t.Fatalf("PublicAppName = %q, want Telesrv", cfg.PublicAppName)
	}
	if cfg.Branding.ProductName != "Telesrv" || cfg.Branding.ProductUsername != "telesrv" ||
		cfg.Branding.DesktopAppName != "Telesrv Desktop" || cfg.Branding.StarsName != "Telesrv Stars" ||
		cfg.Branding.PublicBaseURL != cfg.PublicBaseURL {
		t.Fatalf("Branding = %+v", cfg.Branding)
	}
	if cfg.CallRegistryMaxEntries != 10_000 {
		t.Fatalf("CallRegistryMaxEntries = %d, want 10000", cfg.CallRegistryMaxEntries)
	}
	if cfg.PremiumPromoSeedDir != "data/premium-promo" {
		t.Fatalf("PremiumPromoSeedDir = %q, want data/premium-promo", cfg.PremiumPromoSeedDir)
	}
	if cfg.BlobBackendKind != string(domain.MediaBackendLocalFS) {
		t.Fatalf("BlobBackendKind = %q, want localfs", cfg.BlobBackendKind)
	}
	if cfg.BlobDir != "data/blobs" {
		t.Fatalf("BlobDir = %q, want data/blobs", cfg.BlobDir)
	}
	if !cfg.StorageLowSpaceGuardEnable || cfg.StorageMinFreeBytes != 1<<30 || cfg.StorageMaxTotalBytes != 0 || cfg.StorageUsageRefreshInterval != time.Minute {
		t.Fatalf("unexpected storage capacity defaults: enabled=%v min=%d max=%d interval=%v", cfg.StorageLowSpaceGuardEnable, cfg.StorageMinFreeBytes, cfg.StorageMaxTotalBytes, cfg.StorageUsageRefreshInterval)
	}
	if cfg.SendRateLimit != 0 {
		t.Fatalf("SendRateLimit = %d, want disabled by default", cfg.SendRateLimit)
	}
}

func TestLoadUsesListenPortAsAdvertisePort(t *testing.T) {
	t.Setenv("TELESRV_LISTEN", "127.0.0.1:2444")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdvertisePort != 2444 {
		t.Fatalf("AdvertisePort = %d, want listen port 2444", cfg.AdvertisePort)
	}
}

func TestLoadUsesExplicitAdvertisePort(t *testing.T) {
	t.Setenv("TELESRV_LISTEN", "127.0.0.1:2397")
	t.Setenv("TELESRV_ADVERTISE_PORT", "2398")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdvertisePort != 2398 {
		t.Fatalf("AdvertisePort = %d, want explicit port 2398", cfg.AdvertisePort)
	}
}

func TestLoadRejectsInvalidAdvertisePort(t *testing.T) {
	t.Setenv("TELESRV_ADVERTISE_PORT", "70000")

	if _, err := loadProcessEnvConfig(); err == nil {
		t.Fatal("Load accepted invalid advertise port")
	}
}

func TestLoadS3BlobStorageConfig(t *testing.T) {
	t.Setenv("TELESRV_BLOB_BACKEND", "s3")
	t.Setenv("TELESRV_BLOB_STAGING_DIR", `D:\staging\telesrv`)
	t.Setenv("TELESRV_S3_ENDPOINT", "minio.example.test:9000")
	t.Setenv("TELESRV_S3_BUCKET", "telesrv-media")
	t.Setenv("TELESRV_S3_ACCESS_KEY_ID", "access")
	t.Setenv("TELESRV_S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("TELESRV_S3_USE_SSL", "false")
	t.Setenv("TELESRV_S3_PATH_STYLE", "true")
	t.Setenv("TELESRV_S3_CREATE_BUCKET", "true")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BlobBackendKind != "s3" || cfg.S3Endpoint != "minio.example.test:9000" || cfg.S3Bucket != "telesrv-media" {
		t.Fatalf("unexpected s3 config: backend=%q endpoint=%q bucket=%q", cfg.BlobBackendKind, cfg.S3Endpoint, cfg.S3Bucket)
	}
	if cfg.S3UseSSL || !cfg.S3PathStyle || !cfg.S3CreateBucket {
		t.Fatalf("unexpected s3 flags: ssl=%v path_style=%v create=%v", cfg.S3UseSSL, cfg.S3PathStyle, cfg.S3CreateBucket)
	}
}

func TestLoadRejectsInvalidBlobStorageConfig(t *testing.T) {
	tests := []struct {
		name     string
		backend  string
		endpoint string
		bucket   string
		access   string
		secret   string
	}{
		{name: "unknown backend", backend: "mirror"},
		{name: "missing s3 endpoint", backend: "s3", bucket: "media", access: "access", secret: "secret"},
		{name: "endpoint has scheme", backend: "s3", endpoint: "http://minio:9000", bucket: "media", access: "access", secret: "secret"},
		{name: "missing s3 bucket", backend: "s3", endpoint: "minio:9000", access: "access", secret: "secret"},
		{name: "missing s3 credentials", backend: "s3", endpoint: "minio:9000", bucket: "media"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TELESRV_BLOB_BACKEND", tt.backend)
			t.Setenv("TELESRV_S3_ENDPOINT", tt.endpoint)
			t.Setenv("TELESRV_S3_BUCKET", tt.bucket)
			t.Setenv("TELESRV_S3_ACCESS_KEY_ID", tt.access)
			t.Setenv("TELESRV_S3_SECRET_ACCESS_KEY", tt.secret)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatal("invalid blob storage config accepted")
			}
		})
	}
}

func TestLoadRejectsInvalidStorageCapacityConfig(t *testing.T) {
	for _, item := range []struct{ key, value string }{
		{"TELESRV_STORAGE_MIN_FREE_BYTES", "-1"},
		{"TELESRV_STORAGE_MAX_TOTAL_BYTES", "-1"},
		{"TELESRV_STORAGE_USAGE_REFRESH_INTERVAL", "0s"},
		{"TELESRV_STORAGE_USAGE_REFRESH_INTERVAL", "-1s"},
	} {
		t.Run(item.key+"="+item.value, func(t *testing.T) {
			t.Setenv(item.key, item.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", item.key, item.value)
			}
		})
	}
}

func TestLoadUpdateServiceConfig(t *testing.T) {
	t.Setenv("TELESRV_UPDATE_PUBLIC_URL", "https://updates.example.test/root/")
	t.Setenv("TELESRV_UPDATE_SERVICE_URL", "http://127.0.0.1:2402/")
	t.Setenv("TELESRV_UPDATE_REQUEST_TIMEOUT", "3s")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UpdatePublicURL != "https://updates.example.test/root" || cfg.UpdateServiceURL != "http://127.0.0.1:2402" {
		t.Fatalf("update URLs = %q / %q", cfg.UpdatePublicURL, cfg.UpdateServiceURL)
	}
	if cfg.UpdateRequestTimeout != 3*time.Second {
		t.Fatalf("UpdateRequestTimeout = %v", cfg.UpdateRequestTimeout)
	}
}

func TestLoadUpdateServiceDefaultsInternalURLToPublic(t *testing.T) {
	t.Setenv("TELESRV_UPDATE_PUBLIC_URL", "https://updates.example.test")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UpdateServiceURL != cfg.UpdatePublicURL {
		t.Fatalf("UpdateServiceURL = %q, want %q", cfg.UpdateServiceURL, cfg.UpdatePublicURL)
	}
}

func TestLoadRejectsInvalidUpdateServiceConfig(t *testing.T) {
	t.Setenv("TELESRV_UPDATE_PUBLIC_URL", "file:///updates")
	if _, err := loadProcessEnvConfig(); err == nil {
		t.Fatal("invalid update public URL accepted")
	}
}

func TestLoadPremiumPromoSeedDirOverride(t *testing.T) {
	t.Setenv("TELESRV_PREMIUM_PROMO_SEED_DIR", `D:\seed\premium-promo`)

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PremiumPromoSeedDir != `D:\seed\premium-promo` {
		t.Fatalf("PremiumPromoSeedDir = %q", cfg.PremiumPromoSeedDir)
	}
}

func TestLoadPremiumBotAndPlans(t *testing.T) {
	t.Setenv("TELESRV_PREMIUM_BOT_USERNAME", "@premium_store_bot")
	t.Setenv("TELESRV_PREMIUM_BOT_USER_ID", "1250000999")
	t.Setenv("TELESRV_PREMIUM_PLANS", "1:30:250,12:365:2400")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PremiumBotUsername != "premium_store_bot" || cfg.PremiumBotUserID != 1250000999 {
		t.Fatalf("Premium bot config = %q/%d", cfg.PremiumBotUsername, cfg.PremiumBotUserID)
	}
	if len(cfg.PremiumPlans) != 2 || cfg.PremiumPlans[0].Months != 1 ||
		cfg.PremiumPlans[0].DurationDays != 30 || cfg.PremiumPlans[0].AmountStars != 250 ||
		cfg.PremiumPlans[1].Months != 12 {
		t.Fatalf("Premium plans = %+v", cfg.PremiumPlans)
	}
}

func TestLoadBrandingConfig(t *testing.T) {
	t.Setenv("TELESRV_PUBLIC_BASE_URL", "https://links.example.test/root/")
	t.Setenv("TELESRV_BRAND_PRODUCT_NAME", " Example Chat ")
	t.Setenv("TELESRV_BRAND_PRODUCT_USERNAME", "@Example_Chat")
	t.Setenv("TELESRV_BRAND_DESKTOP_APP_NAME", "Example Workstation")
	t.Setenv("TELESRV_BRAND_ANDROID_APP_NAME", "Example Droid")
	t.Setenv("TELESRV_BRAND_IOS_APP_NAME", "Example Phone")
	t.Setenv("TELESRV_BRAND_MACOS_APP_NAME", "Example Mac")
	t.Setenv("TELESRV_BRAND_WEB_A_APP_NAME", "Example Web Alpha")
	t.Setenv("TELESRV_BRAND_WEB_K_APP_NAME", "Example Web Kappa")
	t.Setenv("TELESRV_BRAND_PREMIUM_NAME", "Example Plus")
	t.Setenv("TELESRV_BRAND_STARS_NAME", "Example Credits")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	brand := cfg.Branding
	if brand.ProductName != "Example Chat" || brand.ProductUsername != "example_chat" ||
		brand.DesktopAppName != "Example Workstation" || brand.AndroidAppName != "Example Droid" ||
		brand.IOSAppName != "Example Phone" || brand.MacOSAppName != "Example Mac" ||
		brand.WebAAppName != "Example Web Alpha" || brand.WebKAppName != "Example Web Kappa" ||
		brand.PremiumName != "Example Plus" || brand.StarsName != "Example Credits" ||
		brand.PublicBaseURL != "https://links.example.test/root" {
		t.Fatalf("Branding = %+v", brand)
	}
	if cfg.PublicAppName != brand.ProductName {
		t.Fatalf("PublicAppName = %q, want product default %q", cfg.PublicAppName, brand.ProductName)
	}
	if cfg.SMTPFromName != brand.ProductName {
		t.Fatalf("SMTPFromName = %q, want product default %q", cfg.SMTPFromName, brand.ProductName)
	}
}

func TestLoadRejectsInvalidBrandingConfig(t *testing.T) {
	for _, item := range []struct{ key, value string }{
		{"TELESRV_BRAND_PRODUCT_NAME", "   "},
		{"TELESRV_BRAND_PRODUCT_USERNAME", "3bad"},
		{"TELESRV_BRAND_DESKTOP_APP_NAME", "bad\nname"},
		{"TELESRV_BRAND_STARS_NAME", "bad\u007fname"},
	} {
		t.Run(item.key, func(t *testing.T) {
			t.Setenv(item.key, item.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted %s=%q", item.key, item.value)
			}
		})
	}
}

func TestLoadRejectsInvalidPremiumCatalog(t *testing.T) {
	for _, value := range []string{
		"3:90:0",
		"3:90:750,3:91:760",
		"121:365:750",
		"3:36501:750",
		"3:90:1000000000000001",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("TELESRV_PREMIUM_PLANS", value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted TELESRV_PREMIUM_PLANS=%q", value)
			}
		})
	}
}

func TestLoadUsesExplicitAdvertiseIP(t *testing.T) {
	t.Setenv("TELESRV_ADVERTISE_IP", "203.0.113.10")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdvertiseIP != "203.0.113.10" {
		t.Fatalf("AdvertiseIP = %q, want explicit env", cfg.AdvertiseIP)
	}
}

func TestLoadCanonicalizesAdvertiseIP(t *testing.T) {
	t.Setenv("TELESRV_ADVERTISE_IP", " 2001:0db8::1 ")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdvertiseIP != "2001:db8::1" {
		t.Fatalf("AdvertiseIP = %q, want canonical IPv6", cfg.AdvertiseIP)
	}
}

func TestLoadRejectsUnusableAdvertiseIP(t *testing.T) {
	for _, value := range []string{"example.com", "0.0.0.0", "::", "224.0.0.1", "fe80::1%eth0"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("TELESRV_ADVERTISE_IP", value)

			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted TELESRV_ADVERTISE_IP=%q", value)
			}
		})
	}
}

func TestLoadDefaultCountryCode(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("TELESRV_DEFAULT_COUNTRY_CODE", "")

		cfg, err := loadProcessEnvConfig()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.DefaultCountryCode != "CN" {
			t.Fatalf("DefaultCountryCode = %q, want CN", cfg.DefaultCountryCode)
		}
	})

	t.Run("normalized override", func(t *testing.T) {
		t.Setenv("TELESRV_DEFAULT_COUNTRY_CODE", " us ")

		cfg, err := loadProcessEnvConfig()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.DefaultCountryCode != "US" {
			t.Fatalf("DefaultCountryCode = %q, want US", cfg.DefaultCountryCode)
		}
	})
}

func TestLoadRejectsInvalidDefaultCountryCode(t *testing.T) {
	for _, value := range []string{"+86", "CHN", "C1", "中", "ZZ"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("TELESRV_DEFAULT_COUNTRY_CODE", value)

			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted TELESRV_DEFAULT_COUNTRY_CODE=%q", value)
			}
		})
	}
}

func TestLoadStrictDCCheck(t *testing.T) {
	t.Run("defaults off", func(t *testing.T) {
		t.Setenv("TELESRV_STRICT_DC_CHECK", "")

		cfg, err := loadProcessEnvConfig()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.StrictDCCheck {
			t.Fatal("StrictDCCheck = true, want default false")
		}
	})

	t.Run("explicitly enabled", func(t *testing.T) {
		t.Setenv("TELESRV_STRICT_DC_CHECK", "true")

		cfg, err := loadProcessEnvConfig()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.StrictDCCheck {
			t.Fatal("StrictDCCheck = false, want true")
		}
	})
}

func TestLoadRejectsNonPositiveCanonicalDC(t *testing.T) {
	for _, value := range []string{"0", "-2", "2147483648"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("TELESRV_DC", value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted TELESRV_DC=%s", value)
			}
		})
	}
}

func TestLoadMTProtoAdmissionAndRPCBudgets(t *testing.T) {
	t.Setenv("TELESRV_MTPROTO_MAX_CONNECTIONS", "12345")
	t.Setenv("TELESRV_MTPROTO_MAX_CONNECTIONS_PER_IP", "234")
	t.Setenv("TELESRV_MTPROTO_MAX_CONCURRENT_HANDSHAKES", "45")
	t.Setenv("TELESRV_MTPROTO_RPC_MAX_INFLIGHT", "7")
	t.Setenv("TELESRV_MTPROTO_RPC_QUEUE_SIZE", "19")
	t.Setenv("TELESRV_MTPROTO_RPC_TIMEOUT", "9s")
	t.Setenv("TELESRV_MTPROTO_RPC_GLOBAL_WORKERS", "33")
	t.Setenv("TELESRV_MTPROTO_RPC_GLOBAL_MAX_TASKS", "444")
	t.Setenv("TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES", "555555")
	t.Setenv("TELESRV_MTPROTO_RPC_EXECUTION_MAX_ENTRIES", "555")
	t.Setenv("TELESRV_MTPROTO_RPC_EXECUTION_AUTH_MAX_ENTRIES", "444")
	t.Setenv("TELESRV_MTPROTO_RPC_EXECUTION_SESSION_MAX_ENTRIES", "333")
	t.Setenv("TELESRV_MTPROTO_RPC_EXECUTION_PENDING_PER_AUTH", "222")
	t.Setenv("TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES", "777777")
	t.Setenv("TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE", "88")
	t.Setenv("TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE", "22")
	t.Setenv("TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES", "888888")
	t.Setenv("TELESRV_MTPROTO_OUTBOUND_CRITICAL_GLOBAL_MAX_BYTES", "898989")
	t.Setenv("TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES", "999999")
	t.Setenv("TELESRV_TEMP_KEY_CACHE_MAX_ENTRIES", "666")
	t.Setenv("TELESRV_TEMP_KEY_CACHE_TTL", "17m")
	t.Setenv("TELESRV_AUTH_USER_CACHE_TTL", "23s")
	t.Setenv("TELESRV_AUTH_KEY_CACHE_MAX_ENTRIES", "777")
	t.Setenv("TELESRV_AUTH_KEY_CACHE_TTL", "19m")
	t.Setenv("TELESRV_ORPHAN_AUTH_KEY_RETENTION", "36h")

	cfg, err := loadProcessEnvConfig()
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
	if cfg.MTProtoRPCExecutionMaxEntries != 555 ||
		cfg.MTProtoRPCExecutionAuthMaxEntries != 444 ||
		cfg.MTProtoRPCExecutionSessionMaxEntries != 333 ||
		cfg.MTProtoRPCExecutionPendingPerAuth != 222 {
		t.Fatalf("rpc execution ledger config = global:%d auth:%d session:%d pending/auth:%d",
			cfg.MTProtoRPCExecutionMaxEntries,
			cfg.MTProtoRPCExecutionAuthMaxEntries,
			cfg.MTProtoRPCExecutionSessionMaxEntries,
			cfg.MTProtoRPCExecutionPendingPerAuth)
	}
	if cfg.MTProtoInboundFrameGlobalMaxBytes != 777777 {
		t.Fatalf("inbound frame budget config = %d", cfg.MTProtoInboundFrameGlobalMaxBytes)
	}
	if cfg.MTProtoOutboundQueueSize != 88 || cfg.MTProtoOutboundControlQueueSize != 22 || cfg.MTProtoOutboundTrackedGlobalMaxBytes != 888888 || cfg.MTProtoOutboundCriticalGlobalMaxBytes != 898989 || cfg.MTProtoOutboundWriteGlobalMaxBytes != 999999 {
		t.Fatalf("outbound config = %d/%d/%d/%d/%d", cfg.MTProtoOutboundQueueSize, cfg.MTProtoOutboundControlQueueSize, cfg.MTProtoOutboundTrackedGlobalMaxBytes, cfg.MTProtoOutboundCriticalGlobalMaxBytes, cfg.MTProtoOutboundWriteGlobalMaxBytes)
	}
	if cfg.TempKeyResolveCacheMaxEntries != 666 || cfg.TempKeyResolveCacheTTL != 17*time.Minute || cfg.AuthUserCacheTTL != 23*time.Second ||
		cfg.AuthKeyCacheMaxEntries != 777 || cfg.AuthKeyCacheTTL != 19*time.Minute || cfg.OrphanAuthKeyRetention != 36*time.Hour {
		t.Fatalf("auth key resource config = temp:%d/%v auth_user:%v auth_key:%d/%v orphan:%v",
			cfg.TempKeyResolveCacheMaxEntries, cfg.TempKeyResolveCacheTTL, cfg.AuthUserCacheTTL,
			cfg.AuthKeyCacheMaxEntries, cfg.AuthKeyCacheTTL, cfg.OrphanAuthKeyRetention)
	}
}

func TestLoadEdgeLocationConfig(t *testing.T) {
	t.Setenv("TELESRV_INSTANCE_ID", " edge-a ")
	t.Setenv("TELESRV_EDGE_LOCATION_TTL", "2m")
	t.Setenv("TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL", "20s")
	t.Setenv("TELESRV_SFU_OWNER_TTL", "3m")
	t.Setenv("TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL", "25s")
	t.Setenv("TELESRV_SFU_INSTANCE_TTL", "4m")
	t.Setenv("TELESRV_SFU_INSTANCE_HEARTBEAT_INTERVAL", "40s")
	t.Setenv("TELESRV_SFU_INSTANCE_HEALTH_TIMEOUT", "1500ms")
	t.Setenv("TELESRV_SFU_INSTANCE_MAX_ACTIVE_CALLS", "128")
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_ADDR", "127.0.0.1:2411")
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_URL", "grpc://sfu-a:2411")
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_REQUEST_TIMEOUT", "6s")
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_TLS_CERT_FILE", `D:\certs\sfu.pem`)
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_TLS_KEY_FILE", `D:\certs\sfu-key.pem`)
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CA_FILE", `D:\certs\core-ca.pem`)
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_TLS_CA_FILE", `D:\certs\sfu-ca.pem`)
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_TLS_SERVER_NAME", "sfu.internal")
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CERT_FILE", `D:\certs\core.pem`)
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_KEY_FILE", `D:\certs\core-key.pem`)
	t.Setenv("TELESRV_SFU_CONTROL_TOKEN", "sfu-secret")
	t.Setenv("TELESRV_GROUPCALL_CONTROL_ADDR", "127.0.0.1:2420")
	t.Setenv("TELESRV_GROUPCALL_CONTROL_URL", "http://127.0.0.1:2420")
	t.Setenv("TELESRV_GROUPCALL_CONTROL_TOKEN", "core-secret")
	t.Setenv("TELESRV_CORE_EXEC_GRPC_ADDR", "127.0.0.1:2440")
	t.Setenv("TELESRV_CORE_EXEC_GRPC_RESOLVER", "static")
	t.Setenv("TELESRV_CORE_EXEC_GRPC_TARGETS", "127.0.0.1:2440,127.0.0.1:2441")
	t.Setenv("TELESRV_CORE_EXEC_GRPC_REQUEST_TIMEOUT", "7s")
	t.Setenv("TELESRV_CORE_EXEC_GRPC_TLS_CERT_FILE", `D:\certs\core.pem`)
	t.Setenv("TELESRV_CORE_EXEC_GRPC_TLS_KEY_FILE", `D:\certs\core-key.pem`)
	t.Setenv("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CA_FILE", `D:\certs\edge-ca.pem`)
	t.Setenv("TELESRV_CORE_EXEC_GRPC_TLS_CA_FILE", `D:\certs\core-ca.pem`)
	t.Setenv("TELESRV_CORE_EXEC_GRPC_TLS_SERVER_NAME", "core.internal")
	t.Setenv("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CERT_FILE", `D:\certs\edge.pem`)
	t.Setenv("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_KEY_FILE", `D:\certs\edge-key.pem`)
	t.Setenv("TELESRV_CORE_EXEC_TOKEN", "exec-secret")
	t.Setenv("TELESRV_FILE_GRPC_ADDR", "127.0.0.1:2460")
	t.Setenv("TELESRV_FILE_GRPC_RESOLVER", "static")
	t.Setenv("TELESRV_FILE_GRPC_TARGETS", "127.0.0.1:2460,127.0.0.1:2461")
	t.Setenv("TELESRV_FILE_GRPC_REQUEST_TIMEOUT", "9s")
	t.Setenv("TELESRV_FILE_GRPC_TLS_CERT_FILE", `D:\certs\file.pem`)
	t.Setenv("TELESRV_FILE_GRPC_TLS_KEY_FILE", `D:\certs\file-key.pem`)
	t.Setenv("TELESRV_FILE_GRPC_TLS_CLIENT_CA_FILE", `D:\certs\edge-ca.pem`)
	t.Setenv("TELESRV_FILE_GRPC_TLS_CA_FILE", `D:\certs\file-ca.pem`)
	t.Setenv("TELESRV_FILE_GRPC_TLS_SERVER_NAME", "file.internal")
	t.Setenv("TELESRV_FILE_GRPC_TLS_CLIENT_CERT_FILE", `D:\certs\edge.pem`)
	t.Setenv("TELESRV_FILE_GRPC_TLS_CLIENT_KEY_FILE", `D:\certs\edge-key.pem`)
	t.Setenv("TELESRV_FILE_TOKEN", "file-secret")
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_ADDR", "127.0.0.1:2450")
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_RESOLVER", "static")
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_TARGETS", "127.0.0.1:2450,127.0.0.1:2451")
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_REQUEST_TIMEOUT", "8s")
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CERT_FILE", `D:\certs\egress.pem`)
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_TLS_KEY_FILE", `D:\certs\egress-key.pem`)
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CA_FILE", `D:\certs\edge-ca.pem`)
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CA_FILE", `D:\certs\egress-ca.pem`)
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_TLS_SERVER_NAME", "egress.internal")
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CERT_FILE", `D:\certs\edge.pem`)
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_KEY_FILE", `D:\certs\edge-key.pem`)
	t.Setenv("TELESRV_EGRESS_DELIVERY_TOKEN", "egress-secret")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.InstanceID != "edge-a" {
		t.Fatalf("InstanceID = %q, want edge-a", cfg.InstanceID)
	}
	if cfg.EdgeLocationTTL != 2*time.Minute || cfg.EdgeLocationHeartbeatInterval != 20*time.Second {
		t.Fatalf("edge location config = %v/%v, want 2m/20s", cfg.EdgeLocationTTL, cfg.EdgeLocationHeartbeatInterval)
	}
	if cfg.SFUOwnerTTL != 3*time.Minute || cfg.SFUOwnerHeartbeatInterval != 25*time.Second {
		t.Fatalf("sfu owner config = %v/%v, want 3m/25s", cfg.SFUOwnerTTL, cfg.SFUOwnerHeartbeatInterval)
	}
	if cfg.SFUInstanceTTL != 4*time.Minute || cfg.SFUInstanceHeartbeatInterval != 40*time.Second {
		t.Fatalf("sfu instance config = %v/%v, want 4m/40s", cfg.SFUInstanceTTL, cfg.SFUInstanceHeartbeatInterval)
	}
	if cfg.SFUInstanceHealthTimeout != 1500*time.Millisecond {
		t.Fatalf("SFUInstanceHealthTimeout = %v, want 1500ms", cfg.SFUInstanceHealthTimeout)
	}
	if cfg.SFUInstanceMaxActiveCalls != 128 {
		t.Fatalf("SFUInstanceMaxActiveCalls = %d, want 128", cfg.SFUInstanceMaxActiveCalls)
	}
	if cfg.SFUControlGRPCAddr != "127.0.0.1:2411" ||
		cfg.SFUControlGRPCURL != "grpc://sfu-a:2411" ||
		cfg.SFUControlGRPCRequestTimeout != 6*time.Second ||
		cfg.SFUControlGRPCTLSCertFile != `D:\certs\sfu.pem` ||
		cfg.SFUControlGRPCTLSKeyFile != `D:\certs\sfu-key.pem` ||
		cfg.SFUControlGRPCTLSClientCAFile != `D:\certs\core-ca.pem` ||
		cfg.SFUControlGRPCTLSCAFile != `D:\certs\sfu-ca.pem` ||
		cfg.SFUControlGRPCTLSServerName != "sfu.internal" ||
		cfg.SFUControlGRPCTLSClientCertFile != `D:\certs\core.pem` ||
		cfg.SFUControlGRPCTLSClientKeyFile != `D:\certs\core-key.pem` ||
		cfg.SFUControlToken != "sfu-secret" {
		t.Fatalf("sfu control config = grpc:%q/%q timeout:%v tls:%q/%q client_ca:%q ca:%q server_name:%q client:%q/%q token:%q",
			cfg.SFUControlGRPCAddr,
			cfg.SFUControlGRPCURL,
			cfg.SFUControlGRPCRequestTimeout,
			cfg.SFUControlGRPCTLSCertFile,
			cfg.SFUControlGRPCTLSKeyFile,
			cfg.SFUControlGRPCTLSClientCAFile,
			cfg.SFUControlGRPCTLSCAFile,
			cfg.SFUControlGRPCTLSServerName,
			cfg.SFUControlGRPCTLSClientCertFile,
			cfg.SFUControlGRPCTLSClientKeyFile,
			cfg.SFUControlToken)
	}
	if cfg.GroupCallControlAddr != "127.0.0.1:2420" || cfg.GroupCallControlURL != "http://127.0.0.1:2420" || cfg.GroupCallControlToken != "core-secret" {
		t.Fatalf("groupcall control config = %q/%q/%q", cfg.GroupCallControlAddr, cfg.GroupCallControlURL, cfg.GroupCallControlToken)
	}
	if cfg.CoreExecToken != "exec-secret" {
		t.Fatalf("CoreExecToken = %q, want exec-secret", cfg.CoreExecToken)
	}
	if cfg.CoreExecGRPCAddr != "127.0.0.1:2440" || cfg.CoreExecGRPCTargets != "127.0.0.1:2440,127.0.0.1:2441" {
		t.Fatalf("core exec grpc config = %q/%q", cfg.CoreExecGRPCAddr, cfg.CoreExecGRPCTargets)
	}
	if cfg.CoreExecGRPCResolver != "static" {
		t.Fatalf("CoreExecGRPCResolver = %q, want static", cfg.CoreExecGRPCResolver)
	}
	if cfg.CoreExecGRPCRequestTimeout != 7*time.Second {
		t.Fatalf("CoreExecGRPCRequestTimeout = %v, want 7s", cfg.CoreExecGRPCRequestTimeout)
	}
	if cfg.CoreExecGRPCTLSCertFile != `D:\certs\core.pem` ||
		cfg.CoreExecGRPCTLSKeyFile != `D:\certs\core-key.pem` ||
		cfg.CoreExecGRPCTLSClientCAFile != `D:\certs\edge-ca.pem` ||
		cfg.CoreExecGRPCTLSCAFile != `D:\certs\core-ca.pem` ||
		cfg.CoreExecGRPCTLSServerName != "core.internal" ||
		cfg.CoreExecGRPCTLSClientCertFile != `D:\certs\edge.pem` ||
		cfg.CoreExecGRPCTLSClientKeyFile != `D:\certs\edge-key.pem` {
		t.Fatalf("core exec grpc tls config = server:%q/%q client_ca:%q ca:%q server_name:%q client:%q/%q",
			cfg.CoreExecGRPCTLSCertFile,
			cfg.CoreExecGRPCTLSKeyFile,
			cfg.CoreExecGRPCTLSClientCAFile,
			cfg.CoreExecGRPCTLSCAFile,
			cfg.CoreExecGRPCTLSServerName,
			cfg.CoreExecGRPCTLSClientCertFile,
			cfg.CoreExecGRPCTLSClientKeyFile)
	}
	if cfg.FileToken != "file-secret" {
		t.Fatalf("FileToken = %q, want file-secret", cfg.FileToken)
	}
	if cfg.FileGRPCAddr != "127.0.0.1:2460" || cfg.FileGRPCTargets != "127.0.0.1:2460,127.0.0.1:2461" {
		t.Fatalf("file grpc config = %q/%q", cfg.FileGRPCAddr, cfg.FileGRPCTargets)
	}
	if cfg.FileGRPCResolver != "static" {
		t.Fatalf("FileGRPCResolver = %q, want static", cfg.FileGRPCResolver)
	}
	if cfg.FileGRPCRequestTimeout != 9*time.Second {
		t.Fatalf("FileGRPCRequestTimeout = %v, want 9s", cfg.FileGRPCRequestTimeout)
	}
	if cfg.FileGRPCTLSCertFile != `D:\certs\file.pem` ||
		cfg.FileGRPCTLSKeyFile != `D:\certs\file-key.pem` ||
		cfg.FileGRPCTLSClientCAFile != `D:\certs\edge-ca.pem` ||
		cfg.FileGRPCTLSCAFile != `D:\certs\file-ca.pem` ||
		cfg.FileGRPCTLSServerName != "file.internal" ||
		cfg.FileGRPCTLSClientCertFile != `D:\certs\edge.pem` ||
		cfg.FileGRPCTLSClientKeyFile != `D:\certs\edge-key.pem` {
		t.Fatalf("file grpc tls config = server:%q/%q client_ca:%q ca:%q server_name:%q client:%q/%q",
			cfg.FileGRPCTLSCertFile,
			cfg.FileGRPCTLSKeyFile,
			cfg.FileGRPCTLSClientCAFile,
			cfg.FileGRPCTLSCAFile,
			cfg.FileGRPCTLSServerName,
			cfg.FileGRPCTLSClientCertFile,
			cfg.FileGRPCTLSClientKeyFile)
	}
	if cfg.EgressDeliveryToken != "egress-secret" {
		t.Fatalf("EgressDeliveryToken = %q, want egress-secret", cfg.EgressDeliveryToken)
	}
	if cfg.EgressDeliveryGRPCAddr != "127.0.0.1:2450" || cfg.EgressDeliveryGRPCTargets != "127.0.0.1:2450,127.0.0.1:2451" {
		t.Fatalf("egress delivery grpc config = %q/%q", cfg.EgressDeliveryGRPCAddr, cfg.EgressDeliveryGRPCTargets)
	}
	if cfg.EgressDeliveryGRPCResolver != "static" {
		t.Fatalf("EgressDeliveryGRPCResolver = %q, want static", cfg.EgressDeliveryGRPCResolver)
	}
	if cfg.EgressDeliveryGRPCRequestTimeout != 8*time.Second {
		t.Fatalf("EgressDeliveryGRPCRequestTimeout = %v, want 8s", cfg.EgressDeliveryGRPCRequestTimeout)
	}
	if cfg.EgressDeliveryGRPCTLSCertFile != `D:\certs\egress.pem` ||
		cfg.EgressDeliveryGRPCTLSKeyFile != `D:\certs\egress-key.pem` ||
		cfg.EgressDeliveryGRPCTLSClientCAFile != `D:\certs\edge-ca.pem` ||
		cfg.EgressDeliveryGRPCTLSCAFile != `D:\certs\egress-ca.pem` ||
		cfg.EgressDeliveryGRPCTLSServerName != "egress.internal" ||
		cfg.EgressDeliveryGRPCTLSClientCertFile != `D:\certs\edge.pem` ||
		cfg.EgressDeliveryGRPCTLSClientKeyFile != `D:\certs\edge-key.pem` {
		t.Fatalf("egress delivery grpc tls config = server:%q/%q client_ca:%q ca:%q server_name:%q client:%q/%q",
			cfg.EgressDeliveryGRPCTLSCertFile,
			cfg.EgressDeliveryGRPCTLSKeyFile,
			cfg.EgressDeliveryGRPCTLSClientCAFile,
			cfg.EgressDeliveryGRPCTLSCAFile,
			cfg.EgressDeliveryGRPCTLSServerName,
			cfg.EgressDeliveryGRPCTLSClientCertFile,
			cfg.EgressDeliveryGRPCTLSClientKeyFile)
	}
}

func TestLoadRejectsInvalidCoreExecGRPCResolver(t *testing.T) {
	t.Setenv("TELESRV_CORE_EXEC_GRPC_RESOLVER", "consul")
	if _, err := loadProcessEnvConfig(); err == nil {
		t.Fatal("Load accepted invalid TELESRV_CORE_EXEC_GRPC_RESOLVER")
	}
}

func TestLoadRejectsOverlappingDurableDeliveryDeadlineWindow(t *testing.T) {
	t.Setenv("TELESRV_OUTBOX_LEASE_TIMEOUT", "3s")
	t.Setenv("TELESRV_DELIVERY_ATTEMPT_TIMEOUT", "2s")
	t.Setenv("TELESRV_DELIVERY_CLOCK_SKEW_ALLOWANCE", "1s")
	if _, err := loadProcessEnvConfig(); err == nil {
		t.Fatal("Load accepted delivery attempt + skew window without lease safety tail")
	}
}

func TestLoadRejectsInvalidCoreExecGRPCRequestTimeout(t *testing.T) {
	for _, item := range []struct{ name, value string }{
		{name: "zero", value: "0s"},
		{name: "negative", value: "-1s"},
		{name: "too large", value: "61s"},
	} {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("TELESRV_CORE_EXEC_GRPC_REQUEST_TIMEOUT", item.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted TELESRV_CORE_EXEC_GRPC_REQUEST_TIMEOUT=%s", item.value)
			}
		})
	}
}

func TestLoadRejectsInvalidEgressDeliveryGRPCResolver(t *testing.T) {
	t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_RESOLVER", "consul")
	if _, err := loadProcessEnvConfig(); err == nil {
		t.Fatal("Load accepted invalid TELESRV_EGRESS_DELIVERY_GRPC_RESOLVER")
	}
}

func TestLoadRejectsInvalidEgressDeliveryGRPCRequestTimeout(t *testing.T) {
	for _, item := range []struct{ name, value string }{
		{name: "zero", value: "0s"},
		{name: "negative", value: "-1s"},
		{name: "too large", value: "61s"},
	} {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("TELESRV_EGRESS_DELIVERY_GRPC_REQUEST_TIMEOUT", item.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted TELESRV_EGRESS_DELIVERY_GRPC_REQUEST_TIMEOUT=%s", item.value)
			}
		})
	}
}

func TestLoadRejectsInvalidCoreExecGRPCTLSConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "server cert without key", env: map[string]string{
			"TELESRV_CORE_EXEC_GRPC_TLS_CERT_FILE": "core.pem",
		}},
		{name: "server client ca without server cert", env: map[string]string{
			"TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CA_FILE": "edge-ca.pem",
		}},
		{name: "client cert without key", env: map[string]string{
			"TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CERT_FILE": "edge.pem",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatal("Load accepted invalid CoreExec gRPC TLS config")
			}
		})
	}
}

func TestLoadRejectsInvalidEgressDeliveryGRPCTLSConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "server cert without key", env: map[string]string{
			"TELESRV_EGRESS_DELIVERY_GRPC_TLS_CERT_FILE": "egress.pem",
		}},
		{name: "server client ca without server cert", env: map[string]string{
			"TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CA_FILE": "edge-ca.pem",
		}},
		{name: "client cert without key", env: map[string]string{
			"TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CERT_FILE": "edge.pem",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatal("Load accepted invalid Egress Delivery gRPC TLS config")
			}
		})
	}
}

func TestLoadRejectsInvalidFileGRPCResolver(t *testing.T) {
	t.Setenv("TELESRV_FILE_GRPC_RESOLVER", "consul")
	if _, err := loadProcessEnvConfig(); err == nil {
		t.Fatal("Load accepted invalid TELESRV_FILE_GRPC_RESOLVER")
	}
}

func TestLoadRejectsInvalidFileGRPCRequestTimeout(t *testing.T) {
	for _, item := range []struct{ name, value string }{
		{name: "zero", value: "0s"},
		{name: "negative", value: "-1s"},
		{name: "too large", value: "61s"},
	} {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("TELESRV_FILE_GRPC_REQUEST_TIMEOUT", item.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted TELESRV_FILE_GRPC_REQUEST_TIMEOUT=%s", item.value)
			}
		})
	}
}

func TestLoadRejectsInvalidFileGRPCTLSConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "server cert without key", env: map[string]string{
			"TELESRV_FILE_GRPC_TLS_CERT_FILE": "file.pem",
		}},
		{name: "server client ca without server cert", env: map[string]string{
			"TELESRV_FILE_GRPC_TLS_CLIENT_CA_FILE": "edge-ca.pem",
		}},
		{name: "client cert without key", env: map[string]string{
			"TELESRV_FILE_GRPC_TLS_CLIENT_CERT_FILE": "edge.pem",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatal("Load accepted invalid FileData gRPC TLS config")
			}
		})
	}
}

func TestLoadRejectsInvalidEdgeLocationConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "zero ttl", env: map[string]string{"TELESRV_EDGE_LOCATION_TTL": "0s"}},
		{name: "negative heartbeat", env: map[string]string{"TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL": "-1s"}},
		{name: "heartbeat equals ttl", env: map[string]string{
			"TELESRV_EDGE_LOCATION_TTL":                "30s",
			"TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL": "30s",
		}},
		{name: "heartbeat exceeds ttl", env: map[string]string{
			"TELESRV_EDGE_LOCATION_TTL":                "20s",
			"TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL": "30s",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted edge location config %v", test.env)
			}
		})
	}
}

func TestLoadRejectsInvalidSFUOwnerConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "zero ttl", env: map[string]string{"TELESRV_SFU_OWNER_TTL": "0s"}},
		{name: "negative heartbeat", env: map[string]string{"TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL": "-1s"}},
		{name: "heartbeat equals ttl", env: map[string]string{
			"TELESRV_SFU_OWNER_TTL":                "30s",
			"TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL": "30s",
		}},
		{name: "heartbeat exceeds ttl", env: map[string]string{
			"TELESRV_SFU_OWNER_TTL":                "20s",
			"TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL": "30s",
		}},
		{name: "zero instance ttl", env: map[string]string{"TELESRV_SFU_INSTANCE_TTL": "0s"}},
		{name: "instance heartbeat equals ttl", env: map[string]string{
			"TELESRV_SFU_INSTANCE_TTL":                "30s",
			"TELESRV_SFU_INSTANCE_HEARTBEAT_INTERVAL": "30s",
		}},
		{name: "zero instance health timeout", env: map[string]string{"TELESRV_SFU_INSTANCE_HEALTH_TIMEOUT": "0s"}},
		{name: "zero grpc request timeout", env: map[string]string{"TELESRV_SFU_CONTROL_GRPC_REQUEST_TIMEOUT": "0s"}},
		{name: "too large grpc request timeout", env: map[string]string{"TELESRV_SFU_CONTROL_GRPC_REQUEST_TIMEOUT": "90s"}},
		{name: "sfu grpc server cert without key", env: map[string]string{
			"TELESRV_SFU_CONTROL_GRPC_TLS_CERT_FILE": "sfu.pem",
		}},
		{name: "sfu grpc client ca without server cert", env: map[string]string{
			"TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CA_FILE": "core-ca.pem",
		}},
		{name: "sfu grpc client cert without key", env: map[string]string{
			"TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CERT_FILE": "core.pem",
		}},
		{name: "token newline", env: map[string]string{"TELESRV_SFU_CONTROL_TOKEN": "bad\nsecret"}},
		{name: "groupcall token newline", env: map[string]string{"TELESRV_GROUPCALL_CONTROL_TOKEN": "bad\nsecret"}},
		{name: "coreexec token newline", env: map[string]string{"TELESRV_CORE_EXEC_TOKEN": "bad\nsecret"}},
		{name: "egress delivery token newline", env: map[string]string{"TELESRV_EGRESS_DELIVERY_TOKEN": "bad\nsecret"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted sfu owner config %v", test.env)
			}
		})
	}
}

func TestLoadRPCExecutionFairBudgetDefaults(t *testing.T) {
	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MTProtoRPCExecutionMaxEntries != 1<<18 ||
		cfg.MTProtoRPCExecutionAuthMaxEntries != 1<<15 ||
		cfg.MTProtoRPCExecutionSessionMaxEntries != 1<<14 ||
		cfg.MTProtoRPCExecutionPendingPerAuth != 1<<11 {
		t.Fatalf("rpc execution receipt defaults = global:%d auth:%d session:%d pending/auth:%d",
			cfg.MTProtoRPCExecutionMaxEntries,
			cfg.MTProtoRPCExecutionAuthMaxEntries,
			cfg.MTProtoRPCExecutionSessionMaxEntries,
			cfg.MTProtoRPCExecutionPendingPerAuth)
	}
}

func TestLoadRejectsInvalidRPCExecutionFairBudgets(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "entry hierarchy", key: "TELESRV_MTPROTO_RPC_EXECUTION_MAX_ENTRIES", value: "1024"},
		{name: "pending hierarchy", key: "TELESRV_MTPROTO_RPC_EXECUTION_PENDING_PER_AUTH", value: "33000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoadRejectsMalformedMTProtoCapacity(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "worker tasks malformed", key: "TELESRV_MTPROTO_RPC_GLOBAL_MAX_TASKS", value: "lots"},
		{name: "receipt entries overflow", key: "TELESRV_MTPROTO_RPC_EXECUTION_MAX_ENTRIES", value: "999999999999999999999999"},
		{name: "tracked bytes overflow", key: "TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES", value: "999999999999999999999999"},
		{name: "outbound queue malformed", key: "TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE", value: "many"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted %s=%q", test.key, test.value)
			}
		})
	}
}

func TestLoadBusinessAIProvider(t *testing.T) {
	t.Setenv("TELESRV_BUSINESS_AI_PROVIDER", "echo")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BusinessAIProvider != "echo" {
		t.Fatalf("BusinessAIProvider = %q, want echo", cfg.BusinessAIProvider)
	}
}

func TestLoadBusinessAIProviderDefaultsToEcho(t *testing.T) {
	t.Setenv("TELESRV_BUSINESS_AI_PROVIDER", "")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BusinessAIProvider != "echo" {
		t.Fatalf("BusinessAIProvider = %q, want echo", cfg.BusinessAIProvider)
	}
}

func TestLoadLoginEmailDefaultsDisabled(t *testing.T) {

	cfg, err := loadProcessEnvConfig()
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

	cfg, err := loadProcessEnvConfig()
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
	t.Setenv("TELESRV_LOGIN_EMAIL_ENABLE", "true")

	if _, err := loadProcessEnvConfig(); err == nil {
		t.Fatal("Load succeeded with login email enabled but no SMTP host")
	}
}

func TestLoadLoginEmailWebhookDoesNotRequireSMTP(t *testing.T) {
	t.Setenv("TELESRV_LOGIN_EMAIL_ENABLE", "true")
	t.Setenv("TELESRV_EMAIL_CODE_DELIVERY_PROVIDER", "webhook")
	t.Setenv("TELESRV_OTP_WEBHOOK_URL", "https://otp.example.test/v1/deliveries")
	t.Setenv("TELESRV_OTP_WEBHOOK_SECRET", "test-secret")
	t.Setenv("TELESRV_OTP_WEBHOOK_TIMEOUT", "3s")
	t.Setenv("TELESRV_SMTP_HOST", "")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EmailCodeDeliveryProvider != "webhook" || cfg.OTPWebhookURL != "https://otp.example.test/v1/deliveries" ||
		cfg.OTPWebhookSecret != "test-secret" || cfg.OTPWebhookTimeout != 3*time.Second {
		t.Fatalf("webhook config = %#v", cfg)
	}
}

func TestLoadPhoneWebhookRequiresValidURL(t *testing.T) {
	t.Setenv("TELESRV_PHONE_CODE_DELIVERY_PROVIDER", "webhook")
	t.Setenv("TELESRV_OTP_WEBHOOK_URL", "relative/path")

	if _, err := loadProcessEnvConfig(); err == nil {
		t.Fatal("Load succeeded with relative OTP webhook URL")
	}
}

func TestLoadPhoneWebhookConfig(t *testing.T) {
	t.Setenv("TELESRV_PHONE_CODE_DELIVERY_PROVIDER", "webhook")
	t.Setenv("TELESRV_PHONE_CODE_LENGTH", "7")
	t.Setenv("TELESRV_OTP_WEBHOOK_URL", "http://127.0.0.1:8080/otp")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PhoneCodeDeliveryProvider != "webhook" || cfg.PhoneCodeLength != 7 {
		t.Fatalf("phone webhook config = %#v", cfg)
	}
}

func TestLoadKeepsAdminAndRtmpDefaultPortsSeparate(t *testing.T) {
	t.Setenv("TELESRV_ADMIN_UI_ADDR", "")
	t.Setenv("TELESRV_LIVESTREAM_RTMP_ADDR", "")

	cfg, err := loadProcessEnvConfig()
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

	cfg, err := loadProcessEnvConfig()
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
	t.Setenv("TELESRV_TRANSLATION_ENABLED", "true")
	t.Setenv("TELESRV_TRANSLATION_PROVIDERS", "openai,gemini")
	t.Setenv("TELESRV_TRANSLATION_TIMEOUT", "9s")
	t.Setenv("TELESRV_TRANSLATION_RATE_LIMIT", "17")
	t.Setenv("TELESRV_TRANSLATION_RATE_WINDOW", "2m")
	cfg, err := loadProcessEnvConfig()
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

func TestLoadNormalizesLocalPublicBaseURL(t *testing.T) {
	t.Setenv("TELESRV_PUBLIC_BASE_URL", "http://127.0.0.1:2401/")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PublicBaseURL != "http://127.0.0.1:2401" {
		t.Fatalf("PublicBaseURL = %q, want http://127.0.0.1:2401", cfg.PublicBaseURL)
	}
}

func TestLoadTelegramLoginConfig(t *testing.T) {
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

	cfg, err := loadProcessEnvConfig()
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
	t.Setenv("TELESRV_PUBLIC_BASE_URL", "https://links.example.test/root?tenant=one")

	if _, err := loadProcessEnvConfig(); err == nil {
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
			t.Setenv(tc.key, tc.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load succeeded with %s=%q", tc.key, tc.value)
			}
		})
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

// 未配置端点时必须放行:会话列表继续回传 "Unknown" 占位文案是未启用时的
// 预期行为,不该让服务起不来。
func TestValidateGeoIPConfigAllowsDisabledBackend(t *testing.T) {
	if err := validateGeoIPConfig(Config{}); err != nil {
		t.Fatalf("empty GeoIP config: %v", err)
	}
	// 全是空白的列表等同于未启用,不该把服务卡在启动校验上。
	if err := validateGeoIPConfig(Config{GeoIPEndpoints: []string{"   ", ""}}); err != nil {
		t.Fatalf("blank GeoIP endpoints: %v", err)
	}
}

func TestValidateGeoIPConfigRejectsUnusableSettings(t *testing.T) {
	valid := Config{
		GeoIPEndpoints:          []string{"https://reallyfreegeoip.org/json/{ip}", "https://api.ipapi.is/?q={ip}"},
		GeoIPTimeout:            2 * time.Second,
		GeoIPConcurrency:        4,
		GeoIPCacheTTL:           24 * time.Hour,
		GeoIPNegativeTTL:        5 * time.Minute,
		GeoIPCacheSize:          4096,
		GeoIPRateLimitThreshold: 3,
		GeoIPRateLimitCooldown:  2 * time.Minute,
		GeoIPDownThreshold:      2,
		GeoIPDownCooldown:       30 * time.Second,
	}
	if err := validateGeoIPConfig(valid); err != nil {
		t.Fatalf("valid GeoIP config: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		// failover 链里任何一条坏配置都必须在启动时报出来,而不是等到某次会话列表才发现
		// 后面那个后端永远不被问。
		{name: "missing placeholder", mutate: func(c *Config) { c.GeoIPEndpoints[1] = "https://geo.example.test/json" }},
		{name: "unsupported scheme", mutate: func(c *Config) { c.GeoIPEndpoints[0] = "ftp://geo.example.test/json/{ip}" }},
		{name: "missing host", mutate: func(c *Config) { c.GeoIPEndpoints[0] = "https:///json/{ip}" }},
		{name: "zero timeout", mutate: func(c *Config) { c.GeoIPTimeout = 0 }},
		// 上游正常响应 0.6~1.3s,给到 10s 以上基本是值填错了(比如被当成 duration 串解析),
		// 那时候每一批解析都会先耗掉整个预算才回落 Unknown。
		{name: "timeout beyond budget", mutate: func(c *Config) { c.GeoIPTimeout = 30 * time.Second }},
		{name: "unbounded concurrency", mutate: func(c *Config) { c.GeoIPConcurrency = 33 }},
		{name: "zero concurrency", mutate: func(c *Config) { c.GeoIPConcurrency = 0 }},
		// 负缓存必须显著短于正文缓存:长到一样就等于一次限流把该 IP 锁到第二天。
		{name: "negative ttl not shorter", mutate: func(c *Config) { c.GeoIPNegativeTTL = c.GeoIPCacheTTL }},
		{name: "zero negative ttl", mutate: func(c *Config) { c.GeoIPNegativeTTL = 0 }},
		{name: "unbounded cache", mutate: func(c *Config) { c.GeoIPCacheSize = 1_000_001 }},
		{name: "zero rate limit threshold", mutate: func(c *Config) { c.GeoIPRateLimitThreshold = 0 }},
		{name: "cooldown too long", mutate: func(c *Config) { c.GeoIPRateLimitCooldown = 2 * time.Hour }},
		{name: "zero down threshold", mutate: func(c *Config) { c.GeoIPDownThreshold = 0 }},
		// 短暂抖动不该让后端离线比配额窗口还久。
		{name: "down cooldown longer than rate limit", mutate: func(c *Config) { c.GeoIPDownCooldown = c.GeoIPRateLimitCooldown + time.Second }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			cfg.GeoIPEndpoints = append([]string(nil), valid.GeoIPEndpoints...)
			tc.mutate(&cfg)
			if err := validateGeoIPConfig(cfg); err == nil {
				t.Fatal("unusable GeoIP config was accepted")
			}
		})
	}
}

// 后端链顺序即 failover 顺序,必须原样保留。重复项交给解析层去重,不该在这里报错。
// v2 只从 Core YAML 的 geoip 段读取,进程环境不能覆盖。
func TestLoadCoreGeoIPEndpointsPreserveFailoverOrder(t *testing.T) {
	path := writeRoleYAML(t, "core-geoip.yaml", `
version: 1
geoip:
  endpoints:
    - https://a.example/json/{ip}
    - " https://b.example/json/{ip}"
    - https://c.example/json/{ip}
  timeout: 1500ms
  concurrency: 6
  cache_ttl: 12h
  negative_ttl: 2m
  cache_size: 128
  rate_limit_threshold: 5
  rate_limit_cooldown: 3m
  down_threshold: 4
  down_cooldown: 45s
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TELESRV_GEOIP_ENDPOINTS", "https://process-env.example/{ip}")

	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore: %v", err)
	}
	want := []string{
		"https://a.example/json/{ip}",
		"https://b.example/json/{ip}",
		"https://c.example/json/{ip}",
	}
	if len(cfg.GeoIPEndpoints) != len(want) {
		t.Fatalf("GeoIPEndpoints = %v, want %v", cfg.GeoIPEndpoints, want)
	}
	for i := range want {
		if cfg.GeoIPEndpoints[i] != want[i] {
			t.Fatalf("GeoIPEndpoints[%d] = %q, want %q", i, cfg.GeoIPEndpoints[i], want[i])
		}
	}
	if cfg.GeoIPTimeout != 1500*time.Millisecond || cfg.GeoIPConcurrency != 6 || cfg.GeoIPCacheTTL != 12*time.Hour ||
		cfg.GeoIPNegativeTTL != 2*time.Minute || cfg.GeoIPCacheSize != 128 || cfg.GeoIPRateLimitThreshold != 5 ||
		cfg.GeoIPRateLimitCooldown != 3*time.Minute || cfg.GeoIPDownThreshold != 4 || cfg.GeoIPDownCooldown != 45*time.Second {
		t.Fatalf("geoip tuning = %+v", cfg)
	}
}

func TestLoadCoreGeoIPDisabledByDefaultAndValidated(t *testing.T) {
	path := writeRoleYAML(t, "core-geoip-default.yaml", "version: 1\n")
	t.Setenv("TELESRV_CONFIG", path)
	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore default: %v", err)
	}
	if len(cfg.GeoIPEndpoints) != 0 || cfg.GeoIPTimeout != 2*time.Second || cfg.GeoIPCacheTTL != 24*time.Hour {
		t.Fatalf("default geoip = endpoints:%v timeout:%s cache_ttl:%s", cfg.GeoIPEndpoints, cfg.GeoIPTimeout, cfg.GeoIPCacheTTL)
	}

	path = writeRoleYAML(t, "core-geoip-invalid.yaml", `
version: 1
geoip:
  endpoints: [https://geo.example/json]
`)
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadCore(); err == nil || !strings.Contains(err.Error(), "{ip}") {
		t.Fatalf("LoadCore accepted an endpoint without {ip}: %v", err)
	}
}

func TestLoadAccountRatingDefaults(t *testing.T) {

	cfg, err := loadProcessEnvConfig()
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
	t.Setenv("TELESRV_RATING_ENABLED", "false")
	t.Setenv("TELESRV_RATING_PENDING_DELAY", "1h")
	t.Setenv("TELESRV_RATING_RECOMPUTE_INTERVAL", "90s")
	t.Setenv("TELESRV_RATING_RECOMPUTE_BATCH", "42")
	t.Setenv("TELESRV_RATING_STALE_AFTER", "30m")
	t.Setenv("TELESRV_RATING_WEIGHT_STARS_RECEIVED_PERMILLE", "500")
	t.Setenv("TELESRV_RATING_WEIGHT_MESSAGE_SENT", "0")
	t.Setenv("TELESRV_RATING_ACTIVITY_CAP", "0")

	cfg, err := loadProcessEnvConfig()
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
			t.Setenv(test.key, test.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoadCollectibleUsernameURLTemplate(t *testing.T) {
	t.Run("absolute template accepted", func(t *testing.T) {
		t.Setenv("TELESRV_COLLECTIBLE_USERNAME_URL_TEMPLATE", " https://frag.example/u/{username} ")

		cfg, err := loadProcessEnvConfig()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.CollectibleUsernameURLTemplate != "https://frag.example/u/{username}" {
			t.Fatalf("template = %q, want the trimmed value", cfg.CollectibleUsernameURLTemplate)
		}
	})

	for _, invalid := range []string{"/nft/{username}", "ftp://frag.example/{username}", "https://user:pass@frag.example/{username}"} {
		t.Run("rejects "+invalid, func(t *testing.T) {
			t.Setenv("TELESRV_COLLECTIBLE_USERNAME_URL_TEMPLATE", invalid)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted template %q", invalid)
			}
		})
	}
}

func TestLoadVerificationDefaults(t *testing.T) {

	cfg, err := loadProcessEnvConfig()
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

	cfg, err := loadProcessEnvConfig()
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
			t.Setenv(test.key, test.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", test.key, test.value)
			}
		})
	}
}

func TestLoadBotVerificationDefaults(t *testing.T) {

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.BotVerificationEnabled {
		t.Fatal("BotVerificationEnabled = false, want the feature shipped on")
	}
	// The default is the storage bound itself, so the shipped behaviour is the same
	// whether or not the key is set.
	if cfg.BotVerificationMaxPerVerifier != domain.MaxCustomVerificationsPerVerifier {
		t.Fatalf("BotVerificationMaxPerVerifier = %d, want %d", cfg.BotVerificationMaxPerVerifier, domain.MaxCustomVerificationsPerVerifier)
	}
	if cfg.BotVerificationRequestRateLimit != 5 || cfg.BotVerificationRequestRateWindow != 24*time.Hour {
		t.Fatalf("request rate = %d/%v, want 5/24h", cfg.BotVerificationRequestRateLimit, cfg.BotVerificationRequestRateWindow)
	}
}

func TestLoadBotVerificationOverrides(t *testing.T) {
	t.Setenv("TELESRV_BOT_VERIFICATION_ENABLED", "false")
	t.Setenv("TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER", "0")
	t.Setenv("TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT", "0")
	t.Setenv("TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW", "0s")

	cfg, err := loadProcessEnvConfig()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BotVerificationEnabled {
		t.Fatal("BotVerificationEnabled = true, want the override honoured")
	}
	if cfg.BotVerificationMaxPerVerifier != 0 {
		t.Fatalf("BotVerificationMaxPerVerifier = %d, want the service bound disabled", cfg.BotVerificationMaxPerVerifier)
	}
	// A zero limit disables the budget, so a zero window is accepted with it.
	if cfg.BotVerificationRequestRateLimit != 0 || cfg.BotVerificationRequestRateWindow != 0 {
		t.Fatalf("request rate = %d/%v, want the budget disabled", cfg.BotVerificationRequestRateLimit, cfg.BotVerificationRequestRateWindow)
	}
}

func TestLoadRejectsInvalidBotVerificationConfig(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{"TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER", "-1"},
		{"TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER", "10001"},
		{"TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT", "-1"},
		{"TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW", "-5m"},
		// A positive limit with no window is a limiter that never refills.
		{"TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW", "0s"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			t.Setenv(test.key, test.value)
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", test.key, test.value)
			}
		})
	}
}

// TestLoadValidatesBotVerificationWhileDisabled pins that the policy is checked
// even with the feature off, so switching it on later is not the moment a typo is
// discovered.
func TestLoadValidatesBotVerificationWhileDisabled(t *testing.T) {
	t.Setenv("TELESRV_BOT_VERIFICATION_ENABLED", "false")
	t.Setenv("TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER", "-3")

	if _, err := loadProcessEnvConfig(); err == nil {
		t.Fatal("Load accepted a negative per-verifier bound while the feature was disabled")
	}
}

func TestLoadAdminRBACDefaults(t *testing.T) {

	cfg, err := loadProcessEnvConfig()
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
	t.Setenv("TELESRV_ADMIN_UI_PERMISSIONS", "users.read, verification:decide")
	t.Setenv("TELESRV_ADMIN_SCOPED_TOKENS", "ops:tok-ops-1:users.read,users.write; audit:tok-audit-2:verification.*")

	cfg, err := loadProcessEnvConfig()
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
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := loadProcessEnvConfig(); err == nil {
				t.Fatalf("Load accepted %v", env)
			}
		})
	}
}

func loadProcessEnvConfig() (Config, error) {
	return loadFromEnv(newEnvSource(nil, true))
}
