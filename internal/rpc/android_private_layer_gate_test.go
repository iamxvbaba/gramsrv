package rpc

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"github.com/iamxvbaba/td/tlprofile"
	compatandroid "telesrv/internal/compat/android"
)

type androidPrivateLayerFixture struct {
	name      string
	privateID uint32
	semantic  tlprofile.SemanticID
	method    string
	wire      func(*testing.T) []byte
}

// TestAndroidPrivateLayerRPCsAdaptAcrossCanonicalBoundary is the production
// admission gate for every audited DrKLO-private constructor. It proves both
// halves of the bridge after a canonical schema upgrade:
//
//   - the generated static client overlay emits a complete request in the current canonical
//     profile (Layer 228), never a stale Layer 227 intermediate; and
//   - Router.AdmitLayer feeds that intermediate through the generated
//     unknown-method view's AdaptCanonical path, yielding the exact Layer 227
//     or Layer 228 wire call for the same semantic method.
func TestAndroidPrivateLayerRPCsAdaptAcrossCanonicalBoundary(t *testing.T) {
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{}, zaptest.NewLogger(t), clock.System)
	fixtures := androidPrivateLayerFixtures()
	if got, want := len(fixtures), 15; got != want {
		t.Fatalf("private fixture count = %d, want %d", got, want)
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			raw := fixture.wire(t)
			if len(raw) < 4 || binary.LittleEndian.Uint32(raw[:4]) != fixture.privateID {
				t.Fatalf("fixture private id = %#x, want %#x", binary.LittleEndian.Uint32(raw[:4]), fixture.privateID)
			}

			private := bin.Buffer{Buf: append([]byte(nil), raw...)}
			canonical, handled, err := compatandroid.UpgradePrivateLayerRPC(tlprofile.ProfileCanonical, &private, tlprofile.Limits{})
			if err != nil || !handled || canonical == nil {
				t.Fatalf("canonical upgrade = value:%v handled:%v err:%v", canonical != nil, handled, err)
			}
			if private.Len() != 0 {
				t.Fatalf("canonical upgrade left %d private bytes", private.Len())
			}
			canonicalID, ok := tlprofile.WireID(tlprofile.ProfileCanonical, fixture.semantic)
			if !ok {
				t.Fatalf("canonical profile has no wire id for %s", fixture.method)
			}
			if got, peekErr := canonical.PeekID(); peekErr != nil || got != canonicalID {
				t.Fatalf("canonical upgrade id = %#x err=%v, want %#x", got, peekErr, canonicalID)
			}

			for _, profile := range []tlprofile.Profile{tlprofile.Profile227, tlprofile.Profile228} {
				profile := profile
				t.Run(fmt.Sprintf("layer_%d", profile), func(t *testing.T) {
					body := bin.Buffer{Buf: append([]byte(nil), raw...)}
					admitted, err := r.AdmitLayer(profile, &body, tlprofile.Limits{})
					if err != nil {
						t.Fatalf("production exact admission: %v", err)
					}
					if body.Len() != 0 {
						t.Fatalf("production exact admission left %d bytes", body.Len())
					}

					call := admitted.Call()
					if call.Profile() != profile || call.Method() != fixture.semantic {
						t.Fatalf("admitted call = profile:%d semantic:%#x, want profile:%d semantic:%#x", call.Profile(), call.Method(), profile, fixture.semantic)
					}
					category, method, ok := tlprofile.SemanticName(call.Method())
					if !ok || category != "function" || method != fixture.method {
						t.Fatalf("admitted semantic name = (%q,%q,%v), want (function,%q,true)", category, method, ok, fixture.method)
					}
					wantWireID, ok := tlprofile.WireID(profile, fixture.semantic)
					if !ok || call.WireID() != wantWireID {
						t.Fatalf("admitted exact id = %#x, want %#x (ok=%v)", call.WireID(), wantWireID, ok)
					}
				})
			}
		})
	}
}

func androidPrivateLayerFixtures() []androidPrivateLayerFixture {
	return []androidPrivateLayerFixture{
		{
			name: "messages.forwardMessages_alias", privateID: 0x41d41ade,
			semantic: tlprofile.SemanticMethodMessagesForwardMessages, method: "messages.forwardMessages",
			wire: func(*testing.T) []byte { return androidPrivateForwardMessagesWire() },
		},
		{
			name: "channels.inviteToChannel_alias", privateID: 0x199f3a6c,
			semantic: tlprofile.SemanticMethodChannelsInviteToChannel, method: "channels.inviteToChannel",
			wire: func(t *testing.T) []byte {
				return androidPrivateAliasWire(t, 0x199f3a6c, &tg.ChannelsInviteToChannelRequest{
					Channel: &tg.InputChannel{ChannelID: 41, AccessHash: 42},
					Users:   []tg.InputUserClass{&tg.InputUser{UserID: 43, AccessHash: 44}},
				})
			},
		},
		{
			name: "updates.getDifference_alias", privateID: 0x25939651,
			semantic: tlprofile.SemanticMethodUpdatesGetDifference, method: "updates.getDifference",
			wire: func(t *testing.T) []byte {
				return androidPrivateAliasWire(t, 0x25939651, &tg.UpdatesGetDifferenceRequest{Pts: 100, Date: 200, Qts: 3})
			},
		},
		{
			name: "messages.createChat_alias", privateID: 0x0034a818,
			semantic: tlprofile.SemanticMethodMessagesCreateChat, method: "messages.createChat",
			wire: func(t *testing.T) []byte {
				return androidPrivateAliasWire(t, 0x0034a818, &tg.MessagesCreateChatRequest{
					Users: []tg.InputUserClass{&tg.InputUser{UserID: 51, AccessHash: 52}},
					Title: "private group",
				})
			},
		},
		{
			name: "messages.uploadMedia_transform", privateID: 0x519bc2b1,
			semantic: tlprofile.SemanticMethodMessagesUploadMedia, method: "messages.uploadMedia",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x519bc2b1, func(body *bin.Buffer) error {
					if err := (&tg.InputPeerSelf{}).Encode(body); err != nil {
						return err
					}
					return (&tg.InputMediaUploadedPhoto{File: &tg.InputFile{ID: 61, Parts: 1, Name: "private.jpg"}}).Encode(body)
				})
			},
		},
		{
			name: "auth.signUp_transform", privateID: 0x80eee427,
			semantic: tlprofile.SemanticMethodAuthSignUp, method: "auth.signUp",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x80eee427, func(body *bin.Buffer) error {
					body.PutString("+15550000228")
					body.PutString("private-hash")
					body.PutString("Private")
					body.PutString("Layer")
					return nil
				})
			},
		},
		{
			name: "messages.getMessages_transform", privateID: 0x4222fa74,
			semantic: tlprofile.SemanticMethodMessagesGetMessages, method: "messages.getMessages",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x4222fa74, func(body *bin.Buffer) error {
					body.PutVectorHeader(2)
					body.PutInt(71)
					body.PutInt(72)
					return nil
				})
			},
		},
		{
			name: "channels.getMessages_transform", privateID: 0x93d7b347,
			semantic: tlprofile.SemanticMethodChannelsGetMessages, method: "channels.getMessages",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x93d7b347, func(body *bin.Buffer) error {
					if err := (&tg.InputChannel{ChannelID: 81, AccessHash: 82}).Encode(body); err != nil {
						return err
					}
					body.PutVectorHeader(2)
					body.PutInt(83)
					body.PutInt(84)
					return nil
				})
			},
		},
		{
			name: "bots.exportBotToken_transform", privateID: 0x0063b089,
			semantic: tlprofile.SemanticMethodBotsExportBotToken, method: "bots.exportBotToken",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x0063b089, func(body *bin.Buffer) error {
					body.PutLong(91)
					body.PutID(tg.BoolTrueTypeID)
					return nil
				})
			},
		},
		{
			name: "account.registerDevice_transform", privateID: 0x637ea878,
			semantic: tlprofile.SemanticMethodAccountRegisterDevice, method: "account.registerDevice",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x637ea878, func(body *bin.Buffer) error {
					body.PutInt(2)
					body.PutString("private-device-token")
					return nil
				})
			},
		},
		{
			name: "contacts.search_transform", privateID: 0x11f812d8,
			semantic: tlprofile.SemanticMethodContactsSearch, method: "contacts.search",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x11f812d8, func(body *bin.Buffer) error {
					body.PutString("private-query")
					body.PutInt(20)
					return nil
				})
			},
		},
		{
			name: "langpack.getLangPack_transform", privateID: 0x9ab5c58e,
			semantic: tlprofile.SemanticMethodLangpackGetLangPack, method: "langpack.getLangPack",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x9ab5c58e, func(body *bin.Buffer) error {
					body.PutString("en")
					return nil
				})
			},
		},
		{
			name: "langpack.getStrings_transform", privateID: 0x2e1ee318,
			semantic: tlprofile.SemanticMethodLangpackGetStrings, method: "langpack.getStrings",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x2e1ee318, func(body *bin.Buffer) error {
					body.PutString("en")
					body.PutVectorHeader(2)
					body.PutString("private.one")
					body.PutString("private.two")
					return nil
				})
			},
		},
		{
			name: "langpack.getLanguages_transform", privateID: 0x800fd57d,
			semantic: tlprofile.SemanticMethodLangpackGetLanguages, method: "langpack.getLanguages",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x800fd57d, func(*bin.Buffer) error { return nil })
			},
		},
		{
			name: "messages.editChatCreator_transform", privateID: 0x8f38cd1f,
			semantic: tlprofile.SemanticMethodMessagesEditChatCreator, method: "messages.editChatCreator",
			wire: func(t *testing.T) []byte {
				return androidPrivateRawWire(t, 0x8f38cd1f, func(body *bin.Buffer) error {
					if err := (&tg.InputChannel{ChannelID: 101, AccessHash: 102}).Encode(body); err != nil {
						return err
					}
					if err := (&tg.InputUser{UserID: 103, AccessHash: 104}).Encode(body); err != nil {
						return err
					}
					return (&tg.InputCheckPasswordEmpty{}).Encode(body)
				})
			},
		},
	}
}

func androidPrivateAliasWire(t *testing.T, privateID uint32, request bin.Encoder) []byte {
	t.Helper()
	var body bin.Buffer
	if err := request.Encode(&body); err != nil {
		t.Fatalf("encode private alias body: %v", err)
	}
	if body.Len() < 4 {
		t.Fatal("encoded private alias body has no constructor id")
	}
	binary.LittleEndian.PutUint32(body.Buf[:4], privateID)
	return body.Copy()
}

func androidPrivateRawWire(t *testing.T, privateID uint32, encodeBody func(*bin.Buffer) error) []byte {
	t.Helper()
	var body bin.Buffer
	body.PutID(privateID)
	if err := encodeBody(&body); err != nil {
		t.Fatalf("encode private raw body: %v", err)
	}
	return body.Copy()
}
