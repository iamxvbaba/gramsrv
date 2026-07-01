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

func userStickerSetRouter(t *testing.T) (*Router, *memory.PasswordStore, *captureSessions) {
	t.Helper()
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			101: {ID: 101, AccessHash: 11, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
			102: {ID: 102, AccessHash: 12, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
		},
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindStickers: {
				{ID: 10, AccessHash: 100, ShortName: "funny", Title: "Funny", Kind: domain.StickerSetKindStickers, Count: 1, Hash: 7, Installed: true, InstalledDate: 1, DocumentIDs: []int64{101}},
				{ID: 20, AccessHash: 200, ShortName: "work", Title: "Work", Kind: domain.StickerSetKindStickers, Count: 1, Hash: 8, Installed: true, InstalledDate: 1, DocumentIDs: []int64{102}},
			},
			domain.StickerSetKindEmoji: {
				{ID: 30, AccessHash: 300, ShortName: "emoji_fun", Title: "Emoji Fun", Kind: domain.StickerSetKindEmoji, Emojis: true, Count: 1, Hash: 9, Installed: true, InstalledDate: 1, DocumentIDs: []int64{101}},
			},
		},
	}
	passwordStore := memory.NewPasswordStore()
	sessions := &captureSessions{}
	router := New(Config{}, Deps{
		Account:  appaccount.NewService(passwordStore, appaccount.WithUserStickerSets(passwordStore)),
		Files:    files,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	return router, passwordStore, sessions
}

func TestMessagesInstallStickerSetPersistsAndPushesUpdate(t *testing.T) {
	r, store, sessions := userStickerSetRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	out, err := r.onMessagesInstallStickerSet(ctx, &tg.MessagesInstallStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "funny"},
	})
	if err != nil {
		t.Fatalf("install sticker set: %v", err)
	}
	if _, ok := out.(*tg.MessagesStickerSetInstallResultSuccess); !ok {
		t.Fatalf("install result = %T, want *tg.MessagesStickerSetInstallResultSuccess", out)
	}

	items, total, err := store.ListUserStickerSets(ctx, 1000000001, domain.StickerSetKindStickers, nil, 0, 10)
	if err != nil {
		t.Fatalf("list installed sets: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].StickerSetID != 10 || items[0].Archived {
		t.Fatalf("installed sets = total %d items %+v, want one active set 10", total, items)
	}
	assertStickerSetsUpdate(t, sessions.lastUserPush(), domain.StickerSetKindStickers, nil)
}

func TestMessagesInstallStickerSetUpdatesAllStickersProjection(t *testing.T) {
	r, _, _ := userStickerSetRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	before, err := r.onMessagesGetAllStickers(ctx, 0)
	if err != nil {
		t.Fatalf("get all stickers before install: %v", err)
	}
	if full, ok := before.(*tg.MessagesAllStickers); ok && len(full.Sets) != 0 {
		t.Fatalf("all stickers before install = %+v, want empty non-default catalog", full.Sets)
	}
	if _, err := r.onMessagesInstallStickerSet(ctx, &tg.MessagesInstallStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "funny"},
	}); err != nil {
		t.Fatalf("install sticker set: %v", err)
	}

	after, err := r.onMessagesGetAllStickers(ctx, 0)
	if err != nil {
		t.Fatalf("get all stickers after install: %v", err)
	}
	full, ok := after.(*tg.MessagesAllStickers)
	if !ok {
		t.Fatalf("get all stickers after install = %T, want *tg.MessagesAllStickers", after)
	}
	if len(full.Sets) != 1 || full.Sets[0].ID != 10 || full.Sets[0].InstalledDate == 0 {
		t.Fatalf("all stickers after install = %+v, want installed set 10", full.Sets)
	}

	if ok, err := r.onMessagesUninstallStickerSet(ctx, &tg.InputStickerSetID{ID: 10, AccessHash: 100}); err != nil || !ok {
		t.Fatalf("uninstall sticker set = %v %v", ok, err)
	}
	afterUninstall, err := r.onMessagesGetAllStickers(ctx, 0)
	if err != nil {
		t.Fatalf("get all stickers after uninstall: %v", err)
	}
	if full, ok := afterUninstall.(*tg.MessagesAllStickers); ok && len(full.Sets) != 0 {
		t.Fatalf("all stickers after uninstall = %+v, want empty", full.Sets)
	}
}

func TestMessagesEmptyViewerStickerSetsInvalidateOldClientHash(t *testing.T) {
	r, _, _ := userStickerSetRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	stickers, err := r.onMessagesGetAllStickers(ctx, 3041827464193675523)
	if err != nil {
		t.Fatalf("get all stickers with old hash: %v", err)
	}
	full, ok := stickers.(*tg.MessagesAllStickers)
	if !ok {
		t.Fatalf("get all stickers with old hash = %T, want full empty list", stickers)
	}
	if full.Hash != 0 || len(full.Sets) != 0 {
		t.Fatalf("empty sticker list = hash %d sets %+v, want hash 0 with no sets", full.Hash, full.Sets)
	}

	emoji, err := r.onMessagesGetEmojiStickers(ctx, 7254637046733671932)
	if err != nil {
		t.Fatalf("get emoji stickers with old hash: %v", err)
	}
	emojiFull, ok := emoji.(*tg.MessagesAllStickers)
	if !ok {
		t.Fatalf("get emoji stickers with old hash = %T, want full empty list", emoji)
	}
	if emojiFull.Hash != 0 || len(emojiFull.Sets) != 0 {
		t.Fatalf("empty emoji list = hash %d sets %+v, want hash 0 with no sets", emojiFull.Hash, emojiFull.Sets)
	}
}

func TestMessagesGetStickerSetUsesViewerInstallState(t *testing.T) {
	r, _, _ := userStickerSetRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	before, err := r.onMessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "funny"},
	})
	if err != nil {
		t.Fatalf("get sticker set before install: %v", err)
	}
	full, ok := before.(*tg.MessagesStickerSet)
	if !ok {
		t.Fatalf("get sticker set before install = %T, want *tg.MessagesStickerSet", before)
	}
	if full.Set.InstalledDate != 0 {
		t.Fatalf("preview installed_date before install = %d, want 0", full.Set.InstalledDate)
	}

	if _, err := r.onMessagesInstallStickerSet(ctx, &tg.MessagesInstallStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "funny"},
	}); err != nil {
		t.Fatalf("install sticker set: %v", err)
	}
	after, err := r.onMessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "funny"},
	})
	if err != nil {
		t.Fatalf("get sticker set after install: %v", err)
	}
	full, ok = after.(*tg.MessagesStickerSet)
	if !ok {
		t.Fatalf("get sticker set after install = %T, want *tg.MessagesStickerSet", after)
	}
	if full.Set.InstalledDate == 0 {
		t.Fatalf("preview installed_date after install = 0, want viewer install date")
	}
}

func TestMessagesInstallStickerSetRejectsBadAccessHash(t *testing.T) {
	r, store, sessions := userStickerSetRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	out, err := r.onMessagesInstallStickerSet(ctx, &tg.MessagesInstallStickerSetRequest{
		Stickerset: &tg.InputStickerSetID{ID: 10, AccessHash: 999},
	})
	if out != nil || !tgerr.Is(err, "STICKERSET_INVALID") {
		t.Fatalf("install with bad access hash = %T %v, want STICKERSET_INVALID", out, err)
	}
	items, total, err := store.ListUserStickerSets(ctx, 1000000001, domain.StickerSetKindStickers, nil, 0, 10)
	if err != nil {
		t.Fatalf("list installed sets: %v", err)
	}
	if total != 0 || len(items) != 0 {
		t.Fatalf("installed after rejected install = total %d items %+v, want empty", total, items)
	}
	if push := sessions.lastUserPush(); push != nil {
		t.Fatalf("push after rejected install = %T, want nil", push)
	}
}

func TestMessagesReorderAndToggleStickerSets(t *testing.T) {
	r, store, sessions := userStickerSetRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	for _, shortName := range []string{"funny", "work"} {
		if _, err := r.onMessagesInstallStickerSet(ctx, &tg.MessagesInstallStickerSetRequest{Stickerset: &tg.InputStickerSetShortName{ShortName: shortName}}); err != nil {
			t.Fatalf("install %s: %v", shortName, err)
		}
	}
	if ok, err := r.onMessagesReorderStickerSets(ctx, &tg.MessagesReorderStickerSetsRequest{Order: []int64{20, 10, 20, 0}}); err != nil || !ok {
		t.Fatalf("reorder = %v %v", ok, err)
	}
	if got := installedStickerSetIDs(t, store, ctx, 1000000001, domain.StickerSetKindStickers, nil); len(got) != 2 || got[0] != 20 || got[1] != 10 {
		t.Fatalf("installed order = %v, want [20 10]", got)
	}
	assertStickerSetsUpdate(t, sessions.lastUserPush(), domain.StickerSetKindStickers, []int64{20, 10})

	if ok, err := r.onMessagesToggleStickerSets(ctx, &tg.MessagesToggleStickerSetsRequest{
		Stickersets: []tg.InputStickerSetClass{&tg.InputStickerSetID{ID: 20, AccessHash: 200}},
		Uninstall:   true,
	}); err != nil || !ok {
		t.Fatalf("toggle uninstall = %v %v", ok, err)
	}
	if got := installedStickerSetIDs(t, store, ctx, 1000000001, domain.StickerSetKindStickers, nil); len(got) != 1 || got[0] != 10 {
		t.Fatalf("installed after toggle uninstall = %v, want [10]", got)
	}
	assertStickerSetsUpdate(t, sessions.lastUserPush(), domain.StickerSetKindStickers, nil)
}

func TestMessagesToggleEmojiStickerSetsUsesEmojiUpdateFlag(t *testing.T) {
	r, store, sessions := userStickerSetRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	if ok, err := r.onMessagesToggleStickerSets(ctx, &tg.MessagesToggleStickerSetsRequest{
		Stickersets: []tg.InputStickerSetClass{&tg.InputStickerSetShortName{ShortName: "emoji_fun"}},
	}); err != nil || !ok {
		t.Fatalf("toggle emoji install = %v %v", ok, err)
	}
	if got := installedStickerSetIDs(t, store, ctx, 1000000001, domain.StickerSetKindEmoji, nil); len(got) != 1 || got[0] != 30 {
		t.Fatalf("emoji installed sets = %v, want [30]", got)
	}
	assertStickerSetsUpdate(t, sessions.lastUserPush(), domain.StickerSetKindEmoji, nil)
}

func TestMessagesGetMyStickersEmptyUntilCreatorStore(t *testing.T) {
	r, _, _ := userStickerSetRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	out, err := r.onMessagesGetMyStickers(ctx, &tg.MessagesGetMyStickersRequest{Limit: 50})
	if err != nil {
		t.Fatalf("get my stickers: %v", err)
	}
	if out.Count != 0 || len(out.Sets) != 0 {
		t.Fatalf("get my stickers = count %d sets %d, want empty creator-owned page", out.Count, len(out.Sets))
	}
	out, err = r.onMessagesGetMyStickers(ctx, &tg.MessagesGetMyStickersRequest{Limit: domain.MaxInstalledStickerSets + 1})
	if out != nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("get my stickers over limit = %T %v, want LIMIT_INVALID", out, err)
	}
}

func installedStickerSetIDs(t *testing.T, store *memory.PasswordStore, ctx context.Context, userID int64, kind domain.StickerSetKind, archived *bool) []int64 {
	t.Helper()
	items, _, err := store.ListUserStickerSets(ctx, userID, kind, archived, 0, 10)
	if err != nil {
		t.Fatalf("list installed sticker sets: %v", err)
	}
	out := make([]int64, 0, len(items))
	for _, item := range items {
		out = append(out, item.StickerSetID)
	}
	return out
}

func assertStickerSetsUpdate(t *testing.T, push any, kind domain.StickerSetKind, order []int64) {
	t.Helper()
	updates, ok := push.(*tg.Updates)
	if !ok {
		t.Fatalf("push = %T, want *tg.Updates", push)
	}
	if len(updates.Updates) != 1 {
		t.Fatalf("push updates = %d, want 1", len(updates.Updates))
	}
	if order != nil {
		update, ok := updates.Updates[0].(*tg.UpdateStickerSetsOrder)
		if !ok {
			t.Fatalf("push update = %T, want *tg.UpdateStickerSetsOrder", updates.Updates[0])
		}
		if len(update.Order) != len(order) {
			t.Fatalf("order update = %v, want %v", update.Order, order)
		}
		for i := range order {
			if update.Order[i] != order[i] {
				t.Fatalf("order update = %v, want %v", update.Order, order)
			}
		}
		if update.Masks != (kind == domain.StickerSetKindMasks) || update.Emojis != (kind == domain.StickerSetKindEmoji) {
			t.Fatalf("order flags masks=%v emojis=%v for kind %s", update.Masks, update.Emojis, kind)
		}
		return
	}
	update, ok := updates.Updates[0].(*tg.UpdateStickerSets)
	if !ok {
		t.Fatalf("push update = %T, want *tg.UpdateStickerSets", updates.Updates[0])
	}
	if update.Masks != (kind == domain.StickerSetKindMasks) || update.Emojis != (kind == domain.StickerSetKindEmoji) {
		t.Fatalf("update flags masks=%v emojis=%v for kind %s", update.Masks, update.Emojis, kind)
	}
}
