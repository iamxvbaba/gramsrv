package domain

import (
	"reflect"
	"testing"
)

func TestDetectAutomaticMessageEntitiesFindsBankCardsAsTDLibDoes(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    []MessageEntity
	}{
		{
			name:    "grouped cards in a sentence",
			message: "Card check 4111 1111 1111 1111 and 6011 1111 1111 1117",
			want: []MessageEntity{
				{Type: MessageEntityBankCard, Offset: 11, Length: 19},
				{Type: MessageEntityBankCard, Offset: 35, Length: 19},
			},
		},
		{
			name:    "hyphens and UTF-16 offsets",
			message: "🚀 5555-5555-5555-4444.",
			want:    []MessageEntity{{Type: MessageEntityBankCard, Offset: 3, Length: 19}},
		},
		{
			name:    "a space after 16 continuous digits ends the card",
			message: "4111111111111111 1234",
			want:    []MessageEntity{{Type: MessageEntityBankCard, Offset: 0, Length: 16}},
		},
		{name: "Luhn failure", message: "4111 1111 1111 1112"},
		{name: "Mastercard needs 16 digits", message: "5555 5555 5555 4444 0"},
		{name: "glued to a letter", message: "id4111111111111111"},
		{name: "followed by a letter", message: "4111111111111111x"},
		{name: "after a plus sign", message: "+4111111111111111"},
		{name: "too many digits", message: "41111111111111111111"},
		{name: "too sparse", message: "4  1  1  1  1  1  1  1  1  1  1  1  1  1  1  1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectAutomaticMessageEntities(tt.message, nil)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("entities = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDetectAutomaticMessageEntitiesKeepsBankCardsOutOfOccupiedRanges(t *testing.T) {
	message := "https://example.test/4111111111111111"
	got := DetectAutomaticMessageEntities(message, []MessageEntitySpan{{Offset: 0, Length: len(message)}})
	if len(got) != 0 {
		t.Fatalf("entities = %+v, want none inside the URL", got)
	}
}

func TestBankCardSchemeAndLuhn(t *testing.T) {
	for digits, scheme := range map[string]string{
		"4111111111111111": "Visa",
		"2223003122003222": "Mastercard",
		"378282246310005":  "American Express",
		"6200000000000005": "UnionPay",
		"2200000000000004": "MIR",
		"6011111111111117": "",
	} {
		if !BankCardLuhnValid(digits) || BankCardScheme(digits) != scheme {
			t.Fatalf("%s: luhn=%v scheme=%q, want valid %q", digits, BankCardLuhnValid(digits), BankCardScheme(digits), scheme)
		}
	}
	for _, digits := range []string{"", "4111111111111112", "41111a1111111111"} {
		if BankCardLuhnValid(digits) {
			t.Fatalf("%q passed Luhn", digits)
		}
	}
}

func TestMayContainBankCardNumberNeedsThirteenDigits(t *testing.T) {
	for text, want := range map[string]bool{
		"":                          false,
		"10:30 in room 7, call 555": false,
		"123456789012":              false,
		"1234 5678 9012 3":          true,
		"4111111111111111":          true,
	} {
		if got := MayContainBankCardNumber(text); got != want {
			t.Fatalf("MayContainBankCardNumber(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestBankCardDigitsStripsOnlyGrouping(t *testing.T) {
	for in, want := range map[string]string{
		"4111 1111 1111 1111": "4111111111111111",
		"4111-1111-1111-1111": "4111111111111111",
		"4111111111111111":    "4111111111111111",
	} {
		if got, ok := BankCardDigits(in); !ok || got != want {
			t.Fatalf("BankCardDigits(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"4111_1111", "4111.1111", "4111 1111 1111 111a"} {
		if _, ok := BankCardDigits(in); ok {
			t.Fatalf("BankCardDigits(%q) accepted a non-grouping character", in)
		}
	}
}
