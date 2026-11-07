package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// widgetQuerySubscriber receives cross-node widget-query answer pushes.
type widgetQuerySubscriber func(context.Context, store.BotWidgetQueryAnswerPush)

// fakeWidgetQueryStore is an in-process store.WidgetQueryStore that mirrors the
// shared-CAS contract of the Redis store: at most one answer per query id, only
// the owning bot may resolve, and subscribers receive every push.
type fakeWidgetQueryStore struct {
	mu          sync.Mutex
	pending     map[int64]store.BotWidgetQueryPending
	ownerBot    map[int64]int64
	answers     map[int64]domain.BotWidgetQueryAnswer
	subscribers []widgetQuerySubscriber
	subscribed  chan struct{}
}

func newFakeWidgetQueryStore() *fakeWidgetQueryStore {
	return &fakeWidgetQueryStore{
		pending:    make(map[int64]store.BotWidgetQueryPending),
		ownerBot:   make(map[int64]int64),
		answers:    make(map[int64]domain.BotWidgetQueryAnswer),
		subscribed: make(chan struct{}, 1),
	}
}

func (s *fakeWidgetQueryStore) PutBotWidgetQueryPending(_ context.Context, pending store.BotWidgetQueryPending, _ time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pending[pending.QueryID]; ok {
		return false, nil
	}
	if _, ok := s.answers[pending.QueryID]; ok {
		return false, nil
	}
	s.pending[pending.QueryID] = pending
	return true, nil
}

func (s *fakeWidgetQueryStore) ResolveBotWidgetQuery(_ context.Context, botUserID, queryID int64, answer domain.BotWidgetQueryAnswer) (bool, error) {
	s.mu.Lock()
	pending, ok := s.pending[queryID]
	if !ok || pending.BotUserID != botUserID {
		s.mu.Unlock()
		return false, nil
	}
	delete(s.pending, queryID)
	s.ownerBot[queryID] = botUserID
	s.answers[queryID] = answer
	subs := append([]widgetQuerySubscriber(nil), s.subscribers...)
	s.mu.Unlock()
	push := store.BotWidgetQueryAnswerPush{QueryID: queryID, BotUserID: botUserID, Answer: answer}
	for _, sub := range subs {
		sub(context.Background(), push)
	}
	return true, nil
}

func (s *fakeWidgetQueryStore) GetBotWidgetQueryAnswer(_ context.Context, botUserID, queryID int64) (domain.BotWidgetQueryAnswer, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, ok := s.ownerBot[queryID]
	if !ok || owner != botUserID {
		return domain.BotWidgetQueryAnswer{}, false, nil
	}
	return s.answers[queryID], true, nil
}

func (s *fakeWidgetQueryStore) DeleteBotWidgetQueryPending(_ context.Context, botUserID, queryID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pending, ok := s.pending[queryID]; ok && pending.BotUserID == botUserID {
		delete(s.pending, queryID)
	}
	return nil
}

func (s *fakeWidgetQueryStore) SubscribeBotWidgetQueryAnswers(ctx context.Context, handle func(context.Context, store.BotWidgetQueryAnswerPush)) error {
	s.mu.Lock()
	s.subscribers = append(s.subscribers, handle)
	s.mu.Unlock()
	select {
	case s.subscribed <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil
}

// TestWidgetQueryCrossNodeRegistryRoundTrip verifies the shared widget-query
// registry: a query registered on one node is resolved by the owning bot on
// another, the answer is readable through the shared store, only one answer wins
// and a non-owner can never resolve it.
func TestWidgetQueryCrossNodeRegistryRoundTrip(t *testing.T) {
	ctx := context.Background()
	shared := newFakeWidgetQueryStore()
	waiting := newWidgetRegistry(shared)
	answering := newWidgetRegistry(shared)

	queryID, _, err := waiting.registerContext(ctx, time.Now(), 7, 42, widgetQueryTimeout)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, found, _ := waiting.sharedAnswer(ctx, 7, queryID); found {
		t.Fatal("answer found before the bot replied")
	}
	if resolved, err := answering.resolveContext(ctx, 8, queryID, domain.BotWidgetQueryAnswer{Result: "forged"}); err != nil || resolved {
		t.Fatalf("non-owner resolve = %v, %v; want false", resolved, err)
	}
	if resolved, err := answering.resolveContext(ctx, 7, queryID, domain.BotWidgetQueryAnswer{Result: "answer"}); err != nil || !resolved {
		t.Fatalf("owner resolve = %v, %v; want true", resolved, err)
	}
	ans, found, err := waiting.sharedAnswer(ctx, 7, queryID)
	if err != nil || !found || ans.Result != "answer" {
		t.Fatalf("shared answer = %+v, %v, %v; want the stored answer", ans, found, err)
	}
	if resolved, _ := answering.resolveContext(ctx, 7, queryID, domain.BotWidgetQueryAnswer{Result: "again"}); resolved {
		t.Fatal("an already-resolved query id resolved twice")
	}
	if _, found, _ := waiting.sharedAnswer(ctx, 9, queryID); found {
		t.Fatal("a foreign bot read the answer")
	}
}

// TestWidgetQuerySubscriberDeliversCrossNodeAnswer verifies that a remote node's
// resolve reaches the local waiter through the shared subscriber, not just
// through local delivery.
func TestWidgetQuerySubscriberDeliversCrossNodeAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shared := newFakeWidgetQueryStore()
	waiting := newWidgetRegistry(shared)
	answering := newWidgetRegistry(shared)
	router := &Router{clock: clock.System, widgets: waiting}

	go router.RunBotWidgetQueryAnswerSubscriber(ctx)
	select {
	case <-shared.subscribed:
	case <-time.After(2 * time.Second):
		t.Fatal("the answer subscriber never registered")
	}

	queryID, pending, err := waiting.registerContext(ctx, time.Now(), 7, 42, widgetQueryTimeout)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if resolved, err := answering.resolveContext(ctx, 7, queryID, domain.BotWidgetQueryAnswer{Result: "remote"}); err != nil || !resolved {
		t.Fatalf("remote resolve = %v, %v; want true", resolved, err)
	}
	select {
	case ans := <-pending.ch:
		if ans.Result != "remote" {
			t.Fatalf("delivered answer = %q", ans.Result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the cross-node answer was not delivered to the local waiter")
	}
}

// widgetQueryBots marks the given accounts as bots for callerBotID / userIsBot.
type widgetQueryBots struct {
	BotsService
	bots map[int64]bool
}

func (b widgetQueryBots) BotInfo(_ context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	return domain.BotProfile{}, b.bots[botUserID], nil
}

// TestWidgetQueryAnswerOwnedByBot verifies messages.setBotWidgetQueryAnswer is
// bot-only and only the query's owning bot may unblock it.
func TestWidgetQueryAnswerOwnedByBot(t *testing.T) {
	r := newWidgetTestRouter(t, 7, 8)
	r.deps.Bots = widgetQueryBots{bots: map[int64]bool{7: true, 8: true}}
	ctx := context.Background()

	queryID, _, err := r.widgets.registerContext(ctx, time.Now(), 7, 42, widgetQueryTimeout)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	req := &tg.MessagesSetBotWidgetQueryAnswerRequest{QueryID: queryID}
	req.SetResult("answer")
	if ok, err := r.onMessagesSetBotWidgetQueryAnswer(WithUserID(ctx, 8), req); !tgerr.Is(err, "QUERY_ID_INVALID") || ok {
		t.Fatalf("non-owner answer = %v, %v; want QUERY_ID_INVALID", ok, err)
	}
	if ok, err := r.onMessagesSetBotWidgetQueryAnswer(WithUserID(ctx, 7), req); err != nil || !ok {
		t.Fatalf("owner answer = %v, %v; want true", ok, err)
	}
}
