package domain

import "testing"

func TestStarsToNanoTONMatchesReferenceRates(t *testing.T) {
	// 666 Stars at 0.013 USD = 8.658 USD = ~6.438 TON at 1.3448 USD.
	got := StarsToNanoTON(666)
	if got < 6_437_000_000 || got > 6_439_000_000 {
		t.Fatalf("666 Stars = %d nanoton", got)
	}
	if StarsToNanoTON(0) != 0 || StarsToNanoTON(1) <= 0 {
		t.Fatal("edge cases")
	}
}
