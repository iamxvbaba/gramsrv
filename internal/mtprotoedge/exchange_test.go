package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/log/logzap"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/exchange"
	"github.com/gotd/td/mt"
	tgproto "github.com/gotd/td/proto"
	"github.com/gotd/td/proto/codec"
	"github.com/gotd/td/transport"

	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// TestKeyExchange 验证 M1：client 用 server 公钥完成 MTProto 密钥交换，
// 双方得到一致的 auth key 与 server salt，且 server 将其存入 AuthKeyStore。
func TestKeyExchange(t *testing.T) {
	const dc = 2

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	keys := memory.NewAuthKeyStore()
	srv := New(Options{
		Logger:   zaptest.NewLogger(t),
		DC:       dc,
		RSAKey:   rsaKey,
		AuthKeys: keys,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	// client：TCP 拨号 + intermediate 握手，跑 client 端密钥交换。
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		t.Fatalf("transport handshake: %v", err)
	}

	pub := exchange.PublicKey{RSA: &rsaKey.PublicKey}
	exchCtx, ec := context.WithTimeout(context.Background(), 10*time.Second)
	defer ec()
	res, err := exchange.NewExchanger(conn, dc).
		WithRand(rand.Reader).
		WithLogger(logzap.New(zaptest.NewLogger(t).Named("client"))).
		Client([]exchange.PublicKey{pub}).
		Run(exchCtx)
	if err != nil {
		t.Fatalf("client exchange: %v", err)
	}

	// server 在 Run 返回后落库，轮询等待。
	var saved store.AuthKeyData
	found := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		saved, found, _ = keys.Get(context.Background(), res.AuthKey.ID)
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("server did not store auth key %x", res.AuthKey.ID)
	}
	if saved.Value != [256]byte(res.AuthKey.Value) {
		t.Fatal("server auth key value mismatch")
	}
	if saved.ServerSalt != res.ServerSalt {
		t.Fatalf("server salt mismatch: server=%d client=%d", saved.ServerSalt, res.ServerSalt)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

type authKeySaveContextObservation struct {
	hasDeadline bool
	deadline    time.Time
}

type observingAuthKeyStore struct {
	store.AuthKeyStore
	saveContext chan authKeySaveContextObservation
}

func (s *observingAuthKeyStore) Save(ctx context.Context, key store.AuthKeyData) error {
	deadline, hasDeadline := ctx.Deadline()
	select {
	case s.saveContext <- authKeySaveContextObservation{hasDeadline: hasDeadline, deadline: deadline}:
	default:
	}
	return s.AuthKeyStore.Save(ctx, key)
}

type gatedAuthKeyStore struct {
	store.AuthKeyStore
	entered chan store.AuthKeyData
	release chan struct{}
	saveErr error
}

type ownershipFrameConn struct {
	transport.Conn
	frame []byte
}

func (c *ownershipFrameConn) Recv(_ context.Context, b *bin.Buffer) error {
	b.ResetTo(c.frame)
	return nil
}

func TestExchangeEncryptedReplayTransfersFrameOwnership(t *testing.T) {
	backing := make([]byte, 64)
	copy(backing[:8], []byte{1, 2, 3, 4, 5, 6, 7, 8})
	conn := &ownershipFrameConn{frame: backing}
	ex := serverExchangeCompat{conn: conn, timeout: time.Second}
	var b bin.Buffer
	err := ex.readUnencrypted(context.Background(), &b, &compatReqPQ{})
	var encrypted *exchange.UnexpectedEncryptedError
	if !errors.As(err, &encrypted) {
		t.Fatalf("read encrypted frame err = %v, want UnexpectedEncryptedError", err)
	}
	if len(encrypted.Frame) != len(backing) || &encrypted.Frame[0] != &backing[0] {
		t.Fatal("encrypted replay copied the received frame instead of transferring ownership")
	}
	if b.Buf != nil {
		t.Fatal("exchange buffer retained transferred encrypted frame backing")
	}
}

func (s *gatedAuthKeyStore) Save(ctx context.Context, key store.AuthKeyData) error {
	select {
	case s.entered <- key:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.AuthKeyStore.Save(ctx, key)
}

// TestKeyExchangeDoesNotAcknowledgeBeforeAuthKeyCommit pins the protocol commit
// boundary: while durable Save is blocked, the client must not receive DhGenOk
// and therefore must not report a successful exchange.
func TestKeyExchangeDoesNotAcknowledgeBeforeAuthKeyCommit(t *testing.T) {
	base := memory.NewAuthKeyStore()
	keys := &gatedAuthKeyStore{
		AuthKeyStore: base,
		entered:      make(chan store.AuthKeyData, 1),
		release:      make(chan struct{}, 1),
	}
	addr, pub, _ := startTestServer(t, Options{DC: 2, AuthKeys: keys})
	conn := dialTransportOnly(t, addr)

	type exchangeOutcome struct {
		result exchange.ClientExchangeResult
		err    error
	}
	outcome := make(chan exchangeOutcome, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() {
		result, err := exchange.NewExchanger(conn, 2).
			WithRand(rand.Reader).
			Client([]exchange.PublicKey{pub}).
			Run(ctx)
		outcome <- exchangeOutcome{result: result, err: err}
	}()

	var pending store.AuthKeyData
	select {
	case pending = <-keys.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("AuthKeyStore.Save was not reached")
	}
	defer func() {
		select {
		case keys.release <- struct{}{}:
		default:
		}
	}()

	select {
	case got := <-outcome:
		t.Fatalf("client exchange completed before auth key commit: err=%v", got.err)
	case <-time.After(150 * time.Millisecond):
	}
	if _, found, err := base.Get(context.Background(), pending.ID); err != nil {
		t.Fatalf("Get before commit: %v", err)
	} else if found {
		t.Fatal("auth key became visible while durable Save was blocked")
	}

	keys.release <- struct{}{}
	select {
	case got := <-outcome:
		if got.err != nil {
			t.Fatalf("client exchange after commit: %v", got.err)
		}
		if got.result.AuthKey.ID != pending.ID {
			t.Fatalf("committed auth key id = %x, client got %x", pending.ID, got.result.AuthKey.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client exchange did not finish after auth key commit")
	}
	if _, found, err := base.Get(context.Background(), pending.ID); err != nil {
		t.Fatalf("Get after commit: %v", err)
	} else if !found {
		t.Fatal("auth key is not durable after successful client exchange")
	}
}

// TestKeyExchangeAuthKeyCommitFailureWithholdsDhGenOk proves the failure side
// of the same invariant. The client must not observe success if storage rejects
// the key; the server closes this exchange and lets the client retry cleanly.
func TestKeyExchangeAuthKeyCommitFailureWithholdsDhGenOk(t *testing.T) {
	base := memory.NewAuthKeyStore()
	keys := &gatedAuthKeyStore{
		AuthKeyStore: base,
		entered:      make(chan store.AuthKeyData, 1),
		release:      make(chan struct{}, 1),
		saveErr:      errors.New("injected auth key persistence failure"),
	}
	keys.release <- struct{}{}
	addr, pub, _ := startTestServer(t, Options{DC: 2, AuthKeys: keys})
	conn := dialTransportOnly(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := exchange.NewExchanger(conn, 2).
		WithRand(rand.Reader).
		Client([]exchange.PublicKey{pub}).
		Run(ctx)
	if err == nil {
		t.Fatal("client exchange succeeded even though auth key commit failed")
	}

	select {
	case attempted := <-keys.entered:
		if _, found, getErr := base.Get(context.Background(), attempted.ID); getErr != nil {
			t.Fatalf("Get failed key: %v", getErr)
		} else if found {
			t.Fatal("failed auth key commit became visible")
		}
	case <-time.After(time.Second):
		t.Fatal("AuthKeyStore.Save was not attempted")
	}
}

func TestKeyExchangeAuthKeySaveUsesHandshakeDeadline(t *testing.T) {
	const handshakeMax = 10 * time.Second
	observed := make(chan authKeySaveContextObservation, 1)
	keys := &observingAuthKeyStore{
		AuthKeyStore: memory.NewAuthKeyStore(),
		saveContext:  observed,
	}
	addr, pub, _ := startTestServer(t, Options{
		DC:                   2,
		AuthKeys:             keys,
		HandshakeMaxDuration: handshakeMax,
	})

	_, _, _ = dialHandshake(t, addr, 2, pub)
	select {
	case got := <-observed:
		if !got.hasDeadline {
			t.Fatal("AuthKeyStore.Save context has no handshake deadline")
		}
		remaining := time.Until(got.deadline)
		if remaining <= 0 || remaining > handshakeMax {
			t.Fatalf("AuthKeyStore.Save deadline remaining = %v, want (0, %v]", remaining, handshakeMax)
		}
	case <-time.After(time.Second):
		t.Fatal("AuthKeyStore.Save was not called")
	}
}

func TestKeyExchangeAcceptsAndroidMediaTempNegativeDC(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})
	conn := dialTransportOnly(t, addr)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := exchange.NewExchanger(conn, -dc).
		WithTempMode(24 * 60 * 60).
		WithRand(rand.Reader).
		WithLogger(logzap.New(zaptest.NewLogger(t).Named("client"))).
		Client([]exchange.PublicKey{pub}).
		Run(ctx)
	if err != nil {
		t.Fatalf("client exchange: %v", err)
	}

	var saved store.AuthKeyData
	found := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		saved, found, _ = srv.authKeys.Get(context.Background(), res.AuthKey.ID)
		if found {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !found {
		t.Fatalf("server did not store media temp auth key %x", res.AuthKey.ID)
	}
	if saved.Value != [256]byte(res.AuthKey.Value) {
		t.Fatal("server auth key value mismatch")
	}
	if saved.ServerSalt != res.ServerSalt {
		t.Fatalf("server salt mismatch: server=%d client=%d", saved.ServerSalt, res.ServerSalt)
	}
}

func TestKeyExchangeRejectsWrongNegativeTempDC(t *testing.T) {
	ex := serverExchangeCompat{dc: 2, log: zaptest.NewLogger(t)}
	err := ex.validatePQInnerDataDC(&mt.PQInnerDataTempDC{DC: -3})
	var exErr *exchange.ServerExchangeError
	if !errors.As(err, &exErr) {
		t.Fatalf("err = %T %v, want ServerExchangeError", err, err)
	}
	if exErr.Code != codec.CodeWrongDC {
		t.Fatalf("error code = %d, want %d", exErr.Code, codec.CodeWrongDC)
	}
}

func TestKeyExchangeIgnoresUnencryptedMsgsAck(t *testing.T) {
	const dc = 2

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	keys := memory.NewAuthKeyStore()
	srv := New(Options{
		Logger:   zaptest.NewLogger(t),
		DC:       dc,
		RSAKey:   rsaKey,
		AuthKeys: keys,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		t.Fatalf("transport handshake: %v", err)
	}

	pub := exchange.PublicKey{RSA: &rsaKey.PublicKey}
	exchCtx, ec := context.WithTimeout(context.Background(), 10*time.Second)
	defer ec()
	res, err := exchange.NewExchanger(&ackingExchangeConn{Conn: conn, t: t}, dc).
		WithRand(rand.Reader).
		WithLogger(logzap.New(zaptest.NewLogger(t).Named("client"))).
		Client([]exchange.PublicKey{pub}).
		Run(exchCtx)
	if err != nil {
		t.Fatalf("client exchange: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, found, _ := keys.Get(context.Background(), res.AuthKey.ID); found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not store auth key %x", res.AuthKey.ID)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

func TestBufferedExchangePushTransfersFrameOwnershipWithoutCopy(t *testing.T) {
	backing := make([]byte, 64)
	for i := range backing {
		backing[i] = byte(i)
	}
	source := &bin.Buffer{Buf: backing}
	buffered := newBufferedConn(nil)
	buffered.push(source)
	if source.Buf != nil {
		t.Fatal("push retained ownership in the source buffer")
	}

	var got bin.Buffer
	if err := buffered.Recv(context.Background(), &got); err != nil {
		t.Fatalf("Recv pending frame: %v", err)
	}
	if len(got.Buf) != len(backing) || &got.Buf[0] != &backing[0] {
		t.Fatal("pending frame was copied instead of transferring its backing")
	}
	if len(buffered.pending) != 0 || cap(buffered.pending) != 0 {
		t.Fatalf("consumed pending ownership retained: len=%d cap=%d", len(buffered.pending), cap(buffered.pending))
	}
}

func TestBufferedExchangeLargeTrailingMsgsAckReleasesFrameBeforeNextRecv(t *testing.T) {
	encodeUnencrypted := func(msg bin.Encoder, msgID int64) []byte {
		var payload bin.Buffer
		if err := msg.Encode(&payload); err != nil {
			t.Fatalf("encode payload: %v", err)
		}
		var frame bin.Buffer
		if err := (tgproto.UnencryptedMessage{MessageID: msgID, MessageData: payload.Raw()}).Encode(&frame); err != nil {
			t.Fatalf("encode unencrypted frame: %v", err)
		}
		return frame.Copy()
	}
	intermediate := func(frame []byte) []byte {
		packet := make([]byte, bin.Word+len(frame))
		binary.LittleEndian.PutUint32(packet, uint32(len(frame)))
		copy(packet[bin.Word:], frame)
		return packet
	}

	// Make the ignored ack larger than the per-codec retained-buffer threshold. The following
	// small req_pq frame forces bufferedConn to cross the next-Recv ownership boundary while the
	// same destination bin.Buffer is reused.
	ids := make([]int64, 300_000)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	ackFrame := encodeUnencrypted(&mt.MsgsAck{MsgIDs: ids}, 4)
	reqFrame := encodeUnencrypted(&mt.ReqPqMultiRequest{}, 8)
	packet := append(intermediate(ackFrame), intermediate(reqFrame)...)
	budget := newInboundFrameBudget(2 * int64(len(ackFrame)))
	conn, _ := newFrameBudgetTestTransport(packet, &quickAckIntermediateCodec{}, budget)
	buffered := newBufferedConn(conn)

	var got bin.Buffer
	if err := buffered.Recv(context.Background(), &got); err != nil {
		t.Fatalf("Recv after large msgs_ack: %v", err)
	}
	if id, ok := unencryptedPayloadID(&got); !ok || id != mt.ReqPqMultiRequestTypeID {
		t.Fatalf("returned frame type = 0x%x ok=%v, want req_pq_multi", id, ok)
	}
	if used, want := budget.usedBytes(), 2*int64(len(reqFrame)); used != want {
		t.Fatalf("inbound budget after skipped ack = %d, want only next frame %d", used, want)
	}
	if cap(got.Buf) >= len(ackFrame)/2 {
		t.Fatalf("large ignored ack backing retained by next frame: cap=%d ack=%d", cap(got.Buf), len(ackFrame))
	}
	conn.releaseInboundFrame()
	if used := budget.usedBytes(); used != 0 {
		t.Fatalf("inbound budget after final ownership release = %d, want 0", used)
	}
}

type ackingExchangeConn struct {
	transport.Conn
	t *testing.T
}

func (c *ackingExchangeConn) Recv(ctx context.Context, b *bin.Buffer) error {
	if err := c.Conn.Recv(ctx, b); err != nil {
		return err
	}
	c.ackHandshakeMessage(b)
	return nil
}

func (c *ackingExchangeConn) ackHandshakeMessage(frame *bin.Buffer) {
	var msg tgproto.UnencryptedMessage
	copy := &bin.Buffer{Buf: frame.Copy()}
	if err := msg.Decode(copy); err != nil {
		return
	}
	payload := &bin.Buffer{Buf: msg.MessageData}
	id, err := payload.PeekID()
	if err != nil {
		return
	}
	switch id {
	case mt.ResPQTypeID, mt.ServerDHParamsOkTypeID:
	default:
		return
	}

	var ackPayload bin.Buffer
	if err := (&mt.MsgsAck{MsgIDs: []int64{msg.MessageID}}).Encode(&ackPayload); err != nil {
		c.t.Fatalf("encode msgs_ack: %v", err)
	}
	var ackFrame bin.Buffer
	if err := (tgproto.UnencryptedMessage{
		MessageID:   int64(tgproto.NewMessageID(time.Now(), tgproto.MessageFromClient)),
		MessageData: ackPayload.Raw(),
	}).Encode(&ackFrame); err != nil {
		c.t.Fatalf("encode msgs_ack frame: %v", err)
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Conn.Send(sendCtx, &ackFrame); err != nil {
		c.t.Fatalf("send msgs_ack: %v", err)
	}
}

func TestReconnectFakeReqPQThenEncryptedFrame(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})

	firstConn, auth, cipher := dialHandshake(t, addr, dc, pub)
	_ = firstConn.Close()

	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial reconnect: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		t.Fatalf("transport reconnect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var reqPayload bin.Buffer
	nonce, err := randInt128ForTest()
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	if err := (&mt.ReqPqMultiRequest{Nonce: nonce}).Encode(&reqPayload); err != nil {
		t.Fatalf("encode req_pq_multi: %v", err)
	}
	var fakeReq bin.Buffer
	if err := (tgproto.UnencryptedMessage{
		MessageID:   int64(tgproto.NewMessageID(time.Now(), tgproto.MessageFromClient)),
		MessageData: reqPayload.Raw(),
	}).Encode(&fakeReq); err != nil {
		t.Fatalf("encode fake req_pq: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := conn.Send(ctx, &fakeReq); err != nil {
		cancel()
		t.Fatalf("send fake req_pq: %v", err)
	}
	cancel()

	msgGen := tgproto.NewMessageIDGen(time.Now)
	sendEncrypted(t, conn, cipher, auth, msgGen.New(tgproto.MessageFromClient), &mt.PingRequest{PingID: 7})

	var resPQFrame bin.Buffer
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	err = conn.Recv(ctx, &resPQFrame)
	cancel()
	if err != nil {
		t.Fatalf("recv resPQ: %v", err)
	}
	var plain tgproto.UnencryptedMessage
	if err := plain.Decode(&resPQFrame); err != nil {
		t.Fatalf("decode resPQ frame: %v", err)
	}
	if id, err := (&bin.Buffer{Buf: plain.MessageData}).PeekID(); err != nil || id != mt.ResPQTypeID {
		t.Fatalf("resPQ payload id = %#x err=%v, want %#x", id, err, mt.ResPQTypeID)
	}

	got := collectReplies(t, conn, cipher, auth.AuthKey, mt.PongTypeID)
	mustHave(t, got, mt.PongTypeID, "pong after fake req_pq reconnect")
}

func randInt128ForTest() (v bin.Int128, err error) {
	_, err = rand.Read(v[:])
	return v, err
}
