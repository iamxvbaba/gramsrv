package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestPrivatePollAndDeliveryEffectRollbackTogetherMemory(t *testing.T) {
	ctx := context.Background()
	const aliceID, bobID = int64(1001), int64(1002)
	polls := NewPollStore()
	outbox := NewDeliveryOutboxStore()
	messages := NewMessageStore()
	messages.AttachPollStore(polls)
	messages.AttachDeliveryOutbox(outbox)
	pollID := int64(991001)
	option := []byte{1}
	if err := polls.CreatePoll(ctx, domain.PollDefinition{
		ID: pollID, CreatorUserID: aliceID, Options: [][]byte{option}, PublicVoters: true,
	}); err != nil {
		t.Fatalf("create poll: %v", err)
	}
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: aliceID, RecipientUserID: bobID, RandomID: 1, Date: 100,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPoll, Poll: &domain.MessagePoll{
			ID: pollID, Question: "atomic?", Answers: []domain.MessagePollAnswer{{Text: "yes", Option: option}},
		}},
	})
	if err != nil {
		t.Fatalf("send poll: %v", err)
	}
	projectionErr := errors.New("poll projection failed")
	_, err = messages.VoteMessagePoll(ctx, domain.VotePrivateMessagePollRequest{
		UserID: bobID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
		MessageID: sent.RecipientMessage.ID, Options: [][]byte{option}, Date: 101,
	}, func(domain.PrivateMessagePollResult) ([]store.DeliveryEffect, error) { return nil, projectionErr })
	if !errors.Is(err, projectionErr) {
		t.Fatalf("failed poll vote err = %v, want projection error", err)
	}
	if _, ok := polls.votes[pollID][bobID]; ok {
		t.Fatal("poll vote survived failed delivery builder")
	}
	if len(outbox.items) != 0 {
		t.Fatalf("failed poll vote outbox items = %d, want 0", len(outbox.items))
	}

	res, err := messages.VoteMessagePoll(ctx, domain.VotePrivateMessagePollRequest{
		UserID: bobID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
		MessageID: sent.RecipientMessage.ID, Options: [][]byte{option}, Date: 102,
	}, func(result domain.PrivateMessagePollResult) ([]store.DeliveryEffect, error) {
		effects := make([]store.DeliveryEffect, 0, len(result.Messages))
		for _, message := range result.Messages {
			effects = append(effects, store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
				TargetUserID: message.OwnerUserID, Payload: []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
			}))
		}
		return effects, nil
	})
	if err != nil {
		t.Fatalf("successful poll vote: %v", err)
	}
	if len(res.Messages) != 2 || len(outbox.items) != 2 {
		t.Fatalf("poll result owners/outbox = %d/%d, want 2/2", len(res.Messages), len(outbox.items))
	}
}

func TestPrivateReactionAndDeliveryEffectRollbackTogetherMemory(t *testing.T) {
	ctx := context.Background()
	const aliceID, bobID = int64(2001), int64(2002)
	outbox := NewDeliveryOutboxStore()
	messages := NewMessageStore()
	messages.AttachDeliveryOutbox(outbox)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: aliceID, RecipientUserID: bobID, RandomID: 2, Message: "react", Date: 200,
	})
	if err != nil {
		t.Fatalf("send private text: %v", err)
	}
	projectionErr := errors.New("reaction projection failed")
	_, err = messages.SetMessageReactions(ctx, domain.SetPrivateMessageReactionsRequest{
		UserID: bobID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: aliceID},
		MessageID: sent.RecipientMessage.ID,
		Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "👍"}}, Date: 201,
	}, func(domain.PrivateMessageReactionsResult) ([]store.DeliveryEffect, error) { return nil, projectionErr })
	if !errors.Is(err, projectionErr) {
		t.Fatalf("failed reaction err = %v, want projection error", err)
	}
	got, err := messages.GetMessageReactions(ctx, domain.PrivateMessageReactionsRequest{
		OwnerUserID: bobID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: aliceID}, IDs: []int{sent.RecipientMessage.ID},
	})
	if err != nil {
		t.Fatalf("get reactions after rollback: %v", err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("reaction rollback message count = %d, want 1", len(got.Messages))
	}
	if len(got.Reactions.Results) != 0 || len(outbox.items) != 0 {
		t.Fatalf("failed reaction facts = reactions %+v outbox %d", got.Reactions, len(outbox.items))
	}
}

func TestPaidReactionPrivacyAndDeliveryEffectRollbackTogetherMemory(t *testing.T) {
	ctx := context.Background()
	const userID = int64(3001)
	outbox := NewDeliveryOutboxStore()
	passwords := NewPasswordStore()
	passwords.AttachDeliveryOutbox(outbox)
	settings := domain.DefaultAccountReactionSettings()
	settings.PaidPrivacy = domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyAnonymous}
	projectionErr := errors.New("paid privacy projection failed")
	patch := domain.AccountReactionSettingsPatch{PaidPrivacy: &settings.PaidPrivacy}
	if _, err := passwords.UpdateReactionSettings(ctx, userID, patch, func(domain.AccountReactionSettings) ([]store.DeliveryEffect, error) {
		return nil, projectionErr
	}); !errors.Is(err, projectionErr) {
		t.Fatalf("failed paid privacy err = %v, want projection error", err)
	}
	if _, found, err := passwords.GetReactionSettings(ctx, userID); err != nil || found {
		t.Fatalf("paid privacy survived failed builder: found=%v err=%v", found, err)
	}
	if len(outbox.items) != 0 {
		t.Fatalf("failed paid privacy outbox items = %d, want 0", len(outbox.items))
	}
	if _, err := passwords.UpdateReactionSettings(ctx, userID, patch, func(domain.AccountReactionSettings) ([]store.DeliveryEffect, error) {
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: userID, Payload: []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}); err != nil {
		t.Fatalf("save paid privacy: %v", err)
	}
	stored, found, err := passwords.GetReactionSettings(ctx, userID)
	if err != nil || !found || stored.PaidPrivacy.Kind != domain.PaidReactionPrivacyAnonymous {
		t.Fatalf("stored paid privacy = %+v found=%v err=%v", stored, found, err)
	}
	if len(outbox.items) != 1 {
		t.Fatalf("paid privacy outbox items = %d, want 1", len(outbox.items))
	}
}

func TestChannelMessageContentsAndDeliveryEffectRollbackTogetherMemory(t *testing.T) {
	ctx := context.Background()
	const ownerID, memberID = int64(4001), int64(4002)
	outbox := NewDeliveryOutboxStore()
	channels := NewChannelStore()
	channels.AttachDeliveryOutbox(outbox)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: ownerID, Title: "content atomic", Megagroup: true,
		MemberUserIDs: []int64{memberID}, Date: 400,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: ownerID, ChannelID: created.Channel.ID, RandomID: 4, Message: "watch", Date: 401,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	if _, err := channels.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{
		UserID: memberID, ChannelID: created.Channel.ID, MessageID: sent.Message.ID,
		Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "👍"}}, Date: 402,
	}); err != nil {
		t.Fatalf("set channel reaction: %v", err)
	}
	projectionErr := errors.New("channel contents projection failed")
	_, err = channels.ReadChannelMessageContents(ctx, domain.ReadChannelMessageContentsRequest{
		UserID: ownerID, ChannelID: created.Channel.ID, IDs: []int{sent.Message.ID},
	}, func(domain.ReadChannelMessageContentsResult) ([]store.DeliveryEffect, error) {
		return nil, projectionErr
	})
	if !errors.Is(err, projectionErr) {
		t.Fatalf("failed channel contents err = %v, want projection error", err)
	}
	unread, err := channels.ListChannelUnreadReactions(ctx, ownerID, domain.ChannelUnreadReactionsFilter{
		ChannelID: created.Channel.ID, Limit: 10,
	})
	if err != nil || len(unread.Messages) != 1 {
		t.Fatalf("unread reaction after rollback = %+v err=%v, want one", unread.Messages, err)
	}
	if len(outbox.items) != 0 {
		t.Fatalf("failed channel contents outbox items = %d, want 0", len(outbox.items))
	}
	if _, err := channels.ReadChannelMessageContents(ctx, domain.ReadChannelMessageContentsRequest{
		UserID: ownerID, ChannelID: created.Channel.ID, IDs: []int{sent.Message.ID},
	}, func(domain.ReadChannelMessageContentsResult) ([]store.DeliveryEffect, error) {
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: ownerID, Payload: []byte{1}, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}); err != nil {
		t.Fatalf("read channel contents with delivery: %v", err)
	}
	unread, err = channels.ListChannelUnreadReactions(ctx, ownerID, domain.ChannelUnreadReactionsFilter{
		ChannelID: created.Channel.ID, Limit: 10,
	})
	if err != nil || len(unread.Messages) != 0 || len(outbox.items) != 1 {
		t.Fatalf("committed channel contents facts = unread %+v outbox=%d err=%v", unread.Messages, len(outbox.items), err)
	}
}
