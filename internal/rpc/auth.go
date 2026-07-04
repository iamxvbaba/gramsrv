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

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

// devCodeLength 是开发固定验证码长度，写入 auth.sentCode 的 type.length。
const devCodeLength = 5

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
	d.OnAuthInitPasskeyLogin(r.onAuthInitPasskeyLogin)
	d.OnAuthFinishPasskeyLogin(r.onAuthFinishPasskeyLogin)
	d.OnAuthSendCode(r.onAuthSendCode)
	d.OnAuthResendCode(r.onAuthResendCode)
	d.OnAuthCancelCode(r.onAuthCancelCode)
	d.OnAuthSignIn(r.onAuthSignIn)
	d.OnAuthSignUp(r.onAuthSignUp)
	d.OnAuthImportBotAuthorization(r.onAuthImportBotAuthorization)
	d.OnAuthLogOut(r.onAuthLogOut)
	d.OnAuthResetAuthorizations(r.onAuthResetAuthorizations)
	d.OnAuthCheckPassword(r.onAuthCheckPassword)
	d.OnAuthRequestPasswordRecovery(r.onAuthRequestPasswordRecovery)
	d.OnAuthRecoverPassword(r.onAuthRecoverPassword)
	d.OnAuthCheckRecoveryPassword(r.onAuthCheckRecoveryPassword)
	d.OnAuthResetLoginEmail(r.onAuthResetLoginEmail)
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
	// temp key (re)bind 后立即作废其 temp→perm 解析缓存，确保下一帧按新绑定重新解析，
	// 不被 TTL 内的旧 perm 缓存命中（防跨账号串号）。
	if id != ([8]byte{}) {
		r.tempKeyResolveCache.Delete(id)
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

// onAuthExportLoginToken 给 QR 登录请求方返回短期 token；扫码端接受后，同一目标
// session 后续 export 会升级为 auth.loginTokenSuccess。
func (r *Router) onAuthExportLoginToken(ctx context.Context, req *tg.AuthExportLoginTokenRequest) (tg.AuthLoginTokenClass, error) {
	target, ok := loginTokenTargetFromContext(ctx)
	if !ok {
		return nil, internalErr()
	}
	authz := r.authzFromCtx(ctx)
	result, err := r.loginTokens.export(r.clock.Now(), target, authz, req.ExceptIDs)
	if err != nil {
		return nil, internalErr()
	}
	if result.accepted {
		return r.authLoginTokenSuccess(ctx, result.acceptedAuth)
	}
	return &tg.AuthLoginToken{Expires: int(result.expires.Unix()), Token: result.token}, nil
}

func (r *Router) onAuthImportLoginToken(ctx context.Context, token []byte) (tg.AuthLoginTokenClass, error) {
	result, err := r.loginTokens.lookup(r.clock.Now(), token)
	if err != nil {
		return nil, err
	}
	if result.accepted {
		return r.authLoginTokenSuccess(ctx, result.acceptedAuth)
	}
	return &tg.AuthLoginToken{Expires: int(result.expires.Unix()), Token: result.token}, nil
}

func (r *Router) onAuthAcceptLoginToken(ctx context.Context, token []byte) (*tg.Authorization, error) {
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !ok || userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	if r.deps.Auth == nil {
		return nil, internalErr()
	}
	now := r.clock.Now()
	accept, err := r.loginTokens.beginAccept(now, token, userID)
	if err != nil {
		return nil, err
	}
	scannerAuthKeyID, _ := AuthKeyIDFrom(ctx)
	if scannerAuthKeyID != ([8]byte{}) && scannerAuthKeyID == accept.authz.AuthKeyID {
		r.loginTokens.failAccept(token)
		return nil, authTokenExceptionErr()
	}
	authz := accept.authz
	authz.UserID = userID
	authz.PasswordPending = false
	if err := r.clearAuthKeyState(ctx, authz.AuthKeyID); err != nil {
		r.loginTokens.failAccept(token)
		return nil, internalErr()
	}
	bound, err := r.deps.Auth.AcceptLoginToken(ctx, authz, userID)
	if err != nil {
		r.loginTokens.failAccept(token)
		if errors.Is(err, auth.ErrSystemUserLoginForbidden) {
			return nil, authKeyUnregisteredErr()
		}
		return nil, internalErr()
	}
	if bound.UserID == 0 {
		bound.UserID = userID
	}
	r.loginTokens.finishAccept(now, token, userID, bound)
	r.invalidateAuthUserCache(bound.AuthKeyID)
	r.setAuthUserCache(bound.AuthKeyID, userID, true)
	r.bindLoginTokenTarget(accept.target, userID)
	r.pushLoginTokenAccepted(ctx, accept.target)
	out := tgAuthorization(bound, scannerAuthKeyID, int(now.Unix()))
	return &out, nil
}

func loginTokenTargetFromContext(ctx context.Context) (loginTokenTarget, bool) {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	rawAuthKeyID, _ := RawAuthKeyIDFrom(ctx)
	if rawAuthKeyID == ([8]byte{}) {
		rawAuthKeyID = authKeyID
	}
	sessionID, _ := SessionIDFrom(ctx)
	return loginTokenTarget{rawAuthKeyID: rawAuthKeyID, authKeyID: authKeyID, sessionID: sessionID}, true
}

func (r *Router) authLoginTokenSuccess(ctx context.Context, a domain.Authorization) (tg.AuthLoginTokenClass, error) {
	if r.deps.Users == nil || a.UserID == 0 {
		return nil, internalErr()
	}
	u, err := r.deps.Users.Self(ctx, a.UserID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AuthLoginTokenSuccess{
		Authorization: &tg.AuthAuthorization{User: r.tgSelfUser(u)},
	}, nil
}

func (r *Router) bindLoginTokenTarget(target loginTokenTarget, userID int64) {
	if r.deps.Sessions == nil || target.sessionID == 0 {
		return
	}
	if scoped, ok := r.scopedSessions(); ok && target.rawAuthKeyID != ([8]byte{}) {
		scoped.BindAuthKeyForSession(target.rawAuthKeyID, target.sessionID, target.authKeyID)
		scoped.BindUserForAuthKey(target.rawAuthKeyID, target.sessionID, userID)
		r.announceSessionOnline(loginTokenTargetContext(target, userID), userID)
		return
	}
	r.deps.Sessions.BindAuthKey(target.sessionID, target.authKeyID)
	r.deps.Sessions.BindUser(target.sessionID, userID)
	r.announceSessionOnline(loginTokenTargetContext(target, userID), userID)
}

func loginTokenTargetContext(target loginTokenTarget, userID int64) context.Context {
	ctx := context.Background()
	ctx = WithRawAuthKeyID(ctx, target.rawAuthKeyID)
	ctx = WithAuthKeyID(ctx, target.authKeyID)
	ctx = WithSessionID(ctx, target.sessionID)
	ctx = WithUserID(ctx, userID)
	return ctx
}

func (r *Router) pushLoginTokenAccepted(ctx context.Context, target loginTokenTarget) {
	if r.deps.Sessions == nil || target.sessionID == 0 {
		return
	}
	updates := &tg.UpdateShort{
		Update: &tg.UpdateLoginToken{},
		Date:   int(r.clock.Now().Unix()),
	}
	if immediate, ok := r.deps.Sessions.(ScopedImmediateSessionPusher); ok && target.rawAuthKeyID != ([8]byte{}) {
		if err := immediate.PushToSessionForAuthKeyImmediate(ctx, target.rawAuthKeyID, target.sessionID, proto.MessageFromServer, updates); err != nil {
			r.log.Debug("push login token accepted immediate", zap.Int64("session_id", target.sessionID), zap.Error(err))
		}
		return
	}
	if scoped, ok := r.scopedSessions(); ok && target.rawAuthKeyID != ([8]byte{}) {
		if err := scoped.PushToSessionForAuthKey(ctx, target.rawAuthKeyID, target.sessionID, proto.MessageFromServer, updates); err != nil {
			r.log.Debug("push login token accepted", zap.Int64("session_id", target.sessionID), zap.Error(err))
		}
		return
	}
	if err := r.deps.Sessions.PushToSession(ctx, target.sessionID, proto.MessageFromServer, updates); err != nil {
		r.log.Debug("push login token accepted", zap.Int64("session_id", target.sessionID), zap.Error(err))
	}
}

// onAuthSendCode 处理 auth.sendCode：生成 phone_code_hash 并返回 sentCode。
// 若该手机号账号设置了登录邮箱，验证码改投递到邮箱，返回 sentCodeTypeEmailCode
// （客户端据此进入"输入邮箱验证码"界面，随后用 auth.signIn 的 email_verification 完成登录）。
func (r *Router) onAuthSendCode(ctx context.Context, req *tg.AuthSendCodeRequest) (tg.AuthSentCodeClass, error) {
	hash, err := r.deps.Auth.SendCode(ctx, req.PhoneNumber)
	if err != nil {
		if errors.Is(err, auth.ErrPhoneNumberInvalid) ||
			errors.Is(err, auth.ErrSystemUserLoginForbidden) {
			return nil, phoneNumberInvalidErr()
		}
		return nil, internalErr()
	}
	if pattern, ok := r.loginEmailPattern(ctx, req.PhoneNumber); ok {
		return tgEmailSentCode(hash, pattern), nil
	}
	return tgSentCode(hash), nil
}

// loginEmailPattern 返回该手机号账号已确认登录邮箱的掩码，不存在则 ok=false。
func (r *Router) loginEmailPattern(ctx context.Context, phone string) (string, bool) {
	if r.deps.Account == nil {
		return "", false
	}
	email, found, err := r.deps.Account.LoginEmailByPhone(ctx, phone)
	if err != nil || !found || email == "" {
		return "", false
	}
	return domain.MaskEmail(email), true
}

func tgSentCode(hash string) tg.AuthSentCodeClass {
	return &tg.AuthSentCode{
		Type:          &tg.AuthSentCodeTypeApp{Length: devCodeLength},
		PhoneCodeHash: hash,
	}
}

func tgEmailSentCode(hash, emailPattern string) tg.AuthSentCodeClass {
	codeType := &tg.AuthSentCodeTypeEmailCode{
		EmailPattern: emailPattern,
		Length:       devCodeLength,
	}
	// reset_available_period=0 表示可立即调用 auth.resetLoginEmail（开发环境无等待期），
	// 让客户端的"无法访问邮箱?"逃生入口可用。
	codeType.SetResetAvailablePeriod(0)
	return &tg.AuthSentCode{
		Type:          codeType,
		PhoneCodeHash: hash,
	}
}

// onAuthSignIn 处理 auth.signIn：校验验证码；用户不存在时返回 SignUpRequired。
// 带 email_verification 时走登录邮箱路径（验证码来自邮箱而非短信）。
func (r *Router) onAuthSignIn(ctx context.Context, req *tg.AuthSignInRequest) (tg.AuthAuthorizationClass, error) {
	var (
		u            domain.User
		loginMessage domain.Message
		needSignUp   bool
		err          error
	)
	if verification, ok := req.GetEmailVerification(); ok {
		u, loginMessage, needSignUp, err = r.deps.Auth.SignInWithEmail(ctx, r.authzFromCtx(ctx), req.PhoneNumber, req.PhoneCodeHash, emailVerificationCode(verification))
	} else {
		u, loginMessage, needSignUp, err = r.deps.Auth.SignIn(ctx, r.authzFromCtx(ctx), req.PhoneNumber, req.PhoneCodeHash, req.PhoneCode)
	}
	if err != nil {
		if errors.Is(err, domain.ErrSessionPasswordNeeded) && u.ID != 0 {
			if err := r.clearAuthKeyStateOnUserChange(ctx, u.ID); err != nil {
				return nil, internalErr()
			}
			// 两步验证未完成：绝不能把 auth_key/session 标记为已登录，否则客户端忽略
			// SESSION_PASSWORD_NEEDED、直接调用业务 RPC 即可绕过 2FA。失效缓存并把 session
			// 置为未授权，让后续鉴权重新读到 password_pending 并拒绝；待 checkPassword 通过后再授权。
			if id, ok := AuthKeyIDFrom(ctx); ok {
				r.invalidateAuthUserCache(id)
			}
			r.bindSessionUser(ctx, 0)
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
	r.enqueueLoginMessageBootstrap(ctx, loginMessage)
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
		r.revokeAuthKeySessions(a.AuthKeyID)
		_ = r.clearAuthKeyState(ctx, a.AuthKeyID)
		// P1 修复：撤销其它会话同样销毁其 auth_key，级联 discard 该设备绑定的活跃密聊并通知对端。
		r.discardSecretChatsForAuthKey(ctx, businessAuthKeyInt64(a.AuthKeyID), userID)
	}
	return true, nil
}

func (r *Router) onAuthCheckPassword(ctx context.Context, password tg.InputCheckPasswordSRPClass) (tg.AuthAuthorizationClass, error) {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	userID, authorized, pending, err := r.currentOrPendingPasswordUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !pending && (!authorized || userID == 0) {
		return nil, passwordHashInvalidErr()
	}
	if r.deps.Account == nil {
		return nil, passwordHashInvalidErr()
	}
	if err := r.deps.Account.CheckPassword(ctx, userID, domainPasswordCheck(password)); err != nil {
		return nil, passwordErr(err)
	}
	// 两步验证通过：清除 password_pending 并把 auth_key/session 提升为完全授权。
	if pending {
		if err := r.completePendingPasswordSignIn(ctx, authKeyID, userID); err != nil {
			return nil, internalErr()
		}
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AuthAuthorization{User: r.tgSelfUser(u)}, nil
}

func (r *Router) onAuthRequestPasswordRecovery(ctx context.Context) (*tg.AuthPasswordRecovery, error) {
	userID, _, _, err := r.currentOrPendingPasswordUserID(ctx)
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
	authKeyID, _ := AuthKeyIDFrom(ctx)
	userID, _, pending, err := r.currentOrPendingPasswordUserID(ctx)
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
	if pending {
		if err := r.completePendingPasswordSignIn(ctx, authKeyID, userID); err != nil {
			return nil, internalErr()
		}
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AuthAuthorization{User: r.tgSelfUser(u)}, nil
}

func (r *Router) onAuthCheckRecoveryPassword(ctx context.Context, code string) (bool, error) {
	userID, _, _, err := r.currentOrPendingPasswordUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if err := r.deps.Account.CheckRecoveryPassword(ctx, userID, code); err != nil {
		return false, passwordErr(err)
	}
	return true, nil
}

// currentOrPendingPasswordUserID returns the fully authorized user when present;
// otherwise it allows the narrow 2FA login continuation path to locate the user
// attached to a password_pending auth key. The pending identity must not be used
// by general business RPCs.
func (r *Router) currentOrPendingPasswordUserID(ctx context.Context) (userID int64, authorized bool, passwordPending bool, err error) {
	userID, authorized, err = r.currentUserID(ctx)
	if err != nil || authorized {
		return userID, authorized, false, err
	}
	if r.deps.Auth == nil {
		return userID, authorized, false, nil
	}
	authKeyID, ok := AuthKeyIDFrom(ctx)
	if !ok {
		return userID, authorized, false, nil
	}
	pendingUserID, pending, err := r.deps.Auth.PendingPasswordUserID(ctx, authKeyID)
	if err != nil {
		return 0, false, false, err
	}
	if !pending || pendingUserID == 0 {
		return userID, authorized, false, nil
	}
	return pendingUserID, false, true, nil
}

func (r *Router) completePendingPasswordSignIn(ctx context.Context, authKeyID [8]byte, userID int64) error {
	if r.deps.Auth == nil {
		return nil
	}
	if err := r.deps.Auth.CompletePasswordSignIn(ctx, authKeyID); err != nil {
		return err
	}
	r.invalidateAuthUserCache(authKeyID)
	r.setAuthUserCache(authKeyID, userID, true)
	r.bindSessionUser(ctx, userID)
	return nil
}

// onAuthResetLoginEmail 处理 auth.resetLoginEmail：用户登录设备时无法访问登录邮箱时
// 清除登录邮箱，改回手机验证码登录，返回一个新的手机 sentCode 供其继续。
func (r *Router) onAuthResetLoginEmail(ctx context.Context, req *tg.AuthResetLoginEmailRequest) (tg.AuthSentCodeClass, error) {
	if r.deps.Account == nil || r.deps.Auth == nil {
		return nil, internalErr()
	}
	if err := r.deps.Account.ClearLoginEmailByPhone(ctx, req.PhoneNumber); err != nil {
		return nil, internalErr()
	}
	hash, err := r.deps.Auth.SendCode(ctx, req.PhoneNumber)
	if err != nil {
		if errors.Is(err, auth.ErrPhoneNumberInvalid) ||
			errors.Is(err, auth.ErrSystemUserLoginForbidden) {
			return nil, phoneNumberInvalidErr()
		}
		return nil, internalErr()
	}
	return tgSentCode(hash), nil
}

// emailVerificationCode 从 emailVerification 取出可校验的字符串（验证码 / Google·Apple
// 令牌）。开发环境一律按"任意非空即通过"处理，故三者等价取值。
// onAuthInitPasskeyLogin 处理 auth.initPasskeyLogin：生成一次性断言挑战（discoverable），
// 以 DataJSON（顶层含 publicKey）返回。免授权（登录前）。
func (r *Router) onAuthInitPasskeyLogin(ctx context.Context, req *tg.AuthInitPasskeyLoginRequest) (*tg.AuthPasskeyLoginOptions, error) {
	if r.deps.Passkey == nil {
		return &tg.AuthPasskeyLoginOptions{Options: tg.DataJSON{Data: "{}"}}, nil
	}
	options, err := r.deps.Passkey.InitLogin(ctx)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AuthPasskeyLoginOptions{Options: tg.DataJSON{Data: string(options)}}, nil
}

// onAuthFinishPasskeyLogin 处理 auth.finishPasskeyLogin：验证登录断言并绑定 auth_key。
// 收尾与 signIn 同构（清水位→授权缓存→session 绑定）；passkey 是强因子，直接完全授权
// （不走 SESSION_PASSWORD_NEEDED）。FromDCID/FromAuthKeyID 为多 DC 重路由用，本单 DC 忽略。
func (r *Router) onAuthFinishPasskeyLogin(ctx context.Context, req *tg.AuthFinishPasskeyLoginRequest) (tg.AuthAuthorizationClass, error) {
	if r.deps.Passkey == nil || r.deps.Auth == nil {
		return nil, internalErr()
	}
	credID, login, ok := passkeyLoginFromCredential(req.Credential)
	if !ok {
		return nil, passkeyErr(domain.ErrPasskeyInvalid)
	}
	userID, err := r.deps.Passkey.FinishLogin(ctx, credID, []byte(login.ClientData.Data), login.AuthenticatorData, login.Signature, login.UserHandle)
	if err != nil {
		return nil, passkeyErr(err)
	}
	u, err := r.deps.Auth.BindVerifiedLogin(ctx, r.authzFromCtx(ctx), userID)
	if err != nil {
		return nil, passkeyErr(err)
	}
	if err := r.clearAuthKeyStateOnUserChange(ctx, u.ID); err != nil {
		return nil, internalErr()
	}
	if id, ok := AuthKeyIDFrom(ctx); ok {
		r.setAuthUserCache(id, u.ID, true)
	}
	r.bindSessionUser(ctx, u.ID)
	return &tg.AuthAuthorization{User: r.tgSelfUser(u)}, nil
}

func emailVerificationCode(v tg.EmailVerificationClass) string {
	switch e := v.(type) {
	case *tg.EmailVerificationCode:
		return e.Code
	case *tg.EmailVerificationGoogle:
		return e.Token
	case *tg.EmailVerificationApple:
		return e.Token
	}
	return ""
}

// onAuthImportBotAuthorization 处理 auth.importBotAuthorization：bot 程序凭 token
// 登录为 bot 账号。api_id/api_hash 与现有 sendCode 行为一致不校验（无 app 注册表）。
// 收尾与 signIn 同构（清水位→授权缓存→session 绑定），但不写登录消息、不推
// signIn 服务通知——那是手机登录语义。
func (r *Router) onAuthImportBotAuthorization(ctx context.Context, req *tg.AuthImportBotAuthorizationRequest) (tg.AuthAuthorizationClass, error) {
	if r.deps.Auth == nil {
		return nil, accessTokenInvalidErr()
	}
	u, err := r.deps.Auth.SignInBot(ctx, r.authzFromCtx(ctx), req.BotAuthToken)
	if err != nil {
		return nil, importBotAuthorizationErr(err)
	}
	if err := r.clearAuthKeyStateOnUserChange(ctx, u.ID); err != nil {
		return nil, internalErr()
	}
	if id, ok := AuthKeyIDFrom(ctx); ok {
		r.setAuthUserCache(id, u.ID, true)
	}
	r.bindSessionUser(ctx, u.ID)
	return &tg.AuthAuthorization{User: r.tgSelfUser(u)}, nil
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
	r.enqueueLoginMessageBootstrap(ctx, loginMessage)
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
	// bot 登出不广播 offline（bot 无 presence 语义，与登录路径对称）。
	if userErr == nil && authorized && userID != 0 && !r.userIsBot(ctx, userID) {
		status, _ := r.setPresenceFromContext(ctx, userID, true, presencePersistSync)
		r.pushUserStatus(ctx, userID, status)
		// 登出后主动清掉本 session 的 presence 条目：连接通常不断开（客户端回登录页），
		// 上面 unbindAuthKey 已把连接 userID 清 0，TCP 真正断开时 SessionOffline 因 userID=0
		// 提前返回、不再清 presence，条目会以 offline 态滞留泄露。这里随登出一并清除。
		if key, ok := presenceSessionKeyFromContext(ctx); ok {
			r.presence.clearSession(key)
		}
	}
	// P1 修复：登出销毁本设备 perm auth_key 后，级联 discard 其绑定的活跃密聊并通知对端
	//（否则对端继续往死 auth_key 投递成静默死链）。best-effort，不阻断登出。
	if userErr == nil && userID != 0 {
		r.discardSecretChatsForAuthKey(ctx, businessAuthKeyInt64(id), userID)
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

// revokeAuthKeySessions 是授权撤销（被踢设备）的完整失效闭环：清 Router 授权缓存、
// 清 temp→perm 短缓存、强制断开在线连接、再兜底解绑。断开不可省略——出站推送用
// 连接持有的密钥加密、不回查授权表，perm-key 连接的授权缓存也只有重连才会重新回查；
// 不断开的话被踢设备仍能持续收到推送并以缓存身份继续发请求。重连后回查 store 即得
// 未授权（401）。
//
// 顺序关键：先 CloseSessionsForBusinessAuthKey 再 unbindAuthKey。Close 内部 removeLocked
// 读取连接当前 userID 生成 SessionOffline 事件（驱动 presence 清理与 offline 广播）；
// 若先 unbind 把 userID 清成 0，事件就退化为 userID=0，被踢设备的 presence 条目不被
// 清理、好友侧最长一个在线 TTL 仍显示其在线。Close 已把连接移出索引，随后的 unbind
// 对未实现 SessionTerminator 的 Sessions 才有意义（生产实现走 Close 即可，unbind 是 no-op）。
func (r *Router) revokeAuthKeySessions(authKeyID [8]byte) {
	r.invalidateAuthUserCache(authKeyID)
	rawTempAuthKeyIDs := r.invalidateTempAuthKeyCacheForPerm(authKeyID)
	if terminator, ok := r.deps.Sessions.(SessionTerminator); ok {
		terminator.CloseSessionsForBusinessAuthKey(authKeyID)
	}
	if terminator, ok := r.deps.Sessions.(RawSessionTerminator); ok {
		for _, rawAuthKeyID := range rawTempAuthKeyIDs {
			if rawAuthKeyID == authKeyID {
				continue
			}
			terminator.CloseSessionsForRawAuthKeyExcept(rawAuthKeyID, 0)
		}
	}
	r.unbindAuthKey(authKeyID)
}

func (r *Router) invalidateTempAuthKeyCacheForPerm(authKeyID [8]byte) [][8]byte {
	return r.tempKeyResolveCache.DeleteByPerm(authKeyID)
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
