package postgres

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func TestMessagePartitionSeekPlansUseIndexes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{
		AccessHash: 51,
		Phone:      "+1999" + suffix + "01",
		FirstName:  "PlanSender",
	})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{
		AccessHash: 52,
		Phone:      "+1999" + suffix + "02",
		FirstName:  "PlanRecipient",
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool)
	var first domain.SendPrivateTextResult
	for i := 0; i < 3; i++ {
		sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    sender.ID,
			RecipientUserID: recipient.ID,
			RandomID:        int64(9000 + i),
			Message:         "plan check",
			Date:            1700000300 + i,
		})
		if err != nil {
			t.Fatalf("seed message %d: %v", i, err)
		}
		if i == 0 {
			first = sent
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE dispatch_outbox
		SET status = 'dispatching',
		    updated_at = now() - interval '1 minute'
		WHERE target_user_id = $1
	`, recipient.ID); err != nil {
		t.Fatalf("mark dispatch stale: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin explain tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	historyPlan := explainText(t, ctx, tx, `
SELECT box_id
FROM message_boxes
WHERE owner_user_id = $1
  AND peer_type = 'user'
  AND peer_id = $2
  AND NOT deleted
  AND box_id < $3
ORDER BY box_id DESC
LIMIT 20
`, recipient.ID, sender.ID, 100000)
	requirePlanUsesIndex(t, historyPlan, "message_boxes")
	requirePlanNotContains(t, historyPlan, "Append")

	updatesPlan := explainText(t, ctx, tx, `
SELECT pts
FROM user_update_events
WHERE user_id = $1
  AND pts > $2
ORDER BY pts ASC
LIMIT 100
`, recipient.ID, 0)
	requirePlanUsesIndex(t, updatesPlan, "user_update_events")
	requirePlanNotContains(t, updatesPlan, "Append")

	updateDetailPlan := explainText(t, ctx, tx, `
SELECT
  e.pts,
  COALESCE(m.box_id, 0)::int AS message_id,
  COALESCE(m.fwd_from_peer_type, '')::text AS fwd_from_peer_type,
  COALESCE(m.fwd_from_peer_id, 0)::bigint AS fwd_from_peer_id,
  COALESCE(m.reply_to_peer_type, '')::text AS reply_to_peer_type,
  COALESCE(m.reply_to_peer_id, 0)::bigint AS reply_to_peer_id,
  COALESCE(peer_u.id, 0)::bigint AS peer_user_id,
  COALESCE(from_u.id, 0)::bigint AS from_user_id,
  COALESCE(fwd_u.id, 0)::bigint AS fwd_user_id,
  COALESCE(reply_u.id, 0)::bigint AS reply_user_id
FROM user_update_events e
LEFT JOIN message_boxes m ON m.owner_user_id = e.user_id AND m.box_id = e.message_box_id
LEFT JOIN users peer_u ON m.peer_type = 'user' AND peer_u.id = m.peer_id
LEFT JOIN users from_u ON from_u.id = m.from_user_id
LEFT JOIN users fwd_u ON m.fwd_from_peer_type = 'user' AND fwd_u.id = m.fwd_from_peer_id
LEFT JOIN users reply_u ON m.reply_to_peer_type = 'user' AND reply_u.id = m.reply_to_peer_id
WHERE e.user_id = $1
  AND e.pts > $2
ORDER BY e.pts ASC
LIMIT 100
`, recipient.ID, 0)
	requirePlanUsesIndex(t, updateDetailPlan, "user_update_events")
	requireUniquePartitionCountAtMost(t, updateDetailPlan, `message_boxes_p\d+`, 1)
	requirePlanNotContains(t, updateDetailPlan, "channels_p")

	batchDispatchDetailPlan := explainText(t, ctx, tx, `
SELECT
  e.pts,
  COALESCE(m.box_id, 0)::int AS message_id,
  COALESCE(m.fwd_from_peer_type, '')::text AS fwd_from_peer_type,
  COALESCE(m.fwd_from_peer_id, 0)::bigint AS fwd_from_peer_id,
  COALESCE(m.reply_to_peer_type, '')::text AS reply_to_peer_type,
  COALESCE(m.reply_to_peer_id, 0)::bigint AS reply_to_peer_id,
  COALESCE(peer_u.id, 0)::bigint AS peer_user_id,
  COALESCE(from_u.id, 0)::bigint AS from_user_id,
  COALESCE(fwd_u.id, 0)::bigint AS fwd_user_id,
  COALESCE(reply_u.id, 0)::bigint AS reply_user_id
FROM unnest($1::bigint[]) WITH ORDINALITY AS u(user_id, ord)
JOIN unnest($2::int[]) WITH ORDINALITY AS p(pts, ord) USING (ord)
JOIN user_update_events e ON e.user_id = u.user_id AND e.pts = p.pts
LEFT JOIN message_boxes m ON m.owner_user_id = e.user_id AND m.box_id = e.message_box_id
LEFT JOIN users peer_u ON m.peer_type = 'user' AND peer_u.id = m.peer_id
LEFT JOIN users from_u ON from_u.id = m.from_user_id
LEFT JOIN users fwd_u ON m.fwd_from_peer_type = 'user' AND fwd_u.id = m.fwd_from_peer_id
LEFT JOIN users reply_u ON m.reply_to_peer_type = 'user' AND reply_u.id = m.reply_to_peer_id
`, []int64{recipient.ID}, []int32{int32(first.RecipientMessage.Pts)})
	requirePlanNotContains(t, batchDispatchDetailPlan, "channels_p")

	visibleByPrivatePlan := explainText(t, ctx, tx, `
SELECT box_id
FROM message_boxes
WHERE owner_user_id = ANY($1::bigint[])
  AND message_sender_id = $2::bigint
  AND private_message_id = $3::bigint
  AND NOT deleted
ORDER BY owner_user_id ASC, box_id ASC
FOR UPDATE
`, []int64{sender.ID, recipient.ID}, sender.ID, first.SenderMessage.UID)
	requirePlanUsesIndex(t, visibleByPrivatePlan, "message_boxes")
	requireUniquePartitionCountAtMost(t, visibleByPrivatePlan, `message_boxes_p\d+`, 2)

	deleteByPrivatePlan := explainText(t, ctx, tx, `
WITH requested AS (
  SELECT
    ($1::bigint[])[i] AS message_sender_id,
    ($2::bigint[])[i] AS private_message_id,
    ($3::bigint[])[i] AS owner_user_id
  FROM generate_subscripts($2::bigint[], 1) AS g(i)
  WHERE i <= cardinality($1::bigint[])
    AND i <= cardinality($3::bigint[])
),
deduped AS (
  SELECT DISTINCT message_sender_id, private_message_id, owner_user_id
  FROM requested
  WHERE owner_user_id <> 0
),
updated AS (
  UPDATE message_boxes m
  SET deleted = true
  FROM deduped d
  WHERE m.owner_user_id = ANY($3::bigint[])
    AND m.owner_user_id = d.owner_user_id
    AND m.message_sender_id = d.message_sender_id
    AND m.private_message_id = d.private_message_id
    AND NOT m.deleted
  RETURNING m.owner_user_id, m.box_id
)
SELECT owner_user_id, box_id
FROM updated
ORDER BY owner_user_id ASC, box_id ASC
`, []int64{sender.ID, sender.ID}, []int64{first.SenderMessage.UID, first.SenderMessage.UID}, []int64{sender.ID, recipient.ID})
	requirePlanUsesIndex(t, deleteByPrivatePlan, "message_boxes")
	requireUniquePartitionCountAtMost(t, deleteByPrivatePlan, `message_boxes_p\d+`, 2)

	dispatchPlan := explainText(t, ctx, tx, `
WITH picked_heads AS (
  SELECT h.target_user_id, h.head_id, h.head_pts
  FROM dispatch_outbox_user_heads h
  WHERE h.logical_shard = ANY($1::smallint[])
    AND (
      (h.status = 'pending' AND h.next_attempt_at <= now())
      OR
      (h.status = 'dispatching' AND h.updated_at < now() - interval '30 seconds')
    )
  ORDER BY h.next_attempt_at ASC, h.target_user_id ASC, h.head_pts ASC, h.head_id ASC
  LIMIT 100
  FOR UPDATE OF h SKIP LOCKED
)
SELECT d.target_user_id, d.pts, d.id
FROM picked_heads h
JOIN dispatch_outbox d
  ON d.target_user_id = h.target_user_id
 AND d.id = h.head_id
`, []int16{int16(recipient.ID % 256)})
	requirePlanContains(t, dispatchPlan, "dispatch_outbox_user_heads_dispatching_shard_idx")
	requirePlanContains(t, dispatchPlan, "dispatch_outbox")
	requirePlanContains(t, dispatchPlan, "Index")
	requirePlanNotMatches(t, dispatchPlan, `dispatch_outbox_p\d+`)
	requirePlanNotContains(t, dispatchPlan, "Seq Scan")

	failedCleanupPlan := explainText(t, ctx, tx, `
WITH doomed AS MATERIALIZED (
  SELECT target_user_id, head_id AS id
  FROM dispatch_outbox_user_heads
  WHERE status = 'failed'
    AND updated_at < now() - interval '1 minute'
  ORDER BY updated_at ASC, target_user_id ASC, head_id ASC
  LIMIT 100
)
SELECT target_user_id, id
FROM doomed
`)
	requirePlanContains(t, failedCleanupPlan, "dispatch_outbox_user_heads_failed_cleanup_idx")
	requirePlanNotContains(t, failedCleanupPlan, "Seq Scan")
}

func explainText(t *testing.T, ctx context.Context, tx pgx.Tx, query string, args ...any) string {
	t.Helper()
	rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+query, args...)
	if err != nil {
		t.Fatalf("explain query: %v", err)
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain row: %v", err)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read explain rows: %v", err)
	}
	return b.String()
}

// requirePlanUsesIndex 断言计划在指定表上走索引扫描、未退化为 Seq Scan。
// 去分区后这些表都是普通表（不再有 *_pNN 子分区），故只校验「命中目标表 + 用了索引 + 无 Seq Scan」。
func requirePlanUsesIndex(t *testing.T, plan, table string) {
	t.Helper()
	requirePlanContains(t, plan, table)
	requirePlanContains(t, plan, "Index")
	requirePlanNotContains(t, plan, "Seq Scan")
}

func requirePlanContains(t *testing.T, plan string, needle string) {
	t.Helper()
	if !strings.Contains(plan, needle) {
		t.Fatalf("plan missing %q:\n%s", needle, plan)
	}
}

func requirePlanNotContains(t *testing.T, plan string, needle string) {
	t.Helper()
	if strings.Contains(plan, needle) {
		t.Fatalf("plan contains %q:\n%s", needle, plan)
	}
}

func requirePlanNotMatches(t *testing.T, plan string, pattern string) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	if re.MatchString(plan) {
		t.Fatalf("plan matches %q:\n%s", pattern, plan)
	}
}

func requireUniquePartitionCountAtMost(t *testing.T, plan, pattern string, max int) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	matches := re.FindAllString(plan, -1)
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		seen[match] = struct{}{}
	}
	// 去分区后这些表已是普通表：0 处 *_pNN 命中是预期（合格）。保留本断言作为回归护栏——
	// 若某天意外重新引入分区且单查询触及 >max 个分区，这里会炸出来。
	if len(seen) > max {
		t.Fatalf("plan touches %d unique partitions, want <= %d:\n%s", len(seen), max, plan)
	}
}
