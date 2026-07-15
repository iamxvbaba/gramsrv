package rpc

import (
	"context"
	"sync"
	"testing"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
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

// TestLegacyDispatchInvokeWithLayerAppliesOrderedSessionCorrection verifies the
// legacy facade follows the same explicit correction semantics as generated
// admission. A repeated selector is idempotent; a new selector updates exactly
// this session and notifies the connection once.
func TestLegacyDispatchInvokeWithLayerAppliesOrderedSessionCorrection(t *testing.T) {
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

	// A later explicit selector is a valid ordered correction.
	dispatchWithLayer(226)
	calls = sessions.layerCallsSnapshot()
	if len(calls) != 2 || calls[1] != (layerBinderCall{rawAuthKeyID: rawAuthKeyID, sessionID: sessionID, layer: 226}) {
		t.Fatalf("layer binder calls after correction = %+v", calls)
	}
	if layer, ok := r.NegotiatedSessionLayer(rawAuthKeyID, sessionID); !ok || layer != 226 {
		t.Fatalf("corrected exact session profile = (%d,%v), want (226,true)", layer, ok)
	}
}
