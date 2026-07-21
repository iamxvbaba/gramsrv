package postgres

import (
	"testing"

	"telesrv/internal/domain"
)

func TestTransferUniqueActionSavedIDNamespace(t *testing.T) {
	saved := domain.SavedStarGift{SavedID: 42, CanCraftAt: 1_780_000_123}
	unique := domain.UniqueStarGift{ID: 7}
	user := domain.Peer{Type: domain.PeerTypeUser, ID: 100}
	channel := domain.Peer{Type: domain.PeerTypeChannel, ID: 200}

	if action := transferUniqueAction(unique, 1, user, saved); action.SavedID != 0 || action.CanCraftAt != saved.CanCraftAt {
		t.Fatalf("user transfer action leaked channel saved_id: %+v", action)
	}
	if action := transferUniqueAction(unique, 1, channel, saved); action.SavedID != saved.SavedID || action.CanCraftAt != 0 {
		t.Fatalf("channel transfer action lost channel saved_id: %+v", action)
	}
}

func TestStarGiftCraftReadyAt(t *testing.T) {
	const date = 1_780_000_000
	if got := starGiftCraftReadyAt(date, 0); got != date {
		t.Fatalf("zero-delay craft ready_at = %d, want %d", got, date)
	}
	if got := starGiftCraftReadyAt(date, 60); got != date+60 {
		t.Fatalf("delayed craft ready_at = %d, want %d", got, date+60)
	}
	if got := starGiftCraftReadyAt(0, 0); got != 0 {
		t.Fatalf("invalid-date craft ready_at = %d, want 0", got)
	}
	if got := starGiftCraftReadyAt(1<<31-10, 60); got != 1<<31-1 {
		t.Fatalf("overflow craft ready_at = %d, want max int32", got)
	}
	if got := starGiftCraftReadyAt(1<<31+10, 0); got != 1<<31-1 {
		t.Fatalf("oversized-date craft ready_at = %d, want max int32", got)
	}
}

func TestEncodeSharedPrivateStarGiftMediaOmitsUserBoxLocalRefs(t *testing.T) {
	ordinary := &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGift,
			StarGift: &domain.MessageStarGiftAction{
				PeerUserID: 9,
				SavedID:    10, GiftMsgID: 11, UpgradeMsgID: 12,
			},
		},
	}
	encoded, err := encodeSharedPrivateStarGiftMedia(ordinary)
	if err != nil {
		t.Fatalf("encode ordinary shared projection: %v", err)
	}
	sharedOrdinary, err := decodeMessageMedia(string(encoded))
	if err != nil {
		t.Fatalf("decode ordinary shared projection: %v", err)
	}
	ordinaryAction := sharedOrdinary.ServiceAction.StarGift
	if ordinaryAction.SavedID != 0 || ordinaryAction.GiftMsgID != 0 || ordinaryAction.UpgradeMsgID != 0 {
		t.Fatalf("ordinary shared projection retained box-local refs: %+v", ordinaryAction)
	}
	if original := ordinary.ServiceAction.StarGift; original.SavedID != 10 || original.GiftMsgID != 11 || original.UpgradeMsgID != 12 {
		t.Fatalf("ordinary source projection was mutated: %+v", original)
	}

	unique := &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftUnique,
			StarGiftUnique: &domain.MessageStarGiftUniqueAction{
				Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 9}, SavedID: 13,
			},
		},
	}
	encoded, err = encodeSharedPrivateStarGiftMedia(unique)
	if err != nil {
		t.Fatalf("encode unique shared projection: %v", err)
	}
	sharedUnique, err := decodeMessageMedia(string(encoded))
	if err != nil {
		t.Fatalf("decode unique shared projection: %v", err)
	}
	if action := sharedUnique.ServiceAction.StarGiftUnique; action.SavedID != 0 {
		t.Fatalf("unique shared projection retained user saved_id: %+v", action)
	}
	if unique.ServiceAction.StarGiftUnique.SavedID != 13 {
		t.Fatalf("unique source projection was mutated: %+v", unique.ServiceAction.StarGiftUnique)
	}
}

func TestEncodeSharedPrivateStarGiftMediaPreservesChannelSavedID(t *testing.T) {
	media := &domain.MessageMedia{
		Kind: domain.MessageMediaKindService,
		ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGiftUnique,
			StarGiftUnique: &domain.MessageStarGiftUniqueAction{
				Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 9}, SavedID: 14,
			},
		},
	}
	encoded, err := encodeSharedPrivateStarGiftMedia(media)
	if err != nil {
		t.Fatalf("encode channel shared projection: %v", err)
	}
	shared, err := decodeMessageMedia(string(encoded))
	if err != nil {
		t.Fatalf("decode channel shared projection: %v", err)
	}
	if action := shared.ServiceAction.StarGiftUnique; action.SavedID != 14 {
		t.Fatalf("channel shared projection lost saved_id: %+v", action)
	}
}
