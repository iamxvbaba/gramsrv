package domain

import "testing"

func TestSavedStarGiftPriceCursorRoundTrip(t *testing.T) {
	cursor := EncodeSavedStarGiftPriceCursor(12345, 678)
	price, id, ok := DecodeSavedStarGiftPriceCursor(cursor)
	if !ok || price != 12345 || id != 678 {
		t.Fatalf("cursor %q = price %d id %d ok %v", cursor, price, id, ok)
	}
	for _, invalid := range []string{"", "garbage", "v1:12345:678", "v2:12345", "v2:x:678"} {
		if price, id, ok := DecodeSavedStarGiftPriceCursor(invalid); ok {
			t.Fatalf("cursor %q decoded to price %d id %d", invalid, price, id)
		}
	}
}
