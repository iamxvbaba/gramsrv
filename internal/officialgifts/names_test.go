package officialgifts

import "testing"

func TestDisplayTitleNamesUntitledGifts(t *testing.T) {
	if got := displayTitle(5170145012310081615, "", 15); got != "Heart" {
		t.Fatalf("classic gift = %q, want Heart", got)
	}
	if got := displayTitle(42, "", 500); got != "Gift 500⭐" {
		t.Fatalf("unknown untitled gift = %q, want its price", got)
	}
	if got := displayTitle(5170145012310081615, "Snapshot Title", 15); got != "Snapshot Title" {
		t.Fatalf("a snapshot title wins, got %q", got)
	}
}
