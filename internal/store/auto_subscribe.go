package store

import (
	"context"

	"telesrv/internal/domain"
)

// AutoSubscribeStore persists the admin-configured "join everyone" channel
// list. See deploy/migrations/0206_channel_auto_subscribe.up.sql for the
// full add/remove semantics this backs.
type AutoSubscribeStore interface {
	// AddAutoSubscribeChannel adds channelID to the list. Returns added=false
	// (no error) if it was already on the list -- idempotent, matching every
	// other admin command in this codebase.
	AddAutoSubscribeChannel(ctx context.Context, channelID int64, addedBy string) (added bool, err error)
	// RemoveAutoSubscribeChannel removes channelID from the list. Returns
	// removed=false (no error) if it wasn't on it.
	RemoveAutoSubscribeChannel(ctx context.Context, channelID int64) (removed bool, err error)
	// ListAutoSubscribeChannels returns the current list, newest-added first,
	// with each channel's current title for the admin panel to render.
	ListAutoSubscribeChannels(ctx context.Context) ([]domain.AutoSubscribeChannel, error)
	// AutoSubscribeChannelIDs is the lightweight variant SignUp's post-creation
	// hook actually calls on every new account -- just the IDs, no title join.
	AutoSubscribeChannelIDs(ctx context.Context) ([]int64, error)
	// EligibleAutoSubscribeUserIDs lists every real (non-bot, non-deleted,
	// non-system) existing user -- the set a fresh Add bulk-joins. Same
	// eligibility predicate broadcast's own "send to all" targeting uses.
	EligibleAutoSubscribeUserIDs(ctx context.Context) ([]int64, error)
}
