package rpc

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/clock"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestOutboxDispatcherPushesNewMessageAndMarksDelivered(t *testing.T) {
	msg := domain.Message{
		ID:          10,
		OwnerUserID: 1000000002,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		Date:        1700000300,
		Body:        "hello",
		Pts:         7,
	}
	outbox := &captureDispatchOutbox{items: []store.DispatchOutboxItem{{
		ID:               55,
		TargetUserID:     msg.OwnerUserID,
		Pts:              msg.Pts,
		EventType:        domain.UpdateEventNewMessage,
		ExcludeSessionID: 99,
	}}}
	events := &captureUpdateEventStore{events: []domain.UpdateEvent{{
		UserID:   msg.OwnerUserID,
		Type:     domain.UpdateEventNewMessage,
		Pts:      msg.Pts,
		PtsCount: 1,
		Date:     msg.Date,
		Message:  msg,
		Users: []domain.User{{
			ID:        msg.From.ID,
			FirstName: "Sender",
		}},
	}}}
	sessions := &captureSessions{}
	metrics := &captureOutboxMetrics{}
	dispatcher := NewOutboxDispatcher(events, outbox, sessions, zaptest.NewLogger(t), WithOutboxMetrics(metrics))
	dispatcher.DispatchOnce(context.Background())

	if !outbox.delivered || outbox.deliveredUserID != msg.OwnerUserID || outbox.deliveredID != 55 {
		t.Fatalf("delivered = %v user=%d id=%d, want outbox delivered", outbox.delivered, outbox.deliveredUserID, outbox.deliveredID)
	}
	if sessions.userID != msg.OwnerUserID || sessions.sessionID != 99 || sessions.messageType != proto.MessageFromServer {
		t.Fatalf("push target = user %d exclude %d type %v, want outbox target/exclude", sessions.userID, sessions.sessionID, sessions.messageType)
	}
	updates, ok := sessions.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", sessions.message)
	}
	if len(updates.Updates) != 1 || len(updates.Users) != 1 {
		t.Fatalf("updates = %+v, want one update and sender user", updates)
	}
	update, ok := updates.Updates[0].(*tg.UpdateNewMessage)
	if !ok || update.Pts != msg.Pts {
		t.Fatalf("update = %#v, want UpdateNewMessage pts=%d", updates.Updates[0], msg.Pts)
	}
	if metrics.claimed != 1 || metrics.delivered != 1 || metrics.failed != 0 {
		t.Fatalf("metrics = claimed %d delivered %d failed %d, want 1/1/0", metrics.claimed, metrics.delivered, metrics.failed)
	}
}

func TestOutboxDispatcherUsesScopedAuthKeyExclusion(t *testing.T) {
	var excludeAuthKeyID [8]byte
	excludeAuthKeyID[0] = 7
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001}
	outbox := &captureDispatchOutbox{items: []store.DispatchOutboxItem{{
		ID:               57,
		TargetUserID:     1000000002,
		Pts:              9,
		EventType:        domain.UpdateEventPeerSettings,
		ExcludeAuthKeyID: excludeAuthKeyID,
		ExcludeSessionID: 99,
	}}}
	events := &captureUpdateEventStore{events: []domain.UpdateEvent{{
		UserID:   1000000002,
		Type:     domain.UpdateEventPeerSettings,
		Pts:      9,
		PtsCount: 1,
		Date:     1700000302,
		Peer:     peer,
	}}}
	sessions := &captureScopedSessions{captureSessions: &captureSessions{}}
	dispatcher := NewOutboxDispatcher(events, outbox, sessions, zaptest.NewLogger(t))
	dispatcher.DispatchOnce(context.Background())

	if sessions.scopedAuthKey() != excludeAuthKeyID || sessions.sessionID != 99 || sessions.userID != 1000000002 {
		t.Fatalf("scoped push = auth %x session %d user %d, want precise outbox exclusion", sessions.scopedAuthKey(), sessions.sessionID, sessions.userID)
	}
}

// TestOutboxDispatcherBatchPath 覆盖生产批量路径：store 同时具备 BatchByCursor + MarkDeliveredBatch
// 时，DispatchOnce 一次批量取事件、推送、再批量标记 delivered，而非逐条。
func TestOutboxDispatcherBatchPath(t *testing.T) {
	msg := domain.Message{
		ID:          10,
		OwnerUserID: 1000000002,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		Date:        1700000300,
		Body:        "hello",
		Pts:         7,
	}
	events := &batchEventStore{captureUpdateEventStore: &captureUpdateEventStore{events: []domain.UpdateEvent{{
		UserID:   msg.OwnerUserID,
		Type:     domain.UpdateEventNewMessage,
		Pts:      msg.Pts,
		PtsCount: 1,
		Date:     msg.Date,
		Message:  msg,
		Users:    []domain.User{{ID: msg.From.ID, FirstName: "Sender"}},
	}}}}
	outbox := &batchDispatchOutbox{captureDispatchOutbox: &captureDispatchOutbox{items: []store.DispatchOutboxItem{{
		ID:               55,
		TargetUserID:     msg.OwnerUserID,
		Pts:              msg.Pts,
		EventType:        domain.UpdateEventNewMessage,
		ExcludeSessionID: 99,
	}}}}
	sessions := &captureSessions{}
	metrics := &captureOutboxMetrics{}
	dispatcher := NewOutboxDispatcher(events, outbox, sessions, zaptest.NewLogger(t), WithOutboxMetrics(metrics))
	dispatcher.DispatchOnce(context.Background())

	if len(events.batchCursors) != 1 || events.batchCursors[0] != (store.EventCursor{UserID: msg.OwnerUserID, Pts: msg.Pts}) {
		t.Fatalf("batch cursors = %+v, want one cursor for (%d,%d)", events.batchCursors, msg.OwnerUserID, msg.Pts)
	}
	if sessions.userID != msg.OwnerUserID || sessions.sessionID != 99 {
		t.Fatalf("push target = user %d exclude %d, want batch push to outbox target", sessions.userID, sessions.sessionID)
	}
	if len(outbox.deliveredBatch) != 1 || outbox.deliveredBatch[0].ID != 55 {
		t.Fatalf("delivered batch = %+v, want one item id=55", outbox.deliveredBatch)
	}
	if outbox.delivered {
		t.Fatalf("batch path should not call per-item MarkDelivered")
	}
	if metrics.claimed != 1 || metrics.delivered != 1 || metrics.failed != 0 {
		t.Fatalf("metrics = claimed %d delivered %d failed %d, want 1/1/0", metrics.claimed, metrics.delivered, metrics.failed)
	}
}

func TestRouterBuildOutboxUpdatesProjectsSenderPerViewerAndCaches(t *testing.T) {
	const (
		senderUserID = int64(1000000001)
		viewerUserID = int64(1000000002)
	)
	projected := domain.User{
		ID:        senderUserID,
		FirstName: "Sender",
		PhotoID:   9301,
		PhotoDCID: 2,
	}
	users := &countingOutboxUsersService{users: map[int64]domain.User{senderUserID: projected}}
	router := New(Config{}, Deps{Users: users}, zaptest.NewLogger(t), clock.System)
	requests := make([]OutboxUpdateRequest, 0, 2)
	for i, pts := range []int{7, 8} {
		msg := domain.Message{
			ID:          10 + i,
			OwnerUserID: viewerUserID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID},
			Date:        1700000300 + i,
			Body:        "hello",
			Pts:         pts,
		}
		requests = append(requests, OutboxUpdateRequest{
			TargetUserID: viewerUserID,
			Event: domain.UpdateEvent{
				UserID:   viewerUserID,
				Type:     domain.UpdateEventNewMessage,
				Pts:      pts,
				PtsCount: 1,
				Date:     msg.Date,
				Message:  msg,
				Users:    []domain.User{{ID: senderUserID, FirstName: "Stale"}},
			},
		})
	}

	updates := router.BuildOutboxUpdates(context.Background(), requests)
	if len(updates) != len(requests) {
		t.Fatalf("updates count = %d, want %d", len(updates), len(requests))
	}
	for i, update := range updates {
		if update == nil || len(update.Users) != 1 {
			t.Fatalf("updates[%d].Users = %+v, want projected sender", i, update)
		}
		user, ok := update.Users[0].(*tg.User)
		if !ok {
			t.Fatalf("updates[%d].Users[0] = %T, want *tg.User", i, update.Users[0])
		}
		if user.FirstName != "Sender" {
			t.Fatalf("updates[%d] user first_name = %q, want projected Sender", i, user.FirstName)
		}
		photo, ok := user.Photo.(*tg.UserProfilePhoto)
		if !ok || photo.PhotoID != projected.PhotoID || photo.DCID != projected.PhotoDCID {
			t.Fatalf("updates[%d] user photo = %#v, want photo_id=%d dc=%d", i, user.Photo, projected.PhotoID, projected.PhotoDCID)
		}
	}
	if len(users.calls) != 1 {
		t.Fatalf("ByIDs calls = %+v, want one batch call for repeated sender", users.calls)
	}
	if users.calls[0].viewerUserID != viewerUserID || !reflect.DeepEqual(users.calls[0].ids, []int64{senderUserID}) {
		t.Fatalf("ByIDs call = %+v, want viewer=%d ids=[%d]", users.calls[0], viewerUserID, senderUserID)
	}
}

func TestRouterBuildOutboxUpdatesSeparatesViewerCache(t *testing.T) {
	const senderUserID = int64(1000000001)
	users := &viewerSpecificOutboxUsersService{}
	router := New(Config{}, Deps{Users: users}, zaptest.NewLogger(t), clock.System)
	requests := []OutboxUpdateRequest{
		{
			TargetUserID: 1000000002,
			Event: domain.UpdateEvent{
				UserID:   1000000002,
				Type:     domain.UpdateEventNewMessage,
				Pts:      7,
				PtsCount: 1,
				Date:     1700000307,
				Message: domain.Message{
					ID:          10,
					OwnerUserID: 1000000002,
					Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID},
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID},
					Date:        1700000307,
					Pts:         7,
				},
			},
		},
		{
			TargetUserID: 1000000003,
			Event: domain.UpdateEvent{
				UserID:   1000000003,
				Type:     domain.UpdateEventNewMessage,
				Pts:      8,
				PtsCount: 1,
				Date:     1700000308,
				Message: domain.Message{
					ID:          11,
					OwnerUserID: 1000000003,
					Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID},
					From:        domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID},
					Date:        1700000308,
					Pts:         8,
				},
			},
		},
	}

	updates := router.BuildOutboxUpdates(context.Background(), requests)
	if len(updates) != 2 || updates[0] == nil || updates[1] == nil {
		t.Fatalf("updates = %+v, want two updates", updates)
	}
	firstUser, ok := updates[0].Users[0].(*tg.User)
	if !ok {
		t.Fatalf("updates[0].Users[0] = %T, want *tg.User", updates[0].Users[0])
	}
	secondUser, ok := updates[1].Users[0].(*tg.User)
	if !ok {
		t.Fatalf("updates[1].Users[0] = %T, want *tg.User", updates[1].Users[0])
	}
	if firstUser.FirstName != "viewer2" || secondUser.FirstName != "viewer3" {
		t.Fatalf("projected users = %q/%q, want viewer-specific names", firstUser.FirstName, secondUser.FirstName)
	}
	wantCalls := []outboxUsersCall{
		{viewerUserID: 1000000002, ids: []int64{senderUserID}},
		{viewerUserID: 1000000003, ids: []int64{senderUserID}},
	}
	if !sameOutboxUsersCalls(users.calls, wantCalls) {
		t.Fatalf("ByIDs calls = %+v, want %+v", users.calls, wantCalls)
	}
}

func TestChannelMessageUpdatesIncludesActionUsers(t *testing.T) {
	const (
		viewerUserID = int64(1000000002)
		senderUserID = int64(1000000001)
		actionUserID = int64(1000000003)
		channelID    = int64(2000000001)
		messageID    = 41
		messageDate  = 1700000330
		messagePts   = 17
	)
	msg := domain.ChannelMessage{
		ID:           messageID,
		ChannelID:    channelID,
		SenderUserID: senderUserID,
		From:         domain.Peer{Type: domain.PeerTypeUser, ID: senderUserID},
		Date:         messageDate,
		Action: &domain.ChannelMessageAction{
			Type:    domain.ChannelActionChatAddUser,
			UserIDs: []int64{actionUserID},
		},
		Pts: messagePts,
	}
	router := New(Config{}, Deps{
		Users: mapUsersService{users: map[int64]domain.User{
			senderUserID: {ID: senderUserID, FirstName: "Sender"},
			actionUserID: {ID: actionUserID, FirstName: "Invitee", PhotoID: 9302, PhotoDCID: 2},
		}},
	}, zaptest.NewLogger(t), clock.System)

	updates := router.channelMessageUpdatesWithPeerCache(context.Background(), viewerUserID, domain.SendChannelMessageResult{
		Channel: domain.Channel{ID: channelID, AccessHash: 44, Title: "Group", Megagroup: true, Date: messageDate},
		Message: msg,
		Event: domain.ChannelUpdateEvent{
			ChannelID: channelID,
			Type:      domain.ChannelUpdateNewMessage,
			Pts:       messagePts,
			PtsCount:  1,
			Date:      messageDate,
			Message:   msg,
		},
	}, 0, newViewerPeerCache(router))
	got := map[int64]*tg.User{}
	for _, user := range updates.Users {
		if u, ok := user.(*tg.User); ok {
			got[u.ID] = u
		}
	}
	if _, ok := got[senderUserID]; !ok {
		t.Fatalf("sender user missing from updates.users: %+v", updates.Users)
	}
	actionUser, ok := got[actionUserID]
	if !ok {
		t.Fatalf("action user missing from updates.users: %+v", updates.Users)
	}
	if photo, ok := actionUser.Photo.(*tg.UserProfilePhoto); !ok || photo.PhotoID != 9302 {
		t.Fatalf("action user photo = %#v, want projected profile photo", actionUser.Photo)
	}
}

func TestOutboxDispatcherBatchPathUsesUpdateBuilder(t *testing.T) {
	items := []store.DispatchOutboxItem{
		{ID: 3, TargetUserID: 1000000003, Pts: 12, EventType: domain.UpdateEventReadHistoryInbox},
		{ID: 1, TargetUserID: 1000000002, Pts: 10, EventType: domain.UpdateEventReadHistoryInbox},
	}
	events := &batchEventStore{captureUpdateEventStore: &captureUpdateEventStore{events: []domain.UpdateEvent{
		{
			UserID:           1000000002,
			Type:             domain.UpdateEventReadHistoryInbox,
			Pts:              10,
			PtsCount:         1,
			Date:             1700000310,
			Peer:             domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
			MaxID:            10,
			StillUnreadCount: 0,
		},
		{
			UserID:           1000000003,
			Type:             domain.UpdateEventReadHistoryInbox,
			Pts:              12,
			PtsCount:         1,
			Date:             1700000312,
			Peer:             domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
			MaxID:            12,
			StillUnreadCount: 0,
		},
	}}}
	outbox := &batchDispatchOutbox{captureDispatchOutbox: &captureDispatchOutbox{items: items}}
	sessions := &orderedOutboxCaptureSessions{}
	var gotRequests []OutboxUpdateRequest
	builder := func(_ context.Context, requests []OutboxUpdateRequest) []*tg.Updates {
		gotRequests = append([]OutboxUpdateRequest(nil), requests...)
		out := make([]*tg.Updates, len(requests))
		for i, req := range requests {
			out[i] = &tg.Updates{
				Updates: []tg.UpdateClass{&tg.UpdateReadHistoryInbox{
					Peer:             &tg.PeerUser{UserID: req.Event.Peer.ID},
					MaxID:            req.Event.MaxID,
					StillUnreadCount: req.Event.StillUnreadCount,
					Pts:              req.Event.Pts,
					PtsCount:         req.Event.PtsCount,
				}},
				Date: req.Event.Date,
			}
		}
		return out
	}
	dispatcher := NewOutboxDispatcher(events, outbox, sessions, zaptest.NewLogger(t), WithOutboxUpdateBuilder(builder))

	dispatcher.DispatchOnce(context.Background())

	if len(gotRequests) != 2 {
		t.Fatalf("builder requests = %+v, want two requests", gotRequests)
	}
	if gotRequests[0].TargetUserID != 1000000002 || gotRequests[0].Event.Pts != 10 || gotRequests[1].TargetUserID != 1000000003 || gotRequests[1].Event.Pts != 12 {
		t.Fatalf("builder requests = %+v, want sorted by target user then pts", gotRequests)
	}
	if got := sessions.pushedPts(); !reflect.DeepEqual(got, []int{10, 12}) {
		t.Fatalf("pushed pts = %v, want builder updates in sorted order", got)
	}
	if len(outbox.deliveredBatch) != 2 {
		t.Fatalf("delivered batch = %+v, want two delivered items", outbox.deliveredBatch)
	}
}

func TestOutboxDispatcherOrdersClaimedItemsByUserPts(t *testing.T) {
	const targetUserID int64 = 1000000002
	items := []store.DispatchOutboxItem{
		{ID: 3, TargetUserID: targetUserID, Pts: 12, EventType: domain.UpdateEventNewMessage},
		{ID: 1, TargetUserID: targetUserID, Pts: 10, EventType: domain.UpdateEventNewMessage},
		{ID: 2, TargetUserID: targetUserID, Pts: 11, EventType: domain.UpdateEventNewMessage},
	}
	events := make([]domain.UpdateEvent, 0, len(items))
	for _, pts := range []int{10, 11, 12} {
		msg := domain.Message{
			ID:          pts,
			OwnerUserID: targetUserID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
			Date:        1700000300 + pts,
			Body:        "ordered",
			Pts:         pts,
		}
		events = append(events, domain.UpdateEvent{
			UserID:   targetUserID,
			Type:     domain.UpdateEventNewMessage,
			Pts:      pts,
			PtsCount: 1,
			Date:     msg.Date,
			Message:  msg,
			Users:    []domain.User{{ID: msg.From.ID, FirstName: "Sender"}},
		})
	}
	eventStore := &batchEventStore{captureUpdateEventStore: &captureUpdateEventStore{events: events}}
	outbox := &batchDispatchOutbox{captureDispatchOutbox: &captureDispatchOutbox{items: items}}
	sessions := &orderedOutboxCaptureSessions{}
	dispatcher := NewOutboxDispatcher(eventStore, outbox, sessions, zaptest.NewLogger(t))

	dispatcher.DispatchOnce(context.Background())

	want := []int{10, 11, 12}
	if got := sessions.pushedPts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("pushed pts = %v, want %v", got, want)
	}
	if got := eventStore.batchCursors; len(got) != len(want) {
		t.Fatalf("batch cursors = %+v, want %d cursors", got, len(want))
	} else {
		for i, cursor := range got {
			if cursor.UserID != targetUserID || cursor.Pts != want[i] {
				t.Fatalf("batch cursor[%d] = %+v, want user=%d pts=%d", i, cursor, targetUserID, want[i])
			}
		}
	}
}

func TestOutboxLogicalShardsAreDisjointAndStable(t *testing.T) {
	for _, workers := range []int{1, 2, 4, 7, 64, outboxLogicalShards} {
		seen := make([]int, outboxLogicalShards)
		for worker := 0; worker < workers; worker++ {
			for _, shard := range logicalShardsForWorker(worker, workers) {
				if shard < 0 || shard >= outboxLogicalShards {
					t.Fatalf("workers=%d worker=%d returned invalid shard %d", workers, worker, shard)
				}
				seen[shard]++
			}
		}
		for shard, owners := range seen {
			if owners != 1 {
				t.Fatalf("workers=%d shard=%d owners=%d, want exactly one", workers, shard, owners)
			}
		}
	}
	if got := normalizedOutboxWorkers(8, false); got != 1 {
		t.Fatalf("non-sharded workers = %d, want 1", got)
	}
	if got := normalizedOutboxWorkers(outboxLogicalShards+100, true); got != outboxLogicalShards {
		t.Fatalf("overprovisioned workers = %d, want clamp %d", got, outboxLogicalShards)
	}
}

func TestOutboxDispatcherBatchFailureBlocksHigherUserPts(t *testing.T) {
	const (
		blockedUser = int64(1000000002)
		otherUser   = int64(1000000003)
	)
	items := []store.DispatchOutboxItem{
		{ID: 12, TargetUserID: blockedUser, Pts: 12, EventType: domain.UpdateEventReadHistoryInbox},
		{ID: 5, TargetUserID: otherUser, Pts: 5, EventType: domain.UpdateEventReadHistoryInbox},
		{ID: 11, TargetUserID: blockedUser, Pts: 11, EventType: domain.UpdateEventReadHistoryInbox},
	}
	events := make([]domain.UpdateEvent, 0, len(items))
	for _, item := range items {
		events = append(events, outboxReadEvent(item.TargetUserID, item.Pts))
	}
	eventStore := &batchEventStore{captureUpdateEventStore: &captureUpdateEventStore{events: events}}
	outbox := &batchDispatchOutbox{captureDispatchOutbox: &captureDispatchOutbox{items: items}}
	sessions := &selectiveFailOutboxSessions{failUserID: blockedUser, failPts: 11}
	dispatcher := NewOutboxDispatcher(eventStore, outbox, sessions, zaptest.NewLogger(t))

	dispatcher.DispatchOnce(context.Background())

	wantAttempts := []outboxPushAttempt{{userID: blockedUser, pts: 11}, {userID: otherUser, pts: 5}}
	if got := sessions.pushAttempts(); !reflect.DeepEqual(got, wantAttempts) {
		t.Fatalf("push attempts = %+v, want %+v (blocked user's pts=12 must not overtake failed pts=11)", got, wantAttempts)
	}
	if !outbox.failed || len(outbox.deliveredBatch) != 1 || outbox.deliveredBatch[0].TargetUserID != otherUser {
		t.Fatalf("outbox failed=%v delivered=%+v, want failed head and only other user delivered", outbox.failed, outbox.deliveredBatch)
	}
}

func TestOutboxDispatcherBatchLoadFallbackStillBlocksHigherUserPts(t *testing.T) {
	const (
		blockedUser = int64(1000000004)
		otherUser   = int64(1000000005)
	)
	items := []store.DispatchOutboxItem{
		{ID: 22, TargetUserID: blockedUser, Pts: 22, EventType: domain.UpdateEventReadHistoryInbox},
		{ID: 21, TargetUserID: blockedUser, Pts: 21, EventType: domain.UpdateEventReadHistoryInbox},
		{ID: 6, TargetUserID: otherUser, Pts: 6, EventType: domain.UpdateEventReadHistoryInbox},
	}
	events := make([]domain.UpdateEvent, 0, len(items))
	for _, item := range items {
		events = append(events, outboxReadEvent(item.TargetUserID, item.Pts))
	}
	eventStore := &failingBatchEventStore{captureUpdateEventStore: &captureUpdateEventStore{events: events}}
	outbox := &batchDispatchOutbox{captureDispatchOutbox: &captureDispatchOutbox{items: items}}
	sessions := &selectiveFailOutboxSessions{failUserID: blockedUser, failPts: 21}
	dispatcher := NewOutboxDispatcher(eventStore, outbox, sessions, zaptest.NewLogger(t))

	dispatcher.DispatchOnce(context.Background())

	wantAttempts := []outboxPushAttempt{{userID: blockedUser, pts: 21}, {userID: otherUser, pts: 6}}
	if got := sessions.pushAttempts(); !reflect.DeepEqual(got, wantAttempts) {
		t.Fatalf("fallback push attempts = %+v, want %+v", got, wantAttempts)
	}
}

func outboxReadEvent(userID int64, pts int) domain.UpdateEvent {
	return domain.UpdateEvent{
		UserID:           userID,
		Type:             domain.UpdateEventReadHistoryInbox,
		Pts:              pts,
		PtsCount:         1,
		Date:             1700000000 + pts,
		Peer:             domain.Peer{Type: domain.PeerTypeUser, ID: 999},
		MaxID:            pts,
		StillUnreadCount: 0,
	}
}

type outboxUsersCall struct {
	viewerUserID int64
	ids          []int64
}

func sameOutboxUsersCalls(got, want []outboxUsersCall) bool {
	if len(got) != len(want) {
		return false
	}
	used := make([]bool, len(want))
	for _, call := range got {
		found := false
		for i, expected := range want {
			if used[i] || call.viewerUserID != expected.viewerUserID || !reflect.DeepEqual(call.ids, expected.ids) {
				continue
			}
			used[i] = true
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

type countingOutboxUsersService struct {
	users map[int64]domain.User
	calls []outboxUsersCall
}

type viewerSpecificOutboxUsersService struct {
	calls []outboxUsersCall
}

func (s *viewerSpecificOutboxUsersService) Self(_ context.Context, userID int64) (domain.User, error) {
	return domain.User{ID: userID, FirstName: "self"}, nil
}

func (s *viewerSpecificOutboxUsersService) ByID(_ context.Context, currentUserID, userID int64) (domain.User, bool, error) {
	return domain.User{ID: userID, FirstName: viewerSpecificName(currentUserID)}, true, nil
}

func (s *viewerSpecificOutboxUsersService) ByIDs(_ context.Context, viewerUserID int64, userIDs []int64) ([]domain.User, error) {
	s.calls = append(s.calls, outboxUsersCall{viewerUserID: viewerUserID, ids: append([]int64(nil), userIDs...)})
	out := make([]domain.User, 0, len(userIDs))
	for _, userID := range userIDs {
		out = append(out, domain.User{ID: userID, FirstName: viewerSpecificName(viewerUserID)})
	}
	return out, nil
}

func viewerSpecificName(viewerUserID int64) string {
	switch viewerUserID {
	case 1000000002:
		return "viewer2"
	case 1000000003:
		return "viewer3"
	default:
		return "viewer"
	}
}

func (s *countingOutboxUsersService) Self(_ context.Context, userID int64) (domain.User, error) {
	if u, ok := s.users[userID]; ok {
		return u, nil
	}
	return domain.User{}, nil
}

func (s *countingOutboxUsersService) ByID(_ context.Context, _ int64, userID int64) (domain.User, bool, error) {
	u, ok := s.users[userID]
	return u, ok, nil
}

func (s *countingOutboxUsersService) ByIDs(_ context.Context, viewerUserID int64, userIDs []int64) ([]domain.User, error) {
	s.calls = append(s.calls, outboxUsersCall{viewerUserID: viewerUserID, ids: append([]int64(nil), userIDs...)})
	out := make([]domain.User, 0, len(userIDs))
	for _, id := range userIDs {
		if u, ok := s.users[id]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func TestOutboxDispatcherUsesBestEffortPush(t *testing.T) {
	msg := domain.Message{
		ID:          10,
		OwnerUserID: 1000000002,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		Date:        1700000300,
		Body:        "hello",
		Pts:         7,
	}
	events := &captureUpdateEventStore{events: []domain.UpdateEvent{{
		UserID:   msg.OwnerUserID,
		Type:     domain.UpdateEventNewMessage,
		Pts:      msg.Pts,
		PtsCount: 1,
		Date:     msg.Date,
		Message:  msg,
		Users:    []domain.User{{ID: msg.From.ID, FirstName: "Sender"}},
	}}}
	outbox := &captureDispatchOutbox{items: []store.DispatchOutboxItem{{
		ID:               55,
		TargetUserID:     msg.OwnerUserID,
		Pts:              msg.Pts,
		EventType:        domain.UpdateEventNewMessage,
		ExcludeSessionID: 99,
	}}}
	sessions := &captureBestEffortSessions{captureSessions: &captureSessions{}}
	dispatcher := NewOutboxDispatcher(events, outbox, sessions, zaptest.NewLogger(t), WithOutboxPushTimeout(50*time.Millisecond))
	dispatcher.DispatchOnce(context.Background())

	if !sessions.bestEffort || sessions.timeout != 50*time.Millisecond {
		t.Fatalf("best-effort push = %v timeout %v, want true/50ms", sessions.bestEffort, sessions.timeout)
	}
	if !outbox.delivered || outbox.failed {
		t.Fatalf("outbox delivered=%v failed=%v, want delivered after accepted best-effort push", outbox.delivered, outbox.failed)
	}
}

type captureBestEffortSessions struct {
	*captureSessions
	bestEffort bool
	timeout    time.Duration
}

func (s *captureBestEffortSessions) PushToUserExceptSessionBestEffort(ctx context.Context, userID, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error) {
	s.bestEffort = true
	s.timeout = timeout
	return s.PushToUserExceptSession(ctx, userID, excludeSessionID, t, msg)
}

type orderedOutboxCaptureSessions struct {
	captureSessions
	pushed []int
}

type outboxPushAttempt struct {
	userID int64
	pts    int
}

type selectiveFailOutboxSessions struct {
	captureSessions
	failUserID int64
	failPts    int
	attempts   []outboxPushAttempt
}

func (s *selectiveFailOutboxSessions) PushToUserExceptSession(_ context.Context, userID, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	pts := 0
	if updates, ok := msg.(*tg.Updates); ok {
		pts = firstOutboxUpdatePts(updates)
	}
	s.attempts = append(s.attempts, outboxPushAttempt{userID: userID, pts: pts})
	if userID == s.failUserID && pts == s.failPts {
		return 0, errors.New("injected outbox push failure")
	}
	return s.captureSessions.PushToUserExceptSession(context.Background(), userID, excludeSessionID, t, msg)
}

func (s *selectiveFailOutboxSessions) pushAttempts() []outboxPushAttempt {
	return append([]outboxPushAttempt(nil), s.attempts...)
}

func (s *orderedOutboxCaptureSessions) PushToUserExceptSession(_ context.Context, userID, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	if updates, ok := msg.(*tg.Updates); ok {
		s.pushed = append(s.pushed, firstOutboxUpdatePts(updates))
	}
	return s.captureSessions.PushToUserExceptSession(context.Background(), userID, excludeSessionID, t, msg)
}

func (s *orderedOutboxCaptureSessions) pushedPts() []int {
	return append([]int(nil), s.pushed...)
}

func firstOutboxUpdatePts(updates *tg.Updates) int {
	if updates == nil || len(updates.Updates) == 0 {
		return 0
	}
	switch update := updates.Updates[0].(type) {
	case *tg.UpdateNewMessage:
		return update.Pts
	case *tg.UpdateReadHistoryInbox:
		return update.Pts
	case *tg.UpdateReadHistoryOutbox:
		return update.Pts
	default:
		return 0
	}
}

// batchEventStore 给 captureUpdateEventStore 加上 BatchByCursor 批量能力。
type batchEventStore struct {
	*captureUpdateEventStore
	batchCursors []store.EventCursor
}

type failingBatchEventStore struct {
	*captureUpdateEventStore
}

func (s *failingBatchEventStore) BatchByCursor(context.Context, []store.EventCursor) ([]domain.UpdateEvent, error) {
	return nil, errors.New("injected batch event load failure")
}

func (s *failingBatchEventStore) ListAfter(_ context.Context, userID int64, pts, limit int) ([]domain.UpdateEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	var next domain.UpdateEvent
	for _, event := range s.events {
		if event.UserID != userID || event.Pts <= pts {
			continue
		}
		if next.Pts == 0 || event.Pts < next.Pts {
			next = event
		}
	}
	if next.Pts == 0 {
		return nil, nil
	}
	return []domain.UpdateEvent{next}, nil
}

func (s *batchEventStore) BatchByCursor(_ context.Context, cursors []store.EventCursor) ([]domain.UpdateEvent, error) {
	s.batchCursors = cursors
	out := make([]domain.UpdateEvent, 0, len(cursors))
	for _, c := range cursors {
		for _, event := range s.events {
			if event.UserID == c.UserID && event.Pts == c.Pts {
				out = append(out, event)
			}
		}
	}
	return out, nil
}

// batchDispatchOutbox 给 captureDispatchOutbox 加上 MarkDeliveredBatch 批量能力。
type batchDispatchOutbox struct {
	*captureDispatchOutbox
	deliveredBatch []store.DispatchOutboxItem
}

func (s *batchDispatchOutbox) MarkDeliveredBatch(_ context.Context, items []store.DispatchOutboxItem) error {
	s.deliveredBatch = append(s.deliveredBatch, items...)
	return nil
}

type captureUpdateEventStore struct {
	events []domain.UpdateEvent
}

func (s *captureUpdateEventStore) Append(context.Context, int64, domain.UpdateEvent) error {
	return nil
}

func (s *captureUpdateEventStore) AppendAllocated(_ context.Context, userID int64, event domain.UpdateEvent) (domain.UpdateEvent, error) {
	if event.PtsCount <= 0 {
		event.PtsCount = 1
	}
	event.UserID = userID
	maxPts := 0
	for _, existing := range s.events {
		if existing.UserID == userID && existing.Pts > maxPts {
			maxPts = existing.Pts
		}
	}
	event.Pts = maxPts + event.PtsCount
	s.events = append(s.events, event)
	return event, nil
}

func (s *captureUpdateEventStore) ListAfter(_ context.Context, _ int64, pts, limit int) ([]domain.UpdateEvent, error) {
	out := make([]domain.UpdateEvent, 0, len(s.events))
	for _, event := range s.events {
		if event.Pts > pts {
			out = append(out, event)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (s *captureUpdateEventStore) Current(context.Context, int64) (int, error) {
	maxPts := 0
	for _, event := range s.events {
		if event.Pts > maxPts {
			maxPts = event.Pts
		}
	}
	return maxPts, nil
}

func (s *captureUpdateEventStore) MaxContiguousPts(context.Context, int64) (int, error) {
	present := make(map[int]struct{}, len(s.events))
	for _, event := range s.events {
		present[event.Pts] = struct{}{}
	}
	contiguous := 0
	for {
		if _, ok := present[contiguous+1]; !ok {
			break
		}
		contiguous++
	}
	return contiguous, nil
}

type captureDispatchOutbox struct {
	items           []store.DispatchOutboxItem
	delivered       bool
	deliveredUserID int64
	deliveredID     int64
	failed          bool
	failedError     string
}

type captureScopedSessions struct {
	*captureSessions
	// scopedMu 保护本层扩展字段：presence 等异步推送 goroutine 会并发写
	// scopedAuthKeyID，测试主 goroutine 并发读（race detector 抓过这里）。
	scopedMu        sync.Mutex
	scopedAuthKeyID [8]byte
	immediatePush   bool
	immediateType   proto.MessageType
	immediateMsg    bin.Encoder
}

func (s *captureScopedSessions) setScopedAuthKeyID(rawAuthKeyID [8]byte) {
	s.scopedMu.Lock()
	s.scopedAuthKeyID = rawAuthKeyID
	s.scopedMu.Unlock()
}

func (s *captureScopedSessions) scopedAuthKey() [8]byte {
	s.scopedMu.Lock()
	defer s.scopedMu.Unlock()
	return s.scopedAuthKeyID
}

func (s *captureScopedSessions) immediatePushSeen() bool {
	s.scopedMu.Lock()
	defer s.scopedMu.Unlock()
	return s.immediatePush
}

func (s *captureScopedSessions) immediatePushSnapshot() (proto.MessageType, bin.Encoder) {
	s.scopedMu.Lock()
	defer s.scopedMu.Unlock()
	return s.immediateType, s.immediateMsg
}

func (s *captureScopedSessions) BindAuthKeyForSession(rawAuthKeyID [8]byte, sessionID int64, authKeyID [8]byte) {
	s.BindAuthKey(sessionID, authKeyID)
	s.setScopedAuthKeyID(rawAuthKeyID)
}

func (s *captureScopedSessions) AuthKeyIDForSession([8]byte, int64) ([8]byte, bool) {
	return s.AuthKeyID(0)
}

func (s *captureScopedSessions) BindUserForAuthKey(rawAuthKeyID [8]byte, sessionID, userID int64) {
	s.BindUser(sessionID, userID)
	s.setScopedAuthKeyID(rawAuthKeyID)
}

func (s *captureScopedSessions) UserIDForAuthKey([8]byte, int64) (int64, bool) {
	return s.UserID(0)
}

func (s *captureScopedSessions) UserIDResolvedForAuthKey([8]byte, int64) (int64, bool) {
	return s.UserIDResolved(0)
}

func (s *captureScopedSessions) SetReceivesUpdatesForAuthKey([8]byte, int64, bool) {}

func (s *captureScopedSessions) PushToSessionForAuthKey(_ context.Context, rawAuthKeyID [8]byte, sessionID int64, t proto.MessageType, msg bin.Encoder) error {
	s.setScopedAuthKeyID(rawAuthKeyID)
	return s.PushToSession(context.Background(), sessionID, t, msg)
}

func (s *captureScopedSessions) PushToSessionForAuthKeyImmediate(_ context.Context, rawAuthKeyID [8]byte, sessionID int64, t proto.MessageType, msg bin.Encoder) error {
	s.scopedMu.Lock()
	s.immediatePush = true
	s.scopedAuthKeyID = rawAuthKeyID
	s.immediateType = t
	s.immediateMsg = msg
	s.scopedMu.Unlock()
	return s.PushToSession(context.Background(), sessionID, t, msg)
}

func (s *captureScopedSessions) PushToUserExceptAuthKeySession(_ context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error) {
	s.setScopedAuthKeyID(excludeAuthKeyID)
	return s.PushToUserExceptSession(context.Background(), userID, excludeSessionID, t, msg)
}

func (s *captureDispatchOutbox) ClaimPending(context.Context, int) ([]store.DispatchOutboxItem, error) {
	items := s.items
	s.items = nil
	return items, nil
}

func (s *captureDispatchOutbox) MarkDelivered(_ context.Context, item store.DispatchOutboxItem) error {
	s.delivered = true
	s.deliveredUserID = item.TargetUserID
	s.deliveredID = item.ID
	return nil
}

func (s *captureDispatchOutbox) MarkFailed(_ context.Context, _ store.DispatchOutboxItem, lastError string) error {
	s.failed = true
	s.failedError = lastError
	return nil
}

func (s *captureDispatchOutbox) DeleteFailed(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}

func TestOutboxDispatcherUsesNoopAsDelivered(t *testing.T) {
	outbox := &captureDispatchOutbox{items: []store.DispatchOutboxItem{{
		ID:           56,
		TargetUserID: 1000000002,
		Pts:          8,
		EventType:    domain.UpdateEventNoop,
	}}}
	events := &captureUpdateEventStore{events: []domain.UpdateEvent{{
		UserID: 1000000002,
		Type:   domain.UpdateEventNoop,
		Pts:    8,
		Date:   1700000301,
	}}}
	metrics := &captureOutboxMetrics{}
	dispatcher := NewOutboxDispatcher(events, outbox, &captureSessions{}, zaptest.NewLogger(t), WithOutboxMetrics(metrics))
	dispatcher.DispatchOnce(context.Background())

	if !outbox.delivered || outbox.failed {
		t.Fatalf("noop delivered=%v failed=%v, want delivered without push", outbox.delivered, outbox.failed)
	}
	if metrics.delivered != 1 {
		t.Fatalf("noop delivered metrics = %d, want 1", metrics.delivered)
	}
}

type captureOutboxMetrics struct {
	claimed   int
	delivered int
	failed    int
}

func (m *captureOutboxMetrics) MessageSend(time.Duration, bool, error) {}

func (m *captureOutboxMetrics) MessageRateLimited(int) {}

func (m *captureOutboxMetrics) OutboxClaimed(count int) {
	m.claimed += count
}

func (m *captureOutboxMetrics) OutboxDelivered(time.Duration) {
	m.delivered++
}

func (m *captureOutboxMetrics) OutboxFailed(error) {
	m.failed++
}

// interruptedBestEffortSessions 模拟 dispatcher context 到期：该中断可安全靠 lease 重试。
type interruptedBestEffortSessions struct {
	*captureSessions
	attempts int
}

func (s *interruptedBestEffortSessions) PushToUserExceptSessionBestEffort(_ context.Context, _ int64, _ int64, _ proto.MessageType, _ bin.Encoder, _ time.Duration) (int, error) {
	s.attempts++
	return 0, context.DeadlineExceeded
}

// TestOutboxDispatcherDefersOnPushInterruption 验证 shutdown/deadline 不把 lane head 误打 failed。
func TestOutboxDispatcherDefersOnPushInterruption(t *testing.T) {
	msg := domain.Message{
		ID:          10,
		OwnerUserID: 1000000002,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		Date:        1700000300,
		Body:        "hello",
		Pts:         7,
	}
	events := &captureUpdateEventStore{events: []domain.UpdateEvent{{
		UserID:   msg.OwnerUserID,
		Type:     domain.UpdateEventNewMessage,
		Pts:      msg.Pts,
		PtsCount: 1,
		Date:     msg.Date,
		Message:  msg,
		Users:    []domain.User{{ID: msg.From.ID, FirstName: "Sender"}},
	}}}
	outbox := &captureDispatchOutbox{items: []store.DispatchOutboxItem{{
		ID:               55,
		TargetUserID:     msg.OwnerUserID,
		Pts:              msg.Pts,
		EventType:        domain.UpdateEventNewMessage,
		ExcludeSessionID: 99,
	}}}
	sessions := &interruptedBestEffortSessions{captureSessions: &captureSessions{}}
	metrics := &captureOutboxMetrics{}
	dispatcher := NewOutboxDispatcher(events, outbox, sessions, zaptest.NewLogger(t), WithOutboxPushTimeout(50*time.Millisecond), WithOutboxMetrics(metrics))
	dispatcher.DispatchOnce(context.Background())

	if sessions.attempts != 1 {
		t.Fatalf("best-effort push attempts = %d, want 1（应走 best-effort 推送路径）", sessions.attempts)
	}
	if outbox.delivered {
		t.Fatalf("outbox delivered=true, want 未投递（中断应保留 dispatching 行靠租约重投）")
	}
	if outbox.failed {
		t.Fatalf("outbox failed=true, want 未失败（context 中断不计入 attempts 升级）")
	}
	if metrics.failed != 0 {
		t.Fatalf("metrics.failed=%d, want 0（context 中断不算投递失败）", metrics.failed)
	}
}

func TestOutboxPushInterruptedRejectsDeterministicErrors(t *testing.T) {
	if !outboxPushInterrupted(context.Canceled) || !outboxPushInterrupted(context.DeadlineExceeded) {
		t.Fatal("context shutdown/deadline must remain retriable")
	}
	if outboxPushInterrupted(errors.New("encode update: invalid constructor")) {
		t.Fatal("deterministic encoding error must fail the lane head instead of lease-retrying forever")
	}
}
