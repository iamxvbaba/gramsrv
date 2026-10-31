package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// TestSendMonoforumMessageAndHistoryPostgres regression for the PG implementation of channel Direct Messages (monoforum) send + history read:
// direct messages are stored in channel_messages' saved_peer dimension and reuse channel pts; subscriber sub-dialogs, idempotency, no crosstalk.
// Gated on TELESRV_TEST_POSTGRES_DSN.
func TestSendMonoforumMessageAndHistoryPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 91, Phone: "+1779" + suffix + "41", FirstName: "MonoMsgOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := users.Create(ctx, domain.User{AccessHash: 92, Phone: "+1779" + suffix + "42", FirstName: "MonoSub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	other, err := users.Create(ctx, domain.User{AccessHash: 93, Phone: "+1779" + suffix + "43", FirstName: "MonoOther"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, sub.ID, other.ID})
	})

	channels := NewChannelStore(pool)
	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "Mono Msg " + suffix, Broadcast: true, Date: 1700001000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelIDs = append(channelIDs, broadcast.Channel.ID)
	enabled, err := channels.SetPaidMessagesPrice(ctx, owner.ID, broadcast.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}
	channelIDs = append(channelIDs, monoID)

	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: sub.ID}
	if _, err := channels.GetChannel(ctx, sub.ID, monoID); err != nil {
		t.Fatalf("subscriber get enabled monoforum without membership: %v", err)
	}
	if _, err := channels.JoinChannel(ctx, monoID, sub.ID, 1700001001); !errors.Is(err, domain.ErrChannelMonoforumUnsupported) {
		t.Fatalf("subscriber join monoforum err = %v, want ErrChannelMonoforumUnsupported", err)
	}
	suggestedDraft := domain.DialogDraft{
		Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}, Message: "pending suggested post", Date: 1700001001,
		SuggestedPost: &domain.SuggestedPost{
			Price:        &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10},
			ScheduleDate: 1700100000,
		},
	}
	dialogStore := NewDialogStore(pool)
	if err := dialogStore.SaveDraft(ctx, sub.ID, suggestedDraft); err != nil {
		t.Fatalf("save subscriber monoforum draft: %v", err)
	}
	loadedDraft, found, err := dialogStore.GetDraft(ctx, sub.ID, suggestedDraft.Peer, 0)
	if err != nil || !found || loadedDraft.SuggestedPost == nil || loadedDraft.SuggestedPost.Price == nil || loadedDraft.SuggestedPost.Price.Amount != 10 || loadedDraft.SuggestedPost.ScheduleDate != 1700100000 {
		t.Fatalf("loaded subscriber monoforum draft = %+v, %v, %v; want suggested post", loadedDraft, found, err)
	}

	suggestedPost := &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}, ScheduleDate: 1700100000}
	forward := &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, Date: 1700000999}
	m1, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 111, Message: "hi", Date: 1700001001,
		SuggestedPost: suggestedPost, Forward: forward,
	})
	if err != nil {
		t.Fatalf("subscriber send 1: %v", err)
	}
	if m1.Message.SavedPeer != subPeer || m1.Message.ChannelID != monoID || m1.Message.Pts == 0 {
		t.Fatalf("m1 = %+v, want saved_peer sub + channel mono + pts>0", m1.Message)
	}
	if len(m1.Recipients) != 2 || !slices.Contains(m1.Recipients, owner.ID) || !slices.Contains(m1.Recipients, sub.ID) {
		t.Fatalf("m1 recipients = %v, want subscriber %d + parent admin %d", m1.Recipients, sub.ID, owner.ID)
	}
	if _, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 112, Message: "again", Date: 1700001002}); err != nil {
		t.Fatalf("subscriber send 2: %v", err)
	}
	if _, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 113, Message: "reply", ReplyTo: &domain.MessageReply{MessageID: m1.Message.ID, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}}, Date: 1700001003}); err != nil {
		t.Fatalf("admin reply: %v", err)
	}

	mainHist, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("main monoforum history: %v", err)
	}
	if mainHist.Count != 1 || len(mainHist.Messages) != 1 {
		t.Fatalf("main monoforum history count=%d len=%d, want only service message", mainHist.Count, len(mainHist.Messages))
	}
	// The monoforum's service message is the creation message (rendered "Direct messages were enabled in this channel."),
	// and paid_messages_price goes only into the parent broadcast channel.
	if action := mainHist.Messages[0].Action; action == nil || action.Type != domain.ChannelActionCreate {
		t.Fatalf("main monoforum action = %+v, want channel_create", action)
	}
	if len(mainHist.Channels) != 1 || mainHist.Channels[0].ID != broadcast.Channel.ID {
		t.Fatalf("main monoforum extra channels = %+v, want parent %d", mainHist.Channels, broadcast.Channel.ID)
	}
	subscriberHist, err := channels.ListChannelHistory(ctx, sub.ID, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("subscriber monoforum history: %v", err)
	}
	if subscriberHist.Count != 3 || len(subscriberHist.Messages) != 3 {
		t.Fatalf("subscriber monoforum history count=%d len=%d, want own 3", subscriberHist.Count, len(subscriberHist.Messages))
	}
	for _, message := range subscriberHist.Messages {
		if message.SavedPeer != subPeer {
			t.Fatalf("subscriber history leaked message %+v", message)
		}
	}

	// Idempotency.
	dup, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 111, Message: "hi", SuggestedPost: suggestedPost, Forward: forward, Date: 1700001004})
	if err != nil {
		t.Fatalf("dup send: %v", err)
	}
	if !dup.Duplicate || dup.Message.ID != m1.Message.ID {
		t.Fatalf("dup = %+v, want duplicate of m1 id %d", dup.Message, m1.Message.ID)
	}
	if _, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 111, Message: "changed", Date: 1700001004}); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("changed monoforum intent err = %v, want ErrMessageRandomIDDuplicate", err)
	}
	var monoforumFingerprint []byte
	if err := pool.QueryRow(ctx, `SELECT request_fingerprint FROM channel_messages WHERE channel_id=$1 AND id=$2`, monoID, m1.Message.ID).Scan(&monoforumFingerprint); err != nil {
		t.Fatalf("load monoforum fingerprint: %v", err)
	}
	if len(monoforumFingerprint) != 32 {
		t.Fatalf("monoforum fingerprint length = %d, want 32", len(monoforumFingerprint))
	}

	// History (saved_peer read back via scanChannelMessage).
	hist, err := channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: monoID, SavedPeer: subPeer, Limit: 10})
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
		if m.SavedPeer != subPeer {
			t.Fatalf("history msg saved_peer = %+v, want sub", m.SavedPeer)
		}
	}
	oldest := hist.Messages[len(hist.Messages)-1]
	if oldest.SuggestedPost == nil || oldest.SuggestedPost.Price == nil || oldest.SuggestedPost.Price.Kind != domain.SuggestedPostPriceStars || oldest.SuggestedPost.Price.Amount != 10 || oldest.SuggestedPost.ScheduleDate != 1700100000 {
		t.Fatalf("persisted suggested post = %+v, want 10 Stars + schedule", oldest.SuggestedPost)
	}
	if oldest.Forward == nil || oldest.Forward.From.ID != owner.ID || oldest.Forward.Date != 1700000999 {
		t.Fatalf("persisted monoforum forward = %+v, want source user %d/date 1700000999", oldest.Forward, owner.ID)
	}
	if newest := hist.Messages[0]; newest.ReplyTo == nil || newest.ReplyTo.MessageID != m1.Message.ID {
		t.Fatalf("persisted admin reply = %+v, want message %d", newest.ReplyTo, m1.Message.ID)
	}
	if _, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 114, Message: "bad reply", ReplyTo: &domain.MessageReply{MessageID: 999999, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: monoID}}, Date: 1700001004}); !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("invalid monoforum reply err = %v, want ErrReplyMessageIDInvalid", err)
	}

	// Another subscriber does not cross conversations.
	otherPeer := domain.Peer{Type: domain.PeerTypeUser, ID: other.ID}
	otherMessage, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: other.ID, SavedPeer: otherPeer, RandomID: 201, Message: "other", Date: 1700001005})
	if err != nil {
		t.Fatalf("other subscriber send: %v", err)
	}
	subHist, _ := channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: monoID, SavedPeer: subPeer, Limit: 10})
	if subHist.Count != 3 {
		t.Fatalf("sub history after other subscriber = %d, want still 3 (no cross-talk)", subHist.Count)
	}
	subscriberChannelHistory, err := channels.ListChannelHistory(ctx, sub.ID, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10})
	if err != nil || subscriberChannelHistory.Count != 3 || len(subscriberChannelHistory.Messages) != 3 {
		t.Fatalf("subscriber channel history after other = %d/%d, %v; want own 3", subscriberChannelHistory.Count, len(subscriberChannelHistory.Messages), err)
	}
	for _, message := range subscriberChannelHistory.Messages {
		if message.SavedPeer != subPeer {
			t.Fatalf("subscriber channel history leaked message %+v", message)
		}
	}
	exactMessages, err := channels.GetChannelMessages(ctx, sub.ID, monoID, []int{m1.Message.ID, otherMessage.Message.ID})
	if err != nil {
		t.Fatalf("subscriber exact monoforum messages: %v", err)
	}
	if len(exactMessages.Messages) != 1 || exactMessages.Messages[0].ID != m1.Message.ID {
		t.Fatalf("subscriber exact monoforum messages = %+v, want only own message %d", exactMessages.Messages, m1.Message.ID)
	}
	monoBeforeViews, err := channels.GetChannelByID(ctx, monoID)
	if err != nil {
		t.Fatalf("get monoforum before views: %v", err)
	}
	subViews, err := channels.GetChannelMessageViews(ctx, domain.ChannelMessageViewsRequest{
		UserID: sub.ID, ChannelID: monoID, IDs: []int{m1.Message.ID, otherMessage.Message.ID},
		Increment: true, Date: 1700001006,
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
	var hiddenViews int
	var hiddenViewer bool
	if err := pool.QueryRow(ctx, `
SELECT m.views_count,
       EXISTS (
           SELECT 1
           FROM channel_message_viewers v
           WHERE v.channel_id = m.channel_id
             AND v.message_id = m.id
             AND v.viewer_user_id = $3
       )
FROM channel_messages m
WHERE m.channel_id = $1 AND m.id = $2`, monoID, otherMessage.Message.ID, sub.ID).Scan(&hiddenViews, &hiddenViewer); err != nil {
		t.Fatalf("load hidden monoforum view state: %v", err)
	}
	if hiddenViews != 0 || hiddenViewer {
		t.Fatalf("hidden monoforum view state = count %d viewer %v, want 0/false", hiddenViews, hiddenViewer)
	}
	repeatedViews, err := channels.GetChannelMessageViews(ctx, domain.ChannelMessageViewsRequest{
		UserID: sub.ID, ChannelID: monoID, IDs: []int{m1.Message.ID},
		Increment: true, Date: 1700001007,
	})
	if err != nil || repeatedViews.Views[m1.Message.ID] != 1 {
		t.Fatalf("repeated subscriber monoforum views = %+v, %v; want idempotent 1", repeatedViews.Views, err)
	}
	adminViews, err := channels.GetChannelMessageViews(ctx, domain.ChannelMessageViewsRequest{
		UserID: owner.ID, ChannelID: monoID, IDs: []int{m1.Message.ID, otherMessage.Message.ID},
		Increment: true, Date: 1700001008,
	})
	if err != nil {
		t.Fatalf("admin get monoforum message views: %v", err)
	}
	if len(adminViews.Views) != 2 || adminViews.Views[m1.Message.ID] != 2 || adminViews.Views[otherMessage.Message.ID] != 1 {
		t.Fatalf("admin monoforum views = %+v, want both saved peers at 2/1", adminViews.Views)
	}
	monoAfterViews, err := channels.GetChannelByID(ctx, monoID)
	if err != nil {
		t.Fatalf("get monoforum after views: %v", err)
	}
	if monoAfterViews.Pts != monoBeforeViews.Pts {
		t.Fatalf("message views advanced monoforum pts = %d, want unchanged %d", monoAfterViews.Pts, monoBeforeViews.Pts)
	}
	if _, err := channels.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{
		UserID: sub.ID, ChannelID: monoID, MessageID: m1.Message.ID,
		Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f44d"}},
		Date:      1700001006,
	}); err != nil {
		t.Fatalf("subscriber react to own monoforum message: %v", err)
	}
	if _, err := channels.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{
		UserID: sub.ID, ChannelID: monoID, MessageID: otherMessage.Message.ID,
		Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f525"}},
		Date:      1700001006,
	}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("subscriber react to another saved_peer err = %v, want ErrMessageIDInvalid", err)
	}
	subReactions, err := channels.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{
		UserID: sub.ID, ChannelID: monoID, IDs: []int{m1.Message.ID, otherMessage.Message.ID},
	})
	if err != nil {
		t.Fatalf("subscriber get monoforum reactions: %v", err)
	}
	if len(subReactions.Messages) != 1 || subReactions.Messages[0].ID != m1.Message.ID {
		t.Fatalf("subscriber monoforum reactions = %+v, want only own message %d", subReactions.Messages, m1.Message.ID)
	}
	adminReactions, err := channels.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{
		UserID: owner.ID, ChannelID: monoID, IDs: []int{m1.Message.ID, otherMessage.Message.ID},
	})
	if err != nil {
		t.Fatalf("admin get monoforum reactions: %v", err)
	}
	if len(adminReactions.Messages) != 2 {
		t.Fatalf("admin monoforum reactions = %+v, want both subscriber messages", adminReactions.Messages)
	}
	reactionList, err := channels.ListChannelMessageReactions(ctx, domain.ChannelMessageReactionsListRequest{
		UserID: sub.ID, ChannelID: monoID, MessageID: m1.Message.ID, Limit: 10,
	})
	if err != nil || reactionList.Count != 1 || len(reactionList.Reactions) != 1 {
		t.Fatalf("subscriber monoforum reaction list = %+v, %v; want one", reactionList, err)
	}
	if _, err := channels.ListChannelMessageReactions(ctx, domain.ChannelMessageReactionsListRequest{
		UserID: sub.ID, ChannelID: monoID, MessageID: otherMessage.Message.ID, Limit: 10,
	}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("subscriber list another saved_peer reactions err = %v, want ErrMessageIDInvalid", err)
	}
	reactionLookup, found, err := channels.FindChannelMessageReaction(ctx, domain.ChannelMessageReactionLookupRequest{
		ViewerUserID: sub.ID, ChannelID: monoID, MessageID: m1.Message.ID, ReactorUserID: sub.ID,
	})
	if err != nil || !found || len(reactionLookup.Reactions) != 1 {
		t.Fatalf("subscriber monoforum reaction lookup = %+v, %v, %v; want one", reactionLookup, found, err)
	}
	if _, _, err := channels.FindChannelMessageReaction(ctx, domain.ChannelMessageReactionLookupRequest{
		ViewerUserID: sub.ID, ChannelID: monoID, MessageID: otherMessage.Message.ID, ReactorUserID: other.ID,
	}); !errors.Is(err, domain.ErrMessageIDInvalid) {
		t.Fatalf("subscriber lookup another saved_peer reaction err = %v, want ErrMessageIDInvalid", err)
	}
	diff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: sub.ID, ChannelID: monoID, Pts: 0, Limit: 100})
	if err != nil {
		t.Fatalf("subscriber channel difference: %v", err)
	}
	if len(diff.NewMessages) != 3 {
		t.Fatalf("subscriber channel difference messages = %d, want own 3", len(diff.NewMessages))
	}
	for _, message := range diff.NewMessages {
		if message.SavedPeer != subPeer {
			t.Fatalf("subscriber difference leaked message %+v", message)
		}
	}
	activeChannelIDs, err := channels.ListActiveChannelIDsForUser(ctx, sub.ID, 0, 10)
	if err != nil || !slices.Contains(activeChannelIDs, monoID) {
		t.Fatalf("subscriber active channels = %v, %v; want monoforum %d", activeChannelIDs, err, monoID)
	}

	// Dedup is per subscriber sub-dialog (migration 0022's unique index includes saved_peer_id): an admin sending the same random_id
	// to two different subscribers must not dedup against each other (consistent with memory behavior).
	a, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 9001, Message: "to sub", Date: 1700001010})
	if err != nil {
		t.Fatalf("dedup send A: %v", err)
	}
	b, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: otherPeer, RandomID: 9001, Message: "to other", Date: 1700001011})
	if err != nil {
		t.Fatalf("dedup send B (cross-sublist same random_id must not collide): %v", err)
	}
	if b.Duplicate || b.Message.ID == a.Message.ID {
		t.Fatalf("cross-sublist same random_id wrongly deduped: a=%d b=%d dup=%v", a.Message.ID, b.Message.ID, b.Duplicate)
	}
	// SavedPeer belongs both to the lookup scope and to the fallback intent.
	// Cross-sublist sends remain legal, while presenting another sublist's
	// fingerprint for an existing scope must be rejected rather than replayed.
	otherIntentFingerprint, err := store.MonoforumSendFingerprint(domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: otherPeer, RandomID: 9001, Message: "to sub",
	})
	if err != nil {
		t.Fatalf("fingerprint mismatched saved peer: %v", err)
	}
	if _, _, err := channels.LookupChannelSendReplay(ctx, domain.ChannelSendReplayRequest{
		ChannelID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 9001, IdempotencyFingerprint: otherIntentFingerprint,
	}); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("mismatched saved-peer fingerprint err = %v, want ErrMessageRandomIDDuplicate", err)
	}
	// A genuine resend in the same sub-dialog is still deduped.
	again, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 9001, Message: "to sub", Date: 1700001012})
	if err != nil {
		t.Fatalf("dedup resend A: %v", err)
	}
	if !again.Duplicate || again.Message.ID != a.Message.ID {
		t.Fatalf("same-sublist retry not deduped: again=%+v want dup of %d", again.Message, a.Message.ID)
	}

	// Subscriber sub-dialog list: two subscribers, ordered by top message id descending (other sent last, so comes first).
	dialogs, err := channels.ListMonoforumDialogs(ctx, domain.MonoforumDialogsFilter{MonoforumID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("list dialogs: %v", err)
	}
	if dialogs.Count != 2 || len(dialogs.Dialogs) != 2 {
		t.Fatalf("dialogs count=%d len=%d, want 2 subscribers", dialogs.Count, len(dialogs.Dialogs))
	}
	if dialogs.Dialogs[0].SavedPeer != otherPeer {
		t.Fatalf("dialogs[0] saved_peer = %+v, want newest 'other'", dialogs.Dialogs[0].SavedPeer)
	}
	if dialogs.Dialogs[1].SavedPeer != subPeer || dialogs.Dialogs[1].TopMessageID == 0 {
		t.Fatalf("dialogs[1] = %+v, want sub with top message", dialogs.Dialogs[1])
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin monoforum delete: %v", err)
	}
	mono, err := getChannelByID(ctx, tx, monoID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("load monoforum for delete: %v", err)
	}
	_, deleteEvent, _, err := channels.deleteChannelMessagesTx(ctx, tx, mono, domain.ChannelMember{ChannelID: monoID, UserID: owner.ID, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive}, []int{a.Message.ID}, owner.ID, 1700001013)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("delete monoforum message: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit monoforum delete: %v", err)
	}
	deleteDiff, err := channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: sub.ID, ChannelID: monoID, Pts: deleteEvent.Pts - deleteEvent.PtsCount, Limit: 10})
	if err != nil {
		t.Fatalf("subscriber difference after own delete: %v", err)
	}
	if deleteDiff.Pts != deleteEvent.Pts || len(deleteDiff.OtherUpdates) != 1 || len(deleteDiff.OtherUpdates[0].MessageIDs) != 1 || deleteDiff.OtherUpdates[0].MessageIDs[0] != a.Message.ID {
		t.Fatalf("subscriber delete difference = %+v, want own deleted id %d at pts %d", deleteDiff, a.Message.ID, deleteEvent.Pts)
	}
	var ptsBeforeReplay, eventsBeforeReplay int
	if err := pool.QueryRow(ctx, `SELECT pts FROM channels WHERE id = $1`, monoID).Scan(&ptsBeforeReplay); err != nil {
		t.Fatalf("load monoforum pts: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_update_events WHERE channel_id = $1`, monoID).Scan(&eventsBeforeReplay); err != nil {
		t.Fatalf("count monoforum events: %v", err)
	}
	deletedReplay, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 9001, Message: "to sub", Date: 1700001014})
	if err != nil {
		t.Fatalf("replay deleted monoforum message: %v", err)
	}
	if !deletedReplay.Duplicate || deletedReplay.Message.ID != a.Message.ID || deletedReplay.Message.Body != "to sub" || deletedReplay.ReplayDeleteEvent == nil || deletedReplay.ReplayDeleteEvent.Pts != deleteEvent.Pts {
		t.Fatalf("deleted monoforum replay = %+v, want first snapshot + durable delete %+v", deletedReplay, deleteEvent)
	}
	var ptsAfterReplay, eventsAfterReplay int
	if err := pool.QueryRow(ctx, `SELECT pts FROM channels WHERE id = $1`, monoID).Scan(&ptsAfterReplay); err != nil {
		t.Fatalf("reload monoforum pts: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_update_events WHERE channel_id = $1`, monoID).Scan(&eventsAfterReplay); err != nil {
		t.Fatalf("recount monoforum events: %v", err)
	}
	if ptsAfterReplay != ptsBeforeReplay || eventsAfterReplay != eventsBeforeReplay {
		t.Fatalf("deleted monoforum replay mutated pts/events = %d/%d, want %d/%d", ptsAfterReplay, eventsAfterReplay, ptsBeforeReplay, eventsBeforeReplay)
	}
}

func TestSendPaidMonoforumMessageLedgerPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 191, Phone: "+1789" + suffix + "41", FirstName: "PaidMonoOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := users.Create(ctx, domain.User{AccessHash: 192, Phone: "+1789" + suffix + "42", FirstName: "PaidMonoSub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	other, err := users.Create(ctx, domain.User{AccessHash: 193, Phone: "+1789" + suffix + "43", FirstName: "PaidMonoOther"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	channels := NewChannelStore(pool)
	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "Paid Mono " + suffix, Broadcast: true, Date: 1700002000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channels.SetPaidMessagesPrice(ctx, owner.ID, broadcast.Channel.ID, 10, true)
	if err != nil {
		t.Fatalf("enable paid DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", []int64{broadcast.Channel.ID, monoID})
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, sub.ID, other.ID})
	})
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, sub.ID, 25, 1700002000); err != nil {
		t.Fatalf("grant subscriber stars: %v", err)
	}
	if _, _, err := stars.EnsureGrant(ctx, other.ID, 5, 1700002000); err != nil {
		t.Fatalf("grant other stars: %v", err)
	}
	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: sub.ID}
	var beforeMessages int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_messages WHERE channel_id=$1`, monoID).Scan(&beforeMessages); err != nil {
		t.Fatalf("count messages before paid send: %v", err)
	}
	lowReq := domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 4001, Message: "too low", AllowPaidStars: 9, Date: 1700002001}
	var required *domain.StarsPaymentRequiredError
	if _, err := channels.SendMonoforumMessage(ctx, lowReq); !errors.As(err, &required) || required.Stars != 10 {
		t.Fatalf("low authorization err = %v, want 10-Star payment required", err)
	}
	var afterLowMessages int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_messages WHERE channel_id=$1`, monoID).Scan(&afterLowMessages); err != nil || afterLowMessages != beforeMessages {
		t.Fatalf("low authorization message count = %d/%v, want %d", afterLowMessages, err, beforeMessages)
	}

	paidReq := domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 4002, Message: "paid", AllowPaidStars: 99, Date: 1700002002}
	paid, err := channels.SendMonoforumMessage(ctx, paidReq)
	if err != nil {
		t.Fatalf("paid send: %v", err)
	}
	if paid.Message.PaidMessageStars != 10 || paid.SenderStarsBalance == nil || paid.SenderStarsBalance.Balance != 15 {
		t.Fatalf("paid result = %+v balance=%+v, want actual 10 and balance 15", paid.Message, paid.SenderStarsBalance)
	}
	var senderBalance, channelBalance, persistedPaid int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, sub.ID).Scan(&senderBalance); err != nil {
		t.Fatalf("load sender balance: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, broadcast.Channel.ID).Scan(&channelBalance); err != nil {
		t.Fatalf("load channel balance: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT paid_message_stars FROM channel_messages WHERE channel_id=$1 AND id=$2`, monoID, paid.Message.ID).Scan(&persistedPaid); err != nil {
		t.Fatalf("load persisted paid stars: %v", err)
	}
	if senderBalance != 15 || channelBalance != 8 || persistedPaid != 10 {
		t.Fatalf("persisted sender/channel/message = %d/%d/%d, want 15/8/10", senderBalance, channelBalance, persistedPaid)
	}

	replay, err := channels.SendMonoforumMessage(ctx, paidReq)
	if err != nil {
		t.Fatalf("paid replay: %v", err)
	}
	if !replay.Duplicate || replay.Message.ID != paid.Message.ID || replay.SenderStarsBalance == nil || replay.SenderStarsBalance.Balance != 15 {
		t.Fatalf("paid replay = %+v, want exact original and balance 15", replay)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, sub.ID).Scan(&senderBalance); err != nil || senderBalance != 15 {
		t.Fatalf("paid replay sender balance = %d/%v, want 15", senderBalance, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, broadcast.Channel.ID).Scan(&channelBalance); err != nil || channelBalance != 8 {
		t.Fatalf("paid replay channel balance = %d/%v, want 8", channelBalance, err)
	}

	admin, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 4003, Message: "free admin reply", AllowPaidStars: 100, Date: 1700002003,
	})
	if err != nil {
		t.Fatalf("admin reply: %v", err)
	}
	if admin.Message.PaidMessageStars != 0 || admin.SenderStarsBalance != nil {
		t.Fatalf("admin reply charged: message=%+v balance=%+v", admin.Message, admin.SenderStarsBalance)
	}

	otherPeer := domain.Peer{Type: domain.PeerTypeUser, ID: other.ID}
	if _, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: other.ID, SavedPeer: otherPeer, RandomID: 4004, Message: "insufficient", AllowPaidStars: 10, Date: 1700002004,
	}); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("insufficient err = %v, want ErrStarsInsufficient", err)
	}
	var otherBalance int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, other.ID).Scan(&otherBalance); err != nil || otherBalance != 5 {
		t.Fatalf("insufficient sender balance = %d/%v, want 5", otherBalance, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, broadcast.Channel.ID).Scan(&channelBalance); err != nil || channelBalance != 8 {
		t.Fatalf("insufficient channel balance = %d/%v, want 8", channelBalance, err)
	}
}
