package domain

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxBotsPerOwner is the per-user bot creation limit (aligned with the official appConfig bots_create_limit_default).
	MaxBotsPerOwner = 20
	// BotTokenSecretLength is the random-part length of a bot token (official format <bot_id>:<35 digits>).
	BotTokenSecretLength = 35
	// BotUsernameSuffix is the mandatory suffix of a bot username (case-insensitive).
	BotUsernameSuffix = "bot"
	// MaxBotNameLength is the bot display name (first_name) limit.
	MaxBotNameLength = 64
	// MaxBotInlinePlaceholderLen is the inline mode placeholder limit (bots.inline_placeholder).
	MaxBotInlinePlaceholderLen = 128
	// MaxBotAppShortNameLen is the bot app short_name limit.
	MaxBotAppShortNameLen = 64
	// MaxBotAppTitleLen is the bot app title limit.
	MaxBotAppTitleLen = 128
	// MaxBotAppDescriptionLen is the bot app description limit.
	MaxBotAppDescriptionLen = 512
	// MaxBotAppURLLen is the bot app / attach menu webview URL limit.
	MaxBotAppURLLen = 512
	// MaxBotAttachMenuPeerTypes limits the attach menu peer_types count to avoid an unbounded response.
	MaxBotAttachMenuPeerTypes = 8
	// MaxBotAttachMenuIcons limits the attach menu icon variant count.
	MaxBotAttachMenuIcons = 8
	// MaxBotPreviewMedia is the per-bot main mini app preview media limit.
	MaxBotPreviewMedia = 20
	// MaxBotRequestedPeerQuantity limits how many peers a single request-peer selection may include.
	MaxBotRequestedPeerQuantity = 10
	// MaxBotCustomMethodLen limits the WebView custom method name length.
	MaxBotCustomMethodLen = 128
	// MaxBotCustomMethodPayloadLen limits the WebView custom method JSON payload length.
	MaxBotCustomMethodPayloadLen = 4096
)

// Bot business errors.
var (
	ErrBotTokenInvalid    = errors.New("bot token invalid")
	ErrBotsTooMany        = errors.New("bot create limit exceeded")
	ErrBotNameInvalid     = errors.New("bot name invalid")
	ErrBotUsernameInvalid = errors.New("bot username invalid")
	ErrBotNotFound        = errors.New("bot not found")
	// ErrBotCommandInvalid means the command name/description is invalid (bots.setBotCommands).
	ErrBotCommandInvalid = errors.New("bot command invalid")
	// ErrBotInfoInvalid means setBotInfo's name/about/description is out of bounds.
	ErrBotInfoInvalid = errors.New("bot info invalid")
	// ErrBotMenuButtonInvalid means the menu button text/URL is invalid.
	ErrBotMenuButtonInvalid = errors.New("bot menu button invalid")
	// ErrBotInlinePlaceholderInvalid means the inline placeholder text is out of bounds for anything other than empty.
	ErrBotInlinePlaceholderInvalid = errors.New("bot inline placeholder invalid")
	// ErrBotAppInvalid means the bot app metadata, URL or access hash is invalid.
	ErrBotAppInvalid = errors.New("bot app invalid")
	// ErrBotAppShortNameInvalid means the bot app short_name is invalid.
	ErrBotAppShortNameInvalid = errors.New("bot app short name invalid")
	// ErrBotAttachMenuInvalid means the attach menu catalog or user state is invalid.
	ErrBotAttachMenuInvalid = errors.New("bot attach menu invalid")
	// ErrBotRequestedButtonInvalid means the request-peer button context is invalid or expired.
	ErrBotRequestedButtonInvalid = errors.New("bot requested button invalid")
	// ErrBotDownloadParamsInvalid means the mini app download parameters failed the security policy.
	ErrBotDownloadParamsInvalid = errors.New("bot download params invalid")
	// ErrBotCustomMethodUnavailable means there is no bot-side answer that can complete the WebView custom method.
	ErrBotCustomMethodUnavailable = errors.New("bot custom method unavailable")
	// ErrBotSessionsNotRevoked means the token was rotated but revoking the logged-in sessions failed
	// (the token cannot be rolled back, so the caller must tell the user to retry to ensure the old sessions are terminated).
	ErrBotSessionsNotRevoked = errors.New("bot sessions not revoked")
	// ErrBotDeletionUnsupported means the current store does not implement account deletion (only Postgres does),
	// so the caller must honestly tell the user this server does not support it, rather than claim it was deleted.
	ErrBotDeletionUnsupported = errors.New("bot deletion unsupported")
)

const (
	// MaxBotCommands is the per-bot command limit (default scope).
	MaxBotCommands = 100
	// MaxBotCommandLen is the command name length limit (excluding the leading /).
	MaxBotCommandLen = 32
	// MaxBotCommandDescriptionLen is the command description length limit.
	MaxBotCommandDescriptionLen = 256
	// MaxBotAboutLen is the bot about (users.about) length limit.
	MaxBotAboutLen = 120
	// MaxBotDescriptionLen is the bot description ("What can this bot do?") length limit.
	MaxBotDescriptionLen = 512
	// MaxBotMenuButtonTextLen / MaxBotMenuButtonURLLen are the menu button text/URL limits.
	MaxBotMenuButtonTextLen = 64
	MaxBotMenuButtonURLLen  = 512
)

// BotCommand is one command of a bot (a botInfo.commands element).
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
	Ephemeral   bool   `json:"ephemeral,omitempty"`
}

// BotMenuButtonType identifies the menu button type.
type BotMenuButtonType int

const (
	// BotMenuButtonDefault is the default (no special button shown; the client decides the '/' round button by whether commands is non-empty).
	BotMenuButtonDefault BotMenuButtonType = 0
	// BotMenuButtonCommands explicitly makes the client show the command menu button.
	BotMenuButtonCommands BotMenuButtonType = 1
	// BotMenuButtonWebView is a web view button with text + URL.
	BotMenuButtonWebView BotMenuButtonType = 2
)

// BotMenuButton is a bot's menu button (global per bot; the per-user dimension is P2 and not done).
type BotMenuButton struct {
	Type BotMenuButtonType
	Text string
	URL  string
}

// BotInfoUpdate is a partial update of bots.setBotInfo (name→users.first_name,
// about→users.about, description→bots.description). When a SetXxx is false the field is left untouched.
type BotInfoUpdate struct {
	SetName        bool
	Name           string
	SetAbout       bool
	About          string
	SetDescription bool
	Description    string
}

// BotApp is one item of the mini app catalog. A bot can own multiple apps; ID/access_hash
// are stable after creation, and short_name is unique within the same bot.
type BotApp struct {
	ID                 int64
	BotUserID          int64
	ShortName          string
	Title              string
	Description        string
	URL                string
	PhotoID            int64
	DocumentID         int64
	AccessHash         int64
	Hash               int64
	Inactive           bool
	RequestWriteAccess bool
	HasSettings        bool
	Main               bool
}

// BotAppSettings is the protocol-neutral representation of BotInfo.app_settings.
type BotAppSettings struct {
	PlaceholderPath     []byte
	BackgroundColor     int
	BackgroundDarkColor int
	HeaderColor         int
	HeaderDarkColor     int
	HasBackgroundColor  bool
	HasBackgroundDark   bool
	HasHeaderColor      bool
	HasHeaderDarkColor  bool
}

// BotAppPreviewMedia is the persisted snapshot of main mini app preview media.
type BotAppPreviewMedia struct {
	ID         int64
	BotUserID  int64
	AppID      int64
	Position   int
	PhotoID    int64
	DocumentID int64
}

// BotAttachMenuIconColor describes a theme-color override for an attach menu icon.
type BotAttachMenuIconColor struct {
	Name  string
	Color int
}

// BotAttachMenuIcon describes a platform icon document reference for the attach menu.
type BotAttachMenuIcon struct {
	Name       string
	DocumentID int64
	Colors     []BotAttachMenuIconColor
}

// BotAttachMenuBot is one item of the global attach/side menu catalog.
type BotAttachMenuBot struct {
	BotUserID                int64
	AppID                    int64
	ShortName                string
	Inactive                 bool
	HasSettings              bool
	RequestWriteAccess       bool
	ShowInAttachMenu         bool
	ShowInSideMenu           bool
	SideMenuDisclaimerNeeded bool
	PeerTypes                []string
	Icons                    []BotAttachMenuIcon
}

// BotAttachMenuState is a user's enable and write-permission state for an attach menu bot.
type BotAttachMenuState struct {
	UserID       int64
	BotUserID    int64
	Enabled      bool
	WriteAllowed bool
}

// BotRequestedWebViewButton is the request-peer context created by bots.requestWebViewButton.
type BotRequestedWebViewButton struct {
	WebAppReqID       string
	BotUserID         int64
	UserID            int64
	ButtonID          int
	Text              string
	PeerType          string
	MaxQuantity       int
	PeerFilter        *BotRequestPeerFilter
	NameRequested     bool
	UsernameRequested bool
	PhotoRequested    bool
	CreatedAt         time.Time
	ExpiresAt         time.Time
}

// BotWebViewCustomMethodQuery is the pending record of a custom method. When there is no bot-side answer
// registry it only records the query and explicitly returns a blocked error, avoiding a false success.
type BotWebViewCustomMethodQuery struct {
	ID           string
	BotUserID    int64
	UserID       int64
	CustomMethod string
	ParamsJSON   string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// ValidBotCommandName validates a command name: 1-32 lowercase letters/digits/underscores (excluding the leading /).
func ValidBotCommandName(cmd string) bool {
	if len(cmd) == 0 || len(cmd) > MaxBotCommandLen {
		return false
	}
	for _, r := range cmd {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// BotProfile is one bots table row: the bot account's metadata and token.
// The display name/username/about reuse the users row and are not duplicated here.
// BotInvoice is one XTR invoice a bot sent as a chat message.
//
// payments.sendPaymentForm carries no purpose in this layer, so the price is
// taken from this record: the client only sends the message id and the server
// resolves the amount it itself advertised. That is what makes a bot invoice
// settleable without any extra TL constructor.
//
// ChargeID is the telegram_payment_charge_id the client reports, kept so
// refundStarPayment can find the invoice again.
type BotInvoice struct {
	ID          int64
	BotUserID   int64
	ChatID      int64
	MessageID   int
	Title       string
	Description string
	// Amount is in whole Stars. currency is always XTR: telesrv does not model
	// a fiat checkout, so there is nothing else an invoice could settle.
	Amount   int64
	Currency string
	// Payload is the Bot API start_param, opaque to the server and echoed back
	// to the bot so it can tell what was bought.
	Payload  string
	ChargeID string
	PayerID  int64
	Paid     bool
	PaidAt   int
	Date     int
	Refunded bool
}

// Bot invoice failures.
var (
	ErrBotInvoiceInvalid  = errors.New("bot invoice invalid")
	ErrBotInvoiceNotFound = errors.New("bot invoice not found")
	// ErrBotInvoiceSettled means the invoice is already paid, so paying it
	// again would charge the buyer twice.
	ErrBotInvoiceSettled = errors.New("bot invoice already settled")
	// ErrBotInvoiceRefunded means the charge was already reversed.
	ErrBotInvoiceRefunded = errors.New("bot invoice already refunded")
)

// Invoice is the messageMediaInvoice payload a bot advertises.
//
// It is deliberately separate from PremiumInvoice: that type carries a Premium
// plan (months, plan version, entitlement message) and its Valid() rejects a
// plain product, so reusing it for a bot sale would mean filling in fields that
// mean nothing here. The Premium bot keeps its own type.
type Invoice struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	AmountStars int64  `json:"amount_stars"`
	StartParam  string `json:"start_param,omitempty"`
}

// Valid reports whether the invoice can be projected onto messageMediaInvoice.
func (i Invoice) Valid() bool {
	return utf8.RuneCountInString(i.Title) > 0 &&
		utf8.RuneCountInString(i.Title) <= BotInvoiceMaxTitle &&
		utf8.RuneCountInString(i.Description) > 0 &&
		utf8.RuneCountInString(i.Description) <= BotInvoiceMaxDescription &&
		i.AmountStars > 0 && len(i.StartParam) <= BotInvoiceMaxPayload
}

// BotInvoiceMaxTitle is the messageMediaInvoice title budget.
const BotInvoiceMaxTitle = 32

// BotInvoiceMaxDescription is the messageMediaInvoice description budget.
const BotInvoiceMaxDescription = 255

// BotInvoiceMaxPayload is the start_param budget Telegram allows.
const BotInvoiceMaxPayload = 64

// GramsrvBotUserID is the built-in @gramsrv moderation bot account used by
// WithGramsrvAdmin. Zero means the bot is not provisioned on this server.
const GramsrvBotUserID = int64(0)

// Valid reports whether the invoice describes a well-formed XTR sale.
func (i BotInvoice) Valid() bool {
	return i.BotUserID > 0 && i.ChatID != 0 && i.MessageID > 0 && i.Amount > 0 &&
		i.Currency == PremiumCurrencyStars &&
		utf8.RuneCountInString(i.Title) <= BotInvoiceMaxTitle &&
		utf8.RuneCountInString(i.Description) <= BotInvoiceMaxDescription &&
		len(i.Payload) <= BotInvoiceMaxPayload
}

// Settled reports whether the invoice has already been paid, which makes a
// replayed sendPaymentForm a duplicate rather than a second charge.
func (i BotInvoice) Settled() bool { return i.Paid && i.ChargeID != "" }

type BotProfile struct {
	BotUserID         int64
	OwnerUserID       int64
	TokenSecret       string
	Description       string
	Commands          []BotCommand
	ChatHistory       bool // privacy mode off = true (bot_chat_history flag)
	Nochats           bool // true = groups not allowed (bot_nochats flag)
	InlineGeo         bool
	InlinePlaceholder string
	MenuButton        BotMenuButton
	HasMainApp        bool
	HasAttachMenu     bool
	HasPreviewMedias  bool
	AppSettings       *BotAppSettings
}

// BotChatState is the persisted state machine of a built-in bot's (currently only BotFather's) chat with a user.
type BotChatState struct {
	BotUserID int64
	UserID    int64
	// Command is the in-progress main command (newbot/token/revoke).
	Command string `json:"command"`
	// Step is the current step within the main command (name/username/choose).
	Step string `json:"step"`
	// Draft buffers intermediate input across steps (e.g. the name already collected by newbot).
	Draft map[string]string `json:"draft,omitempty"`
}

// FormatBotToken assembles the full bot token (<bot_user_id>:<secret>).
func FormatBotToken(botUserID int64, secret string) string {
	return strconv.FormatInt(botUserID, 10) + ":" + secret
}

// ParseBotToken parses the full bot token. ok=false on an illegal format (without distinguishing the cause;
// the caller uniformly returns ACCESS_TOKEN_INVALID to avoid leaking existence).
func ParseBotToken(token string) (botUserID int64, secret string, ok bool) {
	idPart, secret, found := strings.Cut(token, ":")
	if !found || idPart == "" || secret == "" {
		return 0, "", false
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, secret, true
}

// ValidBotUsername validates a bot username: 5-32 chars, starting with a letter, [A-Za-z0-9_],
// ending in bot (case-insensitive). Seed accounts such as BotFather do not go through this validation.
func ValidBotUsername(username string) bool {
	if len(username) < 5 || len(username) > 32 {
		return false
	}
	for i, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9', r == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return strings.HasSuffix(strings.ToLower(username), BotUsernameSuffix)
}
