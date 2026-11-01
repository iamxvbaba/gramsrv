package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

const frozenPresenceTestUserID = int64(1009)

// TestFrozenPresenceUpdateStatusNeverGoesOnline locks the invariant that a
// frozen account cannot re-enter the online watermark: account.updateStatus
// online and reconnect renewals must be forced back to offline, and the
// account's own sessions must never receive an online updateUserStatus.
func TestFrozenPresenceUpdateStatusNeverGoesOnline(t *testing.T) {
	userID := frozenPresenceTestUserID
	provider := &frozenGateFreezeProvider{items: map[int64]domain.AccountFreeze{
		userID: frozenGateActiveState(userID),
	}}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{AccountFreeze: provider, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	ctx := WithSessionID(WithUserID(context.Background(), userID), 22)

	if err := r.NotifyAccountFreezeChanged(ctx, domain.AccountFreeze{UserID: userID, Frozen: true}); err != nil {
		t.Fatalf("NotifyAccountFreezeChanged: %v", err)
	}
	// A residual online watermark from before the freeze must be cleared, and an explicit updateStatus(online) must not revive it.
	if ok, err := r.onAccountUpdateStatus(ctx, false); err != nil || !ok {
		t.Fatalf("updateStatus = %v, %v", ok, err)
	}
	if st, ok := r.presence.statusFor(userID, int(time.Now().Unix())); ok && st.Kind == domain.UserStatusOnline {
		t.Fatalf("frozen account tracked online: %+v", st)
	}
	if st := r.userPresenceStatus(userID); st.Kind == domain.UserStatusOnline {
		t.Fatalf("userPresenceStatus(frozen) = %+v, want non-online", st)
	}
	if msg := sessions.lastUserPush(); msg != nil {
		update := pushedUserStatus(t, msg)
		if update.UserID != userID {
			t.Fatalf("pushed status user = %d, want %d", update.UserID, userID)
		}
		if _, ok := update.Status.(*tg.UserStatusOnline); ok {
			t.Fatalf("frozen account pushed online: %+v", update)
		}
	}
}

// TestFrozenPresenceUserStatusNeverOnlineFromProvider locks the read path: even
// when the edge session tracker reports the frozen account online, the presence
// overlay must refuse to render online toward any peer.
func TestFrozenPresenceUserStatusNeverOnlineFromProvider(t *testing.T) {
	userID := frozenPresenceTestUserID
	sessions := &captureSessions{onlineUserIDs: []int64{userID}}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	r.applyFrozenPresence(userID, true)
	if !sessions.IsUserOnline(userID) {
		t.Fatal("test precondition: provider must report the account online")
	}
	if st := r.userPresenceStatus(userID); st.Kind == domain.UserStatusOnline {
		t.Fatalf("userPresenceStatus(frozen) = %+v, provider overlay leaked online", st)
	}
}

// TestFrozenPresenceNotifyDropsTrackerAndBroadcastsEmptyStatus locks the freeze
// transition: pending online presence entries are evicted and the relevant
// peers receive an immediate empty updateUserStatus.
func TestFrozenPresenceNotifyDropsTrackerAndBroadcastsEmptyStatus(t *testing.T) {
	userID := frozenPresenceTestUserID
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	now := time.Now()
	key := presenceSessionKey{sessionID: 1}
	r.presence.setSessionStatus(key, userID, domain.UserStatus{
		Kind:      domain.UserStatusOnline,
		Expires:   int(now.Add(userOnlineTTL).Unix()),
		WasOnline: int(now.Unix()),
	})
	if err := r.NotifyAccountFreezeChanged(context.Background(), domain.AccountFreeze{
		UserID: userID,
		Frozen: true,
		Since:  now.UTC(),
		Until:  now.Add(24 * time.Hour).UTC(),
	}); err != nil {
		t.Fatalf("NotifyAccountFreezeChanged: %v", err)
	}
	if st, ok := r.presence.statusFor(userID, int(now.Unix())); ok {
		t.Fatalf("frozen presence entry survived freeze: %+v", st)
	}
	if !r.isFrozenPresenceUser(userID) {
		t.Fatal("freeze mask not set")
	}
	msg := sessions.lastUserPush()
	if msg == nil {
		t.Fatal("no offline status broadcast on freeze")
	}
	update := pushedUserStatus(t, msg)
	if update.UserID != userID {
		t.Fatalf("pushed status user = %d, want %d", update.UserID, userID)
	}
	if _, ok := update.Status.(*tg.UserStatusOnline); ok {
		t.Fatalf("freeze broadcast pushed online: %+v", update)
	}
	if _, ok := update.Status.(*tg.UserStatusEmpty); !ok {
		t.Fatalf("freeze broadcast status = %T, want empty", update.Status)
	}
}

// TestFrozenPresenceSeedFromDurableFact locks the restart recovery: after a
// process restart the durable notifications have already been consumed, so the
// first session bind must backfill the freeze mask from the store.
func TestFrozenPresenceSeedFromDurableFact(t *testing.T) {
	userID := frozenPresenceTestUserID
	provider := &frozenGateFreezeProvider{items: map[int64]domain.AccountFreeze{
		userID: frozenGateActiveState(userID),
	}}
	r := New(Config{}, Deps{AccountFreeze: provider}, zaptest.NewLogger(t), clock.System)
	r.seedFrozenPresence(context.Background(), userID)
	if !r.isFrozenPresenceUser(userID) {
		t.Fatal("freeze mask not seeded from durable fact")
	}
	// non-frozen account must stay out of the mask
	r.seedFrozenPresence(context.Background(), userID+9)
	if r.isFrozenPresenceUser(userID + 9) {
		t.Fatal("non-frozen account seeded into freeze mask")
	}
}

// TestFrozenPresenceUnfreezeReenablesPresence locks the unfreeze transition:
// the mask is cleared so the account can announce online again.
func TestFrozenPresenceUnfreezeReenablesPresence(t *testing.T) {
	userID := frozenPresenceTestUserID
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	r.frozenPresence.Store(userID, struct{}{})
	r.applyFrozenPresence(userID, false)
	if r.isFrozenPresenceUser(userID) {
		t.Fatal("account still frozen after unfreeze")
	}
}

// TestFrozenPresenceStatusNeverExposesFreshLastSeen locks the read-path
// regression where a frozen account briefly rendered "recently"/"N minutes ago"
// (its pre-freeze last_seen) until the viewer's tombstone projection landed.
// The frozen read must return the deleted-tombstone Empty ("long time ago")
// immediately and consistently, and never fall back to Recently.
func TestFrozenPresenceStatusNeverExposesFreshLastSeen(t *testing.T) {
	userID := frozenPresenceTestUserID + 1
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	r.applyFrozenPresence(userID, true)
	freshLastSeen := int(time.Now().Unix()) - 120
	if st := r.userPresenceStatusForUser(domain.User{ID: userID, LastSeenAt: freshLastSeen}); st.Kind != domain.UserStatusEmpty {
		t.Fatalf("frozen status with fresh last seen = %+v, want UserStatusEmpty", st)
	}
	if st := r.userPresenceStatusForUser(domain.User{ID: userID}); st.Kind != domain.UserStatusEmpty {
		t.Fatalf("frozen status without last seen = %+v, want UserStatusEmpty (not Recently)", st)
	}
	// Unfrozen accounts keep the exact last_seen offline semantics.
	if st := r.userPresenceStatusForUser(domain.User{ID: userID + 100, LastSeenAt: freshLastSeen}); st.Kind != domain.UserStatusOffline || st.WasOnline != freshLastSeen {
		t.Fatalf("regular user status = %+v, want offline was_online=%d", st, freshLastSeen)
	}
}

type captureLastSeenUpdater struct {
	mapUsersService
	calls int
}

func (u *captureLastSeenUpdater) UpdateLastSeen(_ context.Context, _ int64, _ int) error {
	u.calls++
	return nil
}

// TestFrozenPresenceRenewalDoesNotRefreshLastSeen locks the write-path
// regression: a frozen account's live sessions keep renewing presence on
// reconnect/session bind, which used to revisit users.last_seen_at to "just
// now". That fresh durable value is what the read mask leaked for a moment.
// Frozen renewals must not persist anything; the value stays at the level it
// had before the freeze, so the account ages into "long time ago".
func TestFrozenPresenceRenewalDoesNotRefreshLastSeen(t *testing.T) {
	userID := frozenPresenceTestUserID + 2
	updater := &captureLastSeenUpdater{}
	r := New(Config{}, Deps{Users: updater}, zaptest.NewLogger(t), clock.System)
	r.applyFrozenPresence(userID, true)
	if _, notify := r.setPresenceFromContext(WithSessionID(WithUserID(context.Background(), userID), 33), userID, false, presencePersistSync); !notify {
		t.Fatal("frozen renewal should notify once")
	}
	if updater.calls != 0 {
		t.Fatalf("frozen renewal persisted last seen %d times, want 0", updater.calls)
	}
	liveID := userID + 100
	if _, notify := r.setPresenceFromContext(WithSessionID(WithUserID(context.Background(), liveID), 34), liveID, false, presencePersistSync); !notify {
		t.Fatal("live renewal should notify once")
	}
	if updater.calls != 1 {
		t.Fatalf("live renewal last-seen writes = %d, want 1", updater.calls)
	}
}

// TestFrozenPresencePushUserStatusCoercesToEmpty locks the write invariant that
// pushUserStatus coerces any status to UserStatusEmpty for a frozen account.
func TestFrozenPresencePushUserStatusCoercesToEmpty(t *testing.T) {
	userID := frozenPresenceTestUserID + 3
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	r.applyFrozenPresence(userID, true)

	r.pushUserStatus(context.Background(), userID, domain.UserStatus{Kind: domain.UserStatusOnline})
	msg := sessions.lastUserPush()
	if msg == nil {
		t.Fatal("pushUserStatus produced no push")
	}
	update := pushedUserStatus(t, msg)
	if update.UserID != userID {
		t.Fatalf("pushed status user = %d, want %d", update.UserID, userID)
	}
	if _, ok := update.Status.(*tg.UserStatusEmpty); !ok {
		t.Fatalf("pushed status = %T, want *tg.UserStatusEmpty", update.Status)
	}
}

// TestFrozenPresenceAnnounceOfflineEarlyExit verifies that offline announcements
// (both immediate and grace-period delayed) exit early without broadcasting offline
// timestamps or updating last seen.
func TestFrozenPresenceAnnounceOfflineEarlyExit(t *testing.T) {
	userID := frozenPresenceTestUserID + 4
	sessions := &captureSessions{}
	updater := &captureLastSeenUpdater{}
	r := New(Config{}, Deps{Sessions: sessions, Users: updater}, zaptest.NewLogger(t), clock.System)
	r.applyFrozenPresence(userID, true)

	// announceUserOfflineIfStillGone
	r.announceUserOfflineIfStillGone([8]byte{1}, 10, userID, int(time.Now().Unix()))
	if sessions.lastUserPush() != nil {
		t.Fatalf("announceUserOfflineIfStillGone pushed for frozen user: %#v", sessions.lastUserPush())
	}
	if updater.calls != 0 {
		t.Fatalf("announceUserOfflineIfStillGone updated last seen %d times, want 0", updater.calls)
	}
}

// TestFrozenPresencePushStatusPrivacyRefreshEarlyExit verifies that privacy
// refresh exits early for frozen users without pushing status updates.
func TestFrozenPresencePushStatusPrivacyRefreshEarlyExit(t *testing.T) {
	userID := frozenPresenceTestUserID + 5
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	r.applyFrozenPresence(userID, true)

	r.pushStatusPrivacyRefresh(context.Background(), userID)
	if sessions.lastUserPush() != nil {
		t.Fatalf("pushStatusPrivacyRefresh pushed for frozen user: %#v", sessions.lastUserPush())
	}
}
