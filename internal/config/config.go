// Package config 负责 telesrv 运行配置的加载与校验。
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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
	// StickerWebAddr 是公开 sticker/custom emoji deep link 落地页监听地址；为空关闭。
	// 生产应只监听 loopback，并由 nginx 将 /addstickers/ 与 /addemoji/ 反代到该地址。
	StickerWebAddr string
	// StickerWebPublicURL 是生成 canonical telesrv.net 链接的公开根 URL。
	StickerWebPublicURL string
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
	// TempKeyResolveCacheMaxEntries 是 Router temp→perm 解析缓存容量。
	TempKeyResolveCacheMaxEntries int

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

	// OutboxWorkers 是并发 claim 的 outbox worker 数。默认 1，保证同一用户 pts update
	// 在线投递顺序与持久化顺序一致；后续需要吞吐时应改成按 target_user_id 分片的串行 worker。
	OutboxWorkers int
	// OutboxBatch 是 transactional outbox worker 每次 claim 的最大条数。
	// 调大提升吞吐、增大单批 PG/推送压力；调小降低延迟抖动。配套压测见 docs/message-module.md。
	OutboxBatch int
	// OutboxInterval 是 outbox worker 两次 claim 之间的轮询间隔。
	OutboxInterval time.Duration
	// OutboxLeaseTimeout 是 'dispatching' 行被判定为租约过期、允许其它 worker 重新 claim 的时长。
	// 取值需大于单批投递耗时，否则会重复推送；过大则 worker 崩溃后积压恢复变慢。
	OutboxLeaseTimeout time.Duration
	// OutboundPushTimeout 是 best-effort updates 推送等待 outbound 队列接受的最长时间。
	OutboundPushTimeout time.Duration
	// SendRateLimit 是账号级发送窗口内允许的消息条数；<=0 表示关闭发送限流。
	SendRateLimit int
	// SendRateWindow 是发送限流窗口。
	SendRateWindow time.Duration
	// CatchupRateLimit 是 difference 类 catch-up RPC（getChannelDifference / getPeerDialogs）
	// 每用户每窗口允许的次数；默认开启，防止大群 nudge / 慢客户端恢复时形成补拉风暴；<=0 显式关闭。
	CatchupRateLimit int
	// CatchupRateWindow 是 catch-up 限流窗口。
	CatchupRateWindow time.Duration
	// ChannelNudgeMaxTargets 是一次 fan-out >cap nudge 的目标上限；<=0 用内置默认。
	ChannelNudgeMaxTargets int
	// UpdateEventRetention 是 durable update log 保留期；只清理已被水位/state 覆盖的事件。
	UpdateEventRetention time.Duration
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

	// SFUEnable 为 false 时群通话只走信令（M0 模式，无媒体）。
	SFUEnable bool
	// SFUUDPPort 是内嵌 SFU 的单 UDP 端口（pion ICE UDPMux）。Windows 防火墙需放行。
	SFUUDPPort int
	// SFUAdvertiseIP 是写进下行 candidate 的客户端可达地址，默认回落 AdvertiseIP。
	// ⚠ 127.0.0.1 会让真机 ICE 永远连不上且无任何 RPC 错误（纯媒体面静默失败）。
	SFUAdvertiseIP string
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
		AdvertiseIP:         envOr("TELESRV_ADVERTISE_IP", "127.0.0.1"),
		RSAKeyPath:          envOr("TELESRV_RSA_KEY", "data/server_rsa.pem"),
		DC:                  envIntOr("TELESRV_DC", 2),
		DebugAddr:           envOr("TELESRV_DEBUG_ADDR", "127.0.0.1:6060"),
		BotAPIAddr:          envOr("TELESRV_BOT_API_ADDR", ""),
		AdminAPIAddr:        envOr("TELESRV_ADMIN_API_ADDR", ""),
		AdminAPIToken:       envOr("TELESRV_ADMIN_API_TOKEN", ""),
		StickerWebAddr:      envOr("TELESRV_STICKER_WEB_ADDR", ""),
		StickerWebPublicURL: envOr("TELESRV_STICKER_WEB_PUBLIC_URL", "https://telesrv.net"),
		AdminUIAddr:         envOr("TELESRV_ADMIN_UI_ADDR", "127.0.0.1:2400"),
		AdminUIPassword:     envOr("TELESRV_ADMIN_UI_PASSWORD", ""),
		AdminUIToken:        envOr("TELESRV_ADMIN_UI_TOKEN", ""),
		AdminSessionKey:     envOr("TELESRV_ADMIN_SESSION_KEY", ""),

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
		LangPackSeedDir:               envOr("TELESRV_LANGPACK_SEED_DIR", "data/langpack"),
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
		TempKeyResolveCacheMaxEntries: envIntOr("TELESRV_TEMP_KEY_CACHE_MAX_ENTRIES", 4096),
		ChannelRowCacheMaxEntries:     envIntOr("TELESRV_CHANNEL_ROW_CACHE_MAX", 50000),
		ChannelMemberCacheMaxEntries:  envIntOr("TELESRV_CHANNEL_MEMBER_CACHE_MAX", 100000),
		ChannelDialogCacheMaxEntries:  envIntOr("TELESRV_CHANNEL_DIALOG_CACHE_MAX", 100000),
		ChannelBoostCacheMaxEntries:   envIntOr("TELESRV_CHANNEL_BOOST_CACHE_MAX", 100000),
		ChannelBoostCacheTTL:          envDurationOr("TELESRV_CHANNEL_BOOST_CACHE_TTL", 10*time.Second),

		OutboxWorkers:          envIntOr("TELESRV_OUTBOX_WORKERS", 1),
		OutboxBatch:            envIntOr("TELESRV_OUTBOX_BATCH", 200),
		OutboxInterval:         envDurationOr("TELESRV_OUTBOX_INTERVAL", 50*time.Millisecond),
		OutboxLeaseTimeout:     envDurationOr("TELESRV_OUTBOX_LEASE_TIMEOUT", 30*time.Second),
		OutboundPushTimeout:    envDurationOr("TELESRV_OUTBOUND_PUSH_TIMEOUT", 200*time.Millisecond),
		SendRateLimit:          envIntOr("TELESRV_SEND_RATE_LIMIT", 30),
		SendRateWindow:         envDurationOr("TELESRV_SEND_RATE_WINDOW", time.Minute),
		CatchupRateLimit:       envIntOr("TELESRV_CATCHUP_RATE_LIMIT", 120),
		CatchupRateWindow:      envDurationOr("TELESRV_CATCHUP_RATE_WINDOW", time.Minute),
		ChannelNudgeMaxTargets: envIntOr("TELESRV_CHANNEL_NUDGE_MAX_TARGETS", 0),
		UpdateEventRetention:   envDurationOr("TELESRV_UPDATE_EVENT_RETENTION", 168*time.Hour),
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

		PremiumGrantMonths:    envIntOr("TELESRV_PREMIUM_GRANT_MONTHS", 3),
		PasskeyRPID:           envOr("TELESRV_PASSKEY_RP_ID", "telesrv.net"),
		PasskeyAllowedOrigins: envListOr("TELESRV_PASSKEY_ALLOWED_ORIGINS", nil),
		StarsStartingGrant:    int64(envIntOr("TELESRV_STARS_STARTING_GRANT", 1000)),
		PremiumSweepInterval:  envDurationOr("TELESRV_PREMIUM_SWEEP_INTERVAL", time.Minute),
		PremiumSweepBatch:     envIntOr("TELESRV_PREMIUM_SWEEP_BATCH", 500),

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
	}
	return cfg, nil
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

// envDurationOr 读取 time.ParseDuration 格式（如 "200ms"、"30s"）的时长配置；解析失败回退默认值。
func (e envSource) envDurationOr(key string, def time.Duration) time.Duration {
	if v := e.envOr(key, ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
