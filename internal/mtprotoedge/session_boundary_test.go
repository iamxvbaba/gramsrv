package mtprotoedge

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

// TestFirstContainerBoundaryKeepsAndroidRequestMap models DrKLO's
// new_session_created handling: it drops every running request whose msg_id is
// lower than first_msg_id. Every inner request accepted from the first
// container must therefore remain addressable when its pong arrives.
func TestFirstContainerBoundaryKeepsAndroidRequestMap(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	msgIDs := proto.NewMessageIDGen(time.Now)
	running := make(map[int64]int64, 3)
	messages := make([]proto.Message, 0, 3)
	for i := 0; i < 3; i++ {
		msgID := msgIDs.New(proto.MessageFromClient)
		pingID := int64(10_001 + i)
		body := mustEncodeTL(t, &mt.PingRequest{PingID: pingID})
		messages = append(messages, proto.Message{
			ID:    msgID,
			SeqNo: 1 + i*2,
			Bytes: len(body),
			Body:  body,
		})
		running[msgID] = pingID
	}
	outerMsgID := msgIDs.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, outerMsgID, &proto.MessageContainer{Messages: messages})

	boundaryFrames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{
		mt.NewSessionCreatedTypeID: 1,
	})
	var created mt.NewSessionCreated
	for _, frame := range boundaryFrames {
		if frame.TypeID == mt.PongTypeID {
			t.Fatalf("pong arrived before new_session_created boundary")
		}
		if frame.TypeID != mt.NewSessionCreatedTypeID {
			continue
		}
		if err := created.Decode(frame.Plain); err != nil {
			t.Fatalf("decode new_session_created: %v", err)
		}
	}

	// DrKLO ConnectionsManager.cpp clears running requests with
	// request.messageId < first_msg_id when it receives this notification.
	for msgID := range running {
		if msgID < created.FirstMsgID {
			delete(running, msgID)
		}
	}
	for _, accepted := range messages {
		if _, ok := running[accepted.ID]; !ok {
			t.Fatalf(
				"accepted inner msg_id %d was evicted by Android boundary %d (outer=%d)",
				accepted.ID,
				created.FirstMsgID,
				outerMsgID,
			)
		}
	}

	pongFrames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{
		mt.PongTypeID: len(messages),
	})
	seen := make(map[int64]struct{}, len(messages))
	for _, frame := range pongFrames {
		if frame.TypeID != mt.PongTypeID {
			continue
		}
		var pong mt.Pong
		if err := pong.Decode(frame.Plain); err != nil {
			t.Fatalf("decode pong: %v", err)
		}
		wantPingID, ok := running[pong.MsgID]
		if !ok {
			t.Fatalf("orphan pong for msg_id %d after Android boundary cleanup", pong.MsgID)
		}
		if pong.PingID != wantPingID {
			t.Fatalf("pong ping_id = %d for msg_id %d, want %d", pong.PingID, pong.MsgID, wantPingID)
		}
		if _, duplicate := seen[pong.MsgID]; duplicate {
			t.Fatalf("duplicate pong for msg_id %d", pong.MsgID)
		}
		seen[pong.MsgID] = struct{}{}
		delete(running, pong.MsgID)
	}
	if len(running) != 0 {
		t.Fatalf("requests without correlated pong: %+v", running)
	}
}

type admissionCountingRPC struct {
	calls atomic.Int32
}

func (h *admissionCountingRPC) Dispatch(_ context.Context, _ [8]byte, _ int64, _ *bin.Buffer) (bin.Encoder, error) {
	h.calls.Add(1)
	return &tg.Config{ThisDC: 2}, nil
}

func (*admissionCountingRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

func TestContainerRPCAdmissionFailureIsAtomic(t *testing.T) {
	const dc = 2
	handler := &admissionCountingRPC{}
	addr, pub, _ := startTestServer(t, Options{
		DC:               dc,
		legacyRPC:        handler,
		RPCMaxInflight:   1,
		RPCQueueSize:     1,
		RPCGlobalWorkers: 1,
	})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	msgIDs := proto.NewMessageIDGen(time.Now)
	messages := make([]proto.Message, 0, 2)
	requestIDs := make(map[int64]struct{}, 2)
	for i := 0; i < 2; i++ {
		msgID := msgIDs.New(proto.MessageFromClient)
		body := mustEncodeTL(t, &tg.HelpGetConfigRequest{})
		messages = append(messages, proto.Message{ID: msgID, SeqNo: 1 + i*2, Bytes: len(body), Body: body})
		requestIDs[msgID] = struct{}{}
	}
	outerMsgID := msgIDs.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, outerMsgID, &proto.MessageContainer{Messages: messages})

	frames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{proto.ResultTypeID: 2})
	results := 0
	for _, frame := range frames {
		if frame.TypeID != proto.ResultTypeID {
			continue
		}
		var result proto.Result
		if err := result.Decode(frame.Plain); err != nil {
			t.Fatalf("decode rpc_result: %v", err)
		}
		if _, ok := requestIDs[result.RequestMessageID]; !ok {
			t.Fatalf("unexpected or duplicate capacity rpc_result req_msg_id %d", result.RequestMessageID)
		}
		var rpcErr mt.RPCError
		if err := rpcErr.Decode(&bin.Buffer{Buf: result.Result}); err != nil {
			t.Fatalf("decode capacity rpc_error: %v", err)
		}
		if rpcErr.ErrorCode != 420 || rpcErr.ErrorMessage != "FLOOD_WAIT_1" {
			t.Fatalf("capacity rpc_error = %+v", rpcErr)
		}
		delete(requestIDs, result.RequestMessageID)
		results++
	}
	if results != 2 {
		t.Fatalf("capacity rpc_results = %d, want 2", results)
	}
	if len(requestIDs) != 0 {
		t.Fatalf("capacity requests without exactly one result: %+v", requestIDs)
	}
	if got := handler.calls.Load(); got != 0 {
		t.Fatalf("partially executed handler calls = %d, want 0", got)
	}
}

func TestGZIPWrappedRPCUsesLogicalEnvelopeBoundary(t *testing.T) {
	const dc = 2
	handler := &admissionCountingRPC{}
	addr, pub, _ := startTestServer(t, Options{DC: dc, legacyRPC: handler})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	requestBody := mustEncodeTL(t, &tg.HelpGetConfigRequest{})
	msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, msgID, 1, &proto.GZIP{Data: requestBody})
	frames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{proto.ResultTypeID: 1})
	var boundary mt.NewSessionCreated
	for _, frame := range frames {
		if frame.TypeID == mt.NewSessionCreatedTypeID {
			if err := boundary.Decode(frame.Plain); err != nil {
				t.Fatalf("decode gzip RPC boundary: %v", err)
			}
		}
	}
	if boundary.FirstMsgID != msgID {
		t.Fatalf("gzip RPC boundary = %d, want envelope %d", boundary.FirstMsgID, msgID)
	}
	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("gzip RPC handler calls = %d, want 1", got)
	}
}

func TestGZIPWrappedContainerUsesLowestInnerBoundary(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	ids := proto.NewMessageIDGen(time.Now)
	innerMsgID := ids.New(proto.MessageFromClient)
	outerMsgID := ids.New(proto.MessageFromClient)
	pingBody := mustEncodeTL(t, &mt.PingRequest{PingID: 7001})
	containerBody := mustEncodeTL(t, &proto.MessageContainer{Messages: []proto.Message{{
		ID: innerMsgID, SeqNo: 1, Bytes: len(pingBody), Body: pingBody,
	}}})
	sendEncryptedWithSeq(t, conn, cipher, auth, outerMsgID, 2, &proto.GZIP{Data: containerBody})
	frames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{mt.PongTypeID: 1})
	var boundary mt.NewSessionCreated
	for _, frame := range frames {
		if frame.TypeID == mt.NewSessionCreatedTypeID {
			if err := boundary.Decode(frame.Plain); err != nil {
				t.Fatalf("decode gzip container boundary: %v", err)
			}
		}
	}
	if boundary.FirstMsgID != innerMsgID {
		t.Fatalf("gzip container boundary = %d, want inner %d (outer %d)", boundary.FirstMsgID, innerMsgID, outerMsgID)
	}
}

func TestEmptyContainerUsesOuterBoundary(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	outerMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, outerMsgID, 0, &proto.MessageContainer{})
	frames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{mt.NewSessionCreatedTypeID: 1})
	var boundary mt.NewSessionCreated
	for _, frame := range frames {
		if frame.TypeID != mt.NewSessionCreatedTypeID {
			continue
		}
		if err := boundary.Decode(frame.Plain); err != nil {
			t.Fatalf("decode empty container boundary: %v", err)
		}
	}
	if boundary.FirstMsgID != outerMsgID {
		t.Fatalf("empty container boundary = %d, want outer %d", boundary.FirstMsgID, outerMsgID)
	}
}

func TestDuplicateContainerAcksWithoutBusinessReexecution(t *testing.T) {
	const dc = 2
	handler := &admissionCountingRPC{}
	addr, pub, _ := startTestServer(t, Options{DC: dc, legacyRPC: handler})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	ids := proto.NewMessageIDGen(time.Now)
	messages := make([]proto.Message, 0, 2)
	for i := 0; i < 2; i++ {
		body := mustEncodeTL(t, &tg.HelpGetConfigRequest{})
		messages = append(messages, proto.Message{
			ID: ids.New(proto.MessageFromClient), SeqNo: 1 + i*2, Bytes: len(body), Body: body,
		})
	}
	outerMsgID := ids.New(proto.MessageFromClient)
	container := &proto.MessageContainer{Messages: messages}
	sendEncrypted(t, conn, cipher, auth, outerMsgID, container)
	initialFrames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{proto.ResultTypeID: 2})
	answerIDs := make([]int64, 0, 2)
	for _, frame := range initialFrames {
		if frame.TypeID == proto.ResultTypeID {
			answerIDs = append(answerIDs, frame.Message.MessageID)
		}
	}
	if len(answerIDs) != 2 {
		t.Fatalf("initial rpc_result answer ids = %d, want 2", len(answerIDs))
	}
	// ACK both original server results before retransmitting the container. This is
	// a wire barrier: any later rpc_result is necessarily a duplicate replay rather
	// than an unconsumed response from the initial batch.
	ackMsgID := ids.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, ackMsgID, 4, &mt.MsgsAck{MsgIDs: answerIDs})
	if got := handler.calls.Load(); got != 2 {
		t.Fatalf("initial handler calls = %d, want 2", got)
	}

	sendEncrypted(t, conn, cipher, auth, outerMsgID, container)
	duplicateFrames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{mt.MsgsAckTypeID: 1})
	for _, frame := range duplicateFrames {
		if frame.TypeID == proto.ResultTypeID {
			t.Fatal("same-connection duplicate container replayed an extra rpc_result")
		}
	}
	if got := handler.calls.Load(); got != 2 {
		t.Fatalf("duplicate container reexecuted handlers: calls=%d, want 2", got)
	}
}

type largeStartupBurstRPC struct {
	calls atomic.Int32
	body  string
}

func (h *largeStartupBurstRPC) Dispatch(ctx context.Context, _ [8]byte, _ int64, _ *bin.Buffer) (bin.Encoder, error) {
	call := h.calls.Add(1)
	// Deliberately perturb completion order while remaining cancellation-aware.
	timer := time.NewTimer(time.Duration(call%4) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &tg.DataJSON{Data: h.body}, nil
}

func (*largeStartupBurstRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

func TestAndroidStartupBurstKeepsAllLargeRPCResultsAddressable(t *testing.T) {
	const (
		dc       = 2
		requests = 30
	)
	handler := &largeStartupBurstRPC{body: strings.Repeat("x", 192<<10)}
	addr, pub, _ := startTestServer(t, Options{DC: dc, legacyRPC: handler})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	ids := proto.NewMessageIDGen(time.Now)
	running := make(map[int64]struct{}, requests)
	messages := make([]proto.Message, 0, requests)
	for i := 0; i < requests; i++ {
		msgID := ids.New(proto.MessageFromClient)
		body := mustEncodeTL(t, &tg.HelpGetConfigRequest{})
		messages = append(messages, proto.Message{
			ID: msgID, SeqNo: 1 + i*2, Bytes: len(body), Body: body,
		})
		running[msgID] = struct{}{}
	}
	outerMsgID := ids.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, outerMsgID, &proto.MessageContainer{Messages: messages})

	boundaryFrames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{mt.NewSessionCreatedTypeID: 1})
	var boundary mt.NewSessionCreated
	for _, frame := range boundaryFrames {
		if frame.TypeID == proto.ResultTypeID {
			t.Fatal("large rpc_result arrived before session boundary")
		}
		if frame.TypeID == mt.NewSessionCreatedTypeID {
			if err := boundary.Decode(frame.Plain); err != nil {
				t.Fatalf("decode startup boundary: %v", err)
			}
		}
	}
	for msgID := range running {
		if msgID < boundary.FirstMsgID {
			delete(running, msgID)
		}
	}
	if len(running) != requests {
		t.Fatalf("Android boundary removed accepted startup requests: kept=%d want=%d floor=%d outer=%d", len(running), requests, boundary.FirstMsgID, outerMsgID)
	}

	resultFrames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{proto.ResultTypeID: requests})
	for _, frame := range resultFrames {
		if frame.TypeID != proto.ResultTypeID {
			continue
		}
		var result proto.Result
		if err := result.Decode(frame.Plain); err != nil {
			t.Fatalf("decode startup rpc_result: %v", err)
		}
		if _, ok := running[result.RequestMessageID]; !ok {
			t.Fatalf("orphan large rpc_result for request %d", result.RequestMessageID)
		}
		delete(running, result.RequestMessageID)
	}
	if len(running) != 0 {
		t.Fatalf("startup RPCs without result: %d", len(running))
	}
	if got := handler.calls.Load(); got != requests {
		t.Fatalf("startup handler calls = %d, want %d", got, requests)
	}
}
