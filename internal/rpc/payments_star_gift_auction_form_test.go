package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// The bid form id is the only thing standing between two bids of the same amount by
// the same bidder for the same recipient. Once the first bid's round is settled its
// payment receipt stays in star_gift_auction_bid_payments forever, so a repeated id
// makes BidStarGiftAuction answer the next bid from that receipt — reported as paid,
// never active. Binding the bidder's own bid generation is what keeps the ids apart.
func TestStarGiftAuctionBidFormIDSeparatesBidGenerations(t *testing.T) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 7001}
	base := starGiftAuctionBidFormID(42, 900, peer, 200, 1)
	if base <= 0 {
		t.Fatalf("form id = %d, want a positive id (0 is what sendStarGiftAuctionBidForm rejects)", base)
	}
	if again := starGiftAuctionBidFormID(42, 900, peer, 200, 1); again != base {
		t.Fatalf("form id unstable within one generation: %d then %d", base, again)
	}
	for _, tc := range []struct {
		name string
		id   int64
	}{
		{"next generation", starGiftAuctionBidFormID(42, 900, peer, 200, 2)},
		{"generation after a refund and a win", starGiftAuctionBidFormID(42, 900, peer, 200, 3)},
		{"another bidder", starGiftAuctionBidFormID(43, 900, peer, 200, 1)},
		{"another gift", starGiftAuctionBidFormID(42, 901, peer, 200, 1)},
		{"another recipient", starGiftAuctionBidFormID(42, 900, domain.Peer{Type: domain.PeerTypeUser, ID: 7002}, 200, 1)},
		{"a channel recipient with the same id", starGiftAuctionBidFormID(42, 900, domain.Peer{Type: domain.PeerTypeChannel, ID: 7001}, 200, 1)},
		{"another amount", starGiftAuctionBidFormID(42, 900, peer, 201, 1)},
	} {
		if tc.id == base {
			t.Fatalf("form id for %s repeats the first bid's id (%d)", tc.name, base)
		}
	}
}

type supportOnlyAuctionRPCService struct {
	GiftsService
	state domain.StarGiftAuction
}

func (s *supportOnlyAuctionRPCService) AuctionState(_ context.Context, _ int64, _ int64, _ string, _ int) (domain.StarGiftAuction, error) {
	return s.state, nil
}

// Support-only auctions are gated the same way as support-only purchases: a
// viewer without the official support flag cannot form a bid or confirm it.
// The gate lives in starGiftAuctionBidTarget, which both the bid form and the
// settlement path run through, so a single check covers form and confirm.
func TestStarGiftSupportOnlyAuctionBidGate(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	regular, err := users.Create(ctx, domain.User{AccessHash: 7111, Phone: "15550007111", FirstName: "Regular"})
	if err != nil {
		t.Fatalf("create regular: %v", err)
	}
	helper, err := users.Create(ctx, domain.User{AccessHash: 7112, Phone: "15550007112", FirstName: "Support", Support: true})
	if err != nil {
		t.Fatalf("create support: %v", err)
	}
	gifts := &supportOnlyAuctionRPCService{state: domain.StarGiftAuction{Gift: domain.StarGift{ID: 8009, SupportOnly: true}}}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398, PublicBaseURL: "https://links.example.test"}, Deps{
		Users: appusers.NewService(users),
		Gifts: gifts,
	}, zaptest.NewLogger(t), clock.System)

	inv := &tg.InputInvoiceStarGiftAuctionBid{GiftID: 8009, BidAmount: 200}

	regularCtx := WithUserID(ctx, regular.ID)
	if _, err := r.onPaymentsGetPaymentForm(regularCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv}); !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("regular auction bid form err=%v, want STARGIFT_INVALID", err)
	}
	if _, err := r.onPaymentsSendStarsForm(regularCtx, &tg.PaymentsSendStarsFormRequest{FormID: 1, Invoice: inv}); !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("regular auction bid confirm err=%v, want STARGIFT_INVALID", err)
	}

	supportCtx := WithUserID(ctx, helper.ID)
	formRes, err := r.onPaymentsGetPaymentForm(supportCtx, &tg.PaymentsGetPaymentFormRequest{Invoice: inv})
	if err != nil {
		t.Fatalf("support auction bid form: %v", err)
	}
	if _, ok := formRes.(*tg.PaymentsPaymentFormStarGift); !ok {
		t.Fatalf("support auction bid form=%T, want PaymentsPaymentFormStarGift", formRes)
	}
}
