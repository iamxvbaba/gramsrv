package mtprotoedge

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"go.uber.org/zap/zaptest"
)

func TestContainerMessageCountAndServiceVectorCaps(t *testing.T) {
	var container bin.Buffer
	container.PutID(proto.MessageContainerTypeID)
	container.PutInt(maxContainerMessages)
	if got, err := containerMessageCount(&container); err != nil || got != maxContainerMessages {
		t.Fatalf("container count = %d/%v, want %d/nil", got, err, maxContainerMessages)
	}
	container.Buf[4]++
	if got, err := containerMessageCount(&container); err != nil || got != maxContainerMessages+1 {
		t.Fatalf("oversized container preflight = %d/%v, want %d/nil", got, err, maxContainerMessages+1)
	}

	ack := mt.MsgsAck{MsgIDs: make([]int64, maxServiceMessageIDs)}
	var encoded bin.Buffer
	if err := ack.Encode(&encoded); err != nil {
		t.Fatalf("encode msgs_ack: %v", err)
	}
	if err := validateFirstVectorCount(&encoded, maxServiceMessageIDs); err != nil {
		t.Fatalf("service vector at cap: %v", err)
	}
	// Count lives after constructor + vector constructor. We only mutate the declared count: the
	// preflight must reject before generated Decode attempts a long loop/allocation.
	encoded.Buf[8]++
	if err := validateFirstVectorCount(&encoded, maxServiceMessageIDs); err == nil {
		t.Fatal("service vector above cap unexpectedly accepted")
	}
}

func TestContainerDecodeUsesBudgetedZeroCopyBodies(t *testing.T) {
	encoded := bin.Buffer{}
	wantBody := []byte{0x11, 0x22, 0x33, 0x44}
	message := proto.Message{ID: 1, SeqNo: 1, Bytes: len(wantBody), Body: wantBody}
	if err := (&proto.MessageContainer{Messages: []proto.Message{message}}).Encode(&encoded); err != nil {
		t.Fatalf("encode container: %v", err)
	}

	s := New(Options{Logger: zaptest.NewLogger(t)})
	s.frameBudget = newInboundFrameBudget(containerDescriptorBudgetBytes - 1)
	if _, release, err := s.decodeMessageContainerViews(&encoded, 1); err == nil {
		release()
		t.Fatal("descriptor allocation unexpectedly bypassed process budget")
	}
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("failed descriptor reservation leaked %d bytes", got)
	}

	s.frameBudget = newInboundFrameBudget(2 * containerDescriptorBudgetBytes)
	container, release, err := s.decodeMessageContainerViews(&encoded, 1)
	if err != nil {
		t.Fatalf("decode budgeted container: %v", err)
	}
	if got := s.frameBudget.usedBytes(); got != containerDescriptorBudgetBytes {
		t.Fatalf("descriptor budget = %d, want %d", got, containerDescriptorBudgetBytes)
	}
	container.Messages[0].Body[0] = 0x99
	if encoded.Buf[8+16] != 0x99 {
		t.Fatal("container body was copied instead of viewing the charged input frame")
	}
	release()
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("released descriptor budget = %d, want zero", got)
	}

	truncated := bin.Buffer{Buf: encoded.Buf[:len(encoded.Buf)-1]}
	if _, _, err := s.decodeMessageContainerViews(&truncated, 1); err == nil {
		t.Fatal("truncated container unexpectedly decoded")
	}
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("failed container decode leaked %d bytes", got)
	}
}

func TestServiceInfoViewsRejectOversizedBytesWithoutDecodeCopy(t *testing.T) {
	state := mt.MsgsStateInfo{ReqMsgID: 7, Info: make([]byte, maxServiceMessageIDs)}
	var encodedState bin.Buffer
	if err := state.Encode(&encodedState); err != nil {
		t.Fatalf("encode msgs_state_info: %v", err)
	}
	reqMsgID, info, err := msgsStateInfoView(&encodedState)
	if err != nil || reqMsgID != state.ReqMsgID || len(info) != maxServiceMessageIDs {
		t.Fatalf("state info view = id %d len %d err %v", reqMsgID, len(info), err)
	}
	info[0] = 0x7f
	if encodedState.Buf[16] != 0x7f {
		t.Fatal("msgs_state_info view unexpectedly copied info")
	}

	state.Info = make([]byte, maxServiceMessageIDs+1)
	encodedState.Reset()
	if err := state.Encode(&encodedState); err != nil {
		t.Fatalf("encode oversized msgs_state_info: %v", err)
	}
	if _, _, err := msgsStateInfoView(&encodedState); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized msgs_state_info err = %v, want capped rejection", err)
	}

	all := mt.MsgsAllInfo{MsgIDs: []int64{1, 2}, Info: []byte{4, 4}}
	var encodedAll bin.Buffer
	if err := all.Encode(&encodedAll); err != nil {
		t.Fatalf("encode msgs_all_info: %v", err)
	}
	count, allInfo, err := msgsAllInfoView(&encodedAll)
	if err != nil || count != 2 || len(allInfo) != 2 {
		t.Fatalf("all info view = count %d len %d err %v", count, len(allInfo), err)
	}
	all.Info = make([]byte, maxServiceMessageIDs+1)
	encodedAll.Reset()
	if err := all.Encode(&encodedAll); err != nil {
		t.Fatalf("encode oversized msgs_all_info: %v", err)
	}
	if _, _, err := msgsAllInfoView(&encodedAll); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized msgs_all_info err = %v, want capped rejection", err)
	}
}

func TestDispatchRejectsExcessiveWrapperDepthBeforeRPC(t *testing.T) {
	var body bin.Buffer
	if err := (&mt.MsgsStateInfo{ReqMsgID: 1, Info: []byte{4}}).Encode(&body); err != nil {
		t.Fatalf("encode leaf: %v", err)
	}
	encoded := body.Copy()
	for i := 0; i < maxDispatchDepth+1; i++ {
		var wrapped bin.Buffer
		if err := (proto.GZIP{Data: encoded}).Encode(&wrapped); err != nil {
			t.Fatalf("encode gzip depth %d: %v", i+1, err)
		}
		encoded = wrapped.Copy()
	}

	s := New(Options{Logger: zaptest.NewLogger(t)})
	var acks []int64
	err := s.dispatch(context.Background(), newConnState(), nil, 4, 0, &bin.Buffer{Buf: encoded}, &acks)
	if err == nil || !strings.Contains(err.Error(), "wrapper depth") {
		t.Fatalf("deep wrapper err = %v, want wrapper depth rejection", err)
	}
}

func TestOversizedConnectionBuffersAreReleasedAfterFrame(t *testing.T) {
	inbound := &bin.Buffer{Buf: make([]byte, 1, maxRetainedConnBuffer+1)}
	trimOversizedInboundBuffer(inbound)
	if inbound.Buf != nil {
		t.Fatalf("oversized inbound buffer cap=%d, want released", cap(inbound.Buf))
	}
	regular := &bin.Buffer{Buf: make([]byte, 1, maxRetainedConnBuffer)}
	trimOversizedInboundBuffer(regular)
	if cap(regular.Buf) != maxRetainedConnBuffer {
		t.Fatalf("regular inbound buffer cap=%d, want retained", cap(regular.Buf))
	}

	pool := newOutboundScratchPool(16 << 20)
	scratch, err := pool.acquire(context.Background(), nil, maxRetainedConnBuffer+1)
	if err != nil {
		t.Fatalf("acquire oversized outbound scratch: %v", err)
	}
	pool.release(scratch)
	if got := pool.snapshot(); got != 0 {
		t.Fatalf("oversized outbound scratch retained %d bytes, want 0", got)
	}
}

func TestGZIPExpansionUsesProcessBudgetBeforeDecode(t *testing.T) {
	payload := make([]byte, 1<<20)
	var wrapped bin.Buffer
	if err := (proto.GZIP{Data: payload}).Encode(&wrapped); err != nil {
		t.Fatalf("encode gzip: %v", err)
	}

	s := New(Options{Logger: zaptest.NewLogger(t)})
	s.frameBudget = newInboundFrameBudget(maxSingleGZIPExpandedBytes - 1)
	if _, release, err := s.decodeGZIPWithGlobalBudget(&wrapped); err == nil {
		release()
		t.Fatal("gzip decode unexpectedly bypassed saturated process budget")
	}
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("failed gzip reservation leaked %d bytes", got)
	}

	s.frameBudget = newInboundFrameBudget(2 * maxSingleGZIPExpandedBytes)
	decoded, release, err := s.decodeGZIPWithGlobalBudget(&wrapped)
	if err != nil {
		t.Fatalf("budgeted gzip decode: %v", err)
	}
	if len(decoded) != len(payload) {
		t.Fatalf("decoded bytes = %d, want %d", len(decoded), len(payload))
	}
	if got := s.frameBudget.usedBytes(); got != int64(len(payload)) {
		t.Fatalf("held expansion budget = %d, want %d", got, len(payload))
	}
	release()
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("released expansion budget = %d, want zero", got)
	}
}
