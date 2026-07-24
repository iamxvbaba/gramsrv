package moderation

import (
	"context"
	"crypto/sha256"
	"time"

	"telesrv/internal/domain"
)

func (s *Service) SponsoredImpression(ctx context.Context, userID int64, randomID []byte, now time.Time) (domain.SponsoredMessageImpression, error) {
	if s == nil || s.registry == nil || len(randomID) == 0 {
		return domain.SponsoredMessageImpression{}, domain.ErrModerationEvidenceNotFound
	}
	impression, found, err := s.registry.GetSponsoredMessageImpression(
		ctx, userID, sha256.Sum256(randomID), now,
	)
	if err != nil {
		return domain.SponsoredMessageImpression{}, err
	}
	if !found {
		return domain.SponsoredMessageImpression{}, domain.ErrModerationImpressionExpired
	}
	return impression, nil
}

func (s *Service) ReportSponsored(ctx context.Context, userID int64, randomID []byte, reason domain.ModerationReason, option string, now time.Time) (domain.ModerationReport, bool, error) {
	impression, err := s.SponsoredImpression(ctx, userID, randomID, now)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	if impression.ReportID > 0 {
		report, found, err := s.Report(ctx, impression.ReportID)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		if !found {
			return domain.ModerationReport{}, false, domain.ErrModerationReportNotFound
		}
		return report, false, nil
	}
	report, err := domain.NewModerationReport(domain.ModerationReportDraft{
		ReporterUserID: userID, Source: domain.ModerationSourceSponsored,
		Target: impression.Target, Reason: reason, Option: option,
		Items: []domain.ModerationReportItem{{
			Kind: domain.ModerationItemSponsored, Peer: impression.Target,
			ItemID: impression.ID, AuthorUserID: impression.AuthorUserID,
			EvidenceSchemaVersion: impression.EvidenceSchemaVersion,
			Evidence:              impression.Evidence,
		}},
		CreatedAt: now,
	})
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	return s.registry.CreateSponsoredModerationReport(ctx, impression.ID, report)
}

func (s *Service) ReportAntiSpamFalsePositive(ctx context.Context, reporterUserID, channelID int64, messageID int, now time.Time) (domain.ModerationReport, bool, error) {
	if s == nil || s.registry == nil {
		return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
	}
	decision, found, err := s.registry.GetChannelAntiSpamDecision(
		ctx, channelID, messageID,
	)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	if !found {
		return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
	}
	if decision.ReportID > 0 {
		report, found, err := s.Report(ctx, decision.ReportID)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		if !found {
			return domain.ModerationReport{}, false, domain.ErrModerationReportNotFound
		}
		return report, false, nil
	}
	target := domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}
	report, err := domain.NewModerationReport(domain.ModerationReportDraft{
		ReporterUserID: reporterUserID,
		Source:         domain.ModerationSourceAntiSpamFalsePositive,
		Target:         target,
		Reason:         domain.ModerationReasonOther,
		Option:         "false_positive",
		Items: []domain.ModerationReportItem{{
			Kind: domain.ModerationItemAntiSpamDecision, Peer: target,
			ItemID: decision.ID, SecondaryID: int64(messageID),
			AuthorUserID:          decision.AuthorUserID,
			EvidenceSchemaVersion: decision.EvidenceSchemaVersion,
			Evidence:              decision.Evidence,
		}},
		CreatedAt: now,
	})
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	return s.registry.CreateAntiSpamFalsePositiveReport(ctx, decision.ID, report)
}
