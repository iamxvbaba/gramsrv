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
	lifecycle := NewStarGiftLifecycleStore(pool, NewMessageStore(pool), 1_000_000)
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

	messages := NewMessageStore(pool)
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

func TestStarGiftAuctionEarlyBidsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	start := now + 3600
	users := NewUserStore(pool)
	winner := createTestUser(t, ctx, users, "+1993"+suffix+"01", "EarlyWinner", "")
	loser := createTestUser(t, ctx, users, "+1993"+suffix+"02", "EarlyLoser", "")
	stars := NewStarsStore(pool)
	for _, id := range []int64{winner.ID, loser.ID} {
		if _, _, err := stars.EnsureGrant(ctx, id, 1000, now); err != nil {
			t.Fatal(err)
		}
	}
	gifts := NewStarGiftStore(pool)
	base := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Early auction " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true, AvailabilityTotal: 1, AvailabilityRemains: 1, GiftsPerRound: 1, AuctionStartDate: start, AuctionRoundDuration: 60, AuctionSlug: "early-" + suffix,
		Document: collectibleTestDocument(base, "early.tgs"), Blob: collectibleTestBlob(base, "early"), Animation: collectibleTestAnimation("early.tgs"), Actor: "integration", CommandID: "early-catalog-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := NewStarGiftLifecycleStore(pool, NewMessageStore(pool), 1000)
	first := domain.StarGiftAuctionBidRequest{UserID: winner.ID, GiftID: entry.Gift.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: winner.ID}, BidAmount: 200, FormID: 61301, Date: now}
	if _, err := lifecycle.StarGiftAuctionState(ctx, winner.ID, entry.Gift.ID, "", now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET status='cancelled' WHERE gift_id=$1`, entry.Gift.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lifecycle.BidStarGiftAuction(ctx, first); !errors.Is(err, domain.ErrStarGiftAuctionUnavailable) {
		t.Fatalf("cancelled auction accepted early bid: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET status='pending' WHERE gift_id=$1`, entry.Gift.ID); err != nil {
		t.Fatal(err)
	}
	state, balance, err := lifecycle.BidStarGiftAuction(ctx, first)
	if err != nil || balance.Balance != 800 || state.CurrentRound != 0 || state.NextRoundAt != start+60 || state.UserState.BidAmount != 200 {
		t.Fatalf("early bid balance=%+v state=%+v err=%v", balance, state, err)
	}
	_, balance, err = lifecycle.BidStarGiftAuction(ctx, first)
	if err != nil || balance.Balance != 800 {
		t.Fatalf("early replay balance=%+v err=%v", balance, err)
	}
	updated := first
	updated.UpdateBid = true
	updated.BidAmount = 350
	updated.FormID = 61302
	updated.Date = now + 1
	_, balance, err = lifecycle.BidStarGiftAuction(ctx, updated)
	if err != nil || balance.Balance != 650 {
		t.Fatalf("early increase balance=%+v err=%v", balance, err)
	}
	insufficient := updated
	insufficient.FormID = 61309
	insufficient.BidAmount = 2000
	if _, _, err := lifecycle.BidStarGiftAuction(ctx, insufficient); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("insufficient early increase: %v", err)
	}
	other := first
	other.UserID = loser.ID
	other.Peer.ID = loser.ID
	other.FormID = 61303
	if _, balance, err = lifecycle.BidStarGiftAuction(ctx, other); err != nil || balance.Balance != 800 {
		t.Fatalf("second early bid balance=%+v err=%v", balance, err)
	}
	if err := lifecycle.settleStarGiftAuction(ctx, entry.Gift.ID, start-1); err != nil {
		t.Fatal(err)
	}
	var awards, payments int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM star_gift_auction_acquired WHERE gift_id=$1`, entry.Gift.ID).Scan(&awards); err != nil || awards != 0 {
		t.Fatalf("premature awards=%d err=%v", awards, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM star_gift_auction_bid_payments WHERE gift_id=$1`, entry.Gift.ID).Scan(&payments); err != nil || payments != 3 {
		t.Fatalf("payments=%d err=%v", payments, err)
	}
	state, err = lifecycle.StarGiftAuctionState(ctx, winner.ID, entry.Gift.ID, "", start)
	if err != nil || state.CurrentRound != 1 || state.UserState.BidAmount != 350 || state.LastGiftNum != 0 {
		t.Fatalf("first round state=%+v err=%v", state, err)
	}
	updated.Date = start + 1
	_, balance, err = lifecycle.BidStarGiftAuction(ctx, updated)
	if err != nil || balance.Balance != 650 {
		t.Fatalf("replay across start balance=%+v err=%v", balance, err)
	}
	if err := lifecycle.settleStarGiftAuction(ctx, entry.Gift.ID, start+60); err != nil {
		t.Fatal(err)
	}
	state, err = lifecycle.loadStarGiftAuction(ctx, winner.ID, entry.Gift.ID)
	if err != nil || !state.Finished || state.LastGiftNum != 1 || state.UserState.AcquiredCount != 1 {
		t.Fatalf("finished state=%+v err=%v", state, err)
	}
	var winnerBalance, loserBalance int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, winner.ID).Scan(&winnerBalance); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, loser.ID).Scan(&loserBalance); err != nil {
		t.Fatal(err)
	}
	if winnerBalance != 650 || loserBalance != 1000 {
		t.Fatalf("settlement balances winner=%d loser=%d", winnerBalance, loserBalance)
	}
	afterEnd := first
	afterEnd.FormID = 61304
	afterEnd.Date = start + 61
	if _, _, err := lifecycle.BidStarGiftAuction(ctx, afterEnd); !errors.Is(err, domain.ErrStarGiftAuctionUnavailable) {
		t.Fatalf("finished auction accepted bid: %v", err)
	}
}
