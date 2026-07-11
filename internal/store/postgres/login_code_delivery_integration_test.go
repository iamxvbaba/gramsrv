package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestLoginCodeDeliveryPostgresAtomicFactsAndReplay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := createLoginCodeDeliveryTestUser(t, ctx, pool, "basic")
	req := domain.LoginCodeDeliveryRequest{
		UserID:        user.ID,
		PhoneCodeHash: "pg-login-code-basic-" + randomSuffix(t),
		Code:          "12345",
		Date:          1700001000,
		ExpiresAt:     1700001300,
	}

	first, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, req)
	if err != nil {
		t.Fatalf("DeliverLoginCodeMessage: %v", err)
	}
	if !first.Created || first.Message.ID != 1 || first.Message.Pts != 1 || first.Message.UID <= 0 || first.Message.Out ||
		first.Message.OwnerUserID != user.ID || first.Message.Peer.ID != domain.OfficialSystemUserID || first.Message.From.ID != domain.OfficialSystemUserID {
		t.Fatalf("first delivery = %+v, want first incoming 777000 message", first)
	}

	assertLoginCodeDeliveryFacts(t, ctx, pool, user.ID, first.Message, 1)
	var senderUserID, recipientUserID, randomID int64
	var delivered bool
	var senderBoxID, senderPts, recipientBoxID, recipientPts int32
	if err := pool.QueryRow(ctx, `
SELECT sender_user_id,
       recipient_user_id,
       random_id,
       recipient_delivered,
       sender_box_id,
       sender_pts,
       recipient_box_id,
       recipient_pts
FROM private_messages
WHERE sender_user_id = $1 AND id = $2`, domain.OfficialSystemUserID, first.Message.UID).Scan(
		&senderUserID,
		&recipientUserID,
		&randomID,
		&delivered,
		&senderBoxID,
		&senderPts,
		&recipientBoxID,
		&recipientPts,
	); err != nil {
		t.Fatalf("load private message receipt: %v", err)
	}
	if senderUserID != domain.OfficialSystemUserID || recipientUserID != user.ID || randomID != 0 || !delivered ||
		senderBoxID != 0 || senderPts != 0 || int(recipientBoxID) != first.Message.ID || int(recipientPts) != first.Message.Pts {
		t.Fatalf("private receipt sender=%d recipient=%d random=%d delivered=%v sender=%d/%d recipient=%d/%d",
			senderUserID, recipientUserID, randomID, delivered, senderBoxID, senderPts, recipientBoxID, recipientPts)
	}
	var officialSenderBoxes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_boxes WHERE owner_user_id = $1 AND private_message_id = $2`, domain.OfficialSystemUserID, first.Message.UID).Scan(&officialSenderBoxes); err != nil {
		t.Fatalf("count official sender boxes: %v", err)
	}
	if officialSenderBoxes != 0 {
		t.Fatalf("official sender boxes = %d, want recipient-only login notification", officialSenderBoxes)
	}

	var deliveryKey, codeFingerprint []byte
	if err := pool.QueryRow(ctx, `
SELECT delivery_key, code_fingerprint
FROM login_code_message_deliveries
WHERE user_id = $1 AND message_box_id = $2`, user.ID, first.Message.ID).Scan(&deliveryKey, &codeFingerprint); err != nil {
		t.Fatalf("load compact receipt: %v", err)
	}
	if len(deliveryKey) != 32 || len(codeFingerprint) != 32 || string(deliveryKey) == req.PhoneCodeHash {
		t.Fatalf("compact receipt key/fingerprint lengths = %d/%d", len(deliveryKey), len(codeFingerprint))
	}

	replayReq := req
	replayReq.Date += 99
	replay, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, replayReq)
	if err != nil {
		t.Fatalf("replay DeliverLoginCodeMessage: %v", err)
	}
	if replay.Created || !reflect.DeepEqual(replay.Message, first.Message) {
		t.Fatalf("replay = %+v, want immutable first result %+v", replay, first)
	}
	assertLoginCodeDeliveryFacts(t, ctx, pool, user.ID, first.Message, 1)

	changedCode := req
	changedCode.Code = "54321"
	if _, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, changedCode); !errors.Is(err, domain.ErrLoginCodeDeliveryConflict) {
		t.Fatalf("changed-code replay err = %v, want ErrLoginCodeDeliveryConflict", err)
	}
	assertLoginCodeDeliveryFacts(t, ctx, pool, user.ID, first.Message, 1)

	otherUser := createLoginCodeDeliveryTestUser(t, ctx, pool, "conflict")
	changedUser := req
	changedUser.UserID = otherUser.ID
	if _, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, changedUser); !errors.Is(err, domain.ErrLoginCodeDeliveryConflict) {
		t.Fatalf("changed-user replay err = %v, want ErrLoginCodeDeliveryConflict", err)
	}
	var otherFacts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_boxes WHERE owner_user_id = $1`, otherUser.ID).Scan(&otherFacts); err != nil {
		t.Fatalf("count changed-user facts: %v", err)
	}
	if otherFacts != 0 {
		t.Fatalf("changed-user replay created %d message boxes", otherFacts)
	}
}

func TestLoginCodeDeliveryPostgresConcurrentExactlyOnce(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := createLoginCodeDeliveryTestUser(t, ctx, pool, "concurrent")
	req := domain.LoginCodeDeliveryRequest{
		UserID:        user.ID,
		PhoneCodeHash: "pg-login-code-concurrent-" + randomSuffix(t),
		Code:          "24680",
		Date:          1700001100,
		ExpiresAt:     1700001400,
	}

	const workers = 24
	var created atomic.Int32
	results := make(chan domain.LoginCodeDeliveryResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, req)
			if err != nil {
				errs <- err
				return
			}
			if got.Created {
				created.Add(1)
			}
			results <- got
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatalf("concurrent delivery: %v", err)
	}
	if created.Load() != 1 {
		t.Fatalf("created calls = %d, want exactly 1", created.Load())
	}
	var first domain.Message
	for got := range results {
		if first.ID == 0 {
			first = got.Message
			continue
		}
		if !reflect.DeepEqual(got.Message, first) {
			t.Fatalf("concurrent result = %+v, want %+v", got.Message, first)
		}
	}
	if first.ID == 0 {
		t.Fatal("no successful concurrent result")
	}
	assertLoginCodeDeliveryFacts(t, ctx, pool, user.ID, first, 1)
}

func TestLoginCodeDeliveryPostgresCommitAckLossRecoversFromReceipt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := createLoginCodeDeliveryTestUser(t, ctx, pool, "commit-ack-loss")
	req := domain.LoginCodeDeliveryRequest{
		UserID:        user.ID,
		PhoneCodeHash: "pg-login-code-commit-ack-loss-" + randomSuffix(t),
		Code:          "86420",
		Date:          int(time.Now().Unix()),
		ExpiresAt:     time.Now().Add(5 * time.Minute).Unix(),
	}

	got, err := NewMessageStore(&commitAckLossDB{Pool: pool}).DeliverLoginCodeMessage(ctx, req)
	if err != nil {
		t.Fatalf("DeliverLoginCodeMessage with lost commit ACK: %v", err)
	}
	if got.Created {
		t.Fatalf("commit-ACK recovery Created = true, want conservative replay result")
	}
	assertLoginCodeDeliveryFacts(t, ctx, pool, user.ID, got.Message, 1)

	replay, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, req)
	if err != nil {
		t.Fatalf("replay after lost commit ACK: %v", err)
	}
	if replay.Created || !reflect.DeepEqual(replay.Message, got.Message) {
		t.Fatalf("replay = %+v, want recovered snapshot %+v", replay, got)
	}
}

func TestLoginCodeDeliveryPostgresDifferentUsersDoNotRewriteOfficialUser(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	firstUser := createLoginCodeDeliveryTestUser(t, ctx, pool, "official-row-first")
	now := int(time.Now().Unix())
	if _, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, domain.LoginCodeDeliveryRequest{
		UserID: firstUser.ID, PhoneCodeHash: "official-row-first-" + randomSuffix(t), Code: "12345", Date: now, ExpiresAt: int64(now + 300),
	}); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	var xminBefore string
	if err := pool.QueryRow(ctx, `SELECT xmin::text FROM users WHERE id = $1`, domain.OfficialSystemUserID).Scan(&xminBefore); err != nil {
		t.Fatalf("load official user xmin: %v", err)
	}

	const workers = 12
	users := make([]domain.User, workers)
	hashes := make([]string, workers)
	for i := range users {
		users[i] = createLoginCodeDeliveryTestUser(t, ctx, pool, fmt.Sprintf("official-row-%02d", i))
		hashes[i] = fmt.Sprintf("official-row-concurrent-%d-%s", i, randomSuffix(t))
	}
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i, user := range users {
		wg.Add(1)
		go func(i int, user domain.User) {
			defer wg.Done()
			_, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, domain.LoginCodeDeliveryRequest{
				UserID: user.ID, PhoneCodeHash: hashes[i], Code: "12345", Date: now, ExpiresAt: int64(now + 300),
			})
			if err != nil {
				errs <- err
			}
		}(i, user)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("different-user delivery: %v", err)
	}
	var xminAfter string
	if err := pool.QueryRow(ctx, `SELECT xmin::text FROM users WHERE id = $1`, domain.OfficialSystemUserID).Scan(&xminAfter); err != nil {
		t.Fatalf("reload official user xmin: %v", err)
	}
	if xminAfter != xminBefore {
		t.Fatalf("official system user row was rewritten: xmin %s -> %s", xminBefore, xminAfter)
	}
}

func TestLoginCodeDeliveryPostgresReceiptRetentionIsBoundedAndSeekOrdered(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	users := []domain.User{
		createLoginCodeDeliveryTestUser(t, ctx, pool, "expiry-old-1"),
		createLoginCodeDeliveryTestUser(t, ctx, pool, "expiry-old-2"),
		createLoginCodeDeliveryTestUser(t, ctx, pool, "expiry-future"),
	}
	for i, user := range users {
		expiresAt := now.Add(time.Hour).Unix()
		if i < 2 {
			expiresAt = now.Add(time.Duration(i-2) * time.Minute).Unix()
		}
		if _, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, domain.LoginCodeDeliveryRequest{
			UserID: user.ID, PhoneCodeHash: fmt.Sprintf("expiry-%d-%s", i, randomSuffix(t)), Code: "12345", Date: int(now.Add(-time.Hour).Unix()), ExpiresAt: expiresAt,
		}); err != nil {
			t.Fatalf("seed expiry receipt %d: %v", i, err)
		}
	}
	store := NewMessageStore(pool)
	deleted, err := store.DeleteExpiredLoginCodeDeliveries(ctx, now, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("first bounded retention = %d, %v; want 1", deleted, err)
	}
	deleted, err = store.DeleteExpiredLoginCodeDeliveries(ctx, now, 10)
	if err != nil || deleted != 1 {
		t.Fatalf("second bounded retention = %d, %v; want 1", deleted, err)
	}
	var receipts, messages, events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM login_code_message_deliveries WHERE user_id = ANY($1)`, []int64{users[0].ID, users[1].ID, users[2].ID}).Scan(&receipts); err != nil {
		t.Fatalf("count retained receipts: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_boxes WHERE owner_user_id = ANY($1) AND peer_id = $2`, []int64{users[0].ID, users[1].ID, users[2].ID}, domain.OfficialSystemUserID).Scan(&messages); err != nil {
		t.Fatalf("count retained messages: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_update_events WHERE user_id = ANY($1) AND event_type = 'new_message'`, []int64{users[0].ID, users[1].ID, users[2].ID}).Scan(&events); err != nil {
		t.Fatalf("count retained events: %v", err)
	}
	if receipts != 1 || messages != 3 || events != 3 {
		t.Fatalf("after receipt GC receipts/messages/events = %d/%d/%d, want 1/3/3", receipts, messages, events)
	}
}

func TestLoginCodeDeliveryPostgresRollsBackEveryFact(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	user := createLoginCodeDeliveryTestUser(t, ctx, pool, "rollback")
	first, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, domain.LoginCodeDeliveryRequest{
		UserID:        user.ID,
		PhoneCodeHash: "pg-login-code-rollback-first-" + randomSuffix(t),
		Code:          "11111",
		Date:          1700001200,
		ExpiresAt:     1700001500,
	})
	if err != nil {
		t.Fatalf("seed first delivery: %v", err)
	}
	if first.Message.ID != 1 || first.Message.Pts != 1 {
		t.Fatalf("first allocation = id %d pts %d, want 1/1", first.Message.ID, first.Message.Pts)
	}

	failedReq := domain.LoginCodeDeliveryRequest{
		UserID:        user.ID,
		PhoneCodeHash: "pg-login-code-rollback-failed-" + randomSuffix(t),
		Code:          "22222",
		Date:          1700001201,
		ExpiresAt:     1700001501,
	}
	failing := NewMessageStore(pool, WithMessageAllocators(loginCodeFixedBoxAllocator{boxID: first.Message.ID}))
	if _, err := failing.DeliverLoginCodeMessage(ctx, failedReq); err == nil {
		t.Fatal("duplicate box allocator delivery succeeded, want rollback")
	}
	assertLoginCodeDeliveryFacts(t, ctx, pool, user.ID, first.Message, 1)
	failedKey, err := store.LoginCodeDeliveryKey(failedReq.PhoneCodeHash)
	if err != nil {
		t.Fatalf("failed delivery key: %v", err)
	}
	var failedReceipts, failedBodies int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM login_code_message_deliveries WHERE delivery_key = $1`, failedKey[:]).Scan(&failedReceipts); err != nil {
		t.Fatalf("count failed receipts: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM private_messages WHERE sender_user_id = $1 AND recipient_user_id = $2 AND body LIKE '%22222%'`, domain.OfficialSystemUserID, user.ID).Scan(&failedBodies); err != nil {
		t.Fatalf("count failed private messages: %v", err)
	}
	if failedReceipts != 0 || failedBodies != 0 {
		t.Fatalf("failed transaction leaked receipts=%d private_messages=%d", failedReceipts, failedBodies)
	}

	third, err := NewMessageStore(pool).DeliverLoginCodeMessage(ctx, domain.LoginCodeDeliveryRequest{
		UserID:        user.ID,
		PhoneCodeHash: "pg-login-code-rollback-third-" + randomSuffix(t),
		Code:          "33333",
		Date:          1700001202,
		ExpiresAt:     1700001502,
	})
	if err != nil {
		t.Fatalf("delivery after rollback: %v", err)
	}
	if third.Message.ID != 2 || third.Message.Pts != 2 {
		t.Fatalf("allocation after rollback = id %d pts %d, want contiguous 2/2", third.Message.ID, third.Message.Pts)
	}
}

type loginCodeFixedBoxAllocator struct {
	boxID int
}

type commitAckLossDB struct {
	*pgxpool.Pool
}

func (d *commitAckLossDB) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &commitAckLossTx{Tx: tx}, nil
}

type commitAckLossTx struct {
	pgx.Tx
}

func (t *commitAckLossTx) Commit(ctx context.Context) error {
	if err := t.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("synthetic lost commit acknowledgement")
}

func (a loginCodeFixedBoxAllocator) NextBoxID(context.Context, int64) (int, error) {
	return a.boxID, nil
}

func (a loginCodeFixedBoxAllocator) CurrentBoxID(context.Context, int64) (int, error) {
	return a.boxID, nil
}

func createLoginCodeDeliveryTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, label string) domain.User {
	t.Helper()
	user, err := NewUserStore(pool).Create(ctx, domain.User{
		AccessHash: 8100000000,
		Phone:      "+1888" + randomSuffix(t),
		FirstName:  "LoginCode" + label,
	})
	if err != nil {
		t.Fatalf("create login code test user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
	})
	return user
}

func assertLoginCodeDeliveryFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID int64, msg domain.Message, want int) {
	t.Helper()
	queries := []struct {
		name string
		sql  string
		args []any
	}{
		{"private_messages", `SELECT count(*) FROM private_messages WHERE sender_user_id = $1 AND recipient_user_id = $2`, []any{domain.OfficialSystemUserID, userID}},
		{"message_boxes", `SELECT count(*) FROM message_boxes WHERE owner_user_id = $1 AND peer_type = 'user' AND peer_id = $2`, []any{userID, domain.OfficialSystemUserID}},
		{"dialogs", `SELECT count(*) FROM dialogs WHERE user_id = $1 AND peer_type = 'user' AND peer_id = $2`, []any{userID, domain.OfficialSystemUserID}},
		{"user_update_events", `SELECT count(*) FROM user_update_events WHERE user_id = $1 AND event_type = 'new_message'`, []any{userID}},
		{"dispatch_outbox", `SELECT count(*) FROM dispatch_outbox WHERE target_user_id = $1 AND event_type = 'new_message'`, []any{userID}},
		{"delivery_receipts", `SELECT count(*) FROM login_code_message_deliveries WHERE user_id = $1`, []any{userID}},
	}
	for _, query := range queries {
		var got int
		if err := pool.QueryRow(ctx, query.sql, query.args...).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", query.name, err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", query.name, got, want)
		}
	}

	var boxPts, eventPts, eventBoxID, outboxPts int32
	var eventType, outboxEventType string
	if err := pool.QueryRow(ctx, `
SELECT b.pts,
       e.pts,
       e.message_box_id,
       e.event_type,
       o.pts,
       o.event_type
FROM message_boxes b
JOIN user_update_events e
  ON e.user_id = b.owner_user_id
 AND e.message_box_id = b.box_id
JOIN dispatch_outbox o
  ON o.target_user_id = e.user_id
 AND o.pts = e.pts
WHERE b.owner_user_id = $1
  AND b.box_id = $2`, userID, msg.ID).Scan(&boxPts, &eventPts, &eventBoxID, &eventType, &outboxPts, &outboxEventType); err != nil {
		t.Fatalf("load login message/event/outbox chain: %v", err)
	}
	if int(boxPts) != msg.Pts || eventPts != boxPts || eventBoxID != int32(msg.ID) || eventType != string(domain.UpdateEventNewMessage) ||
		outboxPts != eventPts || outboxEventType != eventType {
		t.Fatalf("box/event/outbox chain = box_pts %d event %d/%d/%s outbox %d/%s, message=%+v",
			boxPts, eventPts, eventBoxID, eventType, outboxPts, outboxEventType, msg)
	}
	var topMessageID, unreadCount int32
	if err := pool.QueryRow(ctx, `
SELECT top_message_id, unread_count
FROM dialogs
WHERE user_id = $1 AND peer_type = 'user' AND peer_id = $2`, userID, domain.OfficialSystemUserID).Scan(&topMessageID, &unreadCount); err != nil {
		t.Fatalf("load login code dialog: %v", err)
	}
	if int(topMessageID) != msg.ID || int(unreadCount) != want {
		t.Fatalf("dialog top/unread = %d/%d, want %d/%d", topMessageID, unreadCount, msg.ID, want)
	}
}
