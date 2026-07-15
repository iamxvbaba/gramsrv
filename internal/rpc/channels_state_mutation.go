package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

type channelStateMutation func(ctx context.Context, userID, channelID int64) (domain.Channel, error)

func (r *Router) applyChannelAdminStateMutation(ctx context.Context, input tg.InputChannelClass, mutate channelStateMutation) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	channel, err := mutate(ctx, userID, channelID)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	return r.channelStateMutationUpdates(ctx, userID, channel), nil
}

type channelChangeInfoMutation func(ctx context.Context, userID int64, view domain.ChannelView) (domain.Channel, error)

func (r *Router) applyChannelChangeInfoMutation(ctx context.Context, input tg.InputChannelClass, mutate channelChangeInfoMutation) (tg.UpdatesClass, error) {
	userID, view, err := r.channelChangeInfoView(ctx, input)
	if err != nil {
		return nil, err
	}
	channel, err := mutate(ctx, userID, view)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	return r.channelStateMutationUpdates(ctx, userID, channel), nil
}

func (r *Router) channelStateMutationUpdates(ctx context.Context, userID int64, channel domain.Channel) tg.UpdatesClass {
	r.invalidateRPCProjectionForChannel(channel.ID)
	if channel.LinkedMonoforumID != 0 {
		r.invalidateRPCProjectionForChannel(channel.LinkedMonoforumID)
	}
	mono, includeMono := r.linkedMonoforumForChannelState(ctx, userID, channel)
	r.pushChannelStateToMembersWithLinkedMonoforum(ctx, userID, channel, mono, includeMono)
	return r.channelStateUpdatesWithLinkedMonoforum(userID, channel, mono, includeMono)
}

func (r *Router) channelPaidMessagesPriceUpdates(ctx context.Context, userID int64, res domain.ChannelPaidMessagesPriceResult) tg.UpdatesClass {
	state := r.channelStateMutationUpdates(ctx, userID, res.Channel)
	services := res.ServiceMessages
	if len(services) == 0 && res.ServiceMessage != nil {
		services = []domain.SendChannelMessageResult{*res.ServiceMessage}
	}
	if len(services) == 0 {
		return state
	}
	service := r.channelMessagesUpdatesWithPeerCache(ctx, userID, services, nil, false, nil, newViewerPeerCache(r))
	if updates, ok := state.(*tg.Updates); ok {
		return mergeUpdates(updates, service)
	}
	return state
}

func mergeUpdates(a, b *tg.Updates) *tg.Updates {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	a.Updates = append(a.Updates, b.Updates...)
	a.Users = appendUniqueTGUsers(a.Users, b.Users...)
	a.Chats = appendUniqueTGChats(a.Chats, b.Chats...)
	if b.Date > a.Date {
		a.Date = b.Date
	}
	return a
}
