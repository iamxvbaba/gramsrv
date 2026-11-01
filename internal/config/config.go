// Package config loads and validates telesrv's runtime configuration.
package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/language"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
	"telesrv/internal/links"
)

const defaultCountryCode = "CN"

const (
	StarGiftExportModeLocal    = "local"
	StarGiftExportModeTON      = "ton"
	StarGiftExportModeDisabled = "disabled"
)

// Config is telesrv's runtime configuration.
type Config struct {
	// ListenAddr is the MTProto TCP listen address.
	// It must match the self-hosted DC address/port the TDesktop patch points at (recorded in docs/tdesktop-patch-notes.md).
	ListenAddr string
	// WebSocketEnable enables MTProto-over-WebSocket multiplexing on the same port (WebA/telegram-tt).
	WebSocketEnable bool
	// WebSocketAllowedOrigins lists page origins allowed to initiate a browser WS upgrade; "*" is only for temporary debugging.
	WebSocketAllowedOrigins []string
	// AdvertiseIP is the externally reachable IP written into help.getConfig DCOptions (clients use it to connect to this DC).
	AdvertiseIP string
	// AdvertisePort is the externally reachable port written into help.getConfig DCOptions.
	// It follows ListenAddr by default; a standalone Core process should set it explicitly to the Edge's public port.
	AdvertisePort int
	// RSAKeyPath is the PEM path of the server RSA private key; generated automatically when missing.
	RSAKeyPath string
	// DC is this server's DC ID.
	DC int
	// DefaultCountryCode is the ISO 3166-1 alpha-2 country code returned by help.getNearestDc.
	// The client login page preselects the country and international dialing code from it; e.g. CN maps to +86.
	DefaultCountryCode string
	// StrictDCCheck enables the default-off key-exchange DC-label diagnostic.
	// The normal single-backend mode accepts every wire int32 label without
	// partitioning auth keys, sessions, or business state. See
	// mtprotoedge.Options.StrictDC for the optional strict behavior.
	StrictDCCheck bool
	// CheckNumberFormat requires ordinary phone numbers to pass assigned-range validation. Off by default,
	// so a 4-32 digit, non-zero-leading number a national numbering plan cannot describe may still be used as
	// a login identity. +888 virtual numbers and reserved system numbers keep their own separate rules.
	CheckNumberFormat bool
	// MTProtoMaxConnections / PerIP cover the full physical connection lifecycle from raw Accept, codec sniff
	// and handshake to authenticated session; a negative value disables the corresponding admission limit.
	MTProtoMaxConnections      int
	MTProtoMaxConnectionsPerIP int
	// MTProtoMaxConcurrentHandshakes limits concurrency of the expensive RSA/DH exchange; a negative value disables it.
	MTProtoMaxConcurrentHandshakes int
	// MTProto RPC uses the Server's shared fair scheduler; the per-connection and global budgets together limit
	// goroutines, queued tasks and request memory charge. legacy charge equals the copied body, while
	// exact charge is a conservative materialization upper bound before typed decode and is not equal to wire bytes.
	MTProtoRPCMaxInflight            int
	MTProtoRPCQueueSize              int
	MTProtoRPCTimeout                time.Duration
	MTProtoRPCGlobalWorkers          int
	MTProtoRPCGlobalMaxTasks         int
	MTProtoRPCGlobalMaxBytes         int64
	MTProtoRPCDeliveryHookWorkers    int
	MTProtoRPCDeliveryHookMaxPending int
	// Pending ownership and compact completed receipts share three-level
	// global/raw-auth/session entry accounting. Result bodies are never cached
	// here; the logical-session outbox owns unacknowledged wire bytes.
	MTProtoRPCExecutionMaxEntries        int
	MTProtoRPCExecutionAuthMaxEntries    int
	MTProtoRPCExecutionSessionMaxEntries int
	MTProtoRPCExecutionPendingPerAuth    int
	// MTProtoInboundFrameGlobalMaxBytes is the process-wide in-flight budget for transport wire + maximum
	// decrypted plaintext; it is reserved after the frame length is read and before the payload is allocated.
	MTProtoInboundFrameGlobalMaxBytes int64
	// The MTProto outbound mailbox is bounded per connection; the resend pending body is separately constrained by the Server's global budget.
	MTProtoOutboundQueueSize              int
	MTProtoOutboundControlQueueSize       int
	MTProtoOutboundTrackedGlobalMaxBytes  int64
	MTProtoOutboundCriticalGlobalMaxBytes int64
	MTProtoOutboundWriteGlobalMaxBytes    int64

	// DebugAddr is the listen address of the net/http/pprof debug endpoint (CPU/heap/goroutine/mutex/block profiling).
	// telesrv is a host process, not inside docker, so docker stats cannot see it and performance analysis relies
	// mainly on this endpoint. It binds only 127.0.0.1 by default to avoid exposing profile data; empty disables it.
	// For remote captures in production use an SSH tunnel; do not change it to 0.0.0.0.
	DebugAddr string
	// BotAPIAddr is the listen address of the minimal HTTP Bot API gateway; empty disables it. The gateway reuses
	// the MTProto app/store source of truth and keeps no separate bot state.
	BotAPIAddr string
	// BotAPIPublicURL is the client-reachable Bot API base URL that BotFather
	// shows next to a token; empty shows none.
	BotAPIPublicURL string
	// AdminAPIAddr is the listen address of the in-process telesrv admin write API; empty disables it.
	AdminAPIAddr string
	// AdminAPIToken is the Admin API bearer token; it must be configured explicitly when AdminAPIAddr is enabled.
	AdminAPIToken string
	// GroupCallControlAddr is the internal HTTP listen address where Core receives media liveness reports from standalone SFUs; required for the Core entry point.
	GroupCallControlAddr string
	// GroupCallControlURL is the internal URL a standalone SFU uses to call Core's media liveness report API.
	GroupCallControlURL string
	// GroupCallControlToken is the bearer token shared by the groupcall control API and client.
	GroupCallControlToken string
	// CoreExecGRPCAddr is the internal listen address where Core exposes the production CoreExec gRPC API; required for the Core entry point.
	CoreExecGRPCAddr string
	// CoreExecGRPCResolver selects the Edge -> CoreExec gRPC service discovery provider: static or dns.
	CoreExecGRPCResolver string
	// CoreExecGRPCTargets is the static multi-endpoint list the Edge uses to call the CoreExec gRPC API.
	CoreExecGRPCTargets string
	// CoreExecGRPCRequestTimeout is the default deadline for Edge -> CoreExec gRPC unary requests.
	CoreExecGRPCRequestTimeout time.Duration
	// CoreExecGRPCTLSCertFile/CoreExecGRPCTLSKeyFile are the CoreExec gRPC server TLS certificate and private key; both must be set together.
	CoreExecGRPCTLSCertFile string
	CoreExecGRPCTLSKeyFile  string
	// When CoreExecGRPCTLSClientCAFile is set, the CoreExec gRPC server requires and verifies the Edge client certificate.
	CoreExecGRPCTLSClientCAFile string
	// CoreExecGRPCTLSCAFile is the root CA bundle the Edge trusts when calling CoreExec gRPC.
	CoreExecGRPCTLSCAFile string
	// CoreExecGRPCTLSServerName is the server name the Edge gRPC client uses when verifying the certificate.
	CoreExecGRPCTLSServerName string
	// CoreExecGRPCTLSClientCertFile/CoreExecGRPCTLSClientKeyFile are the Edge gRPC client certificate; both must be set together.
	CoreExecGRPCTLSClientCertFile string
	CoreExecGRPCTLSClientKeyFile  string
	// CoreExecToken is the bearer token shared by the CoreExec gRPC API and client; required for the Core and Edge entry points.
	CoreExecToken string
	// EgressDeliveryGRPCAddr is the internal listen address where Egress exposes the late client ACK write-back API.
	EgressDeliveryGRPCAddr string
	// EgressDeliveryGRPCResolver selects the Edge -> Egress Delivery gRPC service discovery provider: static or dns.
	EgressDeliveryGRPCResolver string
	// EgressDeliveryGRPCTargets is the multi-endpoint list the Edge uses to call the Egress Delivery gRPC API.
	EgressDeliveryGRPCTargets string
	// EgressDeliveryGRPCRequestTimeout is the default deadline for Edge -> Egress Delivery gRPC unary requests.
	EgressDeliveryGRPCRequestTimeout time.Duration
	// EgressDeliveryGRPCTLSCertFile/EgressDeliveryGRPCTLSKeyFile are the Egress Delivery gRPC server TLS certificate and private key; both must be set together.
	EgressDeliveryGRPCTLSCertFile string
	EgressDeliveryGRPCTLSKeyFile  string
	// When EgressDeliveryGRPCTLSClientCAFile is set, the Egress Delivery gRPC server requires and verifies the Edge client certificate.
	EgressDeliveryGRPCTLSClientCAFile string
	// EgressDeliveryGRPCTLSCAFile is the root CA bundle the Edge trusts when calling Egress Delivery gRPC.
	EgressDeliveryGRPCTLSCAFile string
	// EgressDeliveryGRPCTLSServerName is the server name the Edge gRPC client uses when verifying the certificate.
	EgressDeliveryGRPCTLSServerName string
	// EgressDeliveryGRPCTLSClientCertFile/EgressDeliveryGRPCTLSClientKeyFile are the Edge gRPC client certificate; both must be set together.
	EgressDeliveryGRPCTLSClientCertFile string
	EgressDeliveryGRPCTLSClientKeyFile  string
	// EgressDeliveryToken is the bearer token shared by the Egress Delivery gRPC API and client.
	EgressDeliveryToken string
	// FileGRPCAddr is the internal listen address of the standalone FileData service; required by cmd/telesrv-file.
	FileGRPCAddr string
	// FileGRPCTargets is the comma-separated target list Core/Edge use to call FileData.
	FileGRPCTargets string
	// FileGRPCResolver selects the FileData service discovery provider: static or dns.
	FileGRPCResolver string
	// FileGRPCRequestTimeout is the default deadline for Core/Edge -> FileData unary/stream requests.
	FileGRPCRequestTimeout time.Duration
	// FileGRPCTLSCertFile/FileGRPCTLSKeyFile are the FileData gRPC server TLS certificate and private key; both must be set together.
	FileGRPCTLSCertFile string
	FileGRPCTLSKeyFile  string
	// When FileGRPCTLSClientCAFile is set, the FileData gRPC server requires and verifies the Core/Edge client certificate.
	FileGRPCTLSClientCAFile string
	// FileGRPCTLSCAFile is the root CA bundle Core/Edge trust when calling FileData gRPC.
	FileGRPCTLSCAFile string
	// FileGRPCTLSServerName is the server name the Core/Edge gRPC client uses when verifying the certificate.
	FileGRPCTLSServerName string
	// FileGRPCTLSClientCertFile/FileGRPCTLSClientKeyFile are the Core/Edge gRPC client certificate; both must be set together.
	FileGRPCTLSClientCertFile string
	FileGRPCTLSClientKeyFile  string
	// FileToken is the bearer token shared by the FileData gRPC API and client.
	FileToken string
	// PublicBaseURL is the public root URL of every client-visible telesrv link.
	// Production defaults to https://telesrv.net; locally it can be http://127.0.0.1:2401.
	PublicBaseURL string
	// Branding is the process-level read-only snapshot of every client-visible product name; protocol
	// identifiers, client-detection tokens and compatibility headers are not part of the branding config.
	Branding branding.Config
	// UpdatePublicURL is advertised to native clients through
	// help.getConfig.autoupdate_url_prefix. Empty disables desktop updates.
	UpdatePublicURL string
	// UpdateServiceURL is the internal HTTP endpoint used to resolve
	// help.getAppUpdate responses. It defaults to UpdatePublicURL when enabled.
	UpdateServiceURL     string
	UpdateRequestTimeout time.Duration
	// PublicAppScheme is the URL scheme used by the public landing page to auto-launch the self-hosted client.
	// It must match the scheme registered when the TDesktop/Android client was built, and must not occupy tg/http/https.
	PublicAppScheme string
	// PublicAppLinkBase is the optional host-based self-hosted-client link root, e.g.
	// owpg://example.com. When empty it keeps generating PublicAppScheme://<route>; when non-empty it
	// generates <base>/<route> while retaining the old scheme for server-side input compatibility.
	PublicAppLinkBase string
	// PublicWebBaseURL is the Web client root URL the "Open in Web" button on the public username page points to.
	PublicWebBaseURL string
	// PublicAppName is the product name shown on the public landing page; it does not participate in protocol routing.
	PublicAppName string
	// PublicLinkWebAddr is the listen address of the public-link landing page; empty disables it.
	// Production should listen only on loopback, with nginx reverse-proxying /<username>, /addstickers/, /addemoji/,
	// /addlist/ and the hash-only /appeal/ route to this address.
	PublicLinkWebAddr string
	// GiftPreviewCacheDir, when set together with GiftPreviewRendererURL,
	// enables static og:image previews for /nft/{slug} pages: composited
	// PNGs are rendered once by the external gift-preview-renderer service
	// and cached to this directory forever after.
	GiftPreviewCacheDir string
	// GiftPreviewRendererURL is the internal HTTP endpoint (POST .../render)
	// of the gift-preview-renderer service. Empty disables gift preview
	// images entirely.
	GiftPreviewRendererURL string
	// TelegramLoginEnabled mounts the self-hosted Telegram Login/OIDC provider
	// on PublicLinkWebAddr. Secrets are file-backed so they are not exposed in
	// process listings or accidentally copied into tracked .env templates.
	TelegramLoginEnabled bool
	TelegramLoginIssuer  string
	// TelegramLoginAllowHTTP permits HTTP issuers and registered Login URLs on
	// any valid host/IP and port. HTTPS remains mandatory when false.
	TelegramLoginAllowHTTP         bool
	TelegramLoginSigningKeysFile   string
	TelegramLoginCodeKeysFile      string
	TelegramLoginSecretPepperFile  string
	TelegramLoginRequestTTL        time.Duration
	TelegramLoginCodeTTL           time.Duration
	TelegramLoginIDTokenTTL        time.Duration
	TelegramLoginTrustedProxyCIDRs []string
	TelegramLoginRetention         time.Duration
	TelegramLoginSweepInterval     time.Duration
	TelegramLoginSweepBatch        int
	// The admin UI's standalone-process config items are kept in the unified config, and cmd/telesrv-admin reads them under the same env names.
	AdminUIAddr     string
	AdminUIPassword string
	AdminUIToken    string
	AdminSessionKey string
	// AdminUIPermissions is the permission set granted to a panel session that
	// authenticated with TELESRV_ADMIN_UI_PASSWORD / _TOKEN. The single entry "*"
	// means "every permission" and is the shipped default, so enabling RBAC never
	// silently locks an operator out of a panel that worked before.
	AdminUIPermissions []string
	// AdminScopedTokens are additional adminapi bearer tokens with a bounded
	// permission set each. They exist so an integration can be given exactly the
	// rights it needs instead of the unrestricted TELESRV_ADMIN_API_TOKEN. Parsed
	// from
	// "name:token:perm1,perm2" entries separated by ';'; a malformed entry, a
	// duplicate name or a duplicate token fails startup rather than silently
	// granting or dropping rights.
	AdminScopedTokens []AdminScopedToken

	// GeoIP* controls resolution of the session country/region text in account.getAuthorizations. The
	// protocol layer has no equivalent of help.requestIpAddress, so without these items configured
	// country/region keeps returning the "Unknown" placeholder text, exactly as before enabling it.
	//
	// GeoIPEndpoints is the failover backend list in order: each element is a URL template containing the
	// {ip} placeholder, the first is primary, and if the previous one finds nothing or is currently
	// unavailable the resolver falls through to the next. An empty list means disabled.
	//
	// A single backend inevitably leaves some sessions showing Unknown; measured in 2026-09 the four
	// providers fail in different ways: reallyfreegeoip.org returns 429 after a few consecutive requests
	// (recovering in ~60s); geojs.io only covers ASN level, and an address like 1.1.1.1 does not even have
	// a country field; hackmyip.com intermittently returns 400; ipapi.is returns is_bogon for reserved
	// ranges. So caching and the circuit breaker are necessities rather than optimizations; lowering
	// CacheTTL or NegativeTTL markedly raises the rate-limit probability.
	GeoIPEndpoints          []string
	GeoIPTimeout            time.Duration
	GeoIPConcurrency        int
	GeoIPCacheTTL           time.Duration
	GeoIPNegativeTTL        time.Duration
	GeoIPCacheSize          int
	GeoIPRateLimitThreshold int
	GeoIPRateLimitCooldown  time.Duration
	GeoIPDownThreshold      int
	GeoIPDownCooldown       time.Duration

	// PostgresDSN is the PostgreSQL connection string persisting business data (auth_key / user / authorization, etc.).
	// Its dependencies are started by deploy/docker-compose.yml; see docs/persistence-layer.md for the responsibility split.
	PostgresDSN string
	// PostgresMaxConns is the pgxpool maximum connection count. <=0 uses the pgx default (max(4, NumCPU), small for production).
	// It must cover send transactions + outbox worker concurrency + RPC reads; too small queues under high concurrency (showing up as tail-latency spikes).
	PostgresMaxConns int
	// PostgresMinConns is the number of pgxpool connections pre-warmed at startup, reducing connection-establishment wait for TDesktop's cold-start concurrent RPCs.
	PostgresMinConns int
	// PostgresCounterRecoveryMaxConns caps the dedicated PostgreSQL pool used for cold recovery of
	// Redis durable counters. The pool must be physically separate from the business transaction pool,
	// so recovering a counter cannot deadlock while a transaction holds a main-pool connection.
	PostgresCounterRecoveryMaxConns int
	// RedisAddr is the Redis address for high-frequency volatile state (verification codes, rate-limit counters, update queues).
	RedisAddr string
	// RedisPassword is the Redis password; empty by default in development.
	RedisPassword string
	// RedisDB is the Redis logical database number.
	RedisDB int
	// InstanceID is this process's stable identity for cross-Edge/Core control, the online location
	// index and pub/sub deduplication. When empty, the process generates a temporary instance ID from
	// hostname/pid/time at startup.
	InstanceID string
	// EdgeLocationTTL / HeartbeatInterval control the lifetime and renewal period of the O(1) Edge
	// instance location lease; session locations are written incrementally only on state changes and
	// are not refreshed in full on every heartbeat.
	EdgeLocationTTL               time.Duration
	EdgeLocationHeartbeatInterval time.Duration

	// DevAuthCode is the fixed development verification code; production SMS/anti-fraud is out of current scope.
	DevAuthCode string
	// The welcome message templates are used for the welcome message after a successful login; the
	// login-code template is used for the durable 777000 code delivery to existing accounts, and an
	// admin panel override takes effect live on the next code send.
	WelcomeMessagePhoneTemplate string
	WelcomeMessageEmailTemplate string
	LoginCodeMessageTemplate    string
	// AuthCodeTTL is the validity period of the login/register/email verification code.
	AuthCodeTTL time.Duration
	// PhoneCodeLength is the length of the SMS verification code generated when an external provider is used. The
	// development provider keeps using DevAuthCode as-is and is unaffected by this field.
	PhoneCodeLength int
	// AuthCodeMaxAttempts is the maximum number of wrong attempts for the same phone_code_hash / email verification code.
	// Once the limit is reached the code is invalidated immediately and the user must resend.
	AuthCodeMaxAttempts int
	// AuthCodePhoneRateLimit / AuthCodeAuthKeyRateLimit rate-limit unauthorized code issuance separately by
	// normalized phone-number digest and by the connection's actual raw auth_key. Both dimensions share
	// AuthCodeRateWindow; <=0 disables the corresponding dimension. The phone number enters the rate-limit key
	// only as a SHA-256 digest; the plaintext must never be written to a Redis key or a log.
	AuthCodePhoneRateLimit   int
	AuthCodeAuthKeyRateLimit int
	AuthCodeRateWindow       time.Duration
	// LoginEmailEnable enables email verification-code delivery in the phone login flow.
	LoginEmailEnable bool
	// When LoginEmailRequireSetup is true, an account/new phone number without a login email is required to set one up first.
	LoginEmailRequireSetup bool
	// LoginEmailCodeLength is the email verification-code length.
	LoginEmailCodeLength int
	// PhoneCodeDeliveryProvider selects the delivery method for ordinary login/registration and number-change codes:
	// development keeps the fixed code and 777000 app-code; webhook uses a random SMS code.
	PhoneCodeDeliveryProvider string
	// EmailCodeDeliveryProvider selects the delivery method for the login email and email setup/change.
	EmailCodeDeliveryProvider string
	// OTPWebhook* define the endpoint, HMAC secret and request timeout of the fixed v1 webhook protocol.
	OTPWebhookURL     string
	OTPWebhookSecret  string
	OTPWebhookTimeout time.Duration
	// SMTP* is the outbound email config used when email provider=smtp.
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPFromName string
	SMTPTLSMode  string
	SMTPTimeout  time.Duration
	// MapboxToken is the access token the server uses to proxy map thumbnails (upload.getWebFile) to the Mapbox
	// Static Images API; empty disables the proxy and falls back to a deterministic placeholder image. The client
	// location-picker token is delivered via appConfig `tdesktop_config_map` (same-origin runtime config).
	MapboxToken string
	// MapTileCacheDir is the disk cache directory for fetched map thumbnails (ensuring byte-identical part resume + controlling quota consumption).
	MapTileCacheDir string
	// ExternalMediaEnable controls whether external-link media fetching is enabled (inputMediaPhoto/DocumentExternal).
	// On by default: the server SSRF-safely fetches the user URL and mints a Photo/Document (with SSRF protection/size/rate limiting).
	ExternalMediaEnable bool
	// ExternalMediaMaxBytes is the response-body upper bound for a single external fetch; <=0 uses the default 10MB.
	ExternalMediaMaxBytes int64
	// ExternalMediaRatePerMin is the global per-minute external-fetch limit (to prevent amplification attacks); <=0 uses the default.
	ExternalMediaRatePerMin int
	// WebPagePreviewEnable controls whether link-preview fetching is enabled (messages.getWebPagePreview / attaching a card on send).
	// On by default: the server SSRF-safely fetches URLs inside messages, parses OG/Twitter/title metadata and mints the preview card.
	WebPagePreviewEnable bool
	// WebPagePreviewMaxBytes is the response-body upper bound for a single link-preview fetch (shared by HTML and preview image); <=0 uses the default 5MB.
	WebPagePreviewMaxBytes int64
	// WebPagePreviewRatePerMin is the global per-minute link-preview fetch limit; <=0 uses the default. One parse makes at most 2 upstream requests.
	WebPagePreviewRatePerMin int
	// LangPackSeedDir is the TDesktop language-pack .strings seed directory.
	LangPackSeedDir string
	// OfficialGiftsDir is the read-only official gift snapshot directory generated by cmd/giftfetch.
	OfficialGiftsDir string
	// StarGiftTONStartingGrant is the nanoton granted on the first access to telesrv's internal TON ledger.
	// That ledger is used only by the self-hosted server gift flow and connects to no external blockchain.
	StarGiftTONStartingGrant int64
	// BlobBackendKind selects the single permanent blob backend: localfs (default) or s3.
	BlobBackendKind string
	// BlobDir is the localfs permanent media root directory. s3 mode does not fall back to reading from here.
	BlobDir string
	// BlobStagingDir is the local temp upload-part and write-spool root for s3 mode;
	// it is not a permanent backend, and media is stored only in S3 once successfully assembled.
	BlobStagingDir string
	// S3* configures the MinIO/AWS S3-compatible permanent backend. endpoint has no URL scheme;
	// the access key/secret have no defaults, and CreateBucket is off by default.
	S3Endpoint        string
	S3Region          string
	S3Bucket          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3UseSSL          bool
	S3PathStyle       bool
	S3CreateBucket    bool
	// StorageLowSpaceGuardEnable enables both the local filesystem reserve and,
	// when configured, the S3 tracked-byte budget. A successful initial capacity
	// snapshot is required before the service starts accepting writes.
	StorageLowSpaceGuardEnable  bool
	StorageMinFreeBytes         int64
	StorageMaxTotalBytes        int64
	StorageUsageRefreshInterval time.Duration
	// StickerSeedDir is the reaction / sticker asset seed directory (imported into documents/sticker_sets + blob).
	StickerSeedDir string
	// GifSeedDir is an optional bounded .gif/.mp4 drop directory for @gif.
	GifSeedDir string
	// StickerSeedMaxSets limits the number of regular sticker sets imported (avoiding importing too many packs at startup); <=0 means unlimited.
	StickerSeedMaxSets int
	// PremiumPromoSeedDir is the export directory for help.getPremiumPromo videos and thumbnails.
	// A missing directory keeps the video-less compatibility response; a present directory with invalid content fails startup.
	PremiumPromoSeedDir string
	// IdentityDir holds the admin-editable server identity (name/description in identity.json, icon.<ext>
	// icon) -- see internal/identity. It is written by the cmd/telesrv-admin panel and read live before each
	// use by cmd/telesrv, so edits take effect immediately with no server restart.
	IdentityDir string
	// BusinessAIProvider controls the server-side Business automation reply generator.
	// An empty value/"echo" echoes the triggering private text, used to exercise the downstream AI provider
	// chain; "template" uses the quick reply template.
	BusinessAIProvider string
	// AIEnabled controls the client input-box AI rewrite/polish capability; when off, getTones returns an empty set to hide the entry point.
	AIEnabled bool
	// AIProviders is the compose AI provider chain, tried in order; local by default and does not leave the network.
	AIProviders []AIProviderConfig
	// AITimeout is the total timeout for a single provider call.
	AITimeout time.Duration
	// SRPRateLimit bounds 2FA password verification RPCs per user (per auth key
	// while pre-auth); <=0 disables. Withdrawal/Payment bound star-gift and Stars
	// revenue withdrawals and money-moving payments.* RPCs per user.
	SRPRateLimit         int
	SRPRateWindow        time.Duration
	WithdrawalRateLimit  int
	WithdrawalRateWindow time.Duration
	PaymentRateLimit     int
	PaymentRateWindow    time.Duration
	// AIRateLimit/AIRateWindow is the account-level compose AI rate limit.
	AIRateLimit  int
	AIRateWindow time.Duration
	// When AIPrivacyLogContent is false, logs record only length/provider/status, not user input or generated text.
	AIPrivacyLogContent bool
	// Translation* controls messages.translateText. Remote provider credentials
	// are reused from AIProviders; TranslationProviders optionally selects names
	// from that list. The deterministic local provider is never used.
	TranslationEnabled    bool
	TranslationProviders  []string
	TranslationTimeout    time.Duration
	TranslationRateLimit  int
	TranslationRateWindow time.Duration
	// ASRURL/ASRTimeout point at the local ASR HTTP backend (whisper-server's /inference).
	// If URL is empty speech-to-text is unavailable and messages.transcribeAudio returns TRANSCRIPTION_FAILED explicitly.
	ASRURL     string
	ASRTimeout time.Duration
	// DonationWalletKeyFile names the shared AES-256-GCM key file that encrypts
	// the crypto donations wallet mnemonic. Every Core must point at the same
	// key; it is never generated (decision D7). Empty disables donations.
	DonationWalletKeyFile string
	// DonationPollInterval is how often each chain watcher polls for blocks.
	DonationPollInterval time.Duration
	// TempKeyResolveCacheMaxEntries is the Router temp→perm resolution cache capacity.
	TempKeyResolveCacheMaxEntries int
	// TempKeyResolveCacheTTL is the in-process recheck period for temp→perm bindings. Bind/revoke
	// invalidate precisely; the TTL is a fallback for cross-process or abnormal paths. The 30m default
	// avoids hitting PG in full every 5s with many connections.
	TempKeyResolveCacheTTL            time.Duration
	ReadModelVersionCacheMaxEntries   int
	ReadModelVersionBatchMaxKeys      int
	ReadModelVersionBatchWait         time.Duration
	ReadModelVersionBatchQueue        int
	ReadModelVersionBatchTimeout      time.Duration
	AuthKeyGetBatchMax                int
	AuthKeyGetBatchWait               time.Duration
	AuthKeyGetBatchQueue              int
	AuthKeyGetBatchTimeout            time.Duration
	ContactReverseBatchMaxPairs       int
	ContactReverseBatchWait           time.Duration
	ContactReverseBatchQueue          int
	ContactReverseBatchTimeout        time.Duration
	ContactSnapshotCacheMaxViewers    int
	ProfilePhotoCacheMaxEntries       int
	ProfilePhotoCacheTTL              time.Duration
	PeerIdentityCacheMaxEntries       int
	DialogPrivatePeerCacheMaxEntries  int
	DialogPrivatePeerCacheMaxBytes    int64
	DialogDraftCacheMaxEntries        int
	DialogDraftCacheMaxBytes          int64
	UserProjectionFactCacheMaxEntries int
	StoryActivePeerCacheMaxEntries    int
	StoryHiddenListCacheMaxEntries    int
	StoryHiddenListCacheMaxBytes      int64
	DialogListSnapshotCacheMaxEntries int
	DialogListSnapshotCacheMaxHeaders int64
	DialogListSnapshotCacheTTL        time.Duration
	DialogListSnapshotRedisTTL        time.Duration
	ActiveChannelIDsCacheMaxEntries   int
	ActiveChannelIDsCacheTTL          time.Duration
	ActiveChannelIDsRedisTTL          time.Duration
	ActiveChannelIDsBatchMax          int
	ActiveChannelIDsBatchWait         time.Duration
	ActiveChannelIDsBatchQueue        int
	ActiveChannelIDsBatchTimeout      time.Duration
	BootstrapReadyBatchMax            int
	BootstrapReadyBatchWait           time.Duration
	BootstrapReadyBatchQueue          int
	BootstrapReadyBatchTimeout        time.Duration
	PresenceLastSeenBatchMax          int
	PresenceLastSeenBatchWait         time.Duration
	PresenceLastSeenBatchQueue        int
	PresenceLastSeenBatchTimeout      time.Duration
	PresenceLastSeenDrainTimeout      time.Duration
	// AuthUserCacheTTL is the fallback recheck period of the positive auth_key->user authorization
	// cache. Write-side invalidation is still the main path; the TTL keeps a stale authorization from
	// living in memory forever after a lost cross-Core pub/sub/control message.
	AuthUserCacheTTL time.Duration
	// AuthKeyCacheMaxEntries is the capacity of the Edge's positive raw auth_key_id->key material hot
	// cache. The Edge neither persists auth_keys nor touches PostgreSQL on the hot path; the cache holds
	// only key/salt/expiry, and Layer/client metadata stay with the CoreExec durable profile resolver.
	AuthKeyCacheMaxEntries int
	// AuthKeyCacheTTL only bounds the resources of the Edge auth key hot cache. destroy_auth_key clears
	// the cache and closes raw-key sessions immediately through a Redis invalidation event published by
	// Core; correctness must not rely on the TTL.
	AuthKeyCacheTTL time.Duration

	// ChannelRowCacheMaxEntries is the in-process "shared channel row" cache capacity (channelID→domain.Channel).
	// It is invalidated live by channels-table LISTEN/NOTIFY triggers (strong consistency, zero TTL). <=0 disables the cache and listener.
	ChannelRowCacheMaxEntries        int
	ChannelTopMessageCacheMaxEntries int
	// ChannelMemberCacheMaxEntries is the channel member/access-state read-model cache capacity ((channelID,userID)→member).
	// It is invalidated live by the unified read_model_versions notification. <=0 disables the cache.
	ChannelMemberCacheMaxEntries int
	// ChannelDialogCacheMaxEntries is the channel dialog read-projection cache capacity ((viewerUserID,channelID)→dialog).
	// It is invalidated live by the unified channel_base/channel_member/dialog_light notification. <=0 disables the cache.
	ChannelDialogCacheMaxEntries     int
	ChannelDifferenceCacheMaxEntries int
	ChannelDifferenceCacheMaxBytes   int64
	ChannelDifferenceCacheTTL        time.Duration
	// ChannelBoostCacheMaxEntries is the channel boost read-model cache capacity, covering two projections: the
	// current user's SelfBoostsApplied and the channel's total active boost count. It is invalidated exactly when
	// channel_boost_slots is written, with TTL as a natural-expiry fallback. <=0 disables the cache.
	ChannelBoostCacheMaxEntries int
	// ChannelBoostCacheTTL is the maximum staleness window of the boost read projection when no write-side notification is received.
	ChannelBoostCacheTTL time.Duration

	// OutboxWorkers is the number of concurrent telesrv-egress outbox workers. Users are stably hashed to
	// a fixed logical shard and each shard belongs to one worker, so raising the worker count does not
	// break pts order for a user.
	OutboxWorkers int
	// OutboxBatch is the maximum number of rows telesrv-egress claims from the transactional outbox at
	// once. Raising it increases throughput and per-batch PG/push pressure; lowering it reduces latency
	// jitter. See docs/message-module.md for the matching load tests.
	OutboxBatch int
	// OutboxLeaseTimeout is the duration after which a 'dispatching' row is deemed lease-expired and may be
	// re-claimed by another worker. It must exceed the per-batch delivery time, otherwise pushes will be
	// duplicated; too large and backlog recovery after a worker crash becomes slow.
	OutboxLeaseTimeout time.Duration
	// DeliveryAttemptTimeout freezes the absolute durable Edge command lifetime
	// at database claim time. It is independent from transient Redis admission.
	DeliveryAttemptTimeout time.Duration
	// DeliveryClockSkewAllowance delays database retry after the Edge wire fence
	// so old/new attempts cannot overlap across hosts.
	DeliveryClockSkewAllowance time.Duration
	// OutboundPushTimeout is the longest telesrv-egress waits for the Edge to confirm the outbound queue accepted a push.
	OutboundPushTimeout time.Duration
	// SendRateLimit is the number of messages allowed per account send window under an explicit
	// deployment policy; <=0 disables it. Off by default: durable admission/backpressure handles system
	// capacity, and a normal client sending quickly must not be mistaken for a flood.
	SendRateLimit int
	// SendRateWindow is the send rate-limit window.
	SendRateWindow time.Duration
	// CatchupRateLimit is the number of difference-class catch-up RPCs (getChannelDifference / getPeerDialogs)
	// allowed per user per window; <=0 disables it (fan-out design Phase 2 / §10.3, unblocking full-speed
	// large-group nudge as a prerequisite).
	CatchupRateLimit int
	// CatchupRateWindow is the catch-up rate-limit window.
	CatchupRateWindow time.Duration
	// ChannelNudgeMaxTargets is the target limit for one fan-out >cap nudge; <=0 uses the built-in default.
	ChannelNudgeMaxTargets int
	// UpdateEventRetention is the durable update log retention period; only events already covered by the watermark/state are cleaned up.
	UpdateEventRetention time.Duration
	// BotAPIUpdateRetention is the maximum retention period of the bot_api_updates delivery queue (official Bot API semantics, 24h);
	// acknowledged rows are additionally reclaimed earlier under a fixed short grace period (performance audit H1).
	BotAPIUpdateRetention time.Duration
	// OrphanAuthKeyRetention is the minimum retention period for an auth key created by the handshake but with no
	// authorization/temp binding/active connection. After expiry it is reclaimed by a bounded GC; a client receiving
	// -404 will rebuild the key.
	OrphanAuthKeyRetention time.Duration
	// RetentionInterval is the run interval of the retention worker.
	RetentionInterval time.Duration
	// RetentionBatch is the maximum number of rows deleted in one retention pass.
	RetentionBatch int
	// UploadPartTTL is the retention period for unassembled upload parts.
	UploadPartTTL time.Duration
	// UploadPartGCInterval is the run interval of the upload_parts GC worker.
	UploadPartGCInterval time.Duration
	// UploadPartGCBatch is the maximum number of rows deleted in one upload_parts GC pass.
	UploadPartGCBatch int
	// UploadInFlightMaxBytes is the byte limit for a single user's unassembled upload parts; <=0 means unlimited.
	UploadInFlightMaxBytes int64
	// UploadInFlightMaxParts is the row limit for a single user's unassembled upload parts; <=0 means unlimited.
	UploadInFlightMaxParts int
	// UploadInFlightMaxFiles is the limit on the number of a single user's unassembled file_ids; <=0 means unlimited.
	UploadInFlightMaxFiles int

	// CallRingTimeout is the server-side fallback timeout for a private call (ringing/Accepted hanging), sharing
	// the same source as the callRingTimeoutMs delivered to clients (compat/tdesktop/config.go, 90000ms).
	CallRingTimeout time.Duration
	// CallTombstoneTTL is the retention period for a terminal call tombstone (the absorption window for idempotent/late RPCs).
	CallTombstoneTTL time.Duration
	// CallMaxActivePerUser is the limit on a single user's concurrent non-terminal calls.
	CallMaxActivePerUser int
	// CallRegistryMaxEntries is the global hard limit of the in-process call registry.
	CallRegistryMaxEntries int
	// CallSignalingMaxBytes is the per-payload limit for phone.sendSignalingData.
	CallSignalingMaxBytes int
	// CallSignalingRate is the per-second signaling-forward limit for a single call (excess is silently dropped).
	CallSignalingRate int
	// CallExpiryInterval is the polling interval of the call-timeout fallback dispatcher.
	CallExpiryInterval time.Duration

	// PremiumGrantMonths is the number of Premium months granted by default to a newly registered account; 0 disables the grant.
	// The one-time grant to existing accounts is backfilled by migration 0094 and is unaffected by this setting.
	PremiumGrantMonths int
	// PremiumBotUsername is the public username advertised in app config and
	// Premium deep links. The reserved bot id remains stable.
	PremiumBotUsername string
	PremiumBotUserID   int64
	// PremiumPlans is the authoritative Stars catalog. Each entry carries a
	// fixed month count, exact duration in days and Stars price.
	PremiumPlans []domain.PremiumPlan

	// PasskeyRPID is the passkey (WebAuthn) relying-party id (domain). The server validates
	// authData.rpIdHash against it; on a real device via Android CredentialManager it must match the public
	// domain hosting assetlinks.json (see docs). Any stable value works for local/software-authenticator
	// verification.
	PasskeyRPID string
	// PasskeyAllowedOrigins is the allowed WebAuthn origin allowlist; empty means origins are not strictly checked
	// (the server usually does not know the Android apk-key-hash origin in advance).
	PasskeyAllowedOrigins []string
	// StarsStartingGrant is the starting balance of the local Stars ledger (granted lazily on first read, the
	// granted boolean is idempotent, covering new and old accounts with no backfill migration); 0 disables the automatic grant.
	StarsStartingGrant int64
	// PremiumSweepInterval is the polling interval of the Premium-expiry sweeper. Premium delivery correctness is
	// derived live from the read path; the sweeper only cleans up expired rows and pushes updateUser notifications.
	PremiumSweepInterval time.Duration
	// PremiumSweepBatch is the maximum number of rows cleaned up in one expiry pass.
	PremiumSweepBatch int
	// StarGiftSweepInterval drives offer expiry/refunds, auction rounds and their
	// durable notification/delivery outboxes. It is entirely server-local.
	StarGiftSweepInterval time.Duration
	// StarGiftSweepBatch bounds rows/aggregates claimed by one sweep.
	StarGiftSweepBatch               int
	StarGiftTransferStars            int64
	StarGiftDropOriginalDetailsStars int64
	StarGiftOfferMinStars            int
	StarGiftStarsProceedsPermille    int
	StarGiftTONProceedsPermille      int
	// StarGiftResaleMaxStars/StarGiftResaleMaxTON cap what a seller may list a
	// unique gift for. TON is expressed in whole TON on this env var and
	// converted to nanoton for the market policy.
	StarGiftResaleMaxStars int64
	StarGiftResaleMaxTON   int64
	// StarGiftResaleMinStars is the fallback Stars floor used when a gift's
	// own authored resell_min_stars is unset (0). 125 matches real
	// Telegram's own stars_stargift_resale_amount_min app-config default.
	StarGiftResaleMinStars int64
	// StarGiftTONStarsRate is how many Stars one whole TON is worth for
	// display purposes only (the resell_amount vector shown while browsing a
	// TON-priced listing carries both the true TON amount and this Stars
	// equivalent, matching official clients' own price line). It does not
	// affect what is actually charged.
	StarGiftTONStarsRate               int64
	StarGiftExportDelay                time.Duration
	StarGiftTransferDelay              time.Duration
	StarGiftResellDelay                time.Duration
	StarGiftCraftDelay                 time.Duration
	StarGiftCraftChancePermille        int
	StarGiftExportMode                 string
	StarGiftTONNetwork                 string
	StarGiftTONCollectionAddress       string
	StarGiftTONCollectionCodeHash      string
	StarGiftTONMintABI                 string
	StarGiftTONInitialItemIndex        string
	StarGiftTONProofDomain             string
	StarGiftTONCapabilitySecretFile    string
	StarGiftTONExportTTL               time.Duration
	StarGiftTONChallengeTTL            time.Duration
	StarGiftTONFinalizerBatch          int
	StarGiftTONFinalizerPollInterval   time.Duration
	StarGiftTONFinalizerLeaseTimeout   time.Duration
	StarGiftTONFinalizerRequestTimeout time.Duration
	StarGiftTONFinalizerRetryDelay     time.Duration
	StarGiftTONClaimEnabled            bool
	StarGiftTONClaimBotTokenFile       string
	StarGiftTONClaimInitDataTTL        time.Duration

	// DevStarsPaymentsEnabled gates the fake "buy stars with a card" payment
	// provider that always succeeds without charging anything real -- see
	// rpc.Config.DevStarsPaymentsEnabled. Defaults to false; exploited live
	// 2026-09-08 for 2M+ free stars while this was unconditionally reachable.
	DevStarsPaymentsEnabled bool

	// RatingEnabled controls the local admin-only composite account rating.
	// Disabled keeps every local projection empty and refuses rating writes; no
	// client-facing Telegram field changes in either mode.
	RatingEnabled bool
	// RatingPendingDelay is how long a rating increase stays parked as a pending
	// local score before it becomes the visible admin level. A decrease is
	// always applied immediately: a penalty must not sit behind a delay.
	// 0 applies every change immediately.
	RatingPendingDelay time.Duration
	// RatingRecomputeInterval / RatingRecomputeBatch drive the background
	// recompute worker. The rating derives from signals owned by other
	// subsystems, so freshness is a worker property, not a write-path one.
	RatingRecomputeInterval time.Duration
	RatingRecomputeBatch    int
	// RatingStaleAfter is the projection age after which the worker recomputes a
	// user.
	RatingStaleAfter time.Duration
	// Rating weights are the integer composite formula. Defaults mirror
	// domain.DefaultAccountRatingWeights() exactly, so the shipped behaviour is
	// identical whether or not these keys are set. Every weight is a magnitude:
	// the penalties are subtracted by the domain formula, so all values are
	// non-negative and a negative value fails startup.
	RatingWeightStarsReceivedPermille int64
	RatingWeightStarsSpentPermille    int64
	RatingWeightMessageSent           int64
	RatingWeightAccountAgeDay         int64
	RatingWeightGiftReceived          int64
	RatingWeightModerationCase        int64
	RatingWeightScamPenalty           int64
	RatingWeightFakePenalty           int64
	// RatingActivityCap bounds the activity component so activity alone cannot
	// outweigh Stars and moderation; 0 leaves it uncapped.
	RatingActivityCap int64
	// VerificationEnabled controls official platform verification: the @verifybot
	// application flow and the panel's review queue. Disabled refuses every
	// verification use case explicitly; already-verified peers keep their badge,
	// because the flag lives on the peer record and is not derived from this
	// feature being on.
	VerificationEnabled bool
	// VerificationAllowUserTargets opts plain user accounts in as verification
	// subjects. Off by default: the official process verifies a public presence
	// (bot, public channel, public supergroup), and a private account has nothing
	// to check.
	VerificationAllowUserTargets bool
	// VerificationRejectCooldown is how long an applicant must wait before filing
	// the same target again after a rejection. Measured from the decision, so a
	// slow review never shortens it; 0 disables the cooldown.
	VerificationRejectCooldown time.Duration
	// VerificationApplyRateLimit / VerificationApplyRateWindow bound how many
	// applications one applicant may create per window. 0 for either disables the
	// budget.
	VerificationApplyRateLimit  int
	VerificationApplyRateWindow time.Duration
	// VerificationBotRateLimit / VerificationBotRateWindow bound the @verifybot
	// dialog itself (per-applicant command rate), independently of how many
	// applications are actually created.
	VerificationBotRateLimit  int
	VerificationBotRateWindow time.Duration
	// VerificationNotifyInterval / VerificationNotifyBatch drive the applicant
	// notification worker. A decision commits with its outbox row, never with a
	// message send, so delivery cadence is a worker property.
	VerificationNotifyInterval time.Duration
	VerificationNotifyBatch    int
	// ScamProfileWarning / FakeProfileWarning are the first line the server
	// prepends to a peer's About in getFullUser / channels.getFullChannel and
	// the chat-invite preview after an admin flags the peer SCAM/FAKE. Official
	// clients render their own localized badge from the flag, but the badge is
	// easy to miss; this text makes the mark part of the description itself,
	// shown above the owner's own text. Empty keeps built-in English defaults
	// (domain.ModerationProfileWarning). The stored description is never
	// rewritten -- the warning lives only in the read projection.
	ScamProfileWarning string
	FakeProfileWarning string
	// Broadcast worker enumerates all-user campaigns and delivers claimed
	// recipients in bounded batches. Lease permits another instance to recover a
	// claim after a crash; it is not a message timeout.
	BroadcastWorkerInterval   time.Duration
	BroadcastWorkerLease      time.Duration
	BroadcastMaterializeBatch int
	BroadcastDeliveryBatch    int
	// VerificationMaxActivePerUser bounds how many applications one applicant may
	// keep open at once; 0 disables the cap.
	VerificationMaxActivePerUser int

	// BotVerificationEnabled controls THIRD-PARTY bot verification
	// (core.telegram.org/api/bots/verification): a verifier bot marking peers with
	// its own icon and description, projected onto
	// user/channel.bot_verification_icon and botInfo.verifier_settings. It is a
	// different mechanism from VerificationEnabled above -- that one is the
	// operator-granted platform checkmark, and the two never read each other's
	// state.
	//
	// Disabled refuses every third-party mutation (grants, revocations,
	// applications, catalogue edits) while the marks already granted keep
	// projecting: blanking one verifier's badges is what its per-verifier kill
	// switch is for.
	BotVerificationEnabled bool
	// BotVerificationMaxPerVerifier bounds how many peers one verifier bot may
	// mark. Verifier status is granted per deployment rather than earned per peer,
	// so an unbounded verifier would be an unbounded badge printer. 0 disables the
	// service-level bound and leaves only the storage bound
	// (domain.MaxCustomVerificationsPerVerifier), which is also the maximum this
	// key accepts.
	BotVerificationMaxPerVerifier int
	// BotVerificationRequestRateLimit / BotVerificationRequestRateWindow bound how
	// many verification applications one applicant may file per window, across all
	// verifier bots. 0 for either disables the budget.
	BotVerificationRequestRateLimit  int
	BotVerificationRequestRateWindow time.Duration

	// CollectibleUsernameURLTemplate is the landing URL recorded on a minted
	// collectible username when the mint request carries no explicit URL.
	// Empty derives <TELESRV_PUBLIC_BASE_URL>/nft/username/<username>; a template
	// may carry the {username} placeholder, and without it the name is appended
	// as the last path segment. No external marketplace is contacted.
	CollectibleUsernameURLTemplate string

	// GroupCallCheckTTL is the expiry threshold for the group-call participant liveness watermark (the client in
	// Connecting state ticks every 4s; from M1 the SFU liveness reporter also refreshes this watermark).
	GroupCallCheckTTL time.Duration
	// GroupCallSweepInterval is the polling interval of the ghost-participant sweeper.
	GroupCallSweepInterval time.Duration
	// GroupCallMaxParticipants is the per-room participant limit (demo scale).
	GroupCallMaxParticipants int

	// When TURNEnable is false, Core sends no relays and the SFU does not start the TURN media listener.
	TURNEnable bool
	// TURNUDPPort is the TURN/STUN listen port owned by the SFU role; Core only hands this address to
	// clients. It cannot be shared with the group call SFU port.
	TURNUDPPort int
	// TURNBindIP is the IPv4 address the SFU role actually binds the TURN listener and relay sockets
	// to. Bridge publishing usually uses 0.0.0.0; in host network mode the application itself must honor it.
	TURNBindIP string
	// TURNAdvertiseIP is the client-reachable address written into phoneConnectionWebrtc and relay allocations,
	// falling back by default to SFUAdvertiseIP → AdvertiseIP.
	TURNAdvertiseIP string
	// TURNSecret is the REST HMAC key shared by the Core credential issuer and the SFU TURN listener;
	// a split runtime with TURN enabled must set a stable value explicitly.
	TURNSecret string
	// TURNRelayMinPort/TURNRelayMaxPort bound the UDP port range for relay allocations (the firewall opening range).
	TURNRelayMinPort int
	TURNRelayMaxPort int
	// CallTURNCredentialTTL is the validity period of the TURN credential issued per call.
	CallTURNCredentialTTL time.Duration
	// CallForceRelay forces p2p_allowed=false (for debugging the TURN relay path).
	// Private-call P2P by default is limited to mutual contacts (an rpc-layer hard gate on mutual contacts +
	// phone_p2p defaulting to AllowContacts); this switch forces the whole chain through the relay.
	CallForceRelay bool

	// When LiveStreamEnable is true, the channel RTMP livestream media plane is enabled (embedded RTMP ingest + ffmpeg segmenting).
	LiveStreamEnable bool
	// LiveStreamRtmpAddr is the TCP listen address of RTMP ingest (default ":2400").
	LiveStreamRtmpAddr string
	// LiveStreamRtmpURL is the server address returned to the streaming-side (OBS); empty falls back to rtmp://<AdvertiseIP>:2400/live.
	LiveStreamRtmpURL string
	// LiveStreamFFmpegPath is the ffmpeg executable path (defaults to "ffmpeg" from PATH).
	LiveStreamFFmpegPath string
	// LiveStreamWorkDir is the segmenting temp directory (defaults to the system temp directory).
	LiveStreamWorkDir string
	// LiveStreamSegmentKeep is the number of segment seconds retained in memory per stream (default 32).
	LiveStreamSegmentKeep int

	// SFUUDPPort is the single UDP port of the standalone SFU (pion ICE UDPMux). Allow it through the Windows firewall.
	SFUUDPPort int
	// SFUBindIP is the local IPv4 bind address of the standalone SFU media sockets.
	SFUBindIP string
	// SFUAdvertiseIP is the client-reachable address written into downstream candidates, falling back to AdvertiseIP by default.
	// ⚠ 127.0.0.1 makes ICE on real devices never connect and produces no RPC error at all (a pure media-plane silent failure).
	SFUAdvertiseIP string
	// SFUOwnerTTL / HeartbeatInterval control the callID -> sfu instance owner lease.
	// With several SFUs a call must stick to one owner rather than each instance opening its own room.
	SFUOwnerTTL               time.Duration
	SFUOwnerHeartbeatInterval time.Duration
	// SFUInstanceTTL / HeartbeatInterval control the discovery lease of available SFU media instances.
	SFUInstanceTTL               time.Duration
	SFUInstanceHeartbeatInterval time.Duration
	// SFUInstanceHealthTimeout caps a single SFU /healthz probe by the Core remote owner selector.
	SFUInstanceHealthTimeout time.Duration
	// SFUInstanceMaxActiveCalls of 0 means unlimited; when >0 the owner selector skips full instances.
	SFUInstanceMaxActiveCalls int
	// SFUControlGRPCAddr exposes the standalone SFU's gRPC control API; required by cmd/telesrv-sfu.
	SFUControlGRPCAddr string
	// SFUControlGRPCURL is the address other Core/SFU instances use to call this SFU's gRPC control API; empty falls back to SFUControlGRPCAddr.
	SFUControlGRPCURL string
	// SFUControlGRPCRequestTimeout is the default deadline for Core -> SFU gRPC control unary requests.
	SFUControlGRPCRequestTimeout time.Duration
	// SFUControlGRPCTLSCertFile/SFUControlGRPCTLSKeyFile are the SFU gRPC control server TLS certificate and private key; both must be set together.
	SFUControlGRPCTLSCertFile string
	SFUControlGRPCTLSKeyFile  string
	// When SFUControlGRPCTLSClientCAFile is set, the SFU gRPC control server requires and verifies the Core/client certificate.
	SFUControlGRPCTLSClientCAFile string
	// SFUControlGRPCTLSCAFile is the root CA bundle Core/probes trust when calling SFU gRPC control.
	SFUControlGRPCTLSCAFile string
	// SFUControlGRPCTLSServerName is the server name the Core/probe gRPC client uses when verifying the certificate.
	SFUControlGRPCTLSServerName string
	// SFUControlGRPCTLSClientCertFile/SFUControlGRPCTLSClientKeyFile are the Core/probe gRPC client certificate; both must be set together.
	SFUControlGRPCTLSClientCertFile string
	SFUControlGRPCTLSClientKeyFile  string
	SFUControlToken                 string
}

// AdminScopedToken is one adminapi bearer token restricted to a permission set.
// Name is the audit identity written next to actions performed with the token;
// Permissions is the closed list of rights it carries ("*" means all).
type AdminScopedToken struct {
	Name        string
	Token       string
	Permissions []string
}

type AIProviderConfig struct {
	Name            string
	Kind            string
	BaseURL         string
	APIKey          string
	Model           string
	MaxOutputTokens int
	Temperature     float64
	OmitTemperature bool
	Thinking        string
}

func loadFromEnv(fileEnv envSource) (Config, error) {
	envBoolOr := fileEnv.envBoolOr
	envOr := fileEnv.envOr
	envListOr := fileEnv.envListOr
	envIntOr := fileEnv.envIntOr
	envInt64Or := fileEnv.envInt64Or
	envDurationOr := fileEnv.envDurationOr
	envAllowEmptyOr := fileEnv.envAllowEmptyOr
	if err := validateStrictMTProtoCapacityEnv(fileEnv); err != nil {
		return Config{}, err
	}

	publicBaseURL, err := links.ValidateBaseURL(envOr("TELESRV_PUBLIC_BASE_URL", links.DefaultPublicBaseURL))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_PUBLIC_BASE_URL: %w", err)
	}
	brandConfig := branding.DefaultConfig()
	brandConfig.ProductName = envOr("TELESRV_BRAND_PRODUCT_NAME", brandConfig.ProductName)
	brandConfig.ProductUsername = envOr("TELESRV_BRAND_PRODUCT_USERNAME", brandConfig.ProductUsername)
	brandConfig.DesktopAppName = envOr("TELESRV_BRAND_DESKTOP_APP_NAME", brandConfig.DesktopAppName)
	brandConfig.AndroidAppName = envOr("TELESRV_BRAND_ANDROID_APP_NAME", brandConfig.AndroidAppName)
	brandConfig.IOSAppName = envOr("TELESRV_BRAND_IOS_APP_NAME", brandConfig.IOSAppName)
	brandConfig.MacOSAppName = envOr("TELESRV_BRAND_MACOS_APP_NAME", brandConfig.MacOSAppName)
	brandConfig.WebAAppName = envOr("TELESRV_BRAND_WEB_A_APP_NAME", brandConfig.WebAAppName)
	brandConfig.WebKAppName = envOr("TELESRV_BRAND_WEB_K_APP_NAME", brandConfig.WebKAppName)
	brandConfig.PremiumName = envOr("TELESRV_BRAND_PREMIUM_NAME", brandConfig.PremiumName)
	brandConfig.StarsName = envOr("TELESRV_BRAND_STARS_NAME", brandConfig.StarsName)
	brandConfig.PublicBaseURL = publicBaseURL
	brandConfig, err = branding.Validate(brandConfig)
	if err != nil {
		return Config{}, fmt.Errorf("brand configuration: %w", err)
	}
	updatePublicURL := strings.TrimSpace(envAllowEmptyOr("TELESRV_UPDATE_PUBLIC_URL", ""))
	if updatePublicURL != "" {
		updatePublicURL, err = links.ValidateBaseURL(updatePublicURL)
		if err != nil {
			return Config{}, fmt.Errorf("TELESRV_UPDATE_PUBLIC_URL: %w", err)
		}
	}
	updateServiceURL := strings.TrimSpace(envOr("TELESRV_UPDATE_SERVICE_URL", updatePublicURL))
	if updateServiceURL != "" {
		updateServiceURL, err = links.ValidateBaseURL(updateServiceURL)
		if err != nil {
			return Config{}, fmt.Errorf("TELESRV_UPDATE_SERVICE_URL: %w", err)
		}
	}
	publicAppScheme, err := links.ValidateAppScheme(envOr("TELESRV_PUBLIC_APP_SCHEME", links.DefaultAppScheme))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_PUBLIC_APP_SCHEME: %w", err)
	}
	publicAppLinkBase, err := links.ValidateAppLinkBase(envAllowEmptyOr("TELESRV_PUBLIC_APP_LINK_BASE", ""))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_PUBLIC_APP_LINK_BASE: %w", err)
	}
	publicWebBaseURL, err := links.ValidateBaseURL(envOr("TELESRV_PUBLIC_WEB_BASE_URL", links.DefaultWebBaseURL))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_PUBLIC_WEB_BASE_URL: %w", err)
	}
	publicAppName, err := links.ValidateAppName(envOr("TELESRV_PUBLIC_APP_NAME", brandConfig.ProductName))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_PUBLIC_APP_NAME: %w", err)
	}
	countryCode, err := normalizeDefaultCountryCode(envOr("TELESRV_DEFAULT_COUNTRY_CODE", defaultCountryCode))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_DEFAULT_COUNTRY_CODE: %w", err)
	}
	advertiseIP, err := normalizeAdvertiseIP(envOr("TELESRV_ADVERTISE_IP", "127.0.0.1"))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_ADVERTISE_IP: %w", err)
	}
	listenAddr := envOr("TELESRV_LISTEN", "0.0.0.0:2398")
	listenPort, err := portFromListenAddr("TELESRV_LISTEN", listenAddr)
	if err != nil {
		return Config{}, err
	}
	advertisePort, err := portFromOptionalEnv(fileEnv, "TELESRV_ADVERTISE_PORT", listenPort)
	if err != nil {
		return Config{}, err
	}
	// The composite rating weight defaults are the domain formula's own defaults;
	// see RatingWeight* below.
	defaultRatingWeights := domain.DefaultAccountRatingWeights()
	adminScopedTokens, err := parseAdminScopedTokens(envAllowEmptyOr("TELESRV_ADMIN_SCOPED_TOKENS", ""))
	if err != nil {
		return Config{}, err
	}
	premiumBotUsername := strings.TrimPrefix(strings.TrimSpace(envOr("TELESRV_PREMIUM_BOT_USERNAME", "premiumbot")), "@")
	if !domain.ValidBotUsername(premiumBotUsername) {
		return Config{}, fmt.Errorf("TELESRV_PREMIUM_BOT_USERNAME must be a valid username")
	}
	premiumBotUserID := envInt64Or("TELESRV_PREMIUM_BOT_USER_ID", domain.PremiumBotUserID)
	if !domain.ValidPremiumBotUserID(premiumBotUserID) {
		return Config{}, fmt.Errorf("TELESRV_PREMIUM_BOT_USER_ID must be positive and must not collide with another system account")
	}
	premiumPlans, err := parsePremiumPlans(envOr("TELESRV_PREMIUM_PLANS", "3:90:750,6:180:1300,12:365:2400"))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_PREMIUM_PLANS: %w", err)
	}

	cfg := Config{
		ListenAddr:      listenAddr,
		WebSocketEnable: envBoolOr("TELESRV_WEBSOCKET_ENABLE", true),
		WebSocketAllowedOrigins: envListOr("TELESRV_WEBSOCKET_ALLOWED_ORIGINS", []string{
			"http://localhost:1234",
			"http://127.0.0.1:1234",
		}),
		// help.getConfig must deliver at least one reconnectable main DC address; a remote deployment must
		// not reuse the loopback default and needs the client's actually reachable IP set explicitly.
		AdvertiseIP:                       advertiseIP,
		AdvertisePort:                     advertisePort,
		RSAKeyPath:                        envOr("TELESRV_RSA_KEY", "data/server_rsa.pem"),
		DC:                                envIntOr("TELESRV_DC", 2),
		DefaultCountryCode:                countryCode,
		StrictDCCheck:                     envBoolOr("TELESRV_STRICT_DC_CHECK", false),
		CheckNumberFormat:                 envBoolOr("TELESRV_CHECK_NUMBER_FORMAT", false),
		MTProtoMaxConnections:             envIntOr("TELESRV_MTPROTO_MAX_CONNECTIONS", 200000),
		MTProtoMaxConnectionsPerIP:        envIntOr("TELESRV_MTPROTO_MAX_CONNECTIONS_PER_IP", 4096),
		MTProtoMaxConcurrentHandshakes:    envIntOr("TELESRV_MTPROTO_MAX_CONCURRENT_HANDSHAKES", 256),
		MTProtoRPCMaxInflight:             envIntOr("TELESRV_MTPROTO_RPC_MAX_INFLIGHT", 32),
		MTProtoRPCQueueSize:               envIntOr("TELESRV_MTPROTO_RPC_QUEUE_SIZE", 64),
		MTProtoRPCTimeout:                 envDurationOr("TELESRV_MTPROTO_RPC_TIMEOUT", 30*time.Second),
		MTProtoRPCGlobalWorkers:           envIntOr("TELESRV_MTPROTO_RPC_GLOBAL_WORKERS", 256),
		MTProtoRPCGlobalMaxTasks:          envIntOr("TELESRV_MTPROTO_RPC_GLOBAL_MAX_TASKS", 32768),
		MTProtoRPCGlobalMaxBytes:          envInt64Or("TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES", 512<<20),
		MTProtoRPCDeliveryHookWorkers:     envIntOr("TELESRV_MTPROTO_RPC_DELIVERY_HOOK_WORKERS", 32),
		MTProtoRPCDeliveryHookMaxPending:  envIntOr("TELESRV_MTPROTO_RPC_DELIVERY_HOOK_MAX_PENDING", 16_384),
		MTProtoRPCExecutionMaxEntries:     envIntOr("TELESRV_MTPROTO_RPC_EXECUTION_MAX_ENTRIES", 1<<18),
		MTProtoRPCExecutionAuthMaxEntries: envIntOr("TELESRV_MTPROTO_RPC_EXECUTION_AUTH_MAX_ENTRIES", 1<<15),
		MTProtoRPCExecutionSessionMaxEntries: envIntOr(
			"TELESRV_MTPROTO_RPC_EXECUTION_SESSION_MAX_ENTRIES", 1<<14,
		),
		MTProtoRPCExecutionPendingPerAuth:     envIntOr("TELESRV_MTPROTO_RPC_EXECUTION_PENDING_PER_AUTH", 1<<11),
		MTProtoInboundFrameGlobalMaxBytes:     envInt64Or("TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES", 512<<20),
		MTProtoOutboundQueueSize:              envIntOr("TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE", 128),
		MTProtoOutboundControlQueueSize:       envIntOr("TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE", 32),
		MTProtoOutboundTrackedGlobalMaxBytes:  envInt64Or("TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES", 512<<20),
		MTProtoOutboundCriticalGlobalMaxBytes: envInt64Or("TELESRV_MTPROTO_OUTBOUND_CRITICAL_GLOBAL_MAX_BYTES", 64<<20),
		MTProtoOutboundWriteGlobalMaxBytes:    envInt64Or("TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES", 512<<20),
		DebugAddr:                             envAllowEmptyOr("TELESRV_DEBUG_ADDR", "127.0.0.1:6060"),
		BotAPIAddr:                            envAllowEmptyOr("TELESRV_BOT_API_ADDR", ""),
		BotAPIPublicURL:                       strings.TrimSpace(envOr("TELESRV_BOT_API_PUBLIC_URL", "")),
		AdminAPIAddr:                          envAllowEmptyOr("TELESRV_ADMIN_API_ADDR", ""),
		AdminAPIToken:                         envOr("TELESRV_ADMIN_API_TOKEN", ""),
		GroupCallControlAddr:                  envAllowEmptyOr("TELESRV_GROUPCALL_CONTROL_ADDR", ""),
		GroupCallControlURL:                   envAllowEmptyOr("TELESRV_GROUPCALL_CONTROL_URL", ""),
		GroupCallControlToken:                 envOr("TELESRV_GROUPCALL_CONTROL_TOKEN", ""),
		CoreExecGRPCAddr:                      envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_ADDR", ""),
		CoreExecGRPCResolver:                  strings.ToLower(strings.TrimSpace(envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_RESOLVER", "static"))),
		CoreExecGRPCTargets:                   envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_TARGETS", ""),
		CoreExecGRPCRequestTimeout:            envDurationOr("TELESRV_CORE_EXEC_GRPC_REQUEST_TIMEOUT", 5*time.Second),
		CoreExecGRPCTLSCertFile:               envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_TLS_CERT_FILE", ""),
		CoreExecGRPCTLSKeyFile:                envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_TLS_KEY_FILE", ""),
		CoreExecGRPCTLSClientCAFile:           envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CA_FILE", ""),
		CoreExecGRPCTLSCAFile:                 envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_TLS_CA_FILE", ""),
		CoreExecGRPCTLSServerName:             envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_TLS_SERVER_NAME", ""),
		CoreExecGRPCTLSClientCertFile:         envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CERT_FILE", ""),
		CoreExecGRPCTLSClientKeyFile:          envAllowEmptyOr("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_KEY_FILE", ""),
		CoreExecToken:                         envOr("TELESRV_CORE_EXEC_TOKEN", ""),
		EgressDeliveryGRPCAddr:                envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_ADDR", ""),
		EgressDeliveryGRPCResolver:            strings.ToLower(strings.TrimSpace(envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_RESOLVER", "static"))),
		EgressDeliveryGRPCTargets:             envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_TARGETS", ""),
		EgressDeliveryGRPCRequestTimeout:      envDurationOr("TELESRV_EGRESS_DELIVERY_GRPC_REQUEST_TIMEOUT", 5*time.Second),
		EgressDeliveryGRPCTLSCertFile:         envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CERT_FILE", ""),
		EgressDeliveryGRPCTLSKeyFile:          envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_TLS_KEY_FILE", ""),
		EgressDeliveryGRPCTLSClientCAFile:     envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CA_FILE", ""),
		EgressDeliveryGRPCTLSCAFile:           envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CA_FILE", ""),
		EgressDeliveryGRPCTLSServerName:       envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_TLS_SERVER_NAME", ""),
		EgressDeliveryGRPCTLSClientCertFile:   envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CERT_FILE", ""),
		EgressDeliveryGRPCTLSClientKeyFile:    envAllowEmptyOr("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_KEY_FILE", ""),
		EgressDeliveryToken:                   envOr("TELESRV_EGRESS_DELIVERY_TOKEN", ""),
		FileGRPCAddr:                          envAllowEmptyOr("TELESRV_FILE_GRPC_ADDR", ""),
		FileGRPCTargets:                       envAllowEmptyOr("TELESRV_FILE_GRPC_TARGETS", ""),
		FileGRPCResolver:                      strings.ToLower(strings.TrimSpace(envOr("TELESRV_FILE_GRPC_RESOLVER", "static"))),
		FileGRPCRequestTimeout:                envDurationOr("TELESRV_FILE_GRPC_REQUEST_TIMEOUT", 10*time.Second),
		FileGRPCTLSCertFile:                   envAllowEmptyOr("TELESRV_FILE_GRPC_TLS_CERT_FILE", ""),
		FileGRPCTLSKeyFile:                    envAllowEmptyOr("TELESRV_FILE_GRPC_TLS_KEY_FILE", ""),
		FileGRPCTLSClientCAFile:               envAllowEmptyOr("TELESRV_FILE_GRPC_TLS_CLIENT_CA_FILE", ""),
		FileGRPCTLSCAFile:                     envAllowEmptyOr("TELESRV_FILE_GRPC_TLS_CA_FILE", ""),
		FileGRPCTLSServerName:                 envAllowEmptyOr("TELESRV_FILE_GRPC_TLS_SERVER_NAME", ""),
		FileGRPCTLSClientCertFile:             envAllowEmptyOr("TELESRV_FILE_GRPC_TLS_CLIENT_CERT_FILE", ""),
		FileGRPCTLSClientKeyFile:              envAllowEmptyOr("TELESRV_FILE_GRPC_TLS_CLIENT_KEY_FILE", ""),
		FileToken:                             envOr("TELESRV_FILE_TOKEN", ""),
		PublicBaseURL:                         publicBaseURL,
		Branding:                              brandConfig,
		UpdatePublicURL:                       updatePublicURL,
		UpdateServiceURL:                      updateServiceURL,
		UpdateRequestTimeout:                  envDurationOr("TELESRV_UPDATE_REQUEST_TIMEOUT", 2*time.Second),
		PublicAppScheme:                       publicAppScheme,
		PublicAppLinkBase:                     publicAppLinkBase,
		PublicWebBaseURL:                      publicWebBaseURL,
		PublicAppName:                         publicAppName,
		PublicLinkWebAddr:                     envAllowEmptyOr("TELESRV_PUBLIC_LINK_WEB_ADDR", ""),
		GiftPreviewCacheDir:                   envAllowEmptyOr("TELESRV_GIFT_PREVIEW_CACHE_DIR", ""),
		GiftPreviewRendererURL:                envAllowEmptyOr("TELESRV_GIFT_PREVIEW_RENDERER_URL", ""),
		TelegramLoginEnabled:                  envBoolOr("TELESRV_TELEGRAM_LOGIN_ENABLE", false),
		TelegramLoginIssuer:                   strings.TrimSuffix(envOr("TELESRV_TELEGRAM_LOGIN_ISSUER", publicBaseURL), "/"),
		TelegramLoginAllowHTTP:                envBoolOr("TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP", false),
		TelegramLoginSigningKeysFile:          envOr("TELESRV_TELEGRAM_LOGIN_SIGNING_KEYS_FILE", "data/telegram-login/signing-keys.json"),
		TelegramLoginCodeKeysFile:             envOr("TELESRV_TELEGRAM_LOGIN_CODE_KEYS_FILE", "data/telegram-login/code-keys.json"),
		TelegramLoginSecretPepperFile:         envOr("TELESRV_TELEGRAM_LOGIN_SECRET_PEPPER_FILE", "data/telegram-login/client-secret-pepper"),
		TelegramLoginRequestTTL:               envDurationOr("TELESRV_TELEGRAM_LOGIN_REQUEST_TTL", 5*time.Minute),
		TelegramLoginCodeTTL:                  envDurationOr("TELESRV_TELEGRAM_LOGIN_CODE_TTL", 2*time.Minute),
		TelegramLoginIDTokenTTL:               envDurationOr("TELESRV_TELEGRAM_LOGIN_ID_TOKEN_TTL", time.Hour),
		TelegramLoginTrustedProxyCIDRs:        envListOr("TELESRV_TELEGRAM_LOGIN_TRUSTED_PROXY_CIDRS", nil),
		TelegramLoginRetention:                envDurationOr("TELESRV_TELEGRAM_LOGIN_RETENTION", 7*24*time.Hour),
		TelegramLoginSweepInterval:            envDurationOr("TELESRV_TELEGRAM_LOGIN_SWEEP_INTERVAL", 5*time.Minute),
		TelegramLoginSweepBatch:               envIntOr("TELESRV_TELEGRAM_LOGIN_SWEEP_BATCH", 500),
		AdminUIPermissions:                    envListOr("TELESRV_ADMIN_UI_PERMISSIONS", []string{adminPermissionAll}),
		AdminScopedTokens:                     adminScopedTokens,
		GeoIPEndpoints:                        envListOr("TELESRV_GEOIP_ENDPOINTS", nil),
		GeoIPTimeout:                          envDurationOr("TELESRV_GEOIP_TIMEOUT", 2*time.Second),
		GeoIPConcurrency:                      envIntOr("TELESRV_GEOIP_CONCURRENCY", 4),
		GeoIPCacheTTL:                         envDurationOr("TELESRV_GEOIP_CACHE_TTL", 24*time.Hour),
		GeoIPNegativeTTL:                      envDurationOr("TELESRV_GEOIP_NEGATIVE_TTL", 5*time.Minute),
		GeoIPCacheSize:                        envIntOr("TELESRV_GEOIP_CACHE_SIZE", 4096),
		GeoIPRateLimitThreshold:               envIntOr("TELESRV_GEOIP_RATE_LIMIT_THRESHOLD", 3),
		GeoIPRateLimitCooldown:                envDurationOr("TELESRV_GEOIP_RATE_LIMIT_COOLDOWN", 2*time.Minute),
		GeoIPDownThreshold:                    envIntOr("TELESRV_GEOIP_DOWN_THRESHOLD", 2),
		GeoIPDownCooldown:                     envDurationOr("TELESRV_GEOIP_DOWN_COOLDOWN", 30*time.Second),
		AdminUIAddr:                           envOr("TELESRV_ADMIN_UI_ADDR", "127.0.0.1:2600"),
		AdminUIPassword:                       envOr("TELESRV_ADMIN_UI_PASSWORD", ""),
		AdminUIToken:                          envOr("TELESRV_ADMIN_UI_TOKEN", ""),
		AdminSessionKey:                       envOr("TELESRV_ADMIN_SESSION_KEY", ""),

		// Use 127.0.0.1 rather than localhost: on Windows localhost first resolves to IPv6 ::1, while Docker
		// Desktop's port forwarding only listens on IPv4, so an IPv6 connection waits for a ~1s timeout before
		// falling back to IPv4 (measured: localhost connects in 1.0s vs 6ms for 127.0.0.1). During a cold
		// connection surge each new pool connection would wait 1s -> a pre-handler thundering-herd stall.
		// Production overrides it with TELESRV_POSTGRES_DSN; this default applies only to local development.
		PostgresDSN:                     envOr("TELESRV_POSTGRES_DSN", "postgres://telesrv:telesrv@127.0.0.1:5432/telesrv_v2?sslmode=disable"),
		PostgresMaxConns:                envIntOr("TELESRV_POSTGRES_MAX_CONNS", 50),
		PostgresMinConns:                envIntOr("TELESRV_POSTGRES_MIN_CONNS", 16),
		PostgresCounterRecoveryMaxConns: envIntOr("TELESRV_POSTGRES_COUNTER_RECOVERY_MAX_CONNS", 4),
		RedisAddr:                       envOr("TELESRV_REDIS_ADDR", "127.0.0.1:6399"), // likewise avoid the localhost->IPv6 fallback delay
		RedisPassword:                   envOr("TELESRV_REDIS_PASSWORD", ""),
		RedisDB:                         envIntOr("TELESRV_REDIS_DB", 0),
		InstanceID:                      strings.TrimSpace(envAllowEmptyOr("TELESRV_INSTANCE_ID", "")),
		EdgeLocationTTL:                 envDurationOr("TELESRV_EDGE_LOCATION_TTL", 90*time.Second),
		EdgeLocationHeartbeatInterval: envDurationOr(
			"TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL", 30*time.Second,
		),

		DevAuthCode:                       envOr("TELESRV_DEV_AUTH_CODE", "12345"),
		WelcomeMessagePhoneTemplate:       envOr("TELESRV_WELCOME_MESSAGE_PHONE_TEMPLATE", domain.DefaultWelcomeMessagePhoneTemplate),
		WelcomeMessageEmailTemplate:       envOr("TELESRV_WELCOME_MESSAGE_EMAIL_TEMPLATE", domain.DefaultWelcomeMessageEmailTemplate),
		LoginCodeMessageTemplate:          envOr("TELESRV_LOGIN_CODE_MESSAGE_TEMPLATE", domain.DefaultLoginCodeMessageTemplate),
		AuthCodeTTL:                       envDurationOr("TELESRV_AUTH_CODE_TTL", 5*time.Minute),
		PhoneCodeLength:                   envIntOr("TELESRV_PHONE_CODE_LENGTH", 5),
		AuthCodeMaxAttempts:               envIntOr("TELESRV_AUTH_CODE_MAX_ATTEMPTS", 5),
		AuthCodePhoneRateLimit:            envIntOr("TELESRV_AUTH_CODE_PHONE_RATE_LIMIT", 5),
		AuthCodeAuthKeyRateLimit:          envIntOr("TELESRV_AUTH_CODE_AUTH_KEY_RATE_LIMIT", 20),
		AuthCodeRateWindow:                envDurationOr("TELESRV_AUTH_CODE_RATE_WINDOW", 10*time.Minute),
		LoginEmailEnable:                  envBoolOr("TELESRV_LOGIN_EMAIL_ENABLE", false),
		LoginEmailRequireSetup:            envBoolOr("TELESRV_LOGIN_EMAIL_REQUIRE_SETUP", false),
		LoginEmailCodeLength:              envIntOr("TELESRV_LOGIN_EMAIL_CODE_LENGTH", 6),
		PhoneCodeDeliveryProvider:         strings.ToLower(strings.TrimSpace(envOr("TELESRV_PHONE_CODE_DELIVERY_PROVIDER", "development"))),
		EmailCodeDeliveryProvider:         strings.ToLower(strings.TrimSpace(envOr("TELESRV_EMAIL_CODE_DELIVERY_PROVIDER", "smtp"))),
		OTPWebhookURL:                     envOr("TELESRV_OTP_WEBHOOK_URL", ""),
		OTPWebhookSecret:                  envOr("TELESRV_OTP_WEBHOOK_SECRET", ""),
		OTPWebhookTimeout:                 envDurationOr("TELESRV_OTP_WEBHOOK_TIMEOUT", 5*time.Second),
		SMTPHost:                          envOr("TELESRV_SMTP_HOST", ""),
		SMTPPort:                          envIntOr("TELESRV_SMTP_PORT", 587),
		SMTPUsername:                      envOr("TELESRV_SMTP_USERNAME", ""),
		SMTPPassword:                      envOr("TELESRV_SMTP_PASSWORD", ""),
		SMTPFrom:                          envOr("TELESRV_SMTP_FROM", ""),
		SMTPFromName:                      envOr("TELESRV_SMTP_FROM_NAME", brandConfig.ProductName),
		SMTPTLSMode:                       strings.ToLower(strings.TrimSpace(envOr("TELESRV_SMTP_TLS", "starttls"))),
		SMTPTimeout:                       envDurationOr("TELESRV_SMTP_TIMEOUT", 10*time.Second),
		LangPackSeedDir:                   envOr("TELESRV_LANGPACK_SEED_DIR", "data/langpack"),
		OfficialGiftsDir:                  envOr("TELESRV_OFFICIAL_GIFTS_DIR", "data/official-gifts"),
		StarGiftTONStartingGrant:          envInt64Or("TELESRV_STARGIFT_TON_STARTING_GRANT", 10_000_000_000),
		BlobBackendKind:                   strings.ToLower(strings.TrimSpace(envOr("TELESRV_BLOB_BACKEND", "localfs"))),
		BlobDir:                           envOr("TELESRV_BLOB_DIR", "data/blobs"),
		BlobStagingDir:                    envOr("TELESRV_BLOB_STAGING_DIR", "data/blob-staging"),
		S3Endpoint:                        strings.TrimSpace(envOr("TELESRV_S3_ENDPOINT", "")),
		S3Region:                          strings.TrimSpace(envOr("TELESRV_S3_REGION", "us-east-1")),
		S3Bucket:                          strings.TrimSpace(envOr("TELESRV_S3_BUCKET", "")),
		S3AccessKeyID:                     envOr("TELESRV_S3_ACCESS_KEY_ID", ""),
		S3SecretAccessKey:                 envOr("TELESRV_S3_SECRET_ACCESS_KEY", ""),
		S3UseSSL:                          envBoolOr("TELESRV_S3_USE_SSL", true),
		S3PathStyle:                       envBoolOr("TELESRV_S3_PATH_STYLE", false),
		S3CreateBucket:                    envBoolOr("TELESRV_S3_CREATE_BUCKET", false),
		StorageLowSpaceGuardEnable:        envBoolOr("TELESRV_STORAGE_LOW_SPACE_GUARD_ENABLE", true),
		StorageMinFreeBytes:               envInt64Or("TELESRV_STORAGE_MIN_FREE_BYTES", 1<<30),
		StorageMaxTotalBytes:              envInt64Or("TELESRV_STORAGE_MAX_TOTAL_BYTES", 0),
		StorageUsageRefreshInterval:       envDurationOr("TELESRV_STORAGE_USAGE_REFRESH_INTERVAL", time.Minute),
		StickerSeedDir:                    envOr("TELESRV_STICKER_SEED_DIR", "data/sticker-seed"),
		GifSeedDir:                        envOr("TELESRV_GIF_SEED_DIR", "data/gifs"),
		StickerSeedMaxSets:                envIntOr("TELESRV_STICKER_SEED_MAX_SETS", 300),
		PremiumPromoSeedDir:               envOr("TELESRV_PREMIUM_PROMO_SEED_DIR", "data/premium-promo"),
		IdentityDir:                       envOr("TELESRV_IDENTITY_DIR", "data/identity"),
		MapboxToken:                       envOr("TELESRV_MAPBOX_TOKEN", ""),
		MapTileCacheDir:                   envOr("TELESRV_MAPTILE_CACHE_DIR", "data/maptiles"),
		ExternalMediaEnable:               envBoolOr("TELESRV_EXTERNAL_MEDIA_ENABLE", true),
		ExternalMediaMaxBytes:             int64(envIntOr("TELESRV_EXTERNAL_MEDIA_MAX_BYTES", 10<<20)),
		ExternalMediaRatePerMin:           envIntOr("TELESRV_EXTERNAL_MEDIA_RATE_PER_MIN", 60),
		WebPagePreviewEnable:              envBoolOr("TELESRV_WEBPAGE_PREVIEW_ENABLE", true),
		WebPagePreviewMaxBytes:            int64(envIntOr("TELESRV_WEBPAGE_PREVIEW_MAX_BYTES", 5<<20)),
		WebPagePreviewRatePerMin:          envIntOr("TELESRV_WEBPAGE_PREVIEW_RATE_PER_MIN", 300),
		BusinessAIProvider:                envOr("TELESRV_BUSINESS_AI_PROVIDER", "echo"),
		AIEnabled:                         envBoolOr("TELESRV_AI_ENABLED", true),
		AIProviders:                       loadAIProviders(fileEnv),
		AITimeout:                         envDurationOr("TELESRV_AI_TIMEOUT", 15*time.Second),
		SRPRateLimit:                      envIntOr("TELESRV_SRP_RATE_LIMIT", 10),
		SRPRateWindow:                     envDurationOr("TELESRV_SRP_RATE_WINDOW", time.Minute),
		WithdrawalRateLimit:               envIntOr("TELESRV_WITHDRAWAL_RATE_LIMIT", 5),
		WithdrawalRateWindow:              envDurationOr("TELESRV_WITHDRAWAL_RATE_WINDOW", time.Minute),
		PaymentRateLimit:                  envIntOr("TELESRV_PAYMENT_RATE_LIMIT", 30),
		PaymentRateWindow:                 envDurationOr("TELESRV_PAYMENT_RATE_WINDOW", time.Minute),
		AIRateLimit:                       envIntOr("TELESRV_AI_RATE_LIMIT", 20),
		AIRateWindow:                      envDurationOr("TELESRV_AI_RATE_WINDOW", time.Minute),
		AIPrivacyLogContent:               envBoolOr("TELESRV_AI_LOG_CONTENT", false),
		TranslationEnabled:                envBoolOr("TELESRV_TRANSLATION_ENABLED", true),
		TranslationProviders:              envListOr("TELESRV_TRANSLATION_PROVIDERS", []string{}),
		TranslationTimeout:                envDurationOr("TELESRV_TRANSLATION_TIMEOUT", 15*time.Second),
		TranslationRateLimit:              envIntOr("TELESRV_TRANSLATION_RATE_LIMIT", 60),
		TranslationRateWindow:             envDurationOr("TELESRV_TRANSLATION_RATE_WINDOW", time.Minute),
		ASRURL:                            strings.TrimSpace(envOr("TELESRV_ASR_URL", "")),
		ASRTimeout:                        envDurationOr("TELESRV_ASR_TIMEOUT", 120*time.Second),
		DonationWalletKeyFile:             strings.TrimSpace(envOr("TELESRV_DONATION_WALLET_KEY_FILE", "")),
		DonationPollInterval:              envDurationOr("TELESRV_DONATION_POLL_INTERVAL", 5*time.Second),
		TempKeyResolveCacheMaxEntries:     envIntOr("TELESRV_TEMP_KEY_CACHE_MAX_ENTRIES", 262144),
		TempKeyResolveCacheTTL:            envDurationOr("TELESRV_TEMP_KEY_CACHE_TTL", 30*time.Minute),
		ReadModelVersionCacheMaxEntries:   envIntOr("TELESRV_READ_MODEL_VERSION_CACHE_MAX", 1_000_000),
		ReadModelVersionBatchMaxKeys:      envIntOr("TELESRV_READ_MODEL_VERSION_BATCH_MAX_KEYS", 4096),
		ReadModelVersionBatchWait:         envDurationOr("TELESRV_READ_MODEL_VERSION_BATCH_WAIT", 250*time.Microsecond),
		ReadModelVersionBatchQueue:        envIntOr("TELESRV_READ_MODEL_VERSION_BATCH_QUEUE", 16_384),
		ReadModelVersionBatchTimeout:      envDurationOr("TELESRV_READ_MODEL_VERSION_BATCH_TIMEOUT", 5*time.Second),
		AuthKeyGetBatchMax:                envIntOr("TELESRV_AUTH_KEY_GET_BATCH_MAX", 256),
		AuthKeyGetBatchWait:               envDurationOr("TELESRV_AUTH_KEY_GET_BATCH_WAIT", 250*time.Microsecond),
		AuthKeyGetBatchQueue:              envIntOr("TELESRV_AUTH_KEY_GET_BATCH_QUEUE", 16_384),
		AuthKeyGetBatchTimeout:            envDurationOr("TELESRV_AUTH_KEY_GET_BATCH_TIMEOUT", 5*time.Second),
		ContactReverseBatchMaxPairs:       envIntOr("TELESRV_CONTACT_REVERSE_BATCH_MAX_PAIRS", 4096),
		ContactReverseBatchWait:           envDurationOr("TELESRV_CONTACT_REVERSE_BATCH_WAIT", 2*time.Millisecond),
		ContactReverseBatchQueue:          envIntOr("TELESRV_CONTACT_REVERSE_BATCH_QUEUE", 16_384),
		ContactReverseBatchTimeout:        envDurationOr("TELESRV_CONTACT_REVERSE_BATCH_TIMEOUT", 5*time.Second),
		ContactSnapshotCacheMaxViewers:    envIntOr("TELESRV_CONTACT_SNAPSHOT_CACHE_MAX_VIEWERS", 16_384),
		ProfilePhotoCacheMaxEntries:       envIntOr("TELESRV_PROFILE_PHOTO_CACHE_MAX", 200_000),
		ProfilePhotoCacheTTL:              envDurationOr("TELESRV_PROFILE_PHOTO_CACHE_TTL", 24*time.Hour),
		PeerIdentityCacheMaxEntries:       envIntOr("TELESRV_PEER_IDENTITY_CACHE_MAX", 1_000_000),
		DialogPrivatePeerCacheMaxEntries:  envIntOr("TELESRV_DIALOG_PRIVATE_PEER_CACHE_MAX", 500_000),
		DialogPrivatePeerCacheMaxBytes:    envInt64Or("TELESRV_DIALOG_PRIVATE_PEER_CACHE_BYTES_MAX", 256<<20),
		DialogDraftCacheMaxEntries:        envIntOr("TELESRV_DIALOG_DRAFT_CACHE_MAX", 1_000_000),
		DialogDraftCacheMaxBytes:          envInt64Or("TELESRV_DIALOG_DRAFT_CACHE_BYTES_MAX", 256<<20),
		UserProjectionFactCacheMaxEntries: envIntOr("TELESRV_USER_PROJECTION_FACT_CACHE_MAX", 1_000_000),
		StoryActivePeerCacheMaxEntries:    envIntOr("TELESRV_STORY_ACTIVE_PEER_CACHE_MAX", 1_000_000),
		StoryHiddenListCacheMaxEntries:    envIntOr("TELESRV_STORY_HIDDEN_LIST_CACHE_MAX", 100_000),
		StoryHiddenListCacheMaxBytes:      envInt64Or("TELESRV_STORY_HIDDEN_LIST_CACHE_BYTES_MAX", 64<<20),
		DialogListSnapshotCacheMaxEntries: envIntOr("TELESRV_DIALOG_LIST_SNAPSHOT_CACHE_MAX", 10_000),
		DialogListSnapshotCacheMaxHeaders: envInt64Or("TELESRV_DIALOG_LIST_SNAPSHOT_HEADERS_MAX", 1_000_000),
		DialogListSnapshotCacheTTL:        envDurationOr("TELESRV_DIALOG_LIST_SNAPSHOT_CACHE_TTL", 5*time.Minute),
		DialogListSnapshotRedisTTL:        envDurationOr("TELESRV_DIALOG_LIST_SNAPSHOT_REDIS_TTL", time.Hour),
		ActiveChannelIDsCacheMaxEntries:   envIntOr("TELESRV_ACTIVE_CHANNEL_IDS_CACHE_MAX", 32_768),
		ActiveChannelIDsCacheTTL:          envDurationOr("TELESRV_ACTIVE_CHANNEL_IDS_CACHE_TTL", 24*time.Hour),
		ActiveChannelIDsRedisTTL:          envDurationOr("TELESRV_ACTIVE_CHANNEL_IDS_REDIS_TTL", 24*time.Hour),
		ActiveChannelIDsBatchMax:          envIntOr("TELESRV_ACTIVE_CHANNEL_IDS_BATCH_MAX", 128),
		ActiveChannelIDsBatchWait:         envDurationOr("TELESRV_ACTIVE_CHANNEL_IDS_BATCH_WAIT", 100*time.Millisecond),
		ActiveChannelIDsBatchQueue:        envIntOr("TELESRV_ACTIVE_CHANNEL_IDS_BATCH_QUEUE", 16_384),
		ActiveChannelIDsBatchTimeout:      envDurationOr("TELESRV_ACTIVE_CHANNEL_IDS_BATCH_TIMEOUT", 5*time.Second),
		BootstrapReadyBatchMax:            envIntOr("TELESRV_BOOTSTRAP_READY_BATCH_MAX", 32),
		BootstrapReadyBatchWait:           envDurationOr("TELESRV_BOOTSTRAP_READY_BATCH_WAIT", 100*time.Millisecond),
		BootstrapReadyBatchQueue:          envIntOr("TELESRV_BOOTSTRAP_READY_BATCH_QUEUE", 16_384),
		BootstrapReadyBatchTimeout:        envDurationOr("TELESRV_BOOTSTRAP_READY_BATCH_TIMEOUT", 5*time.Second),
		PresenceLastSeenBatchMax:          envIntOr("TELESRV_PRESENCE_LAST_SEEN_BATCH_MAX", 512),
		PresenceLastSeenBatchWait:         envDurationOr("TELESRV_PRESENCE_LAST_SEEN_BATCH_WAIT", time.Second),
		PresenceLastSeenBatchQueue:        envIntOr("TELESRV_PRESENCE_LAST_SEEN_BATCH_QUEUE", 65_536),
		PresenceLastSeenBatchTimeout:      envDurationOr("TELESRV_PRESENCE_LAST_SEEN_BATCH_TIMEOUT", 5*time.Second),
		PresenceLastSeenDrainTimeout:      envDurationOr("TELESRV_PRESENCE_LAST_SEEN_DRAIN_TIMEOUT", 10*time.Second),
		AuthUserCacheTTL:                  envDurationOr("TELESRV_AUTH_USER_CACHE_TTL", time.Minute),
		AuthKeyCacheMaxEntries:            envIntOr("TELESRV_AUTH_KEY_CACHE_MAX_ENTRIES", 262144),
		AuthKeyCacheTTL:                   envDurationOr("TELESRV_AUTH_KEY_CACHE_TTL", 30*time.Minute),
		ChannelRowCacheMaxEntries:         envIntOr("TELESRV_CHANNEL_ROW_CACHE_MAX", 50000),
		ChannelTopMessageCacheMaxEntries:  envIntOr("TELESRV_CHANNEL_TOP_MESSAGE_CACHE_MAX", 100_000),
		ChannelMemberCacheMaxEntries:      envIntOr("TELESRV_CHANNEL_MEMBER_CACHE_MAX", 1_000_000),
		ChannelDialogCacheMaxEntries:      envIntOr("TELESRV_CHANNEL_DIALOG_CACHE_MAX", 1_000_000),
		ChannelDifferenceCacheMaxEntries:  envIntOr("TELESRV_CHANNEL_DIFFERENCE_CACHE_MAX", 8192),
		ChannelDifferenceCacheMaxBytes:    envInt64Or("TELESRV_CHANNEL_DIFFERENCE_CACHE_BYTES_MAX", 256<<20),
		ChannelDifferenceCacheTTL:         envDurationOr("TELESRV_CHANNEL_DIFFERENCE_CACHE_TTL", 5*time.Minute),
		ChannelBoostCacheMaxEntries:       envIntOr("TELESRV_CHANNEL_BOOST_CACHE_MAX", 100000),
		ChannelBoostCacheTTL:              envDurationOr("TELESRV_CHANNEL_BOOST_CACHE_TTL", 10*time.Second),

		OutboxWorkers:              envIntOr("TELESRV_OUTBOX_WORKERS", 4),
		OutboxBatch:                envIntOr("TELESRV_OUTBOX_BATCH", 100),
		OutboxLeaseTimeout:         envDurationOr("TELESRV_OUTBOX_LEASE_TIMEOUT", 30*time.Second),
		DeliveryAttemptTimeout:     envDurationOr("TELESRV_DELIVERY_ATTEMPT_TIMEOUT", 2*time.Second),
		DeliveryClockSkewAllowance: envDurationOr("TELESRV_DELIVERY_CLOCK_SKEW_ALLOWANCE", time.Second),
		OutboundPushTimeout:        envDurationOr("TELESRV_OUTBOUND_PUSH_TIMEOUT", 200*time.Millisecond),
		SendRateLimit:              envIntOr("TELESRV_SEND_RATE_LIMIT", 0),
		SendRateWindow:             envDurationOr("TELESRV_SEND_RATE_WINDOW", time.Minute),
		CatchupRateLimit:           envIntOr("TELESRV_CATCHUP_RATE_LIMIT", 0),
		CatchupRateWindow:          envDurationOr("TELESRV_CATCHUP_RATE_WINDOW", time.Minute),
		ChannelNudgeMaxTargets:     envIntOr("TELESRV_CHANNEL_NUDGE_MAX_TARGETS", 0),
		UpdateEventRetention:       envDurationOr("TELESRV_UPDATE_EVENT_RETENTION", 168*time.Hour),
		BotAPIUpdateRetention:      envDurationOr("TELESRV_BOT_API_UPDATE_RETENTION", 24*time.Hour),
		OrphanAuthKeyRetention:     envDurationOr("TELESRV_ORPHAN_AUTH_KEY_RETENTION", 24*time.Hour),
		RetentionInterval:          envDurationOr("TELESRV_RETENTION_INTERVAL", time.Hour),
		RetentionBatch:             envIntOr("TELESRV_RETENTION_BATCH", 10000),
		UploadPartTTL:              envDurationOr("TELESRV_UPLOAD_PART_TTL", 24*time.Hour),
		UploadPartGCInterval:       envDurationOr("TELESRV_UPLOAD_PART_GC_INTERVAL", 30*time.Minute),
		UploadPartGCBatch:          envIntOr("TELESRV_UPLOAD_PART_GC_BATCH", 10000),
		UploadInFlightMaxBytes:     envInt64Or("TELESRV_UPLOAD_INFLIGHT_MAX_BYTES", 4194304000),
		UploadInFlightMaxParts:     envIntOr("TELESRV_UPLOAD_INFLIGHT_MAX_PARTS", 8000),
		UploadInFlightMaxFiles:     envIntOr("TELESRV_UPLOAD_INFLIGHT_MAX_FILES", 64),

		CallRingTimeout:        envDurationOr("TELESRV_CALL_RING_TIMEOUT", 90*time.Second),
		CallTombstoneTTL:       envDurationOr("TELESRV_CALL_TOMBSTONE_TTL", 60*time.Second),
		CallMaxActivePerUser:   envIntOr("TELESRV_CALL_MAX_ACTIVE_PER_USER", 4),
		CallRegistryMaxEntries: envIntOr("TELESRV_CALL_REGISTRY_MAX_ENTRIES", 10_000),
		CallSignalingMaxBytes:  envIntOr("TELESRV_CALL_SIGNALING_MAX_BYTES", 65536),
		CallSignalingRate:      envIntOr("TELESRV_CALL_SIGNALING_RATE", 50),
		CallExpiryInterval:     envDurationOr("TELESRV_CALL_EXPIRY_INTERVAL", time.Second),

		PremiumGrantMonths:                 envIntOr("TELESRV_PREMIUM_GRANT_MONTHS", 3),
		PremiumBotUsername:                 premiumBotUsername,
		PremiumBotUserID:                   premiumBotUserID,
		PremiumPlans:                       premiumPlans,
		PasskeyRPID:                        envOr("TELESRV_PASSKEY_RP_ID", "telesrv.net"),
		PasskeyAllowedOrigins:              envListOr("TELESRV_PASSKEY_ALLOWED_ORIGINS", nil),
		StarsStartingGrant:                 int64(envIntOr("TELESRV_STARS_STARTING_GRANT", 1000)),
		PremiumSweepInterval:               envDurationOr("TELESRV_PREMIUM_SWEEP_INTERVAL", time.Minute),
		PremiumSweepBatch:                  envIntOr("TELESRV_PREMIUM_SWEEP_BATCH", 500),
		StarGiftSweepInterval:              envDurationOr("TELESRV_STARGIFT_SWEEP_INTERVAL", 15*time.Second),
		StarGiftSweepBatch:                 envIntOr("TELESRV_STARGIFT_SWEEP_BATCH", 1000),
		StarGiftTransferStars:              int64(envIntOr("TELESRV_STARGIFT_TRANSFER_STARS", 25)),
		StarGiftDropOriginalDetailsStars:   int64(envIntOr("TELESRV_STARGIFT_DROP_DETAILS_STARS", 25)),
		StarGiftOfferMinStars:              envIntOr("TELESRV_STARGIFT_OFFER_MIN_STARS", 1),
		StarGiftStarsProceedsPermille:      envIntOr("TELESRV_STARGIFT_STARS_PROCEEDS_PERMILLE", 1000),
		StarGiftTONProceedsPermille:        envIntOr("TELESRV_STARGIFT_TON_PROCEEDS_PERMILLE", 1000),
		StarGiftResaleMaxStars:             envInt64Or("TELESRV_STARGIFT_RESALE_MAX_STARS", 250_000),
		StarGiftResaleMaxTON:               envInt64Or("TELESRV_STARGIFT_RESALE_MAX_TON", 10_000),
		StarGiftResaleMinStars:             envInt64Or("TELESRV_STARGIFT_RESALE_MIN_STARS", domain.MinStarGiftResaleStars),
		StarGiftTONStarsRate:               envInt64Or("TELESRV_STARGIFT_TON_STARS_RATE", 100),
		StarGiftExportDelay:                envDurationOr("TELESRV_STARGIFT_EXPORT_DELAY", 0),
		StarGiftTransferDelay:              envDurationOr("TELESRV_STARGIFT_TRANSFER_DELAY", 0),
		StarGiftResellDelay:                envDurationOr("TELESRV_STARGIFT_RESELL_DELAY", 0),
		StarGiftCraftDelay:                 envDurationOr("TELESRV_STARGIFT_CRAFT_DELAY", 0),
		StarGiftCraftChancePermille:        envIntOr("TELESRV_STARGIFT_CRAFT_CHANCE_PERMILLE", 250),
		StarGiftExportMode:                 strings.ToLower(strings.TrimSpace(envOr("TELESRV_STARGIFT_EXPORT_MODE", StarGiftExportModeLocal))),
		StarGiftTONNetwork:                 strings.ToLower(strings.TrimSpace(envOr("TELESRV_STARGIFT_TON_NETWORK", TONNetworkMainnet))),
		StarGiftTONCollectionAddress:       strings.TrimSpace(envOr("TELESRV_STARGIFT_TON_COLLECTION_ADDRESS", "")),
		StarGiftTONCollectionCodeHash:      strings.TrimSpace(envOr("TELESRV_STARGIFT_TON_COLLECTION_CODE_HASH", "")),
		StarGiftTONMintABI:                 strings.TrimSpace(envOr("TELESRV_STARGIFT_TON_MINT_ABI", "")),
		StarGiftTONInitialItemIndex:        strings.TrimSpace(envOr("TELESRV_STARGIFT_TON_INITIAL_ITEM_INDEX", "")),
		StarGiftTONProofDomain:             strings.ToLower(strings.TrimSpace(envOr("TELESRV_STARGIFT_TON_PROOF_DOMAIN", ""))),
		StarGiftTONCapabilitySecretFile:    strings.TrimSpace(envOr("TELESRV_STARGIFT_TON_CAPABILITY_SECRET_FILE", "")),
		StarGiftTONExportTTL:               envDurationOr("TELESRV_STARGIFT_TON_EXPORT_TTL", 30*time.Minute),
		StarGiftTONChallengeTTL:            envDurationOr("TELESRV_STARGIFT_TON_CHALLENGE_TTL", 5*time.Minute),
		StarGiftTONFinalizerBatch:          envIntOr("TELESRV_STARGIFT_TON_FINALIZER_BATCH", 20),
		StarGiftTONFinalizerPollInterval:   envDurationOr("TELESRV_STARGIFT_TON_FINALIZER_POLL_INTERVAL", 2*time.Second),
		StarGiftTONFinalizerLeaseTimeout:   envDurationOr("TELESRV_STARGIFT_TON_FINALIZER_LEASE_TIMEOUT", 30*time.Second),
		StarGiftTONFinalizerRequestTimeout: envDurationOr("TELESRV_STARGIFT_TON_FINALIZER_REQUEST_TIMEOUT", 10*time.Second),
		StarGiftTONFinalizerRetryDelay:     envDurationOr("TELESRV_STARGIFT_TON_FINALIZER_RETRY_DELAY", 5*time.Second),
		StarGiftTONClaimEnabled:            envBoolOr("TELESRV_STARGIFT_TON_CLAIM_ENABLED", false),
		StarGiftTONClaimBotTokenFile:       strings.TrimSpace(envOr("TELESRV_STARGIFT_TON_CLAIM_BOT_TOKEN_FILE", "")),
		StarGiftTONClaimInitDataTTL:        envDurationOr("TELESRV_STARGIFT_TON_CLAIM_INIT_DATA_TTL", 15*time.Minute),

		RatingEnabled:           envBoolOr("TELESRV_RATING_ENABLED", true),
		RatingPendingDelay:      envDurationOr("TELESRV_RATING_PENDING_DELAY", 24*time.Hour),
		RatingRecomputeInterval: envDurationOr("TELESRV_RATING_RECOMPUTE_INTERVAL", 15*time.Minute),
		RatingRecomputeBatch:    envIntOr("TELESRV_RATING_RECOMPUTE_BATCH", 500),
		RatingStaleAfter:        envDurationOr("TELESRV_RATING_STALE_AFTER", 6*time.Hour),
		// Weight defaults are read from the domain formula itself so the shipped
		// behaviour cannot drift from domain.DefaultAccountRatingWeights().
		RatingWeightStarsReceivedPermille: envInt64Or("TELESRV_RATING_WEIGHT_STARS_RECEIVED_PERMILLE", defaultRatingWeights.StarsReceivedPermille),
		RatingWeightStarsSpentPermille:    envInt64Or("TELESRV_RATING_WEIGHT_STARS_SPENT_PERMILLE", defaultRatingWeights.StarsSpentPermille),
		RatingWeightMessageSent:           envInt64Or("TELESRV_RATING_WEIGHT_MESSAGE_SENT", defaultRatingWeights.PerMessageSent),
		RatingWeightAccountAgeDay:         envInt64Or("TELESRV_RATING_WEIGHT_ACCOUNT_AGE_DAY", defaultRatingWeights.PerAccountAgeDay),
		RatingWeightGiftReceived:          envInt64Or("TELESRV_RATING_WEIGHT_GIFT_RECEIVED", defaultRatingWeights.PerGiftReceived),
		RatingWeightModerationCase:        envInt64Or("TELESRV_RATING_WEIGHT_MODERATION_CASE", defaultRatingWeights.PerModerationCase),
		RatingWeightScamPenalty:           envInt64Or("TELESRV_RATING_WEIGHT_SCAM_PENALTY", defaultRatingWeights.ScamPenalty),
		RatingWeightFakePenalty:           envInt64Or("TELESRV_RATING_WEIGHT_FAKE_PENALTY", defaultRatingWeights.FakePenalty),
		RatingActivityCap:                 envInt64Or("TELESRV_RATING_ACTIVITY_CAP", defaultRatingWeights.ActivityCap),
		CollectibleUsernameURLTemplate:    strings.TrimSpace(envAllowEmptyOr("TELESRV_COLLECTIBLE_USERNAME_URL_TEMPLATE", "")),

		// Official verification defaults ship the feature on with the official bar
		// in place: user accounts are not accepted, a rejection costs a month, and
		// an applicant can neither flood the queue nor keep an unbounded number of
		// applications open.
		VerificationEnabled:          envBoolOr("TELESRV_VERIFICATION_ENABLED", true),
		VerificationAllowUserTargets: envBoolOr("TELESRV_VERIFICATION_ALLOW_USER_TARGETS", false),
		VerificationRejectCooldown:   envDurationOr("TELESRV_VERIFICATION_REJECT_COOLDOWN", 720*time.Hour),
		VerificationApplyRateLimit:   envIntOr("TELESRV_VERIFICATION_APPLY_RATE_LIMIT", 3),
		VerificationApplyRateWindow:  envDurationOr("TELESRV_VERIFICATION_APPLY_RATE_WINDOW", 24*time.Hour),
		VerificationBotRateLimit:     envIntOr("TELESRV_VERIFICATION_BOT_RATE_LIMIT", 30),
		VerificationBotRateWindow:    envDurationOr("TELESRV_VERIFICATION_BOT_RATE_WINDOW", time.Minute),
		VerificationNotifyInterval:   envDurationOr("TELESRV_VERIFICATION_NOTIFY_INTERVAL", 15*time.Second),
		VerificationNotifyBatch:      envIntOr("TELESRV_VERIFICATION_NOTIFY_BATCH", 50),
		// Empty keeps the built-in English defaults: domain trims and falls
		// back when the stored text is blank (domain.SetModerationProfileWarning).
		ScamProfileWarning:           envOr("TELESRV_SCAM_WARNING", ""),
		FakeProfileWarning:           envOr("TELESRV_FAKE_WARNING", ""),
		BroadcastWorkerInterval:      envDurationOr("TELESRV_BROADCAST_WORKER_INTERVAL", 3*time.Second),
		BroadcastWorkerLease:         envDurationOr("TELESRV_BROADCAST_WORKER_LEASE", 30*time.Second),
		BroadcastMaterializeBatch:    envIntOr("TELESRV_BROADCAST_MATERIALIZE_BATCH", 200),
		BroadcastDeliveryBatch:       envIntOr("TELESRV_BROADCAST_DELIVERY_BATCH", 50),
		VerificationMaxActivePerUser: envIntOr("TELESRV_VERIFICATION_MAX_ACTIVE_PER_USER", 3),

		BotVerificationEnabled:        envBoolOr("TELESRV_BOT_VERIFICATION_ENABLED", true),
		BotVerificationMaxPerVerifier: envIntOr("TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER", domain.MaxCustomVerificationsPerVerifier),
		// The applicant budget is deliberately looser than the official one
		// (TELESRV_VERIFICATION_APPLY_RATE_LIMIT=3): a deployment can run many
		// verifier bots, and filing with a second company is not a retry of the first.
		BotVerificationRequestRateLimit:  envIntOr("TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT", 5),
		BotVerificationRequestRateWindow: envDurationOr("TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW", 24*time.Hour),

		GroupCallCheckTTL:        envDurationOr("TELESRV_GROUPCALL_CHECK_TTL", 45*time.Second),
		GroupCallSweepInterval:   envDurationOr("TELESRV_GROUPCALL_SWEEP_INTERVAL", 10*time.Second),
		GroupCallMaxParticipants: envIntOr("TELESRV_GROUPCALL_MAX_PARTICIPANTS", 32),

		TURNEnable:            envBoolOr("TELESRV_TURN_ENABLE", true),
		TURNUDPPort:           envIntOr("TELESRV_TURN_UDP_PORT", 12400),
		TURNBindIP:            envOr("TELESRV_TURN_BIND_IP", "0.0.0.0"),
		TURNAdvertiseIP:       envOr("TELESRV_TURN_ADVERTISE_IP", ""),
		TURNSecret:            envOr("TELESRV_TURN_SECRET", ""),
		TURNRelayMinPort:      envIntOr("TELESRV_TURN_RELAY_MIN_PORT", 12500),
		TURNRelayMaxPort:      envIntOr("TELESRV_TURN_RELAY_MAX_PORT", 12999),
		CallTURNCredentialTTL: envDurationOr("TELESRV_CALL_TURN_CREDENTIAL_TTL", 6*time.Hour),
		CallForceRelay:        envBoolOr("TELESRV_CALL_FORCE_RELAY", false),

		SFUUDPPort:                   envIntOr("TELESRV_SFU_UDP_PORT", 12399),
		SFUBindIP:                    envOr("TELESRV_SFU_BIND_IP", "0.0.0.0"),
		SFUAdvertiseIP:               envOr("TELESRV_SFU_ADVERTISE_IP", ""),
		SFUOwnerTTL:                  envDurationOr("TELESRV_SFU_OWNER_TTL", 2*time.Minute),
		SFUOwnerHeartbeatInterval:    envDurationOr("TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL", 30*time.Second),
		SFUInstanceTTL:               envDurationOr("TELESRV_SFU_INSTANCE_TTL", 90*time.Second),
		SFUInstanceHeartbeatInterval: envDurationOr("TELESRV_SFU_INSTANCE_HEARTBEAT_INTERVAL", 30*time.Second),
		SFUInstanceHealthTimeout:     envDurationOr("TELESRV_SFU_INSTANCE_HEALTH_TIMEOUT", time.Second),
		SFUInstanceMaxActiveCalls:    envIntOr("TELESRV_SFU_INSTANCE_MAX_ACTIVE_CALLS", 0),
		SFUControlGRPCAddr:           envAllowEmptyOr("TELESRV_SFU_CONTROL_GRPC_ADDR", ""),
		SFUControlGRPCURL:            envAllowEmptyOr("TELESRV_SFU_CONTROL_GRPC_URL", ""),
		SFUControlGRPCRequestTimeout: envDurationOr("TELESRV_SFU_CONTROL_GRPC_REQUEST_TIMEOUT", 5*time.Second),
		SFUControlGRPCTLSCertFile:    envAllowEmptyOr("TELESRV_SFU_CONTROL_GRPC_TLS_CERT_FILE", ""),
		SFUControlGRPCTLSKeyFile:     envAllowEmptyOr("TELESRV_SFU_CONTROL_GRPC_TLS_KEY_FILE", ""),
		SFUControlGRPCTLSClientCAFile: envAllowEmptyOr(
			"TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CA_FILE", "",
		),
		SFUControlGRPCTLSCAFile:         envAllowEmptyOr("TELESRV_SFU_CONTROL_GRPC_TLS_CA_FILE", ""),
		SFUControlGRPCTLSServerName:     envAllowEmptyOr("TELESRV_SFU_CONTROL_GRPC_TLS_SERVER_NAME", ""),
		SFUControlGRPCTLSClientCertFile: envAllowEmptyOr("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CERT_FILE", ""),
		SFUControlGRPCTLSClientKeyFile:  envAllowEmptyOr("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_KEY_FILE", ""),
		SFUControlToken:                 envOr("TELESRV_SFU_CONTROL_TOKEN", ""),

		LiveStreamEnable:      envBoolOr("TELESRV_LIVESTREAM_ENABLE", true),
		LiveStreamRtmpAddr:    envOr("TELESRV_LIVESTREAM_RTMP_ADDR", ":2400"),
		LiveStreamRtmpURL:     envOr("TELESRV_LIVESTREAM_RTMP_URL", ""),
		LiveStreamFFmpegPath:  envOr("TELESRV_LIVESTREAM_FFMPEG_PATH", "ffmpeg"),
		LiveStreamWorkDir:     envOr("TELESRV_LIVESTREAM_WORK_DIR", ""),
		LiveStreamSegmentKeep: envIntOr("TELESRV_LIVESTREAM_SEGMENT_KEEP", 32),
	}
	if err := validateLoginEmailConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateRPCExecutionConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validatePerformanceConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateStarGiftConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateAccountRatingConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateCollectibleUsernameConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateVerificationConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateAdminRBACConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateTelegramLoginConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateGeoIPConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateASRConfig(cfg); err != nil {
		return Config{}, err
	}
	if cfg.DonationPollInterval <= 0 {
		return Config{}, fmt.Errorf("TELESRV_DONATION_POLL_INTERVAL must be positive")
	}
	if err := validateBlobStorageConfig(cfg); err != nil {
		return Config{}, err
	}
	if cfg.AuthKeyCacheMaxEntries <= 0 {
		return Config{}, fmt.Errorf("TELESRV_AUTH_KEY_CACHE_MAX_ENTRIES must be greater than zero")
	}
	if cfg.AuthKeyCacheTTL <= 0 || cfg.AuthKeyCacheTTL > 24*time.Hour {
		return Config{}, fmt.Errorf("TELESRV_AUTH_KEY_CACHE_TTL must be greater than zero and at most 24h")
	}
	if cfg.DC <= 0 || int64(cfg.DC) > int64(1<<31-1) {
		return Config{}, fmt.Errorf("TELESRV_DC must be a positive TL int32")
	}
	if cfg.CoreExecGRPCRequestTimeout <= 0 || cfg.CoreExecGRPCRequestTimeout > time.Minute {
		return Config{}, fmt.Errorf("TELESRV_CORE_EXEC_GRPC_REQUEST_TIMEOUT must be greater than zero and at most 1m")
	}
	if err := validateCoreExecGRPCResolverConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateCoreExecGRPCTLSConfig(cfg); err != nil {
		return Config{}, err
	}
	if cfg.EgressDeliveryGRPCRequestTimeout <= 0 || cfg.EgressDeliveryGRPCRequestTimeout > time.Minute {
		return Config{}, fmt.Errorf("TELESRV_EGRESS_DELIVERY_GRPC_REQUEST_TIMEOUT must be greater than zero and at most 1m")
	}
	if cfg.DeliveryAttemptTimeout <= 0 || cfg.DeliveryAttemptTimeout > time.Minute {
		return Config{}, fmt.Errorf("TELESRV_DELIVERY_ATTEMPT_TIMEOUT must be greater than zero and at most 1m")
	}
	if cfg.DeliveryClockSkewAllowance <= 0 || cfg.DeliveryClockSkewAllowance > 10*time.Second {
		return Config{}, fmt.Errorf("TELESRV_DELIVERY_CLOCK_SKEW_ALLOWANCE must be greater than zero and at most 10s")
	}
	if cfg.OutboxLeaseTimeout <= cfg.DeliveryAttemptTimeout+cfg.DeliveryClockSkewAllowance+100*time.Millisecond {
		return Config{}, fmt.Errorf("TELESRV_OUTBOX_LEASE_TIMEOUT must exceed delivery attempt timeout plus clock skew allowance by more than 100ms")
	}
	if err := validateEgressDeliveryGRPCResolverConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateEgressDeliveryGRPCTLSConfig(cfg); err != nil {
		return Config{}, err
	}
	if cfg.FileGRPCRequestTimeout <= 0 || cfg.FileGRPCRequestTimeout > time.Minute {
		return Config{}, fmt.Errorf("TELESRV_FILE_GRPC_REQUEST_TIMEOUT must be greater than zero and at most 1m")
	}
	if err := validateFileGRPCResolverConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateFileGRPCTLSConfig(cfg); err != nil {
		return Config{}, err
	}
	if cfg.UpdateRequestTimeout <= 0 || cfg.UpdateRequestTimeout > 30*time.Second {
		return Config{}, fmt.Errorf("TELESRV_UPDATE_REQUEST_TIMEOUT must be greater than zero and at most 30s")
	}
	if err := validateEdgeLocationConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateSFUOwnerConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateEdgeLocationConfig(cfg Config) error {
	if cfg.EdgeLocationTTL <= 0 {
		return fmt.Errorf("TELESRV_EDGE_LOCATION_TTL must be positive")
	}
	if cfg.EdgeLocationHeartbeatInterval <= 0 {
		return fmt.Errorf("TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL must be positive")
	}
	if cfg.EdgeLocationHeartbeatInterval >= cfg.EdgeLocationTTL {
		return fmt.Errorf("TELESRV_EDGE_LOCATION_HEARTBEAT_INTERVAL must be less than TELESRV_EDGE_LOCATION_TTL")
	}
	return nil
}

func validateCoreExecGRPCResolverConfig(cfg Config) error {
	switch strings.ToLower(strings.TrimSpace(cfg.CoreExecGRPCResolver)) {
	case "", "static", "dns":
		return nil
	default:
		return fmt.Errorf("TELESRV_CORE_EXEC_GRPC_RESOLVER must be static or dns")
	}
}

func validateCoreExecGRPCTLSConfig(cfg Config) error {
	if (strings.TrimSpace(cfg.CoreExecGRPCTLSCertFile) == "") != (strings.TrimSpace(cfg.CoreExecGRPCTLSKeyFile) == "") {
		return fmt.Errorf("TELESRV_CORE_EXEC_GRPC_TLS_CERT_FILE and TELESRV_CORE_EXEC_GRPC_TLS_KEY_FILE must be configured together")
	}
	if strings.TrimSpace(cfg.CoreExecGRPCTLSClientCAFile) != "" &&
		(strings.TrimSpace(cfg.CoreExecGRPCTLSCertFile) == "" || strings.TrimSpace(cfg.CoreExecGRPCTLSKeyFile) == "") {
		return fmt.Errorf("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CA_FILE requires TELESRV_CORE_EXEC_GRPC_TLS_CERT_FILE and TELESRV_CORE_EXEC_GRPC_TLS_KEY_FILE")
	}
	if (strings.TrimSpace(cfg.CoreExecGRPCTLSClientCertFile) == "") != (strings.TrimSpace(cfg.CoreExecGRPCTLSClientKeyFile) == "") {
		return fmt.Errorf("TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_CERT_FILE and TELESRV_CORE_EXEC_GRPC_TLS_CLIENT_KEY_FILE must be configured together")
	}
	return nil
}

func validateEgressDeliveryGRPCResolverConfig(cfg Config) error {
	switch strings.ToLower(strings.TrimSpace(cfg.EgressDeliveryGRPCResolver)) {
	case "", "static", "dns":
		return nil
	default:
		return fmt.Errorf("TELESRV_EGRESS_DELIVERY_GRPC_RESOLVER must be static or dns")
	}
}

func validateEgressDeliveryGRPCTLSConfig(cfg Config) error {
	if (strings.TrimSpace(cfg.EgressDeliveryGRPCTLSCertFile) == "") != (strings.TrimSpace(cfg.EgressDeliveryGRPCTLSKeyFile) == "") {
		return fmt.Errorf("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CERT_FILE and TELESRV_EGRESS_DELIVERY_GRPC_TLS_KEY_FILE must be configured together")
	}
	if strings.TrimSpace(cfg.EgressDeliveryGRPCTLSClientCAFile) != "" &&
		(strings.TrimSpace(cfg.EgressDeliveryGRPCTLSCertFile) == "" || strings.TrimSpace(cfg.EgressDeliveryGRPCTLSKeyFile) == "") {
		return fmt.Errorf("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CA_FILE requires TELESRV_EGRESS_DELIVERY_GRPC_TLS_CERT_FILE and TELESRV_EGRESS_DELIVERY_GRPC_TLS_KEY_FILE")
	}
	if (strings.TrimSpace(cfg.EgressDeliveryGRPCTLSClientCertFile) == "") != (strings.TrimSpace(cfg.EgressDeliveryGRPCTLSClientKeyFile) == "") {
		return fmt.Errorf("TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_CERT_FILE and TELESRV_EGRESS_DELIVERY_GRPC_TLS_CLIENT_KEY_FILE must be configured together")
	}
	return nil
}

func validateFileGRPCResolverConfig(cfg Config) error {
	switch strings.ToLower(strings.TrimSpace(cfg.FileGRPCResolver)) {
	case "", "static", "dns":
		return nil
	default:
		return fmt.Errorf("TELESRV_FILE_GRPC_RESOLVER must be static or dns")
	}
}

func validateFileGRPCTLSConfig(cfg Config) error {
	if (strings.TrimSpace(cfg.FileGRPCTLSCertFile) == "") != (strings.TrimSpace(cfg.FileGRPCTLSKeyFile) == "") {
		return fmt.Errorf("TELESRV_FILE_GRPC_TLS_CERT_FILE and TELESRV_FILE_GRPC_TLS_KEY_FILE must be configured together")
	}
	if strings.TrimSpace(cfg.FileGRPCTLSClientCAFile) != "" &&
		(strings.TrimSpace(cfg.FileGRPCTLSCertFile) == "" || strings.TrimSpace(cfg.FileGRPCTLSKeyFile) == "") {
		return fmt.Errorf("TELESRV_FILE_GRPC_TLS_CLIENT_CA_FILE requires TELESRV_FILE_GRPC_TLS_CERT_FILE and TELESRV_FILE_GRPC_TLS_KEY_FILE")
	}
	if (strings.TrimSpace(cfg.FileGRPCTLSClientCertFile) == "") != (strings.TrimSpace(cfg.FileGRPCTLSClientKeyFile) == "") {
		return fmt.Errorf("TELESRV_FILE_GRPC_TLS_CLIENT_CERT_FILE and TELESRV_FILE_GRPC_TLS_CLIENT_KEY_FILE must be configured together")
	}
	return nil
}

func validateSFUOwnerConfig(cfg Config) error {
	if cfg.SFUOwnerTTL <= 0 {
		return fmt.Errorf("TELESRV_SFU_OWNER_TTL must be positive")
	}
	if cfg.SFUOwnerHeartbeatInterval <= 0 {
		return fmt.Errorf("TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL must be positive")
	}
	if cfg.SFUOwnerHeartbeatInterval >= cfg.SFUOwnerTTL {
		return fmt.Errorf("TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL must be less than TELESRV_SFU_OWNER_TTL")
	}
	if cfg.SFUInstanceTTL <= 0 {
		return fmt.Errorf("TELESRV_SFU_INSTANCE_TTL must be positive")
	}
	if cfg.SFUInstanceHeartbeatInterval <= 0 {
		return fmt.Errorf("TELESRV_SFU_INSTANCE_HEARTBEAT_INTERVAL must be positive")
	}
	if cfg.SFUInstanceHeartbeatInterval >= cfg.SFUInstanceTTL {
		return fmt.Errorf("TELESRV_SFU_INSTANCE_HEARTBEAT_INTERVAL must be less than TELESRV_SFU_INSTANCE_TTL")
	}
	if cfg.SFUInstanceHealthTimeout <= 0 {
		return fmt.Errorf("TELESRV_SFU_INSTANCE_HEALTH_TIMEOUT must be positive")
	}
	if cfg.SFUInstanceMaxActiveCalls < 0 {
		return fmt.Errorf("TELESRV_SFU_INSTANCE_MAX_ACTIVE_CALLS must be non-negative")
	}
	if cfg.SFUControlGRPCRequestTimeout <= 0 || cfg.SFUControlGRPCRequestTimeout > time.Minute {
		return fmt.Errorf("TELESRV_SFU_CONTROL_GRPC_REQUEST_TIMEOUT must be greater than zero and at most 1m")
	}
	if err := validateSFUControlGRPCTLSConfig(cfg); err != nil {
		return err
	}
	if strings.Contains(strings.TrimSpace(cfg.SFUControlToken), "\n") || strings.Contains(strings.TrimSpace(cfg.SFUControlToken), "\r") {
		return fmt.Errorf("TELESRV_SFU_CONTROL_TOKEN must not contain newlines")
	}
	if strings.Contains(strings.TrimSpace(cfg.GroupCallControlToken), "\n") || strings.Contains(strings.TrimSpace(cfg.GroupCallControlToken), "\r") {
		return fmt.Errorf("TELESRV_GROUPCALL_CONTROL_TOKEN must not contain newlines")
	}
	if strings.Contains(strings.TrimSpace(cfg.CoreExecToken), "\n") || strings.Contains(strings.TrimSpace(cfg.CoreExecToken), "\r") {
		return fmt.Errorf("TELESRV_CORE_EXEC_TOKEN must not contain newlines")
	}
	if strings.Contains(strings.TrimSpace(cfg.EgressDeliveryToken), "\n") || strings.Contains(strings.TrimSpace(cfg.EgressDeliveryToken), "\r") {
		return fmt.Errorf("TELESRV_EGRESS_DELIVERY_TOKEN must not contain newlines")
	}
	if strings.Contains(strings.TrimSpace(cfg.FileToken), "\n") || strings.Contains(strings.TrimSpace(cfg.FileToken), "\r") {
		return fmt.Errorf("TELESRV_FILE_TOKEN must not contain newlines")
	}
	return nil
}

func validateSFUControlGRPCTLSConfig(cfg Config) error {
	if (strings.TrimSpace(cfg.SFUControlGRPCTLSCertFile) == "") != (strings.TrimSpace(cfg.SFUControlGRPCTLSKeyFile) == "") {
		return fmt.Errorf("TELESRV_SFU_CONTROL_GRPC_TLS_CERT_FILE and TELESRV_SFU_CONTROL_GRPC_TLS_KEY_FILE must be configured together")
	}
	if strings.TrimSpace(cfg.SFUControlGRPCTLSClientCAFile) != "" &&
		(strings.TrimSpace(cfg.SFUControlGRPCTLSCertFile) == "" || strings.TrimSpace(cfg.SFUControlGRPCTLSKeyFile) == "") {
		return fmt.Errorf("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CA_FILE requires TELESRV_SFU_CONTROL_GRPC_TLS_CERT_FILE and TELESRV_SFU_CONTROL_GRPC_TLS_KEY_FILE")
	}
	if (strings.TrimSpace(cfg.SFUControlGRPCTLSClientCertFile) == "") != (strings.TrimSpace(cfg.SFUControlGRPCTLSClientKeyFile) == "") {
		return fmt.Errorf("TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_CERT_FILE and TELESRV_SFU_CONTROL_GRPC_TLS_CLIENT_KEY_FILE must be configured together")
	}
	return nil
}

func validateBlobStorageConfig(cfg Config) error {
	if cfg.StorageMinFreeBytes < 0 {
		return fmt.Errorf("TELESRV_STORAGE_MIN_FREE_BYTES must be non-negative")
	}
	if cfg.StorageMaxTotalBytes < 0 {
		return fmt.Errorf("TELESRV_STORAGE_MAX_TOTAL_BYTES must be non-negative")
	}
	if cfg.StorageLowSpaceGuardEnable && cfg.StorageUsageRefreshInterval <= 0 {
		return fmt.Errorf("TELESRV_STORAGE_USAGE_REFRESH_INTERVAL must be positive when storage capacity guard is enabled")
	}
	switch cfg.BlobBackendKind {
	case string(domain.MediaBackendLocalFS):
		if strings.TrimSpace(cfg.BlobDir) == "" {
			return fmt.Errorf("TELESRV_BLOB_DIR is required when TELESRV_BLOB_BACKEND=localfs")
		}
	case string(domain.MediaBackendS3):
		if strings.TrimSpace(cfg.BlobStagingDir) == "" {
			return fmt.Errorf("TELESRV_BLOB_STAGING_DIR is required when TELESRV_BLOB_BACKEND=s3")
		}
		if cfg.S3Endpoint == "" || strings.Contains(cfg.S3Endpoint, "://") {
			return fmt.Errorf("TELESRV_S3_ENDPOINT must be host[:port] without a URL scheme when TELESRV_BLOB_BACKEND=s3")
		}
		if cfg.S3Bucket == "" {
			return fmt.Errorf("TELESRV_S3_BUCKET is required when TELESRV_BLOB_BACKEND=s3")
		}
		if strings.TrimSpace(cfg.S3AccessKeyID) == "" || strings.TrimSpace(cfg.S3SecretAccessKey) == "" {
			return fmt.Errorf("TELESRV_S3_ACCESS_KEY_ID and TELESRV_S3_SECRET_ACCESS_KEY are required when TELESRV_BLOB_BACKEND=s3")
		}
	default:
		return fmt.Errorf("TELESRV_BLOB_BACKEND must be localfs or s3, got %q", cfg.BlobBackendKind)
	}
	return nil
}

func parsePremiumPlans(raw string) ([]domain.PremiumPlan, error) {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	if len(parts) == 0 {
		return nil, fmt.Errorf("at least one plan is required")
	}
	plans := make([]domain.PremiumPlan, 0, len(parts))
	seen := make(map[int]struct{}, len(parts))
	for index, item := range parts {
		fields := strings.Split(strings.TrimSpace(item), ":")
		if len(fields) != 3 {
			return nil, fmt.Errorf("plan %q must use months:days:stars", item)
		}
		months, errMonths := strconv.Atoi(strings.TrimSpace(fields[0]))
		days, errDays := strconv.Atoi(strings.TrimSpace(fields[1]))
		amount, errAmount := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
		if errMonths != nil || errDays != nil || errAmount != nil ||
			months <= 0 || days <= 0 || amount <= 0 {
			return nil, fmt.Errorf("plan %q contains a non-positive integer", item)
		}
		if _, exists := seen[months]; exists {
			return nil, fmt.Errorf("duplicate %d-month plan", months)
		}
		seen[months] = struct{}{}
		plan := domain.PremiumPlan{
			Months: months, DurationDays: days, AmountStars: amount,
			Enabled: true, SortOrder: (index + 1) * 10,
			Label: fmt.Sprintf("%d months", months), Version: 1,
		}
		if !plan.Valid() {
			return nil, fmt.Errorf("plan %q exceeds Premium catalog bounds", item)
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func normalizeDefaultCountryCode(raw string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(raw))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return "", fmt.Errorf("must be a two-letter ISO 3166-1 alpha-2 code")
	}
	region, err := language.ParseRegion(code)
	if err != nil || !region.IsCountry() {
		return "", fmt.Errorf("must identify a country or autonomous area")
	}
	return region.String(), nil
}

func normalizeAdvertiseIP(raw string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("must be an IPv4 or IPv6 address: %w", err)
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsMulticast() || addr.Zone() != "" {
		return "", fmt.Errorf("must be a unicast address usable by clients")
	}
	return addr.String(), nil
}

func portFromListenAddr(envKey, listenAddr string) (int, error) {
	_, portText, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return 0, fmt.Errorf("%s must be a host:port listen address: %w", envKey, err)
	}
	return parseTCPPort(envKey, portText)
}

func portFromOptionalEnv(env envSource, key string, def int) (int, error) {
	raw := strings.TrimSpace(env.envAllowEmptyOr(key, ""))
	if raw == "" {
		return def, nil
	}
	return parseTCPPort(key, raw)
}

func parseTCPPort(envKey, raw string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be a base-10 TCP port: %w", envKey, err)
	}
	if port <= 0 || port > 65535 {
		return 0, fmt.Errorf("%s must be between 1 and 65535", envKey)
	}
	return port, nil
}

// validateGeoIPConfig validates the session geo-attribution backend chain. When no endpoint is configured
// everything is allowed -- the session list keeps returning the "Unknown" placeholder text, which is the
// expected behavior when disabled and should not keep the service from starting.
func validateGeoIPConfig(cfg Config) error {
	configured := 0
	for index, endpoint := range cfg.GeoIPEndpoints {
		if strings.TrimSpace(endpoint) == "" {
			continue
		}
		configured++
		if !strings.Contains(endpoint, "{ip}") {
			return fmt.Errorf("TELESRV_GEOIP_ENDPOINTS entry %d must contain the {ip} placeholder", index+1)
		}
		parsed, err := url.Parse(strings.TrimSpace(endpoint))
		if err != nil {
			return fmt.Errorf("TELESRV_GEOIP_ENDPOINTS entry %d is not a valid URL", index+1)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("TELESRV_GEOIP_ENDPOINTS entry %d must use http or https", index+1)
		}
		if parsed.Host == "" {
			return fmt.Errorf("TELESRV_GEOIP_ENDPOINTS entry %d must include a host", index+1)
		}
	}
	// An all-blank list is equivalent to disabled and should not drag in all the numeric checks below.
	if configured == 0 {
		return nil
	}
	// Upstreams respond normally in 0.6~1.3s. Allowing 10s or more indicates a mis-set value (e.g. mistakenly
	// parsed as a duration string); in that case every resolution batch burns the whole budget before falling
	// back to Unknown.
	if cfg.GeoIPTimeout <= 0 || cfg.GeoIPTimeout > 10*time.Second {
		return fmt.Errorf("TELESRV_GEOIP_TIMEOUT must be greater than zero and at most 10s")
	}
	if cfg.GeoIPConcurrency <= 0 || cfg.GeoIPConcurrency > 32 {
		return fmt.Errorf("TELESRV_GEOIP_CONCURRENCY must be 1..32")
	}
	// The negative cache must be markedly shorter than the positive cache: after a negative entry expires only
	// the failed resolution should be retried, whereas matching the positive cache equals freezing that IP until
	// the next day on a single rate limit.
	if cfg.GeoIPNegativeTTL <= 0 || cfg.GeoIPCacheTTL <= 0 || cfg.GeoIPNegativeTTL >= cfg.GeoIPCacheTTL {
		return fmt.Errorf("TELESRV_GEOIP_NEGATIVE_TTL must be greater than zero and shorter than TELESRV_GEOIP_CACHE_TTL")
	}
	if cfg.GeoIPCacheSize <= 0 || cfg.GeoIPCacheSize > 1_000_000 {
		return fmt.Errorf("TELESRV_GEOIP_CACHE_SIZE must be 1..1000000")
	}
	if cfg.GeoIPRateLimitThreshold <= 0 || cfg.GeoIPRateLimitThreshold > 100 {
		return fmt.Errorf("TELESRV_GEOIP_RATE_LIMIT_THRESHOLD must be 1..100")
	}
	if cfg.GeoIPRateLimitCooldown <= 0 || cfg.GeoIPRateLimitCooldown > time.Hour {
		return fmt.Errorf("TELESRV_GEOIP_RATE_LIMIT_COOLDOWN must be greater than zero and at most 1h")
	}
	// The down/unreachable window must be shorter than the rate-limit window: a backend merely flickering should
	// not stay offline longer than the quota window.
	if cfg.GeoIPDownThreshold <= 0 || cfg.GeoIPDownThreshold > 100 {
		return fmt.Errorf("TELESRV_GEOIP_DOWN_THRESHOLD must be 1..100")
	}
	if cfg.GeoIPDownCooldown <= 0 || cfg.GeoIPDownCooldown > cfg.GeoIPRateLimitCooldown {
		return fmt.Errorf("TELESRV_GEOIP_DOWN_COOLDOWN must be greater than zero and at most TELESRV_GEOIP_RATE_LIMIT_COOLDOWN")
	}
	return nil
}

func validateTelegramLoginConfig(cfg Config) error {
	if !cfg.TelegramLoginEnabled {
		return nil
	}
	if strings.TrimSpace(cfg.PublicLinkWebAddr) == "" {
		return fmt.Errorf("TELESRV_TELEGRAM_LOGIN_ENABLE requires TELESRV_PUBLIC_LINK_WEB_ADDR")
	}
	issuer, err := url.Parse(strings.TrimSpace(cfg.TelegramLoginIssuer))
	if err != nil || issuer.User != nil || issuer.Host == "" || issuer.RawQuery != "" || issuer.Fragment != "" ||
		(issuer.Path != "" && issuer.Path != "/") {
		return fmt.Errorf("TELESRV_TELEGRAM_LOGIN_ISSUER must be an absolute origin URL")
	}
	switch issuer.Scheme {
	case "https":
	case "http":
		if !cfg.TelegramLoginAllowHTTP {
			return fmt.Errorf("TELESRV_TELEGRAM_LOGIN_ISSUER http requires TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP=true")
		}
	default:
		return fmt.Errorf("TELESRV_TELEGRAM_LOGIN_ISSUER must use https")
	}
	if strings.TrimSpace(cfg.TelegramLoginSigningKeysFile) == "" || strings.TrimSpace(cfg.TelegramLoginCodeKeysFile) == "" || strings.TrimSpace(cfg.TelegramLoginSecretPepperFile) == "" {
		return fmt.Errorf("TELESRV_TELEGRAM_LOGIN_* key and pepper files are required")
	}
	if cfg.TelegramLoginRequestTTL < time.Minute || cfg.TelegramLoginRequestTTL > 15*time.Minute ||
		cfg.TelegramLoginCodeTTL < 30*time.Second || cfg.TelegramLoginCodeTTL > 10*time.Minute ||
		cfg.TelegramLoginIDTokenTTL < time.Minute || cfg.TelegramLoginIDTokenTTL > 24*time.Hour {
		return fmt.Errorf("TELESRV_TELEGRAM_LOGIN TTL values are outside their bounded ranges")
	}
	if cfg.TelegramLoginRetention < time.Hour || cfg.TelegramLoginRetention > 90*24*time.Hour ||
		cfg.TelegramLoginSweepInterval < 10*time.Second || cfg.TelegramLoginSweepInterval > time.Hour ||
		cfg.TelegramLoginSweepBatch <= 0 || cfg.TelegramLoginSweepBatch > 1000 {
		return fmt.Errorf("TELESRV_TELEGRAM_LOGIN retention must be 1h..90d, sweep interval 10s..1h, and sweep batch 1..1000")
	}
	for _, raw := range cfg.TelegramLoginTrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(strings.TrimSpace(raw)); err != nil {
			return fmt.Errorf("TELESRV_TELEGRAM_LOGIN_TRUSTED_PROXY_CIDRS contains invalid CIDR %q: %w", raw, err)
		}
	}
	return nil
}

func validateStarGiftConfig(cfg Config) error {
	switch cfg.StarGiftExportMode {
	case "", StarGiftExportModeLocal, StarGiftExportModeTON, StarGiftExportModeDisabled:
	default:
		return fmt.Errorf("TELESRV_STARGIFT_EXPORT_MODE must be local, ton or disabled")
	}
	if cfg.StarGiftExportMode == StarGiftExportModeTON {
		if cfg.StarGiftTONNetwork != TONNetworkMainnet && cfg.StarGiftTONNetwork != TONNetworkTestnet {
			return fmt.Errorf("TELESRV_STARGIFT_TON_NETWORK must be mainnet or testnet")
		}
		if cfg.StarGiftTONCollectionAddress == "" || cfg.StarGiftTONCollectionCodeHash == "" ||
			cfg.StarGiftTONMintABI != "basic-collection-v1" || cfg.StarGiftTONInitialItemIndex == "" ||
			cfg.StarGiftTONProofDomain == "" || cfg.StarGiftTONCapabilitySecretFile == "" {
			return fmt.Errorf("TON Star Gift export collection, code hash, basic-collection-v1 mint ABI, initial item index, proof domain and capability secret file are required")
		}
		if !validDecimalUint64(cfg.StarGiftTONInitialItemIndex) {
			return fmt.Errorf("TELESRV_STARGIFT_TON_INITIAL_ITEM_INDEX must be a uint64 decimal")
		}
		if cfg.StarGiftTONExportTTL < 5*time.Minute || cfg.StarGiftTONExportTTL > 24*time.Hour ||
			cfg.StarGiftTONChallengeTTL < time.Minute || cfg.StarGiftTONChallengeTTL > 10*time.Minute {
			return fmt.Errorf("TON Star Gift export/challenge TTL is outside the admitted range")
		}
		if cfg.StarGiftTONFinalizerBatch < 1 || cfg.StarGiftTONFinalizerBatch > 100 ||
			cfg.StarGiftTONFinalizerPollInterval <= 0 || cfg.StarGiftTONFinalizerLeaseTimeout < 10*time.Second ||
			cfg.StarGiftTONFinalizerLeaseTimeout > time.Hour || cfg.StarGiftTONFinalizerRequestTimeout < time.Second ||
			cfg.StarGiftTONFinalizerRequestTimeout >= cfg.StarGiftTONFinalizerLeaseTimeout || cfg.StarGiftTONFinalizerRetryDelay <= 0 {
			return fmt.Errorf("TON Star Gift finalizer settings are outside the admitted range")
		}
		if cfg.StarGiftTONClaimEnabled && cfg.StarGiftTONClaimBotTokenFile == "" {
			return fmt.Errorf("TON Star Gift claim bot token file is required when claim is enabled")
		}
		if cfg.StarGiftTONClaimInitDataTTL < time.Minute || cfg.StarGiftTONClaimInitDataTTL > 24*time.Hour {
			return fmt.Errorf("TON Star Gift claim init-data TTL must be 1m..24h")
		}
	}
	if cfg.StarGiftSweepInterval <= 0 || cfg.StarGiftSweepBatch <= 0 || cfg.StarGiftSweepBatch > 10000 {
		return fmt.Errorf("TELESRV_STARGIFT_SWEEP_INTERVAL must be positive and TELESRV_STARGIFT_SWEEP_BATCH must be 1..10000")
	}
	if cfg.StarGiftTONStartingGrant < 0 {
		return fmt.Errorf("TELESRV_STARGIFT_TON_STARTING_GRANT must be non-negative")
	}
	if cfg.StarGiftTransferStars < 0 || cfg.StarGiftDropOriginalDetailsStars < 0 || cfg.StarGiftOfferMinStars < 0 {
		return fmt.Errorf("TELESRV_STARGIFT_TRANSFER_STARS, TELESRV_STARGIFT_DROP_DETAILS_STARS and TELESRV_STARGIFT_OFFER_MIN_STARS must be non-negative")
	}
	if cfg.StarGiftResaleMaxStars <= 0 || cfg.StarGiftResaleMaxTON <= 0 || cfg.StarGiftTONStarsRate <= 0 {
		return fmt.Errorf("TELESRV_STARGIFT_RESALE_MAX_STARS, TELESRV_STARGIFT_RESALE_MAX_TON and TELESRV_STARGIFT_TON_STARS_RATE must be positive")
	}
	if cfg.StarGiftResaleMinStars <= 0 {
		return fmt.Errorf("TELESRV_STARGIFT_RESALE_MIN_STARS must be positive")
	}
	if cfg.StarGiftExportDelay < 0 || cfg.StarGiftTransferDelay < 0 || cfg.StarGiftResellDelay < 0 || cfg.StarGiftCraftDelay < 0 {
		return fmt.Errorf("TELESRV_STARGIFT lifecycle delays must be non-negative")
	}
	const maxProtocolDelay = time.Duration(1<<31-1) * time.Second
	if cfg.StarGiftExportDelay > maxProtocolDelay || cfg.StarGiftTransferDelay > maxProtocolDelay ||
		cfg.StarGiftResellDelay > maxProtocolDelay || cfg.StarGiftCraftDelay > maxProtocolDelay {
		return fmt.Errorf("TELESRV_STARGIFT lifecycle delays exceed the protocol int32 date range")
	}
	if cfg.StarGiftCraftChancePermille < 0 || cfg.StarGiftCraftChancePermille > 1000 {
		return fmt.Errorf("TELESRV_STARGIFT_CRAFT_CHANCE_PERMILLE must be 0..1000")
	}
	if cfg.StarGiftStarsProceedsPermille < 0 || cfg.StarGiftStarsProceedsPermille > 1000 ||
		cfg.StarGiftTONProceedsPermille < 0 || cfg.StarGiftTONProceedsPermille > 1000 {
		return fmt.Errorf("TELESRV_STARGIFT_*_PROCEEDS_PERMILLE must be 0..1000")
	}
	return nil
}

// AccountRatingWeights renders the configured composite rating formula. It is
// the single conversion point between env keys and the domain formula, so the
// app service and the admin explanation always use the same numbers.
func (c Config) AccountRatingWeights() domain.AccountRatingWeights {
	return domain.AccountRatingWeights{
		StarsReceivedPermille: c.RatingWeightStarsReceivedPermille,
		StarsSpentPermille:    c.RatingWeightStarsSpentPermille,
		PerMessageSent:        c.RatingWeightMessageSent,
		PerAccountAgeDay:      c.RatingWeightAccountAgeDay,
		PerGiftReceived:       c.RatingWeightGiftReceived,
		PerModerationCase:     c.RatingWeightModerationCase,
		ScamPenalty:           c.RatingWeightScamPenalty,
		FakePenalty:           c.RatingWeightFakePenalty,
		ActivityCap:           c.RatingActivityCap,
	}
}

// validateAccountRatingConfig rejects a formula or worker cadence that cannot
// produce a reproducible rating. Weights are validated even when the feature is
// disabled: enabling it later must not be the moment a typo is discovered.
func validateAccountRatingConfig(cfg Config) error {
	if err := cfg.AccountRatingWeights().Validate(); err != nil {
		return fmt.Errorf("TELESRV_RATING_WEIGHT_* and TELESRV_RATING_ACTIVITY_CAP must be non-negative: %w", err)
	}
	if cfg.RatingPendingDelay < 0 {
		return fmt.Errorf("TELESRV_RATING_PENDING_DELAY must be non-negative")
	}
	const maxRatingPendingDelay = 30 * 24 * time.Hour
	if cfg.RatingPendingDelay > maxRatingPendingDelay {
		return fmt.Errorf("TELESRV_RATING_PENDING_DELAY must not exceed 720h")
	}
	if cfg.RatingRecomputeInterval <= 0 {
		return fmt.Errorf("TELESRV_RATING_RECOMPUTE_INTERVAL must be positive")
	}
	if cfg.RatingStaleAfter <= 0 {
		return fmt.Errorf("TELESRV_RATING_STALE_AFTER must be positive")
	}
	if cfg.RatingRecomputeBatch <= 0 || cfg.RatingRecomputeBatch > 10000 {
		return fmt.Errorf("TELESRV_RATING_RECOMPUTE_BATCH must be 1..10000")
	}
	return nil
}

// adminPermissionAll is the wildcard permission: a session or token carrying it
// may perform every admin action.
const adminPermissionAll = "*"

// validateVerificationConfig rejects a verification policy that cannot be
// enforced, for both mechanisms: the operator-granted platform badge and the
// third-party bot verification marks. It runs even when either feature is
// disabled, so enabling it later is not the moment a typo is discovered.
func validateVerificationConfig(cfg Config) error {
	if cfg.VerificationRejectCooldown < 0 {
		return fmt.Errorf("TELESRV_VERIFICATION_REJECT_COOLDOWN must be non-negative")
	}
	const maxVerificationRejectCooldown = 365 * 24 * time.Hour
	if cfg.VerificationRejectCooldown > maxVerificationRejectCooldown {
		return fmt.Errorf("TELESRV_VERIFICATION_REJECT_COOLDOWN must not exceed 8760h")
	}
	if cfg.VerificationApplyRateLimit < 0 || cfg.VerificationBotRateLimit < 0 {
		return fmt.Errorf("TELESRV_VERIFICATION_APPLY_RATE_LIMIT and TELESRV_VERIFICATION_BOT_RATE_LIMIT must be non-negative")
	}
	if cfg.VerificationApplyRateWindow < 0 || cfg.VerificationBotRateWindow < 0 {
		return fmt.Errorf("TELESRV_VERIFICATION_APPLY_RATE_WINDOW and TELESRV_VERIFICATION_BOT_RATE_WINDOW must be non-negative")
	}
	// A positive limit with a zero window is not "unlimited", it is a limiter that
	// can never refill: reject it instead of shipping a permanent lockout.
	if cfg.VerificationApplyRateLimit > 0 && cfg.VerificationApplyRateWindow <= 0 {
		return fmt.Errorf("TELESRV_VERIFICATION_APPLY_RATE_WINDOW must be positive when TELESRV_VERIFICATION_APPLY_RATE_LIMIT is set")
	}
	if cfg.VerificationBotRateLimit > 0 && cfg.VerificationBotRateWindow <= 0 {
		return fmt.Errorf("TELESRV_VERIFICATION_BOT_RATE_WINDOW must be positive when TELESRV_VERIFICATION_BOT_RATE_LIMIT is set")
	}
	if cfg.VerificationNotifyInterval <= 0 {
		return fmt.Errorf("TELESRV_VERIFICATION_NOTIFY_INTERVAL must be positive")
	}
	if cfg.VerificationNotifyBatch <= 0 || cfg.VerificationNotifyBatch > 500 {
		return fmt.Errorf("TELESRV_VERIFICATION_NOTIFY_BATCH must be 1..500")
	}
	if cfg.BroadcastWorkerInterval <= 0 {
		return fmt.Errorf("TELESRV_BROADCAST_WORKER_INTERVAL must be positive")
	}
	if cfg.BroadcastWorkerLease <= 0 || cfg.BroadcastWorkerLease > time.Hour {
		return fmt.Errorf("TELESRV_BROADCAST_WORKER_LEASE must be positive and at most 1h")
	}
	if cfg.BroadcastMaterializeBatch <= 0 || cfg.BroadcastMaterializeBatch > 1000 {
		return fmt.Errorf("TELESRV_BROADCAST_MATERIALIZE_BATCH must be 1..1000")
	}
	if cfg.BroadcastDeliveryBatch <= 0 || cfg.BroadcastDeliveryBatch > 500 {
		return fmt.Errorf("TELESRV_BROADCAST_DELIVERY_BATCH must be 1..500")
	}
	if cfg.VerificationMaxActivePerUser < 0 || cfg.VerificationMaxActivePerUser > 50 {
		return fmt.Errorf("TELESRV_VERIFICATION_MAX_ACTIVE_PER_USER must be 0..50")
	}
	// Third-party bot verification. The ceiling is the storage bound: a value above
	// it would be silently unreachable, and a configuration key that cannot do what
	// it says is worse than no key.
	if cfg.BotVerificationMaxPerVerifier < 0 {
		return fmt.Errorf("TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER must be non-negative")
	}
	if cfg.BotVerificationMaxPerVerifier > domain.MaxCustomVerificationsPerVerifier {
		return fmt.Errorf("TELESRV_BOT_VERIFICATION_MAX_PER_VERIFIER must not exceed %d", domain.MaxCustomVerificationsPerVerifier)
	}
	if cfg.BotVerificationRequestRateLimit < 0 {
		return fmt.Errorf("TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT must be non-negative")
	}
	if cfg.BotVerificationRequestRateWindow < 0 {
		return fmt.Errorf("TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW must be non-negative")
	}
	// Same trap as the official budget above: a positive limit with a zero window is
	// not "unlimited", it is a limiter that can never refill.
	if cfg.BotVerificationRequestRateLimit > 0 && cfg.BotVerificationRequestRateWindow <= 0 {
		return fmt.Errorf("TELESRV_BOT_VERIFICATION_REQUEST_RATE_WINDOW must be positive when TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT is set")
	}
	return nil
}

// validateAdminRBACConfig checks the panel/adminapi permission configuration.
// An unparsable permission name is refused rather than ignored: a silently
// dropped permission is either a lockout or an unintended grant.
func validateAdminRBACConfig(cfg Config) error {
	if len(cfg.AdminUIPermissions) == 0 {
		return fmt.Errorf("TELESRV_ADMIN_UI_PERMISSIONS must not be empty; use * to grant every permission")
	}
	for _, permission := range cfg.AdminUIPermissions {
		if !validAdminPermission(permission) {
			return fmt.Errorf("TELESRV_ADMIN_UI_PERMISSIONS contains invalid permission %q", permission)
		}
	}
	names := make(map[string]struct{}, len(cfg.AdminScopedTokens))
	tokens := make(map[string]struct{}, len(cfg.AdminScopedTokens))
	for _, scoped := range cfg.AdminScopedTokens {
		if _, dup := names[strings.ToLower(scoped.Name)]; dup {
			return fmt.Errorf("TELESRV_ADMIN_SCOPED_TOKENS has duplicate name %q", scoped.Name)
		}
		names[strings.ToLower(scoped.Name)] = struct{}{}
		if _, dup := tokens[scoped.Token]; dup {
			return fmt.Errorf("TELESRV_ADMIN_SCOPED_TOKENS reuses one token for several names")
		}
		tokens[scoped.Token] = struct{}{}
		if scoped.Token == cfg.AdminAPIToken && strings.TrimSpace(cfg.AdminAPIToken) != "" {
			return fmt.Errorf("TELESRV_ADMIN_SCOPED_TOKENS entry %q reuses TELESRV_ADMIN_API_TOKEN, which would silently widen it to every permission", scoped.Name)
		}
		if len(scoped.Permissions) == 0 {
			return fmt.Errorf("TELESRV_ADMIN_SCOPED_TOKENS entry %q has no permissions", scoped.Name)
		}
		for _, permission := range scoped.Permissions {
			if !validAdminPermission(permission) {
				return fmt.Errorf("TELESRV_ADMIN_SCOPED_TOKENS entry %q has invalid permission %q", scoped.Name, permission)
			}
		}
	}
	return nil
}

// parseAdminScopedTokens reads "name:token:perm1,perm2" entries separated by ';'.
//
// The shape is strict on purpose: the value carries credentials, and a
// half-understood entry must fail startup rather than produce a token whose
// rights nobody can predict. The permission list is the last field, so a token
// itself may not contain ':' -- which is also why it is validated here rather
// than being re-split later by a consumer.
func parseAdminScopedTokens(raw string) ([]AdminScopedToken, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	out := make([]AdminScopedToken, 0, 4)
	for _, entry := range strings.Split(raw, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 3 {
			return nil, fmt.Errorf("TELESRV_ADMIN_SCOPED_TOKENS entry %q must be name:token:perm1,perm2", entry)
		}
		name := strings.TrimSpace(parts[0])
		token := strings.TrimSpace(parts[1])
		if name == "" || token == "" {
			return nil, fmt.Errorf("TELESRV_ADMIN_SCOPED_TOKENS entry %q must carry a non-empty name and token", entry)
		}
		if strings.ContainsAny(token, " \t\r\n") {
			return nil, fmt.Errorf("TELESRV_ADMIN_SCOPED_TOKENS entry %q has whitespace inside its token", name)
		}
		permissions := make([]string, 0, 4)
		for _, permission := range strings.Split(parts[2], ",") {
			permission = strings.TrimSpace(permission)
			if permission == "" {
				continue
			}
			permissions = append(permissions, permission)
		}
		if len(permissions) == 0 {
			return nil, fmt.Errorf("TELESRV_ADMIN_SCOPED_TOKENS entry %q must list at least one permission", name)
		}
		out = append(out, AdminScopedToken{Name: name, Token: token, Permissions: permissions})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// validAdminPermission accepts the wildcard and dotted/namespaced permission
// names such as "users.read" or "verification:decide".
func validAdminPermission(permission string) bool {
	if permission == adminPermissionAll {
		return true
	}
	if permission == "" || len(permission) > 64 {
		return false
	}
	for i := 0; i < len(permission); i++ {
		c := permission[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
		case (c == '.' || c == ':') && i != 0 && i != len(permission)-1:
		case c == '*' && i == len(permission)-1 && i > 0 && (permission[i-1] == '.' || permission[i-1] == ':'):
			// A trailing "namespace.*" grants a whole namespace.
		default:
			return false
		}
	}
	return true
}

// validateCollectibleUsernameConfig checks the optional mint URL template. An
// empty template is the documented default (the public-link route is derived
// from TELESRV_PUBLIC_BASE_URL); a configured one must be a client-openable
// absolute http(s) URL that still fits the registry's url column.
func validateCollectibleUsernameConfig(cfg Config) error {
	template := strings.TrimSpace(cfg.CollectibleUsernameURLTemplate)
	if template == "" {
		return nil
	}
	if len(template)+domain.MaxCollectibleUsernameLength > domain.MaxCollectibleUsernameURLLength {
		return fmt.Errorf("TELESRV_COLLECTIBLE_USERNAME_URL_TEMPLATE is too long to hold a rendered username")
	}
	// Render the placeholder before parsing so a templated path segment is
	// validated in its final shape.
	rendered := strings.ReplaceAll(template, "{username}", "username")
	parsed, err := url.Parse(rendered)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("TELESRV_COLLECTIBLE_USERNAME_URL_TEMPLATE must be an absolute http(s) URL without userinfo")
	}
	return nil
}

func validateRPCExecutionConfig(cfg Config) error {
	if cfg.MTProtoRPCDeliveryHookWorkers <= 0 || cfg.MTProtoRPCDeliveryHookMaxPending < cfg.MTProtoRPCDeliveryHookWorkers {
		return fmt.Errorf("MTProto rpc delivery hook capacity must satisfy pending >= workers > 0: %d/%d", cfg.MTProtoRPCDeliveryHookMaxPending, cfg.MTProtoRPCDeliveryHookWorkers)
	}
	if cfg.MTProtoRPCExecutionMaxEntries <= 0 || cfg.MTProtoRPCExecutionAuthMaxEntries <= 0 ||
		cfg.MTProtoRPCExecutionSessionMaxEntries <= 0 {
		return fmt.Errorf("MTProto rpc execution entry limits must be positive")
	}
	if cfg.MTProtoRPCExecutionMaxEntries < cfg.MTProtoRPCExecutionAuthMaxEntries ||
		cfg.MTProtoRPCExecutionAuthMaxEntries < cfg.MTProtoRPCExecutionSessionMaxEntries {
		return fmt.Errorf("MTProto rpc execution entry hierarchy must satisfy global >= auth >= session: %d/%d/%d",
			cfg.MTProtoRPCExecutionMaxEntries, cfg.MTProtoRPCExecutionAuthMaxEntries, cfg.MTProtoRPCExecutionSessionMaxEntries)
	}
	if cfg.MTProtoRPCGlobalMaxTasks <= 0 || cfg.MTProtoRPCExecutionPendingPerAuth <= 0 ||
		cfg.MTProtoRPCExecutionPendingPerAuth > cfg.MTProtoRPCGlobalMaxTasks ||
		cfg.MTProtoRPCExecutionPendingPerAuth > cfg.MTProtoRPCExecutionAuthMaxEntries {
		return fmt.Errorf("MTProto rpc execution pending-per-auth %d must be positive and <= global pending %d and auth entries %d",
			cfg.MTProtoRPCExecutionPendingPerAuth, cfg.MTProtoRPCGlobalMaxTasks, cfg.MTProtoRPCExecutionAuthMaxEntries)
	}
	return nil
}

func validatePerformanceConfig(cfg Config) error {
	positive := []struct {
		name  string
		value int
	}{
		{"TELESRV_READ_MODEL_VERSION_CACHE_MAX", cfg.ReadModelVersionCacheMaxEntries},
		{"TELESRV_READ_MODEL_VERSION_BATCH_MAX_KEYS", cfg.ReadModelVersionBatchMaxKeys},
		{"TELESRV_READ_MODEL_VERSION_BATCH_QUEUE", cfg.ReadModelVersionBatchQueue},
		{"TELESRV_AUTH_KEY_GET_BATCH_MAX", cfg.AuthKeyGetBatchMax},
		{"TELESRV_AUTH_KEY_GET_BATCH_QUEUE", cfg.AuthKeyGetBatchQueue},
		{"TELESRV_CONTACT_REVERSE_BATCH_MAX_PAIRS", cfg.ContactReverseBatchMaxPairs},
		{"TELESRV_CONTACT_REVERSE_BATCH_QUEUE", cfg.ContactReverseBatchQueue},
		{"TELESRV_CONTACT_SNAPSHOT_CACHE_MAX_VIEWERS", cfg.ContactSnapshotCacheMaxViewers},
		{"TELESRV_PROFILE_PHOTO_CACHE_MAX", cfg.ProfilePhotoCacheMaxEntries},
		{"TELESRV_ACTIVE_CHANNEL_IDS_CACHE_MAX", cfg.ActiveChannelIDsCacheMaxEntries},
		{"TELESRV_ACTIVE_CHANNEL_IDS_BATCH_MAX", cfg.ActiveChannelIDsBatchMax},
		{"TELESRV_ACTIVE_CHANNEL_IDS_BATCH_QUEUE", cfg.ActiveChannelIDsBatchQueue},
		{"TELESRV_BOOTSTRAP_READY_BATCH_MAX", cfg.BootstrapReadyBatchMax},
		{"TELESRV_BOOTSTRAP_READY_BATCH_QUEUE", cfg.BootstrapReadyBatchQueue},
		{"TELESRV_PRESENCE_LAST_SEEN_BATCH_MAX", cfg.PresenceLastSeenBatchMax},
		{"TELESRV_PRESENCE_LAST_SEEN_BATCH_QUEUE", cfg.PresenceLastSeenBatchQueue},
	}
	for _, item := range positive {
		if item.value <= 0 {
			return fmt.Errorf("%s must be positive", item.name)
		}
	}
	durations := []struct {
		name  string
		value time.Duration
	}{
		{"TELESRV_READ_MODEL_VERSION_BATCH_WAIT", cfg.ReadModelVersionBatchWait},
		{"TELESRV_READ_MODEL_VERSION_BATCH_TIMEOUT", cfg.ReadModelVersionBatchTimeout},
		{"TELESRV_AUTH_KEY_GET_BATCH_WAIT", cfg.AuthKeyGetBatchWait},
		{"TELESRV_AUTH_KEY_GET_BATCH_TIMEOUT", cfg.AuthKeyGetBatchTimeout},
		{"TELESRV_CONTACT_REVERSE_BATCH_WAIT", cfg.ContactReverseBatchWait},
		{"TELESRV_CONTACT_REVERSE_BATCH_TIMEOUT", cfg.ContactReverseBatchTimeout},
		{"TELESRV_PROFILE_PHOTO_CACHE_TTL", cfg.ProfilePhotoCacheTTL},
		{"TELESRV_DIALOG_LIST_SNAPSHOT_REDIS_TTL", cfg.DialogListSnapshotRedisTTL},
		{"TELESRV_ACTIVE_CHANNEL_IDS_CACHE_TTL", cfg.ActiveChannelIDsCacheTTL},
		{"TELESRV_ACTIVE_CHANNEL_IDS_REDIS_TTL", cfg.ActiveChannelIDsRedisTTL},
		{"TELESRV_ACTIVE_CHANNEL_IDS_BATCH_WAIT", cfg.ActiveChannelIDsBatchWait},
		{"TELESRV_ACTIVE_CHANNEL_IDS_BATCH_TIMEOUT", cfg.ActiveChannelIDsBatchTimeout},
		{"TELESRV_BOOTSTRAP_READY_BATCH_WAIT", cfg.BootstrapReadyBatchWait},
		{"TELESRV_BOOTSTRAP_READY_BATCH_TIMEOUT", cfg.BootstrapReadyBatchTimeout},
		{"TELESRV_PRESENCE_LAST_SEEN_BATCH_WAIT", cfg.PresenceLastSeenBatchWait},
		{"TELESRV_PRESENCE_LAST_SEEN_BATCH_TIMEOUT", cfg.PresenceLastSeenBatchTimeout},
		{"TELESRV_PRESENCE_LAST_SEEN_DRAIN_TIMEOUT", cfg.PresenceLastSeenDrainTimeout},
	}
	for _, item := range durations {
		if item.value <= 0 {
			return fmt.Errorf("%s must be positive", item.name)
		}
	}
	if cfg.AuthKeyGetBatchQueue < cfg.AuthKeyGetBatchMax || cfg.ActiveChannelIDsBatchQueue < cfg.ActiveChannelIDsBatchMax || cfg.BootstrapReadyBatchQueue < cfg.BootstrapReadyBatchMax || cfg.PresenceLastSeenBatchQueue < cfg.PresenceLastSeenBatchMax {
		return fmt.Errorf("performance batch queues must be at least their batch size")
	}
	if cfg.ChannelDifferenceCacheMaxEntries > 0 && (cfg.ChannelDifferenceCacheMaxBytes <= 0 || cfg.ChannelDifferenceCacheTTL <= 0) {
		return fmt.Errorf("channel difference cache bytes and ttl must be positive when enabled")
	}
	return nil
}

func validateASRConfig(cfg Config) error {
	if cfg.ASRURL == "" {
		return nil
	}
	if cfg.ASRTimeout <= 0 {
		return fmt.Errorf("TELESRV_ASR_TIMEOUT must be positive")
	}
	u, err := url.Parse(cfg.ASRURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("TELESRV_ASR_URL must be an absolute http(s) URL without userinfo")
	}
	return nil
}

func validateLoginEmailConfig(cfg Config) error {
	if cfg.LoginEmailRequireSetup && !cfg.LoginEmailEnable {
		return fmt.Errorf("TELESRV_LOGIN_EMAIL_REQUIRE_SETUP requires TELESRV_LOGIN_EMAIL_ENABLE=true")
	}
	if cfg.AuthCodeTTL <= 0 {
		return fmt.Errorf("TELESRV_AUTH_CODE_TTL must be positive")
	}
	if cfg.AuthCodeMaxAttempts <= 0 {
		return fmt.Errorf("TELESRV_AUTH_CODE_MAX_ATTEMPTS must be positive")
	}
	if cfg.PhoneCodeLength < 4 || cfg.PhoneCodeLength > 10 {
		return fmt.Errorf("TELESRV_PHONE_CODE_LENGTH must be between 4 and 10")
	}
	if cfg.LoginEmailCodeLength < 4 || cfg.LoginEmailCodeLength > 10 {
		return fmt.Errorf("TELESRV_LOGIN_EMAIL_CODE_LENGTH must be between 4 and 10")
	}
	switch cfg.SMTPTLSMode {
	case "", "starttls", "tls", "none":
	default:
		return fmt.Errorf("TELESRV_SMTP_TLS must be starttls, tls, or none")
	}
	switch cfg.PhoneCodeDeliveryProvider {
	case "development", "webhook":
	default:
		return fmt.Errorf("TELESRV_PHONE_CODE_DELIVERY_PROVIDER must be development or webhook")
	}
	switch cfg.EmailCodeDeliveryProvider {
	case "smtp", "webhook":
	default:
		return fmt.Errorf("TELESRV_EMAIL_CODE_DELIVERY_PROVIDER must be smtp or webhook")
	}
	webhookEnabled := cfg.PhoneCodeDeliveryProvider == "webhook" ||
		(cfg.LoginEmailEnable && cfg.EmailCodeDeliveryProvider == "webhook")
	if webhookEnabled {
		if cfg.OTPWebhookTimeout <= 0 {
			return fmt.Errorf("TELESRV_OTP_WEBHOOK_TIMEOUT must be positive")
		}
		u, err := url.Parse(strings.TrimSpace(cfg.OTPWebhookURL))
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("TELESRV_OTP_WEBHOOK_URL must be an absolute http(s) URL without userinfo")
		}
	}
	if !cfg.LoginEmailEnable || cfg.EmailCodeDeliveryProvider == "webhook" {
		return nil
	}
	if strings.TrimSpace(cfg.SMTPHost) == "" {
		return fmt.Errorf("TELESRV_SMTP_HOST is required when TELESRV_LOGIN_EMAIL_ENABLE=true")
	}
	if cfg.SMTPPort <= 0 || cfg.SMTPPort > 65535 {
		return fmt.Errorf("TELESRV_SMTP_PORT must be between 1 and 65535")
	}
	if strings.TrimSpace(cfg.SMTPFrom) == "" && strings.TrimSpace(cfg.SMTPUsername) == "" {
		return fmt.Errorf("TELESRV_SMTP_FROM or TELESRV_SMTP_USERNAME is required when TELESRV_LOGIN_EMAIL_ENABLE=true")
	}
	if cfg.SMTPTimeout <= 0 {
		return fmt.Errorf("TELESRV_SMTP_TIMEOUT must be positive")
	}
	return nil
}

func loadAIProviders(env envSource) []AIProviderConfig {
	names := env.envListOr("TELESRV_AI_PROVIDERS", []string{"local"})
	out := make([]AIProviderConfig, 0, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		suffix := providerEnvSuffix(name)
		kind := env.envOr("TELESRV_AI_"+suffix+"_KIND", defaultAIProviderKind(name))
		out = append(out, AIProviderConfig{
			Name:            name,
			Kind:            strings.ToLower(strings.TrimSpace(kind)),
			BaseURL:         env.envOr("TELESRV_AI_"+suffix+"_BASE_URL", ""),
			APIKey:          env.envOr("TELESRV_AI_"+suffix+"_API_KEY", defaultAIProviderAPIKey(env, name)),
			Model:           env.envOr("TELESRV_AI_"+suffix+"_MODEL", ""),
			MaxOutputTokens: env.envIntOr("TELESRV_AI_"+suffix+"_MAX_OUTPUT_TOKENS", 1024),
			Temperature:     env.envFloatOr("TELESRV_AI_"+suffix+"_TEMPERATURE", 0.2),
			OmitTemperature: env.envBoolOr("TELESRV_AI_"+suffix+"_OMIT_TEMPERATURE", false),
			Thinking:        strings.ToLower(strings.TrimSpace(env.envOr("TELESRV_AI_"+suffix+"_THINKING", ""))),
		})
	}
	if len(out) == 0 {
		out = append(out, AIProviderConfig{Name: "local", Kind: "local", MaxOutputTokens: 1024, Temperature: 0.2})
	}
	return out
}

func providerEnvSuffix(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func defaultAIProviderKind(name string) string {
	switch name {
	case "openai":
		return "openai_responses"
	case "openai_chat", "openai-compatible", "openai_compat":
		return "openai_chat"
	case "gemini":
		return "gemini"
	case "anthropic":
		return "anthropic"
	default:
		return name
	}
}

func defaultAIProviderAPIKey(env envSource, name string) string {
	switch name {
	case "openai", "openai_chat", "openai-compatible", "openai_compat":
		return env.envOr("OPENAI_API_KEY", "")
	case "gemini":
		return env.envOr("GEMINI_API_KEY", "")
	case "anthropic":
		return env.envOr("ANTHROPIC_API_KEY", "")
	default:
		return ""
	}
}

type envSource struct {
	values     map[string]string
	processEnv bool
}

func newEnvSource(values map[string]string, processEnv bool) envSource {
	if values == nil {
		values = map[string]string{}
	}
	return envSource{values: values, processEnv: processEnv}
}

func (e envSource) envBoolOr(key string, def bool) bool {
	if v := e.envOr(key, ""); v != "" {
		switch v {
		case "1", "true", "TRUE", "True", "yes", "on":
			return true
		case "0", "false", "FALSE", "False", "no", "off":
			return false
		}
	}
	return def
}

func (e envSource) envOr(key, def string) string {
	if e.processEnv {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	if v := e.values[key]; v != "" {
		return v
	}
	return def
}

// envAllowEmptyOr is for nullable settings where an explicitly empty process
// environment value must override a non-empty config-file value or default.
func (e envSource) envAllowEmptyOr(key, def string) string {
	if e.processEnv {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
	}
	if v, ok := e.values[key]; ok {
		return v
	}
	return def
}

func (e envSource) envListOr(key string, def []string) []string {
	v := e.envOr(key, "")
	if v == "" {
		return append([]string(nil), def...)
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), def...)
	}
	return out
}

func (e envSource) envIntOr(key string, def int) int {
	if v := e.envOr(key, ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func validateStrictMTProtoCapacityEnv(e envSource) error {
	for _, key := range []string{
		"TELESRV_MTPROTO_MAX_CONNECTIONS",
		"TELESRV_MTPROTO_MAX_CONNECTIONS_PER_IP",
		"TELESRV_MTPROTO_MAX_CONCURRENT_HANDSHAKES",
		"TELESRV_MTPROTO_RPC_MAX_INFLIGHT",
		"TELESRV_MTPROTO_RPC_QUEUE_SIZE",
		"TELESRV_MTPROTO_RPC_GLOBAL_WORKERS",
		"TELESRV_MTPROTO_RPC_GLOBAL_MAX_TASKS",
		"TELESRV_MTPROTO_RPC_DELIVERY_HOOK_WORKERS",
		"TELESRV_MTPROTO_RPC_DELIVERY_HOOK_MAX_PENDING",
		"TELESRV_MTPROTO_RPC_EXECUTION_MAX_ENTRIES",
		"TELESRV_MTPROTO_RPC_EXECUTION_AUTH_MAX_ENTRIES",
		"TELESRV_MTPROTO_RPC_EXECUTION_SESSION_MAX_ENTRIES",
		"TELESRV_MTPROTO_RPC_EXECUTION_PENDING_PER_AUTH",
		"TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE",
		"TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE",
		"TELESRV_CONTACT_REVERSE_BATCH_MAX_PAIRS",
		"TELESRV_CONTACT_REVERSE_BATCH_QUEUE",
		"TELESRV_CONTACT_SNAPSHOT_CACHE_MAX_VIEWERS",
		"TELESRV_PROFILE_PHOTO_CACHE_MAX",
	} {
		if raw := e.envOr(key, ""); raw != "" {
			if _, err := strconv.Atoi(raw); err != nil {
				return fmt.Errorf("%s must be a base-10 integer: %w", key, err)
			}
		}
	}
	for _, key := range []string{
		"TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES",
		"TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES",
		"TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES",
		"TELESRV_MTPROTO_OUTBOUND_CRITICAL_GLOBAL_MAX_BYTES",
		"TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES",
	} {
		if raw := e.envOr(key, ""); raw != "" {
			if _, err := strconv.ParseInt(raw, 10, 64); err != nil {
				return fmt.Errorf("%s must be a base-10 int64: %w", key, err)
			}
		}
	}
	return nil
}

func (e envSource) envInt64Or(key string, def int64) int64 {
	if v := e.envOr(key, ""); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func (e envSource) envFloatOr(key string, def float64) float64 {
	if v := e.envOr(key, ""); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return def
}

func validDecimalUint64(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 20 {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

// envDurationOr reads a duration setting in time.ParseDuration format (e.g. "200ms", "30s"); falls back to the default on parse failure.
func (e envSource) envDurationOr(key string, def time.Duration) time.Duration {
	if v := e.envOr(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
