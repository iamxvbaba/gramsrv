package postgres

import (
	"context"
	"testing"

	"telesrv/internal/seed/catalog"
)

// TestUpsertCountriesFullCatalog 验证整本国家目录能一次写入（jsonb recordset），
// 并且 catalog 里省略 prefixes/patterns 的号码（如 AD）落成空数组而不是 NULL。
// 这条路径每次启动都会走，过去逐行 upsert 要 ~15 秒。
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
	// 再写一次：服务每次启动都执行，必须幂等。
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
