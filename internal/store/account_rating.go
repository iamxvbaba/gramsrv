package store

import (
	"context"

	"telesrv/internal/domain"
)

// AccountRatingStore owns the composite rating read model and its contribution
// ledger.
//
// The read model is derived: SaveAccountRating writes a recomputed projection,
// AccountRatingSignals gathers the raw inputs from the contributing tables, and
// the ledger keeps manual adjustments that must survive a recompute.
type AccountRatingStore interface {
	// AccountRating returns the stored projection. Missing rows return
	// domain.ErrAccountRatingNotFound so callers can distinguish "never computed"
	// from "computed as zero".
	AccountRating(ctx context.Context, userID int64) (domain.AccountRating, error)
	// AccountRatingBatch resolves several users in one round trip. Users without
	// a row are absent from the map.
	AccountRatingBatch(ctx context.Context, userIDs []int64) (map[int64]domain.AccountRating, error)
	// SaveAccountRating upserts the projection using optimistic concurrency on
	// the stored version; a stale version reports changed=false.
	SaveAccountRating(ctx context.Context, rating domain.AccountRating) (stored domain.AccountRating, changed bool, err error)
	// AccountRatingSignals gathers the raw contribution snapshot for one user,
	// including the manual total carried from the ledger.
	AccountRatingSignals(ctx context.Context, userID int64) (domain.AccountRatingSignals, error)
	// AdjustAccountRating appends a manual adjustment. Replaying the same
	// CommandKey returns the recorded event and applied=false.
	AdjustAccountRating(ctx context.Context, req domain.AdjustAccountRatingRequest) (event domain.AccountRatingEvent, applied bool, err error)
	// ListAccountRatings is the admin leaderboard query with keyset paging.
	ListAccountRatings(ctx context.Context, filter domain.AccountRatingFilter) ([]domain.AccountRating, error)
	// AccountRatingEvents returns the ledger for one user, newest first.
	AccountRatingEvents(ctx context.Context, userID int64, limit int) ([]domain.AccountRatingEvent, error)
	// StaleAccountRatings returns user ids whose projection is older than the
	// given horizon, for the background recompute worker.
	StaleAccountRatings(ctx context.Context, olderThanUnix int64, limit int) ([]int64, error)
}
