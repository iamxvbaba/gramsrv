package rpc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"hash/fnv"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// callbackSharedStore adapts the callback store interface to the generic
// answerSharedStore consumed by answerRegistry.
type callbackSharedStore struct {
	store.BotCallbackRegistryStore
}

func (s callbackSharedStore) putPending(ctx context.Context, queryID, botUserID, userID int64, now time.Time, ttl time.Duration) (bool, error) {
	return s.PutBotCallbackPending(ctx, store.BotCallbackPending{
		QueryID: queryID, BotUserID: botUserID, UserID: userID, CreatedAt: now,
	}, ttl)
}

func (s callbackSharedStore) resolve(ctx context.Context, callerBotID, queryID int64, answer domain.BotCallbackAnswer) (bool, error) {
	return s.ResolveBotCallback(ctx, callerBotID, queryID, answer)
}

func (s callbackSharedStore) answer(ctx context.Context, botUserID, queryID int64) (domain.BotCallbackAnswer, bool, error) {
	return s.GetBotCallbackAnswer(ctx, botUserID, queryID)
}

func (s callbackSharedStore) deletePending(ctx context.Context, botUserID, queryID int64) error {
	return s.DeleteBotCallbackPending(ctx, botUserID, queryID)
}

func (s callbackSharedStore) subscribe(ctx context.Context, handle func(context.Context, int64, int64, domain.BotCallbackAnswer)) error {
	return s.SubscribeBotCallbackAnswers(ctx, func(ctx context.Context, push store.BotCallbackAnswerPush) {
		handle(ctx, push.BotUserID, push.QueryID, push.Answer)
	})
}

// pendingCallback is the callback answer waiter; the generic answerRegistry owns
// the register/deliver lifecycle.
type pendingCallback = pendingAnswer[domain.BotCallbackAnswer]

// callbackRegistry keeps local waiter channels and mirrors ownership/answers to
// a short-lived shared store. The shared CAS is the source of truth when wired:
// it lets getBotCallbackAnswer and answerCallbackQuery land on different nodes
// without accepting two answers or trusting a process-local owner map.
type callbackRegistry struct {
	*answerRegistry[domain.BotCallbackAnswer]
}

func newCallbackRegistry(shared ...store.BotCallbackRegistryStore) *callbackRegistry {
	var sharedStore store.BotCallbackRegistryStore
	if len(shared) > 0 {
		sharedStore = shared[0]
	}
	var generic answerSharedStore[domain.BotCallbackAnswer]
	if sharedStore != nil {
		generic = callbackSharedStore{sharedStore}
	}
	return &callbackRegistry{answerRegistry: newAnswerRegistry(generic, "allocate bot callback query id")}
}

// register records one pending callback and returns a globally unique query_id.
// The caller must defer deregister(queryID), whether or not an answer arrives.
func (c *callbackRegistry) register(botUserID, userID int64) (int64, *pendingCallback) {
	queryID, pending, _ := c.registerContext(context.Background(), time.Now(), botUserID, userID, botCallbackTimeout)
	return queryID, pending
}

func (c *callbackRegistry) deregister(queryID int64) {
	c.deregisterContext(context.Background(), 0, queryID)
}

// resolve delivers the bot's answer to the waiter. Authorization: only the
// query's owning bot may unblock (callerBotID must equal the botUserID at
// registration). Returns whether delivery succeeded.
func (c *callbackRegistry) resolve(callerBotID, queryID int64, ans domain.BotCallbackAnswer) bool {
	resolved, _ := c.resolveContext(context.Background(), callerBotID, queryID, ans)
	return resolved
}

func (r *Router) RunBotCallbackAnswerSubscriber(ctx context.Context) {
	if r == nil || r.callbacks == nil {
		return
	}
	r.callbacks.runAnswerSubscriber(ctx, func(err error) {
		if r.log != nil {
			r.log.Warn("bot callback answer subscriber disconnected", zap.Error(err))
		}
	})
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
