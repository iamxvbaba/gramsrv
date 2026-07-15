package mtprotoedge

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appupdates "telesrv/internal/app/updates"
	"telesrv/internal/domain"
	rpchandler "telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

type blockingCloseRPCResultTransport struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type identifiedRouterHandler struct {
	router *rpchandler.Router
	userID int64
}

type failingRPCResultEncoder struct {
	err error
}

func (e failingRPCResultEncoder) Encode(*bin.Buffer) error { return e.err }

type encodeFailingIdentifiedRouterHandler struct {
	identifiedRouterHandler
	err error
}

func (h encodeFailingIdentifiedRouterHandler) Dispatch(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, error) {
	result, err := h.identifiedRouterHandler.Dispatch(ctx, authKeyID, sessionID, b)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	return failingRPCResultEncoder{err: h.err}, nil
}

func (h encodeFailingIdentifiedRouterHandler) DispatchWithMethod(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, string, error) {
	result, method, err := h.identifiedRouterHandler.DispatchWithMethod(ctx, authKeyID, sessionID, b)
	if err != nil {
		return nil, method, err
	}
	if result == nil {
		return nil, method, nil
	}
	return failingRPCResultEncoder{err: h.err}, method, nil
}

func (h identifiedRouterHandler) requestContext(ctx context.Context) context.Context {
	return rpchandler.WithClientInfo(rpchandler.WithUserID(ctx, h.userID), rpchandler.ClientInfo{Type: rpchandler.ClientTypeTDesktop})
}

func (h identifiedRouterHandler) Dispatch(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, error) {
	return h.router.Dispatch(h.requestContext(ctx), authKeyID, sessionID, b)
}

func (h identifiedRouterHandler) DispatchWithMethod(ctx context.Context, authKeyID [8]byte, sessionID int64, b *bin.Buffer) (bin.Encoder, string, error) {
	return h.router.DispatchWithMethod(h.requestContext(ctx), authKeyID, sessionID, b)
}

func (h identifiedRouterHandler) NegotiatedLayer(authKeyID [8]byte, sessionID int64) (int, bool) {
	return h.router.NegotiatedLayer(authKeyID, sessionID)
}

func newBlockingCloseRPCResultTransport() *blockingCloseRPCResultTransport {
	return &blockingCloseRPCResultTransport{started: make(chan struct{}), release: make(chan struct{})}
}

func (*blockingCloseRPCResultTransport) Send(context.Context, *bin.Buffer) error { return nil }
func (*blockingCloseRPCResultTransport) Recv(context.Context, *bin.Buffer) error { return io.EOF }
func (t *blockingCloseRPCResultTransport) Close() error {
	t.once.Do(func() { close(t.started) })
	<-t.release
	return nil
}

func TestRPCResultCachePublishesOnlyAfterPhysicalWrite(t *testing.T) {
	tr := newGatedRequiredControlTransport(nil)
	s := New(Options{WriteTimeout: time.Second})
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74001, 1)
	t.Cleanup(c.ForceClose)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)

	owner, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("initial flight owner = %+v err=%v", owner, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- s.sendResult(context.Background(), c, reqMsgID, exactTestRPCResult(&tg.Config{ThisDC: 2}))
	}()
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("rpc_result did not reach physical writer")
	}
	if _, ok := s.rpcResults.Get(key.ID, c.sessionID, reqMsgID); ok {
		t.Fatal("rpc_result became completed before physical write")
	}
	pending, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || pending.state != rpcResultAcquirePending || pending.waiter == nil {
		t.Fatalf("flight while write blocked = %+v err=%v", pending, err)
	}

	tr.unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("sendResult after write: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sendResult did not finish")
	}
	if encoded, ok, err := pending.waiter.Wait(context.Background()); err != nil || !ok || encoded == nil {
		t.Fatalf("pending waiter after write = encoded:%p ok:%v err:%v", encoded, ok, err)
	}
	completed, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || completed.state != rpcResultAcquireCompleted || completed.encoded == nil {
		t.Fatalf("flight after write = %+v err=%v", completed, err)
	}
}

func TestRPCResultPostResponseHookWaitsForPhysicalWrite(t *testing.T) {
	tr := newGatedRequiredControlTransport(nil)
	s := New(Options{WriteTimeout: time.Second})
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74006, 1)
	legacyCanonicalTestConn(t, c)
	t.Cleanup(c.ForceClose)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	claim, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("initial flight owner = %+v err=%v", claim, err)
	}

	delivered := make(chan struct{})
	if err := s.publishRPCResult(c, reqMsgID, "updates.getState", claim.owner, exactTestRPCResult(&tg.UpdatesState{}), func() {
		close(delivered)
	}); err != nil {
		t.Fatalf("publish rpc_result: %v", err)
	}
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("rpc_result did not reach physical writer")
	}
	select {
	case <-delivered:
		t.Fatal("post-response hook ran while physical writer was blocked")
	case <-time.After(20 * time.Millisecond):
	}

	tr.unblock()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("post-response hook did not run after physical write")
	}
}

func TestRouterUpdateCursorCommitsOnlyAfterHandleRPCPhysicalWrite(t *testing.T) {
	const userID int64 = 1000000301
	events := memory.NewUpdateEventStore()
	states := memory.NewUpdateStateStore()
	if err := events.Append(context.Background(), userID, domain.UpdateEvent{
		UserID: userID, Type: domain.UpdateEventNewMessage,
		Pts: 1, PtsCount: 1, Date: 1700000301,
		Message: domain.Message{ID: 1, OwnerUserID: userID},
	}); err != nil {
		t.Fatalf("seed update event: %v", err)
	}
	router := rpchandler.New(rpchandler.Config{}, rpchandler.Deps{
		Updates: appupdates.NewService(states, events),
	}, zaptest.NewLogger(t), clock.System)
	handler := identifiedRouterHandler{router: router, userID: userID}
	tr := newGatedRequiredControlTransport(nil)
	s := New(Options{legacyRPC: handler, WriteTimeout: time.Second})
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74007, 1)
	t.Cleanup(c.ForceClose)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	claim, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("acquire flight = %+v err=%v", claim, err)
	}
	var request bin.Buffer
	if err := (&tg.UpdatesGetStateRequest{}).Encode(&request); err != nil {
		t.Fatalf("encode getState: %v", err)
	}
	handled := make(chan error, 1)
	go func() {
		handled <- s.handleRPC(context.Background(), c, reqMsgID, "updates.getState", &request, claim.owner)
	}()
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("getState rpc_result did not reach physical writer")
	}
	select {
	case err := <-handled:
		if err != nil {
			t.Fatalf("handleRPC: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handleRPC remained coupled to physical write")
	}
	if state, found, err := states.Get(context.Background(), key.ID, userID); err != nil || found {
		t.Fatalf("confirmed before physical write = %+v/%v err=%v", state, found, err)
	}
	if state, found := states.ObservedClientState(key.ID, userID); found {
		t.Fatalf("observed before physical write = %+v/%v", state, found)
	}

	tr.unblock()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		confirmed, found, err := states.Get(context.Background(), key.ID, userID)
		observed, observedFound := states.ObservedClientState(key.ID, userID)
		if err == nil && found && confirmed.Pts == 1 && observedFound && observed.Pts == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	confirmed, found, err := states.Get(context.Background(), key.ID, userID)
	observed, observedFound := states.ObservedClientState(key.ID, userID)
	t.Fatalf("post-write cursor = confirmed:%+v/%v err=%v observed:%+v/%v", confirmed, found, err, observed, observedFound)
}

func TestRouterUpdateCursorDoesNotCommitAfterPhysicalWriteFailure(t *testing.T) {
	const userID int64 = 1000000302
	events := memory.NewUpdateEventStore()
	states := memory.NewUpdateStateStore()
	if err := events.Append(context.Background(), userID, domain.UpdateEvent{
		UserID: userID, Type: domain.UpdateEventNewMessage,
		Pts: 1, PtsCount: 1, Date: 1700000302,
		Message: domain.Message{ID: 1, OwnerUserID: userID},
	}); err != nil {
		t.Fatalf("seed update event: %v", err)
	}
	router := rpchandler.New(rpchandler.Config{}, rpchandler.Deps{
		Updates: appupdates.NewService(states, events),
	}, zaptest.NewLogger(t), clock.System)
	tr := &failAfterTransport{}
	tr.failAt.Store(1)
	s := New(Options{legacyRPC: identifiedRouterHandler{router: router, userID: userID}, WriteTimeout: time.Second})
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74008, 1)
	t.Cleanup(c.ForceClose)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	claim, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("acquire flight = %+v err=%v", claim, err)
	}
	var request bin.Buffer
	if err := (&tg.UpdatesGetStateRequest{}).Encode(&request); err != nil {
		t.Fatalf("encode getState: %v", err)
	}
	if err := s.handleRPC(context.Background(), c, reqMsgID, "updates.getState", &request, claim.owner); err != nil {
		t.Fatalf("handleRPC admission: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	replayable := false
	for time.Now().Before(deadline) {
		if cached, ok := s.rpcResults.Get(key.ID, c.sessionID, reqMsgID); ok && cached.deliveryState() == rpcResultDeliveryReplayable {
			replayable = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !replayable {
		t.Fatal("failed physical result did not become replayable")
	}
	if state, found, err := states.Get(context.Background(), key.ID, userID); err != nil || found {
		t.Fatalf("failed write advanced confirmed = %+v/%v err=%v", state, found, err)
	}
	if state, found := states.ObservedClientState(key.ID, userID); found {
		t.Fatalf("failed write advanced observed = %+v/%v", state, found)
	}
}

func TestRouterUpdateCursorDoesNotCommitWhenResultEncodingFails(t *testing.T) {
	const userID int64 = 1000000303
	events := memory.NewUpdateEventStore()
	states := memory.NewUpdateStateStore()
	if err := events.Append(context.Background(), userID, domain.UpdateEvent{
		UserID: userID, Type: domain.UpdateEventNewMessage,
		Pts: 1, PtsCount: 1, Date: 1700000303,
		Message: domain.Message{ID: 1, OwnerUserID: userID},
	}); err != nil {
		t.Fatalf("seed update event: %v", err)
	}
	router := rpchandler.New(rpchandler.Config{}, rpchandler.Deps{
		Updates: appupdates.NewService(states, events),
	}, zaptest.NewLogger(t), clock.System)
	handler := encodeFailingIdentifiedRouterHandler{
		identifiedRouterHandler: identifiedRouterHandler{router: router, userID: userID},
		err:                     errors.New("encode result"),
	}
	tr := &collectingSessionTransport{}
	s := New(Options{legacyRPC: handler, WriteTimeout: time.Second})
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74009, 1)
	t.Cleanup(c.ForceClose)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	claim, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || claim.state != rpcResultAcquireOwner {
		t.Fatalf("acquire flight = %+v err=%v", claim, err)
	}
	var request bin.Buffer
	if err := (&tg.UpdatesGetStateRequest{}).Encode(&request); err != nil {
		t.Fatalf("encode getState: %v", err)
	}
	if err := s.handleRPC(context.Background(), c, reqMsgID, "updates.getState", &request, claim.owner); err != nil {
		t.Fatalf("handleRPC encoding fallback: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cached, ok := s.rpcResults.Get(key.ID, c.sessionID, reqMsgID); ok && cached.deliveryState() == rpcResultDeliveryDelivered {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(tr.snapshot()) == 0 {
		t.Fatal("INTERNAL fallback was not physically delivered")
	}
	if state, found, err := states.Get(context.Background(), key.ID, userID); err != nil || found {
		t.Fatalf("encoding failure advanced confirmed = %+v/%v err=%v", state, found, err)
	}
	if state, found := states.ObservedClientState(key.ID, userID); found {
		t.Fatalf("encoding failure advanced observed = %+v/%v", state, found)
	}
}

func TestRPCResultPrewriteFailureFencesConnBeforeCachePublication(t *testing.T) {
	tr := &collectingSessionTransport{}
	s := New(Options{WriteTimeout: 20 * time.Millisecond})
	// No rpc_result can reserve its conservative 3x wire scratch from one byte.
	// The actor therefore fails before touching the socket, which used to leave a
	// live Conn with a prematurely completed cache entry.
	s.outboundScratchPool = newOutboundScratchPool(1)
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74002, 1)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	owner, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("initial flight owner = %+v err=%v", owner, err)
	}

	err = s.sendResult(context.Background(), c, reqMsgID, exactTestRPCResult(&tg.Config{ThisDC: 2}))
	if err == nil || (!errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, ErrConnClosed) && !errors.Is(err, ErrOutboundTrackedBudget)) {
		t.Fatalf("prewrite sendResult error = %v", err)
	}
	closeDeadline := time.Now().Add(time.Second)
	for !tr.closed.Load() && time.Now().Before(closeDeadline) {
		time.Sleep(time.Millisecond)
	}
	if !c.isRetired() || !tr.closed.Load() || c.isPhysicalTransportCurrentOpen() {
		t.Fatalf("failed delivery did not fence Conn: retired=%v closed=%v current_open=%v", c.isRetired(), tr.closed.Load(), c.isPhysicalTransportCurrentOpen())
	}
	completed, acquireErr := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if acquireErr != nil || completed.state != rpcResultAcquireCompleted || completed.encoded == nil {
		t.Fatalf("terminal replay cache = %+v err=%v", completed, acquireErr)
	}
	if got := s.rpcResults.flightLimit.snapshot(); got != 0 {
		t.Fatalf("failed delivery leaked flight slot: %d", got)
	}
	c.Close()
}

func TestRPCResultFailureAfterIntentionalTerminalDoesNotCloseTransferLease(t *testing.T) {
	tr := &collectingSessionTransport{}
	s := New(Options{WriteTimeout: time.Second})
	s.outboundTrackedBudget = newOutboundTrackedBudget(1)
	key := newTestAuthKey(t)
	oldConn := s.newConn(tr, key, 74003, 1)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	owner, err := s.rpcResults.Acquire(key.ID, oldConn.sessionID, reqMsgID)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("initial flight owner = %+v err=%v", owner, err)
	}

	// Session replacement publishes terminal before transferring the physical
	// lease. A late old-generation result sees ErrConnClosed at producer admission;
	// it may publish cache-only, but must not upgrade that intentional fence into
	// a physical close that makes Transfer fail.
	oldConn.beginTerminalShutdown()
	err = s.sendResult(context.Background(), oldConn, reqMsgID, exactTestRPCResult(&tg.Config{ThisDC: 2}))
	if !errors.Is(err, ErrOutboundTrackedBudget) {
		t.Fatalf("late result error = %v, want ErrOutboundTrackedBudget", err)
	}
	if tr.closed.Load() {
		t.Fatal("late result closed intentionally transferable transport")
	}
	if !oldConn.waitOutboundShutdownUntil(time.Second) {
		t.Fatal("old outbound actor did not stop")
	}
	nextLease, ok := oldConn.transferTransportOwnership()
	if !ok || nextLease == nil {
		t.Fatal("late result prevented physical transfer")
	}
	newConn := s.newConnWithLease(nextLease, key, 74004, 1)
	oldConn.ForceClose()
	if tr.closed.Load() || !newConn.isPhysicalTransportCurrentOpen() {
		t.Fatalf("stale close after transfer: raw_closed=%v current_open=%v", tr.closed.Load(), newConn.isPhysicalTransportCurrentOpen())
	}
	completed, acquireErr := s.rpcResults.Acquire(key.ID, oldConn.sessionID, reqMsgID)
	if acquireErr != nil || completed.state != rpcResultAcquireCompleted || completed.encoded == nil {
		t.Fatalf("late result cache = %+v err=%v", completed, acquireErr)
	}
	newConn.ForceClose()
}

func TestRPCResultPublishesBeforePathologicalPhysicalCloseReturns(t *testing.T) {
	tr := newBlockingCloseRPCResultTransport()
	s := New(Options{WriteTimeout: time.Second})
	s.outboundTrackedBudget = newOutboundTrackedBudget(1)
	key := newTestAuthKey(t)
	c := s.newConn(tr, key, 74005, 1)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	owner, err := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("initial flight owner = %+v err=%v", owner, err)
	}

	done := make(chan error, 1)
	go func() {
		done <- s.sendResult(context.Background(), c, reqMsgID, exactTestRPCResult(&tg.Config{ThisDC: 2}))
	}()
	select {
	case <-tr.started:
	case <-time.After(time.Second):
		t.Fatal("physical Close did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrOutboundTrackedBudget) {
			t.Fatalf("sendResult error = %v, want budget error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pathological physical Close blocked result publication")
	}
	completed, acquireErr := s.rpcResults.Acquire(key.ID, c.sessionID, reqMsgID)
	if acquireErr != nil || completed.state != rpcResultAcquireCompleted || completed.encoded == nil {
		t.Fatalf("completed result before raw Close return = %+v err=%v", completed, acquireErr)
	}
	close(tr.release)
	if c.transportLease != nil {
		if err := c.transportLease.owner.waitClosed(); err != nil {
			t.Fatalf("physical Close: %v", err)
		}
	}
	c.Close()
}
