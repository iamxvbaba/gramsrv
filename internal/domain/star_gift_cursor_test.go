package domain

import (
	"encoding/base64"
	"testing"
)

func TestSavedStarGiftListCursorRoundTrip(t *testing.T) {
	want := SavedStarGiftListCursor{PinnedOrder: 7, Date: 1700000123, ID: 9223372036854770000}
	encoded := EncodeSavedStarGiftListCursor(want.PinnedOrder, want.Date, want.ID)
	got, ok := DecodeSavedStarGiftListCursor(encoded)
	if !ok || got != want {
		t.Fatalf("cursor round trip = %+v ok=%v, want %+v", got, ok, want)
	}

	unpinned := SavedStarGiftListCursor{Date: 42, ID: 42}
	got, ok = DecodeSavedStarGiftListCursor(EncodeSavedStarGiftListCursor(0, unpinned.Date, unpinned.ID))
	if !ok || got != unpinned {
		t.Fatalf("unpinned cursor round trip = %+v ok=%v, want %+v", got, ok, unpinned)
	}
}

func TestSavedStarGiftListCursorRejectsInvalidAndObsoleteShapes(t *testing.T) {
	obsoleteV1 := base64.RawURLEncoding.EncodeToString([]byte("v1:0:42"))
	for _, cursor := range []string{
		"not-base64!",
		EncodeStarGiftCursor(42),
		obsoleteV1,
		EncodeSavedStarGiftListCursor(-1, 1700000000, 42),
		EncodeSavedStarGiftListCursor(0, -1, 42),
		EncodeSavedStarGiftListCursor(0, 1700000000, 0),
	} {
		if got, ok := DecodeSavedStarGiftListCursor(cursor); ok {
			t.Fatalf("cursor %q decoded as %+v, want rejected", cursor, got)
		}
	}
}
