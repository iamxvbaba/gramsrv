package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
)

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
		if otpRecipientRejected(err) {
			return nil, phoneNumberInvalidErr()
		}
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
	if result.Changed {
		// account.changePhone returns the authoritative self User to the current
		// session. Other online sessions receive a non-PTS updateUser; offline
		// sessions converge on their next full-user/startup read.
		r.pushPremiumStatusUpdate(ctx, result.User)
	}
	return r.tgSelfUserWithUsernames(ctx, result.User), nil
}
