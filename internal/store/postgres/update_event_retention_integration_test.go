package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"

	appupdates "telesrv/internal/app/updates"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestUserUpdateRetentionUsesClientObservedCommonPrefixPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "update-retention")
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	states := NewUpdateStateStore(pool)
	events := NewUpdateEventStore(pool)

	newKey := func() [8]byte {
		var id [8]byte
		if _, err := rand.Read(id[:]); err != nil {
			t.Fatalf("random auth key id: %v", err)
		}
		return id
	}
	authOne, authTwo := newKey(), newKey()
	for _, id := range [][8]byte{authOne, authTwo} {
		if err := keys.Save(ctx, store.AuthKeyData{ID: id}); err != nil {
			t.Fatalf("save auth key %x: %v", id, err)
		}
		id := id
		t.Cleanup(func() { _ = keys.Delete(ctx, id) })
		if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: id, UserID: userID}); err != nil {
			t.Fatalf("bind authorization %x: %v", id, err)
		}
	}

	const oldDate = 1_600_000_000
	for i := 1; i <= 3; i++ {
		if _, err := events.AppendAllocated(ctx, userID, domain.UpdateEvent{
			Type: domain.UpdateEventNoop, PtsCount: 1, Date: oldDate + i,
		}); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}

	// Save is the state the server has sent/constructed. Neither device has proved receipt, so it
	// must not authorize retention even though both delivered cursors are at pts=3.
	for _, id := range [][8]byte{authOne, authTwo} {
		if err := states.Save(ctx, id, userID, domain.UpdateState{Pts: 3, Date: oldDate + 3}); err != nil {
			t.Fatalf("save delivered state %x: %v", id, err)
		}
	}
	if deleted, err := events.DeleteConfirmedPrefix(ctx, time.Second, 10); err != nil || deleted != 0 {
		t.Fatalf("delete with no observed cursor = %d/%v, want 0/nil", deleted, err)
	}

	if err := states.ObserveClientState(ctx, authOne, userID, domain.UpdateState{Pts: 3, Date: oldDate + 3}); err != nil {
		t.Fatalf("observe first device: %v", err)
	}
	if deleted, err := events.DeleteConfirmedPrefix(ctx, time.Second, 10); err != nil || deleted != 0 {
		t.Fatalf("delete while second device unobserved = %d/%v, want 0/nil", deleted, err)
	}

	// Common observed floor=min(3,1)=1, so exactly the first contiguous event is removable.
	if err := states.ObserveClientState(ctx, authTwo, userID, domain.UpdateState{Pts: 1, Date: oldDate + 1}); err != nil {
		t.Fatalf("observe second device pts=1: %v", err)
	}
	deleted, err := events.DeleteConfirmedPrefix(ctx, time.Second, 10)
	if err != nil || deleted != 1 {
		t.Fatalf("delete common prefix = %d/%v, want 1/nil", deleted, err)
	}
	pts, date, ok, err := events.UserUpdateRetentionCheckpoint(ctx, authTwo, userID)
	if err != nil || !ok || pts != 1 || date != oldDate+1 {
		t.Fatalf("checkpoint = pts:%d date:%d ok:%v err:%v, want 1/%d/true/nil", pts, date, ok, err, oldDate+1)
	}
	remaining, err := events.ListAfter(ctx, userID, 0, 10)
	if err != nil || len(remaining) != 2 || remaining[0].Pts != 2 || remaining[1].Pts != 3 {
		t.Fatalf("remaining events = %+v err=%v, want pts 2,3", remaining, err)
	}

	if err := states.ObserveClientState(ctx, authTwo, userID, domain.UpdateState{Pts: 3, Date: oldDate + 3}); err != nil {
		t.Fatalf("observe second device pts=3: %v", err)
	}
	deleted, err = events.DeleteConfirmedPrefix(ctx, time.Second, 10)
	if err != nil || deleted != 2 {
		t.Fatalf("delete remaining common prefix = %d/%v, want 2/nil", deleted, err)
	}

	// A newly created authorization did not exist when the common prefix was confirmed. Seed its
	// observed baseline at the retained floor (not at current pts): it can receive an ordinary
	// empty differenceSlice checkpoint instead of falling into a silent hole, while still blocking
	// any future pruning until it reports subsequent progress itself.
	authThree := newKey()
	if err := keys.Save(ctx, store.AuthKeyData{ID: authThree}); err != nil {
		t.Fatalf("save third auth key: %v", err)
	}
	t.Cleanup(func() { _ = keys.Delete(ctx, authThree) })
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: authThree, UserID: userID}); err != nil {
		t.Fatalf("bind third authorization: %v", err)
	}
	pts, date, ok, err = events.UserUpdateRetentionCheckpoint(ctx, authThree, userID)
	if err != nil || !ok || pts != 3 || date != oldDate+3 {
		t.Fatalf("new authorization checkpoint = pts:%d date:%d ok:%v err:%v, want 3/%d/true/nil", pts, date, ok, err, oldDate+3)
	}
	var observed int
	if err := pool.QueryRow(ctx, `
SELECT observed_pts FROM update_states WHERE auth_key_id = $1 AND user_id = $2
`, authKeyIDToInt64(authThree), userID).Scan(&observed); err != nil {
		t.Fatalf("load third observed floor: %v", err)
	}
	if observed != 3 {
		t.Fatalf("new authorization observed_pts = %d, want retained floor 3", observed)
	}
}

func TestAuthorizationBindSwitchesAccountAfterRetainedFloorPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	oldUserID := createRevokeTestUser(t, ctx, pool, "retention-switch-old")
	newUserID := createRevokeTestUser(t, ctx, pool, "retention-switch-new")
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	states := NewUpdateStateStore(pool)
	events := NewUpdateEventStore(pool)
	mainKey := randomUpdateRetentionAuthKey(t)
	guardKey := randomUpdateRetentionAuthKey(t)
	for _, id := range [][8]byte{mainKey, guardKey} {
		if err := keys.Save(ctx, store.AuthKeyData{ID: id}); err != nil {
			t.Fatalf("save auth key %x: %v", id, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM update_states WHERE auth_key_id = ANY($1::bigint[])", []int64{
			authKeyIDToInt64(mainKey), authKeyIDToInt64(guardKey),
		})
		_ = keys.Delete(ctx, mainKey)
		_ = keys.Delete(ctx, guardKey)
	})

	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: mainKey, UserID: oldUserID}); err != nil {
		t.Fatalf("bind main key to old account: %v", err)
	}
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: guardKey, UserID: newUserID}); err != nil {
		t.Fatalf("bind guard key to new account: %v", err)
	}
	const oldDate = 1_600_100_000
	for i := 1; i <= 3; i++ {
		if _, err := events.AppendAllocated(ctx, newUserID, domain.UpdateEvent{
			Type: domain.UpdateEventNoop, PtsCount: 1, Date: oldDate + i,
		}); err != nil {
			t.Fatalf("append new-account event %d: %v", i, err)
		}
	}
	if err := states.ObserveClientState(ctx, guardKey, newUserID, domain.UpdateState{Pts: 3, Date: oldDate + 3}); err != nil {
		t.Fatalf("observe guard through pts 3: %v", err)
	}
	if deleted, err := events.DeleteConfirmedPrefix(ctx, time.Second, 10); err != nil || deleted != 3 {
		t.Fatalf("prune new-account prefix = %d/%v, want 3/nil", deleted, err)
	}

	// This is the account-switch boundary that used to be followed by Router.ClearAuthKey,
	// deleting the state Bind had just created for newUserID.
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: mainKey, UserID: newUserID}); err != nil {
		t.Fatalf("switch main key to new account: %v", err)
	}
	var oldStates int
	if err := pool.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM update_states
WHERE auth_key_id = $1 AND user_id = $2`, authKeyIDToInt64(mainKey), oldUserID).Scan(&oldStates); err != nil {
		t.Fatalf("count old-account states: %v", err)
	}
	if oldStates != 0 {
		t.Fatalf("old-account update states = %d, want 0", oldStates)
	}
	var delivered, observed int
	if err := pool.QueryRow(ctx, `
SELECT pts, observed_pts
FROM update_states
WHERE auth_key_id = $1 AND user_id = $2`, authKeyIDToInt64(mainKey), newUserID).Scan(&delivered, &observed); err != nil {
		t.Fatalf("load switched-account state: %v", err)
	}
	if delivered != 3 || observed != 3 {
		t.Fatalf("switched-account state = delivered:%d observed:%d, want 3/3", delivered, observed)
	}

	diff, err := appupdates.NewService(states, events).GetDifference(
		ctx,
		mainKey,
		newUserID,
		domain.UpdateState{Pts: 0, Date: oldDate},
	)
	if err != nil {
		t.Fatalf("difference after account switch: %v", err)
	}
	if !diff.Partial || len(diff.Events) != 0 || diff.State.Pts != 3 {
		t.Fatalf("switch checkpoint difference = %+v, want empty slice at retained pts 3", diff)
	}
}

func TestAuthorizationBindRejectsFutureSameUserStatePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "retention-stale-rebind")
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	states := NewUpdateStateStore(pool)
	events := NewUpdateEventStore(pool)
	guardKey := randomUpdateRetentionAuthKey(t)
	staleKey := randomUpdateRetentionAuthKey(t)
	for _, id := range [][8]byte{guardKey, staleKey} {
		if err := keys.Save(ctx, store.AuthKeyData{ID: id}); err != nil {
			t.Fatalf("save auth key %x: %v", id, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM update_states WHERE auth_key_id = ANY($1::bigint[])", []int64{
			authKeyIDToInt64(guardKey), authKeyIDToInt64(staleKey),
		})
		_ = keys.Delete(ctx, guardKey)
		_ = keys.Delete(ctx, staleKey)
	})

	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: guardKey, UserID: userID}); err != nil {
		t.Fatalf("bind guard authorization: %v", err)
	}
	const oldDate = 1_600_200_000
	for i := 1; i <= 5; i++ {
		if _, err := events.AppendAllocated(ctx, userID, domain.UpdateEvent{
			Type: domain.UpdateEventNoop, PtsCount: 1, Date: oldDate + i,
		}); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}
	if err := states.ObserveClientState(ctx, guardKey, userID, domain.UpdateState{Pts: 3, Date: oldDate + 3}); err != nil {
		t.Fatalf("observe guard pts 3: %v", err)
	}
	if deleted, err := events.DeleteConfirmedPrefix(ctx, time.Second, 10); err != nil || deleted != 3 {
		t.Fatalf("prune stale-rebind prefix = %d/%v, want 3/nil", deleted, err)
	}

	// Deliberately inject historical corruption: the authorization is absent while a stale cursor
	// claims a future pts beyond the account's contiguous watermark (5). Bind must fail-fast and
	// leave the key unauthorized; preserving pts=7 would make future retention/difference lie.
	if _, err := pool.Exec(ctx, `
INSERT INTO update_states (auth_key_id, user_id, pts, qts, date, seq, observed_pts)
VALUES ($1, $2, 7, 4, $3, 2, 1)`, authKeyIDToInt64(staleKey), userID, oldDate+1); err != nil {
		t.Fatalf("insert stale update state: %v", err)
	}
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: staleKey, UserID: userID}); err == nil {
		t.Fatal("Bind accepted future update state, want invariant error")
	}
	if _, found, err := auths.ByAuthKey(ctx, staleKey); err != nil || found {
		t.Fatalf("authorization after rejected Bind found=%v err=%v, want false/nil", found, err)
	}
	var delivered, qts, seq, observed int
	if err := pool.QueryRow(ctx, `
SELECT pts, qts, seq, observed_pts
FROM update_states
WHERE auth_key_id = $1 AND user_id = $2`, authKeyIDToInt64(staleKey), userID).Scan(&delivered, &qts, &seq, &observed); err != nil {
		t.Fatalf("load rejected stale state: %v", err)
	}
	if delivered != 7 || qts != 4 || seq != 2 || observed != 1 {
		t.Fatalf("rejected stale state mutated = pts:%d qts:%d seq:%d observed:%d, want 7/4/2/1", delivered, qts, seq, observed)
	}

	// Once an explicit repair brings the persisted cursor back inside the current account
	// watermark, Bind may establish the retained-floor baseline without moving qts/seq backwards.
	if _, err := pool.Exec(ctx, `
UPDATE update_states
SET pts = 5
WHERE auth_key_id = $1 AND user_id = $2`, authKeyIDToInt64(staleKey), userID); err != nil {
		t.Fatalf("repair future delivered state: %v", err)
	}
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: staleKey, UserID: userID}); err != nil {
		t.Fatalf("bind explicitly repaired authorization: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT pts, qts, seq, observed_pts
FROM update_states
WHERE auth_key_id = $1 AND user_id = $2`, authKeyIDToInt64(staleKey), userID).Scan(&delivered, &qts, &seq, &observed); err != nil {
		t.Fatalf("load bound repaired state: %v", err)
	}
	if delivered != 5 || qts != 4 || seq != 2 || observed != 3 {
		t.Fatalf("bound repaired state = pts:%d qts:%d seq:%d observed:%d, want 5/4/2/3", delivered, qts, seq, observed)
	}

	// Protection: if the lifecycle invariant is corrupted again, checkpoint lookup must fail-fast
	// instead of letting GetDifference fall through to an empty read below deleted history.
	if _, err := pool.Exec(ctx, `
UPDATE update_states
SET observed_pts = 1
WHERE auth_key_id = $1 AND user_id = $2`, authKeyIDToInt64(staleKey), userID); err != nil {
		t.Fatalf("corrupt observed state for guard test: %v", err)
	}
	if _, _, _, err := events.UserUpdateRetentionCheckpoint(ctx, staleKey, userID); err == nil {
		t.Fatal("checkpoint with observed below retained floor succeeded, want invariant error")
	}
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: staleKey, UserID: userID}); err != nil {
		t.Fatalf("same-user Bind did not repair observed floor: %v", err)
	}
	if pts, _, ok, err := events.UserUpdateRetentionCheckpoint(ctx, staleKey, userID); err != nil || !ok || pts != 3 {
		t.Fatalf("checkpoint after same-user repair = pts:%d ok:%v err:%v", pts, ok, err)
	}
}

func TestAuthorizationBindSerializesWithRetentionTwoConnectionsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "retention-bind-race")
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	states := NewUpdateStateStore(pool)
	events := NewUpdateEventStore(pool)
	guardKey := randomUpdateRetentionAuthKey(t)
	newKey := randomUpdateRetentionAuthKey(t)
	for _, id := range [][8]byte{guardKey, newKey} {
		if err := keys.Save(ctx, store.AuthKeyData{ID: id}); err != nil {
			t.Fatalf("save auth key %x: %v", id, err)
		}
		id := id
		t.Cleanup(func() { _ = keys.Delete(ctx, id) })
	}
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: guardKey, UserID: userID}); err != nil {
		t.Fatalf("bind guard authorization: %v", err)
	}
	const eventDate = 1_600_300_001
	if _, err := events.AppendAllocated(ctx, userID, domain.UpdateEvent{
		Type: domain.UpdateEventNoop, PtsCount: 1, Date: eventDate,
	}); err != nil {
		t.Fatalf("append guarded event: %v", err)
	}
	if err := states.ObserveClientState(ctx, guardKey, userID, domain.UpdateState{Pts: 1, Date: eventDate}); err != nil {
		t.Fatalf("observe guard event: %v", err)
	}

	retentionConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire retention connection: %v", err)
	}
	defer retentionConn.Release()
	bindConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire bind connection: %v", err)
	}
	defer bindConn.Release()

	tx, err := retentionConn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin retention transaction: %v", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	var currentPts, floor int
	if err := tx.QueryRow(ctx, `
SELECT contiguous_pts
FROM user_update_watermarks
WHERE user_id = $1
FOR UPDATE`, userID).Scan(&currentPts); err != nil {
		t.Fatalf("lock retention watermark: %v", err)
	}
	if err := tx.QueryRow(ctx, `
SELECT retained_through_pts
FROM user_update_retention
WHERE user_id = $1
FOR UPDATE`, userID).Scan(&floor); err != nil {
		t.Fatalf("lock retention floor: %v", err)
	}
	if currentPts != 1 || floor != 0 {
		t.Fatalf("pre-race watermark/floor = %d/%d, want 1/0", currentPts, floor)
	}

	bindCtx, cancelBind := context.WithTimeout(ctx, 5*time.Second)
	defer cancelBind()
	bindDone := make(chan error, 1)
	go func() {
		bindDone <- NewAuthorizationStore(bindConn).Bind(bindCtx, domain.Authorization{
			AuthKeyID: newKey,
			UserID:    userID,
		})
	}()

	// Observe the second physical connection waiting on the watermark row. This proves the
	// synchronization is a database lock, rather than relying on scheduler timing in the test.
	bindPID := bindConn.Conn().PgConn().PID()
	waitDeadline := time.Now().Add(2 * time.Second)
	waiting := false
	for time.Now().Before(waitDeadline) {
		select {
		case err := <-bindDone:
			t.Fatalf("Bind completed before retained-floor transaction committed: %v", err)
		default:
		}
		if err := tx.QueryRow(ctx, `
SELECT COALESCE(wait_event_type = 'Lock', false)
FROM pg_stat_activity
WHERE pid = $1`, bindPID).Scan(&waiting); err != nil {
			t.Fatalf("inspect bind lock wait: %v", err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("Bind connection did not wait on retention watermark lock")
	}

	// Complete the valid confirmed-prefix transition while Bind is waiting. After commit Bind
	// must read floor=1 and atomically seed observed_pts=1; floor=0 would create a silent hole.
	if tag, err := tx.Exec(ctx, `
DELETE FROM user_update_events
WHERE user_id = $1 AND pts = 1`, userID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("delete retained event rows=%d err=%v, want 1/nil", tag.RowsAffected(), err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE user_update_retention
SET retained_through_pts = 1,
    retained_through_date = $2,
    updated_at = now()
WHERE user_id = $1`, userID, eventDate); err != nil {
		t.Fatalf("advance retained floor: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit retained floor: %v", err)
	}
	committed = true
	select {
	case err := <-bindDone:
		if err != nil {
			t.Fatalf("Bind after retention commit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Bind remained blocked after retention commit")
	}

	var delivered, observed int
	if err := pool.QueryRow(ctx, `
SELECT pts, observed_pts
FROM update_states
WHERE auth_key_id = $1 AND user_id = $2`, authKeyIDToInt64(newKey), userID).Scan(&delivered, &observed); err != nil {
		t.Fatalf("load raced bind baseline: %v", err)
	}
	if delivered != 1 || observed != 1 {
		t.Fatalf("raced bind baseline = delivered:%d observed:%d, want 1/1", delivered, observed)
	}
}

func TestUserUpdateRetentionOldTailsDoNotConsumeCandidatePassPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	const tailUsers = 256
	const totalUsers = tailUsers + 1
	prefix := fmt.Sprintf("+188%010d", time.Now().UnixNano()%10_000_000_000)
	rows, err := pool.Query(ctx, `
INSERT INTO users (access_hash, phone, first_name)
SELECT $1::bigint + n, $2 || lpad(n::text, 3, '0'), 'retention-old-tail'
FROM generate_series(1, $3::int) AS n
RETURNING id
`, time.Now().UnixNano(), prefix, totalUsers)
	if err != nil {
		t.Fatalf("bulk insert old-tail users: %v", err)
	}
	userIDs := make([]int64, 0, totalUsers)
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			rows.Close()
			t.Fatalf("scan old-tail user: %v", err)
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate old-tail users: %v", err)
	}
	rows.Close()
	if len(userIDs) != totalUsers {
		t.Fatalf("inserted users = %d, want %d", len(userIDs), totalUsers)
	}
	authKeyIDs := make([]int64, len(userIDs))
	watermarks := make([]int32, len(userIDs))
	for i, userID := range userIDs {
		authKeyIDs[i] = -userID
		if i < tailUsers {
			watermarks[i] = 2
		} else {
			watermarks[i] = 1
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM update_states WHERE auth_key_id = ANY($1::bigint[])", authKeyIDs)
		_, _ = pool.Exec(ctx, "DELETE FROM auth_keys WHERE auth_key_id = ANY($1::bigint[])", authKeyIDs)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", userIDs)
	})
	if _, err := pool.Exec(ctx, `
INSERT INTO auth_keys (auth_key_id, body, server_salt)
SELECT id, decode(repeat('00', 256), 'hex'), 0
FROM unnest($1::bigint[]) AS id`, authKeyIDs); err != nil {
		t.Fatalf("bulk insert old-tail auth keys: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO authorizations (auth_key_id, user_id)
SELECT * FROM unnest($1::bigint[], $2::bigint[])`, authKeyIDs, userIDs); err != nil {
		t.Fatalf("bulk insert old-tail authorizations: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO user_update_watermarks (user_id, contiguous_pts)
SELECT * FROM unnest($1::bigint[], $2::integer[])`, userIDs, watermarks); err != nil {
		t.Fatalf("bulk insert old-tail watermarks: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO update_states (auth_key_id, user_id, pts, observed_pts)
SELECT auth_key_id, user_id, pts, pts
FROM unnest($1::bigint[], $2::bigint[], $3::integer[]) AS input(auth_key_id, user_id, pts)`, authKeyIDs, userIDs, watermarks); err != nil {
		t.Fatalf("bulk insert old-tail states: %v", err)
	}
	recentHeadDate := int32(time.Now().Add(time.Hour).Unix())
	if _, err := pool.Exec(ctx, `
INSERT INTO user_update_events (user_id, pts, pts_count, date, event_type)
SELECT user_id, 1, 1, $2, 'noop'
FROM unnest($1::bigint[]) AS user_id`, userIDs[:tailUsers], recentHeadDate); err != nil {
		t.Fatalf("insert recent old-tail heads: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO user_update_events (user_id, pts, pts_count, date, event_type)
SELECT user_id, 2, 1, 1, 'noop'
FROM unnest($1::bigint[]) AS user_id`, userIDs[:tailUsers]); err != nil {
		t.Fatalf("insert old tails: %v", err)
	}
	healthyUserID := userIDs[len(userIDs)-1]
	if _, err := pool.Exec(ctx, `
INSERT INTO user_update_events (user_id, pts, pts_count, date, event_type)
VALUES ($1, 1, 1, 2, 'noop')`, healthyUserID); err != nil {
		t.Fatalf("insert healthy retention head: %v", err)
	}

	deleted, err := NewUpdateEventStore(pool).DeleteConfirmedPrefix(ctx, time.Second, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("delete after 256 old tails = %d/%v, want healthy 1/nil", deleted, err)
	}
	var healthyRows, tailRows int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM user_update_events WHERE user_id = $1)::int,
  (SELECT count(*) FROM user_update_events WHERE user_id = ANY($2::bigint[]))::int`, healthyUserID, userIDs[:tailUsers]).Scan(&healthyRows, &tailRows); err != nil {
		t.Fatalf("count old-tail retention rows: %v", err)
	}
	if healthyRows != 0 || tailRows != tailUsers*2 {
		t.Fatalf("remaining healthy/tail rows = %d/%d, want 0/%d", healthyRows, tailRows, tailUsers*2)
	}
}

func TestUserUpdateRetentionDeletesDispatchLeaseAndPromotesHeadPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "retention-dispatch-lease")
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	states := NewUpdateStateStore(pool)
	events := NewUpdateEventStore(pool)
	outbox := NewDispatchOutboxStore(pool, WithLeaseTimeout(time.Hour))
	authKeyID := randomUpdateRetentionAuthKey(t)
	if err := keys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatalf("save retention dispatch auth key: %v", err)
	}
	t.Cleanup(func() { _ = keys.Delete(ctx, authKeyID) })
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: userID}); err != nil {
		t.Fatalf("bind retention dispatch authorization: %v", err)
	}
	appendDispatch := func(date int) domain.UpdateEvent {
		t.Helper()
		event, err := events.AppendAllocatedWithDispatch(ctx, userID, domain.UpdateEvent{
			Type:     domain.UpdateEventDialogPinned,
			PtsCount: 1,
			Date:     date,
			Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: userID},
			Bool:     true,
		}, [8]byte{}, 0)
		if err != nil {
			t.Fatalf("append retention dispatch event: %v", err)
		}
		return event
	}
	first := appendDispatch(1)
	second := appendDispatch(2)
	claimed := store.DispatchOutboxItem{TargetUserID: userID, Pts: first.Pts}
	if err := pool.QueryRow(ctx, `
UPDATE dispatch_outbox
SET status = 'dispatching',
    attempts = attempts + 1,
    updated_at = now()
WHERE target_user_id = $1 AND pts = $2
RETURNING id, attempts`, userID, first.Pts).Scan(&claimed.ID, &claimed.Attempts); err != nil {
		t.Fatalf("acquire exact retention dispatch lease: %v", err)
	}
	if err := states.ObserveClientState(ctx, authKeyID, userID, domain.UpdateState{Pts: first.Pts, Date: first.Date}); err != nil {
		t.Fatalf("observe retained dispatch pts: %v", err)
	}
	deleted, err := events.DeleteConfirmedPrefix(ctx, time.Second, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("delete retained dispatch prefix = %d/%v, want 1/nil", deleted, err)
	}

	// The in-flight worker owns an attempts token for a row retention just removed. It must be
	// fenced instead of recreating/marking the deleted head, while the next pts becomes claimable.
	if err := outbox.MarkDelivered(ctx, claimed); !errors.Is(err, store.ErrDispatchLeaseLost) {
		t.Fatalf("deliver retained dispatch lease err = %v, want ErrDispatchLeaseLost", err)
	}
	var eventRows, outboxRows, headPts int
	var headStatus string
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM user_update_events WHERE user_id = $1 AND pts = $2)::int,
  (SELECT count(*) FROM dispatch_outbox WHERE target_user_id = $1 AND pts = $2)::int,
	  (SELECT head_pts FROM dispatch_outbox_user_heads WHERE target_user_id = $1),
  (SELECT status FROM dispatch_outbox_user_heads WHERE target_user_id = $1)`, userID, first.Pts).Scan(&eventRows, &outboxRows, &headPts, &headStatus); err != nil {
		t.Fatalf("load retained dispatch/head state: %v", err)
	}
	if eventRows != 0 || outboxRows != 0 || headPts != second.Pts || headStatus != "pending" {
		t.Fatalf("retained event/outbox/head = %d/%d/%d/%s, want 0/0/%d/pending", eventRows, outboxRows, headPts, headStatus, second.Pts)
	}
}

func randomUpdateRetentionAuthKey(t *testing.T) [8]byte {
	t.Helper()
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatalf("random auth key id: %v", err)
	}
	return id
}
