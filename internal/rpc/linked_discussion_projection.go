package rpc

import (
	"context"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

type linkedDiscussionChannelProvider interface {
	GetLinkedDiscussionChannel(ctx context.Context, userID, sourceChannelID int64) (domain.ChannelView, error)
}

// linkedDiscussionChat projects the linked megagroup through the source
// broadcast. Generic channel lookup must keep rejecting private non-members;
// this narrow path is what lets TDesktop resolve ChannelFull.linked_chat_id.
func (r *Router) linkedDiscussionChat(ctx context.Context, userID, sourceChannelID int64) (tg.ChatClass, bool) {
	provider, ok := r.deps.Channels.(linkedDiscussionChannelProvider)
	if !ok || userID == 0 || sourceChannelID == 0 {
		return nil, false
	}
	view, err := provider.GetLinkedDiscussionChannel(ctx, userID, sourceChannelID)
	if err != nil || view.Channel.ID == 0 {
		return nil, false
	}
	return tgChannelChatForView(userID, view), true
}

func (r *Router) appendLinkedDiscussionChat(ctx context.Context, userID, sourceChannelID int64, chats []tg.ChatClass) []tg.ChatClass {
	chat, ok := r.linkedDiscussionChat(ctx, userID, sourceChannelID)
	if !ok {
		return chats
	}
	return replaceTGChat(chats, chat)
}

func replaceTGChat(chats []tg.ChatClass, replacement tg.ChatClass) []tg.ChatClass {
	wanted := tgChatID(replacement)
	if wanted == 0 {
		return chats
	}
	out := make([]tg.ChatClass, 0, len(chats)+1)
	for _, chat := range chats {
		if tgChatID(chat) != wanted {
			out = append(out, chat)
		}
	}
	return append(out, replacement)
}

func tgChatID(chat tg.ChatClass) int64 {
	switch item := chat.(type) {
	case *tg.Channel:
		return item.ID
	case *tg.ChannelForbidden:
		return item.ID
	case *tg.Chat:
		return item.ID
	case *tg.ChatForbidden:
		return item.ID
	default:
		return 0
	}
}
