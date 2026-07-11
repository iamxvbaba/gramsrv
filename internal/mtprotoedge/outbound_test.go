package mtprotoedge

import (
	"bytes"
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
	"github.com/gotd/td/transport"
)

type failAfterTransport struct {
	failAt atomic.Int32
	sends  atomic.Int32
	stored atomic.Int32
	closes atomic.Int32
	mu     sync.Mutex
	last   []byte
}

type blockingOutboundTransport struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	sends   atomic.Int32
}

type blockingEncodeProbe struct {
	started chan struct{}
	release <-chan struct{}
	active  atomic.Int32
	max     atomic.Int32
}

func (e *blockingEncodeProbe) Encode(b *bin.Buffer) error {
	active := e.active.Add(1)
	for {
		max := e.max.Load()
		if active <= max || e.max.CompareAndSwap(max, active) {
			break
		}
	}
	e.started <- struct{}{}
	<-e.release
	e.active.Add(-1)
	b.PutID(tg.UpdatesTooLongTypeID)
	return nil
}

func newBlockingOutboundTransport() *blockingOutboundTransport {
	return &blockingOutboundTransport{started: make(chan struct{}), release: make(chan struct{})}
}

func TestOutboundEncodingHasProcessWideConcurrencyBudget(t *testing.T) {
	const extra = 8
	total := defaultOutboundEncodeConcurrency + extra
	release := make(chan struct{})
	probe := &blockingEncodeProbe{
		started: make(chan struct{}, total),
		release: release,
	}
	errs := make(chan error, total)
	for range total {
		go func() {
			_, err := encodeOutboundMessage(probe)
			errs <- err
		}()
	}

	for range defaultOutboundEncodeConcurrency {
		select {
		case <-probe.started:
		case <-time.After(time.Second):
			t.Fatal("encode workers did not fill concurrency budget")
		}
	}
	select {
	case <-probe.started:
		t.Fatalf("more than %d outbound encodes ran concurrently", defaultOutboundEncodeConcurrency)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	for range total {
		if err := <-errs; err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	if got := probe.max.Load(); got != defaultOutboundEncodeConcurrency {
		t.Fatalf("peak concurrent encodes = %d, want %d", got, defaultOutboundEncodeConcurrency)
	}
}

func TestConnectionCloseDoesNotWaitForRunningEncoder(t *testing.T) {
	release := make(chan struct{})
	probe := &blockingEncodeProbe{started: make(chan struct{}, 1), release: release}
	c := &Conn{metrics: NopMetrics{}}
	c.startOutbound()
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- c.Send(context.Background(), proto.MessageFromServer, probe)
	}()
	select {
	case <-probe.started:
	case <-time.After(time.Second):
		t.Fatal("encoder did not start")
	}

	closeDone := make(chan struct{})
	go func() {
		c.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Conn.Close waited for external Encoder")
	}
	close(release)
	select {
	case err := <-sendDone:
		if !errors.Is(err, ErrConnClosed) {
			t.Fatalf("send after concurrent close = %v, want ErrConnClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("send did not return after encoder release")
	}
}

func TestOutboundControlVectorsUseGlobalByteBudget(t *testing.T) {
	budget := newOutboundTrackedBudget(16)
	c := &Conn{outboundControlTrackedBudget: budget}
	op, err := c.newOutboundVectorOp(outboundAck, []int64{1, 2})
	if err != nil {
		t.Fatalf("reserve first vector: %v", err)
	}
	if got := budget.snapshot(); got != 16 {
		t.Fatalf("tracked bytes after reserve = %d, want 16", got)
	}
	if _, err := c.newOutboundVectorOp(outboundResend, []int64{3}); !errors.Is(err, ErrOutboundTrackedBudget) {
		t.Fatalf("reserve over budget error = %v, want %v", err, ErrOutboundTrackedBudget)
	}
	op.releaseReservation(budget)
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("tracked bytes after release = %d, want 0", got)
	}
}

func TestEncodedControlFramesUseIndependentBudgetForQueuedAndPendingLifetime(t *testing.T) {
	bodyBudget := newOutboundTrackedBudget(4)
	controlBudget := newOutboundTrackedBudget(256)
	tr := &failAfterTransport{}
	c := newOutboundTestConn(t, tr, bodyBudget)
	c.outboundControlTrackedBudget = controlBudget
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// One content frame fills the ordinary body budget and remains pending.
	if err := c.Send(ctx, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
		t.Fatalf("fill body budget: %v", err)
	}
	first, err := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(c.key, &bin.Buffer{Buf: tr.lastFrame()})
	if err != nil {
		t.Fatalf("decrypt ordinary frame: %v", err)
	}
	if got := bodyBudget.snapshot(); got != 4 {
		t.Fatalf("body budget = %d, want saturated 4", got)
	}

	created := &mt.NewSessionCreated{FirstMsgID: 1, UniqueID: 2, ServerSalt: 3}
	encodedCreated, err := encodeOutboundMessageWithoutSlot(created)
	if err != nil {
		t.Fatalf("encode new_session_created: %v", err)
	}
	if err := c.SendAsync(ctx, proto.MessageFromServer, created); err != nil {
		t.Fatalf("new_session_created under saturated body budget: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for tr.stored.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := tr.stored.Load(); got != 2 {
		t.Fatalf("completed physical sends = %d, want 2", got)
	}
	second, err := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(c.key, &bin.Buffer{Buf: tr.lastFrame()})
	if err != nil {
		t.Fatalf("decrypt control frame: %v", err)
	}
	if got := bodyBudget.snapshot(); got != 4 {
		t.Fatalf("body budget after control send = %d, want unchanged 4", got)
	}
	if got := controlBudget.snapshot(); got != int64(len(encodedCreated.body)) {
		t.Fatalf("control pending budget = %d, want new_session_created body %d", got, len(encodedCreated.body))
	}
	select {
	case <-c.outboundDone:
		t.Fatal("ordinary body pressure closed a healthy connection")
	default:
	}

	// Pong is non-pending, but must also remain admissible and return its control bytes after write.
	if err := c.SendAsync(ctx, proto.MessageServerResponse, &mt.Pong{MsgID: 4, PingID: 5}); err != nil {
		t.Fatalf("pong under saturated body budget: %v", err)
	}
	deadline = time.Now().Add(time.Second)
	for tr.stored.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := tr.stored.Load(); got != 3 {
		t.Fatalf("completed physical sends after pong = %d, want 3", got)
	}
	if got := controlBudget.snapshot(); got != int64(len(encodedCreated.body)) {
		t.Fatalf("control budget after non-pending pong = %d, want pending %d", got, len(encodedCreated.body))
	}

	c.AckServerMessages([]int64{first.MessageID, second.MessageID})
	deadline = time.Now().Add(time.Second)
	for (bodyBudget.snapshot() != 0 || controlBudget.snapshot() != 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := bodyBudget.snapshot(); got != 0 {
		t.Fatalf("body budget after ACK = %d, want 0", got)
	}
	if got := controlBudget.snapshot(); got != 0 {
		t.Fatalf("control budget after ACK = %d, want 0", got)
	}
}

func TestOutboundScratchPoolBoundsConcurrentWireCopies(t *testing.T) {
	pool := newOutboundScratchPool(300)
	first, err := pool.acquire(context.Background(), nil, 100) // 3x peak = full budget.
	if err != nil {
		t.Fatalf("acquire first scratch: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := pool.acquire(ctx, nil, 100); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second concurrent acquire = %v, want deadline backpressure", err)
	}
	pool.release(first)
	if got := pool.snapshot(); got != 100 {
		t.Fatalf("idle retained scratch = %d, want 100", got)
	}
	second, err := pool.acquire(context.Background(), nil, 100)
	if err != nil {
		t.Fatalf("reuse retained scratch: %v", err)
	}
	pool.release(second)
	if got := pool.snapshot(); got != 100 {
		t.Fatalf("scratch after reuse = %d, want one bounded idle buffer", got)
	}
}

func TestOutboundScratchAdmissionUsesWriteTimeoutWithoutClosingHealthyConnection(t *testing.T) {
	wireBytes := encryptedOutboundWireLen(4)
	pool := newOutboundScratchPool(int64(wireBytes * 3))
	blocker, err := pool.acquire(context.Background(), nil, wireBytes)
	if err != nil {
		t.Fatalf("occupy shared scratch budget: %v", err)
	}

	tr := &failAfterTransport{}
	c := newOutboundTestConn(t, tr, newOutboundTrackedBudget(1<<20))
	c.outboundScratchPool = pool
	c.writeTimeout = 25 * time.Millisecond

	start := time.Now()
	err = c.Send(context.Background(), proto.MessageFromServer, &tg.UpdatesTooLong{})
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("scratch admission err = %v, want deadline exceeded", err)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("scratch admission waited %v, want writeTimeout-bounded wait", elapsed)
	}
	if got := tr.sends.Load(); got != 0 {
		t.Fatalf("writer called %d times without scratch, want 0", got)
	}
	if c.terminal.Load() {
		t.Fatal("scratch admission timeout terminally closed a healthy connection")
	}
	select {
	case <-c.outboundDone:
		t.Fatal("outbound actor exited after pre-write scratch timeout")
	default:
	}

	pool.release(blocker)
	c.writeTimeout = time.Second
	if err := c.Send(context.Background(), proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
		t.Fatalf("send after scratch capacity returned: %v", err)
	}
	if got := tr.sends.Load(); got != 1 {
		t.Fatalf("writer calls after recovery = %d, want 1", got)
	}
}

func (t *blockingOutboundTransport) Send(context.Context, *bin.Buffer) error {
	if t.sends.Add(1) == 1 {
		close(t.started)
	}
	<-t.release
	return io.ErrClosedPipe
}

func (t *blockingOutboundTransport) Recv(context.Context, *bin.Buffer) error { return io.EOF }
func (t *blockingOutboundTransport) Close() error {
	t.once.Do(func() { close(t.release) })
	return nil
}

func (t *failAfterTransport) Send(_ context.Context, b *bin.Buffer) error {
	n := t.sends.Add(1)
	if failAt := t.failAt.Load(); failAt > 0 && n >= failAt {
		return io.ErrClosedPipe
	}
	t.mu.Lock()
	t.last = append(t.last[:0], b.Raw()...)
	t.mu.Unlock()
	t.stored.Add(1)
	return nil
}

func (t *failAfterTransport) Recv(context.Context, *bin.Buffer) error { return io.EOF }
func (t *failAfterTransport) Close() error {
	t.closes.Add(1)
	return nil
}

func (t *failAfterTransport) lastFrame() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.last...)
}

func newOutboundFailureTestConn(t *testing.T, tr transport.Conn) *Conn {
	return newOutboundTestConn(t, tr, nil)
}

func newOutboundTestConn(t *testing.T, tr transport.Conn, budget *outboundTrackedBudget) *Conn {
	t.Helper()
	var key crypto.Key
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	c := &Conn{
		transport:             tr,
		writer:                tr,
		cipher:                crypto.NewServerCipher(rand.Reader),
		msgID:                 proto.NewMessageIDGen(time.Now),
		writeTimeout:          time.Second,
		metrics:               NopMetrics{},
		key:                   key.WithID(),
		salt:                  123,
		sessionID:             456,
		outboundTrackedBudget: budget,
	}
	c.startOutbound()
	t.Cleanup(c.Close)
	return c
}

func TestOutboundQueueBackingUsesSmallConfigurableBounds(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		c := &Conn{metrics: NopMetrics{}}
		c.startOutbound()
		defer c.Close()
		if got := cap(c.outbound); got != defaultOutboundQueueSize {
			t.Fatalf("normal queue cap = %d, want %d", got, defaultOutboundQueueSize)
		}
		if got := cap(c.outboundControl); got != defaultOutboundControlQueueSize {
			t.Fatalf("control queue cap = %d, want %d", got, defaultOutboundControlQueueSize)
		}
	})

	t.Run("configured", func(t *testing.T) {
		c := &Conn{
			metrics:                  NopMetrics{},
			outboundQueueSize:        7,
			outboundControlQueueSize: 3,
		}
		c.startOutbound()
		defer c.Close()
		if got := cap(c.outbound); got != 7 {
			t.Fatalf("normal queue cap = %d, want 7", got)
		}
		if got := cap(c.outboundControl); got != 3 {
			t.Fatalf("control queue cap = %d, want 3", got)
		}
	})
}

func TestOutboundOptionsDefaults(t *testing.T) {
	opts := Options{}
	opts.setDefaults()
	if opts.OutboundQueueSize != 128 || opts.OutboundControlQueueSize != 32 {
		t.Fatalf("outbound queue defaults = %d/%d, want 128/32", opts.OutboundQueueSize, opts.OutboundControlQueueSize)
	}
	if opts.OutboundTrackedGlobalMaxBytes != 512<<20 {
		t.Fatalf("outbound tracked default = %d, want %d", opts.OutboundTrackedGlobalMaxBytes, 512<<20)
	}
}

func TestServerNewConnectionsShareOutboundBudgetAndQueueLimits(t *testing.T) {
	srv := New(Options{
		OutboundQueueSize:             7,
		OutboundControlQueueSize:      3,
		OutboundTrackedGlobalMaxBytes: 20,
	})
	var rawKey crypto.Key
	key := rawKey.WithID()
	c1 := srv.newConn(nil, key, 1, 1)
	c2 := srv.newConn(nil, key, 2, 1)
	defer c1.Close()
	defer c2.Close()

	if cap(c1.outbound) != 7 || cap(c1.outboundControl) != 3 || cap(c2.outbound) != 7 || cap(c2.outboundControl) != 3 {
		t.Fatalf("server queue caps = %d/%d and %d/%d, want 7/3",
			cap(c1.outbound), cap(c1.outboundControl), cap(c2.outbound), cap(c2.outboundControl))
	}
	if c1.outboundTrackedBudget != srv.outboundTrackedBudget || c2.outboundTrackedBudget != srv.outboundTrackedBudget {
		t.Fatal("server connections did not receive the shared outbound tracking budget")
	}
	if got := srv.outboundTrackedBudget.maxBytes; got != 20 {
		t.Fatalf("server outbound tracked max = %d, want 20", got)
	}
}

func TestEncryptOutboundFrameDecryptsWithGotdCipher(t *testing.T) {
	var key crypto.Key
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	authKey := key.WithID()
	body := mustEncodeTL(t, &mt.NewSessionCreated{
		FirstMsgID: 111,
		UniqueID:   222,
		ServerSalt: 333,
	})
	c := &Conn{
		cipher:    crypto.NewServerCipher(rand.Reader),
		key:       authKey,
		salt:      12345,
		sessionID: 67890,
	}
	out, err := c.encryptOutboundFrame(&outboundFrame{
		msgID:  7649066000000000001,
		seqNo:  1,
		typeID: mt.NewSessionCreatedTypeID,
		body:   body,
	})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	data, err := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(authKey, &bin.Buffer{Buf: append([]byte(nil), out.Raw()...)})
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if data.Salt != c.salt || data.SessionID != c.sessionID {
		t.Fatalf("salt/session = %d/%d, want %d/%d", data.Salt, data.SessionID, c.salt, c.sessionID)
	}
	if got := data.Data(); !bytes.Equal(got, body) {
		t.Fatalf("body = %x, want %x", got, body)
	}
}

func TestOutboundActorSerializesConcurrentSends(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	sendEncrypted(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), &mt.PingRequest{PingID: 1})
	collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)
	srv.Conns().SetReceivesUpdates(auth.SessionID, true)

	const sends = 64
	var wg sync.WaitGroup
	errs := make(chan error, sends)
	for i := 0; i < sends; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errs <- srv.Conns().PushToSession(ctx, auth.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("push: %v", err)
		}
	}

	var prevMsgID int64
	var prevSeqNo int32 = -1
	for i := 0; i < sends; i++ {
		data, id, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
		if id != tg.UpdatesTooLongTypeID {
			t.Fatalf("message %d type = %#x, want updatesTooLong", i, id)
		}
		if i > 0 && data.MessageID <= prevMsgID {
			t.Fatalf("message %d msg_id = %d after %d, want strictly increasing", i, data.MessageID, prevMsgID)
		}
		if data.SeqNo%2 != 1 {
			t.Fatalf("message %d seq_no = %d, want odd content-related seq_no", i, data.SeqNo)
		}
		if i > 0 && data.SeqNo <= prevSeqNo {
			t.Fatalf("message %d seq_no = %d after %d, want increasing", i, data.SeqNo, prevSeqNo)
		}
		prevMsgID = data.MessageID
		prevSeqNo = data.SeqNo
	}
}

func TestOutboundWriteErrorTerminallyClosesWithoutActorDeadlock(t *testing.T) {
	tr := &failAfterTransport{}
	tr.failAt.Store(1)
	c := newOutboundFailureTestConn(t, tr)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := c.Send(ctx, proto.MessageFromServer, &tg.UpdatesTooLong{}); err == nil {
		t.Fatal("Send unexpectedly succeeded")
	}
	select {
	case <-c.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("outbound actor deadlocked while terminalizing its own write error")
	}
	if got := tr.closes.Load(); got != 1 {
		t.Fatalf("transport closes = %d, want 1", got)
	}
	if err := c.Send(ctx, proto.MessageFromServer, &tg.UpdatesTooLong{}); !errors.Is(err, ErrConnClosed) {
		t.Fatalf("second Send err = %v, want ErrConnClosed", err)
	}
	if got := tr.sends.Load(); got != 1 {
		t.Fatalf("physical sends after terminal error = %d, want 1", got)
	}
}

func TestOutboundResendWriteErrorTerminallyCloses(t *testing.T) {
	tr := &failAfterTransport{}
	c := newOutboundFailureTestConn(t, tr)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := c.Send(ctx, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
		t.Fatalf("initial Send: %v", err)
	}
	data, err := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(c.key, &bin.Buffer{Buf: tr.lastFrame()})
	if err != nil {
		t.Fatalf("decrypt initial frame: %v", err)
	}
	tr.failAt.Store(2)
	if _, err := c.ResendMessages(ctx, []int64{data.MessageID}); err == nil {
		t.Fatal("ResendMessages unexpectedly succeeded")
	}
	select {
	case <-c.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("outbound actor did not exit after resend write error")
	}
	if got := tr.closes.Load(); got != 1 {
		t.Fatalf("transport closes = %d, want 1", got)
	}
}

func TestOutboundTrackedBudgetSharedAcrossConnections(t *testing.T) {
	budget := newOutboundTrackedBudget(12)
	tr1 := &failAfterTransport{}
	tr2 := &failAfterTransport{}
	c1 := newOutboundTestConn(t, tr1, budget)
	c2 := newOutboundTestConn(t, tr2, budget)
	body := &encodedOutboundMessage{body: make([]byte, 8), typeID: tg.UpdatesTooLongTypeID}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := c1.SendEncoded(ctx, proto.MessageFromServer, body); err != nil {
		t.Fatalf("first connection send: %v", err)
	}
	if got := budget.snapshot(); got != 8 {
		t.Fatalf("tracked bytes after first connection = %d, want 8", got)
	}
	if err := c2.SendEncoded(ctx, proto.MessageFromServer, body); !errors.Is(err, ErrOutboundTrackedBudget) && !errors.Is(err, ErrConnClosed) {
		t.Fatalf("second connection send err = %v, want tracked budget/closed", err)
	}
	select {
	case <-c2.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("budget-exhausted connection did not terminate")
	}
	if got := tr2.sends.Load(); got != 0 {
		t.Fatalf("budget-exhausted connection wrote %d frames, want 0", got)
	}
	if got := budget.snapshot(); got != 8 {
		t.Fatalf("tracked bytes after second rejection = %d, want first connection's 8", got)
	}

	c1.Close()
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("tracked bytes after first connection close = %d, want 0", got)
	}
}

func TestOutboundTrackedBudgetReleaseBroadcastsToAllWaiters(t *testing.T) {
	const waiters = 8
	budget := newOutboundTrackedBudget(waiters)
	if !budget.reserve(waiters) {
		t.Fatal("reserve initial saturated budget")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	results := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			results <- budget.waitReserve(ctx, nil, 1)
		}()
	}

	deadline := time.Now().Add(time.Second)
	for {
		budget.wakeMu.Lock()
		got := budget.wake.waiters
		budget.wakeMu.Unlock()
		if got == waiters {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscribed waiters = %d, want %d", got, waiters)
		}
		time.Sleep(time.Millisecond)
	}

	// One batch release creates capacity for every waiter. A single-token notification strands
	// seven of them forever because successful reservations do not produce another wake-up.
	budget.release(waiters)
	for i := 0; i < waiters; i++ {
		if err := <-results; err != nil {
			t.Fatalf("waiter %d: %v", i, err)
		}
	}
	if got := budget.snapshot(); got != waiters {
		t.Fatalf("reserved bytes after broadcast = %d, want %d", got, waiters)
	}
	budget.release(waiters)
}

func TestOutboundGlobalBudgetIncludesQueuedBodies(t *testing.T) {
	budget := newOutboundTrackedBudget(24)
	tr := newBlockingOutboundTransport()
	c := newOutboundTestConn(t, tr, budget)
	body := &encodedOutboundMessage{body: make([]byte, 8), typeID: tg.UpdatesTooLongTypeID}

	if err := c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer, body, 0); err != nil {
		t.Fatalf("enqueue writing body: %v", err)
	}
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("outbound actor did not start blocked write")
	}
	for i := 0; i < 2; i++ {
		if err := c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer, body, 0); err != nil {
			t.Fatalf("enqueue queued body %d: %v", i, err)
		}
	}
	if got := budget.snapshot(); got != 24 {
		t.Fatalf("writing + queued budget = %d, want 24", got)
	}
	if err := c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer, body, 0); !errors.Is(err, ErrOutboundTrackedBudget) {
		t.Fatalf("over-budget enqueue err = %v, want ErrOutboundTrackedBudget", err)
	}
	select {
	case <-c.outboundDone:
		t.Fatal("best-effort global pressure terminated a healthy connection")
	case <-time.After(50 * time.Millisecond):
	}
	if got := budget.snapshot(); got != 24 {
		t.Fatalf("budget after non-terminal rejection = %d, want existing 24", got)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("close blocking transport: %v", err)
	}
	select {
	case <-c.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("outbound actor did not stop after transport failure")
	}
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("budget after transport close = %d, want zero", got)
	}
}

func TestOutboundOversizedBodyRejectedBeforeEncryption(t *testing.T) {
	budget := newOutboundTrackedBudget(64 << 20)
	tr := &failAfterTransport{}
	c := newOutboundTestConn(t, tr, budget)
	body := &encodedOutboundMessage{body: make([]byte, maxOutboundBodyBytes+1), typeID: tg.UpdatesTooLongTypeID}
	err := c.SendEncoded(context.Background(), proto.MessageFromServer, body)
	if !errors.Is(err, ErrOutboundMessageTooLarge) {
		t.Fatalf("oversized outbound err = %v, want ErrOutboundMessageTooLarge", err)
	}
	if got := tr.sends.Load(); got != 0 {
		t.Fatalf("oversized outbound wrote %d frames, want zero", got)
	}
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("oversized outbound reserved %d bytes, want zero", got)
	}
}

func TestOutboundCloseRaceDrainsEveryProducerReservation(t *testing.T) {
	budget := newOutboundTrackedBudget(1 << 20)
	c := newOutboundTestConn(t, &failAfterTransport{}, budget)
	body := &encodedOutboundMessage{body: make([]byte, 128), typeID: tg.UpdatesTooLongTypeID}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = c.SendBestEffortEncoded(context.Background(), proto.MessageFromServer, body, 0)
		}()
	}
	close(start)
	c.Close()
	wg.Wait()
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("outbound budget after close/enqueue race = %d, want zero", got)
	}
}

func TestOutboundTrackedBudgetAckAndCloseReturnExactly(t *testing.T) {
	t.Run("ack", func(t *testing.T) {
		budget := newOutboundTrackedBudget(64)
		tr := &failAfterTransport{}
		c := newOutboundTestConn(t, tr, budget)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		body := &encodedOutboundMessage{body: make([]byte, 12), typeID: tg.UpdatesTooLongTypeID}
		if err := c.SendEncoded(ctx, proto.MessageFromServer, body); err != nil {
			t.Fatalf("send: %v", err)
		}
		if got := budget.snapshot(); got != 12 {
			t.Fatalf("tracked bytes after send = %d, want 12", got)
		}
		data, err := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(c.key, &bin.Buffer{Buf: tr.lastFrame()})
		if err != nil {
			t.Fatalf("decrypt frame: %v", err)
		}
		c.AckServerMessages([]int64{data.MessageID})
		deadline := time.Now().Add(time.Second)
		for budget.snapshot() != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got := budget.snapshot(); got != 0 {
			t.Fatalf("tracked bytes after ack = %d, want 0", got)
		}
	})

	t.Run("close", func(t *testing.T) {
		budget := newOutboundTrackedBudget(64)
		c := newOutboundTestConn(t, &failAfterTransport{}, budget)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		body := &encodedOutboundMessage{body: make([]byte, 12), typeID: tg.UpdatesTooLongTypeID}
		if err := c.SendEncoded(ctx, proto.MessageFromServer, body); err != nil {
			t.Fatalf("send: %v", err)
		}
		if got := budget.snapshot(); got != 12 {
			t.Fatalf("tracked bytes after send = %d, want 12", got)
		}
		c.Close()
		if got := budget.snapshot(); got != 0 {
			t.Fatalf("tracked bytes after close = %d, want 0", got)
		}
	})
}

func TestOutboundTrackedBudgetWriteFailureReturnsReservation(t *testing.T) {
	budget := newOutboundTrackedBudget(64)
	tr := &failAfterTransport{}
	tr.failAt.Store(1)
	c := newOutboundTestConn(t, tr, budget)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	body := &encodedOutboundMessage{body: make([]byte, 12), typeID: tg.UpdatesTooLongTypeID}
	if err := c.SendEncoded(ctx, proto.MessageFromServer, body); err == nil {
		t.Fatal("send unexpectedly succeeded")
	}
	select {
	case <-c.outboundDone:
	case <-time.After(time.Second):
		t.Fatal("write-failed connection did not terminate")
	}
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("tracked bytes after write failure = %d, want 0", got)
	}
}

func TestOutboundStateEvictionReturnsTrackedBudget(t *testing.T) {
	budget := newOutboundTrackedBudget(64)
	state := newOutboundStateWithLimits(budget, 2, 8)
	defer state.releaseAll()
	frames := make([]*outboundFrame, 0, 3)
	for id := int64(1); id <= 3; id++ {
		frame := &outboundFrame{msgID: id, body: make([]byte, 4), reservedBytes: 4}
		frames = append(frames, frame)
		if !budget.reserve(len(frame.body)) {
			t.Fatalf("reserve frame %d", id)
		}
		dropped := state.addReserved(frame)
		if id < 3 && dropped != 0 {
			t.Fatalf("frame %d dropped %d, want 0", id, dropped)
		}
		if id == 3 && dropped != 1 {
			t.Fatalf("third frame dropped %d, want 1", dropped)
		}
	}
	if got := budget.snapshot(); got != 8 {
		t.Fatalf("tracked bytes after eviction = %d, want 8", got)
	}
	if frames[0].body != nil {
		t.Fatal("evicted frame retained its body reference")
	}
	state.releaseAll()
	if got := budget.snapshot(); got != 0 {
		t.Fatalf("tracked bytes after state close = %d, want 0", got)
	}
}

func TestOutboundStateReleasesMixedBodyAndControlBudgets(t *testing.T) {
	bodyBudget := newOutboundTrackedBudget(16)
	controlBudget := newOutboundTrackedBudget(16)
	state := newOutboundStateWithLimits(bodyBudget, 1, 16)

	if !controlBudget.reserve(4) {
		t.Fatal("reserve control frame")
	}
	controlFrame := &outboundFrame{
		msgID:             1,
		body:              make([]byte, 4),
		reservedBytes:     4,
		reservationBudget: controlBudget,
	}
	if dropped := state.addReserved(controlFrame); dropped != 0 {
		t.Fatalf("first add dropped %d, want 0", dropped)
	}

	if !bodyBudget.reserve(4) {
		t.Fatal("reserve body frame")
	}
	bodyFrame := &outboundFrame{
		msgID:             2,
		body:              make([]byte, 4),
		reservedBytes:     4,
		reservationBudget: bodyBudget,
	}
	if dropped := state.addReserved(bodyFrame); dropped != 1 {
		t.Fatalf("second add dropped %d, want control frame eviction", dropped)
	}
	if got := controlBudget.snapshot(); got != 0 {
		t.Fatalf("control budget after eviction = %d, want 0", got)
	}
	if got := bodyBudget.snapshot(); got != 4 {
		t.Fatalf("body budget after eviction = %d, want 4", got)
	}
	if controlFrame.body != nil || controlFrame.reservationBudget != nil {
		t.Fatal("evicted control frame retained body or budget ownership")
	}

	state.releaseAll()
	if got := bodyBudget.snapshot(); got != 0 {
		t.Fatalf("body budget after state close = %d, want 0", got)
	}
	if bodyFrame.body != nil || bodyFrame.reservationBudget != nil {
		t.Fatal("closed body frame retained body or budget ownership")
	}
}

func TestSendBestEffortQueueFullBehavior(t *testing.T) {
	c := &Conn{metrics: NopMetrics{}, outboundTrackedBudget: newOutboundTrackedBudget(1 << 20)}
	c.outbound = make(chan outboundOp, 1)
	c.outboundControl = make(chan outboundOp, 1)
	c.outboundStop = make(chan struct{})
	// 占满普通队列，模拟出站拥塞。
	c.outbound <- outboundOp{}

	if err := c.SendBestEffort(context.Background(), proto.MessageFromServer, &mt.MsgsAck{}, 0); err != ErrOutboundQueueFull {
		t.Fatalf("timeout=0 on full queue: err = %v, want ErrOutboundQueueFull", err)
	}

	start := time.Now()
	if err := c.SendBestEffort(context.Background(), proto.MessageFromServer, &mt.MsgsAck{}, 30*time.Millisecond); err != ErrOutboundQueueFull {
		t.Fatalf("timeout=30ms on full queue: err = %v, want ErrOutboundQueueFull", err)
	}
	if waited := time.Since(start); waited < 30*time.Millisecond {
		t.Fatalf("timeout wait = %v, want >= 30ms", waited)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.SendBestEffort(canceled, proto.MessageFromServer, &mt.MsgsAck{}, time.Second); err != context.Canceled {
		t.Fatalf("canceled ctx on full queue: err = %v, want context.Canceled", err)
	}

	// 腾出队列后快路径应直接入队成功。
	<-c.outbound
	if err := c.SendBestEffort(context.Background(), proto.MessageFromServer, &mt.MsgsAck{}, 0); err != nil {
		t.Fatalf("enqueue after drain: %v", err)
	}
	if got := len(c.outbound); got != 1 {
		t.Fatalf("queued ops = %d, want 1", got)
	}
}

func TestSendAsyncControlQueueBoundary(t *testing.T) {
	c := &Conn{metrics: NopMetrics{}, outboundTrackedBudget: newOutboundTrackedBudget(1 << 20)}
	c.outbound = make(chan outboundOp, 1)
	c.outboundControl = make(chan outboundOp, 1)
	c.outboundStop = make(chan struct{})
	c.outboundControl <- outboundOp{kind: outboundAck}

	if err := c.SendAsync(context.Background(), proto.MessageFromServer, &mt.MsgsAck{}); err != nil {
		t.Fatalf("SendAsync on full control queue: %v", err)
	}
	if got := len(c.outboundControl); got != 1 {
		t.Fatalf("control queue len = %d, want bounded at 1", got)
	}
}

func TestFrameNeedsAckServiceExceptions(t *testing.T) {
	cases := []struct {
		name string
		id   uint32
		want bool
	}{
		{name: "pong", id: mt.PongTypeID, want: false},
		{name: "future_salts", id: mt.FutureSaltsTypeID, want: false},
		{name: "msgs_ack", id: mt.MsgsAckTypeID, want: false},
		{name: "updatesTooLong", id: tg.UpdatesTooLongTypeID, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := frameNeedsAck(tc.id); got != tc.want {
				t.Fatalf("frameNeedsAck(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestOutboundResendAndAckState(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	sendEncrypted(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), &mt.PingRequest{PingID: 1})
	collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)
	srv.Conns().SetReceivesUpdates(auth.SessionID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := srv.Conns().PushToSession(ctx, auth.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
		cancel()
		t.Fatalf("push: %v", err)
	}
	cancel()

	original, id, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
	if id != tg.UpdatesTooLongTypeID {
		t.Fatalf("pushed type = %#x, want updatesTooLong", id)
	}

	resendReqID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, resendReqID, 3, &mt.MsgResendReq{MsgIDs: []int64{original.MessageID}})
	resent, resentType, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
	if resentType != tg.UpdatesTooLongTypeID {
		t.Fatalf("resent type = %#x, want updatesTooLong", resentType)
	}
	if resent.MessageID != original.MessageID || resent.SeqNo != original.SeqNo {
		t.Fatalf("resent frame = (msg_id=%d seq=%d), want original (msg_id=%d seq=%d)",
			resent.MessageID, resent.SeqNo, original.MessageID, original.SeqNo)
	}
	_, stateType, stateBuf := readServerMessage(t, conn, cipher, auth.AuthKey)
	if stateType != mt.MsgsStateInfoTypeID {
		t.Fatalf("state type = %#x, want msgs_state_info", stateType)
	}
	assertStateInfo(t, stateBuf, resendReqID, []byte{msgStateReceived})
	_, ackType, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
	if ackType != mt.MsgsAckTypeID {
		t.Fatalf("ack type = %#x, want msgs_ack", ackType)
	}

	sendEncryptedWithSeq(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), 4, &mt.MsgsAck{MsgIDs: []int64{original.MessageID}})
	ackedResendReqID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, ackedResendReqID, 5, &mt.MsgResendReq{MsgIDs: []int64{original.MessageID}})
	_, ackedStateType, ackedStateBuf := readServerMessage(t, conn, cipher, auth.AuthKey)
	if ackedStateType != mt.MsgsStateInfoTypeID {
		t.Fatalf("after ack type = %#x, want msgs_state_info without resend", ackedStateType)
	}
	assertStateInfo(t, ackedStateBuf, ackedResendReqID, []byte{msgStateReceived})
}

func assertStateInfo(t *testing.T, b *bin.Buffer, reqMsgID int64, want []byte) {
	t.Helper()
	var info mt.MsgsStateInfo
	if err := info.Decode(b); err != nil {
		t.Fatalf("decode msgs_state_info: %v", err)
	}
	if info.ReqMsgID != reqMsgID {
		t.Fatalf("msgs_state_info.req_msg_id = %d, want %d", info.ReqMsgID, reqMsgID)
	}
	if string(info.Info) != string(want) {
		t.Fatalf("msgs_state_info.info = %v, want %v", []byte(info.Info), want)
	}
}
