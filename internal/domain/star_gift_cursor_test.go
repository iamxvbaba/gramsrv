package domain

import "testing"

func TestSavedStarGiftListCursorRoundTrip(t *testing.T) {
	want := SavedStarGiftListCursor{PinnedOrder: 7, ID: 9223372036854770000}
	encoded := EncodeSavedStarGiftListCursor(want.PinnedOrder, want.ID)
	got, ok := DecodeSavedStarGiftListCursor(encoded)
	if !ok || got != want {
		t.Fatalf("cursor round trip = %+v ok=%v, want %+v", got, ok, want)
	}

	unpinned := SavedStarGiftListCursor{ID: 42}
	got, ok = DecodeSavedStarGiftListCursor(EncodeSavedStarGiftListCursor(0, unpinned.ID))
	if !ok || got != unpinned {
		t.Fatalf("unpinned cursor round trip = %+v ok=%v, want %+v", got, ok, unpinned)
	}
}

func TestSavedStarGiftListCursorRejectsInvalidAndSimpleIDShapes(t *testing.T) {
	for _, cursor := range []string{
		"not-base64!",
		EncodeStarGiftCursor(42),
		EncodeSavedStarGiftListCursor(-1, 42),
		EncodeSavedStarGiftListCursor(1, 0),
	} {
		if got, ok := DecodeSavedStarGiftListCursor(cursor); ok {
			t.Fatalf("cursor %q decoded as %+v, want rejected", cursor, got)
		}
	}
}
