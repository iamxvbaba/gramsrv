package files

import (
	"context"
	"fmt"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"telesrv/internal/domain"
)

// warmBlobReadConcurrency limits how many blobs are read concurrently during startup warm-up.
// The warmed blobs are all small files ≤256KB, so 8 in flight is enough to cover local disk/object
// storage latency without saturating I/O at startup.
const warmBlobReadConcurrency = 8

// WarmStats reports the result of one startup resource-cache warm-up.
type WarmStats struct {
	StickerSets int
	Documents   int
	Blobs       int
}

// WarmCaches warms the small-blob byte cache and the full sticker set cache from persisted
// sticker/reaction metadata. SeedMedia skips importing when data already exists; this method
// ensures that after an ordinary server restart the first render of a historical sticker is not a
// cold cache.
func (s *Service) WarmCaches(ctx context.Context) (WarmStats, error) {
	var stats WarmStats
	// The epoch must be taken before querying the sets: an RPC modification/deletion during the query invalidates the whole warm-up snapshot.
	epoch := s.stickerSetCache.loadEpoch()
	// Phase one: fetch all sticker sets + reactions at once, then fetch all documents in one query.
	// Previously a per-set GetDocuments cost 60+ round-trips against a remote PG.
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
	// GetDocuments has already deduplicated by id, and docIDs is exactly the union of the sticker sets' and reactions' documents.
	docs := allDocs
	stats.Documents = len(docs)

	// Phase two: one ANY query batch-fetches blob metadata for all location keys, replacing the old
	// startup-time N+1 of per-blob GetFileBlob (~2400 blobs each hitting PG → one round-trip).
	keys := make([]string, 0, len(docs)*2)
	for _, doc := range docs {
		keys = append(keys, blobLocationKeys(doc)...)
	}
	blobs, err := s.media.GetFileBlobs(ctx, keys)
	if err != nil {
		return stats, err
	}
	// Phase three: fill the metadata cache and read the small blobs' full bytes into the byte cache
	// (a blob backend read, not PG). Reads are parallelized against the blob backend: sequentially a
	// restart would read thousands of small files in order, spending the vast majority of 2.3s
	// waiting on individual read latency. Hashes/counters use atomics; the caches themselves are locked.
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

// blobLocationKeys returns all location keys a document needs warmed (main body + downloadable thumbnails).
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

// warmBlobBytes reads a small blob's full bytes into byteCache (large blobs are skipped and still
// read in ranges by GetRange). Returns whether bytes were actually written to the byte cache.
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
