package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/deploy"
	"telesrv/internal/domain"
)

// matureRevenuePasswordVersion backdates only the test fixture while bypassing
// the production credential-change trigger. testPool already requires the
// dedicated migration superuser because 0001 sets session_replication_role.
func matureRevenuePasswordVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64) time.Time {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
		t.Fatalf("disable password trigger for mature fixture: %v", err)
	}
	var changedAt time.Time
	if err := tx.QueryRow(ctx, `UPDATE account_passwords SET password_changed_at=now()-interval '48 hours'
WHERE user_id=$1 RETURNING password_changed_at`, userID).Scan(&changedAt); err != nil {
		t.Fatalf("backdate mature password fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return changedAt
}

func matureRevenueAuthorization(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64) ([8]byte, time.Time) {
	t.Helper()
	keys := NewAuthKeyStore(pool)
	authKeyID := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
	if err := NewAuthorizationStore(pool).Bind(ctx, domain.Authorization{
		AuthKeyID: authKeyID, UserID: userID, Hash: time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("bind mature revenue authorization: %v", err)
	}
	var createdAt time.Time
	if err := pool.QueryRow(ctx, `UPDATE authorizations SET created_at=now()-interval '48 hours'
WHERE auth_key_id=$1 RETURNING created_at`, authKeyIDToInt64(authKeyID)).Scan(&createdAt); err != nil {
		t.Fatalf("backdate mature revenue authorization: %v", err)
	}
	return authKeyID, createdAt
}

func TestChannelRevenueWithdrawalDownRefusesCompletedClaimPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	digest := bytes.Repeat([]byte{0x84}, 32)
	if _, err := pool.Exec(ctx, `DELETE FROM channel_revenue_withdrawals WHERE token_digest=$1`, digest); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM channel_revenue_withdrawals WHERE token_digest=$1`, digest)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO channel_revenue_withdrawals
    (token_digest,channel_id,creator_user_id,currency,amount,status,expires_at,created_at,
     completed_at,channel_transaction_id,user_transaction_id,channel_balance_after,user_balance_after)
VALUES($1,9184001,9184002,'stars',1,'completed',1700000900,1700000000,
       1700000010,9184003,9184004,0,1)`, digest); err != nil {
		t.Fatalf("seed completed withdrawal receipt: %v", err)
	}
	downSQL, err := deploy.Migrations.ReadFile("migrations/0181_channel_revenue_withdrawals.down.sql")
	if err != nil {
		t.Fatalf("read 0179 down: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, downErr := tx.Exec(ctx, string(downSQL))
	_ = tx.Rollback(ctx)
	if downErr == nil || !strings.Contains(downErr.Error(), "cannot downgrade channel revenue withdrawals after a completed claim") {
		t.Fatalf("0179 down error=%v, want completed-claim guard", downErr)
	}
	var receipts int
	if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM channel_revenue_withdrawals WHERE token_digest=$1`, digest).Scan(&receipts); err != nil {
		t.Fatalf("withdrawal receipt table was not preserved: %v", err)
	}
	if receipts != 1 {
		t.Fatalf("completed withdrawal receipts=%d after rejected down, want 1", receipts)
	}
}

func TestChannelRevenueWithdrawalAggregatePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	creator := createTestUser(t, ctx, users, "+1887"+suffix+"01", "RevenueCreator", "")
	other := createTestUser(t, ctx, users, "+1887"+suffix+"02", "RevenueOther", "")
	created, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator.ID, Title: "Revenue " + suffix, Broadcast: true, Date: now,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	lifecycle := NewStarGiftLifecycleStore(pool, nil, 0)
	if _, err := pool.Exec(ctx, `INSERT INTO account_passwords(user_id,has_password) VALUES($1,true)`, creator.ID); err != nil {
		t.Fatalf("create revenue 2FA state: %v", err)
	}
	passwordChangedAt := matureRevenuePasswordVersion(t, ctx, pool, creator.ID)
	creatorAuthKeyID, creatorAuthCreatedAt := matureRevenueAuthorization(t, ctx, pool, creator.ID)
	otherAuthKeyID, otherAuthCreatedAt := matureRevenueAuthorization(t, ctx, pool, other.ID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM channel_revenue_withdrawals WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(ctx, `DELETE FROM channel_stars_transactions WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(ctx, `DELETE FROM channel_stars_balances WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(ctx, `DELETE FROM channel_ton_transactions WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(ctx, `DELETE FROM channel_ton_balances WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(ctx, `DELETE FROM stars_transactions WHERE user_id=$1`, creator.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM stars_balances WHERE user_id=$1`, creator.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM ton_transactions WHERE user_id=$1`, creator.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM ton_balances WHERE user_id=$1`, creator.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE id=$1`, channelID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::bigint[])`, []int64{creator.ID, other.ID})
	})
	stalePasswordChangedAt := passwordChangedAt
	if _, err := pool.Exec(ctx, `UPDATE account_passwords SET srp_verifier=$2 WHERE user_id=$1`, creator.ID, []byte{1}); err != nil {
		t.Fatalf("change revenue password version: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT password_changed_at FROM account_passwords WHERE user_id=$1`, creator.ID).Scan(&passwordChangedAt); err != nil {
		t.Fatalf("read changed revenue 2FA version: %v", err)
	}
	if passwordChangedAt.Equal(stalePasswordChangedAt) {
		t.Fatal("password_changed_at did not advance after verifier change")
	}
	var passwordStateChanged *domain.ChannelRevenuePasswordStateChangedError
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 1, PasswordChangedAt: stalePasswordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: bytes.Repeat([]byte{0x30}, 32),
		Date: now, ExpiresAt: now + 900,
	}); !errors.As(err, &passwordStateChanged) || !passwordStateChanged.HasPassword ||
		!passwordStateChanged.PasswordChangedAt.Equal(passwordChangedAt) {
		t.Fatalf("stale password admission err=%v detail=%+v", err, passwordStateChanged)
	}
	passwordChangedAt = matureRevenuePasswordVersion(t, ctx, pool, creator.ID)
	if _, err := pool.Exec(ctx, `INSERT INTO channel_stars_balances(channel_id,balance) VALUES($1,100)`, channelID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO channel_ton_balances(channel_id,balance_nanoton) VALUES($1,900)`, channelID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO channel_stars_transactions
(channel_id,actor_user_id,amount,reason,date) VALUES($1,$2,100,'gift',$3)`, channelID, creator.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO channel_ton_transactions
(channel_id,actor_user_id,amount_nanoton,reason,date) VALUES($1,$2,900,'gift',$3)`, channelID, creator.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO stars_balances(user_id,balance,granted) VALUES($1,7,true)`, creator.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ton_balances(user_id,balance_nanoton,granted) VALUES($1,5,true)`, creator.ID); err != nil {
		t.Fatal(err)
	}

	starsDigest := bytes.Repeat([]byte{0x31}, 32)
	issued, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 40, PasswordChangedAt: passwordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: starsDigest, Date: now, ExpiresAt: now + 900,
	})
	if err != nil || issued.Amount != 40 || issued.Status != "pending" {
		t.Fatalf("issue stars withdrawal = %+v err=%v", issued, err)
	}
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: other.ID, Currency: domain.ChannelRevenueStars,
		Amount: 1, PasswordChangedAt: passwordChangedAt, AuthKeyID: otherAuthKeyID,
		AuthorizationCreatedAt: otherAuthCreatedAt, TokenDigest: bytes.Repeat([]byte{0x32}, 32), Date: now, ExpiresAt: now + 900,
	}); !errors.Is(err, domain.ErrChannelRevenueWithdrawalInvalid) {
		t.Fatalf("non-creator issue err=%v, want invalid", err)
	}

	completed, err := lifecycle.CompleteChannelRevenueWithdrawal(ctx, starsDigest, now+1)
	if err != nil || completed.Status != "completed" || completed.ChannelBalanceAfter != 60 || completed.UserBalanceAfter != 47 ||
		completed.ChannelTransactionID <= 0 || completed.UserTransactionID <= 0 {
		t.Fatalf("complete stars withdrawal = %+v err=%v", completed, err)
	}
	replayed, err := lifecycle.CompleteChannelRevenueWithdrawal(ctx, starsDigest, now+2)
	if err != nil || replayed.ChannelTransactionID != completed.ChannelTransactionID || replayed.UserTransactionID != completed.UserTransactionID ||
		replayed.CompletedAt != completed.CompletedAt {
		t.Fatalf("replay stars withdrawal = %+v err=%v, want receipt %+v", replayed, err, completed)
	}
	var channelTxnCount, userTxnCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_stars_transactions WHERE channel_id=$1 AND reason='withdrawal'`, channelID).Scan(&channelTxnCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions WHERE user_id=$1 AND reason='withdrawal'`, creator.ID).Scan(&userTxnCount); err != nil {
		t.Fatal(err)
	}
	if channelTxnCount != 1 || userTxnCount != 1 {
		t.Fatalf("idempotent transaction counts channel=%d user=%d", channelTxnCount, userTxnCount)
	}
	if overall, err := lifecycle.ChannelStarsOverallRevenue(ctx, channelID); err != nil || overall != 100 {
		t.Fatalf("stars overall revenue=%d err=%v, want lifetime 100 after claim", overall, err)
	}

	tonDigest := bytes.Repeat([]byte{0x41}, 32)
	tonIssued, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueTON,
		PasswordChangedAt: passwordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: tonDigest, Date: now, ExpiresAt: now + 900,
	})
	if err != nil || tonIssued.Amount != 900 {
		t.Fatalf("issue full TON withdrawal = %+v err=%v", tonIssued, err)
	}
	tonCompleted, err := lifecycle.CompleteChannelRevenueWithdrawal(ctx, tonDigest, now+1)
	if err != nil || tonCompleted.ChannelBalanceAfter != 0 || tonCompleted.UserBalanceAfter != 905 {
		t.Fatalf("complete TON withdrawal = %+v err=%v", tonCompleted, err)
	}
	if overall, err := lifecycle.ChannelTonOverallRevenue(ctx, channelID); err != nil || overall != 900 {
		t.Fatalf("TON overall revenue=%d err=%v, want lifetime 900 after claim", overall, err)
	}

	// Concurrent confirmation requests serialize on the durable command and all
	// receive the exact same receipt; only one pair of ledger rows is created.
	concurrentDigest := bytes.Repeat([]byte{0x49}, 32)
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 10, PasswordChangedAt: passwordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: concurrentDigest, Date: now, ExpiresAt: now + 900,
	}); err != nil {
		t.Fatalf("issue concurrent case: %v", err)
	}
	const parallel = 8
	results := make(chan domain.ChannelRevenueWithdrawal, parallel)
	errs := make(chan error, parallel)
	var wg sync.WaitGroup
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, completeErr := lifecycle.CompleteChannelRevenueWithdrawal(ctx, concurrentDigest, now+3)
			results <- result
			errs <- completeErr
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for completeErr := range errs {
		if completeErr != nil {
			t.Fatalf("concurrent completion: %v", completeErr)
		}
	}
	var concurrentReceipt domain.ChannelRevenueWithdrawal
	for result := range results {
		if concurrentReceipt.ID == 0 {
			concurrentReceipt = result
		}
		if result.ChannelTransactionID != concurrentReceipt.ChannelTransactionID ||
			result.UserTransactionID != concurrentReceipt.UserTransactionID || result.CompletedAt != concurrentReceipt.CompletedAt {
			t.Fatalf("non-exact concurrent receipt: first=%+v got=%+v", concurrentReceipt, result)
		}
	}
	if concurrentReceipt.ChannelBalanceAfter != 50 || concurrentReceipt.UserBalanceAfter != 57 {
		t.Fatalf("concurrent receipt balances=%+v", concurrentReceipt)
	}

	// A command is not a reservation. If another legitimate debit consumes the
	// available balance before confirmation, completion fails with no partial
	// creator credit or withdrawal ledger row.
	insufficientDigest := bytes.Repeat([]byte{0x4a}, 32)
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 40, PasswordChangedAt: passwordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: insufficientDigest, Date: now, ExpiresAt: now + 900,
	}); err != nil {
		t.Fatalf("issue insufficient race case: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE channel_stars_balances SET balance=5 WHERE channel_id=$1`, channelID); err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO channel_stars_transactions
(channel_id,actor_user_id,amount,reason,date) VALUES($1,$2,-45,'adjust',$3)`, channelID, creator.ID, now+4)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.CompleteChannelRevenueWithdrawal(ctx, insufficientDigest, now+5); !errors.Is(err, domain.ErrChannelRevenueInsufficient) {
		t.Fatalf("balance-race completion err=%v, want insufficient", err)
	}
	var starsBalance, personalBalance, withdrawals int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, channelID).Scan(&starsBalance); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, creator.ID).Scan(&personalBalance); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_stars_transactions WHERE channel_id=$1 AND reason='withdrawal'`, channelID).Scan(&withdrawals); err != nil {
		t.Fatal(err)
	}
	pending, found, err := lifecycle.ResolveChannelRevenueWithdrawal(ctx, insufficientDigest)
	if err != nil || !found || pending.Status != "pending" || starsBalance != 5 || personalBalance != 57 || withdrawals != 2 {
		t.Fatalf("insufficient rollback pending=%+v found=%v balances=%d/%d withdrawals=%d err=%v",
			pending, found, starsBalance, personalBalance, withdrawals, err)
	}

	expiredDigest := bytes.Repeat([]byte{0x51}, 32)
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 1, PasswordChangedAt: passwordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: expiredDigest, Date: now, ExpiresAt: now + 1,
	}); err != nil {
		t.Fatalf("issue expiry case: %v", err)
	}
	if _, err := lifecycle.CompleteChannelRevenueWithdrawal(ctx, expiredDigest, now+1); !errors.Is(err, domain.ErrChannelRevenueWithdrawalExpired) {
		t.Fatalf("expired completion err=%v", err)
	}

	ownershipDigest := bytes.Repeat([]byte{0x52}, 32)
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 2, PasswordChangedAt: passwordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: ownershipDigest, Date: now, ExpiresAt: now + 900,
	}); err != nil {
		t.Fatalf("issue ownership-transfer case: %v", err)
	}
	if _, found, err := lifecycle.ResolveChannelRevenueWithdrawal(ctx, expiredDigest); err != nil || found {
		t.Fatalf("replacement retained stale pending token found=%v err=%v", found, err)
	}
	var liveStarCommands int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_revenue_withdrawals
WHERE channel_id=$1 AND currency='stars' AND status='pending'`, channelID).Scan(&liveStarCommands); err != nil || liveStarCommands != 1 {
		t.Fatalf("live stars commands=%d err=%v, want bounded one", liveStarCommands, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE channels SET creator_user_id=$2 WHERE id=$1`, channelID, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.CompleteChannelRevenueWithdrawal(ctx, ownershipDigest, now+6); !errors.Is(err, domain.ErrChannelRevenueWithdrawalInvalid) {
		t.Fatalf("former creator completion err=%v, want invalid", err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, channelID).Scan(&starsBalance); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, creator.ID).Scan(&personalBalance); err != nil {
		t.Fatal(err)
	}
	if starsBalance != 5 || personalBalance != 57 {
		t.Fatalf("ownership transfer moved funds: channel=%d former_creator=%d", starsBalance, personalBalance)
	}
	if _, err := pool.Exec(ctx, `UPDATE channels SET creator_user_id=$2 WHERE id=$1`, channelID, creator.ID); err != nil {
		t.Fatal(err)
	}

	// The durable creator id alone is insufficient: a creator who has left or
	// been demoted cannot issue or complete a claim through a stale projection.
	if _, err := pool.Exec(ctx, `UPDATE channel_members SET status='left' WHERE channel_id=$1 AND user_id=$2`, channelID, creator.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 1, PasswordChangedAt: passwordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: bytes.Repeat([]byte{0x53}, 32), Date: now, ExpiresAt: now + 900,
	}); !errors.Is(err, domain.ErrChannelRevenueWithdrawalInvalid) {
		t.Fatalf("inactive creator member issue err=%v, want invalid", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE channel_members SET status='active' WHERE channel_id=$1 AND user_id=$2`, channelID, creator.ID); err != nil {
		t.Fatal(err)
	}

	// A token issued while the creator is active must not credit a tombstoned
	// personal ledger after account deletion. Logical deletion deliberately
	// keeps creator_user_id and memberships, so the user lifecycle row is the
	// authoritative gate.
	deletedDigest := bytes.Repeat([]byte{0x54}, 32)
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 2, PasswordChangedAt: passwordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: deletedDigest, Date: now, ExpiresAt: now + 900,
	}); err != nil {
		t.Fatalf("issue pre-deletion claim: %v", err)
	}
	deletion, err := NewAccountLifecycleStore(pool).ExecuteAccountDeletion(ctx, creator.ID, domain.AccountDeletionManual,
		"revenue withdrawal test", time.Unix(int64(now+7), 0).UTC())
	if err != nil || !deletion.Changed {
		t.Fatalf("delete creator = %+v err=%v", deletion, err)
	}
	if _, err := lifecycle.CompleteChannelRevenueWithdrawal(ctx, deletedDigest, now+8); !errors.Is(err, domain.ErrChannelRevenueWithdrawalInvalid) {
		t.Fatalf("deleted creator completion err=%v, want invalid", err)
	}
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 1, PasswordChangedAt: passwordChangedAt, AuthKeyID: creatorAuthKeyID,
		AuthorizationCreatedAt: creatorAuthCreatedAt, TokenDigest: bytes.Repeat([]byte{0x55}, 32), Date: now + 8, ExpiresAt: now + 900,
	}); !errors.Is(err, domain.ErrChannelRevenueWithdrawalInvalid) {
		t.Fatalf("deleted creator issue err=%v, want invalid", err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, channelID).Scan(&starsBalance); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, creator.ID).Scan(&personalBalance); err != nil {
		t.Fatal(err)
	}
	deletedPending, found, err := lifecycle.ResolveChannelRevenueWithdrawal(ctx, deletedDigest)
	if err != nil || !found || deletedPending.Status != "pending" || starsBalance != 5 || personalBalance != 57 {
		t.Fatalf("deleted creator moved funds: pending=%+v found=%v balances=%d/%d err=%v",
			deletedPending, found, starsBalance, personalBalance, err)
	}
}

func TestChannelRevenueWithdrawalSerializesWithAccountDeletionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	creator := createTestUser(t, ctx, users, "+1888"+suffix+"01", "RevenueRace", "")
	created, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator.ID, Title: "Revenue race " + suffix, Broadcast: true, Date: now,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	lifecycle := NewStarGiftLifecycleStore(pool, nil, 0)
	if _, err := pool.Exec(ctx, `INSERT INTO account_passwords(user_id,has_password) VALUES($1,true)`, creator.ID); err != nil {
		t.Fatalf("create revenue race 2FA state: %v", err)
	}
	passwordChangedAt := matureRevenuePasswordVersion(t, ctx, pool, creator.ID)
	authKeyID, authorizationCreatedAt := matureRevenueAuthorization(t, ctx, pool, creator.ID)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channel_revenue_withdrawals WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channel_stars_transactions WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channel_stars_balances WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM stars_transactions WHERE user_id=$1`, creator.ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM stars_balances WHERE user_id=$1`, creator.ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channels WHERE id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, creator.ID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO channel_stars_balances(channel_id,balance) VALUES($1,10)`, channelID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO channel_stars_transactions
(channel_id,actor_user_id,amount,reason,date) VALUES($1,$2,10,'gift',$3)`, channelID, creator.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO stars_balances(user_id,balance,granted) VALUES($1,0,true)`, creator.ID); err != nil {
		t.Fatal(err)
	}
	digest := bytes.Repeat([]byte{0x61}, 32)
	if _, err := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 10, PasswordChangedAt: passwordChangedAt, AuthKeyID: authKeyID,
		AuthorizationCreatedAt: authorizationCreatedAt, TokenDigest: digest, Date: now, ExpiresAt: now + 900,
	}); err != nil {
		t.Fatalf("issue race claim: %v", err)
	}

	type completionResult struct {
		value domain.ChannelRevenueWithdrawal
		err   error
	}
	type deletionResult struct {
		value domain.AccountDeletionResult
		err   error
	}
	start := make(chan struct{})
	completed := make(chan completionResult, 1)
	deleted := make(chan deletionResult, 1)
	go func() {
		<-start
		value, completeErr := lifecycle.CompleteChannelRevenueWithdrawal(ctx, digest, now+1)
		completed <- completionResult{value: value, err: completeErr}
	}()
	go func() {
		<-start
		value, deleteErr := NewAccountLifecycleStore(pool).ExecuteAccountDeletion(ctx, creator.ID,
			domain.AccountDeletionManual, "concurrent revenue claim", time.Unix(int64(now+1), 0).UTC())
		deleted <- deletionResult{value: value, err: deleteErr}
	}()
	close(start)
	completion := <-completed
	deletion := <-deleted
	if deletion.err != nil || !deletion.value.Changed {
		t.Fatalf("concurrent deletion = %+v err=%v", deletion.value, deletion.err)
	}
	if completion.err != nil && !errors.Is(completion.err, domain.ErrChannelRevenueWithdrawalInvalid) {
		t.Fatalf("concurrent completion err=%v", completion.err)
	}

	var channelBalance, userBalance, channelWithdrawals, userWithdrawals int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, channelID).Scan(&channelBalance); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, creator.ID).Scan(&userBalance); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_stars_transactions WHERE channel_id=$1 AND reason='withdrawal'`, channelID).Scan(&channelWithdrawals); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions WHERE user_id=$1 AND reason='withdrawal'`, creator.ID).Scan(&userWithdrawals); err != nil {
		t.Fatal(err)
	}
	stored, found, err := lifecycle.ResolveChannelRevenueWithdrawal(ctx, digest)
	if err != nil || !found {
		t.Fatalf("resolve raced command found=%v err=%v", found, err)
	}
	if completion.err == nil {
		// Completion acquired the active-user lock first and linearized before
		// deletion; deletion then tombstoned the already-credited account.
		if stored.Status != "completed" || channelBalance != 0 || userBalance != 10 ||
			channelWithdrawals != 1 || userWithdrawals != 1 {
			t.Fatalf("completion-first race stored=%+v balances=%d/%d ledgers=%d/%d", stored,
				channelBalance, userBalance, channelWithdrawals, userWithdrawals)
		}
	} else if stored.Status != "pending" || channelBalance != 10 || userBalance != 0 ||
		channelWithdrawals != 0 || userWithdrawals != 0 {
		t.Fatalf("deletion-first race stored=%+v balances=%d/%d ledgers=%d/%d", stored,
			channelBalance, userBalance, channelWithdrawals, userWithdrawals)
	}
}

func TestChannelRevenueWithdrawalSerializesWithPasswordChangePostgres(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	creator := createTestUser(t, ctx, NewUserStore(pool), "+1889"+suffix+"01", "RevenuePasswordRace", "")
	created, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator.ID, Title: "Revenue password race " + suffix, Broadcast: true, Date: now,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	lifecycle := NewStarGiftLifecycleStore(pool, nil, 0)
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channel_revenue_withdrawals WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channel_stars_balances WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channels WHERE id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=$1`, creator.ID)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO account_passwords(user_id,has_password) VALUES($1,true)`, creator.ID); err != nil {
		t.Fatalf("create 2FA state: %v", err)
	}
	expectedPasswordChangedAt := matureRevenuePasswordVersion(t, ctx, pool, creator.ID)
	authKeyID, authorizationCreatedAt := matureRevenueAuthorization(t, ctx, pool, creator.ID)
	if _, err := pool.Exec(ctx, `INSERT INTO channel_stars_balances(channel_id,balance) VALUES($1,10)`, channelID); err != nil {
		t.Fatal(err)
	}

	digest := bytes.Repeat([]byte{0x71}, 32)
	type issueResult struct {
		value domain.ChannelRevenueWithdrawal
		err   error
	}
	start := make(chan struct{})
	issued := make(chan issueResult, 1)
	passwordChanged := make(chan error, 1)
	go func() {
		<-start
		value, issueErr := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
			ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
			Amount: 1, PasswordChangedAt: expectedPasswordChangedAt, AuthKeyID: authKeyID,
			AuthorizationCreatedAt: authorizationCreatedAt, TokenDigest: digest,
			Date: now, ExpiresAt: now + 900,
		})
		issued <- issueResult{value: value, err: issueErr}
	}()
	go func() {
		<-start
		_, changeErr := pool.Exec(ctx, `UPDATE account_passwords SET srp_verifier=$2 WHERE user_id=$1`, creator.ID, []byte{2})
		passwordChanged <- changeErr
	}()
	close(start)
	issue := <-issued
	if err := <-passwordChanged; err != nil {
		t.Fatalf("concurrent password change: %v", err)
	}
	var currentPasswordChangedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT password_changed_at FROM account_passwords WHERE user_id=$1`, creator.ID).
		Scan(&currentPasswordChangedAt); err != nil {
		t.Fatal(err)
	}
	if currentPasswordChangedAt.Equal(expectedPasswordChangedAt) {
		t.Fatal("concurrent credential update did not advance password version")
	}
	var commands int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_revenue_withdrawals WHERE token_digest=$1`, digest).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if issue.err == nil {
		// Issuance held the password row first and committed the command before
		// the change; the update then advanced the password version.
		if issue.value.Status != "pending" || commands != 1 {
			t.Fatalf("issue-first password race value=%+v commands=%d", issue.value, commands)
		}
		return
	}
	var stateChanged *domain.ChannelRevenuePasswordStateChangedError
	if !errors.As(issue.err, &stateChanged) || commands != 0 || !stateChanged.HasPassword ||
		!stateChanged.PasswordChangedAt.Equal(currentPasswordChangedAt) {
		t.Fatalf("password-first race issue=%v detail=%+v commands=%d current=%v", issue.err,
			stateChanged, commands, currentPasswordChangedAt)
	}
}

func TestChannelRevenueWithdrawalBindsFullyAuthorizedSessionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	creator := createTestUser(t, ctx, users, "+1890"+suffix+"01", "RevenueSession", "")
	other := createTestUser(t, ctx, users, "+1890"+suffix+"02", "RevenueSessionOther", "")
	created, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator.ID, Title: "Revenue session " + suffix, Broadcast: true, Date: now,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	lifecycle := NewStarGiftLifecycleStore(pool, nil, 0)
	if _, err := pool.Exec(ctx, `INSERT INTO account_passwords(user_id,has_password) VALUES($1,true)`, creator.ID); err != nil {
		t.Fatalf("create session-race 2FA state: %v", err)
	}
	passwordChangedAt := matureRevenuePasswordVersion(t, ctx, pool, creator.ID)
	if _, err := pool.Exec(ctx, `INSERT INTO channel_stars_balances(channel_id,balance) VALUES($1,10)`, channelID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channel_revenue_withdrawals WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channel_stars_balances WHERE channel_id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM channels WHERE id=$1`, channelID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id=ANY($1::bigint[])`, []int64{creator.ID, other.ID})
	})

	auths := NewAuthorizationStore(pool)
	keys := NewAuthKeyStore(pool)
	pendingKey := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
	if err := auths.Bind(ctx, domain.Authorization{
		AuthKeyID: pendingKey, UserID: creator.ID, Hash: time.Now().UnixNano(), PasswordPending: true,
	}); err != nil {
		t.Fatalf("bind pending authorization: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE authorizations SET created_at=now()-interval '48 hours'
WHERE auth_key_id=$1`, authKeyIDToInt64(pendingKey)); err != nil {
		t.Fatalf("age pending authorization: %v", err)
	}
	if err := auths.MarkPasswordPassed(ctx, pendingKey, creator.ID); err != nil {
		t.Fatalf("complete pending authorization: %v", err)
	}
	passed, found, err := auths.ByAuthKey(ctx, pendingKey)
	if err != nil || !found || passed.PasswordPending {
		t.Fatalf("completed pending authorization=%+v found=%v err=%v", passed, found, err)
	}
	pendingDigest := bytes.Repeat([]byte{0x81}, 32)
	_, err = lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
		ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
		Amount: 1, PasswordChangedAt: passwordChangedAt, AuthKeyID: pendingKey,
		AuthorizationCreatedAt: passed.CreatedAt, TokenDigest: pendingDigest, Date: now, ExpiresAt: now + 900,
	})
	var freshSession *domain.ChannelRevenueAuthorizationStateChangedError
	if !errors.As(err, &freshSession) || !freshSession.HasAuthorization || !freshSession.OwnerMatches ||
		freshSession.PasswordPending || !freshSession.CreatedAt.Equal(passed.CreatedAt) {
		t.Fatalf("post-2FA immediate issue err=%v detail=%+v, want fresh-session rejection", err, freshSession)
	}
	var commands int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_revenue_withdrawals WHERE token_digest=$1`, pendingDigest).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if commands != 0 {
		t.Fatalf("fresh post-2FA session persisted %d withdrawal commands", commands)
	}

	// A rebind racing durable issuance has two legal serializations: issuance
	// commits under the still-current mature session, or issuance observes the
	// newly bound owner/timestamp and fails without leaving a bearer command.
	matureKey, matureCreatedAt := matureRevenueAuthorization(t, ctx, pool, creator.ID)
	raceDigest := bytes.Repeat([]byte{0x82}, 32)
	type issueResult struct {
		value domain.ChannelRevenueWithdrawal
		err   error
	}
	start := make(chan struct{})
	issued := make(chan issueResult, 1)
	rebound := make(chan error, 1)
	go func() {
		<-start
		value, issueErr := lifecycle.IssueChannelRevenueWithdrawal(ctx, domain.ChannelRevenueWithdrawalRequest{
			ChannelID: channelID, CreatorUserID: creator.ID, Currency: domain.ChannelRevenueStars,
			Amount: 1, PasswordChangedAt: passwordChangedAt, AuthKeyID: matureKey,
			AuthorizationCreatedAt: matureCreatedAt, TokenDigest: raceDigest, Date: now, ExpiresAt: now + 900,
		})
		issued <- issueResult{value: value, err: issueErr}
	}()
	go func() {
		<-start
		rebound <- auths.Bind(ctx, domain.Authorization{
			AuthKeyID: matureKey, UserID: other.ID, Hash: time.Now().UnixNano(),
		})
	}()
	close(start)
	issue := <-issued
	if err := <-rebound; err != nil {
		t.Fatalf("concurrent authorization rebind: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_revenue_withdrawals WHERE token_digest=$1`, raceDigest).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if issue.err == nil {
		if issue.value.Status != "pending" || commands != 1 {
			t.Fatalf("issue-first authorization race value=%+v commands=%d", issue.value, commands)
		}
	} else {
		var changed *domain.ChannelRevenueAuthorizationStateChangedError
		if !errors.As(issue.err, &changed) || commands != 0 || !changed.HasAuthorization || changed.OwnerMatches {
			t.Fatalf("rebind-first authorization race err=%v detail=%+v commands=%d", issue.err, changed, commands)
		}
	}
	current, found, err := auths.ByAuthKey(ctx, matureKey)
	if err != nil || !found || current.UserID != other.ID || !current.CreatedAt.After(matureCreatedAt) {
		t.Fatalf("authorization after rebind=%+v found=%v err=%v", current, found, err)
	}
}
