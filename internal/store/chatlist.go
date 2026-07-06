package store

import (
	"context"

	"telesrv/internal/domain"
)

// ChatlistStore persists exported shared-folder links and imported memberships.
type ChatlistStore interface {
	CountInvites(ctx context.Context, ownerUserID int64, filterID int) (int, error)
	CountActiveInvites(ctx context.Context, ownerUserID int64, filterID int) (int, error)
	SaveInvite(ctx context.Context, invite domain.ChatlistInvite) (domain.ChatlistInvite, error)
	GetInvite(ctx context.Context, ownerUserID int64, filterID int, slug string) (domain.ChatlistInvite, bool, error)
	GetInviteBySlug(ctx context.Context, slug string) (domain.ChatlistInvite, bool, error)
	ListInvites(ctx context.Context, ownerUserID int64, filterID int) ([]domain.ChatlistInvite, error)
	DeleteInvite(ctx context.Context, ownerUserID int64, filterID int, slug string) (bool, error)

	CountMemberships(ctx context.Context, userID int64) (int, error)
	SaveMembership(ctx context.Context, membership domain.ChatlistMembership) error
	GetMembershipBySlug(ctx context.Context, userID int64, slug string) (domain.ChatlistMembership, bool, error)
	GetMembershipByLocalFilter(ctx context.Context, userID int64, localFilterID int) (domain.ChatlistMembership, bool, error)
	DeleteMembershipByLocalFilter(ctx context.Context, userID int64, localFilterID int) (bool, error)
	SetMembershipHidden(ctx context.Context, userID int64, localFilterID int, hidden bool) (bool, error)
}
