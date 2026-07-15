package rpc

import (
	"context"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesSetDefaultReaction(ctx context.Context, reaction tg.ReactionClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	parsed, err := domainMessageReactionFromTL(reaction)
	if err != nil {
		return false, err
	}
	if err := r.validateDefaultReaction(ctx, parsed); err != nil {
		return false, err
	}
	if svc, ok := r.deps.Account.(accountDefaultReactionService); ok {
		if _, err := svc.SetDefaultReaction(ctx, userID, parsed); err != nil {
			return false, internalErr()
		}
	}
	return true, nil
}

func (r *Router) validateDefaultReaction(ctx context.Context, reaction domain.MessageReaction) error {
	if reaction.Type == domain.MessageReactionCustomEmoji {
		return nil
	}
	if reaction.Type != domain.MessageReactionEmoji {
		return reactionInvalidErr()
	}

	if r.deps.Files != nil {
		catalog, err := r.deps.Files.ListAvailableReactions(ctx)
		if err != nil {
			return internalErr()
		}
		if len(catalog) > 0 {
			for _, item := range catalog {
				if !item.Inactive && strings.TrimSpace(item.Reaction) == reaction.Emoticon {
					return nil
				}
			}
			return reactionInvalidErr()
		}
	}

	for _, item := range staticReactionCatalog() {
		if item.Key() == reaction.Key() {
			return nil
		}
	}
	return reactionInvalidErr()
}

func (r *Router) onMessagesGetPaidReactionPrivacy(ctx context.Context) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	settings := domain.DefaultAccountReactionSettings()
	if svc, ok := r.deps.Account.(accountPaidReactionPrivacyService); ok {
		next, err := svc.GetReactionSettings(ctx, userID)
		if err != nil {
			return nil, internalErr()
		}
		settings = next
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePaidReactionPrivacy{
			Private: r.tgPaidReactionPrivacy(ctx, userID, settings.PaidPrivacy),
		}},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{},
		Date:  int(r.clock.Now().Unix()),
		Seq:   0,
	}, nil
}

func (r *Router) onMessagesTogglePaidReactionPrivacy(ctx context.Context, req *tg.MessagesTogglePaidReactionPrivacyRequest) (bool, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return false, messageIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return false, err
	}
	privacy, err := r.domainPaidReactionPrivacy(ctx, userID, req.Private)
	if err != nil {
		return false, err
	}
	if svc, ok := r.deps.Account.(accountPaidReactionPrivacyService); ok {
		next, err := svc.SetPaidReactionPrivacy(ctx, userID, privacy)
		if err != nil {
			return false, internalErr()
		}
		privacy = next.PaidPrivacy
	}
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePaidReactionPrivacy{Private: r.tgPaidReactionPrivacy(ctx, userID, privacy)}},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	})
	return true, nil
}

// onMessagesSendPaidReaction 为一条广播频道消息发送付费 reaction：从 Stars 账本 Debit
// req.Count 星，累计到消息上，返回带 updateMessageReactions（含 ReactionPaid 总星数 +
// top reactors）与 updateStarsBalance 的 Updates。崩溃约束：必须返回合法 Updates——
// DrKLO StarsController 对响应无 instanceof 强转 (TLRPC.Updates)。
func (r *Router) onMessagesSendPaidReaction(ctx context.Context, req *tg.MessagesSendPaidReactionRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if req.Count <= 0 || req.Count > domain.MaxPaidReactionStarsPerRequest {
		return nil, starsAmountInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	// 付费 reaction 仅用于（广播）频道帖子。
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	anonymous, err := r.resolvePaidReactionAnonymous(ctx, userID, req)
	if err != nil {
		return nil, err
	}
	if r.deps.Stars == nil {
		return nil, balanceTooLowErr()
	}
	paidSvc, ok := r.deps.Channels.(channelPaidReactionService)
	if !ok {
		return nil, notImplementedErr()
	}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: peer.ID}
	// 1. 先从账本 Debit（余额不足→真实 BALANCE_TOO_LOW）。
	balance, err := r.deps.Stars.Debit(ctx, userID, int64(req.Count), domain.StarsReasonReaction, channelPeer, "Paid reaction", "")
	if err != nil {
		return nil, starsErr(err)
	}
	// 2. 累计付费 reaction；失败则补偿退款。
	res, err := paidSvc.SendPaidReaction(ctx, userID, domain.SendChannelPaidReactionRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		MessageID: req.MsgID,
		Stars:     int64(req.Count),
		Anonymous: anonymous,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		if _, refundErr := r.deps.Stars.Credit(ctx, userID, int64(req.Count), domain.StarsReasonReaction, channelPeer, "Paid reaction refund", ""); refundErr != nil {
			r.log.Error("paid reaction refund failed after record error",
				zap.Int64("user_id", userID), zap.Int64("channel_id", peer.ID), zap.Int("msg_id", req.MsgID), zap.Error(refundErr))
		}
		return nil, channelReactionErr(err)
	}
	// 3. 构建并扇出 updateMessageReactions；请求者额外带 updateStarsBalance。
	ids := []int{res.Message.ID}
	build := func(viewerUserID int64) *tg.Updates {
		updates := r.channelPaidReactionUpdates(ctx, userID, viewerUserID, res, ids)
		if updates != nil && viewerUserID == userID {
			updates.Updates = append(updates.Updates, &tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance.Balance}})
		}
		return updates
	}
	recipients := append([]int64{res.Message.SenderUserID}, res.Recipients...)
	r.pushChannelViewerUpdates(ctx, userID, res.Channel.ID, recipients, build)
	return build(userID), nil
}

// resolvePaidReactionAnonymous 计算本次付费 reaction 是否匿名：显式 private 优先，
// 缺省回退用户保存的默认付费 reaction 隐私。
func (r *Router) resolvePaidReactionAnonymous(ctx context.Context, userID int64, req *tg.MessagesSendPaidReactionRequest) (bool, error) {
	if private, ok := req.GetPrivate(); ok {
		privacy, err := r.domainPaidReactionPrivacy(ctx, userID, private)
		if err != nil {
			return false, err
		}
		return privacy.Kind == domain.PaidReactionPrivacyAnonymous, nil
	}
	if svc, ok := r.deps.Account.(accountPaidReactionPrivacyService); ok {
		if settings, err := svc.GetReactionSettings(ctx, userID); err == nil {
			return settings.PaidPrivacy.Kind == domain.PaidReactionPrivacyAnonymous, nil
		}
	}
	return false, nil
}

func (r *Router) domainPaidReactionPrivacy(ctx context.Context, userID int64, in tg.PaidReactionPrivacyClass) (domain.PaidReactionPrivacy, error) {
	switch typed := in.(type) {
	case nil, *tg.PaidReactionPrivacyDefault:
		return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyDefault}, nil
	case *tg.PaidReactionPrivacyAnonymous:
		return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyAnonymous}, nil
	case *tg.PaidReactionPrivacyPeer:
		peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, typed.Peer)
		if err != nil {
			return domain.PaidReactionPrivacy{}, err
		}
		return domain.PaidReactionPrivacy{Kind: domain.PaidReactionPrivacyPeer, Peer: &peer}, nil
	default:
		return domain.PaidReactionPrivacy{}, inputConstructorInvalidErr()
	}
}

func (r *Router) tgPaidReactionPrivacy(ctx context.Context, userID int64, in domain.PaidReactionPrivacy) tg.PaidReactionPrivacyClass {
	switch in.Kind {
	case domain.PaidReactionPrivacyAnonymous:
		return &tg.PaidReactionPrivacyAnonymous{}
	case domain.PaidReactionPrivacyPeer:
		if in.Peer == nil {
			return &tg.PaidReactionPrivacyDefault{}
		}
		if peer := r.inputPeerForDomainPeer(ctx, userID, *in.Peer); peer != nil {
			return &tg.PaidReactionPrivacyPeer{Peer: peer}
		}
	}
	return &tg.PaidReactionPrivacyDefault{}
}
