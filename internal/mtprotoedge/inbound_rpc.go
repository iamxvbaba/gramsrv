package mtprotoedge

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrInboundRPCQueueFull 表示 inbound RPC 已触达单连接或进程级预算。
var ErrInboundRPCQueueFull = errors.New("inbound rpc queue full")

// maxInflightRPCBytes 是单连接所有已预留、排队和执行中 RPC body 的总字节上限。
// 进程级预算在 Copy 前先兜底；这里再隔离单个连接，避免一个客户端独占全局内存。
const maxInflightRPCBytes = 32 << 20 // 32 MiB

// rpcCloseWaitTimeout 是连接/Server 关闭时等待在途 RPC 或共享 worker 退出的上限。
const rpcCloseWaitTimeout = 5 * time.Second

type inboundRPC struct {
	ctx         context.Context
	cancel      context.CancelFunc
	stopRoot    func() bool
	stopTimeout func() bool
	method      string
	enqueuedAt  time.Time
	deadline    time.Time
	size        int
	run         func(context.Context) error
	onTimeout   func()
	budget      *inboundRPCGlobalReservation
	ticket      *inboundRPCTicket
}

const (
	inboundRPCTicketQueued int32 = iota
	inboundRPCTicketRunning
	inboundRPCTicketDone
)

type inboundRPCTicket struct {
	state     atomic.Int32
	onTimeout func()
}

// inboundRPCScheduler 是 Server 级共享调度器。ready 中每个 Conn 最多只有一个有效令牌；
// worker 每次只从该连接取一条，再把仍可运行的连接放回队尾，因此单个热点连接不能长期
// 占住共享池。worker 在首条任务到达后才创建，空闲 Server 不预起 256 个 goroutine。
type inboundRPCScheduler struct {
	workers  int
	maxTasks int
	maxBytes int64

	// ready is an intrusive scheduler-owned queue rather than a bounded channel. A connection
	// has at most one element, and close removes that element in O(1). This prevents closed-Conn
	// stale tokens from filling a channel and making every worker block while trying to reschedule.
	readyMu    sync.Mutex
	ready      *list.List
	readyIndex map[*Conn]*list.Element
	readyWake  chan struct{}
	stopCh     chan struct{}

	lifecycleMu    sync.Mutex
	started        bool
	stopped        bool
	workersStarted bool
	workerWG       sync.WaitGroup

	budgetMu sync.Mutex
	tasks    int
	bytes    int64
}

type inboundRPCGlobalReservation struct {
	scheduler *inboundRPCScheduler
	size      int64
	once      sync.Once
}

// inboundRPCReservation 同时持有全局和单连接的“Copy 前”预算。commit/abort 只能成功一次；
// 无论 Copy 后连接关闭、入队成功还是调用方提前返回，预算都有唯一归还路径。
type inboundRPCReservation struct {
	conn       *Conn
	global     *inboundRPCGlobalReservation
	ctx        context.Context
	method     string
	size       int
	enqueuedAt time.Time
	deadline   time.Time
	once       sync.Once
}

func newInboundRPCScheduler(workers, maxTasks int, maxBytes int64) *inboundRPCScheduler {
	if workers <= 0 {
		workers = 1
	}
	if maxTasks <= 0 {
		maxTasks = 1
	}
	if maxBytes <= 0 {
		maxBytes = 1
	}
	return &inboundRPCScheduler{
		workers:    workers,
		maxTasks:   maxTasks,
		maxBytes:   maxBytes,
		ready:      list.New(),
		readyIndex: make(map[*Conn]*list.Element),
		readyWake:  make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
	}
}

// start 允许共享池开始消费。已在 start 前进入 ready 的任务会保留顺序，便于启动突发，
// 也使测试能够确定性验证轮转公平性。
func (s *inboundRPCScheduler) start() {
	s.lifecycleMu.Lock()
	if s.stopped {
		s.lifecycleMu.Unlock()
		return
	}
	s.started = true
	shouldStart := s.readyLen() > 0
	s.lifecycleMu.Unlock()
	if shouldStart {
		s.ensureWorkers()
	}
}

func (s *inboundRPCScheduler) ensureWorkers() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !s.started || s.stopped || s.workersStarted {
		return
	}
	s.workersStarted = true
	s.workerWG.Add(s.workers)
	for i := 0; i < s.workers; i++ {
		go s.worker()
	}
}

func (s *inboundRPCScheduler) stop(timeout time.Duration) {
	s.lifecycleMu.Lock()
	if !s.stopped {
		s.stopped = true
		s.budgetMu.Lock()
		// 与 reserveGlobal 在同一把锁下切断新任务；已持有 reservation 的任务仍由
		// 对应 Conn 的 commit/abort/close 路径精确归还。
		close(s.stopCh)
		s.budgetMu.Unlock()
	}
	s.lifecycleMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.workerWG.Wait()
		close(done)
	}()
	if timeout <= 0 {
		<-done
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

func (s *inboundRPCScheduler) reserveGlobal(size int) (*inboundRPCGlobalReservation, string, error) {
	if size < 0 {
		size = 0
	}
	size64 := int64(size)
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()

	select {
	case <-s.stopCh:
		return nil, "scheduler_closed", ErrConnClosed
	default:
	}
	if s.tasks >= s.maxTasks {
		return nil, "global_task_budget", ErrInboundRPCQueueFull
	}
	// 用减法比较避免 s.bytes+size64 溢出。
	if size64 > s.maxBytes-s.bytes {
		return nil, "global_byte_budget", ErrInboundRPCQueueFull
	}
	s.tasks++
	s.bytes += size64
	return &inboundRPCGlobalReservation{scheduler: s, size: size64}, "", nil
}

func (r *inboundRPCGlobalReservation) release() {
	if r == nil || r.scheduler == nil {
		return
	}
	r.once.Do(func() {
		s := r.scheduler
		s.budgetMu.Lock()
		s.tasks--
		s.bytes -= r.size
		s.budgetMu.Unlock()
	})
}

func (s *inboundRPCScheduler) budgetSnapshot() (tasks int, bytes int64) {
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	return s.tasks, s.bytes
}

func (s *inboundRPCScheduler) schedule(c *Conn) {
	if s == nil || c == nil {
		return
	}
	// rpcReady/rpcClosed and queue membership must be tested/installed while holding rpcMu.
	// Otherwise close can remove the old token between the test and enqueue, leaving a new stale
	// token behind after the connection is already terminal.
	c.rpcMu.Lock()
	eligible := c.rpcReady && !c.rpcClosed
	added := false
	if eligible {
		added = s.enqueueReady(c)
	}
	c.rpcMu.Unlock()
	if !added {
		return
	}
	s.signalReady()
	s.ensureWorkers()
}

func (s *inboundRPCScheduler) worker() {
	defer s.workerWG.Done()
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}
		if c := s.popReady(); c != nil {
			task, ok, reschedule := c.takeInboundRPC()
			if reschedule {
				s.schedule(c)
			}
			if ok {
				c.runInboundRPC(task)
			}
			continue
		}
		select {
		case <-s.readyWake:
		case <-s.stopCh:
			return
		}
	}
}

func (s *inboundRPCScheduler) enqueueReady(c *Conn) bool {
	select {
	case <-s.stopCh:
		return false
	default:
	}
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	select {
	case <-s.stopCh:
		return false
	default:
	}
	if _, exists := s.readyIndex[c]; exists {
		return false
	}
	s.readyIndex[c] = s.ready.PushBack(c)
	return true
}

func (s *inboundRPCScheduler) popReady() *Conn {
	s.readyMu.Lock()
	front := s.ready.Front()
	if front == nil {
		s.readyMu.Unlock()
		return nil
	}
	c, _ := front.Value.(*Conn)
	s.ready.Remove(front)
	delete(s.readyIndex, c)
	hasMore := s.ready.Len() > 0
	s.readyMu.Unlock()
	if hasMore {
		// Wake another worker while this worker begins the task. A capacity-one wake channel is
		// sufficient: every pop cascades another wake until the queue is drained.
		s.signalReady()
	}
	return c
}

func (s *inboundRPCScheduler) unschedule(c *Conn) {
	if s == nil || c == nil {
		return
	}
	s.readyMu.Lock()
	if el := s.readyIndex[c]; el != nil {
		s.ready.Remove(el)
		delete(s.readyIndex, c)
	}
	hasMore := s.ready.Len() > 0
	s.readyMu.Unlock()
	if hasMore {
		s.signalReady()
	}
}

func (s *inboundRPCScheduler) readyLen() int {
	s.readyMu.Lock()
	defer s.readyMu.Unlock()
	return s.ready.Len()
}

func (s *inboundRPCScheduler) signalReady() {
	select {
	case s.readyWake <- struct{}{}:
	default:
	}
}

func (c *Conn) startInboundRPCScheduler(scheduler *inboundRPCScheduler, maxInflight, queueSize int, timeout time.Duration) {
	if c.metrics == nil {
		c.metrics = NopMetrics{}
	}
	if maxInflight <= 0 {
		maxInflight = 1
	}
	if queueSize <= 0 {
		queueSize = 1
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	c.rpcScheduler = scheduler
	c.rpcCancel = cancel
	c.rpcTimeout = timeout
	c.rpcRootCtx = rootCtx
	c.rpcMaxInflight = maxInflight
	c.rpcQueueSize = queueSize
	// rpcQueue 保持 nil；首个成功 commit 才由 append 分配，静默连接零队列内存。
}

// reserveInboundRPC 必须在 request body Copy 前调用。它先拿进程级条数/字节预算，
// 再预占单连接队列槽和字节预算；commit 或 abort 负责唯一释放。
func (c *Conn) reserveInboundRPC(ctx context.Context, method string, size int) (*inboundRPCReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		c.metrics.InboundRPCDropped(method, "context_done")
		return nil, ctx.Err()
	default:
	}
	if c.rpcScheduler == nil {
		c.metrics.InboundRPCDropped(method, "scheduler_closed")
		return nil, ErrConnClosed
	}
	global, reason, err := c.rpcScheduler.reserveGlobal(size)
	if err != nil {
		c.metrics.InboundRPCDropped(method, reason)
		return nil, err
	}

	now := time.Now()
	deadline := time.Time{}
	if c.rpcTimeout > 0 {
		deadline = now.Add(c.rpcTimeout)
	}
	if ctxDeadline, ok := ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
		deadline = ctxDeadline
	}
	if size < 0 {
		size = 0
	}

	c.rpcMu.Lock()
	if err := ctx.Err(); err != nil {
		c.rpcMu.Unlock()
		global.release()
		c.metrics.InboundRPCDropped(method, "context_done")
		return nil, err
	}
	if c.rpcClosed {
		c.rpcMu.Unlock()
		global.release()
		c.metrics.InboundRPCDropped(method, "scheduler_closed")
		return nil, ErrConnClosed
	}
	if c.rpcReserved+len(c.rpcQueue) >= c.rpcQueueSize {
		c.rpcMu.Unlock()
		global.release()
		c.metrics.InboundRPCDropped(method, "queue_full")
		return nil, ErrInboundRPCQueueFull
	}
	if int64(size) > maxInflightRPCBytes-c.inflightRPCBytes.Load() {
		c.rpcMu.Unlock()
		global.release()
		c.metrics.InboundRPCDropped(method, "byte_budget")
		return nil, ErrInboundRPCQueueFull
	}
	c.rpcReserved++
	c.inflightRPCBytes.Add(int64(size))
	// Add 与 close 的 Wait 由 rpcMu 排序：close 置 rpcClosed 后不会再发生 Add。
	c.rpcReservationWG.Add(1)
	c.rpcMu.Unlock()

	return &inboundRPCReservation{
		conn:       c,
		global:     global,
		ctx:        ctx,
		method:     method,
		size:       size,
		enqueuedAt: now,
		deadline:   deadline,
	}, nil
}

// enqueueInboundRPC 是测试和已持有独立 body 的便捷入口。生产收包路径使用
// reserveInboundRPC -> Copy -> commit，保证真正的 Copy 前预算。
func (c *Conn) enqueueInboundRPC(ctx context.Context, task inboundRPC) error {
	reservation, err := c.reserveInboundRPC(ctx, task.method, task.size)
	if err != nil {
		return err
	}
	defer reservation.abort()
	return reservation.commit(task)
}

func (r *inboundRPCReservation) commit(task inboundRPC) error {
	result := ErrConnClosed
	var (
		committed  bool
		reschedule bool
		queueLen   int
		queueCap   int
	)
	r.once.Do(func() {
		c := r.conn
		c.rpcMu.Lock()
		c.rpcReserved--
		if c.rpcClosed {
			c.inflightRPCBytes.Add(-int64(r.size))
		} else {
			// The request deadline starts when admission succeeds, not when a worker
			// eventually dequeues the request. This bounds total queue + execution
			// latency and lets a queued request emit its explicit timeout on time.
			if r.deadline.IsZero() {
				task.ctx, task.cancel = context.WithCancel(r.ctx)
			} else {
				task.ctx, task.cancel = context.WithDeadline(r.ctx, r.deadline)
			}
			task.stopRoot = context.AfterFunc(c.rpcRootCtx, task.cancel)
			task.method = r.method
			task.enqueuedAt = r.enqueuedAt
			task.deadline = r.deadline
			task.size = r.size
			task.budget = r.global
			ticket := &inboundRPCTicket{}
			if task.onTimeout != nil {
				onTimeout := task.onTimeout
				var timeoutOnce sync.Once
				ticket.onTimeout = func() {
					timeoutOnce.Do(onTimeout)
				}
				task.onTimeout = ticket.onTimeout
			}
			task.ticket = ticket
			if task.onTimeout != nil && !task.deadline.IsZero() {
				taskCtx := task.ctx
				task.stopTimeout = context.AfterFunc(taskCtx, func() {
					if errors.Is(taskCtx.Err(), context.DeadlineExceeded) {
						c.expireInboundRPCTicket(ticket)
					}
				})
			}
			c.rpcQueue = append(c.rpcQueue, task)
			queueLen = len(c.rpcQueue)
			queueCap = c.rpcQueueSize
			if c.rpcRunning < c.rpcMaxInflight && !c.rpcReady {
				c.rpcReady = true
				reschedule = true
			}
			committed = true
			result = nil
		}
		c.rpcMu.Unlock()
		c.rpcReservationWG.Done()
		if !committed {
			r.global.release()
		}
	})
	if committed {
		r.conn.metrics.InboundRPCQueued(r.method, queueLen, queueCap)
		if reschedule {
			r.conn.rpcScheduler.schedule(r.conn)
		}
	}
	return result
}

func (r *inboundRPCReservation) abort() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		c := r.conn
		c.rpcMu.Lock()
		c.rpcReserved--
		c.inflightRPCBytes.Add(-int64(r.size))
		c.rpcMu.Unlock()
		c.rpcReservationWG.Done()
		r.global.release()
	})
}

func (c *Conn) takeInboundRPC() (task inboundRPC, ok, reschedule bool) {
	c.rpcMu.Lock()
	defer c.rpcMu.Unlock()
	// ready token 是可替代的：收到一个 token 就消费当前“已调度”状态。关闭后或
	// 已被另一 token 抢先处理时，这只是一个无害 stale token。
	if !c.rpcReady {
		return inboundRPC{}, false, false
	}
	c.rpcReady = false
	if c.rpcClosed || len(c.rpcQueue) == 0 || c.rpcRunning >= c.rpcMaxInflight {
		return inboundRPC{}, false, false
	}
	task = c.rpcQueue[0]
	c.rpcQueue[0] = inboundRPC{}
	c.rpcQueue = c.rpcQueue[1:]
	if len(c.rpcQueue) == 0 {
		c.rpcQueue = nil
	}
	c.rpcRunning++
	if task.ticket != nil {
		task.ticket.state.Store(inboundRPCTicketRunning)
	}
	c.rpcWG.Add(1)
	if len(c.rpcQueue) > 0 && c.rpcRunning < c.rpcMaxInflight {
		c.rpcReady = true
		reschedule = true
	}
	return task, true, reschedule
}

func (c *Conn) runInboundRPC(task inboundRPC) {
	defer c.finishInboundRPC(task)

	now := time.Now()
	ctxErr := task.ctx.Err()
	if (!task.deadline.IsZero() && !now.Before(task.deadline)) || errors.Is(ctxErr, context.DeadlineExceeded) {
		c.metrics.InboundRPCDropped(task.method, "queue_timeout")
		if task.onTimeout != nil {
			task.onTimeout()
		}
		return
	}
	if ctxErr != nil {
		c.metrics.InboundRPCDropped(task.method, "context_done")
		return
	}

	c.metrics.InboundRPCStarted(task.method, now.Sub(task.enqueuedAt))
	ctx := task.ctx
	if task.run != nil {
		_ = task.run(ctx)
	}
}

func (c *Conn) finishInboundRPC(task inboundRPC) {
	if task.ticket != nil {
		task.ticket.state.Store(inboundRPCTicketDone)
	}
	stopInboundRPCTask(task)
	var reschedule bool
	c.rpcMu.Lock()
	c.rpcRunning--
	c.inflightRPCBytes.Add(-int64(task.size))
	if !c.rpcClosed && len(c.rpcQueue) > 0 && c.rpcRunning < c.rpcMaxInflight && !c.rpcReady {
		c.rpcReady = true
		reschedule = true
	}
	c.rpcMu.Unlock()
	reservation := task.budget
	// The scheduler budget may be reused immediately after release. Clear request-owned
	// closures/context references first so slow metrics/rescheduling cannot overlap the old body
	// with a newly admitted body under the same byte accounting.
	task = inboundRPC{}
	reservation.release()
	c.rpcWG.Done()
	if reschedule {
		c.rpcScheduler.schedule(c)
	}
}

// expireInboundRPCTicket removes a request that is still queued and returns its
// memory/task reservations immediately. If the worker won the dequeue race, the
// same callback only signals the running request's response gate; its body remains
// owned until the handler exits.
func (c *Conn) expireInboundRPCTicket(ticket *inboundRPCTicket) {
	if ticket == nil {
		return
	}
	var (
		task       inboundRPC
		found      bool
		unschedule bool
	)
	c.rpcMu.Lock()
	for i := range c.rpcQueue {
		if c.rpcQueue[i].ticket != ticket {
			continue
		}
		task = c.rpcQueue[i]
		copy(c.rpcQueue[i:], c.rpcQueue[i+1:])
		last := len(c.rpcQueue) - 1
		c.rpcQueue[last] = inboundRPC{}
		c.rpcQueue = c.rpcQueue[:last]
		if len(c.rpcQueue) == 0 {
			c.rpcQueue = nil
			if c.rpcReady {
				c.rpcReady = false
				unschedule = true
			}
		}
		c.inflightRPCBytes.Add(-int64(task.size))
		ticket.state.Store(inboundRPCTicketDone)
		found = true
		break
	}
	c.rpcMu.Unlock()

	if unschedule {
		c.rpcScheduler.unschedule(c)
	}
	if found {
		method := task.method
		reservation := task.budget
		stopInboundRPCTask(task)
		// Drop the run/context closures before returning the byte reservation. Otherwise an
		// onTimeout callback that blocks or performs a slow write can keep the copied request body
		// reachable after the global scheduler has advertised those bytes as available again.
		task = inboundRPC{}
		reservation.release()
		c.metrics.InboundRPCDropped(method, "queue_timeout")
		if ticket.onTimeout != nil {
			ticket.onTimeout()
		}
		return
	}
	if ticket.state.Load() == inboundRPCTicketRunning && ticket.onTimeout != nil {
		ticket.onTimeout()
	}
}

// stopInboundRPCTask disarms callbacks before canceling the context so a normal
// completion or connection close cannot manufacture an RPC_TIMEOUT response.
// A deadline callback already in flight is harmless because enqueueRPC's response
// gate makes timeout and normal rpc_result mutually exclusive.
func stopInboundRPCTask(task inboundRPC) {
	if task.stopTimeout != nil {
		task.stopTimeout()
	}
	if task.stopRoot != nil {
		task.stopRoot()
	}
	if task.cancel != nil {
		task.cancel()
	}
}

func (c *Conn) closeInboundRPCScheduler() {
	c.beginCloseInboundRPCScheduler()
	if c.rpcScheduler == nil {
		return
	}
	c.waitInboundShutdown(rpcCloseWaitTimeout)
}

// beginCloseInboundRPCScheduler publishes closure, cancels running work and releases queued
// requests without waiting for handlers. ForceClose uses this phase before transport.Close so a
// pathological/blocking transport implementation cannot leave the RPC admission gate open.
func (c *Conn) beginCloseInboundRPCScheduler() {
	if c.rpcScheduler == nil {
		return
	}
	c.rpcClose.Do(func() {
		c.rpcMu.Lock()
		c.rpcClosed = true
		c.rpcReady = false
		queued := c.rpcQueue
		c.rpcQueue = nil
		for i := range queued {
			c.inflightRPCBytes.Add(-int64(queued[i].size))
		}
		c.rpcMu.Unlock()
		// Remove the scheduler-owned token after rpcClosed/rpcReady become visible. schedule()
		// takes rpcMu while installing a token, so either it finishes first and is removed here,
		// or it observes the closed state and cannot enqueue a new stale token afterward.
		c.rpcScheduler.unschedule(c)

		if c.rpcCancel != nil {
			c.rpcCancel()
		}
		for i := range queued {
			task := queued[i]
			queued[i] = inboundRPC{}
			if task.ticket != nil {
				task.ticket.state.Store(inboundRPCTicketDone)
			}
			method := task.method
			reservation := task.budget
			stopInboundRPCTask(task)
			task = inboundRPC{}
			reservation.release()
			c.metrics.InboundRPCDropped(method, "connection_closed")
		}
	})
}

// waitInboundShutdown 等 Copy 前 reservation 完成 commit/abort，以及本连接已经出队的 RPC
// 完成，二者共用一个 timeout。超时后 reservation/共享 worker 会在底层调用最终返回时自行
// 收敛；连接 root context 已取消。
func (c *Conn) waitInboundShutdown(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		c.rpcReservationWG.Wait()
		c.rpcWG.Wait()
		close(done)
	}()
	if timeout <= 0 {
		return false
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
