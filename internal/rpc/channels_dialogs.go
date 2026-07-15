package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) onChannelsGetLeftChannels(ctx context.Context, offset int) (tg.MessagesChatsClass, error) {
	if offset < 0 || offset > domain.MaxLeftChannelsOffset {
		return nil, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return &tg.MessagesChats{Chats: []tg.ChatClass{}}, nil
	}
	list, err := r.deps.Channels.LeftChannels(ctx, userID, offset, domain.MaxLeftChannelsLimit)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	chats := make([]tg.ChatClass, 0, len(list.Channels))
	for _, item := range list.Channels {
		chats = append(chats, tgChannelChat(userID, item.Channel, &item.Self))
	}
	if len(chats) == 0 && list.Count > 0 {
		return &tg.MessagesChatsSlice{Count: list.Count, Chats: chats}, nil
	}
	if offset+len(chats) < list.Count {
		return &tg.MessagesChatsSlice{Count: list.Count, Chats: chats}, nil
	}
	return &tg.MessagesChats{Chats: chats}, nil
}

func (r *Router) onChannelsGetInactiveChannels(ctx context.Context) (*tg.MessagesInactiveChats, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return &tg.MessagesInactiveChats{Dates: []int{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	list, err := r.deps.Channels.InactiveChannels(ctx, userID, domain.MaxInactiveChannelsLimit)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	dates := make([]int, 0, len(list.Channels))
	chats := make([]tg.ChatClass, 0, len(list.Channels))
	for i, channel := range list.Channels {
		date := channel.Date
		if i < len(list.Dialogs) && list.Dialogs[i].TopMessageDate > 0 {
			date = list.Dialogs[i].TopMessageDate
		}
		dates = append(dates, date)
		chats = append(chats, tgChannelChatMin(userID, channel))
	}
	return &tg.MessagesInactiveChats{Dates: dates, Chats: chats, Users: []tg.UserClass{}}, nil
}

func (r *Router) onChannelsGetGroupsForDiscussion(ctx context.Context) (tg.MessagesChatsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Channels == nil {
		return &tg.MessagesChats{Chats: []tg.ChatClass{}}, nil
	}
	channels, err := r.deps.Channels.DiscussionGroups(ctx, userID, domain.MaxDiscussionGroupsLimit)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	return &tg.MessagesChats{Chats: tgChannels(userID, channels)}, nil
}
