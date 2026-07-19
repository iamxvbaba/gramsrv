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
	if !containsInt64(m1.Recipients, 1) || !containsInt64(m1.Recipients, 42) || len(m1.Recipients) != 2 {
		t.Fatalf("m1 recipients = %v, want subscriber 42 + parent admin 1", m1.Recipients)
	}
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 42, SavedPeer: sub, RandomID: 112, Message: "again", Date: 1_700_001_002}); err != nil {
		t.Fatalf("subscriber send 2: %v", err)
	}
	// 管理员回复:发件人是 creator,saved_peer 仍是该订阅者(同一子会话)。
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 1, SavedPeer: sub, RandomID: 113, Message: "reply", ReplyTo: &domain.MessageReply{MessageID: m1.Message.ID, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}}, Date: 1_700_001_003}); err != nil {
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
	subscriberHist, err := store.ListChannelHistory(ctx, 42, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("subscriber monoforum history: %v", err)
	}
	if subscriberHist.Count != 3 || len(subscriberHist.Messages) != 3 {
		t.Fatalf("subscriber monoforum history count=%d len=%d, want 3 own messages", subscriberHist.Count, len(subscriberHist.Messages))
	}
	for _, message := range subscriberHist.Messages {
		if message.SavedPeer != sub {
			t.Fatalf("subscriber history leaked saved_peer=%+v, want self %+v", message.SavedPeer, sub)
		}
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
	if hist.Messages[0].ReplyTo == nil || hist.Messages[0].ReplyTo.MessageID != m1.Message.ID {
		t.Fatalf("history[0] reply = %+v, want message %d", hist.Messages[0].ReplyTo, m1.Message.ID)
	}
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 1, SavedPeer: sub, RandomID: 114, Message: "cross reply", ReplyTo: &domain.MessageReply{MessageID: 999999, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}}, Date: 1_700_001_004}); !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("invalid monoforum reply err = %v, want ErrReplyMessageIDInvalid", err)
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
	subscriberChannelHistory, err := store.ListChannelHistory(ctx, 42, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("subscriber channel history after other subscriber: %v", err)
	}
	if subscriberChannelHistory.Count != 3 || len(subscriberChannelHistory.Messages) != 3 {
		t.Fatalf("subscriber channel history after other = %d/%d, want own 3", subscriberChannelHistory.Count, len(subscriberChannelHistory.Messages))
	}
	for _, message := range subscriberChannelHistory.Messages {
		if message.SavedPeer != sub {
			t.Fatalf("subscriber channel history leaked message %+v", message)
		}
	}
	diff, err := store.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: 42, ChannelID: monoID, Pts: 0, Limit: 100})
	if err != nil {
		t.Fatalf("subscriber channel difference: %v", err)
	}
	if diff.Pts != store.channels[monoID].Pts {
		t.Fatalf("subscriber difference pts = %d, want channel pts %d despite filtered events", diff.Pts, store.channels[monoID].Pts)
	}
	if len(diff.NewMessages) != 3 {
		t.Fatalf("subscriber difference messages = %d, want own 3", len(diff.NewMessages))
	}
	for _, message := range diff.NewMessages {
		if message.SavedPeer != sub {
			t.Fatalf("subscriber difference leaked message %+v", message)
		}
	}
	activeChannelIDs, err := store.ListActiveChannelIDsForUser(ctx, 42, 0, 10)
	if err != nil {
		t.Fatalf("subscriber active channels: %v", err)
	}
	if !containsInt64(activeChannelIDs, monoID) {
		t.Fatalf("subscriber active channels = %v, want monoforum %d for offline recovery", activeChannelIDs, monoID)
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
	deleteDiff, err := store.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: 42, ChannelID: monoID, Pts: deleteEvent.Pts - deleteEvent.PtsCount, Limit: 10})
	if err != nil {
		t.Fatalf("subscriber difference after own delete: %v", err)
	}
	if deleteDiff.Pts != deleteEvent.Pts || len(deleteDiff.OtherUpdates) != 1 || len(deleteDiff.OtherUpdates[0].MessageIDs) != 1 || deleteDiff.OtherUpdates[0].MessageIDs[0] != a.Message.ID {
		t.Fatalf("subscriber delete difference = %+v, want own deleted id %d at pts %d", deleteDiff, a.Message.ID, deleteEvent.Pts)
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

func TestSendPaidMonoforumMessageLedger(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	broadcast, err := store.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: 1, Title: "Paid DM", Broadcast: true, Date: 1_700_002_000})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	enabled, err := store.SetPaidMessagesPrice(ctx, 1, broadcast.Channel.ID, 10, true)
	if err != nil {
		t.Fatalf("enable paid DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	sub := domain.Peer{Type: domain.PeerTypeUser, ID: 42}
	baseMessages := len(store.messages[monoID])

	low := domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 42, SavedPeer: sub, RandomID: 3001, Message: "too low", AllowPaidStars: 9, Date: 1_700_002_001}
	var required *domain.StarsPaymentRequiredError
	if _, err := store.SendMonoforumMessage(ctx, low); !errors.As(err, &required) || required.Stars != 10 {
		t.Fatalf("low authorization err = %v, want 10-Star payment required", err)
	}
	if len(store.messages[monoID]) != baseMessages {
		t.Fatalf("low authorization wrote a message")
	}

	store.starsBalances[42] = 25
	paidReq := domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 42, SavedPeer: sub, RandomID: 3002, Message: "paid", AllowPaidStars: 99, Date: 1_700_002_002}
	paid, err := store.SendMonoforumMessage(ctx, paidReq)
	if err != nil {
		t.Fatalf("paid send: %v", err)
	}
	if paid.Message.PaidMessageStars != 10 || paid.SenderStarsBalance == nil || paid.SenderStarsBalance.Balance != 15 {
		t.Fatalf("paid result = %+v balance=%+v, want actual 10 and balance 15", paid.Message, paid.SenderStarsBalance)
	}
	if store.starsBalances[42] != 15 || store.channelStarsBalances[broadcast.Channel.ID] != 8 {
		t.Fatalf("ledger sender/channel = %d/%d, want 15/8", store.starsBalances[42], store.channelStarsBalances[broadcast.Channel.ID])
	}

	duplicate, err := store.SendMonoforumMessage(ctx, paidReq)
	if err != nil {
		t.Fatalf("paid replay: %v", err)
	}
	if !duplicate.Duplicate || duplicate.Message.ID != paid.Message.ID || duplicate.SenderStarsBalance == nil || duplicate.SenderStarsBalance.Balance != 15 {
		t.Fatalf("paid replay = %+v, want original message and balance 15", duplicate)
	}
	if store.starsBalances[42] != 15 || store.channelStarsBalances[broadcast.Channel.ID] != 8 {
		t.Fatalf("paid replay double charged: sender/channel=%d/%d", store.starsBalances[42], store.channelStarsBalances[broadcast.Channel.ID])
	}

	admin, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: 1, SavedPeer: sub, RandomID: 3003, Message: "free admin reply", AllowPaidStars: 100, Date: 1_700_002_003,
	})
	if err != nil {
		t.Fatalf("admin reply: %v", err)
	}
	if admin.Message.PaidMessageStars != 0 || admin.SenderStarsBalance != nil || store.channelStarsBalances[broadcast.Channel.ID] != 8 {
		t.Fatalf("admin reply charged: message=%+v balance=%+v channel=%d", admin.Message, admin.SenderStarsBalance, store.channelStarsBalances[broadcast.Channel.ID])
	}

	store.starsBalances[99] = 5
	other := domain.Peer{Type: domain.PeerTypeUser, ID: 99}
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: 99, SavedPeer: other, RandomID: 3004, Message: "insufficient", AllowPaidStars: 10, Date: 1_700_002_004,
	}); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("insufficient err = %v, want ErrStarsInsufficient", err)
	}
	if store.starsBalances[99] != 5 || store.channelStarsBalances[broadcast.Channel.ID] != 8 {
		t.Fatalf("insufficient send mutated ledger: sender/channel=%d/%d", store.starsBalances[99], store.channelStarsBalances[broadcast.Channel.ID])
	}
}
