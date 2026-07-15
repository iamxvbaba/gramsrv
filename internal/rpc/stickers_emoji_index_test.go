package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func emojiStickerRouter(t *testing.T) *Router {
	t.Helper()
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			201: {ID: 201, AccessHash: 1, MimeType: "application/x-tgsticker"},
			202: {ID: 202, AccessHash: 2, MimeType: "application/x-tgsticker"},
			301: {ID: 301, AccessHash: 3, MimeType: "application/x-tgsticker"},
		},
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindStickers: {
				{ID: 10, Hash: 1, Packs: []domain.StickerPack{
					{Emoticon: "👍", DocumentIDs: []int64{201, 202}},
					{Emoticon: "🔥", DocumentIDs: []int64{301}},
				}},
				{ID: 12, Hash: 2, Archived: true, Packs: []domain.StickerPack{
					{Emoticon: "👍", DocumentIDs: []int64{999}}, // 归档集应被排除
				}},
			},
		},
	}
	return New(Config{}, Deps{Files: files}, zaptest.NewLogger(t), clock.System)
}

func stickerDocIDs(t *testing.T, res tg.MessagesStickersClass) []int64 {
	t.Helper()
	full, ok := res.(*tg.MessagesStickers)
	if !ok {
		t.Fatalf("res = %T, want *tg.MessagesStickers", res)
	}
	ids := make([]int64, 0, len(full.Stickers))
	for _, d := range full.Stickers {
		if doc, ok := d.(*tg.Document); ok {
			ids = append(ids, doc.ID)
		}
	}
	return ids
}

// TestMessagesGetStickersByEmoji 验证 emoji→贴纸索引检索 + 归档排除 + hash notModified
// + hash=0 永不返回 NotModified（崩溃安全契约）+ 变体选择符归一化。
func TestMessagesGetStickersByEmoji(t *testing.T) {
	r := emojiStickerRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	first, err := r.onMessagesGetStickers(ctx, &tg.MessagesGetStickersRequest{Emoticon: "👍"})
	if err != nil {
		t.Fatalf("getStickers 👍: %v", err)
	}
	ids := stickerDocIDs(t, first)
	if len(ids) != 2 || ids[0] != 201 || ids[1] != 202 {
		t.Fatalf("👍 stickers = %v, want [201 202]（归档集 999 排除）", ids)
	}
	hash := first.(*tg.MessagesStickers).Hash
	if hash == 0 {
		t.Fatal("hash must be non-zero for cache round-trips")
	}

	// hash 命中 → NotModified。
	if again, err := r.onMessagesGetStickers(ctx, &tg.MessagesGetStickersRequest{Emoticon: "👍", Hash: hash}); err != nil {
		t.Fatalf("getStickers 👍 hash: %v", err)
	} else if _, ok := again.(*tg.MessagesStickersNotModified); !ok {
		t.Fatalf("re-get with hash = %T, want NotModified", again)
	}

	// 崩溃安全：hash=0 永远返回完整响应（DrKLO premium 预览强转，notModified 会闪退）。
	if zero, err := r.onMessagesGetStickers(ctx, &tg.MessagesGetStickersRequest{Emoticon: "👍", Hash: 0}); err != nil {
		t.Fatalf("getStickers 👍 hash=0: %v", err)
	} else if _, ok := zero.(*tg.MessagesStickers); !ok {
		t.Fatalf("hash=0 = %T, want full *tg.MessagesStickers (never NotModified)", zero)
	}

	// 另一个 emoji。
	if ids := stickerDocIDs(t, mustStickers(t, r, ctx, "🔥", 0)); len(ids) != 1 || ids[0] != 301 {
		t.Fatalf("🔥 stickers = %v, want [301]", ids)
	}

	// 变体选择符归一化：👍 + VS16 仍匹配。
	if ids := stickerDocIDs(t, mustStickers(t, r, ctx, "👍️", 0)); len(ids) != 2 {
		t.Fatalf("👍+VS16 stickers = %v, want 2 (variation selector 归一化)", ids)
	}

	// 未知 emoji → 空。
	if ids := stickerDocIDs(t, mustStickers(t, r, ctx, "🦄", 0)); len(ids) != 0 {
		t.Fatalf("unknown emoji stickers = %v, want empty", ids)
	}
}

func mustStickers(t *testing.T, r *Router, ctx context.Context, emoticon string, hash int64) tg.MessagesStickersClass {
	t.Helper()
	res, err := r.onMessagesGetStickers(ctx, &tg.MessagesGetStickersRequest{Emoticon: emoticon, Hash: hash})
	if err != nil {
		t.Fatalf("getStickers %q: %v", emoticon, err)
	}
	return res
}
