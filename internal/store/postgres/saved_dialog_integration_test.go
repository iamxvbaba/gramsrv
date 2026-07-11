package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestMessageStoreSavedDialogsLifecycle 覆盖收藏夹分会话 PG 主链路：
// 直发/转发的 saved_peer 写入、fwd saved_from 持久化（重读非内存 echo）、
// 聚合分页、置顶、按子会话历史过滤与删除（含置顶清理）。
func TestMessageStoreSavedDialogsLifecycle(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	alice, err := users.Create(ctx, domain.User{AccessHash: 91, Phone: "+1672" + suffix + "01", FirstName: "SavedAlice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{AccessHash: 92, Phone: "+1672" + suffix + "02", FirstName: "SavedBob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{alice.ID, bob.ID})
		_, _ = pool.Exec(ctx, "DELETE FROM saved_dialog_pins WHERE user_id = ANY($1::bigint[])", []int64{alice.ID, bob.ID})
	})

	messages := NewMessageStore(pool)
	selfPeer := domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}
	bobPeer := domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID}

	// 直发笔记 → saved_peer = self。
	note, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    alice.ID,
		RecipientUserID: alice.ID,
		RandomID:        841001,
		Message:         "note to self",
		Date:            1700000700,
	})
	if err != nil {
		t.Fatalf("send note: %v", err)
	}

	// bob 私聊来信 → alice 转发进收藏夹 → saved_peer = bob 且 fwd saved_from 持久化。
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    bob.ID,
		RecipientUserID: alice.ID,
		RandomID:        841002,
		Message:         "from bob",
		Date:            1700000701,
	}); err != nil {
		t.Fatalf("bob send: %v", err)
	}
	aliceInbox, err := messages.ListByUser(ctx, alice.ID, domain.MessageFilter{
		HasPeer: true, Peer: bobPeer, Limit: 1,
	})
	if err != nil || len(aliceInbox.Messages) != 1 {
		t.Fatalf("alice inbox = %+v err %v", aliceInbox.Messages, err)
	}
	fwdRes, err := messages.ForwardPrivateMessages(ctx, domain.ForwardPrivateMessagesRequest{
		OwnerUserID: alice.ID,
		ToUserID:    alice.ID,
		FromPeer:    bobPeer,
		MessageIDs:  []int{aliceInbox.Messages[0].ID},
		RandomIDs:   []int64{841003},
		Date:        1700000702,
	})
	if err != nil || len(fwdRes.SenderMessages) != 1 {
		t.Fatalf("forward to saved: %+v err %v", fwdRes, err)
	}
	fwdID := fwdRes.SenderMessages[0].ID

	// 重读 self-chat（非发送 echo）验证持久化：saved_peer 与 fwd saved_from 都在。
	selfHistory, err := messages.ListByUser(ctx, alice.ID, domain.MessageFilter{
		HasPeer: true, Peer: selfPeer, Limit: 10,
	})
	if err != nil || len(selfHistory.Messages) != 2 {
		t.Fatalf("self history = %+v err %v, want note+forward", selfHistory.Messages, err)
	}
	for _, msg := range selfHistory.Messages {
		switch msg.ID {
		case note.SenderMessage.ID:
			if msg.SavedPeer != selfPeer {
				t.Fatalf("note saved_peer = %+v, want self", msg.SavedPeer)
			}
		case fwdID:
			if msg.SavedPeer != bobPeer {
				t.Fatalf("forward saved_peer = %+v, want bob", msg.SavedPeer)
			}
			if msg.Forward == nil || msg.Forward.SavedFrom != bobPeer || msg.Forward.SavedFromMsgID != aliceInbox.Messages[0].ID {
				t.Fatalf("forward header = %+v, want persisted saved_from bob/%d", msg.Forward, aliceInbox.Messages[0].ID)
			}
		default:
			t.Fatalf("unexpected self-chat message %d", msg.ID)
		}
	}

	// 聚合：转发更晚在前；分页 limit=1 → slice，offset 翻页到尾页。
	list, err := messages.ListSavedDialogs(ctx, alice.ID, domain.SavedDialogsFilter{Limit: 20})
	if err != nil {
		t.Fatalf("list saved dialogs: %v", err)
	}
	if !list.Full || list.Count != 2 || len(list.Dialogs) != 2 ||
		list.Dialogs[0].Peer != bobPeer || list.Dialogs[0].TopMessage != fwdID ||
		list.Dialogs[1].Peer != selfPeer || list.Dialogs[1].TopMessage != note.SenderMessage.ID {
		t.Fatalf("saved dialogs = %+v count %d full %v, want bob then self", list.Dialogs, list.Count, list.Full)
	}
	page, err := messages.ListSavedDialogs(ctx, alice.ID, domain.SavedDialogsFilter{Limit: 1})
	if err != nil || page.Full || len(page.Dialogs) != 1 || page.Dialogs[0].Peer != bobPeer || page.Count != 2 {
		t.Fatalf("first page = %+v err %v, want slice bob count 2", page, err)
	}
	tail, err := messages.ListSavedDialogs(ctx, alice.ID, domain.SavedDialogsFilter{Limit: 1, OffsetID: fwdID})
	if err != nil || !tail.Full || len(tail.Dialogs) != 1 || tail.Dialogs[0].Peer != selfPeer {
		t.Fatalf("tail page = %+v err %v, want full self", tail, err)
	}

	// 置顶：self 置顶后首页在前、exclude 口径、ByPeers/pinned 一致。
	changed, err := messages.ToggleSavedDialogPin(ctx, alice.ID, selfPeer, true)
	if err != nil || !changed {
		t.Fatalf("pin self = %v err %v", changed, err)
	}
	if changed, err = messages.ToggleSavedDialogPin(ctx, alice.ID, selfPeer, true); err != nil || changed {
		t.Fatalf("re-pin self = %v err %v, want no-op", changed, err)
	}
	list, err = messages.ListSavedDialogs(ctx, alice.ID, domain.SavedDialogsFilter{Limit: 20})
	if err != nil || len(list.Dialogs) != 2 || list.Dialogs[0].Peer != selfPeer || !list.Dialogs[0].Pinned {
		t.Fatalf("after pin = %+v err %v, want pinned self first", list.Dialogs, err)
	}
	excluded, err := messages.ListSavedDialogs(ctx, alice.ID, domain.SavedDialogsFilter{ExcludePinned: true, Limit: 20})
	if err != nil || excluded.Count != 1 || len(excluded.Dialogs) != 1 || excluded.Dialogs[0].Peer != bobPeer {
		t.Fatalf("exclude pinned = %+v err %v, want only bob count 1", excluded, err)
	}
	pinned, err := messages.ListPinnedSavedDialogs(ctx, alice.ID)
	if err != nil || len(pinned.Dialogs) != 1 || pinned.Dialogs[0].Peer != selfPeer {
		t.Fatalf("pinned list = %+v err %v, want self", pinned.Dialogs, err)
	}
	byPeers, err := messages.ListSavedDialogsByPeers(ctx, alice.ID, []domain.Peer{bobPeer, {Type: domain.PeerTypeUser, ID: 424242}})
	if err != nil || len(byPeers.Dialogs) != 1 || byPeers.Dialogs[0].Peer != bobPeer {
		t.Fatalf("by peers = %+v err %v, want bob only", byPeers.Dialogs, err)
	}

	// reorder：bob 也置顶后强排为 bob→self。
	if _, err := messages.ToggleSavedDialogPin(ctx, alice.ID, bobPeer, true); err != nil {
		t.Fatalf("pin bob: %v", err)
	}
	if err := messages.ReorderPinnedSavedDialogs(ctx, alice.ID, []domain.Peer{bobPeer, selfPeer}, true); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	pinned, err = messages.ListPinnedSavedDialogs(ctx, alice.ID)
	if err != nil || len(pinned.Dialogs) != 2 || pinned.Dialogs[0].Peer != bobPeer || pinned.Dialogs[1].Peer != selfPeer {
		t.Fatalf("after reorder = %+v err %v, want bob then self", pinned.Dialogs, err)
	}

	// 子会话历史过滤。
	bobHistory, err := messages.ListByUser(ctx, alice.ID, domain.MessageFilter{
		HasPeer: true, Peer: selfPeer, SavedPeer: bobPeer, Limit: 10, NeedTotalCount: true,
	})
	if err != nil || len(bobHistory.Messages) != 1 || bobHistory.Messages[0].ID != fwdID || bobHistory.Count != 1 {
		t.Fatalf("bob sublist history = %+v count %d err %v", bobHistory.Messages, bobHistory.Count, err)
	}

	// 删除 bob 子会话：消息消失、置顶清理、带 pts 的 delete 事件。
	delRes, err := messages.DeleteSavedHistory(ctx, domain.DeleteSavedHistoryRequest{
		OwnerUserID: alice.ID,
		SavedPeer:   bobPeer,
		Date:        1700000710,
	})
	if err != nil {
		t.Fatalf("delete saved history: %v", err)
	}
	if delRes.More || len(delRes.MessageIDs) != 1 || delRes.MessageIDs[0] != fwdID || delRes.Event.Pts == 0 || delRes.Event.PtsCount != 1 {
		t.Fatalf("delete result = %+v, want fwd deleted with pts", delRes)
	}
	list, err = messages.ListSavedDialogs(ctx, alice.ID, domain.SavedDialogsFilter{Limit: 20})
	if err != nil || len(list.Dialogs) != 1 || list.Dialogs[0].Peer != selfPeer || list.Count != 1 {
		t.Fatalf("after delete = %+v count %d err %v, want only self", list.Dialogs, list.Count, err)
	}
	pinned, err = messages.ListPinnedSavedDialogs(ctx, alice.ID)
	if err != nil || len(pinned.Dialogs) != 1 || pinned.Dialogs[0].Peer != selfPeer {
		t.Fatalf("pins after delete = %+v err %v, want bob pin cleared", pinned.Dialogs, err)
	}
}

// TestSavedDialogsBackfillRule 验证 0087 回填 CASE 规则与 TDLib 语义对齐：
// fwd 作者可见或无 fwd → self；仅 from_name → hidden author 2666000。
// migration 已在建库时应用，这里手工插入老格式行（saved_peer_type=”）后
// 重放回填语句验证规则本身。
func TestSavedDialogsBackfillRule(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 93, Phone: "+1673" + suffix + "01", FirstName: "BackfillOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	insert := func(boxID int, fwdPeerID int64, fwdName string, fwdDate int) {
		t.Helper()
		var privateID int64
		if err := pool.QueryRow(ctx, `
INSERT INTO private_messages (
  sender_user_id, recipient_user_id, random_id, request_fingerprint, recipient_delivered,
  sender_box_id, sender_pts, recipient_box_id, recipient_pts,
  message_date, body, entities
)
VALUES (
  $1, $1, $2::bigint, decode(repeat('00', 32), 'hex'), false,
  $3::int, $3::int, 0, 0,
  1700000800, 'legacy', '[]'::jsonb
)
RETURNING id`, owner.ID, 841100+boxID, boxID).Scan(&privateID); err != nil {
			t.Fatalf("insert legacy private message %d: %v", boxID, err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO message_boxes (
  owner_user_id, box_id, private_message_id, message_sender_id,
  peer_type, peer_id, from_user_id, message_date, outgoing, body, entities, pts,
  fwd_from_peer_type, fwd_from_peer_id, fwd_from_name, fwd_date
) VALUES ($1, $2::int, $3::bigint, $1, 'user', $1, $1, 1700000800, true, 'legacy', '[]'::jsonb, $2::int,
  CASE WHEN $4::bigint <> 0 THEN 'user' ELSE '' END, $4::bigint, $5, $6::int)`,
			owner.ID, boxID, privateID, fwdPeerID, fwdName, fwdDate); err != nil {
			t.Fatalf("insert legacy row %d: %v", boxID, err)
		}
	}
	insert(1, 0, "", 0)                // 直发笔记 → self
	insert(2, 4242, "", 1700000500)    // fwd 作者可见、saved_from 丢失 → self
	insert(3, 0, "Hidden", 1700000501) // 仅 from_name → 2666000

	if _, err := pool.Exec(ctx, `
UPDATE message_boxes
SET saved_peer_type = 'user',
    saved_peer_id = CASE
        WHEN fwd_date <> 0 AND fwd_from_peer_id = 0 AND fwd_from_name <> '' THEN 2666000
        ELSE owner_user_id
    END
WHERE peer_type = 'user'
  AND peer_id = owner_user_id
  AND saved_peer_type = ''`); err != nil {
		t.Fatalf("replay backfill: %v", err)
	}

	messages := NewMessageStore(pool)
	list, err := messages.ListSavedDialogs(ctx, owner.ID, domain.SavedDialogsFilter{Limit: 20})
	if err != nil {
		t.Fatalf("list after backfill: %v", err)
	}
	if len(list.Dialogs) != 2 {
		t.Fatalf("backfill dialogs = %+v, want self + hidden author", list.Dialogs)
	}
	hidden := domain.Peer{Type: domain.PeerTypeUser, ID: domain.SavedHiddenAuthorUserID}
	self := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	if list.Dialogs[0].Peer != hidden || list.Dialogs[0].TopMessage != 3 {
		t.Fatalf("hidden sublist = %+v, want top 3", list.Dialogs[0])
	}
	if list.Dialogs[1].Peer != self || list.Dialogs[1].TopMessage != 2 {
		t.Fatalf("self sublist = %+v, want top 2 (visible-author legacy forward)", list.Dialogs[1])
	}
}
