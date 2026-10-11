package giftclaim

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	tonwallet "github.com/xssnick/tonutils-go/ton/wallet"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

var (
	ErrUnauthorized       = errors.New("Mini App authorization failed")
	ErrInvalid            = errors.New("gift claim request is invalid")
	ErrNotOwner           = errors.New("connected wallet is not the current NFT owner")
	ErrExpired            = errors.New("gift claim challenge expired")
	ErrGiftUnavailable    = errors.New("\u043f\u043e\u0434\u0430\u0440\u043e\u043a \u043d\u0435 \u043d\u0430\u0439\u0434\u0435\u043d: \u0432\u0432\u0435\u0434\u0438\u0442\u0435 slug, \u0430\u0434\u0440\u0435\u0441 NFT \u0438\u043b\u0438 \u0437\u0430\u043f\u0440\u043e\u0441 \u043d\u0430 \u0432\u044b\u0432\u043e\u0434")
	ErrWithdrawalRequired = errors.New("\u043f\u043e\u0434\u0430\u0440\u043e\u043a \u0435\u0449\u0451 \u043d\u0435 \u0432\u044b\u0432\u0435\u0434\u0435\u043d \u0432 TON: \u0437\u0430\u043f\u0440\u043e\u0441\u0438\u0442\u0435 \u0432\u044b\u0432\u043e\u0434 \u0438\u0437 \u0441\u043e\u043e\u0431\u0449\u0435\u043d\u0438\u044f \u043e \u043f\u043e\u0434\u0430\u0440\u043a\u0435 \u0438 \u043f\u043e\u0432\u0442\u043e\u0440\u0438\u0442\u0435")
	ErrMintUnavailable    = errors.New("\u0432\u044b\u0432\u043e\u0434 \u043f\u043e\u0434\u0430\u0440\u043a\u0430 \u0432 TON \u0432\u0440\u0435\u043c\u0435\u043d\u043d\u043e \u043d\u0435\u0434\u043e\u0441\u0442\u0443\u043f\u0435\u043d")
	ErrMintNotFinalized   = errors.New("gift NFT is not finalized on TON mainnet")
	ErrAdminUnavailable   = errors.New("\u044d\u043a\u0441\u043f\u043e\u0440\u0442 \u043f\u043e\u0434\u0430\u0440\u043a\u0430 \u0432 \u043a\u043e\u0448\u0435\u043b\u0451\u043a \u043d\u0435 \u043d\u0430\u0441\u0442\u0440\u043e\u0435\u043d")
	ErrAdminRequired      = errors.New("\u0440\u0435\u0436\u0438\u043c \u043e\u043f\u0435\u0440\u0430\u0442\u043e\u0440\u0430 \u0434\u043e\u0441\u0442\u0443\u043f\u0435\u043d \u0442\u043e\u043b\u044c\u043a\u043e \u0441\u043e\u0442\u0440\u0443\u0434\u043d\u0438\u043a\u0430\u043c \u043f\u043e\u0434\u0434\u0435\u0440\u0436\u043a\u0438")
	ErrAlreadyExported    = errors.New("\u043f\u043e\u0434\u0430\u0440\u043e\u043a \u0443\u0436\u0435 \u043d\u0430\u0445\u043e\u0434\u0438\u0442\u0441\u044f \u043d\u0430 \u043a\u043e\u0448\u0435\u043b\u044c\u043a\u0435")
)

var (
	ErrGiftOnChain       = errors.New("подарок уже выпущен в TON: отправка на кошелёк недоступна")
	ErrWalletUnavailable = errors.New("операция с кошелёком временно недоступна")
	ErrProfileOwnerOnly  = errors.New("подарок закреплён за другим профилем")
	ErrExportUnavailable = errors.New("этот подарок пока нельзя вывести в TON")
	ErrPasswordInvalid   = errors.New("неверный пароль для подтверждения вывода")
)

type Store interface {
	ResolveOnChainGift(context.Context, string) (domain.UniqueStarGift, bool, error)
	ResolveGiftByRef(context.Context, string) (domain.UniqueStarGift, bool, error)
	WithdrawalForGift(context.Context, int64, int64) (domain.StarGiftWithdrawal, bool, error)
	WithdrawalByRequestID(context.Context, string) (domain.StarGiftWithdrawal, bool, error)
	ListOnChainGifts(context.Context, int64, int) ([]domain.UniqueStarGift, error)
	ReconcileOnChainOwner(context.Context, int64, string, string, string) (bool, error)
	ProfileUsername(context.Context, int64) (string, error)
	CreateChallenge(context.Context, int64, int64, time.Time, time.Duration) (domain.StarGiftClaimChallenge, error)
	ResolveChallenge(context.Context, string, int64, int) (domain.StarGiftClaimChallenge, bool, error)
	CommitClaim(context.Context, domain.StarGiftOnChainClaim) (domain.StarGiftOnChainClaimResult, error)
}

type Verifier interface {
	VerifyWalletProof(context.Context, string, string, tonwallet.TonConnectProof, []byte, time.Duration) error
	VerifyMint(context.Context, string, *big.Int, string) (string, error)
	CurrentNFTOwner(context.Context, string, *big.Int, string) (string, bool, error)
	Close()
}

type MintIntent struct {
	Network           string `json:"network"`
	CollectionAddress string `json:"collection_address"`
	Amount            string `json:"amount"`
	Payload           string `json:"payload"`
	ValidUntil        int64  `json:"valid_until"`
	ItemIndex         string `json:"item_index"`
	WalletAddress     string `json:"wallet_address"`
}

type MintConfirmation struct {
	Status       string `json:"status"`
	OwnerAddress string `json:"owner_address"`
	GiftAddress  string `json:"gift_address"`
	Collection   string `json:"collection_address"`
}

type Minter interface {
	MintIntent(context.Context, string, string, time.Time) (MintIntent, error)
	ConfirmMint(context.Context, string, string, time.Time) (MintConfirmation, error)
}

// Withdrawer is stargifts.Service, the same call payments.getStarGiftWithdrawalUrl makes over MTProto.
type Withdrawer interface {
	Withdraw(context.Context, domain.StarGiftWithdrawalRequest) (domain.StarGiftWithdrawal, error)
}

// AccountPassword is the 2FA half of the web withdrawal flow: GetPassword
// hands out one SRP challenge and CheckPassword verifies the client's M1.
// The plaintext password stays in the browser, never in gramsrv.
type AccountPassword interface {
	GetPassword(context.Context, int64) (domain.PasswordSettings, error)
	CheckPassword(context.Context, int64, domain.PasswordCheck) error
}

// Limiter matches store.RateLimiter so this package keeps no store dependency.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
}

// adminStore is the optional database side of the operator panel. Only the
// postgres store implements it, so the admin endpoints stay unavailable in
// hermetic tests and when the store cannot answer.
type adminStore interface {
	IsSupportUser(context.Context, int64) (bool, error)
	AdminExportGift(context.Context, domain.StarGiftAdminExport) (domain.UniqueStarGift, error)
}

type walletStore interface {
	WalletBindGift(context.Context, domain.StarGiftWalletBind) (domain.UniqueStarGift, error)
	WalletReleaseGift(context.Context, int64, int64) (domain.UniqueStarGift, error)
	SavedGiftOwner(context.Context, int64) (string, int64, bool, error)
}

type Config struct {
	PublicBaseURL         string
	BotToken              string
	BotUserID             int64
	BasePath              string
	Collection            string
	AppName               string
	ChallengeTTL          time.Duration
	ProofTTL              time.Duration
	InitDataTTL           time.Duration
	OwnershipSyncInterval time.Duration
	OwnershipSyncBatch    int
	Minter                Minter
	Withdrawer            Withdrawer
	Password              AccountPassword
	// Limiter bounds claim requests per client IP; nil disables limiting.
	Limiter Limiter
	// APIRateLimit is the per-IP budget for proof/mint/admin requests; <=0 disables.
	APIRateLimit int
	// APIRateWindow is the sliding window for APIRateLimit.
	APIRateWindow time.Duration
	// WithdrawRateLimit is the per-IP budget for withdrawal/SRP wallet requests; <=0 disables.
	WithdrawRateLimit int
	// WithdrawRateWindow is the sliding window for WithdrawRateLimit.
	WithdrawRateWindow time.Duration
}

type Service struct {
	publicBaseURL         string
	proofDomain           string
	botToken              string
	basePath              string
	botHandle             string
	collection            string
	appName               string
	challengeTTL          time.Duration
	proofTTL              time.Duration
	initDataTTL           time.Duration
	ownershipSyncInterval time.Duration
	ownershipSyncBatch    int
	store                 Store
	verifier              Verifier
	minter                Minter
	withdrawer            Withdrawer
	password              AccountPassword
	limiter               Limiter
	apiRateLimit          int
	apiRateWindow         time.Duration
	withdrawRateLimit     int
	withdrawRateWindow    time.Duration
	logger                *zap.Logger
}

type ChallengeResponse struct {
	Mode          string `json:"mode"`
	Payload       string `json:"payload"`
	ExpiresAt     int    `json:"expires_at"`
	RequestID     string `json:"request_id"`
	GiftTitle     string `json:"gift_title"`
	GiftSlug      string `json:"gift_slug"`
	NFTAddress    string `json:"nft_address"`
	WalletAddress string `json:"wallet_address"`
	OwnerProfile  string `json:"owner_profile"`
}

type ClaimInput struct {
	Payload string `json:"payload"`
	Account struct {
		Address         string `json:"address"`
		Chain           string `json:"chain"`
		PublicKey       string `json:"publicKey"`
		WalletStateInit string `json:"walletStateInit"`
	} `json:"account"`
	Proof struct {
		Timestamp int64 `json:"timestamp"`
		Domain    struct {
			LengthBytes uint32 `json:"lengthBytes"`
			Value       string `json:"value"`
		} `json:"domain"`
		Signature string `json:"signature"`
		Payload   string `json:"payload"`
	} `json:"proof"`
}

type ClaimResponse struct {
	OwnerProfile  string `json:"owner_profile"`
	OwnerUserID   int64  `json:"owner_user_id"`
	WalletAddress string `json:"wallet_address"`
	NFTAddress    string `json:"nft_address"`
	GiftSlug      string `json:"gift_slug"`
}

// PasswordChallenge is the SRP material the browser needs to answer the 2FA
// prompt. The password itself never leaves the client. srp_id travels as a
// decimal string: a random int64 does not survive a JSON number.
type PasswordChallenge struct {
	HasPassword bool   `json:"has_password"`
	SRPID       int64  `json:"srp_id,string,omitempty"`
	SRPB        string `json:"srp_b,omitempty"`
	G           int    `json:"g,omitempty"`
	P           string `json:"p,omitempty"`
	Salt1       string `json:"salt1,omitempty"`
	Salt2       string `json:"salt2,omitempty"`
}

// WithdrawalPasswordInput is one SRP answer. A nil value means the caller sent
// inputCheckPasswordEmpty, valid only when the account has no 2FA password.
type WithdrawalPasswordInput struct {
	SRPID int64  `json:"srp_id,string"`
	A     string `json:"a"`
	M1    string `json:"m1"`
}

func New(cfg Config, store Store, verifier Verifier, logger *zap.Logger) (*Service, error) {
	base, err := url.Parse(strings.TrimSpace(cfg.PublicBaseURL))
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil {
		return nil, fmt.Errorf("gift claim public URL must be HTTPS")
	}
	if cfg.BotUserID == 0 {
		cfg.BotUserID = domain.GiftClaimBotUserID
	}
	if cfg.BotUserID != domain.GiftClaimBotUserID && cfg.BotUserID != domain.GiftClaimTestBotUserID {
		return nil, fmt.Errorf("gift claim bot user id is not a claim bot")
	}
	botID, _, ok := domain.ParseBotToken(strings.TrimSpace(cfg.BotToken))
	if !ok || botID != cfg.BotUserID {
		return nil, fmt.Errorf("gift claim bot token does not match the claim bot")
	}
	basePathInit := strings.TrimSpace(cfg.BasePath)
	if basePathInit == "" {
		basePathInit = "/claim"
	}
	if !strings.HasPrefix(basePathInit, "/") {
		return nil, fmt.Errorf("gift claim base path must be absolute")
	}
	botHandle := "@claim"
	if cfg.BotUserID == domain.GiftClaimTestBotUserID {
		botHandle = "@claimtest"
	}
	if store == nil || verifier == nil || strings.TrimSpace(cfg.Collection) == "" {
		return nil, fmt.Errorf("gift claim dependencies are incomplete")
	}
	if cfg.ChallengeTTL == 0 {
		cfg.ChallengeTTL = 5 * time.Minute
	}
	if cfg.ProofTTL == 0 {
		cfg.ProofTTL = 5 * time.Minute
	}
	if cfg.InitDataTTL == 0 {
		cfg.InitDataTTL = 15 * time.Minute
	}
	if cfg.OwnershipSyncInterval == 0 {
		cfg.OwnershipSyncInterval = 15 * time.Second
	}
	if cfg.OwnershipSyncBatch == 0 {
		cfg.OwnershipSyncBatch = 100
	}
	if cfg.ChallengeTTL < time.Minute || cfg.ChallengeTTL > 15*time.Minute ||
		cfg.ProofTTL < time.Minute || cfg.ProofTTL > 15*time.Minute || cfg.InitDataTTL > 24*time.Hour ||
		cfg.OwnershipSyncInterval < 5*time.Second || cfg.OwnershipSyncInterval > time.Hour ||
		cfg.OwnershipSyncBatch < 1 || cfg.OwnershipSyncBatch > 1000 {
		return nil, fmt.Errorf("gift claim TTL is invalid")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "InvGram Gifts"
	}
	base.Path, base.RawQuery, base.Fragment = strings.TrimRight(base.Path, "/"), "", ""
	return &Service{
		publicBaseURL: strings.TrimRight(base.String(), "/"), proofDomain: base.Host,
		botToken: strings.TrimSpace(cfg.BotToken), basePath: basePathInit, botHandle: botHandle,
		collection: strings.TrimSpace(cfg.Collection), appName: appName,
		challengeTTL: cfg.ChallengeTTL, proofTTL: cfg.ProofTTL, initDataTTL: cfg.InitDataTTL,
		ownershipSyncInterval: cfg.OwnershipSyncInterval, ownershipSyncBatch: cfg.OwnershipSyncBatch,
		store: store, verifier: verifier, minter: cfg.Minter, withdrawer: cfg.Withdrawer,
		password: cfg.Password, logger: logger,
		limiter: cfg.Limiter, apiRateLimit: cfg.APIRateLimit, apiRateWindow: cfg.APIRateWindow,
		withdrawRateLimit: cfg.WithdrawRateLimit, withdrawRateWindow: cfg.WithdrawRateWindow,
	}, nil
}

func (s *Service) Close() {
	if s != nil && s.verifier != nil {
		s.verifier.Close()
	}
}

func (s *Service) authenticate(initData string, now time.Time) (webAppUser, error) {
	user, err := verifyInitData(initData, s.botToken, s.initDataTTL, now)
	if err != nil {
		return webAppUser{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	return user, nil
}

func (s *Service) Challenge(ctx context.Context, initData, giftRef string, now time.Time) (ChallengeResponse, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return ChallengeResponse{}, err
	}
	gift, found, err := s.resolveGift(ctx, normalizeGiftRef(giftRef))
	if err != nil {
		return ChallengeResponse{}, err
	}
	if !found {
		return ChallengeResponse{}, ErrGiftUnavailable
	}
	if gift.OwnerAddress != "" && gift.GiftAddress != "" && !gift.Burned {
		return s.claimChallenge(ctx, user.ID, gift, now)
	}
	if s.minter == nil {
		return ChallengeResponse{}, ErrMintUnavailable
	}
	withdrawal, found, err := s.store.WithdrawalForGift(ctx, gift.ID, user.ID)
	if err != nil {
		return ChallengeResponse{}, err
	}
	if !found || withdrawal.Status != "pending" || withdrawal.ExpiresAt <= int(now.Unix()) {
		if found && withdrawal.Status == "completed" {
			// The withdrawal finished but the gift is not on-chain yet; a retry
			// after the relayer updates the database succeeds.
			return ChallengeResponse{}, ErrGiftUnavailable
		}
		return ChallengeResponse{}, ErrWithdrawalRequired
	}
	challenge, err := s.store.CreateChallenge(ctx, user.ID, gift.ID, now, s.challengeTTL)
	if err != nil {
		return ChallengeResponse{}, err
	}
	return ChallengeResponse{Mode: "mint", Payload: challenge.Payload, ExpiresAt: challenge.ExpiresAt,
		RequestID: withdrawal.ProviderRequestID, GiftTitle: gift.Title, GiftSlug: gift.Slug,
		NFTAddress: gift.GiftAddress, WalletAddress: gift.OwnerAddress}, nil
}

func (s *Service) resolveGift(ctx context.Context, ref string) (domain.UniqueStarGift, bool, error) {
	if gift, found, err := s.store.ResolveGiftByRef(ctx, ref); found || err != nil {
		return gift, found, err
	}
	withdrawal, found, err := s.store.WithdrawalByRequestID(ctx, ref)
	if err != nil {
		return domain.UniqueStarGift{}, false, err
	}
	if !found {
		return domain.UniqueStarGift{}, false, nil
	}
	return withdrawal.Gift, true, nil
}

func (s *Service) claimChallenge(ctx context.Context, userID int64, gift domain.UniqueStarGift, now time.Time) (ChallengeResponse, error) {
	challenge, err := s.store.CreateChallenge(ctx, userID, gift.ID, now, s.challengeTTL)
	if err != nil {
		return ChallengeResponse{}, err
	}
	ownerProfile := ""
	if gift.Host.Type == domain.PeerTypeUser && gift.Host.ID > 0 {
		if username, usernameErr := s.store.ProfileUsername(ctx, gift.Host.ID); usernameErr == nil && strings.TrimSpace(username) != "" {
			ownerProfile = "@" + strings.TrimPrefix(strings.TrimSpace(username), "@")
		} else {
			ownerProfile = fmt.Sprintf("user:%d", gift.Host.ID)
		}
	}
	return ChallengeResponse{Mode: "claim", Payload: challenge.Payload, ExpiresAt: challenge.ExpiresAt,
		GiftTitle: gift.Title, GiftSlug: gift.Slug, NFTAddress: gift.GiftAddress,
		WalletAddress: gift.OwnerAddress, OwnerProfile: ownerProfile}, nil
}

func (s *Service) Claim(ctx context.Context, initData string, input ClaimInput, now time.Time) (ClaimResponse, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return ClaimResponse{}, err
	}
	challenge, walletAddress, err := s.resolveClaim(ctx, user.ID, input, now)
	if err != nil {
		return ClaimResponse{}, err
	}
	expectedPreviousWallet, err := canonicalMainnetAddress(challenge.Unique.OwnerAddress)
	if err != nil {
		// A stale or malformed database projection must never be replaced by a
		// claim. Ownership sync can repair it before the user retries.
		return ClaimResponse{}, ErrInvalid
	}
	itemAddress, err := s.verifier.VerifyMint(ctx, s.collection, big.NewInt(challenge.Unique.ID), walletAddress)
	if err != nil || itemAddress != challenge.Unique.GiftAddress {
		return ClaimResponse{}, ErrNotOwner
	}
	// VerifyMint checks the collection/index/owner tuple. Read the owner once
	// more immediately before the database CAS so a transfer observed between
	// the first chain read and the commit fails closed instead of publishing a
	// stale profile projection. The ownership watcher remains the eventual
	// repair path for a transfer occurring after this final read.
	currentOwner, activeCollection, ownerErr := s.verifier.CurrentNFTOwner(
		ctx, s.collection, big.NewInt(challenge.Unique.ID), itemAddress,
	)
	if ownerErr != nil || !activeCollection {
		return ClaimResponse{}, ErrNotOwner
	}
	currentOwner, err = canonicalMainnetAddress(currentOwner)
	if err != nil || currentOwner != walletAddress {
		return ClaimResponse{}, ErrNotOwner
	}
	result, err := s.store.CommitClaim(ctx, domain.StarGiftOnChainClaim{
		Payload: input.Payload, UserID: user.ID, UniqueGiftID: challenge.Unique.ID,
		ExpectedPreviousWallet: expectedPreviousWallet,
		WalletAddress:          walletAddress, GiftAddress: itemAddress, ClaimedAt: int(now.Unix()),
	})
	if err != nil {
		return ClaimResponse{}, err
	}
	profile := "@" + strings.TrimPrefix(strings.TrimSpace(result.ProfileUsername), "@")
	return ClaimResponse{OwnerProfile: profile, OwnerUserID: user.ID, WalletAddress: walletAddress,
		NFTAddress: itemAddress, GiftSlug: result.Gift.Slug}, nil
}

type MintResponse struct {
	Payload       string     `json:"payload"`
	WalletAddress string     `json:"wallet_address"`
	Intent        MintIntent `json:"intent"`
	GiftTitle     string     `json:"gift_title"`
	GiftSlug      string     `json:"gift_slug"`
}

type AdminExportInput struct {
	Gift          string `json:"gift"`
	WalletName    string `json:"wallet_name"`
	WalletAddress string `json:"wallet_address"`
}

type AdminExportResult struct {
	GiftSlug     string `json:"gift_slug"`
	OwnerName    string `json:"owner_name"`
	OwnerAddress string `json:"owner_address"`
	Host         string `json:"host"`
}

type WalletSendInput struct {
	Gift          string `json:"gift"`
	WalletName    string `json:"wallet_name"`
	WalletAddress string `json:"wallet_address"`
}

type WalletSendResult struct {
	GiftSlug     string `json:"gift_slug"`
	OwnerName    string `json:"owner_name"`
	OwnerAddress string `json:"owner_address"`
}

type WithdrawResult struct {
	GiftSlug  string `json:"gift_slug"`
	URL       string `json:"url"`
	ExpiresAt int    `json:"expires_at"`
}

func (s *Service) Mint(ctx context.Context, initData string, input ClaimInput, now time.Time) (MintResponse, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return MintResponse{}, err
	}
	if s.minter == nil {
		return MintResponse{}, ErrMintUnavailable
	}
	challenge, walletAddress, err := s.resolveClaim(ctx, user.ID, input, now)
	if err != nil {
		return MintResponse{}, err
	}
	if challenge.Unique.OwnerAddress != "" || challenge.Unique.GiftAddress != "" || challenge.Unique.Burned {
		// The NFT got minted while the proof was in flight; walk the user back
		// to the claim (attach) flow instead of minting a second item.
		return MintResponse{}, ErrGiftUnavailable
	}
	withdrawal, found, err := s.store.WithdrawalForGift(ctx, challenge.Unique.ID, user.ID)
	if err != nil {
		return MintResponse{}, err
	}
	if !found || withdrawal.Status != "pending" || withdrawal.ExpiresAt <= int(now.Unix()) {
		return MintResponse{}, ErrWithdrawalRequired
	}
	intent, err := s.minter.MintIntent(ctx, withdrawal.ProviderRequestID, walletAddress, now)
	if err != nil {
		return MintResponse{}, err
	}
	return MintResponse{Payload: input.Payload, WalletAddress: walletAddress, Intent: intent,
		GiftTitle: challenge.Unique.Title, GiftSlug: challenge.Unique.Slug}, nil
}

func (s *Service) ConfirmMint(ctx context.Context, initData string, input ClaimInput, now time.Time) (ClaimResponse, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return ClaimResponse{}, err
	}
	if s.minter == nil {
		return ClaimResponse{}, ErrMintUnavailable
	}
	challenge, walletAddress, err := s.resolveClaim(ctx, user.ID, input, now)
	if err != nil {
		return ClaimResponse{}, err
	}
	withdrawal, found, err := s.store.WithdrawalForGift(ctx, challenge.Unique.ID, user.ID)
	if err != nil {
		return ClaimResponse{}, err
	}
	if !found {
		return ClaimResponse{}, ErrWithdrawalRequired
	}
	confirmation, err := s.minter.ConfirmMint(ctx, withdrawal.ProviderRequestID, walletAddress, now)
	if err != nil {
		return ClaimResponse{}, err
	}
	result, err := s.store.CommitClaim(ctx, domain.StarGiftOnChainClaim{
		Payload: input.Payload, UserID: user.ID, UniqueGiftID: challenge.Unique.ID,
		ExpectedPreviousWallet: walletAddress,
		WalletAddress:          walletAddress, GiftAddress: confirmation.GiftAddress, ClaimedAt: int(now.Unix()),
	})
	if err != nil {
		return ClaimResponse{}, err
	}
	profile := "@" + strings.TrimPrefix(strings.TrimSpace(result.ProfileUsername), "@")
	return ClaimResponse{OwnerProfile: profile, OwnerUserID: user.ID, WalletAddress: walletAddress,
		NFTAddress: confirmation.GiftAddress, GiftSlug: result.Gift.Slug}, nil
}

// AdminStatus reports whether the authenticated Mini App user is a support
// operator eligible to use the test export panel.
func (s *Service) AdminStatus(ctx context.Context, initData string, now time.Time) (bool, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return false, err
	}
	store, ok := s.store.(adminStore)
	if !ok {
		return false, nil
	}
	return store.IsSupportUser(ctx, user.ID)
}

func (s *Service) AdminExportGift(ctx context.Context, initData string, input AdminExportInput, now time.Time) (AdminExportResult, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return AdminExportResult{}, err
	}
	store, ok := s.store.(adminStore)
	if !ok {
		return AdminExportResult{}, ErrAdminUnavailable
	}
	support, err := store.IsSupportUser(ctx, user.ID)
	if err != nil {
		return AdminExportResult{}, err
	}
	if !support {
		return AdminExportResult{}, ErrAdminRequired
	}
	giftRef := normalizeGiftRef(input.Gift)
	if giftRef == "" {
		return AdminExportResult{}, ErrInvalid
	}
	gift, found, err := s.store.ResolveGiftByRef(ctx, giftRef)
	if err != nil {
		return AdminExportResult{}, err
	}
	if !found || gift.Burned {
		return AdminExportResult{}, ErrGiftUnavailable
	}
	if gift.OwnerAddress != "" && gift.GiftAddress != "" {
		return AdminExportResult{}, ErrAlreadyExported
	}
	name := strings.TrimSpace(input.WalletName)
	if name == "" || len(name) > 64 {
		return AdminExportResult{}, ErrInvalid
	}
	address := ""
	if strings.TrimSpace(input.WalletAddress) != "" {
		address, err = canonicalMainnetAddress(input.WalletAddress)
		if err != nil {
			return AdminExportResult{}, ErrInvalid
		}
	}
	unique, err := store.AdminExportGift(ctx, domain.StarGiftAdminExport{
		UniqueGiftID: gift.ID, WalletName: name, WalletAddress: address,
		ExportedBy: user.ID, Date: int(now.Unix()),
	})
	if err != nil {
		return AdminExportResult{}, err
	}
	host := "@relayer"
	if username, usernameErr := s.store.ProfileUsername(ctx, domain.GiftRelayerUserID); usernameErr == nil && strings.TrimSpace(username) != "" {
		host = "@" + strings.TrimPrefix(strings.TrimSpace(username), "@")
	}
	return AdminExportResult{GiftSlug: unique.Slug, OwnerName: unique.OwnerName,
		OwnerAddress: unique.OwnerAddress, Host: host}, nil
}

// SendToWallet records a wallet as the gift's owner without any TON
// transaction: the projection lives only in the local ledger.
func (s *Service) SendToWallet(ctx context.Context, initData string, input WalletSendInput, now time.Time) (WalletSendResult, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return WalletSendResult{}, err
	}
	store, ok := s.store.(walletStore)
	if !ok {
		return WalletSendResult{}, ErrWalletUnavailable
	}
	giftRef := normalizeGiftRef(input.Gift)
	if giftRef == "" {
		return WalletSendResult{}, ErrInvalid
	}
	gift, found, err := s.store.ResolveGiftByRef(ctx, giftRef)
	if err != nil {
		return WalletSendResult{}, err
	}
	if !found || gift.Burned {
		return WalletSendResult{}, ErrGiftUnavailable
	}
	if gift.GiftAddress != "" {
		return WalletSendResult{}, ErrGiftOnChain
	}
	if err := s.requireSavedOwner(ctx, store, gift.ID, user.ID); err != nil {
		return WalletSendResult{}, err
	}
	name := strings.TrimSpace(input.WalletName)
	if name == "" || len(name) > 64 {
		return WalletSendResult{}, ErrInvalid
	}
	address, err := canonicalMainnetAddress(input.WalletAddress)
	if err != nil {
		return WalletSendResult{}, ErrInvalid
	}
	unique, err := store.WalletBindGift(ctx, domain.StarGiftWalletBind{
		UniqueGiftID: gift.ID, WalletName: name, WalletAddress: address, ActorUserID: user.ID,
	})
	if err != nil {
		return WalletSendResult{}, err
	}
	s.logger.Info("star gift bound to wallet",
		zap.String("slug", unique.Slug), zap.String("wallet", address), zap.Int64("actor", user.ID))
	return WalletSendResult{GiftSlug: unique.Slug, OwnerName: unique.OwnerName,
		OwnerAddress: unique.OwnerAddress}, nil
}

func (s *Service) ReleaseFromWallet(
	ctx context.Context,
	initData,
	giftRef string,
	now time.Time) (WalletSendResult, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return WalletSendResult{}, err
	}
	store, ok := s.store.(walletStore)
	if !ok {
		return WalletSendResult{}, ErrWalletUnavailable
	}
	giftRef = normalizeGiftRef(giftRef)
	if giftRef == "" {
		return WalletSendResult{}, ErrInvalid
	}
	gift, found, err := s.store.ResolveGiftByRef(ctx, giftRef)
	if err != nil {
		return WalletSendResult{}, err
	}
	if !found || gift.Burned {
		return WalletSendResult{}, ErrGiftUnavailable
	}
	if gift.OwnerAddress == "" || gift.GiftAddress != "" {
		return WalletSendResult{}, ErrGiftUnavailable
	}
	if err := s.requireSavedOwner(ctx, store, gift.ID, user.ID); err != nil {
		return WalletSendResult{}, err
	}
	unique, err := store.WalletReleaseGift(ctx, gift.ID, user.ID)
	if err != nil {
		return WalletSendResult{}, err
	}
	s.logger.Info("star gift released from wallet",
		zap.String("slug", unique.Slug), zap.Int64("actor", user.ID))
	return WalletSendResult{GiftSlug: unique.Slug, OwnerName: unique.OwnerName,
		OwnerAddress: unique.OwnerAddress}, nil
}

// WithdrawalPasswordChallenge rotates and returns the 2FA SRP parameters for
// the Mini App session. Every call invalidates the previous challenge, exactly
// like account.getPassword, so a captured answer cannot be replayed after the
// next attempt.
func (s *Service) WithdrawalPasswordChallenge(
	ctx context.Context,
	initData string,
	now time.Time) (PasswordChallenge, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return PasswordChallenge{}, err
	}
	if s.password == nil {
		return PasswordChallenge{}, ErrWalletUnavailable
	}
	settings, err := s.password.GetPassword(ctx, user.ID)
	if err != nil {
		return PasswordChallenge{}, ErrWalletUnavailable
	}
	if !settings.HasPassword {
		return PasswordChallenge{HasPassword: false}, nil
	}
	algo := settings.NewAlgo
	if settings.CurrentAlgo != nil {
		algo = *settings.CurrentAlgo
	}
	return PasswordChallenge{
		HasPassword: true,
		SRPID:       settings.SRPID,
		SRPB:        hex.EncodeToString(settings.SRPB),
		G:           algo.G,
		P:           hex.EncodeToString(algo.P),
		Salt1:       hex.EncodeToString(algo.Salt1),
		Salt2:       hex.EncodeToString(algo.Salt2),
	}, nil
}

// WithdrawToChain issues the export link payments.getStarGiftWithdrawalUrl returns over MTProto,
// gated on the same 2FA SRP answer the MTProto method expects.
func (s *Service) WithdrawToChain(
	ctx context.Context,
	initData,
	giftRef string,
	password *WithdrawalPasswordInput,
	now time.Time) (WithdrawResult, error) {
	user, err := s.authenticate(initData, now)
	if err != nil {
		return WithdrawResult{}, err
	}
	if err := s.checkWithdrawalPassword(ctx, user.ID, password); err != nil {
		return WithdrawResult{}, err
	}
	store, ok := s.store.(walletStore)
	if !ok {
		return WithdrawResult{}, ErrWalletUnavailable
	}
	if s.withdrawer == nil {
		return WithdrawResult{}, ErrWalletUnavailable
	}
	giftRef = normalizeGiftRef(giftRef)
	if giftRef == "" {
		return WithdrawResult{}, ErrInvalid
	}
	gift, found, err := s.store.ResolveGiftByRef(ctx, giftRef)
	if err != nil {
		return WithdrawResult{}, err
	}
	if !found || gift.Burned {
		return WithdrawResult{}, ErrGiftUnavailable
	}
	if gift.GiftAddress != "" {
		return WithdrawResult{}, ErrGiftOnChain
	}
	if err := s.requireSavedOwner(ctx, store, gift.ID, user.ID); err != nil {
		return WithdrawResult{}, err
	}
	withdrawal, err := s.withdrawer.Withdraw(ctx, domain.StarGiftWithdrawalRequest{
		UserID: user.ID,
		Ref:    domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: user.ID}, Slug: gift.Slug},
		Date:   int(now.Unix()),
	})
	if err != nil {
		if errors.Is(err, domain.ErrStarGiftTransferUnavailable) {
			return WithdrawResult{}, ErrExportUnavailable
		}
		return WithdrawResult{}, err
	}
	if strings.TrimSpace(withdrawal.URL) == "" {
		return WithdrawResult{}, ErrWalletUnavailable
	}
	s.logger.Info("star gift withdrawal link issued",
		zap.String("slug", gift.Slug), zap.String("status", withdrawal.Status), zap.Int64("actor", user.ID))
	return WithdrawResult{GiftSlug: gift.Slug, URL: withdrawal.URL, ExpiresAt: withdrawal.ExpiresAt}, nil
}

// WHY: unique_star_gifts.owner_peer is NULL while a wallet owns the gift, so
// the authorization must come from the saved-gift row that keeps the owner.
func (s *Service) requireSavedOwner(
	ctx context.Context,
	store walletStore,
	giftID,
	userID int64) error {
	peerType, peerID, found, err := store.SavedGiftOwner(ctx, giftID)
	if err != nil {
		return err
	}
	if !found || peerType != "user" || peerID != userID {
		return ErrProfileOwnerOnly
	}
	return nil
}

// checkWithdrawalPassword maps one SRP answer onto the account 2FA state. An
// absent answer means inputCheckPasswordEmpty, which checkSRP accepts only for
// accounts that never set a password.
func (s *Service) checkWithdrawalPassword(ctx context.Context, userID int64, in *WithdrawalPasswordInput) error {
	if s.password == nil {
		return ErrWalletUnavailable
	}
	check := domain.PasswordCheck{Empty: true}
	if in != nil {
		a, errA := hex.DecodeString(strings.TrimSpace(in.A))
		m1, errM1 := hex.DecodeString(strings.TrimSpace(in.M1))
		if errA != nil || errM1 != nil || len(a) == 0 || len(m1) == 0 {
			return ErrPasswordInvalid
		}
		check = domain.PasswordCheck{SRPID: in.SRPID, A: a, M1: m1}
	}
	if err := s.password.CheckPassword(ctx, userID, check); err != nil {
		return ErrPasswordInvalid
	}
	return nil
}

func (s *Service) resolveClaim(ctx context.Context, userID int64, input ClaimInput, now time.Time) (domain.StarGiftClaimChallenge, string, error) {
	if input.Account.Chain != "-239" || input.Payload == "" || input.Proof.Payload != input.Payload {
		return domain.StarGiftClaimChallenge{}, "", ErrInvalid
	}
	challenge, found, err := s.store.ResolveChallenge(ctx, input.Payload, userID, int(now.Unix()))
	if err != nil {
		return domain.StarGiftClaimChallenge{}, "", err
	}
	if !found {
		return domain.StarGiftClaimChallenge{}, "", ErrExpired
	}
	walletAddress, err := canonicalMainnetAddress(input.Account.Address)
	if err != nil {
		return domain.StarGiftClaimChallenge{}, "", ErrInvalid
	}
	signature, err := decodeBase64(input.Proof.Signature, 64, 64)
	if err != nil {
		return domain.StarGiftClaimChallenge{}, "", ErrInvalid
	}
	stateInit, err := decodeBase64(input.Account.WalletStateInit, 1, 32<<10)
	if err != nil {
		return domain.StarGiftClaimChallenge{}, "", ErrInvalid
	}
	proof := tonwallet.TonConnectProof{Timestamp: input.Proof.Timestamp, Signature: signature, Payload: input.Proof.Payload}
	proof.Domain.LengthBytes, proof.Domain.Value = input.Proof.Domain.LengthBytes, input.Proof.Domain.Value
	if err := s.verifier.VerifyWalletProof(ctx, walletAddress, s.proofDomain, proof, stateInit, s.proofTTL); err != nil {
		s.logger.Info("TON Proof rejected", zap.Int64("user_id", userID), zap.Error(err))
		return domain.StarGiftClaimChallenge{}, "", ErrUnauthorized
	}
	return challenge, walletAddress, nil
}

func normalizeGiftRef(ref string) string {
	ref = strings.TrimSpace(strings.TrimPrefix(ref, "https://"))
	if parsed, err := canonicalMainnetAddress(ref); err == nil {
		return parsed
	}
	return strings.TrimPrefix(ref, "nft/")
}

func canonicalMainnetAddress(raw string) (string, error) {
	return domain.CanonicalTONAddress(raw)
}

func decodeBase64(value string, minLen, maxLen int) ([]byte, error) {
	value = strings.TrimSpace(value)
	var out []byte
	var err error
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		out, err = encoding.DecodeString(value)
		if err == nil {
			break
		}
	}
	if err != nil || len(out) < minLen || len(out) > maxLen {
		return nil, errors.New("invalid base64")
	}
	return out, nil
}
