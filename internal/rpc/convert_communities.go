package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func tgCommunityPhoto(c domain.Community) tg.ChatPhotoClass {
	if c.PhotoID == 0 {
		return &tg.ChatPhotoEmpty{}
	}
	out := &tg.ChatPhoto{PhotoID: c.PhotoID, DCID: c.PhotoDCID}
	if len(c.PhotoStripped) > 0 {
		out.SetStrippedThumb(c.PhotoStripped)
	}
	return out
}

func tgCommunityFullPhoto(c domain.Community) tg.PhotoClass {
	if c.PhotoID == 0 {
		return &tg.PhotoEmpty{}
	}
	sizes := syntheticAvatarSizes()
	if len(c.PhotoStripped) > 0 {
		sizes = append([]tg.PhotoSizeClass{&tg.PhotoStrippedSize{Type: "i", Bytes: c.PhotoStripped}}, sizes...)
	}
	return &tg.Photo{ID: c.PhotoID, DCID: c.PhotoDCID, Sizes: sizes}
}

func tgCommunityChat(view domain.CommunityView) tg.ChatClass {
	c := view.Community
	if c.Deleted || view.Forbidden {
		return &tg.CommunityForbidden{ID: c.ID, AccessHash: c.AccessHash, Title: c.Title}
	}
	out := &tg.Community{Creator: view.Creator(), CollapsedInDialogs: view.State.Collapsed, ID: c.ID, Title: c.Title, Photo: tgCommunityPhoto(c), Date: c.Date}
	out.SetAccessHash(c.AccessHash)
	if view.Self.Role == domain.CommunityRoleCreator {
		out.SetAdminRights(tgChatAdminRights(domain.CreatorChannelAdminRights()))
	} else if view.Self.Role == domain.CommunityRoleAdmin {
		out.SetAdminRights(tgChatAdminRights(view.Self.AdminRights))
	}
	out.SetDefaultBannedRights(tgDefaultChatBannedRights(c.DefaultBannedRights))
	return out
}

func tgCommunityChats(views []domain.CommunityView) []tg.ChatClass {
	out := make([]tg.ChatClass, 0, len(views))
	for _, v := range views {
		out = append(out, tgCommunityChat(v))
	}
	return out
}

func tgCommunityPeer(link domain.CommunityPeerLink) tg.CommunityPeer {
	out := tg.CommunityPeer{CanViewHistory: link.CanViewHistory, Peer: tgPeer(link.Peer)}
	out.SetVisible(link.Visible())
	return out
}

func tgCommunityFull(view domain.CommunityView) *tg.CommunityFull {
	links := make([]tg.CommunityPeer, 0, len(view.Links))
	for _, l := range view.Links {
		links = append(links, tgCommunityPeer(l))
	}
	out := &tg.CommunityFull{ID: view.Community.ID, About: view.Community.About, ChatPhoto: tgCommunityFullPhoto(view.Community), LinkedPeers: links}
	if view.AdminsCount > 0 {
		out.SetAdminsCount(view.AdminsCount)
	}
	if view.KickedCount > 0 {
		out.SetKickedCount(view.KickedCount)
	}
	if view.PendingRequests > 0 {
		out.SetPeerLinkRequestsPending(view.PendingRequests)
	}
	return out
}

func tgCommunityHydratedChats(viewerUserID int64, view domain.CommunityView) []tg.ChatClass {
	out := []tg.ChatClass{tgCommunityChat(view)}
	for _, ch := range view.Channels {
		out = appendUniqueTGChats(out, tgChannelChatMin(viewerUserID, ch))
	}
	return out
}

func tgCommunityMember(viewerUserID int64, m domain.CommunityMember) tg.ChannelParticipantClass {
	cm := domain.ChannelMember{ChannelID: m.CommunityID, UserID: m.UserID, Status: domain.ChannelMemberActive, Role: domain.ChannelRoleMember, AdminRights: m.AdminRights, Rank: m.Rank, JoinedAt: m.Date}
	if m.Status == domain.CommunityMemberKicked {
		cm.Status = domain.ChannelMemberKicked
		cm.BannedRights = domain.ChannelBannedRights{ViewMessages: true}
	}
	switch m.Role {
	case domain.CommunityRoleCreator:
		cm.Role = domain.ChannelRoleCreator
	case domain.CommunityRoleAdmin:
		cm.Role = domain.ChannelRoleAdmin
	}
	return tgChannelParticipant(viewerUserID, cm)
}

func tgCommunityDialog(view domain.CommunityView, notify *domain.PeerNotifySettings) *tg.DialogCommunity {
	return &tg.DialogCommunity{Pinned: view.State.Pinned, CommunityID: view.Community.ID, NotifySettings: *tgPeerNotifySettings(notify)}
}
