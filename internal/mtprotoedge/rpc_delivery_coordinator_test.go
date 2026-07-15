package mtprotoedge

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRPCDeliveryCoordinatorWaitsFromClaimedThroughDone(t *testing.T) {
	coordinator := newRPCResultDelivery(1).coordinator
	started := make(chan struct{})
	release := make(chan struct{})
	coordinator.setHook(func() {
		close(started)
		<-release
	})

	claim, err := coordinator.claimReplayDeliveredHook(context.Background(), false)
	if err != nil || claim == nil {
		t.Fatalf("initial hook claim = %p, err=%v", claim, err)
	}
	if got := coordinator.hookState(); got != rpcResultDeliveryHookClaimed {
		t.Fatalf("hook state after claim = %d, want claimed", got)
	}

	type claimResult struct {
		claim *rpcResultDeliveryHookClaim
		err   error
	}
	waited := make(chan claimResult, 1)
	go func() {
		other, waitErr := coordinator.claimReplayDeliveredHook(context.Background(), false)
		waited <- claimResult{claim: other, err: waitErr}
	}()
	select {
	case result := <-waited:
		t.Fatalf("second replay passed a claimed hook: claim=%p err=%v", result.claim, result.err)
	case <-time.After(20 * time.Millisecond):
	}

	go claim.run()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("claimed hook did not enter in-progress")
	}
	if got := coordinator.hookState(); got != rpcResultDeliveryHookInProgress {
		t.Fatalf("hook state while callback blocked = %d, want in-progress", got)
	}
	select {
	case result := <-waited:
		t.Fatalf("second replay passed an in-progress hook: claim=%p err=%v", result.claim, result.err)
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	select {
	case result := <-waited:
		if result.err != nil || result.claim != nil {
			t.Fatalf("wait after done = claim:%p err:%v", result.claim, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("completion did not notify the waiting replay")
	}
	if got := coordinator.hookState(); got != rpcResultDeliveryHookDone {
		t.Fatalf("terminal hook state = %d, want done", got)
	}
}

func TestRPCDeliveryCoordinatorAbandonedClaimCannotRunAfterReacquire(t *testing.T) {
	coordinator := newRPCResultDelivery(1).coordinator
	var calls atomic.Int32
	coordinator.setHook(func() { calls.Add(1) })

	stale, err := coordinator.claimReplayDeliveredHook(context.Background(), false)
	if err != nil || stale == nil || !stale.abandon() {
		t.Fatalf("abandon initial claim = %p err=%v", stale, err)
	}
	current, err := coordinator.claimReplayDeliveredHook(context.Background(), false)
	if err != nil || current == nil {
		t.Fatalf("reacquire hook = %p err=%v", current, err)
	}
	stale.run()
	if got := calls.Load(); got != 0 {
		t.Fatalf("stale claim calls = %d, want 0", got)
	}
	if got := coordinator.hookState(); got != rpcResultDeliveryHookClaimed {
		t.Fatalf("stale claim changed current state to %d", got)
	}
	current.run()
	if got := calls.Load(); got != 1 {
		t.Fatalf("current claim calls = %d, want 1", got)
	}
	if got := coordinator.hookState(); got != rpcResultDeliveryHookDone {
		t.Fatalf("terminal hook state = %d, want done", got)
	}
}

func TestBoundedRPCReplayRestoreDoesNotRetainWorkerOrBarrier(t *testing.T) {
	s := New(Options{})
	c := &Conn{metrics: NopMetrics{}}
	finishBarrier := c.beginRPCReplayRestore()

	coordinator := newRPCResultDelivery(1).coordinator
	var hookCalls atomic.Int32
	coordinator.setHook(func() { hookCalls.Add(1) })
	claim, err := coordinator.claimReplayDeliveredHook(context.Background(), false)
	if err != nil || claim == nil {
		t.Fatalf("claim hook = %p err=%v", claim, err)
	}

	replacementStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseReplacement:
		default:
			close(releaseReplacement)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	restoreResult := make(chan error, 1)
	go func() {
		restoreResult <- s.runBoundedRPCReplayRestore(ctx, c, "non-cooperative replacement", claim, func() error {
			close(replacementStarted)
			<-releaseReplacement // Deliberately ignores ctx.
			return nil
		})
	}()
	select {
	case <-replacementStarted:
	case <-time.After(time.Second):
		t.Fatal("replacement callback never started")
	}
	select {
	case err = <-restoreResult:
	case <-time.After(time.Second):
		t.Fatal("bounded restore retained its caller past the watchdog")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded restore error = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("non-cooperative restore retained caller for %v", elapsed)
	}
	finishBarrier()
	if !c.isRetired() {
		t.Fatal("timed-out restore did not fence its physical generation")
	}
	c.rpcMu.Lock()
	pendingBarriers := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if pendingBarriers != 0 {
		t.Fatalf("timed-out restore retained %d scheduler barriers", pendingBarriers)
	}
	if got := coordinator.hookState(); got != rpcResultDeliveryHookPending {
		t.Fatalf("not-yet-started timed-out hook state = %d, want pending", got)
	}

	// A replacement replay can now acquire the logical hook. The old blocked
	// goroutine holds a stale token and must not execute it when it eventually
	// returns.
	retry, err := coordinator.claimReplayDeliveredHook(context.Background(), false)
	if err != nil || retry == nil {
		t.Fatalf("retry hook claim = %p err=%v", retry, err)
	}
	retry.run()
	close(releaseReplacement)
	deadline := time.Now().Add(time.Second)
	for len(rpcReplayRestoreSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("logical hook calls after stale restore return = %d, want 1", got)
	}
}

func TestBoundedRPCReplayRestoreKeepsInProgressHookAtMostOnce(t *testing.T) {
	s := New(Options{})
	c := &Conn{metrics: NopMetrics{}}
	finishBarrier := c.beginRPCReplayRestore()

	coordinator := newRPCResultDelivery(1).coordinator
	hookStarted := make(chan struct{})
	releaseHook := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseHook:
		default:
			close(releaseHook)
		}
	})
	coordinator.setHook(func() {
		close(hookStarted)
		<-releaseHook // Deliberately ignores the restore deadline.
	})
	claim, err := coordinator.claimReplayDeliveredHook(context.Background(), false)
	if err != nil || claim == nil {
		t.Fatalf("claim hook = %p err=%v", claim, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	restoreResult := make(chan error, 1)
	go func() {
		restoreResult <- s.runBoundedRPCReplayRestore(ctx, c, "non-cooperative logical hook", claim, nil)
	}()
	select {
	case <-hookStarted:
	case <-time.After(time.Second):
		t.Fatal("logical hook never entered in-progress")
	}
	select {
	case err = <-restoreResult:
	case <-time.After(time.Second):
		t.Fatal("non-cooperative logical hook retained its caller past the watchdog")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded logical-hook error = %v, want deadline", err)
	}
	if got := coordinator.hookState(); got != rpcResultDeliveryHookInProgress {
		t.Fatalf("timed-out logical hook state = %d, want in-progress", got)
	}
	finishBarrier()
	if !c.isRetired() {
		t.Fatal("timed-out logical hook did not fence its physical generation")
	}
	c.rpcMu.Lock()
	pendingBarriers := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if pendingBarriers != 0 {
		t.Fatalf("timed-out logical hook retained %d scheduler barriers", pendingBarriers)
	}

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer waitCancel()
	if retry, retryErr := coordinator.claimReplayDeliveredHook(waitCtx, false); retry != nil ||
		!errors.Is(retryErr, context.DeadlineExceeded) {
		t.Fatalf("retry during in-progress hook = claim:%p err:%v", retry, retryErr)
	}
	close(releaseHook)
	deadline := time.Now().Add(time.Second)
	for coordinator.hookState() != rpcResultDeliveryHookDone && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := coordinator.hookState(); got != rpcResultDeliveryHookDone {
		t.Fatalf("released logical hook state = %d, want done", got)
	}
}

func TestCachedReplacementReplayBarrierWaitsForLogicalHookDone(t *testing.T) {
	s := New(Options{})
	const reqMsgID = int64(71001)
	encoded := encodedRPCResultForPriorityTest(reqMsgID, 0)
	encoded.delivery = newRPCResultDelivery(reqMsgID)

	var order atomic.Int32
	hookStarted := make(chan struct{})
	releaseHook := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseHook:
		default:
			close(releaseHook)
		}
	})
	encoded.setDeliveryHook(func() {
		close(hookStarted)
		<-releaseHook
		if !order.CompareAndSwap(1, 2) {
			panic("logical hook did not follow first replacement restore")
		}
	})

	firstConn := newOutboundTestConn(t, &collectingSessionTransport{}, newOutboundTrackedBudget(1<<20))
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- s.sendCachedRPCResultWithHook(context.Background(), firstConn, encoded, func() error {
			if !order.CompareAndSwap(0, 1) {
				return errors.New("first replacement restore ran out of order")
			}
			return nil
		})
	}()
	select {
	case <-hookStarted:
	case <-time.After(time.Second):
		t.Fatal("first replacement did not enter logical hook")
	}
	if got := encoded.delivery.coordinator.hookState(); got != rpcResultDeliveryHookInProgress {
		t.Fatalf("shared hook state = %d, want in-progress", got)
	}

	secondTransport := &collectingSessionTransport{}
	secondConn := newOutboundTestConn(t, secondTransport, newOutboundTrackedBudget(1<<20))
	secondReplacement := make(chan struct{})
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- s.sendCachedRPCResultWithHook(context.Background(), secondConn, encoded, func() error {
			if !order.CompareAndSwap(2, 3) {
				return errors.New("second replacement restore passed logical hook completion")
			}
			close(secondReplacement)
			return nil
		})
	}()
	deadline := time.Now().Add(time.Second)
	for len(secondTransport.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(secondTransport.snapshot()) == 0 {
		t.Fatal("second replacement did not physically deliver cached result")
	}
	secondConn.rpcMu.Lock()
	secondBarriers := secondConn.rpcReplayRestores
	secondConn.rpcMu.Unlock()
	if secondBarriers != 1 {
		t.Fatalf("second replacement barriers while hook in progress = %d, want 1", secondBarriers)
	}
	select {
	case err := <-secondResult:
		t.Fatalf("second replacement returned before hook done: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case <-secondReplacement:
		t.Fatal("second replacement metadata ran before shared hook done")
	default:
	}

	close(releaseHook)
	for name, result := range map[string]<-chan error{
		"first": firstResult, "second": secondResult,
	} {
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("%s replacement replay: %v", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s replacement replay did not finish", name)
		}
	}
	if got := order.Load(); got != 3 {
		t.Fatalf("replacement/logical terminal order = %d, want 3", got)
	}
	secondConn.rpcMu.Lock()
	secondBarriers = secondConn.rpcReplayRestores
	secondConn.rpcMu.Unlock()
	if secondBarriers != 0 {
		t.Fatalf("second replacement retained %d barriers after hook done", secondBarriers)
	}
}

func TestRPCRewrapWatchdogCoversCommittedRestoreWithoutBlockingTimerOrWorker(t *testing.T) {
	s := New(Options{})
	c := &Conn{metrics: NopMetrics{}}
	const reqMsgID = int64(72001)
	encoded := encodedRPCResultForPriorityTest(reqMsgID, 0)
	encoded.delivery = newRPCResultDelivery(reqMsgID)
	var logicalCalls atomic.Int32
	encoded.setDeliveryHook(func() { logicalCalls.Add(1) })

	replacementStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseReplacement:
		default:
			close(releaseReplacement)
		}
	})
	alias := &rpcRewrapAlias{
		conn: c, newReqID: reqMsgID, method: "help.getConfig",
		afterSuccessfulDelivery: func() error {
			close(replacementStarted)
			<-releaseReplacement // Deliberately ignores every deadline.
			return nil
		},
	}
	alias.executionOK.Store(true)
	alias.beginReplayRestore()

	var (
		once sync.Once
		jobs chan rpcRewrapDeliveryJob
	)
	workerDone := make(chan error, 1)
	watchdogDone := make(chan struct{})
	job := rpcRewrapDeliveryJob{
		deadline: time.Now().Add(80 * time.Millisecond),
		run: func(control *rpcRewrapDeliveryControl, _ time.Time) {
			if !control.commit() {
				workerDone <- errors.New("commit lost before restore")
				return
			}
			restoreCtx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			workerDone <- s.completeDeliveredRPCRewrapResult(
				restoreCtx, alias, encoded, "committed restore watchdog regression",
			)
		},
		fail: func(error) {
			c.fenceUndeliveredRPCResult()
			alias.releaseReplayRestoreBarrier()
			close(watchdogDone)
		},
	}
	if !scheduleRPCRewrapJob(job, &once, &jobs, 1, 2) {
		t.Fatal("schedule committed restore job")
	}
	select {
	case <-replacementStarted:
	case <-time.After(time.Second):
		t.Fatal("committed replacement restore never started")
	}
	select {
	case <-watchdogDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("committed-state watchdog did not return promptly")
	}
	if !c.isRetired() {
		t.Fatal("committed-state watchdog did not fence connection")
	}
	c.rpcMu.Lock()
	pendingBarriers := c.rpcReplayRestores
	c.rpcMu.Unlock()
	if pendingBarriers != 0 {
		t.Fatalf("watchdog retained %d scheduler barriers", pendingBarriers)
	}
	select {
	case err := <-workerDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("bounded committed restore error = %v, want deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("non-cooperative restore permanently retained rewrap worker")
	}

	followingRan := make(chan struct{})
	if !scheduleRPCRewrapJob(rpcRewrapDeliveryJob{
		deadline: time.Now().Add(time.Second),
		run:      func(*rpcRewrapDeliveryControl, time.Time) { close(followingRan) },
	}, &once, &jobs, 1, 2) {
		t.Fatal("schedule following rewrap job")
	}
	select {
	case <-followingRan:
	case <-time.After(time.Second):
		t.Fatal("rewrap worker did not accept following job after restore timeout")
	}
	if got := encoded.delivery.coordinator.hookState(); got != rpcResultDeliveryHookPending {
		t.Fatalf("timed-out pre-hook restore state = %d, want pending", got)
	}
	if got := logicalCalls.Load(); got != 0 {
		t.Fatalf("logical hook ran behind non-cooperative replacement = %d", got)
	}
	close(releaseReplacement)
	deadline := time.Now().Add(time.Second)
	for len(rpcReplayRestoreSlots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}
