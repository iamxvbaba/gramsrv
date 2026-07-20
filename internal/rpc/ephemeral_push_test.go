package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type ephemeralPushChannels struct {
	ChannelsService
	view  domain.ChannelView
	calls int
}

func (s *ephemeralPushChannels) ResolveChannel(context.Context, int64, int64) (domain.ChannelView, error) {
	s.calls++
	return s.view, nil
}

type ephemeralPushSessions struct {
	SessionBinder
	OnlineUserProvider
	mu         sync.Mutex
	online     bool
	broadcasts []ephemeralPushCapture
	targeted   []ephemeralPushCapture
}

type ephemeralPushCapture struct {
	userID   int64
	authKey  [8]byte
	minLayer int
	message  tg.UpdatesClass
}

func (s *ephemeralPushSessions) IsUserOnline(int64) bool { return s.online }

func (s *ephemeralPushSessions) PushToUserTransientAtLeastLayer(_ context.Context, userID int64, minLayer int, _ proto.MessageType, message tg.UpdatesClass, _ time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcasts = append(s.broadcasts, ephemeralPushCapture{userID: userID, minLayer: minLayer, message: message})
	return 1, nil
}

func (s *ephemeralPushSessions) PushToUserAuthKeyTransientAtLeastLayer(_ context.Context, userID int64, authKey [8]byte, minLayer int, _ proto.MessageType, message tg.UpdatesClass, _ time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targeted = append(s.targeted, ephemeralPushCapture{userID: userID, authKey: authKey, minLayer: minLayer, message: message})
	return 1, nil
}

func (s *ephemeralPushSessions) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.broadcasts), len(s.targeted)
}

type inMemoryEphemeralBroker struct {
	mu          sync.Mutex
	subscribers []func(context.Context, store.EphemeralPush)
	registered  chan struct{}
	published   []store.EphemeralPush
}

func newInMemoryEphemeralBroker() *inMemoryEphemeralBroker {
	return &inMemoryEphemeralBroker{registered: make(chan struct{}, 8)}
}

func (b *inMemoryEphemeralBroker) PublishEphemeralPush(ctx context.Context, event store.EphemeralPush) error {
	b.mu.Lock()
	b.published = append(b.published, event)
	handlers := append([]func(context.Context, store.EphemeralPush){}, b.subscribers...)
	b.mu.Unlock()
	for _, handler := range handlers {
		handler(ctx, event)
	}
	return nil
}

func (b *inMemoryEphemeralBroker) SubscribeEphemeralPushes(ctx context.Context, handler func(context.Context, store.EphemeralPush)) error {
	b.mu.Lock()
	b.subscribers = append(b.subscribers, handler)
	b.mu.Unlock()
	b.registered <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

func TestEphemeralPushMultiInstanceSourceDedupAndLayerRouting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := newInMemoryEphemeralBroker()
	users := mapUsersService{users: map[int64]domain.User{
		1001: {ID: 1001, FirstName: "Bot", Bot: true},
		2001: {ID: 2001, FirstName: "Alice"},
	}}
	view := domain.ChannelView{
		Channel: domain.Channel{ID: 3001, AccessHash: 7, Title: "Group", Megagroup: true},
		Self:    domain.ChannelMember{ChannelID: 3001, UserID: 2001, Status: domain.ChannelMemberActive},
	}
	channels1, channels2 := &ephemeralPushChannels{view: view}, &ephemeralPushChannels{view: view}
	sessions1, sessions2 := &ephemeralPushSessions{online: true}, &ephemeralPushSessions{online: true}
	r1 := New(Config{InstanceID: "one"}, Deps{Users: users, Channels: channels1, Sessions: sessions1, EphemeralPush: broker}, zaptest.NewLogger(t), clock.System)
	r2 := New(Config{InstanceID: "two"}, Deps{Users: users, Channels: channels2, Sessions: sessions2, EphemeralPush: broker}, zaptest.NewLogger(t), clock.System)
	go r1.RunEphemeralPushSubscriber(ctx)
	go r2.RunEphemeralPushSubscriber(ctx)
	for range 2 {
		select {
		case <-broker.registered:
		case <-time.After(time.Second):
			t.Fatal("subscriber did not register")
		}
	}

	now := time.Now()
	message := domain.EphemeralMessage{
		ID: 77, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001},
		SenderUserID: 1001, ReceiverUserID: 2001, Date: int(now.Unix()), RandomID: 78,
		Content: domain.EphemeralContent{Message: "private"}, PayloadHash: [32]byte{1}, Version: 1,
		CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralMessageRetention),
	}
	r1.publishEphemeralPush(ctx, store.EphemeralPush{Kind: store.EphemeralPushNew, TargetUserID: 2001, Message: message})
	if broadcast, targeted := sessions1.counts(); broadcast != 1 || targeted != 0 {
		t.Fatalf("source delivery broadcast=%d targeted=%d", broadcast, targeted)
	}
	if broadcast, targeted := sessions2.counts(); broadcast != 1 || targeted != 0 {
		t.Fatalf("remote delivery broadcast=%d targeted=%d", broadcast, targeted)
	}
	if sessions1.broadcasts[0].minLayer != 228 || sessions2.broadcasts[0].minLayer != 228 {
		t.Fatalf("min layers source=%d remote=%d", sessions1.broadcasts[0].minLayer, sessions2.broadcasts[0].minLayer)
	}
	if len(broker.published) != 1 || broker.published[0].SourceID != "one" {
		t.Fatalf("published=%+v", broker.published)
	}

	key := [8]byte{9, 8, 7}
	message.Deleted = true
	message.Version++
	message.Content = domain.EphemeralContent{}
	r2.deliverEphemeralPushLocal(ctx, store.EphemeralPush{
		Kind: store.EphemeralPushDelete, TargetUserID: 2001,
		TargetBusinessAuthKey: key, Message: message, Date: int(time.Now().Unix()),
	})
	_, targeted := sessions2.counts()
	if targeted != 1 || sessions2.targeted[0].authKey != key || sessions2.targeted[0].minLayer != 228 {
		t.Fatalf("targeted=%+v", sessions2.targeted)
	}
	deletedUpdates, ok := sessions2.targeted[0].message.(*tg.Updates)
	if !ok || deletedUpdates.Seq != 0 || len(deletedUpdates.Updates) != 1 {
		t.Fatalf("delete updates=%#v", sessions2.targeted[0].message)
	}
	deleted, ok := deletedUpdates.Updates[0].(*tg.UpdateDeleteEphemeralMessages)
	if !ok || len(deleted.IDs) != 1 || deleted.IDs[0] != message.ID {
		t.Fatalf("delete update=%#v", deletedUpdates.Updates[0])
	}
}

func TestEphemeralMessageUpdatesAreTransientAndPtsFree(t *testing.T) {
	now := time.Now()
	router := New(Config{}, Deps{
		Users: mapUsersService{users: map[int64]domain.User{
			1001: {ID: 1001, FirstName: "Bot", Bot: true},
			2001: {ID: 2001, FirstName: "Alice"},
		}},
		Channels: &ephemeralPushChannels{view: domain.ChannelView{
			Channel: domain.Channel{ID: 3001, AccessHash: 7, Title: "Group", Megagroup: true},
			Self:    domain.ChannelMember{ChannelID: 3001, UserID: 2001, Status: domain.ChannelMemberActive},
		}},
	}, zaptest.NewLogger(t), clock.System)
	message := domain.EphemeralMessage{
		ID: 77, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001},
		SenderUserID: 1001, ReceiverUserID: 2001, Date: int(now.Unix()), RandomID: 78,
		Content: domain.EphemeralContent{Message: "private"}, PayloadHash: [32]byte{1}, Version: 1,
		CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralMessageRetention),
	}
	updates, err := router.ephemeralMessageUpdates(context.Background(), 2001, message, false)
	if err != nil || updates.Seq != 0 || len(updates.Updates) != 1 {
		t.Fatalf("updates=%#v err=%v", updates, err)
	}
	if _, ok := updates.Updates[0].(*tg.UpdateNewEphemeralMessage); !ok {
		t.Fatalf("update type=%T", updates.Updates[0])
	}
	deleted := ephemeralDeleteUpdates(domain.EphemeralMessage{ID: message.ID, Peer: message.Peer}, int(now.Unix()))
	if deleted.Seq != 0 {
		t.Fatalf("delete seq=%d", deleted.Seq)
	}
}

func TestEphemeralPushOfflineSkipsHydration(t *testing.T) {
	channels := &ephemeralPushChannels{view: domain.ChannelView{Channel: domain.Channel{ID: 3001}}}
	sessions := &ephemeralPushSessions{online: false}
	now := time.Now()
	router := New(Config{InstanceID: "offline"}, Deps{
		Users: mapUsersService{users: map[int64]domain.User{}}, Channels: channels, Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	router.deliverEphemeralPushLocal(context.Background(), store.EphemeralPush{
		Kind: store.EphemeralPushNew, TargetUserID: 2001,
		Message: domain.EphemeralMessage{
			ID: 77, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 3001},
			SenderUserID: 1001, ReceiverUserID: 2001, Date: int(now.Unix()), RandomID: 78,
			Content: domain.EphemeralContent{Message: "private"}, PayloadHash: [32]byte{1}, Version: 1,
			CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralMessageRetention),
		},
	})
	if channels.calls != 0 {
		t.Fatalf("offline push performed %d channel hydrations", channels.calls)
	}
	if broadcast, targeted := sessions.counts(); broadcast != 0 || targeted != 0 {
		t.Fatalf("offline delivery broadcast=%d targeted=%d", broadcast, targeted)
	}
}
