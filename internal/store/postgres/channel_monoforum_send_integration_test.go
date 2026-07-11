package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// TestSendMonoforumMessageAndHistoryPostgres 回归频道私信(monoforum)发送+读历史的 PG 实现:
// 私信存进 channel_messages 的 saved_peer 维度、复用 channel pts;按订阅者分子会话、幂等、无串话。
// 门控于 TELESRV_TEST_POSTGRES_DSN。
func TestSendMonoforumMessageAndHistoryPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 91, Phone: "+1779" + suffix + "41", FirstName: "MonoMsgOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := users.Create(ctx, domain.User{AccessHash: 92, Phone: "+1779" + suffix + "42", FirstName: "MonoSub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	other, err := users.Create(ctx, domain.User{AccessHash: 93, Phone: "+1779" + suffix + "43", FirstName: "MonoOther"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) > 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, sub.ID, other.ID})
	})

	channels := NewChannelStore(pool)
	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "Mono Msg " + suffix, Broadcast: true, Date: 1700001000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelIDs = append(channelIDs, broadcast.Channel.ID)
	enabled, err := channels.SetPaidMessagesPrice(ctx, owner.ID, broadcast.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if monoID == 0 {
		t.Fatalf("no monoforum created")
	}
	channelIDs = append(channelIDs, monoID)

	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: sub.ID}

	m1, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 111, Message: "hi", Date: 1700001001})
	if err != nil {
		t.Fatalf("subscriber send 1: %v", err)
	}
	if m1.Message.SavedPeer != subPeer || m1.Message.ChannelID != monoID || m1.Message.Pts == 0 {
		t.Fatalf("m1 = %+v, want saved_peer sub + channel mono + pts>0", m1.Message)
	}
	if _, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 112, Message: "again", Date: 1700001002}); err != nil {
		t.Fatalf("subscriber send 2: %v", err)
	}
	if _, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 113, Message: "reply", Date: 1700001003}); err != nil {
		t.Fatalf("admin reply: %v", err)
	}

	mainHist, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("main monoforum history: %v", err)
	}
	if mainHist.Count != 1 || len(mainHist.Messages) != 1 {
		t.Fatalf("main monoforum history count=%d len=%d, want only service message", mainHist.Count, len(mainHist.Messages))
	}
	// monoforum 的服务消息是创建消息(渲染 "Direct messages were enabled in this channel."),
	// paid_messages_price 只进母广播频道。
	if action := mainHist.Messages[0].Action; action == nil || action.Type != domain.ChannelActionCreate {
		t.Fatalf("main monoforum action = %+v, want channel_create", action)
	}
	if len(mainHist.Channels) != 1 || mainHist.Channels[0].ID != broadcast.Channel.ID {
		t.Fatalf("main monoforum extra channels = %+v, want parent %d", mainHist.Channels, broadcast.Channel.ID)
	}
	if _, err := channels.ListChannelHistory(ctx, sub.ID, domain.ChannelHistoryFilter{ChannelID: monoID, Limit: 10}); err == nil {
		t.Fatalf("subscriber main monoforum history = nil err, want denied")
	}

	// 幂等。
	dup, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 111, Message: "hi", Date: 1700001004})
	if err != nil {
		t.Fatalf("dup send: %v", err)
	}
	if !dup.Duplicate || dup.Message.ID != m1.Message.ID {
		t.Fatalf("dup = %+v, want duplicate of m1 id %d", dup.Message, m1.Message.ID)
	}
	if _, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: sub.ID, SavedPeer: subPeer, RandomID: 111, Message: "changed", Date: 1700001004}); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("changed monoforum intent err = %v, want ErrMessageRandomIDDuplicate", err)
	}
	var monoforumFingerprint []byte
	if err := pool.QueryRow(ctx, `SELECT request_fingerprint FROM channel_messages WHERE channel_id=$1 AND id=$2`, monoID, m1.Message.ID).Scan(&monoforumFingerprint); err != nil {
		t.Fatalf("load monoforum fingerprint: %v", err)
	}
	if len(monoforumFingerprint) != 32 {
		t.Fatalf("monoforum fingerprint length = %d, want 32", len(monoforumFingerprint))
	}

	// 历史(经 scanChannelMessage 读回 saved_peer)。
	hist, err := channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: monoID, SavedPeer: subPeer, Limit: 10})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if hist.Count != 3 || len(hist.Messages) != 3 {
		t.Fatalf("history count=%d len=%d, want 3", hist.Count, len(hist.Messages))
	}
	if hist.Messages[0].Body != "reply" {
		t.Fatalf("history[0] = %q, want newest 'reply'", hist.Messages[0].Body)
	}
	for _, m := range hist.Messages {
		if m.SavedPeer != subPeer {
			t.Fatalf("history msg saved_peer = %+v, want sub", m.SavedPeer)
		}
	}

	// 另一个订阅者不串会话。
	otherPeer := domain.Peer{Type: domain.PeerTypeUser, ID: other.ID}
	if _, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: other.ID, SavedPeer: otherPeer, RandomID: 201, Message: "other", Date: 1700001005}); err != nil {
		t.Fatalf("other subscriber send: %v", err)
	}
	subHist, _ := channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: monoID, SavedPeer: subPeer, Limit: 10})
	if subHist.Count != 3 {
		t.Fatalf("sub history after other subscriber = %d, want still 3 (no cross-talk)", subHist.Count)
	}

	// 去重按订阅者子会话维度(迁移 0022 唯一索引含 saved_peer_id):管理员用相同 random_id 向两个不同
	// 订阅者发,不得互相去重(与 memory 行为一致)。
	a, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 9001, Message: "to sub", Date: 1700001010})
	if err != nil {
		t.Fatalf("dedup send A: %v", err)
	}
	b, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: otherPeer, RandomID: 9001, Message: "to other", Date: 1700001011})
	if err != nil {
		t.Fatalf("dedup send B (cross-sublist same random_id must not collide): %v", err)
	}
	if b.Duplicate || b.Message.ID == a.Message.ID {
		t.Fatalf("cross-sublist same random_id wrongly deduped: a=%d b=%d dup=%v", a.Message.ID, b.Message.ID, b.Duplicate)
	}
	// SavedPeer belongs both to the lookup scope and to the fallback intent.
	// Cross-sublist sends remain legal, while presenting another sublist's
	// fingerprint for an existing scope must be rejected rather than replayed.
	otherIntentFingerprint, err := store.MonoforumSendFingerprint(domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: otherPeer, RandomID: 9001, Message: "to sub",
	})
	if err != nil {
		t.Fatalf("fingerprint mismatched saved peer: %v", err)
	}
	if _, _, err := channels.LookupChannelSendReplay(ctx, domain.ChannelSendReplayRequest{
		ChannelID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 9001, IdempotencyFingerprint: otherIntentFingerprint,
	}); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("mismatched saved-peer fingerprint err = %v, want ErrMessageRandomIDDuplicate", err)
	}
	// 同一子会话真重发仍去重。
	again, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 9001, Message: "to sub", Date: 1700001012})
	if err != nil {
		t.Fatalf("dedup resend A: %v", err)
	}
	if !again.Duplicate || again.Message.ID != a.Message.ID {
		t.Fatalf("same-sublist retry not deduped: again=%+v want dup of %d", again.Message, a.Message.ID)
	}

	// 订阅者子会话列表:两个订阅者,按 top 消息 id 倒序(other 最后发,排首)。
	dialogs, err := channels.ListMonoforumDialogs(ctx, domain.MonoforumDialogsFilter{MonoforumID: monoID, Limit: 10})
	if err != nil {
		t.Fatalf("list dialogs: %v", err)
	}
	if dialogs.Count != 2 || len(dialogs.Dialogs) != 2 {
		t.Fatalf("dialogs count=%d len=%d, want 2 subscribers", dialogs.Count, len(dialogs.Dialogs))
	}
	if dialogs.Dialogs[0].SavedPeer != otherPeer {
		t.Fatalf("dialogs[0] saved_peer = %+v, want newest 'other'", dialogs.Dialogs[0].SavedPeer)
	}
	if dialogs.Dialogs[1].SavedPeer != subPeer || dialogs.Dialogs[1].TopMessageID == 0 {
		t.Fatalf("dialogs[1] = %+v, want sub with top message", dialogs.Dialogs[1])
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin monoforum delete: %v", err)
	}
	mono, err := getChannelByID(ctx, tx, monoID)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("load monoforum for delete: %v", err)
	}
	_, deleteEvent, _, err := channels.deleteChannelMessagesTx(ctx, tx, mono, domain.ChannelMember{ChannelID: monoID, UserID: owner.ID, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive}, []int{a.Message.ID}, owner.ID, 1700001013)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("delete monoforum message: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit monoforum delete: %v", err)
	}
	var ptsBeforeReplay, eventsBeforeReplay int
	if err := pool.QueryRow(ctx, `SELECT pts FROM channels WHERE id = $1`, monoID).Scan(&ptsBeforeReplay); err != nil {
		t.Fatalf("load monoforum pts: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_update_events WHERE channel_id = $1`, monoID).Scan(&eventsBeforeReplay); err != nil {
		t.Fatalf("count monoforum events: %v", err)
	}
	deletedReplay, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: owner.ID, SavedPeer: subPeer, RandomID: 9001, Message: "to sub", Date: 1700001014})
	if err != nil {
		t.Fatalf("replay deleted monoforum message: %v", err)
	}
	if !deletedReplay.Duplicate || deletedReplay.Message.ID != a.Message.ID || deletedReplay.Message.Body != "to sub" || deletedReplay.ReplayDeleteEvent == nil || deletedReplay.ReplayDeleteEvent.Pts != deleteEvent.Pts {
		t.Fatalf("deleted monoforum replay = %+v, want first snapshot + durable delete %+v", deletedReplay, deleteEvent)
	}
	var ptsAfterReplay, eventsAfterReplay int
	if err := pool.QueryRow(ctx, `SELECT pts FROM channels WHERE id = $1`, monoID).Scan(&ptsAfterReplay); err != nil {
		t.Fatalf("reload monoforum pts: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_update_events WHERE channel_id = $1`, monoID).Scan(&eventsAfterReplay); err != nil {
		t.Fatalf("recount monoforum events: %v", err)
	}
	if ptsAfterReplay != ptsBeforeReplay || eventsAfterReplay != eventsBeforeReplay {
		t.Fatalf("deleted monoforum replay mutated pts/events = %d/%d, want %d/%d", ptsAfterReplay, eventsAfterReplay, ptsBeforeReplay, eventsBeforeReplay)
	}
}
