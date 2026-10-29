package files

import (
	"context"
	"fmt"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"telesrv/internal/domain"
)

// warmBlobReadConcurrency 限制启动预热时并发读 blob 的数量。预热的都是 ≤256KB 的小
// 文件，8 路足够盖住本地磁盘/对象存储的延迟，又不会在启动时抢满 I/O。
const warmBlobReadConcurrency = 8

// WarmStats 汇报一次启动资源缓存预热结果。
type WarmStats struct {
	StickerSets int
	Documents   int
	Blobs       int
}

// WarmCaches 从已持久化的 sticker/reaction 元数据预热小 blob 字节缓存与完整 sticker set 缓存。
// SeedMedia 在已有数据时会跳过导入；该方法保证普通 server 重启后历史 sticker 首次渲染也不是冷缓存。
func (s *Service) WarmCaches(ctx context.Context) (WarmStats, error) {
	var stats WarmStats
	// 必须在查询集合前取 epoch：查询期间的 RPC 修改/删除会使整份预热快照失效。
	epoch := s.stickerSetCache.loadEpoch()
	// 第一阶段：一次取齐所有贴纸集 + reaction，再一条查询取回全部文档。过去逐集
	// GetDocuments 在远端 PG 上要 60+ 次往返。
	var sets []domain.StickerSet
	docIDs := make([]int64, 0, 4096)
	for _, kind := range []domain.StickerSetKind{
		domain.StickerSetKindStickers,
		domain.StickerSetKindEmoji,
		domain.StickerSetKindMasks,
		domain.StickerSetKindSystem,
	} {
		list, err := s.media.ListStickerSets(ctx, kind)
		if err != nil {
			return stats, err
		}
		for _, set := range list {
			sets = append(sets, set)
			docIDs = append(docIDs, set.DocumentIDs...)
		}
	}
	reactions, err := s.media.ListAvailableReactions(ctx)
	if err != nil {
		return stats, err
	}
	for _, reaction := range reactions {
		docIDs = append(docIDs, reaction.DocumentIDs()...)
	}
	allDocs, err := s.media.GetDocuments(ctx, docIDs)
	if err != nil {
		return stats, err
	}
	byID := make(map[int64]domain.Document, len(allDocs))
	for _, doc := range allDocs {
		byID[doc.ID] = doc
	}
	for _, set := range sets {
		ordered := make([]domain.Document, 0, len(set.DocumentIDs))
		for _, id := range set.DocumentIDs {
			if doc, ok := byID[id]; ok {
				ordered = append(ordered, doc)
			}
		}
		if s.stickerSetCache.putIfEpoch(set, ordered, epoch) {
			stats.StickerSets++
		}
	}
	// GetDocuments 已按 id 去重，且 docIDs 正是贴纸集 ∪ reaction 的全部文档。
	docs := allDocs
	stats.Documents = len(docs)

	// 第二阶段：一发 ANY 查询批量取所有 location key 的 blob 元数据，替代过去逐个
	// GetFileBlob 的启动期 N+1（~2400 个 blob 各打一次 PG → 一次往返）。
	keys := make([]string, 0, len(docs)*2)
	for _, doc := range docs {
		keys = append(keys, blobLocationKeys(doc)...)
	}
	blobs, err := s.media.GetFileBlobs(ctx, keys)
	if err != nil {
		return stats, err
	}
	// 第三阶段：填充元数据缓存，并把小 blob 的全量字节读入字节缓存（blob backend 读，非 PG）。
	// 读取按 blob backend 并行：串行时一次重启要按顺序读几千个小文件，2.3 s 里绝大多数
	// 时间花在等待单次读的延迟上。哈希/计数都用原子量，缓存本身是加锁的。
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(warmBlobReadConcurrency)
	var warmed atomic.Int64
	for _, key := range keys {
		blob, ok := blobs[key]
		if !ok {
			continue
		}
		s.blobCache.put(key, blob)
		group.Go(func() error {
			ok, err := s.warmBlobBytes(groupCtx, blob)
			if err != nil {
				return err
			}
			if ok {
				warmed.Add(1)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return stats, err
	}
	stats.Blobs = int(warmed.Load())
	return stats, nil
}

// blobLocationKeys 返回一个文档需预热的全部 location key（主体 + 可下载缩略图）。
func blobLocationKeys(doc domain.Document) []string {
	if doc.ID == 0 {
		return nil
	}
	keys := make([]string, 0, 1+len(doc.Thumbs))
	keys = append(keys, fmt.Sprintf("doc:%d", doc.ID))
	for _, thumb := range doc.Thumbs {
		if !thumb.Downloadable() {
			continue
		}
		keys = append(keys, fmt.Sprintf("doc:%d:%s", doc.ID, thumb.Type))
	}
	return keys
}

// warmBlobBytes 把小 blob 的全量字节读入 byteCache（大 blob 跳过，仍由 GetRange 分段读）。
// 返回是否实际写入了字节缓存。
func (s *Service) warmBlobBytes(ctx context.Context, blob domain.FileBlob) (bool, error) {
	if blob.Size <= 0 || blob.Size > blobBytesCacheMaxEntryBytes || s.byteCache.has(blob.ObjectKey) {
		return false, nil
	}
	data, total, err := s.blobs.GetRange(ctx, blob.ObjectKey, 0, blobBytesCacheMaxEntryBytes+1)
	if err != nil {
		return false, fmt.Errorf("read blob %q: %w", blob.LocationKey, err)
	}
	if total <= blobBytesCacheMaxEntryBytes && int64(len(data)) == total {
		s.byteCache.put(blob.ObjectKey, data)
		return true, nil
	}
	return false, nil
}
