package admin

import (
	"context"
	"fmt"
	"strings"

	"telesrv/internal/domain"
)

// Shop prices: per-product overrides and the shop-wide stars rate the bot
// multiplies Telegram Star purchases by.
//
// Every mutation runs through runCommand, so each price change is journalled
// in admin_commands / admin_audit_logs and rehearsable with a dry run — the
// same contract as every other money-adjacent surface here. Reads merge
// stored overrides with domain.DefaultItemPrices above the store, so the
// defaults can change in a release without migrating rows.

const (
	maxItemPriceCodeBytes   = 64
	maxItemPriceStarsPrice  = 1_000_000
	maxItemPriceBid         = 1_000_000_000
	maxShopSettingRateValue = 1_000_000
)

// ItemPricesStore is the operator-facing persistence slice: the item_prices
// override rows and the shop_settings key/value rows. Implemented by
// postgres.ItemPriceStore.
type ItemPricesStore interface {
	ListItemPrices(ctx context.Context, enabledOnly bool) ([]domain.ItemPrice, error)
	UpsertItemPrice(ctx context.Context, price domain.ItemPrice) (domain.ItemPrice, error)
	GetShopSetting(ctx context.Context, key string) (value int64, found bool, err error)
	SetShopSetting(ctx context.Context, key string, value int64, updatedBy string) error
}

// UpdateItemPriceRequest upserts one product's price override. Enabled is the
// shop kill switch for that product: false removes it from the bot's catalog.
type UpdateItemPriceRequest struct {
	CommandMeta
	ProductCode string `json:"product_code"`
	StarsPrice  int    `json:"stars_price"`
	Bid         int64  `json:"bid"`
	Enabled     bool   `json:"enabled"`
}

// SetStarsRateRequest changes how many FG Stars one Telegram Star converts
// to — the multiplier the bot applies to star purchases.
type SetStarsRateRequest struct {
	CommandMeta
	StarsRate int64 `json:"stars_rate"`
}

// ItemPrices lists the effective price of every known product: stored
// overrides merged over the catalog defaults.
func (s *Service) ItemPrices(ctx context.Context, enabledOnly bool) ([]domain.ItemPrice, error) {
	if s == nil || s.itemPrices == nil {
		return nil, fmt.Errorf("admin item prices dependency is not configured")
	}
	overrides, err := s.itemPrices.ListItemPrices(ctx, enabledOnly)
	if err != nil {
		return nil, err
	}
	return domain.MergeItemPrices(domain.DefaultItemPrices, overrides, enabledOnly), nil
}

// StarsRate returns the stored FG Stars rate, or 0 when never set — callers
// that must show something treat 0 as "not configured yet" rather than
// silently substituting a value the operator never chose.
func (s *Service) StarsRate(ctx context.Context) (int64, error) {
	if s == nil || s.itemPrices == nil {
		return 0, fmt.Errorf("admin item prices dependency is not configured")
	}
	value, _, err := s.itemPrices.GetShopSetting(ctx, domain.ShopSettingStarsRate)
	if err != nil {
		return 0, err
	}
	return value, nil
}

// UpdateItemPrice validates and saves one product's price override.
func (s *Service) UpdateItemPrice(ctx context.Context, req UpdateItemPriceRequest) (CommandResult, error) {
	code := strings.TrimSpace(req.ProductCode)
	if code == "" || len(code) > maxItemPriceCodeBytes {
		return CommandResult{}, fmt.Errorf("product_code is required and must be <= %d bytes", maxItemPriceCodeBytes)
	}
	if req.StarsPrice < 0 || req.StarsPrice > maxItemPriceStarsPrice {
		return CommandResult{}, fmt.Errorf("stars_price must be between 0 and %d", maxItemPriceStarsPrice)
	}
	if req.Bid < 0 || req.Bid > maxItemPriceBid {
		return CommandResult{}, fmt.Errorf("bid must be between 0 and %d", maxItemPriceBid)
	}
	if s == nil || s.itemPrices == nil {
		return CommandResult{}, fmt.Errorf("admin item prices dependency is not configured")
	}
	req.ProductCode = code
	return s.runCommand(ctx, req.CommandMeta, ActionUpdateItemPrice, 0, domain.Peer{}, req,
		func() (CommandResult, error) {
			current, known, err := s.itemPricesEffective(ctx, code)
			if err != nil {
				return CommandResult{}, err
			}
			details := map[string]any{
				"product_code": code,
				"stars_price":  req.StarsPrice,
				"bid":          req.Bid,
				"enabled":      req.Enabled,
				"current":      current,
				"known":        known,
			}
			if req.DryRun {
				return CommandResult{Message: "item price validated", Details: details}, nil
			}
			saved, err := s.itemPrices.UpsertItemPrice(ctx, domain.ItemPrice{
				ProductCode: code,
				StarsPrice:  req.StarsPrice,
				Bid:         req.Bid,
				Enabled:     req.Enabled,
				UpdatedBy:   req.Actor,
			})
			if err != nil {
				return CommandResult{Details: details}, err
			}
			details["saved"] = saved
			return CommandResult{Message: "item price saved", Details: details}, nil
		})
}

// SetStarsRate saves the FG Stars purchase rate.
func (s *Service) SetStarsRate(ctx context.Context, req SetStarsRateRequest) (CommandResult, error) {
	if req.StarsRate <= 0 || req.StarsRate > maxShopSettingRateValue {
		return CommandResult{}, fmt.Errorf("stars_rate must be between 1 and %d", maxShopSettingRateValue)
	}
	if s == nil || s.itemPrices == nil {
		return CommandResult{}, fmt.Errorf("admin item prices dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetStarsRate, 0, domain.Peer{}, req,
		func() (CommandResult, error) {
			current, err := s.StarsRate(ctx)
			if err != nil {
				return CommandResult{}, err
			}
			details := map[string]any{
				"stars_rate": req.StarsRate,
				"current":    current,
			}
			if req.DryRun {
				return CommandResult{Message: "stars rate validated", Details: details}, nil
			}
			if err := s.itemPrices.SetShopSetting(ctx, domain.ShopSettingStarsRate,
				req.StarsRate, req.Actor); err != nil {
				return CommandResult{Details: details}, err
			}
			return CommandResult{Message: "stars rate saved", Details: details}, nil
		})
}

// itemPricesEffective resolves one product's current merged price for a dry
// run's "current" details. A product with no row reports the catalog default.
func (s *Service) itemPricesEffective(ctx context.Context, code string) (domain.ItemPrice, bool, error) {
	rows, err := s.ItemPrices(ctx, false)
	if err != nil {
		return domain.ItemPrice{}, false, err
	}
	for _, row := range rows {
		if row.ProductCode == code {
			return row, true, nil
		}
	}
	return domain.ItemPrice{}, false, nil
}
