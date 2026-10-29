package core

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/clock"
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
	businessautomationapp "telesrv/internal/app/businessautomation"
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
	welcomemessagesapp "telesrv/internal/app/welcomemessages"
	"telesrv/internal/blobstorage"
	"telesrv/internal/botapi"
	"telesrv/internal/config"
	"telesrv/internal/coreexec"
	"telesrv/internal/domain"
	"telesrv/internal/edgecontrol"
	"telesrv/internal/edgecontrol/redisbus"
	"telesrv/internal/edgecontrol/redisregistry"
	"telesrv/internal/filedata"
	"telesrv/internal/geoip"
	"telesrv/internal/identity"
	"telesrv/internal/node/common"
	nodeprojection "telesrv/internal/node/projection"
	obsmetrics "telesrv/internal/observability/metrics"
	"telesrv/internal/officialgifts"
	"telesrv/internal/otpdelivery"
	otpsmtp "telesrv/internal/otpdelivery/smtp"
	otpwebhook "telesrv/internal/otpdelivery/webhook"
	"telesrv/internal/rpc"
	"telesrv/internal/seed/catalog"
	freezeseed "telesrv/internal/seed/freeze"
	"telesrv/internal/sfu"
	storepkg "telesrv/internal/store"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/postgres"
	"telesrv/internal/store/redisstore"
	"telesrv/internal/telegramloginhttp"
	"telesrv/internal/tonfinalizer"
	"telesrv/internal/turnsrv"
	"telesrv/internal/updatecdn"
	"telesrv/internal/web"
)

func localStarGiftWithdrawalOptions(publicBaseURL, publicLinkWebAddr, exportMode string, logger *zap.Logger) ([]stargifts.Option, error) {
	if strings.TrimSpace(publicLinkWebAddr) == "" {
		return nil, nil
	}
	provider, err := stargifts.NewLocalWithdrawalProvider(publicBaseURL)
	if errors.Is(err, stargifts.ErrLocalWithdrawalInsecureBaseURL) {
		// A plain-HTTP LAN deployment (the Docker quick start) must still
		// start (gramsrv#135). Without a provider, gift export and channel
		// revenue withdrawal answer their explicit "unavailable" errors
		// until the public base URL is HTTPS.
		if logger != nil {
			logger.Warn("local star gift and revenue withdrawal disabled: public base URL is plain HTTP outside loopback",
				zap.String("public_base_url", publicBaseURL))
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	options := []stargifts.Option{stargifts.WithRevenueWithdrawalProvider(provider)}
	if exportMode == "" || exportMode == config.StarGiftExportModeLocal {
		options = append(options, stargifts.WithGiftWithdrawalProvider(provider))
	}
	return options, nil
}

func loadTONCapabilitySecret(path string) ([]byte, error) {
	raw, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("read capability secret: %w", err)
	}
	encoded := strings.TrimSpace(string(raw))
	var secret []byte
	for _, encoding := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		decoded, decodeErr := encoding.DecodeString(encoded)
		if decodeErr == nil {
			secret = decoded
			break
		}
	}
	if len(secret) < 32 || len(secret) > 64 {
		return nil, fmt.Errorf("capability secret must decode to 32-64 bytes")
	}
	return secret, nil
}

func loadTONClaimBotToken(path string) (string, error) {
	raw, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("read claim bot token: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if len(token) > 512 {
		return "", fmt.Errorf("claim bot token file is too large")
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return "", fmt.Errorf("claim bot token file must contain one token")
	}
	if _, _, ok := domain.ParseBotToken(token); !ok {
		return "", fmt.Errorf("claim bot token is invalid")
	}
	return token, nil
}

func newBusinessAutomationOptions(cfg config.CoreConfig, online messageapp.BusinessAutomationOnlineChecker, generator messageapp.BusinessAITextGenerator, logger *zap.Logger) []messageapp.BusinessAutomationOption {
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

func newAIComposeOptions(cfg config.CoreConfig, limiter aiapp.RateLimiter, premium aiapp.PremiumChecker, logger *zap.Logger) []aiapp.Option {
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

func newCrossCraftGenerator(cfg config.CoreConfig, blobs stargifts.BlobBackend, logger *zap.Logger) postgres.StarGiftCrossCraftGenerator {
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

func newTranslationOptions(cfg config.CoreConfig, limiter translationapp.RateLimiter, logger *zap.Logger) []translationapp.Option {
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
		SetVerifiedWithDelivery(ctx context.Context, userID int64, verified bool, effects storepkg.DeliveryEffectsBuilder[storepkg.UserAudienceDeliverySnapshot]) (domain.User, error)
	}
	channels interface {
		SetVerified(ctx context.Context, channelID int64, verified bool) (domain.Channel, error)
	}
	// channelRowCache is handed to the transaction-scoped channel store so the
	// cached channel row is dropped on the flag write, exactly as the pooled store
	// does it.
	channelRowCache *postgres.ChannelRowCache
	userDelivery    storepkg.DeliveryEffectsBuilder[storepkg.UserAudienceDeliverySnapshot]
}

func (v *verificationPeerVerifier) SetUserVerified(ctx context.Context, userID int64, verified bool) error {
	if v == nil || v.userDelivery == nil {
		return storepkg.ErrDeliveryOutboxRequired
	}
	if tx, ok := postgres.VerificationTxFromContext(ctx); ok {
		_, err := postgres.NewUserStore(tx).SetVerifiedWithDelivery(ctx, userID, verified, v.userDelivery)
		return err
	}
	if v.users == nil {
		return fmt.Errorf("verification peer verifier: user service is not wired")
	}
	_, err := v.users.SetVerifiedWithDelivery(ctx, userID, verified, v.userDelivery)
	return err
}

func (v *verificationPeerVerifier) SetChannelVerified(ctx context.Context, channelID int64, verified bool) error {
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

var _ verificationapp.PeerVerifier = (*verificationPeerVerifier)(nil)

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

func (a botVerificationMarkApplier) GrantCustomVerificationWithDelivery(
	ctx context.Context,
	mark domain.CustomVerification,
	effects storepkg.DeliveryEffectsBuilder[storepkg.UserAudienceDeliverySnapshot],
) (domain.CustomVerification, bool, error) {
	if effects == nil {
		return domain.CustomVerification{}, false, storepkg.ErrDeliveryOutboxRequired
	}
	if tx, ok := postgres.VerificationTxFromContext(ctx); ok {
		return postgres.NewBotVerificationStore(tx).GrantCustomVerificationWithDelivery(ctx, mark, effects)
	}
	if a.store == nil {
		return domain.CustomVerification{}, false, fmt.Errorf("bot verification mark applier: store is not wired")
	}
	return a.store.GrantCustomVerificationWithDelivery(ctx, mark, effects)
}

func (a botVerificationMarkApplier) RevokeCustomVerification(ctx context.Context, verifierBotID int64, peer domain.Peer) (bool, error) {
	if tx, ok := postgres.VerificationTxFromContext(ctx); ok {
		return postgres.NewBotVerificationStore(tx).RevokeCustomVerification(ctx, verifierBotID, peer)
	}
	return a.store.RevokeCustomVerification(ctx, verifierBotID, peer)
}

func (a botVerificationMarkApplier) RevokeCustomVerificationWithDelivery(
	ctx context.Context,
	verifierBotID int64,
	peer domain.Peer,
	effects storepkg.DeliveryEffectsBuilder[storepkg.UserAudienceDeliverySnapshot],
) (bool, error) {
	if effects == nil {
		return false, storepkg.ErrDeliveryOutboxRequired
	}
	if tx, ok := postgres.VerificationTxFromContext(ctx); ok {
		return postgres.NewBotVerificationStore(tx).RevokeCustomVerificationWithDelivery(ctx, verifierBotID, peer, effects)
	}
	if a.store == nil {
		return false, fmt.Errorf("bot verification mark applier: store is not wired")
	}
	return a.store.RevokeCustomVerificationWithDelivery(ctx, verifierBotID, peer, effects)
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
	}
	users storepkg.UserCache
	log   *zap.Logger
}

func (n rpcProjectionVerificationNotifier) NotifyPeerVerified(ctx context.Context, peer domain.Peer) error {
	if n.invalidator == nil {
		return nil
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
	return n.edge.NotifyPeerVerified(ctx, peer)
}

var _ verificationapp.PeerNotifier = compositeVerificationNotifier{}

var _ verificationapp.PeerNotifier = rpcProjectionVerificationNotifier{}

func externalMediaOption(cfg config.CoreConfig) filesapp.Option {
	if !cfg.ExternalMediaEnable {
		return nil
	}
	return filesapp.WithExternalMedia(cfg.ExternalMediaMaxBytes, cfg.ExternalMediaRatePerMin)
}

// webPagePreviewOption 按配置启用链接预览抓取；禁用时返回 nil（NewService 跳过 nil option）。
func webPagePreviewOption(cfg config.CoreConfig) filesapp.Option {
	if !cfg.WebPagePreviewEnable {
		return nil
	}
	return filesapp.WithWebPagePreview(cfg.WebPagePreviewMaxBytes, cfg.WebPagePreviewRatePerMin)
}

func validateCoreConfig(cfg config.CoreConfig) error {
	if strings.TrimSpace(cfg.GroupCallControlAddr) == "" {
		return fmt.Errorf("TELESRV_GROUPCALL_CONTROL_ADDR is required by cmd/telesrv-core for standalone SFU liveness callbacks")
	}
	if strings.TrimSpace(cfg.GroupCallControlToken) == "" {
		return fmt.Errorf("TELESRV_GROUPCALL_CONTROL_TOKEN is required by cmd/telesrv-core and cmd/telesrv-sfu")
	}
	if strings.TrimSpace(cfg.SFUControlToken) == "" {
		return fmt.Errorf("TELESRV_SFU_CONTROL_TOKEN is required by cmd/telesrv-core and cmd/telesrv-sfu")
	}
	if strings.TrimSpace(cfg.CoreExecGRPCAddr) == "" {
		return fmt.Errorf("TELESRV_CORE_EXEC_GRPC_ADDR is required by cmd/telesrv-core")
	}
	if strings.TrimSpace(cfg.CoreExecToken) == "" {
		return fmt.Errorf("TELESRV_CORE_EXEC_TOKEN is required by cmd/telesrv-core and cmd/telesrv-edge")
	}
	if strings.TrimSpace(cfg.FileGRPCTargets) == "" {
		return fmt.Errorf("TELESRV_FILE_GRPC_TARGETS is required by cmd/telesrv-core")
	}
	if strings.TrimSpace(cfg.FileToken) == "" {
		return fmt.Errorf("TELESRV_FILE_TOKEN is required by cmd/telesrv-core and cmd/telesrv-file")
	}
	if cfg.TURNEnable {
		if strings.TrimSpace(cfg.TURNSecret) == "" {
			return fmt.Errorf("TELESRV_TURN_SECRET is required by Core credential issuer and SFU media listener")
		}
		if cfg.TURNUDPPort <= 0 {
			return fmt.Errorf("TELESRV_TURN_UDP_PORT must be positive")
		}
		if cfg.CallTURNCredentialTTL <= 0 {
			return fmt.Errorf("TELESRV_CALL_TURN_CREDENTIAL_TTL must be positive")
		}
	}
	return nil
}

type sfuRemoteControl interface {
	sfu.RemoteService
	sfu.InstanceHealthChecker
}

func newSFURemoteControl(cfg config.CoreConfig) (sfuRemoteControl, func() error, error) {
	remote, err := sfu.NewGRPCRemoteService(sfu.GRPCRemoteConfig{
		Token:          cfg.SFUControlToken,
		RequestTimeout: cfg.SFUControlGRPCRequestTimeout,
		TLSCAFile:      cfg.SFUControlGRPCTLSCAFile,
		TLSServerName:  cfg.SFUControlGRPCTLSServerName,
		TLSCertFile:    cfg.SFUControlGRPCTLSClientCertFile,
		TLSKeyFile:     cfg.SFUControlGRPCTLSClientKeyFile,
	})
	if err != nil {
		return nil, nil, err
	}
	return remote, remote.Close, nil
}

// Run starts the production Core role.
func Run(logger *zap.Logger, buildMeta common.BuildMetadata) error {
	cfg, err := config.LoadCore()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	return runWithConfig(logger, cfg, buildMeta)
}

func runWithConfig(logger *zap.Logger, cfg config.CoreConfig, buildMeta common.BuildMetadata) error {
	if err := validateCoreConfig(cfg); err != nil {
		return err
	}
	if err := common.ConfigureProcessGlobals(cfg); err != nil {
		return err
	}
	// Install the phone format policy before any store or service is built.
	// Login, phone change, contact import and the Admin API (served by Core)
	// all canonicalize through it, so every Core must use the same value.
	domain.ConfigurePhoneFormatCheck(cfg.CheckNumberFormat)
	logger.Info("手机号格式策略", zap.Bool("check_number_format", cfg.CheckNumberFormat))
	// Server Settings → Server identity lives in cfg.IdentityDir, shared with
	// the Admin role. The custom name becomes the 777000 display name now; the
	// identity watcher started after the router keeps name and avatar in sync
	// with later panel edits on every Core instance without a restart.
	identityStore := identity.NewStore(cfg.IdentityDir)
	if info, err := identityStore.Get(); err != nil {
		logger.Warn("读取服务器身份失败，沿用品牌默认名", zap.Error(err))
	} else {
		domain.SetOfficialSystemUserDisplayName(info.Name)
	}
	instanceID, err := config.RequireInstanceID(cfg.InstanceID)
	if err != nil {
		return fmt.Errorf("invalid instance id: %w", err)
	}

	// tg.Layer 由当前导入的 canonical schema 生成；纳入未来 Layer 后无需
	// 在 telesrv 另维护一份常量。
	logger.Info("telesrv core starting",
		zap.Int("dc", cfg.DC),
		zap.String("default_country_code", cfg.DefaultCountryCode),
		zap.String("advertise", net.JoinHostPort(cfg.AdvertiseIP, strconv.Itoa(cfg.AdvertisePort))),
		zap.Int("tl_layer", tg.Layer),
		zap.String("git_commit", buildMeta.Commit),
		zap.String("git_branch", buildMeta.Branch),
		zap.String("git_tree_state", buildMeta.TreeState),
		zap.String("build_time", buildMeta.BuildTime),
		zap.String("go_version", buildMeta.GoVersion),
		zap.String("instance_id", instanceID),
	)

	ctx, stop, metricRegistry := common.StartRuntimeSupport(cfg, logger)
	defer stop()

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
	counterRecovery, err := postgres.OpenCounterRecovery(ctx, cfg.PostgresDSN, cfg.PostgresCounterRecoveryMaxConns)
	if err != nil {
		return fmt.Errorf("connect postgres counter recovery: %w", err)
	}
	defer counterRecovery.Close()
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
	metricRegistry.AddGaugeProvider(func() []obsmetrics.GaugeSample {
		stat := counterRecovery.Stat()
		if stat == nil {
			return nil
		}
		return []obsmetrics.GaugeSample{
			{Name: "telesrv_postgres_counter_recovery_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "total"}}, Value: float64(stat.TotalConns())},
			{Name: "telesrv_postgres_counter_recovery_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "acquired"}}, Value: float64(stat.AcquiredConns())},
			{Name: "telesrv_postgres_counter_recovery_pool_connections", Labels: []obsmetrics.Label{{Name: "state", Value: "idle"}}, Value: float64(stat.IdleConns())},
			{Name: "telesrv_postgres_counter_recovery_pool_max_connections", Value: float64(stat.MaxConns())},
			{Name: "telesrv_postgres_counter_recovery_pool_acquire_wait_seconds", Value: stat.AcquireDuration().Seconds()},
			{Name: "telesrv_postgres_counter_recovery_pool_empty_acquire_count", Value: float64(stat.EmptyAcquireCount())},
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
	logger.Info("持久化依赖就绪", zap.String("redis", cfg.RedisAddr))
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
	authKeyGetBatchStore, err := postgres.NewBatchedAuthKeyStore(authKeyStore, postgres.AuthKeyGetBatchConfig{
		MaxSize: cfg.AuthKeyGetBatchMax, MaxWait: cfg.AuthKeyGetBatchWait,
		QueueSize: cfg.AuthKeyGetBatchQueue, QueryTimeout: cfg.AuthKeyGetBatchTimeout,
	})
	if err != nil {
		return fmt.Errorf("initialize auth-key lookup batcher: %w", err)
	}
	defer authKeyGetBatchStore.Close()
	baseTempAuthKeyStore := postgres.NewTempAuthKeyBindingStore(pool)
	authKeySessionLayers, err := redisstore.NewAuthKeySessionLayerStore(rdb, authKeyStore)
	if err != nil {
		return fmt.Errorf("initialize Redis auth key Layer authority: %w", err)
	}
	tempAuthKeyStore, err := redisstore.NewLayerAwareTempAuthKeyBindingStore(
		baseTempAuthKeyStore, authKeySessionLayers,
	)
	if err != nil {
		return fmt.Errorf("initialize temp auth key Layer binding: %w", err)
	}
	authzStore := postgres.NewAuthorizationStore(pool)
	adminStore := postgres.NewAdminStore(pool)
	updateStateStore := postgres.NewUpdateStateStore(pool)
	updateEventStore := postgres.NewUpdateEventStore(pool, postgres.WithUpdateEventLogger(logger.Named("store").Named("updates")))
	phoneChangeStore := postgres.NewPhoneChangeStore(pool)
	deliveryOutboxStore := postgres.NewDeliveryOutboxStore(pool, postgres.WithDeliveryLeaseTimeout(cfg.OutboxLeaseTimeout))
	bootstrapUpdateStore, err := postgres.NewBatchedBootstrapUpdateJobStore(
		postgres.NewBootstrapUpdateJobStore(pool),
		postgres.BootstrapReadyBatchConfig{
			MaxSize: cfg.BootstrapReadyBatchMax, MaxWait: cfg.BootstrapReadyBatchWait,
			QueueSize: cfg.BootstrapReadyBatchQueue, QueryTimeout: cfg.BootstrapReadyBatchTimeout,
			Metrics: metricRegistry,
		},
	)
	if err != nil {
		return fmt.Errorf("initialize bootstrap readiness batcher: %w", err)
	}
	defer bootstrapUpdateStore.Close()
	botAPIUpdateStore := postgres.NewBotAPIUpdateStore(pool)
	botCallbackStore := redisstore.NewBotCallbackRegistryStore(rdb)
	botPreCheckoutStore := redisstore.NewBotPreCheckoutRegistryStore(rdb)
	loginTokenStore := redisstore.NewLoginTokenRegistryStore(rdb)
	ephemeralStore := redisstore.NewEphemeralMessageStore(rdb)
	ephemeralReportStore := postgres.NewEphemeralReportStore(pool)
	welcomeMessageStore := postgres.NewWelcomeMessageStore(pool)
	messageTranscriptionStore := postgres.NewMessageTranscriptionStore(pool)
	moderationReportStore := postgres.NewModerationReportStore(pool)
	authDeliveryReportStore := postgres.NewAuthDeliveryReportStore(pool)
	clientTelemetryStore := postgres.NewClientTelemetryStore(pool)
	boxIDAllocator := redisstore.NewBoxIDAllocator(rdb, counterRecovery.MessageBoxSource())
	channelIDAllocator := redisstore.NewChannelIDAllocator(rdb, counterRecovery.ChannelIDSource())
	channelMessageIDAllocator := redisstore.NewChannelMessageIDAllocator(rdb, counterRecovery.ChannelMessageIDSource())
	readModelRedisBus := redisstore.NewReadModelInvalidationBus(rdb, logger.Named("store").Named("read-model-redis"))
	channelTopMessageCache := postgres.NewChannelTopMessageCache(cfg.ChannelTopMessageCacheMaxEntries)
	channelDifferenceCache := postgres.NewChannelDifferenceBaseCache(
		cfg.ChannelDifferenceCacheMaxEntries,
		cfg.ChannelDifferenceCacheMaxBytes,
		cfg.ChannelDifferenceCacheTTL,
	)
	projectionStores := nodeprojection.NewStores(pool, rdb, nodeprojection.StoreConfig{
		ChannelRowCacheMaxEntries:    cfg.ChannelRowCacheMaxEntries,
		ChannelMemberCacheMaxEntries: cfg.ChannelMemberCacheMaxEntries,
		ChannelDialogCacheMaxEntries: cfg.ChannelDialogCacheMaxEntries,
		ChannelBoostCacheMaxEntries:  cfg.ChannelBoostCacheMaxEntries,
		ChannelBoostCacheTTL:         cfg.ChannelBoostCacheTTL,
		StarGiftTONStartingGrant:     cfg.StarGiftTONStartingGrant,
	}, nodeprojection.StoreOptions{
		ChannelOptions: []postgres.ChannelStoreOption{
			postgres.WithChannelAllocators(channelIDAllocator, channelMessageIDAllocator),
			postgres.WithChannelStarsStartingGrant(cfg.StarsStartingGrant),
			postgres.WithChannelTopMessageCache(channelTopMessageCache),
			postgres.WithChannelDifferenceBaseCache(channelDifferenceCache),
		},
		MessageOptions: []postgres.MessageStoreOption{
			postgres.WithMessageAllocators(boxIDAllocator),
			postgres.WithMessageReadModelInvalidations(readModelRedisBus),
			postgres.WithPlainPrivateSendBatchObserver(metricRegistry),
		},
	}, logger)
	readModelVersionBatchStore, err := storepkg.NewBatchedReadModelVersionStore(
		postgres.NewReadModelVersionStore(pool),
		storepkg.ReadModelVersionBatchConfig{
			MaxKeys: cfg.ReadModelVersionBatchMaxKeys, MaxWait: cfg.ReadModelVersionBatchWait,
			QueueSize: cfg.ReadModelVersionBatchQueue, QueryTimeout: cfg.ReadModelVersionBatchTimeout,
		},
	)
	if err != nil {
		return fmt.Errorf("initialize read-model version batcher: %w", err)
	}
	defer readModelVersionBatchStore.Close()
	projectionStores.ReadModelVersions = storepkg.NewCachedReadModelVersionStore(
		readModelVersionBatchStore, 0, cfg.ReadModelVersionCacheMaxEntries,
	)
	reverseContactStore, err := storepkg.NewBatchedReverseContactStore(
		postgres.NewContactStore(pool),
		storepkg.ReverseContactBatchConfig{
			MaxPairs: cfg.ContactReverseBatchMaxPairs, MaxWait: cfg.ContactReverseBatchWait,
			QueueSize: cfg.ContactReverseBatchQueue, QueryTimeout: cfg.ContactReverseBatchTimeout,
		},
	)
	if err != nil {
		return fmt.Errorf("initialize reverse-contact batcher: %w", err)
	}
	defer reverseContactStore.Close()
	projectionStores.ContactStore = userprojection.NewCachedContactStoreWithMaxViewers(
		reverseContactStore, 0, cfg.ContactSnapshotCacheMaxViewers,
	)
	projectionStores.Photos = userprojection.NewCachedPhotoProviderWithMaxEntries(
		projectionStores.MediaStore, cfg.ProfilePhotoCacheTTL, cfg.ProfilePhotoCacheMaxEntries,
	)
	userStore := projectionStores.UserStore
	contactStore := projectionStores.ContactStore
	collectiblePhoneStore := projectionStores.CollectiblePhoneStore
	readModelVersionStore := projectionStores.ReadModelVersions
	userCache := projectionStores.UserCache
	dialogStore := projectionStores.DialogStore
	chatlistStore := postgres.NewChatlistStore(pool)
	messageStore := projectionStores.MessageStore
	broadcastStore := postgres.NewBroadcastStore(pool)
	broadcastService := broadcastapp.NewService(broadcastStore, messageStore, logger.Named("app").Named("broadcast"))
	channelRowCache := projectionStores.ChannelRowCache
	channelStore := projectionStores.ChannelStore
	activeChannelIDsPageBatcher, err := postgres.NewActiveChannelIDsPageBatcher(channelStore, postgres.ActiveChannelIDsBatchConfig{
		MaxSize: cfg.ActiveChannelIDsBatchMax, MaxWait: cfg.ActiveChannelIDsBatchWait,
		QueueSize: cfg.ActiveChannelIDsBatchQueue, QueryTimeout: cfg.ActiveChannelIDsBatchTimeout,
		Metrics: metricRegistry,
	})
	if err != nil {
		return fmt.Errorf("initialize active-channel page batcher: %w", err)
	}
	defer activeChannelIDsPageBatcher.Close()
	dialogListSnapshotCache := redisstore.NewDialogListSnapshotCache(rdb, cfg.DialogListSnapshotRedisTTL)
	activeChannelIDsPageCache := redisstore.NewActiveChannelIDsPageCache(rdb, cfg.ActiveChannelIDsRedisTTL)
	communityCatalogCache := postgres.NewCommunityCatalogCache()
	communityStore := postgres.NewCommunityStore(pool, channelIDAllocator, channelMessageIDAllocator,
		postgres.WithCommunityCatalogCache(communityCatalogCache))
	pollStore := postgres.NewPollStore(pool)
	mediaStore := projectionStores.MediaStore
	gifCatalogStore := postgres.NewGifCatalogStore(pool)
	cachedPhotos := projectionStores.Photos
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
	storyStore := postgres.NewStoryStore(pool)
	fileGRPCTargets, err := filedata.ParseGRPCTargetsForResolver(cfg.FileGRPCTargets, cfg.FileGRPCResolver)
	if err != nil {
		return fmt.Errorf("parse TELESRV_FILE_GRPC_TARGETS: %w", err)
	}
	fileRemote, fileConn, err := filedata.DialGRPCRemote(ctx, filedata.GRPCClientConfig{
		Targets:        fileGRPCTargets,
		ResolverKind:   cfg.FileGRPCResolver,
		Token:          cfg.FileToken,
		Logger:         logger.Named("filedata").Named("grpc").Named("client"),
		RequestTimeout: cfg.FileGRPCRequestTimeout,
		TLSCAFile:      cfg.FileGRPCTLSCAFile,
		TLSServerName:  cfg.FileGRPCTLSServerName,
		TLSCertFile:    cfg.FileGRPCTLSClientCertFile,
		TLSKeyFile:     cfg.FileGRPCTLSClientKeyFile,
	})
	if err != nil {
		return fmt.Errorf("connect filedata grpc: %w", err)
	}
	defer func() { _ = fileConn.Close() }()
	if err := blobstorage.RequireConfiguredBackend(ctx, mediaStore, fileRemote.Name()); err != nil {
		return fmt.Errorf("validate filedata blob backend: %w", err)
	}
	blobBackend := filesapp.BlobBackend(fileRemote)
	uploadPartBackend := filesapp.UploadPartBackend(fileRemote)
	logger.Info("filedata grpc health check passed",
		zap.String("resolver", cfg.FileGRPCResolver),
		zap.Strings("targets", fileGRPCTargets),
		zap.String("backend", blobBackend.Name()))
	filesService := filesapp.NewService(mediaStore, blobBackend, cfg.DC,
		filesapp.WithLogger(logger),
		filesapp.WithGifCatalog(gifCatalogStore),
		filesapp.WithUploadPartBackend(uploadPartBackend),
		filesapp.WithUploadBlobAssembler(fileRemote),
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
		logger.Info("Premium promo 视频种子导入完成",
			zap.String("dir", cfg.PremiumPromoSeedDir),
			zap.Int("videos", stats.Videos),
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
	if stats, err := filesService.SeedFreezeEmoji(ctx); err != nil {
		return fmt.Errorf("seed freeze emoji: %w", err)
	} else if stats.Imported {
		logger.Info("内置 freeze 表情种子导入完成",
			zap.Int64("document_id", freezeseed.DocumentID),
			zap.Int64("set_id", freezeseed.SetID),
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
	businessAutomationStore, err := businessautomationapp.NewCachedStore(
		passwordStore,
		redisstore.NewBusinessAutomationGateCache(rdb, redisstore.DefaultBusinessAutomationGateTTL),
		logger.Named("app").Named("business-automation-cache"),
	)
	if err != nil {
		return fmt.Errorf("init business automation read model: %w", err)
	}
	helpStore := postgres.NewHelpStore(pool)
	aiComposeStore := postgres.NewAIComposeStore(pool)
	inlineRegistryStore := redisstore.NewInlineRegistryStore(rdb)
	authInvalidationBroker := redisstore.NewAuthInvalidationBroker(rdb)
	codeStore := redisstore.NewCodeStore(rdb)
	authDeliveryReportService := authdiagnosticsapp.NewService(codeStore, authDeliveryReportStore)
	clientTelemetryService := clienttelemetryapp.NewService(clientTelemetryStore)
	rateLimiter := redisstore.NewRateLimiter(rdb)
	coreLocalEdgeControl := edgecontrol.NewNoLocalController()
	edgeLocationRegistry := redisregistry.New(rdb)
	activeRawAuthKeys := edgecontrol.NewDistributedActiveRawAuthKeys(edgeLocationRegistry)
	edgeCommandBus := redisbus.New(rdb)
	sfuOwnerRegistry := sfu.NewRedisOwnerRegistry(rdb)
	sfuInstanceRegistry := sfu.NewRedisInstanceRegistry(rdb)
	edgeControl, err := edgecontrol.NewControlFabricController(coreLocalEdgeControl, edgecontrol.NewSessionControlFabric(edgecontrol.SessionControlFabricConfig{
		InstanceID: instanceID,
		Registry:   edgeLocationRegistry,
		Bus:        edgeCommandBus,
	}))
	if err != nil {
		return fmt.Errorf("create edge control fabric: %w", err)
	}
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
	go maintenance.NewRetentionWorker(tempAuthKeyStore, logger.Named("maintenance").Named("retention"),
		cfg.UpdateEventRetention,
		cfg.RetentionInterval,
		cfg.RetentionBatch,
	).WithBotAPIUpdateRetention(botAPIUpdateStore, cfg.BotAPIUpdateRetention).
		WithLoginCodeDeliveryRetention(messageStore).
		WithClientTelemetryRetention(clientTelemetryStore, 30*24*time.Hour).
		WithAuthDeliveryReportRetention(authDeliveryReportStore, 30*24*time.Hour).
		WithModerationRetention(moderationReportStore).
		WithUserUpdateRetention(updateEventStore).
		WithChannelUpdateRetention(channelStore).
		WithOrphanAuthKeyRetention(authKeyStore, activeRawAuthKeys, cfg.OrphanAuthKeyRetention).
		Run(ctx)
	langPackService := langpack.NewService(langPackStore, langpack.WithPublicBaseURL(cfg.PublicBaseURL))
	privacyService := projectionStores.NewPrivacyService()
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
	accountLifecycleStore := postgres.NewAccountLifecycleStore(pool, boxIDAllocator)
	accountOptions := []account.ServiceOption{
		account.WithReactionSettings(passwordStore),
		account.WithAccountSettings(passwordStore),
		account.WithNotifySettings(passwordStore),
		account.WithStickerCollections(passwordStore),
		account.WithUserStickerSets(passwordStore),
		account.WithSavedMusic(passwordStore),
		account.WithBusinessAutomation(businessAutomationStore),
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
		logger.Info("OTP Webhook 投递已启用",
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
		botsapp.WithTelegramLogin(telegramLoginService),
		botsapp.WithGramsrvOperatorBot(cfg.OperatorBotUserID, cfg.OperatorBotChatIDs, adminService),
		botsapp.WithDialogRateLimiter(rateLimiter, cfg.VerificationBotRateLimit, cfg.VerificationBotRateWindow),
		botsapp.WithPublicBaseURL(cfg.PublicBaseURL),
		botsapp.WithBotAPIURL(cfg.BotAPIPublicURL))
	groupCallStore := postgres.NewGroupCallStore(pool)
	groupCallsService := groupcallsapp.NewService(groupCallStore, groupcallsapp.WithPublicBaseURL(cfg.PublicBaseURL))
	if cfg.GroupCallControlAddr != "" {
		if _, err := sfu.StartGroupCallControlHTTP(ctx, sfu.GroupCallControlHTTPConfig{
			Addr:  cfg.GroupCallControlAddr,
			Token: cfg.GroupCallControlToken,
			Touch: func(ctx context.Context, callID, userID int64) error {
				_, _, err := groupCallsService.Touch(ctx, callID, userID, int(time.Now().Unix()))
				return err
			},
			Logger: logger.Named("groupcall").Named("control"),
		}); err != nil {
			return fmt.Errorf("start groupcall control: %w", err)
		}
	}
	var runRemoteSFUOwnerHeartbeat func()
	remoteSFU, closeRemoteSFU, err := newSFURemoteControl(cfg)
	if err != nil {
		return fmt.Errorf("init standalone sfu control: %w", err)
	}
	defer func() { _ = closeRemoteSFU() }()
	ownerSFU, err := sfu.NewRemoteOwnerService(sfuOwnerRegistry, instanceID, cfg.SFUOwnerTTL,
		sfu.WithOwnerSelector(sfu.NewRegistryOwnerSelector(sfuInstanceRegistry,
			sfu.WithInstanceHealthChecker(remoteSFU),
			sfu.WithInstanceHealthTimeout(cfg.SFUInstanceHealthTimeout))),
		sfu.WithRemoteService(remoteSFU))
	if err != nil {
		return fmt.Errorf("init standalone sfu owner: %w", err)
	}
	sfuService := sfu.Service(ownerSFU)
	runRemoteSFUOwnerHeartbeat = func() {
		sfu.RunRemoteOwnerHeartbeat(ctx, sfuOwnerRegistry, sfuInstanceRegistry, remoteSFU, cfg.SFUOwnerTTL, cfg.SFUOwnerHeartbeatInterval, cfg.SFUInstanceHealthTimeout)
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
	// 私聊通话中继（P3）：Core 只签发 phoneCall.connections 的 TURN REST
	// credentials；UDP listener 和 relay allocations 固定由 standalone SFU role
	// 持有。未启用时退回 P1 的纯信令 LAN/P2P 路径。
	turnService := turnsrv.Service(turnsrv.Disabled())
	if cfg.TURNEnable {
		turnAdvertise := cfg.TURNAdvertiseIP
		if turnAdvertise == "" {
			turnAdvertise = cfg.SFUAdvertiseIP
		}
		if turnAdvertise == "" {
			turnAdvertise = cfg.AdvertiseIP
		}
		issuer, err := turnsrv.NewCredentialIssuer(turnsrv.Config{
			UDPPort:       cfg.TURNUDPPort,
			AdvertiseIP:   turnAdvertise,
			SharedSecret:  cfg.TURNSecret,
			CredentialTTL: cfg.CallTURNCredentialTTL,
		})
		if err != nil {
			return fmt.Errorf("init turn credential issuer: %w", err)
		}
		turnService = issuer
		logger.Info("turn credential issuer ready",
			zap.String("advertise_ip", issuer.IP()),
			zap.Int("udp_port", issuer.Port()))
	}
	logger.Info("standalone sfu: skip startup group call participant reset", zap.String("instance_id", instanceID))
	phoneService, err := phoneapp.NewService(phoneapp.Config{
		RingTimeout:            cfg.CallRingTimeout,
		TombstoneTTL:           cfg.CallTombstoneTTL,
		MaxActivePerUser:       cfg.CallMaxActivePerUser,
		MaxRegistryEntries:     cfg.CallRegistryMaxEntries,
		SignalingRatePerSecond: cfg.CallSignalingRate,
	}, phoneapp.NewRedisActiveCallStore(rdb))
	if err != nil {
		return fmt.Errorf("init phone service: %w", err)
	}
	// 私聊端对端加密（Secret Chat）握手状态机 + qts 投递队列（盲中继）。
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
			ResaleMaxStars:        cfg.StarGiftResaleMaxStars,
			ResaleMaxTONNanoton:   cfg.StarGiftResaleMaxTON * 1_000_000_000,
			ResaleMinStars:        cfg.StarGiftResaleMinStars,
		}),
		postgres.WithStarGiftCrossCraftGenerator(newCrossCraftGenerator(cfg, blobBackend, logger)),
		postgres.WithStarGiftCrossCraftChance(cfg.StarGiftCraftChancePermille),
		postgres.WithStarGiftCrossCraftUsers(cfg.StarGiftCrossCraftUserIDs),
		postgres.WithStarGiftCrossCraftBlobReader(blobBackend))
	starGiftWithdrawalOptions, err := localStarGiftWithdrawalOptions(cfg.PublicBaseURL, cfg.PublicLinkWebAddr, cfg.StarGiftExportMode, logger)
	if err != nil {
		return fmt.Errorf("init local star gift withdrawal provider: %w", err)
	}
	starGiftOptions := []stargifts.Option{
		stargifts.WithUpgradeStore(starGiftUpgradeStore),
		stargifts.WithLifecycleStore(starGiftLifecycleStore),
	}
	starGiftOptions = append(starGiftOptions, starGiftWithdrawalOptions...)
	var tonExportService *stargifts.TONExportService
	var tonClaimService *stargifts.TONClaimService
	var tonFinalizerStore *postgres.StarGiftTONFinalizerStore
	if cfg.StarGiftExportMode == config.StarGiftExportModeTON {
		if strings.TrimSpace(cfg.PublicLinkWebAddr) == "" {
			return fmt.Errorf("TON star gift export requires the public Web listener")
		}
		capabilitySecret, err := loadTONCapabilitySecret(cfg.StarGiftTONCapabilitySecretFile)
		if err != nil {
			return fmt.Errorf("load TON star gift capability secret: %w", err)
		}
		tonStore := postgres.NewStarGiftTONStore(pool)
		tonFinalizerStore = postgres.NewStarGiftTONFinalizerStore(pool, starGiftLifecycleStore)
		tonExportService, err = stargifts.NewTONExportService(
			tonStore,
			starGiftStore,
			stargifts.TONExportConfig{
				PublicBaseURL:      cfg.PublicBaseURL,
				Network:            domain.TONNetwork(cfg.StarGiftTONNetwork),
				Collection:         cfg.StarGiftTONCollectionAddress,
				CollectionCodeHash: cfg.StarGiftTONCollectionCodeHash,
				MintABI:            cfg.StarGiftTONMintABI,
				InitialItemIndex:   cfg.StarGiftTONInitialItemIndex,
				ProofDomain:        cfg.StarGiftTONProofDomain,
				ExportTTL:          cfg.StarGiftTONExportTTL,
				ChallengeTTL:       cfg.StarGiftTONChallengeTTL,
				CapabilitySecret:   capabilitySecret,
				AllowUserIDs:       cfg.StarGiftTONAllowUserIDs,
			},
		)
		if err != nil {
			return fmt.Errorf("init TON star gift export provider: %w", err)
		}
		starGiftOptions = append(starGiftOptions, stargifts.WithGiftWithdrawalProvider(tonExportService))
		if cfg.StarGiftTONClaimEnabled {
			botToken, err := loadTONClaimBotToken(cfg.StarGiftTONClaimBotTokenFile)
			if err != nil {
				return fmt.Errorf("load TON star gift claim bot token: %w", err)
			}
			claimBotID, claimBotSecret, _ := domain.ParseBotToken(botToken)
			claimBot, found, err := botStore.GetBot(ctx, claimBotID)
			if err != nil {
				return fmt.Errorf("load TON star gift claim bot: %w", err)
			}
			if !found || claimBot.TokenSecret != claimBotSecret {
				return fmt.Errorf("TON star gift claim bot token does not match an active local bot")
			}
			tonClaimService, err = stargifts.NewTONClaimService(tonFinalizerStore, stargifts.TONClaimConfig{
				Network: domain.TONNetwork(cfg.StarGiftTONNetwork), ProofDomain: cfg.StarGiftTONProofDomain,
				BotToken: botToken, ChallengeTTL: cfg.StarGiftTONChallengeTTL, InitDataTTL: cfg.StarGiftTONClaimInitDataTTL,
			})
			if err != nil {
				return fmt.Errorf("init TON star gift claim service: %w", err)
			}
			claimButton := domain.BotMenuButton{
				Type: domain.BotMenuButtonWebView, Text: "Claim Gift",
				URL: strings.TrimRight(cfg.PublicBaseURL, "/") + "/ton-gift/claim",
			}
			currentButton, err := botsService.GetBotMenuButton(ctx, claimBotID)
			if err != nil {
				return fmt.Errorf("load TON star gift claim bot menu: %w", err)
			}
			if currentButton != claimButton {
				_, err = botsService.SetBotMenuButton(ctx, claimBotID, claimButton)
			} else {
				_, _, err = botsService.EnsureMenuBotApp(ctx, claimBotID, claimButton)
			}
			if err != nil {
				return fmt.Errorf("configure TON star gift claim Mini App: %w", err)
			}
		}
	}
	giftsService := stargifts.NewService(starGiftStore, blobBackend, cfg.DC, starGiftOptions...)
	domain.SetModerationProfileWarning(cfg.ScamProfileWarning, cfg.FakeProfileWarning)
	// Crypto donations (D7): every Core decrypts the wallet with the same shared
	// key file; only the donations lease holder provisions the wallet and runs
	// the chain watchers. No key file means the feature is off, while the chain
	// registry stays editable.
	donationStore := postgres.NewDonationStore(pool)
	donationsService := donationsapp.NewConfigOnlyService(donationStore)
	if cfg.DonationWalletKeyFile != "" {
		donationWalletKey, err := donationsapp.LoadEncryptionKeyFile(cfg.DonationWalletKeyFile)
		if err != nil {
			return fmt.Errorf("init donations wallet key: %w", err)
		}
		donationsService, err = donationsapp.NewService(ctx, donationStore, donationWalletKey)
		if err != nil {
			return fmt.Errorf("init donations service: %w", err)
		}
	}
	botsService.SetDonationsSource(donationsService)
	donationsService.SetNotifier(botsService)
	// Passkey:凭据持久化走 postgres;一次性挑战走进程内内存(短 TTL,与 QR 登录 token
	// 同属进程内一次性凭据,不跨实例)。
	passkeyStore := postgres.NewPasskeyStore(pool)
	passkeyChallengeStore := memory.NewPasskeyChallengeStore()
	passkeyService := passkeyapp.NewService(passkeyStore, passkeyChallengeStore, cfg.PasskeyRPID, cfg.DC,
		passkeyapp.WithAllowedOrigins(cfg.PasskeyAllowedOrigins))
	// 自定义云主题(Create a New Theme):主题目录与每用户已安装列表均持久化到 postgres。
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
	businessAutomationOptions := newBusinessAutomationOptions(cfg, edgeControl, aiComposeService, logger)
	messagesService := messageapp.NewService(messageStore, dialogStore,
		messageapp.WithContactStore(contactStore),
		messageapp.WithPhotoProvider(cachedPhotos),
		messageapp.WithPrivacyEvaluator(privacyService),
		messageapp.WithAccountFreezeProvider(userProjectionFacts),
		messageapp.WithCollectiblePhoneProvider(userProjectionFacts),
		messageapp.WithReadModelVersions(readModelVersionStore),
		messageapp.WithBotResponder(botsService),
		messageapp.WithSendPermissionChecker(adminService),
		messageapp.WithBusinessAutomation(businessAutomationStore, businessAutomationOptions...),
		// The recipient probe only needs the unprojected row, so the send gate
		// reads the tombstone without viewer projection.
		messageapp.WithRecipientUserReader(userStore),
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
		logger.Info("旧 ephemeral 举报已迁移到统一审核管线",
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
			logger.Named("otp").Warn("附加 OTP provider 投递失败，777000 App-code 保持有效",
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
	collectibleUsernameStore := postgres.NewCollectibleUsernameStore(pool, postgres.WithCollectibleUsernameTONStartingGrant(cfg.StarGiftTONStartingGrant))
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
	verificationVerifier := &verificationPeerVerifier{
		users:           usersService,
		channels:        channelsService,
		channelRowCache: channelRowCache,
	}
	verificationService := verificationapp.NewService(
		verificationapp.WithStore(verificationStore),
		verificationapp.WithUserDirectory(usersService),
		verificationapp.WithBotDirectory(botsService),
		verificationapp.WithChannelDirectory(channelsService),
		verificationapp.WithAccountFreezeProvider(adminService),
		verificationapp.WithPeerVerifier(verificationVerifier),
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
	// 会话地理归属(account.getAuthorizations 的 country/region)。协议层没有
	// help.requestIpAddress 的对等实现,不配 endpoint 时 resolver 为 nil,列表继续回传
	// "Unknown" 占位文案——这是未启用时的预期路径,不该让服务起不来。
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
		// failover 链是有序的,启动时打出来,排查"为什么这条会话显示 Unknown"时能直接
		// 看出当时主力是哪个。
		logger.Info("会话地理归属已启用",
			zap.Strings("backends", geoip.EndpointNames(cfg.GeoIPEndpoints)))
	} else {
		logger.Info("会话地理归属未启用：account.getAuthorizations 继续回传 Unknown 占位文案")
	}
	router := rpc.New(rpc.Config{
		DevStarsPaymentsEnabled:  cfg.DevStarsPaymentsEnabled,
		DC:                       cfg.DC,
		DefaultCountryCode:       cfg.DefaultCountryCode,
		IP:                       cfg.AdvertiseIP,
		Port:                     cfg.AdvertisePort,
		InstanceID:               instanceID,
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
		StarGiftTONStarsRate:     cfg.StarGiftTONStarsRate,
		// PFS temp→perm 解析缓存：显式撤销会清缓存并断开连接，re-bind 即时失效；
		// 配置 TTL 只承担跨进程/异常失效兜底，避免大连接数周期性打满 PG。
		TempKeyResolveCacheTTL:         cfg.TempKeyResolveCacheTTL,
		TempKeyResolveCacheMaxEntries:  cfg.TempKeyResolveCacheMaxEntries,
		AuthUserCacheTTL:               cfg.AuthUserCacheTTL,
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
		AuthInvalidations:    authInvalidationBroker,
		AuthDeliveryReports:  authDeliveryReportService,
		ClientTelemetry:      clientTelemetryService,
		AuthKeySessionLayers: authKeySessionLayers,
		ReadModelVersions:    readModelVersionStore,
		UserProjectionFacts:  userProjectionFacts,
		Account:              accountService,
		Privacy:              privacyService,
		Help: help.NewService(helpStore, helpStore,
			help.WithMapboxToken(cfg.MapboxToken),
			help.WithPremiumBotUsername(cfg.PremiumBotUsername),
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
		DeliveryOutbox:               deliveryOutboxStore,
		BootstrapUpdates:             bootstrapUpdateStore,
		BotAPIUpdates:                botAPIUpdateStore,
		BotCallbacks:                 botCallbackStore,
		BotPreCheckouts:              botPreCheckoutStore,
		LoginTokens:                  loginTokenStore,
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
		MainTabs:                     postgres.NewPeerMainTabStore(pool),
		Passkey:                      passkeyService,
		Themes:                       themeService,
		GeoIP:                        geoIPResolver,
		GroupCalls:                   groupCallsService,
		LiveStreams:                  liveStreamDep(liveStreamService),
		SFU:                          sfuService,
		TURN:                         turnService,
		LangPack:                     langPackService,
		Sessions:                     edgeControl,
		Metrics:                      metricRegistry,
		Inline:                       inlineRegistryStore,
		Limiter:                      rateLimiter,
	}, logger.Named("rpc"), clock.System)
	registerPresenceWorkMetrics(metricRegistry, router)
	verificationVerifier.userDelivery = router.UserAudienceDeliveryEffects
	usernamesService.SetUserAudienceDelivery(router.UsernameAudienceDeliveryEffects)
	botVerificationService.SetUserAudienceDelivery(router.UserAudienceDeliveryEffects)
	// Assign embedded avatars to the built-in system account and bots (idempotent).
	// Runs after EnsurePremiumBotIdentity so the configured Premium bot ID exists,
	// after the router so each avatar write commits its durable delivery fact,
	// and before CoreExec admits the first RPC.
	if err := botavatars.Seed(ctx, filesService, router, time.Now().Unix()); err != nil {
		return fmt.Errorf("seed bot avatars: %w", err)
	}
	// The official system account (777000) follows Server identity: seed its
	// avatar from the operator icon (or the bundled default) through the same
	// durable delivery boundary. The seed is fingerprinted in seed_states, so
	// every instance applying it at startup and from its watcher converges on
	// one avatar without churn.
	seedOfficialAvatar := func(ctx context.Context, icon []byte, now int64) (bool, error) {
		return botavatars.SeedOfficialSystemAvatar(ctx, filesService, router, icon, now)
	}
	if _, err := systemidentity.Apply(ctx, identityStore, seedOfficialAvatar, time.Now().Unix()); err != nil {
		// Identity is a deployment branding layer: a malformed or unreadable
		// identity.json must not stop Core from booting.
		logger.Warn("apply server identity failed", zap.Error(err))
	}
	go (&systemidentity.Watcher{
		Store:      identityStore,
		SeedAvatar: seedOfficialAvatar,
		Logger:     logger.Named("server-identity"),
	}).Run(ctx)
	if _, err := coreexec.StartGRPC(ctx, coreexec.GRPCServerConfig{
		Addr:              cfg.CoreExecGRPCAddr,
		InstanceID:        instanceID,
		Token:             cfg.CoreExecToken,
		TLSCertFile:       cfg.CoreExecGRPCTLSCertFile,
		TLSKeyFile:        cfg.CoreExecGRPCTLSKeyFile,
		TLSClientCAFile:   cfg.CoreExecGRPCTLSClientCAFile,
		Handler:           router,
		AuthKeys:          authKeyGetBatchStore,
		AuthInvalidations: authInvalidationBroker,
		Logger:            logger.Named("coreexec").Named("grpc"),
		Metrics:           metricRegistry,
	}); err != nil {
		return fmt.Errorf("start coreexec grpc: %w", err)
	}
	readModelCaches := projectionStores.ReadModelCacheSet(nodeprojection.CacheSetDeps{
		ContactExtras:       []postgres.ContactReadModelCache{contactsService},
		Dialogs:             dialogsService,
		Privacy:             privacyService,
		Stories:             router,
		ChannelFullBots:     router,
		ChannelBotMembers:   channelsService,
		ChannelMediaCounts:  channelsService,
		PrivateMediaCounts:  messagesService,
		RPCProjections:      router,
		PeerIdentities:      router,
		BotProfiles:         botsService,
		StarGifts:           giftsService,
		AccountSettings:     router,
		AccountFreezes:      adminService,
		UserProjectionFacts: userProjectionFacts,
		BusinessAutomation:  businessAutomationStore,
	})
	readModelCaches.ChannelTopMessages = channelTopMessageCache
	readModelCaches.ChannelDifferences = channelDifferenceCache
	readModelCaches.CommunityCatalog = communityCatalogCache
	readModelListener := postgres.NewReadModelChangeListener(cfg.PostgresDSN, readModelCaches, logger.Named("store").Named("read-model-listener"))
	go readModelListener.Run(ctx)
	go readModelRedisBus.Run(ctx,
		func() { readModelListener.FlushRelayedReadModelCaches("redis_subscribe") },
		readModelListener.HandleReadModelInvalidations,
	)
	readModelRelay, err := postgres.NewReadModelInvalidationRelay(
		pool, readModelRedisBus, instanceID, logger.Named("store").Named("read-model-relay"),
	)
	if err != nil {
		return fmt.Errorf("init dialog read-model invalidation relay: %w", err)
	}
	go readModelRelay.Run(ctx)
	logger.Info("core role: edge session heartbeat and local command subscribers disabled",
		zap.String("instance_id", instanceID))
	if runRemoteSFUOwnerHeartbeat != nil {
		go runRemoteSFUOwnerHeartbeat()
	}
	adminService.Configure(adminapp.Dependencies{
		Auth:                    authService,
		Revoker:                 router,
		Users:                   usersService,
		UserLookup:              userStore,
		UserAudienceDelivery:    router.UserAudienceDeliveryEffects,
		Account:                 accountService,
		Photos:                  filesService,
		Donations:               donationsService,
		AvatarDelivery:          router,
		Stars:                   starsService,
		StarsDelivery:           router.StarsBalanceDeliveryEffects,
		Premium:                 premiumService,
		PremiumUserDelivery:     router.PremiumUserDeliveryEffects,
		PremiumRefundDelivery:   router.PremiumRefundDeliveryEffects,
		UserProjectionCache:     router,
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
	})
	// The RPC edge owns the tg.* projection cache and the standard non-PTS
	// updateUser/updateChannel refresh, so committed registry mutations are
	// visible to online viewers immediately.
	usernamesService.SetPeerUsernameNotifier(router)
	// The badge change is a peer fact the protocol edge caches and projects. The
	// concrete Router is a compile-time mandatory notifier; production startup no
	// longer carries a cache-only capability fallback.
	verificationService.SetPeerNotifier(compositeVerificationNotifier{
		cache: rpcProjectionVerificationNotifier{
			invalidator: router,
			users:       userCache,
			log:         verificationLogger,
		},
		edge: router,
	})
	// The third-party mark lives on the same peer projections as the official flag,
	// so it needs the same edge hook. Composed with the cache drop for the same reason:
	// the mark can be written on the decision's own transaction, bypassing the app
	// services that would otherwise refresh the shared user:base entry.
	botVerificationService.SetPeerNotifier(compositeBotVerificationNotifier{
		cache: rpcProjectionVerificationNotifier{
			invalidator: router,
			users:       userCache,
			log:         verificationLogger,
		},
		edge: router,
	})
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
		adminService, channelsService, accountLifecycleStore,
		moderationActionOptions...,
	)
	go moderationapp.NewActionWorker(
		moderationReportStore,
		moderationActionExecutor,
		logger.Named("moderation").Named("actions"),
	).Run(ctx)
	// bot session 撤销、在线通知与 @ChatBot 流式草稿推送经 router 实现（需 tg.* 边界），
	// router 创建后注入。
	botsService.SetRouterHooks(router)
	botsService.SetTextDraftPusher(router)
	transcriptionService.SetCompletionDelivery(router.TranscribedAudioDelivery)
	donationsService.SetBalanceDelivery(router.StarsBalanceDeliveryEffects)
	if donationsService.KeyConfigured() {
		donationLog := logger.Named("donations")
		go postgres.RunWithAdvisoryLease(ctx, pool, "telesrv-donations-leader", 30*time.Second, donationLog, func(leaseCtx context.Context) error {
			return donationsService.RunLeader(leaseCtx, cfg.DonationPollInterval, donationLog)
		})
	}
	if tonFinalizerStore != nil {
		finalizer, err := tonfinalizer.New(tonFinalizerStore, tonfinalizer.Config{
			WorkerID: instanceID + ":ton-finalizer", Batch: cfg.StarGiftTONFinalizerBatch,
			PollInterval: cfg.StarGiftTONFinalizerPollInterval, LeaseTimeout: cfg.StarGiftTONFinalizerLeaseTimeout,
			RequestTimeout: cfg.StarGiftTONFinalizerRequestTimeout, RetryDelay: cfg.StarGiftTONFinalizerRetryDelay,
			OnChanged: func(result domain.StarGiftTONFinalizationResult) {
				router.InvalidateStarGiftProfiles(result.PreviousHost, result.Gift.Host)
			},
		}, logger.Named("ton").Named("finalizer"))
		if err != nil {
			return fmt.Errorf("init TON star gift finalizer: %w", err)
		}
		go func() {
			if err := finalizer.Run(ctx); err != nil && ctx.Err() == nil {
				logger.Error("TON star gift finalizer exited", zap.Error(err))
			}
		}()
	}
	bootstrapWake := make(chan struct{}, 1)
	wakeBootstrap := func() {
		select {
		case bootstrapWake <- struct{}{}:
		default:
		}
	}
	bootstrapReadyListener := postgres.NewBootstrapUpdateReadyListener(cfg.PostgresDSN, logger.Named("rpc").Named("bootstrap").Named("ready-listener"))
	go bootstrapReadyListener.Run(ctx, wakeBootstrap)
	go rpc.NewBootstrapUpdateDispatcher(router, logger.Named("rpc").Named("bootstrap")).RunWithWake(ctx, bootstrapWake)
	go rpc.NewScheduledDispatcher(router, logger.Named("rpc").Named("scheduled")).Run(ctx)
	go rpc.NewSuggestedPostDispatcher(router, logger.Named("rpc").Named("suggested-post")).Run(ctx)
	go rpc.NewExpiryDispatcher(router, logger.Named("rpc").Named("expiry")).Run(ctx)
	go rpc.NewPhoneExpiryDispatcher(router, logger.Named("rpc").Named("phone-expiry"), cfg.CallExpiryInterval).Run(ctx)
	go rpc.NewGroupCallSweepDispatcher(router, logger.Named("rpc").Named("groupcall-sweep"), cfg.GroupCallSweepInterval, cfg.GroupCallCheckTTL).Run(ctx)
	go router.RunBotAPIEnqueue(ctx)
	go router.RunPresenceLastSeenBatch(ctx)
	go router.RunPresenceSweeper(ctx, time.Minute)
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
	go router.RunEphemeralPushSubscriber(ctx)
	go router.RunAuthInvalidationSubscriber(ctx)
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
		Addr:                    cfg.PublicLinkWebAddr,
		PublicBaseURL:           cfg.PublicBaseURL,
		AppScheme:               cfg.PublicAppScheme,
		AppLinkBase:             cfg.PublicAppLinkBase,
		WebBaseURL:              cfg.PublicWebBaseURL,
		AppName:                 cfg.PublicAppName,
		DevStarsPaymentsEnabled: cfg.DevStarsPaymentsEnabled,
		StickerSets:             filesService,
		Users:                   userStore,
		Channels:                channelStore,
		Privacy:                 privacyService,
		Photos:                  filesService,
		UniqueGifts:             giftsService,
		GiftWithdrawals:         giftsService,
		RevenueWithdrawals:      giftsService,
		TONGiftExports:          tonExportService,
		TONGiftClaims:           tonClaimService,
		TONGiftFiles:            filesService,
		MiniApps: web.NewConfiguredMiniAppsHandler(web.MiniAppsConfig{
			AppName:  cfg.PublicAppName,
			Bots:     miniAppBots{router: router, bots: botsService},
			Stickers: miniAppStickers{router: router, files: filesService},
			Tokens:   botStore,
			// Mini App initData is signed with the launching bot's token. These
			// optional overrides stay server-side; the pages never receive them.
			BotFatherToken: cfg.MiniAppBotFatherToken,
			StickersToken:  cfg.MiniAppStickersToken,
		}),
		ModerationAppeals:      moderationService,
		TelegramLogin:          telegramLoginHTTPHandler,
		GiftPreviewCacheDir:    cfg.GiftPreviewCacheDir,
		GiftPreviewRendererURL: cfg.GiftPreviewRendererURL,
	}, logger.Named("public-web")); err != nil {
		return fmt.Errorf("start public Web: %w", err)
	}

	logger.Info("telesrv core role ready",
		zap.String("core_exec_grpc_addr", cfg.CoreExecGRPCAddr),
		zap.String("instance_id", instanceID),
		zap.Int("pid", os.Getpid()),
		zap.String("git_commit", buildMeta.Commit),
		zap.Uint("schema_version", migrationStatus.Version),
	)
	<-ctx.Done()
	return nil
}

// telegramLoginRPCDependency preserves a disabled Telegram Login service as a
// nil interface. Assigning the nil *Service directly to rpc.Deps would create a
// non-nil interface with a nil concrete pointer and bypass Router availability

func telegramLoginRPCDependency(service *telegramloginapp.Service) rpc.TelegramLoginService {
	if service == nil {
		return nil
	}
	return service
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

// miniAppBots and miniAppStickers adapt the services to the public Web Mini
// Apps: reads go straight to the services, writes through the Router bridges
// so they commit the same durable deliveries as the MTProto RPCs.
type miniAppBots struct {
	router *rpc.Router
	bots   *botsapp.Service
}

func (m miniAppBots) CreateBot(ctx context.Context, ownerUserID int64, name, username string) (domain.User, string, error) {
	return m.router.MiniAppCreateBot(ctx, ownerUserID, name, username)
}

func (m miniAppBots) ListOwnedBots(ctx context.Context, ownerUserID int64) ([]domain.User, error) {
	return m.bots.ListOwnedBots(ctx, ownerUserID)
}

func (m miniAppBots) SetBotInfo(ctx context.Context, botUserID int64, update domain.BotInfoUpdate) (int, error) {
	return m.router.MiniAppSetBotInfo(ctx, botUserID, update)
}

type miniAppStickers struct {
	router *rpc.Router
	files  *filesapp.Service
}

func (m miniAppStickers) ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	return m.files.ListStickerSets(ctx, kind)
}

func (m miniAppStickers) ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	return m.files.ResolveStickerSet(ctx, ref)
}

func (m miniAppStickers) ListCreatedStickerSets(ctx context.Context, userID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error) {
	return m.files.ListCreatedStickerSets(ctx, userID, offsetID, limit)
}

func (m miniAppStickers) CreateStickerSet(ctx context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error) {
	return m.router.MiniAppCreateStickerSet(ctx, req)
}
