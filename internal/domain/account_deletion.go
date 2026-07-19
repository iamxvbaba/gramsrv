package domain

import (
	"errors"
	"time"
)

var (
	ErrAccountDeleted             = errors.New("account deleted")
	ErrAccountDeletionForbidden   = errors.New("account deletion forbidden")
	ErrAccountDeletionHashInvalid = errors.New("account deletion hash invalid")
	ErrAccountDeletionNotPending  = errors.New("account deletion not pending")
)

// AccountDeletionSource is the single audited reason attached to a user
// tombstone. Different entry points share one execution and cleanup path.
type AccountDeletionSource string

const (
	AccountDeletionManual              AccountDeletionSource = "manual"
	AccountDeletionForgotPassword      AccountDeletionSource = "forgot_password"
	AccountDeletionTOSDecline          AccountDeletionSource = "tos_decline"
	AccountDeletionPasswordResetExpiry AccountDeletionSource = "password_reset_expiry"
	AccountDeletionAccountTTL          AccountDeletionSource = "account_ttl"
	AccountDeletionFreezeExpiry        AccountDeletionSource = "freeze_expiry"
)

type AccountDeletionRequestState string

const (
	AccountDeletionPending   AccountDeletionRequestState = "pending"
	AccountDeletionCancelled AccountDeletionRequestState = "cancelled"
	AccountDeletionExecuted  AccountDeletionRequestState = "executed"
)

// AccountDeletionRequest represents the seven-day 2FA confirmation window.
// ConfirmHashDigest is SHA-256(raw link token); the raw token is only included
// in the durable service message and is never persisted as a credential.
type AccountDeletionRequest struct {
	ID                 int64
	UserID             int64
	RequesterAuthKeyID [8]byte
	State              AccountDeletionRequestState
	Reason             string
	ConfirmHashDigest  [32]byte
	RequestedAt        time.Time
	ExecuteAt          time.Time
	CompletedAt        time.Time
}

type AccountDeletionSnapshot struct {
	User              User
	HasPassword       bool
	PasswordUpdatedAt time.Time
	Pending           *AccountDeletionRequest
}

type ScheduleAccountDeletion struct {
	UserID             int64
	RequesterAuthKeyID [8]byte
	Reason             string
	ConfirmHashDigest  [32]byte
	ServiceMessage     string
	RequestedAt        time.Time
	ExecuteAt          time.Time
}

type AccountDeletionResult struct {
	User                  User
	Changed               bool
	RevokedAuthorizations []Authorization
}

type AccountDeleteKind string

const (
	AccountDeleteImmediate AccountDeleteKind = "immediate"
	AccountDeleteDelayed   AccountDeleteKind = "delayed"
)

type AccountDeleteOutcome struct {
	Kind        AccountDeleteKind
	WaitSeconds int
	ExecuteAt   time.Time
	Deletion    AccountDeletionResult
}

type AccountDeletionCandidate struct {
	UserID int64
	Source AccountDeletionSource
	DueAt  time.Time
}

type AccountDeletionNotification struct {
	ID            int64
	TargetUserID  int64
	DeletedUserID int64
	Attempts      int
}
