package rpc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"

	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// devCodeLength 是开发固定验证码长度，写入 auth.sentCode 的 type.length。
const devCodeLength = 5

const loginMessagePushDelay = 2 * time.Second

// registerAuth 注册 auth.* RPC handler。
func (r *Router) registerAuth(d *tg.ServerDispatcher) {
	d.OnAuthBindTempAuthKey(r.onAuthBindTempAuthKey)
	d.OnAuthExportLoginToken(r.onAuthExportLoginToken)
	d.OnAuthImportLoginToken(r.onAuthImportLoginToken)
	d.OnAuthAcceptLoginToken(r.onAuthAcceptLoginToken)
	d.OnAuthExportAuthorization(func(ctx context.Context, dcid int) (*tg.AuthExportedAuthorization, error) {
		return nil, dcIDInvalidErr()
	})
	d.OnAuthImportAuthorization(func(ctx context.Context, req *tg.AuthImportAuthorizationRequest) (tg.AuthAuthorizationClass, error) {
		return nil, dcIDInvalidErr()
	})
	d.OnAuthDropTempAuthKeys(func(ctx context.Context, exceptauthkeys []int64) (bool, error) {
		return true, nil
	})
	d.OnAuthInitPasskeyLogin(func(ctx context.Context, req *tg.AuthInitPasskeyLoginRequest) (*tg.AuthPasskeyLoginOptions, error) {
		return &tg.AuthPasskeyLoginOptions{Options: tg.DataJSON{Data: "{}"}}, nil
	})
	d.OnAuthSendCode(r.onAuthSendCode)
	d.OnAuthResendCode(r.onAuthResendCode)
	d.OnAuthCancelCode(r.onAuthCancelCode)
	d.OnAuthSignIn(r.onAuthSignIn)
	d.OnAuthSignUp(r.onAuthSignUp)
	d.OnAuthLogOut(r.onAuthLogOut)
	d.OnAuthResetAuthorizations(r.onAuthResetAuthorizations)
	d.OnAuthCheckPassword(r.onAuthCheckPassword)
	d.OnAuthRequestPasswordRecovery(r.onAuthRequestPasswordRecovery)
	d.OnAuthRecoverPassword(r.onAuthRecoverPassword)
	d.OnAuthCheckRecoveryPassword(r.onAuthCheckRecoveryPassword)
}

// onAuthBindTempAuthKey 记录 TDesktop 的 PFS temp→perm auth key 绑定。
func (r *Router) onAuthBindTempAuthKey(ctx context.Context, req *tg.AuthBindTempAuthKeyRequest) (bool, error) {
	if r.deps.Auth == nil {
		return true, nil
	}
	id, _ := RawAuthKeyIDFrom(ctx)
	if id == ([8]byte{}) {
		id, _ = AuthKeyIDFrom(ctx)
	}
	sessionID, _ := SessionIDFrom(ctx)
	if err := r.deps.Auth.BindTempAuthKey(ctx, sessionID, domain.TempAuthKeyBinding{
		TempAuthKeyID:    id,
		PermAuthKeyID:    req.PermAuthKeyID,
		Nonce:            req.Nonce,
		ExpiresAt:        req.ExpiresAt,
		EncryptedMessage: append([]byte(nil), req.EncryptedMessage...),
	}); err != nil {
		return false, bindTempAuthKeyErr(err)
	}
	if r.deps.Sessions != nil {
		if scoped, ok := r.scopedSessions(); ok {
			rawAuthKeyID, _ := RawAuthKeyIDFrom(ctx)
			scoped.BindAuthKeyForSession(rawAuthKeyID, sessionID, authKeyIDFromInt64(req.PermAuthKeyID))
		} else {
			r.deps.Sessions.BindAuthKey(sessionID, authKeyIDFromInt64(req.PermAuthKeyID))
		}
	}
	r.invalidateAuthUserCache(id)
	return true, nil
}

// onAuthExportLoginToken 给 TDesktop QR 登录页返回一个短期占位 token。
func (r *Router) onAuthExportLoginToken(ctx context.Context, _ *tg.AuthExportLoginTokenRequest) (tg.AuthLoginTokenClass, error) {
	id, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	return tdesktop.LoginToken(r.clock.Now(), id, sessionID), nil
}

func (r *Router) onAuthImportLoginToken(ctx context.Context, token []byte) (tg.AuthLoginTokenClass, error) {
	id, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	return tdesktop.LoginToken(r.clock.Now(), id, sessionID), nil
}

func (r *Router) onAuthAcceptLoginToken(ctx context.Context, token []byte) (*tg.Authorization, error) {
	return nil, authTokenInvalidErr()
}

// onAuthSendCode 处理 auth.sendCode：生成 phone_code_hash 并返回 sentCode。
func (r *Router) onAuthSendCode(ctx context.Context, req *tg.AuthSendCodeRequest) (tg.AuthSentCodeClass, error) {
	hash, err := r.deps.Auth.SendCode(ctx, req.PhoneNumber)
	if err != nil {
		return nil, internalErr()
	}
	return tgSentCode(hash), nil
}

func tgSentCode(hash string) tg.AuthSentCodeClass {
	return &tg.AuthSentCode{
		Type:          &tg.AuthSentCodeTypeApp{Length: devCodeLength},
		PhoneCodeHash: hash,
	}
}

// onAuthSignIn 处理 auth.signIn：校验验证码；用户不存在时返回 SignUpRequired。
func (r *Router) onAuthSignIn(ctx context.Context, req *tg.AuthSignInRequest) (tg.AuthAuthorizationClass, error) {
	u, loginMessage, needSignUp, err := r.deps.Auth.SignIn(ctx, r.authzFromCtx(ctx), req.PhoneNumber, req.PhoneCodeHash, req.PhoneCode)
	if err != nil {
		if errors.Is(err, domain.ErrSessionPasswordNeeded) && u.ID != 0 {
			if err := r.clearAuthKeyStateOnUserChange(ctx, u.ID); err != nil {
				return nil, internalErr()
			}
			if id, ok := AuthKeyIDFrom(ctx); ok {
				r.setAuthUserCache(id, u.ID, true)
			}
			r.bindSessionUser(ctx, u.ID)
		}
		return nil, signInErr(err)
	}
	if needSignUp {
		return &tg.AuthAuthorizationSignUpRequired{}, nil
	}
	if err := r.clearAuthKeyStateOnUserChange(ctx, u.ID); err != nil {
		return nil, internalErr()
	}
	if id, ok := AuthKeyIDFrom(ctx); ok {
		r.setAuthUserCache(id, u.ID, true)
	}
	r.bindSessionUser(ctx, u.ID)
	r.recordAndScheduleLoginMessagePush(ctx, loginMessage)
	r.pushSignInServiceNotificationToOthers(ctx, u)
	return &tg.AuthAuthorization{User: r.tgSelfUser(u)}, nil
}

func (r *Router) onAuthResendCode(ctx context.Context, req *tg.AuthResendCodeRequest) (tg.AuthSentCodeClass, error) {
	hash, err := r.deps.Auth.ResendCode(ctx, req.PhoneNumber, req.PhoneCodeHash)
	if err != nil {
		return nil, signInErr(err)
	}
	return tgSentCode(hash), nil
}

func (r *Router) onAuthCancelCode(ctx context.Context, req *tg.AuthCancelCodeRequest) (bool, error) {
	if err := r.deps.Auth.CancelCode(ctx, req.PhoneNumber, req.PhoneCodeHash); err != nil {
		return false, signInErr(err)
	}
	return true, nil
}

func (r *Router) onAuthResetAuthorizations(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	deleted, err := r.deps.Auth.ResetAuthorizations(ctx, userID, authKeyID)
	if err != nil {
		return false, internalErr()
	}
	for _, a := range deleted {
		r.invalidateAuthUserCache(a.AuthKeyID)
		r.unbindAuthKey(a.AuthKeyID)
		_ = r.clearAuthKeyState(ctx, a.AuthKeyID)
	}
	return true, nil
}

func (r *Router) onAuthCheckPassword(ctx context.Context, password tg.InputCheckPasswordSRPClass) (tg.AuthAuthorizationClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Account == nil {
		return nil, passwordHashInvalidErr()
	}
	if err := r.deps.Account.CheckPassword(ctx, userID, domainPasswordCheck(password)); err != nil {
		return nil, passwordErr(err)
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AuthAuthorization{User: r.tgSelfUser(u)}, nil
}

func (r *Router) onAuthRequestPasswordRecovery(ctx context.Context) (*tg.AuthPasswordRecovery, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	pattern, err := r.deps.Account.RequestPasswordRecovery(ctx, userID)
	if err != nil {
		return nil, passwordErr(err)
	}
	return &tg.AuthPasswordRecovery{EmailPattern: pattern}, nil
}

func (r *Router) onAuthRecoverPassword(ctx context.Context, req *tg.AuthRecoverPasswordRequest) (tg.AuthAuthorizationClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	var input *domain.PasswordInputSettings
	if settings, ok := req.GetNewSettings(); ok {
		converted, err := domainPasswordInputSettings(settings)
		if err != nil {
			return nil, err
		}
		input = &converted
	}
	if err := r.deps.Account.RecoverPassword(ctx, userID, req.Code, input); err != nil {
		return nil, passwordErr(err)
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AuthAuthorization{User: r.tgSelfUser(u)}, nil
}

func (r *Router) onAuthCheckRecoveryPassword(ctx context.Context, code string) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if err := r.deps.Account.CheckRecoveryPassword(ctx, userID, code); err != nil {
		return false, passwordErr(err)
	}
	return true, nil
}

// onAuthSignUp 处理 auth.signUp：创建用户并绑定授权。
func (r *Router) onAuthSignUp(ctx context.Context, req *tg.AuthSignUpRequest) (tg.AuthAuthorizationClass, error) {
	u, loginMessage, err := r.deps.Auth.SignUp(ctx, r.authzFromCtx(ctx), req.PhoneNumber, req.PhoneCodeHash, req.FirstName, req.LastName)
	if err != nil {
		return nil, signInErr(err)
	}
	if err := r.clearAuthKeyStateOnUserChange(ctx, u.ID); err != nil {
		return nil, internalErr()
	}
	if id, ok := AuthKeyIDFrom(ctx); ok {
		r.setAuthUserCache(id, u.ID, true)
	}
	r.bindSessionUser(ctx, u.ID)
	r.recordAndScheduleLoginMessagePush(ctx, loginMessage)
	return &tg.AuthAuthorization{User: r.tgSelfUser(u)}, nil
}

// onAuthLogOut 处理 auth.logOut：解绑当前 auth_key 的授权。
func (r *Router) onAuthLogOut(ctx context.Context) (*tg.AuthLoggedOut, error) {
	id, _ := AuthKeyIDFrom(ctx)
	userID, authorized, userErr := r.currentUserID(ctx)
	if err := r.deps.Auth.LogOut(ctx, id); err != nil {
		return nil, internalErr()
	}
	r.invalidateAuthUserCache(id)
	r.unbindAuthKey(id)
	if userErr == nil && authorized && userID != 0 {
		status := r.setPresenceFromContext(ctx, userID, true)
		r.pushUserStatus(ctx, userID, status)
	}
	if err := r.clearAuthKeyState(ctx, id); err != nil {
		return nil, internalErr()
	}
	return &tg.AuthLoggedOut{}, nil
}

func (r *Router) clearAuthKeyStateOnUserChange(ctx context.Context, newUserID int64) error {
	oldUserID, ok := UserIDFrom(ctx)
	if !ok || oldUserID == 0 || oldUserID == newUserID {
		return nil
	}
	id, ok := AuthKeyIDFrom(ctx)
	if !ok {
		return nil
	}
	return r.clearAuthKeyState(ctx, id)
}

func (r *Router) clearAuthKeyState(ctx context.Context, authKeyID [8]byte) error {
	if r.deps.Updates == nil {
		return nil
	}
	return r.deps.Updates.ClearAuthKey(ctx, authKeyID)
}

func (r *Router) bindSessionUser(ctx context.Context, userID int64) {
	if r.deps.Sessions == nil {
		return
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	if scoped, ok := r.scopedSessions(); ok {
		rawAuthKeyID, _ := RawAuthKeyIDFrom(ctx)
		scoped.BindUserForAuthKey(rawAuthKeyID, sessionID, userID)
		r.announceSessionOnline(ctx, userID)
		return
	}
	r.deps.Sessions.BindUser(sessionID, userID)
	r.announceSessionOnline(ctx, userID)
}

func (r *Router) unbindAuthKey(authKeyID [8]byte) {
	if r.deps.Sessions == nil {
		return
	}
	r.deps.Sessions.UnbindAuthKey(authKeyID)
}

func (r *Router) pushSignInServiceNotificationToOthers(ctx context.Context, u domain.User) {
	if r.deps.Sessions == nil || u.ID == 0 {
		return
	}
	authKeyID, hasAuthKeyID := AuthKeyIDFrom(ctx)
	sessionID, hasSessionID := SessionIDFrom(ctx)
	if !hasAuthKeyID || !hasSessionID {
		return
	}
	notification := r.tgSignInServiceNotification(ctx, u, authKeyID)
	go func() {
		pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if scoped, ok := r.scopedSessions(); ok {
			if sent, err := scoped.PushToUserExceptAuthKeySession(pushCtx, u.ID, authKeyID, sessionID, proto.MessageFromServer, notification); err != nil {
				r.log.Debug("push sign-in service notification", zap.Int64("user_id", u.ID), zap.Int("sent", sent), zap.Error(err))
			}
			return
		}
		if sent, err := r.deps.Sessions.PushToUserExceptSession(pushCtx, u.ID, sessionID, proto.MessageFromServer, notification); err != nil {
			r.log.Debug("push sign-in service notification", zap.Int64("user_id", u.ID), zap.Int("sent", sent), zap.Error(err))
		}
	}()
}

func (r *Router) recordAndScheduleLoginMessagePush(ctx context.Context, msg domain.Message) {
	authKeyID, hasAuthKeyID := AuthKeyIDFrom(ctx)
	sessionID, hasSessionID := SessionIDFrom(ctx)
	if !hasAuthKeyID || !hasSessionID || msg.ID == 0 {
		return
	}
	event := domain.UpdateEvent{Type: domain.UpdateEventNewMessage, Pts: 1, PtsCount: 1, Date: msg.Date, Message: msg}
	state := domain.UpdateState{Pts: 1, Date: msg.Date, Seq: 0}
	if r.deps.Updates != nil {
		recorded, st, err := r.deps.Updates.RecordNewMessage(ctx, authKeyID, msg.OwnerUserID, msg)
		if err != nil {
			r.log.Warn("record login message update", zap.Error(err))
			return
		}
		event = recorded
		state = st
	}
	if r.deps.Sessions == nil {
		return
	}
	// 提前从请求 ctx 取出 rawAuthKeyID（值类型），闭包只捕获该值、不捕获请求 ctx——
	// 避免延迟推送的 AfterFunc 在 loginMessagePushDelay 期间延长请求 ctx 链路的存活。
	rawAuthKeyID, _ := RawAuthKeyIDFrom(ctx)
	time.AfterFunc(loginMessagePushDelay, func() {
		pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r.pushLoginMessage(pushCtx, rawAuthKeyID, sessionID, event, state)
	})
}

func (r *Router) pushLoginMessage(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64, event domain.UpdateEvent, state domain.UpdateState) {
	if r.deps.Sessions == nil || event.Message.ID == 0 {
		return
	}
	updates := tgLoginMessageUpdates(event, state)
	if updates == nil {
		return
	}
	var err error
	if scoped, ok := r.scopedSessions(); ok && rawAuthKeyID != ([8]byte{}) {
		err = scoped.PushToSessionForAuthKey(ctx, rawAuthKeyID, sessionID, proto.MessageFromServer, updates)
	} else {
		err = r.deps.Sessions.PushToSession(ctx, sessionID, proto.MessageFromServer, updates)
	}
	if err != nil {
		r.log.Debug("push login message", zap.Int64("session_id", sessionID), zap.Error(err))
		return
	}
	r.log.Debug("pushed login message",
		zap.Int64("session_id", sessionID),
		zap.Int("message_id", event.Message.ID),
		zap.Int("pts", event.Pts),
		zap.Int("seq", state.Seq),
	)
}

func tgLoginMessageUpdates(event domain.UpdateEvent, state domain.UpdateState) *tg.Updates {
	item := tgMessage(event.Message)
	if item == nil {
		return nil
	}
	if state.Date == 0 {
		state.Date = event.Date
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{
			&tg.UpdateNewMessage{
				Message:  item,
				Pts:      event.Pts,
				PtsCount: event.PtsCount,
			},
		},
		Users: []tg.UserClass{tgUser(domain.OfficialSystemUser())},
		Date:  state.Date,
		Seq:   state.Seq,
	}
}

func (r *Router) tgSignInServiceNotification(ctx context.Context, u domain.User, authKeyID [8]byte) *tg.Updates {
	now := r.clock.Now()
	client := "Unknown device"
	if ci, ok := ClientInfoFrom(ctx); ok {
		parts := []string{}
		if ci.DeviceModel != "" {
			parts = append(parts, ci.DeviceModel)
		}
		if ci.SystemVersion != "" {
			parts = append(parts, ci.SystemVersion)
		}
		if ci.AppVersion != "" {
			parts = append(parts, ci.AppVersion)
		}
		if len(parts) > 0 {
			client = strings.Join(parts, " / ")
		}
	}
	name := strings.TrimSpace(strings.TrimSpace(u.FirstName + " " + u.LastName))
	if name == "" {
		name = u.Phone
	}
	if name == "" {
		name = "there"
	}
	message := fmt.Sprintf("New login.\nDear %s, we detected a login into your account from a new device on %s.\n\nDevice: %s\nLocation: Unknown\n\nIf this wasn't you, you can terminate that session in Settings > Devices (or Privacy & Security > Active Sessions).",
		name,
		now.UTC().Format(time.RFC1123),
		client,
	)
	authID := int64(binary.LittleEndian.Uint64(authKeyID[:]))
	update := &tg.UpdateServiceNotification{
		InboxDate: int(now.Unix()),
		Type:      fmt.Sprintf("auth%d_%d", authID, now.Unix()),
		Message:   message,
		Media:     &tg.MessageMediaEmpty{},
		Entities:  signInNotificationEntities(message),
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(now.Unix()),
	}
}

func signInNotificationEntities(message string) []tg.MessageEntityClass {
	terms := []string{"New login.", "Settings > Devices", "Privacy & Security > Active Sessions"}
	out := make([]tg.MessageEntityClass, 0, len(terms))
	for _, term := range terms {
		if offset := strings.Index(message, term); offset >= 0 {
			out = append(out, &tg.MessageEntityBold{Offset: offset, Length: len(term)})
		}
	}
	return out
}

func authKeyIDFromInt64(v int64) [8]byte {
	var id [8]byte
	binary.LittleEndian.PutUint64(id[:], uint64(v))
	return id
}
