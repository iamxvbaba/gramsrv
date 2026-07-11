package mtprotoedge

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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

func TestServiceLongVectorViewsAreBoundedExactAndZeroCopy(t *testing.T) {
	tests := []struct {
		name   string
		typeID uint32
		value  bin.Encoder
	}{
		{name: "msgs_ack", typeID: mt.MsgsAckTypeID, value: &mt.MsgsAck{MsgIDs: []int64{11, 22}}},
		{name: "msgs_state_req", typeID: mt.MsgsStateReqTypeID, value: &mt.MsgsStateReq{MsgIDs: []int64{11, 22}}},
		{name: "msg_resend_req", typeID: mt.MsgResendReqTypeID, value: &mt.MsgResendReq{MsgIDs: []int64{11, 22}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var encoded bin.Buffer
			if err := tt.value.Encode(&encoded); err != nil {
				t.Fatalf("encode service vector: %v", err)
			}
			view, err := decodeInt64VectorView(&encoded, tt.typeID, maxServiceMessageIDs)
			if err != nil {
				t.Fatalf("decode service vector view: %v", err)
			}
			if view.count != 2 {
				t.Fatalf("view count = %d, want 2", view.count)
			}

			// The retained plan payload is a view into the already-budgeted frame,
			// not a generated-decoder []int64 copy.
			binary.LittleEndian.PutUint64(encoded.Buf[12:20], uint64(99))
			ids := view.materialize()
			if len(ids) != 2 || ids[0] != 99 || ids[1] != 22 {
				t.Fatalf("materialized ids = %v, want [99 22]", ids)
			}

			trailing := bin.Buffer{Buf: append(append([]byte(nil), encoded.Buf...), 0, 0, 0, 0)}
			if _, err := decodeInt64VectorView(&trailing, tt.typeID, maxServiceMessageIDs); err == nil || !strings.Contains(err.Error(), "trailing") {
				t.Fatalf("trailing vector err = %v, want exact-length rejection", err)
			}
			truncated := bin.Buffer{Buf: encoded.Buf[:len(encoded.Buf)-1]}
			if _, err := decodeInt64VectorView(&truncated, tt.typeID, maxServiceMessageIDs); !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("truncated vector err = %v, want unexpected EOF", err)
			}
		})
	}

	var oversized bin.Buffer
	if err := (&mt.MsgsAck{MsgIDs: []int64{1}}).Encode(&oversized); err != nil {
		t.Fatalf("encode oversized seed: %v", err)
	}
	binary.LittleEndian.PutUint32(oversized.Buf[8:12], uint32(maxServiceMessageIDs+1))
	if _, err := decodeInt64VectorView(&oversized, mt.MsgsAckTypeID, maxServiceMessageIDs); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized vector err = %v, want capped rejection", err)
	}
}

func TestInboundPlanContainerCapacityBoundary(t *testing.T) {
	ids := proto.NewMessageIDGen(time.Now)
	waitBody := encodeClientMessageBodyForTest(t, &mt.HTTPWaitRequest{MaxWait: 25_000})
	messages := make([]proto.Message, maxContainerMessages)
	for i := range messages {
		messages[i] = proto.Message{
			ID: ids.New(proto.MessageFromClient), SeqNo: 0, Bytes: len(waitBody), Body: waitBody,
		}
	}
	outerMsgID := ids.New(proto.MessageFromClient)
	body := encodeClientMessageBodyForTest(t, &proto.MessageContainer{Messages: messages})

	s := New(Options{Logger: zaptest.NewLogger(t)})
	before := s.frameBudget.usedBytes()
	plan, err := s.preflightInbound(newConnState(), outerMsgID, 0, body)
	if err != nil {
		t.Fatalf("container at cap preflight: %v", err)
	}
	if len(plan.items) != maxContainerMessages || len(plan.staged) != maxContainerMessages+1 {
		t.Fatalf("container plan sizes = items:%d staged:%d", len(plan.items), len(plan.staged))
	}
	if plan.logicalMin != messages[0].ID {
		t.Fatalf("container plan floor = %d, want %d", plan.logicalMin, messages[0].ID)
	}
	plan.close()
	if after := s.frameBudget.usedBytes(); after != before {
		t.Fatalf("container plan leaked frame budget: before=%d after=%d", before, after)
	}

	over := append(append([]proto.Message(nil), messages...), proto.Message{
		ID: outerMsgID, SeqNo: 0, Bytes: len(waitBody), Body: waitBody,
	})
	overBody := encodeClientMessageBodyForTest(t, &proto.MessageContainer{Messages: over})
	if plan, err := s.preflightInbound(newConnState(), ids.New(proto.MessageFromClient), 0, overBody); err == nil {
		plan.close()
		t.Fatal("container above cap unexpectedly accepted")
	}
	if after := s.frameBudget.usedBytes(); after != before {
		t.Fatalf("rejected container leaked frame budget: before=%d after=%d", before, after)
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

func TestContainerAndGZIPViewsRejectTrailingBytes(t *testing.T) {
	var container bin.Buffer
	if err := (&proto.MessageContainer{}).Encode(&container); err != nil {
		t.Fatalf("encode empty container: %v", err)
	}
	container.PutInt(0x11223344)
	s := New(Options{Logger: zaptest.NewLogger(t)})
	if _, release, err := s.decodeMessageContainerViews(&container, 0); err == nil {
		release()
		t.Fatal("container trailing bytes unexpectedly accepted")
	}

	var packed bin.Buffer
	if err := (proto.GZIP{Data: []byte{1, 2, 3, 4}}).Encode(&packed); err != nil {
		t.Fatalf("encode gzip wrapper: %v", err)
	}
	packed.PutInt(0x55667788)
	if _, err := gzipPackedBytesView(&packed); err == nil {
		t.Fatal("gzip trailing bytes unexpectedly accepted")
	}
}

func TestContainerInvalidSequenceTailIsAtomic(t *testing.T) {
	ids := proto.NewMessageIDGen(time.Now)
	firstMsgID := ids.New(proto.MessageFromClient)
	secondMsgID := ids.New(proto.MessageFromClient)
	outerMsgID := ids.New(proto.MessageFromClient)
	pingOne := encodeClientMessageBodyForTest(t, &mt.PingRequest{PingID: 1})
	pingTwo := encodeClientMessageBodyForTest(t, &mt.PingRequest{PingID: 2})
	container := proto.MessageContainer{Messages: []proto.Message{
		{ID: firstMsgID, SeqNo: 3, Bytes: len(pingOne), Body: pingOne},
		{ID: secondMsgID, SeqNo: 1, Bytes: len(pingTwo), Body: pingTwo},
	}}
	encoded := encodeClientMessageBodyForTest(t, &container)

	s := New(Options{Logger: zaptest.NewLogger(t)})
	cs := newConnState()
	c := &Conn{
		metrics:         NopMetrics{},
		outbound:        make(chan outboundOp, 4),
		outboundControl: make(chan outboundOp, 4),
		outboundStop:    make(chan struct{}),
	}
	var acks []int64
	if err := s.dispatch(context.Background(), cs, c, outerMsgID, 4, &bin.Buffer{Buf: encoded}, &acks); err != nil {
		t.Fatalf("dispatch invalid-tail container: %v", err)
	}

	if len(cs.seen) != 0 || len(cs.order) != 0 || cs.maxContentMsgID != 0 || cs.maxContentSeqNo != 0 {
		t.Fatalf("invalid container partially committed connState: %+v", cs)
	}
	if len(acks) != 0 {
		t.Fatalf("invalid container produced ACKs: %v", acks)
	}
	if got := len(c.outboundControl); got != 1 {
		t.Fatalf("control frames = %d, want only bad_msg_notification", got)
	}
	op := <-c.outboundControl
	if op.encoded == nil || op.encoded.typeID != mt.BadMsgNotificationTypeID {
		t.Fatalf("control frame type = %#v, want bad_msg_notification", op.encoded)
	}
	var bad mt.BadMsgNotification
	if err := bad.Decode(&bin.Buffer{Buf: op.encoded.body}); err != nil {
		t.Fatalf("decode bad_msg_notification: %v", err)
	}
	if bad.BadMsgID != secondMsgID || bad.BadMsgSeqno != 1 || bad.ErrorCode != badMsgSeqTooLow {
		t.Fatalf("bad_msg_notification = %+v", bad)
	}
	op.releaseReservation(c.outboundTrackedBudget)
}

func TestContainerMalformedGZIPTailHasNoStateEffects(t *testing.T) {
	ids := proto.NewMessageIDGen(time.Now)
	waitMsgID := ids.New(proto.MessageFromClient)
	badMsgID := ids.New(proto.MessageFromClient)
	outerMsgID := ids.New(proto.MessageFromClient)
	waitBody := encodeClientMessageBodyForTest(t, &mt.HTTPWaitRequest{MaxWait: 25_000})
	var malformedGZIP bin.Buffer
	malformedGZIP.PutID(proto.GZIPTypeID)
	container := proto.MessageContainer{Messages: []proto.Message{
		{ID: waitMsgID, SeqNo: 0, Bytes: len(waitBody), Body: waitBody},
		{ID: badMsgID, SeqNo: 1, Bytes: malformedGZIP.Len(), Body: malformedGZIP.Buf},
	}}
	encoded := encodeClientMessageBodyForTest(t, &container)

	s := New(Options{Logger: zaptest.NewLogger(t)})
	cs := newConnState()
	var acks []int64
	if err := s.dispatch(context.Background(), cs, nil, outerMsgID, 2, &bin.Buffer{Buf: encoded}, &acks); err == nil {
		t.Fatal("malformed gzip tail unexpectedly accepted")
	}
	if len(cs.seen) != 0 || len(cs.order) != 0 || cs.maxContentMsgID != 0 || cs.maxContentSeqNo != 0 {
		t.Fatalf("malformed gzip tail partially committed connState: %+v", cs)
	}
	if len(acks) != 0 {
		t.Fatalf("malformed gzip tail produced ACKs: %v", acks)
	}
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("malformed gzip tail leaked frame budget: %d", got)
	}
}

func TestInvalidMessageIDRejectsBeforeGZIPExpansion(t *testing.T) {
	leaf := encodeClientMessageBodyForTest(t, &mt.PingRequest{PingID: 1})
	wrapped := encodeClientMessageBodyForTest(t, &proto.GZIP{Data: leaf})
	current := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	tests := []struct {
		name    string
		msgID   int64
		badCode int
	}{
		{name: "stale", msgID: proto.NewMessageIDGen(func() time.Time { return time.Now().Add(-10 * time.Minute) }).New(proto.MessageFromClient), badCode: badMsgIDTooLow},
		{name: "future", msgID: proto.NewMessageIDGen(func() time.Time { return time.Now().Add(time.Minute) }).New(proto.MessageFromClient), badCode: badMsgIDTooHigh},
		{name: "invalid bits", msgID: current + 1, badCode: badMsgIDInvalidBits},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(Options{Logger: zaptest.NewLogger(t)})
			// Any attempted gzip expansion would fail this deliberately tiny
			// budget and mask the required bad_msg_notification.
			s.frameBudget = newInboundFrameBudget(1)
			plan, err := s.preflightInbound(newConnState(), tt.msgID, 1, wrapped)
			if plan != nil {
				plan.close()
				t.Fatal("invalid gzip envelope unexpectedly produced a plan")
			}
			var bad *dispatchBadMsgError
			if !errors.As(err, &bad) || bad.code != tt.badCode {
				t.Fatalf("invalid gzip envelope err = %v, want bad_msg code %d", err, tt.badCode)
			}
			if got := s.frameBudget.usedBytes(); got != 0 {
				t.Fatalf("invalid gzip envelope consumed expansion budget: %d", got)
			}
		})
	}
}

func TestInvalidContainerInnerIDRejectsBeforeGZIPExpansion(t *testing.T) {
	ids := proto.NewMessageIDGen(time.Now)
	validInnerID := ids.New(proto.MessageFromClient)
	invalidInnerID := validInnerID + 1
	if proto.MessageID(invalidInnerID).Type() == proto.MessageFromClient {
		t.Fatalf("test inner msg_id %d unexpectedly has client bits", invalidInnerID)
	}
	outerMsgID := ids.New(proto.MessageFromClient)
	leaf := encodeClientMessageBodyForTest(t, &mt.PingRequest{PingID: 2})
	gzipped := encodeClientMessageBodyForTest(t, &proto.GZIP{Data: leaf})
	container := encodeClientMessageBodyForTest(t, &proto.MessageContainer{Messages: []proto.Message{{
		ID: invalidInnerID, SeqNo: 1, Bytes: len(gzipped), Body: gzipped,
	}}})

	s := New(Options{Logger: zaptest.NewLogger(t)})
	// Enough for the one descriptor, intentionally not enough for a gzip
	// expansion. Invalid inner bits must win and every descriptor charge unwind.
	s.frameBudget = newInboundFrameBudget(containerDescriptorBudgetBytes)
	plan, err := s.preflightInbound(newConnState(), outerMsgID, 2, container)
	if plan != nil {
		plan.close()
		t.Fatal("invalid inner msg_id unexpectedly produced a plan")
	}
	var bad *dispatchBadMsgError
	if !errors.As(err, &bad) || bad.code != badMsgContainer || bad.msgID != invalidInnerID {
		t.Fatalf("invalid inner gzip err = %v, want container rejection for %d", err, invalidInnerID)
	}
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("invalid inner gzip leaked descriptor/expansion budget: %d", got)
	}
}

func TestSeenGZIPInnerShortCircuitsBeforeExpansion(t *testing.T) {
	tests := []struct {
		name    string
		seqNo   int32
		content bool
		leaf    bin.Encoder
		wantACK bool
	}{
		{name: "content", seqNo: 1, content: true, leaf: &mt.PingRequest{PingID: 3}, wantACK: true},
		{name: "non_content", seqNo: 0, content: false, leaf: &mt.HTTPWaitRequest{MaxWait: 25_000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids := proto.NewMessageIDGen(time.Now)
			innerMsgID := ids.New(proto.MessageFromClient)
			outerMsgID := ids.New(proto.MessageFromClient)
			leaf := encodeClientMessageBodyForTest(t, tt.leaf)
			gzipped := encodeClientMessageBodyForTest(t, &proto.GZIP{Data: leaf})
			container := encodeClientMessageBodyForTest(t, &proto.MessageContainer{Messages: []proto.Message{{
				ID: innerMsgID, SeqNo: int(tt.seqNo), Bytes: len(gzipped), Body: gzipped,
			}}})

			cs := newConnState()
			cs.track(innerMsgID, tt.seqNo, tt.content, msgStateReceived)
			s := New(Options{Logger: zaptest.NewLogger(t)})
			s.frameBudget = newInboundFrameBudget(containerDescriptorBudgetBytes)
			plan, err := s.preflightInbound(cs, outerMsgID, 2, container)
			if err != nil {
				t.Fatalf("preflight seen gzip inner: %v", err)
			}
			if len(plan.items) != 1 || plan.items[0].kind != inboundItemDuplicate || plan.items[0].content != tt.content {
				plan.close()
				t.Fatalf("seen gzip plan items = %+v, want one duplicate", plan.items)
			}
			if gotACK := len(plan.ackIDs) == 1 && plan.ackIDs[0] == innerMsgID; gotACK != tt.wantACK {
				plan.close()
				t.Fatalf("seen gzip ACKs = %v, wantACK=%v", plan.ackIDs, tt.wantACK)
			}
			if got := s.frameBudget.usedBytes(); got != containerDescriptorBudgetBytes {
				plan.close()
				t.Fatalf("seen gzip held budget = %d, want descriptor-only %d", got, containerDescriptorBudgetBytes)
			}
			plan.close()
			if got := s.frameBudget.usedBytes(); got != 0 {
				t.Fatalf("seen gzip close leaked budget: %d", got)
			}
		})
	}
}

func TestSeenTopLevelContentGZIPShortCircuitsBeforeExpansion(t *testing.T) {
	msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	leaf := encodeClientMessageBodyForTest(t, &mt.PingRequest{PingID: 31})
	wrapped := encodeClientMessageBodyForTest(t, &proto.GZIP{Data: leaf})
	cs := newConnState()
	cs.track(msgID, 1, true, msgStateReceived)

	s := New(Options{Logger: zaptest.NewLogger(t)})
	s.frameBudget = newInboundFrameBudget(1)
	plan, err := s.preflightInbound(cs, msgID, 1, wrapped)
	if err != nil {
		t.Fatalf("preflight seen top-level gzip: %v", err)
	}
	if len(plan.items) != 1 || plan.items[0].kind != inboundItemDuplicate || len(plan.ackIDs) != 1 || plan.ackIDs[0] != msgID {
		plan.close()
		t.Fatalf("seen top-level gzip plan = items:%+v acks:%v", plan.items, plan.ackIDs)
	}
	plan.close()
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("seen top-level gzip consumed expansion budget: %d", got)
	}
}

func TestDuplicateOuterContainerOnlyInspectsInnerDescriptors(t *testing.T) {
	ids := proto.NewMessageIDGen(time.Now)
	innerMsgID := ids.New(proto.MessageFromClient)
	outerMsgID := ids.New(proto.MessageFromClient)
	leaf := encodeClientMessageBodyForTest(t, &mt.PingRequest{PingID: 4})
	gzipped := encodeClientMessageBodyForTest(t, &proto.GZIP{Data: leaf})
	container := encodeClientMessageBodyForTest(t, &proto.MessageContainer{Messages: []proto.Message{{
		ID: innerMsgID, SeqNo: 1, Bytes: len(gzipped), Body: gzipped,
	}}})
	wrappedContainer := encodeClientMessageBodyForTest(t, &proto.GZIP{Data: container})

	t.Run("seen inner replays without expansion", func(t *testing.T) {
		cs := newConnState()
		cs.track(outerMsgID, 2, false, msgStateReceived)
		cs.track(innerMsgID, 1, true, msgStateReceived)
		s := New(Options{Logger: zaptest.NewLogger(t)})
		s.frameBudget = newInboundFrameBudget(containerDescriptorBudgetBytes)
		plan, err := s.preflightInbound(cs, outerMsgID, 2, container)
		if err != nil {
			t.Fatalf("preflight duplicate outer: %v", err)
		}
		if len(plan.items) != 1 || plan.items[0].kind != inboundItemDuplicate {
			plan.close()
			t.Fatalf("duplicate outer items = %+v", plan.items)
		}
		plan.close()
		if got := s.frameBudget.usedBytes(); got != 0 {
			t.Fatalf("duplicate outer leaked descriptor budget: %d", got)
		}
	})

	t.Run("top-level gzip container still locates inner", func(t *testing.T) {
		cs := newConnState()
		cs.track(outerMsgID, 2, false, msgStateReceived)
		cs.track(innerMsgID, 1, true, msgStateReceived)
		s := New(Options{Logger: zaptest.NewLogger(t)})
		s.frameBudget = newInboundFrameBudget(maxSingleGZIPExpandedBytes + containerDescriptorBudgetBytes)
		plan, err := s.preflightInbound(cs, outerMsgID, 2, wrappedContainer)
		if err != nil {
			t.Fatalf("preflight gzip duplicate outer: %v", err)
		}
		if len(plan.items) != 1 || plan.items[0].kind != inboundItemDuplicate || plan.items[0].msgID != innerMsgID {
			plan.close()
			t.Fatalf("gzip duplicate outer items = %+v, want inner duplicate", plan.items)
		}
		plan.close()
		if got := s.frameBudget.usedBytes(); got != 0 {
			t.Fatalf("gzip duplicate outer leaked expansion/descriptor budget: %d", got)
		}
	})

	t.Run("unseen inner rejects without expansion", func(t *testing.T) {
		cs := newConnState()
		cs.track(outerMsgID, 2, false, msgStateReceived)
		s := New(Options{Logger: zaptest.NewLogger(t)})
		s.frameBudget = newInboundFrameBudget(containerDescriptorBudgetBytes)
		plan, err := s.preflightInbound(cs, outerMsgID, 2, container)
		if plan != nil {
			plan.close()
			t.Fatal("duplicate outer with unseen inner unexpectedly produced a plan")
		}
		var bad *dispatchBadMsgError
		if !errors.As(err, &bad) || bad.code != badMsgContainer {
			t.Fatalf("duplicate outer unseen-inner err = %v, want container rejection", err)
		}
		if got := s.frameBudget.usedBytes(); got != 0 {
			t.Fatalf("unseen duplicate inner leaked descriptor/expansion budget: %d", got)
		}
	})
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
	msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	err := s.dispatch(context.Background(), newConnState(), nil, msgID, 0, &bin.Buffer{Buf: encoded}, &acks)
	if err == nil || !strings.Contains(err.Error(), "wrapper depth") {
		t.Fatalf("deep wrapper err = %v, want wrapper depth rejection", err)
	}
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("deep wrapper rejection leaked frame budget: %d", got)
	}
}

func TestNestedWrapperTailFailuresReleaseEveryBudget(t *testing.T) {
	ping := encodeClientMessageBodyForTest(t, &mt.PingRequest{PingID: 5})

	var gzipWithTail bin.Buffer
	if err := (&proto.GZIP{Data: ping}).Encode(&gzipWithTail); err != nil {
		t.Fatalf("encode inner gzip: %v", err)
	}
	gzipWithTail.PutInt(0x11223344)
	gzipTailWrapped := encodeClientMessageBodyForTest(t, &proto.GZIP{Data: gzipWithTail.Copy()})

	var containerWithTail bin.Buffer
	if err := (&proto.MessageContainer{}).Encode(&containerWithTail); err != nil {
		t.Fatalf("encode inner container: %v", err)
	}
	containerWithTail.PutInt(0x55667788)
	containerTailWrapped := encodeClientMessageBodyForTest(t, &proto.GZIP{Data: containerWithTail.Copy()})

	tests := []struct {
		name  string
		body  []byte
		seqNo int32
	}{
		{name: "gzip tail after outer expansion", body: gzipTailWrapped, seqNo: 1},
		{name: "container tail after outer expansion", body: containerTailWrapped, seqNo: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(Options{Logger: zaptest.NewLogger(t)})
			msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
			plan, err := s.preflightInbound(newConnState(), msgID, tt.seqNo, tt.body)
			if plan != nil {
				plan.close()
				t.Fatal("invalid nested wrapper unexpectedly produced a plan")
			}
			if err == nil || !strings.Contains(err.Error(), "trailing") {
				t.Fatalf("nested wrapper err = %v, want trailing-byte rejection", err)
			}
			if got := s.frameBudget.usedBytes(); got != 0 {
				t.Fatalf("nested wrapper failure leaked frame budget: %d", got)
			}
		})
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
