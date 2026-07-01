package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func stickerCreatorRouter(t *testing.T) (*Router, *fakeFiles, *memory.PasswordStore, *captureSessions) {
	t.Helper()
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			101: {ID: 101, AccessHash: 11, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
			102: {ID: 102, AccessHash: 12, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
			103: {ID: 103, AccessHash: 13, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
		},
		sets: map[domain.StickerSetKind][]domain.StickerSet{},
	}
	passwordStore := memory.NewPasswordStore()
	sessions := &captureSessions{}
	router := New(Config{}, Deps{
		Account:  appaccount.NewService(passwordStore, appaccount.WithUserStickerSets(passwordStore)),
		Files:    files,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	return router, files, passwordStore, sessions
}

func TestStickersCreateStickerSetInstallsAndInvalidatesCatalog(t *testing.T) {
	r, _, store, sessions := stickerCreatorRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	before, err := r.onMessagesGetAllStickers(ctx, 0)
	if err != nil {
		t.Fatalf("get all before create: %v", err)
	}
	if full, ok := before.(*tg.MessagesAllStickers); ok && len(full.Sets) != 0 {
		t.Fatalf("all stickers before create = %+v, want empty", full.Sets)
	}

	out, err := r.onStickersCreateStickerSet(ctx, &tg.StickersCreateStickerSetRequest{
		UserID:    &tg.InputUserSelf{},
		Title:     "Fresh Pack",
		ShortName: "fresh_pack",
		Stickers: []tg.InputStickerSetItem{{
			Document: &tg.InputDocument{ID: 101, AccessHash: 11},
			Emoji:    "🙂",
			Keywords: "fresh,happy",
		}},
	})
	if err != nil {
		t.Fatalf("create sticker set: %v", err)
	}
	full, ok := out.(*tg.MessagesStickerSet)
	if !ok {
		t.Fatalf("create result = %T, want *tg.MessagesStickerSet", out)
	}
	if full.Set.ShortName != "fresh_pack" || !full.Set.Creator || full.Set.InstalledDate == 0 {
		t.Fatalf("created set = %+v, want creator installed fresh_pack", full.Set)
	}
	if len(full.Packs) != 1 || len(full.Keywords) != 1 || len(full.Documents) != 1 {
		t.Fatalf("created payload packs=%d keywords=%d docs=%d, want 1/1/1", len(full.Packs), len(full.Keywords), len(full.Documents))
	}
	if got := installedStickerSetIDs(t, store, ctx, 1000000001, domain.StickerSetKindStickers, nil); len(got) != 1 || got[0] != full.Set.ID {
		t.Fatalf("installed created set ids = %v, want [%d]", got, full.Set.ID)
	}
	assertStickerSetsUpdate(t, sessions.lastUserPush(), domain.StickerSetKindStickers, nil)

	owned, err := r.onMessagesGetMyStickers(ctx, &tg.MessagesGetMyStickersRequest{Limit: 10})
	if err != nil {
		t.Fatalf("get my stickers: %v", err)
	}
	if owned.Count != 1 || len(owned.Sets) != 1 {
		t.Fatalf("my stickers = count %d sets %d, want one created set", owned.Count, len(owned.Sets))
	}

	after, err := r.onMessagesGetAllStickers(ctx, 0)
	if err != nil {
		t.Fatalf("get all after create: %v", err)
	}
	all, ok := after.(*tg.MessagesAllStickers)
	if !ok {
		t.Fatalf("all after create = %T, want *tg.MessagesAllStickers", after)
	}
	if len(all.Sets) != 1 || all.Sets[0].ID != full.Set.ID {
		t.Fatalf("all after create = %+v, want created set", all.Sets)
	}

	available, err := r.onStickersCheckShortName(ctx, "fresh_pack")
	if err != nil {
		t.Fatalf("check short name: %v", err)
	}
	if available {
		t.Fatalf("fresh_pack available = true, want false after create")
	}
}

func TestStickersCreateStickerSetRejectsBadDocumentAccessHash(t *testing.T) {
	r, _, _, _ := stickerCreatorRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	out, err := r.onStickersCreateStickerSet(ctx, &tg.StickersCreateStickerSetRequest{
		UserID:    &tg.InputUserSelf{},
		Title:     "Fresh Pack",
		ShortName: "fresh_pack",
		Stickers: []tg.InputStickerSetItem{{
			Document: &tg.InputDocument{ID: 101, AccessHash: 999},
			Emoji:    "🙂",
		}},
	})
	if out != nil || !tgerr.Is(err, "STICKER_FILE_INVALID") {
		t.Fatalf("create with bad document hash = %T %v, want STICKER_FILE_INVALID", out, err)
	}
}

func TestStickersSuggestAndCheckShortNameValidation(t *testing.T) {
	r, _, _, _ := stickerCreatorRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	suggested, err := r.onStickersSuggestShortName(ctx, "Fresh Pack")
	if err != nil {
		t.Fatalf("suggest short name: %v", err)
	}
	if suggested.ShortName == "" {
		t.Fatalf("suggested short name empty")
	}
	if ok, err := r.onStickersCheckShortName(ctx, "bad!"); ok || !tgerr.Is(err, "SHORT_NAME_INVALID") {
		t.Fatalf("check invalid short name = %v %v, want SHORT_NAME_INVALID", ok, err)
	}
}

func TestStickersManageCreatedStickerSetRPCs(t *testing.T) {
	r, files, _, sessions := stickerCreatorRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	created, err := r.onStickersCreateStickerSet(ctx, &tg.StickersCreateStickerSetRequest{
		UserID:    &tg.InputUserSelf{},
		Title:     "Fresh Pack",
		ShortName: "fresh_pack",
		Stickers: []tg.InputStickerSetItem{{
			Document: &tg.InputDocument{ID: 101, AccessHash: 11},
			Emoji:    "🙂",
		}},
	})
	if err != nil {
		t.Fatalf("create sticker set: %v", err)
	}
	full := created.(*tg.MessagesStickerSet)
	setInput := &tg.InputStickerSetID{ID: full.Set.ID, AccessHash: full.Set.AccessHash}

	added, err := r.onStickersAddStickerToSet(ctx, &tg.StickersAddStickerToSetRequest{
		Stickerset: setInput,
		Sticker: tg.InputStickerSetItem{
			Document: &tg.InputDocument{ID: 102, AccessHash: 12},
			Emoji:    "😄",
			Keywords: "smile",
		},
	})
	if err != nil {
		t.Fatalf("add sticker: %v", err)
	}
	addedFull := added.(*tg.MessagesStickerSet)
	if addedFull.Set.Count != 2 || len(addedFull.Documents) != 2 || len(addedFull.Keywords) != 1 {
		t.Fatalf("after add count=%d docs=%d keywords=%d, want 2/2/1", addedFull.Set.Count, len(addedFull.Documents), len(addedFull.Keywords))
	}
	assertStickerSetsUpdate(t, sessions.lastUserPush(), domain.StickerSetKindStickers, nil)

	moved, err := r.onStickersChangeStickerPosition(ctx, &tg.StickersChangeStickerPositionRequest{
		Sticker:  &tg.InputDocument{ID: 102, AccessHash: 12},
		Position: 0,
	})
	if err != nil {
		t.Fatalf("change sticker position: %v", err)
	}
	movedFull := moved.(*tg.MessagesStickerSet)
	if len(movedFull.Documents) != 2 || tgDocumentID(movedFull.Documents[0]) != 102 {
		t.Fatalf("documents after move = %+v, want doc 102 first", movedFull.Documents)
	}

	renamed, err := r.onStickersRenameStickerSet(ctx, &tg.StickersRenameStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "fresh_pack"},
		Title:      "Renamed Pack",
	})
	if err != nil {
		t.Fatalf("rename sticker set: %v", err)
	}
	renamedFull := renamed.(*tg.MessagesStickerSet)
	if renamedFull.Set.Title != "Renamed Pack" {
		t.Fatalf("renamed title = %q, want Renamed Pack", renamedFull.Set.Title)
	}

	removed, err := r.onStickersRemoveStickerFromSet(ctx, &tg.InputDocument{ID: 102, AccessHash: 12})
	if err != nil {
		t.Fatalf("remove sticker: %v", err)
	}
	removedFull := removed.(*tg.MessagesStickerSet)
	if removedFull.Set.Count != 1 || len(removedFull.Documents) != 1 || tgDocumentID(removedFull.Documents[0]) != 101 {
		t.Fatalf("after remove count=%d docs=%+v, want only doc 101", removedFull.Set.Count, removedFull.Documents)
	}

	ok, err := r.onStickersDeleteStickerSet(ctx, setInput)
	if err != nil || !ok {
		t.Fatalf("delete sticker set = %v %v, want true nil", ok, err)
	}
	if _, _, found, err := files.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: "fresh_pack"}); err != nil || found {
		t.Fatalf("resolve deleted set = found %v err %v, want miss", found, err)
	}
}

func TestStickersManageCreatedStickerSetRejectsNonCreator(t *testing.T) {
	r, _, _, _ := stickerCreatorRouter(t)
	ownerCtx := WithUserID(context.Background(), 1000000001)

	created, err := r.onStickersCreateStickerSet(ownerCtx, &tg.StickersCreateStickerSetRequest{
		UserID:    &tg.InputUserSelf{},
		Title:     "Fresh Pack",
		ShortName: "fresh_pack",
		Stickers: []tg.InputStickerSetItem{{
			Document: &tg.InputDocument{ID: 101, AccessHash: 11},
			Emoji:    "🙂",
		}},
	})
	if err != nil {
		t.Fatalf("create sticker set: %v", err)
	}
	full := created.(*tg.MessagesStickerSet)
	otherCtx := WithUserID(context.Background(), 1000000002)
	out, err := r.onStickersAddStickerToSet(otherCtx, &tg.StickersAddStickerToSetRequest{
		Stickerset: &tg.InputStickerSetID{ID: full.Set.ID, AccessHash: full.Set.AccessHash},
		Sticker: tg.InputStickerSetItem{
			Document: &tg.InputDocument{ID: 102, AccessHash: 12},
			Emoji:    "😄",
		},
	})
	if out != nil || !tgerr.Is(err, "STICKERSET_INVALID") {
		t.Fatalf("non-creator add = %T %v, want STICKERSET_INVALID", out, err)
	}
}

func tgDocumentID(doc tg.DocumentClass) int64 {
	if d, ok := doc.(*tg.Document); ok && d != nil {
		return d.ID
	}
	return 0
}
