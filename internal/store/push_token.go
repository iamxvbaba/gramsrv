package store

import (
	"context"

	"telesrv/internal/domain"
)

// PushTokenStore persists device push tokens registered through
// account.registerDevice. It is optional: when a router has no PushTokenStore the
// register/unregister RPCs stay no-ops, which is the pre-feature behaviour for a
// deployment that has not enabled push notifications.
type PushTokenStore interface {
	// UpsertPushToken inserts or updates a token. A token is globally unique per
	// (tokenType, token); re-registering it for another user reassigns ownership so
	// a token is never delivered for two accounts.
	UpsertPushToken(ctx context.Context, token domain.PushToken) error
	// DeletePushToken removes a token only when it is owned by userID, so one
	// account cannot unregister another account's device.
	DeletePushToken(ctx context.Context, userID int64, tokenType int, token string) (bool, error)
	// DeletePushTokenByValue removes a token regardless of its owner. It is used
	// to drop a token the provider has invalidated.
	DeletePushTokenByValue(ctx context.Context, tokenType int, token string) (bool, error)
	// ListPushTokensForUser returns every token bound to a user.
	ListPushTokensForUser(ctx context.Context, userID int64) ([]domain.PushToken, error)
	// ListPushTokensForUsers returns tokens grouped by user for a batch of users.
	ListPushTokensForUsers(ctx context.Context, userIDs []int64) (map[int64][]domain.PushToken, error)
}
