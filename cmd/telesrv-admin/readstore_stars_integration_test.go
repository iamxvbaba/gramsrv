package main

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The Stars ledger is read with hand-written SQL, so the only thing that can
// prove the joins, the keyset ordering and the search widening are right is
// running them against the real schema. Gated on TELESRV_TEST_POSTGRES_DSN via
// verificationReadStore, like every other read-model integration test.
//
// Usernames and ids are unique per run (verificationReadStore follows the same
// convention), so leftover rows from an earlier run never collide with a fresh
// fixture.

type starsFixture struct {
	alice int64
	bob   int64

	// aliceTxn1 is the newest entry on Alice's ledger, aliceTxn2 the older one.
	aliceTxn1 int64
	aliceTxn2 int64

	alicePhone string
}

func seedStarsFixture(t *testing.T, pool *pgxpool.Pool) starsFixture {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	base := now.UnixNano() & 0x7fffffff

	// The test DB is shared and reused across runs, so start from a clean
	// Stars ledger every time; paid_reactions and premium refund rows reference
	// stars_transactions, hence CASCADE.
	if _, err := pool.Exec(ctx, `TRUNCATE stars_transactions, stars_balances CASCADE`); err != nil {
		t.Fatalf("truncate stars ledger: %v", err)
	}
	alicePhone := strconv.FormatInt(base, 10)
	bobPhone := strconv.FormatInt(base+1, 10)
	drainedPhone := strconv.FormatInt(base+2, 10)
	suffix := strconv.FormatInt(base&0xfffff, 10)
	aliceUser := "alice_" + suffix
	bobUser := "bob_" + suffix
	drainedUser := "drained_" + suffix

	var fx starsFixture
	insert := func(name, username, phone string) int64 {
		base++
		var id int64
		if err := pool.QueryRow(ctx, `
INSERT INTO users (access_hash, phone, first_name, last_name, username, is_bot, verified)
VALUES ($1, $2, $3, '', $4, false, false)
RETURNING id`, base, phone, name, username).Scan(&id); err != nil {
			t.Fatalf("insert user %s: %v", name, err)
		}
		return id
	}
	setBalance := func(userID, balance int64, granted bool) {
		if _, err := pool.Exec(ctx, `
INSERT INTO stars_balances (user_id, balance, granted, updated_at)
VALUES ($1, $2, $3, $4)`, userID, balance, granted, now); err != nil {
			t.Fatalf("insert stars_balances for user %d: %v", userID, err)
		}
	}
	// ledger appends one signed entry and returns its id. date is a unix-seconds
	// integer column, exactly as in deploy/migrations/0009_stars_ledger.up.sql.
	ledger := func(userID, amount int64, reason, title string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
INSERT INTO stars_transactions (user_id, peer_type, peer_id, amount, reason, title, description, date)
VALUES ($1, '', 0, $2, $3, $4, '', $5)
RETURNING id`, userID, amount, reason, title, int64(now.Unix())).Scan(&id); err != nil {
			t.Fatalf("insert stars_transactions for user %d: %v", userID, err)
		}
		return id
	}

	// Alice holds the largest balance with two credits; Bob holds a smaller one
	// with a single credit. "Drained" had stars but spent them down to zero, so
	// it must disappear from the leaderboard yet stay findable by search.
fx.alice = insert("Alice", aliceUser, alicePhone)
	fx.alicePhone = alicePhone
	setBalance(fx.alice, 5000, true)
	// aliceTxn1 is the newest entry on Alice's ledger (the 2000 grant was the
	// latest one), aliceTxn2 the older 3000 grant.
	fx.aliceTxn2 = ledger(fx.alice, 3000, "adjust", "Admin Stars grant")
	fx.aliceTxn1 = ledger(fx.alice, 2000, "adjust", "Admin Stars grant")

	fx.bob = insert("Bob", bobUser, bobPhone)
	setBalance(fx.bob, 2000, false)
	ledger(fx.bob, 2000, "adjust", "Admin Stars grant")

	drained := insert("Drained", drainedUser, drainedPhone)
	setBalance(drained, 0, true)
	ledger(drained, 1000, "adjust", "Admin Stars grant")
	ledger(drained, -1000, "adjust", "Admin Stars debit")
	return fx
}

func TestStarsTopLeaderboardAndSearch(t *testing.T) {
	s, pool := verificationReadStore(t)
	ctx := context.Background()
	fx := seedStarsFixture(t, pool)

	// Empty query is the leaderboard proper: only positive balances, largest first.
	rows, hasMore, err := s.ListStarsTopAccounts(ctx, 0, 50, "")
	if err != nil {
		t.Fatalf("list leaderboard: %v", err)
	}
	if hasMore {
		t.Errorf("leaderboard with 2 rows reported has_more on a limit of 50")
	}
	if len(rows) != 2 || rows[0].UserID != fx.alice || rows[1].UserID != fx.bob {
		t.Fatalf("leaderboard order wrong: got %d rows, first=%d second=%d (want alice=%d then bob=%d)",
			len(rows), firstUserID(rows, 0), firstUserID(rows, 1), fx.alice, fx.bob)
	}
	if rows[0].Balance != 5000 || rows[0].TxnCount != 2 {
		t.Errorf("alice position wrong: balance=%d txn_count=%d (want 5000/2)", rows[0].Balance, rows[0].TxnCount)
}
	if !rows[0].Granted || rows[0].Phone != fx.alicePhone && rows[0].Phone != "" {
		t.Errorf("alice flags wrong: granted=%v phone=%q", rows[0].Granted, rows[0].Phone)
	}

	// Searching widens the pool to every account that ever had a Stars row, so a
	// spent-down balance is still findable -- that is the audit path.
	rows, _, err = s.ListStarsTopAccounts(ctx, 0, 50, "drained")
	if err != nil {
		t.Fatalf("search drained: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("search for drained returned %d rows, want 1", len(rows))
	}
	if rows[0].Balance != 0 {
		t.Errorf("drained balance = %d, want 0 (still findable after spending)", rows[0].Balance)
	}

// Searches by phone and by numeric user id resolve the same account.
	rows, _, err = s.ListStarsTopAccounts(ctx, 0, 50, fx.alicePhone)
	if err != nil {
		t.Fatalf("search by phone: %v", err)
	}
	if len(rows) != 1 || rows[0].UserID != fx.alice {
		t.Fatalf("phone search returned %d rows (first=%d), want alice", len(rows), firstUserID(rows, 0))
	}

	rows, _, err = s.ListStarsTopAccounts(ctx, 0, 50, strconv.FormatInt(fx.bob, 10))
	if err != nil {
		t.Fatalf("search by user id: %v", err)
	}
	if len(rows) != 1 || rows[0].UserID != fx.bob {
		t.Fatalf("user id search returned %d rows (first=%d), want bob", len(rows), firstUserID(rows, 0))
	}
}

func TestStarsLeaderboardKeysetPaging(t *testing.T) {
	s, pool := verificationReadStore(t)
	ctx := context.Background()
	fx := seedStarsFixture(t, pool)

	page, hasMore, err := s.ListStarsTopAccounts(ctx, 0, 1, "")
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if !hasMore || len(page) != 1 || page[0].UserID != fx.alice {
		t.Fatalf("first page wrong: len=%d has_more=%v", len(page), hasMore)
	}

	next, hasMore, err := s.ListStarsTopAccounts(ctx, page[len(page)-1].UserID, 1, "")
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if hasMore || len(next) != 1 || next[0].UserID != fx.bob {
		t.Fatalf("second page wrong: len=%d has_more=%v first=%d (want bob=%d)",
			len(next), hasMore, firstUserID(next, 0), fx.bob)
	}
}

func TestStarsLedgerHistory(t *testing.T) {
	s, pool := verificationReadStore(t)
	ctx := context.Background()
	fx := seedStarsFixture(t, pool)

	account, entries, hasMore, err := s.StarsAccountLedger(ctx, fx.alice, 0, 50)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if account.UserID != fx.alice || account.Balance != 5000 || account.TxnCount != 2 {
		t.Fatalf("account summary wrong: user=%d balance=%d txn_count=%d", account.UserID, account.Balance, account.TxnCount)
	}
	if hasMore || len(entries) != 2 {
		t.Fatalf("ledger page wrong: len=%d has_more=%v", len(entries), hasMore)
	}
	// Newest first: txn1 carries the larger post fix applying order -- the
	// latest admin grant was 2000, so it is the newest entry.
	if entries[0].ID != fx.aliceTxn1 || entries[0].Amount != 2000 {
		t.Errorf("newest entry wrong: id=%d amount=%d (want %d/2000)", entries[0].ID, entries[0].Amount, fx.aliceTxn1)
	}
	if entries[1].ID != fx.aliceTxn2 || entries[1].Amount != 3000 || entries[1].Date <= 0 {
		t.Errorf("oldest entry wrong: id=%d amount=%d date=%d", entries[1].ID, entries[1].Amount, entries[1].Date)
	}

	// Keyset paging walks older entries by id.
	_, one, hasMore, err := s.StarsAccountLedger(ctx, fx.alice, 0, 1)
	if err != nil {
		t.Fatalf("paged ledger: %v", err)
	}
	if !hasMore || len(one) != 1 || one[0].ID != fx.aliceTxn1 {
		t.Fatalf("first page wrong: len=%d has_more=%v", len(one), hasMore)
	}
	_, older, hasMore, err := s.StarsAccountLedger(ctx, fx.alice, one[0].ID, 1)
	if err != nil {
		t.Fatalf("older page: %v", err)
	}
	if hasMore || len(older) != 1 || older[0].ID != fx.aliceTxn2 {
		t.Fatalf("older page wrong: len=%d has_more=%v", len(older), hasMore)
	}
	_, tail, hasMore, err := s.StarsAccountLedger(ctx, fx.alice, older[0].ID, 1)
	if err != nil {
		t.Fatalf("tail page: %v", err)
	}
	if hasMore || len(tail) != 0 {
		t.Fatalf("tail page wrong: len=%d has_more=%v", len(tail), hasMore)
	}

	// An account that never held stars has no ledger to show: 404, not an empty page.
	if _, _, _, err := s.StarsAccountLedger(ctx, 999999999997, 0, 50); !errors.Is(err, errReadNotFound) {
		t.Fatalf("unknown account err = %v, want errReadNotFound", err)
	}
}

func firstUserID(rows []StarsAccountRow, index int) int64 {
	if index >= len(rows) {
		return 0
	}
	return rows[index].UserID
}
