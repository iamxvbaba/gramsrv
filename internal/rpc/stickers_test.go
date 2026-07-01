package rpc

import (
	"context"
	"testing"

	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func TestStickerSetsCatalogHashMatchesTDesktopFormula(t *testing.T) {
	sets := []domain.StickerSet{
		{ID: 10, Hash: 123},
		{ID: 11, Hash: 456},
	}
	got := stickerSetsCatalogHash(sets)
	const want int64 = 4284229878340
	if got != want {
		t.Fatalf("stickerSetsCatalogHash() = %d, want %d", got, want)
	}
	if old := mediaCatalogHash([]int64{10, 123, 11, 456}); old == got {
		t.Fatalf("test fixture no longer distinguishes old media hash from TDesktop hash: %d", got)
	}
}

func TestFeaturedStickerSetsHashMatchesTDesktopFormula(t *testing.T) {
	sets := []domain.StickerSet{
		{ID: 10, Hash: 123},
		{ID: 11, Hash: 456},
	}
	got := featuredStickerSetsHash(sets)
	const want int64 = 365072220181
	if got != want {
		t.Fatalf("featuredStickerSetsHash() = %d, want %d", got, want)
	}
	if catalog := stickerSetsCatalogHash(sets); catalog == got {
		t.Fatalf("test fixture no longer distinguishes featured hash from catalog hash: %d", got)
	}
}

func TestMessagesGetAllStickersUsesTDesktopHashForNotModified(t *testing.T) {
	ctx := context.Background()
	files := &fakeFiles{
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindStickers: {
				{
					ID:         10,
					AccessHash: 100,
					ShortName:  "one",
					Title:      "One",
					Count:      1,
					Hash:       123,
					Installed:  true,
				},
				{
					ID:         11,
					AccessHash: 110,
					ShortName:  "two",
					Title:      "Two",
					Count:      1,
					Hash:       456,
					Installed:  true,
				},
			},
		},
	}
	r := &Router{deps: Deps{Files: files}}

	first, err := r.onMessagesGetAllStickers(ctx, 0)
	if err != nil {
		t.Fatalf("first getAllStickers: %v", err)
	}
	full, ok := first.(*tg.MessagesAllStickers)
	if !ok {
		t.Fatalf("first getAllStickers = %T, want *tg.MessagesAllStickers", first)
	}
	const wantHash int64 = 4284229878340
	if full.Hash != wantHash {
		t.Fatalf("first hash = %d, want %d", full.Hash, wantHash)
	}

	second, err := r.onMessagesGetAllStickers(ctx, full.Hash)
	if err != nil {
		t.Fatalf("second getAllStickers: %v", err)
	}
	if _, ok := second.(*tg.MessagesAllStickersNotModified); !ok {
		t.Fatalf("second getAllStickers = %T, want *tg.MessagesAllStickersNotModified", second)
	}
}

func TestAccountGetDefaultEmojiStatusesServesSynthesizedSet(t *testing.T) {
	ctx := context.Background()
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			101: {ID: 101, AccessHash: 1},
			102: {ID: 102, AccessHash: 2},
		},
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindSystem: {
				{
					ID:          77,
					ShortName:   "TelesrvDefaultStatuses",
					Kind:        domain.StickerSetKindSystem,
					SystemKey:   domain.StickerSetSystemKeyEmojiDefaultStatuses,
					Emojis:      true,
					Count:       2,
					Hash:        12345,
					DocumentIDs: []int64{101, 102},
				},
			},
		},
	}
	r := &Router{deps: Deps{Files: files}}

	first, err := r.onAccountGetDefaultEmojiStatuses(ctx, 0)
	if err != nil {
		t.Fatalf("first getDefaultEmojiStatuses: %v", err)
	}
	full, ok := first.(*tg.AccountEmojiStatuses)
	if !ok {
		t.Fatalf("first getDefaultEmojiStatuses = %T, want *tg.AccountEmojiStatuses", first)
	}
	if len(full.Statuses) != 2 {
		t.Fatalf("statuses = %d, want 2", len(full.Statuses))
	}
	status, ok := full.Statuses[0].(*tg.EmojiStatus)
	if !ok || status.DocumentID != 101 {
		t.Fatalf("statuses[0] = %#v, want EmojiStatus document 101", full.Statuses[0])
	}
	if full.Hash == 0 {
		t.Fatal("hash must be non-zero for cache round-trips")
	}

	second, err := r.onAccountGetDefaultEmojiStatuses(ctx, full.Hash)
	if err != nil {
		t.Fatalf("second getDefaultEmojiStatuses: %v", err)
	}
	if _, ok := second.(*tg.AccountEmojiStatusesNotModified); !ok {
		t.Fatalf("second getDefaultEmojiStatuses = %T, want notModified", second)
	}

	// getStickerSet(inputStickerSetEmojiDefaultStatuses) 走同一系统集。
	resolved, err := r.onMessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetEmojiDefaultStatuses{},
	})
	if err != nil {
		t.Fatalf("getStickerSet default statuses: %v", err)
	}
	setFull, ok := resolved.(*tg.MessagesStickerSet)
	if !ok {
		t.Fatalf("getStickerSet = %T, want *tg.MessagesStickerSet", resolved)
	}
	if setFull.Set.ID != 77 || len(setFull.Documents) != 2 {
		t.Fatalf("resolved set = %d with %d docs, want 77 with 2", setFull.Set.ID, len(setFull.Documents))
	}
}

func TestAccountGetDefaultEmojiStatusesFallsBackWhenUnseeded(t *testing.T) {
	ctx := context.Background()
	r := &Router{deps: Deps{Files: &fakeFiles{}}}
	res, err := r.onAccountGetDefaultEmojiStatuses(ctx, 0)
	if err != nil {
		t.Fatalf("getDefaultEmojiStatuses without seed: %v", err)
	}
	if _, ok := res.(*tg.AccountEmojiStatusesNotModified); !ok {
		t.Fatalf("unseeded result = %T, want compat notModified stub", res)
	}
}

func TestMessagesGetStickerSetAndroidPlaceholderUsesSeededSet(t *testing.T) {
	ctx := context.Background()
	docs := make(map[int64]domain.Document)
	documentIDs := make([]int64, 0, androidPlaceholderStickerMinDocuments)
	for i := 0; i < androidPlaceholderStickerMinDocuments; i++ {
		id := int64(100 + i)
		documentIDs = append(documentIDs, id)
		docs[id] = domain.Document{ID: id, AccessHash: id + 1000, DCID: 2}
	}
	files := &fakeFiles{
		docs: docs,
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindSystem: {
				{
					ID:          77,
					AccessHash:  7700,
					ShortName:   "AnimatedEmojies",
					Title:       "Animated Emoji",
					Kind:        domain.StickerSetKindSystem,
					SystemKey:   "animated_emoji",
					Emojis:      true,
					Count:       len(documentIDs),
					Hash:        12345,
					DocumentIDs: documentIDs,
				},
			},
		},
	}
	r := &Router{deps: Deps{Files: files}}

	res, err := r.onMessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "tg_placeholders_android"},
	})
	if err != nil {
		t.Fatalf("getStickerSet placeholder: %v", err)
	}
	full, ok := res.(*tg.MessagesStickerSet)
	if !ok {
		t.Fatalf("getStickerSet placeholder = %T, want *tg.MessagesStickerSet", res)
	}
	if full.Set.ID != 77 {
		t.Fatalf("placeholder fallback set id = %d, want 77", full.Set.ID)
	}
	if len(full.Documents) < androidPlaceholderStickerMinDocuments {
		t.Fatalf("placeholder fallback documents = %d, want >= %d", len(full.Documents), androidPlaceholderStickerMinDocuments)
	}
}

func TestMessagesGetStickerSetReturnsFullOnMatchingHash(t *testing.T) {
	ctx := context.Background()
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			201: {ID: 201, AccessHash: 21, DCID: 2},
		},
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindStickers: {
				{
					ID:          10,
					AccessHash:  100,
					ShortName:   "one",
					Title:       "One",
					Kind:        domain.StickerSetKindStickers,
					Count:       1,
					Hash:        123,
					DocumentIDs: []int64{201},
				},
			},
		},
	}
	r := &Router{deps: Deps{Files: files}}

	res, err := r.onMessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetID{ID: 10, AccessHash: 100},
		Hash:       123,
	})
	if err != nil {
		t.Fatalf("getStickerSet matching hash: %v", err)
	}
	full, ok := res.(*tg.MessagesStickerSet)
	if !ok {
		t.Fatalf("getStickerSet matching hash = %T, want *tg.MessagesStickerSet", res)
	}
	if full.Set.ID != 10 || len(full.Documents) != 1 {
		t.Fatalf("getStickerSet matching hash returned set %d docs %d, want set 10 with one doc", full.Set.ID, len(full.Documents))
	}
}

func TestMessagesGetStickerSetAndroidPlaceholderFallsBackToEmptyWithoutSeed(t *testing.T) {
	ctx := context.Background()
	r := &Router{deps: Deps{Files: &fakeFiles{}}}
	res, err := r.onMessagesGetStickerSet(ctx, &tg.MessagesGetStickerSetRequest{
		Stickerset: &tg.InputStickerSetShortName{ShortName: "tg_placeholders_android"},
	})
	if err != nil {
		t.Fatalf("getStickerSet placeholder without seed: %v", err)
	}
	full, ok := res.(*tg.MessagesStickerSet)
	if !ok {
		t.Fatalf("getStickerSet placeholder without seed = %T, want *tg.MessagesStickerSet", res)
	}
	if len(full.Documents) != 0 {
		t.Fatalf("placeholder without seed documents = %d, want empty compat set", len(full.Documents))
	}
}

func TestStickerSetRefFromSystemInputs(t *testing.T) {
	tests := []struct {
		name string
		in   tg.InputStickerSetClass
		want string
	}{
		{"emoji default statuses", &tg.InputStickerSetEmojiDefaultStatuses{}, domain.StickerSetSystemKeyEmojiDefaultStatuses},
		{"emoji channel default statuses", &tg.InputStickerSetEmojiChannelDefaultStatuses{}, domain.StickerSetSystemKeyEmojiDefaultStatuses},
		{"emoji default topic icons", &tg.InputStickerSetEmojiDefaultTopicIcons{}, domain.StickerSetSystemKeyEmojiDefaultTopicIcons},
		{"premium gifts", &tg.InputStickerSetPremiumGifts{}, domain.StickerSetSystemKeyPremiumGifts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref, ok := stickerSetRefFromInput(tt.in)
			if !ok {
				t.Fatalf("stickerSetRefFromInput(%T) not handled", tt.in)
			}
			if ref.Kind != domain.StickerSetRefBySystem || ref.SystemKey != tt.want {
				t.Fatalf("ref = %+v, want system key %q", ref, tt.want)
			}
		})
	}
}

func TestMessagesGetMaskStickersUsesMaskCatalog(t *testing.T) {
	ctx := context.Background()
	files := &fakeFiles{
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindStickers: {
				{ID: 10, AccessHash: 100, ShortName: "regular", Title: "Regular", Count: 1, Hash: 123, Installed: true},
			},
			domain.StickerSetKindMasks: {
				{ID: 20, AccessHash: 200, ShortName: "masks", Title: "Masks", Count: 1, Hash: 789, Installed: true},
			},
		},
	}
	r := &Router{deps: Deps{Files: files}}

	first, err := r.onMessagesGetMaskStickers(ctx, 0)
	if err != nil {
		t.Fatalf("first getMaskStickers: %v", err)
	}
	full, ok := first.(*tg.MessagesAllStickers)
	if !ok {
		t.Fatalf("first getMaskStickers = %T, want *tg.MessagesAllStickers", first)
	}
	if len(full.Sets) != 1 || full.Sets[0].ID != 20 {
		t.Fatalf("mask sets = %+v, want only mask set 20", full.Sets)
	}

	second, err := r.onMessagesGetMaskStickers(ctx, full.Hash)
	if err != nil {
		t.Fatalf("second getMaskStickers: %v", err)
	}
	if _, ok := second.(*tg.MessagesAllStickersNotModified); !ok {
		t.Fatalf("second getMaskStickers = %T, want *tg.MessagesAllStickersNotModified", second)
	}
}

func TestMessagesGetFeaturedStickersSurfacesSeededSets(t *testing.T) {
	ctx := context.Background()
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			201: {ID: 201, AccessHash: 1, MimeType: "application/x-tgsticker"},
			202: {ID: 202, AccessHash: 2, MimeType: "application/x-tgsticker"},
			301: {ID: 301, AccessHash: 3, MimeType: "application/x-tgsticker"},
		},
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindStickers: {
				{ID: 10, AccessHash: 100, ShortName: "one", Title: "One", Count: 2, Hash: 123, DocumentIDs: []int64{201, 202}},
				{ID: 11, AccessHash: 110, ShortName: "two", Title: "Two", Count: 1, Hash: 456, DocumentIDs: []int64{301}},
				{ID: 12, AccessHash: 120, ShortName: "arch", Title: "Arch", Hash: 789, Archived: true, DocumentIDs: []int64{301}},
			},
			domain.StickerSetKindEmoji: {
				{ID: 20, AccessHash: 200, ShortName: "emo", Title: "Emo", Count: 1, Hash: 999, Emojis: true, DocumentIDs: []int64{201}},
			},
		},
	}
	r := &Router{deps: Deps{Files: files}}

	first, err := r.onMessagesGetFeaturedStickers(ctx, 0)
	if err != nil {
		t.Fatalf("first getFeaturedStickers: %v", err)
	}
	full, ok := first.(*tg.MessagesFeaturedStickers)
	if !ok {
		t.Fatalf("first getFeaturedStickers = %T, want *tg.MessagesFeaturedStickers", first)
	}
	if full.Count != 2 || len(full.Sets) != 2 {
		t.Fatalf("featured = count %d / %d sets, want 2/2 (archived excluded)", full.Count, len(full.Sets))
	}
	covered, ok := full.Sets[0].(*tg.StickerSetMultiCovered)
	if !ok {
		t.Fatalf("featured set[0] = %T, want *tg.StickerSetMultiCovered", full.Sets[0])
	}
	if covered.Set.ID != 10 || len(covered.Covers) != 2 {
		t.Fatalf("featured set[0] = id %d with %d covers, want 10 with 2", covered.Set.ID, len(covered.Covers))
	}
	if full.Hash == 0 {
		t.Fatal("featured hash must be non-zero for cache round-trips")
	}

	second, err := r.onMessagesGetFeaturedStickers(ctx, full.Hash)
	if err != nil {
		t.Fatalf("second getFeaturedStickers: %v", err)
	}
	if _, ok := second.(*tg.MessagesFeaturedStickersNotModified); !ok {
		t.Fatalf("second getFeaturedStickers = %T, want NotModified", second)
	}

	// emoji featured 走 emoji 集。
	emoji, err := r.onMessagesGetFeaturedEmojiStickers(ctx, 0)
	if err != nil {
		t.Fatalf("getFeaturedEmojiStickers: %v", err)
	}
	emojiFull, ok := emoji.(*tg.MessagesFeaturedStickers)
	if !ok || emojiFull.Count != 1 {
		t.Fatalf("featured emoji = %T count %v, want 1 emoji set", emoji, emojiFull)
	}
}

func TestMessagesGetOldFeaturedStickersUsesFeaturedCatalog(t *testing.T) {
	ctx := context.Background()
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			201: {ID: 201, AccessHash: 21, DCID: 2},
		},
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindStickers: {
				{
					ID:          10,
					AccessHash:  100,
					ShortName:   "one",
					Title:       "One",
					Kind:        domain.StickerSetKindStickers,
					Count:       1,
					Hash:        123,
					DocumentIDs: []int64{201},
				},
			},
		},
	}
	r := &Router{deps: Deps{Files: files}}
	res, err := r.onMessagesGetOldFeaturedStickers(ctx, &tg.MessagesGetOldFeaturedStickersRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getOldFeaturedStickers: %v", err)
	}
	full, ok := res.(*tg.MessagesFeaturedStickers)
	if !ok {
		t.Fatalf("getOldFeaturedStickers = %T, want *tg.MessagesFeaturedStickers", res)
	}
	if len(full.Sets) != 1 {
		t.Fatalf("old featured sets = %d, want one", len(full.Sets))
	}
}

func TestMessagesGetEmojiStickerGroupsUsesSeededEmojiCatalog(t *testing.T) {
	ctx := context.Background()
	files := &fakeFiles{
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindEmoji: {
				{
					ID:              20,
					AccessHash:      200,
					ShortName:       "emoji",
					Title:           "Emoji",
					Kind:            domain.StickerSetKindEmoji,
					Count:           1,
					Hash:            999,
					Emojis:          true,
					ThumbDocumentID: 555,
					DocumentIDs:     []int64{201},
				},
			},
		},
	}
	r := &Router{deps: Deps{Files: files}}
	first, err := r.onMessagesGetEmojiStickerGroups(ctx, 0)
	if err != nil {
		t.Fatalf("getEmojiStickerGroups: %v", err)
	}
	full, ok := first.(*tg.MessagesEmojiGroups)
	if !ok {
		t.Fatalf("getEmojiStickerGroups = %T, want *tg.MessagesEmojiGroups", first)
	}
	if len(full.Groups) != 1 {
		t.Fatalf("emoji sticker groups = %d, want one", len(full.Groups))
	}
	group, ok := full.Groups[0].(*tg.EmojiGroupPremium)
	if !ok || group.IconEmojiID != 555 {
		t.Fatalf("emoji sticker group = %T %+v, want premium icon 555", full.Groups[0], full.Groups[0])
	}
	second, err := r.onMessagesGetEmojiStickerGroups(ctx, full.Hash)
	if err != nil {
		t.Fatalf("getEmojiStickerGroups cached: %v", err)
	}
	if _, ok := second.(*tg.MessagesEmojiGroupsNotModified); !ok {
		t.Fatalf("cached getEmojiStickerGroups = %T, want notModified", second)
	}
}

// countingStickerFiles 包 *fakeFiles 计数 ListStickerSets，验证目录缓存短路。
type countingStickerFiles struct {
	*fakeFiles
	listCalls int
}

func (f *countingStickerFiles) ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	f.listCalls++
	return f.fakeFiles.ListStickerSets(ctx, kind)
}

// TestStickerCatalogCacheShortCircuitsListStickerSets 回归 P2-5：getAllStickers/
// getFeaturedStickers 经目录缓存——同 kind 重复请求只 ListStickerSets 一次（TTL 内 0 PG）。
func TestStickerCatalogCacheShortCircuitsListStickerSets(t *testing.T) {
	ctx := context.Background()
	files := &countingStickerFiles{fakeFiles: &fakeFiles{
		docs: map[int64]domain.Document{201: {ID: 201, AccessHash: 1}},
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindStickers: {{ID: 10, AccessHash: 100, ShortName: "one", Count: 1, Hash: 123, DocumentIDs: []int64{201}}},
		},
	}}
	r := New(Config{}, Deps{Files: files}, zaptest.NewLogger(t), clock.System)

	for i := 0; i < 3; i++ {
		if _, err := r.onMessagesGetAllStickers(ctx, 0); err != nil {
			t.Fatalf("getAllStickers %d: %v", i, err)
		}
	}
	if _, err := r.onMessagesGetFeaturedStickers(ctx, 0); err != nil {
		t.Fatalf("getFeaturedStickers: %v", err)
	}
	if files.listCalls != 1 {
		t.Fatalf("ListStickerSets(stickers) calls = %d, want 1 (目录缓存应短路)", files.listCalls)
	}
}

func TestMessagesGetFeaturedStickersEmptyWithoutSeed(t *testing.T) {
	ctx := context.Background()
	r := &Router{deps: Deps{Files: &fakeFiles{}}}
	res, err := r.onMessagesGetFeaturedStickers(ctx, 0)
	if err != nil {
		t.Fatalf("getFeaturedStickers without seed: %v", err)
	}
	full, ok := res.(*tg.MessagesFeaturedStickers)
	if !ok || full.Count != 0 || len(full.Sets) != 0 {
		t.Fatalf("unseeded featured = %#v, want empty", res)
	}
}

func TestMessagesGetAvailableReactionsNotModified(t *testing.T) {
	ctx := context.Background()
	reactions := []domain.AvailableReaction{
		{
			Reaction:            "👍",
			Title:               "Like",
			StaticIconID:        101,
			AppearAnimationID:   102,
			SelectAnimationID:   103,
			ActivateAnimationID: 104,
			EffectAnimationID:   105,
			AroundAnimationID:   106,
			CenterIconID:        107,
		},
	}
	files := &fakeFiles{reactions: reactions}
	r := &Router{deps: Deps{Files: files}}

	first, err := r.onMessagesGetAvailableReactions(ctx, 0)
	if err != nil {
		t.Fatalf("first getAvailableReactions: %v", err)
	}
	full, ok := first.(*tg.MessagesAvailableReactions)
	if !ok {
		t.Fatalf("first getAvailableReactions = %T, want *tg.MessagesAvailableReactions", first)
	}
	if full.Hash == 0 {
		t.Fatal("first getAvailableReactions returned zero hash")
	}

	second, err := r.onMessagesGetAvailableReactions(ctx, full.Hash)
	if err != nil {
		t.Fatalf("second getAvailableReactions: %v", err)
	}
	if _, ok := second.(*tg.MessagesAvailableReactionsNotModified); !ok {
		t.Fatalf("second getAvailableReactions = %T, want *tg.MessagesAvailableReactionsNotModified", second)
	}
}

func TestTGDocumentCompactsCachedThumbToDownloadableSize(t *testing.T) {
	doc := tgDocument(domain.Document{
		ID:         100,
		AccessHash: 1,
		DCID:       2,
		MimeType:   "application/x-tgsticker",
		Thumbs: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindCached, Type: "m", W: 128, H: 128, Bytes: []byte("webp")},
		},
	})
	full, ok := doc.(*tg.Document)
	if !ok {
		t.Fatalf("tgDocument = %T, want *tg.Document", doc)
	}
	if len(full.Thumbs) != 1 {
		t.Fatalf("thumbs = %d, want 1", len(full.Thumbs))
	}
	size, ok := full.Thumbs[0].(*tg.PhotoSize)
	if !ok {
		t.Fatalf("thumb = %T, want *tg.PhotoSize", full.Thumbs[0])
	}
	if size.Type != "m" || size.W != 128 || size.H != 128 || size.Size != 4 {
		t.Fatalf("thumb size = %+v, want downloadable m 128x128 size=4", size)
	}
}

func TestTGDocumentDropsSeedSyntheticTGSPreviewThumb(t *testing.T) {
	doc := tgDocument(domain.Document{
		ID:         100,
		AccessHash: 1,
		DCID:       2,
		MimeType:   "application/x-tgsticker",
		Thumbs: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindCached, Type: "m", W: 1, H: 1, Bytes: []byte("png")},
		},
	})
	full, ok := doc.(*tg.Document)
	if !ok {
		t.Fatalf("tgDocument = %T, want *tg.Document", doc)
	}
	if len(full.Thumbs) != 0 {
		t.Fatalf("thumbs = %#v, want no synthetic TGS preview thumb", full.Thumbs)
	}
}

func TestTGDocumentAddsAnimatedAttributeForTGSSticker(t *testing.T) {
	doc := tgDocument(domain.Document{
		ID:         100,
		AccessHash: 1,
		DCID:       2,
		MimeType:   "application/x-tgsticker",
		Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrImageSize, W: 512, H: 512},
			{Kind: domain.DocAttrSticker, Alt: "🙂", StickerSetID: 10, StickerSetAccessHash: 20},
			{Kind: domain.DocAttrFilename, FileName: "AnimatedSticker.tgs"},
		},
	})
	full, ok := doc.(*tg.Document)
	if !ok {
		t.Fatalf("tgDocument = %T, want *tg.Document", doc)
	}
	hasSticker := false
	hasAnimated := false
	for _, attr := range full.Attributes {
		switch attr.(type) {
		case *tg.DocumentAttributeSticker:
			hasSticker = true
		case *tg.DocumentAttributeAnimated:
			hasAnimated = true
		}
	}
	if !hasSticker {
		t.Fatal("TGS sticker document missing sticker attribute")
	}
	if !hasAnimated {
		t.Fatal("TGS sticker document missing synthesized animated attribute")
	}
}

func TestTGDocumentUsesDomainDocumentID(t *testing.T) {
	const documentID int64 = 1382305375846410902

	doc := tgDocument(domain.Document{
		ID:         documentID,
		AccessHash: 1,
		DCID:       2,
		MimeType:   "application/x-tgsticker",
	})
	full, ok := doc.(*tg.Document)
	if !ok {
		t.Fatalf("tgDocument = %T, want *tg.Document", doc)
	}
	if full.ID != documentID {
		t.Fatalf("document id = %d, want %d", full.ID, documentID)
	}
}

func TestMessagesGetCustomEmojiDocumentsUsesDomainIDs(t *testing.T) {
	const documentID int64 = 1382305375846410902
	ctx := WithUserID(context.Background(), 1780269504)
	r := &Router{deps: Deps{Files: &fakeFiles{
		docs: map[int64]domain.Document{
			documentID: {
				ID:         documentID,
				AccessHash: 1,
				DCID:       2,
				MimeType:   "application/x-tgsticker",
			},
		},
	}}}

	docs, err := r.onMessagesGetCustomEmojiDocuments(ctx, []int64{documentID})
	if err != nil {
		t.Fatalf("getCustomEmojiDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
	doc, ok := docs[0].(*tg.Document)
	if !ok {
		t.Fatalf("doc = %T, want *tg.Document", docs[0])
	}
	if doc.ID != documentID {
		t.Fatalf("doc id = %d, want %d", doc.ID, documentID)
	}
}
