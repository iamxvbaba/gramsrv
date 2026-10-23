package gen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"sort"
	"testing"

	"github.com/iamxvbaba/td/gen/semantic"
)

// TestRealLayerPolicy is deliberately both a review gate and the only writer
// for _schema/layers/policy.json. UPDATE_LAYER_POLICY=1 rewrites the checked-in
// document from semantic coordinates; the normal test path requires a byte-for-
// byte match and then proves that applying it leaves no unresolved obligation.
func TestRealLayerPolicy(t *testing.T) {
	set, err := semantic.LoadUniverse("../_schema/layers/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := NewSchemaSetGenerator(set, GeneratorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	document, err := buildRealLayerPolicy(initial.LayerConversionPlan().Report)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := "../_schema/layers/policy.json"
	if os.Getenv("UPDATE_LAYER_POLICY") == "1" {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, data) {
		t.Fatal("_schema/layers/policy.json is stale; in PowerShell run: $env:UPDATE_LAYER_POLICY='1'; go test ./gen -run TestRealLayerPolicy")
	}

	policy := LayerObligationPolicy{Entries: document.Entries}
	resolved, err := NewSchemaSetGenerator(set, GeneratorOptions{LayerPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if unresolved := resolved.LayerConversionPlan().Report.Unresolved(); len(unresolved) != 0 {
		t.Fatalf("real policy leaves %d unresolved obligations; first=%+v", len(unresolved), unresolved[0])
	}
	if got, want := len(document.Entries), 270; got != want {
		t.Fatalf("reviewed policy entry count = %d, want %d", got, want)
	}
	codec, err := resolved.buildLayerCodecModel("tg")
	if err != nil {
		t.Fatal(err)
	}
	// inputStorePaymentAuthCode exists in historical profiles, but the reviewed
	// required-field policy rejects every profile-to-canonical projection. Keep
	// that all-reject shape explicit so the template cannot emit a dead boxed
	// decode tail after a terminating switch (go vet must remain clean).
	paymentAuthCode := findRealLayerCodecWire(t, codec, 0x9bb2636d)
	if !paymentAuthCode.Encodable || paymentAuthCode.Decodable {
		t.Fatalf("inputStorePaymentAuthCode codec capability = encode:%v decode:%v, want true/false", paymentAuthCode.Encodable, paymentAuthCode.Decodable)
	}
	sparse, err := resolved.buildLayerSparseCodecModel("tlprofile")
	if err != nil {
		t.Fatal(err)
	}
	assertRealLayerHookContracts(t, codec.Hooks, sparse.Hooks)
}

func assertRealLayerHookContracts(t *testing.T, codec, sparse []layerCodecHookContract) {
	t.Helper()
	wantCodec := map[string]string{
		"layerBotVerificationTextEncode":                             "func(LayerProfile, *BotVerification, TextWithEntities) (string, error)",
		"layerBotVerificationTextDecode":                             "func(LayerProfile, *BotVerification, bool, string) (TextWithEntities, error)",
		"layerBotVerifierSettingsTextEncode":                         "func(LayerProfile, *BotVerifierSettings, bool, TextWithEntities) (string, bool, error)",
		"layerBotVerifierSettingsTextDecode":                         "func(LayerProfile, *BotVerifierSettings, bool, string) (TextWithEntities, bool, error)",
		"layerBotsSetCustomVerificationTextEncode":                   "func(LayerProfile, *BotsSetCustomVerificationRequest, bool, TextWithEntities) (string, bool, error)",
		"layerBotsSetCustomVerificationTextDecode":                   "func(LayerProfile, *BotsSetCustomVerificationRequest, bool, string) (TextWithEntities, bool, error)",
		"layerAdaptLegacyInputKeyboardButtonRequestPeerDecode":       "func(LayerProfile, bool, bool, bool, bool, KeyboardButtonStyle, string, int, RequestPeerTypeClass, int) (*KeyboardButton, error)",
		"layerAdaptLegacyInputKeyboardButtonRequestPeerEncode":       "func(LayerProfile, *KeyboardButton) (bool, bool, bool, bool, KeyboardButtonStyle, string, int, RequestPeerTypeClass, int, error)",
		"layerAdaptLegacyInputKeyboardInlineButtonURLAuthDecode":     "func(LayerProfile, bool, bool, KeyboardButtonStyle, string, bool, string, string, InputUserClass) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyInputKeyboardInlineButtonURLAuthEncode":     "func(LayerProfile, *KeyboardInlineButton) (bool, bool, KeyboardButtonStyle, string, bool, string, string, InputUserClass, error)",
		"layerAdaptLegacyInputKeyboardInlineButtonUserProfileDecode": "func(LayerProfile, bool, KeyboardButtonStyle, string, InputUserClass) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyInputKeyboardInlineButtonUserProfileEncode": "func(LayerProfile, *KeyboardInlineButton) (bool, KeyboardButtonStyle, string, InputUserClass, error)",
		"layerAdaptLegacyKeyboardButtonRequestGeoLocationDecode":     "func(LayerProfile, bool, KeyboardButtonStyle, string) (*KeyboardButton, error)",
		"layerAdaptLegacyKeyboardButtonRequestGeoLocationEncode":     "func(LayerProfile, *KeyboardButton) (bool, KeyboardButtonStyle, string, error)",
		"layerAdaptLegacyKeyboardButtonRequestPeerDecode":            "func(LayerProfile, bool, KeyboardButtonStyle, string, int, RequestPeerTypeClass, int) (*KeyboardButton, error)",
		"layerAdaptLegacyKeyboardButtonRequestPeerEncode":            "func(LayerProfile, *KeyboardButton) (bool, KeyboardButtonStyle, string, int, RequestPeerTypeClass, int, error)",
		"layerAdaptLegacyKeyboardButtonRequestPhoneDecode":           "func(LayerProfile, bool, KeyboardButtonStyle, string) (*KeyboardButton, error)",
		"layerAdaptLegacyKeyboardButtonRequestPhoneEncode":           "func(LayerProfile, *KeyboardButton) (bool, KeyboardButtonStyle, string, error)",
		"layerAdaptLegacyKeyboardButtonRequestPollDecode":            "func(LayerProfile, bool, KeyboardButtonStyle, bool, bool, string) (*KeyboardButton, error)",
		"layerAdaptLegacyKeyboardButtonRequestPollEncode":            "func(LayerProfile, *KeyboardButton) (bool, KeyboardButtonStyle, bool, bool, string, error)",
		"layerAdaptLegacyKeyboardButtonSimpleWebViewDecode":          "func(LayerProfile, bool, KeyboardButtonStyle, string, string) (*KeyboardButton, error)",
		"layerAdaptLegacyKeyboardButtonSimpleWebViewEncode":          "func(LayerProfile, *KeyboardButton) (bool, KeyboardButtonStyle, string, string, error)",
		"layerAdaptLegacyKeyboardInlineButtonBuyDecode":              "func(LayerProfile, bool, KeyboardButtonStyle, string) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyKeyboardInlineButtonBuyEncode":              "func(LayerProfile, *KeyboardInlineButton) (bool, KeyboardButtonStyle, string, error)",
		"layerAdaptLegacyKeyboardInlineButtonCallbackDecode":         "func(LayerProfile, bool, bool, KeyboardButtonStyle, string, []byte) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyKeyboardInlineButtonCallbackEncode":         "func(LayerProfile, *KeyboardInlineButton) (bool, bool, KeyboardButtonStyle, string, []byte, error)",
		"layerAdaptLegacyKeyboardInlineButtonCopyDecode":             "func(LayerProfile, bool, KeyboardButtonStyle, string, string) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyKeyboardInlineButtonCopyEncode":             "func(LayerProfile, *KeyboardInlineButton) (bool, KeyboardButtonStyle, string, string, error)",
		"layerAdaptLegacyKeyboardInlineButtonGameDecode":             "func(LayerProfile, bool, KeyboardButtonStyle, string) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyKeyboardInlineButtonGameEncode":             "func(LayerProfile, *KeyboardInlineButton) (bool, KeyboardButtonStyle, string, error)",
		"layerAdaptLegacyKeyboardInlineButtonSwitchInlineDecode":     "func(LayerProfile, bool, bool, KeyboardButtonStyle, string, string, bool, []InlineQueryPeerTypeClass) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyKeyboardInlineButtonSwitchInlineEncode":     "func(LayerProfile, *KeyboardInlineButton) (bool, bool, KeyboardButtonStyle, string, string, bool, []InlineQueryPeerTypeClass, error)",
		"layerAdaptLegacyKeyboardInlineButtonURLAuthDecode":          "func(LayerProfile, bool, KeyboardButtonStyle, string, bool, string, string, int) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyKeyboardInlineButtonURLAuthEncode":          "func(LayerProfile, *KeyboardInlineButton) (bool, KeyboardButtonStyle, string, bool, string, string, int, error)",
		"layerAdaptLegacyKeyboardInlineButtonURLDecode":              "func(LayerProfile, bool, KeyboardButtonStyle, string, string) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyKeyboardInlineButtonURLEncode":              "func(LayerProfile, *KeyboardInlineButton) (bool, KeyboardButtonStyle, string, string, error)",
		"layerAdaptLegacyKeyboardInlineButtonUserProfileDecode":      "func(LayerProfile, bool, KeyboardButtonStyle, string, int64) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyKeyboardInlineButtonUserProfileEncode":      "func(LayerProfile, *KeyboardInlineButton) (bool, KeyboardButtonStyle, string, int64, error)",
		"layerAdaptLegacyKeyboardInlineButtonWebViewDecode":          "func(LayerProfile, bool, KeyboardButtonStyle, string, string) (*KeyboardInlineButton, error)",
		"layerAdaptLegacyKeyboardInlineButtonWebViewEncode":          "func(LayerProfile, *KeyboardInlineButton) (bool, KeyboardButtonStyle, string, string, error)",
		"layerCaptureChatInviteJoinQueryIDFromWebViewDecode":         "func(LayerProfile, *MessagesChatInviteJoinResultWebView, WebViewResultURL) error",
		"layerCaptureStarGiftWithdrawalLegacyPasswordDecode":         "func(LayerProfile, *PaymentsGetStarGiftWithdrawalURLRequest, InputCheckPasswordSRPClass) error",
		"layerDefaultLegacyKeyboardButtonTypeDecode":                 "func(LayerProfile, *KeyboardButton) (ButtonTypeClass, error)",
		"layerProjectEphemeralBotCommandProject":                     "func(LayerProfile, *BotCommand, bool) (*BotCommand, bool, error)",
		"layerProjectLegacyDefaultKeyboardButtonProject":             "func(LayerProfile, *KeyboardButton, ButtonTypeClass) (*KeyboardButton, bool, error)",
		"layerRequireCapturedChatInviteJoinQueryIDDecode":            "func(LayerProfile, *MessagesChatInviteJoinResultWebView) (int64, error)",
		"layerRequireStarGiftWithdrawalTmpPasswordDecode":            "func(LayerProfile, *PaymentsGetStarGiftWithdrawalURLRequest) ([]byte, error)",
		"layerRequireLegacyEphemeralDeletePeerEncode":                "func(LayerProfile, *EphemeralDeleteMessageRequest, bool, InputPeerClass) (InputPeerClass, error)",
		"layerRequireLegacyEphemeralMessagePeerEncode":               "func(LayerProfile, *EphemeralMessage, bool, PeerClass) (PeerClass, error)",
		"layerRequireLegacyEphemeralSendPeerEncode":                  "func(LayerProfile, *EphemeralSendMessageRequest, bool, InputPeerClass) (InputPeerClass, error)",
	}
	gotCodec := make(map[string]string, len(codec))
	for _, hook := range codec {
		gotCodec[hook.Name] = hook.Signature
	}
	if !maps.Equal(gotCodec, wantCodec) {
		t.Fatalf("real codec hook contracts changed\ngot:  %v\nwant: %v", gotCodec, wantCodec)
	}

	wantRPC := map[string]string{
		"layerAdaptBotGuestChatResult":                "func(LayerProfile, tg.InputBotInlineMessageIDClass) (bool, error)",
		"layerAdaptChatInviteJoinResultToUpdates":     "func(LayerProfile, tg.MessagesChatInviteJoinResultClass) (tg.UpdatesClass, error)",
		"layerAdaptWebBrowserSettingsExceptionResult": "func(LayerProfile, tg.UpdatesClass) (bool, error)",
	}
	gotRPC := make(map[string]string, len(sparse))
	for _, hook := range sparse {
		if _, objectHook := wantCodec[hook.Name]; objectHook {
			continue
		}
		gotRPC[hook.Name] = hook.Signature
	}
	if !maps.Equal(gotRPC, wantRPC) {
		t.Fatalf("real RPC hook contracts changed\ngot:  %v\nwant: %v", gotRPC, wantRPC)
	}
}

func findRealLayerCodecWire(t *testing.T, model *layerCodecModel, id uint32) *layerCodecWire {
	t.Helper()
	for index := range model.Wires {
		if model.Wires[index].WireID == id {
			return &model.Wires[index]
		}
	}
	t.Fatalf("wire %#08x is absent", id)
	return nil
}

func buildRealLayerPolicy(report LayerObligationReport) (LayerPolicyDocument, error) {
	document := LayerPolicyDocument{Version: LayerPolicyVersion}
	for _, obligation := range report.Obligations {
		resolution, include, err := realLayerResolution(obligation)
		if err != nil {
			return LayerPolicyDocument{}, err
		}
		if !include {
			continue
		}
		resolution.Note = formatLayerPolicyTemplateNote(obligation)
		document.Entries = append(document.Entries, LayerObligationPolicyEntry{
			Key:        obligation.Key,
			Resolution: resolution,
		})
	}
	sort.Slice(document.Entries, func(i, j int) bool { return document.Entries[i].Key < document.Entries[j].Key })
	return document, nil
}

func realLayerResolution(obligation LayerObligation) (LayerObligationResolution, bool, error) {
	// Mechanical field projections are omitted according to the exact target
	// schema. The ephemeral marker is different: preserving the surrounding
	// BotCommand while dropping this privacy bit would expose a private command
	// as an ordinary group command, so older profiles must project the entire
	// vector element out.
	if obligation.Kind == LayerObligationFieldProjection {
		// An explicitly supplied Layer 231 wallet OAuth session expresses a
		// wallet-connection choice, which an encoder must not silently replace
		// with plain login. Optional response hints can use the drop default:
		// plain login success does not constitute wallet authorization.
		if obligation.Layer >= 225 && obligation.Layer < 231 {
			switch obligation.Semantic.QName + ":" + obligation.Field {
			case "messages.acceptUrlAuth:tonconnect_session":
				return LayerObligationResolution{Action: LayerResolveRejectIfPresent}, true, nil
			}
		}
		if obligation.Layer >= 225 && obligation.Layer < 230 {
			switch obligation.Semantic.QName + ":" + obligation.Field {
			case "username:deletable", "username:expired", "bots.checkUsername:additional", "auth.initFirebasePnvLogin:except_ids":
				return LayerObligationResolution{Action: LayerResolveRejectIfPresent}, true, nil
			}
		}
		if obligation.Semantic.QName == "botCommand" &&
			obligation.Field == "ephemeral" &&
			obligation.Layer >= 225 && obligation.Layer < 228 {
			return LayerObligationResolution{
				Action: LayerResolveProject,
				Hook:   "layerProjectEphemeralBotCommand",
			}, true, nil
		}
		if obligation.Layer >= 225 && obligation.Layer < 229 {
			switch obligation.Semantic.QName + ":" + obligation.Field {
			case "keyboardButton:type":
				return LayerObligationResolution{
					Action: LayerResolveProject,
					Hook:   "layerProjectLegacyDefaultKeyboardButton",
				}, true, nil
			case "ephemeralMessage:welcome_template",
				"ephemeralMessage:invert_media",
				"ephemeralMessage:noforwards",
				"ephemeralMessage:rich_message",
				"ephemeralMessage:chat_instance",
				"ephemeralMessage:anchor_msg_id",
				"ephemeral.sendMessage:invert_media",
				"ephemeral.sendMessage:welcome",
				"ephemeral.sendMessage:anchor",
				"ephemeral.sendMessage:noforwards",
				"messages.forwardMessages:from_ephemeral",
				"messageActionStarGiftUnique:name_hidden",
				"messageActionStarGiftUnique:message",
				"inputInvoiceStarGiftResale:show_name",
				"inputInvoiceStarGiftResale:message",
				"replyKeyboardMarkup:force_reply",
				"replyInlineMarkup:force_reply":
				return LayerObligationResolution{Action: LayerResolveRejectIfPresent}, true, nil
			}
		}
		return LayerObligationResolution{}, false, nil
	}
	if obligation.Resolution.resolved() {
		return LayerObligationResolution{}, false, nil
	}

	adapter := func(hook string) (LayerObligationResolution, bool, error) {
		return LayerObligationResolution{Action: LayerResolveAdapter, Hook: hook}, true, nil
	}
	switch obligation.Kind {
	case LayerObligationOldOnly:
		if obligation.Semantic.QName == "inputPasskeyCredentialFirebasePNV" {
			return LayerObligationResolution{Action: LayerResolveReject}, true, nil
		}
		return realHistoricalKeyboardResolution(obligation)
	case LayerObligationIncompatible:
		switch obligation.Semantic.QName + ":" + obligation.Field {
		case "botVerification:description":
			return adapter("layerBotVerificationText")
		case "botVerifierSettings:custom_description":
			return adapter("layerBotVerifierSettingsText")
		case "bots.setCustomVerification:custom_description":
			return adapter("layerBotsSetCustomVerificationText")
		}
		if obligation.Semantic.QName == "replyInlineMarkup" && obligation.Field == "rows" {
			return adapter("layerAdaptLegacyInlineKeyboardRows")
		}
	case LayerObligationDiscard:
		switch obligation.Semantic.QName + ":" + obligation.Field {
		case "messages.chatInviteJoinResultWebView:url":
			return LayerObligationResolution{Action: LayerResolveDrop}, true, nil
		case "messages.chatInviteJoinResultWebView:webview":
			return adapter("layerCaptureChatInviteJoinQueryIDFromWebView")
		case "payments.getStarGiftWithdrawalUrl:password":
			// Layer 232 replaced the inline SRP proof with tmp_password. The
			// proof is carried in a marked tmp_password so the server can still
			// verify it; it is never silently dropped.
			return adapter("layerCaptureStarGiftWithdrawalLegacyPassword")
		}
	case LayerObligationRequired:
		return realRequiredLayerResolution(obligation)
	case LayerObligationResult:
		if obligation.Direction == LayerDirectionProfileToCanonical {
			switch obligation.Semantic.QName {
			case "channels.joinChannel", "messages.importChatInvite":
				return adapter("layerAdaptUpdatesToChatInviteJoinResult")
			case "account.toggleWebBrowserSettingsException", "messages.setBotGuestChatResult":
				// The historical Bool contains no canonical result payload. A
				// layer-aware client must reject these exact profiles rather than
				// invent Updates or an inline-message identifier.
				return LayerObligationResolution{Action: LayerResolveReject}, true, nil
			}
		}
		switch obligation.Semantic.QName {
		case "channels.joinChannel", "messages.importChatInvite":
			return adapter("layerAdaptChatInviteJoinResultToUpdates")
		case "account.toggleWebBrowserSettingsException":
			return adapter("layerAdaptWebBrowserSettingsExceptionResult")
		case "messages.setBotGuestChatResult":
			return adapter("layerAdaptBotGuestChatResult")
		}
	case LayerObligationUpdateProjection:
		switch obligation.Semantic.QName {
		case "updateWalletState", "updateSentWalletTransaction", "updateWalletGaslessInfo", "updateWalletTonConnectSession", "updateWalletTonConnectPendingDisconnect":
			return LayerObligationResolution{Action: LayerResolveReject}, true, nil
		}
		return LayerObligationResolution{Action: LayerResolveDrop}, true, nil
	}
	return LayerObligationResolution{}, false, fmt.Errorf("unreviewed real layer obligation: %+v", obligation)
}

func realRequiredLayerResolution(obligation LayerObligation) (LayerObligationResolution, bool, error) {
	adapter := func(hook string) (LayerObligationResolution, bool, error) {
		return LayerObligationResolution{Action: LayerResolveAdapter, Hook: hook}, true, nil
	}
	defaulted := func() (LayerObligationResolution, bool, error) {
		return LayerObligationResolution{Action: LayerResolveDefault}, true, nil
	}
	switch obligation.Semantic.QName {
	case "keyboardButton":
		if obligation.OtherField == "type" {
			return adapter("layerDefaultLegacyKeyboardButtonType")
		}
	case "ephemeralMessage":
		if obligation.OtherField == "peer_id" {
			return adapter("layerRequireLegacyEphemeralMessagePeer")
		}
	case "ephemeral.sendMessage":
		if obligation.OtherField == "peer" {
			return adapter("layerRequireLegacyEphemeralSendPeer")
		}
	case "ephemeral.deleteMessage":
		if obligation.OtherField == "peer" {
			return adapter("layerRequireLegacyEphemeralDeletePeer")
		}
	case "auth.sentCodePaymentRequired", "pageListOrderedItemBlocks", "pageListOrderedItemText":
		return defaulted()
	case "inputStorePaymentAuthCode":
		return LayerObligationResolution{Action: LayerResolveReject}, true, nil
	case "payments.getStarGiftWithdrawalUrl":
		if obligation.Direction == LayerDirectionCanonicalToProfile && obligation.OtherField == "password" {
			return LayerObligationResolution{Action: LayerResolveReject}, true, nil
		}
		if obligation.Direction == LayerDirectionProfileToCanonical && obligation.OtherField == "tmp_password" {
			return adapter("layerRequireStarGiftWithdrawalTmpPassword")
		}
	case "messages.chatInviteJoinResultWebView":
		if obligation.Direction == LayerDirectionCanonicalToProfile {
			switch obligation.OtherField {
			case "url", "webview":
				return LayerObligationResolution{Action: LayerResolveReject}, true, nil
			}
		}
		if obligation.Direction == LayerDirectionProfileToCanonical && obligation.OtherField == "query_id" {
			return adapter("layerRequireCapturedChatInviteJoinQueryID")
		}
	}
	return LayerObligationResolution{}, false, fmt.Errorf("unreviewed required layer obligation: %+v", obligation)
}

func realHistoricalKeyboardResolution(obligation LayerObligation) (LayerObligationResolution, bool, error) {
	if obligation.Direction != LayerDirectionProfileToCanonical {
		return LayerObligationResolution{}, false, fmt.Errorf("unreviewed historical keyboard direction: %+v", obligation)
	}
	reply := map[string]string{
		"keyboardButtonRequestPhone":       "layerAdaptLegacyKeyboardButtonRequestPhone",
		"keyboardButtonRequestGeoLocation": "layerAdaptLegacyKeyboardButtonRequestGeoLocation",
		"keyboardButtonRequestPoll":        "layerAdaptLegacyKeyboardButtonRequestPoll",
		"keyboardButtonRequestPeer":        "layerAdaptLegacyKeyboardButtonRequestPeer",
		"inputKeyboardButtonRequestPeer":   "layerAdaptLegacyInputKeyboardButtonRequestPeer",
		"keyboardButtonSimpleWebView":      "layerAdaptLegacyKeyboardButtonSimpleWebView",
	}
	if hook := reply[obligation.Semantic.QName]; hook != "" {
		return LayerObligationResolution{
			Action: LayerResolveAdapter,
			Hook:   hook,
			Target: "type:keyboardButton",
		}, true, nil
	}
	inline := map[string]string{
		"keyboardButtonUrl":              "layerAdaptLegacyKeyboardInlineButtonURL",
		"keyboardButtonCallback":         "layerAdaptLegacyKeyboardInlineButtonCallback",
		"keyboardButtonSwitchInline":     "layerAdaptLegacyKeyboardInlineButtonSwitchInline",
		"keyboardButtonGame":             "layerAdaptLegacyKeyboardInlineButtonGame",
		"keyboardButtonBuy":              "layerAdaptLegacyKeyboardInlineButtonBuy",
		"keyboardButtonUrlAuth":          "layerAdaptLegacyKeyboardInlineButtonURLAuth",
		"inputKeyboardButtonUrlAuth":     "layerAdaptLegacyInputKeyboardInlineButtonURLAuth",
		"keyboardButtonUserProfile":      "layerAdaptLegacyKeyboardInlineButtonUserProfile",
		"inputKeyboardButtonUserProfile": "layerAdaptLegacyInputKeyboardInlineButtonUserProfile",
		"keyboardButtonWebView":          "layerAdaptLegacyKeyboardInlineButtonWebView",
		"keyboardButtonCopy":             "layerAdaptLegacyKeyboardInlineButtonCopy",
	}
	if hook := inline[obligation.Semantic.QName]; hook != "" {
		return LayerObligationResolution{
			Action: LayerResolveAdapter,
			Hook:   hook,
			Target: "type:keyboardInlineButton",
		}, true, nil
	}
	return LayerObligationResolution{}, false, fmt.Errorf("unreviewed old-only layer obligation: %+v", obligation)
}
