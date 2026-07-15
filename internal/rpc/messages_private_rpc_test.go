package rpc

import (
	"context"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	appcontacts "telesrv/internal/app/contacts"
	appdialogs "telesrv/internal/app/dialogs"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
	"time"
)

func TestMessagesSendMultiMediaRateLimitCountsItems(t *testing.T) {
	const userID = int64(1000000001)
	limiter := &captureRateLimiter{block: true, retryAfter: 11}
	r := New(Config{SendRateLimit: 2, SendRateWindow: 3 * time.Second}, Deps{Limiter: limiter}, zaptest.NewLogger(t), clock.System)

	_, err := r.onMessagesSendMultiMedia(WithUserID(context.Background(), userID), &tg.MessagesSendMultiMediaRequest{
		Peer: &tg.InputPeerSelf{},
		MultiMedia: []tg.InputSingleMedia{
			{Media: &tg.InputMediaEmpty{}, RandomID: 1},
			{Media: &tg.InputMediaEmpty{}, RandomID: 2},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "FLOOD_WAIT") || !strings.Contains(err.Error(), "(11)") {
		t.Fatalf("sendMultiMedia rate err = %v, want FLOOD_WAIT 11", err)
	}
	if len(limiter.calls) != 1 || limiter.calls[0].cost != 2 {
		t.Fatalf("limiter calls = %+v, want one cost=2", limiter.calls)
	}
}

func TestMessagesPrivateBlockPreventsRecipientInboxAndRevokeRPC(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	alice, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550009101", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550009102", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	dialogs := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogs)
	contactStore := memory.NewContactStore()
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Contacts: appcontacts.NewService(contactStore, userStore),
		Messages: appmessages.NewService(messageStore, dialogs),
		Dialogs:  appdialogs.NewService(dialogs),
	}, zaptest.NewLogger(t), clock.System)

	delivered, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "before block",
		RandomID: 91001,
	})
	if err != nil {
		t.Fatalf("send before block: %v", err)
	}
	deliveredUpdates := delivered.(*tg.Updates)
	deliveredMsg := deliveredUpdates.Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)

	if ok, err := r.onContactsBlock(WithUserID(ctx, bob.ID), &tg.ContactsBlockRequest{
		ID: &tg.InputPeerUser{UserID: alice.ID, AccessHash: alice.AccessHash},
	}); err != nil || !ok {
		t.Fatalf("bob block alice = %v, %v", ok, err)
	}
	blockedSend, err := r.onMessagesSendMessage(WithUserID(ctx, alice.ID), &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bob.ID, AccessHash: bob.AccessHash},
		Message:  "after block",
		RandomID: 91002,
	})
	if err != nil {
		t.Fatalf("send after block: %v", err)
	}
	blockedUpdates := blockedSend.(*tg.Updates)
	blockedMsg := blockedUpdates.Updates[1].(*tg.UpdateNewMessage).Message.(*tg.Message)
	if blockedMsg.ID == 0 || !blockedMsg.Out {
		t.Fatalf("blocked sender update = %#v, want outgoing sender message", blockedMsg)
	}
	bobHistory, err := messageStore.ListByUser(ctx, bob.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("bob history: %v", err)
	}
	if len(bobHistory.Messages) != 1 || bobHistory.Messages[0].Body != "before block" {
		t.Fatalf("bob history = %+v, want only pre-block delivered message", bobHistory.Messages)
	}
	aliceHistory, err := messageStore.ListByUser(ctx, alice.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("alice history: %v", err)
	}
	if len(aliceHistory.Messages) != 2 {
		t.Fatalf("alice history len = %d, want delivered + sender-only blocked message", len(aliceHistory.Messages))
	}
	// 官方对私聊 revoke 不做 block 限制：被拉黑后撤回自己的消息仍双向
	// 生效（客户端本地已先删，服务端报错只会造成消息复活错乱）。
	deleteReq := &tg.MessagesDeleteMessagesRequest{ID: []int{deliveredMsg.ID}}
	deleteReq.SetRevoke(true)
	if _, err := r.onMessagesDeleteMessages(WithUserID(ctx, alice.ID), deleteReq); err != nil {
		t.Fatalf("revoke after block err = %v, want success", err)
	}
	bobAfter, err := messageStore.ListByUser(ctx, bob.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("bob history after revoke: %v", err)
	}
	if len(bobAfter.Messages) != 0 {
		t.Fatalf("bob history after revoke = %+v, want revoked message removed on blocked side too", bobAfter.Messages)
	}
}

func TestMessagesGetMessageEditDataPrivateValidatesAuthor(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	messages := &captureMessages{list: domain.MessageList{
		Messages: []domain.Message{{
			ID:          3,
			OwnerUserID: userID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: userID},
			Out:         true,
			Body:        "editable",
		}},
	}}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)

	got, err := r.onMessagesGetMessageEditData(WithUserID(context.Background(), userID), &tg.MessagesGetMessageEditDataRequest{
		Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		ID:   3,
	})
	if err != nil {
		t.Fatalf("get private edit data: %v", err)
	}
	if got.GetCaption() {
		t.Fatalf("private edit data caption = true, want false for text-only message")
	}

	messages.list.Messages[0].Out = false
	if _, err := r.onMessagesGetMessageEditData(WithUserID(context.Background(), userID), &tg.MessagesGetMessageEditDataRequest{
		Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		ID:   3,
	}); err == nil || !strings.Contains(err.Error(), "MESSAGE_AUTHOR_REQUIRED") {
		t.Fatalf("non-author edit data err = %v, want MESSAGE_AUTHOR_REQUIRED", err)
	}
}
