package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"go.uber.org/zap"

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
