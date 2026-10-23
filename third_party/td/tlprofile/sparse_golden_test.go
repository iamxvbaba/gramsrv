package tlprofile

import (
	"bytes"
	"encoding/hex"
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func mustGolden(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestSparseCodecExactWireGoldenAcrossProfiles(t *testing.T) {
	const (
		direct     = "def8bde52a000000000000004939b9ed00b33f71"
		rights     = "d524b25f00000000"
		channel225 = "1cb1321c000000000000000064000000000000000b6c617965722070726f6f661c01c13701000000"
		channel228 = "c6349fd4000000000000000064000000000000000b6c617965722070726f6f661c01c13701000000"
		updates225 = "4042ae7415c4b51c0000000015c4b51c0000000015c4b51c000000000100000001000000"
		updates228 = "4042ae7415c4b51c01000000a1bbbc201adcc6d9000000000100000022175159010000000000000022175159020000000000000002000000000000000100000009657068656d6572616c000015c4b51c0000000015c4b51c000000000100000001000000"
	)
	cases := []struct {
		name  string
		value bin.Object
		wire  map[Profile]string
	}{
		{"direct", &tg.UpdateUserStatus{UserID: 42, Status: &tg.UserStatusOnline{Expires: 1_900_000_000}}, map[Profile]string{Profile225: direct, Profile226: direct, Profile227: direct, Profile228: direct}},
		{"same-crc-flags", &tg.ChatAdminRights{}, map[Profile]string{Profile225: rights, Profile226: rights, Profile227: rights, Profile228: rights}},
		{"changed-id-and-shape", &tg.Channel{ID: 100, Title: "layer proof", Photo: &tg.ChatPhotoEmpty{}, Date: 1}, map[Profile]string{Profile225: channel225, Profile226: channel225, Profile227: channel225, Profile228: channel228}},
		{"nested-vector-projection", &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewEphemeralMessage{Message: tg.EphemeralMessage{ID: 1, FromID: &tg.PeerUser{UserID: 1}, PeerID: &tg.PeerUser{UserID: 2}, ReceiverID: 2, Date: 1, Message: "ephemeral"}}}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: 1, Seq: 1}, map[Profile]string{Profile225: updates225, Profile226: updates225, Profile227: updates225, Profile228: updates228}},
	}
	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		for _, test := range cases {
			t.Run(test.name+"/layer"+strconv.Itoa(int(profile)), func(t *testing.T) {
				var got bin.Buffer
				if err := EncodeObject(profile, test.value, &got); err != nil {
					t.Fatal(err)
				}
				want := mustGolden(t, test.wire[profile])
				if !bytes.Equal(got.Raw(), want) {
					t.Fatalf("wire differs:\ngot =%x\nwant=%x", got.Raw(), want)
				}
				decoded, err := DecodeObject(profile, &bin.Buffer{Buf: append([]byte(nil), want...)}, Limits{})
				if err != nil {
					t.Fatal(err)
				}
				var roundTrip bin.Buffer
				if err := EncodeObject(profile, decoded, &roundTrip); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(roundTrip.Raw(), want) {
					t.Fatalf("round-trip differs: got=%x want=%x", roundTrip.Raw(), want)
				}
			})
		}
	}
}

func TestSparseCodecHistoricalPolicyAdapter(t *testing.T) {
	wire226 := &bin.Buffer{}
	wire226.PutID(0x774bbdf4)
	wire226.PutLong(10)
	wire226.PutString("https://example.test")
	wire226.PutLong(42)
	wire226.PutVectorHeader(0)
	decoded, err := DecodeObject(Profile226, wire226, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := decoded.(*tg.MessagesChatInviteJoinResultWebView)
	if !ok || value.QueryID != 42 {
		t.Fatalf("historical policy projection = %#v", decoded)
	}
	if err := EncodeObject(Profile226, value, &bin.Buffer{}); err == nil {
		t.Fatal("sparse codec fabricated removed historical URL")
	}
}

func TestSparseResultExactWireGoldenAcrossProfiles(t *testing.T) {
	const (
		config      = "1e241acc000000000100000002000000379779bc0100000015c4b51c000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
		emptyVector = "15c4b51c00000000"
		join225     = "4042ae7415c4b51c0000000015c4b51c0000000015c4b51c000000000000000000000000"
		join226     = "a76356444042ae7415c4b51c0000000015c4b51c0000000015c4b51c000000000000000000000000"
	)
	cases := []struct {
		name    string
		request bin.Object
		result  any
		wire    map[Profile]string
	}{
		{"boxed-concrete", &tg.HelpGetConfigRequest{}, &tg.Config{Date: 1, Expires: 2, ThisDC: 1}, map[Profile]string{Profile225: config, Profile226: config, Profile227: config, Profile228: config}},
		{"vector-class", &tg.UsersGetUsersRequest{ID: []tg.InputUserClass{}}, []tg.UserClass{}, map[Profile]string{Profile225: emptyVector, Profile226: emptyVector, Profile227: emptyVector, Profile228: emptyVector}},
		{"result-adapter", &tg.MessagesImportChatInviteRequest{Hash: "x"}, &tg.MessagesChatInviteJoinResultOk{Updates: &tg.Updates{Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Updates: []tg.UpdateClass{}}}, map[Profile]string{Profile225: join225, Profile226: join226, Profile227: join226, Profile228: join226}},
	}
	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		for _, test := range cases {
			t.Run(test.name+"/layer"+strconv.Itoa(int(profile)), func(t *testing.T) {
				var canonical bin.Buffer
				if err := test.request.Encode(&canonical); err != nil {
					t.Fatal(err)
				}
				outbound, err := prepareSparseOutbound(profile, &canonical, Limits{})
				if err != nil {
					t.Fatal(err)
				}
				var requestWire bin.Buffer
				if err := outbound.Encode(&requestWire); err != nil {
					t.Fatal(err)
				}
				admission, err := NewDispatcher().Admit(profile, &requestWire, Limits{})
				if err != nil {
					t.Fatal(err)
				}
				var got bin.Buffer
				if err := admission.Call().EncodeResult(test.result, &got); err != nil {
					t.Fatal(err)
				}
				want := mustGolden(t, test.wire[profile])
				if !bytes.Equal(got.Raw(), want) {
					t.Fatalf("result wire differs:\ngot =%x\nwant=%x", got.Raw(), want)
				}
			})
		}
	}
}
