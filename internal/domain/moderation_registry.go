package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"time"
)

const MaxSponsoredImpressionLifetime = 30 * 24 * time.Hour

// SponsoredMessageImpression is the server-issued fact required before a
// random_id may enter the human moderation pipeline.
type SponsoredMessageImpression struct {
	ID                    int64
	UserID                int64
	RandomIDHash          [sha256.Size]byte
	Target                Peer
	AuthorUserID          int64
	EvidenceSchemaVersion int
	Evidence              json.RawMessage
	EvidenceHash          [sha256.Size]byte
	ReportID              int64
	CreatedAt             time.Time
	ExpiresAt             time.Time
}

func NewSponsoredMessageImpression(userID int64, randomID []byte, target Peer, authorUserID int64, evidence json.RawMessage, createdAt, expiresAt time.Time) (SponsoredMessageImpression, error) {
	canonical, err := CanonicalModerationEvidence(evidence)
	if err != nil {
		return SponsoredMessageImpression{}, ErrModerationReportInvalid
	}
	impression := SponsoredMessageImpression{
		UserID: userID, RandomIDHash: sha256.Sum256(randomID),
		Target: target, AuthorUserID: authorUserID,
		EvidenceSchemaVersion: 1, Evidence: canonical,
		EvidenceHash: sha256.Sum256(canonical),
		CreatedAt:    createdAt.UTC(), ExpiresAt: expiresAt.UTC(),
	}
	if err := impression.Validate(); err != nil {
		return SponsoredMessageImpression{}, err
	}
	return impression, nil
}

func (i SponsoredMessageImpression) Validate() error {
	canonical, err := CanonicalModerationEvidence(i.Evidence)
	if i.ID < 0 || i.UserID <= 0 ||
		i.RandomIDHash == ([sha256.Size]byte{}) ||
		!moderationPeerValid(i.Target) || i.AuthorUserID < 0 ||
		i.EvidenceSchemaVersion <= 0 ||
		i.EvidenceHash != sha256.Sum256(i.Evidence) ||
		err != nil || !bytes.Equal(canonical, i.Evidence) ||
		i.ReportID < 0 || i.CreatedAt.IsZero() ||
		!i.ExpiresAt.After(i.CreatedAt) ||
		i.ExpiresAt.Sub(i.CreatedAt) > MaxSponsoredImpressionLifetime {
		return ErrModerationReportInvalid
	}
	return nil
}

func ValidateSponsoredModerationReport(impression SponsoredMessageImpression, report ModerationReport) error {
	if err := impression.Validate(); err != nil {
		return err
	}
	if err := report.Validate(); err != nil ||
		report.ReporterUserID != impression.UserID ||
		report.Source != ModerationSourceSponsored ||
		report.Target != impression.Target ||
		report.CreatedAt.Before(impression.CreatedAt) ||
		!report.CreatedAt.Before(impression.ExpiresAt) ||
		len(report.Items) != 1 || len(report.MediaHolds) != 0 {
		return ErrModerationReportInvalid
	}
	item := report.Items[0]
	if item.Kind != ModerationItemSponsored ||
		item.Peer != impression.Target ||
		item.ItemID != impression.ID || item.SecondaryID != 0 ||
		item.AuthorUserID != impression.AuthorUserID ||
		item.EvidenceSchemaVersion != impression.EvidenceSchemaVersion ||
		item.EvidenceHash != impression.EvidenceHash ||
		!bytes.Equal(item.Evidence, impression.Evidence) {
		return ErrModerationReportInvalid
	}
	return nil
}

// ChannelAntiSpamDecision is immutable evidence that native anti-spam
// actually removed the referenced message. A false-positive report without
// this fact must fail closed.
type ChannelAntiSpamDecision struct {
	ID                    int64
	ChannelID             int64
	MessageID             int
	AuthorUserID          int64
	EvidenceSchemaVersion int
	Evidence              json.RawMessage
	EvidenceHash          [sha256.Size]byte
	ReportID              int64
	CreatedAt             time.Time
}

func NewChannelAntiSpamDecision(channelID int64, messageID int, authorUserID int64, evidence json.RawMessage, createdAt time.Time) (ChannelAntiSpamDecision, error) {
	canonical, err := CanonicalModerationEvidence(evidence)
	if err != nil {
		return ChannelAntiSpamDecision{}, ErrModerationReportInvalid
	}
	decision := ChannelAntiSpamDecision{
		ChannelID: channelID, MessageID: messageID,
		AuthorUserID: authorUserID, EvidenceSchemaVersion: 1,
		Evidence: canonical, EvidenceHash: sha256.Sum256(canonical),
		CreatedAt: createdAt.UTC(),
	}
	if err := decision.Validate(); err != nil {
		return ChannelAntiSpamDecision{}, err
	}
	return decision, nil
}

func (d ChannelAntiSpamDecision) Validate() error {
	canonical, err := CanonicalModerationEvidence(d.Evidence)
	if d.ID < 0 || d.ChannelID <= 0 || d.MessageID <= 0 ||
		d.MessageID > MaxMessageBoxID || d.AuthorUserID <= 0 ||
		d.EvidenceSchemaVersion <= 0 ||
		d.EvidenceHash != sha256.Sum256(d.Evidence) ||
		err != nil || !bytes.Equal(canonical, d.Evidence) ||
		d.ReportID < 0 || d.CreatedAt.IsZero() {
		return ErrModerationReportInvalid
	}
	return nil
}

func ValidateAntiSpamFalsePositiveReport(decision ChannelAntiSpamDecision, report ModerationReport) error {
	if err := decision.Validate(); err != nil {
		return err
	}
	target := Peer{Type: PeerTypeChannel, ID: decision.ChannelID}
	if err := report.Validate(); err != nil ||
		report.Source != ModerationSourceAntiSpamFalsePositive ||
		report.Target != target ||
		report.CreatedAt.Before(decision.CreatedAt) ||
		len(report.Items) != 1 || len(report.MediaHolds) != 0 {
		return ErrModerationReportInvalid
	}
	item := report.Items[0]
	if item.Kind != ModerationItemAntiSpamDecision ||
		item.Peer != target || item.ItemID != decision.ID ||
		item.SecondaryID != int64(decision.MessageID) ||
		item.AuthorUserID != decision.AuthorUserID ||
		item.EvidenceSchemaVersion != decision.EvidenceSchemaVersion ||
		item.EvidenceHash != decision.EvidenceHash ||
		!bytes.Equal(item.Evidence, decision.Evidence) {
		return ErrModerationReportInvalid
	}
	return nil
}
