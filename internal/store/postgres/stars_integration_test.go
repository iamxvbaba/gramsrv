package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// TestStarsLedgerPostgres regresses migration 0009: the atomic semantics of the local Stars ledger
// against a real PG (idempotent first-read grant / credit / atomic debit / insufficient balance blocked without touching the ledger / keyset pagination with no cursor on the last page).
func TestStarsLedgerPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := NewStarsStore(pool)

	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	u, err := users.Create(ctx, domain.User{AccessHash: 92, Phone: "+1665" + suffix + "01", FirstName: "StarsLedger"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM stars_transactions WHERE user_id = $1", u.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM stars_balances WHERE user_id = $1", u.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", u.ID)
	})

	// Empty account: balance 0, not granted.
	if bal, err := st.GetBalance(ctx, u.ID); err != nil || bal.Balance != 0 || bal.Granted {
		t.Fatalf("empty balance = %+v err %v, want 0 not granted", bal, err)
	}

	// The first read grants once.
	bal, applied, err := st.EnsureGrant(ctx, u.ID, 1000, 1700000000)
	if err != nil || !applied || bal.Balance != 1000 || !bal.Granted {
		t.Fatalf("first grant = %+v applied %v err %v, want 1000 granted applied", bal, applied, err)
	}
	// Granting again is idempotent: no duplication.
	bal, applied, err = st.EnsureGrant(ctx, u.ID, 1000, 1700000001)
	if err != nil || applied || bal.Balance != 1000 {
		t.Fatalf("second grant = %+v applied %v err %v, want 1000 not applied", bal, applied, err)
	}

	// Atomic debit deduction + writing a negative ledger entry.
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 4242}
	bal, err = st.Debit(ctx, u.ID, 300, domain.StarsReasonReaction, peer, 1700000002, "paid reaction", "")
	if err != nil || bal.Balance != 700 {
		t.Fatalf("debit = %+v err %v, want 700", bal, err)
	}

	// Insufficient balance: blocked without touching the ledger (CHECK + FOR UPDATE, double safeguard).
	if _, err := st.Debit(ctx, u.ID, 100000, domain.StarsReasonReaction, peer, 1700000003, "", ""); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("over-debit err = %v, want ErrStarsInsufficient", err)
	}
	if bal, err := st.GetBalance(ctx, u.ID); err != nil || bal.Balance != 700 {
		t.Fatalf("balance after failed debit = %+v err %v, want 700 unchanged", bal, err)
	}

	// Credit.
	bal, err = st.Credit(ctx, u.ID, 50, domain.StarsReasonGift, domain.Peer{Type: domain.PeerTypeUser, ID: 9}, 1700000004, "gift", "")
	if err != nil || bal.Balance != 750 {
		t.Fatalf("credit = %+v err %v, want 750", bal, err)
	}

	// Ledger: grant(+1000) / debit(-300) / credit(+50), 3 entries total, descending with the newest first.
	page, err := st.ListTransactions(ctx, u.ID, domain.StarsTransactionQuery{Limit: 2})
	if err != nil {
		t.Fatalf("list page1: %v", err)
	}
	if len(page.Transactions) != 2 || page.NextOffset == "" {
		t.Fatalf("page1 = %d txns next=%q, want 2 + next", len(page.Transactions), page.NextOffset)
	}
	if page.Transactions[0].Amount != 50 || page.Transactions[0].Reason != domain.StarsReasonGift {
		t.Fatalf("page1[0] = %+v, want +50 gift (newest)", page.Transactions[0])
	}
	if page.Balance != 750 {
		t.Fatalf("page balance = %d, want 750", page.Balance)
	}
	page2, err := st.ListTransactions(ctx, u.ID, domain.StarsTransactionQuery{Offset: page.NextOffset, Limit: 2})
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if len(page2.Transactions) != 1 || page2.NextOffset != "" {
		t.Fatalf("page2 = %d txns next=%q, want 1 + empty (terminal)", len(page2.Transactions), page2.NextOffset)
	}
	if page2.Transactions[0].Reason != domain.StarsReasonGrant || page2.Transactions[0].Amount != 1000 {
		t.Fatalf("page2[0] = %+v, want +1000 grant (oldest)", page2.Transactions[0])
	}

	incoming1, err := st.ListTransactions(ctx, u.ID, domain.StarsTransactionQuery{
		Limit: 1, Direction: domain.StarsTransactionDirectionIncoming,
	})
	if err != nil {
		t.Fatalf("incoming page1: %v", err)
	}
	if len(incoming1.Transactions) != 1 || incoming1.Transactions[0].Amount != 50 || incoming1.NextOffset == "" {
		t.Fatalf("incoming page1 = %+v next=%q, want +50 and next", incoming1.Transactions, incoming1.NextOffset)
	}
	incoming2, err := st.ListTransactions(ctx, u.ID, domain.StarsTransactionQuery{
		Offset: incoming1.NextOffset, Limit: 1, Direction: domain.StarsTransactionDirectionIncoming,
	})
	if err != nil {
		t.Fatalf("incoming page2: %v", err)
	}
	if len(incoming2.Transactions) != 1 || incoming2.Transactions[0].Amount != 1000 || incoming2.NextOffset != "" {
		t.Fatalf("incoming page2 = %+v next=%q, want +1000 terminal", incoming2.Transactions, incoming2.NextOffset)
	}

	outgoing, err := st.ListTransactions(ctx, u.ID, domain.StarsTransactionQuery{
		Limit: 10, Direction: domain.StarsTransactionDirectionOutgoing,
	})
	if err != nil || len(outgoing.Transactions) != 1 || outgoing.Transactions[0].Amount != -300 {
		t.Fatalf("outgoing = %+v err=%v, want only -300", outgoing.Transactions, err)
	}

	ascending, err := st.ListTransactions(ctx, u.ID, domain.StarsTransactionQuery{Limit: 10, Ascending: true})
	if err != nil || len(ascending.Transactions) != 3 {
		t.Fatalf("ascending = %+v err=%v", ascending.Transactions, err)
	}
	wantAscending := []int64{1000, -300, 50}
	for i, amount := range wantAscending {
		if ascending.Transactions[i].Amount != amount {
			t.Fatalf("ascending[%d].amount = %d, want %d", i, ascending.Transactions[i].Amount, amount)
		}
	}
}

func TestStarsDeliveryBuilderFailureRollsBackLedgerPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	user, err := users.Create(ctx, domain.User{AccessHash: 93, Phone: "+1666" + randomSuffix(t), FirstName: "StarsRollback"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM edge_delivery_outbox WHERE target_user_id=$1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM stars_transactions WHERE user_id=$1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM stars_balances WHERE user_id=$1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", user.ID)
	})
	wantErr := errors.New("encode failed")
	stars := NewStarsStore(pool)
	if _, err := stars.CreditWithDelivery(ctx, user.ID, 50, domain.StarsReasonAdjust, domain.Peer{}, 1, "admin", "", func(domain.StarsBalance) ([]store.DeliveryEffect, error) {
		return nil, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("CreditWithDelivery error = %v", err)
	}
	if balance, err := stars.GetBalance(ctx, user.ID); err != nil || balance.Balance != 0 {
		t.Fatalf("balance after rollback = %+v err=%v", balance, err)
	}
	var transactions, deliveries int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM stars_transactions WHERE user_id=$1", user.ID).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1", user.ID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if transactions != 0 || deliveries != 0 {
		t.Fatalf("rollback left transactions=%d deliveries=%d", transactions, deliveries)
	}
}
