package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

func TestMessageStorePrivateRandomIDConflictMatrix(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1881"+suffix+"01", "IDSender", "")
	recipient := createTestUser(t, ctx, users, "+1881"+suffix+"02", "IDRecipient", "")
	other := createTestUser(t, ctx, users, "+1881"+suffix+"03", "IDOther", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID, other.ID})
	})

	messages := NewMessageStore(pool)
	base := domain.SendPrivateTextRequest{
		SenderUserID:    sender.ID,
		RecipientUserID: recipient.ID,
		RandomID:        771001,
		Message:         "immutable payload",
		Date:            1700001000,
	}
	first, err := messages.SendPrivateText(ctx, base)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*domain.SendPrivateTextRequest)
	}{
		{name: "peer", mutate: func(req *domain.SendPrivateTextRequest) { req.RecipientUserID = other.ID }},
		{name: "body", mutate: func(req *domain.SendPrivateTextRequest) { req.Message = "different body" }},
		{name: "media", mutate: func(req *domain.SendPrivateTextRequest) {
			req.Media = &domain.MessageMedia{
				Kind: domain.MessageMediaKindContact,
				Contact: &domain.MessageContact{
					PhoneNumber: "+10000000000",
					FirstName:   "Different",
				},
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			if _, err := messages.SendPrivateText(ctx, req); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
				t.Fatalf("conflicting replay err = %v, want ErrMessageRandomIDDuplicate", err)
			}
		})
	}

	var privateCount, boxCount, eventCount, outboxCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM private_messages WHERE sender_user_id = $1 AND random_id = $2`, sender.ID, base.RandomID).Scan(&privateCount); err != nil {
		t.Fatalf("count private messages: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_boxes WHERE private_message_id = $1`, first.SenderMessage.UID).Scan(&boxCount); err != nil {
		t.Fatalf("count message boxes: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_update_events WHERE user_id = ANY($1::bigint[])`, []int64{sender.ID, recipient.ID, other.ID}).Scan(&eventCount); err != nil {
		t.Fatalf("count update events: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])`, []int64{sender.ID, recipient.ID, other.ID}).Scan(&outboxCount); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	if privateCount != 1 || boxCount != 2 || eventCount != 2 || outboxCount != 2 {
		t.Fatalf("rows after conflicts = private %d boxes %d events %d outbox %d, want 1/2/2/2", privateCount, boxCount, eventCount, outboxCount)
	}
}

func TestMessageStorePrivateRandomIDReplaySelfAndBlocked(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	self := createTestUser(t, ctx, users, "+1882"+suffix+"01", "IDSelf", "")
	sender := createTestUser(t, ctx, users, "+1882"+suffix+"02", "BlockedSender", "")
	recipient := createTestUser(t, ctx, users, "+1882"+suffix+"03", "BlockedRecipient", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{self.ID, sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	selfReq := domain.SendPrivateTextRequest{
		SenderUserID: self.ID, RecipientUserID: self.ID, RandomID: 772001,
		Message: "saved note", Date: 1700001100,
	}
	selfFirst, err := messages.SendPrivateText(ctx, selfReq)
	if err != nil {
		t.Fatalf("self first: %v", err)
	}
	selfReq.Date++
	selfReq.OriginSessionID = 99
	selfReq.RecipientBlocked = true
	selfReplay, err := messages.SendPrivateText(ctx, selfReq)
	if err != nil {
		t.Fatalf("self replay: %v", err)
	}
	if !selfReplay.Duplicate || selfReplay.SenderMessage.ID != selfFirst.SenderMessage.ID || selfReplay.RecipientMessage.ID != selfFirst.SenderMessage.ID {
		t.Fatalf("self replay = %+v, want original single box", selfReplay)
	}

	blockedReq := domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID, RandomID: 772002,
		Message: "blocked delivery", Date: 1700001110, RecipientBlocked: true,
	}
	blockedFirst, err := messages.SendPrivateText(ctx, blockedReq)
	if err != nil {
		t.Fatalf("blocked first: %v", err)
	}
	if blockedFirst.RecipientMessage.ID != 0 {
		t.Fatalf("blocked recipient message = %+v, want empty", blockedFirst.RecipientMessage)
	}
	blockedReq.Date++
	blockedReq.RecipientBlocked = false
	blockedReplay, err := messages.SendPrivateText(ctx, blockedReq)
	if err != nil {
		t.Fatalf("blocked replay: %v", err)
	}
	if !blockedReplay.Duplicate || blockedReplay.SenderMessage.ID != blockedFirst.SenderMessage.ID || blockedReplay.RecipientMessage.ID != 0 {
		t.Fatalf("blocked replay = %+v, want original sender-only result", blockedReplay)
	}

	var selfBoxes, blockedBoxes, recipientEvents, recipientOutbox int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_boxes WHERE private_message_id = $1`, selfFirst.SenderMessage.UID).Scan(&selfBoxes); err != nil {
		t.Fatalf("count self boxes: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_boxes WHERE private_message_id = $1`, blockedFirst.SenderMessage.UID).Scan(&blockedBoxes); err != nil {
		t.Fatalf("count blocked boxes: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_update_events WHERE user_id = $1`, recipient.ID).Scan(&recipientEvents); err != nil {
		t.Fatalf("count blocked recipient events: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dispatch_outbox WHERE target_user_id = $1`, recipient.ID).Scan(&recipientOutbox); err != nil {
		t.Fatalf("count blocked recipient outbox: %v", err)
	}
	if selfBoxes != 1 || blockedBoxes != 1 || recipientEvents != 0 || recipientOutbox != 0 {
		t.Fatalf("replay rows = self boxes %d blocked boxes %d recipient events %d outbox %d, want 1/1/0/0", selfBoxes, blockedBoxes, recipientEvents, recipientOutbox)
	}
}

func TestMessageStorePrivateRandomIDReplayUsesCurrentSnapshotAndDurableDelete(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1884"+suffix+"01", "ReceiptSender", "")
	recipient := createTestUser(t, ctx, users, "+1884"+suffix+"02", "ReceiptRecipient", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	type replayState struct {
		events int
		outbox int
		pts    int
	}
	loadReplayState := func() replayState {
		t.Helper()
		var state replayState
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_update_events WHERE user_id = $1`, sender.ID).Scan(&state.events); err != nil {
			t.Fatalf("count sender events: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM dispatch_outbox WHERE target_user_id = $1`, sender.ID).Scan(&state.outbox); err != nil {
			t.Fatalf("count sender outbox: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT contiguous_pts FROM user_update_watermarks WHERE user_id = $1`, sender.ID).Scan(&state.pts); err != nil {
			t.Fatalf("load sender pts: %v", err)
		}
		return state
	}
	req := domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID, RandomID: 774001,
		Message: "immutable receipt", Date: 1700001300,
	}
	first, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	edited, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID: sender.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
		ID:          first.SenderMessage.ID,
		Message:     "edited projection",
		EditDate:    1700001301,
	})
	if err != nil {
		t.Fatalf("edit message: %v", err)
	}
	beforeReplay := loadReplayState()
	replay, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("replay after edit: %v", err)
	}
	if replay.SenderMessage.ID != first.SenderMessage.ID || replay.SenderMessage.Pts != edited.Self().Message.Pts || replay.SenderMessage.Body != "edited projection" ||
		replay.RecipientMessage.ID != first.RecipientMessage.ID || replay.RecipientMessage.Pts != first.RecipientMessage.Pts {
		t.Fatalf("replay after edit = %+v/%+v, want current sender snapshot and immutable recipient receipt %d/%d",
			replay.SenderMessage, replay.RecipientMessage,
			first.RecipientMessage.ID, first.RecipientMessage.Pts)
	}
	if replay.SenderEvent.Pts != first.SenderEvent.Pts || replay.ReplayDeleteEvent != nil {
		t.Fatalf("replay after edit event = %+v delete=%+v, want first-send pts and no delete", replay.SenderEvent, replay.ReplayDeleteEvent)
	}
	if after := loadReplayState(); after != beforeReplay {
		t.Fatalf("edit replay mutated durable state = %+v, want %+v", after, beforeReplay)
	}
	deleted, err := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
		OwnerUserID: sender.ID,
		IDs:         []int{first.SenderMessage.ID},
		Revoke:      true,
		Date:        1700001302,
	})
	if err != nil {
		t.Fatalf("delete message: %v", err)
	}
	beforeReplay = loadReplayState()
	replay, err = messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("replay after delete: %v", err)
	}
	if replay.SenderMessage.ID != first.SenderMessage.ID || replay.SenderMessage.Pts != first.SenderMessage.Pts || replay.SenderMessage.Body != "immutable receipt" {
		t.Fatalf("replay after delete = %+v, want immutable first sender snapshot", replay.SenderMessage)
	}
	if replay.ReplayDeleteEvent == nil || replay.ReplayDeleteEvent.Pts != deleted.Self().Event.Pts ||
		len(replay.ReplayDeleteEvent.MessageIDs) != 1 || replay.ReplayDeleteEvent.MessageIDs[0] != first.SenderMessage.ID {
		t.Fatalf("replay delete event = %+v, want durable delete %+v", replay.ReplayDeleteEvent, deleted.Self().Event)
	}
	if after := loadReplayState(); after != beforeReplay {
		t.Fatalf("delete replay mutated durable state = %+v, want %+v", after, beforeReplay)
	}
}

// beginHookDB lets the test commit a competing send exactly after the outer
// fast-path lookup and before its transaction starts. With MaxConns=1, a
// duplicate fallback that queries the pool while holding the transaction would
// wait until the context deadline; reading through qtx completes immediately.
type beginHookDB struct {
	*pgxpool.Pool
	once      sync.Once
	before    func(context.Context) error
	beforeErr error
}

func (db *beginHookDB) Begin(ctx context.Context) (pgx.Tx, error) {
	db.once.Do(func() {
		if db.before != nil {
			db.beforeErr = db.before(ctx)
		}
	})
	if db.beforeErr != nil {
		return nil, db.beforeErr
	}
	return db.Pool.Begin(ctx)
}

func TestMessageStorePrivateRandomIDConflictFallbackUsesTransactionConnection(t *testing.T) {
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run postgres integration test")
	}
	if err := Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse pool config: %v", err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open single-connection pool: %v", err)
	}
	t.Cleanup(pool.Close)

	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1883"+suffix+"01", "PoolSender", "")
	recipient := createTestUser(t, ctx, users, "+1883"+suffix+"02", "PoolRecipient", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	req := domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID, RandomID: 773001,
		Message: "commit between preflight and insert", Date: 1700001200,
	}
	boxIDs := &perUserCounterAllocator{}
	db := &beginHookDB{Pool: pool}
	db.before = func(ctx context.Context) error {
		_, err := NewMessageStore(pool, WithMessageAllocators(boxIDs)).SendPrivateText(ctx, req)
		return err
	}
	deadlineCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	got, err := NewMessageStore(db, WithMessageAllocators(boxIDs)).SendPrivateText(deadlineCtx, req)
	if err != nil {
		t.Fatalf("conflict fallback with MaxConns=1: %v", err)
	}
	if !got.Duplicate || got.SenderMessage.ID == 0 || got.RecipientMessage.ID == 0 {
		t.Fatalf("conflict fallback result = %+v, want committed duplicate boxes", got)
	}
}
