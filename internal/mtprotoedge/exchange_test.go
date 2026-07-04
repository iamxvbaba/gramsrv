package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
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
