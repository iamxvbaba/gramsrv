package rpc

import (
	"context"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
	"time"
)

func TestUpdatesDifferenceIncludesReactionMessageAndUpdate(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
	)
	reaction := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f44d"}
	reactions := domain.ChannelMessageReactions{
		CanSeeList: true,
		Results: []domain.ChannelMessageReactionCount{{
			Reaction:    reaction,
			Count:       1,
			ChosenOrder: 1,
		}},
		Recent: []domain.ChannelMessagePeerReaction{{
			UserID:      bobID,
			Reaction:    reaction,
			My:          true,
			ChosenOrder: 1,
			Date:        1700000310,
		}},
	}
	msg := domain.Message{
		ID:          68,
		UID:         7001,
		OwnerUserID: aliceID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
		Date:        1700000300,
		Body:        "rx",
		Reactions:   &reactions,
	}
	got, ok := tgUpdatesDifference(0, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 9, Date: 1700000310},
		Events: []domain.UpdateEvent{{
			UserID:   aliceID,
			Type:     domain.UpdateEventMessageReactions,
			Pts:      9,
			PtsCount: 1,
			Date:     1700000310,
			Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
			Message:  msg,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if len(got.NewMessages) != 1 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference messages/updates = %d/%d, want 1/1", len(got.NewMessages), len(got.OtherUpdates))
	}
	wireMsg, ok := got.NewMessages[0].(*tg.Message)
	if !ok || wireMsg.ID != msg.ID {
		t.Fatalf("message = %T %+v, want message %d", got.NewMessages[0], got.NewMessages[0], msg.ID)
	}
	msgReactions, ok := wireMsg.GetReactions()
	if !ok || len(msgReactions.Results) != 1 || msgReactions.Results[0].Count != 1 || msgReactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("message reactions = %+v set=%v, want chosen reaction", msgReactions, ok)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateMessageReactions)
	if !ok || update.MsgID != msg.ID || len(update.Reactions.Results) != 1 || update.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("reaction update = %T %+v, want update for msg %d", got.OtherUpdates[0], got.OtherUpdates[0], msg.ID)
	}
}

func TestMessagesUpdateSavedReactionTagPersistsAndPushesRefresh(t *testing.T) {
	const userID = int64(1000000001)
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Channels: appchannels.NewService(memory.NewChannelStore()),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	req := &tg.MessagesUpdateSavedReactionTagRequest{
		Reaction: &tg.ReactionEmoji{Emoticon: "\U0001f44d"},
	}
	req.SetTitle("Fav")
	ok, err := r.onMessagesUpdateSavedReactionTag(WithSessionID(WithUserID(context.Background(), userID), 55), req)
	if err != nil || !ok {
		t.Fatalf("update saved reaction tag = %v, %v, want true nil", ok, err)
	}

	got, err := r.onMessagesGetSavedReactionTags(WithUserID(context.Background(), userID), &tg.MessagesGetSavedReactionTagsRequest{})
	if err != nil {
		t.Fatalf("get saved reaction tags: %v", err)
	}
	page, ok := got.(*tg.MessagesSavedReactionTags)
	if !ok || len(page.Tags) != 1 {
		t.Fatalf("saved reaction tags = %T %+v, want one tag", got, got)
	}
	if emoji, ok := page.Tags[0].Reaction.(*tg.ReactionEmoji); !ok || emoji.Emoticon != "\U0001f44d" || page.Tags[0].Title != "Fav" {
		t.Fatalf("saved reaction tag = %+v, want persisted thumb/Fav", page.Tags[0])
	}

	push := sessions.snapshot()
	if push.userID != userID || push.sessionID != 55 || push.messageType != proto.MessageFromServer {
		t.Fatalf("push = user %d exclude session %d type %v, want self/exclude/from_server", push.userID, push.sessionID, push.messageType)
	}
	updates, ok := push.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", push.message)
	}
	if len(updates.Updates) != 1 {
		t.Fatalf("updates = %+v, want one update", updates.Updates)
	}
	if _, ok := updates.Updates[0].(*tg.UpdateSavedReactionTags); !ok {
		t.Fatalf("update = %T, want *tg.UpdateSavedReactionTags", updates.Updates[0])
	}
}

func TestMessagesSendReactionPrivatePeerReturnsReactionUpdate(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
		now    = int64(1700000200)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	req := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		MsgID:    7,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
		Big:      true,
	}
	req.SetReaction(req.Reaction)
	req.SetAddToRecent(true)

	updates, err := r.onMessagesSendReaction(WithUserID(context.Background(), userID), req)
	if err != nil {
		t.Fatalf("messages.sendReaction private: %v", err)
	}
	if messages.setReactionReq.UserID != userID || messages.setReactionReq.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: peerID}) || messages.setReactionReq.MessageID != req.MsgID || !messages.setReactionReq.Big || !messages.setReactionReq.AddToRecent {
		t.Fatalf("set reaction req = %+v, want private peer/message context", messages.setReactionReq)
	}
	got := updates.(*tg.Updates).Updates
	if len(got) != 1 {
		t.Fatalf("updates = %+v, want one reaction update", got)
	}
	update, ok := got[0].(*tg.UpdateMessageReactions)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateMessageReactions", got[0])
	}
	peer, ok := update.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != peerID || update.MsgID != req.MsgID {
		t.Fatalf("update peer/msg = %+v/%d, want peer %d msg %d", update.Peer, update.MsgID, peerID, req.MsgID)
	}
	if len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 1 || update.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("reaction results = %+v, want one chosen reaction", update.Reactions.Results)
	}
}

func TestMessagesSendReactionSuppressesTransientPrivateBigClear(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
		now    = int64(1700000200)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	add := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		MsgID:    7,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
		Big:      true,
	}
	add.SetReaction(add.Reaction)
	if _, err := r.onMessagesSendReaction(WithUserID(context.Background(), userID), add); err != nil {
		t.Fatalf("messages.sendReaction add: %v", err)
	}
	messages.getReactionRes = messages.setReactionRes

	clear := &tg.MessagesSendReactionRequest{
		Peer:  &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		MsgID: 7,
	}
	updates, err := r.onMessagesSendReaction(WithUserID(context.Background(), userID), clear)
	if err != nil {
		t.Fatalf("messages.sendReaction transient clear: %v", err)
	}
	if messages.setReactionCalls != 1 {
		t.Fatalf("SetMessageReactions calls = %d, want only the initial add", messages.setReactionCalls)
	}
	if messages.getReactionReq.OwnerUserID != userID || len(messages.getReactionReq.IDs) != 1 || messages.getReactionReq.IDs[0] != 7 {
		t.Fatalf("GetMessageReactions req = %+v, want current message reload", messages.getReactionReq)
	}
	got := updates.(*tg.Updates).Updates
	if len(got) != 1 {
		t.Fatalf("updates = %+v, want one reaction update", got)
	}
	update, ok := got[0].(*tg.UpdateMessageReactions)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateMessageReactions", got[0])
	}
	if len(update.Reactions.Results) != 1 || update.Reactions.Results[0].Count != 1 || update.Reactions.Results[0].ChosenOrder != 1 {
		t.Fatalf("reaction results = %+v, want preserved chosen reaction", update.Reactions.Results)
	}
}

func TestMessagesSendReactionPrivatePeerAllowsCustomEmoji(t *testing.T) {
	const (
		userID           = int64(1000000001)
		peerID           = int64(1000000002)
		customDocumentID = int64(990001)
	)
	messages := &captureMessages{}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000200, 0)})
	req := &tg.MessagesSendReactionRequest{
		Peer:  &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		MsgID: 7,
	}
	req.SetReaction([]tg.ReactionClass{&tg.ReactionCustomEmoji{DocumentID: customDocumentID}})

	updates, err := r.onMessagesSendReaction(WithUserID(context.Background(), userID), req)
	if err != nil {
		t.Fatalf("messages.sendReaction custom private: %v", err)
	}
	if len(messages.setReactionReq.Reactions) != 1 || messages.setReactionReq.Reactions[0].Type != domain.MessageReactionCustomEmoji || messages.setReactionReq.Reactions[0].DocumentID != customDocumentID {
		t.Fatalf("set reaction req reactions = %+v, want custom document %d", messages.setReactionReq.Reactions, customDocumentID)
	}
	update := updates.(*tg.Updates).Updates[0].(*tg.UpdateMessageReactions)
	reaction, ok := update.Reactions.Results[0].Reaction.(*tg.ReactionCustomEmoji)
	if !ok || reaction.DocumentID != customDocumentID {
		t.Fatalf("update reaction = %T %+v, want custom document %d", update.Reactions.Results[0].Reaction, update.Reactions.Results[0].Reaction, customDocumentID)
	}
}

func TestMessagesSendReactionPrivatePushesViewerLocalMessageID(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700000200)
	)
	reaction := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f44d"}
	aliceReactions := domain.ChannelMessageReactions{
		CanSeeList: true,
		Results: []domain.ChannelMessageReactionCount{{
			Reaction: reaction,
			Count:    1,
		}},
		Recent: []domain.ChannelMessagePeerReaction{{
			SenderUserID: aliceID,
			UserID:       bobID,
			Reaction:     reaction,
			Unread:       true,
			Date:         int(now),
		}},
	}
	bobReactions := domain.ChannelMessageReactions{
		CanSeeList: true,
		Results: []domain.ChannelMessageReactionCount{{
			Reaction:    reaction,
			Count:       1,
			ChosenOrder: 1,
		}},
		Recent: []domain.ChannelMessagePeerReaction{{
			UserID:      bobID,
			Reaction:    reaction,
			My:          true,
			ChosenOrder: 1,
			Date:        int(now),
		}},
	}
	messages := &captureMessages{
		setReactionRes: domain.PrivateMessageReactionsResult{
			Messages: []domain.Message{
				{
					ID:          68,
					UID:         7001,
					OwnerUserID: aliceID,
					Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: bobID},
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
					Date:        int(now),
					Reactions:   &aliceReactions,
				},
				{
					ID:          64,
					UID:         7001,
					OwnerUserID: bobID,
					Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
					Date:        int(now),
					Reactions:   &bobReactions,
				},
			},
			Reactions: bobReactions,
		},
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Messages: messages, Sessions: sessions}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	req := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerUser{UserID: aliceID, AccessHash: 11},
		MsgID:    64,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "\U0001f44d"}},
	}
	req.SetReaction(req.Reaction)

	updates, err := r.onMessagesSendReaction(WithSessionID(WithUserID(context.Background(), bobID), 77), req)
	if err != nil {
		t.Fatalf("messages.sendReaction private: %v", err)
	}
	self := updates.(*tg.Updates).Updates[0].(*tg.UpdateMessageReactions)
	if peer, ok := self.Peer.(*tg.PeerUser); !ok || peer.UserID != aliceID || self.MsgID != 64 {
		t.Fatalf("self update peer/msg = %#v/%d, want alice/msg64", self.Peer, self.MsgID)
	}
	if got := sessions.pushedUserIDs(); len(got) != 2 || got[0] != bobID || got[1] != aliceID {
		t.Fatalf("pushed users = %+v, want bob then alice", got)
	}
	pushed := sessions.snapshot()
	if pushed.userID != aliceID || pushed.sessionID != 77 || pushed.messageType != proto.MessageFromServer {
		t.Fatalf("last push = user %d session %d type %v, want alice/exclude bob/from_server", pushed.userID, pushed.sessionID, pushed.messageType)
	}
	pushedUpdates, ok := pushed.message.(*tg.Updates)
	if !ok || len(pushedUpdates.Updates) != 1 {
		t.Fatalf("pushed message = %T %+v, want one updates container", pushed.message, pushed.message)
	}
	other, ok := pushedUpdates.Updates[0].(*tg.UpdateMessageReactions)
	if !ok {
		t.Fatalf("pushed update = %T, want *tg.UpdateMessageReactions", pushedUpdates.Updates[0])
	}
	peer, ok := other.Peer.(*tg.PeerUser)
	if !ok || peer.UserID != bobID || other.MsgID != 68 {
		t.Fatalf("pushed update peer/msg = %#v/%d, want bob/msg68", other.Peer, other.MsgID)
	}
	if len(other.Reactions.Results) != 1 || other.Reactions.Results[0].Count != 1 || other.Reactions.Results[0].ChosenOrder != 0 {
		t.Fatalf("pushed reaction results = %+v, want one non-chosen reaction", other.Reactions.Results)
	}
	if recent, ok := other.Reactions.GetRecentReactions(); !ok || len(recent) != 1 || !recent[0].Unread || recent[0].My {
		t.Fatalf("pushed recent reactions = %+v set=%v, want one unread non-my reaction", recent, ok)
	}
}

func TestTransientPrivateBigReactionCacheIsBoundedAndExpires(t *testing.T) {
	r := &Router{}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 2002}
	r.rememberTransientPrivateBigReaction(1001, peer, 1, 10)
	if !r.shouldSuppressTransientPrivateReactionClear(1001, peer, 1, 12) {
		t.Fatalf("transient big reaction clear should be suppressed inside window")
	}
	if r.shouldSuppressTransientPrivateReactionClear(1001, peer, 1, 14) {
		t.Fatalf("transient big reaction clear should not be suppressed after expiry")
	}

	var cache transientPrivateBigReactionCache
	for i := 0; i < transientPrivateBigReactionMaxEntries+100; i++ {
		cache.remember(transientPrivateBigReactionKey{
			UserID:    1001,
			PeerID:    int64(2000 + i),
			MessageID: i + 1,
		}, 100+i, 1)
	}
	cache.mu.Lock()
	got := len(cache.entries)
	cache.mu.Unlock()
	if got > transientPrivateBigReactionMaxEntries {
		t.Fatalf("transient cache entries = %d, want <= %d", got, transientPrivateBigReactionMaxEntries)
	}
}
