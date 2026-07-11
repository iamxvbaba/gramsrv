package rpc

import (
	"errors"
	"fmt"

	"github.com/gotd/td/tgerr"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

// 本文件集中构造所有 rpc_error：error_code 遵循 Telegram 约定（4xx 客户端 / 5xx 服务端），
// error_message 为 SCREAMING_SNAKE_CASE。统一用 tgerr.New，由它从 message 解析 Type/Argument
// （正确处理 FLOOD_WAIT_X 等带参数错误），避免手写 &tgerr.Error{} 时重复且易错。

// internalErr 是兜底的服务端错误。
func internalErr() error { return tgerr.New(500, "INTERNAL_SERVER_ERROR") }

// notImplementedErr 表示 RPC 未实现（落兼容矩阵），让客户端继续运行而非断连。
func notImplementedErr() error { return tgerr.New(500, "NOT_IMPLEMENTED") }

// wrapperTooDeepErr 表示 invokeWithLayer/initConnection 等 wrapper 嵌套过深。
func wrapperTooDeepErr() error { return tgerr.New(400, "WRAPPER_TOO_DEEP") }

// inputConstructorInvalidErr 表示客户端传入的 TL 构造器不在当前 RPC 接受范围内。
func inputConstructorInvalidErr() error { return tgerr.New(400, "INPUT_CONSTRUCTOR_INVALID") }

// folderIDInvalidErr 表示客户端传入多个 folder peer 或非法 folder。
func folderIDInvalidErr() error { return tgerr.New(400, "FOLDER_ID_INVALID") }

func pinnedTooMuchErr() error { return tgerr.New(400, "PINNED_DIALOGS_TOO_MUCH") }

// savedPinnedTooMuchErr 是收藏夹子会话置顶超限（messages.toggleSavedDialogPin /
// reorderPinnedSavedDialogs，官方错误码 PINNED_TOO_MUCH）。
func savedPinnedTooMuchErr() error { return tgerr.New(400, "PINNED_TOO_MUCH") }

func filterIDInvalidErr() error { return tgerr.New(400, "FILTER_ID_INVALID") }

func filterIncludeEmptyErr() error { return tgerr.New(400, "FILTER_INCLUDE_EMPTY") }

func filterTitleEmptyErr() error { return tgerr.New(400, "FILTER_TITLE_EMPTY") }

func filterNotSupportedErr() error { return tgerr.New(400, "FILTER_NOT_SUPPORTED") }

func inviteSlugEmptyErr() error { return tgerr.New(400, "INVITE_SLUG_EMPTY") }

func inviteSlugExpiredErr() error { return tgerr.New(400, "INVITE_SLUG_EXPIRED") }

func invitesTooMuchErr() error { return tgerr.New(400, "INVITES_TOO_MUCH") }

func chatlistsTooMuchErr() error { return tgerr.New(400, "CHATLISTS_TOO_MUCH") }

func peersListEmptyErr() error { return tgerr.New(400, "PEERS_LIST_EMPTY") }

// peerIDInvalidErr 表示目标 peer 不存在或当前阶段不支持。
func peerIDInvalidErr() error { return tgerr.New(400, "PEER_ID_INVALID") }

func parentPeerInvalidErr() error { return tgerr.New(400, "PARENT_PEER_INVALID") }

func sendAsPeerInvalidErr() error { return tgerr.New(400, "SEND_AS_PEER_INVALID") }

func limitInvalidErr() error { return tgerr.New(400, "LIMIT_INVALID") }

func offsetInvalidErr() error { return tgerr.New(400, "OFFSET_INVALID") }

func platformInvalidErr() error { return tgerr.New(400, "PLATFORM_INVALID") }

func secondsInvalidErr() error { return tgerr.New(400, "SECONDS_INVALID") }

func ttlPeriodInvalidErr() error { return tgerr.New(400, "TTL_PERIOD_INVALID") }

func chatInvalidErr() error { return tgerr.New(500, "CHAT_INVALID") }

func addressInvalidErr() error { return tgerr.New(400, "ADDRESS_INVALID") }

func mediaInvalidErr() error { return tgerr.New(400, "MEDIA_INVALID") }

func mediaTypeInvalidErr() error { return tgerr.New(400, "MEDIA_TYPE_INVALID") }

func urlInvalidErr() error { return tgerr.New(400, "URL_INVALID") }

// 文件上传 / 下载相关错误。
func filePartInvalidErr() error      { return tgerr.New(400, "FILE_PART_INVALID") }
func filePartsInvalidErr() error     { return tgerr.New(400, "FILE_PARTS_INVALID") }
func filePartTooBigErr() error       { return tgerr.New(400, "FILE_PART_TOO_BIG") }
func fileReferenceInvalidErr() error { return tgerr.New(400, "FILE_REFERENCE_INVALID") }
func locationInvalidErr() error      { return tgerr.New(400, "LOCATION_INVALID") }
func fileIDInvalidErr() error        { return tgerr.New(400, "FILE_ID_INVALID") }
func documentInvalidErr() error      { return tgerr.New(400, "DOCUMENT_INVALID") }

func mediaEmptyErr() error { return tgerr.New(400, "MEDIA_EMPTY") }

func frozenMethodInvalidErr() error { return tgerr.New(400, "FROZEN_METHOD_INVALID") }

func photoInvalidErr() error { return tgerr.New(400, "PHOTO_INVALID") }

func stickersetInvalidErr() error { return tgerr.New(406, "STICKERSET_INVALID") }

// stickerInvalidErr 表示输入文档不是合法贴纸/GIF（faveSticker/saveRecentSticker/saveGif）。
func stickerInvalidErr() error { return tgerr.New(400, "STICKER_DOCUMENT_INVALID") }

// gifIDInvalidErr 表示 messages.saveGif 引用的文档不存在或不是规范 GIFv。
func gifIDInvalidErr() error { return tgerr.New(400, "GIF_ID_INVALID") }

func mediaCaptionTooLongErr() error { return tgerr.New(400, "MEDIA_CAPTION_TOO_LONG") }

func replyMarkupInvalidErr() error { return tgerr.New(400, "REPLY_MARKUP_INVALID") }

// accessTokenInvalidErr 是 auth.importBotAuthorization 的 bot token 校验失败
// （含 revoke 后旧 token）。
func accessTokenInvalidErr() error { return tgerr.New(400, "ACCESS_TOKEN_INVALID") }

// importBotAuthorizationErr 把 bot 登录业务错误映射为 rpc_error。
func importBotAuthorizationErr(err error) error {
	if errors.Is(err, domain.ErrBotTokenInvalid) {
		return accessTokenInvalidErr()
	}
	return internalErr()
}

func shortcutInvalidErr() error { return tgerr.New(400, "SHORTCUT_INVALID") }

func effectIDInvalidErr() error { return tgerr.New(400, "EFFECT_ID_INVALID") }

func paymentUnsupportedErr() error { return tgerr.New(406, "PAYMENT_UNSUPPORTED") }

func balanceTooLowErr() error { return tgerr.New(400, "BALANCE_TOO_LOW") }

func starsAmountInvalidErr() error { return tgerr.New(400, "STARS_AMOUNT_INVALID") }

func starsFormAmountMismatchErr() error { return tgerr.New(406, "STARS_FORM_AMOUNT_MISMATCH") }

func formIDEmptyErr() error { return tgerr.New(400, "FORM_ID_EMPTY") }

func suggestedPostPeerInvalidErr() error { return tgerr.New(400, "SUGGESTED_POST_PEER_INVALID") }

func storyIDInvalidErr() error { return tgerr.New(400, "STORY_ID_INVALID") }

func storyIDEmptyErr() error { return tgerr.New(400, "STORY_ID_EMPTY") }

func storyNotModifiedErr() error { return tgerr.New(400, "STORY_NOT_MODIFIED") }

func storyPeriodInvalidErr() error { return tgerr.New(400, "STORY_PERIOD_INVALID") }

func replyToMonoforumPeerInvalidErr() error { return tgerr.New(400, "REPLY_TO_MONOFORUM_PEER_INVALID") }

func optionsTooMuchErr() error { return tgerr.New(400, "OPTIONS_TOO_MUCH") }

func optionInvalidErr() error { return tgerr.New(400, "OPTION_INVALID") }

func pollOptionInvalidErr() error { return tgerr.New(400, "POLL_OPTION_INVALID") }

func pollAnswerInvalidErr() error { return tgerr.New(400, "POLL_ANSWER_INVALID") }

func quizCorrectAnswersInvalidErr() error { return tgerr.New(400, "QUIZ_CORRECT_ANSWERS_INVALID") }

func pollClosedErr() error { return tgerr.New(400, "MESSAGE_POLL_CLOSED") }

func revoteNotAllowedErr() error { return tgerr.New(400, "REVOTE_NOT_ALLOWED") }

// pollVoteRequiredErr 用于 getPollVotes 防御路径（非公开投票/未投票即查投票人，官方同名错误）。
func pollVoteRequiredErr() error { return tgerr.New(403, "POLL_VOTE_REQUIRED") }

func reactionInvalidErr() error { return tgerr.New(400, "REACTION_INVALID") }

// emoticonInvalidErr 用于 InputMediaDice 等带 emoticon 的输入不在支持集合内。
func emoticonInvalidErr() error { return tgerr.New(400, "EMOTICON_INVALID") }

func themeInvalidErr() error { return tgerr.New(400, "THEME_INVALID") }

// themeErr 把自定义云主题(account.createTheme 等)的 domain 错误映射为 tgerr。
func themeErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrThemeFormatInvalid):
		return tgerr.New(400, "THEME_FORMAT_INVALID")
	case errors.Is(err, domain.ErrThemeSlugTaken):
		return tgerr.New(400, "THEME_SLUG_INVALID")
	case errors.Is(err, domain.ErrThemeInvalid), errors.Is(err, domain.ErrThemeNotFound):
		return tgerr.New(400, "THEME_INVALID")
	default:
		return internalErr()
	}
}

func colorInvalidErr() error { return tgerr.New(400, "COLOR_INVALID") }

func privacyKeyInvalidErr() error { return tgerr.New(400, "PRIVACY_KEY_INVALID") }

func privacyValueInvalidErr() error { return tgerr.New(400, "PRIVACY_VALUE_INVALID") }

func todoItemsEmptyErr() error { return tgerr.New(400, "TODO_ITEMS_EMPTY") }

func todoNotModifiedErr() error { return tgerr.New(400, "TODO_NOT_MODIFIED") }

func searchQueryEmptyErr() error { return tgerr.New(400, "SEARCH_QUERY_EMPTY") }

func queryTooShortErr() error { return tgerr.New(400, "QUERY_TOO_SHORT") }

func usernameInvalidErr() error { return tgerr.New(400, "USERNAME_INVALID") }

func usernameOccupiedErr() error { return tgerr.New(400, "USERNAME_OCCUPIED") }

func usernameNotOccupiedErr() error { return tgerr.New(400, "USERNAME_NOT_OCCUPIED") }

func usernameNotModifiedErr() error { return tgerr.New(400, "USERNAME_NOT_MODIFIED") }

func phoneNotOccupiedErr() error { return tgerr.New(400, "PHONE_NOT_OCCUPIED") }

func dcIDInvalidErr() error { return tgerr.New(400, "DC_ID_INVALID") }

func authTokenInvalidErr() error { return tgerr.New(400, "AUTH_TOKEN_INVALID") }

func authTokenExpiredErr() error { return tgerr.New(400, "AUTH_TOKEN_EXPIRED") }

func authTokenAlreadyAcceptedErr() error { return tgerr.New(400, "AUTH_TOKEN_ALREADY_ACCEPTED") }

func authTokenExceptionErr() error { return tgerr.New(400, "AUTH_TOKEN_EXCEPTION") }

func userIDInvalidErr() error { return tgerr.New(400, "USER_ID_INVALID") }

func usersTooFewErr() error { return tgerr.New(400, "USERS_TOO_FEW") }

func firstNameInvalidErr() error { return tgerr.New(400, "FIRSTNAME_INVALID") }

func aboutTooLongErr() error { return tgerr.New(400, "ABOUT_TOO_LONG") }

func birthdayInvalidErr() error { return tgerr.New(400, "BIRTHDAY_INVALID") }

func contactIDInvalidErr() error { return tgerr.New(400, "CONTACT_ID_INVALID") }

func contactNameEmptyErr() error { return tgerr.New(400, "CONTACT_NAME_EMPTY") }

func contactReqMissingErr() error { return tgerr.New(400, "CONTACT_REQ_MISSING") }

// messageEmptyErr 表示发送空文本。
func messageEmptyErr() error { return tgerr.New(400, "MESSAGE_EMPTY") }

// messageTooLongErr 表示文本超出当前阶段限制。
func messageTooLongErr() error { return tgerr.New(400, "MESSAGE_TOO_LONG") }

func entitiesTooLongErr() error { return tgerr.New(400, "ENTITIES_TOO_LONG") }

func entityBoundsInvalidErr() error { return tgerr.New(400, "ENTITY_BOUNDS_INVALID") }

func messageIDInvalidErr() error { return tgerr.New(400, "MESSAGE_ID_INVALID") }

func msgIDInvalidErr() error { return tgerr.New(400, "MSG_ID_INVALID") }

func messageAuthorRequiredErr() error { return tgerr.New(403, "MESSAGE_AUTHOR_REQUIRED") }

func messageNotModifiedErr() error { return tgerr.New(400, "MESSAGE_NOT_MODIFIED") }

func messageEditForbiddenErr() error { return tgerr.New(403, "EDIT_MESSAGES_FORBIDDEN") }

func messageNotReadYetErr() error { return tgerr.New(400, "MESSAGE_NOT_READ_YET") }

func scoreInvalidErr() error { return tgerr.New(400, "SCORE_INVALID") }

func sessionPasswordNeededErr() error { return tgerr.New(401, "SESSION_PASSWORD_NEEDED") }
func passwordHashInvalidErr() error   { return tgerr.New(400, "PASSWORD_HASH_INVALID") }
func srpIDInvalidErr() error          { return tgerr.New(400, "SRP_ID_INVALID") }
func srpPasswordChangedErr() error    { return tgerr.New(400, "SRP_PASSWORD_CHANGED") }
func newSettingsInvalidErr() error    { return tgerr.New(400, "NEW_SETTINGS_INVALID") }
func newSaltInvalidErr() error        { return tgerr.New(400, "NEW_SALT_INVALID") }
func emailInvalidErr() error          { return tgerr.New(400, "EMAIL_INVALID") }
func emailNotAllowedErr() error       { return tgerr.New(400, "EMAIL_NOT_ALLOWED") }
func emailCodeInvalidErr() error      { return tgerr.New(400, "CODE_INVALID") }
func passwordRecoveryNAErr() error    { return tgerr.New(400, "PASSWORD_RECOVERY_NA") }

func replyMessageIDInvalidErr() error { return tgerr.New(400, "REPLY_MESSAGE_ID_INVALID") }

func chatForwardsRestrictedErr() error { return tgerr.New(400, "CHAT_FORWARDS_RESTRICTED") }

func inputRequestInvalidErr() error { return tgerr.New(400, "INPUT_REQUEST_INVALID") }

func inputRequestTooLongErr() error { return tgerr.New(400, "INPUT_REQUEST_TOO_LONG") }

func persistentTimestampInvalidErr() error { return tgerr.New(400, "PERSISTENT_TIMESTAMP_INVALID") }

func channelForumMissingErr() error { return tgerr.New(400, "CHANNEL_FORUM_MISSING") }

func topicTitleEmptyErr() error { return tgerr.New(400, "TOPIC_TITLE_EMPTY") }
func topicIDInvalidErr() error  { return tgerr.New(400, "TOPIC_ID_INVALID") }

// randomIDEmptyErr 表示发送消息缺少 random_id。
func randomIDEmptyErr() error { return tgerr.New(400, "RANDOM_ID_EMPTY") }

// randomIDDuplicateErr 表示同一发送者重复使用 random_id，但请求载荷与首次
// 成功发送不一致。Layer 227 为该错误定义的 code 是 500。
func randomIDDuplicateErr() error { return tgerr.New(500, "RANDOM_ID_DUPLICATE") }

// scheduleDateInvalidErr 表示当前阶段不支持定时消息。
func scheduleDateInvalidErr() error { return tgerr.New(400, "SCHEDULE_DATE_INVALID") }

// floodWaitErr 表示触发写操作限流。
func floodWaitErr(seconds int) error {
	if seconds <= 0 {
		seconds = 1
	}
	return tgerr.New(420, fmt.Sprintf("FLOOD_WAIT_%d", seconds))
}

// phoneNumberInvalidErr 表示手机号为空或格式非法（auth.sendCode/signIn/signUp）。
func phoneNumberInvalidErr() error { return tgerr.New(406, "PHONE_NUMBER_INVALID") }

func phoneNumberOccupiedErr() error { return tgerr.New(400, "PHONE_NUMBER_OCCUPIED") }
func phoneCodeEmptyErr() error      { return tgerr.New(400, "PHONE_CODE_EMPTY") }
func phoneCodeInvalidErr() error    { return tgerr.New(400, "PHONE_CODE_INVALID") }
func phoneCodeExpiredErr() error    { return tgerr.New(400, "PHONE_CODE_EXPIRED") }

func phoneChangeErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrPhoneNumberInvalid):
		return phoneNumberInvalidErr()
	case errors.Is(err, domain.ErrPhoneNumberOccupied):
		return phoneNumberOccupiedErr()
	case errors.Is(err, domain.ErrPhoneCodeEmpty):
		return phoneCodeEmptyErr()
	case errors.Is(err, domain.ErrPhoneCodeInvalid):
		return phoneCodeInvalidErr()
	case errors.Is(err, domain.ErrPhoneCodeExpired):
		return phoneCodeExpiredErr()
	case errors.Is(err, domain.ErrPhoneChangeAuthInvalid):
		return authKeyUnregisteredErr()
	case errors.Is(err, domain.ErrPhoneChangeForbidden):
		return tgerr.New(400, "BOT_METHOD_INVALID")
	default:
		return internalErr()
	}
}

// authKeyUnregisteredErr 表示请求要求登录态而当前连接未授权。
func authKeyUnregisteredErr() error { return tgerr.New(401, "AUTH_KEY_UNREGISTERED") }
func botMethodInvalidErr() error    { return tgerr.New(400, "BOT_METHOD_INVALID") }

// 私聊通话（phone.*）错误；触发点见 internal/rpc/phone_calls.go 与 app/phone 错误映射。
func callPeerInvalidErr() error     { return tgerr.New(400, "CALL_PEER_INVALID") }
func callAlreadyAcceptedErr() error { return tgerr.New(400, "CALL_ALREADY_ACCEPTED") }
func callAlreadyDeclinedErr() error { return tgerr.New(400, "CALL_ALREADY_DECLINED") }
func callOccupyFailedErr() error    { return tgerr.New(400, "CALL_OCCUPY_FAILED") }
func callProtocolLayerInvalidErr() error {
	return tgerr.New(400, "CALL_PROTOCOL_LAYER_INVALID")
}
func callProtocolCompatLayerInvalidErr() error {
	return tgerr.New(400, "CALL_PROTOCOL_COMPAT_LAYER_INVALID")
}
func callProtocolFlagsInvalidErr() error {
	return tgerr.New(400, "CALL_PROTOCOL_FLAGS_INVALID")
}

func userIsBlockedErr() error         { return tgerr.New(400, "USER_IS_BLOCKED") }
func userPrivacyRestrictedErr() error { return tgerr.New(403, "USER_PRIVACY_RESTRICTED") }

// signalingDataInvalidErr 表示 phone.sendSignalingData 载荷超限或非法。
func signalingDataInvalidErr() error { return tgerr.New(400, "DATA_INVALID") }

// 群通话（group call）错误。
func groupCallInvalidErr() error          { return tgerr.New(400, "GROUPCALL_INVALID") }
func groupCallAlreadyDiscardedErr() error { return tgerr.New(400, "GROUPCALL_ALREADY_DISCARDED") }
func groupCallAlreadyStartedErr() error   { return tgerr.New(400, "GROUPCALL_ALREADY_STARTED") }
func groupCallForbiddenErr() error        { return tgerr.New(403, "GROUPCALL_FORBIDDEN") }
func publicChannelMissingErr() error      { return tgerr.New(403, "PUBLIC_CHANNEL_MISSING") }
func groupCallSSRCDuplicateErr() error {
	return tgerr.New(400, "GROUPCALL_SSRC_DUPLICATE_MUCH")
}
func groupCallJoinMissingErr() error { return tgerr.New(400, "GROUPCALL_JOIN_MISSING") }
func groupCallNotModifiedErr() error { return tgerr.New(400, "GROUPCALL_NOT_MODIFIED") }
func confWriteChainInvalidErr() error {
	return tgerr.New(400, "CONF_WRITE_CHAIN_INVALID")
}

// 私聊端对端加密（Secret Chat / encrypted chat）错误；触发点见
// internal/rpc/encrypted_chats.go 与 app/secretchat、domain 错误映射。
func encryptionIDInvalidErr() error       { return tgerr.New(400, "ENCRYPTION_ID_INVALID") }
func encryptionAlreadyAcceptedErr() error { return tgerr.New(400, "ENCRYPTION_ALREADY_ACCEPTED") }
func encryptionAlreadyDeclinedErr() error { return tgerr.New(400, "ENCRYPTION_ALREADY_DECLINED") }
func chatIDInvalidErr() error             { return tgerr.New(400, "CHAT_ID_INVALID") }
func dhGAInvalidErr() error               { return tgerr.New(400, "DH_G_A_INVALID") }
func maxDateInvalidErr() error            { return tgerr.New(400, "MAX_DATE_INVALID") }
func fileEmptyErr() error                 { return tgerr.New(400, "FILE_EMPTY") }

// signInErr 把登录业务错误映射为客户端可识别的 rpc_error。
func signInErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrPhoneNumberInvalid),
		errors.Is(err, auth.ErrSystemUserLoginForbidden):
		return phoneNumberInvalidErr()
	case errors.Is(err, auth.ErrCodeInvalid):
		return tgerr.New(400, "PHONE_CODE_INVALID")
	case errors.Is(err, auth.ErrCodeExpired):
		return tgerr.New(400, "PHONE_CODE_EXPIRED")
	case errors.Is(err, domain.ErrFirstNameInvalid):
		return firstNameInvalidErr()
	case errors.Is(err, domain.ErrSessionPasswordNeeded):
		return sessionPasswordNeededErr()
	default:
		return internalErr()
	}
}

// passkeyErr 映射 passkey(WebAuthn)错误。验证类失败统一返回 PASSKEY_INVALID,不泄漏
// 具体哪一步失败(防探测);非验证类(store/内部)返回 500。
func passkeyErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrSystemUserLoginForbidden):
		return tgerr.New(400, "PASSKEY_INVALID")
	case errors.Is(err, domain.ErrPasskeyChallengeInvalid):
		return tgerr.New(400, "PASSKEY_CHALLENGE_INVALID")
	case errors.Is(err, domain.ErrPasskeyInvalid),
		errors.Is(err, domain.ErrPasskeyNotFound),
		errors.Is(err, domain.ErrPasskeyUserHandleInvalid):
		return tgerr.New(400, "PASSKEY_INVALID")
	default:
		return internalErr()
	}
}

func passwordErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrPasswordHashInvalid):
		return passwordHashInvalidErr()
	case errors.Is(err, domain.ErrSRPIDInvalid):
		return srpIDInvalidErr()
	case errors.Is(err, domain.ErrSRPPasswordChanged):
		return srpPasswordChangedErr()
	case errors.Is(err, domain.ErrNewSettingsInvalid):
		return newSettingsInvalidErr()
	case errors.Is(err, domain.ErrNewSaltInvalid):
		return newSaltInvalidErr()
	case errors.Is(err, domain.ErrEmailInvalid):
		return emailInvalidErr()
	case errors.Is(err, domain.ErrEmailOccupied):
		return emailNotAllowedErr()
	case errors.Is(err, domain.ErrEmailNotAllowed):
		return emailNotAllowedErr()
	case errors.Is(err, domain.ErrEmailCodeInvalid):
		return emailCodeInvalidErr()
	case errors.Is(err, domain.ErrPasswordRecoveryNA):
		return passwordRecoveryNAErr()
	default:
		return internalErr()
	}
}

// bindTempAuthKeyErr 映射 PFS temp auth key 绑定错误。
func bindTempAuthKeyErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrEncryptedMessageInvalid):
		return tgerr.New(400, "ENCRYPTED_MESSAGE_INVALID")
	default:
		return internalErr()
	}
}
