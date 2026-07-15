package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// BuildOutboxUpdates 为在线 outbox worker 构造按接收者视角补全后的 updates。
func (r *Router) BuildOutboxUpdates(ctx context.Context, requests []OutboxUpdateRequest) []*tg.Updates {
	out := make([]*tg.Updates, len(requests))
	if len(requests) == 0 {
		return out
	}
	cache := newViewerPeerCache(r)
	groups := make(map[int64][]outboxUpdateBuildItem)
	for i, req := range requests {
		viewerUserID := req.TargetUserID
		if viewerUserID == 0 {
			viewerUserID = req.Event.UserID
		}
		event := req.Event
		if event.UserID == 0 {
			event.UserID = viewerUserID
		}
		groups[viewerUserID] = append(groups[viewerUserID], outboxUpdateBuildItem{index: i, event: event})
	}
	for viewerUserID, items := range groups {
		events := make([]domain.UpdateEvent, len(items))
		for i, item := range items {
			events[i] = item.event
		}
		events = r.enrichUpdateEventsWithPeerCache(ctx, viewerUserID, events, cache)
		for i, item := range items {
			update := tgUpdateForOutboxEventForViewer(events[i], viewerUserID)
			if peers := storyUpdateEventPeers(events[i]); len(peers) > 0 {
				update = r.withStoryUpdatePeerObjects(ctx, viewerUserID, update, peers...)
			}
			out[item.index] = update
		}
	}
	return out
}

type outboxUpdateBuildItem struct {
	index int
	event domain.UpdateEvent
}
