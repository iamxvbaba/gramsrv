package mtprotoedge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
)

type countingLayerRPCResult struct {
	inner        tg.LayerRPCResult
	encodeCalls  atomic.Int32
	prepareCalls atomic.Int32
}

const (
	testChannelWireID227 uint32 = 0x1c32b11c
	testChannelWireID228 uint32 = 0xd49f34c6
)

func testChannelWireID(profile tg.LayerProfile) uint32 {
	if profile == tg.LayerProfile228 {
		return testChannelWireID228
	}
	return testChannelWireID227
}

func testOtherChannelWireID(profile tg.LayerProfile) uint32 {
	if profile == tg.LayerProfile228 {
		return testChannelWireID227
	}
	return testChannelWireID228
}

func testLayerChannel() *tg.Channel {
	return &tg.Channel{
		ID:    100,
		Title: "layer proof",
		Photo: &tg.ChatPhotoEmpty{},
		Date:  1,
	}
}

func (r *countingLayerRPCResult) Encode(b *bin.Buffer) error {
	r.encodeCalls.Add(1)
	return r.inner.Encode(b)
}

func (r *countingLayerRPCResult) Prepared() tg.LayerPreparedCall { return r.inner.Prepared() }

func (r *countingLayerRPCResult) WireInvariant() bool { return r.inner.WireInvariant() }

func (r *countingLayerRPCResult) Freeze() (tg.LayerFrozenResult, error) {
	return r.inner.Freeze()
}

func (r *countingLayerRPCResult) Prepare() (tg.LayerPreparedResult, error) {
	r.prepareCalls.Add(1)
	return r.inner.Prepare()
}

func TestExactLayerRPCResultEncodesDifferenceWithAdmittedCodec(t *testing.T) {
	for _, profile := range []tg.LayerProfile{tg.LayerProfile225, tg.LayerProfile227, tg.LayerProfile228} {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			testExactLayerRPCResultEncodesDifferenceWithAdmittedCodec(t, profile)
		})
	}
}

func testExactLayerRPCResultEncodesDifferenceWithAdmittedCodec(t *testing.T, profile tg.LayerProfile) {
	t.Helper()
	diff := &tg.UpdatesDifference{
		NewMessages: []tg.MessageClass{
			&tg.Message{
				ID:      2,
				FromID:  &tg.PeerUser{UserID: 3},
				PeerID:  &tg.PeerUser{UserID: 3},
				Date:    1,
				Message: "hi",
			},
		},
		NewEncryptedMessages: []tg.EncryptedMessageClass{},
		OtherUpdates:         []tg.UpdateClass{},
		Chats:                []tg.ChatClass{testLayerChannel()},
		Users:                []tg.UserClass{},
		State:                tg.UpdatesState{Pts: 2, Date: 1},
	}

	dispatcher := tg.NewServerDispatcher(nil)
	dispatcher.OnUpdatesGetDifference(func(context.Context, *tg.UpdatesGetDifferenceRequest) (tg.UpdatesDifferenceClass, error) {
		return diff, nil
	})
	outbound, err := tg.PrepareLayerOutboundCall(profile, &tg.UpdatesGetDifferenceRequest{Pts: 1, Date: 1})
	if err != nil {
		t.Fatal(err)
	}
	var requestBody bin.Buffer
	if err := outbound.Encode(&requestBody); err != nil {
		t.Fatal(err)
	}
	admitted, err := dispatcher.AdmitLayer(profile, &requestBody)
	if err != nil {
		t.Fatal(err)
	}
	serverResult, err := dispatcher.DispatchAdmitted(context.Background(), admitted)
	if err != nil {
		t.Fatal(err)
	}
	counted := &countingLayerRPCResult{inner: serverResult}
	exact := &layerRPCResultEncoder{call: counted.Prepared().Call(), result: counted}

	c := &Conn{metrics: NopMetrics{}, msgID: proto.NewMessageIDGen(time.Now)}
	if err := c.FreezeLayerProfile(profile); err != nil {
		t.Fatal(err)
	}
	// Simulate an invokeWithLayer correction admitted while this handler was
	// still running. The result must retain the request's admitted profile.
	corrected := tg.LayerProfile227
	if profile == tg.LayerProfile227 {
		corrected = tg.LayerProfile225
	}
	if err := c.FreezeLayerProfile(corrected); err != nil {
		t.Fatal(err)
	}
	s := &Server{log: zaptest.NewLogger(t)}
	encoded, err := s.encodeRPCResult(c, 12345, exact)
	if err != nil {
		t.Fatalf("encode rpc_result: %v", err)
	}
	if got := counted.prepareCalls.Load(); got != 0 {
		t.Fatalf("generated Prepare calls = %d, want 0; inbound workers must not snapshot result bytes", got)
	}
	if got := counted.encodeCalls.Load(); got != 1 {
		t.Fatalf("generated Encode calls = %d, want exactly 1 under outbound admission", got)
	}
	if encoded.layer == nil || encoded.layer.profile != profile || encoded.layer.typ != admitted.Call().WireResultType() {
		t.Fatalf("result binding = %#v, want profile %d and admitted result TypeRef", encoded.layer, profile)
	}
	if encoded.layer.kind != outboundLayerBindingRequest {
		t.Fatalf("exact RPC result binding kind = %d, want request-bound", encoded.layer.kind)
	}
	beforeFrame := append([]byte(nil), encoded.body...)
	frame, err := c.buildFrame(context.Background(), proto.MessageServerResponse, nil, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame.body, beforeFrame) {
		t.Fatal("exact RPC result was transcoded after generated preparation")
	}
	var rpcEnvelope proto.Result
	if err := rpcEnvelope.Decode(&bin.Buffer{Buf: frame.body}); err != nil {
		t.Fatalf("decode rpc_result: %v", err)
	}
	if rpcEnvelope.RequestMessageID != 12345 {
		t.Fatalf("req_msg_id = %d, want 12345", rpcEnvelope.RequestMessageID)
	}
	wantChannelID := testChannelWireID(profile)
	if !bytes.Contains(rpcEnvelope.Result, littleEndianID(wantChannelID)) {
		t.Fatalf("profile %d offline difference lacks channel constructor %#08x", profile, wantChannelID)
	}
	if otherChannelID := testOtherChannelWireID(profile); bytes.Contains(rpcEnvelope.Result, littleEndianID(otherChannelID)) {
		t.Fatalf("profile %d offline difference leaked channel constructor %#08x", profile, otherChannelID)
	}
	inner := bin.Buffer{Buf: rpcEnvelope.Result}
	decoded, err := tg.DecodeLayer(profile, tg.LayerClassUpdatesDifferenceType(), &inner)
	if err != nil {
		t.Fatalf("decode exact difference: %v", err)
	}
	if inner.Len() != 0 {
		t.Fatalf("exact difference left %d bytes", inner.Len())
	}
	got, ok := decoded.(*tg.UpdatesDifference)
	message, messageOK := func() (*tg.Message, bool) {
		if !ok || len(got.NewMessages) != 1 {
			return nil, false
		}
		value, valueOK := got.NewMessages[0].(*tg.Message)
		return value, valueOK
	}()
	if !messageOK || message.ID != 2 {
		t.Fatalf("decoded exact difference = %#v", decoded)
	}
	if len(got.Chats) != 1 {
		t.Fatalf("decoded exact difference chats = %#v", got.Chats)
	}
	channel, channelOK := got.Chats[0].(*tg.Channel)
	if !channelOK || channel.ID != 100 {
		t.Fatalf("decoded exact difference channel = %#v", got.Chats)
	}
}

func TestExactLayerRPCResultUsesHistoricalMethodResultType(t *testing.T) {
	const profile = tg.LayerProfile225
	dispatcher := tg.NewServerDispatcher(nil)
	dispatcher.OnChannelsJoinChannel(func(context.Context, tg.InputChannelClass) (tg.MessagesChatInviteJoinResultClass, error) {
		return &tg.MessagesChatInviteJoinResultOk{Updates: &tg.UpdatesTooLong{}}, nil
	})
	outbound, err := tg.PrepareLayerOutboundCall(profile, &tg.ChannelsJoinChannelRequest{Channel: &tg.InputChannelEmpty{}})
	if err != nil {
		t.Fatal(err)
	}
	var requestBody bin.Buffer
	if err := outbound.Encode(&requestBody); err != nil {
		t.Fatal(err)
	}
	admitted, err := dispatcher.AdmitLayer(profile, &requestBody)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Call().WireID() == tg.ChannelsJoinChannelRequestTypeID {
		t.Fatal("historical request unexpectedly retained canonical method id")
	}
	serverResult, err := dispatcher.DispatchAdmitted(context.Background(), admitted)
	if err != nil {
		t.Fatal(err)
	}
	exact := &layerRPCResultEncoder{call: serverResult.Prepared().Call(), result: serverResult}
	c := &Conn{metrics: NopMetrics{}}
	if err := c.FreezeLayerProfile(profile); err != nil {
		t.Fatal(err)
	}
	encoded, err := (&Server{log: zaptest.NewLogger(t)}).encodeRPCResult(c, 67890, exact)
	if err != nil {
		t.Fatal(err)
	}
	var rpcEnvelope proto.Result
	if err := rpcEnvelope.Decode(&bin.Buffer{Buf: encoded.body}); err != nil {
		t.Fatal(err)
	}
	inner := bin.Buffer{Buf: rpcEnvelope.Result}
	updates, err := tg.DecodeLayer(profile, tg.LayerClassUpdatesType(), &inner)
	if err != nil {
		t.Fatalf("decode historical channels.joinChannel result: %v", err)
	}
	if inner.Len() != 0 {
		t.Fatalf("historical result left %d bytes", inner.Len())
	}
	if _, ok := updates.(*tg.UpdatesTooLong); !ok {
		t.Fatalf("historical result = %T, want Updates", updates)
	}
}

func TestProductionUnboundApplicationResultFailsClosedForLayer227(t *testing.T) {
	c := &Conn{metrics: NopMetrics{}}
	if err := c.FreezeLayerProfile(tg.LayerProfile227); err != nil {
		t.Fatal(err)
	}
	encoded, err := (&Server{log: zaptest.NewLogger(t)}).encodeRPCResult(c, 12345, testLayerChannel())
	if !errors.Is(err, ErrOutboundLayerBindingRequired) {
		t.Fatalf("unbound Layer 228 result error = %v, want %v", err, ErrOutboundLayerBindingRequired)
	}
	if encoded != nil {
		t.Fatalf("unbound result produced %d wire bytes", len(encoded.body))
	}
}

func TestProductionUnboundApplicationPushFailsClosedForLayer227(t *testing.T) {
	c := &Conn{metrics: NopMetrics{}}
	if err := c.FreezeLayerProfile(tg.LayerProfile227); err != nil {
		t.Fatal(err)
	}
	frame, err := c.buildFrame(context.Background(), proto.MessageFromServer, testLayerChannelUpdatesValue(321), nil)
	if !errors.Is(err, ErrOutboundLayerBindingRequired) {
		t.Fatalf("unbound Layer 228 push error = %v, want %v", err, ErrOutboundLayerBindingRequired)
	}
	if frame != nil {
		t.Fatalf("unbound push produced frame %#v", frame)
	}
}

func littleEndianID(id uint32) []byte {
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, id)
	return buf
}
