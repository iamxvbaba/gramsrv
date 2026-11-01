package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// Regression: replying to media (a photo) in an ordinary group used to fail with
// ErrReplyMessageIDInvalid. TDesktop additionally sets inputReplyToMessage.top_msg_id in
// messages.sendMedia (a value of 1 was observed), whereas messages.sendMessage does not
// send it. top_msg_id is a topic hint, not a validity condition for the reply, so a
// mismatch with the server's thread root must not reject a reply_to_msg_id that uniquely
// points at an existing message.
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

	// top_msg_id=1 -- as it arrives from TDesktop in sendMedia.
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
			// The thread root is always derived by the server from the target message, not taken from the client hint.
			if want := replyTarget.Message.ID; res.Message.ReplyTo.TopMessageID != want {
				t.Fatalf("computed top_msg_id = %d, want %d", res.Message.ReplyTo.TopMessageID, want)
			}
		})
	}

	// Regression guard: a genuinely non-existent reply target must still be rejected.
	if _, err := store.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channel.Channel.ID, RandomID: 909, Date: 1700000004, Message: "bad",
		ReplyTo: &domain.MessageReply{MessageID: 999999, Peer: channelPeer},
	}); !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("missing reply target err = %v, want ErrReplyMessageIDInvalid", err)
	}
}
