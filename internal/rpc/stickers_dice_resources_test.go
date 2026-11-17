package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"telesrv/internal/domain"
	"testing"
)

func TestMissingDiceSetIsNotCachedAsAnEmptySuccess(t *testing.T) {
	for _, emoji := range []string{"🎲", "🎯", "🏀", "⚽", "🎳", "🎰"} {
		for _, files := range []FilesService{nil, &fakeFiles{}} {
			r := &Router{deps: Deps{Files: files}}
			_, err := r.onMessagesGetStickerSet(context.Background(), &tg.MessagesGetStickerSetRequest{Stickerset: &tg.InputStickerSetDice{Emoticon: emoji}})
			if !tgerr.Is(err, "STICKERSET_INVALID") {
				t.Fatalf("emoji=%q err=%v", emoji, err)
			}
		}
	}
}
func TestDiceSetVariationSelectorUsesCanonicalKey(t *testing.T) {
	ref, ok := stickerSetRefFromInput(&tg.InputStickerSetDice{Emoticon: "⚽️"})
	if !ok || ref.SystemKey != "dice:⚽" {
		t.Fatalf("ref=%+v ok=%v", ref, ok)
	}
}

func TestDiceSetRequiresEveryAnimationAndCanonicalCasinoOrder(t *testing.T) {
	doc := func(id int64) domain.Document {
		return domain.Document{ID: id, MimeType: "application/x-tgsticker", Size: 100, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}}
	}
	set := domain.StickerSet{}
	docs := []domain.Document{}
	for i := int64(1); i <= 21; i++ {
		set.DocumentIDs = append(set.DocumentIDs, i)
		docs = append(docs, doc(i))
	}
	if !completeDiceStickerSet(set, docs, "🎰") {
		t.Fatal("complete casino rejected")
	}
	if completeDiceStickerSet(set, docs[:20], "🎰") {
		t.Fatal("missing reel animation accepted")
	}
	docs[0], docs[1] = docs[1], docs[0]
	if completeDiceStickerSet(set, docs, "🎰") {
		t.Fatal("shifted casino order accepted")
	}
	docs[0], docs[1] = docs[1], docs[0]
	for i := 0; i <= 6; i++ {
		key := string(rune('0' + i))
		if i == 0 {
			key = "#"
		}
		set.Packs = append(set.Packs, domain.StickerPack{Emoticon: key + "\u20E3", DocumentIDs: []int64{int64(i + 1)}})
	}
	if !completeDiceStickerSet(set, docs, "🎲") {
		t.Fatal("complete dice rejected")
	}
	set.Packs = set.Packs[:6]
	if completeDiceStickerSet(set, docs, "🎲") {
		t.Fatal("missing final face accepted")
	}
	ref, ok := stickerSetRefFromInput(&tg.InputStickerSetDice{Emoticon: "⚽️"})
	if !ok || ref.SystemKey != "dice:⚽" {
		t.Fatalf("variation selector ref=%+v", ref)
	}
}
