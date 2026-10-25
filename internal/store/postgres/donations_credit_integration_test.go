package postgres

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
)

// TestDonationCreditCommitsBalanceDeliveryPostgres pins the v2 rewrite of the
// donation credit: deposit state, Stars balance, ledger row and the durable
// updateStarsBalance commit together, a failing builder rolls all of it back,
// and a second credit is a no-op.
func TestDonationCreditCommitsBalanceDeliveryPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	donor := createTestUser(t, ctx, users, "+1783"+suffix+"01", "DonationDonor", "")
	donations := NewDonationStore(pool)
	txHash := "0x" + strings.Repeat("d", 56) + suffix
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM donation_deposits WHERE tx_hash=$1`, txHash)
		_, _ = pool.Exec(ctx, `DELETE FROM edge_delivery_outbox WHERE target_user_id=$1`, donor.ID)
	})

	recorded, created, err := donations.RecordDonationDeposit(ctx, domain.DonationDeposit{
		UserID: donor.ID, ChainKey: "sepolia", TxHash: txHash, LogIndex: -1, BlockNumber: 100,
		AmountRaw: "1000000000000000", Status: domain.DonationDepositPending,
	})
	if err != nil || !created {
		t.Fatalf("record deposit = created %v err %v", created, err)
	}
	if _, err := donations.UpdateDonationDepositConfirmations(ctx, "sepolia", txHash, -1, 50); err != nil {
		t.Fatalf("confirm deposit: %v", err)
	}
	balanceOf := func() int64 {
		t.Helper()
		var balance int64
		err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM stars_balances WHERE user_id=$1),0)`, donor.ID).Scan(&balance)
		if err != nil {
			t.Fatalf("read balance: %v", err)
		}
		return balance
	}
	outbox := func() int {
		t.Helper()
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1`, donor.ID).Scan(&count); err != nil {
			t.Fatalf("count outbox: %v", err)
		}
		return count
	}
	before := balanceOf()

	if _, _, err := donations.CreditDonationDeposit(ctx, recorded.ID, 2_000_000, 40, int(time.Now().Unix()), nil); !errors.Is(err, storepkg.ErrDeliveryOutboxRequired) {
		t.Fatalf("nil builder err = %v, want ErrDeliveryOutboxRequired", err)
	}
	injected := errors.New("injected balance delivery failure")
	if _, _, err := donations.CreditDonationDeposit(ctx, recorded.ID, 2_000_000, 40, int(time.Now().Unix()),
		func(domain.StarsBalance) ([]storepkg.DeliveryEffect, error) { return nil, injected }); !errors.Is(err, injected) {
		t.Fatalf("failing builder err = %v, want injected", err)
	}
	if got := balanceOf(); got != before {
		t.Fatalf("balance after rolled back credit = %d, want %d", got, before)
	}

	var delivered domain.StarsBalance
	builder := func(balance domain.StarsBalance) ([]storepkg.DeliveryEffect, error) {
		delivered = balance
		return []storepkg.DeliveryEffect{storepkg.AbsoluteDeliveryEffect(storepkg.DeliveryOutboxEnqueue{
			TargetUserID: balance.UserID, Payload: []byte("donation-balance"),
			RecoveryPolicy: storepkg.OutboxRecoveryAbsoluteReload,
		})}, nil
	}
	credited, ok, err := donations.CreditDonationDeposit(ctx, recorded.ID, 2_000_000, 40, int(time.Now().Unix()), builder)
	if err != nil || !ok || credited.StarsCredited != 40 {
		t.Fatalf("credit = %+v ok %v err %v", credited, ok, err)
	}
	if got := balanceOf(); got != before+40 || delivered.Balance != got || delivered.UserID != donor.ID {
		t.Fatalf("balance = %d delivered %+v, want %d", got, delivered, before+40)
	}
	if got := outbox(); got != 1 {
		t.Fatalf("outbox rows = %d, want 1", got)
	}
	if _, ok, err := donations.CreditDonationDeposit(ctx, recorded.ID, 2_000_000, 40, int(time.Now().Unix()), builder); err != nil || ok {
		t.Fatalf("repeat credit = ok %v err %v, want no-op", ok, err)
	}
	if got := balanceOf(); got != before+40 || outbox() != 1 {
		t.Fatalf("repeat credit moved balance to %d", got)
	}
}

// TestAdvisoryLeaseRunsOneHolderAtATimePostgres pins the donations lease: a
// second runner waits while the first holds the lock and takes over once the
// first gives it up.
func TestAdvisoryLeaseRunsOneHolderAtATimePostgres(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	key := "telesrv-test-lease-" + randomSuffix(t)
	log := zaptest.NewLogger(t)

	var active, maxActive, runs atomic.Int32
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondRan := make(chan struct{})
	job := func(first bool) func(context.Context) error {
		return func(jobCtx context.Context) error {
			now := active.Add(1)
			defer active.Add(-1)
			for {
				prev := maxActive.Load()
				if now <= prev || maxActive.CompareAndSwap(prev, now) {
					break
				}
			}
			runs.Add(1)
			if first {
				close(firstStarted)
				select {
				case <-releaseFirst:
					return nil
				case <-jobCtx.Done():
					return jobCtx.Err()
				}
			}
			close(secondRan)
			<-jobCtx.Done()
			return jobCtx.Err()
		}
	}
	firstCtx, stopFirst := context.WithCancel(ctx)
	go RunWithAdvisoryLease(firstCtx, pool, key, 50*time.Millisecond, log, job(true))
	select {
	case <-firstStarted:
	case <-ctx.Done():
		t.Fatal("first runner never acquired the lease")
	}
	secondCtx, stopSecond := context.WithCancel(ctx)
	defer stopSecond()
	go RunWithAdvisoryLease(secondCtx, pool, key, 50*time.Millisecond, log, job(false))
	time.Sleep(300 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatalf("runs while first holds the lease = %d, want 1", runs.Load())
	}
	stopFirst()
	close(releaseFirst)
	select {
	case <-secondRan:
	case <-ctx.Done():
		t.Fatal("second runner never took over the lease")
	}
	if maxActive.Load() != 1 {
		t.Fatalf("concurrent holders = %d, want 1", maxActive.Load())
	}
}
