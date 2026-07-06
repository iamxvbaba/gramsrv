package rpc

import (
	"context"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const channelMembershipSyncPageSize = domain.MaxSynchronousChannelDialogFanout

func (r *Router) trackChannelInterest(ctx context.Context, userID int64, channelIDs ...int64) {
	if userID == 0 || r.deps.Sessions == nil {
		return
	}
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return
	}
	rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx)
	if !ok {
		return
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	if len(channelIDs) == 0 {
		provider.ClearChannelInterest(rawAuthKeyID, sessionID, userID)
		return
	}
	provider.TrackChannelInterest(rawAuthKeyID, sessionID, userID, channelIDs)
}

func (r *Router) clearChannelInterest(ctx context.Context, userID int64) {
	r.trackChannelInterest(ctx, userID)
}

func (r *Router) syncSessionChannelMemberships(ctx context.Context, userID int64) {
	if userID == 0 || r.deps.Sessions == nil || r.deps.Channels == nil {
		return
	}
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return
	}
	rawAuthKeyID, ok := RawAuthKeyIDFrom(ctx)
	if !ok {
		return
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	// 在读取持久成员列表之前采样修订号：读取窗口内若发生增量 join/leave
	// （AddUserChannelMembership/RemoveUserChannelMembership），全量替换会覆盖增量，
	// SetSessionChannelMemberships 据此改走合并路径并保持未就绪重试。
	expectedGen := provider.ChannelMembershipGeneration(rawAuthKeyID, sessionID)
	channelIDs := make([]int64, 0, channelMembershipSyncPageSize)
	after := int64(0)
	for {
		page, err := r.deps.Channels.ActiveChannelIDsForUser(ctx, userID, after, channelMembershipSyncPageSize)
		if err != nil {
			r.log.Warn("sync session channel memberships failed",
				zap.Int64("user_id", userID),
				zap.Int64("after_channel_id", after),
				zap.Error(err))
			return
		}
		if len(page) == 0 {
			break
		}
		progressed := false
		for _, channelID := range page {
			if channelID == 0 {
				continue
			}
			channelIDs = append(channelIDs, channelID)
			if channelID > after {
				after = channelID
				progressed = true
			}
		}
		if !progressed || len(page) < channelMembershipSyncPageSize {
			break
		}
	}
	provider.SetSessionChannelMemberships(rawAuthKeyID, sessionID, userID, channelIDs, expectedGen)
}

func (r *Router) addOnlineChannelMemberships(channelID int64, userIDs ...int64) {
	if channelID == 0 || len(userIDs) == 0 || r.deps.Sessions == nil {
		return
	}
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return
	}
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		provider.AddUserChannelMembership(userID, channelID)
	}
}

func (r *Router) removeOnlineChannelMemberships(channelID int64, userIDs ...int64) {
	if channelID == 0 || len(userIDs) == 0 || r.deps.Sessions == nil {
		return
	}
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return
	}
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		provider.RemoveUserChannelMembership(userID, channelID)
	}
}

func (r *Router) removeOnlineChannelMembershipsForOnlineMembers(channelID int64) {
	if channelID == 0 || r.deps.Sessions == nil {
		return
	}
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return
	}
	r.removeOnlineChannelMemberships(channelID, provider.OnlineChannelMemberUserIDs(channelID, 0)...)
}

func channelMemberUserIDs(members []domain.ChannelMember) []int64 {
	if len(members) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		if member.UserID == 0 || member.Status != domain.ChannelMemberActive {
			continue
		}
		ids = append(ids, member.UserID)
	}
	return ids
}

func channelIDsFromDialogs(list domain.DialogList) []int64 {
	if len(list.Dialogs) == 0 && len(list.Channels) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(list.Dialogs)+len(list.Channels))
	seen := make(map[int64]struct{}, len(list.Dialogs)+len(list.Channels))
	for _, d := range list.Dialogs {
		if d.Peer.Type != domain.PeerTypeChannel || d.Peer.ID == 0 {
			continue
		}
		if _, ok := seen[d.Peer.ID]; ok {
			continue
		}
		seen[d.Peer.ID] = struct{}{}
		ids = append(ids, d.Peer.ID)
	}
	for _, ch := range list.Channels {
		if ch.ID == 0 {
			continue
		}
		if _, ok := seen[ch.ID]; ok {
			continue
		}
		seen[ch.ID] = struct{}{}
		ids = append(ids, ch.ID)
	}
	return ids
}
