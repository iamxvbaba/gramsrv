package tlprofile

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestLayer231URLAuthConstructorOracles(t *testing.T) {
	if tg.Layer < 231 || ProfileCanonical < Profile231 {
		t.Fatalf("canonical Layer = tg:%d profile:%d, want >= 231", tg.Layer, ProfileCanonical)
	}
	for profile := Profile225; profile <= ProfileCanonical; profile++ {
		methodID, resultID := uint32(0x67a3f0de), uint32(0x3cd623ec)
		if profile >= Profile231 {
			methodID, resultID = 0x18dd4e92, 0xbdca6167
		}
		accept := &tg.MessagesAcceptURLAuthRequest{}
		accept.SetURL("sg://oauth?token=test")
		var requestWire bin.Buffer
		if err := EncodeObject(profile, accept, &requestWire); err != nil {
			t.Fatalf("Layer %d accept encode: %v", profile, err)
		}
		if id, _ := requestWire.PeekID(); id != methodID {
			t.Fatalf("Layer %d accept constructor = %#08x, want %#08x", profile, id, methodID)
		}
		var requestOracle bin.Buffer
		requestOracle.PutID(methodID)
		requestOracle.PutInt(1 << 2) // url:flags.2?string
		requestOracle.PutString(accept.URL)
		if !bytes.Equal(requestWire.Raw(), requestOracle.Raw()) {
			t.Fatalf("Layer %d accept wire differs from literal schema oracle", profile)
		}
		decoded, err := DecodeObject(profile, &requestWire, Limits{})
		if err != nil {
			t.Fatalf("Layer %d accept decode: %v", profile, err)
		}
		if request, ok := decoded.(*tg.MessagesAcceptURLAuthRequest); !ok || request.URL != accept.URL {
			t.Fatalf("Layer %d accept round trip = %#v", profile, decoded)
		}

		prompt := &tg.URLAuthResultRequest{Bot: &tg.UserEmpty{ID: 7}, Domain: "example.test"}
		var resultWire bin.Buffer
		if err := EncodeObject(profile, prompt, &resultWire); err != nil {
			t.Fatalf("Layer %d result encode: %v", profile, err)
		}
		if id, _ := resultWire.PeekID(); id != resultID {
			t.Fatalf("Layer %d result constructor = %#08x, want %#08x", profile, id, resultID)
		}
		var resultOracle bin.Buffer
		resultOracle.PutID(resultID)
		resultOracle.PutInt(0)
		resultOracle.PutID(0xd3bc4b7a) // userEmpty
		resultOracle.PutLong(7)
		resultOracle.PutString(prompt.Domain)
		if !bytes.Equal(resultWire.Raw(), resultOracle.Raw()) {
			t.Fatalf("Layer %d result wire differs from literal schema oracle", profile)
		}
		decoded, err = DecodeObject(profile, &resultWire, Limits{})
		if err != nil {
			t.Fatalf("Layer %d result decode: %v", profile, err)
		}
		if result, ok := decoded.(*tg.URLAuthResultRequest); !ok || result.Domain != prompt.Domain || result.GetRequestWallet() {
			t.Fatalf("Layer %d result round trip = %#v", profile, decoded)
		}
	}
	for _, pair := range [][2]Profile{{Profile230, Profile231}, {Profile231, Profile230}} {
		request := &tg.MessagesAcceptURLAuthRequest{}
		request.SetURL("sg://oauth?token=test")
		var wire bin.Buffer
		if err := EncodeObject(pair[0], request, &wire); err != nil {
			t.Fatal(err)
		}
		before := wire.Copy()
		if _, err := NewDispatcher().Admit(pair[1], &wire, Limits{}); err == nil || !bytes.Equal(before, wire.Raw()) {
			t.Fatalf("Layer %d method accepted or consumed by Layer %d: %v", pair[0], pair[1], err)
		}
	}
}

func TestLayer231WalletOAuthRequestIsUnavailableInOlderProfiles(t *testing.T) {
	session := tg.InputTonConnectOauthSession{
		SessionID: 42, ChallengeAnswer: []byte{1}, Body: []byte{2},
		Proof: tg.WalletOwnershipProof{Timestamp: 123, Signature: []byte{3}},
	}
	accept := &tg.MessagesAcceptURLAuthRequest{}
	accept.SetURL("sg://oauth?token=test")
	accept.SetTonconnectSession(session)
	var wire bin.Buffer
	if err := EncodeObject(Profile231, accept, &wire); err != nil {
		t.Fatalf("Layer 231 wallet accept encode: %v", err)
	}
	if id, _ := wire.PeekID(); id != 0x18dd4e92 {
		t.Fatalf("wallet accept constructor = %#08x", id)
	}
	decoded, err := DecodeObject(Profile231, &wire, Limits{})
	if err != nil {
		t.Fatalf("Layer 231 wallet accept decode: %v", err)
	}
	if value, ok := decoded.(*tg.MessagesAcceptURLAuthRequest); !ok {
		t.Fatalf("decoded wallet accept = %T", decoded)
	} else if got, present := value.GetTonconnectSession(); !present || got.SessionID != session.SessionID {
		t.Fatalf("wallet session lost: %#v, present=%v", got, present)
	}
	prompt := &tg.URLAuthResultRequest{Bot: &tg.UserEmpty{ID: 7}, Domain: "example.test"}
	prompt.SetRequestWallet(true)
	prompt.SetTcSessionID(0)
	prompt.SetTcClientID("")
	if err := EncodeObject(Profile231, prompt, &wire); err != nil {
		t.Fatal(err)
	}
	decoded, err = DecodeObject(Profile231, &wire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	gotPrompt := decoded.(*tg.URLAuthResultRequest)
	if !gotPrompt.GetRequestWallet() || !gotPrompt.Flags.Has(9) || !gotPrompt.Flags.Has(10) {
		t.Fatal("Layer 231 wallet response flags or present-empty metadata lost")
	}

	for profile := Profile225; profile <= Profile230; profile++ {
		assert230EncodeRejected(t, profile, accept)
		assert230EncodeRejected(t, profile, &session)
		raw := &tg.MessagesAcceptURLAuthRequest{}
		raw.SetURL("sg://oauth?token=test")
		raw.Flags.Set(5)
		assert230EncodeRejected(t, profile, raw)

	}

	// A malformed/truncated new request may not consume the caller's input.
	truncated := &bin.Buffer{Buf: []byte{0x92, 0x4e, 0xdd, 0x18, 0x20}}
	before := truncated.Copy()
	if _, err := DecodeObject(Profile231, truncated, Limits{}); err == nil || !bytes.Equal(before, truncated.Raw()) {
		t.Fatalf("truncated wallet request was admitted or consumed: %v", err)
	}
}

func TestLayer231OptionalWalletPromptProjection(t *testing.T) {
	// Exercise partial metadata as well as the complete wallet hint. The old
	// schema carries none of these fields, but retains independent login consent.
	for profile := Profile225; profile <= ProfileCanonical; profile++ {
		request := &tg.MessagesRequestURLAuthRequest{}
		request.SetURL("sg://oauth?token=test")
		var requestWire bin.Buffer
		if err := EncodeObject(profile, request, &requestWire); err != nil {
			t.Fatal(err)
		}
		admitted, err := NewDispatcher().Admit(profile, &requestWire, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		for mask := 0; mask < 8; mask++ {
			for _, empty := range []bool{false, true} {
				t.Run(fmt.Sprintf("layer_%d/mask_%d/empty_%v", profile, mask, empty), func(t *testing.T) {
					plain := &tg.URLAuthResultRequest{Bot: &tg.UserEmpty{ID: 7}, Domain: "example.test"}
					plain.SetRequestWriteAccess(true)
					plain.SetRequestPhoneNumber(true)
					plain.SetMatchCodes([]string{"1234"})
					prompt := *plain
					if mask&1 != 0 {
						prompt.SetRequestWallet(true)
					}
					sessionID, clientID := int64(42), "wallet-client"
					if empty {
						sessionID, clientID = 0, ""
					}
					if mask&2 != 0 {
						prompt.SetTcSessionID(sessionID)
					}
					if mask&4 != 0 {
						prompt.SetTcClientID(clientID)
					}
					var canonicalBefore, projected bin.Buffer
					if err := EncodeObject(Profile231, &prompt, &canonicalBefore); err != nil {
						t.Fatal(err)
					}
					if err := EncodeObject(profile, &prompt, &projected); err != nil {
						t.Fatal(err)
					}
					var expected bin.Buffer
					if profile >= Profile231 {
						expected.Buf = canonicalBefore.Copy()
					} else if err := EncodeObject(profile, plain, &expected); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(projected.Raw(), expected.Raw()) {
						t.Fatal("projection changed ordinary consent or retained unsupported wallet metadata")
					}
					var rpcResult bin.Buffer
					if err := admitted.Call().EncodeResult(&prompt, &rpcResult); err != nil {
						t.Fatalf("RPC result projection: %v", err)
					}
					if !bytes.Equal(rpcResult.Raw(), expected.Raw()) {
						t.Fatal("RPC result projection differs from the exact object codec")
					}
					decoded, err := DecodeObject(profile, &projected, Limits{})
					if err != nil || projected.Len() != 0 {
						t.Fatalf("decode projected prompt: %v, remaining=%d", err, projected.Len())
					}
					got := decoded.(*tg.URLAuthResultRequest)
					if profile < Profile231 && (got.RequestWallet || got.Flags.Has(8) || got.Flags.Has(9) || got.Flags.Has(10)) {
						t.Fatal("old profile must not imply wallet authorization")
					}
					var canonicalAfter bin.Buffer
					if err := EncodeObject(Profile231, &prompt, &canonicalAfter); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(canonicalBefore.Raw(), canonicalAfter.Raw()) {
						t.Fatal("old profile projection mutated the canonical wallet prompt")
					}
				})
			}
		}
	}
}
