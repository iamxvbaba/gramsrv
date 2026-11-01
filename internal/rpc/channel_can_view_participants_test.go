package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// TestChannelFullCanViewParticipants locks in: a broadcast channel's subscriber list is visible
// only to admins (official semantics), so a non-admin subscriber gets can_view_participants=false
// -- otherwise DrKLO's Profile would show the Subscribers/Administrators/Channel Settings rows.
// Supergroup members can see it (when members are hidden, only admins).
func TestChannelFullCanViewParticipants(t *testing.T) {
	broadcast := domain.Channel{ID: 1, Broadcast: true}
	megagroup := domain.Channel{ID: 2, Megagroup: true}
	megagroupHidden := domain.Channel{ID: 3, Megagroup: true, ParticipantsHidden: true}

	cases := []struct {
		name string
		ch   domain.Channel
		role domain.ChannelMemberRole
		want bool
	}{
		{"broadcast-subscriber", broadcast, domain.ChannelRoleMember, false},
		{"broadcast-admin", broadcast, domain.ChannelRoleAdmin, true},
		{"broadcast-creator", broadcast, domain.ChannelRoleCreator, true},
		{"megagroup-member", megagroup, domain.ChannelRoleMember, true},
		{"megagroup-admin", megagroup, domain.ChannelRoleAdmin, true},
		{"megagroup-hidden-member", megagroupHidden, domain.ChannelRoleMember, false},
		{"megagroup-hidden-admin", megagroupHidden, domain.ChannelRoleAdmin, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := domain.ChannelView{
				Channel: tc.ch,
				Self: domain.ChannelMember{
					ChannelID: tc.ch.ID,
					Role:      tc.role,
					Status:    domain.ChannelMemberActive,
				},
			}
			if got := tgChannelFull(view).CanViewParticipants; got != tc.want {
				t.Fatalf("CanViewParticipants = %v, want %v", got, tc.want)
			}
		})
	}
}
