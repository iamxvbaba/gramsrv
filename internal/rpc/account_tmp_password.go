package rpc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
)

// account.getTmpPassword 签发的临时密码是无状态 HMAC 令牌，绑定
// user、auth key、过期时间与密码最近修改时间：改密码或换设备都会让旧令牌失效。
// 密钥按进程随机生成，重启后已签发令牌失效（客户端会重新索取）。
const (
	tmpPasswordVersion   = 0x01
	tmpPasswordBodyLen   = 1 + 8 + 8 + 8 + 8
	tmpPasswordLen       = tmpPasswordBodyLen + sha256.Size
	tmpPasswordMinPeriod = 60
	tmpPasswordMaxPeriod = 86400
)

type tmpPasswordSigner struct {
	once sync.Once
	key  [32]byte
	err  error
}

func (s *tmpPasswordSigner) mac(body []byte) ([]byte, error) {
	s.once.Do(func() {
		_, s.err = rand.Read(s.key[:])
	})
	if s.err != nil {
		return nil, s.err
	}
	h := hmac.New(sha256.New, s.key[:])
	h.Write(body)
	return h.Sum(nil), nil
}

func tmpPasswordBody(userID int64, authKeyID [8]byte, validUntil int64, passwordChangedAt time.Time) []byte {
	body := make([]byte, 0, tmpPasswordBodyLen)
	body = append(body, tmpPasswordVersion)
	body = binary.LittleEndian.AppendUint64(body, uint64(userID))
	body = append(body, authKeyID[:]...)
	body = binary.LittleEndian.AppendUint64(body, uint64(validUntil))
	var changed int64
	if !passwordChangedAt.IsZero() {
		changed = passwordChangedAt.UnixNano()
	}
	return binary.LittleEndian.AppendUint64(body, uint64(changed))
}

func (s *tmpPasswordSigner) issue(userID int64, authKeyID [8]byte, validUntil int64, passwordChangedAt time.Time) ([]byte, error) {
	body := tmpPasswordBody(userID, authKeyID, validUntil, passwordChangedAt)
	sum, err := s.mac(body)
	if err != nil {
		return nil, err
	}
	return append(body, sum...), nil
}

func (s *tmpPasswordSigner) valid(token []byte, userID int64, authKeyID [8]byte, now time.Time, passwordChangedAt time.Time) bool {
	if len(token) != tmpPasswordLen || token[0] != tmpPasswordVersion {
		return false
	}
	validUntil := int64(binary.LittleEndian.Uint64(token[1+8+8:]))
	want := tmpPasswordBody(userID, authKeyID, validUntil, passwordChangedAt)
	sum, err := s.mac(want)
	if err != nil {
		return false
	}
	if !hmac.Equal(token[:tmpPasswordBodyLen], want) || !hmac.Equal(token[tmpPasswordBodyLen:], sum) {
		return false
	}
	return now.Unix() < validUntil
}

func tmpPasswordInvalidErr() error { return tgerr.New(400, "TMP_PASSWORD_INVALID") }

func (r *Router) onAccountGetTmpPassword(ctx context.Context, req *tg.AccountGetTmpPasswordRequest) (*tg.AccountTmpPassword, error) {
	if req == nil || r.deps.Account == nil {
		return nil, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.userIsBot(ctx, userID) {
		return nil, botMethodInvalidErr()
	}
	if req.Period < tmpPasswordMinPeriod || req.Period > tmpPasswordMaxPeriod {
		return nil, tgerr.New(400, "PERIOD_INVALID")
	}
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok || authKeyID == ([8]byte{}) {
		return nil, authKeyUnregisteredErr()
	}
	state, err := r.deps.Account.RevenueWithdrawalPasswordState(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	if err := r.deps.Account.CheckPassword(ctx, userID, domainPasswordCheck(req.Password)); err != nil {
		return nil, passwordErr(err)
	}
	validUntil := r.clock.Now().Add(time.Duration(req.Period) * time.Second).Unix()
	token, err := r.tmpPasswords.issue(userID, authKeyID, validUntil, state.PasswordChangedAt)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AccountTmpPassword{TmpPassword: token, ValidUntil: int(validUntil)}, nil
}

// checkStarGiftWithdrawalPassword 校验 payments.getStarGiftWithdrawalUrl 的口令。
// Layer 232 传 account.getTmpPassword 签发的 tmp_password；Layer 225–231 的
// SRP 证明由协议层原样打包进 tmp_password，这里按旧逻辑校验。
func (r *Router) checkStarGiftWithdrawalPassword(ctx context.Context, userID int64, authKeyID [8]byte, tmpPassword []byte, passwordChangedAt time.Time) error {
	srp, legacy, err := tlprofile.DecodeLegacySRPTmpPassword(tmpPassword)
	if legacy {
		if err != nil {
			return tmpPasswordInvalidErr()
		}
		if err := r.deps.Account.CheckPassword(ctx, userID, domainPasswordCheck(srp)); err != nil {
			return passwordErr(err)
		}
		return nil
	}
	if !r.tmpPasswords.valid(tmpPassword, userID, authKeyID, r.clock.Now(), passwordChangedAt) {
		return tmpPasswordInvalidErr()
	}
	return nil
}
