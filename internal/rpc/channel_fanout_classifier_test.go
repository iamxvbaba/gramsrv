package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// channelUpdateWirePts 返回一条 channel update 的 channel pts（若该 TL update 携带 channel pts）。
// 只有携带 channel pts 的 update 会推进客户端 channel PtsWaiter，是设计 §5 classifier 第一类
// 「channel-payload-pts durable」的判据；其余（participant=qts、channel state=无 pts）不携带。
func channelUpdateWirePts(u tg.UpdateClass) (int, bool) {
	switch v := u.(type) {
	case *tg.UpdateNewChannelMessage:
		return v.Pts, true
	case *tg.UpdateEditChannelMessage:
		return v.Pts, true
	case *tg.UpdateDeleteChannelMessages:
		return v.Pts, true
	case *tg.UpdatePinnedChannelMessages:
		return v.Pts, true
	default:
		return 0, false
	}
}

// TestChannelUpdateEventTypePtsClassification 固化设计 §5 classifier 的基础轴：哪些
// domain.ChannelUpdateEventType 经 tgChannelUpdate 产出「带 channel pts、会推进客户端 channel
// PtsWaiter」的真实 payload（→可进 Phase 0 异步 durable fan-out worker），哪些不带 channel pts
// （participant=qts / noop → 必须排除出 ChannelFanoutJob，走专用同步路径）。
//
// 新增 ChannelUpdateEventType 时必须在此表显式分类：忘记会触发完整性断言失败，强制人为裁决，
// 防止把无 pts / transient 事件误塞进 (channel_id, pts) durable worker（设计 §2.1/§8-D6）。
func TestChannelUpdateEventTypePtsClassification(t *testing.T) {
	const viewer = int64(9001)
	msg := domain.ChannelMessage{ChannelID: 1, ID: 10, SenderUserID: 2, Date: 1, Body: "hi"}

	cases := []struct {
		name           string
		eventType      domain.ChannelUpdateEventType
		event          domain.ChannelUpdateEvent
		wantCarriesPts bool // 是否产出带 channel pts 的 wire update（= 可进 durable worker）
	}{
		{
			name:           "new_message",
			eventType:      domain.ChannelUpdateNewMessage,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateNewMessage, ChannelID: 1, Pts: 5, PtsCount: 1, Message: msg},
			wantCarriesPts: true,
		},
		{
			name:           "edit_message",
			eventType:      domain.ChannelUpdateEditMessage,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateEditMessage, ChannelID: 1, Pts: 6, PtsCount: 1, Message: msg},
			wantCarriesPts: true,
		},
		{
			name:           "delete_messages",
			eventType:      domain.ChannelUpdateDeleteMessages,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateDeleteMessages, ChannelID: 1, Pts: 7, PtsCount: 1, MessageIDs: []int{10}},
			wantCarriesPts: true,
		},
		{
			name:           "pinned_messages",
			eventType:      domain.ChannelUpdatePinnedMessages,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdatePinnedMessages, ChannelID: 1, Pts: 8, PtsCount: 1, MessageIDs: []int{10}, Pinned: true},
			wantCarriesPts: true,
		},
		{
			// participant 是 qts/no-channel-pts：必须排除出 durable worker，走 participant 专用路径
			// （账号级 channel state / getFullChannel / getParticipants 兜底）。设计 §8-D6。
			name:           "participant",
			eventType:      domain.ChannelUpdateParticipant,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateParticipant, ChannelID: 1, Participant: domain.ChannelMember{UserID: 2}},
			wantCarriesPts: false,
		},
		{
			name:           "noop",
			eventType:      domain.ChannelUpdateNoop,
			event:          domain.ChannelUpdateEvent{Type: domain.ChannelUpdateNoop, ChannelID: 1},
			wantCarriesPts: false,
		},
	}

	// 完整性：本表必须覆盖全部已知 ChannelUpdateEventType。新增类型未分类 → 失败。
	allKnownTypes := map[domain.ChannelUpdateEventType]struct{}{
		domain.ChannelUpdateNewMessage:     {},
		domain.ChannelUpdateEditMessage:    {},
		domain.ChannelUpdateDeleteMessages: {},
		domain.ChannelUpdateParticipant:    {},
		domain.ChannelUpdatePinnedMessages: {},
		domain.ChannelUpdateNoop:           {},
	}
	covered := make(map[domain.ChannelUpdateEventType]struct{}, len(cases))
	for _, tc := range cases {
		covered[tc.eventType] = struct{}{}
	}
	for et := range allKnownTypes {
		if _, ok := covered[et]; !ok {
			t.Fatalf("ChannelUpdateEventType %q 未在 classifier 固化表分类——新增类型必须显式裁决 pts 归类", et)
		}
	}
	if len(covered) != len(allKnownTypes) {
		t.Fatalf("classifier 表覆盖 %d 类型，已知 %d 类型；新增类型须同步 allKnownTypes + 本表", len(covered), len(allKnownTypes))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			update := tgChannelUpdate(viewer, tc.event)
			if tc.eventType == domain.ChannelUpdateNoop {
				if update != nil {
					t.Fatalf("noop event produced update %#v, want nil", update)
				}
				return
			}
			if update == nil {
				t.Fatalf("event %q produced nil update", tc.eventType)
			}
			pts, carries := channelUpdateWirePts(update)
			if carries != tc.wantCarriesPts {
				t.Fatalf("event %q carriesChannelPts=%v (%T), want %v", tc.eventType, carries, update, tc.wantCarriesPts)
			}
			if tc.wantCarriesPts && pts != tc.event.Pts {
				t.Fatalf("event %q wire pts=%d, want %d", tc.eventType, pts, tc.event.Pts)
			}
		})
	}
}

// TestChannelFanoutEntranceClassificationLocked 把设计 §5 的「channel-scoped 主动投递入口」分类
// 与当前 dispatch 方式固化为测试期边界（防回归 + 文档化为何某些带 channel pts 的入口仍同步）。
//
// 本表是人工维护的清单（不自动反射调用图），与 docs/channel-fanout-async-design.md §5 一致。
// 新增/改动 channel-scoped 主动投递入口时必须同步本表并在 review 中复核分类：
//
//	已异步（durable channel-payload-pts → 进 enqueueChannelFanout* 异步 worker）：
//	  send / sendMedia / forward / forum-topic-msg / discussion 联动 / edit / geolive-edit / todo /
//	  bot-inline-edit / delete / deleteParticipantHistory / pin(updatePinnedMessage) / unpinAll /
//	  TTL-expiry-delete。
//	刻意保持同步且锁定（混合容器 / MustDeliver 失去 difference 恢复面 / 冷起手，详见下表 reason）：
//	  createChannel / importChatInvite / inviteToChannel / createChat(invite) / joinChannel /
//	  leaveChannel / editBanned(kick) / hideChatJoinRequest / hideAllChatJoinRequests /
//	  editTitle / group-call started/ended/invite 服务消息。
//	无 channel pts，排除出 durable worker（participant / state / TTL-set / forum-pin / read-outbox /
//	  group-call version / typing）；viewer-only 无 durable event（poll / reaction）——Phase 4 才迁移。
//
// 此处只断言一组「不变量级」的分类事实，作为编译期/测试期护栏。
func TestChannelFanoutEntranceClassificationLocked(t *testing.T) {
	type entrance struct {
		name           string
		carriesChanPts bool
		asyncFanout    bool   // 当前是否走异步 durable worker
		reason         string // 若带 pts 却仍同步，记录原因（mixed-container / MustDeliver / 冷起手）
	}
	// 仅列「带 channel pts」入口（无 pts 的 participant/state/typing 由上文事件类型表与设计排除规则覆盖）。
	entrances := []entrance{
		{name: "send/sendMedia/forward/forum-msg/discussion/edit/geolive/todo/bot-inline/delete/pin/unpinAll/deleteParticipantHistory/expiry-delete", carriesChanPts: true, asyncFanout: true},
		{name: "editTitle", carriesChanPts: true, asyncFanout: true, reason: "已拆容器：无 pts UpdateChannel 同步发(无 difference 恢复面)，带 pts 标题服务消息(broadcast+megagroup 均产 pts)走异步 fan-out(有 difference 恢复面+nudge 兜底)"},
		{name: "createChannel", carriesChanPts: true, asyncFanout: false, reason: "冷起手：recipients=创建者+少量初始受邀，无大群放大/无延迟收益，RPC result 与首 push 紧耦合"},
		{name: "importChatInvite/inviteToChannel/createChat-invite/joinChannel/hideChatJoinRequest/hideAllChatJoinRequests", carriesChanPts: true, asyncFanout: false, reason: "经 channelOperationUpdates 混合容器（pts 服务消息 + 无 pts UpdateChannel；broadcast 仅 UpdateChannel），且新成员 MustDeliver 失去 difference 恢复面——须先拆容器+no-fold 才能异步"},
		{name: "leaveChannel/editBanned(kick)", carriesChanPts: true, asyncFanout: false, reason: "离开/被踢者在 recipients 内且已失去 channel difference 恢复面（MustDeliver），可丢弃队列无 no-fold 通道；participant 部分无 pts"},
		{name: "groupCallServiceMessage", carriesChanPts: true, asyncFanout: false, reason: "经 channelOperationUpdates 混合容器；version 信令须留特殊路径，只能拆出服务消息再异步"},
	}
	for _, e := range entrances {
		if !e.carriesChanPts {
			t.Fatalf("entrance %q 误入本表（本表仅列带 channel pts 入口）", e.name)
		}
		if !e.asyncFanout && e.reason == "" {
			t.Fatalf("entrance %q 带 channel pts 却仍同步，必须记录保持同步的原因（mixed-container/MustDeliver/冷起手）", e.name)
		}
	}
}
