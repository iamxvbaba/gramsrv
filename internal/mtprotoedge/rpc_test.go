package mtprotoedge

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/transport"

	"telesrv/internal/rpc"
)

// TestRPCGetConfig 验证 M3：握手后 client 加密 help.getConfig，
// server 经 tg.ServerDispatcher 路由并回 rpc_result（含本地 DC），外加 new_session_created + ack。
func TestRPCGetConfig(t *testing.T) {
	const (
		dc      = 2
		advIP   = "127.0.0.1"
		advPort = 12345
	)
	router := rpc.New(rpc.Config{DC: dc, IP: advIP, Port: advPort}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	addr, pub, _ := startTestServer(t, Options{DC: dc, RPC: router})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, reqMsgID, &tg.HelpGetConfigRequest{})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, proto.ResultTypeID)
	if _, ok := replies[mt.MsgsAckTypeID]; !ok {
		for id, b := range collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID) {
			replies[id] = b
		}
	}
	mustHave(t, replies, mt.NewSessionCreatedTypeID, "new_session_created")
	mustHave(t, replies, mt.MsgsAckTypeID, "msgs_ack")
	resultBuf := mustHave(t, replies, proto.ResultTypeID, "rpc_result")

	var res proto.Result
	if err := res.Decode(resultBuf); err != nil {
		t.Fatalf("decode rpc_result: %v", err)
	}
	if res.RequestMessageID != reqMsgID {
		t.Fatalf("rpc_result req_msg_id = %d, want %d", res.RequestMessageID, reqMsgID)
	}

	var cfg tg.Config
	if err := cfg.Decode(&bin.Buffer{Buf: res.Result}); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.ThisDC != dc {
		t.Fatalf("config.ThisDC = %d, want %d", cfg.ThisDC, dc)
	}
	// 不下发 DCOptions：客户端使用写死的 static DC 地址（空列表令其保留本地地址）。
	if len(cfg.DCOptions) != 0 {
		t.Fatalf("config.DCOptions = %+v, want empty (client uses pinned static address)", cfg.DCOptions)
	}
}

func TestInboundRPCQueueFullReturnsFloodWait(t *testing.T) {
	const dc = 2
	handler := &blockingRPC{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	addr, pub, _ := startTestServer(t, Options{
		DC:             dc,
		RPC:            handler,
		RPCMaxInflight: 1,
		RPCQueueSize:   1,
		RPCTimeout:     5 * time.Second,
	})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	firstReqID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, firstReqID, 1, &tg.HelpGetConfigRequest{})
	select {
	case <-handler.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first rpc to start")
	}

	secondReqID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, secondReqID, 3, &tg.HelpGetConfigRequest{})
	thirdReqID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, thirdReqID, 5, &tg.HelpGetConfigRequest{})

	result := readRPCResultForRequest(t, conn, cipher, auth.AuthKey, thirdReqID)
	var rpcErr mt.RPCError
	if err := rpcErr.Decode(&bin.Buffer{Buf: result.Result}); err != nil {
		t.Fatalf("decode rpc_error: %v", err)
	}
	if rpcErr.ErrorCode != 420 || rpcErr.ErrorMessage != "FLOOD_WAIT_1" {
		t.Fatalf("rpc_error = %d %q, want 420 FLOOD_WAIT_1", rpcErr.ErrorCode, rpcErr.ErrorMessage)
	}
	close(handler.release)
}

func TestInboundRPCQueuedDeadlineReturnsRPCTimeout(t *testing.T) {
	const dc = 2
	handler := &queueDeadlineRPC{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	addr, pub, _ := startTestServer(t, Options{
		DC:               dc,
		RPC:              handler,
		RPCMaxInflight:   1,
		RPCQueueSize:     2,
		RPCTimeout:       60 * time.Millisecond,
		RPCGlobalWorkers: 1,
	})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	firstReqID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, firstReqID, 1, &tg.HelpGetConfigRequest{})
	select {
	case <-handler.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first rpc to start")
	}

	secondReqID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, secondReqID, 3, &tg.HelpGetConfigRequest{})
	// 第一条故意忽略 context，使第二条越过自身从入队起计算的 deadline 后才有机会出队。
	time.Sleep(120 * time.Millisecond)
	close(handler.releaseFirst)

	result := readRPCResultForRequest(t, conn, cipher, auth.AuthKey, secondReqID)
	var rpcErr mt.RPCError
	if err := rpcErr.Decode(&bin.Buffer{Buf: result.Result}); err != nil {
		t.Fatalf("decode rpc timeout: %v", err)
	}
	if rpcErr.ErrorCode != 500 || rpcErr.ErrorMessage != "RPC_TIMEOUT" {
		t.Fatalf("rpc_error = %d %q, want 500 RPC_TIMEOUT", rpcErr.ErrorCode, rpcErr.ErrorMessage)
	}
	if calls := handler.calls.Load(); calls != 1 {
		t.Fatalf("handler calls = %d, want 1 (expired queued RPC must not dispatch)", calls)
	}
}

func TestInboundRPCRunningDeadlineWaitsForHandlerTerminalResult(t *testing.T) {
	for _, tc := range []struct {
		name         string
		honorContext bool
	}{
		{name: "handler_honors_context", honorContext: true},
		{name: "handler_temporarily_ignores_context", honorContext: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const dc = 2
			handler := &runningDeadlineRPC{
				started:      make(chan struct{}),
				release:      make(chan struct{}),
				honorContext: tc.honorContext,
			}
			addr, pub, _ := startTestServer(t, Options{
				DC:               dc,
				RPC:              handler,
				RPCMaxInflight:   1,
				RPCQueueSize:     1,
				RPCTimeout:       60 * time.Millisecond,
				RPCGlobalWorkers: 1,
			})
			conn, auth, cipher := dialHandshake(t, addr, dc, pub)

			clientMsgID := proto.NewMessageIDGen(time.Now)
			reqID := clientMsgID.New(proto.MessageFromClient)
			sendEncryptedWithSeq(t, conn, cipher, auth, reqID, 1, &tg.HelpGetConfigRequest{})
			select {
			case <-handler.started:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for running rpc")
			}

			if !tc.honorContext {
				// Let the deadline expire while Dispatch is still running. No early timeout
				// may win; after the handler reports committed success, that success is the
				// sole terminal result.
				time.Sleep(120 * time.Millisecond)
				close(handler.release)
			}
			result := readRPCResultForRequest(t, conn, cipher, auth.AuthKey, reqID)
			if tc.honorContext {
				var rpcErr mt.RPCError
				if err := rpcErr.Decode(&bin.Buffer{Buf: result.Result}); err != nil {
					t.Fatalf("decode converged rpc timeout: %v", err)
				}
				if rpcErr.ErrorCode != 500 || rpcErr.ErrorMessage != "RPC_TIMEOUT" {
					t.Fatalf("rpc_error = %d %q, want 500 RPC_TIMEOUT", rpcErr.ErrorCode, rpcErr.ErrorMessage)
				}
				close(handler.release)
			} else {
				var config tg.Config
				if err := config.Decode(&bin.Buffer{Buf: result.Result}); err != nil {
					t.Fatalf("decode committed success after deadline: %v", err)
				}
			}
		})
	}
}

func TestDuplicateRPCResultAcrossReconnectUsesSessionCache(t *testing.T) {
	const dc = 2
	handler := &countingConfigRPC{}
	addr, pub, _ := startTestServer(t, Options{DC: dc, RPC: handler})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, reqMsgID, &tg.HelpGetConfigRequest{})

	first := readRPCResultForRequest(t, conn, cipher, auth.AuthKey, reqMsgID)
	if calls := handler.calls.Load(); calls != 1 {
		t.Fatalf("handler calls after first request = %d, want 1", calls)
	}
	var firstConfig tg.Config
	if err := firstConfig.Decode(&bin.Buffer{Buf: first.Result}); err != nil {
		t.Fatalf("decode first config: %v", err)
	}
	if firstConfig.ThisDC != dc {
		t.Fatalf("first config.ThisDC = %d, want %d", firstConfig.ThisDC, dc)
	}

	_ = conn.Close()
	replayConn := dialTransportOnly(t, addr)
	sendEncrypted(t, replayConn, cipher, auth, reqMsgID, &tg.HelpGetConfigRequest{})

	second := readRPCResultForRequest(t, replayConn, cipher, auth.AuthKey, reqMsgID)
	if calls := handler.calls.Load(); calls != 1 {
		t.Fatalf("handler calls after replay = %d, want 1", calls)
	}
	if string(second.Result) != string(first.Result) {
		t.Fatalf("replayed rpc_result payload changed")
	}
}

func TestCanceledRPCErrorIsNotCachedAcrossReconnect(t *testing.T) {
	const dc = 2
	handler := &canceledInternalRPC{
		firstStarted: make(chan struct{}),
		firstDone:    make(chan struct{}),
	}
	addr, pub, _ := startTestServer(t, Options{DC: dc, RPC: handler})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, reqMsgID, &tg.HelpGetConfigRequest{})

	select {
	case <-handler.firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first rpc to start after session barrier")
	}
	_ = conn.Close()
	select {
	case <-handler.firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for canceled first rpc")
	}

	replayConn := dialTransportOnly(t, addr)
	sendEncrypted(t, replayConn, cipher, auth, reqMsgID, &tg.HelpGetConfigRequest{})

	result := readRPCResultForRequest(t, replayConn, cipher, auth.AuthKey, reqMsgID)
	var cfg tg.Config
	if err := cfg.Decode(&bin.Buffer{Buf: result.Result}); err != nil {
		t.Fatalf("decode replay config: %v", err)
	}
	if cfg.ThisDC != dc {
		t.Fatalf("replay config.ThisDC = %d, want %d", cfg.ThisDC, dc)
	}
	if calls := handler.calls.Load(); calls != 2 {
		t.Fatalf("handler calls = %d, want 2 (canceled first result must not be cached)", calls)
	}
}

type countingConfigRPC struct {
	calls atomic.Int32
}

func (h *countingConfigRPC) Dispatch(context.Context, [8]byte, int64, *bin.Buffer) (bin.Encoder, error) {
	h.calls.Add(1)
	return &tg.Config{ThisDC: 2}, nil
}

func (h *countingConfigRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

type blockingRPC struct {
	started chan struct{}
	release chan struct{}
}

func (h *blockingRPC) Dispatch(ctx context.Context, _ [8]byte, _ int64, _ *bin.Buffer) (bin.Encoder, error) {
	select {
	case h.started <- struct{}{}:
	default:
	}
	select {
	case <-h.release:
		return &tg.Config{ThisDC: 2}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *blockingRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

type queueDeadlineRPC struct {
	calls        atomic.Int32
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (h *queueDeadlineRPC) Dispatch(context.Context, [8]byte, int64, *bin.Buffer) (bin.Encoder, error) {
	if h.calls.Add(1) == 1 {
		close(h.firstStarted)
		<-h.releaseFirst
	}
	return &tg.Config{ThisDC: 2}, nil
}

func (h *queueDeadlineRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

type runningDeadlineRPC struct {
	started      chan struct{}
	release      chan struct{}
	honorContext bool
}

func (h *runningDeadlineRPC) Dispatch(ctx context.Context, _ [8]byte, _ int64, _ *bin.Buffer) (bin.Encoder, error) {
	close(h.started)
	if h.honorContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	<-h.release
	return &tg.Config{ThisDC: 2}, nil
}

func (h *runningDeadlineRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

type canceledInternalRPC struct {
	calls        atomic.Int32
	firstStarted chan struct{}
	firstDone    chan struct{}
}

func (h *canceledInternalRPC) Dispatch(ctx context.Context, _ [8]byte, _ int64, _ *bin.Buffer) (bin.Encoder, error) {
	if h.calls.Add(1) == 1 {
		close(h.firstStarted)
		<-ctx.Done()
		close(h.firstDone)
		return nil, tgerr.New(500, "INTERNAL_SERVER_ERROR")
	}
	return &tg.Config{ThisDC: 2}, nil
}

func (h *canceledInternalRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

func readRPCResultForRequest(t *testing.T, conn transport.Conn, cipher crypto.Cipher, key crypto.AuthKey, reqMsgID int64) proto.Result {
	t.Helper()
	for i := 0; i < 12; i++ {
		_, id, plain := readServerMessage(t, conn, cipher, key)
		if id != proto.ResultTypeID {
			continue
		}
		var result proto.Result
		if err := result.Decode(plain); err != nil {
			t.Fatalf("decode rpc_result: %v", err)
		}
		if result.RequestMessageID == reqMsgID {
			return result
		}
	}
	t.Fatalf("missing rpc_result for req_msg_id %d", reqMsgID)
	return proto.Result{}
}
