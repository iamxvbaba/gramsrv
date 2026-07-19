package domain

import (
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	// MaxMarkupRows 限制 inline keyboard 行数。
	MaxMarkupRows = 100
	// MaxMarkupButtonsPerRow 限制单行按钮数。
	MaxMarkupButtonsPerRow = 8
	// MaxMarkupButtonsTotal 限制 inline keyboard 总按钮数（对齐官方约 100）。
	MaxMarkupButtonsTotal = 100
	// MaxCallbackDataLen 是 callback 按钮 data 的字节上限（对齐 Bot API 1-64）。
	MaxCallbackDataLen = 64
	// MaxMarkupButtonTextLen 是按钮文本长度上限（rune 计数）。
	MaxMarkupButtonTextLen = 256
	// MaxReplyKeyboardButtonTextLen 对齐 Bot API KeyboardButton 的 1-64 字符约束。
	MaxReplyKeyboardButtonTextLen = 64
	// MaxReplyKeyboardPlaceholderLen 是 reply keyboard / force reply 输入框占位符上限。
	MaxReplyKeyboardPlaceholderLen = 64
	// MaxBotCallbackAnswerLen 是 callback answer 弹窗/toast 文本上限。
	MaxBotCallbackAnswerLen = 200
	// MaxStartParamLen 是 messages.startBot 深链 payload 上限（对齐官方 64）。
	MaxStartParamLen = 64
)

// markup / callback 业务错误。
var (
	// ErrButtonDataInvalid 表示 callback data 越界（>64 字节）。
	ErrButtonDataInvalid = errors.New("button data invalid")
	// ErrButtonInvalid 表示键盘结构非法（行/按钮数超限、文本空/过长）。
	ErrButtonInvalid = errors.New("button invalid")
	// ErrButtonURLInvalid 表示 url 按钮的链接非法（非 https）。
	ErrButtonURLInvalid = errors.New("button url invalid")
	// ErrButtonTypeInvalid 表示按钮类型 P3 不支持（webview/game/url_auth/request_* 等）。
	ErrButtonTypeInvalid = errors.New("button type invalid")
	// ErrStartParamInvalid 表示 startBot 的 start_param 越界。
	ErrStartParamInvalid = errors.New("start param invalid")
)

// MarkupButtonType 标识消息键盘按钮类型。
type MarkupButtonType string

const (
	// MarkupButtonText 是 reply keyboard 的普通文本按钮；点击后客户端发送标准文本消息。
	MarkupButtonText MarkupButtonType = "text"
	// MarkupButtonCallback 是 keyboardButtonCallback（点击触发 getBotCallbackAnswer）。
	MarkupButtonCallback MarkupButtonType = "callback"
	// MarkupButtonURL 是 keyboardButtonUrl（点击打开链接）。
	MarkupButtonURL             MarkupButtonType = "url"
	MarkupButtonRequestPhone    MarkupButtonType = "request_phone"
	MarkupButtonRequestLocation MarkupButtonType = "request_location"
	MarkupButtonRequestPoll     MarkupButtonType = "request_poll"
	MarkupButtonRequestPeer     MarkupButtonType = "request_peer"
	MarkupButtonWebView         MarkupButtonType = "webview"
	MarkupButtonSimpleWebView   MarkupButtonType = "simple_webview"
	MarkupButtonSwitchInline    MarkupButtonType = "switch_inline"
	MarkupButtonCopy            MarkupButtonType = "copy"
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

// MarkupButton 是一颗消息键盘按钮。reply keyboard 当前只接受普通文本按钮；
// inline keyboard 当前接受 callback/url。
type MarkupButton struct {
	Type MarkupButtonType `json:"type"`
	Text string           `json:"text"`
	// Style is one of primary/danger/success. Empty means the client default.
	Style MarkupButtonStyle `json:"style,omitempty"`
	// IconCustomEmojiID is the optional custom emoji rendered before Text.
	IconCustomEmojiID int64 `json:"icon_custom_emoji_id,omitempty"`
	// Data 仅 callback 使用：原始字节（含 0x00/非 UTF-8/高位）。json 自动 base64
	// 编解码，保证经 JSONB 列字节级 round-trip（updateBotCallbackQuery.data 须原样）。
	Data []byte `json:"data,omitempty"`
	// URL 仅 url 使用。
	URL string `json:"url,omitempty"`
	// RequiresPassword 仅 callback 使用（keyboardButtonCallback.requires_password，
	// 2FA SRP 校验 P3 stub）。
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

// MessageReplyMarkupType 标识互斥的 ReplyMarkup constructor。
type MessageReplyMarkupType string

const (
	MessageReplyMarkupInline     MessageReplyMarkupType = "inline"
	MessageReplyMarkupKeyboard   MessageReplyMarkupType = "keyboard"
	MessageReplyMarkupHide       MessageReplyMarkupType = "hide"
	MessageReplyMarkupForceReply MessageReplyMarkupType = "force_reply"
)

// MessageReplyMarkup 是消息携带的协议中立 reply markup 快照。Type 为空且 Inline
// 非空表示 0110 之前已经持久化的合法 inline keyboard，Kind 会将其解释为 inline。
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

// Kind 返回 markup constructor；兼容已落库的无 Type inline 快照。
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

// IsReplyKeyboardFamily 报告 markup 是否会控制输入框下方的 reply keyboard。
func (m *MessageReplyMarkup) IsReplyKeyboardFamily() bool {
	switch m.Kind() {
	case MessageReplyMarkupKeyboard, MessageReplyMarkupHide, MessageReplyMarkupForceReply:
		return true
	default:
		return false
	}
}

// IsZero 报告 markup 是否为空（无任何按钮）。空 markup 不写 wire flag、不入库。
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

// ValidateReplyMarkup 校验 markup constructor、结构与按钮，校验须先于落库（I9）。
// 空 inline markup 合法（视为清空/无键盘）。
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
	default:
		// webview/game/url_auth/request_* 等 P3 未实现类型：拒绝，绝不半实现下发。
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

// BotCallbackAnswer 是 bot 对一次 callback query 的应答（setBotCallbackAnswer →
// 解挂等待中的 getBotCallbackAnswer）。
type BotCallbackAnswer struct {
	Alert     bool
	Message   string
	URL       string
	CacheTime int
}
