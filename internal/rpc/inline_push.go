package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/store"
)

const inlinePushSubscribeRetry = time.Second

func (r *Router) RunInlineBotPushSubscriber(ctx context.Context) {
	broker, ok := r.deps.Inline.(store.BotInlineQueryPushBroker)
	if !ok || broker == nil {
		return
	}
	for {
		err := broker.SubscribeBotInlineQueries(ctx, func(ctx context.Context, event store.BotInlineQueryPush) {
			r.handleRemoteInlineBotQuery(ctx, event)
		})
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.log.Warn("inline bot push subscriber stopped", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(inlinePushSubscribeRetry):
		}
	}
}

func (r *Router) publishInlineBotQuery(ctx context.Context, event store.BotInlineQueryPush) {
	broker, ok := r.deps.Inline.(store.BotInlineQueryPushBroker)
	if !ok || broker == nil {
		return
	}
	event.SourceID = r.instanceID
	if event.Date == 0 {
		event.Date = int(r.clock.Now().Unix())
	}
	if err := broker.PublishBotInlineQuery(ctx, event); err != nil {
		r.log.Debug("publish inline bot query", zap.Int64("bot_user_id", event.BotUserID), zap.Int64("query_id", event.QueryID), zap.Error(err))
	}
}

func (r *Router) handleRemoteInlineBotQuery(ctx context.Context, event store.BotInlineQueryPush) {
	if event.SourceID == "" || event.SourceID == r.instanceID {
		return
	}
	if event.QueryID == 0 || event.BotUserID == 0 || event.UserID == 0 {
		return
	}
	peerType, ok := tgInlineQueryPeerTypeFromStore(event.PeerType)
	if !ok {
		return
	}
	update := &tg.UpdateBotInlineQuery{
		QueryID:  event.QueryID,
		UserID:   event.UserID,
		Query:    event.Query,
		Offset:   event.Offset,
		PeerType: peerType,
	}
	if event.Geo != nil {
		update.SetGeo(tgGeoPoint(*event.Geo))
	}
	date := event.Date
	if date <= 0 {
		date = int(r.clock.Now().Unix())
	}
	r.pushUserMessage(ctx, event.BotUserID, "push remote bot inline query", &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Date:    date,
	})
}

func storeInlineQueryPeerType(peerType tg.InlineQueryPeerTypeClass) string {
	switch peerType.(type) {
	case *tg.InlineQueryPeerTypeSameBotPM:
		return store.InlineQueryPeerTypeSameBotPM
	case *tg.InlineQueryPeerTypePM:
		return store.InlineQueryPeerTypePM
	case *tg.InlineQueryPeerTypeChat:
		return store.InlineQueryPeerTypeChat
	case *tg.InlineQueryPeerTypeMegagroup:
		return store.InlineQueryPeerTypeMegagroup
	case *tg.InlineQueryPeerTypeBroadcast:
		return store.InlineQueryPeerTypeBroadcast
	case *tg.InlineQueryPeerTypeBotPM:
		return store.InlineQueryPeerTypeBotPM
	default:
		return ""
	}
}

func tgInlineQueryPeerTypeFromStore(peerType string) (tg.InlineQueryPeerTypeClass, bool) {
	switch peerType {
	case store.InlineQueryPeerTypeSameBotPM:
		return &tg.InlineQueryPeerTypeSameBotPM{}, true
	case store.InlineQueryPeerTypePM:
		return &tg.InlineQueryPeerTypePM{}, true
	case store.InlineQueryPeerTypeChat:
		return &tg.InlineQueryPeerTypeChat{}, true
	case store.InlineQueryPeerTypeMegagroup:
		return &tg.InlineQueryPeerTypeMegagroup{}, true
	case store.InlineQueryPeerTypeBroadcast:
		return &tg.InlineQueryPeerTypeBroadcast{}, true
	case store.InlineQueryPeerTypeBotPM:
		return &tg.InlineQueryPeerTypeBotPM{}, true
	default:
		return nil, false
	}
}
