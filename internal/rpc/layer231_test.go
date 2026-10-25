package rpc

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"
)

// Layer 231 stays an exact historical profile after canonical moved to 232:
// it resolves to itself and is never clamped to another profile.
func TestLayer231StaysSupportedExactProfile(t *testing.T) {
	profile, ok := tlprofile.ResolveProfile(231)
	if !ok || profile != tlprofile.Profile231 {
		t.Fatalf("ResolveProfile(231) = %d, %v, want Profile231", profile, ok)
	}
	if !isSupportedLayer(231) {
		t.Fatal("isSupportedLayer(231) = false, want true")
	}
}

// Layer 231 changed the urlAuthResultRequest and messages.acceptUrlAuth
// constructor IDs. Every exact profile keeps its own wire: a Layer 230 client
// must still see the historical constructor, a Layer 231 client the new one.
func TestLayer231URLAuthConstructorsAreProfileExact(t *testing.T) {
	for _, tc := range []struct {
		profile tlprofile.Profile
		wantID  uint32
	}{
		{profile: tlprofile.Profile230, wantID: 0x3cd623ec},
		{profile: tlprofile.Profile231, wantID: 0xbdca6167},
	} {
		t.Run(fmt.Sprintf("layer_%d", tc.profile), func(t *testing.T) {
			var body bin.Buffer
			request := &tg.URLAuthResultRequest{Bot: &tg.UserEmpty{}}
			if err := tlprofile.EncodeObject(tc.profile, request, &body); err != nil {
				t.Fatalf("encode Layer %d urlAuthResultRequest: %v", tc.profile, err)
			}
			if got := binary.LittleEndian.Uint32(body.Raw()); got != tc.wantID {
				t.Fatalf("Layer %d urlAuthResultRequest wire id = %#x, want %#x", tc.profile, got, tc.wantID)
			}
		})
	}
}

// The Layer 231 acceptUrlAuth request carries an optional TON Connect OAuth
// session. The server implements the Telegram Login Widget flow only, so a
// wallet session must be refused instead of silently accepted as a login.
func TestLayer231AcceptURLAuthRejectsTONConnectSession(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	ctx := WithUserID(context.Background(), 1000000001)
	request := &tg.MessagesAcceptURLAuthRequest{}
	request.SetTonconnectSession(tg.InputTonConnectOauthSession{
		SessionID:       42,
		ChallengeAnswer: []byte("answer"),
		Body:            []byte("{}"),
		Proof:           tg.WalletOwnershipProof{Timestamp: 1, Signature: []byte{0x01, 0x02}},
	})
	if _, err := r.onMessagesAcceptURLAuth(ctx, request); err == nil {
		t.Fatal("acceptUrlAuth with a TON Connect session succeeded, want a refusal")
	}
}
