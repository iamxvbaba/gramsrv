package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestAccountLifecycleScheduleCancelAndTombstonePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := time.Now().UnixNano()
	users := NewUserStore(pool)
	deleted := createTestUser(t, ctx, users, fmt.Sprintf("15571%d", nonce), "Delete", "Me")
	peer := createTestUser(t, ctx, users, fmt.Sprintf("15572%d", nonce), "Keep", "Peer")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM stars_transactions WHERE user_id = ANY($1)`, []int64{deleted.ID, peer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM ton_transactions WHERE user_id = ANY($1)`, []int64{deleted.ID, peer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM stars_balances WHERE user_id = ANY($1)`, []int64{deleted.ID, peer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM ton_balances WHERE user_id = ANY($1)`, []int64{deleted.ID, peer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM account_deletion_notifications WHERE target_user_id = ANY($1) OR deleted_user_id = ANY($1)`, []int64{deleted.ID, peer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM account_deletion_requests WHERE user_id = ANY($1)`, []int64{deleted.ID, peer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM private_messages WHERE sender_user_id = ANY($1) OR recipient_user_id = ANY($1)`, []int64{deleted.ID, peer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int64{deleted.ID, peer.ID})
	})

	authOne := saveLifecycleTestAuthorization(t, ctx, pool, deleted.ID, 1)
	authTwo := saveLifecycleTestAuthorization(t, ctx, pool, deleted.ID, 2)
	if _, err := pool.Exec(ctx, `INSERT INTO contacts
(user_id, contact_user_id, contact_phone, contact_first_name, contact_last_name)
VALUES ($1, $2, 'stale-phone', 'Stale', 'Alias')`, peer.ID, deleted.ID); err != nil {
		t.Fatalf("insert reverse contact: %v", err)
	}
	if _, err := NewMessageStore(pool).SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: deleted.ID, RecipientUserID: peer.ID, RandomID: nonce, Message: "keep shared history",
	}); err != nil {
		t.Fatalf("send shared message: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_settings (user_id, account_ttl_days) VALUES ($1, 30)`, deleted.ID); err != nil {
		t.Fatalf("insert account settings: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO stars_balances (user_id, balance) VALUES ($1, 50)`, deleted.ID); err != nil {
		t.Fatalf("insert stars balance: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ton_balances (user_id, balance_nanoton) VALUES ($1, 100)`, deleted.ID); err != nil {
		t.Fatalf("insert TON balance: %v", err)
	}

	lifecycle := NewAccountLifecycleStore(pool)
	now := time.Now().UTC().Truncate(time.Second)
	digestOne := sha256.Sum256([]byte("confirm-one"))
	pending, created, err := lifecycle.ScheduleAccountDeletion(ctx, domain.ScheduleAccountDeletion{
		UserID: deleted.ID, RequesterAuthKeyID: authOne, Reason: "Forgot password",
		ConfirmHashDigest: digestOne, ServiceMessage: "tg://confirmphone?phone=hidden&hash=confirm-one",
		RequestedAt: now, ExecuteAt: now.Add(7 * 24 * time.Hour),
	})
	if err != nil || !created || pending.UserID != deleted.ID {
		t.Fatalf("schedule deletion = %+v created=%v err=%v", pending, created, err)
	}
	if got, found, err := lifecycle.PendingAccountDeletionByHash(ctx, deleted.ID, digestOne); err != nil || !found || got.ID != pending.ID {
		t.Fatalf("pending deletion by hash = %+v found=%v err=%v", got, found, err)
	}
	revoked, err := lifecycle.CancelAccountDeletion(ctx, deleted.ID, digestOne, now.Add(time.Minute))
	if err != nil || len(revoked) != 1 || revoked[0].AuthKeyID != authOne {
		t.Fatalf("cancel deletion revoked=%+v err=%v", revoked, err)
	}
	if _, found, err := NewAuthKeyStore(pool).Get(ctx, authOne); err != nil || found {
		t.Fatalf("requester auth key after cancel found=%v err=%v, want revoked", found, err)
	}
	if _, found, err := NewAuthKeyStore(pool).Get(ctx, authTwo); err != nil || !found {
		t.Fatalf("other auth key after cancel found=%v err=%v, want retained", found, err)
	}

	result, err := lifecycle.ExecuteAccountDeletion(ctx, deleted.ID, domain.AccountDeletionManual, "manual", now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("execute account deletion: %v", err)
	}
	if !result.Changed || !result.User.Deleted || result.User.Phone != "" || result.User.FirstName != "" || len(result.RevokedAuthorizations) != 1 {
		t.Fatalf("deletion result = %+v", result)
	}
	if _, found, err := users.ByPhone(ctx, deleted.Phone); err != nil || found {
		t.Fatalf("released phone found=%v err=%v", found, err)
	}
	if tombstone, found, err := users.ByID(ctx, deleted.ID); err != nil || !found || !tombstone.Deleted || tombstone.FirstName != "" {
		t.Fatalf("tombstone = %+v found=%v err=%v", tombstone, found, err)
	}
	if _, err := users.UpdateProfile(ctx, deleted.ID, "Resurrected", "", ""); err == nil {
		t.Fatal("deleted account profile mutation unexpectedly succeeded")
	}
	history, err := NewMessageStore(pool).ListByUser(ctx, peer.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: deleted.ID},
		Limit:   10,
	})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].Body != "keep shared history" || history.Messages[0].From.ID != deleted.ID {
		t.Fatalf("peer history after deletion = %+v err=%v", history, err)
	}
	var peerBoxes, settings, contacts, notifications int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_boxes WHERE owner_user_id = $1 AND from_user_id = $2`, peer.ID, deleted.ID).Scan(&peerBoxes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM account_settings WHERE user_id = $1`, deleted.ID).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM contacts WHERE user_id = $1 OR contact_user_id = $1`, deleted.ID).Scan(&contacts); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM account_deletion_notifications WHERE target_user_id = $1 AND deleted_user_id = $2`, peer.ID, deleted.ID).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if peerBoxes != 1 || settings != 0 || contacts != 0 || notifications != 1 {
		t.Fatalf("post-delete state peerBoxes=%d settings=%d contacts=%d notifications=%d", peerBoxes, settings, contacts, notifications)
	}
	var stars, ton, starClear, tonClear int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id = $1`, deleted.ID).Scan(&stars); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance_nanoton FROM ton_balances WHERE user_id = $1`, deleted.ID).Scan(&ton); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(amount), 0) FROM stars_transactions WHERE user_id = $1 AND reason = 'account_deleted'`, deleted.ID).Scan(&starClear); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(amount_nanoton), 0) FROM ton_transactions WHERE user_id = $1 AND reason = 'account_deleted'`, deleted.ID).Scan(&tonClear); err != nil {
		t.Fatal(err)
	}
	if stars != 0 || ton != 0 || starClear != -50 || tonClear != -100 {
		t.Fatalf("financial clearing stars=%d ton=%d star_tx=%d ton_tx=%d", stars, ton, starClear, tonClear)
	}
}

func TestAccountLifecycleDueSourcesAndTTLWatermarkPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := time.Now().UnixNano()
	users := NewUserStore(pool)
	ttlUser := createTestUser(t, ctx, users, fmt.Sprintf("15671%d", nonce), "TTL", "User")
	freezeUser := createTestUser(t, ctx, users, fmt.Sprintf("15672%d", nonce), "Frozen", "User")
	pendingUser := createTestUser(t, ctx, users, fmt.Sprintf("15673%d", nonce), "Pending", "User")
	ids := []int64{ttlUser.ID, freezeUser.ID, pendingUser.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM account_deletion_notifications WHERE target_user_id = ANY($1) OR deleted_user_id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM account_deletion_requests WHERE user_id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, ids)
	})
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE users SET account_delete_at = $2 WHERE id = $1`, ttlUser.ID, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_restrictions
(user_id, frozen, reason, actor, command_id, frozen_since, frozen_until, appeal_url)
VALUES ($1, true, 'abuse', 'test', 'freeze-test', $2, $3, 'https://example.test/appeal')`, freezeUser.ID, now.Add(-time.Hour), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("due-pending"))
	if _, err := pool.Exec(ctx, `INSERT INTO account_deletion_requests
(user_id, requester_auth_key_id, reason, confirm_hash_digest, requested_at, execute_at)
VALUES ($1, 123, 'forgot', $2, $3, $4)`, pendingUser.ID, digest[:], now.Add(-8*24*time.Hour), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	candidates, err := NewAccountLifecycleStore(pool).DueAccountDeletions(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	sources := make(map[int64]domain.AccountDeletionSource, len(candidates))
	for _, candidate := range candidates {
		sources[candidate.UserID] = candidate.Source
	}
	if sources[ttlUser.ID] != domain.AccountDeletionAccountTTL || sources[freezeUser.ID] != domain.AccountDeletionFreezeExpiry || sources[pendingUser.ID] != domain.AccountDeletionPasswordResetExpiry {
		t.Fatalf("due sources = %+v", sources)
	}
	seen := now.Add(time.Hour)
	if err := users.UpdateLastSeen(ctx, ttlUser.ID, int(seen.Unix())); err != nil {
		t.Fatal(err)
	}
	lifecycle := NewAccountLifecycleStore(pool)
	if stale, err := lifecycle.ExecuteAccountDeletion(ctx, ttlUser.ID, domain.AccountDeletionAccountTTL, "", now); err != nil || stale.Changed {
		t.Fatalf("stale TTL candidate changed=%v err=%v", stale.Changed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE account_restrictions SET frozen_until = $2, updated_at = $3 WHERE user_id = $1`, freezeUser.ID, now.Add(24*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if stale, err := lifecycle.ExecuteAccountDeletion(ctx, freezeUser.ID, domain.AccountDeletionFreezeExpiry, "", now); err != nil || stale.Changed {
		t.Fatalf("extended freeze candidate changed=%v err=%v", stale.Changed, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE account_deletion_requests SET state = 'cancelled', completed_at = $2, updated_at = $2 WHERE user_id = $1`, pendingUser.ID, now); err != nil {
		t.Fatal(err)
	}
	if stale, err := lifecycle.ExecuteAccountDeletion(ctx, pendingUser.ID, domain.AccountDeletionPasswordResetExpiry, "", now); err != nil || stale.Changed {
		t.Fatalf("cancelled pending candidate changed=%v err=%v", stale.Changed, err)
	}
	var deadline time.Time
	if err := pool.QueryRow(ctx, `SELECT account_delete_at FROM users WHERE id = $1`, ttlUser.ID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if want := seen.Add(365 * 24 * time.Hour); deadline.Sub(want) > time.Second || want.Sub(deadline) > time.Second {
		t.Fatalf("TTL watermark deadline=%v want=%v", deadline, want)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_settings (user_id, account_ttl_days) VALUES ($1, 30)
ON CONFLICT (user_id) DO UPDATE SET account_ttl_days = EXCLUDED.account_ttl_days`, ttlUser.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT account_delete_at FROM users WHERE id = $1`, ttlUser.ID).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if want := seen.Add(30 * 24 * time.Hour); deadline.Sub(want) > time.Second || want.Sub(deadline) > time.Second {
		t.Fatalf("custom TTL deadline=%v want=%v", deadline, want)
	}
}

func TestAccountPasswordChangedAtIgnoresSRPChallengeRotationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := createTestUser(t, ctx, NewUserStore(pool), fmt.Sprintf("15771%d", time.Now().UnixNano()), "Password", "Clock")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID) })
	if _, err := pool.Exec(ctx, `INSERT INTO account_passwords
(user_id, has_password, current_algo_salt1, current_algo_salt2, current_algo_g, current_algo_p, srp_verifier, srp_id, srp_b)
VALUES ($1, true, '\x01', '\x02', 3, '\x03', '\x04', 10, '\x05')`, user.ID); err != nil {
		t.Fatal(err)
	}
	var initial, afterChallenge, afterPassword time.Time
	if err := pool.QueryRow(ctx, `SELECT password_changed_at FROM account_passwords WHERE user_id = $1`, user.ID).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT pg_sleep(0.02)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE account_passwords SET srp_id = 11, srp_b = '\x06', updated_at = now() WHERE user_id = $1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT password_changed_at FROM account_passwords WHERE user_id = $1`, user.ID).Scan(&afterChallenge); err != nil {
		t.Fatal(err)
	}
	if !afterChallenge.Equal(initial) {
		t.Fatalf("SRP challenge rotation changed password clock: initial=%v after=%v", initial, afterChallenge)
	}
	if _, err := pool.Exec(ctx, `SELECT pg_sleep(0.02)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE account_passwords SET srp_verifier = '\x07', updated_at = now() WHERE user_id = $1`, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT password_changed_at FROM account_passwords WHERE user_id = $1`, user.ID).Scan(&afterPassword); err != nil {
		t.Fatal(err)
	}
	if !afterPassword.After(afterChallenge) {
		t.Fatalf("password verifier change did not advance clock: before=%v after=%v", afterChallenge, afterPassword)
	}
}

func TestTruncateAccountDeletionReasonUTF8(t *testing.T) {
	got := truncateUTF8Bytes(strings.Repeat("界", 400), 1024)
	if !utf8.ValidString(got) || len(got) > 1024 {
		t.Fatalf("truncateUTF8Bytes returned invalid result: valid=%v bytes=%d", utf8.ValidString(got), len(got))
	}
	if got == "" {
		t.Fatal("truncateUTF8Bytes unexpectedly removed the whole reason")
	}
}

func saveLifecycleTestAuthorization(t *testing.T, ctx context.Context, db *pgxpool.Pool, userID int64, marker byte) [8]byte {
	t.Helper()
	var id [8]byte
	var value [256]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	id[0] = marker
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	if err := NewAuthKeyStore(db).Save(ctx, store.AuthKeyData{ID: id, Value: value}); err != nil {
		t.Fatalf("save lifecycle auth key: %v", err)
	}
	if err := NewAuthorizationStore(db).Bind(ctx, domain.Authorization{AuthKeyID: id, UserID: userID, Hash: int64(marker)}); err != nil {
		t.Fatalf("bind lifecycle authorization: %v", err)
	}
	return id
}
