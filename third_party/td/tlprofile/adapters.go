package tlprofile

import (
	"fmt"

	"github.com/iamxvbaba/td/tg"
)

func layerCaptureChatInviteJoinQueryIDFromWebViewDecode(_ LayerProfile, value *tg.MessagesChatInviteJoinResultWebView, webview tg.WebViewResultURL) error {
	if value == nil {
		return fmt.Errorf("chat invite webview target is nil")
	}
	queryID, ok := webview.GetQueryID()
	if !ok {
		return fmt.Errorf("historical chat invite webview omitted its query ID")
	}
	value.QueryID = queryID
	return nil
}

func layerRequireCapturedChatInviteJoinQueryIDDecode(_ LayerProfile, value *tg.MessagesChatInviteJoinResultWebView) (int64, error) {
	if value == nil {
		return 0, fmt.Errorf("chat invite webview target is nil")
	}
	return value.QueryID, nil
}

func layerAdaptChatInviteJoinResultToUpdates(_ LayerProfile, result tg.MessagesChatInviteJoinResultClass) (tg.UpdatesClass, error) {
	value, ok := result.(*tg.MessagesChatInviteJoinResultOk)
	if !ok || value == nil || value.Updates == nil {
		return nil, fmt.Errorf("historical Updates result cannot represent chat invite result %T", result)
	}
	return value.Updates, nil
}

func layerAdaptUpdatesToChatInviteJoinResult(_ LayerProfile, updates tg.UpdatesClass) (tg.MessagesChatInviteJoinResultClass, error) {
	if updates == nil {
		return nil, fmt.Errorf("chat invite join returned nil historical Updates")
	}
	return &tg.MessagesChatInviteJoinResultOk{Updates: updates}, nil
}

func layerAdaptWebBrowserSettingsExceptionResult(_ LayerProfile, updates tg.UpdatesClass) (bool, error) {
	if updates == nil {
		return false, fmt.Errorf("web browser settings exception returned nil Updates")
	}
	return true, nil
}

func layerAdaptBotGuestChatResult(_ LayerProfile, messageID tg.InputBotInlineMessageIDClass) (bool, error) {
	switch value := messageID.(type) {
	case *tg.InputBotInlineMessageID:
		if value == nil {
			return false, fmt.Errorf("set bot guest chat result returned nil inline message ID")
		}
	case *tg.InputBotInlineMessageID64:
		if value == nil {
			return false, fmt.Errorf("set bot guest chat result returned nil inline message ID")
		}
	case nil:
		return false, fmt.Errorf("set bot guest chat result returned nil inline message ID")
	default:
		return false, fmt.Errorf("set bot guest chat result returned unsupported inline message ID %T", messageID)
	}
	return true, nil
}

// layerProjectEphemeralBotCommandProject removes a Layer 228 ephemeral command when
// encoding a command vector for a profile that cannot represent its privacy
// bit. Keeping the command and merely dropping the bit would make an older
// client send the command as an ordinary visible group message.
func layerProjectEphemeralBotCommandProject(
	profile LayerProfile,
	value *tg.BotCommand,
	ephemeral bool,
) (*tg.BotCommand, bool, error) {
	if value == nil {
		return nil, false, fmt.Errorf("bot command target is nil")
	}
	if profile >= LayerProfile228 {
		return nil, false, fmt.Errorf("ephemeral bot command projector used for profile %d", profile)
	}
	if ephemeral || value.Flags.Has(0) {
		return nil, false, nil
	}
	return value, true, nil
}

func layerLegacyProjectionMiss(profile LayerProfile, kind string, value any) error {
	return &LayerProjectionError{
		Profile: profile,
		Dropped: true,
		Reason:  fmt.Sprintf("%s does not match canonical payload %T", kind, value),
	}
}

func layerLegacyKeyboardButton(stylePresent bool, style tg.KeyboardButtonStyle, text string, buttonType tg.ButtonTypeClass) *tg.KeyboardButton {
	value := &tg.KeyboardButton{Text: text, Type: buttonType}
	if stylePresent {
		value.Flags.Set(10)
		value.Style = style
	}
	return value
}

func layerLegacyKeyboardButtonFields(value *tg.KeyboardButton) (bool, tg.KeyboardButtonStyle, string, error) {
	if value == nil || value.Type == nil {
		return false, tg.KeyboardButtonStyle{}, "", fmt.Errorf("legacy keyboard button is nil or has no type")
	}
	return value.Flags.Has(10) || !value.Style.Zero(), value.Style, value.Text, nil
}

func layerDefaultLegacyKeyboardButtonTypeDecode(_ LayerProfile, value *tg.KeyboardButton) (tg.ButtonTypeClass, error) {
	if value == nil {
		return nil, fmt.Errorf("legacy default keyboard button target is nil")
	}
	return &tg.ButtonTypeDefault{}, nil
}

func layerProjectLegacyDefaultKeyboardButtonProject(profile LayerProfile, value *tg.KeyboardButton, buttonType tg.ButtonTypeClass) (*tg.KeyboardButton, bool, error) {
	if value == nil || buttonType == nil {
		return nil, false, fmt.Errorf("legacy default keyboard button is nil or has no type")
	}
	if _, ok := buttonType.(*tg.ButtonTypeDefault); !ok {
		return nil, false, nil
	}
	return value, true, nil
}

func layerAdaptLegacyKeyboardButtonRequestPhoneDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text string) (*tg.KeyboardButton, error) {
	return layerLegacyKeyboardButton(stylePresent, style, text, &tg.ButtonTypeRequestPhone{}), nil
}

func layerAdaptLegacyKeyboardButtonRequestPhoneEncode(profile LayerProfile, value *tg.KeyboardButton) (bool, tg.KeyboardButtonStyle, string, error) {
	present, style, text, err := layerLegacyKeyboardButtonFields(value)
	if err != nil {
		return false, style, "", err
	}
	if _, ok := value.Type.(*tg.ButtonTypeRequestPhone); !ok {
		return false, style, "", layerLegacyProjectionMiss(profile, "request-phone button", value.Type)
	}
	return present, style, text, nil
}

func layerAdaptLegacyKeyboardButtonRequestGeoLocationDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text string) (*tg.KeyboardButton, error) {
	return layerLegacyKeyboardButton(stylePresent, style, text, &tg.ButtonTypeRequestGeoLocation{}), nil
}

func layerAdaptLegacyKeyboardButtonRequestGeoLocationEncode(profile LayerProfile, value *tg.KeyboardButton) (bool, tg.KeyboardButtonStyle, string, error) {
	present, style, text, err := layerLegacyKeyboardButtonFields(value)
	if err != nil {
		return false, style, "", err
	}
	if _, ok := value.Type.(*tg.ButtonTypeRequestGeoLocation); !ok {
		return false, style, "", layerLegacyProjectionMiss(profile, "request-location button", value.Type)
	}
	return present, style, text, nil
}

func layerAdaptLegacyKeyboardButtonRequestPollDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, quizPresent bool, quiz bool, text string) (*tg.KeyboardButton, error) {
	buttonType := &tg.ButtonTypeRequestPoll{}
	if quizPresent {
		buttonType.Flags.Set(0)
		buttonType.Quiz = quiz
	}
	return layerLegacyKeyboardButton(stylePresent, style, text, buttonType), nil
}

func layerAdaptLegacyKeyboardButtonRequestPollEncode(profile LayerProfile, value *tg.KeyboardButton) (bool, tg.KeyboardButtonStyle, bool, bool, string, error) {
	present, style, text, err := layerLegacyKeyboardButtonFields(value)
	if err != nil {
		return false, style, false, false, "", err
	}
	buttonType, ok := value.Type.(*tg.ButtonTypeRequestPoll)
	if !ok || buttonType == nil {
		return false, style, false, false, "", layerLegacyProjectionMiss(profile, "request-poll button", value.Type)
	}
	return present, style, buttonType.Flags.Has(0) || buttonType.Quiz, buttonType.Quiz, text, nil
}

func layerAdaptLegacyKeyboardButtonRequestPeerDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text string, buttonID int, peerType tg.RequestPeerTypeClass, maxQuantity int) (*tg.KeyboardButton, error) {
	return layerLegacyKeyboardButton(stylePresent, style, text, &tg.ButtonTypeRequestPeer{ButtonID: buttonID, PeerType: peerType, MaxQuantity: maxQuantity}), nil
}

func layerAdaptLegacyKeyboardButtonRequestPeerEncode(profile LayerProfile, value *tg.KeyboardButton) (bool, tg.KeyboardButtonStyle, string, int, tg.RequestPeerTypeClass, int, error) {
	present, style, text, err := layerLegacyKeyboardButtonFields(value)
	if err != nil {
		return false, style, "", 0, nil, 0, err
	}
	buttonType, ok := value.Type.(*tg.ButtonTypeRequestPeer)
	if !ok || buttonType == nil {
		return false, style, "", 0, nil, 0, layerLegacyProjectionMiss(profile, "request-peer button", value.Type)
	}
	return present, style, text, buttonType.ButtonID, buttonType.PeerType, buttonType.MaxQuantity, nil
}

func layerAdaptLegacyInputKeyboardButtonRequestPeerDecode(_ LayerProfile, nameRequested, usernameRequested, photoRequested, stylePresent bool, style tg.KeyboardButtonStyle, text string, buttonID int, peerType tg.RequestPeerTypeClass, maxQuantity int) (*tg.KeyboardButton, error) {
	buttonType := &tg.InputButtonTypeRequestPeer{ButtonID: buttonID, PeerType: peerType, MaxQuantity: maxQuantity}
	if nameRequested {
		buttonType.Flags.Set(0)
		buttonType.NameRequested = true
	}
	if usernameRequested {
		buttonType.Flags.Set(1)
		buttonType.UsernameRequested = true
	}
	if photoRequested {
		buttonType.Flags.Set(2)
		buttonType.PhotoRequested = true
	}
	return layerLegacyKeyboardButton(stylePresent, style, text, buttonType), nil
}

func layerAdaptLegacyInputKeyboardButtonRequestPeerEncode(profile LayerProfile, value *tg.KeyboardButton) (bool, bool, bool, bool, tg.KeyboardButtonStyle, string, int, tg.RequestPeerTypeClass, int, error) {
	present, style, text, err := layerLegacyKeyboardButtonFields(value)
	if err != nil {
		return false, false, false, false, style, "", 0, nil, 0, err
	}
	buttonType, ok := value.Type.(*tg.InputButtonTypeRequestPeer)
	if !ok || buttonType == nil {
		return false, false, false, false, style, "", 0, nil, 0, layerLegacyProjectionMiss(profile, "input request-peer button", value.Type)
	}
	return buttonType.Flags.Has(0) || buttonType.NameRequested,
		buttonType.Flags.Has(1) || buttonType.UsernameRequested,
		buttonType.Flags.Has(2) || buttonType.PhotoRequested,
		present, style, text, buttonType.ButtonID, buttonType.PeerType, buttonType.MaxQuantity, nil
}

func layerAdaptLegacyKeyboardButtonSimpleWebViewDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text, url string) (*tg.KeyboardButton, error) {
	return layerLegacyKeyboardButton(stylePresent, style, text, &tg.ButtonTypeSimpleWebView{URL: url}), nil
}

func layerAdaptLegacyKeyboardButtonSimpleWebViewEncode(profile LayerProfile, value *tg.KeyboardButton) (bool, tg.KeyboardButtonStyle, string, string, error) {
	present, style, text, err := layerLegacyKeyboardButtonFields(value)
	if err != nil {
		return false, style, "", "", err
	}
	buttonType, ok := value.Type.(*tg.ButtonTypeSimpleWebView)
	if !ok || buttonType == nil {
		return false, style, "", "", layerLegacyProjectionMiss(profile, "simple-webview button", value.Type)
	}
	return present, style, text, buttonType.URL, nil
}

func layerLegacyKeyboardInlineButton(stylePresent bool, style tg.KeyboardButtonStyle, text string, buttonType tg.InlineButtonTypeClass) *tg.KeyboardInlineButton {
	value := &tg.KeyboardInlineButton{Text: text, Type: buttonType}
	if stylePresent {
		value.Flags.Set(10)
		value.Style = style
	}
	return value
}

func layerLegacyKeyboardInlineButtonFields(value *tg.KeyboardInlineButton) (bool, tg.KeyboardButtonStyle, string, error) {
	if value == nil || value.Type == nil {
		return false, tg.KeyboardButtonStyle{}, "", fmt.Errorf("legacy inline keyboard button is nil or has no type")
	}
	return value.Flags.Has(10) || !value.Style.Zero(), value.Style, value.Text, nil
}

func layerAdaptLegacyKeyboardInlineButtonURLDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text, url string) (*tg.KeyboardInlineButton, error) {
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, &tg.InlineButtonTypeURL{URL: url}), nil
}

func layerAdaptLegacyKeyboardInlineButtonURLEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, tg.KeyboardButtonStyle, string, string, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, style, "", "", err
	}
	buttonType, ok := value.Type.(*tg.InlineButtonTypeURL)
	if !ok || buttonType == nil {
		return false, style, "", "", layerLegacyProjectionMiss(profile, "URL inline button", value.Type)
	}
	return present, style, text, buttonType.URL, nil
}

func layerAdaptLegacyKeyboardInlineButtonCallbackDecode(_ LayerProfile, requiresPassword, stylePresent bool, style tg.KeyboardButtonStyle, text string, data []byte) (*tg.KeyboardInlineButton, error) {
	buttonType := &tg.InlineButtonTypeCallback{Data: data}
	if requiresPassword {
		buttonType.Flags.Set(0)
		buttonType.RequiresPassword = true
	}
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, buttonType), nil
}

func layerAdaptLegacyKeyboardInlineButtonCallbackEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, bool, tg.KeyboardButtonStyle, string, []byte, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, false, style, "", nil, err
	}
	buttonType, ok := value.Type.(*tg.InlineButtonTypeCallback)
	if !ok || buttonType == nil {
		return false, false, style, "", nil, layerLegacyProjectionMiss(profile, "callback inline button", value.Type)
	}
	return buttonType.Flags.Has(0) || buttonType.RequiresPassword, present, style, text, buttonType.Data, nil
}

func layerAdaptLegacyKeyboardInlineButtonSwitchInlineDecode(_ LayerProfile, samePeer, stylePresent bool, style tg.KeyboardButtonStyle, text, query string, peerTypesPresent bool, peerTypes []tg.InlineQueryPeerTypeClass) (*tg.KeyboardInlineButton, error) {
	buttonType := &tg.InlineButtonTypeSwitchInline{Query: query}
	if samePeer {
		buttonType.Flags.Set(0)
		buttonType.SamePeer = true
	}
	if peerTypesPresent {
		buttonType.Flags.Set(1)
		buttonType.PeerTypes = peerTypes
	}
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, buttonType), nil
}

func layerAdaptLegacyKeyboardInlineButtonSwitchInlineEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, bool, tg.KeyboardButtonStyle, string, string, bool, []tg.InlineQueryPeerTypeClass, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, false, style, "", "", false, nil, err
	}
	buttonType, ok := value.Type.(*tg.InlineButtonTypeSwitchInline)
	if !ok || buttonType == nil {
		return false, false, style, "", "", false, nil, layerLegacyProjectionMiss(profile, "switch-inline button", value.Type)
	}
	return buttonType.Flags.Has(0) || buttonType.SamePeer, present, style, text, buttonType.Query,
		buttonType.Flags.Has(1) || buttonType.PeerTypes != nil, buttonType.PeerTypes, nil
}

func layerAdaptLegacyKeyboardInlineButtonGameDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text string) (*tg.KeyboardInlineButton, error) {
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, &tg.InlineButtonTypeGame{}), nil
}

func layerAdaptLegacyKeyboardInlineButtonGameEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, tg.KeyboardButtonStyle, string, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, style, "", err
	}
	if _, ok := value.Type.(*tg.InlineButtonTypeGame); !ok {
		return false, style, "", layerLegacyProjectionMiss(profile, "game inline button", value.Type)
	}
	return present, style, text, nil
}

func layerAdaptLegacyKeyboardInlineButtonBuyDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text string) (*tg.KeyboardInlineButton, error) {
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, &tg.InlineButtonTypeBuy{}), nil
}

func layerAdaptLegacyKeyboardInlineButtonBuyEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, tg.KeyboardButtonStyle, string, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, style, "", err
	}
	if _, ok := value.Type.(*tg.InlineButtonTypeBuy); !ok {
		return false, style, "", layerLegacyProjectionMiss(profile, "buy inline button", value.Type)
	}
	return present, style, text, nil
}

func layerAdaptLegacyKeyboardInlineButtonURLAuthDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text string, fwdTextPresent bool, fwdText, url string, buttonID int) (*tg.KeyboardInlineButton, error) {
	buttonType := &tg.InlineButtonTypeURLAuth{URL: url, ButtonID: buttonID}
	if fwdTextPresent {
		buttonType.Flags.Set(0)
		buttonType.FwdText = fwdText
	}
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, buttonType), nil
}

func layerAdaptLegacyKeyboardInlineButtonURLAuthEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, tg.KeyboardButtonStyle, string, bool, string, string, int, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, style, "", false, "", "", 0, err
	}
	buttonType, ok := value.Type.(*tg.InlineButtonTypeURLAuth)
	if !ok || buttonType == nil {
		return false, style, "", false, "", "", 0, layerLegacyProjectionMiss(profile, "URL-auth inline button", value.Type)
	}
	return present, style, text, buttonType.Flags.Has(0) || buttonType.FwdText != "", buttonType.FwdText, buttonType.URL, buttonType.ButtonID, nil
}

func layerAdaptLegacyInputKeyboardInlineButtonURLAuthDecode(_ LayerProfile, requestWriteAccess, stylePresent bool, style tg.KeyboardButtonStyle, text string, fwdTextPresent bool, fwdText, url string, bot tg.InputUserClass) (*tg.KeyboardInlineButton, error) {
	buttonType := &tg.InputInlineButtonTypeURLAuth{URL: url, Bot: bot}
	if requestWriteAccess {
		buttonType.Flags.Set(0)
		buttonType.RequestWriteAccess = true
	}
	if fwdTextPresent {
		buttonType.Flags.Set(1)
		buttonType.FwdText = fwdText
	}
	buttonType.Flags.Set(2)
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, buttonType), nil
}

func layerAdaptLegacyInputKeyboardInlineButtonURLAuthEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, bool, tg.KeyboardButtonStyle, string, bool, string, string, tg.InputUserClass, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, false, style, "", false, "", "", nil, err
	}
	buttonType, ok := value.Type.(*tg.InputInlineButtonTypeURLAuth)
	if !ok || buttonType == nil {
		return false, false, style, "", false, "", "", nil, layerLegacyProjectionMiss(profile, "input URL-auth inline button", value.Type)
	}
	if buttonType.Bot == nil {
		return false, false, style, "", false, "", "", nil, fmt.Errorf("legacy input URL-auth button requires bot")
	}
	return buttonType.Flags.Has(0) || buttonType.RequestWriteAccess, present, style, text,
		buttonType.Flags.Has(1) || buttonType.FwdText != "", buttonType.FwdText, buttonType.URL, buttonType.Bot, nil
}

func layerAdaptLegacyKeyboardInlineButtonUserProfileDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text string, userID int64) (*tg.KeyboardInlineButton, error) {
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, &tg.InlineButtonTypeUserProfile{UserID: userID}), nil
}

func layerAdaptLegacyKeyboardInlineButtonUserProfileEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, tg.KeyboardButtonStyle, string, int64, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, style, "", 0, err
	}
	buttonType, ok := value.Type.(*tg.InlineButtonTypeUserProfile)
	if !ok || buttonType == nil {
		return false, style, "", 0, layerLegacyProjectionMiss(profile, "user-profile inline button", value.Type)
	}
	return present, style, text, buttonType.UserID, nil
}

func layerAdaptLegacyInputKeyboardInlineButtonUserProfileDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text string, userID tg.InputUserClass) (*tg.KeyboardInlineButton, error) {
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, &tg.InputInlineButtonTypeUserProfile{UserID: userID}), nil
}

func layerAdaptLegacyInputKeyboardInlineButtonUserProfileEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, tg.KeyboardButtonStyle, string, tg.InputUserClass, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, style, "", nil, err
	}
	buttonType, ok := value.Type.(*tg.InputInlineButtonTypeUserProfile)
	if !ok || buttonType == nil {
		return false, style, "", nil, layerLegacyProjectionMiss(profile, "input user-profile inline button", value.Type)
	}
	return present, style, text, buttonType.UserID, nil
}

func layerAdaptLegacyKeyboardInlineButtonWebViewDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text, url string) (*tg.KeyboardInlineButton, error) {
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, &tg.InlineButtonTypeWebView{URL: url}), nil
}

func layerAdaptLegacyKeyboardInlineButtonWebViewEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, tg.KeyboardButtonStyle, string, string, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, style, "", "", err
	}
	buttonType, ok := value.Type.(*tg.InlineButtonTypeWebView)
	if !ok || buttonType == nil {
		return false, style, "", "", layerLegacyProjectionMiss(profile, "webview inline button", value.Type)
	}
	return present, style, text, buttonType.URL, nil
}

func layerAdaptLegacyKeyboardInlineButtonCopyDecode(_ LayerProfile, stylePresent bool, style tg.KeyboardButtonStyle, text, copyText string) (*tg.KeyboardInlineButton, error) {
	return layerLegacyKeyboardInlineButton(stylePresent, style, text, &tg.InlineButtonTypeCopy{CopyText: copyText}), nil
}

func layerAdaptLegacyKeyboardInlineButtonCopyEncode(profile LayerProfile, value *tg.KeyboardInlineButton) (bool, tg.KeyboardButtonStyle, string, string, error) {
	present, style, text, err := layerLegacyKeyboardInlineButtonFields(value)
	if err != nil {
		return false, style, "", "", err
	}
	buttonType, ok := value.Type.(*tg.InlineButtonTypeCopy)
	if !ok || buttonType == nil {
		return false, style, "", "", layerLegacyProjectionMiss(profile, "copy inline button", value.Type)
	}
	return present, style, text, buttonType.CopyText, nil
}

func layerRequireLegacyEphemeralMessagePeerEncode(_ LayerProfile, _ *tg.EphemeralMessage, present bool, peer tg.PeerClass) (tg.PeerClass, error) {
	if !present || peer == nil {
		return nil, fmt.Errorf("Layer 228 ephemeral message requires peer_id")
	}
	return peer, nil
}

func layerRequireLegacyEphemeralSendPeerEncode(_ LayerProfile, _ *tg.EphemeralSendMessageRequest, present bool, peer tg.InputPeerClass) (tg.InputPeerClass, error) {
	if !present || peer == nil {
		return nil, fmt.Errorf("Layer 228 ephemeral.sendMessage requires peer")
	}
	return peer, nil
}

func layerRequireLegacyEphemeralDeletePeerEncode(_ LayerProfile, _ *tg.EphemeralDeleteMessageRequest, present bool, peer tg.InputPeerClass) (tg.InputPeerClass, error) {
	if !present || peer == nil {
		return nil, fmt.Errorf("Layer 228 ephemeral.deleteMessage requires peer")
	}
	return peer, nil
}
