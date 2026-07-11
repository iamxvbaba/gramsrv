package mtprotoedge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func rpcFlightTestAuthID(seed byte) [8]byte {
	return [8]byte{seed, seed + 1, seed + 2, seed + 3}
}

func TestRPCResultFlightConcurrentAcquireHasUniqueOwner(t *testing.T) {
	const callers = 64
	cache := newRPCResultCacheWithFlightLimit(time.Now, callers)
	authKeyID := rpcFlightTestAuthID(1)
	start := make(chan struct{})
	results := make(chan rpcResultAcquire, callers)
	errs := make(chan error, callers)

	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			<-start
			claim, err := cache.Acquire(authKeyID, 10, 100)
			if err != nil {
				errs <- err
				return
			}
			results <- claim
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("Acquire: %v", err)
	}
	owners := 0
	waiters := 0
	var owner *rpcResultOwnerLease
	for claim := range results {
		switch claim.state {
		case rpcResultAcquireOwner:
			owners++
			owner = claim.owner
		case rpcResultAcquirePending:
			waiters++
			if claim.waiter == nil {
				t.Fatal("pending claim has nil waiter")
			}
		default:
			t.Fatalf("unexpected claim state %d", claim.state)
		}
	}
	if owners != 1 || waiters != callers-1 {
		t.Fatalf("claims = owners:%d waiters:%d, want 1/%d", owners, waiters, callers-1)
	}
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("pending count = %d, want 1", got)
	}
	if owner == nil || !owner.Abort() {
		t.Fatal("unique owner did not abort its claim")
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("pending count after abort = %d, want 0", got)
	}
}

func TestRPCResultFlightPutPublishesAndWakesAllWaiters(t *testing.T) {
	const waiters = 24
	cache := newRPCResultCacheWithFlightLimit(time.Now, 32)
	authKeyID := rpcFlightTestAuthID(10)
	owner, err := cache.Acquire(authKeyID, 20, 200)
	if err != nil || owner.state != rpcResultAcquireOwner || owner.owner == nil {
		t.Fatalf("owner Acquire = state:%d err:%v", owner.state, err)
	}

	waiterClaims := make([]*rpcResultWaiter, 0, waiters)
	for i := 0; i < waiters; i++ {
		claim, acquireErr := cache.Acquire(authKeyID, 20, 200)
		if acquireErr != nil || claim.state != rpcResultAcquirePending || claim.waiter == nil {
			t.Fatalf("waiter %d Acquire = state:%d err:%v", i, claim.state, acquireErr)
		}
		waiterClaims = append(waiterClaims, claim.waiter)
	}

	type waiterResult struct {
		encoded *encodedOutboundMessage
		cached  *encodedOutboundMessage
		ok      bool
		err     error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan waiterResult, waiters)
	for _, waiter := range waiterClaims {
		go func(w *rpcResultWaiter) {
			encoded, ok, waitErr := w.Wait(ctx)
			cached, _ := cache.Get(authKeyID, 20, 200)
			results <- waiterResult{encoded: encoded, cached: cached, ok: ok, err: waitErr}
		}(waiter)
	}

	want := &encodedOutboundMessage{body: []byte{1, 2, 3, 4}, typeID: 42, reqMsgID: 200}
	cache.Put(authKeyID, 20, 200, want)
	for i := 0; i < waiters; i++ {
		got := <-results
		if got.err != nil || !got.ok {
			t.Fatalf("waiter %d result = ok:%v err:%v", i, got.ok, got.err)
		}
		if got.encoded != want || got.cached != want {
			t.Fatalf("waiter %d observed direct/cache pointers %p/%p, want %p", i, got.encoded, got.cached, want)
		}
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("pending count after Put = %d, want 0", got)
	}
	completed, err := cache.Acquire(authKeyID, 20, 200)
	if err != nil || completed.state != rpcResultAcquireCompleted || completed.encoded != want {
		t.Fatalf("completed Acquire = state:%d encoded:%p err:%v", completed.state, completed.encoded, err)
	}
	if owner.owner.Abort() {
		t.Fatal("completed owner's stale lease aborted a resolved claim")
	}
}

func TestRPCResultFlightAbortWakesAndAllowsReclaim(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 2)
	authKeyID := rpcFlightTestAuthID(20)
	first, err := cache.Acquire(authKeyID, 30, 300)
	if err != nil || first.state != rpcResultAcquireOwner {
		t.Fatalf("first Acquire = state:%d err:%v", first.state, err)
	}
	waiting, err := cache.Acquire(authKeyID, 30, 300)
	if err != nil || waiting.state != rpcResultAcquirePending {
		t.Fatalf("waiting Acquire = state:%d err:%v", waiting.state, err)
	}
	if !first.owner.Abort() {
		t.Fatal("first Abort lost")
	}
	if encoded, ok, waitErr := waiting.waiter.Wait(context.Background()); waitErr != nil || ok || encoded != nil {
		t.Fatalf("aborted Wait = encoded:%p ok:%v err:%v", encoded, ok, waitErr)
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("pending count after Abort = %d, want 0", got)
	}

	second, err := cache.Acquire(authKeyID, 30, 300)
	if err != nil || second.state != rpcResultAcquireOwner || second.owner == nil {
		t.Fatalf("reclaim = state:%d err:%v", second.state, err)
	}
	if first.owner.Abort() {
		t.Fatal("stale first lease aborted the replacement owner")
	}
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("pending count after reclaim = %d, want 1", got)
	}
	if !second.owner.Abort() || cache.flightLimit.snapshot() != 0 {
		t.Fatal("replacement owner did not release its claim")
	}
}

func TestRPCResultFlightCompletedCachePressureDoesNotEvictPending(t *testing.T) {
	now := time.Unix(1_000, 0)
	cache := newRPCResultCacheWithFlightLimit(func() time.Time { return now }, 4)
	authKeyID := rpcFlightTestAuthID(30)
	pending, err := cache.Acquire(authKeyID, 40, 400)
	if err != nil || pending.state != rpcResultAcquireOwner {
		t.Fatalf("pending Acquire = state:%d err:%v", pending.state, err)
	}

	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: 40, reqMsgID: 400}
	shard := cache.shard(key)
	shard.mu.Lock()
	shard.maxEntries = 2
	shard.maxBytes = 2
	shard.mu.Unlock()
	for i := int64(0); i < 16; i++ {
		cache.Put(authKeyID, 40, 500+i, &encodedOutboundMessage{body: []byte{byte(i)}})
	}
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("completed trim changed pending count to %d", got)
	}
	joined, err := cache.Acquire(authKeyID, 40, 400)
	if err != nil || joined.state != rpcResultAcquirePending {
		t.Fatalf("Acquire after completed trim = state:%d err:%v", joined.state, err)
	}

	// Expire the independent completed cache and prove the pending owner remains.
	now = now.Add(rpcResultCacheTTL + time.Second)
	_, _ = cache.Get(authKeyID, 40, 515)
	joinedAfterTTL, err := cache.Acquire(authKeyID, 40, 400)
	if err != nil || joinedAfterTTL.state != rpcResultAcquirePending {
		t.Fatalf("Acquire after completed TTL = state:%d err:%v", joinedAfterTTL.state, err)
	}
	if !pending.owner.Abort() || cache.flightLimit.snapshot() != 0 {
		t.Fatal("pending claim did not survive pressure through explicit Abort")
	}
}

func TestRPCResultFlightCapacityAndCountReturn(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 2)
	authKeyID := rpcFlightTestAuthID(40)
	first, err := cache.Acquire(authKeyID, 50, 501)
	if err != nil || first.state != rpcResultAcquireOwner {
		t.Fatalf("first Acquire = state:%d err:%v", first.state, err)
	}
	second, err := cache.Acquire(authKeyID, 50, 502)
	if err != nil || second.state != rpcResultAcquireOwner {
		t.Fatalf("second Acquire = state:%d err:%v", second.state, err)
	}
	joined, err := cache.Acquire(authKeyID, 50, 501)
	if err != nil || joined.state != rpcResultAcquirePending {
		t.Fatalf("join at capacity = state:%d err:%v", joined.state, err)
	}
	if _, err := cache.Acquire(authKeyID, 50, 503); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("over-capacity Acquire err = %v, want %v", err, ErrRPCResultFlightCapacity)
	}
	if got := cache.flightLimit.snapshot(); got != 2 {
		t.Fatalf("pending count at capacity = %d, want 2", got)
	}

	want := &encodedOutboundMessage{body: []byte{9}, reqMsgID: 501}
	cache.Put(authKeyID, 50, 501, want)
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("pending count after Put = %d, want 1", got)
	}
	third, err := cache.Acquire(authKeyID, 50, 503)
	if err != nil || third.state != rpcResultAcquireOwner {
		t.Fatalf("Acquire after returned slot = state:%d err:%v", third.state, err)
	}
	completed, err := cache.Acquire(authKeyID, 50, 501)
	if err != nil || completed.state != rpcResultAcquireCompleted || completed.encoded != want {
		t.Fatalf("completed Acquire at capacity = state:%d err:%v", completed.state, err)
	}
	if encoded, ok, waitErr := joined.waiter.Wait(context.Background()); waitErr != nil || !ok || encoded != want {
		t.Fatalf("joined Wait = encoded:%p ok:%v err:%v", encoded, ok, waitErr)
	}
	if !second.owner.Abort() || !third.owner.Abort() {
		t.Fatal("owners failed to return remaining capacity")
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("final pending count = %d, want 0", got)
	}
}

func TestRPCResultFlightOversizedPutStillResolvesWaiters(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 1)
	authKeyID := rpcFlightTestAuthID(50)
	owner, err := cache.Acquire(authKeyID, 60, 600)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("owner Acquire = state:%d err:%v", owner.state, err)
	}
	joined, err := cache.Acquire(authKeyID, 60, 600)
	if err != nil || joined.state != rpcResultAcquirePending {
		t.Fatalf("joined Acquire = state:%d err:%v", joined.state, err)
	}

	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: 60, reqMsgID: 600}
	shard := cache.shard(key)
	shard.mu.Lock()
	shard.maxBytes = 1
	shard.mu.Unlock()
	want := &encodedOutboundMessage{body: []byte{1, 2}, reqMsgID: 600}
	cache.Put(authKeyID, 60, 600, want)
	if encoded, ok, waitErr := joined.waiter.Wait(context.Background()); waitErr != nil || !ok || encoded != want {
		t.Fatalf("oversized Wait = encoded:%p ok:%v err:%v", encoded, ok, waitErr)
	}
	if _, ok := cache.Get(authKeyID, 60, 600); ok {
		t.Fatal("oversized result changed completed-cache compatibility")
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("oversized Put leaked pending count %d", got)
	}
	if owner.owner.Abort() {
		t.Fatal("oversized Put left its old owner abortable")
	}
	retry, err := cache.Acquire(authKeyID, 60, 600)
	if err != nil || retry.state != rpcResultAcquireOwner {
		t.Fatalf("retry after uncacheable completion = state:%d err:%v", retry.state, err)
	}
	if !retry.owner.Abort() {
		t.Fatal("retry owner failed to abort")
	}
}

func TestRPCResultFlightWaitContextDoesNotReleaseOwner(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 1)
	authKeyID := rpcFlightTestAuthID(60)
	owner, err := cache.Acquire(authKeyID, 70, 700)
	if err != nil {
		t.Fatalf("owner Acquire: %v", err)
	}
	joined, err := cache.Acquire(authKeyID, 70, 700)
	if err != nil {
		t.Fatalf("joined Acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if encoded, ok, waitErr := joined.waiter.Wait(ctx); !errors.Is(waitErr, context.Canceled) || ok || encoded != nil {
		t.Fatalf("canceled Wait = encoded:%p ok:%v err:%v", encoded, ok, waitErr)
	}
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("waiter cancellation released owner count to %d", got)
	}
	if !owner.owner.Abort() || cache.flightLimit.snapshot() != 0 {
		t.Fatal("owner did not retain and release claim after waiter cancellation")
	}
}

func TestRPCResultFlightConcurrentCapacityReturnsAllSlots(t *testing.T) {
	const (
		limit   = 32
		callers = 512
	)
	cache := newRPCResultCacheWithFlightLimit(time.Now, limit)
	authKeyID := rpcFlightTestAuthID(70)
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			claim, err := cache.Acquire(authKeyID, 80+int64(i%4), 1_000+int64(i))
			if errors.Is(err, ErrRPCResultFlightCapacity) {
				return
			}
			if err != nil {
				errs <- err
				return
			}
			if claim.state != rpcResultAcquireOwner || claim.owner == nil {
				errs <- errors.New("unique-key claim did not become owner")
				return
			}
			if i%2 == 0 {
				cache.Put(authKeyID, 80+int64(i%4), 1_000+int64(i), &encodedOutboundMessage{body: []byte{1}})
			} else if !claim.owner.Abort() {
				errs <- errors.New("owner Abort lost")
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent claim: %v", err)
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("concurrent completion leaked %d pending slots", got)
	}
}

func TestQueuedRPCConnectionCloseAbortsOwnerClaim(t *testing.T) {
	s := New(Options{})
	c := newInboundTestConn(s.rpcScheduler, 1, 4, time.Second)
	claim, err := s.rpcResults.Acquire([8]byte{9}, 90, 900)
	if err != nil || claim.state != rpcResultAcquireOwner || claim.owner == nil {
		t.Fatalf("Acquire owner = state:%d err:%v", claim.state, err)
	}
	reservation, err := c.reserveInboundRPC(context.Background(), "test.queuedFlight", 4)
	if err != nil {
		t.Fatalf("reserve queued RPC: %v", err)
	}
	task := s.newInboundRPCTask(c, 900, "test.queuedFlight", []byte{1, 2, 3, 4}, claim.owner)
	if err := reservation.commit(task); err != nil {
		t.Fatalf("commit queued RPC: %v", err)
	}

	// The scheduler is intentionally not started, so close must drain the queued
	// task and invoke its independent flight-release callback.
	c.closeInboundRPCScheduler()
	if got := s.rpcResults.flightLimit.snapshot(); got != 0 {
		t.Fatalf("connection close leaked %d queued owner claims", got)
	}
	if tasks, bytes := s.rpcScheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("connection close leaked scheduler budget %d/%d", tasks, bytes)
	}
	retry, err := s.rpcResults.Acquire([8]byte{9}, 90, 900)
	if err != nil || retry.state != rpcResultAcquireOwner {
		t.Fatalf("reclaim after queued close = state:%d err:%v", retry.state, err)
	}
	if !retry.owner.Abort() {
		t.Fatal("replacement owner did not abort")
	}
}
