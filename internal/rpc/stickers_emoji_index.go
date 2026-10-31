package rpc

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// messages.getStickers(emoticon): returns matching stickers by emoji. The emoji->sticker
// mapping comes from each sticker set's Packs (Emoticon->DocumentIDs). To avoid a
// ListStickerSets + pack walk on every request, a process-level TTL cache index is
// used; on a hash match it returns *NotModified without resolving any documents.

const (
	// emojiStickerIndexTTL: sticker sets are essentially static after seeding, so the
	// index is reused within the TTL to avoid repeated PG queries. After installing or
	// archiving a sticker set the index lags by at most this duration (acceptable for
	// sticker search).
	emojiStickerIndexTTL = 5 * time.Minute
	// maxStickersPerEmoji bounds the number of stickers returned for a single emoji.
	maxStickersPerEmoji = 100
	// maxGreetingStickers bounds the startup prefetch set for the official client's
	// greeting category. An ordinary 👋 search still returns the full result; the
	// special 👋⭐ category takes only one representative per distinct sticker set, so
	// the client does not, on every startup, randomly hit among dozens of ordinary wave
	// packs and gradually download the entire catalog.
	maxGreetingStickers = 12
	// greetingStickerCategoryKey is the official greeting marker with variation
	// selectors removed.
	greetingStickerCategoryKey = "👋⭐"
)

// emojiStickerIndex is a TTL-cached index from emoji to sticker document ids.
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

// lookup returns the sticker document ids for an emoji; when the index is unbuilt or
// expired it is rebuilt with build. perf: the rebuild runs outside the lock (build
// reads the catalog cache, so I/O is not in the critical section), avoiding every
// concurrent request piling up on the mutex at the TTL expiry point. Concurrent stale
// requests may each build once (the catalog cache itself singleflights PG, so the pack
// walk is duplicated but cheap). When build returns nil (e.g. a catalog read failure)
// the old index is kept.
func (idx *emojiStickerIndex) lookup(emoticon string, build func() map[string][]int64) []int64 {
	idx.mu.RLock()
	fresh := idx.ready && idx.now().Sub(idx.builtAt) < idx.ttl
	if fresh {
		out := append([]int64(nil), idx.byEmoji[emoticon]...)
		idx.mu.RUnlock()
		return out
	}
	idx.mu.RUnlock()

	next := build() // rebuild outside the lock

	idx.mu.Lock()
	defer idx.mu.Unlock()
	// Re-check: a concurrent caller may have rebuilt outside the lock and swapped it in
	// first.
	stillStale := !idx.ready || idx.now().Sub(idx.builtAt) >= idx.ttl
	if stillStale && next != nil {
		idx.byEmoji = next
		idx.builtAt = idx.now()
		idx.ready = true
	}
	return append([]int64(nil), idx.byEmoji[emoticon]...)
}

// normalizeStickerEmoticon strips variation selectors (U+FE0F/U+FE0E) and trims
// whitespace, so a client-sent "👍️" (with VS16) matches the "👍" in a pack.
func normalizeStickerEmoticon(e string) string {
	e = strings.ReplaceAll(e, "️", "")
	e = strings.ReplaceAll(e, "︎", "")
	return strings.TrimSpace(e)
}

// normalizeStickerSearchEmoticon parses the special sticker-category markers that
// official clients pass through messages.getStickers. TDesktop, DrKLO Android and
// Telegram-iOS all use wave+star for greeting, double-star for the premium preview,
// and folder+star for the premium/cloud catalog; these are not ordinary composite
// emoji. greeting maintains a separate bounded representative set in the index, while
// the other two categories still map to the base keys of the seed packs.
//
// Only these three complete markers are matched; an arbitrary composite emoji must not
// be split into single emoji, or the exact-match semantics of ordinary sticker search
// would change. Stripping variation selectors first also accommodates the Unicode
// representation differences across the three clients.
func normalizeStickerSearchEmoticon(e string) string {
	e = normalizeStickerEmoticon(e)
	switch e {
	case greetingStickerCategoryKey:
		return greetingStickerCategoryKey
	case "⭐⭐":
		return "⭐"
	case "📂⭐":
		return "📂"
	default:
		return e
	}
}

func (r *Router) onMessagesGetStickers(ctx context.Context, req *tg.MessagesGetStickersRequest) (tg.MessagesStickersClass, error) {
	if req == nil || r.deps.Files == nil || r.emojiStickers == nil {
		return &tg.MessagesStickers{Hash: 0, Stickers: []tg.DocumentClass{}}, nil
	}
	searchKey := normalizeStickerSearchEmoticon(req.Emoticon)
	docIDs := r.emojiStickers.lookup(searchKey, func() map[string][]int64 {
		return r.buildEmojiStickerIndex(ctx)
	})
	limit := maxStickersPerEmoji
	if searchKey == greetingStickerCategoryKey {
		limit = maxGreetingStickers
	}
	if len(docIDs) > limit {
		docIDs = docIDs[:limit]
	}
	catalogHash := int64(tdesktopCountHash(docIDs))
	if req.Hash != 0 && req.Hash == catalogHash {
		// perf short-circuit: consistent with the client cache, resolve no documents.
		// Hard contract: NotModified may only be returned when hash!=0 - DrKLO's premium
		// preview sticker prefetch sends hash=0 and unconditionally casts to
		// TL_messages_stickers, so a notModified would ClassCastException and crash;
		// hash=0 always takes the full response below. Do not "optimize" this into
		// returning NotModified for hash=0.
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
	for _, id := range docIDs { // preserve index order
		if d, ok := byID[id]; ok {
			ordered = append(ordered, d)
		}
	}
	return &tg.MessagesStickers{Hash: catalogHash, Stickers: tgDocuments(ordered)}, nil
}

// buildEmojiStickerIndex builds emoji->deduplicated ordered docIDs from the Packs of
// all (non-archived) regular sticker sets. Returning nil means the build failed (keep
// the old index).
func (r *Router) buildEmojiStickerIndex(ctx context.Context) map[string][]int64 {
	// perf: read the sets from the catalog cache (shared with getAllStickers/featured,
	// no PG hit within the TTL).
	sets := r.stickerCatalogSets(ctx, domain.StickerSetKindStickers)
	if sets == nil {
		return nil // catalog read failed: keep the old index (nil = do not replace)
	}
	byEmoji := make(map[string][]int64)
	seen := make(map[string]map[int64]struct{})
	for _, s := range sets {
		if s.Archived {
			continue
		}
		greetingAdded := false
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
			// greeting is the client's startup prefetch catalog and is not the same as
			// an ordinary 👋 search. Each sticker set contributes only one representative,
			// with the total further bounded at the response boundary; this keeps
			// diversity without exposing every ordinary wave sticker as a startup
			// resource candidate.
			if key == "👋" && !greetingAdded {
				greetingSeen := seen[greetingStickerCategoryKey]
				if greetingSeen == nil {
					greetingSeen = make(map[int64]struct{})
					seen[greetingStickerCategoryKey] = greetingSeen
				}
				for _, id := range pack.DocumentIDs {
					if id == 0 {
						continue
					}
					if _, ok := greetingSeen[id]; ok {
						continue
					}
					greetingSeen[id] = struct{}{}
					byEmoji[greetingStickerCategoryKey] = append(byEmoji[greetingStickerCategoryKey], id)
					greetingAdded = true
					break
				}
			}
		}
	}
	return byEmoji
}
