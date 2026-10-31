package catalog

import "testing"

// TestSeededCatalogsLoaded verifies that the official catalog data embedded via
// go:embed is parsed correctly and non-empty. The content is fetched by
// cmd/catalogfetch; the numbers are lower bounds, so a refetch yielding more does
// not fail the test.
func TestSeededCatalogsLoaded(t *testing.T) {
	cs := Countries()
	if len(cs.Countries) < 200 {
		t.Fatalf("countries = %d, want >= 200 (official ~235)", len(cs.Countries))
	}
	if cs.Hash == 0 {
		t.Fatalf("countries hash = 0, want stable nonzero")
	}
	var cnHasPattern bool
	for _, c := range cs.Countries {
		if c.ISO2 == "CN" {
			for _, cc := range c.CountryCodes {
				if cc.CountryCode == "86" && len(cc.Patterns) > 0 {
					cnHasPattern = true
				}
			}
		}
	}
	if !cnHasPattern {
		t.Fatalf("CN(+86) missing phone pattern")
	}

	tzs, tzHash := Timezones()
	if len(tzs) < 100 {
		t.Fatalf("timezones = %d, want >= 100 (official ~419)", len(tzs))
	}
	if tzHash == 0 {
		t.Fatalf("timezones hash = 0")
	}

	groups, gHash := EmojiGroups()
	if len(groups) == 0 || gHash == 0 {
		t.Fatalf("emoji groups = %d hash=%d, want non-empty", len(groups), gHash)
	}
	if len(groups[0].Emoticons) == 0 {
		t.Fatalf("emoji group %q has no emoticons", groups[0].Title)
	}

	set, ok := EmojiKeywords("en")
	if !ok || set.Version == 0 || len(set.Keywords) < 1000 {
		t.Fatalf("en emoji keywords ok=%v version=%d count=%d, want seeded (official ~4286)", ok, set.Version, len(set.Keywords))
	}
}
