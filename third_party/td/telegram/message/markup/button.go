package markup

import "github.com/iamxvbaba/td/tg"

// StyleOption is a functional parameter that configures the tg.KeyboardButtonStyle
// of a button, allowing a custom background color and a custom emoji label.
//
// See https://core.telegram.org/api/bots/buttons#button-styles.
type StyleOption func(s *tg.KeyboardButtonStyle)

// applyStyle builds a tg.KeyboardButtonStyle from the given options.
func applyStyle(options []StyleOption) tg.KeyboardButtonStyle {
	var style tg.KeyboardButtonStyle
	for _, opt := range options {
		opt(&style)
	}
	return style
}

// StyleBgPrimary sets a dark blue background color, recommended for main actions.
func StyleBgPrimary() StyleOption {
	return func(s *tg.KeyboardButtonStyle) {
		s.BgPrimary = true
	}
}

// StyleBgDanger sets a red background color, recommended for destructive actions.
func StyleBgDanger() StyleOption {
	return func(s *tg.KeyboardButtonStyle) {
		s.BgDanger = true
	}
}

// StyleBgSuccess sets a green background color, recommended for positive actions.
func StyleBgSuccess() StyleOption {
	return func(s *tg.KeyboardButtonStyle) {
		s.BgSuccess = true
	}
}

// StyleIcon sets the ID of a custom emoji to be displayed before the button's label.
//
// See https://core.telegram.org/api/custom-emoji.
func StyleIcon(icon int64) StyleOption {
	return func(s *tg.KeyboardButtonStyle) {
		s.SetIcon(icon)
	}
}

// Row creates keyboard row.
func Row(buttons ...tg.KeyboardButton) tg.KeyboardButtonRow {
	return tg.KeyboardButtonRow{
		Buttons: buttons,
	}
}

// Button creates new plain text button.
func Button(text string, style ...StyleOption) tg.KeyboardButton {
	return tg.KeyboardButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.ButtonTypeDefault{},
	}
}

// URL creates new URL button.
func URL(text, url string, style ...StyleOption) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.InlineButtonTypeURL{URL: url},
	}
}

// Callback creates new callback button.
func Callback(text string, data []byte, style ...StyleOption) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.InlineButtonTypeCallback{Data: data},
	}
}

// RequestPhone creates button to request a user's phone number.
func RequestPhone(text string, style ...StyleOption) tg.KeyboardButton {
	return tg.KeyboardButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.ButtonTypeRequestPhone{},
	}
}

// RequestGeoLocation creates button to request a user's geo location.
func RequestGeoLocation(text string, style ...StyleOption) tg.KeyboardButton {
	return tg.KeyboardButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.ButtonTypeRequestGeoLocation{},
	}
}

// SwitchInline creates button to force a user to switch to inline mode.
// Pressing the button will prompt the user to select one of their chats, open that chat and insert the bot‘s username
// and the specified inline query in the input field.
//
// If samePeer set, pressing the button will insert the bot‘s
// username and the specified inline query in the current chat's input field.
func SwitchInline(text, query string, samePeer bool, style ...StyleOption) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text:  text,
		Style: applyStyle(style),
		Type: &tg.InlineButtonTypeSwitchInline{
			SamePeer: samePeer,
			Query:    query,
		},
	}
}

// Game creates button to start a game.
func Game(text string, style ...StyleOption) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.InlineButtonTypeGame{},
	}
}

// Buy creates button to buy a product.
func Buy(text string, style ...StyleOption) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.InlineButtonTypeBuy{},
	}
}

// InputURLAuth creates button to request a user to authorize via URL using Seamless Telegram Login.
// Can only be sent or received as part of an inline keyboard, use URLAuth for reply keyboards.
func InputURLAuth(requestWriteAccess bool, text, fwdText, url string, bot tg.InputUserClass) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text: text,
		Type: &tg.InputInlineButtonTypeURLAuth{
			RequestWriteAccess: requestWriteAccess,
			FwdText:            fwdText,
			URL:                url,
			Bot:                bot,
		},
	}
}

// URLAuth creates button to request a user to authorize via URL using Seamless Telegram Login.
// Can only be sent or received as part of a reply keyboard, use InputURLAuth for inline keyboards.
func URLAuth(text, url string, buttonID int, fwdText string, style ...StyleOption) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text:  text,
		Style: applyStyle(style),
		Type: &tg.InlineButtonTypeURLAuth{
			URL:      url,
			ButtonID: buttonID,
			FwdText:  fwdText,
		},
	}
}

// RequestPoll creates button that allows the user to create and send a poll when pressed.
// Available only in private.
func RequestPoll(text string, quiz bool, style ...StyleOption) tg.KeyboardButton {
	return tg.KeyboardButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.ButtonTypeRequestPoll{Quiz: quiz},
	}
}

// InputUserProfile creates button that links directly to a user profile.
// Can only be sent or received as part of an inline keyboard, use UserProfile for reply keyboards.
func InputUserProfile(text string, user tg.InputUserClass) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text: text,
		Type: &tg.InputInlineButtonTypeUserProfile{UserID: user},
	}
}

// UserProfile creates button that links directly to a user profile.
// Can only be sent or received as part of a reply keyboard, use InputUserProfile for inline keyboards.
func UserProfile(text string, userID int64, style ...StyleOption) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.InlineButtonTypeUserProfile{UserID: userID},
	}
}

// WebView creates button to open a bot web app using messages.requestWebView, sending over user information after
// user confirmation.
// Can only be sent or received as part of an inline keyboard, use SimpleWebView for reply keyboards.
func WebView(text, url string, style ...StyleOption) tg.KeyboardInlineButton {
	return tg.KeyboardInlineButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.InlineButtonTypeWebView{URL: url},
	}
}

// SimpleWebView creates button to open a bot web app using messages.requestSimpleWebView, without sending user
// information to the web app.
// Can only be sent or received as part of a reply keyboard, use WebView for inline keyboards.
func SimpleWebView(text, url string, style ...StyleOption) tg.KeyboardButton {
	return tg.KeyboardButton{
		Text:  text,
		Style: applyStyle(style),
		Type:  &tg.ButtonTypeSimpleWebView{URL: url},
	}
}

// RequestPeer creates button that prompts the user to select and share a peer with the bot using
// messages.sendBotRequestedPeer.
func RequestPeer(text string, buttonID int, peerType tg.RequestPeerTypeClass, style ...StyleOption) tg.KeyboardButton {
	return tg.KeyboardButton{
		Text:  text,
		Style: applyStyle(style),
		Type: &tg.ButtonTypeRequestPeer{
			ButtonID: buttonID,
			PeerType: peerType,
		},
	}
}
