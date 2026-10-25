package rpc

import (
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// TestBotAPIEditMessageReplyMarkupAndCaption pins gramsrv#137 tier 1: the
// keyboard-only edit keeps the text, the caption edit replaces only the caption
// of a media message, and a text message has no caption to edit.
func TestBotAPIEditMessageReplyMarkupAndCaption(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	keyboard := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: [][]domain.MarkupButton{{{
		Type: domain.MarkupButtonCallback, Text: "Next", Data: []byte("page:2"),
	}}}}
	text, err := fixture.router.BotAPISendMessage(fixture.ctx, fixture.bot.ID, fixture.owner.ID, "Page 1", nil, nil, false, false, 0)
	if err != nil {
		t.Fatalf("BotAPISendMessage: %v", err)
	}
	edited, err := fixture.router.BotAPIEditMessageReplyMarkup(fixture.ctx, fixture.bot.ID, fixture.owner.ID, text.ID, keyboard)
	if err != nil {
		t.Fatalf("BotAPIEditMessageReplyMarkup: %v", err)
	}
	if edited.Body != "Page 1" || edited.ReplyMarkup == nil || string(edited.ReplyMarkup.Inline[0][0].Data) != "page:2" || edited.Pts <= text.Pts {
		t.Fatalf("keyboard edit = %+v, want the same text with the new keyboard", edited)
	}
	if owner := privateBotAPIHistory(t, fixture, fixture.owner.ID, fixture.bot.ID); owner.Body != "Page 1" || owner.ReplyMarkup == nil {
		t.Fatalf("owner history after keyboard edit = %+v", owner)
	}
	cleared, err := fixture.router.BotAPIEditMessageReplyMarkup(fixture.ctx, fixture.bot.ID, fixture.owner.ID, text.ID, nil)
	if err != nil || cleared.Body != "Page 1" || cleared.ReplyMarkup != nil {
		t.Fatalf("keyboard removal = %+v err=%v, want no keyboard", cleared, err)
	}
	if _, err := fixture.router.BotAPIEditMessageCaption(fixture.ctx, fixture.bot.ID, fixture.owner.ID, text.ID, "caption", nil, false, nil); err == nil || !strings.Contains(err.Error(), "MESSAGE_HAS_NO_CAPTION") {
		t.Fatalf("caption edit of a text message err = %v, want MESSAGE_HAS_NO_CAPTION", err)
	}

	photo, err := fixture.messages.SendPrivateText(fixture.ctx, fixture.bot.ID, domain.SendPrivateTextRequest{
		SenderUserID: fixture.bot.ID, RecipientUserID: fixture.owner.ID, RandomID: 7301,
		Message: "old caption", Date: 1700007301,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 7302, AccessHash: 7303, DCID: 2}},
	})
	if err != nil {
		t.Fatalf("seed photo message: %v", err)
	}
	captioned, err := fixture.router.BotAPIEditMessageCaption(fixture.ctx, fixture.bot.ID, fixture.owner.ID, photo.SenderMessage.ID, "new caption", nil, true, keyboard)
	if err != nil {
		t.Fatalf("BotAPIEditMessageCaption: %v", err)
	}
	if captioned.Body != "new caption" || captioned.Media == nil || captioned.Media.Photo == nil || captioned.Media.Photo.ID != 7302 || captioned.ReplyMarkup == nil {
		t.Fatalf("caption edit = %+v, want the same photo with the new caption and keyboard", captioned)
	}
	if owner := privateBotAPIHistory(t, fixture, fixture.owner.ID, fixture.bot.ID); owner.Body != "new caption" || owner.Media == nil {
		t.Fatalf("owner history after caption edit = %+v", owner)
	}
	if _, err := fixture.router.BotAPIEditMessageReplyMarkup(fixture.ctx, fixture.bot.ID, fixture.owner.ID, 999999, keyboard); err == nil || !strings.Contains(err.Error(), "MESSAGE_ID_INVALID") {
		t.Fatalf("keyboard edit of a missing message err = %v, want MESSAGE_ID_INVALID", err)
	}
}

func TestBotAPIEditMessageReplyMarkupInSupergroup(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	chatID := -botAPIChannelChatIDBase - fixture.channel.ID
	sent, err := fixture.router.BotAPISendMessage(fixture.ctx, fixture.bot.ID, chatID, "Vote", nil, nil, false, false, 0)
	if err != nil {
		t.Fatalf("BotAPISendMessage channel: %v", err)
	}
	keyboard := &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: [][]domain.MarkupButton{{{
		Type: domain.MarkupButtonCallback, Text: "Yes", Data: []byte("vote:yes"),
	}}}}
	edited, err := fixture.router.BotAPIEditMessageReplyMarkup(fixture.ctx, fixture.bot.ID, chatID, sent.ID, keyboard)
	if err != nil {
		t.Fatalf("BotAPIEditMessageReplyMarkup channel: %v", err)
	}
	if edited.Body != "Vote" || edited.ReplyMarkup == nil || edited.Pts <= sent.Pts {
		t.Fatalf("channel keyboard edit = %+v", edited)
	}
	history, err := fixture.channels.GetHistory(fixture.ctx, fixture.owner.ID, domain.ChannelHistoryFilter{ChannelID: fixture.channel.ID, Limit: 1})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].Body != "Vote" || history.Messages[0].ReplyMarkup == nil {
		t.Fatalf("channel history after keyboard edit = %+v err=%v", history.Messages, err)
	}
}

func TestBotAPISendChatActionPushesTransientTyping(t *testing.T) {
	fixture := newBotAPIReceiveFixture(t, false)
	sessions := fixture.router.deps.Sessions.(*captureSessions)

	if ok, err := fixture.router.BotAPISendChatAction(fixture.ctx, fixture.bot.ID, fixture.owner.ID, domain.BotAPIChatActionUploadPhoto, 0); err != nil || !ok {
		t.Fatalf("private sendChatAction = %v err=%v", ok, err)
	}
	short, ok := sessions.lastUserPush().(*tg.UpdateShort)
	if !ok {
		t.Fatalf("private push = %T, want updateShort", sessions.lastUserPush())
	}
	typing, ok := short.Update.(*tg.UpdateUserTyping)
	if !ok || typing.UserID != fixture.bot.ID {
		t.Fatalf("private typing = %#v, want updateUserTyping from the bot", short.Update)
	}
	if _, ok := typing.Action.(*tg.SendMessageUploadPhotoAction); !ok {
		t.Fatalf("private action = %T, want upload photo", typing.Action)
	}

	chatID := -botAPIChannelChatIDBase - fixture.channel.ID
	if ok, err := fixture.router.BotAPISendChatAction(fixture.ctx, fixture.bot.ID, chatID, domain.BotAPIChatActionTyping, 0); err != nil || !ok {
		t.Fatalf("supergroup sendChatAction = %v err=%v", ok, err)
	}
	if _, err := fixture.router.BotAPISendChatAction(fixture.ctx, fixture.bot.ID, fixture.owner.ID, domain.BotAPIChatAction("dance"), 0); err == nil || !strings.Contains(err.Error(), "ACTION_INVALID") {
		t.Fatalf("unknown action err = %v, want ACTION_INVALID", err)
	}
	if _, err := fixture.router.BotAPISendChatAction(fixture.ctx, fixture.bot.ID, -botAPIChannelChatIDBase-987654, domain.BotAPIChatActionTyping, 0); err == nil || !strings.Contains(err.Error(), "CHAT_ID_INVALID") {
		t.Fatalf("foreign supergroup err = %v, want CHAT_ID_INVALID", err)
	}
}
