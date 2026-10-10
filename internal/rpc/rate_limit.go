package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

const sendRateLimitKeyPrefix = "messages:send:"

const (
	authCodePhoneRateLimitKeyPrefix   = "auth:code:phone-sha256:"
	authCodeAuthKeyRateLimitKeyPrefix = "auth:code:raw-auth-key:"
	defaultAuthCodeRateWindow         = 10 * time.Minute
)

const (
	channelDifferenceRateLimitKeyPrefix = "updates:channeldifference:"
	peerDialogsRateLimitKeyPrefix       = "messages:peerdialogs:"
	defaultCatchupRateWindow            = time.Minute
)

// checkCatchupRateLimit 对 difference 类 catch-up RPC（getChannelDifference / getPeerDialogs）按
// 每用户频率限速，超限返回 FLOOD_WAIT（设计 Phase 2 / §10.3）。keyPrefix 区分两类（各自独立计数），
// 共用 cfg.CatchupRateLimit/Window 阈值。Limiter 未装配或阈值 <=0 时不限速（行为不变）。
func (r *Router) checkCatchupRateLimit(ctx context.Context, userID int64, keyPrefix string) error {
	if r.deps.Limiter == nil || userID == 0 {
		return nil
	}
	limit := r.cfg.CatchupRateLimit
	if limit <= 0 {
		return nil
	}
	window := r.cfg.CatchupRateWindow
	if window <= 0 {
		window = defaultCatchupRateWindow
	}
	allowed, retryAfter, err := r.deps.Limiter.AllowN(ctx, keyPrefix+strconv.FormatInt(userID, 10), 1, limit, window)
	if err != nil {
		return internalErr()
	}
	if allowed {
		return nil
	}
	r.log.Debug("catch-up rpc rate limited (flood wait)",
		zap.Int64("user_id", userID), zap.String("kind", keyPrefix), zap.Int("retry_after", retryAfter))
	return floodWaitErr(retryAfter)
}

func (r *Router) checkSendRateLimit(ctx context.Context, userID int64, cost int) error {
	if r.deps.Limiter == nil || userID == 0 || cost <= 0 {
		return nil
	}
	limit := r.cfg.SendRateLimit
	if limit <= 0 {
		return nil
	}
	window := r.cfg.SendRateWindow
	if window <= 0 {
		window = sendMessageRateWindow
	}
	allowed, retryAfter, err := r.deps.Limiter.AllowN(ctx, sendRateLimitKeyPrefix+strconv.FormatInt(userID, 10), cost, limit, window)
	if err != nil {
		r.log.Warn("message send rate limiter failed",
			append(r.contextLogFields(ctx),
				zap.Error(err),
				zap.Int("cost", cost),
				zap.Int("limit", limit),
				zap.Duration("window", window),
			)...)
		return internalErr()
	}
	if allowed {
		return nil
	}
	r.metrics().MessageRateLimited(retryAfter)
	return floodWaitErr(retryAfter)
}

// checkAuthCodeRateLimit protects the unauthenticated code-issuance path before
// any account lookup or durable 777000 write. Existing and unknown phone numbers
// therefore consume identical budgets and cannot be distinguished through the
// limiter. Plaintext phone numbers are never used as limiter keys or log fields.
func (r *Router) checkAuthCodeRateLimit(ctx context.Context, phone string) error {
	if r.deps.Limiter == nil {
		return nil
	}
	normalizedPhone := domain.NormalizePhone(phone)
	if !domain.ValidPhone(normalizedPhone) {
		return phoneNumberInvalidErr()
	}
	window := r.cfg.AuthCodeRateWindow
	if window <= 0 {
		window = defaultAuthCodeRateWindow
	}
	// Check the connection/auth-key budget first. If that dimension is already
	// blocked, changing phone strings cannot create one phone-digest Redis key
	// per attempt and bypass the intended cardinality bound.
	if limit := r.cfg.AuthCodeAuthKeyRateLimit; limit > 0 {
		if rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx); ok && rawAuthKeyID != ([8]byte{}) {
			if err := r.checkAuthCodeRateLimitKey(ctx, authCodeAuthKeyRateLimitKeyPrefix+hex.EncodeToString(rawAuthKeyID[:]), limit, window, "raw_auth_key"); err != nil {
				return err
			}
		}
	}
	if limit := r.cfg.AuthCodePhoneRateLimit; limit > 0 {
		digest := sha256.Sum256([]byte(normalizedPhone))
		if err := r.checkAuthCodeRateLimitKey(ctx, authCodePhoneRateLimitKeyPrefix+hex.EncodeToString(digest[:]), limit, window, "phone_digest"); err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) checkAuthCodeRateLimitKey(ctx context.Context, key string, limit int, window time.Duration, dimension string) error {
	allowed, retryAfter, err := r.deps.Limiter.AllowN(ctx, key, 1, limit, window)
	if err != nil {
		return internalErr()
	}
	if allowed {
		return nil
	}
	if retryAfter <= 0 {
		retryAfter = 1
	}
	r.log.Debug("auth code issuance rate limited",
		zap.String("dimension", dimension),
		zap.Int("retry_after", retryAfter))
	return floodWaitErr(retryAfter)
}

const (
	srpRateLimitKeyPrefix        = "auth:srp:"
	withdrawalRateLimitKeyPrefix = "payments:withdrawal:"
	paymentRateLimitKeyPrefix    = "payments:move:"
	defaultSRPRateWindow         = time.Minute
	defaultWithdrawalRateWindow  = time.Minute
	defaultPaymentRateWindow     = time.Minute
)

type sensitiveRateGroup int

const (
	rateGroupSRP sensitiveRateGroup = iota + 1
	rateGroupWithdrawal
	rateGroupPayment
)

func (g sensitiveRateGroup) String() string {
	switch g {
	case rateGroupSRP:
		return "srp"
	case rateGroupWithdrawal:
		return "withdrawal"
	default:
		return "payment"
	}
}

// WHY: SRP checks and money-moving RPCs are cheap to request and expensive to
// serve, so the dispatch gate keeps one wire-id table instead of scattering
// rate checks through the handlers; every other method misses in O(1).
var sensitiveRPCRateGroups = map[uint32]sensitiveRateGroup{
	tg.AuthCheckPasswordRequestTypeID:                    rateGroupSRP,
	tg.AccountGetPasswordRequestTypeID:                   rateGroupSRP,
	tg.AccountUpdatePasswordSettingsRequestTypeID:        rateGroupSRP,
	tg.AccountDeleteAccountRequestTypeID:                 rateGroupSRP,
	tg.MessagesEditChatCreatorRequestTypeID:              rateGroupSRP,
	tg.PaymentsGetStarGiftWithdrawalURLRequestTypeID:     rateGroupWithdrawal,
	tg.PaymentsGetStarsRevenueWithdrawalURLRequestTypeID: rateGroupWithdrawal,
	tg.PaymentsSendStarsFormRequestTypeID:                rateGroupPayment,
	tg.PaymentsSendPaymentFormRequestTypeID:              rateGroupPayment,
	tg.PaymentsUpgradeStarGiftRequestTypeID:              rateGroupPayment,
	tg.PaymentsTransferStarGiftRequestTypeID:             rateGroupPayment,
	tg.PaymentsSendStarGiftOfferRequestTypeID:            rateGroupPayment,
	tg.PaymentsResolveStarGiftOfferRequestTypeID:         rateGroupPayment,
	tg.PaymentsUpdateStarGiftPriceRequestTypeID:          rateGroupPayment,
	tg.PaymentsCraftStarGiftRequestTypeID:                rateGroupPayment,
	tg.PaymentsSaveStarGiftRequestTypeID:                 rateGroupPayment,
	tg.PaymentsConvertStarGiftRequestTypeID:              rateGroupPayment,
}

// checkSensitiveRPCRateLimit runs in the dispatch pipeline before any handler
// state: authorized calls spend a per-user budget, pre-auth calls (login-time
// password checks) fall back to the physical auth key, and over-budget calls
// get the same FLOOD_WAIT the other limiter gates return.
func (r *Router) checkSensitiveRPCRateLimit(ctx context.Context, id uint32) error {
	if r.deps.Limiter == nil {
		return nil
	}
	group, ok := sensitiveRPCRateGroups[id]
	if !ok {
		return nil
	}
	var keyPrefix string
	var limit int
	var window time.Duration
	switch group {
	case rateGroupSRP:
		keyPrefix, limit, window = srpRateLimitKeyPrefix, r.cfg.SRPRateLimit, r.cfg.SRPRateWindow
		if window <= 0 {
			window = defaultSRPRateWindow
		}
	case rateGroupWithdrawal:
		keyPrefix, limit, window = withdrawalRateLimitKeyPrefix, r.cfg.WithdrawalRateLimit, r.cfg.WithdrawalRateWindow
		if window <= 0 {
			window = defaultWithdrawalRateWindow
		}
	default:
		keyPrefix, limit, window = paymentRateLimitKeyPrefix, r.cfg.PaymentRateLimit, r.cfg.PaymentRateWindow
		if window <= 0 {
			window = defaultPaymentRateWindow
		}
	}
	if limit <= 0 {
		return nil
	}
	subject, ok := rateLimitSubject(ctx)
	if !ok {
		return nil
	}
	allowed, retryAfter, err := r.deps.Limiter.AllowN(ctx, keyPrefix+subject, 1, limit, window)
	if err != nil {
		return internalErr()
	}
	if allowed {
		return nil
	}
	if retryAfter <= 0 {
		retryAfter = 1
	}
	r.log.Debug("sensitive rpc rate limited",
		zap.String("method", tlTypeName(id)),
		zap.String("group", group.String()),
		zap.Int("retry_after", retryAfter))
	return floodWaitErr(retryAfter)
}

// rateLimitSubject prefers the authorized user; pre-auth password checks fall back to the connection's auth key.
func rateLimitSubject(ctx context.Context) (string, bool) {
	if userID, ok := UserIDFrom(ctx); ok && userID != 0 {
		return "u:" + strconv.FormatInt(userID, 10), true
	}
	if rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx); ok && rawAuthKeyID != ([8]byte{}) {
		return "k:" + hex.EncodeToString(rawAuthKeyID[:]), true
	}
	return "", false
}
