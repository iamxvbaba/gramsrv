package mtprotoedge

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
)

type ownershipTestTransport struct {
	closeCalls atomic.Int32
	sendCalls  atomic.Int32
	closed     chan struct{}
	closeOnce  sync.Once
	blockSend  bool
	started    chan struct{}
	startOnce  sync.Once
}

func newOwnershipTestTransport(blockSend bool) *ownershipTestTransport {
	return &ownershipTestTransport{
		closed:    make(chan struct{}),
		blockSend: blockSend,
		started:   make(chan struct{}),
	}
}

func (t *ownershipTestTransport) Send(context.Context, *bin.Buffer) error {
	t.sendCalls.Add(1)
	t.startOnce.Do(func() { close(t.started) })
	if !t.blockSend {
		select {
		case <-t.closed:
			return io.ErrClosedPipe
		default:
			return nil
		}
	}
	<-t.closed
	return io.ErrClosedPipe
}

func (*ownershipTestTransport) Recv(context.Context, *bin.Buffer) error { return io.EOF }

func (t *ownershipTestTransport) Close() error {
	t.closeOnce.Do(func() {
		t.closeCalls.Add(1)
		close(t.closed)
	})
	return nil
}

func TestPhysicalTransportTransferFencesStaleLease(t *testing.T) {
	raw := newOwnershipTestTransport(false)
	owner, oldLease := newPhysicalTransportOwner(raw)
	newLease, ok := oldLease.Transfer()
	if !ok {
		t.Fatal("transfer failed")
	}
	if err := oldLease.Send(context.Background(), &bin.Buffer{}); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("stale Send error = %v, want ErrConnClosed", err)
	}
	if err := oldLease.Close(); err != nil {
		t.Fatalf("stale Close: %v", err)
	}
	if got := raw.closeCalls.Load(); got != 0 {
		t.Fatalf("stale Close closed raw %d times", got)
	}
	if err := newLease.Send(context.Background(), &bin.Buffer{}); err != nil {
		t.Fatalf("current Send: %v", err)
	}
	if err := owner.CloseAny(); err != nil {
		t.Fatalf("owner CloseAny: %v", err)
	}
	if got := raw.closeCalls.Load(); got != 1 {
		t.Fatalf("raw close calls = %d, want 1", got)
	}
}

func TestPhysicalTransportCloseWinningPreventsTransfer(t *testing.T) {
	raw := newOwnershipTestTransport(false)
	owner, lease := newPhysicalTransportOwner(raw)
	if err := owner.CloseAny(); err != nil {
		t.Fatalf("CloseAny: %v", err)
	}
	if next, ok := lease.Transfer(); ok || next != nil {
		t.Fatalf("transfer after close = (%p,%v), want nil,false", next, ok)
	}
}

func TestPhysicalTransportCloseAnyInterruptsWriteAndDefeatsTransfer(t *testing.T) {
	raw := newOwnershipTestTransport(true)
	owner, lease := newPhysicalTransportOwner(raw)
	sendDone := make(chan error, 1)
	go func() { sendDone <- lease.Send(context.Background(), &bin.Buffer{}) }()
	select {
	case <-raw.started:
	case <-time.After(time.Second):
		t.Fatal("write did not block")
	}
	transferDone := make(chan bool, 1)
	go func() {
		_, ok := lease.Transfer()
		transferDone <- ok
	}()
	select {
	case ok := <-transferDone:
		t.Fatalf("transfer returned before blocked write ended: %v", ok)
	case <-time.After(20 * time.Millisecond):
	}
	if err := owner.CloseAny(); err != nil {
		t.Fatalf("CloseAny: %v", err)
	}
	select {
	case err := <-sendDone:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("blocked Send error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseAny did not interrupt write")
	}
	select {
	case ok := <-transferDone:
		if ok {
			t.Fatal("transfer won after physical close")
		}
	case <-time.After(time.Second):
		t.Fatal("transfer did not finish")
	}
}

func TestPhysicalCloseFencesActivationPublication(t *testing.T) {
	raw := newOwnershipTestTransport(false)
	owner, lease := newPhysicalTransportOwner(raw)
	s := New(Options{})
	c := s.newConnWithLease(lease, newTestAuthKey(t), 73001, 1)
	if !c.beginActivationClaim() {
		t.Fatal("activation claim failed")
	}
	if err := owner.CloseAny(); err != nil {
		t.Fatalf("CloseAny: %v", err)
	}
	if c.publishActivation() {
		t.Fatal("closed physical transport published active Conn")
	}
	if !c.isRetired() {
		t.Fatalf("closed Conn lifecycle=%v", c.lifecycleState())
	}
	c.Close()
}

func TestPhysicalCloseBitPreventsActivationClaimBeforeLogicalFence(t *testing.T) {
	raw := newOwnershipTestTransport(false)
	owner, lease := newPhysicalTransportOwner(raw)
	s := New(Options{})
	c := s.newConnWithLease(lease, newTestAuthKey(t), 73002, 1)

	// Hold the binding lock so CloseAny can linearize the physical closed bit but
	// cannot yet retire the logical Conn. beginActivationClaim must inspect the lease
	// itself and refuse this otherwise-dangerous window.
	owner.bindingMu.Lock()
	closeDone := make(chan error, 1)
	go func() { closeDone <- owner.CloseAny() }()
	deadline := time.Now().Add(time.Second)
	for owner.state.Load()&physicalTransportClosedBit == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if owner.state.Load()&physicalTransportClosedBit == 0 {
		owner.bindingMu.Unlock()
		t.Fatal("CloseAny did not publish closed bit")
	}
	if c.isRetired() {
		owner.bindingMu.Unlock()
		t.Fatal("logical fence escaped held binding lock")
	}
	if c.beginActivationClaim() {
		owner.bindingMu.Unlock()
		t.Fatal("closed physical generation entered activation claim")
	}
	owner.bindingMu.Unlock()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("CloseAny: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CloseAny did not finish")
	}
	if !c.isRetired() {
		t.Fatalf("logical fence lifecycle=%v", c.lifecycleState())
	}
	c.Close()
}
