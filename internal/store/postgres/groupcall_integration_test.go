package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/storetest"
)

// TestGroupCallStoreContractPostgres runs the same contract as the memory implementation (gated on TELESRV_TEST_POSTGRES_DSN).
func TestGroupCallStoreContractPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	// A run-unique namespace keeps filtered subtest runs independent from stale
	// rows left by an interrupted older run. Per-subtest cleanup below still makes
	// successful runs leave no state behind.
	var nextChannel = int64(1_000_000_000 + time.Now().UnixNano()%100_000_000)
	storetest.RunGroupCallStoreContract(t, func(t *testing.T) (store.GroupCallStore, int64) {
		nextChannel++
		channelID := nextChannel
		// Conference contract rows have channel_id=0 and derive their call IDs from
		// this synthetic channel namespace. Clean both shapes before and after each
		// subtest so a previous failed run cannot feed discarded calls into the next
		// run (and so conference invite/chain rows cascade away as well).
		cleanup := func() {
			_, _ = pool.Exec(ctx, `
DELETE FROM group_calls
WHERE channel_id = $1
   OR (call_id >= $1 * 100 AND call_id < $1 * 100 + 100)`, channelID)
		}
		cleanup()
		t.Cleanup(cleanup)
		return NewGroupCallStore(pool), channelID
	})
}

// TestGroupCallStoreM2ContractPostgres runs the overrides/raise-hand contract (DSN-gated).
func TestGroupCallStoreM2ContractPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var nextChannel int64 = 920_000_000
	storetest.RunGroupCallStoreM2Contract(t, func(t *testing.T) (store.GroupCallStore, int64) {
		nextChannel++
		channelID := nextChannel
		t.Cleanup(func() {
			_, _ = pool.Exec(ctx, "DELETE FROM group_calls WHERE channel_id = $1", channelID)
		})
		return NewGroupCallStore(pool), channelID
	})
}

// TestGroupCallStoreConcurrentJoinVersions verifies that version has no skips under row-lock serialization.
func TestGroupCallStoreConcurrentJoinVersions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const channelID = 910_900_001
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM group_calls WHERE channel_id = $1", channelID)
	})
	st := NewGroupCallStore(pool)
	// Use the run time as the time basis: the test database may be shared with a running dev
	// server, where an ancient last_check_date would be swept away as a ghost by the online
	// sweeper and pollute the version/count assertions.
	now := int(time.Now().Unix())
	call, err := st.CreateGroupCall(ctx, domain.GroupCall{ID: 910_900_100, AccessHash: 5, ChannelID: channelID, CreatorUserID: 1, CreatedAt: now})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	const joiners = 8
	var wg sync.WaitGroup
	versions := make(chan int, joiners)
	for i := int64(0); i < joiners; i++ {
		wg.Add(1)
		go func(userID int64) {
			defer wg.Done()
			mut, err := st.JoinGroupCall(ctx, domain.JoinGroupCallRequest{CallID: call.ID, UserID: userID, SSRC: 7000 + userID, Now: now})
			if err != nil {
				t.Errorf("join %d: %v", userID, err)
				return
			}
			versions <- mut.Call.Version
		}(100 + i)
	}
	wg.Wait()
	close(versions)
	seen := map[int]bool{}
	for v := range versions {
		if seen[v] {
			t.Fatalf("duplicate version %d under concurrency", v)
		}
		seen[v] = true
	}
	final, _, err := st.GetGroupCall(ctx, call.ID)
	if err != nil || final.Version != 1+joiners || final.ParticipantsCount != joiners {
		t.Fatalf("final call = %+v err=%v", final, err)
	}
}
