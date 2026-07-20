package rpc

import (
	"context"
	"errors"
	"fmt"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"
	"telesrv/internal/domain"
)

func validateEmptyChannelStickerSet(stickerset tg.InputStickerSetClass) error {
	if _, ok := stickerset.(*tg.InputStickerSetEmpty); ok {
		return nil
	}
	return stickersetInvalidErr()
}

func (r *Router) onChannelsReportSpam(ctx context.Context, req *tg.ChannelsReportSpamRequest) (bool, error) {
	if len(req.ID) > maxChannelReportMessageIDs {
		return false, limitInvalidErr()
	}
	for _, id := range req.ID {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return false, messageIDInvalidErr()
		}
	}
	if _, _, err := r.channelView(ctx, req.Channel); err != nil {
		return false, err
	}
	if peer, ok := r.domainPeerFromInputPeer(0, req.Participant); !ok || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return false, peerIDInvalidErr()
	}
	return true, nil
}

func (r *Router) publicRecommendationSourceChannel(ctx context.Context, userID int64, input tg.InputChannelClass) (int64, error) {
	ref, ok := inputChannelRef(input)
	if !ok {
		return 0, channelInvalidErr(domain.ErrChannelInvalid)
	}
	channel, err := r.deps.Channels.GetJoinableChannel(ctx, userID, ref.ID)
	if err != nil {
		return 0, channelInvalidErr(err)
	}
	if !inputChannelAccessHashMatches(ref, channel) {
		return 0, channelInvalidErr(domain.ErrChannelPrivate)
	}
	if channel.Deleted || !channel.Broadcast || channel.Megagroup || channel.Username == "" {
		return 0, channelInvalidErr(domain.ErrChannelInvalid)
	}
	return channel.ID, nil
}

func (r *Router) onChannelsSetMainProfileTab(ctx context.Context, req *tg.ChannelsSetMainProfileTabRequest) (bool, error) {
	if _, _, err := r.channelChangeInfoView(ctx, req.Channel); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onChannelsDeleteChannel(ctx context.Context, input tg.InputChannelClass) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil && r.deps.Communities == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if community, ok, err := r.maybeCommunityFromInput(ctx, userID, input); ok {
		if err != nil {
			return nil, err
		}
		view, _, err := r.deps.Communities.Delete(ctx, userID, community.Community.ID, int(r.clock.Now().Unix()))
		if err != nil {
			return nil, communityErr(err)
		}
		for _, serviceMessage := range view.ServiceMessages {
			r.enqueueChannelMessageFanout(ctx, userID, serviceMessage, nil)
		}
		return r.communityMutationUpdates(ctx, userID, view, true), nil
	}
	if r.deps.Channels == nil {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	channelID, err := r.channelIDFromInput(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	res, err := r.deps.Channels.DeleteChannel(ctx, userID, domain.DeleteChannelRequest{
		UserID:    userID,
		ChannelID: channelID,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelAdminErr(err)
	}
	r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
	updates := r.channelStateUpdates(userID, res.Channel)
	// 母频道随带删的 monoforum 必须同样以 ChannelForbidden 墓碑下发,否则在线客户端会留着 mono 会话
	// (isMonoforum=true 但 link 已不可解析)继续渲染崩溃。对操作者本设备(updates)与所有接收方都补一条。
	if mono := res.LinkedMonoforum; mono != nil {
		r.invalidateChannelFullBotInfoCacheForChannel(mono.ID)
		appendChannelStateUpdates(updates, r.channelStateUpdates(userID, *mono))
	}
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		upd := r.channelStateUpdates(viewerUserID, res.Channel)
		if mono := res.LinkedMonoforum; mono != nil {
			appendChannelStateUpdates(upd, r.channelStateUpdates(viewerUserID, *mono))
		}
		return upd
	})
	r.removeOnlineChannelMembershipsForOnlineMembers(res.Channel.ID)
	if mono := res.LinkedMonoforum; mono != nil {
		r.removeOnlineChannelMembershipsForOnlineMembers(mono.ID)
	}
	return updates, nil
}

func (r *Router) onMessagesUnpinAllMessages(ctx context.Context, req *tg.MessagesUnpinAllMessagesRequest) (*tg.MessagesAffectedHistory, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if topMsgID, ok := req.GetTopMsgID(); ok && (topMsgID <= 0 || topMsgID > domain.MaxMessageBoxID) {
		return nil, messageIDInvalidErr()
	}
	if savedPeer, ok := req.GetSavedPeerID(); ok && savedPeer != nil {
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, savedPeer); err != nil {
			return nil, err
		}
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		// 私聊置顶按账号 pts 记账；返回 0 会让 Android 误判 pts 空洞多拉
		// 一轮 difference。
		authKeyID, _ := AuthKeyIDFrom(ctx)
		if peer.Type != domain.PeerTypeUser || peer.ID == 0 {
			return r.affectedHistory(ctx, authKeyID, userID, 0)
		}
		if topMsgID, ok := req.GetTopMsgID(); ok && topMsgID > 0 {
			// 私聊无 forum topic 维度，按无置顶可清处理。
			return r.affectedHistory(ctx, authKeyID, userID, 0)
		}
		if _, ok := req.GetSavedPeerID(); ok {
			// Saved Messages 子会话置顶维度未实现，避免误清全局置顶。
			return r.affectedHistory(ctx, authKeyID, userID, 0)
		}
		return r.unpinAllPrivateMessages(ctx, userID, peer)
	}
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, peer.ID)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	if topMsgID, ok := req.GetTopMsgID(); ok && topMsgID > 0 {
		return &tg.MessagesAffectedHistory{Pts: view.Channel.Pts, Offset: 0}, nil
	}
	if _, ok := req.GetSavedPeerID(); ok {
		return &tg.MessagesAffectedHistory{Pts: view.Channel.Pts, Offset: 0}, nil
	}
	res, err := r.deps.Channels.UnpinAllMessages(ctx, userID, domain.UnpinAllChannelMessagesRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		if errors.Is(err, domain.ErrChannelNotModified) {
			return &tg.MessagesAffectedHistory{Pts: view.Channel.Pts, Offset: 0}, nil
		}
		return nil, channelAdminErr(err)
	}
	r.invalidateRPCProjectionForChannel(res.Channel.ID)
	r.enqueueChannelFanout(ctx, channelFanoutMembers, userID, res.Channel.ID, res.Event.Pts, res.Recipients, func(_ context.Context, viewerUserID int64) *tg.Updates {
		return r.channelPinnedUpdates(viewerUserID, res)
	})
	return &tg.MessagesAffectedHistory{
		Pts:      res.Event.Pts,
		PtsCount: res.Event.PtsCount,
		Offset:   0,
	}, nil
}

// createChatNeedsLegacyChat decides whether messages.createChat returns the
// legacy chat-shaped response. Old DrKLO/TDesktop clients (and pre-initConnection
// sessions) expect it; modern clients get the canonical InvitedUsers response.
// The old createChat#34a818 id now upgrades via the gotdgen client overlay,
// and android client metadata is applied during that upgrade, so keying on
// ClientType (below) is sufficient — no per-request ctx flag is needed.
func createChatNeedsLegacyChat(ctx context.Context) bool {
	if _, ok := ClientInfoFrom(ctx); !ok {
		_, hasSession := SessionIDFrom(ctx)
		return hasSession
	}
	switch ClientTypeFrom(ctx) {
	case ClientTypeTDesktop, ClientTypeAndroid:
		return true
	default:
		return false
	}
}

func channelMemberForUser(members []domain.ChannelMember, userID int64) *domain.ChannelMember {
	for i := range members {
		if members[i].UserID == userID {
			return &members[i]
		}
	}
	return nil
}

func mergeChannelMembers(a, b []domain.ChannelMember) []domain.ChannelMember {
	if len(a) == 0 {
		return append([]domain.ChannelMember(nil), b...)
	}
	if len(b) == 0 {
		return append([]domain.ChannelMember(nil), a...)
	}
	out := make([]domain.ChannelMember, 0, len(a)+len(b))
	seen := make(map[int64]struct{}, len(a)+len(b))
	appendOne := func(member domain.ChannelMember) {
		if member.UserID == 0 {
			return
		}
		if _, ok := seen[member.UserID]; ok {
			return
		}
		seen[member.UserID] = struct{}{}
		out = append(out, member)
	}
	for _, member := range a {
		appendOne(member)
	}
	for _, member := range b {
		appendOne(member)
	}
	return out
}

func peerIDsExcept(ids []int64, skipIDs ...int64) []int64 {
	unique := uniquePeerIDs(ids)
	if len(unique) == 0 || len(skipIDs) == 0 {
		return unique
	}
	skip := make(map[int64]struct{}, len(skipIDs))
	for _, id := range skipIDs {
		if id != 0 {
			skip[id] = struct{}{}
		}
	}
	out := unique[:0]
	for _, id := range unique {
		if _, ok := skip[id]; ok {
			continue
		}
		out = append(out, id)
	}
	return out
}

type channelFanoutScope int

func (r *Router) recordChannelAvailableMessages(ctx context.Context, userID, channelID int64, availableMinID int) domain.UpdateEvent {
	event := domain.UpdateEvent{
		UserID:   userID,
		Type:     domain.UpdateEventChannelAvailable,
		Date:     int(r.clock.Now().Unix()),
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		MaxID:    availableMinID,
		PtsCount: 1,
	}
	if r.deps.Updates == nil || userID == 0 || channelID == 0 || availableMinID <= 0 {
		return event
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	recorded, _, err := r.deps.Updates.RecordChannelAvailableMessages(ctx, authKeyID, userID, channelID, availableMinID, rawAuthKeyIDForOrigin(ctx), sessionID)
	if err != nil {
		return event
	}
	return recorded
}

func (r *Router) recordChannelReadInbox(ctx context.Context, userID int64, read domain.ReadChannelHistoryResult) (domain.UpdateEvent, error) {
	if !read.Changed || read.ChannelID == 0 {
		return domain.UpdateEvent{}, nil
	}
	date := int(r.clock.Now().Unix())
	event := domain.UpdateEvent{
		UserID:           userID,
		Type:             domain.UpdateEventReadHistoryInbox,
		Date:             date,
		Peer:             domain.Peer{Type: domain.PeerTypeChannel, ID: read.ChannelID},
		MaxID:            read.MaxID,
		StillUnreadCount: read.StillUnreadCount,
		ChannelPts:       read.Pts,
		FolderID:         read.Dialog.FolderID,
		PtsCount:         1,
	}
	// event.Pts 是账号 pts 槽位，只能来自真实 durable 记录；channel pts
	// 永远只放 ChannelPts，混填会让 pts 簿记把 channel 序列当账号序列。
	recordedEvent := event
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		recorded, _, err := r.deps.Updates.RecordReadHistory(ctx, authKeyID, userID, domain.ReadHistoryResult{
			OwnerUserID:      userID,
			Peer:             event.Peer,
			MaxID:            read.MaxID,
			StillUnreadCount: read.StillUnreadCount,
			ChannelPts:       read.Pts,
			Changed:          read.Changed,
		}, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return domain.UpdateEvent{}, internalErr()
		}
		recordedEvent = recorded
	}
	r.pushCurrentReadHistoryEvent(ctx, recordedEvent)
	r.pushReadHistoryEvent(ctx, userID, recordedEvent)
	return recordedEvent, nil
}

func (r *Router) channelFanoutRecipients(ctx context.Context, scope channelFanoutScope, channelID int64, explicit []int64) []int64 {
	if channelID == 0 || r.deps.Channels == nil || r.deps.Sessions == nil {
		return uniqueRecipientIDs(explicit)
	}
	if scope == channelFanoutExplicit {
		return uniqueRecipientIDs(explicit)
	}
	provider, ok := r.deps.Sessions.(OnlineUserProvider)
	if !ok {
		return uniqueRecipientIDs(explicit)
	}
	// 实时推送按 MaxChannelRealtimeFanout 封顶：每个收件人都要逐 viewer 重建 payload
	// 并批量解析 users，放开会把高频操作（如 reaction）放大成 O(全部在线成员) 的逐条推送。
	var online []int64
	switch scope {
	case channelFanoutMembers:
		online = provider.OnlineChannelMemberUserIDs(channelID, domain.MaxChannelRealtimeFanout)
	case channelFanoutViewers:
		online = provider.OnlineChannelUserIDs(channelID, domain.MaxChannelRealtimeFanout)
	}
	if len(online) == 0 {
		return uniqueRecipientIDs(explicit)
	}
	active, err := r.deps.Channels.FilterActiveMemberIDs(ctx, channelID, online)
	if err != nil {
		return uniqueRecipientIDs(explicit)
	}
	if len(active) == 0 && len(explicit) == 0 {
		return nil
	}
	if len(active) > domain.MaxChannelRealtimeFanout {
		active = active[:domain.MaxChannelRealtimeFanout]
	}
	out := uniqueRecipientIDs(active)
	seen := make(map[int64]struct{}, len(out)+len(explicit))
	for _, userID := range active {
		if userID == 0 {
			continue
		}
		seen[userID] = struct{}{}
	}
	// Keep operation-specific recipients as a fallback: leave/kick/delete flows
	// may need to notify a user who is no longer an active member after commit.
	for _, userID := range explicit {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		out = append(out, userID)
	}
	return out
}

func uniqueRecipientIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, userID := range ids {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		out = append(out, userID)
	}
	return out
}

func (r *Router) pushChannelStateToMembers(ctx context.Context, originUserID int64, channel domain.Channel) {
	r.pushChannelStateToMembersWithLinkedMonoforum(ctx, originUserID, channel, domain.Channel{}, false)
}

func (r *Router) pushChannelStateToMembersWithLinkedMonoforum(ctx context.Context, originUserID int64, channel domain.Channel, mono domain.Channel, includeMono bool) {
	if r.deps.Channels == nil || channel.ID == 0 {
		return
	}
	r.pushChannelUpdates(ctx, originUserID, channel.ID, []int64{originUserID}, func(viewerUserID int64) *tg.Updates {
		return r.channelStateUpdatesWithLinkedMonoforum(viewerUserID, channel, mono, includeMono)
	})
}

func (r *Router) tgUsersForIDs(ctx context.Context, currentUserID int64, ids []int64) []tg.UserClass {
	if r.deps.Users == nil || len(ids) == 0 {
		return nil
	}
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	users, err := r.deps.Users.ByIDs(ctx, currentUserID, unique)
	if err != nil {
		// 批量解析失败（通常是 DB 故障）不静默丢弃：批量语义下无部分结果，记日志便于排查。
		// update 仍以空 users 列表推送，客户端用本地缓存或后续 getUser 补齐，不致命。
		// 不降级逐个查询以免 DB 抖动时把一次失败放大成 N 次查询。
		r.log.Warn("batch resolve users for channel update failed",
			zap.Int("count", len(unique)), zap.Error(err))
		return nil
	}
	byID := make(map[int64]domain.User, len(users))
	for _, u := range users {
		if u.ID != 0 {
			byID[u.ID] = u
		}
	}
	out := make([]tg.UserClass, 0, len(byID))
	for _, id := range unique {
		u, ok := byID[id]
		if !ok {
			continue
		}
		u = r.withUserPresence(u)
		if id == currentUserID {
			out = append(out, tgSelfUser(u))
			continue
		}
		out = append(out, tgUser(u))
	}
	r.withBotProfileFlagsForUsers(ctx, out)
	return out
}

func (r *Router) inviteManagementChannelView(ctx context.Context, peer tg.InputPeerClass) (int64, domain.ChannelView, error) {
	ref, ok := inviteManagementChannelRef(peer)
	if !ok {
		return 0, domain.ChannelView{}, peerIDInvalidErr()
	}
	input := &tg.InputChannel{ChannelID: ref.ID}
	if ref.CheckAccessHash {
		input.AccessHash = ref.AccessHash
	}
	userID, view, err := r.channelView(ctx, input)
	if err != nil {
		return 0, domain.ChannelView{}, err
	}
	if view.Self.Role != domain.ChannelRoleCreator && view.Self.Role != domain.ChannelRoleAdmin {
		return 0, domain.ChannelView{}, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	return userID, view, nil
}

func inviteManagementChannelRef(peer tg.InputPeerClass) (channelInputRef, bool) {
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		if p == nil {
			return channelInputRef{}, false
		}
		return channelInputRef{
			ID:              p.ChannelID,
			AccessHash:      p.AccessHash,
			CheckAccessHash: p.AccessHash != 0,
		}, p.ChannelID > 0
	case *tg.InputPeerChannelFromMessage:
		if p == nil {
			return channelInputRef{}, false
		}
		return channelInputRef{ID: p.ChannelID}, p.ChannelID > 0
	case *tg.InputPeerChat:
		if p == nil {
			return channelInputRef{}, false
		}
		return channelInputRef{ID: p.ChatID}, p.ChatID > 0
	default:
		return channelInputRef{}, false
	}
}

func inputUserIsEmpty(input tg.InputUserClass) bool {
	switch input.(type) {
	case nil, *tg.InputUserEmpty:
		return true
	default:
		return false
	}
}

func (r *Router) userIDsFromInputUsers(ctx context.Context, currentUserID int64, inputs []tg.InputUserClass) ([]int64, error) {
	out := make([]int64, 0, len(inputs))
	seen := make(map[int64]struct{}, len(inputs))
	for _, input := range inputs {
		u, found, err := r.userFromInput(ctx, currentUserID, input)
		if err != nil {
			return nil, internalErr()
		}
		if !found || u.ID == 0 {
			return nil, peerIDInvalidErr()
		}
		if _, ok := seen[u.ID]; ok {
			continue
		}
		seen[u.ID] = struct{}{}
		out = append(out, u.ID)
	}
	return out, nil
}

type channelInputRef struct {
	ID              int64
	AccessHash      int64
	CheckAccessHash bool
}

func inputChannelRef(input tg.InputChannelClass) (channelInputRef, bool) {
	switch channel := input.(type) {
	case *tg.InputChannel:
		return channelInputRef{
			ID:              channel.ChannelID,
			AccessHash:      channel.AccessHash,
			CheckAccessHash: channel.AccessHash != 0,
		}, channel.ChannelID > 0
	case *tg.InputChannelFromMessage:
		return channelInputRef{ID: channel.ChannelID}, channel.ChannelID > 0
	default:
		return channelInputRef{}, false
	}
}

func inputChannelAccessHashMatches(ref channelInputRef, channel domain.Channel) bool {
	return !ref.CheckAccessHash || ref.AccessHash == channel.AccessHash
}

func (r *Router) optionalChannelIDFromInput(ctx context.Context, userID int64, input tg.InputChannelClass) (int64, error) {
	switch input.(type) {
	case nil, *tg.InputChannelEmpty:
		return 0, nil
	default:
		return r.channelIDFromInput(ctx, userID, input)
	}
}

func (r *Router) channelIDFromInput(ctx context.Context, userID int64, input tg.InputChannelClass) (int64, error) {
	ref, ok := inputChannelRef(input)
	if !ok {
		return 0, channelInvalidErr(domain.ErrChannelInvalid)
	}
	if !ref.CheckAccessHash || r.deps.Channels == nil {
		return ref.ID, nil
	}
	// 只校验 access_hash + 返回 ID：走轻量 ResolveChannel（省 dialog/读态/boost 这 3 条查询）。
	// 本函数被 23 个频道 RPC 复用，其中 getChannelDifference 被客户端按打开频道每秒轮询——是
	// 完整 GetChannel 投影放大的主源头。返回契约不变（仅 ID + 越权校验）。
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, ref.ID)
	if err != nil {
		return 0, channelInvalidErr(err)
	}
	if !inputChannelAccessHashMatches(ref, view.Channel) {
		return 0, channelInvalidErr(domain.ErrChannelPrivate)
	}
	return ref.ID, nil
}

func (r *Router) channelView(ctx context.Context, input tg.InputChannelClass) (int64, domain.ChannelView, error) {
	if r.deps.Channels == nil {
		return 0, domain.ChannelView{}, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, domain.ChannelView{}, internalErr()
	}
	ref, ok := inputChannelRef(input)
	if !ok {
		return 0, domain.ChannelView{}, channelInvalidErr(domain.ErrChannelInvalid)
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, ref.ID)
	if err != nil {
		return 0, domain.ChannelView{}, channelInvalidErr(err)
	}
	if !inputChannelAccessHashMatches(ref, view.Channel) {
		return 0, domain.ChannelView{}, channelInvalidErr(domain.ErrChannelPrivate)
	}
	return userID, view, nil
}

func (r *Router) channelChangeInfoView(ctx context.Context, input tg.InputChannelClass) (int64, domain.ChannelView, error) {
	if r.deps.Channels == nil {
		return 0, domain.ChannelView{}, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, domain.ChannelView{}, internalErr()
	}
	ref, ok := inputChannelRef(input)
	if !ok {
		return 0, domain.ChannelView{}, channelInvalidErr(domain.ErrChannelInvalid)
	}
	view, err := r.deps.Channels.GetChannelForChangeInfo(ctx, userID, ref.ID)
	if err != nil {
		return 0, domain.ChannelView{}, channelAdminErr(err)
	}
	if !inputChannelAccessHashMatches(ref, view.Channel) {
		return 0, domain.ChannelView{}, channelInvalidErr(domain.ErrChannelPrivate)
	}
	return userID, view, nil
}

func channelIDFromLegacyInputPeer(userID int64, peer tg.InputPeerClass) (int64, bool) {
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		if p == nil {
			return 0, false
		}
		return p.ChannelID, p.ChannelID > 0
	case *tg.InputPeerChat:
		if p == nil {
			return 0, false
		}
		return p.ChatID, p.ChatID > 0
	case *tg.InputPeerChannelFromMessage:
		if p == nil {
			return 0, false
		}
		return p.ChannelID, p.ChannelID > 0
	default:
		return 0, false
	}
}

func (r *Router) channelIDFromLegacyInputPeerChecked(ctx context.Context, userID int64, peer tg.InputPeerClass) (int64, error) {
	channelID, ok := channelIDFromLegacyInputPeer(userID, peer)
	if !ok {
		return 0, peerIDInvalidErr()
	}
	if err := r.validateInputPeerChannelAccess(ctx, userID, peer, channelID); err != nil {
		return 0, err
	}
	return channelID, nil
}

func channelInvalidErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrUserFrozen):
		return frozenMethodInvalidErr()
	case errors.Is(err, domain.ErrChannelTitleInvalid):
		return tgerr400("CHAT_TITLE_EMPTY")
	case errors.Is(err, domain.ErrChannelInvalid):
		return tgerr400("CHANNEL_INVALID")
	case errors.Is(err, domain.ErrChannelPrivate):
		return tgerr400("CHANNEL_PRIVATE")
	case errors.Is(err, domain.ErrChannelUserBanned):
		return tgerr400("USER_BANNED_IN_CHANNEL")
	case errors.Is(err, domain.ErrChannelWriteForbidden):
		return tgerr400("CHAT_WRITE_FORBIDDEN")
	case errors.Is(err, domain.ErrChannelAdminRequired):
		return tgerr400("CHAT_ADMIN_REQUIRED")
	case errors.Is(err, domain.ErrChannelMonoforumUnsupported):
		return tgerr400("CHANNEL_MONOFORUM_UNSUPPORTED")
	case errors.Is(err, domain.ErrUserAlreadyParticipant):
		return tgerr400("USER_ALREADY_PARTICIPANT")
	case errors.Is(err, domain.ErrReplyMessageIDInvalid):
		return replyMessageIDInvalidErr()
	case errors.Is(err, domain.ErrMessageRandomIDDuplicate):
		return randomIDDuplicateErr()
	default:
		if seconds, ok := domain.SlowModeWaitSeconds(err); ok {
			return tgerr.New(420, fmt.Sprintf("SLOWMODE_WAIT_%d", seconds))
		}
		return internalErr()
	}
}

func channelDeleteErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrMessageIDInvalid):
		return messageIDInvalidErr()
	case errors.Is(err, domain.ErrMessageAuthorRequired):
		return messageAuthorRequiredErr()
	case errors.Is(err, domain.ErrChannelAdminRequired):
		// 官方对 channels.deleteMessages 的越权删除返回该错误码。
		return tgerr400("MESSAGE_DELETE_FORBIDDEN")
	default:
		return channelInvalidErr(err)
	}
}

func channelDiscussionErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrLinkNotModified):
		return tgerr400("LINK_NOT_MODIFIED")
	case errors.Is(err, domain.ErrBroadcastIDInvalid):
		return tgerr400("BROADCAST_ID_INVALID")
	case errors.Is(err, domain.ErrMegagroupIDInvalid):
		return tgerr400("MEGAGROUP_ID_INVALID")
	case errors.Is(err, domain.ErrMegagroupPrehistoryHidden):
		return tgerr400("MEGAGROUP_PREHISTORY_HIDDEN")
	default:
		return channelAdminErr(err)
	}
}

func tgerr400(message string) error {
	return tgerr.New(400, message)
}
