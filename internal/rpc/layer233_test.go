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

// Layer 233 widget messages are not modeled. The send paths reject the new
// input constructors, so the server never stores or emits messageMediaWidget
// and never owes the pts-bearing updateMessageWidgetData that td drops for
// 225-232.
func TestLayer233WidgetInputsAreRejected(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)
	for _, input := range []tg.InputMediaClass{
		&tg.InputMediaWidget{Width: 320, Height: 240, HTML: "<p>widget</p>"},
		&tg.InputMediaWidgetID{WidgetID: 7},
	} {
		if _, err := r.resolveInputMedia(ctx, 1000000001, input); !tgerr.Is(err, "MEDIA_INVALID") {
			t.Fatalf("%T err = %v, want MEDIA_INVALID", input, err)
		}
	}
	for _, message := range []tg.InputBotInlineMessageClass{
		&tg.InputBotInlineMessageWidget{Width: 320, Height: 240, HTML: "<p>widget</p>"},
		&tg.InputBotInlineMessageWidgetID{WidgetID: 7},
	} {
		result := &tg.InputBotInlineResult{ID: "widget", Type: "article", SendMessage: message}
		if _, err := r.domainInlineResultFromTG(ctx, 1000000002, result); !tgerr.Is(err, "SEND_MESSAGE_TYPE_INVALID") {
			t.Fatalf("inline result with %T err = %v, want SEND_MESSAGE_TYPE_INVALID", message, err)
		}
		if _, _, _, err := domainInlineMediaAutoMessage(1000000002, message); !tgerr.Is(err, "SEND_MESSAGE_TYPE_INVALID") {
			t.Fatalf("inline media result with %T err = %v, want SEND_MESSAGE_TYPE_INVALID", message, err)
		}
	}
}
