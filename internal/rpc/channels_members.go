package rpc

import (
	"context"
	"errors"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"go.uber.org/zap"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onChannelsGetAdminedPublicChannels(ctx context.Context, req *tg.ChannelsGetAdminedPublicChannelsRequest) (tg.MessagesChatsClass, error) {
	if r.deps.Channels == nil {
		return &tg.MessagesChats{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.ByLocation {
		return &tg.MessagesChats{}, nil
	}
	channels, err := r.deps.Channels.ListAdminedPublicChannels(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.MessagesChats{Chats: tgChannels(userID, channels)}, nil
}

func (r *Router) onChannelsDeleteParticipantHistory(ctx context.Context, req *tg.ChannelsDeleteParticipantHistoryRequest) (*tg.MessagesAffectedHistory, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	peer, ok := r.domainPeerFromInputPeer(0, req.Participant)
	if !ok || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	res, err := r.deps.Channels.DeleteParticipantHistory(ctx, userID, domain.DeleteChannelParticipantHistoryRequest{
		UserID:            userID,
		ChannelID:         channelID,
		ParticipantUserID: peer.ID,
		Date:              int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelDeleteErr(err)
	}
	if res.Event.Pts != 0 {
		// deleteParticipantHistory fan-out 异步化（设计 Phase 0），与已异步的 messages.deleteMessages
		// 频道分支对齐。builder 纯 viewer 无关（仅 delete update + ChatMin，无 Users），无需预热。
		r.enqueueChannelFanout(ctx, channelFanoutMembers, userID, res.Channel.ID, res.Event.Pts, res.Recipients, func(_ context.Context, viewerUserID int64) *tg.Updates {
			return r.channelDeleteMessagesUpdates(viewerUserID, res.Channel, res.Event)
		})
	}
	return &tg.MessagesAffectedHistory{Pts: res.Channel.Pts, PtsCount: res.Event.PtsCount, Offset: res.Offset}, nil
}

func (r *Router) onChannelsToggleJoinToSend(ctx context.Context, req *tg.ChannelsToggleJoinToSendRequest) (tg.UpdatesClass, error) {
	return r.applyChannelAdminStateMutation(ctx, req.Channel, func(ctx context.Context, userID, channelID int64) (domain.Channel, error) {
		return r.deps.Channels.SetJoinToSend(ctx, userID, channelID, req.Enabled)
	})
}

func (r *Router) onChannelsToggleJoinRequest(ctx context.Context, req *tg.ChannelsToggleJoinRequestRequest) (tg.UpdatesClass, error) {
	return r.applyChannelAdminStateMutation(ctx, req.Channel, func(ctx context.Context, userID, channelID int64) (domain.Channel, error) {
		return r.deps.Channels.SetJoinRequest(ctx, userID, channelID, req.Enabled)
	})
}

func (r *Router) onChannelsToggleParticipantsHidden(ctx context.Context, req *tg.ChannelsToggleParticipantsHiddenRequest) (tg.UpdatesClass, error) {
	return r.applyChannelAdminStateMutation(ctx, req.Channel, func(ctx context.Context, userID, channelID int64) (domain.Channel, error) {
		return r.deps.Channels.SetParticipantsHidden(ctx, userID, channelID, req.Enabled)
	})
}

func (r *Router) onChannelsGetMessageAuthor(ctx context.Context, req *tg.ChannelsGetMessageAuthorRequest) (tg.UserClass, error) {
	userID, view, err := r.channelView(ctx, req.Channel)
	if err != nil {
		return nil, err
	}
	author, err := r.deps.Channels.GetMessageAuthor(ctx, userID, domain.GetChannelMessageAuthorRequest{
		UserID:    userID,
		ChannelID: view.Channel.ID,
		ID:        req.ID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return nil, messageIDInvalidErr()
		}
		return nil, channelInvalidErr(err)
	}
	users := r.tgUsersForIDs(ctx, userID, []int64{author.SenderUserID})
	if len(users) == 0 {
		return nil, peerIDInvalidErr()
	}
	return users[0], nil
}

func (r *Router) onChannelsGetParticipants(ctx context.Context, req *tg.ChannelsGetParticipantsRequest) (tg.ChannelsChannelParticipantsClass, error) {
	if r.deps.Channels == nil {
		return &tg.ChannelsChannelParticipants{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	ref, ok := inputChannelRef(req.Channel)
	if !ok {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	filter := domainChannelParticipantsFilter(req.Filter)
	if utf8.RuneCountInString(filter.Query) > domain.MaxChannelParticipantsQueryLength {
		return nil, limitInvalidErr()
	}
	list, err := r.deps.Channels.GetParticipants(ctx, userID, ref.ID, filter, req.Offset, req.Limit)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	if !inputChannelAccessHashMatches(ref, list.Channel) {
		return nil, channelInvalidErr(domain.ErrChannelPrivate)
	}
	if req.Hash != 0 && list.Hash == req.Hash {
		return &tg.ChannelsChannelParticipantsNotModified{}, nil
	}
	participants := make([]tg.ChannelParticipantClass, 0, len(list.Participants))
	userIDs := make([]int64, 0, len(list.Participants))
	seenUserIDs := make(map[int64]struct{}, len(list.Participants)*2)
	addUserID := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seenUserIDs[id]; ok {
			return
		}
		seenUserIDs[id] = struct{}{}
		userIDs = append(userIDs, id)
	}
	for _, member := range list.Participants {
		participant := tgChannelParticipant(userID, member)
		participants = append(participants, participant)
		for _, id := range channelParticipantUserRefs(participant) {
			addUserID(id)
		}
	}
	users := r.tgUsers(list.Users)
	if len(users) == 0 {
		users = r.tgUsersForIDs(ctx, userID, userIDs)
	} else {
		present := make(map[int64]struct{}, len(users))
		for _, item := range users {
			if u, ok := item.(*tg.User); ok {
				present[u.ID] = struct{}{}
			}
		}
		missing := make([]int64, 0, len(userIDs))
		for _, id := range userIDs {
			if _, ok := present[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			users = append(users, r.tgUsersForIDs(ctx, userID, missing)...)
		}
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, userID, users, nil)
	r.log.Debug("channels.getParticipants result",
		zap.Int64("channel_id", ref.ID),
		zap.String("filter", string(filter.Kind)),
		zap.Int("count", list.Count),
		zap.Int("participants", len(participants)),
		zap.Int("users", len(users)),
	)
	return &tg.ChannelsChannelParticipants{
		Count:        list.Count,
		Participants: participants,
		Chats:        []tg.ChatClass{},
		Users:        users,
	}, nil
}

func (r *Router) onChannelsGetParticipant(ctx context.Context, req *tg.ChannelsGetParticipantRequest) (*tg.ChannelsChannelParticipant, error) {
	if r.deps.Channels == nil {
		return &tg.ChannelsChannelParticipant{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.Participant)
	if !ok || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	member, err := r.deps.Channels.GetParticipant(ctx, userID, channelID, peer.ID)
	if err != nil {
		return nil, channelParticipantErr(err)
	}
	participant := tgChannelParticipant(userID, member)
	users := r.tgUsersForIDs(ctx, userID, channelParticipantUserRefs(participant))
	r.applyStoryMaxIDsToPeerObjects(ctx, userID, users, nil)
	return &tg.ChannelsChannelParticipant{
		Participant: participant,
		Users:       users,
	}, nil
}

func channelParticipantErr(err error) error {
	if errors.Is(err, domain.ErrUserNotParticipant) {
		return tgerr400("USER_NOT_PARTICIPANT")
	}
	return channelInvalidErr(err)
}

func channelParticipantUserRefs(participant tg.ChannelParticipantClass) []int64 {
	ids := make([]int64, 0, 3)
	add := func(id int64) {
		if id == 0 {
			return
		}
		for _, existing := range ids {
			if existing == id {
				return
			}
		}
		ids = append(ids, id)
	}
	addPeer := func(peer tg.PeerClass) {
		if p, ok := peer.(*tg.PeerUser); ok {
			add(p.UserID)
		}
	}
	switch p := participant.(type) {
	case *tg.ChannelParticipantCreator:
		add(p.UserID)
	case *tg.ChannelParticipantAdmin:
		add(p.UserID)
		add(p.PromotedBy)
		if inviterID, ok := p.GetInviterID(); ok {
			add(inviterID)
		}
	case *tg.ChannelParticipantSelf:
		add(p.UserID)
		add(p.InviterID)
	case *tg.ChannelParticipant:
		add(p.UserID)
	case *tg.ChannelParticipantLeft:
		addPeer(p.Peer)
	case *tg.ChannelParticipantBanned:
		addPeer(p.Peer)
		add(p.KickedBy)
	}
	return ids
}

func (r *Router) onChannelsInviteToChannel(ctx context.Context, req *tg.ChannelsInviteToChannelRequest) (*tg.MessagesInvitedUsers, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if len(req.Users) == 0 || len(req.Users) > domain.MaxChannelInviteUsers {
		return nil, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	userIDs, err := r.userIDsFromInputUsers(ctx, userID, req.Users)
	if err != nil {
		return nil, err
	}
	res, err := r.deps.Channels.InviteToChannel(ctx, userID, channelID, userIDs, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, channelInviteErr(err)
	}
	r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
	r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
	cache := newViewerPeerCache(r)
	updates := r.channelOperationUpdatesWithPeerCache(ctx, userID, res, cache)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOperationUpdatesWithPeerCache(ctx, viewerUserID, res, cache)
	})
	return &tg.MessagesInvitedUsers{Updates: updates, MissingInvitees: []tg.MissingInvitee{}}, nil
}

func (r *Router) onChannelsJoinChannel(ctx context.Context, input tg.InputChannelClass) (tg.MessagesChatInviteJoinResultClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	ref, ok := inputChannelRef(input)
	if !ok {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	if ref.CheckAccessHash {
		channel, err := r.deps.Channels.GetJoinableChannel(ctx, userID, ref.ID)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		if !inputChannelAccessHashMatches(ref, channel) {
			return nil, channelInvalidErr(domain.ErrChannelPrivate)
		}
	}
	res, err := r.deps.Channels.JoinChannel(ctx, userID, ref.ID, int(r.clock.Now().Unix()))
	if err != nil {
		if errors.Is(err, domain.ErrInviteRequestSent) && res.Channel.ID != 0 {
			r.pushPendingJoinRequestsToAdmins(ctx, res.Channel)
		}
		return nil, channelInviteErr(err)
	}
	r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
	r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
	updates := r.channelOperationUpdates(ctx, userID, res)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOperationUpdates(ctx, viewerUserID, res)
	})
	// Layer 227：channels.joinChannel 返回 messages.ChatInviteJoinResult；
	// 正常加入即 chatInviteJoinResultOk 包裹本次操作的 updates。
	return &tg.MessagesChatInviteJoinResultOk{Updates: updates}, nil
}

func (r *Router) onChannelsLeaveChannel(ctx context.Context, input tg.InputChannelClass) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	res, err := r.deps.Channels.LeaveChannel(ctx, userID, channelID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, channelAdminErr(err)
	}
	r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
	r.removeOnlineChannelMemberships(res.Channel.ID, userID)
	r.recordChannelStateForUser(ctx, userID, res.Channel.ID, true)
	updates := r.channelOperationUpdates(ctx, userID, res)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOperationUpdates(ctx, viewerUserID, res)
	})
	return updates, nil
}

// recordChannelStateForUser 给该账号写一条 durable channel 状态事件：
// 离线设备经 difference 收到 updateChannel 后重拉 channel，发现 left/
// forbidden 并移除会话；excludeCurrent 时当前 session 由 RPC 响应承担。
func (r *Router) recordChannelStateForUser(ctx context.Context, userID, channelID int64, excludeCurrent bool) {
	if r.deps.Updates == nil || userID == 0 || channelID == 0 {
		return
	}
	authKeyID := [8]byte{}
	excludeSessionID := int64(0)
	if excludeCurrent {
		authKeyID, _ = AuthKeyIDFrom(ctx)
		excludeSessionID, _ = SessionIDFrom(ctx)
	}
	excludeAuthKeyID := [8]byte{}
	if excludeCurrent {
		excludeAuthKeyID = rawAuthKeyIDForOrigin(ctx)
	}
	event, _, err := r.deps.Updates.RecordChannelState(ctx, authKeyID, userID, channelID, excludeAuthKeyID, excludeSessionID)
	if err != nil {
		return
	}
	if excludeCurrent {
		r.bookkeepAuxPtsForCurrentSession(ctx, event)
	}
}

func (r *Router) onChannelsEditAdmin(ctx context.Context, req *tg.ChannelsEditAdminRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	target, found, err := r.userFromInput(ctx, userID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || target.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	res, err := r.deps.Channels.EditAdmin(ctx, userID, domain.EditChannelAdminRequest{
		UserID:      userID,
		ChannelID:   channelID,
		MemberID:    target.ID,
		AdminRights: domainChannelAdminRights(req.AdminRights),
		RankSet:     req.Flags.Has(0),
		Rank:        req.Rank,
		Date:        int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelAdminErr(err)
	}
	r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
	if res.Participant.Status == domain.ChannelMemberActive {
		r.addOnlineChannelMemberships(res.Channel.ID, res.Participant.UserID)
	} else {
		r.removeOnlineChannelMemberships(res.Channel.ID, res.Participant.UserID)
	}
	cache := newViewerPeerCache(r)
	updates := r.channelParticipantUpdatesWithPeerCache(ctx, userID, userID, res.Channel, res.Previous, res.Participant, res.Date, cache)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelParticipantUpdatesWithPeerCache(ctx, viewerUserID, userID, res.Channel, res.Previous, res.Participant, res.Date, cache)
	})
	return updates, nil
}

func (r *Router) onChannelsEditBanned(ctx context.Context, req *tg.ChannelsEditBannedRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	participant, ok := r.domainPeerFromInputPeer(userID, req.Participant)
	if !ok || participant.Type != domain.PeerTypeUser || participant.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	res, err := r.deps.Channels.EditBanned(ctx, userID, domain.EditChannelBannedRequest{
		UserID:       userID,
		ChannelID:    channelID,
		Participant:  participant,
		BannedRights: domainChannelBannedRights(req.BannedRights),
		Date:         int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelAdminErr(err)
	}
	r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
	// 与 onChannelsEditAdmin 对称维护在线成员路由索引：踢出/封禁后立即摘除，
	// 否则被踢在线成员会留着 stale byMemberChannel 条目直到断线（占用实时
	// fan-out cap 名额，且群通话推送等未过 PG 复核的路径会继续投递）。
	if res.Participant.Status == domain.ChannelMemberActive {
		r.addOnlineChannelMemberships(res.Channel.ID, res.Participant.UserID)
	} else {
		r.removeOnlineChannelMemberships(res.Channel.ID, res.Participant.UserID)
	}
	if res.Participant.Status == domain.ChannelMemberKicked && res.Previous.Status == domain.ChannelMemberActive {
		r.recordChannelStateForUser(ctx, res.Participant.UserID, res.Channel.ID, false)
	}
	cache := newViewerPeerCache(r)
	build := func(viewerUserID int64) *tg.Updates {
		updates := r.channelParticipantUpdatesWithPeerCache(ctx, viewerUserID, userID, res.Channel, res.Previous, res.Participant, res.Date, cache)
		if updates != nil && res.ServiceEvent.Pts != 0 {
			// megagroup 踢人服务消息占 channel pts，必须先于 participant
			// update 应用，让成员面板/人数与消息流一起收敛。
			if update := tgChannelUpdate(viewerUserID, res.ServiceEvent); update != nil {
				updates.Updates = append([]tg.UpdateClass{update}, updates.Updates...)
			}
		}
		return updates
	}
	updates := build(userID)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, build)
	return updates, nil
}

func (r *Router) onChannelsGetAdminLog(ctx context.Context, req *tg.ChannelsGetAdminLogRequest) (*tg.ChannelsAdminLogResults, error) {
	if r.deps.Channels == nil {
		return &tg.ChannelsAdminLogResults{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	adminIDs := []int64(nil)
	if admins, ok := req.GetAdmins(); ok && len(admins) > 0 {
		if len(admins) > domain.MaxChannelAdminLogAdmins {
			return nil, limitInvalidErr()
		}
		adminIDs, err = r.userIDsFromInputUsers(ctx, userID, admins)
		if err != nil {
			return nil, err
		}
	}
	res, err := r.deps.Channels.ListAdminLog(ctx, userID, domain.ChannelAdminLogRequest{
		UserID:       userID,
		ChannelID:    channelID,
		Query:        req.Q,
		AdminUserIDs: adminIDs,
		MaxID:        req.MaxID,
		MinID:        req.MinID,
		Limit:        req.Limit,
		Filter:       domainChannelAdminLogFilter(req),
	})
	if err != nil {
		return nil, channelAdminErr(err)
	}
	events := tgChannelAdminLogEvents(userID, res.Events)
	chats := []tg.ChatClass{tgChannelChatMin(userID, res.Channel)}
	users := r.channelAdminLogUsers(ctx, userID, res.Events)
	r.applyStoryMaxIDsToPeerObjects(ctx, userID, users, chats)
	return &tg.ChannelsAdminLogResults{
		Events: events,
		Chats:  chats,
		Users:  users,
	}, nil
}

func (r *Router) onMessagesGetAdminsWithInvites(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatAdminsWithInvites, error) {
	userID, view, err := r.inviteManagementChannelView(ctx, peer)
	if err != nil {
		return nil, err
	}
	counts, err := r.deps.Channels.ListAdminsWithInvites(ctx, userID, view.Channel.ID)
	if err != nil {
		return nil, channelInviteErr(err)
	}
	admins := make([]tg.ChatAdminWithInvites, 0, len(counts))
	userIDs := make([]int64, 0, len(counts))
	for _, count := range counts {
		admins = append(admins, tg.ChatAdminWithInvites{
			AdminID:             count.AdminUserID,
			InvitesCount:        count.InvitesCount,
			RevokedInvitesCount: count.RevokedInvitesCount,
		})
		userIDs = append(userIDs, count.AdminUserID)
	}
	return &tg.MessagesChatAdminsWithInvites{
		Admins: admins,
		Users:  r.tgUsersForIDs(ctx, userID, userIDs),
	}, nil
}

func (r *Router) onMessagesHideChatJoinRequest(ctx context.Context, req *tg.MessagesHideChatJoinRequestRequest) (tg.UpdatesClass, error) {
	userID, view, err := r.inviteManagementChannelView(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	targets, err := r.userIDsFromInputUsers(ctx, userID, []tg.InputUserClass{req.UserID})
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, userIDInvalidErr()
	}
	res, err := r.deps.Channels.HideChatJoinRequest(ctx, userID, domain.HideChannelJoinRequestRequest{
		UserID:       userID,
		ChannelID:    view.Channel.ID,
		TargetUserID: targets[0],
		Approved:     req.Approved,
		Date:         int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelInviteErr(err)
	}
	r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
	r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
	updates := r.channelOperationUpdates(ctx, userID, res)
	r.appendPendingJoinRequestsUpdate(ctx, userID, updates, res.Channel)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOperationUpdates(ctx, viewerUserID, res)
	})
	r.pushPendingJoinRequestsToAdmins(ctx, res.Channel)
	return updates, nil
}

func (r *Router) onMessagesHideAllChatJoinRequests(ctx context.Context, req *tg.MessagesHideAllChatJoinRequestsRequest) (tg.UpdatesClass, error) {
	userID, view, err := r.inviteManagementChannelView(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	link, hasLink := req.GetLink()
	if hasLink && len(link) > maxChatInviteLinkLength {
		return nil, limitInvalidErr()
	}
	hash := ""
	if hasLink && strings.TrimSpace(link) != "" {
		hash, err = channelInviteHashFromLink(link)
		if err != nil {
			return nil, err
		}
	}
	res, err := r.deps.Channels.HideAllChatJoinRequests(ctx, userID, domain.HideChannelJoinRequestsRequest{
		UserID:    userID,
		ChannelID: view.Channel.ID,
		Hash:      hash,
		Approved:  req.Approved,
		Limit:     domain.MaxChannelHideJoinRequests,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelInviteErr(err)
	}
	r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
	r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
	updates := r.channelOperationUpdates(ctx, userID, res)
	r.appendPendingJoinRequestsUpdate(ctx, userID, updates, res.Channel)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOperationUpdates(ctx, viewerUserID, res)
	})
	r.pushPendingJoinRequestsToAdmins(ctx, res.Channel)
	return updates, nil
}

func canViewChannelJoinRequests(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator ||
		(member.Role == domain.ChannelRoleAdmin && (member.AdminRights.InviteUsers || member.AdminRights.ChangeInfo))
}

func (r *Router) pendingJoinRequestsUpdates(ctx context.Context, viewerUserID int64, channel domain.Channel) *tg.Updates {
	if r.deps.Channels == nil || channel.ID == 0 {
		return nil
	}
	pending, err := r.deps.Channels.PendingJoinRequests(ctx, channel.ID, domain.MaxChannelPendingJoinRecentRequesters)
	if err != nil {
		return nil
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePendingJoinRequests{
			Peer:             &tg.PeerChannel{ChannelID: channel.ID},
			RequestsPending:  pending.Count,
			RecentRequesters: pending.RecentRequesters,
		}},
		Users: r.tgUsersForIDs(ctx, viewerUserID, pending.RecentRequesters),
		Chats: []tg.ChatClass{tgChannelChatMin(viewerUserID, channel)},
		Date:  int(r.clock.Now().Unix()),
		Seq:   0,
	}
}

func (r *Router) appendPendingJoinRequestsUpdate(ctx context.Context, viewerUserID int64, updates *tg.Updates, channel domain.Channel) {
	if updates == nil {
		return
	}
	pending := r.pendingJoinRequestsUpdates(ctx, viewerUserID, channel)
	if pending == nil {
		return
	}
	updates.Updates = append(updates.Updates, pending.Updates...)
	updates.Users = append(updates.Users, pending.Users...)
}

func (r *Router) pushPendingJoinRequestsToAdmins(ctx context.Context, channel domain.Channel) {
	if r.deps.Channels == nil || r.deps.Sessions == nil || channel.ID == 0 {
		return
	}
	adminIDs, err := r.deps.Channels.InviteAdminMemberIDs(ctx, channel.ID, domain.MaxChannelRealtimeFanout)
	if err != nil || len(adminIDs) == 0 {
		adminIDs = []int64{channel.CreatorUserID}
	}
	seen := make(map[int64]struct{}, len(adminIDs))
	for _, adminID := range adminIDs {
		if adminID == 0 {
			continue
		}
		if _, ok := seen[adminID]; ok {
			continue
		}
		seen[adminID] = struct{}{}
		updates := r.pendingJoinRequestsUpdates(ctx, adminID, channel)
		if updates == nil {
			continue
		}
		r.pushUserUpdates(ctx, adminID, updates)
	}
}

func (r *Router) channelParticipantUpdates(ctx context.Context, viewerUserID, actorUserID int64, channel domain.Channel, previous, participant domain.ChannelMember, date int) *tg.Updates {
	return r.channelParticipantUpdatesWithPeerCache(ctx, viewerUserID, actorUserID, channel, previous, participant, date, newViewerPeerCache(r))
}

func (r *Router) channelParticipantUpdatesWithPeerCache(ctx context.Context, viewerUserID, actorUserID int64, channel domain.Channel, previous, participant domain.ChannelMember, date int, cache *viewerPeerCache) *tg.Updates {
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	update := &tg.UpdateChannelParticipant{
		ChannelID: channel.ID,
		Date:      date,
		ActorID:   actorUserID,
		UserID:    participant.UserID,
	}
	if update.ActorID == 0 {
		update.ActorID = viewerUserID
	}
	if previous.UserID != 0 {
		update.SetPrevParticipant(tgChannelParticipantForUpdate(viewerUserID, previous))
	}
	if participant.UserID != 0 {
		update.SetNewParticipant(tgChannelParticipantForUpdate(viewerUserID, participant))
	}
	// 当事成员必须收到完整 channel 投影：被踢/被封禁状态只有通过非 min
	// 对象的 left/banned_rights 才会被客户端应用，min 形态会让被踢者
	// 永远不知道自己已离开会话。其它接收者用 min 保护各自本地权限。
	var chat tg.ChatClass
	if viewerUserID != 0 && viewerUserID == participant.UserID {
		self := participant
		chat = tgChannelChat(viewerUserID, channel, &self)
	} else {
		chat = tgChannelChatMin(viewerUserID, channel)
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{update, &tg.UpdateChannel{ChannelID: channel.ID}},
		Users:   tgUsersForViewer(viewerUserID, cache.usersForIDs(ctx, viewerUserID, []int64{participant.UserID, participant.InviterUserID, previous.UserID, previous.InviterUserID, update.ActorID})),
		Chats:   []tg.ChatClass{chat},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func (r *Router) channelOwnershipTransferUpdatesWithPeerCache(ctx context.Context, viewerUserID, actorUserID int64, res domain.TransferChannelOwnershipResult, cache *viewerPeerCache) *tg.Updates {
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	date := res.Date
	if date == 0 {
		date = int(r.clock.Now().Unix())
	}
	updates := make([]tg.UpdateClass, 0, len(res.Events)+1)
	userIDs := []int64{actorUserID, res.PreviousOwner.UserID, res.OldOwner.UserID, res.OldOwner.InviterUserID, res.PreviousNewOwner.UserID, res.NewOwner.UserID, res.NewOwner.InviterUserID}
	for _, event := range res.Events {
		update := tgChannelUpdate(viewerUserID, event)
		if update == nil {
			continue
		}
		updates = append(updates, update)
		userIDs = append(userIDs, event.SenderUserID, event.Previous.UserID, event.Previous.InviterUserID, event.Participant.UserID, event.Participant.InviterUserID)
	}
	updates = append(updates, &tg.UpdateChannel{ChannelID: res.Channel.ID})
	var self *domain.ChannelMember
	switch viewerUserID {
	case res.OldOwner.UserID:
		member := res.OldOwner
		self = &member
	case res.NewOwner.UserID:
		member := res.NewOwner
		self = &member
	}
	chat := tgChannelChatMin(viewerUserID, res.Channel)
	if self != nil {
		chat = tgChannelChat(viewerUserID, res.Channel, self)
	}
	return &tg.Updates{
		Updates: updates,
		Users:   tgUsersForViewer(viewerUserID, cache.usersForIDs(ctx, viewerUserID, uniqueRecipientIDs(userIDs))),
		Chats:   []tg.ChatClass{chat},
		Date:    date,
		Seq:     0,
	}
}

func domainChannelAdminLogFilter(req *tg.ChannelsGetAdminLogRequest) domain.ChannelAdminLogFilter {
	filter, ok := req.GetEventsFilter()
	if !ok {
		return domain.ChannelAdminLogFilter{}
	}
	return domain.ChannelAdminLogFilter{
		Join:      filter.GetJoin(),
		Leave:     filter.GetLeave(),
		Invite:    filter.GetInvite(),
		Ban:       filter.GetBan(),
		Unban:     filter.GetUnban(),
		Kick:      filter.GetKick(),
		Unkick:    filter.GetUnkick(),
		Promote:   filter.GetPromote(),
		Demote:    filter.GetDemote(),
		Info:      filter.GetInfo(),
		Settings:  filter.GetSettings(),
		Pinned:    filter.GetPinned(),
		Edit:      filter.GetEdit(),
		Delete:    filter.GetDelete(),
		Send:      filter.GetSend(),
		Invites:   filter.GetInvites(),
		Forums:    filter.GetForums(),
		SubExtend: filter.GetSubExtend(),
		EditRank:  filter.GetEditRank(),
	}
}

func (r *Router) channelAdminLogUsers(ctx context.Context, currentUserID int64, events []domain.ChannelAdminLogEvent) []tg.UserClass {
	if r.deps.Users == nil || len(events) == 0 {
		return nil
	}
	ids := make(map[int64]struct{}, len(events))
	add := func(id int64) {
		if id != 0 {
			ids[id] = struct{}{}
		}
	}
	addMember := func(member *domain.ChannelMember) {
		if member != nil {
			add(member.UserID)
			add(member.InviterUserID)
		}
	}
	addMessage := func(msg *domain.ChannelMessage) {
		if msg != nil {
			add(msg.SenderUserID)
			if msg.From.Type == domain.PeerTypeUser {
				add(msg.From.ID)
			}
		}
	}
	for _, event := range events {
		add(event.UserID)
		addMember(event.PrevParticipant)
		addMember(event.NewParticipant)
		addMember(event.Participant)
		addMessage(event.Message)
		addMessage(event.PrevMessage)
		addMessage(event.NewMessage)
	}
	userIDs := make([]int64, 0, len(ids))
	for id := range ids {
		userIDs = append(userIDs, id)
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	return r.tgUsersForIDs(ctx, currentUserID, userIDs)
}

func domainChannelParticipantsFilter(filter tg.ChannelParticipantsFilterClass) domain.ChannelParticipantsFilter {
	switch f := filter.(type) {
	case *tg.ChannelParticipantsAdmins:
		return domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsAdmins}
	case *tg.ChannelParticipantsKicked:
		return domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked, Query: f.Q}
	case *tg.ChannelParticipantsBanned:
		return domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsBanned, Query: f.Q}
	case *tg.ChannelParticipantsSearch:
		return domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsSearch, Query: f.Q}
	case *tg.ChannelParticipantsBots:
		return domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsBots}
	case *tg.ChannelParticipantsContacts:
		return domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsContacts, Query: f.Q}
	case *tg.ChannelParticipantsMentions:
		return domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsMentions, Query: f.Q}
	default:
		return domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsRecent}
	}
}

func channelAdminErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrChannelNotModified):
		return tgerr400("CHAT_NOT_MODIFIED")
	case errors.Is(err, domain.ErrChatPublicRequired):
		return tgerr400("CHAT_PUBLIC_REQUIRED")
	case errors.Is(err, domain.ErrChatDiscussionUnallowed):
		return tgerr400("CHAT_DISCUSSION_UNALLOWED")
	case errors.Is(err, domain.ErrChannelRightForbidden):
		return tgerr.New(403, "RIGHT_FORBIDDEN")
	case errors.Is(err, domain.ErrChannelUserCreator):
		return tgerr400("USER_CREATOR")
	case errors.Is(err, domain.ErrUserNotParticipant):
		return tgerr400("USER_NOT_PARTICIPANT")
	case errors.Is(err, domain.ErrMegagroupIDInvalid):
		return tgerr400("MEGAGROUP_ID_INVALID")
	case errors.Is(err, domain.ErrMessageIDInvalid):
		return messageIDInvalidErr()
	default:
		return channelInvalidErr(err)
	}
}

func channelTransferErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrChannelAdminRequired):
		return tgerr400("CHAT_CREATOR_REQUIRED")
	case errors.Is(err, domain.ErrUserNotParticipant):
		return tgerr400("PARTICIPANT_MISSING")
	default:
		return channelAdminErr(err)
	}
}
