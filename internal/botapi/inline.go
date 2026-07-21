package botapi

import (
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func inlineResultFromAPI(raw string) (domain.BotInlineResult, error) {
	if strings.TrimSpace(raw) == "" {
		return domain.BotInlineResult{}, errors.New("RESULT_ID_INVALID")
	}
	var payload apiInlineResult
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return domain.BotInlineResult{}, errors.New("RESULT_TYPE_INVALID")
	}
	if payload.ID == "" {
		return domain.BotInlineResult{}, errors.New("RESULT_ID_EMPTY")
	}
	if len(payload.ID) > domain.MaxBotInlineResultIDLen {
		return domain.BotInlineResult{}, errors.New("RESULT_ID_INVALID")
	}
	if payload.Type != "article" {
		return domain.BotInlineResult{}, errors.New("RESULT_TYPE_INVALID")
	}
	if payload.URL != "" && !validBotAPIHTTPSURL(payload.URL) {
		return domain.BotInlineResult{}, errors.New("BUTTON_URL_INVALID")
	}
	message, entities, noWebpage, err := inputTextMessageContentFromAPI(payload)
	if err != nil {
		return domain.BotInlineResult{}, err
	}
	markup, err := inlineReplyMarkupFromAPI(payload.ReplyMarkup)
	if err != nil {
		return domain.BotInlineResult{}, err
	}
	return domain.BotInlineResult{
		ID:          payload.ID,
		Type:        payload.Type,
		Title:       payload.Title,
		Description: payload.Description,
		URL:         payload.URL,
		Message:     message,
		Entities:    entities,
		ReplyMarkup: markup,
		NoWebpage:   noWebpage,
	}, nil
}

func inputTextMessageContentFromAPI(payload apiInlineResult) (string, []domain.MessageEntity, bool, error) {
	var content apiInputTextMessageContent
	if len(payload.InputMessageContent) > 0 && string(payload.InputMessageContent) != "null" {
		if err := json.Unmarshal(payload.InputMessageContent, &content); err != nil {
			return "", nil, false, errors.New("MESSAGE_EMPTY")
		}
	} else if payload.MessageText != "" {
		content.MessageText = payload.MessageText
	}
	message, entities, err := botAPIFormattedText(content.MessageText, content.ParseMode, content.Entities, domain.MaxMessageTextLength, true)
	if err != nil {
		return "", nil, false, err
	}
	noWebpage := content.DisableWebPagePreview || content.LinkPreviewOptions.IsDisabled
	return message, entities, noWebpage, nil
}

func messageEntitiesFromAPI(in []apiMessageEntity) ([]domain.MessageEntity, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > domain.MaxMessageEntityCount {
		return nil, errors.New("ENTITIES_TOO_LONG")
	}
	out := make([]domain.MessageEntity, 0, len(in))
	for _, entity := range in {
		if entity.Offset < 0 || entity.Length <= 0 {
			return nil, errors.New("ENTITY_BOUNDS_INVALID")
		}
		mapped, ok := apiEntityType(entity.Type)
		if !ok {
			return nil, errors.New("ENTITY_TYPE_UNSUPPORTED")
		}
		item := domain.MessageEntity{
			Type:   mapped,
			Offset: entity.Offset,
			Length: entity.Length,
		}
		switch mapped {
		case domain.MessageEntityTextURL:
			resolved, ok := botAPITextLinkEntity(entity.URL, entity.Offset, entity.Length)
			if !ok {
				return nil, errors.New("ENTITY_TYPE_UNSUPPORTED")
			}
			item = resolved
		case domain.MessageEntityMentionName:
			if entity.User != nil {
				item.UserID = entity.User.ID
			}
			if item.UserID <= 0 {
				return nil, errors.New("ENTITY_TYPE_UNSUPPORTED")
			}
		case domain.MessageEntityPre:
			item.Language = entity.Language
		case domain.MessageEntityBlockquote:
			item.Collapsed = entity.Type == "expandable_blockquote"
		case domain.MessageEntityCustomEmoji:
			id, err := strconv.ParseInt(entity.CustomEmojiID, 10, 64)
			if err != nil || id <= 0 {
				return nil, errors.New("ENTITY_TYPE_UNSUPPORTED")
			}
			item.DocumentID = id
		case domain.MessageEntityFormattedDate:
			formatted, err := botAPIFormattedDate(entity.UnixTime, entity.DateTimeFormat)
			if err != nil {
				return nil, errors.New("ENTITY_TYPE_UNSUPPORTED")
			}
			formatted.Offset, formatted.Length = entity.Offset, entity.Length
			item = formatted
		}
		out = append(out, item)
	}
	return out, nil
}

func apiEntityType(in string) (domain.MessageEntityType, bool) {
	switch in {
	case "bold":
		return domain.MessageEntityBold, true
	case "italic":
		return domain.MessageEntityItalic, true
	case "underline":
		return domain.MessageEntityUnderline, true
	case "strikethrough":
		return domain.MessageEntityStrike, true
	case "code":
		return domain.MessageEntityCode, true
	case "pre":
		return domain.MessageEntityPre, true
	case "text_link":
		return domain.MessageEntityTextURL, true
	case "text_mention":
		return domain.MessageEntityMentionName, true
	case "spoiler":
		return domain.MessageEntitySpoiler, true
	case "blockquote":
		return domain.MessageEntityBlockquote, true
	case "expandable_blockquote":
		return domain.MessageEntityBlockquote, true
	case "custom_emoji":
		return domain.MessageEntityCustomEmoji, true
	case "mention":
		return domain.MessageEntityMention, true
	case "hashtag":
		return domain.MessageEntityHashtag, true
	case "cashtag":
		return domain.MessageEntityCashtag, true
	case "bot_command":
		return domain.MessageEntityBotCommand, true
	case "url":
		return domain.MessageEntityURL, true
	case "email":
		return domain.MessageEntityEmail, true
	case "phone_number":
		return domain.MessageEntityPhone, true
	case "bank_card_number":
		return domain.MessageEntityBankCard, true
	case "date_time":
		return domain.MessageEntityFormattedDate, true
	default:
		return "", false
	}
}

func replyMarkupFromAPI(raw json.RawMessage) (*domain.MessageReplyMarkup, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		return nil, errors.New("BUTTON_INVALID")
	}
	constructors := 0
	for _, key := range []string{"inline_keyboard", "keyboard", "remove_keyboard", "force_reply"} {
		if _, ok := shape[key]; ok {
			constructors++
		}
	}
	if constructors != 1 {
		return nil, errors.New("BUTTON_INVALID")
	}
	if _, ok := shape["inline_keyboard"]; ok {
		return inlineKeyboardMarkupFromAPI(raw)
	}
	if _, ok := shape["keyboard"]; ok {
		return replyKeyboardMarkupFromAPI(raw)
	}
	if _, ok := shape["remove_keyboard"]; ok {
		var payload apiReplyKeyboardRemove
		if err := json.Unmarshal(raw, &payload); err != nil || !payload.RemoveKeyboard {
			return nil, errors.New("BUTTON_INVALID")
		}
		out := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupHide, Selective: payload.Selective}
		if err := domain.ValidateReplyMarkup(out); err != nil {
			return nil, replyMarkupErrFromDomain(err)
		}
		return out, nil
	}
	var payload apiForceReply
	if err := json.Unmarshal(raw, &payload); err != nil || !payload.ForceReply {
		return nil, errors.New("BUTTON_INVALID")
	}
	out := &domain.MessageReplyMarkup{
		Type:        domain.MessageReplyMarkupForceReply,
		SingleUse:   true,
		Selective:   payload.Selective,
		Placeholder: payload.InputFieldPlaceholder,
	}
	if err := domain.ValidateReplyMarkup(out); err != nil {
		return nil, replyMarkupErrFromDomain(err)
	}
	return out, nil
}

func inlineReplyMarkupFromAPI(raw json.RawMessage) (*domain.MessageReplyMarkup, error) {
	markup, err := replyMarkupFromAPI(raw)
	if err != nil || markup == nil {
		return markup, err
	}
	if markup.Kind() != domain.MessageReplyMarkupInline {
		return nil, errors.New("BUTTON_INVALID")
	}
	return markup, nil
}

func inlineKeyboardMarkupFromAPI(raw json.RawMessage) (*domain.MessageReplyMarkup, error) {
	var payload apiInlineKeyboardMarkup
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errors.New("BUTTON_INVALID")
	}
	if len(payload.InlineKeyboard) == 0 {
		return nil, nil
	}
	out := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: make([][]domain.MarkupButton, 0, len(payload.InlineKeyboard))}
	for _, row := range payload.InlineKeyboard {
		domainRow := make([]domain.MarkupButton, 0, len(row))
		for _, button := range row {
			item, err := markupButtonFromAPI(button)
			if err != nil {
				return nil, err
			}
			domainRow = append(domainRow, item)
		}
		out.Inline = append(out.Inline, domainRow)
	}
	if err := domain.ValidateReplyMarkup(out); err != nil {
		return nil, replyMarkupErrFromDomain(err)
	}
	if out.IsZero() {
		return nil, nil
	}
	return out, nil
}

func replyKeyboardMarkupFromAPI(raw json.RawMessage) (*domain.MessageReplyMarkup, error) {
	var payload apiReplyKeyboardMarkup
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.Keyboard) == 0 {
		return nil, errors.New("BUTTON_INVALID")
	}
	out := &domain.MessageReplyMarkup{
		Type:        domain.MessageReplyMarkupKeyboard,
		Keyboard:    make([][]domain.MarkupButton, 0, len(payload.Keyboard)),
		Resize:      payload.ResizeKeyboard,
		SingleUse:   payload.OneTimeKeyboard,
		Selective:   payload.Selective,
		Persistent:  payload.IsPersistent,
		Placeholder: payload.InputFieldPlaceholder,
	}
	for _, row := range payload.Keyboard {
		domainRow := make([]domain.MarkupButton, 0, len(row))
		for _, button := range row {
			if button.Text == "" {
				return nil, errors.New("BUTTON_INVALID")
			}
			if button.Unsupported {
				return nil, errors.New("BUTTON_TYPE_INVALID")
			}
			style, icon, err := markupButtonDecorationFromAPI(button.Style, button.IconCustomEmojiID, button.IconCustomEmojiIDSet)
			if err != nil {
				return nil, err
			}
			item := domain.MarkupButton{Type: domain.MarkupButtonText, Text: button.Text, Style: style, IconCustomEmojiID: icon}
			switch button.Kind {
			case "request_contact":
				item.Type = domain.MarkupButtonRequestPhone
			case "request_location":
				item.Type = domain.MarkupButtonRequestLocation
			case "request_poll":
				item.Type, item.PollType = domain.MarkupButtonRequestPoll, button.PollType
			case "request_users":
				item.Type, item.ButtonID, item.RequestPeerType = domain.MarkupButtonRequestPeer, button.RequestID, "user"
				item.MaxQuantity, item.NameRequested, item.UsernameRequested, item.PhotoRequested = button.MaxQuantity, button.RequestName, button.RequestUsername, button.RequestPhoto
				item.RequestPeerFilter = button.RequestPeerFilter
			case "request_chat":
				item.Type, item.ButtonID = domain.MarkupButtonRequestPeer, button.RequestID
				if button.ChatIsChannel {
					item.RequestPeerType = "broadcast"
				} else {
					item.RequestPeerType = "chat"
				}
				item.MaxQuantity, item.NameRequested, item.UsernameRequested, item.PhotoRequested = 1, button.RequestTitle, button.RequestUsername, button.RequestPhoto
				item.RequestPeerFilter = button.RequestPeerFilter
			case "web_app":
				item.Type, item.URL = domain.MarkupButtonSimpleWebView, button.WebAppURL
			}
			domainRow = append(domainRow, item)
		}
		out.Keyboard = append(out.Keyboard, domainRow)
	}
	if err := domain.ValidateReplyMarkup(out); err != nil {
		return nil, replyMarkupErrFromDomain(err)
	}
	return out, nil
}

func markupButtonFromAPI(button apiInlineKeyboardButton) (domain.MarkupButton, error) {
	if button.Unsupported {
		return domain.MarkupButton{}, errors.New("BUTTON_TYPE_INVALID")
	}
	constructors := 0
	if button.URLSet {
		constructors++
	}
	if button.CallbackDataSet {
		constructors++
	}
	if button.WebAppSet {
		constructors++
	}
	if button.SwitchInlineSet {
		constructors++
	}
	if button.CopyTextSet {
		constructors++
	}
	if button.LoginURLSet {
		constructors++
	}
	if constructors != 1 {
		return domain.MarkupButton{}, errors.New("BUTTON_INVALID")
	}
	style, icon, err := markupButtonDecorationFromAPI(button.Style, button.IconCustomEmojiID, button.IconCustomEmojiIDSet)
	if err != nil {
		return domain.MarkupButton{}, err
	}
	if button.URLSet {
		return domain.MarkupButton{Type: domain.MarkupButtonURL, Text: button.Text, URL: button.URL, Style: style, IconCustomEmojiID: icon}, nil
	}
	if button.LoginURLSet {
		return domain.MarkupButton{
			Type: domain.MarkupButtonLoginURL, Text: button.Text, URL: button.LoginURL,
			ForwardText: button.LoginForwardText, LoginBotUsername: button.LoginBotUsername,
			RequestWriteAccess: button.LoginRequestWriteAccess, Style: style, IconCustomEmojiID: icon,
		}, nil
	}
	if button.CallbackDataSet {
		if button.CallbackData == "" || len([]byte(button.CallbackData)) > domain.MaxCallbackDataLen {
			return domain.MarkupButton{}, errors.New("BUTTON_DATA_INVALID")
		}
		return domain.MarkupButton{Type: domain.MarkupButtonCallback, Text: button.Text, Data: []byte(button.CallbackData), Style: style, IconCustomEmojiID: icon}, nil
	}
	if button.WebAppSet {
		return domain.MarkupButton{Type: domain.MarkupButtonWebView, Text: button.Text, URL: button.WebAppURL, Style: style, IconCustomEmojiID: icon}, nil
	}
	if button.SwitchInlineSet {
		return domain.MarkupButton{Type: domain.MarkupButtonSwitchInline, Text: button.Text, Query: button.SwitchInlineQuery, SamePeer: button.SwitchInlineSamePeer, PeerTypes: append([]string(nil), button.SwitchInlinePeerTypes...), Style: style, IconCustomEmojiID: icon}, nil
	}
	if button.CopyTextSet {
		return domain.MarkupButton{Type: domain.MarkupButtonCopy, Text: button.Text, CopyText: button.CopyText, Style: style, IconCustomEmojiID: icon}, nil
	}
	return domain.MarkupButton{}, errors.New("BUTTON_INVALID")
}

func markupButtonDecorationFromAPI(rawStyle, rawIcon string, iconSet bool) (domain.MarkupButtonStyle, int64, error) {
	style := domain.MarkupButtonStyle(strings.TrimSpace(rawStyle))
	switch style {
	case "", domain.MarkupButtonStylePrimary, domain.MarkupButtonStyleDanger, domain.MarkupButtonStyleSuccess:
	default:
		return "", 0, errors.New("BUTTON_INVALID")
	}
	if !iconSet {
		return style, 0, nil
	}
	icon, err := strconv.ParseInt(strings.TrimSpace(rawIcon), 10, 64)
	if err != nil || icon <= 0 {
		return "", 0, errors.New("BUTTON_INVALID")
	}
	return style, icon, nil
}

func replyMarkupErrFromDomain(err error) error {
	switch {
	case errors.Is(err, domain.ErrButtonURLInvalid):
		return errors.New("BUTTON_URL_INVALID")
	case errors.Is(err, domain.ErrButtonDataInvalid):
		return errors.New("BUTTON_DATA_INVALID")
	default:
		return errors.New("BUTTON_INVALID")
	}
}

func preparedPeerTypesFromAPI(values map[string]string) []string {
	out := make([]string, 0, 5)
	if apiBool(values["allow_user_chats"]) {
		out = append(out, store.InlineQueryPeerTypePM)
	}
	if apiBool(values["allow_bot_chats"]) {
		out = append(out, store.InlineQueryPeerTypeBotPM)
	}
	if apiBool(values["allow_group_chats"]) {
		out = append(out, store.InlineQueryPeerTypeChat, store.InlineQueryPeerTypeMegagroup)
	}
	if apiBool(values["allow_channel_chats"]) {
		out = append(out, store.InlineQueryPeerTypeBroadcast)
	}
	return out
}

func apiBool(raw string) bool {
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	return err == nil && v
}

func validBotAPIHTTPSURL(raw string) bool {
	if raw == "" || len(raw) > domain.MaxBotInlineWebURLLen || strings.TrimSpace(raw) != raw {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Host == "" || strings.ToLower(parsed.Scheme) != "https" {
		return false
	}
	return !strings.ContainsAny(parsed.Host, " \t\r\n")
}

type apiInlineResult struct {
	Type                string          `json:"type"`
	ID                  string          `json:"id"`
	Title               string          `json:"title"`
	Description         string          `json:"description"`
	URL                 string          `json:"url"`
	MessageText         string          `json:"message_text"`
	InputMessageContent json.RawMessage `json:"input_message_content"`
	ReplyMarkup         json.RawMessage `json:"reply_markup"`
}

type apiInputTextMessageContent struct {
	MessageText           string             `json:"message_text"`
	ParseMode             string             `json:"parse_mode"`
	Entities              []apiMessageEntity `json:"entities"`
	DisableWebPagePreview bool               `json:"disable_web_page_preview"`
	LinkPreviewOptions    struct {
		IsDisabled bool `json:"is_disabled"`
	} `json:"link_preview_options"`
}

type apiMessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	URL    string `json:"url"`
	User   *struct {
		ID int64 `json:"id"`
	} `json:"user"`
	Language       string `json:"language"`
	CustomEmojiID  string `json:"custom_emoji_id"`
	UnixTime       int    `json:"unix_time"`
	DateTimeFormat string `json:"date_time_format"`
}

type apiInlineKeyboardMarkup struct {
	InlineKeyboard [][]apiInlineKeyboardButton `json:"inline_keyboard"`
}

type apiReplyKeyboardMarkup struct {
	Keyboard              [][]apiKeyboardButton `json:"keyboard"`
	IsPersistent          bool                  `json:"is_persistent"`
	ResizeKeyboard        bool                  `json:"resize_keyboard"`
	OneTimeKeyboard       bool                  `json:"one_time_keyboard"`
	InputFieldPlaceholder string                `json:"input_field_placeholder"`
	Selective             bool                  `json:"selective"`
}

type apiKeyboardButton struct {
	Text                 string
	Style                string
	IconCustomEmojiID    string
	IconCustomEmojiIDSet bool
	Unsupported          bool
	Kind                 string
	PollType             string
	RequestID            int
	MaxQuantity          int
	RequestName          bool
	RequestUsername      bool
	RequestPhoto         bool
	RequestTitle         bool
	ChatIsChannel        bool
	WebAppURL            string
	RequestPeerFilter    *domain.BotRequestPeerFilter
}

type apiChatAdministratorRights struct {
	IsAnonymous             bool `json:"is_anonymous"`
	CanManageChat           bool `json:"can_manage_chat"`
	CanDeleteMessages       bool `json:"can_delete_messages"`
	CanManageVideoChats     bool `json:"can_manage_video_chats"`
	CanRestrictMembers      bool `json:"can_restrict_members"`
	CanPromoteMembers       bool `json:"can_promote_members"`
	CanChangeInfo           bool `json:"can_change_info"`
	CanInviteUsers          bool `json:"can_invite_users"`
	CanPostStories          bool `json:"can_post_stories"`
	CanEditStories          bool `json:"can_edit_stories"`
	CanDeleteStories        bool `json:"can_delete_stories"`
	CanPostMessages         bool `json:"can_post_messages"`
	CanEditMessages         bool `json:"can_edit_messages"`
	CanPinMessages          bool `json:"can_pin_messages"`
	CanManageTopics         bool `json:"can_manage_topics"`
	CanManageDirectMessages bool `json:"can_manage_direct_messages"`
}

func domainRequestAdminRights(in *apiChatAdministratorRights) *domain.BotRequestAdminRights {
	if in == nil {
		return nil
	}
	return &domain.BotRequestAdminRights{
		Anonymous: in.IsAnonymous, ManageChat: in.CanManageChat, DeleteMessages: in.CanDeleteMessages,
		ManageVideoChats: in.CanManageVideoChats, RestrictMembers: in.CanRestrictMembers,
		PromoteMembers: in.CanPromoteMembers, ChangeInfo: in.CanChangeInfo, InviteUsers: in.CanInviteUsers,
		PostStories: in.CanPostStories, EditStories: in.CanEditStories, DeleteStories: in.CanDeleteStories,
		PostMessages: in.CanPostMessages, EditMessages: in.CanEditMessages, PinMessages: in.CanPinMessages,
		ManageTopics: in.CanManageTopics, ManageDirectMessages: in.CanManageDirectMessages,
	}
}

func (b *apiKeyboardButton) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "\"") {
		b.Kind = "text"
		return json.Unmarshal([]byte(trimmed), &b.Text)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	text, ok := fields["text"]
	if !ok || json.Unmarshal(text, &b.Text) != nil {
		return errors.New("invalid keyboard button text")
	}
	if raw, ok := fields["style"]; ok {
		if err := json.Unmarshal(raw, &b.Style); err != nil {
			return err
		}
	}
	if raw, ok := fields["icon_custom_emoji_id"]; ok {
		b.IconCustomEmojiIDSet = true
		if err := json.Unmarshal(raw, &b.IconCustomEmojiID); err != nil {
			return err
		}
	}
	actions := 0
	if raw, ok := fields["request_contact"]; ok {
		var enabled bool
		if json.Unmarshal(raw, &enabled) != nil || !enabled {
			b.Unsupported = true
		} else {
			b.Kind = "request_contact"
			actions++
		}
	}
	if raw, ok := fields["request_location"]; ok {
		var enabled bool
		if json.Unmarshal(raw, &enabled) != nil || !enabled {
			b.Unsupported = true
		} else {
			b.Kind = "request_location"
			actions++
		}
	}
	if raw, ok := fields["request_poll"]; ok {
		var poll struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &poll) != nil {
			b.Unsupported = true
		} else {
			b.Kind, b.PollType = "request_poll", poll.Type
			actions++
		}
	}
	if raw, ok := fields["request_users"]; ok {
		var request struct {
			RequestID       int   `json:"request_id"`
			UserIsBot       *bool `json:"user_is_bot"`
			UserIsPremium   *bool `json:"user_is_premium"`
			MaxQuantity     int   `json:"max_quantity"`
			RequestName     bool  `json:"request_name"`
			RequestUsername bool  `json:"request_username"`
			RequestPhoto    bool  `json:"request_photo"`
		}
		if json.Unmarshal(raw, &request) != nil || request.RequestID == 0 {
			b.Unsupported = true
		} else {
			b.Kind, b.RequestID, b.MaxQuantity, b.RequestName, b.RequestUsername, b.RequestPhoto = "request_users", request.RequestID, request.MaxQuantity, request.RequestName, request.RequestUsername, request.RequestPhoto
			b.RequestPeerFilter = &domain.BotRequestPeerFilter{}
			if request.UserIsBot != nil {
				b.RequestPeerFilter.UserIsBotSet, b.RequestPeerFilter.UserIsBot = true, *request.UserIsBot
			}
			if request.UserIsPremium != nil {
				b.RequestPeerFilter.UserIsPremiumSet, b.RequestPeerFilter.UserIsPremium = true, *request.UserIsPremium
			}
			if b.MaxQuantity == 0 {
				b.MaxQuantity = 1
			}
			actions++
		}
	}
	if raw, ok := fields["request_chat"]; ok {
		var request struct {
			RequestID               int                         `json:"request_id"`
			ChatIsChannel           bool                        `json:"chat_is_channel"`
			ChatIsForum             *bool                       `json:"chat_is_forum"`
			ChatHasUsername         *bool                       `json:"chat_has_username"`
			ChatIsCreated           bool                        `json:"chat_is_created"`
			UserAdministratorRights *apiChatAdministratorRights `json:"user_administrator_rights"`
			BotAdministratorRights  *apiChatAdministratorRights `json:"bot_administrator_rights"`
			BotIsMember             bool                        `json:"bot_is_member"`
			RequestTitle            bool                        `json:"request_title"`
			RequestUsername         bool                        `json:"request_username"`
			RequestPhoto            bool                        `json:"request_photo"`
		}
		if json.Unmarshal(raw, &request) != nil || request.RequestID == 0 || (request.ChatIsChannel && (request.ChatIsForum != nil || request.BotIsMember)) {
			b.Unsupported = true
		} else {
			b.Kind, b.RequestID, b.ChatIsChannel, b.RequestTitle, b.RequestUsername, b.RequestPhoto = "request_chat", request.RequestID, request.ChatIsChannel, request.RequestTitle, request.RequestUsername, request.RequestPhoto
			b.RequestPeerFilter = &domain.BotRequestPeerFilter{
				ChatIsCreated: request.ChatIsCreated, BotIsMember: request.BotIsMember,
				UserAdminRights: domainRequestAdminRights(request.UserAdministratorRights),
				BotAdminRights:  domainRequestAdminRights(request.BotAdministratorRights),
			}
			if request.ChatIsForum != nil {
				b.RequestPeerFilter.ChatIsForumSet, b.RequestPeerFilter.ChatIsForum = true, *request.ChatIsForum
			}
			if request.ChatHasUsername != nil {
				b.RequestPeerFilter.ChatHasUsernameSet, b.RequestPeerFilter.ChatHasUsername = true, *request.ChatHasUsername
			}
			actions++
		}
	}
	if raw, ok := fields["web_app"]; ok {
		var app struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(raw, &app) != nil {
			b.Unsupported = true
		} else {
			b.Kind, b.WebAppURL = "web_app", app.URL
			actions++
		}
	}
	if actions == 0 {
		b.Kind = "text"
	}
	if actions > 1 {
		b.Unsupported = true
	}
	for key := range fields {
		switch key {
		case "text", "style", "icon_custom_emoji_id", "request_contact", "request_location", "request_poll", "request_users", "request_chat", "web_app":
		default:
			b.Unsupported = true
		}
	}
	return nil
}

type apiReplyKeyboardRemove struct {
	RemoveKeyboard bool `json:"remove_keyboard"`
	Selective      bool `json:"selective"`
}

type apiForceReply struct {
	ForceReply            bool   `json:"force_reply"`
	InputFieldPlaceholder string `json:"input_field_placeholder"`
	Selective             bool   `json:"selective"`
}

type apiInlineKeyboardButton struct {
	Text                    string
	URL                     string
	URLSet                  bool
	CallbackData            string
	CallbackDataSet         bool
	Style                   string
	IconCustomEmojiID       string
	IconCustomEmojiIDSet    bool
	Unsupported             bool
	WebAppURL               string
	WebAppSet               bool
	SwitchInlineQuery       string
	SwitchInlineSet         bool
	SwitchInlineSamePeer    bool
	SwitchInlinePeerTypes   []string
	CopyText                string
	CopyTextSet             bool
	LoginURL                string
	LoginForwardText        string
	LoginBotUsername        string
	LoginRequestWriteAccess bool
	LoginURLSet             bool
}

func (b *apiInlineKeyboardButton) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	text, ok := fields["text"]
	if !ok || json.Unmarshal(text, &b.Text) != nil {
		return errors.New("invalid inline keyboard button text")
	}
	if raw, ok := fields["url"]; ok {
		b.URLSet = true
		if err := json.Unmarshal(raw, &b.URL); err != nil {
			return err
		}
	}
	if raw, ok := fields["callback_data"]; ok {
		b.CallbackDataSet = true
		if err := json.Unmarshal(raw, &b.CallbackData); err != nil {
			return err
		}
	}
	if raw, ok := fields["web_app"]; ok {
		b.WebAppSet = true
		var app struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(raw, &app) != nil {
			return errors.New("invalid web app")
		}
		b.WebAppURL = app.URL
	}
	if raw, ok := fields["login_url"]; ok {
		b.LoginURLSet = true
		var login struct {
			URL                string `json:"url"`
			ForwardText        string `json:"forward_text"`
			BotUsername        string `json:"bot_username"`
			RequestWriteAccess bool   `json:"request_write_access"`
		}
		if json.Unmarshal(raw, &login) != nil {
			return errors.New("invalid login url")
		}
		b.LoginURL, b.LoginForwardText = login.URL, login.ForwardText
		b.LoginBotUsername, b.LoginRequestWriteAccess = login.BotUsername, login.RequestWriteAccess
	}
	switchActions := 0
	if raw, ok := fields["switch_inline_query"]; ok {
		switchActions++
		b.SwitchInlineSet = true
		if json.Unmarshal(raw, &b.SwitchInlineQuery) != nil {
			return errors.New("invalid switch inline query")
		}
	}
	if raw, ok := fields["switch_inline_query_current_chat"]; ok {
		switchActions++
		b.SwitchInlineSet, b.SwitchInlineSamePeer = true, true
		if json.Unmarshal(raw, &b.SwitchInlineQuery) != nil {
			return errors.New("invalid switch inline query")
		}
	}
	if raw, ok := fields["switch_inline_query_chosen_chat"]; ok {
		switchActions++
		b.SwitchInlineSet = true
		var chosen struct {
			Query             string `json:"query"`
			AllowUserChats    bool   `json:"allow_user_chats"`
			AllowBotChats     bool   `json:"allow_bot_chats"`
			AllowGroupChats   bool   `json:"allow_group_chats"`
			AllowChannelChats bool   `json:"allow_channel_chats"`
		}
		if json.Unmarshal(raw, &chosen) != nil {
			return errors.New("invalid switch inline query")
		}
		b.SwitchInlineQuery = chosen.Query
		if chosen.AllowUserChats {
			b.SwitchInlinePeerTypes = append(b.SwitchInlinePeerTypes, store.InlineQueryPeerTypePM)
		}
		if chosen.AllowBotChats {
			b.SwitchInlinePeerTypes = append(b.SwitchInlinePeerTypes, store.InlineQueryPeerTypeBotPM)
		}
		if chosen.AllowGroupChats {
			b.SwitchInlinePeerTypes = append(b.SwitchInlinePeerTypes, store.InlineQueryPeerTypeChat, store.InlineQueryPeerTypeMegagroup)
		}
		if chosen.AllowChannelChats {
			b.SwitchInlinePeerTypes = append(b.SwitchInlinePeerTypes, store.InlineQueryPeerTypeBroadcast)
		}
	}
	if switchActions > 1 {
		b.Unsupported = true
	}
	if raw, ok := fields["copy_text"]; ok {
		b.CopyTextSet = true
		var copy struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &copy) != nil {
			return errors.New("invalid copy text")
		}
		b.CopyText = copy.Text
	}
	if raw, ok := fields["style"]; ok {
		if err := json.Unmarshal(raw, &b.Style); err != nil {
			return err
		}
	}
	if raw, ok := fields["icon_custom_emoji_id"]; ok {
		b.IconCustomEmojiIDSet = true
		if err := json.Unmarshal(raw, &b.IconCustomEmojiID); err != nil {
			return err
		}
	}
	for key := range fields {
		switch key {
		case "text", "url", "callback_data", "web_app", "login_url", "switch_inline_query", "switch_inline_query_current_chat", "switch_inline_query_chosen_chat", "copy_text", "style", "icon_custom_emoji_id":
		default:
			b.Unsupported = true
		}
	}
	return nil
}
