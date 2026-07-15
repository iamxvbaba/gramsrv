package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestInlineBotQueryPublishesRemotePush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	shared := newTestInlinePushBroker()
	users := memory.NewUserStore()
	botStore := memory.NewBotStore(users)
	bots := botsapp.NewService(users, botStore, nil)
	owner, err := users.Create(ctx, domain.User{AccessHash: 9101, Phone: "15550009101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	peer, err := users.Create(ctx, domain.User{AccessHash: 9102, Phone: "15550009102", FirstName: "Peer"})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	bot, _, err := bots.CreateBot(ctx, owner.ID, "Inline Push Bot", "inline_push_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	if _, err := bots.SetInlinePlaceholder(ctx, bot.ID, "Search push"); err != nil {
		t.Fatalf("set inline placeholder: %v", err)
	}

	localSessions := &captureSessions{}
	remoteSessions := &captureSessions{}
	local := New(Config{InstanceID: "node-a"}, Deps{
		Users:    appusers.NewService(users),
		Bots:     bots,
		Inline:   shared,
		Sessions: localSessions,
	}, zaptest.NewLogger(t), clock.System)
	remote := New(Config{InstanceID: "node-b"}, Deps{
		Inline:   shared,
		Sessions: remoteSessions,
	}, zaptest.NewLogger(t), clock.System)

	go local.RunInlineBotPushSubscriber(ctx)
	go remote.RunInlineBotPushSubscriber(ctx)
	shared.waitSubscribers(t, 2)

	type inlineGetResult struct {
		res *tg.MessagesBotResults
		err error
	}
	gotCh := make(chan inlineGetResult, 1)
	go func() {
		res, err := local.onMessagesGetInlineBotResults(WithUserID(ctx, owner.ID), &tg.MessagesGetInlineBotResultsRequest{
			Bot:   inputUser(bot),
			Peer:  inputPeerUser(peer),
			Query: "remote-shape",
		})
		gotCh <- inlineGetResult{res: res, err: err}
	}()

	queryID := waitInlineBotQuery(t, local)
	event := shared.waitPublished(t)
	if event.SourceID != "node-a" || event.QueryID != queryID || event.BotUserID != bot.ID || event.UserID != owner.ID {
		t.Fatalf("published event = %+v, queryID=%d bot=%d owner=%d", event, queryID, bot.ID, owner.ID)
	}
	if event.Query != "remote-shape" || event.PeerType != store.InlineQueryPeerTypePM {
		t.Fatalf("published query metadata = %+v", event)
	}

	localPushes := localSessions.pushedUserIDs()
	if len(localPushes) != 1 || localPushes[0] != bot.ID {
		t.Fatalf("local pushes = %+v, want exactly one direct push to bot", localPushes)
	}
	remoteUpdates := waitInlinePushUpdate(t, remoteSessions)
	if remoteUpdates.Date == 0 || len(remoteUpdates.Updates) != 1 {
		t.Fatalf("remote updates = %+v", remoteUpdates)
	}
	update, ok := remoteUpdates.Updates[0].(*tg.UpdateBotInlineQuery)
	if !ok {
		t.Fatalf("remote update type = %T", remoteUpdates.Updates[0])
	}
	if update.QueryID != queryID || update.UserID != owner.ID || update.Query != "remote-shape" {
		t.Fatalf("remote inline update = %+v", update)
	}
	if _, ok := update.PeerType.(*tg.InlineQueryPeerTypePM); !ok {
		t.Fatalf("remote peer type = %T", update.PeerType)
	}

	if ok, err := local.onMessagesSetInlineBotResults(WithUserID(ctx, bot.ID), &tg.MessagesSetInlineBotResultsRequest{
		QueryID:   queryID,
		CacheTime: 30,
		Results: []tg.InputBotInlineResultClass{
			&tg.InputBotInlineResult{
				ID:          "remote-1",
				Type:        "article",
				Title:       "Remote",
				SendMessage: &tg.InputBotInlineMessageText{Message: "remote ok"},
			},
		},
	}); err != nil || !ok {
		t.Fatalf("set inline results = %v,%v, want true,nil", ok, err)
	}
	select {
	case got := <-gotCh:
		if got.err != nil {
			t.Fatalf("get inline results: %v", got.err)
		}
		if got.res.QueryID != queryID || len(got.res.Results) != 1 {
			t.Fatalf("inline results = %+v", got.res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("get inline results did not finish")
	}

	localPushes = localSessions.pushedUserIDs()
	if len(localPushes) != 1 {
		t.Fatalf("local self subscription duplicated push: %+v", localPushes)
	}
}

type testInlinePushBroker struct {
	*testInlineRegistryStore

	mu          sync.Mutex
	subscribers map[chan store.BotInlineQueryPush]struct{}
	published   []store.BotInlineQueryPush
}

func newTestInlinePushBroker() *testInlinePushBroker {
	return &testInlinePushBroker{
		testInlineRegistryStore: newTestInlineRegistryStore(),
		subscribers:             make(map[chan store.BotInlineQueryPush]struct{}),
	}
}

func (s *testInlinePushBroker) PublishBotInlineQuery(ctx context.Context, event store.BotInlineQueryPush) error {
	s.mu.Lock()
	s.published = append(s.published, event)
	subscribers := make([]chan store.BotInlineQueryPush, 0, len(s.subscribers))
	for ch := range s.subscribers {
		subscribers = append(subscribers, ch)
	}
	s.mu.Unlock()
	for _, ch := range subscribers {
		select {
		case ch <- event:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *testInlinePushBroker) SubscribeBotInlineQueries(ctx context.Context, handle func(context.Context, store.BotInlineQueryPush)) error {
	ch := make(chan store.BotInlineQueryPush, 16)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subscribers, ch)
		s.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-ch:
			handle(ctx, event)
		}
	}
}

func (s *testInlinePushBroker) waitSubscribers(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := len(s.subscribers)
		s.mu.Unlock()
		if got >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscribers < %d", want)
}

func (s *testInlinePushBroker) waitPublished(t *testing.T) store.BotInlineQueryPush {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		if len(s.published) > 0 {
			event := s.published[0]
			s.mu.Unlock()
			return event
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("inline push event was not published")
	return store.BotInlineQueryPush{}
}

func waitInlinePushUpdate(t *testing.T, sessions *captureSessions) *tg.Updates {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := sessions.snapshot()
		if snap.message != nil {
			updates, ok := snap.message.(*tg.Updates)
			if !ok {
				t.Fatalf("pushed message type = %T", snap.message)
			}
			return updates
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("inline push update was not delivered")
	return nil
}
