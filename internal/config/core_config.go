package config

import (
	"fmt"
	"strings"
	"time"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
)

type CoreConfigYAML struct {
	Version       int `yaml:"version"`
	CommonYAML    `yaml:",inline"`
	Branding      BrandingYAML      `yaml:"branding"`
	CoreExec      GRPCServerYAML    `yaml:"core_exec"`
	FileData      GRPCClientYAML    `yaml:"file_data"`
	GroupCall     GroupCallYAML     `yaml:"group_call"`
	SFU           CoreSFUYAML       `yaml:"sfu"`
	HTTP          CoreHTTPYAML      `yaml:"http"`
	Media         MediaYAML         `yaml:"media"`
	Seeds         SeedsYAML         `yaml:"seeds"`
	Auth          AuthYAML          `yaml:"auth"`
	TURN          CoreTURNYAML      `yaml:"turn"`
	LiveStream    LiveStreamYAML    `yaml:"livestream"`
	Outbox        OutboxYAML        `yaml:"outbox"`
	Cache         CoreCacheYAML     `yaml:"cache"`
	TelegramLogin TelegramLoginYAML `yaml:"telegram_login"`
	AI            AIYAML            `yaml:"ai"`
	Translation   TranslationYAML   `yaml:"translation"`
	Transcription TranscriptionYAML `yaml:"transcription"`
	Donations     DonationsYAML     `yaml:"donations"`
	OperatorBot   OperatorBotYAML   `yaml:"operator_bot"`
	MiniApps      MiniAppsYAML      `yaml:"mini_apps"`
	Moderation    ModerationYAML    `yaml:"moderation"`
	StarGifts     StarGiftsYAML     `yaml:"star_gifts"`
	Identity      IdentityYAML      `yaml:"identity"`
	GeoIP         GeoIPYAML         `yaml:"geoip"`
	SensitiveRate SensitiveRateYAML `yaml:"sensitive_rate_limits"`
}

// SensitiveRateYAML bounds 2FA verification, withdrawals and money-moving
// payments.* RPCs per user; a limit <= 0 disables that group.
type SensitiveRateYAML struct {
	SRPLimit         *int   `yaml:"srp_limit"`
	SRPWindow        string `yaml:"srp_window"`
	WithdrawalLimit  *int   `yaml:"withdrawal_limit"`
	WithdrawalWindow string `yaml:"withdrawal_window"`
	PaymentLimit     *int   `yaml:"payment_limit"`
	PaymentWindow    string `yaml:"payment_window"`
}

// GeoIPYAML configures the opt-in account.getAuthorizations country/region
// lookup. An empty endpoints list keeps the "Unknown" placeholders.
type GeoIPYAML struct {
	Endpoints          []string `yaml:"endpoints"`
	Timeout            string   `yaml:"timeout"`
	Concurrency        *int     `yaml:"concurrency"`
	CacheTTL           string   `yaml:"cache_ttl"`
	NegativeTTL        string   `yaml:"negative_ttl"`
	CacheSize          *int     `yaml:"cache_size"`
	RateLimitThreshold *int     `yaml:"rate_limit_threshold"`
	RateLimitCooldown  string   `yaml:"rate_limit_cooldown"`
	DownThreshold      *int     `yaml:"down_threshold"`
	DownCooldown       string   `yaml:"down_cooldown"`
}

type GroupCallYAML struct {
	ControlAddr  string `yaml:"control_addr"`
	ControlURL   string `yaml:"control_url"`
	ControlToken string `yaml:"control_token"`
}

type CoreSFUYAML struct {
	Control GRPCClientYAML `yaml:"control"`
	Owner   SFUOwnerYAML   `yaml:"owner"`
}

type SFUOwnerYAML struct {
	TTL               string `yaml:"ttl"`
	HeartbeatInterval string `yaml:"heartbeat_interval"`
	HealthTimeout     string `yaml:"health_timeout"`
}

type CoreHTTPYAML struct {
	BotAPIAddr        *string                `yaml:"bot_api_addr"`
	AdminAPIAddr      *string                `yaml:"admin_api_addr"`
	AdminAPIToken     string                 `yaml:"admin_api_token"`
	AdminScopedTokens []AdminScopedTokenYAML `yaml:"admin_scoped_tokens"`
	PublicLinkWebAddr *string                `yaml:"public_link_web_addr"`
	// GiftPreviewCacheDir / GiftPreviewRendererURL enable static gift
	// preview images (see internal/web's uniqueGiftPreviewImage); both
	// empty/absent leaves the og:image tag off /nft/{slug} pages.
	GiftPreviewCacheDir    *string `yaml:"gift_preview_cache_dir"`
	GiftPreviewRendererURL *string `yaml:"gift_preview_renderer_url"`
}

type AdminScopedTokenYAML struct {
	Name        string   `yaml:"name"`
	Token       string   `yaml:"token"`
	Permissions []string `yaml:"permissions"`
}

type MediaYAML struct {
	MapboxToken              string `yaml:"mapbox_token"`
	MapTileCacheDir          string `yaml:"map_tile_cache_dir"`
	ExternalMediaEnable      *bool  `yaml:"external_media_enable"`
	ExternalMediaMaxBytes    *int64 `yaml:"external_media_max_bytes"`
	ExternalMediaRatePerMin  *int   `yaml:"external_media_rate_per_min"`
	WebPagePreviewEnable     *bool  `yaml:"web_page_preview_enable"`
	WebPagePreviewMaxBytes   *int64 `yaml:"web_page_preview_max_bytes"`
	WebPagePreviewRatePerMin *int   `yaml:"web_page_preview_rate_per_min"`
}

type StarGiftsYAML struct {
	TONStartingGrant         *int64             `yaml:"ton_starting_grant"`
	SweepInterval            string             `yaml:"sweep_interval"`
	SweepBatch               *int               `yaml:"sweep_batch"`
	TransferStars            *int64             `yaml:"transfer_stars"`
	DropOriginalDetailsStars *int64             `yaml:"drop_original_details_stars"`
	OfferMinStars            *int               `yaml:"offer_min_stars"`
	StarsProceedsPermille    *int               `yaml:"stars_proceeds_permille"`
	TONProceedsPermille      *int               `yaml:"ton_proceeds_permille"`
	ExportDelay              string             `yaml:"export_delay"`
	TransferDelay            string             `yaml:"transfer_delay"`
	ResellDelay              string             `yaml:"resell_delay"`
	CraftDelay               string             `yaml:"craft_delay"`
	CraftChancePermille      *int               `yaml:"craft_chance_permille"`
	CrossCraftUserIDs        []int64            `yaml:"cross_craft_user_ids"`
	Export                   StarGiftExportYAML `yaml:"export"`
}

type StarGiftExportYAML struct {
	Mode string                `yaml:"mode"`
	TON  StarGiftExportTONYAML `yaml:"ton"`
}

type StarGiftExportTONYAML struct {
	Network              string                   `yaml:"network"`
	CollectionAddress    string                   `yaml:"collection_address"`
	CollectionCodeHash   string                   `yaml:"collection_code_hash"`
	MintABI              string                   `yaml:"mint_abi"`
	InitialItemIndex     string                   `yaml:"initial_item_index"`
	ProofDomain          string                   `yaml:"proof_domain"`
	CapabilitySecretFile string                   `yaml:"capability_secret_file"`
	AllowUserIDs         []int64                  `yaml:"allow_user_ids"`
	TTL                  string                   `yaml:"ttl"`
	ChallengeTTL         string                   `yaml:"challenge_ttl"`
	Finalizer            StarGiftTONFinalizerYAML `yaml:"finalizer"`
	Claim                StarGiftTONClaimYAML     `yaml:"claim"`
}

type StarGiftTONFinalizerYAML struct {
	Batch          *int   `yaml:"batch"`
	PollInterval   string `yaml:"poll_interval"`
	LeaseTimeout   string `yaml:"lease_timeout"`
	RequestTimeout string `yaml:"request_timeout"`
	RetryDelay     string `yaml:"retry_delay"`
}

type StarGiftTONClaimYAML struct {
	Enabled      *bool  `yaml:"enabled"`
	BotTokenFile string `yaml:"bot_token_file"`
	InitDataTTL  string `yaml:"init_data_ttl"`
}

type SeedsYAML struct {
	LangPackDir       string `yaml:"langpack_dir"`
	OfficialGiftsDir  string `yaml:"official_gifts_dir"`
	StickerDir        string `yaml:"sticker_dir"`
	GifDir            string `yaml:"gif_dir"`
	PremiumPromoDir   string `yaml:"premium_promo_dir"`
	StickerSeedMaxSet *int   `yaml:"sticker_seed_max_sets"`
}

type AuthYAML struct {
	DevCode              string   `yaml:"dev_code"`
	LoginCodeTemplate    string   `yaml:"login_code_message_template"`
	CodeTTL              string   `yaml:"code_ttl"`
	CodeMaxAttempts      *int     `yaml:"code_max_attempts"`
	PhoneRateLimit       *int     `yaml:"phone_rate_limit"`
	AuthKeyRateLimit     *int     `yaml:"auth_key_rate_limit"`
	RateWindow           string   `yaml:"rate_window"`
	PhoneDelivery        string   `yaml:"phone_delivery"`
	PhoneCodeLength      *int     `yaml:"phone_code_length"`
	LoginEmailEnable     *bool    `yaml:"login_email_enable"`
	LoginEmailRequireSet *bool    `yaml:"login_email_require_setup"`
	LoginEmailCodeLength *int     `yaml:"login_email_code_length"`
	EmailDelivery        string   `yaml:"email_delivery"`
	OTPWebhookURL        string   `yaml:"otp_webhook_url"`
	OTPWebhookSecret     string   `yaml:"otp_webhook_secret"`
	OTPWebhookTimeout    string   `yaml:"otp_webhook_timeout"`
	SMTP                 SMTPYAML `yaml:"smtp"`
	CheckNumberFormat    *bool    `yaml:"check_number_format"`
}

type SMTPYAML struct {
	Host     string `yaml:"host"`
	Port     *int   `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	From     string `yaml:"from"`
	FromName string `yaml:"from_name"`
	TLSMode  string `yaml:"tls"`
	Timeout  string `yaml:"timeout"`
}

type OutboxYAML struct {
	Workers                *int   `yaml:"workers"`
	Batch                  *int   `yaml:"batch"`
	LeaseTimeout           string `yaml:"lease_timeout"`
	OutboundPushTimeout    string `yaml:"outbound_push_timeout"`
	UpdateEventRetention   string `yaml:"update_event_retention"`
	BotAPIUpdateRetention  string `yaml:"bot_api_update_retention"`
	OrphanAuthKeyRetention string `yaml:"orphan_auth_key_retention"`
	RetentionInterval      string `yaml:"retention_interval"`
	RetentionBatch         *int   `yaml:"retention_batch"`
}

type CoreCacheYAML struct {
	TempKey       CacheYAML `yaml:"temp_key"`
	AuthUser      CacheYAML `yaml:"auth_user"`
	ChannelRow    CacheYAML `yaml:"channel_row"`
	ChannelMember CacheYAML `yaml:"channel_member"`
	ChannelDialog CacheYAML `yaml:"channel_dialog"`
	ChannelBoost  CacheYAML `yaml:"channel_boost"`
}

type TelegramLoginYAML struct {
	Enabled           *bool    `yaml:"enabled"`
	Issuer            string   `yaml:"issuer"`
	AllowHTTP         *bool    `yaml:"allow_http"`
	SigningKeysFile   string   `yaml:"signing_keys_file"`
	CodeKeysFile      string   `yaml:"code_keys_file"`
	SecretPepperFile  string   `yaml:"secret_pepper_file"`
	RequestTTL        string   `yaml:"request_ttl"`
	CodeTTL           string   `yaml:"code_ttl"`
	IDTokenTTL        string   `yaml:"id_token_ttl"`
	TrustedProxyCIDRs []string `yaml:"trusted_proxy_cidrs"`
	Retention         string   `yaml:"retention"`
	SweepInterval     string   `yaml:"sweep_interval"`
	SweepBatch        *int     `yaml:"sweep_batch"`
}

type AIYAML struct {
	Enabled           *bool  `yaml:"enabled"`
	Timeout           string `yaml:"timeout"`
	RateLimit         *int   `yaml:"rate_limit"`
	RateWindow        string `yaml:"rate_window"`
	PrivacyLogContent *bool  `yaml:"privacy_log_content"`
	BusinessProvider  string `yaml:"business_provider"`
}

type TranslationYAML struct {
	Enabled    *bool    `yaml:"enabled"`
	Providers  []string `yaml:"providers"`
	Timeout    string   `yaml:"timeout"`
	RateLimit  *int     `yaml:"rate_limit"`
	RateWindow string   `yaml:"rate_window"`
}

// ModerationYAML overrides the warning line prepended to a SCAM/FAKE-flagged
// peer's About in read projections; empty keeps the built-in English text.
type ModerationYAML struct {
	ScamProfileWarning string `yaml:"scam_profile_warning"`
	FakeProfileWarning string `yaml:"fake_profile_warning"`
}

// MiniAppsYAML holds optional bot tokens for verifying the self-hosted
// BotFather and Stickers Mini App initData. Empty values fall back to the
// system bots' stored token secrets. They are secrets: keep them out of
// tracked files.
type MiniAppsYAML struct {
	BotFatherToken string `yaml:"botfather_token"`
	StickersToken  string `yaml:"stickers_token"`
}

// OperatorBotYAML enables the in-process account-freeze operator dialog on an
// existing bot account. Both bot_user_id and chat_ids are required; the
// actions run through admin.SetAccountFrozen with its audit trail.
type OperatorBotYAML struct {
	BotUserID int64   `yaml:"bot_user_id"`
	ChatIDs   []int64 `yaml:"chat_ids"`
}

// DonationsYAML configures crypto donations. wallet_key_file must name the
// same shared key file on every Core (decision D7); empty disables donations.
type DonationsYAML struct {
	WalletKeyFile string `yaml:"wallet_key_file"`
	PollInterval  string `yaml:"poll_interval"`
}

// TranscriptionYAML points messages.transcribeAudio at a local ASR HTTP backend
// (whisper-server style /inference). An empty asr_url keeps it disabled.
type TranscriptionYAML struct {
	ASRURL     string `yaml:"asr_url"`
	ASRTimeout string `yaml:"asr_timeout"`
}

// CoreTURNYAML contains signaling-only TURN settings. The standalone SFU role
// owns relay allocation ports and the UDP listener lifecycle.
type CoreTURNYAML struct {
	Enabled       *bool  `yaml:"enabled"`
	UDPPort       *int   `yaml:"udp_port"`
	AdvertiseIP   string `yaml:"advertise_ip"`
	Secret        string `yaml:"secret"`
	CredentialTTL string `yaml:"credential_ttl"`
	ForceRelay    *bool  `yaml:"force_relay"`
}

type LiveStreamYAML struct {
	Enabled     *bool  `yaml:"enabled"`
	RtmpAddr    string `yaml:"rtmp_addr"`
	RtmpURL     string `yaml:"rtmp_url"`
	FFmpegPath  string `yaml:"ffmpeg_path"`
	WorkDir     string `yaml:"work_dir"`
	SegmentKeep *int   `yaml:"segment_keep"`
}

type CoreConfig struct {
	AdvertiseIP                        string
	AdvertisePort                      int
	DC                                 int
	DefaultCountryCode                 string
	DebugAddr                          string
	BotAPIAddr                         string
	AdminAPIAddr                       string
	AdminAPIToken                      string
	GroupCallControlAddr               string
	GroupCallControlToken              string
	CoreExecGRPCAddr                   string
	CoreExecGRPCTLSCertFile            string
	CoreExecGRPCTLSKeyFile             string
	CoreExecGRPCTLSClientCAFile        string
	CoreExecToken                      string
	FileGRPCTargets                    string
	FileGRPCResolver                   string
	FileGRPCRequestTimeout             time.Duration
	FileGRPCTLSCAFile                  string
	FileGRPCTLSServerName              string
	FileGRPCTLSClientCertFile          string
	FileGRPCTLSClientKeyFile           string
	FileToken                          string
	PublicBaseURL                      string
	Branding                           branding.Config
	UpdatePublicURL                    string
	UpdateServiceURL                   string
	UpdateRequestTimeout               time.Duration
	PublicAppScheme                    string
	PublicAppLinkBase                  string
	PublicWebBaseURL                   string
	PublicAppName                      string
	PublicLinkWebAddr                  string
	GiftPreviewCacheDir                string
	GiftPreviewRendererURL             string
	TelegramLoginEnabled               bool
	TelegramLoginIssuer                string
	TelegramLoginAllowHTTP             bool
	TelegramLoginSigningKeysFile       string
	TelegramLoginCodeKeysFile          string
	TelegramLoginSecretPepperFile      string
	TelegramLoginRequestTTL            time.Duration
	TelegramLoginCodeTTL               time.Duration
	TelegramLoginIDTokenTTL            time.Duration
	TelegramLoginTrustedProxyCIDRs     []string
	TelegramLoginRetention             time.Duration
	TelegramLoginSweepInterval         time.Duration
	TelegramLoginSweepBatch            int
	AdminScopedTokens                  []AdminScopedToken
	SRPRateLimit                       int
	SRPRateWindow                      time.Duration
	WithdrawalRateLimit                int
	WithdrawalRateWindow               time.Duration
	PaymentRateLimit                   int
	PaymentRateWindow                  time.Duration
	GeoIPEndpoints                     []string
	GeoIPTimeout                       time.Duration
	GeoIPConcurrency                   int
	GeoIPCacheTTL                      time.Duration
	GeoIPNegativeTTL                   time.Duration
	GeoIPCacheSize                     int
	GeoIPRateLimitThreshold            int
	GeoIPRateLimitCooldown             time.Duration
	GeoIPDownThreshold                 int
	GeoIPDownCooldown                  time.Duration
	PostgresDSN                        string
	PostgresMaxConns                   int
	PostgresMinConns                   int
	PostgresCounterRecoveryMaxConns    int
	RedisAddr                          string
	RedisPassword                      string
	RedisDB                            int
	InstanceID                         string
	DevAuthCode                        string
	LoginCodeMessageTemplate           string
	AuthCodeTTL                        time.Duration
	PhoneCodeLength                    int
	AuthCodeMaxAttempts                int
	AuthCodePhoneRateLimit             int
	AuthCodeAuthKeyRateLimit           int
	AuthCodeRateWindow                 time.Duration
	LoginEmailEnable                   bool
	LoginEmailRequireSetup             bool
	LoginEmailCodeLength               int
	PhoneCodeDeliveryProvider          string
	EmailCodeDeliveryProvider          string
	OTPWebhookURL                      string
	OTPWebhookSecret                   string
	OTPWebhookTimeout                  time.Duration
	SMTPHost                           string
	SMTPPort                           int
	SMTPUsername                       string
	SMTPPassword                       string
	SMTPFrom                           string
	SMTPFromName                       string
	SMTPTLSMode                        string
	SMTPTimeout                        time.Duration
	MapboxToken                        string
	MapTileCacheDir                    string
	ExternalMediaEnable                bool
	ExternalMediaMaxBytes              int64
	ExternalMediaRatePerMin            int
	WebPagePreviewEnable               bool
	WebPagePreviewMaxBytes             int64
	WebPagePreviewRatePerMin           int
	LangPackSeedDir                    string
	OfficialGiftsDir                   string
	StarGiftTONStartingGrant           int64
	StickerSeedDir                     string
	GifSeedDir                         string
	StickerSeedMaxSets                 int
	PremiumPromoSeedDir                string
	BusinessAIProvider                 string
	AIEnabled                          bool
	AIProviders                        []AIProviderConfig
	AITimeout                          time.Duration
	AIRateLimit                        int
	AIRateWindow                       time.Duration
	AIPrivacyLogContent                bool
	TranslationEnabled                 bool
	TranslationProviders               []string
	TranslationTimeout                 time.Duration
	ASRURL                             string
	ASRTimeout                         time.Duration
	DonationWalletKeyFile              string
	DonationPollInterval               time.Duration
	TranslationRateLimit               int
	TranslationRateWindow              time.Duration
	TempKeyResolveCacheMaxEntries      int
	TempKeyResolveCacheTTL             time.Duration
	ReadModelVersionCacheMaxEntries    int
	ReadModelVersionBatchMaxKeys       int
	ReadModelVersionBatchWait          time.Duration
	ReadModelVersionBatchQueue         int
	ReadModelVersionBatchTimeout       time.Duration
	AuthKeyGetBatchMax                 int
	AuthKeyGetBatchWait                time.Duration
	AuthKeyGetBatchQueue               int
	AuthKeyGetBatchTimeout             time.Duration
	ContactReverseBatchMaxPairs        int
	ContactReverseBatchWait            time.Duration
	ContactReverseBatchQueue           int
	ContactReverseBatchTimeout         time.Duration
	ContactSnapshotCacheMaxViewers     int
	ProfilePhotoCacheMaxEntries        int
	ProfilePhotoCacheTTL               time.Duration
	PeerIdentityCacheMaxEntries        int
	DialogPrivatePeerCacheMaxEntries   int
	DialogPrivatePeerCacheMaxBytes     int64
	DialogDraftCacheMaxEntries         int
	DialogDraftCacheMaxBytes           int64
	UserProjectionFactCacheMaxEntries  int
	StoryActivePeerCacheMaxEntries     int
	StoryHiddenListCacheMaxEntries     int
	StoryHiddenListCacheMaxBytes       int64
	DialogListSnapshotCacheMaxEntries  int
	DialogListSnapshotCacheMaxHeaders  int64
	DialogListSnapshotCacheTTL         time.Duration
	DialogListSnapshotRedisTTL         time.Duration
	ActiveChannelIDsCacheMaxEntries    int
	ActiveChannelIDsCacheTTL           time.Duration
	ActiveChannelIDsRedisTTL           time.Duration
	ActiveChannelIDsBatchMax           int
	ActiveChannelIDsBatchWait          time.Duration
	ActiveChannelIDsBatchQueue         int
	ActiveChannelIDsBatchTimeout       time.Duration
	BootstrapReadyBatchMax             int
	BootstrapReadyBatchWait            time.Duration
	BootstrapReadyBatchQueue           int
	BootstrapReadyBatchTimeout         time.Duration
	PresenceLastSeenBatchMax           int
	PresenceLastSeenBatchWait          time.Duration
	PresenceLastSeenBatchQueue         int
	PresenceLastSeenBatchTimeout       time.Duration
	PresenceLastSeenDrainTimeout       time.Duration
	AuthUserCacheTTL                   time.Duration
	ChannelRowCacheMaxEntries          int
	ChannelTopMessageCacheMaxEntries   int
	ChannelMemberCacheMaxEntries       int
	ChannelDialogCacheMaxEntries       int
	ChannelDifferenceCacheMaxEntries   int
	ChannelDifferenceCacheMaxBytes     int64
	ChannelDifferenceCacheTTL          time.Duration
	ChannelBoostCacheMaxEntries        int
	ChannelBoostCacheTTL               time.Duration
	OutboxLeaseTimeout                 time.Duration
	OutboundPushTimeout                time.Duration
	SendRateLimit                      int
	SendRateWindow                     time.Duration
	CatchupRateLimit                   int
	CatchupRateWindow                  time.Duration
	ChannelNudgeMaxTargets             int
	UpdateEventRetention               time.Duration
	BotAPIUpdateRetention              time.Duration
	OrphanAuthKeyRetention             time.Duration
	RetentionInterval                  time.Duration
	RetentionBatch                     int
	UploadInFlightMaxBytes             int64
	UploadInFlightMaxParts             int
	UploadInFlightMaxFiles             int
	CallRingTimeout                    time.Duration
	CallTombstoneTTL                   time.Duration
	CallMaxActivePerUser               int
	CallRegistryMaxEntries             int
	CallSignalingMaxBytes              int
	CallSignalingRate                  int
	CallExpiryInterval                 time.Duration
	PremiumGrantMonths                 int
	PremiumBotUsername                 string
	PremiumBotUserID                   int64
	IdentityDir                        string
	PremiumPlans                       []domain.PremiumPlan
	PasskeyRPID                        string
	PasskeyAllowedOrigins              []string
	StarsStartingGrant                 int64
	PremiumSweepInterval               time.Duration
	PremiumSweepBatch                  int
	StarGiftSweepInterval              time.Duration
	StarGiftSweepBatch                 int
	StarGiftTransferStars              int64
	StarGiftDropOriginalDetailsStars   int64
	StarGiftOfferMinStars              int
	StarGiftStarsProceedsPermille      int
	StarGiftTONProceedsPermille        int
	StarGiftResaleMaxStars             int64
	StarGiftResaleMaxTON               int64
	StarGiftResaleMinStars             int64
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
	StarGiftTONAllowUserIDs            []int64
	StarGiftCrossCraftUserIDs          []int64
	OperatorBotUserID                  int64
	OperatorBotChatIDs                 []int64
	ScamProfileWarning                 string
	FakeProfileWarning                 string
	CheckNumberFormat                  bool
	MiniAppBotFatherToken              string
	MiniAppStickersToken               string
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
	DevStarsPaymentsEnabled            bool
	RatingEnabled                      bool
	RatingPendingDelay                 time.Duration
	RatingRecomputeInterval            time.Duration
	RatingRecomputeBatch               int
	RatingStaleAfter                   time.Duration
	RatingWeightStarsReceivedPermille  int64
	RatingWeightStarsSpentPermille     int64
	RatingWeightMessageSent            int64
	RatingWeightAccountAgeDay          int64
	RatingWeightGiftReceived           int64
	RatingWeightModerationCase         int64
	RatingWeightScamPenalty            int64
	RatingWeightFakePenalty            int64
	RatingActivityCap                  int64
	VerificationEnabled                bool
	VerificationAllowUserTargets       bool
	VerificationRejectCooldown         time.Duration
	VerificationApplyRateLimit         int
	VerificationApplyRateWindow        time.Duration
	VerificationBotRateLimit           int
	VerificationBotRateWindow          time.Duration
	VerificationNotifyInterval         time.Duration
	VerificationNotifyBatch            int
	BroadcastWorkerInterval            time.Duration
	BroadcastWorkerLease               time.Duration
	BroadcastMaterializeBatch          int
	BroadcastDeliveryBatch             int
	VerificationMaxActivePerUser       int
	BotVerificationEnabled             bool
	BotVerificationMaxPerVerifier      int
	BotVerificationRequestRateLimit    int
	BotVerificationRequestRateWindow   time.Duration
	CollectibleUsernameURLTemplate     string
	GroupCallCheckTTL                  time.Duration
	GroupCallSweepInterval             time.Duration
	GroupCallMaxParticipants           int
	TURNEnable                         bool
	TURNUDPPort                        int
	TURNAdvertiseIP                    string
	TURNSecret                         string
	CallTURNCredentialTTL              time.Duration
	CallForceRelay                     bool
	LiveStreamEnable                   bool
	LiveStreamRtmpAddr                 string
	LiveStreamRtmpURL                  string
	LiveStreamFFmpegPath               string
	LiveStreamWorkDir                  string
	LiveStreamSegmentKeep              int
	SFUAdvertiseIP                     string
	SFUOwnerTTL                        time.Duration
	SFUOwnerHeartbeatInterval          time.Duration
	SFUInstanceHealthTimeout           time.Duration
	SFUControlGRPCRequestTimeout       time.Duration
	SFUControlGRPCTLSCAFile            string
	SFUControlGRPCTLSServerName        string
	SFUControlGRPCTLSClientCertFile    string
	SFUControlGRPCTLSClientKeyFile     string
	SFUControlToken                    string
}

func (c CoreConfig) ProcessGlobals() ProcessGlobals {
	return ProcessGlobals{Branding: c.Branding, PremiumBotUsername: c.PremiumBotUsername, PremiumBotUserID: c.PremiumBotUserID}
}

func (c CoreConfig) DebugAddress() string {
	return c.DebugAddr
}

func (c CoreConfig) AccountRatingWeights() domain.AccountRatingWeights {
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

func coreConfigFromConfig(c Config) CoreConfig {
	return CoreConfig{
		AdvertiseIP:                        c.AdvertiseIP,
		AdvertisePort:                      c.AdvertisePort,
		DC:                                 c.DC,
		DefaultCountryCode:                 c.DefaultCountryCode,
		DebugAddr:                          c.DebugAddr,
		BotAPIAddr:                         c.BotAPIAddr,
		AdminAPIAddr:                       c.AdminAPIAddr,
		AdminAPIToken:                      c.AdminAPIToken,
		GroupCallControlAddr:               c.GroupCallControlAddr,
		GroupCallControlToken:              c.GroupCallControlToken,
		CoreExecGRPCAddr:                   c.CoreExecGRPCAddr,
		CoreExecGRPCTLSCertFile:            c.CoreExecGRPCTLSCertFile,
		CoreExecGRPCTLSKeyFile:             c.CoreExecGRPCTLSKeyFile,
		CoreExecGRPCTLSClientCAFile:        c.CoreExecGRPCTLSClientCAFile,
		CoreExecToken:                      c.CoreExecToken,
		FileGRPCTargets:                    c.FileGRPCTargets,
		FileGRPCResolver:                   c.FileGRPCResolver,
		FileGRPCRequestTimeout:             c.FileGRPCRequestTimeout,
		FileGRPCTLSCAFile:                  c.FileGRPCTLSCAFile,
		FileGRPCTLSServerName:              c.FileGRPCTLSServerName,
		FileGRPCTLSClientCertFile:          c.FileGRPCTLSClientCertFile,
		FileGRPCTLSClientKeyFile:           c.FileGRPCTLSClientKeyFile,
		FileToken:                          c.FileToken,
		PublicBaseURL:                      c.PublicBaseURL,
		Branding:                           c.Branding,
		UpdatePublicURL:                    c.UpdatePublicURL,
		UpdateServiceURL:                   c.UpdateServiceURL,
		UpdateRequestTimeout:               c.UpdateRequestTimeout,
		PublicAppScheme:                    c.PublicAppScheme,
		PublicAppLinkBase:                  c.PublicAppLinkBase,
		PublicWebBaseURL:                   c.PublicWebBaseURL,
		PublicAppName:                      c.PublicAppName,
		PublicLinkWebAddr:                  c.PublicLinkWebAddr,
		GiftPreviewCacheDir:                c.GiftPreviewCacheDir,
		GiftPreviewRendererURL:             c.GiftPreviewRendererURL,
		TelegramLoginEnabled:               c.TelegramLoginEnabled,
		TelegramLoginIssuer:                c.TelegramLoginIssuer,
		TelegramLoginAllowHTTP:             c.TelegramLoginAllowHTTP,
		TelegramLoginSigningKeysFile:       c.TelegramLoginSigningKeysFile,
		TelegramLoginCodeKeysFile:          c.TelegramLoginCodeKeysFile,
		TelegramLoginSecretPepperFile:      c.TelegramLoginSecretPepperFile,
		TelegramLoginRequestTTL:            c.TelegramLoginRequestTTL,
		TelegramLoginCodeTTL:               c.TelegramLoginCodeTTL,
		TelegramLoginIDTokenTTL:            c.TelegramLoginIDTokenTTL,
		TelegramLoginTrustedProxyCIDRs:     append([]string(nil), c.TelegramLoginTrustedProxyCIDRs...),
		TelegramLoginRetention:             c.TelegramLoginRetention,
		TelegramLoginSweepInterval:         c.TelegramLoginSweepInterval,
		TelegramLoginSweepBatch:            c.TelegramLoginSweepBatch,
		AdminScopedTokens:                  append([]AdminScopedToken(nil), c.AdminScopedTokens...),
		SRPRateLimit:                       c.SRPRateLimit,
		SRPRateWindow:                      c.SRPRateWindow,
		WithdrawalRateLimit:                c.WithdrawalRateLimit,
		WithdrawalRateWindow:               c.WithdrawalRateWindow,
		PaymentRateLimit:                   c.PaymentRateLimit,
		PaymentRateWindow:                  c.PaymentRateWindow,
		GeoIPEndpoints:                     append([]string(nil), c.GeoIPEndpoints...),
		GeoIPTimeout:                       c.GeoIPTimeout,
		GeoIPConcurrency:                   c.GeoIPConcurrency,
		GeoIPCacheTTL:                      c.GeoIPCacheTTL,
		GeoIPNegativeTTL:                   c.GeoIPNegativeTTL,
		GeoIPCacheSize:                     c.GeoIPCacheSize,
		GeoIPRateLimitThreshold:            c.GeoIPRateLimitThreshold,
		GeoIPRateLimitCooldown:             c.GeoIPRateLimitCooldown,
		GeoIPDownThreshold:                 c.GeoIPDownThreshold,
		GeoIPDownCooldown:                  c.GeoIPDownCooldown,
		PostgresDSN:                        c.PostgresDSN,
		PostgresMaxConns:                   c.PostgresMaxConns,
		PostgresMinConns:                   c.PostgresMinConns,
		PostgresCounterRecoveryMaxConns:    c.PostgresCounterRecoveryMaxConns,
		RedisAddr:                          c.RedisAddr,
		RedisPassword:                      c.RedisPassword,
		RedisDB:                            c.RedisDB,
		InstanceID:                         c.InstanceID,
		DevAuthCode:                        c.DevAuthCode,
		LoginCodeMessageTemplate:           c.LoginCodeMessageTemplate,
		AuthCodeTTL:                        c.AuthCodeTTL,
		PhoneCodeLength:                    c.PhoneCodeLength,
		AuthCodeMaxAttempts:                c.AuthCodeMaxAttempts,
		AuthCodePhoneRateLimit:             c.AuthCodePhoneRateLimit,
		AuthCodeAuthKeyRateLimit:           c.AuthCodeAuthKeyRateLimit,
		AuthCodeRateWindow:                 c.AuthCodeRateWindow,
		LoginEmailEnable:                   c.LoginEmailEnable,
		LoginEmailRequireSetup:             c.LoginEmailRequireSetup,
		LoginEmailCodeLength:               c.LoginEmailCodeLength,
		PhoneCodeDeliveryProvider:          c.PhoneCodeDeliveryProvider,
		EmailCodeDeliveryProvider:          c.EmailCodeDeliveryProvider,
		OTPWebhookURL:                      c.OTPWebhookURL,
		OTPWebhookSecret:                   c.OTPWebhookSecret,
		OTPWebhookTimeout:                  c.OTPWebhookTimeout,
		SMTPHost:                           c.SMTPHost,
		SMTPPort:                           c.SMTPPort,
		SMTPUsername:                       c.SMTPUsername,
		SMTPPassword:                       c.SMTPPassword,
		SMTPFrom:                           c.SMTPFrom,
		SMTPFromName:                       c.SMTPFromName,
		SMTPTLSMode:                        c.SMTPTLSMode,
		SMTPTimeout:                        c.SMTPTimeout,
		MapboxToken:                        c.MapboxToken,
		MapTileCacheDir:                    c.MapTileCacheDir,
		ExternalMediaEnable:                c.ExternalMediaEnable,
		ExternalMediaMaxBytes:              c.ExternalMediaMaxBytes,
		ExternalMediaRatePerMin:            c.ExternalMediaRatePerMin,
		WebPagePreviewEnable:               c.WebPagePreviewEnable,
		WebPagePreviewMaxBytes:             c.WebPagePreviewMaxBytes,
		WebPagePreviewRatePerMin:           c.WebPagePreviewRatePerMin,
		LangPackSeedDir:                    c.LangPackSeedDir,
		OfficialGiftsDir:                   c.OfficialGiftsDir,
		StarGiftTONStartingGrant:           c.StarGiftTONStartingGrant,
		StickerSeedDir:                     c.StickerSeedDir,
		GifSeedDir:                         c.GifSeedDir,
		StickerSeedMaxSets:                 c.StickerSeedMaxSets,
		PremiumPromoSeedDir:                c.PremiumPromoSeedDir,
		BusinessAIProvider:                 c.BusinessAIProvider,
		AIEnabled:                          c.AIEnabled,
		AIProviders:                        append([]AIProviderConfig(nil), c.AIProviders...),
		AITimeout:                          c.AITimeout,
		AIRateLimit:                        c.AIRateLimit,
		AIRateWindow:                       c.AIRateWindow,
		AIPrivacyLogContent:                c.AIPrivacyLogContent,
		TranslationEnabled:                 c.TranslationEnabled,
		TranslationProviders:               append([]string(nil), c.TranslationProviders...),
		TranslationTimeout:                 c.TranslationTimeout,
		ScamProfileWarning:                 c.ScamProfileWarning,
		FakeProfileWarning:                 c.FakeProfileWarning,
		CheckNumberFormat:                  c.CheckNumberFormat,
		ASRURL:                             c.ASRURL,
		ASRTimeout:                         c.ASRTimeout,
		DonationWalletKeyFile:              c.DonationWalletKeyFile,
		DonationPollInterval:               c.DonationPollInterval,
		TranslationRateLimit:               c.TranslationRateLimit,
		TranslationRateWindow:              c.TranslationRateWindow,
		TempKeyResolveCacheMaxEntries:      c.TempKeyResolveCacheMaxEntries,
		TempKeyResolveCacheTTL:             c.TempKeyResolveCacheTTL,
		ReadModelVersionCacheMaxEntries:    c.ReadModelVersionCacheMaxEntries,
		ReadModelVersionBatchMaxKeys:       c.ReadModelVersionBatchMaxKeys,
		ReadModelVersionBatchWait:          c.ReadModelVersionBatchWait,
		ReadModelVersionBatchQueue:         c.ReadModelVersionBatchQueue,
		ReadModelVersionBatchTimeout:       c.ReadModelVersionBatchTimeout,
		AuthKeyGetBatchMax:                 c.AuthKeyGetBatchMax,
		AuthKeyGetBatchWait:                c.AuthKeyGetBatchWait,
		AuthKeyGetBatchQueue:               c.AuthKeyGetBatchQueue,
		AuthKeyGetBatchTimeout:             c.AuthKeyGetBatchTimeout,
		ContactReverseBatchMaxPairs:        c.ContactReverseBatchMaxPairs,
		ContactReverseBatchWait:            c.ContactReverseBatchWait,
		ContactReverseBatchQueue:           c.ContactReverseBatchQueue,
		ContactReverseBatchTimeout:         c.ContactReverseBatchTimeout,
		ContactSnapshotCacheMaxViewers:     c.ContactSnapshotCacheMaxViewers,
		ProfilePhotoCacheMaxEntries:        c.ProfilePhotoCacheMaxEntries,
		ProfilePhotoCacheTTL:               c.ProfilePhotoCacheTTL,
		PeerIdentityCacheMaxEntries:        c.PeerIdentityCacheMaxEntries,
		DialogPrivatePeerCacheMaxEntries:   c.DialogPrivatePeerCacheMaxEntries,
		DialogPrivatePeerCacheMaxBytes:     c.DialogPrivatePeerCacheMaxBytes,
		DialogDraftCacheMaxEntries:         c.DialogDraftCacheMaxEntries,
		DialogDraftCacheMaxBytes:           c.DialogDraftCacheMaxBytes,
		UserProjectionFactCacheMaxEntries:  c.UserProjectionFactCacheMaxEntries,
		StoryActivePeerCacheMaxEntries:     c.StoryActivePeerCacheMaxEntries,
		StoryHiddenListCacheMaxEntries:     c.StoryHiddenListCacheMaxEntries,
		StoryHiddenListCacheMaxBytes:       c.StoryHiddenListCacheMaxBytes,
		DialogListSnapshotCacheMaxEntries:  c.DialogListSnapshotCacheMaxEntries,
		DialogListSnapshotCacheMaxHeaders:  c.DialogListSnapshotCacheMaxHeaders,
		DialogListSnapshotCacheTTL:         c.DialogListSnapshotCacheTTL,
		DialogListSnapshotRedisTTL:         c.DialogListSnapshotRedisTTL,
		ActiveChannelIDsCacheMaxEntries:    c.ActiveChannelIDsCacheMaxEntries,
		ActiveChannelIDsCacheTTL:           c.ActiveChannelIDsCacheTTL,
		ActiveChannelIDsRedisTTL:           c.ActiveChannelIDsRedisTTL,
		ActiveChannelIDsBatchMax:           c.ActiveChannelIDsBatchMax,
		ActiveChannelIDsBatchWait:          c.ActiveChannelIDsBatchWait,
		ActiveChannelIDsBatchQueue:         c.ActiveChannelIDsBatchQueue,
		ActiveChannelIDsBatchTimeout:       c.ActiveChannelIDsBatchTimeout,
		BootstrapReadyBatchMax:             c.BootstrapReadyBatchMax,
		BootstrapReadyBatchWait:            c.BootstrapReadyBatchWait,
		BootstrapReadyBatchQueue:           c.BootstrapReadyBatchQueue,
		BootstrapReadyBatchTimeout:         c.BootstrapReadyBatchTimeout,
		PresenceLastSeenBatchMax:           c.PresenceLastSeenBatchMax,
		PresenceLastSeenBatchWait:          c.PresenceLastSeenBatchWait,
		PresenceLastSeenBatchQueue:         c.PresenceLastSeenBatchQueue,
		PresenceLastSeenBatchTimeout:       c.PresenceLastSeenBatchTimeout,
		PresenceLastSeenDrainTimeout:       c.PresenceLastSeenDrainTimeout,
		AuthUserCacheTTL:                   c.AuthUserCacheTTL,
		ChannelRowCacheMaxEntries:          c.ChannelRowCacheMaxEntries,
		ChannelTopMessageCacheMaxEntries:   c.ChannelTopMessageCacheMaxEntries,
		ChannelMemberCacheMaxEntries:       c.ChannelMemberCacheMaxEntries,
		ChannelDialogCacheMaxEntries:       c.ChannelDialogCacheMaxEntries,
		ChannelDifferenceCacheMaxEntries:   c.ChannelDifferenceCacheMaxEntries,
		ChannelDifferenceCacheMaxBytes:     c.ChannelDifferenceCacheMaxBytes,
		ChannelDifferenceCacheTTL:          c.ChannelDifferenceCacheTTL,
		ChannelBoostCacheMaxEntries:        c.ChannelBoostCacheMaxEntries,
		ChannelBoostCacheTTL:               c.ChannelBoostCacheTTL,
		OutboxLeaseTimeout:                 c.OutboxLeaseTimeout,
		OutboundPushTimeout:                c.OutboundPushTimeout,
		SendRateLimit:                      c.SendRateLimit,
		SendRateWindow:                     c.SendRateWindow,
		CatchupRateLimit:                   c.CatchupRateLimit,
		CatchupRateWindow:                  c.CatchupRateWindow,
		ChannelNudgeMaxTargets:             c.ChannelNudgeMaxTargets,
		UpdateEventRetention:               c.UpdateEventRetention,
		BotAPIUpdateRetention:              c.BotAPIUpdateRetention,
		OrphanAuthKeyRetention:             c.OrphanAuthKeyRetention,
		RetentionInterval:                  c.RetentionInterval,
		RetentionBatch:                     c.RetentionBatch,
		UploadInFlightMaxBytes:             c.UploadInFlightMaxBytes,
		UploadInFlightMaxParts:             c.UploadInFlightMaxParts,
		UploadInFlightMaxFiles:             c.UploadInFlightMaxFiles,
		CallRingTimeout:                    c.CallRingTimeout,
		CallTombstoneTTL:                   c.CallTombstoneTTL,
		CallMaxActivePerUser:               c.CallMaxActivePerUser,
		CallRegistryMaxEntries:             c.CallRegistryMaxEntries,
		CallSignalingMaxBytes:              c.CallSignalingMaxBytes,
		CallSignalingRate:                  c.CallSignalingRate,
		CallExpiryInterval:                 c.CallExpiryInterval,
		PremiumGrantMonths:                 c.PremiumGrantMonths,
		PremiumBotUsername:                 c.PremiumBotUsername,
		IdentityDir:                        c.IdentityDir,
		PremiumBotUserID:                   c.PremiumBotUserID,
		PremiumPlans:                       append([]domain.PremiumPlan(nil), c.PremiumPlans...),
		PasskeyRPID:                        c.PasskeyRPID,
		PasskeyAllowedOrigins:              append([]string(nil), c.PasskeyAllowedOrigins...),
		StarsStartingGrant:                 c.StarsStartingGrant,
		PremiumSweepInterval:               c.PremiumSweepInterval,
		PremiumSweepBatch:                  c.PremiumSweepBatch,
		StarGiftSweepInterval:              c.StarGiftSweepInterval,
		StarGiftSweepBatch:                 c.StarGiftSweepBatch,
		StarGiftTransferStars:              c.StarGiftTransferStars,
		StarGiftDropOriginalDetailsStars:   c.StarGiftDropOriginalDetailsStars,
		StarGiftOfferMinStars:              c.StarGiftOfferMinStars,
		StarGiftStarsProceedsPermille:      c.StarGiftStarsProceedsPermille,
		StarGiftTONProceedsPermille:        c.StarGiftTONProceedsPermille,
		StarGiftResaleMaxStars:             c.StarGiftResaleMaxStars,
		StarGiftResaleMaxTON:               c.StarGiftResaleMaxTON,
		StarGiftResaleMinStars:             c.StarGiftResaleMinStars,
		StarGiftTONStarsRate:               c.StarGiftTONStarsRate,
		StarGiftExportDelay:                c.StarGiftExportDelay,
		StarGiftTransferDelay:              c.StarGiftTransferDelay,
		StarGiftResellDelay:                c.StarGiftResellDelay,
		StarGiftCraftDelay:                 c.StarGiftCraftDelay,
		StarGiftCraftChancePermille:        c.StarGiftCraftChancePermille,
		StarGiftExportMode:                 c.StarGiftExportMode,
		StarGiftTONNetwork:                 c.StarGiftTONNetwork,
		StarGiftTONCollectionAddress:       c.StarGiftTONCollectionAddress,
		StarGiftTONCollectionCodeHash:      c.StarGiftTONCollectionCodeHash,
		StarGiftTONMintABI:                 c.StarGiftTONMintABI,
		StarGiftTONInitialItemIndex:        c.StarGiftTONInitialItemIndex,
		StarGiftTONProofDomain:             c.StarGiftTONProofDomain,
		StarGiftTONCapabilitySecretFile:    c.StarGiftTONCapabilitySecretFile,
		StarGiftTONExportTTL:               c.StarGiftTONExportTTL,
		StarGiftTONChallengeTTL:            c.StarGiftTONChallengeTTL,
		StarGiftTONFinalizerBatch:          c.StarGiftTONFinalizerBatch,
		StarGiftTONFinalizerPollInterval:   c.StarGiftTONFinalizerPollInterval,
		StarGiftTONFinalizerLeaseTimeout:   c.StarGiftTONFinalizerLeaseTimeout,
		StarGiftTONFinalizerRequestTimeout: c.StarGiftTONFinalizerRequestTimeout,
		StarGiftTONFinalizerRetryDelay:     c.StarGiftTONFinalizerRetryDelay,
		StarGiftTONClaimEnabled:            c.StarGiftTONClaimEnabled,
		StarGiftTONClaimBotTokenFile:       c.StarGiftTONClaimBotTokenFile,
		StarGiftTONClaimInitDataTTL:        c.StarGiftTONClaimInitDataTTL,
		DevStarsPaymentsEnabled:            c.DevStarsPaymentsEnabled,
		RatingEnabled:                      c.RatingEnabled,
		RatingPendingDelay:                 c.RatingPendingDelay,
		RatingRecomputeInterval:            c.RatingRecomputeInterval,
		RatingRecomputeBatch:               c.RatingRecomputeBatch,
		RatingStaleAfter:                   c.RatingStaleAfter,
		RatingWeightStarsReceivedPermille:  c.RatingWeightStarsReceivedPermille,
		RatingWeightStarsSpentPermille:     c.RatingWeightStarsSpentPermille,
		RatingWeightMessageSent:            c.RatingWeightMessageSent,
		RatingWeightAccountAgeDay:          c.RatingWeightAccountAgeDay,
		RatingWeightGiftReceived:           c.RatingWeightGiftReceived,
		RatingWeightModerationCase:         c.RatingWeightModerationCase,
		RatingWeightScamPenalty:            c.RatingWeightScamPenalty,
		RatingWeightFakePenalty:            c.RatingWeightFakePenalty,
		RatingActivityCap:                  c.RatingActivityCap,
		VerificationEnabled:                c.VerificationEnabled,
		VerificationAllowUserTargets:       c.VerificationAllowUserTargets,
		VerificationRejectCooldown:         c.VerificationRejectCooldown,
		VerificationApplyRateLimit:         c.VerificationApplyRateLimit,
		VerificationApplyRateWindow:        c.VerificationApplyRateWindow,
		VerificationBotRateLimit:           c.VerificationBotRateLimit,
		VerificationBotRateWindow:          c.VerificationBotRateWindow,
		VerificationNotifyInterval:         c.VerificationNotifyInterval,
		VerificationNotifyBatch:            c.VerificationNotifyBatch,
		BroadcastWorkerInterval:            c.BroadcastWorkerInterval,
		BroadcastWorkerLease:               c.BroadcastWorkerLease,
		BroadcastMaterializeBatch:          c.BroadcastMaterializeBatch,
		BroadcastDeliveryBatch:             c.BroadcastDeliveryBatch,
		VerificationMaxActivePerUser:       c.VerificationMaxActivePerUser,
		BotVerificationEnabled:             c.BotVerificationEnabled,
		BotVerificationMaxPerVerifier:      c.BotVerificationMaxPerVerifier,
		BotVerificationRequestRateLimit:    c.BotVerificationRequestRateLimit,
		BotVerificationRequestRateWindow:   c.BotVerificationRequestRateWindow,
		CollectibleUsernameURLTemplate:     c.CollectibleUsernameURLTemplate,
		GroupCallCheckTTL:                  c.GroupCallCheckTTL,
		GroupCallSweepInterval:             c.GroupCallSweepInterval,
		GroupCallMaxParticipants:           c.GroupCallMaxParticipants,
		TURNEnable:                         c.TURNEnable,
		TURNUDPPort:                        c.TURNUDPPort,
		TURNAdvertiseIP:                    c.TURNAdvertiseIP,
		TURNSecret:                         c.TURNSecret,
		CallTURNCredentialTTL:              c.CallTURNCredentialTTL,
		CallForceRelay:                     c.CallForceRelay,
		LiveStreamEnable:                   c.LiveStreamEnable,
		LiveStreamRtmpAddr:                 c.LiveStreamRtmpAddr,
		LiveStreamRtmpURL:                  c.LiveStreamRtmpURL,
		LiveStreamFFmpegPath:               c.LiveStreamFFmpegPath,
		LiveStreamWorkDir:                  c.LiveStreamWorkDir,
		LiveStreamSegmentKeep:              c.LiveStreamSegmentKeep,
		SFUAdvertiseIP:                     c.SFUAdvertiseIP,
		SFUOwnerTTL:                        c.SFUOwnerTTL,
		SFUOwnerHeartbeatInterval:          c.SFUOwnerHeartbeatInterval,
		SFUInstanceHealthTimeout:           c.SFUInstanceHealthTimeout,
		SFUControlGRPCRequestTimeout:       c.SFUControlGRPCRequestTimeout,
		SFUControlGRPCTLSCAFile:            c.SFUControlGRPCTLSCAFile,
		SFUControlGRPCTLSServerName:        c.SFUControlGRPCTLSServerName,
		SFUControlGRPCTLSClientCertFile:    c.SFUControlGRPCTLSClientCertFile,
		SFUControlGRPCTLSClientKeyFile:     c.SFUControlGRPCTLSClientKeyFile,
		SFUControlToken:                    c.SFUControlToken,
	}
}

// LoadCore reads the Core role YAML config and returns the runtime snapshot used by the Core process.
func LoadCore() (CoreConfig, error) {
	var starGiftTONAllowUserIDs, starGiftCrossCraftUserIDs, operatorBotChatIDs []int64
	var operatorBotUserID int64
	var miniAppBotFatherToken, miniAppStickersToken string
	cfg, err := loadRoleYAML(roleCore, func(b *envBuilder, y CoreConfigYAML) error {
		if err := requireYAMLVersion(y.Version); err != nil {
			return err
		}
		if err := applyCommonYAML(b, y.CommonYAML); err != nil {
			return err
		}
		applyBrandingYAML(b, y.Branding)
		applyCoreExecServerYAML(b, y.CoreExec)
		if err := applyFileClientYAML(b, y.FileData); err != nil {
			return err
		}
		b.setString("TELESRV_GROUPCALL_CONTROL_ADDR", y.GroupCall.ControlAddr)
		b.setString("TELESRV_GROUPCALL_CONTROL_URL", y.GroupCall.ControlURL)
		b.setString("TELESRV_GROUPCALL_CONTROL_TOKEN", y.GroupCall.ControlToken)
		if err := applySFUControlClientYAML(b, y.SFU.Control); err != nil {
			return err
		}
		if err := applySFUOwnerYAML(b, y.SFU.Owner); err != nil {
			return err
		}
		b.setOptionalString("TELESRV_BOT_API_ADDR", y.HTTP.BotAPIAddr)
		b.setOptionalString("TELESRV_ADMIN_API_ADDR", y.HTTP.AdminAPIAddr)
		b.setString("TELESRV_ADMIN_API_TOKEN", y.HTTP.AdminAPIToken)
		b.setString("TELESRV_ADMIN_SCOPED_TOKENS", formatAdminScopedTokensYAML(y.HTTP.AdminScopedTokens))
		b.setOptionalString("TELESRV_PUBLIC_LINK_WEB_ADDR", y.HTTP.PublicLinkWebAddr)
		b.setOptionalString("TELESRV_GIFT_PREVIEW_CACHE_DIR", y.HTTP.GiftPreviewCacheDir)
		b.setOptionalString("TELESRV_GIFT_PREVIEW_RENDERER_URL", y.HTTP.GiftPreviewRendererURL)
		if err := applyMediaYAML(b, y.Media); err != nil {
			return err
		}
		applySeedsYAML(b, y.Seeds)
		applyIdentityYAML(b, y.Identity)
		if err := applyAuthYAML(b, y.Auth); err != nil {
			return err
		}
		if err := applyCoreTURNYAML(b, y.TURN); err != nil {
			return err
		}
		if err := applyLiveStreamYAML(b, y.LiveStream); err != nil {
			return err
		}
		if err := applyOutboxYAML(b, y.Outbox); err != nil {
			return err
		}
		if err := applyCoreCacheYAML(b, y.Cache); err != nil {
			return err
		}
		if err := applyTelegramLoginYAML(b, y.TelegramLogin); err != nil {
			return err
		}
		if err := applyAIYAML(b, y.AI); err != nil {
			return err
		}
		if err := applyTranslationYAML(b, y.Translation); err != nil {
			return err
		}
		b.setString("TELESRV_SCAM_WARNING", y.Moderation.ScamProfileWarning)
		b.setString("TELESRV_FAKE_WARNING", y.Moderation.FakeProfileWarning)
		b.setString("TELESRV_DONATION_WALLET_KEY_FILE", y.Donations.WalletKeyFile)
		if err := b.setDuration("TELESRV_DONATION_POLL_INTERVAL", y.Donations.PollInterval); err != nil {
			return err
		}
		b.setString("TELESRV_ASR_URL", y.Transcription.ASRURL)
		if err := b.setDuration("TELESRV_ASR_TIMEOUT", y.Transcription.ASRTimeout); err != nil {
			return err
		}
		if err := applyStarGiftsYAML(b, y.StarGifts); err != nil {
			return err
		}
		if err := applyGeoIPYAML(b, y.GeoIP); err != nil {
			return err
		}
		if err := applySensitiveRateYAML(b, y.SensitiveRate); err != nil {
			return err
		}
		var err error
		starGiftTONAllowUserIDs, err = validateTONAllowUserIDs(y.StarGifts.Export.TON.AllowUserIDs)
		if err != nil {
			return err
		}
		starGiftCrossCraftUserIDs, err = validateUserIDList("star_gifts.cross_craft_user_ids", y.StarGifts.CrossCraftUserIDs)
		if err != nil {
			return err
		}
		operatorBotChatIDs, err = validateUserIDList("operator_bot.chat_ids", y.OperatorBot.ChatIDs)
		if err != nil {
			return err
		}
		if y.OperatorBot.BotUserID < 0 {
			return fmt.Errorf("operator_bot.bot_user_id must not be negative")
		}
		operatorBotUserID = y.OperatorBot.BotUserID
		miniAppBotFatherToken = strings.TrimSpace(y.MiniApps.BotFatherToken)
		miniAppStickersToken = strings.TrimSpace(y.MiniApps.StickersToken)
		for field, token := range map[string]string{"mini_apps.botfather_token": miniAppBotFatherToken, "mini_apps.stickers_token": miniAppStickersToken} {
			if token != "" {
				if id, secret, ok := domain.ParseBotToken(token); !ok || id <= 0 || secret == "" {
					return fmt.Errorf("%s must be a bot token", field)
				}
			}
		}
		return nil
	})
	if err != nil {
		return CoreConfig{}, err
	}
	result := coreConfigFromConfig(cfg)
	result.StarGiftTONAllowUserIDs = starGiftTONAllowUserIDs
	result.StarGiftCrossCraftUserIDs = starGiftCrossCraftUserIDs
	result.MiniAppBotFatherToken = miniAppBotFatherToken
	result.MiniAppStickersToken = miniAppStickersToken
	result.OperatorBotUserID = operatorBotUserID
	result.OperatorBotChatIDs = operatorBotChatIDs
	return result, nil
}

func validateTONAllowUserIDs(ids []int64) ([]int64, error) {
	return validateUserIDList("star_gifts.export.ton.allow_user_ids", ids)
}

func validateUserIDList(field string, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	result := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%s must contain positive user IDs", field)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("%s contains duplicate user ID %d", field, id)
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func applySensitiveRateYAML(b *envBuilder, y SensitiveRateYAML) error {
	b.setInt("TELESRV_SRP_RATE_LIMIT", y.SRPLimit)
	if err := b.setDuration("TELESRV_SRP_RATE_WINDOW", y.SRPWindow); err != nil {
		return err
	}
	b.setInt("TELESRV_WITHDRAWAL_RATE_LIMIT", y.WithdrawalLimit)
	if err := b.setDuration("TELESRV_WITHDRAWAL_RATE_WINDOW", y.WithdrawalWindow); err != nil {
		return err
	}
	b.setInt("TELESRV_PAYMENT_RATE_LIMIT", y.PaymentLimit)
	return b.setDuration("TELESRV_PAYMENT_RATE_WINDOW", y.PaymentWindow)
}

func applyGeoIPYAML(b *envBuilder, y GeoIPYAML) error {
	b.setList("TELESRV_GEOIP_ENDPOINTS", y.Endpoints)
	if err := b.setDuration("TELESRV_GEOIP_TIMEOUT", y.Timeout); err != nil {
		return err
	}
	b.setInt("TELESRV_GEOIP_CONCURRENCY", y.Concurrency)
	if err := b.setDuration("TELESRV_GEOIP_CACHE_TTL", y.CacheTTL); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_GEOIP_NEGATIVE_TTL", y.NegativeTTL); err != nil {
		return err
	}
	b.setInt("TELESRV_GEOIP_CACHE_SIZE", y.CacheSize)
	b.setInt("TELESRV_GEOIP_RATE_LIMIT_THRESHOLD", y.RateLimitThreshold)
	if err := b.setDuration("TELESRV_GEOIP_RATE_LIMIT_COOLDOWN", y.RateLimitCooldown); err != nil {
		return err
	}
	b.setInt("TELESRV_GEOIP_DOWN_THRESHOLD", y.DownThreshold)
	return b.setDuration("TELESRV_GEOIP_DOWN_COOLDOWN", y.DownCooldown)
}

func applyStarGiftsYAML(b *envBuilder, y StarGiftsYAML) error {
	b.setInt64("TELESRV_STARGIFT_TON_STARTING_GRANT", y.TONStartingGrant)
	if err := b.setDuration("TELESRV_STARGIFT_SWEEP_INTERVAL", y.SweepInterval); err != nil {
		return err
	}
	b.setInt("TELESRV_STARGIFT_SWEEP_BATCH", y.SweepBatch)
	b.setInt64("TELESRV_STARGIFT_TRANSFER_STARS", y.TransferStars)
	b.setInt64("TELESRV_STARGIFT_DROP_DETAILS_STARS", y.DropOriginalDetailsStars)
	b.setInt("TELESRV_STARGIFT_OFFER_MIN_STARS", y.OfferMinStars)
	b.setInt("TELESRV_STARGIFT_STARS_PROCEEDS_PERMILLE", y.StarsProceedsPermille)
	b.setInt("TELESRV_STARGIFT_TON_PROCEEDS_PERMILLE", y.TONProceedsPermille)
	if err := b.setDuration("TELESRV_STARGIFT_EXPORT_DELAY", y.ExportDelay); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_STARGIFT_TRANSFER_DELAY", y.TransferDelay); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_STARGIFT_RESELL_DELAY", y.ResellDelay); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_STARGIFT_CRAFT_DELAY", y.CraftDelay); err != nil {
		return err
	}
	b.setInt("TELESRV_STARGIFT_CRAFT_CHANCE_PERMILLE", y.CraftChancePermille)
	b.setString("TELESRV_STARGIFT_EXPORT_MODE", y.Export.Mode)
	b.setString("TELESRV_STARGIFT_TON_NETWORK", y.Export.TON.Network)
	b.setString("TELESRV_STARGIFT_TON_COLLECTION_ADDRESS", y.Export.TON.CollectionAddress)
	b.setString("TELESRV_STARGIFT_TON_COLLECTION_CODE_HASH", y.Export.TON.CollectionCodeHash)
	b.setString("TELESRV_STARGIFT_TON_MINT_ABI", y.Export.TON.MintABI)
	b.setString("TELESRV_STARGIFT_TON_INITIAL_ITEM_INDEX", y.Export.TON.InitialItemIndex)
	b.setString("TELESRV_STARGIFT_TON_PROOF_DOMAIN", y.Export.TON.ProofDomain)
	b.setString("TELESRV_STARGIFT_TON_CAPABILITY_SECRET_FILE", y.Export.TON.CapabilitySecretFile)
	if err := b.setDuration("TELESRV_STARGIFT_TON_EXPORT_TTL", y.Export.TON.TTL); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_STARGIFT_TON_CHALLENGE_TTL", y.Export.TON.ChallengeTTL); err != nil {
		return err
	}
	b.setInt("TELESRV_STARGIFT_TON_FINALIZER_BATCH", y.Export.TON.Finalizer.Batch)
	if err := b.setDuration("TELESRV_STARGIFT_TON_FINALIZER_POLL_INTERVAL", y.Export.TON.Finalizer.PollInterval); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_STARGIFT_TON_FINALIZER_LEASE_TIMEOUT", y.Export.TON.Finalizer.LeaseTimeout); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_STARGIFT_TON_FINALIZER_REQUEST_TIMEOUT", y.Export.TON.Finalizer.RequestTimeout); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_STARGIFT_TON_FINALIZER_RETRY_DELAY", y.Export.TON.Finalizer.RetryDelay); err != nil {
		return err
	}
	b.setBool("TELESRV_STARGIFT_TON_CLAIM_ENABLED", y.Export.TON.Claim.Enabled)
	b.setString("TELESRV_STARGIFT_TON_CLAIM_BOT_TOKEN_FILE", y.Export.TON.Claim.BotTokenFile)
	return b.setDuration("TELESRV_STARGIFT_TON_CLAIM_INIT_DATA_TTL", y.Export.TON.Claim.InitDataTTL)
}

func applySFUOwnerYAML(b *envBuilder, y SFUOwnerYAML) error {
	if err := b.setDuration("TELESRV_SFU_OWNER_TTL", y.TTL); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_SFU_OWNER_HEARTBEAT_INTERVAL", y.HeartbeatInterval); err != nil {
		return err
	}
	return b.setDuration("TELESRV_SFU_INSTANCE_HEALTH_TIMEOUT", y.HealthTimeout)
}

func formatAdminScopedTokensYAML(tokens []AdminScopedTokenYAML) string {
	if len(tokens) == 0 {
		return ""
	}
	entries := make([]string, 0, len(tokens))
	for _, token := range tokens {
		name := strings.TrimSpace(token.Name)
		value := strings.TrimSpace(token.Token)
		permissions := trimStringList(token.Permissions)
		if name == "" && value == "" && len(permissions) == 0 {
			continue
		}
		entries = append(entries, name+":"+value+":"+strings.Join(permissions, ","))
	}
	return strings.Join(entries, ";")
}

func applyMediaYAML(b *envBuilder, y MediaYAML) error {
	b.setString("TELESRV_MAPBOX_TOKEN", y.MapboxToken)
	b.setString("TELESRV_MAPTILE_CACHE_DIR", y.MapTileCacheDir)
	b.setBool("TELESRV_EXTERNAL_MEDIA_ENABLE", y.ExternalMediaEnable)
	b.setInt64("TELESRV_EXTERNAL_MEDIA_MAX_BYTES", y.ExternalMediaMaxBytes)
	b.setInt("TELESRV_EXTERNAL_MEDIA_RATE_PER_MIN", y.ExternalMediaRatePerMin)
	b.setBool("TELESRV_WEBPAGE_PREVIEW_ENABLE", y.WebPagePreviewEnable)
	b.setInt64("TELESRV_WEBPAGE_PREVIEW_MAX_BYTES", y.WebPagePreviewMaxBytes)
	b.setInt("TELESRV_WEBPAGE_PREVIEW_RATE_PER_MIN", y.WebPagePreviewRatePerMin)
	return nil
}

func applySeedsYAML(b *envBuilder, y SeedsYAML) {
	b.setString("TELESRV_LANGPACK_SEED_DIR", y.LangPackDir)
	b.setString("TELESRV_OFFICIAL_GIFTS_DIR", y.OfficialGiftsDir)
	b.setString("TELESRV_STICKER_SEED_DIR", y.StickerDir)
	b.setString("TELESRV_GIF_SEED_DIR", y.GifDir)
	b.setString("TELESRV_PREMIUM_PROMO_SEED_DIR", y.PremiumPromoDir)
	b.setInt("TELESRV_STICKER_SEED_MAX_SETS", y.StickerSeedMaxSet)
}

func applyAuthYAML(b *envBuilder, y AuthYAML) error {
	b.setString("TELESRV_DEV_AUTH_CODE", y.DevCode)
	b.setBool("TELESRV_CHECK_NUMBER_FORMAT", y.CheckNumberFormat)
	b.setString("TELESRV_LOGIN_CODE_MESSAGE_TEMPLATE", y.LoginCodeTemplate)
	if err := b.setDuration("TELESRV_AUTH_CODE_TTL", y.CodeTTL); err != nil {
		return err
	}
	b.setInt("TELESRV_AUTH_CODE_MAX_ATTEMPTS", y.CodeMaxAttempts)
	b.setInt("TELESRV_AUTH_CODE_PHONE_RATE_LIMIT", y.PhoneRateLimit)
	b.setInt("TELESRV_AUTH_CODE_AUTH_KEY_RATE_LIMIT", y.AuthKeyRateLimit)
	if err := b.setDuration("TELESRV_AUTH_CODE_RATE_WINDOW", y.RateWindow); err != nil {
		return err
	}
	b.setString("TELESRV_PHONE_CODE_DELIVERY_PROVIDER", y.PhoneDelivery)
	b.setInt("TELESRV_PHONE_CODE_LENGTH", y.PhoneCodeLength)
	b.setBool("TELESRV_LOGIN_EMAIL_ENABLE", y.LoginEmailEnable)
	b.setBool("TELESRV_LOGIN_EMAIL_REQUIRE_SETUP", y.LoginEmailRequireSet)
	b.setInt("TELESRV_LOGIN_EMAIL_CODE_LENGTH", y.LoginEmailCodeLength)
	b.setString("TELESRV_EMAIL_CODE_DELIVERY_PROVIDER", y.EmailDelivery)
	b.setString("TELESRV_OTP_WEBHOOK_URL", y.OTPWebhookURL)
	b.setString("TELESRV_OTP_WEBHOOK_SECRET", y.OTPWebhookSecret)
	if err := b.setDuration("TELESRV_OTP_WEBHOOK_TIMEOUT", y.OTPWebhookTimeout); err != nil {
		return err
	}
	b.setString("TELESRV_SMTP_HOST", y.SMTP.Host)
	b.setInt("TELESRV_SMTP_PORT", y.SMTP.Port)
	b.setString("TELESRV_SMTP_USERNAME", y.SMTP.Username)
	b.setString("TELESRV_SMTP_PASSWORD", y.SMTP.Password)
	b.setString("TELESRV_SMTP_FROM", y.SMTP.From)
	b.setString("TELESRV_SMTP_FROM_NAME", y.SMTP.FromName)
	b.setString("TELESRV_SMTP_TLS", y.SMTP.TLSMode)
	return b.setDuration("TELESRV_SMTP_TIMEOUT", y.SMTP.Timeout)
}

func applyOutboxYAML(b *envBuilder, y OutboxYAML) error {
	b.setInt("TELESRV_OUTBOX_WORKERS", y.Workers)
	b.setInt("TELESRV_OUTBOX_BATCH", y.Batch)
	if err := b.setDuration("TELESRV_OUTBOX_LEASE_TIMEOUT", y.LeaseTimeout); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_OUTBOUND_PUSH_TIMEOUT", y.OutboundPushTimeout); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_UPDATE_EVENT_RETENTION", y.UpdateEventRetention); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_BOT_API_UPDATE_RETENTION", y.BotAPIUpdateRetention); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_ORPHAN_AUTH_KEY_RETENTION", y.OrphanAuthKeyRetention); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_RETENTION_INTERVAL", y.RetentionInterval); err != nil {
		return err
	}
	b.setInt("TELESRV_RETENTION_BATCH", y.RetentionBatch)
	return nil
}

func applyCoreCacheYAML(b *envBuilder, y CoreCacheYAML) error {
	b.setInt("TELESRV_TEMP_KEY_CACHE_MAX_ENTRIES", y.TempKey.MaxEntries)
	if err := b.setDuration("TELESRV_TEMP_KEY_CACHE_TTL", y.TempKey.TTL); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_AUTH_USER_CACHE_TTL", y.AuthUser.TTL); err != nil {
		return err
	}
	b.setInt("TELESRV_CHANNEL_ROW_CACHE_MAX_ENTRIES", y.ChannelRow.MaxEntries)
	b.setInt("TELESRV_CHANNEL_MEMBER_CACHE_MAX_ENTRIES", y.ChannelMember.MaxEntries)
	b.setInt("TELESRV_CHANNEL_DIALOG_CACHE_MAX_ENTRIES", y.ChannelDialog.MaxEntries)
	b.setInt("TELESRV_CHANNEL_BOOST_CACHE_MAX_ENTRIES", y.ChannelBoost.MaxEntries)
	return b.setDuration("TELESRV_CHANNEL_BOOST_CACHE_TTL", y.ChannelBoost.TTL)
}

func applyTelegramLoginYAML(b *envBuilder, y TelegramLoginYAML) error {
	b.setBool("TELESRV_TELEGRAM_LOGIN_ENABLE", y.Enabled)
	b.setString("TELESRV_TELEGRAM_LOGIN_ISSUER", y.Issuer)
	b.setBool("TELESRV_TELEGRAM_LOGIN_ALLOW_HTTP", y.AllowHTTP)
	b.setString("TELESRV_TELEGRAM_LOGIN_SIGNING_KEYS_FILE", y.SigningKeysFile)
	b.setString("TELESRV_TELEGRAM_LOGIN_CODE_KEYS_FILE", y.CodeKeysFile)
	b.setString("TELESRV_TELEGRAM_LOGIN_SECRET_PEPPER_FILE", y.SecretPepperFile)
	if err := b.setDuration("TELESRV_TELEGRAM_LOGIN_REQUEST_TTL", y.RequestTTL); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_TELEGRAM_LOGIN_CODE_TTL", y.CodeTTL); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_TELEGRAM_LOGIN_ID_TOKEN_TTL", y.IDTokenTTL); err != nil {
		return err
	}
	b.setList("TELESRV_TELEGRAM_LOGIN_TRUSTED_PROXY_CIDRS", y.TrustedProxyCIDRs)
	if err := b.setDuration("TELESRV_TELEGRAM_LOGIN_RETENTION", y.Retention); err != nil {
		return err
	}
	if err := b.setDuration("TELESRV_TELEGRAM_LOGIN_SWEEP_INTERVAL", y.SweepInterval); err != nil {
		return err
	}
	b.setInt("TELESRV_TELEGRAM_LOGIN_SWEEP_BATCH", y.SweepBatch)
	return nil
}

func applyAIYAML(b *envBuilder, y AIYAML) error {
	b.setBool("TELESRV_AI_ENABLED", y.Enabled)
	if err := b.setDuration("TELESRV_AI_TIMEOUT", y.Timeout); err != nil {
		return err
	}
	b.setInt("TELESRV_AI_RATE_LIMIT", y.RateLimit)
	if err := b.setDuration("TELESRV_AI_RATE_WINDOW", y.RateWindow); err != nil {
		return err
	}
	b.setBool("TELESRV_AI_LOG_CONTENT", y.PrivacyLogContent)
	b.setString("TELESRV_BUSINESS_AI_PROVIDER", y.BusinessProvider)
	return nil
}

func applyTranslationYAML(b *envBuilder, y TranslationYAML) error {
	b.setBool("TELESRV_TRANSLATION_ENABLED", y.Enabled)
	b.setList("TELESRV_TRANSLATION_PROVIDERS", y.Providers)
	if err := b.setDuration("TELESRV_TRANSLATION_TIMEOUT", y.Timeout); err != nil {
		return err
	}
	b.setInt("TELESRV_TRANSLATION_RATE_LIMIT", y.RateLimit)
	return b.setDuration("TELESRV_TRANSLATION_RATE_WINDOW", y.RateWindow)
}

func applyCoreTURNYAML(b *envBuilder, y CoreTURNYAML) error {
	b.setBool("TELESRV_TURN_ENABLE", y.Enabled)
	b.setInt("TELESRV_TURN_UDP_PORT", y.UDPPort)
	b.setString("TELESRV_TURN_ADVERTISE_IP", y.AdvertiseIP)
	b.setString("TELESRV_TURN_SECRET", y.Secret)
	if err := b.setDuration("TELESRV_CALL_TURN_CREDENTIAL_TTL", y.CredentialTTL); err != nil {
		return err
	}
	b.setBool("TELESRV_CALL_FORCE_RELAY", y.ForceRelay)
	return nil
}

func applyLiveStreamYAML(b *envBuilder, y LiveStreamYAML) error {
	b.setBool("TELESRV_LIVESTREAM_ENABLE", y.Enabled)
	b.setString("TELESRV_LIVESTREAM_RTMP_ADDR", y.RtmpAddr)
	b.setString("TELESRV_LIVESTREAM_RTMP_URL", y.RtmpURL)
	b.setString("TELESRV_LIVESTREAM_FFMPEG_PATH", y.FFmpegPath)
	b.setString("TELESRV_LIVESTREAM_WORK_DIR", y.WorkDir)
	b.setInt("TELESRV_LIVESTREAM_SEGMENT_KEEP", y.SegmentKeep)
	return nil
}
