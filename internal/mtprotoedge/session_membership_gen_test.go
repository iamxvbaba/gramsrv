package mtprotoedge

import (
	"slices"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"
)

func TestOnlineChannelIDsSnapshotAndDiagnosticPagesStableAscending(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{4, 5, 6}
	c := &Conn{sessionID: 77, authKeyID: raw}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, 77, 100)
	sm.SetSessionChannelMemberships(raw, 77, 100, []int64{50, 10, 30, 20, 40}, sm.ChannelMembershipGeneration(raw, 77))
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

// TestSetSessionChannelMembershipsDetectsConcurrentIncrementalUpdates verifies the
// lost-update protection of full membership sync: the sync party samples the
// revision number before reading the persistent membership list, and if an
// incremental join/leave happens within the read window (another device's
// operation lands in the index via Add/RemoveUserChannelMembership), a full
// replacement carrying a stale revision must switch to a union merge (not
// overwriting the increment) and keep membershipsSynced=false, prompting the next
// RPC to retry the full sync to converge.
func TestSetSessionChannelMembershipsDetectsConcurrentIncrementalUpdates(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	c := &Conn{sessionID: 42, authKeyID: raw}
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatal(err)
	}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, 42, 100)
	sm.SetReceivesUpdatesForAuthKey(raw, 42, true)

	// After the sync party samples the revision number and before the full list is
	// committed, the user joins channel 7 on another device.
	gen := sm.ChannelMembershipGeneration(raw, 42)
	sm.AddUserChannelMembership(100, 7)
	// The full list based on the old snapshot (only channel 5, excluding 7) commits
	// carrying a stale revision number.
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{5}, gen)

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
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{5, 7}, sm.ChannelMembershipGeneration(raw, 42))
	if !sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session not ready after clean resync")
	}

	// The other direction: removed from channel 5 within the window, and the stale
	// full list still contains 5 → the merge briefly retains the stale entry
	// (caught by the pre-fan-out PG active recheck), but must stay not-ready awaiting retry.
	gen = sm.ChannelMembershipGeneration(raw, 42)
	sm.RemoveUserChannelMembership(100, 5)
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{5, 7}, gen)
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("session ready despite raced removal during sync")
	}
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{7}, sm.ChannelMembershipGeneration(raw, 42))
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
