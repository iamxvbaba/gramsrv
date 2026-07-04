package updates

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestRecordNewMessageFeedsGetDifference(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 1
	svc := NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore())
	msg := domain.Message{
		ID:          10,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000000,
		Body:        "Login code: 12345",
	}

	event, state, err := svc.RecordNewMessage(ctx, authKeyID, msg.OwnerUserID, msg)
	if err != nil {
		t.Fatalf("RecordNewMessage: %v", err)
	}
	if event.Pts != 1 || event.PtsCount != 1 || state.Pts != 1 || state.Seq != 0 {
		t.Fatalf("event/state = %+v / %+v, want first pts event with seq=0", event, state)
	}

	diff, err := svc.GetDifference(ctx, authKeyID, msg.OwnerUserID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if diff.State != state || len(diff.Events) != 1 || diff.Events[0].Message.ID != msg.ID {
		t.Fatalf("diff = %+v, want recorded login message event and state %+v", diff, state)
	}

	diff, err = svc.GetDifference(ctx, authKeyID, msg.OwnerUserID, state)
	if err != nil {
		t.Fatalf("GetDifference current: %v", err)
	}
	if len(diff.Events) != 0 || diff.State != state {
		t.Fatalf("current diff = %+v, want empty events and same state", diff)
	}
}

func TestPublishNewMessageIsIdempotentByMessageBoxID(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 11
	events := memory.NewUpdateEventStore()
	svc := NewService(memory.NewUpdateStateStore(), events)
	msg := domain.Message{
		ID:          10,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000000,
		Body:        "Login code: 12345",
	}

	firstEvent, firstState, err := svc.PublishNewMessage(ctx, msg.OwnerUserID, msg)
	if err != nil {
		t.Fatalf("PublishNewMessage first: %v", err)
	}
	secondEvent, secondState, err := svc.PublishNewMessage(ctx, msg.OwnerUserID, msg)
	if err != nil {
		t.Fatalf("PublishNewMessage retry: %v", err)
	}
	if firstEvent.Pts != 1 || firstState.Pts != 1 {
		t.Fatalf("first event/state = %+v / %+v, want pts=1", firstEvent, firstState)
	}
	if secondEvent.Pts != firstEvent.Pts || secondState.Pts != firstState.Pts {
		t.Fatalf("retry event/state = %+v / %+v, want same pts as first %+v / %+v", secondEvent, secondState, firstEvent, firstState)
	}

	diff, err := svc.GetDifference(ctx, authKeyID, msg.OwnerUserID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if diff.State.Pts != 1 || len(diff.Events) != 1 || diff.Events[0].Message.ID != msg.ID {
		t.Fatalf("diff = %+v, want one durable login message event", diff)
	}
}

func TestRecordReadHistoryFeedsGetDifference(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 2
	svc := NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore())
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}

	ownerUserID := int64(1000000001)
	event, state, err := svc.RecordReadHistory(ctx, authKeyID, ownerUserID, domain.ReadHistoryResult{
		OwnerUserID: ownerUserID,
		Peer:        peer,
		MaxID:       10,
		Changed:     true,
	}, 0)
	if err != nil {
		t.Fatalf("RecordReadHistory: %v", err)
	}
	if event.Type != domain.UpdateEventReadHistoryInbox || event.Pts != 1 || event.PtsCount != 1 || state.Pts != 1 {
		t.Fatalf("event/state = %+v / %+v, want read history event with first pts", event, state)
	}

	diff, err := svc.GetDifference(ctx, authKeyID, ownerUserID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if diff.State != state || len(diff.Events) != 1 || diff.Events[0].Peer != peer || diff.Events[0].MaxID != 10 {
		t.Fatalf("diff = %+v, want recorded read history event and state %+v", diff, state)
	}
}

func TestRecordChannelReadHistoryKeepsChannelPtsPayload(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 7
	svc := NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore())
	ownerUserID := int64(1000000001)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 7001}

	event, state, err := svc.RecordReadHistory(ctx, authKeyID, ownerUserID, domain.ReadHistoryResult{
		OwnerUserID:      ownerUserID,
		Peer:             peer,
		MaxID:            11,
		StillUnreadCount: 3,
		ChannelPts:       77,
		Changed:          true,
	}, 0)
	if err != nil {
		t.Fatalf("RecordReadHistory: %v", err)
	}
	if event.Pts != 1 || state.Pts != 1 || event.ChannelPts != 77 {
		t.Fatalf("event/state = %+v / %+v, want account pts=1 and channel pts payload=77", event, state)
	}

	diff, err := svc.GetDifference(ctx, authKeyID, ownerUserID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if len(diff.Events) != 1 || diff.Events[0].Peer != peer || diff.Events[0].ChannelPts != 77 {
		t.Fatalf("diff = %+v, want recorded channel read payload with channel pts=77", diff)
	}
}

func TestRecordSettingsEventsFeedGetDifference(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 3
	svc := NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore())
	ownerUserID := int64(1000000001)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}

	if _, _, err := svc.RecordContactsReset(ctx, authKeyID, ownerUserID, 0); err != nil {
		t.Fatalf("RecordContactsReset: %v", err)
	}
	if _, _, err := svc.RecordDialogPinned(ctx, authKeyID, ownerUserID, peer, true, 0, 0); err != nil {
		t.Fatalf("RecordDialogPinned: %v", err)
	}
	order := []domain.Peer{peer}
	if _, _, err := svc.RecordPinnedDialogs(ctx, authKeyID, ownerUserID, 0, order, 0); err != nil {
		t.Fatalf("RecordPinnedDialogs: %v", err)
	}
	if _, _, err := svc.RecordDialogUnreadMark(ctx, authKeyID, ownerUserID, peer, false, 0); err != nil {
		t.Fatalf("RecordDialogUnreadMark: %v", err)
	}
	settings := domain.PeerSettings{ShareContact: true}
	if _, _, err := svc.RecordPeerSettings(ctx, authKeyID, ownerUserID, peer, settings, 0); err != nil {
		t.Fatalf("RecordPeerSettings: %v", err)
	}
	stateEvent, state, err := svc.RecordPeerStoryBlocked(ctx, authKeyID, ownerUserID, peer, true, 0)
	if err != nil {
		t.Fatalf("RecordPeerStoryBlocked: %v", err)
	}
	if stateEvent.Pts != 6 || state.Pts != 6 {
		t.Fatalf("last event/state = %+v / %+v, want pts=6", stateEvent, state)
	}

	diff, err := svc.GetDifference(ctx, authKeyID, ownerUserID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if diff.State.Pts != 6 || len(diff.Events) != 6 {
		t.Fatalf("diff = %+v, want six settings events", diff)
	}
	wantTypes := []domain.UpdateEventType{
		domain.UpdateEventContactsReset,
		domain.UpdateEventDialogPinned,
		domain.UpdateEventPinnedDialogs,
		domain.UpdateEventDialogUnreadMark,
		domain.UpdateEventPeerSettings,
		domain.UpdateEventPeerStoryBlocked,
	}
	for i, typ := range wantTypes {
		if diff.Events[i].Type != typ || diff.Events[i].Pts != i+1 || diff.Events[i].PtsCount != 1 {
			t.Fatalf("event[%d] = %+v, want type=%s pts=%d pts_count=1", i, diff.Events[i], typ, i+1)
		}
	}
	if diff.Events[1].Peer != peer || !diff.Events[1].Bool {
		t.Fatalf("dialog pinned event = %+v, want peer and pinned=true", diff.Events[1])
	}
	if diff.Events[3].Peer != peer || diff.Events[3].Bool {
		t.Fatalf("unread mark event = %+v, want peer and unread=false", diff.Events[3])
	}
	if len(diff.Events[2].Peers) != 1 || diff.Events[2].Peers[0] != peer {
		t.Fatalf("pinned dialogs event = %+v, want order peer", diff.Events[2])
	}
	if diff.Events[4].Peer != peer || !diff.Events[4].Settings.ShareContact {
		t.Fatalf("peer settings event = %+v, want peer and settings", diff.Events[4])
	}
	if diff.Events[5].Peer != peer || !diff.Events[5].Bool {
		t.Fatalf("peer story blocked event = %+v, want peer and blocked=true", diff.Events[5])
	}
}

func TestRecordSettingsEventUsesDispatchAppender(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 4
	events := &captureDispatchAppender{UpdateEventStore: memory.NewUpdateEventStore()}
	svc := NewService(memory.NewUpdateStateStore(), events)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}

	event, state, err := svc.RecordDialogPinned(ctx, authKeyID, 1000000001, peer, true, 0, 42)
	if err != nil {
		t.Fatalf("RecordDialogPinned: %v", err)
	}
	if event.Pts != 1 || state.Pts != 1 {
		t.Fatalf("event/state = %+v / %+v, want first pts", event, state)
	}
	if !events.dispatched || events.excludeAuthKeyID != authKeyID || events.excludeSessionID != 42 || events.event.Type != domain.UpdateEventDialogPinned || events.event.Peer != peer {
		t.Fatalf("dispatch capture = %+v exclude_auth=%v exclude_session=%d dispatched=%v, want dialog_pinned outbox", events.event, events.excludeAuthKeyID, events.excludeSessionID, events.dispatched)
	}
}

func TestRecordSettingsEventDispatchFailureDoesNotRecordEvent(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 6
	events := &failingDispatchAppender{UpdateEventStore: memory.NewUpdateEventStore()}
	svc := NewService(memory.NewUpdateStateStore(), events)

	_, _, err := svc.RecordDialogPinned(ctx, authKeyID, 1000000001, domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}, true, 0, 42)
	if !errors.Is(err, errDispatchFailed) {
		t.Fatalf("RecordDialogPinned err = %v, want dispatch failure", err)
	}
	diff, err := svc.GetDifference(ctx, authKeyID, 1000000001, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if diff.State.Pts != 0 || len(diff.Events) != 0 {
		t.Fatalf("diff after dispatch failure = %+v, want no durable event before allocated append commits", diff)
	}
}

func TestRecordPeerStoryBlockedUsesDispatchAppender(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 7
	events := &captureDispatchAppender{UpdateEventStore: memory.NewUpdateEventStore()}
	svc := NewService(memory.NewUpdateStateStore(), events)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}

	event, state, err := svc.RecordPeerStoryBlocked(ctx, authKeyID, 1000000001, peer, true, 91)
	if err != nil {
		t.Fatalf("RecordPeerStoryBlocked: %v", err)
	}
	if event.Pts != 1 || state.Pts != 1 || !event.LacksWirePts() {
		t.Fatalf("event/state = %+v / %+v, want first aux pts event", event, state)
	}
	if !events.dispatched || events.excludeAuthKeyID != authKeyID || events.excludeSessionID != 91 || events.event.Type != domain.UpdateEventPeerStoryBlocked || events.event.Peer != peer || !events.event.Bool {
		t.Fatalf("dispatch capture = %+v exclude_auth=%v exclude_session=%d dispatched=%v, want peer_story_blocked outbox", events.event, events.excludeAuthKeyID, events.excludeSessionID, events.dispatched)
	}
}

func TestRecordStoryUsesDispatchAppenderExcludeCurrentSession(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{8, 1, 0}
	events := &captureDispatchAppender{UpdateEventStore: memory.NewUpdateEventStore()}
	svc := NewService(memory.NewUpdateStateStore(), events)
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	story := domain.Story{
		Owner:      owner,
		ID:         3,
		Date:       1700000100,
		ExpireDate: 1700086500,
		Public:     true,
		Caption:    "owner story",
	}

	event, state, err := svc.RecordStory(ctx, authKeyID, owner.ID, story, 1234)
	if err != nil {
		t.Fatalf("RecordStory: %v", err)
	}
	if event.Type != domain.UpdateEventStory || event.Pts != 1 || event.PtsCount != 1 || state.Pts != 1 {
		t.Fatalf("event/state = %+v / %+v, want first story pts event", event, state)
	}
	if !events.dispatched || events.userID != owner.ID || events.excludeAuthKeyID != authKeyID || events.excludeSessionID != 1234 {
		t.Fatalf("dispatch capture = user %d exclude_auth=%v exclude_session=%d dispatched=%v, want current session excluded", events.userID, events.excludeAuthKeyID, events.excludeSessionID, events.dispatched)
	}
	if events.event.Type != domain.UpdateEventStory || events.event.Peer != owner || events.event.Story.ID != story.ID {
		t.Fatalf("dispatch event = %+v, want story update for owner story", events.event)
	}
}

func TestRecordStoryReadAndSentReactionExcludeCurrentSession(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{8, 1, 4}
	events := &captureDispatchAppender{UpdateEventStore: memory.NewUpdateEventStore()}
	svc := NewService(memory.NewUpdateStateStore(), events)
	viewerID := int64(1000000002)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	story := domain.Story{
		Owner:      peer,
		ID:         5,
		Date:       1700000200,
		ExpireDate: 1700086600,
		Public:     true,
	}

	event, state, err := svc.RecordReadStories(ctx, authKeyID, viewerID, domain.StoryReadResult{
		ViewerID:  viewerID,
		Peer:      peer,
		MaxReadID: story.ID,
		Advanced:  true,
		Date:      1700000201,
	}, 2233)
	if err != nil {
		t.Fatalf("RecordReadStories: %v", err)
	}
	if event.Type != domain.UpdateEventReadStories || event.Pts != 1 || state.Pts != 1 {
		t.Fatalf("read event/state = %+v / %+v, want first read story pts event", event, state)
	}
	if !events.dispatched || events.userID != viewerID || events.excludeAuthKeyID != authKeyID || events.excludeSessionID != 2233 || events.event.Type != domain.UpdateEventReadStories || events.event.MaxID != story.ID {
		t.Fatalf("read dispatch capture = %+v user %d exclude_auth=%v exclude_session=%d dispatched=%v, want current session excluded", events.event, events.userID, events.excludeAuthKeyID, events.excludeSessionID, events.dispatched)
	}

	reaction := &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "🔥"}
	event, state, err = svc.RecordSentStoryReaction(ctx, authKeyID, viewerID, domain.StoryReactionResult{
		ViewerID: viewerID,
		Peer:     peer,
		StoryID:  story.ID,
		Story:    story,
		Reaction: reaction,
		Changed:  true,
		Date:     1700000202,
	}, 2233)
	if err != nil {
		t.Fatalf("RecordSentStoryReaction: %v", err)
	}
	if event.Type != domain.UpdateEventSentStoryReaction || event.Pts != 2 || state.Pts != 2 {
		t.Fatalf("reaction event/state = %+v / %+v, want second sent story reaction pts event", event, state)
	}
	if !events.dispatched || events.userID != viewerID || events.excludeAuthKeyID != authKeyID || events.excludeSessionID != 2233 || events.event.Type != domain.UpdateEventSentStoryReaction || events.event.Reaction == nil || events.event.Reaction.Emoticon != "🔥" {
		t.Fatalf("reaction dispatch capture = %+v user %d exclude_auth=%v exclude_session=%d dispatched=%v, want current session excluded", events.event, events.userID, events.excludeAuthKeyID, events.excludeSessionID, events.dispatched)
	}
}

func TestRecordNewStoryReactionDispatchesWithoutSavingDeviceState(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 5
	states := &captureStateStore{}
	events := &captureDispatchAppender{UpdateEventStore: memory.NewUpdateEventStore()}
	svc := NewService(states, events)
	ownerID := int64(1000000001)
	viewerID := int64(1000000002)
	reaction := &domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}

	event, state, err := svc.RecordNewStoryReaction(ctx, authKeyID, 0, domain.StoryReactionResult{
		ViewerID: viewerID,
		Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: ownerID},
		StoryID:  3,
		Story: domain.Story{
			Owner: domain.Peer{Type: domain.PeerTypeUser, ID: ownerID},
			ID:    3,
			Date:  1700000100,
		},
		Reaction: reaction,
		Date:     1700000101,
	}, 0)
	if err != nil {
		t.Fatalf("RecordNewStoryReaction: %v", err)
	}
	if event.Type != domain.UpdateEventNewStoryReaction || event.UserID != ownerID || event.Peer.ID != viewerID || event.Reaction == nil || event.Reaction.Emoticon != "👍" {
		t.Fatalf("event = %+v, want owner-side new story reaction from viewer", event)
	}
	if state.Pts != 1 || state.Seq != 0 {
		t.Fatalf("state = %+v, want first account pts", state)
	}
	if states.saveCount != 0 {
		t.Fatalf("state saves = %d, want no device state save for remote owner notification", states.saveCount)
	}
	if !events.dispatched || events.userID != ownerID || events.event.Type != domain.UpdateEventNewStoryReaction {
		t.Fatalf("dispatch capture = %+v user=%d dispatched=%v, want owner outbox event", events.event, events.userID, events.dispatched)
	}

	diff, err := svc.GetDifference(ctx, authKeyID, ownerID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if diff.State.Pts != 1 || len(diff.Events) != 1 || diff.Events[0].Type != domain.UpdateEventNewStoryReaction {
		t.Fatalf("diff = %+v, want one durable new story reaction", diff)
	}
}

func TestClearAuthKeyDropsStateAndEvents(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 8
	states := memory.NewUpdateStateStore()
	events := memory.NewUpdateEventStore()
	svc := NewService(states, events)
	msg := domain.Message{
		ID:          1,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		Date:        1700000000,
	}
	if _, _, err := svc.RecordNewMessage(ctx, authKeyID, msg.OwnerUserID, msg); err != nil {
		t.Fatalf("RecordNewMessage: %v", err)
	}
	if err := svc.ClearAuthKey(ctx, authKeyID); err != nil {
		t.Fatalf("ClearAuthKey: %v", err)
	}
	diff, err := svc.GetDifference(ctx, authKeyID, msg.OwnerUserID, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if diff.State.Pts != 1 || len(diff.Events) != 1 {
		t.Fatalf("difference after clear = %+v, want durable user events to remain", diff)
	}
	diff, err = svc.GetDifference(ctx, authKeyID, msg.OwnerUserID+1, domain.UpdateState{})
	if err != nil {
		t.Fatalf("GetDifference other user: %v", err)
	}
	if diff.State.Pts != 0 || len(diff.Events) != 0 {
		t.Fatalf("difference for other user after clear = %+v, want no cross-account events", diff)
	}
}

func TestDeleteMessagesPtsRangeFeedsGetDifference(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 9
	userID := int64(1000000001)
	events := memory.NewUpdateEventStore()
	svc := NewService(memory.NewUpdateStateStore(), events)
	for _, event := range []domain.UpdateEvent{
		{UserID: userID, Type: domain.UpdateEventNewMessage, Pts: 1, PtsCount: 1, Date: 1700000001, Message: domain.Message{ID: 1, OwnerUserID: userID}},
		{UserID: userID, Type: domain.UpdateEventNewMessage, Pts: 2, PtsCount: 1, Date: 1700000002, Message: domain.Message{ID: 2, OwnerUserID: userID}},
		{UserID: userID, Type: domain.UpdateEventDeleteMessages, Pts: 4, PtsCount: 2, Date: 1700000003, MessageIDs: []int{1, 2}},
	} {
		if err := events.Append(ctx, userID, event); err != nil {
			t.Fatalf("append event pts=%d: %v", event.Pts, err)
		}
	}

	state, err := svc.GetState(ctx, authKeyID, userID)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if state.Pts != 4 {
		t.Fatalf("state = %+v, want contiguous pts=4 across delete range", state)
	}
	diff, err := svc.GetDifference(ctx, authKeyID, userID, domain.UpdateState{Pts: 2})
	if err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	if diff.State.Pts != 4 || len(diff.Events) != 1 {
		t.Fatalf("diff = %+v, want one delete event ending at pts=4", diff)
	}
	got := diff.Events[0]
	if got.Type != domain.UpdateEventDeleteMessages || got.Pts != 4 || got.PtsCount != 2 || len(got.MessageIDs) != 2 {
		t.Fatalf("delete event = %+v, want pts=4 pts_count=2 ids", got)
	}
}

// TestAcknowledgeCurrentStateAdvancesConfirmedWatermark 验证 updates.getState
// 的语义：返回账号当前最新连续 pts（而非设备旧确认水位），并把确认水位推进
// 到此——TDesktop 不持久化 pts，启动靠 getState+getDialogs 快照对齐，返回旧
// 水位会诱导其重放快照前差分（未读重复累计、dialog 预览被旧消息抢占）。
func TestAcknowledgeCurrentStateAdvancesConfirmedWatermark(t *testing.T) {
	ctx := context.Background()
	var authKeyID [8]byte
	authKeyID[0] = 11
	userID := int64(1000000001)
	events := memory.NewUpdateEventStore()
	svc := NewService(memory.NewUpdateStateStore(), events)
	if err := events.Append(ctx, userID, domain.UpdateEvent{
		UserID: userID, Type: domain.UpdateEventNewMessage, Pts: 1, PtsCount: 1,
		Date: 1700000001, Message: domain.Message{ID: 1, OwnerUserID: userID},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// 设备确认水位停在 pts=1 后账号又推进两格。
	if _, err := svc.GetDifference(ctx, authKeyID, userID, domain.UpdateState{Pts: 1}); err != nil {
		t.Fatalf("GetDifference: %v", err)
	}
	for pts := 2; pts <= 3; pts++ {
		if err := events.Append(ctx, userID, domain.UpdateEvent{
			UserID: userID, Type: domain.UpdateEventNewMessage, Pts: pts, PtsCount: 1,
			Date: 1700000001 + pts, Message: domain.Message{ID: pts, OwnerUserID: userID},
		}); err != nil {
			t.Fatalf("append pts=%d: %v", pts, err)
		}
	}

	st, err := svc.AcknowledgeCurrentState(ctx, authKeyID, userID)
	if err != nil {
		t.Fatalf("AcknowledgeCurrentState: %v", err)
	}
	if st.Pts != 3 {
		t.Fatalf("acknowledged state pts = %d, want account current 3", st.Pts)
	}
	confirmed, err := svc.GetState(ctx, authKeyID, userID)
	if err != nil {
		t.Fatalf("GetState after acknowledge: %v", err)
	}
	if confirmed.Pts != 3 {
		t.Fatalf("confirmed watermark = %d, want advanced to 3", confirmed.Pts)
	}
}

type captureDispatchAppender struct {
	*memory.UpdateEventStore
	dispatched       bool
	userID           int64
	event            domain.UpdateEvent
	excludeAuthKeyID [8]byte
	excludeSessionID int64
}

func (s *captureDispatchAppender) AppendAllocatedWithDispatch(ctx context.Context, userID int64, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, error) {
	s.dispatched = true
	s.userID = userID
	s.excludeAuthKeyID = excludeAuthKeyID
	s.excludeSessionID = excludeSessionID
	event, err := s.UpdateEventStore.AppendAllocated(ctx, userID, event)
	s.event = event
	return event, err
}

var errDispatchFailed = errors.New("dispatch failed")

type failingDispatchAppender struct {
	*memory.UpdateEventStore
}

func (s *failingDispatchAppender) AppendAllocatedWithDispatch(context.Context, int64, domain.UpdateEvent, [8]byte, int64) (domain.UpdateEvent, error) {
	return domain.UpdateEvent{}, errDispatchFailed
}

type captureStateStore struct {
	saveCount int
	states    map[[16]byte]domain.UpdateState
}

func (s *captureStateStore) Get(_ context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, bool, error) {
	if s.states == nil {
		return domain.UpdateState{}, false, nil
	}
	st, ok := s.states[captureStateKey(authKeyID, userID)]
	return st, ok, nil
}

func (s *captureStateStore) Save(_ context.Context, authKeyID [8]byte, userID int64, state domain.UpdateState) error {
	if s.states == nil {
		s.states = make(map[[16]byte]domain.UpdateState)
	}
	s.saveCount++
	s.states[captureStateKey(authKeyID, userID)] = state
	return nil
}

func (s *captureStateStore) Delete(_ context.Context, authKeyID [8]byte, userID int64) error {
	if s.states != nil {
		delete(s.states, captureStateKey(authKeyID, userID))
	}
	return nil
}

func (s *captureStateStore) DeleteAuthKey(_ context.Context, authKeyID [8]byte) error {
	if s.states == nil {
		return nil
	}
	for key := range s.states {
		var got [8]byte
		copy(got[:], key[:8])
		if got == authKeyID {
			delete(s.states, key)
		}
	}
	return nil
}

func captureStateKey(authKeyID [8]byte, userID int64) [16]byte {
	var key [16]byte
	copy(key[:8], authKeyID[:])
	for i := 0; i < 8; i++ {
		key[8+i] = byte(userID >> (8 * i))
	}
	return key
}
