package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

// maxHistoryTTLPeriod 限制自毁倒计时上界（366 天）。无上界时客户端可传接近
// int32 上限的 period，使发送路径 expires_at = date + ttl_period 经 int32 截断
// 回绕为负值，被到期扫描的 expires_at>0 过滤掉，导致 TTL 静默失效（消息以为
// 会自毁实则永久留存）。
const maxHistoryTTLPeriod = 366 * 24 * 3600

func (r *Router) onMessagesGetDefaultHistoryTTL(ctx context.Context) (*tg.DefaultHistoryTTL, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	period := 0
	if ttlSvc, ok := r.deps.Messages.(historyTTLMessagesService); r.deps.Messages != nil && ok {
		period, err = ttlSvc.DefaultHistoryTTL(ctx, userID)
		if err != nil {
			return nil, internalErr()
		}
	}
	return &tg.DefaultHistoryTTL{Period: period}, nil
}

func (r *Router) onMessagesSetDefaultHistoryTTL(ctx context.Context, period int) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if period < 0 || period > maxHistoryTTLPeriod {
		return false, ttlPeriodInvalidErr()
	}
	if ttlSvc, ok := r.deps.Messages.(historyTTLMessagesService); r.deps.Messages != nil && ok {
		if err := ttlSvc.SetDefaultHistoryTTL(ctx, userID, period); err != nil {
			return false, internalErr()
		}
	}
	return true, nil
}

func (r *Router) onMessagesSetHistoryTTL(ctx context.Context, req *tg.MessagesSetHistoryTTLRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.Period < 0 || req.Period > maxHistoryTTLPeriod {
		return nil, ttlPeriodInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		if ttlSvc, ok := r.deps.Messages.(historyTTLMessagesService); r.deps.Messages != nil && ok {
			if err := ttlSvc.SetPrivateHistoryTTL(ctx, userID, peer, req.Period); err != nil {
				return nil, internalErr()
			}
		}
	case domain.PeerTypeChannel:
		ttlSvc, ok := r.deps.Channels.(channelHistoryTTLService)
		if r.deps.Channels == nil || !ok {
			return nil, channelInvalidErr(domain.ErrChannelInvalid)
		}
		channel, recipients, err := ttlSvc.SetHistoryTTL(ctx, userID, peer.ID, req.Period, int(r.clock.Now().Unix()))
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		date := int(r.clock.Now().Unix())
		out := r.peerHistoryTTLUpdates(ctx, userID, peer, req.Peer, req.Period, date)
		r.pushChannelUpdates(ctx, userID, channel.ID, recipients, func(viewerUserID int64) *tg.Updates {
			return r.peerHistoryTTLUpdates(ctx, viewerUserID, peer, req.Peer, req.Period, date)
		})
		return out, nil
	default:
		return nil, peerIDInvalidErr()
	}
	date := int(r.clock.Now().Unix())
	out := r.peerHistoryTTLUpdates(ctx, userID, peer, req.Peer, req.Period, date)
	r.pushUserUpdates(ctx, userID, out)
	if peer.Type == domain.PeerTypeUser && peer.ID != userID {
		otherPeer := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
		r.pushUserUpdates(ctx, peer.ID, r.peerHistoryTTLUpdates(ctx, peer.ID, otherPeer, nil, req.Period, date))
	}
	return out, nil
}

func (r *Router) peerHistoryTTLUpdates(ctx context.Context, viewerUserID int64, peer domain.Peer, inputPeer tg.InputPeerClass, period int, date int) *tg.Updates {
	update := &tg.UpdatePeerHistoryTTL{Peer: tgPeer(peer)}
	if period > 0 {
		update.SetTTLPeriod(period)
	}
	chats := []tg.ChatClass{}
	if inputPeer != nil {
		chats = r.chatsForInputPeer(ctx, viewerUserID, inputPeer)
	} else if peer.Type == domain.PeerTypeChannel {
		chats = r.chatsForMessageUpdate(ctx, viewerUserID, domain.Message{Peer: peer})
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   []tg.UserClass{},
		Chats:   chats,
		Date:    date,
		Seq:     0,
	}
}
