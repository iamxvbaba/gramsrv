package mtprotoedge

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
)

type gatedRequiredControlTransport struct {
	started chan struct{}
	release chan struct{}
	sendErr error

	startOnce sync.Once
	closeOnce sync.Once
	sends     atomic.Int32
	closes    atomic.Int32
}

func newGatedRequiredControlTransport(sendErr error) *gatedRequiredControlTransport {
	return &gatedRequiredControlTransport{
		started: make(chan struct{}),
		release: make(chan struct{}),
		sendErr: sendErr,
	}
}

func (t *gatedRequiredControlTransport) Send(context.Context, *bin.Buffer) error {
	t.sends.Add(1)
	t.startOnce.Do(func() { close(t.started) })
	<-t.release
	return t.sendErr
}

func (t *gatedRequiredControlTransport) Recv(context.Context, *bin.Buffer) error {
	return io.EOF
}

func (t *gatedRequiredControlTransport) Close() error {
	t.closes.Add(1)
	t.closeOnce.Do(func() { close(t.release) })
	return nil
}

func (t *gatedRequiredControlTransport) unblock() {
	t.closeOnce.Do(func() { close(t.release) })
}

func TestSendRequiredControlWaitsForPhysicalWriteAndReturnsBudget(t *testing.T) {
	tr := newGatedRequiredControlTransport(nil)
	controlBudget := newOutboundTrackedBudget(1 << 20)
	c := newOutboundTestConn(t, tr, newOutboundTrackedBudget(1<<20))
	c.outboundControlTrackedBudget = controlBudget

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- c.SendRequiredControl(ctx, proto.MessageServerResponse, &mt.Pong{MsgID: 1, PingID: 2})
	}()

	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("required control did not reach the physical writer")
	}
	select {
	case err := <-done:
		t.Fatalf("SendRequiredControl returned before physical write completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	tr.unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SendRequiredControl: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SendRequiredControl did not return after physical write")
	}
	if c.isRetired() {
		t.Fatal("successful required control terminally closed the connection")
	}
	if got := controlBudget.snapshot(); got != 0 {
		t.Fatalf("control budget after non-pending physical write = %d, want 0", got)
	}
	if got := tr.sends.Load(); got != 1 {
		t.Fatalf("physical sends = %d, want 1", got)
	}
}

func TestSendRequiredControlReturnsAfterWriteWithoutWaitingForAck(t *testing.T) {
	tr := &failAfterTransport{}
	controlBudget := newOutboundTrackedBudget(1 << 20)
	c := newOutboundTestConn(t, tr, newOutboundTrackedBudget(1<<20))
	c.outboundControlTrackedBudget = controlBudget
	created := &mt.NewSessionCreated{FirstMsgID: 1, UniqueID: 2, ServerSalt: 3}
	encoded, err := encodeOutboundMessageWithoutSlot(created)
	if err != nil {
		t.Fatalf("encode new_session_created: %v", err)
	}

	if err := c.SendRequiredControl(context.Background(), proto.MessageFromServer, created); err != nil {
		t.Fatalf("SendRequiredControl: %v", err)
	}
	if got := tr.stored.Load(); got != 1 {
		t.Fatalf("completed physical sends = %d, want 1", got)
	}
	if got := controlBudget.snapshot(); got != int64(len(encoded.body)) {
		t.Fatalf("pending control budget = %d, want %d until client ACK", got, len(encoded.body))
	}

	frame, err := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(c.key, &bin.Buffer{Buf: tr.lastFrame()})
	if err != nil {
		t.Fatalf("decrypt new_session_created: %v", err)
	}
	c.AckServerMessages([]int64{frame.MessageID})
	deadline := time.Now().Add(time.Second)
	for controlBudget.snapshot() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := controlBudget.snapshot(); got != 0 {
		t.Fatalf("control budget after ACK = %d, want 0", got)
	}
}

func TestSendRequiredControlQueueDeadlineTerminatesAndReturnsBudget(t *testing.T) {
	tr := &failAfterTransport{}
	controlBudget := newOutboundTrackedBudget(1 << 20)
	c := &Conn{
		transport:                    tr,
		writer:                       tr,
		metrics:                      NopMetrics{},
		writeTimeout:                 time.Second,
		outboundTrackedBudget:        newOutboundTrackedBudget(1 << 20),
		outboundControlTrackedBudget: controlBudget,
		outbound:                     make(chan outboundOp, 1),
		outboundControl:              make(chan outboundOp, 1),
		outboundStop:                 make(chan struct{}),
	}
	// No actor is running and the bounded control queue is full, so the parent
	// deadline must cover queue admission and make the failure terminal.
	c.outboundControl <- outboundOp{kind: outboundAck}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := c.SendRequiredControl(ctx, proto.MessageServerResponse, &mt.Pong{MsgID: 1, PingID: 2})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full control queue error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("full control queue waited %v, want parent-deadline-bounded admission", elapsed)
	}
	if !c.isRetired() {
		t.Fatal("required control queue failure did not terminally close the connection")
	}
	if got := tr.sends.Load(); got != 0 {
		t.Fatalf("physical sends = %d, want 0", got)
	}
	if got := tr.closes.Load(); got != 1 {
		t.Fatalf("transport closes = %d, want 1", got)
	}
	if got := controlBudget.snapshot(); got != 0 {
		t.Fatalf("control budget after queue timeout = %d, want 0", got)
	}
}

func TestSendRequiredControlBlockedWriteUsesWholeOperationDeadline(t *testing.T) {
	tr := newGatedRequiredControlTransport(io.ErrClosedPipe)
	controlBudget := newOutboundTrackedBudget(1 << 20)
	c := newOutboundTestConn(t, tr, newOutboundTrackedBudget(1<<20))
	c.outboundControlTrackedBudget = controlBudget
	c.writeTimeout = 25 * time.Millisecond

	started := time.Now()
	err := c.SendRequiredControl(context.Background(), proto.MessageServerResponse, &mt.Pong{MsgID: 1, PingID: 2})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked required control error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("blocked required control waited %v, want write-timeout-bounded operation", elapsed)
	}
	select {
	case <-c.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("outbound actor did not stop after required-control write timeout")
	}
	if !c.isRetired() {
		t.Fatal("blocked required control did not terminally close the connection")
	}
	if got := tr.closes.Load(); got != 1 {
		t.Fatalf("transport closes = %d, want 1", got)
	}
	if got := controlBudget.snapshot(); got != 0 {
		t.Fatalf("control budget after blocked write = %d, want 0", got)
	}
}

func TestSendRequiredControlWriteFailureTerminatesAndReturnsBudget(t *testing.T) {
	tr := &failAfterTransport{}
	tr.failAt.Store(1)
	controlBudget := newOutboundTrackedBudget(1 << 20)
	c := newOutboundTestConn(t, tr, newOutboundTrackedBudget(1<<20))
	c.outboundControlTrackedBudget = controlBudget

	err := c.SendRequiredControl(context.Background(), proto.MessageServerResponse, &mt.Pong{MsgID: 1, PingID: 2})
	if err == nil {
		t.Fatal("write-failed required control unexpectedly succeeded")
	}
	select {
	case <-c.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("outbound actor did not stop after required-control write failure")
	}
	if !c.isRetired() {
		t.Fatal("write-failed required control did not terminally close the connection")
	}
	if got := tr.closes.Load(); got != 1 {
		t.Fatalf("transport closes = %d, want 1", got)
	}
	if got := controlBudget.snapshot(); got != 0 {
		t.Fatalf("control budget after write failure = %d, want 0", got)
	}
}

func TestSendRequiredControlBudgetFailureIsTerminal(t *testing.T) {
	tr := &failAfterTransport{}
	controlBudget := newOutboundTrackedBudget(1)
	c := newOutboundTestConn(t, tr, newOutboundTrackedBudget(1<<20))
	c.outboundControlTrackedBudget = controlBudget

	err := c.SendRequiredControl(context.Background(), proto.MessageServerResponse, &mt.Pong{MsgID: 1, PingID: 2})
	if !errors.Is(err, ErrOutboundTrackedBudget) {
		t.Fatalf("required control over budget = %v, want ErrOutboundTrackedBudget", err)
	}
	select {
	case <-c.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("outbound actor did not stop after required-control budget failure")
	}
	if !c.isRetired() {
		t.Fatal("required-control budget failure did not terminally close the connection")
	}
	if got := tr.sends.Load(); got != 0 {
		t.Fatalf("budget-rejected required control wrote %d frames, want 0", got)
	}
	if got := controlBudget.snapshot(); got != 0 {
		t.Fatalf("control budget after reservation failure = %d, want 0", got)
	}
}
