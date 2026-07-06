package mtprotoedge

import (
	"bytes"
	"context"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/mt"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
)

func TestEncryptOutboundFrameDecryptsWithGotdCipher(t *testing.T) {
	var key crypto.Key
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	authKey := key.WithID()
	body := mustEncodeTL(t, &mt.NewSessionCreated{
		FirstMsgID: 111,
		UniqueID:   222,
		ServerSalt: 333,
	})
	c := &Conn{
		cipher:    crypto.NewServerCipher(rand.Reader),
		key:       authKey,
		salt:      12345,
		sessionID: 67890,
	}
	out, err := c.encryptOutboundFrame(&outboundFrame{
		msgID:  7649066000000000001,
		seqNo:  1,
		typeID: mt.NewSessionCreatedTypeID,
		body:   body,
	})
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	data, err := crypto.NewClientCipher(rand.Reader).DecryptFromBuffer(authKey, &bin.Buffer{Buf: append([]byte(nil), out.Raw()...)})
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if data.Salt != c.salt || data.SessionID != c.sessionID {
		t.Fatalf("salt/session = %d/%d, want %d/%d", data.Salt, data.SessionID, c.salt, c.sessionID)
	}
	if got := data.Data(); !bytes.Equal(got, body) {
		t.Fatalf("body = %x, want %x", got, body)
	}
}

func TestOutboundActorSerializesConcurrentSends(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	sendEncrypted(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), &mt.PingRequest{PingID: 1})
	collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)
	srv.Conns().SetReceivesUpdates(auth.SessionID, true)

	const sends = 64
	var wg sync.WaitGroup
	errs := make(chan error, sends)
	for i := 0; i < sends; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errs <- srv.Conns().PushToSession(ctx, auth.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("push: %v", err)
		}
	}

	var prevMsgID int64
	var prevSeqNo int32 = -1
	for i := 0; i < sends; i++ {
		data, id, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
		if id != tg.UpdatesTooLongTypeID {
			t.Fatalf("message %d type = %#x, want updatesTooLong", i, id)
		}
		if i > 0 && data.MessageID <= prevMsgID {
			t.Fatalf("message %d msg_id = %d after %d, want strictly increasing", i, data.MessageID, prevMsgID)
		}
		if data.SeqNo%2 != 1 {
			t.Fatalf("message %d seq_no = %d, want odd content-related seq_no", i, data.SeqNo)
		}
		if i > 0 && data.SeqNo <= prevSeqNo {
			t.Fatalf("message %d seq_no = %d after %d, want increasing", i, data.SeqNo, prevSeqNo)
		}
		prevMsgID = data.MessageID
		prevSeqNo = data.SeqNo
	}
}

func TestSendBestEffortQueueFullBehavior(t *testing.T) {
	c := &Conn{metrics: NopMetrics{}}
	c.outbound = make(chan outboundOp, 1)
	c.outboundControl = make(chan outboundOp, 1)
	c.outboundStop = make(chan struct{})
	// 占满普通队列，模拟出站拥塞。
	c.outbound <- outboundOp{}

	if err := c.SendBestEffort(context.Background(), proto.MessageFromServer, &mt.MsgsAck{}, 0); err != ErrOutboundQueueFull {
		t.Fatalf("timeout=0 on full queue: err = %v, want ErrOutboundQueueFull", err)
	}

	start := time.Now()
	if err := c.SendBestEffort(context.Background(), proto.MessageFromServer, &mt.MsgsAck{}, 30*time.Millisecond); err != ErrOutboundQueueFull {
		t.Fatalf("timeout=30ms on full queue: err = %v, want ErrOutboundQueueFull", err)
	}
	if waited := time.Since(start); waited < 30*time.Millisecond {
		t.Fatalf("timeout wait = %v, want >= 30ms", waited)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.SendBestEffort(canceled, proto.MessageFromServer, &mt.MsgsAck{}, time.Second); err != context.Canceled {
		t.Fatalf("canceled ctx on full queue: err = %v, want context.Canceled", err)
	}

	// 腾出队列后快路径应直接入队成功。
	<-c.outbound
	if err := c.SendBestEffort(context.Background(), proto.MessageFromServer, &mt.MsgsAck{}, 0); err != nil {
		t.Fatalf("enqueue after drain: %v", err)
	}
	if got := len(c.outbound); got != 1 {
		t.Fatalf("queued ops = %d, want 1", got)
	}
}

func TestFrameNeedsAckServiceExceptions(t *testing.T) {
	cases := []struct {
		name string
		id   uint32
		want bool
	}{
		{name: "pong", id: mt.PongTypeID, want: false},
		{name: "future_salts", id: mt.FutureSaltsTypeID, want: false},
		{name: "msgs_ack", id: mt.MsgsAckTypeID, want: false},
		{name: "updatesTooLong", id: tg.UpdatesTooLongTypeID, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := frameNeedsAck(tc.id); got != tc.want {
				t.Fatalf("frameNeedsAck(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestOutboundResendAndAckState(t *testing.T) {
	const dc = 2
	addr, pub, srv := startTestServer(t, Options{DC: dc})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)

	clientMsgID := proto.NewMessageIDGen(time.Now)
	sendEncrypted(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), &mt.PingRequest{PingID: 1})
	collectReplies(t, conn, cipher, auth.AuthKey, mt.MsgsAckTypeID)
	srv.Conns().SetReceivesUpdates(auth.SessionID, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := srv.Conns().PushToSession(ctx, auth.SessionID, proto.MessageFromServer, &tg.UpdatesTooLong{}); err != nil {
		cancel()
		t.Fatalf("push: %v", err)
	}
	cancel()

	original, id, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
	if id != tg.UpdatesTooLongTypeID {
		t.Fatalf("pushed type = %#x, want updatesTooLong", id)
	}

	resendReqID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, resendReqID, 3, &mt.MsgResendReq{MsgIDs: []int64{original.MessageID}})
	resent, resentType, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
	if resentType != tg.UpdatesTooLongTypeID {
		t.Fatalf("resent type = %#x, want updatesTooLong", resentType)
	}
	if resent.MessageID != original.MessageID || resent.SeqNo != original.SeqNo {
		t.Fatalf("resent frame = (msg_id=%d seq=%d), want original (msg_id=%d seq=%d)",
			resent.MessageID, resent.SeqNo, original.MessageID, original.SeqNo)
	}
	_, stateType, stateBuf := readServerMessage(t, conn, cipher, auth.AuthKey)
	if stateType != mt.MsgsStateInfoTypeID {
		t.Fatalf("state type = %#x, want msgs_state_info", stateType)
	}
	assertStateInfo(t, stateBuf, resendReqID, []byte{msgStateReceived})
	_, ackType, _ := readServerMessage(t, conn, cipher, auth.AuthKey)
	if ackType != mt.MsgsAckTypeID {
		t.Fatalf("ack type = %#x, want msgs_ack", ackType)
	}

	sendEncryptedWithSeq(t, conn, cipher, auth, clientMsgID.New(proto.MessageFromClient), 4, &mt.MsgsAck{MsgIDs: []int64{original.MessageID}})
	ackedResendReqID := clientMsgID.New(proto.MessageFromClient)
	sendEncryptedWithSeq(t, conn, cipher, auth, ackedResendReqID, 5, &mt.MsgResendReq{MsgIDs: []int64{original.MessageID}})
	_, ackedStateType, ackedStateBuf := readServerMessage(t, conn, cipher, auth.AuthKey)
	if ackedStateType != mt.MsgsStateInfoTypeID {
		t.Fatalf("after ack type = %#x, want msgs_state_info without resend", ackedStateType)
	}
	assertStateInfo(t, ackedStateBuf, ackedResendReqID, []byte{msgStateReceived})
}

func assertStateInfo(t *testing.T, b *bin.Buffer, reqMsgID int64, want []byte) {
	t.Helper()
	var info mt.MsgsStateInfo
	if err := info.Decode(b); err != nil {
		t.Fatalf("decode msgs_state_info: %v", err)
	}
	if info.ReqMsgID != reqMsgID {
		t.Fatalf("msgs_state_info.req_msg_id = %d, want %d", info.ReqMsgID, reqMsgID)
	}
	if string(info.Info) != string(want) {
		t.Fatalf("msgs_state_info.info = %v, want %v", []byte(info.Info), want)
	}
}
