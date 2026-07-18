package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestSavedStarGiftIdentityDoesNotAcceptUpgradeMessageID(t *testing.T) {
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 42}
	store := NewStarGiftStore()
	id, err := store.Create(ctx, domain.SavedStarGift{
		Owner: owner, GiftID: 8001, RevisionID: 9001, MsgID: 115,
		UniqueGiftID: 901, UpgradeMsgID: 116,
	})
	if err != nil {
		t.Fatalf("create saved gift: %v", err)
	}
	store.uniqueBySlug["official-8001-1"] = 901

	canonical := domain.SavedStarGiftRef{Owner: owner, MsgID: 115}
	if saved, found, err := store.GetByRef(ctx, canonical); err != nil || !found || saved.ID != id {
		t.Fatalf("canonical identity: saved=%+v found=%v err=%v", saved, found, err)
	}
	wrong := domain.SavedStarGiftRef{Owner: owner, MsgID: 116}
	if saved, found, err := store.GetByRef(ctx, wrong); err != nil || found {
		t.Fatalf("upgrade message id resolved gift: saved=%+v found=%v err=%v", saved, found, err)
	}
	if _, err := store.ResolveSavedIDs(ctx, owner, []domain.SavedStarGiftRef{wrong}); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("upgrade message id resolve err=%v, want ErrStarGiftNotFound", err)
	}
	if _, err := store.ResolveSavedIDs(ctx, owner, []domain.SavedStarGiftRef{
		canonical,
		{Owner: owner, Slug: "official-8001-1"},
	}); !errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
		t.Fatalf("duplicate official identities err=%v", err)
	}
}
