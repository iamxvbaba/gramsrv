package rpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	verificationapp "telesrv/internal/app/verification"
	"telesrv/internal/domain"
)

// The process wires the badge hook through a dynamic type assertion on
// *rpc.Router, so a signature drift would silently fall back to the
// invalidation-only notifier instead of breaking the build. Assert the port here
// so it breaks the build instead.
var _ verificationapp.PeerNotifier = (*Router)(nil)

// verifiedNotifyUsers is the authoritative account reader plus the moderation
// audience port, i.e. exactly the two capabilities the badge push relies on.
type verifiedNotifyUsers struct {
	UsersService
	user       domain.User
	found      bool
	adminCalls int
	audience   []int64
	viewers    []int64
}

func (s *verifiedNotifyUsers) AdminUser(_ context.Context, userID int64) (domain.User, bool, error) {
	s.adminCalls++
	if !s.found || s.user.ID != userID {
		return domain.User{}, false, nil
	}
	return s.user, true, nil
}

func (s *verifiedNotifyUsers) ByIDs(_ context.Context, viewerUserID int64, ids []int64) ([]domain.User, error) {
	s.viewers = append(s.viewers, viewerUserID)
	if !s.found || len(ids) == 0 {
		return nil, nil
	}
	return []domain.User{s.user}, nil
}

func (s *verifiedNotifyUsers) ModerationFlagAudience(_ context.Context, _ int64, _ int) ([]int64, error) {
	return append([]int64(nil), s.audience...), nil
}

// verifiedNotifyChannels exposes the viewer-independent base row the hook needs
// plus the membership filter the channel fan-out uses.
type verifiedNotifyChannels struct {
	ChannelsService
	channel domain.Channel
	err     error
	calls   int
}

func (s *verifiedNotifyChannels) GetChannelByID(_ context.Context, channelID int64) (domain.Channel, error) {
	s.calls++
	if s.err != nil {
		return domain.Channel{}, s.err
	}
	if s.channel.ID != channelID {
		return domain.Channel{}, nil
	}
	return s.channel, nil
}

func (s *verifiedNotifyChannels) FilterActiveMemberIDs(_ context.Context, _ int64, userIDs []int64) ([]int64, error) {
	return append([]int64(nil), userIDs...), nil
}

// channelsWithoutDirectory models a channels adapter that does not expose the
// non-personalized base-row reader.
type channelsWithoutDirectory struct{ ChannelsService }

func seedUserFullProjection(t *testing.T, r *Router, viewerUserID, targetUserID int64) {
	t.Helper()
	epoch := r.userFullProjectionCache.LoadEpoch()
	r.userFullProjectionCache.StoreIfEpoch(viewerUserID, targetUserID, tg.UserFull{ID: targetUserID}, epoch)
	if _, ok := r.userFullProjectionCache.Lookup(viewerUserID, targetUserID); !ok {
		t.Fatalf("seed userFull projection for viewer %d target %d", viewerUserID, targetUserID)
	}
}

func seedChannelFullProjection(t *testing.T, r *Router, viewerUserID, channelID int64) {
	t.Helper()
	epoch := r.channelFullProjectionCache.LoadEpoch()
	r.channelFullProjectionCache.StoreIfEpoch(viewerUserID, channelID, channelFullProjection{
		accessHash: 1,
		full:       tg.ChannelFull{ID: channelID},
	}, epoch)
	if _, ok := r.channelFullProjectionCache.Lookup(viewerUserID, channelID); !ok {
		t.Fatalf("seed channelFull projection for viewer %d channel %d", viewerUserID, channelID)
	}
}

// TestNotifyPeerVerifiedUserInvalidatesAndPushesUpdateUser is the bot/user half of
// the main scenario: an approved bot must reach every online account that already
// sees it, through the same non-PTS updateUser shape the scam/fake flags use.
func TestNotifyPeerVerifiedUserInvalidatesAndPushesUpdateUser(t *testing.T) {
	const (
		botID           = int64(1250000012)
		onlineViewerID  = int64(1001)
		offlineViewerID = int64(3003)
	)
	users := &verifiedNotifyUsers{
		user:     domain.User{ID: botID, FirstName: "Shop", Bot: true, Verified: true},
		found:    true,
		audience: []int64{botID, onlineViewerID, offlineViewerID},
	}
	sessions := &captureSessions{onlineUserIDs: []int64{botID, onlineViewerID}}
	r := New(Config{}, Deps{Users: users, Sessions: sessions}, zap.NewNop(), clock.System)
	seedUserFullProjection(t, r, onlineViewerID, botID)
	seedUserFullProjection(t, r, botID, botID)

	if err := r.NotifyPeerVerified(context.Background(), domain.Peer{
		Type: domain.PeerTypeUser, ID: botID,
	}); err != nil {
		t.Fatalf("notify peer verified: %v", err)
	}

	if _, ok := r.userFullProjectionCache.Lookup(onlineViewerID, botID); ok {
		t.Fatal("viewer userFull projection survived the badge change")
	}
	if _, ok := r.userFullProjectionCache.Lookup(botID, botID); ok {
		t.Fatal("target userFull projection survived the badge change")
	}
	if users.adminCalls != 1 {
		t.Fatalf("authoritative account reads = %d", users.adminCalls)
	}

	// Offline audience members are skipped; they converge through the bumped
	// user_base read model on their next getDifference / getFullUser.
	pushed := sessions.pushedUserIDs()
	if len(pushed) != 2 || pushed[0] != botID || pushed[1] != onlineViewerID {
		t.Fatalf("pushed user ids = %v", pushed)
	}
	if len(users.viewers) != 2 || users.viewers[0] != botID || users.viewers[1] != onlineViewerID {
		t.Fatalf("re-projected viewers = %v", users.viewers)
	}

	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("updates = %T %+v", sessions.lastUserPush(), sessions.lastUserPush())
	}
	refresh, ok := updates.Updates[0].(*tg.UpdateUser)
	if !ok || refresh.UserID != botID {
		t.Fatalf("refresh = %T %+v", updates.Updates[0], updates.Updates[0])
	}
	if len(updates.Users) != 1 {
		t.Fatalf("users = %+v", updates.Users)
	}
	user, ok := updates.Users[0].(*tg.User)
	if !ok || user.ID != botID || !user.Verified {
		t.Fatalf("user = %T %+v", updates.Users[0], updates.Users[0])
	}
	if user.Scam || user.Fake {
		t.Fatalf("badge push leaked moderation flags: %+v", user)
	}
}

// TestNotifyPeerVerifiedChannelInvalidatesAndPushesUpdateChannel is the channel
// half: the badge rides the existing channel state-mutation fan-out.
func TestNotifyPeerVerifiedChannelInvalidatesAndPushesUpdateChannel(t *testing.T) {
	const (
		channelID = int64(4004)
		ownerID   = int64(3003)
		memberID  = int64(3004)
	)
	channels := &verifiedNotifyChannels{channel: domain.Channel{
		ID: channelID, AccessHash: 44, CreatorUserID: ownerID,
		Title: "Official", Username: "official", Broadcast: true, Verified: true,
	}}
	sessions := &captureSessions{
		onlineUserIDs:  []int64{ownerID, memberID},
		channelMembers: map[int64][]int64{channelID: {ownerID, memberID}},
	}
	r := New(Config{}, Deps{Channels: channels, Sessions: sessions}, zap.NewNop(), clock.System)
	seedChannelFullProjection(t, r, ownerID, channelID)
	seedChannelFullProjection(t, r, memberID, channelID)

	if err := r.NotifyPeerVerified(context.Background(), domain.Peer{
		Type: domain.PeerTypeChannel, ID: channelID,
	}); err != nil {
		t.Fatalf("notify peer verified: %v", err)
	}

	if _, ok := r.channelFullProjectionCache.Lookup(ownerID, channelID); ok {
		t.Fatal("owner channelFull projection survived the badge change")
	}
	if _, ok := r.channelFullProjectionCache.Lookup(memberID, channelID); ok {
		t.Fatal("member channelFull projection survived the badge change")
	}
	if channels.calls != 1 {
		t.Fatalf("base channel row reads = %d", channels.calls)
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
	channel, ok := updates.Chats[0].(*tg.Channel)
	if !ok || channel.ID != channelID || !channel.Verified {
		t.Fatalf("channel = %T %+v", updates.Chats[0], updates.Chats[0])
	}
	if channel.Scam || channel.Fake {
		t.Fatalf("badge push leaked moderation flags: %+v", channel)
	}
}

// TestNotifyPeerVerifiedNilRouterIsSafe pins the nil-receiver contract: the hook is
// invoked after the decision already committed, so it may never panic.
func TestNotifyPeerVerifiedNilRouterIsSafe(t *testing.T) {
	var r *Router
	if err := r.NotifyPeerVerified(context.Background(), domain.Peer{
		Type: domain.PeerTypeUser, ID: 1001,
	}); err != nil {
		t.Fatalf("nil router notify = %v", err)
	}
}

// TestNotifyPeerVerifiedWithoutServicesStillInvalidates covers the degraded wiring:
// with no user/channel service the hook cannot push, but the stale projection must
// still be dropped and no error reported.
func TestNotifyPeerVerifiedWithoutServicesStillInvalidates(t *testing.T) {
	r := New(Config{}, Deps{}, zap.NewNop(), clock.System)
	seedUserFullProjection(t, r, 1001, 2002)
	seedChannelFullProjection(t, r, 1001, 4004)

	if err := r.NotifyPeerVerified(context.Background(), domain.Peer{
		Type: domain.PeerTypeUser, ID: 2002,
	}); err != nil {
		t.Fatalf("notify user without users service = %v", err)
	}
	if err := r.NotifyPeerVerified(context.Background(), domain.Peer{
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

// TestNotifyPeerVerifiedRejectsUnknownPeers pins the "explain, never panic"
// contract for every peer the hook cannot act on.
func TestNotifyPeerVerifiedRejectsUnknownPeers(t *testing.T) {
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
		err := r.NotifyPeerVerified(ctx, tc.peer)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want mention of %q", tc.name, err, tc.want)
		}
	}
	if pushed := sessions.pushedUserIDs(); len(pushed) != 0 {
		t.Fatalf("unresolved peers pushed updates to %v", pushed)
	}
}

// TestNotifyPeerVerifiedReportsLookupFailures keeps a directory error distinct from
// "peer not found", and keeps a channels adapter without the base-row reader from
// failing silently.
func TestNotifyPeerVerifiedReportsLookupFailures(t *testing.T) {
	ctx := context.Background()
	loadErr := errors.New("boom")

	failing := New(Config{}, Deps{
		Channels: &verifiedNotifyChannels{err: loadErr},
		Sessions: &captureSessions{},
	}, zap.NewNop(), clock.System)
	err := failing.NotifyPeerVerified(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: 4004})
	if !errors.Is(err, loadErr) {
		t.Fatalf("channel load error = %v", err)
	}

	unwired := New(Config{}, Deps{
		Channels: channelsWithoutDirectory{},
		Sessions: &captureSessions{},
	}, zap.NewNop(), clock.System)
	err = unwired.NotifyPeerVerified(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: 4004})
	if err == nil || !strings.Contains(err.Error(), "GetChannelByID") {
		t.Fatalf("missing channel directory error = %v", err)
	}
}
