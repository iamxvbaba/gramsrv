package mtprotoedge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

type blockingDuplicateRPC struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	once    sync.Once
}

func newBlockingDuplicateRPC() *blockingDuplicateRPC {
	return &blockingDuplicateRPC{
		started: make(chan struct{}, 4),
		release: make(chan struct{}),
	}
}

func (h *blockingDuplicateRPC) Dispatch(ctx context.Context, _ [8]byte, _ int64, _ *bin.Buffer) (bin.Encoder, error) {
	h.calls.Add(1)
	h.started <- struct{}{}
	select {
	case <-h.release:
		return &tg.Config{ThisDC: 2}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (*blockingDuplicateRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

func (h *blockingDuplicateRPC) unblock() { h.once.Do(func() { close(h.release) }) }

// TestPendingSameConnectionDuplicateDoesNotBlockFreshRequest models the core
// Android startup failure: an RPC is executing, salt correction makes the client
// resend the same msg_id, then initConnection assigns a fresh msg_id. A local
// duplicate must not synchronously join the old owner on the socket read loop,
// otherwise the fresh request remains unread until the old result/replay storm
// has drained.
func TestPendingSameConnectionDuplicateDoesNotBlockFreshRequest(t *testing.T) {
	const dc = 2
	handler := newBlockingDuplicateRPC()
	defer handler.unblock()
	addr, pub, server := startTestServer(t, Options{
		DC:               dc,
		legacyRPC:        handler,
		RPCMaxInflight:   2,
		RPCGlobalWorkers: 2,
		RPCQueueSize:     8,
	})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	ids := proto.NewMessageIDGen(time.Now)
	oldID := ids.New(proto.MessageFromClient)
	newID := ids.New(proto.MessageFromClient)

	sendEncryptedWithSeq(t, conn, cipher, auth, oldID, 1, &tg.HelpGetConfigRequest{})
	waitDuplicateHandlerStarts(t, handler.started, "original request")

	// Same ID/seq is a retransmission, not a second business operation.
	sendEncryptedWithSeq(t, conn, cipher, auth, oldID, 1, &tg.HelpGetConfigRequest{})
	// A client-side init/layer rewrap legitimately assigns a new ID and next seq.
	sendEncryptedWithSeq(t, conn, cipher, auth, newID, 3, &tg.HelpGetConfigRequest{})
	waitDuplicateHandlerStarts(t, handler.started, "fresh request behind duplicate")
	if got := handler.calls.Load(); got != 2 {
		t.Fatalf("handler calls before release = %d, want original + fresh only", got)
	}

	handler.unblock()
	frames := collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{
		proto.ResultTypeID: 2,
	})
	results := make(map[int64]int)
	for _, frame := range frames {
		if frame.TypeID != proto.ResultTypeID {
			continue
		}
		var result proto.Result
		if err := result.Decode(frame.Plain); err != nil {
			t.Fatalf("decode rpc_result: %v", err)
		}
		results[result.RequestMessageID]++
	}
	if results[oldID] != 1 || results[newID] != 1 || len(results) != 2 {
		t.Fatalf("rpc_result counts = %+v, want one for old and one for fresh id", results)
	}
	if got := handler.calls.Load(); got != 2 {
		t.Fatalf("final handler calls = %d, want 2", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for server.rpcResults.flightLimit.snapshot() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := server.rpcResults.flightLimit.snapshot(); got != 0 {
		t.Fatalf("duplicate admission leaked flight slots: %d", got)
	}
}

func waitDuplicateHandlerStarts(t *testing.T, started <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s did not reach handler; socket read loop is likely blocked on a duplicate", what)
	}
}
