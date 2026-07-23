package mtprotoedge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mtproxy"
	"github.com/iamxvbaba/td/mtproxy/obfuscator"
	"github.com/iamxvbaba/td/proto/codec"
	"github.com/iamxvbaba/td/telegram/dcs"
	"github.com/iamxvbaba/td/transport"
)

// TestServerAcceptAndCodec 验证 M0：
// server 能接受连接、自动协商 codec、读到客户端帧，并在 ctx 取消时优雅退出。
func TestServerAcceptAndCodec(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	frames := make(chan int, 1)
	srv := New(Options{Logger: zaptest.NewLogger(t)})
	srv.onFrame = func(n int) {
		select {
		case frames <- n:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	// 客户端：TCP 拨号 + intermediate 协议握手 + 发送一帧。
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// payload 必须 ≠ 4 字节：codec 把恰好 4 字节的帧当作 transport 协议错误码（checkProtocolError）。
	// 真实 MTProto 帧远大于 4 字节，这里发 8 字节模拟一个普通帧。
	var b bin.Buffer
	b.PutInt32(0x12345678)
	b.PutInt32(0x0badf00d)
	sendCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	if err := conn.Send(sendCtx, &b); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case n := <-frames:
		if n <= 0 {
			t.Fatalf("received empty frame, len = %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive frame in time")
	}

	_ = conn.Close()

	// 验证优雅退出。
	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

// TestServerAcceptObfuscatedAbridged 验证 TDesktop tcpo_only 连接形态：
// 先做 MTProto TCP obfuscation，再在解密后的流上使用 abridged codec。
func TestServerAcceptObfuscatedAbridged(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	frames := make(chan int, 1)
	srv := New(Options{Logger: zaptest.NewLogger(t), ObfuscatedTCP: true})
	srv.onFrame = func(n int) {
		select {
		case frames <- n:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	bad, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("bad dial: %v", err)
	}
	_ = bad.Close()
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-serveErr:
		t.Fatalf("server stopped after bad obfuscated accept: %v", err)
	default:
	}

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	obfs := obfuscator.Obfuscated2(rand.Reader, raw)
	if err := obfs.Handshake((codec.Abridged{}).ObfuscatedTag(), 2, mtproxy.Secret{}); err != nil {
		t.Fatalf("obfuscated handshake: %v", err)
	}
	conn, err := transport.NewProtocol(func() transport.Codec {
		return transport.Abridged.CodecNoHeader()
	}).Handshake(obfs)
	if err != nil {
		t.Fatalf("transport handshake: %v", err)
	}

	var b bin.Buffer
	b.PutInt32(0x12345678)
	b.PutInt32(0x0badf00d)
	sendCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	if err := conn.Send(sendCtx, &b); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case n := <-frames:
		if n <= 0 {
			t.Fatalf("received empty frame, len = %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive frame in time")
	}

	_ = conn.Close()

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

func TestServerObfuscatedTCPAlsoAcceptsPlainIntermediate(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	frames := make(chan int, 1)
	srv := New(Options{Logger: zaptest.NewLogger(t), ObfuscatedTCP: true})
	srv.onFrame = func(n int) {
		select {
		case frames <- n:
		default:
		}
	}

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
		_ = raw.Close()
		t.Fatalf("transport handshake: %v", err)
	}

	var b bin.Buffer
	b.PutInt32(0x12345678)
	b.PutInt32(0x0badf00d)
	sendCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	if err := conn.Send(sendCtx, &b); err != nil {
		t.Fatalf("send plain intermediate: %v", err)
	}

	select {
	case n := <-frames:
		if n != b.Len() {
			t.Fatalf("received frame len = %d, want %d", n, b.Len())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive plain intermediate frame in dual mode")
	}

	_ = conn.Close()

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

func TestServerObfuscatedTCPAlsoAcceptsPlainFull(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	frames := make(chan int, 1)
	srv := New(Options{Logger: zaptest.NewLogger(t), ObfuscatedTCP: true})
	srv.onFrame = func(n int) {
		select {
		case frames <- n:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := transport.Full.Handshake(raw)
	if err != nil {
		_ = raw.Close()
		t.Fatalf("full transport handshake: %v", err)
	}

	var b bin.Buffer
	b.PutInt32(0x12345678)
	b.PutInt32(0x0badf00d)
	sendCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	if err := conn.Send(sendCtx, &b); err != nil {
		t.Fatalf("send plain full: %v", err)
	}

	select {
	case n := <-frames:
		if n != b.Len() {
			t.Fatalf("received frame len = %d, want %d", n, b.Len())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive plain full frame in dual mode")
	}

	_ = conn.Close()

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

func TestServerDualModeBadObfuscatedInitDoesNotBlockPlainClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	frames := make(chan int, 1)
	srv := New(Options{Logger: zaptest.NewLogger(t), ObfuscatedTCP: true})
	srv.onFrame = func(n int) {
		select {
		case frames <- n:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	bad, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("bad dial: %v", err)
	}
	if _, err := bad.Write([]byte{0x40, 0xd5, 0x59, 0xfe}); err != nil {
		t.Fatalf("write bad prefix: %v", err)
	}
	_ = bad.Close()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		_ = raw.Close()
		t.Fatalf("plain transport handshake: %v", err)
	}

	var b bin.Buffer
	b.PutInt32(0x12345678)
	b.PutInt32(0x0badf00d)
	sendCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	defer sc()
	if err := conn.Send(sendCtx, &b); err != nil {
		t.Fatalf("send plain intermediate after bad init: %v", err)
	}

	select {
	case n := <-frames:
		if n != b.Len() {
			t.Fatalf("received frame len = %d, want %d", n, b.Len())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive frame after bad obfuscated init")
	}

	_ = conn.Close()

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

func TestServerAcceptObfuscatedAbridgedQuickAckFrame(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	frames := make(chan int, 1)
	srv := New(Options{Logger: zaptest.NewLogger(t), ObfuscatedTCP: true})
	srv.onFrame = func(n int) {
		select {
		case frames <- n:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	obfs := obfuscator.Obfuscated2(rand.Reader, raw)
	if err := obfs.Handshake((codec.Abridged{}).ObfuscatedTag(), 2, mtproxy.Secret{}); err != nil {
		t.Fatalf("obfuscated handshake: %v", err)
	}

	var b bin.Buffer
	b.PutInt32(0x12345678)
	b.PutInt32(0x0badf00d)
	packet := append([]byte{0x80 | byte(b.Len()/4)}, b.Raw()...)
	if _, err := obfs.Write(packet); err != nil {
		t.Fatalf("write quick ack frame: %v", err)
	}

	select {
	case n := <-frames:
		if n != b.Len() {
			t.Fatalf("received frame len = %d, want %d", n, b.Len())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive quick-ack abridged frame in time")
	}

	_ = raw.Close()

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

func TestServerSamePortWebSocketAndObfuscatedTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	frames := make(chan int, 2)
	srv := New(Options{Logger: zaptest.NewLogger(t), ObfuscatedTCP: true, WebSocket: true})
	srv.onFrame = func(n int) {
		select {
		case frames <- n:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	var wsPayload bin.Buffer
	wsPayload.PutInt32(0x11223344)
	wsPayload.PutInt32(0x55667788)

	wsResolver := dcs.Websocket(dcs.WebsocketOptions{})
	wsConn, err := wsResolver.Primary(context.Background(), 2, dcs.List{
		Domains: map[int]string{
			2: "ws://" + ln.Addr().String() + "/apiws",
		},
	})
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	sendCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	if err := wsConn.Send(sendCtx, &wsPayload); err != nil {
		sc()
		t.Fatalf("websocket send: %v", err)
	}
	sc()
	expectFrameLen(t, frames, wsPayload.Len())
	_ = wsConn.Close()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("tcp dial: %v", err)
	}
	obfs := obfuscator.Obfuscated2(rand.Reader, raw)
	if err := obfs.Handshake((codec.Abridged{}).ObfuscatedTag(), 2, mtproxy.Secret{}); err != nil {
		t.Fatalf("tcp obfuscated handshake: %v", err)
	}
	tcpConn, err := transport.NewProtocol(func() transport.Codec {
		return transport.Abridged.CodecNoHeader()
	}).Handshake(obfs)
	if err != nil {
		t.Fatalf("tcp transport handshake: %v", err)
	}

	var tcpPayload bin.Buffer
	tcpPayload.PutInt32(0x12345678)
	tcpPayload.PutInt32(0x0badf00d)
	sendCtx, sc = context.WithTimeout(context.Background(), 5*time.Second)
	if err := tcpConn.Send(sendCtx, &tcpPayload); err != nil {
		sc()
		t.Fatalf("tcp send: %v", err)
	}
	sc()
	expectFrameLen(t, frames, tcpPayload.Len())
	_ = tcpConn.Close()

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}

func TestSamePortWebSocketTransportRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mux := newSamePortMux(ln, 5*time.Second)
	wsLn, wsHandler := transport.WebsocketListener(ln.Addr())
	httpServer := &http.Server{
		Handler:           websocketRouteHandler(wsHandler, []string{"http://localhost:1234"}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 2)
	go func() { serveErr <- mux.Serve(ctx) }()
	go func() {
		err := httpServer.Serve(mux.HTTP())
		if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()
	defer func() {
		cancel()
		_ = mux.Close()
		_ = httpServer.Close()
		_ = wsLn.Close()
		for i := 0; i < 2; i++ {
			select {
			case err := <-serveErr:
				if err != nil {
					t.Fatalf("serve returned error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("same-port websocket transport did not stop")
			}
		}
	}()

	serverDone := make(chan error, 1)
	go func() {
		l := newCompatTransportListener(nil, wsLn, newInboundFrameBudget(defaultInboundFrameGlobalMaxBytes))
		defer func() { _ = l.Close() }()

		conn, err := l.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = conn.Close() }()

		recvCtx, rc := context.WithTimeout(ctx, 5*time.Second)
		defer rc()
		var got bin.Buffer
		if err := conn.Recv(recvCtx, &got); err != nil {
			serverDone <- err
			return
		}

		var reply bin.Buffer
		reply.PutInt32(0x10203040)
		reply.PutInt32(0x50607080)
		sendCtx, sc := context.WithTimeout(ctx, 5*time.Second)
		defer sc()
		if err := conn.Send(sendCtx, &reply); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	wsResolver := dcs.Websocket(dcs.WebsocketOptions{})
	wsConn, err := wsResolver.Primary(context.Background(), 2, dcs.List{
		Domains: map[int]string{
			2: "ws://" + ln.Addr().String() + "/apiws",
		},
	})
	if err != nil {
		t.Fatalf("websocket dial: %v", err)
	}
	defer func() { _ = wsConn.Close() }()

	var request bin.Buffer
	request.PutInt32(0x11223344)
	request.PutInt32(0x55667788)
	sendCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	if err := wsConn.Send(sendCtx, &request); err != nil {
		sc()
		t.Fatalf("websocket send: %v", err)
	}
	sc()

	var want bin.Buffer
	want.PutInt32(0x10203040)
	want.PutInt32(0x50607080)
	recvCtx, rc := context.WithTimeout(context.Background(), 5*time.Second)
	var got bin.Buffer
	if err := wsConn.Recv(recvCtx, &got); err != nil {
		rc()
		t.Fatalf("websocket recv: %v", err)
	}
	rc()
	if !bytes.Equal(got.Raw(), want.Raw()) {
		t.Fatalf("websocket recv = %x, want %x", got.Raw(), want.Raw())
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server transport: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server transport did not finish")
	}
}

// TestSamePortMuxIdleConnNotReapedBeforeHandshakeTimeout 回归：WebSocket 同端口复用的嗅探
// 读超时（读首 4 字节做 HTTP/TCP 分流）必须对齐 HandshakeIdleTimeout，而不是旧的硬上限 5s。
// 合法 MTProto 客户端（DrKLO）会预开「暖」连接、在有请求前并不立即发 obfuscated2 init；旧实现
// 用 minDuration(5s, handshakeTimeout) 把嗅探压到 5s，比非 mux 路径（serveDetectedConn 用满
// handshakeTimeout）激进 12 倍，会把这些暖连接在 5s 误杀，触发 DrKLO 6s 重连风暴 + EPOLLRDHUP
// + 误判后端不健康回退外部 DNS（见 docs/client-compat-notes.md）。
//
// HandshakeIdleTimeout 必须 >5s 才能暴露旧的截断：连一条裸 TCP、不发任何字节，本地用 6s 读
// deadline。新实现下连接在 8s 嗅探超时前一直存活，故本地 Read 因自身 deadline 超时（net timeout）；
// 旧实现下服务端 5s 即 FIN，本地 Read 会在 6s 前拿到 EOF/reset —— 据此判定回归。
func TestSamePortMuxIdleConnNotReapedBeforeHandshakeTimeout(t *testing.T) {
	addr, _, _ := startTestServer(t, Options{
		WebSocket:            true,
		ObfuscatedTCP:        true,
		HandshakeIdleTimeout: 8 * time.Second, // 必须 >5s 才能区分「对齐 handshakeTimeout」与旧的 5s 截断
	})

	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = raw.Close() }()

	// 不发任何字节，模拟客户端预开、暂未发首帧的暖连接。
	if err := raw.SetReadDeadline(time.Now().Add(6 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	n, err := raw.Read(make([]byte, 1))
	if err == nil {
		t.Fatalf("unexpected %d bytes on idle pre-handshake conn (server should send nothing)", n)
	}
	// 只有「本地读 deadline 超时」才说明连接在 6s 时仍存活（嗅探超时已对齐 8s）。
	// 任何由对端关闭导致的 EOF/reset 都意味着服务端在 handshakeTimeout 前过早回收了连接。
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return
	}
	t.Fatalf("server closed idle pre-handshake conn before handshake idle timeout (err=%v); "+
		"same-port mux sniff timeout must align with HandshakeIdleTimeout, not a 5s cap", err)
}

func expectFrameLen(t *testing.T, frames <-chan int, want int) {
	t.Helper()
	select {
	case n := <-frames:
		if n != want {
			t.Fatalf("received frame len = %d, want %d", n, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not receive frame in time")
	}
}

// TestWebSocketRouteHandlerChecksBrowserOrigin 回归保护 Origin 修复：浏览器发起的 WS 升级
// 必带 Origin（≠ Host），白名单来源需要改写 Origin 通过 coder/websocket，同名单外来源必须 403。
func TestWebSocketRouteHandlerChecksBrowserOrigin(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	_, wsHandler := transport.WebsocketListener(ln.Addr())
	httpServer := &http.Server{Handler: websocketRouteHandler(wsHandler, []string{"http://localhost:1234"})}
	go func() { _ = httpServer.Serve(ln) }()
	defer func() { _ = httpServer.Close() }()

	host := ln.Addr().String()

	// 白名单跨源升级：Origin 指向页面来源（端口/主机不同于 Host）。
	status := wsUpgradeStatus(t, host, "/apiws", "http://localhost:1234")
	if !strings.Contains(status, "101") {
		t.Fatalf("allowed cross-origin /apiws upgrade: got status %q, want 101 Switching Protocols", status)
	}

	status = wsUpgradeStatus(t, host, "/apiws", "http://evil.example")
	if !strings.Contains(status, "403") {
		t.Fatalf("disallowed origin: got status %q, want 403", status)
	}

	// 非白名单路径必须 404，不得升级。
	status = wsUpgradeStatus(t, host, "/nope", "http://localhost:1234")
	if !strings.Contains(status, "404") {
		t.Fatalf("disallowed path: got status %q, want 404", status)
	}
}

// wsUpgradeStatus 用裸连接发一个合法的 WebSocket 升级请求并返回状态行。
func wsUpgradeStatus(t *testing.T, host, path, origin string) string {
	t.Helper()
	conn, err := net.Dial("tcp", host)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	var keyBytes [16]byte
	if _, err := rand.Read(keyBytes[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes[:])
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: binary\r\nOrigin: %s\r\n\r\n",
		path, host, key, origin)

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write upgrade: %v", err)
	}
	statusLine, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	return statusLine
}

// TestServerObfuscatedTCPNotBlockedByStalledClient 回归保护「去 worker 池 / 握手移出 accept
// 循环」的修复：一个发了几字节就挂起的连接，过去会卡死串行 accept 循环、阻塞所有后续接入；
// 现在它只占用自己的 goroutine，正常客户端仍能在握手超时内被服务。
func TestServerObfuscatedTCPNotBlockedByStalledClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	frames := make(chan int, 1)
	srv := New(Options{Logger: zaptest.NewLogger(t), ObfuscatedTCP: true, WebSocket: true})
	srv.onFrame = func(n int) {
		select {
		case frames <- n:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	// 挂起连接：发 4 个非 HTTP 字节通过分流（路由到 TCP），但不补满 obfuscated2 的 64 字节
	// init，使其卡在去混淆读取上。修复前这会拖死整个 TCP accept 循环。
	stalled, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("stalled dial: %v", err)
	}
	defer func() { _ = stalled.Close() }()
	if _, err := stalled.Write([]byte{0x01, 0x02, 0x03, 0x04}); err != nil {
		t.Fatalf("stalled write: %v", err)
	}

	// 正常的 obfuscated abridged 客户端：应当照常被服务（onFrame 触发）。
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("tcp dial: %v", err)
	}
	obfs := obfuscator.Obfuscated2(rand.Reader, raw)
	if err := obfs.Handshake((codec.Abridged{}).ObfuscatedTag(), 2, mtproxy.Secret{}); err != nil {
		t.Fatalf("tcp obfuscated handshake: %v", err)
	}
	tcpConn, err := transport.NewProtocol(func() transport.Codec {
		return transport.Abridged.CodecNoHeader()
	}).Handshake(obfs)
	if err != nil {
		t.Fatalf("tcp transport handshake: %v", err)
	}

	var payload bin.Buffer
	payload.PutInt32(0x12345678)
	payload.PutInt32(0x0badf00d)
	sendCtx, sc := context.WithTimeout(context.Background(), 5*time.Second)
	if err := tcpConn.Send(sendCtx, &payload); err != nil {
		sc()
		t.Fatalf("tcp send: %v", err)
	}
	sc()
	expectFrameLen(t, frames, payload.Len())
	_ = tcpConn.Close()

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serve returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after ctx cancel")
	}
}
