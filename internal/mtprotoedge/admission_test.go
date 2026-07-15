package mtprotoedge

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto/codec"

	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestAdmissionConnectionLimitsAndIdempotentRelease(t *testing.T) {
	a := newAdmissionController(2, 1, 1)
	ip1a := &net.TCPAddr{IP: net.ParseIP("203.0.113.1"), Port: 1000}
	ip1b := &net.TCPAddr{IP: net.ParseIP("203.0.113.1"), Port: 1001}
	ip2 := &net.TCPAddr{IP: net.ParseIP("203.0.113.2"), Port: 1000}
	ip3 := &net.TCPAddr{IP: net.ParseIP("203.0.113.3"), Port: 1000}

	release1, ok := a.acquireConnection(ip1a)
	if !ok {
		t.Fatal("first connection rejected")
	}
	if _, ok := a.acquireConnection(ip1b); ok {
		t.Fatal("second connection from same IP bypassed per-IP cap")
	}
	release2, ok := a.acquireConnection(ip2)
	if !ok {
		t.Fatal("second IP connection rejected below global cap")
	}
	if _, ok := a.acquireConnection(ip3); ok {
		t.Fatal("third connection bypassed global cap")
	}

	release1()
	release1() // 幂等归还不得把计数减成负数。
	releaseAgain, ok := a.acquireConnection(ip1b)
	if !ok {
		t.Fatal("released per-IP/global slot was not reusable")
	}
	releaseAgain()
	release2()

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.connections != 0 || len(a.byIP) != 0 {
		t.Fatalf("admission counters after release = %d/%v, want 0/empty", a.connections, a.byIP)
	}
}

func TestAdmissionHandshakeLimitAndRelease(t *testing.T) {
	a := newAdmissionController(-1, -1, 1)
	release, ok := a.tryAcquireHandshake()
	if !ok {
		t.Fatal("first handshake rejected")
	}
	if _, ok := a.tryAcquireHandshake(); ok {
		t.Fatal("second handshake bypassed semaphore")
	}
	release()
	release() // 幂等
	release2, ok := a.tryAcquireHandshake()
	if !ok {
		t.Fatal("released handshake slot was not reusable")
	}
	release2()
}

type oneConnListener struct {
	conn net.Conn
	once sync.Once
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() {
		conn = l.conn
	})
	if conn == nil {
		return nil, net.ErrClosed
	}
	return conn, nil
}
func (l *oneConnListener) Close() error   { return l.conn.Close() }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

func TestAdmissionListenerTracksUntilPhysicalClose(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	a := newAdmissionController(1, 1, 1)
	ln := a.wrapListener(&oneConnListener{conn: serverSide})
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	a.mu.Lock()
	active := a.connections
	a.mu.Unlock()
	if active != 1 {
		t.Fatalf("active after Accept = %d, want 1", active)
	}
	_ = conn.Close()
	_ = conn.Close()
	a.mu.Lock()
	active = a.connections
	a.mu.Unlock()
	if active != 0 {
		t.Fatalf("active after physical Close = %d, want 0", active)
	}
}

type temporaryAcceptTestError struct{}

func (temporaryAcceptTestError) Error() string   { return "temporary accept failure" }
func (temporaryAcceptTestError) Timeout() bool   { return false }
func (temporaryAcceptTestError) Temporary() bool { return true }

type temporaryThenConnListener struct {
	conn      net.Conn
	closed    chan struct{}
	closeOnce sync.Once
	calls     atomic.Int32
}

type connThenErrorListener struct {
	conn      net.Conn
	err       error
	closeOnce sync.Once
	calls     atomic.Int32
}

func (l *connThenErrorListener) Accept() (net.Conn, error) {
	if l.calls.Add(1) == 1 {
		return l.conn, nil
	}
	return nil, l.err
}

func (l *connThenErrorListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		if l.conn != nil {
			err = l.conn.Close()
		}
	})
	return err
}

func (l *connThenErrorListener) Addr() net.Addr {
	if l.conn != nil {
		return l.conn.LocalAddr()
	}
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}

type fixedErrorListener struct {
	err  error
	addr net.Addr
}

func (l *fixedErrorListener) Accept() (net.Conn, error) { return nil, l.err }
func (*fixedErrorListener) Close() error                { return nil }
func (l *fixedErrorListener) Addr() net.Addr            { return l.addr }

func (l *temporaryThenConnListener) Accept() (net.Conn, error) {
	call := l.calls.Add(1)
	if call == 1 {
		return nil, temporaryAcceptTestError{}
	}
	if call == 2 {
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *temporaryThenConnListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		_ = l.conn.Close()
	})
	return nil
}

func (l *temporaryThenConnListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}

func TestAcceptLoopRetriesTemporaryError(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	ln := &temporaryThenConnListener{conn: serverSide, closed: make(chan struct{})}
	srv := New(Options{HandshakeIdleTimeout: 100 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.acceptLoop(ctx, ln, false) }()

	deadline := time.Now().Add(time.Second)
	for ln.calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ln.calls.Load() < 3 {
		cancel()
		<-done
		t.Fatalf("accept calls = %d, want temporary retry then next accept", ln.calls.Load())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("acceptLoop after temporary error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("acceptLoop did not stop after cancel")
	}
}

func TestAcceptLoopPermanentErrorCancelsAcceptedConnectionsBeforeWait(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	wantErr := errors.New("permanent accept failure")
	ln := &connThenErrorListener{conn: serverSide, err: wantErr}
	srv := New(Options{HandshakeIdleTimeout: time.Hour})

	done := make(chan error, 1)
	go func() {
		done <- srv.acceptLoop(context.Background(), ln, false)
	}()

	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("acceptLoop error = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("acceptLoop waited for an accepted connection before canceling it")
	}

	_ = clientSide.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := clientSide.Read(one[:]); err == nil {
		t.Fatal("accepted connection remained open after permanent accept failure")
	}
}

func TestServeMixedStopsAllComponentsWhenOneReturnsCleanly(t *testing.T) {
	srv := New(Options{WebSocket: true})
	ln := &fixedErrorListener{
		err:  net.ErrClosed,
		addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2398},
	}

	done := make(chan error, 1)
	go func() {
		done <- srv.serveMixed(context.Background(), ln)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveMixed error = %v, want nil closed-listener shutdown", err)
		}
	case <-time.After(time.Second):
		t.Fatal("serveMixed did not stop remaining components after one clean exit")
	}
}

type countingAuthKeyStore struct {
	store.AuthKeyStore
	gets atomic.Int32
}

func (s *countingAuthKeyStore) Get(ctx context.Context, id [8]byte) (store.AuthKeyData, bool, error) {
	s.gets.Add(1)
	return s.AuthKeyStore.Get(ctx, id)
}

func TestUnknownAuthKeyRespondsOnceThenCloses(t *testing.T) {
	keys := &countingAuthKeyStore{AuthKeyStore: memory.NewAuthKeyStore()}
	addr, _, _ := startTestServer(t, Options{AuthKeys: keys})
	conn := dialTransportOnly(t, addr)

	var request bin.Buffer
	request.PutLong(0x0102030405060708)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := conn.Send(ctx, &request); err != nil {
		t.Fatalf("send unknown auth key: %v", err)
	}
	var response bin.Buffer
	err := conn.Recv(ctx, &response)
	var protocolErr *codec.ProtocolErr
	if !errors.As(err, &protocolErr) || protocolErr.Code != codec.CodeAuthKeyNotFound {
		t.Fatalf("first recv err = %T %v, want protocol -404", err, err)
	}
	if got := keys.gets.Load(); got != 1 {
		t.Fatalf("AuthKeyStore.Get calls = %d, want 1", got)
	}

	response.Reset()
	err = conn.Recv(ctx, &response)
	if err == nil {
		t.Fatal("connection remained readable after terminal -404")
	}
	if got := keys.gets.Load(); got != 1 {
		t.Fatalf("AuthKeyStore.Get calls after close = %d, want 1", got)
	}
}
