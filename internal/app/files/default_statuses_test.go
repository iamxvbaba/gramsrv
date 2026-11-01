package files

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func defaultStatusesTestService(t *testing.T) (*Service, *fakeMediaStore) {
	t.Helper()
	media := newFakeMediaStore()
	return NewService(media, nil, 2), media
}

func putAnimatedEmojiSet(t *testing.T, media *fakeMediaStore, packs []domain.StickerPack) {
	t.Helper()
	ctx := context.Background()
	var ids []int64
	for _, p := range packs {
		ids = append(ids, p.DocumentIDs...)
	}
	for _, id := range ids {
		if err := media.PutDocument(ctx, domain.Document{ID: id, AccessHash: id, MimeType: "application/x-tgsticker"}); err != nil {
			t.Fatalf("put document %d: %v", id, err)
		}
	}
	if err := media.PutStickerSet(ctx, domain.StickerSet{
		ID:          1,
		ShortName:   "AnimatedEmojies",
		Kind:        domain.StickerSetKindSystem,
		SystemKey:   "animated_emoji",
		Animated:    true,
		DocumentIDs: ids,
		Packs:       packs,
	}); err != nil {
		t.Fatalf("put animated_emoji set: %v", err)
	}
}

func TestEnsureDefaultEmojiStatusSet(t *testing.T) {
	ctx := context.Background()
	svc, media := defaultStatusesTestService(t)
	putAnimatedEmojiSet(t, media, []domain.StickerPack{
		// The seed export is usually bare code points (no FE0F); the curated list must match both forms.
		{Emoticon: "❤", DocumentIDs: []int64{101}},
		{Emoticon: "👍", DocumentIDs: []int64{102}},
		{Emoticon: "☕️", DocumentIDs: []int64{103}}, // the reverse form with FE0F
		{Emoticon: "🥔", DocumentIDs: []int64{999}},  // not in the curated list, must be excluded
	})

	count, created, err := svc.EnsureDefaultEmojiStatusSet(ctx)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !created || count != 3 {
		t.Fatalf("ensure = (count=%d, created=%v), want (3, true)", count, created)
	}
	set, found, err := media.GetStickerSetBySystemKey(ctx, domain.StickerSetSystemKeyEmojiDefaultStatuses)
	if err != nil || !found {
		t.Fatalf("synthesized set not found: found=%v err=%v", found, err)
	}
	if set.Kind != domain.StickerSetKindSystem || !set.Emojis || set.Count != 3 || set.Hash == 0 {
		t.Fatalf("set meta = %+v, want system kind, emojis, count 3, non-zero hash", set)
	}
	got := map[int64]bool{}
	for _, id := range set.DocumentIDs {
		got[id] = true
	}
	if !got[101] || !got[102] || !got[103] || got[999] {
		t.Fatalf("document ids = %v, want 101/102/103 without 999", set.DocumentIDs)
	}
	// Curated order: ☕(=103) before ❤(=101), and ❤ before 👍(=102) (by list order, not pack order).
	index := map[int64]int{}
	for i, id := range set.DocumentIDs {
		index[id] = i
	}
	if !(index[103] < index[101] && index[101] < index[102]) {
		t.Fatalf("document order = %v, want curated order ☕<❤<👍", set.DocumentIDs)
	}

	// Idempotent: the second call must not rebuild.
	count2, created2, err := svc.EnsureDefaultEmojiStatusSet(ctx)
	if err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	if created2 || count2 != 3 {
		t.Fatalf("ensure again = (count=%d, created=%v), want (3, false)", count2, created2)
	}

	// ResolveStickerSet (the service path for inputStickerSetEmojiDefaultStatuses) can resolve it.
	resolved, docs, found, err := svc.ResolveStickerSet(ctx, domain.StickerSetRef{
		Kind:      domain.StickerSetRefBySystem,
		SystemKey: domain.StickerSetSystemKeyEmojiDefaultStatuses,
	})
	if err != nil || !found {
		t.Fatalf("resolve: found=%v err=%v", found, err)
	}
	if resolved.ID != set.ID || len(docs) != 3 {
		t.Fatalf("resolve = set %d with %d docs, want %d with 3", resolved.ID, len(docs), set.ID)
	}
}

func TestEnsureDefaultEmojiStatusSetWithoutSeed(t *testing.T) {
	ctx := context.Background()
	svc, media := defaultStatusesTestService(t)
	count, created, err := svc.EnsureDefaultEmojiStatusSet(ctx)
	if err != nil {
		t.Fatalf("ensure without seed: %v", err)
	}
	if created || count != 0 {
		t.Fatalf("ensure without seed = (count=%d, created=%v), want (0, false)", count, created)
	}
	if _, found, _ := media.GetStickerSetBySystemKey(ctx, domain.StickerSetSystemKeyEmojiDefaultStatuses); found {
		t.Fatal("set must not be created without animated_emoji seed")
	}
}
