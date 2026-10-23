package domain

import (
	"errors"
	"fmt"
	"strings"
)

// BotShopConfig is the price list of the numbers bot shop: ShuzaGram Stars
// and Premium sold for Telegram Stars, TON/USDT (Tonkeeper) or UAH by card.
// The admin panel edits it; the bot reads it through the admin API.
type BotShopConfig struct {
	Enabled bool `json:"enabled"`

	// StarPacks are the ready-made ShuzaGram Stars amounts.
	StarPacks []int64 `json:"star_packs"`
	// Custom amounts between CustomMin and CustomMax are priced by the rates.
	CustomEnabled bool  `json:"custom_enabled"`
	CustomMin     int64 `json:"custom_min"`
	CustomMax     int64 `json:"custom_max"`

	// Rates are given per RateBase ShuzaGram Stars (200 by default).
	RateBase int64 `json:"rate_base"`
	// SGStarsPerTGStar: how many ShuzaGram Stars one Telegram Star buys.
	SGStarsPerTGStar int64   `json:"sg_stars_per_tg_star"`
	USDPerBase       float64 `json:"usd_per_base"`
	UAHPerBase       float64 `json:"uah_per_base"`

	// Premium is sold by month; longer plans cost months × the monthly price.
	PremiumEnabled     bool    `json:"premium_enabled"`
	PremiumMonths      []int   `json:"premium_months"`
	PremiumTGStarsPerM int64   `json:"premium_tg_stars_per_month"`
	PremiumUSDPerM     float64 `json:"premium_usd_per_month"`
	PremiumUAHPerM     float64 `json:"premium_uah_per_month"`

	// Payment methods.
	PayTGStars  bool    `json:"pay_tg_stars"`
	PayUSDT     bool    `json:"pay_usdt"`
	PayTON      bool    `json:"pay_ton"`
	TONAddress  string  `json:"ton_address"`
	TONUSDRate  float64 `json:"ton_usd_rate"` // USD for 1 TON; needed for TON payments
	PayCard     bool    `json:"pay_card"`
	CardNumber  string  `json:"card_number"`
	CardHolder  string  `json:"card_holder"`
	CardKopMax  int     `json:"card_kop_max"` // random 0..N kopecks added to each card price
	ShopMessage string  `json:"shop_message"` // optional text shown on the shop screen
}

var ErrBotShopConfigInvalid = errors.New("bot shop config invalid")

// DefaultBotShopConfig is the shop before anything is changed in the panel.
func DefaultBotShopConfig() BotShopConfig {
	return BotShopConfig{
		Enabled:            true,
		StarPacks:          []int64{200, 1000, 2000, 3000, 5000, 10000, 25000, 50000, 100000},
		CustomEnabled:      true,
		CustomMin:          200,
		CustomMax:          1000000,
		RateBase:           200,
		SGStarsPerTGStar:   1000,
		USDPerBase:         0.002,
		UAHPerBase:         0.7,
		PremiumEnabled:     true,
		PremiumMonths:      []int{1, 3, 6, 12},
		PremiumTGStarsPerM: 50,
		PremiumUSDPerM:     0.79,
		PremiumUAHPerM:     35,
		PayTGStars:         true,
		// Wallet address and card are set in the admin panel; none ship by default.
		PayUSDT:    false,
		PayTON:     false,
		PayCard:    false,
		CardKopMax: 30,
	}
}

// Validate checks the config is usable by the bot.
func (c BotShopConfig) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrBotShopConfigInvalid, fmt.Sprintf(format, args...))
	}
	if len(c.StarPacks) > 20 {
		return bad("at most 20 star packs")
	}
	for _, p := range c.StarPacks {
		if p <= 0 || p > 100_000_000 {
			return bad("star pack %d out of range", p)
		}
	}
	if c.RateBase <= 0 {
		return bad("rate base must be positive")
	}
	if c.CustomEnabled && (c.CustomMin <= 0 || c.CustomMax < c.CustomMin || c.CustomMax > 100_000_000) {
		return bad("custom amount range is invalid")
	}
	if c.SGStarsPerTGStar < 0 || c.USDPerBase < 0 || c.UAHPerBase < 0 || c.TONUSDRate < 0 ||
		c.PremiumTGStarsPerM < 0 || c.PremiumUSDPerM < 0 || c.PremiumUAHPerM < 0 {
		return bad("rates must not be negative")
	}
	if len(c.PremiumMonths) > 8 {
		return bad("at most 8 premium plans")
	}
	for _, m := range c.PremiumMonths {
		if m < 1 || m > 36 {
			return bad("premium plan of %d months out of range", m)
		}
	}
	if c.CardKopMax < 0 || c.CardKopMax > 99 {
		return bad("card kopecks must be 0..99")
	}
	if len(c.TONAddress) > 128 || len(c.CardNumber) > 64 || len(c.CardHolder) > 128 || len(c.ShopMessage) > 2000 {
		return bad("text field too long")
	}
	if (c.PayUSDT || c.PayTON) && strings.TrimSpace(c.TONAddress) == "" {
		return bad("TON address is required for TON/USDT payments")
	}
	if c.PayTON && c.TONUSDRate <= 0 {
		return bad("TON payments need the TON/USD rate")
	}
	if c.PayCard && strings.TrimSpace(c.CardNumber) == "" {
		return bad("card number is required for card payments")
	}
	return nil
}
