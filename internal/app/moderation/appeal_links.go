package moderation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"telesrv/internal/domain"
)

const moderationAppealTokenBytes = 32

// IssueAppealLink creates a hash-only, time-bounded bearer capability. The raw
// token is returned once and must only be embedded in the affected user's
// client-visible appeal URL.
func (s *Service) IssueAppealLink(ctx context.Context, caseID, appellantUserID int64, expiresAt, now time.Time) (string, error) {
	if s == nil || s.cases == nil {
		return "", fmt.Errorf("moderation case store is not configured")
	}
	for attempt := 0; attempt < 3; attempt++ {
		raw := make([]byte, moderationAppealTokenBytes)
		if _, err := rand.Read(raw); err != nil {
			return "", fmt.Errorf("generate moderation appeal token: %w", err)
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		link := domain.ModerationAppealLink{
			CaseID: caseID, AppellantUserID: appellantUserID,
			TokenHash: sha256.Sum256(raw), ExpiresAt: expiresAt.UTC(),
			CreatedAt: now.UTC(),
		}
		if _, err := s.cases.IssueModerationAppealLink(ctx, link); err == nil {
			return token, nil
		} else if !errors.Is(err, domain.ErrModerationActionConflict) {
			return "", err
		}
	}
	return "", domain.ErrModerationActionConflict
}

func (s *Service) ResolveAppealLink(ctx context.Context, token string, now time.Time) (domain.ModerationAppealLink, bool, error) {
	if s == nil || s.cases == nil {
		return domain.ModerationAppealLink{}, false, fmt.Errorf("moderation case store is not configured")
	}
	hash, err := moderationAppealTokenHash(token)
	if err != nil {
		return domain.ModerationAppealLink{}, false, err
	}
	return s.cases.GetModerationAppealLink(ctx, hash, now.UTC())
}

func (s *Service) Appeal(ctx context.Context, appealID int64) (domain.ModerationAppeal, bool, error) {
	if s == nil || s.cases == nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("moderation case store is not configured")
	}
	return s.cases.GetModerationAppeal(ctx, appealID)
}

func (s *Service) SubmitAppealLink(ctx context.Context, token, text string, now time.Time) (domain.ModerationAppeal, bool, error) {
	if s == nil || s.cases == nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("moderation case store is not configured")
	}
	hash, err := moderationAppealTokenHash(token)
	if err != nil {
		return domain.ModerationAppeal{}, false, err
	}
	return s.cases.SubmitModerationAppealByLink(ctx, hash, text, now.UTC())
}

func moderationAppealTokenHash(token string) ([sha256.Size]byte, error) {
	if len(token) != base64.RawURLEncoding.EncodedLen(moderationAppealTokenBytes) {
		return [sha256.Size]byte{}, domain.ErrModerationAppealLinkInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != moderationAppealTokenBytes ||
		base64.RawURLEncoding.EncodeToString(raw) != token {
		return [sha256.Size]byte{}, domain.ErrModerationAppealLinkInvalid
	}
	return sha256.Sum256(raw), nil
}
