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

// TestDispatchMarksSessionReceivesUpdates 验证已登录连接发出的裸 RPC（未包
// invokeWithoutUpdates）即视为 updates 接收声明。仅靠 updates.getState/getDifference
// 置位会漏掉热恢复重连的客户端：它不重建同步基线，置位永不发生时主动推送一直
// 暂存直至超时丢弃，表现为另一端消息不再实时同步。
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
}

// TestDispatchSkipsReceivesUpdatesForInvokeWithoutUpdates 验证 invokeWithoutUpdates
// 包装的请求（media/temp 连接）不会把该 session 标记为 updates 接收者。
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
}

func (s *captureBootstrapReadyStore) MarkReadyForSession(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64) (int, error) {
	s.readyCalls++
	return s.BootstrapUpdateJobStore.MarkReadyForSession(ctx, userID, authKeyID, sessionID)
}

func TestInvokeWithoutUpdatesBaselineCommitsResultAndSecretEventsWithoutSubscribing(t *testing.T) {
	const userID int64 = 1000000201
	authKeyID := [8]byte{21}
	deviceKey := businessAuthKeyInt64(authKeyID)
	queue := memory.NewEncryptedQueueStore()
	secret := appsecret.NewService(memory.NewSecretChatStore(), queue, &seqSecretChatIDAllocator{})
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

// TestDispatchSkipsReceivesUpdatesWhenLoggedOut 验证未登录连接的 RPC 不置位。
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
