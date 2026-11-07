package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// randomWidgetID returns an unpredictable non-zero widget id: ids are referenced
// by inputMediaWidgetID, so a sequential id would let a bot enumerate every
// shared widget.
func randomWidgetID() int64 {
	return domain.RandomWidgetID()
}

func (s *WidgetStore) CreateWidget(ctx context.Context, widget domain.BotWidget) (int64, error) {
	if widget.WidgetID != 0 {
		// Preassigned id: the shared definition is normally persisted in the
		// committed send transaction, but honoring an explicit id keeps direct
		// callers (and tests) able to pin it. Idempotent on a duplicate id.
		if _, err := s.db.Exec(ctx, `
INSERT INTO bot_widgets (widget_id, bot_user_id, html, payload, width, height, allow_queries, public_data, public_data_hash, cache_time)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (widget_id) DO NOTHING`,
			widget.WidgetID, widget.BotUserID, widget.HTML, widget.Payload, widget.Width, widget.Height,
			widget.AllowQueries, widget.PublicData, widget.PublicDataHash, widget.CacheTime); err != nil {
			return 0, err
		}
		return widget.WidgetID, nil
	}
	for attempt := 0; attempt < 32; attempt++ {
		id := randomWidgetID()
		_, err := s.db.Exec(ctx, `
INSERT INTO bot_widgets (widget_id, bot_user_id, html, payload, width, height, allow_queries, public_data, public_data_hash, cache_time)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			id, widget.BotUserID, widget.HTML, widget.Payload, widget.Width, widget.Height,
			widget.AllowQueries, widget.PublicData, widget.PublicDataHash, widget.CacheTime)
		if err == nil {
			return id, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation {
			continue
		}
		return 0, err
	}
	return 0, fmt.Errorf("allocate widget id")
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
