package tlprofile

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestStaticScannerUsesInvokeWithLayerProfile(t *testing.T) {
	request := &tg.InvokeWithLayerRequest{
		Layer: int(Profile225),
		Query: &tg.HelpGetConfigRequest{},
	}
	var wire bin.Buffer
	if err := request.Encode(&wire); err != nil {
		t.Fatal(err)
	}
	if err := tlScanExact(Profile228, &wire, Limits{}); err != nil {
		t.Fatalf("scan exact nested profile: %v", err)
	}

	malformed := wire.Copy()
	binary.LittleEndian.PutUint32(malformed[4:8], 224)
	if err := tlScanExact(Profile228, &bin.Buffer{Buf: malformed}, Limits{}); err == nil || !strings.Contains(err.Error(), "unsupported nested exact profile") {
		t.Fatalf("unsupported nested profile error = %v", err)
	}
}

func TestStaticScannerRejectsVectorBeforeMaterialization(t *testing.T) {
	value := &tg.UpdateUserName{UserID: 42, FirstName: "A", LastName: "B", Usernames: []tg.Username{}}
	var wire bin.Buffer
	if err := EncodeObject(Profile225, value, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Len() < 8 || binary.LittleEndian.Uint32(wire.Raw()[wire.Len()-8:]) != bin.TypeVector {
		t.Fatalf("test fixture has no terminal vector: %x", wire.Raw())
	}
	malformed := wire.Copy()
	binary.LittleEndian.PutUint32(malformed[len(malformed)-4:], 5)
	input := &bin.Buffer{Buf: malformed}
	before := input.Copy()
	err := tlScanExact(Profile225, input, Limits{MaxVectorElements: 4})
	if err == nil || !strings.Contains(err.Error(), "configured limit 4") {
		t.Fatalf("vector limit error = %v", err)
	}
	if string(input.Raw()) != string(before) {
		t.Fatal("non-materializing scanner consumed the caller buffer")
	}
}

func TestSharedStaticScannerRechecksReplacementWireLimit(t *testing.T) {
	state, err := tlNewScanState(Profile228, bin.Word, Limits{MaxWireBytes: 2 * bin.Word})
	if err != nil {
		t.Fatal(err)
	}
	var replacement bin.Buffer
	replacement.PutID(tg.HelpGetConfigRequestTypeID)
	replacement.PutLong(0)
	err = tlScanExactObservedWithState(Profile228, &replacement, &state, nil)
	if err == nil || !strings.Contains(err.Error(), "wire byte length 12 exceeds limit 8") {
		t.Fatalf("replacement wire limit error = %v", err)
	}
}

func TestSparseRouteRetagsWithoutHistoricalStruct(t *testing.T) {
	request := &tg.ChannelsJoinChannelRequest{Channel: &tg.InputChannelEmpty{}}
	var wire bin.Buffer
	wire.PutID(0x24b524c5) // exact Layer 225 channels.joinChannel
	if err := request.EncodeBare(&wire); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeObject(Profile225, &wire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.(*tg.ChannelsJoinChannelRequest)
	if !ok {
		t.Fatalf("decoded retag type = %T", decoded)
	}
	if _, ok := got.Channel.(*tg.InputChannelEmpty); !ok {
		t.Fatalf("decoded retag channel = %T", got.Channel)
	}
}
