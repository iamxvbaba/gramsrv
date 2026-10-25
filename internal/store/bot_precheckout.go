package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// BotPreCheckoutRegistryStore coordinates one pre-checkout question across Core
// instances. The payer's payment RPC waits on one instance while the bot may
// answer through MTProto messages.setBotPrecheckoutResults or the HTTP Bot API
// on another, so ownership and the single winning answer live in this
// short-lived shared record rather than in process memory.
//
// Implementations must make Put, Resolve and Take atomic: query ids are never
// overwritten, at most one answer wins for the owning bot, and Take removes the
// record while returning any answer that already won. A waiter that times out
// therefore either honours an answer that beat it or leaves nothing for a late
// answer to resolve.
type BotPreCheckoutRegistryStore interface {
	PutBotPreCheckoutPending(ctx context.Context, queryID, botUserID, userID int64, ttl time.Duration) (bool, error)
	ResolveBotPreCheckout(ctx context.Context, botUserID, queryID int64, answer domain.BotPreCheckoutAnswer) (bool, error)
	WaitBotPreCheckoutAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotPreCheckoutAnswer, bool, error)
	TakeBotPreCheckout(ctx context.Context, botUserID, queryID int64) (domain.BotPreCheckoutAnswer, bool, error)
}
