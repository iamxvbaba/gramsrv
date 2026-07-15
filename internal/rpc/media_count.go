package rpc

import (
	"context"
	"fmt"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) mediaCountsForPeer(ctx context.Context, userID int64, peer domain.Peer) (domain.MediaCategoryCounts, error) {
	if userID == 0 || peer.ID == 0 {
		return domain.MediaCategoryCounts{}, nil
	}
	key := fmt.Sprintf("%d:%s:%d", userID, peer.Type, peer.ID)
	v, err, _ := r.mediaCountSF.Do(key, func() (any, error) {
		counts, err := r.loadMediaCountsForPeer(ctx, userID, peer)
		if err != nil {
			return nil, err
		}
		return cloneMediaCategoryCounts(counts), nil
	})
	if err != nil {
		return nil, err
	}
	counts, _ := v.(domain.MediaCategoryCounts)
	return cloneMediaCategoryCounts(counts), nil
}

func (r *Router) loadMediaCountsForPeer(ctx context.Context, userID int64, peer domain.Peer) (domain.MediaCategoryCounts, error) {
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return domain.MediaCategoryCounts{}, nil
		}
		return r.deps.Channels.CountChannelMediaCategories(ctx, userID, peer.ID)
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return domain.MediaCategoryCounts{}, nil
		}
		return r.deps.Messages.CountPrivateMediaCategories(ctx, userID, peer.ID)
	default:
		return domain.MediaCategoryCounts{}, nil
	}
}

func cloneMediaCategoryCounts(in domain.MediaCategoryCounts) domain.MediaCategoryCounts {
	if len(in) == 0 {
		return domain.MediaCategoryCounts{}
	}
	out := make(domain.MediaCategoryCounts, len(in))
	for category, count := range in {
		out[category] = count
	}
	return out
}

func mediaSearchCountOnlyRequest(req *tg.MessagesSearchRequest) bool {
	if req == nil {
		return false
	}
	if req.Q != "" || req.Limit != 0 || req.OffsetID != 0 || req.AddOffset != 0 ||
		req.MaxID != 0 || req.MinID != 0 || req.MinDate != 0 || req.MaxDate != 0 || req.Hash != 0 {
		return false
	}
	if _, ok := req.GetFromID(); ok {
		return false
	}
	if _, ok := req.GetSavedPeerID(); ok {
		return false
	}
	if _, ok := req.GetSavedReaction(); ok {
		return false
	}
	if _, ok := req.GetTopMsgID(); ok {
		return false
	}
	return searchFilterNeedsMediaStore(req.Filter)
}

func mediaSearchCanReusePeerWideCount(req *tg.MessagesSearchRequest) bool {
	if req == nil {
		return false
	}
	if req.Q != "" || req.MaxID != 0 || req.MinID != 0 || req.MinDate != 0 || req.MaxDate != 0 {
		return false
	}
	if _, ok := req.GetFromID(); ok {
		return false
	}
	if _, ok := req.GetSavedPeerID(); ok {
		return false
	}
	if _, ok := req.GetSavedReaction(); ok {
		return false
	}
	if _, ok := req.GetTopMsgID(); ok {
		return false
	}
	return searchFilterNeedsMediaStore(req.Filter)
}
