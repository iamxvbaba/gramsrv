package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type GifCatalogStore struct{ db sqlcgen.DBTX }

func NewGifCatalogStore(db sqlcgen.DBTX) *GifCatalogStore { return &GifCatalogStore{db: db} }

func (s *GifCatalogStore) CreateGifCatalogEntry(ctx context.Context, entry domain.GifCatalogEntry) (domain.GifCatalogEntry, error) {
	if entry.ID == 0 || entry.DocumentID == 0 {
		return domain.GifCatalogEntry{}, domain.ErrGifCatalogEntryInvalid
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO gif_catalog
    (id, title, file_name, document_id, enabled, sort_order, created_by, source_filename, source_sha256)
VALUES ($1, $2, $3, $4, true, $5, $6, $7, $8)
RETURNING id, title, document_id, enabled, sort_order, created_by,
          source_filename, source_sha256, file_name, category_override, created_at, updated_at`, entry.ID, entry.Title, entry.FileName, entry.DocumentID, entry.SortOrder, entry.CreatedBy,
		entry.SourceFilename, entry.SourceSHA256)
	out, err := scanGifCatalogEntry(row.Scan)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "gif_catalog_capacity_limit" {
		return domain.GifCatalogEntry{}, domain.ErrGifCatalogFull
	}
	if err != nil {
		return domain.GifCatalogEntry{}, fmt.Errorf("create gif catalog entry: %w", err)
	}
	return out, nil
}

func (s *GifCatalogStore) GifCatalogSeedMatches(ctx context.Context, filename, sha256 string) (bool, bool, error) {
	if filename == "" || sha256 == "" {
		return false, false, nil
	}
	var byName, byDigest bool
	err := s.db.QueryRow(ctx, `
SELECT EXISTS(SELECT 1 FROM gif_catalog WHERE source_filename = $1),
       EXISTS(SELECT 1 FROM gif_catalog WHERE source_sha256 = $2)`, filename, sha256).Scan(&byName, &byDigest)
	if err != nil {
		return false, false, fmt.Errorf("check gif catalog seed identity: %w", err)
	}
	return byName, byDigest, nil
}

func (s *GifCatalogStore) ListGifCatalog(ctx context.Context, onlyEnabled bool) ([]domain.GifCatalogEntry, error) {
	rows, err := s.db.Query(ctx, `
SELECT id, title, document_id, enabled, sort_order, created_by,
       source_filename, source_sha256, file_name, category_override, created_at, updated_at
FROM gif_catalog
WHERE NOT $1 OR enabled
ORDER BY sort_order, id`, onlyEnabled)
	if err != nil {
		return nil, fmt.Errorf("list gif catalog: %w", err)
	}
	defer rows.Close()
	out := make([]domain.GifCatalogEntry, 0, domain.MaxGifCatalogEntries)
	for rows.Next() {
		item, err := scanGifCatalogEntry(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan gif catalog entry: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *GifCatalogStore) SetGifCatalogEnabled(ctx context.Context, id int64, enabled bool) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE gif_catalog SET enabled=$2, updated_at=now() WHERE id=$1`, id, enabled)
	if err != nil {
		return false, fmt.Errorf("set gif catalog enabled: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *GifCatalogStore) SetGifCatalogSortOrder(ctx context.Context, id int64, order int) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE gif_catalog SET sort_order=$2, updated_at=now() WHERE id=$1`, id, order)
	if err != nil {
		return false, fmt.Errorf("set gif catalog sort order: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *GifCatalogStore) SetGifCatalogCategory(ctx context.Context, id int64, category string) (bool, error) {
	if category != "" && !domain.ValidGifCategory(category) {
		return false, domain.ErrGifCatalogEntryInvalid
	}
	tag, err := s.db.Exec(ctx, `UPDATE gif_catalog SET category_override=NULLIF($2, ''), updated_at=now() WHERE id=$1`, id, category)
	if err != nil {
		return false, fmt.Errorf("set gif catalog category: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *GifCatalogStore) DeleteGifCatalogEntry(ctx context.Context, id int64) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM gif_catalog WHERE id=$1`, id)
	if err != nil {
		return false, fmt.Errorf("delete gif catalog entry: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func scanGifCatalogEntry(scan func(...any) error) (domain.GifCatalogEntry, error) {
	var e domain.GifCatalogEntry
	var categoryOverride *string
	err := scan(&e.ID, &e.Title, &e.DocumentID, &e.Enabled, &e.SortOrder, &e.CreatedBy,
		&e.SourceFilename, &e.SourceSHA256, &e.FileName, &categoryOverride, &e.CreatedAt, &e.UpdatedAt)
	if err == nil {
		if categoryOverride != nil {
			e.Category, e.CategoryManual = *categoryOverride, true
		} else {
			e.Category = domain.ClassifyGifCategory(e.Title, strings.TrimSpace(e.FileName))
		}
	}
	return e, err
}
