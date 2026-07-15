package mtprotoedge

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRPCResultCacheFullSessionDoesNotBlockAnotherAuth(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCacheWithFairCapacity(func() time.Time { return now }, rpcResultCacheCapacity{
		maxPending: 8, maxPendingPerAuth: 6,
		globalMaxEntries: 8, globalMaxBytes: 64,
		authMaxEntries: 6, authMaxBytes: 48,
		sessionMaxEntries: 2, sessionMaxBytes: 16,
	})
	authA := [8]byte{0xa1}
	authB := [8]byte{0xb1}
	const sessionA = int64(77)

	for i := 0; i < 2; i++ {
		msgID := int64(1000 + i)
		claim, err := cache.Acquire(authA, sessionA, msgID)
		if err != nil || claim.state != rpcResultAcquireOwner {
			t.Fatalf("same-session admission %d = %#v, %v", i, claim, err)
		}
		cache.Put(authA, sessionA, msgID, &encodedOutboundMessage{body: []byte{1}})
	}
	if _, err := cache.Acquire(authA, sessionA, 2000); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("admission beyond session entry limit = %v, want capacity", err)
	}
	otherAuth, err := cache.Acquire(authB, 88, 3000)
	if err != nil || otherAuth.state != rpcResultAcquireOwner {
		t.Fatalf("other auth blocked by full session: %#v, %v", otherAuth, err)
	}
	if !otherAuth.owner.Abort() {
		t.Fatal("other-auth owner did not abort")
	}
	if _, ok := cache.Get(authA, sessionA, 1000); !ok {
		t.Fatal("session capacity pressure evicted an unexpired result")
	}
}

func TestRPCResultCacheFullAuthDoesNotBlockAnotherAuth(t *testing.T) {
	cache := newRPCResultCacheWithFairCapacity(time.Now, rpcResultCacheCapacity{
		maxPending: 8, maxPendingPerAuth: 4,
		globalMaxEntries: 8, globalMaxBytes: 64,
		authMaxEntries: 2, authMaxBytes: 32,
		sessionMaxEntries: 2, sessionMaxBytes: 16,
	})
	authA := [8]byte{0xa2}
	authB := [8]byte{0xb2}
	for i := 0; i < 2; i++ {
		claim, err := cache.Acquire(authA, int64(10+i), int64(100+i))
		if err != nil || claim.state != rpcResultAcquireOwner {
			t.Fatalf("auth A admission %d = %#v, %v", i, claim, err)
		}
		cache.Put(authA, int64(10+i), int64(100+i), &encodedOutboundMessage{body: []byte{1}})
	}
	if _, err := cache.Acquire(authA, 12, 102); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("same-auth new session at auth limit = %v, want capacity", err)
	}
	other, err := cache.Acquire(authB, 20, 200)
	if err != nil || other.state != rpcResultAcquireOwner {
		t.Fatalf("other auth blocked by full auth A: %#v, %v", other, err)
	}
	other.owner.Abort()
}

func TestRPCResultCacheAuthAndSessionByteLimitsAreIndependent(t *testing.T) {
	cache := newRPCResultCacheWithFairCapacity(time.Now, rpcResultCacheCapacity{
		maxPending: 8, maxPendingPerAuth: 6,
		globalMaxEntries: 10, globalMaxBytes: 10,
		authMaxEntries: 8, authMaxBytes: 4,
		sessionMaxEntries: 6, sessionMaxBytes: 2,
	})
	authA := [8]byte{0xa4}
	authB := [8]byte{0xb4}
	first, err := cache.Acquire(authA, 1, 101)
	if err != nil || first.state != rpcResultAcquireOwner {
		t.Fatalf("first owner = %#v, %v", first, err)
	}
	cache.Put(authA, 1, 101, &encodedOutboundMessage{body: []byte{1, 2}})
	if _, err := cache.Acquire(authA, 1, 102); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("same session beyond byte limit = %v, want capacity", err)
	}
	second, err := cache.Acquire(authA, 2, 201)
	if err != nil || second.state != rpcResultAcquireOwner {
		t.Fatalf("second session owner = %#v, %v", second, err)
	}
	cache.Put(authA, 2, 201, &encodedOutboundMessage{body: []byte{3, 4}})
	if _, err := cache.Acquire(authA, 3, 301); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("same auth beyond byte limit = %v, want capacity", err)
	}
	other, err := cache.Acquire(authB, 3, 302)
	if err != nil || other.state != rpcResultAcquireOwner {
		t.Fatalf("other auth blocked by auth A byte limit: %#v, %v", other, err)
	}
	other.owner.Abort()
}

func TestRPCResultCachePerAuthPendingLimitIsAdditional(t *testing.T) {
	cache := newRPCResultCacheWithFairCapacity(time.Now, rpcResultCacheCapacity{
		maxPending: 6, maxPendingPerAuth: 2,
		globalMaxEntries: 12, globalMaxBytes: 64,
		authMaxEntries: 6, authMaxBytes: 32,
		sessionMaxEntries: 4, sessionMaxBytes: 16,
	})
	authA := [8]byte{0xa3}
	authB := [8]byte{0xb3}
	owners := make([]*rpcResultOwnerLease, 0, 3)
	for i := 0; i < 2; i++ {
		claim, err := cache.Acquire(authA, int64(i+1), int64(100+i))
		if err != nil || claim.state != rpcResultAcquireOwner {
			t.Fatalf("pending auth A %d = %#v, %v", i, claim, err)
		}
		owners = append(owners, claim.owner)
	}
	if _, err := cache.Acquire(authA, 3, 103); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("third pending owner for auth A = %v, want capacity", err)
	}
	other, err := cache.Acquire(authB, 4, 104)
	if err != nil || other.state != rpcResultAcquireOwner {
		t.Fatalf("auth B blocked by auth A pending limit: %#v, %v", other, err)
	}
	owners = append(owners, other.owner)
	for _, owner := range owners {
		if !owner.Abort() {
			t.Fatal("pending owner did not abort")
		}
	}
	if usage := cache.fairBudget.authSnapshot(authA); usage != (rpcResultBudgetUsage{}) {
		t.Fatalf("auth A budget after abort = %#v", usage)
	}
}

func TestRPCResultCacheFairReservationLifecycleReturnsEveryScope(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCacheWithFairCapacity(func() time.Time { return now }, rpcResultCacheCapacity{
		maxPending: 4, maxPendingPerAuth: 3,
		globalMaxEntries: 6, globalMaxBytes: 10,
		authMaxEntries: 5, authMaxBytes: 8,
		sessionMaxEntries: 3, sessionMaxBytes: 6,
	})
	auth := [8]byte{0xc1}

	aborted, err := cache.Acquire(auth, 1, 101)
	if err != nil || aborted.state != rpcResultAcquireOwner {
		t.Fatalf("aborted owner = %#v, %v", aborted, err)
	}
	if usage := cache.fairBudget.authSnapshot(auth); usage.entries != 1 || usage.bytes != 1 || usage.pending != 1 {
		t.Fatalf("pending auth reservation = %#v", usage)
	}
	if !aborted.owner.Abort() {
		t.Fatal("owner Abort lost")
	}
	if usage := cache.fairBudget.authSnapshot(auth); usage != (rpcResultBudgetUsage{}) {
		t.Fatalf("Abort leaked auth reservation %#v", usage)
	}

	body, err := cache.Acquire(auth, 1, 102)
	if err != nil || body.state != rpcResultAcquireOwner {
		t.Fatalf("body owner = %#v, %v", body, err)
	}
	cache.Put(auth, 1, 102, &encodedOutboundMessage{body: make([]byte, 4)})
	if usage := cache.fairBudget.sessionSnapshot(auth, 1); usage.entries != 1 || usage.bytes != 4 || usage.pending != 0 {
		t.Fatalf("body session reservation = %#v", usage)
	}

	tombstone, err := cache.Acquire(auth, 2, 201)
	if err != nil || tombstone.state != rpcResultAcquireOwner {
		t.Fatalf("tombstone owner = %#v, %v", tombstone, err)
	}
	// This cannot fit the 10-byte global or 8-byte auth ceiling. Put must not
	// panic or lose ownership; it transfers the one-byte token to a tombstone.
	cache.Put(auth, 2, 201, &encodedOutboundMessage{body: make([]byte, 20)})
	if usage := cache.fairBudget.sessionSnapshot(auth, 2); usage.entries != 1 || usage.bytes != 1 || usage.pending != 0 {
		t.Fatalf("tombstone session reservation = %#v", usage)
	}
	if got := cache.completedEntries.snapshot(); got != 2 {
		t.Fatalf("global entries after body+tombstone = %d, want 2", got)
	}
	if got := cache.completedBytes.snapshot(); got != 5 {
		t.Fatalf("global bytes after body+tombstone = %d, want 5", got)
	}

	cache.Put(auth, 1, 102, &encodedOutboundMessage{body: make([]byte, 2)})
	if got := cache.completedBytes.snapshot(); got != 3 {
		t.Fatalf("replacement did not resize global bytes: %d", got)
	}
	now = now.Add(rpcResultCacheTTL + time.Second)
	_, _ = cache.Get(auth, 1, 102)
	_, _ = cache.Get(auth, 2, 201)
	if got := cache.completedEntries.snapshot(); got != 0 {
		t.Fatalf("TTL leaked global entries %d", got)
	}
	if got := cache.completedBytes.snapshot(); got != 0 {
		t.Fatalf("TTL leaked global bytes %d", got)
	}
	if usage := cache.fairBudget.authSnapshot(auth); usage != (rpcResultBudgetUsage{}) {
		t.Fatalf("TTL leaked auth reservation %#v", usage)
	}
}

func TestRPCResultCacheFullKeyMaphashSpreadsOneSession(t *testing.T) {
	first := newRPCResultCacheWithFlightLimit(time.Now, 64)
	second := newRPCResultCacheWithFlightLimit(time.Now, 64)
	auth := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	const sessionID = int64(99)
	seen := make(map[uint64]struct{})
	differentInstance := false
	for msgID := int64(1); msgID <= 256; msgID++ {
		key := rpcResultCacheKey{authKeyID: auth, sessionID: sessionID, reqMsgID: msgID}
		firstIndex := first.shardIndex(key)
		seen[firstIndex] = struct{}{}
		if firstIndex != second.shardIndex(key) {
			differentInstance = true
		}
	}
	if len(seen) < rpcResultCacheShards/2 {
		t.Fatalf("one session used only %d/%d full-key shards", len(seen), rpcResultCacheShards)
	}
	if !differentInstance {
		t.Fatal("two cache instances produced an identical shard stream; seed is not instance-random")
	}
}

func TestRPCResultCacheConcurrentFairReservationsNeverOvercommit(t *testing.T) {
	cache := newRPCResultCacheWithFairCapacity(time.Now, rpcResultCacheCapacity{
		maxPending: 24, maxPendingPerAuth: 4,
		globalMaxEntries: 24, globalMaxBytes: 24,
		authMaxEntries: 8, authMaxBytes: 8,
		sessionMaxEntries: 3, sessionMaxBytes: 3,
	})
	const callers = 256
	start := make(chan struct{})
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		owners []*rpcResultOwnerLease
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			auth := [8]byte{byte(i % 4)}
			claim, err := cache.Acquire(auth, int64(i%8), int64(1000+i))
			if errors.Is(err, ErrRPCResultFlightCapacity) {
				return
			}
			if err != nil || claim.state != rpcResultAcquireOwner {
				t.Errorf("Acquire %d = %#v, %v", i, claim, err)
				return
			}
			mu.Lock()
			owners = append(owners, claim.owner)
			mu.Unlock()
		}(i)
	}
	close(start)
	wg.Wait()
	if got := cache.completedEntries.snapshot(); got > 24 || got != int64(len(owners)) {
		t.Fatalf("global entry usage=%d owners=%d limit=24", got, len(owners))
	}
	if got := cache.completedBytes.snapshot(); got > 24 || got != int64(len(owners)) {
		t.Fatalf("global byte usage=%d owners=%d limit=24", got, len(owners))
	}
	for i := 0; i < 4; i++ {
		auth := [8]byte{byte(i)}
		usage := cache.fairBudget.authSnapshot(auth)
		if usage.entries > 8 || usage.bytes > 8 || usage.pending > 4 {
			t.Fatalf("auth %d overcommitted: %#v", i, usage)
		}
		for sessionID := int64(0); sessionID < 8; sessionID++ {
			session := cache.fairBudget.sessionSnapshot(auth, sessionID)
			if session.entries > 3 || session.bytes > 3 {
				t.Fatalf("auth %d session %d overcommitted: %#v", i, sessionID, session)
			}
		}
	}
	for _, owner := range owners {
		if !owner.Abort() {
			t.Fatal("concurrent owner did not abort")
		}
	}
	if cache.completedEntries.snapshot() != 0 || cache.completedBytes.snapshot() != 0 {
		t.Fatal("concurrent Abort leaked global fair budget")
	}
}

func TestRPCResultCacheConcurrentOwnerPublicationAcrossShards(t *testing.T) {
	const publications = 256
	now := time.Unix(1000, 0)
	cache := newRPCResultCacheWithFairCapacity(func() time.Time { return now }, rpcResultCacheCapacity{
		maxPending: publications, maxPendingPerAuth: 4,
		globalMaxEntries: publications, globalMaxBytes: publications * 4,
		authMaxEntries: 4, authMaxBytes: 16,
		sessionMaxEntries: 1, sessionMaxBytes: 4,
	})

	type publication struct {
		auth    [8]byte
		session int64
		msgID   int64
		owner   *rpcResultOwnerLease
	}
	publicationsByKey := make([]publication, 0, publications)
	for i := 0; i < publications; i++ {
		auth := [8]byte{byte(i), byte(i >> 8), 0xa5}
		sessionID := int64(10_000 + i)
		msgID := int64(20_000 + i)
		claim, err := cache.Acquire(auth, sessionID, msgID)
		if err != nil || claim.state != rpcResultAcquireOwner {
			t.Fatalf("Acquire %d = %#v, %v", i, claim, err)
		}
		publicationsByKey = append(publicationsByKey, publication{
			auth: auth, session: sessionID, msgID: msgID, owner: claim.owner,
		})
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range publicationsByKey {
		item := publicationsByKey[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if !item.owner.CompleteExecution(true) {
				t.Errorf("CompleteExecution(%d) lost owner", item.msgID)
				return
			}
			cache.Put(item.auth, item.session, item.msgID, &encodedOutboundMessage{body: []byte{1, 2, 3, 4}})
		}()
	}
	close(start)
	wg.Wait()

	for _, item := range publicationsByKey {
		encoded, ok := cache.Get(item.auth, item.session, item.msgID)
		if !ok || encoded == nil || len(encoded.body) != 4 {
			t.Fatalf("completed publication %d missing: ok=%v encoded=%#v", item.msgID, ok, encoded)
		}
	}
	if got := cache.completedEntries.snapshot(); got != publications {
		t.Fatalf("completed entries=%d, want %d", got, publications)
	}
	if got := cache.completedBytes.snapshot(); got != publications*4 {
		t.Fatalf("completed bytes=%d, want %d", got, publications*4)
	}

	now = now.Add(rpcResultCacheTTL + time.Second)
	cache.expireCompletedResults()
	if cache.completedEntries.snapshot() != 0 || cache.completedBytes.snapshot() != 0 {
		t.Fatal("parallel publications leaked fair-budget reservations after TTL")
	}
}

func BenchmarkRPCResultCacheParallelShardPut(b *testing.B) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, rpcResultFlightDefaultMaxPending)
	var nextWorker atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := nextWorker.Add(1)
		auth := [8]byte{
			byte(id), byte(id >> 8), byte(id >> 16), byte(id >> 24),
			byte(id >> 32), byte(id >> 40), byte(id >> 48), byte(id >> 56),
		}
		sessionID := int64(id)
		msgID := int64(1_000_000 + id)
		encoded := &encodedOutboundMessage{body: []byte{1, 2, 3, 4}}
		for pb.Next() {
			cache.Put(auth, sessionID, msgID, encoded)
		}
	})
}

func TestRPCResultCacheEntryReservationTransfersAndReturns(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCacheWithCapacity(func() time.Time { return now }, 4, 2, 2)
	authKeyID := [8]byte{0xa2}

	first, err := cache.Acquire(authKeyID, 1, 101)
	if err != nil || first.state != rpcResultAcquireOwner || cache.completedEntries.snapshot() != 1 {
		t.Fatalf("first pending reservation = %#v entries=%d err=%v", first, cache.completedEntries.snapshot(), err)
	}
	cache.Put(authKeyID, 1, 101, &encodedOutboundMessage{body: []byte{1}})
	if got := cache.completedEntries.snapshot(); got != 1 {
		t.Fatalf("pending -> body changed entry count to %d", got)
	}

	second, err := cache.Acquire(authKeyID, 2, 202)
	if err != nil || second.state != rpcResultAcquireOwner || cache.completedEntries.snapshot() != 2 {
		t.Fatalf("second pending reservation = %#v entries=%d err=%v", second, cache.completedEntries.snapshot(), err)
	}
	// The byte budget has only the second owner's one-byte token remaining.
	// Publication therefore leaves an identity tombstone, which still owns its
	// real process-wide entry slot.
	cache.Put(authKeyID, 2, 202, &encodedOutboundMessage{body: []byte{2, 2, 2}})
	if got := cache.completedEntries.snapshot(); got != 2 {
		t.Fatalf("pending -> tombstone changed entry count to %d", got)
	}
	if _, err := cache.Acquire(authKeyID, 3, 303); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("third admission at entry limit = %v, want capacity", err)
	}

	now = now.Add(rpcResultCacheTTL + time.Second)
	firstShard := cache.shardIndex(rpcResultCacheKey{authKeyID: authKeyID, sessionID: 1, reqMsgID: 101})
	secondShard := cache.shardIndex(rpcResultCacheKey{authKeyID: authKeyID, sessionID: 2, reqMsgID: 202})
	thirdMsgID := rpcResultTestMsgIDOutsideShards(t, cache, authKeyID, 3, 303, firstShard, secondShard)
	third, err := cache.Acquire(authKeyID, 3, thirdMsgID)
	if err != nil || third.state != rpcResultAcquireOwner {
		t.Fatalf("admission after global expiry reap = %#v, %v", third, err)
	}
	if got := cache.completedEntries.snapshot(); got != 1 {
		t.Fatalf("expired entries were not returned before new owner: %d", got)
	}
	if !third.owner.Abort() || cache.completedEntries.snapshot() != 0 {
		t.Fatalf("Abort did not return entry reservation: entries=%d", cache.completedEntries.snapshot())
	}
}

func TestRPCResultCacheConcurrentGlobalEntryReservationNeverOvercommits(t *testing.T) {
	const limit = 8
	cache := newRPCResultCacheWithCapacity(time.Now, 128, 1<<20, limit)
	authKeyID := [8]byte{0xa3}
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		owners []*rpcResultOwnerLease
	)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claim, err := cache.Acquire(authKeyID, int64(i+1), int64(1000+i))
			if err != nil {
				if !errors.Is(err, ErrRPCResultFlightCapacity) {
					t.Errorf("Acquire %d: %v", i, err)
				}
				return
			}
			if claim.state != rpcResultAcquireOwner {
				t.Errorf("Acquire %d state = %d", i, claim.state)
				return
			}
			mu.Lock()
			owners = append(owners, claim.owner)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(owners) != limit || cache.completedEntries.snapshot() != limit {
		t.Fatalf("concurrent owners=%d entries=%d, want %d", len(owners), cache.completedEntries.snapshot(), limit)
	}
	for _, owner := range owners {
		if !owner.Abort() {
			t.Fatal("reserved owner failed to abort")
		}
	}
	if got := cache.completedEntries.snapshot(); got != 0 {
		t.Fatalf("entry reservations after abort = %d", got)
	}
}

func TestRPCResultCacheRoundTripAndTTL(t *testing.T) {
	if rpcResultCacheTTL != 331*time.Second {
		t.Fatalf("replay TTL = %v, want full 300s past + 30s future window + 1s", rpcResultCacheTTL)
	}
	now := time.Unix(1000, 0)
	cache := newRPCResultCache(func() time.Time { return now })

	var keyID [8]byte
	keyID[0] = 0xab
	encoded := &encodedOutboundMessage{body: []byte{1, 2, 3, 4}, typeID: 42, reqMsgID: 7}

	if _, ok := cache.Get(keyID, 5, 7); ok {
		t.Fatal("unexpected hit on empty cache")
	}
	cache.Put(keyID, 5, 7, encoded)
	if got := cache.completedEntries.snapshot(); got != 1 {
		t.Fatalf("direct Put entry reservation = %d, want 1", got)
	}
	if usage := cache.fairBudget.sessionSnapshot(keyID, 5); usage.entries != 1 || usage.bytes != 4 || usage.pending != 0 {
		t.Fatalf("direct Put session reservation = %#v", usage)
	}

	got, ok := cache.Get(keyID, 5, 7)
	if !ok {
		t.Fatal("expected hit")
	}
	// encodedOutboundMessage 不可变契约下 Get/Put 共享指针，不做防御性拷贝。
	if got != encoded {
		t.Fatal("expected shared pointer, got clone")
	}

	// 不同 session / msg_id 不串。
	if _, ok := cache.Get(keyID, 6, 7); ok {
		t.Fatal("hit with wrong session id")
	}
	if _, ok := cache.Get(keyID, 5, 8); ok {
		t.Fatal("hit with wrong msg id")
	}

	// TTL 过期。
	now = now.Add(rpcResultCacheTTL + time.Second)
	if _, ok := cache.Get(keyID, 5, 7); ok {
		t.Fatal("expected expiry after TTL")
	}
	if got := cache.completedEntries.snapshot(); got != 0 {
		t.Fatalf("direct Put expiry left %d entry reservations", got)
	}
	if usage := cache.fairBudget.authSnapshot(keyID); usage != (rpcResultBudgetUsage{}) {
		t.Fatalf("direct Put expiry leaked auth reservation %#v", usage)
	}
}

func TestRPCResultCacheDuplicatePutPreservesCompletedExecutionMetadata(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 1)
	keyID := [8]byte{1, 9, 8, 4}
	const sessionID, reqMsgID = int64(11), int64(12)
	claim, err := cache.Acquire(keyID, sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("Acquire owner = %#v, %v", claim, err)
	}
	if !claim.owner.CompleteExecution(true) || !claim.owner.HandOff() {
		t.Fatal("complete owner metadata")
	}
	first := &encodedOutboundMessage{body: []byte{1, 2, 3, 4}, typeID: 42, reqMsgID: reqMsgID}
	cache.Put(keyID, sessionID, reqMsgID, first)
	second := &encodedOutboundMessage{body: []byte{5, 6, 7, 8}, typeID: 42, reqMsgID: reqMsgID}
	cache.Put(keyID, sessionID, reqMsgID, second)

	replay, err := cache.Acquire(keyID, sessionID, reqMsgID)
	if err != nil || replay.state != rpcResultAcquireCompleted || replay.encoded != second ||
		!replay.executionKnown || !replay.executionOK {
		t.Fatalf("duplicate Put metadata = %#v, err=%v", replay, err)
	}
}

func TestRPCResultCacheShardCapacityNeverEvictsUnexpiredResult(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCache(func() time.Time { return now })

	var keyID [8]byte
	firstKey := rpcResultCacheKey{authKeyID: keyID, sessionID: 1, reqMsgID: 100}
	shard := cache.shard(firstKey)
	shard.mu.Lock()
	shard.maxEntries = 1
	shard.mu.Unlock()

	claim, err := cache.Acquire(keyID, 1, 100)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("first admission = %#v, %v", claim, err)
	}
	first := &encodedOutboundMessage{body: []byte{1}}
	cache.Put(keyID, 1, 100, first)
	secondMsgID := rpcResultTestMsgIDForShard(t, cache, keyID, 1, 101, cache.shardIndex(firstKey))
	if _, err := cache.Acquire(keyID, 1, secondMsgID); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("full-shard admission = %v, want capacity", err)
	}
	if got, ok := cache.Get(keyID, 1, 100); !ok || got != first {
		t.Fatalf("unexpired first result was displaced: got=%p ok=%v", got, ok)
	}

	now = now.Add(rpcResultCacheTTL + time.Second)
	claim, err = cache.Acquire(keyID, 1, secondMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("admission after expiry = %#v, %v", claim, err)
	}
	claim.owner.Abort()
}

func TestRPCResultCacheGlobalByteCapacityNeverEvictsUnexpiredResults(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCacheWithLimits(func() time.Time { return now }, 32, 10)
	var keyID [8]byte

	// Five two-byte results consume the global budget. The sixth admission must
	// fail bounded; none of the retained results may be sacrificed for it.
	for sessionID := int64(1); sessionID <= 5; sessionID++ {
		claim, err := cache.Acquire(keyID, sessionID, 100+sessionID)
		if err != nil || claim.state != rpcResultAcquireOwner {
			t.Fatalf("admission %d = %#v, %v", sessionID, claim, err)
		}
		cache.Put(keyID, sessionID, 100+sessionID, &encodedOutboundMessage{body: []byte{1, 2}})
	}
	if got := cache.completedBytes.snapshot(); got != 10 {
		t.Fatalf("completed bytes at capacity = %d, want 10", got)
	}
	if _, err := cache.Acquire(keyID, 6, 106); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("byte-full admission = %v, want capacity", err)
	}
	for sessionID := int64(1); sessionID <= 5; sessionID++ {
		if _, ok := cache.Get(keyID, sessionID, 100+sessionID); !ok {
			t.Fatalf("unexpired result %d was evicted", sessionID)
		}
	}
}

func TestRPCResultCacheByteBudgetReturnsOnReplaceExpiryAndCapacity(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCacheWithLimits(func() time.Time { return now }, 32, 32)
	var keyID [8]byte

	cache.Put(keyID, 1, 101, &encodedOutboundMessage{body: make([]byte, 4)})
	cache.Put(keyID, 1, 101, &encodedOutboundMessage{body: make([]byte, 7)})
	if got := cache.completedBytes.snapshot(); got != 7 {
		t.Fatalf("completed bytes after growing replacement = %d, want 7", got)
	}
	if usage := cache.fairBudget.sessionSnapshot(keyID, 1); usage.entries != 1 || usage.bytes != 7 {
		t.Fatalf("replacement fair reservation after growth = %#v", usage)
	}
	cache.Put(keyID, 1, 101, &encodedOutboundMessage{body: make([]byte, 2)})
	if got := cache.completedBytes.snapshot(); got != 2 {
		t.Fatalf("completed bytes after shrinking replacement = %d, want 2", got)
	}
	if usage := cache.fairBudget.sessionSnapshot(keyID, 1); usage.entries != 1 || usage.bytes != 2 {
		t.Fatalf("replacement fair reservation after shrink = %#v", usage)
	}

	now = now.Add(rpcResultCacheTTL + time.Second)
	if _, ok := cache.Get(keyID, 1, 101); ok {
		t.Fatal("replacement should expire")
	}
	if got := cache.completedBytes.snapshot(); got != 0 {
		t.Fatalf("completed bytes after expiry = %d, want 0", got)
	}

	key := rpcResultCacheKey{authKeyID: keyID, sessionID: 2, reqMsgID: 201}
	shard := cache.shard(key)
	shard.mu.Lock()
	shard.maxEntries = 1
	shard.mu.Unlock()
	claim, err := cache.Acquire(keyID, 2, 201)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("entry-capacity first admission = %#v, %v", claim, err)
	}
	cache.Put(keyID, 2, 201, &encodedOutboundMessage{body: make([]byte, 3)})
	secondMsgID := rpcResultTestMsgIDForShard(t, cache, keyID, 2, 202, cache.shardIndex(key))
	if _, err := cache.Acquire(keyID, 2, secondMsgID); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("entry-capacity second admission = %v", err)
	}
	if got := cache.completedBytes.snapshot(); got != 3 {
		t.Fatalf("completed bytes after capacity rejection = %d, want 3", got)
	}
	if _, ok := cache.Get(keyID, 2, 201); !ok {
		t.Fatal("capacity rejection displaced the first result")
	}
}

func TestRPCResultCachePublicationOverflowLeavesReplayCapacityTombstone(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCacheWithLimits(func() time.Time { return now }, 32, 4)
	var keyID [8]byte
	claim, err := cache.Acquire(keyID, 1, 101)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("owner admission = %#v, %v", claim, err)
	}
	claim.owner.CompleteExecution(true)
	tooLarge := &encodedOutboundMessage{body: make([]byte, 5)}
	cache.Put(keyID, 1, 101, tooLarge)
	if got := cache.completedBytes.snapshot(); got != 1 {
		t.Fatalf("tombstone bytes = %d, want 1", got)
	}
	if _, ok := cache.Get(keyID, 1, 101); ok {
		t.Fatal("capacity tombstone must not masquerade as a replayable body")
	}
	if _, err := cache.Acquire(keyID, 1, 101); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("duplicate after publication overflow = %v, want capacity", err)
	}

	now = now.Add(rpcResultCacheTTL + time.Second)
	retry, err := cache.Acquire(keyID, 1, 101)
	if err != nil || retry.state != rpcResultAcquireOwner {
		t.Fatalf("admission after tombstone expiry = %#v, %v", retry, err)
	}
	retry.owner.Abort()
}

func TestRPCResultCacheByteCapacityReclaimsExpiredAcrossShards(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := newRPCResultCacheWithLimits(func() time.Time { return now }, 32, 2)
	var keyID [8]byte

	first, err := cache.Acquire(keyID, 1, 101)
	if err != nil || first.state != rpcResultAcquireOwner {
		t.Fatalf("first admission = %#v, %v", first, err)
	}
	cache.Put(keyID, 1, 101, &encodedOutboundMessage{body: []byte{1, 2}})
	if got := cache.completedBytes.snapshot(); got != 2 {
		t.Fatalf("full budget = %d, want 2", got)
	}

	// Select a key in another full-key shard. Its failed one-byte reservation
	// must trigger the cold-path global expiry reap before returning capacity.
	now = now.Add(rpcResultCacheTTL + time.Second)
	firstKey := rpcResultCacheKey{authKeyID: keyID, sessionID: 1, reqMsgID: 101}
	secondMsgID := rpcResultTestMsgIDOutsideShard(t, cache, keyID, 2, 202, cache.shardIndex(firstKey))
	second, err := cache.Acquire(keyID, 2, secondMsgID)
	if err != nil || second.state != rpcResultAcquireOwner {
		t.Fatalf("cross-shard admission after expiry = %#v, %v", second, err)
	}
	second.owner.Abort()
	if got := cache.completedBytes.snapshot(); got != 0 {
		t.Fatalf("bytes after expired reap and abort = %d, want 0", got)
	}
}

func TestRPCResultCacheServerOptionsPropagateFairLimits(t *testing.T) {
	sessionBytes := int64(maxOutboundBodyBytes)
	s := New(Options{
		RPCGlobalMaxTasks:               6,
		RPCResultCacheMaxEntries:        12,
		RPCResultCacheMaxBytes:          sessionBytes + 2048,
		RPCResultCacheAuthMaxEntries:    8,
		RPCResultCacheAuthMaxBytes:      sessionBytes + 1024,
		RPCResultCacheSessionMaxEntries: 4,
		RPCResultCacheSessionMaxBytes:   sessionBytes,
		RPCResultPendingPerAuth:         3,
	})
	if s.rpcResults.completedEntries.max != 12 || s.rpcResults.completedBytes.max != sessionBytes+2048 {
		t.Fatalf("global option propagation = %d/%d", s.rpcResults.completedEntries.max, s.rpcResults.completedBytes.max)
	}
	budget := s.rpcResults.fairBudget
	if budget.authLimit.entries != 8 || budget.authLimit.bytes != sessionBytes+1024 ||
		budget.sessionLimit.entries != 4 || budget.sessionLimit.bytes != sessionBytes || budget.pendingPerAuth != 3 {
		t.Fatalf("fair option propagation = auth:%#v session:%#v pending:%d",
			budget.authLimit, budget.sessionLimit, budget.pendingPerAuth)
	}
}

func TestRPCResultCacheServerOptionsFailFast(t *testing.T) {
	base := Options{
		RPCGlobalMaxTasks:               6,
		RPCResultCacheMaxEntries:        12,
		RPCResultCacheMaxBytes:          64 << 20,
		RPCResultCacheAuthMaxEntries:    8,
		RPCResultCacheAuthMaxBytes:      32 << 20,
		RPCResultCacheSessionMaxEntries: 4,
		RPCResultCacheSessionMaxBytes:   16 << 20,
		RPCResultPendingPerAuth:         3,
	}
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{name: "entry hierarchy", mutate: func(o *Options) { o.RPCResultCacheAuthMaxEntries = 13 }},
		{name: "body does not fit session", mutate: func(o *Options) { o.RPCResultCacheSessionMaxBytes = maxOutboundBodyBytes - 1 }},
		{name: "pending hierarchy", mutate: func(o *Options) { o.RPCResultPendingPerAuth = 7 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opts := base
			test.mutate(&opts)
			defer func() {
				if recover() == nil {
					t.Fatal("New accepted invalid rpc_result cache options")
				}
			}()
			_ = New(opts)
		})
	}
}

func rpcResultTestMsgIDForShard(
	t *testing.T,
	cache *rpcResultCache,
	authKeyID [8]byte,
	sessionID, start int64,
	target uint64,
) int64 {
	t.Helper()
	for msgID := start; msgID < start+1_000_000; msgID++ {
		key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: msgID}
		if cache.shardIndex(key) == target {
			return msgID
		}
	}
	t.Fatal("failed to find rpc_result key for target shard")
	return 0
}

func rpcResultTestMsgIDOutsideShard(
	t *testing.T,
	cache *rpcResultCache,
	authKeyID [8]byte,
	sessionID, start int64,
	excluded uint64,
) int64 {
	t.Helper()
	for msgID := start; msgID < start+1_000_000; msgID++ {
		key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: msgID}
		if cache.shardIndex(key) != excluded {
			return msgID
		}
	}
	t.Fatal("failed to find rpc_result key outside excluded shard")
	return 0
}

func rpcResultTestMsgIDOutsideShards(
	t *testing.T,
	cache *rpcResultCache,
	authKeyID [8]byte,
	sessionID, start int64,
	excluded ...uint64,
) int64 {
	t.Helper()
	for msgID := start; msgID < start+1_000_000; msgID++ {
		key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: sessionID, reqMsgID: msgID}
		index := cache.shardIndex(key)
		allowed := true
		for _, blocked := range excluded {
			if index == blocked {
				allowed = false
				break
			}
		}
		if allowed {
			return msgID
		}
	}
	t.Fatal("failed to find rpc_result key outside excluded shards")
	return 0
}
