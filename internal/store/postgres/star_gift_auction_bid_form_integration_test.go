package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// A bid whose round has been settled must not be able to answer for the next one.
// Bid form ids used to be a pure function of (bidder, gift, recipient, amount), so a
// bidder who had just won or been refunded and then bid the same amount for the same
// recipient got the settled bid's id back. BidStarGiftAuction answers a known
// star_gift_auction_bid_payments(user_id, form_id) receipt with the stored payment, so
// the new bid was reported as a successful replay while star_gift_auction_bids.active
// stayed false — the bidder was silently locked out of the auction they had just paid
// to re-enter. This pins both halves of the fix: the settled form is rejected instead
// of being mistaken for the new bid, and the state the rpc layer hashes into the next
// form id (the bidder's own bid generation) has moved, so the next form is a new one.
// A gift whose entire auction supply is awarded must flip sold_out=true on the
// active revision so the rpc layer exposes its first/last sale timestamps.
func TestStarGiftAuctionExhaustedSupplyFlipsSoldOutPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	bidder := createTestUser(t, ctx, users, "+1992"+suffix+"01", "AuctionSellout", "")
	bidderPeer := domain.Peer{Type: domain.PeerTypeUser, ID: bidder.ID}
	if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, bidder.ID, 10000, now); err != nil {
		t.Fatalf("grant bid stars: %v", err)
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Auction Sellout " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 1, AvailabilityRemains: 1, GiftsPerRound: 1, AuctionStartDate: now - 10,
		AuctionRoundDuration: 60, AuctionSlug: "auction-sellout-" + suffix,
		Document:  collectibleTestDocument(baseDocumentID, "auction-sellout.tgs"),
		Blob:      collectibleTestBlob(baseDocumentID, "auction-sellout"),
		Animation: collectibleTestAnimation("auction-sellout.tgs"),
		Actor:     "integration", CommandID: "auction-sellout-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create sellout auction: %v", err)
	}
	lifecycle := NewStarGiftLifecycleStore(pool, newTestMessageStore(pool), 1_000_000)
	if _, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: bidder.ID,
		GiftID: entry.Gift.ID, Peer: bidderPeer, BidAmount: 200, FormID: 41301, Date: now}); err != nil {
		t.Fatalf("bid sellout auction: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET next_round_at=$2 WHERE gift_id=$1`, entry.Gift.ID, now+2); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.SweepStarGiftLifecycle(ctx, now+2, 1000); err != nil {
		t.Fatalf("settle sellout auction: %v", err)
	}
	var soldOut bool
	var remains int
	if err := pool.QueryRow(ctx, `SELECT r.sold_out,c.availability_remains FROM star_gift_catalog c
JOIN star_gift_catalog_revisions r ON r.id=c.active_revision_id WHERE c.gift_id=$1`, entry.Gift.ID).
		Scan(&soldOut, &remains); err != nil {
		t.Fatal(err)
	}
	if !soldOut || remains != 0 {
		t.Fatalf("exhausted auction supply sold_out=%v remains=%d, want true/0", soldOut, remains)
	}
}

func TestStarGiftAuctionBidAfterSettledRoundNeedsFreshFormPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	bidder := createTestUser(t, ctx, users, "+1991"+suffix+"01", "AuctionReBidder", "")
	bidderPeer := domain.Peer{Type: domain.PeerTypeUser, ID: bidder.ID}

	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, bidder.ID, 10000, now); err != nil {
		t.Fatalf("grant bid stars: %v", err)
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Auction Rebid " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 3, AvailabilityRemains: 3, GiftsPerRound: 1, AuctionStartDate: now - 10,
		AuctionRoundDuration: 60, AuctionSlug: "auction-rebid-" + suffix,
		Document:  collectibleTestDocument(baseDocumentID, "auction-rebid.tgs"),
		Blob:      collectibleTestBlob(baseDocumentID, "auction-rebid"),
		Animation: collectibleTestAnimation("auction-rebid.tgs"),
		Actor:     "integration", CommandID: "auction-rebid-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create rebid auction catalog: %v", err)
	}
	giftID := entry.Gift.ID

	messages := newTestMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)

	const settledForm = int64(31001)
	first, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: bidder.ID,
		GiftID: giftID, Peer: bidderPeer, BidAmount: 200, FormID: settledForm, Date: now, Message: "round one"})
	if err != nil || first.UserState.BidAmount != 200 {
		t.Fatalf("first bid state = %+v err %v", first.UserState, err)
	}
	firstGeneration := first.UserState.BidVersion
	if firstGeneration <= 0 {
		t.Fatalf("first bid generation = %d, want a positive counter", firstGeneration)
	}

	// Round one falls due and the only active bid wins it: the reservation is spent,
	// the bid row goes inactive, and the bidder may come back for the next gift.
	if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET next_round_at=$2 WHERE gift_id=$1`, giftID, now+2); err != nil {
		t.Fatalf("make auction round due: %v", err)
	}
	if err := lifecycle.SweepStarGiftLifecycle(ctx, now+2, 1000); err != nil {
		t.Fatalf("settle auction round: %v", err)
	}
	acquired, err := lifecycle.StarGiftAuctionAcquired(ctx, bidder.ID, giftID)
	if err != nil || len(acquired) != 1 || acquired[0].BidAmount != 200 {
		t.Fatalf("round one award = %+v err %v", acquired, err)
	}

	settled, err := lifecycle.StarGiftAuctionState(ctx, bidder.ID, giftID, "", now+3)
	if err != nil {
		t.Fatalf("state after settlement: %v", err)
	}
	// A settled round holds nothing: reporting the spent amount as a live bid used to
	// leave the bidder with no accepted call at all — the rpc gate refuses a fresh bid
	// while an amount is present and the store refuses a raise while the row is
	// inactive.
	if settled.UserState.BidAmount != 0 || settled.UserState.BidDate != 0 {
		t.Fatalf("settled bid still reported live: %+v", settled.UserState)
	}
	if settled.UserState.AcquiredCount != 1 {
		t.Fatalf("settled acquired count = %d, want 1", settled.UserState.AcquiredCount)
	}
	if settled.UserState.BidVersion == firstGeneration {
		t.Fatalf("bid generation unchanged across settlement (%d): the next form id would repeat", firstGeneration)
	}

	balanceAfterFirst, err := stars.GetBalance(ctx, bidder.ID)
	if err != nil {
		t.Fatalf("balance after first bid: %v", err)
	}
	if balanceAfterFirst.Balance != 9800 {
		t.Fatalf("balance after winning round one = %d, want 9800", balanceAfterFirst.Balance)
	}

	// The exact regression: the settled round's form, submitted again for the same
	// gift, recipient and amount. It must not be answered from the old receipt.
	if _, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: bidder.ID,
		GiftID: giftID, Peer: bidderPeer, BidAmount: 200, FormID: settledForm,
		Date: now + 3, Message: "round two"}); !errors.Is(err, domain.ErrStarGiftAuctionUnavailable) {
		t.Fatalf("settled form re-submitted err = %v, want %v", err, domain.ErrStarGiftAuctionUnavailable)
	}
	var replayedActive bool
	if err := pool.QueryRow(ctx, `SELECT active FROM star_gift_auction_bids WHERE gift_id=$1 AND bidder_user_id=$2`,
		giftID, bidder.ID).Scan(&replayedActive); err != nil {
		t.Fatal(err)
	}
	if replayedActive {
		t.Fatal("settled form re-submitted left a bid behind")
	}
	if balance, err := stars.GetBalance(ctx, bidder.ID); err != nil || balance.Balance != 9800 {
		t.Fatalf("rejected re-submit moved the balance = %+v err %v", balance, err)
	}

	// The form the rpc layer now mints carries the new generation, so it is a value
	// the receipt table has never seen, and the bid lands for real.
	const freshForm = int64(31002)
	second, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: bidder.ID,
		GiftID: giftID, Peer: bidderPeer, BidAmount: 200, FormID: freshForm, Date: now + 4, Message: "round two"})
	if err != nil {
		t.Fatalf("fresh-form rebid: %v", err)
	}
	if second.UserState.BidAmount != 200 || second.UserState.MinBidAmount != 201 {
		t.Fatalf("rebid state = %+v, want a live 200 bid", second.UserState)
	}
	var secondActive bool
	var secondAmount int64
	if err := pool.QueryRow(ctx, `SELECT active,amount FROM star_gift_auction_bids WHERE gift_id=$1 AND bidder_user_id=$2`,
		giftID, bidder.ID).Scan(&secondActive, &secondAmount); err != nil {
		t.Fatal(err)
	}
	if !secondActive || secondAmount != 200 {
		t.Fatalf("rebid row active=%v amount=%d, want an active 200 bid", secondActive, secondAmount)
	}
	if balance, err := stars.GetBalance(ctx, bidder.ID); err != nil || balance.Balance != 9600 {
		t.Fatalf("balance after rebid = %+v err %v, want 9600", balance, err)
	}
	// The genuine replay still works: the same fresh form, submitted twice, charges
	// once and answers from the receipt rather than raising the bid.
	replay, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: bidder.ID,
		GiftID: giftID, Peer: bidderPeer, BidAmount: 200, FormID: freshForm, Date: now + 5, Message: "round two"})
	if err != nil || replay.UserState.BidAmount != 200 {
		t.Fatalf("fresh-form replay = %+v err %v", replay.UserState, err)
	}
	if balance, err := stars.GetBalance(ctx, bidder.ID); err != nil || balance.Balance != 9600 {
		t.Fatalf("replay charged twice = %+v err %v", balance, err)
	}
}
