package domain

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The shop's product list lives in the bot's catalog.js, not here. The Go side
// keeps a copy so the admin panel can show and edit every product before any
// override row exists -- and a copy is exactly the kind of thing that goes
// stale quietly. It already had: num_short, num_long and uname_10 were priced
// differently from the bot, two of the three username tiers were missing
// entirely (so they could never be priced at all), and three rows were listed
// for products the bot does not sell.
//
// These tests read catalog.js directly so the two cannot drift again.

// botCatalogProduct is one entry of the `fixed` array in catalog.js.
type botCatalogProduct struct {
	code       string
	starsPrice int
	bid        int64
}

var catalogEntry = regexp.MustCompile(`\{ kind: KINDS\.(\w+), code: "([^"]+)"([^\n]*)\}`)

func parseBotCatalog(t *testing.T) []botCatalogProduct {
	t.Helper()
	raw, err := os.ReadFile("../../cmd/bots/grammystore/src/catalog.js")
	if err != nil {
		t.Fatalf("read bot catalog: %v", err)
	}
	src := string(raw)
	fixed := src[strings.Index(src, "const fixed"):]
	fixed = fixed[:strings.Index(fixed, "];")]

	var out []botCatalogProduct
	for _, m := range catalogEntry.FindAllStringSubmatch(fixed, -1) {
		price := regexp.MustCompile(`starsPrice: (\d+)`).FindStringSubmatch(m[3])
		if price == nil {
			t.Fatalf("catalog entry %q has no starsPrice; update this test", m[2])
		}
		starsPrice, err := strconv.Atoi(price[1])
		if err != nil {
			t.Fatalf("catalog entry %q: %v", m[2], err)
		}
		var bid int64
		if b := regexp.MustCompile(`bid: (\d+)`).FindStringSubmatch(m[3]); b != nil {
			// The catalog's bid is a TON amount that fits an int by
			// inspection today; a jump past that is worth failing on
			// rather than silently truncating.
			v, err := strconv.ParseInt(b[1], 10, 64)
			if err != nil {
				t.Fatalf("catalog entry %q bid %q: %v", m[2], b[1], err)
			}
			bid = v
		}
		out = append(out, botCatalogProduct{code: m[2], starsPrice: starsPrice, bid: bid})
	}
	if len(out) == 0 {
		t.Fatal("no products parsed out of catalog.js; the format changed and this test needs updating")
	}
	return out
}

func TestDefaultItemPricesMirrorBotCatalog(t *testing.T) {
	bot := parseBotCatalog(t)

	defaults := make(map[string]ItemPrice, len(DefaultItemPrices))
	for _, p := range DefaultItemPrices {
		defaults[p.ProductCode] = p
	}

	for _, want := range bot {
		got, ok := defaults[want.code]
		if !ok {
			t.Errorf("DefaultItemPrices is missing %q, which the bot sells at %d stars -- it cannot be priced from the admin panel", want.code, want.starsPrice)
			continue
		}
		if got.StarsPrice != want.starsPrice {
			t.Errorf("%q: DefaultItemPrices says %d stars, the bot charges %d", want.code, got.StarsPrice, want.starsPrice)
		}
		if got.Bid != want.bid {
			t.Errorf("%q: DefaultItemPrices says bid %d TON, the bot uses %d", want.code, got.Bid, want.bid)
		}
	}

	for _, p := range DefaultItemPrices {
		found := false
		for _, want := range bot {
			if want.code == p.ProductCode {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DefaultItemPrices lists %q, which the bot's catalog() does not sell -- it shows up in the admin shop-prices page as a product that cannot exist", p.ProductCode)
		}
	}
}

// TestBotStarPackagesAreNotPriced makes the other half of the mirror explicit:
// the bot's stars_N packages are computed from the rate rather than listed in
// `fixed`, so they must not appear as editable products. They change with
// stars_rate and an operator price on a rate-derived row would be meaningless.
func TestBotStarPackagesAreNotPriced(t *testing.T) {
	raw, err := os.ReadFile("../../cmd/bots/grammystore/src/catalog.js")
	if err != nil {
		t.Fatalf("read bot catalog: %v", err)
	}
	pkg := regexp.MustCompile(`const starPackages = \[([\d, ]+)\]`)
	m := pkg.FindStringSubmatch(string(raw))
	if m == nil {
		t.Skip("catalog.js no longer defines starPackages; re-check whether stars_N rows belong here")
	}
	for _, size := range strings.Split(m[1], ",") {
		size = strings.TrimSpace(size)
		if size == "" {
			continue
		}
		code := "stars_" + size
		for _, p := range DefaultItemPrices {
			if p.ProductCode == code {
				t.Errorf("DefaultItemPrices lists %q, but the bot derives it from stars_rate rather than reading a per-product price", code)
			}
		}
	}
}

// MergeItemPrices has to keep serving an override for a code the defaults do
// not know: a product added on the bot side first is still editable here
// without a backend release. That is the escape hatch that made the stale copy
// above survive so long, so it is worth pinning.
func TestMergeItemPricesAppendsUnknownOverride(t *testing.T) {
	defaults := []ItemPrice{{ProductCode: "premium_1m", StarsPrice: 20, Enabled: true}}
	overrides := []ItemPrice{
		{ProductCode: "premium_1m", StarsPrice: 25, Enabled: true, UpdatedBy: "ops"},
		{ProductCode: "brand_new", StarsPrice: 7, Enabled: true},
	}
	got := MergeItemPrices(defaults, overrides, false)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(got), got)
	}
	if got[0].StarsPrice != 25 || got[0].UpdatedBy != "ops" {
		t.Errorf("known default did not take the override: %+v", got[0])
	}
	if got[1].ProductCode != "brand_new" || got[1].StarsPrice != 7 {
		t.Errorf("unknown override was not appended: %+v", got[1])
	}
}
