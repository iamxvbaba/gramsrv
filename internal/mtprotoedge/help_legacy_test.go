package mtprotoedge

import (
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/mt"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

type trailingHelpTestRequest struct{}

func (*trailingHelpTestRequest) Encode(b *bin.Buffer) error {
	b.PutID(helpTestRequestTypeID)
	b.PutID(0xdeadbeef)
	return nil
}

func (*trailingHelpTestRequest) Decode(b *bin.Buffer) error {
	if err := b.ConsumeID(helpTestRequestTypeID); err != nil {
		return err
	}
	_, err := b.ID()
	return err
}

// TestHelpTestLegacyTerminal verifies that the removed help.test#c0e202f7 = Bool
// method is answered with a fixed boolTrue both bare and along the official
// invokeWithLayer/initConnection wrapper path, and that the connection survives.
func TestHelpTestLegacyTerminal(t *testing.T) {
	tests := []struct {
		name    string
		layer   int
		wrapped bool
	}{
		{name: "bare"},
		{name: "layer227_wrapped", layer: 227, wrapped: true},
		{name: "layer228_wrapped", layer: 228, wrapped: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const dc = 2
			addr, pub, _ := startTestServer(t, Options{DC: dc, LayerRPC: newAdmissionOnlyLayerRPC()})
			conn, auth, cipher := dialHandshake(t, addr, dc, pub)

			var request bin.Encoder = &helpTestRequest{}
			if test.wrapped {
				request = &tg.InvokeWithLayerRequest{
					Layer: test.layer,
					Query: &tg.InitConnectionRequest{
						APIID: 1, DeviceModel: "help-test", SystemVersion: "test",
						AppVersion: "test", SystemLangCode: "en", LangCode: "en",
						Query: &helpTestRequest{},
					},
				}
			}
			clientMsgID := proto.NewMessageIDGen(time.Now)
			reqMsgID := clientMsgID.New(proto.MessageFromClient)
			sendEncrypted(t, conn, cipher, auth, reqMsgID, request)

			replies := collectReplies(t, conn, cipher, auth.AuthKey, proto.ResultTypeID)
			assertHelpTestRPCResult(t, mustHave(t, replies, proto.ResultTypeID, "help.test rpc_result"), reqMsgID)

			pingMsgID := clientMsgID.New(proto.MessageFromClient)
			sendEncryptedWithSeq(t, conn, cipher, auth, pingMsgID, 3, &mt.PingRequest{PingID: 97})
			pongReplies := collectReplies(t, conn, cipher, auth.AuthKey, mt.PongTypeID)
			var pong mt.Pong
			if err := pong.Decode(mustHave(t, pongReplies, mt.PongTypeID, "pong after help.test")); err != nil {
				t.Fatalf("decode pong after help.test: %v", err)
			}
			if pong.MsgID != pingMsgID || pong.PingID != 97 {
				t.Fatalf("pong after help.test = %+v", pong)
			}
		})
	}
}

// TestWrappedHelpTestTrailingBytesRejected keeps the compatibility terminal
// exactly one word: trailing bytes stay a correlated INPUT_REQUEST_INVALID.
func TestWrappedHelpTestTrailingBytesRejected(t *testing.T) {
	const dc = 2
	addr, pub, _ := startTestServer(t, Options{DC: dc, LayerRPC: newAdmissionOnlyLayerRPC()})
	conn, auth, cipher := dialHandshake(t, addr, dc, pub)
	reqMsgID := proto.NewMessageIDGen(time.Now).New(proto.MessageFromClient)
	request := &tg.InvokeWithLayerRequest{
		Layer: 228,
		Query: &tg.InitConnectionRequest{
			APIID: 1, DeviceModel: "malformed-help-test", SystemVersion: "test",
			AppVersion: "test", SystemLangCode: "en", LangCode: "en",
			Query: &trailingHelpTestRequest{},
		},
	}
	sendEncrypted(t, conn, cipher, auth, reqMsgID, request)

	replies := collectReplies(t, conn, cipher, auth.AuthKey, proto.ResultTypeID)
	var result proto.Result
	if err := result.Decode(mustHave(t, replies, proto.ResultTypeID, "malformed help.test rpc_result")); err != nil {
		t.Fatalf("decode malformed help.test rpc_result: %v", err)
	}
	if result.RequestMessageID != reqMsgID {
		t.Fatalf("malformed help.test req_msg_id = %d, want %d", result.RequestMessageID, reqMsgID)
	}
	var rpcErr mt.RPCError
	if err := rpcErr.Decode(&bin.Buffer{Buf: result.Result}); err != nil {
		t.Fatalf("decode malformed help.test RPC error: %v", err)
	}
	if rpcErr.ErrorCode != 400 || rpcErr.ErrorMessage != "INPUT_REQUEST_INVALID" {
		t.Fatalf("malformed help.test RPC error = %+v", rpcErr)
	}
}

func assertHelpTestRPCResult(t *testing.T, b *bin.Buffer, reqMsgID int64) {
	t.Helper()
	var result proto.Result
	if err := result.Decode(b); err != nil {
		t.Fatalf("decode help.test rpc_result: %v", err)
	}
	if result.RequestMessageID != reqMsgID {
		t.Fatalf("help.test rpc_result.req_msg_id = %d, want %d", result.RequestMessageID, reqMsgID)
	}
	inner := &bin.Buffer{Buf: result.Result}
	innerID, err := inner.PeekID()
	if err != nil {
		t.Fatalf("peek help.test rpc_result inner: %v", err)
	}
	if innerID != boolTrueTypeID || inner.Len() != bin.Word {
		t.Fatalf("help.test rpc_result inner = %#x/%d bytes, want %#x/%d", innerID, inner.Len(), boolTrueTypeID, bin.Word)
	}
}
