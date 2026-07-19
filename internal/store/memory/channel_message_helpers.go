package memory

import (
	"telesrv/internal/domain"
)

func (s *ChannelStore) eventForMessageLocked(channelID int64, id int) domain.ChannelUpdateEvent {
	for _, event := range s.events[channelID] {
		if event.Message.ID == id {
			return cloneChannelEvent(event)
		}
	}
	return domain.ChannelUpdateEvent{}
}

func sameMessageEntities(a, b []domain.MessageEntity) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cloneChannelMessage(in domain.ChannelMessage) domain.ChannelMessage {
	in.Entities = append([]domain.MessageEntity(nil), in.Entities...)
	in.ReplyTo = cloneMessageReply(in.ReplyTo)
	in.Forward = cloneMessageForward(in.Forward)
	in.ReplyMarkup = cloneReplyMarkup(in.ReplyMarkup)
	in.RichMessage = cloneRichMessage(in.RichMessage)
	in.Discussion = cloneChannelDiscussionRef(in.Discussion)
	in.Replies = cloneChannelMessageReplies(in.Replies)
	in.Reactions = cloneChannelMessageReactionsPtr(in.Reactions)
	if in.SuggestedPost != nil {
		suggested := *in.SuggestedPost
		if suggested.Price != nil {
			price := *suggested.Price
			suggested.Price = &price
		}
		in.SuggestedPost = &suggested
	}
	if in.SendAs != nil {
		p := *in.SendAs
		in.SendAs = &p
	}
	if in.Action != nil {
		in.Action = cloneChannelMessageAction(in.Action)
	}
	return in
}

func cloneChannelMessageAction(in *domain.ChannelMessageAction) *domain.ChannelMessageAction {
	if in == nil {
		return nil
	}
	out := *in
	out.UserIDs = append([]int64(nil), in.UserIDs...)
	out.Completed = append([]int(nil), in.Completed...)
	out.Incompleted = append([]int(nil), in.Incompleted...)
	out.TodoItems = append([]domain.MessageTodoItem(nil), in.TodoItems...)
	if in.Closed != nil {
		v := *in.Closed
		out.Closed = &v
	}
	if in.Hidden != nil {
		v := *in.Hidden
		out.Hidden = &v
	}
	if in.StarGift != nil {
		g := *in.StarGift
		if in.StarGift.Sticker != nil {
			sticker := *in.StarGift.Sticker
			g.Sticker = &sticker
		}
		out.StarGift = &g
	}
	out.Wallpaper = domain.CloneWallpaperPtr(in.Wallpaper)
	out.Photo = domain.ClonePhotoPtr(in.Photo)
	return &out
}

func ptrChannelMessage(in domain.ChannelMessage) *domain.ChannelMessage {
	out := cloneChannelMessage(in)
	return &out
}
