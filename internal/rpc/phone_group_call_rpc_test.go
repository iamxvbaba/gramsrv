package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appgroupcalls "telesrv/internal/app/groupcalls"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// groupCallSessions 在推送捕获之上叠加 OnlineUserProvider（群通话扇出需要）。
type groupCallSessions struct {
	phoneCaptureSessions
	online []int64
}

func (s *groupCallSessions) IsUserOnline(userID int64) bool { return false }
func (s *groupCallSessions) OnlineUserIDsForCandidates(candidateUserIDs []int64, limit int) []int64 {
	online := map[int64]struct{}{}
	for _, id := range s.online {
		online[id] = struct{}{}
	}
	out := make([]int64, 0, len(candidateUserIDs))
	for _, id := range candidateUserIDs {
		if _, ok := online[id]; ok {
			out = append(out, id)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out
}
func (s *groupCallSessions) TrackChannelInterest([8]byte, int64, int64, []int64)                {}
func (s *groupCallSessions) ClearChannelInterest([8]byte, int64, int64)                         {}
func (s *groupCallSessions) OnlineChannelUserIDs(int64, int) []int64                            { return nil }
func (s *groupCallSessions) ChannelMembershipGeneration([8]byte, int64) int64                   { return 0 }
func (s *groupCallSessions) SetSessionChannelMemberships([8]byte, int64, int64, []int64, int64) {}
func (s *groupCallSessions) AddUserChannelMembership(int64, int64)                              {}
func (s *groupCallSessions) RemoveUserChannelMembership(int64, int64)                           {}
func (s *groupCallSessions) OnlineChannelMemberUserIDs(channelID int64, limit int) []int64 {
	return append([]int64(nil), s.online...)
}

type groupCallFixture struct {
	t        *testing.T
	ctx      context.Context
	router   *Router
	sessions *groupCallSessions
	clk      *phoneTestClock
	owner    domain.User
	member   domain.User
	outsider domain.User
	channel  *tg.Channel
}

func newGroupCallFixture(t *testing.T) *groupCallFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	channelStore := memory.NewChannelStore()
	sessions := &groupCallSessions{}
	clk := &phoneTestClock{now: time.Unix(1_700_000_000, 0)}
	router := New(Config{GroupCallMaxParticipants: 8}, Deps{
		Users:      appusers.NewService(userStore),
		Channels:   appchannels.NewService(channelStore),
		GroupCalls: appgroupcalls.NewService(memory.NewGroupCallStore()),
		Sessions:   sessions,
	}, zaptest.NewLogger(t), clk)
	f := &groupCallFixture{t: t, ctx: ctx, router: router, sessions: sessions, clk: clk}
	mk := func(hash int64, phone, name string) domain.User {
		u, err := userStore.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name})
		if err != nil {
			t.Fatalf("create user %s: %v", name, err)
		}
		return u
	}
	f.owner = mk(2001, "13900000001", "Owner")
	f.member = mk(2002, "13900000002", "Member")
	f.outsider = mk(2003, "13900000003", "Outsider")

	created, err := router.onMessagesCreateChat(f.userCtx(f.owner, 11), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}},
		Title: "voice room",
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	updates := created.Updates.(*tg.Updates)
	for _, chat := range updates.Chats {
		if ch, ok := chat.(*tg.Channel); ok {
			f.channel = ch
			break
		}
	}
	if f.channel == nil {
		t.Fatalf("no channel in create chat result")
	}
	f.sessions.online = []int64{f.owner.ID, f.member.ID}
	f.sessions.reset()
	return f
}

func (f *groupCallFixture) userCtx(u domain.User, session int64) context.Context {
	return WithSessionID(WithUserID(f.ctx, u.ID), session)
}

func groupCallJoinParams(t *testing.T, ssrc int32) tg.DataJSON {
	t.Helper()
	payload := map[string]any{
		"ssrc":  ssrc,
		"ufrag": "clientufrag",
		"pwd":   "clientpwd-clientpwd-1234",
		"fingerprints": []map[string]any{
			{"hash": "sha-256", "fingerprint": "AA:BB:CC:DD", "setup": "passive"},
		},
		// TDesktop join 可能携带的多余字段：解析必须容忍。
		"ssrc-groups": []map[string]any{},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal join payload: %v", err)
	}
	return tg.DataJSON{Data: string(data)}
}

func findUpdate[T tg.UpdateClass](t *testing.T, updates tg.UpdatesClass) T {
	t.Helper()
	box, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, u := range box.Updates {
		if v, ok := u.(T); ok {
			return v
		}
	}
	var zero T
	t.Fatalf("updates %v missing %T", box.Updates, zero)
	return zero
}

// TestGroupCallPushSkipsNonMemberOnlineCandidates 验证群通话推送收件人必须过一次
// PG active 成员复核：在线成员索引 stale（如踢人窗口内、或索引维护缺口）时，
// 非成员绝不能收到通话状态/参与者名单（信息泄漏），真实成员照常收到。
func TestGroupCallPushSkipsNonMemberOnlineCandidates(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	// 模拟 stale 在线索引：outsider 不是频道成员却出现在候选里。
	f.sessions.online = []int64{f.owner.ID, f.member.ID, f.outsider.ID}

	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		RandomID: 1,
	})
	if err != nil {
		t.Fatalf("createGroupCall: %v", err)
	}
	callUpdate := findUpdate[*tg.UpdateGroupCall](t, createRes)
	call := callUpdate.Call.(*tg.GroupCall)
	f.sessions.reset()

	if _, err := f.router.onPhoneJoinGroupCall(ownerCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash},
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 1001),
	}); err != nil {
		t.Fatalf("joinGroupCall: %v", err)
	}

	pushes := f.sessions.records()
	memberGot := false
	for _, rec := range pushes {
		if rec.userID == f.outsider.ID {
			t.Fatalf("group call push leaked to non-member outsider %d: %+v", f.outsider.ID, rec)
		}
		if rec.userID == f.member.ID {
			memberGot = true
		}
	}
	if !memberGot {
		t.Fatalf("member %d received no group call push, fan-out broken: %+v", f.member.ID, pushes)
	}
}

func TestGroupCallM0Lifecycle(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	memberCtx := f.userCtx(f.member, 22)

	// --- create ---
	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		RandomID: 1,
	})
	if err != nil {
		t.Fatalf("createGroupCall: %v", err)
	}
	callUpdate := findUpdate[*tg.UpdateGroupCall](t, createRes)
	call, ok := callUpdate.Call.(*tg.GroupCall)
	if !ok || call.ID == 0 || call.AccessHash == 0 || call.Version != 1 {
		t.Fatalf("created call = %+v", callUpdate.Call)
	}
	// started 服务消息（带频道 pts、messageActionGroupCall 无 duration）。
	newMsg := findUpdate[*tg.UpdateNewChannelMessage](t, createRes)
	svc, ok := newMsg.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("service message = %T", newMsg.Message)
	}
	action, ok := svc.Action.(*tg.MessageActionGroupCall)
	if !ok {
		t.Fatalf("service action = %T, want MessageActionGroupCall", svc.Action)
	}
	if _, hasDuration := action.GetDuration(); hasDuration {
		t.Fatalf("started action must not carry duration")
	}
	if newMsg.Pts == 0 {
		t.Fatalf("service message must carry channel pts")
	}
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}

	// banner 数据源：channel 行已挂 ActiveCall。
	full, err := f.router.deps.Channels.GetChannel(f.ctx, f.member.ID, f.channel.ID)
	if err != nil || full.Channel.ActiveCallID != call.ID || full.Channel.ActiveCallNotEmpty {
		t.Fatalf("channel active call = %+v err=%v", full.Channel, err)
	}
	f.sessions.reset()

	// --- owner join ---
	joinRes, err := f.router.onPhoneJoinGroupCall(ownerCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   &inputCall,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, -100200300), // 负数 ssrc：uint32 按位转 int32
	})
	if err != nil {
		t.Fatalf("joinGroupCall: %v", err)
	}
	conn := findUpdate[*tg.UpdateGroupCallConnection](t, joinRes)
	var params struct {
		Transport struct {
			Ufrag        string `json:"ufrag"`
			Pwd          string `json:"pwd"`
			Fingerprints []struct {
				Hash  string `json:"hash"`
				Setup string `json:"setup"`
				Print string `json:"fingerprint"`
			} `json:"fingerprints"`
			Candidates []any `json:"candidates"`
		} `json:"transport"`
	}
	if err := json.Unmarshal([]byte(conn.Params.Data), &params); err != nil {
		t.Fatalf("connection params not parseable: %v\n%s", err, conn.Params.Data)
	}
	// ⚠ P2-6：M0 下发的 transport JSON 必须语法完备（合法 ufrag/pwd/sha-256 指纹 + 空 candidates）。
	if params.Transport.Ufrag == "" || params.Transport.Pwd == "" ||
		len(params.Transport.Fingerprints) != 1 ||
		params.Transport.Fingerprints[0].Hash != "sha-256" ||
		params.Transport.Fingerprints[0].Setup != "active" ||
		params.Transport.Fingerprints[0].Print == "" {
		t.Fatalf("transport params incomplete: %s", conn.Params.Data)
	}
	if len(params.Transport.Candidates) != 0 {
		t.Fatalf("M0 candidates = %v, want empty", params.Transport.Candidates)
	}
	joinParts := findUpdate[*tg.UpdateGroupCallParticipants](t, joinRes)
	if joinParts.Version != 2 || len(joinParts.Participants) != 1 {
		t.Fatalf("join participants update = %+v", joinParts)
	}
	if !joinParts.Participants[0].Self || joinParts.Participants[0].Source != -100200300 {
		t.Fatalf("join self participant = %+v", joinParts.Participants[0])
	}
	f.sessions.reset()

	// --- member join：在线成员收到 version=3 增量 ---
	if _, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   &inputCall,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 500600700),
	}); err != nil {
		t.Fatalf("member join: %v", err)
	}
	sawParticipants := false
	for _, rec := range f.sessions.records() {
		box, ok := rec.msg.(*tg.Updates)
		if !ok {
			continue
		}
		for _, u := range box.Updates {
			if parts, ok := u.(*tg.UpdateGroupCallParticipants); ok && rec.userID == f.owner.ID {
				if parts.Version != 3 || len(parts.Participants) != 1 {
					t.Fatalf("fanout participants = %+v", parts)
				}
				if parts.Participants[0].Self {
					t.Fatalf("member row pushed to owner must not be self-flagged")
				}
				sawParticipants = true
			}
		}
	}
	if !sawParticipants {
		t.Fatalf("owner must receive member-join participants update, got %+v", f.sessions.records())
	}
	// call_not_empty 翻转已写回 channel。
	view, _ := f.router.deps.Channels.GetChannel(f.ctx, f.owner.ID, f.channel.ID)
	if !view.Channel.ActiveCallNotEmpty {
		t.Fatalf("call_not_empty should flip after first join")
	}
	f.sessions.reset()

	// --- checkGroupCall：在会返回自己的 ssrc 子集；未在会返回空 ---
	ssrcs, err := f.router.onPhoneCheckGroupCall(memberCtx, &tg.PhoneCheckGroupCallRequest{
		Call: &inputCall, Sources: []int{500600700, 42},
	})
	if err != nil || len(ssrcs) != 1 || ssrcs[0] != 500600700 {
		t.Fatalf("checkGroupCall = %v err=%v", ssrcs, err)
	}
	// getGroupParticipants 携带当前 version。
	parts, err := f.router.onPhoneGetGroupParticipants(memberCtx, &tg.PhoneGetGroupParticipantsRequest{
		Call: &inputCall, Limit: 10,
	})
	if err != nil || parts.Count != 2 || parts.Version != 3 {
		t.Fatalf("getGroupParticipants = %+v err=%v", parts, err)
	}

	// --- 自我静音（M0 self-edit）---
	editRes, err := f.router.onPhoneEditGroupCallParticipant(memberCtx, &tg.PhoneEditGroupCallParticipantRequest{
		Call:        &inputCall,
		Participant: &tg.InputPeerSelf{},
	})
	if err == nil {
		t.Fatalf("edit without flags should be NOT_MODIFIED, got %+v", editRes)
	}
	req := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: &tg.InputPeerSelf{}}
	req.SetMuted(true)
	editRes, err = f.router.onPhoneEditGroupCallParticipant(memberCtx, req)
	if err != nil {
		t.Fatalf("self mute: %v", err)
	}
	muteParts := findUpdate[*tg.UpdateGroupCallParticipants](t, editRes)
	if muteParts.Version != 4 || !muteParts.Participants[0].Muted || !muteParts.Participants[0].CanSelfUnmute {
		t.Fatalf("self mute participants = %+v", muteParts.Participants[0])
	}
	f.sessions.reset()

	// --- leave ---
	leaveRes, err := f.router.onPhoneLeaveGroupCall(memberCtx, &tg.PhoneLeaveGroupCallRequest{
		Call: &inputCall, Source: 500600700,
	})
	if err != nil {
		t.Fatalf("leave: %v", err)
	}
	leaveParts := findUpdate[*tg.UpdateGroupCallParticipants](t, leaveRes)
	if leaveParts.Version != 5 || !leaveParts.Participants[0].Left {
		t.Fatalf("leave participants = %+v", leaveParts.Participants[0])
	}

	// --- discard（owner）---
	f.sessions.reset()
	f.clk.Advance(90 * time.Second) // 让 ended 服务消息有非零 duration
	discardRes, err := f.router.onPhoneDiscardGroupCall(ownerCtx, &inputCall)
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	discardUpdate := findUpdate[*tg.UpdateGroupCall](t, discardRes)
	if _, ok := discardUpdate.Call.(*tg.GroupCallDiscarded); !ok {
		t.Fatalf("discard call = %T, want GroupCallDiscarded", discardUpdate.Call)
	}
	endedMsg := findUpdate[*tg.UpdateNewChannelMessage](t, discardRes)
	endedAction := endedMsg.Message.(*tg.MessageService).Action.(*tg.MessageActionGroupCall)
	if _, hasDuration := endedAction.GetDuration(); !hasDuration {
		t.Fatalf("ended action must carry duration")
	}
	view, _ = f.router.deps.Channels.GetChannel(f.ctx, f.owner.ID, f.channel.ID)
	if view.Channel.ActiveCallID != 0 || view.Channel.ActiveCallNotEmpty {
		t.Fatalf("channel active call must clear after discard, got %+v", view.Channel)
	}
	// 重复 discard。
	if _, err := f.router.onPhoneDiscardGroupCall(ownerCtx, &inputCall); err == nil {
		t.Fatalf("double discard must fail")
	}
}

func TestGroupCallM0Permissions(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	memberCtx := f.userCtx(f.member, 22)
	outsiderCtx := f.userCtx(f.outsider, 33)

	// 普通成员建会：CHAT_ADMIN_REQUIRED。
	_, err := f.router.onPhoneCreateGroupCall(memberCtx, &tg.PhoneCreateGroupCallRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
	})
	assertPhoneRPCErr(t, err, "CHAT_ADMIN_REQUIRED")

	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}

	// 重复建会。
	_, err = f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
	})
	assertPhoneRPCErr(t, err, "GROUPCALL_ALREADY_STARTED")

	// 非群成员。
	_, err = f.router.onPhoneJoinGroupCall(outsiderCtx, &tg.PhoneJoinGroupCallRequest{
		Call: &inputCall, JoinAs: &tg.InputPeerSelf{}, Params: groupCallJoinParams(t, 123),
	})
	assertPhoneRPCErr(t, err, "GROUPCALL_FORBIDDEN")

	// access_hash 不符。
	_, err = f.router.onPhoneGetGroupCall(memberCtx, &tg.PhoneGetGroupCallRequest{
		Call: &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash + 1}, Limit: 10,
	})
	assertPhoneRPCErr(t, err, "GROUPCALL_INVALID")

	// ⚠ P1-6：slug 变体（conference 路径）须返回 GROUPCALL_INVALID 而非 panic。
	_, err = f.router.onPhoneGetGroupCall(memberCtx, &tg.PhoneGetGroupCallRequest{
		Call: &tg.InputGroupCallSlug{Slug: "x"}, Limit: 10,
	})
	assertPhoneRPCErr(t, err, "GROUPCALL_INVALID")

	// ssrc 撞车：member 用 owner 的 ssrc。
	if _, err := f.router.onPhoneJoinGroupCall(ownerCtx, &tg.PhoneJoinGroupCallRequest{
		Call: &inputCall, JoinAs: &tg.InputPeerSelf{}, Params: groupCallJoinParams(t, 777),
	}); err != nil {
		t.Fatalf("owner join: %v", err)
	}
	_, err = f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call: &inputCall, JoinAs: &tg.InputPeerSelf{}, Params: groupCallJoinParams(t, 777),
	})
	assertPhoneRPCErr(t, err, "GROUPCALL_SSRC_DUPLICATE_MUCH")

	// 普通成员 mute 他人：M2 起为 per-viewer 本地覆盖（合法，详见 TestGroupCallM2MemberOverride）。
	muteOther := &tg.PhoneEditGroupCallParticipantRequest{
		Call:        &inputCall,
		Participant: &tg.InputPeerUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash},
	}
	muteOther.SetMuted(true)
	if _, err = f.router.onPhoneEditGroupCallParticipant(memberCtx, muteOther); err != nil {
		t.Fatalf("member local-mute other: %v", err)
	}

	// 普通成员结束通话 / 改标题。
	_, err = f.router.onPhoneDiscardGroupCall(memberCtx, &inputCall)
	assertPhoneRPCErr(t, err, "CHAT_ADMIN_REQUIRED")
	_, err = f.router.onPhoneEditGroupCallTitle(memberCtx, &tg.PhoneEditGroupCallTitleRequest{Call: &inputCall, Title: "x"})
	assertPhoneRPCErr(t, err, "CHAT_ADMIN_REQUIRED")
}

func TestGroupCallSweeper(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)

	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	if _, err := f.router.onPhoneJoinGroupCall(ownerCtx, &tg.PhoneJoinGroupCallRequest{
		Call: &inputCall, JoinAs: &tg.InputPeerSelf{}, Params: groupCallJoinParams(t, 9001),
	}); err != nil {
		t.Fatalf("join: %v", err)
	}
	dispatcher := NewGroupCallSweepDispatcher(f.router, zaptest.NewLogger(t), 10*time.Second, 45*time.Second)

	// 30s：心跳未过期，不清。
	f.clk.Advance(30 * time.Second)
	dispatcher.DispatchOnce(f.ctx)
	if ssrcs, err := f.router.onPhoneCheckGroupCall(ownerCtx, &tg.PhoneCheckGroupCallRequest{Call: &inputCall, Sources: []int{9001}}); err != nil || len(ssrcs) != 1 {
		t.Fatalf("participant swept too early: %v err=%v", ssrcs, err)
	}
	// checkGroupCall 刷新水位后再过 30s：仍不清（水位以最后心跳为准）。
	f.clk.Advance(30 * time.Second)
	dispatcher.DispatchOnce(f.ctx)
	if ssrcs, _ := f.router.onPhoneCheckGroupCall(ownerCtx, &tg.PhoneCheckGroupCallRequest{Call: &inputCall, Sources: []int{9001}}); len(ssrcs) != 1 {
		t.Fatalf("heartbeat-refreshed participant must survive, got %v", ssrcs)
	}
	// 46s 无心跳：清掉；checkGroupCall 返回空集触发客户端 rejoin。
	f.clk.Advance(46 * time.Second)
	dispatcher.DispatchOnce(f.ctx)
	ssrcs, err := f.router.onPhoneCheckGroupCall(ownerCtx, &tg.PhoneCheckGroupCallRequest{Call: &inputCall, Sources: []int{9001}})
	if err != nil || len(ssrcs) != 0 {
		t.Fatalf("stale participant must be swept, got %v err=%v", ssrcs, err)
	}
	// rejoin（换 ssrc）恢复。
	if _, err := f.router.onPhoneJoinGroupCall(ownerCtx, &tg.PhoneJoinGroupCallRequest{
		Call: &inputCall, JoinAs: &tg.InputPeerSelf{}, Params: groupCallJoinParams(t, 9002),
	}); err != nil {
		t.Fatalf("rejoin after sweep: %v", err)
	}
}

// 防御未来误删：fmt 占位避免 unused import（findUpdate 泛型在部分场景未实例化时）。
var _ = fmt.Sprintf

// TestGroupCallM2MuteMatrix 覆盖 editGroupCallParticipant 权限矩阵全表。
func TestGroupCallM2MuteMatrix(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	memberCtx := f.userCtx(f.member, 22)

	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	for i, u := range []struct {
		ctx  context.Context
		ssrc int32
	}{{ownerCtx, 9001}, {memberCtx, 9002}} {
		if _, err := f.router.onPhoneJoinGroupCall(u.ctx, &tg.PhoneJoinGroupCallRequest{
			Call: &inputCall, JoinAs: &tg.InputPeerSelf{}, Params: groupCallJoinParams(t, u.ssrc),
		}); err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
	}
	memberPeer := &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}

	// --- admin 禁言成员：muted=true 且 can_self_unmute=false ---
	muteReq := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: memberPeer}
	muteReq.SetMuted(true)
	res, err := f.router.onPhoneEditGroupCallParticipant(ownerCtx, muteReq)
	if err != nil {
		t.Fatalf("admin mute: %v", err)
	}
	row := findUpdate[*tg.UpdateGroupCallParticipants](t, res).Participants[0]
	if !row.Muted || row.CanSelfUnmute {
		t.Fatalf("admin-muted row = %+v, want muted && !can_self_unmute", row)
	}

	// --- 被禁言成员自行 unmute：forbidden（客户端此时只会发 raise_hand）---
	selfUnmute := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: &tg.InputPeerSelf{}}
	selfUnmute.SetMuted(false)
	_, err = f.router.onPhoneEditGroupCallParticipant(memberCtx, selfUnmute)
	assertPhoneRPCErr(t, err, "GROUPCALL_FORBIDDEN")

	// --- 举手：rating 单调、versioned 推送 ---
	raise := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: &tg.InputPeerSelf{}}
	raise.SetRaiseHand(true)
	res, err = f.router.onPhoneEditGroupCallParticipant(memberCtx, raise)
	if err != nil {
		t.Fatalf("raise hand: %v", err)
	}
	row = findUpdate[*tg.UpdateGroupCallParticipants](t, res).Participants[0]
	rating, hasRating := row.GetRaiseHandRating()
	if !hasRating || rating <= 0 {
		t.Fatalf("raise hand row = %+v, want rating > 0", row)
	}

	// --- admin 允许发言（muted=false）：恢复 can_self_unmute、清举手、不替用户开麦 ---
	allow := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: memberPeer}
	allow.SetMuted(false)
	res, err = f.router.onPhoneEditGroupCallParticipant(ownerCtx, allow)
	if err != nil {
		t.Fatalf("admin allow: %v", err)
	}
	row = findUpdate[*tg.UpdateGroupCallParticipants](t, res).Participants[0]
	if !row.Muted || !row.CanSelfUnmute {
		t.Fatalf("allow-to-speak row = %+v, want still muted && can_self_unmute", row)
	}
	if _, has := row.GetRaiseHandRating(); has {
		t.Fatalf("allow-to-speak must clear raise hand, got %+v", row)
	}

	// --- 现在成员可自行 unmute ---
	if _, err := f.router.onPhoneEditGroupCallParticipant(memberCtx, selfUnmute); err != nil {
		t.Fatalf("self unmute after allow: %v", err)
	}

	// --- admin 设全局音量 ---
	vol := &tg.PhoneEditGroupCallParticipantRequest{Call: &inputCall, Participant: memberPeer}
	vol.SetVolume(5000)
	res, err = f.router.onPhoneEditGroupCallParticipant(ownerCtx, vol)
	if err != nil {
		t.Fatalf("admin volume: %v", err)
	}
	row = findUpdate[*tg.UpdateGroupCallParticipants](t, res).Participants[0]
	if v, has := row.GetVolume(); !has || v != 5000 || !row.VolumeByAdmin {
		t.Fatalf("admin volume row = %+v", row)
	}
}

// TestGroupCallM2MemberOverride 覆盖普通成员的本地静音/音量（per-viewer overrides）。
func TestGroupCallM2MemberOverride(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	memberCtx := f.userCtx(f.member, 22)

	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}
	for _, u := range []struct {
		ctx  context.Context
		ssrc int32
	}{{ownerCtx, 9001}, {memberCtx, 9002}} {
		if _, err := f.router.onPhoneJoinGroupCall(u.ctx, &tg.PhoneJoinGroupCallRequest{
			Call: &inputCall, JoinAs: &tg.InputPeerSelf{}, Params: groupCallJoinParams(t, u.ssrc),
		}); err != nil {
			t.Fatalf("join: %v", err)
		}
	}
	versionBefore := 0
	if c, found, _ := f.router.deps.GroupCalls.Get(f.ctx, call.ID); found {
		versionBefore = c.Version
	}
	f.sessions.reset()

	// member 本地静音 owner。
	localMute := &tg.PhoneEditGroupCallParticipantRequest{
		Call:        &inputCall,
		Participant: &tg.InputPeerUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash},
	}
	localMute.SetMuted(true)
	res, err := f.router.onPhoneEditGroupCallParticipant(memberCtx, localMute)
	if err != nil {
		t.Fatalf("local mute: %v", err)
	}
	// ⚠ P2-7：推给 setter 的行必须带 min flag + muted_by_you，防 setter 其它设备
	// 用该行覆盖本地 muted/volume 状态。
	row := findUpdate[*tg.UpdateGroupCallParticipants](t, res).Participants[0]
	if !row.Min || !row.MutedByYou {
		t.Fatalf("override row = %+v, want min && muted_by_you", row)
	}
	// overrides 不推进全房间 version。
	if c, found, _ := f.router.deps.GroupCalls.Get(f.ctx, call.ID); !found || c.Version != versionBefore {
		t.Fatalf("override must not bump version: %d → %d", versionBefore, c.Version)
	}
	// target 自身权威态不受影响。
	if p, found, _ := f.router.deps.GroupCalls.Participant(f.ctx, call.ID, f.owner.ID); !found || p.Muted {
		t.Fatalf("target authoritative state polluted: %+v", p)
	}
	// 推送只发给 setter 自己。
	for _, rec := range f.sessions.records() {
		if rec.userID != f.member.ID {
			t.Fatalf("override pushed to %d, want setter only", rec.userID)
		}
	}
}

// TestGroupCallM2Invite 覆盖 inviteToGroupCall 服务消息。
func TestGroupCallM2Invite(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)

	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, &tg.PhoneCreateGroupCallRequest{
		Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	inputCall := tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}

	// 邀请群成员：产生带频道 pts 的 messageActionInviteToGroupCall。
	res, err := f.router.onPhoneInviteToGroupCall(ownerCtx, &tg.PhoneInviteToGroupCallRequest{
		Call:  &inputCall,
		Users: []tg.InputUserClass{&tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}},
	})
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	msgUpdate := findUpdate[*tg.UpdateNewChannelMessage](t, res)
	if msgUpdate.Pts == 0 {
		t.Fatalf("invite service message must carry channel pts")
	}
	action, ok := msgUpdate.Message.(*tg.MessageService).Action.(*tg.MessageActionInviteToGroupCall)
	if !ok {
		t.Fatalf("invite action = %T", msgUpdate.Message.(*tg.MessageService).Action)
	}
	if len(action.Users) != 1 || action.Users[0] != f.member.ID {
		t.Fatalf("invite action users = %v", action.Users)
	}

	// 邀请非群成员。
	_, err = f.router.onPhoneInviteToGroupCall(ownerCtx, &tg.PhoneInviteToGroupCallRequest{
		Call:  &inputCall,
		Users: []tg.InputUserClass{&tg.InputUser{UserID: f.outsider.ID, AccessHash: f.outsider.AccessHash}},
	})
	assertPhoneRPCErr(t, err, "USER_NOT_PARTICIPANT")
}
