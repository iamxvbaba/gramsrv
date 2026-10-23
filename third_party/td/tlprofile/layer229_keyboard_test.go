package tlprofile

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestLayer229ReplyKeyboardButtonRoundTripToLegacyProfiles(t *testing.T) {
	canonical := &tg.KeyboardButton{
		Text: "phone",
		Type: &tg.ButtonTypeRequestPhone{},
	}
	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		t.Run(fmt.Sprintf("layer-%d", profile), func(t *testing.T) {
			var wire bin.Buffer
			if err := EncodeObject(profile, canonical, &wire); err != nil {
				t.Fatal(err)
			}
			id, err := wire.PeekID()
			if err != nil {
				t.Fatal(err)
			}
			if id != 0x417efd8f {
				t.Fatalf("legacy request-phone wire ID = %#08x, want %#08x", id, uint32(0x417efd8f))
			}
			decoded, err := DecodeObject(profile, &wire, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			button, ok := decoded.(*tg.KeyboardButton)
			if !ok || button.Text != canonical.Text {
				t.Fatalf("decoded button = %#v", decoded)
			}
			if _, ok := button.Type.(*tg.ButtonTypeRequestPhone); !ok {
				t.Fatalf("decoded button type = %T", button.Type)
			}
		})
	}

	var wire bin.Buffer
	if err := EncodeObject(Profile229, canonical, &wire); err != nil {
		t.Fatal(err)
	}
	id, err := wire.PeekID()
	if err != nil {
		t.Fatal(err)
	}
	if id != tg.KeyboardButtonTypeID {
		t.Fatalf("Layer 229 keyboard wire ID = %#08x, want %#08x", id, uint32(tg.KeyboardButtonTypeID))
	}
}

func TestLayer229InlineKeyboardRowsRoundTripToLegacyProfiles(t *testing.T) {
	canonical := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{Buttons: []tg.KeyboardInlineButton{{
		Text: "callback",
		Type: &tg.InlineButtonTypeCallback{RequiresPassword: true, Data: []byte{1, 2, 3}},
	}}}}}
	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		t.Run(fmt.Sprintf("layer-%d", profile), func(t *testing.T) {
			var wire bin.Buffer
			if err := EncodeObject(profile, canonical, &wire); err != nil {
				t.Fatal(err)
			}
			id, err := wire.PeekID()
			if err != nil {
				t.Fatal(err)
			}
			if id != 0x48a30254 {
				t.Fatalf("legacy reply-inline wire ID = %#08x, want %#08x", id, uint32(0x48a30254))
			}
			decoded, err := DecodeObject(profile, &wire, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			markup, ok := decoded.(*tg.ReplyInlineMarkup)
			if !ok || len(markup.Rows) != 1 || len(markup.Rows[0].Buttons) != 1 {
				t.Fatalf("decoded markup = %#v", decoded)
			}
			button := markup.Rows[0].Buttons[0]
			callback, ok := button.Type.(*tg.InlineButtonTypeCallback)
			if !ok || button.Text != "callback" || !callback.RequiresPassword || !bytes.Equal(callback.Data, []byte{1, 2, 3}) {
				t.Fatalf("decoded inline button = %#v / %T", button, button.Type)
			}
		})
	}
}

func TestLayer229InlineForceReplyFailsClosedForLegacyProfiles(t *testing.T) {
	canonical := &tg.ReplyInlineMarkup{ForceReply: true}
	for _, profile := range []Profile{Profile225, Profile226, Profile227, Profile228} {
		var wire bin.Buffer
		wire.PutInt(99)
		before := append([]byte(nil), wire.Raw()...)
		if err := EncodeObject(profile, canonical, &wire); err == nil {
			t.Fatalf("profile %d silently dropped force_reply", profile)
		}
		if !bytes.Equal(wire.Raw(), before) {
			t.Fatalf("profile %d failed encode was not transactional", profile)
		}
	}
}
