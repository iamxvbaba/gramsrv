package domain

import "errors"

var (
	ErrUsernameInvalid           = errors.New("username invalid")
	ErrUsernameOccupied          = errors.New("username occupied")
	ErrUsernameNotOccupied       = errors.New("username not occupied")
	ErrPhoneNotOccupied          = errors.New("phone not occupied")
	ErrFirstNameInvalid          = errors.New("first name invalid")
	ErrAboutTooLong              = errors.New("about too long")
	ErrUserNotFound              = errors.New("user not found")
	ErrUserFrozen                = errors.New("user account frozen")
	ErrAuthenticatedScopeInvalid = errors.New("authenticated user scope invalid")
	// ErrPeerModerationFlagsInvalid rejects the impossible scam+fake state at
	// every write boundary shared by user, bot and channel projections.
	ErrPeerModerationFlagsInvalid = errors.New("peer moderation flags invalid")
	// ErrPremiumRequired 表示该操作仅限有效会员（PREMIUM_ACCOUNT_REQUIRED）。
	ErrPremiumRequired = errors.New("premium account required")
	// ErrPremiumBotUnsupported 表示 bot 账号不可被授予会员（官方语义）。
	ErrPremiumBotUnsupported = errors.New("bot accounts cannot be premium")
	// ErrBirthdayInvalid 表示生日的月/日/年不在合法范围（BIRTHDAY_INVALID）。
	ErrBirthdayInvalid = errors.New("birthday invalid")
)
