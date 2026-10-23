package tlprofile

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func codecDialogsCall(t testing.TB, profile Profile) Call {
	t.Helper()
	var wire bin.Buffer
	request := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 100}
	if err := EncodeObject(profile, request, &wire); err != nil {
		t.Fatal(err)
	}
	admission, err := NewDispatcher().Admit(profile, &wire, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return admission.Call()
}

func codecTextMessage() *tg.Message {
	return &tg.Message{ID: 1, PeerID: &tg.PeerUser{UserID: 2}, Date: 3, Message: "test"}
}

func TestSparseOptionalFieldWireUnchanged(t *testing.T) {
	for profile := Profile225; profile <= Profile230; profile++ {
		call := codecDialogsCall(t, profile)
		for _, mode := range []string{"absent", "empty-vector", "populated", "shared-flag"} {
			t.Run(fmt.Sprintf("%d/%s", profile, mode), func(t *testing.T) {
				message := codecTextMessage()
				var flags int
				switch mode {
				case "empty-vector":
					flags = 1 << 7
					message.SetEntities([]tg.MessageEntityClass{})
				case "populated":
					flags = 1<<7 | 1<<8
					message.SetFromID(&tg.PeerUser{UserID: 4})
					message.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 4}})
				case "shared-flag":
					flags = 1 << 2
					// The peer keeps the shared bit set even when saved_from_msg_id is zero.
					message.SetFwdFrom(tg.MessageFwdHeader{Date: 6, SavedFromPeer: &tg.PeerUser{UserID: 9}})
				}
				value := &tg.MessagesDialogs{Messages: []tg.MessageClass{message}}
				var got bin.Buffer
				if err := call.EncodeResult(value, &got); err != nil {
					t.Fatal(err)
				}
				// Literal TL oracle, independent of the generated encoder's flag logic.
				var want bin.Buffer
				want.PutID(0x15ba6c40) // messages.dialogs
				want.PutVectorHeader(0)
				want.PutVectorHeader(1)
				if profile <= Profile226 {
					want.PutID(0x95ef6f2b)
				} else {
					want.PutID(0x7600b9d3)
				}
				want.PutInt(flags)
				want.PutInt(0) // flags2
				want.PutInt(1)
				if mode == "populated" {
					want.PutID(0x59511722)
					want.PutLong(4)
				}
				want.PutID(0x59511722)
				want.PutLong(2)
				if mode == "shared-flag" {
					want.PutID(0x4e4df4bb)
					want.PutInt(1 << 4)
					want.PutInt(6)
					want.PutID(0x59511722)
					want.PutLong(9)
					want.PutInt(0)
				}
				want.PutInt(3)
				want.PutString("test")
				if mode == "empty-vector" {
					want.PutVectorHeader(0)
				}
				if mode == "populated" {
					want.PutVectorHeader(1)
					want.PutID(0xbd610bc9)
					want.PutInt(0)
					want.PutInt(4)
				}
				want.PutVectorHeader(0)
				want.PutVectorHeader(0)
				if !bytes.Equal(got.Buf, want.Buf) {
					t.Fatalf("wire changed:\ngot  %x\nwant %x", got.Buf, want.Buf)
				}
				decoded, err := DecodeObject(profile, &got, Limits{})
				if err != nil || got.Len() != 0 {
					t.Fatalf("decode oracle: %v", err)
				}
				m := decoded.(*tg.MessagesDialogs).Messages[0].(*tg.Message)
				if int(m.Flags) != flags || m.Message != "test" {
					t.Fatalf("decoded flags/value: %+v", m)
				}
			})
		}
		for _, mode := range []string{"nil-optional", "partial-shared-group", "nested-error"} {
			t.Run(fmt.Sprintf("%d/reject/%s", profile, mode), func(t *testing.T) {
				message := codecTextMessage()
				switch mode {
				case "nil-optional":
					message.Flags.Set(3) // reply_to explicitly present but nil
				case "partial-shared-group":
					message.SetFwdFrom(tg.MessageFwdHeader{Date: 6, SavedFromMsgID: 1})
				case "nested-error":
					message.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityBold{Length: 1}, nil})
				}
				out := bin.Buffer{Buf: []byte{1, 2, 3}}
				if err := call.EncodeResult(&tg.MessagesDialogs{Messages: []tg.MessageClass{message}}, &out); err == nil {
					t.Fatal("malformed optional field was accepted")
				}
				if !bytes.Equal(out.Buf, []byte{1, 2, 3}) {
					t.Fatalf("failed encoding changed output: %x", out.Buf)
				}
			})
		}
	}
}

func TestSparseAbsentOptionalFieldAllocationBudget(t *testing.T) {
	value := &tg.MessagesDialogs{Messages: []tg.MessageClass{codecTextMessage()}}
	for profile := Profile225; profile <= Profile230; profile++ {
		call := codecDialogsCall(t, profile)
		out := bin.Buffer{Buf: make([]byte, 0, 1024)}
		allocations := testing.AllocsPerRun(100, func() {
			out.Buf = out.Buf[:0]
			if err := call.EncodeResult(value, &out); err != nil {
				t.Fatal(err)
			}
		})
		if allocations != 0 {
			t.Errorf("profile %d absent optional fields allocate %.0f times", profile, allocations)
		}
	}
}

var codecVectorSink bin.Object

func TestCanonicalVectorPreallocationBoundaries(t *testing.T) {
	for _, n := range []int{0, 1, 1023, 1024, 1025, 4096} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ids := make([]int, n)
			for i := range ids {
				ids[i] = i + 1
			}
			var encoded bin.Buffer
			if err := (&tg.MessagesDeleteMessagesRequest{ID: ids}).Encode(&encoded); err != nil {
				t.Fatal(err)
			}
			input := bin.Buffer{Buf: encoded.Buf}
			decoded, err := DecodeObject(ProfileCanonical, &input, Limits{})
			if err != nil || input.Len() != 0 {
				t.Fatalf("decode: %v", err)
			}
			got := decoded.(*tg.MessagesDeleteMessagesRequest).ID
			if len(got) != n {
				t.Fatalf("length %d != %d", len(got), n)
			}
			for i, id := range got {
				if id != ids[i] {
					t.Fatal("vector element changed")
				}
			}
			if n == bin.PreallocateLimit {
				allocations := testing.AllocsPerRun(100, func() {
					in := bin.Buffer{Buf: encoded.Buf}
					var err error
					codecVectorSink, err = DecodeObject(ProfileCanonical, &in, Limits{})
					if err != nil {
						t.Fatal(err)
					}
				})
				if allocations > 3 {
					t.Fatalf("vector decode allocations %.0f, want <= 3", allocations)
				}
			}
			for _, test := range []struct {
				wire   []byte
				limits Limits
			}{
				{encoded.Buf[:len(encoded.Buf)-1], Limits{}},
				{encoded.Buf, Limits{MaxVectorElements: 1}},
				{encoded.Buf, Limits{MaxAggregateElements: 1}},
			} {
				if n <= 1 && len(test.wire) == len(encoded.Buf) {
					continue
				}
				in := bin.Buffer{Buf: test.wire}
				if _, err := DecodeObject(ProfileCanonical, &in, test.limits); err == nil {
					t.Fatal("malformed/over-budget vector accepted")
				}
				if !bytes.Equal(in.Buf, test.wire) {
					t.Fatal("failed vector decode consumed input")
				}
			}
		})
	}
	for _, n := range []int{-1, 1 << 30} {
		var wire bin.Buffer
		wire.PutID(tg.MessagesDeleteMessagesRequestTypeID)
		wire.PutInt(0)
		wire.PutVectorHeader(n)
		before := wire.Copy()
		if _, err := DecodeObject(ProfileCanonical, &wire, Limits{}); err == nil {
			t.Fatal("invalid vector length accepted")
		}
		if !bytes.Equal(wire.Buf, before) {
			t.Fatal("invalid length consumed input")
		}
	}
}
