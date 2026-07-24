package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// ModerationReportStore atomically persists an immutable report, all evidence
// items and media holds. A retry with the same fingerprint returns the original
// report and created=false; implementations must never create partial items.
type ModerationReportStore interface {
	CreateModerationReport(ctx context.Context, report domain.ModerationReport) (stored domain.ModerationReport, created bool, err error)
	GetModerationReport(ctx context.Context, reportID int64) (domain.ModerationReport, bool, error)
}

type ModerationEvidenceRegistryStore interface {
	CreateSponsoredMessageImpression(ctx context.Context, impression domain.SponsoredMessageImpression) (domain.SponsoredMessageImpression, bool, error)
	GetSponsoredMessageImpression(ctx context.Context, userID int64, randomIDHash [32]byte, now time.Time) (domain.SponsoredMessageImpression, bool, error)
	CreateSponsoredModerationReport(ctx context.Context, impressionID int64, report domain.ModerationReport) (domain.ModerationReport, bool, error)
	CreateChannelAntiSpamDecision(ctx context.Context, decision domain.ChannelAntiSpamDecision) (domain.ChannelAntiSpamDecision, bool, error)
	GetChannelAntiSpamDecision(ctx context.Context, channelID int64, messageID int) (domain.ChannelAntiSpamDecision, bool, error)
	CreateAntiSpamFalsePositiveReport(ctx context.Context, decisionID int64, report domain.ModerationReport) (domain.ModerationReport, bool, error)
	DeleteExpiredSponsoredMessageImpressions(ctx context.Context, olderThan time.Time, limit int) (int, error)
}

// LegacyEphemeralReport is an immutable row produced before ephemeral reports
// joined the unified moderation pipeline.
type LegacyEphemeralReport struct {
	ID     int64
	Report domain.EphemeralAbuseReport
}

// LegacyEphemeralReportReader exposes only still-unmapped rows. It exists for
// the startup migration and must not be injected into RPC handlers.
type LegacyEphemeralReportReader interface {
	ListUnmigratedEphemeralReports(ctx context.Context, limit int) ([]LegacyEphemeralReport, error)
}

// LegacyEphemeralReportImporter atomically inserts the unified report and its
// provenance mapping. Historical imports bypass submission rate limits but
// retain normal report validation and fingerprint idempotency.
type LegacyEphemeralReportImporter interface {
	ImportLegacyEphemeralReport(ctx context.Context, legacyReportID int64, report domain.ModerationReport) (stored domain.ModerationReport, created bool, err error)
}

type ModerationCaseStore interface {
	ListModerationCases(ctx context.Context, filter domain.ModerationCaseFilter) ([]domain.ModerationCase, error)
	GetModerationCase(ctx context.Context, caseID int64) (domain.ModerationCaseDetail, bool, error)
	ClaimModerationCase(ctx context.Context, caseID, expectedVersion int64, actor string, now time.Time) (domain.ModerationCase, error)
	DecideModerationCase(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error)
	ReviewModerationAppeal(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error)
	CreateModerationAppeal(ctx context.Context, appeal domain.ModerationAppeal) (domain.ModerationAppeal, bool, error)
	GetModerationAppeal(ctx context.Context, appealID int64) (domain.ModerationAppeal, bool, error)
	IssueModerationAppealLink(ctx context.Context, link domain.ModerationAppealLink) (domain.ModerationAppealLink, error)
	GetModerationAppealLink(ctx context.Context, tokenHash [32]byte, now time.Time) (domain.ModerationAppealLink, bool, error)
	SubmitModerationAppealByLink(ctx context.Context, tokenHash [32]byte, text string, now time.Time) (domain.ModerationAppeal, bool, error)
	DeleteExpiredModerationAppealLinks(ctx context.Context, olderThan time.Time, limit int) (int, error)
	ClaimModerationActions(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.ModerationAction, error)
	IsModerationActionCurrent(ctx context.Context, action domain.ModerationAction) (bool, error)
	SupersedeModerationAction(ctx context.Context, actionID int64, expectedAttempts int, now time.Time) error
	CompleteModerationAction(ctx context.Context, actionID int64, expectedAttempts int, succeeded bool, errorText string, retryAt, now time.Time) error
}
