package moderation

import (
	"context"
	"encoding/json"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// MigrateLegacyEphemeralReports converts every pre-unified durable report into
// the canonical moderation shape. The store commits the new report and its
// legacy provenance mapping atomically; rerunning after a crash is safe.
func (s *Service) MigrateLegacyEphemeralReports(ctx context.Context, source store.LegacyEphemeralReportReader, batchSize int) (int, error) {
	if s == nil || s.reports == nil || source == nil {
		return 0, fmt.Errorf("legacy ephemeral report migration is not configured")
	}
	if batchSize <= 0 || batchSize > 1000 {
		return 0, fmt.Errorf("legacy ephemeral report batch limit out of range")
	}
	importer, ok := s.reports.(store.LegacyEphemeralReportImporter)
	if !ok {
		return 0, fmt.Errorf("moderation report store does not support legacy imports")
	}
	migrated := 0
	for {
		rows, err := source.ListUnmigratedEphemeralReports(ctx, batchSize)
		if err != nil {
			return migrated, err
		}
		for _, legacy := range rows {
			report, err := legacyEphemeralModerationReport(legacy.Report)
			if err != nil {
				return migrated, fmt.Errorf("convert legacy ephemeral report %d: %w", legacy.ID, err)
			}
			if _, _, err := importer.ImportLegacyEphemeralReport(ctx, legacy.ID, report); err != nil {
				return migrated, fmt.Errorf("import legacy ephemeral report %d: %w", legacy.ID, err)
			}
			migrated++
		}
		if len(rows) < batchSize {
			return migrated, nil
		}
	}
}

func legacyEphemeralModerationReport(legacy domain.EphemeralAbuseReport) (domain.ModerationReport, error) {
	if err := legacy.Validate(); err != nil {
		return domain.ModerationReport{}, err
	}
	reason, ok := legacyEphemeralModerationReason(legacy.Option)
	if !ok {
		return domain.ModerationReport{}, fmt.Errorf("%w: unsupported legacy option %q", domain.ErrModerationReportInvalid, legacy.Option)
	}
	evidence, err := json.Marshal(struct {
		SchemaVersion int                            `json:"schema_version"`
		Evidence      domain.EphemeralReportEvidence `json:"evidence"`
	}{SchemaVersion: 1, Evidence: legacy.Evidence})
	if err != nil {
		return domain.ModerationReport{}, fmt.Errorf("marshal legacy ephemeral evidence: %w", err)
	}
	return domain.NewModerationReport(domain.ModerationReportDraft{
		ReporterUserID: legacy.ReporterUserID,
		Source:         domain.ModerationSourceEphemeral,
		Target:         legacy.Evidence.Peer,
		Reason:         reason,
		Option:         legacy.Option,
		Comment:        legacy.Comment,
		Items: []domain.ModerationReportItem{{
			Kind:                  domain.ModerationItemEphemeral,
			Peer:                  legacy.Evidence.Peer,
			ItemID:                int64(legacy.Evidence.MessageID),
			AuthorUserID:          legacy.Evidence.SenderUserID,
			EvidenceSchemaVersion: 1,
			Evidence:              evidence,
		}},
		MediaHolds: mediaHolds(0, legacy.Evidence.Content.Media),
		CreatedAt:  legacy.CreatedAt,
	})
}

func legacyEphemeralModerationReason(option string) (domain.ModerationReason, bool) {
	switch option {
	case "spam":
		return domain.ModerationReasonSpam, true
	case "violence":
		return domain.ModerationReasonViolence, true
	case "pornography":
		return domain.ModerationReasonPornography, true
	case "child_abuse":
		return domain.ModerationReasonChildAbuse, true
	case "illegal_drugs":
		return domain.ModerationReasonIllegalDrugs, true
	case "personal_details":
		return domain.ModerationReasonPersonalDetails, true
	case "copyright":
		return domain.ModerationReasonCopyright, true
	case "fake":
		return domain.ModerationReasonFake, true
	case "other", "other:comment":
		return domain.ModerationReasonOther, true
	default:
		return "", false
	}
}
