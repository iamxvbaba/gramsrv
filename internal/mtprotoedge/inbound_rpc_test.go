package mtprotoedge

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func newInboundTestConn(s *inboundRPCScheduler, maxInflight, queueSize int, timeout time.Duration) *Conn {
	c := &Conn{metrics: NopMetrics{}}
	c.startInboundRPCScheduler(s, maxInflight, queueSize, timeout)
	return c
}

func TestInboundRPCSchedulerIsLazyPerConnectionAndServer(t *testing.T) {
	scheduler := newInboundRPCScheduler(4, 16, 1<<20)
	scheduler.start()
	c := newInboundTestConn(scheduler, 2, 4, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	if c.rpcQueue != nil {
		t.Fatal("new connection eagerly allocated an inbound queue")
	}
	scheduler.lifecycleMu.Lock()
	workersStarted := scheduler.workersStarted
	scheduler.lifecycleMu.Unlock()
	if workersStarted {
		t.Fatal("empty server eagerly started inbound RPC workers")
	}
}

func TestInboundRPCSchedulerBoundsConcurrentWork(t *testing.T) {
	scheduler := newInboundRPCScheduler(2, 32, 1<<20)
	scheduler.start()
	c := newInboundTestConn(scheduler, 2, 4, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	var active atomic.Int64
	var maxActive atomic.Int64
	var done atomic.Int64
	started := make(chan struct{}, 6)
	release := make(chan struct{})
	task := inboundRPC{
		method: "test.method",
		run: func(ctx context.Context) error {
			cur := active.Add(1)
			for {
				old := maxActive.Load()
				if cur <= old || maxActive.CompareAndSwap(old, cur) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
			}
			active.Add(-1)
			done.Add(1)
			return nil
		},
	}

	for i := 0; i < 2; i++ {
		if err := c.enqueueInboundRPC(context.Background(), task); err != nil {
			t.Fatalf("enqueue active task %d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for active rpc workers")
		}
	}
	for i := 0; i < 4; i++ {
		if err := c.enqueueInboundRPC(context.Background(), task); err != nil {
			t.Fatalf("enqueue queued task %d: %v", i, err)
		}
	}
	if err := c.enqueueInboundRPC(context.Background(), task); !errors.Is(err, ErrInboundRPCQueueFull) {
		t.Fatalf("enqueue over capacity err = %v, want ErrInboundRPCQueueFull", err)
	}
	if got := maxActive.Load(); got != 2 {
		t.Fatalf("max active = %d, want 2", got)
	}

	close(release)
	deadline := time.After(2 * time.Second)
	for done.Load() != 6 {
		select {
		case <-deadline:
			t.Fatalf("done = %d, want 6", done.Load())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("global budget after completion = (%d tasks, %d bytes), want zero", tasks, bytes)
	}
}

func TestInboundRPCSchedulerFairAcrossConnections(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 16, 1<<20)
	c1 := newInboundTestConn(scheduler, 1, 4, time.Second)
	c2 := newInboundTestConn(scheduler, 1, 4, time.Second)
	defer func() {
		c1.closeInboundRPCScheduler()
		c2.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	order := make(chan string, 3)
	enqueue := func(c *Conn, label string) {
		t.Helper()
		if err := c.enqueueInboundRPC(context.Background(), inboundRPC{
			method: label,
			run: func(context.Context) error {
				order <- label
				return nil
			},
		}); err != nil {
			t.Fatalf("enqueue %s: %v", label, err)
		}
	}

	// 先在 worker 启动前形成 [c1, c2] ready 顺序。c1 每次只执行一条后回到队尾，
	// 因此 c2 必须在 c1 的第二条之前获得执行机会。
	enqueue(c1, "c1-first")
	enqueue(c1, "c1-second")
	enqueue(c2, "c2-first")
	scheduler.start()

	want := []string{"c1-first", "c2-first", "c1-second"}
	for i := range want {
		select {
		case got := <-order:
			if got != want[i] {
				t.Fatalf("execution[%d] = %q, want %q", i, got, want[i])
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for execution[%d]", i)
		}
	}
}

func TestInboundRPCBudgetReservedBeforeCommitAndFullyReturned(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 2, 10)
	c1 := newInboundTestConn(scheduler, 1, 4, time.Second)
	c2 := newInboundTestConn(scheduler, 1, 4, time.Second)
	defer func() {
		c1.closeInboundRPCScheduler()
		c2.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	r1, err := c1.reserveInboundRPC(context.Background(), "one", 6)
	if err != nil {
		t.Fatalf("reserve first body: %v", err)
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 1 || bytes != 6 {
		t.Fatalf("budget after first pre-Copy reservation = (%d, %d), want (1, 6)", tasks, bytes)
	}
	if _, err := c2.reserveInboundRPC(context.Background(), "too-large", 5); !errors.Is(err, ErrInboundRPCQueueFull) {
		t.Fatalf("reserve over byte budget err = %v, want queue full", err)
	}
	r2, err := c2.reserveInboundRPC(context.Background(), "two", 4)
	if err != nil {
		t.Fatalf("reserve second body: %v", err)
	}
	if _, err := c1.reserveInboundRPC(context.Background(), "too-many", 0); !errors.Is(err, ErrInboundRPCQueueFull) {
		t.Fatalf("reserve over task budget err = %v, want queue full", err)
	}

	r1.abort()
	r2.abort()
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("budget after aborts = (%d, %d), want zero", tasks, bytes)
	}
	if got := c1.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("c1 inflight bytes = %d, want zero", got)
	}
	if got := c2.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("c2 inflight bytes = %d, want zero", got)
	}
}

func TestInboundRPCPerConnectionByteBudgetRejectedBeforeCommit(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 2, int64(maxInflightRPCBytes)+1)
	c := newInboundTestConn(scheduler, 1, 2, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	if _, err := c.reserveInboundRPC(context.Background(), "oversized", maxInflightRPCBytes+1); !errors.Is(err, ErrInboundRPCQueueFull) {
		t.Fatalf("reserve over per-connection byte budget err = %v, want queue full", err)
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("global budget after per-connection rejection = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection bytes after rejection = %d, want zero", got)
	}
}

func TestInboundRPCCommitRacingCloseReturnsReservation(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 4, 1<<20)
	c := newInboundTestConn(scheduler, 1, 2, time.Second)
	defer scheduler.stop(time.Second)

	reservation, err := c.reserveInboundRPC(context.Background(), "closing", 13)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	closed := make(chan struct{})
	go func() {
		c.closeInboundRPCScheduler()
		close(closed)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		c.rpcMu.Lock()
		isClosed := c.rpcClosed
		c.rpcMu.Unlock()
		if isClosed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connection scheduler was not marked closed")
		}
		time.Sleep(time.Millisecond)
	}

	if err := reservation.commit(inboundRPC{run: func(context.Context) error { return nil }}); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("commit after close err = %v, want ErrConnClosed", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not finish after reservation commit")
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("global budget after close/commit race = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection bytes after close/commit race = %d, want zero", got)
	}
}

func TestInboundRPCSchedulerCloseRemovesReadyTokenBeforeStart(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 1, 1<<20)
	defer scheduler.stop(time.Second)

	// A bounded ready channel used to retain one stale token per closed connection. With workers
	// not started yet, the second connection then blocked forever trying to publish its token even
	// though the first connection had returned every task/byte budget.
	for i := 0; i < 32; i++ {
		c := newInboundTestConn(scheduler, 1, 1, time.Second)
		done := make(chan error, 1)
		go func() {
			done <- c.enqueueInboundRPC(context.Background(), inboundRPC{
				method: "close-before-start",
				run:    func(context.Context) error { return nil },
			})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("enqueue iteration %d: %v", i, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("enqueue iteration %d blocked behind a stale ready token", i)
		}
		c.closeInboundRPCScheduler()
		if got := scheduler.readyLen(); got != 0 {
			t.Fatalf("ready tokens after close iteration %d = %d, want zero", i, got)
		}
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("budget after close churn = (%d, %d), want zero", tasks, bytes)
	}
}

func TestInboundRPCExpiredInQueueNeverRunsAndSignalsTimeout(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	scheduler.start()
	c := newInboundTestConn(scheduler, 1, 4, 40*time.Millisecond)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	started := make(chan struct{})
	release := make(chan struct{})
	if err := c.enqueueInboundRPC(context.Background(), inboundRPC{
		method: "blocker",
		size:   7,
		run: func(context.Context) error {
			close(started)
			<-release // 刻意忽略 deadline，确保下一条在队列中到期。
			return nil
		},
	}); err != nil {
		t.Fatalf("enqueue blocker: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocker did not start")
	}

	var ran atomic.Bool
	timedOut := make(chan struct{})
	if err := c.enqueueInboundRPC(context.Background(), inboundRPC{
		method: "expires",
		size:   11,
		onTimeout: func() {
			close(timedOut)
		},
		run: func(context.Context) error {
			ran.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("enqueue expiring task: %v", err)
	}
	select {
	case <-timedOut:
	case <-time.After(time.Second):
		t.Fatal("queued task did not signal timeout while the worker was still blocked")
	}
	deadline := time.Now().Add(time.Second)
	for {
		tasks, bytes := scheduler.budgetSnapshot()
		if tasks == 1 && bytes == 7 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("budget while blocker still runs = (%d, %d), want only blocker (1, 7)", tasks, bytes)
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	if ran.Load() {
		t.Fatal("expired queued task entered business handler")
	}

	deadline = time.Now().Add(time.Second)
	for {
		tasks, bytes := scheduler.budgetSnapshot()
		if tasks == 0 && bytes == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("budget after timeout = (%d, %d), want zero", tasks, bytes)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestInboundRPCCloseDisarmsQueuedTimeout(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	c := newInboundTestConn(scheduler, 1, 4, 30*time.Millisecond)
	defer scheduler.stop(time.Second)

	timedOut := make(chan struct{}, 1)
	if err := c.enqueueInboundRPC(context.Background(), inboundRPC{
		method: "queued",
		size:   11,
		onTimeout: func() {
			timedOut <- struct{}{}
		},
	}); err != nil {
		t.Fatalf("enqueue queued task: %v", err)
	}
	c.closeInboundRPCScheduler()
	time.Sleep(60 * time.Millisecond)
	select {
	case <-timedOut:
		t.Fatal("connection close emitted a queued RPC timeout")
	default:
	}
}

func TestInboundRPCRunningDeadlineCancelsWithoutEarlyTimeout(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	scheduler.start()
	c := newInboundTestConn(scheduler, 1, 4, 30*time.Millisecond)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	started := make(chan struct{})
	release := make(chan struct{})
	timedOut := make(chan struct{}, 1)
	if err := c.enqueueInboundRPC(context.Background(), inboundRPC{
		method: "running",
		size:   7,
		onTimeout: func() {
			timedOut <- struct{}{}
		},
		run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}); err != nil {
		t.Fatalf("enqueue running task: %v", err)
	}
	<-started
	time.Sleep(80 * time.Millisecond)
	select {
	case <-timedOut:
		t.Fatal("running task emitted an early timeout before handler convergence")
	default:
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 1 || bytes != 7 {
		t.Fatalf("running body budget after timeout = (%d, %d), want retained (1, 7)", tasks, bytes)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		tasks, bytes := scheduler.budgetSnapshot()
		if tasks == 0 && bytes == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("running body budget after completion = (%d, %d), want zero", tasks, bytes)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestInboundRPCCloseDrainsQueueAndReturnsBudgets(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	scheduler.start()
	c := newInboundTestConn(scheduler, 1, 4, time.Second)
	defer scheduler.stop(time.Second)

	started := make(chan struct{})
	if err := c.enqueueInboundRPC(context.Background(), inboundRPC{
		method: "running",
		size:   7,
		run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	}); err != nil {
		t.Fatalf("enqueue running task: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("running task did not start")
	}

	var queuedRan atomic.Bool
	if err := c.enqueueInboundRPC(context.Background(), inboundRPC{
		method: "queued",
		size:   11,
		run: func(context.Context) error {
			queuedRan.Store(true)
			return nil
		},
	}); err != nil {
		t.Fatalf("enqueue queued task: %v", err)
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 2 || bytes != 18 {
		t.Fatalf("budget before close = (%d, %d), want (2, 18)", tasks, bytes)
	}

	c.closeInboundRPCScheduler()
	if queuedRan.Load() {
		t.Fatal("queued task ran during connection close")
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("budget after close = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection inflight bytes after close = %d, want zero", got)
	}
}
