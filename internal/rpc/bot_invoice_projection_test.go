package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// A bot's product invoice used to be carried in a PremiumInvoice, whose Valid()
// requires months, a plan version and an entitlement message. That made every
// plain sale fail validation and surface as an opaque BAD_REQUEST, so the
// generic shape has its own type and its own rules.
func TestProductInvoiceProjectsOntoMessageMediaInvoice(t *testing.T) {
	media := tgMessageMedia(&domain.MessageMedia{
		Kind: domain.MessageMediaKindInvoice,
		ProductInvoice: &domain.Invoice{
			Title: "Тестовый товар", Description: "Оплата 100 звёздами",
			AmountStars: 100, StartParam: "test-item-1",
		},
	})
	got, ok := media.(*tg.MessageMediaInvoice)
	if !ok {
		t.Fatalf("media = %T, want *tg.MessageMediaInvoice", media)
	}
	if got.Title != "Тестовый товар" || got.Description != "Оплата 100 звёздами" {
		t.Fatalf("projected = %+v, want the advertised title and description", got)
	}
	if got.Currency != domain.PremiumCurrencyStars {
		t.Fatalf("currency = %q, want %q", got.Currency, domain.PremiumCurrencyStars)
	}
	if got.TotalAmount != 100 {
		t.Fatalf("total_amount = %d, want 100", got.TotalAmount)
	}
	if got.StartParam != "test-item-1" {
		t.Fatalf("start_param = %q, want test-item-1", got.StartParam)
	}
}

// The Premium bot keeps its own invoice type; the two must not shadow each other.
func TestPremiumInvoiceProjectionStillWorksAlongsideProductInvoice(t *testing.T) {
	media := tgMessageMedia(&domain.MessageMedia{
		Kind: domain.MessageMediaKindInvoice,
		Invoice: &domain.PremiumInvoice{
			Kind: domain.PremiumPurchaseSelf, Months: 1, DurationDays: 30,
			AmountStars: 500, PlanVersion: 1,
			Title: "Premium", Description: "1 month",
		},
	})
	got, ok := media.(*tg.MessageMediaInvoice)
	if !ok {
		t.Fatalf("media = %T, want *tg.MessageMediaInvoice", media)
	}
	if got.Title != "Premium" || got.TotalAmount != 500 {
		t.Fatalf("premium projection = %+v, want Premium/500", got)
	}
}

// An invalid product invoice must not become an empty media object, which the
// client would render as a broken message.
func TestProductInvoiceRejectsEmptyShape(t *testing.T) {
	for name, invoice := range map[string]domain.Invoice{
		"no title":       {Description: "d", AmountStars: 10},
		"no description": {Title: "t", AmountStars: 10},
		"no amount":      {Title: "t", Description: "d"},
		"negative":       {Title: "t", Description: "d", AmountStars: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if invoice.Valid() {
				t.Fatalf("invoice %+v was accepted", invoice)
			}
			media := tgMessageMedia(&domain.MessageMedia{
				Kind: domain.MessageMediaKindInvoice, ProductInvoice: &invoice,
			})
			if _, ok := media.(*tg.MessageMediaEmpty); !ok {
				t.Fatalf("media = %T, want *tg.MessageMediaEmpty", media)
			}
		})
	}
}
