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

// 本文件把 reaction / sticker 资源 RPC 接到真实 seed 数据（documents / sticker_sets /
// available_reactions）。存储错误直接失败；未导入的客户端贴纸资源使用稳定兼容响应。

func (r *Router) onMessagesGetAvailableReactions(ctx context.Context, hash int) (tg.MessagesAvailableReactionsClass, error) {
	if r.deps.Files == nil {
		return nil, internalErr()
	}
	reactions, err := r.deps.Files.ListAvailableReactions(ctx)
	if err != nil {
		return nil, internalErr()
	}
	documentIDs := reactionDocumentIDs(reactions)
	docs, err := r.deps.Files.GetDocuments(ctx, documentIDs)
	if err != nil {
		return nil, internalErr()
	}
	docByID := documentsByID(docs)
	if !allDocumentsResolved(documentIDs, docByID) {
		return nil, internalErr()
	}
	catalogHash := availableReactionsHash(reactions, docByID)
	if hash != 0 && hash == catalogHash {
		return &tg.MessagesAvailableReactionsNotModified{}, nil
	}
	return tgAvailableReactions(reactions, docByID, catalogHash), nil
}

// onMessagesGetAvailableEffects 返回消息发送特效目录(全局静态,seed 进内存)。镜像
// getAvailableReactions:文档批量预加载后由 tgAvailableEffects 组装,hash 命中回 NotModified。
func (r *Router) onMessagesGetAvailableEffects(ctx context.Context, hash int) (tg.MessagesAvailableEffectsClass, error) {
	empty := func() *tg.MessagesAvailableEffects {
		return &tg.MessagesAvailableEffects{Hash: 0, Effects: []tg.AvailableEffect{}, Documents: []tg.DocumentClass{}}
	}
	if r.deps.Files == nil {
		return nil, internalErr()
	}
	effects, catalogHash, err := r.deps.Files.AvailableEffects(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if len(effects) == 0 {
		return empty(), nil
	}
	// catalogHash 在 seed 时算好(全局静态),命中即零查库返回 NotModified——必须在
	// GetDocuments(唯一打 PG 的点)之前。
	if hash == catalogHash {
		return &tg.MessagesAvailableEffectsNotModified{}, nil
	}
	documentIDs := effectDocumentIDs(effects)
	docs, err := r.deps.Files.GetDocuments(ctx, documentIDs)
	if err != nil {
		return nil, internalErr()
	}
	docByID := documentsByID(docs)
	if !allDocumentsResolved(documentIDs, docByID) {
		return nil, internalErr()
	}
	return tgAvailableEffects(effects, docByID, catalogHash), nil
}

func (r *Router) onMessagesGetStickerSet(ctx context.Context, req *tg.MessagesGetStickerSetRequest) (tg.MessagesStickerSetClass, error) {
	if req == nil || req.Stickerset == nil {
		return nil, stickersetInvalidErr()
	}
	if r.deps.Files == nil {
		return nil, internalErr()
	}
	ref, ok := stickerSetRefFromInput(req.Stickerset)
	if !ok {
		return tdesktop.StickerSet(req), nil
	}
	if ref.Kind == domain.StickerSetRefByID && ref.ID <= 0 {
		return nil, stickersetInvalidErr()
	}
	set, docs, found, err := r.deps.Files.ResolveStickerSet(ctx, ref)
	if err != nil {
		return nil, internalErr()
	}
	// 量化客户端反复请求哪些集（同集重试 vs 大量不同集）。ResolveStickerSet
	// 自身的负缓存会短路重复 miss。
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
		if ref.Kind == domain.StickerSetRefByID {
			for _, placeholder := range clientPlaceholderStickerSets {
				if ref.ID == placeholder.ID && ref.AccessHash != placeholder.AccessHash {
					return nil, stickersetInvalidErr()
				}
			}
		}
		fallbackSet, fallbackDocs, fallbackFound, fallbackErr := r.resolvePlaceholderStickerSet(ctx, ref)
		if fallbackErr != nil {
			return nil, internalErr()
		}
		if fallbackFound {
			if req.Hash != 0 && req.Hash == fallbackSet.Hash {
				return &tg.MessagesStickerSetNotModified{}, nil
			}
			return tgMessagesStickerSet(fallbackSet, fallbackDocs), nil
		}
		// 客户端可引用尚未导入的官方资源或旧缓存中的 set ID。
		// 空集携带稳定 hash，让启动查询收敛；真实存储故障仍在上面返回错误。
		return tdesktop.StickerSet(req), nil
	}
	if ref.Kind == domain.StickerSetRefByID && ref.AccessHash != set.AccessHash {
		return nil, stickersetInvalidErr()
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

// Android 的两个内建 placeholder 名称没有独立 seed。兼容投影使用稳定的独立
// identity，避免复用 AnimatedEmoji 的 set id/hash 后与真正的 598 文档缓存互相覆盖。
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
		return nil, internalErr()
	}
	sets, err := r.stickerCatalogSets(ctx, domain.StickerSetKindEmoji)
	if err != nil {
		return nil, internalErr()
	}
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
	if r.deps.Files == nil || r.deps.Account == nil {
		return nil, internalErr()
	}
	sets, err := r.installedStickerSetsForViewer(ctx, kind)
	if err != nil {
		return nil, err
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

func (r *Router) installedStickerSetsForViewer(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error) {
	if r.deps.Account == nil || r.deps.Files == nil {
		return nil, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil || userID == 0 {
		return nil, internalErr()
	}
	userSets, _, err := r.deps.Account.ListUserStickerSets(ctx, userID, kind, nil, 0, domain.MaxInstalledStickerSets)
	if err != nil {
		return nil, internalErr()
	}
	out := make([]domain.StickerSet, 0, len(userSets))
	for _, item := range userSets {
		if item.Archived {
			continue
		}
		if item.StickerSetID == 0 {
			return nil, internalErr()
		}
		set, _, found, err := r.deps.Files.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: item.StickerSetID})
		if err != nil {
			return nil, internalErr()
		}
		if !found || set.ID == 0 || set.Deleted || userStickerSetKind(set) != kind {
			return nil, internalErr()
		}
		set = stickerSetWithoutViewerInstallState(set)
		out = append(out, stickerSetWithViewerInstallItem(set, item))
	}
	return out, nil
}

func (r *Router) stickerSetsWithViewerInstallState(ctx context.Context, kind domain.StickerSetKind, sets []domain.StickerSet) ([]domain.StickerSet, error) {
	out := make([]domain.StickerSet, 0, len(sets))
	byID := make(map[int64]int, len(sets))
	for _, set := range sets {
		set = stickerSetWithoutViewerInstallState(set)
		byID[set.ID] = len(out)
		out = append(out, set)
	}
	if r.deps.Account == nil {
		return nil, internalErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, internalErr()
	}
	userSets, _, err := r.deps.Account.ListUserStickerSets(ctx, userID, kind, nil, 0, domain.MaxInstalledStickerSets)
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

// featuredCoversPerSet 限制每个 featured 集解析的封面贴纸数量（trending 预览用）。
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

// featuredStickersForKind 把已 seed 的（未归档）贴纸/emoji 集作为 trending 呈现。
// 性能：先用集目录 hash 比对，命中即返回 *NotModified——封面文档解析只在 cache-miss
// 时发生（一次批量 GetDocuments），避免每次请求都解析封面。
func (r *Router) featuredStickersForKind(ctx context.Context, hash int64, kind domain.StickerSetKind) (tg.MessagesFeaturedStickersClass, error) {
	if r.deps.Files == nil {
		return nil, internalErr()
	}
	// perf：从目录缓存读集（TTL 内 hash 命中不打 PG，封面仅 cache-miss 解析）。
	sets, err := r.stickerCatalogSets(ctx, kind)
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
		// 关键 perf 短路：目录未变直接返回，不解析任何封面文档。
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

// resolveFeaturedCovers 批量解析所有 featured 集的前若干封面文档（一次查询）。
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
	docByID := documentsByID(docs)
	if !allDocumentsResolved(ids, docByID) {
		return nil, domain.ErrStickerInvalid
	}
	return docByID, nil
}

// featuredCoveredSet 用已解析的封面构造 covered set；集合本身无封面时返回 noCovered。
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

func allDocumentsResolved(ids []int64, byID map[int64]domain.Document) bool {
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := byID[id]; !ok {
			return false
		}
	}
	return true
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
