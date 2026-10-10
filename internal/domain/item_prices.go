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

// ShopSettingStarsRate is the shop_settings key holding FG Stars per one
// Telegram Star — the rate the bot multiplies star purchases by.
const ShopSettingStarsRate = "stars_rate"

// DefaultItemPrices mirrors the flashgram-bot catalog(): the prices a fresh
// deployment shows before any override row exists. The bot treats the server
// list as authoritative, so keeping this in sync with the bot's catalog() is
// what makes an admin edit visible everywhere at once.
var DefaultItemPrices = []ItemPrice{
	{ProductCode: "premium_1m", Title: "FlashGram Premium — 1 месяц", StarsPrice: 20, Enabled: true},
	{ProductCode: "premium_3m", Title: "FlashGram Premium — 3 месяца", StarsPrice: 40, Enabled: true},
	{ProductCode: "num_short", Title: "Анонимный номер +888 8 XXX", StarsPrice: 125, Enabled: true},
	{ProductCode: "num_long", Title: "Анонимный номер +888 0XXX XXXX", StarsPrice: 50, Enabled: true},
	{ProductCode: "uname_10", Title: "NFT Username (5-32)", StarsPrice: 50, Bid: 10, Enabled: true},
	{ProductCode: "uname_5000", Title: "NFT Username (4 - @user)", StarsPrice: 85, Bid: 5000, Enabled: true},
	{ProductCode: "verify_major", Title: "Сторонняя верификация — @newmajorbot", StarsPrice: 200, Enabled: true},
	{ProductCode: "verify_hold", Title: "Сторонняя верификация — @holddbot", StarsPrice: 200, Enabled: true},
}

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
