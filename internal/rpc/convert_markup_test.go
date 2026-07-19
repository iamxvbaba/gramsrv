package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestReplyKeyboardTLDomainRoundTrip(t *testing.T) {
	in := &tg.ReplyKeyboardMarkup{
		Resize:      true,
		SingleUse:   true,
		Selective:   true,
		Persistent:  true,
		Placeholder: "Choose",
		Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButton{Text: "Help"},
			func() *tg.KeyboardButton {
				button := &tg.KeyboardButton{Text: "Status"}
				style := tg.KeyboardButtonStyle{}
				style.SetBgPrimary(true)
				style.SetIcon(123456)
				button.SetStyle(style)
				return button
			}(),
		}}},
	}
	got, err := domainOutgoingReplyMarkupForSender(in, true)
	if err != nil {
		t.Fatalf("domainOutgoingReplyMarkupForSender: %v", err)
	}
	if got == nil || got.Kind() != domain.MessageReplyMarkupKeyboard || len(got.Keyboard) != 1 ||
		len(got.Keyboard[0]) != 2 || got.Keyboard[0][0].Text != "Help" || !got.Resize ||
		!got.SingleUse || !got.Selective || !got.Persistent || got.Placeholder != "Choose" {
		t.Fatalf("domain markup = %#v", got)
	}
	if got.Keyboard[0][1].Style != domain.MarkupButtonStylePrimary || got.Keyboard[0][1].IconCustomEmojiID != 123456 {
		t.Fatalf("second button decoration = %#v", got.Keyboard[0][1])
	}
	wire, ok := tgReplyMarkup(got).(*tg.ReplyKeyboardMarkup)
	if !ok || len(wire.Rows) != 1 || len(wire.Rows[0].Buttons) != 2 {
		t.Fatalf("wire markup = %#v", wire)
	}
	if button, ok := wire.Rows[0].Buttons[1].(*tg.KeyboardButton); !ok || button.Text != "Status" {
		t.Fatalf("second button = %#v", wire.Rows[0].Buttons[1])
	} else if style, ok := button.GetStyle(); !ok || !style.GetBgPrimary() || style.Icon != 123456 {
		t.Fatalf("second button style = %#v ok=%v", style, ok)
	}
	if !wire.Resize || !wire.SingleUse || !wire.Selective || !wire.Persistent || wire.Placeholder != "Choose" {
		t.Fatalf("wire flags = %#v", wire)
	}
}

func TestInlineButtonStyleTLDomainRoundTrip(t *testing.T) {
	button := &tg.KeyboardButtonCallback{Text: "Delete", Data: []byte("delete")}
	style := tg.KeyboardButtonStyle{}
	style.SetBgDanger(true)
	button.SetStyle(style)
	got, err := domainReplyMarkupForSender(&tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{button}}}}, true)
	if err != nil {
		t.Fatalf("domainReplyMarkupForSender: %v", err)
	}
	if got.Inline[0][0].Style != domain.MarkupButtonStyleDanger {
		t.Fatalf("domain style = %#v", got.Inline[0][0])
	}
	wire := tgReplyMarkup(got).(*tg.ReplyInlineMarkup).Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if roundTrip, ok := wire.GetStyle(); !ok || !roundTrip.GetBgDanger() {
		t.Fatalf("wire style = %#v ok=%v", roundTrip, ok)
	}
}

func TestReplyKeyboardHideAndForceReplyTLDomainRoundTrip(t *testing.T) {
	hide, err := domainOutgoingReplyMarkupForSender(&tg.ReplyKeyboardHide{Selective: true}, true)
	if err != nil {
		t.Fatalf("hide parse: %v", err)
	}
	if wire, ok := tgReplyMarkup(hide).(*tg.ReplyKeyboardHide); !ok || !wire.Selective {
		t.Fatalf("hide wire = %#v", wire)
	}
	force, err := domainOutgoingReplyMarkupForSender(&tg.ReplyKeyboardForceReply{
		SingleUse: true, Selective: true, Placeholder: "Answer",
	}, true)
	if err != nil {
		t.Fatalf("force parse: %v", err)
	}
	if wire, ok := tgReplyMarkup(force).(*tg.ReplyKeyboardForceReply); !ok || !wire.SingleUse || !wire.Selective || wire.Placeholder != "Answer" {
		t.Fatalf("force wire = %#v", wire)
	}
}

func TestReplyKeyboardRequestPhoneTLDomainRoundTrip(t *testing.T) {
	markup, err := domainOutgoingReplyMarkupForSender(&tg.ReplyKeyboardMarkup{Rows: []tg.KeyboardButtonRow{{
		Buttons: []tg.KeyboardButtonClass{&tg.KeyboardButtonRequestPhone{Text: "Share phone"}},
	}}}, true)
	if err != nil || markup == nil || len(markup.Keyboard) != 1 || len(markup.Keyboard[0]) != 1 ||
		markup.Keyboard[0][0].Type != domain.MarkupButtonRequestPhone {
		t.Fatalf("request_phone markup = %#v err=%v", markup, err)
	}
	wire, ok := tgReplyMarkup(markup).(*tg.ReplyKeyboardMarkup)
	if !ok || len(wire.Rows) != 1 || len(wire.Rows[0].Buttons) != 1 {
		t.Fatalf("request_phone wire = %#v", wire)
	}
	if _, ok := wire.Rows[0].Buttons[0].(*tg.KeyboardButtonRequestPhone); !ok {
		t.Fatalf("request_phone button = %#v", wire.Rows[0].Buttons[0])
	}
	if _, err := domainReplyMarkupForSender(&tg.ReplyKeyboardHide{}, true); err == nil {
		t.Fatal("inline-only edit/result parser must reject reply-keyboard constructors")
	}
}

func TestReplyKeyboardRequestPeerFiltersTLDomainRoundTrip(t *testing.T) {
	userType := &tg.RequestPeerTypeUser{}
	userType.SetBot(false)
	userType.SetPremium(true)
	chatType := &tg.RequestPeerTypeChat{Creator: true, BotParticipant: true}
	chatType.SetHasUsername(false)
	chatType.SetForum(true)
	chatType.SetUserAdminRights(tg.ChatAdminRights{DeleteMessages: true, ManageTopics: true})
	in := &tg.ReplyKeyboardMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonRequestPeer{Text: "Premium person", ButtonID: 1, PeerType: userType, MaxQuantity: 2},
		&tg.KeyboardButtonRequestPeer{Text: "Forum", ButtonID: 2, PeerType: chatType, MaxQuantity: 1},
	}}}}
	markup, err := domainOutgoingReplyMarkupForSender(in, true)
	if err != nil {
		t.Fatalf("parse request peer filters: %v", err)
	}
	userFilter := markup.Keyboard[0][0].RequestPeerFilter
	chatFilter := markup.Keyboard[0][1].RequestPeerFilter
	if userFilter == nil || !userFilter.UserIsBotSet || userFilter.UserIsBot || !userFilter.UserIsPremiumSet || !userFilter.UserIsPremium {
		t.Fatalf("user filter = %#v", userFilter)
	}
	if chatFilter == nil || !chatFilter.ChatIsCreated || !chatFilter.BotIsMember || !chatFilter.ChatHasUsernameSet ||
		chatFilter.ChatHasUsername || !chatFilter.ChatIsForumSet || !chatFilter.ChatIsForum ||
		chatFilter.UserAdminRights == nil || !chatFilter.UserAdminRights.DeleteMessages || !chatFilter.UserAdminRights.ManageTopics {
		t.Fatalf("chat filter = %#v", chatFilter)
	}
	wire := tgReplyMarkup(markup).(*tg.ReplyKeyboardMarkup)
	wireUser := wire.Rows[0].Buttons[0].(*tg.KeyboardButtonRequestPeer).PeerType.(*tg.RequestPeerTypeUser)
	if bot, ok := wireUser.GetBot(); !ok || bot {
		t.Fatalf("wire user bot=%v ok=%v", bot, ok)
	}
	if premium, ok := wireUser.GetPremium(); !ok || !premium {
		t.Fatalf("wire user premium=%v ok=%v", premium, ok)
	}
	wireChat := wire.Rows[0].Buttons[1].(*tg.KeyboardButtonRequestPeer).PeerType.(*tg.RequestPeerTypeChat)
	if !wireChat.Creator || !wireChat.BotParticipant {
		t.Fatalf("wire chat = %#v", wireChat)
	}
	if hasUsername, ok := wireChat.GetHasUsername(); !ok || hasUsername {
		t.Fatalf("wire has_username=%v ok=%v", hasUsername, ok)
	}
	if rights, ok := wireChat.GetUserAdminRights(); !ok || !rights.DeleteMessages || !rights.ManageTopics {
		t.Fatalf("wire rights=%#v ok=%v", rights, ok)
	}
}

func TestInputRequestPeerButtonPreservesRequestedMetadata(t *testing.T) {
	button := &tg.InputKeyboardButtonRequestPeer{
		NameRequested: true, UsernameRequested: true, PhotoRequested: true,
		Text: "Share", ButtonID: 99, PeerType: &tg.RequestPeerTypeUser{}, MaxQuantity: 3,
	}
	got, err := domainRequestedButtonFromTG(1001, nil, button)
	if err != nil {
		t.Fatal(err)
	}
	if !got.NameRequested || !got.UsernameRequested || !got.PhotoRequested || got.MaxQuantity != 3 {
		t.Fatalf("requested button=%#v", got)
	}
}
