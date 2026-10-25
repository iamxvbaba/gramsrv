package mtprotoedge

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func hookTestMessage(reqMsgID int64, coordinator *rpcResultDeliveryCoordinator, fn func()) *encodedOutboundMessage {
	msg := &encodedOutboundMessage{delivery: newRPCResultDelivery(reqMsgID, coordinator)}
	msg.setDeliveryHook(fn)
	return msg
}

func TestRPCDeliveryHookExecutorBoundsAdmissionWithoutBlockingDelivery(t *testing.T) {
	executor := newRPCDeliveryHookExecutor(1, 1)
	started := make(chan struct{})
	// The hook signals that it is running and then occupies the single worker
	// for a guaranteed 50ms. The sleep is what keeps the "completes" timing
	// honest: host monotonic clocks tick coarsely (a Windows VM in CI can
	// quantise back-to-back time.Now reads to the same tick), so a hook that
	// only waits on a channel a test closes microseconds later reliably
	// measures as 0ns duration and the snapshot assertion below would flake.
	first := hookTestMessage(1, nil, func() {
		close(started)
		time.Sleep(50 * time.Millisecond)
	})
	if err := first.prepareDeliveryHook(executor); err != nil {
		t.Fatalf("reserve first hook: %v", err)
	}
	delivered := make(chan struct{})
	go func() {
		first.markDelivered()
		close(delivered)
	}()
	select {
	case <-delivered:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("physical delivery blocked on hook execution")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first hook did not start")
	}

	second := hookTestMessage(2, nil, func() {})
	if err := second.prepareDeliveryHook(executor); !errors.Is(err, ErrRPCDeliveryHookCapacity) {
		t.Fatalf("second reservation = %v, want capacity error", err)
	}
	// The first hook is still running (its 50ms window has not elapsed yet, and
	// admission is what matters, not how long the worker is held). Wait for it
	// to finish and drain the capacity token.
	deadline := time.Now().Add(time.Second)
	for len(executor.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(executor.slots); got != 0 {
		t.Fatalf("executor retained %d capacity slots", got)
	}
	snapshot := executor.runtimeSnapshot()
	if snapshot.workers != 1 || snapshot.capacity != 1 || snapshot.completed != 1 || snapshot.rejected != 1 ||
		snapshot.reserved != 0 || snapshot.queued != 0 || snapshot.running != 0 || snapshot.durationSeconds < 0.05 {
		t.Fatalf("executor snapshot = %#v", snapshot)
	}
}

func TestRPCDeliveryHookExecutorIsolatesPanicsAndContinues(t *testing.T) {
	executor := newRPCDeliveryHookExecutor(1, 2)
	first := hookTestMessage(1, nil, func() { panic("hook boom") })
	done := make(chan struct{})
	second := hookTestMessage(2, nil, func() { close(done) })
	if err := first.prepareDeliveryHook(executor); err != nil {
		t.Fatalf("reserve panic hook: %v", err)
	}
	if err := second.prepareDeliveryHook(executor); err != nil {
		t.Fatalf("reserve following hook: %v", err)
	}
	first.markDelivered()
	second.markDelivered()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker stopped after a hook panic")
	}
	deadline := time.Now().Add(time.Second)
	for executor.panics.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := executor.panics.Load(); got != 1 {
		t.Fatalf("recorded hook panics = %d, want 1", got)
	}
}

func TestEquivalentRPCDeliveryAttemptsShareExactlyOnceCoordinator(t *testing.T) {
	executor := newRPCDeliveryHookExecutor(1, 2)
	var calls atomic.Int32
	first := hookTestMessage(11, nil, func() { calls.Add(1) })
	second := hookTestMessage(22, first.delivery.coordinator, nil)
	if err := first.prepareDeliveryHook(executor); err != nil {
		t.Fatalf("reserve first attempt: %v", err)
	}
	if err := second.prepareDeliveryHook(executor); err != nil {
		t.Fatalf("reserve equivalent attempt: %v", err)
	}
	first.markDelivered()
	second.markDelivered()
	deadline := time.Now().Add(time.Second)
	for calls.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("equivalent delivery hooks = %d, want 1", got)
	}
	deadline = time.Now().Add(time.Second)
	for len(executor.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(executor.slots); got != 0 {
		t.Fatalf("equivalent attempts leaked %d tickets", got)
	}
}

func TestRPCDeliveryHookExecutorStopRejectsNewAndDrainsReserved(t *testing.T) {
	executor := newRPCDeliveryHookExecutor(1, 2)
	ticket, ok := executor.reserve()
	if !ok {
		t.Fatal("reserve ticket")
	}
	stopped := make(chan bool, 1)
	go func() { stopped <- executor.stop(time.Second) }()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		executor.mu.Lock()
		stopping := executor.stopping
		executor.mu.Unlock()
		if stopping {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, ok := executor.reserve(); ok {
		t.Fatal("executor accepted a reservation after stop")
	}
	select {
	case <-stopped:
		t.Fatal("stop returned while a reserved ticket was still owned")
	default:
	}
	ticket.release()
	select {
	case ok := <-stopped:
		if !ok {
			t.Fatal("executor did not drain before timeout")
		}
	case <-time.After(time.Second):
		t.Fatal("executor stop did not finish after ticket release")
	}
	snapshot := executor.runtimeSnapshot()
	if snapshot.reserved != 0 || snapshot.queued != 0 || snapshot.running != 0 || snapshot.rejected < 1 {
		t.Fatalf("stopped executor snapshot = %#v", snapshot)
	}
}

func TestRPCDeliveryHookExecutorIsServerScoped(t *testing.T) {
	first := New(Options{RPCDeliveryHookWorkers: 2, RPCDeliveryHookMaxPending: 7})
	second := New(Options{RPCDeliveryHookWorkers: 3, RPCDeliveryHookMaxPending: 9})
	if first.rpcDeliveryHooks == nil || second.rpcDeliveryHooks == nil || first.rpcDeliveryHooks == second.rpcDeliveryHooks {
		t.Fatal("servers did not receive isolated delivery-hook executors")
	}
	firstSnapshot := first.RuntimeSnapshot()
	secondSnapshot := second.RuntimeSnapshot()
	if firstSnapshot.RPCDeliveryHookWorkers != 2 || firstSnapshot.RPCDeliveryHookCapacity != 7 ||
		secondSnapshot.RPCDeliveryHookWorkers != 3 || secondSnapshot.RPCDeliveryHookCapacity != 9 {
		t.Fatalf("server delivery-hook limits = %#v / %#v", firstSnapshot, secondSnapshot)
	}
}
