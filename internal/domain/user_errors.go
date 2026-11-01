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
	// ErrPremiumRequired means the operation is limited to an active Premium account (PREMIUM_ACCOUNT_REQUIRED).
	ErrPremiumRequired = errors.New("premium account required")
	// ErrPremiumBotUnsupported means a bot account cannot be granted Premium (official semantics).
	ErrPremiumBotUnsupported = errors.New("bot accounts cannot be premium")
	// ErrBirthdayInvalid means the birthday month/day/year is out of the legal range (BIRTHDAY_INVALID).
	ErrBirthdayInvalid = errors.New("birthday invalid")
)
