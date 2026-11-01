package mtprotoedge

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"telesrv/internal/transport"
)

// ErrInboundRPCQueueFull means inbound RPC has reached the per-connection or process-level budget.
var ErrInboundRPCQueueFull = errors.New("inbound rpc queue full")

// maxInflightRPCBytes is the memory charge cap for all reserved, queued and
// executing RPCs on a single connection. The legacy path charges the copied body;
// the exact path charges conservatively based on wire size and the inflation of
// generated objects/vectors/interfaces/strings/bytes before typed decode. It is
// not a cap on acceptable wire bytes. The process-level budget applies first as a
// fallback; this additionally isolates a single connection, preventing one client
// from monopolizing global memory.
const maxInflightRPCBytes = 32 << 20 // 32 MiB

// rpcCloseWaitTimeout is the cap for waiting on in-flight RPCs or shared workers
// to exit when a connection/Server closes.
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
	// release drops request-scoped ownership which is independent from the body
	// budget (for example a cross-connection in-flight RPC claim). It runs once
	// after the request has either published a terminal result or become
	// impossible to run; callers should still make the callback idempotent.
	release func()
	budget  *inboundRPCGlobalReservation
	ticket  *inboundRPCTicket
	gate    *inboundRPCGate
}

// inboundRPCGate keeps dependency waits out of the shared worker pool. A
// gated task remains within the ordinary queue/task/byte budgets, but workers
// skip it until every prerequisite publishes its terminal execution outcome.
type inboundRPCGate struct {
	remaining atomic.Int32
	failed    atomic.Bool
	ready     atomic.Bool
	wake      func()
}

func newInboundRPCGate(prerequisites int, wake func()) *inboundRPCGate {
	if prerequisites < 0 {
		prerequisites = 0
	}
	g := &inboundRPCGate{wake: wake}
	// One sentinel keeps the gate closed while subscribers are installed; the
	// caller resolves it after registration is complete.
	g.remaining.Store(int32(prerequisites + 1))
	return g
}

func (g *inboundRPCGate) resolve(success bool) {
	if g == nil {
		return
	}
	if !success {
		g.failed.Store(true)
	}
	remaining := g.remaining.Add(-1)
	if remaining < 0 {
		panic("mtproto inbound RPC gate counter underflow")
	}
	if remaining == 0 && g.ready.CompareAndSwap(false, true) && g.wake != nil {
		g.wake()
	}
}

func (g *inboundRPCGate) runnable() bool { return g == nil || g.ready.Load() }
func (g *inboundRPCGate) success() bool  { return g == nil || (g.ready.Load() && !g.failed.Load()) }

type inboundRPCTicket struct {
	onTimeout func()
}

// inboundRPCScheduler is the Server-level shared scheduler. Each Conn in ready
// has at most one valid token; a worker takes only one item from that connection
// at a time, then puts the still-runnable connection back at the tail of the
// queue, so a single hot connection cannot occupy the shared pool for long.
// Workers are created only after the first task arrives; an idle Server does not
// pre-start 256 goroutines.
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
	released  atomic.Bool
}

// inboundRPCSpec is the bounded admission descriptor between container preflight
// and the RPC scheduler. method is only used for metrics; size is the
// materialization charge. The legacy path equals the request body byte count
// before Copy; the exact path is the conservative memory bound computed before
// typed decode.
type inboundRPCSpec struct {
	method string
	size   int
}

type inboundRPCBatchEntry struct {
	global *inboundRPCGlobalReservation
	method string
	size   int
}

// inboundRPCBatchReservation treats the RPCs within one container as a single
// admission unit. reserve reserves the whole batch's global/per-connection count
// and byte budgets at once; commit appends all tasks at once; abort returns the
// whole budget at once. This prevents a container from executing only the first
// half of its batch.
type inboundRPCBatchReservation struct {
	conn      *Conn
	ctx       context.Context
	entries   []inboundRPCBatchEntry
	totalSize int64
	once      sync.Once
}

var errInboundRPCBatchTaskCount = errors.New("inbound rpc batch task count mismatch")
var errInboundRPCBatchSelection = errors.New("inbound rpc batch selection is invalid")

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

// start lets the shared pool begin consuming. Tasks that entered ready before
// start keep their order, which smooths startup bursts and lets tests verify
// round-robin fairness deterministically.
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
		// Cut off new tasks under the same lock as reserveGlobal; tasks already
		// holding a reservation are still returned precisely by the corresponding
		// Conn's commit/abort/close path.
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

// reserveGlobalBatch checks and reserves the whole batch's count/bytes within one
// budgetMu critical section. Each returned reservation is still returned
// individually by its task, so a slow RPC does not hold the budget for the whole
// container's completed tasks.
func (s *inboundRPCScheduler) reserveGlobalBatch(sizes []int) ([]*inboundRPCGlobalReservation, string, error) {
	reservations := make([]*inboundRPCGlobalReservation, len(sizes))
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()

	select {
	case <-s.stopCh:
		return nil, "scheduler_closed", ErrConnClosed
	default:
	}
	if len(sizes) > s.maxTasks-s.tasks {
		return nil, "global_task_budget", ErrInboundRPCQueueFull
	}
	// Subtract item by item from the remaining budget, avoiding overflow of total and s.bytes+total.
	remaining := s.maxBytes - s.bytes
	var total int64
	for i, size := range sizes {
		if size < 0 {
			size = 0
		}
		size64 := int64(size)
		if size64 > remaining-total {
			return nil, "global_byte_budget", ErrInboundRPCQueueFull
		}
		total += size64
		reservations[i] = &inboundRPCGlobalReservation{scheduler: s, size: size64}
	}
	s.tasks += len(sizes)
	s.bytes += total
	return reservations, "", nil
}

func (r *inboundRPCGlobalReservation) release() {
	if r == nil || r.scheduler == nil {
		return
	}
	if !r.released.CompareAndSwap(false, true) {
		return
	}
	s := r.scheduler
	s.budgetMu.Lock()
	s.tasks--
	s.bytes -= r.size
	s.budgetMu.Unlock()
}

// releaseInboundRPCGlobalBatch lets a batch abort/commit-failure return all the
// budget it still holds within one global lock. Each ticket's CAS guarantees
// idempotency with any concurrent release.
func releaseInboundRPCGlobalBatch(reservations []*inboundRPCGlobalReservation) {
	var (
		scheduler *inboundRPCScheduler
		tasks     int
		bytes     int64
	)
	for _, reservation := range reservations {
		if reservation == nil || reservation.scheduler == nil || !reservation.released.CompareAndSwap(false, true) {
			continue
		}
		if scheduler == nil {
			scheduler = reservation.scheduler
		}
		// A batch can only be created by the same scheduler. If a mixed call ever
		// appears, it is still returned correctly through a single release without
		// debiting the wrong scheduler.
		if reservation.scheduler != scheduler {
			reservation.scheduler.budgetMu.Lock()
			reservation.scheduler.tasks--
			reservation.scheduler.bytes -= reservation.size
			reservation.scheduler.budgetMu.Unlock()
			continue
		}
		tasks++
		bytes += reservation.size
	}
	if scheduler == nil || tasks == 0 {
		return
	}
	scheduler.budgetMu.Lock()
	scheduler.tasks -= tasks
	scheduler.bytes -= bytes
	scheduler.budgetMu.Unlock()
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
	// rpcQueue stays nil; only the first successful commit allocates it via append,
	// so a silent connection uses zero queue memory.
}

// reserveInboundRPCBatch must be called before any request body Copy within the
// container. The global budget is locked once and the per-connection budget is
// locked once; if either limit is not satisfied the whole batch fails, leaving no
// partial task/byte reservation.
func (c *Conn) reserveInboundRPCBatch(ctx context.Context, specs []inboundRPCSpec) (*inboundRPCBatchReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		c.dropInboundRPCSpecs(specs, "context_done")
		return nil, ctx.Err()
	default:
	}
	if c.isRetired() {
		c.dropInboundRPCSpecs(specs, "scheduler_closed")
		return nil, ErrConnClosed
	}
	if c.rpcScheduler == nil {
		c.dropInboundRPCSpecs(specs, "scheduler_closed")
		return nil, ErrConnClosed
	}

	normalized := make([]inboundRPCSpec, len(specs))
	sizes := make([]int, len(specs))
	for i, spec := range specs {
		if spec.size < 0 {
			spec.size = 0
		}
		normalized[i] = spec
		sizes[i] = spec.size
	}
	globals, reason, err := c.rpcScheduler.reserveGlobalBatch(sizes)
	if err != nil {
		c.dropInboundRPCSpecs(normalized, reason)
		return nil, err
	}

	entries := make([]inboundRPCBatchEntry, len(normalized))
	var totalSize int64
	for i, spec := range normalized {
		entries[i] = inboundRPCBatchEntry{
			global: globals[i],
			method: spec.method,
			size:   spec.size,
		}
		totalSize += int64(spec.size)
	}

	c.rpcMu.Lock()
	if err := ctx.Err(); err != nil {
		c.rpcMu.Unlock()
		releaseInboundRPCGlobalBatch(globals)
		c.dropInboundRPCSpecs(normalized, "context_done")
		return nil, err
	}
	if c.rpcClosed || c.isRetired() {
		c.rpcMu.Unlock()
		releaseInboundRPCGlobalBatch(globals)
		c.dropInboundRPCSpecs(normalized, "scheduler_closed")
		return nil, ErrConnClosed
	}
	// Compare by subtraction to avoid overflow when adding len for an adversarial batch.
	availableSlots := c.rpcQueueSize - c.rpcReserved - len(c.rpcQueue)
	if len(entries) > availableSlots {
		c.rpcMu.Unlock()
		releaseInboundRPCGlobalBatch(globals)
		c.dropInboundRPCSpecs(normalized, "queue_full")
		return nil, ErrInboundRPCQueueFull
	}
	if totalSize > maxInflightRPCBytes-c.inflightRPCBytes.Load() {
		c.rpcMu.Unlock()
		releaseInboundRPCGlobalBatch(globals)
		c.dropInboundRPCSpecs(normalized, "byte_budget")
		return nil, ErrInboundRPCQueueFull
	}
	c.rpcReserved += len(entries)
	c.inflightRPCBytes.Add(totalSize)
	// Add and close's Wait are ordered by rpcMu: once close sets rpcClosed, no
	// further Add can occur. The whole batch reservation needs only one waiter;
	// commit/abort also complete only once.
	c.rpcReservationWG.Add(1)
	c.rpcMu.Unlock()

	return &inboundRPCBatchReservation{
		conn:      c,
		ctx:       ctx,
		entries:   entries,
		totalSize: totalSize,
	}, nil
}

func (c *Conn) dropInboundRPCSpecs(specs []inboundRPCSpec, reason string) {
	for _, spec := range specs {
		c.metrics.InboundRPCDropped(spec.method, reason)
	}
}

// growEntry raises one provisional task's materialization charge without a
// release/reacquire window. Exact gzip admission calls this after the expanded
// size is known but before the generated typed decoder can allocate the request
// graph. A failure leaves every existing reservation unchanged so the caller
// can reject and abort the whole container atomically.
func (r *inboundRPCBatchReservation) growEntry(index, targetSize int) error {
	if r == nil || index < 0 || index >= len(r.entries) || targetSize < 0 {
		return errInboundRPCBatchSelection
	}
	entry := &r.entries[index]
	if targetSize <= entry.size {
		return nil
	}
	if entry.global == nil || entry.global.scheduler == nil || entry.global.released.Load() {
		return ErrConnClosed
	}
	delta := int64(targetSize) - int64(entry.size)
	scheduler := entry.global.scheduler
	conn := r.conn

	// Match initial admission's lock order: global scheduler budget, then the
	// connection queue budget. Release paths never hold rpcMu while acquiring
	// budgetMu, so this cannot invert task completion or close.
	scheduler.budgetMu.Lock()
	defer scheduler.budgetMu.Unlock()
	select {
	case <-scheduler.stopCh:
		return ErrConnClosed
	default:
	}
	if delta > scheduler.maxBytes-scheduler.bytes {
		return fmt.Errorf("%w: grow exact admission global byte budget by %d", ErrInboundRPCQueueFull, delta)
	}

	conn.rpcMu.Lock()
	defer conn.rpcMu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if conn.rpcClosed || conn.isRetired() {
		return ErrConnClosed
	}
	if delta > int64(maxInflightRPCBytes)-conn.inflightRPCBytes.Load() {
		return fmt.Errorf("%w: grow exact admission connection byte budget by %d", ErrInboundRPCQueueFull, delta)
	}

	scheduler.bytes += delta
	entry.global.size += delta
	entry.size = targetSize
	r.totalSize += delta
	conn.inflightRPCBytes.Add(delta)
	return nil
}

// retain keeps a subset of a provisional batch on the original connection and
// global reservations. Exact-layer admission uses this after typed decode has
// classified completed replays, pending joins, admission errors, and fresh
// owners. A retained fresh owner therefore never passes through a release then
// reacquire window where another connection could consume its memory/task
// budget. Entries not retained are returned immediately as one batch.
//
// indices are reservation-entry indices, must be strictly increasing, and have
// a one-to-one correspondence with specs. The original conservative byte
// charge is intentionally retained; a typed request remains reachable from its
// scheduler task until completion, so shrinking it to the wire size would make
// the materialized graph unaccounted.
func (r *inboundRPCBatchReservation) retain(indices []int, specs []inboundRPCSpec) error {
	if r == nil || len(indices) != len(specs) || len(indices) > len(r.entries) {
		return errInboundRPCBatchSelection
	}
	retained := make([]inboundRPCBatchEntry, len(indices))
	removed := make([]*inboundRPCGlobalReservation, 0, len(r.entries)-len(indices))
	var (
		previous     = -1
		retainedSize int64
		removedSize  int64
	)
	selected := 0
	for output, index := range indices {
		if index <= previous || index < 0 || index >= len(r.entries) {
			return errInboundRPCBatchSelection
		}
		previous = index
		for selected < index {
			entry := r.entries[selected]
			removed = append(removed, entry.global)
			removedSize += int64(entry.size)
			selected++
		}
		entry := r.entries[index]
		// The caller may improve the provisional metric label but may not increase
		// or replace its byte charge after materialization has started.
		if specs[output].size > entry.size {
			return errInboundRPCBatchSelection
		}
		entry.method = specs[output].method
		retained[output] = entry
		retainedSize += int64(entry.size)
		selected = index + 1
	}
	for selected < len(r.entries) {
		entry := r.entries[selected]
		removed = append(removed, entry.global)
		removedSize += int64(entry.size)
		selected++
	}

	c := r.conn
	c.rpcMu.Lock()
	c.rpcReserved -= len(removed)
	c.inflightRPCBytes.Add(-removedSize)
	r.entries = retained
	r.totalSize = retainedSize
	c.rpcMu.Unlock()
	releaseInboundRPCGlobalBatch(removed)

	if len(retained) == 0 {
		// Finish the one reservation waiter as part of the same ownership
		// transition. abort sees an empty batch and is therefore accounting-only.
		r.abort()
	}
	return nil
}

// commit appends the whole batch of tasks to the queue within one rpcMu critical
// section and immediately publishes the ready token. The protocol barrier must be
// completed before calling commit; delaying token publication cannot stop an
// existing worker from taking new tasks off the same connection's queue, so no
// fake deferred-schedule mode is provided.
func (r *inboundRPCBatchReservation) commit(tasks []inboundRPC) (result error) {
	if r == nil {
		return ErrConnClosed
	}
	result = ErrConnClosed
	var (
		committed     bool
		reschedule    bool
		firstQueueLen int
		queueCap      int
		globals       []*inboundRPCGlobalReservation
	)
	r.once.Do(func() {
		c := r.conn
		// A container reservation may deliberately span protocol-critical barriers
		// (session ownership claim and new_session_created's physical write). Queue
		// and execution latency starts only when the fully admitted batch becomes
		// runnable, not while it is waiting behind those independent barriers.
		enqueuedAt := time.Now()
		deadline := time.Time{}
		if c.rpcTimeout > 0 {
			deadline = enqueuedAt.Add(c.rpcTimeout)
		}
		if ctxDeadline, ok := r.ctx.Deadline(); ok && (deadline.IsZero() || ctxDeadline.Before(deadline)) {
			deadline = ctxDeadline
		}
		globals = make([]*inboundRPCGlobalReservation, len(r.entries))
		for i := range r.entries {
			globals[i] = r.entries[i].global
		}
		c.rpcMu.Lock()
		c.rpcReserved -= len(r.entries)
		if len(tasks) != len(r.entries) {
			c.inflightRPCBytes.Add(-r.totalSize)
			result = errInboundRPCBatchTaskCount
		} else if c.rpcClosed || c.isRetired() {
			c.inflightRPCBytes.Add(-r.totalSize)
		} else {
			prepared := make([]inboundRPC, len(tasks))
			// The request deadline starts when admission succeeds, not when a worker
			// eventually dequeues the request. This bounds total queue + execution
			// latency and lets a queued request emit its explicit timeout on time.
			for i := range tasks {
				task := tasks[i]
				entry := r.entries[i]
				if deadline.IsZero() {
					task.ctx, task.cancel = context.WithCancel(r.ctx)
				} else {
					task.ctx, task.cancel = context.WithDeadline(r.ctx, deadline)
				}
				task.stopRoot = context.AfterFunc(c.rpcRootCtx, task.cancel)
				task.method = entry.method
				task.enqueuedAt = enqueuedAt
				task.deadline = deadline
				task.size = entry.size
				task.budget = entry.global
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
				prepared[i] = task
			}
			firstQueueLen = len(c.rpcQueue) + 1
			c.rpcQueue = append(c.rpcQueue, prepared...)
			queueCap = c.rpcQueueSize
			if c.rpcRunning < c.rpcMaxInflight && !c.rpcReady && c.hasRunnableInboundRPCLocked() {
				c.rpcReady = true
				reschedule = true
			}
			committed = true
			result = nil
		}
		c.rpcMu.Unlock()
		c.rpcReservationWG.Done()
		if !committed {
			releaseInboundRPCGlobalBatch(globals)
		}
	})
	if committed {
		for i, entry := range r.entries {
			r.conn.metrics.InboundRPCQueued(entry.method, firstQueueLen+i, queueCap)
		}
		if reschedule {
			r.conn.rpcScheduler.schedule(r.conn)
		}
	}
	return result
}

func (r *inboundRPCBatchReservation) abort() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		c := r.conn
		c.rpcMu.Lock()
		c.rpcReserved -= len(r.entries)
		c.inflightRPCBytes.Add(-r.totalSize)
		c.rpcMu.Unlock()
		c.rpcReservationWG.Done()
		globals := make([]*inboundRPCGlobalReservation, len(r.entries))
		for i := range r.entries {
			globals[i] = r.entries[i].global
		}
		releaseInboundRPCGlobalBatch(globals)
	})
}

func (c *Conn) takeInboundRPC() (task inboundRPC, ok, reschedule bool) {
	c.rpcMu.Lock()
	defer c.rpcMu.Unlock()
	// The ready token is fungible: receiving one token consumes the current
	// "scheduled" state. After close, or when another token has already handled it
	// first, this is just a harmless stale token.
	if !c.rpcReady {
		return inboundRPC{}, false, false
	}
	c.rpcReady = false
	if c.rpcClosed || len(c.rpcQueue) == 0 || c.rpcRunning >= c.rpcMaxInflight {
		return inboundRPC{}, false, false
	}
	index := -1
	for i := range c.rpcQueue {
		if c.rpcQueue[i].gate.runnable() {
			index = i
			break
		}
	}
	if index < 0 {
		return inboundRPC{}, false, false
	}
	task = c.rpcQueue[index]
	if index == 0 {
		// The overwhelmingly common FIFO path advances the slice head in O(1).
		// Clear the departed element first so its request graph and closures are
		// not retained by the shared backing array.
		c.rpcQueue[0] = inboundRPC{}
		c.rpcQueue = c.rpcQueue[1:]
	} else {
		copy(c.rpcQueue[index:], c.rpcQueue[index+1:])
		last := len(c.rpcQueue) - 1
		c.rpcQueue[last] = inboundRPC{}
		c.rpcQueue = c.rpcQueue[:last]
	}
	if len(c.rpcQueue) == 0 {
		c.rpcQueue = nil
	}
	c.rpcRunning++
	c.rpcWG.Add(1)
	if c.rpcRunning < c.rpcMaxInflight && c.hasRunnableInboundRPCLocked() {
		c.rpcReady = true
		reschedule = true
	}
	return task, true, reschedule
}

func (c *Conn) hasRunnableInboundRPCLocked() bool {
	if c.rpcReplayRestores > 0 {
		return false
	}
	for i := range c.rpcQueue {
		if c.rpcQueue[i].gate.runnable() {
			return true
		}
	}
	return false
}

// beginRPCReplayRestore installs a scheduler-level barrier without occupying a
// global RPC worker. The returned idempotent completion function wakes queued
// work only after the last overlapping restore has finished.
func (c *Conn) beginRPCReplayRestore() func() {
	if c == nil {
		return func() {}
	}
	active := false
	unschedule := false
	c.rpcMu.Lock()
	if !c.rpcClosed && !c.isRetired() {
		c.rpcReplayRestores++
		active = true
		if c.rpcReady {
			c.rpcReady = false
			unschedule = true
		}
	}
	c.rpcMu.Unlock()
	if unschedule && c.rpcScheduler != nil {
		c.rpcScheduler.unschedule(c)
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			if !active {
				return
			}
			reschedule := false
			c.rpcMu.Lock()
			c.rpcReplayRestores--
			if c.rpcReplayRestores < 0 {
				c.rpcMu.Unlock()
				panic("mtproto inbound RPC replay restore barrier underflow")
			}
			if c.rpcReplayRestores == 0 && !c.rpcClosed && c.rpcRunning < c.rpcMaxInflight &&
				!c.rpcReady && c.hasRunnableInboundRPCLocked() {
				c.rpcReady = true
				reschedule = true
			}
			c.rpcMu.Unlock()
			if reschedule && c.rpcScheduler != nil {
				c.rpcScheduler.schedule(c)
			}
		})
	}
}

// wakeInboundRPC is safe from a flight completion callback. It does no work
// while the gate's task has not been committed yet; commit performs the same
// runnable scan and publishes the initial scheduler token.
func (c *Conn) wakeInboundRPC() {
	if c == nil || c.rpcScheduler == nil {
		return
	}
	reschedule := false
	c.rpcMu.Lock()
	if !c.rpcClosed && c.rpcRunning < c.rpcMaxInflight && !c.rpcReady && c.hasRunnableInboundRPCLocked() {
		c.rpcReady = true
		reschedule = true
	}
	c.rpcMu.Unlock()
	if reschedule {
		c.rpcScheduler.schedule(c)
	}
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
	// Inject the connection's peer IP as neutral transport metadata, written to
	// authorizations.ip when binding a device authorization. The edge only produces
	// transport facts; they are consumed by the RPC layer's transport.ClientIPFrom
	// to avoid a reverse dependency.
	if ip := c.clientIP(); ip != "" {
		ctx = transport.WithClientIP(ctx, ip)
	}
	if task.run != nil {
		_ = task.run(ctx)
	}
}

func (c *Conn) finishInboundRPC(task inboundRPC) {
	stopInboundRPCTask(task)
	var reschedule bool
	c.rpcMu.Lock()
	c.rpcRunning--
	c.inflightRPCBytes.Add(-int64(task.size))
	if !c.rpcClosed && c.rpcRunning < c.rpcMaxInflight && !c.rpcReady && c.hasRunnableInboundRPCLocked() {
		c.rpcReady = true
		reschedule = true
	}
	c.rpcMu.Unlock()
	reservation := task.budget
	release := task.release
	// The scheduler budget may be reused immediately after release. Clear request-owned
	// closures/context references first so slow metrics/rescheduling cannot overlap the old body
	// with a newly admitted body under the same byte accounting.
	task = inboundRPC{}
	reservation.release()
	if release != nil {
		release()
	}
	c.rpcWG.Done()
	if reschedule {
		c.rpcScheduler.schedule(c)
	}
}

// expireInboundRPCTicket removes a request that is still queued and returns its
// memory/task reservations immediately. If the worker won the dequeue race, the
// callback does nothing: the running handler owns the only terminal response and
// its deadline is represented solely by context cancellation.
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
		release := task.release
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
		if release != nil {
			release()
		}
		return
	}
}

// stopInboundRPCTask disarms queue-expiration cleanup before canceling the
// context. Once a worker dequeues the task, the deadline only cancels the
// handler; it never races the handler with an early RPC_TIMEOUT response.
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
// requests without waiting for handlers. Shutdown publishes this phase before transport.Close so
// a pathological/blocking transport implementation cannot leave the RPC admission gate open.
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
			method := task.method
			reservation := task.budget
			release := task.release
			stopInboundRPCTask(task)
			task = inboundRPC{}
			reservation.release()
			if release != nil {
				release()
			}
			c.metrics.InboundRPCDropped(method, "connection_closed")
		}
	})
}

// waitInboundShutdown waits for the pre-Copy reservation to complete its
// commit/abort, and for RPCs already dequeued on this connection to finish, both
// sharing one timeout. After the timeout the reservation/shared worker converge
// on their own when the underlying call finally returns; the connection root
// context has already been canceled.
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
