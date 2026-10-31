package mtprotoedge

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// samePortMuxBacklog is the handshake buffer depth of the tcp/http child
// listeners, absorbing intake bursts.
const samePortMuxBacklog = 1024

// websocketAllowedPaths is the whitelist of local paths allowed to upgrade to WebSocket.
//
// telegram-tt (WebA) builds the URL as `/apiws{_test}{_premium}`, so all four
// combinations must be allowed; of these `/apiws_test_premium` is the
// "test server + Premium" combination, and missing it would make Premium accounts
// get a 404 on the test server.
var websocketAllowedPaths = map[string]struct{}{
	"/apiws":              {},
	"/apiws_test":         {},
	"/apiws_premium":      {},
	"/apiws_test_premium": {},
}

// samePortMux splits the two kinds of connection, "HTTP (WebSocket upgrade
// request)" and "bare MTProto TCP", on the same listener: each new connection only
// needs the first 4 bytes sniffed to decide its route. Each connection's sniff
// runs in its own goroutine (bounded by sniffTimeout), so a slow connection only
// occupies its own goroutine and never blocks the intake and splitting of other
// connections—avoiding intake starvation from a fixed worker pool saturated by
// slow-loris.
type samePortMux struct {
	base         net.Listener
	addr         net.Addr
	sniffTimeout time.Duration
	observe      func(connectionIntakeEvent)

	tcp  *samePortMuxListener
	http *samePortMuxListener

	closed chan struct{}
	once   sync.Once

	// sniffing contains only sockets still owned by dispatch while it reads the first four
	// bytes. Keeping an explicit registry lets Close interrupt every slow-loris read without a
	// second cancellation goroutine per raw connection. A socket is removed under sniffMu before
	// successful child-listener hand-off, establishing the ownership barrier.
	sniffMu  sync.Mutex
	sniffing map[net.Conn]struct{}
}

func newSamePortMux(base net.Listener, sniffTimeout time.Duration, observers ...func(connectionIntakeEvent)) *samePortMux {
	if sniffTimeout <= 0 {
		sniffTimeout = 5 * time.Second
	}
	m := &samePortMux{
		base:         base,
		addr:         base.Addr(),
		sniffTimeout: sniffTimeout,
		closed:       make(chan struct{}),
		sniffing:     make(map[net.Conn]struct{}),
	}
	if len(observers) > 0 {
		m.observe = observers[0]
	}
	m.tcp = newSamePortMuxListener(m.addr, m.closed)
	m.http = newSamePortMuxListener(m.addr, m.closed)
	return m
}

// TCP returns the listener for bare MTProto TCP connections (connections are
// already sniffed, and the first 4 bytes are replayed).
func (m *samePortMux) TCP() net.Listener {
	return m.tcp
}

// HTTP returns the listener for WebSocket upgrade requests, handed to http.Server.Serve.
func (m *samePortMux) HTTP() net.Listener {
	return m.http
}

func (m *samePortMux) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	// Every exit path must publish cancellation and close both child listeners before waiting
	// for sniff/delivery goroutines. A permanent Accept error can otherwise leave a dispatch
	// blocked on a full child backlog while the old defer order waits for it before canceling.
	var wg sync.WaitGroup
	defer func() {
		cancel()
		_ = m.Close()
		wg.Wait()
	}()

	go func() {
		<-ctx.Done()
		_ = m.Close()
	}()

	// One sniff goroutine per connection: wg makes Serve wait for in-flight sniffs
	// to finish handing off their connections before exiting.
	var tempDelay time.Duration
	for {
		conn, err := m.base.Accept()
		if err != nil {
			if ctx.Err() != nil || isSamePortMuxClosed(m.closed) || isNetClosed(err) {
				return nil
			}
			if isTemporaryAcceptError(err) {
				tempDelay = nextAcceptRetryDelay(tempDelay)
				if !waitAcceptRetry(ctx, tempDelay) {
					return nil
				}
				continue
			}
			return err
		}
		tempDelay = 0
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.dispatch(ctx, conn)
		}()
	}
}

func (m *samePortMux) Close() error {
	m.once.Do(func() {
		close(m.closed)
		// Snapshot under the ownership lock, then close outside it. finishSniff observes
		// m.closed and refuses hand-off even after the map is cleared, so dispatch cannot race
		// this snapshot and deliver a socket that Close is about to terminate.
		m.sniffMu.Lock()
		sniffing := make([]net.Conn, 0, len(m.sniffing))
		for conn := range m.sniffing {
			sniffing = append(sniffing, conn)
			delete(m.sniffing, conn)
		}
		m.sniffMu.Unlock()
		for _, conn := range sniffing {
			_ = conn.Close()
		}
		_ = m.tcp.Close()
		_ = m.http.Close()
		_ = m.base.Close()
	})
	return nil
}

// dispatch sniffs the first 4 bytes of a single connection and hands it to the
// tcp or http child listener. The sniff has a sniffTimeout read bound, so a slow
// or half-open connection occupies this goroutine for at most sniffTimeout before
// being reclaimed.
func (m *samePortMux) dispatch(ctx context.Context, conn net.Conn) {
	started := time.Now()
	remote, local := connRemote(conn), connLocal(conn)
	// SetReadDeadline bounds an otherwise healthy slow-loris connection, but Close only owns
	// the base listener, not sockets Accept has already returned. Register temporary ownership so
	// mux shutdown can close this read immediately. finishSniff removes the socket before hand-off.
	if !m.beginSniff(conn) {
		m.observeEvent(connectionIntakeEvent{stage: "mux_sniff", outcome: "closed", remote: remote, local: local, duration: time.Since(started)})
		_ = conn.Close()
		return
	}
	finishedSniff := false
	defer func() {
		if !finishedSniff {
			m.finishSniff(conn)
		}
	}()

	var header [4]byte
	if err := conn.SetReadDeadline(time.Now().Add(m.sniffTimeout)); err != nil {
		m.observeEvent(connectionIntakeEvent{stage: "mux_sniff", outcome: "error", remote: remote, local: local, duration: time.Since(started), err: err})
		_ = conn.Close()
		return
	}
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		outcome := "error"
		if isClientDisconnect(err) {
			outcome = "client_disconnect"
		}
		m.observeEvent(connectionIntakeEvent{stage: "mux_sniff", outcome: outcome, remote: remote, local: local, duration: time.Since(started), err: err})
		_ = conn.Close()
		return
	}
	// From this point onward deliver/child-listener closure owns cancellation. Removing the
	// registry entry under sniffMu is the hand-off barrier: Close either captured and closed this
	// socket, or it can no longer find it. A concurrently closed mux refuses delivery.
	if !m.finishSniff(conn) {
		m.observeEvent(connectionIntakeEvent{stage: "mux_sniff", outcome: "closed", remote: remote, local: local, duration: time.Since(started)})
		_ = conn.Close()
		return
	}
	finishedSniff = true
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		m.observeEvent(connectionIntakeEvent{stage: "mux_sniff", outcome: "error", remote: remote, local: local, duration: time.Since(started), err: err})
		_ = conn.Close()
		return
	}

	// Fixed eight-byte replay storage avoids bytes.Reader + MultiReader allocations
	// on every accepted same-port connection.
	wrapped := newReplayNetConn(conn, header[:])

	target := m.tcp
	transport := "tcp"
	if isHTTPHeaderPrefix(header) {
		target = m.http
		transport = "websocket"
	}
	m.observeEvent(connectionIntakeEvent{stage: "mux_sniff", outcome: "ready", transport: transport, remote: remote, local: local, duration: time.Since(started), bytes: len(header)})
	if !target.deliver(ctx, wrapped) {
		m.observeEvent(connectionIntakeEvent{stage: "mux_delivery", outcome: "closed", transport: transport, remote: remote, local: local, duration: time.Since(started)})
		_ = conn.Close()
	}
}

func (m *samePortMux) observeEvent(event connectionIntakeEvent) {
	if m.observe != nil {
		m.observe(event)
	}
}

func (m *samePortMux) beginSniff(conn net.Conn) bool {
	m.sniffMu.Lock()
	defer m.sniffMu.Unlock()
	if isSamePortMuxClosed(m.closed) {
		return false
	}
	if m.sniffing == nil {
		m.sniffing = make(map[net.Conn]struct{})
	}
	m.sniffing[conn] = struct{}{}
	return true
}

// finishSniff returns true only when dispatch still owned the socket and the mux remained open
// through the ownership barrier. A false result means Close captured the socket; dispatch must
// not hand it to a child listener.
func (m *samePortMux) finishSniff(conn net.Conn) bool {
	m.sniffMu.Lock()
	defer m.sniffMu.Unlock()
	_, owned := m.sniffing[conn]
	delete(m.sniffing, conn)
	return owned && !isSamePortMuxClosed(m.closed)
}

// isHTTPHeaderPrefix determines whether the first 4 bytes begin an HTTP request line.
//
// Only GET/POST/HEAD/OPTI are recognized here, "strictly aligned" with the prefix
// set excluded by gotd generateInit: a valid obfuscated2 init header is guaranteed
// not to start with these four prefixes (see mtproxy/obfuscated2/keys_util.go), so
// bare MTProto is never misclassified as HTTP. It deliberately does not extend to
// other methods such as PUT/DELETE—generateInit does not exclude them, and
// extending the whitelist would instead let a random init (2^-32) occasionally be
// misrouted. Real WebSocket upgrades are always GET, and browsers use no other
// method, so the current set is both safe and complete.
func isHTTPHeaderPrefix(header [4]byte) bool {
	switch string(header[:]) {
	case "GET ", "POST", "HEAD", "OPTI":
		return true
	default:
		return false
	}
}

// websocketRouteHandler upgrades /apiws* and hands every other path to
// fallback (nil: 404). fallback never sees a WebSocket path.
func websocketRouteHandler(handler http.Handler, allowedOrigins []string, fallback http.Handler) http.Handler {
	origins := websocketOriginSet(allowedOrigins)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := websocketAllowedPaths[r.URL.Path]; !ok {
			if fallback != nil {
				fallback.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		// gotd's WebsocketListener hardcodes websocket.Accept's AcceptOptions and does
		// not pass InsecureSkipVerify/OriginPatterns, so coder/websocket returns 403
		// for a handshake where "Origin.Host != Host". A ws connection initiated by a
		// browser (WebA/telegram-tt) always carries an Origin (= the page origin,
		// ≠ this service's listen address), so the handshake would be rejected
		// unconditionally, while a non-browser client without Origin (the gotd test
		// client) passes—so unit tests were all green while real browsers all failed.
		//
		// After the whitelist confirms it, rewrite the Origin to the same origin as
		// Host so Accept lets it through without forking gotd. Non-browser clients
		// without Origin are allowed through; browser origins must be configured
		// explicitly, and "*" is for temporary debugging only.
		if !websocketOriginAllowed(origins, r.Header.Get("Origin")) {
			http.Error(w, "websocket origin forbidden", http.StatusForbidden)
			return
		}
		if r.Header.Get("Origin") != "" {
			r.Header.Set("Origin", "http://"+r.Host)
		}
		handler.ServeHTTP(w, r)
	})
}

func websocketOriginSet(origins []string) map[string]struct{} {
	out := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" {
			out[strings.ToLower(origin)] = struct{}{}
		}
	}
	return out
}

func websocketOriginAllowed(allowed map[string]struct{}, origin string) bool {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin == "" {
		return true
	}
	if _, ok := allowed["*"]; ok {
		return true
	}
	_, ok := allowed[strings.ToLower(origin)]
	return ok
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 || a < b {
		return a
	}
	return b
}

func isNetClosed(err error) bool {
	return errors.Is(err, net.ErrClosed)
}

func isSamePortMuxClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// samePortMuxListener is an in-memory listener: dispatch delivers the split
// connections into it, and downstream (serveMixed's accept loop / http.Server)
// Accepts from it.
type samePortMuxListener struct {
	addr   net.Addr
	ch     chan net.Conn
	closed chan struct{}
	once   sync.Once

	deliveryMu sync.Mutex
	closing    bool
	deliveryWG sync.WaitGroup
}

func newSamePortMuxListener(addr net.Addr, parentClosed <-chan struct{}) *samePortMuxListener {
	closed := make(chan struct{})
	l := &samePortMuxListener{
		addr:   addr,
		ch:     make(chan net.Conn, samePortMuxBacklog),
		closed: closed,
	}
	go func() {
		select {
		case <-parentClosed:
			_ = l.Close()
		case <-closed:
		}
	}()
	return l
}

func (l *samePortMuxListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case conn := <-l.ch:
		return conn, nil
	}
}

func (l *samePortMuxListener) Close() error {
	l.once.Do(func() {
		// Add and Wait on a WaitGroup must not race while the counter may still be zero.
		// The delivery gate serializes the final Add with the transition to closing; after
		// closing becomes true no producer can enter, so waiting and draining are safe.
		l.deliveryMu.Lock()
		l.closing = true
		close(l.closed)
		l.deliveryMu.Unlock()

		l.deliveryWG.Wait()
		for {
			select {
			case conn := <-l.ch:
				if conn != nil {
					_ = conn.Close()
				}
			default:
				return
			}
		}
	})
	return nil
}

func (l *samePortMuxListener) Addr() net.Addr {
	return l.addr
}

func (l *samePortMuxListener) deliver(ctx context.Context, conn net.Conn) bool {
	if !l.beginDelivery() {
		return false
	}
	defer l.deliveryWG.Done()

	select {
	case <-l.closed:
		return false
	case l.ch <- conn:
		return true
	case <-ctx.Done():
		return false
	}
}

func (l *samePortMuxListener) beginDelivery() bool {
	l.deliveryMu.Lock()
	defer l.deliveryMu.Unlock()
	if l.closing {
		return false
	}
	l.deliveryWG.Add(1)
	return true
}
