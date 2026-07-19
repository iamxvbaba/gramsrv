package postgres

import (
	"testing"

	"telesrv/internal/domain"
)

func testCollectibleEmojiStatusValue() domain.UserEmojiStatus {
	return domain.UserEmojiStatus{
		DocumentID: 101,
		Until:      2_000_000_000,
		Collectible: domain.EmojiStatusCollectible{
			CollectibleID: 1001, DocumentID: 101, Title: "Gift", Slug: "Gift-1",
			PatternDocumentID: 102, CenterColor: 1, EdgeColor: 2, PatternColor: 3, TextColor: 4,
		},
	}
}

func TestCollectibleEmojiStatusUserAndEventCodecsRoundTrip(t *testing.T) {
	value := testCollectibleEmojiStatusValue()
	raw, id, err := encodeEmojiStatusCollectible(value)
	if err != nil {
		t.Fatalf("encode user collectible: %v", err)
	}
	if id == nil || *id != value.Collectible.CollectibleID {
		t.Fatalf("collectible id = %v", id)
	}
	if got := mustDecodeEmojiStatusCollectible(id, raw); got != value.Collectible {
		t.Fatalf("decoded user collectible = %+v, want %+v", got, value.Collectible)
	}

	eventRaw, err := encodeEventEmojiStatus(value)
	if err != nil {
		t.Fatalf("encode event collectible: %v", err)
	}
	got, err := decodeEventEmojiStatus(string(eventRaw))
	if err != nil || got != value {
		t.Fatalf("decoded event collectible = %+v err=%v, want %+v", got, err, value)
	}
}

func TestCollectibleEmojiStatusCodecRejectsPartialSnapshot(t *testing.T) {
	value := domain.UserEmojiStatus{
		DocumentID:  101,
		Collectible: domain.EmojiStatusCollectible{CollectibleID: 1001, DocumentID: 101},
	}
	if _, _, err := encodeEmojiStatusCollectible(value); err == nil {
		t.Fatal("partial user snapshot encoded successfully")
	}
	if _, err := encodeEventEmojiStatus(value); err == nil {
		t.Fatal("partial event snapshot encoded successfully")
	}
}
