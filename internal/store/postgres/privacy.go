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

var _ store.PrivacyStore = (*PrivacyStore)(nil)

// PrivacyStore persists account privacy rules in PostgreSQL.
type PrivacyStore struct {
	db sqlcgen.DBTX
}

func NewPrivacyStore(db sqlcgen.DBTX) *PrivacyStore {
	return &PrivacyStore{db: db}
}

func (s *PrivacyStore) GetPrivacyRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (domain.PrivacyRules, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT rules::text
FROM account_privacy_rules
WHERE owner_user_id = $1
  AND privacy_key = $2
`, ownerUserID, string(key))
	var raw string
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PrivacyRules{}, false, nil
		}
		return domain.PrivacyRules{}, false, fmt.Errorf("get privacy rules: %w", err)
	}
	rules, err := decodePrivacyRulesJSON(raw)
	if err != nil {
		return domain.PrivacyRules{}, false, err
	}
	return domain.PrivacyRules{OwnerUserID: ownerUserID, Key: key, Rules: rules}, true, nil
}

func (s *PrivacyStore) SetPrivacyRules(ctx context.Context, rules domain.PrivacyRules) error {
	return setPrivacyRules(ctx, s.db, rules)
}

func setPrivacyRules(ctx context.Context, db sqlcgen.DBTX, rules domain.PrivacyRules) error {
	raw, err := json.Marshal(rules.Rules)
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `
INSERT INTO account_privacy_rules (owner_user_id, privacy_key, rules, updated_at)
VALUES ($1, $2, $3::jsonb, NOW())
ON CONFLICT (owner_user_id, privacy_key) DO UPDATE SET
  rules = EXCLUDED.rules,
  updated_at = EXCLUDED.updated_at
`, rules.OwnerUserID, string(rules.Key), string(raw))
	if err != nil {
		return fmt.Errorf("set privacy rules: %w", err)
	}
	return nil
}

func (s *PrivacyStore) ListPrivacyRules(ctx context.Context, ownerUserIDs []int64, keys []domain.PrivacyKey) ([]domain.PrivacyRules, error) {
	if len(ownerUserIDs) == 0 || len(keys) == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT owner_user_id, privacy_key, rules::text
FROM account_privacy_rules
WHERE owner_user_id = ANY($1::bigint[])
  AND privacy_key = ANY($2::text[])
`, ownerUserIDs, privacyKeyStrings(keys))
	if err != nil {
		return nil, fmt.Errorf("list privacy rules: %w", err)
	}
	defer rows.Close()
	out := make([]domain.PrivacyRules, 0)
	for rows.Next() {
		var ownerUserID int64
		var key string
		var raw string
		if err := rows.Scan(&ownerUserID, &key, &raw); err != nil {
			return nil, err
		}
		rules, err := decodePrivacyRulesJSON(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, domain.PrivacyRules{
			OwnerUserID: ownerUserID,
			Key:         domain.PrivacyKey(key),
			Rules:       rules,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func privacyKeyStrings(keys []domain.PrivacyKey) []string {
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, string(key))
	}
	return out
}

func decodePrivacyRulesJSON(raw string) ([]domain.PrivacyRule, error) {
	if raw == "" {
		return nil, nil
	}
	var rules []domain.PrivacyRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("decode privacy rules: %w", err)
	}
	for i := range rules {
		rules[i].UserIDs = append([]int64(nil), rules[i].UserIDs...)
		rules[i].ChatIDs = append([]int64(nil), rules[i].ChatIDs...)
	}
	return rules, nil
}
