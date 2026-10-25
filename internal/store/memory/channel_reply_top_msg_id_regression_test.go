package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// Регрессия: ответ медиа (фото) в обычной группе раньше падал с
// ErrReplyMessageIDInvalid. TDesktop в messages.sendMedia дополнительно
// проставляет inputReplyToMessage.top_msg_id (наблюдалось значение 1), тогда как
// messages.sendMessage его не шлёт. top_msg_id — подсказка топика, а не условие
// валидности ответа, поэтому расхождение с серверным thread root не должно
// отвергать reply_to_msg_id, который однозначно указывает на существующее сообщение.
func TestChannelSendReplyIgnoresClientTopMsgIDMismatch(t *testing.T) {
	ctx := context.Background()
	store := NewChannelStore()
	users := NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 11, Phone: "+15550007001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 12, Phone: "+15550007002", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	channel, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Group",
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: channel.Channel.ID}

	root, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: member.ID, ChannelID: channel.Channel.ID, RandomID: 101, Message: "root", Date: 1700000001,
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	replyTarget, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channel.Channel.ID, RandomID: 102, Message: "a photo", Date: 1700000002,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 71, AccessHash: 7}},
	})
	if err != nil {
		t.Fatalf("send photo: %v", err)
	}

	// top_msg_id=1 — как приходит от TDesktop в sendMedia.
	for name, topMsgID := range map[string]int{"top_msg_id=1": 1, "top_msg_id=target": root.Message.ID, "top_msg_id=0": 0} {
		t.Run(name, func(t *testing.T) {
			res, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
				UserID: owner.ID, ChannelID: channel.Channel.ID, RandomID: int64(200 + topMsgID), Date: 1700000003,
				Message: "reply", ReplyTo: &domain.MessageReply{
					MessageID: replyTarget.Message.ID, TopMessageID: topMsgID, Peer: channelPeer,
				},
			})
			if err != nil {
				t.Fatalf("reply with %s rejected: %v", name, err)
			}
			if res.Message.ReplyTo == nil || res.Message.ReplyTo.MessageID != replyTarget.Message.ID {
				t.Fatalf("reply projection = %+v, want message id %d", res.Message.ReplyTo, replyTarget.Message.ID)
			}
			// 线程根始终由服务端从目标消息推导,不采用客户端提示值。
			if want := replyTarget.Message.ID; res.Message.ReplyTo.TopMessageID != want {
				t.Fatalf("computed top_msg_id = %d, want %d", res.Message.ReplyTo.TopMessageID, want)
			}
		})
	}

	// 回归护栏: 真正不存在的 reply 目标仍然必须被拒绝。
	if _, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channel.Channel.ID, RandomID: 909, Date: 1700000004, Message: "bad",
		ReplyTo: &domain.MessageReply{MessageID: 999999, Peer: channelPeer},
	}); !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("missing reply target err = %v, want ErrReplyMessageIDInvalid", err)
	}
}
