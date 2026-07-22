package android

import (
	"errors"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tlprofile"
)

func TestUpgradePrivateLayerRPCOnlyAcceptsAuditedAndroidConstructors(t *testing.T) {
	// DrKLO messages.forwardMessages private CRC has a body identical to the
	// canonical request for the flags=0 empty-vector case.
	private := bin.Buffer{}
	private.PutID(0x41d41ade)
	private.PutInt(0)
	private.PutID(0x7f3b18ea) // inputPeerEmpty
	private.PutVectorHeader(0)
	private.PutVectorHeader(0)
	private.PutID(0x7f3b18ea) // inputPeerEmpty

	in := &bin.Buffer{Buf: private.Copy()}
	upgraded, ok, err := UpgradePrivateLayerRPC(tlprofile.ProfileCanonical, in, tlprofile.Limits{})
	if err != nil || !ok {
		t.Fatalf("upgrade private method = ok:%v err:%v", ok, err)
	}
	if in.Len() != 0 {
		t.Fatalf("successful private method left %d bytes", in.Len())
	}
	if id, peekErr := upgraded.PeekID(); peekErr != nil || id != 0x13704a7c {
		t.Fatalf("canonical id = %#x err=%v", id, peekErr)
	}

	official := bin.Buffer{}
	official.PutID(0xb921bd04) // arbitrary non-private/official constructor
	if value, handled, err := UpgradePrivateLayerRPC(tlprofile.ProfileCanonical, &official, tlprofile.Limits{}); value != nil || handled || err != nil {
		t.Fatalf("non-private method = value:%v handled:%v err:%v", value, handled, err)
	}
}

func TestGeneratedPrivateLayerRPCOverlayHasAllAuditedMethods(t *testing.T) {
	if got, want := tlprofile.ClientRPCOverlayMethodCount(tlprofile.ClientRPCOverlayDrkloAndroid), 17; got != want {
		t.Fatalf("generated DrKLO method count = %d, want %d", got, want)
	}
}

func TestUpgradePrivateLayerRPCRejectsMalformedBody(t *testing.T) {
	malformed := bin.Buffer{}
	malformed.PutID(0x41d41ade)
	_, ok, err := UpgradePrivateLayerRPC(tlprofile.ProfileCanonical, &malformed, tlprofile.Limits{})
	if !ok || !errors.Is(err, ErrPrivateLayerRPCInvalid) {
		t.Fatalf("malformed private method = ok:%v err:%v", ok, err)
	}
}
