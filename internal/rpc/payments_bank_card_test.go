package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"
)

func TestBankCardDataNamesTheSchemesTDLibRecognizes(t *testing.T) {
	cases := map[string]string{
		"4111111111111111":    "Visa",
		"5555555555554444":    "Mastercard",
		"2223003122003222":    "Mastercard",
		"378282246310005":     "American Express",
		"6200000000000005":    "UnionPay",
		"2200000000000004":    "MIR",
		"6011111111111117":    "",
		"4000000000000000006": "Visa",
	}
	for number, title := range cases {
		data, err := bankCardData(number)
		if err != nil {
			t.Fatalf("bankCardData(%s) error = %v", number, err)
		}
		if data.Title != title || len(data.OpenURLs) != 0 {
			t.Fatalf("bankCardData(%s) = %+v, want title %q without bank links", number, data, title)
		}
	}
}

func TestBankCardDataRejectsInvalidNumbers(t *testing.T) {
	for _, number := range []string{
		"",
		"411111111111",         // 12 digits
		"41111111111111111111", // 20 digits
		"4111111111111112",     // Luhn
		"4111 1111 1111 1111",  // TDLib sends digits only
		"411111111111111a",
	} {
		if _, err := bankCardData(number); !tgerr.Is(err, "BANK_CARD_NUMBER_INVALID") {
			t.Fatalf("bankCardData(%q) error = %v, want BANK_CARD_NUMBER_INVALID", number, err)
		}
	}
}

func TestPaymentsGetBankCardDataDispatches(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)
	result, method := dispatchExactLayerRPCTest(t, r, ctx, tlprofile.ProfileCanonical, &tg.PaymentsGetBankCardDataRequest{Number: "4111111111111111"})
	if method != "payments.getBankCardData" {
		t.Fatalf("dispatched method = %q", method)
	}
	data, ok := result.CanonicalValue().(*tg.PaymentsBankCardData)
	if !ok || data.Title != "Visa" {
		t.Fatalf("result = %#v, want Visa card data", result.CanonicalValue())
	}
}

func TestAugmentAutoEntitiesAddsBankCardsOutsideURLsAndCode(t *testing.T) {
	got := testAugmentAutoEntities("Card 4111 1111 1111 1111", nil)
	if len(got) != 1 {
		t.Fatalf("entities = %#v, want one bank card", got)
	}
	if card, ok := got[0].(*tg.MessageEntityBankCard); !ok || card.Offset != 5 || card.Length != 19 {
		t.Fatalf("entity = %#v, want bank card at 5+19", got[0])
	}

	for _, entities := range [][]tg.MessageEntityClass{nil, {&tg.MessageEntityCode{Offset: 0, Length: 16}}} {
		message := "https://example.test/4111111111111111"
		if entities != nil {
			message = "4111111111111111"
		}
		for _, entity := range testAugmentAutoEntities(message, entities) {
			if _, ok := entity.(*tg.MessageEntityBankCard); ok {
				t.Fatalf("%q got a bank card inside an occupied range: %#v", message, entity)
			}
		}
	}
}
