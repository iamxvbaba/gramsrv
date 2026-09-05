package postgres

import "context"

// SweepSoldOutStarGifts is the daily "sold out gift disappears from the
// marketplace" job: any enabled, limited, non-auction catalog gift with
// availability_remains<=0 (every copy already sold) is disabled, the same
// as an operator hitting SetCatalogEnabled(false) by hand.
//
// Two things it deliberately never touches:
//   - Auctions. An auction gift is also `limited`, but it already has its
//     own end-of-rounds/closing lifecycle (star_gift_auctions) -- sweeping
//     it here would race that separate mechanism.
//   - The secondary NFT marketplace (star_gift_listings/unique_star_gifts/
//     peer_star_gifts). A unique gift a user already owns and has listed
//     for resale is a completely different surface from "buy a fresh copy
//     from the catalog" and is untouched by this sweep, on purpose.
//
// The "sold out" ribbon a real Telegram client shows on a still-enabled
// gift needs no code here: tgStarGift always sends the live
// availability_remains/availability_total for a Limited gift on every
// payments.getStarGifts call (no cache), and the client renders that badge
// on its own once remains hits 0 -- this sweep only handles the SEPARATE
// "stop offering it at all" step, on a schedule.
func (s *StarGiftStore) SweepSoldOutStarGifts(ctx context.Context) (int, error) {
	// Defensive: the purchase path (star_gift_purchase.go) already flips
	// sold_out=true the moment a sale empties the count, so the badge
	// normally appears immediately, well before this daily sweep runs. This
	// covers any other way remains could reach zero (a manual admin
	// adjustment, say) so the two facts -- the ribbon and the catalog
	// listing -- never disagree.
	if _, err := s.db.Exec(ctx, `
UPDATE star_gift_catalog_revisions r SET sold_out=true
FROM star_gift_catalog c
WHERE r.id = c.active_revision_id AND r.limited AND NOT r.auction AND c.availability_remains <= 0 AND NOT r.sold_out`); err != nil {
		return 0, err
	}
	tag, err := s.db.Exec(ctx, `
UPDATE star_gift_catalog c SET enabled=false, updated_at=now()
FROM star_gift_catalog_revisions r
WHERE r.id = c.active_revision_id AND c.enabled AND r.limited AND NOT r.auction AND c.availability_remains <= 0`)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
