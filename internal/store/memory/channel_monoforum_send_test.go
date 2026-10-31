package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestSendMonoforumMessageAndHistory verifies channel DM (monoforum) send + per-subscriber
// history reads: a subscriber send and an admin reply land in the same saved_peer
// sub-conversation; idempotent; different subscribers do not cross into each other's conversations.
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
	forward := &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeUser, ID: 1}, Date: 1_700_000_999}

	m1, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 42, SavedPeer: sub, RandomID: 111, Message: "hi", Forward: forward, Date: 1_700_001_001})
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
	// Admin reply: the sender is the creator, and saved_peer is still that subscriber (same sub-conversation).
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
	// The monoforum's service message is the creation message (rendered as
	// "Direct messages were enabled in this channel."); paid_messages_price goes only into the parent broadcast channel.
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

	// Idempotent: the same randomID returns the original message, without a duplicate.
	dup, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 42, SavedPeer: sub, RandomID: 111, Message: "hi", Forward: forward, Date: 1_700_001_004})
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
	if oldest := hist.Messages[len(hist.Messages)-1]; oldest.Forward == nil || oldest.Forward.From.ID != 1 || oldest.Forward.Date != 1_700_000_999 {
		t.Fatalf("persisted monoforum forward = %+v, want source user 1/date 1700000999", oldest.Forward)
	}
	if _, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 1, SavedPeer: sub, RandomID: 114, Message: "cross reply", ReplyTo: &domain.MessageReply{MessageID: 999999, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}}, Date: 1_700_001_004}); !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("invalid monoforum reply err = %v, want ErrReplyMessageIDInvalid", err)
	}
	for _, m := range hist.Messages {
		if m.SavedPeer != sub {
			t.Fatalf("history msg saved_peer = %+v, want sub", m.SavedPeer)
		}
	}

	// Another subscriber's DMs do not cross into this conversation.
	other := domain.Peer{Type: domain.PeerTypeUser, ID: 99}
	otherMessage, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 99, SavedPeer: other, RandomID: 201, Message: "other", Date: 1_700_001_005})
	if err != nil {
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
	exactMessages, err := store.GetChannelMessages(ctx, 42, monoID, []int{m1.Message.ID, otherMessage.Message.ID})
	if err != nil {
		t.Fatalf("subscriber exact monoforum messages: %v", err)
	}
	if len(exactMessages.Messages) != 1 || exactMessages.Messages[0].ID != m1.Message.ID {
		t.Fatalf("subscriber exact monoforum messages = %+v, want only own message %d", exactMessages.Messages, m1.Message.ID)
	}
	ptsBeforeViews := store.channels[monoID].Pts
	subViews, err := store.GetChannelMessageViews(ctx, domain.ChannelMessageViewsRequest{
		UserID: 42, ChannelID: monoID, IDs: []int{m1.Message.ID, otherMessage.Message.ID},
		Increment: true, Date: 1_700_001_006,
	})
	if err != nil {
		t.Fatalf("subscriber get monoforum message views: %v", err)
	}
	if len(subViews.Views) != 1 || subViews.Views[m1.Message.ID] != 1 {
		t.Fatalf("subscriber monoforum views = %+v, want own message %d at 1", subViews.Views, m1.Message.ID)
	}
	if _, ok := subViews.Views[otherMessage.Message.ID]; ok {
		t.Fatalf("subscriber monoforum views leaked other saved_peer message %d", otherMessage.Message.ID)
	}
	repeatedViews, err := store.GetChannelMessageViews(ctx, domain.ChannelMessageViewsRequest{
		UserID: 42, ChannelID: monoID, IDs: []int{m1.Message.ID},
		Increment: true, Date: 1_700_001_007,
	})
	if err != nil || repeatedViews.Views[m1.Message.ID] != 1 {
		t.Fatalf("repeated subscriber monoforum views = %+v, %v; want idempotent 1", repeatedViews.Views, err)
	}
	if got := store.msgViews[monoID][otherMessage.Message.ID]; got != 0 {
		t.Fatalf("hidden saved_peer views = %d, want 0 before admin view", got)
	}
	adminViews, err := store.GetChannelMessageViews(ctx, domain.ChannelMessageViewsRequest{
		UserID: 1, ChannelID: monoID, IDs: []int{m1.Message.ID, otherMessage.Message.ID},
		Increment: true, Date: 1_700_001_008,
	})
	if err != nil {
		t.Fatalf("admin get monoforum message views: %v", err)
	}
	if len(adminViews.Views) != 2 || adminViews.Views[m1.Message.ID] != 2 || adminViews.Views[otherMessage.Message.ID] != 1 {
		t.Fatalf("admin monoforum views = %+v, want both saved peers at 2/1", adminViews.Views)
	}
	if got := store.channels[monoID].Pts; got != ptsBeforeViews {
		t.Fatalf("message views advanced monoforum pts = %d, want unchanged %d", got, ptsBeforeViews)
	}
	if _, err := store.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{
		UserID: 42, ChannelID: monoID, MessageID: m1.Message.ID,
		Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f44d"}},
		Date:      1_700_001_006,
	}); err != nil {
		t.Fatalf("subscriber react to own monoforum message: %v", err)
	}
	if _, err := store.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{
		UserID: 42, ChannelID: monoID, MessageID: otherMessage.Message.ID,
		Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f525"}},
		Date:      1_700_001_006,
	}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("subscriber react to another saved_peer err = %v, want ErrMessageIDInvalid", err)
	}
	subReactions, err := store.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{
		UserID: 42, ChannelID: monoID, IDs: []int{m1.Message.ID, otherMessage.Message.ID},
	})
	if err != nil {
		t.Fatalf("subscriber get monoforum reactions: %v", err)
	}
	if len(subReactions.Messages) != 1 || subReactions.Messages[0].ID != m1.Message.ID {
		t.Fatalf("subscriber monoforum reactions = %+v, want only own message %d", subReactions.Messages, m1.Message.ID)
	}
	adminReactions, err := store.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{
		UserID: 1, ChannelID: monoID, IDs: []int{m1.Message.ID, otherMessage.Message.ID},
	})
	if err != nil {
		t.Fatalf("admin get monoforum reactions: %v", err)
	}
	if len(adminReactions.Messages) != 2 {
		t.Fatalf("admin monoforum reactions = %+v, want both subscriber messages", adminReactions.Messages)
	}
	reactionList, err := store.ListChannelMessageReactions(ctx, domain.ChannelMessageReactionsListRequest{
		UserID: 42, ChannelID: monoID, MessageID: m1.Message.ID, Limit: 10,
	})
	if err != nil || reactionList.Count != 1 || len(reactionList.Reactions) != 1 {
		t.Fatalf("subscriber monoforum reaction list = %+v, %v; want one", reactionList, err)
	}
	if _, err := store.ListChannelMessageReactions(ctx, domain.ChannelMessageReactionsListRequest{
		UserID: 42, ChannelID: monoID, MessageID: otherMessage.Message.ID, Limit: 10,
	}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("subscriber list another saved_peer reactions err = %v, want ErrMessageIDInvalid", err)
	}
	reactionLookup, found, err := store.FindChannelMessageReaction(ctx, domain.ChannelMessageReactionLookupRequest{
		ViewerUserID: 42, ChannelID: monoID, MessageID: m1.Message.ID, ReactorUserID: 42,
	})
	if err != nil || !found || len(reactionLookup.Reactions) != 1 {
		t.Fatalf("subscriber monoforum reaction lookup = %+v, %v, %v; want one", reactionLookup, found, err)
	}
	if _, _, err := store.FindChannelMessageReaction(ctx, domain.ChannelMessageReactionLookupRequest{
		ViewerUserID: 42, ChannelID: monoID, MessageID: otherMessage.Message.ID, ReactorUserID: 99,
	}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("subscriber lookup another saved_peer reaction err = %v, want ErrMessageIDInvalid", err)
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

	// Dedup is scoped to the subscriber sub-conversation: the same sender (here the admin) sending to two different subscribers with the same random_id must not dedup against each other.
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
	// A genuine resend within the same sub-conversation (same random_id) is still deduped.
	again, err := store.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: 1, SavedPeer: sub, RandomID: 9001, Message: "to sub", Date: 1_700_001_012})
	if err != nil {
		t.Fatalf("dedup resend A: %v", err)
	}
	if !again.Duplicate || again.Message.ID != a.Message.ID {
		t.Fatalf("same-sublist retry not deduped: again=%+v want dup of %d", again.Message, a.Message.ID)
	}

	// Subscriber sub-conversation list: two subscribers, in descending top-message id order (other sent last, so it is first).
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
