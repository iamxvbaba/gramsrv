package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
)

func privateUpdate(userID, senderID int64, text string, media tg.MessageMediaClass, flags func(*tg.Message)) *tg.Updates {
	msg := &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: userID}, FromID: &tg.PeerUser{UserID: senderID}, Message: text, Media: media}
	if flags != nil {
		flags(msg)
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: msg, Pts: 1}},
		Users:   []tg.UserClass{&tg.User{ID: senderID, FirstName: "Alice", LastName: "Doe"}},
	}
}

func TestBuildPushNotificationPrivateText(t *testing.T) {
	target, ok := buildPushTarget(1001, privateUpdate(1001, 42, "hello there", nil, nil))
	if !ok {
		t.Fatal("private text message produced no notification")
	}
	notif := target.notif
	if notif.LocKey != "MESSAGE_TEXT" {
		t.Fatalf("loc_key = %q, want MESSAGE_TEXT", notif.LocKey)
	}
	if len(notif.LocArgs) != 2 || notif.LocArgs[0] != "Alice Doe" || notif.LocArgs[1] != "hello there" {
		t.Fatalf("loc_args = %#v", notif.LocArgs)
	}
	if notif.Custom["from_id"] != int64(42) || notif.Custom["msg_id"] != 7 {
		t.Fatalf("custom = %#v", notif.Custom)
	}
	if notif.UserID != 1001 || notif.Title != "Alice Doe" || notif.Description != "hello there" {
		t.Fatalf("notification = %+v", notif)
	}
	if target.peer.ID != 42 || target.category != "users" {
		t.Fatalf("target peer/category = %+v", target)
	}
}

func TestBuildPushNotificationPrivateMediaAndFlags(t *testing.T) {
	target, ok := buildPushTarget(1001, privateUpdate(1001, 42, "", &tg.MessageMediaPhoto{}, func(m *tg.Message) { m.Mentioned = true }))
	if !ok {
		t.Fatal("private media message produced no notification")
	}
	if target.notif.LocKey != "MESSAGE_PHOTO" || len(target.notif.LocArgs) != 1 {
		t.Fatalf("media notification = %+v", target.notif)
	}
	if target.notif.Custom["mention"] != 1 {
		t.Fatalf("mention flag missing: %#v", target.notif.Custom)
	}
	if target.notif.Description != "Photo" {
		t.Fatalf("description = %q, want Photo", target.notif.Description)
	}
}

func TestBuildPushNotificationSkips(t *testing.T) {
	cases := map[string]*tg.Updates{
		"outgoing": privateUpdate(1001, 1001, "x", nil, func(m *tg.Message) { m.Out = true }),
		"silent":   privateUpdate(1001, 42, "x", nil, func(m *tg.Message) { m.Silent = true }),
		"selfchat": privateUpdate(1001, 1001, "x", nil, nil),
	}
	for name, updates := range cases {
		if _, ok := buildPushTarget(1001, updates); ok {
			t.Fatalf("%s message produced a notification, want skip", name)
		}
	}
}

func TestBuildPushNotificationMegagroupAndChannel(t *testing.T) {
	msg := &tg.Message{ID: 8, PeerID: &tg.PeerChannel{ChannelID: 100}, FromID: &tg.PeerUser{UserID: 2}, Message: "hi group"}
	megagroup := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: msg}},
		Chats:   []tg.ChatClass{&tg.Channel{ID: 100, Title: "Family", Megagroup: true}},
		Users:   []tg.UserClass{&tg.User{ID: 2, FirstName: "Bob"}},
	}
	target, ok := buildPushTarget(5, megagroup)
	if !ok {
		t.Fatal("megagroup message produced no notification")
	}
	if target.notif.LocKey != "CHAT_MESSAGE_TEXT" || len(target.notif.LocArgs) != 3 ||
		target.notif.LocArgs[0] != "Bob" || target.notif.LocArgs[1] != "Family" || target.notif.LocArgs[2] != "hi group" {
		t.Fatalf("megagroup notification = %+v", target.notif)
	}
	if target.notif.Custom["channel_id"] != int64(100) || target.notif.Custom["chat_from_id"] != int64(2) {
		t.Fatalf("megagroup custom = %#v", target.notif.Custom)
	}
	if target.category != "chats" || target.notif.Description != "Bob: hi group" {
		t.Fatalf("megagroup target = %+v", target)
	}

	broadcast := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: msg}},
		Chats:   []tg.ChatClass{&tg.Channel{ID: 100, Title: "News", Broadcast: true}},
	}
	target, ok = buildPushTarget(5, broadcast)
	if !ok {
		t.Fatal("channel post produced no notification")
	}
	if target.notif.LocKey != "CHANNEL_MESSAGE_TEXT" || len(target.notif.LocArgs) != 2 ||
		target.notif.LocArgs[0] != "News" || target.notif.LocArgs[1] != "hi group" {
		t.Fatalf("channel notification = %+v", target.notif)
	}
	if _, hasFrom := target.notif.Custom["chat_from_id"]; hasFrom {
		t.Fatalf("broadcast custom must not carry chat_from_id: %#v", target.notif.Custom)
	}
	if target.category != "broadcasts" {
		t.Fatalf("broadcast category = %q", target.category)
	}
}
