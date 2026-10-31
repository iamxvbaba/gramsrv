package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/app/auth"
	"telesrv/internal/branding"
	"telesrv/internal/domain"
)

// devCodeLength is the fixed development verification-code length, written into auth.sentCode's type.length.
const devCodeLength = 5

// registerAuth registers the auth.* RPC handlers.
func (r *Router) registerAuth(d *tlprofile.Dispatcher) {
	registerRPC[*tg.AuthBindTempAuthKeyRequest](d, tlprofile.SemanticMethodAuthBindTempAuthKey, func(ctx context.Context, layerRequest *tg.AuthBindTempAuthKeyRequest) (any, error) {
		return r.onAuthBindTempAuthKey(ctx, layerRequest)
	})
	registerRPC[*tg.AuthExportLoginTokenRequest](d, tlprofile.SemanticMethodAuthExportLoginToken, func(ctx context.Context, layerRequest *tg.AuthExportLoginTokenRequest) (any, error) {
		return r.onAuthExportLoginToken(ctx, layerRequest)
	})
	registerRPC[*tg.AuthImportLoginTokenRequest](d, tlprofile.SemanticMethodAuthImportLoginToken, func(ctx context.Context, layerRequest *tg.AuthImportLoginTokenRequest) (any, error) {
		return r.onAuthImportLoginToken(ctx, layerRequest.
			Token)
	})
	registerRPC[*tg.AuthAcceptLoginTokenRequest](d, tlprofile.SemanticMethodAuthAcceptLoginToken, func(ctx context.Context, layerRequest *tg.AuthAcceptLoginTokenRequest) (any, error) {
		return r.onAuthAcceptLoginToken(ctx, layerRequest.
			Token)
	})
	registerRPC[*tg.AuthExportAuthorizationRequest](d, tlprofile.SemanticMethodAuthExportAuthorization, func(ctx context.Context, layerRequest *tg.AuthExportAuthorizationRequest) (any, error) {
		dcid := layerRequest.
			DCID
		_ = dcid

		return nil, dcIDInvalidErr()
	})
	registerRPC[*tg.AuthImportAuthorizationRequest](d, tlprofile.SemanticMethodAuthImportAuthorization, func(ctx context.Context, req *tg.AuthImportAuthorizationRequest) (any, error) {
		return nil, dcIDInvalidErr()
	})
	registerRPC[*tg.AuthDropTempAuthKeysRequest](d, tlprofile.SemanticMethodAuthDropTempAuthKeys, func(ctx context.Context, layerRequest *tg.AuthDropTempAuthKeysRequest) (any, error) {
		exceptauthkeys := layerRequest.
			ExceptAuthKeys
		_ = exceptauthkeys

		return true, nil
	})
	registerRPC[*tg.AuthInitPasskeyLoginRequest](d, tlprofile.SemanticMethodAuthInitPasskeyLogin, func(ctx context.Context, layerRequest *tg.AuthInitPasskeyLoginRequest) (any, error) {
		return r.onAuthInitPasskeyLogin(ctx, layerRequest)
	})
	registerRPC[*tg.AuthFinishPasskeyLoginRequest](d, tlprofile.SemanticMethodAuthFinishPasskeyLogin, func(ctx context.Context, layerRequest *tg.AuthFinishPasskeyLoginRequest) (any, error) {
		return r.onAuthFinishPasskeyLogin(ctx, layerRequest)
	})
	registerRPC[*tg.AuthSendCodeRequest](d, tlprofile.SemanticMethodAuthSendCode, func(ctx context.Context, layerRequest *tg.AuthSendCodeRequest) (any, error) {
		return r.onAuthSendCode(ctx, layerRequest)
	})
	registerRPC[*tg.AuthReportMissingCodeRequest](d, tlprofile.SemanticMethodAuthReportMissingCode, func(ctx context.Context, req *tg.AuthReportMissingCodeRequest) (any, error) {
		return r.onAuthReportMissingCode(ctx, req)
	})
	registerRPC[*tg.AuthResendCodeRequest](d, tlprofile.SemanticMethodAuthResendCode, func(ctx context.Context, layerRequest *tg.AuthResendCodeRequest) (any, error) {
		return r.onAuthResendCode(ctx, layerRequest)
	})
	registerRPC[*tg.AuthCancelCodeRequest](d, tlprofile.SemanticMethodAuthCancelCode, func(ctx context.Context, layerRequest *tg.AuthCancelCodeRequest) (any, error) {
		return r.onAuthCancelCode(ctx, layerRequest)
	})
	registerRPC[*tg.AuthSignInRequest](d, tlprofile.SemanticMethodAuthSignIn, func(ctx context.Context, layerRequest *tg.AuthSignInRequest) (any, error) {
		return r.onAuthSignIn(ctx, layerRequest)
	})
	registerRPC[*tg.AuthSignUpRequest](d, tlprofile.SemanticMethodAuthSignUp, func(ctx context.Context, layerRequest *tg.AuthSignUpRequest) (any, error) {
		return r.onAuthSignUp(ctx, layerRequest)
	})
	registerRPC[*tg.AuthImportBotAuthorizationRequest](d, tlprofile.SemanticMethodAuthImportBotAuthorization, func(ctx context.Context, layerRequest *tg.AuthImportBotAuthorizationRequest) (any, error) {
		return r.onAuthImportBotAuthorization(ctx, layerRequest)
	})
	registerRPC[*tg.AuthLogOutRequest](d, tlprofile.SemanticMethodAuthLogOut, func(ctx context.Context, layerRequest *tg.AuthLogOutRequest) (any, error) {
		return r.onAuthLogOut(ctx)
	})
	registerRPC[*tg.AuthResetAuthorizationsRequest](d, tlprofile.SemanticMethodAuthResetAuthorizations, func(ctx context.Context, layerRequest *tg.AuthResetAuthorizationsRequest) (any, error) {
		return r.onAuthResetAuthorizations(ctx)
	})
	registerRPC[*tg.AuthCheckPasswordRequest](d, tlprofile.SemanticMethodAuthCheckPassword, func(ctx context.Context, layerRequest *tg.AuthCheckPasswordRequest) (any, error) {
		return r.onAuthCheckPassword(ctx, layerRequest.
			Password)
	})
	registerRPC[*tg.AuthRequestPasswordRecoveryRequest](d, tlprofile.SemanticMethodAuthRequestPasswordRecovery, func(ctx context.Context, layerRequest *tg.AuthRequestPasswordRecoveryRequest) (any, error) {
		return r.onAuthRequestPasswordRecovery(ctx)
	})
	registerRPC[*tg.AuthRecoverPasswordRequest](d, tlprofile.SemanticMethodAuthRecoverPassword, func(ctx context.Context, layerRequest *tg.AuthRecoverPasswordRequest) (any, error) {
		return r.onAuthRecoverPassword(ctx, layerRequest)
	})
	registerRPC[*tg.AuthCheckRecoveryPasswordRequest](d, tlprofile.SemanticMethodAuthCheckRecoveryPassword, func(ctx context.Context, layerRequest *tg.AuthCheckRecoveryPasswordRequest) (

		// onAuthBindTempAuthKey records TDesktop's PFS temp->perm auth key binding.
		any, error) {
		return r.onAuthCheckRecoveryPassword(ctx, layerRequest.
			Code)
	})
	registerRPC[*tg.AuthResetLoginEmailRequest](d, tlprofile.SemanticMethodAuthResetLoginEmail, func(ctx context.Context, layerRequest *tg.AuthResetLoginEmailRequest) (any, error) {
		return r.onAuthResetLoginEmail(ctx, layerRequest)
	})
}

func (r *Router) onAuthBindTempAuthKey(ctx context.Context, req *tg.AuthBindTempAuthKeyRequest) (bool, error) {
	if !layerRPCProfileEvidenceFresh(ctx) {
		// The inner request is outside MTProto's mutable msg_id window. It may be
		// decoded request-locally, but acknowledging it would mutate the durable
		// temp→perm identity and every live session from stale replay evidence.
		return false, bindTempAuthKeyErr(auth.ErrTempAuthKeyEmpty)
	}
	if r.deps.Auth == nil {
		return true, nil
	}
	id, _ := RawAuthKeyIDFrom(ctx)
	if id == ([8]byte{}) {
		id, _ = AuthKeyIDFrom(ctx)
	}
	sessionID, _ := SessionIDFrom(ctx)
	boundState, err := r.deps.Auth.BindTempAuthKey(ctx, sessionID, domain.TempAuthKeyBinding{
		TempAuthKeyID:    id,
		PermAuthKeyID:    req.PermAuthKeyID,
		Nonce:            req.Nonce,
		ExpiresAt:        req.ExpiresAt,
		EncryptedMessage: append([]byte(nil), req.EncryptedMessage...),
	})
	if err != nil {
		return false, bindTempAuthKeyErr(err)
	}
	permID := authKeyIDFromInt64(req.PermAuthKeyID)
	// The committed bind transaction is authoritative for this immutable
	// temp→permanent identity. Replace any prior local entry, then publish the
	// exact positive mapping so Layer publication and the first business RPC do
	// not re-read the same row. A competing different-permanent bind has already
	// failed in the store before reaching this point.
	if id != ([8]byte{}) {
		r.tempKeyResolveCache.Delete(id)
		r.cacheResolvedAuthKey(id, permID)
	}
	// Save atomically merged raw/permanent Layer observations and returned the
	// exact committed tuple. Project that generation directly; a post-commit
	// read could observe a later selector and wrongly attribute it to this bind.
	r.invalidateAuthUserCache(id)
	r.invalidateAuthUserCache(permID)
	unlockLayerCommit := r.lockAuthLayerCommit(id, permID)
	defer unlockLayerCommit()
	layer, blocked, err := r.cacheBoundAuthKeyLayerResolution(id, permID, boundState)
	if err != nil {
		if r.log != nil {
			r.log.Error("project committed temp auth key bind Layer failed",
				zap.String("raw_auth_key_id", fmt.Sprintf("%x", id[:])),
				zap.String("perm_auth_key_id", fmt.Sprintf("%x", permID[:])),
				zap.Error(err))
		}
		return false, internalErr()
	}
	if r.deps.Sessions != nil {
		if all, ok := r.deps.Sessions.(RawAuthKeySessionBinder); ok {
			all.BindAuthKeyForRawAuthKey(id, permID)
		} else {
			r.deps.Sessions.BindAuthKeyForSession(id, sessionID, permID)
		}
	}
	if isSupportedLayer(layer) {
		if refresher, ok := r.deps.Sessions.(AuthKeyLayerRefresher); ok {
			refresher.RefreshInheritedLayerForRawAuthKey(id, layer)
		} else if binder, ok := r.deps.Sessions.(AuthKeyLayerBinder); ok {
			binder.SeedInheritedLayerForRawAuthKey(id, layer)
		}
	} else if blocked || layer == 0 {
		if clearer, ok := r.deps.Sessions.(AuthKeyInheritedLayerClearer); ok {
			clearer.ClearInheritedLayerForRawAuthKey(id)
		}
	}
	return true, nil
}

func (r *Router) cacheBoundAuthKeyLayerResolution(
	rawAuthKeyID, permAuthKeyID [8]byte,
	result domain.TempAuthKeyBindingResult,
) (layer int, blocked bool, err error) {
	if result.Layer < 0 || result.LayerObservationID < 0 ||
		(result.LayerObservationID > 0 && result.Layer == 0) {
		return 0, false, fmt.Errorf(
			"invalid bound auth-key Layer result layer=%d observation=%d",
			result.Layer, result.LayerObservationID,
		)
	}
	outcome := clientSessionInfo{layerObservationID: result.LayerObservationID}
	if isSupportedLayer(result.Layer) {
		outcome.layer = result.Layer
	} else if result.Layer != 0 {
		outcome.layerBlocked = true
		outcome.layerBlockedByAuthKey = true
	}

	r.clientInfoMu.Lock()
	defer r.clientInfoMu.Unlock()
	for _, authKeyID := range [][8]byte{rawAuthKeyID, permAuthKeyID} {
		current := r.authInfo[authKeyID]
		switch {
		case current.layerObservationID > outcome.layerObservationID:
			outcome.layer = current.layer
			outcome.layerObservationID = current.layerObservationID
			outcome.layerBlocked = current.layerBlocked
			outcome.layerBlockedByAuthKey = current.layerBlockedByAuthKey
		case current.layerObservationID == outcome.layerObservationID && outcome.layerObservationID > 0:
			currentBlocked := current.layerBlocked || current.layerBlockedByAuthKey
			outcomeBlocked := outcome.layerBlocked || outcome.layerBlockedByAuthKey
			if current.layer != 0 && outcome.layer != 0 && current.layer != outcome.layer {
				return 0, false, fmt.Errorf(
					"conflicting cached bound auth-key Layer observation %d: %d != %d",
					outcome.layerObservationID, current.layer, outcome.layer,
				)
			}
			if currentBlocked != outcomeBlocked &&
				(current.layer != 0 || outcome.layer != 0 || currentBlocked || outcomeBlocked) {
				return 0, false, fmt.Errorf(
					"conflicting cached bound auth-key blocked observation %d",
					outcome.layerObservationID,
				)
			}
			if outcome.layer == 0 {
				outcome.layer = current.layer
			}
		}
	}
	if r.authInfo == nil {
		r.authInfo = make(map[[8]byte]clientSessionInfo)
	}
	for _, authKeyID := range [][8]byte{rawAuthKeyID, permAuthKeyID} {
		if _, exists := r.authInfo[authKeyID]; !exists {
			evictMapEntryIfFullLocked(r.authInfo, maxAuthInfoEntries)
		}
		info := r.authInfo[authKeyID]
		info.layer = outcome.layer
		info.layerObservationID = outcome.layerObservationID
		info.layerAdmissionSeq = 0
		info.layerBlocked = outcome.layerBlocked
		info.layerBlockedByAuthKey = outcome.layerBlockedByAuthKey
		r.authInfo[authKeyID] = info
	}
	return outcome.layer, outcome.layerBlocked || outcome.layerBlockedByAuthKey, nil
}

// onAuthExportLoginToken returns a short-lived token to the QR login requester; once the scanning
// side accepts it, subsequent exports for the same target session upgrade to auth.loginTokenSuccess.
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
	// The acceptLoginToken of QR login also immediately returns an authorization for the client to
	// display, and it goes through the same geo-attribution resolution as account.getAuthorizations;
	// there is only this one record, so a single-element batch suffices.
	boundLocations := r.resolveAuthorizationLocations(ctx, []domain.Authorization{bound})
	out := r.tgAuthorization(boundLocations[bound.IP], bound, scannerAuthKeyID, int(now.Unix()))
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
		Authorization: &tg.AuthAuthorization{User: r.tgSelfUserWithUsernames(ctx, u)},
	}, nil
}

func (r *Router) bindLoginTokenTarget(target loginTokenTarget, userID int64) {
	if r.deps.Sessions == nil || target.sessionID == 0 {
		return
	}
	r.deps.Sessions.BindAuthKeyForSession(target.rawAuthKeyID, target.sessionID, target.authKeyID)
	r.deps.Sessions.BindUserForAuthKey(target.rawAuthKeyID, target.sessionID, userID)
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
	if immediate, ok := r.deps.Sessions.(ImmediateSessionPusher); ok {
		if err := immediate.PushToSessionForAuthKeyImmediate(ctx, target.rawAuthKeyID, target.sessionID, proto.MessageFromServer, updates); err != nil {
			r.log.Debug("push login token accepted immediate", zap.Int64("session_id", target.sessionID), zap.Error(err))
		}
		return
	}
	if err := r.deps.Sessions.PushToSessionForAuthKey(ctx, target.rawAuthKeyID, target.sessionID, proto.MessageFromServer, updates); err != nil {
		r.log.Debug("push login token accepted", zap.Int64("session_id", target.sessionID), zap.Error(err))
	}
}

// onAuthSendCode handles auth.sendCode: it generates phone_code_hash and returns sentCode. If the
// account with this phone number has a login email set, the code is delivered to the email instead
// and sentCodeTypeEmailCode is returned (the client then enters the "enter the email code" screen
// and later completes login via auth.signIn's email_verification).
func (r *Router) onAuthSendCode(ctx context.Context, req *tg.AuthSendCodeRequest) (tg.AuthSentCodeClass, error) {
	if err := r.checkAuthCodeRateLimit(ctx, req.PhoneNumber); err != nil {
		return nil, err
	}
	r.rememberClientAPIID(ctx, req.APIID)
	hash, err := r.deps.Auth.SendCode(ctx, req.PhoneNumber)
	if err != nil {
		if errors.Is(err, auth.ErrPhoneNumberInvalid) ||
			errors.Is(err, auth.ErrSystemUserLoginForbidden) {
			return nil, phoneNumberInvalidErr()
		}
		// The public MTProto error intentionally stays opaque, but operators need
		// the wrapped store/provider cause to repair an update-related failure.
		// Hash the normalized phone so neither the number nor the OTP reaches logs.
		phoneDigest := sha256.Sum256([]byte(domain.NormalizePhone(req.PhoneNumber)))
		fields := append(r.contextLogFields(ctx),
			zap.Int("api_id", req.APIID),
			zap.String("phone_digest", hex.EncodeToString(phoneDigest[:8])),
			zap.Error(err),
		)
		r.log.Error("auth.sendCode failed", fields...)
		return nil, internalErr()
	}
	sent, err := r.tgSentCodeForHash(ctx, hash)
	if err != nil {
		fields := append(r.contextLogFields(ctx), zap.Error(err))
		r.log.Error("auth.sendCode delivery lookup failed", fields...)
		return nil, err
	}
	return sent, nil
}

func (r *Router) onAuthReportMissingCode(ctx context.Context, req *tg.AuthReportMissingCodeRequest) (bool, error) {
	if req == nil || r.deps.AuthDeliveryReports == nil {
		return false, inputRequestInvalidErr()
	}
	authKeyID, authKeyOK := AuthKeyIDFrom(ctx)
	sessionID, sessionOK := SessionIDFrom(ctx)
	if !authKeyOK || authKeyID == ([8]byte{}) || !sessionOK || sessionID == 0 {
		return false, internalErr()
	}
	clientType := string(ClientTypeFrom(ctx))
	if _, _, err := r.deps.AuthDeliveryReports.ReportMissingCode(ctx, domain.AuthMissingCodeReportRequest{
		AuthKeyID: authKeyID, SessionID: sessionID, ClientType: clientType,
		Phone: req.PhoneNumber, PhoneCodeHash: req.PhoneCodeHash,
		MNC: req.Mnc, CreatedAt: r.clock.Now(),
	}); err != nil {
		switch {
		case errors.Is(err, domain.ErrPhoneCodeExpired):
			return false, phoneCodeExpiredErr()
		case errors.Is(err, domain.ErrPhoneCodeInvalid),
			errors.Is(err, domain.ErrAuthDeliveryReportInvalid):
			return false, phoneCodeInvalidErr()
		case errors.Is(err, domain.ErrAuthDeliveryRateLimited):
			return false, floodWaitErr(60)
		default:
			return false, internalErr()
		}
	}
	return true, nil
}

func tgSentCode(hash string) tg.AuthSentCodeClass {
	return tgSentCodeWithLength(hash, devCodeLength)
}

func tgSentCodeWithLength(hash string, length int) tg.AuthSentCodeClass {
	if length <= 0 {
		length = devCodeLength
	}
	return &tg.AuthSentCode{
		Type:          &tg.AuthSentCodeTypeApp{Length: length},
		PhoneCodeHash: hash,
	}
}

func tgSMSSentCode(hash string, length int) tg.AuthSentCodeClass {
	if length <= 0 {
		length = devCodeLength
	}
	return &tg.AuthSentCode{
		Type:          &tg.AuthSentCodeTypeSMS{Length: length},
		PhoneCodeHash: hash,
	}
}

func tgEmailSentCode(hash, emailPattern string, length int, resetAvailable bool) tg.AuthSentCodeClass {
	if length <= 0 {
		length = devCodeLength
	}
	codeType := &tg.AuthSentCodeTypeEmailCode{
		EmailPattern: emailPattern,
		Length:       length,
	}
	if resetAvailable {
		// 0 means the SMS fallback is available immediately. Absence means this
		// deployment cannot safely service auth.resetLoginEmail.
		codeType.SetResetAvailablePeriod(0)
	}
	return &tg.AuthSentCode{
		Type:          codeType,
		PhoneCodeHash: hash,
	}
}

type loginEmailResetAvailabilityChecker interface {
	LoginEmailResetAvailable() bool
}

func (r *Router) loginEmailResetAvailable() bool {
	checker, ok := r.deps.Auth.(loginEmailResetAvailabilityChecker)
	return ok && checker.LoginEmailResetAvailable()
}

func tgEmailSetupRequiredSentCode(hash string) tg.AuthSentCodeClass {
	return &tg.AuthSentCode{
		Type:          &tg.AuthSentCodeTypeSetUpEmailRequired{},
		PhoneCodeHash: hash,
	}
}

func (r *Router) tgSentCodeForHash(ctx context.Context, hash string) (tg.AuthSentCodeClass, error) {
	if r.deps.Auth == nil {
		return tgSentCode(hash), nil
	}
	delivery, found, err := r.deps.Auth.CodeDelivery(ctx, hash)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, signInErr(auth.ErrCodeExpired)
	}
	switch delivery.Kind {
	case domain.AuthCodeDeliverySMS:
		return tgSMSSentCode(hash, delivery.Length), nil
	case domain.AuthCodeDeliveryEmail:
		return tgEmailSentCode(hash, delivery.EmailPattern, delivery.Length, r.loginEmailResetAvailable()), nil
	case domain.AuthCodeDeliveryEmailSetupRequired:
		return tgEmailSetupRequiredSentCode(hash), nil
	default:
		return tgSentCodeWithLength(hash, delivery.Length), nil
	}
}

// onAuthSignIn handles auth.signIn: validates the verification code; returns SignUpRequired when
// the user does not exist. With email_verification it takes the login-email path (the code comes
// from the email rather than SMS).
func (r *Router) onAuthSignIn(ctx context.Context, req *tg.AuthSignInRequest) (tg.AuthAuthorizationClass, error) {
	var (
		u          domain.User
		needSignUp bool
		err        error
	)
	if verification, ok := req.GetEmailVerification(); ok {
		u, _, needSignUp, err = r.deps.Auth.SignInWithEmail(ctx, r.authzFromCtx(ctx), req.PhoneNumber, req.PhoneCodeHash, emailVerificationCode(verification))
	} else {
		u, _, needSignUp, err = r.deps.Auth.SignIn(ctx, r.authzFromCtx(ctx), req.PhoneNumber, req.PhoneCodeHash, req.PhoneCode)
	}
	return r.finishAuthSignIn(ctx, u, needSignUp, err)
}

func (r *Router) finishAuthSignIn(ctx context.Context, u domain.User, needSignUp bool, err error) (tg.AuthAuthorizationClass, error) {
	if err != nil {
		if errors.Is(err, domain.ErrSessionPasswordNeeded) && u.ID != 0 {
			// Two-step verification incomplete: never mark auth_key/session as logged in, otherwise
			// the client ignores SESSION_PASSWORD_NEEDED and can bypass 2FA by calling business RPCs
			// directly. Invalidate the cache and mark the session unauthorized, so subsequent auth
			// re-reads password_pending and rejects; authorize again after checkPassword passes.
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
	if id, ok := AuthKeyIDFrom(ctx); ok {
		r.setAuthUserCache(id, u.ID, true)
	}
	r.bindSessionUser(ctx, u.ID)
	r.pushSignInServiceNotificationToOthers(ctx, u)
	return &tg.AuthAuthorization{User: r.tgSelfUserWithUsernames(ctx, u)}, nil
}

func (r *Router) onAuthResendCode(ctx context.Context, req *tg.AuthResendCodeRequest) (tg.AuthSentCodeClass, error) {
	if err := r.checkAuthCodeRateLimit(ctx, req.PhoneNumber); err != nil {
		return nil, err
	}
	if userID, authorized, err := r.currentUserID(ctx); err == nil && authorized && userID != 0 {
		if svc, ok := r.deps.Account.(accountDeletionService); ok {
			authKeyID, _ := AuthKeyIDFrom(ctx)
			sessionID, _ := SessionIDFrom(ctx)
			hash, delivery, handled, err := svc.ResendConfirmPhoneCode(ctx, userID, authKeyID, sessionID, req.PhoneNumber, req.PhoneCodeHash)
			if handled {
				if err != nil {
					return nil, accountDeletionErr(err)
				}
				return tgSMSSentCode(hash, delivery.Length), nil
			}
		}
	}
	var hash string
	var err error
	if scoped, ok := r.deps.Auth.(interface {
		ResendCodeForAuthKey(context.Context, [8]byte, string, string) (string, error)
	}); ok {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		hash, err = scoped.ResendCodeForAuthKey(ctx, authKeyID, req.PhoneNumber, req.PhoneCodeHash)
	} else {
		hash, err = r.deps.Auth.ResendCode(ctx, req.PhoneNumber, req.PhoneCodeHash)
	}
	if err != nil {
		return nil, signInErr(err)
	}
	return r.tgSentCodeForHash(ctx, hash)
}

func (r *Router) onAuthCancelCode(ctx context.Context, req *tg.AuthCancelCodeRequest) (bool, error) {
	if userID, authorized, err := r.currentUserID(ctx); err == nil && authorized && userID != 0 {
		if svc, ok := r.deps.Account.(accountDeletionService); ok {
			authKeyID, _ := AuthKeyIDFrom(ctx)
			handled, err := svc.CancelConfirmPhoneCode(ctx, userID, authKeyID, req.PhoneNumber, req.PhoneCodeHash)
			if handled {
				if err != nil {
					return false, accountDeletionErr(err)
				}
				return true, nil
			}
		}
	}
	var err error
	if scoped, ok := r.deps.Auth.(interface {
		CancelCodeForAuthKey(context.Context, [8]byte, string, string) error
	}); ok {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		err = scoped.CancelCodeForAuthKey(ctx, authKeyID, req.PhoneNumber, req.PhoneCodeHash)
	} else {
		err = r.deps.Auth.CancelCode(ctx, req.PhoneNumber, req.PhoneCodeHash)
	}
	if err != nil {
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
		// Revoking the other sessions deletes their business authorization; the protocol key is kept
		// so the client gets AUTH_KEY_UNREGISTERED after reconnecting. Secret chats are still
		// discarded along the device-authorization boundary and the peer is notified.
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
	// Two-step verification passed: clear password_pending and promote auth_key/session to fully authorized.
	if pending {
		if err := r.completePendingPasswordSignIn(ctx, authKeyID, userID); err != nil {
			return nil, internalErr()
		}
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AuthAuthorization{User: r.tgSelfUserWithUsernames(ctx, u)}, nil
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
	return &tg.AuthAuthorization{User: r.tgSelfUserWithUsernames(ctx, u)}, nil
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
	if err := r.deps.Auth.CompletePasswordSignIn(ctx, authKeyID, userID); err != nil {
		return err
	}
	r.invalidateAuthUserCache(authKeyID)
	r.setAuthUserCache(authKeyID, userID, true)
	r.bindSessionUser(ctx, userID)
	return nil
}

// onAuthResetLoginEmail handles auth.resetLoginEmail: when the user cannot access the login email
// on the logging-in device, it clears the login email, switches back to phone-code login, and
// returns a new phone sentCode for the user to continue.
type loginEmailResetConsumer interface {
	ConsumeLoginEmailReset(ctx context.Context, phone, phoneCodeHash string) (userID int64, err error)
	SendPhoneCodeAfterLoginEmailReset(ctx context.Context, phone string, expectedUserID int64) (string, error)
}

func (r *Router) onAuthResetLoginEmail(ctx context.Context, req *tg.AuthResetLoginEmailRequest) (tg.AuthSentCodeClass, error) {
	if r.deps.Account == nil || r.deps.Auth == nil {
		return nil, internalErr()
	}
	if err := r.checkAuthCodeRateLimit(ctx, req.PhoneNumber); err != nil {
		return nil, err
	}
	resetConsumer, ok := r.deps.Auth.(loginEmailResetConsumer)
	if !ok {
		return nil, internalErr()
	}
	resetUserID, err := resetConsumer.ConsumeLoginEmailReset(ctx, req.PhoneNumber, req.PhoneCodeHash)
	if err != nil {
		return nil, signInErr(err)
	}
	if err := r.deps.Account.ClearLoginEmail(ctx, resetUserID); err != nil {
		return nil, internalErr()
	}
	hash, err := resetConsumer.SendPhoneCodeAfterLoginEmailReset(ctx, req.PhoneNumber, resetUserID)
	if err != nil {
		if errors.Is(err, auth.ErrCodeExpired) || errors.Is(err, auth.ErrCodeInvalid) {
			return nil, signInErr(err)
		}
		if errors.Is(err, auth.ErrPhoneNumberInvalid) ||
			errors.Is(err, auth.ErrSystemUserLoginForbidden) {
			return nil, phoneNumberInvalidErr()
		}
		return nil, internalErr()
	}
	return r.tgSentCodeForHash(ctx, hash)
}

// emailVerificationCode extracts the verifiable string from an emailVerification (the verification
// code / a Google·Apple token); ultimately it must be precisely validated by the auth service
// against the issued record.
// onAuthInitPasskeyLogin handles auth.initPasskeyLogin: generates a one-time assertion challenge
// (discoverable) and returns it as DataJSON (with publicKey at the top level). No authorization
// required (before login).
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

// onAuthFinishPasskeyLogin handles auth.finishPasskeyLogin: validates the login assertion and binds
// the auth_key. The wrap-up is isomorphic with signIn (Bind atomically switches the update baseline
// -> authorization cache -> session binding); a passkey is a strong factor and directly fully
// authorizes (without SESSION_PASSWORD_NEEDED). FromDCID/FromAuthKeyID are for multi-DC rerouting
// and are ignored in this single-DC setup.
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
	if id, ok := AuthKeyIDFrom(ctx); ok {
		r.setAuthUserCache(id, u.ID, true)
	}
	r.bindSessionUser(ctx, u.ID)
	return &tg.AuthAuthorization{User: r.tgSelfUserWithUsernames(ctx, u)}, nil
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

// onAuthImportBotAuthorization handles auth.importBotAuthorization: a bot program logs in as a bot
// account using a token. api_id/api_hash are, consistent with the existing sendCode behavior, not
// validated (there is no app registry). The wrap-up is isomorphic with signIn (Bind atomically
// switches the update baseline -> authorization cache -> session binding), but it writes no login
// message and pushes no signIn service notification -- those are phone-login semantics.
func (r *Router) onAuthImportBotAuthorization(ctx context.Context, req *tg.AuthImportBotAuthorizationRequest) (tg.AuthAuthorizationClass, error) {
	if r.deps.Auth == nil {
		return nil, accessTokenInvalidErr()
	}
	u, err := r.deps.Auth.SignInBot(ctx, r.authzFromCtx(ctx), req.BotAuthToken)
	if err != nil {
		return nil, importBotAuthorizationErr(err)
	}
	if id, ok := AuthKeyIDFrom(ctx); ok {
		r.setAuthUserCache(id, u.ID, true)
	}
	r.bindSessionUser(ctx, u.ID)
	return &tg.AuthAuthorization{User: r.tgSelfUserWithUsernames(ctx, u)}, nil
}

// onAuthSignUp handles auth.signUp: creates the user and binds the authorization.
func (r *Router) onAuthSignUp(ctx context.Context, req *tg.AuthSignUpRequest) (tg.AuthAuthorizationClass, error) {
	u, loginMessage, err := r.deps.Auth.SignUp(ctx, r.authzFromCtx(ctx), req.PhoneNumber, req.PhoneCodeHash, req.FirstName, req.LastName)
	if err != nil {
		return nil, signInErr(err)
	}
	if id, ok := AuthKeyIDFrom(ctx); ok {
		r.setAuthUserCache(id, u.ID, true)
	}
	r.bindSessionUser(ctx, u.ID)
	r.enqueueLoginMessageBootstrap(ctx, loginMessage)
	return &tg.AuthAuthorization{User: r.tgSelfUserWithUsernames(ctx, u)}, nil
}

// onAuthLogOut handles auth.logOut: unbinds the current auth_key's authorization.
func (r *Router) onAuthLogOut(ctx context.Context) (*tg.AuthLoggedOut, error) {
	id, _ := AuthKeyIDFrom(ctx)
	userID, authorized, userErr := r.currentUserID(ctx)
	if err := r.deps.Auth.LogOut(ctx, id); err != nil {
		return nil, internalErr()
	}
	r.invalidateAuthUserCache(id)
	r.unbindAuthKey(id)
	// A bot logout does not broadcast offline (bots have no presence semantics, symmetric with the login path).
	if userErr == nil && authorized && userID != 0 && !r.userIsBot(ctx, userID) {
		status, _ := r.setPresenceFromContext(ctx, userID, true, presencePersistSync)
		r.pushUserStatus(ctx, userID, status)
		// After logout, proactively clear this session's presence entry: the connection usually does
		// not close (the client returns to the login page), and unbindAuthKey above already cleared
		// the connection's userID to 0, so when TCP actually disconnects SessionOffline returns early
		// due to userID=0 and no longer clears presence, leaving the entry leaked in the offline
		// state. Clear it along with the logout here.
		if key, ok := presenceSessionKeyFromContext(ctx); ok {
			r.presence.clearSession(key)
		}
	}
	// After logout revokes this device's authorization, cascade-discard the active secret chats it is
	// bound to and notify the peer (otherwise the peer would keep delivering to the exited device as
	// a silent dead link). Best-effort, does not block logout.
	if userErr == nil && userID != 0 {
		r.discardSecretChatsForAuthKey(ctx, businessAuthKeyInt64(id), userID)
	}
	if err := r.clearAuthKeyState(ctx, id); err != nil {
		return nil, internalErr()
	}
	return &tg.AuthLoggedOut{}, nil
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
	r.deps.Sessions.BindUserForAuthKey(rawAuthKeyIDForOrigin(ctx), sessionID, userID)
	r.announceSessionOnline(ctx, userID)
}

func (r *Router) unbindAuthKey(authKeyID [8]byte) {
	if r.deps.Sessions == nil {
		return
	}
	r.deps.Sessions.UnbindAuthKey(authKeyID)
}

// revokeAuthKeySessions is the full invalidation loop for an authorization revocation (a kicked
// device): clear the Router authorization cache, clear the temp->perm short cache, force-disconnect
// online connections, and finally unbind as a fallback. Disconnecting is not optional -- outbound
// pushes are encrypted with the key held by the connection and do not re-query the authorization
// table, and a perm-key connection's authorization cache is only re-queried on reconnect; without
// disconnecting, the kicked device would keep receiving pushes and keep sending requests as its
// cached identity. After reconnecting, re-querying the store yields unauthorized (401).
//
// Order matters: CloseSessionsForBusinessAuthKey before unbindAuthKey. Close internally does
// removeLocked, reading the connection's current userID to generate the SessionOffline event
// (driving presence cleanup and the offline broadcast); if unbind came first and cleared userID to
// 0, the event would degrade to userID=0, the kicked device's presence entry would not be cleaned,
// and the friend side would show it online for up to one online TTL. Close has already removed the
// connection from the index, so the subsequent unbind only matters for Sessions implementations
// that do not implement SessionTerminator (production implementations just need Close; unbind is a
// no-op).
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
	rawAuthKeyID, hasRawAuthKeyID := RawAuthKeyIDFrom(ctx)
	sessionID, hasSessionID := SessionIDFrom(ctx)
	if !hasAuthKeyID || !hasRawAuthKeyID || !hasSessionID {
		return
	}
	notification := r.tgSignInServiceNotification(ctx, u, authKeyID)
	go func() {
		pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if sent, err := r.deps.Sessions.PushToUserExceptAuthKeySession(pushCtx, u.ID, rawAuthKeyID, sessionID, proto.MessageFromServer, notification); err != nil {
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
			parts = append(parts, branding.UserVisibleText(ci.DeviceModel, ""))
		}
		if ci.SystemVersion != "" {
			parts = append(parts, branding.UserVisibleText(ci.SystemVersion, ""))
		}
		if ci.AppVersion != "" {
			parts = append(parts, branding.UserVisibleText(ci.AppVersion, ""))
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
