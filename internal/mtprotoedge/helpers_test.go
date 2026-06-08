package mtprotoedge

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/exchange"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/transport"
)

// startTestServer 生成 RSA key、监听随机端口并启动 Server，返回监听地址与公钥。
// 通过 t.Cleanup 自动取消并校验优雅退出。opts 的 RSAKey/Logger/DC 会被补默认。
func startTestServer(t *testing.T, opts Options) (addr string, pub exchange.PublicKey, srv *Server) {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}
	opts.RSAKey = rsaKey
	if opts.Logger == nil {
		opts.Logger = zaptest.NewLogger(t)
	}
	if opts.DC == 0 {
		opts.DC = 2
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv = New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("server did not stop after ctx cancel")
		}
	})

	return ln.Addr().String(), exchange.PublicKey{RSA: &rsaKey.PublicKey}, srv
}

// dialHandshake 建立 TCP 连接、完成 intermediate 协商与 MTProto 密钥交换，
// 返回连接、握手结果与 client 端 cipher。连接通过 t.Cleanup 自动关闭。
func dialHandshake(t *testing.T, addr string, dc int, pub exchange.PublicKey) (transport.Conn, exchange.ClientExchangeResult, crypto.Cipher) {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		t.Fatalf("transport handshake: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	auth, err := exchange.NewExchanger(conn, dc).
		WithRand(rand.Reader).
		WithLogger(zaptest.NewLogger(t).Named("client")).
		Client([]exchange.PublicKey{pub}).
		Run(ctx)
	if err != nil {
		t.Fatalf("client exchange: %v", err)
	}
	return conn, auth, crypto.NewClientCipher(rand.Reader)
}

// sendEncrypted 用 client cipher 加密并发送一条带 msgID 的消息。
func sendEncrypted(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, msgID int64, msg bin.Encoder) {
	t.Helper()
	sendEncryptedWithSalt(t, conn, cipher, auth, auth.ServerSalt, msgID, msg)
}

// sendEncryptedWithSalt 用指定 salt 加密并发送一条消息。
func sendEncryptedWithSalt(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, salt, msgID int64, msg bin.Encoder) {
	t.Helper()
	body, seqNo := encodeClientMessageForTest(t, msg)
	sendEncryptedWithSaltAndSeq(t, conn, cipher, auth, salt, msgID, seqNo, body)
}

func sendEncryptedWithSeq(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, msgID int64, seqNo int32, msg bin.Encoder) {
	t.Helper()
	body := encodeClientMessageBodyForTest(t, msg)
	sendEncryptedWithSaltAndSeq(t, conn, cipher, auth, auth.ServerSalt, msgID, seqNo, body)
}

func sendEncryptedWithSaltAndSeq(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, salt, msgID int64, seqNo int32, body []byte) {
	t.Helper()
	sendEncryptedWithSessionSaltAndSeq(t, conn, cipher, auth, auth.SessionID, salt, msgID, seqNo, body)
}

func sendEncryptedWithSessionSaltAndSeq(t *testing.T, conn transport.Conn, cipher crypto.Cipher, auth exchange.ClientExchangeResult, sessionID, salt, msgID int64, seqNo int32, body []byte) {
	t.Helper()
	var buf bin.Buffer
	if err := cipher.Encrypt(auth.AuthKey, crypto.EncryptedMessageData{
		Salt:                   salt,
		SessionID:              sessionID,
		MessageID:              msgID,
		SeqNo:                  seqNo,
		MessageDataLen:         int32(len(body)),
		MessageDataWithPadding: body,
	}, &buf); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Send(ctx, &buf); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func encodeClientMessageForTest(t *testing.T, msg bin.Encoder) ([]byte, int32) {
	t.Helper()
	raw := encodeClientMessageBodyForTest(t, msg)
	typeID, err := (&bin.Buffer{Buf: raw}).PeekID()
	if err != nil {
		t.Fatalf("peek encrypted message type: %v", err)
	}
	if container, ok := msg.(*proto.MessageContainer); ok {
		return raw, clientContainerSeqNoForTest(container)
	}
	if clientMessageNeedsAck(typeID) {
		return raw, 1
	}
	return raw, 0
}

func encodeClientMessageBodyForTest(t *testing.T, msg bin.Encoder) []byte {
	t.Helper()
	var body bin.Buffer
	if err := msg.Encode(&body); err != nil {
		t.Fatalf("encode encrypted message: %v", err)
	}
	return body.Copy()
}

func clientContainerSeqNoForTest(container *proto.MessageContainer) int32 {
	var maxSeq int32
	for _, msg := range container.Messages {
		if seq := int32(msg.SeqNo); seq > maxSeq {
			maxSeq = seq
		}
	}
	if maxSeq%2 != 0 {
		maxSeq++
	}
	return maxSeq
}

// collectReplies 读取并解密 server 回发的消息，按 TypeID 收集明文 buffer，
// 直到见到 wantID（含）或达到上限。用于断言一次请求触发的多条响应
// （new_session_created / 业务响应 / msgs_ack）。
func collectReplies(t *testing.T, conn transport.Conn, cipher crypto.Cipher, key crypto.AuthKey, wantID uint32) map[uint32]*bin.Buffer {
	t.Helper()
	got := make(map[uint32]*bin.Buffer)
	for i := 0; i < 8; i++ {
		_, id, plain := readServerMessage(t, conn, cipher, key)
		got[id] = plain
		if id == wantID {
			break
		}
	}
	return got
}

func readServerMessage(t *testing.T, conn transport.Conn, cipher crypto.Cipher, key crypto.AuthKey) (*crypto.EncryptedMessageData, uint32, *bin.Buffer) {
	t.Helper()
	var buf bin.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := conn.Recv(ctx, &buf)
	cancel()
	if err != nil {
		t.Fatalf("recv server message: %v", err)
	}
	data, err := cipher.DecryptFromBuffer(key, &buf)
	if err != nil {
		t.Fatalf("decrypt server message: %v", err)
	}
	plain := append([]byte(nil), data.Data()...)
	id, err := (&bin.Buffer{Buf: plain}).PeekID()
	if err != nil {
		t.Fatalf("peek server message: %v", err)
	}
	return data, id, &bin.Buffer{Buf: plain}
}

// mustHave 断言 replies 含指定 TypeID 的消息并返回其 buffer。
func mustHave(t *testing.T, replies map[uint32]*bin.Buffer, id uint32, name string) *bin.Buffer {
	t.Helper()
	b, ok := replies[id]
	if !ok {
		t.Fatalf("missing %s (%#x)", name, id)
	}
	return b
}
