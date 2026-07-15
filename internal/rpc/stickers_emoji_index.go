package rpc

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// messages.getStickers(emoticon)：按 emoji 返回匹配的贴纸。emoji→贴纸映射来自各
// 贴纸集的 Packs（Emoticon→DocumentIDs）。为避免每次请求都 ListStickerSets + 遍历
// packs，用一个进程级 TTL 缓存索引；命中 hash 时返回 *NotModified 不解析任何文档。

const (
	// emojiStickerIndexTTL：贴纸集 seed 后基本静态，TTL 内复用索引免重复 PG 查询。
	// 安装/归档贴纸集后索引最多滞后该时长（贴纸搜索可接受）。
	emojiStickerIndexTTL = 5 * time.Minute
	// maxStickersPerEmoji 限制单个 emoji 返回的贴纸数。
	maxStickersPerEmoji = 100
)

// emojiStickerIndex 是 emoji→贴纸文档 id 的 TTL 缓存索引。
type emojiStickerIndex struct {
	mu      sync.RWMutex
	now     func() time.Time
	ttl     time.Duration
	builtAt time.Time
	ready   bool
	byEmoji map[string][]int64
}

func newEmojiStickerIndex(now func() time.Time) *emojiStickerIndex {
	return &emojiStickerIndex{now: now, ttl: emojiStickerIndexTTL, byEmoji: map[string][]int64{}}
}

// lookup 返回某 emoji 的贴纸文档 id；索引未建或过期时用 build 重建。
// perf：重建在锁外执行（build 读目录缓存，I/O 不在临界区），避免 TTL 过期点把所有
// 并发请求堵在互斥锁上。并发 stale 请求可能各自 build 一次（目录缓存自身 singleflight
// 去重 PG，pack 遍历重复但廉价）。build 返回 nil（如目录读失败）时保留旧索引。
func (idx *emojiStickerIndex) lookup(emoticon string, build func() map[string][]int64) []int64 {
	idx.mu.RLock()
	fresh := idx.ready && idx.now().Sub(idx.builtAt) < idx.ttl
	if fresh {
		out := append([]int64(nil), idx.byEmoji[emoticon]...)
		idx.mu.RUnlock()
		return out
	}
	idx.mu.RUnlock()

	next := build() // 锁外重建

	idx.mu.Lock()
	defer idx.mu.Unlock()
	// 复查：可能已有并发者在锁外重建并先一步换入。
	stillStale := !idx.ready || idx.now().Sub(idx.builtAt) >= idx.ttl
	if stillStale && next != nil {
		idx.byEmoji = next
		idx.builtAt = idx.now()
		idx.ready = true
	}
	return append([]int64(nil), idx.byEmoji[emoticon]...)
}

// normalizeStickerEmoticon 去掉变体选择符（U+FE0F/U+FE0E）并裁剪空白，使客户端发的
// "👍️"（带 VS16）能匹配 pack 里的 "👍"。
func normalizeStickerEmoticon(e string) string {
	e = strings.ReplaceAll(e, "️", "")
	e = strings.ReplaceAll(e, "︎", "")
	return strings.TrimSpace(e)
}

func (r *Router) onMessagesGetStickers(ctx context.Context, req *tg.MessagesGetStickersRequest) (tg.MessagesStickersClass, error) {
	if req == nil || r.deps.Files == nil || r.emojiStickers == nil {
		return &tg.MessagesStickers{Hash: 0, Stickers: []tg.DocumentClass{}}, nil
	}
	docIDs := r.emojiStickers.lookup(normalizeStickerEmoticon(req.Emoticon), func() map[string][]int64 {
		return r.buildEmojiStickerIndex(ctx)
	})
	if len(docIDs) > maxStickersPerEmoji {
		docIDs = docIDs[:maxStickersPerEmoji]
	}
	catalogHash := int64(tdesktopCountHash(docIDs))
	if req.Hash != 0 && req.Hash == catalogHash {
		// perf 短路：与客户端缓存一致，不解析任何文档。
		// 硬契约：仅在 hash!=0 时可返回 NotModified——DrKLO premium 预览贴纸预取发
		// hash=0 且无条件强转 TL_messages_stickers，对 notModified 会 ClassCastException
		// 闪退；hash=0 一律走下方完整响应。勿"优化"成对 hash=0 也返回 NotModified。
		return &tg.MessagesStickersNotModified{}, nil
	}
	if len(docIDs) == 0 {
		return &tg.MessagesStickers{Hash: catalogHash, Stickers: []tg.DocumentClass{}}, nil
	}
	docs, err := r.deps.Files.GetDocuments(ctx, docIDs)
	if err != nil {
		return nil, internalErr()
	}
	byID := documentsByID(docs)
	ordered := make([]domain.Document, 0, len(docIDs))
	for _, id := range docIDs { // 保持索引顺序
		if d, ok := byID[id]; ok {
			ordered = append(ordered, d)
		}
	}
	return &tg.MessagesStickers{Hash: catalogHash, Stickers: tgDocuments(ordered)}, nil
}

// buildEmojiStickerIndex 从所有（未归档）常规贴纸集的 Packs 构建 emoji→去重有序 docIDs。
// 返回 nil 表示构建失败（保留旧索引）。
func (r *Router) buildEmojiStickerIndex(ctx context.Context) map[string][]int64 {
	// perf：从目录缓存读集（与 getAllStickers/featured 共用，TTL 内不打 PG）。
	sets := r.stickerCatalogSets(ctx, domain.StickerSetKindStickers)
	if sets == nil {
		return nil // 目录读失败：保留旧索引（nil=不替换）
	}
	byEmoji := make(map[string][]int64)
	seen := make(map[string]map[int64]struct{})
	for _, s := range sets {
		if s.Archived {
			continue
		}
		for _, pack := range s.Packs {
			key := normalizeStickerEmoticon(pack.Emoticon)
			if key == "" {
				continue
			}
			dedup := seen[key]
			if dedup == nil {
				dedup = make(map[int64]struct{})
				seen[key] = dedup
			}
			for _, id := range pack.DocumentIDs {
				if id == 0 {
					continue
				}
				if _, ok := dedup[id]; ok {
					continue
				}
				dedup[id] = struct{}{}
				byEmoji[key] = append(byEmoji[key], id)
			}
		}
	}
	return byEmoji
}
