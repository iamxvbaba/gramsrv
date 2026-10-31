package domain

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	// MaxMarkupRows limits the inline keyboard row count.
	MaxMarkupRows = 100
	// MaxMarkupButtonsPerRow limits the per-row button count.
	MaxMarkupButtonsPerRow = 8
	// MaxMarkupButtonsTotal limits the inline keyboard total button count (aligned with the official ~100).
	MaxMarkupButtonsTotal = 100
	// MaxCallbackDataLen is the byte limit of a callback button's data (aligned with Bot API 1-64).
	MaxCallbackDataLen = 64
	// MaxMarkupButtonTextLen is the button text length limit (rune count).
	MaxMarkupButtonTextLen = 256
	// MaxReplyKeyboardButtonTextLen aligns with Bot API KeyboardButton's 1-64 character constraint.
	MaxReplyKeyboardButtonTextLen = 64
	// MaxReplyKeyboardPlaceholderLen is the placeholder limit for the reply keyboard / force reply input field.
	MaxReplyKeyboardPlaceholderLen = 64
	// MaxBotCallbackAnswerLen is the text limit of a callback answer alert/toast.
	MaxBotCallbackAnswerLen = 200
	// MaxStartParamLen is the deep-link payload limit of messages.startBot (aligned with the official 64).
	MaxStartParamLen = 64
)

// Markup / callback business errors.
var (
	// ErrButtonDataInvalid means the callback data is out of bounds (>64 bytes).
	ErrButtonDataInvalid = errors.New("button data invalid")
	// ErrButtonInvalid means the keyboard structure is invalid (row/button count exceeded, empty/too-long text).
	ErrButtonInvalid = errors.New("button invalid")
	// ErrButtonURLInvalid means a url button's link is invalid (not https).
	ErrButtonURLInvalid = errors.New("button url invalid")
	// ErrButtonTypeInvalid means the button type is unsupported in P3 (webview/game/url_auth/request_* etc.).
	ErrButtonTypeInvalid = errors.New("button type invalid")
	// ErrStartParamInvalid means startBot's start_param is out of bounds.
	ErrStartParamInvalid = errors.New("start param invalid")
)

// MarkupButtonType identifies a message keyboard button type.
type MarkupButtonType string

const (
	// MarkupButtonText is a plain text button of the reply keyboard; tapping it makes the client send a standard text message.
	MarkupButtonText MarkupButtonType = "text"
	// MarkupButtonCallback is keyboardButtonCallback (tapping it triggers getBotCallbackAnswer).
	MarkupButtonCallback MarkupButtonType = "callback"
	// MarkupButtonURL is keyboardButtonUrl (tapping it opens a link).
	MarkupButtonURL MarkupButtonType = "url"
	// MarkupButtonLoginURL is Bot API login_url / inputKeyboardButtonUrlAuth.
	// The target bot is resolved and the linked origin is verified before the
	// message is persisted; ButtonID is the stable flattened keyboard index
	// returned to clients as keyboardButtonUrlAuth.button_id.
	MarkupButtonLoginURL        MarkupButtonType = "login_url"
	MarkupButtonRequestPhone    MarkupButtonType = "request_phone"
	MarkupButtonRequestLocation MarkupButtonType = "request_location"
	MarkupButtonRequestPoll     MarkupButtonType = "request_poll"
	MarkupButtonRequestPeer     MarkupButtonType = "request_peer"
	MarkupButtonWebView         MarkupButtonType = "webview"
	MarkupButtonSimpleWebView   MarkupButtonType = "simple_webview"
	MarkupButtonSwitchInline    MarkupButtonType = "switch_inline"
	MarkupButtonCopy            MarkupButtonType = "copy"
	// MarkupButtonBuy is keyboardButtonBuy. It is valid only on an inline
	// keyboard attached to invoice media.
	MarkupButtonBuy MarkupButtonType = "buy"
)

// MarkupButtonStyle is the protocol-neutral semantic button color. Telegram
// intentionally exposes semantic colors instead of arbitrary RGB values.
type MarkupButtonStyle string

const (
	MarkupButtonStylePrimary MarkupButtonStyle = "primary"
	MarkupButtonStyleDanger  MarkupButtonStyle = "danger"
	MarkupButtonStyleSuccess MarkupButtonStyle = "success"
)

// BotRequestAdminRights mirrors Bot API ChatAdministratorRights without
// importing protocol types into persisted message state.
type BotRequestAdminRights struct {
	Anonymous            bool `json:"anonymous,omitempty"`
	ManageChat           bool `json:"manage_chat,omitempty"`
	DeleteMessages       bool `json:"delete_messages,omitempty"`
	ManageVideoChats     bool `json:"manage_video_chats,omitempty"`
	RestrictMembers      bool `json:"restrict_members,omitempty"`
	PromoteMembers       bool `json:"promote_members,omitempty"`
	ChangeInfo           bool `json:"change_info,omitempty"`
	InviteUsers          bool `json:"invite_users,omitempty"`
	PostStories          bool `json:"post_stories,omitempty"`
	EditStories          bool `json:"edit_stories,omitempty"`
	DeleteStories        bool `json:"delete_stories,omitempty"`
	PostMessages         bool `json:"post_messages,omitempty"`
	EditMessages         bool `json:"edit_messages,omitempty"`
	PinMessages          bool `json:"pin_messages,omitempty"`
	ManageTopics         bool `json:"manage_topics,omitempty"`
	ManageDirectMessages bool `json:"manage_direct_messages,omitempty"`
}

type BotRequestPeerFilter struct {
	UserIsBotSet       bool                   `json:"user_is_bot_set,omitempty"`
	UserIsBot          bool                   `json:"user_is_bot,omitempty"`
	UserIsPremiumSet   bool                   `json:"user_is_premium_set,omitempty"`
	UserIsPremium      bool                   `json:"user_is_premium,omitempty"`
	ChatHasUsernameSet bool                   `json:"chat_has_username_set,omitempty"`
	ChatHasUsername    bool                   `json:"chat_has_username,omitempty"`
	ChatIsForumSet     bool                   `json:"chat_is_forum_set,omitempty"`
	ChatIsForum        bool                   `json:"chat_is_forum,omitempty"`
	ChatIsCreated      bool                   `json:"chat_is_created,omitempty"`
	BotIsMember        bool                   `json:"bot_is_member,omitempty"`
	UserAdminRights    *BotRequestAdminRights `json:"user_admin_rights,omitempty"`
	BotAdminRights     *BotRequestAdminRights `json:"bot_admin_rights,omitempty"`
}

// MarkupButton is one message keyboard button. The reply keyboard currently accepts only plain text buttons;
// the inline keyboard currently accepts callback/url.
type MarkupButton struct {
	Type MarkupButtonType `json:"type"`
	Text string           `json:"text"`
	// Style is one of primary/danger/success. Empty means the client default.
	Style MarkupButtonStyle `json:"style,omitempty"`
	// IconCustomEmojiID is the optional custom emoji rendered before Text.
	IconCustomEmojiID int64 `json:"icon_custom_emoji_id,omitempty"`
	// Data is used only by callback: raw bytes (including 0x00/non-UTF-8/high bits). json automatically base64
	// encodes/decodes, guaranteeing a byte-level round-trip through the JSONB column (updateBotCallbackQuery.data must be verbatim).
	Data []byte `json:"data,omitempty"`
	// URL is used only by url.
	URL string `json:"url,omitempty"`
	// Login URL-only fields. LoginBotUserID=0 means the sending bot until the
	// RPC/Bot API edge resolves it. LoginBotUsername is input-only and must be
	// cleared before persistence.
	ForwardText        string `json:"forward_text,omitempty"`
	LoginBotUserID     int64  `json:"login_bot_user_id,omitempty"`
	LoginBotUsername   string `json:"login_bot_username,omitempty"`
	RequestWriteAccess bool   `json:"request_write_access,omitempty"`
	// RequiresPassword is used only by callback (keyboardButtonCallback.requires_password,
	// the P3 stub for 2FA SRP verification).
	RequiresPassword bool `json:"requires_password,omitempty"`
	// PollType is empty, "regular", or "quiz" for request_poll.
	PollType string `json:"poll_type,omitempty"`
	// ButtonID and request-peer fields preserve Bot API request_id and the
	// client-side chooser shape. RequestPeerType is user/chat/broadcast.
	ButtonID          int                   `json:"button_id,omitempty"`
	RequestPeerType   string                `json:"request_peer_type,omitempty"`
	MaxQuantity       int                   `json:"max_quantity,omitempty"`
	NameRequested     bool                  `json:"name_requested,omitempty"`
	UsernameRequested bool                  `json:"username_requested,omitempty"`
	PhotoRequested    bool                  `json:"photo_requested,omitempty"`
	RequestPeerFilter *BotRequestPeerFilter `json:"request_peer_filter,omitempty"`
	Query             string                `json:"query,omitempty"`
	SamePeer          bool                  `json:"same_peer,omitempty"`
	PeerTypes         []string              `json:"peer_types,omitempty"`
	CopyText          string                `json:"copy_text,omitempty"`
}

// MessageReplyMarkupType identifies the mutually exclusive ReplyMarkup constructors.
type MessageReplyMarkupType string

const (
	MessageReplyMarkupInline     MessageReplyMarkupType = "inline"
	MessageReplyMarkupKeyboard   MessageReplyMarkupType = "keyboard"
	MessageReplyMarkupHide       MessageReplyMarkupType = "hide"
	MessageReplyMarkupForceReply MessageReplyMarkupType = "force_reply"
)

// MessageReplyMarkup is the protocol-neutral reply markup snapshot carried by a message. An empty Type with a
// non-empty Inline means a valid inline keyboard persisted before 0110, which Kind interprets as inline.
type MessageReplyMarkup struct {
	Type        MessageReplyMarkupType `json:"type,omitempty"`
	Inline      [][]MarkupButton       `json:"inline,omitempty"`
	Keyboard    [][]MarkupButton       `json:"keyboard,omitempty"`
	Resize      bool                   `json:"resize,omitempty"`
	SingleUse   bool                   `json:"single_use,omitempty"`
	Selective   bool                   `json:"selective,omitempty"`
	Persistent  bool                   `json:"persistent,omitempty"`
	Placeholder string                 `json:"placeholder,omitempty"`
}

// Kind returns the markup constructor; it is compatible with an already-persisted inline snapshot without a Type.
func (m *MessageReplyMarkup) Kind() MessageReplyMarkupType {
	if m == nil {
		return ""
	}
	if m.Type != "" {
		return m.Type
	}
	if len(m.Inline) > 0 {
		return MessageReplyMarkupInline
	}
	return ""
}

// IsReplyKeyboardFamily reports whether the markup controls the reply keyboard below the input field.
func (m *MessageReplyMarkup) IsReplyKeyboardFamily() bool {
	switch m.Kind() {
	case MessageReplyMarkupKeyboard, MessageReplyMarkupHide, MessageReplyMarkupForceReply:
		return true
	default:
		return false
	}
}

// IsZero reports whether the markup is empty (no buttons). An empty markup writes no wire flag and is not persisted.
func (m *MessageReplyMarkup) IsZero() bool {
	if m == nil {
		return true
	}
	switch m.Kind() {
	case MessageReplyMarkupInline:
		for _, row := range m.Inline {
			if len(row) > 0 {
				return false
			}
		}
		return true
	case MessageReplyMarkupKeyboard:
		for _, row := range m.Keyboard {
			if len(row) > 0 {
				return false
			}
		}
		return true
	case MessageReplyMarkupHide, MessageReplyMarkupForceReply:
		return false
	default:
		return true
	}
}

// ValidateReplyMarkup validates the markup constructor, structure and buttons; validation must precede persist (I9).
// An empty inline markup is valid (treated as clearing/no keyboard).
func ValidateReplyMarkup(m *MessageReplyMarkup) error {
	if m == nil {
		return nil
	}
	kind := m.Kind()
	if kind == "" {
		if len(m.Inline) != 0 || len(m.Keyboard) != 0 || m.Resize || m.SingleUse || m.Selective || m.Persistent || m.Placeholder != "" {
			return ErrButtonInvalid
		}
		return nil
	}
	switch kind {
	case MessageReplyMarkupInline:
		if len(m.Keyboard) != 0 || m.Resize || m.SingleUse || m.Selective || m.Persistent || m.Placeholder != "" {
			return ErrButtonInvalid
		}
		return validateMarkupRows(m.Inline, false)
	case MessageReplyMarkupKeyboard:
		if len(m.Inline) != 0 || utf8.RuneCountInString(m.Placeholder) > MaxReplyKeyboardPlaceholderLen {
			return ErrButtonInvalid
		}
		if len(m.Keyboard) == 0 {
			return ErrButtonInvalid
		}
		return validateMarkupRows(m.Keyboard, true)
	case MessageReplyMarkupHide:
		if len(m.Inline) != 0 || len(m.Keyboard) != 0 || m.Resize || m.SingleUse || m.Persistent || m.Placeholder != "" {
			return ErrButtonInvalid
		}
		return nil
	case MessageReplyMarkupForceReply:
		if len(m.Inline) != 0 || len(m.Keyboard) != 0 || m.Resize || m.Persistent || utf8.RuneCountInString(m.Placeholder) > MaxReplyKeyboardPlaceholderLen {
			return ErrButtonInvalid
		}
		return nil
	default:
		return ErrButtonInvalid
	}
}

func validateMarkupRows(rows [][]MarkupButton, replyKeyboard bool) error {
	if len(rows) > MaxMarkupRows {
		return ErrButtonInvalid
	}
	total := 0
	for _, row := range rows {
		if len(row) == 0 || len(row) > MaxMarkupButtonsPerRow {
			return ErrButtonInvalid
		}
		total += len(row)
		if total > MaxMarkupButtonsTotal {
			return ErrButtonInvalid
		}
		for i := range row {
			if err := validateMarkupButton(row[i], replyKeyboard); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateMarkupButton(b MarkupButton, replyKeyboard bool) error {
	text := strings.TrimSpace(b.Text)
	if text == "" || utf8.RuneCountInString(b.Text) > MaxMarkupButtonTextLen {
		return ErrButtonInvalid
	}
	switch b.Style {
	case "", MarkupButtonStylePrimary, MarkupButtonStyleDanger, MarkupButtonStyleSuccess:
	default:
		return ErrButtonInvalid
	}
	if b.IconCustomEmojiID < 0 {
		return ErrButtonInvalid
	}
	if replyKeyboard {
		if utf8.RuneCountInString(b.Text) > MaxReplyKeyboardButtonTextLen {
			return ErrButtonInvalid
		}
		switch b.Type {
		case MarkupButtonText, MarkupButtonRequestPhone, MarkupButtonRequestLocation:
		case MarkupButtonRequestPoll:
			if b.PollType != "" && b.PollType != "regular" && b.PollType != "quiz" {
				return ErrButtonInvalid
			}
		case MarkupButtonRequestPeer:
			if b.ButtonID == 0 || b.MaxQuantity < 1 || b.MaxQuantity > 10 ||
				(b.RequestPeerType != "user" && b.RequestPeerType != "chat" && b.RequestPeerType != "broadcast") {
				return ErrButtonInvalid
			}
			if b.RequestPeerFilter != nil {
				filter := b.RequestPeerFilter
				if b.RequestPeerType == "user" {
					if filter.ChatHasUsernameSet || filter.ChatIsForumSet || filter.ChatIsCreated || filter.BotIsMember ||
						filter.UserAdminRights != nil || filter.BotAdminRights != nil {
						return ErrButtonInvalid
					}
				} else {
					if filter.UserIsBotSet || filter.UserIsPremiumSet ||
						(b.RequestPeerType == "broadcast" && (filter.ChatIsForumSet || filter.BotIsMember)) {
						return ErrButtonInvalid
					}
				}
			}
		case MarkupButtonSimpleWebView:
			if err := validateButtonURL(b.URL); err != nil {
				return err
			}
		default:
			return ErrButtonTypeInvalid
		}
		if len(b.Data) != 0 || b.RequiresPassword || b.Query != "" || b.SamePeer || len(b.PeerTypes) != 0 || b.CopyText != "" {
			return ErrButtonInvalid
		}
		return nil
	}
	switch b.Type {
	case MarkupButtonCallback:
		if len(b.Data) > MaxCallbackDataLen {
			return ErrButtonDataInvalid
		}
	case MarkupButtonURL:
		if err := validateButtonURL(b.URL); err != nil {
			return err
		}
	case MarkupButtonLoginURL:
		if err := validateLoginButtonURL(b.URL); err != nil {
			return err
		}
		if b.ButtonID < 0 || b.LoginBotUserID < 0 || utf8.RuneCountInString(b.ForwardText) > MaxReplyKeyboardButtonTextLen ||
			utf8.RuneCountInString(b.LoginBotUsername) > 64 {
			return ErrButtonInvalid
		}
	case MarkupButtonWebView:
		if err := validateButtonURL(b.URL); err != nil {
			return err
		}
	case MarkupButtonSwitchInline:
		if utf8.RuneCountInString(b.Query) > 256 {
			return ErrButtonInvalid
		}
	case MarkupButtonCopy:
		if b.CopyText == "" || utf8.RuneCountInString(b.CopyText) > 256 {
			return ErrButtonInvalid
		}
	case MarkupButtonBuy:
		if len(b.Data) != 0 || b.URL != "" || b.Query != "" || b.CopyText != "" ||
			b.RequiresPassword || b.LoginBotUserID != 0 || b.ButtonID != 0 {
			return ErrButtonInvalid
		}
	default:
		// P3-unimplemented types such as webview/game/url_auth/request_*: reject, never ship a half-implementation.
		return ErrButtonTypeInvalid
	}
	return nil
}

func validateButtonURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxBotMenuButtonURLLen {
		return ErrButtonURLInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ErrButtonURLInvalid
	}
	return nil
}

// validateLoginButtonURL performs only the protocol-shape validation shared by
// Bot API and MTProto input buttons. The Telegram Login service remains the
// authority for the deployment policy: HTTP is accepted here as a protocol
// shape, then allowed only when the Login HTTP switch is enabled and the exact
// origin is registered.
func validateLoginButtonURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > MaxBotMenuButtonURLLen {
		return ErrButtonURLInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return ErrButtonURLInvalid
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return ErrButtonURLInvalid
	}
	return nil
}

// BotCallbackAnswer is a bot's answer to one callback query (setBotCallbackAnswer →
// unblocks a waiting getBotCallbackAnswer).
type BotCallbackAnswer struct {
	Alert     bool
	Message   string
	URL       string
	CacheTime int
}
