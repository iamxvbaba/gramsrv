package rpc

import (
	"context"
	"fmt"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"telesrv/internal/store"
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
	if r.deps.Messages == nil {
		return nil, internalErr()
	}
	period, err := r.deps.Messages.DefaultHistoryTTL(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.DefaultHistoryTTL{Period: period}, nil
}

func (r *Router) onMessagesSetDefaultHistoryTTL(ctx context.Context, period int) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if period < 0 || period > 365*24*3600 || period%86400 != 0 {
		return false, ttlPeriodInvalidErr()
	}
	if r.deps.Messages == nil {
		return false, internalErr()
	}
	if err := r.deps.Messages.SetDefaultHistoryTTL(ctx, userID, period); err != nil {
		return false, internalErr()
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
	if err := r.requireAccountDelivery(userID, "messages.setHistoryTTL"); err != nil {
		return nil, err
	}
	date := int(r.clock.Now().Unix())
	switch peer.Type {
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, internalErr()
		}
		if err := r.deps.Messages.SetPrivateHistoryTTL(ctx, userID, peer, req.Period,
			privateHistoryTTLDeliveryEffects(ctx, userID, date)); err != nil {
			return nil, internalErr()
		}
	case domain.PeerTypeChannel:
		ttlSvc, ok := r.deps.Channels.(channelHistoryTTLService)
		if r.deps.Channels == nil || !ok {
			return nil, channelInvalidErr(domain.ErrChannelInvalid)
		}
		channel, recipients, err := ttlSvc.SetHistoryTTL(ctx, userID, peer.ID, req.Period, date)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		out := r.peerHistoryTTLUpdates(ctx, userID, peer, req.Peer, req.Period, date)
		_ = channel
		_ = recipients // channel_pts event + channel delivery signal own fan-out.
		return out, nil
	default:
		return nil, peerIDInvalidErr()
	}
	out := r.peerHistoryTTLUpdates(ctx, userID, peer, req.Peer, req.Period, date)
	return out, nil
}

func privateHistoryTTLDeliveryEffects(ctx context.Context, requestUserID int64, date int) store.DeliveryEffectsBuilder[domain.PrivateHistoryTTLResult] {
	excludeAuthKeyID, excludeSessionID := deliveryExclusionFromContext(ctx)
	return func(res domain.PrivateHistoryTTLResult) ([]store.DeliveryEffect, error) {
		owners := []struct {
			userID int64
			peer   domain.Peer
		}{{res.OwnerUserID, res.Peer}}
		if res.Peer.ID != res.OwnerUserID {
			owners = append(owners, struct {
				userID int64
				peer   domain.Peer
			}{res.Peer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: res.OwnerUserID}})
		}
		effects := make([]store.DeliveryEffect, 0, len(owners))
		for _, owner := range owners {
			update := &tg.UpdatePeerHistoryTTL{Peer: tgPeer(owner.peer)}
			if res.Period > 0 {
				update.SetTTLPeriod(res.Period)
			}
			payload, err := encodeDeliveryUpdate(&tg.Updates{
				Updates: []tg.UpdateClass{update}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: date, Seq: 0,
			})
			if err != nil {
				return nil, fmt.Errorf("encode private history ttl delivery for user %d: %w", owner.userID, err)
			}
			authKeyID, sessionID := [8]byte{}, int64(0)
			if owner.userID == requestUserID {
				authKeyID, sessionID = excludeAuthKeyID, excludeSessionID
			}
			effects = append(effects, store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
				TargetUserID: owner.userID, ExcludeAuthKeyID: authKeyID, ExcludeSessionID: sessionID,
				Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
			}))
		}
		return effects, nil
	}
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
