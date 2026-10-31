package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// preCheckoutTimeout is the window during which the client suspends waiting for
// an answer. It is the same 10 second requirement as preCheckoutTimeoutSeconds,
// written as a time.Duration only to hand directly to time.NewTimer.
//
// A timeout means the bot did not approve this payment, the payment fails, and it
// is never downgraded to a direct charge - which is exactly what pre-checkout is
// meant to prevent.
const preCheckoutTimeout = preCheckoutTimeoutSeconds * time.Second

// preCheckoutRegistry holds one pending pre-checkout query and its answer channel.
//
// The difference from callbackRegistry is the timing: a callback allows 25s and a
// timeout only affects the button animation, whereas pre-checkout is a gate before
// payment, the window is a fixed 10s, and a timeout must fail the payment. So this
// does not share a store - the query's lifetime is a single RPC call, and sharing
// it across nodes would instead introduce a write path for "the waiter already
// timed out, the answer arrives late". The registry lives only in-process and its
// lifetime is bound to the RPC.
type preCheckoutRegistry struct {
	mu      chan struct{}
	pending map[int64]*pendingPreCheckout
}

type pendingPreCheckout struct {
	answer    chan domain.BotPreCheckoutAnswer
	done      chan struct{}
	botUserID int64
	userID    int64
}

func newPreCheckoutRegistry() *preCheckoutRegistry {
	return &preCheckoutRegistry{mu: make(chan struct{}, 1), pending: make(map[int64]*pendingPreCheckout)}
}

// register records one pending query and returns the query_id and the receive
// channel. The caller must defer deregister, whether or not an answer arrives,
// otherwise both the table and a goroutine leak.
func (p *preCheckoutRegistry) register(botUserID, userID int64) (int64, *pendingPreCheckout) {
	entry := &pendingPreCheckout{
		answer:    make(chan domain.BotPreCheckoutAnswer, 1),
		done:      make(chan struct{}),
		botUserID: botUserID,
		userID:    userID,
	}
	p.mu <- struct{}{}
	queryID := randomNonZeroInt64()
	for _, exists := p.pending[queryID]; exists; _, exists = p.pending[queryID] {
		queryID = randomNonZeroInt64()
	}
	p.pending[queryID] = entry
	<-p.mu
	return queryID, entry
}

func (p *preCheckoutRegistry) deregister(queryID int64) {
	p.mu <- struct{}{}
	entry, ok := p.pending[queryID]
	if ok {
		delete(p.pending, queryID)
		close(entry.done)
	}
	<-p.mu
}

// resolve only accepts the owning bot's answer: another bot that obtains the
// query_id still cannot answer for the payment gate.
func (p *preCheckoutRegistry) resolve(callerBotID, queryID int64, ans domain.BotPreCheckoutAnswer) bool {
	p.mu <- struct{}{}
	entry, ok := p.pending[queryID]
	if ok && entry.botUserID == callerBotID {
		delete(p.pending, queryID)
	} else {
		ok = false
	}
	<-p.mu
	if !ok {
		return false
	}
	// answer has a capacity-1 buffer and is delivered non-blockingly: if the waiter
	// has already timed out and left, the buffer is reclaimed with the entry and
	// resolve will not block on the send.
	select {
	case entry.answer <- ans:
	default:
	}
	close(entry.done)
	return true
}

// pendingSnapshot returns the query_ids of the currently pending queries, so a
// test can assert whether a question was asked.
func (p *preCheckoutRegistry) pendingSnapshot() []int64 {
	p.mu <- struct{}{}
	defer func() { <-p.mu }()
	out := make([]int64, 0, len(p.pending))
	for queryID := range p.pending {
		out = append(out, queryID)
	}
	return out
}

func (p *preCheckoutRegistry) size() int {
	p.mu <- struct{}{}
	defer func() { <-p.mu }()
	return len(p.pending)
}

// await blocks until the bot answers, the 10s timeout fires, or the caller's ctx is
// cancelled. Whichever comes first wins; both timeout and cancellation deregister
// first, guaranteeing that no late answer is delivered to a waiter that has left.
//
// When the returned answered is false, the caller must fail the payment - degrading
// "not approved" into "direct charge" is exactly what pre-checkout is meant to
// prevent.
func (p *preCheckoutRegistry) await(ctx context.Context, queryID int64, entry *pendingPreCheckout) (domain.BotPreCheckoutAnswer, bool) {
	timer := time.NewTimer(preCheckoutTimeout)
	defer timer.Stop()
	defer p.deregister(queryID)

	select {
	case ans := <-entry.answer:
		return ans, true
	case <-timer.C:
		return domain.BotPreCheckoutAnswer{}, false
	case <-entry.done:
		// resolve already delivered the answer and it is in the buffer; this was
		// just woken by the close.
		select {
		case ans := <-entry.answer:
			return ans, true
		default:
			return domain.BotPreCheckoutAnswer{}, false
		}
	case <-ctx.Done():
		return domain.BotPreCheckoutAnswer{}, false
	}
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
	queryID, entry := r.preCheckouts.register(botUserID, payerUserID)
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
		r.preCheckouts.deregister(queryID)
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
		r.preCheckouts.deregister(queryID)
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
	r.pushUserMessage(ctx, botUserID, "push bot pre-checkout query", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    int(r.clock.Now().Unix()),
	})

	reply, answered := r.preCheckouts.await(ctx, queryID, entry)
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

// runPreCheckout asks the bot before charging and waits for an answer. A bot that
// does not answer is taken as a refusal, which is Telegram's contract: no answer
// within 10 seconds means the payment does not stand.
//

// runPreCheckout asks the bot before charging and waits for an answer.
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
