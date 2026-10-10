package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// errGiftNotFound maps a missing catalog row to 400 instead of 500.
var errGiftNotFound = errors.New("gift not found")

// Upgrade attribute pools for the model/pattern/backdrop picker: published
// collectible revision of a catalog gift. Reads come from the panel's own
// readStore. NOTE: this schema has no catalog.visual_only column, so every
// gift reports visual_only=false.

type giftAttributeRow struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Rarity int    `json:"rarity"`
}

type giftBackdropRow struct {
	giftAttributeRow
	BackdropID int `json:"backdrop_id"`
}

// giftUpgradePools returns the published collectible revision's attribute
// pools for a catalog gift. A missing/empty revision means no upgrade.
func (s *readStore) giftUpgradePools(ctx context.Context, giftID int64) (revID int64, visualOnly bool, models, patterns []giftAttributeRow, backdrops []giftBackdropRow, err error) {
	models = []giftAttributeRow{}
	patterns = []giftAttributeRow{}
	backdrops = []giftBackdropRow{}
	if s == nil || s.pool == nil {
		return 0, false, models, patterns, backdrops, fmt.Errorf("gifts store is not configured")
	}
	var rev sql.NullInt64
	if err := s.pool.QueryRow(ctx, `SELECT collectible_revision_id FROM star_gift_catalog WHERE gift_id = $1`, giftID).Scan(&rev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, models, patterns, backdrops, errGiftNotFound
		}
		return 0, false, models, patterns, backdrops, fmt.Errorf("gift catalog: %w", err)
	}
	if !rev.Valid {
		return 0, false, models, patterns, backdrops, nil
	}
	var status string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM star_gift_collectible_revisions WHERE id = $1`, rev.Int64).Scan(&status); err != nil || status != "published" {
		if err != nil {
			return 0, false, models, patterns, backdrops, fmt.Errorf("gift revision: %w", err)
		}
		return 0, false, models, patterns, backdrops, nil
	}
	revID = rev.Int64
	if models, err = scanGiftAttributes(ctx, s, `SELECT id, name, rarity_permille FROM star_gift_collectible_models WHERE collectible_revision_id = $1 ORDER BY sort_order, id`, revID); err != nil {
		return 0, false, models, patterns, backdrops, err
	}
	if patterns, err = scanGiftAttributes(ctx, s, `SELECT id, name, rarity_permille FROM star_gift_collectible_patterns WHERE collectible_revision_id = $1 ORDER BY sort_order, id`, revID); err != nil {
		return 0, false, models, patterns, backdrops, err
	}
	rows, err := s.pool.Query(ctx, `SELECT id, name, rarity_permille, backdrop_id FROM star_gift_collectible_backdrops WHERE collectible_revision_id = $1 ORDER BY sort_order, id`, revID)
	if err != nil {
		return 0, false, models, patterns, backdrops, fmt.Errorf("gift backdrops: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row giftBackdropRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Rarity, &row.BackdropID); err != nil {
			return 0, false, models, patterns, backdrops, fmt.Errorf("gift backdrops: %w", err)
		}
		backdrops = append(backdrops, row)
	}
	return revID, false, models, patterns, backdrops, rows.Err()
}

func scanGiftAttributes(ctx context.Context, s *readStore, query string, revID int64) ([]giftAttributeRow, error) {
	out := []giftAttributeRow{}
	rows, err := s.pool.Query(ctx, query, revID)
	if err != nil {
		return out, fmt.Errorf("gift attributes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row giftAttributeRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Rarity); err != nil {
			return out, fmt.Errorf("gift attributes: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *server) handleGiftAttributesAPI(w http.ResponseWriter, r *http.Request) {
	giftID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || giftID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid gift id")
		return
	}
	revID, visualOnly, models, patterns, backdrops, err := s.read.giftUpgradePools(r.Context(), giftID)
	if err != nil {
		if errors.Is(err, errGiftNotFound) {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"gift_id":     giftID,
		"visual_only": visualOnly,
		"has_upgrade": revID > 0,
		"models":      models,
		"patterns":    patterns,
		"backdrops":   backdrops,
	})
}
