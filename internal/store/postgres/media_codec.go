package postgres

import (
	"encoding/json"
	"fmt"
	"reflect"

	"telesrv/internal/domain"
)

// Media-related JSON encoding/decoding: domain value objects ↔ JSONB columns. domain.* carries json tags and can be marshaled directly.

// jsonArrayOrEmpty serializes a slice to JSONB; nil serializes to "[]" (the column is NOT NULL DEFAULT '[]').
func jsonArrayOrEmpty(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return []byte("[]"), nil
	}
	return b, nil
}

// encodeMessageMedia serializes a message media snapshot to JSONB; no media serializes to "{}".
func encodeMessageMedia(m *domain.MessageMedia) ([]byte, error) {
	if m.IsZero() {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// decodeMessageMedia restores the media JSONB text of a message row to *MessageMedia; an empty payload returns nil.
func decodeMessageMedia(s string) (*domain.MessageMedia, error) {
	if s == "" || s == "{}" || s == "null" {
		return nil, nil
	}
	var m domain.MessageMedia
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	if m.IsZero() {
		return nil, nil
	}
	return &m, nil
}

// encodeReplyMarkup serializes a reply/inline keyboard snapshot to JSONB; an empty markup serializes to "{}".
// callback data is []byte and json.Marshal base64-encodes it automatically (guaranteeing a byte-level round-trip through JSONB).
func encodeReplyMarkup(m *domain.MessageReplyMarkup) ([]byte, error) {
	if err := domain.ValidateReplyMarkup(m); err != nil {
		return nil, fmt.Errorf("encode reply markup: %w", err)
	}
	if m.IsZero() {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// decodeReplyMarkup restores the reply_markup JSONB text of a message row to
// *MessageReplyMarkup; an empty payload returns nil.
func decodeReplyMarkup(s string) (*domain.MessageReplyMarkup, error) {
	if s == "" || s == "{}" || s == "null" {
		return nil, nil
	}
	var m domain.MessageReplyMarkup
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	if err := domain.ValidateReplyMarkup(&m); err != nil {
		return nil, fmt.Errorf("decode reply markup: %w", err)
	}
	if m.IsZero() {
		return nil, nil
	}
	return &m, nil
}

// encodeRichMessage serializes a rich text message snapshot to JSONB; an empty payload serializes to "{}".
// Blocks is []byte (opaque TL) and json.Marshal base64-encodes it automatically, guaranteeing a byte-level round-trip through JSONB.
func encodeRichMessage(m *domain.MessageRichMessage) ([]byte, error) {
	if m.IsZero() {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// decodeRichMessage restores the rich_message JSONB text of a message row to
// *MessageRichMessage; an empty payload returns nil.
func decodeRichMessage(s string) (*domain.MessageRichMessage, error) {
	if s == "" || s == "{}" || s == "null" {
		return nil, nil
	}
	var m domain.MessageRichMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	if m.IsZero() {
		return nil, nil
	}
	return &m, nil
}

func richMessagesEqual(a, b *domain.MessageRichMessage) bool {
	if a.IsZero() && b.IsZero() {
		return true
	}
	return reflect.DeepEqual(a, b)
}

func decodePhotoSizes(s string) ([]domain.PhotoSize, error) {
	if s == "" || s == "[]" || s == "null" {
		return nil, nil
	}
	var out []domain.PhotoSize
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeDocumentAttributes(s string) ([]domain.DocumentAttribute, error) {
	if s == "" || s == "[]" || s == "null" {
		return nil, nil
	}
	var out []domain.DocumentAttribute
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeInt64Slice(s string) ([]int64, error) {
	if s == "" || s == "[]" || s == "null" {
		return nil, nil
	}
	var out []int64
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeStickerPacks(s string) ([]domain.StickerPack, error) {
	if s == "" || s == "[]" || s == "null" {
		return nil, nil
	}
	var out []domain.StickerPack
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func decodeStickerKeywords(s string) ([]domain.StickerKeyword, error) {
	if s == "" || s == "[]" || s == "null" {
		return nil, nil
	}
	var out []domain.StickerKeyword
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	return out, nil
}
