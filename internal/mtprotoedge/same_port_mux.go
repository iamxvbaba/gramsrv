package mtprotoedge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// samePortMuxBacklog 是 tcp/http 两个子 listener 的握手缓冲深度，吸收接入突发。
const samePortMuxBacklog = 1024

// websocketAllowedPaths 是允许升级为 WebSocket 的本地路径白名单。
//
// telegram-tt(WebA)按 `/apiws{_test}{_premium}` 拼 URL，故四种组合都要放行；
// 其中 `/apiws_test_premium` 是「测试服 + 会员」组合，缺它会让会员账号在测试服 404。
var websocketAllowedPaths = map[string]struct{}{
	"/apiws":              {},
	"/apiws_test":         {},
	"/apiws_premium":      {},
	"/apiws_test_premium": {},
}

// samePortMux 在同一个 listener 上把「HTTP(WebSocket 升级请求)」与「裸 MTProto TCP」
// 两类连接拆开：每条新连接只窥探前 4 字节即可判定走向。每条连接的窥探都在各自的
// goroutine 里完成（带 sniffTimeout 上界），慢连接只占用自己的 goroutine，绝不阻塞其他
// 连接的接入与分流——这避免了固定 worker 池被 slow-loris 占满导致的接入饥饿。
type samePortMux struct {
	base         net.Listener
	addr         net.Addr
	sniffTimeout time.Duration

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

func newSamePortMux(base net.Listener, sniffTimeout time.Duration) *samePortMux {
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
	m.tcp = newSamePortMuxListener(m.addr, m.closed)
	m.http = newSamePortMuxListener(m.addr, m.closed)
	return m
}

// TCP 返回裸 MTProto TCP 连接的 listener（连接已窥探，前 4 字节会被回放）。
func (m *samePortMux) TCP() net.Listener {
	return m.tcp
}

// HTTP 返回 WebSocket 升级请求的 listener，交给 http.Server.Serve。
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

	// 每条连接一个窥探 goroutine：wg 让 Serve 在退出前等待在途窥探把连接交接完成。
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

// dispatch 窥探单条连接的前 4 字节并把它交给 tcp 或 http 子 listener。窥探带 sniffTimeout
// 读上界，慢/半开连接最多占用本 goroutine sniffTimeout 后即被回收。
func (m *samePortMux) dispatch(ctx context.Context, conn net.Conn) {
	// SetReadDeadline bounds an otherwise healthy slow-loris connection, but Close only owns
	// the base listener, not sockets Accept has already returned. Register temporary ownership so
	// mux shutdown can close this read immediately. finishSniff removes the socket before hand-off.
	if !m.beginSniff(conn) {
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
		_ = conn.Close()
		return
	}
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		_ = conn.Close()
		return
	}
	// From this point onward deliver/child-listener closure owns cancellation. Removing the
	// registry entry under sniffMu is the hand-off barrier: Close either captured and closed this
	// socket, or it can no longer find it. A concurrently closed mux refuses delivery.
	if !m.finishSniff(conn) {
		_ = conn.Close()
		return
	}
	finishedSniff = true
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return
	}

	wrapped := &prefixedNetConn{
		Conn:   conn,
		reader: io.MultiReader(bytes.NewReader(header[:]), conn),
	}

	target := m.tcp
	if isHTTPHeaderPrefix(header) {
		target = m.http
	}
	if !target.deliver(ctx, wrapped) {
		_ = conn.Close()
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

// isHTTPHeaderPrefix 判断前 4 字节是否是 HTTP 请求行起始。
//
// 这里只认 GET/POST/HEAD/OPTI，与 gotd generateInit 排除的前缀集合「严格对齐」：合法的
// obfuscated2 init 头被保证不会以这四个前缀开头（见 mtproxy/obfuscated2/keys_util.go），
// 故裸 MTProto 永不会被误判为 HTTP。刻意不扩展到 PUT/DELETE 等其他方法——generateInit
// 并未排除它们，扩展白名单反而会让随机 init 偶发（2^-32）被误分流。真实 WebSocket 升级一律
// 是 GET，浏览器不会用其他方法,因此当前集合既安全又完备。
func isHTTPHeaderPrefix(header [4]byte) bool {
	switch string(header[:]) {
	case "GET ", "POST", "HEAD", "OPTI":
		return true
	default:
		return false
	}
}

func websocketRouteHandler(handler http.Handler, allowedOrigins []string) http.Handler {
	origins := websocketOriginSet(allowedOrigins)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := websocketAllowedPaths[r.URL.Path]; !ok {
			http.NotFound(w, r)
			return
		}
		// gotd 的 WebsocketListener 把 websocket.Accept 的 AcceptOptions 写死且不带
		// InsecureSkipVerify/OriginPatterns，coder/websocket 因此会对「Origin.Host != Host」
		// 的握手返回 403。浏览器(WebA/telegram-tt)发起的 ws 连接必然带 Origin(=页面来源，
		// ≠ 本服务监听地址)，握手会被无条件拒绝；而不带 Origin 的非浏览器客户端(gotd 测试
		// 客户端)却能通过——故单测全绿、真浏览器全挂。
		//
		// 白名单确认后再把 Origin 改写成与 Host 同源，让 Accept 放行且无需 fork gotd。
		// 无 Origin 的非浏览器客户端允许通过；浏览器来源必须显式配置，"*" 仅用于临时调试。
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

// prefixedNetConn 把被窥探掉的前缀字节回放在数据流最前面，使下游(去混淆/codec 探测/
// http.Server)看到完整原始字节流。
type prefixedNetConn struct {
	reader io.Reader
	net.Conn
}

func (p *prefixedNetConn) Read(b []byte) (int, error) {
	return p.reader.Read(b)
}

// samePortMuxListener 是一个内存 listener：dispatch 把分流后的连接投递进来，下游
// (serveMixed 的 accept 循环 / http.Server) 从这里 Accept。
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
