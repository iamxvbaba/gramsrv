package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type captureOfflinePusher struct {
	calls  int
	userID int64
}

func (p *captureOfflinePusher) PushOfflineUpdate(_ context.Context, userID int64, _ domain.UpdateEvent, _ *tg.Updates) {
	p.calls++
	p.userID = userID
}

// offlineSessions is a SessionBinder whose user push reaches zero sessions, the
// signal that the target is offline.
type offlineSessions struct{ *captureSessions }

func (offlineSessions) PushToUserExceptAuthKeySession(context.Context, int64, [8]byte, int64, proto.MessageType, tg.UpdatesClass) (int, error) {
	return 0, nil
}

func outboxOfflineTestCase() (store.UpdateEventStore, store.DispatchOutboxStore, domain.Message) {
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
		ExcludeAuthKeyID: [8]byte{1},
		ExcludeSessionID: 99,
	}}}
	events := &captureUpdateEventStore{events: []domain.UpdateEvent{{
		UserID:   msg.OwnerUserID,
		Type:     domain.UpdateEventNewMessage,
		Pts:      msg.Pts,
		PtsCount: 1,
		Date:     msg.Date,
		Message:  msg,
		Users:    []domain.User{{ID: msg.From.ID, FirstName: "Sender"}},
	}}}
	return events, outbox, msg
}

func TestOutboxDispatcherNotifiesOfflineWhenNoSessions(t *testing.T) {
	events, outbox, msg := outboxOfflineTestCase()
	pusher := &captureOfflinePusher{}
	dispatcher := newTestOutboxDispatcher(events, outbox, offlineSessions{&captureSessions{}}, zaptest.NewLogger(t), WithOfflinePusher(pusher))
	dispatcher.DispatchOnce(context.Background())

	if pusher.calls != 1 || pusher.userID != msg.OwnerUserID {
		t.Fatalf("offline pusher calls=%d user=%d, want 1 for user %d", pusher.calls, pusher.userID, msg.OwnerUserID)
	}
}

func TestOutboxDispatcherSkipsOfflinePushWhenSessionsReached(t *testing.T) {
	events, outbox, _ := outboxOfflineTestCase()
	pusher := &captureOfflinePusher{}
	// captureSessions returns sent=1, i.e. the user is online.
	dispatcher := newTestOutboxDispatcher(events, outbox, &captureSessions{}, zaptest.NewLogger(t), WithOfflinePusher(pusher))
	dispatcher.DispatchOnce(context.Background())

	if pusher.calls != 0 {
		t.Fatalf("offline pusher called %d times for an online user, want 0", pusher.calls)
	}
}
