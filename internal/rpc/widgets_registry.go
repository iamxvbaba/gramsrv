package rpc

import (
	"errors"
	"sync"

	"telesrv/internal/domain"
)

// errNoQueryID signals the registry could not allocate a unique query id.
var errNoQueryID = errors.New("rpc: unable to allocate widget query id")

// widgetRegistry coordinates the synchronous answer to messages.getBotWidgetQueryAnswer:
// the server pushes updateBotWidgetQuery to the bot, which answers asynchronously
// with messages.setBotWidgetQueryAnswer(query_id, result). It is process-local,
// matching the pre-checkout registry; an unanswered query ends in BOT_RESPONSE_TIMEOUT.
type widgetRegistry struct {
	mu      sync.Mutex
	pending map[int64]*pendingWidgetQuery
}

type pendingWidgetQuery struct {
	ch        chan domain.BotWidgetQueryAnswer
	done      chan struct{}
	botUserID int64
	userID    int64
}

func newWidgetRegistry() *widgetRegistry {
	return &widgetRegistry{pending: make(map[int64]*pendingWidgetQuery)}
}

// register allocates a non-zero query id owned by botUserID.
func (w *widgetRegistry) register(botUserID, userID int64) (int64, *pendingWidgetQuery, error) {
	pending := &pendingWidgetQuery{
		ch:        make(chan domain.BotWidgetQueryAnswer, 1),
		done:      make(chan struct{}),
		botUserID: botUserID,
		userID:    userID,
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for attempt := 0; attempt < 32; attempt++ {
		queryID := randomNonZeroInt64()
		if _, exists := w.pending[queryID]; exists {
			continue
		}
		w.pending[queryID] = pending
		return queryID, pending, nil
	}
	return 0, nil, errNoQueryID
}

// resolve delivers the answer to the owning bot's waiter. A non-owner or an
// unknown/expired query id is not delivered.
func (w *widgetRegistry) resolve(callerBotID, queryID int64, answer domain.BotWidgetQueryAnswer) bool {
	w.mu.Lock()
	pending, ok := w.pending[queryID]
	if !ok {
		w.mu.Unlock()
		return false
	}
	if pending.botUserID != callerBotID {
		w.mu.Unlock()
		return false
	}
	delete(w.pending, queryID)
	w.mu.Unlock()
	select {
	case pending.ch <- answer:
	default:
	}
	return true
}

// deregister drops an abandoned waiter so a late answer is never delivered.
func (w *widgetRegistry) deregister(queryID int64) {
	w.mu.Lock()
	pending, ok := w.pending[queryID]
	if ok {
		delete(w.pending, queryID)
	}
	w.mu.Unlock()
	if ok {
		close(pending.done)
	}
}
