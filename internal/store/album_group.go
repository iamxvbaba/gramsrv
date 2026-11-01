package store

import (
	"context"

	"telesrv/internal/domain"
)

// AlbumGroupStore persists the pre-send grouping reservation for sendMultiMedia.
//
// ReserveAlbumGroup must atomically satisfy:
//   - when the request binds no prior group, every random_id is bound to ProposedGroupedID;
//   - when exactly one prior grouped_id is matched, every missing entry converges to that old value;
//   - when multiple prior grouped_ids are matched, return domain.ErrMessageRandomIDDuplicate and write nothing;
//   - overlapping requests from concurrent, multi-instance callers are equivalent to some serial order.
type AlbumGroupStore interface {
	ReserveAlbumGroup(ctx context.Context, req domain.AlbumGroupReservationRequest) (groupedID int64, err error)
}
