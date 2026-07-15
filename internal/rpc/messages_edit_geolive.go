package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 本文件实现 live location 的续报与停止：editMessage + InputMediaGeoLive。
// DrKLO Android 用它周期性上报新坐标；stopped=true 表示停止共享。
// 停止语义：把 Period 改写为「已逝时长」，所有客户端按 message.date + period
// 判定立即过期，服务端不需要后台到点任务。

func (r *Router) onEditMessageLiveLocation(ctx context.Context, req *tg.MessagesEditMessageRequest, media *tg.InputMediaGeoLive) (tg.UpdatesClass, error) {
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	current, found := r.loadLiveLocationMessage(ctx, userID, peer, req.ID)
	if !found {
		return nil, messageIDInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	next := *current.media.GeoLive
	if media.Stopped {
		// 立即过期：elapsed 至少 1 秒；forever 共享停止后同样按 elapsed 收口。
		elapsed := now - current.date
		if elapsed < 1 {
			elapsed = 1
		}
		next.Period = elapsed
	} else {
		geo, err := domainGeoPointFromInput(media.GeoPoint)
		if err != nil {
			return nil, err
		}
		// 保留首发 access_hash：客户端可能继续用旧 hash 拉地图缩略。
		geo.AccessHash = next.Geo.AccessHash
		next.Geo = *geo
		if heading, ok := media.GetHeading(); ok {
			if heading < 0 || heading > maxLiveLocationHeading {
				return nil, mediaInvalidErr()
			}
			next.Heading = heading
		}
		if period, ok := media.GetPeriod(); ok {
			if period != foreverLiveLocationPeriod && (period < minLiveLocationPeriod || period > maxLiveLocationPeriod) {
				return nil, mediaInvalidErr()
			}
			next.Period = period
		}
		if radius, ok := media.GetProximityNotificationRadius(); ok {
			if radius < 0 || radius > maxProximityRadiusMeters {
				return nil, mediaInvalidErr()
			}
			next.ProximityNotificationRadius = radius
		}
	}
	newMedia := &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &next}

	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return nil, peerIDInvalidErr()
		}
		res, err := r.deps.Channels.EditMessage(ctx, userID, domain.EditChannelMessageRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			ID:        req.ID,
			Message:   current.body,
			Entities:  current.entities,
			Media:     newMedia,
			EditDate:  now,
		})
		if err != nil {
			return nil, channelEditErr(err)
		}
		updates := r.channelEditMessageUpdates(ctx, userID, res)
		r.enqueueChannelEditMessageFanout(ctx, userID, res)
		return updates, nil
	}
	if peer.Type != domain.PeerTypeUser || r.deps.Messages == nil {
		return nil, peerIDInvalidErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.EditMessage(ctx, userID, domain.EditMessageRequest{
		OwnerUserID:     userID,
		Peer:            peer,
		ID:              req.ID,
		Message:         current.body,
		Entities:        current.entities,
		Media:           newMedia,
		EditDate:        now,
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionID,
	})
	if err != nil {
		return nil, messageEditErr(err)
	}
	self := res.Self()
	if self.Event.Pts == 0 || self.Message.ID == 0 {
		return nil, messageIDInvalidErr()
	}
	users := r.usersForMessageUpdate(ctx, userID, self.Message)
	chats := r.chatsForMessageUpdate(ctx, userID, self.Message)
	return tgEditMessageUpdates(self.Event, self.Message, users, chats), nil
}

type liveLocationTarget struct {
	media    *domain.MessageMedia
	body     string
	entities []domain.MessageEntity
	date     int
}

// loadLiveLocationMessage 加载目标消息并校验它是一条 live location。
func (r *Router) loadLiveLocationMessage(ctx context.Context, userID int64, peer domain.Peer, msgID int) (liveLocationTarget, bool) {
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return liveLocationTarget{}, false
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{msgID})
		if err != nil {
			return liveLocationTarget{}, false
		}
		for _, msg := range history.Messages {
			if msg.ID == msgID && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindGeoLive && msg.Media.GeoLive != nil {
				return liveLocationTarget{media: msg.Media, body: msg.Body, entities: msg.Entities, date: msg.Date}, true
			}
		}
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return liveLocationTarget{}, false
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, []int{msgID})
		if err != nil {
			return liveLocationTarget{}, false
		}
		for _, msg := range list.Messages {
			if msg.ID == msgID && msg.Peer == peer && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindGeoLive && msg.Media.GeoLive != nil {
				return liveLocationTarget{media: msg.Media, body: msg.Body, entities: msg.Entities, date: msg.Date}, true
			}
		}
	}
	return liveLocationTarget{}, false
}
