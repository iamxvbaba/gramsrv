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
// available_reactions）；Files 服务缺失或资源未导入时回退到 tdesktop 兼容 stub。

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

// onMessagesGetAvailableEffects 返回消息发送特效目录(全局静态,seed 进内存)。镜像
// getAvailableReactions:文档批量预加载后由 tgAvailableEffects 组装,hash 命中回 NotModified。
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
	// catalogHash 在 seed 时算好(全局静态),命中即零查库返回 NotModified——必须在
	// GetDocuments(唯一打 PG 的点)之前。
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
	if r.deps.Files == nil {
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
	// 观测：量化客户端反复请求哪些集（同集重试 vs 大量不同集）。未 seed 集走 stub，
	// 由 ResolveStickerSet 的负缓存短路 PG；这里只记 ref 与命中情况。
	if r.log != nil {
		r.log.Debug("getStickerSet",
			zap.String("ref_kind", string(ref.Kind)),
			zap.String("short_name", ref.ShortName),
			zap.Int64("set_id", ref.ID),
			zap.String("system_key", ref.SystemKey),
			zap.Bool("found", found),
		)
	}
	if !found {
		if fallbackSet, fallbackDocs, fallbackFound, fallbackErr := r.resolvePlaceholderStickerSet(ctx, ref); fallbackErr != nil {
			return nil, internalErr()
		} else if fallbackFound {
			fallbackSet, fallbackErr = r.stickerSetWithViewerInstallState(ctx, fallbackSet)
			if fallbackErr != nil {
				return nil, fallbackErr
			}
			if r.log != nil {
				r.log.Debug("getStickerSet placeholder fallback",
					zap.String("short_name", ref.ShortName),
					zap.Int64("fallback_set_id", fallbackSet.ID),
					zap.String("fallback_short_name", fallbackSet.ShortName),
					zap.Int("documents", len(fallbackDocs)),
				)
			}
			return tgMessagesStickerSet(fallbackSet, fallbackDocs), nil
		}
		// 未 seed 的系统集 / 未知短名：回退兼容 stub，避免破坏客户端。
		return tdesktop.StickerSet(req), nil
	}
	set, err = r.stickerSetWithViewerInstallState(ctx, set)
	if err != nil {
		return nil, err
	}
	return tgMessagesStickerSet(set, docs), nil
}

func (r *Router) resolvePlaceholderStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	if ref.Kind != domain.StickerSetRefByShortName || !isClientPlaceholderStickerSet(ref.ShortName) {
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
		if len(docs) >= androidPlaceholderStickerMinDocuments {
			return set, docs, true, nil
		}
	}
	for _, kind := range []domain.StickerSetKind{domain.StickerSetKindSystem, domain.StickerSetKindEmoji, domain.StickerSetKindStickers} {
		sets, err := r.deps.Files.ListStickerSets(ctx, kind)
		if err != nil {
			return domain.StickerSet{}, nil, false, err
		}
		for _, candidate := range sets {
			if len(candidate.DocumentIDs) < androidPlaceholderStickerMinDocuments {
				continue
			}
			set, docs, found, err := r.deps.Files.ResolveStickerSet(ctx, domain.StickerSetRef{
				Kind:       domain.StickerSetRefByID,
				ID:         candidate.ID,
				AccessHash: candidate.AccessHash,
			})
			if err != nil {
				return domain.StickerSet{}, nil, false, err
			}
			if found && len(docs) >= androidPlaceholderStickerMinDocuments {
				return set, docs, true, nil
			}
		}
	}
	return domain.StickerSet{}, nil, false, nil
}

const androidPlaceholderStickerMinDocuments = 7

func isClientPlaceholderStickerSet(shortName string) bool {
	switch shortName {
	case "tg_placeholders_android", "tg_superplaceholders_android_2":
		return true
	default:
		return false
	}
}

func placeholderStickerSetCandidates() []domain.StickerSetRef {
	return []domain.StickerSetRef{
		{Kind: domain.StickerSetRefBySystem, SystemKey: "animated_emoji"},
		{Kind: domain.StickerSetRefBySystem, SystemKey: "emoji_generic_animations"},
		{Kind: domain.StickerSetRefBySystem, SystemKey: "animated_emoji_animations"},
		{Kind: domain.StickerSetRefByShortName, ShortName: "AnimatedEmojies"},
		{Kind: domain.StickerSetRefByShortName, ShortName: "EmojiGenericAnimations"},
		{Kind: domain.StickerSetRefByShortName, ShortName: "EmojiAnimations"},
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
		// 兼容无 per-user 安装态的测试/旧内存路径：从目录缓存读全局 installed 标志。
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
	return r.onMessagesGetFeaturedStickers(ctx, req.Hash)
}

// featuredStickersForKind 把已 seed 的（未归档）贴纸/emoji 集作为 trending 呈现。
// 性能：先用集目录 hash 比对，命中即返回 *NotModified——封面文档解析只在 cache-miss
// 时发生（一次批量 GetDocuments），避免每次请求都解析封面。
func (r *Router) featuredStickersForKind(ctx context.Context, hash int64, kind domain.StickerSetKind) (tg.MessagesFeaturedStickersClass, error) {
	if r.deps.Files == nil {
		return messagesFeaturedStickersEmpty(hash), nil
	}
	// perf：从目录缓存读集（TTL 内 hash 命中不打 PG，封面仅 cache-miss 解析）。
	sets := r.stickerCatalogSets(ctx, kind)
	visible := make([]domain.StickerSet, 0, len(sets))
	for _, s := range sets {
		if s.ID == 0 || s.Archived {
			continue
		}
		visible = append(visible, s)
	}
	if len(visible) == 0 {
		return messagesFeaturedStickersEmpty(hash), nil
	}
	var err error
	visible, err = r.stickerSetsWithViewerInstallState(ctx, kind, visible)
	if err != nil {
		return nil, err
	}
	catalogHash := featuredStickerSetsHash(visible)
	if hash != 0 && hash == catalogHash {
		// 关键 perf 短路：目录未变直接返回，不解析任何封面文档。
		return &tg.MessagesFeaturedStickersNotModified{Count: len(visible)}, nil
	}
	covers := r.resolveFeaturedCovers(ctx, visible)
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
func (r *Router) resolveFeaturedCovers(ctx context.Context, sets []domain.StickerSet) map[int64]domain.Document {
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
		return nil
	}
	docs, err := r.deps.Files.GetDocuments(ctx, ids)
	if err != nil {
		return nil
	}
	return documentsByID(docs)
}

// featuredCoveredSet 用已解析的封面构造 covered set；无封面时回退 noCovered。
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
