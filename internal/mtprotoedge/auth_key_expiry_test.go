package mtprotoedge

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/log/logzap"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/exchange"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/proto/codec"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/transport"
)

func TestAuthKeyProtocolUnavailable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tests := []struct {
		name      string
		expiresAt int
		want      bool
	}{
		{name: "legacy unknown", expiresAt: -1, want: true},
		{name: "permanent", expiresAt: 0, want: false},
		{name: "expired temporary", expiresAt: int(now.Unix()), want: true},
		{name: "live temporary", expiresAt: int(now.Add(time.Second).Unix()), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authKeyProtocolUnavailable(tt.expiresAt, now); got != tt.want {
				t.Fatalf("authKeyProtocolUnavailable(%d) = %v, want %v", tt.expiresAt, got, tt.want)
			}
		})
	}
}

// expiryTestClock keeps server protocol time deterministic while retaining real
// timers for transport/RPC deadlines. Expiry admission reads Now before any
// envelope validation, so advancing it exercises the cached active-connection
// boundary without making the test sleep until a wall-clock second rolls over.
type expiryTestClock struct {
	mu  sync.RWMutex
	now time.Time
}

func newExpiryTestClock(now time.Time) *expiryTestClock {
	return &expiryTestClock{now: now}
}

func (c *expiryTestClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *expiryTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (*expiryTestClock) Timer(d time.Duration) clock.Timer   { return clock.System.Timer(d) }
func (*expiryTestClock) Ticker(d time.Duration) clock.Ticker { return clock.System.Ticker(d) }

type signalingGuardedLeaseWriter struct {
	lease   *physicalTransportLease
	entered chan struct{}
	once    sync.Once
}

func (w *signalingGuardedLeaseWriter) Send(ctx context.Context, b *bin.Buffer) error {
	return w.lease.Send(ctx, b)
}

func (w *signalingGuardedLeaseWriter) SendDeadlineWithScratchGuarded(deadline time.Time, b *bin.Buffer, scratch *[]byte, guard func() error) error {
	w.once.Do(func() { close(w.entered) })
	return w.lease.SendDeadlineWithScratchGuarded(deadline, b, scratch, guard)
}

func dialTemporaryHandshakeForExpiryTest(
	t *testing.T,
	addr string,
	dc, expiresIn int,
	pub exchange.PublicKey,
) (transport.Conn, exchange.ClientExchangeResult, crypto.Cipher) {
	t.Helper()
	conn := dialTransportOnly(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	auth, err := exchange.NewExchanger(conn, dc).
		WithTempMode(expiresIn).
		WithRand(rand.Reader).
		WithLogger(logzap.New(zaptest.NewLogger(t).Named("temp-client"))).
		Client([]exchange.PublicKey{pub}).
		Run(ctx)
	if err != nil {
		t.Fatalf("temporary client exchange: %v", err)
	}
	return conn, auth, crypto.NewClientCipher(rand.Reader)
}

func TestActiveTemporaryAuthKeyExpiresBeforeNextRPCDispatch(t *testing.T) {
	const (
		dc        = 2
		expiresIn = 60 * 60
	)
	now := time.Now()
	testClock := newExpiryTestClock(now)
	handler := &admissionCountingRPC{}
	addr, pub, srv := startTestServer(t, Options{
		DC:    dc,
		Clock: testClock,
		RPC:   handler,
	})
	conn, auth, cipher := dialTemporaryHandshakeForExpiryTest(t, addr, dc, expiresIn, pub)

	stored, found, err := srv.authKeys.Get(context.Background(), auth.AuthKey.ID)
	if err != nil || !found {
		t.Fatalf("temporary auth key after exchange: found=%v err=%v", found, err)
	}
	wantExpiresAt := int(now.Unix()) + expiresIn
	if stored.ExpiresAt != wantExpiresAt {
		t.Fatalf("temporary auth key expires_at = %d, want %d", stored.ExpiresAt, wantExpiresAt)
	}

	ids := proto.NewMessageIDGen(time.Now)
	firstID := ids.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, firstID, &tg.HelpGetConfigRequest{})
	collectReplyFrames(t, conn, cipher, auth.AuthKey, map[uint32]int{
		proto.ResultTypeID: 1,
		mt.MsgsAckTypeID:   1,
	})
	waitForAtomicCalls(t, &handler.calls, 1)

	key := sessionKey{authKeyID: auth.AuthKey.ID, sessionID: auth.SessionID}
	srv.conns.mu.RLock()
	active := srv.conns.bySession[key]
	srv.conns.mu.RUnlock()
	if active == nil || !active.isActive() {
		t.Fatalf("temporary session was not active before expiry: %p", active)
	}

	// Cross the exact protocol boundary: expires_at <= now is invalid. The next
	// frame must be rejected before decrypt/preflight/Dispatch, even though this
	// connection already cached the key and completed session activation.
	testClock.Advance(time.Duration(expiresIn+1) * time.Second)
	sendEncrypted(t, conn, cipher, auth, ids.New(proto.MessageFromClient), &tg.HelpGetConfigRequest{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var response bin.Buffer
	err = conn.Recv(ctx, &response)
	var protocolErr *codec.ProtocolErr
	if !errors.As(err, &protocolErr) || protocolErr.Code != codec.CodeAuthKeyNotFound {
		t.Fatalf("expired active temp key recv = %T %v, want protocol -404", err, err)
	}

	waitForManagedSessionAbsent(t, srv.conns, key)
	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("expired active temp key executed %d RPCs, want only the pre-expiry request", got)
	}
}

func TestExpiredTemporaryAuthKeyRejectsServerPushWithoutWireWrite(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := newExpiryTestClock(now)
	tr := &failAfterTransport{}
	c := newOutboundTestConn(t, tr, nil)
	c.now = clock.Now
	c.authKeyExpiresAt = int(now.Unix())

	err := c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer,
		&encodedOutboundMessage{typeID: tg.UpdatesTooLongTypeID, body: []byte{0x0b, 0xa1, 0x01, 0xe3}}, 0)
	if !errors.Is(err, ErrConnClosed) {
		t.Fatalf("push on expired temp key = %v, want ErrConnClosed", err)
	}
	if got := tr.sends.Load(); got != 0 {
		t.Fatalf("wire sends after expiry = %d, want zero", got)
	}
	if !c.isRetired() || tr.closes.Load() != 1 {
		t.Fatalf("expired connection retired=%v transport_closes=%d, want true/1", c.isRetired(), tr.closes.Load())
	}
}

func TestQueuedPushCannotCrossTemporaryAuthKeyExpiry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := newExpiryTestClock(now)
	tr := newGatedRecordingTransport()
	c := newOutboundTestConn(t, tr, nil)
	c.now = clock.Now
	c.authKeyExpiresAt = int(now.Add(time.Minute).Unix())
	encoded := &encodedOutboundMessage{typeID: tg.UpdatesTooLongTypeID, body: []byte{0x0b, 0xa1, 0x01, 0xe3}}

	if err := c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer, encoded, 0); err != nil {
		t.Fatalf("enqueue first push: %v", err)
	}
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("first push did not enter blocked writer")
	}
	if err := c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer, encoded, 0); err != nil {
		t.Fatalf("enqueue second push: %v", err)
	}
	clock.Advance(time.Minute)
	tr.once.Do(func() { close(tr.release) })

	select {
	case <-c.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("expired outbound actor did not stop")
	}
	if got := len(tr.snapshot()); got != 1 {
		t.Fatalf("wire frames across expiry = %d, want only already-writing frame", got)
	}
}

func TestTemporaryAuthKeyExpiryWhileWaitingForPhysicalWriterSkipsRawSend(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	testClock := newExpiryTestClock(now)
	raw := newGatedRecordingTransport()
	_, lease := newPhysicalTransportOwner(raw)
	c := newOutboundTestConn(t, lease, nil)
	c.transportLease = lease
	c.now = testClock.Now
	c.authKeyExpiresAt = int(now.Add(time.Minute).Unix())
	signaling := &signalingGuardedLeaseWriter{lease: lease, entered: make(chan struct{})}
	c.writer = signaling

	// Simulate a quick ACK/protocol write that already owns the physical writer.
	quickDone := make(chan error, 1)
	go func() {
		quickDone <- lease.Send(context.Background(), &bin.Buffer{Buf: []byte{1, 2, 3, 4}})
	}()
	select {
	case <-raw.started:
	case <-time.After(time.Second):
		t.Fatal("direct protocol write did not acquire physical writer")
	}

	encoded := &encodedOutboundMessage{typeID: tg.UpdatesTooLongTypeID, body: []byte{0x0b, 0xa1, 0x01, 0xe3}}
	actorDone := make(chan error, 1)
	go func() {
		actorDone <- c.SendEncoded(context.Background(), proto.MessageFromServer, encoded)
	}()
	select {
	case <-signaling.entered:
		// writeFrame passed its outer expiry check and entered the guarded lease;
		// the direct write still owns writeMu, so raw.Send cannot have started.
	case <-time.After(time.Second):
		t.Fatal("outbound actor did not wait for physical writer ownership")
	}

	testClock.Advance(time.Minute)
	raw.once.Do(func() { close(raw.release) })
	if err := <-quickDone; err != nil {
		t.Fatalf("direct protocol write: %v", err)
	}
	if err := <-actorDone; !errors.Is(err, ErrConnClosed) {
		t.Fatalf("actor write after expiry = %v, want ErrConnClosed", err)
	}
	if frames := raw.snapshot(); len(frames) != 1 {
		t.Fatalf("raw wire frames = %d, want only the pre-expiry direct frame", len(frames))
	}
	if !c.isRetired() {
		t.Fatal("connection was not fenced after guarded expiry rejection")
	}
}

func TestRetiredActorWaitingForPhysicalWriterDoesNotDefeatLeaseTransfer(t *testing.T) {
	raw := newGatedRecordingTransport()
	_, lease := newPhysicalTransportOwner(raw)
	c := newOutboundTestConn(t, lease, nil)
	c.transportLease = lease
	signaling := &signalingGuardedLeaseWriter{lease: lease, entered: make(chan struct{})}
	c.writer = signaling

	directDone := make(chan error, 1)
	go func() {
		directDone <- lease.Send(context.Background(), &bin.Buffer{Buf: []byte{5, 6, 7, 8}})
	}()
	select {
	case <-raw.started:
	case <-time.After(time.Second):
		t.Fatal("direct protocol write did not acquire physical writer")
	}

	actorDone := make(chan error, 1)
	go func() {
		actorDone <- c.SendEncoded(context.Background(), proto.MessageFromServer,
			&encodedOutboundMessage{typeID: tg.UpdatesTooLongTypeID, body: []byte{0x0b, 0xa1, 0x01, 0xe3}})
	}()
	select {
	case <-signaling.entered:
	case <-time.After(time.Second):
		t.Fatal("outbound actor did not reach guarded physical writer")
	}

	c.beginTerminalShutdown()
	raw.once.Do(func() { close(raw.release) })
	if err := <-directDone; err != nil {
		t.Fatalf("direct protocol write: %v", err)
	}
	select {
	case <-c.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("retired outbound actor did not drain")
	}
	select {
	case err := <-actorDone:
		if !errors.Is(err, ErrConnClosed) {
			t.Fatalf("retired actor write = %v, want ErrConnClosed", err)
		}
	default:
	}
	if frames := raw.snapshot(); len(frames) != 1 {
		t.Fatalf("retired actor reached raw writer: frames=%d, want one direct frame", len(frames))
	}
	if !lease.IsCurrentOpen() {
		t.Fatal("retired actor closed physical lease")
	}
	if next, ok := lease.Transfer(); !ok || next == nil {
		t.Fatal("retired actor defeated physical lease transfer")
	}
}

func TestTerminalAuthKeyNotFoundSurvivesActorWaitingForPhysicalWriter(t *testing.T) {
	raw := newGatedRecordingTransport()
	_, lease := newPhysicalTransportOwner(raw)
	c := newOutboundTestConn(t, lease, nil)
	c.transportLease = lease
	signaling := &signalingGuardedLeaseWriter{lease: lease, entered: make(chan struct{})}
	c.writer = signaling

	directDone := make(chan error, 1)
	go func() {
		directDone <- lease.Send(context.Background(), &bin.Buffer{Buf: []byte{9, 10, 11, 12}})
	}()
	select {
	case <-raw.started:
	case <-time.After(time.Second):
		t.Fatal("direct protocol write did not acquire physical writer")
	}
	go func() {
		_ = c.SendEncoded(context.Background(), proto.MessageFromServer,
			&encodedOutboundMessage{typeID: tg.UpdatesTooLongTypeID, body: []byte{0x0b, 0xa1, 0x01, 0xe3}})
	}()
	select {
	case <-signaling.entered:
	case <-time.After(time.Second):
		t.Fatal("outbound actor did not reach guarded physical writer")
	}

	srv := New(Options{WriteTimeout: time.Second})
	terminalDone := make(chan error, 1)
	go func() {
		terminalDone <- srv.sendTerminalProtoError(context.Background(), c, codec.CodeAuthKeyNotFound)
	}()
	select {
	case err := <-terminalDone:
		t.Fatalf("terminal error bypassed waiting actor: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	raw.once.Do(func() { close(raw.release) })
	if err := <-directDone; err != nil {
		t.Fatalf("direct protocol write: %v", err)
	}
	select {
	case err := <-terminalDone:
		if err != nil {
			t.Fatalf("send terminal -404: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal -404 did not follow waiting actor drain")
	}

	frames := raw.snapshot()
	if len(frames) != 2 {
		t.Fatalf("wire frames = %d, want direct frame then -404", len(frames))
	}
	last := frames[len(frames)-1]
	if len(last) != 4 || int32(binary.LittleEndian.Uint32(last)) != -codec.CodeAuthKeyNotFound {
		t.Fatalf("last wire frame = %x, want bare -404", last)
	}
}

func TestTerminalAuthKeyNotFoundWaitsForOutboundAndIsLastFrame(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := newExpiryTestClock(now)
	tr := newGatedRecordingTransport()
	_, lease := newPhysicalTransportOwner(tr)
	c := newOutboundTestConn(t, lease, nil)
	c.transportLease = lease
	c.now = clock.Now
	c.authKeyExpiresAt = int(now.Add(time.Minute).Unix())
	encoded := &encodedOutboundMessage{typeID: tg.UpdatesTooLongTypeID, body: []byte{0x0b, 0xa1, 0x01, 0xe3}}
	if err := c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer, encoded, 0); err != nil {
		t.Fatalf("enqueue blocked push: %v", err)
	}
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("push did not enter blocked writer")
	}

	clock.Advance(time.Minute)
	srv := New(Options{WriteTimeout: time.Second})
	terminalDone := make(chan error, 1)
	go func() {
		terminalDone <- srv.sendTerminalProtoError(context.Background(), c, codec.CodeAuthKeyNotFound)
	}()
	select {
	case err := <-terminalDone:
		t.Fatalf("terminal error bypassed active outbound writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer, encoded, 0); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("push admitted behind terminal fence: %v", err)
	}
	tr.once.Do(func() { close(tr.release) })
	select {
	case err := <-terminalDone:
		if err != nil {
			t.Fatalf("send terminal -404: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal -404 did not follow drained writer")
	}

	frames := tr.snapshot()
	if len(frames) != 2 {
		t.Fatalf("wire frames = %d, want encrypted frame then -404", len(frames))
	}
	last := frames[len(frames)-1]
	if len(last) != 4 || int32(binary.LittleEndian.Uint32(last)) != -codec.CodeAuthKeyNotFound {
		t.Fatalf("last wire frame = %x, want bare -404", last)
	}
}
