package postgres

import (
	"bytes"
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestBotAPICallbackQueryQueueRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	bot, err := users.Create(ctx, domain.User{
		AccessHash: 921, Phone: "+1921" + suffix + "01", FirstName: "CallbackQueueBot",
	})
	if err != nil {
		t.Fatalf("create bot user: %v", err)
	}
	clicker, err := users.Create(ctx, domain.User{
		AccessHash: 922, Phone: "+1922" + suffix + "02", FirstName: "CallbackClicker",
	})
	if err != nil {
		t.Fatalf("create callback user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO bots (bot_user_id, owner_user_id, token_secret)
VALUES ($1, $1, 'callback-queue-secret')`, bot.ID); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_updates WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_update_states WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE bot_user_id = $1", bot.ID)
	})

	callback := &domain.BotCallbackQuery{
		ID: 880011, BotUserID: bot.ID, UserID: clicker.ID,
		Peer: domain.Peer{Type: domain.PeerTypeUser, ID: clicker.ID}, MessageID: 17,
		ChatInstance: 990022, Data: []byte{0, 1, 0xff, 'x'},
	}
	req := domain.EnqueueBotAPIUpdateRequest{
		BotUserID: bot.ID, Kind: domain.BotAPIUpdateCallbackQuery,
		Peer: callback.Peer, MessageID: callback.MessageID, Date: int(time.Now().Unix()), Callback: callback,
	}
	store := NewBotAPIUpdateStore(pool)
	first, created, err := store.EnqueueBotAPIUpdate(ctx, req)
	if err != nil || !created {
		t.Fatalf("enqueue callback: row=%+v created=%v err=%v", first, created, err)
	}
	again, created, err := store.EnqueueBotAPIUpdate(ctx, req)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("dedupe callback: row=%+v created=%v err=%v", again, created, err)
	}
	items, err := store.ListBotAPIUpdates(ctx, bot.ID, first.ID, 100)
	if err != nil || len(items) != 1 {
		t.Fatalf("list callback = %+v, %v", items, err)
	}
	got := items[0].Callback
	if got == nil || got.ID != callback.ID || got.BotUserID != bot.ID || got.UserID != clicker.ID ||
		got.Peer != callback.Peer || got.MessageID != callback.MessageID || got.ChatInstance != callback.ChatInstance ||
		!bytes.Equal(got.Data, callback.Data) {
		t.Fatalf("callback round trip = %+v, want %+v", got, callback)
	}
}

func TestBotAPIInlineCallbackAndWebhookStateRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	bot, err := users.Create(ctx, domain.User{AccessHash: 931, Phone: "+1931" + suffix + "01", FirstName: "WebhookBot"})
	if err != nil {
		t.Fatal(err)
	}
	clicker, err := users.Create(ctx, domain.User{AccessHash: 932, Phone: "+1932" + suffix + "02", FirstName: "InlineClicker"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bots (bot_user_id, owner_user_id, token_secret) VALUES ($1, $1, 'webhook-secret')`, bot.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_webhooks WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_updates WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_update_states WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE bot_user_id = $1", bot.ID)
	})

	s := NewBotAPIUpdateStore(pool)
	inline := &domain.BotInlineMessageID{DCID: 2, OwnerID: clicker.ID, ID: 17, AccessHash: 445566}
	callback := &domain.BotCallbackQuery{
		ID: 9911, BotUserID: bot.ID, UserID: clicker.ID, ChatInstance: 8811,
		Data: []byte{0, 1, 0xff}, InlineMessage: inline,
	}
	row, created, err := s.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
		BotUserID: bot.ID, Kind: domain.BotAPIUpdateCallbackQuery, Date: int(time.Now().Unix()), Callback: callback,
	})
	if err != nil || !created {
		t.Fatalf("enqueue inline callback row=%#v created=%v err=%v", row, created, err)
	}
	items, err := s.ListBotAPIUpdates(ctx, bot.ID, row.ID, 100)
	if err != nil || len(items) != 1 || items[0].Peer != (domain.Peer{}) || items[0].MessageID != 0 ||
		items[0].Callback == nil || items[0].Callback.InlineMessage == nil || *items[0].Callback.InlineMessage != *inline ||
		!bytes.Equal(items[0].Callback.Data, callback.Data) {
		t.Fatalf("inline callback items=%#v err=%v", items, err)
	}

	config := domain.BotAPIWebhook{
		BotUserID: bot.ID, URL: "https://example.test/hook", SecretToken: "safe_secret",
		MaxConnections: 8, AllowedUpdates: []domain.BotAPIUpdateKind{domain.BotAPIUpdateCallbackQuery}, AllowedUpdatesSet: true,
	}
	if err := s.SetBotAPIWebhook(ctx, config, false); err != nil {
		t.Fatal(err)
	}
	stored, found, err := s.BotAPIWebhook(ctx, bot.ID)
	if err != nil || !found || stored.URL != config.URL || stored.SecretToken != config.SecretToken ||
		stored.MaxConnections != 8 || len(stored.AllowedUpdates) != 1 {
		t.Fatalf("webhook=%#v found=%v err=%v", stored, found, err)
	}
	config.URL = "https://example.test/reconfigured"
	config.AllowedUpdates = nil
	config.AllowedUpdatesSet = false
	if err := s.SetBotAPIWebhook(ctx, config, false); err != nil {
		t.Fatal(err)
	}
	stored, found, err = s.BotAPIWebhook(ctx, bot.ID)
	if err != nil || !found || stored.URL != config.URL || len(stored.AllowedUpdates) != 1 || stored.AllowedUpdates[0] != domain.BotAPIUpdateCallbackQuery {
		t.Fatalf("preserved webhook=%#v found=%v err=%v", stored, found, err)
	}
	if acquired, err := s.AcquireBotAPIWebhookLease(ctx, bot.ID, "one", time.Minute); err != nil || !acquired {
		t.Fatalf("first lease=%v err=%v", acquired, err)
	}
	if acquired, err := s.AcquireBotAPIWebhookLease(ctx, bot.ID, "two", time.Minute); err != nil || acquired {
		t.Fatalf("second lease=%v err=%v", acquired, err)
	}
	if err := s.ReleaseBotAPIWebhookLease(ctx, bot.ID, "stale"); err != nil {
		t.Fatal(err)
	}
	if acquired, _ := s.AcquireBotAPIWebhookLease(ctx, bot.ID, "two", time.Minute); acquired {
		t.Fatal("stale webhook release removed active lease")
	}
	next := time.Now().Add(time.Hour)
	if err := s.RecordBotAPIWebhookSuccess(ctx, bot.ID, "one", next); err != nil {
		t.Fatal(err)
	}
	if due, err := s.ListDueBotAPIWebhooks(ctx, 10); err != nil || len(due) != 0 {
		t.Fatalf("idle due=%#v err=%v", due, err)
	}
	// A newly inserted allowed callback wakes the idle webhook in the same SQL statement.
	callback2 := *callback
	callback2.ID++
	callback2.InlineMessage = &domain.BotInlineMessageID{DCID: 2, OwnerID: clicker.ID, ID: 18, AccessHash: 556677}
	if _, created, err := s.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
		BotUserID: bot.ID, Kind: domain.BotAPIUpdateCallbackQuery, Date: int(time.Now().Unix()), Callback: &callback2,
	}); err != nil || !created {
		t.Fatalf("enqueue wake created=%v err=%v", created, err)
	}
	if due, err := s.ListDueBotAPIWebhooks(ctx, 10); err != nil || len(due) != 1 || due[0].BotUserID != bot.ID {
		t.Fatalf("woken due=%#v err=%v", due, err)
	}
	if err := s.DeleteBotAPIWebhook(ctx, bot.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, found, err := s.BotAPIWebhook(ctx, bot.ID); err != nil || found {
		t.Fatalf("webhook after delete found=%v err=%v", found, err)
	}
	if pending, err := s.PendingBotAPIUpdateCount(ctx, bot.ID); err != nil || pending != 0 {
		t.Fatalf("pending after delete/drop=%d err=%v", pending, err)
	}
}

func TestBotAPIPollLeaseCrossStoreInstance(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	bot, err := users.Create(ctx, domain.User{AccessHash: 933, Phone: "+1933" + suffix + "01", FirstName: "PollLeaseBot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bots (bot_user_id, owner_user_id, token_secret) VALUES ($1, $1, 'poll-lease-secret')`, bot.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_update_states WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE bot_user_id = $1", bot.ID)
	})
	a, b := NewBotAPIUpdateStore(pool), NewBotAPIUpdateStore(pool)
	if acquired, err := a.AcquireBotAPIPollLease(ctx, bot.ID, "one", time.Minute); err != nil || !acquired {
		t.Fatalf("first acquire=%v err=%v", acquired, err)
	}
	if acquired, err := b.AcquireBotAPIPollLease(ctx, bot.ID, "two", time.Minute); err != nil || acquired {
		t.Fatalf("cross-instance acquire=%v err=%v", acquired, err)
	}
	if err := b.ReleaseBotAPIPollLease(ctx, bot.ID, "stale"); err != nil {
		t.Fatal(err)
	}
	if acquired, _ := b.AcquireBotAPIPollLease(ctx, bot.ID, "two", time.Minute); acquired {
		t.Fatal("stale release removed active poll lease")
	}
	if err := a.ReleaseBotAPIPollLease(ctx, bot.ID, "one"); err != nil {
		t.Fatal(err)
	}
	if acquired, err := b.AcquireBotAPIPollLease(ctx, bot.ID, "two", time.Minute); err != nil || !acquired {
		t.Fatalf("successor acquire=%v err=%v", acquired, err)
	}
}

func TestBotAPIPollingStateClampFilterTailAndDrop(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	bot, err := users.Create(ctx, domain.User{
		AccessHash: 923, Phone: "+1923" + suffix + "01", FirstName: "PollingStateBot",
	})
	if err != nil {
		t.Fatalf("create bot user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO bots (bot_user_id, owner_user_id, token_secret) VALUES ($1, $1, 'poll-state-secret')`, bot.ID); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_updates WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_update_states WHERE bot_user_id = $1", bot.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE bot_user_id = $1", bot.ID)
	})

	s := NewBotAPIUpdateStore(pool)
	enqueue := func(kind domain.BotAPIUpdateKind, messageID int) (domain.BotAPIUpdate, bool) {
		t.Helper()
		row, created, err := s.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: bot.ID, Kind: kind,
			Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: bot.ID + 1},
			MessageID: messageID, SourcePts: messageID, Date: int(time.Now().Unix()),
		})
		if err != nil {
			t.Fatalf("enqueue %s/%d: %v", kind, messageID, err)
		}
		return row, created
	}
	for id := 1; id <= 3; id++ {
		if _, created := enqueue(domain.BotAPIUpdateMessage, id); !created {
			t.Fatalf("initial message %d was not created", id)
		}
	}
	if err := s.SetBotAPIAllowedUpdates(ctx, bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateEditedMessage}); err != nil {
		t.Fatalf("set allowed updates: %v", err)
	}
	if row, created := enqueue(domain.BotAPIUpdateMessage, 4); created || row.ID != 0 {
		t.Fatalf("filtered row=%+v created=%v", row, created)
	}
	lastBeforeBaseline, created := enqueue(domain.BotAPIUpdateEditedMessage, 5)
	if !created {
		t.Fatal("allowed edit was filtered")
	}
	if err := s.ConfirmBotAPIUpdates(ctx, bot.ID, 1<<60); err != nil {
		t.Fatalf("initialize external cursor: %v", err)
	}
	confirmed, found, err := s.ConfirmedBotAPIUpdateID(ctx, bot.ID)
	if err != nil || !found || confirmed != lastBeforeBaseline.ID {
		t.Fatalf("baseline confirmed=%d found=%v err=%v want=%d", confirmed, found, err, lastBeforeBaseline.ID)
	}
	pendingRow, created := enqueue(domain.BotAPIUpdateEditedMessage, 6)
	if !created {
		t.Fatal("post-baseline edit was filtered")
	}
	if err := s.ConfirmBotAPIUpdates(ctx, bot.ID, 1<<60); err != nil {
		t.Fatalf("repeat external cursor: %v", err)
	}
	confirmed, _, _ = s.ConfirmedBotAPIUpdateID(ctx, bot.ID)
	if confirmed != lastBeforeBaseline.ID {
		t.Fatalf("repeat external cursor advanced to %d, want %d", confirmed, lastBeforeBaseline.ID)
	}
	tail, err := s.ListTailBotAPIUpdates(ctx, bot.ID, 1, 100)
	if err != nil || len(tail) != 1 || tail[0].ID != pendingRow.ID {
		t.Fatalf("tail=%+v err=%v want=%d", tail, err, pendingRow.ID)
	}
	if count, err := s.PendingBotAPIUpdateCount(ctx, bot.ID); err != nil || count != 1 {
		t.Fatalf("pending count=%d err=%v", count, err)
	}
	if err := s.DropPendingBotAPIUpdates(ctx, bot.ID); err != nil {
		t.Fatalf("drop pending: %v", err)
	}
	if count, err := s.PendingBotAPIUpdateCount(ctx, bot.ID); err != nil || count != 0 {
		t.Fatalf("pending after drop=%d err=%v", count, err)
	}
}

// TestBotAPIUpdateRetention 锁定 H1 场景矩阵：
//   - 已确认 + 超宽限 → 删；已确认 + 宽限内 → 留；
//   - 未确认 + created_at 超保留期 → 删（含无 state 行的 MTProto-only bot）；
//   - 未确认 + created_at 在保留期内 → 留；
//   - 删除后 getUpdates 读路径（fromID > confirmed）不受影响。
func TestBotAPIUpdateRetention(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	newBot := func(phoneTail, name string) int64 {
		t.Helper()
		u, err := users.Create(ctx, domain.User{
			AccessHash: 920,
			Phone:      "+1920" + suffix + phoneTail,
			FirstName:  name,
		})
		if err != nil {
			t.Fatalf("create bot user %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO bots (bot_user_id, owner_user_id, token_secret)
VALUES ($1, $1, 'retention-test-secret')
ON CONFLICT (bot_user_id) DO NOTHING`, u.ID); err != nil {
			t.Fatalf("seed bot %s: %v", name, err)
		}
		return u.ID
	}
	confirmedBot := newBot("01", "RetentionConfirmedBot")
	mtprotoOnlyBot := newBot("02", "RetentionMTOnlyBot")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_updates WHERE bot_user_id IN ($1, $2)", confirmedBot, mtprotoOnlyBot)
		_, _ = pool.Exec(ctx, "DELETE FROM bot_api_update_states WHERE bot_user_id IN ($1, $2)", confirmedBot, mtprotoOnlyBot)
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE bot_user_id IN ($1, $2)", confirmedBot, mtprotoOnlyBot)
	})

	s := NewBotAPIUpdateStore(pool)
	now := time.Now().Unix()
	enqueue := func(botID int64, messageID int, date int64) domain.BotAPIUpdate {
		t.Helper()
		row, created, err := s.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{
			BotUserID: botID,
			Kind:      domain.BotAPIUpdateMessage,
			Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: 1},
			MessageID: messageID,
			SourcePts: messageID,
			Date:      int(date),
		})
		if err != nil || !created {
			t.Fatalf("enqueue bot=%d msg=%d: created=%v err=%v", botID, messageID, created, err)
		}
		return row
	}

	confirmedOld := enqueue(confirmedBot, 1, now)   // 已确认 + created_at 回拨超宽限 → 删
	confirmedFresh := enqueue(confirmedBot, 2, now) // 已确认 + 宽限内 → 留
	unconfirmedFresh := enqueue(confirmedBot, 3, now)
	expiredNoState := enqueue(mtprotoOnlyBot, 4, now) // 无 state 行 + created_at 超保留期 → 删
	freshNoState := enqueue(mtprotoOnlyBot, 5, now)

	if err := s.ConfirmBotAPIUpdates(ctx, confirmedBot, confirmedFresh.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE bot_api_updates SET created_at = now() - interval '1 hour' WHERE id = $1", confirmedOld.ID); err != nil {
		t.Fatalf("backdate confirmed row: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE bot_api_updates SET created_at = now() - interval '48 hours' WHERE id = $1", expiredNoState.ID); err != nil {
		t.Fatalf("backdate expired row: %v", err)
	}

	deleted, err := s.DeleteDeliveredOrExpired(ctx, 15*time.Minute, 24*time.Hour, 1000)
	if err != nil {
		t.Fatalf("DeleteDeliveredOrExpired: %v", err)
	}
	// 共享测试库可能有其它历史行同被回收，只要求至少删掉本测试的 2 行；
	// 精确归属由下方 remaining 断言保证。
	if deleted < 2 {
		t.Fatalf("deleted = %d, want >= 2 (confirmed+grace expired, created_at expired)", deleted)
	}

	remaining := map[int64]bool{}
	rows, err := pool.Query(ctx, "SELECT id FROM bot_api_updates WHERE bot_user_id IN ($1, $2)", confirmedBot, mtprotoOnlyBot)
	if err != nil {
		t.Fatalf("list remaining: %v", err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan remaining: %v", err)
		}
		remaining[id] = true
	}
	rows.Close()
	if remaining[confirmedOld.ID] {
		t.Fatal("confirmed row past grace was not deleted")
	}
	if remaining[expiredNoState.ID] {
		t.Fatal("expired row of state-less bot was not deleted")
	}
	if !remaining[confirmedFresh.ID] || !remaining[unconfirmedFresh.ID] || !remaining[freshNoState.ID] {
		t.Fatalf("fresh rows were deleted, remaining=%v", remaining)
	}

	// 读路径回归：确认水位之后的未确认行仍可被 getUpdates 读到。
	items, err := s.ListBotAPIUpdates(ctx, confirmedBot, confirmedFresh.ID+1, 100)
	if err != nil {
		t.Fatalf("list after retention: %v", err)
	}
	if len(items) != 1 || items[0].ID != unconfirmedFresh.ID {
		t.Fatalf("post-retention list = %+v, want only unconfirmed fresh row %d", items, unconfirmedFresh.ID)
	}
}
