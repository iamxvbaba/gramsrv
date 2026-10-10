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
	if cfg.MTProtoRPCGlobalMaxTasks != 32768 {
		t.Fatalf("MTProtoRPCGlobalMaxTasks = %d, want 32768", cfg.MTProtoRPCGlobalMaxTasks)
	}
}

func TestLoadBuildsPostgresDSNFromPassword(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_POSTGRES_DSN", "")
	t.Setenv("TELESRV_POSTGRES_PASSWORD", "p@ss:word/with?reserved#chars")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := "postgres://telesrv:p%40ss%3Aword%2Fwith%3Freserved%23chars@127.0.0.1:5432/telesrv_main?sslmode=disable"
	if cfg.PostgresDSN != want {
		t.Fatalf("PostgresDSN = %q, want %q", cfg.PostgresDSN, want)
	}
}

func TestLoadPreservesExplicitPostgresDSN(t *testing.T) {
	disableDefaultConfigFile(t)
	want := "postgres://external:secret@db.example:5432/app?sslmode=require"
	t.Setenv("TELESRV_POSTGRES_DSN", want)
	t.Setenv("TELESRV_POSTGRES_PASSWORD", "ignored")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PostgresDSN != want {
		t.Fatalf("PostgresDSN = %q, want explicit DSN %q", cfg.PostgresDSN, want)
	}
}

func TestLoadEmptyProcessPasswordsOverrideFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	writeConfigFile(t, path, "TELESRV_POSTGRES_PASSWORD=from-file\nTELESRV_REDIS_PASSWORD=from-file\n")
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TELESRV_POSTGRES_DSN", "")
	t.Setenv("TELESRV_POSTGRES_PASSWORD", "")
	t.Setenv("TELESRV_REDIS_PASSWORD", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PostgresDSN != "postgres://telesrv:telesrv@127.0.0.1:5432/telesrv_main?sslmode=disable" {
		t.Fatal("empty process Postgres password must select the Compose default")
	}
	if cfg.RedisPassword != "" {
		t.Fatal("empty process Redis password must override the config file")
	}
}

func TestLoadDialogListSnapshotRedisTTL(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_DIALOG_LIST_SNAPSHOT_REDIS_TTL", "37m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DialogListSnapshotRedisTTL != 37*time.Minute {
		t.Fatalf("DialogListSnapshotRedisTTL = %v, want 37m", cfg.DialogListSnapshotRedisTTL)
	}
}

func TestLoadRejectsInvalidDialogListSnapshotRedisTTL(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_DIALOG_LIST_SNAPSHOT_REDIS_TTL", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("zero dialog list snapshot Redis TTL accepted")
	}
}

func TestLoadActiveChannelIDsReadModel(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_ACTIVE_CHANNEL_IDS_CACHE_MAX", "45678")
	t.Setenv("TELESRV_ACTIVE_CHANNEL_IDS_CACHE_TTL", "9h")
	t.Setenv("TELESRV_ACTIVE_CHANNEL_IDS_REDIS_TTL", "27h")
	t.Setenv("TELESRV_ACTIVE_CHANNEL_IDS_BATCH_MAX", "73")
	t.Setenv("TELESRV_ACTIVE_CHANNEL_IDS_BATCH_WAIT", "37ms")
	t.Setenv("TELESRV_ACTIVE_CHANNEL_IDS_BATCH_QUEUE", "901")
	t.Setenv("TELESRV_ACTIVE_CHANNEL_IDS_BATCH_TIMEOUT", "3s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ActiveChannelIDsCacheMaxEntries != 45678 || cfg.ActiveChannelIDsCacheTTL != 9*time.Hour ||
		cfg.ActiveChannelIDsRedisTTL != 27*time.Hour || cfg.ActiveChannelIDsBatchMax != 73 ||
		cfg.ActiveChannelIDsBatchWait != 37*time.Millisecond || cfg.ActiveChannelIDsBatchQueue != 901 ||
		cfg.ActiveChannelIDsBatchTimeout != 3*time.Second {
		t.Fatalf("active channel IDs config = max=%d l1=%v redis=%v batch=%d/%v/%d/%v",
			cfg.ActiveChannelIDsCacheMaxEntries, cfg.ActiveChannelIDsCacheTTL, cfg.ActiveChannelIDsRedisTTL,
			cfg.ActiveChannelIDsBatchMax, cfg.ActiveChannelIDsBatchWait, cfg.ActiveChannelIDsBatchQueue,
			cfg.ActiveChannelIDsBatchTimeout)
	}
}

func TestLoadRejectsInvalidActiveChannelIDsReadModel(t *testing.T) {
	for _, test := range []struct{ key, value string }{
		{key: "TELESRV_ACTIVE_CHANNEL_IDS_CACHE_MAX", value: "0"},
		{key: "TELESRV_ACTIVE_CHANNEL_IDS_CACHE_TTL", value: "0s"},
		{key: "TELESRV_ACTIVE_CHANNEL_IDS_REDIS_TTL", value: "0s"},
		{key: "TELESRV_ACTIVE_CHANNEL_IDS_BATCH_MAX", value: "0"},
		{key: "TELESRV_ACTIVE_CHANNEL_IDS_BATCH_WAIT", value: "0s"},
		{key: "TELESRV_ACTIVE_CHANNEL_IDS_BATCH_QUEUE", value: "1"},
		{key: "TELESRV_ACTIVE_CHANNEL_IDS_BATCH_TIMEOUT", value: "0s"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid %s=%s accepted", test.key, test.value)
			}
		})
	}
}

func TestLoadChannelDifferenceCache(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_CHANNEL_DIFFERENCE_CACHE_MAX", "123")
	t.Setenv("TELESRV_CHANNEL_DIFFERENCE_CACHE_BYTES_MAX", "456789")
	t.Setenv("TELESRV_CHANNEL_DIFFERENCE_CACHE_TTL", "7m")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ChannelDifferenceCacheMaxEntries != 123 || cfg.ChannelDifferenceCacheMaxBytes != 456789 || cfg.ChannelDifferenceCacheTTL != 7*time.Minute {
		t.Fatalf("channel difference cache = %d/%d/%v",
			cfg.ChannelDifferenceCacheMaxEntries, cfg.ChannelDifferenceCacheMaxBytes, cfg.ChannelDifferenceCacheTTL)
	}
}

func TestLoadRejectsInvalidChannelDifferenceCache(t *testing.T) {
	for _, test := range []struct{ key, value string }{
		{key: "TELESRV_CHANNEL_DIFFERENCE_CACHE_BYTES_MAX", value: "0"},
		{key: "TELESRV_CHANNEL_DIFFERENCE_CACHE_TTL", value: "0s"},
		{key: "TELESRV_CHANNEL_DIFFERENCE_CACHE_TTL", value: "25h"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid %s=%s accepted", test.key, test.value)
			}
		})
	}
}

func TestLoadLayerAdvanceBatch(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_LAYER_ADVANCE_BATCH_MAX", "73")
	t.Setenv("TELESRV_LAYER_ADVANCE_BATCH_WAIT", "400us")
	t.Setenv("TELESRV_LAYER_ADVANCE_BATCH_QUEUE", "901")
	t.Setenv("TELESRV_LAYER_ADVANCE_BATCH_TIMEOUT", "3s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LayerAdvanceBatchMax != 73 || cfg.LayerAdvanceBatchWait != 400*time.Microsecond ||
		cfg.LayerAdvanceBatchQueue != 901 || cfg.LayerAdvanceBatchTimeout != 3*time.Second {
		t.Fatalf("layer advance batch = %d/%v/%d/%v",
			cfg.LayerAdvanceBatchMax, cfg.LayerAdvanceBatchWait,
			cfg.LayerAdvanceBatchQueue, cfg.LayerAdvanceBatchTimeout)
	}
}

func TestLoadReadModelVersionBatch(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_READ_MODEL_VERSION_BATCH_MAX_KEYS", "3072")
	t.Setenv("TELESRV_READ_MODEL_VERSION_BATCH_WAIT", "350us")
	t.Setenv("TELESRV_READ_MODEL_VERSION_BATCH_QUEUE", "777")
	t.Setenv("TELESRV_READ_MODEL_VERSION_BATCH_TIMEOUT", "4s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ReadModelVersionBatchMaxKeys != 3072 || cfg.ReadModelVersionBatchWait != 350*time.Microsecond ||
		cfg.ReadModelVersionBatchQueue != 777 || cfg.ReadModelVersionBatchTimeout != 4*time.Second {
		t.Fatalf("read-model version batch = %d/%v/%d/%v",
			cfg.ReadModelVersionBatchMaxKeys, cfg.ReadModelVersionBatchWait,
			cfg.ReadModelVersionBatchQueue, cfg.ReadModelVersionBatchTimeout)
	}
}

func TestLoadRejectsInvalidReadModelVersionBatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero max keys", key: "TELESRV_READ_MODEL_VERSION_BATCH_MAX_KEYS", value: "0"},
		{name: "wait too large", key: "TELESRV_READ_MODEL_VERSION_BATCH_WAIT", value: "11ms"},
		{name: "zero queue", key: "TELESRV_READ_MODEL_VERSION_BATCH_QUEUE", value: "0"},
		{name: "timeout too large", key: "TELESRV_READ_MODEL_VERSION_BATCH_TIMEOUT", value: "31s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid %s=%s accepted", test.key, test.value)
			}
		})
	}
}

func TestLoadAuthKeyGetBatch(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_AUTH_KEY_GET_BATCH_MAX", "97")
	t.Setenv("TELESRV_AUTH_KEY_GET_BATCH_WAIT", "425us")
	t.Setenv("TELESRV_AUTH_KEY_GET_BATCH_QUEUE", "997")
	t.Setenv("TELESRV_AUTH_KEY_GET_BATCH_TIMEOUT", "4s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuthKeyGetBatchMax != 97 || cfg.AuthKeyGetBatchWait != 425*time.Microsecond ||
		cfg.AuthKeyGetBatchQueue != 997 || cfg.AuthKeyGetBatchTimeout != 4*time.Second {
		t.Fatalf("auth-key get batch = %d/%v/%d/%v",
			cfg.AuthKeyGetBatchMax, cfg.AuthKeyGetBatchWait,
			cfg.AuthKeyGetBatchQueue, cfg.AuthKeyGetBatchTimeout)
	}
}

func TestLoadRejectsInvalidAuthKeyGetBatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero max", key: "TELESRV_AUTH_KEY_GET_BATCH_MAX", value: "0"},
		{name: "wait too large", key: "TELESRV_AUTH_KEY_GET_BATCH_WAIT", value: "11ms"},
		{name: "queue below max", key: "TELESRV_AUTH_KEY_GET_BATCH_QUEUE", value: "1"},
		{name: "timeout too large", key: "TELESRV_AUTH_KEY_GET_BATCH_TIMEOUT", value: "31s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid %s=%s accepted", test.key, test.value)
			}
		})
	}
}

func TestLoadContactReverseBatch(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_CONTACT_REVERSE_BATCH_MAX_PAIRS", "3073")
	t.Setenv("TELESRV_CONTACT_REVERSE_BATCH_WAIT", "375us")
	t.Setenv("TELESRV_CONTACT_REVERSE_BATCH_QUEUE", "778")
	t.Setenv("TELESRV_CONTACT_REVERSE_BATCH_TIMEOUT", "4s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ContactReverseBatchMaxPairs != 3073 || cfg.ContactReverseBatchWait != 375*time.Microsecond ||
		cfg.ContactReverseBatchQueue != 778 || cfg.ContactReverseBatchTimeout != 4*time.Second {
		t.Fatalf("contact reverse batch = %d/%v/%d/%v",
			cfg.ContactReverseBatchMaxPairs, cfg.ContactReverseBatchWait,
			cfg.ContactReverseBatchQueue, cfg.ContactReverseBatchTimeout)
	}
}

func TestLoadRejectsInvalidContactReverseBatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero max pairs", key: "TELESRV_CONTACT_REVERSE_BATCH_MAX_PAIRS", value: "0"},
		{name: "wait too large", key: "TELESRV_CONTACT_REVERSE_BATCH_WAIT", value: "11ms"},
		{name: "zero queue", key: "TELESRV_CONTACT_REVERSE_BATCH_QUEUE", value: "0"},
		{name: "timeout too large", key: "TELESRV_CONTACT_REVERSE_BATCH_TIMEOUT", value: "31s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid %s=%s accepted", test.key, test.value)
			}
		})
	}
}

func TestLoadRejectsInvalidLayerAdvanceBatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero max", key: "TELESRV_LAYER_ADVANCE_BATCH_MAX", value: "0"},
		{name: "wait too large", key: "TELESRV_LAYER_ADVANCE_BATCH_WAIT", value: "11ms"},
		{name: "queue below max", key: "TELESRV_LAYER_ADVANCE_BATCH_QUEUE", value: "1"},
		{name: "timeout too large", key: "TELESRV_LAYER_ADVANCE_BATCH_TIMEOUT", value: "31s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid %s=%s accepted", test.key, test.value)
			}
		})
	}
}

func TestLoadBootstrapReadyBatch(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_BOOTSTRAP_READY_BATCH_MAX", "41")
	t.Setenv("TELESRV_BOOTSTRAP_READY_BATCH_WAIT", "37ms")
	t.Setenv("TELESRV_BOOTSTRAP_READY_BATCH_QUEUE", "917")
	t.Setenv("TELESRV_BOOTSTRAP_READY_BATCH_TIMEOUT", "4s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BootstrapReadyBatchMax != 41 || cfg.BootstrapReadyBatchWait != 37*time.Millisecond ||
		cfg.BootstrapReadyBatchQueue != 917 || cfg.BootstrapReadyBatchTimeout != 4*time.Second {
		t.Fatalf("bootstrap readiness batch = %d/%v/%d/%v",
			cfg.BootstrapReadyBatchMax, cfg.BootstrapReadyBatchWait,
			cfg.BootstrapReadyBatchQueue, cfg.BootstrapReadyBatchTimeout)
	}
}

func TestLoadRejectsInvalidBootstrapReadyBatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero max", key: "TELESRV_BOOTSTRAP_READY_BATCH_MAX", value: "0"},
		{name: "wait too large", key: "TELESRV_BOOTSTRAP_READY_BATCH_WAIT", value: "1001ms"},
		{name: "queue below max", key: "TELESRV_BOOTSTRAP_READY_BATCH_QUEUE", value: "1"},
		{name: "timeout too large", key: "TELESRV_BOOTSTRAP_READY_BATCH_TIMEOUT", value: "31s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid %s=%s accepted", test.key, test.value)
			}
		})
	}
}

func TestLoadPresenceLastSeenBatch(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_PRESENCE_LAST_SEEN_BATCH_MAX", "73")
	t.Setenv("TELESRV_PRESENCE_LAST_SEEN_BATCH_WAIT", "12ms")
	t.Setenv("TELESRV_PRESENCE_LAST_SEEN_BATCH_QUEUE", "901")
	t.Setenv("TELESRV_PRESENCE_LAST_SEEN_BATCH_TIMEOUT", "3s")
	t.Setenv("TELESRV_PRESENCE_LAST_SEEN_DRAIN_TIMEOUT", "17s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PresenceLastSeenBatchMax != 73 || cfg.PresenceLastSeenBatchWait != 12*time.Millisecond ||
		cfg.PresenceLastSeenBatchQueue != 901 || cfg.PresenceLastSeenBatchTimeout != 3*time.Second ||
		cfg.PresenceLastSeenDrainTimeout != 17*time.Second {
		t.Fatalf("presence last-seen batch = %d/%v/%d/%v/%v",
			cfg.PresenceLastSeenBatchMax, cfg.PresenceLastSeenBatchWait,
			cfg.PresenceLastSeenBatchQueue, cfg.PresenceLastSeenBatchTimeout,
			cfg.PresenceLastSeenDrainTimeout)
	}
}

func TestLoadRejectsInvalidPresenceLastSeenBatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "zero max", key: "TELESRV_PRESENCE_LAST_SEEN_BATCH_MAX", value: "0"},
		{name: "wait too large", key: "TELESRV_PRESENCE_LAST_SEEN_BATCH_WAIT", value: "6s"},
		{name: "queue below max", key: "TELESRV_PRESENCE_LAST_SEEN_BATCH_QUEUE", value: "1"},
		{name: "timeout too large", key: "TELESRV_PRESENCE_LAST_SEEN_BATCH_TIMEOUT", value: "31s"},
		{name: "drain too large", key: "TELESRV_PRESENCE_LAST_SEEN_DRAIN_TIMEOUT", value: "61s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("invalid %s=%s accepted", test.key, test.value)
			}
		})
	}
}

func TestLoadS3BlobStorageConfig(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_BLOB_BACKEND", "s3")
	t.Setenv("TELESRV_BLOB_STAGING_DIR", `D:\staging\telesrv`)
	t.Setenv("TELESRV_S3_ENDPOINT", "minio.example.test:9000")
	t.Setenv("TELESRV_S3_BUCKET", "telesrv-media")
	t.Setenv("TELESRV_S3_ACCESS_KEY_ID", "access")
	t.Setenv("TELESRV_S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("TELESRV_S3_USE_SSL", "false")
	t.Setenv("TELESRV_S3_PATH_STYLE", "true")
	t.Setenv("TELESRV_S3_CREATE_BUCKET", "true")

	cfg, err := Load()
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
			disableDefaultConfigFile(t)
			t.Setenv("TELESRV_BLOB_BACKEND", tt.backend)
			t.Setenv("TELESRV_S3_ENDPOINT", tt.endpoint)
			t.Setenv("TELESRV_S3_BUCKET", tt.bucket)
			t.Setenv("TELESRV_S3_ACCESS_KEY_ID", tt.access)
			t.Setenv("TELESRV_S3_SECRET_ACCESS_KEY", tt.secret)
			if _, err := Load(); err == nil {
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
			disableDefaultConfigFile(t)
			t.Setenv(item.key, item.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", item.key, item.value)
			}
		})
	}
}

func TestLoadUpdateServiceConfig(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_UPDATE_PUBLIC_URL", "https://updates.example.test/root/")
	t.Setenv("TELESRV_UPDATE_SERVICE_URL", "http://127.0.0.1:2402/")
	t.Setenv("TELESRV_UPDATE_REQUEST_TIMEOUT", "3s")

	cfg, err := Load()
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
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_UPDATE_PUBLIC_URL", "https://updates.example.test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UpdateServiceURL != cfg.UpdatePublicURL {
		t.Fatalf("UpdateServiceURL = %q, want %q", cfg.UpdateServiceURL, cfg.UpdatePublicURL)
	}
}

func TestLoadRejectsInvalidUpdateServiceConfig(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_UPDATE_PUBLIC_URL", "file:///updates")
	if _, err := Load(); err == nil {
		t.Fatal("invalid update public URL accepted")
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

func TestLoadPremiumBotAndPlans(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_PREMIUM_BOT_USERNAME", "@premium_store_bot")
	t.Setenv("TELESRV_PREMIUM_BOT_USER_ID", "1250000999")
	t.Setenv("TELESRV_PREMIUM_PLANS", "1:30:250,12:365:2400")

	cfg, err := Load()
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
	disableDefaultConfigFile(t)
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

	cfg, err := Load()
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
			disableDefaultConfigFile(t)
			t.Setenv(item.key, item.value)
			if _, err := Load(); err == nil {
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
			disableDefaultConfigFile(t)
			t.Setenv("TELESRV_PREMIUM_PLANS", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted TELESRV_PREMIUM_PLANS=%q", value)
			}
		})
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

func TestLoadCanonicalizesAdvertiseIP(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_ADVERTISE_IP", " 2001:0db8::1 ")

	cfg, err := Load()
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
			disableDefaultConfigFile(t)
			t.Setenv("TELESRV_ADVERTISE_IP", value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted TELESRV_ADVERTISE_IP=%q", value)
			}
		})
	}
}

func TestLoadDefaultCountryCode(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		disableDefaultConfigFile(t)
		t.Setenv("TELESRV_DEFAULT_COUNTRY_CODE", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.DefaultCountryCode != "CN" {
			t.Fatalf("DefaultCountryCode = %q, want CN", cfg.DefaultCountryCode)
		}
	})

	t.Run("normalized override", func(t *testing.T) {
		disableDefaultConfigFile(t)
		t.Setenv("TELESRV_DEFAULT_COUNTRY_CODE", " us ")

		cfg, err := Load()
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
			disableDefaultConfigFile(t)
			t.Setenv("TELESRV_DEFAULT_COUNTRY_CODE", value)

			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted TELESRV_DEFAULT_COUNTRY_CODE=%q", value)
			}
		})
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

func TestLoadRejectsNonPositiveCanonicalDC(t *testing.T) {
	for _, value := range []string{"0", "-2", "2147483648"} {
		t.Run(value, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv("TELESRV_DC", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted TELESRV_DC=%s", value)
			}
		})
	}
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
	t.Setenv("TELESRV_MTPROTO_RPC_DELIVERY_HOOK_WORKERS", "17")
	t.Setenv("TELESRV_MTPROTO_RPC_DELIVERY_HOOK_MAX_PENDING", "777")
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
	t.Setenv("TELESRV_ORPHAN_AUTH_KEY_RETENTION", "36h")
	t.Setenv("TELESRV_CONTACT_SNAPSHOT_CACHE_MAX_VIEWERS", "1234")
	t.Setenv("TELESRV_PROFILE_PHOTO_CACHE_MAX", "2345")
	t.Setenv("TELESRV_PROFILE_PHOTO_CACHE_TTL", "36h")

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
	if cfg.MTProtoRPCDeliveryHookWorkers != 17 || cfg.MTProtoRPCDeliveryHookMaxPending != 777 {
		t.Fatalf("rpc delivery hook config = %d/%d", cfg.MTProtoRPCDeliveryHookWorkers, cfg.MTProtoRPCDeliveryHookMaxPending)
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
	if cfg.TempKeyResolveCacheMaxEntries != 666 || cfg.TempKeyResolveCacheTTL != 17*time.Minute || cfg.OrphanAuthKeyRetention != 36*time.Hour {
		t.Fatalf("auth key resource config = %d/%v/%v", cfg.TempKeyResolveCacheMaxEntries, cfg.TempKeyResolveCacheTTL, cfg.OrphanAuthKeyRetention)
	}
	if cfg.ContactSnapshotCacheMaxViewers != 1234 {
		t.Fatalf("contact snapshot cache viewers = %d", cfg.ContactSnapshotCacheMaxViewers)
	}
	if cfg.ProfilePhotoCacheMaxEntries != 2345 || cfg.ProfilePhotoCacheTTL != 36*time.Hour {
		t.Fatalf("profile photo cache = %d/%v", cfg.ProfilePhotoCacheMaxEntries, cfg.ProfilePhotoCacheTTL)
	}
}

func TestLoadRPCExecutionFairBudgetDefaults(t *testing.T) {
	disableDefaultConfigFile(t)
	cfg, err := Load()
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
	if cfg.MTProtoRPCDeliveryHookWorkers != 32 || cfg.MTProtoRPCDeliveryHookMaxPending != 16_384 {
		t.Fatalf("rpc delivery hook defaults = %d/%d", cfg.MTProtoRPCDeliveryHookWorkers, cfg.MTProtoRPCDeliveryHookMaxPending)
	}
	if cfg.ContactSnapshotCacheMaxViewers != 16_384 {
		t.Fatalf("contact snapshot cache default = %d", cfg.ContactSnapshotCacheMaxViewers)
	}
	if cfg.ProfilePhotoCacheMaxEntries != 200_000 || cfg.ProfilePhotoCacheTTL != 24*time.Hour {
		t.Fatalf("profile photo cache defaults = %d/%v", cfg.ProfilePhotoCacheMaxEntries, cfg.ProfilePhotoCacheTTL)
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
		{name: "delivery workers", key: "TELESRV_MTPROTO_RPC_DELIVERY_HOOK_WORKERS", value: "0"},
		{name: "delivery pending hierarchy", key: "TELESRV_MTPROTO_RPC_DELIVERY_HOOK_MAX_PENDING", value: "16"},
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

func TestLoadRejectsNonPositiveContactSnapshotCache(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		t.Run(value, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv("TELESRV_CONTACT_SNAPSHOT_CACHE_MAX_VIEWERS", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted contact snapshot cache capacity %s", value)
			}
		})
	}
}

func TestLoadRejectsInvalidProfilePhotoCache(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "TELESRV_PROFILE_PHOTO_CACHE_MAX", value: "0"},
		{key: "TELESRV_PROFILE_PHOTO_CACHE_MAX", value: "-1"},
		{key: "TELESRV_PROFILE_PHOTO_CACHE_TTL", value: "0s"},
		{key: "TELESRV_PROFILE_PHOTO_CACHE_TTL", value: "169h"},
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
		{name: "profile photo entries malformed", key: "TELESRV_PROFILE_PHOTO_CACHE_MAX", value: "many"},
	} {
		t.Run(test.name, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted %s=%q", test.key, test.value)
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

func TestLoadStorySparseProjectionCacheConfig(t *testing.T) {
	t.Setenv("TELESRV_CONFIG", "")
	t.Setenv("TELESRV_STORY_ACTIVE_PEER_CACHE_MAX", "1234")
	t.Setenv("TELESRV_STORY_HIDDEN_LIST_CACHE_MAX", "2345")
	t.Setenv("TELESRV_STORY_HIDDEN_LIST_CACHE_BYTES_MAX", "3456")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StoryActivePeerCacheMaxEntries != 1234 || cfg.StoryHiddenListCacheMaxEntries != 2345 || cfg.StoryHiddenListCacheMaxBytes != 3456 {
		t.Fatalf("story sparse cache config = %d/%d/%d, want 1234/2345/3456",
			cfg.StoryActivePeerCacheMaxEntries, cfg.StoryHiddenListCacheMaxEntries, cfg.StoryHiddenListCacheMaxBytes)
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
TELESRV_BRAND_PRODUCT_NAME=File Chat
TELESRV_BRAND_PRODUCT_USERNAME=@File_Chat
TELESRV_BRAND_DESKTOP_APP_NAME=File Workstation
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
	if cfg.Branding.ProductName != "File Chat" || cfg.Branding.ProductUsername != "file_chat" ||
		cfg.Branding.DesktopAppName != "File Workstation" || cfg.Branding.PublicBaseURL != cfg.PublicBaseURL {
		t.Fatalf("Branding = %+v", cfg.Branding)
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

func TestLoadCustomFragmentPublicBaseURL(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_PUBLIC_BASE_URL", "https://links.example.test")
	t.Setenv("TELESRV_CUSTOM_FRAGMENT_PUBLIC_BASE_URL", "https://fragment.example.test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CustomFragmentPublicBaseURL != "https://fragment.example.test" {
		t.Fatalf("CustomFragmentPublicBaseURL = %q", cfg.CustomFragmentPublicBaseURL)
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
func TestLoadGeoIPEndpointsPreservesFailoverOrder(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_GEOIP_ENDPOINTS", "https://a.example/json/{ip}, https://b.example/json/{ip},https://c.example/json/{ip}")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
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

func TestLoadBotVerificationDefaults(t *testing.T) {
	disableDefaultConfigFile(t)

	cfg, err := Load()
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
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_BOT_VERIFICATION_ENABLED", "false")
	t.Setenv("TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER", "0")
	t.Setenv("TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT", "0")
	t.Setenv("TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW", "0s")

	cfg, err := Load()
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
			disableDefaultConfigFile(t)
			t.Setenv(test.key, test.value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load accepted invalid %s=%s", test.key, test.value)
			}
		})
	}
}

// TestLoadValidatesBotVerificationWhileDisabled pins that the policy is checked
// even with the feature off, so switching it on later is not the moment a typo is
// discovered.
func TestLoadValidatesBotVerificationWhileDisabled(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_BOT_VERIFICATION_ENABLED", "false")
	t.Setenv("TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER", "-3")

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted a negative per-verifier bound while the feature was disabled")
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
