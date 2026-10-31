package rpc

import "github.com/iamxvbaba/td/tg"

// rpcAllowedWithoutAuthorization returns true for methods that are valid before
// an auth key is bound to a user. Everything else must fail with
// AUTH_KEY_UNREGISTERED so stale Web/desktop sessions fall back to login.
//
// Inbound layer/client-drift upgrades run before this gate (see router.dispatch),
// so ids here are always canonical (227) — old client constructor ids never reach
// this check.
func rpcAllowedWithoutAuthorization(id uint32) bool {
	switch id {
	case tg.AuthBindTempAuthKeyRequestTypeID,
		// TWeb handles a 401 from a remotely revoked session by sending
		// auth.logOut before it clears IndexedDB/local authorization state.
		// This cleanup RPC is idempotent when no authorization remains; rejecting
		// it with another 401 makes Web repeat its startup/logout cycle forever.
		tg.AuthLogOutRequestTypeID,
		tg.AuthExportLoginTokenRequestTypeID,
		tg.AuthImportLoginTokenRequestTypeID,
		tg.AuthAcceptLoginTokenRequestTypeID,
		tg.AuthInitPasskeyLoginRequestTypeID,
		tg.AuthFinishPasskeyLoginRequestTypeID,
		tg.AuthDropTempAuthKeysRequestTypeID,
		tg.AuthSendCodeRequestTypeID,
		tg.AuthResendCodeRequestTypeID,
		tg.AuthCancelCodeRequestTypeID,
		tg.AuthSignInRequestTypeID,
		tg.AuthSignUpRequestTypeID,
		tg.AuthImportBotAuthorizationRequestTypeID,
		tg.AuthCheckPasswordRequestTypeID,
		tg.AuthRequestPasswordRecoveryRequestTypeID,
		tg.AuthRecoverPasswordRequestTypeID,
		tg.AuthCheckRecoveryPasswordRequestTypeID,
		tg.AuthRequestFirebaseSMSRequestTypeID,
		tg.AuthReportMissingCodeRequestTypeID,
		tg.AuthResetLoginEmailRequestTypeID,
		tg.AccountGetPasswordRequestTypeID,
		// deleteAccount may complete the narrow password_pending login path when
		// the user forgot 2FA. The handler resolves only that bound identity.
		tg.AccountDeleteAccountRequestTypeID,
		// Login email setup (emailVerifyPurposeLoginSetup) happens during the login flow, before
		// authentication, so these two account.* methods must be allowed pre-auth; the loginChange
		// branch still validates userID internally.
		tg.AccountSendVerifyEmailCodeRequestTypeID,
		tg.AccountVerifyEmailRequestTypeID,
		tg.HelpGetConfigRequestTypeID,
		tg.HelpGetNearestDCRequestTypeID,
		tg.HelpGetInviteTextRequestTypeID,
		tg.HelpSaveAppLogRequestTypeID,
		tg.HelpGetAppConfigRequestTypeID,
		tg.HelpGetCountriesListRequestTypeID,
		tg.HelpGetTimezonesListRequestTypeID,
		tg.HelpGetPeerColorsRequestTypeID,
		tg.HelpGetPeerProfileColorsRequestTypeID,
		tg.HelpGetPromoDataRequestTypeID,
		tg.HelpGetTermsOfServiceUpdateRequestTypeID,
		tg.LangpackGetLanguagesRequestTypeID,
		tg.LangpackGetLanguageRequestTypeID,
		tg.LangpackGetLangPackRequestTypeID,
		tg.LangpackGetDifferenceRequestTypeID,
		tg.LangpackGetStringsRequestTypeID:
		return true
	default:
		return false
	}
}
