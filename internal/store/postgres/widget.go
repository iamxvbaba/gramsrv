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

func createBotWidgetParams(widgetID int64, widget domain.BotWidget) sqlcgen.CreateBotWidgetParams {
	return sqlcgen.CreateBotWidgetParams{
		WidgetID: widgetID, BotUserID: widget.BotUserID, Payload: widget.Payload,
		Width: int32(widget.Width), Height: int32(widget.Height), AllowQueries: widget.AllowQueries,
		PublicData: widget.PublicData, PublicDataHash: widget.PublicDataHash, CacheTime: int32(widget.CacheTime),
	}
}

func (s *WidgetStore) CreateWidget(ctx context.Context, widget domain.BotWidget) (int64, error) {
	queries := sqlcgen.New(s.db)
	if widget.WidgetID != 0 {
		// Preassigned id: the shared definition is normally persisted in the
		// committed send transaction, but honoring an explicit id keeps direct
		// callers (and tests) able to pin it.
		rows, err := queries.CreateBotWidgetIfAbsent(ctx,
			sqlcgen.CreateBotWidgetIfAbsentParams(createBotWidgetParams(widget.WidgetID, widget)))
		if err != nil {
			return 0, err
		}
		if rows == 1 {
			return widget.WidgetID, nil
		}
		// The id already exists: accept only the same bot's definition, never
		// silently link to another bot's widget.
		existing, err := queries.GetBotWidget(ctx, widget.WidgetID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, domain.ErrWidgetNotFound
		}
		if err != nil {
			return 0, err
		}
		if existing.BotUserID != widget.BotUserID {
			return 0, domain.ErrWidgetNotFound
		}
		return widget.WidgetID, nil
	}
	for attempt := 0; attempt < 32; attempt++ {
		id := randomWidgetID()
		err := queries.CreateBotWidget(ctx, createBotWidgetParams(id, widget))
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
	row, err := sqlcgen.New(s.db).GetBotWidget(ctx, widgetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotWidget{}, false, nil
	}
	if err != nil {
		return domain.BotWidget{}, false, err
	}
	return domain.BotWidget{
		WidgetID: row.WidgetID, BotUserID: row.BotUserID, Payload: row.Payload,
		Width: int(row.Width), Height: int(row.Height), AllowQueries: row.AllowQueries,
		PublicData: row.PublicData, PublicDataHash: row.PublicDataHash, CacheTime: int(row.CacheTime),
	}, true, nil
}

func (s *WidgetStore) SetWidgetPublicData(ctx context.Context, widgetID int64, publicData string, hash int64, cacheTime int) error {
	rows, err := sqlcgen.New(s.db).SetBotWidgetPublicData(ctx, sqlcgen.SetBotWidgetPublicDataParams{
		WidgetID: widgetID, PublicData: publicData, PublicDataHash: hash, CacheTime: int32(cacheTime),
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return domain.ErrWidgetNotFound
	}
	return nil
}
