package store

import (
	"context"

	"telesrv/internal/domain"
)

// UsernameRegistryStore reads a peer's full username list. The list is the
// projection source for the TL usernames vector, so every caller sees the same
// order the client will render: editable slot first, then collectibles.
type UsernameRegistryStore interface {
	// PeerUsernames returns the peer's registry rows in projection order.
	PeerUsernames(ctx context.Context, peer domain.Peer) ([]domain.Username, error)
	// PeerUsernamesBatch resolves several peers in one round trip; peers absent
	// from the result simply hold no usernames.
	PeerUsernamesBatch(ctx context.Context, peers []domain.Peer) (map[domain.Peer][]domain.Username, error)
	// SetUsernameActive toggles one collectible row. It never touches the
	// editable slot and returns domain.ErrUsernameNotCollectible if asked to.
	SetUsernameActive(ctx context.Context, peer domain.Peer, username string, active bool) (bool, error)
	// ReorderUsernames rewrites collectible sort order. order must be a
	// permutation of the peer's collectible usernames.
	ReorderUsernames(ctx context.Context, peer domain.Peer, order []string) (bool, error)
	// DeactivateAllUsernames clears the active flag on every collectible row and
	// reports whether anything changed. The editable slot is left alone.
	DeactivateAllUsernames(ctx context.Context, peer domain.Peer) (bool, error)
}

// CollectibleUsernameStore owns the collectible asset lifecycle: minting into the
// operator vault, assigning to a holder, returning to the vault and burning.
//
// Every mutation is expected to be atomic with the corresponding username
// registry row, so an asset can never be owned without being resolvable and can
// never be resolvable without being owned.
type CollectibleUsernameStore interface {
	// MintCollectibleUsername creates the asset. A request carrying an owner
	// assigns it in the same transaction. Replaying the same CommandKey returns
	// the original asset and reports created=false.
	MintCollectibleUsername(ctx context.Context, req domain.MintCollectibleUsernameRequest) (asset domain.CollectibleUsername, created bool, err error)
	// TransferCollectibleUsername moves the asset to req.To, out of the vault or
	// from the current holder. Replay by CommandKey is a no-op returning the
	// current state with changed=false.
	TransferCollectibleUsername(ctx context.Context, req domain.TransferCollectibleUsernameRequest) (asset domain.CollectibleUsername, changed bool, err error)
	// RevokeCollectibleUsername returns the asset to the vault, or burns it when
	// req.Burn is set. Burning releases the name back to the free pool.
	RevokeCollectibleUsername(ctx context.Context, req domain.RevokeCollectibleUsernameRequest) (asset domain.CollectibleUsername, changed bool, err error)
	// CollectibleUsername looks the asset up by name.
	CollectibleUsername(ctx context.Context, username string) (domain.CollectibleUsername, error)
	// CollectibleUsernameByID looks the asset up by identity.
	CollectibleUsernameByID(ctx context.Context, id int64) (domain.CollectibleUsername, error)
	// ListCollectibleUsernames is the admin listing query with keyset paging.
	ListCollectibleUsernames(ctx context.Context, filter domain.CollectibleUsernameFilter) ([]domain.CollectibleUsername, error)
	// CollectibleUsernameTransfers returns the provenance log, newest first.
	CollectibleUsernameTransfers(ctx context.Context, collectibleID int64, limit int) ([]domain.CollectibleUsernameTransfer, error)
}
