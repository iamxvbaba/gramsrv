package rpc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type replayPreflightMessages struct {
	*captureMessages
	replays            map[int64]domain.SendPrivateTextResult
	lookupRequests     []domain.PrivateSendReplayRequest
	sendRequests       []domain.SendPrivateTextRequest
	reserveRequests    []domain.AlbumGroupReservationRequest
	reservedGroupedID  int64
	forceSendDuplicate bool
}

func newReplayPreflightMessages() *replayPreflightMessages {
	return &replayPreflightMessages{
		captureMessages:   &captureMessages{},
		replays:           make(map[int64]domain.SendPrivateTextResult),
		reservedGroupedID: 81001,
	}
}

func (s *replayPreflightMessages) LookupPrivateSendReplay(_ context.Context, _ int64, req domain.PrivateSendReplayRequest) (domain.SendPrivateTextResult, bool, error) {
	s.lookupRequests = append(s.lookupRequests, req)
	res, found := s.replays[req.RandomID]
	if found {
		res.Duplicate = true
	}
	return res, found, nil
}

func (s *replayPreflightMessages) SendPrivateText(_ context.Context, _ int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error) {
	s.sendRequests = append(s.sendRequests, req)
	res := privateReplayFixture(req.SenderUserID, req.RecipientUserID, req.RandomID, 100+len(s.sendRequests), req.GroupedID)
	res.Duplicate = s.forceSendDuplicate
	s.replays[req.RandomID] = res
	return res, nil
}

func (s *replayPreflightMessages) ReserveAlbumGroup(_ context.Context, _ int64, req domain.AlbumGroupReservationRequest) (int64, error) {
	s.reserveRequests = append(s.reserveRequests, req)
	return s.reservedGroupedID, nil
}

func privateReplayFixture(senderID, recipientID, randomID int64, messageID int, groupedID int64) domain.SendPrivateTextResult {
	msg := domain.Message{
		ID:          messageID,
		UID:         int64(messageID) + 1000,
		RandomID:    randomID,
		OwnerUserID: senderID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: recipientID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: senderID},
		Date:        1700000000 + messageID,
		Out:         true,
		Body:        "committed",
		GroupedID:   groupedID,
		Pts:         messageID,
	}
	event := domain.UpdateEvent{
		UserID:   senderID,
		Type:     domain.UpdateEventNewMessage,
		Pts:      messageID,
		PtsCount: 1,
		Date:     msg.Date,
		Message:  msg,
	}
	return domain.SendPrivateTextResult{
		SenderMessage: msg,
		SenderEvent:   event,
	}
}

type replayCountingDialogs struct {
	*captureDialogs
	deleteDraftCalls int
}

func (s *replayCountingDialogs) DeleteDraft(_ context.Context, _ int64, peer domain.Peer, topMessageID int) (bool, error) {
	s.deleteDraftCalls++
	s.deletedDraft.peer = peer
	s.deletedDraft.topMessageID = topMessageID
	return true, nil
}

type replaySelectiveFiles struct {
	*fakeFiles
	getPhotoIDs []int64
}

func (f *replaySelectiveFiles) GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error) {
	f.getPhotoIDs = append(f.getPhotoIDs, id)
	return f.fakeFiles.GetPhoto(ctx, id)
}

func TestSendMessageExactReplayPrecedesSaturatedRateLimiter(t *testing.T) {
	const userID = int64(1001)
	messages := newReplayPreflightMessages()
	messages.replays[7001] = privateReplayFixture(userID, userID, 7001, 71, 0)
	limiter := &captureRateLimiter{block: true, retryAfter: 30}
	dialogs := &replayCountingDialogs{captureDialogs: &captureDialogs{}}
	r := New(Config{SendRateLimit: 1, SendRateWindow: time.Minute}, Deps{
		Messages: messages,
		Dialogs:  dialogs,
		Limiter:  limiter,
	}, zaptest.NewLogger(t), clock.System)

	updates, err := r.onMessagesSendMessage(WithUserID(context.Background(), userID), &tg.MessagesSendMessageRequest{
		Peer:       &tg.InputPeerSelf{},
		Message:    "already committed",
		RandomID:   7001,
		ClearDraft: true,
	})
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if _, ok := updates.(*tg.Updates); !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	if len(limiter.calls) != 0 {
		t.Fatalf("limiter calls = %+v, want none for exact replay", limiter.calls)
	}
	if len(messages.sendRequests) != 0 {
		t.Fatalf("send requests = %d, want 0", len(messages.sendRequests))
	}
	if dialogs.deleteDraftCalls != 0 {
		t.Fatalf("draft deletes = %d, want 0 for duplicate", dialogs.deleteDraftCalls)
	}
}

func TestSendMessageConcurrentReplayRaceDoesNotClearDraft(t *testing.T) {
	const userID = int64(1001)
	messages := newReplayPreflightMessages()
	// The read-only preflight misses, then the atomic store fence observes that another request
	// committed the same random_id first and returns Duplicate=true.
	messages.forceSendDuplicate = true
	dialogs := &replayCountingDialogs{captureDialogs: &captureDialogs{}}
	r := New(Config{}, Deps{Messages: messages, Dialogs: dialogs}, zaptest.NewLogger(t), clock.System)

	if _, err := r.onMessagesSendMessage(WithUserID(context.Background(), userID), &tg.MessagesSendMessageRequest{
		Peer:       &tg.InputPeerSelf{},
		Message:    "concurrent exact replay",
		RandomID:   7002,
		ClearDraft: true,
	}); err != nil {
		t.Fatalf("concurrent replay race: %v", err)
	}
	if len(messages.lookupRequests) != 1 || len(messages.sendRequests) != 1 {
		t.Fatalf("lookup/send calls = %d/%d, want preflight miss then one atomic send", len(messages.lookupRequests), len(messages.sendRequests))
	}
	if dialogs.deleteDraftCalls != 0 {
		t.Fatalf("draft deletes = %d, want 0 when store race returns duplicate", dialogs.deleteDraftCalls)
	}
}

func TestSendMediaExactReplayPrecedesMediaResolvers(t *testing.T) {
	const userID = int64(1001)
	tests := []struct {
		name  string
		media tg.InputMediaClass
	}{
		{
			name:  "uploaded photo",
			media: &tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 11, Parts: 1, Name: "gone.jpg"}},
		},
		{
			name:  "referenced photo",
			media: &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: 22, AccessHash: 220}},
		},
		{
			name: "poll",
			media: &tg.InputMediaPoll{Poll: tg.Poll{
				Question: tg.TextWithEntities{Text: "question?", Entities: []tg.MessageEntityClass{}},
				Answers: []tg.PollAnswerClass{
					&tg.PollAnswer{Text: tg.TextWithEntities{Text: "yes", Entities: []tg.MessageEntityClass{}}, Option: []byte{0}},
					&tg.PollAnswer{Text: tg.TextWithEntities{Text: "no", Entities: []tg.MessageEntityClass{}}, Option: []byte{1}},
				},
			}},
		},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			randomID := int64(7100 + i)
			messages := newReplayPreflightMessages()
			messages.replays[randomID] = privateReplayFixture(userID, userID, randomID, 80+i, 0)
			limiter := &captureRateLimiter{block: true, retryAfter: 30}
			r := New(Config{SendRateLimit: 1, SendRateWindow: time.Minute}, Deps{
				Messages: messages,
				Limiter:  limiter,
				// Files and Polls are intentionally nil. Reaching any resolver would fail.
			}, zaptest.NewLogger(t), clock.System)
			req := &tg.MessagesSendMediaRequest{
				Peer:     &tg.InputPeerSelf{},
				Media:    tc.media,
				Message:  "already committed",
				RandomID: randomID,
			}
			if fingerprint, err := sendMediaIdempotencyFingerprint(req); err != nil || len(fingerprint) != 32 {
				t.Fatalf("fingerprint len=%d err=%v, want SHA-256", len(fingerprint), err)
			}
			if _, err := r.onMessagesSendMedia(WithUserID(context.Background(), userID), req); err != nil {
				t.Fatalf("exact replay: %v", err)
			}
			if len(limiter.calls) != 0 {
				t.Fatalf("limiter calls = %+v, want none", limiter.calls)
			}
			if len(messages.sendRequests) != 0 || len(messages.lookupRequests) != 1 {
				t.Fatalf("lookup=%d send=%d, want 1/0", len(messages.lookupRequests), len(messages.sendRequests))
			}
		})
	}
}

func TestSendMultiMediaMixedReplayChargesAndResolvesOnlyAbsentItems(t *testing.T) {
	const userID = int64(1001)
	messages := newReplayPreflightMessages()
	messages.replays[7201] = privateReplayFixture(userID, userID, 7201, 91, messages.reservedGroupedID)
	limiter := &captureRateLimiter{}
	dialogs := &replayCountingDialogs{captureDialogs: &captureDialogs{}}
	files := &replaySelectiveFiles{fakeFiles: &fakeFiles{photos: map[int64]domain.Photo{
		222: {ID: 222, AccessHash: 2220, DCID: 2, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 100, H: 100}}},
	}}}
	r := New(Config{SendRateLimit: 10, SendRateWindow: time.Minute}, Deps{
		Messages: messages,
		Dialogs:  dialogs,
		Files:    files,
		Limiter:  limiter,
	}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesSendMultiMediaRequest{
		Peer:       &tg.InputPeerSelf{},
		ClearDraft: true,
		MultiMedia: []tg.InputSingleMedia{
			{Media: &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: 111, AccessHash: 1110}}, RandomID: 7201, Message: "duplicate"},
			{Media: &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: 222, AccessHash: 2220}}, RandomID: 7202, Message: "new"},
		},
	}

	if _, err := r.onMessagesSendMultiMedia(WithUserID(context.Background(), userID), req); err != nil {
		t.Fatalf("mixed sendMultiMedia: %v", err)
	}
	if len(limiter.calls) != 1 || limiter.calls[0].cost != 1 {
		t.Fatalf("limiter calls = %+v, want one absent-item cost", limiter.calls)
	}
	if !reflect.DeepEqual(files.getPhotoIDs, []int64{222}) {
		t.Fatalf("resolved photo IDs = %v, want only absent item 222", files.getPhotoIDs)
	}
	if len(messages.sendRequests) != 1 || messages.sendRequests[0].RandomID != 7202 {
		t.Fatalf("send requests = %+v, want only random_id 7202", messages.sendRequests)
	}
	if len(messages.reserveRequests) != 1 {
		t.Fatalf("album reservations = %d, want 1", len(messages.reserveRequests))
	}
	if dialogs.deleteDraftCalls != 1 {
		t.Fatalf("draft deletes = %d, want exactly one from first genuinely-new item", dialogs.deleteDraftCalls)
	}

	limiterCalls := len(limiter.calls)
	resolved := append([]int64(nil), files.getPhotoIDs...)
	reservations := len(messages.reserveRequests)
	if _, err := r.onMessagesSendMultiMedia(WithUserID(context.Background(), userID), req); err != nil {
		t.Fatalf("full duplicate sendMultiMedia: %v", err)
	}
	if len(limiter.calls) != limiterCalls {
		t.Fatalf("full duplicate added limiter calls: before=%d after=%d", limiterCalls, len(limiter.calls))
	}
	if !reflect.DeepEqual(files.getPhotoIDs, resolved) {
		t.Fatalf("full duplicate resolved media: before=%v after=%v", resolved, files.getPhotoIDs)
	}
	if len(messages.reserveRequests) != reservations {
		t.Fatalf("full duplicate reservations: before=%d after=%d", reservations, len(messages.reserveRequests))
	}
	if dialogs.deleteDraftCalls != 1 {
		t.Fatalf("full duplicate cleared draft again: calls=%d", dialogs.deleteDraftCalls)
	}
}

func TestForwardReplayPreflightSkipsCommittedSourcesAndLoadsOnlyAbsentIDs(t *testing.T) {
	const userID = int64(1001)
	t.Run("full duplicate tolerates deleted sources", func(t *testing.T) {
		messages := newReplayPreflightMessages()
		messages.replays[7301] = privateReplayFixture(userID, userID, 7301, 101, 0)
		messages.replays[7302] = privateReplayFixture(userID, userID, 7302, 102, 0)
		limiter := &captureRateLimiter{block: true, retryAfter: 30}
		r := New(Config{SendRateLimit: 1, SendRateWindow: time.Minute}, Deps{Messages: messages, Limiter: limiter}, zaptest.NewLogger(t), clock.System)

		if _, err := r.onMessagesForwardMessages(WithUserID(context.Background(), userID), &tg.MessagesForwardMessagesRequest{
			FromPeer: &tg.InputPeerEmpty{},
			ToPeer:   &tg.InputPeerSelf{},
			ID:       []int{41, 42},
			RandomID: []int64{7301, 7302},
		}); err != nil {
			t.Fatalf("full duplicate forward with deleted sources: %v", err)
		}
		if messages.getMessagesCalls != 0 {
			t.Fatalf("GetMessages calls = %d, want 0", messages.getMessagesCalls)
		}
		if len(limiter.calls) != 0 || len(messages.sendRequests) != 0 {
			t.Fatalf("limiter=%v sends=%d, want no side effects", limiter.calls, len(messages.sendRequests))
		}
	})

	t.Run("mixed loads absent IDs only", func(t *testing.T) {
		messages := newReplayPreflightMessages()
		messages.replays[7311] = privateReplayFixture(userID, userID, 7311, 111, 0)
		messages.list = domain.MessageList{Messages: []domain.Message{{
			ID:          52,
			OwnerUserID: userID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 2002},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: 2002},
			Date:        1700000100,
			Body:        "still available",
		}}}
		limiter := &captureRateLimiter{}
		r := New(Config{SendRateLimit: 10, SendRateWindow: time.Minute}, Deps{Messages: messages, Limiter: limiter}, zaptest.NewLogger(t), clock.System)

		if _, err := r.onMessagesForwardMessages(WithUserID(context.Background(), userID), &tg.MessagesForwardMessagesRequest{
			FromPeer: &tg.InputPeerEmpty{},
			ToPeer:   &tg.InputPeerSelf{},
			ID:       []int{51, 52},
			RandomID: []int64{7311, 7312},
		}); err != nil {
			t.Fatalf("mixed forward: %v", err)
		}
		if messages.getMessagesCalls != 1 || !reflect.DeepEqual(messages.getMessagesIDs, [][]int{{52}}) {
			t.Fatalf("GetMessages calls=%d ids=%v, want one [52]", messages.getMessagesCalls, messages.getMessagesIDs)
		}
		if len(limiter.calls) != 1 || limiter.calls[0].cost != 1 {
			t.Fatalf("limiter calls = %+v, want cost 1", limiter.calls)
		}
		if len(messages.sendRequests) != 1 || messages.sendRequests[0].RandomID != 7312 {
			t.Fatalf("send requests = %+v, want only random_id 7312", messages.sendRequests)
		}
	})
}

func TestChannelAndMonoforumExactReplayPrecedesLimiterAndCurrentPermission(t *testing.T) {
	t.Run("channel replay survives sender ban", func(t *testing.T) {
		ctx := context.Background()
		users := memory.NewUserStore()
		owner, err := users.Create(ctx, domain.User{AccessHash: 11, Phone: "15550007001", FirstName: "Owner"})
		if err != nil {
			t.Fatalf("create owner: %v", err)
		}
		member, err := users.Create(ctx, domain.User{AccessHash: 12, Phone: "15550007002", FirstName: "Member"})
		if err != nil {
			t.Fatalf("create member: %v", err)
		}
		channels := appchannels.NewService(memory.NewChannelStore())
		created, err := channels.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
			Title: "Replay Group", MemberUserIDs: []int64{member.ID}, Date: 100,
		})
		if err != nil {
			t.Fatalf("create group: %v", err)
		}
		limiter := &captureRateLimiter{}
		r := New(Config{SendRateLimit: 10, SendRateWindow: time.Minute}, Deps{
			Users:    appusers.NewService(users),
			Channels: channels,
			Limiter:  limiter,
		}, zaptest.NewLogger(t), clock.System)
		req := &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
			Message:  "committed before ban",
			RandomID: 7401,
		}
		memberCtx := WithUserID(ctx, member.ID)
		if _, err := r.onMessagesSendMessage(memberCtx, req); err != nil {
			t.Fatalf("first channel send: %v", err)
		}
		callsBeforeReplay := len(limiter.calls)
		if _, err := channels.EditBanned(ctx, owner.ID, domain.EditChannelBannedRequest{
			ChannelID:   created.Channel.ID,
			Participant: domain.Peer{Type: domain.PeerTypeUser, ID: member.ID},
			BannedRights: domain.ChannelBannedRights{
				ViewMessages: true,
				UntilDate:    1000,
			},
			Date: 101,
		}); err != nil {
			t.Fatalf("ban member: %v", err)
		}
		limiter.block = true
		if _, err := r.onMessagesSendMessage(memberCtx, req); err != nil {
			t.Fatalf("channel exact replay after ban: %v", err)
		}
		if len(limiter.calls) != callsBeforeReplay {
			t.Fatalf("replay limiter calls: before=%d after=%d", callsBeforeReplay, len(limiter.calls))
		}
	})

	t.Run("monoforum replay survives direct-message disable", func(t *testing.T) {
		ctx := context.Background()
		users := memory.NewUserStore()
		owner, err := users.Create(ctx, domain.User{AccessHash: 21, Phone: "15550007101", FirstName: "Owner"})
		if err != nil {
			t.Fatalf("create owner: %v", err)
		}
		subscriber, err := users.Create(ctx, domain.User{AccessHash: 22, Phone: "15550007102", FirstName: "Subscriber"})
		if err != nil {
			t.Fatalf("create subscriber: %v", err)
		}
		channelStore := memory.NewChannelStore()
		channels := appchannels.NewService(channelStore)
		created, err := channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "Replay DM", Broadcast: true, Date: 200})
		if err != nil {
			t.Fatalf("create broadcast: %v", err)
		}
		enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
		if err != nil {
			t.Fatalf("enable direct messages: %v", err)
		}
		monoID := enabled.Channel.LinkedMonoforumID
		limiter := &captureRateLimiter{}
		r := New(Config{SendRateLimit: 10, SendRateWindow: time.Minute}, Deps{
			Users:    appusers.NewService(users),
			Channels: channels,
			Limiter:  limiter,
		}, zaptest.NewLogger(t), clock.System)
		req := &tg.MessagesSendMessageRequest{
			Peer:     &tg.InputPeerChannel{ChannelID: monoID},
			Message:  "committed before disable",
			RandomID: 7411,
		}
		req.SetReplyTo(&tg.InputReplyToMonoForum{MonoforumPeerID: &tg.InputPeerUser{UserID: subscriber.ID}})
		subscriberCtx := WithUserID(ctx, subscriber.ID)
		if _, err := r.onMessagesSendMessage(subscriberCtx, req); err != nil {
			t.Fatalf("first monoforum send: %v", err)
		}
		callsBeforeReplay := len(limiter.calls)
		if _, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, false); err != nil {
			t.Fatalf("disable direct messages: %v", err)
		}
		limiter.block = true
		if _, err := r.onMessagesSendMessage(subscriberCtx, req); err != nil {
			t.Fatalf("monoforum exact replay after disable: %v", err)
		}
		if len(limiter.calls) != callsBeforeReplay {
			t.Fatalf("replay limiter calls: before=%d after=%d", callsBeforeReplay, len(limiter.calls))
		}
	})
}
