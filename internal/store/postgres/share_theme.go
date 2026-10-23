package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// ShareThemeStore is the PostgreSQL store for Layer 232 share themes.
type ShareThemeStore struct {
	db sqlcgen.DBTX
}

func NewShareThemeStore(db sqlcgen.DBTX) *ShareThemeStore {
	return &ShareThemeStore{db: db}
}

var _ store.ShareThemeStore = (*ShareThemeStore)(nil)

func validShareThemePeer(peer domain.Peer) bool {
	return peer.ID > 0 && (peer.Type == domain.PeerTypeUser || peer.Type == domain.PeerTypeChannel)
}

func (s *ShareThemeStore) GetShareTheme(ctx context.Context, peer domain.Peer) (domain.ShareTheme, bool, error) {
	if s == nil || s.db == nil {
		return domain.ShareTheme{}, false, fmt.Errorf("share theme store is not configured")
	}
	if !validShareThemePeer(peer) {
		return domain.ShareTheme{}, false, nil
	}
	var theme domain.ShareTheme
	var kind string
	var emojis []byte
	var giftID *int64
	err := s.db.QueryRow(ctx, `
SELECT kind, backdrop_id, emojis, unique_gift_id
FROM peer_share_themes WHERE peer_type = $1 AND peer_id = $2`, string(peer.Type), peer.ID).
		Scan(&kind, &theme.BackdropID, &emojis, &giftID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ShareTheme{}, false, nil
	}
	if err != nil {
		return domain.ShareTheme{}, false, fmt.Errorf("load share theme: %w", err)
	}
	theme.Kind = domain.ShareThemeKind(kind)
	if giftID != nil {
		theme.UniqueGiftID = *giftID
	}
	if err := json.Unmarshal(emojis, &theme.Emojis); err != nil {
		return domain.ShareTheme{}, false, fmt.Errorf("decode share theme emojis: %w", err)
	}
	if len(theme.Emojis) == 0 {
		theme.Emojis = nil
	}
	return theme, true, nil
}

func (s *ShareThemeStore) SetShareTheme(ctx context.Context, peer domain.Peer, theme *domain.ShareTheme) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("share theme store is not configured")
	}
	if !validShareThemePeer(peer) {
		return domain.ErrShareThemeInvalid
	}
	if theme == nil {
		_, err := s.db.Exec(ctx, `DELETE FROM peer_share_themes WHERE peer_type = $1 AND peer_id = $2`, string(peer.Type), peer.ID)
		if err != nil {
			return fmt.Errorf("reset share theme: %w", err)
		}
		return nil
	}
	if err := theme.Validate(); err != nil {
		return err
	}
	emojis := theme.Emojis
	if emojis == nil {
		emojis = []domain.ShareThemeEmoji{}
	}
	encoded, err := json.Marshal(emojis)
	if err != nil {
		return fmt.Errorf("encode share theme emojis: %w", err)
	}
	var giftID *int64
	if theme.UniqueGiftID > 0 {
		giftID = &theme.UniqueGiftID
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO peer_share_themes (peer_type, peer_id, kind, backdrop_id, emojis, unique_gift_id, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (peer_type, peer_id) DO UPDATE SET
    kind = EXCLUDED.kind, backdrop_id = EXCLUDED.backdrop_id, emojis = EXCLUDED.emojis,
    unique_gift_id = EXCLUDED.unique_gift_id, updated_at = EXCLUDED.updated_at`,
		string(peer.Type), peer.ID, string(theme.Kind), theme.BackdropID, encoded, giftID); err != nil {
		return fmt.Errorf("save share theme: %w", err)
	}
	return nil
}

func (s *ShareThemeStore) ShareThemeBackdrops(ctx context.Context) ([]domain.ShareThemeBackdrop, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("share theme store is not configured")
	}
	rows, err := s.db.Query(ctx, `
SELECT DISTINCT ON (b.backdrop_id)
       b.backdrop_id, b.name, b.center_color, b.edge_color, b.pattern_color, b.text_color
FROM star_gift_collectible_backdrops b
JOIN star_gift_collectible_revisions r ON r.id = b.collectible_revision_id
WHERE r.status = 'published' AND b.backdrop_id > 0
ORDER BY b.backdrop_id, r.published_at DESC, b.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list share theme backdrops: %w", err)
	}
	defer rows.Close()
	var out []domain.ShareThemeBackdrop
	for rows.Next() {
		var b domain.ShareThemeBackdrop
		if err := rows.Scan(&b.BackdropID, &b.Name, &b.CenterColor, &b.EdgeColor, &b.PatternColor, &b.TextColor); err != nil {
			return nil, fmt.Errorf("scan share theme backdrop: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list share theme backdrops: %w", err)
	}
	return out, nil
}
