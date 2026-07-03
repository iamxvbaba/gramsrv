package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// AIComposeStore 用 PostgreSQL 实现 store.AIComposeStore。
type AIComposeStore struct {
	db sqlcgen.DBTX
}

func NewAIComposeStore(db sqlcgen.DBTX) *AIComposeStore {
	return &AIComposeStore{db: db}
}

const aiComposeToneColumns = `id, access_hash, owner_user_id, slug, title, emoji_id, prompt, display_author, installs_count, created_at, updated_at`

func (s *AIComposeStore) CreateAIComposeTone(ctx context.Context, tone domain.AIComposeTone) error {
	if tone.ID == 0 || tone.AccessHash == 0 || tone.OwnerUserID == 0 || tone.Slug == "" {
		return domain.ErrAIComposeToneInvalid
	}
	createdAt := time.Now()
	if tone.CreatedAt > 0 {
		createdAt = time.Unix(tone.CreatedAt, 0)
	}
	updatedAt := createdAt
	if tone.UpdatedAt > 0 {
		updatedAt = time.Unix(tone.UpdatedAt, 0)
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO ai_compose_tones (
  id, access_hash, owner_user_id, slug, title, emoji_id, prompt, display_author, installs_count, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		tone.ID, tone.AccessHash, tone.OwnerUserID, tone.Slug, tone.Title, tone.EmojiID,
		tone.Prompt, tone.DisplayAuthor, tone.InstallsCount, createdAt, updatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrAIComposeToneInvalid
		}
		return fmt.Errorf("insert ai compose tone: %w", err)
	}
	return nil
}

func (s *AIComposeStore) UpdateAIComposeTone(ctx context.Context, tone domain.AIComposeTone) error {
	updatedAt := time.Now()
	if tone.UpdatedAt > 0 {
		updatedAt = time.Unix(tone.UpdatedAt, 0)
	}
	tag, err := s.db.Exec(ctx, `
UPDATE ai_compose_tones
SET title = $3, emoji_id = $4, prompt = $5, display_author = $6, updated_at = $7
WHERE id = $1 AND owner_user_id = $2`,
		tone.ID, tone.OwnerUserID, tone.Title, tone.EmojiID, tone.Prompt, tone.DisplayAuthor, updatedAt)
	if err != nil {
		return fmt.Errorf("update ai compose tone: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrAIComposeToneNotFound
	}
	return nil
}

func (s *AIComposeStore) DeleteAIComposeTone(ctx context.Context, ownerUserID, toneID int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM ai_compose_tones WHERE id = $1 AND owner_user_id = $2`, toneID, ownerUserID)
	if err != nil {
		return fmt.Errorf("delete ai compose tone: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrAIComposeToneNotFound
	}
	return nil
}

func (s *AIComposeStore) GetAIComposeToneByID(ctx context.Context, id, accessHash int64) (domain.AIComposeTone, bool, error) {
	row := s.db.QueryRow(ctx, `SELECT `+aiComposeToneColumns+` FROM ai_compose_tones WHERE id = $1 AND access_hash = $2`, id, accessHash)
	tone, err := scanAIComposeTone(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AIComposeTone{}, false, nil
		}
		return domain.AIComposeTone{}, false, fmt.Errorf("get ai compose tone by id: %w", err)
	}
	return tone, true, nil
}

func (s *AIComposeStore) GetAIComposeToneBySlug(ctx context.Context, slug string) (domain.AIComposeTone, bool, error) {
	row := s.db.QueryRow(ctx, `SELECT `+aiComposeToneColumns+` FROM ai_compose_tones WHERE slug = $1`, slug)
	tone, err := scanAIComposeTone(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AIComposeTone{}, false, nil
		}
		return domain.AIComposeTone{}, false, fmt.Errorf("get ai compose tone by slug: %w", err)
	}
	return tone, true, nil
}

func (s *AIComposeStore) ListAIComposeTonesForUser(ctx context.Context, userID int64) ([]domain.AIComposeTone, error) {
	rows, err := s.db.Query(ctx, `
SELECT `+aiComposeToneColumns+`, (owner_user_id = $1) AS creator, true AS saved
FROM ai_compose_tones
WHERE owner_user_id = $1
UNION ALL
SELECT `+prefixAIComposeToneColumns("t")+`, false AS creator, true AS saved
FROM ai_compose_tones t
JOIN ai_compose_tone_saves s ON s.tone_id = t.id
WHERE s.user_id = $1 AND t.owner_user_id <> $1
ORDER BY creator DESC, updated_at DESC, id ASC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list ai compose tones: %w", err)
	}
	defer rows.Close()
	out := make([]domain.AIComposeTone, 0)
	for rows.Next() {
		tone, err := scanAIComposeToneWithFlags(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ai compose tone: %w", err)
		}
		out = append(out, tone)
	}
	return out, rows.Err()
}

func (s *AIComposeStore) SaveAIComposeTone(ctx context.Context, userID, toneID int64) error {
	return withTx(ctx, s.db, "save ai compose tone", func(tx pgx.Tx) error {
		var ownerUserID int64
		if err := tx.QueryRow(ctx, `SELECT owner_user_id FROM ai_compose_tones WHERE id = $1`, toneID).Scan(&ownerUserID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrAIComposeToneNotFound
			}
			return fmt.Errorf("select ai compose tone owner: %w", err)
		}
		if ownerUserID == userID {
			return nil
		}
		tag, err := tx.Exec(ctx, `
INSERT INTO ai_compose_tone_saves (user_id, tone_id, saved_at)
VALUES ($1,$2,now())
ON CONFLICT (user_id, tone_id) DO NOTHING`, userID, toneID)
		if err != nil {
			return fmt.Errorf("save ai compose tone: %w", err)
		}
		if tag.RowsAffected() > 0 {
			if _, err := tx.Exec(ctx, `UPDATE ai_compose_tones SET installs_count = installs_count + 1 WHERE id = $1`, toneID); err != nil {
				return fmt.Errorf("increment ai compose tone installs: %w", err)
			}
		}
		return nil
	})
}

func (s *AIComposeStore) UnsaveAIComposeTone(ctx context.Context, userID, toneID int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM ai_compose_tone_saves WHERE user_id = $1 AND tone_id = $2`, userID, toneID)
	if err != nil {
		return fmt.Errorf("unsave ai compose tone: %w", err)
	}
	return nil
}

func (s *AIComposeStore) SavedAIComposeToneCount(ctx context.Context, userID int64) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int FROM (
  SELECT id FROM ai_compose_tones WHERE owner_user_id = $1
  UNION
  SELECT tone_id FROM ai_compose_tone_saves WHERE user_id = $1
) x`, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count ai compose tones: %w", err)
	}
	return count, nil
}

func scanAIComposeTone(row pgx.Row) (domain.AIComposeTone, error) {
	var (
		tone      domain.AIComposeTone
		createdAt time.Time
		updatedAt time.Time
	)
	if err := row.Scan(&tone.ID, &tone.AccessHash, &tone.OwnerUserID, &tone.Slug, &tone.Title,
		&tone.EmojiID, &tone.Prompt, &tone.DisplayAuthor, &tone.InstallsCount, &createdAt, &updatedAt); err != nil {
		return domain.AIComposeTone{}, err
	}
	tone.CreatedAt = createdAt.Unix()
	tone.UpdatedAt = updatedAt.Unix()
	if tone.DisplayAuthor {
		tone.AuthorID = tone.OwnerUserID
	}
	return tone, nil
}

func scanAIComposeToneWithFlags(row pgx.Row) (domain.AIComposeTone, error) {
	var (
		tone      domain.AIComposeTone
		createdAt time.Time
		updatedAt time.Time
	)
	if err := row.Scan(&tone.ID, &tone.AccessHash, &tone.OwnerUserID, &tone.Slug, &tone.Title,
		&tone.EmojiID, &tone.Prompt, &tone.DisplayAuthor, &tone.InstallsCount, &createdAt, &updatedAt,
		&tone.Creator, &tone.Saved); err != nil {
		return domain.AIComposeTone{}, err
	}
	tone.CreatedAt = createdAt.Unix()
	tone.UpdatedAt = updatedAt.Unix()
	if tone.DisplayAuthor {
		tone.AuthorID = tone.OwnerUserID
	}
	return tone, nil
}

func prefixAIComposeToneColumns(prefix string) string {
	return prefix + `.id, ` + prefix + `.access_hash, ` + prefix + `.owner_user_id, ` +
		prefix + `.slug, ` + prefix + `.title, ` + prefix + `.emoji_id, ` + prefix + `.prompt, ` +
		prefix + `.display_author, ` + prefix + `.installs_count, ` + prefix + `.created_at, ` + prefix + `.updated_at`
}
