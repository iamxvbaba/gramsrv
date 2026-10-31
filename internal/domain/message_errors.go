package domain

import "errors"

var (
	ErrMessageIDInvalid      = errors.New("message id invalid")
	ErrMessageEmpty          = errors.New("message empty")
	ErrMessageAuthorRequired = errors.New("message author required")
	ErrMessageNotModified    = errors.New("message not modified")
	ErrMessageNotReadYet     = errors.New("message not read yet")
	// ErrFactCheckInvalid means the fact-check tag itself is invalid (empty text, illegal country code).
	ErrFactCheckInvalid = errors.New("fact check invalid")
	// ErrFactCheckTooLong means the fact-check verdict text exceeds the client limit.
	ErrFactCheckTooLong = errors.New("fact check text too long")
	// ErrFactCheckForbidden means the current user is not allowed to rewrite this message's fact-check tag.
	ErrFactCheckForbidden = errors.New("fact check forbidden")
	// ErrPeerDeleted rejects a private write whose recipient is an account
	// tombstone. The rows survive for history, so only this gate stops the
	// message from landing in a dialog nobody can read.
	ErrPeerDeleted = errors.New("peer deleted")
	// ErrMessageRandomIDDuplicate means the same sender reused a random_id and this
	// immutable request payload differs from the first successful send. An exactly identical replay does not return this error,
	// but reuses the first send's result.
	ErrMessageRandomIDDuplicate = errors.New("message random id duplicate")
	// ErrLoginCodeDeliveryInvalid rejects malformed durable 777000 delivery
	// commands before allocating message/pts facts.
	ErrLoginCodeDeliveryInvalid = errors.New("login code delivery invalid")
	// ErrLoginCodeDeliveryConflict means one phone_code_hash digest was reused
	// for a different account or code. It must fail closed rather than expose or
	// overwrite the first account's immutable receipt.
	ErrLoginCodeDeliveryConflict = errors.New("login code delivery conflict")
	// ErrLoginCodeDeliveryCommitAmbiguous means PostgreSQL lost the commit
	// acknowledgement and an independent receipt probe could not prove whether
	// the durable 777000 transaction committed. Callers must retain the opaque
	// code record until TTL expiry; deleting it could invalidate a committed but
	// undisclosed delivery and make a retry impossible to reconcile.
	ErrLoginCodeDeliveryCommitAmbiguous = errors.New("login code delivery commit ambiguous")
	ErrReplyMessageIDInvalid            = errors.New("reply message id invalid")
	ErrQuoteTextInvalid                 = errors.New("quote text invalid")
	ErrChatForwardsRestricted           = errors.New("chat forwards restricted")
	ErrNoForwardsRequestExpired         = errors.New("no forwards request expired")
	// ErrPinnedSavedDialogsTooMuch maps PINNED_TOO_MUCH: the saved-messages sub-chat pin
	// count reached the MaxPinnedSavedDialogs limit.
	ErrPinnedSavedDialogsTooMuch = errors.New("pinned saved dialogs too much")
	// ErrPinnedDialogsTooMuch maps PINNED_DIALOGS_TOO_MUCH: the pinned chat count in the
	// target folder reached the limit.
	ErrPinnedDialogsTooMuch = errors.New("pinned dialogs too much")
)
