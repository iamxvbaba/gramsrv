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

// TestGroupCallStoreContractPostgres 与 memory 实现共跑同一契约（TELESRV_TEST_POSTGRES_DSN 门控）。
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

// TestGroupCallStoreM2ContractPostgres 跑 overrides/raise-hand 契约（DSN 门控）。
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

// TestGroupCallStoreConcurrentJoinVersions 验证行锁串行化下 version 无跳号。
func TestGroupCallStoreConcurrentJoinVersions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const channelID = 910_900_001
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM group_calls WHERE channel_id = $1", channelID)
	})
	st := NewGroupCallStore(pool)
	// 时间基准取运行时刻：测试库可能与运行中的开发服务器共库，远古
	// last_check_date 会被线上 sweeper 当幽灵清掉，污染 version/count 断言。
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
