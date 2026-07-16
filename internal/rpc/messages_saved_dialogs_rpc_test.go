package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appcontacts "telesrv/internal/app/contacts"
	appdialogs "telesrv/internal/app/dialogs"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newSavedDialogsTestRouter(t *testing.T) (*Router, *memory.MessageStore, *memory.UserStore) {
	t.Helper()
	userStore := memory.NewUserStore()
	dialogs := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogs)
	contactStore := memory.NewContactStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Contacts: appcontacts.NewService(contactStore, userStore),
		Messages: appmessages.NewService(messageStore, dialogs),
		Dialogs:  appdialogs.NewService(dialogs),
	}, zaptest.NewLogger(t), clock.System)
	return r, messageStore, userStore
}

// savedDialogsFixture 构造收藏夹两个子会话：直发笔记（saved=self）+
// 从 bob 私聊转发（saved=bob，转发更晚 top 更新）。
func savedDialogsFixture(t *testing.T) (*Router, *memory.MessageStore, domain.User, domain.User, int, int) {
	t.Helper()
	ctx := context.Background()
	r, messageStore, userStore := newSavedDialogsTestRouter(t)
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 81, Phone: "15550009301", FirstName: "Alice"})
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 82, Phone: "15550009302", FirstName: "Bob"})

	if _, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerSelf{},
		Message:  "my note",
		RandomID: 93001,
	}); err != nil {
		t.Fatalf("send self note: %v", err)
	}
	if _, err := r.onMessagesSendMessage(WithUserID(ctx, bob.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
		Message:  "from bob",
		RandomID: 93002,
	}); err != nil {
		t.Fatalf("bob send: %v", err)
	}
	bobMsgID := privateTopMessageID(t, messageStore, alice.ID, bob.ID)
	if _, err := r.onMessagesForwardMessages(WithUserID(ctx, alice.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ToPeer:   &tg.InputPeerSelf{},
		ID:       []int{bobMsgID},
		RandomID: []int64{93003},
	}); err != nil {
		t.Fatalf("forward to saved: %v", err)
	}
	noteID := savedTopMessageID(t, messageStore, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID})
	fwdID := savedTopMessageID(t, messageStore, alice.ID, domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID})
	return r, messageStore, alice, bob, noteID, fwdID
}

func privateTopMessageID(t *testing.T, store *memory.MessageStore, ownerID, peerID int64) int {
	t.Helper()
	list, err := store.ListByUser(context.Background(), ownerID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		Limit:   1,
	})
	if err != nil || len(list.Messages) == 0 {
		t.Fatalf("list private top: %+v err %v", list.Messages, err)
	}
	return list.Messages[0].ID
}

func savedTopMessageID(t *testing.T, store *memory.MessageStore, ownerID int64, savedPeer domain.Peer) int {
	t.Helper()
	list, err := store.ListByUser(context.Background(), ownerID, domain.MessageFilter{
		HasPeer:   true,
		Peer:      domain.Peer{Type: domain.PeerTypeUser, ID: ownerID},
		SavedPeer: savedPeer,
		Limit:     1,
	})
	if err != nil || len(list.Messages) == 0 {
		t.Fatalf("list saved top: %+v err %v", list.Messages, err)
	}
	return list.Messages[0].ID
}

func savedDialogPage(t *testing.T, res tg.MessagesSavedDialogsClass) ([]*tg.SavedDialog, []tg.MessageClass, []tg.UserClass, int, bool) {
	t.Helper()
	var rawDialogs []tg.SavedDialogClass
	var messages []tg.MessageClass
	var users []tg.UserClass
	count := 0
	full := false
	switch v := res.(type) {
	case *tg.MessagesSavedDialogs:
		rawDialogs, messages, users = v.Dialogs, v.Messages, v.Users
		count = len(v.Dialogs)
		full = true
	case *tg.MessagesSavedDialogsSlice:
		rawDialogs, messages, users = v.Dialogs, v.Messages, v.Users
		count = v.Count
	default:
		t.Fatalf("saved dialogs = %T, want savedDialogs/slice", res)
	}
	dialogs := make([]*tg.SavedDialog, 0, len(rawDialogs))
	for _, d := range rawDialogs {
		sd, ok := d.(*tg.SavedDialog)
		if !ok {
			t.Fatalf("dialog = %T, want *tg.SavedDialog", d)
		}
		dialogs = append(dialogs, sd)
	}
	return dialogs, messages, users, count, full
}

func savedDialogPeerID(t *testing.T, d *tg.SavedDialog) int64 {
	t.Helper()
	switch p := d.Peer.(type) {
	case *tg.PeerUser:
		return p.UserID
	case *tg.PeerChannel:
		return p.ChannelID
	default:
		t.Fatalf("saved dialog peer = %T", d.Peer)
		return 0
	}
}

// TestMessagesGetSavedDialogsGroupsBySource 验证直发/转发分组、排序、
// saved_peer_id 下发与 users 投影。
func TestMessagesGetSavedDialogsGroupsBySource(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, noteID, fwdID := savedDialogsFixture(t)

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs: %v", err)
	}
	dialogs, messages, users, _, full := savedDialogPage(t, res)
	if !full || len(dialogs) != 2 {
		t.Fatalf("dialogs = %d full %v, want 2 full", len(dialogs), full)
	}
	// 转发更晚，bob 子会话在前。
	if savedDialogPeerID(t, dialogs[0]) != bob.ID || dialogs[0].TopMessage != fwdID {
		t.Fatalf("first dialog = peer %d top %d, want bob %d top %d", savedDialogPeerID(t, dialogs[0]), dialogs[0].TopMessage, bob.ID, fwdID)
	}
	if savedDialogPeerID(t, dialogs[1]) != alice.ID || dialogs[1].TopMessage != noteID {
		t.Fatalf("second dialog = peer %d top %d, want self %d top %d", savedDialogPeerID(t, dialogs[1]), dialogs[1].TopMessage, alice.ID, noteID)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2 tops", len(messages))
	}
	for _, raw := range messages {
		msg, ok := raw.(*tg.Message)
		if !ok {
			t.Fatalf("message = %T", raw)
		}
		saved, ok := msg.GetSavedPeerID()
		if !ok {
			t.Fatalf("message %d lacks saved_peer_id", msg.ID)
		}
		want := alice.ID
		if msg.ID == fwdID {
			want = bob.ID
		}
		if user, ok := saved.(*tg.PeerUser); !ok || user.UserID != want {
			t.Fatalf("message %d saved_peer_id = %+v, want user %d", msg.ID, saved, want)
		}
		if msg.ID == fwdID {
			fwd, ok := msg.GetFwdFrom()
			if !ok {
				t.Fatalf("forwarded top lacks fwd header")
			}
			savedFrom, ok := fwd.GetSavedFromPeer()
			if !ok {
				t.Fatalf("forwarded top lacks saved_from_peer")
			}
			if user, ok := savedFrom.(*tg.PeerUser); !ok || user.UserID != bob.ID {
				t.Fatalf("saved_from_peer = %+v, want bob", savedFrom)
			}
		}
	}
	var hasSelf, hasBob bool
	for _, raw := range users {
		u, ok := raw.(*tg.User)
		if !ok {
			continue
		}
		if u.ID == alice.ID {
			hasSelf = true
			if !u.Self {
				t.Fatalf("self user lacks self flag")
			}
		}
		if u.ID == bob.ID {
			hasBob = true
		}
	}
	if !hasSelf || !hasBob {
		t.Fatalf("users projection self=%v bob=%v, want both", hasSelf, hasBob)
	}
}

// TestMessagesGetSavedDialogsPagination 验证分页：limit 截断 → slice+count，
// offset 翻页 → 尾页 savedDialogs；Android 首页 offset_id=int32 max 等价首页。
func TestMessagesGetSavedDialogsPagination(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, noteID, fwdID := savedDialogsFixture(t)

	first, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	dialogs, _, _, count, full := savedDialogPage(t, first)
	if full || count != 2 || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("first page = %d dialogs count %d full %v, want slice 1/2 bob first", len(dialogs), count, full)
	}
	second, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		Limit:    1,
		OffsetID: fwdID,
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	dialogs, _, _, _, full = savedDialogPage(t, second)
	if !full || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != alice.ID || dialogs[0].TopMessage != noteID {
		t.Fatalf("second page = %+v full %v, want self note tail", dialogs, full)
	}

	androidFirst, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		Limit:    20,
		OffsetID: 2147483647,
	})
	if err != nil {
		t.Fatalf("android first page: %v", err)
	}
	dialogs, _, _, _, full = savedDialogPage(t, androidFirst)
	if !full || len(dialogs) != 2 {
		t.Fatalf("android first page = %d full %v, want 2 full", len(dialogs), full)
	}
}

// TestMessagesGetSavedDialogsNegativePendingOffset 验证 DrKLO Android self Saved
// Messages 分组里出现本地 pending 负消息 id 时，下一页请求不会被误判为
// LIMIT_INVALID。服务端持久化 top_message 仍保持正数；负 offset 仅是客户端本地
// 游标噪声，按首页/无 offset 处理。
func TestMessagesGetSavedDialogsNegativePendingOffset(t *testing.T) {
	ctx := context.Background()
	r, _, alice, _, _, _ := savedDialogsFixture(t)

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		Limit:      20,
		OffsetID:   -1001,
		OffsetDate: -1,
		OffsetPeer: &tg.InputPeerSelf{},
	})
	if err != nil {
		t.Fatalf("negative pending offset: %v", err)
	}
	dialogs, _, _, _, full := savedDialogPage(t, res)
	if !full || len(dialogs) != 2 {
		t.Fatalf("negative pending offset page = %d full %v, want first page 2 full", len(dialogs), full)
	}

	_, err = r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: -1})
	if !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("negative limit err = %v, want LIMIT_INVALID", err)
	}
}

// TestMessagesGetSavedDialogsHashNotModified 验证 Android calcHash 序列命中
// 返回 savedDialogsNotModified{count}。
func TestMessagesGetSavedDialogsHashNotModified(t *testing.T) {
	ctx := context.Background()
	r, _, alice, _, _, _ := savedDialogsFixture(t)

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs: %v", err)
	}
	dialogs, messages, _, _, _ := savedDialogPage(t, res)
	dateByID := map[int]int{}
	for _, raw := range messages {
		if msg, ok := raw.(*tg.Message); ok {
			dateByID[msg.ID] = msg.Date
		}
	}
	var hash uint64
	for _, d := range dialogs {
		pinned := int64(0)
		if d.Pinned {
			pinned = 1
		}
		hash = tdesktopHashUpdate(hash, pinned)
		hash = tdesktopHashUpdate(hash, savedDialogPeerID(t, d))
		hash = tdesktopHashUpdate(hash, int64(d.TopMessage))
		hash = tdesktopHashUpdate(hash, int64(dateByID[d.TopMessage]))
	}
	cached, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		Limit: 20,
		Hash:  int64(hash),
	})
	if err != nil {
		t.Fatalf("getSavedDialogs hash: %v", err)
	}
	notModified, ok := cached.(*tg.MessagesSavedDialogsNotModified)
	if !ok || notModified.Count != 2 {
		t.Fatalf("hash hit = %#v, want notModified count 2", cached)
	}
}

// TestMessagesSavedDialogPinLifecycle 验证置顶翻转/排序/排除全链路：
// updateSavedDialogPinned 推送、首页 pinned 在前、getPinnedSavedDialogs、
// exclude_pinned 口径、reorder 推 updatePinnedSavedDialogs。
func TestMessagesSavedDialogPinLifecycle(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, noteID, _ := savedDialogsFixture(t)

	pinSelfReq := &tg.MessagesToggleSavedDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerSelf{}}}
	pinSelfReq.SetPinned(true)
	ok, err := r.onMessagesToggleSavedDialogPin(WithUserID(ctx, alice.ID), pinSelfReq)
	if err != nil || !ok {
		t.Fatalf("pin self sublist = %v err %v", ok, err)
	}

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs: %v", err)
	}
	dialogs, _, _, _, _ := savedDialogPage(t, res)
	if len(dialogs) != 2 || savedDialogPeerID(t, dialogs[0]) != alice.ID || !dialogs[0].Pinned {
		t.Fatalf("pinned self sublist not first: %+v", dialogs)
	}
	if dialogs[1].Pinned {
		t.Fatalf("bob sublist unexpectedly pinned")
	}

	pinnedRes, err := r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs: %v", err)
	}
	dialogs, _, _, _, full := savedDialogPage(t, pinnedRes)
	if !full || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != alice.ID || dialogs[0].TopMessage != noteID {
		t.Fatalf("pinned list = %+v, want self note", dialogs)
	}

	excluded, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{
		ExcludePinned: true,
		Limit:         20,
	})
	if err != nil {
		t.Fatalf("getSavedDialogs exclude: %v", err)
	}
	dialogs, _, _, count, full := savedDialogPage(t, excluded)
	if !full || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("exclude pinned = %+v, want only bob", dialogs)
	}
	if count != 1 {
		t.Fatalf("exclude pinned count = %d, want 1", count)
	}

	pinBobReq := &tg.MessagesToggleSavedDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}}}
	pinBobReq.SetPinned(true)
	ok, err = r.onMessagesToggleSavedDialogPin(WithUserID(ctx, alice.ID), pinBobReq)
	if err != nil || !ok {
		t.Fatalf("pin bob sublist = %v err %v", ok, err)
	}
	// 新置顶插到最前。
	pinnedRes, err = r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs: %v", err)
	}
	dialogs, _, _, _, _ = savedDialogPage(t, pinnedRes)
	if len(dialogs) != 2 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("pinned order after pin bob = %+v, want bob first", dialogs)
	}

	ok, err = r.onMessagesReorderPinnedSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesReorderPinnedSavedDialogsRequest{
		Order: []tg.InputDialogPeerClass{
			&tg.InputDialogPeer{Peer: &tg.InputPeerSelf{}},
			&tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}},
		},
	})
	if err != nil || !ok {
		t.Fatalf("reorder = %v err %v", ok, err)
	}
	pinnedRes, err = r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs after reorder: %v", err)
	}
	dialogs, _, _, _, _ = savedDialogPage(t, pinnedRes)
	if len(dialogs) != 2 || savedDialogPeerID(t, dialogs[0]) != alice.ID || savedDialogPeerID(t, dialogs[1]) != bob.ID {
		t.Fatalf("reorder result = %+v, want self then bob", dialogs)
	}

	ok, err = r.onMessagesToggleSavedDialogPin(WithUserID(ctx, alice.ID), &tg.MessagesToggleSavedDialogPinRequest{
		Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerSelf{}},
	})
	if err != nil || !ok {
		t.Fatalf("unpin self = %v err %v", ok, err)
	}
	pinnedRes, err = r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs after unpin: %v", err)
	}
	dialogs, _, _, _, _ = savedDialogPage(t, pinnedRes)
	if len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("after unpin = %+v, want only bob", dialogs)
	}
}

// TestMessagesGetSavedDialogsByID 验证指定查询：命中返回、未知 peer 静默缺席。
func TestMessagesGetSavedDialogsByID(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, _, fwdID := savedDialogsFixture(t)

	res, err := r.onMessagesGetSavedDialogsByID(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsByIDRequest{
		IDs: []tg.InputPeerClass{
			&tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
			&tg.InputPeerUser{UserID: 999999, AccessHash: 1},
		},
	})
	if err != nil {
		t.Fatalf("getSavedDialogsByID: %v", err)
	}
	dialogs, _, _, _, full := savedDialogPage(t, res)
	if !full || len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID || dialogs[0].TopMessage != fwdID {
		t.Fatalf("byID = %+v, want bob sublist only", dialogs)
	}
}

// TestMessagesGetSavedHistoryFiltersBySavedPeer 验证子会话历史过滤、
// count 与 hash notModified。
func TestMessagesGetSavedHistoryFiltersBySavedPeer(t *testing.T) {
	ctx := context.Background()
	r, _, alice, bob, noteID, fwdID := savedDialogsFixture(t)

	bobHistory, err := r.onMessagesGetSavedHistory(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("getSavedHistory bob: %v", err)
	}
	bobMsgs := savedHistoryMessages(t, bobHistory)
	if len(bobMsgs) != 1 || bobMsgs[0].ID != fwdID {
		t.Fatalf("bob saved history = %+v, want forwarded message only", bobMsgs)
	}

	selfHistory, err := r.onMessagesGetSavedHistory(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerSelf{},
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("getSavedHistory self: %v", err)
	}
	selfMsgs := savedHistoryMessages(t, selfHistory)
	if len(selfMsgs) != 1 || selfMsgs[0].ID != noteID {
		t.Fatalf("self saved history = %+v, want note only", selfMsgs)
	}

	// max_id 边界：低于转发 id 时空。
	empty, err := r.onMessagesGetSavedHistory(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedHistoryRequest{
		Peer:  &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		MaxID: fwdID,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("getSavedHistory max_id: %v", err)
	}
	if msgs := savedHistoryMessages(t, empty); len(msgs) != 0 {
		t.Fatalf("max_id bounded history = %+v, want empty", msgs)
	}
}

func savedHistoryMessages(t *testing.T, res tg.MessagesMessagesClass) []*tg.Message {
	t.Helper()
	var raw []tg.MessageClass
	switch v := res.(type) {
	case *tg.MessagesMessages:
		raw = v.Messages
	case *tg.MessagesMessagesSlice:
		raw = v.Messages
	default:
		t.Fatalf("saved history = %T", res)
	}
	out := make([]*tg.Message, 0, len(raw))
	for _, m := range raw {
		msg, ok := m.(*tg.Message)
		if !ok {
			t.Fatalf("message = %T", m)
		}
		out = append(out, msg)
	}
	return out
}

// TestMessagesDeleteSavedHistoryRemovesSublist 验证删除子会话：带 pts 的
// affectedHistory、子会话从列表消失、置顶清理、self-chat 历史同步消失。
func TestMessagesDeleteSavedHistoryRemovesSublist(t *testing.T) {
	ctx := context.Background()
	r, messageStore, alice, bob, noteID, fwdID := savedDialogsFixture(t)

	pinBobReq := &tg.MessagesToggleSavedDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}}}
	pinBobReq.SetPinned(true)
	if _, err := r.onMessagesToggleSavedDialogPin(WithUserID(ctx, alice.ID), pinBobReq); err != nil {
		t.Fatalf("pin bob: %v", err)
	}

	affected, err := r.onMessagesDeleteSavedHistory(WithUserID(ctx, alice.ID), &tg.MessagesDeleteSavedHistoryRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
	})
	if err != nil {
		t.Fatalf("deleteSavedHistory: %v", err)
	}
	if affected.Pts == 0 || affected.PtsCount != 1 || affected.Offset != 0 {
		t.Fatalf("affected = %+v, want pts advance for 1 message, no continuation", affected)
	}

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs after delete: %v", err)
	}
	dialogs, _, _, _, _ := savedDialogPage(t, res)
	if len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != alice.ID {
		t.Fatalf("dialogs after delete = %+v, want only self note", dialogs)
	}

	pinnedRes, err := r.onMessagesGetPinnedSavedDialogs(WithUserID(ctx, alice.ID))
	if err != nil {
		t.Fatalf("getPinnedSavedDialogs after delete: %v", err)
	}
	dialogs, _, _, _, _ = savedDialogPage(t, pinnedRes)
	if len(dialogs) != 0 {
		t.Fatalf("pinned after delete = %+v, want empty (pin cleared)", dialogs)
	}

	list, err := messageStore.ListByUser(ctx, alice.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID},
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("self-chat history: %v", err)
	}
	if len(list.Messages) != 1 || list.Messages[0].ID != noteID {
		t.Fatalf("self-chat history after delete = %+v, want only note (fwd %d gone)", list.Messages, fwdID)
	}
}

// TestModernForwardMessagesConstructorSavesToSelf 复现 Android「保存到收藏夹」
// 路径：DrKLO 12.7.x 发晚于 Layer 225 的 messages.forwardMessages#41d41ade，
// 服务端 500 时 tgnet 无限重试（UI 永久转圈）。compat 解码后必须等价于
// #13704a7c：转发成功、收藏夹出现来源子会话。
func TestModernForwardMessagesConstructorSavesToSelf(t *testing.T) {
	ctx := context.Background()
	r, messageStore, userStore := newSavedDialogsTestRouter(t)
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 83, Phone: "15550009401", FirstName: "Alice"})
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 84, Phone: "15550009402", FirstName: "Bob"})

	if _, err := r.onMessagesSendMessage(WithUserID(ctx, bob.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
		Message:  "save me",
		RandomID: 94001,
	}); err != nil {
		t.Fatalf("bob send: %v", err)
	}
	bobMsgID := privateTopMessageID(t, messageStore, alice.ID, bob.ID)

	// 按 DrKLO TL_messages_forwardMessages.serializeToStream 的线格式手工编码：
	// constructor + flags + from_peer + Vector<int> id + Vector<long> random_id + to_peer。
	var raw bin.Buffer
	raw.PutID(0x41d41ade)
	raw.PutInt(0) // flags：保存到收藏夹不带任何可选字段
	if err := (&tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash}).Encode(&raw); err != nil {
		t.Fatalf("encode from_peer: %v", err)
	}
	raw.PutVectorHeader(1)
	raw.PutInt(bobMsgID)
	raw.PutVectorHeader(1)
	raw.PutLong(94002)
	if err := (&tg.InputPeerSelf{}).Encode(&raw); err != nil {
		t.Fatalf("encode to_peer: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(androidClientContext(), alice.ID), [8]byte{}, 0, &raw)
	if err != nil {
		t.Fatalf("dispatch messages.forwardMessages#41d41ade: %v", err)
	}
	// The sparse dispatcher exposes the canonical concrete class result.
	if _, ok := enc.(tg.UpdatesClass); !ok {
		t.Fatalf("forward result = %T, want tg.UpdatesClass", enc)
	}

	res, err := r.onMessagesGetSavedDialogs(WithUserID(ctx, alice.ID), &tg.MessagesGetSavedDialogsRequest{Limit: 20})
	if err != nil {
		t.Fatalf("getSavedDialogs after android forward: %v", err)
	}
	dialogs, messages, _, _, _ := savedDialogPage(t, res)
	if len(dialogs) != 1 || savedDialogPeerID(t, dialogs[0]) != bob.ID {
		t.Fatalf("saved dialogs after android forward = %+v, want bob sublist", dialogs)
	}
	msg, ok := messages[0].(*tg.Message)
	if !ok {
		t.Fatalf("top message = %T", messages[0])
	}
	if saved, ok := msg.GetSavedPeerID(); !ok {
		t.Fatalf("forwarded message lacks saved_peer_id")
	} else if user, ok := saved.(*tg.PeerUser); !ok || user.UserID != bob.ID {
		t.Fatalf("saved_peer_id = %+v, want bob", saved)
	}
}

// TestSavedPeerForSelfChat 验证子会话分组键计算与 TDLib 语义对齐。
func TestSavedPeerForSelfChat(t *testing.T) {
	self := int64(1000000001)
	bobPeer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	cases := []struct {
		name    string
		forward *domain.MessageForward
		want    domain.Peer
	}{
		{"direct note", nil, domain.Peer{Type: domain.PeerTypeUser, ID: self}},
		{"forward with source", &domain.MessageForward{From: bobPeer, SavedFrom: bobPeer, SavedFromMsgID: 7}, bobPeer},
		{"forward channel source", &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeChannel, ID: 500}, SavedFrom: domain.Peer{Type: domain.PeerTypeChannel, ID: 500}}, domain.Peer{Type: domain.PeerTypeChannel, ID: 500}},
		{"hidden author without source", &domain.MessageForward{FromName: "Hidden"}, domain.Peer{Type: domain.PeerTypeUser, ID: domain.SavedHiddenAuthorUserID}},
		{"visible author without source", &domain.MessageForward{From: bobPeer}, domain.Peer{Type: domain.PeerTypeUser, ID: self}},
	}
	for _, tc := range cases {
		if got := domain.SavedPeerForSelfChat(self, tc.forward); got != tc.want {
			t.Fatalf("%s: SavedPeerForSelfChat = %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
