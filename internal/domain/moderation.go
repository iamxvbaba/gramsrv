package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"time"
	"unicode/utf8"
)

const (
	ModerationTaxonomyVersion         = 1
	MaxModerationReportItems          = 100
	MaxModerationMediaHolds           = 1000
	MaxModerationOptionBytes          = 32
	MaxModerationCommentRunes         = 512
	MaxModerationEvidenceBytes        = 1 << 20
	MaxModerationTotalEvidenceBytes   = 4 << 20
	MaxModerationMediaStorageKeyBytes = 512
	MaxModerationReportsPerHour       = 20
	MaxModerationReportsPerDay        = 100
)

var (
	ErrModerationReportInvalid     = errors.New("moderation report invalid")
	ErrModerationReportNotFound    = errors.New("moderation report not found")
	ErrModerationCaseInvalid       = errors.New("moderation case invalid")
	ErrModerationCaseNotFound      = errors.New("moderation case not found")
	ErrModerationCaseConflict      = errors.New("moderation case conflict")
	ErrModerationActionInvalid     = errors.New("moderation action invalid")
	ErrModerationActionConflict    = errors.New("moderation action conflict")
	ErrModerationPermissionDenied  = errors.New("moderation permission denied")
	ErrModerationRateLimited       = errors.New("moderation rate limited")
	ErrModerationEvidenceNotFound  = errors.New("moderation evidence not found")
	ErrModerationDecisionNotFound  = errors.New("moderation decision not found")
	ErrModerationImpressionExpired = errors.New("moderation impression expired")
	ErrModerationAppealLinkInvalid = errors.New("moderation appeal link invalid")
)

// ModerationReportSource identifies the client RPC and evidence admission path.
// Operational telemetry and authentication delivery diagnostics deliberately do
// not use this type or the moderation report tables.
type ModerationReportSource string

const (
	ModerationSourceAccountPeer           ModerationReportSource = "account_peer"
	ModerationSourceProfilePhoto          ModerationReportSource = "profile_photo"
	ModerationSourceMessagesSpam          ModerationReportSource = "messages_spam"
	ModerationSourceMessages              ModerationReportSource = "messages"
	ModerationSourceEncryptedSpam         ModerationReportSource = "encrypted_spam"
	ModerationSourceReaction              ModerationReportSource = "reaction"
	ModerationSourceChannelSpam           ModerationReportSource = "channel_spam"
	ModerationSourceStory                 ModerationReportSource = "story"
	ModerationSourceEphemeral             ModerationReportSource = "ephemeral"
	ModerationSourceSponsored             ModerationReportSource = "sponsored"
	ModerationSourceAntiSpamFalsePositive ModerationReportSource = "antispam_false_positive"
)

func (s ModerationReportSource) Valid() bool {
	switch s {
	case ModerationSourceAccountPeer, ModerationSourceProfilePhoto,
		ModerationSourceMessagesSpam, ModerationSourceMessages,
		ModerationSourceEncryptedSpam, ModerationSourceReaction,
		ModerationSourceChannelSpam, ModerationSourceStory,
		ModerationSourceEphemeral, ModerationSourceSponsored,
		ModerationSourceAntiSpamFalsePositive:
		return true
	default:
		return false
	}
}

// ModerationReason is the canonical domain taxonomy shared by ReportReason
// constructors and the opaque multi-step report option flow.
type ModerationReason string

const (
	ModerationReasonSpam            ModerationReason = "spam"
	ModerationReasonViolence        ModerationReason = "violence"
	ModerationReasonPornography     ModerationReason = "pornography"
	ModerationReasonChildAbuse      ModerationReason = "child_abuse"
	ModerationReasonOther           ModerationReason = "other"
	ModerationReasonCopyright       ModerationReason = "copyright"
	ModerationReasonGeoIrrelevant   ModerationReason = "geo_irrelevant"
	ModerationReasonFake            ModerationReason = "fake"
	ModerationReasonIllegalDrugs    ModerationReason = "illegal_drugs"
	ModerationReasonPersonalDetails ModerationReason = "personal_details"
)

func (r ModerationReason) Valid() bool {
	switch r {
	case ModerationReasonSpam, ModerationReasonViolence,
		ModerationReasonPornography, ModerationReasonChildAbuse,
		ModerationReasonOther, ModerationReasonCopyright,
		ModerationReasonGeoIrrelevant, ModerationReasonFake,
		ModerationReasonIllegalDrugs, ModerationReasonPersonalDetails:
		return true
	default:
		return false
	}
}

type ModerationReportItemKind string

const (
	ModerationItemPeer             ModerationReportItemKind = "peer"
	ModerationItemMessage          ModerationReportItemKind = "message"
	ModerationItemProfilePhoto     ModerationReportItemKind = "profile_photo"
	ModerationItemReaction         ModerationReportItemKind = "reaction"
	ModerationItemStory            ModerationReportItemKind = "story"
	ModerationItemEncryptedChat    ModerationReportItemKind = "encrypted_chat"
	ModerationItemEphemeral        ModerationReportItemKind = "ephemeral"
	ModerationItemSponsored        ModerationReportItemKind = "sponsored"
	ModerationItemAntiSpamDecision ModerationReportItemKind = "antispam_decision"
)

func (k ModerationReportItemKind) Valid() bool {
	switch k {
	case ModerationItemPeer, ModerationItemMessage,
		ModerationItemProfilePhoto, ModerationItemReaction,
		ModerationItemStory, ModerationItemEncryptedChat,
		ModerationItemEphemeral, ModerationItemSponsored,
		ModerationItemAntiSpamDecision:
		return true
	default:
		return false
	}
}

type ModerationMediaKind string

const (
	ModerationMediaPhoto    ModerationMediaKind = "photo"
	ModerationMediaDocument ModerationMediaKind = "document"
	ModerationMediaBlob     ModerationMediaKind = "blob"
)

func (k ModerationMediaKind) Valid() bool {
	switch k {
	case ModerationMediaPhoto, ModerationMediaDocument, ModerationMediaBlob:
		return true
	default:
		return false
	}
}

// ModerationReportItem is a stable reference plus a privacy-bounded evidence
// snapshot. Evidence must be a versioned JSON object produced by the owning app
// service; moderation never repairs malformed historical snapshots on read.
type ModerationReportItem struct {
	Kind                  ModerationReportItemKind
	Peer                  Peer
	ItemID                int64
	SecondaryID           int64
	AuthorUserID          int64
	EvidenceSchemaVersion int
	Evidence              json.RawMessage
	EvidenceHash          [sha256.Size]byte
}

type ModerationMediaHold struct {
	ItemIndex  int
	Kind       ModerationMediaKind
	StorageKey string
}

// ModerationReport is immutable after acceptance. ID is assigned by the store;
// Fingerprint is a deterministic SHA-256 of the immutable client intent and
// evidence identity, excluding CreatedAt.
type ModerationReport struct {
	ID              int64
	ReporterUserID  int64
	Source          ModerationReportSource
	Target          Peer
	Reason          ModerationReason
	Option          string
	Comment         string
	CommentHash     [sha256.Size]byte
	Fingerprint     [sha256.Size]byte
	TaxonomyVersion int
	Items           []ModerationReportItem
	MediaHolds      []ModerationMediaHold
	CreatedAt       time.Time
}

type ModerationReportDraft struct {
	ReporterUserID  int64
	Source          ModerationReportSource
	Target          Peer
	Reason          ModerationReason
	Option          string
	Comment         string
	TaxonomyVersion int
	Items           []ModerationReportItem
	MediaHolds      []ModerationMediaHold
	CreatedAt       time.Time
}

type ModerationMessageReportRequest struct {
	ReporterUserID int64
	Target         Peer
	MessageIDs     []int
	Reason         ModerationReason
	Option         string
	Comment        string
	CreatedAt      time.Time
}

type ModerationStoryReportRequest struct {
	ReporterUserID int64
	Target         Peer
	StoryIDs       []int
	Reason         ModerationReason
	Option         string
	Comment        string
	CreatedAt      time.Time
}

type ModerationProfilePhotoReportRequest struct {
	ReporterUserID int64
	Target         Peer
	PhotoID        int64
	AccessHash     int64
	FileReference  []byte
	Reason         ModerationReason
	Comment        string
	CreatedAt      time.Time
}

type ModerationChannelSpamReportRequest struct {
	ReporterUserID    int64
	ChannelID         int64
	ParticipantUserID int64
	MessageIDs        []int
	CreatedAt         time.Time
}

type ModerationReactionReportRequest struct {
	ReporterUserID int64
	Target         Peer
	MessageID      int
	ReactorUserID  int64
	CreatedAt      time.Time
}

// NewModerationReport canonicalizes item order and computes all content hashes.
// Callers must pass snapshots, not mutable domain objects.
func NewModerationReport(draft ModerationReportDraft) (ModerationReport, error) {
	originalItems := cloneModerationItems(draft.Items)
	report := ModerationReport{
		ReporterUserID:  draft.ReporterUserID,
		Source:          draft.Source,
		Target:          draft.Target,
		Reason:          draft.Reason,
		Option:          draft.Option,
		Comment:         draft.Comment,
		TaxonomyVersion: draft.TaxonomyVersion,
		Items:           cloneModerationItems(originalItems),
		MediaHolds:      append([]ModerationMediaHold(nil), draft.MediaHolds...),
		CreatedAt:       draft.CreatedAt,
	}
	if report.TaxonomyVersion == 0 {
		report.TaxonomyVersion = ModerationTaxonomyVersion
	}
	report.CommentHash = sha256.Sum256([]byte(report.Comment))
	for i := range report.Items {
		evidence, err := CanonicalModerationEvidence(report.Items[i].Evidence)
		if err != nil {
			return ModerationReport{}, err
		}
		report.Items[i].Evidence = evidence
		report.Items[i].EvidenceHash = sha256.Sum256(report.Items[i].Evidence)
	}
	sort.Slice(report.Items, func(i, j int) bool {
		return moderationItemLess(report.Items[i], report.Items[j])
	})
	canonicalIndexes := make(map[moderationItemIdentity]int, len(report.Items))
	for i, item := range report.Items {
		canonicalIndexes[moderationItemIdentityOf(item)] = i
	}
	for i := range report.MediaHolds {
		oldIndex := report.MediaHolds[i].ItemIndex
		if oldIndex >= 0 && oldIndex < len(originalItems) {
			if canonicalIndex, ok := canonicalIndexes[moderationItemIdentityOf(originalItems[oldIndex])]; ok {
				report.MediaHolds[i].ItemIndex = canonicalIndex
			}
		}
	}
	sort.Slice(report.MediaHolds, func(i, j int) bool {
		a, b := report.MediaHolds[i], report.MediaHolds[j]
		if a.ItemIndex != b.ItemIndex {
			return a.ItemIndex < b.ItemIndex
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.StorageKey < b.StorageKey
	})
	fingerprint, err := moderationReportFingerprint(report)
	if err != nil {
		return ModerationReport{}, err
	}
	report.Fingerprint = fingerprint
	if err := report.Validate(); err != nil {
		return ModerationReport{}, err
	}
	return report, nil
}

func (r ModerationReport) Validate() error {
	if r.ID < 0 || r.ReporterUserID <= 0 || !r.Source.Valid() ||
		!moderationPeerValid(r.Target) || !r.Reason.Valid() ||
		r.Option == "" || len(r.Option) > MaxModerationOptionBytes ||
		!utf8.ValidString(r.Option) || !utf8.ValidString(r.Comment) ||
		utf8.RuneCountInString(r.Comment) > MaxModerationCommentRunes ||
		r.TaxonomyVersion <= 0 || r.TaxonomyVersion > 32767 ||
		len(r.Items) == 0 || len(r.Items) > MaxModerationReportItems ||
		len(r.MediaHolds) > MaxModerationMediaHolds || r.CreatedAt.IsZero() ||
		r.CommentHash != sha256.Sum256([]byte(r.Comment)) {
		return ErrModerationReportInvalid
	}
	totalEvidence := 0
	seenItems := make(map[moderationItemIdentity]struct{}, len(r.Items))
	for i, item := range r.Items {
		if !item.Kind.Valid() || !moderationPeerValid(item.Peer) ||
			item.ItemID <= 0 || item.SecondaryID < 0 || item.AuthorUserID < 0 ||
			item.EvidenceSchemaVersion <= 0 || item.EvidenceSchemaVersion > 32767 ||
			len(item.Evidence) == 0 || len(item.Evidence) > MaxModerationEvidenceBytes ||
			!json.Valid(item.Evidence) ||
			item.EvidenceHash != sha256.Sum256(item.Evidence) {
			return ErrModerationReportInvalid
		}
		canonical, err := CanonicalModerationEvidence(item.Evidence)
		if err != nil || !bytes.Equal(canonical, item.Evidence) {
			return ErrModerationReportInvalid
		}
		if i > 0 && moderationItemLess(item, r.Items[i-1]) {
			return ErrModerationReportInvalid
		}
		identity := moderationItemIdentityOf(item)
		if _, duplicate := seenItems[identity]; duplicate {
			return ErrModerationReportInvalid
		}
		seenItems[identity] = struct{}{}
		totalEvidence += len(item.Evidence)
		if totalEvidence > MaxModerationTotalEvidenceBytes {
			return ErrModerationReportInvalid
		}
	}
	seenHolds := make(map[ModerationMediaHold]struct{}, len(r.MediaHolds))
	for _, hold := range r.MediaHolds {
		if hold.ItemIndex < 0 || hold.ItemIndex >= len(r.Items) ||
			!hold.Kind.Valid() || hold.StorageKey == "" ||
			len(hold.StorageKey) > MaxModerationMediaStorageKeyBytes ||
			!utf8.ValidString(hold.StorageKey) {
			return ErrModerationReportInvalid
		}
		if _, duplicate := seenHolds[hold]; duplicate {
			return ErrModerationReportInvalid
		}
		seenHolds[hold] = struct{}{}
	}
	fingerprint, err := moderationReportFingerprint(r)
	if err != nil || fingerprint != r.Fingerprint {
		return ErrModerationReportInvalid
	}
	return nil
}

type moderationItemIdentity struct {
	Kind        ModerationReportItemKind
	PeerType    PeerType
	PeerID      int64
	ItemID      int64
	SecondaryID int64
}

func moderationItemIdentityOf(item ModerationReportItem) moderationItemIdentity {
	return moderationItemIdentity{
		Kind: item.Kind, PeerType: item.Peer.Type, PeerID: item.Peer.ID,
		ItemID: item.ItemID, SecondaryID: item.SecondaryID,
	}
}

func moderationItemLess(a, b ModerationReportItem) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Peer.Type != b.Peer.Type {
		return a.Peer.Type < b.Peer.Type
	}
	if a.Peer.ID != b.Peer.ID {
		return a.Peer.ID < b.Peer.ID
	}
	if a.ItemID != b.ItemID {
		return a.ItemID < b.ItemID
	}
	return a.SecondaryID < b.SecondaryID
}

func moderationPeerValid(peer Peer) bool {
	return peer.ID > 0 && (peer.Type == PeerTypeUser || peer.Type == PeerTypeChannel)
}

// CanonicalModerationEvidence normalizes a JSON object with Go's deterministic
// map-key ordering. This keeps evidence hashes stable after PostgreSQL jsonb
// normalizes whitespace and object key order.
func CanonicalModerationEvidence(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, ErrModerationReportInvalid
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrModerationReportInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) == 0 || len(canonical) > MaxModerationEvidenceBytes {
		return nil, ErrModerationReportInvalid
	}
	return canonical, nil
}

type moderationFingerprintItem struct {
	Kind                  ModerationReportItemKind `json:"kind"`
	PeerType              PeerType                 `json:"peer_type"`
	PeerID                int64                    `json:"peer_id"`
	ItemID                int64                    `json:"item_id"`
	SecondaryID           int64                    `json:"secondary_id"`
	AuthorUserID          int64                    `json:"author_user_id"`
	EvidenceSchemaVersion int                      `json:"evidence_schema_version"`
	EvidenceHash          [sha256.Size]byte        `json:"evidence_hash"`
}

type moderationFingerprintPayload struct {
	Version         int                         `json:"version"`
	ReporterUserID  int64                       `json:"reporter_user_id"`
	Source          ModerationReportSource      `json:"source"`
	TargetType      PeerType                    `json:"target_type"`
	TargetID        int64                       `json:"target_id"`
	Reason          ModerationReason            `json:"reason"`
	Option          string                      `json:"option"`
	CommentHash     [sha256.Size]byte           `json:"comment_hash"`
	TaxonomyVersion int                         `json:"taxonomy_version"`
	Items           []moderationFingerprintItem `json:"items"`
}

func moderationReportFingerprint(report ModerationReport) ([sha256.Size]byte, error) {
	items := make([]moderationFingerprintItem, 0, len(report.Items))
	for _, item := range report.Items {
		items = append(items, moderationFingerprintItem{
			Kind: item.Kind, PeerType: item.Peer.Type, PeerID: item.Peer.ID,
			ItemID: item.ItemID, SecondaryID: item.SecondaryID,
			AuthorUserID:          item.AuthorUserID,
			EvidenceSchemaVersion: item.EvidenceSchemaVersion,
			EvidenceHash:          item.EvidenceHash,
		})
	}
	raw, err := json.Marshal(moderationFingerprintPayload{
		Version: 1, ReporterUserID: report.ReporterUserID, Source: report.Source,
		TargetType: report.Target.Type, TargetID: report.Target.ID,
		Reason: report.Reason, Option: report.Option,
		CommentHash: report.CommentHash, TaxonomyVersion: report.TaxonomyVersion,
		Items: items,
	})
	if err != nil {
		return [sha256.Size]byte{}, ErrModerationReportInvalid
	}
	return sha256.Sum256(raw), nil
}

func cloneModerationItems(items []ModerationReportItem) []ModerationReportItem {
	out := make([]ModerationReportItem, len(items))
	copy(out, items)
	for i := range out {
		out[i].Evidence = append(json.RawMessage(nil), items[i].Evidence...)
	}
	return out
}

func CloneModerationReport(report ModerationReport) ModerationReport {
	report.Items = cloneModerationItems(report.Items)
	report.MediaHolds = append([]ModerationMediaHold(nil), report.MediaHolds...)
	return report
}
