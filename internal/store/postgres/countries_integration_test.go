package postgres

import (
	"context"
	"testing"

	"telesrv/internal/seed/catalog"
)

// TestUpsertCountriesFullCatalog verifies the whole country catalog can be written in one go (jsonb recordset),
// and that entries whose prefixes/patterns are omitted in the catalog (e.g. AD) land as empty arrays rather than NULL.
// This path runs on every startup; the previous row-by-row upsert took ~15 seconds.
func TestUpsertCountriesFullCatalog(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	countries := catalog.Countries().Countries
	if len(countries) == 0 {
		t.Skip("catalog countries not seeded")
	}
	help := NewHelpStore(pool)
	if err := help.UpsertCountries(ctx, countries); err != nil {
		t.Fatalf("upsert countries: %v", err)
	}
	// Write once more: it runs on every service startup, so it must be idempotent.
	if err := help.UpsertCountries(ctx, countries); err != nil {
		t.Fatalf("upsert countries again: %v", err)
	}
	list, err := help.ListCountries(ctx, "en")
	if err != nil {
		t.Fatalf("list countries: %v", err)
	}
	stored := make(map[string]int, len(list.Countries))
	for _, country := range list.Countries {
		stored[country.ISO2] = len(country.CountryCodes)
	}
	for _, country := range countries {
		if got, ok := stored[country.ISO2]; !ok || got != len(country.CountryCodes) {
			t.Fatalf("country %s codes = %d (found=%v), want %d", country.ISO2, got, ok, len(country.CountryCodes))
		}
	}
	for _, country := range list.Countries {
		if country.ISO2 != "AD" {
			continue
		}
		for _, code := range country.CountryCodes {
			if code.CountryCode != "376" {
				continue
			}
			if len(code.Prefixes) != 0 {
				t.Fatalf("AD prefixes = %#v, want empty", code.Prefixes)
			}
			if len(code.Patterns) != 1 || code.Patterns[0] != "XXXXXX" {
				t.Fatalf("AD patterns = %#v, want [XXXXXX]", code.Patterns)
			}
			return
		}
	}
	t.Fatal("AD/376 not found")
}
