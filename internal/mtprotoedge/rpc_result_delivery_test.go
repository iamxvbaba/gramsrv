package mtprotoedge

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
)

type blockingCloseRPCResultTransport struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingCloseRPCResultTransport() *blockingCloseRPCResultTransport {
	return &blockingCloseRPCResultTransport{started: make(chan struct{}), release: make(chan struct{})}
}

func (*blockingCloseRPCResultTransport) Send(context.Context, *bin.Buffer) error { return nil }
func (*blockingCloseRPCResultTransport) Recv(context.Context, *bin.Buffer) error { return io.EOF }
func (t *blockingCloseRPCResultTransport) Close() error {
	t.once.Do(func() { close(t.started) })
	<-t.release
	return nil
}

func TestRPCResultCachePublishesOnlyAfterPhysicalWrite(t *testing.T) {
	tr := newGatedRequiredControlTransport(nil)
	s := New(Options{WriteTimeout: time.Second})
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74001, 1)
	t.Cleanup(c.ForceClose)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)

	owner, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("initial flight owner = %+v err=%v", owner, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- s.sendResult(context.Background(), c, reqMsgID, &tg.Config{ThisDC: 2})
	}()
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("rpc_result did not reach physical writer")
	}
	if _, ok := s.rpcResults.Get(key.ID, c.sessionID, reqMsgID); ok {
		t.Fatal("rpc_result became completed before physical write")
	}
	pending, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || pending.state != rpcResultAcquirePending || pending.waiter == nil {
		t.Fatalf("flight while write blocked = %+v err=%v", pending, err)
	}

	tr.unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sendResult after write: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sendResult did not finish")
	}
	if encoded, ok, err := pending.waiter.Wait(context.Background()); err != nil || !ok || encoded == nil {
		t.Fatalf("pending waiter after write = encoded:%p ok:%v err:%v", encoded, ok, err)
	}
	completed, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || completed.state != rpcResultAcquireCompleted || completed.encoded == nil {
		t.Fatalf("flight after write = %+v err=%v", completed, err)
	}
}

func TestRPCResultPrewriteFailureFencesConnBeforeCachePublication(t *testing.T) {
	tr := &collectingSessionTransport{}
	s := New(Options{WriteTimeout: 20 * time.Millisecond})
	// No rpc_result can reserve its conservative 3x wire scratch from one byte.
	// The actor therefore fails before touching the socket, which used to leave a
	// live Conn with a prematurely completed cache entry.
	s.outboundScratchPool = newOutboundScratchPool(1)
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74002, 1)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	owner, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("initial flight owner = %+v err=%v", owner, err)
	}

	err = s.sendResult(context.Background(), c, reqMsgID, &tg.Config{ThisDC: 2})
	if err == nil || (!errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, ErrConnClosed) && !errors.Is(err, ErrOutboundTrackedBudget)) {
		t.Fatalf("prewrite sendResult error = %v", err)
	}
	closeDeadline := time.Now().Add(time.Second)
	for !tr.closed.Load() && time.Now().Before(closeDeadline) {
		time.Sleep(time.Millisecond)
	}
	if !c.isRetired() || !tr.closed.Load() || c.isPhysicalTransportCurrentOpen() {
		t.Fatalf("failed delivery did not fence Conn: retired=%v closed=%v current_open=%v", c.isRetired(), tr.closed.Load(), c.isPhysicalTransportCurrentOpen())
	}
	completed, acquireErr := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if acquireErr != nil || completed.state != rpcResultAcquireCompleted || completed.encoded == nil {
		t.Fatalf("terminal replay cache = %+v err=%v", completed, acquireErr)
	}
	if got := s.rpcResults.flightLimit.snapshot(); got != 0 {
		t.Fatalf("failed delivery leaked flight slot: %d", got)
	}
	c.Close()
}

func TestRPCResultFailureAfterIntentionalTerminalDoesNotCloseTransferLease(t *testing.T) {
	tr := &collectingSessionTransport{}
	s := New(Options{WriteTimeout: time.Second})
	s.outboundTrackedBudget = newOutboundTrackedBudget(1)
	key := newTestAuthKey(t)
	oldConn := s.newConn(tr, key, 74003, 1)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	owner, err := s.rpcResults.Acquire(key.ID, oldConn.sessionID, reqMsgID)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("initial flight owner = %+v err=%v", owner, err)
	}

	// Session replacement publishes terminal before transferring the physical
	// lease. A late old-generation result sees ErrConnClosed at producer admission;
	// it may publish cache-only, but must not upgrade that intentional fence into
	// a physical close that makes Transfer fail.
	oldConn.beginTerminalShutdown()
	err = s.sendResult(context.Background(), oldConn, reqMsgID, &tg.Config{ThisDC: 2})
	if !errors.Is(err, ErrOutboundTrackedBudget) {
		t.Fatalf("late result error = %v, want ErrOutboundTrackedBudget", err)
	}
	if tr.closed.Load() {
		t.Fatal("late result closed intentionally transferable transport")
	}
	if !oldConn.waitOutboundShutdownUntil(time.Second) {
		t.Fatal("old outbound actor did not stop")
	}
	nextLease, ok := oldConn.transferTransportOwnership()
	if !ok || nextLease == nil {
		t.Fatal("late result prevented physical transfer")
	}
	newConn := s.newConnWithLease(nextLease, key, 74004, 1)
	oldConn.ForceClose()
	if tr.closed.Load() || !newConn.isPhysicalTransportCurrentOpen() {
		t.Fatalf("stale close after transfer: raw_closed=%v current_open=%v", tr.closed.Load(), newConn.isPhysicalTransportCurrentOpen())
	}
	completed, acquireErr := s.rpcResults.Acquire(key.ID, oldConn.sessionID, reqMsgID)
	if acquireErr != nil || completed.state != rpcResultAcquireCompleted || completed.encoded == nil {
		t.Fatalf("late result cache = %+v err=%v", completed, acquireErr)
	}
	newConn.ForceClose()
}

func TestRPCResultPublishesBeforePathologicalPhysicalCloseReturns(t *testing.T) {
	tr := newBlockingCloseRPCResultTransport()
	s := New(Options{WriteTimeout: time.Second})
	s.outboundTrackedBudget = newOutboundTrackedBudget(1)
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74005, 1)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	owner, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("initial flight owner = %+v err=%v", owner, err)
	}

	done := make(chan error, 1)
	go func() { done <- s.sendResult(context.Background(), c, reqMsgID, &tg.Config{ThisDC: 2}) }()
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("physical Close did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrOutboundTrackedBudget) {
			t.Fatalf("sendResult error = %v, want budget error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pathological physical Close blocked result publication")
	}
	completed, acquireErr := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if acquireErr != nil || completed.state != rpcResultAcquireCompleted || completed.encoded == nil {
		t.Fatalf("completed result before raw Close return = %+v err=%v", completed, acquireErr)
	}
	close(tr.release)
	if c.transportLease != nil {
		if err := c.transportLease.owner.waitClosed(); err != nil {
			t.Fatalf("physical Close: %v", err)
		}
	}
	c.Close()
}
