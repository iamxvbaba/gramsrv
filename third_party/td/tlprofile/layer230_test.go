package tlprofile

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

var pre230Profiles = []Profile{Profile225, Profile226, Profile227, Profile228, Profile229}

func TestLayer230VerificationWireOracle(t *testing.T) {
	for _, profile := range pre230Profiles {
		t.Run(fmt.Sprint(profile), func(t *testing.T) {
			// Literal schema oracle: botVerification#f93cd45c bot_id:long icon:long description:string.
			var legacy bin.Buffer
			legacy.PutID(0xf93cd45c)
			legacy.PutLong(42)
			legacy.PutLong(99)
			legacy.PutString("verified")
			expected := legacy.Copy()
			value, err := DecodeObject(profile, &legacy, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			verification := value.(*tg.BotVerification)
			if verification.Description.Text != "verified" || len(verification.Description.Entities) != 0 || legacy.Len() != 0 {
				t.Fatalf("decoded: %+v", verification)
			}
			var encoded bin.Buffer
			if err := EncodeObject(profile, verification, &encoded); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded.Raw(), expected) {
				t.Fatalf("legacy wire: %x != %x", encoded.Raw(), expected)
			}
			verification.Description.Entities = []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 8}}
			assert230EncodeRejected(t, profile, verification)
		})
	}
	rich := &tg.BotVerification{BotID: 42, Icon: 99, Description: tg.TextWithEntities{Text: "verified", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Length: 8}}}}
	var wire bin.Buffer
	if err := EncodeObject(Profile230, rich, &wire); err != nil {
		t.Fatal(err)
	}
	if id, _ := wire.PeekID(); id != 0xf72e0ed9 {
		t.Fatalf("Layer 230 constructor: %#x", id)
	}
	got, err := DecodeObject(Profile230, &wire, Limits{})
	if err != nil || !reflect.DeepEqual(got, rich) {
		t.Fatalf("rich roundtrip: %+v, %v", got, err)
	}
}

func TestLayer230VerificationOptionalPresence(t *testing.T) {
	for _, profile := range append(append([]Profile{}, pre230Profiles...), Profile230) {
		for _, present := range []bool{false, true} {
			for _, text := range []string{"", "verified"} {
				settings := &tg.BotVerifierSettings{Icon: 1, Company: "Company"}
				request := &tg.BotsSetCustomVerificationRequest{Enabled: true, Peer: &tg.InputPeerSelf{}}
				if present {
					settings.SetCustomDescription(tg.TextWithEntities{Text: text})
					request.SetCustomDescription(tg.TextWithEntities{Text: text})
				}
				for _, object := range []bin.Object{settings, request} {
					var wire bin.Buffer
					if err := EncodeObject(profile, object, &wire); err != nil {
						t.Fatalf("%d %T: %v", profile, object, err)
					}
					if profile != Profile230 {
						var oracle bin.Buffer
						switch object.(type) {
						case *tg.BotVerifierSettings:
							oracle.PutID(0xb0cd6617)
							flags := 0
							if present {
								flags = 1
							}
							oracle.PutInt(flags)
							oracle.PutLong(1)
							oracle.PutString("Company")
						case *tg.BotsSetCustomVerificationRequest:
							oracle.PutID(0x8b89dfbd)
							flags := 2
							if present {
								flags |= 4
							}
							oracle.PutInt(flags)
							oracle.PutID(0x7da07ec9)
						}
						if present {
							oracle.PutString(text)
						}
						if !bytes.Equal(wire.Raw(), oracle.Raw()) {
							t.Fatalf("%d %T presence=%v oracle mismatch: %x != %x", profile, object, present, wire.Raw(), oracle.Raw())
						}
					}
					decoded, err := DecodeObject(profile, &wire, Limits{})
					if err != nil {
						t.Fatal(err)
					}
					var description tg.TextWithEntities
					var gotPresent bool
					switch v := decoded.(type) {
					case *tg.BotVerifierSettings:
						description, gotPresent = v.GetCustomDescription()
					case *tg.BotsSetCustomVerificationRequest:
						description, gotPresent = v.GetCustomDescription()
					}
					if gotPresent != present || (present && description.Text != text) {
						t.Fatalf("%d %T presence=%v text=%q", profile, decoded, gotPresent, description.Text)
					}
				}
			}
		}
	}
	for _, profile := range pre230Profiles {
		rich := tg.TextWithEntities{Text: "x", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Length: 1}}}
		settings := &tg.BotVerifierSettings{Icon: 1, Company: "Company"}
		settings.SetCustomDescription(rich)
		request := &tg.BotsSetCustomVerificationRequest{Peer: &tg.InputPeerSelf{}}
		request.SetCustomDescription(rich)
		assert230EncodeRejected(t, profile, settings)
		assert230EncodeRejected(t, profile, request)
	}
}

func TestLayer230FlagsRejectLossyProjection(t *testing.T) {
	for _, profile := range pre230Profiles {
		for _, raw := range []bool{false, true} {
			for _, bit := range []int{2, 3} {
				name := &tg.Username{Username: "alice"}
				if raw {
					name.Flags.Set(bit)
				} else if bit == 2 {
					name.Deletable = true
				} else {
					name.Expired = true
				}
				assert230EncodeRejected(t, profile, name)
				assert230EncodeRejected(t, profile, &tg.UpdateUserName{UserID: 1, Usernames: []tg.Username{*name}})
			}
			req := &tg.BotsCheckUsernameRequest{Username: "alice"}
			if raw {
				req.Flags.Set(0)
			} else {
				req.Additional = true
			}
			assert230EncodeRejected(t, profile, req)
		}
	}
	for _, ids := range [][]int64{nil, {}, {42}} {
		req := &tg.AuthInitFirebasePnvLoginRequest{APIID: 1, APIHash: "hash"}
		req.SetExceptIDs(ids)
		assert230EncodeRejected(t, Profile229, req)
		var wire bin.Buffer
		if err := EncodeObject(Profile230, req, &wire); err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeObject(Profile230, &wire, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		values, present := decoded.(*tg.AuthInitFirebasePnvLoginRequest).GetExceptIDs()
		if !present || len(values) != len(ids) {
			t.Fatal("except_ids presence lost")
		}
	}
	req := &tg.AuthInitFirebasePnvLoginRequest{APIID: 1, APIHash: "hash"}
	var wire bin.Buffer
	if err := EncodeObject(Profile229, req, &wire); err != nil {
		t.Fatal(err)
	}
	if id, _ := wire.PeekID(); id != 0x777df37a {
		t.Fatalf("legacy firebase method %#x", id)
	}
	if _, err := DecodeObject(Profile229, &wire, Limits{}); err != nil {
		t.Fatal(err)
	}
}

func TestLayer230RemovedCredentialFailsClosed(t *testing.T) {
	for _, profile := range append(append([]Profile{}, pre230Profiles...), Profile230) {
		for _, nested := range []bool{false, true} {
			var wire bin.Buffer
			if nested {
				wire.PutID(0x9857ad07)
				wire.PutInt(0)
			}
			wire.PutID(0x5b1ccb28)
			wire.PutString("pnv-token")
			before := wire.Copy()
			if _, err := DecodeObject(profile, &wire, Limits{}); err == nil {
				t.Fatalf("%d accepted removed credential", profile)
			}
			if !bytes.Equal(before, wire.Raw()) {
				t.Fatal("failed decode consumed cursor")
			}
			if nested {
				if _, err := NewDispatcher().Admit(profile, &wire, Limits{}); err == nil {
					t.Fatalf("%d admitted removed credential", profile)
				}
				if !bytes.Equal(before, wire.Raw()) {
					t.Fatal("failed admission consumed cursor")
				}
			}
		}
	}
	valid := &tg.AuthFinishPasskeyLoginRequest{Credential: tg.InputPasskeyCredentialPublicKey{ID: "id", RawID: "raw", Response: &tg.InputPasskeyResponseLogin{ClientData: tg.DataJSON{Data: "{}"}}}}
	for _, profile := range append(append([]Profile{}, pre230Profiles...), Profile230) {
		var wire bin.Buffer
		if err := EncodeObject(profile, valid, &wire); err != nil {
			t.Fatal(err)
		}
		if _, err := NewDispatcher().Admit(profile, &wire, Limits{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLayer230WalletExactResults(t *testing.T) {
	tests := []struct {
		request bin.Object
		result  any
	}{
		{&tg.WalletGetStateRequest{}, &tg.WalletState{Address: "address", PublicKey: []byte{1, 2}, Balance: 123}},
		{&tg.WalletGetTransactionsRequest{Limit: 10}, &tg.WalletTransactions{Balance: 123, Transactions: []tg.WalletTransaction{{ID: "tx", Amount: 5, Peer: &tg.WalletTransactionPeerAddress{Address: "address"}}}}},
		{&tg.WalletGetBackupHolderDCsRequest{}, []tg.WalletHolderDC{{DC: 2, PublicKey: []byte{1, 2}}}},
	}
	for _, test := range tests {
		var request bin.Buffer
		if err := EncodeObject(Profile230, test.request, &request); err != nil {
			t.Fatal(err)
		}
		original := request.Copy()
		admission, err := NewDispatcher().Admit(Profile230, &request, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		var result bin.Buffer
		if err := admission.Call().EncodeResult(test.result, &result); err != nil {
			t.Fatal(err)
		}
		var oracle bin.Buffer
		switch value := test.result.(type) {
		case bin.Encoder:
			if err := value.Encode(&oracle); err != nil {
				t.Fatal(err)
			}
		case []tg.WalletHolderDC:
			oracle.PutVectorHeader(len(value))
			for i := range value {
				if err := value[i].Encode(&oracle); err != nil {
					t.Fatal(err)
				}
			}
		}
		if !bytes.Equal(result.Raw(), oracle.Raw()) {
			t.Fatalf("%T result mismatch", test.request)
		}
		for _, profile := range pre230Profiles {
			assert230EncodeRejected(t, profile, test.request)
			wire := &bin.Buffer{Buf: append([]byte(nil), original...)}
			if _, err := NewDispatcher().Admit(profile, wire, Limits{}); err == nil {
				t.Fatalf("%d admitted wallet request", profile)
			}
			if !bytes.Equal(wire.Raw(), original) {
				t.Fatal("rejected wallet admission consumed cursor")
			}
		}
		for n := 0; n < len(original); n++ {
			wire := &bin.Buffer{Buf: append([]byte(nil), original[:n]...)}
			before := wire.Copy()
			if _, err := NewDispatcher().Admit(Profile230, wire, Limits{}); err == nil {
				t.Fatalf("%T accepted truncation %d", test.request, n)
			}
			if !bytes.Equal(before, wire.Raw()) {
				t.Fatal("truncated admission consumed cursor")
			}
		}
	}
	for _, update := range []tg.UpdateClass{
		&tg.UpdateWalletState{State: &tg.WalletStateEmpty{}},
		&tg.UpdateSentWalletTransaction{}, &tg.UpdateWalletGaslessInfo{},
		&tg.UpdateWalletTonConnectSession{Session: tg.TonConnectSession{}},
		&tg.UpdateWalletTonConnectPendingDisconnect{SessionIDs: []int64{1}},
	} {
		var current bin.Buffer
		if err := EncodeObject(Profile230, update, &current); err != nil {
			t.Fatal(err)
		}
		if decoded, err := DecodeObject(Profile230, &current, Limits{}); err != nil || reflect.TypeOf(decoded) != reflect.TypeOf(update) {
			t.Fatalf("%T canonical update: %T, %v", update, decoded, err)
		}
		for _, profile := range pre230Profiles {
			assert230EncodeRejected(t, profile, update)
			assert230EncodeRejected(t, profile, &tg.Updates{Updates: []tg.UpdateClass{update}})
		}
	}
}

func assert230EncodeRejected(t *testing.T, profile Profile, object bin.Object) {
	t.Helper()
	out := &bin.Buffer{Buf: []byte{9, 8, 7}}
	before := out.Copy()
	if err := EncodeObject(profile, object, out); err == nil {
		t.Fatalf("%d accepted lossy/unavailable %T", profile, object)
	}
	if !bytes.Equal(before, out.Raw()) {
		t.Fatalf("%d %T mutated failed output", profile, object)
	}
}
