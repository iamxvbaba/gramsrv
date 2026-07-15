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
	release := make(chan struct{})
	first := hookTestMessage(1, nil, func() {
		close(started)
		<-release
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
	close(release)
	deadline := time.Now().Add(time.Second)
	for len(executor.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(executor.slots); got != 0 {
		t.Fatalf("executor retained %d capacity slots", got)
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
