package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// validateReplyMarkupForPeer enforces the Bot API/TL boundary that reply keyboards control
// a chat input field and are not supported in broadcast channels. Inline keyboards remain
// valid in both megagroups and broadcasts.
func (r *Router) validateReplyMarkupForPeer(ctx context.Context, userID int64, peer domain.Peer, markup *domain.MessageReplyMarkup) error {
	if err := r.prepareTelegramLoginMarkup(ctx, userID, markup); err != nil {
		return replyMarkupErr(err)
	}
	if markup == nil || !markup.IsReplyKeyboardFamily() || peer.Type != domain.PeerTypeChannel {
		return nil
	}
	if r == nil || r.deps.Channels == nil {
		return channelInvalidErr(domain.ErrChannelInvalid)
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, peer.ID)
	if err != nil {
		return channelInvalidErr(err)
	}
	if view.Channel.Broadcast && !view.Channel.Megagroup {
		return replyMarkupInvalidErr()
	}
	return nil
}

// prepareTelegramLoginMarkup resolves every login_url target and validates its
// linked web origin before persistence. It mutates only the freshly parsed
// request DTO and assigns a deterministic flattened button id, which is later
// re-read by messages.requestUrlAuth.
func (r *Router) prepareTelegramLoginMarkup(ctx context.Context, senderBotID int64, markup *domain.MessageReplyMarkup) error {
	if markup == nil || markup.Kind() != domain.MessageReplyMarkupInline {
		return nil
	}
	hasLoginButton := false
	for rowIndex := range markup.Inline {
		for buttonIndex := range markup.Inline[rowIndex] {
			if markup.Inline[rowIndex][buttonIndex].Type == domain.MarkupButtonLoginURL {
				hasLoginButton = true
				break
			}
		}
		if hasLoginButton {
			break
		}
	}
	if !hasLoginButton {
		return nil
	}
	if r == nil || r.deps.TelegramLogin == nil || r.deps.Users == nil || senderBotID <= 0 {
		return domain.ErrButtonTypeInvalid
	}
	sender, found, err := r.deps.Users.ByID(ctx, senderBotID, senderBotID)
	if err != nil {
		return err
	}
	if !found || !sender.Bot || sender.Deleted {
		return domain.ErrButtonTypeInvalid
	}
	flatID := 0
	for rowIndex := range markup.Inline {
		for buttonIndex := range markup.Inline[rowIndex] {
			button := &markup.Inline[rowIndex][buttonIndex]
			if button.Type != domain.MarkupButtonLoginURL {
				flatID++
				continue
			}
			botID := button.LoginBotUserID
			if button.LoginBotUsername != "" {
				resolver, ok := r.deps.Users.(UserIdentityService)
				if !ok {
					return domain.ErrButtonInvalid
				}
				bot, found, err := resolver.ResolveUsername(ctx, senderBotID, strings.TrimPrefix(button.LoginBotUsername, "@"))
				if err != nil {
					return err
				}
				if !found || !bot.Bot || bot.Deleted {
					return domain.ErrButtonInvalid
				}
				botID = bot.ID
			}
			if botID == 0 {
				botID = senderBotID
			}
			bot, found, err := r.deps.Users.ByID(ctx, senderBotID, botID)
			if err != nil {
				return err
			}
			if !found || !bot.Bot || bot.Deleted {
				return domain.ErrButtonInvalid
			}
			normalized, _, err := r.deps.TelegramLogin.ValidateMessageButton(ctx, botID, button.URL)
			if err != nil {
				if errors.Is(err, domain.ErrTelegramLoginURLInvalid) || errors.Is(err, domain.ErrTelegramLoginOriginNotAllowed) {
					return domain.ErrButtonURLInvalid
				}
				if errors.Is(err, domain.ErrTelegramLoginClientDisabled) {
					return domain.ErrButtonInvalid
				}
				return err
			}
			button.URL = normalized
			button.LoginBotUserID = botID
			button.LoginBotUsername = ""
			button.ButtonID = flatID
			flatID++
		}
	}
	return domain.ValidateReplyMarkup(markup)
}

// P3 reply_markup 错误码（对齐官方）。
func buttonDataInvalidErr() error { return tgerr.New(400, "BUTTON_DATA_INVALID") }
func buttonInvalidErr() error     { return tgerr.New(400, "BUTTON_INVALID") }
func buttonTypeInvalidErr() error { return tgerr.New(400, "BUTTON_TYPE_INVALID") }
func buttonURLInvalidErr() error  { return tgerr.New(400, "BUTTON_URL_INVALID") }

// replyMarkupErr 把 domain 校验错误映射为客户端错误码。
func replyMarkupErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrButtonDataInvalid):
		return buttonDataInvalidErr()
	case errors.Is(err, domain.ErrButtonURLInvalid):
		return buttonURLInvalidErr()
	case errors.Is(err, domain.ErrButtonTypeInvalid):
		return buttonTypeInvalidErr()
	case errors.Is(err, domain.ErrButtonInvalid):
		return buttonInvalidErr()
	default:
		return replyMarkupInvalidErr()
	}
}

// domainReplyMarkupForSender 解析只能携带 inline keyboard 的入站 reply_markup（inline
// result / edit 路径）。普通消息发送使用 domainOutgoingReplyMarkupForSender。
// 语义：
//   - 仅 bot 账号下发的 markup 被接受；非 bot 一律丢弃（返回 nil，不报错——对齐
//     官方「普通用户 markup 无效」，I1）。
//   - 仅 ReplyInlineMarkup 被处理；bot 的 reply keyboard 家族在这些上下文中显式拒绝。
//   - inline 行内按钮仅 callback / url；其它按钮类型（webview/game/url_auth/
//     request_* 等）→ ErrButtonTypeInvalid（拒绝整条发送，绝不半实现下发）。
//   - data≤64B、行/按钮上限、url https 由 domain.ValidateReplyMarkup 校验。
func domainReplyMarkupForSender(markup tg.ReplyMarkupClass, senderIsBot bool) (*domain.MessageReplyMarkup, error) {
	if markup == nil || !senderIsBot {
		return nil, nil
	}
	inline, ok := markup.(*tg.ReplyInlineMarkup)
	if !ok {
		return nil, domain.ErrButtonTypeInvalid
	}
	parsed, err := domainInlineMarkup(inline)
	if err != nil {
		return nil, err
	}
	if parsed.IsZero() {
		return nil, nil
	}
	if err := domain.ValidateReplyMarkup(parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// domainOutgoingReplyMarkupForSender 解析普通 sendMessage/sendMedia 的完整 reply markup。
// 非 bot 携带 markup 仍按官方权限边界静默丢弃；bot 的未知/未实现按钮则拒绝整条消息，
// 避免客户端看到一个被服务端悄悄改形的键盘。
func domainOutgoingReplyMarkupForSender(markup tg.ReplyMarkupClass, senderIsBot bool) (*domain.MessageReplyMarkup, error) {
	if markup == nil || !senderIsBot {
		return nil, nil
	}
	switch v := markup.(type) {
	case *tg.ReplyInlineMarkup:
		return domainReplyMarkupForSender(v, true)
	case *tg.ReplyKeyboardMarkup:
		out := &domain.MessageReplyMarkup{
			Type:        domain.MessageReplyMarkupKeyboard,
			Keyboard:    make([][]domain.MarkupButton, 0, len(v.Rows)),
			Resize:      v.Resize,
			SingleUse:   v.SingleUse,
			Selective:   v.Selective,
			Persistent:  v.Persistent,
			Placeholder: v.Placeholder,
		}
		for _, row := range v.Rows {
			domainRow := make([]domain.MarkupButton, 0, len(row.Buttons))
			for _, button := range row.Buttons {
				parsed, err := domainReplyKeyboardButton(button)
				if err != nil {
					return nil, err
				}
				domainRow = append(domainRow, parsed)
			}
			out.Keyboard = append(out.Keyboard, domainRow)
		}
		if err := domain.ValidateReplyMarkup(out); err != nil {
			return nil, err
		}
		return out, nil
	case *tg.ReplyKeyboardHide:
		out := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupHide, Selective: v.Selective}
		if err := domain.ValidateReplyMarkup(out); err != nil {
			return nil, err
		}
		return out, nil
	case *tg.ReplyKeyboardForceReply:
		out := &domain.MessageReplyMarkup{
			Type:        domain.MessageReplyMarkupForceReply,
			SingleUse:   v.SingleUse,
			Selective:   v.Selective,
			Placeholder: v.Placeholder,
		}
		if err := domain.ValidateReplyMarkup(out); err != nil {
			return nil, err
		}
		return out, nil
	default:
		return nil, domain.ErrButtonTypeInvalid
	}
}

func domainReplyKeyboardButton(button tg.KeyboardButtonClass) (domain.MarkupButton, error) {
	style, icon, err := domainMarkupButtonStyle(button)
	if err != nil {
		return domain.MarkupButton{}, err
	}
	base := domain.MarkupButton{Style: style, IconCustomEmojiID: icon}
	switch b := button.(type) {
	case *tg.KeyboardButton:
		base.Type, base.Text = domain.MarkupButtonText, b.Text
	case *tg.KeyboardButtonRequestPhone:
		base.Type, base.Text = domain.MarkupButtonRequestPhone, b.Text
	case *tg.KeyboardButtonRequestGeoLocation:
		base.Type, base.Text = domain.MarkupButtonRequestLocation, b.Text
	case *tg.KeyboardButtonRequestPoll:
		base.Type, base.Text = domain.MarkupButtonRequestPoll, b.Text
		if quiz, ok := b.GetQuiz(); ok {
			if quiz {
				base.PollType = "quiz"
			} else {
				base.PollType = "regular"
			}
		}
	case *tg.KeyboardButtonRequestPeer:
		base.Type, base.Text = domain.MarkupButtonRequestPeer, b.Text
		base.ButtonID, base.MaxQuantity = b.ButtonID, b.MaxQuantity
		base.RequestPeerType, base.RequestPeerFilter = domainRequestPeerFilter(b.PeerType)
	case *tg.KeyboardButtonSimpleWebView:
		base.Type, base.Text, base.URL = domain.MarkupButtonSimpleWebView, b.Text, b.URL
	default:
		return domain.MarkupButton{}, domain.ErrButtonTypeInvalid
	}
	return base, nil
}

func domainInlineMarkup(inline *tg.ReplyInlineMarkup) (*domain.MessageReplyMarkup, error) {
	out := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: make([][]domain.MarkupButton, 0, len(inline.Rows))}
	buttonID := 0
	for _, row := range inline.Rows {
		domainRow := make([]domain.MarkupButton, 0, len(row.Buttons))
		for _, btn := range row.Buttons {
			db, err := domainMarkupButton(btn, buttonID)
			if err != nil {
				return nil, err
			}
			domainRow = append(domainRow, db)
			buttonID++
		}
		out.Inline = append(out.Inline, domainRow)
	}
	return out, nil
}

func domainMarkupButton(btn tg.KeyboardButtonClass, buttonID int) (domain.MarkupButton, error) {
	style, icon, err := domainMarkupButtonStyle(btn)
	if err != nil {
		return domain.MarkupButton{}, err
	}
	switch b := btn.(type) {
	case *tg.KeyboardButtonCallback:
		return domain.MarkupButton{
			Type:              domain.MarkupButtonCallback,
			Text:              b.Text,
			Style:             style,
			IconCustomEmojiID: icon,
			Data:              append([]byte(nil), b.Data...),
			RequiresPassword:  b.RequiresPassword,
		}, nil
	case *tg.KeyboardButtonURL:
		return domain.MarkupButton{
			Type: domain.MarkupButtonURL, Text: b.Text, URL: b.URL,
			Style: style, IconCustomEmojiID: icon,
		}, nil
	case *tg.InputKeyboardButtonURLAuth:
		botUserID := int64(0)
		switch bot := b.Bot.(type) {
		case nil, *tg.InputUserEmpty, *tg.InputUserSelf:
		case *tg.InputUser:
			botUserID = bot.UserID
		default:
			return domain.MarkupButton{}, domain.ErrButtonInvalid
		}
		return domain.MarkupButton{
			Type: domain.MarkupButtonLoginURL, Text: b.Text, URL: b.URL,
			ForwardText: b.FwdText, ButtonID: buttonID, LoginBotUserID: botUserID,
			RequestWriteAccess: b.RequestWriteAccess, Style: style, IconCustomEmojiID: icon,
		}, nil
	case *tg.KeyboardButtonURLAuth:
		return domain.MarkupButton{
			Type: domain.MarkupButtonLoginURL, Text: b.Text, URL: b.URL,
			ForwardText: b.FwdText, ButtonID: b.ButtonID,
			Style: style, IconCustomEmojiID: icon,
		}, nil
	case *tg.KeyboardButtonWebView:
		return domain.MarkupButton{Type: domain.MarkupButtonWebView, Text: b.Text, URL: b.URL, Style: style, IconCustomEmojiID: icon}, nil
	case *tg.KeyboardButtonSwitchInline:
		peerTypes, err := preparedInlinePeerTypesFromTG(b.PeerTypes)
		if err != nil {
			return domain.MarkupButton{}, domain.ErrButtonInvalid
		}
		return domain.MarkupButton{Type: domain.MarkupButtonSwitchInline, Text: b.Text, Query: b.Query, SamePeer: b.SamePeer, PeerTypes: peerTypes, Style: style, IconCustomEmojiID: icon}, nil
	case *tg.KeyboardButtonCopy:
		return domain.MarkupButton{Type: domain.MarkupButtonCopy, Text: b.Text, CopyText: b.CopyText, Style: style, IconCustomEmojiID: icon}, nil
	default:
		// webview/game/url_auth/request_*/switch_inline/buy 等 P3 未实现按钮类型。
		return domain.MarkupButton{}, domain.ErrButtonTypeInvalid
	}
}

func domainMarkupButtonStyle(btn tg.KeyboardButtonClass) (domain.MarkupButtonStyle, int64, error) {
	style, ok := btn.GetStyle()
	if !ok {
		return "", 0, nil
	}
	colors := 0
	var out domain.MarkupButtonStyle
	if style.GetBgPrimary() {
		colors++
		out = domain.MarkupButtonStylePrimary
	}
	if style.GetBgDanger() {
		colors++
		out = domain.MarkupButtonStyleDanger
	}
	if style.GetBgSuccess() {
		colors++
		out = domain.MarkupButtonStyleSuccess
	}
	icon, hasIcon := style.GetIcon()
	if colors > 1 || (hasIcon && icon <= 0) || (colors == 0 && !hasIcon) {
		return "", 0, domain.ErrButtonInvalid
	}
	return out, icon, nil
}

func tgMarkupButtonStyle(btn domain.MarkupButton) (tg.KeyboardButtonStyle, bool) {
	var out tg.KeyboardButtonStyle
	switch btn.Style {
	case domain.MarkupButtonStylePrimary:
		out.SetBgPrimary(true)
	case domain.MarkupButtonStyleDanger:
		out.SetBgDanger(true)
	case domain.MarkupButtonStyleSuccess:
		out.SetBgSuccess(true)
	}
	if btn.IconCustomEmojiID > 0 {
		out.SetIcon(btn.IconCustomEmojiID)
	}
	return out, btn.Style != "" || btn.IconCustomEmojiID > 0
}

// tgReplyMarkup 把存储的协议中立快照还原为对应 ReplyMarkup constructor。
func tgReplyMarkup(m *domain.MessageReplyMarkup) tg.ReplyMarkupClass {
	if m.IsZero() {
		return nil
	}
	switch m.Kind() {
	case domain.MessageReplyMarkupKeyboard:
		rows := make([]tg.KeyboardButtonRow, 0, len(m.Keyboard))
		for _, row := range m.Keyboard {
			buttons := make([]tg.KeyboardButtonClass, 0, len(row))
			for _, btn := range row {
				buttons = append(buttons, tgReplyKeyboardButton(btn))
			}
			rows = append(rows, tg.KeyboardButtonRow{Buttons: buttons})
		}
		return &tg.ReplyKeyboardMarkup{
			Resize:      m.Resize,
			SingleUse:   m.SingleUse,
			Selective:   m.Selective,
			Persistent:  m.Persistent,
			Rows:        rows,
			Placeholder: m.Placeholder,
		}
	case domain.MessageReplyMarkupHide:
		return &tg.ReplyKeyboardHide{Selective: m.Selective}
	case domain.MessageReplyMarkupForceReply:
		return &tg.ReplyKeyboardForceReply{
			SingleUse:   m.SingleUse,
			Selective:   m.Selective,
			Placeholder: m.Placeholder,
		}
	case domain.MessageReplyMarkupInline:
		// Continue below.
	default:
		return nil
	}
	rows := make([]tg.KeyboardButtonRow, 0, len(m.Inline))
	for _, row := range m.Inline {
		buttons := make([]tg.KeyboardButtonClass, 0, len(row))
		for _, btn := range row {
			buttons = append(buttons, tgMarkupButton(btn))
		}
		rows = append(rows, tg.KeyboardButtonRow{Buttons: buttons})
	}
	return &tg.ReplyInlineMarkup{Rows: rows}
}

func tgMarkupButton(btn domain.MarkupButton) tg.KeyboardButtonClass {
	switch btn.Type {
	case domain.MarkupButtonURL:
		out := &tg.KeyboardButtonURL{Text: btn.Text, URL: btn.URL}
		if style, ok := tgMarkupButtonStyle(btn); ok {
			out.SetStyle(style)
		}
		return out
	case domain.MarkupButtonLoginURL:
		out := &tg.KeyboardButtonURLAuth{Text: btn.Text, URL: btn.URL, ButtonID: btn.ButtonID}
		if btn.ForwardText != "" {
			out.SetFwdText(btn.ForwardText)
		}
		if style, ok := tgMarkupButtonStyle(btn); ok {
			out.SetStyle(style)
		}
		return out
	case domain.MarkupButtonWebView:
		out := &tg.KeyboardButtonWebView{Text: btn.Text, URL: btn.URL}
		if style, ok := tgMarkupButtonStyle(btn); ok {
			out.SetStyle(style)
		}
		return out
	case domain.MarkupButtonSwitchInline:
		out := &tg.KeyboardButtonSwitchInline{Text: btn.Text, Query: btn.Query, SamePeer: btn.SamePeer}
		if len(btn.PeerTypes) > 0 {
			out.SetPeerTypes(tgPreparedInlinePeerTypes(btn.PeerTypes))
		}
		if style, ok := tgMarkupButtonStyle(btn); ok {
			out.SetStyle(style)
		}
		return out
	case domain.MarkupButtonCopy:
		out := &tg.KeyboardButtonCopy{Text: btn.Text, CopyText: btn.CopyText}
		if style, ok := tgMarkupButtonStyle(btn); ok {
			out.SetStyle(style)
		}
		return out
	default: // callback
		out := &tg.KeyboardButtonCallback{Text: btn.Text, Data: btn.Data}
		if btn.RequiresPassword {
			out.SetRequiresPassword(true)
		}
		if style, ok := tgMarkupButtonStyle(btn); ok {
			out.SetStyle(style)
		}
		return out
	}
}

func tgReplyKeyboardButton(btn domain.MarkupButton) tg.KeyboardButtonClass {
	var out tg.KeyboardButtonClass
	switch btn.Type {
	case domain.MarkupButtonRequestPhone:
		out = &tg.KeyboardButtonRequestPhone{Text: btn.Text}
	case domain.MarkupButtonRequestLocation:
		out = &tg.KeyboardButtonRequestGeoLocation{Text: btn.Text}
	case domain.MarkupButtonRequestPoll:
		button := &tg.KeyboardButtonRequestPoll{Text: btn.Text}
		if btn.PollType == "quiz" {
			button.SetQuiz(true)
		} else if btn.PollType == "regular" {
			button.SetQuiz(false)
		}
		out = button
	case domain.MarkupButtonRequestPeer:
		out = &tg.KeyboardButtonRequestPeer{Text: btn.Text, ButtonID: btn.ButtonID, PeerType: tgRequestPeerTypeWithFilter(btn.RequestPeerType, btn.RequestPeerFilter), MaxQuantity: btn.MaxQuantity}
	case domain.MarkupButtonSimpleWebView:
		out = &tg.KeyboardButtonSimpleWebView{Text: btn.Text, URL: btn.URL}
	default:
		out = &tg.KeyboardButton{Text: btn.Text}
	}
	if style, ok := tgMarkupButtonStyle(btn); ok {
		if setter, ok := out.(interface{ SetStyle(tg.KeyboardButtonStyle) }); ok {
			setter.SetStyle(style)
		}
	}
	return out
}

func domainRequestPeerFilter(peerType tg.RequestPeerTypeClass) (string, *domain.BotRequestPeerFilter) {
	filter := &domain.BotRequestPeerFilter{}
	switch v := peerType.(type) {
	case *tg.RequestPeerTypeUser:
		if value, ok := v.GetBot(); ok {
			filter.UserIsBotSet, filter.UserIsBot = true, value
		}
		if value, ok := v.GetPremium(); ok {
			filter.UserIsPremiumSet, filter.UserIsPremium = true, value
		}
		if !filter.UserIsBotSet && !filter.UserIsPremiumSet {
			return "user", nil
		}
		return "user", filter
	case *tg.RequestPeerTypeChat:
		filter.ChatIsCreated, filter.BotIsMember = v.Creator, v.BotParticipant
		if value, ok := v.GetHasUsername(); ok {
			filter.ChatHasUsernameSet, filter.ChatHasUsername = true, value
		}
		if value, ok := v.GetForum(); ok {
			filter.ChatIsForumSet, filter.ChatIsForum = true, value
		}
		if rights, ok := v.GetUserAdminRights(); ok {
			mapped := domainBotRequestAdminRights(rights)
			filter.UserAdminRights = &mapped
		}
		if rights, ok := v.GetBotAdminRights(); ok {
			mapped := domainBotRequestAdminRights(rights)
			filter.BotAdminRights = &mapped
		}
		if botRequestPeerFilterZero(filter) {
			return "chat", nil
		}
		return "chat", filter
	case *tg.RequestPeerTypeBroadcast:
		filter.ChatIsCreated = v.Creator
		if value, ok := v.GetHasUsername(); ok {
			filter.ChatHasUsernameSet, filter.ChatHasUsername = true, value
		}
		if rights, ok := v.GetUserAdminRights(); ok {
			mapped := domainBotRequestAdminRights(rights)
			filter.UserAdminRights = &mapped
		}
		if rights, ok := v.GetBotAdminRights(); ok {
			mapped := domainBotRequestAdminRights(rights)
			filter.BotAdminRights = &mapped
		}
		if botRequestPeerFilterZero(filter) {
			return "broadcast", nil
		}
		return "broadcast", filter
	default:
		return "", nil
	}
}

func botRequestPeerFilterZero(filter *domain.BotRequestPeerFilter) bool {
	return filter == nil || (!filter.UserIsBotSet && !filter.UserIsPremiumSet && !filter.ChatHasUsernameSet &&
		!filter.ChatIsForumSet && !filter.ChatIsCreated && !filter.BotIsMember && filter.UserAdminRights == nil && filter.BotAdminRights == nil)
}

func tgRequestPeerTypeWithFilter(kind string, filter *domain.BotRequestPeerFilter) tg.RequestPeerTypeClass {
	switch kind {
	case "chat":
		out := &tg.RequestPeerTypeChat{}
		if filter != nil {
			out.Creator, out.BotParticipant = filter.ChatIsCreated, filter.BotIsMember
			if filter.ChatHasUsernameSet {
				out.SetHasUsername(filter.ChatHasUsername)
			}
			if filter.ChatIsForumSet {
				out.SetForum(filter.ChatIsForum)
			}
			if filter.UserAdminRights != nil {
				out.SetUserAdminRights(tgBotRequestAdminRights(*filter.UserAdminRights))
			}
			if filter.BotAdminRights != nil {
				out.SetBotAdminRights(tgBotRequestAdminRights(*filter.BotAdminRights))
			}
		}
		return out
	case "broadcast":
		out := &tg.RequestPeerTypeBroadcast{}
		if filter != nil {
			out.Creator = filter.ChatIsCreated
			if filter.ChatHasUsernameSet {
				out.SetHasUsername(filter.ChatHasUsername)
			}
			if filter.UserAdminRights != nil {
				out.SetUserAdminRights(tgBotRequestAdminRights(*filter.UserAdminRights))
			}
			if filter.BotAdminRights != nil {
				out.SetBotAdminRights(tgBotRequestAdminRights(*filter.BotAdminRights))
			}
		}
		return out
	default:
		out := &tg.RequestPeerTypeUser{}
		if filter != nil {
			if filter.UserIsBotSet {
				out.SetBot(filter.UserIsBot)
			}
			if filter.UserIsPremiumSet {
				out.SetPremium(filter.UserIsPremium)
			}
		}
		return out
	}
}

func domainBotRequestAdminRights(rights tg.ChatAdminRights) domain.BotRequestAdminRights {
	return domain.BotRequestAdminRights{
		Anonymous: rights.Anonymous, ManageChat: rights.Other, DeleteMessages: rights.DeleteMessages,
		ManageVideoChats: rights.ManageCall, RestrictMembers: rights.BanUsers, PromoteMembers: rights.AddAdmins,
		ChangeInfo: rights.ChangeInfo, InviteUsers: rights.InviteUsers, PostStories: rights.PostStories,
		EditStories: rights.EditStories, DeleteStories: rights.DeleteStories, PostMessages: rights.PostMessages,
		EditMessages: rights.EditMessages, PinMessages: rights.PinMessages, ManageTopics: rights.ManageTopics,
		ManageDirectMessages: rights.ManageDirectMessages,
	}
}

func tgBotRequestAdminRights(rights domain.BotRequestAdminRights) tg.ChatAdminRights {
	return tg.ChatAdminRights{
		Anonymous: rights.Anonymous, Other: rights.ManageChat, DeleteMessages: rights.DeleteMessages,
		ManageCall: rights.ManageVideoChats, BanUsers: rights.RestrictMembers, AddAdmins: rights.PromoteMembers,
		ChangeInfo: rights.ChangeInfo, InviteUsers: rights.InviteUsers, PostStories: rights.PostStories,
		EditStories: rights.EditStories, DeleteStories: rights.DeleteStories, PostMessages: rights.PostMessages,
		EditMessages: rights.EditMessages, PinMessages: rights.PinMessages, ManageTopics: rights.ManageTopics,
		ManageDirectMessages: rights.ManageDirectMessages,
	}
}
