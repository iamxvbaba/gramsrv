package store

import (
	"container/list"
	"context"
	"sort"
	"sync"
	"time"

	"telesrv/internal/domain"
)

const (
	defaultReadModelHashCacheTTL = 30 * time.Minute
	defaultReadModelHashCacheMax = 1000000
)

type readModelHashCacheEntry struct {
	key      ReadModelKey
	hash     int64
	expireAt time.Time
}

type readModelHashInflight struct {
	done     chan struct{}
	err      error
	hash     int64
	accepted bool
}

// CachedReadModelVersionStore caches read_model_versions hash tokens in-process.
// Correctness is driven by the same read-model NOTIFY stream that invalidates the
// heavier projection caches; TTL only bounds missed out-of-band writes.
type CachedReadModelVersionStore struct {
	base ReadModelVersionStore
	ttl  time.Duration
	max  int
	now  func() time.Time

	mu       sync.Mutex
	lru      *list.List
	m        map[ReadModelKey]*list.Element
	inflight map[ReadModelKey]*readModelHashInflight
	// flushGeneration rejects every refill that started before a listener
	// reconnect/full flush. keyGeneration rejects only the exact key whose
	// NOTIFY arrived while it was loading; unrelated high-churn keys must not
	// keep the complete version spine permanently cold.
	flushGeneration uint64
	keyGeneration   map[ReadModelKey]uint64
}

func NewCachedReadModelVersionStore(base ReadModelVersionStore, ttl time.Duration, max int) *CachedReadModelVersionStore {
	if base == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = defaultReadModelHashCacheTTL
	}
	if max <= 0 {
		max = defaultReadModelHashCacheMax
	}
	return &CachedReadModelVersionStore{
		base:          base,
		ttl:           ttl,
		max:           max,
		now:           time.Now,
		lru:           list.New(),
		m:             make(map[ReadModelKey]*list.Element, 1024),
		inflight:      make(map[ReadModelKey]*readModelHashInflight),
		keyGeneration: make(map[ReadModelKey]uint64, 1024),
	}
}

func (s *CachedReadModelVersionStore) ReadModelHash(ctx context.Context, model string, ownerUserID int64, peerType domain.PeerType, peerID int64) (int64, bool, error) {
	if s == nil || s.base == nil || model == "" {
		return 0, false, nil
	}
	key := ReadModelKey{Model: model, OwnerUserID: ownerUserID, PeerType: peerType, PeerID: peerID}
	rows, err := s.ReadModelHashes(ctx, []ReadModelKey{key})
	if err != nil {
		return 0, false, err
	}
	hash := rows[key]
	return hash, hash != 0, nil
}

func (s *CachedReadModelVersionStore) ReadModelHashes(ctx context.Context, keys []ReadModelKey) (map[ReadModelKey]int64, error) {
	out := make(map[ReadModelKey]int64, len(keys))
	if s == nil || s.base == nil || len(keys) == 0 {
		return out, nil
	}
	now := s.now()
	misses := make([]ReadModelKey, 0, len(keys))
	done := make(map[ReadModelKey]struct{}, len(keys))
	seen := make(map[ReadModelKey]struct{}, len(keys))

	s.mu.Lock()
	for _, key := range keys {
		if key.Model == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if entry, ok := s.readHashLocked(key, now); ok {
			out[key] = entry.hash
			done[key] = struct{}{}
			continue
		}
		misses = append(misses, key)
	}
	s.mu.Unlock()

	if len(misses) == 0 {
		return out, nil
	}
	sortReadModelKeys(misses)

	for len(done) < len(seen) {
		owned := make([]ReadModelKey, 0, len(misses))
		waiting := make(map[ReadModelKey]*readModelHashInflight)
		var loadFlushGeneration uint64
		ownedGeneration := make(map[ReadModelKey]uint64, len(misses))
		ownedInflight := make(map[ReadModelKey]*readModelHashInflight, len(misses))
		now = s.now()
		s.mu.Lock()
		loadFlushGeneration = s.flushGeneration
		for _, key := range misses {
			if _, ok := done[key]; ok {
				continue
			}
			if entry, ok := s.readHashLocked(key, now); ok {
				out[key] = entry.hash
				done[key] = struct{}{}
				continue
			}
			if inflight := s.inflight[key]; inflight != nil {
				waiting[key] = inflight
				continue
			}
			inflight := &readModelHashInflight{done: make(chan struct{})}
			s.inflight[key] = inflight
			owned = append(owned, key)
			ownedGeneration[key] = s.keyGeneration[key]
			ownedInflight[key] = inflight
		}
		s.mu.Unlock()
		if len(owned) == 0 && len(waiting) == 0 {
			break
		}
		if len(owned) > 0 {
			loaded, err := s.base.ReadModelHashes(ctx, owned)
			if err != nil {
				s.finishReadModelHashInflight(owned, ownedGeneration, nil, err, time.Time{}, loadFlushGeneration)
				return nil, err
			}
			expireAt := s.now().Add(s.ttl)
			s.finishReadModelHashInflight(owned, ownedGeneration, loaded, nil, expireAt, loadFlushGeneration)
			// Invalidation may arrive during load and write a newer hash; prefer returning the
			// cache's current value (possibly the new hash NOTIFY just wrote). After a precise
			// invalidation/flush, if there is no current value, do not return the stale load;
			// instead re-claim/load on the next round.
			effNow := s.now()
			s.mu.Lock()
			for _, key := range owned {
				if entry, ok := s.readHashLocked(key, effNow); ok {
					out[key] = entry.hash
					done[key] = struct{}{}
					continue
				}
				if inflight := ownedInflight[key]; inflight != nil && inflight.accepted {
					// A capacity eviction may remove an otherwise generation-valid
					// entry before this owner reacquires the lock. Its accepted value
					// remains a valid result for this call even if it is not retained.
					out[key] = inflight.hash
					done[key] = struct{}{}
				}
			}
			s.mu.Unlock()
		}
		for key, inflight := range waiting {
			select {
			case <-inflight.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if inflight.err != nil {
				return nil, inflight.err
			}
			s.mu.Lock()
			entry, ok := s.readHashLocked(key, s.now())
			s.mu.Unlock()
			if ok {
				out[key] = entry.hash
				done[key] = struct{}{}
				continue
			}
			if inflight.accepted {
				out[key] = inflight.hash
				done[key] = struct{}{}
			}
		}
	}
	return out, nil
}

func (s *CachedReadModelVersionStore) finishReadModelHashInflight(
	keys []ReadModelKey,
	keyGeneration map[ReadModelKey]uint64,
	loaded map[ReadModelKey]int64,
	err error,
	expireAt time.Time,
	loadFlushGeneration uint64,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		if expireAt.IsZero() {
			expireAt = s.now().Add(s.ttl)
		}
		for _, key := range keys {
			inflight := s.inflight[key]
			if inflight == nil {
				continue
			}
			if s.flushGeneration != loadFlushGeneration || s.keyGeneration[key] != keyGeneration[key] {
				continue
			}
			inflight.hash = loaded[key]
			inflight.accepted = true
			s.storeHashLocked(key, inflight.hash, expireAt)
		}
	}
	for _, key := range keys {
		if inflight := s.inflight[key]; inflight != nil {
			inflight.err = err
			delete(s.inflight, key)
			delete(s.keyGeneration, key)
			close(inflight.done)
		}
	}
}

func (s *CachedReadModelVersionStore) InvalidateReadModel(key ReadModelKey) {
	if s == nil || key.Model == "" {
		return
	}
	s.mu.Lock()
	s.removeHashLocked(key)
	s.bumpReadModelKeyGenerationLocked(key)
	s.mu.Unlock()
}

func (s *CachedReadModelVersionStore) UpdateReadModelHash(key ReadModelKey, hash int64) {
	if s == nil || key.Model == "" {
		return
	}
	if hash == 0 {
		s.InvalidateReadModel(key)
		return
	}
	s.mu.Lock()
	s.storeHashLocked(key, hash, s.now().Add(s.ttl))
	// After writing an authoritative new hash, advance only that exact key's generation;
	// other keys' inflight refills can still complete normally.
	s.bumpReadModelKeyGenerationLocked(key)
	s.mu.Unlock()
}

func (s *CachedReadModelVersionStore) bumpReadModelKeyGenerationLocked(key ReadModelKey) {
	if s.inflight[key] == nil {
		// Generations only guard a currently unlocked refill. Keeping tombstones
		// for every historical notification would make this side map unbounded.
		delete(s.keyGeneration, key)
		return
	}
	s.keyGeneration[key]++
}

func (s *CachedReadModelVersionStore) FlushReadModelCache() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.lru.Init()
	s.m = make(map[ReadModelKey]*list.Element, 1024)
	s.keyGeneration = make(map[ReadModelKey]uint64, 1024)
	s.flushGeneration++
	s.mu.Unlock()
}

func (s *CachedReadModelVersionStore) readHashLocked(key ReadModelKey, now time.Time) (readModelHashCacheEntry, bool) {
	el := s.m[key]
	if el == nil {
		return readModelHashCacheEntry{}, false
	}
	entry := el.Value.(*readModelHashCacheEntry)
	if !entry.expireAt.After(now) {
		s.lru.Remove(el)
		delete(s.m, key)
		return readModelHashCacheEntry{}, false
	}
	s.lru.MoveToFront(el)
	return *entry, true
}

func (s *CachedReadModelVersionStore) storeHashLocked(key ReadModelKey, hash int64, expireAt time.Time) {
	if el := s.m[key]; el != nil {
		entry := el.Value.(*readModelHashCacheEntry)
		entry.hash = hash
		entry.expireAt = expireAt
		s.lru.MoveToFront(el)
		return
	}
	entry := &readModelHashCacheEntry{key: key, hash: hash, expireAt: expireAt}
	s.m[key] = s.lru.PushFront(entry)
	for len(s.m) > s.max {
		oldest := s.lru.Back()
		if oldest == nil {
			break
		}
		old := oldest.Value.(*readModelHashCacheEntry)
		delete(s.m, old.key)
		s.lru.Remove(oldest)
	}
}

func (s *CachedReadModelVersionStore) removeHashLocked(key ReadModelKey) {
	if el := s.m[key]; el != nil {
		delete(s.m, key)
		s.lru.Remove(el)
	}
}

func sortReadModelKeys(keys []ReadModelKey) {
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.OwnerUserID != b.OwnerUserID {
			return a.OwnerUserID < b.OwnerUserID
		}
		if a.PeerType != b.PeerType {
			return a.PeerType < b.PeerType
		}
		return a.PeerID < b.PeerID
	})
}
