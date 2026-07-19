package postgres

import (
	"testing"

	"telesrv/internal/domain"
)

func TestReplyMarkupCodecValidatesTaggedUnion(t *testing.T) {
	keyboard := &domain.MessageReplyMarkup{
		Type:        domain.MessageReplyMarkupKeyboard,
		Keyboard:    [][]domain.MarkupButton{{{Type: domain.MarkupButtonText, Text: "Help"}}},
		Resize:      true,
		Placeholder: "Choose",
	}
	raw, err := encodeReplyMarkup(keyboard)
	if err != nil {
		t.Fatalf("encode reply keyboard: %v", err)
	}
	got, err := decodeReplyMarkup(string(raw))
	if err != nil || got == nil || got.Kind() != domain.MessageReplyMarkupKeyboard ||
		len(got.Keyboard) != 1 || got.Keyboard[0][0].Text != "Help" || !got.Resize || got.Placeholder != "Choose" {
		t.Fatalf("decoded reply keyboard = %#v, err=%v", got, err)
	}

	// Pre-union inline snapshots intentionally remain readable.
	legacy, err := decodeReplyMarkup(`{"inline":[[{"type":"callback","text":"OK","data":"b2s="}]]}`)
	if err != nil || legacy == nil || legacy.Kind() != domain.MessageReplyMarkupInline {
		t.Fatalf("legacy inline markup = %#v, err=%v", legacy, err)
	}

	malformed := &domain.MessageReplyMarkup{
		Type:     domain.MessageReplyMarkupInline,
		Keyboard: [][]domain.MarkupButton{{{Type: domain.MarkupButtonText, Text: "wrong"}}},
	}
	if _, err := encodeReplyMarkup(malformed); err == nil {
		t.Fatal("malformed union must fail at the write boundary")
	}
	if _, err := decodeReplyMarkup(`{"type":"inline","keyboard":[[{"type":"text","text":"wrong"}]]}`); err == nil {
		t.Fatal("malformed stored union must fail at the read boundary")
	}
}
