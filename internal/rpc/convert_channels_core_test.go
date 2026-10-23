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
	if !invite.Permanent || invite.Revoked || invite.AdminID != 10 || invite.Link != "https://sgq.me/+abc123" {
		t.Fatalf("channelFull.exported_invite = %#v, want active permanent invite", invite)
	}
}

func TestChannelFullStatsCapabilityRequiresEligibleViewerAndExactDC(t *testing.T) {
	tests := []struct {
		name      string
		dc        int
		monoforum bool
		role      domain.ChannelMemberRole
		want      bool
	}{
		{name: "creator", dc: 2, role: domain.ChannelRoleCreator, want: true},
		{name: "ordinary member", dc: 2, role: domain.ChannelRoleMember},
		{name: "monoforum creator", dc: 2, monoforum: true, role: domain.ChannelRoleCreator},
		{name: "invalid canonical dc", role: domain.ChannelRoleCreator},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := domain.ChannelView{
				Channel: domain.Channel{ID: 1003, Monoforum: tc.monoforum},
				Self: domain.ChannelMember{
					ChannelID: 1003,
					UserID:    10,
					Status:    domain.ChannelMemberActive,
					Role:      tc.role,
				},
			}
			full := tgChannelFull(view)
			(&Router{cfg: Config{DC: tc.dc}}).applyChannelStatsCapability(full)
			statsDC, ok := full.GetStatsDC()
			if tc.want {
				if !full.CanViewStats || !ok || statsDC != tc.dc {
					t.Fatalf("can_view_stats=%v stats_dc=(%d,%v), want true and (%d,true)", full.CanViewStats, statsDC, ok, tc.dc)
				}
				return
			}
			if full.CanViewStats || ok {
				t.Fatalf("can_view_stats=%v stats_dc=(%d,%v), want false and absent", full.CanViewStats, statsDC, ok)
			}
		})
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
