package rpc

import (
	"context"
	"errors"
	"sync"
	"time"
)

// answerSharedStore abstracts the cross-node CAS used by both the bot-callback
// and bot-widget-query registries. Keeping the local waiter bookkeeping in one
// generic place avoids keeping two near-identical copies in sync.
type answerSharedStore[A any] interface {
	putPending(ctx context.Context, queryID, botUserID, userID int64, now time.Time, ttl time.Duration) (bool, error)
	resolve(ctx context.Context, callerBotID, queryID int64, answer A) (bool, error)
	answer(ctx context.Context, botUserID, queryID int64) (A, bool, error)
	deletePending(ctx context.Context, botUserID, queryID int64) error
	subscribe(ctx context.Context, handle func(context.Context, int64, int64, A)) error
}

// pendingAnswer is one registered waiter: a 1-slot answer channel plus a done
// signal that wakes a waiter which already left on timeout.
type pendingAnswer[A any] struct {
	ch        chan A
	done      chan struct{}
	botUserID int64
	userID    int64
}

// answerRegistry implements the shared "register a waiter, deliver exactly one
// owner-authorized answer, mirror ownership to the shared store" behavior used by
// both bot callbacks and bot widget queries. The shared CAS is the source of
// truth when wired: the request and its answer may land on different nodes
// without accepting two answers or trusting a process-local owner map.
type answerRegistry[A any] struct {
	mu          sync.Mutex
	pending     map[int64]*pendingAnswer[A]
	shared      answerSharedStore[A]
	allocateErr string
}

func newAnswerRegistry[A any](shared answerSharedStore[A], allocateErr string) *answerRegistry[A] {
	return &answerRegistry[A]{pending: make(map[int64]*pendingAnswer[A]), shared: shared, allocateErr: allocateErr}
}

// registerContext records one pending answer and returns a globally unique
// query_id. The caller must defer deregister(queryID).
func (r *answerRegistry[A]) registerContext(ctx context.Context, now time.Time, botUserID, userID int64, ttl time.Duration) (int64, *pendingAnswer[A], error) {
	for attempts := 0; attempts < 32; attempts++ {
		p := &pendingAnswer[A]{
			ch:        make(chan A, 1),
			done:      make(chan struct{}),
			botUserID: botUserID,
			userID:    userID,
		}
		r.mu.Lock()
		queryID := randomNonZeroInt64()
		if _, exists := r.pending[queryID]; exists {
			r.mu.Unlock()
			continue
		}
		r.pending[queryID] = p
		r.mu.Unlock()
		if r.shared == nil {
			return queryID, p, nil
		}
		created, err := r.shared.putPending(ctx, queryID, botUserID, userID, now, ttl)
		if err != nil {
			r.removeLocal(queryID)
			return 0, nil, err
		}
		if created {
			return queryID, p, nil
		}
		r.removeLocal(queryID)
	}
	return 0, nil, errors.New(r.allocateErr)
}

// deregisterContext removes the pending entry and closes done (idempotent).
func (r *answerRegistry[A]) deregisterContext(ctx context.Context, botUserID, queryID int64) {
	r.mu.Lock()
	if p, ok := r.pending[queryID]; ok {
		if botUserID == 0 {
			botUserID = p.botUserID
		}
		delete(r.pending, queryID)
		close(p.done)
	}
	r.mu.Unlock()
	if r.shared != nil && botUserID > 0 {
		_ = r.shared.deletePending(ctx, botUserID, queryID)
	}
}

func (r *answerRegistry[A]) removeLocal(queryID int64) {
	r.mu.Lock()
	if p, ok := r.pending[queryID]; ok {
		delete(r.pending, queryID)
		close(p.done)
	}
	r.mu.Unlock()
}

// size returns the current number of pending entries (for tests: asserting it
// drops to zero after timeout/unblock, no leak).
func (r *answerRegistry[A]) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

// resolveContext delivers the answer to the waiter. Only the query's owning
// owner may unblock it.
func (r *answerRegistry[A]) resolveContext(ctx context.Context, callerBotID, queryID int64, answer A) (bool, error) {
	if r.shared != nil {
		resolved, err := r.shared.resolve(ctx, callerBotID, queryID, answer)
		if err != nil || !resolved {
			return resolved, err
		}
		r.deliver(callerBotID, queryID, answer)
		return true, nil
	}
	return r.deliver(callerBotID, queryID, answer), nil
}

func (r *answerRegistry[A]) deliver(callerBotID, queryID int64, answer A) bool {
	r.mu.Lock()
	p, ok := r.pending[queryID]
	if !ok || p.botUserID != callerBotID {
		r.mu.Unlock()
		return false
	}
	delete(r.pending, queryID)
	r.mu.Unlock()
	// ch has a 1-slot buffer, so delivery is non-blocking even when the waiter
	// already exited on timeout.
	select {
	case p.ch <- answer:
	default:
	}
	return true
}

func (r *answerRegistry[A]) sharedAnswer(ctx context.Context, botUserID, queryID int64) (A, bool, error) {
	var zero A
	if r.shared == nil {
		return zero, false, nil
	}
	return r.shared.answer(ctx, botUserID, queryID)
}

// runAnswerSubscriber retries the shared subscription until ctx is done. It is a
// no-op without a shared store.
func (r *answerRegistry[A]) runAnswerSubscriber(ctx context.Context, log func(error)) {
	if r == nil || r.shared == nil {
		return
	}
	for ctx.Err() == nil {
		err := r.shared.subscribe(ctx, func(_ context.Context, botUserID, queryID int64, answer A) {
			r.deliver(botUserID, queryID, answer)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil && log != nil {
			log(err)
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
