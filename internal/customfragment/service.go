// Package customfragment implements gramsrv's self-hosted collectible-gift
// bridge. A withdrawal URL is a short-lived bearer capability created only
// after payments.getStarGiftWithdrawalUrl has checked the user's 2FA password.
package customfragment

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"math"
	"math/big"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tvm/cell"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"

	"telesrv/internal/domain"
	"telesrv/internal/lottierender"
)

const (
	mintOpcode            = uint64(0x4637289b)
	mainnetChain          = "-239"
	defaultSubwalletID    = uint32(0x4752414d) // "GRAM"
	defaultMintAmount     = int64(80_000_000)
	defaultAuthorization  = 5 * time.Minute
	maxWithdrawalTokenLen = 256
)

var (
	ErrInvalidRequest  = errors.New("invalid CustomFragment request")
	ErrUnavailable     = errors.New("CustomFragment unavailable")
	ErrNotFinalized    = errors.New("gift NFT is not finalized on TON mainnet")
	ErrAlreadyExported = errors.New("gift is already exported")
)

type Config struct {
	PublicBaseURL       string
	AppName             string
	SigningKeyFile      string
	SigningKey          ed25519.PrivateKey // tests and in-process tooling only
	GiftCollection      string
	CollectionName      string
	MintAmountNanoton   int64
	SubwalletID         uint32
	AuthorizationTTL    time.Duration
	LiteserverConfigURL string
	ModelRenderer       ModelRenderFunc // tests or an alternate production renderer
	// Limiter bounds gift API requests per client IP; nil disables limiting.
	Limiter Limiter
	// APIRateLimit is the per-IP budget for intent/confirm requests; <=0 disables.
	APIRateLimit int
	// APIRateWindow is the sliding window for APIRateLimit.
	APIRateWindow time.Duration
}

type ModelRenderFunc func(data []byte, width, height int, position float64) (*image.NRGBA, error)

type GiftLedger interface {
	ResolveWithdrawal(ctx context.Context, providerRequestID string) (domain.StarGiftWithdrawal, bool, error)
	CompleteWithdrawalOnChain(ctx context.Context, providerRequestID, ownerAddress, giftAddress string, date int) (domain.StarGiftWithdrawal, error)
	UniqueBySlug(ctx context.Context, slug string) (domain.UniqueStarGift, bool, error)
}

// Verifier is intentionally narrow so payload generation is unit-testable
// without network access. Production uses a proof-checking lite-server client.
type Verifier interface {
	VerifyMint(ctx context.Context, collectionRaw string, itemIndex *big.Int, ownerRaw string) (itemRaw string, err error)
	Close()
}

// Limiter matches store.RateLimiter so this package keeps no store dependency.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
}

type Service struct {
	publicBaseURL  string
	appName        string
	privateKey     ed25519.PrivateKey
	publicKey      ed25519.PublicKey
	collection     *address.Address
	collectionName string
	mintAmount     int64
	subwalletID    uint32
	authTTL        time.Duration
	ledger         GiftLedger
	verifier       Verifier
	renderModel    ModelRenderFunc
	imageMu        sync.RWMutex
	imageCache     map[string][]byte
	animationCache map[string][]byte
	confirmSF      singleflight.Group
	limiter        Limiter
	apiRateLimit   int
	apiRateWindow  time.Duration
	logger         *zap.Logger
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

type Confirmation struct {
	Status       string `json:"status"`
	OwnerAddress string `json:"owner_address"`
	GiftAddress  string `json:"gift_address"`
	Collection   string `json:"collection_address"`
}

func New(cfg Config, ledger GiftLedger, verifier Verifier, logger *zap.Logger) (*Service, error) {
	if ledger == nil {
		return nil, fmt.Errorf("CustomFragment gift ledger is nil")
	}
	base, err := url.Parse(strings.TrimSpace(cfg.PublicBaseURL))
	if err != nil || base.Scheme == "" || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return nil, fmt.Errorf("CustomFragment public base URL is invalid")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	base.RawQuery, base.Fragment = "", ""

	collection, err := parseMainnetAddress(cfg.GiftCollection)
	if err != nil {
		return nil, fmt.Errorf("CustomFragment gift collection: %w", err)
	}
	privateKey := append(ed25519.PrivateKey(nil), cfg.SigningKey...)
	if len(privateKey) == 0 {
		privateKey, err = loadSigningKey(cfg.SigningKeyFile)
		if err != nil {
			return nil, fmt.Errorf("CustomFragment signing key: %w", err)
		}
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("CustomFragment signing key must be an Ed25519 private key")
	}
	mintAmount := cfg.MintAmountNanoton
	if mintAmount == 0 {
		mintAmount = defaultMintAmount
	}
	if mintAmount < 40_000_000 || mintAmount > 2_000_000_000 {
		return nil, fmt.Errorf("CustomFragment mint amount must be 0.04..2 TON")
	}
	authTTL := cfg.AuthorizationTTL
	if authTTL == 0 {
		authTTL = defaultAuthorization
	}
	if authTTL < time.Minute || authTTL > 15*time.Minute {
		return nil, fmt.Errorf("CustomFragment authorization TTL must be 1m..15m")
	}
	subwalletID := cfg.SubwalletID
	if subwalletID == 0 {
		subwalletID = defaultSubwalletID
	}
	if verifier == nil {
		verifier = NewMainnetVerifier(cfg.LiteserverConfigURL)
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	renderModel := cfg.ModelRenderer
	if renderModel == nil {
		renderModel = lottierender.Render
	}
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "Gramsrv"
	}
	collectionName := strings.TrimSpace(cfg.CollectionName)
	if collectionName == "" {
		collectionName = "InvGram Gifts"
	}
	if len(collectionName) > 80 {
		return nil, fmt.Errorf("CustomFragment collection name is too long")
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return &Service{
		publicBaseURL: strings.TrimRight(base.String(), "/"), appName: appName,
		privateKey: privateKey, publicKey: publicKey, collection: collection, collectionName: collectionName,
		mintAmount: mintAmount, subwalletID: subwalletID, authTTL: authTTL,
		ledger: ledger, verifier: verifier, logger: logger,
		renderModel: renderModel,
		limiter:     cfg.Limiter, apiRateLimit: cfg.APIRateLimit, apiRateWindow: cfg.APIRateWindow,
		imageCache: make(map[string][]byte), animationCache: make(map[string][]byte),
	}, nil
}

func (s *Service) Close() {
	if s != nil && s.verifier != nil {
		s.verifier.Close()
	}
}

func (s *Service) PublicKeyHex() string {
	if s == nil {
		return ""
	}
	return hex.EncodeToString(s.publicKey)
}

func (s *Service) Intent(ctx context.Context, requestID, walletAddress string, now time.Time) (MintIntent, error) {
	if s == nil || !validRequestID(requestID) {
		return MintIntent{}, ErrInvalidRequest
	}
	withdrawal, found, err := s.ledger.ResolveWithdrawal(ctx, requestID)
	if err != nil {
		return MintIntent{}, err
	}
	if !found {
		return MintIntent{}, ErrInvalidRequest
	}
	if withdrawal.Status == "completed" {
		return MintIntent{}, ErrAlreadyExported
	}
	nowUnix := now.Unix()
	if withdrawal.Status != "pending" || withdrawal.ExpiresAt <= int(nowUnix) || withdrawal.Gift.ID <= 0 {
		return MintIntent{}, ErrUnavailable
	}
	owner, err := parseMainnetAddress(walletAddress)
	if err != nil {
		return MintIntent{}, ErrInvalidRequest
	}
	validSince := nowUnix - 30
	validUntil := now.Add(s.authTTL).Unix()
	if max := int64(withdrawal.ExpiresAt); validUntil > max {
		validUntil = max
	}
	if validSince < 0 || validUntil <= nowUnix || validUntil > math.MaxUint32 {
		return MintIntent{}, ErrUnavailable
	}
	itemIndex := big.NewInt(withdrawal.Gift.ID)
	metadataURL := s.publicBaseURL + path.Join("/custom-fragment/metadata/gift/", url.PathEscape(withdrawal.Gift.Slug)+".json")
	content := cell.BeginCell().MustStoreUInt(1, 8).MustStoreStringSnake(metadataURL).EndCell()
	authorization := cell.BeginCell().
		MustStoreUInt(uint64(s.subwalletID), 32).
		MustStoreUInt(uint64(validSince), 32).
		MustStoreUInt(uint64(validUntil), 32).
		MustStoreBigUInt(itemIndex, 256).
		MustStoreAddr(owner).
		MustStoreRef(content).
		EndCell()
	signature := ed25519.Sign(s.privateKey, authorization.Hash())
	body := cell.BeginCell().
		MustStoreUInt(mintOpcode, 32).
		MustStoreSlice(signature, 512).
		MustStoreRef(authorization).
		EndCell()

	return MintIntent{
		Network: mainnetChain, CollectionAddress: s.collection.String(),
		Amount:  fmt.Sprintf("%d", s.mintAmount),
		Payload: base64.StdEncoding.EncodeToString(body.ToBOC()), ValidUntil: validUntil,
		ItemIndex: itemIndex.String(), WalletAddress: owner.StringRaw(),
	}, nil
}

func (s *Service) Confirm(ctx context.Context, requestID, walletAddress string, now time.Time) (Confirmation, error) {
	if s == nil || !validRequestID(requestID) {
		return Confirmation{}, ErrInvalidRequest
	}
	value, err, _ := s.confirmSF.Do(requestID, func() (any, error) {
		return s.confirmOnce(ctx, requestID, walletAddress, now)
	})
	if err != nil {
		return Confirmation{}, err
	}
	confirmation, ok := value.(Confirmation)
	if !ok {
		return Confirmation{}, ErrUnavailable
	}
	return confirmation, nil
}

// confirmOnce is singleflighted per bearer request ID. The database transaction
// remains the cross-process authority; this layer only prevents a local burst
// of concurrent userbot retries from issuing duplicate lite-server reads.
func (s *Service) confirmOnce(ctx context.Context, requestID, walletAddress string, now time.Time) (Confirmation, error) {
	withdrawal, found, err := s.ledger.ResolveWithdrawal(ctx, requestID)
	if err != nil {
		return Confirmation{}, err
	}
	if !found {
		return Confirmation{}, ErrInvalidRequest
	}
	if withdrawal.Status == "completed" && withdrawal.Gift.OwnerAddress != "" && withdrawal.Gift.GiftAddress != "" {
		return Confirmation{Status: withdrawal.Status, OwnerAddress: withdrawal.Gift.OwnerAddress,
			GiftAddress: withdrawal.Gift.GiftAddress, Collection: s.collection.StringRaw()}, nil
	}
	if withdrawal.Status != "pending" || withdrawal.ExpiresAt <= int(now.Unix()) || withdrawal.Gift.ID <= 0 {
		return Confirmation{}, ErrUnavailable
	}
	owner, err := parseMainnetAddress(walletAddress)
	if err != nil {
		return Confirmation{}, ErrInvalidRequest
	}
	itemAddress, err := s.verifier.VerifyMint(ctx, s.collection.StringRaw(), big.NewInt(withdrawal.Gift.ID), owner.StringRaw())
	if err != nil {
		s.logger.Debug("CustomFragment gift is not finalized", zap.Int64("gift_id", withdrawal.Gift.ID), zap.Error(err))
		return Confirmation{}, fmt.Errorf("%w: %v", ErrNotFinalized, err)
	}
	item, err := parseMainnetAddress(itemAddress)
	if err != nil {
		return Confirmation{}, fmt.Errorf("%w: verifier returned invalid address", ErrNotFinalized)
	}
	completed, err := s.ledger.CompleteWithdrawalOnChain(ctx, requestID, owner.StringRaw(), item.StringRaw(), int(now.Unix()))
	if err != nil {
		return Confirmation{}, err
	}
	return Confirmation{Status: completed.Status, OwnerAddress: completed.Gift.OwnerAddress,
		GiftAddress: completed.Gift.GiftAddress, Collection: s.collection.StringRaw()}, nil
}

func validRequestID(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= maxWithdrawalTokenLen && !strings.ContainsAny(value, "/\\\x00")
}

func parseMainnetAddress(raw string) (*address.Address, error) {
	raw = strings.TrimSpace(raw)
	var parsed *address.Address
	var err error
	if strings.Contains(raw, ":") {
		parsed, err = address.ParseRawAddr(raw)
	} else {
		parsed, err = address.ParseAddr(raw)
	}
	if err != nil || parsed == nil || parsed.IsAddrNone() || parsed.Workchain() != 0 || parsed.IsTestnetOnly() {
		return nil, errors.New("expected a basechain mainnet TON address")
	}
	return parsed, nil
}

func loadSigningKey(filename string) (ed25519.PrivateKey, error) {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return nil, errors.New("signing key file is required")
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	var decoded []byte
	if value, decodeErr := hex.DecodeString(strings.TrimPrefix(trimmed, "hex:")); decodeErr == nil {
		decoded = value
	} else if value, decodeErr := base64.StdEncoding.DecodeString(strings.TrimPrefix(trimmed, "base64:")); decodeErr == nil {
		decoded = value
	} else if value, decodeErr := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(trimmed, "base64:")); decodeErr == nil {
		decoded = value
	} else {
		decoded = raw
	}
	switch len(decoded) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(append([]byte(nil), decoded...)), nil
	default:
		return nil, fmt.Errorf("key file must contain a 32-byte seed or 64-byte private key (hex/base64/raw)")
	}
}
