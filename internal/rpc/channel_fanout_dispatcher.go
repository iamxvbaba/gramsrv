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

// channel fan-out 异步化（设计 docs/channel-fanout-async-design.md Phase 0 / §9 v1 最小实现）。
//
// 目标：把频道 payload fan-out 移出发送者 RPC 同步路径——发送者只等事务 + 自己视角 echo
// （rpc_result），其余在线成员的投递交给本 dispatcher 的后台 worker。
//
// v1（单实例）关键取舍：
//   - durable 真值仍是 channel_update_events；本 dispatcher 只做 best-effort 在线加速，
//     丢失由客户端 getChannelDifference 兜底（设计决策 1）。
//   - 按 channelID 哈希到固定分片，使同一 channel 串行处理（FIFO → 单调 pts），满足
//     DrKLO Android ~1.5s 乱序窗口与 TDesktop PtsWaiter 的连续性期望（设计 §10.2）。
//   - 单实例 + 无 durable 重投队列 + 同 channel 串行 → 无乱序、无自重复，故 v1 不需要
//     per-session at-most-once 双水位（那是 Phase 3 跨实例的事，设计 §9/§10.1）。
//   - 有界队列满时不静默丢弃恢复触发：真实 payload job 降级为按 channel 合并、只保留
//     最高 pts 的 UpdateChannelTooLong nudge。每个 shard 独立公平 drain；即使该频道随后
//     静默，也不依赖“下一条消息”才能触发 getChannelDifference（设计约束 B）。

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

// channelFanoutBuilder 按 viewer 构建该 viewer 视角的 channel updates。与同步
// channelUpdatesBuilder 不同，它接受 worker 提供的后台 ctx（请求 ctx 在 fan-out 异步
// 执行时已被取消），且实现不得从 ctx 读取 viewer/auth 派生数据（viewerUserID 显式传入）。
type channelFanoutBuilder func(ctx context.Context, viewerUserID int64) *tg.Updates

// channelFanoutJob 是一条频道 payload fan-out 任务。Pts 仅用于日志/折叠语义；真值仍是
// channel_update_events，worker 只做在线投递。originAuthKeyID 是物理 raw auth key
// （与 SessionManager.shouldExcludeSession 的比较侧一致），用于显式排除发起设备——异步
// 执行时请求 ctx 已失效，不能再靠 ctx 派生排除。
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
	// queueSeq 是 dispatcher shard 内部的 FIFO 序号。仅成功进入正常 payload queue 的
	// job 占用序号；overflow watermark 记录入队失败时已经接受的最大序号，等这些更早
	// payload 处理完后才发 nudge，避免 nudge 越过其之前的正常 FIFO payload。
	queueSeq uint64
}

// channelFanoutOverflow 是 queue full 时的 nudge-only 恢复水位。同一 channel 只保留
// 最大 pts；barrier 是该次 overflow 之前已经进入正常 FIFO 的最后一个 shard 序号。
type channelFanoutOverflow struct {
	pts     int
	barrier uint64
}

// channelFanoutShard 把正常 payload FIFO 与 overflow nudge mailbox 放在同一个 worker
// 下。overflowOrder 每个 channel 最多出现一次；热点 channel 只更新 map 水位，不会占满
// order，从而不能把其它 channel 的唯一恢复 nudge 永久饿死。
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

// enqueue 尝试把真实 payload 放入正常 FIFO；满时按 channel 合并最高 pts 的 nudge-only
// watermark。返回 true 表示正常入队，false 表示已经安全降级为 overflow watermark。
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

// popOverflow 仅供 mailbox/cardinality 单元测试直接释放一个 overflow；生产 drain 必须走
// tryQueueOverflow，确保 nudgeJobs 真正接收成功前不删除水位。
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

// tryQueueOverflow 尝试把一个 barrier 已满足的 overflow 水位非阻塞提交给共享 nudge queue。
// 只有 channel send 成功才从 mailbox 删除；queue 满时保留原 item（包含并发合并后的最高 pts）
// 和原 order 位置。整个操作在 shard.mu 下完成，因此不会出现“读到旧 pts 后删除新 pts”的竞态。
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

// channelFanoutPrefetch 在 worker 解析出最终 recipient 集合后、逐 viewer build 之前调用一次，
// 用于跨全部 recipient 一次性预热每 viewer 的用户投影（fan-out 模板化，O(owner)）。可选：为 nil
// 时 build 仍逐 viewer 解析（行为不变）。在 worker goroutine 内串行执行，与 build 共享同一
// viewerPeerCache，无跨 goroutine 竞态。
type channelFanoutPrefetch func(ctx context.Context, viewers []int64)

// channelFanoutDispatcher 把频道 payload fan-out 移出发送者 RPC，按 channelID 分片串行处理。
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
	// dropped 保留旧字段名供既有统计兼容；现在表示“真实 payload 因 queue full 被折叠为
	// nudge-only overflow watermark”的次数，不再表示恢复触发也被静默丢弃。
	dropped atomic.Int64
}

type channelFanoutNudge struct {
	channelID int64
	pts       int
}

// enqueueChannelFanout 把一条 channel-payload-pts 的 fan-out 投入异步 dispatcher。
// 从请求 ctx 抓取发起设备的 raw auth key + session_id 显式带入 job，使异步 worker 仍能
// 排除发起设备回显（请求 ctx 异步时已失效）。仅用于会推进客户端 channel PtsWaiter 的真实
// payload（新消息/编辑/删除/pin）；reaction/poll（viewer-only 零 pts）、participant/TTL/
// channel state（无 channel pts）、typing（transient）不走此路径（设计 §2.1/§5 分类）。
func (r *Router) enqueueChannelFanout(ctx context.Context, scope channelFanoutScope, originUserID, channelID int64, pts int, recipients []int64, build channelFanoutBuilder) {
	r.enqueueChannelFanoutWithPrefetch(ctx, scope, originUserID, channelID, pts, recipients, 0, nil, build)
}

// enqueueChannelFanoutWithPrefetch 同 enqueueChannelFanout，但额外带一个跨 viewer 用户投影预热钩子
// （fan-out 模板化把每 recipient 的逐 viewer 投影折叠成一次 O(owner) 投影；见 prefetchChannelFanoutUsers）。
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

// RunChannelFanout 启动频道 fan-out 后台 worker，由 main 与其它 dispatcher 一同 go 起。
// 阻塞到 ctx 取消；未调用前 fan-out 同步执行（行为同旧版）。
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

// Run 启动 worker：每分片一个 goroutine，保证同 channel 串行。阻塞到 ctx 取消。
// 未调用 Run 前 Enqueue 回退为同步执行（保持测试/未装配场景行为不变）。
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
					// 每处理一条正常 FIFO payload，主动尝试 drain 一条已经越过
					// barrier 的 overflow。这样持续灌满正常队列的热点频道也不能
					// 永久饿死其它频道的恢复 nudge。
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

// Enqueue 投递一条 fan-out 任务。dispatcher 未启动时同步执行（用请求 ctx，保持旧行为）；
// 已启动时投入对应分片。满时正常 payload 不阻塞请求路径，而是按 channel 合并为最高 pts
// 的 nudge-only overflow watermark，由同 shard worker 在更早的 FIFO payload 后公平 drain。
// 若 overflow cardinality 也已满，只发布一个常量大小的全局 recovery generation；固定后台
// actor 随后从 durable channel pts 重建全部在线 channel 的 nudge。RPC goroutine 永不等待 slot。
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
	if job.scope != channelFanoutMembers || job.pts <= 0 {
		// 当前所有 enqueue 入口均为 members + durable pts；若未来新增其它 scope，必须先
		// 定义其 overflow 恢复面，不能误把 viewer-only/no-pts 更新伪装成 channel nudge。
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

// runChannelFanoutOverflowNudge 是 queue-full 的 nudge-only 降级路径。不能复用原 job 的
// origin exclude：同一 channel 水位可能合并多个不同发起 session；向全部在线成员发最高 pts
// nudge 是保守且幂等的，已追上 pts 的 TDesktop 会直接忽略。
func (r *Router) runChannelFanoutOverflowNudge(ctx context.Context, channelID int64, pts int) bool {
	if r.deps.Sessions == nil || channelID == 0 || pts <= 0 {
		return true
	}
	return r.nudgeBeyondCapChannelMembers(ctx, channelID, pts, nil)
}

// runChannelFanoutJob 执行一条 fan-out：与同步 pushChannelUpdatesWithScope 等价，区别是
// build 接受 ctx、且排除发起设备用 job 显式携带的 (originAuthKeyID, originSessionID)
// 叠加到 ctx 后复用 pushUserUpdates，而非依赖已失效的请求 ctx。
func (r *Router) runChannelFanoutJob(ctx context.Context, job channelFanoutJob) {
	if r.deps.Sessions == nil || job.build == nil {
		return
	}
	pushCtx := WithSessionID(WithRawAuthKeyID(ctx, job.originAuthKeyID), job.originSessionID)
	recipients := r.channelFanoutRecipients(ctx, job.scope, job.channelID, job.recipients)
	// 预热跨 viewer 用户投影（fan-out 模板化）：在逐 viewer build 之前一次性算好每 recipient 的
	// 投影并预热共享 cache，使 build 只命中缓存、不再 O(viewer) 逐个 ForViewer。覆盖 recipients +
	// 兜底 origin（无在线 recipient 时 build 会回退给 origin）。失败/未实现时静默退化为逐 viewer。
	if job.prefetch != nil {
		viewers := recipients
		if job.originUserID != 0 {
			viewers = append(append(make([]int64, 0, len(recipients)+1), recipients...), job.originUserID)
		}
		job.prefetch(ctx, viewers)
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
	// P0-8：完整 payload 受 MaxChannelRealtimeFanout 封顶，超出 cap 的在线成员既收不到
	// payload 也收不到任何东西（频道纯拉模型不会自发轮询）。给这些「已在线但未收完整 payload」
	// 的成员发廉价 UpdateChannelTooLong{pts} nudge，促其 getChannelDifference 收敛。
	// 仅对会推进客户端 channel PtsWaiter 的真实 payload（members scope + 带 channel pts）做。
	if job.scope == channelFanoutMembers && job.pts > 0 {
		r.nudgeBeyondCapChannelMembers(pushCtx, job.channelID, job.pts, seen)
	}
}

// prefetchChannelFanoutUsers 跨全部 recipient 一次性投影 owner 用户（fan-out 模板化，O(owner)），
// 把结果按 viewer 预热进共享 cache；之后每 viewer 的 build 只命中缓存，不再逐 viewer ForViewer。
// ownerIDs 由调用方从消息/事件 peer refs 收集。deps.Users 未实现 BatchViewerUsersResolver 或解析
// 失败时静默跳过——build 回退逐 viewer 解析，行为不变，仅退化为旧的 O(viewer) 成本。
func (r *Router) prefetchChannelFanoutUsers(ctx context.Context, cache *viewerPeerCache, viewers, ownerIDs []int64) {
	if cache == nil || len(viewers) == 0 || len(ownerIDs) == 0 || r.deps.Users == nil {
		return
	}
	resolver, ok := r.deps.Users.(BatchViewerUsersResolver)
	if !ok {
		return
	}
	byViewer, err := resolver.ByIDsForViewers(ctx, viewers, ownerIDs)
	if err != nil {
		r.log.Warn("channel fanout user prefetch failed; falling back to per-viewer projection",
			zap.Int("viewers", len(viewers)), zap.Int("owners", len(ownerIDs)), zap.Error(err))
		return
	}
	for viewer, users := range byViewer {
		cache.primeUsers(viewer, users)
	}
}

// channelMessageFanoutOwnerIDs 收集一条频道消息 fan-out 会下发到 Users 数组里的全部 owner 用户 id
// （sender/from/send_as/forward/via_bot/reply/contact/poll 等 peer refs，与
// channelMessagesUpdatesWithPeerCache 的收集口径一致），用于预热跨 viewer 投影。
func channelMessageFanoutOwnerIDs(res domain.SendChannelMessageResult, extraUserIDs []int64) []int64 {
	return channelMessagesFanoutOwnerIDs([]domain.SendChannelMessageResult{res}, extraUserIDs)
}

// channelMessagesFanoutOwnerIDs 同上，但取多条结果（批量转发汇成一个 job）的 owner id 并集。
func channelMessagesFanoutOwnerIDs(results []domain.SendChannelMessageResult, extraUserIDs []int64) []int64 {
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
	return peerIDMapKeys(userIDs)
}

// enqueueChannelMessageFanout 异步 fan-out 单条频道消息并预热跨 viewer 投影（「频道里出现一条新消息」
// 类事件的常见形态：发送/转发单条/讨论组联动/forum topic 消息）。语义与 enqueueChannelFanout 一致，
// 仅多了把每 viewer 投影一次性算好预热进共享 cache（O(owner)），不改变投递/排除/nudge 行为。
func (r *Router) enqueueChannelMessageFanout(ctx context.Context, originUserID int64, res domain.SendChannelMessageResult, extraUserIDs []int64) {
	r.enqueueBotAPIChannelMessageUpdate(ctx, originUserID, res)
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelMessageFanoutOwnerIDs(res, extraUserIDs)
	skip := skipDeliverySet(res.SkipDeliveryUserIDs)
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMembers, originUserID, res.Channel.ID, res.Event.Pts, res.Recipients,
		0,
		func(bgCtx context.Context, viewers []int64) {
			r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs)
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			// privacy bot 在 send 时被 SkipDeliveryUserIDs 排除（命令/@/回复以外的消息不可见）。
			// channelFanoutRecipients 按「在线活跃成员」重算 recipients 会把它加回，故在线 fanout
			// 必须在此跳过它的直接推送，否则在线 bot 仍能实时收到群里全部消息（持久 history/
			// difference 已正确隐藏，仅此直接推送泄漏内容）。nudge 安全：bot 落在 seen 里不被 nudge，
			// 即便 nudge 也会被 filterBotChannelDifference 过滤掉隐藏消息。
			if _, skipped := skip[viewerUserID]; skipped {
				return nil
			}
			return r.channelMessageUpdatesWithPeerCache(bgCtx, viewerUserID, res, 0, fanoutCache)
		})
}

// enqueueMonoforumMessageFanout only targets the subscriber sub-dialog and active parent-channel
// admins. A monoforum has no ordinary members, so member recomputation would either drop the
// message or leak it to an invalid historical membership.
func (r *Router) enqueueMonoforumMessageFanout(ctx context.Context, originUserID int64, mono domain.Channel, savedPeer domain.Peer, res domain.SendChannelMessageResult) {
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelMessageFanoutOwnerIDs(res, []int64{savedPeer.ID})
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutExplicit, originUserID, mono.ID, res.Event.Pts, res.Recipients,
		0,
		func(bgCtx context.Context, viewers []int64) {
			r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs)
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.monoforumDeliveryUpdates(bgCtx, viewerUserID, mono, savedPeer, res)
		})
}

// skipDeliverySet 把 SkipDeliveryUserIDs 切片转成查找集合（nil 表示无排除）。
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

// channelEditMessageFanoutOwnerIDs 收集一条频道编辑 fan-out 会下发到 Users 数组里的全部 owner 用户 id。
// 严格镜像 channelEditMessageUpdates 的两容器 + pts 门控收集口径（Event/Message 仅 Event.Pts!=0 时收，
// ServiceEvent/ServiceMessage 仅 ServiceEvent.Pts!=0 时收，对应 todo 编辑的服务消息第二容器），使预热
// owner 集与 build 实际下发的 Users 集恰好一致——多收只会无害多预热，但镜像门控让等价测试最紧。
func channelEditMessageFanoutOwnerIDs(res domain.EditChannelMessageResult) []int64 {
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
	return peerIDMapKeys(userIDs)
}

// enqueueChannelEditMessageFanout 异步 fan-out 一条频道编辑并预热跨 viewer 投影（editMessage/geolive/
// todo/bot-inline edit 的共同形态）。语义与原 enqueueChannelFanout(channelEditMessageUpdates) 一致，
// 仅多了把每 viewer 投影一次性算好预热进共享 cache（O(owner)）。注意 edit 不做 per-viewer mention
// overlay（EditChannelMessageResult 不带 MentionUserIDs，编辑新增 @ 走 durable channel_unread_mentions
// 经 getChannelDifference 自愈），故各 viewer 的 mentioned/media_unread 字节恒等，预热不影响等价。
//
// nudge pts 取两容器较大值：edit 可产两条带 pts 事件（Event=编辑本身、ServiceEvent=如 todo 完成的
// "X completed Y" 服务消息），ServiceEvent.Pts 在后分配恒更大；某些编辑只产 ServiceEvent（Event.Pts==0）。
// nudge 须带 channel 当前最高 pts 才能让 >cap 在线成员的 getChannelDifference 拉齐到末尾——用 Event.Pts
// 会在 Event.Pts==0 时漏发 nudge、或低于真实 pts。max() 在三种形态（仅 Event/仅 ServiceEvent/两者）都正确。
func (r *Router) enqueueChannelEditMessageFanout(ctx context.Context, originUserID int64, res domain.EditChannelMessageResult) {
	r.enqueueBotAPIChannelEditMessageUpdate(ctx, originUserID, res)
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelEditMessageFanoutOwnerIDs(res)
	nudgePts := max(res.Event.Pts, res.ServiceEvent.Pts)
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMembers, originUserID, res.Channel.ID, nudgePts, res.Recipients,
		0,
		func(bgCtx context.Context, viewers []int64) {
			r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs)
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.channelEditMessageUpdatesWithPeerCache(bgCtx, viewerUserID, res, fanoutCache)
		})
}

// enqueueChannelMessagesFanout 同 enqueueChannelMessageFanout，但把多条结果汇成一个 job（批量转发：
// 一个 Updates 内含多条 UpdateNewChannelMessage），peer refs 取全部结果并集预热。channelID/pts/
// recipients 由调用方按批量语义给定（pts 取最后一条；recipients 受大群截断口径影响）。
func (r *Router) enqueueChannelMessagesFanout(ctx context.Context, originUserID, channelID int64, pts int, recipients []int64, results []domain.SendChannelMessageResult, extraUserIDs []int64) {
	r.enqueueBotAPIChannelMessagesUpdate(ctx, originUserID, results)
	fanoutCache := newViewerPeerCache(r)
	ownerIDs := channelMessagesFanoutOwnerIDs(results, extraUserIDs)
	r.enqueueChannelFanoutWithPrefetch(ctx, channelFanoutMembers, originUserID, channelID, pts, recipients,
		int64(len(results))*(64<<10),
		func(bgCtx context.Context, viewers []int64) {
			r.prefetchChannelFanoutUsers(bgCtx, fanoutCache, viewers, ownerIDs)
		},
		func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.channelMessagesUpdatesWithPeerCache(bgCtx, viewerUserID, results, nil, false, extraUserIDs, fanoutCache)
		})
}

// defaultChannelNudgeMaxTargets 限一次 fan-out 的 nudge 上限：nudge 是 O(1)/人廉价 push，但 nudge 被
// 消费后客户端会 getChannelDifference（DrKLO 对未加载频道还会先 getPeerDialogs，设计 §10.3），
// 大群高频下可能放大。可经 Config.ChannelNudgeMaxTargets 覆盖；客户端侧由 difference/getPeerDialogs
// 的 FLOOD_WAIT（Phase 2）兜底（见 checkCatchupRateLimit）。
const defaultChannelNudgeMaxTargets = 50000

// channelNudgeMaxTargets 返回生效的 nudge 上限（Config 覆盖，否则默认）。
func (r *Router) channelNudgeMaxTargets() int {
	if r.cfg.ChannelNudgeMaxTargets > 0 {
		return r.cfg.ChannelNudgeMaxTargets
	}
	return defaultChannelNudgeMaxTargets
}

// nudgeBeyondCapChannelMembers 给频道在线成员中未收到完整 payload（不在 delivered 内）的成员发
// UpdateChannelTooLong{pts}。nudge 必须带 pts（flags&1）——DrKLO 对不带 pts 的 tooLong 不触发
// getChannelDifference（设计 §10.3）。走 pushUserUpdates（best-effort、未就绪入 pending、非
// transient），符合设计 §决策4 的 nudge 投递可靠性要求。SessionManager 未实现 ChannelNudgeProvider
// 时（测试/未装配）静默跳过，不影响完整 payload 投递。
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
