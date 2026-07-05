package postgres

import (
	"testing"

	"telesrv/internal/domain"
)

func TestMessageEntityJSONRoundTripPreservesFormattedDate(t *testing.T) {
	in := []domain.MessageEntity{
		{
			Type:      domain.MessageEntityFormattedDate,
			Offset:    2,
			Length:    5,
			Date:      1773436800,
			Relative:  true,
			ShortTime: true,
			LongDate:  true,
			DayOfWeek: true,
		},
	}
	raw, err := encodeMessageEntities(in)
	if err != nil {
		t.Fatalf("encodeMessageEntities: %v", err)
	}
	out, err := decodeMessageEntities(string(raw))
	if err != nil {
		t.Fatalf("decodeMessageEntities: %v", err)
	}
	if !sameMessageEntities(in, out) {
		t.Fatalf("round-trip entities = %+v raw=%s, want %+v", out, raw, in)
	}
}
