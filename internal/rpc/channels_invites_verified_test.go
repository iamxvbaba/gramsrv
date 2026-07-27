package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// inviteBadgeChannels answers messages.checkChatInvite from a canned persistent
// channel record, so the test observes exactly the flags the projection derives.
type inviteBadgeChannels struct {
	ChannelsService
	result domain.CheckChannelInviteResult
}

func (s *inviteBadgeChannels) CheckInvite(_ context.Context, _ int64, _ string, _ int) (domain.CheckChannelInviteResult, error) {
	return s.result, nil
}

func checkChatInvitePreview(t *testing.T, ch domain.Channel) *tg.ChatInvite {
	t.Helper()
	r := New(Config{}, Deps{Channels: &inviteBadgeChannels{
		result: domain.CheckChannelInviteResult{
			Channel: ch,
			Invite:  domain.ChannelInvite{Hash: "hash", ChannelID: ch.ID},
		},
	}}, zap.NewNop(), clock.System)
	res, err := r.onMessagesCheckChatInvite(WithUserID(context.Background(), 1001), "hash")
	if err != nil {
		t.Fatalf("check chat invite: %v", err)
	}
	invite, ok := res.(*tg.ChatInvite)
	if !ok {
		t.Fatalf("invite = %T %+v", res, res)
	}
	// Round-trip through the wire so the assertions read the encoded flags word
	// rather than only the Go struct fields: a raw field assignment would pass the
	// struct check and still ship flags.7/8/9 unset.
	buf := &bin.Buffer{}
	if err := invite.Encode(buf); err != nil {
		t.Fatalf("encode chatInvite: %v", err)
	}
	decoded := &tg.ChatInvite{}
	if err := decoded.Decode(buf); err != nil {
		t.Fatalf("decode chatInvite: %v", err)
	}
	return decoded
}

// TestCheckChatInvitePreviewCarriesLayer228BadgeFlags pins chatInvite#5c9d3702
// verified:flags.7 / scam:flags.8 / fake:flags.9 onto the invite preview, so an
// official client shows the badge before the user joins rather than after.
func TestCheckChatInvitePreviewCarriesLayer228BadgeFlags(t *testing.T) {
	if tg.ChatInviteTypeID != 0x5c9d3702 {
		t.Fatalf("chatInvite constructor id = %#x", tg.ChatInviteTypeID)
	}

	verified := checkChatInvitePreview(t, domain.Channel{
		ID: 4004, AccessHash: 44, Title: "Official", Username: "official",
		Broadcast: true, ParticipantsCount: 7, Verified: true,
	})
	if !verified.GetVerified() || !verified.Verified {
		t.Fatalf("verified invite = %+v", verified)
	}
	if verified.GetScam() || verified.GetFake() {
		t.Fatalf("verified invite leaked moderation flags: %+v", verified)
	}
	if !verified.Channel || !verified.Broadcast || !verified.Public ||
		verified.Title != "Official" || verified.ParticipantsCount != 7 {
		t.Fatalf("verified invite lost unrelated fields: %+v", verified)
	}

	flagged := checkChatInvitePreview(t, domain.Channel{
		ID: 4005, AccessHash: 45, Title: "Flagged", Megagroup: true,
		Scam: true, Fake: true,
	})
	if !flagged.GetScam() || !flagged.GetFake() {
		t.Fatalf("flagged invite = %+v", flagged)
	}
	if flagged.GetVerified() {
		t.Fatalf("flagged invite claims verification: %+v", flagged)
	}
	if !flagged.Megagroup || flagged.Public {
		t.Fatalf("flagged invite lost unrelated fields: %+v", flagged)
	}
}

// TestCheckChatInvitePreviewLeavesUnflaggedChannelClean is the negative half: an
// ordinary channel must not carry flags.7/8/9 at all, so the encoded preview stays
// byte-identical to what an official server sends for an unflagged peer.
func TestCheckChatInvitePreviewLeavesUnflaggedChannelClean(t *testing.T) {
	plain := checkChatInvitePreview(t, domain.Channel{
		ID: 4006, AccessHash: 46, Title: "Plain", Broadcast: true,
	})
	if plain.GetVerified() || plain.Verified {
		t.Fatalf("unverified channel exposed verified: %+v", plain)
	}
	if plain.GetScam() || plain.GetFake() || plain.Scam || plain.Fake {
		t.Fatalf("unflagged channel exposed moderation flags: %+v", plain)
	}
	if plain.Flags.Has(7) || plain.Flags.Has(8) || plain.Flags.Has(9) {
		t.Fatalf("unflagged channel set badge bits in flags word: %d", plain.Flags)
	}
}

// TestCheckChatInviteAlreadyKeepsChannelBadge guards the other branch: a member
// gets chatInviteAlready#5a686d7c, whose Chat already carries
// channel#d49f34c6 verified:flags.7 through tgChannelChat, so the flags must not be
// applied twice or lost.
func TestCheckChatInviteAlreadyKeepsChannelBadge(t *testing.T) {
	const channelID = int64(4007)
	r := New(Config{}, Deps{Channels: &inviteBadgeChannels{
		result: domain.CheckChannelInviteResult{
			Channel: domain.Channel{
				ID: channelID, AccessHash: 47, Title: "Official", Username: "official",
				Broadcast: true, Verified: true,
			},
			Invite:  domain.ChannelInvite{Hash: "hash", ChannelID: channelID},
			Already: true,
			Self: domain.ChannelMember{
				ChannelID: channelID, UserID: 1001, Status: domain.ChannelMemberActive,
			},
		},
	}}, zap.NewNop(), clock.System)
	res, err := r.onMessagesCheckChatInvite(WithUserID(context.Background(), 1001), "hash")
	if err != nil {
		t.Fatalf("check chat invite: %v", err)
	}
	already, ok := res.(*tg.ChatInviteAlready)
	if !ok {
		t.Fatalf("invite = %T %+v", res, res)
	}
	channel, ok := already.Chat.(*tg.Channel)
	if !ok || channel.ID != channelID {
		t.Fatalf("chat = %T %+v", already.Chat, already.Chat)
	}
	if !channel.Verified || channel.Scam || channel.Fake {
		t.Fatalf("already-member channel badge = %+v", channel)
	}
}
