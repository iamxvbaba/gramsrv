package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) onChannelsReadMessageContents(ctx context.Context, req *tg.ChannelsReadMessageContentsRequest) (bool, error) {
	if r.deps.Channels == nil {
		return false, notImplementedErr()
	}
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return false, err
	}
	read, err := r.deps.Channels.ReadMessageContents(ctx, userID, domain.ReadChannelMessageContentsRequest{
		UserID:    userID,
		ChannelID: channelID,
		IDs:       req.ID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return false, messageIDInvalidErr()
		}
		return false, channelInvalidErr(err)
	}
	if ids := readChannelMessageContentIDs(read.Messages); len(ids) > 0 {
		r.pushUserUpdates(ctx, userID, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateChannelReadMessagesContents{
				ChannelID: read.Channel.ID,
				Messages:  ids,
			}},
			Users: []tg.UserClass{},
			Chats: []tg.ChatClass{tgChannelChatMin(userID, read.Channel)},
			Date:  int(r.clock.Now().Unix()),
			Seq:   0,
		})
	}
	if len(read.ClearedUnreadReactionMessageIDs) > 0 {
		r.pushUserUpdates(ctx, userID, r.channelMessagesReactionsUpdates(ctx, userID, domain.ChannelMessageReactionsResult{
			Channel:  read.Channel,
			Messages: read.Messages,
		}, read.ClearedUnreadReactionMessageIDs))
	}
	return true, nil
}

func (r *Router) onChannelsReadHistory(ctx context.Context, req *tg.ChannelsReadHistoryRequest) (bool, error) {
	if r.deps.Channels == nil {
		return true, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return false, err
	}
	read, err := r.deps.Channels.ReadHistory(ctx, userID, domain.ReadChannelHistoryRequest{
		UserID:    userID,
		ChannelID: channelID,
		MaxID:     req.MaxID,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return false, channelInvalidErr(err)
	}
	if _, err := r.recordChannelReadInbox(ctx, userID, read); err != nil {
		return false, err
	}
	r.pushChannelReadOutboxUpdates(ctx, read.ChannelID, read.OutboxUpdates)
	r.advanceForumGeneralReadAfterChannelRead(ctx, userID, read)
	return true, nil
}

func domainChannelReactionPolicy(req *tg.MessagesSetChatAvailableReactionsRequest, current domain.ChannelReactionPolicy, defaultReactionDocuments map[int64]string) (domain.ChannelReactionPolicy, error) {
	if req == nil || req.AvailableReactions == nil {
		return domain.ChannelReactionPolicy{}, tgerr400("REACTION_INVALID")
	}
	policy := domain.ChannelReactionPolicy{
		Limit:       current.Limit,
		PaidEnabled: current.PaidEnabled,
	}
	if limit, ok := req.GetReactionsLimit(); ok {
		if limit < 0 || limit > domain.MaxChannelReactionsLimit {
			return domain.ChannelReactionPolicy{}, limitInvalidErr()
		}
		policy.Limit = limit
	}
	if paidEnabled, ok := req.GetPaidEnabled(); ok {
		policy.PaidEnabled = paidEnabled
	}
	switch reactions := req.AvailableReactions.(type) {
	case *tg.ChatReactionsNone:
		policy.Type = domain.ChannelReactionPolicyNone
	case *tg.ChatReactionsAll:
		policy.Type = domain.ChannelReactionPolicyAll
		policy.AllowCustom = reactions.AllowCustom
	case *tg.ChatReactionsSome:
		policy.Type = domain.ChannelReactionPolicySome
		seen := make(map[string]struct{}, len(reactions.Reactions))
		for _, reaction := range reactions.Reactions {
			// TDesktop models the paid toggle as a pseudo reaction in the
			// selector and may submit reactionPaid in chatReactionsSome. The
			// durable paid state is carried by paid_enabled, not the normal
			// reaction whitelist.
			if paidReactionSentinel(reaction) {
				continue
			}
			parsed, err := domainMessageReactionFromTL(reaction)
			if err != nil {
				return domain.ChannelReactionPolicy{}, tgerr400("REACTION_INVALID")
			}
			parsed = normalizeDefaultReactionDocument(parsed, defaultReactionDocuments)
			key := parsed.Key()
			if _, ok := seen[key]; ok {
				continue
			}
			if len(seen) >= domain.MaxChannelReactionTypes {
				return domain.ChannelReactionPolicy{}, limitInvalidErr()
			}
			seen[key] = struct{}{}
			switch parsed.Type {
			case domain.MessageReactionEmoji:
				policy.Emoticons = append(policy.Emoticons, parsed.Emoticon)
			case domain.MessageReactionCustomEmoji:
				policy.CustomEmojiIDs = append(policy.CustomEmojiIDs, parsed.DocumentID)
			}
		}
	default:
		return domain.ChannelReactionPolicy{}, tgerr400("REACTION_INVALID")
	}
	return policy, nil
}

func paidReactionSentinel(reaction tg.ReactionClass) bool {
	paid, ok := reaction.(*tg.ReactionPaid)
	return ok && paid != nil
}
