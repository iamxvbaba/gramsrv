package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// Regression (postgres semantics): top_msg_id must not be a reason for rejection.
// In messages.sendMedia TDesktop sets inputReplyToMessage.top_msg_id (observed 1), while
// messages.sendMessage does not. Previously the mismatch with the server's thread root yielded
// ErrReplyMessageIDInvalid, i.e. "replying with a photo in a group" was impossible.
func TestChannelReplyTopMsgIDMismatchIsAccepted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 11, Phone: "+1555" + suffix + "01", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 12, Phone: "+1555" + suffix + "02", FirstName: "Member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "TopMsg " + suffix, Megagroup: true,
		MemberUserIDs: []int64{member.ID}, Date: 1700000300,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}

	root, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: member.ID, ChannelID: channelID, RandomID: 7001, Message: "root", Date: 1700000301,
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	photoTarget, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, RandomID: 7002, Date: 1700000302,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 77, AccessHash: 7}},
	})
	if err != nil {
		t.Fatalf("send photo: %v", err)
	}

	// top_msg_id=1 — exactly what TDesktop sends for a media reply.
	res, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: member.ID, ChannelID: channelID, RandomID: 7003, Date: 1700000303, Message: "reply",
		ReplyTo: &domain.MessageReply{MessageID: photoTarget.Message.ID, TopMessageID: 1, Peer: channelPeer},
	})
	if err != nil {
		t.Fatalf("reply with foreign top_msg_id rejected: %v", err)
	}
	if res.Message.ReplyTo == nil || res.Message.ReplyTo.MessageID != photoTarget.Message.ID {
		t.Fatalf("reply projection = %+v, want message id %d", res.Message.ReplyTo, photoTarget.Message.ID)
	}
	if want := photoTarget.Message.ID; res.Message.ReplyTo.TopMessageID != want {
		t.Fatalf("computed top_msg_id = %d, want server-derived %d", res.Message.ReplyTo.TopMessageID, want)
	}
	_ = root

	// Guardrail: a nonexistent target is still rejected.
	if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: member.ID, ChannelID: channelID, RandomID: 7004, Date: 1700000304, Message: "bad",
		ReplyTo: &domain.MessageReply{MessageID: 999999, Peer: channelPeer},
	}); !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
		t.Fatalf("missing reply target err = %v, want ErrReplyMessageIDInvalid", err)
	}
}
