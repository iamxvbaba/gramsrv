package mtprotoedge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
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

// TestServerAcceptAndCodec verifies M0: the server can accept a connection,
// auto-negotiate the codec, read a client frame, and exit gracefully on ctx cancellation.
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

	// Client: TCP dial + intermediate protocol handshake + send one frame.
	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn, err := transport.Intermediate.Handshake(raw)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}

	// The payload must ≠ 4 bytes: the codec treats a frame of exactly 4 bytes as a
	// transport protocol error code (checkProtocolError). Real MTProto frames are far
	// larger than 4 bytes; here 8 bytes are sent to simulate an ordinary frame.
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

	// Verify graceful shutdown.
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

// TestServerAcceptObfuscatedAbridged verifies the TDesktop tcpo_only connection
// shape: first do MTProto TCP obfuscation, then use the abridged codec on the
// decrypted stream.
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

func TestServerSamePortWebSocketAndMixedTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	frames := make(chan int, 3)
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

	plainRaw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("plain tcp dial: %v", err)
	}
	plainConn, err := transport.Intermediate.Handshake(plainRaw)
	if err != nil {
		_ = plainRaw.Close()
		t.Fatalf("plain tcp transport handshake: %v", err)
	}
	var plainPayload bin.Buffer
	plainPayload.PutInt32(0x33445566)
	plainPayload.PutInt32(0x77889900)
	sendCtx, sc = context.WithTimeout(context.Background(), 5*time.Second)
	if err := plainConn.Send(sendCtx, &plainPayload); err != nil {
		sc()
		t.Fatalf("plain tcp send: %v", err)
	}
	sc()
	expectFrameLen(t, frames, plainPayload.Len())
	_ = plainConn.Close()

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
		Handler:           websocketRouteHandler(wsHandler, []string{"http://localhost:1234"}, nil),
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

// TestSamePortMuxIdleConnNotReapedBeforeHandshakeTimeout regression: the sniff
// read timeout of WebSocket same-port reuse (reading the first 4 bytes for HTTP/TCP
// splitting) must align with HandshakeIdleTimeout, not the old hard cap of 5s.
// Legitimate MTProto clients (DrKLO) pre-open "warm" connections and do not
// immediately send the obfuscated2 init before there is a request; the old
// implementation used minDuration(5s, handshakeTimeout) to force the sniff down to
// 5s, 12x more aggressive than the non-mux path (serveDetectedConn uses the full
// handshakeTimeout), which wrongly killed these warm connections at 5s and
// triggered a DrKLO 6s reconnect storm + EPOLLRDHUP + a wrong "backend unhealthy"
// fallback to external DNS (see docs/client-compat-notes.md).
//
// HandshakeIdleTimeout must be >5s to expose the old truncation: connect a bare TCP,
// send no bytes, and use a 6s local read deadline. Under the new implementation the
// connection stays alive until the 8s sniff timeout, so the local Read times out on
// its own deadline (net timeout); under the old implementation the server FINs at
// 5s, so the local Read gets EOF/reset before 6s—that is how the regression is judged.
func TestSamePortMuxIdleConnNotReapedBeforeHandshakeTimeout(t *testing.T) {
	addr, _, _ := startTestServer(t, Options{
		WebSocket:            true,
		ObfuscatedTCP:        true,
		HandshakeIdleTimeout: 8 * time.Second, // must be >5s to distinguish "aligned with handshakeTimeout" from the old 5s truncation
	})

	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = raw.Close() }()

	// Send no bytes, simulating a client's pre-opened warm connection that has not yet sent its first frame.
	if err := raw.SetReadDeadline(time.Now().Add(6 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	n, err := raw.Read(make([]byte, 1))
	if err == nil {
		t.Fatalf("unexpected %d bytes on idle pre-handshake conn (server should send nothing)", n)
	}
	// Only "the local read deadline timed out" indicates the connection is still
	// alive at 6s (the sniff timeout is aligned to 8s). Any EOF/reset caused by the
	// peer closing means the server reclaimed the connection too early, before the
	// handshakeTimeout.
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

// TestWebSocketRouteHandlerChecksBrowserOrigin regression-protects the Origin fix:
// a browser-initiated WS upgrade necessarily carries an Origin (≠ Host); whitelisted
// origins need Origin rewritten to pass coder/websocket, and non-whitelisted
// origins must get 403.
func TestWebSocketRouteHandlerChecksBrowserOrigin(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	_, wsHandler := transport.WebsocketListener(ln.Addr())
	httpServer := &http.Server{Handler: websocketRouteHandler(wsHandler, []string{"http://localhost:1234"}, nil)}
	go func() { _ = httpServer.Serve(ln) }()
	defer func() { _ = httpServer.Close() }()

	host := ln.Addr().String()

	// Whitelisted cross-origin upgrade: Origin points to the page origin (port/host differs from Host).
	status := wsUpgradeStatus(t, host, "/apiws", "http://localhost:1234")
	if !strings.Contains(status, "101") {
		t.Fatalf("allowed cross-origin /apiws upgrade: got status %q, want 101 Switching Protocols", status)
	}

	status = wsUpgradeStatus(t, host, "/apiws", "http://evil.example")
	if !strings.Contains(status, "403") {
		t.Fatalf("disallowed origin: got status %q, want 403", status)
	}

	// A non-whitelisted path must 404 and must not upgrade.
	status = wsUpgradeStatus(t, host, "/nope", "http://localhost:1234")
	if !strings.Contains(status, "404") {
		t.Fatalf("disallowed path: got status %q, want 404", status)
	}
}

// TestSamePortHTTPServesInjectedRoutes pins gramsrv#138: a plain HTTP GET on the
// MTProto port reaches the injected same-port handler, while /apiws* stays on
// the WebSocket upgrade path and is never handed to it.
func TestSamePortHTTPServesInjectedRoutes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var mu sync.Mutex
	var seen []string
	srv := New(Options{
		Logger:        zaptest.NewLogger(t),
		ObfuscatedTCP: true,
		WebSocket:     true,
		SamePortHTTP: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.URL.Path)
			mu.Unlock()
			if r.URL.Path != "/owpengram/server-info" {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, `{"dc_id":2}`)
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ctx, ln) }()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + ln.Addr().String() + "/owpengram/server-info")
	if err != nil {
		t.Fatalf("GET server-info: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != `{"dc_id":2}` {
		t.Fatalf("GET server-info = %d %q", resp.StatusCode, body)
	}
	if status := wsUpgradeStatus(t, ln.Addr().String(), "/apiws", ""); !strings.Contains(status, "101") {
		t.Fatalf("/apiws upgrade with a same-port handler = %q, want 101", status)
	}
	mu.Lock()
	got := append([]string(nil), seen...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "/owpengram/server-info" {
		t.Fatalf("same-port handler saw %v, want only the server-info path", got)
	}
	cancel()
	select {
	case <-serveErr:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}

// wsUpgradeStatus sends a valid WebSocket upgrade request over a bare connection and returns the status line.
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

// TestServerObfuscatedTCPNotBlockedByStalledClient regression-protects the
// "remove worker pool / move handshake out of the accept loop" fix: a connection
// that hangs after sending a few bytes used to deadlock the serial accept loop and
// block all subsequent intake; now it only occupies its own goroutine, and normal
// clients can still be served within the handshake timeout.
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

	// Stalled connection: send 4 non-HTTP bytes to pass the split (routed to TCP),
	// but do not fill in the full 64-byte obfuscated2 init, leaving it stuck on the
	// de-obfuscation read. Before the fix this would drag down the whole TCP accept loop.
	stalled, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("stalled dial: %v", err)
	}
	defer func() { _ = stalled.Close() }()
	if _, err := stalled.Write([]byte{0x01, 0x02, 0x03, 0x04}); err != nil {
		t.Fatalf("stalled write: %v", err)
	}

	// A normal obfuscated abridged client: should still be served as usual (onFrame fires).
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
