// Package config 负责 telesrv 运行配置的加载与校验。
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

// Config 是 telesrv 的运行配置。
type Config struct {
	// ListenAddr 是 MTProto TCP 监听地址。
	// 需与 TDesktop patch 指向的自建 DC 地址/端口一致（记录于 docs/tdesktop-patch-notes.md）。
	ListenAddr string
	// WebSocketEnable 在同一端口启用 MTProto-over-WebSocket 分流（WebA/telegram-tt）。
	WebSocketEnable bool
	// WebSocketAllowedOrigins 是允许浏览器发起 WS upgrade 的页面 origin；"*" 仅用于临时调试。
	WebSocketAllowedOrigins []string
	// AdvertiseIP 是写入 help.getConfig DCOptions 的对外可达 IP（客户端据此连接本 DC）。
	AdvertiseIP string
	// AdvertisePort 是写入 help.getConfig DCOptions 的对外可达端口。
	// 默认跟随 ListenAddr；独立 Core 进程应显式设置为 Edge 对外端口。
	AdvertisePort int
	// RSAKeyPath 是 server RSA 私钥的 PEM 路径；不存在时自动生成。
	RSAKeyPath string
	// DC 是本 server 的 DC ID。
	DC int
	// DefaultCountryCode 是 help.getNearestDc 返回的 ISO 3166-1 alpha-2 国家码。
	// 客户端登录页据此预选国家和国际电话区号；例如 CN 对应 +86。
	DefaultCountryCode string
	// StrictDCCheck enables the default-off key-exchange DC-label diagnostic.
	// The normal single-backend mode accepts every wire int32 label without
	// partitioning auth keys, sessions, or business state. See
	// mtprotoedge.Options.StrictDC for the optional strict behavior.
	StrictDCCheck bool
	// CheckNumberFormat 要求普通手机号通过已分配号段校验。默认关闭，
	// 国家号码计划无法描述的 4-32 位非零开头号码也可作为登录身份。
	// +888 虚拟号码与保留系统号继续使用各自的独立规则。
	CheckNumberFormat bool
	// MTProtoMaxConnections / PerIP 覆盖 raw Accept、codec sniff、握手到认证 session
	// 的完整物理连接生命周期；负数关闭对应 admission 上限。
	MTProtoMaxConnections      int
	MTProtoMaxConnectionsPerIP int
	// MTProtoMaxConcurrentHandshakes 限制昂贵 RSA/DH exchange 并发；负数关闭。
	MTProtoMaxConcurrentHandshakes int
	// MTProto RPC 使用 Server 共享公平调度器；per-connection 与 global 预算共同限制
	// goroutine、排队任务和 request memory charge。legacy charge 等于 copied body，
	// exact charge 是 typed decode 前的保守 materialization 上界，不等同 wire bytes。
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
	// MTProtoInboundFrameGlobalMaxBytes 是 transport wire + 最大解密 plaintext 的
	// 进程级在途预算；frame 长度读出后、payload 分配前预留。
	MTProtoInboundFrameGlobalMaxBytes int64
	// MTProto outbound mailbox 按连接有界；resend pending body 另受 Server 全局预算约束。
	MTProtoOutboundQueueSize              int
	MTProtoOutboundControlQueueSize       int
	MTProtoOutboundTrackedGlobalMaxBytes  int64
	MTProtoOutboundCriticalGlobalMaxBytes int64
	MTProtoOutboundWriteGlobalMaxBytes    int64

	// DebugAddr 是 net/http/pprof 调试端点监听地址（CPU/heap/goroutine/mutex/block 剖析）。
	// telesrv 是宿主进程、不在 docker 内，docker stats 看不到它，性能定位主要靠此端点。
	// 默认仅绑 127.0.0.1，避免 profile 数据对外暴露；置空关闭。生产需远程抓取时走 SSH 隧道，
	// 不要改成 0.0.0.0。
	DebugAddr string
	// BotAPIAddr 是最小 HTTP Bot API 网关监听地址；为空关闭。该网关复用 MTProto
	// app/store 事实源，不维护独立 bot 状态。
	BotAPIAddr string
	// AdminAPIAddr 是 telesrv 进程内管理写 API 监听地址；为空关闭。
	AdminAPIAddr string
	// AdminAPIToken 是 Admin API bearer token；开启 AdminAPIAddr 时必须显式配置。
	AdminAPIToken string
	// GroupCallControlAddr 是 Core 接收独立 SFU 媒体活性回报的内部 HTTP 监听地址；Core 入口必填。
	GroupCallControlAddr string
	// GroupCallControlURL 是独立 SFU 调用 Core 媒体活性回报 API 的内部 URL。
	GroupCallControlURL string
	// GroupCallControlToken 是 groupcall control API/client 共用的 bearer token。
	GroupCallControlToken string
	// CoreExecGRPCAddr 是 Core 暴露生产 CoreExec gRPC API 的内部监听地址；Core 入口必填。
	CoreExecGRPCAddr string
	// CoreExecGRPCResolver 选择 Edge -> CoreExec gRPC 服务发现 provider：static 或 dns。
	CoreExecGRPCResolver string
	// CoreExecGRPCTargets 是 Edge 调用 CoreExec gRPC API 的静态多 endpoint 列表。
	CoreExecGRPCTargets string
	// CoreExecGRPCRequestTimeout 是 Edge -> CoreExec gRPC unary request 默认 deadline。
	CoreExecGRPCRequestTimeout time.Duration
	// CoreExecGRPCTLSCertFile/CoreExecGRPCTLSKeyFile 是 CoreExec gRPC server TLS 证书与私钥；二者必须同时配置。
	CoreExecGRPCTLSCertFile string
	CoreExecGRPCTLSKeyFile  string
	// CoreExecGRPCTLSClientCAFile 配置后，CoreExec gRPC server 要求并验证 Edge client certificate。
	CoreExecGRPCTLSClientCAFile string
	// CoreExecGRPCTLSCAFile 是 Edge 调 CoreExec gRPC 时信任的 root CA bundle。
	CoreExecGRPCTLSCAFile string
	// CoreExecGRPCTLSServerName 是 Edge gRPC client 校验证书时使用的 server name。
	CoreExecGRPCTLSServerName string
	// CoreExecGRPCTLSClientCertFile/CoreExecGRPCTLSClientKeyFile 是 Edge gRPC client certificate；二者必须同时配置。
	CoreExecGRPCTLSClientCertFile string
	CoreExecGRPCTLSClientKeyFile  string
	// CoreExecToken 是 CoreExec gRPC API/client 共用的 bearer token；Core 与 Edge 入口必填。
	CoreExecToken string
	// EgressDeliveryGRPCAddr 是 Egress 暴露 late client ACK 写回 API 的内部监听地址。
	EgressDeliveryGRPCAddr string
	// EgressDeliveryGRPCResolver 选择 Edge -> Egress Delivery gRPC 服务发现 provider：static 或 dns。
	EgressDeliveryGRPCResolver string
	// EgressDeliveryGRPCTargets 是 Edge 调用 Egress Delivery gRPC API 的多 endpoint 列表。
	EgressDeliveryGRPCTargets string
	// EgressDeliveryGRPCRequestTimeout 是 Edge -> Egress Delivery gRPC unary request 默认 deadline。
	EgressDeliveryGRPCRequestTimeout time.Duration
	// EgressDeliveryGRPCTLSCertFile/EgressDeliveryGRPCTLSKeyFile 是 Egress Delivery gRPC server TLS 证书与私钥；二者必须同时配置。
	EgressDeliveryGRPCTLSCertFile string
	EgressDeliveryGRPCTLSKeyFile  string
	// EgressDeliveryGRPCTLSClientCAFile 配置后，Egress Delivery gRPC server 要求并验证 Edge client certificate。
	EgressDeliveryGRPCTLSClientCAFile string
	// EgressDeliveryGRPCTLSCAFile 是 Edge 调 Egress Delivery gRPC 时信任的 root CA bundle。
	EgressDeliveryGRPCTLSCAFile string
	// EgressDeliveryGRPCTLSServerName 是 Edge gRPC client 校验证书时使用的 server name。
	EgressDeliveryGRPCTLSServerName string
	// EgressDeliveryGRPCTLSClientCertFile/EgressDeliveryGRPCTLSClientKeyFile 是 Edge gRPC client certificate；二者必须同时配置。
	EgressDeliveryGRPCTLSClientCertFile string
	EgressDeliveryGRPCTLSClientKeyFile  string
	// EgressDeliveryToken 是 Egress Delivery gRPC API/client 共用的 bearer token。
	EgressDeliveryToken string
	// FileGRPCAddr 是独立 FileData 服务的内部监听地址；cmd/telesrv-file 必须配置。
	FileGRPCAddr string
	// FileGRPCTargets 是 Core/Edge 调 FileData 的目标列表，逗号分隔。
	FileGRPCTargets string
	// FileGRPCResolver 选择 FileData 服务发现 provider：static 或 dns。
	FileGRPCResolver string
	// FileGRPCRequestTimeout 是 Core/Edge -> FileData unary/stream request 默认 deadline。
	FileGRPCRequestTimeout time.Duration
	// FileGRPCTLSCertFile/FileGRPCTLSKeyFile 是 FileData gRPC server TLS 证书与私钥；二者必须同时配置。
	FileGRPCTLSCertFile string
	FileGRPCTLSKeyFile  string
	// FileGRPCTLSClientCAFile 配置后，FileData gRPC server 要求并验证 Core/Edge client certificate。
	FileGRPCTLSClientCAFile string
	// FileGRPCTLSCAFile 是 Core/Edge 调 FileData gRPC 时信任的 root CA bundle。
	FileGRPCTLSCAFile string
	// FileGRPCTLSServerName 是 Core/Edge gRPC client 校验证书时使用的 server name。
	FileGRPCTLSServerName string
	// FileGRPCTLSClientCertFile/FileGRPCTLSClientKeyFile 是 Core/Edge gRPC client certificate；二者必须同时配置。
	FileGRPCTLSClientCertFile string
	FileGRPCTLSClientKeyFile  string
	// FileToken 是 FileData gRPC API/client 共用的 bearer token。
	FileToken string
	// PublicBaseURL 是所有客户端可见 telesrv 链接的公开根 URL。
	// 生产默认 https://telesrv.net；本地可设为 http://127.0.0.1:2401。
	PublicBaseURL string
	// Branding 是所有客户端可见产品名称的进程级只读快照；协议标识、
	// 客户端检测 token 与兼容 header 不属于品牌配置。
	Branding branding.Config
	// UpdatePublicURL is advertised to native clients through
	// help.getConfig.autoupdate_url_prefix. Empty disables desktop updates.
	UpdatePublicURL string
	// UpdateServiceURL is the internal HTTP endpoint used to resolve
	// help.getAppUpdate responses. It defaults to UpdatePublicURL when enabled.
	UpdateServiceURL     string
	UpdateRequestTimeout time.Duration
	// PublicAppScheme 是公开落地页自动唤起自建客户端时使用的 URL scheme。
	// 必须与 TDesktop/Android 客户端构建时注册的 scheme 一致，且不能占用 tg/http/https。
	PublicAppScheme string
	// PublicAppLinkBase 是可选的 host-based 自建客户端链接根，例如
	// owpg://example.com。为空时继续生成 PublicAppScheme://<route>；非空时
	// 生成 <base>/<route>，同时保留旧 scheme 作为服务端输入兼容。
	PublicAppLinkBase string
	// PublicWebBaseURL 是公开 username 页面“Open in Web”按钮指向的 Web 客户端根 URL。
	PublicWebBaseURL string
	// PublicAppName 是公开落地页展示的产品名，不参与协议路由。
	PublicAppName string
	// PublicLinkWebAddr 是公开链接落地页监听地址；为空关闭。
	// 生产应只监听 loopback，并由 nginx 将 /<username>、/addstickers/、/addemoji/、
	// /addlist/ 与 hash-only /appeal/ 路由反代到该地址。
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
	// Admin UI 独立进程配置项保留在统一配置中，cmd/telesrv-admin 也按同名 env 读取。
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

	// GeoIP* 控制 account.getAuthorizations 里会话国家/地区文案的解析。协议层
	// 没有 help.requestIpAddress 的对等实现,没配这些项时 country/region 继续
	// 回传 "Unknown" 占位文案,与启用前完全一致。
	//
	// GeoIPEndpoints 是按顺序 failover 的后端列表:每个元素是含 {ip} 占位符的 URL
	// 模板,第一个是主力,上一个查不到或此刻不可用就自动落到下一个。空列表表示未启用。
	//
	// 单后端必然会让一部分会话显示 Unknown,实测 2026-09 四家的失效方式各不相同:
	// reallyfreegeoip.org 连续几次请求后 429(约 60s 恢复);geojs.io 只有 ASN 级
	// 覆盖,1.1.1.1 那种地址连 country 字段都没有;hackmyip.com 间歇性回 400;
	// ipapi.is 对保留网段回 is_bogon。所以缓存和熔断不是优化而是必需品,调小
	// CacheTTL 或 NegativeTTL 会明显提高限流概率。
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

	// PostgresDSN 是业务数据（auth_key / user / authorization 等）持久化的 PostgreSQL 连接串。
	// 依赖由 deploy/docker-compose.yml 启动；职责划分见 docs/persistence-layer.md。
	PostgresDSN string
	// PostgresMaxConns 是 pgxpool 最大连接数。<=0 用 pgx 默认（max(4, NumCPU)，生产偏小）。
	// 需覆盖发送事务 + outbox worker 并发 + RPC 读，过小会在高并发下排队（表现为尾延迟突刺）。
	PostgresMaxConns int
	// PostgresMinConns 是启动时预热的 pgxpool 连接数，降低 TDesktop 冷启动并发 RPC 的建连等待。
	PostgresMinConns int
	// PostgresCounterRecoveryMaxConns 是 Redis durable counter 冷恢复专用 PostgreSQL
	// 连接池上限。该池必须与业务事务池物理独立，避免事务持有主池连接时恢复计数器形成自锁。
	PostgresCounterRecoveryMaxConns int
	// RedisAddr 是高频易失态（验证码、限流计数、update 队列）的 Redis 地址。
	RedisAddr string
	// RedisPassword 是 Redis 密码；开发默认空。
	RedisPassword string
	// RedisDB 是 Redis 逻辑库编号。
	RedisDB int
	// InstanceID 是当前进程在跨 Edge/Core 控制、在线位置索引与 pub/sub 去重中的稳定标识。
	// 为空时进程会在启动时按 hostname/pid/time 生成一个临时实例 ID。
	InstanceID string
	// EdgeLocationTTL / HeartbeatInterval 控制 O(1) Edge instance location lease 的
	// 存活时间与续租周期；session 位置只在状态变化时增量写入，不随 heartbeat 全量刷新。
	EdgeLocationTTL               time.Duration
	EdgeLocationHeartbeatInterval time.Duration

	// DevAuthCode 是开发固定验证码；生产短信/风控不在当前范围内。
	DevAuthCode string
	// 欢迎消息模板分别用于登录成功后的欢迎消息；登录码模板用于已有账号
	// 777000 durable 验证码投递，管理面板覆盖可在下一次发码时实时生效。
	WelcomeMessagePhoneTemplate string
	WelcomeMessageEmailTemplate string
	LoginCodeMessageTemplate    string
	// AuthCodeTTL 是登录/注册/邮箱验证 code 的有效期。
	AuthCodeTTL time.Duration
	// PhoneCodeLength 是使用外部 provider 时生成的短信验证码长度。development
	// provider 继续使用 DevAuthCode 原样，不受此字段影响。
	PhoneCodeLength int
	// AuthCodeMaxAttempts 是同一 phone_code_hash / email verification code 的最大错误次数。
	// 达到上限后验证码立即失效，用户必须重发。
	AuthCodeMaxAttempts int
	// AuthCodePhoneRateLimit / AuthCodeAuthKeyRateLimit 对未授权验证码签发按规范化手机号摘要
	// 与连接实际 raw auth_key 分别限流。两个维度共用 AuthCodeRateWindow；<=0 关闭对应维度。
	// 手机号只以 SHA-256 摘要进入限流 key，禁止把原文写入 Redis key 或日志。
	AuthCodePhoneRateLimit   int
	AuthCodeAuthKeyRateLimit int
	AuthCodeRateWindow       time.Duration
	// LoginEmailEnable 启用手机号登录流程中的邮箱验证码投递。
	LoginEmailEnable bool
	// LoginEmailRequireSetup 为 true 时，没有登录邮箱的账号/新手机号会要求先设置邮箱。
	LoginEmailRequireSetup bool
	// LoginEmailCodeLength 是邮箱验证码长度。
	LoginEmailCodeLength int
	// PhoneCodeDeliveryProvider 选择普通登录/注册与改号验证码的投递方式：
	// development 保留固定码与 777000 app-code；webhook 使用随机 SMS code。
	PhoneCodeDeliveryProvider string
	// EmailCodeDeliveryProvider 选择登录邮箱与邮箱 setup/change 的投递方式。
	EmailCodeDeliveryProvider string
	// OTPWebhook* 定义固定 v1 webhook 协议的端点、HMAC secret 与请求超时。
	OTPWebhookURL     string
	OTPWebhookSecret  string
	OTPWebhookTimeout time.Duration
	// SMTP* 是 email provider=smtp 时使用的出站邮件配置。
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPFromName string
	SMTPTLSMode  string
	SMTPTimeout  time.Duration
	// MapboxToken 是服务端代理地图缩略图（upload.getWebFile）请求 Mapbox Static Images API
	// 的 access token；为空则关闭代理、回退确定性占位图。客户端选点器 token 经 appConfig
	// `tdesktop_config_map` 下发（同源运行时配置）。
	MapboxToken string
	// MapTileCacheDir 是已抓取地图缩略图的磁盘缓存目录（保证分片续传字节一致 + 控制配额消耗）。
	MapTileCacheDir string
	// ExternalMediaEnable 控制是否启用外链媒体抓取（inputMediaPhoto/DocumentExternal）。
	// 默认开启：服务端 SSRF 安全抓取用户 URL 并铸造 Photo/Document（含 SSRF 防护/大小/限速）。
	ExternalMediaEnable bool
	// ExternalMediaMaxBytes 是单次外链抓取响应体上限；<=0 用默认 10MB。
	ExternalMediaMaxBytes int64
	// ExternalMediaRatePerMin 是全局每分钟外链抓取上限（防放大攻击）；<=0 用默认。
	ExternalMediaRatePerMin int
	// WebPagePreviewEnable 控制是否启用链接预览抓取（messages.getWebPagePreview / 发送时挂卡片）。
	// 默认开启：服务端 SSRF 安全抓取消息内 URL，解析 OG/Twitter/标题元数据并铸造预览卡片。
	WebPagePreviewEnable bool
	// WebPagePreviewMaxBytes 是单次链接预览抓取响应体上限（HTML 与预览图共用）；<=0 用默认 5MB。
	WebPagePreviewMaxBytes int64
	// WebPagePreviewRatePerMin 是全局每分钟链接预览抓取上限；<=0 用默认。一次解析最多 2 次上游。
	WebPagePreviewRatePerMin int
	// LangPackSeedDir 是 TDesktop 语言包 .strings 种子目录。
	LangPackSeedDir string
	// OfficialGiftsDir 是 cmd/giftfetch 生成的只读官方礼物快照目录。
	OfficialGiftsDir string
	// StarGiftTONStartingGrant 是 telesrv 内部 TON 账本首次访问时授予的 nanoton。
	// 该账本只用于自建服务端礼物链路，不连接任何外部区块链。
	StarGiftTONStartingGrant int64
	// BlobBackendKind 选择唯一永久 blob backend：localfs（默认）或 s3。
	BlobBackendKind string
	// BlobDir 是 localfs 永久媒体根目录。s3 模式不从这里回退读取。
	BlobDir string
	// BlobStagingDir 是 s3 模式的本地临时上传分片与写入 spool 根目录；
	// 它不是永久 backend，成功组装后的媒体只存在 S3。
	BlobStagingDir string
	// S3* 配置 MinIO/AWS S3 兼容永久 backend。endpoint 不含 URL scheme；
	// access key/secret 没有默认值，CreateBucket 默认关闭。
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
	// StickerSeedDir 是 reaction / sticker 资源种子目录（导入到 documents/sticker_sets + blob）。
	StickerSeedDir string
	// GifSeedDir is an optional bounded .gif/.mp4 drop directory for @gif.
	GifSeedDir string
	// StickerSeedMaxSets 限制导入的常规贴纸集数量（避免启动时导入过多包），<=0 表示不限。
	StickerSeedMaxSets int
	// PremiumPromoSeedDir 是 help.getPremiumPromo 视频与缩略图导出目录。
	// 目录缺失时保留无视频兼容响应；目录存在但内容非法时启动失败。
	PremiumPromoSeedDir string
	// IdentityDir 存放管理员可编辑的服务器身份（identity.json 中的名称/简介、icon.<ext>
	// 图标）——见 internal/identity。由 cmd/telesrv-admin 的面板写入、cmd/telesrv 每次
	// 使用前实时读取，编辑即时生效、无需重启服务器。
	IdentityDir string
	// BusinessAIProvider 控制服务端 Business automation 回复生成器。
	// 空值/"echo" 回显触发私聊文本，用于跑通后续 AI provider 链路；
	// "template" 使用 quick reply 模板。
	BusinessAIProvider string
	// AIEnabled 控制客户端输入框 AI 改写/润色能力；关闭时 getTones 返回空集合以隐藏入口。
	AIEnabled bool
	// AIProviders 是 compose AI provider 链路，按顺序尝试；默认 local，不出网。
	AIProviders []AIProviderConfig
	// AITimeout 是单次 provider 调用总超时。
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
	// AIRateLimit/AIRateWindow 是账号级 compose AI 限流。
	AIRateLimit  int
	AIRateWindow time.Duration
	// AIPrivacyLogContent 为 false 时日志只写长度/provider/状态，不写用户输入和生成文本。
	AIPrivacyLogContent bool
	// Translation* controls messages.translateText. Remote provider credentials
	// are reused from AIProviders; TranslationProviders optionally selects names
	// from that list. The deterministic local provider is never used.
	TranslationEnabled    bool
	TranslationProviders  []string
	TranslationTimeout    time.Duration
	TranslationRateLimit  int
	TranslationRateWindow time.Duration
	// ASRURL/ASRTimeout 指向本地 ASR HTTP 后端（whisper-server 的 /inference）。
	// URL 为空则语音转文字不可用，messages.transcribeAudio 显式回 TRANSCRIPTION_FAILED。
	ASRURL     string
	ASRTimeout time.Duration
	// DonationWalletKeyFile names the shared AES-256-GCM key file that encrypts
	// the crypto donations wallet mnemonic. Every Core must point at the same
	// key; it is never generated (decision D7). Empty disables donations.
	DonationWalletKeyFile string
	// DonationPollInterval is how often each chain watcher polls for blocks.
	DonationPollInterval time.Duration
	// TempKeyResolveCacheMaxEntries 是 Router temp→perm 解析缓存容量。
	TempKeyResolveCacheMaxEntries int
	// TempKeyResolveCacheTTL 是 temp→perm 绑定的进程内复核周期。绑定/revoke 有精确
	// 失效，TTL 作为跨进程或异常路径兜底；默认 30m 避免大连接数下每 5s 全量打 PG。
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
	// AuthUserCacheTTL 是 auth_key->user 正向授权缓存的兜底复核周期。写侧
	// invalidation 仍是主路径；TTL 防止跨 Core pub/sub/control 丢失后旧授权永久留在内存。
	AuthUserCacheTTL time.Duration
	// AuthKeyCacheMaxEntries 是 Edge raw auth_key_id->key material 正向热缓存容量。
	// Edge 不持久化 auth_keys，也不在热路径访问 PostgreSQL；缓存只保存 key/salt/expiry，
	// Layer/client metadata 仍由 CoreExec durable profile resolver 负责。
	AuthKeyCacheMaxEntries int
	// AuthKeyCacheTTL 只限制 Edge auth key 热缓存资源占用。destroy_auth_key 通过
	// Core 发布 Redis 失效事件即时清理缓存并关闭 raw-key sessions，不能依赖 TTL 保正确性。
	AuthKeyCacheTTL time.Duration

	// ChannelRowCacheMaxEntries 是「共享频道行」进程内缓存容量(channelID→domain.Channel)。
	// 由 channels 表 LISTEN/NOTIFY 触发器实时失效(强一致、零 TTL)。<=0 禁用缓存与监听。
	ChannelRowCacheMaxEntries        int
	ChannelTopMessageCacheMaxEntries int
	// ChannelMemberCacheMaxEntries 是频道成员/访问态 read-model 缓存容量((channelID,userID)→member)。
	// 由 read_model_versions 统一通知实时失效。<=0 禁用缓存。
	ChannelMemberCacheMaxEntries int
	// ChannelDialogCacheMaxEntries 是频道 dialog 读投影缓存容量((viewerUserID,channelID)→dialog)。
	// 由 channel_base/channel_member/dialog_light 统一通知实时失效。<=0 禁用缓存。
	ChannelDialogCacheMaxEntries     int
	ChannelDifferenceCacheMaxEntries int
	ChannelDifferenceCacheMaxBytes   int64
	ChannelDifferenceCacheTTL        time.Duration
	// ChannelBoostCacheMaxEntries 是频道 boost read-model 缓存容量，覆盖当前用户
	// SelfBoostsApplied 与频道总 active boost 数两类投影。写入 channel_boost_slots
	// 时精确失效，TTL 兜底自然过期。<=0 禁用缓存。
	ChannelBoostCacheMaxEntries int
	// ChannelBoostCacheTTL 是 boost 读投影在未收到写侧通知时的最大陈旧窗口。
	ChannelBoostCacheTTL time.Duration

	// OutboxWorkers 是 telesrv-egress 并发 outbox worker 数。用户先稳定哈希到固定 logical shard，
	// 每个 shard 只归一个 worker，故提高 worker 数不会破坏同一用户 pts 顺序。
	OutboxWorkers int
	// OutboxBatch 是 telesrv-egress 每次 claim transactional outbox 的最大条数。
	// 调大提升吞吐、增大单批 PG/推送压力；调小降低延迟抖动。配套压测见 docs/message-module.md。
	OutboxBatch int
	// OutboxLeaseTimeout 是 'dispatching' 行被判定为租约过期、允许其它 worker 重新 claim 的时长。
	// 取值需大于单批投递耗时，否则会重复推送；过大则 worker 崩溃后积压恢复变慢。
	OutboxLeaseTimeout time.Duration
	// DeliveryAttemptTimeout freezes the absolute durable Edge command lifetime
	// at database claim time. It is independent from transient Redis admission.
	DeliveryAttemptTimeout time.Duration
	// DeliveryClockSkewAllowance delays database retry after the Edge wire fence
	// so old/new attempts cannot overlap across hosts.
	DeliveryClockSkewAllowance time.Duration
	// OutboundPushTimeout 是 telesrv-egress 等待 Edge 确认 outbound 队列接受的最长时间。
	OutboundPushTimeout time.Duration
	// SendRateLimit 是显式部署策略下账号级发送窗口内允许的消息条数；<=0 表示关闭。
	// 默认关闭：durable admission/backpressure 负责系统容量，不能把正常客户端快发误判成洪泛。
	SendRateLimit int
	// SendRateWindow 是发送限流窗口。
	SendRateWindow time.Duration
	// CatchupRateLimit 是 difference 类 catch-up RPC（getChannelDifference / getPeerDialogs）
	// 每用户每窗口允许的次数；<=0 关闭（设计 fan-out Phase 2 / §10.3，放开大群 nudge 全速前置）。
	CatchupRateLimit int
	// CatchupRateWindow 是 catch-up 限流窗口。
	CatchupRateWindow time.Duration
	// ChannelNudgeMaxTargets 是一次 fan-out >cap nudge 的目标上限；<=0 用内置默认。
	ChannelNudgeMaxTargets int
	// UpdateEventRetention 是 durable update log 保留期；只清理已被水位/state 覆盖的事件。
	UpdateEventRetention time.Duration
	// BotAPIUpdateRetention 是 bot_api_updates 投递队列的最大保留期（官方 Bot API 语义 24h）；
	// 已确认的行另按固定短宽限提前回收（性能审计 H1）。
	BotAPIUpdateRetention time.Duration
	// OrphanAuthKeyRetention 是握手已创建、但没有 authorization/temp binding/活跃连接的
	// auth key 最短保留期。过期后由有界 GC 回收；客户端收到 -404 会重建 key。
	OrphanAuthKeyRetention time.Duration
	// RetentionInterval 是 retention worker 的运行间隔。
	RetentionInterval time.Duration
	// RetentionBatch 是单次 retention 最多删除的行数。
	RetentionBatch int
	// UploadPartTTL 是未组装上传分片的保留期。
	UploadPartTTL time.Duration
	// UploadPartGCInterval 是 upload_parts GC worker 的运行间隔。
	UploadPartGCInterval time.Duration
	// UploadPartGCBatch 是单次 upload_parts GC 最多删除的行数。
	UploadPartGCBatch int
	// UploadInFlightMaxBytes 是单用户未组装上传分片的字节上限；<=0 表示不限。
	UploadInFlightMaxBytes int64
	// UploadInFlightMaxParts 是单用户未组装上传分片行数上限；<=0 表示不限。
	UploadInFlightMaxParts int
	// UploadInFlightMaxFiles 是单用户未组装 file_id 数上限；<=0 表示不限。
	UploadInFlightMaxFiles int

	// CallRingTimeout 是私聊通话服务端兜底超时（振铃/Accepted 悬挂），与下发给
	// 客户端的 callRingTimeoutMs（compat/tdesktop/config.go，90000ms）同源。
	CallRingTimeout time.Duration
	// CallTombstoneTTL 是终态通话 tombstone 保留期（幂等/晚到 RPC 吸收窗口）。
	CallTombstoneTTL time.Duration
	// CallMaxActivePerUser 是单用户并发非终态通话上限。
	CallMaxActivePerUser int
	// CallRegistryMaxEntries 是进程内通话 registry 的全局硬上限。
	CallRegistryMaxEntries int
	// CallSignalingMaxBytes 是 phone.sendSignalingData 单条载荷上限。
	CallSignalingMaxBytes int
	// CallSignalingRate 是单通话每秒信令转发上限（超限静默丢弃）。
	CallSignalingRate int
	// CallExpiryInterval 是通话超时兜底 dispatcher 的轮询间隔。
	CallExpiryInterval time.Duration

	// PremiumGrantMonths 是新注册账号默认赠送的会员月数；0 关闭赠送。
	// 存量账号的一次性赠送由迁移 0094 backfill，不受该配置影响。
	PremiumGrantMonths int
	// PremiumBotUsername is the public username advertised in app config and
	// Premium deep links. The reserved bot id remains stable.
	PremiumBotUsername string
	PremiumBotUserID   int64
	// PremiumPlans is the authoritative Stars catalog. Each entry carries a
	// fixed month count, exact duration in days and Stars price.
	PremiumPlans []domain.PremiumPlan

	// PasskeyRPID 是 passkey(WebAuthn) relying-party id（域名）。服务端据此校验
	// authData.rpIdHash；真机经 Android CredentialManager 时须与托管 assetlinks.json
	// 的公网域名一致(详见 docs)。本地/软件 authenticator 验证用任意稳定值即可。
	PasskeyRPID string
	// PasskeyAllowedOrigins 是允许的 WebAuthn origin 白名单；为空=不强校验 origin
	//（服务端通常不预知 Android apk-key-hash origin）。
	PasskeyAllowedOrigins []string
	// StarsStartingGrant 是 Stars 本地账本的起始余额（首读时惰性授予、granted 布尔幂等，
	// 新老账号都覆盖、免回填迁移）；0 关闭自动授予。
	StarsStartingGrant int64
	// PremiumSweepInterval 是会员到期 sweeper 的轮询间隔。premium 下发正确性
	// 由读取路径即时派生，sweeper 只负责清理过期行并推 updateUser 通知。
	PremiumSweepInterval time.Duration
	// PremiumSweepBatch 是单次到期清理的最大行数。
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

	// GroupCallCheckTTL 是群通话参与者保活水位的过期阈值（客户端 Connecting 态
	// 4s 一跳；M1 起 SFU liveness reporter 同样刷新该水位）。
	GroupCallCheckTTL time.Duration
	// GroupCallSweepInterval 是幽灵参与者 sweeper 的轮询间隔。
	GroupCallSweepInterval time.Duration
	// GroupCallMaxParticipants 是单房间参与者上限（演示规模）。
	GroupCallMaxParticipants int

	// TURNEnable 为 false 时 Core 不下发中继且 SFU 不启动 TURN media listener。
	TURNEnable bool
	// TURNUDPPort 是 SFU role 持有的 TURN/STUN 监听端口；Core 只把该地址下发
	// 给客户端。它与群通话 SFU 端口不能共用。
	TURNUDPPort int
	// TURNBindIP 是 SFU role 实际绑定 TURN listener 和 relay sockets 的 IPv4
	// 地址。bridge 发布模式通常为 0.0.0.0；host network 模式必须由应用自身遵守。
	TURNBindIP string
	// TURNAdvertiseIP 是写进 phoneConnectionWebrtc 与 relay 分配的客户端可达
	// 地址，默认回落 SFUAdvertiseIP → AdvertiseIP。
	TURNAdvertiseIP string
	// TURNSecret 是 Core credential issuer 与 SFU TURN listener 共享的 REST HMAC
	// 密钥；split runtime 启用 TURN 时必须显式配置稳定值。
	TURNSecret string
	// TURNRelayMinPort/TURNRelayMaxPort 限定 relay 分配端口段（防火墙放行范围）。
	TURNRelayMinPort int
	TURNRelayMaxPort int
	// CallTURNCredentialTTL 是按通话签发的 TURN 凭据有效期。
	CallTURNCredentialTTL time.Duration
	// CallForceRelay 强制 p2p_allowed=false（调试 TURN 中继路径用）。
	// 私聊通话 P2P 默认仅限互为联系人（rpc 层互认联系人硬门槛 + phone_p2p
	// 默认 AllowContacts）；此开关让整条链路强制走中继。
	CallForceRelay bool

	// LiveStreamEnable 为 true 时启用频道 RTMP 直播媒体面（内嵌 RTMP ingest + ffmpeg 切段）。
	LiveStreamEnable bool
	// LiveStreamRtmpAddr 是 RTMP ingest 的 TCP 监听地址（默认 ":2400"）。
	LiveStreamRtmpAddr string
	// LiveStreamRtmpURL 是返回给推流端（OBS）的服务器地址；为空回落 rtmp://<AdvertiseIP>:2400/live。
	LiveStreamRtmpURL string
	// LiveStreamFFmpegPath 是 ffmpeg 可执行路径（默认走 PATH 的 "ffmpeg"）。
	LiveStreamFFmpegPath string
	// LiveStreamWorkDir 是切段临时目录（默认系统临时目录）。
	LiveStreamWorkDir string
	// LiveStreamSegmentKeep 是每路流内存保留的 segment 秒数（默认 32）。
	LiveStreamSegmentKeep int

	// SFUUDPPort 是独立 SFU 的单 UDP 端口（pion ICE UDPMux）。Windows 防火墙需放行。
	SFUUDPPort int
	// SFUBindIP 是 standalone SFU 媒体 socket 的本地 IPv4 绑定地址。
	SFUBindIP string
	// SFUAdvertiseIP 是写进下行 candidate 的客户端可达地址，默认回落 AdvertiseIP。
	// ⚠ 127.0.0.1 会让真机 ICE 永远连不上且无任何 RPC 错误（纯媒体面静默失败）。
	SFUAdvertiseIP string
	// SFUOwnerTTL / HeartbeatInterval 控制 callID -> sfu instance owner 租约。
	// 多 SFU 时同一 call 必须粘到同一个 owner，不能由每个实例各自开房间。
	SFUOwnerTTL               time.Duration
	SFUOwnerHeartbeatInterval time.Duration
	// SFUInstanceTTL / HeartbeatInterval 控制可用 SFU 媒体实例发现租约。
	SFUInstanceTTL               time.Duration
	SFUInstanceHeartbeatInterval time.Duration
	// SFUInstanceHealthTimeout 是 Core remote owner selector 对单个 SFU /healthz 探测的上限。
	SFUInstanceHealthTimeout time.Duration
	// SFUInstanceMaxActiveCalls 为 0 表示不限制；>0 时 owner selector 不再选择已满实例。
	SFUInstanceMaxActiveCalls int
	// SFUControlGRPCAddr 暴露独立 SFU 的 gRPC control API；cmd/telesrv-sfu 必须配置。
	SFUControlGRPCAddr string
	// SFUControlGRPCURL 是其它 Core/SFU 实例调用本 SFU gRPC control API 的地址；为空回退 SFUControlGRPCAddr。
	SFUControlGRPCURL string
	// SFUControlGRPCRequestTimeout 是 Core -> SFU gRPC control unary request 默认 deadline。
	SFUControlGRPCRequestTimeout time.Duration
	// SFUControlGRPCTLSCertFile/SFUControlGRPCTLSKeyFile 是 SFU gRPC control server TLS 证书与私钥；二者必须同时配置。
	SFUControlGRPCTLSCertFile string
	SFUControlGRPCTLSKeyFile  string
	// SFUControlGRPCTLSClientCAFile 配置后，SFU gRPC control server 要求并验证 Core/client certificate。
	SFUControlGRPCTLSClientCAFile string
	// SFUControlGRPCTLSCAFile 是 Core/probe 调 SFU gRPC control 时信任的 root CA bundle。
	SFUControlGRPCTLSCAFile string
	// SFUControlGRPCTLSServerName 是 Core/probe gRPC client 校验证书时使用的 server name。
	SFUControlGRPCTLSServerName string
	// SFUControlGRPCTLSClientCertFile/SFUControlGRPCTLSClientKeyFile 是 Core/probe gRPC client certificate；二者必须同时配置。
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
		// help.getConfig 必须下发至少一个可重连的主 DC 地址；远端部署不能
		// 沿用 loopback 默认值，需显式设置客户端实际可达的 IP。
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

		// 用 127.0.0.1 而非 localhost：localhost 在 Windows 上会先解析到 IPv6 ::1，而 Docker
		// Desktop 的端口转发只在 IPv4 监听，IPv6 连接要等 ~1s 超时才回退 IPv4（实测 localhost
		// 建连 1.0s vs 127.0.0.1 6ms）。冷连接洪峰下池扩容的新连接各等 1s → pre-handler 惊群卡顿。
		// 生产由 TELESRV_POSTGRES_DSN 覆盖；该默认值仅作用于本地开发。
		PostgresDSN:                     envOr("TELESRV_POSTGRES_DSN", "postgres://telesrv:telesrv@127.0.0.1:5432/telesrv_v2?sslmode=disable"),
		PostgresMaxConns:                envIntOr("TELESRV_POSTGRES_MAX_CONNS", 50),
		PostgresMinConns:                envIntOr("TELESRV_POSTGRES_MIN_CONNS", 16),
		PostgresCounterRecoveryMaxConns: envIntOr("TELESRV_POSTGRES_COUNTER_RECOVERY_MAX_CONNS", 4),
		RedisAddr:                       envOr("TELESRV_REDIS_ADDR", "127.0.0.1:6399"), // 同理避开 localhost→IPv6 回退延迟
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

// validateGeoIPConfig 校验会话地理归属后端链。未配置端点时全部放行——
// 会话列表会继续回传 "Unknown" 占位文案,这是未启用时的预期行为,不该让服务起不来。
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
	// 全是空白的列表等同于未启用,不该拖出后面那堆数值校验。
	if configured == 0 {
		return nil
	}
	// 上游正常响应 0.6~1.3s。给到 10s 以上就说明值填错了(比如误当成 duration
	// 字符串解析),那时候每一批解析都会先耗掉整个预算才回落 Unknown。
	if cfg.GeoIPTimeout <= 0 || cfg.GeoIPTimeout > 10*time.Second {
		return fmt.Errorf("TELESRV_GEOIP_TIMEOUT must be greater than zero and at most 10s")
	}
	if cfg.GeoIPConcurrency <= 0 || cfg.GeoIPConcurrency > 32 {
		return fmt.Errorf("TELESRV_GEOIP_CONCURRENCY must be 1..32")
	}
	// 负缓存必须显著短于正文缓存:负缓存过期后只应重试失败的那次解析,长到
	// 和正文缓存一样就等于一次限流把该 IP 冻到第二天。
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
	// 不可达窗口必须比限流窗口短:后端只是抖了一下不该离线比配额窗口还久。
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

// envDurationOr 读取 time.ParseDuration 格式（如 "200ms"、"30s"）的时长配置；解析失败回退默认值。
func (e envSource) envDurationOr(key string, def time.Duration) time.Duration {
	if v := e.envOr(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
