package store

import (
	"context"

	"telesrv/internal/domain"
)

// ContactStore persists a user's contact list.
type ContactStore interface {
	ListByUser(ctx context.Context, userID int64) (domain.ContactList, error)
	Get(ctx context.Context, userID, contactUserID int64) (domain.Contact, bool, error)
	GetMany(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.Contact, error)
	GetReverseContacts(ctx context.Context, userID int64, ownerUserIDs []int64) (map[int64]domain.Contact, error)
	ContactProjectionForViewers(ctx context.Context, viewerUserIDs, contactUserIDs []int64) (domain.ContactProjectionBatch, error)
	Upsert(ctx context.Context, userID int64, input domain.ContactInput) (domain.Contact, error)
	UpsertMany(ctx context.Context, userID int64, inputs []domain.ContactInput) ([]domain.Contact, error)
	UpdateNote(ctx context.Context, userID, contactUserID int64, note string, entities []domain.MessageEntity) (domain.Contact, bool, error)
	SetCloseFriends(ctx context.Context, userID int64, contactUserIDs []int64) (domain.CloseFriendsEditResult, error)
	SetPersonalPhoto(ctx context.Context, userID, contactUserID int64, photoID int64, date int) (domain.Contact, bool, error)
	PersonalPhotos(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error)
	Delete(ctx context.Context, userID int64, contactUserIDs []int64) (int, error)
	ReadBlocklistIDs(ctx context.Context, ownerUserID int64) ([]int64, error)
	MutateBlocklist(ctx context.Context, mutation BlocklistMutation, effects DeliveryEffectsBuilder[BlocklistMutationSnapshot]) (BlocklistMutationSnapshot, error)
	IsBlocked(ctx context.Context, userID, blockedUserID int64) (bool, error)
	ListBlocked(ctx context.Context, userID int64, offset, limit int) (domain.BlockedContactList, error)
}

// SparseContactProjectionStore reads only the explicitly requested
// viewer->contact pairs. Unlike ContactProjectionForViewers, the two dimensions
// are not crossed: a target listed for one viewer is never read for another
// viewer unless that pair is also present in contactUserIDsByViewer.
type SparseContactProjectionStore interface {
	ContactProjectionForViewerUserIDs(ctx context.Context, contactUserIDsByViewer map[int64][]int64) (domain.ContactProjectionBatch, error)
}

// SparseReverseContactStore reads only explicitly requested owner->viewer
// relationship pairs. It is the database-facing primitive behind batched
// privacy projection: unlike GetReverseContacts it can combine different
// viewers in one query without broadening the request into a cross product.
type SparseReverseContactStore interface {
	GetReverseContactsForViewerUserIDs(ctx context.Context, viewerUserIDsByOwner map[int64][]int64) (map[int64]map[int64]domain.Contact, error)
}
