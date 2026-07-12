package mtprotoedge

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
)

type countingOutboundEncoder struct {
	count *int
}

func (e *countingOutboundEncoder) Encode(b *bin.Buffer) error {
	*e.count++
	return (&tg.UpdatesTooLong{}).Encode(b)
}

type closeCountingTransport struct {
	closes int
}

type slowCloseTransport struct {
	delay   time.Duration
	release <-chan struct{}
	done    chan struct{}
	once    sync.Once
	closes  atomic.Int32
}

func newSlowCloseTransport(delay time.Duration, release <-chan struct{}) *slowCloseTransport {
	return &slowCloseTransport{delay: delay, release: release, done: make(chan struct{})}
}

func (*slowCloseTransport) Send(context.Context, *bin.Buffer) error {
	return errors.New("test transport send")
}
func (*slowCloseTransport) Recv(context.Context, *bin.Buffer) error {
	return errors.New("test transport recv")
}
func (t *slowCloseTransport) Close() error {
	t.closes.Add(1)
	if t.release != nil {
		<-t.release
	} else if t.delay > 0 {
		time.Sleep(t.delay)
	}
	t.once.Do(func() { close(t.done) })
	return nil
}

func (t *closeCountingTransport) Send(context.Context, *bin.Buffer) error {
	return errors.New("test transport send")
}

func (t *closeCountingTransport) Recv(context.Context, *bin.Buffer) error {
	return errors.New("test transport recv")
}

func (t *closeCountingTransport) Close() error {
	t.closes++
	return nil
}

// TestSessionManagerRegistry 验证注册表的注册/注销/查找语义（不涉及网络发送）。
func TestSessionManagerRegistry(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	c := &Conn{sessionID: 42, authKeyID: [8]byte{1, 2, 3}}
	c.receivesUpdates.Store(true)

	sm.Register(c)
	if got := sm.Online(); got != 1 {
		t.Fatalf("online = %d, want 1", got)
	}
	sm.BindAuthKey(42, [8]byte{1, 2, 3})
	sm.BindUser(42, 100)
	if userID, ok := sm.UserID(42); !ok || userID != 100 {
		t.Fatalf("cached user = %d ok %v, want 100/true", userID, ok)
	}
	sm.BindAuthKey(42, [8]byte{9})
	if userID, ok := sm.UserID(42); ok || userID != 0 {
		t.Fatalf("cached user after auth key switch = %d ok %v, want 0/false", userID, ok)
	}
	if userID, resolved := sm.UserIDResolved(42); resolved || userID != 0 {
		t.Fatalf("resolved user after auth key switch = %d resolved %v, want unresolved", userID, resolved)
	}
	sm.BindUser(42, 0)
	if userID, resolved := sm.UserIDResolved(42); !resolved || userID != 0 {
		t.Fatalf("negative user cache = %d resolved %v, want 0/true", userID, resolved)
	}

	sm.Unregister(c)
	if got := sm.Online(); got != 0 {
		t.Fatalf("online after unregister = %d, want 0", got)
	}

	err := sm.PushToSession(context.Background(), 42, proto.MessageFromServer, &tg.UpdatesTooLong{})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("push to missing session err = %v, want ErrSessionNotFound", err)
	}
}

func TestSessionManagerReplacementClosesOldPhysicalTransport(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	oldTransport := &closeCountingTransport{}
	old := &Conn{sessionID: 42, authKeyID: raw, transport: oldTransport}
	replacement := &Conn{sessionID: 42, authKeyID: raw}

	sm.Register(old)
	sm.Register(replacement)
	if oldTransport.closes != 1 {
		t.Fatalf("old transport closes = %d, want 1", oldTransport.closes)
	}
	// 旧 serveConn 稍后退出时不得把 replacement 从索引删掉。
	sm.Unregister(old)
	if got, ok := sm.bySession[sessionKey{authKeyID: raw, sessionID: 42}]; !ok || got != replacement {
		t.Fatal("old unregister removed the replacement connection")
	}
}

func TestSessionManagerDestroyClosesPhysicalTransport(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{4, 5, 6}
	physical := &closeCountingTransport{}
	c := &Conn{sessionID: 77, authKeyID: raw, transport: physical}
	sm.Register(c)

	if !sm.DestroySessionForAuthKey(raw, 77) {
		t.Fatal("DestroySessionForAuthKey returned false")
	}
	if physical.closes != 1 {
		t.Fatalf("destroyed transport closes = %d, want 1", physical.closes)
	}
	if sm.Online() != 0 {
		t.Fatalf("online after destroy = %d, want 0", sm.Online())
	}
}

func TestSessionManagerBestEffortFanoutPreencodesOnce(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(100)
	for i := 0; i < 2; i++ {
		c := &Conn{
			sessionID:       int64(i + 1),
			authKeyID:       [8]byte{byte(i + 1)},
			outbound:        make(chan outboundOp, 1),
			outboundControl: make(chan outboundOp, 1),
			outboundStop:    make(chan struct{}),
			metrics:         NopMetrics{},
		}
		c.userID.Store(userID)
		c.userIDResolved.Store(true)
		c.receivesUpdates.Store(true)
		sm.Register(c)
	}

	encodes := 0
	sent, err := sm.PushToUserExceptSessionBestEffort(
		context.Background(),
		userID,
		0,
		proto.MessageFromServer,
		&countingOutboundEncoder{count: &encodes},
		0,
	)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if sent != 2 {
		t.Fatalf("sent = %d, want 2", sent)
	}
	if encodes != 1 {
		t.Fatalf("encoded %d times, want 1", encodes)
	}
}

func TestSessionManagerPendingFanoutSharesOneEncodedBodyAndBudget(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(102)
	keys := make([]sessionKey, 0, 2)
	for i := 0; i < 2; i++ {
		c := &Conn{sessionID: int64(i + 1), authKeyID: [8]byte{byte(i + 1)}}
		c.userID.Store(userID)
		c.userIDResolved.Store(true)
		sm.Register(c)
		keys = append(keys, connSessionKey(c))
	}

	encodes := 0
	msg := &countingOutboundEncoder{count: &encodes}
	sent, err := sm.PushToUserExceptSession(context.Background(), userID, 0, proto.MessageFromServer, msg)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if sent != 2 || encodes != 1 {
		t.Fatalf("pending fanout = sent:%d encodes:%d, want 2/1", sent, encodes)
	}

	sm.mu.Lock()
	first := sm.pending[keys[0]][0]
	second := sm.pending[keys[1]][0]
	if first.encoded != second.encoded || first.reservation != second.reservation {
		sm.mu.Unlock()
		t.Fatal("pending sessions did not share encoded body/reservation")
	}
	wantBytes := int64(len(first.encoded.body))
	sm.deletePendingLocked(keys[0])
	if got := sm.pendingBudget.snapshot(); got != wantBytes {
		sm.mu.Unlock()
		t.Fatalf("budget after first session drop = %d, want shared body %d", got, wantBytes)
	}
	sm.deletePendingLocked(keys[1])
	sm.mu.Unlock()
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("budget after last session drop = %d, want 0", got)
	}
}

func TestSessionManagerBestEffortFanoutUsesOneBudgetAndDropsOnlySlowConsumers(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	const userID = int64(101)

	// 三个满队列模拟三个慢设备；没有 outbound actor，确保队列在测试期间不会自行排空。
	slow := make([]*Conn, 0, 3)
	for i := 0; i < 3; i++ {
		tr := &closeCountingTransport{}
		c := &Conn{
			sessionID:       int64(i + 1),
			authKeyID:       [8]byte{byte(i + 1)},
			transport:       tr,
			metrics:         NopMetrics{},
			outbound:        make(chan outboundOp, 1),
			outboundControl: make(chan outboundOp, 1),
			outboundStop:    make(chan struct{}),
		}
		c.outbound <- outboundOp{}
		c.userID.Store(userID)
		c.userIDResolved.Store(true)
		c.receivesUpdates.Store(true)
		sm.Register(c)
		slow = append(slow, c)
	}

	healthy := &Conn{
		sessionID:       99,
		authKeyID:       [8]byte{99},
		metrics:         NopMetrics{},
		outbound:        make(chan outboundOp, 1),
		outboundControl: make(chan outboundOp, 1),
		outboundStop:    make(chan struct{}),
	}
	healthy.userID.Store(userID)
	healthy.userIDResolved.Store(true)
	healthy.receivesUpdates.Store(true)
	sm.Register(healthy)

	const budget = 40 * time.Millisecond
	start := time.Now()
	sent, err := sm.PushToUserExceptSessionBestEffort(
		context.Background(), userID, 0, proto.MessageFromServer, &tg.UpdatesTooLong{}, budget,
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want only healthy session", sent)
	}
	if elapsed >= 3*budget {
		t.Fatalf("fan-out waited %v; want one shared %v budget, not one per slow session", elapsed, budget)
	}
	if got := len(healthy.outbound); got != 1 {
		t.Fatalf("healthy queued ops = %d, want 1", got)
	}
	if healthy.isRetired() {
		t.Fatal("healthy session was terminalized")
	}
	for i, c := range slow {
		if !c.isRetired() {
			t.Fatalf("slow session %d was not terminalized", i)
		}
		if tr := c.transport.(*closeCountingTransport); tr.closes != 1 {
			t.Fatalf("slow session %d transport closes = %d, want 1", i, tr.closes)
		}
	}
}

func TestSessionManagerScopesSameSessionIDByAuthKey(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw1 := [8]byte{1}
	raw2 := [8]byte{2}
	perm1 := [8]byte{9}
	c1 := &Conn{sessionID: 42, authKeyID: raw1}
	c2 := &Conn{sessionID: 42, authKeyID: raw2}

	sm.Register(c1)
	sm.Register(c2)
	if got := sm.Online(); got != 2 {
		t.Fatalf("online = %d, want 2", got)
	}

	sm.BindAuthKeyForSession(raw1, 42, perm1)
	// 两条 PFS/raw 连接可以解析到同一业务 perm key 且复用同一个 session_id；
	// 精确排除必须只匹配 raw1，不能按 business key 把 raw2 一并排除。
	sm.BindAuthKeyForSession(raw2, 42, perm1)
	sm.BindUserForAuthKey(raw1, 42, 100)
	sm.BindUserForAuthKey(raw2, 42, 200)

	if userID, ok := sm.UserIDForAuthKey(raw1, 42); !ok || userID != 100 {
		t.Fatalf("scoped user raw1 = %d ok %v, want 100/true", userID, ok)
	}
	if userID, ok := sm.UserIDForAuthKey(raw2, 42); !ok || userID != 200 {
		t.Fatalf("scoped user raw2 = %d ok %v, want 200/true", userID, ok)
	}
	if _, ok := sm.UserID(42); ok {
		t.Fatal("legacy UserID unexpectedly resolved ambiguous session_id")
	}
	if err := sm.PushToSession(context.Background(), 42, proto.MessageFromServer, &tg.UpdatesTooLong{}); !errors.Is(err, ErrSessionAmbiguous) {
		t.Fatalf("ambiguous push err = %v, want ErrSessionAmbiguous", err)
	}

	sm.BindUserForAuthKey(raw1, 42, 300)
	sm.BindUserForAuthKey(raw2, 42, 300)
	sent, err := sm.PushToUserExceptAuthKeySession(context.Background(), 300, raw1, 42, proto.MessageFromServer, &tg.UpdatesTooLong{})
	if err != nil {
		t.Fatalf("push except scoped session: %v", err)
	}
	if sent != 1 {
		t.Fatalf("pushed to %d sessions, want 1", sent)
	}
	if _, ok := sm.pending[sessionKey{authKeyID: raw1, sessionID: 42}]; ok {
		t.Fatal("excluded session received pending push")
	}
	if got := len(sm.pending[sessionKey{authKeyID: raw2, sessionID: 42}]); got != 1 {
		t.Fatalf("raw2 pending pushes = %d, want 1", got)
	}
	if !sm.DestroySessionForAuthKey(raw1, 42) {
		t.Fatal("scoped destroy did not remove raw1 session")
	}
	if _, ok := sm.AuthKeyIDForSession(raw1, 42); ok {
		t.Fatal("raw1 session survived scoped destroy")
	}
	if _, ok := sm.AuthKeyIDForSession(raw2, 42); !ok {
		t.Fatal("same session_id under raw2 was removed by scoped destroy")
	}
}

func TestSessionManagerCloseSessionsForBusinessAuthKeyClosesBoundTempAndRaw(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	rawTemp := [8]byte{1}
	perm := [8]byte{9}
	otherRaw := [8]byte{2}
	otherPerm := [8]byte{8}
	tempTransport := &closeCountingTransport{}
	permTransport := &closeCountingTransport{}
	otherTransport := &closeCountingTransport{}
	cTemp := &Conn{sessionID: 11, authKeyID: rawTemp, transport: tempTransport}
	cPerm := &Conn{sessionID: 12, authKeyID: perm, transport: permTransport}
	cOther := &Conn{sessionID: 13, authKeyID: otherRaw}
	cOther.transport = otherTransport

	sm.Register(cTemp)
	sm.Register(cPerm)
	sm.Register(cOther)
	sm.BindAuthKeyForSession(rawTemp, 11, perm)
	sm.BindAuthKeyForSession(perm, 12, perm)
	sm.BindAuthKeyForSession(otherRaw, 13, otherPerm)
	sm.BindUserForAuthKey(rawTemp, 11, 100)
	sm.BindUserForAuthKey(perm, 12, 100)
	sm.BindUserForAuthKey(otherRaw, 13, 200)

	if closed := sm.CloseSessionsForBusinessAuthKey(perm); closed != 2 {
		t.Fatalf("closed sessions = %d, want 2", closed)
	}
	if tempTransport.closes != 1 || permTransport.closes != 1 {
		t.Fatalf("transport closes temp=%d perm=%d, want 1/1", tempTransport.closes, permTransport.closes)
	}
	if otherTransport.closes != 0 {
		t.Fatalf("other transport closes = %d, want 0", otherTransport.closes)
	}
	if got := sm.Online(); got != 1 {
		t.Fatalf("online after close = %d, want 1", got)
	}
	if _, ok := sm.AuthKeyIDForSession(rawTemp, 11); ok {
		t.Fatal("temp session still indexed after business auth key close")
	}
	if _, ok := sm.AuthKeyIDForSession(perm, 12); ok {
		t.Fatal("raw perm session still indexed after business auth key close")
	}
	if userID, ok := sm.UserIDForAuthKey(otherRaw, 13); !ok || userID != 200 {
		t.Fatalf("other session user = %d ok %v, want 200/true", userID, ok)
	}
	if closed := sm.CloseSessionsForBusinessAuthKey(perm); closed != 0 {
		t.Fatalf("second close = %d, want 0", closed)
	}
}

func TestSessionManagerCloseSessionsRunsSlowPhysicalClosesConcurrently(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	business := [8]byte{9, 9, 9}
	const sessions = 8
	const closeDelay = 75 * time.Millisecond
	transports := make([]*slowCloseTransport, 0, sessions)
	for i := 0; i < sessions; i++ {
		raw := [8]byte{byte(i + 1)}
		tr := newSlowCloseTransport(closeDelay, nil)
		c := &Conn{sessionID: int64(i + 1), authKeyID: raw, transport: tr}
		sm.Register(c)
		sm.BindAuthKeyForSession(raw, c.sessionID, business)
		transports = append(transports, tr)
	}

	started := time.Now()
	if got := sm.CloseSessionsForBusinessAuthKey(business); got != sessions {
		t.Fatalf("closed sessions = %d, want %d", got, sessions)
	}
	elapsed := time.Since(started)
	// A serial implementation takes ~600ms. Leave ample Windows/CI scheduling margin while
	// still proving that the per-Conn delay is not multiplied by the session count.
	if elapsed >= 4*closeDelay {
		t.Fatalf("batch close elapsed = %v, want concurrent closes near %v", elapsed, closeDelay)
	}
	for i, tr := range transports {
		select {
		case <-tr.done:
		default:
			t.Fatalf("transport %d close had not completed when batch returned", i)
		}
		if got := tr.closes.Load(); got != 1 {
			t.Fatalf("transport %d closes = %d, want 1", i, got)
		}
	}
}

func TestSessionManagerCloseRawSessionsExceptRunsConcurrentlyAndPreservesExcluded(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{6, 6, 6}
	const sessions = 7
	const excludedSession = int64(4)
	const closeDelay = 60 * time.Millisecond
	transports := make([]*slowCloseTransport, 0, sessions)
	for i := 0; i < sessions; i++ {
		tr := newSlowCloseTransport(closeDelay, nil)
		c := &Conn{sessionID: int64(i + 1), authKeyID: raw, transport: tr}
		sm.Register(c)
		transports = append(transports, tr)
	}

	started := time.Now()
	if got, want := sm.CloseSessionsForRawAuthKeyExcept(raw, excludedSession), sessions-1; got != want {
		t.Fatalf("closed sessions = %d, want %d", got, want)
	}
	if elapsed := time.Since(started); elapsed >= 4*closeDelay {
		t.Fatalf("raw-key batch close elapsed = %v, want concurrent closes near %v", elapsed, closeDelay)
	}
	for i, tr := range transports {
		sessionID := int64(i + 1)
		if sessionID == excludedSession {
			if got := tr.closes.Load(); got != 0 {
				t.Fatalf("excluded transport closes = %d, want 0", got)
			}
			continue
		}
		select {
		case <-tr.done:
		default:
			t.Fatalf("transport for session %d had not closed", sessionID)
		}
	}
	if _, ok := sm.bySession[sessionKey{authKeyID: raw, sessionID: excludedSession}]; !ok {
		t.Fatal("excluded session was removed from the registry")
	}
	// Clean up the deliberately preserved connection without making the assertion path depend
	// on test process teardown.
	if !sm.DestroySessionForAuthKey(raw, excludedSession) {
		t.Fatal("cleanup destroy of excluded session failed")
	}
}

func TestForceCloseBatchTimeoutStillClosesProducerAndRPCGates(t *testing.T) {
	release := make(chan struct{})
	const sessions = 4
	scheduler := newInboundRPCScheduler(1, 16, 1<<20)
	defer scheduler.stop(time.Second)
	conns := make([]*Conn, 0, sessions)
	transports := make([]*slowCloseTransport, 0, sessions)
	for i := 0; i < sessions; i++ {
		tr := newSlowCloseTransport(0, release)
		c := &Conn{
			transport:       tr,
			metrics:         NopMetrics{},
			outbound:        make(chan outboundOp, 1),
			outboundControl: make(chan outboundOp, 1),
			outboundStop:    make(chan struct{}),
		}
		c.startInboundRPCScheduler(scheduler, 1, 1, time.Second)
		if err := c.enqueueInboundRPC(context.Background(), inboundRPC{
			method: "shutdown.budget",
			size:   32,
		}); err != nil {
			t.Fatalf("enqueue queued RPC %d: %v", i, err)
		}
		conns = append(conns, c)
		transports = append(transports, tr)
	}

	started := time.Now()
	if completed := forceCloseConnBatch(conns, 40*time.Millisecond); completed {
		t.Fatal("blocked transport close batch unexpectedly completed")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("timed batch close blocked for %v", elapsed)
	}
	for i, c := range conns {
		if !c.isRetired() {
			t.Fatalf("connection %d producer gate remains open after batch timeout", i)
		}
		select {
		case <-c.outboundStop:
		default:
			t.Fatalf("connection %d outbound stop was not published", i)
		}
		select {
		case <-c.rpcRootCtx.Done():
		default:
			t.Fatalf("connection %d RPC root remains open after batch timeout", i)
		}
	}
	if tasks, bytes := scheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("RPC budget after batch gate close = tasks:%d bytes:%d, want zero", tasks, bytes)
	}

	close(release)
	for i, tr := range transports {
		select {
		case <-tr.done:
		case <-time.After(time.Second):
			t.Fatalf("transport %d did not finish after release", i)
		}
	}
}

func TestSessionManagerBusinessAuthKeyIndexTracksRebind(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1}
	oldPerm := [8]byte{7}
	newPerm := [8]byte{8}
	c := &Conn{sessionID: 21, authKeyID: raw}

	sm.Register(c)
	sm.BindAuthKeyForSession(raw, 21, oldPerm)
	sm.BindAuthKeyForSession(raw, 21, newPerm)

	if closed := sm.CloseSessionsForBusinessAuthKey(oldPerm); closed != 0 {
		t.Fatalf("close old business auth key = %d, want 0", closed)
	}
	if got := sm.Online(); got != 1 {
		t.Fatalf("online after closing old key = %d, want 1", got)
	}
	if closed := sm.CloseSessionsForBusinessAuthKey(newPerm); closed != 1 {
		t.Fatalf("close new business auth key = %d, want 1", closed)
	}
	if got := sm.Online(); got != 0 {
		t.Fatalf("online after closing new key = %d, want 0", got)
	}
}

func TestPushToUserAuthKeyUsesOneDeadlineAndDropsOnlySlowPFSConnections(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	business := [8]byte{9, 9}
	const userID = int64(100)
	newConn := func(raw [8]byte, sessionID int64, queueFull bool) (*Conn, *closeCountingTransport) {
		transport := &closeCountingTransport{}
		c := &Conn{
			authKeyID:       raw,
			sessionID:       sessionID,
			metrics:         NopMetrics{},
			transport:       transport,
			outbound:        make(chan outboundOp, 1),
			outboundControl: make(chan outboundOp, 1),
			outboundStop:    make(chan struct{}),
		}
		c.receivesUpdates.Store(true)
		if queueFull {
			c.outbound <- outboundOp{}
		}
		sm.Register(c)
		sm.BindAuthKeyForSession(raw, sessionID, business)
		sm.BindUserForAuthKey(raw, sessionID, userID)
		return c, transport
	}

	slowOne, slowOneTransport := newConn([8]byte{1}, 11, true)
	slowTwo, slowTwoTransport := newConn([8]byte{2}, 12, true)
	healthy, healthyTransport := newConn([8]byte{3}, 13, false)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	sent, err := sm.PushToUserAuthKey(ctx, userID, business, proto.MessageFromServer, &tg.UpdatesTooLong{})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("PushToUserAuthKey: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want only healthy connection", sent)
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("elapsed = %v, want one shared deadline rather than per-session waits", elapsed)
	}
	if !slowOne.isRetired() || !slowTwo.isRetired() || slowOneTransport.closes != 1 || slowTwoTransport.closes != 1 {
		t.Fatalf("slow connections not terminal/closed: one=%v/%d two=%v/%d",
			slowOne.isRetired(), slowOneTransport.closes, slowTwo.isRetired(), slowTwoTransport.closes)
	}
	if healthy.isRetired() || healthyTransport.closes != 0 {
		t.Fatalf("healthy connection was dropped: lifecycle=%v closes=%d", healthy.lifecycleState(), healthyTransport.closes)
	}
	select {
	case <-healthy.outbound:
	default:
		t.Fatal("healthy PFS connection did not receive best-effort enqueue")
	}
}

func TestSessionManagerChannelInterestIndex(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	c := &Conn{sessionID: 42, authKeyID: raw}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, 42, 100)

	sm.TrackChannelInterest(raw, 42, 100, []int64{10, 10, 20})
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 10 online users = %v, want [100]", got)
	}
	sm.TrackChannelInterest(raw, 42, 100, []int64{20})
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel 10 after viewer switch = %v, want empty", got)
	}
	if got := sm.OnlineChannelUserIDs(20, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 20 after viewer switch = %v, want [100]", got)
	}
	sm.TrackChannelInterest(raw, 42, 100, []int64{10})
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel 10 online members before membership sync = %v, want empty", got)
	}
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{10, 30}, sm.ChannelMembershipGeneration(raw, 42))
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 10 online members = %v, want [100]", got)
	}
	if got := sm.OnlineChannelUserIDs(30, 10); len(got) != 0 {
		t.Fatalf("channel 30 viewers = %v, want empty", got)
	}
	if got := sm.OnlineUserIDsForCandidates([]int64{0, 200, 100, 100}, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("candidate online users = %v, want [100]", got)
	}

	sm.BindUserForAuthKey(raw, 42, 200)
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel interest after user switch = %v, want empty", got)
	}
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel membership after user switch = %v, want empty", got)
	}
	sm.TrackChannelInterest(raw, 42, 200, []int64{10})
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 1 || got[0] != 200 {
		t.Fatalf("channel 10 after re-track = %v, want [200]", got)
	}
	sm.AddUserChannelMembership(200, 10)
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 1 || got[0] != 200 {
		t.Fatalf("channel 10 membership after add = %v, want [200]", got)
	}
	sm.RemoveUserChannelMembership(200, 10)
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel membership after remove = %v, want empty", got)
	}
	sm.ClearChannelInterest(raw, 42, 200)
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel interest after explicit clear = %v, want empty", got)
	}

	sm.Unregister(c)
	if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel interest after unregister = %v, want empty", got)
	}
	if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
		t.Fatalf("channel membership after unregister = %v, want empty", got)
	}
}

func TestSessionManagerClearsChannelIndexesOnAuthAndReadinessChanges(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	business := [8]byte{8}
	c := &Conn{sessionID: 42, authKeyID: raw}
	sm.Register(c)
	sm.BindAuthKeyForSession(raw, 42, business)
	sm.BindUserForAuthKey(raw, 42, 100)

	track := func() {
		sm.TrackChannelInterest(raw, 42, 100, []int64{10})
		sm.SetSessionChannelMemberships(raw, 42, 100, []int64{10}, sm.ChannelMembershipGeneration(raw, 42))
		if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
			t.Fatalf("channel viewers before cleanup = %v, want [100]", got)
		}
		if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 1 || got[0] != 100 {
			t.Fatalf("channel members before cleanup = %v, want [100]", got)
		}
	}
	assertCleared := func(label string) {
		if got := sm.OnlineChannelUserIDs(10, 10); len(got) != 0 {
			t.Fatalf("%s viewers = %v, want empty", label, got)
		}
		if got := sm.OnlineChannelMemberUserIDs(10, 10); len(got) != 0 {
			t.Fatalf("%s members = %v, want empty", label, got)
		}
	}

	track()
	sm.SetReceivesUpdatesForAuthKey(raw, 42, false)
	assertCleared("after receivesUpdates=false")

	track()
	sm.BindAuthKeyForSession(raw, 42, [8]byte{9})
	assertCleared("after business auth key change")

	sm.BindAuthKeyForSession(raw, 42, business)
	sm.BindUserForAuthKey(raw, 42, 100)
	track()
	if n := sm.UnbindAuthKey(business); n != 1 {
		t.Fatalf("UnbindAuthKey count = %d, want 1", n)
	}
	assertCleared("after unbind auth key")
}

func TestPushToSessionForAuthKeyImmediateBypassesReadinessQueue(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	c := &Conn{
		sessionID:       42,
		authKeyID:       raw,
		outbound:        make(chan outboundOp, 1),
		outboundControl: make(chan outboundOp, 1),
		outboundStop:    make(chan struct{}),
	}
	sm.Register(c)

	msg := &tg.UpdateShort{Update: &tg.UpdateLoginToken{}, Date: 1700000000}
	if err := sm.PushToSessionForAuthKeyImmediate(context.Background(), raw, 42, proto.MessageFromServer, msg); err != nil {
		t.Fatalf("immediate push: %v", err)
	}

	select {
	case op := <-c.outbound:
		defer op.releaseReservation(c.outboundTrackedBudget)
		if op.encoded == nil {
			t.Fatal("immediate push did not retain its encoded body")
		}
		var got tg.UpdateShort
		if err := got.Decode(&bin.Buffer{Buf: op.encoded.body}); err != nil {
			t.Fatalf("decode enqueued update: %v", err)
		}
		if _, ok := got.Update.(*tg.UpdateLoginToken); !ok || got.Date != msg.Date {
			t.Fatalf("enqueued update = %+v, want login token date %d", got, msg.Date)
		}
	case <-time.After(time.Second):
		t.Fatal("immediate push was not enqueued")
	}

	sm.mu.RLock()
	pending := len(sm.pending[sessionKey{authKeyID: raw, sessionID: 42}])
	sm.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("pending pushes = %d, want 0", pending)
	}
}

func TestPendingPushBodiesUseGlobalByteBudgetAndReleaseOnDrop(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	msg := &tg.UpdateShort{Update: &tg.UpdateLoginToken{}, Date: 1700000000}
	encoded, err := encodeOutboundMessage(msg)
	if err != nil {
		t.Fatalf("encode pending fixture: %v", err)
	}
	sm.pendingBudget = newOutboundTrackedBudget(int64(len(encoded.body)))
	key := sessionKey{authKeyID: [8]byte{9}, sessionID: 77}

	sm.mu.Lock()
	first := sm.queueLocked(key, proto.MessageFromServer, msg)
	second := sm.queueLocked(key, proto.MessageFromServer, msg)
	sm.mu.Unlock()
	if !first || second {
		t.Fatalf("pending queue results = first %v second %v, want true/false at byte cap", first, second)
	}
	if got := sm.pendingBudget.snapshot(); got != int64(len(encoded.body)) {
		t.Fatalf("pending body budget = %d, want %d", got, len(encoded.body))
	}

	sm.mu.Lock()
	sm.deletePendingLocked(key)
	sm.mu.Unlock()
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("pending body budget after drop = %d, want zero", got)
	}
}

func TestPendingFlushGlobalBodyPressureDoesNotTerminateHealthyConnection(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	key := sessionKey{authKeyID: [8]byte{6}, sessionID: 66}
	c := &Conn{
		authKeyID:             key.authKeyID,
		sessionID:             key.sessionID,
		outbound:              make(chan outboundOp, 1),
		outboundControl:       make(chan outboundOp, 1),
		outboundStop:          make(chan struct{}),
		metrics:               NopMetrics{},
		outboundTrackedBudget: newOutboundTrackedBudget(1),
	}
	const userID = int64(606)
	c.userID.Store(userID)
	c.userIDResolved.Store(true)
	sm.Register(c)

	msg := &tg.UpdateShort{Update: &tg.UpdateLoginToken{}, Date: 1700000000}
	sm.mu.Lock()
	if !sm.queueLocked(key, proto.MessageFromServer, msg) {
		sm.mu.Unlock()
		t.Fatal("queue pending push")
	}
	sm.flushing[key] = true
	sm.mu.Unlock()

	// Enter at the final retry so the test exercises the durable-difference fallback without
	// waiting for the production backoff timer.
	sm.runFlush(c, key, userID, maxFlushAttempts-1)
	if c.isRetired() {
		t.Fatal("shared body pressure terminated a healthy pending-flush connection")
	}
	if !c.receivesUpdates.Load() {
		t.Fatal("pending flush did not activate difference fallback after bounded retries")
	}
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("pending budget after fallback = %d, want zero", got)
	}
}

func TestPendingPushBudgetSurvivesTakeAndReturnsAcrossOverflowAndUnregister(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	msg := &tg.UpdateShort{Update: &tg.UpdateLoginToken{}, Date: 1700000000}
	encoded, err := encodeOutboundMessage(msg)
	if err != nil {
		t.Fatalf("encode pending fixture: %v", err)
	}
	bytesPerPush := int64(len(encoded.body))
	sm.pendingBudget = newOutboundTrackedBudget(bytesPerPush * (maxPendingPushesPerSession + 8))
	key := sessionKey{authKeyID: [8]byte{7}, sessionID: 55}
	c := &Conn{authKeyID: key.authKeyID, sessionID: key.sessionID}
	sm.Register(c)

	sm.mu.Lock()
	for i := 0; i < maxPendingPushesPerSession+5; i++ {
		if !sm.queueLocked(key, proto.MessageFromServer, msg) {
			sm.mu.Unlock()
			t.Fatalf("queue pending push %d unexpectedly failed", i)
		}
	}
	if got, want := sm.pendingBudget.snapshot(), bytesPerPush*maxPendingPushesPerSession; got != want {
		sm.mu.Unlock()
		t.Fatalf("budget after overflow replacement = %d, want %d", got, want)
	}
	batch := sm.takePendingLocked(key, true)
	sm.mu.Unlock()
	if len(batch) != maxPendingPushesPerSession {
		t.Fatalf("taken pending pushes = %d, want %d", len(batch), maxPendingPushesPerSession)
	}
	// take transfers ownership to runFlush; deleting the map entry must not release bodies while
	// the batch still references them.
	if got, want := sm.pendingBudget.snapshot(), bytesPerPush*maxPendingPushesPerSession; got != want {
		t.Fatalf("budget after take = %d, want transferred ownership %d", got, want)
	}
	releaseQueuedPushes(batch)
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("budget after taken batch release = %d, want 0", got)
	}

	sm.mu.Lock()
	if !sm.queueLocked(key, proto.MessageFromServer, msg) {
		sm.mu.Unlock()
		t.Fatal("queue before unregister failed")
	}
	sm.mu.Unlock()
	sm.Unregister(c)
	if got := sm.pendingBudget.snapshot(); got != 0 {
		t.Fatalf("budget after unregister = %d, want 0", got)
	}
}

// TestSessionManagerPush 验证主动推送端到端：两个 client 连接握手并建立 session 后，
// server 经 PushToSession / PushToUser 主动向其推送，client 收到。
func TestSessionManagerPush(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})

	conn1, auth1, cipher1 := dialHandshake(t, addr, dc, pub)
	conn2, auth2, cipher2 := dialHandshake(t, addr, dc, pub)

	// 各发一个 ping 建立 session，触发注册（并清掉 new_session_created/pong/ack）。
	msgGen := proto.NewMessageIDGen(time.Now)
	sendEncrypted(t, conn1, cipher1, auth1, msgGen.New(proto.MessageFromClient), &mt.PingRequest{PingID: 1})
	collectReplies(t, conn1, cipher1, auth1.AuthKey, mt.PongTypeID)
	sendEncrypted(t, conn2, cipher2, auth2, msgGen.New(proto.MessageFromClient), &mt.PingRequest{PingID: 2})
	collectReplies(t, conn2, cipher2, auth2.AuthKey, mt.PongTypeID)

	if got := srv.Conns().Online(); got != 2 {
		t.Fatalf("online = %d, want 2", got)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1) PushToSession：session2 尚未进入 updates 同步入口时先暂存，ready 后下发。
	if err := srv.Conns().PushToSession(ctx, auth2.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
		t.Fatalf("push to session: %v", err)
	}
	srv.Conns().SetReceivesUpdates(auth2.SessionID, true)
	r2 := collectReplies(t, conn2, cipher2, auth2.AuthKey, tg.UpdatesTooLongTypeID)
	mustHave(t, r2, tg.UpdatesTooLongTypeID, "pushed updates on conn2")

	// 2) BindUser + PushToUser：按 user 维度推送给 conn1。
	srv.Conns().BindUser(auth1.SessionID, 100)
	srv.Conns().SetReceivesUpdates(auth1.SessionID, true)
	sent, err := srv.Conns().PushToUser(ctx, 100, proto.MessageFromServer, &tg.UpdatesTooLong{})
	if err != nil {
		t.Fatalf("push to user: %v", err)
	}
	if sent != 1 {
		t.Fatalf("pushed to %d conns, want 1", sent)
	}
	r1 := collectReplies(t, conn1, cipher1, auth1.AuthKey, tg.UpdatesTooLongTypeID)
	mustHave(t, r1, tg.UpdatesTooLongTypeID, "pushed updates on conn1")

	// 3) PushToUserExceptSession：模拟 SyncUpdatesNotMe，跳过当前 session。
	srv.Conns().BindUser(auth1.SessionID, 200)
	srv.Conns().BindUser(auth2.SessionID, 200)
	sent, err = srv.Conns().PushToUserExceptSession(ctx, 200, auth2.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{})
	if err != nil {
		t.Fatalf("push to user except session: %v", err)
	}
	if sent != 1 {
		t.Fatalf("pushed to %d conns, want 1 after excluding current session", sent)
	}
	r1 = collectReplies(t, conn1, cipher1, auth1.AuthKey, tg.UpdatesTooLongTypeID)
	mustHave(t, r1, tg.UpdatesTooLongTypeID, "pushed not-me updates on conn1")
}

func BenchmarkSessionManagerOnlineCandidateFilter(b *testing.B) {
	sm := NewSessionManager(zaptest.NewLogger(b))
	const online = 200_000
	rawPrefix := [8]byte{9}
	for i := 1; i <= online; i++ {
		raw := rawPrefix
		raw[1] = byte(i)
		raw[2] = byte(i >> 8)
		raw[3] = byte(i >> 16)
		raw[4] = byte(i >> 24)
		c := &Conn{sessionID: int64(i), authKeyID: raw}
		sm.Register(c)
		sm.BindUserForAuthKey(raw, int64(i), int64(i))
	}
	candidates := make([]int64, 0, 500)
	for i := 0; i < 500; i++ {
		candidates = append(candidates, int64(i*97+1))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got := sm.OnlineUserIDsForCandidates(candidates, 500)
		if len(got) == 0 {
			b.Fatal("no candidates matched")
		}
	}
}
