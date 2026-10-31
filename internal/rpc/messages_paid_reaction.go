package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// channelPaidReactionService is an optional extension of r.deps.Channels
// (implemented by app/channels.Service), wired in via type assertion to avoid
// adding a paid-reaction-only method to the ChannelsService interface.
type channelPaidReactionService interface {
	SendPaidReaction(ctx context.Context, userID int64, req domain.SendChannelPaidReactionRequest) (domain.ChannelMessagePaidReactionResult, error)
}

type channelPaidReactionReplayService interface {
	ReplayPaidReaction(ctx context.Context, userID int64, req domain.SendChannelPaidReactionRequest) (domain.ChannelMessagePaidReactionResult, bool, error)
}

// channelPaidReactionUpdates builds an updateMessageReactions for a viewer: it
// merges the message's existing ordinary reactions with the paid ReactionPaid
// (total stars + top reactors). Non-requesters use min semantics (Min set, the
// client ignores chosen and keeps its local state) to avoid leaking the
// requester's view to others.
func (r *Router) channelPaidReactionUpdates(ctx context.Context, requestUserID, viewerUserID int64, res domain.ChannelMessagePaidReactionResult, ids []int) *tg.Updates {
	isRequester := viewerUserID == requestUserID
	base := domain.ChannelMessageReactions{}
	if res.Message.Reactions != nil {
		base = *res.Message.Reactions
	}
	paid := clonePaidForViewer(res.Paid, isRequester)
	base.Paid = &paid
	mr := tgMessageReactions(viewerUserID, &base)
	if mr == nil {
		mr = &tg.MessageReactions{Results: []tg.ReactionCount{}}
	}
	if !isRequester {
		mr.Min = true
	}
	msgID := res.Message.ID
	if msgID == 0 && len(ids) > 0 {
		msgID = ids[0]
	}
	update := &tg.UpdateMessageReactions{
		Peer:      &tg.PeerChannel{ChannelID: res.Channel.ID},
		MsgID:     msgID,
		Reactions: *mr,
	}
	channelIDs := paidReactorChannelIDs(paid)
	channels := make([]domain.Channel, 0, 1+len(res.DisplayChannels))
	channels = append(channels, res.Channel)
	seenChannels := map[int64]struct{}{res.Channel.ID: {}}
	for _, channel := range res.DisplayChannels {
		if channel.ID == 0 {
			continue
		}
		if _, seen := seenChannels[channel.ID]; seen {
			continue
		}
		seenChannels[channel.ID] = struct{}{}
		channels = append(channels, channel)
	}
	chats := tgChannels(viewerUserID, channels)
	missingChannelIDs := make([]int64, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		if _, seen := seenChannels[channelID]; !seen {
			missingChannelIDs = append(missingChannelIDs, channelID)
		}
	}
	chats = append(chats, r.tgChatsForChannelIDs(ctx, viewerUserID, missingChannelIDs)...)
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   r.tgUsersForIDs(ctx, viewerUserID, paidReactorUserIDs(paid)),
		Chats:   chats,
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

// clonePaidForViewer returns a copy of the paid aggregate; non-requesters have
// My/MyStars scrubbed (min semantics, preventing view leakage).
func clonePaidForViewer(paid domain.ChannelMessagePaidReactions, isRequester bool) domain.ChannelMessagePaidReactions {
	out := paid
	out.TopReactors = make([]domain.PaidReactor, len(paid.TopReactors))
	copy(out.TopReactors, paid.TopReactors)
	if !isRequester {
		out.MyStars = 0
		out.MyAnonymous = false
		for i := range out.TopReactors {
			out.TopReactors[i].My = false
		}
	}
	return out
}

// injectPaidReaction injects the paid reaction into MessageReactions: the
// ReactionPaid count goes first and the top reactors ranking is filled in.
// My/chosen are driven entirely by the paid view data (the caller has already
// scrubbed My/MyStars for other viewers' views).
func injectPaidReaction(mr *tg.MessageReactions, paid domain.ChannelMessagePaidReactions) {
	if paid.TotalStars <= 0 {
		return
	}
	paidCount := tg.ReactionCount{Reaction: &tg.ReactionPaid{}, Count: int(paid.TotalStars)}
	if paid.MyStars > 0 {
		// chosen_order only marks "this viewer has reacted"; the exact ordinal
		// does not matter, so set it positive.
		paidCount.SetChosenOrder(int(paid.MyStars))
	}
	mr.Results = append([]tg.ReactionCount{paidCount}, mr.Results...)

	reactors := make([]tg.MessageReactor, 0, len(paid.TopReactors))
	for _, rr := range paid.TopReactors {
		item := tg.MessageReactor{Count: int(rr.Stars)}
		if rr.My {
			item.My = true
		} else {
			item.Top = true
		}
		if rr.Anonymous {
			item.Anonymous = true
		}
		// PeerID: given for non-anonymous reactors; for anonymous ones only to the
		// reactor itself (TL semantics: for the anonymous self both anonymous and
		// peer are set).
		if (!rr.Anonymous || rr.My) && rr.DisplayPeer().ID != 0 {
			if peer := tgPeer(rr.DisplayPeer()); peer != nil {
				item.SetPeerID(peer)
			}
		}
		reactors = append(reactors, item)
	}
	if len(reactors) > 0 {
		mr.SetTopReactors(reactors)
	}
}

// paidReactorUserIDs collects visible user display identities. An anonymous
// reactor is visible only to itself, matching injectPaidReaction.
func paidReactorUserIDs(paid domain.ChannelMessagePaidReactions) []int64 {
	ids := make([]int64, 0, len(paid.TopReactors))
	for _, rr := range paid.TopReactors {
		peer := rr.DisplayPeer()
		if peer.Type == domain.PeerTypeUser && peer.ID != 0 && (!rr.Anonymous || rr.My) {
			ids = append(ids, peer.ID)
		}
	}
	return ids
}

func paidReactorChannelIDs(paid domain.ChannelMessagePaidReactions) []int64 {
	ids := make([]int64, 0, len(paid.TopReactors))
	for _, rr := range paid.TopReactors {
		peer := rr.DisplayPeer()
		if peer.Type == domain.PeerTypeChannel && peer.ID != 0 && (!rr.Anonymous || rr.My) {
			ids = append(ids, peer.ID)
		}
	}
	return uniqueInt64(ids)
}
