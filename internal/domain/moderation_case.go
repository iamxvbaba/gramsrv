package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"time"
	"unicode/utf8"
)

const (
	MaxModerationActorBytes           = 128
	MaxModerationDecisionCommandBytes = 120
	MaxModerationDecisionTextRunes    = 2000
	MaxModerationActionPayload        = 64 << 10
	MaxModerationCasePage             = 100
	MaxModerationCaseDetailEntries    = 100
	MaxModerationActionsPerCase       = 100
	MaxModerationAppealTextRunes      = 4000
	MaxModerationActionAttempts       = 20
	MaxModerationAppealLinksPerCase   = 20
	MaxModerationAppealLinkLifetime   = 90 * 24 * time.Hour
)

type ModerationSeverity int16

const (
	ModerationSeverityLow ModerationSeverity = iota + 1
	ModerationSeverityMedium
	ModerationSeverityHigh
	ModerationSeverityCritical
)

func (s ModerationSeverity) Valid() bool {
	return s >= ModerationSeverityLow && s <= ModerationSeverityCritical
}

func ModerationSeverityForReason(reason ModerationReason) ModerationSeverity {
	switch reason {
	case ModerationReasonChildAbuse:
		return ModerationSeverityCritical
	case ModerationReasonViolence, ModerationReasonPornography,
		ModerationReasonIllegalDrugs, ModerationReasonPersonalDetails:
		return ModerationSeverityHigh
	case ModerationReasonFake, ModerationReasonCopyright:
		return ModerationSeverityMedium
	default:
		return ModerationSeverityLow
	}
}

type ModerationCaseStatus string

const (
	ModerationCaseOpen          ModerationCaseStatus = "open"
	ModerationCaseInReview      ModerationCaseStatus = "in_review"
	ModerationCaseActionPending ModerationCaseStatus = "action_pending"
	ModerationCaseActionFailed  ModerationCaseStatus = "action_failed"
	ModerationCaseResolved      ModerationCaseStatus = "resolved"
	ModerationCaseDismissed     ModerationCaseStatus = "dismissed"
	ModerationCaseAppealReview  ModerationCaseStatus = "appeal_review"
)

func (s ModerationCaseStatus) Valid() bool {
	switch s {
	case ModerationCaseOpen, ModerationCaseInReview,
		ModerationCaseActionPending, ModerationCaseResolved,
		ModerationCaseActionFailed, ModerationCaseDismissed,
		ModerationCaseAppealReview:
		return true
	default:
		return false
	}
}

func (s ModerationCaseStatus) Active() bool {
	switch s {
	case ModerationCaseOpen, ModerationCaseInReview,
		ModerationCaseActionPending, ModerationCaseActionFailed,
		ModerationCaseAppealReview:
		return true
	default:
		return false
	}
}

type ModerationDecisionKind string

const (
	ModerationDecisionNoViolation ModerationDecisionKind = "no_violation"
	ModerationDecisionViolation   ModerationDecisionKind = "violation"
	ModerationDecisionAppealGrant ModerationDecisionKind = "appeal_granted"
	ModerationDecisionAppealDeny  ModerationDecisionKind = "appeal_denied"
)

func (k ModerationDecisionKind) Valid() bool {
	switch k {
	case ModerationDecisionNoViolation, ModerationDecisionViolation,
		ModerationDecisionAppealGrant, ModerationDecisionAppealDeny:
		return true
	default:
		return false
	}
}

type ModerationActionKind string

const (
	ModerationActionMarkScam             ModerationActionKind = "mark_scam"
	ModerationActionMarkFake             ModerationActionKind = "mark_fake"
	ModerationActionClearPeerFlags       ModerationActionKind = "clear_peer_flags"
	ModerationActionFreezeAccount        ModerationActionKind = "freeze_account"
	ModerationActionUnfreezeAccount      ModerationActionKind = "unfreeze_account"
	ModerationActionDeletePrivateMessage ModerationActionKind = "delete_private_message"
	ModerationActionDeleteChannelMessage ModerationActionKind = "delete_channel_message"
	ModerationActionDeleteAccount        ModerationActionKind = "delete_account"
)

func (k ModerationActionKind) Valid() bool {
	switch k {
	case ModerationActionMarkScam, ModerationActionMarkFake,
		ModerationActionClearPeerFlags, ModerationActionFreezeAccount,
		ModerationActionUnfreezeAccount,
		ModerationActionDeletePrivateMessage,
		ModerationActionDeleteChannelMessage,
		ModerationActionDeleteAccount:
		return true
	default:
		return false
	}
}

type ModerationActionStatus string

const (
	ModerationActionPending    ModerationActionStatus = "pending"
	ModerationActionProcessing ModerationActionStatus = "processing"
	ModerationActionSucceeded  ModerationActionStatus = "succeeded"
	ModerationActionSuperseded ModerationActionStatus = "superseded"
	ModerationActionRetry      ModerationActionStatus = "retry"
	ModerationActionFailed     ModerationActionStatus = "failed"
)

func (s ModerationActionStatus) Valid() bool {
	switch s {
	case ModerationActionPending, ModerationActionProcessing,
		ModerationActionSucceeded, ModerationActionSuperseded,
		ModerationActionRetry,
		ModerationActionFailed:
		return true
	default:
		return false
	}
}

// ModerationSanctionFamily groups reversible actions that mutate the same
// target-scoped state. Only the latest desired action in a family may execute;
// older queued work is retained as superseded audit history.
type ModerationSanctionFamily string

const (
	ModerationSanctionPeerFlags     ModerationSanctionFamily = "peer_flags"
	ModerationSanctionAccountFreeze ModerationSanctionFamily = "account_freeze"
)

func (k ModerationActionKind) SanctionFamily() (ModerationSanctionFamily, bool) {
	switch k {
	case ModerationActionMarkScam, ModerationActionMarkFake,
		ModerationActionClearPeerFlags:
		return ModerationSanctionPeerFlags, true
	case ModerationActionFreezeAccount, ModerationActionUnfreezeAccount:
		return ModerationSanctionAccountFreeze, true
	default:
		return "", false
	}
}

type ModerationAppealStatus string

const (
	ModerationAppealPending  ModerationAppealStatus = "pending"
	ModerationAppealGranted  ModerationAppealStatus = "granted"
	ModerationAppealRejected ModerationAppealStatus = "rejected"
)

func (s ModerationAppealStatus) Valid() bool {
	switch s {
	case ModerationAppealPending, ModerationAppealGranted, ModerationAppealRejected:
		return true
	default:
		return false
	}
}

type ModerationCase struct {
	ID                    int64
	Target                Peer
	Status                ModerationCaseStatus
	Severity              ModerationSeverity
	AssignedTo            string
	Version               int64
	ReportCount           int
	DistinctReporterCount int
	FirstReportAt         time.Time
	LastReportAt          time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (c ModerationCase) Validate() error {
	if c.ID <= 0 || !moderationPeerValid(c.Target) || !c.Status.Valid() ||
		!c.Severity.Valid() || c.Version <= 0 || c.ReportCount <= 0 ||
		c.DistinctReporterCount <= 0 ||
		c.DistinctReporterCount > c.ReportCount ||
		len(c.AssignedTo) > MaxModerationActorBytes ||
		!utf8.ValidString(c.AssignedTo) || c.FirstReportAt.IsZero() ||
		c.LastReportAt.Before(c.FirstReportAt) || c.CreatedAt.IsZero() ||
		c.UpdatedAt.Before(c.CreatedAt) {
		return ErrModerationCaseInvalid
	}
	if (c.Status == ModerationCaseInReview ||
		c.Status == ModerationCaseActionPending ||
		c.Status == ModerationCaseActionFailed) && c.AssignedTo == "" {
		return ErrModerationCaseInvalid
	}
	return nil
}

type ModerationCaseFilter struct {
	Statuses     []ModerationCaseStatus
	AssignedTo   string
	Target       Peer
	BeforeUpdate time.Time
	BeforeID     int64
	Limit        int
}

func (f ModerationCaseFilter) Validate() error {
	if f.Limit <= 0 || f.Limit > MaxModerationCasePage ||
		len(f.AssignedTo) > MaxModerationActorBytes ||
		!utf8.ValidString(f.AssignedTo) || f.BeforeID < 0 {
		return ErrModerationCaseInvalid
	}
	if f.Target.ID != 0 && !moderationPeerValid(f.Target) {
		return ErrModerationCaseInvalid
	}
	if f.Target.ID == 0 && f.Target.Type != "" {
		return ErrModerationCaseInvalid
	}
	for _, status := range f.Statuses {
		if !status.Valid() {
			return ErrModerationCaseInvalid
		}
	}
	return nil
}

type ModerationCaseDetail struct {
	Case      ModerationCase
	ReportIDs []int64
	Decisions []ModerationDecision
	Actions   []ModerationAction
	Appeals   []ModerationAppeal
}

type ModerationDecision struct {
	ID          int64
	CaseID      int64
	AppealID    int64
	Kind        ModerationDecisionKind
	Actor       string
	Reason      string
	CommandID   string
	Fingerprint [sha256.Size]byte
	CreatedAt   time.Time
}

type ModerationActionDraft struct {
	Kind    ModerationActionKind
	Payload json.RawMessage
}

type ModerationDecisionRequest struct {
	CaseID          int64
	AppealID        int64
	ExpectedVersion int64
	Actor           string
	Reason          string
	CommandID       string
	Kind            ModerationDecisionKind
	Actions         []ModerationActionDraft
	Fingerprint     [sha256.Size]byte
	CreatedAt       time.Time
}

func NewModerationDecisionRequest(request ModerationDecisionRequest) (ModerationDecisionRequest, error) {
	out := request
	out.Actions = append([]ModerationActionDraft(nil), request.Actions...)
	for i := range out.Actions {
		canonical, err := CanonicalModerationActionPayload(out.Actions[i].Payload)
		if err != nil {
			return ModerationDecisionRequest{}, err
		}
		out.Actions[i].Payload = canonical
	}
	fingerprint, err := moderationDecisionFingerprint(out)
	if err != nil {
		return ModerationDecisionRequest{}, err
	}
	out.Fingerprint = fingerprint
	if err := out.Validate(); err != nil {
		return ModerationDecisionRequest{}, err
	}
	return out, nil
}

func (r ModerationDecisionRequest) Validate() error {
	if r.CaseID <= 0 || r.ExpectedVersion <= 0 || !r.Kind.Valid() ||
		r.Actor == "" || len(r.Actor) > MaxModerationActorBytes ||
		!utf8.ValidString(r.Actor) || r.CommandID == "" ||
		len(r.CommandID) > MaxModerationDecisionCommandBytes ||
		!utf8.ValidString(r.CommandID) ||
		r.Reason == "" || !utf8.ValidString(r.Reason) ||
		utf8.RuneCountInString(r.Reason) > MaxModerationDecisionTextRunes ||
		len(r.Actions) > MaxModerationActionsPerCase ||
		r.Fingerprint == ([sha256.Size]byte{}) || r.CreatedAt.IsZero() {
		return ErrModerationCaseInvalid
	}
	if r.Kind == ModerationDecisionNoViolation && len(r.Actions) != 0 {
		return ErrModerationActionInvalid
	}
	if r.Kind == ModerationDecisionViolation && len(r.Actions) == 0 {
		return ErrModerationActionInvalid
	}
	if (r.Kind == ModerationDecisionAppealGrant ||
		r.Kind == ModerationDecisionAppealDeny) != (r.AppealID > 0) {
		return ErrModerationCaseInvalid
	}
	if r.Kind == ModerationDecisionAppealDeny && len(r.Actions) != 0 {
		return ErrModerationActionInvalid
	}
	if (r.Kind == ModerationDecisionNoViolation ||
		r.Kind == ModerationDecisionViolation) && r.AppealID != 0 {
		return ErrModerationCaseInvalid
	}
	for i := range r.Actions {
		canonical, err := CanonicalModerationActionPayload(r.Actions[i].Payload)
		if !r.Actions[i].Kind.Valid() || err != nil ||
			!bytes.Equal(canonical, r.Actions[i].Payload) {
			return ErrModerationActionInvalid
		}
	}
	fingerprint, err := moderationDecisionFingerprint(r)
	if err != nil || fingerprint != r.Fingerprint {
		return ErrModerationCaseInvalid
	}
	return nil
}

type ModerationAction struct {
	ID          int64
	CaseID      int64
	DecisionID  int64
	Kind        ModerationActionKind
	Payload     json.RawMessage
	Status      ModerationActionStatus
	Attempts    int
	AvailableAt time.Time
	LeaseUntil  time.Time
	LastError   string
	CommandID   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (a ModerationAction) Validate() error {
	canonical, err := CanonicalModerationActionPayload(a.Payload)
	if a.ID <= 0 || a.CaseID <= 0 || a.DecisionID <= 0 ||
		!a.Kind.Valid() || !a.Status.Valid() || a.Attempts < 0 ||
		a.Attempts > MaxModerationActionAttempts || a.AvailableAt.IsZero() ||
		a.CommandID == "" || len(a.CommandID) > 160 ||
		!utf8.ValidString(a.CommandID) || a.CreatedAt.IsZero() ||
		a.UpdatedAt.Before(a.CreatedAt) || err != nil ||
		!bytes.Equal(canonical, a.Payload) {
		return ErrModerationActionInvalid
	}
	return nil
}

type ModerationAppeal struct {
	ID                 int64
	CaseID             int64
	AppellantUserID    int64
	Text               string
	TextHash           [sha256.Size]byte
	Fingerprint        [sha256.Size]byte
	Status             ModerationAppealStatus
	PreviousCaseStatus ModerationCaseStatus
	Reviewer           string
	ReviewReason       string
	CreatedAt          time.Time
	ReviewedAt         time.Time
}

// ModerationAppealLink is a hash-only bearer capability issued for the user
// targeted by a moderation case. The raw token is never persisted.
type ModerationAppealLink struct {
	ID              int64
	CaseID          int64
	AppellantUserID int64
	TokenHash       [sha256.Size]byte
	ExpiresAt       time.Time
	AppealID        int64
	CreatedAt       time.Time
	ConsumedAt      time.Time
}

func (l ModerationAppealLink) Validate() error {
	if l.ID < 0 || l.CaseID <= 0 || l.AppellantUserID <= 0 ||
		l.TokenHash == ([sha256.Size]byte{}) || l.CreatedAt.IsZero() ||
		!l.ExpiresAt.After(l.CreatedAt) ||
		l.ExpiresAt.Sub(l.CreatedAt) > MaxModerationAppealLinkLifetime ||
		l.AppealID < 0 {
		return ErrModerationAppealLinkInvalid
	}
	if l.AppealID == 0 {
		if !l.ConsumedAt.IsZero() {
			return ErrModerationAppealLinkInvalid
		}
	} else if l.ConsumedAt.IsZero() {
		return ErrModerationAppealLinkInvalid
	}
	return nil
}

func NewModerationAppeal(caseID, appellantUserID int64, previousStatus ModerationCaseStatus, text string, createdAt time.Time) (ModerationAppeal, error) {
	appeal := ModerationAppeal{
		CaseID: caseID, AppellantUserID: appellantUserID, Text: text,
		TextHash: sha256.Sum256([]byte(text)), Status: ModerationAppealPending,
		PreviousCaseStatus: previousStatus, CreatedAt: createdAt,
	}
	raw, err := json.Marshal(struct {
		Version         int
		CaseID          int64
		AppellantUserID int64
		PreviousStatus  ModerationCaseStatus
		TextHash        [sha256.Size]byte
	}{1, caseID, appellantUserID, previousStatus, appeal.TextHash})
	if err != nil {
		return ModerationAppeal{}, ErrModerationCaseInvalid
	}
	appeal.Fingerprint = sha256.Sum256(raw)
	if err := appeal.Validate(); err != nil {
		return ModerationAppeal{}, err
	}
	return appeal, nil
}

func (a ModerationAppeal) Validate() error {
	if a.ID < 0 || a.CaseID <= 0 || a.AppellantUserID <= 0 ||
		a.Text == "" || !utf8.ValidString(a.Text) ||
		utf8.RuneCountInString(a.Text) > MaxModerationAppealTextRunes ||
		a.TextHash != sha256.Sum256([]byte(a.Text)) ||
		a.Fingerprint == ([sha256.Size]byte{}) || !a.Status.Valid() ||
		(a.PreviousCaseStatus != ModerationCaseResolved &&
			a.PreviousCaseStatus != ModerationCaseDismissed) ||
		len(a.Reviewer) > MaxModerationActorBytes ||
		!utf8.ValidString(a.Reviewer) || !utf8.ValidString(a.ReviewReason) ||
		utf8.RuneCountInString(a.ReviewReason) > MaxModerationDecisionTextRunes ||
		a.CreatedAt.IsZero() {
		return ErrModerationCaseInvalid
	}
	if a.Status == ModerationAppealPending {
		if a.Reviewer != "" || a.ReviewReason != "" || !a.ReviewedAt.IsZero() {
			return ErrModerationCaseInvalid
		}
	} else if a.Reviewer == "" || a.ReviewReason == "" || a.ReviewedAt.IsZero() {
		return ErrModerationCaseInvalid
	}
	return nil
}

func CanonicalModerationActionPayload(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, ErrModerationActionInvalid
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrModerationActionInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > MaxModerationActionPayload {
		return nil, ErrModerationActionInvalid
	}
	return canonical, nil
}

func moderationDecisionFingerprint(request ModerationDecisionRequest) ([sha256.Size]byte, error) {
	raw, err := json.Marshal(struct {
		Version         int
		CaseID          int64
		AppealID        int64
		ExpectedVersion int64
		Actor           string
		Reason          string
		CommandID       string
		Kind            ModerationDecisionKind
		Actions         []ModerationActionDraft
	}{
		Version: 1, CaseID: request.CaseID,
		AppealID:        request.AppealID,
		ExpectedVersion: request.ExpectedVersion, Actor: request.Actor,
		Reason: request.Reason, CommandID: request.CommandID,
		Kind: request.Kind, Actions: request.Actions,
	})
	if err != nil {
		return [sha256.Size]byte{}, ErrModerationCaseInvalid
	}
	return sha256.Sum256(raw), nil
}
