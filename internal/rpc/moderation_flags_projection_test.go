package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestModerationFlagsProjectToSelfUserAndWire(t *testing.T) {
	for _, test := range []struct {
		name string
		user domain.User
		scam bool
		fake bool
	}{
		{name: "scam", user: domain.User{ID: 1, Scam: true}, scam: true},
		{name: "fake", user: domain.User{ID: 2, Fake: true}, fake: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			projected := tgSelfUser(test.user)
			if projected.Scam != test.scam || projected.Fake != test.fake ||
				!projected.Self {
				t.Fatalf("projected self=%+v", projected)
			}
			var wire bin.Buffer
			if err := projected.Encode(&wire); err != nil {
				t.Fatal(err)
			}
			var decoded tg.User
			input := bin.Buffer{Buf: append([]byte(nil), wire.Buf...)}
			if err := decoded.Decode(&input); err != nil {
				t.Fatal(err)
			}
			if decoded.Scam != test.scam || decoded.Fake != test.fake ||
				!decoded.Self {
				t.Fatalf("decoded self=%+v", decoded)
			}
		})
	}
}

func TestModerationFlagsProjectToChannelWithoutMutatingAbout(t *testing.T) {
	channel := domain.Channel{
		ID: 10, AccessHash: 20, CreatorUserID: 1,
		Title: "Reported channel", About: "Owner description",
		Broadcast: true, Scam: true,
	}
	projected := tgChannel(2, channel, nil)
	if !projected.Scam || projected.Fake {
		t.Fatalf("projected channel=%+v", projected)
	}
	var wire bin.Buffer
	if err := projected.Encode(&wire); err != nil {
		t.Fatal(err)
	}
	var decoded tg.Channel
	input := bin.Buffer{Buf: append([]byte(nil), wire.Buf...)}
	if err := decoded.Decode(&input); err != nil {
		t.Fatal(err)
	}
	if !decoded.Scam || decoded.Fake {
		t.Fatalf("decoded channel=%+v", decoded)
	}
	full := tgChannelFull(domain.ChannelView{Channel: channel})
	if full.About != channel.About {
		t.Fatalf("about=%q, want unmodified %q", full.About, channel.About)
	}
}
