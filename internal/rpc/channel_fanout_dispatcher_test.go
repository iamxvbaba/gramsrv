package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func fanoutTestJob(recipients []int64, originUser, originSession int64, built map[int64]bool) channelFanoutJob {
	return channelFanoutJob{
		scope:           channelFanoutMembers,
		originUserID:    originUser,
		channelID:       1001,
		pts:             5,
		recipients:      recipients,
		originSessionID: originSession,
		build: func(_ context.Context, viewerUserID int64) *tg.Updates {
			if built != nil {
				built[viewerUserID] = true
			}
			return &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateChannelTooLong{ChannelID: 1001}}, Date: 1}
		},
	}
}

func fanoutHasID(ids []int64, want int64) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestChannelFanoutDispatcherSyncFallback：dispatcher 未启动时 Enqueue 同步执行——
// 保持测试/未装配场景行为不变，recipients 立即被推送、发起 session 作为 exclude 透传。
// deps.Channels=nil 时 channelFanoutRecipients 直接返回 explicit recipients。
func TestChannelFanoutDispatcherSyncFallback(t *testing.T) {
	cs := &captureSessions{}
	r := New(Config{}, Deps{Sessions: cs}, zaptest.NewLogger(t), clock.System)
	built := map[int64]bool{}
	r.channelFanout.Enqueue(context.Background(), fanoutTestJob([]int64{2001, 2002}, 0, 99, built))

	pushed := cs.pushedUserIDs()
	if len(pushed) != 2 || !fanoutHasID(pushed, 2001) || !fanoutHasID(pushed, 2002) {
		t.Fatalf("sync fallback pushed = %v, want [2001 2002]", pushed)
	}
	if got := cs.snapshot().sessionID; got != 99 {
		t.Fatalf("exclude session = %d, want 99 (origin session passed explicitly, not via request ctx)", got)
	}
	if !built[2001] || !built[2002] {
		t.Fatalf("build not invoked per viewer: %v", built)
	}
}

// TestChannelFanoutDispatcherDeliversAsync：dispatcher 启动后 Enqueue 异步投递，
// recipients 最终被 worker 推送（不阻塞 Enqueue 调用方）。
func TestChannelFanoutDispatcherDeliversAsync(t *testing.T) {
	cs := &captureSessions{}
	r := New(Config{}, Deps{Sessions: cs}, zaptest.NewLogger(t), clock.System)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.RunChannelFanout(ctx)
	for i := 0; i < 200 && !r.channelFanout.started.Load(); i++ {
		time.Sleep(time.Millisecond)
	}
	if !r.channelFanout.started.Load() {
		t.Fatal("dispatcher did not start")
	}

	// built map 跨 goroutine，只断言 mutex 保护的 pushedUserIDs，不读 built。
	r.channelFanout.Enqueue(context.Background(), fanoutTestJob([]int64{3001}, 0, 7, nil))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fanoutHasID(cs.pushedUserIDs(), 3001) {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !fanoutHasID(cs.pushedUserIDs(), 3001) {
		t.Fatalf("async fan-out did not deliver to 3001: %v", cs.pushedUserIDs())
	}
	if got := cs.snapshot().sessionID; got != 7 {
		t.Fatalf("exclude session = %d, want 7 (origin carried into job, not lost on bg ctx)", got)
	}
}

// TestChannelFanoutDispatcherInvokesPrefetch：worker 在逐 viewer build 之前调用一次 prefetch，
// 且传入「解析后的 recipients + 兜底 origin」——这是 fan-out 跨 viewer 投影预热（O(owner)）的入口。
func TestChannelFanoutDispatcherInvokesPrefetch(t *testing.T) {
	cs := &captureSessions{}
	r := New(Config{}, Deps{Sessions: cs}, zaptest.NewLogger(t), clock.System)

	var gotViewers []int64
	job := fanoutTestJob([]int64{2001, 2002}, 5, 99, nil)
	job.prefetch = func(_ context.Context, viewers []int64) {
		gotViewers = append([]int64(nil), viewers...)
	}
	// deps.Channels=nil → channelFanoutRecipients 返回 explicit recipients=[2001 2002]；origin=5 兜底追加。
	r.channelFanout.Enqueue(context.Background(), job)

	want := map[int64]bool{2001: true, 2002: true, 5: true}
	if len(gotViewers) != len(want) {
		t.Fatalf("prefetch viewers = %v, want recipients+origin %v", gotViewers, want)
	}
	for _, v := range gotViewers {
		if !want[v] {
			t.Fatalf("prefetch viewers = %v, unexpected %d (want recipients+origin)", gotViewers, v)
		}
	}
}

// editFanoutTestResult 构造一条覆盖两容器的 EditChannelMessageResult：主容器(Event/Message)带
// sender A + reply B，服务消息容器(ServiceEvent/ServiceMessage)带 sender C + Action.UserIDs=[D]。
func editFanoutTestResult(eventPts, servicePts int) domain.EditChannelMessageResult {
	res := domain.EditChannelMessageResult{
		Channel:    domain.Channel{ID: 1001},
		Recipients: []int64{3001, 3002},
	}
	res.Event = domain.ChannelUpdateEvent{Pts: eventPts, SenderUserID: 2001, Message: domain.ChannelMessage{ChannelID: 1001, SenderUserID: 2001}}
	res.Message = domain.ChannelMessage{ChannelID: 1001, SenderUserID: 2001, ReplyTo: &domain.MessageReply{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2002}}}
	res.ServiceEvent = domain.ChannelUpdateEvent{Pts: servicePts, SenderUserID: 2003, Message: domain.ChannelMessage{ChannelID: 1001, SenderUserID: 2003}}
	res.ServiceMessage = domain.ChannelMessage{ChannelID: 1001, SenderUserID: 2003, Action: &domain.ChannelMessageAction{Type: domain.ChannelActionTodoCompletions, UserIDs: []int64{2004}}}
	return res
}

func ownerIDSet(ids []int64) map[int64]bool {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// TestChannelEditMessageFanoutOwnerIDsCoversBothContainers：编辑预热的 owner-id 收集必须并集两个
// 容器（主消息 + 服务消息），否则服务消息的 sender/Action.UserIDs 漏出缓存预热（edit 相对 send
// 路径唯一新增的等价面，见 editVerify）。
func TestChannelEditMessageFanoutOwnerIDsCoversBothContainers(t *testing.T) {
	got := ownerIDSet(channelEditMessageFanoutOwnerIDs(editFanoutTestResult(5, 6)))
	for _, want := range []int64{2001, 2002, 2003, 2004} {
		if !got[want] {
			t.Fatalf("owner ids %v missing %d (both containers must be unioned)", got, want)
		}
	}
}

// TestChannelEditMessageFanoutOwnerIDsGating：owner-id 收集必须严格镜像 builder 的 pts 门控——
// ServiceEvent.Pts==0 时不收服务消息容器；Event.Pts==0 时不收主容器。保证预热集与 build 下发的
// Users 集恰好一致（多收无害但破坏等价测试紧致性）。
func TestChannelEditMessageFanoutOwnerIDsGating(t *testing.T) {
	// 仅主容器（服务消息 pts=0）。
	noService := ownerIDSet(channelEditMessageFanoutOwnerIDs(editFanoutTestResult(5, 0)))
	if !noService[2001] || !noService[2002] {
		t.Fatalf("event-only owner ids %v should contain 2001/2002", noService)
	}
	if noService[2003] || noService[2004] {
		t.Fatalf("event-only owner ids %v must not contain service-container ids 2003/2004", noService)
	}
	// 两容器都无 pts → 空。
	if ids := channelEditMessageFanoutOwnerIDs(editFanoutTestResult(0, 0)); len(ids) != 0 {
		t.Fatalf("no-pts owner ids = %v, want empty", ids)
	}
}

// prefetchRecordingUsersService 在 mapUsersService 基础上实现 BatchViewerUsersResolver 并记录
// ByIDsForViewers 收到的 (viewers, ownerIDs)，用于断言 edit fan-out 用正确 owner 集预热。
type prefetchRecordingUsersService struct {
	mapUsersService
	mu            sync.Mutex
	gotViewers    []int64
	gotOwnerIDs   []int64
	forViewerCall int
}

func (s *prefetchRecordingUsersService) ByIDsForViewers(_ context.Context, viewerUserIDs, userIDs []int64) (map[int64][]domain.User, error) {
	s.mu.Lock()
	s.forViewerCall++
	s.gotViewers = append([]int64(nil), viewerUserIDs...)
	s.gotOwnerIDs = append([]int64(nil), userIDs...)
	s.mu.Unlock()
	out := make(map[int64][]domain.User, len(viewerUserIDs))
	for _, v := range viewerUserIDs {
		out[v] = nil
	}
	return out, nil
}

// TestChannelEditMessageFanoutInvokesPrefetch：enqueueChannelEditMessageFanout 在逐 viewer build
// 前用「channelEditMessageFanoutOwnerIDs(res) + recipients+origin」预热（dispatcher 未启动→同步
// 回退，prefetch 同步执行）。锁定 edit 路径接入了 O(owner) 预热而非逐 viewer 投影。
func TestChannelEditMessageFanoutInvokesPrefetch(t *testing.T) {
	users := &prefetchRecordingUsersService{mapUsersService: mapUsersService{users: map[int64]domain.User{}}}
	cs := &captureSessions{}
	r := New(Config{}, Deps{Sessions: cs, Users: users}, zaptest.NewLogger(t), clock.System)

	res := editFanoutTestResult(5, 6)
	r.enqueueChannelEditMessageFanout(context.Background(), 5, res)

	if users.forViewerCall != 1 {
		t.Fatalf("ByIDsForViewers called %d times, want 1 (prefetch must run once before per-viewer build)", users.forViewerCall)
	}
	gotViewers := ownerIDSet(users.gotViewers)
	for _, want := range []int64{3001, 3002, 5} {
		if !gotViewers[want] {
			t.Fatalf("prefetch viewers %v missing %d (recipients+origin)", users.gotViewers, want)
		}
	}
	gotOwners := ownerIDSet(users.gotOwnerIDs)
	for _, want := range []int64{2001, 2002, 2003, 2004} {
		if !gotOwners[want] {
			t.Fatalf("prefetch owner ids %v missing %d (must equal channelEditMessageFanoutOwnerIDs)", users.gotOwnerIDs, want)
		}
	}
}

// nudgeSessions 在 captureSessions 基础上实现 ChannelNudgeProvider 并按 user 记录最近一次推送，
// 用于断言 >cap 在线成员收到带 pts 的 UpdateChannelTooLong nudge。
type nudgeSessions struct {
	*captureSessions
	online []int64
	mu     sync.Mutex
	byUser map[int64]bin.Encoder
}

func newNudgeSessions(online []int64) *nudgeSessions {
	return &nudgeSessions{captureSessions: &captureSessions{}, online: online, byUser: map[int64]bin.Encoder{}}
}

func (s *nudgeSessions) PushToUserExceptSession(ctx context.Context, userID, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	s.mu.Lock()
	s.byUser[userID] = msg
	s.mu.Unlock()
	return s.captureSessions.PushToUserExceptSession(ctx, userID, excludeSessionID, t, msg)
}

func (s *nudgeSessions) OnlineChannelMemberUserIDsExcluding(_ int64, exclude map[int64]struct{}, limit int) []int64 {
	out := make([]int64, 0, len(s.online))
	for _, id := range s.online {
		if _, ok := exclude[id]; ok {
			continue
		}
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *nudgeSessions) msgFor(userID int64) bin.Encoder {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byUser[userID]
}

// TestChannelFanoutDispatcherNudgesBeyondCapMembers（P0-8）：完整 payload 投递给 cap 内
// recipients 后，cap 外在线成员收到带 pts 的 UpdateChannelTooLong nudge；cap 内成员不重复 nudge。
func TestChannelFanoutDispatcherNudgesBeyondCapMembers(t *testing.T) {
	cs := newNudgeSessions([]int64{2001, 2002, 2003})
	r := New(Config{}, Deps{Sessions: cs}, zaptest.NewLogger(t), clock.System)
	// deps.Channels=nil → channelFanoutRecipients 返回 explicit recipients=[2001]（收完整 payload）。
	// 2002/2003 是 cap 外在线成员（OnlineChannelMemberUserIDsExcluding 排除 2001 后返回）。
	r.channelFanout.Enqueue(context.Background(), fanoutTestJob([]int64{2001}, 0, 99, nil))

	pushed := cs.pushedUserIDs()
	for _, want := range []int64{2001, 2002, 2003} {
		if !fanoutHasID(pushed, want) {
			t.Fatalf("user %d not pushed: %v", want, pushed)
		}
	}
	// 2002/2003 必须是带 pts 的 UpdateChannelTooLong（DrKLO 对不带 pts 的 tooLong 不触发 difference）。
	for _, uid := range []int64{2002, 2003} {
		ups, ok := cs.msgFor(uid).(*tg.Updates)
		if !ok || len(ups.Updates) != 1 {
			t.Fatalf("nudge to %d not single-update *tg.Updates: %#v", uid, cs.msgFor(uid))
		}
		tl, ok := ups.Updates[0].(*tg.UpdateChannelTooLong)
		if !ok {
			t.Fatalf("nudge to %d not UpdateChannelTooLong: %#v", uid, ups.Updates[0])
		}
		if p, ok := tl.GetPts(); !ok || p != 5 {
			t.Fatalf("nudge to %d pts=%d ok=%v, want 5 (must carry pts)", uid, p, ok)
		}
	}
}

// TestChannelFanoutDispatcherQueueFullNudgesOnlineMembers：分片队列满时不再只丢完整 payload
// 并等待下一条 pts，而是立刻发 UpdateChannelTooLong{pts} 给在线成员，触发 getChannelDifference。
func TestChannelFanoutDispatcherQueueFullNudgesOnlineMembers(t *testing.T) {
	cs := newNudgeSessions([]int64{2001, 2002})
	r := New(Config{}, Deps{Sessions: cs}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, 1, 1)
	r.channelFanout = d
	d.started.Store(true)
	d.shards[0] <- fanoutTestJob([]int64{9999}, 0, 0, nil)

	built := map[int64]bool{}
	d.Enqueue(context.Background(), fanoutTestJob([]int64{2001}, 0, 99, built))

	if got := d.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d, want 1", got)
	}
	if len(built) != 0 {
		t.Fatalf("full payload builder ran on queue-full overflow: %v", built)
	}
	for _, uid := range []int64{2001, 2002} {
		ups, ok := cs.msgFor(uid).(*tg.Updates)
		if !ok || len(ups.Updates) != 1 {
			t.Fatalf("overflow nudge to %d not single-update *tg.Updates: %#v", uid, cs.msgFor(uid))
		}
		tl, ok := ups.Updates[0].(*tg.UpdateChannelTooLong)
		if !ok {
			t.Fatalf("overflow nudge to %d not UpdateChannelTooLong: %#v", uid, ups.Updates[0])
		}
		if p, ok := tl.GetPts(); !ok || p != 5 {
			t.Fatalf("overflow nudge to %d pts=%d ok=%v, want 5", uid, p, ok)
		}
	}
}

// TestChannelEditMessageFanoutNudgePtsUsesMaxContainer：edit 可只产服务消息容器（Event.Pts==0、
// ServiceEvent.Pts!=0，如纯 todo 完成）。此时 >cap 在线成员的 nudge 必须带 ServiceEvent.Pts（两容器
// 较大值），否则用 Event.Pts==0 会被 job.pts>0 门控吞掉 nudge、beyond-cap 成员错过 getChannelDifference。
func TestChannelEditMessageFanoutNudgePtsUsesMaxContainer(t *testing.T) {
	cs := newNudgeSessions([]int64{3001, 4001}) // 4001 是 cap 外在线成员（不在 recipients）
	r := New(Config{}, Deps{Sessions: cs, Users: mapUsersService{users: map[int64]domain.User{}}}, zaptest.NewLogger(t), clock.System)

	// 仅服务消息容器有 pts：Event.Pts=0, ServiceEvent.Pts=11。deps.Channels=nil → recipients=[3001 3002]。
	res := editFanoutTestResult(0, 11)
	r.enqueueChannelEditMessageFanout(context.Background(), 0, res)

	ups, ok := cs.msgFor(4001).(*tg.Updates)
	if !ok || len(ups.Updates) != 1 {
		t.Fatalf("nudge to 4001 not single-update *tg.Updates: %#v", cs.msgFor(4001))
	}
	tl, ok := ups.Updates[0].(*tg.UpdateChannelTooLong)
	if !ok {
		t.Fatalf("nudge to 4001 not UpdateChannelTooLong: %#v", ups.Updates[0])
	}
	if p, ok := tl.GetPts(); !ok || p != 11 {
		t.Fatalf("nudge pts=%d ok=%v, want 11 (max(Event=0, Service=11))", p, ok)
	}
}
