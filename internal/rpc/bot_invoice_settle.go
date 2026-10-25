package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// botInvoiceSettle commits through a single durable payment aggregate.
func (r *Router) botInvoiceSettle(ctx context.Context, userID int64, req *tg.PaymentsSendPaymentFormRequest, inv *tg.InputInvoiceMessage) (tg.PaymentsPaymentResultClass, bool, error) {
	if r.deps.Bots == nil || r.deps.Stars == nil {
		return nil, false, notImplementedErr()
	}
	invoice, err := r.botInvoiceForMessage(ctx, userID, inv.Peer, inv.MsgID)
	if err != nil {
		if isInvoiceLookupMiss(err) {
			return nil, false, nil
		}
		return nil, true, err
	}
	// A real XTR purchase carries neither shipping nor a tip: telesrv models no
	// fiat checkout, so anything extra is a request it cannot honour.
	if req.RequestedInfoID != "" {
		return nil, true, tgerr.New(400, "REQUESTED_INFO_ID_INVALID")
	}
	if req.ShippingOptionID != "" {
		return nil, true, tgerr.New(400, "SHIPPING_OPTION_INVALID")
	}
	if req.TipAmount != 0 {
		return nil, true, tgerr.New(400, "TIP_AMOUNT_INVALID")
	}
	if req.FormID != botInvoiceFormID(invoice) {
		return nil, true, tgerr.New(400, "PAYMENT_FORM_INVALID")
	}
	payments, ok := r.deps.Bots.(store.BotInvoicePayments)
	if !ok {
		return nil, true, notImplementedErr()
	}

	if invoice.Refunded {
		return nil, true, tgerr.New(400, "INVOICE_REFUNDED")
	}
	// Pre-checkout runs before anything is marked, not after. Settling first would
	// leave the invoice flagged as paid even when the bot refuses, and the retry
	// would then return a receipt with no money behind it.
	//
	// An already settled invoice skips the question: the order was approved once
	// and a replay must not re-ask or re-charge.
	if !invoice.Settled() {
		if err := r.runPreCheckout(ctx, invoice.BotUserID, userID,
			invoice.Currency, invoice.Amount, invoice.Payload); err != nil {
			return nil, true, err
		}
	}
	// Apply the configured starting grant before checkout, exactly as the Stars service does.
	if _, err := r.deps.Stars.GetBalance(ctx, userID); err != nil {
		return nil, true, starsErr(err)
	}
	sent, err := payments.PayBotInvoice(ctx, invoice.ID, userID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, true, starsErr(err)
	}
	r.notifyBotAPIUpdate(invoice.BotUserID)
	return &tg.PaymentsPaymentResult{Updates: tgPrivateSendResultUpdates(sent, 0, false, nil, nil)}, true, nil
}

// botInvoiceChargeID derives a stable charge id from the invoice id.
//
// It must be stable across retries so the wallet receipt suppresses a second
// credit, and it must not look like a real Telegram charge id: the prefix keeps
// it recognisable in the ledger.
func botInvoiceChargeID(invoice domain.BotInvoice) string {
	return fmt.Sprintf("botinv-%d", invoice.ID)
}

// BotAPIRefundStarPayment implements refundStarPayment. It reverses the exact
// Stars the charge credited: the bot wallet is debited and the buyer refunded.
//
// Nothing is flagged until the request has proved it owns the charge, because a
// bot calling this with someone else's charge id would otherwise mark it refunded
// and permanently deny the real owner the refund.
func (r *Router) BotAPIRefundStarPayment(ctx context.Context, botID, requestedPayerID int64, telegramPaymentChargeID string) (bool, error) {
	if r.deps.Bots == nil || r.deps.Stars == nil {
		return false, notImplementedErr()
	}
	chargeID := strings.TrimSpace(telegramPaymentChargeID)
	if chargeID == "" {
		return false, errors.New("CHARGE_ID_INVALID")
	}
	invoice, found, err := r.deps.Bots.BotInvoiceByCharge(ctx, chargeID)
	if err != nil {
		if errors.Is(err, domain.ErrBotInvoiceNotFound) {
			return false, errors.New("CHARGE_ID_NOT_FOUND")
		}
		return false, err
	}
	if !found {
		return false, errors.New("CHARGE_ID_NOT_FOUND")
	}
	// Only the owning bot may refund its own charge.
	if invoice.BotUserID != botID {
		return false, errors.New("CHARGE_ID_NOT_FOUND")
	}
	// user_id from the request body is not trusted to say who gets the money. The
	// payer is recorded on the invoice, so the two have to agree; otherwise a bot
	// could refund its own charge into an account it controls.
	if invoice.PayerID <= 0 || requestedPayerID != invoice.PayerID {
		return false, errors.New("USER_ID_INVALID")
	}

	payments, ok := r.deps.Bots.(store.BotInvoicePayments)
	if !ok {
		return false, notImplementedErr()
	}
	if err := payments.RefundBotInvoice(ctx, botID, requestedPayerID, chargeID, int(r.clock.Now().Unix())); err != nil {
		return false, starsErr(err)
	}
	return true, nil
}
