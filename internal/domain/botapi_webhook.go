package domain

import "time"

// BotAPIWebhook is durable delivery configuration and observable retry state.
// The token secret is never stored here: authentication remains owned by BotProfile.
type BotAPIWebhook struct {
	BotUserID      int64
	URL            string
	SecretToken    string
	MaxConnections int
	AllowedUpdates []BotAPIUpdateKind
	// AllowedUpdatesSet distinguishes an explicitly supplied (possibly empty)
	// setWebhook parameter from omission, which must preserve the previous
	// getUpdates/setWebhook policy atomically at the store boundary.
	AllowedUpdatesSet bool
	FailureCount      int
	LastErrorDate     int
	LastErrorMessage  string
	NextAttemptAt     time.Time
}
