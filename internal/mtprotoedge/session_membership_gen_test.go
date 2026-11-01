package mtprotoedge

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/edgecontrol"
)

func syncTestSessionChannelMemberships(t *testing.T, sm *SessionManager, raw [8]byte, sessionID, userID int64, channelIDs []int64) bool {
	t.Helper()
	ctx := context.Background()
	syncID, disposition, err := sm.BeginSessionChannelMembershipSync(ctx, raw, sessionID, userID)
	if err != nil {
		t.Fatalf("BeginSessionChannelMembershipSync: %v", err)
	}
	if disposition == edgecontrol.ChannelMembershipSyncPrepared {
		return true
	}
	if disposition != edgecontrol.ChannelMembershipSyncAcquired {
		t.Fatalf("BeginSessionChannelMembershipSync disposition = %q, want acquired", disposition)
	}
	for start := 0; start < len(channelIDs); start += 1000 {
		end := min(start+1000, len(channelIDs))
		if err := sm.AppendSessionChannelMembershipSync(ctx, raw, sessionID, userID, syncID, channelIDs[start:end]); err != nil {
			t.Fatalf("AppendSessionChannelMembershipSync: %v", err)
		}
	}
	synced, err := sm.CommitSessionChannelMembershipSync(ctx, raw, sessionID, userID, syncID)
	if err != nil {
		t.Fatalf("CommitSessionChannelMembershipSync: %v", err)
	}
	return synced
}

func TestSessionChannelMembershipSyncSingleflightKeepsOneOwner(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{0x31}
	const sessionID = int64(31)
	const userID = int64(1000000031)
	c := &Conn{sessionID: sessionID, authKeyID: raw}
	if err := c.FreezeLayerProfile(tlprofile.Profile228); err != nil {
		t.Fatal(err)
	}
	if err := sm.Register(c); err != nil {
		t.Fatal(err)
	}
	sm.BindUserForAuthKey(raw, sessionID, userID)

	const callers = 100
	start := make(chan struct{})
	results := make(chan edgecontrol.ChannelMembershipSyncDisposition, callers)
	ids := make(chan int64, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, disposition, err := sm.BeginSessionChannelMembershipSync(context.Background(), raw, sessionID, userID)
			results <- disposition
			ids <- id
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(ids)
	close(errs)

	acquired := 0
	inProgress := 0
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent begin: %v", err)
		}
	}
	for disposition := range results {
		switch disposition {
		case edgecontrol.ChannelMembershipSyncAcquired:
			acquired++
		case edgecontrol.ChannelMembershipSyncInProgress:
			inProgress++
		default:
			t.Fatalf("concurrent disposition = %q", disposition)
		}
	}
	var syncID int64
	for id := range ids {
		if syncID == 0 {
			syncID = id
		}
		if id != syncID {
			t.Fatalf("concurrent begin replaced owner id: got %d want %d", id, syncID)
		}
	}
	if acquired != 1 || inProgress != callers-1 {
		t.Fatalf("singleflight results acquired=%d in_progress=%d", acquired, inProgress)
	}
	if err := sm.AppendSessionChannelMembershipSync(context.Background(), raw, sessionID, userID, syncID, []int64{7, 8}); err != nil {
		t.Fatal(err)
	}
	if synced, err := sm.CommitSessionChannelMembershipSync(context.Background(), raw, sessionID, userID, syncID); err != nil || !synced {
		t.Fatalf("owner commit synced=%v err=%v", synced, err)
	}
	if !sm.ReceivesUpdatesForAuthKey(raw, sessionID) {
		t.Fatal("singleflight owner commit did not activate session")
	}
}

func TestSessionChannelMembershipSyncExpiredOwnerCanBeTakenOver(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{0x32}
	const sessionID = int64(32)
	const userID = int64(1000000032)
	c := &Conn{sessionID: sessionID, authKeyID: raw}
	if err := c.FreezeLayerProfile(tlprofile.Profile228); err != nil {
		t.Fatal(err)
	}
	if err := sm.Register(c); err != nil {
		t.Fatal(err)
	}
	sm.BindUserForAuthKey(raw, sessionID, userID)

	oldID, disposition, err := sm.BeginSessionChannelMembershipSync(context.Background(), raw, sessionID, userID)
	if err != nil || disposition != edgecontrol.ChannelMembershipSyncAcquired {
		t.Fatalf("first begin id=%d disposition=%q err=%v", oldID, disposition, err)
	}
	key := sessionKey{authKeyID: raw, sessionID: sessionID}
	sm.mu.Lock()
	sm.membershipSyncs[key].expiresAt = time.Now().Add(-time.Second)
	sm.mu.Unlock()
	newID, disposition, err := sm.BeginSessionChannelMembershipSync(context.Background(), raw, sessionID, userID)
	if err != nil || disposition != edgecontrol.ChannelMembershipSyncAcquired || newID == oldID {
		t.Fatalf("takeover begin old=%d new=%d disposition=%q err=%v", oldID, newID, disposition, err)
	}
	if err := sm.AppendSessionChannelMembershipSync(context.Background(), raw, sessionID, userID, oldID, []int64{1}); err == nil {
		t.Fatal("expired owner append unexpectedly succeeded")
	}
	if err := sm.AppendSessionChannelMembershipSync(context.Background(), raw, sessionID, userID, newID, []int64{2}); err != nil {
		t.Fatal(err)
	}
	if synced, err := sm.CommitSessionChannelMembershipSync(context.Background(), raw, sessionID, userID, newID); err != nil || !synced {
		t.Fatalf("takeover commit synced=%v err=%v", synced, err)
	}
}

func TestSessionChannelMembershipSyncProjectionFailureStaysNotReady(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	registry := newCaptureLocationRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sm.StartLocationRegistry(ctx, registry, "edge-membership-failure", time.Minute, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	raw := [8]byte{0x33}
	const sessionID = int64(33)
	const userID = int64(1000000033)
	c := &Conn{sessionID: sessionID, authKeyID: raw}
	if err := c.FreezeLayerProfile(tlprofile.Profile228); err != nil {
		t.Fatal(err)
	}
	if err := sm.Register(c); err != nil {
		t.Fatal(err)
	}
	sm.BindUserForAuthKey(raw, sessionID, userID)
	syncID, disposition, err := sm.BeginSessionChannelMembershipSync(context.Background(), raw, sessionID, userID)
	if err != nil || disposition != edgecontrol.ChannelMembershipSyncAcquired {
		t.Fatalf("begin id=%d disposition=%q err=%v", syncID, disposition, err)
	}
	if err := sm.AppendSessionChannelMembershipSync(context.Background(), raw, sessionID, userID, syncID, []int64{7}); err != nil {
		t.Fatal(err)
	}
	registry.setApplyErr(errors.New("redis unavailable"))
	if synced, err := sm.CommitSessionChannelMembershipSync(context.Background(), raw, sessionID, userID, syncID); err == nil || synced {
		t.Fatalf("failed projection commit synced=%v err=%v, want false/error", synced, err)
	}
	if sm.ReceivesUpdatesForAuthKey(raw, sessionID) || c.membershipsSynced.Load() || c.receivesUpdates.Load() {
		t.Fatal("projection failure exposed a partially ready session")
	}
	registry.setApplyErr(nil)
	if !syncTestSessionChannelMemberships(t, sm, raw, sessionID, userID, []int64{7}) {
		t.Fatal("membership retry did not recover")
	}
	if !sm.ReceivesUpdatesForAuthKey(raw, sessionID) {
		t.Fatal("session not ready after projection retry")
	}
}

func TestOnlineChannelIDsSnapshotAndDiagnosticPagesStableAscending(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{4, 5, 6}
	c := &Conn{sessionID: 77, authKeyID: raw}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, 77, 100)
	if !syncTestSessionChannelMemberships(t, sm, raw, 77, 100, []int64{50, 10, 30, 20, 40}) {
		t.Fatal("initial membership sync raced unexpectedly")
	}
	want := []int64{10, 20, 30, 40, 50}
	snapshot := sm.OnlineChannelIDsSnapshot()
	if !slices.Equal(snapshot, want) {
		t.Fatalf("online channel snapshot = %v, want %v", snapshot, want)
	}

	var got []int64
	after := int64(0)
	for {
		page := sm.OnlineChannelIDsAfter(after, 2)
		if len(page) == 0 {
			break
		}
		for _, channelID := range page {
			if channelID <= after {
				t.Fatalf("page %v not strictly after cursor %d", page, after)
			}
			after = channelID
			got = append(got, channelID)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paged online channels = %v, want %v", got, want)
	}
	// The recovery actor owns a stable copy: later membership changes are visible to the next
	// generation, not spliced into the in-flight sorted snapshot.
	sm.AddUserChannelMembership(100, 5)
	if !slices.Equal(snapshot, want) {
		t.Fatalf("owned snapshot mutated after membership insert: %v", snapshot)
	}
	if current := sm.OnlineChannelIDsSnapshot(); !slices.Equal(current, []int64{5, 10, 20, 30, 40, 50}) {
		t.Fatalf("next online channel snapshot = %v", current)
	}

	// Removing the only live session must immediately remove all channel ids from the recovery
	// enumeration; stale membership map entries are never enough without a live bySession key.
	sm.Unregister(c)
	if got := sm.OnlineChannelIDsAfter(0, 10); len(got) != 0 {
		t.Fatalf("online channels after unregister = %v, want empty", got)
	}
}

// TestPagedSessionChannelMembershipSyncDetectsConcurrentIncrementalUpdates verifies the
// lost-update protection of paged membership sync: the syncer samples the revision number before
// reading the persisted member list; if an incremental join/leave happens inside the read window
// (another device's action lands in the index through Add/RemoveUserChannelMembership), a full
// replacement carrying the stale revision must switch to a union merge (never overwriting the
// increment) and keep membershipsSynced=false so the next RPC retries the full sync until it
// converges.
func TestPagedSessionChannelMembershipSyncDetectsConcurrentIncrementalUpdates(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	c := &Conn{sessionID: 42, authKeyID: raw}
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatal(err)
	}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, 42, 100)
	sm.SetReceivesUpdatesForAuthKey(raw, 42, true)

	ctx := context.Background()
	// After Begin samples the revision on the Edge and before the paged list commits, the user joins channel 7 on another device.
	syncID, disposition, err := sm.BeginSessionChannelMembershipSync(ctx, raw, 42, 100)
	if err != nil || disposition != edgecontrol.ChannelMembershipSyncAcquired {
		t.Fatalf("begin raced sync id=%d disposition=%q err=%v", syncID, disposition, err)
	}
	sm.AddUserChannelMembership(100, 7)
	if err := sm.AppendSessionChannelMembershipSync(ctx, raw, 42, 100, syncID, []int64{5}); err != nil {
		t.Fatal(err)
	}
	if synced, err := sm.CommitSessionChannelMembershipSync(ctx, raw, 42, 100, syncID); err != nil || synced {
		t.Fatalf("raced commit synced=%v err=%v, want false/nil", synced, err)
	}

	if got := sm.OnlineChannelMemberUserIDs(7, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 7 members = %v, want [100]: full replace overwrote the in-window incremental join", got)
	}
	if got := sm.OnlineChannelMemberUserIDs(5, 10); len(got) != 1 || got[0] != 100 {
		t.Fatalf("channel 5 members = %v, want [100]: merge path must still apply the full list", got)
	}
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session fully ready despite raced membership sync; retry would never happen")
	}

	// Retry: under the new revision number the full sync replaces normally and sets ready.
	if !syncTestSessionChannelMemberships(t, sm, raw, 42, 100, []int64{5, 7}) {
		t.Fatal("clean resync did not commit")
	}
	if !sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session not ready after clean resync")
	}

	// The other direction: removed from channel 5 within the window, and the stale
	// full list still contains 5 → the merge briefly retains the stale entry
	// (caught by the pre-fan-out PG active recheck), but must stay not-ready awaiting retry.
	c.membershipsSynced.Store(false)
	c.receivesUpdates.Store(false)
	syncID, disposition, err = sm.BeginSessionChannelMembershipSync(ctx, raw, 42, 100)
	if err != nil || disposition != edgecontrol.ChannelMembershipSyncAcquired {
		t.Fatalf("begin removal race id=%d disposition=%q err=%v", syncID, disposition, err)
	}
	sm.RemoveUserChannelMembership(100, 5)
	if err := sm.AppendSessionChannelMembershipSync(ctx, raw, 42, 100, syncID, []int64{5, 7}); err != nil {
		t.Fatal(err)
	}
	if synced, err := sm.CommitSessionChannelMembershipSync(ctx, raw, 42, 100, syncID); err != nil || synced {
		t.Fatalf("raced removal commit synced=%v err=%v, want false/nil", synced, err)
	}
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session ready despite raced removal during sync")
	}
	if !syncTestSessionChannelMemberships(t, sm, raw, 42, 100, []int64{7}) {
		t.Fatal("final membership resync did not commit")
	}
	if got := sm.OnlineChannelMemberUserIDs(5, 10); len(got) != 0 {
		t.Fatalf("channel 5 members after resync = %v, want empty", got)
	}
	if !sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session not ready after final resync")
	}
}

// TestRegisterEvictsOldestSessionAtCap verifies that when the session count for the
// same raw auth_key hits the cap, the connection established earliest is evicted,
// not a random one under map iteration order (which could wrongly kill a
// just-established active connection).
func TestRegisterEvictsOldestSessionAtCap(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{9}
	base := time.Unix(1_700_000_000, 0)

	const oldestSession = int64(100)
	oldestTransport := &closeCountingTransport{}
	for i := 0; i < maxSessionsPerAuthKey; i++ {
		sid := int64(i + 1)
		created := base.Add(time.Duration(i+1) * time.Second)
		if sid == oldestSession {
			created = base // the only timestamp earlier than all other connections, and deliberately not first in registration order.
		}
		c := &Conn{sessionID: sid, authKeyID: raw, createdAt: created}
		if sid == oldestSession {
			c.transport = oldestTransport
		}
		sm.Register(c)
	}

	sm.Register(&Conn{sessionID: 9999, authKeyID: raw, createdAt: base.Add(time.Hour)})

	sm.mu.RLock()
	_, oldestAlive := sm.bySession[sessionKey{authKeyID: raw, sessionID: oldestSession}]
	_, newestAlive := sm.bySession[sessionKey{authKeyID: raw, sessionID: 9999}]
	total := len(sm.byAuthKey[raw])
	sm.mu.RUnlock()
	if oldestAlive {
		t.Fatal("oldest session survived eviction at cap")
	}
	if !newestAlive {
		t.Fatal("newly registered session missing after eviction")
	}
	if total != maxSessionsPerAuthKey {
		t.Fatalf("sessions for auth key = %d, want cap %d", total, maxSessionsPerAuthKey)
	}
	if oldestTransport.closes != 1 {
		t.Fatalf("evicted transport closes = %d, want 1", oldestTransport.closes)
	}
}
