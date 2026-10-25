package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"telesrv/internal/domain"
)

func TestLoadEdgeReadsRoleYAMLWithoutProcessEnvOverride(t *testing.T) {
	path := writeRoleYAML(t, "edge.yaml", `
version: 1
instance:
  id: edge-test
public:
  dc: 2
  advertise:
    ip: 10.1.2.3
    port: 2398
redis:
  addr: 127.0.0.1:6399
edge:
  listen: 0.0.0.0:2398
  rsa_key: data/test-rsa.pem
  websocket:
    enabled: false
    allowed_origins:
      - http://localhost:1234
  mtproto:
    outbound_critical_global_max_bytes: 7654321
  location:
    ttl: 2m
    heartbeat_interval: 30s
core_exec:
  resolver: static
  targets:
    - 127.0.0.1:2440
    - 127.0.0.1:2441
  token: ${TEST_CORE_TOKEN}
file_data:
  resolver: dns
  targets:
    - telesrv-file.default.svc.cluster.local:2520
  token: ${TEST_FILE_TOKEN}
  timeout: 11s
egress:
  delivery:
    resolver: static
    targets:
      - 127.0.0.1:2510
    token: ${TEST_EGRESS_TOKEN}
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TEST_CORE_TOKEN", "core-secret")
	t.Setenv("TEST_FILE_TOKEN", "file-secret")
	t.Setenv("TEST_EGRESS_TOKEN", "egress-secret")
	t.Setenv("TELESRV_CORE_EXEC_TOKEN", "must-not-win")

	cfg, err := LoadEdge()
	if err != nil {
		t.Fatalf("LoadEdge: %v", err)
	}
	if cfg.InstanceID != "edge-test" || cfg.ListenAddr != "0.0.0.0:2398" {
		t.Fatalf("unexpected edge identity/listen: instance=%q listen=%q", cfg.InstanceID, cfg.ListenAddr)
	}
	if cfg.CoreExecToken != "core-secret" {
		t.Fatalf("CoreExecToken = %q, want YAML-expanded secret", cfg.CoreExecToken)
	}
	if cfg.FileGRPCResolver != "dns" || cfg.FileGRPCTargets != "telesrv-file.default.svc.cluster.local:2520" {
		t.Fatalf("file grpc resolver/targets = %q/%q", cfg.FileGRPCResolver, cfg.FileGRPCTargets)
	}
	if cfg.WebSocketEnable {
		t.Fatalf("WebSocketEnable = true, want false from YAML")
	}
	if cfg.MTProtoOutboundCriticalGlobalMaxBytes != 7654321 {
		t.Fatalf("critical outbound bytes = %d, want 7654321", cfg.MTProtoOutboundCriticalGlobalMaxBytes)
	}
}

func TestLoadRoleYAMLRejectsUnknownFields(t *testing.T) {
	path := writeRoleYAML(t, "edge-bad.yaml", `
version: 1
edge:
  listen: 0.0.0.0:2398
  typo_field: true
`)
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadEdge(); err == nil || !strings.Contains(err.Error(), "typo_field") {
		t.Fatalf("LoadEdge err = %v, want unknown field rejection", err)
	}
}

func TestEveryRoleRequiresYAMLVersionOne(t *testing.T) {
	tests := []struct {
		name string
		load func() error
	}{
		{name: "edge", load: func() error { _, err := LoadEdge(); return err }},
		{name: "core", load: func() error { _, err := LoadCore(); return err }},
		{name: "egress", load: func() error { _, err := LoadEgress(); return err }},
		{name: "file", load: func() error { _, err := LoadFile(); return err }},
		{name: "sfu", load: func() error { _, err := LoadSFU(); return err }},
		{name: "admin", load: func() error { _, err := LoadAdmin(); return err }},
		{name: "ton", load: func() error { _, err := LoadTON(); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeRoleYAML(t, tt.name+".yaml", "version: 2")
			t.Setenv("TELESRV_CONFIG", path)
			if err := tt.load(); err == nil || !strings.Contains(err.Error(), "version must be 1") {
				t.Fatalf("load err = %v, want version rejection", err)
			}
		})
	}
}

func TestLoadCoreRejectsFileStorageConfiguration(t *testing.T) {
	path := writeRoleYAML(t, "core-with-file-storage.yaml", `
version: 1
storage:
  blob_backend: s3
`)
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadCore(); err == nil || !strings.Contains(err.Error(), "field storage not found") {
		t.Fatalf("LoadCore err = %v, want strict rejection of File-owned storage config", err)
	}
}

func TestLoadNonCoreRejectsBrandingConfiguration(t *testing.T) {
	path := writeRoleYAML(t, "file-with-core-branding.yaml", `
version: 1
branding:
  product_name: Wrong Owner
`)
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadFile(); err == nil || !strings.Contains(err.Error(), "field branding not found") {
		t.Fatalf("LoadFile err = %v, want strict rejection of Core-owned branding config", err)
	}
}

func TestLoadCoreReadsBrandingYAML(t *testing.T) {
	path := writeRoleYAML(t, "core-branding.yaml", `
version: 1
public:
  base_url: https://chat.example.test
branding:
  product_name: Example Chat
  product_username: example_chat
  desktop_app_name: Example Desktop
  android_app_name: Example Android
  ios_app_name: Example iOS
  macos_app_name: Example macOS
  web_a_app_name: Example Web A
  web_k_app_name: Example Web K
  premium_name: Example Plus
  stars_name: Example Credits
`)
	t.Setenv("TELESRV_CONFIG", path)

	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore: %v", err)
	}
	brand := cfg.Branding
	if brand.ProductName != "Example Chat" || brand.ProductUsername != "example_chat" ||
		brand.DesktopAppName != "Example Desktop" || brand.AndroidAppName != "Example Android" ||
		brand.IOSAppName != "Example iOS" || brand.MacOSAppName != "Example macOS" ||
		brand.WebAAppName != "Example Web A" || brand.WebKAppName != "Example Web K" ||
		brand.PremiumName != "Example Plus" || brand.StarsName != "Example Credits" ||
		brand.PublicBaseURL != "https://chat.example.test" {
		t.Fatalf("Branding = %+v", brand)
	}
	if cfg.PublicAppName != brand.ProductName || cfg.SMTPFromName != brand.ProductName {
		t.Fatalf("brand-derived defaults = public app %q smtp from %q", cfg.PublicAppName, cfg.SMTPFromName)
	}
}

func TestLoadCoreReadsCounterRecoveryPoolSizeFromYAML(t *testing.T) {
	path := writeRoleYAML(t, "core-postgres.yaml", `
version: 1
postgres:
  dsn: postgres://core-test
  max_conns: 11
  min_conns: 3
  counter_recovery_max_conns: 2
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TELESRV_POSTGRES_COUNTER_RECOVERY_MAX_CONNS", "99")

	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore: %v", err)
	}
	if cfg.PostgresDSN != "postgres://core-test" || cfg.PostgresMaxConns != 11 || cfg.PostgresMinConns != 3 || cfg.PostgresCounterRecoveryMaxConns != 2 {
		t.Fatalf("postgres config = dsn:%q max:%d min:%d recovery:%d", cfg.PostgresDSN, cfg.PostgresMaxConns, cfg.PostgresMinConns, cfg.PostgresCounterRecoveryMaxConns)
	}
}

// The 777000 login-code default template is Core YAML only; the Admin
// identity override still takes precedence at delivery time.
func TestLoadCoreReadsLoginCodeMessageTemplateFromYAML(t *testing.T) {
	path := writeRoleYAML(t, "core-auth.yaml", `
version: 1
auth:
  login_code_message_template: "Operator code {{code}} for {{server_name}}"
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TELESRV_LOGIN_CODE_MESSAGE_TEMPLATE", "process env must not apply: {{code}}")

	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore: %v", err)
	}
	if cfg.LoginCodeMessageTemplate != "Operator code {{code}} for {{server_name}}" {
		t.Fatalf("login code template = %q", cfg.LoginCodeMessageTemplate)
	}

	path = writeRoleYAML(t, "core-auth-default.yaml", "version: 1\n")
	t.Setenv("TELESRV_CONFIG", path)
	cfg, err = LoadCore()
	if err != nil {
		t.Fatalf("LoadCore default: %v", err)
	}
	if cfg.LoginCodeMessageTemplate != domain.DefaultLoginCodeMessageTemplate {
		t.Fatalf("default login code template = %q", cfg.LoginCodeMessageTemplate)
	}
	if cfg.CheckNumberFormat {
		t.Fatal("default CheckNumberFormat = true, want lenient policy")
	}
}

func TestLoadCoreReadsCheckNumberFormatFromYAML(t *testing.T) {
	path := writeRoleYAML(t, "core-phone-format.yaml", `
version: 1
auth:
  check_number_format: true
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TELESRV_CHECK_NUMBER_FORMAT", "false")

	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore: %v", err)
	}
	if !cfg.CheckNumberFormat {
		t.Fatal("CheckNumberFormat = false, want YAML auth.check_number_format")
	}
}

func TestLoadCoreReadsSensitiveRateLimitsFromYAML(t *testing.T) {
	path := writeRoleYAML(t, "core-sensitive-rate.yaml", `
version: 1
sensitive_rate_limits:
  srp_limit: 3
  srp_window: 30s
  withdrawal_limit: 0
  payment_limit: 12
  payment_window: 2m
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TELESRV_SRP_RATE_LIMIT", "99")
	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore: %v", err)
	}
	if cfg.SRPRateLimit != 3 || cfg.SRPRateWindow != 30*time.Second || cfg.WithdrawalRateLimit != 0 ||
		cfg.WithdrawalRateWindow != time.Minute || cfg.PaymentRateLimit != 12 || cfg.PaymentRateWindow != 2*time.Minute {
		t.Fatalf("sensitive rate limits = srp %d/%s withdrawal %d/%s payment %d/%s", cfg.SRPRateLimit, cfg.SRPRateWindow,
			cfg.WithdrawalRateLimit, cfg.WithdrawalRateWindow, cfg.PaymentRateLimit, cfg.PaymentRateWindow)
	}
}

func TestLoadCoreReadsStarGiftsYAMLWithoutProcessEnvOverride(t *testing.T) {
	path := writeRoleYAML(t, "core.yaml", `
version: 1
star_gifts:
  ton_starting_grant: 42
  sweep_interval: 17s
  sweep_batch: 23
  transfer_stars: 31
  drop_original_details_stars: 37
  offer_min_stars: 41
  stars_proceeds_permille: 950
  ton_proceeds_permille: 925
  export_delay: 1m
  transfer_delay: 2m
  resell_delay: 3m
  craft_delay: 4m
  craft_chance_permille: 275
  export:
    mode: disabled
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TELESRV_STARGIFT_EXPORT_MODE", StarGiftExportModeTON)
	t.Setenv("TELESRV_STARGIFT_SWEEP_BATCH", "9999")

	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore: %v", err)
	}
	if cfg.StarGiftExportMode != StarGiftExportModeDisabled || cfg.StarGiftTONStartingGrant != 42 ||
		cfg.StarGiftSweepInterval != 17*time.Second || cfg.StarGiftSweepBatch != 23 {
		t.Fatalf("star gift mode/grant/sweep = %q/%d/%s/%d", cfg.StarGiftExportMode, cfg.StarGiftTONStartingGrant, cfg.StarGiftSweepInterval, cfg.StarGiftSweepBatch)
	}
	if cfg.StarGiftTransferStars != 31 || cfg.StarGiftDropOriginalDetailsStars != 37 || cfg.StarGiftOfferMinStars != 41 ||
		cfg.StarGiftStarsProceedsPermille != 950 || cfg.StarGiftTONProceedsPermille != 925 {
		t.Fatalf("star gift fees = transfer:%d drop:%d offer:%d stars:%d ton:%d", cfg.StarGiftTransferStars, cfg.StarGiftDropOriginalDetailsStars, cfg.StarGiftOfferMinStars, cfg.StarGiftStarsProceedsPermille, cfg.StarGiftTONProceedsPermille)
	}
	if cfg.StarGiftExportDelay != time.Minute || cfg.StarGiftTransferDelay != 2*time.Minute ||
		cfg.StarGiftResellDelay != 3*time.Minute || cfg.StarGiftCraftDelay != 4*time.Minute || cfg.StarGiftCraftChancePermille != 275 {
		t.Fatalf("star gift lifecycle = export:%s transfer:%s resell:%s craft:%s chance:%d", cfg.StarGiftExportDelay, cfg.StarGiftTransferDelay, cfg.StarGiftResellDelay, cfg.StarGiftCraftDelay, cfg.StarGiftCraftChancePermille)
	}
}

func TestLoadCoreDefaultsStarGiftExportToLocalAndRejectsInvalidMode(t *testing.T) {
	path := writeRoleYAML(t, "core-default.yaml", "version: 1")
	t.Setenv("TELESRV_CONFIG", path)
	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore default: %v", err)
	}
	if cfg.StarGiftExportMode != StarGiftExportModeLocal {
		t.Fatalf("StarGiftExportMode = %q, want local", cfg.StarGiftExportMode)
	}

	path = writeRoleYAML(t, "core-invalid.yaml", `
version: 1
star_gifts:
  export:
    mode: fragment
`)
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadCore(); err == nil || !strings.Contains(err.Error(), "must be local, ton or disabled") {
		t.Fatalf("LoadCore invalid mode err = %v", err)
	}
}

func TestLoadCoreRequiresTONExportAdmissionOnlyInTONMode(t *testing.T) {
	path := writeRoleYAML(t, "core-ton.yaml", `
version: 1
star_gifts:
  export:
    mode: ton
    ton:
      network: mainnet
      collection_address: 0:collection
      collection_code_hash: collection-code-hash
      mint_abi: basic-collection-v1
      initial_item_index: "17"
      proof_domain: gifts.example
      capability_secret_file: secrets/ton-capability.key
      allow_user_ids: [29, 31]
      ttl: 45m
      challenge_ttl: 3m
      finalizer:
        batch: 7
        poll_interval: 4s
        lease_timeout: 40s
        request_timeout: 12s
        retry_delay: 6s
      claim:
        enabled: true
        bot_token_file: secrets/ton-claim-bot.token
        init_data_ttl: 20m
`)
	t.Setenv("TELESRV_CONFIG", path)
	cfg, err := LoadCore()
	if err != nil {
		t.Fatalf("LoadCore TON: %v", err)
	}
	if cfg.StarGiftTONNetwork != TONNetworkMainnet || cfg.StarGiftTONCollectionCodeHash != "collection-code-hash" ||
		cfg.StarGiftTONMintABI != "basic-collection-v1" || cfg.StarGiftTONInitialItemIndex != "17" ||
		cfg.StarGiftTONExportTTL != 45*time.Minute || cfg.StarGiftTONChallengeTTL != 3*time.Minute ||
		cfg.StarGiftTONFinalizerBatch != 7 || cfg.StarGiftTONFinalizerPollInterval != 4*time.Second ||
		cfg.StarGiftTONFinalizerLeaseTimeout != 40*time.Second || cfg.StarGiftTONFinalizerRequestTimeout != 12*time.Second ||
		cfg.StarGiftTONFinalizerRetryDelay != 6*time.Second || !cfg.StarGiftTONClaimEnabled ||
		cfg.StarGiftTONClaimBotTokenFile != "secrets/ton-claim-bot.token" || cfg.StarGiftTONClaimInitDataTTL != 20*time.Minute ||
		!reflect.DeepEqual(cfg.StarGiftTONAllowUserIDs, []int64{29, 31}) {
		t.Fatalf("TON export config = network:%q index:%q ttl:%s/%s", cfg.StarGiftTONNetwork,
			cfg.StarGiftTONInitialItemIndex, cfg.StarGiftTONExportTTL, cfg.StarGiftTONChallengeTTL)
	}

	path = writeRoleYAML(t, "core-ton-missing.yaml", `
version: 1
star_gifts:
  export:
    mode: ton
`)
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadCore(); err == nil || !strings.Contains(err.Error(), "capability secret") {
		t.Fatalf("LoadCore incomplete TON err = %v", err)
	}

	path = writeRoleYAML(t, "core-ton-invalid-allowlist.yaml", `
version: 1
star_gifts:
  export:
    ton:
      allow_user_ids: [7, 0]
`)
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadCore(); err == nil || !strings.Contains(err.Error(), "positive user IDs") {
		t.Fatalf("LoadCore invalid TON allowlist err = %v", err)
	}
}

func TestLoadTONReadsStrictYAMLAndDefaultsMintOff(t *testing.T) {
	path := writeRoleYAML(t, "ton.yaml", `
version: 1
instance:
  id: ton-test-a
postgres:
  dsn: ${TEST_TON_POSTGRES_DSN}
ton:
  network: mainnet
  lite_servers:
    - address: 1.2.3.4:19949
      public_key: lite-server-key
  trusted_block:
    workchain: -1
    shard: -9223372036854775808
    seqno: 100
    root_hash: root-hash
    file_hash: file-hash
  collection:
    address: EQCollection
    code_hash: collection-code-hash
    mint_abi: basic-collection-v1
    initial_item_index: "17"
  relayer:
    wallet_version: v4r2
  worker:
    poll_interval: 3s
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TEST_TON_POSTGRES_DSN", "postgres://ton")
	t.Setenv("TELESRV_POSTGRES_DSN", "must-not-win")

	cfg, err := LoadTON()
	if err != nil {
		t.Fatalf("LoadTON: %v", err)
	}
	if cfg.PostgresDSN != "postgres://ton" || cfg.InstanceID != "ton-test-a" || cfg.Network != TONNetworkMainnet ||
		cfg.MintABI != "basic-collection-v1" || cfg.InitialItemIndex != "17" {
		t.Fatalf("unexpected TON identity: dsn=%q instance=%q network=%q", cfg.PostgresDSN, cfg.InstanceID, cfg.Network)
	}
	if cfg.MintEnabled || cfg.RelayerMnemonicFile != "" {
		t.Fatalf("mint defaults = enabled:%v mnemonic:%q, want reconciliation-only", cfg.MintEnabled, cfg.RelayerMnemonicFile)
	}
	if cfg.WorkerBatch != 10 || cfg.PollInterval != 3*time.Second || cfg.LeaseTimeout != 2*time.Minute || cfg.RequestTimeout != 90*time.Second {
		t.Fatalf("worker defaults = batch:%d poll:%s lease:%s request:%s", cfg.WorkerBatch, cfg.PollInterval, cfg.LeaseTimeout, cfg.RequestTimeout)
	}
}

func TestLoadTONRejectsUnknownAndUnsafeConfig(t *testing.T) {
	base := `
version: 1
instance:
  id: ton-test-a
postgres:
  dsn: postgres://ton
ton:
  network: mainnet
  lite_servers:
    - address: 1.2.3.4:19949
      public_key: lite-server-key
  trusted_block:
    workchain: -1
    shard: -9223372036854775808
    seqno: 100
    root_hash: root-hash
    file_hash: file-hash
  collection:
    address: EQCollection
    code_hash: collection-code-hash
    mint_abi: basic-collection-v1
    initial_item_index: "17"
  relayer:
    wallet_version: v4r2
`

	path := writeRoleYAML(t, "ton-unknown.yaml", base+"  typo_field: true\n")
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadTON(); err == nil || !strings.Contains(err.Error(), "typo_field") {
		t.Fatalf("LoadTON unknown field err = %v", err)
	}

	path = writeRoleYAML(t, "ton-unsupported-abi.yaml", strings.Replace(base,
		"mint_abi: basic-collection-v1", "mint_abi: custom-v1", 1))
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadTON(); err == nil || !strings.Contains(err.Error(), "basic-collection-v1") {
		t.Fatalf("LoadTON unsupported mint ABI err = %v", err)
	}

	path = writeRoleYAML(t, "ton-enabled.yaml", base+`  mint:
    enabled: true
`)
	t.Setenv("TELESRV_CONFIG", path)
	if _, err := LoadTON(); err == nil || !strings.Contains(err.Error(), "mnemonic_file") {
		t.Fatalf("LoadTON enabled without mnemonic err = %v", err)
	}
}

func TestLoadFileReadsStorageAndUploadYAML(t *testing.T) {
	path := writeRoleYAML(t, "file.yaml", `
version: 1
public:
  dc: 2
postgres:
  dsn: postgres://file:file@127.0.0.1:5432/file?sslmode=disable
file_data:
  addr: 127.0.0.1:2520
  token: ${TEST_FILE_TOKEN}
storage:
  blob_backend: localfs
  blob_dir: data/test-blobs
  low_space_guard_enabled: false
  usage_refresh_interval: 2m
upload:
  part_ttl: 24h
  part_gc_interval: 5m
  part_gc_batch: 50
  in_flight_max_bytes: 1048576
media:
  external_media_enable: false
  web_page_preview_enable: false
`)
	t.Setenv("TELESRV_CONFIG", path)
	t.Setenv("TEST_FILE_TOKEN", "file-secret")

	cfg, err := LoadFile()
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.FileGRPCAddr != "127.0.0.1:2520" || cfg.FileToken != "file-secret" {
		t.Fatalf("file grpc = %q token=%q", cfg.FileGRPCAddr, cfg.FileToken)
	}
	if cfg.BlobDir != "data/test-blobs" || cfg.StorageLowSpaceGuardEnable {
		t.Fatalf("storage blob_dir=%q guard=%v", cfg.BlobDir, cfg.StorageLowSpaceGuardEnable)
	}
	if cfg.UploadPartGCBatch != 50 || cfg.UploadInFlightMaxBytes != 1048576 {
		t.Fatalf("upload batch=%d bytes=%d", cfg.UploadPartGCBatch, cfg.UploadInFlightMaxBytes)
	}
}

func TestLoadFileReadsCompleteS3YAML(t *testing.T) {
	path := writeRoleYAML(t, "file-s3.yaml", `
version: 1
storage:
  blob_backend: s3
  blob_staging_dir: data/blob-staging
  s3:
    endpoint: minio.example.test:9000
    region: eu-west-1
    bucket: telesrv-media
    access_key_id: access
    secret_access_key: secret
    use_ssl: false
    path_style: true
    create_bucket: true
  low_space_guard_enabled: true
  min_free_bytes: 4096
  max_total_bytes: 8192
  usage_refresh_interval: 17s
`)
	t.Setenv("TELESRV_CONFIG", path)

	cfg, err := LoadFile()
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg.BlobBackendKind != "s3" || cfg.BlobStagingDir != "data/blob-staging" ||
		cfg.S3Endpoint != "minio.example.test:9000" || cfg.S3Region != "eu-west-1" ||
		cfg.S3Bucket != "telesrv-media" || cfg.S3AccessKeyID != "access" || cfg.S3SecretAccessKey != "secret" ||
		cfg.S3UseSSL || !cfg.S3PathStyle || !cfg.S3CreateBucket {
		t.Fatal("S3 config was not preserved")
	}
	if !cfg.StorageLowSpaceGuardEnable || cfg.StorageMinFreeBytes != 4096 || cfg.StorageMaxTotalBytes != 8192 || cfg.StorageUsageRefreshInterval != 17*time.Second {
		t.Fatalf("S3 capacity config = enabled:%v min:%d max:%d interval:%s", cfg.StorageLowSpaceGuardEnable, cfg.StorageMinFreeBytes, cfg.StorageMaxTotalBytes, cfg.StorageUsageRefreshInterval)
	}
}

func TestLoadExampleRoleYAMLFiles(t *testing.T) {
	t.Setenv("TELESRV_CORE_EXEC_TOKEN", "core-secret")
	t.Setenv("TELESRV_FILE_TOKEN", "file-secret")
	t.Setenv("TELESRV_EGRESS_DELIVERY_TOKEN", "egress-secret")
	t.Setenv("TELESRV_GROUPCALL_CONTROL_TOKEN", "groupcall-secret")
	t.Setenv("TELESRV_SFU_CONTROL_TOKEN", "sfu-secret")
	t.Setenv("TELESRV_POSTGRES_DSN", "postgres://telesrv:telesrv@127.0.0.1:5432/telesrv?sslmode=disable")
	t.Setenv("TELESRV_ADMIN_UI_PASSWORD", "admin-password")
	t.Setenv("TELESRV_ADMIN_UI_TOKEN", "")
	t.Setenv("TELESRV_ADMIN_SESSION_KEY", "admin-session-secret")
	t.Setenv("TELESRV_ADMIN_API_TOKEN", "admin-api-secret")
	t.Setenv("TELESRV_TON_LITESERVER_PUBLIC_KEY", "test-lite-server-key")
	t.Setenv("TELESRV_TON_COLLECTION_ADDRESS", "EQTestCollection")
	t.Setenv("TELESRV_TON_COLLECTION_CODE_HASH", "test-collection-code-hash")
	t.Setenv("TELESRV_TON_TRUSTED_BLOCK_SEQNO", "1")
	t.Setenv("TELESRV_TON_TRUSTED_BLOCK_ROOT_HASH", strings.Repeat("00", 32))
	t.Setenv("TELESRV_TON_TRUSTED_BLOCK_FILE_HASH", strings.Repeat("11", 32))
	t.Setenv("TELESRV_PUBLIC_IP", "203.0.113.10")
	t.Setenv("TELESRV_PUBLIC_HOST", "gifts.example")
	t.Setenv("TELESRV_WEB_HOST", "web.gifts.example")
	t.Setenv("TELESRV_REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("TELESRV_REDIS_PASSWORD", "redis-secret")
	t.Setenv("TELESRV_TON_INITIAL_ITEM_INDEX", "1000")
	t.Setenv("TELESRV_TON_CAPABILITY_SECRET_FILE", "secrets/ton-capability.key")
	t.Setenv("TELESRV_TON_CLAIM_BOT_TOKEN_FILE", "secrets/ton-claim-bot.token")
	t.Setenv("TELESRV_TON_LITESERVER_1_ADDRESS", "1.2.3.4:19949")
	t.Setenv("TELESRV_TON_LITESERVER_1_PUBLIC_KEY", "mainnet-lite-key-1")
	t.Setenv("TELESRV_TON_LITESERVER_2_ADDRESS", "5.6.7.8:19949")
	t.Setenv("TELESRV_TON_LITESERVER_2_PUBLIC_KEY", "mainnet-lite-key-2")
	t.Setenv("TELESRV_TON_MNEMONIC_FILE", "secrets/ton-relayer.mnemonic")

	exampleDir := filepath.Join("..", "..", "configs", "examples")
	tests := []struct {
		name string
		file string
		load func() error
	}{
		{name: "edge", file: "edge.local.yaml", load: func() error { _, err := LoadEdge(); return err }},
		{name: "core", file: "core.local.yaml", load: func() error { _, err := LoadCore(); return err }},
		{name: "egress", file: "egress.local.yaml", load: func() error { _, err := LoadEgress(); return err }},
		{name: "file", file: "file.local.yaml", load: func() error { _, err := LoadFile(); return err }},
		{name: "sfu", file: "sfu.local.yaml", load: func() error { _, err := LoadSFU(); return err }},
		{name: "admin", file: "admin.local.yaml", load: func() error { _, err := LoadAdmin(); return err }},
		{name: "ton", file: "ton.local.yaml", load: func() error { _, err := LoadTON(); return err }},
		{name: "core-ton-mainnet", file: "core.ton-mainnet.yaml", load: func() error { _, err := LoadCore(); return err }},
		{name: "ton-mainnet", file: "ton.mainnet.yaml", load: func() error { _, err := LoadTON(); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TELESRV_CONFIG", filepath.Join(exampleDir, tt.file))
			if err := tt.load(); err != nil {
				t.Fatalf("load %s: %v", tt.file, err)
			}
		})
	}
}

func TestLoadDockerRoleYAMLFiles(t *testing.T) {
	t.Setenv("TELESRV_INSTANCE_ID", "compose-test-1")
	t.Setenv("TELESRV_POSTGRES_DSN", "postgres://telesrv:secret@postgres:5432/telesrv_v2?sslmode=disable")
	t.Setenv("TELESRV_REDIS_PASSWORD", "redis-secret")
	t.Setenv("TELESRV_REDIS_ADDR", "redis:6379")
	t.Setenv("TELESRV_CORE_EXEC_TOKEN", "core-secret")
	t.Setenv("TELESRV_FILE_TOKEN", "file-secret")
	t.Setenv("TELESRV_EGRESS_DELIVERY_TOKEN", "egress-secret")
	t.Setenv("TELESRV_GROUPCALL_CONTROL_TOKEN", "group-call-secret")
	t.Setenv("TELESRV_SFU_CONTROL_TOKEN", "sfu-secret")
	t.Setenv("TELESRV_SFU_CONTROL_GRPC_URL", "grpc://sfu-1:2450")
	t.Setenv("TELESRV_GROUPCALL_CONTROL_URL", "http://core:2420")
	t.Setenv("TELESRV_ADMIN_API_TOKEN", "admin-api-secret")
	t.Setenv("TELESRV_ADMIN_UI_PASSWORD", "admin-password")
	t.Setenv("TELESRV_ADMIN_UI_TOKEN", "")
	t.Setenv("TELESRV_ADMIN_SESSION_KEY", "admin-session-secret")
	t.Setenv("TELESRV_DEV_AUTH_CODE", "918274")
	t.Setenv("TELESRV_PHONE_CODE_DELIVERY_PROVIDER", "development")
	t.Setenv("TELESRV_OTP_WEBHOOK_URL", "https://otp.example.test/v1/deliveries")
	t.Setenv("TELESRV_OTP_WEBHOOK_SECRET", "otp-secret")
	t.Setenv("TELESRV_OTP_WEBHOOK_TIMEOUT", "4s")
	t.Setenv("TELESRV_ADVERTISE_IP", "203.0.113.10")
	t.Setenv("TELESRV_ADVERTISE_PORT", "2398")
	t.Setenv("TELESRV_SFU_UDP_PORT", "12399")
	t.Setenv("TELESRV_SFU_BIND_IP", "0.0.0.0")
	t.Setenv("TELESRV_TURN_ENABLE", "true")
	t.Setenv("TELESRV_TURN_UDP_PORT", "12400")
	t.Setenv("TELESRV_TURN_BIND_IP", "0.0.0.0")
	t.Setenv("TELESRV_TURN_ADVERTISE_IP", "203.0.113.10")
	t.Setenv("TELESRV_TURN_SECRET", "turn-secret")
	t.Setenv("TELESRV_TURN_RELAY_MIN_PORT", "12500")
	t.Setenv("TELESRV_TURN_RELAY_MAX_PORT", "12563")
	t.Setenv("TELESRV_CALL_TURN_CREDENTIAL_TTL", "6h")
	t.Setenv("TELESRV_CALL_FORCE_RELAY", "false")
	t.Setenv("TELESRV_DEFAULT_COUNTRY_CODE", "CN")
	t.Setenv("TELESRV_PUBLIC_BASE_URL", "https://example.test")
	t.Setenv("TELESRV_PUBLIC_APP_SCHEME", "telesrv")
	t.Setenv("TELESRV_PUBLIC_WEB_BASE_URL", "https://web.example.test")
	t.Setenv("TELESRV_BLOB_BACKEND", "s3")
	t.Setenv("TELESRV_EXTERNAL_MEDIA_ENABLE", "true")
	t.Setenv("TELESRV_WEBPAGE_PREVIEW_ENABLE", "true")
	t.Setenv("TELESRV_S3_ENDPOINT", "minio.example.test:9000")
	t.Setenv("TELESRV_S3_REGION", "eu-central-1")
	t.Setenv("TELESRV_S3_BUCKET", "docker-media")
	t.Setenv("TELESRV_S3_ACCESS_KEY_ID", "docker-access")
	t.Setenv("TELESRV_S3_SECRET_ACCESS_KEY", "docker-secret")
	t.Setenv("TELESRV_S3_USE_SSL", "false")
	t.Setenv("TELESRV_S3_PATH_STYLE", "true")
	t.Setenv("TELESRV_S3_CREATE_BUCKET", "true")
	t.Setenv("TELESRV_BRAND_PRODUCT_NAME", "Docker Chat")
	t.Setenv("TELESRV_BRAND_PRODUCT_USERNAME", "docker_chat")
	t.Setenv("TELESRV_BRAND_DESKTOP_APP_NAME", "Docker Desktop")
	t.Setenv("TELESRV_BRAND_ANDROID_APP_NAME", "Docker Android")
	t.Setenv("TELESRV_BRAND_IOS_APP_NAME", "Docker iOS")
	t.Setenv("TELESRV_BRAND_MACOS_APP_NAME", "Docker macOS")
	t.Setenv("TELESRV_BRAND_WEB_A_APP_NAME", "Docker Web A")
	t.Setenv("TELESRV_BRAND_WEB_K_APP_NAME", "Docker Web K")
	t.Setenv("TELESRV_BRAND_PREMIUM_NAME", "Docker Plus")
	t.Setenv("TELESRV_BRAND_STARS_NAME", "Docker Credits")

	dockerConfigDir := filepath.Join("..", "..", "deploy", "docker", "config")
	tests := []struct {
		name string
		file string
		load func() error
	}{
		{name: "edge", file: "edge.yaml", load: func() error {
			cfg, err := LoadEdge()
			if err == nil && (cfg.InstanceID != "compose-test-1" || cfg.CoreExecGRPCTargets != "core:2440" || cfg.CoreExecGRPCResolver != "dns" || cfg.FileGRPCTargets != "file:2520" || cfg.FileGRPCResolver != "dns" || cfg.EgressDeliveryGRPCTargets != "egress:2510" || cfg.EgressDeliveryGRPCResolver != "dns" || cfg.IdentityDir != "/var/lib/telesrv-identity") {
				return fmt.Errorf("unexpected Docker edge identity or service targets")
			}
			return err
		}},
		{name: "core", file: "core.yaml", load: func() error {
			cfg, err := LoadCore()
			if err == nil && (cfg.InstanceID != "compose-test-1" || cfg.CoreExecGRPCAddr != "0.0.0.0:2440" || cfg.FileGRPCTargets != "file:2520" || cfg.FileGRPCResolver != "dns" || cfg.AdminAPIAddr != "0.0.0.0:2599" || cfg.AdminAPIToken != "admin-api-secret" || cfg.BotAPIAddr != "" || cfg.LangPackSeedDir != "/usr/share/telesrv/langpack" || cfg.OTPWebhookURL != "https://otp.example.test/v1/deliveries" || cfg.OTPWebhookSecret != "otp-secret" || cfg.OTPWebhookTimeout != 4*time.Second || cfg.Branding.ProductName != "Docker Chat" || cfg.Branding.StarsName != "Docker Credits") {
				return fmt.Errorf("unexpected Docker core identity, listeners, or seed path")
			}
			return err
		}},
		{name: "admin", file: "admin.yaml", load: func() error {
			cfg, err := LoadAdmin()
			if err == nil && (cfg.Addr != "0.0.0.0:2600" || cfg.PostgresDSN != "postgres://telesrv:secret@postgres:5432/telesrv_v2?sslmode=disable" || cfg.AdminAPIAddr != "core:2599" || cfg.AdminAPIToken != "admin-api-secret" || cfg.Password != "admin-password" || cfg.SessionKey != "admin-session-secret" || cfg.DiskStatsPath != "/var/lib/telesrv-file/blob-staging") {
				return fmt.Errorf("unexpected Docker admin listener, credentials, or storage path")
			}
			return err
		}},
		{name: "egress", file: "egress.yaml", load: func() error {
			cfg, err := LoadEgress()
			if err == nil && (cfg.InstanceID != "compose-test-1" || cfg.EgressDeliveryGRPCAddr != "0.0.0.0:2510" || cfg.DefaultCountryCode != "CN" || cfg.PublicBaseURL != "https://example.test" || cfg.PublicAppScheme != "telesrv" || cfg.DeliveryAttemptTimeout != 2*time.Second || cfg.DeliveryClockSkewAllowance != time.Second) {
				return fmt.Errorf("unexpected Docker egress identity or listener")
			}
			return err
		}},
		{name: "file", file: "file.yaml", load: func() error {
			cfg, err := LoadFile()
			if err == nil && (cfg.InstanceID != "compose-test-1" || cfg.FileGRPCAddr != "0.0.0.0:2520" || cfg.BlobBackendKind != "s3" || cfg.BlobStagingDir != "/var/lib/telesrv-file/blob-staging" || cfg.S3Endpoint != "minio.example.test:9000" || cfg.S3Region != "eu-central-1" || cfg.S3Bucket != "docker-media" || cfg.S3AccessKeyID != "docker-access" || cfg.S3SecretAccessKey != "docker-secret" || cfg.S3UseSSL || !cfg.S3PathStyle || !cfg.S3CreateBucket) {
				return fmt.Errorf("unexpected Docker file identity, listener, or blob path")
			}
			return err
		}},
		{name: "sfu", file: "sfu.yaml", load: func() error {
			cfg, err := LoadSFU()
			if err == nil && (cfg.InstanceID != "compose-test-1" || cfg.SFUControlGRPCAddr != "0.0.0.0:2450" || cfg.SFUControlGRPCURL != "grpc://sfu-1:2450" || cfg.GroupCallControlURL != "http://core:2420" || cfg.SFUBindIP != "0.0.0.0" || !cfg.TURNEnable || cfg.TURNUDPPort != 12400 || cfg.TURNBindIP != "0.0.0.0" || cfg.TURNAdvertiseIP != "203.0.113.10" || cfg.TURNSecret != "turn-secret" || cfg.TURNRelayMinPort != 12500 || cfg.TURNRelayMaxPort != 12563) {
				return fmt.Errorf("unexpected Docker SFU identity or control endpoint")
			}
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TELESRV_CONFIG", filepath.Join(dockerConfigDir, tt.file))
			if err := tt.load(); err != nil {
				t.Fatalf("load Docker %s: %v", tt.file, err)
			}
		})
	}
	// gramsrv #116: core.yaml used to hard-code bot_api_addr: "", so the
	// documented TELESRV_BOT_API_ADDR could never enable the Bot API gateway.
	t.Run("core bot api", func(t *testing.T) {
		t.Setenv("TELESRV_CONFIG", filepath.Join(dockerConfigDir, "core.yaml"))
		t.Setenv("TELESRV_BOT_API_ADDR", "0.0.0.0:8088")
		cfg, err := LoadCore()
		if err != nil {
			t.Fatalf("load Docker core.yaml: %v", err)
		}
		if cfg.BotAPIAddr != "0.0.0.0:8088" {
			t.Fatalf("Docker core BotAPIAddr = %q, want the TELESRV_BOT_API_ADDR value", cfg.BotAPIAddr)
		}
	})
}

// TestDockerBotAPIOverridePublishesCoreGateway pins the opt-in Bot API path:
// the default topology keeps the gateway off, and compose.bot-api.yaml
// publishes core's container port 8088 on the configured host address.
func TestDockerBotAPIOverridePublishesCoreGateway(t *testing.T) {
	dockerDir := filepath.Join("..", "..", "deploy", "docker")
	envExample, err := os.ReadFile(filepath.Join(dockerDir, ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	for _, line := range []string{"TELESRV_BOT_API_ADDR=\n", "TELESRV_BOT_API_BIND_IP=127.0.0.1\n", "TELESRV_BOT_API_PORT=8088\n"} {
		if !strings.Contains(string(envExample), line) {
			t.Errorf(".env.example is missing %q (the gateway must default to off on loopback)", strings.TrimSpace(line))
		}
	}
	overrideData, err := os.ReadFile(filepath.Join(dockerDir, "compose.bot-api.yaml"))
	if err != nil {
		t.Fatalf("read compose.bot-api.yaml: %v", err)
	}
	var override struct {
		Services map[string]struct {
			Ports []struct {
				Name      string `yaml:"name"`
				Target    int    `yaml:"target"`
				Published string `yaml:"published"`
				HostIP    string `yaml:"host_ip"`
				Protocol  string `yaml:"protocol"`
			} `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(overrideData, &override); err != nil {
		t.Fatalf("parse compose.bot-api.yaml: %v", err)
	}
	if len(override.Services) != 1 {
		t.Fatalf("compose.bot-api.yaml services = %v, want only core", override.Services)
	}
	core, ok := override.Services["core"]
	if !ok || len(core.Ports) != 1 {
		t.Fatalf("compose.bot-api.yaml core ports = %+v, want one bot-api port", core.Ports)
	}
	port := core.Ports[0]
	if port.Name != "bot-api" || port.Target != 8088 || port.Published != "${TELESRV_BOT_API_PORT:-8088}" ||
		port.HostIP != "${TELESRV_BOT_API_BIND_IP:-127.0.0.1}" || port.Protocol != "tcp" {
		t.Fatalf("bot-api port = %+v, want container 8088 on TELESRV_BOT_API_BIND_IP:TELESRV_BOT_API_PORT", port)
	}
	composeData, err := os.ReadFile(filepath.Join(dockerDir, "compose.yaml"))
	if err != nil {
		t.Fatalf("read compose.yaml: %v", err)
	}
	if strings.Contains(string(composeData), "8088") {
		t.Error("the default compose.yaml must not publish the Bot API port; it is opt-in through compose.bot-api.yaml")
	}
}

func TestDockerRoleConfigVariablesAreInjectedByMatchingService(t *testing.T) {
	dockerDir := filepath.Join("..", "..", "deploy", "docker")
	composeData, err := os.ReadFile(filepath.Join(dockerDir, "compose.yaml"))
	if err != nil {
		t.Fatalf("read compose.yaml: %v", err)
	}
	var compose struct {
		Services map[string]struct {
			Environment map[string]any `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(composeData, &compose); err != nil {
		t.Fatalf("parse compose.yaml: %v", err)
	}

	variablePattern := regexp.MustCompile(`\$\{([A-Z][A-Z0-9_]*)\}`)
	referencedByRole := make(map[string]map[string]struct{})
	for _, role := range []string{"edge", "core", "egress", "file", "sfu", "admin"} {
		configData, err := os.ReadFile(filepath.Join(dockerDir, "config", role+".yaml"))
		if err != nil {
			t.Fatalf("read %s config: %v", role, err)
		}
		service, ok := compose.Services[role]
		if !ok {
			t.Fatalf("compose service %q is missing", role)
		}
		referencedByRole[role] = make(map[string]struct{})
		for _, match := range variablePattern.FindAllSubmatch(configData, -1) {
			name := string(match[1])
			referencedByRole[role][name] = struct{}{}
			if _, ok := service.Environment[name]; !ok {
				t.Errorf("%s config expands %s but the matching Compose service does not inject it", role, name)
			}
		}
	}

	processOnly := map[string]map[string]struct{}{
		"edge": {
			"TELESRV_EDGE_STATE_DIR":    {},
			"TELESRV_LOG_LEVEL":         {},
			"TELESRV_RSA_IDENTITY_MODE": {},
		},
		"core": {
			"TELESRV_ALLOW_INSECURE_DEVELOPMENT_AUTH": {},
			"TELESRV_LOG_LEVEL":                       {},
		},
		"egress": {"TELESRV_LOG_LEVEL": {}},
		"file":   {"TELESRV_LOG_LEVEL": {}},
		"sfu":    {"TELESRV_LOG_LEVEL": {}},
		"admin":  {},
	}
	for role, referenced := range referencedByRole {
		for name := range compose.Services[role].Environment {
			if !strings.HasPrefix(name, "TELESRV_") {
				continue
			}
			if _, ok := referenced[name]; ok {
				continue
			}
			if _, ok := processOnly[role][name]; !ok {
				t.Errorf("Compose service %s injects unused setting %s", role, name)
			}
		}
	}

	for serviceName, service := range compose.Services {
		for name := range service.Environment {
			fileOwned := name == "TELESRV_BLOB_BACKEND" || strings.HasPrefix(name, "TELESRV_BLOB_") || strings.HasPrefix(name, "TELESRV_S3_") || strings.HasPrefix(name, "TELESRV_STORAGE_")
			adminStorageSelector := serviceName == "admin" && name == "TELESRV_BLOB_BACKEND"
			if fileOwned && serviceName != "file" && !adminStorageSelector {
				t.Errorf("File-owned setting %s is injected into Compose service %s", name, serviceName)
			}
		}
	}
}

func TestDockerComposeDefaultsTURNToSFUHostNetwork(t *testing.T) {
	dockerDir := filepath.Join("..", "..", "deploy", "docker")
	composeData, err := os.ReadFile(filepath.Join(dockerDir, "compose.yaml"))
	if err != nil {
		t.Fatalf("read compose.yaml: %v", err)
	}
	var compose struct {
		Services map[string]struct {
			Environment map[string]any `yaml:"environment"`
			Ports       []any          `yaml:"ports"`
			Networks    []string       `yaml:"networks"`
			NetworkMode string         `yaml:"network_mode"`
			Healthcheck struct {
				StartPeriod string `yaml:"start_period"`
			} `yaml:"healthcheck"`
		} `yaml:"services"`
		Networks map[string]struct {
			Internal bool `yaml:"internal"`
		} `yaml:"networks"`
	}
	if err := yaml.Unmarshal(composeData, &compose); err != nil {
		t.Fatalf("parse compose.yaml: %v", err)
	}

	core, ok := compose.Services["core"]
	if !ok {
		t.Fatal("compose core service is missing")
	}
	sfu, ok := compose.Services["sfu"]
	if !ok {
		t.Fatal("compose sfu service is missing")
	}
	for _, name := range []string{"TELESRV_TURN_RELAY_MIN_PORT", "TELESRV_TURN_RELAY_MAX_PORT"} {
		if _, ok := core.Environment[name]; ok {
			t.Errorf("core must not receive media-owned setting %s", name)
		}
		if _, ok := sfu.Environment[name]; !ok {
			t.Errorf("sfu must receive media-owned setting %s", name)
		}
	}
	corePorts := fmt.Sprint(core.Ports)
	if strings.Contains(corePorts, "TELESRV_TURN_") {
		t.Errorf("core publishes TURN media ports: %s", corePorts)
	}
	if sfu.NetworkMode != "host" {
		t.Errorf("sfu network_mode = %q, want host", sfu.NetworkMode)
	}
	if len(sfu.Ports) != 0 {
		t.Errorf("host-network sfu must not publish Docker ports: %v", sfu.Ports)
	}
	redis, ok := compose.Services["redis"]
	if !ok {
		t.Fatal("compose redis service is missing")
	}
	hasHostAccess := false
	for _, network := range redis.Networks {
		if network == "sfu_host_access" {
			hasHostAccess = true
			break
		}
	}
	if !hasHostAccess {
		t.Error("redis must join the dedicated SFU host-access return network")
	}
	hostAccess, ok := compose.Networks["sfu_host_access"]
	if !ok {
		t.Fatal("compose sfu_host_access network is missing")
	}
	if hostAccess.Internal {
		t.Error("sfu_host_access must provide a return path for the loopback Redis publication")
	}
	admin, ok := compose.Services["admin"]
	if !ok {
		t.Fatal("compose admin service is missing")
	}
	hasAdminHostAccess := false
	for _, network := range admin.Networks {
		if network == "admin_host_access" {
			hasAdminHostAccess = true
			break
		}
	}
	if !hasAdminHostAccess {
		t.Error("admin must join its dedicated host-access return network")
	}
	adminHostAccess, ok := compose.Networks["admin_host_access"]
	if !ok {
		t.Fatal("compose admin_host_access network is missing")
	}
	if adminHostAccess.Internal {
		t.Error("admin_host_access must provide a return path for the published Admin port")
	}
	if core.Healthcheck.StartPeriod != "60s" {
		t.Errorf("core healthcheck start_period = %q, want 60s", core.Healthcheck.StartPeriod)
	}

	envExample, err := os.ReadFile(filepath.Join(dockerDir, ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	if !strings.Contains(string(envExample), "TELESRV_TURN_RELAY_MAX_PORT=12999") {
		t.Error("host-network SFU must retain the full TURN relay range")
	}
	if !strings.Contains(string(envExample), "TELESRV_TURN_BRIDGE_RELAY_MAX_PORT=12563") {
		t.Error("Docker bridge fallback must remain bounded at 64 relay ports")
	}
	if !strings.Contains(string(envExample), "TELESRV_SFU_HOST_NETWORK=true") {
		t.Error("Docker must default SFU to direct host networking")
	}
	bridgeOverride, err := os.ReadFile(filepath.Join(dockerDir, "compose.sfu-bridge-network.yaml"))
	if err != nil {
		t.Fatalf("read compose.sfu-bridge-network.yaml: %v", err)
	}
	for _, required := range []string{"network_mode: !reset null", "TELESRV_TURN_UDP_PORT", "TELESRV_TURN_RELAY_MIN_PORT", "TELESRV_TURN_RELAY_MAX_PORT"} {
		if !strings.Contains(string(bridgeOverride), required) {
			t.Errorf("bridge fallback is missing %q", required)
		}
	}
}

func writeRoleYAML(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	return path
}
