package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// TestMessagesGetAvailableEffects 验证消息特效 handler:effects 引用的文档去重进独立
// Documents 数组、optional 字段(static/anim/premium)正确编码、hash 命中回 NotModified。
func TestMessagesGetAvailableEffects(t *testing.T) {
	doc := func(id int64) domain.Document {
		return domain.Document{
			ID:         id,
			AccessHash: id * 10,
			MimeType:   "application/x-tgsticker",
			DCID:       2,
			Attributes: []domain.DocumentAttribute{
				{Kind: domain.DocAttrImageSize, W: 512, H: 512},
				{Kind: domain.DocAttrSticker, Alt: "\U0001f525", StickerSetID: 10, StickerSetAccessHash: 20},
				{Kind: domain.DocAttrAnimated},
				{Kind: domain.DocAttrFilename, FileName: "AnimatedSticker.tgs"},
			},
		}
	}
	files := &fakeFiles{
		docs: map[int64]domain.Document{1: doc(1), 2: doc(2), 3: doc(3)},
		effects: []domain.AvailableEffect{
			{ID: 100, Emoticon: "\U0001f525", StaticIconID: 1, EffectStickerID: 2, EffectAnimationID: 3},
			{ID: 101, Emoticon: "\U0001f4a5", EffectStickerID: 2, PremiumRequired: true},
		},
	}
	r := New(Config{}, Deps{Files: files}, zaptest.NewLogger(t), clock.System)
	ctx := context.Background()

	res, err := r.onMessagesGetAvailableEffects(ctx, 0)
	if err != nil {
		t.Fatalf("getAvailableEffects: %v", err)
	}
	full, ok := res.(*tg.MessagesAvailableEffects)
	if !ok {
		t.Fatalf("res = %T, want *tg.MessagesAvailableEffects", res)
	}
	if len(full.Effects) != 2 {
		t.Fatalf("effects = %d, want 2", len(full.Effects))
	}
	if full.Hash == 0 {
		t.Fatalf("hash = 0, want stable nonzero")
	}
	// 文档去重:effect 引用 1/2/3 + 2 → 3 个唯一文档。
	if len(full.Documents) != 3 {
		t.Fatalf("documents = %d, want 3 deduped", len(full.Documents))
	}
	for _, item := range full.Documents {
		d, ok := item.(*tg.Document)
		if !ok {
			t.Fatalf("document = %T, want *tg.Document", item)
		}
		hasSticker := false
		hasAnimated := false
		for _, attr := range d.Attributes {
			switch attr.(type) {
			case *tg.DocumentAttributeSticker:
				hasSticker = true
			case *tg.DocumentAttributeAnimated:
				hasAnimated = true
			}
		}
		if !hasSticker {
			t.Fatalf("document %d missing sticker attribute", d.ID)
		}
		if hasAnimated {
			t.Fatalf("document %d has documentAttributeAnimated; TDesktop treats TGS stickers as plain animations", d.ID)
		}
	}
	e0 := full.Effects[0]
	if e0.ID != 100 || e0.Emoticon != "\U0001f525" || e0.EffectStickerID != 2 {
		t.Fatalf("effect0 = %#v", e0)
	}
	if v, ok := e0.GetStaticIconID(); !ok || v != 1 {
		t.Fatalf("effect0 static icon = %d ok=%v, want 1", v, ok)
	}
	if v, ok := e0.GetEffectAnimationID(); !ok || v != 3 {
		t.Fatalf("effect0 anim = %d ok=%v, want 3", v, ok)
	}
	if full.Effects[0].PremiumRequired {
		t.Fatalf("effect0 premium = true, want false")
	}
	if !full.Effects[1].PremiumRequired {
		t.Fatalf("effect1 premium = false, want true")
	}

	again, err := r.onMessagesGetAvailableEffects(ctx, full.Hash)
	if err != nil {
		t.Fatalf("getAvailableEffects (cached): %v", err)
	}
	if _, ok := again.(*tg.MessagesAvailableEffectsNotModified); !ok {
		t.Fatalf("matching hash = %T, want NotModified", again)
	}
}

// TestMessagesGetAvailableEffectsEmpty 验证无 effects 时回空(非 NotModified),不让客户端卡住。
func TestMessagesGetAvailableEffectsEmpty(t *testing.T) {
	r := New(Config{}, Deps{Files: &fakeFiles{}}, zaptest.NewLogger(t), clock.System)
	res, err := r.onMessagesGetAvailableEffects(context.Background(), 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	empty, ok := res.(*tg.MessagesAvailableEffects)
	if !ok || len(empty.Effects) != 0 {
		t.Fatalf("res = %#v, want empty availableEffects", res)
	}
}
