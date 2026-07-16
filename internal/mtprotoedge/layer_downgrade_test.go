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
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"
)

type countingLayerRPCResult struct {
	inner       tlprofile.Result
	encodeCalls atomic.Int32
}

const (
	testChannelWireID227 uint32 = 0x1c32b11c
	testChannelWireID228 uint32 = 0xd49f34c6
)

func testChannelWireID(profile tlprofile.Profile) uint32 {
	if profile == tlprofile.Profile228 {
		return testChannelWireID228
	}
	return testChannelWireID227
}

func testOtherChannelWireID(profile tlprofile.Profile) uint32 {
	if profile == tlprofile.Profile228 {
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

func (r *countingLayerRPCResult) Prepared() tlprofile.PreparedCall { return r.inner.Prepared() }

func (r *countingLayerRPCResult) WireInvariant() bool { return r.inner.WireInvariant() }

func (r *countingLayerRPCResult) CanonicalValue() any { return r.inner.CanonicalValue() }

func TestExactLayerRPCResultEncodesDifferenceWithAdmittedCodec(t *testing.T) {
	for _, profile := range []tlprofile.Profile{tlprofile.Profile225, tlprofile.Profile227, tlprofile.Profile228} {
		t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
			testExactLayerRPCResultEncodesDifferenceWithAdmittedCodec(t, profile)
		})
	}
}

func testExactLayerRPCResultEncodesDifferenceWithAdmittedCodec(t *testing.T, profile tlprofile.Profile) {
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

	dispatcher := tlprofile.NewDispatcher()
	if err := dispatcher.Register(tlprofile.SemanticMethodUpdatesGetDifference, func(context.Context, bin.Object) (any, error) {
		return diff, nil
	}); err != nil {
		t.Fatal(err)
	}
	var requestBody bin.Buffer
	if err := tlprofile.EncodeObject(profile, &tg.UpdatesGetDifferenceRequest{Pts: 1, Date: 1}, &requestBody); err != nil {
		t.Fatal(err)
	}
	admitted, err := dispatcher.Admit(profile, &requestBody, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	serverResult, err := dispatcher.Dispatch(context.Background(), admitted)
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
	corrected := tlprofile.Profile227
	if profile == tlprofile.Profile227 {
		corrected = tlprofile.Profile225
	}
	if err := c.FreezeLayerProfile(corrected); err != nil {
		t.Fatal(err)
	}
	s := &Server{log: zaptest.NewLogger(t)}
	encoded, err := s.encodeRPCResult(c, 12345, exact)
	if err != nil {
		t.Fatalf("encode rpc_result: %v", err)
	}
	if got := counted.encodeCalls.Load(); got != 1 {
		t.Fatalf("generated Encode calls = %d, want exactly 1 under outbound admission", got)
	}
	if encoded.layer == nil || encoded.layer.profile != profile {
		t.Fatalf("result binding = %#v, want profile %d", encoded.layer, profile)
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
	decoded, err := tlprofile.DecodeObject(profile, &inner, tlprofile.Limits{})
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
	const profile = tlprofile.Profile225
	dispatcher := tlprofile.NewDispatcher()
	if err := dispatcher.Register(tlprofile.SemanticMethodChannelsJoinChannel, func(context.Context, bin.Object) (any, error) {
		return &tg.MessagesChatInviteJoinResultOk{Updates: &tg.UpdatesTooLong{}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var requestBody bin.Buffer
	if err := tlprofile.EncodeObject(profile, &tg.ChannelsJoinChannelRequest{Channel: &tg.InputChannelEmpty{}}, &requestBody); err != nil {
		t.Fatal(err)
	}
	admitted, err := dispatcher.Admit(profile, &requestBody, tlprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Call().WireID() == tg.ChannelsJoinChannelRequestTypeID {
		t.Fatal("historical request unexpectedly retained canonical method id")
	}
	serverResult, err := dispatcher.Dispatch(context.Background(), admitted)
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
	updates, err := tlprofile.DecodeObject(profile, &inner, tlprofile.Limits{})
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
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
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
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
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
