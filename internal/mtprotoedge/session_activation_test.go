package mtprotoedge

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
)

func newSessionActivationTestConn(t *testing.T, authKeyID [8]byte, sessionID int64) *Conn {
	t.Helper()
	c := &Conn{
		authKeyID: authKeyID,
		sessionID: sessionID,
		metrics:   NopMetrics{},
	}
	c.startOutbound()
	t.Cleanup(c.Close)
	return c
}

func TestSessionActivationGatesReplacementBeforePublishing(t *testing.T) {
	manager := NewSessionManager(zaptest.NewLogger(t))
	key := [8]byte{1, 2, 3, 4}
	oldConn := newSessionActivationTestConn(t, key, 7001)
	newConn := newSessionActivationTestConn(t, key, 7001)

	if err := manager.Register(oldConn); err != nil {
		t.Fatalf("register initial: %v", err)
	}
	if !oldConn.isActive() {
		t.Fatal("initial connection was not activated")
	}
	if newConn.lifecycleState() != connLifecycleProvisional {
		t.Fatal("provisional replacement was active before registration")
	}

	if err := manager.Register(newConn); err != nil {
		t.Fatalf("register replacement: %v", err)
	}
	if !newConn.isActive() {
		t.Fatal("replacement was not activated")
	}
	if !oldConn.isRetired() {
		t.Fatalf("old connection lifecycle=%v", oldConn.lifecycleState())
	}
	if err := oldConn.SendAsync(context.Background(), proto.MessageFromServer, &mt.MsgsAck{}); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("stale old connection send error = %v, want ErrConnClosed", err)
	}

	manager.mu.RLock()
	got := manager.bySession[sessionKey{authKeyID: key, sessionID: 7001}]
	manager.mu.RUnlock()
	if got != newConn {
		t.Fatalf("published session = %p, want replacement %p", got, newConn)
	}
}

func TestSessionActivationClaimPreemptionCannotReversePublish(t *testing.T) {
	manager := NewSessionManager(zaptest.NewLogger(t))
	key := [8]byte{9, 8, 7, 6}
	first := newSessionActivationTestConn(t, key, 8001)
	second := newSessionActivationTestConn(t, key, 8001)

	if err := manager.BeginActivation(first); err != nil {
		t.Fatalf("begin first activation: %v", err)
	}
	if first.lifecycleState() != connLifecycleClaiming {
		t.Fatalf("first lifecycle = %v, want claiming", first.lifecycleState())
	}
	if got := manager.Online(); got != 0 {
		t.Fatalf("online during claim = %d, want 0", got)
	}

	if err := manager.BeginActivation(second); err != nil {
		t.Fatalf("begin superseding activation: %v", err)
	}
	if !first.isRetired() {
		t.Fatalf("superseded first lifecycle=%v", first.lifecycleState())
	}
	if err := manager.PublishActivation(first); !errors.Is(err, ErrSessionActivationSuperseded) {
		t.Fatalf("stale publish error = %v, want superseded", err)
	}
	if err := manager.Register(first); !errors.Is(err, ErrSessionActivationSuperseded) {
		t.Fatalf("stale register error = %v, want superseded", err)
	}
	if err := manager.PublishActivation(second); err != nil {
		t.Fatalf("publish second activation: %v", err)
	}

	manager.mu.RLock()
	got := manager.bySession[sessionKey{authKeyID: key, sessionID: 8001}]
	claim := manager.claims[sessionKey{authKeyID: key, sessionID: 8001}]
	manager.mu.RUnlock()
	if got != second || claim != nil || !second.isActive() {
		t.Fatalf("activation owner=%p claim=%p second_active=%v", got, claim, second.isActive())
	}
}

func TestBeginActivationWaitsForPreviousPhysicalWriterFence(t *testing.T) {
	manager := NewSessionManager(zaptest.NewLogger(t))
	key := [8]byte{3, 3, 3, 3}
	releaseClose := make(chan struct{})
	transport := newSlowCloseTransport(0, releaseClose)
	oldConn := &Conn{
		authKeyID: key,
		sessionID: 8501,
		metrics:   NopMetrics{},
		transport: transport,
		writer:    transport,
	}
	oldConn.startOutbound()
	t.Cleanup(oldConn.Close)
	if err := manager.Register(oldConn); err != nil {
		t.Fatalf("register old: %v", err)
	}
	newConn := newSessionActivationTestConn(t, key, oldConn.sessionID)

	beginDone := make(chan error, 1)
	go func() { beginDone <- manager.BeginActivation(newConn) }()
	deadline := time.Now().Add(time.Second)
	for transport.closes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if transport.closes.Load() == 0 {
		t.Fatal("replacement did not start closing previous transport")
	}
	select {
	case err := <-beginDone:
		t.Fatalf("BeginActivation returned before old physical close: %v", err)
	default:
	}
	close(releaseClose)
	select {
	case err := <-beginDone:
		if err != nil {
			t.Fatalf("BeginActivation after old close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BeginActivation did not converge after old close")
	}
	if err := manager.PublishActivation(newConn); err != nil {
		t.Fatalf("publish replacement: %v", err)
	}
}

func TestSessionActivationClaimIndexesCleanOnPublishAndAbort(t *testing.T) {
	manager := NewSessionManager(zaptest.NewLogger(t))
	key := [8]byte{4, 4, 4, 4}
	published := newSessionActivationTestConn(t, key, 9001)
	if err := manager.BeginActivation(published); err != nil {
		t.Fatalf("begin publish claim: %v", err)
	}
	manager.mu.RLock()
	if manager.claims[connSessionKey(published)] != published || manager.claimsByAuth[key][published.sessionID] != published {
		manager.mu.RUnlock()
		t.Fatal("claim indexes missing after BeginActivation")
	}
	manager.mu.RUnlock()
	if err := manager.PublishActivation(published); err != nil {
		t.Fatalf("publish claim: %v", err)
	}
	manager.mu.RLock()
	globalClaims, authClaims := len(manager.claims), len(manager.claimsByAuth[key])
	manager.mu.RUnlock()
	if globalClaims != 0 || authClaims != 0 {
		t.Fatalf("claim indexes after publish = %d/%d, want 0/0", globalClaims, authClaims)
	}

	aborted := newSessionActivationTestConn(t, key, 9002)
	if err := manager.BeginActivation(aborted); err != nil {
		t.Fatalf("begin abort claim: %v", err)
	}
	manager.AbortActivation(aborted)
	manager.mu.RLock()
	globalClaims, authClaims = len(manager.claims), len(manager.claimsByAuth[key])
	manager.mu.RUnlock()
	if globalClaims != 0 || authClaims != 0 || aborted.lifecycleState() != connLifecycleRetired {
		t.Fatalf("claim indexes/lifecycle after abort = %d/%d/%v", globalClaims, authClaims, aborted.lifecycleState())
	}
}

func TestRawAuthKeyCloseExactConnDoesNotExcludeSameSessionReplacement(t *testing.T) {
	manager := NewSessionManager(zaptest.NewLogger(t))
	key := [8]byte{4, 3, 2, 1}
	const sessionID = 9050

	// The destroy request may finish on a retired Conn after another physical
	// connection has become the owner of the same logical session.
	destroyer := newSessionActivationTestConn(t, key, sessionID)
	destroyer.beginTerminalShutdown()
	replacement := newSessionActivationTestConn(t, key, sessionID)
	if err := manager.Register(replacement); err != nil {
		t.Fatalf("register replacement: %v", err)
	}

	if got := manager.CloseSessionsForRawAuthKeyExceptConn(key, destroyer); got != 1 {
		t.Fatalf("closed sessions = %d, want replacement only", got)
	}
	manager.mu.RLock()
	active, claim := manager.bySession[sessionKey{authKeyID: key, sessionID: sessionID}], manager.claims[sessionKey{authKeyID: key, sessionID: sessionID}]
	manager.mu.RUnlock()
	if active != nil || claim != nil || !replacement.isRetired() {
		t.Fatalf("same-session replacement escaped exact exclusion: active=%p claim=%p lifecycle=%v", active, claim, replacement.lifecycleState())
	}
}

func TestDestroyFencesOutboundAndReservedRPCBeforeRemoval(t *testing.T) {
	manager := NewSessionManager(zaptest.NewLogger(t))
	key := [8]byte{5, 5, 5, 5}
	c := newSessionActivationTestConn(t, key, 9101)
	scheduler := newInboundRPCScheduler(1, 8, 1<<20)
	c.startInboundRPCScheduler(scheduler, 1, 8, time.Second)
	if err := manager.Register(c); err != nil {
		t.Fatalf("register: %v", err)
	}

	reservation, err := c.reserveInboundRPC(context.Background(), "test.destroyFence", 8)
	if err != nil {
		t.Fatalf("reserve inbound RPC: %v", err)
	}
	destroyed := make(chan bool, 1)
	go func() {
		destroyed <- manager.DestroySessionForAuthKey(key, c.sessionID)
	}()
	select {
	case <-c.rpcRootCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("destroy did not synchronously cancel RPC admission")
	}
	if err := c.SendAsync(context.Background(), proto.MessageFromServer, &mt.MsgsAck{}); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("send after destroy fence = %v, want ErrConnClosed", err)
	}
	if err := reservation.commit(inboundRPC{}); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("reserved RPC commit after destroy = %v, want ErrConnClosed", err)
	}
	select {
	case ok := <-destroyed:
		if !ok {
			t.Fatal("destroy returned false")
		}
	case <-time.After(time.Second):
		t.Fatal("destroy did not converge after reservation commit")
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("scheduler budget after destroy = %d/%d, want 0/0", tasks, bytes)
	}
}
