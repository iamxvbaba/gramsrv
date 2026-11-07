package rpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// Layer 233 is the canonical profile of the published schema. The server must
// admit it, keep it as the session default and never clamp it to an older
// profile.
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

func createLayer233Megagroup(t *testing.T, f *rpcChannelFixture, owner domain.User, title string) *tg.Channel {
	t.Helper()
	created, err := f.router.onChannelsCreateChannel(f.userCtx(owner), &tg.ChannelsCreateChannelRequest{
		Title:     title,
		Megagroup: true,
	})
	if err != nil {
		t.Fatalf("create megagroup %q: %v", title, err)
	}
	return created.(*tg.Updates).Chats[0].(*tg.Channel)
}

func assertLayer233ForumState(t *testing.T, f *rpcChannelFixture, channelID int64, forum, tabs bool) {
	t.Helper()
	stored, err := f.channels.GetChannelByID(f.ctx, channelID)
	if err != nil {
		t.Fatalf("load channel %d: %v", channelID, err)
	}
	if stored.Forum != forum || stored.ForumTabs != tabs {
		t.Fatalf("stored forum=%v tabs=%v, want forum=%v tabs=%v", stored.Forum, stored.ForumTabs, forum, tabs)
	}
}

// channels.toggleForum gained a required sidebar layout flag in Layer 233
// (#3ff75734 -> #a2952b54). Every exact profile must still turn a forum on
// with the tabs layout: 225-232 arrive with the td default sidebar=false, and
// a canonical client such as TDesktop ready@a6df181a sends false itself.
func TestLayer233ToggleForumAcrossExactProfiles(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(23301, "15550023301", "Owner")
	for profile := tlprofile.Profile225; profile <= tlprofile.Profile233; profile++ {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			channel := createLayer233Megagroup(t, f, owner, fmt.Sprintf("Forum %d", profile))
			request := &tg.ChannelsToggleForumRequest{Channel: inputChannel(channel), Enabled: true, Tabs: true}
			result, method := dispatchExactLayerRPCTest(t, f.router, f.userCtx(owner), profile, request)
			if method != "channels.toggleForum" {
				t.Fatalf("method = %q", method)
			}
			updates, ok := dispatchCanonicalValue(result).(*tg.Updates)
			if !ok || len(updates.Chats) != 1 {
				t.Fatalf("toggle forum result = %#v, want updates with the channel", dispatchCanonicalValue(result))
			}
			chat, ok := updates.Chats[0].(*tg.Channel)
			if !ok || !chat.Forum || !chat.ForumTabs || chat.ForumSidebar {
				t.Fatalf("toggle forum chat = %#v, want forum+forum_tabs without forum_sidebar", updates.Chats[0])
			}
			var out bin.Buffer
			if err := result.Encode(&out); err != nil {
				t.Fatalf("encode exact Layer %d toggleForum result: %v", profile, err)
			}
			assertLayer233ForumState(t, f, channel.ID, true, true)
		})
	}
}

// The server models only the list and tabs forum layouts. Enabling a forum
// with the Layer 233 sidebar layout fails closed before any write, including
// on a forum that already uses tabs; disabling a forum ignores the layout
// flags, as it does tabs.
func TestLayer233ToggleForumSidebarFailsClosed(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(23302, "15550023302", "Owner")
	ctx := f.userCtx(owner)
	channel := createLayer233Megagroup(t, f, owner, "Sidebar")
	sidebar := &tg.ChannelsToggleForumRequest{Channel: inputChannel(channel), Enabled: true, Sidebar: true}

	if _, err := f.router.onChannelsToggleForum(ctx, sidebar); !tgerr.Is(err, "NOT_IMPLEMENTED") {
		t.Fatalf("sidebar toggleForum err = %v, want NOT_IMPLEMENTED", err)
	}
	wire := encodeExactLayerRPC(t, tlprofile.Profile233, sidebar)
	admitted, err := f.router.AdmitLayer(tlprofile.Profile233, &wire, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("admit Layer 233 sidebar toggleForum: %v", err)
	}
	if _, _, err := f.router.DispatchAdmitted(ctx, [8]byte{}, 0, 0, 0, admitted); !tgerr.Is(err, "NOT_IMPLEMENTED") {
		t.Fatalf("dispatched sidebar toggleForum err = %v, want NOT_IMPLEMENTED", err)
	}
	assertLayer233ForumState(t, f, channel.ID, false, false)

	tabs := &tg.ChannelsToggleForumRequest{Channel: inputChannel(channel), Enabled: true, Tabs: true}
	if _, err := f.router.onChannelsToggleForum(ctx, tabs); err != nil {
		t.Fatalf("tabs toggleForum: %v", err)
	}
	if _, err := f.router.onChannelsToggleForum(ctx, sidebar); !tgerr.Is(err, "NOT_IMPLEMENTED") {
		t.Fatalf("sidebar toggleForum on a tabs forum err = %v, want NOT_IMPLEMENTED", err)
	}
	assertLayer233ForumState(t, f, channel.ID, true, true)

	disable := &tg.ChannelsToggleForumRequest{Channel: inputChannel(channel), Enabled: false, Sidebar: true}
	if _, err := f.router.onChannelsToggleForum(ctx, disable); err != nil {
		t.Fatalf("disable toggleForum with sidebar: %v", err)
	}
	assertLayer233ForumState(t, f, channel.ID, false, false)
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
	r := newWidgetTestRouter(t, 42, 43)
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

// Widget media may only be authored by bots: a client-rendered widget from an
// ordinary account must be rejected instead of becoming a query target.
func TestLayer233WidgetInputsRequireBot(t *testing.T) {
	r := newWidgetTestRouter(t)
	ctx := context.Background()
	for _, input := range []tg.InputMediaClass{
		&tg.InputMediaWidget{Width: 320, Height: 200, HTML: "<b>x</b>", AllowQueries: true, SharedWidget: true},
		&tg.InputMediaWidgetID{WidgetID: 1},
	} {
		if _, err := r.resolveInputMedia(ctx, 99, input); !tgerr.Is(err, "MEDIA_INVALID") {
			t.Fatalf("%T from a non-bot sender = %v, want MEDIA_INVALID", input, err)
		}
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
	req := &tg.MessagesEditWidgetDataRequest{WidgetID: id, PublicData: "new"}
	req.SetCacheTime(60)

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

// File-based transcription is wired (TranscribeFile) but needs the ASR backend
// and the Files service: without them it still fails with TRANSCRIPTION_FAILED,
// and the method is explicitly registered rather than falling to the trace.
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
