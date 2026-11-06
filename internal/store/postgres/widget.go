package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// WidgetStore implements store.WidgetStore with PostgreSQL.
type WidgetStore struct {
	db sqlcgen.DBTX
}

// NewWidgetStore creates a WidgetStore backed by a pgx pool (or transaction).
func NewWidgetStore(db sqlcgen.DBTX) *WidgetStore {
	return &WidgetStore{db: db}
}

func (s *WidgetStore) CreateWidget(ctx context.Context, widget domain.BotWidget) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `
INSERT INTO bot_widgets (bot_user_id, html, payload, width, height, allow_queries, public_data, public_data_hash, cache_time)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING widget_id`,
		widget.BotUserID, widget.HTML, widget.Payload, widget.Width, widget.Height,
		widget.AllowQueries, widget.PublicData, widget.PublicDataHash, widget.CacheTime).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

func (s *WidgetStore) GetWidget(ctx context.Context, widgetID int64) (domain.BotWidget, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT widget_id, bot_user_id, html, payload, width, height, allow_queries, public_data, public_data_hash, cache_time
FROM bot_widgets
WHERE widget_id = $1`, widgetID)
	var widget domain.BotWidget
	err := row.Scan(
		&widget.WidgetID, &widget.BotUserID, &widget.HTML, &widget.Payload, &widget.Width, &widget.Height,
		&widget.AllowQueries, &widget.PublicData, &widget.PublicDataHash, &widget.CacheTime,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotWidget{}, false, nil
	}
	if err != nil {
		return domain.BotWidget{}, false, err
	}
	return widget, true, nil
}

func (s *WidgetStore) SetWidgetPublicData(ctx context.Context, widgetID int64, publicData string, hash int64, cacheTime int) error {
	tag, err := s.db.Exec(ctx, `
UPDATE bot_widgets
SET public_data = $2, public_data_hash = $3, cache_time = $4, updated_at = now()
WHERE widget_id = $1`, widgetID, publicData, hash, cacheTime)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrWidgetNotFound
	}
	return nil
}
