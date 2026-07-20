package domain

import (
	"crypto/sha256"
	"errors"
	"time"
	"unicode/utf8"
)

const (
	// EphemeralMessageRetention matches TDesktop's in-memory upper bound. The
	// server never replays these records; the retention only keeps callback,
	// edit, delete and abuse-report lookups coherent across instances.
	EphemeralMessageRetention = 48 * time.Hour
	// EphemeralReplyWindow is the official Bot API eligible-action window.
	EphemeralReplyWindow = 15 * time.Second
	// MaxEphemeralCreateAttempts bounds random int32 ID collision retries.
	MaxEphemeralCreateAttempts = 8
	// MaxEphemeralCallbackDataBytes is the Bot API callback_data wire limit.
	MaxEphemeralCallbackDataBytes = 64
	// MaxEphemeralCaptionLength follows the Bot API media-caption contract.
	MaxEphemeralCaptionLength = 1024
	// Rich messages are accepted at the domain boundary only within a bounded
	// wire-sized snapshot. The current official client does not send this flag,
	// but malformed callers must not be able to retain unbounded block vectors.
	MaxEphemeralRichBlocksBytes = 1 << 20
	MaxEphemeralRichMediaRefs   = 100
)

var (
	ErrEphemeralInvalid          = errors.New("ephemeral message invalid")
	ErrEphemeralNotFound         = errors.New("ephemeral message not found")
	ErrEphemeralExpired          = errors.New("ephemeral message expired")
	ErrEphemeralDeleted          = errors.New("ephemeral message deleted")
	ErrEphemeralIDCollision      = errors.New("ephemeral message id collision")
	ErrEphemeralRandomIDConflict = errors.New("ephemeral random id conflict")
	ErrEphemeralVersionConflict  = errors.New("ephemeral message version conflict")
	ErrEphemeralReplyExpired     = errors.New("ephemeral reply expired")
	ErrEphemeralQueryInvalid     = errors.New("ephemeral query invalid")
	ErrEphemeralPeerInvalid      = errors.New("ephemeral peer invalid")
	ErrEphemeralSenderInvalid    = errors.New("ephemeral sender invalid")
	ErrEphemeralReceiverInvalid  = errors.New("ephemeral receiver invalid")
	ErrEphemeralCommandInvalid   = errors.New("ephemeral command invalid")
	ErrEphemeralForbidden        = errors.New("ephemeral action forbidden")
	ErrEphemeralDeviceMismatch   = errors.New("ephemeral device mismatch")
	ErrEphemeralCallbackInvalid  = errors.New("ephemeral callback invalid")
)

// EphemeralDevice identifies the exact client application that originated an
// eligible action. BusinessAuthKeyID is the durable device identity; SessionID
// is retained for binding checks and diagnostics, not used as a global key.
type EphemeralDevice struct {
	UserID            int64
	BusinessAuthKeyID [8]byte
	SessionID         int64
}

// EphemeralContent is the mutable presentation payload. Identity, routing and
// reply ancestry live on EphemeralMessage and never change during edits.
type EphemeralContent struct {
	Message     string
	Entities    []MessageEntity
	Media       *MessageMedia
	ReplyMarkup *MessageReplyMarkup
	RichMessage *MessageRichMessage
}

// EphemeralMessage is a short-lived bot/member interaction. It deliberately
// has no ordinary message box ID, pts, qts, seq, unread or dialog fields.
type EphemeralMessage struct {
	ID                 int
	Peer               Peer
	SenderUserID       int64
	ReceiverUserID     int64
	Date               int
	EditDate           int
	RandomID           int64
	TopMessageID       int
	ReplyToEphemeralID int
	Content            EphemeralContent
	OriginDevice       EphemeralDevice
	PayloadHash        [32]byte
	Version            uint64
	Deleted            bool
	CreatedAt          time.Time
	ExpiresAt          time.Time
	// BotAPIReply is a one-level, runtime-only reply snapshot. It is attached
	// after the authoritative message has been written, excluded from Redis and
	// broker JSON, and used only to project a valid Bot API reply_to_message.
	BotAPIReply *EphemeralMessage `json:"-"`
}

type SendClientEphemeralRequest struct {
	SenderUserID       int64
	ReceiverBotID      int64
	Peer               Peer
	QueryID            int64
	RandomID           int64
	TopMessageID       int
	ReplyToEphemeralID int
	Content            EphemeralContent
	OriginDevice       EphemeralDevice
}

type SendBotEphemeralRequest struct {
	BotUserID          int64
	ReceiverUserID     int64
	Peer               Peer
	RandomID           int64
	TopMessageID       int
	ReplyToEphemeralID int
	Content            EphemeralContent
	// ActionMessageID authorizes the ordinary 15-second response path. When it
	// is zero the bot must be an administrator and delivery targets every ready
	// Layer 228 device of ReceiverUserID.
	ActionMessageID int
	// CallbackQueryID authorizes a response to a callback originating from a
	// bot→user ephemeral message. The shared action record owns the target device.
	CallbackQueryID int64
}

type EphemeralCallback struct {
	Message    EphemeralMessage
	BotUserID  int64
	UserID     int64
	Peer       Peer
	Data       []byte
	Device     EphemeralDevice
	OccurredAt time.Time
}

type EphemeralCallbackAction struct {
	QueryID      int64
	BotUserID    int64
	UserID       int64
	Peer         Peer
	MessageID    int
	TopMessageID int
	Device       EphemeralDevice
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// EphemeralReportEvidence is the durable, device-identity-free snapshot kept
// for abuse review after the transient Redis record expires. It intentionally
// excludes OriginDevice, random IDs and session/auth-key identifiers.
type EphemeralReportEvidence struct {
	MessageID          int
	Peer               Peer
	SenderUserID       int64
	ReceiverUserID     int64
	Date               int
	EditDate           int
	TopMessageID       int
	ReplyToEphemeralID int
	Content            EphemeralContent
	PayloadHash        [32]byte
	Version            uint64
}

// EphemeralAbuseReport is written only for a final report option. CommentHash
// makes retries idempotent without indexing potentially large user text.
type EphemeralAbuseReport struct {
	ReporterUserID int64
	Option         string
	Comment        string
	CommentHash    [32]byte
	Evidence       EphemeralReportEvidence
	CreatedAt      time.Time
}

func NewEphemeralAbuseReport(reporterUserID int64, option, comment string, message EphemeralMessage, createdAt time.Time) EphemeralAbuseReport {
	return EphemeralAbuseReport{
		ReporterUserID: reporterUserID,
		Option:         option,
		Comment:        comment,
		CommentHash:    sha256.Sum256([]byte(comment)),
		Evidence: EphemeralReportEvidence{
			MessageID: message.ID, Peer: message.Peer,
			SenderUserID: message.SenderUserID, ReceiverUserID: message.ReceiverUserID,
			Date: message.Date, EditDate: message.EditDate,
			TopMessageID: message.TopMessageID, ReplyToEphemeralID: message.ReplyToEphemeralID,
			Content: message.Content, PayloadHash: message.PayloadHash, Version: message.Version,
		},
		CreatedAt: createdAt,
	}
}

func (r EphemeralAbuseReport) Validate() error {
	if r.ReporterUserID <= 0 || r.Option == "" || len(r.Option) > 64 || utf8.RuneCountInString(r.Comment) > 4096 ||
		r.Evidence.MessageID <= 0 || r.Evidence.MessageID > MaxMessageBoxID ||
		r.Evidence.Peer.Type != PeerTypeChannel || r.Evidence.Peer.ID <= 0 ||
		r.Evidence.SenderUserID <= 0 || r.Evidence.ReceiverUserID != r.ReporterUserID ||
		r.Evidence.SenderUserID == r.Evidence.ReceiverUserID || r.CreatedAt.IsZero() ||
		r.CommentHash != sha256.Sum256([]byte(r.Comment)) {
		return ErrEphemeralInvalid
	}
	return nil
}

type EditEphemeralFields struct {
	SetMessage     bool
	Message        string
	Entities       []MessageEntity
	SetMedia       bool
	Media          *MessageMedia
	SetReplyMarkup bool
	ReplyMarkup    *MessageReplyMarkup
}

type BotAPIFileInput struct {
	LocationKey string
	RemoteURL   string
	FileName    string
	MimeType    string
	Bytes       []byte
	Width       int
	Height      int
	Duration    int
	Title       string
	Performer   string
	Emoji       string
}

type BotAPIEphemeralSendInput struct {
	BotUserID          int64
	ChatID             int64
	ReceiverUserID     int64
	CallbackQueryID    int64
	ReplyToEphemeralID int
	TopMessageID       int
	Kind               string
	Text               string
	Entities           []MessageEntity
	ReplyMarkup        *MessageReplyMarkup
	File               BotAPIFileInput
	SecondaryFile      BotAPIFileInput
	DirectMedia        *MessageMedia
}

type BotAPIEphemeralEditInput struct {
	BotUserID      int64
	ChatID         int64
	ReceiverUserID int64
	MessageID      int
	Mode           EphemeralEditMode
	Fields         EditEphemeralFields
	MediaKind      string
	File           BotAPIFileInput
	SecondaryFile  BotAPIFileInput
}

type EphemeralEditMode string

const (
	EphemeralEditText        EphemeralEditMode = "text"
	EphemeralEditMedia       EphemeralEditMode = "media"
	EphemeralEditCaption     EphemeralEditMode = "caption"
	EphemeralEditReplyMarkup EphemeralEditMode = "reply_markup"
)

func (m EphemeralMessage) ValidateForCreate(now time.Time) error {
	if err := m.ValidateStored(); err != nil || m.Version != 1 || m.Deleted || !m.ExpiresAt.After(now) {
		return ErrEphemeralInvalid
	}
	return nil
}

func (m EphemeralMessage) ValidateStored() error {
	if m.ID <= 0 || m.ID > MaxMessageBoxID || m.Peer.Type != PeerTypeChannel || m.Peer.ID <= 0 ||
		m.SenderUserID <= 0 || m.ReceiverUserID <= 0 || m.SenderUserID == m.ReceiverUserID ||
		m.RandomID == 0 || m.Date <= 0 || m.Version == 0 || m.CreatedAt.IsZero() || m.ExpiresAt.IsZero() ||
		!m.ExpiresAt.After(m.CreatedAt) || m.ExpiresAt.Sub(m.CreatedAt) > EphemeralMessageRetention ||
		m.Date != int(m.CreatedAt.Unix()) || (m.EditDate != 0 && m.EditDate < m.Date) ||
		m.TopMessageID < 0 || m.TopMessageID > MaxMessageBoxID ||
		m.ReplyToEphemeralID < 0 || m.ReplyToEphemeralID > MaxMessageBoxID || m.ReplyToEphemeralID == m.ID ||
		m.PayloadHash == ([32]byte{}) {
		return ErrEphemeralInvalid
	}
	zeroDevice := m.OriginDevice == (EphemeralDevice{})
	if !zeroDevice && (m.OriginDevice.UserID <= 0 || m.OriginDevice.BusinessAuthKeyID == ([8]byte{}) ||
		m.OriginDevice.SessionID == 0 ||
		(m.OriginDevice.UserID != m.SenderUserID && m.OriginDevice.UserID != m.ReceiverUserID)) {
		return ErrEphemeralInvalid
	}
	if m.Deleted {
		if m.Version < 2 || m.Content.Message != "" || len(m.Content.Entities) != 0 || m.Content.Media != nil ||
			m.Content.ReplyMarkup != nil || !m.Content.RichMessage.IsZero() {
			return ErrEphemeralInvalid
		}
		return nil
	}
	return ValidateEphemeralContent(m.Content)
}

func ValidateEphemeralContent(content EphemeralContent) error {
	if !utf8.ValidString(content.Message) || utf8.RuneCountInString(content.Message) > MaxMessageTextLength ||
		len(content.Entities) > MaxMessageEntityCount || !validEphemeralEntityBounds(content.Message, content.Entities) {
		return ErrEphemeralInvalid
	}
	if err := ValidateReplyMarkup(content.ReplyMarkup); err != nil {
		return ErrEphemeralInvalid
	}
	if content.ReplyMarkup != nil && !content.ReplyMarkup.IsZero() && content.ReplyMarkup.Kind() != MessageReplyMarkupInline {
		return ErrEphemeralInvalid
	}
	if content.Media != nil && !validEphemeralMedia(content.Media) {
		return ErrEphemeralInvalid
	}
	if rich := content.RichMessage; !rich.IsZero() {
		if len(rich.Blocks) == 0 || len(rich.Blocks) > MaxEphemeralRichBlocksBytes ||
			len(rich.Photos) > MaxEphemeralRichMediaRefs || len(rich.Documents) > MaxEphemeralRichMediaRefs {
			return ErrEphemeralInvalid
		}
	}
	if content.Message == "" && content.Media == nil && content.RichMessage.IsZero() {
		return ErrEphemeralInvalid
	}
	return nil
}

func validEphemeralEntityBounds(message string, entities []MessageEntity) bool {
	utf16Length := 0
	for _, value := range message {
		utf16Length++
		if value > 0xffff {
			utf16Length++
		}
	}
	for _, entity := range entities {
		if entity.Type == "" || entity.Offset < 0 || entity.Length <= 0 || entity.Offset > utf16Length ||
			entity.Length > utf16Length-entity.Offset {
			return false
		}
	}
	return true
}

func validEphemeralMedia(media *MessageMedia) bool {
	if media == nil || media.IsZero() || media.ServiceAction != nil || media.Dice != nil || media.Poll != nil ||
		media.GeoLive != nil || media.Todo != nil || media.Story != nil || media.WebPage != nil {
		return false
	}
	switch media.Kind {
	case MessageMediaKindPhoto:
		return media.Photo != nil && media.Document == nil && media.Contact == nil && media.Geo == nil && media.Venue == nil
	case MessageMediaKindDocument:
		return media.Document != nil && media.Photo == nil && media.LivePhotoVideo == nil && media.Contact == nil && media.Geo == nil && media.Venue == nil
	case MessageMediaKindContact:
		return media.Contact != nil && media.Photo == nil && media.LivePhotoVideo == nil && media.Document == nil && media.Geo == nil && media.Venue == nil
	case MessageMediaKindGeo:
		return media.Geo != nil && media.Photo == nil && media.LivePhotoVideo == nil && media.Document == nil && media.Contact == nil && media.Venue == nil
	case MessageMediaKindVenue:
		return media.Venue != nil && media.Photo == nil && media.LivePhotoVideo == nil && media.Document == nil && media.Contact == nil && media.Geo == nil
	default:
		return false
	}
}

func (m EphemeralMessage) Expired(now time.Time) bool {
	return !m.ExpiresAt.IsZero() && !now.Before(m.ExpiresAt)
}
