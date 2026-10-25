package rpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// preCheckoutTimeout 是客户端挂起等待应答的窗口。与 preCheckoutTimeoutSeconds 是同一
// 个 10 秒要求，写成 time.Duration 只为直接交给 time.NewTimer。
//
// 超时即视为 bot 没有批准这笔支付，付款失败，绝不降级为直接扣款——那正是 pre-checkout
// 要防的事。
const preCheckoutTimeout = preCheckoutTimeoutSeconds * time.Second

var errBotPreCheckoutRegistryStoreRequired = errors.New("bot pre-checkout shared registry store is required")

// preCheckoutRecordTTL bounds the shared record when the waiting Core dies before
// it can take the question back. It only needs to outlive the 10 second window.
const preCheckoutRecordTTL = preCheckoutTimeout + 5*time.Second

// preCheckoutRegistry 协调一次挂起的 pre-checkout 询问。
//
// 与 callbackRegistry 的差别在于时效：callback 允许 25s 且超时只影响按钮动画，
// 而 pre-checkout 是付款前的闸门，窗口固定 10s，超时必须让付款失败。
//
// v2 的 Core 横向扩展：付款方的 RPC 挂在一个实例上，bot 的
// messages.setBotPrecheckoutResults 或 Bot API answerPreCheckoutQuery 可能落在另一
// 个实例。所以属主和唯一胜出的答案放在共享 store 里，进程内只记本实例在等的
// query_id 供测试观察。等待结束时 TakeBotPreCheckout 原子地删除记录并取回已胜出的
// 答案："等待者已超时、答案迟到"要么被采纳（答案先到），要么被拒为 QUERY_ID_INVALID
// （记录已删），不会出现 bot 以为批准了而付款方已离开的写入路径。
type preCheckoutRegistry struct {
	mu      sync.Mutex
	pending map[int64]int64 // query_id → bot_user_id，本实例正在等待的询问
	shared  store.BotPreCheckoutRegistryStore
}

func newPreCheckoutRegistry(shared store.BotPreCheckoutRegistryStore) *preCheckoutRegistry {
	return &preCheckoutRegistry{pending: make(map[int64]int64), shared: shared}
}

// register 在共享 store 里登记一次询问并返回 query_id。调用方必须以 await 或
// abandon 收尾，否则记录要等 TTL 才过期。
func (p *preCheckoutRegistry) register(ctx context.Context, botUserID, userID int64) (int64, error) {
	if p == nil || p.shared == nil {
		return 0, errBotPreCheckoutRegistryStoreRequired
	}
	for attempts := 0; attempts < 32; attempts++ {
		queryID := randomNonZeroInt64()
		created, err := p.shared.PutBotPreCheckoutPending(ctx, queryID, botUserID, userID, preCheckoutRecordTTL)
		if err != nil {
			return 0, err
		}
		if created {
			p.mu.Lock()
			p.pending[queryID] = botUserID
			p.mu.Unlock()
			return queryID, nil
		}
	}
	return 0, fmt.Errorf("allocate bot pre-checkout query id")
}

// abandon 收回一次还没开始等待的询问（投递失败时）。
func (p *preCheckoutRegistry) abandon(ctx context.Context, botUserID, queryID int64) {
	p.removeLocal(queryID)
	if p != nil && p.shared != nil {
		_, _, _ = p.shared.TakeBotPreCheckout(ctx, botUserID, queryID)
	}
}

func (p *preCheckoutRegistry) removeLocal(queryID int64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	delete(p.pending, queryID)
	p.mu.Unlock()
}

// resolve 只接受属主 bot 的应答：其它 bot 拿到 query_id 也无法代答付款闸门。
func (p *preCheckoutRegistry) resolve(ctx context.Context, callerBotID, queryID int64, ans domain.BotPreCheckoutAnswer) (bool, error) {
	if p == nil || p.shared == nil {
		return false, errBotPreCheckoutRegistryStoreRequired
	}
	return p.shared.ResolveBotPreCheckout(ctx, callerBotID, queryID, ans)
}

// pendingSnapshot 返回本实例挂起询问的 query_id，供测试断言"有没有被问过"。
func (p *preCheckoutRegistry) pendingSnapshot() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int64, 0, len(p.pending))
	for queryID := range p.pending {
		out = append(out, queryID)
	}
	return out
}

func (p *preCheckoutRegistry) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending)
}

// await 阻塞到 bot 应答、10s 超时，或调用方 ctx 取消，然后无论结果如何都 Take
// 掉共享记录，保证此后迟到的应答只会得到 QUERY_ID_INVALID。
//
// 返回的 answered 为 false 时，调用方必须让付款失败——把"没有批准"降级成"直接
// 扣款"正是 pre-checkout 要防的事。
func (p *preCheckoutRegistry) await(ctx context.Context, botUserID, queryID int64) (domain.BotPreCheckoutAnswer, bool, error) {
	if p == nil || p.shared == nil {
		return domain.BotPreCheckoutAnswer{}, false, errBotPreCheckoutRegistryStoreRequired
	}
	defer p.removeLocal(queryID)
	waitCtx, cancel := context.WithTimeout(ctx, preCheckoutTimeout)
	ans, found, waitErr := p.shared.WaitBotPreCheckoutAnswer(waitCtx, botUserID, queryID)
	cancel()
	taken, takenFound, takeErr := p.shared.TakeBotPreCheckout(context.WithoutCancel(ctx), botUserID, queryID)
	if ctx.Err() != nil {
		return domain.BotPreCheckoutAnswer{}, false, nil
	}
	if found {
		return ans, true, nil
	}
	if waitErr != nil {
		return domain.BotPreCheckoutAnswer{}, false, waitErr
	}
	if takeErr != nil {
		return domain.BotPreCheckoutAnswer{}, false, takeErr
	}
	// 答案恰好在等待超时与 Take 之间胜出：bot 已被告知成功，按它的回答处理。
	return taken, takenFound, nil
}

// awaitBotPreCheckout 走完整链路：投递 update（Bot API 队列 + MTProto push），然后挂起
// 最多 10 秒等应答。Bot 不应答即视为拒绝——这正是 Telegram 的约定：10 秒内不答 = 这笔
// 支付不成立，所以超时绝不能降级成直接扣款。
func (r *Router) awaitBotPreCheckout(ctx context.Context, botUserID, payerUserID int64, currency string, totalAmount int64, payload string) error {
	query := domain.BotPreCheckoutQuery{
		BotUserID: botUserID, UserID: payerUserID,
		Currency: currency, TotalAmount: totalAmount, Payload: payload,
	}
	queryID, err := r.preCheckouts.register(ctx, botUserID, payerUserID)
	if err != nil {
		if r.log != nil {
			r.log.Error("register bot pre-checkout query",
				zap.Int64("bot_user_id", botUserID), zap.Error(err))
		}
		return internalErr()
	}
	query.ID = queryID

	// A question that was never queued can never be answered. Carrying on would
	// spend the full 10 second window waiting for a bot that was never asked, and
	// then report PAYMENT_FAILED as if the bot had declined - which hides the real
	// cause. So a queue failure fails the payment here, with the reason logged.
	if r.deps.BotAPIUpdates == nil {
		if r.log != nil {
			r.log.Error("bot invoice pre-checkout not queued",
				zap.Int64("bot_user_id", botUserID), zap.Error(errors.New("bot api update store not wired")))
		}
		r.preCheckouts.abandon(context.WithoutCancel(ctx), botUserID, queryID)
		return internalErr()
	}
	queued, _, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
		BotUserID:   botUserID,
		Kind:        domain.BotAPIUpdatePreCheckoutQuery,
		Date:        int(r.clock.Now().Unix()),
		PreCheckout: &query,
	})
	if err != nil {
		if r.log != nil {
			r.log.Error("bot invoice pre-checkout not queued",
				zap.Int64("bot_user_id", botUserID),
				zap.Int64("query_id", queryID),
				zap.Error(err))
		}
		r.preCheckouts.abandon(context.WithoutCancel(ctx), botUserID, queryID)
		return internalErr()
	}
	r.notifyBotAPIUpdate(botUserID)
	if r.log != nil {
		r.log.Info("bot invoice pre-checkout queued",
			zap.Int64("bot_user_id", botUserID),
			zap.Int64("query_id", queryID),
			zap.Int64("update_id", queued.ID),
			zap.Int64("payer_id", payerUserID),
			zap.Int64("total_amount", totalAmount))
	}

	update := &tg.UpdateBotPrecheckoutQuery{
		QueryID: queryID, UserID: payerUserID, Currency: currency, TotalAmount: totalAmount,
	}
	update.Payload = []byte(payload)
	// updateBotPrecheckoutQuery 是 ephemeral 控制消息（无 pts/qts）：durable 的那份是
	// 上面的 Bot API 队列，MTProto 推送只用来唤醒在线的 bot 会话。
	r.pushUserMessageTransient(ctx, botUserID, "push bot pre-checkout query", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(r.clock.Now().Unix()),
	})

	reply, answered, err := r.preCheckouts.await(ctx, botUserID, queryID)
	if err != nil {
		if r.log != nil {
			r.log.Error("wait bot pre-checkout answer",
				zap.Int64("bot_user_id", botUserID),
				zap.Int64("query_id", queryID),
				zap.Error(err))
		}
		return internalErr()
	}
	if !answered {
		// 超时即失败：没有批准就没有付款。
		return preCheckoutFailedErr()
	}
	if reply.OK {
		return nil
	}
	return preCheckoutDeclinedErr(reply.Error)
}

// serviceBotPreCheckout 让内置 service bot 同步回答 pre-checkout。它们没有 MTProto
// 会话也没有 Bot API 消费者，若走"投递 update + 挂起 10 秒"必然每次都超时付款。
//
// 它是可选能力（type-assert）而非 ServiceBotCallbacks 的成员：绝大多数内置 bot 都不卖
// 东西，强迫它们实现一个用不到的方法没有意义。
type serviceBotPreCheckout interface {
	ServiceBotCallbacks
	OnPreCheckoutQuery(ctx context.Context, query domain.BotPreCheckoutQuery) (domain.BotPreCheckoutAnswer, bool, error)
}

// botInvoicePreCheckoutResponder 让内置 service bot 同步回答 pre-checkout，
// 免得它们在没有 MTProto 会话的情况下必然超时。
type botInvoicePreCheckoutResponder interface {
	HandlesBot(botUserID int64) bool
	OnPreCheckoutQuery(ctx context.Context, query domain.BotPreCheckoutQuery) (domain.BotPreCheckoutAnswer, bool, error)
}

// runPreCheckout 在扣款前向 bot 询问并等待应答。
//
// 内置 service bot 没有 MTProto 会话也没有 Bot API 消费者，走"投递 update + 挂起"
// 必然超时，因此先尝试同步应答器（与 callback 的处理一致，见 bots_callback.go）。
// 外部 bot 一律走真实链路：投递 update，然后最多挂起 10 秒。
func (r *Router) runPreCheckout(ctx context.Context, botUserID, payerUserID int64, currency string, totalAmount int64, payload string) error {
	if responder, ok := r.deps.ServiceBotCallbacks.(serviceBotPreCheckout); ok &&
		r.deps.ServiceBotCallbacks.HandlesBot(botUserID) {
		reply, handled, err := responder.OnPreCheckoutQuery(ctx, domain.BotPreCheckoutQuery{
			BotUserID: botUserID, UserID: payerUserID,
			Currency: currency, TotalAmount: totalAmount, Payload: payload,
		})
		if handled {
			if err != nil {
				if r.log != nil {
					r.log.Warn("service bot pre-checkout",
						zap.Int64("bot_user_id", botUserID), zap.Error(err))
				}
				return preCheckoutFailedErr()
			}
			if reply.OK {
				return nil
			}
			return preCheckoutDeclinedErr(reply.Error)
		}
	}
	return r.awaitBotPreCheckout(ctx, botUserID, payerUserID, currency, totalAmount, payload)
}
