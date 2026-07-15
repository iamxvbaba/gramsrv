package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"telesrv/internal/domain"
)

func (r *Router) onMessagesDeleteParticipantReaction(ctx context.Context, req *tg.MessagesDeleteParticipantReactionRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	participant, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Participant)
	if err != nil {
		return nil, err
	}
	if participant.Type != domain.PeerTypeUser || participant.ID == 0 {
		return nil, userIDInvalidErr()
	}
	if peer.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	moderator, ok := r.deps.Channels.(channelParticipantReactionModerator)
	if !ok {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	res, err := moderator.DeleteParticipantReaction(ctx, userID, domain.DeleteChannelParticipantReactionRequest{
		UserID:            userID,
		ChannelID:         peer.ID,
		MessageID:         req.MsgID,
		ParticipantUserID: participant.ID,
		Date:              int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	updates := r.channelMessageReactionsUpdates(ctx, userID, res)
	moderationIDs := []int{res.Message.ID}
	r.pushChannelViewerUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelReactionsViewerUpdates(ctx, userID, viewerUserID, res, moderationIDs)
	})
	return updates, nil
}

func (r *Router) onMessagesDeleteParticipantReactions(ctx context.Context, req *tg.MessagesDeleteParticipantReactionsRequest) (bool, error) {
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return false, err
	}
	participant, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Participant)
	if err != nil {
		return false, err
	}
	if participant.Type != domain.PeerTypeUser || participant.ID == 0 {
		return false, userIDInvalidErr()
	}
	if peer.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return true, nil
	}
	moderator, ok := r.deps.Channels.(channelParticipantReactionModerator)
	if !ok {
		return false, channelInvalidErr(domain.ErrChannelInvalid)
	}
	res, err := moderator.DeleteParticipantReactions(ctx, userID, domain.DeleteChannelParticipantReactionsRequest{
		UserID:            userID,
		ChannelID:         peer.ID,
		ParticipantUserID: participant.ID,
		Limit:             domain.MaxDeleteParticipantReactionsBatch,
		Date:              int(r.clock.Now().Unix()),
	})
	if err != nil {
		return false, channelInvalidErr(err)
	}
	if len(res.Messages) > 0 {
		reactionRes := domain.ChannelMessageReactionsResult{
			Channel:    res.Channel,
			Messages:   res.Messages,
			Recipients: res.Recipients,
		}
		ids := make([]int, 0, len(res.Messages))
		for _, msg := range res.Messages {
			if msg.ID > 0 {
				ids = append(ids, msg.ID)
			}
		}
		r.pushChannelViewerUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
			return r.channelReactionsViewerUpdates(ctx, userID, viewerUserID, reactionRes, ids)
		})
	}
	return true, nil
}

func (r *Router) onMessagesGetMessagesReactions(ctx context.Context, req *tg.MessagesGetMessagesReactionsRequest) (tg.UpdatesClass, error) {
	if len(req.ID) > maxGetMessagesIDs {
		return nil, limitInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		res, err := r.deps.Channels.GetMessageReactions(ctx, userID, domain.ChannelMessageReactionsRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			IDs:       append([]int(nil), req.ID...),
		})
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		return r.channelMessagesReactionsUpdates(ctx, userID, res, req.ID), nil
	}
	if peer.Type == domain.PeerTypeUser && r.deps.Messages != nil {
		res, err := r.deps.Messages.GetMessageReactions(ctx, userID, domain.PrivateMessageReactionsRequest{
			OwnerUserID: userID,
			Peer:        peer,
			IDs:         append([]int(nil), req.ID...),
		})
		if err != nil {
			return nil, messageReactionErr(err)
		}
		return r.privateMessagesReactionsUpdates(ctx, userID, peer, res, req.ID), nil
	}
	updates := make([]tg.UpdateClass, 0, len(req.ID))
	tgPeer := tgPeer(peer)
	for _, msgID := range req.ID {
		if msgID <= 0 || msgID > domain.MaxMessageBoxID {
			return nil, messageIDInvalidErr()
		}
		updates = append(updates, &tg.UpdateMessageReactions{
			Peer:  tgPeer,
			MsgID: msgID,
			Reactions: tg.MessageReactions{
				Results: []tg.ReactionCount{},
			},
		})
	}
	return &tg.Updates{
		Updates: updates,
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}, nil
}

func (r *Router) onMessagesGetMessageReactionsList(ctx context.Context, req *tg.MessagesGetMessageReactionsListRequest) (*tg.MessagesMessageReactionsList, error) {
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if req.Limit < 0 || req.Limit > maxSearchResultsLimit {
		return nil, limitInvalidErr()
	}
	if offset, ok := req.GetOffset(); ok && len(offset) > maxReactionListOffset {
		return nil, limitInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		filter, err := optionalDomainMessageReaction(req.Reaction)
		if err != nil {
			return nil, err
		}
		res, err := r.deps.Channels.ListMessageReactions(ctx, userID, domain.ChannelMessageReactionsListRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			MessageID: req.ID,
			Reaction:  filter,
			Offset:    optionalString(req.GetOffset),
			Limit:     req.Limit,
		})
		if errors.Is(err, domain.ErrChannelRightForbidden) {
			return nil, tgerr.New(403, "BROADCAST_FORBIDDEN")
		}
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		userIDs := make([]int64, 0, len(res.Reactions))
		reactions := make([]tg.MessagePeerReaction, 0, len(res.Reactions))
		for _, item := range res.Reactions {
			if item.UserID != 0 {
				userIDs = append(userIDs, item.UserID)
			}
			if converted := tgMessagePeerReaction(userID, item); converted != nil {
				reactions = append(reactions, *converted)
			}
		}
		out := &tg.MessagesMessageReactionsList{
			Count:     res.Count,
			Reactions: reactions,
			Chats:     tgChannels(userID, []domain.Channel{res.Channel}),
			Users:     r.tgUsersForIDs(ctx, userID, userIDs),
		}
		if res.NextOffset != "" {
			out.SetNextOffset(res.NextOffset)
		}
		return r.applyStoryMaxIDsToMessageReactionsList(ctx, userID, out), nil
	}
	if peer.Type == domain.PeerTypeUser && r.deps.Messages != nil {
		filter, err := optionalDomainMessageReaction(req.Reaction)
		if err != nil {
			return nil, err
		}
		res, err := r.deps.Messages.GetMessageReactions(ctx, userID, domain.PrivateMessageReactionsRequest{
			OwnerUserID: userID,
			Peer:        peer,
			IDs:         []int{req.ID},
		})
		if err != nil {
			return nil, messageReactionErr(err)
		}
		var source domain.ChannelMessageReactions
		if len(res.Messages) > 0 && res.Messages[0].Reactions != nil {
			source = *res.Messages[0].Reactions
		} else {
			source = res.Reactions
		}
		limit := req.Limit
		if limit <= 0 || limit > len(source.Recent) {
			limit = len(source.Recent)
		}
		userIDs := []int64{userID, peer.ID}
		reactions := make([]tg.MessagePeerReaction, 0, limit)
		count := 0
		for _, item := range source.Recent {
			if filter != nil && item.Reaction.Key() != filter.Key() {
				continue
			}
			count++
			if len(reactions) >= limit {
				continue
			}
			if item.UserID != 0 {
				userIDs = append(userIDs, item.UserID)
			}
			if converted := tgMessagePeerReaction(userID, item); converted != nil {
				reactions = append(reactions, *converted)
			}
		}
		return r.applyStoryMaxIDsToMessageReactionsList(ctx, userID, &tg.MessagesMessageReactionsList{
			Count:     count,
			Reactions: reactions,
			Chats:     r.chatsForInputPeer(ctx, userID, req.Peer),
			Users:     r.tgUsersForIDs(ctx, userID, userIDs),
		}), nil
	}
	return r.applyStoryMaxIDsToMessageReactionsList(ctx, userID, &tg.MessagesMessageReactionsList{
		Count:     0,
		Reactions: []tg.MessagePeerReaction{},
		Chats:     r.chatsForInputPeer(ctx, userID, req.Peer),
		Users:     []tg.UserClass{},
	}), nil
}

func (r *Router) onMessagesGetUnreadReactions(ctx context.Context, req *tg.MessagesGetUnreadReactionsRequest) (tg.MessagesMessagesClass, error) {
	if err := validateHistoryBounds(req.OffsetID, req.AddOffset, req.Limit, req.MaxID, req.MinID); err != nil {
		return nil, err
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, req.GetSavedPeerID)
	if err != nil {
		return nil, err
	}
	if topMsgID, ok := req.GetTopMsgID(); ok && (topMsgID < 0 || topMsgID > domain.MaxMessageBoxID) {
		return nil, messageIDInvalidErr()
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		history, err := r.deps.Channels.GetUnreadReactions(ctx, userID, domain.ChannelUnreadReactionsFilter{
			ChannelID: peer.ID,
			TopMsgID:  req.TopMsgID,
			OffsetID:  req.OffsetID,
			AddOffset: req.AddOffset,
			Limit:     req.Limit,
			MaxID:     req.MaxID,
			MinID:     req.MinID,
		})
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		return r.tgChannelHistoryMessages(ctx, userID, r.enrichChannelHistory(ctx, userID, history)), nil
	}
	if peer.Type == domain.PeerTypeUser && r.deps.Messages != nil {
		messages, err := r.deps.Messages.ListUnreadReactionMessages(ctx, userID, peer, req.Limit)
		if err != nil {
			return nil, internalErr()
		}
		out := &tg.MessagesMessages{
			Messages: make([]tg.MessageClass, 0, len(messages)),
			Topics:   []tg.ForumTopicClass{},
			Chats:    r.chatsForInputPeer(ctx, userID, req.Peer),
			Users:    r.usersForMessageUpdates(ctx, userID, messages),
		}
		for _, msg := range messages {
			if item := tgMessage(msg); item != nil {
				out.Messages = append(out.Messages, item)
			}
		}
		r.applyStoryMaxIDsToMessages(ctx, userID, out)
		return out, nil
	}
	out := &tg.MessagesMessages{
		Messages: []tg.MessageClass{},
		Topics:   []tg.ForumTopicClass{},
		Chats:    r.chatsForInputPeer(ctx, userID, req.Peer),
		Users:    []tg.UserClass{},
	}
	r.applyStoryMaxIDsToMessages(ctx, userID, out)
	return out, nil
}

func (r *Router) onMessagesReadReactions(ctx context.Context, req *tg.MessagesReadReactionsRequest) (*tg.MessagesAffectedHistory, error) {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if savedPeer, ok := req.GetSavedPeerID(); ok && savedPeer != nil {
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, savedPeer); err != nil {
			return nil, err
		}
	}
	if topMsgID, ok := req.GetTopMsgID(); ok && (topMsgID < 0 || topMsgID > domain.MaxMessageBoxID) {
		return nil, messageIDInvalidErr()
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		res, err := r.deps.Channels.ReadReactions(ctx, userID, domain.ReadChannelReactionsRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			TopMsgID:  req.TopMsgID,
			Limit:     domain.MaxChannelReadReactionsBatch,
		})
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		return &tg.MessagesAffectedHistory{Pts: res.ChannelPts, PtsCount: 0, Offset: res.Offset}, nil
	}
	if peer.Type == domain.PeerTypeUser && r.deps.Messages != nil {
		if _, err := r.deps.Messages.ReadPeerReactions(ctx, userID, peer); err != nil {
			return nil, internalErr()
		}
	}
	return r.affectedHistory(ctx, authKeyID, userID, 0)
}
