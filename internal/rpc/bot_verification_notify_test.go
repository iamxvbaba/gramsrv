package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// TestNotifyPeerBotVerificationUserInvalidatesAndPushesUpdateUser is the bot/user
// half: a newly marked peer must reach every online account that already sees it,
// through the same non-PTS updateUser shape the scam/fake flags use, and the pushed
// tg.User must already carry bot_verification_icon:flags2.14.
func TestNotifyPeerBotVerificationUserInvalidatesAndPushesUpdateUser(t *testing.T) {
	const (
		shopID          = int64(2200000022)
		onlineViewerID  = int64(1001)
		offlineViewerID = int64(3003)
		icon            = int64(9900001)
	)
	users := &verifiedNotifyUsers{
		user:     domain.User{ID: shopID, FirstName: "Shop", AccessHash: 22},
		found:    true,
		audience: []int64{shopID, onlineViewerID, offlineViewerID},
	}
	verify := newFakeBotVerifications()
	verify.marks[domain.Peer{Type: domain.PeerTypeUser, ID: shopID}] = domain.CustomVerification{
		VerifierBotID: 777000123, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: shopID},
		IconDocumentID: icon, Description: "Verified by Acme Trust",
	}
	sessions := &captureSessions{onlineUserIDs: []int64{shopID, onlineViewerID}}
	r := New(Config{}, Deps{Users: users, Sessions: sessions, BotVerifications: verify}, zap.NewNop(), clock.System)
	seedUserFullProjection(t, r, onlineViewerID, shopID)
	seedUserFullProjection(t, r, shopID, shopID)

	if err := r.NotifyPeerBotVerification(context.Background(), domain.Peer{
		Type: domain.PeerTypeUser, ID: shopID,
	}); err != nil {
		t.Fatalf("notify peer bot verification: %v", err)
	}

	if _, ok := r.userFullProjectionCache.Lookup(onlineViewerID, shopID); ok {
		t.Fatal("viewer userFull projection survived the mark change")
	}
	if _, ok := r.userFullProjectionCache.Lookup(shopID, shopID); ok {
		t.Fatal("target userFull projection survived the mark change")
	}
	if users.adminCalls != 1 {
		t.Fatalf("authoritative account reads = %d, want 1", users.adminCalls)
	}
	// One read for the whole audience: the mark is a peer-wide fact, so the
	// per-recipient push builder must not query it again for every recipient.
	if verify.peerCalls != 1 || verify.batchCalls != 0 {
		t.Fatalf("verification reads = peer %d / batch %d, want peer 1 / batch 0",
			verify.peerCalls, verify.batchCalls)
	}

	// Offline audience members are skipped; they converge through the bumped peer
	// read model on their next getDifference / getUsers.
	pushed := sessions.pushedUserIDs()
	if len(pushed) != 2 || pushed[0] != shopID || pushed[1] != onlineViewerID {
		t.Fatalf("pushed user ids = %v", pushed)
	}

	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("updates = %T %+v", sessions.lastUserPush(), sessions.lastUserPush())
	}
	refresh, ok := updates.Updates[0].(*tg.UpdateUser)
	if !ok || refresh.UserID != shopID {
		t.Fatalf("refresh = %T %+v", updates.Updates[0], updates.Updates[0])
	}
	if len(updates.Users) != 1 {
		t.Fatalf("users = %+v", updates.Users)
	}
	pushedUser := &tg.User{}
	tlRoundTrip(t, updates.Users[0].(*tg.User), pushedUser)
	got, ok := pushedUser.GetBotVerificationIcon()
	if !ok || got != icon {
		t.Fatalf("pushed user bot_verification_icon = %d ok=%v, want %d on flags2.14", got, ok, icon)
	}
	if !pushedUser.Flags2.Has(14) {
		t.Fatalf("pushed user flags2 = %032b, want bit 14", uint32(pushedUser.Flags2))
	}
	// The official checkmark is a separate mechanism and must not be implied.
	if pushedUser.Verified || pushedUser.Scam || pushedUser.Fake {
		t.Fatalf("mark push leaked moderation flags: %+v", pushedUser)
	}
}

// TestNotifyPeerBotVerificationChannelInvalidatesAndPushesUpdateChannel is the
// channel half: the mark rides the existing channel state-mutation fan-out, and the
// pushed tg.Channel carries bot_verification_icon:flags2.13.
func TestNotifyPeerBotVerificationChannelInvalidatesAndPushesUpdateChannel(t *testing.T) {
	const (
		channelID = int64(4404)
		ownerID   = int64(3003)
		memberID  = int64(3004)
		icon      = int64(9900002)
	)
	channels := &verifiedNotifyChannels{channel: domain.Channel{
		ID: channelID, AccessHash: 44, CreatorUserID: ownerID,
		Title: "Partner", Username: "partner", Broadcast: true,
	}}
	verify := newFakeBotVerifications()
	verify.marks[domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}] = domain.CustomVerification{
		VerifierBotID: 777000123, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		IconDocumentID: icon, Description: "Verified by Acme Trust",
	}
	sessions := &captureSessions{
		onlineUserIDs:  []int64{ownerID, memberID},
		channelMembers: map[int64][]int64{channelID: {ownerID, memberID}},
	}
	r := New(Config{}, Deps{Channels: channels, Sessions: sessions, BotVerifications: verify}, zap.NewNop(), clock.System)
	seedChannelFullProjection(t, r, ownerID, channelID)
	seedChannelFullProjection(t, r, memberID, channelID)

	if err := r.NotifyPeerBotVerification(context.Background(), domain.Peer{
		Type: domain.PeerTypeChannel, ID: channelID,
	}); err != nil {
		t.Fatalf("notify peer bot verification: %v", err)
	}

	if _, ok := r.channelFullProjectionCache.Lookup(ownerID, channelID); ok {
		t.Fatal("owner channelFull projection survived the mark change")
	}
	if _, ok := r.channelFullProjectionCache.Lookup(memberID, channelID); ok {
		t.Fatal("member channelFull projection survived the mark change")
	}
	if channels.calls != 1 {
		t.Fatalf("base channel row reads = %d, want 1", channels.calls)
	}
	// Same contract on the channel fan-out: one read for every recipient plus the
	// returned updates.
	if verify.peerCalls != 1 || verify.batchCalls != 0 {
		t.Fatalf("verification reads = peer %d / batch %d, want peer 1 / batch 0",
			verify.peerCalls, verify.batchCalls)
	}

	if pushed := sessions.pushedUserIDs(); len(pushed) != 2 {
		t.Fatalf("pushed user ids = %v", pushed)
	}
	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("updates = %T %+v", sessions.lastUserPush(), sessions.lastUserPush())
	}
	refresh, ok := updates.Updates[0].(*tg.UpdateChannel)
	if !ok || refresh.ChannelID != channelID {
		t.Fatalf("refresh = %T %+v", updates.Updates[0], updates.Updates[0])
	}
	if len(updates.Chats) != 1 {
		t.Fatalf("chats = %+v", updates.Chats)
	}
	pushedChannel := &tg.Channel{}
	tlRoundTrip(t, updates.Chats[0].(*tg.Channel), pushedChannel)
	got, ok := pushedChannel.GetBotVerificationIcon()
	if !ok || got != icon {
		t.Fatalf("pushed channel bot_verification_icon = %d ok=%v, want %d on flags2.13", got, ok, icon)
	}
	if !pushedChannel.Flags2.Has(13) {
		t.Fatalf("pushed channel flags2 = %032b, want bit 13", uint32(pushedChannel.Flags2))
	}
	if pushedChannel.Verified || pushedChannel.Scam || pushedChannel.Fake {
		t.Fatalf("mark push leaked moderation flags: %+v", pushedChannel)
	}
}

// TestNotifyPeerBotVerificationDropsChannelBotInfoCache guards the one cache the
// user path does not have: channelFull.bot_info bakes botInfo.verifier_settings, so a
// verifier's own status change has to drop it or the profile keeps advertising the
// old state for the cache TTL.
func TestNotifyPeerBotVerificationDropsChannelBotInfoCache(t *testing.T) {
	const channelID = int64(4405)
	channels := &verifiedNotifyChannels{channel: domain.Channel{
		ID: channelID, AccessHash: 45, CreatorUserID: 3003, Title: "Partner", Broadcast: true,
	}}
	r := New(Config{}, Deps{Channels: channels, Sessions: &captureSessions{}}, zap.NewNop(), clock.System)
	epoch := r.channelFullBotCache.LoadEpoch()
	r.channelFullBotCache.StoreIfEpoch(3003, channelID, channelFullBotInfoResult{
		userIDs:  []int64{777000123},
		botInfos: []tg.BotInfo{{}},
	}, epoch)
	if _, ok := r.channelFullBotCache.Lookup(3003, channelID); !ok {
		t.Fatal("seed channelFull bot info cache")
	}

	if err := r.NotifyPeerBotVerification(context.Background(), domain.Peer{
		Type: domain.PeerTypeChannel, ID: channelID,
	}); err != nil {
		t.Fatalf("notify peer bot verification: %v", err)
	}
	if _, ok := r.channelFullBotCache.Lookup(3003, channelID); ok {
		t.Fatal("channelFull bot info cache survived the mark change")
	}
}

// TestNotifyPeerBotVerificationNilRouterIsSafe pins the nil-receiver contract: the
// hook runs after the change already committed, so it may never panic.
func TestNotifyPeerBotVerificationNilRouterIsSafe(t *testing.T) {
	var r *Router
	if err := r.NotifyPeerBotVerification(context.Background(), domain.Peer{
		Type: domain.PeerTypeUser, ID: 1001,
	}); err != nil {
		t.Fatalf("nil router notify = %v", err)
	}
}

// TestNotifyPeerBotVerificationWithoutServicesStillInvalidates covers degraded
// wiring: with no user/channel service the hook cannot push, but the stale
// projection must still be dropped and no error reported.
func TestNotifyPeerBotVerificationWithoutServicesStillInvalidates(t *testing.T) {
	r := New(Config{}, Deps{}, zap.NewNop(), clock.System)
	seedUserFullProjection(t, r, 1001, 2002)
	seedChannelFullProjection(t, r, 1001, 4004)

	if err := r.NotifyPeerBotVerification(context.Background(), domain.Peer{
		Type: domain.PeerTypeUser, ID: 2002,
	}); err != nil {
		t.Fatalf("notify user without users service = %v", err)
	}
	if err := r.NotifyPeerBotVerification(context.Background(), domain.Peer{
		Type: domain.PeerTypeChannel, ID: 4004,
	}); err != nil {
		t.Fatalf("notify channel without channels service = %v", err)
	}
	if _, ok := r.userFullProjectionCache.Lookup(1001, 2002); ok {
		t.Fatal("userFull projection survived without a users service")
	}
	if _, ok := r.channelFullProjectionCache.Lookup(1001, 4004); ok {
		t.Fatal("channelFull projection survived without a channels service")
	}
}

// TestNotifyPeerBotVerificationRejectsUnknownPeers pins the "explain, never panic"
// contract for every peer the hook cannot act on.
func TestNotifyPeerBotVerificationRejectsUnknownPeers(t *testing.T) {
	users := &verifiedNotifyUsers{}
	channels := &verifiedNotifyChannels{}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Users: users, Channels: channels, Sessions: sessions}, zap.NewNop(), clock.System)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		peer domain.Peer
		want string
	}{
		{"zero id", domain.Peer{Type: domain.PeerTypeUser}, "invalid peer id"},
		{"negative id", domain.Peer{Type: domain.PeerTypeChannel, ID: -1}, "invalid peer id"},
		{"community peer", domain.Peer{Type: domain.PeerTypeCommunity, ID: 5005}, "unsupported peer type"},
		{"empty type", domain.Peer{ID: 5005}, "unsupported peer type"},
		{"missing user", domain.Peer{Type: domain.PeerTypeUser, ID: 2002}, "user 2002 not found"},
		{"missing channel", domain.Peer{Type: domain.PeerTypeChannel, ID: 4004}, "channel 4004 not found"},
	} {
		err := r.NotifyPeerBotVerification(ctx, tc.peer)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want mention of %q", tc.name, err, tc.want)
		}
		if err != nil && !strings.Contains(err.Error(), "notify peer bot verification") {
			t.Fatalf("%s: err = %v, want the bot-verification hook named", tc.name, err)
		}
	}
	if pushed := sessions.pushedUserIDs(); len(pushed) != 0 {
		t.Fatalf("unresolved peers pushed updates to %v", pushed)
	}
}

// TestNotifyPeerBotVerificationReportsLookupFailures keeps a directory error distinct
// from "peer not found", and keeps a channels adapter without the base-row reader
// from failing silently.
func TestNotifyPeerBotVerificationReportsLookupFailures(t *testing.T) {
	ctx := context.Background()
	loadErr := errors.New("boom")

	failing := New(Config{}, Deps{
		Channels: &verifiedNotifyChannels{err: loadErr},
		Sessions: &captureSessions{},
	}, zap.NewNop(), clock.System)
	if err := failing.NotifyPeerBotVerification(ctx, domain.Peer{
		Type: domain.PeerTypeChannel, ID: 4004,
	}); !errors.Is(err, loadErr) {
		t.Fatalf("channel load error = %v", err)
	}

	unwired := New(Config{}, Deps{
		Channels: channelsWithoutDirectory{},
		Sessions: &captureSessions{},
	}, zap.NewNop(), clock.System)
	err := unwired.NotifyPeerBotVerification(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: 4004})
	if err == nil || !strings.Contains(err.Error(), "GetChannelByID") {
		t.Fatalf("missing channel directory error = %v", err)
	}
}

// TestSetCustomVerificationNotifiesAudience closes the loop: a successful
// bots.setCustomVerification must itself drop the projections and push the refresh,
// so the badge appears in a running client without a second RPC.
func TestSetCustomVerificationNotifiesAudience(t *testing.T) {
	fake := newFakeBotVerifications()
	f := newBotVerificationFixture(t, fake)
	// Production wiring: the service holds the router as its PeerNotifier, so the
	// push happens once, in the service, for every driver of a mark change.
	fake.notifier = f.router
	f.enableVerifier(f.bot.ID, 9900003, true)
	sessions := &captureSessions{onlineUserIDs: []int64{f.owner.ID, f.target.ID}}
	f.router.deps.Sessions = sessions
	seedUserFullProjection(t, f.router, f.owner.ID, f.target.ID)

	ok, err := f.router.onBotsSetCustomVerification(WithUserID(context.Background(), f.owner.ID),
		setCustomVerificationRequest(inputPeerUser(f.target), inputUser(f.bot), true, ""))
	if err != nil || !ok {
		t.Fatalf("grant = %v,%v, want true,nil", ok, err)
	}
	if _, cached := f.router.userFullProjectionCache.Lookup(f.owner.ID, f.target.ID); cached {
		t.Fatal("userFull projection survived the grant")
	}
	if pushed := sessions.pushedUserIDs(); len(pushed) == 0 {
		t.Fatal("grant pushed no refresh update")
	}
	updates, isUpdates := sessions.lastUserPush().(*tg.Updates)
	if !isUpdates || len(updates.Users) == 0 {
		t.Fatalf("push = %T %+v", sessions.lastUserPush(), sessions.lastUserPush())
	}
	pushedUser := &tg.User{}
	tlRoundTrip(t, updates.Users[0].(*tg.User), pushedUser)
	if icon, set := pushedUser.GetBotVerificationIcon(); !set || icon != 9900003 {
		t.Fatalf("pushed user icon = %d set=%v, want 9900003 on flags2.14", icon, set)
	}
}
