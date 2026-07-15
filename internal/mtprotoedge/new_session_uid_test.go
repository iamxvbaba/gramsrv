package mtprotoedge

import (
	"testing"
	"time"

	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

// TestNewSessionCreatedUniqueIDPerSession 验证两次 session 建立收到的
// new_session_created.unique_id 互不相同。客户端按 unique_id 去重，复用同一值
// 会让断线重连后的 new_session_created 被吞掉，依赖它触发的差分补拉随之丢失。
func TestNewSessionCreatedUniqueIDPerSession(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	firstMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, firstMsgID, 1, &tg.HelpGetConfigRequest{})
	first := collectReplies(t, conn, cipher, auth.AuthKey, mt.NewSessionCreatedTypeID)
	var created1 mt.NewSessionCreated
	if err := created1.Decode(mustHave(t, first, mt.NewSessionCreatedTypeID, "first new_session_created")); err != nil {
		t.Fatalf("decode first new_session_created: %v", err)
	}

	nextSessionID := auth.SessionID + 1
	if nextSessionID == 0 {
		nextSessionID++
	}
	secondMsgID := clientMsgID.New(proto.MessageFromClient)
	body := encodeClientMessageBodyForTest(t, &tg.HelpGetConfigRequest{})
	sendEncryptedWithSessionSaltAndSeq(t, conn, cipher, auth, nextSessionID, auth.ServerSalt, secondMsgID, 1, body)
	second := collectReplies(t, conn, cipher, auth.AuthKey, mt.NewSessionCreatedTypeID)
	var created2 mt.NewSessionCreated
	if err := created2.Decode(mustHave(t, second, mt.NewSessionCreatedTypeID, "second new_session_created")); err != nil {
		t.Fatalf("decode second new_session_created: %v", err)
	}

	if created1.UniqueID == created2.UniqueID {
		t.Fatalf("new_session_created.unique_id reused across sessions: %d", created1.UniqueID)
	}
}

// TestNewSessionCreatedMovesFloorForLaterLowerMessage locks the MTProto rule
// that a server-side session must publish a fresh boundary notification when it
// later accepts a smaller logical client msg_id. Official clients use this
// boundary to decide which requests must be regenerated and resent.
func TestNewSessionCreatedMovesFloorForLaterLowerMessage(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	freshIDs := proto.NewMessageIDGen(time.Now)
	firstMsgID := freshIDs.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, firstMsgID, 0, &mt.HTTPWaitRequest{
		MaxDelay:  0,
		WaitAfter: 0,
		MaxWait:   25_000,
	})
	first := collectReplies(t, conn, cipher, auth.AuthKey, mt.NewSessionCreatedTypeID)
	var created1 mt.NewSessionCreated
	if err := created1.Decode(mustHave(t, first, mt.NewSessionCreatedTypeID, "first new_session_created")); err != nil {
		t.Fatalf("decode first new_session_created: %v", err)
	}
	if created1.FirstMsgID != firstMsgID {
		t.Fatalf("first boundary = %d, want %d", created1.FirstMsgID, firstMsgID)
	}

	oldIDs := proto.NewMessageIDGen(func() time.Time { return time.Now().Add(-10 * time.Minute) })
	lowerMsgID := oldIDs.New(proto.MessageFromClient)
	outerMsgID := freshIDs.New(proto.MessageFromClient)
	pingBody := mustEncodeTL(t, &mt.PingRequest{PingID: 99})
	sendEncrypted(t, conn, cipher, auth, outerMsgID, &proto.MessageContainer{
		Messages: []proto.Message{{
			ID: lowerMsgID, SeqNo: 1, Bytes: len(pingBody), Body: pingBody,
		}},
	})
	second := collectReplies(t, conn, cipher, auth.AuthKey, mt.PongTypeID)
	var created2 mt.NewSessionCreated
	if err := created2.Decode(mustHave(t, second, mt.NewSessionCreatedTypeID, "lower-boundary new_session_created")); err != nil {
		t.Fatalf("decode lower-boundary new_session_created: %v", err)
	}
	if created2.FirstMsgID != lowerMsgID {
		t.Fatalf("lower boundary = %d, want %d", created2.FirstMsgID, lowerMsgID)
	}
	if created2.UniqueID == created1.UniqueID {
		t.Fatalf("lower-boundary notification reused unique_id %d", created2.UniqueID)
	}
}
