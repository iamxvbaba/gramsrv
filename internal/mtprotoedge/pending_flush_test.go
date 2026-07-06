package mtprotoedge

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
)

// TestSetReceivesUpdatesFlushesPendingBeforeActivation 验证置位时先排空暂存推送
// 再激活 receivesUpdates：客户端必须真实收到暂存消息，且激活最终完成。
// 排空失败时 receivesUpdates 保持 false、暂存回排，由下一次置位重试。
func TestSetReceivesUpdatesFlushesPendingBeforeActivation(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	sendEncryptedWithSeq(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), 1, &tg.HelpGetConfigRequest{})
	collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)

	raw := auth.AuthKey.ID
	ctx := context.Background()

	// 完全就绪还要求 membership 路由建立（ReceivesUpdatesForAuthKey 的另一半条件）。
	srv.Conns().BindUserForAuthKey(raw, auth.SessionID, 100)
	srv.Conns().SetSessionChannelMemberships(raw, auth.SessionID, 100, nil, srv.Conns().ChannelMembershipGeneration(raw, auth.SessionID))

	// 未就绪：推送进 pending 而非直发。
	for i := 0; i < 2; i++ {
		if err := srv.Conns().PushToSessionForAuthKey(ctx, raw, auth.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
			t.Fatalf("queue pending push: %v", err)
		}
	}
	if srv.Conns().ReceivesUpdatesForAuthKey(raw, auth.SessionID) {
		t.Fatal("session ready before activation")
	}

	srv.Conns().SetReceivesUpdatesForAuthKey(raw, auth.SessionID, true)

	// 客户端应收到暂存的 updatesTooLong（flush 直发）。
	replies := collectReplies(t, conn, cipher, auth.AuthKey, tg.UpdatesTooLongTypeID)
	mustHave(t, replies, tg.UpdatesTooLongTypeID, "flushed pending push")

	// 排空完成后 receivesUpdates 才置位（异步，轮询等待）。
	deadline := time.Now().Add(5 * time.Second)
	for !srv.Conns().ReceivesUpdatesForAuthKey(raw, auth.SessionID) {
		if time.Now().After(deadline) {
			t.Fatal("session never became ready after flush")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
