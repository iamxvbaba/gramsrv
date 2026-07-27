package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesExportChatInvite(ctx context.Context, req *tg.MessagesExportChatInviteRequest) (tg.ExportedChatInviteClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	if req.UsageLimit < 0 || req.ExpireDate < 0 || len(req.Title) > domain.MaxChannelInviteTitleLength {
		return nil, limitInvalidErr()
	}
	res, err := r.deps.Channels.ExportInvite(ctx, userID, domain.ExportChannelInviteRequest{
		UserID:                userID,
		ChannelID:             peer.ID,
		Title:                 req.Title,
		RequestNeeded:         req.RequestNeeded,
		ExpireDate:            req.ExpireDate,
		UsageLimit:            req.UsageLimit,
		LegacyRevokePermanent: req.LegacyRevokePermanent,
		Date:                  int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelInviteErr(err)
	}
	r.invalidateRPCProjectionForChannel(res.Channel.ID)
	return r.tgExportedChannelInvite(res.Invite), nil
}

func (r *Router) onMessagesCheckChatInvite(ctx context.Context, hash string) (tg.ChatInviteClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	res, err := r.deps.Channels.CheckInvite(ctx, userID, hash, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, channelInviteErr(err)
	}
	if res.Already {
		return &tg.ChatInviteAlready{Chat: tgChannelChat(userID, res.Channel, &res.Self)}, nil
	}
	return &tg.ChatInvite{
		Channel:           true,
		Broadcast:         res.Channel.Broadcast,
		Megagroup:         res.Channel.Megagroup,
		Public:            res.Channel.Username != "",
		RequestNeeded:     res.Invite.RequestNeeded,
		Title:             res.Channel.Title,
		About:             res.Channel.About,
		Photo:             &tg.PhotoEmpty{},
		ParticipantsCount: res.Channel.ParticipantsCount,
	}, nil
}

func (r *Router) onMessagesImportChatInvite(ctx context.Context, hash string) (tg.MessagesChatInviteJoinResultClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	res, err := r.deps.Channels.ImportInvite(ctx, userID, domain.ImportChannelInviteRequest{
		UserID: userID,
		Hash:   hash,
		Date:   int(r.clock.Now().Unix()),
	})
	if err != nil {
		if errors.Is(err, domain.ErrInviteRequestSent) && res.Channel.ID != 0 {
			r.pushPendingJoinRequestsToAdmins(ctx, res.Channel)
		}
		return nil, channelInviteErr(err)
	}
	r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
	updates := r.channelOperationUpdates(ctx, userID, res)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOperationUpdates(ctx, viewerUserID, res)
	})
	// Layer 227：messages.importChatInvite 返回 messages.ChatInviteJoinResult；
	// 正常加入即 chatInviteJoinResultOk 包裹本次操作的 updates。
	return &tg.MessagesChatInviteJoinResultOk{Updates: updates}, nil
}

func (r *Router) onMessagesGetExportedChatInvites(ctx context.Context, req *tg.MessagesGetExportedChatInvitesRequest) (*tg.MessagesExportedChatInvites, error) {
	userID, view, err := r.inviteManagementChannelView(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	if req.Limit < 0 || req.Limit > maxChatInviteListLimit || len(req.OffsetLink) > maxChatInviteLinkLength {
		return nil, limitInvalidErr()
	}
	adminID := userID
	if !inputUserIsEmpty(req.AdminID) {
		admins, err := r.userIDsFromInputUsers(ctx, userID, []tg.InputUserClass{req.AdminID})
		if err != nil {
			return nil, err
		}
		if len(admins) > 0 {
			adminID = admins[0]
		}
	}
	offsetHash := ""
	if req.OffsetLink != "" {
		offsetHash, err = channelInviteHashFromLink(req.OffsetLink)
		if err != nil {
			return nil, err
		}
	}
	list, err := r.deps.Channels.ListExportedInvites(ctx, userID, domain.ChannelInviteListRequest{
		UserID:      userID,
		ChannelID:   view.Channel.ID,
		AdminUserID: adminID,
		Revoked:     req.Revoked,
		OffsetDate:  req.OffsetDate,
		OffsetHash:  offsetHash,
		Limit:       req.Limit,
	})
	if err != nil {
		return nil, channelInviteErr(err)
	}
	userIDs := []int64{adminID}
	invites := make([]tg.ExportedChatInviteClass, 0, len(list.Invites))
	for _, invite := range list.Invites {
		invites = append(invites, r.tgExportedChannelInvite(invite))
		userIDs = append(userIDs, invite.AdminUserID)
	}
	return &tg.MessagesExportedChatInvites{
		Count:   list.Count,
		Invites: invites,
		Users:   r.tgUsersForIDs(ctx, userID, userIDs),
	}, nil
}

func (r *Router) onMessagesGetExportedChatInvite(ctx context.Context, req *tg.MessagesGetExportedChatInviteRequest) (tg.MessagesExportedChatInviteClass, error) {
	userID, view, err := r.inviteManagementChannelView(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	hash, err := channelInviteHashFromLink(req.Link)
	if err != nil {
		return nil, err
	}
	invite, err := r.deps.Channels.GetExportedInvite(ctx, userID, domain.GetChannelInviteRequest{
		UserID:    userID,
		ChannelID: view.Channel.ID,
		Hash:      hash,
	})
	if err != nil {
		return nil, channelInviteErr(err)
	}
	return &tg.MessagesExportedChatInvite{
		Invite: r.tgExportedChannelInvite(invite),
		Users:  r.tgUsersForIDs(ctx, userID, []int64{invite.AdminUserID}),
	}, nil
}

func (r *Router) onMessagesEditExportedChatInvite(ctx context.Context, req *tg.MessagesEditExportedChatInviteRequest) (tg.MessagesExportedChatInviteClass, error) {
	userID, view, err := r.inviteManagementChannelView(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	hash, err := channelInviteHashFromLink(req.Link)
	if err != nil {
		return nil, err
	}
	if req.ExpireDate < 0 || req.UsageLimit < 0 || len(req.Title) > domain.MaxChannelInviteTitleLength {
		return nil, limitInvalidErr()
	}
	expireDate, hasExpireDate := req.GetExpireDate()
	usageLimit, hasUsageLimit := req.GetUsageLimit()
	requestNeeded, hasRequestNeeded := req.GetRequestNeeded()
	title, hasTitle := req.GetTitle()
	edited, err := r.deps.Channels.EditExportedInvite(ctx, userID, domain.EditChannelInviteRequest{
		UserID:           userID,
		ChannelID:        view.Channel.ID,
		Hash:             hash,
		Revoked:          req.Revoked,
		HasExpireDate:    hasExpireDate,
		ExpireDate:       expireDate,
		HasUsageLimit:    hasUsageLimit,
		UsageLimit:       usageLimit,
		HasRequestNeeded: hasRequestNeeded,
		RequestNeeded:    requestNeeded,
		HasTitle:         hasTitle,
		Title:            title,
		Date:             int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelInviteErr(err)
	}
	r.invalidateRPCProjectionForChannel(view.Channel.ID)
	users := r.tgUsersForIDs(ctx, userID, []int64{edited.Invite.AdminUserID})
	if edited.NewInvite != nil {
		return &tg.MessagesExportedChatInviteReplaced{
			Invite:    r.tgExportedChannelInvite(edited.Invite),
			NewInvite: r.tgExportedChannelInvite(*edited.NewInvite),
			Users:     users,
		}, nil
	}
	return &tg.MessagesExportedChatInvite{Invite: r.tgExportedChannelInvite(edited.Invite), Users: users}, nil
}

func (r *Router) onMessagesDeleteRevokedExportedChatInvites(ctx context.Context, req *tg.MessagesDeleteRevokedExportedChatInvitesRequest) (bool, error) {
	userID, view, err := r.inviteManagementChannelView(ctx, req.Peer)
	if err != nil {
		return false, err
	}
	adminID := userID
	if !inputUserIsEmpty(req.AdminID) {
		admins, err := r.userIDsFromInputUsers(ctx, userID, []tg.InputUserClass{req.AdminID})
		if err != nil {
			return false, err
		}
		if len(admins) > 0 {
			adminID = admins[0]
		}
	}
	if err := r.deps.Channels.DeleteRevokedExportedInvites(ctx, userID, domain.DeleteRevokedChannelInvitesRequest{
		UserID:      userID,
		ChannelID:   view.Channel.ID,
		AdminUserID: adminID,
		Limit:       domain.MaxChannelHideJoinRequests,
	}); err != nil {
		return false, channelInviteErr(err)
	}
	r.invalidateRPCProjectionForChannel(view.Channel.ID)
	return true, nil
}

func (r *Router) onMessagesDeleteExportedChatInvite(ctx context.Context, req *tg.MessagesDeleteExportedChatInviteRequest) (bool, error) {
	userID, view, err := r.inviteManagementChannelView(ctx, req.Peer)
	if err != nil {
		return false, err
	}
	hash, err := channelInviteHashFromLink(req.Link)
	if err != nil {
		return false, err
	}
	if err := r.deps.Channels.DeleteExportedInvite(ctx, userID, domain.DeleteChannelInviteRequest{
		UserID:    userID,
		ChannelID: view.Channel.ID,
		Hash:      hash,
	}); err != nil {
		return false, channelInviteErr(err)
	}
	r.invalidateRPCProjectionForChannel(view.Channel.ID)
	return true, nil
}

func (r *Router) onMessagesGetChatInviteImporters(ctx context.Context, req *tg.MessagesGetChatInviteImportersRequest) (*tg.MessagesChatInviteImporters, error) {
	userID, view, err := r.inviteManagementChannelView(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	link, hasLink := req.GetLink()
	query, hasQuery := req.GetQ()
	if hasLink && hasQuery && strings.TrimSpace(link) != "" && strings.TrimSpace(query) != "" {
		return nil, tgerr400("SEARCH_WITH_LINK_NOT_SUPPORTED")
	}
	if req.Limit < 0 || req.Limit > maxChatInviteListLimit || len(link) > maxChatInviteLinkLength || len(query) > maxChatInviteSearchLength {
		return nil, limitInvalidErr()
	}
	hash := ""
	if strings.TrimSpace(link) != "" {
		hash, err = channelInviteHashFromLink(link)
		if err != nil {
			return nil, err
		}
	}
	offsetUserID := int64(0)
	if !inputUserIsEmpty(req.OffsetUser) {
		ids, err := r.userIDsFromInputUsers(ctx, userID, []tg.InputUserClass{req.OffsetUser})
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			offsetUserID = ids[0]
		}
	}
	list, err := r.deps.Channels.ListInviteImporters(ctx, userID, domain.ChannelInviteImportersRequest{
		UserID:       userID,
		ChannelID:    view.Channel.ID,
		Hash:         hash,
		Requested:    req.Requested,
		Query:        query,
		OffsetDate:   req.OffsetDate,
		OffsetUserID: offsetUserID,
		Limit:        req.Limit,
	})
	if err != nil {
		return nil, channelInviteErr(err)
	}
	importers := make([]tg.ChatInviteImporter, 0, len(list.Importers))
	userIDs := make([]int64, 0, len(list.Importers))
	for _, importer := range list.Importers {
		tgImporter := tg.ChatInviteImporter{
			UserID: importer.UserID,
			Date:   importer.Date,
		}
		if importer.Requested {
			tgImporter.SetRequested(true)
		}
		if importer.ApprovedBy != 0 {
			tgImporter.SetApprovedBy(importer.ApprovedBy)
			userIDs = append(userIDs, importer.ApprovedBy)
		}
		importers = append(importers, tgImporter)
		userIDs = append(userIDs, importer.UserID)
	}
	return &tg.MessagesChatInviteImporters{
		Count:     list.Count,
		Importers: importers,
		Users:     r.tgUsersForIDs(ctx, userID, userIDs),
	}, nil
}

func createChatInviteMemberIDs(ids []int64, selfUserID int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 || id == selfUserID {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (r *Router) tgExportedChannelInvite(invite domain.ChannelInvite) tg.ExportedChatInviteClass {
	return tgExportedChannelInvite(invite, r.cfg.PublicBaseURL)
}

func tgExportedChannelInvite(invite domain.ChannelInvite, publicBaseURL string) tg.ExportedChatInviteClass {
	out := &tg.ChatInviteExported{
		Revoked:       invite.Revoked,
		Permanent:     invite.Permanent,
		RequestNeeded: invite.RequestNeeded,
		Link:          publicLinkWithBaseURL(publicBaseURL, "+"+invite.Hash),
		AdminID:       invite.AdminUserID,
		Date:          invite.Date,
	}
	if invite.Title != "" {
		out.SetTitle(invite.Title)
	}
	if invite.ExpireDate > 0 {
		out.SetExpireDate(invite.ExpireDate)
	}
	if invite.UsageLimit > 0 {
		out.SetUsageLimit(invite.UsageLimit)
	}
	if invite.UsageCount > 0 {
		out.SetUsage(invite.UsageCount)
	}
	if invite.RequestedCount > 0 {
		out.SetRequested(invite.RequestedCount)
	}
	return out
}

func validateChatInviteLink(link string) error {
	link = strings.TrimSpace(link)
	if link == "" {
		return tgerr400("INVITE_HASH_EMPTY")
	}
	if len(link) > maxChatInviteLinkLength {
		return limitInvalidErr()
	}
	return nil
}

func channelInviteHashFromLink(link string) (string, error) {
	if err := validateChatInviteLink(link); err != nil {
		return "", err
	}
	link = strings.TrimSpace(link)
	link = strings.TrimPrefix(link, "tg://join?invite=")
	if strings.Contains(link, "://") {
		if idx := strings.LastIndex(link, "/+"); idx >= 0 {
			link = link[idx+2:]
		} else if idx := strings.LastIndex(link, "/joinchat/"); idx >= 0 {
			link = link[idx+10:]
		} else if idx := strings.LastIndex(link, "/"); idx >= 0 {
			link = link[idx+1:]
		}
	}
	link = strings.TrimPrefix(link, "+")
	link = strings.TrimSpace(link)
	if link == "" {
		return "", tgerr400("INVITE_HASH_EMPTY")
	}
	if len(link) > maxChatInviteLinkLength {
		return "", limitInvalidErr()
	}
	return link, nil
}

func channelInviteErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrInviteHashEmpty):
		return tgerr400("INVITE_HASH_EMPTY")
	case errors.Is(err, domain.ErrInviteHashInvalid):
		return tgerr400("INVITE_HASH_INVALID")
	case errors.Is(err, domain.ErrInviteHashExpired):
		return tgerr.New(406, "INVITE_HASH_EXPIRED")
	case errors.Is(err, domain.ErrInvitePermanent):
		return tgerr400("CHAT_INVITE_PERMANENT")
	case errors.Is(err, domain.ErrInviteRevokedMissing):
		return tgerr400("INVITE_REVOKED_MISSING")
	case errors.Is(err, domain.ErrInviteRequestSent):
		return tgerr400("INVITE_REQUEST_SENT")
	case errors.Is(err, domain.ErrHideRequesterMissing):
		return tgerr400("HIDE_REQUESTER_MISSING")
	case errors.Is(err, domain.ErrUsersTooMuch):
		return tgerr400("USERS_TOO_MUCH")
	case errors.Is(err, domain.ErrUserAlreadyParticipant):
		return tgerr400("USER_ALREADY_PARTICIPANT")
	case errors.Is(err, domain.ErrUserKicked):
		return tgerr400("USER_KICKED")
	case errors.Is(err, domain.ErrBotGroupsBlocked):
		return tgerr400("BOT_GROUPS_BLOCKED")
	default:
		return channelInvalidErr(err)
	}
}
