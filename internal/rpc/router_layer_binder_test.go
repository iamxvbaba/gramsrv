package rpc

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/tg"
)

type layerBinderCall struct {
	rawAuthKeyID [8]byte
	sessionID    int64
	layer        int
}

// layerCaptureSessions 在 captureSessions 基础上实现可选的 ClientLayerBinder。
type layerCaptureSessions struct {
	captureSessions
	layerMu    sync.Mutex
	layerCalls []layerBinderCall
}

func (s *layerCaptureSessions) SetClientLayerForAuthKey(rawAuthKeyID [8]byte, sessionID int64, layer int) {
	s.layerMu.Lock()
	defer s.layerMu.Unlock()
	s.layerCalls = append(s.layerCalls, layerBinderCall{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID, layer: layer})
}

func (s *layerCaptureSessions) layerCallsSnapshot() []layerBinderCall {
	s.layerMu.Lock()
	defer s.layerMu.Unlock()
	return append([]layerBinderCall(nil), s.layerCalls...)
}

// TestDispatchInvokeWithLayerPushesLayerToSessionBinder 验证 invokeWithLayer 在
// Dispatch 入口把新观测的 layer 即时下推到连接层（ClientLayerBinder），且仅在
// 首次观测或 layer 变化时下推——每条请求都带 wrapper 的客户端不会造成逐 RPC 下推。
func TestDispatchInvokeWithLayerPushesLayerToSessionBinder(t *testing.T) {
	sessions := &layerCaptureSessions{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	rawAuthKeyID := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	const sessionID = int64(42)

	dispatchWithLayer := func(layer int) {
		t.Helper()
		var in bin.Buffer
		req := &tg.InvokeWithLayerRequest{Layer: layer, Query: &tg.HelpGetConfigRequest{}}
		if err := req.Encode(&in); err != nil {
			t.Fatalf("encode: %v", err)
		}
		if _, err := r.Dispatch(context.Background(), rawAuthKeyID, sessionID, &in); err != nil {
			t.Fatalf("dispatch layer %d: %v", layer, err)
		}
	}

	dispatchWithLayer(225)
	calls := sessions.layerCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("layer binder calls = %d, want 1", len(calls))
	}
	if calls[0] != (layerBinderCall{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID, layer: 225}) {
		t.Fatalf("layer binder call = %+v", calls[0])
	}

	// 同 layer 重复 wrapper：不再下推。
	dispatchWithLayer(225)
	if calls := sessions.layerCallsSnapshot(); len(calls) != 1 {
		t.Fatalf("layer binder calls after repeat = %d, want 1", len(calls))
	}

	// layer 变化：再次下推。
	dispatchWithLayer(226)
	calls = sessions.layerCallsSnapshot()
	if len(calls) != 2 || calls[1].layer != 226 {
		t.Fatalf("layer binder calls after change = %+v, want second call with layer 226", calls)
	}
}
