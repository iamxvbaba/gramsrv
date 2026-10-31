package rpc

import (
	"errors"
	"fmt"

	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

// This file centralizes the construction of every rpc_error: error_code follows the
// Telegram convention (4xx client / 5xx server), and error_message is
// SCREAMING_SNAKE_CASE. Always use tgerr.New, which parses Type/Argument from the
// message (correctly handling parameterized errors like FLOOD_WAIT_X), avoiding the
// duplication and mistakes of hand-writing &tgerr.Error{}.

// internalErr is the fallback server error.
func internalErr() error { return tgerr.New(500, "INTERNAL_SERVER_ERROR") }

// notImplementedErr means the RPC is not implemented (recorded in the compatibility matrix), so the client keeps running instead of disconnecting.
func notImplementedErr() error { return tgerr.New(500, "NOT_IMPLEMENTED") }

// wrapperTooDeepErr means invokeWithLayer/initConnection etc. wrappers are nested too deeply.
func wrapperTooDeepErr() error { return tgerr.New(400, "WRAPPER_TOO_DEEP") }

// inputConstructorInvalidErr means the TL constructor sent by the client is not accepted by the current RPC.
func inputConstructorInvalidErr() error { return tgerr.New(400, "INPUT_CONSTRUCTOR_INVALID") }

// langCodeNotSupportedErr means the requested language code has no imported language pack.
func langCodeNotSupportedErr() error { return tgerr.New(400, "LANG_CODE_NOT_SUPPORTED") }

// langPackInvalidErr means the requested language pack directory does not exist.
func langPackInvalidErr() error { return tgerr.New(400, "LANG_PACK_INVALID") }

// folderIDInvalidErr means the client passed multiple folder peers or an invalid folder.
func folderIDInvalidErr() error { return tgerr.New(400, "FOLDER_ID_INVALID") }

func pinnedTooMuchErr() error { return tgerr.New(400, "PINNED_DIALOGS_TOO_MUCH") }

// savedPinnedTooMuchErr is the favorites sub-session pin limit exceeded
// (messages.toggleSavedDialogPin / reorderPinnedSavedDialogs; official error code
// PINNED_TOO_MUCH).
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

// peerIDInvalidErr means the target peer does not exist or is not supported at this stage.
func peerIDInvalidErr() error { return tgerr.New(400, "PEER_ID_INVALID") }

func fromPeerInvalidErr() error { return tgerr.New(400, "FROM_PEER_INVALID") }

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

func richMessageInvalidErr() error { return tgerr.New(400, "RICH_MESSAGE_INVALID") }

func richMessageTooLongErr() error { return tgerr.New(400, "RICH_MESSAGE_TOO_LONG") }

func richMessageDateInvalidErr() error { return tgerr.New(400, "RICH_MESSAGE_DATE_INVALID") }

func richMessageMediaUnsupportedErr() error {
	return tgerr.New(400, "RICH_MESSAGE_MEDIA_UNSUPPORTED")
}

func webpageMediaEmptyErr() error { return tgerr.New(400, "WEBPAGE_MEDIA_EMPTY") }

func mediaTypeInvalidErr() error { return tgerr.New(400, "MEDIA_TYPE_INVALID") }

func urlInvalidErr() error { return tgerr.New(400, "URL_INVALID") }

// File upload / download related errors.
func filePartInvalidErr() error      { return tgerr.New(400, "FILE_PART_INVALID") }
func filePartsInvalidErr() error     { return tgerr.New(400, "FILE_PARTS_INVALID") }
func filePartTooBigErr() error       { return tgerr.New(400, "FILE_PART_TOO_BIG") }
func fileReferenceInvalidErr() error { return tgerr.New(400, "FILE_REFERENCE_INVALID") }
func locationInvalidErr() error      { return tgerr.New(400, "LOCATION_INVALID") }
func fileIDInvalidErr() error        { return tgerr.New(400, "FILE_ID_INVALID") }
func documentInvalidErr() error      { return tgerr.New(400, "DOCUMENT_INVALID") }

// A capacity failure is not a rate limit: FLOOD_WAIT would make clients retry
// an operation that cannot succeed until an operator adds capacity.
func storageFullErr() error { return tgerr.New(400, "STORAGE_FULL") }

func mediaEmptyErr() error { return tgerr.New(400, "MEDIA_EMPTY") }

func frozenMethodInvalidErr() error      { return tgerr.New(420, "FROZEN_METHOD_INVALID") }
func frozenParticipantMissingErr() error { return tgerr.New(400, "FROZEN_PARTICIPANT_MISSING") }

func photoInvalidErr() error { return tgerr.New(400, "PHOTO_INVALID") }

func stickersetInvalidErr() error { return tgerr.New(406, "STICKERSET_INVALID") }

// stickerInvalidErr means the input document is not a valid sticker/GIF (faveSticker/saveRecentSticker/saveGif).
func stickerInvalidErr() error { return tgerr.New(400, "STICKER_DOCUMENT_INVALID") }

// gifIDInvalidErr means the document referenced by messages.saveGif does not exist or is not a canonical GIFv.
func gifIDInvalidErr() error { return tgerr.New(400, "GIF_ID_INVALID") }

func mediaCaptionTooLongErr() error { return tgerr.New(400, "MEDIA_CAPTION_TOO_LONG") }

func replyMarkupInvalidErr() error { return tgerr.New(400, "REPLY_MARKUP_INVALID") }

// accessTokenInvalidErr is a failed auth.importBotAuthorization bot token
// validation (including an old token after revoke).
func accessTokenInvalidErr() error { return tgerr.New(400, "ACCESS_TOKEN_INVALID") }

// importBotAuthorizationErr maps a bot login business error to an rpc_error.
func importBotAuthorizationErr(err error) error {
	if errors.Is(err, domain.ErrBotTokenInvalid) {
		return accessTokenInvalidErr()
	}
	return internalErr()
}

func shortcutInvalidErr() error { return tgerr.New(400, "SHORTCUT_INVALID") }

func effectIDInvalidErr() error { return tgerr.New(400, "EFFECT_ID_INVALID") }

func paymentUnsupportedErr() error { return tgerr.New(406, "PAYMENT_UNSUPPORTED") }

func allowPaymentRequiredErr(stars int64) error {
	return tgerr.New(403, fmt.Sprintf("ALLOW_PAYMENT_REQUIRED_%d", stars))
}

func balanceTooLowErr() error { return tgerr.New(400, "BALANCE_TOO_LOW") }

func starsAmountInvalidErr() error { return tgerr.New(400, "STARS_AMOUNT_INVALID") }

func subscriptionIDInvalidErr() error { return tgerr.New(400, "SUBSCRIPTION_ID_INVALID") }

func starsFormAmountMismatchErr() error { return tgerr.New(406, "STARS_FORM_AMOUNT_MISMATCH") }

func formIDEmptyErr() error { return tgerr.New(400, "FORM_ID_EMPTY") }

func formExpiredErr() error { return tgerr.New(400, "FORM_EXPIRED") }

func purposeInvalidErr() error { return tgerr.New(400, "PURPOSE_INVALID") }

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

// pollVoteRequiredErr is used in the getPollVotes defensive path (querying voters for a non-public poll / without voting; the official error of the same name).
func pollVoteRequiredErr() error { return tgerr.New(403, "POLL_VOTE_REQUIRED") }

func reactionInvalidErr() error { return tgerr.New(400, "REACTION_INVALID") }

// emoticonInvalidErr is for InputMediaDice-style inputs whose emoticon is outside the supported set.
func emoticonInvalidErr() error { return tgerr.New(400, "EMOTICON_INVALID") }

func themeInvalidErr() error { return tgerr.New(400, "THEME_INVALID") }

// themeErr maps the domain errors of custom cloud themes (account.createTheme etc.) to tgerr.
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

func firstNameInvalidErr() error { return tgerr.New(400, "FIRSTNAME_INVALID") }

func aboutTooLongErr() error { return tgerr.New(400, "ABOUT_TOO_LONG") }

func birthdayInvalidErr() error { return tgerr.New(400, "BIRTHDAY_INVALID") }

func contactIDInvalidErr() error { return tgerr.New(400, "CONTACT_ID_INVALID") }

func contactNameEmptyErr() error { return tgerr.New(400, "CONTACT_NAME_EMPTY") }

func contactReqMissingErr() error { return tgerr.New(400, "CONTACT_REQ_MISSING") }

// messageEmptyErr means sending empty text.
func messageEmptyErr() error { return tgerr.New(400, "MESSAGE_EMPTY") }

// messageTooLongErr means the text exceeds the current stage's limit.
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
func tmpPasswordInvalidErr() error    { return tgerr.New(400, "TMP_PASSWORD_INVALID") }
func newSettingsInvalidErr() error    { return tgerr.New(400, "NEW_SETTINGS_INVALID") }
func newSaltInvalidErr() error        { return tgerr.New(400, "NEW_SALT_INVALID") }
func emailInvalidErr() error          { return tgerr.New(400, "EMAIL_INVALID") }
func emailNotAllowedErr() error       { return tgerr.New(400, "EMAIL_NOT_ALLOWED") }
func emailCodeInvalidErr() error      { return tgerr.New(400, "CODE_INVALID") }
func passwordRecoveryNAErr() error    { return tgerr.New(400, "PASSWORD_RECOVERY_NA") }

func replyMessageIDInvalidErr() error { return tgerr.New(400, "REPLY_MESSAGE_ID_INVALID") }

func chatForwardsRestrictedErr() error { return tgerr.New(400, "CHAT_FORWARDS_RESTRICTED") }

func requestMsgExpiredErr() error { return tgerr.New(400, "REQUEST_MSG_EXPIRED") }

func inputRequestInvalidErr() error { return tgerr.New(400, "INPUT_REQUEST_INVALID") }

func inputRequestTooLongErr() error { return tgerr.New(400, "INPUT_REQUEST_TOO_LONG") }
func dataTooLongErr() error         { return tgerr.New(400, "DATA_TOO_LONG") }

func inputTextEmptyErr() error            { return tgerr.New(400, "INPUT_TEXT_EMPTY") }
func inputTextTooLongErr() error          { return tgerr.New(400, "INPUT_TEXT_TOO_LONG") }
func toLangInvalidErr() error             { return tgerr.New(400, "TO_LANG_INVALID") }
func translateReqFailedErr() error        { return tgerr.New(500, "TRANSLATE_REQ_FAILED") }
func translateReqQuotaExceededErr() error { return tgerr.New(400, "TRANSLATE_REQ_QUOTA_EXCEEDED") }
func translationsDisabledErr() error      { return tgerr.New(406, "TRANSLATIONS_DISABLED") }
func translationTimeoutErr() error        { return tgerr.New(500, "TRANSLATION_TIMEOUT") }

func persistentTimestampInvalidErr() error { return tgerr.New(400, "PERSISTENT_TIMESTAMP_INVALID") }

func channelForumMissingErr() error { return tgerr.New(400, "CHANNEL_FORUM_MISSING") }

func topicTitleEmptyErr() error { return tgerr.New(400, "TOPIC_TITLE_EMPTY") }
func topicIDInvalidErr() error  { return tgerr.New(400, "TOPIC_ID_INVALID") }
func topicsEmptyErr() error     { return tgerr.New(400, "TOPICS_EMPTY") }

// randomIDEmptyErr means the sent message is missing random_id.
func randomIDEmptyErr() error { return tgerr.New(400, "RANDOM_ID_EMPTY") }

func randomIDExpiredErr() error { return tgerr.New(400, "RANDOM_ID_EXPIRED") }

// randomIDDuplicateErr means the same sender reused a random_id, but the request
// payload does not match the first successful send. Layer 227 defines code 500 for
// this error.
func randomIDDuplicateErr() error { return tgerr.New(500, "RANDOM_ID_DUPLICATE") }

// secretChatRandomIDDuplicateErr uses the official BAD_REQUEST semantics of
// Telegram messages.requestEncryption; the 500 mapping historically used by plain
// messages stays separate to avoid widening the change.
func secretChatRandomIDDuplicateErr() error { return tgerr.New(400, "RANDOM_ID_DUPLICATE") }

// scheduleDateInvalidErr means scheduled messages are not supported at this stage.
func scheduleDateInvalidErr() error { return tgerr.New(400, "SCHEDULE_DATE_INVALID") }

// floodWaitErr means a write-operation rate limit was triggered.
func floodWaitErr(seconds int) error {
	if seconds <= 0 {
		seconds = 1
	}
	return tgerr.New(420, fmt.Sprintf("FLOOD_WAIT_%d", seconds))
}

// phoneNumberInvalidErr means the phone number is empty or malformed (auth.sendCode/signIn/signUp).
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

// authKeyUnregisteredErr means the request requires a logged-in state but the current connection is unauthorized.
func authKeyUnregisteredErr() error { return tgerr.New(401, "AUTH_KEY_UNREGISTERED") }
func botMethodInvalidErr() error    { return tgerr.New(400, "BOT_METHOD_INVALID") }

// Private call (phone.*) errors; trigger points are in internal/rpc/phone_calls.go and the app/phone error mapping.
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
func chatSendVoicesForbiddenErr() error {
	return tgerr.New(403, "CHAT_SEND_VOICES_FORBIDDEN")
}

// signalingDataInvalidErr means the phone.sendSignalingData payload is oversized or invalid.
func signalingDataInvalidErr() error { return tgerr.New(400, "DATA_INVALID") }

// Group call errors.
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

// Private end-to-end encryption (Secret Chat / encrypted chat) errors; trigger
// points are in internal/rpc/encrypted_chats.go and the app/secretchat, domain error
// mappings.
func encryptionIDInvalidErr() error       { return tgerr.New(400, "ENCRYPTION_ID_INVALID") }
func encryptionAlreadyAcceptedErr() error { return tgerr.New(400, "ENCRYPTION_ALREADY_ACCEPTED") }
func encryptionAlreadyDeclinedErr() error { return tgerr.New(400, "ENCRYPTION_ALREADY_DECLINED") }
func chatIDInvalidErr() error             { return tgerr.New(400, "CHAT_ID_INVALID") }
func dhGAInvalidErr() error               { return tgerr.New(400, "DH_G_A_INVALID") }
func maxDateInvalidErr() error            { return tgerr.New(400, "MAX_DATE_INVALID") }
func fileEmptyErr() error                 { return tgerr.New(400, "FILE_EMPTY") }

// signInErr maps a login business error to a client-recognizable rpc_error.
func signInErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrPhoneNumberInvalid),
		errors.Is(err, auth.ErrSystemUserLoginForbidden):
		return phoneNumberInvalidErr()
	case errors.Is(err, auth.ErrCodeInvalid):
		return tgerr.New(400, "PHONE_CODE_INVALID")
	case errors.Is(err, auth.ErrCodeExpired):
		return tgerr.New(400, "PHONE_CODE_EXPIRED")
	case errors.Is(err, auth.ErrAuthKeyPermEmpty):
		return tgerr.New(401, "AUTH_KEY_PERM_EMPTY")
	case errors.Is(err, domain.ErrFirstNameInvalid):
		return firstNameInvalidErr()
	case errors.Is(err, domain.ErrSessionPasswordNeeded):
		return sessionPasswordNeededErr()
	default:
		return internalErr()
	}
}

// passkeyErr maps passkey (WebAuthn) errors. Verification-class failures uniformly
// return PASSKEY_INVALID without leaking which step failed (anti-probing);
// non-verification (store/internal) failures return 500.
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
	case errors.Is(err, domain.ErrTmpPasswordInvalid):
		return tmpPasswordInvalidErr()
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
	case errors.Is(err, domain.ErrRecoveryCodeEmpty):
		return tgerr.New(400, "CODE_EMPTY")
	case errors.Is(err, domain.ErrRecoveryCodeInvalid):
		return tgerr.New(400, "CODE_INVALID")
	case errors.Is(err, domain.ErrPasswordRecoveryExpired):
		return tgerr.New(400, "PASSWORD_RECOVERY_EXPIRED")
	case errors.Is(err, domain.ErrPasswordRecoveryNA):
		return passwordRecoveryNAErr()
	default:
		return internalErr()
	}
}

// bindTempAuthKeyErr maps PFS temp auth key binding errors.
func bindTempAuthKeyErr(err error) error {
	switch {
	case errors.Is(err, auth.ErrExpiresAtInvalid):
		return tgerr.New(400, "EXPIRES_AT_INVALID")
	case errors.Is(err, auth.ErrTempAuthKeyEmpty):
		return tgerr.New(400, "TEMP_AUTH_KEY_EMPTY")
	case errors.Is(err, auth.ErrEncryptedMessageInvalid):
		return tgerr.New(400, "ENCRYPTED_MESSAGE_INVALID")
	case errors.Is(err, auth.ErrTempAuthKeyAlreadyBound):
		return tgerr.New(400, "TEMP_AUTH_KEY_ALREADY_BOUND")
	default:
		return internalErr()
	}
}

func quoteTextInvalidErr() error { return tgerr.New(400, "QUOTE_TEXT_INVALID") }
