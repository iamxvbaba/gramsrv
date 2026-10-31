package rpc

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appsecret "telesrv/internal/app/secretchat"
	"telesrv/internal/domain"
	"telesrv/internal/postresponse"
	"telesrv/internal/store/memory"
)

func dispatchForReceivesUpdates(t *testing.T, sessions SessionBinder, wrapWithoutUpdates, loggedIn bool) context.Context {
	t.Helper()
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)

	var inner bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&inner); err != nil {
		t.Fatalf("encode help.getConfig: %v", err)
	}
	var in bin.Buffer
	if wrapWithoutUpdates {
		in.PutID(tg.InvokeWithoutUpdatesRequestTypeID)
	}
	in.Put(inner.Buf)

	ctx := postresponse.WithCallbacks(context.Background())
	if loggedIn {
		ctx = WithUserID(ctx, 1000000001)
	}
	if _, err := r.Dispatch(ctx, [8]byte{1}, 42, &in); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return ctx
}

// TestDispatchMarksSessionReceivesUpdates verifies that a plain RPC sent by a
// logged-in connection (not wrapped in invokeWithoutUpdates) counts as declaring
// receipt of updates. Relying only on updates.getState/getDifference to set the
// flag would miss clients that reconnect after a warm recovery: they do not
// rebuild the sync baseline, so the flag would never be set and proactive pushes
// would stay buffered until they time out and are dropped, showing up as the other
// side's messages no longer syncing in real time.
func TestDispatchMarksSessionReceivesUpdates(t *testing.T) {
	sessions := &captureSessions{}
	ctx := dispatchForReceivesUpdates(t, sessions, false, true)
	if sessions.snapshot().receives {
		t.Fatal("plain RPC marked receivesUpdates before rpc_result delivery")
	}
	postresponse.Run(ctx)
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if !sessions.receives {
		t.Fatal("plain RPC from logged-in session must mark receivesUpdates")
	}
	if sessions.sessionID != 42 {
		t.Fatalf("marked session_id = %d, want 42", sessions.sessionID)
	}
	if sessions.message != nil {
		t.Fatalf("session readiness emitted unsolicited update %T; readiness must only open delivery", sessions.message)
	}
}

type activationCaptureSessions struct {
	*captureSessions
	activationMu     sync.Mutex
	nextToken        uint64
	activeToken      uint64
	beginCalls       int
	grants           int
	endCalls         int
	bootstrapMu      sync.Mutex
	bootstrapNext    uint64
	bootstrapActive  uint64
	bootstrapBegin   int
	bootstrapEnd     int
	bootstrapSuccess int
	bootstrapProbed  bool
}

func (s *activationCaptureSessions) ReceivesUpdatesForAuthKey([8]byte, int64) bool {
	return s.captureSessions.snapshot().receives
}

func (s *activationCaptureSessions) BeginSessionUpdatesActivation([8]byte, int64) (uint64, bool) {
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	s.beginCalls++
	if s.activeToken != 0 || s.captureSessions.snapshot().receives {
		return 0, false
	}
	s.nextToken++
	s.activeToken = s.nextToken
	s.grants++
	return s.activeToken, true
}

func (s *activationCaptureSessions) EndSessionUpdatesActivation(_ [8]byte, _ int64, token uint64) {
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	s.endCalls++
	if s.activeToken == token {
		s.activeToken = 0
	}
}

func (s *activationCaptureSessions) activationSnapshot() (begin, grants, end int, active uint64) {
	s.activationMu.Lock()
	defer s.activationMu.Unlock()
	return s.beginCalls, s.grants, s.endCalls, s.activeToken
}

func (s *activationCaptureSessions) BeginSessionBootstrapProbe([8]byte, int64) (uint64, bool) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	s.bootstrapBegin++
	if s.bootstrapActive != 0 || s.bootstrapProbed {
		return 0, false
	}
	s.bootstrapNext++
	s.bootstrapActive = s.bootstrapNext
	return s.bootstrapActive, true
}

func (s *activationCaptureSessions) EndSessionBootstrapProbe(_ [8]byte, _ int64, token uint64, success bool) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	s.bootstrapEnd++
	if s.bootstrapActive != token {
		return
	}
	s.bootstrapActive = 0
	if success {
		s.bootstrapSuccess++
		s.bootstrapProbed = true
	}
}

func (s *activationCaptureSessions) bootstrapSnapshot() (begin, end, success int, active uint64, probed bool) {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	return s.bootstrapBegin, s.bootstrapEnd, s.bootstrapSuccess, s.bootstrapActive, s.bootstrapProbed
}

func TestDispatchCoalescesConcurrentSessionReadinessActivation(t *testing.T) {
	sessions := &activationCaptureSessions{captureSessions: &captureSessions{}}
	first := dispatchForReceivesUpdates(t, sessions, false, true)
	second := dispatchForReceivesUpdates(t, sessions, false, true)

	if begin, grants, end, active := sessions.activationSnapshot(); begin != 2 || grants != 1 || end != 0 || active == 0 {
		t.Fatalf("activation before delivery = begin:%d grants:%d end:%d active:%d", begin, grants, end, active)
	}
	postresponse.Run(second)
	if got := sessions.snapshot().receivesCalls; got != 0 {
		t.Fatalf("non-owner delivery marked readiness %d times", got)
	}
	postresponse.Run(first)
	if got := sessions.snapshot(); !got.receives || got.receivesCalls != 1 {
		t.Fatalf("owner delivery readiness = receives:%v calls:%d", got.receives, got.receivesCalls)
	}
	if begin, grants, end, active := sessions.activationSnapshot(); begin != 2 || grants != 1 || end != 1 || active != 0 {
		t.Fatalf("activation after delivery = begin:%d grants:%d end:%d active:%d", begin, grants, end, active)
	}
}

func TestSessionReadinessActivationReleasedWhenCallbackRegistrationFails(t *testing.T) {
	sessions := &activationCaptureSessions{captureSessions: &captureSessions{}}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	ctx := WithSessionID(WithRawAuthKeyID(context.Background(), [8]byte{9}), 99)
	r.stageSessionUpdatesReadyAfterDelivery(ctx, 1000000009)
	if begin, grants, end, active := sessions.activationSnapshot(); begin != 1 || grants != 1 || end != 1 || active != 0 {
		t.Fatalf("activation after missing callback registry = begin:%d grants:%d end:%d active:%d", begin, grants, end, active)
	}
}

func TestSuppressSessionActivationReleasesClaimButKeepsCursorWork(t *testing.T) {
	sessions := &activationCaptureSessions{captureSessions: &captureSessions{}}
	bootstrap := &captureBootstrapReadyStore{BootstrapUpdateJobStore: memory.NewBootstrapUpdateJobStore()}
	r := New(Config{}, Deps{Sessions: sessions, BootstrapUpdates: bootstrap}, zaptest.NewLogger(t), clock.System)
	ctx := WithSessionID(WithRawAuthKeyID(context.Background(), [8]byte{8}), 88)
	ctx, plan := withUpdatesDeliveryPlan(ctx)
	r.tryStageSessionUpdatesReady(ctx, plan, 1000000008)
	r.tryStageBootstrapProbe(ctx, plan, 1000000008)
	plan.stageCursor([8]byte{7}, 1000000008, domain.UpdateState{Pts: 7}, domain.UpdateStateCommitDeliveredOnly)
	plan.suppressSessionActivation()
	if plan.markSessionReady || plan.publishBootstrap || !plan.commitCursor {
		t.Fatalf("suppressed plan = ready:%v bootstrap:%v cursor:%v", plan.markSessionReady, plan.publishBootstrap, plan.commitCursor)
	}
	if begin, grants, end, active := sessions.activationSnapshot(); begin != 1 || grants != 1 || end != 1 || active != 0 {
		t.Fatalf("activation after suppression = begin:%d grants:%d end:%d active:%d", begin, grants, end, active)
	}
	if begin, end, success, active, probed := sessions.bootstrapSnapshot(); begin != 1 || end != 1 || success != 0 || active != 0 || probed {
		t.Fatalf("bootstrap after suppression = begin:%d end:%d success:%d active:%d probed:%v", begin, end, success, active, probed)
	}
}

func TestBootstrapProbeReleasedWhenCallbackRegistrationFails(t *testing.T) {
	sessions := &activationCaptureSessions{captureSessions: &captureSessions{}}
	bootstrap := &captureBootstrapReadyStore{BootstrapUpdateJobStore: memory.NewBootstrapUpdateJobStore()}
	r := New(Config{}, Deps{Sessions: sessions, BootstrapUpdates: bootstrap}, zaptest.NewLogger(t), clock.System)
	ctx := WithAuthKeyID(context.Background(), [8]byte{7})
	ctx = WithRawAuthKeyID(ctx, [8]byte{8})
	ctx = WithSessionID(ctx, 88)
	r.stageUpdatesBaselineAfterDelivery(ctx, 1000000008, nil, 0, nil, true)
	if bootstrap.readyCalls != 0 {
		t.Fatalf("bootstrap store called before an attached delivery callback: %d", bootstrap.readyCalls)
	}
	if begin, end, success, active, probed := sessions.bootstrapSnapshot(); begin != 1 || end != 1 || success != 0 || active != 0 || probed {
		t.Fatalf("bootstrap after missing registry = begin:%d end:%d success:%d active:%d probed:%v", begin, end, success, active, probed)
	}
}

// TestDispatchSkipsReceivesUpdatesForInvokeWithoutUpdates verifies that a request
// wrapped in invokeWithoutUpdates (media/temp connections) does not mark the
// session as an updates recipient.
func TestDispatchSkipsReceivesUpdatesForInvokeWithoutUpdates(t *testing.T) {
	sessions := &captureSessions{}
	ctx := dispatchForReceivesUpdates(t, sessions, true, true)
	postresponse.Run(ctx)
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.receives {
		t.Fatal("invokeWithoutUpdates-wrapped RPC must not mark receivesUpdates")
	}
}

type captureBootstrapReadyStore struct {
	*memory.BootstrapUpdateJobStore
	readyCalls int
	readyErr   error
}

func (s *captureBootstrapReadyStore) MarkReadyForSession(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64) (int, error) {
	s.readyCalls++
	if s.readyErr != nil {
		err := s.readyErr
		s.readyErr = nil
		return 0, err
	}
	return s.BootstrapUpdateJobStore.MarkReadyForSession(ctx, userID, authKeyID, sessionID)
}

func TestBootstrapReadinessProbeIsOneShotAndRetriesFailure(t *testing.T) {
	const userID int64 = 1000000199
	rawAuthKeyID := [8]byte{19}
	businessAuthKeyID := [8]byte{29}
	const sessionID int64 = 199
	sessions := &activationCaptureSessions{captureSessions: &captureSessions{}}
	bootstrap := &captureBootstrapReadyStore{
		BootstrapUpdateJobStore: memory.NewBootstrapUpdateJobStore(),
		readyErr:                errors.New("temporary bootstrap lookup failure"),
	}
	r := New(Config{}, Deps{Sessions: sessions, BootstrapUpdates: bootstrap}, zaptest.NewLogger(t), clock.System)

	baseline := func() {
		ctx := postresponse.WithCallbacks(context.Background())
		ctx = WithAuthKeyID(ctx, businessAuthKeyID)
		ctx = WithRawAuthKeyID(ctx, rawAuthKeyID)
		ctx = WithSessionID(ctx, sessionID)
		ctx = WithUserID(ctx, userID)
		r.stageUpdatesBaselineAfterDelivery(ctx, userID, nil, 0, nil, true)
		postresponse.Run(ctx)
	}

	baseline() // authoritative query fails: release the physical-generation claim
	baseline() // successful zero-row result: complete the one-shot
	baseline() // no third store call on the same physical generation

	if bootstrap.readyCalls != 2 {
		t.Fatalf("bootstrap readiness calls = %d, want failure + one successful retry", bootstrap.readyCalls)
	}
	begin, end, success, active, probed := sessions.bootstrapSnapshot()
	if begin != 3 || end != 2 || success != 1 || active != 0 || !probed {
		t.Fatalf("bootstrap probe = begin:%d end:%d success:%d active:%d probed:%v", begin, end, success, active, probed)
	}
}

func TestInvokeWithoutUpdatesBaselineCommitsResultAndSecretEventsWithoutSubscribing(t *testing.T) {
	const userID int64 = 1000000201
	authKeyID := [8]byte{21}
	deviceKey := businessAuthKeyInt64(authKeyID)
	queue := memory.NewEncryptedQueueStore()
	secret := appsecret.NewService(memory.NewSecretChatStore(), queue)
	eventID, err := queue.AppendStateEvent(context.Background(), domain.EncryptedStateEvent{
		TargetUserID: userID,
		ChatID:       77,
		Type:         domain.EncryptedStateEventRead,
		MaxDate:      1700000200,
		Date:         1700000201,
	})
	if err != nil {
		t.Fatalf("append state event: %v", err)
	}
	sessions := &captureSessions{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 4, Date: 1700000201}}
	bootstrap := &captureBootstrapReadyStore{BootstrapUpdateJobStore: memory.NewBootstrapUpdateJobStore()}
	r := New(Config{}, Deps{
		Sessions: sessions, Updates: updates, SecretChats: secret, BootstrapUpdates: bootstrap,
	}, zaptest.NewLogger(t), clock.System)

	var inner bin.Buffer
	if err := (&tg.UpdatesGetDifferenceRequest{Pts: 4, Date: 1700000201}).Encode(&inner); err != nil {
		t.Fatalf("encode getDifference: %v", err)
	}
	var wrapped bin.Buffer
	wrapped.PutID(tg.InvokeWithoutUpdatesRequestTypeID)
	wrapped.Put(inner.Raw())
	ctx := postresponse.WithCallbacks(WithAuthKeyID(WithSessionID(WithUserID(context.Background(), userID), 202), authKeyID))
	if _, err := r.Dispatch(ctx, authKeyID, 202, &wrapped); err != nil {
		t.Fatalf("dispatch wrapped baseline: %v", err)
	}
	if updates.commitCalls != 0 || sessions.snapshot().receivesCalls != 0 {
		t.Fatalf("pre-delivery effects = commits:%d ready_calls:%d", updates.commitCalls, sessions.snapshot().receivesCalls)
	}
	pending, err := queue.ListUndeliveredStateEvents(context.Background(), userID, deviceKey, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != eventID {
		t.Fatalf("pending before delivery = %+v err=%v", pending, err)
	}

	postresponse.Run(ctx)
	if updates.commitCalls != 1 || updates.committedState.Pts != 4 {
		t.Fatalf("delivered cursor commit = calls:%d state:%+v", updates.commitCalls, updates.committedState)
	}
	if got := sessions.snapshot(); got.receives || got.receivesCalls != 0 {
		t.Fatalf("invokeWithoutUpdates subscribed session: receives=%v calls=%d", got.receives, got.receivesCalls)
	}
	if bootstrap.readyCalls != 0 {
		t.Fatalf("invokeWithoutUpdates released bootstrap %d times", bootstrap.readyCalls)
	}
	pending, err = queue.ListUndeliveredStateEvents(context.Background(), userID, deviceKey, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("secret events after delivered wrapped baseline = %+v err=%v", pending, err)
	}
}

// TestDispatchSkipsReceivesUpdatesWhenLoggedOut verifies that an RPC from a
// not-logged-in connection does not set the flag.
func TestDispatchSkipsReceivesUpdatesWhenLoggedOut(t *testing.T) {
	sessions := &captureSessions{}
	ctx := dispatchForReceivesUpdates(t, sessions, false, false)
	postresponse.Run(ctx)
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.receives {
		t.Fatal("RPC without bound user must not mark receivesUpdates")
	}
}

type fifoFlushCaptureSessions struct {
	*captureSessions
	flushMu sync.Mutex
	pending []int
	flushed []int
}

func (s *fifoFlushCaptureSessions) SetReceivesUpdatesForAuthKey(rawAuthKeyID [8]byte, sessionID int64, receives bool) {
	if receives {
		s.flushMu.Lock()
		s.flushed = append(s.flushed, s.pending...)
		s.pending = nil
		s.flushMu.Unlock()
	}
	s.captureSessions.SetReceivesUpdatesForAuthKey(rawAuthKeyID, sessionID, receives)
}

func (s *fifoFlushCaptureSessions) flushedSnapshot() []int {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	return append([]int(nil), s.flushed...)
}

// TestDispatchDefersMembershipAndFIFOFlushUntilPostResponse pins the complete
// readiness barrier: channel membership and pending updates remain untouched
// while the rpc_result is only prepared, then the delivery hook rebuilds
// membership before SetReceivesUpdates drains the original FIFO order.
func TestDispatchDefersMembershipAndFIFOFlushUntilPostResponse(t *testing.T) {
	const (
		userID    = int64(1000000111)
		sessionID = int64(87)
	)
	channelSvc := appchannels.NewService(memory.NewChannelStore())
	created, err := channelSvc.CreateMegagroupFromCreateChat(context.Background(), userID, domain.CreateChannelRequest{
		Title: "delivery barrier",
		Date:  1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sessions := &fifoFlushCaptureSessions{
		captureSessions: &captureSessions{},
		pending:         []int{11, 22, 33},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Sessions: sessions,
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
		t.Fatalf("encode help.getConfig: %v", err)
	}
	ctx := postresponse.WithCallbacks(WithUserID(context.Background(), userID))
	if _, err := r.Dispatch(ctx, [8]byte{7}, sessionID, &in); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := sessions.flushedSnapshot(); len(got) != 0 {
		t.Fatalf("pending flushed before result delivery: %v", got)
	}
	if got := sessions.onlineChannelMemberIDs(created.Channel.ID); len(got) != 0 {
		t.Fatalf("membership synced before result delivery: %v", got)
	}
	if sessions.snapshot().receives {
		t.Fatal("session ready before result delivery")
	}

	postresponse.Run(ctx)
	if got, want := sessions.flushedSnapshot(), []int{11, 22, 33}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FIFO flush after result delivery = %v, want %v", got, want)
	}
	if got := sessions.onlineChannelMemberIDs(created.Channel.ID); !reflect.DeepEqual(got, []int64{userID}) {
		t.Fatalf("membership after result delivery = %v, want [%d]", got, userID)
	}
	if !sessions.snapshot().receives {
		t.Fatal("session not ready after result delivery")
	}
}

type failingCurrentStateUpdates struct{ *captureUpdates }

func (s *failingCurrentStateUpdates) CurrentState(context.Context, int64) (domain.UpdateState, error) {
	return domain.UpdateState{}, errors.New("current state failed")
}

func TestFailedRPCDoesNotRegisterSessionReadyPostResponse(t *testing.T) {
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Sessions: sessions,
		Updates:  &failingCurrentStateUpdates{captureUpdates: &captureUpdates{}},
	}, zaptest.NewLogger(t), clock.System)
	var in bin.Buffer
	if err := (&tg.UpdatesGetStateRequest{}).Encode(&in); err != nil {
		t.Fatalf("encode updates.getState: %v", err)
	}
	ctx := postresponse.WithCallbacks(WithUserID(context.Background(), 1000000123))
	if _, err := r.Dispatch(ctx, [8]byte{8}, 91, &in); err == nil {
		t.Fatal("updates.getState unexpectedly succeeded")
	}
	postresponse.Run(ctx)
	if sessions.snapshot().receives {
		t.Fatal("failed RPC marked session ready")
	}
}
