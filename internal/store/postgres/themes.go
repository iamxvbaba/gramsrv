package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// ThemeStore implements store.ThemeStore with PostgreSQL (raw pgx, consistent with recent features, not via sqlc).
type ThemeStore struct {
	db sqlcgen.DBTX
}

// NewThemeStore creates a ThemeStore on top of a pgx pool (or transaction).
func NewThemeStore(db sqlcgen.DBTX) *ThemeStore {
	return &ThemeStore{db: db}
}

const themeColumns = `id, access_hash, creator_user_id, slug, title, emoticon, for_chat, document_id, settings, installs_count, created_at`

func (s *ThemeStore) CreateTheme(ctx context.Context, t domain.Theme) error {
	if t.ID == 0 || t.CreatorUserID == 0 {
		return domain.ErrThemeInvalid
	}
	settings, err := marshalThemeSettings(t.Settings)
	if err != nil {
		return err
	}
	createdAt := time.Now()
	if t.CreatedAt > 0 {
		createdAt = time.Unix(t.CreatedAt, 0)
	}
	_, err = s.db.Exec(ctx, `
INSERT INTO themes (
  id, access_hash, creator_user_id, slug, title, emoticon, for_chat, document_id, settings, installs_count, created_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		t.ID, t.AccessHash, t.CreatorUserID, t.Slug, t.Title, t.Emoticon, t.ForChat,
		t.DocumentID, settings, t.InstallsCount, createdAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrThemeSlugTaken
		}
		return fmt.Errorf("insert theme: %w", err)
	}
	return nil
}

func scanTheme(row pgx.Row) (domain.Theme, error) {
	var (
		t         domain.Theme
		settings  []byte
		createdAt time.Time
	)
	if err := row.Scan(
		&t.ID, &t.AccessHash, &t.CreatorUserID, &t.Slug, &t.Title, &t.Emoticon, &t.ForChat,
		&t.DocumentID, &settings, &t.InstallsCount, &createdAt,
	); err != nil {
		return domain.Theme{}, err
	}
	t.CreatedAt = createdAt.Unix()
	parsed, err := unmarshalThemeSettings(settings)
	if err != nil {
		return domain.Theme{}, err
	}
	t.Settings = parsed
	return t, nil
}

func (s *ThemeStore) GetThemeByID(ctx context.Context, id int64) (domain.Theme, bool, error) {
	row := s.db.QueryRow(ctx, `SELECT `+themeColumns+` FROM themes WHERE id = $1`, id)
	t, err := scanTheme(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Theme{}, false, nil
		}
		return domain.Theme{}, false, fmt.Errorf("get theme by id: %w", err)
	}
	return t, true, nil
}

func (s *ThemeStore) GetThemeBySlug(ctx context.Context, slug string) (domain.Theme, bool, error) {
	if slug == "" {
		return domain.Theme{}, false, nil
	}
	row := s.db.QueryRow(ctx, `SELECT `+themeColumns+` FROM themes WHERE slug = $1`, slug)
	t, err := scanTheme(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Theme{}, false, nil
		}
		return domain.Theme{}, false, fmt.Errorf("get theme by slug: %w", err)
	}
	return t, true, nil
}

func (s *ThemeStore) SlugExists(ctx context.Context, slug string) (bool, error) {
	if slug == "" {
		return false, nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM themes WHERE slug = $1)`, slug).Scan(&exists); err != nil {
		return false, fmt.Errorf("theme slug exists: %w", err)
	}
	return exists, nil
}

func (s *ThemeStore) UpdateTheme(ctx context.Context, t domain.Theme) error {
	settings, err := marshalThemeSettings(t.Settings)
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `
UPDATE themes SET slug = $2, title = $3, emoticon = $4, for_chat = $5, document_id = $6, settings = $7
WHERE id = $1`,
		t.ID, t.Slug, t.Title, t.Emoticon, t.ForChat, t.DocumentID, settings,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrThemeSlugTaken
		}
		return fmt.Errorf("update theme: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrThemeNotFound
	}
	return nil
}

func (s *ThemeStore) IncrementInstalls(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE themes SET installs_count = installs_count + 1 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("increment theme installs: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrThemeNotFound
	}
	return nil
}

func (s *ThemeStore) SetInstalled(ctx context.Context, userID, themeID int64, dark bool) error {
	_, err := s.db.Exec(ctx, `
INSERT INTO theme_user_installs (user_id, theme_id, dark, installed_at)
VALUES ($1,$2,$3, now())
ON CONFLICT (user_id, theme_id) DO UPDATE SET dark = EXCLUDED.dark`,
		userID, themeID, dark,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrThemeNotFound // a missing FK is equivalent to the theme not existing
		}
		return fmt.Errorf("set theme installed: %w", err)
	}
	return nil
}

func (s *ThemeStore) RemoveInstalled(ctx context.Context, userID, themeID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM theme_user_installs WHERE user_id = $1 AND theme_id = $2`, userID, themeID)
	if err != nil {
		return fmt.Errorf("remove theme installed: %w", err)
	}
	return nil
}

func (s *ThemeStore) ListInstalledByUser(ctx context.Context, userID int64) ([]domain.Theme, error) {
	rows, err := s.db.Query(ctx, `
SELECT `+prefixThemeColumns()+`
FROM themes t
JOIN theme_user_installs i ON i.theme_id = t.id
WHERE i.user_id = $1
ORDER BY i.installed_at, t.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list installed themes: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Theme, 0)
	for rows.Next() {
		t, err := scanTheme(rows)
		if err != nil {
			return nil, fmt.Errorf("scan installed theme: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *ThemeStore) ListThemesForUser(ctx context.Context, userID int64) ([]domain.Theme, error) {
	rows, err := s.db.Query(ctx, `
SELECT `+themeColumns+`
FROM themes
WHERE creator_user_id = $1
   OR id IN (SELECT theme_id FROM theme_user_installs WHERE user_id = $1)
ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list themes for user: %w", err)
	}
	defer rows.Close()
	out := make([]domain.Theme, 0)
	for rows.Next() {
		t, err := scanTheme(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user theme: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func prefixThemeColumns() string {
	parts := strings.Split(themeColumns, ", ")
	for i, p := range parts {
		parts[i] = "t." + p
	}
	return strings.Join(parts, ", ")
}

func marshalThemeSettings(settings []domain.ThemeSettingsSpec) ([]byte, error) {
	if len(settings) == 0 {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("marshal theme settings: %w", err)
	}
	return b, nil
}

func unmarshalThemeSettings(b []byte) ([]domain.ThemeSettingsSpec, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out []domain.ThemeSettingsSpec
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal theme settings: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
