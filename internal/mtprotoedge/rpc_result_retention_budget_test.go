package mtprotoedge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/proto"
)

func TestRPCResultCloneReservationIsOneShotUnderReleaseRace(t *testing.T) {
	const iterations = 256
	for i := 0; i < iterations; i++ {
		encoded := encodedRPCResultForPriorityTest(int64(i+1), 0)
		budget := newOutboundTrackedBudget(int64(len(encoded.body)))
		if !budget.reserve(len(encoded.body)) {
			t.Fatal("reserve body")
		}
		reserved := &outboundBodyReservation{budget: budget, bytes: len(encoded.body)}
		start := make(chan struct{})
		taken := make(chan outboundOp, 1)
		go func() {
			<-start
			op, _ := reserved.take(encoded)
			taken <- op
		}()
		released := make(chan struct{})
		go func() {
			<-start
			reserved.release()
			close(released)
		}()
		close(start)
		op := <-taken
		<-released
		op.releaseReservation(budget)
		reserved.release()
		if got := budget.snapshot(); got != 0 {
			t.Fatalf("iteration %d retained bytes = %d, want 0", i, got)
		}
	}
}

func TestRPCResultReservationReleaseWinsAdmissionRollback(t *testing.T) {
	encoded := encodedRPCResultForPriorityTest(6999, 0)
	budget := newOutboundTrackedBudget(int64(len(encoded.body)))
	if !budget.reserve(len(encoded.body)) {
		t.Fatal("reserve body")
	}
	reserved := &outboundBodyReservation{budget: budget, bytes: len(encoded.body)}
	op, err := reserved.take(encoded)
	if err != nil {
		t.Fatalf("take reservation: %v", err)
	}
	// Model a watchdog that retires the queued owner just before queue admission
	// rolls the op back. The rollback must observe the release request and return
	// the raw op charge instead of resurrecting an owner nobody will release.
	reserved.release()
	if !reserved.reclaim(&op) {
		t.Fatal("reclaim actor reservation")
	}
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("retained bytes after watchdog/rollback race = %d, want 0", got)
	}
	reserved.release()
	op.releaseReservation(budget)
}

func TestCachedRPCResultReplayUsesPreReservedBodyWithoutDoubleCharge(t *testing.T) {
	encoded := encodedRPCResultForPriorityTest(7001, 32<<10)
	budget := newOutboundTrackedBudget(int64(len(encoded.body)))
	tr := newGatedRecordingTransport()
	c := newOutboundTestConn(t, tr, budget)
	s := New(Options{WriteTimeout: time.Second})

	done := make(chan error, 1)
	go func() {
		done <- s.sendCachedRPCResult(context.Background(), c, encoded)
	}()
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("cached replay did not reach the blocked physical write")
	}
	if got, want := budget.snapshot(), int64(len(encoded.body)); got != want {
		t.Fatalf("blocked replay retained bytes = %d, want exactly one body %d", got, want)
	}
	tr.once.Do(func() { close(tr.release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cached replay: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cached replay did not finish")
	}
	// rpc_result is reliable and remains charged once as resend state until ACK/close.
	if got, want := budget.snapshot(), int64(len(encoded.body)); got != want {
		t.Fatalf("pending replay retained bytes = %d, want %d", got, want)
	}
	c.Close()
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("replay retained bytes after close = %d, want 0", got)
	}
}

func TestQueuedRPCRewrapClonesAreBoundedBeforeAllocation(t *testing.T) {
	source := encodedRPCResultForPriorityTest(7101, 64<<10)
	perBody := len(source.body)
	budget := newOutboundTrackedBudget(int64(2 * perBody))
	c := &Conn{metrics: NopMetrics{}, outboundTrackedBudget: budget}

	first, firstReserved, err := c.cloneRPCResultForRequestReserved(source, 7102, false)
	if err != nil || first == nil || firstReserved == nil {
		t.Fatalf("first queued clone = %p reservation=%p err=%v", first, firstReserved, err)
	}
	firstAlias := &rpcRewrapAlias{bodyReservation: firstReserved}
	second, secondReserved, err := c.cloneRPCResultForRequestReserved(source, 7103, false)
	if err != nil || second == nil || secondReserved == nil {
		t.Fatalf("second queued clone = %p reservation=%p err=%v", second, secondReserved, err)
	}
	secondAlias := &rpcRewrapAlias{bodyReservation: secondReserved}
	if got, want := budget.snapshot(), int64(2*perBody); got != want {
		t.Fatalf("two queued aliases retained bytes = %d, want %d", got, want)
	}
	third, thirdReserved, err := c.cloneRPCResultForRequestReserved(source, 7104, false)
	if !errors.Is(err, ErrOutboundTrackedBudget) || third != nil || thirdReserved != nil {
		t.Fatalf("third queued clone = %p reservation=%p err=%v, want pre-allocation budget rejection", third, thirdReserved, err)
	}
	if got, want := budget.snapshot(), int64(2*perBody); got != want {
		t.Fatalf("budget changed after rejected clone = %d, want %d", got, want)
	}
	firstAlias.finishReplayRestoreWithoutDelivery()
	secondAlias.finishReplayRestoreWithoutDelivery()
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("queued alias bytes after terminal release = %d, want 0", got)
	}
}

func TestOutboundActorRetargetRequiresSecondBodyReservation(t *testing.T) {
	const (
		oldReqID = int64(7201)
		newReqID = int64(7202)
	)
	encoded := encodedRPCResultForPriorityTest(oldReqID, 32<<10)
	encoded.delivery = newRPCResultDelivery(oldReqID)
	encoded.markQueued()
	if !encoded.tryRetarget(newReqID) {
		t.Fatal("retarget prepared rpc_result")
	}
	budget := newOutboundTrackedBudget(int64(len(encoded.body)))
	if !budget.reserve(len(encoded.body)) {
		t.Fatal("reserve original queued body")
	}
	tr := &failAfterTransport{}
	c := newOutboundTestConn(t, tr, budget)
	state := newOutboundState(budget)
	var terminalErr error
	var terminalBytes int64
	c.handleOutboundSend(state, outboundOp{
		kind:              outboundSend,
		ctx:               context.Background(),
		msgType:           proto.MessageServerResponse,
		encoded:           encoded,
		reservedBytes:     len(encoded.body),
		reservationBudget: budget,
		enqueuedAt:        time.Now(),
		terminal: func(err error) {
			terminalErr = err
			terminalBytes = budget.snapshot()
		},
	})
	if !errors.Is(terminalErr, ErrOutboundTrackedBudget) {
		t.Fatalf("retarget terminal error = %v, want %v", terminalErr, ErrOutboundTrackedBudget)
	}
	if terminalBytes != int64(len(encoded.body)) {
		t.Fatalf("bytes visible to terminal = %d, want original body %d retained", terminalBytes, len(encoded.body))
	}
	if got := tr.sends.Load(); got != 0 {
		t.Fatalf("retarget under one-body budget wrote %d frames, want 0", got)
	}
	if got := int64(binary.LittleEndian.Uint64(encoded.body[4:12])); got != oldReqID {
		t.Fatalf("source req_msg_id mutated to %d before second-body admission", got)
	}
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("retarget bytes after terminal = %d, want 0", got)
	}
}

func TestOutboundActorRetargetTransfersOnlyReplacementToPending(t *testing.T) {
	const (
		oldReqID = int64(7301)
		newReqID = int64(7302)
	)
	encoded := encodedRPCResultForPriorityTest(oldReqID, 32<<10)
	encoded.delivery = newRPCResultDelivery(oldReqID)
	encoded.markQueued()
	if !encoded.tryRetarget(newReqID) {
		t.Fatal("retarget prepared rpc_result")
	}
	perBody := len(encoded.body)
	budget := newOutboundTrackedBudget(int64(2 * perBody))
	if !budget.reserve(perBody) {
		t.Fatal("reserve original queued body")
	}
	tr := &failAfterTransport{}
	c := newOutboundTestConn(t, tr, budget)
	state := newOutboundState(budget)
	var terminalErr error
	var terminalBytes int64
	c.handleOutboundSend(state, outboundOp{
		kind:              outboundSend,
		ctx:               context.Background(),
		msgType:           proto.MessageServerResponse,
		encoded:           encoded,
		reservedBytes:     perBody,
		reservationBudget: budget,
		enqueuedAt:        time.Now(),
		terminal: func(err error) {
			terminalErr = err
			terminalBytes = budget.snapshot()
		},
	})
	if terminalErr != nil {
		t.Fatalf("retarget terminal error: %v", terminalErr)
	}
	if terminalBytes != int64(2*perBody) {
		t.Fatalf("bytes visible to terminal = %d, want original+replacement %d", terminalBytes, 2*perBody)
	}
	if got := budget.snapshot(); got != int64(perBody) {
		t.Fatalf("bytes after terminal = %d, want one pending replacement %d", got, perBody)
	}
	data, err := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(c.key, &bin.Buffer{Buf: tr.lastFrame()})
	if err != nil {
		t.Fatalf("decrypt retargeted frame: %v", err)
	}
	var result proto.Result
	if err := result.Decode(&bin.Buffer{Buf: append([]byte(nil), data.Data()...)}); err != nil {
		t.Fatalf("decode retargeted rpc_result: %v", err)
	}
	if result.RequestMessageID != newReqID {
		t.Fatalf("wire req_msg_id = %d, want %d", result.RequestMessageID, newReqID)
	}
	state.releaseAll()
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("retarget bytes after pending release = %d, want 0", got)
	}
}
