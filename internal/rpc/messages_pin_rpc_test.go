package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appcontacts "telesrv/internal/app/contacts"
	appdialogs "telesrv/internal/app/dialogs"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newPinTestRouter(t *testing.T) (*Router, *memory.MessageStore, *memory.UserStore) {
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

func findPinnedMessagesUpdate(t *testing.T, updates tg.UpdatesClass) *tg.UpdatePinnedMessages {
	t.Helper()
	box, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, update := range box.Updates {
		if pinned, ok := update.(*tg.UpdatePinnedMessages); ok {
			return pinned
		}
	}
	return nil
}

func findServiceMessageUpdate(t *testing.T, updates tg.UpdatesClass) *tg.MessageService {
	t.Helper()
	box, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates = %T, want *tg.Updates", updates)
	}
	for _, update := range box.Updates {
		if newMsg, ok := update.(*tg.UpdateNewMessage); ok {
			if svc, ok := newMsg.Message.(*tg.MessageService); ok {
				return svc
			}
		}
	}
	return nil
}

func privatePinnedIDs(t *testing.T, store *memory.MessageStore, ownerID, peerID int64) []int {
	t.Helper()
	list, err := store.ListByUser(context.Background(), ownerID, domain.MessageFilter{
		HasPeer:    true,
		Peer:       domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		PinnedOnly: true,
		Limit:      50,
	})
	if err != nil {
		t.Fatalf("list pinned: %v", err)
	}
	ids := make([]int, 0, len(list.Messages))
	for _, msg := range list.Messages {
		ids = append(ids, msg.ID)
	}
	return ids
}

// TestMessagesUpdatePinnedMessagePrivateSharedPin 验证私聊共享置顶全链路：
// 双侧 box 翻转、updatePinnedMessages 账号 pts 事件、messageActionPinMessage
// 服务消息（reply_to 指向被置顶消息）、filterPinned 搜索与 userFull 发现。
func TestMessagesUpdatePinnedMessagePrivateSharedPin(t *testing.T) {
	ctx := context.Background()
	r, messageStore, userStore := newPinTestRouter(t)
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 71, Phone: "15550009201", FirstName: "Alice"})
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 72, Phone: "15550009202", FirstName: "Bob"})

	sent, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "pin target",
		RandomID: 92001,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	aliceMsg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)

	bobList, err := messageStore.ListByUser(ctx, bob.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}, Limit: 10,
	})
	if err != nil || len(bobList.Messages) != 1 {
		t.Fatalf("bob list = %+v err %v, want delivered message", bobList.Messages, err)
	}
	bobBoxID := bobList.Messages[0].ID

	// 共享置顶（未设 pm_oneside = 官方"同时为对方置顶"勾选形态）。
	pinRes, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, alice.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ID:   aliceMsg.ID,
	})
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	pinnedUpdate := findPinnedMessagesUpdate(t, pinRes)
	if pinnedUpdate == nil || !pinnedUpdate.Pinned || pinnedUpdate.Pts == 0 || pinnedUpdate.PtsCount != 1 {
		t.Fatalf("pin update = %+v, want pinned with account pts", pinnedUpdate)
	}
	if len(pinnedUpdate.Messages) != 1 || pinnedUpdate.Messages[0] != aliceMsg.ID {
		t.Fatalf("pin update messages = %v, want [%d] (alice 视角 box id)", pinnedUpdate.Messages, aliceMsg.ID)
	}
	if peer, ok := pinnedUpdate.Peer.(*tg.PeerUser); !ok || peer.UserID != bob.ID {
		t.Fatalf("pin update peer = %+v, want bob", pinnedUpdate.Peer)
	}
	svc := findServiceMessageUpdate(t, pinRes)
	if svc == nil {
		t.Fatalf("pin response lacks messageService, want messageActionPinMessage")
	}
	if _, ok := svc.Action.(*tg.MessageActionPinMessage); !ok {
		t.Fatalf("service action = %T, want messageActionPinMessage", svc.Action)
	}
	reply, ok := svc.GetReplyTo()
	if !ok {
		t.Fatalf("pin service message lacks reply_to (客户端凭此渲染置顶预览)")
	}
	if header, ok := reply.(*tg.MessageReplyHeader); !ok || header.ReplyToMsgID != aliceMsg.ID {
		t.Fatalf("service reply_to = %+v, want alice box id %d", reply, aliceMsg.ID)
	}

	// 双侧置顶状态 + 对端服务消息（含对端视角 reply_to 翻译）。
	if ids := privatePinnedIDs(t, messageStore, alice.ID, bob.ID); len(ids) != 1 || ids[0] != aliceMsg.ID {
		t.Fatalf("alice pinned = %v, want [%d]", ids, aliceMsg.ID)
	}
	if ids := privatePinnedIDs(t, messageStore, bob.ID, alice.ID); len(ids) != 1 || ids[0] != bobBoxID {
		t.Fatalf("bob pinned = %v, want [%d] (bob 视角 box id)", ids, bobBoxID)
	}
	bobAfter, _ := messageStore.ListByUser(ctx, bob.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}, Limit: 10,
	})
	foundService := false
	for _, msg := range bobAfter.Messages {
		if msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindService &&
			msg.Media.ServiceAction != nil && msg.Media.ServiceAction.Kind == domain.MessageServiceActionPinMessage {
			foundService = true
			if msg.ReplyTo == nil || msg.ReplyTo.MessageID != bobBoxID {
				t.Fatalf("bob service reply = %+v, want bob box id %d", msg.ReplyTo, bobBoxID)
			}
		}
	}
	if !foundService {
		t.Fatalf("bob history lacks pin service message: %+v", bobAfter.Messages)
	}

	// 幂等：重复 pin 返回空 updates，不再生成服务消息。
	repeat, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, alice.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ID:   aliceMsg.ID,
	})
	if err != nil {
		t.Fatalf("repeat pin: %v", err)
	}
	if update := findPinnedMessagesUpdate(t, repeat); update != nil {
		t.Fatalf("repeat pin update = %+v, want no-op", update)
	}

	// 搜索发现链路：filterPinned（bob 视角）带总数与 message.pinned 标志。
	searchReq := &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
		Filter: &tg.InputMessagesFilterPinned{},
		Limit:  10,
	}
	var in bin.Buffer
	if err := searchReq.Encode(&in); err != nil {
		t.Fatalf("encode pinned search: %v", err)
	}
	enc, err := r.Dispatch(WithUserID(ctx, bob.ID), [8]byte{}, 0, &in)
	if err != nil {
		t.Fatalf("dispatch pinned search: %v", err)
	}
	pinnedMessages, _, _ := searchMessagesPayload(t, enc)
	if len(pinnedMessages) != 1 {
		t.Fatalf("pinned search len = %d, want 1", len(pinnedMessages))
	}
	if msg := pinnedMessages[0].(*tg.Message); msg.ID != bobBoxID || !msg.Pinned {
		t.Fatalf("pinned search msg = id %d pinned %v, want bob box id %d pinned", msg.ID, msg.Pinned, bobBoxID)
	}

	// userFull 发现链路：pinned_msg_id + can_pin_message。
	full, err := r.onUsersGetFullUser(WithUserID(ctx, alice.ID), &tg.InputUser{UserID: bob.ID, AccessHash: bob.AccessHash})
	if err != nil {
		t.Fatalf("get full user: %v", err)
	}
	if pinnedID, ok := full.FullUser.GetPinnedMsgID(); !ok || pinnedID != aliceMsg.ID {
		t.Fatalf("userFull pinned_msg_id = %d (%v), want %d", pinnedID, ok, aliceMsg.ID)
	}
	if !full.FullUser.CanPinMessage {
		t.Fatalf("userFull can_pin_message = false, want true")
	}

	// unpin：双侧清除，事件 pinned=false。
	unpinReq := &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ID:   aliceMsg.ID,
	}
	unpinReq.SetUnpin(true)
	unpinRes, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, alice.ID), unpinReq)
	if err != nil {
		t.Fatalf("unpin: %v", err)
	}
	unpinUpdate := findPinnedMessagesUpdate(t, unpinRes)
	if unpinUpdate == nil || unpinUpdate.Pinned {
		t.Fatalf("unpin update = %+v, want pinned=false", unpinUpdate)
	}
	if findServiceMessageUpdate(t, unpinRes) != nil {
		t.Fatalf("unpin generated service message, official unpin emits none")
	}
	if ids := privatePinnedIDs(t, messageStore, alice.ID, bob.ID); len(ids) != 0 {
		t.Fatalf("alice pinned after unpin = %v, want empty", ids)
	}
	if ids := privatePinnedIDs(t, messageStore, bob.ID, alice.ID); len(ids) != 0 {
		t.Fatalf("bob pinned after unpin = %v, want empty (共享置顶 unpin 双侧传播)", ids)
	}
}

// TestMessagesUpdatePinnedMessagePrivateOnesideAndUnpinAll 验证 pm_oneside
// 单侧置顶（不传播、无服务消息）与 unpinAllMessages 清扫。
func TestMessagesUpdatePinnedMessagePrivateOnesideAndUnpinAll(t *testing.T) {
	ctx := context.Background()
	r, messageStore, userStore := newPinTestRouter(t)
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 73, Phone: "15550009203", FirstName: "Alice"})
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 74, Phone: "15550009204", FirstName: "Bob"})

	sent, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "oneside target",
		RandomID: 92011,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	aliceMsg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)
	bobBefore, _ := messageStore.ListByUser(ctx, bob.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}, Limit: 10,
	})

	// pm_oneside = 官方私聊置顶框默认形态（未勾选"同时为对方置顶"）。
	onesideReq := &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ID:   aliceMsg.ID,
	}
	onesideReq.SetPmOneside(true)
	onesideRes, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, alice.ID), onesideReq)
	if err != nil {
		t.Fatalf("oneside pin: %v", err)
	}
	if update := findPinnedMessagesUpdate(t, onesideRes); update == nil || !update.Pinned {
		t.Fatalf("oneside pin update = %+v, want pinned", update)
	}
	if findServiceMessageUpdate(t, onesideRes) != nil {
		t.Fatalf("oneside pin generated service message, want none")
	}
	if ids := privatePinnedIDs(t, messageStore, alice.ID, bob.ID); len(ids) != 1 {
		t.Fatalf("alice oneside pinned = %v, want 1", ids)
	}
	if ids := privatePinnedIDs(t, messageStore, bob.ID, alice.ID); len(ids) != 0 {
		t.Fatalf("bob pinned after oneside = %v, want empty (单侧不传播)", ids)
	}
	bobAfter, _ := messageStore.ListByUser(ctx, bob.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}, Limit: 10,
	})
	if len(bobAfter.Messages) != len(bobBefore.Messages) {
		t.Fatalf("bob history grew %d -> %d after oneside pin, want unchanged", len(bobBefore.Messages), len(bobAfter.Messages))
	}

	// 单侧→共享升级：同一消息再发不带 pm_oneside 的 pin，必须向对端
	// 传播并生成服务消息（own 侧已置顶不构成整体 no-op）。
	upgradeRes, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, alice.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ID:   aliceMsg.ID,
	})
	if err != nil {
		t.Fatalf("upgrade pin: %v", err)
	}
	if ids := privatePinnedIDs(t, messageStore, bob.ID, alice.ID); len(ids) != 1 {
		t.Fatalf("bob pinned after upgrade = %v, want shared pin propagated", ids)
	}
	if findServiceMessageUpdate(t, upgradeRes) == nil {
		t.Fatalf("upgrade pin lacks service message, want messageActionPinMessage")
	}

	// 再追加一条共享置顶，然后 unpinAll 一次清掉两条。
	sent2, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "shared target",
		RandomID: 92012,
	})
	if err != nil {
		t.Fatalf("send shared: %v", err)
	}
	sharedMsg := sent2.(*tg.Updates).Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)
	if _, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, alice.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ID:   sharedMsg.ID,
	}); err != nil {
		t.Fatalf("shared pin: %v", err)
	}
	if ids := privatePinnedIDs(t, messageStore, alice.ID, bob.ID); len(ids) != 2 {
		t.Fatalf("alice pinned = %v, want 2 before unpinAll", ids)
	}
	if ids := privatePinnedIDs(t, messageStore, bob.ID, alice.ID); len(ids) != 2 {
		t.Fatalf("bob pinned = %v, want 2 shared before unpinAll", ids)
	}

	affected, err := r.onMessagesUnpinAllMessages(WithUserID(ctx, alice.ID), &tg.MessagesUnpinAllMessagesRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
	})
	if err != nil {
		t.Fatalf("unpinAll: %v", err)
	}
	if affected.Pts == 0 || affected.Offset != 0 {
		t.Fatalf("unpinAll affected = %+v, want account pts and offset 0", affected)
	}
	if ids := privatePinnedIDs(t, messageStore, alice.ID, bob.ID); len(ids) != 0 {
		t.Fatalf("alice pinned after unpinAll = %v, want empty", ids)
	}
	if ids := privatePinnedIDs(t, messageStore, bob.ID, alice.ID); len(ids) != 0 {
		t.Fatalf("bob pinned after unpinAll = %v, want empty (共享置顶同步清除)", ids)
	}
}

// TestMessagesUpdatePinnedMessageSavedMessagesAndServiceGuard 验证 Saved
// Messages 置顶（无服务消息）与服务消息禁止置顶。
func TestMessagesUpdatePinnedMessageSavedMessagesAndServiceGuard(t *testing.T) {
	ctx := context.Background()
	r, messageStore, userStore := newPinTestRouter(t)
	alice, _ := userStore.Create(ctx, domain.User{AccessHash: 75, Phone: "15550009205", FirstName: "Alice"})

	sent, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerSelf{},
		Message:  "saved note",
		RandomID: 92021,
	})
	if err != nil {
		t.Fatalf("send saved: %v", err)
	}
	savedMsg := sent.(*tg.Updates).Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)

	pinRes, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, alice.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerSelf{},
		ID:   savedMsg.ID,
	})
	if err != nil {
		t.Fatalf("pin saved: %v", err)
	}
	if update := findPinnedMessagesUpdate(t, pinRes); update == nil || !update.Pinned {
		t.Fatalf("saved pin update = %+v, want pinned", update)
	}
	if findServiceMessageUpdate(t, pinRes) != nil {
		t.Fatalf("saved messages pin generated service message, want none (无人可通知)")
	}
	list, _ := messageStore.ListByUser(ctx, alice.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}, Limit: 10,
	})
	if len(list.Messages) != 1 || !list.Messages[0].Pinned {
		t.Fatalf("saved history = %+v, want single pinned message without service message", list.Messages)
	}

	// 服务消息不可置顶：先用共享置顶在双人聊天里生成一条服务消息再试。
	bob, _ := userStore.Create(ctx, domain.User{AccessHash: 76, Phone: "15550009206", FirstName: "Bob"})
	sent2, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "to pin",
		RandomID: 92022,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	target := sent2.(*tg.Updates).Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)
	if _, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, alice.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ID:   target.ID,
	}); err != nil {
		t.Fatalf("pin for service msg: %v", err)
	}
	aliceList, _ := messageStore.ListByUser(ctx, alice.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID}, Limit: 10,
	})
	serviceID := 0
	for _, msg := range aliceList.Messages {
		if msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindService {
			serviceID = msg.ID
		}
	}
	if serviceID == 0 {
		t.Fatalf("no service message found in alice history: %+v", aliceList.Messages)
	}
	if _, err := r.onMessagesUpdatePinnedMessage(WithUserID(ctx, alice.ID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		ID:   serviceID,
	}); err == nil {
		t.Fatalf("pin service message succeeded, want MESSAGE_ID_INVALID")
	}
}
