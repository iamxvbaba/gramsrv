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
	done      chan struct{} // deregister 时关闭：唤醒超时退出的等待者，消除「答案投递到已离开 select 的 ch」竞态
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

// register 登记一次挂起的 callback，返回全局唯一 query_id 与接收通道。调用方必须
// defer deregister(queryID)，无论是否收到答案（超时三件套之一，防 goroutine/表泄漏）。
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

// deregister 移除挂起条目并关闭 done（超时/解挂后必调，幂等）。关闭 done 让仍在
// select 的等待者立即醒来，避免 resolve 把答案投递到一个等待者已离开的 ch（TOCTOU）。
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

// size 返回当前挂起条目数（测试用：断言超时/解挂后归零、无泄漏）。
func (c *callbackRegistry) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// resolve 把 bot 的答案投递给等待者。鉴权：仅该 query 的属主 bot 可解挂（callerBotID
// 必须等于注册时的 botUserID，I6）。返回是否成功投递（query 未注册/已超时/非属主 → false）。
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
	// ch 有 1 容量缓冲，非阻塞投递；等待者已超时退出时缓冲被 GC，不阻塞。
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

// randomNonZeroInt64 取密码学随机非零 int64。register 在持锁下调用，故此处禁止
// 无限重试——熵源异常时退化为单调序列兜底（query_id 只需进程内唯一，register 的
// 撞键复核会再保证唯一性），绝不卡住整个 registry。
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

// callbackFallbackSeq 是熵源失败时的单调兜底序列（极罕见路径）。
var callbackFallbackSeq atomic.Int64

// chatInstanceFor 为 (bot,user) 私聊派生稳定的 chat_instance（同一对话多次 callback
// 间恒定，I8）。当前用确定性 hash 派生（持久化记 todo）。
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
