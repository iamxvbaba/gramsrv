package rpc

import (
	"strings"
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
	// The basic tg.Channel carries no About, so only the full projection can
	// show the mark: it prepends the server warning above the owner's text
	// while the stored description itself stays untouched.
	full := tgChannelFull(domain.ChannelView{Channel: channel})
	wantAbout := domain.ModerationProfileWarning(true, false, channel.About)
	if full.About != wantAbout {
		t.Fatalf("about=%q, want warning over owner text %q", full.About, wantAbout)
	}
	if !strings.HasSuffix(full.About, "Owner description") {
		t.Fatalf("about=%q, want the owner's own text kept below the warning", full.About)
	}
	if channel.About != "Owner description" {
		t.Fatalf("stored about mutated to %q", channel.About)
	}
}
