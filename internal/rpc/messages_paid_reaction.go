package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// channelPaidReactionService 是 r.deps.Channels 的可选扩展（app/channels.Service 实现），
// 用类型断言接入，避免在 ChannelsService 接口上为付费 reaction 单加方法。
type channelPaidReactionService interface {
	SendPaidReaction(ctx context.Context, userID int64, req domain.SendChannelPaidReactionRequest) (domain.ChannelMessagePaidReactionResult, error)
}

// channelPaidReactionUpdates 为某 viewer 构造一条 updateMessageReactions：把消息已有的普通
// reaction 与付费 ReactionPaid（总星数 + top reactors）合并。非请求者走 min 语义（置 Min，
// 客户端忽略 chosen 保留本地态），避免把请求者视角串给他人。
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
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   r.tgUsersForIDs(ctx, viewerUserID, paidReactorUserIDs(res.Paid)),
		Chats:   tgChannels(viewerUserID, []domain.Channel{res.Channel}),
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

// clonePaidForViewer 返回付费聚合的副本；非请求者抹除 My/MyStars（min 语义防串视角）。
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

// injectPaidReaction 把付费 reaction 注入 MessageReactions：ReactionPaid 计数置首位、填充
// top reactors 排行。My/chosen 完全由 paid 的视角数据驱动（调用方对他人视角已抹除 My/MyStars）。
func injectPaidReaction(mr *tg.MessageReactions, paid domain.ChannelMessagePaidReactions) {
	if paid.TotalStars <= 0 {
		return
	}
	paidCount := tg.ReactionCount{Reaction: &tg.ReactionPaid{}, Count: int(paid.TotalStars)}
	if paid.MyStars > 0 {
		// chosen_order 仅标记「本人已投」，具体序值不关键，置正。
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
		// PeerID：非匿名给出；匿名仅本人给出（TL 语义：匿名本人 anonymous+peer 都置）。
		if rr.UserID != 0 && (!rr.Anonymous || rr.My) {
			item.SetPeerID(&tg.PeerUser{UserID: rr.UserID})
		}
		reactors = append(reactors, item)
	}
	if len(reactors) > 0 {
		mr.SetTopReactors(reactors)
	}
}

// paidReactorUserIDs 收集非匿名 reactor 的用户 id，供 Updates.Users 富化头像。
func paidReactorUserIDs(paid domain.ChannelMessagePaidReactions) []int64 {
	ids := make([]int64, 0, len(paid.TopReactors))
	for _, rr := range paid.TopReactors {
		if rr.UserID != 0 && !rr.Anonymous {
			ids = append(ids, rr.UserID)
		}
	}
	return ids
}
