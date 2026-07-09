package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestBotAPIUpdateRetention 锁定 H1 场景矩阵：
//   - 已确认 + 超宽限 → 删；已确认 + 宽限内 → 留；
//   - 未确认 + date 超保留期 → 删（含无 state 行的 MTProto-only bot）；
//   - 未确认 + date 在保留期内 → 留；
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
	stale := now - int64((48 * time.Hour).Seconds())
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
	expiredNoState := enqueue(mtprotoOnlyBot, 4, stale) // 无 state 行 + date 超保留期 → 删
	freshNoState := enqueue(mtprotoOnlyBot, 5, now)

	if err := s.ConfirmBotAPIUpdates(ctx, confirmedBot, confirmedFresh.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"UPDATE bot_api_updates SET created_at = now() - interval '1 hour' WHERE id = $1", confirmedOld.ID); err != nil {
		t.Fatalf("backdate confirmed row: %v", err)
	}

	deleted, err := s.DeleteDeliveredOrExpired(ctx, 15*time.Minute, 24*time.Hour, 1000)
	if err != nil {
		t.Fatalf("DeleteDeliveredOrExpired: %v", err)
	}
	// 共享测试库可能有其它历史行同被回收，只要求至少删掉本测试的 2 行；
	// 精确归属由下方 remaining 断言保证。
	if deleted < 2 {
		t.Fatalf("deleted = %d, want >= 2 (confirmed+grace expired, date expired)", deleted)
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
