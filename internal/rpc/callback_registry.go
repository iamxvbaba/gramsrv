package rpc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// callbackRegistry keeps local waiter channels and mirrors ownership/answers to
// a short-lived shared store. The shared CAS is the source of truth when wired:
// it lets getBotCallbackAnswer and answerCallbackQuery land on different nodes
// without accepting two answers or trusting a process-local owner map.
type callbackRegistry struct {
	mu      sync.Mutex
	pending map[int64]*pendingCallback
	shared  store.BotCallbackRegistryStore
}

type pendingCallback struct {
	ch        chan domain.BotCallbackAnswer
	done      chan struct{} // closed on deregister: wakes a waiter that exited on timeout, removing the "answer delivered to a ch whose waiter already left select" race
	botUserID int64
	userID    int64
}

func newCallbackRegistry(shared ...store.BotCallbackRegistryStore) *callbackRegistry {
	var sharedStore store.BotCallbackRegistryStore
	if len(shared) > 0 {
		sharedStore = shared[0]
	}
	return &callbackRegistry{pending: make(map[int64]*pendingCallback), shared: sharedStore}
}

// register records one pending callback and returns a globally unique query_id and a receive
// channel. The caller must defer deregister(queryID), whether or not an answer arrives (one of
// the timeout trio, preventing goroutine/table leaks).
func (c *callbackRegistry) register(botUserID, userID int64) (int64, *pendingCallback) {
	queryID, pending, _ := c.registerContext(context.Background(), time.Now(), botUserID, userID, botCallbackTimeout)
	return queryID, pending
}

func (c *callbackRegistry) registerContext(ctx context.Context, now time.Time, botUserID, userID int64, ttl time.Duration) (int64, *pendingCallback, error) {
	for attempts := 0; attempts < 32; attempts++ {
		p := &pendingCallback{
			ch:        make(chan domain.BotCallbackAnswer, 1),
			done:      make(chan struct{}),
			botUserID: botUserID,
			userID:    userID,
		}
		c.mu.Lock()
		queryID := randomNonZeroInt64()
		if _, exists := c.pending[queryID]; exists {
			c.mu.Unlock()
			continue
		}
		c.pending[queryID] = p
		c.mu.Unlock()
		if c.shared == nil {
			return queryID, p, nil
		}
		created, err := c.shared.PutBotCallbackPending(ctx, store.BotCallbackPending{
			QueryID: queryID, BotUserID: botUserID, UserID: userID, CreatedAt: now,
		}, ttl)
		if err != nil {
			c.removeLocal(queryID)
			return 0, nil, err
		}
		if created {
			return queryID, p, nil
		}
		c.removeLocal(queryID)
	}
	return 0, nil, fmt.Errorf("allocate bot callback query id")
}

// deregister removes the pending entry and closes done (must be called after timeout/unblock,
// idempotent). Closing done immediately wakes a waiter still in select, preventing resolve from
// delivering an answer to a ch whose waiter has left (TOCTOU).
func (c *callbackRegistry) deregister(queryID int64) {
	c.deregisterContext(context.Background(), 0, queryID)
}

func (c *callbackRegistry) deregisterContext(ctx context.Context, botUserID, queryID int64) {
	c.mu.Lock()
	if p, ok := c.pending[queryID]; ok {
		if botUserID == 0 {
			botUserID = p.botUserID
		}
		delete(c.pending, queryID)
		close(p.done)
	}
	c.mu.Unlock()
	if c.shared != nil && botUserID > 0 {
		_ = c.shared.DeleteBotCallbackPending(ctx, botUserID, queryID)
	}
}

func (c *callbackRegistry) removeLocal(queryID int64) {
	c.mu.Lock()
	if p, ok := c.pending[queryID]; ok {
		delete(c.pending, queryID)
		close(p.done)
	}
	c.mu.Unlock()
}

// size returns the current number of pending entries (for tests: asserting it drops to zero after timeout/unblock, no leak).
func (c *callbackRegistry) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// resolve delivers the bot's answer to the waiter. Authorization: only the query's owning bot may
// unblock (callerBotID must equal the botUserID at registration, I6). Returns whether delivery
// succeeded (query unregistered/expired/non-owner -> false).
func (c *callbackRegistry) resolve(callerBotID, queryID int64, ans domain.BotCallbackAnswer) bool {
	resolved, _ := c.resolveContext(context.Background(), callerBotID, queryID, ans)
	return resolved
}

func (c *callbackRegistry) resolveContext(ctx context.Context, callerBotID, queryID int64, ans domain.BotCallbackAnswer) (bool, error) {
	if c.shared != nil {
		resolved, err := c.shared.ResolveBotCallback(ctx, callerBotID, queryID, ans)
		if err != nil || !resolved {
			return resolved, err
		}
		c.deliver(callerBotID, queryID, ans)
		return true, nil
	}
	return c.deliver(callerBotID, queryID, ans), nil
}

func (c *callbackRegistry) deliver(callerBotID, queryID int64, ans domain.BotCallbackAnswer) bool {
	c.mu.Lock()
	p, ok := c.pending[queryID]
	if !ok || p.botUserID != callerBotID {
		c.mu.Unlock()
		return false
	}
	delete(c.pending, queryID)
	c.mu.Unlock()
	// ch has a 1-slot buffer, so delivery is non-blocking; when the waiter has already exited on timeout the buffer is GC'd, no blocking.
	select {
	case p.ch <- ans:
	default:
	}
	return true
}

func (c *callbackRegistry) sharedAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotCallbackAnswer, bool, error) {
	if c.shared == nil {
		return domain.BotCallbackAnswer{}, false, nil
	}
	return c.shared.GetBotCallbackAnswer(ctx, botUserID, queryID)
}

func (r *Router) RunBotCallbackAnswerSubscriber(ctx context.Context) {
	if r == nil || r.callbacks == nil || r.callbacks.shared == nil {
		return
	}
	for ctx.Err() == nil {
		err := r.callbacks.shared.SubscribeBotCallbackAnswers(ctx, func(_ context.Context, push store.BotCallbackAnswerPush) {
			r.callbacks.deliver(push.BotUserID, push.QueryID, push.Answer)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil && r.log != nil {
			r.log.Warn("bot callback answer subscriber disconnected", zap.Error(err))
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

// randomNonZeroInt64 returns a cryptographically random non-zero int64. register calls it while
// holding the lock, so unbounded retrying is forbidden here -- on entropy source failure it
// degrades to a monotonic sequence as a fallback (query_id only needs to be unique within the
// process, and register's collision re-check guarantees uniqueness anyway), never stalling the
// whole registry.
func randomNonZeroInt64() int64 {
	var buf [8]byte
	for i := 0; i < 8; i++ {
		if _, err := rand.Read(buf[:]); err != nil {
			break
		}
		if v := int64(binary.LittleEndian.Uint64(buf[:])); v != 0 {
			return v
		}
	}
	if v := callbackFallbackSeq.Add(1); v != 0 {
		return v
	}
	return 1
}

// callbackFallbackSeq is the monotonic fallback sequence used when the entropy source fails (an extremely rare path).
var callbackFallbackSeq atomic.Int64

// chatInstanceFor derives a stable chat_instance for a (bot,user) private chat (constant across
// multiple callbacks in the same conversation, I8). Currently derived with a deterministic hash
// (persistence is a todo).
func chatInstanceFor(botUserID, userID int64) int64 {
	h := fnv.New64a()
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(botUserID))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(userID))
	_, _ = h.Write(buf[:])
	v := int64(h.Sum64())
	if v == 0 {
		v = 1
	}
	return v
}

// chatInstanceForPeer extends the stable hash to non-private chats without allowing a
// channel id to collide with a numerically equal private user id.
func chatInstanceForPeer(botUserID int64, peer domain.Peer) int64 {
	h := fnv.New64a()
	var buf [17]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(botUserID))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(peer.ID))
	switch peer.Type {
	case domain.PeerTypeChannel:
		buf[16] = 2
	default:
		buf[16] = 1
	}
	_, _ = h.Write(buf[:])
	v := int64(h.Sum64())
	if v == 0 {
		return 1
	}
	return v
}
