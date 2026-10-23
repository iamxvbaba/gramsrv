package tlprofile

import (
	"bytes"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

const (
	starGiftWithdrawalLegacyID = 0xd06e93a8 // password:InputCheckPasswordSRP (225–231)
	starGiftWithdrawalLayer232 = 0xc8ca817b // flags:# tmp_password:bytes (232)
)

func TestLayer232IsCanonical(t *testing.T) {
	if tg.Layer != 232 || ProfileCanonical != Profile232 {
		t.Fatalf("canonical Layer = tg:%d profile:%d, want 232", tg.Layer, ProfileCanonical)
	}
}

func legacyStarGiftWithdrawalWire(t *testing.T, password tg.InputCheckPasswordSRPClass) *bin.Buffer {
	t.Helper()
	var wire bin.Buffer
	wire.PutID(starGiftWithdrawalLegacyID)
	if err := (&tg.InputSavedStarGiftUser{MsgID: 42}).Encode(&wire); err != nil {
		t.Fatal(err)
	}
	if err := password.Encode(&wire); err != nil {
		t.Fatal(err)
	}
	return &wire
}

func TestLayer232StarGiftWithdrawalCarriesLegacySRPPassword(t *testing.T) {
	srp := &tg.InputCheckPasswordSRP{SRPID: 77, A: []byte{1, 2, 3}, M1: []byte{4, 5, 6}}
	for profile := Profile225; profile < Profile232; profile++ {
		decoded, err := DecodeObject(profile, legacyStarGiftWithdrawalWire(t, srp), Limits{})
		if err != nil {
			t.Fatalf("Layer %d legacy withdrawal decode: %v", profile, err)
		}
		request, ok := decoded.(*tg.PaymentsGetStarGiftWithdrawalURLRequest)
		if !ok {
			t.Fatalf("Layer %d decoded %T", profile, decoded)
		}
		if gift, ok := request.Stargift.(*tg.InputSavedStarGiftUser); !ok || gift.MsgID != 42 {
			t.Fatalf("Layer %d stargift = %#v", profile, request.Stargift)
		}
		got, packed, err := DecodeLegacySRPTmpPassword(request.TmpPassword)
		if err != nil || !packed {
			t.Fatalf("Layer %d tmp_password does not carry the SRP proof: packed=%v err=%v", profile, packed, err)
		}
		proof, ok := got.(*tg.InputCheckPasswordSRP)
		if !ok || proof.SRPID != srp.SRPID || !bytes.Equal(proof.A, srp.A) || !bytes.Equal(proof.M1, srp.M1) {
			t.Fatalf("Layer %d SRP proof = %#v, want %#v", profile, got, srp)
		}
	}

	// An empty-password proof is carried too; the server decides what it means.
	decoded, err := DecodeObject(Profile231, legacyStarGiftWithdrawalWire(t, &tg.InputCheckPasswordEmpty{}), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, packed, err := DecodeLegacySRPTmpPassword(decoded.(*tg.PaymentsGetStarGiftWithdrawalURLRequest).TmpPassword)
	if err != nil || !packed {
		t.Fatalf("empty password not carried: packed=%v err=%v", packed, err)
	}
	if _, ok := got.(*tg.InputCheckPasswordEmpty); !ok {
		t.Fatalf("empty password = %T", got)
	}
}

func TestLayer232StarGiftWithdrawalCanonicalWire(t *testing.T) {
	request := &tg.PaymentsGetStarGiftWithdrawalURLRequest{
		Stargift:    &tg.InputSavedStarGiftUser{MsgID: 42},
		TmpPassword: []byte("server-issued-token"),
	}
	var wire bin.Buffer
	if err := EncodeObject(Profile232, request, &wire); err != nil {
		t.Fatal(err)
	}
	if id, _ := wire.PeekID(); id != starGiftWithdrawalLayer232 {
		t.Fatalf("Layer 232 constructor = %#08x, want %#08x", id, starGiftWithdrawalLayer232)
	}
	decoded, err := DecodeObject(Profile232, &wire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	tmp := decoded.(*tg.PaymentsGetStarGiftWithdrawalURLRequest).TmpPassword
	if !bytes.Equal(tmp, request.TmpPassword) {
		t.Fatalf("tmp_password = %q", tmp)
	}
	if _, packed, err := DecodeLegacySRPTmpPassword(tmp); packed || err != nil {
		t.Fatalf("ordinary token treated as packed SRP proof: packed=%v err=%v", packed, err)
	}

	// The server never sends this request to a client; encoding it for an
	// older profile has no SRP proof to emit and must fail.
	for profile := Profile225; profile < Profile232; profile++ {
		var old bin.Buffer
		if err := EncodeObject(profile, request, &old); err == nil {
			t.Fatalf("Layer %d encoded a tmp_password withdrawal without an SRP proof", profile)
		}
	}
}

func TestLayer232DecodeLegacySRPTmpPasswordRejectsMalformed(t *testing.T) {
	packed, err := EncodeLegacySRPTmpPassword(&tg.InputCheckPasswordSRP{SRPID: 1, A: []byte{1}, M1: []byte{2}})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{
		"truncated": packed[:len(packed)-1],
		"trailing":  append(append([]byte{}, packed...), 0),
	} {
		if _, ok, err := DecodeLegacySRPTmpPassword(raw); !ok || err == nil {
			t.Fatalf("%s packed proof accepted: ok=%v err=%v", name, ok, err)
		}
	}
	if _, err := EncodeLegacySRPTmpPassword(nil); err == nil {
		t.Fatal("nil password encoded")
	}
}

func TestLayer232ChannelNewFieldsDropForLayer231(t *testing.T) {
	channel := &tg.Channel{ID: 5, Title: "news", Photo: &tg.ChatPhotoEmpty{}}
	channel.SetInNews(true)
	channel.SetPinnedMsgID(9)

	var canonical bin.Buffer
	if err := EncodeObject(Profile232, channel, &canonical); err != nil {
		t.Fatal(err)
	}
	if id, _ := canonical.PeekID(); id != 0x7ad15240 {
		t.Fatalf("Layer 232 channel = %#08x", id)
	}

	var old bin.Buffer
	if err := EncodeObject(Profile231, channel, &old); err != nil {
		t.Fatalf("Layer 231 channel encode: %v", err)
	}
	if id, _ := old.PeekID(); id != 0xd49f34c6 {
		t.Fatalf("Layer 231 channel = %#08x, want 0xd49f34c6", id)
	}
	decoded, err := DecodeObject(Profile231, &old, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.(*tg.Channel)
	if !ok || got.ID != 5 || got.Title != "news" || got.InNews || got.PinnedMsgID != 0 {
		t.Fatalf("Layer 231 channel round trip = %#v", decoded)
	}
}
