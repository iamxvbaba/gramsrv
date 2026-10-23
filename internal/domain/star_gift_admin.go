package domain

import "errors"

// ErrStarGiftDeleteBlocked means an admin gift deletion was refused because
// StarGiftDeleteResult.Blockers is not empty.
var ErrStarGiftDeleteBlocked = errors.New("stargift: delete blocked: the gift has collectible copies or an auction")

// StarGiftOwnerRefund is what one user owner gets back when an admin deletes
// a gift they hold: the gift's conversion value, i.e. the Stars they could
// have converted it into themselves.
type StarGiftOwnerRefund struct {
	UserID int64
	Stars  int64
}

// StarGiftDeleteResult describes an admin gift deletion (or, from the
// preview, what it would do). Every ordinary saved copy is retired. Refunds
// lists user owners only; a channel cannot be credited.
//
// Blockers lists state the deletion refuses to cascade through: collectible
// (NFT) instances, which carry a marketplace and other people's commitments,
// and an auction. A non-empty Blockers means the deletion does not run.
type StarGiftDeleteResult struct {
	GiftID   int64
	Title    string
	Owners   int
	Refunds  []StarGiftOwnerRefund
	Blockers map[string]int
}

// RefundTotal sums every owner's refund, whether or not it was credited.
func (r StarGiftDeleteResult) RefundTotal() int64 {
	var total int64
	for _, refund := range r.Refunds {
		total += refund.Stars
	}
	return total
}
