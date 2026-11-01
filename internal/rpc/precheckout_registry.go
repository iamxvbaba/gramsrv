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

// preCheckoutTimeout is the window during which the client suspends waiting for
// an answer. It is the same 10 second requirement as preCheckoutTimeoutSeconds,
// written as a time.Duration only to hand directly to time.NewTimer.
//
// A timeout means the bot did not approve this payment, the payment fails, and it
// is never downgraded to a direct charge - which is exactly what pre-checkout is
// meant to prevent.
const preCheckoutTimeout = preCheckoutTimeoutSeconds * time.Second

var errBotPreCheckoutRegistryStoreRequired = errors.New("bot pre-checkout shared registry store is required")

// preCheckoutRecordTTL bounds the shared record when the waiting Core dies before
// it can take the question back. It only needs to outlive the 10 second window.
const preCheckoutRecordTTL = preCheckoutTimeout + 5*time.Second

// preCheckoutRegistry coordinates one pending pre-checkout query.
//
// The difference from callbackRegistry is timing: a callback allows 25s and a timeout only
// affects the button animation, while pre-checkout is the gate before payment, with a fixed
// 10s window, and a timeout must fail the payment.
//
// v2 Core scales horizontally: the payer's RPC waits on one instance, while the bot's
// messages.setBotPrecheckoutResults or Bot API answerPreCheckoutQuery may land on another
// instance. So the owner and the single winning answer live in the shared store, and the
// process only remembers the query_ids this instance is waiting on, for tests to observe.
// When the wait ends, TakeBotPreCheckout atomically deletes the record and takes the winning
// answer: "the waiter timed out and the answer arrived late" is either accepted (the answer
// came first) or rejected as QUERY_ID_INVALID (the record is gone); there is no write path
// where the bot believes it approved while the payer has already left.
type preCheckoutRegistry struct {
	mu      sync.Mutex
	pending map[int64]int64 // query_id → bot_user_id, the queries this instance is waiting on
	shared  store.BotPreCheckoutRegistryStore
}

func newPreCheckoutRegistry(shared store.BotPreCheckoutRegistryStore) *preCheckoutRegistry {
	return &preCheckoutRegistry{pending: make(map[int64]int64), shared: shared}
}

// register records a query in the shared store and returns its query_id. The caller must
// finish with await or abandon, otherwise the record only expires after its TTL.
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

// abandon withdraws a query whose wait has not started (when delivery fails).
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

// resolve only accepts the owning bot's answer: another bot that obtains the
// query_id still cannot answer for the payment gate.
func (p *preCheckoutRegistry) resolve(ctx context.Context, callerBotID, queryID int64, ans domain.BotPreCheckoutAnswer) (bool, error) {
	if p == nil || p.shared == nil {
		return false, errBotPreCheckoutRegistryStoreRequired
	}
	return p.shared.ResolveBotPreCheckout(ctx, callerBotID, queryID, ans)
}

// pendingSnapshot returns the query_ids pending on this instance, for tests to assert "was it asked".
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

// await blocks until the bot answers, the 10s timeout, or the caller's ctx is cancelled, and
// then Takes the shared record whatever the outcome, so a later answer only gets
// QUERY_ID_INVALID.
//
// When the returned answered is false the caller must fail the payment -- downgrading "not
// approved" to "charge anyway" is exactly what pre-checkout exists to prevent.
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
	// The answer won right between the wait timeout and Take: the bot was told it succeeded, so honor its answer.
	return taken, takenFound, nil
}

// awaitBotPreCheckout runs the full path: deliver the update (Bot API queue +
// MTProto push), then suspend for up to 10 seconds waiting for an answer. A bot
// that does not answer is taken as a refusal - this is Telegram's contract: no
// answer within 10 seconds means the payment does not stand, so a timeout must
// never degrade into a direct charge.
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
	// updateBotPrecheckoutQuery is an ephemeral control message (no pts/qts): the durable copy is
	// the Bot API queue above, and the MTProto push only wakes online bot sessions.
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
		// A timeout is a failure: no approval, no payment.
		return preCheckoutFailedErr()
	}
	if reply.OK {
		return nil
	}
	return preCheckoutDeclinedErr(reply.Error)
}

// serviceBotPreCheckout lets a built-in service bot answer pre-checkout
// synchronously. They have no MTProto session and no Bot API consumer, so going
// through "deliver the update + suspend 10 seconds" would inevitably time out and
// fail the payment every time.
//
// It is an optional capability (a type assertion) rather than a member of
// ServiceBotCallbacks: the vast majority of built-in bots sell nothing, and forcing
// them to implement a method they do not use is pointless.
type serviceBotPreCheckout interface {
	ServiceBotCallbacks
	OnPreCheckoutQuery(ctx context.Context, query domain.BotPreCheckoutQuery) (domain.BotPreCheckoutAnswer, bool, error)
}

// botInvoicePreCheckoutResponder lets a built-in service bot answer pre-checkout
// synchronously, sparing it the inevitable timeout when it has no MTProto session.
type botInvoicePreCheckoutResponder interface {
	HandlesBot(botUserID int64) bool
	OnPreCheckoutQuery(ctx context.Context, query domain.BotPreCheckoutQuery) (domain.BotPreCheckoutAnswer, bool, error)
}

// runPreCheckout asks the bot before charging and waits for its answer.
//
// A built-in service bot has no MTProto session and no Bot API consumer, so
// "deliver the update + suspend" inevitably times out; therefore try the
// synchronous responder first (consistent with callback handling, see
// bots_callback.go). External bots always take the real path: deliver the update,
// then suspend for up to 10 seconds.
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
