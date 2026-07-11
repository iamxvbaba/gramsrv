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

func stickerCollectionRouter(t *testing.T) (*Router, *captureSessions) {
	t.Helper()
	files := &fakeFiles{docs: map[int64]domain.Document{
		101: {ID: 101, AccessHash: 11, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
		102: {ID: 102, AccessHash: 12, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
		201: {ID: 201, AccessHash: 21, MimeType: "video/mp4", Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrAnimated},
			{Kind: domain.DocAttrVideo, W: 320, H: 240, Duration: 1},
		}},
		202: {ID: 202, AccessHash: 22, MimeType: "video/mp4", Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrAnimated},
			{Kind: domain.DocAttrVideo, W: 640, H: 360, Duration: 2},
		}},
		301: {ID: 301, AccessHash: 31, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrAudio}}},
	}}
	passwordStore := memory.NewPasswordStore()
	sessions := &captureSessions{}
	return New(Config{}, Deps{
		Account:  appaccount.NewService(passwordStore, appaccount.WithStickerCollections(passwordStore)),
		Files:    files,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System), sessions
}

func inputDoc(id, accessHash int64) *tg.InputDocument {
	return &tg.InputDocument{ID: id, AccessHash: accessHash}
}

// TestFavedStickersRoundTrip 回归：faveSticker/getFavedStickers 此前未注册/返空。
func TestFavedStickersRoundTrip(t *testing.T) {
	r, _ := stickerCollectionRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	// 非贴纸文档拒绝。
	if ok, err := r.onMessagesFaveSticker(ctx, &tg.MessagesFaveStickerRequest{ID: inputDoc(301, 31)}); ok || !tgerr.Is(err, "STICKER_DOCUMENT_INVALID") {
		t.Fatalf("fave non-sticker = ok %v err %v, want STICKER_DOCUMENT_INVALID", ok, err)
	}

	// fave 101 → 102（最新在前）。
	for _, id := range []int64{101, 102} {
		if ok, err := r.onMessagesFaveSticker(ctx, &tg.MessagesFaveStickerRequest{ID: inputDoc(id, id%100+10)}); err != nil || !ok {
			t.Fatalf("fave %d = ok %v err %v", id, ok, err)
		}
	}
	faved := favedStickerIDs(t, r, ctx, 0)
	if len(faved) != 2 || faved[0] != 102 || faved[1] != 101 {
		t.Fatalf("faved = %v, want [102 101]（最新在前）", faved)
	}

	// not-modified：用返回 hash 再请求。
	full, err := r.onMessagesGetFavedStickers(ctx, 0)
	if err != nil {
		t.Fatalf("get faved: %v", err)
	}
	hash := full.(*tg.MessagesFavedStickers).Hash
	again, err := r.onMessagesGetFavedStickers(ctx, hash)
	if err != nil {
		t.Fatalf("get faved again: %v", err)
	}
	if _, ok := again.(*tg.MessagesFavedStickersNotModified); !ok {
		t.Fatalf("re-get with hash = %T, want NotModified", again)
	}

	// unfave 101 → 只剩 102。
	if ok, err := r.onMessagesFaveSticker(ctx, &tg.MessagesFaveStickerRequest{ID: inputDoc(101, 11), Unfave: true}); err != nil || !ok {
		t.Fatalf("unfave 101 = ok %v err %v", ok, err)
	}
	if faved := favedStickerIDs(t, r, ctx, 0); len(faved) != 1 || faved[0] != 102 {
		t.Fatalf("faved after unfave = %v, want [102]", faved)
	}
}

// TestRecentStickersRoundTrip 验证 saveRecentSticker/getRecentStickers + dates + clear。
func TestRecentStickersRoundTrip(t *testing.T) {
	r, _ := stickerCollectionRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	if ok, err := r.onMessagesSaveRecentSticker(ctx, &tg.MessagesSaveRecentStickerRequest{ID: inputDoc(101, 11)}); err != nil || !ok {
		t.Fatalf("save recent = ok %v err %v", ok, err)
	}
	out, err := r.onMessagesGetRecentStickers(ctx, &tg.MessagesGetRecentStickersRequest{})
	if err != nil {
		t.Fatalf("get recent: %v", err)
	}
	recent := out.(*tg.MessagesRecentStickers)
	if len(recent.Stickers) != 1 {
		t.Fatalf("recent stickers = %d, want 1", len(recent.Stickers))
	}
	if len(recent.Dates) != 1 || recent.Dates[0] == 0 {
		t.Fatalf("recent dates = %v, want one non-zero date", recent.Dates)
	}

	// clear → 空。
	if ok, err := r.onMessagesClearRecentStickers(ctx, &tg.MessagesClearRecentStickersRequest{}); err != nil || !ok {
		t.Fatalf("clear recent = ok %v err %v", ok, err)
	}
	out, _ = r.onMessagesGetRecentStickers(ctx, &tg.MessagesGetRecentStickersRequest{})
	if got := out.(*tg.MessagesRecentStickers); len(got.Stickers) != 0 {
		t.Fatalf("recent after clear = %d, want 0", len(got.Stickers))
	}
}

// TestSavedGifsRoundTrip 验证 saveGif/getSavedGifs + 非 GIF 拒绝。
func TestSavedGifsRoundTrip(t *testing.T) {
	r, sessions := stickerCollectionRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	// 非 GIF（贴纸）拒绝。
	if ok, err := r.onMessagesSaveGif(ctx, &tg.MessagesSaveGifRequest{ID: inputDoc(101, 11)}); ok || !tgerr.Is(err, "GIF_ID_INVALID") {
		t.Fatalf("save non-gif = ok %v err %v, want GIF_ID_INVALID", ok, err)
	}
	if ok, err := r.onMessagesSaveGif(ctx, &tg.MessagesSaveGifRequest{ID: inputDoc(201, 21)}); err != nil || !ok {
		t.Fatalf("save gif = ok %v err %v", ok, err)
	}
	if pushed, ok := sessions.lastUserPush().(*tg.Updates); !ok || len(pushed.Updates) != 1 {
		t.Fatalf("save gif push = %T %+v, want updateSavedGifs", sessions.lastUserPush(), pushed)
	} else if _, ok := pushed.Updates[0].(*tg.UpdateSavedGifs); !ok {
		t.Fatalf("save gif update = %T, want *tg.UpdateSavedGifs", pushed.Updates[0])
	}
	if ok, err := r.onMessagesSaveGif(ctx, &tg.MessagesSaveGifRequest{ID: inputDoc(202, 22)}); err != nil || !ok {
		t.Fatalf("save second gif = ok %v err %v", ok, err)
	}
	out, err := r.onMessagesGetSavedGifs(ctx, 0)
	if err != nil {
		t.Fatalf("get saved gifs: %v", err)
	}
	full := out.(*tg.MessagesSavedGifs)
	if len(full.Gifs) != 2 || full.Gifs[0].(*tg.Document).ID != 202 {
		t.Fatalf("saved gifs = %+v, want newest 202 first", full.Gifs)
	}
	again, err := r.onMessagesGetSavedGifs(ctx, full.Hash)
	if err != nil {
		t.Fatalf("get saved gifs by hash: %v", err)
	}
	if _, ok := again.(*tg.MessagesSavedGifsNotModified); !ok {
		t.Fatalf("get saved gifs by hash = %T, want NotModified", again)
	}
	if ok, err := r.onMessagesSaveGif(ctx, &tg.MessagesSaveGifRequest{ID: inputDoc(201, 21), Unsave: true}); err != nil || !ok {
		t.Fatalf("unsave gif = ok %v err %v", ok, err)
	}
	out, err = r.onMessagesGetSavedGifs(ctx, 0)
	if err != nil {
		t.Fatalf("get after unsave: %v", err)
	}
	if got := out.(*tg.MessagesSavedGifs); len(got.Gifs) != 1 || got.Gifs[0].(*tg.Document).ID != 202 {
		t.Fatalf("saved gifs after unsave = %+v, want [202]", got.Gifs)
	}
}

func favedStickerIDs(t *testing.T, r *Router, ctx context.Context, hash int64) []int64 {
	t.Helper()
	out, err := r.onMessagesGetFavedStickers(ctx, hash)
	if err != nil {
		t.Fatalf("get faved: %v", err)
	}
	faved, ok := out.(*tg.MessagesFavedStickers)
	if !ok {
		t.Fatalf("get faved = %T, want *tg.MessagesFavedStickers", out)
	}
	ids := make([]int64, 0, len(faved.Stickers))
	for _, d := range faved.Stickers {
		if doc, ok := d.(*tg.Document); ok {
			ids = append(ids, doc.ID)
		}
	}
	return ids
}
