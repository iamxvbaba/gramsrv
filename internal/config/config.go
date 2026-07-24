// Package config 负责 telesrv 运行配置的加载与校验。
package config

import (
	"bufio"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/links"
)

const defaultConfigFile = ".env"

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
	// RSAKeyPath 是 server RSA 私钥的 PEM 路径；不存在时自动生成。
	RSAKeyPath string
	// DC 是本 server 的 DC ID。
	DC int
	// StrictDCCheck enables the default-off key-exchange DC-label diagnostic.
	// The normal single-backend mode accepts every wire int32 label without
	// partitioning auth keys, sessions, or business state. See
	// mtprotoedge.Options.StrictDC for the optional strict behavior.
	StrictDCCheck bool
	// MTProtoMaxConnections / PerIP 覆盖 raw Accept、codec sniff、握手到认证 session
	// 的完整物理连接生命周期；负数关闭对应 admission 上限。
	MTProtoMaxConnections      int
	MTProtoMaxConnectionsPerIP int
	// MTProtoMaxConcurrentHandshakes 限制昂贵 RSA/DH exchange 并发；负数关闭。
	MTProtoMaxConcurrentHandshakes int
	// MTProto RPC 使用 Server 共享公平调度器；per-connection 与 global 预算共同限制
	// goroutine、排队任务和 request memory charge。legacy charge 等于 copied body，
	// exact charge 是 typed decode 前的保守 materialization 上界，不等同 wire bytes。
	MTProtoRPCMaxInflight    int
	MTProtoRPCQueueSize      int
	MTProtoRPCTimeout        time.Duration
	MTProtoRPCGlobalWorkers  int
	MTProtoRPCGlobalMaxTasks int
	MTProtoRPCGlobalMaxBytes int64
	// Pending ownership and completed rpc_result replay state share a three-level
	// global/raw-auth/session budget over the full MTProto duplicate horizon.
	MTProtoRPCResultCacheMaxEntries        int
	MTProtoRPCResultCacheMaxBytes          int64
	MTProtoRPCResultCacheAuthMaxEntries    int
	MTProtoRPCResultCacheAuthMaxBytes      int64
	MTProtoRPCResultCacheSessionMaxEntries int
	MTProtoRPCResultCacheSessionMaxBytes   int64
	MTProtoRPCResultPendingPerAuth         int
	// MTProtoInboundFrameGlobalMaxBytes 是 transport wire + 最大解密 plaintext 的
	// 进程级在途预算；frame 长度读出后、payload 分配前预留。
	MTProtoInboundFrameGlobalMaxBytes int64
	// MTProto outbound mailbox 按连接有界；resend pending body 另受 Server 全局预算约束。
	MTProtoOutboundQueueSize             int
	MTProtoOutboundControlQueueSize      int
	MTProtoOutboundTrackedGlobalMaxBytes int64
	MTProtoOutboundWriteGlobalMaxBytes   int64

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
	// PublicBaseURL 是所有客户端可见 telesrv 链接的公开根 URL。
	// 生产默认 https://telesrv.net；本地可设为 http://127.0.0.1:2401。
	PublicBaseURL string
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

	// PostgresDSN 是业务数据（auth_key / user / authorization 等）持久化的 PostgreSQL 连接串。
	// 依赖由 deploy/docker-compose.yml 启动；职责划分见 docs/persistence-layer.md。
	PostgresDSN string
	// PostgresMaxConns 是 pgxpool 最大连接数。<=0 用 pgx 默认（max(4, NumCPU)，生产偏小）。
	// 需覆盖发送事务 + outbox worker 并发 + RPC 读，过小会在高并发下排队（表现为尾延迟突刺）。
	PostgresMaxConns int
	// PostgresMinConns 是启动时预热的 pgxpool 连接数，降低 TDesktop 冷启动并发 RPC 的建连等待。
	PostgresMinConns int
	// RedisAddr 是高频易失态（验证码、限流计数、update 队列）的 Redis 地址。
	RedisAddr string
	// RedisPassword 是 Redis 密码；开发默认空。
	RedisPassword string
	// RedisDB 是 Redis 逻辑库编号。
	RedisDB int

	// DevAuthCode 是开发固定验证码；生产短信/风控不在当前范围内。
	DevAuthCode string
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
	// BlobDir 是本地磁盘 blob backend 根目录（媒体文件字节内容）。
	BlobDir string
	// StickerSeedDir 是 reaction / sticker 资源种子目录（导入到 documents/sticker_sets + blob）。
	StickerSeedDir string
	// StickerSeedMaxSets 限制导入的常规贴纸集数量（避免启动时导入过多包），<=0 表示不限。
	StickerSeedMaxSets int
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
	// TempKeyResolveCacheMaxEntries 是 Router temp→perm 解析缓存容量。
	TempKeyResolveCacheMaxEntries int
	// TempKeyResolveCacheTTL 是 temp→perm 绑定的进程内复核周期。绑定/revoke 有精确
	// 失效，TTL 作为跨进程或异常路径兜底；默认 30m 避免大连接数下每 5s 全量打 PG。
	TempKeyResolveCacheTTL time.Duration

	// ChannelRowCacheMaxEntries 是「共享频道行」进程内缓存容量(channelID→domain.Channel)。
	// 由 channels 表 LISTEN/NOTIFY 触发器实时失效(强一致、零 TTL)。<=0 禁用缓存与监听。
	ChannelRowCacheMaxEntries int
	// ChannelMemberCacheMaxEntries 是频道成员/访问态 read-model 缓存容量((channelID,userID)→member)。
	// 由 read_model_versions 统一通知实时失效。<=0 禁用缓存。
	ChannelMemberCacheMaxEntries int
	// ChannelDialogCacheMaxEntries 是频道 dialog 读投影缓存容量((viewerUserID,channelID)→dialog)。
	// 由 channel_base/channel_member/dialog_light 统一通知实时失效。<=0 禁用缓存。
	ChannelDialogCacheMaxEntries int
	// ChannelBoostCacheMaxEntries 是频道 boost read-model 缓存容量，覆盖当前用户
	// SelfBoostsApplied 与频道总 active boost 数两类投影。写入 channel_boost_slots
	// 时精确失效，TTL 兜底自然过期。<=0 禁用缓存。
	ChannelBoostCacheMaxEntries int
	// ChannelBoostCacheTTL 是 boost 读投影在未收到写侧通知时的最大陈旧窗口。
	ChannelBoostCacheTTL time.Duration

	// OutboxWorkers 是并发 outbox worker 数。用户先稳定哈希到固定 logical shard，
	// 每个 shard 只归一个 worker，故提高 worker 数不会破坏同一用户 pts 顺序。
	OutboxWorkers int
	// OutboxBatch 是 transactional outbox worker 每次 claim 的最大条数。
	// 调大提升吞吐、增大单批 PG/推送压力；调小降低延迟抖动。配套压测见 docs/message-module.md。
	OutboxBatch int
	// OutboxInterval 是 outbox worker 两次 claim 之间的轮询间隔。
	OutboxInterval time.Duration
	// OutboxLeaseTimeout 是 'dispatching' 行被判定为租约过期、允许其它 worker 重新 claim 的时长。
	// 取值需大于单批投递耗时，否则会重复推送；过大则 worker 崩溃后积压恢复变慢。
	OutboxLeaseTimeout time.Duration
	// OutboxPoisonRetention 是 terminal failed outbox head 的隔离窗口。隔离期内保留
	// last_error 供排障，期满只删除在线投递任务；durable user_update_events 仍保留，
	// 客户端可经 updates.getDifference 恢复。
	OutboxPoisonRetention time.Duration
	// OutboxPoisonCleanupInterval 独立于大表 retention 周期清理 terminal failed head，
	// 避免一条确定性坏事件长期冻结同账号更高 pts 的在线投递 lane。
	OutboxPoisonCleanupInterval time.Duration
	// OutboundPushTimeout 是 best-effort updates 推送等待 outbound 队列接受的最长时间。
	OutboundPushTimeout time.Duration
	// SendRateLimit 是账号级发送窗口内允许的消息条数；<=0 表示关闭发送限流。
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
	// CallSignalingMaxBytes 是 phone.sendSignalingData 单条载荷上限。
	CallSignalingMaxBytes int
	// CallSignalingRate 是单通话每秒信令转发上限（超限静默丢弃）。
	CallSignalingRate int
	// CallExpiryInterval 是通话超时兜底 dispatcher 的轮询间隔。
	CallExpiryInterval time.Duration

	// PremiumGrantMonths 是新注册账号默认赠送的会员月数；0 关闭赠送。
	// 存量账号的一次性赠送由迁移 0094 backfill，不受该配置影响。
	PremiumGrantMonths int

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
	StarGiftExportDelay              time.Duration
	StarGiftTransferDelay            time.Duration
	StarGiftResellDelay              time.Duration
	StarGiftCraftDelay               time.Duration
	StarGiftCraftChancePermille      int

	// GroupCallCheckTTL 是群通话参与者保活水位的过期阈值（客户端 Connecting 态
	// 4s 一跳；M1 起 SFU liveness reporter 同样刷新该水位）。
	GroupCallCheckTTL time.Duration
	// GroupCallSweepInterval 是幽灵参与者 sweeper 的轮询间隔。
	GroupCallSweepInterval time.Duration
	// GroupCallMaxParticipants 是单房间参与者上限（演示规模）。
	GroupCallMaxParticipants int

	// TURNEnable 为 false 时私聊通话不下发中继（退回 P1 的 LAN 直连模式）。
	TURNEnable bool
	// TURNUDPPort 是内嵌 TURN/STUN 的监听端口（独立于 SFU 端口，两者都要独占
	// 消费各自 socket 的 STUN 流量）。Windows 防火墙需放行。
	TURNUDPPort int
	// TURNAdvertiseIP 是写进 phoneConnectionWebrtc 与 relay 分配的客户端可达
	// 地址，默认回落 SFUAdvertiseIP → AdvertiseIP。
	TURNAdvertiseIP string
	// TURNSecret 是 TURN REST 凭据 HMAC 密钥；为空则进程级随机（单实例自洽，
	// 多实例/外部 coturn 必须显式配置同一值）。
	TURNSecret string
	// TURNRelayMinPort/TURNRelayMaxPort 限定 relay 分配端口段（防火墙放行范围）。
	TURNRelayMinPort int
	TURNRelayMaxPort int
	// CallTURNCredentialTTL 是按通话签发的 TURN 凭据有效期。
	CallTURNCredentialTTL time.Duration
	// CallForceRelay 强制 p2p_allowed=false（调试 TURN 中继路径用）。
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

	// SFUEnable 为 false 时群通话只走信令（M0 模式，无媒体）。
	SFUEnable bool
	// SFUUDPPort 是内嵌 SFU 的单 UDP 端口（pion ICE UDPMux）。Windows 防火墙需放行。
	SFUUDPPort int
	// SFUAdvertiseIP 是写进下行 candidate 的客户端可达地址，默认回落 AdvertiseIP。
	// ⚠ 127.0.0.1 会让真机 ICE 永远连不上且无任何 RPC 错误（纯媒体面静默失败）。
	SFUAdvertiseIP string
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

// Load 从环境变量与可选配置文件读取配置并填充默认值。环境变量优先于配置文件。
func Load() (Config, error) {
	fileEnv, err := loadConfigEnv()
	if err != nil {
		return Config{}, err
	}
	envBoolOr := fileEnv.envBoolOr
	envOr := fileEnv.envOr
	envListOr := fileEnv.envListOr
	envIntOr := fileEnv.envIntOr
	envInt64Or := fileEnv.envInt64Or
	envDurationOr := fileEnv.envDurationOr
	envAllowEmptyOr := fileEnv.envAllowEmptyOr

	publicBaseURL, err := links.ValidateBaseURL(envOr("TELESRV_PUBLIC_BASE_URL", links.DefaultPublicBaseURL))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_PUBLIC_BASE_URL: %w", err)
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
	publicAppName, err := links.ValidateAppName(envOr("TELESRV_PUBLIC_APP_NAME", links.DefaultAppName))
	if err != nil {
		return Config{}, fmt.Errorf("TELESRV_PUBLIC_APP_NAME: %w", err)
	}

	cfg := Config{
		ListenAddr:      envOr("TELESRV_LISTEN", "0.0.0.0:2398"),
		WebSocketEnable: envBoolOr("TELESRV_WEBSOCKET_ENABLE", true),
		WebSocketAllowedOrigins: envListOr("TELESRV_WEBSOCKET_ALLOWED_ORIGINS", []string{
			"http://localhost:1234",
			"http://127.0.0.1:1234",
		}),
		// AdvertiseIP 当前不影响 help.getConfig——getConfig 返回空 DCOptions，
		// 客户端使用其写死的 static DC 地址（见 compat/tdesktop/config.go）。
		// 字段与默认值保留，供未来需要显式下发 DC 地址时使用。
		AdvertiseIP:                         envOr("TELESRV_ADVERTISE_IP", "127.0.0.1"),
		RSAKeyPath:                          envOr("TELESRV_RSA_KEY", "data/server_rsa.pem"),
		DC:                                  envIntOr("TELESRV_DC", 2),
		StrictDCCheck:                       envBoolOr("TELESRV_STRICT_DC_CHECK", false),
		MTProtoMaxConnections:               envIntOr("TELESRV_MTPROTO_MAX_CONNECTIONS", 200000),
		MTProtoMaxConnectionsPerIP:          envIntOr("TELESRV_MTPROTO_MAX_CONNECTIONS_PER_IP", 4096),
		MTProtoMaxConcurrentHandshakes:      envIntOr("TELESRV_MTPROTO_MAX_CONCURRENT_HANDSHAKES", 256),
		MTProtoRPCMaxInflight:               envIntOr("TELESRV_MTPROTO_RPC_MAX_INFLIGHT", 32),
		MTProtoRPCQueueSize:                 envIntOr("TELESRV_MTPROTO_RPC_QUEUE_SIZE", 64),
		MTProtoRPCTimeout:                   envDurationOr("TELESRV_MTPROTO_RPC_TIMEOUT", 30*time.Second),
		MTProtoRPCGlobalWorkers:             envIntOr("TELESRV_MTPROTO_RPC_GLOBAL_WORKERS", 256),
		MTProtoRPCGlobalMaxTasks:            envIntOr("TELESRV_MTPROTO_RPC_GLOBAL_MAX_TASKS", 8192),
		MTProtoRPCGlobalMaxBytes:            envInt64Or("TELESRV_MTPROTO_RPC_GLOBAL_MAX_BYTES", 512<<20),
		MTProtoRPCResultCacheMaxEntries:     envIntOr("TELESRV_MTPROTO_RPC_RESULT_CACHE_MAX_ENTRIES", 1<<18),
		MTProtoRPCResultCacheMaxBytes:       envInt64Or("TELESRV_MTPROTO_RPC_RESULT_CACHE_MAX_BYTES", 64<<20),
		MTProtoRPCResultCacheAuthMaxEntries: envIntOr("TELESRV_MTPROTO_RPC_RESULT_CACHE_AUTH_MAX_ENTRIES", 1<<15),
		MTProtoRPCResultCacheAuthMaxBytes:   envInt64Or("TELESRV_MTPROTO_RPC_RESULT_CACHE_AUTH_MAX_BYTES", 32<<20),
		MTProtoRPCResultCacheSessionMaxEntries: envIntOr(
			"TELESRV_MTPROTO_RPC_RESULT_CACHE_SESSION_MAX_ENTRIES", 1<<14,
		),
		MTProtoRPCResultCacheSessionMaxBytes: envInt64Or(
			"TELESRV_MTPROTO_RPC_RESULT_CACHE_SESSION_MAX_BYTES", 16<<20,
		),
		MTProtoRPCResultPendingPerAuth:       envIntOr("TELESRV_MTPROTO_RPC_RESULT_PENDING_PER_AUTH", 1<<11),
		MTProtoInboundFrameGlobalMaxBytes:    envInt64Or("TELESRV_MTPROTO_INBOUND_FRAME_GLOBAL_MAX_BYTES", 512<<20),
		MTProtoOutboundQueueSize:             envIntOr("TELESRV_MTPROTO_OUTBOUND_QUEUE_SIZE", 128),
		MTProtoOutboundControlQueueSize:      envIntOr("TELESRV_MTPROTO_OUTBOUND_CONTROL_QUEUE_SIZE", 32),
		MTProtoOutboundTrackedGlobalMaxBytes: envInt64Or("TELESRV_MTPROTO_OUTBOUND_TRACKED_GLOBAL_MAX_BYTES", 512<<20),
		MTProtoOutboundWriteGlobalMaxBytes:   envInt64Or("TELESRV_MTPROTO_OUTBOUND_WRITE_GLOBAL_MAX_BYTES", 512<<20),
		DebugAddr:                            envAllowEmptyOr("TELESRV_DEBUG_ADDR", "127.0.0.1:6060"),
		BotAPIAddr:                           envAllowEmptyOr("TELESRV_BOT_API_ADDR", ""),
		AdminAPIAddr:                         envAllowEmptyOr("TELESRV_ADMIN_API_ADDR", ""),
		AdminAPIToken:                        envOr("TELESRV_ADMIN_API_TOKEN", ""),
		PublicBaseURL:                        publicBaseURL,
		PublicAppScheme:                      publicAppScheme,
		PublicAppLinkBase:                    publicAppLinkBase,
		PublicWebBaseURL:                     publicWebBaseURL,
		PublicAppName:                        publicAppName,
		PublicLinkWebAddr:                    envAllowEmptyOr("TELESRV_PUBLIC_LINK_WEB_ADDR", ""),
		TelegramLoginEnabled:                 envBoolOr("TELESRV_TELEGRAM_LOGIN_ENABLE", false),
		TelegramLoginIssuer:                  strings.TrimSuffix(envOr("TELESRV_TELEGRAM_LOGIN_ISSUER", publicBaseURL), "/"),
		TelegramLoginAllowHTTP:               envBoolOr("TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP", false),
		TelegramLoginSigningKeysFile:         envOr("TELESRV_TELEGRAM_LOGIN_SIGNING_KEYS_FILE", "data/telegram-login/signing-keys.json"),
		TelegramLoginCodeKeysFile:            envOr("TELESRV_TELEGRAM_LOGIN_CODE_KEYS_FILE", "data/telegram-login/code-keys.json"),
		TelegramLoginSecretPepperFile:        envOr("TELESRV_TELEGRAM_LOGIN_SECRET_PEPPER_FILE", "data/telegram-login/client-secret-pepper"),
		TelegramLoginRequestTTL:              envDurationOr("TELESRV_TELEGRAM_LOGIN_REQUEST_TTL", 5*time.Minute),
		TelegramLoginCodeTTL:                 envDurationOr("TELESRV_TELEGRAM_LOGIN_CODE_TTL", 2*time.Minute),
		TelegramLoginIDTokenTTL:              envDurationOr("TELESRV_TELEGRAM_LOGIN_ID_TOKEN_TTL", time.Hour),
		TelegramLoginTrustedProxyCIDRs:       envListOr("TELESRV_TELEGRAM_LOGIN_TRUSTED_PROXY_CIDRS", nil),
		TelegramLoginRetention:               envDurationOr("TELESRV_TELEGRAM_LOGIN_RETENTION", 7*24*time.Hour),
		TelegramLoginSweepInterval:           envDurationOr("TELESRV_TELEGRAM_LOGIN_SWEEP_INTERVAL", 5*time.Minute),
		TelegramLoginSweepBatch:              envIntOr("TELESRV_TELEGRAM_LOGIN_SWEEP_BATCH", 500),
		AdminUIAddr:                          envOr("TELESRV_ADMIN_UI_ADDR", "127.0.0.1:2600"),
		AdminUIPassword:                      envOr("TELESRV_ADMIN_UI_PASSWORD", ""),
		AdminUIToken:                         envOr("TELESRV_ADMIN_UI_TOKEN", ""),
		AdminSessionKey:                      envOr("TELESRV_ADMIN_SESSION_KEY", ""),

		// 用 127.0.0.1 而非 localhost：localhost 在 Windows 上会先解析到 IPv6 ::1，而 Docker
		// Desktop 的端口转发只在 IPv4 监听，IPv6 连接要等 ~1s 超时才回退 IPv4（实测 localhost
		// 建连 1.0s vs 127.0.0.1 6ms）。冷连接洪峰下池扩容的新连接各等 1s → pre-handler 惊群卡顿。
		// 生产由 TELESRV_POSTGRES_DSN 覆盖；该默认值仅作用于本地开发。
		PostgresDSN:      envOr("TELESRV_POSTGRES_DSN", "postgres://telesrv:telesrv@127.0.0.1:5432/telesrv?sslmode=disable"),
		PostgresMaxConns: envIntOr("TELESRV_POSTGRES_MAX_CONNS", 50),
		PostgresMinConns: envIntOr("TELESRV_POSTGRES_MIN_CONNS", 16),
		RedisAddr:        envOr("TELESRV_REDIS_ADDR", "127.0.0.1:6399"), // 同理避开 localhost→IPv6 回退延迟
		RedisPassword:    envOr("TELESRV_REDIS_PASSWORD", ""),
		RedisDB:          envIntOr("TELESRV_REDIS_DB", 0),

		DevAuthCode:                   envOr("TELESRV_DEV_AUTH_CODE", "12345"),
		AuthCodeTTL:                   envDurationOr("TELESRV_AUTH_CODE_TTL", 5*time.Minute),
		PhoneCodeLength:               envIntOr("TELESRV_PHONE_CODE_LENGTH", 5),
		AuthCodeMaxAttempts:           envIntOr("TELESRV_AUTH_CODE_MAX_ATTEMPTS", 5),
		AuthCodePhoneRateLimit:        envIntOr("TELESRV_AUTH_CODE_PHONE_RATE_LIMIT", 5),
		AuthCodeAuthKeyRateLimit:      envIntOr("TELESRV_AUTH_CODE_AUTH_KEY_RATE_LIMIT", 20),
		AuthCodeRateWindow:            envDurationOr("TELESRV_AUTH_CODE_RATE_WINDOW", 10*time.Minute),
		LoginEmailEnable:              envBoolOr("TELESRV_LOGIN_EMAIL_ENABLE", false),
		LoginEmailRequireSetup:        envBoolOr("TELESRV_LOGIN_EMAIL_REQUIRE_SETUP", false),
		LoginEmailCodeLength:          envIntOr("TELESRV_LOGIN_EMAIL_CODE_LENGTH", 6),
		PhoneCodeDeliveryProvider:     strings.ToLower(strings.TrimSpace(envOr("TELESRV_PHONE_CODE_DELIVERY_PROVIDER", "development"))),
		EmailCodeDeliveryProvider:     strings.ToLower(strings.TrimSpace(envOr("TELESRV_EMAIL_CODE_DELIVERY_PROVIDER", "smtp"))),
		OTPWebhookURL:                 envOr("TELESRV_OTP_WEBHOOK_URL", ""),
		OTPWebhookSecret:              envOr("TELESRV_OTP_WEBHOOK_SECRET", ""),
		OTPWebhookTimeout:             envDurationOr("TELESRV_OTP_WEBHOOK_TIMEOUT", 5*time.Second),
		SMTPHost:                      envOr("TELESRV_SMTP_HOST", ""),
		SMTPPort:                      envIntOr("TELESRV_SMTP_PORT", 587),
		SMTPUsername:                  envOr("TELESRV_SMTP_USERNAME", ""),
		SMTPPassword:                  envOr("TELESRV_SMTP_PASSWORD", ""),
		SMTPFrom:                      envOr("TELESRV_SMTP_FROM", ""),
		SMTPFromName:                  envOr("TELESRV_SMTP_FROM_NAME", "telesrv"),
		SMTPTLSMode:                   strings.ToLower(strings.TrimSpace(envOr("TELESRV_SMTP_TLS", "starttls"))),
		SMTPTimeout:                   envDurationOr("TELESRV_SMTP_TIMEOUT", 10*time.Second),
		LangPackSeedDir:               envOr("TELESRV_LANGPACK_SEED_DIR", "data/langpack"),
		OfficialGiftsDir:              envOr("TELESRV_OFFICIAL_GIFTS_DIR", "data/official-gifts"),
		StarGiftTONStartingGrant:      envInt64Or("TELESRV_STARGIFT_TON_STARTING_GRANT", 10_000_000_000),
		BlobDir:                       envOr("TELESRV_BLOB_DIR", "data/blobs"),
		StickerSeedDir:                envOr("TELESRV_STICKER_SEED_DIR", "data/sticker-seed"),
		StickerSeedMaxSets:            envIntOr("TELESRV_STICKER_SEED_MAX_SETS", 300),
		MapboxToken:                   envOr("TELESRV_MAPBOX_TOKEN", ""),
		MapTileCacheDir:               envOr("TELESRV_MAPTILE_CACHE_DIR", "data/maptiles"),
		ExternalMediaEnable:           envBoolOr("TELESRV_EXTERNAL_MEDIA_ENABLE", true),
		ExternalMediaMaxBytes:         int64(envIntOr("TELESRV_EXTERNAL_MEDIA_MAX_BYTES", 10<<20)),
		ExternalMediaRatePerMin:       envIntOr("TELESRV_EXTERNAL_MEDIA_RATE_PER_MIN", 60),
		WebPagePreviewEnable:          envBoolOr("TELESRV_WEBPAGE_PREVIEW_ENABLE", true),
		WebPagePreviewMaxBytes:        int64(envIntOr("TELESRV_WEBPAGE_PREVIEW_MAX_BYTES", 5<<20)),
		WebPagePreviewRatePerMin:      envIntOr("TELESRV_WEBPAGE_PREVIEW_RATE_PER_MIN", 300),
		BusinessAIProvider:            envOr("TELESRV_BUSINESS_AI_PROVIDER", "echo"),
		AIEnabled:                     envBoolOr("TELESRV_AI_ENABLED", true),
		AIProviders:                   loadAIProviders(fileEnv),
		AITimeout:                     envDurationOr("TELESRV_AI_TIMEOUT", 15*time.Second),
		AIRateLimit:                   envIntOr("TELESRV_AI_RATE_LIMIT", 20),
		AIRateWindow:                  envDurationOr("TELESRV_AI_RATE_WINDOW", time.Minute),
		AIPrivacyLogContent:           envBoolOr("TELESRV_AI_LOG_CONTENT", false),
		TranslationEnabled:            envBoolOr("TELESRV_TRANSLATION_ENABLED", true),
		TranslationProviders:          envListOr("TELESRV_TRANSLATION_PROVIDERS", []string{}),
		TranslationTimeout:            envDurationOr("TELESRV_TRANSLATION_TIMEOUT", 15*time.Second),
		TranslationRateLimit:          envIntOr("TELESRV_TRANSLATION_RATE_LIMIT", 60),
		TranslationRateWindow:         envDurationOr("TELESRV_TRANSLATION_RATE_WINDOW", time.Minute),
		TempKeyResolveCacheMaxEntries: envIntOr("TELESRV_TEMP_KEY_CACHE_MAX_ENTRIES", 262144),
		TempKeyResolveCacheTTL:        envDurationOr("TELESRV_TEMP_KEY_CACHE_TTL", 30*time.Minute),
		ChannelRowCacheMaxEntries:     envIntOr("TELESRV_CHANNEL_ROW_CACHE_MAX", 50000),
		ChannelMemberCacheMaxEntries:  envIntOr("TELESRV_CHANNEL_MEMBER_CACHE_MAX", 100000),
		ChannelDialogCacheMaxEntries:  envIntOr("TELESRV_CHANNEL_DIALOG_CACHE_MAX", 100000),
		ChannelBoostCacheMaxEntries:   envIntOr("TELESRV_CHANNEL_BOOST_CACHE_MAX", 100000),
		ChannelBoostCacheTTL:          envDurationOr("TELESRV_CHANNEL_BOOST_CACHE_TTL", 10*time.Second),

		OutboxWorkers:         envIntOr("TELESRV_OUTBOX_WORKERS", 4),
		OutboxBatch:           envIntOr("TELESRV_OUTBOX_BATCH", 100),
		OutboxInterval:        envDurationOr("TELESRV_OUTBOX_INTERVAL", 200*time.Millisecond),
		OutboxLeaseTimeout:    envDurationOr("TELESRV_OUTBOX_LEASE_TIMEOUT", 30*time.Second),
		OutboxPoisonRetention: envDurationOr("TELESRV_OUTBOX_POISON_RETENTION", time.Minute),
		OutboxPoisonCleanupInterval: envDurationOr(
			"TELESRV_OUTBOX_POISON_CLEANUP_INTERVAL", 15*time.Second,
		),
		OutboundPushTimeout:    envDurationOr("TELESRV_OUTBOUND_PUSH_TIMEOUT", 200*time.Millisecond),
		SendRateLimit:          envIntOr("TELESRV_SEND_RATE_LIMIT", 30),
		SendRateWindow:         envDurationOr("TELESRV_SEND_RATE_WINDOW", time.Minute),
		CatchupRateLimit:       envIntOr("TELESRV_CATCHUP_RATE_LIMIT", 0),
		CatchupRateWindow:      envDurationOr("TELESRV_CATCHUP_RATE_WINDOW", time.Minute),
		ChannelNudgeMaxTargets: envIntOr("TELESRV_CHANNEL_NUDGE_MAX_TARGETS", 0),
		UpdateEventRetention:   envDurationOr("TELESRV_UPDATE_EVENT_RETENTION", 168*time.Hour),
		BotAPIUpdateRetention:  envDurationOr("TELESRV_BOT_API_UPDATE_RETENTION", 24*time.Hour),
		OrphanAuthKeyRetention: envDurationOr("TELESRV_ORPHAN_AUTH_KEY_RETENTION", 24*time.Hour),
		RetentionInterval:      envDurationOr("TELESRV_RETENTION_INTERVAL", time.Hour),
		RetentionBatch:         envIntOr("TELESRV_RETENTION_BATCH", 10000),
		UploadPartTTL:          envDurationOr("TELESRV_UPLOAD_PART_TTL", 24*time.Hour),
		UploadPartGCInterval:   envDurationOr("TELESRV_UPLOAD_PART_GC_INTERVAL", 30*time.Minute),
		UploadPartGCBatch:      envIntOr("TELESRV_UPLOAD_PART_GC_BATCH", 10000),
		UploadInFlightMaxBytes: envInt64Or("TELESRV_UPLOAD_INFLIGHT_MAX_BYTES", 4194304000),
		UploadInFlightMaxParts: envIntOr("TELESRV_UPLOAD_INFLIGHT_MAX_PARTS", 8000),
		UploadInFlightMaxFiles: envIntOr("TELESRV_UPLOAD_INFLIGHT_MAX_FILES", 64),

		CallRingTimeout:       envDurationOr("TELESRV_CALL_RING_TIMEOUT", 90*time.Second),
		CallTombstoneTTL:      envDurationOr("TELESRV_CALL_TOMBSTONE_TTL", 60*time.Second),
		CallMaxActivePerUser:  envIntOr("TELESRV_CALL_MAX_ACTIVE_PER_USER", 4),
		CallSignalingMaxBytes: envIntOr("TELESRV_CALL_SIGNALING_MAX_BYTES", 65536),
		CallSignalingRate:     envIntOr("TELESRV_CALL_SIGNALING_RATE", 50),
		CallExpiryInterval:    envDurationOr("TELESRV_CALL_EXPIRY_INTERVAL", time.Second),

		PremiumGrantMonths:               envIntOr("TELESRV_PREMIUM_GRANT_MONTHS", 3),
		PasskeyRPID:                      envOr("TELESRV_PASSKEY_RP_ID", "telesrv.net"),
		PasskeyAllowedOrigins:            envListOr("TELESRV_PASSKEY_ALLOWED_ORIGINS", nil),
		StarsStartingGrant:               int64(envIntOr("TELESRV_STARS_STARTING_GRANT", 1000)),
		PremiumSweepInterval:             envDurationOr("TELESRV_PREMIUM_SWEEP_INTERVAL", time.Minute),
		PremiumSweepBatch:                envIntOr("TELESRV_PREMIUM_SWEEP_BATCH", 500),
		StarGiftSweepInterval:            envDurationOr("TELESRV_STARGIFT_SWEEP_INTERVAL", 15*time.Second),
		StarGiftSweepBatch:               envIntOr("TELESRV_STARGIFT_SWEEP_BATCH", 1000),
		StarGiftTransferStars:            int64(envIntOr("TELESRV_STARGIFT_TRANSFER_STARS", 25)),
		StarGiftDropOriginalDetailsStars: int64(envIntOr("TELESRV_STARGIFT_DROP_DETAILS_STARS", 25)),
		StarGiftOfferMinStars:            envIntOr("TELESRV_STARGIFT_OFFER_MIN_STARS", 1),
		StarGiftStarsProceedsPermille:    envIntOr("TELESRV_STARGIFT_STARS_PROCEEDS_PERMILLE", 1000),
		StarGiftTONProceedsPermille:      envIntOr("TELESRV_STARGIFT_TON_PROCEEDS_PERMILLE", 1000),
		StarGiftExportDelay:              envDurationOr("TELESRV_STARGIFT_EXPORT_DELAY", 0),
		StarGiftTransferDelay:            envDurationOr("TELESRV_STARGIFT_TRANSFER_DELAY", 0),
		StarGiftResellDelay:              envDurationOr("TELESRV_STARGIFT_RESELL_DELAY", 0),
		StarGiftCraftDelay:               envDurationOr("TELESRV_STARGIFT_CRAFT_DELAY", 0),
		StarGiftCraftChancePermille:      envIntOr("TELESRV_STARGIFT_CRAFT_CHANCE_PERMILLE", 250),

		GroupCallCheckTTL:        envDurationOr("TELESRV_GROUPCALL_CHECK_TTL", 45*time.Second),
		GroupCallSweepInterval:   envDurationOr("TELESRV_GROUPCALL_SWEEP_INTERVAL", 10*time.Second),
		GroupCallMaxParticipants: envIntOr("TELESRV_GROUPCALL_MAX_PARTICIPANTS", 32),

		TURNEnable:            envBoolOr("TELESRV_TURN_ENABLE", true),
		TURNUDPPort:           envIntOr("TELESRV_TURN_UDP_PORT", 12400),
		TURNAdvertiseIP:       envOr("TELESRV_TURN_ADVERTISE_IP", ""),
		TURNSecret:            envOr("TELESRV_TURN_SECRET", ""),
		TURNRelayMinPort:      envIntOr("TELESRV_TURN_RELAY_MIN_PORT", 12500),
		TURNRelayMaxPort:      envIntOr("TELESRV_TURN_RELAY_MAX_PORT", 12999),
		CallTURNCredentialTTL: envDurationOr("TELESRV_CALL_TURN_CREDENTIAL_TTL", 6*time.Hour),
		CallForceRelay:        envBoolOr("TELESRV_CALL_FORCE_RELAY", false),

		SFUEnable:      envBoolOr("TELESRV_SFU_ENABLE", true),
		SFUUDPPort:     envIntOr("TELESRV_SFU_UDP_PORT", 12399),
		SFUAdvertiseIP: envOr("TELESRV_SFU_ADVERTISE_IP", ""),

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
	if err := validateRPCResultCacheConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateStarGiftConfig(cfg); err != nil {
		return Config{}, err
	}
	if err := validateTelegramLoginConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
	if cfg.StarGiftSweepInterval <= 0 || cfg.StarGiftSweepBatch <= 0 || cfg.StarGiftSweepBatch > 10000 {
		return fmt.Errorf("TELESRV_STARGIFT_SWEEP_INTERVAL must be positive and TELESRV_STARGIFT_SWEEP_BATCH must be 1..10000")
	}
	if cfg.StarGiftTONStartingGrant < 0 {
		return fmt.Errorf("TELESRV_STARGIFT_TON_STARTING_GRANT must be non-negative")
	}
	if cfg.StarGiftTransferStars < 0 || cfg.StarGiftDropOriginalDetailsStars < 0 || cfg.StarGiftOfferMinStars < 0 {
		return fmt.Errorf("TELESRV_STARGIFT_TRANSFER_STARS, TELESRV_STARGIFT_DROP_DETAILS_STARS and TELESRV_STARGIFT_OFFER_MIN_STARS must be non-negative")
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

const mtProtoRPCResultMinBytes = int64((1 << 24) - (2 << 10))

func validateRPCResultCacheConfig(cfg Config) error {
	if cfg.MTProtoRPCResultCacheMaxEntries <= 0 || cfg.MTProtoRPCResultCacheAuthMaxEntries <= 0 ||
		cfg.MTProtoRPCResultCacheSessionMaxEntries <= 0 {
		return fmt.Errorf("MTProto rpc_result entry limits must be positive")
	}
	if cfg.MTProtoRPCResultCacheMaxEntries < cfg.MTProtoRPCResultCacheAuthMaxEntries ||
		cfg.MTProtoRPCResultCacheAuthMaxEntries < cfg.MTProtoRPCResultCacheSessionMaxEntries {
		return fmt.Errorf("MTProto rpc_result entry hierarchy must satisfy global >= auth >= session: %d/%d/%d",
			cfg.MTProtoRPCResultCacheMaxEntries, cfg.MTProtoRPCResultCacheAuthMaxEntries, cfg.MTProtoRPCResultCacheSessionMaxEntries)
	}
	if cfg.MTProtoRPCResultCacheMaxBytes < mtProtoRPCResultMinBytes ||
		cfg.MTProtoRPCResultCacheAuthMaxBytes < mtProtoRPCResultMinBytes ||
		cfg.MTProtoRPCResultCacheSessionMaxBytes < mtProtoRPCResultMinBytes {
		return fmt.Errorf("MTProto rpc_result byte limits must each be at least %d: %d/%d/%d",
			mtProtoRPCResultMinBytes, cfg.MTProtoRPCResultCacheMaxBytes,
			cfg.MTProtoRPCResultCacheAuthMaxBytes, cfg.MTProtoRPCResultCacheSessionMaxBytes)
	}
	if cfg.MTProtoRPCResultCacheMaxBytes < cfg.MTProtoRPCResultCacheAuthMaxBytes ||
		cfg.MTProtoRPCResultCacheAuthMaxBytes < cfg.MTProtoRPCResultCacheSessionMaxBytes {
		return fmt.Errorf("MTProto rpc_result byte hierarchy must satisfy global >= auth >= session: %d/%d/%d",
			cfg.MTProtoRPCResultCacheMaxBytes, cfg.MTProtoRPCResultCacheAuthMaxBytes, cfg.MTProtoRPCResultCacheSessionMaxBytes)
	}
	if cfg.MTProtoRPCGlobalMaxTasks <= 0 || cfg.MTProtoRPCResultPendingPerAuth <= 0 ||
		cfg.MTProtoRPCResultPendingPerAuth > cfg.MTProtoRPCGlobalMaxTasks ||
		cfg.MTProtoRPCResultPendingPerAuth > cfg.MTProtoRPCResultCacheAuthMaxEntries {
		return fmt.Errorf("MTProto rpc_result pending-per-auth %d must be positive and <= global pending %d and auth entries %d",
			cfg.MTProtoRPCResultPendingPerAuth, cfg.MTProtoRPCGlobalMaxTasks, cfg.MTProtoRPCResultCacheAuthMaxEntries)
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

type envSource map[string]string

func loadConfigEnv() (envSource, error) {
	path, explicit := os.LookupEnv("TELESRV_CONFIG")
	if !explicit {
		path = defaultConfigFile
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	env, err := readEnvFile(path)
	if err != nil {
		if !explicit && os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("config file %q: %w", path, err)
	}
	return env, nil
}

func readEnvFile(path string) (envSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	values := make(envSource)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNo)
		}
		key = strings.TrimSpace(key)
		if !strings.HasPrefix(key, "TELESRV_") || !validEnvKey(key) {
			return nil, fmt.Errorf("line %d: unsupported key %q; use TELESRV_* keys", lineNo, key)
		}
		value = strings.TrimSpace(value)
		if unquoted, ok := unquoteEnvValue(value); ok {
			value = unquoted
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r == '_' || ('A' <= r && r <= 'Z') || (i > 0 && '0' <= r && r <= '9') {
			continue
		}
		return false
	}
	return true
}

func unquoteEnvValue(value string) (string, bool) {
	if len(value) < 2 {
		return "", false
	}
	quote := value[0]
	if quote != '"' && quote != '\'' {
		return "", false
	}
	if value[len(value)-1] != quote {
		return "", false
	}
	if quote == '\'' {
		return value[1 : len(value)-1], true
	}
	unquoted, err := strconv.Unquote(value)
	if err != nil {
		return value[1 : len(value)-1], true
	}
	return unquoted, true
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
	if v := os.Getenv(key); v != "" {
		return v
	}
	if v := e[key]; v != "" {
		return v
	}
	return def
}

// envAllowEmptyOr is for nullable settings where an explicitly empty process
// environment value must override a non-empty config-file value or default.
func (e envSource) envAllowEmptyOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	if v, ok := e[key]; ok {
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

// envDurationOr 读取 time.ParseDuration 格式（如 "200ms"、"30s"）的时长配置；解析失败回退默认值。
func (e envSource) envDurationOr(key string, def time.Duration) time.Duration {
	if v := e.envOr(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
