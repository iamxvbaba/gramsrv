package tlprofile

import (
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

var benchmarkRouteSink tlRoute

func BenchmarkSparseDialogsResult(b *testing.B) {
	for _, profile := range []Profile{Profile225, Profile229, Profile230} {
		for _, populated := range []bool{false, true} {
			b.Run(fmt.Sprintf("%d/optional=%t", profile, populated), func(b *testing.B) {
				call := codecDialogsCall(b, profile)
				value := &tg.MessagesDialogs{}
				for i := range 100 {
					id := int64(i + 1)
					message := &tg.Message{ID: i + 1, PeerID: &tg.PeerUser{UserID: id}, Date: 1_900_000_000, Message: "A representative text message with enough content for the codec audit."}
					if populated {
						message.SetFromID(&tg.PeerUser{UserID: 99})
						message.SetReplyTo(&tg.MessageReplyHeader{ReplyToMsgID: 1})
						message.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityBold{Length: 10}})
					}
					value.Dialogs = append(value.Dialogs, &tg.Dialog{Peer: &tg.PeerUser{UserID: id}, TopMessage: i + 1})
					value.Messages = append(value.Messages, message)
					value.Users = append(value.Users, &tg.User{ID: id, FirstName: "Alice", LastName: "Example"})
				}
				out := bin.Buffer{Buf: make([]byte, 0, 128<<10)}
				b.ReportAllocs()
				for b.Loop() {
					out.Buf = out.Buf[:0]
					if err := call.EncodeResult(value, &out); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkExactVectorDecode(b *testing.B) {
	for _, n := range []int{100, 1023, 1024, 1025, 4096} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			var encoded bin.Buffer
			if err := (&tg.MessagesDeleteMessagesRequest{ID: make([]int, n)}).Encode(&encoded); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				input := bin.Buffer{Buf: encoded.Buf}
				var err error
				codecVectorSink, err = DecodeObject(ProfileCanonical, &input, Limits{})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkExactObjectCodec(b *testing.B) {
	direct := &tg.UpdateUserStatus{
		UserID: 42,
		Status: &tg.UserStatusOnline{Expires: 1_900_000_000},
	}
	rewrite := &tg.Channel{
		ID:    100,
		Title: "layer benchmark",
		Photo: &tg.ChatPhotoEmpty{},
		Date:  1,
	}

	benchmarkExactEncode(b, "direct", Profile225, direct)
	benchmarkExactDecode(b, "direct", Profile225, direct)
	benchmarkExactEncode(b, "rewrite", Profile225, rewrite)
	benchmarkExactDecode(b, "rewrite", Profile225, rewrite)
}

func benchmarkExactEncode(b *testing.B, name string, profile Profile, value bin.Object) {
	b.Helper()
	var out bin.Buffer
	if err := EncodeObject(profile, value, &out); err != nil {
		b.Fatal(err)
	}
	capacity := len(out.Buf)

	b.Run(name+"/encode", func(b *testing.B) {
		b.ReportAllocs()
		out.Buf = make([]byte, 0, capacity)
		b.ResetTimer()
		for b.Loop() {
			out.Buf = out.Buf[:0]
			if err := EncodeObject(profile, value, &out); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func benchmarkExactDecode(b *testing.B, name string, profile Profile, value bin.Object) {
	b.Helper()
	var encoded bin.Buffer
	if err := EncodeObject(profile, value, &encoded); err != nil {
		b.Fatal(err)
	}
	wire := encoded.Copy()

	b.Run(name+"/decode", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			input := bin.Buffer{Buf: wire}
			if _, err := DecodeObject(profile, &input, Limits{}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkExactAdmission(b *testing.B) {
	cases := []struct {
		name    string
		request bin.Object
	}{
		{name: "direct", request: &tg.HelpGetConfigRequest{}},
		{name: "rewrite", request: &tg.AccountGetNotifySettingsRequest{Peer: &tg.InputNotifyUsers{}}},
	}
	dispatcher := NewDispatcher()

	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			var encoded bin.Buffer
			if err := EncodeObject(Profile225, test.request, &encoded); err != nil {
				b.Fatal(err)
			}
			wire := encoded.Copy()

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				input := bin.Buffer{Buf: wire}
				if _, err := dispatcher.Admit(Profile225, &input, Limits{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkLookupRoute(b *testing.B) {
	cases := []struct {
		name    string
		profile Profile
		wireID  uint32
	}{
		{name: "universal-direct", profile: Profile225, wireID: tg.HelpGetConfigRequestTypeID},
		{name: "split-rewrite", profile: Profile225, wireID: tg.AccountGetNotifySettingsRequestTypeID},
	}

	for _, test := range cases {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				route, ok := tlLookupRoute(test.profile, test.wireID)
				if !ok {
					b.Fatal("route not found")
				}
				benchmarkRouteSink = route
			}
		})
	}
}
