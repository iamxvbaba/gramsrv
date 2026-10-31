package domain

import (
	"crypto/sha256"
	"errors"
)

// ErrAlbumGroupReservationInvalid means an album group reservation is missing a sender, target,
// random_id or proposed grouped_id. The RPC boundary usually rejects these inputs earlier;
// domain/store still fails fast to keep a bad binding from reaching the persistence layer.
var ErrAlbumGroupReservationInvalid = errors.New("album group reservation invalid")

// AlbumGroupReservationRequest atomically binds a set of random_ids to the same grouped_id
// before any album item is persisted or uploaded media is parsed. Peer is part of the idempotency scope:
// the same sender can reuse a random_id in different sessions without cross-contaminating album groups.
type AlbumGroupReservationRequest struct {
	SenderUserID      int64
	Peer              Peer
	Items             []AlbumGroupReservationItem
	ProposedGroupedID int64
}

// AlbumGroupReservationItem binds a random_id to that item's immutable client intent.
// IntentHash is the SHA-256 computed before media parsing/server-derived fields are produced; the same random_id
// carrying a different intent must report a conflict and must not bypass send-idempotency checks via an old album reservation.
type AlbumGroupReservationItem struct {
	RandomID   int64
	IntentHash []byte
}

// Validate checks the minimal invariants the persistence layer must rely on. A duplicate random_id within
// one batch is synonymous with a send-idempotency conflict and must fail explicitly, not silently dedup and change the client's requested message count.
func (r AlbumGroupReservationRequest) Validate() error {
	if r.SenderUserID <= 0 || r.Peer.ID <= 0 || r.ProposedGroupedID == 0 || len(r.Items) == 0 {
		return ErrAlbumGroupReservationInvalid
	}
	if r.Peer.Type != PeerTypeUser && r.Peer.Type != PeerTypeChannel {
		return ErrAlbumGroupReservationInvalid
	}
	seen := make(map[int64]struct{}, len(r.Items))
	for _, item := range r.Items {
		if item.RandomID == 0 || len(item.IntentHash) != sha256.Size {
			return ErrAlbumGroupReservationInvalid
		}
		if _, exists := seen[item.RandomID]; exists {
			return ErrMessageRandomIDDuplicate
		}
		seen[item.RandomID] = struct{}{}
	}
	return nil
}
