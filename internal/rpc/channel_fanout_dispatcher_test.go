package rpc

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
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

type recoveryFanoutSessions struct {
	SessionBinder

	onlineChannels []int64
	pushStarted    chan struct{}
	pushRelease    <-chan struct{}
	startOnce      sync.Once

	mu      sync.Mutex
	nudges  map[int64][]int
	pushErr int
}

func newRecoveryFanoutSessions(onlineChannels []int64, release <-chan struct{}) *recoveryFanoutSessions {
	return &recoveryFanoutSessions{
		onlineChannels: append([]int64(nil), onlineChannels...),
		pushStarted:    make(chan struct{}),
		pushRelease:    release,
		nudges:         make(map[int64][]int),
	}
}

func (s *recoveryFanoutSessions) PushToUserExceptAuthKeySession(ctx context.Context, _ int64, _ [8]byte, _ int64, _ proto.MessageType, msg tg.UpdatesClass) (int, error) {
	s.startOnce.Do(func() { close(s.pushStarted) })
	if s.pushRelease != nil {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.pushErr++
			s.mu.Unlock()
			return 0, ctx.Err()
		case <-s.pushRelease:
		}
	}
	updates, ok := msg.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		return 1, nil
	}
	nudge, ok := updates.Updates[0].(*tg.UpdateChannelTooLong)
	if !ok {
		return 1, nil
	}
	pts, _ := nudge.GetPts()
	s.mu.Lock()
	s.nudges[nudge.ChannelID] = append(s.nudges[nudge.ChannelID], pts)
	s.mu.Unlock()
	return 1, nil
}

func (s *recoveryFanoutSessions) OnlineChannelMemberUserIDsExcluding(_ int64, _ map[int64]struct{}, _ int) []int64 {
	return []int64{42}
}

func (s *recoveryFanoutSessions) OnlineChannelIDsAfter(afterChannelID int64, limit int) []int64 {
	out := make([]int64, 0, limit)
	for _, channelID := range s.onlineChannels {
		if channelID <= afterChannelID {
			continue
		}
		out = append(out, channelID)
		if len(out) == limit {
			break
		}
	}
	return out
}

func (s *recoveryFanoutSessions) OnlineChannelIDsSnapshot() []int64 {
	return append([]int64(nil), s.onlineChannels...)
}

func (s *recoveryFanoutSessions) nudgePts(channelID int64) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.nudges[channelID]...)
}

func (s *recoveryFanoutSessions) pushErrors() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushErr
}

type recoveryFanoutChannels struct {
	ChannelsService

	mu          sync.Mutex
	pts         map[int64]int
	calls       int
	failCalls   int
	firstCalled chan struct{}
	firstOnce   sync.Once
	release     <-chan struct{}
}

func (s *recoveryFanoutChannels) MaxChannelPts(ctx context.Context, channelID int64) (int, error) {
	pts, err := s.MaxChannelPtsBatch(ctx, []int64{channelID})
	return pts[channelID], err
}

func (s *recoveryFanoutChannels) MaxChannelPtsBatch(ctx context.Context, channelIDs []int64) (map[int64]int, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	if call <= s.failCalls {
		s.mu.Unlock()
		return nil, fmt.Errorf("injected max pts failure %d", call)
	}
	pts := make(map[int64]int, len(channelIDs))
	for _, channelID := range channelIDs {
		if value, ok := s.pts[channelID]; ok {
			pts[channelID] = value
		}
	}
	release := s.release
	s.mu.Unlock()
	if s.firstCalled != nil {
		s.firstOnce.Do(func() { close(s.firstCalled) })
	}
	if release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
	}
	return pts, nil
}

func (s *recoveryFanoutChannels) setPts(channelID int64, pts int) {
	s.mu.Lock()
	if s.pts == nil {
		s.pts = make(map[int64]int)
	}
	s.pts[channelID] = pts
	s.mu.Unlock()
}

func (s *recoveryFanoutChannels) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
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

// overflowNudgeSessions 为 queue-full 回归按 channel 提供不同在线成员，并只记录
// UpdateChannelTooLong。正常 FIFO payload 仍委托 captureSessions 记录，但不会污染 nudge 断言。
type overflowNudgeSessions struct {
	*captureSessions
	onlineByChannel map[int64][]int64

	mu     sync.Mutex
	nudges map[int64][]int
	order  []string
}

func newOverflowNudgeSessions(onlineByChannel map[int64][]int64) *overflowNudgeSessions {
	return &overflowNudgeSessions{
		captureSessions: &captureSessions{},
		onlineByChannel: onlineByChannel,
		nudges:          make(map[int64][]int),
	}
}

func (s *overflowNudgeSessions) PushToUserExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, typ proto.MessageType, msg tg.UpdatesClass) (int, error) {
	if updates, ok := msg.(*tg.Updates); ok && len(updates.Updates) == 1 {
		if nudge, ok := updates.Updates[0].(*tg.UpdateChannelTooLong); ok {
			pts, _ := nudge.GetPts()
			s.mu.Lock()
			s.nudges[nudge.ChannelID] = append(s.nudges[nudge.ChannelID], pts)
			s.order = append(s.order, fmt.Sprintf("nudge:%d:%d", nudge.ChannelID, pts))
			s.mu.Unlock()
		}
	}
	return s.captureSessions.PushToUserExceptAuthKeySession(ctx, userID, excludeAuthKeyID, excludeSessionID, typ, msg)
}

func (s *overflowNudgeSessions) OnlineChannelMemberUserIDsExcluding(channelID int64, exclude map[int64]struct{}, limit int) []int64 {
	online := s.onlineByChannel[channelID]
	out := make([]int64, 0, len(online))
	for _, id := range online {
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

func (s *overflowNudgeSessions) recordOrder(event string) {
	s.mu.Lock()
	s.order = append(s.order, event)
	s.mu.Unlock()
}

func (s *overflowNudgeSessions) snapshot() (map[int64][]int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nudges := make(map[int64][]int, len(s.nudges))
	for channelID, pts := range s.nudges {
		nudges[channelID] = append([]int(nil), pts...)
	}
	return nudges, append([]string(nil), s.order...)
}

func newNudgeSessions(online []int64) *nudgeSessions {
	return &nudgeSessions{captureSessions: &captureSessions{}, online: online, byUser: map[int64]bin.Encoder{}}
}

func (s *nudgeSessions) PushToUserExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass) (int, error) {
	s.mu.Lock()
	s.byUser[userID] = msg
	s.mu.Unlock()
	return s.captureSessions.PushToUserExceptAuthKeySession(ctx, userID, excludeAuthKeyID, excludeSessionID, t, msg)
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

// TestChannelFanoutDispatcherOverflowCoalescesHighestPtsAndDrains 验证 queue full 不再静默
// 丢掉恢复触发：正常 payload 仍按 FIFO 执行；同 channel 多次 overflow 只发最高 pts nudge；
// 另一个落在同 shard 的 channel 也最终得到 nudge，且不需要后续再 Enqueue 才唤醒 worker。
func TestChannelFanoutDispatcherOverflowCoalescesHighestPtsAndDrains(t *testing.T) {
	const (
		hotChannel   = int64(1001)
		otherChannel = int64(2001)
		hotUser      = int64(10001)
		otherUser    = int64(20001)
	)
	sessions := newOverflowNudgeSessions(map[int64][]int64{
		hotChannel:   {hotUser},
		otherChannel: {otherUser},
	})
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	// 单 shard + 单 buffer 让测试确定地产生：1 条执行中、1 条正常 FIFO pending、其余 overflow。
	r.channelFanout = newChannelFanoutDispatcher(r, 1, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.RunChannelFanout(ctx)
		close(done)
	}()
	for i := 0; i < 200 && !r.channelFanout.started.Load(); i++ {
		time.Sleep(time.Millisecond)
	}
	if !r.channelFanout.started.Load() {
		cancel()
		<-done
		t.Fatal("dispatcher did not start")
	}

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	overflowBuildCalled := make(chan struct{}, 1)
	job := func(channelID int64, pts int, recipients []int64, build channelFanoutBuilder) channelFanoutJob {
		return channelFanoutJob{
			scope:      channelFanoutMembers,
			channelID:  channelID,
			pts:        pts,
			recipients: recipients,
			build:      build,
		}
	}
	payload := func(channelID int64) *tg.Updates {
		return &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateChannel{ChannelID: channelID}},
			Users:   []tg.UserClass{},
			Chats:   []tg.ChatClass{},
			Date:    1,
		}
	}

	r.channelFanout.Enqueue(context.Background(), job(hotChannel, 1, []int64{hotUser}, func(context.Context, int64) *tg.Updates {
		close(firstStarted)
		<-releaseFirst
		sessions.recordOrder("payload:1001:1")
		return payload(hotChannel)
	}))
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("first FIFO payload did not start")
	}

	// 这条占满唯一正常 buffer；overflow barrier 必须等它完成后才允许发 nudge。
	r.channelFanout.Enqueue(context.Background(), job(hotChannel, 2, []int64{hotUser}, func(context.Context, int64) *tg.Updates {
		sessions.recordOrder("payload:1001:2")
		return payload(hotChannel)
	}))
	overflowBuild := func(context.Context, int64) *tg.Updates {
		select {
		case overflowBuildCalled <- struct{}{}:
		default:
		}
		return nil
	}
	// 热点 channel 连续灌入只占一个 overflow mailbox 项，最终仅保留 pts=20。
	for pts := 3; pts <= 20; pts++ {
		r.channelFanout.Enqueue(context.Background(), job(hotChannel, pts, []int64{hotUser}, overflowBuild))
	}
	// 同 shard 的其它 channel 也必须最终 drain，不能被热点 channel 永久饿死。
	r.channelFanout.Enqueue(context.Background(), job(otherChannel, 7, []int64{otherUser}, overflowBuild))
	if got, want := r.channelFanout.dropped.Load(), int64(19); got != want {
		cancel()
		<-done
		t.Fatalf("coalesced overflow jobs = %d, want %d", got, want)
	}

	close(releaseFirst)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		nudges, _ := sessions.snapshot()
		if len(nudges[hotChannel]) == 1 && len(nudges[otherChannel]) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not stop")
	}

	nudges, order := sessions.snapshot()
	if got := nudges[hotChannel]; len(got) != 1 || got[0] != 20 {
		t.Fatalf("hot channel nudges = %v, want one highest-pts nudge [20]", got)
	}
	if got := nudges[otherChannel]; len(got) != 1 || got[0] != 7 {
		t.Fatalf("other channel nudges = %v, want [7] (must not be starved by hot channel)", got)
	}
	select {
	case <-overflowBuildCalled:
		t.Fatal("overflow job build was called; queue-full path must be nudge-only")
	default:
	}
	if len(order) != 4 {
		t.Fatalf("delivery order = %v, want two payloads + two nudges", order)
	}
	if order[0] != "payload:1001:1" || order[1] != "payload:1001:2" {
		t.Fatalf("delivery order = %v, normal payload FIFO/barrier violated", order)
	}
	wantNudges := map[string]bool{"nudge:1001:20": true, "nudge:2001:7": true}
	if !wantNudges[order[2]] || !wantNudges[order[3]] || order[2] == order[3] {
		t.Fatalf("delivery order = %v, want both nudges after FIFO barrier (nudge pool may reorder channels)", order)
	}
}

func TestChannelFanoutQueueBudgetIsBoundedAndPreciselyReleased(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, 1, 1)
	d.maxQueuedJobs = 1
	d.maxQueuedBytes = channelFanoutMinRetainedBytes
	job := fanoutTestJob(nil, 0, 0, nil)
	job.retainedBytes = channelFanoutMinRetainedBytes
	if !d.reserveQueuedJob(job) {
		t.Fatal("first job reservation rejected")
	}
	if d.reserveQueuedJob(job) {
		t.Fatal("second job reservation exceeded global count/byte budget")
	}
	if jobs, bytes := d.queuedBudgetSnapshot(); jobs != 1 || bytes != channelFanoutMinRetainedBytes {
		t.Fatalf("budget = %d/%d, want 1/%d", jobs, bytes, channelFanoutMinRetainedBytes)
	}
	d.releaseQueuedJob(job)
	if jobs, bytes := d.queuedBudgetSnapshot(); jobs != 0 || bytes != 0 {
		t.Fatalf("released budget = %d/%d, want 0/0", jobs, bytes)
	}
}

func TestChannelFanoutOverflowCardinalityBackpressuresUntilSpace(t *testing.T) {
	shard := newChannelFanoutShard(1)
	shard.overflowLimit = 1
	if !shard.enqueueOverflow(1001, 5) {
		t.Fatal("first overflow watermark rejected")
	}
	stop := make(chan struct{})
	accepted := make(chan bool, 1)
	go func() { accepted <- shard.enqueueOverflowWait(context.Background(), 2001, 7, stop) }()
	select {
	case <-accepted:
		t.Fatal("second unique channel bypassed overflow cardinality bound")
	case <-time.After(20 * time.Millisecond):
	}
	channelID, pts, ok := shard.popOverflow()
	if !ok || channelID != 1001 || pts != 5 {
		t.Fatalf("first pop = %d/%d/%v, want 1001/5/true", channelID, pts, ok)
	}
	select {
	case ok := <-accepted:
		if !ok {
			t.Fatal("waiting overflow rejected after space became available")
		}
	case <-time.After(time.Second):
		t.Fatal("waiting overflow did not resume after space became available")
	}
	channelID, pts, ok = shard.popOverflow()
	if !ok || channelID != 2001 || pts != 7 {
		t.Fatalf("second pop = %d/%d/%v, want 2001/7/true", channelID, pts, ok)
	}
}

func TestChannelFanoutOverflowSpaceBroadcastWakesAllWaitersAfterBatchRelease(t *testing.T) {
	const waiters = 8
	shard := newChannelFanoutShard(1)
	shard.overflowLimit = waiters
	for i := range waiters {
		if !shard.enqueueOverflow(int64(1000+i), i+1) {
			t.Fatalf("initial overflow watermark %d rejected", i)
		}
	}

	results := make(chan bool, waiters)
	for i := range waiters {
		go func(i int) {
			results <- shard.enqueueOverflowWait(context.Background(), int64(2000+i), 100+i, make(chan struct{}))
		}(i)
	}

	// Wait until every goroutine has atomically observed the same full-mailbox
	// generation.  This makes the regression deterministic: a one-token space
	// notification can admit at most one waiter after the batch release below.
	waitDeadline := time.Now().Add(50 * time.Millisecond)
	for {
		shard.mu.Lock()
		waiting := shard.overflowWaiters
		shard.mu.Unlock()
		if waiting == waiters {
			break
		}
		if time.Now().After(waitDeadline) {
			t.Fatalf("overflow waiters = %d, want %d before release", waiting, waiters)
		}
		time.Sleep(time.Millisecond)
	}

	// Model a drain that releases several cardinality slots before any waiter can
	// run.  One generation broadcast is sufficient: waiters serialize under mu and
	// consume the eight real slots; no notification count is used as capacity.
	shard.mu.Lock()
	for channelID := range shard.overflow {
		delete(shard.overflow, channelID)
	}
	shard.overflowOrder = shard.overflowOrder[:0]
	shard.signalOverflowSpaceLocked()
	shard.mu.Unlock()

	for i := range waiters {
		select {
		case accepted := <-results:
			if !accepted {
				t.Fatalf("waiter %d timed out after batch space release", i)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %d remained blocked after batch space release", i)
		}
	}
	shard.mu.Lock()
	mailboxLen := len(shard.overflow)
	remainingWaiters := shard.overflowWaiters
	shard.mu.Unlock()
	if mailboxLen != waiters || remainingWaiters != 0 {
		t.Fatalf("post-release mailbox/waiters = %d/%d, want %d/0", mailboxLen, remainingWaiters, waiters)
	}
}

func TestChannelFanoutSameKeyOverflowKeepsFirstBarrierUnderContinuousPayload(t *testing.T) {
	const channelID = int64(1001)
	shard := newChannelFanoutShard(1)
	firstJob := channelFanoutJob{channelID: channelID, pts: 1}
	if !shard.enqueue(firstJob) {
		t.Fatal("first payload enqueue rejected")
	}
	if !shard.enqueueOverflow(channelID, 2) {
		t.Fatal("first overflow watermark rejected")
	}

	// Model a continuously saturated producer: as soon as the worker takes one payload, another
	// payload occupies the slot and a newer loss merges into the same overflow key. The recovery
	// nudge must be eligible after the original barrier, without waiting for the producer to stop.
	processed := <-shard.jobs
	shard.markProcessed(processed.queueSeq)
	if !shard.enqueue(channelFanoutJob{channelID: channelID, pts: 3}) {
		t.Fatal("replacement payload enqueue rejected")
	}
	if !shard.enqueueOverflow(channelID, 4) {
		t.Fatal("same-key overflow merge rejected")
	}

	shard.mu.Lock()
	item := shard.overflow[channelID]
	nextSeq := shard.nextSeq
	shard.mu.Unlock()
	if item.barrier != processed.queueSeq || nextSeq <= item.barrier {
		t.Fatalf("overflow barrier/next = %d/%d, want first barrier %d while a newer payload remains queued", item.barrier, nextSeq, processed.queueSeq)
	}
	var got channelFanoutNudge
	queued, blocked := shard.tryQueueOverflow(func(nudge channelFanoutNudge) bool {
		got = nudge
		return true
	})
	if !queued || blocked || got.channelID != channelID || got.pts != 4 {
		t.Fatalf("continuous-load drain = queued:%v blocked:%v nudge:%+v, want highest pts 4", queued, blocked, got)
	}
	if len(shard.jobs) != 1 {
		t.Fatalf("replacement payload queue length = %d, want producer still active", len(shard.jobs))
	}
}

func TestChannelFanoutRecoveryFailureBackoffIgnoresContinuousWake(t *testing.T) {
	sessions := newRecoveryFanoutSessions([]int64{10}, nil)
	channels := &recoveryFanoutChannels{pts: map[int64]int{10: 7}, failCalls: 1 << 30}
	r := New(Config{}, Deps{Sessions: sessions, Channels: channels}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, 1, 1)
	d.nudgeWorkers = 0
	r.channelFanout = d
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	for !d.started.Load() {
		time.Sleep(time.Millisecond)
	}

	deadline := time.Now().Add(85 * time.Millisecond)
	for time.Now().Before(deadline) {
		d.requestRecoverySweep("continuous saturation during injected DB failure")
		time.Sleep(time.Millisecond)
	}
	calls := channels.callCount()
	cancel()
	<-done
	// With 10ms -> 20ms -> 40ms backoff this window permits about four calls. Keep a generous
	// scheduler margin; the old wake-bypasses-timer loop makes tens of calls in the same window.
	if calls < 2 || calls > 7 {
		t.Fatalf("MaxChannelPtsBatch calls under continuous wake = %d, want bounded backoff in [2,7]", calls)
	}
}

func TestChannelFanoutShutdownWaitsForInFlightOverflowAdmission(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, 1, 1)
	d.maxQueuedJobs = 0 // force Enqueue directly into overflow admission
	r.channelFanout = d
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	for !d.started.Load() {
		time.Sleep(time.Millisecond)
	}

	shard := d.shards[0]
	shard.mu.Lock() // hold admission so the enqueue lifetime is observable
	enqueueDone := make(chan struct{})
	go func() {
		d.Enqueue(context.Background(), channelFanoutJob{
			scope: channelFanoutMembers, channelID: 1001, pts: 5,
			build: func(context.Context, int64) *tg.Updates { return nil },
		})
		close(enqueueDone)
	}()
	observedReader := false
	waitUntil := time.Now().Add(time.Second)
	for time.Now().Before(waitUntil) {
		if !d.enqueueMu.TryLock() {
			observedReader = true
			break
		}
		d.enqueueMu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if !observedReader {
		shard.mu.Unlock()
		cancel()
		<-done
		t.Fatal("Enqueue did not retain shutdown gate while waiting for overflow admission")
	}

	cancel()
	time.Sleep(20 * time.Millisecond)
	if d.stopped.Load() {
		shard.mu.Unlock()
		<-enqueueDone
		<-done
		t.Fatal("shutdown crossed an in-flight overflow admission")
	}
	shard.mu.Unlock()
	<-enqueueDone
	<-done
	shard.mu.Lock()
	remaining := len(shard.overflow)
	shard.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("overflow entries after shutdown = %d, want cleanup after all admissions finish", remaining)
	}
}

func TestChannelFanoutNudgeQueueFullRetainsHighestPtsUntilRetrySucceeds(t *testing.T) {
	const channelID = int64(1001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, 1, 1)
	d.nudgeWorkers = 0
	d.nudgeJobs = make(chan int64, 1)
	d.nudgeLimit = 1
	if !d.offerNudge(channelFanoutNudge{channelID: 9999, pts: 1}) {
		t.Fatal("failed to prefill nudge mailbox")
	}
	r.channelFanout = d

	shard := d.shards[0]
	if !shard.enqueueOverflow(channelID, 5) {
		t.Fatal("initial overflow watermark rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	for i := 0; i < 200 && !d.started.Load(); i++ {
		time.Sleep(time.Millisecond)
	}
	if !d.started.Load() {
		cancel()
		<-done
		t.Fatal("dispatcher did not start")
	}

	// Let the shard observe the full shared queue, then merge newer/lower pts while retries
	// remain backpressured.  The failed queue attempt must not remove the mailbox entry.
	time.Sleep(10 * time.Millisecond)
	if !shard.enqueueOverflow(channelID, 9) || !shard.enqueueOverflow(channelID, 7) {
		cancel()
		<-done
		t.Fatal("same-channel overflow merge was rejected")
	}
	time.Sleep(10 * time.Millisecond)
	shard.mu.Lock()
	item, exists := shard.overflow[channelID]
	mailboxLen := len(shard.overflow)
	shard.mu.Unlock()
	if !exists || mailboxLen != 1 || item.pts != 9 {
		cancel()
		<-done
		t.Fatalf("full nudge queue mailbox = exists:%v len:%d item:%+v, want one retained pts=9", exists, mailboxLen, item)
	}

	// Free one shared slot.  The shard-owned bounded retry timer must submit the retained
	// highest watermark without requiring another payload or Enqueue call.
	blockedID := <-d.nudgeJobs
	if _, ok := d.takeNudge(blockedID); !ok {
		t.Fatal("prefilled nudge mailbox lost its pts")
	}
	select {
	case gotID := <-d.nudgeJobs:
		got, ok := d.takeNudge(gotID)
		if !ok {
			cancel()
			<-done
			t.Fatal("retried nudge queue id had no coalesced pts")
		}
		if got.channelID != channelID || got.pts != 9 {
			cancel()
			<-done
			t.Fatalf("retried nudge = %+v, want channel=%d pts=9", got, channelID)
		}
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("retained overflow was not retried after nudge queue space became available")
	}
	shard.mu.Lock()
	_, exists = shard.overflow[channelID]
	shard.mu.Unlock()
	if exists {
		cancel()
		<-done
		t.Fatal("overflow watermark remained after successful nudge queue submission")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop with an armed/recent nudge retry")
	}
}

func TestChannelFanoutNudgeBackpressureDoesNotBlockPayloadShards(t *testing.T) {
	const (
		shardCount   = 4
		jobsPerShard = 64
	)
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, shardCount, jobsPerShard+1)
	d.nudgeWorkers = 0
	d.nudgeJobs = make(chan int64, 1)
	d.nudgeLimit = 1
	if !d.offerNudge(channelFanoutNudge{channelID: 9999, pts: 1}) {
		t.Fatal("failed to prefill nudge mailbox")
	}
	r.channelFanout = d

	channelIDs := make([]int64, shardCount)
	for shardIndex := range shardCount {
		channelID := int64(1000 + shardIndex)
		for d.shardIndex(channelID) != shardIndex {
			channelID++
		}
		channelIDs[shardIndex] = channelID
		if !d.shards[shardIndex].enqueueOverflow(channelID, 10+shardIndex) {
			t.Fatalf("shard %d overflow watermark rejected", shardIndex)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	for i := 0; i < 200 && !d.started.Load(); i++ {
		time.Sleep(time.Millisecond)
	}
	if !d.started.Load() {
		cancel()
		<-done
		t.Fatal("dispatcher did not start")
	}

	processed := make(chan struct{}, shardCount*jobsPerShard)
	for shardIndex, channelID := range channelIDs {
		for pts := 1; pts <= jobsPerShard; pts++ {
			d.Enqueue(context.Background(), channelFanoutJob{
				scope:      channelFanoutMembers,
				channelID:  channelID,
				pts:        pts,
				recipients: []int64{int64(2000 + shardIndex)},
				build: func(context.Context, int64) *tg.Updates {
					processed <- struct{}{}
					return &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: 1}
				},
			})
		}
	}
	for completed := 0; completed < shardCount*jobsPerShard; completed++ {
		select {
		case <-processed:
		case <-time.After(2 * time.Second):
			cancel()
			<-done
			t.Fatalf("payload shards stalled at %d/%d while nudge queue was full", completed, shardCount*jobsPerShard)
		}
	}
	for shardIndex, shard := range d.shards {
		shard.mu.Lock()
		item, exists := shard.overflow[channelIDs[shardIndex]]
		shard.mu.Unlock()
		if !exists || item.pts != 10+shardIndex {
			cancel()
			<-done
			t.Fatalf("shard %d lost overflow under nudge backpressure: exists=%v item=%+v", shardIndex, exists, item)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher stop blocked behind a full nudge queue")
	}
	if jobs, bytes := d.queuedBudgetSnapshot(); jobs != 0 || bytes != 0 {
		t.Fatalf("shutdown queue budget = %d/%d, want 0/0", jobs, bytes)
	}
}

func TestChannelFanoutAllMemoryLayersFullReturnsRPCAndDurableSweepRecovers(t *testing.T) {
	const (
		fullChannel = int64(1001)
		lostChannel = int64(3001)
		lostMaxPts  = 123
	)
	nudgeRelease := make(chan struct{})
	sessions := newRecoveryFanoutSessions([]int64{lostChannel}, nudgeRelease)
	channels := &recoveryFanoutChannels{pts: map[int64]int{lostChannel: lostMaxPts}}
	r := New(Config{}, Deps{Sessions: sessions, Channels: channels}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, 1, 1)
	d.shards[0].overflowLimit = 1
	d.nudgeWorkers = 1
	d.nudgeJobs = make(chan int64, 1)
	d.nudgeLimit = 1
	r.channelFanout = d

	// Occupy the sole nudge worker, then the sole queued nudge slot.
	if !d.offerNudge(channelFanoutNudge{channelID: 9000, pts: 1}) {
		t.Fatal("initial nudge rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.Run(ctx)
		close(done)
	}()
	select {
	case <-sessions.pushStarted:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("nudge worker did not enter blocking push")
	}
	if !d.offerNudge(channelFanoutNudge{channelID: 9001, pts: 2}) {
		cancel()
		<-done
		t.Fatal("queued nudge slot was not available")
	}

	// Occupy the payload worker and its one buffered slot.
	payloadStarted := make(chan struct{})
	payloadRelease := make(chan struct{})
	job := func(channelID int64, pts int, build channelFanoutBuilder) channelFanoutJob {
		return channelFanoutJob{scope: channelFanoutMembers, channelID: channelID, pts: pts, recipients: []int64{42}, build: build}
	}
	d.Enqueue(context.Background(), job(7001, 1, func(context.Context, int64) *tg.Updates {
		close(payloadStarted)
		<-payloadRelease
		return nil
	}))
	select {
	case <-payloadStarted:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("payload worker did not enter blocking build")
	}
	d.Enqueue(context.Background(), job(7002, 2, func(context.Context, int64) *tg.Updates { return nil }))
	// The next job cannot retain its payload and fills the sole shard overflow key.
	d.Enqueue(context.Background(), job(fullChannel, 3, func(context.Context, int64) *tg.Updates { return nil }))

	// All key-bearing structures are now full. Repeated lost-channel producers must publish only
	// a constant-size generation and return; no producer waiter or goroutine is allowed.
	canceled, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	goroutinesBefore := runtime.NumGoroutine()
	started := time.Now()
	for pts := 4; pts <= 67; pts++ {
		d.Enqueue(canceled, job(lostChannel, pts, func(context.Context, int64) *tg.Updates { return nil }))
	}
	elapsed := time.Since(started)
	goroutinesAfter := runtime.NumGoroutine()
	t.Logf("64 fully saturated Enqueue calls: elapsed=%v goroutines_before=%d goroutines_after=%d", elapsed, goroutinesBefore, goroutinesAfter)
	if elapsed > 500*time.Millisecond {
		close(payloadRelease)
		cancel()
		<-done
		t.Fatalf("64 saturated Enqueue calls took %v; RPC producers must not wait for recovery capacity", elapsed)
	}
	if goroutinesAfter > goroutinesBefore+1 {
		close(payloadRelease)
		cancel()
		<-done
		t.Fatalf("producer goroutines grew from %d to %d; Enqueue must not spawn per-request waiters", goroutinesBefore, goroutinesAfter)
	}
	if generation := d.recoveryGeneration.Load(); generation == 0 {
		close(payloadRelease)
		cancel()
		<-done
		t.Fatal("all-memory saturation did not publish a recovery generation")
	}
	d.shards[0].mu.Lock()
	waiters := d.shards[0].overflowWaiters
	d.shards[0].mu.Unlock()
	if waiters > 1 {
		close(payloadRelease)
		cancel()
		<-done
		t.Fatalf("overflow waiters = %d, want at most the one fixed recovery actor", waiters)
	}

	// Restore capacity. The sweep no longer has the lost channel key in memory, so success proves
	// it enumerated online membership and reloaded the authoritative max pts.
	close(payloadRelease)
	close(nudgeRelease)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pts := sessions.nudgePts(lostChannel)
		if len(pts) > 0 && pts[len(pts)-1] == lostMaxPts && d.recoveryCompleted.Load() == d.recoveryGeneration.Load() {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	pts := sessions.nudgePts(lostChannel)
	if len(pts) == 0 || pts[len(pts)-1] != lostMaxPts {
		cancel()
		<-done
		t.Fatalf("lost channel nudges = %v, want durable max pts %d", pts, lostMaxPts)
	}
	if got, want := d.recoveryCompleted.Load(), d.recoveryGeneration.Load(); got != want {
		cancel()
		<-done
		t.Fatalf("recovery generation completed=%d want=%d", got, want)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not stop after saturated recovery")
	}
	if jobs, bytes := d.queuedBudgetSnapshot(); jobs != 0 || bytes != 0 {
		t.Fatalf("shutdown queue budget = %d/%d, want 0/0", jobs, bytes)
	}
}

func TestChannelFanoutNudgeDeadlineRequestsRecoveryAndShutdownConverges(t *testing.T) {
	blocked := make(chan struct{}) // deliberately never closed
	sessions := newRecoveryFanoutSessions(nil, blocked)
	channels := &recoveryFanoutChannels{pts: map[int64]int{}}
	r := New(Config{}, Deps{Sessions: sessions, Channels: channels}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, 1, 1)
	d.nudgeTimeout = 25 * time.Millisecond
	r.channelFanout = d
	if !d.offerNudge(channelFanoutNudge{channelID: 8001, pts: 9}) {
		t.Fatal("nudge rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	started := time.Now()
	go func() {
		d.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && (sessions.pushErrors() == 0 || d.recoveryGeneration.Load() == 0) {
		time.Sleep(time.Millisecond)
	}
	if sessions.pushErrors() == 0 || d.recoveryGeneration.Load() == 0 {
		cancel()
		<-done
		t.Fatal("blocked nudge did not hit its explicit deadline and request recovery")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		cancel()
		<-done
		t.Fatalf("nudge deadline observed after %v, want bounded worker occupancy", elapsed)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("dispatcher shutdown did not cancel a blocked nudge push")
	}
}

func TestChannelFanoutShutdownCancelsCurrentlyBlockedNudgeWorker(t *testing.T) {
	blocked := make(chan struct{}) // never released; only Run ctx may end the push
	sessions := newRecoveryFanoutSessions(nil, blocked)
	channels := &recoveryFanoutChannels{pts: map[int64]int{}}
	r := New(Config{}, Deps{Sessions: sessions, Channels: channels}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, 1, 1)
	d.nudgeTimeout = time.Hour
	r.channelFanout = d
	if !d.offerNudge(channelFanoutNudge{channelID: 8101, pts: 10}) {
		t.Fatal("nudge rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	select {
	case <-sessions.pushStarted:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("nudge worker did not enter blocked push")
	}
	started := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not converge after canceling a currently blocked nudge worker")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("shutdown took %v with blocked nudge worker", elapsed)
	}
	if sessions.pushErrors() == 0 {
		t.Fatal("blocked session push did not observe Run context cancellation")
	}
}

func TestChannelFanoutRecoveryRetainsGenerationOnErrorAndRepeatsConcurrentGeneration(t *testing.T) {
	t.Run("max pts error is retried", func(t *testing.T) {
		release := make(chan struct{})
		close(release)
		sessions := newRecoveryFanoutSessions([]int64{10}, release)
		channels := &recoveryFanoutChannels{pts: map[int64]int{10: 7}, failCalls: 1}
		r := New(Config{}, Deps{Sessions: sessions, Channels: channels}, zaptest.NewLogger(t), clock.System)
		d := newChannelFanoutDispatcher(r, 1, 1)
		d.nudgeWorkers = 0
		r.channelFanout = d
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { d.Run(ctx); close(done) }()
		d.requestRecoverySweep("test injected max pts error")
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && (channels.callCount() < 2 || d.recoveryCompleted.Load() != d.recoveryGeneration.Load()) {
			time.Sleep(time.Millisecond)
		}
		if channels.callCount() < 2 {
			t.Fatalf("MaxChannelPts calls = %d, want retry after error", channels.callCount())
		}
		if got, want := d.recoveryCompleted.Load(), d.recoveryGeneration.Load(); got != want {
			t.Fatalf("completed generation=%d want=%d after retry", got, want)
		}
		cancel()
		<-done
	})

	t.Run("generation raised mid sweep forces a second full pass", func(t *testing.T) {
		release := make(chan struct{})
		firstCalled := make(chan struct{})
		sessions := newRecoveryFanoutSessions([]int64{10}, nil)
		channels := &recoveryFanoutChannels{pts: map[int64]int{10: 5}, firstCalled: firstCalled, release: release}
		r := New(Config{}, Deps{Sessions: sessions, Channels: channels}, zaptest.NewLogger(t), clock.System)
		d := newChannelFanoutDispatcher(r, 1, 1)
		d.nudgeWorkers = 0
		r.channelFanout = d
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { d.Run(ctx); close(done) }()
		d.requestRecoverySweep("first generation")
		select {
		case <-firstCalled:
		case <-time.After(time.Second):
			cancel()
			<-done
			t.Fatal("first sweep did not reach MaxChannelPts")
		}
		channels.setPts(10, 9)
		d.requestRecoverySweep("concurrent generation")
		close(release)
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) && (channels.callCount() < 2 || d.recoveryCompleted.Load() != d.recoveryGeneration.Load()) {
			time.Sleep(time.Millisecond)
		}
		if channels.callCount() < 2 {
			t.Fatalf("MaxChannelPts calls = %d, want two complete passes", channels.callCount())
		}
		if got, want := d.recoveryCompleted.Load(), d.recoveryGeneration.Load(); got != want {
			t.Fatalf("completed generation=%d want=%d", got, want)
		}
		select {
		case channelID := <-d.nudgeJobs:
			nudge, ok := d.takeNudge(channelID)
			if !ok || nudge.channelID != 10 || nudge.pts != 9 {
				t.Fatalf("coalesced nudge = %+v ok=%v, want channel=10 highest pts=9", nudge, ok)
			}
		case <-time.After(time.Second):
			t.Fatal("coalesced recovery nudge was not queued")
		}
		cancel()
		<-done
	})
}

func TestChannelFanoutCoalescingNudgeMailboxKeepsHighestPts(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	d := newChannelFanoutDispatcher(r, 1, 1)
	for _, pts := range []int{5, 11, 7} {
		if !d.offerNudge(channelFanoutNudge{channelID: 1001, pts: pts}) {
			t.Fatalf("offer pts %d rejected", pts)
		}
	}
	if got := len(d.nudgeJobs); got != 1 {
		t.Fatalf("nudge queue cardinality = %d, want one slot for a hot channel", got)
	}
	channelID := <-d.nudgeJobs
	nudge, ok := d.takeNudge(channelID)
	if !ok || nudge.pts != 11 {
		t.Fatalf("coalesced nudge = %+v ok=%v, want highest pts=11", nudge, ok)
	}
}

func TestChannelFanoutOverflowWaitHonorsContextStopAndHasNoLossyTimeout(t *testing.T) {
	t.Run("context cancellation", func(t *testing.T) {
		shard := newChannelFanoutShard(1)
		shard.overflowLimit = 1
		if !shard.enqueueOverflow(1001, 5) {
			t.Fatal("initial overflow watermark rejected")
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan bool, 1)
		go func() { result <- shard.enqueueOverflowWait(ctx, 2001, 7, make(chan struct{})) }()
		cancel()
		select {
		case accepted := <-result:
			if accepted {
				t.Fatal("overflow wait accepted without mailbox space")
			}
		case <-time.After(time.Second):
			t.Fatal("overflow wait ignored request context cancellation")
		}
	})

	t.Run("dispatcher stop", func(t *testing.T) {
		shard := newChannelFanoutShard(1)
		shard.overflowLimit = 1
		if !shard.enqueueOverflow(1001, 5) {
			t.Fatal("initial overflow watermark rejected")
		}
		stop := make(chan struct{})
		result := make(chan bool, 1)
		go func() { result <- shard.enqueueOverflowWait(context.Background(), 2001, 7, stop) }()
		close(stop)
		select {
		case accepted := <-result:
			if accepted {
				t.Fatal("overflow wait accepted without mailbox space")
			}
		case <-time.After(time.Second):
			t.Fatal("overflow wait ignored dispatcher stop")
		}
	})

	t.Run("waits beyond old maximum until space", func(t *testing.T) {
		shard := newChannelFanoutShard(1)
		shard.overflowLimit = 1
		if !shard.enqueueOverflow(1001, 5) {
			t.Fatal("initial overflow watermark rejected")
		}
		result := make(chan bool, 1)
		go func() {
			result <- shard.enqueueOverflowWait(context.Background(), 2001, 7, make(chan struct{}))
		}()
		select {
		case accepted := <-result:
			t.Fatalf("overflow wait ended at the old fixed timeout: accepted=%v", accepted)
		case <-time.After(150 * time.Millisecond):
		}
		if channelID, pts, ok := shard.popOverflow(); !ok || channelID != 1001 || pts != 5 {
			t.Fatalf("released watermark = %d/%d/%v, want 1001/5/true", channelID, pts, ok)
		}
		select {
		case accepted := <-result:
			if !accepted {
				t.Fatal("overflow wait rejected after mailbox space was released")
			}
		case <-time.After(time.Second):
			t.Fatal("overflow wait did not resume after mailbox space was released")
		}
	})
}
