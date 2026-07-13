package mtprotoedge

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"

	"telesrv/internal/store"
)

func encryptedRPCFrameForBarrierTest(
	t *testing.T,
	key crypto.AuthKey,
	salt, sessionID, msgID int64,
) (*bin.Buffer, store.AuthKeyData) {
	return encryptedRPCFrameWithAuthoritativeSaltForBarrierTest(t, key, salt, salt, sessionID, msgID, 1)
}

func encryptedRPCFrameWithAuthoritativeSaltForBarrierTest(
	t *testing.T,
	key crypto.AuthKey,
	frameSalt, authoritativeSalt, sessionID, msgID int64,
	seqNo int32,
) (*bin.Buffer, store.AuthKeyData) {
	t.Helper()
	body := encodeClientMessageBodyForTest(t, &tg.HelpGetConfigRequest{})
	var frame bin.Buffer
	if err := crypto.NewClientCipher(rand.Reader).Encrypt(key, crypto.EncryptedMessageData{
		Salt:                   frameSalt,
		SessionID:              sessionID,
		MessageID:              msgID,
		SeqNo:                  seqNo,
		MessageDataLen:         int32(len(body)),
		MessageDataWithPadding: body,
	}, &frame); err != nil {
		t.Fatalf("encrypt client RPC: %v", err)
	}
	return &frame, store.AuthKeyData{
		ID:         key.ID,
		Value:      [256]byte(key.Value),
		ServerSalt: authoritativeSalt,
	}
}

func TestBadServerSaltRetainsOneProvisionalConnUntilCorrected(t *testing.T) {
	handler := &admissionCountingRPC{}
	s := New(Options{RPC: handler, WriteTimeout: time.Second})
	s.rpcScheduler.start()
	t.Cleanup(func() { s.rpcScheduler.stop(time.Second) })

	tr := &collectingSessionTransport{}
	key := newTestAuthKey(t)
	const (
		serverSalt = int64(0x1020304050)
		wrongSalt  = serverSalt + 1
		sessionID  = int64(71001)
	)
	ids := proto.NewMessageIDGen(time.Now)
	firstID := ids.New(proto.MessageFromClient)
	firstWrong, stored := encryptedRPCFrameWithAuthoritativeSaltForBarrierTest(
		t, key, wrongSalt, serverSalt, sessionID, firstID, 1,
	)
	if err := s.authKeys.Save(context.Background(), stored); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	cs := newConnState()
	var plain bin.Buffer

	firstConn, err := s.handleEncrypted(context.Background(), tr, cs, nil, &stored, firstWrong, &plain)
	if err != nil {
		t.Fatalf("first bad salt: %v", err)
	}
	if firstConn == nil || firstConn.lifecycleState() != connLifecycleProvisional {
		t.Fatalf("first correction lifecycle conn=%p state=%v", firstConn, firstConn.lifecycleState())
	}
	if cs.createdFloor != 0 || len(cs.seen) != 0 || handler.calls.Load() != 0 {
		t.Fatalf("bad salt admitted state: floor=%d seen=%d calls=%d", cs.createdFloor, len(cs.seen), handler.calls.Load())
	}

	secondID := ids.New(proto.MessageFromClient)
	secondWrong, _ := encryptedRPCFrameWithAuthoritativeSaltForBarrierTest(
		t, key, wrongSalt, serverSalt, sessionID, secondID, 3,
	)
	secondConn, err := s.handleEncrypted(context.Background(), tr, cs, firstConn, nil, secondWrong, &plain)
	if err != nil {
		t.Fatalf("second bad salt: %v", err)
	}
	if secondConn != firstConn {
		t.Fatalf("bad-salt retry replaced provisional Conn: first=%p second=%p", firstConn, secondConn)
	}
	if got := len(tr.snapshot()); got != 2 {
		t.Fatalf("distinct bad msg ids produced %d corrections, want 2", got)
	}
	for i, wire := range tr.snapshot()[:2] {
		data, decryptErr := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(key, &bin.Buffer{Buf: wire})
		if decryptErr != nil {
			t.Fatalf("decrypt correction %d: %v", i, decryptErr)
		}
		if data.Salt != serverSalt {
			t.Fatalf("correction %d envelope salt = %#x, want %#x", i, data.Salt, serverSalt)
		}
		var bad mt.BadServerSalt
		body := &bin.Buffer{Buf: append([]byte(nil), data.Data()...)}
		if decodeErr := bad.Decode(body); decodeErr != nil {
			t.Fatalf("decode correction %d: %v", i, decodeErr)
		}
		if bad.NewServerSalt != serverSalt {
			t.Fatalf("correction %d payload salt = %#x, want %#x", i, bad.NewServerSalt, serverSalt)
		}
	}

	corrected, _ := encryptedRPCFrameWithAuthoritativeSaltForBarrierTest(
		t, key, serverSalt, serverSalt, sessionID, firstID, 1,
	)
	activeConn, err := s.handleEncrypted(context.Background(), tr, cs, secondConn, nil, corrected, &plain)
	if err != nil {
		t.Fatalf("corrected retry: %v", err)
	}
	if activeConn != firstConn || !activeConn.isActive() {
		t.Fatalf("corrected retry connection=%p first=%p active=%v", activeConn, firstConn, activeConn != nil && activeConn.isActive())
	}
	if cs.createdFloor != firstID {
		t.Fatalf("corrected created floor = %d, want %d", cs.createdFloor, firstID)
	}
	waitForAtomicCalls(t, &handler.calls, 1)
	flightDeadline := time.Now().Add(2 * time.Second)
	for s.rpcResults.flightLimit.snapshot() != 0 && time.Now().Before(flightDeadline) {
		time.Sleep(time.Millisecond)
	}
	if got := s.rpcResults.flightLimit.snapshot(); got != 0 {
		t.Fatalf("corrected retry leaked flight slots: %d", got)
	}
	activeConn.ForceClose()
}

func TestWrongSaltSessionChangeTransfersPhysicalOwnership(t *testing.T) {
	// The first handler remains pending until session replacement cancels it. Its
	// owner Abort must observe the already-published terminal gate and must not
	// close the physical lease that is about to transfer to the new session.
	handler := &cancelThenRetryRPC{started: make(chan struct{})}
	s := New(Options{RPC: handler, WriteTimeout: time.Second})
	s.rpcScheduler.start()
	t.Cleanup(func() { s.rpcScheduler.stop(time.Second) })

	tr := &collectingSessionTransport{}
	key := newTestAuthKey(t)
	const (
		serverSalt = int64(0x66778899)
		wrongSalt  = serverSalt + 7
		firstSID   = int64(72001)
		secondSID  = int64(72002)
	)
	ids := proto.NewMessageIDGen(time.Now)
	firstID := ids.New(proto.MessageFromClient)
	firstFrame, stored := encryptedRPCFrameWithAuthoritativeSaltForBarrierTest(
		t, key, serverSalt, serverSalt, firstSID, firstID, 1,
	)
	if err := s.authKeys.Save(context.Background(), stored); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	cs := newConnState()
	var plain bin.Buffer
	oldConn, err := s.handleEncrypted(context.Background(), tr, cs, nil, &stored, firstFrame, &plain)
	if err != nil {
		t.Fatalf("activate first session: %v", err)
	}
	select {
	case <-handler.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first session RPC did not start")
	}

	secondID := ids.New(proto.MessageFromClient)
	wrongFrame, _ := encryptedRPCFrameWithAuthoritativeSaltForBarrierTest(
		t, key, wrongSalt, serverSalt, secondSID, secondID, 1,
	)
	newConn, err := s.handleEncrypted(context.Background(), tr, cs, oldConn, nil, wrongFrame, &plain)
	if err != nil {
		t.Fatalf("new session bad salt: %v", err)
	}
	if newConn == nil || newConn == oldConn || newConn.lifecycleState() != connLifecycleProvisional {
		t.Fatalf("replacement conn old=%p new=%p state=%v", oldConn, newConn, newConn.lifecycleState())
	}
	if !oldConn.isRetired() || tr.closed.Load() {
		t.Fatalf("transfer state old_lifecycle=%v raw_closed=%v", oldConn.lifecycleState(), tr.closed.Load())
	}
	// A delayed stale close must not tear down the generation already transferred
	// to the new logical session.
	oldConn.ForceClose()
	if tr.closed.Load() || !newConn.isPhysicalTransportCurrentOpen() {
		t.Fatalf("stale ForceClose closed replacement: raw_closed=%v current_open=%v", tr.closed.Load(), newConn.isPhysicalTransportCurrentOpen())
	}

	corrected, _ := encryptedRPCFrameWithAuthoritativeSaltForBarrierTest(
		t, key, serverSalt, serverSalt, secondSID, secondID, 1,
	)
	activated, err := s.handleEncrypted(context.Background(), tr, cs, newConn, nil, corrected, &plain)
	if err != nil {
		t.Fatalf("activate transferred session: %v", err)
	}
	if activated != newConn || !activated.isActive() {
		t.Fatalf("transferred session active=%v conn=%p want=%p", activated != nil && activated.isActive(), activated, newConn)
	}
	waitForAtomicCalls(t, &handler.calls, 2)
	activated.ForceClose()
}

type collectingSessionTransport struct {
	mu     sync.Mutex
	frames [][]byte
	closed atomic.Bool
}

func (t *collectingSessionTransport) Send(_ context.Context, b *bin.Buffer) error {
	if t.closed.Load() {
		return io.ErrClosedPipe
	}
	t.mu.Lock()
	t.frames = append(t.frames, append([]byte(nil), b.Raw()...))
	t.mu.Unlock()
	return nil
}

func (*collectingSessionTransport) Recv(context.Context, *bin.Buffer) error { return io.EOF }
func (t *collectingSessionTransport) Close() error {
	t.closed.Store(true)
	return nil
}

func (t *collectingSessionTransport) snapshot() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([][]byte, len(t.frames))
	for i := range t.frames {
		out[i] = append([]byte(nil), t.frames[i]...)
	}
	return out
}

type reconnectFlightRPC struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *reconnectFlightRPC) Dispatch(context.Context, [8]byte, int64, *bin.Buffer) (bin.Encoder, error) {
	h.calls.Add(1)
	h.once.Do(func() { close(h.started) })
	<-h.release // deliberately ignore replacement cancellation after committing work
	return &tg.Config{ThisDC: 2}, nil
}

func (*reconnectFlightRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

type cancelThenRetryRPC struct {
	calls   atomic.Int32
	active  atomic.Int32
	max     atomic.Int32
	started chan struct{}
	once    sync.Once
}

func (h *cancelThenRetryRPC) Dispatch(ctx context.Context, _ [8]byte, _ int64, _ *bin.Buffer) (bin.Encoder, error) {
	call := h.calls.Add(1)
	active := h.active.Add(1)
	defer h.active.Add(-1)
	for {
		max := h.max.Load()
		if active <= max || h.max.CompareAndSwap(max, active) {
			break
		}
	}
	if call == 1 {
		h.once.Do(func() { close(h.started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &tg.Config{ThisDC: 2}, nil
}

func (*cancelThenRetryRPC) NegotiatedLayer([8]byte, int64) (int, bool) { return 227, true }

func TestHandleEncryptedRequiredSessionBarrierPrecedesStateRegistrationAndRPC(t *testing.T) {
	handler := &admissionCountingRPC{}
	s := New(Options{RPC: handler, WriteTimeout: time.Second})
	s.rpcScheduler.start()
	t.Cleanup(func() { s.rpcScheduler.stop(time.Second) })

	tr := newGatedRequiredControlTransport(nil)
	key := newTestAuthKey(t)
	const (
		salt      = int64(0x10203040)
		sessionID = int64(70001)
	)
	msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	frame, stored := encryptedRPCFrameForBarrierTest(t, key, salt, sessionID, msgID)
	if err := s.authKeys.Save(context.Background(), stored); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	cs := newConnState()
	var plain bin.Buffer

	type result struct {
		conn *Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := s.handleEncrypted(context.Background(), tr, cs, nil, &stored, frame, &plain)
		done <- result{conn: conn, err: err}
	}()

	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("new_session_created did not reach the physical-write barrier")
	}
	if got := handler.calls.Load(); got != 0 {
		t.Fatalf("handler calls while session barrier blocked = %d, want 0", got)
	}
	if cs.createdFloor != 0 || len(cs.seen) != 0 {
		t.Fatalf("connState committed before session barrier: floor=%d seen=%d", cs.createdFloor, len(cs.seen))
	}
	s.conns.mu.RLock()
	registered := s.conns.bySession[sessionKey{authKeyID: key.ID, sessionID: sessionID}]
	claim := s.conns.claims[sessionKey{authKeyID: key.ID, sessionID: sessionID}]
	s.conns.mu.RUnlock()
	if registered != nil || claim == nil {
		t.Fatalf("blocked barrier visibility = registered:%p claim:%p, want nil/non-nil", registered, claim)
	}
	select {
	case got := <-done:
		t.Fatalf("handleEncrypted returned before physical barrier: %v", got.err)
	default:
	}

	tr.unblock()
	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handleEncrypted did not finish after session barrier write")
	}
	if got.err != nil {
		t.Fatalf("handleEncrypted: %v", got.err)
	}
	if got.conn == nil || !got.conn.isActive() {
		t.Fatalf("connection after successful barrier = %p lifecycle=%v", got.conn, got.conn.lifecycleState())
	}
	if cs.createdFloor != msgID {
		t.Fatalf("created floor after barrier = %d, want %d", cs.createdFloor, msgID)
	}
	waitForAtomicCalls(t, &handler.calls, 1)
	s.conns.mu.RLock()
	registered = s.conns.bySession[sessionKey{authKeyID: key.ID, sessionID: sessionID}]
	s.conns.mu.RUnlock()
	if registered != got.conn {
		t.Fatalf("registered connection = %p, want %p", registered, got.conn)
	}
	got.conn.ForceClose()
}

func TestHandleEncryptedRequiredSessionBarrierFailureIsAtomic(t *testing.T) {
	handler := &admissionCountingRPC{}
	s := New(Options{RPC: handler, WriteTimeout: time.Second})
	s.rpcScheduler.start()
	t.Cleanup(func() { s.rpcScheduler.stop(time.Second) })

	tr := newGatedRequiredControlTransport(io.ErrClosedPipe)
	key := newTestAuthKey(t)
	const (
		salt      = int64(0x50607080)
		sessionID = int64(70002)
	)
	msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	frame, stored := encryptedRPCFrameForBarrierTest(t, key, salt, sessionID, msgID)
	if err := s.authKeys.Save(context.Background(), stored); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	cs := newConnState()
	var plain bin.Buffer

	done := make(chan error, 1)
	go func() {
		_, err := s.handleEncrypted(context.Background(), tr, cs, nil, &stored, frame, &plain)
		done <- err
	}()
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("failed new_session_created did not reach physical writer")
	}
	if handler.calls.Load() != 0 || cs.createdFloor != 0 || len(cs.seen) != 0 {
		t.Fatalf("state changed while failing barrier blocked: calls=%d floor=%d seen=%d", handler.calls.Load(), cs.createdFloor, len(cs.seen))
	}
	tr.unblock()
	select {
	case err := <-done:
		if err == nil || !(errors.Is(err, io.ErrClosedPipe) || errors.Is(err, ErrConnClosed)) {
			t.Fatalf("failed session barrier error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed session barrier did not return")
	}

	if got := handler.calls.Load(); got != 0 {
		t.Fatalf("handler ran after failed session barrier: %d", got)
	}
	if cs.createdFloor != 0 || len(cs.seen) != 0 || len(cs.order) != 0 {
		t.Fatalf("failed barrier committed connState: floor=%d seen=%d order=%d", cs.createdFloor, len(cs.seen), len(cs.order))
	}
	s.conns.mu.RLock()
	registered := s.conns.bySession[sessionKey{authKeyID: key.ID, sessionID: sessionID}]
	claim := s.conns.claims[sessionKey{authKeyID: key.ID, sessionID: sessionID}]
	s.conns.mu.RUnlock()
	if registered != nil || claim != nil {
		t.Fatalf("failed barrier leaked manager ownership: registered=%p claim=%p", registered, claim)
	}
	if tasks, bytes := s.rpcScheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("failed barrier leaked RPC budget: tasks=%d bytes=%d", tasks, bytes)
	}
	if got := s.frameBudget.usedBytes(); got != 0 {
		t.Fatalf("failed barrier leaked frame budget: %d", got)
	}
	if got := s.rpcResults.flightLimit.snapshot(); got != 0 {
		t.Fatalf("failed barrier leaked RPC claim slots: %d", got)
	}
}

func TestCrossConnectionInflightRPCHasOneBusinessOwnerAndReplaysResult(t *testing.T) {
	handler := &reconnectFlightRPC{started: make(chan struct{}), release: make(chan struct{})}
	s := New(Options{RPC: handler, WriteTimeout: time.Second, RPCTimeout: 5 * time.Second})
	s.rpcScheduler.start()
	t.Cleanup(func() { s.rpcScheduler.stop(time.Second) })

	key := newTestAuthKey(t)
	const (
		salt      = int64(0x11223344)
		sessionID = int64(70003)
	)
	msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	firstFrame, stored := encryptedRPCFrameForBarrierTest(t, key, salt, sessionID, msgID)
	if err := s.authKeys.Save(context.Background(), stored); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	firstTransport := &collectingSessionTransport{}
	firstState := newConnState()
	var firstPlain bin.Buffer
	firstConn, err := s.handleEncrypted(context.Background(), firstTransport, firstState, nil, &stored, firstFrame, &firstPlain)
	if err != nil {
		t.Fatalf("first handleEncrypted: %v", err)
	}
	select {
	case <-handler.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first RPC owner did not start")
	}

	secondFrame, _ := encryptedRPCFrameForBarrierTest(t, key, salt, sessionID, msgID)
	secondTransport := &collectingSessionTransport{}
	secondState := newConnState()
	var secondPlain bin.Buffer
	type handleResult struct {
		conn *Conn
		err  error
	}
	secondDone := make(chan handleResult, 1)
	go func() {
		conn, handleErr := s.handleEncrypted(context.Background(), secondTransport, secondState, nil, &stored, secondFrame, &secondPlain)
		secondDone <- handleResult{conn: conn, err: handleErr}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !firstConn.isRetired() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !firstConn.isRetired() {
		t.Fatal("replacement did not fence the first physical connection")
	}
	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("overlapping reconnect executed %d business handlers, want 1", got)
	}
	var second handleResult
	select {
	case second = <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("event-driven duplicate admission blocked the replacement read loop")
	}
	if second.err != nil {
		t.Fatalf("second handleEncrypted: %v", second.err)
	}
	if second.conn == nil || !second.conn.isActive() {
		t.Fatalf("second connection was not active after non-blocking admission: %p", second.conn)
	}

	close(handler.release)
	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("cross-connection duplicate business calls = %d, want 1", got)
	}

	deadline = time.Now().Add(3 * time.Second)
	for len(secondTransport.snapshot()) < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	resultCount := 0
	for _, wire := range secondTransport.snapshot() {
		data, decryptErr := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(key, &bin.Buffer{Buf: wire})
		if decryptErr != nil {
			t.Fatalf("decrypt second connection reply: %v", decryptErr)
		}
		plain := &bin.Buffer{Buf: append([]byte(nil), data.Data()...)}
		typeID, peekErr := plain.PeekID()
		if peekErr != nil {
			t.Fatalf("peek second connection reply: %v", peekErr)
		}
		if typeID != proto.ResultTypeID {
			continue
		}
		var result proto.Result
		if decodeErr := result.Decode(plain); decodeErr != nil {
			t.Fatalf("decode replayed rpc_result: %v", decodeErr)
		}
		if result.RequestMessageID != msgID {
			t.Fatalf("replayed rpc_result request id = %d, want %d", result.RequestMessageID, msgID)
		}
		resultCount++
	}
	if resultCount != 1 {
		t.Fatalf("replayed rpc_result count on replacement = %d, want 1", resultCount)
	}
	waitInboundRPCBatchBudget(t, s.rpcScheduler, 0, 0)
	if got := s.rpcResults.flightLimit.snapshot(); got != 0 {
		t.Fatalf("completed reconnect leaked flight claims: %d", got)
	}
	second.conn.ForceClose()
}

func TestCrossConnectionInflightAbortRetriesOnlyAfterOldOwnerStops(t *testing.T) {
	handler := &cancelThenRetryRPC{started: make(chan struct{})}
	s := New(Options{RPC: handler, WriteTimeout: time.Second, RPCTimeout: 5 * time.Second})
	s.rpcScheduler.start()
	t.Cleanup(func() { s.rpcScheduler.stop(time.Second) })

	key := newTestAuthKey(t)
	const (
		salt      = int64(0x55667788)
		sessionID = int64(70004)
	)
	msgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	firstFrame, stored := encryptedRPCFrameForBarrierTest(t, key, salt, sessionID, msgID)
	if err := s.authKeys.Save(context.Background(), stored); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	firstTransport := &collectingSessionTransport{}
	firstState := newConnState()
	var firstPlain bin.Buffer
	firstConn, err := s.handleEncrypted(context.Background(), firstTransport, firstState, nil, &stored, firstFrame, &firstPlain)
	if err != nil {
		t.Fatalf("first handleEncrypted: %v", err)
	}
	select {
	case <-handler.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first cancellation-aware owner did not start")
	}

	secondFrame, _ := encryptedRPCFrameForBarrierTest(t, key, salt, sessionID, msgID)
	secondTransport := &collectingSessionTransport{}
	secondState := newConnState()
	var secondPlain bin.Buffer
	secondConn, err := s.handleEncrypted(context.Background(), secondTransport, secondState, nil, &stored, secondFrame, &secondPlain)
	if err != nil && !errors.Is(err, ErrConnClosed) {
		t.Fatalf("second handleEncrypted: %v", err)
	}
	if secondConn == nil {
		t.Fatal("second handleEncrypted returned no logical connection")
	}
	deadline := time.Now().Add(2 * time.Second)
	for (handler.active.Load() != 0 || !secondConn.isRetired()) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := handler.calls.Load(); got != 1 {
		t.Fatalf("event subscription re-executed aborted owner: calls=%d", got)
	}
	if handler.active.Load() != 0 || secondConn == nil || !secondConn.isRetired() {
		t.Fatalf("abort convergence = active:%d second:%p state:%v", handler.active.Load(), secondConn, secondConn.lifecycleState())
	}

	// The aborted owner has now definitively stopped and the subscribed
	// replacement generation is fenced. A fresh physical retry may acquire the
	// same msg_id and execute, still with max concurrency one.
	thirdFrame, _ := encryptedRPCFrameForBarrierTest(t, key, salt, sessionID, msgID)
	thirdTransport := &collectingSessionTransport{}
	thirdState := newConnState()
	var thirdPlain bin.Buffer
	thirdConn, err := s.handleEncrypted(context.Background(), thirdTransport, thirdState, nil, &stored, thirdFrame, &thirdPlain)
	if err != nil {
		t.Fatalf("third handleEncrypted: %v", err)
	}
	waitForAtomicCalls(t, &handler.calls, 2)
	waitInboundRPCBatchBudget(t, s.rpcScheduler, 0, 0)
	if got := handler.max.Load(); got != 1 {
		t.Fatalf("old and retry handlers overlapped: max active=%d, want 1", got)
	}
	if got := s.rpcResults.flightLimit.snapshot(); got != 0 {
		t.Fatalf("sequential retry leaked flight claims: %d", got)
	}

	resultCount := 0
	deadline = time.Now().Add(2 * time.Second)
	for len(thirdTransport.snapshot()) < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	for _, wire := range thirdTransport.snapshot() {
		data, decryptErr := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(key, &bin.Buffer{Buf: wire})
		if decryptErr != nil {
			t.Fatalf("decrypt sequential-retry reply: %v", decryptErr)
		}
		plain := &bin.Buffer{Buf: append([]byte(nil), data.Data()...)}
		typeID, peekErr := plain.PeekID()
		if peekErr != nil {
			t.Fatalf("peek sequential-retry reply: %v", peekErr)
		}
		if typeID == proto.ResultTypeID {
			resultCount++
		}
	}
	if resultCount != 1 {
		t.Fatalf("sequential retry result count = %d, want 1", resultCount)
	}
	if !firstConn.isRetired() || !secondConn.isRetired() || thirdConn == nil || !thirdConn.isActive() {
		t.Fatalf("replacement lifecycle = first:%v second:%v third:%p active:%v", firstConn.lifecycleState(), secondConn.lifecycleState(), thirdConn, thirdConn != nil && thirdConn.isActive())
	}
	thirdConn.ForceClose()
}

func waitForAtomicCalls(t *testing.T, calls interface{ Load() int32 }, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := calls.Load(); got != want {
		t.Fatalf("handler calls = %d, want %d", got, want)
	}
}
