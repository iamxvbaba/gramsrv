package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// deleteStarGiftRPCGifts implements only the part used by payments.deleteStarGift; the remaining
// methods stay satisfied via the embedded interface for GiftsService.
type deleteStarGiftRPCGifts struct {
	GiftsService
	req   domain.StarGiftDeleteRequest
	calls int
	err   error
}

func (s *deleteStarGiftRPCGifts) DeleteStarGift(_ context.Context, req domain.StarGiftDeleteRequest) (domain.StarGiftDeleteResult, error) {
	s.calls++
	s.req = req
	if s.err != nil {
		return domain.StarGiftDeleteResult{}, s.err
	}
	return domain.StarGiftDeleteResult{SavedGiftID: 11, Deleted: true}, nil
}

func deleteStarGiftRouter(t *testing.T) (*Router, *deleteStarGiftRPCGifts, domain.User) {
	t.Helper()
	r, sender, _, _ := starGiftTestRouter(t)
	gifts := &deleteStarGiftRPCGifts{}
	r.deps.Gifts = gifts
	return r, gifts, sender
}

func TestPaymentsDeleteStarGiftDeletesOwnedGift(t *testing.T) {
	r, gifts, sender := deleteStarGiftRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)

	ok, err := r.onPaymentsDeleteStarGift(ctx, &tg.PaymentsDeleteStarGiftRequest{
		Stargift: &tg.InputSavedStarGiftUser{MsgID: 4242},
	})
	if err != nil {
		t.Fatalf("delete star gift: %v", err)
	}
	if !ok {
		t.Fatal("delete star gift returned false, want true")
	}
	if gifts.calls != 1 {
		t.Fatalf("delete calls = %d, want 1", gifts.calls)
	}
	if gifts.req.UserID != sender.ID || gifts.req.Ref.Owner.Type != domain.PeerTypeUser ||
		gifts.req.Ref.Owner.ID != sender.ID || gifts.req.Ref.MsgID != 4242 {
		t.Fatalf("delete request = %+v", gifts.req)
	}
	if gifts.req.Date <= 0 {
		t.Fatalf("delete request has no server date: %+v", gifts.req)
	}
}

func TestPaymentsDeleteStarGiftRejectsForeignOwner(t *testing.T) {
	r, gifts, sender := deleteStarGiftRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)

	// Channel gifts are channel assets: rejected at the resolution stage, never reaching deletion.
	_, err := r.onPaymentsDeleteStarGift(ctx, &tg.PaymentsDeleteStarGiftRequest{
		Stargift: &tg.InputSavedStarGiftChat{Peer: &tg.InputPeerSelf{}, SavedID: 7},
	})
	if err == nil {
		t.Fatal("channel gift was accepted for a personal delete")
	}
	if gifts.calls != 0 {
		t.Fatalf("channel gift still deleted: calls=%d", gifts.calls)
	}
}

func TestPaymentsDeleteStarGiftMapsStoreFailure(t *testing.T) {
	r, gifts, sender := deleteStarGiftRouter(t)
	ctx := WithUserID(context.Background(), sender.ID)

	gifts.err = domain.ErrStarGiftDeleteUnavailable
	ok, err := r.onPaymentsDeleteStarGift(ctx, &tg.PaymentsDeleteStarGiftRequest{
		Stargift: &tg.InputSavedStarGiftUser{MsgID: 4242},
	})
	if ok {
		t.Fatal("delete star gift succeeded on a refused gift")
	}
	if !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("delete err = %v, want STARGIFT_INVALID", err)
	}

	gifts.err = domain.ErrStarGiftOwnerInvalid
	_, err = r.onPaymentsDeleteStarGift(ctx, &tg.PaymentsDeleteStarGiftRequest{
		Stargift: &tg.InputSavedStarGiftUser{MsgID: 4242},
	})
	if !tgerr.Is(err, "STARGIFT_OWNER_INVALID") {
		t.Fatalf("owner err = %v, want STARGIFT_OWNER_INVALID", err)
	}
}

// deletedStarGiftRPCGifts resolves the ref to a gift that was already deleted.
type deletedStarGiftRPCGifts struct {
	GiftsService
	owner int64
}

func (s *deletedStarGiftRPCGifts) GetSaved(_ context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error) {
	return domain.SavedStarGift{
		ID: 11, Owner: domain.Peer{Type: domain.PeerTypeUser, ID: s.owner}, MsgID: ref.MsgID, FromUserID: s.owner + 1,
		GiftID: 5, LifecycleStatus: domain.StarGiftLifecycleDeleted, Unsaved: true,
	}, true, nil
}

func TestStarGiftUpgradeFormRefusesDeletedGift(t *testing.T) {
	r, sender, _, _ := starGiftTestRouter(t)
	r.deps.Gifts = &deletedStarGiftRPCGifts{owner: sender.ID}
	ctx := WithUserID(context.Background(), sender.ID)

	// The preview is never consulted: the embedded nil GiftsService would panic.
	_, err := r.starGiftUpgradePaymentForm(ctx, sender.ID, &tg.InputInvoiceStarGiftUpgrade{
		Stargift: &tg.InputSavedStarGiftUser{MsgID: 4242},
	})
	if !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("upgrade form for a deleted gift err = %v, want STARGIFT_INVALID", err)
	}
}
