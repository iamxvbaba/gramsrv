package domain

import "sort"

// ItemPrice is an admin-configurable price for a catalog product. A row in
// item_prices overrides the catalog default for that product; enabled=false
// hides the product from the shop. Products without a row follow
// DefaultItemPrices.
type ItemPrice struct {
	ProductCode string `json:"product_code"` // e.g., "premium_1m", "uname_10"
	Title       string `json:"title,omitempty"`
	StarsPrice  int    `json:"stars_price"` // price in Telegram Stars
	Bid         int64  `json:"bid"`         // for username products (TON bid)
	Enabled     bool   `json:"enabled"`
	UpdatedBy   string `json:"updated_by,omitempty"`
	UpdatedAt   int64  `json:"updated_at,omitempty"` // unix timestamp
}

// ItemPriceListRequest filters the price list.
type ItemPriceListRequest struct {
	EnabledOnly bool `json:"enabled_only"`
}

// ItemPriceUpdateRequest updates a single product's price.
type ItemPriceUpdateRequest struct {
	ProductCode string `json:"product_code"`
	StarsPrice  int    `json:"stars_price"`
	Bid         int64  `json:"bid"`
	Enabled     bool   `json:"enabled"`
}

// ShopSettingStarsRate is the shop_settings key holding how many server Stars
// one Telegram Star buys — the rate the bot multiplies a Stars purchase by, and
// the only input to the stars_N packages.
const ShopSettingStarsRate = "stars_rate"

// DefaultItemPrices mirrors the `fixed` array of the store bot's catalog.js
// (cmd/bots/grammystore/src/catalog.js): the prices a fresh deployment shows
// before any override row exists. The bot treats the server list as
// authoritative for what it charges, so an admin edit here changes the shop.
//
// This list is a copy of another repository's file, and a copy is what goes
// stale quietly. It had: three products were priced differently from the bot,
// two of the three username tiers were missing entirely -- so they could never
// be priced from the panel at all -- and three rows were listed for products
// the bot does not sell. TestDefaultItemPricesMirrorBotCatalog parses
// catalog.js and fails on any difference, in either direction; keep the two in
// step by changing both, not this file alone.
//
// Titles come from the bot's catalog too, minus the FlashGram prefix: the
// server's own name is configurable (see Server Settings -> Server identity)
// and the shop should read as the operator's, not as the reference build's.
//
// The bot's stars_N packages are deliberately absent. It derives them from
// shop_settings.stars_rate rather than reading a per-product price, so there is
// nothing for a row here to override -- they are surfaced on the admin page as
// read-only instead. TestBotStarPackagesAreNotPriced pins that.
var DefaultItemPrices = []ItemPrice{
	{ProductCode: "premium_1m", Title: "Premium — 1 month", StarsPrice: 20, Enabled: true},
	{ProductCode: "premium_3m", Title: "Premium — 3 months", StarsPrice: 40, Enabled: true},
	{ProductCode: "num_short", Title: "Anonymous +888 8 XXX", StarsPrice: 50, Enabled: true},
	{ProductCode: "num_long", Title: "Anonymous +888 0XXX XXXX", StarsPrice: 25, Enabled: true},
	{ProductCode: "uname_10", Title: "Collectible username — 10 TON", StarsPrice: 10, Bid: 10, Enabled: true},
	{ProductCode: "uname_100", Title: "Collectible username — 100 TON", StarsPrice: 20, Bid: 100, Enabled: true},
	{ProductCode: "uname_1000", Title: "Collectible username — 1000 TON", StarsPrice: 40, Bid: 1000, Enabled: true},
}

// DefaultStarsPackageSizes are the Telegram-Star package sizes the store bot
// offers, in the order catalog.js lists them. Each is sold for that many
// Telegram Stars and credits package * stars_rate server Stars, so its price is
// the package size itself rather than a per-product figure: the admin page
// lists these read-only, and the single rate above them is what changes them.
var DefaultStarsPackageSizes = []int{1, 5, 10, 25, 50, 100}

// MergeItemPrices overlays stored overrides onto the catalog defaults. Rows
// for codes the defaults do not know (a product added on the bot side first)
// are appended after the defaults so a new product is still editable here
// without a backend release.
func MergeItemPrices(defaults, overrides []ItemPrice, enabledOnly bool) []ItemPrice {
	byCode := make(map[string]ItemPrice, len(overrides))
	for _, o := range overrides {
		byCode[o.ProductCode] = o
	}
	out := make([]ItemPrice, 0, len(defaults)+len(overrides))
	known := make(map[string]struct{}, len(defaults))
	for _, d := range defaults {
		p := d
		if o, ok := byCode[d.ProductCode]; ok {
			p.StarsPrice = o.StarsPrice
			p.Bid = o.Bid
			p.Enabled = o.Enabled
			p.UpdatedBy = o.UpdatedBy
			p.UpdatedAt = o.UpdatedAt
		}
		known[d.ProductCode] = struct{}{}
		if enabledOnly && !p.Enabled {
			continue
		}
		out = append(out, p)
	}
	var extra []ItemPrice
	for _, o := range overrides {
		if _, ok := known[o.ProductCode]; ok {
			continue
		}
		if enabledOnly && !o.Enabled {
			continue
		}
		extra = append(extra, o)
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i].ProductCode < extra[j].ProductCode })
	return append(out, extra...)
}
