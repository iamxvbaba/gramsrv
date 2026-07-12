package mtprotoedge

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInboundRPCBatchReservationRejectsGloballyWithoutPartialBudget(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 2, 10)
	c := newInboundTestConn(scheduler, 1, 8, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	_, err := c.reserveInboundRPCBatch(context.Background(), []inboundRPCSpec{
		{method: "one", size: 3},
		{method: "two", size: 3},
		{method: "three", size: 3},
	})
	if !errors.Is(err, ErrInboundRPCQueueFull) {
		t.Fatalf("reserve over global task budget err = %v, want ErrInboundRPCQueueFull", err)
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("global budget after atomic rejection = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection bytes after global rejection = %d, want zero", got)
	}
	c.rpcMu.Lock()
	reserved := c.rpcReserved
	queued := len(c.rpcQueue)
	c.rpcMu.Unlock()
	if reserved != 0 || queued != 0 {
		t.Fatalf("connection state after global rejection = reserved %d queued %d, want zero", reserved, queued)
	}
}

func TestInboundRPCBatchReservationRejectsConnectionWithoutLeakingGlobalBudget(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 16, 1<<20)
	c := newInboundTestConn(scheduler, 1, 2, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	_, err := c.reserveInboundRPCBatch(context.Background(), []inboundRPCSpec{
		{method: "one", size: 3},
		{method: "two", size: 5},
		{method: "three", size: 7},
	})
	if !errors.Is(err, ErrInboundRPCQueueFull) {
		t.Fatalf("reserve over connection queue budget err = %v, want ErrInboundRPCQueueFull", err)
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("global budget after connection rejection = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection bytes after connection rejection = %d, want zero", got)
	}
	c.rpcMu.Lock()
	reserved := c.rpcReserved
	queued := len(c.rpcQueue)
	c.rpcMu.Unlock()
	if reserved != 0 || queued != 0 {
		t.Fatalf("connection state after connection rejection = reserved %d queued %d, want zero", reserved, queued)
	}
}

func TestInboundRPCBatchReservationRejectsAggregateConnectionBytes(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, int64(maxInflightRPCBytes)*2)
	c := newInboundTestConn(scheduler, 1, 4, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	halfPlusOne := maxInflightRPCBytes/2 + 1
	_, err := c.reserveInboundRPCBatch(context.Background(), []inboundRPCSpec{
		{method: "one", size: halfPlusOne},
		{method: "two", size: halfPlusOne},
	})
	if !errors.Is(err, ErrInboundRPCQueueFull) {
		t.Fatalf("reserve over aggregate connection byte budget err = %v, want ErrInboundRPCQueueFull", err)
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("global budget after aggregate byte rejection = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection bytes after aggregate byte rejection = %d, want zero", got)
	}
}

func TestInboundRPCBatchAbortReturnsEveryReservationExactlyOnce(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	c := newInboundTestConn(scheduler, 1, 4, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	reservation, err := c.reserveInboundRPCBatch(context.Background(), []inboundRPCSpec{
		{method: "one", size: 3},
		{method: "two", size: 5},
		{method: "three", size: 7},
	})
	if err != nil {
		t.Fatalf("reserve batch: %v", err)
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 3 || bytes != 15 {
		t.Fatalf("budget after reserve = (%d, %d), want (3, 15)", tasks, bytes)
	}
	reservation.abort()
	reservation.abort()

	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("budget after idempotent abort = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection bytes after abort = %d, want zero", got)
	}
	c.rpcMu.Lock()
	reserved := c.rpcReserved
	queued := len(c.rpcQueue)
	c.rpcMu.Unlock()
	if reserved != 0 || queued != 0 {
		t.Fatalf("connection state after abort = reserved %d queued %d, want zero", reserved, queued)
	}
}

func TestInboundRPCBatchCommitAppendsAllAndSchedulesAtomically(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	c := newInboundTestConn(scheduler, 1, 4, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	specs := []inboundRPCSpec{
		{method: "one", size: 3},
		{method: "two", size: 5},
		{method: "three", size: 7},
	}
	reservation, err := c.reserveInboundRPCBatch(context.Background(), specs)
	if err != nil {
		t.Fatalf("reserve batch: %v", err)
	}
	defer reservation.abort()

	runs := make(chan string, len(specs))
	tasks := make([]inboundRPC, len(specs))
	for i, spec := range specs {
		method := spec.method
		tasks[i].run = func(context.Context) error {
			runs <- method
			return nil
		}
	}
	if err := reservation.commit(tasks); err != nil {
		t.Fatalf("commit batch: %v", err)
	}
	c.rpcMu.Lock()
	queued := len(c.rpcQueue)
	ready := c.rpcReady
	c.rpcMu.Unlock()
	if queued != len(specs) || !ready {
		t.Fatalf("atomic queue state after commit = queued %d ready %v, want %d/true", queued, ready, len(specs))
	}
	if got := scheduler.readyLen(); got != 1 {
		t.Fatalf("scheduler ready tokens after commit = %d, want one", got)
	}
	select {
	case method := <-runs:
		t.Fatalf("RPC %q ran before scheduler start", method)
	default:
	}

	scheduler.start()
	for _, want := range []string{"one", "two", "three"} {
		select {
		case got := <-runs:
			if got != want {
				t.Fatalf("execution order = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
	waitInboundRPCBatchBudget(t, scheduler, 0, 0)
}

func TestInboundRPCBatchCommitMismatchReleasesAllWithoutEnqueue(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	c := newInboundTestConn(scheduler, 1, 4, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	reservation, err := c.reserveInboundRPCBatch(context.Background(), []inboundRPCSpec{
		{method: "one", size: 3},
		{method: "two", size: 5},
	})
	if err != nil {
		t.Fatalf("reserve batch: %v", err)
	}
	if err := reservation.commit([]inboundRPC{{}}); !errors.Is(err, errInboundRPCBatchTaskCount) {
		t.Fatalf("commit task mismatch err = %v, want %v", err, errInboundRPCBatchTaskCount)
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("budget after mismatched commit = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection bytes after mismatched commit = %d, want zero", got)
	}
	c.rpcMu.Lock()
	reserved := c.rpcReserved
	queued := len(c.rpcQueue)
	c.rpcMu.Unlock()
	if reserved != 0 || queued != 0 {
		t.Fatalf("connection state after mismatched commit = reserved %d queued %d, want zero", reserved, queued)
	}
}

func TestInboundRPCBatchCommitRacingCloseNeverPartiallyEnqueues(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	c := newInboundTestConn(scheduler, 1, 4, time.Second)
	defer scheduler.stop(time.Second)

	reservation, err := c.reserveInboundRPCBatch(context.Background(), []inboundRPCSpec{
		{method: "one", size: 3},
		{method: "two", size: 5},
		{method: "three", size: 7},
	})
	if err != nil {
		t.Fatalf("reserve batch: %v", err)
	}
	closed := make(chan struct{})
	go func() {
		c.closeInboundRPCScheduler()
		close(closed)
	}()
	waitInboundRPCBatchConnClosed(t, c)

	if err := reservation.commit(make([]inboundRPC, 3)); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("commit after close err = %v, want ErrConnClosed", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close did not finish after batch commit returned its reservation")
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("budget after close/commit race = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection bytes after close/commit race = %d, want zero", got)
	}
	c.rpcMu.Lock()
	queued := len(c.rpcQueue)
	c.rpcMu.Unlock()
	if queued != 0 {
		t.Fatalf("queue after close/commit race = %d, want zero", queued)
	}
}

func TestInboundRPCBatchCommitAfterTerminalFenceRejectsAll(t *testing.T) {
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	c := newInboundTestConn(scheduler, 1, 4, time.Second)
	defer func() {
		c.closeInboundRPCScheduler()
		scheduler.stop(time.Second)
	}()

	reservation, err := c.reserveInboundRPCBatch(context.Background(), []inboundRPCSpec{
		{method: "one", size: 3},
		{method: "two", size: 5},
	})
	if err != nil {
		t.Fatalf("reserve batch: %v", err)
	}
	// Session replacement and revocation publish terminal before the slower
	// physical-close path. A reservation held across that fence must not be able
	// to append even one stale task.
	c.retire()
	if err := reservation.commit(make([]inboundRPC, 2)); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("commit after terminal fence err = %v, want ErrConnClosed", err)
	}

	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("budget after terminal commit rejection = (%d, %d), want zero", tasks, bytes)
	}
	if got := c.inflightRPCBytes.Load(); got != 0 {
		t.Fatalf("connection bytes after terminal commit rejection = %d, want zero", got)
	}
	c.rpcMu.Lock()
	reserved := c.rpcReserved
	queued := len(c.rpcQueue)
	c.rpcMu.Unlock()
	if reserved != 0 || queued != 0 {
		t.Fatalf("connection state after terminal commit rejection = reserved %d queued %d, want zero", reserved, queued)
	}
}

func waitInboundRPCBatchBudget(t *testing.T, scheduler *inboundRPCScheduler, wantTasks int, wantBytes int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		tasks, bytes := scheduler.budgetSnapshot()
		if tasks == wantTasks && bytes == wantBytes {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("budget = (%d, %d), want (%d, %d)", tasks, bytes, wantTasks, wantBytes)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitInboundRPCBatchConnClosed(t *testing.T, c *Conn) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		c.rpcMu.Lock()
		closed := c.rpcClosed
		c.rpcMu.Unlock()
		if closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("connection scheduler was not marked closed")
		}
		time.Sleep(time.Millisecond)
	}
}
