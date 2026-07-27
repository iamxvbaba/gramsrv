package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
	"time"
)

func TestMessagesUpdateSavedReactionTagPersistsAndPushesRefresh(t *testing.T) {
	userID, users := newReactionTestUsers(t, true)
	sessions := &captureSessions{}
	reaction := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "\U0001f44d"}
	messages := &captureMessages{savedTags: []domain.SavedReactionTag{{
		UserID: userID, Reaction: reaction, Count: 1,
	}}}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    users,
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

func TestMessagesUpdateSavedReactionTagAcceptsCustomEmoji(t *testing.T) {
	userID, users := newReactionTestUsers(t, true)
	custom := domain.MessageReaction{Type: domain.MessageReactionCustomEmoji, DocumentID: 90001}
	messages := &captureMessages{savedTags: []domain.SavedReactionTag{{
		UserID: userID, Reaction: custom, Count: 1,
	}}}
	r := New(Config{}, Deps{Messages: messages, Users: users}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesUpdateSavedReactionTagRequest{
		Reaction: &tg.ReactionCustomEmoji{DocumentID: custom.DocumentID},
	}
	req.SetTitle("Work")
	ok, err := r.onMessagesUpdateSavedReactionTag(WithUserID(context.Background(), userID), req)
	if err != nil || !ok {
		t.Fatalf("rename custom saved tag = %v, %v", ok, err)
	}
	if messages.updatedSavedTag.Reaction.Key() != custom.Key() || messages.updatedSavedTag.Title != "Work" {
		t.Fatalf("updated custom tag = %+v", messages.updatedSavedTag)
	}
}

func newReactionTestUsers(t *testing.T, premium bool) (int64, UsersService) {
	t.Helper()
	users := memory.NewUserStore()
	user, err := users.Create(context.Background(), domain.User{
		Phone:     "+15550000001",
		FirstName: "Reaction",
	})
	if err != nil {
		t.Fatalf("create reaction test user: %v", err)
	}
	if premium {
		if _, err := users.SetPremiumUntil(context.Background(), user.ID, int(time.Now().Add(time.Hour).Unix())); err != nil {
			t.Fatalf("set reaction test premium: %v", err)
		}
	}
	return user.ID, appusers.NewService(users)
}

func TestMessagesSendReactionSavedMessageUsesTagsWithoutPTSBookkeeping(t *testing.T) {
	userID, users := newReactionTestUsers(t, true)
	messages := &captureMessages{}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    users,
		Sessions: sessions,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1_700_000_200, 0)})
	req := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerSelf{},
		MsgID:    7,
		Reaction: []tg.ReactionClass{&tg.ReactionCustomEmoji{DocumentID: 90001}},
	}
	req.SetReaction(req.Reaction)
	got, err := r.onMessagesSendReaction(
		WithSessionID(WithUserID(context.Background(), userID), 72),
		req,
	)
	if err != nil {
		t.Fatalf("send saved tag: %v", err)
	}
	updates, ok := got.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("saved tag result = %T %+v, want one update", got, got)
	}
	update, ok := updates.Updates[0].(*tg.UpdateMessageReactions)
	if !ok || !update.Reactions.ReactionsAsTags || len(update.Reactions.Results) != 1 {
		t.Fatalf("saved tag update = %T %+v, want reactions_as_tags", updates.Updates[0], updates.Updates[0])
	}
	for _, item := range updates.Updates {
		if deleted, ok := item.(*tg.UpdateDeleteMessages); ok {
			t.Fatalf("saved tag emitted fake delete pts bookkeeping: %+v", deleted)
		}
	}
	if messages.setReactionReq.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
		t.Fatalf("saved tag peer = %+v, want self", messages.setReactionReq.Peer)
	}
	push := sessions.snapshot()
	pushed, ok := push.message.(*tg.Updates)
	if push.userID != userID || push.sessionID != 72 || !ok || len(pushed.Updates) != 1 {
		t.Fatalf("saved tag push = user %d exclude %d %T %+v", push.userID, push.sessionID, push.message, push.message)
	}
	pushedReaction, ok := pushed.Updates[0].(*tg.UpdateMessageReactions)
	if !ok || !pushedReaction.Reactions.ReactionsAsTags {
		t.Fatalf("saved tag pushed update = %T %+v, want reactions_as_tags", pushed.Updates[0], pushed.Updates[0])
	}
}

func TestMessagesSendReactionSavedMessageRequiresPremiumButAllowsClear(t *testing.T) {
	userID, users := newReactionTestUsers(t, false)
	messages := &captureMessages{}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    users,
	}, zaptest.NewLogger(t), clock.System)
	add := &tg.MessagesSendReactionRequest{
		Peer:     &tg.InputPeerSelf{},
		MsgID:    8,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "👍"}},
	}
	add.SetReaction(add.Reaction)
	if _, err := r.onMessagesSendReaction(WithUserID(context.Background(), userID), add); !tgerr.Is(err, "PREMIUM_ACCOUNT_REQUIRED") {
		t.Fatalf("non-premium add err = %v, want PREMIUM_ACCOUNT_REQUIRED", err)
	}
	clear := &tg.MessagesSendReactionRequest{Peer: &tg.InputPeerSelf{}, MsgID: 8}
	if _, err := r.onMessagesSendReaction(WithUserID(context.Background(), userID), clear); err != nil {
		t.Fatalf("non-premium clear: %v", err)
	}
}

func TestSavedReactionTagHashMatchesClientShape(t *testing.T) {
	plain := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "❤️"}
	withoutVariation := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "❤"}
	if got, want := messageReactionListHash([]domain.MessageReaction{plain}), messageReactionListHash([]domain.MessageReaction{withoutVariation}); got != want {
		t.Fatalf("emoji variation-selector hash = %d, want normalized %d", got, want)
	}
	tags := []domain.SavedReactionTag{{
		Reaction: plain,
		Title:    "Love",
		Count:    3,
	}}
	full := savedReactionTagsFromDomain(tags, 0, true)
	page, ok := full.(*tg.MessagesSavedReactionTags)
	if !ok || page.Hash == 0 || len(page.Tags) != 1 || page.Tags[0].Title != "Love" {
		t.Fatalf("saved tag page = %T %+v", full, full)
	}
	if page.Hash != -4770309592622053821 {
		t.Fatalf("saved tag client hash = %d, want -4770309592622053821", page.Hash)
	}
	if cached := savedReactionTagsFromDomain(tags, page.Hash, true); cached == nil {
		t.Fatal("cached saved tag result is nil")
	} else if _, ok := cached.(*tg.MessagesSavedReactionTagsNotModified); !ok {
		t.Fatalf("cached saved tag result = %T, want not modified", cached)
	}
	perPeer := savedReactionTagsFromDomain(tags, 0, false)
	peerPage, ok := perPeer.(*tg.MessagesSavedReactionTags)
	if !ok || peerPage.Tags[0].Title != "" || peerPage.Hash == page.Hash {
		t.Fatalf("per-peer saved tags = %T %+v, want title omitted and scope hash", perPeer, perPeer)
	}
}

func TestMessageFilterFromSearchRequestParsesSavedTagsAndPeer(t *testing.T) {
	const userID = int64(1000000001)
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesSearchRequest{
		Peer:    &tg.InputPeerSelf{},
		Q:       "needle",
		MinDate: 100,
		MaxDate: 200,
		Limit:   50,
		Filter:  &tg.InputMessagesFilterEmpty{},
	}
	req.SetSavedPeerID(&tg.InputPeerSelf{})
	req.SetSavedReaction([]tg.ReactionClass{
		&tg.ReactionEmoji{Emoticon: "👍"},
		&tg.ReactionCustomEmoji{DocumentID: 90001},
	})
	filter, err := r.messageFilterFromSearchRequest(WithUserID(context.Background(), userID), userID, req)
	if err != nil {
		t.Fatalf("parse saved search filter: %v", err)
	}
	if filter.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) ||
		filter.SavedPeer != filter.Peer || len(filter.SavedReactions) != 2 ||
		filter.MinDate != 100 || filter.MaxDate != 200 {
		t.Fatalf("saved search filter = %+v", filter)
	}

	req.Peer = &tg.InputPeerUser{UserID: userID + 1, AccessHash: 1}
	if _, err := r.messageFilterFromSearchRequest(WithUserID(context.Background(), userID), userID, req); !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("non-self saved search err = %v, want PEER_ID_INVALID", err)
	}

	emptyTagReq := &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerUser{UserID: userID + 1, AccessHash: 1},
		Q:      "ordinary",
		Filter: &tg.InputMessagesFilterEmpty{},
		Limit:  20,
	}
	emptyTagReq.SetSavedReaction([]tg.ReactionClass{})
	ordinary, err := r.messageFilterFromSearchRequest(WithUserID(context.Background(), userID), userID, emptyTagReq)
	if err != nil {
		t.Fatalf("empty saved reaction on ordinary peer search: %v", err)
	}
	if !ordinary.HasPeer ||
		ordinary.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: userID + 1}) ||
		len(ordinary.SavedReactions) != 0 {
		t.Fatalf("ordinary peer filter with empty saved reaction = %+v", ordinary)
	}
}

func TestMessagesGetDefaultTagReactionsReturnsHashableCatalog(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)
	got, err := r.onMessagesGetDefaultTagReactions(ctx, 0)
	if err != nil {
		t.Fatalf("get default tag reactions: %v", err)
	}
	page, ok := got.(*tg.MessagesReactions)
	if !ok || page.Hash == 0 || len(page.Reactions) == 0 {
		t.Fatalf("default tags = %T %+v, want non-empty hashable catalog", got, got)
	}
	cached, err := r.onMessagesGetDefaultTagReactions(ctx, page.Hash)
	if err != nil {
		t.Fatalf("get cached default tags: %v", err)
	}
	if _, ok := cached.(*tg.MessagesReactionsNotModified); !ok {
		t.Fatalf("cached default tags = %T, want not modified", cached)
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
