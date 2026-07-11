package layerwire

import (
	"errors"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestValidateRoutableRequestCompatibilityAndUnknown(t *testing.T) {
	t.Run("legacy theme is fully walked", func(t *testing.T) {
		var b bin.Buffer
		b.PutID(0x8d9d742b)
		b.PutString("android")
		(&tg.InputThemeSlug{Slug: "night"}).Encode(&b)
		b.PutLong(42)
		known, err := ValidateRoutableRequest(b.Buf)
		if err != nil || !known {
			t.Fatalf("legacy theme known=%v err=%v, want true/nil", known, err)
		}

		b.Buf = b.Buf[:len(b.Buf)-4]
		known, err = ValidateRoutableRequest(b.Buf)
		if !known || !errors.Is(err, ErrMalformed) {
			t.Fatalf("truncated legacy theme known=%v err=%v, want true/malformed", known, err)
		}
	})

	t.Run("unknown stays opaque and bounded", func(t *testing.T) {
		var b bin.Buffer
		b.PutID(0x12345678)
		b.PutUint32(0xffffffff)
		known, err := ValidateRoutableRequest(b.Buf)
		if err != nil || known {
			t.Fatalf("opaque unknown known=%v err=%v, want false/nil", known, err)
		}
		known, err = ValidateRoutableRequest(append(b.Buf, 1))
		if known || !errors.Is(err, ErrMalformed) {
			t.Fatalf("unaligned unknown known=%v err=%v, want false/malformed", known, err)
		}
	})
}
