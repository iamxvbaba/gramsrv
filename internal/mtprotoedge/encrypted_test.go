package mtprotoedge

import (
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/transport"
)

// TestEncryptedPingPong 验证 M2/M4：握手后 client 加密 ping，
// server 回 new_session_created + pong + msgs_ack。
func TestEncryptedPingPong(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	const pingID int64 = 0x1234beef
	pingMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, pingMsgID, &mt.PingRequest{PingID: pingID})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.PongTypeID)
	mustHave(t, replies, mt.NewSessionCreatedTypeID, "new_session_created")
	pongBuf := mustHave(t, replies, mt.PongTypeID, "pong")

	var pong mt.Pong
	if err := pong.Decode(pongBuf); err != nil {
		t.Fatalf("decode pong: %v", err)
	}
	if pong.PingID != pingID {
		t.Fatalf("pong.PingID = %#x, want %#x", pong.PingID, pingID)
	}
	if pong.MsgID != pingMsgID {
		t.Fatalf("pong.MsgID = %d, want %d (req msg id)", pong.MsgID, pingMsgID)
	}
}

// TestDuplicateMsgIDIdempotent 验证 M4：相同 msg_id 的重复 content 请求被幂等处理，
// server 重发已缓存的 rpc_result，并重新 ack，不重复执行业务。
func TestDuplicateMsgIDIdempotent(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	msgID := clientMsgID.New(proto.MessageFromClient)

	sendEncrypted(t, conn, cipher, auth, msgID, &mt.RPCDropAnswerRequest{ReqMsgID: msgID - 4})
	first := collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)
	mustHave(t, first, proto.ResultTypeID, "first rpc_result")

	// 相同 msg_id —— 幂等：重发已有 rpc_result，并重新 ack。
	sendEncrypted(t, conn, cipher, auth, msgID, &mt.RPCDropAnswerRequest{ReqMsgID: msgID - 4})
	second := collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)
	mustHave(t, second, proto.ResultTypeID, "resent rpc_result")
	mustHave(t, second, mt.MsgsAckTypeID, "second ack")
}

// TestGetFutureSalts 验证 MTProto service message get_future_salts 由连接层直接响应，
// 不再落到业务 RPC fallback。
func TestGetFutureSalts(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, reqMsgID, &mt.GetFutureSaltsRequest{Num: 32})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.FutureSaltsTypeID)
	buf := mustHave(t, replies, mt.FutureSaltsTypeID, "future_salts")

	var salts mt.FutureSalts
	if err := salts.Decode(buf); err != nil {
		t.Fatalf("decode future_salts: %v", err)
	}
	if salts.ReqMsgID != reqMsgID {
		t.Fatalf("future_salts.req_msg_id = %d, want %d", salts.ReqMsgID, reqMsgID)
	}
	if len(salts.Salts) != 1 {
		t.Fatalf("future_salts len = %d, want 1", len(salts.Salts))
	}
	if got := salts.Salts[0].Salt; got != auth.ServerSalt {
		t.Fatalf("future salt = %#x, want server salt %#x", got, auth.ServerSalt)
	}
	if salts.Salts[0].ValidSince > salts.Now || salts.Salts[0].ValidUntil <= salts.Now {
		t.Fatalf("future salt validity = [%d,%d], now %d", salts.Salts[0].ValidSince, salts.Salts[0].ValidUntil, salts.Now)
	}
}

// TestMsgsStateReq 验证 MTProto service message msgs_state_req 由连接层直接响应，
// 不再落到业务 RPC fallback。
func TestMsgsStateReq(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	asked := []int64{reqMsgID, reqMsgID - 4, reqMsgID + 4}
	sendEncrypted(t, conn, cipher, auth, reqMsgID, &mt.MsgsStateReq{MsgIDs: asked})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsStateInfoTypeID)
	buf := mustHave(t, replies, mt.MsgsStateInfoTypeID, "msgs_state_info")

	var info mt.MsgsStateInfo
	if err := info.Decode(buf); err != nil {
		t.Fatalf("decode msgs_state_info: %v", err)
	}
	if info.ReqMsgID != reqMsgID {
		t.Fatalf("msgs_state_info.req_msg_id = %d, want %d", info.ReqMsgID, reqMsgID)
	}
	if len(info.Info) != len(asked) {
		t.Fatalf("msgs_state_info len = %d, want %d", len(info.Info), len(asked))
	}
	want := []byte{4, 1, 3}
	for i, b := range info.Info {
		if b != want[i] {
			t.Fatalf("msgs_state_info[%d] = %d, want %d", i, b, want[i])
		}
	}
}

// TestMsgResendReq 验证 MTProto msg_resend_req 由连接层按状态查询兜底响应，
// 不会落入业务 RPC fallback。
func TestMsgResendReq(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	asked := []int64{reqMsgID, reqMsgID - 4, reqMsgID + 4}
	sendEncrypted(t, conn, cipher, auth, reqMsgID, &mt.MsgResendReq{MsgIDs: asked})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsStateInfoTypeID)
	buf := mustHave(t, replies, mt.MsgsStateInfoTypeID, "msgs_state_info")

	var info mt.MsgsStateInfo
	if err := info.Decode(buf); err != nil {
		t.Fatalf("decode msgs_state_info: %v", err)
	}
	if info.ReqMsgID != reqMsgID {
		t.Fatalf("msgs_state_info.req_msg_id = %d, want %d", info.ReqMsgID, reqMsgID)
	}
	if len(info.Info) != len(asked) {
		t.Fatalf("msgs_state_info len = %d, want %d", len(info.Info), len(asked))
	}
	want := []byte{4, 1, 3}
	for i, b := range info.Info {
		if b != want[i] {
			t.Fatalf("msgs_state_info[%d] = %d, want %d", i, b, want[i])
		}
	}
}

// TestDestroySession 验证 destroy_session 返回 raw DestroySessionRes，
// 避免客户端清理旧 session 时掉到 RPC fallback。
func TestDestroySession(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	targetSessionID := auth.SessionID + 4
	sendEncrypted(t, conn, cipher, auth, reqMsgID, &mt.DestroySessionRequest{SessionID: targetSessionID})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.DestroySessionNoneTypeID)
	buf := mustHave(t, replies, mt.DestroySessionNoneTypeID, "destroy_session_none")

	var res mt.DestroySessionNone
	if err := res.Decode(buf); err != nil {
		t.Fatalf("decode destroy_session_none: %v", err)
	}
	if res.SessionID != targetSessionID {
		t.Fatalf("destroy_session_none.session_id = %d, want %d", res.SessionID, targetSessionID)
	}
}

// TestRPCDropAnswer 验证 rpc_drop_answer 以 rpc_result 包装 RpcDropAnswer 返回，
// 与 gotd/td 和 TDesktop 的请求/响应模型对齐。
func TestRPCDropAnswer(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	droppedReqID := reqMsgID - 4
	sendEncrypted(t, conn, cipher, auth, reqMsgID, &mt.RPCDropAnswerRequest{ReqMsgID: droppedReqID})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, proto.ResultTypeID)
	buf := mustHave(t, replies, proto.ResultTypeID, "rpc_result")

	var result proto.Result
	if err := result.Decode(buf); err != nil {
		t.Fatalf("decode rpc_result: %v", err)
	}
	if result.RequestMessageID != reqMsgID {
		t.Fatalf("rpc_result.req_msg_id = %d, want %d", result.RequestMessageID, reqMsgID)
	}
	answer, err := mt.DecodeRPCDropAnswer(&bin.Buffer{Buf: result.Result})
	if err != nil {
		t.Fatalf("decode RpcDropAnswer: %v", err)
	}
	if _, ok := answer.(*mt.RPCAnswerUnknown); !ok {
		t.Fatalf("RpcDropAnswer = %T, want *mt.RPCAnswerUnknown", answer)
	}
}

// TestHTTPWaitInContainerDoesNotNeedAck 验证 http_wait 在 container 中被协议层吞掉，
// 但同 container 内的 ping 仍按 content-related service request 回 ack。
func TestHTTPWaitInContainerDoesNotNeedAck(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	waitMsgID := clientMsgID.New(proto.MessageFromClient)
	pingMsgID := clientMsgID.New(proto.MessageFromClient)
	containerMsgID := clientMsgID.New(proto.MessageFromClient)
	waitBody := mustEncodeTL(t, &mt.HTTPWaitRequest{MaxDelay: 0, WaitAfter: 0, MaxWait: 25_000})
	pingBody := mustEncodeTL(t, &mt.PingRequest{PingID: 7})
	sendEncrypted(t, conn, cipher, auth, containerMsgID, &proto.MessageContainer{
		Messages: []proto.Message{
			{ID: waitMsgID, SeqNo: 0, Bytes: len(waitBody), Body: waitBody},
			{ID: pingMsgID, SeqNo: 1, Bytes: len(pingBody), Body: pingBody},
		},
	})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)
	mustHave(t, replies, mt.PongTypeID, "pong")
	ackBuf := mustHave(t, replies, mt.MsgsAckTypeID, "msgs_ack")
	var ack mt.MsgsAck
	if err := ack.Decode(ackBuf); err != nil {
		t.Fatalf("decode msgs_ack: %v", err)
	}
	if len(ack.MsgIDs) != 1 || ack.MsgIDs[0] != pingMsgID {
		t.Fatalf("msgs_ack = %+v, want only ping msg_id %d", ack.MsgIDs, pingMsgID)
	}
}

// TestOldMessageInFreshContainerAccepted verifies TDesktop's bad_msg recovery
// path: an old request can be resent inside a fresh container msg_id.
func TestOldMessageInFreshContainerAccepted(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	oldMsgIDGen := proto.NewMessageIDGen(func() time.Time {
		return time.Now().Add(-10 * time.Minute)
	})
	freshMsgIDGen := proto.NewMessageIDGen(time.Now)
	oldPingMsgID := oldMsgIDGen.New(proto.MessageFromClient)
	containerMsgID := freshMsgIDGen.New(proto.MessageFromClient)
	pingBody := mustEncodeTL(t, &mt.PingRequest{PingID: 42})

	sendEncrypted(t, conn, cipher, auth, containerMsgID, &proto.MessageContainer{
		Messages: []proto.Message{
			{ID: oldPingMsgID, SeqNo: 1, Bytes: len(pingBody), Body: pingBody},
		},
	})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.PongTypeID)
	buf := mustHave(t, replies, mt.PongTypeID, "pong")
	var pong mt.Pong
	if err := pong.Decode(buf); err != nil {
		t.Fatalf("decode pong: %v", err)
	}
	if pong.MsgID != oldPingMsgID || pong.PingID != 42 {
		t.Fatalf("pong = %+v, want msg_id=%d ping_id=42", pong, oldPingMsgID)
	}
}

func TestPingDelayDisconnectEvenSeqAccepted(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, reqMsgID, 0, &mt.PingDelayDisconnectRequest{
		PingID:          9,
		DisconnectDelay: 60,
	})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.PongTypeID)
	buf := mustHave(t, replies, mt.PongTypeID, "pong")
	var pong mt.Pong
	if err := pong.Decode(buf); err != nil {
		t.Fatalf("decode pong: %v", err)
	}
	if pong.MsgID != reqMsgID || pong.PingID != 9 {
		t.Fatalf("pong = %+v, want msg_id=%d ping_id=9", pong, reqMsgID)
	}
}

// TestDestroyAuthKey 验证 MTProto service message destroy_auth_key 由连接层直接响应，
// 避免 TDesktop 清理旧 key 时落到业务 RPC fallback。
func TestDestroyAuthKey(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncrypted(t, conn, cipher, auth, reqMsgID, &destroyAuthKeyRequest{})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, destroyAuthKeyOkTypeID)
	mustHave(t, replies, destroyAuthKeyOkTypeID, "destroy_auth_key_ok")
}

// TestBadServerSalt 验证客户端带错 server_salt 时 server 返回 bad_server_salt，
// 并携带当前 auth key 的权威 salt。
func TestBadServerSalt(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	wrongSalt := auth.ServerSalt + 1
	sendEncryptedWithSalt(t, conn, cipher, auth, wrongSalt, reqMsgID, &mt.PingRequest{PingID: 1})

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.BadServerSaltTypeID)
	buf := mustHave(t, replies, mt.BadServerSaltTypeID, "bad_server_salt")

	var bad mt.BadServerSalt
	if err := bad.Decode(buf); err != nil {
		t.Fatalf("decode bad_server_salt: %v", err)
	}
	if bad.BadMsgID != reqMsgID {
		t.Fatalf("bad_server_salt.bad_msg_id = %d, want %d", bad.BadMsgID, reqMsgID)
	}
	if bad.ErrorCode != 48 {
		t.Fatalf("bad_server_salt.error_code = %d, want 48", bad.ErrorCode)
	}
	if bad.NewServerSalt != auth.ServerSalt {
		t.Fatalf("bad_server_salt.new_server_salt = %#x, want %#x", bad.NewServerSalt, auth.ServerSalt)
	}
}

func TestBadMsgSeqOddExpected(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, reqMsgID, 0, &tg.HelpGetConfigRequest{})

	bad := readBadMsgNotification(t, conn, cipher, auth.AuthKey)
	if bad.BadMsgID != reqMsgID || bad.BadMsgSeqno != 0 || bad.ErrorCode != badMsgSeqNotOdd {
		t.Fatalf("bad_msg = %+v, want msg_id=%d seq=0 code=%d", bad, reqMsgID, badMsgSeqNotOdd)
	}
}

func TestBadMsgSeqEvenExpected(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	reqMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, reqMsgID, 1, &mt.MsgsAck{MsgIDs: []int64{reqMsgID}})

	bad := readBadMsgNotification(t, conn, cipher, auth.AuthKey)
	if bad.BadMsgID != reqMsgID || bad.BadMsgSeqno != 1 || bad.ErrorCode != badMsgSeqNotEven {
		t.Fatalf("bad_msg = %+v, want msg_id=%d seq=1 code=%d", bad, reqMsgID, badMsgSeqNotEven)
	}
}

func TestBadMsgSeqTooLow(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	firstMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, firstMsgID, 3, &tg.HelpGetConfigRequest{})
	collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)

	secondMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, secondMsgID, 1, &tg.HelpGetConfigRequest{})

	bad := readBadMsgNotification(t, conn, cipher, auth.AuthKey)
	if bad.BadMsgID != secondMsgID || bad.BadMsgSeqno != 1 || bad.ErrorCode != badMsgSeqTooLow {
		t.Fatalf("bad_msg = %+v, want msg_id=%d seq=1 code=%d", bad, secondMsgID, badMsgSeqTooLow)
	}
}

func TestBadMsgSeqTooHigh(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	lowMsgID := clientMsgID.New(proto.MessageFromClient)
	highMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, highMsgID, 1, &tg.HelpGetConfigRequest{})
	collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)

	sendEncryptedWithSeq(t, conn, cipher, auth, lowMsgID, 3, &tg.HelpGetConfigRequest{})

	bad := readBadMsgNotification(t, conn, cipher, auth.AuthKey)
	if bad.BadMsgID != lowMsgID || bad.BadMsgSeqno != 3 || bad.ErrorCode != badMsgSeqTooHigh {
		t.Fatalf("bad_msg = %+v, want msg_id=%d seq=3 code=%d", bad, lowMsgID, badMsgSeqTooHigh)
	}
}

func TestSessionChangeResetsClientSeqState(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	firstMsgID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, firstMsgID, 1, &tg.HelpGetConfigRequest{})
	collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)

	nextSessionID := auth.SessionID + 1
	if nextSessionID == 0 {
		nextSessionID++
	}
	secondMsgID := clientMsgID.New(proto.MessageFromClient)
	body := encodeClientMessageBodyForTest(t, &tg.HelpGetConfigRequest{})
	sendEncryptedWithSessionSaltAndSeq(t, conn, cipher, auth, nextSessionID, auth.ServerSalt, secondMsgID, 1, body)

	replies := collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)
	if _, ok := replies[mt.BadMsgNotificationTypeID]; ok {
		t.Fatalf("session change with fresh seq_no produced bad_msg_notification")
	}
	mustHave(t, replies, mt.NewSessionCreatedTypeID, "new_session_created after session change")
	mustHave(t, replies, mt.MsgsAckTypeID, "msgs_ack after session change")
}

func readBadMsgNotification(t *testing.T, conn transport.Conn, cipher crypto.Cipher, key crypto.AuthKey) mt.BadMsgNotification {
	t.Helper()
	replies := collectReplies(t, conn, cipher, key, mt.BadMsgNotificationTypeID)
	buf := mustHave(t, replies, mt.BadMsgNotificationTypeID, "bad_msg_notification")
	var bad mt.BadMsgNotification
	if err := bad.Decode(buf); err != nil {
		t.Fatalf("decode bad_msg_notification: %v", err)
	}
	return bad
}

func mustEncodeTL(t *testing.T, msg bin.Encoder) []byte {
	t.Helper()
	var b bin.Buffer
	if err := msg.Encode(&b); err != nil {
		t.Fatalf("encode TL: %v", err)
	}
	return b.Copy()
}
