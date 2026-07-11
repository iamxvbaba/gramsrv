package rpc

import (
	"context"

	"go.uber.org/zap"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

type phoneChangeEventConfirmer interface {
	ConfirmEvent(ctx context.Context, authKeyID [8]byte, userID int64, event domain.UpdateEvent) error
}

type phoneChangeReliableDispatchReporter interface {
	PhoneChangeUsesReliableDispatch() bool
}

func (r *Router) onAccountSendChangePhoneCode(ctx context.Context, req *tg.AccountSendChangePhoneCodeRequest) (tg.AuthSentCodeClass, error) {
	userID, found, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !found || r.deps.Account == nil {
		return nil, authKeyUnregisteredErr()
	}
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok || authKeyID == ([8]byte{}) {
		return nil, authKeyUnregisteredErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	hash, delivery, err := r.deps.Account.SendChangePhoneCode(ctx, userID, authKeyID, sessionID, req.PhoneNumber)
	if err != nil {
		return nil, phoneChangeErr(err)
	}
	return tgSMSSentCode(hash, delivery.Length), nil
}

func (r *Router) onAccountChangePhone(ctx context.Context, req *tg.AccountChangePhoneRequest) (tg.UserClass, error) {
	userID, found, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !found || r.deps.Account == nil {
		return nil, authKeyUnregisteredErr()
	}
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok || authKeyID == ([8]byte{}) {
		return nil, authKeyUnregisteredErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	originRawAuthKeyID := rawAuthKeyIDForOrigin(ctx)
	result, err := r.deps.Account.ChangePhone(
		ctx,
		userID,
		authKeyID,
		originRawAuthKeyID,
		sessionID,
		req.PhoneNumber,
		req.PhoneCodeHash,
		req.PhoneCode,
		int(r.clock.Now().Unix()),
	)
	if err != nil {
		return nil, phoneChangeErr(err)
	}
	if result.User.ID == 0 {
		return nil, internalErr()
	}
	r.invalidateRPCProjectionForUser(result.User.ID)
	if result.Event.Pts > 0 {
		if confirmer, ok := r.deps.Updates.(phoneChangeEventConfirmer); ok {
			if err := confirmer.ConfirmEvent(ctx, authKeyID, userID, result.Event); err != nil {
				// user/event/outbox 已原子提交，不能把已成功改号伪装成失败；当前
				// session 仍会收到 pts 簿记，设备水位存储可由后续 getDifference 自愈。
				r.log.Warn("confirm phone change event", zap.Int64("user_id", userID), zap.Int("pts", result.Event.Pts), zap.Error(err))
			}
		}
		reliable := false
		if reporter, ok := r.deps.Account.(phoneChangeReliableDispatchReporter); ok {
			reliable = reporter.PhoneChangeUsesReliableDispatch()
		}
		if !reliable {
			r.pushUserUpdates(ctx, userID, tgUpdateForOutboxEvent(result.Event))
		}
		r.bookkeepAuxPtsForCurrentSession(ctx, result.Event)
	}
	return r.tgSelfUser(result.User), nil
}
