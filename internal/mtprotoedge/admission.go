package mtprotoedge

import (
	"context"
	"net"
	"sync"
	"time"
)

const (
	defaultMaxConnections          = 200_000
	defaultMaxConnectionsPerIP     = 4_096
	defaultMaxConcurrentHandshakes = 256
	acceptRetryInitialDelay        = 5 * time.Millisecond
	acceptRetryMaxDelay            = time.Second
)

// admissionController 把 raw socket 与昂贵的 RSA/DH exchange 分开限流。
// raw 配额覆盖连接从 Accept 到物理 Close 的完整生命周期；handshake 配额只覆盖
// auth_key_id=0 的 exchange（包括 TDesktop 每条候选连接的 fake req_pq 探活）。
type admissionController struct {
	mu             sync.Mutex
	maxConnections int
	maxPerIP       int
	connections    int
	byIP           map[string]int
	handshakes     chan struct{}
}

func newAdmissionController(maxConnections, maxPerIP, maxHandshakes int) *admissionController {
	a := &admissionController{
		maxConnections: maxConnections,
		maxPerIP:       maxPerIP,
		byIP:           make(map[string]int),
	}
	if maxHandshakes > 0 {
		a.handshakes = make(chan struct{}, maxHandshakes)
	}
	return a
}

func (a *admissionController) wrapListener(ln net.Listener) net.Listener {
	if a == nil {
		return ln
	}
	return &admissionListener{Listener: ln, admission: a}
}

func (a *admissionController) acquireConnection(addr net.Addr) (func(), bool) {
	if a == nil {
		return func() {}, true
	}
	ip := remoteAdmissionKey(addr)
	a.mu.Lock()
	if (a.maxConnections > 0 && a.connections >= a.maxConnections) ||
		(a.maxPerIP > 0 && a.byIP[ip] >= a.maxPerIP) {
		a.mu.Unlock()
		return nil, false
	}
	a.connections++
	a.byIP[ip]++
	a.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.connections--
			a.byIP[ip]--
			if a.byIP[ip] == 0 {
				delete(a.byIP, ip)
			}
			a.mu.Unlock()
		})
	}, true
}

func (a *admissionController) tryAcquireHandshake() (func(), bool) {
	if a == nil || a.handshakes == nil {
		return func() {}, true
	}
	select {
	case a.handshakes <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-a.handshakes })
		}, true
	default:
		return nil, false
	}
}

func remoteAdmissionKey(addr net.Addr) string {
	if addr == nil {
		return "<unknown>"
	}
	if host, _, err := net.SplitHostPort(addr.String()); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
		return host
	}
	return addr.Network() + ":" + addr.String()
}

// admissionListener 在最早的原始 Accept 边界记账，因此 mixed TCP/WebSocket 的
// sniff/upgrade 阶段也受 raw cap 保护。admittedConn.Close 负责幂等归还配额。
type admissionListener struct {
	net.Listener
	admission *admissionController
}

func (l *admissionListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		release, ok := l.admission.acquireConnection(conn.RemoteAddr())
		if !ok {
			_ = conn.Close()
			continue
		}
		return &admittedConn{Conn: conn, release: release}, nil
	}
}

type admittedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *admittedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func isTemporaryAcceptError(err error) bool {
	type temporary interface{ Temporary() bool }
	e, ok := err.(temporary)
	return ok && e.Temporary()
}

func nextAcceptRetryDelay(previous time.Duration) time.Duration {
	if previous <= 0 {
		return acceptRetryInitialDelay
	}
	next := previous * 2
	if next > acceptRetryMaxDelay {
		return acceptRetryMaxDelay
	}
	return next
}

func waitAcceptRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
