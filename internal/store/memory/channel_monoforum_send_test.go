package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestSendMonoforumMessageAndHistory 验证频道私信(monoforum)发送+按订阅者读历史:
// 订阅者发、管理员回复同进一个 saved_peer 子会话;幂等;不同订阅者互不串会话。
func TestSendMonoforumMessageAndHistory(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	broadcast, err := store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: 1, Title: "DM", Broadcast: true, Date: 1_700_001_000})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	enabled, err := store.SetPaidMessagesPrice(ctx, 1, broadcast.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}

	sub := domain.Peer{Type: domain.PeerTypeUser, ID: 42}

	m1, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 42, SavedPeer: sub, RandomID: 111, Message: "hi", Date: 1_700_001_001})
	if err != nil {
		t.Fatalf("subscriber send 1: %v", err)
	}
	if m1.Message.SavedPeer != sub || m1.Message.ChannelID != monoID || m1.Message.Pts == 0 {
		t.Fatalf("m1 = %+v, want saved_peer sub + channel mono + pts>0", m1.Message)
	}
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 42, SavedPeer: sub, RandomID: 112, Message: "again", Date: 1_700_001_002}); err != nil {
		t.Fatalf("subscriber send 2: %v", err)
	}
	// 管理员回复:发件人是 creator,saved_peer 仍是该订阅者(同一子会话)。
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 1, SavedPeer: sub, RandomID: 113, Message: "reply", Date: 1_700_001_003}); err != nil {
		t.Fatalf("admin reply: %v", err)
	}

	mainHist, err := store.ListChannelHistory(ctx, 1, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("main monoforum history: %v", err)
	}
	if mainHist.Count != 1 || len(mainHist.Messages) != 1 {
		t.Fatalf("main monoforum history count=%d len=%d, want only service message", mainHist.Count, len(mainHist.Messages))
	}
	// monoforum 的服务消息是创建消息(渲染 "Direct messages were enabled in this channel."),
	// paid_messages_price 只进母广播频道。
	if action := mainHist.Messages[0].Action; action == nil || action.Type != domain.ChannelActionCreate {
		t.Fatalf("main monoforum action = %+v, want channel_create", action)
	}
	if len(mainHist.Channels) != 1 || mainHist.Channels[0].ID != broadcast.Channel.ID {
		t.Fatalf("main monoforum extra channels = %+v, want parent %d", mainHist.Channels, broadcast.Channel.ID)
	}
	if _, err := store.ListChannelHistory(ctx, 42, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10}); err == nil {
		t.Fatalf("subscriber main monoforum history = nil err, want denied")
	}

	// 幂等:相同 randomID 返回原消息、不重复。
	dup, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 42, SavedPeer: sub, RandomID: 111, Message: "hi", Date: 1_700_001_004})
	if err != nil {
		t.Fatalf("dup send: %v", err)
	}
	if !dup.Duplicate || dup.Message.ID != m1.Message.ID {
		t.Fatalf("dup = %+v, want duplicate of m1 id %d", dup.Message, m1.Message.ID)
	}
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 42, SavedPeer: sub, RandomID: 111, Message: "changed", Date: 1_700_001_004}); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("conflicting monoforum replay err=%v, want ErrMessageRandomIDDuplicate", err)
	}

	hist, err := store.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: monoID, SavedPeer: sub, Limit: 10})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if hist.Count != 3 || len(hist.Messages) != 3 {
		t.Fatalf("history count=%d len=%d, want 3", hist.Count, len(hist.Messages))
	}
	if hist.Messages[0].Body != "reply" {
		t.Fatalf("history[0] = %q, want newest 'reply'", hist.Messages[0].Body)
	}
	for _, m := range hist.Messages {
		if m.SavedPeer != sub {
			t.Fatalf("history msg saved_peer = %+v, want sub", m.SavedPeer)
		}
	}

	// 另一个订阅者的私信不串会话。
	other := domain.Peer{Type: domain.PeerTypeUser, ID: 99}
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 99, SavedPeer: other, RandomID: 201, Message: "other", Date: 1_700_001_005}); err != nil {
		t.Fatalf("other subscriber send: %v", err)
	}
	subHist, _ := store.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: monoID, SavedPeer: sub, Limit: 10})
	if subHist.Count != 3 {
		t.Fatalf("sub history after other subscriber = %d, want still 3 (no cross-talk)", subHist.Count)
	}

	// 去重按订阅者子会话维度:同一发件人(此处管理员)用相同 random_id 向两个不同订阅者发,不得互相去重。
	a, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 1, SavedPeer: sub, RandomID: 9001, Message: "to sub", Date: 1_700_001_010})
	if err != nil {
		t.Fatalf("dedup send A: %v", err)
	}
	b, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 1, SavedPeer: other, RandomID: 9001, Message: "to other", Date: 1_700_001_011})
	if err != nil {
		t.Fatalf("dedup send B: %v", err)
	}
	if b.Duplicate || b.Message.ID == a.Message.ID {
		t.Fatalf("cross-sublist same random_id wrongly deduped: a=%d b=%d dup=%v", a.Message.ID, b.Message.ID, b.Duplicate)
	}
	// 同一子会话真重发(相同 random_id)仍去重。
	again, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 1, SavedPeer: sub, RandomID: 9001, Message: "to sub", Date: 1_700_001_012})
	if err != nil {
		t.Fatalf("dedup resend A: %v", err)
	}
	if !again.Duplicate || again.Message.ID != a.Message.ID {
		t.Fatalf("same-sublist retry not deduped: again=%+v want dup of %d", again.Message, a.Message.ID)
	}

	// 订阅者子会话列表:两个订阅者,按 top 消息 id 倒序(other 最后发,排首)。
	dialogs, err := store.ListMonoforumDialogs(ctx, domain.MonoforumDialogsFilter{MonoforumID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("list dialogs: %v", err)
	}
	if dialogs.Count != 2 || len(dialogs.Dialogs) != 2 {
		t.Fatalf("dialogs count=%d len=%d, want 2 subscribers", dialogs.Count, len(dialogs.Dialogs))
	}
	if dialogs.Dialogs[0].SavedPeer != other {
		t.Fatalf("dialogs[0] saved_peer = %+v, want newest 'other'", dialogs.Dialogs[0].SavedPeer)
	}
	if dialogs.Dialogs[1].SavedPeer != sub || dialogs.Dialogs[1].TopMessageID == 0 {
		t.Fatalf("dialogs[1] = %+v, want sub with top message", dialogs.Dialogs[1])
	}
	store.mu.Lock()
	_, deleteEvent, _, err := store.deleteChannelMessagesLocked(store.channels[monoID], domain.ChannelMember{ChannelID: monoID, UserID: 1, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive}, []int{a.Message.ID}, 1, 1_700_001_013)
	store.mu.Unlock()
	if err != nil {
		t.Fatalf("delete monoforum message: %v", err)
	}
	ptsBeforeReplay, eventsBeforeReplay := store.ptsSeq[monoID], len(store.events[monoID])
	deletedReplay, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 1, SavedPeer: sub, RandomID: 9001, Message: "to sub", Date: 1_700_001_014})
	if err != nil {
		t.Fatalf("replay deleted monoforum message: %v", err)
	}
	if !deletedReplay.Duplicate || deletedReplay.Message.ID != a.Message.ID || deletedReplay.Message.Body != "to sub" || deletedReplay.ReplayDeleteEvent == nil || deletedReplay.ReplayDeleteEvent.Pts != deleteEvent.Pts {
		t.Fatalf("deleted monoforum replay = %+v, want first snapshot + durable delete %+v", deletedReplay, deleteEvent)
	}
	if store.ptsSeq[monoID] != ptsBeforeReplay || len(store.events[monoID]) != eventsBeforeReplay {
		t.Fatalf("deleted monoforum replay mutated pts/events = %d/%d, want %d/%d", store.ptsSeq[monoID], len(store.events[monoID]), ptsBeforeReplay, eventsBeforeReplay)
	}
}
