package domain

import "time"

// UserIDSequenceBase is the starting value of ordinary user IDs.
//
// It is the Unix second timestamp of 2026-06-01 00:00:00 Asia/Shanghai.
// Compatible system accounts such as 777000 are below this range, and business-registered users increment from here.
const UserIDSequenceBase int64 = 1780243200

// PeerColor is a domain-only representation of Telegram peerColor.
// HasColor preserves explicit color=0, which is distinct from "color unset".
type PeerColor struct {
	HasColor          bool
	Color             int
	BackgroundEmojiID int64
}

// EmojiStatusCollectible is the immutable projection needed to render a
// collectible gift as an emoji status.  The source of truth remains the owned
// UniqueStarGift; users store an immutable snapshot so every user projection,
// online update and offline difference observes the same shape without an
// RPC-layer lookup.
type EmojiStatusCollectible struct {
	CollectibleID     int64  `json:"collectible_id"`
	DocumentID        int64  `json:"document_id"`
	Title             string `json:"title"`
	Slug              string `json:"slug"`
	PatternDocumentID int64  `json:"pattern_document_id"`
	CenterColor       int    `json:"center_color"`
	EdgeColor         int    `json:"edge_color"`
	PatternColor      int    `json:"pattern_color"`
	TextColor         int    `json:"text_color"`
}

// Empty reports whether no collectible status is present.
func (s EmojiStatusCollectible) Empty() bool {
	return s == (EmojiStatusCollectible{})
}

// Valid enforces the complete collectible status shape.  Partial snapshots
// are forbidden because clients would otherwise render a gradient without its
// model/pattern or be unable to resolve the collectible link.
func (s EmojiStatusCollectible) Valid() bool {
	if s.CollectibleID <= 0 || s.DocumentID <= 0 || s.PatternDocumentID <= 0 ||
		s.Title == "" || s.Slug == "" {
		return false
	}
	for _, color := range []int{s.CenterColor, s.EdgeColor, s.PatternColor, s.TextColor} {
		if color < 0 || color > 0xffffff {
			return false
		}
	}
	return true
}

// UserEmojiStatus is the protocol-neutral mutation value accepted by the user
// service/store boundary.  Exactly one of a normal document or a complete
// collectible snapshot may be active; the zero value clears the status.
type UserEmojiStatus struct {
	DocumentID  int64                  `json:"document_id"`
	Until       int                    `json:"until,omitempty"`
	Collectible EmojiStatusCollectible `json:"collectible,omitempty"`
}

func (s UserEmojiStatus) Empty() bool {
	return s.DocumentID == 0 && s.Collectible.Empty()
}

func (s UserEmojiStatus) Valid() bool {
	if s.Until < 0 {
		return false
	}
	if s.Empty() {
		return s.Until == 0
	}
	if s.DocumentID <= 0 {
		return false
	}
	if s.Collectible.Empty() {
		return true
	}
	return s.Collectible.Valid() && s.DocumentID == s.Collectible.DocumentID
}

// Empty reports whether no explicit color/profile color state is set.
func (c PeerColor) Empty() bool {
	return !c.HasColor && c.BackgroundEmojiID == 0
}

// User is an account. Phase one keeps only the fields required by the login path;
// access_hash is required for any InputUser validation and cannot be omitted.
type User struct {
	ID          int64
	AccessHash  int64
	Phone       string
	FirstName   string
	LastName    string
	About       string
	Username    string
	CountryCode string
	Verified    bool
	Scam        bool
	Fake        bool
	Support     bool
	Contact     bool
	Mutual      bool
	CloseFriend bool
	// RestrictionReasons are transient, viewer-scoped unavailability reasons.
	// They are produced after loading the viewer-independent base user and must
	// never be persisted in users or the base-user cache.
	RestrictionReasons []UserRestrictionReason
	// ContactNote/ContactNoteEntities are transient viewer-scoped contact
	// projection fields. They must never be persisted into users or a
	// viewer-independent base-user cache.
	ContactNote         string
	ContactNoteEntities []MessageEntity
	// Bot marks a bot account; when set, BotInfoVersion must be ≥1 (TDesktop only recognizes
	// whether the user TL carries the bot_info_version field, which shares bit14 with the bot flag).
	Bot            bool
	BotInfoVersion int
	// PremiumUntil is the Premium expiry in Unix seconds; 0 means non-member. This field is the single authoritative
	// source of Premium state, derived on the fly by the read path via PremiumActiveAt — after expiry, Premium is
	// no longer sent without waiting for the background sweeper to flip it (the sweeper only handles cleanup and notification pushes).
	PremiumUntil int
	// EmojiStatusDocumentID / EmojiStatusUntil are the user's custom emoji status
	// (Premium-only, account.updateEmojiStatus). DocumentID==0 means unset;
	// Until==0 means permanent. When EmojiStatusCollectible is non-zero, DocumentID must equal
	// the collectible's model document id.
	EmojiStatusDocumentID  int64
	EmojiStatusUntil       int
	EmojiStatusCollectible EmojiStatusCollectible
	// Birthday is the user's public birthday (account.updateBirthday). The zero value means unset.
	Birthday Birthday
	// PersonalChannelID is the "personal channel" shown on the profile page (account.updatePersonalChannel);
	// 0 means unset. Profile projection uses it to fetch the channel object and its latest post.
	PersonalChannelID int64
	// LinkedCommunityID is the single Community containing this bot. Ordinary
	// users must keep it zero; the community aggregate enforces that invariant.
	LinkedCommunityID int64
	Color             PeerColor
	ProfileColor      PeerColor
	// Profile photo fields are filled by app-layer user projection. PhotoID==0 means no avatar.
	PhotoID       int64
	PhotoDCID     int
	PhotoStripped []byte
	PhotoPersonal bool
	PhotoHasVideo bool
	LastSeenAt    int
	Status        UserStatus
	// Deleted is the durable tombstone state. Deleted users remain addressable by
	// ID so historical messages can render "Deleted Account", but all profile
	// and reusable identity fields are cleared at the store boundary.
	Deleted         bool
	DeletedAt       int64
	DeletionSource  AccountDeletionSource
	DeletionReason  string
	CreatedAt       time.Time
	AccountDeleteAt time.Time
	// FrozenForViewer is a transient, viewer-scoped marker: the account is served
	// to this viewer as a deleted tombstone because it is frozen, not because it
	// was deleted. It must never be persisted into users or a viewer-independent
	// base-user cache; the RPC boundary uses it to attach the synthetic
	// "frozen" third-party mark alongside the deleted presentation.
	FrozenForViewer bool
}

// UserRestrictionReason is the protocol-neutral form of Telegram's
// restrictionReason. Platform "all" applies to TDesktop and official mobile
// clients; Text is intentionally server supplied and directly user-visible.
type UserRestrictionReason struct {
	Platform string
	Reason   string
	Text     string
}

func AccountFrozenRestrictionReasons() []UserRestrictionReason {
	return []UserRestrictionReason{{
		Platform: "all",
		Reason:   "frozen",
		Text:     "This account is frozen.",
	}}
}

// PremiumActiveAt reports whether the user is an active Premium member at time now (Unix seconds).
// A bot is never a Premium member (official semantics; the grant path also excludes bots, this is belt and suspenders).
func (u User) PremiumActiveAt(now int64) bool {
	return !u.Bot && u.PremiumUntil > 0 && int64(u.PremiumUntil) > now
}

// EmojiStatusActiveAt reports whether the user has an active emoji status at time now
// (Unix seconds) (set and unexpired; Until==0 means permanent). Emoji status is Premium-only, and after
// a downgrade on expiry it is no longer sent even if the column still holds a residual value.
func (u User) EmojiStatusActiveAt(now int64) bool {
	if !u.PremiumActiveAt(now) || !u.EmojiStatus().Valid() || u.EmojiStatusDocumentID == 0 {
		return false
	}
	return u.EmojiStatusUntil == 0 || int64(u.EmojiStatusUntil) > now
}

// EmojiStatus returns the complete status snapshot carried by this user.
func (u User) EmojiStatus() UserEmojiStatus {
	return UserEmojiStatus{
		DocumentID:  u.EmojiStatusDocumentID,
		Until:       u.EmojiStatusUntil,
		Collectible: u.EmojiStatusCollectible,
	}
}

// DeletedTombstone strips every viewer-dependent or personally identifying
// field while preserving the immutable id and lifecycle audit facts.
func (u User) DeletedTombstone() User {
	if !u.Deleted {
		return u
	}
	return User{
		ID:              u.ID,
		AccessHash:      u.AccessHash,
		Deleted:         true,
		DeletedAt:       u.DeletedAt,
		DeletionSource:  u.DeletionSource,
		DeletionReason:  u.DeletionReason,
		CreatedAt:       u.CreatedAt,
		AccountDeleteAt: u.AccountDeleteAt,
		Status:          UserStatus{Kind: UserStatusEmpty},
	}
}

// UserStatusKind is a protocol-neutral account presence state.
type UserStatusKind int

const (
	UserStatusUnknown UserStatusKind = iota
	UserStatusOnline
	UserStatusOffline
	UserStatusRecently
	UserStatusLastWeek
	UserStatusLastMonth
	UserStatusEmpty
)

// UserStatus describes the currently visible presence state for a user.
//
// Expires and WasOnline are absolute Unix timestamps in seconds, matching
// Telegram's UserStatus semantics without leaking tg.* into domain.
type UserStatus struct {
	Kind      UserStatusKind
	Expires   int
	WasOnline int
}

// ApproximateUserStatus returns Telegram's coarse privacy-preserving last-seen
// buckets. Exact online/offline timestamps must never be reattached after this
// projection.
func ApproximateUserStatus(lastSeenAt, now int) UserStatus {
	if lastSeenAt <= 0 || now <= 0 || lastSeenAt >= now {
		return UserStatus{Kind: UserStatusRecently}
	}
	age := now - lastSeenAt
	switch {
	case age <= 3*24*60*60:
		return UserStatus{Kind: UserStatusRecently}
	case age <= 7*24*60*60:
		return UserStatus{Kind: UserStatusLastWeek}
	case age <= 30*24*60*60:
		return UserStatus{Kind: UserStatusLastMonth}
	default:
		return UserStatus{Kind: UserStatusEmpty}
	}
}

// Birthday is the user's public birthday. Day/Month 0 means unset; Year 0 means only month/day were given without a year.
type Birthday struct {
	Day   int
	Month int
	Year  int
}

// IsSet reports whether the birthday is set (a valid month/day is required).
func (b Birthday) IsSet() bool {
	return b.Day != 0 && b.Month != 0
}

// ValidBirthday validates whether the month/day (year optional) is within legal range. Pass the zero value to clear the birthday (IsSet is false).
func ValidBirthday(b Birthday) bool {
	if b.Month < 1 || b.Month > 12 || b.Day < 1 || b.Day > 31 {
		return false
	}
	if b.Year != 0 && (b.Year < 1900 || b.Year > 2100) {
		return false
	}
	return true
}

// UserProfileUpdate describes the optional field updates of account.updateProfile.
type UserProfileUpdate struct {
	FirstName    string
	HasFirstName bool
	LastName     string
	HasLastName  bool
	About        string
	HasAbout     bool
}

// UserFullView is the app-layer personalized full user view consumed by RPC.
type UserFullView struct {
	User                   User
	ProfilePhoto           *Photo
	PersonalPhoto          *Photo
	FallbackPhoto          *Photo
	About                  string
	PhoneCallsAvailable    bool
	PhoneCallsPrivate      bool
	VideoCallsAvailable    bool
	VoiceMessagesForbidden bool
	ReadDatesPrivate       bool
}
