package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func TestPeerResponseRetainsBotOwnershipAndVerificationAcrossEnvelopes(t *testing.T) {
	f := newBotVerificationFixture(t, &fakeBotVerifications{marks: map[domain.Peer]domain.CustomVerification{}})
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}
	f.verify.marks[peer] = domain.CustomVerification{Peer: peer, VerifierBotID: f.bot.ID, IconDocumentID: 9900765}
	for _, viewer := range []int64{f.owner.ID, f.stranger.ID, f.owner.ID} {
		for _, kind := range []string{"users", "messages", "updates"} {
			u := tgUser(f.bot)
			var box any
			switch kind {
			case "users":
				box = []tg.UserClass{u}
			case "messages":
				box = &tg.MessagesMessages{Users: []tg.UserClass{u}}
			default:
				box = &tg.Updates{Users: []tg.UserClass{u}}
			}
			f.router.finalizePeerResponse(context.Background(), viewer, box)
			wire := &tg.User{}
			tlRoundTrip(t, u, wire)
			if wire.GetBotCanEdit() != (viewer == f.owner.ID) {
				t.Fatalf("%s viewer %d: bot_can_edit=%v", kind, viewer, wire.GetBotCanEdit())
			}
			if icon, set := wire.GetBotVerificationIcon(); !set || icon != 9900765 {
				t.Fatalf("%s: verification=%d present=%v", kind, icon, set)
			}
		}
	}
}

func TestChannelWithoutMembershipCannotOverwriteAdminState(t *testing.T) {
	ch := domain.Channel{ID: 123, AccessHash: 456, Title: "Channel", CreatorUserID: 99, Broadcast: true}
	for _, viewer := range []int64{99, 100} {
		projected := tgChannel(viewer, ch, nil)
		wire := &tg.Channel{}
		tlRoundTrip(t, projected, wire)
		if !wire.Min {
			t.Fatal("channel without viewer membership was sent as a full peer")
		}
	}
	self := domain.ChannelMember{ChannelID: ch.ID, UserID: 100, Role: domain.ChannelRoleAdmin, AdminRights: domain.ChannelAdminRights{ChangeInfo: true}}
	full := tgChannel(100, ch, &self)
	wire := &tg.Channel{}
	tlRoundTrip(t, full, wire)
	if wire.Min {
		t.Fatal("known membership was downgraded to min")
	}
	rights, set := wire.GetAdminRights()
	if !set || !rights.ChangeInfo {
		t.Fatal("administrator rights were lost")
	}
}

func TestPeerResponseDecoratesSupplementalChannelVerification(t *testing.T) {
	f := newBotVerificationFixture(t, newFakeBotVerifications())
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 12345}
	f.verify.marks[peer] = domain.CustomVerification{Peer: peer, VerifierBotID: f.bot.ID, IconDocumentID: 9900766}
	ch := tgChannel(f.owner.ID, domain.Channel{ID: peer.ID, AccessHash: 123, Title: "Marked channel", Broadcast: true}, nil)
	box := &tg.Updates{Chats: []tg.ChatClass{ch}}
	f.router.finalizePeerResponse(context.Background(), f.owner.ID, box)
	wire := &tg.Channel{}
	tlRoundTrip(t, ch, wire)
	if icon, set := wire.GetBotVerificationIcon(); !set || icon != 9900766 {
		t.Fatalf("channel mark=%d present=%v", icon, set)
	}
	if !wire.Min {
		t.Fatal("identity decoration promoted an incomplete channel to full")
	}
}
