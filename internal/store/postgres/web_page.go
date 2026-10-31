package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

// web_pages is the link-preview resolution cache: deduplicated by a 63-bit hash of the normalized URL,
// with snapshot persisting the full domain.MessageWebPage snapshot. It uses raw pgx (not sqlc), so this
// table does not need to enter query code generation.

// PutWebPage upserts one resolution row; on conflict it overwrites the snapshot and refreshed_at by
// url_hash (created_at keeps the first value).
func (s *MediaStore) PutWebPage(ctx context.Context, urlHash int64, page domain.MessageWebPage, now int) error {
	snapshot, err := json.Marshal(page)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `
		INSERT INTO web_pages (url_hash, web_page_id, state, snapshot, created_at, refreshed_at)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (url_hash) DO UPDATE SET
			web_page_id  = EXCLUDED.web_page_id,
			state        = EXCLUDED.state,
			snapshot     = EXCLUDED.snapshot,
			refreshed_at = EXCLUDED.refreshed_at`,
		urlHash, page.ID, string(page.State), snapshot, int64(now))
	return err
}

// GetWebPageByURLHash reads back a resolved snapshot; found=false on a miss.
func (s *MediaStore) GetWebPageByURLHash(ctx context.Context, urlHash int64) (domain.MessageWebPage, int, bool, error) {
	var (
		snapshot    []byte
		refreshedAt int64
	)
	err := s.db.QueryRow(ctx, `SELECT snapshot, refreshed_at FROM web_pages WHERE url_hash = $1`, urlHash).
		Scan(&snapshot, &refreshedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.MessageWebPage{}, 0, false, nil
		}
		return domain.MessageWebPage{}, 0, false, err
	}
	var page domain.MessageWebPage
	if len(snapshot) > 0 {
		if err := json.Unmarshal(snapshot, &page); err != nil {
			return domain.MessageWebPage{}, 0, false, err
		}
	}
	return page, int(refreshedAt), true, nil
}
