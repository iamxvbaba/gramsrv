package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelMultiPin 验证超级群多置顶模型：多条共存、filterPinned 搜索、
// 上限、unpinAll 批量清除、删除被置顶消息后的缓存重算。
func TestChannelMultiPin(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 91, Phone: "+1676" + suffix + "01", FirstName: "MultiPinOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := users.Create(ctx, domain.User{AccessHash: 92, Phone: "+1676" + suffix + "02", FirstName: "MultiPinMember"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, member.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "MultiPin " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{member.ID},
		Date:          1700000920,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID

	const pinCount = 11
	ids := make([]int, 0, pinCount)
	for i := 0; i < pinCount; i++ {
		sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    owner.ID,
			ChannelID: channelID,
			RandomID:  int64(852000 + i),
			Message:   "pin-target",
			Date:      1700000921 + i,
		})
		if err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		ids = append(ids, sent.Message.ID)
	}

	pin := func(id int, pinned bool) (domain.UpdateChannelPinnedMessageResult, error) {
		return channels.UpdatePinnedMessage(ctx, domain.UpdateChannelPinnedMessageRequest{
			UserID:    owner.ID,
			ChannelID: channelID,
			MessageID: id,
			Pinned:    pinned,
			Date:      1700000950,
		})
	}

	// 多条共存：pin 前三条，互不替代。
	for _, id := range ids[:3] {
		res, err := pin(id, true)
		if err != nil {
			t.Fatalf("pin %d: %v", id, err)
		}
		if !res.Event.Pinned || len(res.Event.MessageIDs) != 1 || res.Event.MessageIDs[0] != id {
			t.Fatalf("pin event = %+v, want pinned [%d]", res.Event, id)
		}
	}
	view, err := channels.GetChannel(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if view.Channel.PinnedMessageID != ids[2] {
		t.Fatalf("pinned_message_id = %d, want latest pinned %d", view.Channel.PinnedMessageID, ids[2])
	}
	history, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID: channelID, PinnedOnly: true, Limit: 50,
	})
	if err != nil {
		t.Fatalf("filterPinned search: %v", err)
	}
	if len(history.Messages) != 3 {
		t.Fatalf("filterPinned messages = %d, want 3 coexisting pins: %+v", len(history.Messages), history.Messages)
	}
	for _, msg := range history.Messages {
		if !msg.Pinned {
			t.Fatalf("filterPinned message %d lacks pinned flag", msg.ID)
		}
	}
	// 普通历史热路径保留 bounded has-more hint，不为每页额外 COUNT。
	hint, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID: channelID, PinnedOnly: true, Limit: 1,
	})
	if err != nil {
		t.Fatalf("filterPinned hint page: %v", err)
	}
	if len(hint.Messages) != 1 || hint.Count != 2 {
		t.Fatalf("filterPinned hint messages/count = %d/%d, want 1/2", len(hint.Messages), hint.Count)
	}
	// messages.search 首屏显式请求精确总数；页大小不能污染 Count。
	exact, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID: channelID, PinnedOnly: true, Limit: 1, NeedTotalCount: true,
	})
	if err != nil {
		t.Fatalf("filterPinned exact page: %v", err)
	}
	if len(exact.Messages) != 1 || exact.Count != 3 {
		t.Fatalf("filterPinned exact messages/count = %d/%d, want 1/3", len(exact.Messages), exact.Count)
	}
	// messages.getSearchCounters/limit=0 只计数，不加载消息及 reply/reaction companion。
	countOnly, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID: channelID, PinnedOnly: true, NeedTotalCount: true, CountOnly: true,
	})
	if err != nil {
		t.Fatalf("filterPinned count-only: %v", err)
	}
	if len(countOnly.Messages) != 0 || countOnly.Count != 3 {
		t.Fatalf("filterPinned count-only messages/count = %d/%d, want 0/3", len(countOnly.Messages), countOnly.Count)
	}
	// 精确计数只扫描该频道的 pinned 部分索引，不能退化为全频道消息扫描。
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin pinned count explain: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("disable seqscan for pinned count explain: %v", err)
	}
	rows, err := tx.Query(ctx, `EXPLAIN (COSTS OFF)
SELECT count(*)::int
FROM channel_messages
WHERE channel_id = $1 AND NOT deleted AND pinned`, channelID)
	if err != nil {
		t.Fatalf("explain pinned count: %v", err)
	}
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			t.Fatalf("scan pinned count plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("read pinned count plan: %v", err)
	}
	rows.Close()
	if !strings.Contains(plan.String(), "channel_messages_live_pinned_idx") {
		t.Fatalf("pinned count plan misses partial index:\n%s", plan.String())
	}
	// 普通历史页的消息行直接携带 pinned 标志（多置顶都标，不只最新）。
	page, err := channels.ListChannelHistory(ctx, member.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 50})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	flagged := 0
	for _, msg := range page.Messages {
		if msg.Pinned {
			flagged++
		}
	}
	if flagged != 3 {
		t.Fatalf("history pinned flags = %d, want 3", flagged)
	}

	// unpin 最新一条：其它置顶保留，缓存回落到次新。
	if _, err := pin(ids[2], false); err != nil {
		t.Fatalf("unpin latest: %v", err)
	}
	view, err = channels.GetChannel(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("get channel after unpin: %v", err)
	}
	if view.Channel.PinnedMessageID != ids[1] {
		t.Fatalf("pinned_message_id after unpin = %d, want %d", view.Channel.PinnedMessageID, ids[1])
	}

	// 无数量上限（对齐官方）：剩余全部 pin 上，共 pinCount 条同时置顶。
	for _, id := range ids[2:] {
		if _, err := pin(id, true); err != nil {
			t.Fatalf("pin %d: %v", id, err)
		}
	}
	history, err = channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID: channelID, PinnedOnly: true, Limit: 50,
	})
	if err != nil {
		t.Fatalf("filterPinned all pinned: %v", err)
	}
	if len(history.Messages) != pinCount {
		t.Fatalf("filterPinned messages = %d, want %d coexisting pins", len(history.Messages), pinCount)
	}

	// 删除被置顶的最新消息：置顶集合自动排除，缓存重算。
	latest := ids[pinCount-1]
	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{
		UserID: owner.ID, ChannelID: channelID, IDs: []int{latest}, Date: 1700000961,
	}); err != nil {
		t.Fatalf("delete pinned message: %v", err)
	}
	view, err = channels.GetChannel(ctx, owner.ID, channelID)
	if err != nil {
		t.Fatalf("get channel after delete: %v", err)
	}
	if view.Channel.PinnedMessageID != ids[pinCount-2] {
		t.Fatalf("pinned_message_id after delete = %d, want %d", view.Channel.PinnedMessageID, ids[pinCount-2])
	}

	// unpinAll：一条事件批量携带剩余全部置顶 id，缓存清零，再次调用 no-op。
	res, err := channels.UnpinAllChannelMessages(ctx, domain.UnpinAllChannelMessagesRequest{
		UserID: owner.ID, ChannelID: channelID, Date: 1700000962,
	})
	if err != nil {
		t.Fatalf("unpin all: %v", err)
	}
	if res.Event.Pinned || len(res.Event.MessageIDs) != pinCount-1 {
		t.Fatalf("unpin all event = %+v, want pinned=false with %d ids", res.Event, pinCount-1)
	}
	if res.Channel.PinnedMessageID != 0 {
		t.Fatalf("pinned_message_id after unpin all = %d, want 0", res.Channel.PinnedMessageID)
	}
	if _, err := channels.UnpinAllChannelMessages(ctx, domain.UnpinAllChannelMessagesRequest{
		UserID: owner.ID, ChannelID: channelID, Date: 1700000963,
	}); !errors.Is(err, domain.ErrChannelNotModified) {
		t.Fatalf("unpin all again err = %v, want ErrChannelNotModified", err)
	}
	history, err = channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{
		ChannelID: channelID, PinnedOnly: true, Limit: 50,
	})
	if err != nil {
		t.Fatalf("filterPinned after unpin all: %v", err)
	}
	if len(history.Messages) != 0 {
		t.Fatalf("filterPinned after unpin all = %+v, want empty", history.Messages)
	}
}
