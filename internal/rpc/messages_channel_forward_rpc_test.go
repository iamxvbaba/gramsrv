package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestForwardChannelMessageToUserIncludesSourceChannelChat(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 33, Phone: "15550002033", FirstName: "Owner"})
	recipient, _ := userStore.Create(ctx, domain.User{AccessHash: 34, Phone: "15550002034", FirstName: "Recipient"})
	channelStore := memory.NewChannelStore()
	messageStore := memory.NewMessageStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(channelStore),
		Messages: appmessages.NewService(messageStore, nil),
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Source Channel",
		About:     "forward source",
		Broadcast: true,
	})
	if err != nil {
		t.Fatalf("create source channel: %v", err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	sent, err := r.onMessagesSendMessage(WithUserID(ctx, owner.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Message:  "source post",
		RandomID: 4001,
	})
	if err != nil {
		t.Fatalf("send source channel message: %v", err)
	}
	msgID := sent.(*tg.Updates).Updates[0].(*tg.UpdateMessageID).ID
	forwarded, err := r.onMessagesForwardMessages(WithUserID(ctx, owner.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		ToPeer:   &tg.InputPeerUser{UserID: recipient.ID, AccessHash: recipient.AccessHash},
		ID:       []int{msgID},
		RandomID: []int64{4002},
	})
	if err != nil {
		t.Fatalf("forward channel to user: %v", err)
	}
	updates := forwarded.(*tg.Updates)
	if len(updates.Chats) != 1 || updates.Chats[0].(*tg.Channel).ID != channel.ID {
		t.Fatalf("forward updates chats = %+v, want source channel chat", updates.Chats)
	}
	newMsg := updates.Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)
	if from, ok := newMsg.FwdFrom.FromID.(*tg.PeerChannel); !ok || from.ChannelID != channel.ID {
		t.Fatalf("forward header = %#v, want source channel peer", newMsg.FwdFrom)
	}
}
