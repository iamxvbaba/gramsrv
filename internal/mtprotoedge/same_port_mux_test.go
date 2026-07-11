package mtprotoedge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestSamePortMuxListenerCloseWaitsAndReturnsBacklogAdmission(t *testing.T) {
	admission := newAdmissionController(4, 4, 1)
	listener := &samePortMuxListener{
		addr:   &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)},
		ch:     make(chan net.Conn, 1),
		closed: make(chan struct{}),
	}

	backlog, backlogPeer := trackedMuxPipe(t, admission, 1001)
	defer backlogPeer.Close()
	if !listener.deliver(context.Background(), backlog) {
		t.Fatal("initial backlog delivery was rejected")
	}

	// Deterministically model a producer that passed the delivery gate but has not yet
	// completed. Close must publish closed first, then wait before draining the backlog.
	if !listener.beginDelivery() {
		t.Fatal("in-flight delivery gate unexpectedly closed")
	}
	closeDone := make(chan struct{})
	go func() {
		_ = listener.Close()
		close(closeDone)
	}()
	select {
	case <-listener.closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not publish listener closure")
	}
	select {
	case <-closeDone:
		t.Fatal("Close returned before in-flight delivery completed")
	default:
	}
	listener.deliveryWG.Done()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after delivery completed")
	}

	assertAdmissionConnections(t, admission, 0)

	late, latePeer := trackedMuxPipe(t, admission, 1002)
	defer latePeer.Close()
	if listener.deliver(context.Background(), late) {
		t.Fatal("delivery after Close unexpectedly succeeded")
	}
	_ = late.Close() // dispatch owns and closes a rejected delivery.
	assertAdmissionConnections(t, admission, 0)
}

func TestSamePortMuxPermanentAcceptErrorCancelsBlockedDeliveryBeforeWait(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer clientSide.Close()
	wantErr := errors.New("same-port permanent accept failure")
	base := &connThenErrorListener{conn: serverSide, err: wantErr}
	closed := make(chan struct{})
	mux := &samePortMux{
		base:         base,
		addr:         base.Addr(),
		sniffTimeout: time.Hour,
		closed:       closed,
	}
	// An unbuffered child listener deterministically leaves dispatch blocked in deliver: no
	// consumer is running, and the base listener immediately returns a permanent second error.
	mux.tcp = &samePortMuxListener{addr: mux.addr, ch: make(chan net.Conn), closed: make(chan struct{})}
	mux.http = &samePortMuxListener{addr: mux.addr, ch: make(chan net.Conn), closed: make(chan struct{})}

	writeDone := make(chan error, 1)
	go func() {
		_, err := clientSide.Write([]byte{0xef, 0, 0, 0})
		writeDone <- err
	}()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- mux.Serve(context.Background())
	}()

	select {
	case err := <-serveDone:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Serve error = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("same-port Serve waited for blocked delivery before canceling it")
	}
	select {
	case <-writeDone:
	case <-time.After(time.Second):
		t.Fatal("sniff writer remained blocked after same-port shutdown")
	}
}

func TestSamePortMuxShutdownInterruptsSlowSniffImmediately(t *testing.T) {
	tests := []struct {
		name     string
		shutdown func(context.CancelFunc, *samePortMux)
	}{
		{
			name: "context cancel",
			shutdown: func(cancel context.CancelFunc, _ *samePortMux) {
				cancel()
			},
		},
		{
			name: "mux close",
			shutdown: func(_ context.CancelFunc, mux *samePortMux) {
				_ = mux.Close()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("listen: %v", err)
			}
			mux := newSamePortMux(base, time.Minute)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			serveDone := make(chan error, 1)
			go func() { serveDone <- mux.Serve(ctx) }()

			peer, err := net.Dial("tcp", base.Addr().String())
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer peer.Close()
			// No bytes are written: dispatch is blocked in the four-byte sniff with a one-minute
			// deadline. Shutdown must close this accepted socket instead of waiting for it.
			tt.shutdown(cancel, mux)

			select {
			case err := <-serveDone:
				if err != nil {
					t.Fatalf("Serve: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Serve waited for the sniff deadline after shutdown")
			}
			if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatalf("set peer deadline: %v", err)
			}
			var one [1]byte
			if _, err := peer.Read(one[:]); err == nil {
				t.Fatal("slow sniff socket remained open after mux shutdown")
			}
		})
	}
}

func TestSamePortMuxSuccessfulHandoffReleasesSniffOwnership(t *testing.T) {
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := newSamePortMux(base, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- mux.Serve(ctx) }()

	peer, err := net.Dial("tcp", base.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer peer.Close()
	if _, err := peer.Write([]byte{0xef, 0, 0, 0}); err != nil {
		t.Fatalf("write sniff prefix: %v", err)
	}
	accepted, err := mux.TCP().Accept()
	if err != nil {
		t.Fatalf("accept child: %v", err)
	}
	defer accepted.Close()

	// Once dispatch has delivered the Conn, canceling the mux may close listeners/backlog but
	// must not let the old sniff watcher close a socket now owned by the child consumer.
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after cancel")
	}
	if _, err := peer.Write([]byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("write after handoff/shutdown: %v", err)
	}
	if err := accepted.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set accepted deadline: %v", err)
	}
	got := make([]byte, 8)
	if _, err := io.ReadFull(accepted, got); err != nil {
		t.Fatalf("read handed-off connection: %v", err)
	}
	want := []byte{0xef, 0, 0, 0, 1, 2, 3, 4}
	if !bytes.Equal(got, want) {
		t.Fatalf("handed-off bytes = %x, want %x", got, want)
	}
}

func trackedMuxPipe(t *testing.T, admission *admissionController, port int) (net.Conn, net.Conn) {
	t.Helper()
	server, peer := net.Pipe()
	release, ok := admission.acquireConnection(&net.TCPAddr{
		IP:   net.ParseIP("203.0.113.20"),
		Port: port,
	})
	if !ok {
		_ = server.Close()
		_ = peer.Close()
		t.Fatal("test connection admission rejected")
	}
	return &admittedConn{Conn: server, release: release}, peer
}

func assertAdmissionConnections(t *testing.T, admission *admissionController, want int) {
	t.Helper()
	admission.mu.Lock()
	got := admission.connections
	byIP := len(admission.byIP)
	admission.mu.Unlock()
	if got != want || (want == 0 && byIP != 0) {
		t.Fatalf("admission state = connections:%d by_ip:%d, want connections:%d", got, byIP, want)
	}
}
