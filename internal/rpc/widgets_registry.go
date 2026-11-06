package rpc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// widgetRegistry keeps local waiter channels and mirrors ownership/answers to a
// short-lived shared store. Like callbackRegistry, the shared CAS is the source
// of truth when wired: messages.getBotWidgetQueryAnswer and
// messages.setBotWidgetQueryAnswer may land on different nodes without
// accepting two answers or trusting a process-local owner map.
type widgetRegistry struct {
	mu      sync.Mutex
	pending map[int64]*pendingWidgetQuery
	shared  store.WidgetQueryStore
}

type pendingWidgetQuery struct {
	ch        chan domain.BotWidgetQueryAnswer
	done      chan struct{} // closed on deregister: wakes a waiter that exited on timeout
	botUserID int64
	userID    int64
}

func newWidgetRegistry(shared ...store.WidgetQueryStore) *widgetRegistry {
	var sharedStore store.WidgetQueryStore
	if len(shared) > 0 {
		sharedStore = shared[0]
	}
	return &widgetRegistry{pending: make(map[int64]*pendingWidgetQuery), shared: sharedStore}
}

// registerContext records one pending query and returns a globally unique
// query_id. The caller must defer deregister(queryID).
func (w *widgetRegistry) registerContext(ctx context.Context, now time.Time, botUserID, userID int64, ttl time.Duration) (int64, *pendingWidgetQuery, error) {
	for attempts := 0; attempts < 32; attempts++ {
		p := &pendingWidgetQuery{
			ch:        make(chan domain.BotWidgetQueryAnswer, 1),
			done:      make(chan struct{}),
			botUserID: botUserID,
			userID:    userID,
		}
		w.mu.Lock()
		queryID := randomNonZeroInt64()
		if _, exists := w.pending[queryID]; exists {
			w.mu.Unlock()
			continue
		}
		w.pending[queryID] = p
		w.mu.Unlock()
		if w.shared == nil {
			return queryID, p, nil
		}
		created, err := w.shared.PutBotWidgetQueryPending(ctx, store.BotWidgetQueryPending{
			QueryID: queryID, BotUserID: botUserID, UserID: userID, CreatedAt: now,
		}, ttl)
		if err != nil {
			w.removeLocal(queryID)
			return 0, nil, err
		}
		if created {
			return queryID, p, nil
		}
		w.removeLocal(queryID)
	}
	return 0, nil, fmt.Errorf("allocate bot widget query id")
}

func (w *widgetRegistry) deregister(queryID int64) {
	w.deregisterContext(context.Background(), 0, queryID)
}

func (w *widgetRegistry) deregisterContext(ctx context.Context, botUserID, queryID int64) {
	w.mu.Lock()
	if p, ok := w.pending[queryID]; ok {
		if botUserID == 0 {
			botUserID = p.botUserID
		}
		delete(w.pending, queryID)
		close(p.done)
	}
	w.mu.Unlock()
	if w.shared != nil && botUserID > 0 {
		_ = w.shared.DeleteBotWidgetQueryPending(ctx, botUserID, queryID)
	}
}

func (w *widgetRegistry) removeLocal(queryID int64) {
	w.mu.Lock()
	if p, ok := w.pending[queryID]; ok {
		delete(w.pending, queryID)
		close(p.done)
	}
	w.mu.Unlock()
}

func (w *widgetRegistry) size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.pending)
}

// resolveContext delivers the bot's answer to the waiter. Only the query's
// owning bot may unblock it.
func (w *widgetRegistry) resolveContext(ctx context.Context, callerBotID, queryID int64, answer domain.BotWidgetQueryAnswer) (bool, error) {
	if w.shared != nil {
		resolved, err := w.shared.ResolveBotWidgetQuery(ctx, callerBotID, queryID, answer)
		if err != nil || !resolved {
			return resolved, err
		}
		w.deliver(callerBotID, queryID, answer)
		return true, nil
	}
	return w.deliver(callerBotID, queryID, answer), nil
}

func (w *widgetRegistry) deliver(callerBotID, queryID int64, answer domain.BotWidgetQueryAnswer) bool {
	w.mu.Lock()
	p, ok := w.pending[queryID]
	if !ok || p.botUserID != callerBotID {
		w.mu.Unlock()
		return false
	}
	delete(w.pending, queryID)
	w.mu.Unlock()
	select {
	case p.ch <- answer:
	default:
	}
	return true
}

func (w *widgetRegistry) sharedAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotWidgetQueryAnswer, bool, error) {
	if w.shared == nil {
		return domain.BotWidgetQueryAnswer{}, false, nil
	}
	return w.shared.GetBotWidgetQueryAnswer(ctx, botUserID, queryID)
}

// RunBotWidgetQueryAnswerSubscriber mirrors RunBotCallbackAnswerSubscriber: it
// delivers cross-node widget answers to local waiters.
func (r *Router) RunBotWidgetQueryAnswerSubscriber(ctx context.Context) {
	if r == nil || r.widgets == nil || r.widgets.shared == nil {
		return
	}
	for ctx.Err() == nil {
		err := r.widgets.shared.SubscribeBotWidgetQueryAnswers(ctx, func(_ context.Context, push store.BotWidgetQueryAnswerPush) {
			r.widgets.deliver(push.BotUserID, push.QueryID, push.Answer)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil && r.log != nil {
			r.log.Warn("bot widget query answer subscriber disconnected", zap.Error(err))
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
