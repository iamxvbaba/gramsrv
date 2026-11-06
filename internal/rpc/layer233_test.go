package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// Layer 233 is the canonical profile of the published schema (td v1.8.0). The
// server admits it, keeps it as the session default and never clamps it.
func TestLayer233IsSupportedCanonicalProfile(t *testing.T) {
	profile, ok := tlprofile.ResolveProfile(233)
	if !ok || profile != tlprofile.Profile233 {
		t.Fatalf("ResolveProfile(233) = %d, %v, want Profile233", profile, ok)
	}
	if !isSupportedLayer(233) {
		t.Fatal("isSupportedLayer(233) = false, want true")
	}
	if isSupportedLayer(234) {
		t.Fatal("isSupportedLayer(234) = true, want false for an ungenerated future layer")
	}
	if int(tlprofile.ProfileCanonical) != 233 {
		t.Fatalf("canonical profile = %d, want 233", tlprofile.ProfileCanonical)
	}
	if currentClientLayer != 233 {
		t.Fatalf("currentClientLayer = %d, want 233", currentClientLayer)
	}
}

// The Layer 233 methods must be explicitly registered so clients get a scoped
// error instead of the unhandled-RPC compatibility trace.
func TestLayer233WidgetMethodsRegistered(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	methods := []tlprofile.SemanticID{
		tlprofile.SemanticMethodMessagesGetWidgetPublicData,
		tlprofile.SemanticMethodMessagesEditMessageWidgetData,
		tlprofile.SemanticMethodMessagesEditWidgetData,
		tlprofile.SemanticMethodMessagesEditInlineBotMessageWidgetData,
		tlprofile.SemanticMethodMessagesGetBotWidgetQueryAnswer,
		tlprofile.SemanticMethodMessagesSetBotWidgetQueryAnswer,
		tlprofile.SemanticMethodMessagesGetTranscriptionLangs,
		tlprofile.SemanticMethodMessagesTranscribeAudioFile,
	}
	for _, method := range methods {
		if !r.dispatcher.Has(method) {
			t.Fatalf("Layer 233 method %#016x is not registered", uint64(method))
		}
	}
}

// inputMediaWidget is stored on the message and, when shared, as a reusable
// widget definition that inputMediaWidgetID resolves by id.
func TestLayer233WidgetMediaRoundTrip(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	r.deps.Widgets = memory.NewWidgetStore()
	ctx := context.Background()

	in := &tg.InputMediaWidget{Width: 320, Height: 200, HTML: "<b>widget</b>", AllowQueries: true, SharedWidget: true}
	in.SetPublicData("public")
	in.SetChatData("chat")
	media, err := r.resolveInputMedia(ctx, 42, in)
	if err != nil {
		t.Fatalf("resolveInputMedia(inputMediaWidget): %v", err)
	}
	if media == nil || media.Kind != domain.MessageMediaKindWidget || media.Widget == nil {
		t.Fatalf("widget media = %#v", media)
	}
	if media.Widget.WidgetID == 0 {
		t.Fatal("shared widget did not get a server-assigned id")
	}
	if media.Widget.PublicData != "public" || media.Widget.PublicDataHash == 0 || media.Widget.ChatData != "chat" {
		t.Fatalf("widget data snapshot = %#v", media.Widget)
	}

	tl, ok := tgMessageMedia(media).(*tg.MessageMediaWidget)
	if !ok {
		t.Fatalf("widget projection = %T", tgMessageMedia(media))
	}
	if tl.Width != 320 || tl.Height != 200 || !tl.CanQuery {
		t.Fatalf("messageMediaWidget = %#v", tl)
	}
	if payload := string(tl.Payload); payload != "<b>widget</b>" {
		t.Fatalf("messageMediaWidget.Payload = %q", payload)
	}
	if data, ok := tl.GetPublicData(); !ok || data != "public" {
		t.Fatalf("messageMediaWidget public_data = %q, %v", data, ok)
	}
	if id, ok := tl.GetWidgetID(); !ok || id != media.Widget.WidgetID {
		t.Fatalf("messageMediaWidget widget_id = %d, %v", id, ok)
	}

	idInput := &tg.InputMediaWidgetID{WidgetID: media.Widget.WidgetID}
	idInput.SetChatData("chat2")
	copied, err := r.resolveInputMedia(ctx, 43, idInput)
	if err != nil {
		t.Fatalf("resolveInputMedia(inputMediaWidgetID): %v", err)
	}
	if copied.Widget == nil || !copied.Widget.IsCopy || copied.Widget.WidgetID != media.Widget.WidgetID {
		t.Fatalf("copied widget = %#v", copied.Widget)
	}
	if copied.Widget.ChatData != "chat2" || copied.Widget.PublicData != "public" {
		t.Fatalf("copied widget data = %#v", copied.Widget)
	}
	if string(copied.Widget.Payload) != "<b>widget</b>" {
		t.Fatalf("copied widget payload = %q", string(copied.Widget.Payload))
	}
}

// Only the owning bot may replace a shared widget's public data.
func TestLayer233EditWidgetDataIsBotOwned(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	r.deps.Widgets = memory.NewWidgetStore()
	ctx := context.Background()
	id, err := r.deps.Widgets.CreateWidget(ctx, domain.BotWidget{BotUserID: 7, HTML: "<b>x</b>", Payload: []byte("<b>x</b>"), PublicData: "old", PublicDataHash: widgetDataHash("old")})
	if err != nil {
		t.Fatalf("CreateWidget: %v", err)
	}
	req := &tg.MessagesEditWidgetDataRequest{WidgetID: id, PublicData: "new", CacheTime: 60}

	if ok, err := r.onMessagesEditWidgetData(WithUserID(ctx, 9), req); err == nil || ok {
		t.Fatalf("non-owner edit = %v, %v; want a refusal", ok, err)
	}
	if ok, err := r.onMessagesEditWidgetData(WithUserID(ctx, 7), req); err != nil || !ok {
		t.Fatalf("owner edit = %v, %v; want true", ok, err)
	}
	def, found, err := r.deps.Widgets.GetWidget(ctx, id)
	if err != nil || !found {
		t.Fatalf("GetWidget = %v, %v", found, err)
	}
	if def.PublicData != "new" || def.PublicDataHash != widgetDataHash("new") || def.CacheTime != 60 {
		t.Fatalf("widget public data = %#v", def)
	}
}

// File-based transcription is not wired; the method must still be registered and
// fail with TRANSCRIPTION_FAILED rather than the compatibility trace.
func TestLayer233TranscriptionFileUnavailable(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := context.Background()
	if _, err := r.onMessagesTranscribeAudioFile(ctx, &tg.MessagesTranscribeAudioFileRequest{}); err == nil {
		t.Fatal("transcribeAudioFile succeeded without an ASR backend")
	}
	langs, err := r.onMessagesGetTranscriptionLangs(WithUserID(ctx, 7), &tg.MessagesGetTranscriptionLangsRequest{})
	if err != nil || langs == nil {
		t.Fatalf("getTranscriptionLangs = %v, %v; want an empty list", langs, err)
	}
}
