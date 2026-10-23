package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestQuickReplyMediaAndEditPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner := createTestUser(t, ctx, NewUserStore(pool), "7904"+suffix, "Quick", "Media")
	store := NewPasswordStore(pool)
	photo := &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 77, AccessHash: 78, Date: 1}}

	first, err := store.SaveQuickReplyText(ctx, owner.ID, "photo", domain.QuickReplyMessage{RandomID: 1, Date: 10, Message: "", Media: photo, GroupedID: 555})
	if err != nil {
		t.Fatalf("save media quick reply: %v", err)
	}
	if _, err := store.SaveQuickReplyText(ctx, owner.ID, "photo", domain.QuickReplyMessage{RandomID: 2, Date: 11, Message: "caption two", Media: photo, GroupedID: 555}); err != nil {
		t.Fatal(err)
	}
	list, err := store.GetQuickReplyMessages(ctx, owner.ID, first.ShortcutID, nil)
	if err != nil || len(list.Messages) != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	got := list.Messages[0]
	if got.Media == nil || got.Media.Photo == nil || got.Media.Photo.ID != 77 || got.GroupedID != 555 {
		t.Fatalf("stored media = %+v grouped=%d", got.Media, got.GroupedID)
	}
	edited, err := store.EditQuickReplyMessage(ctx, owner.ID, domain.QuickReplyEdit{ShortcutID: first.ShortcutID, ID: got.ID, Message: "new caption", EditDate: 20})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Message.Message != "new caption" || edited.Message.Media == nil || edited.Message.EditDate != 20 {
		t.Fatalf("edited = %+v", edited.Message)
	}
	if _, err := store.EditQuickReplyMessage(ctx, owner.ID, domain.QuickReplyEdit{ShortcutID: first.ShortcutID, ID: 999999, Message: "x"}); err == nil {
		t.Fatal("editing a missing message succeeded")
	}
}
