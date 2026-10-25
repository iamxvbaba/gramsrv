package coreexec

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/rpc"
)

// countingRateLimiter allows `limit` calls per key and records every key.
type countingRateLimiter struct {
	mu     sync.Mutex
	counts map[string]int
	keys   []string
}

func (l *countingRateLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, int, error) {
	return l.AllowN(ctx, key, 1, limit, window)
}

func (l *countingRateLimiter) AllowN(_ context.Context, key string, cost, limit int, _ time.Duration) (bool, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts == nil {
		l.counts = map[string]int{}
	}
	l.keys = append(l.keys, key)
	l.counts[key] += cost
	if l.counts[key] > limit {
		return false, 30, nil
	}
	return true, 0, nil
}

// The Edge forwards every production RPC to Core through CoreExec, where it
// runs Router.DispatchAdmitted. A pre-auth auth.checkPassword must spend the
// SRP budget of its raw auth key there, and the over-budget FLOOD_WAIT must
// survive the gRPC round trip.
func TestGRPCRemoteDispatchAppliesSensitiveRPCRateLimit(t *testing.T) {
	limiter := &countingRateLimiter{}
	coreRouter := rpc.New(rpc.Config{
		DC: 2, IP: "127.0.0.1", Port: 2398,
		SRPRateLimit: 1, SRPRateWindow: time.Minute,
	}, rpc.Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)
	edgeRouter := rpc.New(rpc.Config{DC: 2, IP: "127.0.0.1", Port: 2398}, rpc.Deps{}, zaptest.NewLogger(t), clock.System)
	remote, cleanup := newBufGRPCRemote(t, coreRouter, edgeRouter, "secret", "secret")
	defer cleanup()

	authKeyID := [8]byte{4, 3, 2, 1, 8, 7, 6, 5}
	dispatch := func(msgID int64) error {
		t.Helper()
		var body bin.Buffer
		if err := tlprofile.EncodeObject(tlprofile.Profile228, &tg.AuthCheckPasswordRequest{Password: &tg.InputCheckPasswordEmpty{}}, &body); err != nil {
			t.Fatal(err)
		}
		admitted, err := remote.AdmitLayer(tlprofile.Profile228, &body, tlprofile.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		_, method, err := remote.DispatchAdmitted(context.Background(), authKeyID, 77, msgID, uint64(msgID+1000), admitted)
		if method != "auth.checkPassword" {
			t.Fatalf("method = %q, want auth.checkPassword", method)
		}
		return err
	}

	if err := dispatch(100); err != nil && strings.Contains(err.Error(), "FLOOD_WAIT") {
		t.Fatalf("first auth.checkPassword err = %v, want it within budget", err)
	}
	err := dispatch(101)
	var rpcErr *tgerr.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != 420 || rpcErr.Message != "FLOOD_WAIT_30" {
		t.Fatalf("second auth.checkPassword err = %v, want 420 FLOOD_WAIT_30", err)
	}
	wantKey := "auth:srp:k:" + hex.EncodeToString(authKeyID[:])
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if len(limiter.keys) != 2 || limiter.keys[0] != wantKey || limiter.keys[1] != wantKey {
		t.Fatalf("limiter keys = %v, want two %q", limiter.keys, wantKey)
	}
}
