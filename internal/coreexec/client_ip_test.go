package coreexec

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"telesrv/internal/coreexec/coreexecpb"
	"telesrv/internal/rpc"
	"telesrv/internal/transport"
)

// captureClientIPHandler records the transport client IP that reaches the Core
// handler after the CoreExec gRPC hop.
type captureClientIPHandler struct {
	Handler

	mu         sync.Mutex
	dispatchIP string
	dispatchOK bool
	observes   int
	observeIP  string
	observeOK  bool
}

func (h *captureClientIPHandler) DispatchAdmitted(ctx context.Context, authKeyID [8]byte, sessionID int64, msgID int64, admissionSeq uint64, request tlprofile.Admission) (tlprofile.Result, string, error) {
	h.mu.Lock()
	h.dispatchIP, h.dispatchOK = transport.ClientIPFrom(ctx)
	h.mu.Unlock()
	return h.Handler.DispatchAdmitted(ctx, authKeyID, sessionID, msgID, admissionSeq, request)
}

func (h *captureClientIPHandler) ObserveInitConnection(ctx context.Context, _ [8]byte, _ int64, _, _ int, _, _, _, _, _, _ string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.observes++
	h.observeIP, h.observeOK = transport.ClientIPFrom(ctx)
	return nil
}

func (h *captureClientIPHandler) dispatched() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dispatchIP, h.dispatchOK
}

func (h *captureClientIPHandler) observed() (int, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.observes, h.observeIP, h.observeOK
}

func TestGRPCRemoteDispatchCarriesClientIP(t *testing.T) {
	coreRouter := rpc.New(rpc.Config{DC: 2, IP: "127.0.0.1", Port: 2398}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	core := &captureClientIPHandler{Handler: coreRouter}
	edgeRouter := rpc.New(rpc.Config{DC: 2, IP: "127.0.0.1", Port: 2398}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	remote, cleanup := newBufGRPCRemote(t, core, edgeRouter, "secret", "secret")
	defer cleanup()

	ctx := transport.WithClientIP(context.Background(), "203.0.113.24")
	if _, _, err := remote.DispatchAdmitted(ctx, [8]byte{1, 2, 3}, 10, 20, 30, admitHelpGetConfigForGRPCTest(t, remote)); err != nil {
		t.Fatal(err)
	}
	if ip, ok := core.dispatched(); !ok || ip != "203.0.113.24" {
		t.Fatalf("core client ip = %q/%v, want 203.0.113.24", ip, ok)
	}

	if _, _, err := remote.DispatchAdmitted(context.Background(), [8]byte{1, 2, 3}, 10, 21, 31, admitHelpGetConfigForGRPCTest(t, remote)); err != nil {
		t.Fatal(err)
	}
	if ip, ok := core.dispatched(); ok {
		t.Fatalf("core received client ip %q without Edge transport metadata", ip)
	}
}

func TestGRPCRemoteObserveInitConnectionCarriesClientIP(t *testing.T) {
	core := &captureClientIPHandler{}
	remote, cleanup := newBufGRPCRemote(t, core, core, "secret", "secret")
	defer cleanup()

	ctx := transport.WithClientIP(context.Background(), "2001:db8::7")
	if err := remote.ObserveInitConnection(ctx, [8]byte{1}, 77, 228, 2040, "Desktop", "Windows", "5.0", "en", "tdesktop", "en"); err != nil {
		t.Fatal(err)
	}
	if n, ip, ok := core.observed(); n != 1 || !ok || ip != "2001:db8::7" {
		t.Fatalf("observed = %d %q/%v, want one call with 2001:db8::7", n, ip, ok)
	}
}

func TestCoreExecServerRejectsNonCanonicalClientIP(t *testing.T) {
	core := &captureClientIPHandler{}
	remote, cleanup := newBufGRPCRemote(t, core, core, "secret", "secret")
	defer cleanup()

	_, err := remote.client.ObserveInitConnection(context.Background(), &coreexecpb.ObserveInitConnectionRequest{
		AuthKeyId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, SessionId: 77, ClientIp: "::ffff:203.0.113.5",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("observe with IPv4-mapped client ip err = %v, want InvalidArgument", err)
	}
	if n, _, _ := core.observed(); n != 0 {
		t.Fatalf("handler ran %d times for a rejected client ip", n)
	}
}

func TestApplyClientIPPBAcceptsOnlyCanonicalIPLiterals(t *testing.T) {
	for _, tc := range []struct {
		ip string
		ok bool
	}{
		{"", true},
		{"203.0.113.5", true},
		{"2001:db8::1", true},
		{"::ffff:203.0.113.5", false},
		{"fe80::1%en0", false},
		{"2001:DB8::1", false},
		{"203.0.113.5:443", false},
		{"/tmp/telesrv.sock", false},
	} {
		ctx, err := applyClientIPPB(context.Background(), tc.ip)
		if tc.ok != (err == nil) {
			t.Fatalf("applyClientIPPB(%q) err = %v, want ok=%v", tc.ip, err, tc.ok)
		}
		if err != nil && !errors.Is(err, errBadClientIP) {
			t.Fatalf("applyClientIPPB(%q) err = %v, want errBadClientIP", tc.ip, err)
		}
		got, carried := transport.ClientIPFrom(ctx)
		if tc.ok && tc.ip != "" && (!carried || got != tc.ip) {
			t.Fatalf("applyClientIPPB(%q) carried %q/%v", tc.ip, got, carried)
		}
		if (!tc.ok || tc.ip == "") && carried {
			t.Fatalf("applyClientIPPB(%q) attached %q", tc.ip, got)
		}
	}
}
