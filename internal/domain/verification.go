package domain

import (
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Official platform verification.
//
// This is the badge official clients render from user#b1b8cc83 verified:flags.17
// and channel#d49f34c6 verified:flags.7 (layer 228). It is deliberately NOT the
// third-party mechanism built on botVerification#f93cd45c /
// bots.setCustomVerification#8b89dfbd / bot_verification_icon, where an outside
// organisation attaches its own icon. The two must never be conflated: an
// application reviewed here flips the platform flag on the peer record and
// nothing else.
//
// Applications are filed through the built-in @verifybot and decided in the
// admin panel. The application record is the durable audit subject: it is never
// deleted, only moved through its status machine.

// Verification target kinds. A verification subject is always a public presence:
// a bot, a public channel or a public supergroup. Ordinary user accounts are
// excluded unless the operator opts in via configuration.
type VerificationTargetType string

const (
	VerificationTargetBot        VerificationTargetType = "bot"
	VerificationTargetChannel    VerificationTargetType = "channel"
	VerificationTargetSupergroup VerificationTargetType = "supergroup"
	VerificationTargetUser       VerificationTargetType = "user"
)

// Valid reports whether the target type is modelled.
func (t VerificationTargetType) Valid() bool {
	switch t {
	case VerificationTargetBot, VerificationTargetChannel, VerificationTargetSupergroup, VerificationTargetUser:
		return true
	default:
		return false
	}
}

// PeerType maps the target kind onto the peer namespace that holds it: bots and
// users live in the user namespace, channels and supergroups in the channel one.
func (t VerificationTargetType) PeerType() PeerType {
	switch t {
	case VerificationTargetChannel, VerificationTargetSupergroup:
		return PeerTypeChannel
	default:
		return PeerTypeUser
	}
}

// VerificationStatus is the application lifecycle.
//
// draft     -- being filled in through the bot dialog, not yet visible to review
// submitted -- awaiting a reviewer
// in_review -- claimed by a reviewer
// approved  -- decided in favour; the target carries the platform flag
// rejected  -- decided against, with a mandatory reason
// cancelled -- withdrawn by the applicant
type VerificationStatus string

const (
	VerificationStatusDraft     VerificationStatus = "draft"
	VerificationStatusSubmitted VerificationStatus = "submitted"
	VerificationStatusInReview  VerificationStatus = "in_review"
	VerificationStatusApproved  VerificationStatus = "approved"
	VerificationStatusRejected  VerificationStatus = "rejected"
	VerificationStatusCancelled VerificationStatus = "cancelled"
)

// Valid reports whether the status is modelled.
func (s VerificationStatus) Valid() bool {
	switch s {
	case VerificationStatusDraft, VerificationStatusSubmitted, VerificationStatusInReview,
		VerificationStatusApproved, VerificationStatusRejected, VerificationStatusCancelled:
		return true
	default:
		return false
	}
}

// Active reports whether the application still occupies its target. Exactly one
// active application per target is allowed, which the store enforces with a
// partial unique index over these statuses.
func (s VerificationStatus) Active() bool {
	switch s {
	case VerificationStatusDraft, VerificationStatusSubmitted, VerificationStatusInReview:
		return true
	default:
		return false
	}
}

// Decided reports whether a reviewer has closed the application.
func (s VerificationStatus) Decided() bool {
	return s == VerificationStatusApproved || s == VerificationStatusRejected
}

// CanTransitionVerificationStatus is the single source of truth for the status
// machine. Both the bot dialog and the review API validate against it, so an
// operator cannot drive an application into a state the applicant path forbids.
func CanTransitionVerificationStatus(from, to VerificationStatus) bool {
	if !from.Valid() || !to.Valid() || from == to {
		return false
	}
	switch from {
	case VerificationStatusDraft:
		return to == VerificationStatusSubmitted || to == VerificationStatusCancelled
	case VerificationStatusSubmitted:
		return to == VerificationStatusInReview || to == VerificationStatusApproved ||
			to == VerificationStatusRejected || to == VerificationStatusCancelled
	case VerificationStatusInReview:
		return to == VerificationStatusApproved || to == VerificationStatusRejected ||
			to == VerificationStatusSubmitted || to == VerificationStatusCancelled
	default:
		// Decided and cancelled applications are terminal: history is kept, a new
		// attempt is a new application.
		return false
	}
}

// Field bounds. They are enforced in the domain so the bot dialog, the admin API
// and both store backends reject the same inputs, and they mirror the CHECK
// constraints on verification_applications.
const (
	MaxVerificationCategoryLength    = 64
	MaxVerificationDescriptionLength = 1024
	MinVerificationDescriptionLength = 40
	MaxVerificationCommentLength     = 1024
	MaxVerificationURLLength         = 512
	MaxVerificationSocialLinks       = 10
	MaxVerificationPressLinks        = 10
	// MinVerificationPressLinks is the official bar: independent coverage is what
	// distinguishes a verifiable public presence from a self-declared one.
	MinVerificationPressLinks       = 2
	MaxVerificationReasonLength     = 1024
	MaxVerificationNoteLength       = 2048
	MaxVerificationTitleLength      = 256
	MaxVerificationReviewerLength   = 128
	MaxVerificationCorrelationLen   = 128
	MaxVerificationEventPayloadSize = 8192
)

// VerificationCategories is the closed set of project categories offered by the
// bot. A closed set keeps the review queue groupable and keeps free text out of
// a field the panel filters on.
var VerificationCategories = []string{
	"media",
	"government",
	"company",
	"brand",
	"sport",
	"culture",
	"education",
	"nonprofit",
	"public_figure",
	"service",
	"other",
}

// ValidVerificationCategory reports whether the category is offered.
func ValidVerificationCategory(category string) bool {
	for _, item := range VerificationCategories {
		if item == category {
			return true
		}
	}
	return false
}

var (
	// ErrVerificationApplicationNotFound reports a missing application.
	ErrVerificationApplicationNotFound = errors.New("verification application not found")
	// ErrVerificationApplicationExists reports an active application already
	// occupying the target.
	ErrVerificationApplicationExists = errors.New("verification application already active for target")
	// ErrVerificationApplicationInvalid rejects a malformed application.
	ErrVerificationApplicationInvalid = errors.New("verification application invalid")
	// ErrVerificationStatusInvalid rejects an impossible status transition.
	ErrVerificationStatusInvalid = errors.New("verification status transition invalid")
	// ErrVerificationVersionConflict reports a lost optimistic-locking race, which
	// is what two reviewers deciding at once must produce for the loser.
	ErrVerificationVersionConflict = errors.New("verification application changed concurrently")
	// ErrVerificationTargetInvalid rejects a target that cannot be verified.
	ErrVerificationTargetInvalid = errors.New("verification target invalid")
	// ErrVerificationTargetNotPublic rejects a target without a public username.
	ErrVerificationTargetNotPublic = errors.New("verification target has no public username")
	// ErrVerificationTargetAlreadyVerified rejects a target that already carries
	// the platform flag.
	ErrVerificationTargetAlreadyVerified = errors.New("verification target already verified")
	// ErrVerificationTargetRestricted rejects a scam/fake/frozen/deleted target.
	ErrVerificationTargetRestricted = errors.New("verification target restricted")
	// ErrVerificationTargetSystem rejects a built-in system entity.
	ErrVerificationTargetSystem = errors.New("verification target is a system entity")
	// ErrVerificationNotOwner reports that the applicant does not control the
	// target.
	ErrVerificationNotOwner = errors.New("applicant does not control verification target")
	// ErrVerificationReasonRequired reports a rejection or revocation without a
	// reason, which the audit trail must never contain.
	ErrVerificationReasonRequired = errors.New("verification decision reason required")
	// ErrVerificationCooldown reports a re-application filed before the
	// configured cooldown after a rejection has elapsed.
	ErrVerificationCooldown = errors.New("verification re-application cooldown active")
	// ErrVerificationRateLimited reports too many applications in the window.
	ErrVerificationRateLimited = errors.New("verification rate limit exceeded")
	// ErrVerificationURLInvalid rejects a link that is not a plain http(s) URL to
	// a public host.
	ErrVerificationURLInvalid = errors.New("verification link invalid")
	// ErrVerificationUserTargetsDisabled reports that plain user accounts are not
	// accepted by this deployment.
	ErrVerificationUserTargetsDisabled = errors.New("verification of user accounts is disabled")
)

// VerificationApplication is the persistent application record.
//
// The target is addressed by its stable peer id; TargetTitle and TargetUsername
// are a snapshot for the review UI and audit trail, because a username can move
// between peers and a title can change after submission.
type VerificationApplication struct {
	ID               int64
	ApplicantUserID  int64
	TargetType       VerificationTargetType
	TargetID         int64
	TargetTitle      string
	TargetUsername   string
	TargetAccessHash int64
	Category         string
	Description      string
	OfficialWebsite  string
	SocialLinks      []string
	PressLinks       []string
	AdditionalNote   string
	Status           VerificationStatus
	CreatedAt        time.Time
	UpdatedAt        time.Time
	SubmittedAt      time.Time
	ReviewedAt       time.Time
	ReviewerAdminID  string
	DecisionReason   string
	InternalNote     string
	CorrelationID    string
	// Version is the optimistic-locking token. Every mutation submits the version
	// it read and the store refuses a stale one, which is how two reviewers
	// deciding the same application at the same time produce exactly one decision.
	Version int64
}

// Target returns the peer the application is about.
func (a VerificationApplication) Target() Peer {
	return Peer{Type: a.TargetType.PeerType(), ID: a.TargetID}
}

// Editable reports whether the applicant may still change the payload.
func (a VerificationApplication) Editable() bool {
	return a.Status == VerificationStatusDraft
}

// VerificationApplicationEventKind is one entry of the immutable per-application
// history rendered in the panel.
type VerificationApplicationEventKind string

const (
	VerificationEventCreated   VerificationApplicationEventKind = "created"
	VerificationEventUpdated   VerificationApplicationEventKind = "updated"
	VerificationEventSubmitted VerificationApplicationEventKind = "submitted"
	VerificationEventClaimed   VerificationApplicationEventKind = "claimed"
	VerificationEventApproved  VerificationApplicationEventKind = "approved"
	VerificationEventRejected  VerificationApplicationEventKind = "rejected"
	VerificationEventCancelled VerificationApplicationEventKind = "cancelled"
	VerificationEventRevoked   VerificationApplicationEventKind = "revoked"
	VerificationEventNotified  VerificationApplicationEventKind = "notified"
)

// Valid reports whether the event kind is modelled.
func (k VerificationApplicationEventKind) Valid() bool {
	switch k {
	case VerificationEventCreated, VerificationEventUpdated, VerificationEventSubmitted,
		VerificationEventClaimed, VerificationEventApproved, VerificationEventRejected,
		VerificationEventCancelled, VerificationEventRevoked, VerificationEventNotified:
		return true
	default:
		return false
	}
}

// VerificationApplicationEvent is an append-only history row. Actor is the admin
// identity for review actions and empty for applicant-driven ones, which are
// attributed by ApplicantUserID on the application itself.
type VerificationApplicationEvent struct {
	ID            int64
	ApplicationID int64
	Kind          VerificationApplicationEventKind
	FromStatus    VerificationStatus
	ToStatus      VerificationStatus
	Actor         string
	Reason        string
	Note          string
	CorrelationID string
	CreatedAt     time.Time
}

// VerificationTarget is a candidate the applicant controls, as offered by the
// bot's target picker.
type VerificationTarget struct {
	Type       VerificationTargetType
	ID         int64
	Title      string
	Username   string
	AccessHash int64
	Verified   bool
	// Eligible is false when the target cannot be filed right now; Reason carries
	// the domain error for the bot to explain why.
	Eligible bool
	Reason   string
}

// VerificationDraftInput is the applicant-supplied payload collected by the bot.
type VerificationDraftInput struct {
	Category        string
	Description     string
	OfficialWebsite string
	SocialLinks     []string
	PressLinks      []string
	AdditionalNote  string
}

// Normalize trims the payload and drops empty links. It is applied before
// validation so a trailing newline from a chat message is not an error.
func (in VerificationDraftInput) Normalize() VerificationDraftInput {
	out := VerificationDraftInput{
		Category:        strings.TrimSpace(in.Category),
		Description:     strings.TrimSpace(in.Description),
		OfficialWebsite: strings.TrimSpace(in.OfficialWebsite),
		AdditionalNote:  strings.TrimSpace(in.AdditionalNote),
	}
	out.SocialLinks = normalizeVerificationLinks(in.SocialLinks)
	out.PressLinks = normalizeVerificationLinks(in.PressLinks)
	return out
}

func normalizeVerificationLinks(links []string) []string {
	out := make([]string, 0, len(links))
	seen := make(map[string]struct{}, len(links))
	for _, link := range links {
		link = strings.TrimSpace(link)
		if link == "" {
			continue
		}
		key := strings.ToLower(link)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, link)
	}
	return out
}

// ValidateDraft checks a partially filled draft: every present field must be
// well formed, but nothing is required yet. The bot uses it per step.
func (in VerificationDraftInput) ValidateDraft() error {
	in = in.Normalize()
	if in.Category != "" && !ValidVerificationCategory(in.Category) {
		return ErrVerificationApplicationInvalid
	}
	if utf8.RuneCountInString(in.Description) > MaxVerificationDescriptionLength {
		return ErrVerificationApplicationInvalid
	}
	if utf8.RuneCountInString(in.AdditionalNote) > MaxVerificationCommentLength {
		return ErrVerificationApplicationInvalid
	}
	if in.OfficialWebsite != "" {
		if err := ValidateVerificationURL(in.OfficialWebsite); err != nil {
			return err
		}
	}
	if len(in.SocialLinks) > MaxVerificationSocialLinks || len(in.PressLinks) > MaxVerificationPressLinks {
		return ErrVerificationApplicationInvalid
	}
	for _, link := range append(append([]string(nil), in.SocialLinks...), in.PressLinks...) {
		if err := ValidateVerificationURL(link); err != nil {
			return err
		}
	}
	return nil
}

// ValidateForSubmission checks a complete application. Everything the official
// process requires must be present: category, a real description, a website, and
// at least MinVerificationPressLinks independent press links.
func (in VerificationDraftInput) ValidateForSubmission() error {
	in = in.Normalize()
	if err := in.ValidateDraft(); err != nil {
		return err
	}
	if !ValidVerificationCategory(in.Category) {
		return ErrVerificationApplicationInvalid
	}
	if utf8.RuneCountInString(in.Description) < MinVerificationDescriptionLength {
		return ErrVerificationApplicationInvalid
	}
	if in.OfficialWebsite == "" {
		return ErrVerificationApplicationInvalid
	}
	if len(in.PressLinks) < MinVerificationPressLinks {
		return ErrVerificationApplicationInvalid
	}
	return nil
}

// ValidateVerificationURL accepts only a plain http(s) URL naming a public host.
//
// The server never fetches these links, and this check is what keeps that
// decision safe to revisit: credentials, non-web schemes, loopback, link-local,
// private and other reserved address space are all refused, so a link can never
// become an SSRF probe against the deployment's own network, and the admin panel
// only ever renders an absolute external URL.
func ValidateVerificationURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxVerificationURLLength {
		return ErrVerificationURLInvalid
	}
	if strings.ContainsAny(raw, " \t\r\n<>\"'\\") {
		return ErrVerificationURLInvalid
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ErrVerificationURLInvalid
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return ErrVerificationURLInvalid
	}
	if parsed.User != nil {
		// user:password@host would be rendered as a credential-bearing link.
		return ErrVerificationURLInvalid
	}
	host := parsed.Hostname()
	if host == "" {
		return ErrVerificationURLInvalid
	}
	if err := validateVerificationHost(host); err != nil {
		return err
	}
	if port := parsed.Port(); port != "" {
		switch port {
		case "80", "443":
		default:
			// A non-standard port on a "public site" link is a smell and would be
			// the first thing an SSRF attempt reaches for.
			return ErrVerificationURLInvalid
		}
	}
	return nil
}

func validateVerificationHost(host string) error {
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") ||
		strings.HasSuffix(lower, ".onion") {
		return ErrVerificationURLInvalid
	}
	if ip := net.ParseIP(host); ip != nil {
		if !publicVerificationIP(ip) {
			return ErrVerificationURLInvalid
		}
		return nil
	}
	// A registered name must look like a real domain: at least one dot, no
	// underscores, no trailing dot-only labels.
	if !strings.Contains(lower, ".") {
		return ErrVerificationURLInvalid
	}
	for _, label := range strings.Split(strings.TrimSuffix(lower, "."), ".") {
		if label == "" || len(label) > 63 {
			return ErrVerificationURLInvalid
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z':
			case c >= '0' && c <= '9':
			case c == '-' && i != 0 && i != len(label)-1:
			default:
				return ErrVerificationURLInvalid
			}
		}
	}
	return nil
}

// publicVerificationIP reports whether an IP literal is globally routable.
func publicVerificationIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsPrivate() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 100 && v4[1]&0xc0 == 64: // 100.64.0.0/10 CGNAT
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0.0/24
			return false
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 2: // TEST-NET-1
			return false
		case v4[0] == 198 && v4[1] == 51 && v4[2] == 100: // TEST-NET-2
			return false
		case v4[0] == 203 && v4[1] == 0 && v4[2] == 113: // TEST-NET-3
			return false
		case v4[0] == 198 && v4[1]&0xfe == 18: // 198.18.0.0/15 benchmarking
			return false
		case v4[0] >= 240: // 240.0.0.0/4 reserved, 255.255.255.255 broadcast
			return false
		}
		return true
	}
	// IPv6: refuse unique-local, documentation, and v4-mapped forms that would
	// smuggle a private v4 address past the checks above.
	if len(ip) == net.IPv6len {
		if ip[0]&0xfe == 0xfc { // fc00::/7
			return false
		}
		if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8 { // 2001:db8::/32
			return false
		}
	}
	return true
}

// SubmitVerificationApplicationRequest is the applicant-side submission.
type SubmitVerificationApplicationRequest struct {
	ApplicantUserID int64
	TargetType      VerificationTargetType
	TargetID        int64
	TargetTitle     string
	TargetUsername  string
	Draft           VerificationDraftInput
	CorrelationID   string
}

// Validate checks the request shape without touching the target state, which is
// the service's job.
func (r SubmitVerificationApplicationRequest) Validate() error {
	if r.ApplicantUserID <= 0 || r.TargetID <= 0 || !r.TargetType.Valid() {
		return ErrVerificationApplicationInvalid
	}
	if utf8.RuneCountInString(r.TargetTitle) > MaxVerificationTitleLength {
		return ErrVerificationApplicationInvalid
	}
	if len(r.CorrelationID) > MaxVerificationCorrelationLen {
		return ErrVerificationApplicationInvalid
	}
	return r.Draft.ValidateForSubmission()
}

// VerificationDecision is a reviewer's action on one application.
type VerificationDecision struct {
	ApplicationID int64
	// Version is the value the reviewer read; a mismatch is a concurrent decision.
	Version       int64
	Reviewer      string
	Reason        string
	InternalNote  string
	CorrelationID string
}

// Validate checks a decision that does not require a reason.
func (d VerificationDecision) Validate() error {
	if d.ApplicationID <= 0 || d.Version <= 0 {
		return ErrVerificationApplicationInvalid
	}
	if strings.TrimSpace(d.Reviewer) == "" || len(d.Reviewer) > MaxVerificationReviewerLength {
		return ErrVerificationApplicationInvalid
	}
	if utf8.RuneCountInString(d.Reason) > MaxVerificationReasonLength ||
		utf8.RuneCountInString(d.InternalNote) > MaxVerificationNoteLength {
		return ErrVerificationApplicationInvalid
	}
	if len(d.CorrelationID) > MaxVerificationCorrelationLen {
		return ErrVerificationApplicationInvalid
	}
	return nil
}

// ValidateWithReason checks a decision that must carry one: rejection and
// revocation are never recorded without a stated reason.
func (d VerificationDecision) ValidateWithReason() error {
	if err := d.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(d.Reason) == "" {
		return ErrVerificationReasonRequired
	}
	return nil
}

// VerificationApplicationFilter bounds a review-queue query.
type VerificationApplicationFilter struct {
	Statuses   []VerificationStatus
	TargetType VerificationTargetType
	Reviewer   string
	// Query matches an application id, a peer id or a username.
	Query     string
	CreatedAt time.Time
	Until     time.Time
	BeforeID  int64
	Limit     int
}

// VerificationStatusCounts is the queue summary shown above the list.
type VerificationStatusCounts map[VerificationStatus]int64

// VerificationRevocation removes the platform flag from a previously approved
// target. It is modelled separately from a rejection: the application stays
// approved as history, and the revocation is its own audit event.
type VerificationRevocation struct {
	TargetType    VerificationTargetType
	TargetID      int64
	Reviewer      string
	Reason        string
	InternalNote  string
	CorrelationID string
}

// Validate checks the revocation shape; a reason is mandatory.
func (r VerificationRevocation) Validate() error {
	if r.TargetID <= 0 || !r.TargetType.Valid() {
		return ErrVerificationTargetInvalid
	}
	if strings.TrimSpace(r.Reviewer) == "" || len(r.Reviewer) > MaxVerificationReviewerLength {
		return ErrVerificationApplicationInvalid
	}
	if strings.TrimSpace(r.Reason) == "" {
		return ErrVerificationReasonRequired
	}
	if utf8.RuneCountInString(r.Reason) > MaxVerificationReasonLength ||
		utf8.RuneCountInString(r.InternalNote) > MaxVerificationNoteLength {
		return ErrVerificationApplicationInvalid
	}
	return nil
}
