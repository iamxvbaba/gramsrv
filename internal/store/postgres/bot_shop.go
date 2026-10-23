package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// BotShopStore keeps the numbers bot shop price list.
type BotShopStore struct {
	db sqlcgen.DBTX
}

func NewBotShopStore(db sqlcgen.DBTX) *BotShopStore {
	return &BotShopStore{db: db}
}

// BotShopConfig returns the stored config, or the default one.
func (s *BotShopStore) BotShopConfig(ctx context.Context) (domain.BotShopConfig, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT config FROM bot_shop_config WHERE id = 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DefaultBotShopConfig(), nil
	}
	if err != nil {
		return domain.BotShopConfig{}, fmt.Errorf("load bot shop config: %w", err)
	}
	cfg := domain.DefaultBotShopConfig()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return domain.BotShopConfig{}, fmt.Errorf("decode bot shop config: %w", err)
	}
	return cfg, nil
}

// SetBotShopConfig validates and stores the config.
func (s *BotShopStore) SetBotShopConfig(ctx context.Context, cfg domain.BotShopConfig, actor string) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
INSERT INTO bot_shop_config (id, config, updated_by, updated_at) VALUES (1, $1, $2, now())
ON CONFLICT (id) DO UPDATE SET config = EXCLUDED.config, updated_by = EXCLUDED.updated_by, updated_at = now()`, raw, actor)
	if err != nil {
		return fmt.Errorf("save bot shop config: %w", err)
	}
	return nil
}
