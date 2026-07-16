// Command telesrv 是基于 iamxvbaba/td 的 Telegram-like server（第一兼容目标：Telegram Desktop）。
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/exchange"
	"github.com/iamxvbaba/td/tg"

	adminapp "telesrv/internal/admin"
	"telesrv/internal/adminapi"
	"telesrv/internal/app/account"
	aiapp "telesrv/internal/app/ai"
	"telesrv/internal/app/auth"
	botsapp "telesrv/internal/app/bots"
	channelapp "telesrv/internal/app/channels"
	chatlistsapp "telesrv/internal/app/chatlists"
	"telesrv/internal/app/contacts"
	"telesrv/internal/app/dialogs"
	filesapp "telesrv/internal/app/files"
	groupcallsapp "telesrv/internal/app/groupcalls"
	"telesrv/internal/app/help"
	"telesrv/internal/app/langpack"
	"telesrv/internal/app/livestream"
	"telesrv/internal/app/maintenance"
	messageapp "telesrv/internal/app/messages"
	passkeyapp "telesrv/internal/app/passkey"
	phoneapp "telesrv/internal/app/phone"
	pollsapp "telesrv/internal/app/polls"
	privacyapp "telesrv/internal/app/privacy"
	secretchatapp "telesrv/internal/app/secretchat"
	"telesrv/internal/app/stargifts"
	"telesrv/internal/app/stars"
	storiesapp "telesrv/internal/app/stories"
	themesapp "telesrv/internal/app/themes"
	translationapp "telesrv/internal/app/translation"
	"telesrv/internal/app/updates"
	"telesrv/internal/app/userprojection"
	"telesrv/internal/app/users"
	"telesrv/internal/botapi"
	"telesrv/internal/config"
	"telesrv/internal/domain"
	mailpkg "telesrv/internal/mail"
	"telesrv/internal/mtprotoedge"
	"telesrv/internal/rpc"
	"telesrv/internal/seed/catalog"
	"telesrv/internal/sfu"
	storepkg "telesrv/internal/store"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/postgres"
	"telesrv/internal/store/redisstore"
	"telesrv/internal/turnsrv"
	"telesrv/internal/web"
)

func main() {
	logger, err := newLogger()
	if err != nil {
		fmt.Fprintln(os.Stderr, "init logger:", err)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()

	if err := run(logger); err != nil {
		logger.Error("telesrv 退出", zap.Error(err))
		_ = logger.Sync() // os.Exit 跳过 defer；缓冲写需显式 flush 错误日志
		os.Exit(1)
	}
}

// newLogger 构建运行日志器。两项关键改造（相对旧的 zap.NewDevelopment）：
//   - 级别可配（TELESRV_LOG_LEVEL，默认 info）：旧版固定 Debug，热路径 65 处 Debug（含
//     mtprotoedge 每帧一条）在连接洪峰会刷爆日志。生产/压测用 info 即可，需要时设 debug。
//   - 缓冲异步写（BufferedWriteSyncer）：旧版每条日志一次 stderr 同步写 + 全局锁，高并发下
//     在日志锁上串行累积——实测连接时 12 个并发 RPC 的 client_info 阶段被拖成 ~1s 惊群
//     （mutex profile 91% 竞争在 zap 写）。缓冲后写入批量化，锁持有时间从「每条一次系统调用」
//     降到「攒一批刷一次」。FlushInterval 控制日志可见延迟上界。
func newLogger() (*zap.Logger, error) {
	level := zapcore.InfoLevel
	if v := strings.TrimSpace(os.Getenv("TELESRV_LOG_LEVEL")); v != "" {
		if err := level.UnmarshalText([]byte(strings.ToLower(v))); err != nil {
			return nil, fmt.Errorf("parse TELESRV_LOG_LEVEL %q: %w", v, err)
		}
	}
	ws := &zapcore.BufferedWriteSyncer{
		WS:            zapcore.AddSync(os.Stderr),
		FlushInterval: 500 * time.Millisecond,
	}
	core := zapcore.NewCore(zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()), ws, level)
	return zap.New(core,
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
		zap.ErrorOutput(zapcore.AddSync(os.Stderr)),
	), nil
}

func newBusinessAutomationOptions(cfg config.Config, online messageapp.BusinessAutomationOnlineChecker, generator messageapp.BusinessAITextGenerator, logger *zap.Logger) []messageapp.BusinessAutomationOption {
	opts := []messageapp.BusinessAutomationOption{
		messageapp.WithBusinessAutomationOnlineChecker(online),
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.BusinessAIProvider))
	switch provider {
	case "", "echo":
		opts = append(opts, messageapp.WithBusinessAutomationReplyProvider(messageapp.NewEchoBusinessAutomationProvider()))
		logger.Info("Business automation reply provider", zap.String("provider", "echo"))
	case "template", "quick_reply", "quick-reply":
		logger.Info("Business automation reply provider", zap.String("provider", "template"))
	case "ai", "compose_ai", "ai_compose", "aicompose", "kimi":
		if generator == nil {
			logger.Warn("Business automation AI provider requested but AI generator is unavailable", zap.String("provider", cfg.BusinessAIProvider))
			return opts
		}
		opts = append(opts, messageapp.WithBusinessAutomationReplyProvider(messageapp.NewAIBusinessAutomationProvider(generator)))
		logger.Info("Business automation reply provider", zap.String("provider", "ai"))
	default:
		logger.Warn("未知 Business automation AI provider，回退 quick reply 模板", zap.String("provider", cfg.BusinessAIProvider))
	}
	return opts
}

func newAIComposeOptions(cfg config.Config, limiter aiapp.RateLimiter, premium aiapp.PremiumChecker, logger *zap.Logger) []aiapp.Option {
	opts := []aiapp.Option{
		aiapp.WithEnabled(cfg.AIEnabled),
		aiapp.WithTimeout(cfg.AITimeout),
		aiapp.WithRateLimiter(limiter, cfg.AIRateLimit, cfg.AIRateWindow),
		aiapp.WithPremiumChecker(premium),
		aiapp.WithLogger(logger.Named("app").Named("ai")),
		aiapp.WithPrivacyLogContent(cfg.AIPrivacyLogContent),
	}
	providers := make([]aiapp.Provider, 0, len(cfg.AIProviders))
	for _, pc := range cfg.AIProviders {
		provider, err := aiapp.NewProviderFromConfig(aiapp.ProviderConfig{
			Name:            pc.Name,
			Kind:            aiapp.ProviderKind(pc.Kind),
			BaseURL:         pc.BaseURL,
			APIKey:          pc.APIKey,
			Model:           pc.Model,
			Timeout:         cfg.AITimeout,
			MaxOutputTokens: pc.MaxOutputTokens,
			Temperature:     pc.Temperature,
			OmitTemperature: pc.OmitTemperature,
			Thinking:        pc.Thinking,
		})
		if err != nil {
			logger.Warn("AI compose provider 已跳过", zap.String("provider", pc.Name), zap.String("kind", pc.Kind), zap.Error(err))
			continue
		}
		providers = append(providers, provider)
		logger.Info("AI compose provider 已启用", zap.String("provider", provider.Name()), zap.String("kind", pc.Kind))
	}
	if len(providers) > 0 {
		opts = append(opts, aiapp.WithProviders(providers...))
	}
	return opts
}

func newTranslationOptions(cfg config.Config, limiter translationapp.RateLimiter, logger *zap.Logger) []translationapp.Option {
	opts := []translationapp.Option{
		translationapp.WithEnabled(cfg.TranslationEnabled),
		translationapp.WithTimeout(cfg.TranslationTimeout),
		translationapp.WithRateLimiter(limiter, cfg.TranslationRateLimit, cfg.TranslationRateWindow),
	}
	selected := make(map[string]struct{}, len(cfg.TranslationProviders))
	for _, name := range cfg.TranslationProviders {
		selected[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	providers := make([]translationapp.Provider, 0, len(cfg.AIProviders))
	for _, pc := range cfg.AIProviders {
		if aiapp.ProviderKind(pc.Kind) == aiapp.ProviderKindLocal {
			continue
		}
		if len(selected) > 0 {
			if _, ok := selected[strings.ToLower(pc.Name)]; !ok {
				continue
			}
		}
		provider, err := aiapp.NewProviderFromConfig(aiapp.ProviderConfig{
			Name: pc.Name, Kind: aiapp.ProviderKind(pc.Kind), BaseURL: pc.BaseURL,
			APIKey: pc.APIKey, Model: pc.Model, Timeout: cfg.TranslationTimeout,
			MaxOutputTokens: max(pc.MaxOutputTokens, 8192), Temperature: pc.Temperature,
			OmitTemperature: pc.OmitTemperature, Thinking: pc.Thinking,
		})
		if err != nil {
			logger.Warn("translation provider 已跳过", zap.String("provider", pc.Name), zap.Error(err))
			continue
		}
		providers = append(providers, translationapp.NewAIProvider(provider))
		logger.Info("translation provider 已启用", zap.String("provider", provider.Name()), zap.String("kind", pc.Kind))
	}
	if len(providers) > 0 {
		opts = append(opts, translationapp.WithProviders(providers...))
	} else if cfg.TranslationEnabled {
		logger.Warn("translation 已启用但没有远程 provider；messages.translateText 将返回 TRANSLATIONS_DISABLED")
	}
	return opts
}

// startDebugServer 在 addr 上挂起 net/http/pprof 调试端点（addr 为空则关闭）。
// 用独立 mux（不污染 http.DefaultServeMux），仅注册 pprof 路由：
//   - /debug/pprof/profile  CPU 剖析（?seconds=30）
//   - /debug/pprof/heap     堆内存快照
//   - /debug/pprof/goroutine goroutine 栈（排查泄漏/阻塞）
//   - /debug/pprof/mutex    锁竞争（需 SetMutexProfileFraction）
//   - /debug/pprof/block    阻塞剖析（需 SetBlockProfileRate）
//   - /debug/pprof/allocs   累计分配（带宽/序列化热点常与之相关）
//
// mutex/block 采样在低流量测试环境开销可忽略；高流量生产如担心扰动，置空 DebugAddr 关闭整端点。
func startDebugServer(ctx context.Context, addr string, logger *zap.Logger) {
	if addr == "" {
		return
	}
	runtime.SetMutexProfileFraction(5) // 采样 1/5 的锁竞争事件
	runtime.SetBlockProfileRate(10000) // 每阻塞约 10µs 采一次样

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index) // 含 heap/goroutine/mutex/block/allocs 等命名 profile
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		logger.Info("pprof 调试端点已启用", zap.String("addr", addr),
			zap.String("hint", "go tool pprof http://"+addr+"/debug/pprof/profile?seconds=30"))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warn("pprof 端点退出", zap.Error(err))
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}

// externalMediaOption 按配置启用外链媒体抓取；禁用时返回 nil（NewService 跳过 nil option）。
// liveStreamDep 把可能为 nil 的 *livestream.Service 转成 rpc.LiveStreamsService，
// 避免 typed-nil interface（nil 具体指针装进接口后 != nil 的坑）。
func liveStreamDep(s *livestream.Service) rpc.LiveStreamsService {
	if s == nil {
		return nil
	}
	return s
}

func externalMediaOption(cfg config.Config) filesapp.Option {
	if !cfg.ExternalMediaEnable {
		return nil
	}
	return filesapp.WithExternalMedia(cfg.ExternalMediaMaxBytes, cfg.ExternalMediaRatePerMin)
}

// webPagePreviewOption 按配置启用链接预览抓取；禁用时返回 nil（NewService 跳过 nil option）。
func webPagePreviewOption(cfg config.Config) filesapp.Option {
	if !cfg.WebPagePreviewEnable {
		return nil
	}
	return filesapp.WithWebPagePreview(cfg.WebPagePreviewMaxBytes, cfg.WebPagePreviewRatePerMin)
}

func run(logger *zap.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	buildMeta := currentBuildMetadata()

	rsaKey, err := mtprotoedge.LoadOrGenerateRSAKey(cfg.RSAKeyPath)
	if err != nil {
		return fmt.Errorf("server rsa key: %w", err)
	}
	fingerprint := exchange.PrivateKey{RSA: rsaKey}.Fingerprint()

	_, portStr, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("parse listen addr %q: %w", cfg.ListenAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("parse listen port %q: %w", portStr, err)
	}

	// tg.Layer 由当前导入的 canonical schema 生成；纳入未来 Layer 后无需
	// 在 telesrv 另维护一份常量。
	logger.Info("telesrv 启动",
		zap.String("listen", cfg.ListenAddr),
		zap.Int("dc", cfg.DC),
		zap.String("advertise", net.JoinHostPort(cfg.AdvertiseIP, portStr)),
		zap.Int("tl_layer", tg.Layer),
		zap.String("git_commit", buildMeta.Commit),
		zap.String("git_branch", buildMeta.Branch),
		zap.String("git_tree_state", buildMeta.TreeState),
		zap.String("build_time", buildMeta.BuildTime),
		zap.String("go_version", buildMeta.GoVersion),
		zap.String("rsa_key", cfg.RSAKeyPath),
		zap.Int64("rsa_fingerprint", fingerprint),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// pprof 调试端点：telesrv 是宿主进程（不在 docker 内，docker stats 看不到它），CPU/内存/
	// goroutine/锁竞争的定位全靠此端点。早于重负载初始化启动，连 seed/预热阶段也可剖析。
	startDebugServer(ctx, cfg.DebugAddr, logger)

	// 持久化依赖：先迁移 schema，再建立连接。auth key 与业务事实落 PostgreSQL，
	// Redis 只承载可重建的短 TTL 状态、缓存、计数器和限流。
	// 依赖由 deploy/docker-compose.yml 启动；连不上则启动失败（开发期须先 docker compose up）。
	migrationStatus, err := postgres.MigrateAndStatus(cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("postgres migrate: %w", err)
	}
	logger.Info("PostgreSQL schema 已迁移",
		zap.Uint("schema_version", migrationStatus.Version),
		zap.Bool("schema_dirty", migrationStatus.Dirty),
		zap.Bool("schema_empty", migrationStatus.Empty),
	)
	pool, err := postgres.Open(ctx, cfg.PostgresDSN,
		postgres.WithMaxConns(cfg.PostgresMaxConns),
		postgres.WithMinConns(cfg.PostgresMinConns),
	)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	rdb, err := redisstore.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() { _ = rdb.Close() }()
	logger.Info("持久化依赖就绪", zap.String("redis", cfg.RedisAddr))

	authKeyStore := postgres.NewAuthKeyStore(pool)
	userStore := postgres.NewUserStore(pool)
	authzStore := postgres.NewAuthorizationStore(pool)
	adminStore := postgres.NewAdminStore(pool)
	updateStateStore := postgres.NewUpdateStateStore(pool)
	updateEventStore := postgres.NewUpdateEventStore(pool, postgres.WithUpdateEventLogger(logger.Named("store").Named("updates")))
	phoneChangeStore := postgres.NewPhoneChangeStore(pool)
	readModelVersionStore := storepkg.NewCachedReadModelVersionStore(postgres.NewReadModelVersionStore(pool), 0, 0)
	dispatchOutboxStore := postgres.NewDispatchOutboxStore(pool, postgres.WithLeaseTimeout(cfg.OutboxLeaseTimeout))
	bootstrapUpdateStore := postgres.NewBootstrapUpdateJobStore(pool)
	botAPIUpdateStore := postgres.NewBotAPIUpdateStore(pool)
	boxIDAllocator := redisstore.NewBoxIDAllocator(rdb, postgres.NewMessageBoxCounterSource(pool))
	channelIDAllocator := redisstore.NewChannelIDAllocator(rdb, postgres.NewChannelIDCounterSource(pool))
	channelMessageIDAllocator := redisstore.NewChannelMessageIDAllocator(rdb, postgres.NewChannelMessageIDCounterSource(pool))
	secretChatIDAllocator := redisstore.NewSecretChatIDAllocator(rdb, postgres.NewSecretChatIDCounterSource(pool))
	contactStore := userprojection.NewCachedContactStore(postgres.NewContactStore(pool), 0)
	dialogStore := postgres.NewDialogStore(pool)
	chatlistStore := postgres.NewChatlistStore(pool)
	messageStore := postgres.NewMessageStore(pool,
		postgres.WithMessageAllocators(boxIDAllocator),
		postgres.WithMessageLogger(logger.Named("store").Named("messages")))
	// 共享频道行/成员缓存 + 统一 read-model LISTEN/NOTIFY 实时失效：消除高频「逐 RPC
	// 解析频道/成员」在客户端重连同步突发里重复读同一行的放大。
	channelRowCache := postgres.NewChannelRowCache(cfg.ChannelRowCacheMaxEntries)
	channelMemberCache := postgres.NewChannelMemberCache(cfg.ChannelMemberCacheMaxEntries)
	channelDialogCache := postgres.NewChannelDialogCache(cfg.ChannelDialogCacheMaxEntries)
	channelBoostCache := postgres.NewChannelBoostCache(cfg.ChannelBoostCacheMaxEntries, cfg.ChannelBoostCacheTTL)
	channelStore := postgres.NewChannelStore(pool,
		postgres.WithChannelAllocators(channelIDAllocator, channelMessageIDAllocator),
		postgres.WithChannelLogger(logger.Named("store").Named("channels")),
		postgres.WithChannelRowCache(channelRowCache),
		postgres.WithChannelMemberCache(channelMemberCache),
		postgres.WithChannelDialogCache(channelDialogCache),
		postgres.WithChannelBoostCache(channelBoostCache))
	pollStore := postgres.NewPollStore(pool)
	mediaStore := postgres.NewMediaStore(pool)
	// 头像投影缓存：所有 projector 共用一层短 TTL owner→头像缓存，消除高频「返回用户」RPC
	// 每次投影对每批 owner 固定 2 次的 CurrentProfilePhotosKind PG 查询。
	cachedPhotos := userprojection.NewCachedPhotoProvider(mediaStore, userprojection.DefaultPhotoCacheTTL)
	privacyStore := privacyapp.NewCachedPrivacyStore(postgres.NewPrivacyStore(pool), 0)
	storyStore := postgres.NewStoryStore(pool)
	blobBackend, err := filesapp.NewLocalFS(cfg.BlobDir)
	if err != nil {
		return fmt.Errorf("init blob backend: %w", err)
	}
	logger.Info("blob backend 就绪",
		zap.String("backend", "localfs"),
		zap.String("dir", cfg.BlobDir),
	)
	filesService := filesapp.NewService(mediaStore, blobBackend, cfg.DC,
		filesapp.WithLogger(logger),
		filesapp.WithUploadPartQuota(domain.UploadPartQuota{
			MaxBytes: cfg.UploadInFlightMaxBytes,
			MaxParts: cfg.UploadInFlightMaxParts,
			MaxFiles: cfg.UploadInFlightMaxFiles,
		}),
		filesapp.WithMapboxMapTiles(cfg.MapboxToken, cfg.MapTileCacheDir),
		externalMediaOption(cfg),
		webPagePreviewOption(cfg),
	)
	if cfg.MapboxToken != "" {
		logger.Info("地图缩略图代理已启用", zap.String("provider", "mapbox"), zap.String("cache_dir", cfg.MapTileCacheDir))
	}
	if cfg.ExternalMediaEnable {
		logger.Info("外链媒体抓取已启用", zap.Int64("max_bytes", cfg.ExternalMediaMaxBytes), zap.Int("rate_per_min", cfg.ExternalMediaRatePerMin))
	}
	if cfg.WebPagePreviewEnable {
		logger.Info("链接预览抓取已启用", zap.Int64("max_bytes", cfg.WebPagePreviewMaxBytes), zap.Int("rate_per_min", cfg.WebPagePreviewRatePerMin))
	}
	if stats, err := filesService.SeedMedia(ctx, cfg.StickerSeedDir, cfg.StickerSeedMaxSets); err != nil {
		return fmt.Errorf("seed media: %w", err)
	} else if !stats.Skipped {
		logger.Info("媒体种子导入完成",
			zap.String("dir", cfg.StickerSeedDir),
			zap.Int("reactions", stats.Reactions),
			zap.Int("sticker_sets", stats.StickerSets),
			zap.Int("effects", stats.Effects),
			zap.Int("documents", stats.Documents),
			zap.Int("blobs", stats.Blobs),
		)
	}
	if stats, err := filesService.SeedAppearance(ctx); err != nil {
		return fmt.Errorf("seed appearance: %w", err)
	} else if !stats.Skipped {
		logger.Info("外观种子导入完成",
			zap.String("source", "default-seed"),
			zap.Int("wallpapers", stats.Wallpapers),
			zap.Int("documents", stats.Documents),
			zap.Int("blobs", stats.Blobs),
		)
	}
	if stats, err := filesService.WarmCaches(ctx); err != nil {
		logger.Warn("媒体资源缓存预热失败", zap.Error(err))
	} else if stats.StickerSets > 0 || stats.Documents > 0 || stats.Blobs > 0 {
		logger.Info("媒体资源缓存预热完成",
			zap.Int("sticker_sets", stats.StickerSets),
			zap.Int("documents", stats.Documents),
			zap.Int("blobs", stats.Blobs),
		)
	}
	// 默认 emoji status 系统集：从 animated_emoji 精选合成（幂等，已 seed 的存量
	// 库重启后自动补上）；缺失时 premium 用户的 status 选择器会是空的。
	if count, created, err := filesService.EnsureDefaultEmojiStatusSet(ctx); err != nil {
		logger.Warn("默认 emoji status 系统集合成失败", zap.Error(err))
	} else if created {
		logger.Info("默认 emoji status 系统集已合成", zap.Int("documents", count))
	}
	langPackStore := postgres.NewLangPackStore(pool)
	passwordStore := postgres.NewPasswordStore(pool)
	helpStore := postgres.NewHelpStore(pool)
	aiComposeStore := postgres.NewAIComposeStore(pool)
	tempAuthKeyStore := postgres.NewTempAuthKeyBindingStore(pool)
	inlineRegistryStore := redisstore.NewInlineRegistryStore(rdb)
	codeStore := redisstore.NewCodeStore(rdb)
	rateLimiter := redisstore.NewRateLimiter(rdb)
	activeSessions := mtprotoedge.NewSessionManager(logger.Named("mtprotoedge").Named("sessions"))
	adminService := adminapp.NewService(adminapp.Dependencies{
		Commands:     adminStore,
		Restrictions: adminStore,
	})
	go maintenance.NewRetentionWorker(dispatchOutboxStore, tempAuthKeyStore, logger.Named("maintenance").Named("retention"),
		cfg.UpdateEventRetention,
		cfg.RetentionInterval,
		cfg.RetentionBatch,
	).WithDispatchOutboxPoisonPolicy(cfg.OutboxPoisonRetention, cfg.OutboxPoisonCleanupInterval).
		WithBotAPIUpdateRetention(botAPIUpdateStore, cfg.BotAPIUpdateRetention).
		WithAuthKeySessionLayerRetention(authKeyStore).
		WithLoginCodeDeliveryRetention(messageStore).
		WithUserUpdateRetention(updateEventStore).
		WithChannelUpdateRetention(channelStore).
		WithOrphanAuthKeyRetention(authKeyStore, activeSessions, cfg.OrphanAuthKeyRetention).
		Run(ctx)
	go filesapp.NewUploadPartGCWorker(filesService, logger.Named("files").Named("upload_gc"),
		cfg.UploadPartTTL,
		cfg.UploadPartGCInterval,
		cfg.UploadPartGCBatch,
	).Run(ctx)
	langPackService := langpack.NewService(langPackStore)
	privacyService := privacyapp.NewService(privacyStore, contactStore)
	contactsService := contacts.NewService(contactStore, userStore).Configure(
		contacts.WithPhotoProvider(cachedPhotos),
		contacts.WithPrivacyEvaluator(privacyService),
		contacts.WithReadModelVersions(readModelVersionStore),
	)
	if seeded, err := langPackService.SeedDirectory(ctx, cfg.LangPackSeedDir); err != nil {
		return fmt.Errorf("seed langpack: %w", err)
	} else if seeded > 0 {
		logger.Info("语言包种子导入完成", zap.String("dir", cfg.LangPackSeedDir), zap.Int("strings", seeded))
	}
	// 国家区号目录:把 catalog 固化的官方全量(~235 国)幂等 upsert 进 PG,覆盖迁移里仅
	// seed 的 2 国(US/CN)默认值。否则 countries 表非空,ListCountries 返回那 2 行就会
	// 绕过 catalog,登录页/号码格式只显示 2 国。upsert 失败仅告警不阻断启动(回退旧 2 行)。
	if cs := catalog.Countries().Countries; len(cs) > 0 {
		if err := helpStore.UpsertCountries(ctx, cs); err != nil {
			logger.Warn("国家区号种子导入失败", zap.Error(err))
		} else {
			logger.Info("国家区号种子导入完成", zap.Int("countries", len(cs)))
		}
	}

	botStore := postgres.NewBotStore(pool)
	// userCache 与 users 服务共享同一实例：bot 元数据写入（version bump）后必须
	// 失效缓存，否则 TTL 内 getUsers 回旧 first_name/旧 bot_info_version。
	userCache := redisstore.NewUserCache(rdb, redisstore.DefaultUserCacheTTL)
	accountOptions := []account.ServiceOption{
		account.WithReactionSettings(passwordStore),
		account.WithAccountSettings(passwordStore),
		account.WithNotifySettings(passwordStore),
		account.WithStickerCollections(passwordStore),
		account.WithUserStickerSets(passwordStore),
		account.WithSavedMusic(passwordStore),
		account.WithBusinessAutomation(passwordStore),
		account.WithUsers(userStore),
		account.WithPhoneChange(phoneChangeStore, authzStore, codeStore, userCache, cfg.DevAuthCode, cfg.AuthCodeTTL, cfg.AuthCodeMaxAttempts),
		account.WithPublicBaseURL(cfg.PublicBaseURL),
	}
	var loginEmailSender mailpkg.Sender
	if cfg.LoginEmailEnable {
		loginEmailSender = mailpkg.NewSMTP(mailpkg.Config{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			From:     cfg.SMTPFrom,
			FromName: cfg.SMTPFromName,
			TLSMode:  cfg.SMTPTLSMode,
			Timeout:  cfg.SMTPTimeout,
		})
		accountOptions = append(accountOptions,
			account.WithLoginEmailVerification(codeStore, loginEmailSender, cfg.AuthCodeTTL, cfg.AuthCodeMaxAttempts, cfg.LoginEmailCodeLength))
	}
	accountService := account.NewService(passwordStore, accountOptions...)
	botsService := botsapp.NewService(userStore, botStore, messageStore,
		botsapp.WithLogger(logger.Named("bots")),
		botsapp.WithBlockChecker(contactStore),
		botsapp.WithPublicChannelUsernameResolver(channelStore),
		botsapp.WithUserCache(userCache),
		botsapp.WithStickerSetCreator(filesService),
		botsapp.WithUserStickerSets(accountService),
		botsapp.WithPublicBaseURL(cfg.PublicBaseURL))
	groupCallStore := postgres.NewGroupCallStore(pool)
	groupCallsService := groupcallsapp.NewService(groupCallStore, groupcallsapp.WithPublicBaseURL(cfg.PublicBaseURL))
	// 群通话媒体面：内嵌 pion SFU（M1+）。SFU 的 liveness reporter 把媒体面存活
	// 回报给信令侧保活水位（sweeper 双过期判据的实现）；未启用则退化为纯信令（M0）。
	sfuService := sfu.Service(sfu.Disabled())
	if cfg.SFUEnable {
		sfuAdvertise := cfg.SFUAdvertiseIP
		if sfuAdvertise == "" {
			sfuAdvertise = cfg.AdvertiseIP
		}
		pionSFU, err := sfu.NewPion(sfu.PionConfig{
			UDPPort:     cfg.SFUUDPPort,
			AdvertiseIP: sfuAdvertise,
			Logger:      logger.Named("sfu"),
			Touch: func(callID, userID int64) {
				if _, _, err := groupCallsService.Touch(context.Background(), callID, userID, int(time.Now().Unix())); err != nil {
					logger.Debug("sfu liveness touch", zap.Int64("call_id", callID), zap.Int64("user_id", userID), zap.Error(err))
				}
			},
		})
		if err != nil {
			return fmt.Errorf("init sfu: %w", err)
		}
		sfuService = pionSFU
	}
	// 频道 RTMP 直播媒体面（Live Stream）：内嵌 RTMP ingest（OBS 推流）+ ffmpeg
	// 切段。未启用时信令仍可用，观众停留在"等待推流"占位。
	var liveStreamService *livestream.Service
	if cfg.LiveStreamEnable {
		liveStreamService = livestream.NewService(livestream.Config{
			ListenAddr:  cfg.LiveStreamRtmpAddr,
			FFmpegPath:  cfg.LiveStreamFFmpegPath,
			WorkDir:     cfg.LiveStreamWorkDir,
			SegmentKeep: cfg.LiveStreamSegmentKeep,
		}, groupCallsService, logger.Named("livestream"))
		if err := liveStreamService.Start(); err != nil {
			return fmt.Errorf("init live stream: %w", err)
		}
		defer liveStreamService.Close()
	}
	// 私聊通话中继（P3）：内嵌 TURN/STUN，phoneCall.connections 经 phoneConnectionWebrtc
	// 下发。未启用时退回 P1 的纯信令 LAN 直连。
	turnService := turnsrv.Service(turnsrv.Disabled())
	if cfg.TURNEnable {
		turnAdvertise := cfg.TURNAdvertiseIP
		if turnAdvertise == "" {
			turnAdvertise = cfg.SFUAdvertiseIP
		}
		if turnAdvertise == "" {
			turnAdvertise = cfg.AdvertiseIP
		}
		t, err := turnsrv.New(turnsrv.Config{
			UDPPort:       cfg.TURNUDPPort,
			AdvertiseIP:   turnAdvertise,
			SharedSecret:  cfg.TURNSecret,
			RelayMinPort:  cfg.TURNRelayMinPort,
			RelayMaxPort:  cfg.TURNRelayMaxPort,
			CredentialTTL: cfg.CallTURNCredentialTTL,
			Logger:        logger.Named("turn"),
		})
		if err != nil {
			return fmt.Errorf("init turn: %w", err)
		}
		defer t.Close()
		turnService = t
	}
	// 服务端重启恢复：SFU 状态全失，把全部活跃通话的参与者批量置 left（version++），
	// 客户端经 checkGroupCall 发现自己 ssrc 消失后自动 rejoin。
	if calls, err := groupCallsService.ResetAllParticipants(ctx, int(time.Now().Unix())); err != nil {
		logger.Warn("重启清理群通话参与者失败", zap.Error(err))
	} else if len(calls) > 0 {
		logger.Info("重启清理群通话参与者", zap.Int("calls", len(calls)))
	}
	phoneService := phoneapp.NewService(phoneapp.Config{
		RingTimeout:            cfg.CallRingTimeout,
		TombstoneTTL:           cfg.CallTombstoneTTL,
		MaxActivePerUser:       cfg.CallMaxActivePerUser,
		SignalingRatePerSecond: cfg.CallSignalingRate,
	})
	// 私聊端对端加密（Secret Chat）握手状态机 + qts 投递队列（盲中继）。
	secretChatStore := postgres.NewSecretChatStore(pool)
	encryptedQueueStore := postgres.NewEncryptedQueueStore(pool)
	secretChatService := secretchatapp.NewService(secretChatStore, encryptedQueueStore, secretChatIDAllocator)
	starsStore := postgres.NewStarsStore(pool)
	starsService := stars.NewService(starsStore, stars.WithStartingGrant(cfg.StarsStartingGrant))
	starGiftStore := postgres.NewStarGiftStore(pool)
	starGiftUpgradeStore := postgres.NewStarGiftUpgradeStore(pool, messageStore)
	giftsService := stargifts.NewService(starGiftStore, blobBackend, cfg.DC,
		stargifts.WithUpgradeStore(starGiftUpgradeStore))
	// Passkey:凭据持久化走 postgres;一次性挑战走进程内内存(短 TTL,与 QR 登录 token
	// 同属进程内一次性凭据,不跨实例)。
	passkeyStore := postgres.NewPasskeyStore(pool)
	passkeyChallengeStore := memory.NewPasskeyChallengeStore()
	passkeyService := passkeyapp.NewService(passkeyStore, passkeyChallengeStore, cfg.PasskeyRPID, cfg.DC,
		passkeyapp.WithAllowedOrigins(cfg.PasskeyAllowedOrigins))
	// 自定义云主题(Create a New Theme):主题目录与每用户已安装列表均持久化到 postgres。
	themeService := themesapp.NewService(postgres.NewThemeStore(pool))
	usersService := users.NewService(userStore, users.WithBaseUserCache(userCache), users.WithContactStore(contactStore), users.WithPhotoProvider(cachedPhotos), users.WithPrivacyEvaluator(privacyService))
	aiComposeService := aiapp.NewService(aiComposeStore, newAIComposeOptions(cfg, rateLimiter, usersService.PremiumActive, logger)...)
	botsService.SetAIChatGenerator(aiComposeService)
	dialogsService := dialogs.NewService(dialogStore, channelStore).Configure(
		dialogs.WithContactStore(contactStore),
		dialogs.WithPhotoProvider(cachedPhotos),
		dialogs.WithPrivacyEvaluator(privacyService),
		dialogs.WithPremiumChecker(usersService.PremiumActive),
		dialogs.WithReadModelVersions(readModelVersionStore),
	)
	// 编译期保证 *users.Service 满足 channel fan-out 跨 viewer 投影预热的可选能力；签名漂移会在
	// 这里立刻断编译，而非在运行时静默退化回 O(viewer) 逐 viewer 投影。
	var _ rpc.BatchViewerUsersResolver = usersService
	channelsService := channelapp.NewService(channelStore,
		channelapp.WithBotProfileResolver(botsService),
		channelapp.WithReadModelVersions(readModelVersionStore),
		channelapp.WithSendPermissionChecker(adminService),
	)
	chatlistsService := chatlistsapp.NewService(
		chatlistStore,
		dialogStore,
		chatlistsapp.WithChannels(channelsService),
		chatlistsapp.WithPremiumChecker(usersService.PremiumActive),
	)
	businessAutomationOptions := newBusinessAutomationOptions(cfg, activeSessions, aiComposeService, logger)
	messagesService := messageapp.NewService(messageStore, dialogStore,
		messageapp.WithContactStore(contactStore),
		messageapp.WithPhotoProvider(cachedPhotos),
		messageapp.WithPrivacyEvaluator(privacyService),
		messageapp.WithReadModelVersions(readModelVersionStore),
		messageapp.WithBotResponder(botsService),
		messageapp.WithSendPermissionChecker(adminService),
		messageapp.WithBusinessAutomation(passwordStore, businessAutomationOptions...),
	)
	translationService := translationapp.NewService(
		messagesService,
		channelsService,
		dialogStore,
		newTranslationOptions(cfg, rateLimiter, logger)...,
	)
	authService := auth.NewService(userStore, authzStore, codeStore, authKeyStore, tempAuthKeyStore, cfg.DevAuthCode,
		auth.WithLoginMessages(messageStore, dialogStore),
		auth.WithLoginCodeDelivery(messageStore),
		auth.WithPasswords(passwordStore),
		auth.WithBotLogin(botStore),
		auth.WithPremiumGrant(cfg.PremiumGrantMonths),
		auth.WithCodeTTL(cfg.AuthCodeTTL),
		auth.WithCodeMaxAttempts(cfg.AuthCodeMaxAttempts),
		auth.WithLoginEmail(auth.LoginEmailOptions{
			Enabled:      cfg.LoginEmailEnable,
			RequireSetup: cfg.LoginEmailRequireSetup,
			CodeLength:   cfg.LoginEmailCodeLength,
			Store:        accountService,
			Sender:       loginEmailSender,
		}))
	updatesService := updates.NewService(updateStateStore, updateEventStore, updates.WithLogger(logger.Named("app").Named("updates")))
	router := rpc.New(rpc.Config{
		DC:                       cfg.DC,
		IP:                       cfg.AdvertiseIP,
		Port:                     port,
		OutboundPushTimeout:      cfg.OutboundPushTimeout,
		SendRateLimit:            cfg.SendRateLimit,
		SendRateWindow:           cfg.SendRateWindow,
		AuthCodePhoneRateLimit:   cfg.AuthCodePhoneRateLimit,
		AuthCodeAuthKeyRateLimit: cfg.AuthCodeAuthKeyRateLimit,
		AuthCodeRateWindow:       cfg.AuthCodeRateWindow,
		CatchupRateLimit:         cfg.CatchupRateLimit,
		CatchupRateWindow:        cfg.CatchupRateWindow,
		ChannelNudgeMaxTargets:   cfg.ChannelNudgeMaxTargets,
		CallSignalingMaxBytes:    cfg.CallSignalingMaxBytes,
		CallForceRelay:           cfg.CallForceRelay,
		GroupCallMaxParticipants: cfg.GroupCallMaxParticipants,
		RtmpIngestURL:            cfg.LiveStreamRtmpURL,
		PublicBaseURL:            cfg.PublicBaseURL,
		// PFS temp→perm 解析缓存：显式撤销会清缓存并断开连接，re-bind 即时失效；
		// 配置 TTL 只承担跨进程/异常失效兜底，避免大连接数周期性打满 PG。
		TempKeyResolveCacheTTL:        cfg.TempKeyResolveCacheTTL,
		TempKeyResolveCacheMaxEntries: cfg.TempKeyResolveCacheMaxEntries,
	}, rpc.Deps{
		Auth:                 authService,
		AuthKeySessionLayers: authKeyStore,
		Account:              accountService,
		Privacy:              privacyService,
		Help:                 help.NewService(helpStore, helpStore, help.WithMapboxToken(cfg.MapboxToken), help.WithAccountFreezeProvider(adminService)),
		AccountFreeze:        adminService,
		AICompose:            aiComposeService,
		Users:                usersService,
		Updates:              updatesService,
		BootstrapUpdates:     bootstrapUpdateStore,
		BotAPIUpdates:        botAPIUpdateStore,
		Contacts:             contactsService,
		Dialogs:              dialogsService,
		Chatlists:            chatlistsService,
		Messages:             messagesService,
		Translation:          translationService,
		Channels:             channelsService,
		Files:                filesService,
		Bots:                 botsService,
		Polls:                pollsapp.NewService(pollStore),
		Stories:              storiesapp.NewService(storyStore, storiesapp.WithChannelStoryAccess(channelsService)),
		Phone:                phoneService,
		SecretChats:          secretChatService,
		Stars:                starsService,
		Gifts:                giftsService,
		Passkey:              passkeyService,
		Themes:               themeService,
		GroupCalls:           groupCallsService,
		LiveStreams:          liveStreamDep(liveStreamService),
		SFU:                  sfuService,
		TURN:                 turnService,
		LangPack:             langPackService,
		Sessions:             activeSessions,
		Inline:               inlineRegistryStore,
		Limiter:              rateLimiter,
	}, logger.Named("rpc"), clock.System)
	readModelListener := postgres.NewReadModelChangeListener(cfg.PostgresDSN, postgres.ReadModelCacheSet{
		ReadModelVersions:  readModelVersionStore,
		ChannelRows:        channelRowCache,
		ChannelMembers:     channelMemberCache,
		ChannelDialogs:     channelDialogCache,
		ChannelBoosts:      channelBoostCache,
		Contacts:           postgres.ContactReadModelCaches{contactStore, contactsService},
		Dialogs:            dialogsService,
		Privacy:            privacyStore,
		ProfilePhotos:      cachedPhotos,
		Stories:            router,
		ChannelFullBots:    router,
		ChannelBotMembers:  channelsService,
		ChannelMediaCounts: channelsService,
		PrivateMediaCounts: messagesService,
		RPCProjections:     router,
		BaseUsers:          userCache,
		BotProfiles:        botsService,
		StarGifts:          giftsService,
	}, logger.Named("store").Named("read-model-listener"))
	go readModelListener.Run(ctx)
	activeSessions.SetLifecycleObserver(router)
	adminService.Configure(adminapp.Dependencies{
		Auth:            authService,
		Revoker:         router,
		Users:           usersService,
		Stars:           starsService,
		StarsNotifier:   router,
		UserNotifier:    router,
		Channels:        channelsService,
		ChannelNotifier: router,
		Messages:        messagesService,
		Gifts:           giftsService,
	})
	// bot session 撤销、在线通知与 @ChatBot 流式草稿推送经 router 实现（需 tg.* 边界），
	// router 创建后注入。
	botsService.SetRouterHooks(router)
	botsService.SetTextDraftPusher(router)
	go rpc.NewOutboxDispatcher(updateEventStore, dispatchOutboxStore, activeSessions, logger.Named("rpc").Named("outbox"),
		rpc.WithOutboxWorkers(cfg.OutboxWorkers),
		rpc.WithOutboxBatch(cfg.OutboxBatch),
		rpc.WithOutboxInterval(cfg.OutboxInterval),
		rpc.WithOutboxPushTimeout(cfg.OutboundPushTimeout),
		rpc.WithOutboxUpdateBuilder(router.BuildOutboxUpdates),
	).Run(ctx)
	go rpc.NewBootstrapUpdateDispatcher(router, logger.Named("rpc").Named("bootstrap")).Run(ctx)
	go rpc.NewScheduledDispatcher(router, logger.Named("rpc").Named("scheduled")).Run(ctx)
	go rpc.NewExpiryDispatcher(router, logger.Named("rpc").Named("expiry")).Run(ctx)
	go rpc.NewPhoneExpiryDispatcher(router, logger.Named("rpc").Named("phone-expiry"), cfg.CallExpiryInterval).Run(ctx)
	go rpc.NewGroupCallSweepDispatcher(router, logger.Named("rpc").Named("groupcall-sweep"), cfg.GroupCallSweepInterval, cfg.GroupCallCheckTTL).Run(ctx)
	go router.RunChannelFanout(ctx)
	go router.RunBotAPIEnqueue(ctx)
	go router.RunPresenceSweeper(ctx, time.Minute)
	go activeSessions.RunPendingSweeper(ctx, time.Minute)
	go router.RunPremiumSweeper(ctx, cfg.PremiumSweepInterval, cfg.PremiumSweepBatch)
	go router.RunInlineBotPushSubscriber(ctx)
	if _, err := botapi.Start(ctx, cfg.BotAPIAddr, botsService, usersService, router, router, logger.Named("botapi")); err != nil {
		return fmt.Errorf("start bot api: %w", err)
	}
	if _, err := adminapi.Start(ctx, adminapi.Config{Addr: cfg.AdminAPIAddr, Token: cfg.AdminAPIToken}, adminService, logger.Named("adminapi")); err != nil {
		return fmt.Errorf("start admin api: %w", err)
	}
	if _, err := web.Start(ctx, web.Config{
		Addr:          cfg.PublicLinkWebAddr,
		PublicBaseURL: cfg.PublicBaseURL,
		AppScheme:     cfg.PublicAppScheme,
		WebBaseURL:    cfg.PublicWebBaseURL,
		AppName:       cfg.PublicAppName,
		StickerSets:   filesService,
		Users:         userStore,
		Channels:      channelStore,
		Privacy:       privacyService,
		Photos:        filesService,
	}, logger.Named("public-web")); err != nil {
		return fmt.Errorf("start public Web: %w", err)
	}

	srv := mtprotoedge.New(mtprotoedge.Options{
		Logger:                          logger.Named("mtprotoedge"),
		DC:                              cfg.DC,
		RSAKey:                          rsaKey,
		LayerRPC:                        router,
		AuthKeys:                        authKeyStore,
		ActiveSessions:                  activeSessions,
		ObfuscatedTCP:                   true,
		WebSocket:                       cfg.WebSocketEnable,
		WebSocketAllowedOrigins:         cfg.WebSocketAllowedOrigins,
		MaxConnections:                  cfg.MTProtoMaxConnections,
		MaxConnectionsPerIP:             cfg.MTProtoMaxConnectionsPerIP,
		MaxConcurrentHandshakes:         cfg.MTProtoMaxConcurrentHandshakes,
		RPCMaxInflight:                  cfg.MTProtoRPCMaxInflight,
		RPCQueueSize:                    cfg.MTProtoRPCQueueSize,
		RPCTimeout:                      cfg.MTProtoRPCTimeout,
		RPCGlobalWorkers:                cfg.MTProtoRPCGlobalWorkers,
		RPCGlobalMaxTasks:               cfg.MTProtoRPCGlobalMaxTasks,
		RPCGlobalMaxBytes:               cfg.MTProtoRPCGlobalMaxBytes,
		RPCResultCacheMaxEntries:        cfg.MTProtoRPCResultCacheMaxEntries,
		RPCResultCacheMaxBytes:          cfg.MTProtoRPCResultCacheMaxBytes,
		RPCResultCacheAuthMaxEntries:    cfg.MTProtoRPCResultCacheAuthMaxEntries,
		RPCResultCacheAuthMaxBytes:      cfg.MTProtoRPCResultCacheAuthMaxBytes,
		RPCResultCacheSessionMaxEntries: cfg.MTProtoRPCResultCacheSessionMaxEntries,
		RPCResultCacheSessionMaxBytes:   cfg.MTProtoRPCResultCacheSessionMaxBytes,
		RPCResultPendingPerAuth:         cfg.MTProtoRPCResultPendingPerAuth,
		InboundFrameGlobalMaxBytes:      cfg.MTProtoInboundFrameGlobalMaxBytes,
		OutboundQueueSize:               cfg.MTProtoOutboundQueueSize,
		OutboundControlQueueSize:        cfg.MTProtoOutboundControlQueueSize,
		OutboundTrackedGlobalMaxBytes:   cfg.MTProtoOutboundTrackedGlobalMaxBytes,
		OutboundWriteGlobalMaxBytes:     cfg.MTProtoOutboundWriteGlobalMaxBytes,
		OnServing: func(_ net.Addr) {
			logger.Info("telesrv 服务就绪",
				zap.String("listen", cfg.ListenAddr),
				zap.String("advertise", net.JoinHostPort(cfg.AdvertiseIP, portStr)),
				zap.Int("pid", os.Getpid()),
				zap.String("git_commit", buildMeta.Commit),
				zap.Uint("schema_version", migrationStatus.Version),
				zap.String("blob_backend", "localfs"),
			)
		},
	})
	// This is intentionally the final startup operation. ListenAndServe owns the
	// public listener so no seed/prewarm work can run after port 2398 is exposed.
	return srv.ListenAndServe(ctx, cfg.ListenAddr)
}
