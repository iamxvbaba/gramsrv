package tlprofile

import (
	"encoding/binary"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

func TestPageBlockVectorExactProfiles(t *testing.T) {
	text := &tg.TextPlain{Text: "legacy quote"}
	caption := &tg.TextEmpty{}
	canonical := []tg.PageBlockClass{&tg.PageBlockBlockquote{
		Collapsed: true,
		Text:      text,
		Caption:   caption,
	}}

	tests := []struct {
		name        string
		profile     Profile
		constructor uint32
		collapsed   bool
	}{
		{name: "layer228", profile: Profile228, constructor: 0x263d7c26, collapsed: false},
		{name: "layer229", profile: Profile229, constructor: 0x66d1670b, collapsed: true},
		{name: "layer230", profile: Profile230, constructor: 0x66d1670b, collapsed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var wire bin.Buffer
			if err := EncodePageBlockVector(test.profile, canonical, &wire); err != nil {
				t.Fatal(err)
			}
			if got := binary.LittleEndian.Uint32(wire.Raw()[8:12]); got != test.constructor {
				t.Fatalf("PageBlock constructor = %#08x, want %#08x", got, test.constructor)
			}

			input := &bin.Buffer{Buf: wire.Copy()}
			decoded, err := DecodePageBlockVector(test.profile, input, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if input.Len() != 0 {
				t.Fatalf("successful decode left %d bytes", input.Len())
			}
			quote, ok := decoded[0].(*tg.PageBlockBlockquote)
			if !ok {
				t.Fatalf("decoded PageBlock = %T", decoded[0])
			}
			if quote.Collapsed != test.collapsed {
				t.Fatalf("collapsed = %v, want %v", quote.Collapsed, test.collapsed)
			}
		})
	}
}

func TestDecodePageBlockVectorIsTransactional(t *testing.T) {
	var legacy bin.Buffer
	if err := EncodePageBlockVector(Profile228, []tg.PageBlockClass{&tg.PageBlockBlockquote{
		Text:    &tg.TextPlain{Text: "legacy quote"},
		Caption: &tg.TextEmpty{},
	}}, &legacy); err != nil {
		t.Fatal(err)
	}
	input := &bin.Buffer{Buf: legacy.Copy()}
	before := input.Copy()
	if _, err := DecodePageBlockVector(Profile229, input, Limits{}); err == nil {
		t.Fatal("Layer 229 exact decoder accepted a Layer 228 constructor")
	}
	if got := input.Raw(); string(got) != string(before) {
		t.Fatal("failed decode consumed input")
	}
}

func TestEncodePageBlockVectorIsTransactional(t *testing.T) {
	out := &bin.Buffer{Buf: []byte{1, 2, 3}}
	if err := EncodePageBlockVector(Profile228, []tg.PageBlockClass{&tg.PageBlockDocument{DocumentID: 42}}, out); err == nil {
		t.Fatal("Layer 228 exact encoder accepted a Layer 229-only PageBlock")
	}
	if got := out.Raw(); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("failed encode changed output: %x", got)
	}
}
