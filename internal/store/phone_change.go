package store

import (
	"context"

	"telesrv/internal/domain"
)

// PhoneChangeStore atomically changes an account's phone number and records a recoverable
// updateUserPhone event. The production implementation must also enqueue into the dispatch
// outbox in the same transaction.
type PhoneChangeStore interface {
	ChangePhone(ctx context.Context, req domain.PhoneChangeRequest) (domain.PhoneChangeResult, error)
}
