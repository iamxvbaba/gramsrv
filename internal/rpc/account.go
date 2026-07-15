package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"

	ioscompat "telesrv/internal/compat/ios"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// registerAccount 注册 account.* RPC handler。
func (r *Router) registerAccount(d *tg.ServerDispatcher) {
	d.OnAccountRegisterDevice(func(ctx context.Context, req *tg.AccountRegisterDeviceRequest) (bool, error) {
		return true, nil
	})
	d.OnAccountUnregisterDevice(func(ctx context.Context, req *tg.AccountUnregisterDeviceRequest) (bool, error) {
		return true, nil
	})
	d.OnAccountUpdateDeviceLocked(func(ctx context.Context, period int) (bool, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return false, internalErr()
		}
		return ioscompat.DeviceLockedUpdated(), nil
	})
	d.OnAccountSendChangePhoneCode(r.onAccountSendChangePhoneCode)
	d.OnAccountChangePhone(r.onAccountChangePhone)
	d.OnAccountCheckUsername(r.onAccountCheckUsername)
	d.OnAccountUpdateProfile(r.onAccountUpdateProfile)
	d.OnAccountUpdateUsername(r.onAccountUpdateUsername)
	d.OnAccountUpdateBirthday(r.onAccountUpdateBirthday)
	d.OnAccountUpdatePersonalChannel(r.onAccountUpdatePersonalChannel)
	d.OnAccountGetPassword(r.onAccountGetPassword)
	d.OnAccountGetNotifySettings(r.onAccountGetNotifySettings)
	d.OnAccountUpdateNotifySettings(r.onAccountUpdateNotifySettings)
	d.OnAccountResetNotifySettings(r.onAccountResetNotifySettings)
	d.OnAccountGetPrivacy(r.onAccountGetPrivacy)
	d.OnAccountSetPrivacy(r.onAccountSetPrivacy)
	d.OnAccountGetAuthorizations(r.onAccountGetAuthorizations)
	d.OnAccountResetAuthorization(r.onAccountResetAuthorization)
	d.OnAccountGetPasswordSettings(r.onAccountGetPasswordSettings)
	d.OnAccountUpdatePasswordSettings(r.onAccountUpdatePasswordSettings)
	d.OnAccountConfirmPasswordEmail(r.onAccountConfirmPasswordEmail)
	d.OnAccountResendPasswordEmail(r.onAccountResendPasswordEmail)
	d.OnAccountCancelPasswordEmail(r.onAccountCancelPasswordEmail)
	d.OnAccountSendVerifyEmailCode(r.onAccountSendVerifyEmailCode)
	d.OnAccountVerifyEmail(r.onAccountVerifyEmail)
	d.OnAccountGetDefaultEmojiStatuses(r.onAccountGetDefaultEmojiStatuses)
	d.OnAccountGetCollectibleEmojiStatuses(func(ctx context.Context, hash int64) (tg.AccountEmojiStatusesClass, error) {
		return tdesktop.CollectibleEmojiStatuses(), nil
	})
	d.OnAccountGetDefaultGroupPhotoEmojis(func(ctx context.Context, hash int64) (tg.EmojiListClass, error) {
		return tdesktop.DefaultGroupPhotoEmojis(), nil
	})
	d.OnAccountGetConnectedBots(r.onAccountGetConnectedBots)
	d.OnAccountUpdateBusinessWorkHours(r.onAccountUpdateBusinessWorkHours)
	d.OnAccountUpdateBusinessLocation(r.onAccountUpdateBusinessLocation)
	d.OnAccountUpdateBusinessIntro(r.onAccountUpdateBusinessIntro)
	d.OnAccountUpdateBusinessGreetingMessage(r.onAccountUpdateBusinessGreetingMessage)
	d.OnAccountUpdateBusinessAwayMessage(r.onAccountUpdateBusinessAwayMessage)
	d.OnAccountGetBusinessChatLinks(r.onAccountGetBusinessChatLinks)
	d.OnAccountCreateBusinessChatLink(r.onAccountCreateBusinessChatLink)
	d.OnAccountEditBusinessChatLink(r.onAccountEditBusinessChatLink)
	d.OnAccountDeleteBusinessChatLink(r.onAccountDeleteBusinessChatLink)
	d.OnAccountResolveBusinessChatLink(r.onAccountResolveBusinessChatLink)
	d.OnAccountUpdateConnectedBot(r.onAccountUpdateConnectedBot)
	d.OnAccountGetBotBusinessConnection(func(ctx context.Context, connectionID string) (tg.UpdatesClass, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return nil, tgerr400("BOT_BUSINESS_MISSING")
	})
	d.OnAccountToggleConnectedBotPaused(r.onAccountToggleConnectedBotPaused)
	d.OnAccountDisablePeerConnectedBot(r.onAccountDisablePeerConnectedBot)
	d.OnAccountGetReactionsNotifySettings(r.onAccountGetReactionsNotifySettings)
	d.OnAccountSetReactionsNotifySettings(r.onAccountSetReactionsNotifySettings)
	d.OnAccountGetContactSignUpNotification(r.onAccountGetContactSignUpNotification)
	d.OnAccountSetContactSignUpNotification(r.onAccountSetContactSignUpNotification)
	d.OnAccountGetThemes(r.onAccountGetThemes)
	d.OnAccountGetChatThemes(func(ctx context.Context, hash int64) (tg.AccountThemesClass, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return tdesktop.ChatThemes(hash), nil
	})
	// 自定义云主题(Create a New Theme 全链路):upload→create→update→save→install→get。
	d.OnAccountUploadTheme(r.onAccountUploadTheme)
	d.OnAccountCreateTheme(r.onAccountCreateTheme)
	d.OnAccountUpdateTheme(r.onAccountUpdateTheme)
	d.OnAccountSaveTheme(r.onAccountSaveTheme)
	d.OnAccountInstallTheme(r.onAccountInstallTheme)
	d.OnAccountGetTheme(r.onAccountGetTheme)
	d.OnAccountGetWallPapers(func(ctx context.Context, hash int64) (tg.AccountWallPapersClass, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return tdesktop.WallPapers(hash), nil
	})
	d.OnAccountGetWallPaper(func(ctx context.Context, wallpaper tg.InputWallPaperClass) (tg.WallPaperClass, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		found, ok := tdesktop.LookupWallPaper(wallpaper)
		if !ok {
			return nil, tgerr400("WALLPAPER_INVALID")
		}
		return found, nil
	})
	d.OnAccountGetMultiWallPapers(func(ctx context.Context, wallpapers []tg.InputWallPaperClass) ([]tg.WallPaperClass, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		if len(wallpapers) > 100 {
			return nil, tgerr400("WALLPAPER_INVALID")
		}
		found, ok := tdesktop.LookupWallPapers(wallpapers)
		if !ok {
			return nil, tgerr400("WALLPAPER_INVALID")
		}
		return found, nil
	})
	d.OnAccountSaveWallPaper(func(ctx context.Context, req *tg.AccountSaveWallPaperRequest) (bool, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return false, internalErr()
		}
		if req == nil {
			return false, tgerr400("WALLPAPER_INVALID")
		}
		if _, ok := tdesktop.LookupWallPaper(req.Wallpaper); !ok {
			return false, tgerr400("WALLPAPER_INVALID")
		}
		return true, nil
	})
	d.OnAccountInstallWallPaper(func(ctx context.Context, req *tg.AccountInstallWallPaperRequest) (bool, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return false, internalErr()
		}
		if req == nil {
			return false, tgerr400("WALLPAPER_INVALID")
		}
		if _, ok := tdesktop.LookupWallPaper(req.Wallpaper); !ok {
			return false, tgerr400("WALLPAPER_INVALID")
		}
		return true, nil
	})
	d.OnAccountResetWallPapers(func(ctx context.Context) (bool, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return false, internalErr()
		}
		return true, nil
	})
	d.OnAccountGetUniqueGiftChatThemes(func(ctx context.Context, req *tg.AccountGetUniqueGiftChatThemesRequest) (tg.AccountChatThemesClass, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		if req == nil {
			req = &tg.AccountGetUniqueGiftChatThemesRequest{}
		}
		return tdesktop.UniqueGiftChatThemes(req.Hash), nil
	})
	d.OnAccountGetRecentEmojiStatuses(func(ctx context.Context, hash int64) (tg.AccountEmojiStatusesClass, error) {
		return &tg.AccountEmojiStatuses{Hash: 0, Statuses: []tg.EmojiStatusClass{}}, nil
	})
	d.OnAccountClearRecentEmojiStatuses(func(ctx context.Context) (bool, error) {
		return true, nil
	})
	d.OnAccountUpdateEmojiStatus(r.onAccountUpdateEmojiStatus)
	d.OnAccountUpdateColor(r.onAccountUpdateColor)
	d.OnAccountGetDefaultProfilePhotoEmojis(r.onAccountGetDefaultProfilePhotoEmojis)
	d.OnAccountGetDefaultBackgroundEmojis(r.onAccountGetDefaultBackgroundEmojis)
	d.OnAccountGetChannelDefaultEmojiStatuses(func(ctx context.Context, hash int64) (tg.AccountEmojiStatusesClass, error) {
		return &tg.AccountEmojiStatuses{Hash: 0, Statuses: []tg.EmojiStatusClass{}}, nil
	})
	d.OnAccountGetChannelRestrictedStatusEmojis(func(ctx context.Context, hash int64) (tg.EmojiListClass, error) {
		return tdesktop.DefaultGroupPhotoEmojis(), nil
	})
	d.OnAccountSetContentSettings(r.onAccountSetContentSettings)
	d.OnAccountGetContentSettings(r.onAccountGetContentSettings)
	d.OnAccountGetGlobalPrivacySettings(r.onAccountGetGlobalPrivacySettings)
	d.OnAccountSetGlobalPrivacySettings(r.onAccountSetGlobalPrivacySettings)
	d.OnAccountGetPasskeys(r.onAccountGetPasskeys)
	d.OnAccountInitPasskeyRegistration(r.onAccountInitPasskeyRegistration)
	d.OnAccountRegisterPasskey(r.onAccountRegisterPasskey)
	d.OnAccountDeletePasskey(r.onAccountDeletePasskey)
	d.OnAccountGetWebAuthorizations(func(ctx context.Context) (*tg.AccountWebAuthorizations, error) {
		return tdesktop.WebAuthorizations(), nil
	})
	d.OnAccountResetWebAuthorization(func(ctx context.Context, hash int64) (bool, error) {
		return true, nil
	})
	d.OnAccountResetWebAuthorizations(func(ctx context.Context) (bool, error) {
		return true, nil
	})
	// account.getWebBrowserSettings：telesrv 不接入网页浏览器（web bot）集成，返回空设置
	// （无内置浏览器例外、不强制外部浏览器）。Android 启动时会拉取，缺它会反复 500
	// NOT_IMPLEMENTED。空结构 Hash=0，客户端按默认（内置浏览器、无例外）渲染。
	d.OnAccountGetWebBrowserSettings(func(ctx context.Context, hash int64) (tg.AccountWebBrowserSettingsClass, error) {
		return &tg.AccountWebBrowserSettings{}, nil
	})
	d.OnAccountGetNotifyExceptions(r.onAccountGetNotifyExceptions)
	d.OnAccountGetAutoDownloadSettings(func(ctx context.Context) (*tg.AccountAutoDownloadSettings, error) {
		return tdesktop.AutoDownloadSettings(), nil
	})
	d.OnAccountSaveAutoDownloadSettings(func(ctx context.Context, req *tg.AccountSaveAutoDownloadSettingsRequest) (bool, error) {
		return true, nil
	})
	d.OnAccountSaveMusic(r.onAccountSaveMusic)
	d.OnAccountGetSavedMusicIDs(r.onAccountGetSavedMusicIDs)
	d.OnAccountGetSavedRingtones(func(ctx context.Context, hash int64) (tg.AccountSavedRingtonesClass, error) {
		if _, _, err := r.currentUserID(ctx); err != nil {
			return nil, internalErr()
		}
		return &tg.AccountSavedRingtones{Hash: 0, Ringtones: []tg.DocumentClass{}}, nil
	})
	d.OnAccountGetAccountTTL(r.onAccountGetAccountTTL)
	d.OnAccountSetAccountTTL(r.onAccountSetAccountTTL)
	d.OnAccountSetAuthorizationTTL(func(ctx context.Context, authorizationttldays int) (bool, error) {
		return true, nil
	})
	d.OnAccountChangeAuthorizationSettings(func(ctx context.Context, req *tg.AccountChangeAuthorizationSettingsRequest) (bool, error) {
		return true, nil
	})
	d.OnAccountResetPassword(r.onAccountResetPassword)
	d.OnAccountDeclinePasswordReset(r.onAccountDeclinePasswordReset)
	d.OnAccountUpdateStatus(r.onAccountUpdateStatus)
}

func (r *Router) onAccountGetPassword(ctx context.Context) (*tg.AccountPassword, error) {
	if r.deps.Account == nil {
		return tgPassword(domain.PasswordSettings{SecureRandom: []byte("telesrv-tdesktop-dev-secure-rand")}), nil
	}
	userID, _, _, err := r.currentOrPendingPasswordUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	settings, err := r.deps.Account.GetPassword(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return tgPassword(settings), nil
}

func (r *Router) onAccountSaveMusic(ctx context.Context, req *tg.AccountSaveMusicRequest) (bool, error) {
	if req == nil {
		return false, documentInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	doc, err := r.musicDocumentFromInput(ctx, req.ID)
	if err != nil {
		return false, err
	}
	var afterID int64
	if after, ok := req.GetAfterID(); ok {
		afterDoc, err := r.musicDocumentFromInput(ctx, after)
		if err != nil {
			return false, err
		}
		afterID = afterDoc.ID
	}
	if r.deps.Account == nil {
		return true, nil
	}
	ok, err := r.deps.Account.SaveMusic(ctx, userID, domain.SaveMusicRequest{
		UserID:          userID,
		Document:        doc,
		Unsave:          req.Unsave,
		AfterDocumentID: afterID,
		Date:            int(r.clock.Now().Unix()),
	})
	if err != nil {
		return false, savedMusicErr(err)
	}
	if ok {
		r.invalidateRPCProjectionForUser(userID)
	}
	return ok, nil
}

func (r *Router) onAccountGetSavedMusicIDs(ctx context.Context, hash int64) (tg.AccountSavedMusicIDsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Account == nil {
		return &tg.AccountSavedMusicIDs{IDs: []int64{}}, nil
	}
	ids, err := r.deps.Account.ListSavedMusicIDs(ctx, userID, domain.MaxSavedMusicItems)
	if err != nil {
		return nil, internalErr()
	}
	if hash != 0 && int64(tdesktopCountHash(ids)) == hash {
		return &tg.AccountSavedMusicIDsNotModified{}, nil
	}
	return &tg.AccountSavedMusicIDs{IDs: ids}, nil
}

func (r *Router) musicDocumentFromInput(ctx context.Context, input tg.InputDocumentClass) (domain.Document, error) {
	in, ok := input.(*tg.InputDocument)
	if !ok || in.ID == 0 || r.deps.Files == nil {
		return domain.Document{}, documentInvalidErr()
	}
	doc, found, err := r.deps.Files.GetDocument(ctx, in.ID)
	if err != nil {
		return domain.Document{}, internalErr()
	}
	if !found || doc.AccessHash != in.AccessHash || !doc.IsMusic() {
		return domain.Document{}, documentInvalidErr()
	}
	return doc, nil
}

func savedMusicErr(err error) error {
	if errors.Is(err, domain.ErrDocumentInvalid) {
		return documentInvalidErr()
	}
	return internalErr()
}

func (r *Router) onAccountGetAuthorizations(ctx context.Context) (*tg.AccountAuthorizations, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Auth == nil {
		return tdesktop.Authorizations(), nil
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	items, err := r.deps.Auth.ListAuthorizations(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	out := &tg.AccountAuthorizations{Authorizations: make([]tg.Authorization, 0, len(items))}
	for _, item := range items {
		out.Authorizations = append(out.Authorizations, tgAuthorization(item, authKeyID, int(r.clock.Now().Unix())))
	}
	return out, nil
}

func (r *Router) onAccountResetAuthorization(ctx context.Context, hash int64) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Auth == nil {
		return true, nil
	}
	deleted, found, err := r.deps.Auth.ResetAuthorization(ctx, userID, hash)
	if err != nil {
		return false, internalErr()
	}
	if !found {
		return true, nil
	}
	r.revokeAuthKeySessions(deleted.AuthKeyID)
	_ = r.clearAuthKeyState(ctx, deleted.AuthKeyID)
	// P1 修复：撤销该会话销毁其 auth_key，级联 discard 该设备绑定的活跃密聊并通知对端。
	r.discardSecretChatsForAuthKey(ctx, businessAuthKeyInt64(deleted.AuthKeyID), userID)
	return true, nil
}

func (r *Router) onAccountGetPasswordSettings(ctx context.Context, password tg.InputCheckPasswordSRPClass) (*tg.AccountPasswordSettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	settings, err := r.deps.Account.GetPasswordSettings(ctx, userID, domainPasswordCheck(password))
	if err != nil {
		return nil, passwordErr(err)
	}
	return tgPasswordSettings(settings), nil
}

func (r *Router) onAccountUpdatePasswordSettings(ctx context.Context, req *tg.AccountUpdatePasswordSettingsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	input, err := domainPasswordInputSettings(req.NewSettings)
	if err != nil {
		return false, err
	}
	if err := r.deps.Account.UpdatePasswordSettings(ctx, userID, domainPasswordCheck(req.Password), input); err != nil {
		return false, passwordErr(err)
	}
	return true, nil
}

func (r *Router) onAccountConfirmPasswordEmail(ctx context.Context, code string) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if err := r.deps.Account.ConfirmPasswordEmail(ctx, userID, code); err != nil {
		return false, passwordErr(err)
	}
	return true, nil
}

func (r *Router) onAccountResendPasswordEmail(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if err := r.deps.Account.ResendPasswordEmail(ctx, userID); err != nil {
		return false, passwordErr(err)
	}
	return true, nil
}

func (r *Router) onAccountCancelPasswordEmail(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if err := r.deps.Account.CancelPasswordEmail(ctx, userID); err != nil {
		return false, passwordErr(err)
	}
	return true, nil
}

// onAccountSendVerifyEmailCode 处理 account.sendVerifyEmailCode：为登录邮箱的设置/变更
// 发送邮箱验证码。loginChange 走已登录用户，loginSetup 走登录流程中的手机号 + phone_code_hash。
func (r *Router) onAccountSendVerifyEmailCode(ctx context.Context, req *tg.AccountSendVerifyEmailCodeRequest) (*tg.AccountSentEmailCode, error) {
	if r.deps.Account == nil {
		return nil, internalErr()
	}
	email := strings.TrimSpace(req.Email)
	if email == "" || !strings.Contains(email, "@") {
		return nil, emailInvalidErr()
	}
	switch p := req.Purpose.(type) {
	case *tg.EmailVerifyPurposeLoginChange:
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		if userID == 0 {
			return nil, authKeyUnregisteredErr()
		}
		pattern, length, err := r.deps.Account.SendLoginEmailCode(ctx, userID, "", "", email, false)
		if err != nil {
			return nil, passwordErr(err)
		}
		return &tg.AccountSentEmailCode{EmailPattern: pattern, Length: length}, nil
	case *tg.EmailVerifyPurposeLoginSetup:
		pattern, length, err := r.deps.Account.SendLoginEmailCode(ctx, 0, p.PhoneNumber, p.PhoneCodeHash, email, true)
		if err != nil {
			return nil, passwordErr(err)
		}
		return &tg.AccountSentEmailCode{EmailPattern: pattern, Length: length}, nil
	default:
		return nil, emailInvalidErr()
	}
}

// onAccountVerifyEmail 处理 account.verifyEmail：确认登录邮箱验证码。
// loginChange（已登录）返回 emailVerified{email}；loginSetup（登录流程中）返回
// emailVerifiedLogin{email, sent_code}。TDesktop 能消费嵌套 auth.sentCodeSuccess，
// 直接进入注册/登录完成；DrKLO Android 12.8.1 该路径漏处理 sentCodeSuccess，
// 临时降级为普通 emailCode sentCode，待 Android 补齐后移除。
func (r *Router) onAccountVerifyEmail(ctx context.Context, req *tg.AccountVerifyEmailRequest) (tg.AccountEmailVerifiedClass, error) {
	if r.deps.Account == nil {
		return nil, internalErr()
	}
	code := emailVerificationCode(req.Verification)
	switch p := req.Purpose.(type) {
	case *tg.EmailVerifyPurposeLoginChange:
		userID, _, err := r.currentUserID(ctx)
		if err != nil {
			return nil, internalErr()
		}
		if userID == 0 {
			return nil, authKeyUnregisteredErr()
		}
		email, err := r.deps.Account.VerifyLoginEmail(ctx, userID, "", "", code, false)
		if err != nil {
			return nil, passwordErr(err)
		}
		return &tg.AccountEmailVerified{Email: email}, nil
	case *tg.EmailVerifyPurposeLoginSetup:
		if r.deps.Auth == nil {
			return nil, internalErr()
		}
		email, err := r.deps.Account.VerifyLoginEmail(ctx, 0, p.PhoneNumber, p.PhoneCodeHash, code, true)
		if err != nil {
			return nil, passwordErr(err)
		}
		if ClientTypeFrom(ctx) == ClientTypeAndroid {
			return &tg.AccountEmailVerifiedLogin{
				Email:    email,
				SentCode: tgEmailSentCode(p.PhoneCodeHash, domain.MaskEmail(email), len(strings.TrimSpace(code))),
			}, nil
		}
		u, _, needSignUp, signInErr := r.deps.Auth.SignInWithEmail(ctx, r.authzFromCtx(ctx), p.PhoneNumber, p.PhoneCodeHash, code)
		authorization, err := r.finishAuthSignIn(ctx, u, needSignUp, signInErr)
		if err != nil {
			return nil, err
		}
		return &tg.AccountEmailVerifiedLogin{
			Email: email,
			SentCode: &tg.AuthSentCodeSuccess{
				Authorization: authorization,
			},
		}, nil
	default:
		return nil, emailInvalidErr()
	}
}

// onAccountGetPasskeys 列出当前用户的 passkey(管理页)。
func (r *Router) onAccountGetPasskeys(ctx context.Context) (*tg.AccountPasskeys, error) {
	if r.deps.Passkey == nil {
		return &tg.AccountPasskeys{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	creds, err := r.deps.Passkey.List(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AccountPasskeys{Passkeys: tgPasskeys(creds)}, nil
}

// onAccountInitPasskeyRegistration 为已登录用户生成 passkey 注册选项(creation options)。
func (r *Router) onAccountInitPasskeyRegistration(ctx context.Context) (*tg.AccountPasskeyRegistrationOptions, error) {
	if r.deps.Passkey == nil {
		return nil, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	options, err := r.deps.Passkey.InitRegistration(ctx, userID, r.passkeyDisplayName(ctx, userID))
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AccountPasskeyRegistrationOptions{Options: tg.DataJSON{Data: string(options)}}, nil
}

// onAccountRegisterPasskey 验证注册 attestation 并持久化凭据。
func (r *Router) onAccountRegisterPasskey(ctx context.Context, credential tg.InputPasskeyCredentialClass) (*tg.Passkey, error) {
	if r.deps.Passkey == nil {
		return nil, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, authKeyUnregisteredErr()
	}
	credID, reg, ok := passkeyRegisterFromCredential(credential)
	if !ok {
		return nil, passkeyErr(domain.ErrPasskeyInvalid)
	}
	cred, err := r.deps.Passkey.Register(ctx, userID, credID, []byte(reg.ClientData.Data), reg.AttestationData, "")
	if err != nil {
		return nil, passkeyErr(err)
	}
	pk := tgPasskey(cred)
	return &pk, nil
}

// onAccountDeletePasskey 删除当前用户的某个 passkey。
func (r *Router) onAccountDeletePasskey(ctx context.Context, id string) (bool, error) {
	if r.deps.Passkey == nil {
		return false, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	credID, ok := decodePasskeyID(id)
	if !ok {
		return false, passkeyErr(domain.ErrPasskeyInvalid)
	}
	deleted, err := r.deps.Passkey.Delete(ctx, userID, credID)
	if err != nil {
		return false, internalErr()
	}
	return deleted, nil
}

// passkeyDisplayName 取用户显示名(authenticator UI 用),失败回退空串。
func (r *Router) passkeyDisplayName(ctx context.Context, userID int64) string {
	if r.deps.Users == nil {
		return ""
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

func (r *Router) onAccountGetPrivacy(ctx context.Context, key tg.InputPrivacyKeyClass) (*tg.AccountPrivacyRules, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	domainKey, ok := domainPrivacyKeyFromInput(key)
	if !ok {
		return nil, privacyKeyInvalidErr()
	}
	if r.deps.Privacy == nil {
		return tdesktop.PrivacyRules(key), nil
	}
	rules, err := r.deps.Privacy.GetRules(ctx, userID, domainKey)
	if err != nil {
		return nil, privacyErr(err)
	}
	return r.tgAccountPrivacyRules(ctx, userID, rules)
}

func (r *Router) onAccountSetPrivacy(ctx context.Context, req *tg.AccountSetPrivacyRequest) (*tg.AccountPrivacyRules, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	domainKey, ok := domainPrivacyKeyFromInput(req.Key)
	if !ok {
		return nil, privacyKeyInvalidErr()
	}
	rules, err := r.domainPrivacyRulesFromInput(ctx, userID, req.Rules)
	if err != nil {
		return nil, err
	}
	if r.deps.Privacy == nil {
		return &tg.AccountPrivacyRules{Rules: tgPrivacyRules(rules), Users: []tg.UserClass{}, Chats: []tg.ChatClass{}}, nil
	}
	saved, err := r.deps.Privacy.SetRules(ctx, userID, domainKey, rules)
	if err != nil {
		return nil, privacyErr(err)
	}
	out, err := r.tgAccountPrivacyRules(ctx, userID, saved)
	if err != nil {
		return nil, err
	}
	r.invalidateRPCProjectionForUser(userID)
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePrivacy{
			Key:   tgPrivacyKey(saved.Key),
			Rules: tgPrivacyRules(saved.Rules),
		}},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{},
	})
	return out, nil
}

func (r *Router) onAccountGetAccountTTL(ctx context.Context) (*tg.AccountDaysTTL, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	settings, err := r.cachedAccountSettings(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.AccountDaysTTL{Days: settings.NormalizedTTLDays()}, nil
}

func (r *Router) onAccountSetAccountTTL(ctx context.Context, ttl tg.AccountDaysTTL) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if ttl.Days <= 0 {
		return false, tgerr400("TTL_DAYS_INVALID")
	}
	if svc, ok := r.accountSettingsSvc(); ok {
		if _, err := svc.SetAccountTTL(ctx, userID, ttl.Days); err != nil {
			return false, internalErr()
		}
		r.accountSettings.Delete(userID)
	}
	return true, nil
}

func (r *Router) onAccountGetGlobalPrivacySettings(ctx context.Context) (*tg.GlobalPrivacySettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	settings, err := r.cachedAccountSettings(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return tgGlobalPrivacySettings(settings.GlobalPrivacy), nil
}

func (r *Router) onAccountSetGlobalPrivacySettings(ctx context.Context, settings tg.GlobalPrivacySettings) (*tg.GlobalPrivacySettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if svc, ok := r.accountSettingsSvc(); ok {
		saved, err := svc.SetGlobalPrivacy(ctx, userID, domainGlobalPrivacy(settings))
		if err != nil {
			return nil, internalErr()
		}
		r.accountSettings.Delete(userID)
		return tgGlobalPrivacySettings(saved.GlobalPrivacy), nil
	}
	return &settings, nil
}

func (r *Router) onAccountGetContentSettings(ctx context.Context) (*tg.AccountContentSettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	settings, err := r.cachedAccountSettings(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return tgContentSettings(settings.SensitiveContentEnabled), nil
}

func (r *Router) onAccountSetContentSettings(ctx context.Context, req *tg.AccountSetContentSettingsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	if svc, ok := r.accountSettingsSvc(); ok {
		if _, err := svc.SetSensitiveContent(ctx, userID, req.SensitiveEnabled); err != nil {
			return false, internalErr()
		}
		r.accountSettings.Delete(userID)
	}
	return true, nil
}

func (r *Router) onAccountGetContactSignUpNotification(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	settings, err := r.cachedAccountSettings(ctx, userID)
	if err != nil {
		return false, internalErr()
	}
	return settings.ContactSignUpSilent, nil
}

func (r *Router) onAccountSetContactSignUpNotification(ctx context.Context, silent bool) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if svc, ok := r.accountSettingsSvc(); ok {
		if _, err := svc.SetContactSignUpSilent(ctx, userID, silent); err != nil {
			return false, internalErr()
		}
		r.accountSettings.Delete(userID)
	}
	return true, nil
}

func (r *Router) onAccountResetPassword(ctx context.Context) (tg.AccountResetPasswordResultClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Account == nil {
		return &tg.AccountResetPasswordFailedWait{RetryDate: int(r.clock.Now().Unix()) + 86400}, nil
	}
	result, err := r.deps.Account.ResetPassword(ctx, userID)
	if err != nil {
		return nil, passwordErr(err)
	}
	return tgPasswordResetResult(result), nil
}

func (r *Router) onAccountDeclinePasswordReset(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Account == nil {
		return true, nil
	}
	if err := r.deps.Account.DeclinePasswordReset(ctx, userID); err != nil {
		return false, internalErr()
	}
	return true, nil
}

func (r *Router) onAccountUpdateStatus(ctx context.Context, offline bool) (bool, error) {
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if !authorized || userID == 0 {
		return true, nil
	}
	status, _ := r.setPresenceFromContext(ctx, userID, offline, presencePersistSync)
	r.pushUserStatus(ctx, userID, status)
	return true, nil
}

func tgPasswordResetResult(result domain.PasswordResetResult) tg.AccountResetPasswordResultClass {
	switch result.Kind {
	case domain.PasswordResetOK:
		return &tg.AccountResetPasswordOk{}
	case domain.PasswordResetRequestedWait:
		return &tg.AccountResetPasswordRequestedWait{UntilDate: result.UntilDate}
	case domain.PasswordResetFailedWait:
		return &tg.AccountResetPasswordFailedWait{RetryDate: result.RetryDate}
	default:
		return &tg.AccountResetPasswordFailedWait{}
	}
}

func (r *Router) tgAccountPrivacyRules(ctx context.Context, viewerUserID int64, rules domain.PrivacyRules) (*tg.AccountPrivacyRules, error) {
	userIDs := privacyRuleUserIDs(rules.Rules)
	users := []domain.User{}
	if r.deps.Users != nil && len(userIDs) > 0 {
		var err error
		users, err = r.deps.Users.ByIDs(ctx, viewerUserID, userIDs)
		if err != nil {
			return nil, internalErr()
		}
	}
	return &tg.AccountPrivacyRules{
		Rules: tgPrivacyRules(rules.Rules),
		// viewer 可能把自己（inputUserSelf）写进隐私名单，须带 self 标志，否则下发的
		// self=false user 会被 DrKLO putUsers 覆盖账号缓存。
		Users: tgUsersForViewer(viewerUserID, users),
		Chats: []tg.ChatClass{},
	}, nil
}

func (r *Router) domainPrivacyRulesFromInput(ctx context.Context, userID int64, in []tg.InputPrivacyRuleClass) ([]domain.PrivacyRule, error) {
	out := make([]domain.PrivacyRule, 0, len(in))
	for _, rule := range in {
		if inputPrivacyRuleClassNil(rule) {
			return nil, privacyValueInvalidErr()
		}
		switch v := rule.(type) {
		case *tg.InputPrivacyValueAllowContacts:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowContacts})
		case *tg.InputPrivacyValueAllowAll:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowAll})
		case *tg.InputPrivacyValueAllowUsers:
			ids, err := r.privacyUserIDsFromInput(ctx, userID, v.Users)
			if err != nil {
				return nil, err
			}
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowUsers, UserIDs: ids})
		case *tg.InputPrivacyValueDisallowContacts:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleDisallowContacts})
		case *tg.InputPrivacyValueDisallowAll:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleDisallowAll})
		case *tg.InputPrivacyValueDisallowUsers:
			ids, err := r.privacyUserIDsFromInput(ctx, userID, v.Users)
			if err != nil {
				return nil, err
			}
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleDisallowUsers, UserIDs: ids})
		case *tg.InputPrivacyValueAllowChatParticipants:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowChatParticipants, ChatIDs: append([]int64(nil), v.Chats...)})
		case *tg.InputPrivacyValueDisallowChatParticipants:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleDisallowChatParticipants, ChatIDs: append([]int64(nil), v.Chats...)})
		case *tg.InputPrivacyValueAllowCloseFriends:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowCloseFriends})
		case *tg.InputPrivacyValueAllowPremium:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowPremium})
		case *tg.InputPrivacyValueAllowBots:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowBots})
		case *tg.InputPrivacyValueDisallowBots:
			out = append(out, domain.PrivacyRule{Kind: domain.PrivacyRuleDisallowBots})
		default:
			return nil, privacyValueInvalidErr()
		}
	}
	return out, nil
}

func inputPrivacyRuleClassNil(rule tg.InputPrivacyRuleClass) bool {
	switch typed := rule.(type) {
	case nil:
		return true
	case *tg.InputPrivacyValueAllowContacts:
		return typed == nil
	case *tg.InputPrivacyValueAllowAll:
		return typed == nil
	case *tg.InputPrivacyValueAllowUsers:
		return typed == nil
	case *tg.InputPrivacyValueDisallowContacts:
		return typed == nil
	case *tg.InputPrivacyValueDisallowAll:
		return typed == nil
	case *tg.InputPrivacyValueDisallowUsers:
		return typed == nil
	case *tg.InputPrivacyValueAllowChatParticipants:
		return typed == nil
	case *tg.InputPrivacyValueDisallowChatParticipants:
		return typed == nil
	case *tg.InputPrivacyValueAllowCloseFriends:
		return typed == nil
	case *tg.InputPrivacyValueAllowPremium:
		return typed == nil
	case *tg.InputPrivacyValueAllowBots:
		return typed == nil
	case *tg.InputPrivacyValueDisallowBots:
		return typed == nil
	default:
		return false
	}
}

func (r *Router) privacyUserIDsFromInput(ctx context.Context, currentUserID int64, inputs []tg.InputUserClass) ([]int64, error) {
	out := make([]int64, 0, len(inputs))
	seen := make(map[int64]struct{}, len(inputs))
	for _, input := range inputs {
		if inputUserClassNil(input) {
			return nil, userIDInvalidErr()
		}
		if r.deps.Users == nil {
			return nil, userIDInvalidErr()
		}
		u, found, err := r.userFromInput(ctx, currentUserID, input)
		if err != nil {
			return nil, internalErr()
		}
		if !found || u.ID == 0 {
			return nil, userIDInvalidErr()
		}
		if _, ok := seen[u.ID]; ok {
			continue
		}
		seen[u.ID] = struct{}{}
		out = append(out, u.ID)
	}
	return out, nil
}

func inputUserClassNil(input tg.InputUserClass) bool {
	switch typed := input.(type) {
	case nil:
		return true
	case *tg.InputUserEmpty:
		return typed == nil
	case *tg.InputUserSelf:
		return typed == nil
	case *tg.InputUser:
		return typed == nil
	case *tg.InputUserFromMessage:
		return typed == nil
	default:
		return false
	}
}

func domainPrivacyKeyFromInput(key tg.InputPrivacyKeyClass) (domain.PrivacyKey, bool) {
	switch key.(type) {
	case *tg.InputPrivacyKeyStatusTimestamp:
		return domain.PrivacyKeyStatusTimestamp, true
	case *tg.InputPrivacyKeyChatInvite:
		return domain.PrivacyKeyChatInvite, true
	case *tg.InputPrivacyKeyPhoneCall:
		return domain.PrivacyKeyPhoneCall, true
	case *tg.InputPrivacyKeyPhoneP2P:
		return domain.PrivacyKeyPhoneP2P, true
	case *tg.InputPrivacyKeyForwards:
		return domain.PrivacyKeyForwards, true
	case *tg.InputPrivacyKeyProfilePhoto:
		return domain.PrivacyKeyProfilePhoto, true
	case *tg.InputPrivacyKeyPhoneNumber:
		return domain.PrivacyKeyPhoneNumber, true
	case *tg.InputPrivacyKeyAddedByPhone:
		return domain.PrivacyKeyAddedByPhone, true
	case *tg.InputPrivacyKeyVoiceMessages:
		return domain.PrivacyKeyVoiceMessages, true
	case *tg.InputPrivacyKeyAbout:
		return domain.PrivacyKeyAbout, true
	case *tg.InputPrivacyKeyBirthday:
		return domain.PrivacyKeyBirthday, true
	case *tg.InputPrivacyKeyStarGiftsAutoSave:
		return domain.PrivacyKeyStarGiftsAutoSave, true
	case *tg.InputPrivacyKeyNoPaidMessages:
		return domain.PrivacyKeyNoPaidMessages, true
	case *tg.InputPrivacyKeySavedMusic:
		return domain.PrivacyKeySavedMusic, true
	default:
		return "", false
	}
}

func tgPrivacyKey(key domain.PrivacyKey) tg.PrivacyKeyClass {
	switch key {
	case domain.PrivacyKeyStatusTimestamp:
		return &tg.PrivacyKeyStatusTimestamp{}
	case domain.PrivacyKeyChatInvite:
		return &tg.PrivacyKeyChatInvite{}
	case domain.PrivacyKeyPhoneCall:
		return &tg.PrivacyKeyPhoneCall{}
	case domain.PrivacyKeyPhoneP2P:
		return &tg.PrivacyKeyPhoneP2P{}
	case domain.PrivacyKeyForwards:
		return &tg.PrivacyKeyForwards{}
	case domain.PrivacyKeyProfilePhoto:
		return &tg.PrivacyKeyProfilePhoto{}
	case domain.PrivacyKeyPhoneNumber:
		return &tg.PrivacyKeyPhoneNumber{}
	case domain.PrivacyKeyAddedByPhone:
		return &tg.PrivacyKeyAddedByPhone{}
	case domain.PrivacyKeyVoiceMessages:
		return &tg.PrivacyKeyVoiceMessages{}
	case domain.PrivacyKeyAbout:
		return &tg.PrivacyKeyAbout{}
	case domain.PrivacyKeyBirthday:
		return &tg.PrivacyKeyBirthday{}
	case domain.PrivacyKeyStarGiftsAutoSave:
		return &tg.PrivacyKeyStarGiftsAutoSave{}
	case domain.PrivacyKeyNoPaidMessages:
		return &tg.PrivacyKeyNoPaidMessages{}
	case domain.PrivacyKeySavedMusic:
		return &tg.PrivacyKeySavedMusic{}
	default:
		return &tg.PrivacyKeyStatusTimestamp{}
	}
}

func tgPrivacyRules(rules []domain.PrivacyRule) []tg.PrivacyRuleClass {
	out := make([]tg.PrivacyRuleClass, 0, len(rules))
	for _, rule := range rules {
		switch rule.Kind {
		case domain.PrivacyRuleAllowContacts:
			out = append(out, &tg.PrivacyValueAllowContacts{})
		case domain.PrivacyRuleAllowAll:
			out = append(out, &tg.PrivacyValueAllowAll{})
		case domain.PrivacyRuleAllowUsers:
			out = append(out, &tg.PrivacyValueAllowUsers{Users: append([]int64(nil), rule.UserIDs...)})
		case domain.PrivacyRuleDisallowContacts:
			out = append(out, &tg.PrivacyValueDisallowContacts{})
		case domain.PrivacyRuleDisallowAll:
			out = append(out, &tg.PrivacyValueDisallowAll{})
		case domain.PrivacyRuleDisallowUsers:
			out = append(out, &tg.PrivacyValueDisallowUsers{Users: append([]int64(nil), rule.UserIDs...)})
		case domain.PrivacyRuleAllowChatParticipants:
			out = append(out, &tg.PrivacyValueAllowChatParticipants{Chats: append([]int64(nil), rule.ChatIDs...)})
		case domain.PrivacyRuleDisallowChatParticipants:
			out = append(out, &tg.PrivacyValueDisallowChatParticipants{Chats: append([]int64(nil), rule.ChatIDs...)})
		case domain.PrivacyRuleAllowCloseFriends:
			out = append(out, &tg.PrivacyValueAllowCloseFriends{})
		case domain.PrivacyRuleAllowPremium:
			out = append(out, &tg.PrivacyValueAllowPremium{})
		case domain.PrivacyRuleAllowBots:
			out = append(out, &tg.PrivacyValueAllowBots{})
		case domain.PrivacyRuleDisallowBots:
			out = append(out, &tg.PrivacyValueDisallowBots{})
		}
	}
	return out
}

func privacyRuleUserIDs(rules []domain.PrivacyRule) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0)
	for _, rule := range rules {
		for _, id := range rule.UserIDs {
			if id == 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func privacyErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrPrivacyKeyInvalid):
		return privacyKeyInvalidErr()
	case errors.Is(err, domain.ErrPrivacyRuleInvalid):
		return privacyValueInvalidErr()
	default:
		return internalErr()
	}
}

type accountReactionSettingsService interface {
	accountReactionSettingsReader
	SetReactionsNotifySettings(ctx context.Context, userID int64, settings domain.ReactionsNotifySettings) (domain.AccountReactionSettings, error)
}

// accountSettingsService 是账号级单例设置（全局隐私/TTL/敏感内容/注册通知）持久化的
// 可选扩展，由 *app/account.Service 实现。未接通时各 handler 回落历史回显默认。
type accountSettingsService interface {
	GetAccountSettings(ctx context.Context, userID int64) (domain.AccountSettings, error)
	SetGlobalPrivacy(ctx context.Context, userID int64, privacy domain.GlobalPrivacy) (domain.AccountSettings, error)
	SetAccountTTL(ctx context.Context, userID int64, days int) (domain.AccountSettings, error)
	SetSensitiveContent(ctx context.Context, userID int64, enabled bool) (domain.AccountSettings, error)
	SetContactSignUpSilent(ctx context.Context, userID int64, silent bool) (domain.AccountSettings, error)
}

// accountSettingsSvc 取账号设置服务（未接通返回 nil）。
func (r *Router) accountSettingsSvc() (accountSettingsService, bool) {
	svc, ok := r.deps.Account.(accountSettingsService)
	return svc, ok
}

func tgGlobalPrivacySettings(gp domain.GlobalPrivacy) *tg.GlobalPrivacySettings {
	out := &tg.GlobalPrivacySettings{
		ArchiveAndMuteNewNoncontactPeers: gp.ArchiveAndMuteNewNoncontactPeers,
		KeepArchivedUnmuted:              gp.KeepArchivedUnmuted,
		KeepArchivedFolders:              gp.KeepArchivedFolders,
		HideReadMarks:                    gp.HideReadMarks,
		NewNoncontactPeersRequirePremium: gp.NewNoncontactPeersRequirePremium,
		DisplayGiftsButton:               gp.DisplayGiftsButton,
	}
	if gp.NoncontactPeersPaidStars > 0 {
		out.SetNoncontactPeersPaidStars(gp.NoncontactPeersPaidStars)
	}
	return out
}

func domainGlobalPrivacy(settings tg.GlobalPrivacySettings) domain.GlobalPrivacy {
	gp := domain.GlobalPrivacy{
		ArchiveAndMuteNewNoncontactPeers: settings.ArchiveAndMuteNewNoncontactPeers,
		KeepArchivedUnmuted:              settings.KeepArchivedUnmuted,
		KeepArchivedFolders:              settings.KeepArchivedFolders,
		HideReadMarks:                    settings.HideReadMarks,
		NewNoncontactPeersRequirePremium: settings.NewNoncontactPeersRequirePremium,
		DisplayGiftsButton:               settings.DisplayGiftsButton,
	}
	if v, ok := settings.GetNoncontactPeersPaidStars(); ok && v > 0 {
		gp.NoncontactPeersPaidStars = v
	}
	return gp
}

// tgContentSettings 把敏感内容开关投影为 account.contentSettings。
// SensitiveCanChange 恒置位：telesrv 不做地区年龄门控，客户端始终可切换
// （直接赋值 flag-bool 字段，EncodeBare 自动 SetFlags）。
func tgContentSettings(enabled bool) *tg.AccountContentSettings {
	return &tg.AccountContentSettings{
		SensitiveEnabled:   enabled,
		SensitiveCanChange: true,
	}
}

func (r *Router) onAccountGetReactionsNotifySettings(ctx context.Context) (*tg.ReactionsNotifySettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if svc, ok := r.deps.Account.(accountReactionSettingsService); ok {
		settings, err := svc.GetReactionSettings(ctx, userID)
		if err != nil {
			return nil, internalErr()
		}
		return tgReactionsNotifySettings(settings.Notify), nil
	}
	return tgReactionsNotifySettings(domain.DefaultAccountReactionSettings().Notify), nil
}

func (r *Router) onAccountSetReactionsNotifySettings(ctx context.Context, settings tg.ReactionsNotifySettings) (*tg.ReactionsNotifySettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	notify := domainReactionsNotifySettings(settings)
	if svc, ok := r.deps.Account.(accountReactionSettingsService); ok {
		next, err := svc.SetReactionsNotifySettings(ctx, userID, notify)
		if err != nil {
			return nil, internalErr()
		}
		return tgReactionsNotifySettings(next.Notify), nil
	}
	return tgReactionsNotifySettings(notify), nil
}

func domainReactionsNotifySettings(settings tg.ReactionsNotifySettings) domain.ReactionsNotifySettings {
	return domain.ReactionsNotifySettings{
		MessagesFrom:  domainReactionNotifyFrom(settings.GetMessagesNotifyFrom),
		StoriesFrom:   domainReactionNotifyFrom(settings.GetStoriesNotifyFrom),
		PollVotesFrom: domainReactionNotifyFrom(settings.GetPollVotesNotifyFrom),
		ShowPreviews:  settings.ShowPreviews,
	}
}

func domainReactionNotifyFrom(get func() (tg.ReactionNotificationsFromClass, bool)) domain.ReactionNotifyFrom {
	if get == nil {
		return domain.ReactionNotifyFromNone
	}
	value, ok := get()
	if !ok || value == nil {
		return domain.ReactionNotifyFromNone
	}
	switch value.(type) {
	case *tg.ReactionNotificationsFromAll:
		return domain.ReactionNotifyFromAll
	case *tg.ReactionNotificationsFromContacts:
		return domain.ReactionNotifyFromContacts
	default:
		return domain.ReactionNotifyFromNone
	}
}

func tgReactionsNotifySettings(settings domain.ReactionsNotifySettings) *tg.ReactionsNotifySettings {
	out := &tg.ReactionsNotifySettings{
		Sound:        &tg.NotificationSoundDefault{},
		ShowPreviews: settings.ShowPreviews,
	}
	if value := tgReactionNotifyFrom(settings.MessagesFrom); value != nil {
		out.SetMessagesNotifyFrom(value)
	}
	if value := tgReactionNotifyFrom(settings.StoriesFrom); value != nil {
		out.SetStoriesNotifyFrom(value)
	}
	if value := tgReactionNotifyFrom(settings.PollVotesFrom); value != nil {
		out.SetPollVotesNotifyFrom(value)
	}
	return out
}

func tgReactionNotifyFrom(value domain.ReactionNotifyFrom) tg.ReactionNotificationsFromClass {
	switch value {
	case domain.ReactionNotifyFromAll:
		return &tg.ReactionNotificationsFromAll{}
	case domain.ReactionNotifyFromContacts:
		return &tg.ReactionNotificationsFromContacts{}
	default:
		return nil
	}
}

func (r *Router) onAccountUpdateProfile(ctx context.Context, req *tg.AccountUpdateProfileRequest) (tg.UserClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	svc, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return nil, internalErr()
	}
	firstName, hasFirstName := req.GetFirstName()
	lastName, hasLastName := req.GetLastName()
	about, hasAbout := req.GetAbout()
	u, err := svc.UpdateProfile(ctx, userID, domain.UserProfileUpdate{
		FirstName:    firstName,
		HasFirstName: hasFirstName,
		LastName:     lastName,
		HasLastName:  hasLastName,
		About:        about,
		HasAbout:     hasAbout,
	})
	if err != nil {
		return nil, profileErr(err)
	}
	r.invalidateRPCProjectionForUser(u.ID)
	r.pushUsernameUpdate(ctx, u)
	return r.tgSelfUser(u), nil
}

func (r *Router) onAccountCheckUsername(ctx context.Context, username string) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return false, internalErr()
	}
	okUsername, err := svc.CheckUsername(ctx, userID, username)
	if err != nil {
		return false, usernameErr(err)
	}
	return okUsername, nil
}

func (r *Router) onAccountUpdateUsername(ctx context.Context, username string) (tg.UserClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	svc, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return nil, internalErr()
	}
	u, err := svc.UpdateUsername(ctx, userID, username)
	if err != nil {
		return nil, usernameErr(err)
	}
	r.invalidateRPCProjectionForUser(u.ID)
	r.pushUsernameUpdate(ctx, u)
	return r.tgSelfUser(u), nil
}

// onAccountUpdateBirthday 持久化资料页生日（account.updateBirthday）。birthday 缺省即清除；
// 月/日/年非法返回 BIRTHDAY_INVALID。生日落在 userFull（按隐私 PrivacyKeyBirthday 对外裁剪）。
// 写入后推 updateUser 信号给本人其它在线 session，促使已加载 full profile 的客户端重拉。
func (r *Router) onAccountUpdateBirthday(ctx context.Context, req *tg.AccountUpdateBirthdayRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return false, internalErr()
	}
	var birthday domain.Birthday
	if b, ok := req.GetBirthday(); ok {
		birthday = domain.Birthday{Day: b.Day, Month: b.Month}
		if year, ok := b.GetYear(); ok {
			birthday.Year = year
		}
	}
	u, err := svc.UpdateBirthday(ctx, userID, birthday)
	if err != nil {
		if errors.Is(err, domain.ErrBirthdayInvalid) {
			return false, birthdayInvalidErr()
		}
		return false, internalErr()
	}
	r.invalidateRPCProjectionForUser(u.ID)
	r.pushSelfUserChangedUpdate(ctx, u)
	return true, nil
}

// onAccountUpdatePersonalChannel 设置/清除资料页「个人频道」（account.updatePersonalChannel）。
// inputChannelEmpty 清除；否则解析频道并要求调用者为其创建者/管理员（与官方 picker 取
// getAdminedPublicChannels 一致）。频道与最新一帖在 getFullUser 时按 id 投影进 userFull + chats。
func (r *Router) onAccountUpdatePersonalChannel(ctx context.Context, channel tg.InputChannelClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return false, internalErr()
	}
	var channelID int64
	switch channel.(type) {
	case *tg.InputChannelEmpty:
		channelID = 0
	default:
		if r.deps.Channels == nil {
			return false, channelInvalidErr(domain.ErrChannelInvalid)
		}
		view, err := r.channelFullReadView(ctx, userID, channel)
		if err != nil {
			return false, err
		}
		if !channelMemberIsAdmin(view.Self) {
			return false, channelInvalidErr(domain.ErrChannelAdminRequired)
		}
		channelID = view.Channel.ID
	}
	u, err := svc.UpdatePersonalChannel(ctx, userID, channelID)
	if err != nil {
		return false, internalErr()
	}
	r.invalidateRPCProjectionForUser(u.ID)
	return true, nil
}

// onAccountUpdateEmojiStatus 持久化用户自定义 emoji status（premium 专属）。
// emojiStatusEmpty 与未支持的 collectible 类型按清除处理（collectible 依赖
// Stars 礼物模型，范围外，记兼容矩阵）；变更经 updateUserEmojiStatus 推给
// 本人全部在线 session（self user 对象同时携带最新 emoji_status 字段）。
func (r *Router) onAccountUpdateEmojiStatus(ctx context.Context, status tg.EmojiStatusClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	svc, ok := r.deps.Users.(UserPremiumService)
	if !ok {
		return true, nil // 服务未接通（精简测试装配）时保持旧 stub 语义
	}
	var documentID int64
	var until int
	if s, ok := status.(*tg.EmojiStatus); ok {
		documentID = s.DocumentID
		if v, ok := s.GetUntil(); ok {
			until = v
		}
	}
	u, err := svc.UpdateEmojiStatus(ctx, userID, documentID, until)
	if err != nil {
		if errors.Is(err, domain.ErrPremiumRequired) {
			return false, tgerr400("PREMIUM_ACCOUNT_REQUIRED")
		}
		return false, internalErr()
	}
	r.invalidateRPCProjectionForUser(u.ID)
	r.pushUserUpdates(ctx, u.ID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUserEmojiStatus{
			UserID:      u.ID,
			EmojiStatus: tgUserEmojiStatus(u, r.clock.Now().Unix()),
		}},
		Users: []tg.UserClass{r.tgSelfUser(u)},
		Date:  int(r.clock.Now().Unix()),
	})
	return true, nil
}

// onAccountUpdateColor 持久化当前用户的消息 accent 或资料页背景色。
// 普通 peerColor 可清除（color flag absent）、可显式设置 color=0；collectible
// 颜色依赖礼物资产模型，当前阶段按范围外能力拒绝并记录在兼容矩阵。
func (r *Router) onAccountUpdateColor(ctx context.Context, req *tg.AccountUpdateColorRequest) (bool, error) {
	if req == nil {
		return false, inputConstructorInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	color, err := domainPeerColorFromAccountUpdate(req)
	if err != nil {
		return false, err
	}
	svc, ok := r.deps.Users.(UserColorService)
	if !ok {
		return true, nil // 精简测试装配或旧 wiring 未接入时保持兼容 stub 语义。
	}
	u, err := svc.UpdateColor(ctx, userID, req.GetForProfile(), color)
	if err != nil {
		return false, internalErr()
	}
	r.invalidateRPCProjectionForUser(u.ID)
	r.pushUserUpdates(ctx, u.ID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: u.ID}},
		Users:   []tg.UserClass{r.tgSelfUser(u)},
		Date:    int(r.clock.Now().Unix()),
	})
	return true, nil
}

func domainPeerColorFromAccountUpdate(req *tg.AccountUpdateColorRequest) (domain.PeerColor, error) {
	input, ok := req.GetColor()
	if !ok {
		return domain.PeerColor{}, nil
	}
	switch color := input.(type) {
	case *tg.PeerColor:
		value, hasColor := color.GetColor()
		if hasColor && !accountUpdateColorIDAllowed(req.GetForProfile(), value) {
			return domain.PeerColor{}, colorInvalidErr()
		}
		backgroundEmojiID, _ := color.GetBackgroundEmojiID()
		if backgroundEmojiID < 0 {
			return domain.PeerColor{}, documentInvalidErr()
		}
		return domain.PeerColor{
			HasColor:          hasColor,
			Color:             value,
			BackgroundEmojiID: backgroundEmojiID,
		}, nil
	case *tg.InputPeerColorCollectible, *tg.PeerColorCollectible:
		return domain.PeerColor{}, colorInvalidErr()
	default:
		return domain.PeerColor{}, inputConstructorInvalidErr()
	}
}

func accountUpdateColorIDAllowed(forProfile bool, colorID int) bool {
	if forProfile {
		return tdesktop.IsPeerProfileColorID(colorID)
	}
	return tdesktop.IsPeerColorID(colorID)
}

// onAccountGetDefaultEmojiStatuses 返回默认 emoji status 列表，与
// inputStickerSetEmojiDefaultStatuses 系统集同源同序（选择器主体由该系统集
// 经 messages.getStickerSet 填充，这里是顶部"默认状态"行）。资源未合成时回退
// 空 stub，与其它 sticker 资源 RPC 的降级路径一致。
func (r *Router) onAccountGetDefaultEmojiStatuses(ctx context.Context, hash int64) (tg.AccountEmojiStatusesClass, error) {
	if r.deps.Files == nil {
		return tdesktop.DefaultEmojiStatuses(), nil
	}
	set, _, found, err := r.deps.Files.ResolveStickerSet(ctx, domain.StickerSetRef{
		Kind:      domain.StickerSetRefBySystem,
		SystemKey: domain.StickerSetSystemKeyEmojiDefaultStatuses,
	})
	if err != nil {
		return nil, internalErr()
	}
	if !found || len(set.DocumentIDs) == 0 {
		return tdesktop.DefaultEmojiStatuses(), nil
	}
	catalogHash := mediaCatalogHash(set.DocumentIDs)
	if hash == catalogHash {
		return &tg.AccountEmojiStatusesNotModified{}, nil
	}
	statuses := make([]tg.EmojiStatusClass, 0, len(set.DocumentIDs))
	for _, id := range set.DocumentIDs {
		statuses = append(statuses, &tg.EmojiStatus{DocumentID: id})
	}
	return &tg.AccountEmojiStatuses{Hash: catalogHash, Statuses: statuses}, nil
}

func (r *Router) pushUsernameUpdate(ctx context.Context, u domain.User) {
	if u.ID == 0 {
		return
	}
	r.pushUserUpdates(ctx, u.ID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUserName{
			UserID:    u.ID,
			FirstName: u.FirstName,
			LastName:  u.LastName,
			Usernames: tgUsernames(u.Username),
		}},
		Users: []tg.UserClass{r.tgSelfUser(u)},
		Date:  int(r.clock.Now().Unix()),
	})
}

func (r *Router) pushSelfUserChangedUpdate(ctx context.Context, u domain.User) {
	if u.ID == 0 {
		return
	}
	r.pushUserUpdates(ctx, u.ID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: u.ID}},
		Users:   []tg.UserClass{r.tgSelfUser(u)},
		Date:    int(r.clock.Now().Unix()),
	})
}

func tgAuthorization(a domain.Authorization, currentAuthKeyID [8]byte, now int) tg.Authorization {
	created := int(a.CreatedAt.Unix())
	if created == 0 {
		created = now
	}
	active := int(a.ActiveAt.Unix())
	if active == 0 {
		active = created
	}
	return tg.Authorization{
		Current:       a.AuthKeyID == currentAuthKeyID,
		OfficialApp:   true,
		Hash:          a.Hash,
		DeviceModel:   a.DeviceModel,
		Platform:      a.Platform,
		SystemVersion: a.SystemVersion,
		APIID:         a.APIID,
		AppName:       "Telegram Desktop",
		AppVersion:    a.AppVersion,
		DateCreated:   created,
		DateActive:    active,
		IP:            a.IP,
		Country:       "Unknown",
		Region:        "Unknown",
	}
}

func (r *Router) onAccountGetDefaultProfilePhotoEmojis(ctx context.Context, hash int64) (tg.EmojiListClass, error) {
	if r.deps.Files == nil {
		return tdesktop.DefaultGroupPhotoEmojis(), nil
	}
	const maxDefaultProfilePhotoEmojiIDs = 64
	ids, err := r.defaultProfilePhotoEmojiDocumentIDs(ctx, maxDefaultProfilePhotoEmojiIDs)
	if err != nil {
		return nil, internalErr()
	}
	listHash := emojiDocumentIDListHash(ids)
	if hash != 0 && hash == listHash {
		return &tg.EmojiListNotModified{}, nil
	}
	return &tg.EmojiList{Hash: listHash, DocumentID: ids}, nil
}

func (r *Router) onAccountGetDefaultBackgroundEmojis(ctx context.Context, hash int64) (tg.EmojiListClass, error) {
	if r.deps.Files == nil {
		return tdesktop.DefaultGroupPhotoEmojis(), nil
	}
	ids, err := defaultBackgroundEmojiDocumentIDs(ctx, r.deps.Files)
	if err != nil {
		return nil, internalErr()
	}
	if len(ids) == 0 {
		return tdesktop.DefaultGroupPhotoEmojis(), nil
	}
	listHash := emojiDocumentIDListHash(ids)
	if hash != 0 && hash == listHash {
		return &tg.EmojiListNotModified{}, nil
	}
	return &tg.EmojiList{Hash: listHash, DocumentID: ids}, nil
}

func defaultBackgroundEmojiDocumentIDs(ctx context.Context, files FilesService) ([]int64, error) {
	return statusPackEmojiDocumentIDs(ctx, files, 0)
}

func statusPackEmojiDocumentIDs(ctx context.Context, files FilesService, limit int) ([]int64, error) {
	set, _, found, err := files.ResolveStickerSet(ctx, domain.StickerSetRef{
		Kind:      domain.StickerSetRefByShortName,
		ShortName: "StatusPack",
	})
	if err != nil || !found {
		return nil, err
	}
	return uniquePositiveDocumentIDs(set.DocumentIDs, limit), nil
}

func (r *Router) defaultProfilePhotoEmojiDocumentIDs(ctx context.Context, limit int) ([]int64, error) {
	ids, err := profilePhotoEmojiDocumentIDsFromRef(ctx, r.deps.Files, domain.StickerSetRef{
		Kind:      domain.StickerSetRefByShortName,
		ShortName: "TelesrvDefaultStatuses",
	}, limit)
	if err != nil || len(ids) > 0 {
		return ids, err
	}
	ids, err = defaultProfilePhotoEmojiDocumentIDsFromKind(ctx, r.deps.Files, domain.StickerSetKindSystem, limit, func(set domain.StickerSet) bool {
		return set.SystemKey == "animated_emoji"
	})
	if err != nil || len(ids) > 0 {
		return ids, err
	}
	ids, err = defaultProfilePhotoEmojiDocumentIDsFromKind(ctx, r.deps.Files, domain.StickerSetKindEmoji, limit, nil)
	if err != nil || len(ids) > 0 {
		return ids, err
	}
	return profilePhotoEmojiDocumentIDsFromRef(ctx, r.deps.Files, domain.StickerSetRef{
		Kind:      domain.StickerSetRefByShortName,
		ShortName: "StatusPack",
	}, limit)
}

func profilePhotoEmojiDocumentIDsFromRef(ctx context.Context, files FilesService, ref domain.StickerSetRef, limit int) ([]int64, error) {
	set, docs, found, err := files.ResolveStickerSet(ctx, ref)
	if err != nil || !found {
		return nil, err
	}
	return profilePhotoEmojiDocumentIDs(set.DocumentIDs, docs, limit), nil
}

func defaultProfilePhotoEmojiDocumentIDsFromKind(
	ctx context.Context,
	files FilesService,
	kind domain.StickerSetKind,
	limit int,
	allow func(domain.StickerSet) bool,
) ([]int64, error) {
	sets, err := files.ListStickerSets(ctx, kind)
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]struct{}, limit)
	ids := make([]int64, 0, limit)
	for _, set := range sets {
		if allow != nil && !allow(set) {
			continue
		}
		candidateIDs := uniquePositiveDocumentIDs(set.DocumentIDs, limit)
		docs, err := files.GetDocuments(ctx, candidateIDs)
		if err != nil {
			return nil, err
		}
		for _, id := range profilePhotoEmojiDocumentIDs(candidateIDs, docs, limit) {
			if id == 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			if len(ids) >= limit {
				break
			}
		}
		if len(ids) >= limit {
			break
		}
	}
	return ids, nil
}

func profilePhotoEmojiDocumentIDs(setIDs []int64, docs []domain.Document, limit int) []int64 {
	textColorDocs := make(map[int64]bool, len(docs))
	for _, doc := range docs {
		if documentHasTextColorCustomEmoji(doc) {
			textColorDocs[doc.ID] = true
		}
	}
	out := make([]int64, 0, len(setIDs))
	seen := make(map[int64]struct{}, len(setIDs))
	for _, id := range setIDs {
		if id <= 0 || textColorDocs[id] {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func documentHasTextColorCustomEmoji(doc domain.Document) bool {
	for _, attr := range doc.Attributes {
		if attr.Kind == domain.DocAttrCustomEmoji && attr.TextColor {
			return true
		}
	}
	return false
}

func uniquePositiveDocumentIDs(ids []int64, limit int) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func emojiDocumentIDListHash(ids []int64) int64 {
	var h uint64 = 1469598103934665603
	for _, id := range ids {
		h ^= uint64(id)
		h *= 1099511628211
	}
	return int64(h & 0x7fffffffffffffff)
}

func usernameErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrUsernameInvalid):
		return usernameInvalidErr()
	case errors.Is(err, domain.ErrUsernameOccupied):
		return usernameOccupiedErr()
	case errors.Is(err, domain.ErrUsernameNotOccupied):
		return usernameNotOccupiedErr()
	default:
		return internalErr()
	}
}

func profileErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrFirstNameInvalid):
		return firstNameInvalidErr()
	case errors.Is(err, domain.ErrAboutTooLong):
		return aboutTooLongErr()
	default:
		return internalErr()
	}
}
