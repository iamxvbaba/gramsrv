package rpc

import (
	"context"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// widgetSharedStore adapts the widget-query store interface to the generic
// answerSharedStore consumed by answerRegistry.
type widgetSharedStore struct {
	store.WidgetQueryStore
}

func (s widgetSharedStore) putPending(ctx context.Context, queryID, botUserID, userID int64, now time.Time, ttl time.Duration) (bool, error) {
	return s.PutBotWidgetQueryPending(ctx, store.BotWidgetQueryPending{
		QueryID: queryID, BotUserID: botUserID, UserID: userID, CreatedAt: now,
	}, ttl)
}

func (s widgetSharedStore) resolve(ctx context.Context, callerBotID, queryID int64, answer domain.BotWidgetQueryAnswer) (bool, error) {
	return s.ResolveBotWidgetQuery(ctx, callerBotID, queryID, answer)
}

func (s widgetSharedStore) answer(ctx context.Context, botUserID, queryID int64) (domain.BotWidgetQueryAnswer, bool, error) {
	return s.GetBotWidgetQueryAnswer(ctx, botUserID, queryID)
}

func (s widgetSharedStore) deletePending(ctx context.Context, botUserID, queryID int64) error {
	return s.DeleteBotWidgetQueryPending(ctx, botUserID, queryID)
}

func (s widgetSharedStore) subscribe(ctx context.Context, handle func(context.Context, int64, int64, domain.BotWidgetQueryAnswer)) error {
	return s.SubscribeBotWidgetQueryAnswers(ctx, func(ctx context.Context, push store.BotWidgetQueryAnswerPush) {
		handle(ctx, push.BotUserID, push.QueryID, push.Answer)
	})
}

// pendingWidgetQuery is the widget-query answer waiter; the generic
// answerRegistry owns the register/deliver lifecycle.
type pendingWidgetQuery = pendingAnswer[domain.BotWidgetQueryAnswer]

// widgetRegistry keeps local waiter channels and mirrors ownership/answers to a
// short-lived shared store. The shared CAS is the source of truth when wired:
// messages.getBotWidgetQueryAnswer and messages.setBotWidgetQueryAnswer may land
// on different nodes without accepting two answers or trusting a process-local
// owner map.
type widgetRegistry struct {
	*answerRegistry[domain.BotWidgetQueryAnswer]
}

func newWidgetRegistry(shared ...store.WidgetQueryStore) *widgetRegistry {
	var sharedStore store.WidgetQueryStore
	if len(shared) > 0 {
		sharedStore = shared[0]
	}
	var generic answerSharedStore[domain.BotWidgetQueryAnswer]
	if sharedStore != nil {
		generic = widgetSharedStore{sharedStore}
	}
	return &widgetRegistry{answerRegistry: newAnswerRegistry(generic, "allocate bot widget query id")}
}

func (w *widgetRegistry) deregister(queryID int64) {
	w.deregisterContext(context.Background(), 0, queryID)
}

// RunBotWidgetQueryAnswerSubscriber mirrors RunBotCallbackAnswerSubscriber: it
// delivers cross-node widget answers to local waiters.
func (r *Router) RunBotWidgetQueryAnswerSubscriber(ctx context.Context) {
	if r == nil || r.widgets == nil {
		return
	}
	r.widgets.runAnswerSubscriber(ctx, func(err error) {
		if r.log != nil {
			r.log.Warn("bot widget query answer subscriber disconnected", zap.Error(err))
		}
	})
}
