package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// ItemPriceStore persists the admin-configurable shop prices: per-product
// overrides in item_prices (migration 20260926010000) and shop-wide settings
// in shop_settings (migration 20260927010000). It stores overrides only —
// catalog defaults live in domain.DefaultItemPrices, and merging happens above
// the store so a default change never needs a data migration.
type ItemPriceStore struct {
	db sqlcgen.DBTX
}

// NewItemPriceStore builds the store on a pgx pool or transaction.
func NewItemPriceStore(db sqlcgen.DBTX) *ItemPriceStore {
	return &ItemPriceStore{db: db}
}

// ListItemPrices returns stored overrides, optionally only enabled ones.
func (s *ItemPriceStore) ListItemPrices(ctx context.Context, enabledOnly bool) ([]domain.ItemPrice, error) {
	query := `SELECT product_code, stars_price, bid, enabled, updated_by,
extract(epoch FROM updated_at)::bigint AS updated_at
FROM item_prices`
	if enabledOnly {
		query += ` WHERE enabled`
	}
	query += ` ORDER BY product_code`
	rows, err := s.db.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list item prices: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ItemPrice, 0)
	for rows.Next() {
		var p domain.ItemPrice
		if err := rows.Scan(&p.ProductCode, &p.StarsPrice, &p.Bid, &p.Enabled,
			&p.UpdatedBy, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan item price: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertItemPrice stores one product's override, replacing any previous row.
func (s *ItemPriceStore) UpsertItemPrice(ctx context.Context, p domain.ItemPrice) (domain.ItemPrice, error) {
	row := s.db.QueryRow(ctx, `INSERT INTO item_prices (product_code, stars_price, bid, enabled, updated_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (product_code) DO UPDATE SET
stars_price = EXCLUDED.stars_price,
bid = EXCLUDED.bid,
enabled = EXCLUDED.enabled,
updated_by = EXCLUDED.updated_by,
updated_at = now()
RETURNING product_code, stars_price, bid, enabled, updated_by,
extract(epoch FROM updated_at)::bigint AS updated_at`,
		p.ProductCode, p.StarsPrice, p.Bid, p.Enabled, p.UpdatedBy)
	var out domain.ItemPrice
	if err := row.Scan(&out.ProductCode, &out.StarsPrice, &out.Bid, &out.Enabled,
		&out.UpdatedBy, &out.UpdatedAt); err != nil {
		return domain.ItemPrice{}, fmt.Errorf("upsert item price: %w", err)
	}
	return out, nil
}

// GetShopSetting reads one shop-wide setting. found=false means the key was
// never written; callers then fall back to their own default rather than
// inventing a stored value.
func (s *ItemPriceStore) GetShopSetting(ctx context.Context, key string) (value int64, found bool, err error) {
	if err := s.db.QueryRow(ctx,
		`SELECT value FROM shop_settings WHERE key = $1`, key,
	).Scan(&value); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read shop setting %q: %w", key, err)
	}
	return value, true, nil
}

// SetShopSetting writes one shop-wide setting.
func (s *ItemPriceStore) SetShopSetting(ctx context.Context, key string, value int64, updatedBy string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO shop_settings (key, value, updated_by)
VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET
value = EXCLUDED.value,
updated_by = EXCLUDED.updated_by,
updated_at = now()`, key, value, updatedBy)
	if err != nil {
		return fmt.Errorf("write shop setting %q: %w", key, err)
	}
	return nil
}
