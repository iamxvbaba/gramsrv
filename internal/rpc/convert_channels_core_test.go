package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestTGChannelHasLinkFromInviteProjection(t *testing.T) {
	channel := domain.Channel{
		ID:         1001,
		AccessHash: 42,
		Title:      "private group with link",
		Megagroup:  true,
		HasLink:    true,
		Date:       1700000100,
	}
	self := &domain.ChannelMember{
		ChannelID: channel.ID,
		UserID:    10,
		Status:    domain.ChannelMemberActive,
		Role:      domain.ChannelRoleCreator,
	}
	got := tgChannel(10, channel, self)
	if !got.GetHasLink() {
		t.Fatalf("tgChannel.has_link = false, want true for linked private megagroup")
	}
}

func TestTGChannelFullIncludesExportedInvite(t *testing.T) {
	view := domain.ChannelView{
		Channel: domain.Channel{
			ID:         1002,
			AccessHash: 43,
			Title:      "private group",
			Megagroup:  true,
			Date:       1700000100,
		},
		Self: domain.ChannelMember{
			ChannelID: 1002,
			UserID:    10,
			Status:    domain.ChannelMemberActive,
			Role:      domain.ChannelRoleCreator,
		},
		ExportedInvite: &domain.ChannelInvite{
			ChannelID:   1002,
			InviteID:    77,
			Hash:        "abc123",
			AdminUserID: 10,
			Permanent:   true,
			Date:        1700000111,
		},
	}

	full := tgChannelFull(view)
	rawInvite, ok := full.GetExportedInvite()
	if !ok {
		t.Fatalf("channelFull.exported_invite missing")
	}
	invite, ok := rawInvite.(*tg.ChatInviteExported)
	if !ok {
		t.Fatalf("channelFull.exported_invite = %T, want *tg.ChatInviteExported", rawInvite)
	}
	if !invite.Permanent || invite.Revoked || invite.AdminID != 10 || invite.Link != "https://telesrv.net/+abc123" {
		t.Fatalf("channelFull.exported_invite = %#v, want active permanent invite", invite)
	}
}

func TestChannelBannedRightsRoundTripModernFields(t *testing.T) {
	in := tg.ChatBannedRights{
		ViewMessages:    true,
		SendMessages:    true,
		SendMedia:       true,
		SendStickers:    true,
		SendGifs:        true,
		SendGames:       true,
		SendInline:      true,
		EmbedLinks:      true,
		SendPolls:       true,
		ChangeInfo:      true,
		InviteUsers:     true,
		PinMessages:     true,
		ManageTopics:    true,
		SendPhotos:      true,
		SendVideos:      true,
		SendRoundvideos: true,
		SendAudios:      true,
		SendVoices:      true,
		SendDocs:        true,
		SendPlain:       true,
		EditRank:        true,
		SendReactions:   true,
		UntilDate:       12345,
	}
	domainRights := domainChannelBannedRights(in)
	out := tgChatBannedRights(domainRights)

	if out != in {
		t.Fatalf("banned rights round-trip = %+v, want %+v", out, in)
	}
}
