package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) suggestedPostApprovalUpdates(ctx context.Context, viewerUserID int64, result domain.ToggleSuggestedPostApprovalResult) *tg.Updates {
	updates := make([]tg.UpdateClass, 0, 4)
	if result.OriginalEvent.Pts > 0 {
		if update := tgChannelUpdate(viewerUserID, result.OriginalEvent); update != nil {
			updates = append(updates, update)
		}
	}
	if result.ServiceEvent.Pts > 0 {
		if update := tgChannelUpdate(viewerUserID, result.ServiceEvent); update != nil {
			updates = append(updates, update)
		}
	}
	if result.Published != nil && result.Published.Event.Pts > 0 {
		if update := tgChannelUpdate(viewerUserID, result.Published.Event); update != nil {
			updates = append(updates, update)
		}
	}
	if result.PayerStarsBalance != nil && result.PayerStarsBalance.UserID == viewerUserID {
		updates = append(updates, &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: result.PayerStarsBalance.Balance}})
	}
	chats := r.monoforumChats(ctx, viewerUserID, result.Monoforum)
	if result.Parent.ID != 0 {
		chats = appendUniqueTGChats(chats, tgChannelChatMin(viewerUserID, result.Parent))
	}
	messages := make([]domain.ChannelMessage, 0, 3)
	if result.OriginalMessage.ID != 0 {
		messages = append(messages, result.OriginalMessage)
	}
	if result.ServiceMessage.ID != 0 {
		messages = append(messages, result.ServiceMessage)
	}
	if result.Published != nil {
		messages = append(messages, result.Published.Message)
	}
	return &tg.Updates{
		Updates: updates,
		Chats:   chats,
		Users:   r.monoforumSubscriberUsers(ctx, viewerUserID, []domain.MonoforumDialog{{SavedPeer: result.SavedPeer}}, messages),
		Date:    int(r.clock.Now().Unix()),
	}
}

func (r *Router) enqueueSuggestedPostApprovalFanout(ctx context.Context, originUserID int64, result domain.ToggleSuggestedPostApprovalResult) {
	monoOnly := result
	monoOnly.Published = nil
	nudge := max(result.OriginalEvent.Pts, result.ServiceEvent.Pts)
	if nudge > 0 {
		r.enqueueChannelFanout(ctx, channelFanoutExplicit, originUserID, result.Monoforum.ID, nudge, result.Recipients, func(bgCtx context.Context, viewerUserID int64) *tg.Updates {
			return r.suggestedPostApprovalUpdates(bgCtx, viewerUserID, monoOnly)
		})
	}
	if result.Published != nil && result.Published.Event.Pts > 0 {
		r.enqueueChannelMessageFanout(ctx, originUserID, *result.Published, nil)
	}
}
