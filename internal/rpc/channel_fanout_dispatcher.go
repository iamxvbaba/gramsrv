package rpc

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// channel fan-out async-ification (design docs/channel-fanout-async-design.md Phase 0 / §9 v1 minimal implementation).
//
// Goal: move channel payload fan-out off the sender's RPC synchronous path -- the sender waits
// only for the transaction + its own-perspective echo (rpc_result), and delivery to the other
// online members is handed to this dispatcher's background workers.
//
// v1 (single instance) key trade-offs:
//   - the durable source of truth is still channel_update_events; this dispatcher only does
//     best-effort online acceleration, and losses are covered by the client's
//     getChannelDifference (design decision 1).
//   - hash by channelID to a fixed shard so the same channel is processed serially (FIFO ->
//     monotonic pts), satisfying DrKLO Android's ~1.5s out-of-order window and TDesktop
//     PtsWaiter's continuity expectation (design §10.2).
//   - single instance + no durable re-delivery queue + per-channel serialization -> no
//     out-of-order, no self-duplication, so v1 does not need the per-session at-most-once double
//     watermark (that is a Phase 3 cross-instance concern, design §9/§10.1).
//   - when the bounded queue is full, do not silently drop the recovery trigger: downgrade the
//     real payload job to a per-channel coalesced UpdateChannelTooLong nudge keeping only the
//     highest pts. Each shard drains fairly and independently; even if the channel then goes
//     quiet, it does not depend on "the next message" to trigger getChannelDifference (design
//     constraint B).

const (
	defaultChannelFanoutShards = 64
	// The old 64x2048 channel buffers eagerly retained up to 131k closures (each may capture a
	// message batch and ~2k recipients).  Keep one small FIFO per ordering shard and enforce a
	// process-wide retained-byte budget below.
	defaultChannelFanoutBuffer                  = 64
	defaultChannelFanoutMaxQueuedJobs           = 4096
	defaultChannelFanoutMaxQueuedBytes          = 256 << 20
	defaultChannelFanoutOverflowPerShard        = 256
	defaultChannelNudgeWorkers                  = 8
	defaultChannelNudgeQueue                    = 4096
	defaultChannelFanoutRecoverySweepPage       = 256
	channelFanoutMinRetainedBytes         int64 = 64 << 10
	channelFanoutNudgeRetryMin                  = time.Millisecond
	channelFanoutNudgeRetryMax                  = 50 * time.Millisecond
	channelFanoutRecoveryRetryMin               = 10 * time.Millisecond
	channelFanoutRecoveryRetryMax               = time.Second
	defaultChannelFanoutNudgeDeadline           = 5 * time.Second
)

// channelFanoutBuilder builds the channel updates from a viewer's perspective. Unlike the
// synchronous channelUpdatesBuilder, it accepts a background ctx provided by the worker (the
// request ctx is already canceled when fan-out runs asynchronously), and the implementation must
// not read viewer/auth-derived data from ctx (viewerUserID is passed explicitly).
type channelFanoutBuilder func(ctx context.Context, viewerUserID int64) *tg.Updates

// channelFanoutJob is one channel payload fan-out task. Pts is only used for logging/coalescing
// semantics; the source of truth is still channel_update_events, and the worker only does online
// delivery. originAuthKeyID is the physical raw auth key (matching the comparison side of
// SessionManager.shouldExcludeSession), used to explicitly exclude the originating device --
// during asynchronous execution the request ctx is already invalid, so exclusion can no longer be
// derived from ctx.
type channelFanoutJob struct {
	scope           channelFanoutScope
	originUserID    int64
	channelID       int64
	pts             int
	recipients      []int64
	originAuthKeyID [8]byte
	originSessionID int64
	prefetch        channelFanoutPrefetch
	build           channelFanoutBuilder
	// retainedBytes is a conservative reservation for the request-derived closure, result
	// snapshots and explicit recipient slice.  It is charged before the job enters any queue.
	retainedBytes int64
	// queueSeq is the dispatcher shard's internal FIFO sequence number. Only jobs that successfully
	// enter the normal payload queue consume a sequence number; the overflow watermark records the
	// largest sequence number accepted when enqueueing failed, and the nudge is only sent after
	// those earlier payloads are processed, so a nudge cannot overtake the normal FIFO payloads
	// before it.
	queueSeq uint64
}

// channelFanoutOverflow is the nudge-only recovery watermark when the queue is full. Only the
// highest pts is kept per channel; barrier is the last shard sequence number that entered the
// normal FIFO before this overflow.
type channelFanoutOverflow struct {
	pts     int
	barrier uint64
}

// channelFanoutShard puts the normal payload FIFO and the overflow nudge mailbox under the same
// worker. overflowOrder contains each channel at most once; a hot channel only updates the map
// watermark and cannot fill up order, so it cannot starve the other channels' single recovery
// nudge forever.
type channelFanoutShard struct {
	jobs         chan channelFanoutJob
	overflowWake chan struct{}
	// overflowSpace is a generation channel, not a one-token notification.  A slot
	// release closes the current generation and installs a fresh channel while mu is
	// held, waking every waiter that observed the old full mailbox.  Each waiter then
	// competes under mu for the actually available slots; losers observe the new
	// generation and sleep again.  This avoids losing N-1 wakeups when several slots
	// are released before any of N waiters gets scheduled.
	overflowSpace chan struct{}

	mu            sync.Mutex
	nextSeq       uint64
	processedSeq  uint64
	overflow      map[int64]channelFanoutOverflow
	overflowOrder []int64
	overflowLimit int
	// overflowWaiters is guarded by mu and only covers the distinct-channel
	// saturation slow path.  Besides making the wait lifecycle explicit, it avoids
	// allocating a fresh generation channel when no goroutine is subscribed.
	overflowWaiters int
}

func newChannelFanoutShard(buffer int) *channelFanoutShard {
	return &channelFanoutShard{
		jobs:          make(chan channelFanoutJob, buffer),
		overflowWake:  make(chan struct{}, 1),
		overflowSpace: make(chan struct{}),
		overflow:      make(map[int64]channelFanoutOverflow),
		overflowLimit: defaultChannelFanoutOverflowPerShard,
	}
}

// enqueue tries to put the real payload into the normal FIFO; when full, coalesces into a
// per-channel nudge-only watermark with the highest pts. Returns true for a normal enqueue, false
// if it safely downgraded to an overflow watermark.
func (s *channelFanoutShard) enqueue(job channelFanoutJob) bool {
	s.mu.Lock()
	job.queueSeq = s.nextSeq + 1
	select {
	case s.jobs <- job:
		s.nextSeq = job.queueSeq
		s.mu.Unlock()
		return true
	default:
		s.mu.Unlock()
		return false
	}
}

func (s *channelFanoutShard) enqueueOverflow(channelID int64, pts int) bool {
	s.mu.Lock()
	accepted := s.addOverflowLocked(channelID, pts)
	s.mu.Unlock()
	if accepted {
		s.signalOverflow()
	}
	return accepted
}

func (s *channelFanoutShard) enqueueOverflowWait(ctx context.Context, channelID int64, pts int, stop <-chan struct{}) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	default:
	}
	// Try and capture the current space generation under the same lock.  Separating
	// these operations creates a classic missed-wakeup window: a drain may free the
	// mailbox after the failed try but before the waiter starts observing the signal.
	s.mu.Lock()
	if s.addOverflowLocked(channelID, pts) {
		s.mu.Unlock()
		s.signalOverflow()
		return true
	}
	space := s.overflowSpace
	s.overflowWaiters++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.overflowWaiters--
		s.mu.Unlock()
	}()

	// Same-channel overflow is the hot saturation path and only updates an existing map item.
	// Distinct-channel saturation may wait here only from the dispatcher's fixed recovery sweep
	// actor. RPC producers never call this method: they publish an O(1) global recovery generation
	// when the bounded mailbox is full, so request goroutines cannot be exhausted by fan-out
	// admission pressure.
	for {
		// Cardinality is full with distinct channels. Apply bounded-memory backpressure instead of
		// allocating an unbounded recovery map or dropping the recovery watermark.
		select {
		case <-space:
		case <-ctx.Done():
			return false
		case <-stop:
			return false
		}
		// Do not turn a release racing with cancellation into admission after the caller ended.
		select {
		case <-ctx.Done():
			return false
		case <-stop:
			return false
		default:
		}
		// Retrying and subscribing to the next generation must also be atomic with
		// respect to a release.  Broadcast wakeups can be spurious for a particular
		// waiter (another waiter may win the sole slot), so loop until accepted or stopped.
		s.mu.Lock()
		if s.addOverflowLocked(channelID, pts) {
			s.mu.Unlock()
			s.signalOverflow()
			return true
		}
		space = s.overflowSpace
		s.mu.Unlock()
	}
}

func (s *channelFanoutShard) addOverflowLocked(channelID int64, pts int) bool {
	item, exists := s.overflow[channelID]
	if !exists {
		if len(s.overflow) >= s.overflowLimit {
			return false
		}
		s.overflowOrder = append(s.overflowOrder, channelID)
		// The first overflow fixes the FIFO barrier. Later same-channel losses only raise the
		// durable pts watermark: moving the barrier on every merge lets a continuously full
		// payload queue keep the recovery nudge one slot behind forever. UpdateChannelTooLong is
		// an idempotent catch-up trigger, so it is safe for its newest pts to overtake payloads
		// accepted after the first loss; those payloads become harmless duplicates after
		// getChannelDifference converges the client.
		item.barrier = s.nextSeq
	}
	if pts > item.pts {
		item.pts = pts
	}
	s.overflow[channelID] = item
	return true
}

func (s *channelFanoutShard) markProcessed(seq uint64) {
	s.mu.Lock()
	if seq > s.processedSeq {
		s.processedSeq = seq
	}
	s.mu.Unlock()
}

// signalOverflowSpaceLocked announces a mailbox-cardinality decrease.  Callers
// must hold s.mu.  Close-and-replace provides broadcast generations without an
// unbounded waiter list, goroutine-per-waiter, or lossy fixed-capacity token queue.
func (s *channelFanoutShard) signalOverflowSpaceLocked() {
	if s.overflowWaiters == 0 {
		return
	}
	close(s.overflowSpace)
	s.overflowSpace = make(chan struct{})
}

// popOverflow is for mailbox/cardinality unit tests to release one overflow directly; the
// production drain must go through tryQueueOverflow so the watermark is not deleted before
// nudgeJobs has actually accepted it successfully.
func (s *channelFanoutShard) popOverflow() (channelID int64, pts int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for remaining := len(s.overflowOrder); remaining > 0; remaining-- {
		channelID = s.overflowOrder[0]
		s.overflowOrder = s.overflowOrder[1:]
		item, exists := s.overflow[channelID]
		if !exists {
			continue
		}
		if item.barrier > s.processedSeq {
			s.overflowOrder = append(s.overflowOrder, channelID)
			continue
		}
		delete(s.overflow, channelID)
		s.signalOverflowSpaceLocked()
		return channelID, item.pts, true
	}
	return 0, 0, false
}

// tryQueueOverflow tries to non-blockingly submit an overflow watermark whose barrier is satisfied
// to the shared nudge queue. It is removed from the mailbox only when the channel send succeeds;
// when the queue is full it keeps the original item (including the highest pts after concurrent
// coalescing) and the original order position. The whole operation completes under shard.mu, so
// there is no "read old pts then delete new pts" race.
func (s *channelFanoutShard) tryQueueOverflow(offer func(channelFanoutNudge) bool) (queued, blocked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := 0; i < len(s.overflowOrder); {
		channelID := s.overflowOrder[i]
		item, exists := s.overflow[channelID]
		if !exists {
			copy(s.overflowOrder[i:], s.overflowOrder[i+1:])
			s.overflowOrder = s.overflowOrder[:len(s.overflowOrder)-1]
			continue
		}
		if item.barrier > s.processedSeq {
			i++
			continue
		}
		if offer(channelFanoutNudge{channelID: channelID, pts: item.pts}) {
			delete(s.overflow, channelID)
			copy(s.overflowOrder[i:], s.overflowOrder[i+1:])
			s.overflowOrder = s.overflowOrder[:len(s.overflowOrder)-1]
			s.signalOverflowSpaceLocked()
			return true, false
		} else {
			// Shared nudge workers are saturated.  Keep the exact watermark and retry from
			// the shard's bounded timer; do not remove or advance the mailbox entry, and
			// never park this payload worker on the nudge queue.
			return false, true
		}
	}
	return false, false
}

func (s *channelFanoutShard) signalOverflow() {
	select {
	case s.overflowWake <- struct{}{}:
	default:
	}
}

func (s *channelFanoutShard) signalEligibleOverflow() {
	s.mu.Lock()
	eligible := false
	for _, channelID := range s.overflowOrder {
		if item, ok := s.overflow[channelID]; ok && item.barrier <= s.processedSeq {
			eligible = true
			break
		}
	}
	s.mu.Unlock()
	if eligible {
		s.signalOverflow()
	}
}

// channelFanoutPrefetch is called once after the worker resolves the final recipient set and
// before the per-viewer build, to warm each viewer's user projection in one pass across all
// recipients (fan-out templating, O(owner)). A nil value means the payload has no user envelope
// to warm. It runs serially inside the worker goroutine and shares the same viewerPeerCache as
// build, with no cross-goroutine race. Returning false means batch warm-up failed; the worker must
// fail closed and must not silently fall back to per-viewer projection.
type channelFanoutPrefetch func(ctx context.Context, viewers []int64) bool

// channelFanoutDispatcher moves channel payload fan-out off the sender's RPC and processes it
// serially, sharded by channelID.
type channelFanoutDispatcher struct {
	r         *Router
	log       *zap.Logger
	shards    []*channelFanoutShard
	started   atomic.Bool
	stopped   atomic.Bool
	stopCh    chan struct{}
	stopOnce  sync.Once
	enqueueMu sync.RWMutex

	budgetMu       sync.Mutex
	queuedJobs     int
	queuedBytes    int64
	maxQueuedJobs  int
	maxQueuedBytes int64

	nudgeJobs    chan int64
	nudgeWorkers int
	nudgeTimeout time.Duration
	nudgeMu      sync.Mutex
	// nudgePending and nudgeJobs form one bounded coalescing mailbox. nudgeJobs contains only
	// channel ids; the mutable map value always holds the highest pts observed before a worker
	// takes that id. A hot channel therefore occupies one slot rather than filling the queue.
	nudgePending map[int64]int
	nudgeLimit   int

	// recoveryGeneration is the terminal in-memory saturation fallback. It deliberately carries
	// no channel id: the fixed recovery actor enumerates the online membership index and reloads
	// each channel's durable max pts. Thus even when every key-bearing mailbox is full, publishing
	// one constant-size generation cannot fail or block an RPC producer.
	recoveryGeneration atomic.Uint64
	recoveryCompleted  atomic.Uint64
	recoveryWake       chan struct{}
	// dropped keeps the old field name for compatibility with existing stats; it now counts the
	// number of times a real payload was coalesced into a nudge-only overflow watermark due to a
	// full queue, and no longer means the recovery trigger was silently dropped too.
	dropped atomic.Int64
}

type channelFanoutNudge struct {
	channelID int64
	pts       int
}

// enqueueChannelFanout submits one channel-payload-pts fan-out to the async dispatcher. It grabs
// the originating device's raw auth key + session_id from the request ctx and explicitly carries
// them into the job so the async worker can still exclude the originating device's echo (the
// request ctx is invalid by the time it runs async). Used only for real payloads that advance the
// client's channel PtsWaiter (new message/edit/delete/pin); reaction/poll (viewer-only, zero
// pts), participant/TTL/channel state (no channel pts), and typing (transient) do not take this
// path (design §2.1/§5 classification).
func (r *Router) enqueueChannelFanout(ctx context.Context, scope channelFanoutScope, originUserID, channelID int64, pts int, recipients []int64, build channelFanoutBuilder) {
	r.enqueueChannelFanoutWithPrefetch(ctx, scope, originUserID, channelID, pts, recipients, 0, nil, build)
}

// enqueueChannelFanoutWithPrefetch is like enqueueChannelFanout but with an extra cross-viewer
// user-projection warm-up hook (fan-out templating folds each recipient's per-viewer projection
// into one O(owner) projection; see prefetchChannelFanoutUsers).
func (r *Router) enqueueChannelFanoutWithPrefetch(ctx context.Context, scope channelFanoutScope, originUserID, channelID int64, pts int, recipients []int64, retainedFloor int64, prefetch channelFanoutPrefetch, build channelFanoutBuilder) {
	if r.channelFanout == nil || build == nil {
		return
	}
	originAuthKeyID := rawAuthKeyIDForOrigin(ctx)
	originSessionID, _ := SessionIDFrom(ctx)
	retainedBytes := int64(inboundRPCBytesFrom(ctx)) + int64(len(recipients))*8 + 4096
	if retainedFloor < channelFanoutMinRetainedBytes {
		retainedFloor = channelFanoutMinRetainedBytes
	}
	if retainedBytes < retainedFloor {
		retainedBytes = retainedFloor
	}
	r.channelFanout.Enqueue(ctx, channelFanoutJob{
		scope:           scope,
		originUserID:    originUserID,
		channelID:       channelID,
		pts:             pts,
		recipients:      recipients,
		originAuthKeyID: originAuthKeyID,
		originSessionID: originSessionID,
		prefetch:        prefetch,
		build:           build,
		retainedBytes:   retainedBytes,
	})
}

// RunChannelFanout starts the channel fan-out background workers, launched alongside the other
// dispatchers by main. Blocks until ctx is canceled; before it is called, fan-out runs
// synchronously (same behavior as the old version).
func (r *Router) RunChannelFanout(ctx context.Context) {
	r.channelFanout.Run(ctx)
}

func newChannelFanoutDispatcher(r *Router, shards, buffer int) *channelFanoutDispatcher {
	if shards <= 0 {
		shards = defaultChannelFanoutShards
	}
	if buffer <= 0 {
		buffer = defaultChannelFanoutBuffer
	}
	d := &channelFanoutDispatcher{
		r:              r,
		log:            r.log.Named("channel-fanout"),
		shards:         make([]*channelFanoutShard, shards),
		stopCh:         make(chan struct{}),
		maxQueuedJobs:  defaultChannelFanoutMaxQueuedJobs,
		maxQueuedBytes: defaultChannelFanoutMaxQueuedBytes,
		nudgeJobs:      make(chan int64, defaultChannelNudgeQueue),
		nudgeWorkers:   defaultChannelNudgeWorkers,
		nudgeTimeout:   defaultChannelFanoutNudgeDeadline,
		nudgePending:   make(map[int64]int),
		nudgeLimit:     defaultChannelNudgeQueue,
		recoveryWake:   make(chan struct{}, 1),
	}
	for i := range d.shards {
		d.shards[i] = newChannelFanoutShard(buffer)
	}
	return d
}

// Run starts the workers: one goroutine per shard, guaranteeing per-channel serialization. Blocks
// until ctx is canceled. Before Run is called, Enqueue falls back to synchronous execution
// (keeping tests/unwired setups unchanged).
func (d *channelFanoutDispatcher) Run(ctx context.Context) {
	if d == nil || !d.started.CompareAndSwap(false, true) {
		return
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		d.enqueueMu.Lock()
		d.stopped.Store(true)
		d.stopOnce.Do(func() { close(d.stopCh) })
		d.enqueueMu.Unlock()
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		d.runRecoverySweeps(ctx)
	}()
	for range d.nudgeWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case channelID := <-d.nudgeJobs:
					nudge, ok := d.takeNudge(channelID)
					if !ok {
						continue
					}
					timeout := d.nudgeTimeout
					if timeout <= 0 {
						timeout = defaultChannelFanoutNudgeDeadline
					}
					nudgeCtx, cancel := context.WithTimeout(ctx, timeout)
					complete := d.r.runChannelFanoutOverflowNudge(nudgeCtx, nudge.channelID, nudge.pts)
					cancel()
					if !complete && ctx.Err() == nil {
						// A deadline may leave only a prefix of online members nudged. Do not try to
						// remember that recipient subset: request a durable max-pts sweep instead.
						d.requestRecoverySweep("nudge deadline")
					}
				}
			}
		}()
	}
	for i := range d.shards {
		wg.Add(1)
		shard := d.shards[i]
		go func() {
			defer wg.Done()
			var retryTimer *time.Timer
			var retryC <-chan time.Time
			retryDelay := channelFanoutNudgeRetryMin
			stopRetryTimer := func() {
				if retryTimer != nil {
					retryTimer.Stop()
				}
			}
			defer stopRetryTimer()
			scheduleRetry := func() {
				if retryC != nil {
					return
				}
				if retryTimer == nil {
					retryTimer = time.NewTimer(retryDelay)
				} else {
					retryTimer.Reset(retryDelay)
				}
				retryC = retryTimer.C
				if retryDelay < channelFanoutNudgeRetryMax {
					retryDelay *= 2
					if retryDelay > channelFanoutNudgeRetryMax {
						retryDelay = channelFanoutNudgeRetryMax
					}
				}
			}
			drain := func() {
				// While a retry is armed, payload completions and coalescing wakeups must not
				// defeat backoff and spin on a full shared queue.
				if retryC != nil {
					return
				}
				queued, blocked := d.drainOneOverflow(shard)
				if queued {
					retryDelay = channelFanoutNudgeRetryMin
					shard.signalEligibleOverflow()
					return
				}
				if blocked {
					scheduleRetry()
				}
			}
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-shard.jobs:
					d.r.runChannelFanoutJob(ctx, job)
					d.releaseQueuedJob(job)
					shard.markProcessed(job.queueSeq)
					// After processing each normal FIFO payload, proactively try to drain one
					// overflow that has passed its barrier. This way a hot channel that keeps the
					// normal queue full cannot permanently starve the other channels' recovery nudge.
					drain()
				case <-shard.overflowWake:
					drain()
				case <-retryC:
					retryC = nil
					drain()
				}
			}
		}()
	}
	wg.Wait()
	// Workers may choose ctx.Done while jobs remain buffered.  Release every reservation and
	// drop closure references so tests/restarts do not retain the global budget after shutdown.
	for _, shard := range d.shards {
		for {
			select {
			case job := <-shard.jobs:
				d.releaseQueuedJob(job)
			default:
				goto drained
			}
		}
	drained:
		shard.mu.Lock()
		clear(shard.overflow)
		shard.overflowOrder = nil
		shard.mu.Unlock()
	}
	d.nudgeMu.Lock()
	clear(d.nudgePending)
	d.nudgeMu.Unlock()
}

func (d *channelFanoutDispatcher) shardIndex(channelID int64) int {
	n := int64(len(d.shards))
	idx := channelID % n
	if idx < 0 {
		idx += n
	}
	return int(idx)
}

// Enqueue submits a fan-out task. When the dispatcher is not started it runs synchronously (using
// the request ctx, keeping the old behavior); when started it goes into the corresponding shard.
// When full, a normal payload does not block the request path but is coalesced per channel into a
// nudge-only overflow watermark with the highest pts, drained fairly by the same shard worker
// after the earlier FIFO payloads. If the overflow cardinality is also full, it only publishes a
// constant-sized global recovery generation; a fixed background actor then rebuilds the nudges
// for all online channels from the durable channel pts. The RPC goroutine never waits for a slot.
func (d *channelFanoutDispatcher) Enqueue(reqCtx context.Context, job channelFanoutJob) {
	if d == nil || job.build == nil {
		return
	}
	if !d.started.Load() {
		d.r.runChannelFanoutJob(reqCtx, job)
		return
	}
	d.enqueueMu.RLock()
	if d.stopped.Load() {
		d.enqueueMu.RUnlock()
		return
	}
	shard := d.shards[d.shardIndex(job.channelID)]
	queued := false
	if d.reserveQueuedJob(job) {
		queued = shard.enqueue(job)
		if queued {
			d.enqueueMu.RUnlock()
			return
		}
		d.releaseQueuedJob(job)
	}
	d.dropped.Add(1)
	if job.scope != channelFanoutMembers && job.scope != channelFanoutMessageBox || job.pts <= 0 {
		// Only durable member/message-box payloads may be coalesced into a channel nudge;
		// viewer-only/no-pts updates have no difference recovery contract and must not be disguised
		// as a channel PTS watermark.
		d.log.Error("channel fanout queue full for non-coalescible job; overflow contract violated",
			zap.Int64("channel_id", job.channelID), zap.Int("pts", job.pts), zap.Int("scope", int(job.scope)))
		d.enqueueMu.RUnlock()
		return
	}
	channelID, pts := job.channelID, job.pts
	// The payload closure and recipient snapshot are no longer needed after normal queue
	// admission failed. Make them unreachable before applying overflow-cardinality backpressure;
	// otherwise blocked producers would retain unbudgeted request bodies while waiting for one of
	// the fixed mailbox slots. Inbound RPC concurrency remains the producer-count bound.
	job.recipients = nil
	job.prefetch = nil
	job.build = nil
	if !shard.enqueueOverflow(channelID, pts) {
		// Every key-bearing in-memory structure is bounded. Once the shard mailbox has no distinct
		// channel slot, do not add another queue and do not park this RPC worker. A generation bit is
		// enough because channels.pts/channel_update_events are already the durable truth: the fixed
		// recovery actor can enumerate all online channel ids and reconstruct the highest watermark.
		d.requestRecoverySweep("overflow cardinality full")
		d.log.Warn("channel fanout overflow cardinality exhausted; scheduled durable max-pts recovery sweep",
			zap.Int64("channel_id", channelID), zap.Int("pts", pts))
		d.enqueueMu.RUnlock()
		return
	}
	d.log.Warn("channel fanout capacity exhausted, coalesced realtime payload into highest-pts overflow nudge",
		zap.Int64("channel_id", channelID), zap.Int("pts", pts))
	d.enqueueMu.RUnlock()
}

func (d *channelFanoutDispatcher) reserveQueuedJob(job channelFanoutJob) bool {
	size := job.retainedBytes
	if size < channelFanoutMinRetainedBytes {
		size = channelFanoutMinRetainedBytes
	}
	d.budgetMu.Lock()
	defer d.budgetMu.Unlock()
	if d.queuedJobs >= d.maxQueuedJobs || size > d.maxQueuedBytes-d.queuedBytes {
		return false
	}
	d.queuedJobs++
	d.queuedBytes += size
	return true
}

func (d *channelFanoutDispatcher) releaseQueuedJob(job channelFanoutJob) {
	size := job.retainedBytes
	if size < channelFanoutMinRetainedBytes {
		size = channelFanoutMinRetainedBytes
	}
	d.budgetMu.Lock()
	d.queuedJobs--
	d.queuedBytes -= size
	if d.queuedJobs < 0 || d.queuedBytes < 0 {
		panic("channel fanout queue budget underflow")
	}
	d.budgetMu.Unlock()
}

func (d *channelFanoutDispatcher) queuedBudgetSnapshot() (jobs int, bytes int64) {
	d.budgetMu.Lock()
	defer d.budgetMu.Unlock()
	return d.queuedJobs, d.queuedBytes
}

func (d *channelFanoutDispatcher) drainOneOverflow(shard *channelFanoutShard) (queued, blocked bool) {
	return shard.tryQueueOverflow(d.offerNudge)
}

// offerNudge inserts one channel id into the bounded shared queue and stores its mutable highest
// pts in nudgePending. It never blocks. A same-channel update succeeds even when cardinality is
// full because it consumes no additional queue slot.
func (d *channelFanoutDispatcher) offerNudge(nudge channelFanoutNudge) bool {
	if nudge.channelID == 0 || nudge.pts <= 0 {
		return true
	}
	d.nudgeMu.Lock()
	if current, exists := d.nudgePending[nudge.channelID]; exists {
		if nudge.pts > current {
			d.nudgePending[nudge.channelID] = nudge.pts
		}
		d.nudgeMu.Unlock()
		return true
	}
	if len(d.nudgePending) >= d.nudgeLimit {
		d.nudgeMu.Unlock()
		return false
	}
	d.nudgePending[nudge.channelID] = nudge.pts
	select {
	case d.nudgeJobs <- nudge.channelID:
		d.nudgeMu.Unlock()
		return true
	default:
		// nudgeJobs has the same cardinality bound as nudgePending. This branch is reachable only
		// while a test overrides one without the other or an invariant regresses; roll back rather
		// than retain an unreachable map entry.
		delete(d.nudgePending, nudge.channelID)
		d.nudgeMu.Unlock()
		return false
	}
}

func (d *channelFanoutDispatcher) takeNudge(channelID int64) (channelFanoutNudge, bool) {
	d.nudgeMu.Lock()
	pts, ok := d.nudgePending[channelID]
	if ok {
		delete(d.nudgePending, channelID)
	}
	d.nudgeMu.Unlock()
	return channelFanoutNudge{channelID: channelID, pts: pts}, ok
}

func (d *channelFanoutDispatcher) requestRecoverySweep(reason string) {
	generation := d.recoveryGeneration.Add(1)
	select {
	case d.recoveryWake <- struct{}{}:
	default:
	}
	d.log.Debug("channel fanout durable recovery sweep requested",
		zap.Uint64("generation", generation), zap.String("reason", reason))
}

// runRecoverySweeps owns the only potentially waiting overflow admission path. Producers publish
// generations and return; this fixed actor reconstructs channel ids from the live membership index
// and watermarks from durable channels.pts. A generation is marked complete only after every page
// and every channel in that page has successfully entered its shard's barrier-preserving overflow
// mailbox. Errors retain the generation and retry with bounded backoff.
func (d *channelFanoutDispatcher) runRecoverySweeps(ctx context.Context) {
	completed := d.recoveryCompleted.Load()
	retryDelay := channelFanoutRecoveryRetryMin
	var retryTimer *time.Timer
	defer func() {
		if retryTimer != nil {
			retryTimer.Stop()
		}
	}()
	for {
		target := d.recoveryGeneration.Load()
		if target <= completed {
			select {
			case <-ctx.Done():
				return
			case <-d.recoveryWake:
				continue
			}
		}
		if err := d.sweepOnlineChannelRecovery(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			d.log.Warn("channel fanout durable recovery sweep failed; retaining generation",
				zap.Uint64("generation", target), zap.Duration("retry_in", retryDelay), zap.Error(err))
			if retryTimer == nil {
				retryTimer = time.NewTimer(retryDelay)
			} else {
				retryTimer.Reset(retryDelay)
			}
			// recoveryGeneration already records every concurrent request. A wake may shorten the
			// idle wait before a healthy sweep, but it must never bypass failure backoff: otherwise
			// sustained saturation plus a persistent DB error retries at producer rate.
			select {
			case <-ctx.Done():
				return
			case <-retryTimer.C:
			}
			if retryDelay < channelFanoutRecoveryRetryMax {
				retryDelay *= 2
				if retryDelay > channelFanoutRecoveryRetryMax {
					retryDelay = channelFanoutRecoveryRetryMax
				}
			}
			continue
		}
		completed = target
		d.recoveryCompleted.Store(completed)
		retryDelay = channelFanoutRecoveryRetryMin
		d.log.Info("channel fanout durable recovery sweep completed", zap.Uint64("generation", completed))
		// If a producer saturated after its channel had already been visited, generation is now
		// greater than completed and the next loop immediately performs a fresh full pass.
	}
}

func (d *channelFanoutDispatcher) sweepOnlineChannelRecovery(ctx context.Context) error {
	sessions, ok := d.r.deps.Sessions.(ChannelFanoutRecoverySessionProvider)
	if !ok {
		return fmt.Errorf("sessions dependency lacks online channel recovery enumeration")
	}
	channels, ok := d.r.deps.Channels.(ChannelFanoutRecoveryPtsProvider)
	if !ok {
		return fmt.Errorf("channels dependency lacks durable max pts lookup")
	}
	channelIDs := sessions.OnlineChannelIDsSnapshot()
	for i, channelID := range channelIDs {
		if channelID <= 0 || (i > 0 && channelID <= channelIDs[i-1]) {
			return fmt.Errorf("online channel recovery snapshot is not strictly ascending: index=%d got=%d", i, channelID)
		}
	}
	for start := 0; start < len(channelIDs); start += defaultChannelFanoutRecoverySweepPage {
		end := start + defaultChannelFanoutRecoverySweepPage
		if end > len(channelIDs) {
			end = len(channelIDs)
		}
		page := channelIDs[start:end]
		ptsByChannel, err := channels.MaxChannelPtsBatch(ctx, page)
		if err != nil {
			return fmt.Errorf("load durable max pts for online channel page [%d:%d]: %w", start, end, err)
		}
		for _, channelID := range page {
			pts := ptsByChannel[channelID]
			if pts > 0 {
				shard := d.shards[d.shardIndex(channelID)]
				if !shard.enqueueOverflowWait(ctx, channelID, pts, d.stopCh) {
					if err := ctx.Err(); err != nil {
						return err
					}
					return fmt.Errorf("dispatcher stopped while admitting recovery for channel %d", channelID)
				}
			}
		}
	}
	return nil
}

// runChannelFanoutOverflowNudge is the nudge-only downgrade path for a full queue. It cannot reuse
// the original job's origin exclude: the same channel watermark may coalesce several different
// originating sessions; sending the highest-pts nudge to all online members is conservative and
// idempotent, and a TDesktop already caught up to that pts ignores it.
func (r *Router) runChannelFanoutOverflowNudge(ctx context.Context, channelID int64, pts int) bool {
	if r.deps.Sessions == nil || channelID == 0 || pts <= 0 {
		return true
	}
	return r.nudgeBeyondCapChannelMessageAudience(ctx, channelID, pts, nil)
}

// runChannelFanoutJob executes one fan-out: equivalent to the synchronous
// pushChannelUpdatesWithScope, except build accepts a ctx and excludes the originating device by
// overlaying the job's explicitly carried (originAuthKeyID, originSessionID) onto the ctx and
// reusing pushUserUpdates, rather than relying on the invalidated request ctx.
func (r *Router) runChannelFanoutJob(ctx context.Context, job channelFanoutJob) {
	if r.deps.Sessions == nil || job.build == nil {
		return
	}
	pushCtx := WithSessionID(WithRawAuthKeyID(ctx, job.originAuthKeyID), job.originSessionID)
	recipients := r.channelFanoutRecipients(ctx, job.scope, job.channelID, job.recipients)
	// Warm the cross-viewer user projection (fan-out templating): compute each recipient's
	// projection once before the per-viewer build and warm the shared cache, so build only hits the
	// cache and no longer does an O(viewer) ForViewer per recipient. This covers recipients + the
	// fallback origin (when there is no online recipient, build falls back to origin). On failure,
	// constructing the real payload is forbidden; send a viewer-independent too-long nudge instead,
	// letting the client recover from the durable difference.
	if job.prefetch != nil {
		viewers := recipients
		if job.originUserID != 0 {
			viewers = append(append(make([]int64, 0, len(recipients)+1), recipients...), job.originUserID)
		}
		if !job.prefetch(ctx, viewers) {
			r.log.Warn("channel fanout prefetch failed; replacing online payload with recovery nudge",
				zap.Int64("channel_id", job.channelID),
				zap.Int("pts", job.pts),
				zap.Int("viewers", len(viewers)),
			)
			r.recoverFailedChannelFanoutPrefetch(pushCtx, job, recipients)
			return
		}
	}
	seen := make(map[int64]struct{}, len(recipients))
	pushed := false
	for _, userID := range recipients {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		updates := job.build(ctx, userID)
		if updates == nil {
			continue
		}
		r.pushUserUpdates(pushCtx, userID, updates)
		pushed = true
	}
	if !pushed && job.originUserID != 0 {
		seen[job.originUserID] = struct{}{}
		if updates := job.build(ctx, job.originUserID); updates != nil {
			r.pushUserUpdates(pushCtx, job.originUserID, updates)
		}
	}
	// P0-8: the full payload is capped by MaxChannelRealtimeFanout, and online members beyond the
	// cap receive neither the payload nor anything at all (the pure-pull channel model does not
	// poll spontaneously). Send these members, who are online but did not receive the full payload,
	// a cheap UpdateChannelTooLong{pts} nudge to prompt getChannelDifference convergence. This is
	// done only for real payloads that advance the client's channel PtsWaiter (members scope +
	// carrying channel pts).
	if job.scope == channelFanoutMembers && job.pts > 0 {
		r.nudgeBeyondCapChannelMembers(pushCtx, job.channelID, job.pts, seen)
	} else if job.scope == channelFanoutMessageBox && job.pts > 0 {
		r.nudgeBeyondCapChannelMessageAudience(pushCtx, job.channelID, job.pts, seen)
	}
}

func (r *Router) recoverFailedChannelFanoutPrefetch(ctx context.Context, job channelFanoutJob, recipients []int64) {
	if job.channelID == 0 || job.pts <= 0 {
		return
	}
	targets := append([]int64(nil), recipients...)
	if job.originUserID != 0 {
		targets = append(targets, job.originUserID)
	}
	targets = uniquePeerIDs(targets)
	delivered := make(map[int64]struct{}, len(targets))
	date := int(r.clock.Now().Unix())
	tooLong := &tg.UpdateChannelTooLong{ChannelID: job.channelID}
	tooLong.SetPts(job.pts)
	updates := &tg.Updates{
		Updates: []tg.UpdateClass{tooLong},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
	}
	for _, userID := range targets {
		if userID == 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
		r.pushUserUpdates(ctx, userID, updates)
		delivered[userID] = struct{}{}
	}
	// Explicit monoforum/suggested-post recipients are the full authorized
	// audience. Member/message-box scopes may have additional online viewers
	// beyond the full-payload cap, so nudge that recovery audience too.
	switch job.scope {
	case channelFanoutMembers:
		r.nudgeBeyondCapChannelMembers(ctx, job.channelID, job.pts, delivered)
	case channelFanoutMessageBox:
		r.nudgeBeyondCapChannelMessageAudience(ctx, job.channelID, job.pts, delivered)
	}
}

// prefetchChannelFanoutUsers projects the owner users in one pass across all recipients (fan-out
// templating, O(owner)), warming the results per viewer into the shared cache; afterwards each
// viewer's build only hits the cache and no longer does a per-viewer ForViewer. ownerIDs are
// collected by the caller from the message/event peer refs. deps.Users must implement
// BatchViewerUsersResolver; on a missing capability, resolution failure, or incomplete envelope it
// returns false, and the worker fails closed, with no per-viewer fallback permitted.
func (r *Router) prefetchChannelFanoutUsers(ctx context.Context, cache *viewerPeerCache, viewers, ownerIDs []int64) bool {
	viewers = uniquePeerIDs(viewers)
	ownerIDs = uniquePeerIDs(ownerIDs)
	if len(viewers) == 0 || len(ownerIDs) == 0 {
		return true
	}
	if cache == nil || r.deps.Users == nil {
		return false
	}
	resolver, ok := r.deps.Users.(BatchViewerUsersResolver)
	if !ok {
		return false
	}
	byViewer, err := resolver.ByIDsForViewers(ctx, viewers, ownerIDs)
	if err != nil {
		r.log.Warn("channel fanout user prefetch failed",
			zap.Int("viewers", len(viewers)), zap.Int("owners", len(ownerIDs)), zap.Error(err))
		return false
	}
	for _, viewer := range viewers {
		if missingID, missing := missingProjectedUserID(ownerIDs, byViewer[viewer]); missing {
			r.log.Warn("channel fanout user prefetch returned an incomplete envelope",
				zap.Int64("viewer_user_id", viewer),
				zap.Int64("missing_user_id", missingID),
				zap.Int("owners", len(ownerIDs)))
			return false
		}
		cache.primeExpectedUsers(viewer, ownerIDs, byViewer[viewer])
	}
	return true
}

// channelMessageFanoutOwnerIDs collects all owner user ids that a channel message fan-out will put
// into the Users array (sender/from/send_as/forward/via_bot/reply/contact/poll and other peer
// refs, matching the collection scope of channelMessagesUpdatesWithPeerCache), for warming the
// cross-viewer projection.
func channelMessageFanoutOwnerIDs(res domain.SendChannelMessageResult, extraUserIDs []int64) []int64 {
	return channelMessagesFanoutOwnerIDs([]domain.SendChannelMessageResult{res}, extraUserIDs)
}

// channelMessagesFanoutOwnerIDs is the same, but takes the union of owner ids across several
// results (a batch forward merged into one job).
func channelMessagesFanoutOwnerIDs(results []domain.SendChannelMessageResult, extraUserIDs []int64) []int64 {
	userIDs, _ := channelMessagesFanoutPeerRefs(results, extraUserIDs)
	return peerIDMapKeys(userIDs)
}

func channelMessagesFanoutUsernamePeers(results []domain.SendChannelMessageResult, extraUserIDs []int64) []domain.Peer {
	userIDs, channelIDs := channelMessagesFanoutPeerRefs(results, extraUserIDs)
	peers := make([]domain.Peer, 0, len(userIDs)+len(channelIDs))
	for userID := range userIDs {
		peers = append(peers, domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	}
	for channelID := range channelIDs {
		peers = append(peers, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	}
	return peers
}

func channelMessagesFanoutPeerRefs(results []domain.SendChannelMessageResult, extraUserIDs []int64) (map[int64]struct{}, map[int64]struct{}) {
	userIDs := make(map[int64]struct{}, len(results)+len(extraUserIDs)+4)
	channelIDs := make(map[int64]struct{})
	for _, id := range extraUserIDs {
		if id != 0 {
			userIDs[id] = struct{}{}
		}
	}
	for _, res := range results {
		collectChannelUpdatePeerRefs(res.Event, res.Channel.ID, userIDs, channelIDs)
		collectChannelMessagePeerRefs(res.Message, res.Channel.ID, userIDs, channelIDs)
	}
	return userIDs, channelIDs
}

// enqueueChannelMessageFanout asynchronously fan-outs a single channel message and warms the
// cross-viewer projection (the common form of a "a new message appeared in the channel" event:
// send/forward one/discussion-group linkage/forum topic message). Semantics match
// enqueueChannelFanout, with the only addition being computing each viewer's projection once and
// warming it into the shared cache (O(owner)); delivery/exclusion/nudge behavior is unchanged.
func (r *Router) enqueueChannelMessageFanout(ctx context.Context, originUserID int64, res domain.SendChannelMessageResult, extraUserIDs []int64) {
	r.enqueueBotAPIChannelMessageUpdate(ctx, originUserID, res)
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelMessageFanoutOwnerIDs(res, extraUserIDs)
	usernamePeers := channelMessagesFanoutUsernamePeers([]domain.SendChannelMessageResult{res}, extraUserIDs)
	var usernames map[domain.Peer][]domain.Username
	skip := skipDeliverySet(res.SkipDeliveryUserIDs)
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMessageBox, originUserID, res.Channel.ID, res.Event.Pts, res.Recipients,
		0,
		func(bgCtx context.Context, viewers []int64) bool {
			if !r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs) {
				return false
			}
			usernames = r.usernameRegistryMap(bgCtx, usernamePeers)
			return true
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			// A privacy bot is excluded by SkipDeliveryUserIDs at send time (messages other than
			// commands/@/replies are invisible to it). channelFanoutRecipients recomputing
			// recipients by "online active members" adds it back, so online fanout must skip its
			// direct push here, otherwise the online bot still receives all group messages in
			// realtime (persistent history/difference already hides them correctly; only this direct
			// push leaks content). The nudge is safe: the bot lands in seen and is not nudged, and
			// even a nudge would have hidden messages filtered out by filterBotChannelDifference.
			if _, skipped := skip[viewerUserID]; skipped {
				return nil
			}
			return r.channelMessageUpdatesWithPeerCacheAndUsernames(bgCtx, viewerUserID, res, 0, fanoutCache, usernames)
		})
}

// enqueueMonoforumMessageFanout only targets the subscriber sub-dialog and active parent-channel
// admins. A monoforum has no ordinary members, so member recomputation would either drop the
// message or leak it to an invalid historical membership.
func (r *Router) enqueueMonoforumMessageFanout(ctx context.Context, originUserID int64, mono domain.Channel, savedPeer domain.Peer, res domain.SendChannelMessageResult) {
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelMessageFanoutOwnerIDs(res, []int64{savedPeer.ID})
	projectionPeers := monoforumProjectionPeers(mono.ID, mono.LinkedMonoforumID, ownerIDs)
	var overlays *monoforumPeerOverlays
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutExplicit, originUserID, mono.ID, res.Event.Pts, res.Recipients,
		0,
		func(bgCtx context.Context, viewers []int64) bool {
			if !r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs) {
				return false
			}
			overlays = r.loadMonoforumPeerOverlays(bgCtx, projectionPeers)
			return true
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.monoforumDeliveryUpdatesWithPeerCacheAndOverlays(bgCtx, viewerUserID, mono, savedPeer, res, fanoutCache, overlays)
		})
}

// skipDeliverySet converts the SkipDeliveryUserIDs slice into a lookup set (nil means no exclusion).
func skipDeliverySet(ids []int64) map[int64]struct{} {
	if len(ids) == 0 {
		return nil
	}
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id != 0 {
			set[id] = struct{}{}
		}
	}
	return set
}

// channelEditMessageFanoutOwnerIDs collects all owner user ids that a channel edit fan-out will put
// into the Users array. It strictly mirrors channelEditMessageUpdates' two-container + pts-gated
// collection scope (Event/Message only when Event.Pts!=0, ServiceEvent/ServiceMessage only when
// ServiceEvent.Pts!=0, corresponding to the second container of the todo-edit service message), so
// the warmed owner set exactly matches the Users set build actually sends -- collecting extra
// would only harmlessly over-warm, but mirroring the gate keeps the equivalence test tightest.
func channelEditMessageFanoutOwnerIDs(res domain.EditChannelMessageResult) []int64 {
	userIDs, _ := channelEditMessageFanoutPeerRefs(res)
	return peerIDMapKeys(userIDs)
}

func channelEditMessageFanoutUsernamePeers(res domain.EditChannelMessageResult) []domain.Peer {
	userIDs, channelIDs := channelEditMessageFanoutPeerRefs(res)
	peers := make([]domain.Peer, 0, len(userIDs)+len(channelIDs))
	for userID := range userIDs {
		peers = append(peers, domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	}
	for channelID := range channelIDs {
		peers = append(peers, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	}
	return peers
}

func channelEditMessageFanoutPeerRefs(res domain.EditChannelMessageResult) (map[int64]struct{}, map[int64]struct{}) {
	userIDs := make(map[int64]struct{}, 4)
	channelIDs := make(map[int64]struct{})
	if res.Event.Pts != 0 {
		collectChannelUpdatePeerRefs(res.Event, res.Channel.ID, userIDs, channelIDs)
		collectChannelMessagePeerRefs(res.Message, res.Channel.ID, userIDs, channelIDs)
	}
	if res.ServiceEvent.Pts != 0 {
		collectChannelUpdatePeerRefs(res.ServiceEvent, res.Channel.ID, userIDs, channelIDs)
		collectChannelMessagePeerRefs(res.ServiceMessage, res.Channel.ID, userIDs, channelIDs)
	}
	return userIDs, channelIDs
}

// enqueueChannelEditMessageFanout asynchronously fan-outs a channel edit and warms the
// cross-viewer projection (the common form of editMessage/geolive/todo/bot-inline edit). Semantics
// match the original enqueueChannelFanout(channelEditMessageUpdates), with the only addition being
// computing each viewer's projection once and warming it into the shared cache (O(owner)). Note
// that an edit does no per-viewer mention overlay (EditChannelMessageResult carries no
// MentionUserIDs, and a newly added @ in an edit self-heals via durable channel_unread_mentions
// through getChannelDifference), so each viewer's mentioned/media_unread bytes are identical and
// warming does not affect equivalence.
//
// The nudge pts takes the larger of the two containers: an edit can produce two pts-bearing events
// (Event = the edit itself, ServiceEvent = e.g. the "X completed Y" service message for a todo
// completion), and ServiceEvent.Pts is allocated later and thus always larger; some edits produce
// only a ServiceEvent (Event.Pts==0). The nudge must carry the channel's current highest pts for
// an over-cap online member's getChannelDifference to catch up to the end -- using Event.Pts would
// omit the nudge when Event.Pts==0 or report a pts lower than the real one. max() is correct in
// all three forms (Event only/ServiceEvent only/both).
func (r *Router) enqueueChannelEditMessageFanout(ctx context.Context, originUserID int64, res domain.EditChannelMessageResult) {
	r.enqueueBotAPIChannelEditMessageUpdate(ctx, originUserID, res)
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelEditMessageFanoutOwnerIDs(res)
	usernamePeers := channelEditMessageFanoutUsernamePeers(res)
	var usernames map[domain.Peer][]domain.Username
	nudgePts := max(res.Event.Pts, res.ServiceEvent.Pts)
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMessageBox, originUserID, res.Channel.ID, nudgePts, res.Recipients,
		0,
		func(bgCtx context.Context, viewers []int64) bool {
			if !r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs) {
				return false
			}
			usernames = r.usernameRegistryMap(bgCtx, usernamePeers)
			return true
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.channelEditMessageUpdatesWithPeerCacheAndUsernames(bgCtx, viewerUserID, res, fanoutCache, usernames)
		})
}

// enqueueChannelMessagesFanout is like enqueueChannelMessageFanout but merges several results into
// one job (batch forward: one Updates containing several UpdateNewChannelMessage); peer refs take
// the union across all results for warming. channelID/pts/recipients are given by the caller per
// batch semantics (pts is the last one; recipients are affected by the large-group truncation
// scope).
func (r *Router) enqueueChannelMessagesFanout(ctx context.Context, originUserID, channelID int64, pts int, recipients []int64, results []domain.SendChannelMessageResult, extraUserIDs []int64) {
	r.enqueueBotAPIChannelMessagesUpdate(ctx, originUserID, results)
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelMessagesFanoutOwnerIDs(results, extraUserIDs)
	usernamePeers := channelMessagesFanoutUsernamePeers(results, extraUserIDs)
	var usernames map[domain.Peer][]domain.Username
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMessageBox, originUserID, channelID, pts, recipients,
		int64(len(results))*(64<<10),
		func(bgCtx context.Context, viewers []int64) bool {
			if !r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs) {
				return false
			}
			usernames = r.usernameRegistryMap(bgCtx, usernamePeers)
			return true
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.channelMessagesUpdatesWithPeerCacheAndUsernames(bgCtx, viewerUserID, results, nil, false, extraUserIDs, fanoutCache, usernames)
		})
}

// defaultChannelNudgeMaxTargets caps the nudges per fan-out: a nudge is a cheap O(1)/person push,
// but once consumed the client calls getChannelDifference (for an unloaded channel DrKLO first
// calls getPeerDialogs, design §10.3), which at high frequency in a large group can amplify. It
// can be overridden via Config.ChannelNudgeMaxTargets; the client side is covered by the
// difference/getPeerDialogs FLOOD_WAIT (Phase 2) (see checkCatchupRateLimit).
const defaultChannelNudgeMaxTargets = 50000

// channelNudgeMaxTargets returns the effective nudge cap (a Config override, otherwise the default).
func (r *Router) channelNudgeMaxTargets() int {
	if r.cfg.ChannelNudgeMaxTargets > 0 {
		return r.cfg.ChannelNudgeMaxTargets
	}
	return defaultChannelNudgeMaxTargets
}

// nudgeBeyondCapChannelMembers sends UpdateChannelTooLong{pts} to channel online members that did
// not receive the full payload (not in delivered). The nudge must carry pts (flags&1) -- DrKLO
// does not trigger getChannelDifference for a tooLong without pts (design §10.3). It goes through
// pushUserUpdates (best-effort, pends when not ready, non-transient), satisfying the nudge
// delivery reliability requirement of design decision 4. When SessionManager does not implement
// ChannelNudgeProvider (tests/unwired), it is silently skipped and does not affect full payload
// delivery.
func (r *Router) nudgeBeyondCapChannelMembers(ctx context.Context, channelID int64, pts int, delivered map[int64]struct{}) bool {
	provider, ok := r.deps.Sessions.(ChannelNudgeProvider)
	if !ok || channelID == 0 || pts <= 0 {
		return true
	}
	targets := provider.OnlineChannelMemberUserIDsExcluding(channelID, delivered, r.channelNudgeMaxTargets())
	if len(targets) == 0 {
		return true
	}
	date := int(r.clock.Now().Unix())
	tooLong := &tg.UpdateChannelTooLong{ChannelID: channelID}
	tooLong.SetPts(pts)
	updates := &tg.Updates{
		Updates: []tg.UpdateClass{tooLong},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
	for _, userID := range targets {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		if userID == 0 {
			continue
		}
		// The nudge is viewer-independent and immutable. Reuse the TL object across
		// recipients; SessionManager encodes before enqueue and never mutates it.
		r.pushUserUpdates(ctx, userID, updates)
	}
	return ctx.Err() == nil
}

// nudgeBeyondCapChannelMessageAudience extends member recovery to users with an
// unexpired public-channel short-poll subscription. Subscribers are selected
// first (normally a tiny set), then the remaining bounded capacity is filled
// with joined members. One authoritative batched audience check prevents stale
// runtime indexes from leaking even the channel id/pts to revoked viewers.
func (r *Router) nudgeBeyondCapChannelMessageAudience(ctx context.Context, channelID int64, pts int, delivered map[int64]struct{}) bool {
	if r.deps.Sessions == nil || channelID == 0 || pts <= 0 {
		return true
	}
	if r.deps.Channels == nil {
		return r.nudgeBeyondCapChannelMembers(ctx, channelID, pts, delivered)
	}
	audience, ok := r.deps.Channels.(ChannelMessageAudienceService)
	if !ok {
		return r.nudgeBeyondCapChannelMembers(ctx, channelID, pts, delivered)
	}
	limit := r.channelNudgeMaxTargets()
	excluded := make(map[int64]struct{}, len(delivered)+16)
	for userID := range delivered {
		excluded[userID] = struct{}{}
	}
	candidates := make([]int64, 0, min(limit, 64))
	if subscriptions, ok := r.deps.Sessions.(ChannelSubscriptionProvider); ok {
		for _, userID := range subscriptions.OnlineChannelSubscriberUserIDsExcluding(channelID, excluded, limit) {
			if userID == 0 {
				continue
			}
			excluded[userID] = struct{}{}
			candidates = append(candidates, userID)
			if len(candidates) >= limit {
				break
			}
		}
	}
	if len(candidates) < limit {
		if members, ok := r.deps.Sessions.(ChannelNudgeProvider); ok {
			for _, userID := range members.OnlineChannelMemberUserIDsExcluding(channelID, excluded, limit-len(candidates)) {
				if userID == 0 {
					continue
				}
				excluded[userID] = struct{}{}
				candidates = append(candidates, userID)
			}
		}
	}
	if len(candidates) == 0 {
		return true
	}
	targets, err := audience.FilterMessageAudienceIDs(ctx, channelID, candidates)
	if err != nil {
		r.log.Warn("channel message audience nudge authorization failed",
			zap.Int64("channel_id", channelID), zap.Int("pts", pts), zap.Error(err))
		return false
	}
	if len(targets) == 0 {
		return true
	}
	date := int(r.clock.Now().Unix())
	tooLong := &tg.UpdateChannelTooLong{ChannelID: channelID}
	tooLong.SetPts(pts)
	updates := &tg.Updates{
		Updates: []tg.UpdateClass{tooLong},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
	for _, userID := range targets {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		r.pushUserUpdates(ctx, userID, updates)
	}
	return true
}
