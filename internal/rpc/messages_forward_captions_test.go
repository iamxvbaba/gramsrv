package rpc

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestForwardMediaCaptionsAcrossDestinations(t *testing.T) {
	const ownerID, fromID, toID = int64(1000000101), int64(1000000102), int64(1000000103)
	const now = 1700001000
	for _, destination := range []string{"private", "channel", "scheduled"} {
		for _, kind := range []string{"text", "photo", "document", "webpage"} {
			for _, drop := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/drop=%v", destination, kind, drop), func(t *testing.T) {
					var media *domain.MessageMedia
					switch kind {
					case "photo":
						media = &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 11, AccessHash: 12, DCID: 2}}
					case "document":
						media = &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &domain.Document{ID: 21, AccessHash: 22, DCID: 2, MimeType: "application/pdf", Size: 100}}
					case "webpage":
						media = &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{ID: 31, State: domain.MessageWebPageStateDone, URL: "https://example.test/", DisplayURL: "example.test", Title: "Preview"}}
					}
					entities := []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 4}}
					source := domain.Message{ID: 8, OwnerUserID: ownerID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: fromID}, From: domain.Peer{Type: domain.PeerTypeUser, ID: fromID}, Date: now - 10, Body: "body", Entities: entities, Media: media}
					messages := &scheduledCaptureMessages{captureMessages: &captureMessages{list: domain.MessageList{Messages: []domain.Message{source}}}}
					channels := appchannels.NewService(memory.NewChannelStore())
					// v2 commits scheduled-box mutations with durable delivery.
					outbox := attachCaptureMessagesDeliveryOutbox(messages.captureMessages)
					r := New(Config{}, Deps{Messages: messages, Channels: channels, DeliveryOutbox: outbox, Users: mapUsersService{users: map[int64]domain.User{
						ownerID: {ID: ownerID, FirstName: "Owner"}, fromID: {ID: fromID, FirstName: "Source"}, toID: {ID: toID, FirstName: "Target"},
					}}}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
					ctx := WithUserID(context.Background(), ownerID)
					req := &tg.MessagesForwardMessagesRequest{FromPeer: &tg.InputPeerUser{UserID: fromID}, ToPeer: &tg.InputPeerUser{UserID: toID}, ID: []int{8}, RandomID: []int64{801}, DropAuthor: true, DropMediaCaptions: drop}
					var channelID int64
					if destination == "channel" {
						created, err := channels.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{CreatorUserID: ownerID, Title: "Caption Test", Megagroup: true, Date: now})
						if err != nil {
							t.Fatal(err)
						}
						channelID = created.Channel.ID
						req.ToPeer = &tg.InputPeerChannel{ChannelID: channelID, AccessHash: created.Channel.AccessHash}
					} else if destination == "scheduled" {
						req.SetScheduleDate(now + 3600)
					}
					updates, err := r.onMessagesForwardMessages(ctx, req)
					if err != nil {
						t.Fatal(err)
					}
					var body string
					var gotEntities []domain.MessageEntity
					var gotMedia *domain.MessageMedia
					switch destination {
					case "private":
						body, gotEntities, gotMedia = messages.sendReq.Message, messages.sendReq.Entities, messages.sendReq.Media
					case "scheduled":
						body, gotEntities, gotMedia = messages.scheduleReq.Message, messages.scheduleReq.Entities, messages.scheduleReq.Media
					case "channel":
						id := sentMessageIDFromUpdates(updates)
						history, err := channels.GetMessages(ctx, ownerID, channelID, []int{id})
						if err != nil || len(history.Messages) != 1 {
							t.Fatalf("history: %+v, %v", history, err)
						}
						message := history.Messages[0]
						body, gotEntities, gotMedia = message.Body, message.Entities, message.Media
					}
					if drop && (kind == "photo" || kind == "document") {
						if body != "" || len(gotEntities) != 0 {
							t.Fatalf("caption survived: body=%q entities=%+v", body, gotEntities)
						}
					} else if body != "body" || !reflect.DeepEqual(gotEntities, entities) {
						t.Fatalf("non-caption text changed: body=%q entities=%+v", body, gotEntities)
					}
					if !reflect.DeepEqual(gotMedia, media) || !reflect.DeepEqual(messages.list.Messages[0], source) {
						t.Fatal("forward changed the source or media")
					}
				})
			}
		}
	}
}

func TestForwardCaptionOptionIsPartOfReplayIdentity(t *testing.T) {
	req := &tg.MessagesForwardMessagesRequest{FromPeer: &tg.InputPeerSelf{}, ToPeer: &tg.InputPeerUser{UserID: 1001}, ID: []int{8}, RandomID: []int64{801}, DropAuthor: true}
	before, err := forwardMessagesItemIdempotencyFingerprint(req, 8, 801)
	if err != nil {
		t.Fatal(err)
	}
	req.DropMediaCaptions = true
	after, err := forwardMessagesItemIdempotencyFingerprint(req, 8, 801)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, after) {
		t.Fatal("changed caption option reused the same fingerprint")
	}
}

func TestEditMediaCaptionEmptyUsesStoredMedia(t *testing.T) {
	const ownerID, peerID = int64(1000000101), int64(1000000102)
	const now = 1700001000
	for _, destination := range []string{"private", "channel"} {
		for _, kind := range []string{"text", "photo", "document"} {
			t.Run(destination+"/"+kind, func(t *testing.T) {
				ctx := WithUserID(context.Background(), ownerID)
				var media *domain.MessageMedia
				if kind == "photo" {
					media = &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 11, AccessHash: 12, DCID: 2}}
				}
				if kind == "document" {
					media = &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &domain.Document{ID: 21, AccessHash: 22, DCID: 2, MimeType: "application/pdf", Size: 100}}
				}
				messages := appmessages.NewService(memory.NewMessageStore(), nil)
				channels := appchannels.NewService(memory.NewChannelStore())
				r := New(Config{}, Deps{Messages: messages, Channels: channels, Users: mapUsersService{users: map[int64]domain.User{ownerID: {ID: ownerID, FirstName: "Owner"}, peerID: {ID: peerID, FirstName: "Peer"}}}}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now+30, 0)})
				var peer tg.InputPeerClass = &tg.InputPeerUser{UserID: peerID}
				var id int
				var recipientID int
				if destination == "private" {
					sent, err := messages.SendPrivateText(ctx, ownerID, domain.SendPrivateTextRequest{SenderUserID: ownerID, RecipientUserID: peerID, RandomID: 701, Message: "caption", Media: media, Date: now})
					if err != nil {
						t.Fatal(err)
					}
					id, recipientID = sent.SenderMessage.ID, sent.RecipientMessage.ID
				} else {
					created, err := channels.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{CreatorUserID: ownerID, Title: "Edit Test", Megagroup: true, Date: now})
					if err != nil {
						t.Fatal(err)
					}
					peer = &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
					sent, err := channels.SendMessage(ctx, ownerID, domain.SendChannelMessageRequest{UserID: ownerID, ChannelID: created.Channel.ID, RandomID: 701, Message: "caption", Media: media, Date: now})
					if err != nil {
						t.Fatal(err)
					}
					id = sent.Message.ID
				}
				req := &tg.MessagesEditMessageRequest{Peer: peer, ID: id}
				req.SetMessage("")
				updates, err := r.onMessagesEditMessage(ctx, req)
				if kind == "text" {
					if err == nil || !strings.Contains(err.Error(), "MESSAGE_EMPTY") {
						t.Fatalf("empty text err=%v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				var message *tg.Message
				for _, update := range updates.(*tg.Updates).Updates {
					switch v := update.(type) {
					case *tg.UpdateEditMessage:
						message = v.Message.(*tg.Message)
						if v.PtsCount != 1 {
							t.Fatalf("edit pts_count=%d", v.PtsCount)
						}
					case *tg.UpdateEditChannelMessage:
						message = v.Message.(*tg.Message)
						if v.PtsCount != 1 {
							t.Fatalf("edit pts_count=%d", v.PtsCount)
						}
					}
				}
				if message == nil || message.Message != "" || len(message.Entities) != 0 || message.Media == nil {
					t.Fatalf("edited message=%+v", message)
				}
				if destination == "private" {
					list, err := messages.GetMessages(ctx, peerID, []int{recipientID})
					if err != nil || len(list.Messages) != 1 || list.Messages[0].Body != "" || !reflect.DeepEqual(list.Messages[0].Media, media) {
						t.Fatalf("recipient=%+v, err=%v", list, err)
					}
				}
			})
		}
	}
}
