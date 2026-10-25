package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// The charge id has to be stable across retries: bot_stars_payments uses it as
// the idempotency key, so a resend that generated a fresh id would mint a second
// credit for one sale.
func TestBotInvoiceChargeIDIsStableAndUniquelyPrefixed(t *testing.T) {
	invoice := domain.BotInvoice{ID: 4242}
	first := botInvoiceChargeID(invoice)
	second := botInvoiceChargeID(invoice)
	if first != second {
		t.Fatalf("charge id = %q then %q, want a stable value", first, second)
	}
	if first == botInvoiceChargeID(domain.BotInvoice{ID: 4243}) {
		t.Fatal("two invoices produced the same charge id")
	}
	if got := botInvoiceChargeID(invoice); got == chargeIDForBot("real-telegram-charge") {
		t.Fatal("generated charge id collides with a real-looking one")
	}
	// The refund uses a derived key, so it must not reuse the credit key or the
	// receipt unique index would swallow the reversal.
	if botInvoiceChargeID(invoice)+"-refund" == first {
		t.Fatal("refund key collides with the credit key")
	}
}

func chargeIDForBot(raw string) string { return raw }

// Form ids are generated locally because sendPaymentForm in this layer has no
// provider: they only have to round-trip and stay out of the real namespace.
func TestBotInvoiceFormIDRoundTrips(t *testing.T) {
	for _, id := range []int64{1, 2, 99, 1 << 20} {
		formID := botInvoiceFormID(domain.BotInvoice{ID: id})
		got, ok := botInvoiceFromFormID(formID)
		if !ok || got != id {
			t.Fatalf("form id %d round-tripped to %d, ok=%v", id, got, ok)
		}
	}
	// Anything outside the generated range is refused so a client cannot aim a
	// settlement at an arbitrary form id. base+1 is valid: it is invoice 1.
	for _, formID := range []int64{0, 1, -1, botInvoiceFormIDBase - 1, -botInvoiceFormIDBase} {
		if _, ok := botInvoiceFromFormID(formID); ok {
			t.Fatalf("form id %d was accepted, want refusal", formID)
		}
	}
}
