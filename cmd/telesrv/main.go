// Command telesrv is a Telegram-like server based on gotd/td (first compatibility target: Telegram Desktop).
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	runtimemetrics "runtime/metrics"
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
	authdiagnosticsapp "telesrv/internal/app/authdiagnostics"
	botsapp "telesrv/internal/app/bots"
	botverificationapp "telesrv/internal/app/botverification"
	broadcastapp "telesrv/internal/app/broadcast"
	channelapp "telesrv/internal/app/channels"
	chatlistsapp "telesrv/internal/app/chatlists"
	clienttelemetryapp "telesrv/internal/app/clienttelemetry"
	communitiesapp "telesrv/internal/app/communities"
	"telesrv/internal/app/contacts"
	"telesrv/internal/app/dialogs"
	donationsapp "telesrv/internal/app/donations"
	ephemeralapp "telesrv/internal/app/ephemeral"
	filesapp "telesrv/internal/app/files"
	"telesrv/internal/app/files/botavatars"
	groupcallsapp "telesrv/internal/app/groupcalls"
	"telesrv/internal/app/help"
	"telesrv/internal/app/langpack"
	"telesrv/internal/app/livestream"
	"telesrv/internal/app/maintenance"
	messageapp "telesrv/internal/app/messages"
	moderationapp "telesrv/internal/app/moderation"
	passkeyapp "telesrv/internal/app/passkey"
	phoneapp "telesrv/internal/app/phone"
	pollsapp "telesrv/internal/app/polls"
	premiumapp "telesrv/internal/app/premium"
	privacyapp "telesrv/internal/app/privacy"
	ratingapp "telesrv/internal/app/rating"
	secretchatapp "telesrv/internal/app/secretchat"
	"telesrv/internal/app/stargifts"
	"telesrv/internal/app/stars"
	storiesapp "telesrv/internal/app/stories"
	"telesrv/internal/app/systemidentity"
	telegramloginapp "telesrv/internal/app/telegramlogin"
	themesapp "telesrv/internal/app/themes"
	transcriptionapp "telesrv/internal/app/transcription"
	translationapp "telesrv/internal/app/translation"
	"telesrv/internal/app/updates"
	usernamesapp "telesrv/internal/app/usernames"
	"telesrv/internal/app/userprojection"
	"telesrv/internal/app/users"
	verificationapp "telesrv/internal/app/verification"
	walletapp "telesrv/internal/app/wallet"
	welcomemessagesapp "telesrv/internal/app/welcomemessages"
	"telesrv/internal/botapi"
	"telesrv/internal/branding"
	"telesrv/internal/config"
	"telesrv/internal/customfragment"
	"telesrv/internal/domain"
	"telesrv/internal/geoip"
	"telesrv/internal/giftclaim"
	"telesrv/internal/identity"
	"telesrv/internal/mtprotoedge"
	obsmetrics "telesrv/internal/observability/metrics"
	"telesrv/internal/officialgifts"
	"telesrv/internal/otpdelivery"
	otpsmtp "telesrv/internal/otpdelivery/smtp"
	otpwebhook "telesrv/internal/otpdelivery/webhook"
	"telesrv/internal/rpc"
	"telesrv/internal/seed/catalog"
	freezeseed "telesrv/internal/seed/freeze"
	"telesrv/internal/serverinfo"
	"telesrv/internal/sfu"
	storepkg "telesrv/internal/store"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/postgres"
	"telesrv/internal/store/redisstore"
	"telesrv/internal/telegramloginhttp"
	"telesrv/internal/tondns"
	"telesrv/internal/turnsrv"
	"telesrv/internal/updatecdn"
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
		logger.Error("telesrv exiting", zap.Error(err))
		_ = logger.Sync() // os.Exit skips defers; buffered writes need an explicit flush of the error log
		os.Exit(1)
	}
}

// newLogger builds the runtime logger. Two key changes (versus the old zap.NewDevelopment):
//   - Configurable level (TELESRV_LOG_LEVEL, default info): the old version was fixed at
//     Debug, and 65 Debug calls on hot paths (including one per frame in mtprotoedge) would
//     flood the log during connection bursts. info is enough for production/load testing;
//     set debug when needed.
//   - Buffered async writes (BufferedWriteSyncer): the old version did one synchronous
//     stderr write + global lock per log line, which under high concurrency accumulated
//     serially on the log lock -- measured during connection, the client_info phase of 12
//     concurrent RPCs was dragged into a ~1s thundering herd (91% of mutex profile
//     contention in zap writes). Buffering batches writes, cutting lock hold time from
//     "one system call per line" to "flush once per batch". FlushInterval controls the upper
//     bound on log visibility latency.
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

func localStarGiftWithdrawalOption(publicBaseURL, publicLinkWebAddr string, logger *zap.Logger) (stargifts.Option, error) {
	if strings.TrimSpace(publicLinkWebAddr) == "" {
		return nil, nil
	}
	provider, err := stargifts.NewLocalWithdrawalProvider(publicBaseURL)
	if errors.Is(err, stargifts.ErrLocalWithdrawalInsecureBaseURL) {
		// The Docker LAN quick start uses http://<LAN IP>:2401 (gramsrv#135).
		// Withdrawal is an optional feature there, so it must not stop the
		// server; the bearer links simply stay unavailable until HTTPS is set.
		if logger != nil {
			logger.Warn("local star gift withdrawal disabled: public base URL is plain HTTP outside loopback",
				zap.String("public_base_url", publicBaseURL))
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return stargifts.WithWithdrawalProvider(provider), nil
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
		logger.Warn("unknown Business automation AI provider, falling back to the quick reply template", zap.String("provider", cfg.BusinessAIProvider))
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
			logger.Warn("AI compose provider skipped", zap.String("provider", pc.Name), zap.String("kind", pc.Kind), zap.Error(err))
			continue
		}
		providers = append(providers, provider)
		logger.Info("AI compose provider enabled", zap.String("provider", provider.Name()), zap.String("kind", pc.Kind))
	}
	if len(providers) > 0 {
		opts = append(opts, aiapp.WithProviders(providers...))
	}
	return opts
}

func newCrossCraftGenerator(cfg config.Config, blobs stargifts.BlobBackend, logger *zap.Logger) postgres.StarGiftCrossCraftGenerator {
	if !cfg.AIEnabled {
		return nil
	}
	for _, pc := range cfg.AIProviders {
		if aiapp.ProviderKind(pc.Kind) != aiapp.ProviderKindOpenAIChat || pc.APIKey == "" {
			continue
		}
		generator := stargifts.NewCrossCraftGenerator(aiapp.ProviderConfig{
			Name: pc.Name, Kind: aiapp.ProviderKind(pc.Kind), BaseURL: pc.BaseURL, APIKey: pc.APIKey,
			Model: pc.Model, Timeout: max(cfg.AITimeout, 45*time.Second),
		}, blobs, cfg.DC)
		if generator != nil {
			logger.Info("cross collection craft AI configured", zap.String("provider", pc.Name))
			return generator
		}
	}
	logger.Warn("cross collection craft AI unavailable")
	return nil
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
			logger.Warn("translation provider skipped", zap.String("provider", pc.Name), zap.Error(err))
			continue
		}
		providers = append(providers, translationapp.NewAIProvider(provider))
		logger.Info("translation provider enabled", zap.String("provider", provider.Name()), zap.String("kind", pc.Kind))
	}
	if len(providers) > 0 {
		opts = append(opts, translationapp.WithProviders(providers...))
	} else if cfg.TranslationEnabled {
		logger.Warn("translation enabled but no remote provider; messages.translateText will return TRANSLATIONS_DISABLED")
	}
	return opts
}

// startDebugServer mounts the net/http/pprof debug endpoints on addr (disabled when addr is empty).
// It uses its own mux (so it does not pollute http.DefaultServeMux) and registers only pprof routes:
//   - /debug/pprof/profile  CPU profile (?seconds=30)
//   - /debug/pprof/heap     heap memory snapshot
//   - /debug/pprof/goroutine goroutine stacks (tracking leaks/blocks)
//   - /debug/pprof/mutex    lock contention (requires SetMutexProfileFraction)
//   - /debug/pprof/block    block profile (requires SetBlockProfileRate)
//   - /debug/pprof/allocs   cumulative allocations (bandwidth/serialization hotspots often correlate)
//
// mutex/block sampling overhead is negligible in a low-traffic test environment; in
// high-traffic production, if worried about perturbation, set DebugAddr empty to close the whole endpoint.
func startDebugServer(ctx context.Context, addr string, metricsHandler http.Handler, logger *zap.Logger) {
	if addr == "" {
		return
	}
	runtime.SetMutexProfileFraction(5) // sample 1/5 of lock contention events
	runtime.SetBlockProfileRate(10000) // sample once per ~10µs of blocking

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index) // includes named profiles such as heap/goroutine/mutex/block/allocs
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	if metricsHandler != nil {
		mux.Handle("/metrics", metricsHandler)
	}

	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		logger.Info("pprof debug endpoint enabled", zap.String("addr", addr),
			zap.String("hint", "go tool pprof http://"+addr+"/debug/pprof/profile?seconds=30"))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warn("pprof endpoint exiting", zap.Error(err))
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}

func goRuntimeGaugeSamples() []obsmetrics.GaugeSample {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	samples := []obsmetrics.GaugeSample{
		{Name: "telesrv_go_goroutines", Value: float64(runtime.NumGoroutine())},
		{Name: "telesrv_go_scheduler_busy_seconds", Value: goSchedulerBusySeconds()},
		{Name: "telesrv_go_heap_alloc_bytes", Value: float64(mem.HeapAlloc)},
		{Name: "telesrv_go_heap_inuse_bytes", Value: float64(mem.HeapInuse)},
		{Name: "telesrv_go_heap_objects", Value: float64(mem.HeapObjects)},
		{Name: "telesrv_go_stack_inuse_bytes", Value: float64(mem.StackInuse)},
		{Name: "telesrv_go_sys_bytes", Value: float64(mem.Sys)},
		{Name: "telesrv_go_gc_cycles", Value: float64(mem.NumGC)},
		{Name: "telesrv_go_gc_pause_seconds", Value: time.Duration(mem.PauseTotalNs).Seconds()},
	}
	if value, ok := processCPUSeconds(); ok {
		samples = append(samples, obsmetrics.GaugeSample{Name: "telesrv_process_cpu_seconds", Value: value})
	}
	return samples
}

// goSchedulerBusySeconds is a Go scheduler-class estimate. The runtime
// documentation explicitly warns that CPU-class values are overestimates and
// are not comparable to operating-system process CPU time, so capacity reports
// use telesrv_process_cpu_seconds instead.
func goSchedulerBusySeconds() float64 {
	samples := []runtimemetrics.Sample{
		{Name: "/cpu/classes/total:cpu-seconds"},
		{Name: "/cpu/classes/idle:cpu-seconds"},
	}
	runtimemetrics.Read(samples)
	total := samples[0].Value.Float64()
	idle := samples[1].Value.Float64()
	if total <= idle {
		return 0
	}
	return total - idle
}

func mtprotoRuntimeGaugeSamples(snapshot mtprotoedge.RuntimeSnapshot) []obsmetrics.GaugeSample {
	return []obsmetrics.GaugeSample{
		{Name: "telesrv_mtproto_raw_connections", Value: float64(snapshot.RawConnections)},
		{Name: "telesrv_mtproto_raw_connection_limit", Value: float64(snapshot.RawConnectionLimit)},
		{Name: "telesrv_mtproto_handshakes_active", Value: float64(snapshot.Handshakes)},
		{Name: "telesrv_mtproto_handshake_limit", Value: float64(snapshot.HandshakeLimit)},
		{Name: "telesrv_mtproto_sessions", Labels: []obsmetrics.Label{{Name: "state", Value: "active"}}, Value: float64(snapshot.ActiveSessions)},
		{Name: "telesrv_mtproto_sessions", Labels: []obsmetrics.Label{{Name: "state", Value: "provisional"}}, Value: float64(snapshot.ProvisionalSessions)},
		{Name: "telesrv_mtproto_logical_sessions", Labels: []obsmetrics.Label{{Name: "state", Value: "retained"}}, Value: float64(snapshot.LogicalSessions)},
		{Name: "telesrv_mtproto_logical_sessions", Labels: []obsmetrics.Label{{Name: "state", Value: "offline"}}, Value: float64(snapshot.OfflineLogicalSessions)},
		{Name: "telesrv_mtproto_logical_outbox_frames", Value: float64(snapshot.LogicalOutboxFrames)},
		{Name: "telesrv_mtproto_logical_outbox_bytes", Value: float64(snapshot.LogicalOutboxBytes)},
		{Name: "telesrv_mtproto_pending_push_bytes", Value: float64(snapshot.PendingPushBytes)},
		{Name: "telesrv_mtproto_inbound_rpc_tasks", Value: float64(snapshot.InboundRPCTasks)},
		{Name: "telesrv_mtproto_inbound_rpc_bytes", Value: float64(snapshot.InboundRPCBytes)},
		{Name: "telesrv_mtproto_inbound_rpc_ready_connections", Value: float64(snapshot.InboundRPCReadyConnections)},
		{Name: "telesrv_mtproto_inbound_rpc_task_limit", Value: float64(snapshot.InboundRPCMaxTasks)},
		{Name: "telesrv_mtproto_inbound_rpc_byte_limit", Value: float64(snapshot.InboundRPCMaxBytes)},
		{Name: "telesrv_mtproto_rpc_delivery_hook_workers", Value: float64(snapshot.RPCDeliveryHookWorkers)},
		{Name: "telesrv_mtproto_rpc_delivery_hook_capacity", Value: float64(snapshot.RPCDeliveryHookCapacity)},
		{Name: "telesrv_mtproto_rpc_delivery_hook_reserved", Value: float64(snapshot.RPCDeliveryHookReserved)},
		{Name: "telesrv_mtproto_rpc_delivery_hook_queued", Value: float64(snapshot.RPCDeliveryHookQueued)},
		{Name: "telesrv_mtproto_rpc_delivery_hook_running", Value: float64(snapshot.RPCDeliveryHookRunning)},
		{Name: "telesrv_mtproto_rpc_delivery_hook_completed_total", Value: float64(snapshot.RPCDeliveryHookCompleted)},
		{Name: "telesrv_mtproto_rpc_delivery_hook_rejected_total", Value: float64(snapshot.RPCDeliveryHookRejected)},
		{Name: "telesrv_mtproto_rpc_delivery_hook_panics_total", Value: float64(snapshot.RPCDeliveryHookPanics)},
		{Name: "telesrv_mtproto_rpc_delivery_hook_duration_seconds_total", Value: snapshot.RPCDeliveryHookDurationSeconds},
		{Name: "telesrv_mtproto_inbound_frame_bytes", Value: float64(snapshot.InboundFrameBytes)},
		{Name: "telesrv_mtproto_inbound_frame_byte_limit", Value: float64(snapshot.InboundFrameMaxBytes)},
		{Name: "telesrv_mtproto_outbound_tracked_bytes", Labels: []obsmetrics.Label{{Name: "kind", Value: "body"}}, Value: float64(snapshot.OutboundTrackedBytes)},
		{Name: "telesrv_mtproto_outbound_tracked_bytes", Labels: []obsmetrics.Label{{Name: "kind", Value: "control"}}, Value: float64(snapshot.OutboundControlBytes)},
		{Name: "telesrv_mtproto_outbound_tracked_bytes", Labels: []obsmetrics.Label{{Name: "kind", Value: "critical"}}, Value: float64(snapshot.OutboundCriticalBytes)},
		{Name: "telesrv_mtproto_outbound_tracked_byte_limit", Labels: []obsmetrics.Label{{Name: "kind", Value: "body"}}, Value: float64(snapshot.OutboundTrackedMaxBytes)},
		{Name: "telesrv_mtproto_outbound_tracked_byte_limit", Labels: []obsmetrics.Label{{Name: "kind", Value: "control"}}, Value: float64(snapshot.OutboundControlMaxBytes)},
		{Name: "telesrv_mtproto_outbound_tracked_byte_limit", Labels: []obsmetrics.Label{{Name: "kind", Value: "critical"}}, Value: float64(snapshot.OutboundCriticalMaxBytes)},
		{Name: "telesrv_mtproto_outbound_write_bytes", Value: float64(snapshot.OutboundWriteBytes)},
		{Name: "telesrv_mtproto_outbound_write_byte_limit", Value: float64(snapshot.OutboundWriteMaxBytes)},
		{Name: "telesrv_mtproto_rpc_execution_owners", Value: float64(snapshot.RPCExecutionOwners)},
		{Name: "telesrv_mtproto_rpc_execution_reserved_entries", Value: float64(snapshot.RPCExecutionReservedEntries)},
		{Name: "telesrv_mtproto_rpc_execution_receipts", Value: float64(snapshot.RPCExecutionReceipts)},
		{Name: "telesrv_mtproto_rpc_execution_receipt_budget_bytes", Value: float64(snapshot.RPCExecutionReceiptBudgetBytes)},
		{Name: "telesrv_mtproto_rpc_execution_subscribers", Value: float64(snapshot.RPCExecutionSubscribers)},
	}
}

// externalMediaOption enables external media fetching per config; when disabled it
// returns nil (NewService skips a nil option).
// liveStreamDep converts a possibly-nil *livestream.Service into an
// rpc.LiveStreamsService, avoiding the typed-nil interface trap (a nil concrete
// pointer stored in an interface compares != nil).
func liveStreamDep(s *livestream.Service) rpc.LiveStreamsService {
	if s == nil {
		return nil
	}
	return s
}

// verificationPeerVerifier writes the platform verification flag onto the peer
// record for app/verification.
//
// It is called from *inside* the store transaction that decides the application,
// which is the whole point of the port: "approved" and "target carries the badge"
// must commit together. That is why the transaction is taken from the context
// (postgres.VerificationTxFromContext) and written through — a write on a separate
// pool connection would survive a rollback of the decision and leave a peer
// wearing a badge no approved application backs.
//
// The app-service path is only the fallback for a context that carries no
// transaction (a non-postgres store, or a direct call): there is nothing to join
// then, and going through the services keeps their cache refresh behaviour.
type verificationPeerVerifier struct {
	users interface {
		SetVerified(ctx context.Context, userID int64, verified bool) (domain.User, error)
	}
	channels interface {
		SetVerified(ctx context.Context, channelID int64, verified bool) (domain.Channel, error)
	}
	// channelRowCache is handed to the transaction-scoped channel store so the
	// cached channel row is dropped on the flag write, exactly as the pooled store
	// does it.
	channelRowCache *postgres.ChannelRowCache
}

func (v verificationPeerVerifier) SetUserVerified(ctx context.Context, userID int64, verified bool) error {
	if tx, ok := postgres.VerificationTxFromContext(ctx); ok {
		_, err := postgres.NewUserStore(tx).SetVerified(ctx, userID, verified)
		return err
	}
	if v.users == nil {
		return fmt.Errorf("verification peer verifier: user service is not wired")
	}
	_, err := v.users.SetVerified(ctx, userID, verified)
	return err
}

func (v verificationPeerVerifier) SetChannelVerified(ctx context.Context, channelID int64, verified bool) error {
	if tx, ok := postgres.VerificationTxFromContext(ctx); ok {
		opts := []postgres.ChannelStoreOption(nil)
		if v.channelRowCache != nil {
			opts = append(opts, postgres.WithChannelRowCache(v.channelRowCache))
		}
		_, err := postgres.NewChannelStore(tx, opts...).SetChannelVerified(ctx, channelID, verified)
		return err
	}
	if v.channels == nil {
		return fmt.Errorf("verification peer verifier: channel service is not wired")
	}
	_, err := v.channels.SetVerified(ctx, channelID, verified)
	return err
}

var _ verificationapp.PeerVerifier = verificationPeerVerifier{}

// botVerificationMarkApplier writes a third-party mark on the decision's own
// transaction when there is one.
//
// postgres.DecideCustomVerificationRequest hands its callback a context carrying
// the transaction, and the pooled store would open a second, independently
// committing one -- so an approval whose mark write failed would leave the request
// approved with no mark. This adapter is what makes "approved implies mark exists"
// survive a rollback, exactly as verificationPeerVerifier does for the official flag.
type botVerificationMarkApplier struct {
	store storepkg.BotVerificationStore
}

func (a botVerificationMarkApplier) GrantCustomVerification(ctx context.Context, mark domain.CustomVerification) (domain.CustomVerification, bool, error) {
	if tx, ok := postgres.VerificationTxFromContext(ctx); ok {
		return postgres.NewBotVerificationStore(tx).GrantCustomVerification(ctx, mark)
	}
	return a.store.GrantCustomVerification(ctx, mark)
}

func (a botVerificationMarkApplier) RevokeCustomVerification(ctx context.Context, verifierBotID int64, peer domain.Peer) (bool, error) {
	if tx, ok := postgres.VerificationTxFromContext(ctx); ok {
		return postgres.NewBotVerificationStore(tx).RevokeCustomVerification(ctx, verifierBotID, peer)
	}
	return a.store.RevokeCustomVerification(ctx, verifierBotID, peer)
}

var _ botverificationapp.MarkApplier = botVerificationMarkApplier{}

// compositeBotVerificationNotifier drops the cached peer projections before the
// edge rebuilds and pushes the peer, so a mark change cannot be pushed with a
// stale badge.
type compositeBotVerificationNotifier struct {
	cache rpcProjectionVerificationNotifier
	edge  botverificationapp.PeerNotifier
}

func (n compositeBotVerificationNotifier) NotifyPeerBotVerification(ctx context.Context, peer domain.Peer) error {
	if err := n.cache.NotifyPeerVerified(ctx, peer); err != nil && n.cache.log != nil {
		n.cache.log.Warn("invalidate peer caches after third-party verification change",
			zap.String("peer_type", string(peer.Type)), zap.Int64("peer_id", peer.ID), zap.Error(err))
	}
	if n.edge == nil {
		return nil
	}
	return n.edge.NotifyPeerBotVerification(ctx, peer)
}

var _ botverificationapp.PeerNotifier = compositeBotVerificationNotifier{}

// rpcProjectionVerificationNotifier is the fallback badge-change hook, the same
// shape and for the same reason as rpcProjectionUsernameNotifier: the RPC edge
// owns both the cached peer projections and the tg.* push, and until it exposes
// NotifyPeerVerified only the invalidation half can be wired here. Invalidation is
// the half that must not be skipped — a decided application whose peer projection
// still says "not verified" would keep showing the old badge state to every client
// that reads from cache.
type rpcProjectionVerificationNotifier struct {
	invalidator interface {
		InvalidateRPCProjectionReadModelForUser(userID int64)
		InvalidateRPCProjectionReadModelForChannel(channelID int64)
		InvalidatePeerIdentityReadModel(domain.Peer)
	}
	users        storepkg.UserCache
	peerIdentity bool
	log          *zap.Logger
}

func (n rpcProjectionVerificationNotifier) NotifyPeerVerified(ctx context.Context, peer domain.Peer) error {
	if n.invalidator == nil {
		return nil
	}
	if n.peerIdentity {
		n.invalidator.InvalidatePeerIdentityReadModel(peer)
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		n.invalidator.InvalidateRPCProjectionReadModelForUser(peer.ID)
		// The shared user:base cache is the source the projection rebuilds from, so
		// dropping only the projection would let it rebuild from a stale row.
		if n.users != nil {
			if err := n.users.Delete(ctx, []int64{peer.ID}); err != nil && n.log != nil {
				n.log.Warn("invalidate base user cache after verification change",
					zap.Int64("user_id", peer.ID), zap.Error(err))
			}
		}
	case domain.PeerTypeChannel:
		n.invalidator.InvalidateRPCProjectionReadModelForChannel(peer.ID)
	}
	return nil
}

// compositeVerificationNotifier drops the cached peer projections first and only
// then lets the protocol edge push the change, so the pushed peer is rebuilt from
// the committed row rather than from a cache entry written before the decision.
// A cache failure must not swallow the push: the push is what online clients see.
type compositeVerificationNotifier struct {
	cache rpcProjectionVerificationNotifier
	edge  verificationapp.PeerNotifier
}

func (n compositeVerificationNotifier) NotifyPeerVerified(ctx context.Context, peer domain.Peer) error {
	if err := n.cache.NotifyPeerVerified(ctx, peer); err != nil && n.cache.log != nil {
		n.cache.log.Warn("invalidate peer caches after verification change",
			zap.String("peer_type", string(peer.Type)), zap.Int64("peer_id", peer.ID), zap.Error(err))
	}
	if n.edge == nil {
		return nil
	}
	return n.edge.NotifyPeerVerified(ctx, peer)
}

var _ verificationapp.PeerNotifier = compositeVerificationNotifier{}

var _ verificationapp.PeerNotifier = rpcProjectionVerificationNotifier{}

func externalMediaOption(cfg config.Config) filesapp.Option {
	if !cfg.ExternalMediaEnable {
		return nil
	}
	return filesapp.WithExternalMedia(cfg.ExternalMediaMaxBytes, cfg.ExternalMediaRatePerMin)
}

// webPagePreviewOption enables link-preview fetching per config; when disabled it returns nil (NewService skips a nil option).
func webPagePreviewOption(cfg config.Config) filesapp.Option {
	if !cfg.WebPagePreviewEnable {
		return nil
	}
	return filesapp.WithWebPagePreview(cfg.WebPagePreviewMaxBytes, cfg.WebPagePreviewRatePerMin)
}

// logConfigDrift compares .env against .env.example at startup, reporting only,
// never rewriting:
//
//   - INFO "declared in the template but not set on this machine" -- usually a
//     deliberate default, but omissions like "a new version added config while
//     .env is still old" also show up here, so it gives a checklist to verify;
//   - WARN "present in .env but not listed in the template" -- almost certainly a
//     key-name typo or a lagging template: readEnvFile accepts any TELESRV_* key
//     name, while Load only queries keys it knows, so a typo's value is silently
//     ignored.
//
// When the template is missing (production images do not ship .env.example),
// DetectEnvDrift returns a zero value and this silently skips.
// It deliberately does not auto-write .env: the container is a read-only rootfs,
// and silently editing the operator's config file is worse than under-reporting.
func logConfigDrift(logger *zap.Logger) {
	drift, err := config.DetectEnvDrift()
	if err != nil {
		logger.Warn("config drift comparison failed (does not affect startup)", zap.Error(err))
		return
	}
	if len(drift.Missing) == 0 && len(drift.Unknown) == 0 {
		return
	}
	if len(drift.Missing) > 0 {
		logger.Info("declared in .env.example but not set on this machine; using code defaults",
			zap.Int("count", len(drift.Missing)),
			zap.Strings("keys", drift.Missing))
	}
	if len(drift.Unknown) > 0 {
		logger.Warn("keys in .env are not listed in .env.example: possibly a key-name typo or a lagging template; a typo's value is silently ignored",
			zap.Int("count", len(drift.Unknown)),
			zap.Strings("keys", drift.Unknown))
	}
}

func run(logger *zap.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logConfigDrift(logger)
	if err := branding.Configure(cfg.Branding); err != nil {
		return fmt.Errorf("configure branding: %w", err)
	}
	if !domain.ConfigurePremiumBotUserID(cfg.PremiumBotUserID) {
		return fmt.Errorf("configure Premium bot user id %d", cfg.PremiumBotUserID)
	}
	if !domain.ConfigurePremiumBotUsername(cfg.PremiumBotUsername) {
		return fmt.Errorf("configure Premium bot username %q", cfg.PremiumBotUsername)
	}
	// The phone format policy must be installed before store / RPC services are
	// constructed: a login identity's normalized result lands in users.phone, and
	// switching the policy must not leave half-normalized data behind.
	domain.ConfigurePhoneFormatCheck(cfg.CheckNumberFormat)
	logger.Info("phone format policy",
		zap.Bool("check_number_format", cfg.CheckNumberFormat),
		zap.Bool("strict", domain.PhoneFormatCheckEnabled()))
	// SCAM/FAKE warning text (blank -> domain's built-in English default): written
	// once at startup and read by getFullUser / channels.getFullChannel / the About
	// projection of invite previews.
	domain.SetModerationProfileWarning(cfg.ScamProfileWarning, cfg.FakeProfileWarning)
	// The server identity is maintained by the admin panel (Server Settings ->
	// Server identity) under cfg.IdentityDir; here it reads the custom server name
	// once at startup, used as the display name of the official 777000 system
	// account and the login-notification {{server_name}} placeholder (see
	// domain.SetOfficialSystemUserDisplayName). The login notification template is
	// read live on every send inside internal/app/auth; the name and icon then keep
	// following panel changes at runtime via systemidentity.Watcher, with no restart.
	identityStore := identity.NewStore(cfg.IdentityDir)
	if info, err := identityStore.Get(); err != nil {
		logger.Warn("failed to read server identity, falling back to the branding default name", zap.Error(err))
	} else {
		domain.SetOfficialSystemUserDisplayName(info.Name)
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

	// tg.Layer is generated from the currently imported canonical schema; after
	// adopting a future Layer there is no need to maintain a separate constant in telesrv.
	logger.Info("telesrv starting",
		zap.String("listen", cfg.ListenAddr),
		zap.Int("dc", cfg.DC),
		zap.String("default_country_code", cfg.DefaultCountryCode),
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
	metricRegistry := obsmetrics.New()
	metricRegistry.AddGaugeProvider(goRuntimeGaugeSamples)

	// pprof debug endpoint: telesrv is a host process (not inside docker, so
	// docker stats cannot see it), and diagnosing CPU/memory/goroutine/lock
	// contention all relies on this endpoint. It is started before heavy
	// initialization, so even the seed/warm-up phases can be profiled.
	startDebugServer(ctx, cfg.DebugAddr, metricRegistry, logger)

	// Persistence dependencies: migrate the schema first, then establish
	// connections. Auth keys and business facts land in PostgreSQL, while Redis
	// carries only rebuildable short-TTL state, caches, counters and rate limits.
	// Dependencies are started by deploy/docker-compose.yml; failing to connect
	// fails startup (during development you must docker compose up first).
	migrationStatus, err := postgres.MigrateAndStatus(cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("postgres migrate: %w", err)
	}
	logger.Info("PostgreSQL schema migrated",
		zap.Uint("schema_version", migrationStatus.Version),
		zap.Bool("schema_dirty", migrationStatus.Dirty),
		zap.Bool("schema_empty", migrationStatus.Empty),
	)
	blobRuntimeLock, err := postgres.AcquireBlobRuntimeLock(ctx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("acquire blob runtime lock: %w", err)
	}
	defer func() {
		if err := blobRuntimeLock.Close(); err != nil {
			logger.Error("release blob runtime lock", zap.Error(err))
		}
	}()
	pool, err := postgres.Open(ctx, cfg.PostgresDSN,
		postgres.WithMaxConns(cfg.PostgresMaxConns),
		postgres.WithMinConns(cfg.PostgresMinConns),
	)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()
	metricRegistry.AddGaugeProvider(func() []obsmetrics.GaugeSample {
		stat := pool.Stat()
		return []obsmetrics.GaugeSample{
			{Name: "telesrv_postgres_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "total"}}, Value: float64(stat.TotalConns())},
			{Name: "telesrv_postgres_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "acquired"}}, Value: float64(stat.AcquiredConns())},
			{Name: "telesrv_postgres_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "idle"}}, Value: float64(stat.IdleConns())},
			{Name: "telesrv_postgres_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "constructing"}}, Value: float64(stat.ConstructingConns())},
			{Name: "telesrv_postgres_pool_max_connections", Value: float64(stat.MaxConns())},
			{Name: "telesrv_postgres_pool_acquire_count", Value: float64(stat.AcquireCount())},
			{Name: "telesrv_postgres_pool_acquire_wait_seconds", Value: stat.AcquireDuration().Seconds()},
			{Name: "telesrv_postgres_pool_empty_acquire_count", Value: float64(stat.EmptyAcquireCount())},
			{Name: "telesrv_postgres_pool_canceled_acquire_count", Value: float64(stat.CanceledAcquireCount())},
		}
	})

	var telegramLoginService *telegramloginapp.Service
	var telegramLoginIDTokens *telegramloginapp.IDTokenIssuer
	var telegramLoginHTTPHandler http.Handler
	if cfg.TelegramLoginEnabled {
		codeSealer, err := telegramloginapp.LoadCodeSealer(cfg.TelegramLoginCodeKeysFile)
		if err != nil {
			return fmt.Errorf("load telegram login code keys: %w", err)
		}
		clientSecretPepper, err := telegramloginapp.LoadClientSecretPepper(cfg.TelegramLoginSecretPepperFile)
		if err != nil {
			return fmt.Errorf("load telegram login client-secret pepper: %w", err)
		}
		signingKeys, err := telegramloginapp.LoadSigningKeyRing(cfg.TelegramLoginSigningKeysFile, time.Now)
		if err != nil {
			return fmt.Errorf("load telegram login signing keys: %w", err)
		}
		telegramLoginService, err = telegramloginapp.NewService(postgres.NewTelegramLoginStore(pool), codeSealer, telegramloginapp.Config{
			Issuer: cfg.TelegramLoginIssuer, AppScheme: cfg.PublicAppScheme, AppLinkBase: cfg.PublicAppLinkBase,
			AllowHTTP:                  cfg.TelegramLoginAllowHTTP,
			ClientSecretPepper:         clientSecretPepper,
			SupportedSigningAlgorithms: signingKeys.ActiveAlgorithms(),
			RequestTTL:                 cfg.TelegramLoginRequestTTL, CodeTTL: cfg.TelegramLoginCodeTTL,
		})
		if err != nil {
			return fmt.Errorf("initialize telegram login service: %w", err)
		}
		telegramLoginIDTokens, err = telegramloginapp.NewIDTokenIssuer(signingKeys, telegramloginapp.IDTokenIssuerConfig{
			Issuer: cfg.TelegramLoginIssuer, TTL: cfg.TelegramLoginIDTokenTTL, AllowHTTP: cfg.TelegramLoginAllowHTTP,
		})
		if err != nil {
			return fmt.Errorf("initialize telegram login ID-token issuer: %w", err)
		}
	}

	rdb, err := redisstore.Open(ctx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		return fmt.Errorf("connect redis: %w", err)
	}
	defer func() { _ = rdb.Close() }()
	metricRegistry.AddGaugeProvider(func() []obsmetrics.GaugeSample {
		stat := rdb.PoolStats()
		return []obsmetrics.GaugeSample{
			{Name: "telesrv_redis_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "total"}}, Value: float64(stat.TotalConns)},
			{Name: "telesrv_redis_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "idle"}}, Value: float64(stat.IdleConns)},
			{Name: "telesrv_redis_pool_pending_requests", Value: float64(stat.PendingRequests)},
			{Name: "telesrv_redis_pool_hits", Value: float64(stat.Hits)},
			{Name: "telesrv_redis_pool_misses", Value: float64(stat.Misses)},
			{Name: "telesrv_redis_pool_timeouts", Value: float64(stat.Timeouts)},
			{Name: "telesrv_redis_pool_wait_count", Value: float64(stat.WaitCount)},
			{Name: "telesrv_redis_pool_wait_seconds", Value: time.Duration(stat.WaitDurationNs).Seconds()},
		}
	})
	logger.Info("persistence dependencies ready", zap.String("redis", cfg.RedisAddr))
	if cfg.TelegramLoginEnabled {
		telegramLoginHTTPHandler, err = telegramloginhttp.NewHandler(telegramloginhttp.Config{
			Service: telegramLoginService, Tokens: telegramLoginIDTokens,
			BotUsernames: postgres.NewUserStore(pool),
			Limiter:      redisstore.NewRateLimiter(rdb), AppName: cfg.PublicAppName,
			Logger: logger.Named("telegram-login-http"), TrustedProxyCIDRs: cfg.TelegramLoginTrustedProxyCIDRs,
			AllowHTTP: cfg.TelegramLoginAllowHTTP,
		})
		if err != nil {
			return fmt.Errorf("initialize telegram login HTTP provider: %w", err)
		}
		logger.Info("Telegram Login/OIDC provider enabled",
			zap.String("issuer", telegramLoginIDTokens.Issuer()),
			zap.Strings("signing_algorithms", telegramLoginIDTokens.SupportedAlgorithms()))
	}

	authKeyStore := postgres.NewAuthKeyStore(pool)
	authKeyGetBatchStore, err := postgres.NewBatchedAuthKeyStore(
		authKeyStore,
		postgres.AuthKeyGetBatchConfig{
			MaxSize: cfg.AuthKeyGetBatchMax, MaxWait: cfg.AuthKeyGetBatchWait,
			QueueSize: cfg.AuthKeyGetBatchQueue, QueryTimeout: cfg.AuthKeyGetBatchTimeout,
		},
	)
	if err != nil {
		return err
	}
	defer authKeyGetBatchStore.Close()
	authKeySessionLayerStore, err := postgres.NewBatchedAuthKeySessionLayerStore(
		authKeyStore,
		postgres.AuthKeySessionLayerBatchConfig{
			MaxSize: cfg.LayerAdvanceBatchMax, MaxWait: cfg.LayerAdvanceBatchWait,
			QueueSize: cfg.LayerAdvanceBatchQueue, QueryTimeout: cfg.LayerAdvanceBatchTimeout,
		},
	)
	if err != nil {
		return err
	}
	defer authKeySessionLayerStore.Close()
	userStore := postgres.NewUserStore(pool)
	// The configured product username belongs to the official 777000 identity.
	// Claiming it is atomic with clearing an ordinary account that occupied the
	// editable slot; protected identities and collectible names are not seized.
	if claim, err := userStore.ClaimOfficialUsername(ctx, branding.ProductUsername()); err != nil {
		logger.Warn("sync system account username", zap.Error(err))
	} else if claim.DisplacedUserID != 0 {
		logger.Warn("reclaimed system account username from ordinary user",
			zap.String("username", claim.Official.Username),
			zap.Int64("displaced_user_id", claim.DisplacedUserID))
	}
	authzStore := postgres.NewAuthorizationStore(pool)
	adminStore := postgres.NewAdminStore(pool)
	updateStateStore := postgres.NewUpdateStateStore(pool)
	updateEventStore := postgres.NewUpdateEventStore(pool, postgres.WithUpdateEventLogger(logger.Named("store").Named("updates")))
	phoneChangeStore := postgres.NewPhoneChangeStore(pool)
	collectiblePhoneStore := postgres.NewCollectiblePhoneStore(pool)
	readModelVersionBatchStore, err := storepkg.NewBatchedReadModelVersionStore(
		postgres.NewReadModelVersionStore(pool),
		storepkg.ReadModelVersionBatchConfig{
			MaxKeys: cfg.ReadModelVersionBatchMaxKeys, MaxWait: cfg.ReadModelVersionBatchWait,
			QueueSize: cfg.ReadModelVersionBatchQueue, QueryTimeout: cfg.ReadModelVersionBatchTimeout,
		},
	)
	if err != nil {
		return err
	}
	defer readModelVersionBatchStore.Close()
	readModelVersionStore := storepkg.NewCachedReadModelVersionStore(
		readModelVersionBatchStore,
		0,
		cfg.ReadModelVersionCacheMaxEntries,
	)
	dialogListSnapshotCache := redisstore.NewDialogListSnapshotCache(rdb, cfg.DialogListSnapshotRedisTTL)
	activeChannelIDsPageCache := redisstore.NewActiveChannelIDsPageCache(rdb, cfg.ActiveChannelIDsRedisTTL)
	dispatchOutboxStore := postgres.NewDispatchOutboxStore(pool, postgres.WithLeaseTimeout(cfg.OutboxLeaseTimeout))
	bootstrapUpdateStore, err := postgres.NewBatchedBootstrapUpdateJobStore(
		postgres.NewBootstrapUpdateJobStore(pool),
		postgres.BootstrapReadyBatchConfig{
			MaxSize: cfg.BootstrapReadyBatchMax, MaxWait: cfg.BootstrapReadyBatchWait,
			QueueSize: cfg.BootstrapReadyBatchQueue, QueryTimeout: cfg.BootstrapReadyBatchTimeout,
			Metrics: metricRegistry,
		},
	)
	if err != nil {
		return err
	}
	defer bootstrapUpdateStore.Close()
	botAPIUpdateStore := postgres.NewBotAPIUpdateStore(pool)
	botCallbackStore := redisstore.NewBotCallbackRegistryStore(rdb)
	ephemeralStore := redisstore.NewEphemeralMessageStore(rdb)
	ephemeralReportStore := postgres.NewEphemeralReportStore(pool)
	welcomeMessageStore := postgres.NewWelcomeMessageStore(pool)
	messageTranscriptionStore := postgres.NewMessageTranscriptionStore(pool)
	moderationReportStore := postgres.NewModerationReportStore(pool)
	authDeliveryReportStore := postgres.NewAuthDeliveryReportStore(pool)
	clientTelemetryStore := postgres.NewClientTelemetryStore(pool)
	boxIDAllocator := redisstore.NewBoxIDAllocator(rdb, postgres.NewMessageBoxCounterSource(pool))
	channelIDAllocator := redisstore.NewChannelIDAllocator(rdb, postgres.NewChannelIDCounterSource(pool))
	channelMessageIDAllocator := redisstore.NewChannelMessageIDAllocator(rdb, postgres.NewChannelMessageIDCounterSource(pool))
	reverseContactStore, err := storepkg.NewBatchedReverseContactStore(
		postgres.NewContactStore(pool),
		storepkg.ReverseContactBatchConfig{
			MaxPairs: cfg.ContactReverseBatchMaxPairs, MaxWait: cfg.ContactReverseBatchWait,
			QueueSize: cfg.ContactReverseBatchQueue, QueryTimeout: cfg.ContactReverseBatchTimeout,
		},
	)
	if err != nil {
		return err
	}
	defer reverseContactStore.Close()
	contactStore := userprojection.NewCachedContactStoreWithMaxViewers(
		reverseContactStore,
		0,
		cfg.ContactSnapshotCacheMaxViewers,
	)
	dialogStore := postgres.NewDialogStore(pool)
	chatlistStore := postgres.NewChatlistStore(pool)
	messageStore := postgres.NewMessageStore(pool,
		postgres.WithMessageAllocators(boxIDAllocator),
		postgres.WithMessageLogger(logger.Named("store").Named("messages")))
	broadcastStore := postgres.NewBroadcastStore(pool)
	broadcastService := broadcastapp.NewService(broadcastStore, messageStore, logger.Named("app").Named("broadcast"))
	// Shared channel row/member caches + unified read-model LISTEN/NOTIFY realtime
	// invalidation: eliminate the amplification of high-frequency "resolve
	// channel/member per RPC" re-reading the same row during a client reconnect sync burst.
	channelRowCache := postgres.NewChannelRowCache(cfg.ChannelRowCacheMaxEntries)
	channelTopMessageCache := postgres.NewChannelTopMessageCache(cfg.ChannelTopMessageCacheMaxEntries)
	channelMemberCache := postgres.NewChannelMemberCache(cfg.ChannelMemberCacheMaxEntries)
	channelDialogCache := postgres.NewChannelDialogCache(cfg.ChannelDialogCacheMaxEntries)
	channelDifferenceCache := postgres.NewChannelDifferenceBaseCache(
		cfg.ChannelDifferenceCacheMaxEntries,
		cfg.ChannelDifferenceCacheMaxBytes,
		cfg.ChannelDifferenceCacheTTL,
	)
	channelBoostCache := postgres.NewChannelBoostCache(cfg.ChannelBoostCacheMaxEntries, cfg.ChannelBoostCacheTTL)
	channelStore := postgres.NewChannelStore(pool,
		postgres.WithChannelAllocators(channelIDAllocator, channelMessageIDAllocator),
		postgres.WithChannelStarsStartingGrant(cfg.StarsStartingGrant),
		postgres.WithChannelLogger(logger.Named("store").Named("channels")),
		postgres.WithChannelRowCache(channelRowCache),
		postgres.WithChannelTopMessageCache(channelTopMessageCache),
		postgres.WithChannelMemberCache(channelMemberCache),
		postgres.WithChannelDialogCache(channelDialogCache),
		postgres.WithChannelDifferenceBaseCache(channelDifferenceCache),
		postgres.WithChannelBoostCache(channelBoostCache))
	activeChannelIDsPageBatcher, err := postgres.NewActiveChannelIDsPageBatcher(
		channelStore,
		postgres.ActiveChannelIDsBatchConfig{
			MaxSize: cfg.ActiveChannelIDsBatchMax, MaxWait: cfg.ActiveChannelIDsBatchWait,
			QueueSize: cfg.ActiveChannelIDsBatchQueue, QueryTimeout: cfg.ActiveChannelIDsBatchTimeout,
			Metrics: metricRegistry,
		},
	)
	if err != nil {
		return err
	}
	defer activeChannelIDsPageBatcher.Close()
	metricRegistry.AddGaugeProvider(func() []obsmetrics.GaugeSample {
		snapshot := channelDifferenceCache.Snapshot()
		return []obsmetrics.GaugeSample{
			{Name: "telesrv_channel_difference_cache_entries", Value: float64(snapshot.Entries)},
			{Name: "telesrv_channel_difference_cache_weight_bytes", Value: float64(snapshot.Weight)},
			{Name: "telesrv_channel_difference_cache_hits", Value: float64(snapshot.Hits)},
			{Name: "telesrv_channel_difference_cache_misses", Value: float64(snapshot.Misses)},
			{Name: "telesrv_channel_difference_cache_loads", Value: float64(snapshot.Loads)},
			{Name: "telesrv_channel_difference_cache_load_errors", Value: float64(snapshot.LoadErrors)},
		}
	})
	communityCatalogCache := postgres.NewCommunityCatalogCache()
	communityStore := postgres.NewCommunityStore(pool, channelIDAllocator, channelMessageIDAllocator,
		postgres.WithCommunityCatalogCache(communityCatalogCache))
	pollStore := postgres.NewPollStore(pool)
	mediaStore := postgres.NewMediaStore(pool)
	gifCatalogStore := postgres.NewGifCatalogStore(pool)
	// Avatar projection cache: all projectors share an owner->avatar positive/negative
	// LRU. profile_photo NOTIFY precise invalidation handles normal freshness, while
	// the long TTL only covers missed notifications, avoiding a login ramp periodically
	// re-querying stable negative values.
	cachedPhotos := userprojection.NewCachedPhotoProviderWithMaxEntries(
		mediaStore,
		cfg.ProfilePhotoCacheTTL,
		cfg.ProfilePhotoCacheMaxEntries,
	)
	privacyStore := privacyapp.NewCachedPrivacyStore(postgres.NewPrivacyStore(pool), 0)
	storyStore := postgres.NewStoryStore(pool)
	if err := requireConfiguredBlobBackend(ctx, mediaStore, cfg.BlobBackendKind); err != nil {
		return fmt.Errorf("validate configured blob backend: %w", err)
	}
	blobStorage, err := newBlobStorageRuntime(ctx, cfg)
	if err != nil {
		return fmt.Errorf("init blob backend: %w", err)
	}
	capacity, err := newBlobCapacityRuntime(ctx, cfg, mediaStore)
	if err != nil {
		return fmt.Errorf("init blob capacity guard: %w", err)
	}
	for _, guard := range capacity.workers {
		go filesapp.NewDiskUsageWorker(guard, cfg.StorageUsageRefreshInterval, logger.Named("files").Named("capacity")).Run(ctx)
	}
	blobBackend := filesapp.BlobBackend(filesapp.NewGuardedBlobBackend(blobStorage.permanent, capacity.permanentWrite))
	uploadPartBackend := filesapp.UploadPartBackend(filesapp.NewGuardedUploadPartBackend(blobStorage.uploadPart, capacity.stagingWrite))
	if cfg.BlobBackendKind == string(domain.MediaBackendS3) {
		logger.Info("blob backend ready",
			zap.String("backend", blobBackend.Name()),
			zap.String("endpoint", cfg.S3Endpoint),
			zap.String("bucket", cfg.S3Bucket),
			zap.String("upload_staging_dir", cfg.BlobStagingDir),
		)
	} else {
		logger.Info("blob backend ready",
			zap.String("backend", blobBackend.Name()),
			zap.String("dir", cfg.BlobDir),
		)
	}
	filesService := filesapp.NewService(mediaStore, blobBackend, cfg.DC,
		filesapp.WithLogger(logger),
		filesapp.WithGifCatalog(gifCatalogStore),
		filesapp.WithUploadPartBackend(uploadPartBackend),
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
		logger.Info("map thumbnail proxy enabled", zap.String("provider", "mapbox"), zap.String("cache_dir", cfg.MapTileCacheDir))
	}
	if cfg.ExternalMediaEnable {
		logger.Info("external media fetching enabled", zap.Int64("max_bytes", cfg.ExternalMediaMaxBytes), zap.Int("rate_per_min", cfg.ExternalMediaRatePerMin))
	}
	if cfg.WebPagePreviewEnable {
		logger.Info("link preview fetching enabled", zap.Int64("max_bytes", cfg.WebPagePreviewMaxBytes), zap.Int("rate_per_min", cfg.WebPagePreviewRatePerMin))
	}
	if stats, err := filesService.SeedMedia(ctx, cfg.StickerSeedDir, cfg.StickerSeedMaxSets); err != nil {
		return fmt.Errorf("seed media: %w", err)
	} else if !stats.Skipped {
		logger.Info("media seed import complete",
			zap.String("dir", cfg.StickerSeedDir),
			zap.Int("reactions", stats.Reactions),
			zap.Int("sticker_sets", stats.StickerSets),
			zap.Int("effects", stats.Effects),
			zap.Int("documents", stats.Documents),
			zap.Int("blobs", stats.Blobs),
		)
	}
	if stats, err := filesService.SeedGifs(ctx, cfg.GifSeedDir); err != nil {
		return fmt.Errorf("seed gif catalog: %w", err)
	} else if stats.Imported > 0 || stats.Skipped > 0 {
		logger.Info("GIF catalog seed complete", zap.String("dir", cfg.GifSeedDir),
			zap.Int("imported", stats.Imported), zap.Int("skipped", stats.Skipped),
			zap.String("blob_backend", blobBackend.Name()))
	}
	if err := filesService.ValidateGifCatalog(ctx); err != nil {
		return fmt.Errorf("validate gif catalog: %w", err)
	}
	if stats, err := filesService.SeedPremiumPromo(ctx, cfg.PremiumPromoSeedDir); err != nil {
		return fmt.Errorf("seed premium promo: %w", err)
	} else if !stats.Skipped {
		logger.Info("Premium promo video seed import complete",
			zap.String("dir", cfg.PremiumPromoSeedDir),
			zap.Int("videos", stats.Videos),
			zap.Int("blobs", stats.Blobs),
		)
	}
	if stats, err := filesService.SeedAppearance(ctx); err != nil {
		return fmt.Errorf("seed appearance: %w", err)
	} else if !stats.Skipped {
		logger.Info("appearance seed import complete",
			zap.String("source", "default-seed"),
			zap.Int("wallpapers", stats.Wallpapers),
			zap.Int("documents", stats.Documents),
			zap.Int("blobs", stats.Blobs),
		)
	}
	if stats, err := filesService.SeedFreezeEmoji(ctx); err != nil {
		return fmt.Errorf("seed freeze emoji: %w", err)
	} else if stats.Imported {
		logger.Info("built-in freeze emoji seed import complete",
			zap.Int64("document_id", freezeseed.DocumentID),
			zap.Int64("set_id", freezeseed.SetID),
		)
	}
	// Default emoji status system set: synthesized from curated animated_emoji
	// (idempotent, automatically filled in after a restart of an already-seeded
	// database); when missing, Premium users' status picker would be empty.
	if count, created, err := filesService.EnsureDefaultEmojiStatusSet(ctx); err != nil {
		logger.Warn("failed to synthesize the default emoji status system set", zap.Error(err))
	} else if created {
		logger.Info("default emoji status system set synthesized", zap.Int("documents", count))
	}
	langPackStore := postgres.NewLangPackStore(pool)
	passwordStore := postgres.NewPasswordStore(pool)
	helpStore := postgres.NewHelpStore(pool)
	aiComposeStore := postgres.NewAIComposeStore(pool)
	tempAuthKeyStore := postgres.NewTempAuthKeyBindingStore(pool)
	inlineRegistryStore := redisstore.NewInlineRegistryStore(rdb)
	codeStore := redisstore.NewCodeStore(rdb)
	authDeliveryReportService := authdiagnosticsapp.NewService(codeStore, authDeliveryReportStore)
	clientTelemetryService := clienttelemetryapp.NewService(clientTelemetryStore)
	rateLimiter := redisstore.NewRateLimiter(rdb)
	activeSessions := mtprotoedge.NewSessionManager(logger.Named("mtprotoedge").Named("sessions"))
	adminService := adminapp.NewService(adminapp.Dependencies{
		Commands:      adminStore,
		Restrictions:  adminStore,
		OfficialGifts: officialgifts.New(cfg.OfficialGiftsDir),
	})
	userProjectionFacts := userprojection.NewDurableUserProjectionFacts(
		adminService,
		collectiblePhoneStore,
		readModelVersionStore,
		cfg.UserProjectionFactCacheMaxEntries,
	)
	go maintenance.NewRetentionWorker(dispatchOutboxStore, tempAuthKeyStore, logger.Named("maintenance").Named("retention"),
		cfg.UpdateEventRetention,
		cfg.RetentionInterval,
		cfg.RetentionBatch,
	).WithDispatchOutboxPoisonPolicy(cfg.OutboxPoisonRetention, cfg.OutboxPoisonCleanupInterval).
		WithBotAPIUpdateRetention(botAPIUpdateStore, cfg.BotAPIUpdateRetention).
		WithAuthKeySessionLayerRetention(authKeyStore).
		WithLoginCodeDeliveryRetention(messageStore).
		WithClientTelemetryRetention(clientTelemetryStore, 30*24*time.Hour).
		WithAuthDeliveryReportRetention(authDeliveryReportStore, 30*24*time.Hour).
		WithModerationRetention(moderationReportStore).
		WithUserUpdateRetention(updateEventStore).
		WithChannelUpdateRetention(channelStore).
		WithOrphanAuthKeyRetention(authKeyStore, activeSessions, cfg.OrphanAuthKeyRetention).
		Run(ctx)
	go filesapp.NewUploadPartGCWorker(filesService, logger.Named("files").Named("upload_gc"),
		cfg.UploadPartTTL,
		cfg.UploadPartGCInterval,
		cfg.UploadPartGCBatch,
	).Run(ctx)
	langPackService := langpack.NewService(langPackStore, langpack.WithPublicBaseURL(cfg.PublicBaseURL))
	privacyService := privacyapp.NewService(privacyStore, contactStore)
	contactsService := contacts.NewService(contactStore, userStore).Configure(
		contacts.WithPhotoProvider(cachedPhotos),
		contacts.WithPrivacyEvaluator(privacyService),
		contacts.WithAccountFreezeProvider(userProjectionFacts),
		contacts.WithCollectiblePhoneProvider(userProjectionFacts),
		contacts.WithReadModelVersions(readModelVersionStore),
	)
	if seeded, err := langPackService.SeedDirectory(ctx, cfg.LangPackSeedDir); err != nil {
		return fmt.Errorf("seed langpack: %w", err)
	} else if seeded > 0 {
		logger.Info("language pack seed import complete", zap.String("dir", cfg.LangPackSeedDir), zap.Int("strings", seeded))
	}
	// Country dialing code catalog: idempotently upsert the full official set
	// (~235 countries) fixed in the catalog into PG, overwriting the migration's
	// 2-country (US/CN) default seed. Otherwise the non-empty countries table makes
	// ListCountries return those 2 rows and bypass the catalog, so the login page /
	// number format shows only 2 countries. An upsert failure only warns and does not
	// block startup (falling back to the old 2 rows).
	if cs := catalog.Countries().Countries; len(cs) > 0 {
		if err := helpStore.UpsertCountries(ctx, cs); err != nil {
			logger.Warn("country dialing code seed import failed", zap.Error(err))
		} else {
			logger.Info("country dialing code seed import complete", zap.Int("countries", len(cs)))
		}
	}

	botStore := postgres.NewBotStore(pool)
	// userCache is shared with the users service: after a bot metadata write
	// (version bump) the cache must be invalidated, or getUsers returns a stale
	// first_name/bot_info_version within the TTL.
	userCache := redisstore.NewUserCache(rdb, redisstore.DefaultUserCacheTTL)
	accountLifecycleStore := postgres.NewAccountLifecycleStore(pool)
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
		account.WithAccountLifecycle(accountLifecycleStore),
		account.WithTmpPasswords(postgres.NewTmpPasswordStore(pool), authzStore),
		account.WithPublicBaseURL(cfg.PublicBaseURL),
	}
	var webhookSender otpdelivery.Sender
	if cfg.PhoneCodeDeliveryProvider == "webhook" ||
		(cfg.LoginEmailEnable && cfg.EmailCodeDeliveryProvider == "webhook") {
		configured, err := otpwebhook.New(otpwebhook.Config{
			URL:     cfg.OTPWebhookURL,
			Secret:  cfg.OTPWebhookSecret,
			Timeout: cfg.OTPWebhookTimeout,
			Logger:  logger.Named("otp").Named("webhook"),
		})
		if err != nil {
			return fmt.Errorf("configure OTP webhook: %w", err)
		}
		webhookSender = configured
		logger.Info("OTP Webhook delivery enabled",
			zap.Bool("phone", cfg.PhoneCodeDeliveryProvider == "webhook"),
			zap.Bool("email", cfg.LoginEmailEnable && cfg.EmailCodeDeliveryProvider == "webhook"))
	}
	var phoneCodeSender otpdelivery.Sender
	if cfg.PhoneCodeDeliveryProvider == "webhook" {
		phoneCodeSender = webhookSender
		accountOptions = append(accountOptions, account.WithPhoneCodeDelivery(phoneCodeSender, cfg.PhoneCodeLength))
	}
	var loginEmailSender otpdelivery.Sender
	if cfg.LoginEmailEnable {
		switch cfg.EmailCodeDeliveryProvider {
		case "webhook":
			loginEmailSender = webhookSender
		default:
			loginEmailSender = otpsmtp.New(otpsmtp.Config{
				Host:     cfg.SMTPHost,
				Port:     cfg.SMTPPort,
				Username: cfg.SMTPUsername,
				Password: cfg.SMTPPassword,
				From:     cfg.SMTPFrom,
				FromName: cfg.SMTPFromName,
				TLSMode:  cfg.SMTPTLSMode,
				Timeout:  cfg.SMTPTimeout,
			})
		}
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
		botsapp.WithGifCatalog(filesService),
		botsapp.WithUserStickerSets(accountService),
		botsapp.WithTelegramLogin(telegramLoginService),
		botsapp.WithGramsrvOperatorBot(cfg.GramsrvBotChatIDs, adminService),
		botsapp.WithDialogRateLimiter(rateLimiter, cfg.VerificationBotRateLimit, cfg.VerificationBotRateWindow),
		botsapp.WithPublicBaseURL(cfg.PublicBaseURL),
		botsapp.WithBotAPIURL(cfg.BotAPIPublicURL))
	// The built-in ChatBot and StickersBot are seeded with the default product
	// name in their bio (users.about) and description (bots.description). Align
	// them with the active branding on startup so the seeded "telesrv" text is
	// replaced. SetBotInfo writes both fields; the sync is a no-op when the text
	// already matches.
	for _, botID := range []int64{domain.ChatBotUserID, domain.StickersBotUserID} {
		var wantAbout, wantDesc string
		switch botID {
		case domain.ChatBotUserID:
			wantAbout = domain.ChatBotDescription()
			wantDesc = wantAbout
		case domain.StickersBotUserID:
			wantAbout = domain.StickersBotDescription()
			wantDesc = wantAbout
		}
		if _, curAbout, curDesc, err := botsService.GetBotInfo(ctx, botID); err == nil && curAbout == wantAbout && curDesc == wantDesc {
			continue
		}
		if _, err := botsService.SetBotInfo(ctx, botID, domain.BotInfoUpdate{
			SetAbout:       true,
			About:          wantAbout,
			SetDescription: true,
			Description:    wantDesc,
		}); err != nil {
			logger.Warn("sync bot branding", zap.Int64("bot", botID), zap.Error(err))
		}
	}
	groupCallStore := postgres.NewGroupCallStore(pool)
	groupCallsService := groupcallsapp.NewService(groupCallStore, groupcallsapp.WithPublicBaseURL(cfg.PublicBaseURL))
	// Group call media plane: embedded pion SFU (M1+). The SFU's liveness reporter
	// reports media-plane liveness to the signaling-side keepalive watermark (the
	// implementation of the sweeper's dual expiry criteria); when disabled it
	// degrades to signaling only (M0).
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
	// Channel RTMP live media plane (Live Stream): embedded RTMP ingest (OBS
	// streaming) + ffmpeg segmentation. When disabled, signaling still works and
	// viewers stay on the "waiting for stream" placeholder.
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
	// Private call relay (P3): embedded TURN/STUN, with phoneCall.connections
	// delivered via phoneConnectionWebrtc. When disabled it falls back to P1's
	// signaling-only LAN direct connection.
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
	// Server restart recovery: all SFU state is lost, so batch-set every active
	// call's participants to left (version++); clients notice their ssrc disappeared
	// via checkGroupCall and rejoin automatically.
	if calls, err := groupCallsService.ResetAllParticipants(ctx, int(time.Now().Unix())); err != nil {
		logger.Warn("failed to clean up group call participants on restart", zap.Error(err))
	} else if len(calls) > 0 {
		logger.Info("cleaned up group call participants on restart", zap.Int("calls", len(calls)))
	}
	phoneService := phoneapp.NewService(phoneapp.Config{
		RingTimeout:            cfg.CallRingTimeout,
		TombstoneTTL:           cfg.CallTombstoneTTL,
		MaxActivePerUser:       cfg.CallMaxActivePerUser,
		MaxRegistryEntries:     cfg.CallRegistryMaxEntries,
		SignalingRatePerSecond: cfg.CallSignalingRate,
	})
	// Private end-to-end encryption (Secret Chat) handshake state machine + qts delivery queue (blind relay).
	secretChatStore := postgres.NewSecretChatStore(pool)
	encryptedQueueStore := postgres.NewEncryptedQueueStore(pool)
	secretChatService := secretchatapp.NewService(secretChatStore, encryptedQueueStore)
	starsStore := postgres.NewStarsStore(pool)
	starsPurchaseStore := postgres.NewStarsPurchaseStore(pool, messageStore, channelStore)
	starsService := stars.NewService(starsStore,
		stars.WithStartingGrant(cfg.StarsStartingGrant),
		stars.WithPurchaseStore(starsPurchaseStore))
	premiumStore := postgres.NewPremiumStore(pool, messageStore, cfg.PremiumBotUserID)
	if err := premiumStore.EnsurePremiumBotIdentity(ctx, cfg.PremiumBotUsername); err != nil {
		return fmt.Errorf("configure Premium bot: %w", err)
	}
	// Assign embedded avatars to the built-in bots (idempotent). Runs after
	// EnsurePremiumBotIdentity so the configured Premium bot ID exists before
	// the avatar is attached to it. The official system account (777000) is
	// seeded separately below because its avatar follows Server identity.
	if err := botavatars.Seed(ctx, filesService, time.Now().Unix()); err != nil {
		return fmt.Errorf("seed bot avatars: %w", err)
	}
	// The official system account mirrors Server Settings → Server identity:
	// its display name was already set from identityStore above, and here its
	// avatar is seeded from the operator's icon (falling back to the bundled
	// default when none is configured). The watcher then keeps both in sync
	// with later admin-panel edits without a restart, since the panel is a
	// separate process that only writes identity.json.
	seedOfficialAvatar := func(ctx context.Context, icon []byte, now int64) (bool, error) {
		return botavatars.SeedOfficialSystemAvatar(ctx, filesService, icon, now)
	}
	if _, err := systemidentity.Apply(ctx, identityStore, seedOfficialAvatar, time.Now().Unix()); err != nil {
		// Identity is a best-effort deployment branding layer: a malformed or
		// unreadable identity.json must not stop the server from booting.
		logger.Warn("apply server identity failed", zap.Error(err))
	}
	go (&systemidentity.Watcher{
		Store:      identityStore,
		SeedAvatar: seedOfficialAvatar,
		Logger:     logger.Named("server-identity"),
	}).Run(ctx)
	premiumService := premiumapp.NewService(premiumStore, premiumapp.Config{
		BotUserID: cfg.PremiumBotUserID,
		Username:  cfg.PremiumBotUsername,
		Stars:     starsService,
	})
	if err := premiumService.SyncPlans(ctx, cfg.PremiumPlans); err != nil {
		return fmt.Errorf("sync Premium plans: %w", err)
	}
	botsService.SetPremium(premiumService)
	starGiftStore := postgres.NewStarGiftStore(pool)
	starGiftUpgradeStore := postgres.NewStarGiftUpgradeStore(pool, messageStore, postgres.WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: cfg.StarGiftTransferStars, DropOriginalDetailsStars: cfg.StarGiftDropOriginalDetailsStars,
		OfferMinStars:      cfg.StarGiftOfferMinStars,
		ExportDelaySeconds: int(cfg.StarGiftExportDelay / time.Second), TransferDelaySeconds: int(cfg.StarGiftTransferDelay / time.Second),
		ResellDelaySeconds: int(cfg.StarGiftResellDelay / time.Second), CraftDelaySeconds: int(cfg.StarGiftCraftDelay / time.Second),
		CraftChancePermille: cfg.StarGiftCraftChancePermille,
	}))
	starGiftLifecycleStore := postgres.NewStarGiftLifecycleStore(pool, messageStore, cfg.StarGiftTONStartingGrant,
		postgres.WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
			StarsProceedsPermille: cfg.StarGiftStarsProceedsPermille,
			TONProceedsPermille:   cfg.StarGiftTONProceedsPermille,
		}), postgres.WithStarGiftCrossCraftGenerator(newCrossCraftGenerator(cfg, blobBackend, logger)),
		postgres.WithStarGiftCrossCraftChance(cfg.StarGiftCraftChancePermille),
		postgres.WithStarGiftCrossCraftUsers(cfg.StarGiftCrossCraftUserIDs),
		postgres.WithStarGiftCrossCraftBlobReader(blobBackend))
	var starGiftWithdrawalOption stargifts.Option
	if cfg.CustomFragmentEnabled {
		starGiftWithdrawalProvider, providerErr := stargifts.NewCustomFragmentWithdrawalProvider(cfg.CustomFragmentPublicBaseURL)
		if providerErr != nil {
			return fmt.Errorf("init CustomFragment star gift withdrawal provider: %w", providerErr)
		}
		starGiftWithdrawalOption = stargifts.WithWithdrawalProvider(starGiftWithdrawalProvider)
	} else {
		starGiftWithdrawalOption, err = localStarGiftWithdrawalOption(cfg.PublicBaseURL, cfg.PublicLinkWebAddr, logger)
	}
	if err != nil {
		return fmt.Errorf("init local star gift withdrawal provider: %w", err)
	}
	starGiftOptions := []stargifts.Option{
		stargifts.WithUpgradeStore(starGiftUpgradeStore),
		stargifts.WithLifecycleStore(starGiftLifecycleStore),
	}
	if starGiftWithdrawalOption != nil {
		starGiftOptions = append(starGiftOptions, starGiftWithdrawalOption)
	}
	giftsService := stargifts.NewService(starGiftStore, blobBackend, cfg.DC, starGiftOptions...)
	var customFragmentService *customfragment.Service
	if cfg.CustomFragmentEnabled {
		customFragmentService, err = customfragment.New(customfragment.Config{
			PublicBaseURL: cfg.CustomFragmentPublicBaseURL, AppName: cfg.PublicAppName,
			SigningKeyFile:      cfg.CustomFragmentSigningKeyFile,
			GiftCollection:      cfg.CustomFragmentGiftCollection,
			CollectionName:      cfg.CustomFragmentCollectionName,
			MintAmountNanoton:   cfg.CustomFragmentMintAmountNanoton,
			SubwalletID:         uint32(cfg.CustomFragmentSubwalletID),
			AuthorizationTTL:    cfg.CustomFragmentAuthorizationTTL,
			LiteserverConfigURL: cfg.CustomFragmentLiteserverConfigURL,
			Limiter:             rateLimiter,
			APIRateLimit:        cfg.ClaimAPIRateLimit,
			APIRateWindow:       cfg.ClaimAPIRateWindow,
		}, giftsService, nil, logger.Named("custom-fragment"))
		if err != nil {
			return fmt.Errorf("init CustomFragment: %w", err)
		}
		defer customFragmentService.Close()
		logger.Info("CustomFragment TON mainnet gift withdrawal enabled",
			zap.String("collection", cfg.CustomFragmentGiftCollection),
			zap.String("signing_public_key", customFragmentService.PublicKeyHex()))
	}
	var giftClaimService *giftclaim.Service
	var giftClaimTestService *giftclaim.Service
	claimAPIRateLimit, claimAPIRateWindow := cfg.ClaimAPIRateLimit, cfg.ClaimAPIRateWindow
	claimWithdrawRateLimit, claimWithdrawRateWindow := cfg.ClaimWithdrawRateLimit, cfg.ClaimWithdrawRateWindow
	wireGiftClaim := func(cfg giftClaimInstanceConfig) (*giftclaim.Service, error) {
		if !cfg.Enabled {
			return nil, nil
		}
		_, botSecret, _ := domain.ParseBotToken(cfg.BotToken)
		if err := postgres.EnsureSystemUser(ctx, pool, cfg.SystemUser); err != nil {
			return nil, fmt.Errorf("materialize %s identity: %w", cfg.BotHandle, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO bots (bot_user_id, owner_user_id, token_secret) VALUES ($1, $1, $2) ON CONFLICT (bot_user_id) DO UPDATE SET token_secret=EXCLUDED.token_secret, updated_at=now()`, cfg.BotUserID, botSecret); err != nil {
			return nil, fmt.Errorf("configure %s token: %w", cfg.BotHandle, err)
		}
		if _, err := botsService.SetBotMenuButton(ctx, cfg.BotUserID, domain.BotMenuButton{
			Type: domain.BotMenuButtonWebView, Text: "Claim NFT", URL: strings.TrimRight(cfg.PublicBaseURL, "/") + cfg.BasePath,
		}); err != nil {
			menuErr := fmt.Errorf("configure %s Mini App: %w", cfg.BotHandle, err)
			if !cfg.MenuButtonRequired {
				logger.Warn("claim test instance skips menu button setup", zap.Error(menuErr))
			} else {
				return nil, menuErr
			}
		}
		claimVerifier := customfragment.NewMainnetVerifier(cfg.LiteserverConfigURL)
		svc, err := giftclaim.New(giftclaim.Config{
			PublicBaseURL: cfg.PublicBaseURL, BotToken: cfg.BotToken, BotUserID: cfg.BotUserID, BasePath: cfg.BasePath,
			Collection: cfg.Collection, AppName: cfg.AppName,
			ChallengeTTL: cfg.ChallengeTTL, ProofTTL: cfg.ProofTTL, InitDataTTL: cfg.InitDataTTL,
			Minter:       cfg.Minter,
			Withdrawer:   cfg.Withdrawer,
			Password:     cfg.Password,
			Limiter:      rateLimiter,
			APIRateLimit: claimAPIRateLimit, APIRateWindow: claimAPIRateWindow,
			WithdrawRateLimit: claimWithdrawRateLimit, WithdrawRateWindow: claimWithdrawRateWindow,
		}, postgres.NewStarGiftClaimStore(pool), claimVerifier, logger.Named("gift-claim"))
		if err != nil {
			claimVerifier.Close()
			return nil, fmt.Errorf("init %s gift claim Mini App: %w", cfg.BotHandle, err)
		}
		go svc.RunOwnershipSync(ctx)
		return svc, nil
	}
	if cfg.StarGiftClaimEnabled || cfg.StarGiftClaimTestEnabled {
		giftClaimService, err = wireGiftClaim(giftClaimInstanceConfig{
			Enabled: cfg.StarGiftClaimEnabled, BotUserID: domain.GiftClaimBotUserID,
			SystemUser: domain.GiftClaimBotUser(), BotHandle: "@claim", BotToken: cfg.StarGiftClaimBotToken,
			PublicBaseURL: cfg.StarGiftClaimPublicBaseURL, BasePath: "/claim",
			Collection: cfg.CustomFragmentGiftCollection, AppName: cfg.CustomFragmentCollectionName,
			LiteserverConfigURL: cfg.CustomFragmentLiteserverConfigURL,
			ChallengeTTL:        cfg.StarGiftClaimChallengeTTL, ProofTTL: cfg.StarGiftClaimProofTTL, InitDataTTL: cfg.StarGiftClaimInitDataTTL,
			Minter:             newGiftClaimMinter(customFragmentService),
			Withdrawer:         giftsService,
			Password:           accountService,
			MenuButtonRequired: true,
		})
		if err != nil {
			logger.Error("wire @claim gift claim failed", zap.Error(err))
			return err
		}
		if giftClaimService != nil {
			defer giftClaimService.Close()
			logger.Info("TON Proof gift claim enabled", zap.String("url", cfg.StarGiftClaimPublicBaseURL), zap.String("bot", "@claim"))
		}
		giftClaimTestService, err = wireGiftClaim(giftClaimInstanceConfig{
			Enabled: cfg.StarGiftClaimTestEnabled, BotUserID: domain.GiftClaimTestBotUserID,
			SystemUser: domain.GiftClaimTestBotUser(), BotHandle: "@claimtest", BotToken: cfg.StarGiftClaimTestBotToken,
			PublicBaseURL: cfg.StarGiftClaimTestPublicBaseURL, BasePath: "/claimtest",
			Collection: cfg.CustomFragmentGiftCollection, AppName: cfg.CustomFragmentCollectionName,
			LiteserverConfigURL: cfg.CustomFragmentLiteserverConfigURL,
			ChallengeTTL:        cfg.StarGiftClaimChallengeTTL, ProofTTL: cfg.StarGiftClaimProofTTL, InitDataTTL: cfg.StarGiftClaimInitDataTTL,
			Minter:             newGiftClaimMinter(customFragmentService),
			Withdrawer:         giftsService,
			Password:           accountService,
			MenuButtonRequired: false,
		})
		if err != nil {
			logger.Error("wire @claimtest gift claim failed", zap.Error(err))
			return err
		}
		if giftClaimTestService != nil {
			defer giftClaimTestService.Close()
			logger.Info("TON Proof gift claim (test) enabled", zap.String("url", cfg.StarGiftClaimTestPublicBaseURL), zap.String("bot", "@claimtest"))
		}
	}
	// Crypto donations (deposit -> Stars): OFF unless TELESRV_DONATIONS_ENABLED
	// says otherwise.
	//
	// It used to be on by default, which was wrong twice over. The wallet was
	// provisioned on first boot of every deployment -- a real custodial BIP39
	// mnemonic, a data/donation_wallet.key, and a warning about backing it up,
	// created for a feature the operator had not asked for and could not use.
	// And the seeded registry enabled the local Ganache chain, so every boot
	// then dialled 127.0.0.1:7545, failed, and logged an ERROR for a chain
	// that does not exist outside a developer's laptop.
	//
	// The service object is still constructed and still handed to the bot and
	// the admin service, because both already treat it as optional: the bot's
	// /deposit answers from a nil source, and the admin Donations page renders
	// "not provisioned" against an empty chain list rather than erroring. That
	// is deliberate -- an operator who has not enabled the feature can still
	// read what it would take, without the server having quietly taken custody
	// of anything.
	donationStore := postgres.NewDonationStore(pool)
	donationsService, donationsDeps := (*donationsapp.Service)(nil), adminapp.DonationsService(nil)
	if cfg.DonationsEnabled {
		donationWalletKey, err := donationsWalletKey(cfg)
		if err != nil {
			return fmt.Errorf("init donations wallet key: %w", err)
		}
		donationsService, err = donationsapp.NewService(ctx, donationStore, donationWalletKey)
		if err != nil {
			return fmt.Errorf("init donations service: %w", err)
		}
		if !donationsService.Ready() {
			if _, err := donationsService.EnsureWallet(ctx); err != nil {
				return fmt.Errorf("provision donations wallet: %w", err)
			}
			logger.Warn("generated a new crypto donations wallet -- recovery phrase stored encrypted, back up data/donation_wallet.key")
		}
		botsService.SetDonationsSource(donationsService)
		donationsService.SetNotifier(botsService)
		donationsDeps = donationsService
	} else {
		logger.Info("crypto donations disabled; set TELESRV_DONATIONS_ENABLED=true and enable a chain to turn it on")
	}
	// Passkey: credentials are persisted in postgres; one-time challenges live in
	// in-process memory (short TTL, the same kind of in-process one-time credential
	// as the QR login token, not shared across instances).
	passkeyStore := postgres.NewPasskeyStore(pool)
	passkeyChallengeStore := memory.NewPasskeyChallengeStore()
	passkeyService := passkeyapp.NewService(passkeyStore, passkeyChallengeStore, cfg.PasskeyRPID, cfg.DC,
		passkeyapp.WithAllowedOrigins(cfg.PasskeyAllowedOrigins))
	// Custom cloud themes (Create a New Theme): both the theme catalog and each user's installed list are persisted in postgres.
	themeService := themesapp.NewService(postgres.NewThemeStore(pool))
	usersService := users.NewService(userStore,
		users.WithBaseUserCache(userCache),
		users.WithContactStore(contactStore),
		users.WithPhotoProvider(cachedPhotos),
		users.WithPrivacyEvaluator(privacyService),
		users.WithAccountFreezeProvider(userProjectionFacts),
		users.WithCollectiblePhoneStore(collectiblePhoneStore),
		users.WithCollectiblePhoneProvider(userProjectionFacts),
	)
	privacyService.ConfigureReadModels(usersService, channelStore)
	aiComposeService := aiapp.NewService(aiComposeStore, newAIComposeOptions(cfg, rateLimiter, usersService.PremiumActive, logger)...)
	botsService.SetAIChatGenerator(aiComposeService)
	dialogsService := dialogs.NewService(dialogStore, channelStore).Configure(
		dialogs.WithContactStore(contactStore),
		dialogs.WithPhotoProvider(cachedPhotos),
		dialogs.WithPrivacyEvaluator(privacyService),
		dialogs.WithAccountFreezeProvider(userProjectionFacts),
		dialogs.WithCollectiblePhoneProvider(userProjectionFacts),
		dialogs.WithPremiumChecker(usersService.PremiumActive),
		dialogs.WithReadModelVersions(readModelVersionStore),
		dialogs.WithDialogHydrationCaches(
			cfg.DialogPrivatePeerCacheMaxEntries,
			cfg.DialogPrivatePeerCacheMaxBytes,
			cfg.DialogDraftCacheMaxEntries,
			cfg.DialogDraftCacheMaxBytes,
		),
		dialogs.WithDialogListSnapshotCache(
			cfg.DialogListSnapshotCacheMaxEntries,
			cfg.DialogListSnapshotCacheMaxHeaders,
			cfg.DialogListSnapshotCacheTTL,
		),
		dialogs.WithSharedDialogListSnapshotCache(dialogListSnapshotCache),
	)
	// Compile-time guarantee that *users.Service satisfies the optional capability
	// for channel fan-out cross-viewer projection warm-up; a signature drift breaks
	// the build right here rather than silently degrading at runtime to O(viewer)
	// per-viewer projection.
	var _ rpc.BatchViewerUsersResolver = usersService
	channelsService := channelapp.NewService(channelStore,
		channelapp.WithBotProfileResolver(botsService),
		channelapp.WithReadModelVersions(readModelVersionStore),
		channelapp.WithActiveChannelIDsReadModel(
			activeChannelIDsPageCache,
			activeChannelIDsPageBatcher,
			cfg.ActiveChannelIDsCacheMaxEntries,
			cfg.ActiveChannelIDsCacheTTL,
			metricRegistry,
		),
		channelapp.WithSendPermissionChecker(adminService),
	)
	communitiesService := communitiesapp.NewService(communityStore)
	ephemeralService := ephemeralapp.NewService(ephemeralStore, channelsService, usersService, botsService)
	welcomeMessageService := welcomemessagesapp.NewService(welcomeMessageStore, channelsService)
	transcriptionService := transcriptionapp.NewService(
		messageTranscriptionStore,
		filesService,
		cfg.ASRURL,
		cfg.ASRTimeout,
		logger.Named("transcription"),
	)
	storiesService := storiesapp.NewService(storyStore, storiesapp.WithChannelStoryAccess(channelsService))
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
		messageapp.WithAccountFreezeProvider(userProjectionFacts),
		messageapp.WithCollectiblePhoneProvider(userProjectionFacts),
		messageapp.WithReadModelVersions(readModelVersionStore),
		messageapp.WithBotResponder(botsService),
		messageapp.WithSendPermissionChecker(adminService),
		// The recipient probe only needs the unprojected row, so the users
		// service reads the tombstone without viewer projection.
		messageapp.WithRecipientUserReader(userStore),
		messageapp.WithBusinessAutomation(passwordStore, businessAutomationOptions...),
	)
	moderationService := moderationapp.NewService(
		moderationReportStore,
		moderationapp.WithMessageReaders(messagesService, channelsService),
		moderationapp.WithStoryReader(storiesService),
		moderationapp.WithPeerReaders(usersService, channelsService),
		moderationapp.WithProfilePhotoReader(filesService),
	)
	legacyReportsMigrated, err := moderationService.MigrateLegacyEphemeralReports(ctx, ephemeralReportStore, 500)
	if err != nil {
		return fmt.Errorf("migrate legacy ephemeral reports: %w", err)
	}
	if legacyReportsMigrated > 0 {
		logger.Info("legacy ephemeral reports migrated to the unified moderation pipeline",
			zap.Int("reports", legacyReportsMigrated))
	}
	translationService := translationapp.NewService(
		messagesService,
		channelsService,
		dialogStore,
		newTranslationOptions(cfg, rateLimiter, logger)...,
	)
	authService := auth.NewService(userStore, authzStore, codeStore, authKeyGetBatchStore, tempAuthKeyStore, cfg.DevAuthCode,
		auth.WithLoginMessages(messageStore, dialogStore),
		auth.WithLoginCodeDelivery(messageStore),
		auth.WithLoginWelcomeMessages(identityStore, cfg.WelcomeMessagePhoneTemplate, cfg.WelcomeMessageEmailTemplate),
		auth.WithLoginCodeMessageTemplate(identityStore, cfg.LoginCodeMessageTemplate),
		auth.WithLoginCodeMessageTemplateResolver(func() (string, error) {
			info, err := identityStore.Get()
			if err != nil {
				return "", err
			}
			return domain.ResolveLoginCodeMessageTemplate(info.LoginCodeMessageTemplate, cfg.LoginCodeMessageTemplate), nil
		}),
		auth.WithPasswords(passwordStore),
		auth.WithBotLogin(botStore),
		auth.WithPremiumGrant(cfg.PremiumGrantMonths),
		auth.WithCodeTTL(cfg.AuthCodeTTL),
		auth.WithCodeMaxAttempts(cfg.AuthCodeMaxAttempts),
		auth.WithPhoneCodeDelivery(phoneCodeSender, cfg.PhoneCodeLength),
		auth.WithOTPDeliveryFailureObserver(func(_ context.Context, request otpdelivery.Request, err error) {
			logger.Named("otp").Warn("additional OTP provider delivery failed; the 777000 App-code remains valid",
				zap.String("delivery_id", request.DeliveryID),
				zap.String("purpose", string(request.Purpose)),
				zap.String("channel", string(request.Channel)),
				zap.Error(err))
		}),
		auth.WithLoginEmail(auth.LoginEmailOptions{
			Enabled:      cfg.LoginEmailEnable,
			RequireSetup: cfg.LoginEmailRequireSetup,
			CodeLength:   cfg.LoginEmailCodeLength,
			Store:        accountService,
			Sender:       loginEmailSender,
		}))
	// Collectible (NFT) usernames and the gramsrv composite account rating are
	// optional read models projected at the protocol edge. The rating worker
	// computes and persists scores; profile reads never recompute them.
	collectibleUsernameStore := postgres.NewCollectibleUsernameStore(pool)
	accountRatingStore := postgres.NewAccountRatingStore(pool)
	usernamesService := usernamesapp.NewService(
		usernamesapp.WithRegistryStore(collectibleUsernameStore),
		usernamesapp.WithCollectibleStore(collectibleUsernameStore),
		usernamesapp.WithURLTemplate(cfg.CollectibleUsernameURLTemplate),
		usernamesapp.WithPublicBaseURL(cfg.PublicBaseURL),
		usernamesapp.WithLogger(logger.Named("app").Named("usernames")),
	)
	ratingService := ratingapp.NewService(
		ratingapp.WithStore(accountRatingStore),
		ratingapp.WithEnabled(cfg.RatingEnabled),
		ratingapp.WithWeights(cfg.AccountRatingWeights()),
		ratingapp.WithPendingDelay(cfg.RatingPendingDelay),
		ratingapp.WithStaleAfter(cfg.RatingStaleAfter),
		ratingapp.WithLogger(logger.Named("app").Named("rating")),
	)
	// Official platform verification: applications are filed through the built-in
	// @verifybot and decided in the admin panel. Every eligibility rule lives in
	// this service; the bot and the panel are only its two surfaces.
	verificationStore := postgres.NewVerificationStore(pool)
	verificationLogger := logger.Named("app").Named("verification")
	verificationService := verificationapp.NewService(
		verificationapp.WithStore(verificationStore),
		verificationapp.WithUserDirectory(usersService),
		verificationapp.WithBotDirectory(botsService),
		verificationapp.WithChannelDirectory(channelsService),
		verificationapp.WithAccountFreezeProvider(adminService),
		verificationapp.WithPeerVerifier(verificationPeerVerifier{
			users:           usersService,
			channels:        channelsService,
			channelRowCache: channelRowCache,
		}),
		verificationapp.WithRateLimiter(rateLimiter, cfg.VerificationApplyRateLimit, cfg.VerificationApplyRateWindow),
		verificationapp.WithEnabled(cfg.VerificationEnabled),
		verificationapp.WithAllowUserTargets(cfg.VerificationAllowUserTargets),
		verificationapp.WithRejectCooldown(cfg.VerificationRejectCooldown),
		verificationapp.WithMaxActivePerUser(cfg.VerificationMaxActivePerUser),
		verificationapp.WithLogger(verificationLogger),
	)
	// @verifybot is the applicant surface, and the notifier that carries decisions
	// back to the applicant as ordinary messages. Both directions are deferred
	// injections because the bots service is built before the peer directories the
	// verification service needs.
	botsService.SetVerification(verificationService)
	verificationService.SetApplicantNotifier(botsService)
	// Third-party verification is a SEPARATE mechanism: a verifier bot marks peers
	// with its own custom-emoji icon and description, which clients render before the
	// name. It shares no state with the official badge above -- different tables,
	// different rights, different TL fields (bot_verification_icon / bot_verification
	// versus verified).
	botVerificationStore := postgres.NewBotVerificationStore(pool)
	botVerificationService := botverificationapp.NewService(
		botverificationapp.WithStore(botVerificationStore),
		botverificationapp.WithUserDirectory(usersService),
		botverificationapp.WithBotDirectory(botsService),
		botverificationapp.WithChannelDirectory(channelsService),
		// The icon must be a real custom emoji document: an id no client can fetch
		// renders as nothing, so the badge would be silently invisible.
		botverificationapp.WithIconResolver(filesService),
		botverificationapp.WithMarkApplier(botVerificationMarkApplier{store: botVerificationStore}),
		botverificationapp.WithRateLimiter(rateLimiter, cfg.BotVerificationRequestRateLimit, cfg.BotVerificationRequestRateWindow),
		botverificationapp.WithEnabled(cfg.BotVerificationEnabled),
		botverificationapp.WithMaxPerVerifier(cfg.BotVerificationMaxPerVerifier),
		botverificationapp.WithLogger(logger.Named("app").Named("botverification")),
	)
	// @verifierbot files applications with the operator and reports decisions back.
	botsService.SetCustomVerification(botVerificationService)
	botVerificationService.SetApplicantNotifier(botsService)
	updatesService := updates.NewService(updateStateStore, updateEventStore, updates.WithLogger(logger.Named("app").Named("updates")))
	var appUpdateResolver updatecdn.Resolver
	if cfg.UpdateServiceURL != "" {
		client, err := updatecdn.NewClient(cfg.UpdateServiceURL, cfg.UpdateRequestTimeout)
		if err != nil {
			return fmt.Errorf("initialize update service client: %w", err)
		}
		appUpdateResolver = client
	}
	// Session geolocation (country/region for account.getAuthorizations). The
	// protocol layer has no peer implementation of help.requestIpAddress, so when no
	// endpoint is configured the resolver is nil and the list keeps returning the
	// "Unknown" placeholder -- that is the expected path when disabled and must not
	// prevent the service from starting.
	geoIPResolver, err := geoip.New(geoip.Config{
		Endpoints:          cfg.GeoIPEndpoints,
		Timeout:            cfg.GeoIPTimeout,
		Concurrency:        cfg.GeoIPConcurrency,
		CacheTTL:           cfg.GeoIPCacheTTL,
		NegativeTTL:        cfg.GeoIPNegativeTTL,
		CacheSize:          cfg.GeoIPCacheSize,
		RateLimitThreshold: cfg.GeoIPRateLimitThreshold,
		RateLimitCooldown:  cfg.GeoIPRateLimitCooldown,
		DownThreshold:      cfg.GeoIPDownThreshold,
		DownCooldown:       cfg.GeoIPDownCooldown,
	}, logger.Named("geoip"))
	if err != nil {
		return fmt.Errorf("init geoip resolver: %w", err)
	}
	if geoIPResolver != nil {
		defer func() { _ = geoIPResolver.Close() }()
		// The failover chain is ordered and printed at startup, so when diagnosing
		// "why does this session show Unknown" you can see directly which backend was
		// primary at the time.
		logger.Info("session geolocation enabled",
			zap.Strings("backends", geoip.EndpointNames(cfg.GeoIPEndpoints)))
	} else {
		logger.Info("session geolocation disabled: account.getAuthorizations keeps returning the Unknown placeholder")
	}
	// Layer 230 in-app TON wallet. Enabled only by explicit config: challenge/binding
	// state lives only in local data/wallet, and the balance comes from telesrv's own
	// TON ledger. The provider (toncenter) is unconfigured by default, so neither
	// toncenter.* nor balance queries contact any external TON node.
	var walletService rpc.WalletService
	if cfg.WalletStateDir != "" {
		walletStore, err := walletapp.NewFileStore(cfg.WalletStateDir)
		if err != nil {
			return fmt.Errorf("open wallet state directory: %w", err)
		}
		defer func() { _ = walletStore.Close() }()
		var walletProvider walletapp.Provider
		if cfg.WalletToncenterAPIKey != "" {
			walletProvider = walletapp.NewToncenter(cfg.WalletToncenterAPIKey)
		}
		// When the master key is missing, a 0600 file is generated automatically;
		// without it telesrv cannot custody wallets it generates itself, and
		// inputWalletNew is explicitly rejected rather than writing a plaintext key.
		walletKeys, err := walletapp.LoadOrCreateKeyCipher(cfg.WalletKeyFile)
		if err != nil {
			return fmt.Errorf("load wallet master key: %w", err)
		}
		if err := walletStore.ValidateKeyCipher(walletKeys); err != nil {
			return fmt.Errorf("validate wallet custody keys: %w", err)
		}
		walletService = walletapp.NewService(walletStore, walletProvider, cfg.AdvertiseIP,
			walletapp.WithBalanceSource(walletapp.TONLedgerBalanceSource(starGiftLifecycleStore.TonBalance)),
			walletapp.WithTransactionSource(walletapp.TONLedgerTransactionSource(starGiftLifecycleStore.TonTransactions)),
			walletapp.WithKeyCipher(walletKeys),
			walletapp.WithInternalTransfers(cfg.WalletInternalTransfers, postgres.NewWalletTransferStore(pool, messageStore, cfg.StarGiftTONStartingGrant)))
		logger.Info("in-app TON wallet enabled",
			zap.String("state_dir", cfg.WalletStateDir),
			zap.Bool("ton_provider", walletProvider != nil))
	} else {
		logger.Info("in-app TON wallet disabled: wallet.* returns WALLET_UNAVAILABLE")
	}
	router := rpc.New(rpc.Config{
		DC:                       cfg.DC,
		DefaultCountryCode:       cfg.DefaultCountryCode,
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
		SRPRateLimit:             cfg.SRPRateLimit,
		SRPRateWindow:            cfg.SRPRateWindow,
		WithdrawalRateLimit:      cfg.WithdrawalRateLimit,
		WithdrawalRateWindow:     cfg.WithdrawalRateWindow,
		PaymentRateLimit:         cfg.PaymentRateLimit,
		PaymentRateWindow:        cfg.PaymentRateWindow,
		ChannelNudgeMaxTargets:   cfg.ChannelNudgeMaxTargets,
		CallSignalingMaxBytes:    cfg.CallSignalingMaxBytes,
		CallForceRelay:           cfg.CallForceRelay,
		GroupCallMaxParticipants: cfg.GroupCallMaxParticipants,
		RtmpIngestURL:            cfg.LiveStreamRtmpURL,
		PublicBaseURL:            cfg.PublicBaseURL,
		UpdatePublicURL:          cfg.UpdatePublicURL,
		PublicAppScheme:          cfg.PublicAppScheme,
		PublicAppLinkBase:        cfg.PublicAppLinkBase,
		AllowDevPayments:         cfg.AllowDevPayments,
		// PFS temp→perm resolution cache: an explicit revocation clears the cache and
		// drops the connection, so a re-bind invalidates immediately; the configured
		// TTL only serves as a fallback for cross-process/abnormal invalidation,
		// avoiding a large connection count periodically saturating PG.
		TempKeyResolveCacheTTL:         cfg.TempKeyResolveCacheTTL,
		TempKeyResolveCacheMaxEntries:  cfg.TempKeyResolveCacheMaxEntries,
		PeerIdentityCacheMaxEntries:    cfg.PeerIdentityCacheMaxEntries,
		StoryActivePeerCacheMaxEntries: cfg.StoryActivePeerCacheMaxEntries,
		StoryHiddenListCacheMaxEntries: cfg.StoryHiddenListCacheMaxEntries,
		StoryHiddenListCacheMaxBytes:   cfg.StoryHiddenListCacheMaxBytes,
		PresenceLastSeenBatchMax:       cfg.PresenceLastSeenBatchMax,
		PresenceLastSeenBatchWait:      cfg.PresenceLastSeenBatchWait,
		PresenceLastSeenBatchQueue:     cfg.PresenceLastSeenBatchQueue,
		PresenceLastSeenBatchTimeout:   cfg.PresenceLastSeenBatchTimeout,
		PresenceLastSeenDrainTimeout:   cfg.PresenceLastSeenDrainTimeout,
	}, rpc.Deps{
		Auth:                 authService,
		AuthDeliveryReports:  authDeliveryReportService,
		ClientTelemetry:      clientTelemetryService,
		AuthKeySessionLayers: authKeySessionLayerStore,
		ReadModelVersions:    readModelVersionStore,
		UserProjectionFacts:  userProjectionFacts,
		Account:              accountService,
		Privacy:              privacyService,
		Help: help.NewService(helpStore, helpStore,
			help.WithMapboxToken(cfg.MapboxToken),
			help.WithPremiumBotUsername(cfg.PremiumBotUsername),
			// The wallet requires an explicit state directory; an empty directory is how an
			// operator turns it off, and that is exactly when a client must be told
			// the wallet is unavailable rather than shown an entry that cannot work.
			help.WithWalletAvailable(walletService != nil),
			help.WithInternalWalletTransfers(cfg.WalletInternalTransfers),
			help.WithAccountFreezeProvider(adminService)),
		AppUpdates:                   appUpdateResolver,
		AccountFreeze:                userProjectionFacts,
		AccountFreezeNotifications:   adminService,
		AccountDeletionNotifications: accountLifecycleStore,
		AICompose:                    aiComposeService,
		Ephemeral:                    ephemeralService,
		EphemeralPush:                ephemeralStore,
		WelcomeMessages:              welcomeMessageService,
		Moderation:                   moderationService,
		Users:                        usersService,
		Usernames:                    usernamesService,
		CollectiblePhones:            collectiblePhoneStore,
		AccountRatings:               ratingService,
		BotVerifications:             botVerificationService,
		TelegramLogin:                telegramLoginRPCDependency(telegramLoginService),
		Updates:                      updatesService,
		BootstrapUpdates:             bootstrapUpdateStore,
		BotAPIUpdates:                botAPIUpdateStore,
		BotCallbacks:                 botCallbackStore,
		Contacts:                     contactsService,
		Dialogs:                      dialogsService,
		Chatlists:                    chatlistsService,
		Messages:                     messagesService,
		Translation:                  translationService,
		Transcriptions:               transcriptionService,
		Channels:                     channelsService,
		Communities:                  communitiesService,
		Files:                        filesService,
		PremiumPromo:                 filesService,
		Bots:                         botsService,
		ServiceBotCallbacks:          botsService,
		ServiceBotInlineResults:      botsService,
		Polls:                        pollsapp.NewService(pollStore),
		Stories:                      storiesService,
		Phone:                        phoneService,
		SecretChats:                  secretChatService,
		Stars:                        starsService,
		Premium:                      premiumService,
		Gifts:                        giftsService,
		Wallet:                       walletService,
		MainTabs:                     postgres.NewPeerMainTabStore(pool),
		Passkey:                      passkeyService,
		Themes:                       themeService,
		GeoIP:                        geoIPResolver,
		GroupCalls:                   groupCallsService,
		LiveStreams:                  liveStreamDep(liveStreamService),
		SFU:                          sfuService,
		TURN:                         turnService,
		LangPack:                     langPackService,
		Sessions:                     activeSessions,
		Metrics:                      metricRegistry,
		Inline:                       inlineRegistryStore,
		Limiter:                      rateLimiter,
	}, logger.Named("rpc"), clock.System)
	readModelListener := postgres.NewReadModelChangeListener(cfg.PostgresDSN, postgres.ReadModelCacheSet{
		ReadModelVersions:   readModelVersionStore,
		ChannelRows:         channelRowCache,
		ChannelTopMessages:  channelTopMessageCache,
		CommunityCatalog:    communityCatalogCache,
		ChannelMembers:      channelMemberCache,
		ChannelDialogs:      channelDialogCache,
		ChannelDifferences:  channelDifferenceCache,
		ChannelBoosts:       channelBoostCache,
		Contacts:            postgres.ContactReadModelCaches{contactStore, contactsService},
		Dialogs:             dialogsService,
		Privacy:             privacyService,
		ProfilePhotos:       cachedPhotos,
		Stories:             router,
		ChannelFullBots:     router,
		ChannelBotMembers:   channelsService,
		ChannelMediaCounts:  channelsService,
		PrivateMediaCounts:  messagesService,
		RPCProjections:      router,
		PeerIdentities:      router,
		BaseUsers:           userCache,
		BotProfiles:         botsService,
		StarGifts:           giftsService,
		AccountSettings:     router,
		UserProjectionFacts: userProjectionFacts,
	}, logger.Named("store").Named("read-model-listener"))
	go readModelListener.Run(ctx)
	activeSessions.SetLifecycleObserver(router)
	adminService.Configure(adminapp.Dependencies{
		Auth:                    authService,
		Revoker:                 router,
		Users:                   usersService,
		UserLookup:              userStore,
		Account:                 accountService,
		Photos:                  filesService,
		Donations:               donationsDeps,
		Stars:                   starsService,
		Premium:                 premiumService,
		StarsNotifier:           router,
		UserNotifier:            router,
		UserModerationNotifier:  router,
		FreezeNotifier:          router,
		AccountDeletion:         accountLifecycleStore,
		AccountDeletionNotifier: router,
		Channels:                channelsService,
		ChannelNotifier:         router,
		Messages:                messagesService,
		Gifts:                   giftsService,
		GiftGranter:             router,
		Bots:                    botsService,
		Broadcast:               broadcastService,
		Emoji:                   filesService,
		StickerSets:             filesService,
		GifCatalog:              filesService,
		Moderation:              moderationService,
		Usernames:               usernamesService,
		CollectiblePhones:       collectiblePhoneStore,
		Rating:                  ratingService,
		Verification:            verificationService,
		BotVerification:         botVerificationService,
		ItemPrices:              postgres.NewItemPriceStore(pool),
		UniqueGifts:             postgres.NewStarGiftClaimStore(pool),
		TonDNS:                  tondns.New(cfg.CustomFragmentLiteserverConfigURL),
		UsernameWallets:         postgres.NewStarGiftClaimStore(pool),
	})
	// The RPC edge owns the tg.* projection cache and the standard non-PTS
	// updateUser/updateChannel refresh, so committed registry mutations are
	// visible to online viewers immediately.
	usernamesService.SetPeerUsernameNotifier(router)
	// The badge change is a peer fact the protocol edge caches and pushes, so the
	// verification service gets the same hook the username registry uses. The
	// assertion is deliberately dynamic: NotifyPeerVerified lands with the edge
	// agent, and until then only projection invalidation is wired — a decision can
	// then never be masked by a stale projection, and clients converge on their next
	// authoritative peer read.
	if notifier, ok := any(router).(verificationapp.PeerNotifier); ok {
		// Compose rather than choose: the decision writes users.verified inside the
		// verification transaction (through postgres.VerificationTxFromContext), so it
		// bypasses users.Service and its cache refresh. Dropping the shared user:base
		// entry before the edge builds the pushed tg.User is what keeps the badge in
		// that push from being one beat stale; the cross-instance read-model listener
		// would otherwise only catch up asynchronously.
		verificationService.SetPeerNotifier(compositeVerificationNotifier{
			cache: rpcProjectionVerificationNotifier{
				invalidator: router,
				users:       userCache,
				log:         verificationLogger,
			},
			edge: notifier,
		})
	} else {
		verificationService.SetPeerNotifier(rpcProjectionVerificationNotifier{
			invalidator: router,
			users:       userCache,
			log:         verificationLogger,
		})
		logger.Warn("verification badge update push is not implemented by the RPC edge; only projection invalidation is wired",
			zap.String("expected_hook", "rpc.Router.NotifyPeerVerified"))
	}
	// The third-party mark lives on the same peer projections as the official flag,
	// so it needs the same edge hook. Composed with the cache drop for the same reason:
	// the mark can be written on the decision's own transaction, bypassing the app
	// services that would otherwise refresh the shared user:base entry.
	if notifier, ok := any(router).(botverificationapp.PeerNotifier); ok {
		botVerificationService.SetPeerNotifier(compositeBotVerificationNotifier{
			cache: rpcProjectionVerificationNotifier{
				invalidator:  router,
				users:        userCache,
				peerIdentity: true,
				log:          verificationLogger,
			},
			edge: notifier,
		})
	} else {
		logger.Warn("third-party verification push is not implemented by the RPC edge",
			zap.String("expected_hook", "rpc.Router.NotifyPeerBotVerification"))
	}
	go ratingapp.NewRecomputeWorker(ratingService, logger.Named("rating").Named("recompute"),
		cfg.RatingRecomputeInterval, cfg.RatingRecomputeBatch).Run(ctx)
	// Applicant notifications are delivered from a durable outbox, never inside the
	// decision transaction: @verifybot may be blocked and the panel must not wait on
	// a message send.
	go verificationapp.NewNotificationWorker(verificationService, logger.Named("verification").Named("notify"),
		cfg.VerificationNotifyInterval, cfg.VerificationNotifyBatch).Run(ctx)
	go broadcastapp.NewWorker(broadcastService, broadcastapp.WorkerConfig{
		Interval:         cfg.BroadcastWorkerInterval,
		Lease:            cfg.BroadcastWorkerLease,
		MaterializeBatch: cfg.BroadcastMaterializeBatch,
		DeliveryBatch:    cfg.BroadcastDeliveryBatch,
	}, logger.Named("broadcast").Named("delivery")).Run(ctx)
	moderationActionOptions := []moderationapp.ActionExecutorOption{
		moderationapp.WithAccountDeletionNotifier(router),
	}
	if cfg.PublicLinkWebAddr != "" {
		moderationActionOptions = append(
			moderationActionOptions,
			moderationapp.WithAppealLinks(moderationService, cfg.PublicBaseURL),
		)
	}
	moderationActionExecutor := moderationapp.NewActionExecutor(
		adminService, channelsService, router, accountLifecycleStore,
		moderationActionOptions...,
	)
	go moderationapp.NewActionWorker(
		moderationReportStore,
		moderationActionExecutor,
		logger.Named("moderation").Named("actions"),
	).Run(ctx)
	// Bot session revocation, online notifications and @ChatBot streaming draft
	// pushes are implemented via the router (they need the tg.* boundary), injected
	// after the router is created.
	botsService.SetRouterHooks(router)
	botsService.SetTextDraftPusher(router)
	transcriptionService.SetCompletionHandler(router.PushTranscribedAudioUpdate)
	go rpc.NewOutboxDispatcher(updateEventStore, dispatchOutboxStore, activeSessions, logger.Named("rpc").Named("outbox"),
		rpc.WithOutboxWorkers(cfg.OutboxWorkers),
		rpc.WithOutboxBatch(cfg.OutboxBatch),
		rpc.WithOutboxInterval(cfg.OutboxInterval),
		rpc.WithOutboxPushTimeout(cfg.OutboundPushTimeout),
		rpc.WithOutboxMetrics(metricRegistry),
		rpc.WithOutboxUpdateBuilder(router.BuildOutboxUpdates),
	).Run(ctx)
	go rpc.NewBootstrapUpdateDispatcher(router, logger.Named("rpc").Named("bootstrap")).Run(ctx)
	go rpc.NewWelcomeDeliveryDispatcher(router, welcomeMessageStore, logger.Named("rpc").Named("welcome-delivery")).Run(ctx)
	go rpc.NewScheduledDispatcher(router, logger.Named("rpc").Named("scheduled")).Run(ctx)
	go rpc.NewSuggestedPostDispatcher(router, logger.Named("rpc").Named("suggested-post")).Run(ctx)
	go rpc.NewExpiryDispatcher(router, logger.Named("rpc").Named("expiry")).Run(ctx)
	go rpc.NewPhoneExpiryDispatcher(router, logger.Named("rpc").Named("phone-expiry"), cfg.CallExpiryInterval).Run(ctx)
	go rpc.NewGroupCallSweepDispatcher(router, logger.Named("rpc").Named("groupcall-sweep"), cfg.GroupCallSweepInterval, cfg.GroupCallCheckTTL).Run(ctx)
	go router.RunChannelFanout(ctx)
	go router.RunBotAPIEnqueue(ctx)
	go router.RunPresenceLastSeenBatch(ctx)
	go router.RunPresenceSweeper(ctx, time.Minute)
	go activeSessions.RunPendingSweeper(ctx, time.Minute)
	go router.RunPremiumSweeper(ctx, cfg.PremiumSweepInterval, cfg.PremiumSweepBatch)
	go router.RunAccountLifecycle(ctx, time.Minute, 500)
	go router.RunAccountFreezeNotifications(ctx, time.Minute, 500)
	go router.RunAccountDeletionNotifications(ctx, time.Minute, 500)
	if telegramLoginService != nil {
		go runTelegramLoginRetention(ctx, telegramLoginService, cfg.TelegramLoginRetention, cfg.TelegramLoginSweepInterval, cfg.TelegramLoginSweepBatch, logger.Named("telegram-login-retention"))
	}
	go func() {
		interval := cfg.StarGiftSweepInterval
		if interval <= 0 {
			interval = 15 * time.Second
		}
		batch := cfg.StarGiftSweepBatch
		if batch <= 0 {
			batch = 1000
		}
		run := func() {
			if err := giftsService.SweepLifecycle(ctx, int(time.Now().Unix()), batch); err != nil && ctx.Err() == nil {
				logger.Warn("star_gift_lifecycle_sweep_failed", zap.Error(err))
			}
		}
		run()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	go router.RunInlineBotPushSubscriber(ctx)
	go router.RunBotCallbackAnswerSubscriber(ctx)
	go router.RunEphemeralPushSubscriber(ctx)
	// Watchers only exist when the feature is switched on AND the wallet is
	// provisioned. EnabledChains can still return a chain whose rpc_url an
	// operator has since cleared, so each one is checked rather than dialed
	// blind -- that was the source of a connection-refused ERROR on every boot
	// of every server (see the seed migration's ganache row).
	if donationsService != nil && donationsService.Ready() {
		donationChains, err := donationsService.EnabledChains(ctx)
		if err != nil {
			logger.Warn("list enabled donation chains failed; no donation watchers started", zap.Error(err))
		}
		for _, chain := range donationChains {
			if !chain.Watchable() {
				// Enabled but not yet configured (empty rpc_url, or no price
				// feed). Say so once, at Warn: this is a row an operator will
				// finish filling in, not a fault in the server.
				logger.Named("donations").Named(chain.Key).Warn(
					"chain is enabled but has no RPC endpoint; no watcher started", zap.String("rpc_url", chain.RPCURL))
				continue
			}
			chainLog := logger.Named("donations").Named(chain.Key)
			go func(chainKey string, log *zap.Logger) {
				err := donationsService.WatchChain(ctx, chainKey, cfg.DonationPollInterval, log)
				if err == nil || errors.Is(err, context.Canceled) {
					return
				}
				// A watcher that stops is either the chain being unreachable
				// or the operator misconfiguring it, and in both cases the
				// operator has to look at it. It is a configuration problem
				// with the deployment, not an internal failure, so it is a
				// Warn with the chain named -- previously every boot logged
				// an unparameterised ERROR for the seeded local chain.
				log.Warn("donation watcher stopped", zap.Error(err))
			}(chain.Key, chainLog)
		}
	}
	if _, err := botapi.Start(ctx, cfg.BotAPIAddr, botsService, usersService, router, router, router, logger.Named("botapi")); err != nil {
		return fmt.Errorf("start bot api: %w", err)
	}
	// Scoped tokens carry a bounded permission set; the master token stays
	// unrestricted, so a deployment that configures none behaves exactly as before.
	adminScopedTokens := make([]adminapi.ScopedToken, 0, len(cfg.AdminScopedTokens))
	for _, item := range cfg.AdminScopedTokens {
		adminScopedTokens = append(adminScopedTokens, adminapi.ScopedToken{
			Name:        item.Name,
			Token:       item.Token,
			Permissions: item.Permissions,
		})
	}
	if _, err := adminapi.Start(ctx, adminapi.Config{
		Addr:         cfg.AdminAPIAddr,
		Token:        cfg.AdminAPIToken,
		ScopedTokens: adminScopedTokens,
	}, adminService, logger.Named("adminapi")); err != nil {
		return fmt.Errorf("start admin api: %w", err)
	}
	if _, err := web.Start(ctx, web.Config{
		Addr:               cfg.PublicLinkWebAddr,
		PublicBaseURL:      cfg.PublicBaseURL,
		AppScheme:          cfg.PublicAppScheme,
		AppLinkBase:        cfg.PublicAppLinkBase,
		WebBaseURL:         cfg.PublicWebBaseURL,
		AppName:            cfg.PublicAppName,
		AllowDevPayments:   cfg.AllowDevPayments,
		StickerSets:        filesService,
		Users:              userStore,
		Channels:           channelStore,
		Privacy:            privacyService,
		Photos:             filesService,
		UniqueGifts:        giftsService,
		GiftWithdrawals:    giftsService,
		RevenueWithdrawals: giftsService,
		CustomFragment:     customFragmentService,
		GiftClaim:          giftClaimService,
		GiftClaimTest:      giftClaimTestService,
		MiniApps: web.NewConfiguredMiniAppsHandler(web.MiniAppsConfig{
			AppName:  cfg.PublicAppName,
			Bots:     botsService,
			Stickers: filesService,
			Tokens:   botStore,
			// Telegram signs Mini App initData with the launching bot token.
			// Keep these secrets server-side; the HTML/JS never receives them.
			BotFatherToken: os.Getenv("TELESRV_MINIAPP_BOTFATHER_TOKEN"),
			StickersToken:  os.Getenv("TELESRV_MINIAPP_STICKERS_TOKEN"),
		}),
		ModerationAppeals: moderationService,
		TelegramLogin:     telegramLoginHTTPHandler,
	}, logger.Named("public-web")); err != nil {
		return fmt.Errorf("start public Web: %w", err)
	}

	srv := mtprotoedge.New(mtprotoedge.Options{
		Logger:                         logger.Named("mtprotoedge"),
		DC:                             cfg.DC,
		StrictDC:                       cfg.StrictDCCheck,
		RSAKey:                         rsaKey,
		LayerRPC:                       router,
		AuthKeys:                       authKeyGetBatchStore,
		ActiveSessions:                 activeSessions,
		Metrics:                        metricRegistry,
		ObfuscatedTCP:                  true,
		WebSocket:                      cfg.WebSocketEnable,
		WebSocketAllowedOrigins:        cfg.WebSocketAllowedOrigins,
		SamePortHTTP:                   serverinfo.Handler(cfg.DC, rsaKey, identityStore),
		MaxConnections:                 cfg.MTProtoMaxConnections,
		MaxConnectionsPerIP:            cfg.MTProtoMaxConnectionsPerIP,
		MaxConcurrentHandshakes:        cfg.MTProtoMaxConcurrentHandshakes,
		RPCMaxInflight:                 cfg.MTProtoRPCMaxInflight,
		RPCQueueSize:                   cfg.MTProtoRPCQueueSize,
		RPCTimeout:                     cfg.MTProtoRPCTimeout,
		RPCGlobalWorkers:               cfg.MTProtoRPCGlobalWorkers,
		RPCGlobalMaxTasks:              cfg.MTProtoRPCGlobalMaxTasks,
		RPCGlobalMaxBytes:              cfg.MTProtoRPCGlobalMaxBytes,
		RPCDeliveryHookWorkers:         cfg.MTProtoRPCDeliveryHookWorkers,
		RPCDeliveryHookMaxPending:      cfg.MTProtoRPCDeliveryHookMaxPending,
		RPCExecutionMaxEntries:         cfg.MTProtoRPCExecutionMaxEntries,
		RPCExecutionAuthMaxEntries:     cfg.MTProtoRPCExecutionAuthMaxEntries,
		RPCExecutionSessionMaxEntries:  cfg.MTProtoRPCExecutionSessionMaxEntries,
		RPCExecutionPendingPerAuth:     cfg.MTProtoRPCExecutionPendingPerAuth,
		InboundFrameGlobalMaxBytes:     cfg.MTProtoInboundFrameGlobalMaxBytes,
		OutboundQueueSize:              cfg.MTProtoOutboundQueueSize,
		OutboundControlQueueSize:       cfg.MTProtoOutboundControlQueueSize,
		OutboundTrackedGlobalMaxBytes:  cfg.MTProtoOutboundTrackedGlobalMaxBytes,
		OutboundCriticalGlobalMaxBytes: cfg.MTProtoOutboundCriticalGlobalMaxBytes,
		OutboundWriteGlobalMaxBytes:    cfg.MTProtoOutboundWriteGlobalMaxBytes,
		OnServing: func(_ net.Addr) {
			logger.Info("telesrv server ready",
				zap.String("listen", cfg.ListenAddr),
				zap.String("advertise", net.JoinHostPort(cfg.AdvertiseIP, portStr)),
				zap.Int("pid", os.Getpid()),
				zap.String("git_commit", buildMeta.Commit),
				zap.Uint("schema_version", migrationStatus.Version),
				zap.String("blob_backend", "localfs"),
			)
			// Cache warm-up is a pure optimization and reads thousands of blobs. It
			// runs after the listener is ready to avoid competing for I/O with the
			// seed/langpack disk reads and slowing startup; epoch validation ensures a
			// stale warm-up snapshot cannot overwrite concurrent RPC mutations.
			go func() {
				stats, err := filesService.WarmCaches(ctx)
				if err != nil {
					logger.Warn("media resource cache warm-up failed", zap.Error(err))
				} else if stats.StickerSets > 0 || stats.Documents > 0 || stats.Blobs > 0 {
					logger.Info("media resource cache warm-up complete",
						zap.Int("sticker_sets", stats.StickerSets),
						zap.Int("documents", stats.Documents),
						zap.Int("blobs", stats.Blobs),
					)
				}
			}()
		},
	})
	metricRegistry.AddGaugeProvider(func() []obsmetrics.GaugeSample {
		return mtprotoRuntimeGaugeSamples(srv.RuntimeSnapshot())
	})
	// This is intentionally the final startup operation. ListenAndServe owns the
	// public listener; seeding finishes before port 2398 is exposed, and the
	// epoch-guarded cache warm-up starts asynchronously in OnServing.
	return srv.ListenAndServe(ctx, cfg.ListenAddr)
}

// telegramLoginRPCDependency preserves a disabled Telegram Login service as a
// nil interface. Assigning the nil *Service directly to rpc.Deps would create a
// non-nil interface with a nil concrete pointer and bypass Router availability
// checks.
func telegramLoginRPCDependency(service *telegramloginapp.Service) rpc.TelegramLoginService {
	if service == nil {
		return nil
	}
	return service
}

// donationsWalletKey resolves the crypto donations encryption key: an
// explicit TELESRV_DONATION_WALLET_KEY wins if set, otherwise it's the
// local key file at cfg.DonationWalletKeyPath, generated automatically the
// first time this server ever runs (see donationsapp.LoadOrGenerateEncryptionKey).
func donationsWalletKey(cfg config.Config) (donationsapp.EncryptionKey, error) {
	if key := strings.TrimSpace(cfg.DonationWalletKey); key != "" {
		return donationsapp.ParseEncryptionKey(key)
	}
	return donationsapp.LoadOrGenerateEncryptionKey(cfg.DonationWalletKeyPath)
}

func runTelegramLoginRetention(ctx context.Context, service *telegramloginapp.Service, retention, interval time.Duration, batch int, logger *zap.Logger) {
	run := func() {
		var total int64
		// Bound one tick even when a deployment accumulated years of stale data;
		// subsequent ticks continue without monopolizing the database pool.
		for range 10 {
			deleted, err := service.DeleteExpiredArtifacts(ctx, time.Now().UTC().Add(-retention), batch)
			if err != nil {
				if ctx.Err() == nil {
					logger.Warn("telegram_login_retention_failed", zap.Error(err))
				}
				return
			}
			total += deleted
			if deleted < int64(batch) {
				break
			}
		}
		if total > 0 {
			logger.Info("telegram_login_retention_completed", zap.Int64("deleted", total))
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

type giftClaimInstanceConfig struct {
	Enabled             bool
	BotUserID           int64
	SystemUser          domain.User
	BotHandle           string
	BotToken            string
	PublicBaseURL       string
	BasePath            string
	Collection          string
	AppName             string
	LiteserverConfigURL string
	ChallengeTTL        time.Duration
	ProofTTL            time.Duration
	InitDataTTL         time.Duration
	Minter              giftclaim.Minter
	Withdrawer          giftclaim.Withdrawer
	Password            giftclaim.AccountPassword
	MenuButtonRequired  bool
}
