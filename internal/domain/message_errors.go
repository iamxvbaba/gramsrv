package domain

import "errors"

var (
	ErrMessageIDInvalid      = errors.New("message id invalid")
	ErrMessageEmpty          = errors.New("message empty")
	ErrMessageAuthorRequired = errors.New("message author required")
	ErrMessageNotModified    = errors.New("message not modified")
	ErrMessageNotReadYet     = errors.New("message not read yet")
	// ErrMessageRandomIDDuplicate 表示同一发送者重复使用 random_id，且本次
	// 不可变请求载荷与首次成功发送不一致。完全相同的重放不返回此错误，
	// 而是复用首次发送结果。
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
	ErrChatForwardsRestricted           = errors.New("chat forwards restricted")
	// ErrPinnedSavedDialogsTooMuch 映射 PINNED_TOO_MUCH：收藏夹子会话置顶
	// 数量达到 MaxPinnedSavedDialogs 上限。
	ErrPinnedSavedDialogsTooMuch = errors.New("pinned saved dialogs too much")
	// ErrPinnedDialogsTooMuch 映射 PINNED_DIALOGS_TOO_MUCH：目标 folder 内
	// 置顶会话数量达到上限。
	ErrPinnedDialogsTooMuch = errors.New("pinned dialogs too much")
)
