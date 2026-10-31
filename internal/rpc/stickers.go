package rpc

import (
	"context"
	"encoding/json"
	"hash/fnv"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// This file wires the reaction / sticker resource RPCs to real seed data (documents /
// sticker_sets / available_reactions); when the Files service is missing or the
// resources are not imported, it falls back to the tdesktop compatibility stub.

func (r *Router) onMessagesGetAvailableReactions(ctx context.Context, hash int) (tg.MessagesAvailableReactionsClass, error) {
	if r.deps.Files == nil {
		return tdesktop.AvailableReactions(hash), nil
	}
	reactions, err := r.deps.Files.ListAvailableReactions(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if len(reactions) == 0 {
		return tdesktop.AvailableReactions(hash), nil
	}
	docs, err := r.deps.Files.GetDocuments(ctx, reactionDocumentIDs(reactions))
	if err != nil {
		return nil, internalErr()
	}
	docByID := documentsByID(docs)
	catalogHash := availableReactionsHash(reactions, docByID)
	if hash == catalogHash {
		return &tg.MessagesAvailableReactionsNotModified{}, nil
	}
	return tgAvailableReactions(reactions, docByID, catalogHash), nil
}

// onMessagesGetAvailableEffects returns the message-effects catalog (global static,
// seeded into memory). It mirrors getAvailableReactions: documents are batch
// preloaded and assembled by tgAvailableEffects, and a hash match returns
// NotModified.
func (r *Router) onMessagesGetAvailableEffects(ctx context.Context, hash int) (tg.MessagesAvailableEffectsClass, error) {
	empty := func() *tg.MessagesAvailableEffects {
		return &tg.MessagesAvailableEffects{Hash: 0, Effects: []tg.AvailableEffect{}, Documents: []tg.DocumentClass{}}
	}
	if r.deps.Files == nil {
		return empty(), nil
	}
	effects, catalogHash, err := r.deps.Files.AvailableEffects(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if len(effects) == 0 {
		return empty(), nil
	}
	// catalogHash is computed at seed time (global static); a hit returns NotModified
	// with zero DB queries - and must come before GetDocuments (the only PG hit).
	if hash == catalogHash {
		return &tg.MessagesAvailableEffectsNotModified{}, nil
	}
	docs, err := r.deps.Files.GetDocuments(ctx, effectDocumentIDs(effects))
	if err != nil {
		return nil, internalErr()
	}
	return tgAvailableEffects(effects, documentsByID(docs), catalogHash), nil
}

func (r *Router) onMessagesGetStickerSet(ctx context.Context, req *tg.MessagesGetStickerSetRequest) (tg.MessagesStickerSetClass, error) {
	if req == nil || r.deps.Files == nil {
		return tdesktop.StickerSet(req), nil
	}
	ref, ok := stickerSetRefFromInput(req.Stickerset)
	if !ok {
		return tdesktop.StickerSet(req), nil
	}
	set, docs, found, err := r.deps.Files.ResolveStickerSet(ctx, ref)
	if err != nil {
		return nil, internalErr()
	}
	// Observability: measure which sets clients repeatedly request (retries of the same
	// set vs many distinct sets). Unseeded sets go to the stub, with
	// ResolveStickerSet's negative cache short-circuiting PG; here we only log the
	// ref and the hit outcome.
	if r.log != nil {
		r.log.Debug("getStickerSet",
			zap.String("ref_kind", string(ref.Kind)),
			zap.String("short_name", ref.ShortName),
			zap.Int64("set_id", ref.ID),
			zap.String("system_key", ref.SystemKey),
			zap.Bool("found", found),
			zap.Int("request_hash", req.Hash),
		)
	}
	if !found {
		if fallbackSet, fallbackDocs, fallbackFound, fallbackErr := r.resolvePlaceholderStickerSet(ctx, ref); fallbackErr != nil {
			return nil, internalErr()
		} else if fallbackFound {
			if r.log != nil {
				r.log.Debug("getStickerSet placeholder fallback",
					zap.String("short_name", ref.ShortName),
					zap.Int64("fallback_set_id", fallbackSet.ID),
					zap.String("fallback_short_name", fallbackSet.ShortName),
					zap.Int("documents", len(fallbackDocs)),
					zap.Int("response_hash", fallbackSet.Hash),
				)
			}
			if req.Hash != 0 && req.Hash == fallbackSet.Hash {
				return &tg.MessagesStickerSetNotModified{}, nil
			}
			return tgMessagesStickerSet(fallbackSet, fallbackDocs), nil
		}
		// Unseeded system set / unknown short name: fall back to the compatibility
		// stub to avoid breaking clients.
		return tdesktop.StickerSet(req), nil
	}
	if req.Hash != 0 && req.Hash == set.Hash {
		return &tg.MessagesStickerSetNotModified{}, nil
	}
	set, err = r.stickerSetWithViewerInstallState(ctx, set)
	if err != nil {
		return nil, err
	}
	return tgMessagesStickerSet(set, docs), nil
}

func (r *Router) resolvePlaceholderStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	placeholder, ok := clientPlaceholderStickerSet(ref)
	if !ok {
		return domain.StickerSet{}, nil, false, nil
	}
	for _, candidate := range placeholderStickerSetCandidates() {
		set, docs, found, err := r.deps.Files.ResolveStickerSet(ctx, candidate)
		if err != nil || !found {
			if err != nil {
				return domain.StickerSet{}, nil, false, err
			}
			continue
		}
		if len(docs) >= androidPlaceholderStickerDocumentLimit {
			projectedSet, projectedDocs := projectClientPlaceholderStickerSet(placeholder, set, docs)
			if len(projectedDocs) == androidPlaceholderStickerDocumentLimit {
				return projectedSet, projectedDocs, true, nil
			}
		}
	}
	return domain.StickerSet{}, nil, false, nil
}

const androidPlaceholderStickerDocumentLimit = 7

// Android's two built-in placeholder names have no dedicated seed. The compatibility
// projection uses a stable, distinct identity so that reusing AnimatedEmoji's set
// id/hash does not make it overwrite the real 598-document cache.
var clientPlaceholderStickerSets = []domain.StickerSet{
	{
		ID:         7_776_100_000_000_001,
		AccessHash: 7_776_100_000_000_101,
		ShortName:  "tg_placeholders_android",
		Title:      "Android Placeholders",
		Kind:       domain.StickerSetKindSystem,
		Official:   true,
	},
	{
		ID:         7_776_100_000_000_002,
		AccessHash: 7_776_100_000_000_102,
		ShortName:  "tg_superplaceholders_android_2",
		Title:      "Android Super Placeholders",
		Kind:       domain.StickerSetKindSystem,
		Official:   true,
	},
}

func clientPlaceholderStickerSet(ref domain.StickerSetRef) (domain.StickerSet, bool) {
	for _, set := range clientPlaceholderStickerSets {
		switch ref.Kind {
		case domain.StickerSetRefByShortName:
			if ref.ShortName == set.ShortName {
				return set, true
			}
		case domain.StickerSetRefByID:
			if ref.ID == set.ID && ref.AccessHash == set.AccessHash {
				return set, true
			}
		}
	}
	return domain.StickerSet{}, false
}

func projectClientPlaceholderStickerSet(placeholder, source domain.StickerSet, docs []domain.Document) (domain.StickerSet, []domain.Document) {
	docByID := documentsByID(docs)
	selectedDocs := make([]domain.Document, 0, androidPlaceholderStickerDocumentLimit)
	selectedIDs := make([]int64, 0, androidPlaceholderStickerDocumentLimit)
	selected := make(map[int64]struct{}, androidPlaceholderStickerDocumentLimit)
	for _, id := range source.DocumentIDs {
		if len(selectedDocs) == androidPlaceholderStickerDocumentLimit {
			break
		}
		doc, found := docByID[id]
		if !found {
			continue
		}
		if _, duplicate := selected[id]; duplicate {
			continue
		}
		selected[id] = struct{}{}
		selectedIDs = append(selectedIDs, id)
		selectedDocs = append(selectedDocs, doc)
	}

	placeholder.Animated = source.Animated
	placeholder.Videos = source.Videos
	placeholder.Emojis = source.Emojis
	placeholder.TextColor = source.TextColor
	placeholder.Count = len(selectedIDs)
	placeholder.DocumentIDs = selectedIDs
	placeholder.Hash = projectedStickerSetHash(selectedIDs)
	placeholder.Packs = filterStickerPacks(source.Packs, selected)
	placeholder.Keywords = filterStickerKeywords(source.Keywords, selected)
	return placeholder, selectedDocs
}

func projectedStickerSetHash(ids []int64) int {
	hash := int(tdesktopCountHash(ids) & 0x7fffffff)
	if hash == 0 && len(ids) > 0 {
		return 1
	}
	return hash
}

func filterStickerPacks(packs []domain.StickerPack, selected map[int64]struct{}) []domain.StickerPack {
	out := make([]domain.StickerPack, 0, len(packs))
	for _, pack := range packs {
		filtered := domain.StickerPack{Emoticon: pack.Emoticon}
		for _, id := range pack.DocumentIDs {
			if _, ok := selected[id]; ok {
				filtered.DocumentIDs = append(filtered.DocumentIDs, id)
			}
		}
		if len(filtered.DocumentIDs) > 0 {
			out = append(out, filtered)
		}
	}
	return out
}

func filterStickerKeywords(keywords []domain.StickerKeyword, selected map[int64]struct{}) []domain.StickerKeyword {
	out := make([]domain.StickerKeyword, 0, len(keywords))
	for _, keyword := range keywords {
		if _, ok := selected[keyword.DocumentID]; ok {
			out = append(out, keyword)
		}
	}
	return out
}

func placeholderStickerSetCandidates() []domain.StickerSetRef {
	return []domain.StickerSetRef{
		{Kind: domain.StickerSetRefBySystem, SystemKey: "emoji_generic_animations"},
		{Kind: domain.StickerSetRefBySystem, SystemKey: "animated_emoji_animations"},
		{Kind: domain.StickerSetRefByShortName, ShortName: "EmojiGenericAnimations"},
		{Kind: domain.StickerSetRefByShortName, ShortName: "EmojiAnimations"},
		{Kind: domain.StickerSetRefBySystem, SystemKey: "animated_emoji"},
		{Kind: domain.StickerSetRefByShortName, ShortName: "AnimatedEmojies"},
	}
}

func (r *Router) onMessagesGetAllStickers(ctx context.Context, hash int64) (tg.MessagesAllStickersClass, error) {
	return r.allStickersForKind(ctx, hash, domain.StickerSetKindStickers)
}

func (r *Router) onMessagesGetEmojiStickers(ctx context.Context, hash int64) (tg.MessagesAllStickersClass, error) {
	return r.allStickersForKind(ctx, hash, domain.StickerSetKindEmoji)
}

func (r *Router) onMessagesGetEmojiStickerGroups(ctx context.Context, hash int) (tg.MessagesEmojiGroupsClass, error) {
	empty := func() tg.MessagesEmojiGroupsClass {
		return &tg.MessagesEmojiGroups{Hash: 0, Groups: []tg.EmojiGroupClass{}}
	}
	if r.deps.Files == nil {
		return empty(), nil
	}
	sets := r.stickerCatalogSets(ctx, domain.StickerSetKindEmoji)
	visible := make([]domain.StickerSet, 0, len(sets))
	for _, set := range sets {
		if set.ID == 0 || set.Archived {
			continue
		}
		visible = append(visible, set)
	}
	if len(visible) == 0 {
		return empty(), nil
	}
	catalogHash := emojiStickerGroupsHash(visible)
	if hash != 0 && hash == catalogHash {
		return &tg.MessagesEmojiGroupsNotModified{}, nil
	}
	iconEmojiID := emojiStickerGroupIconID(visible)
	if iconEmojiID == 0 {
		return empty(), nil
	}
	return &tg.MessagesEmojiGroups{
		Hash: catalogHash,
		Groups: []tg.EmojiGroupClass{
			&tg.EmojiGroupPremium{
				Title:       "Premium",
				IconEmojiID: iconEmojiID,
			},
		},
	}, nil
}

func (r *Router) onMessagesGetMaskStickers(ctx context.Context, hash int64) (tg.MessagesAllStickersClass, error) {
	return r.allStickersForKind(ctx, hash, domain.StickerSetKindMasks)
}

func (r *Router) allStickersForKind(ctx context.Context, hash int64, kind domain.StickerSetKind) (tg.MessagesAllStickersClass, error) {
	if r.deps.Files == nil {
		return messagesAllStickersEmpty(hash), nil
	}
	sets, handled, err := r.installedStickerSetsForViewer(ctx, kind)
	if err != nil {
		return nil, err
	}
	if !handled {
		// Compatibility for tests / the old in-memory path without per-user install
		// state: read the global installed flag from the catalog cache.
		sets = installedGlobalStickerSets(r.stickerCatalogSets(ctx, kind))
	}
	if len(sets) == 0 {
		return messagesAllStickersEmpty(hash), nil
	}
	catalogHash := stickerSetsCatalogHash(sets)
	if hash == catalogHash {
		return &tg.MessagesAllStickersNotModified{}, nil
	}
	return &tg.MessagesAllStickers{Hash: catalogHash, Sets: tgStickerSets(sets)}, nil
}

func (r *Router) installedStickerSetsForViewer(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, bool, error) {
	svc, ok := r.userStickerSetSvc()
	if !ok {
		return nil, false, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		if err != nil {
			return nil, true, internalErr()
		}
		return nil, true, nil
	}
	userSets, _, err := svc.ListUserStickerSets(ctx, userID, kind, nil, 0, domain.MaxInstalledStickerSets)
	if err != nil {
		return nil, true, internalErr()
	}
	out := make([]domain.StickerSet, 0, len(userSets))
	for _, item := range userSets {
		if item.Archived || item.StickerSetID == 0 {
			continue
		}
		set, _, found, err := r.deps.Files.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: item.StickerSetID})
		if err != nil {
			return nil, true, internalErr()
		}
		if !found || set.ID == 0 || set.Deleted || userStickerSetKind(set) != kind {
			continue
		}
		set = stickerSetWithoutViewerInstallState(set)
		out = append(out, stickerSetWithViewerInstallItem(set, item))
	}
	return out, true, nil
}

func (r *Router) stickerSetsWithViewerInstallState(ctx context.Context, kind domain.StickerSetKind, sets []domain.StickerSet) ([]domain.StickerSet, error) {
	out := make([]domain.StickerSet, 0, len(sets))
	byID := make(map[int64]int, len(sets))
	for _, set := range sets {
		set = stickerSetWithoutViewerInstallState(set)
		byID[set.ID] = len(out)
		out = append(out, set)
	}
	svc, ok := r.userStickerSetSvc()
	if !ok {
		return out, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return out, nil
	}
	userSets, _, err := svc.ListUserStickerSets(ctx, userID, kind, nil, 0, domain.MaxInstalledStickerSets)
	if err != nil {
		return nil, internalErr()
	}
	for _, item := range userSets {
		i, ok := byID[item.StickerSetID]
		if !ok {
			continue
		}
		out[i] = stickerSetWithViewerInstallItem(out[i], item)
	}
	return out, nil
}

func (r *Router) stickerSetWithViewerInstallState(ctx context.Context, set domain.StickerSet) (domain.StickerSet, error) {
	sets, err := r.stickerSetsWithViewerInstallState(ctx, userStickerSetKind(set), []domain.StickerSet{set})
	if err != nil {
		return domain.StickerSet{}, err
	}
	if len(sets) == 0 {
		return stickerSetWithoutViewerInstallState(set), nil
	}
	return sets[0], nil
}

func stickerSetWithoutViewerInstallState(set domain.StickerSet) domain.StickerSet {
	set.Installed = false
	set.InstalledDate = 0
	return set
}

func stickerSetWithViewerInstallItem(set domain.StickerSet, item domain.UserStickerSet) domain.StickerSet {
	set.Installed = true
	set.Archived = item.Archived
	set.InstalledDate = item.InstalledDate
	return set
}

func installedGlobalStickerSets(sets []domain.StickerSet) []domain.StickerSet {
	out := make([]domain.StickerSet, 0, len(sets))
	for _, set := range sets {
		if set.Installed && !set.Archived {
			out = append(out, set)
		}
	}
	return out
}

// featuredCoversPerSet bounds the number of cover stickers resolved per featured set
// (for the trending preview).
const featuredCoversPerSet = 5

func (r *Router) onMessagesGetFeaturedStickers(ctx context.Context, hash int64) (tg.MessagesFeaturedStickersClass, error) {
	return r.featuredStickersForKind(ctx, hash, domain.StickerSetKindStickers)
}

func (r *Router) onMessagesGetFeaturedEmojiStickers(ctx context.Context, hash int64) (tg.MessagesFeaturedStickersClass, error) {
	return r.featuredStickersForKind(ctx, hash, domain.StickerSetKindEmoji)
}

func (r *Router) onMessagesGetOldFeaturedStickers(ctx context.Context, req *tg.MessagesGetOldFeaturedStickersRequest) (tg.MessagesFeaturedStickersClass, error) {
	if req == nil {
		return r.onMessagesGetFeaturedStickers(ctx, 0)
	}
	full, err := r.onMessagesGetFeaturedStickers(ctx, req.Hash)
	if err != nil {
		return nil, err
	}
	// offset/limit page through the full featured list; the hash still covers
	// the whole list, so a paged request never answers NotModified.
	featured, ok := full.(*tg.MessagesFeaturedStickers)
	if !ok || req.Offset <= 0 && req.Limit <= 0 {
		return full, nil
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(featured.Sets) {
		offset = len(featured.Sets)
	}
	end := len(featured.Sets)
	if req.Limit > 0 && offset+req.Limit < end {
		end = offset + req.Limit
	}
	featured.Sets = featured.Sets[offset:end]
	return featured, nil
}

// featuredStickersForKind presents the seeded (non-archived) sticker/emoji sets as
// trending. Performance: first compare the set-catalog hash; a hit returns
// *NotModified, and cover-document resolution only happens on a cache miss (one
// batch GetDocuments), avoiding resolving covers on every request.
func (r *Router) featuredStickersForKind(ctx context.Context, hash int64, kind domain.StickerSetKind) (tg.MessagesFeaturedStickersClass, error) {
	// perf: read the sets from the catalog cache (a hash hit within the TTL avoids PG,
	// and covers are resolved only on a cache miss).
	sets, err := r.loadStickerCatalogSets(ctx, kind)
	if err != nil {
		return nil, internalErr()
	}
	visible := domain.SearchStickerSets(sets, kind, "")
	if len(visible) == 0 {
		return messagesFeaturedStickersEmpty(hash), nil
	}
	visible, err = r.stickerSetsWithViewerInstallState(ctx, kind, visible)
	if err != nil {
		return nil, err
	}
	catalogHash := featuredStickerSetsHash(visible)
	if hash != 0 && hash == catalogHash {
		// Critical perf short-circuit: the catalog is unchanged, so return directly
		// without resolving any cover documents.
		return &tg.MessagesFeaturedStickersNotModified{Count: len(visible)}, nil
	}
	covers, err := r.resolveFeaturedCovers(ctx, visible)
	if err != nil {
		return nil, internalErr()
	}
	covered := make([]tg.StickerSetCoveredClass, 0, len(visible))
	for _, s := range visible {
		covered = append(covered, featuredCoveredSet(s, covers))
	}
	return &tg.MessagesFeaturedStickers{
		Hash:   catalogHash,
		Count:  len(visible),
		Sets:   covered,
		Unread: []int64{},
	}, nil
}

// resolveFeaturedCovers batch-resolves the first few cover documents of every
// featured set (one query).
func (r *Router) resolveFeaturedCovers(ctx context.Context, sets []domain.StickerSet) (map[int64]domain.Document, error) {
	ids := make([]int64, 0, len(sets)*featuredCoversPerSet)
	for _, s := range sets {
		for i, id := range s.DocumentIDs {
			if i >= featuredCoversPerSet {
				break
			}
			if id != 0 {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	docs, err := r.deps.Files.GetDocuments(ctx, ids)
	if err != nil {
		return nil, err
	}
	return documentsByID(docs), nil
}

// featuredCoveredSet builds a covered set from the resolved covers; falls back to
// noCovered when there are none.
func featuredCoveredSet(s domain.StickerSet, covers map[int64]domain.Document) tg.StickerSetCoveredClass {
	out := make([]tg.DocumentClass, 0, featuredCoversPerSet)
	for i, id := range s.DocumentIDs {
		if i >= featuredCoversPerSet {
			break
		}
		if doc, ok := covers[id]; ok {
			out = append(out, tgDocument(doc))
		}
	}
	if len(out) == 0 {
		return &tg.StickerSetNoCovered{Set: tgStickerSet(s)}
	}
	return &tg.StickerSetMultiCovered{Set: tgStickerSet(s), Covers: out}
}

func documentsByID(docs []domain.Document) map[int64]domain.Document {
	m := make(map[int64]domain.Document, len(docs))
	for _, d := range docs {
		m[d.ID] = d
	}
	return m
}

// availableReactionsHash covers every domain field that contributes to the TL
// response, including the embedded documents. A repaired file reference,
// attribute, thumbnail, title, or emoji must invalidate clients which cached an
// older response; hashing only document ids leaves those clients permanently on
// stale resources after a seed repair.
func availableReactionsHash(reactions []domain.AvailableReaction, docByID map[int64]domain.Document) int {
	h := fnv.New32a()
	for _, r := range reactions {
		encoded, _ := json.Marshal(r)
		_, _ = h.Write(encoded)
		_, _ = h.Write([]byte{0xff})
		for _, id := range r.DocumentIDs() {
			doc, ok := docByID[id]
			if !ok {
				// Missing mandatory/optional documents are part of the response as
				// documentEmpty{id}; keep that state hashable as well.
				doc.ID = id
			}
			encoded, _ = json.Marshal(doc)
			_, _ = h.Write(encoded)
			_, _ = h.Write([]byte{0xfe})
		}
	}
	sum := int(h.Sum32() & 0x7fffffff)
	if sum == 0 {
		return 1
	}
	return sum
}

func stickerSetsCatalogHash(sets []domain.StickerSet) int64 {
	values := make([]int64, 0, len(sets))
	for _, set := range sets {
		if set.ID == 0 {
			return 0
		}
		if set.Archived {
			continue
		}
		values = append(values, int64(set.Hash))
	}
	return int64(tdesktopCountHash(values))
}

func featuredStickerSetsHash(sets []domain.StickerSet) int64 {
	values := make([]int64, 0, len(sets))
	for _, set := range sets {
		if set.ID == 0 {
			return 0
		}
		if set.Archived {
			continue
		}
		values = append(values, set.ID)
	}
	return int64(tdesktopCountHash(values))
}

func emojiStickerGroupsHash(sets []domain.StickerSet) int {
	values := make([]int64, 0, len(sets)*2)
	for _, set := range sets {
		if set.ID == 0 || set.Archived {
			continue
		}
		values = append(values, set.ID, int64(set.Hash))
	}
	return int(tdesktopCountHash(values) & 0x7fffffff)
}

func emojiStickerGroupIconID(sets []domain.StickerSet) int64 {
	for _, set := range sets {
		if set.ThumbDocumentID != 0 {
			return set.ThumbDocumentID
		}
		for _, id := range set.DocumentIDs {
			if id != 0 {
				return id
			}
		}
	}
	return 0
}

func boolHashValue(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func tdesktopCountHash(values []int64) uint64 {
	var hash uint64
	for _, value := range values {
		hash = tdesktopHashUpdate(hash, value)
	}
	return hash
}

func tdesktopHashUpdate(hash uint64, value int64) uint64 {
	hash ^= hash >> 21
	hash ^= hash << 35
	hash ^= hash >> 4
	hash += uint64(value)
	return hash
}
