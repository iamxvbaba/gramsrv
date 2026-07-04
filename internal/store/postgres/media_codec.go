package postgres

import (
	"encoding/json"
	"reflect"

	"telesrv/internal/domain"
)

// 媒体相关 JSON 编解码：domain 值对象 ↔ JSONB 列。domain.* 带 json tag，可直接 marshal。

// jsonArrayOrEmpty 把切片序列化为 JSONB；nil 序列化为 "[]"（列为 NOT NULL DEFAULT '[]'）。
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

// encodeMessageMedia 把消息媒体快照序列化为 JSONB；无媒体序列化为 "{}"。
func encodeMessageMedia(m *domain.MessageMedia) ([]byte, error) {
	if m.IsZero() {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// decodeMessageMedia 把消息行的 media JSONB 文本还原为 *MessageMedia；空载荷返回 nil。
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

// encodeReplyMarkup 把 inline keyboard 快照序列化为 JSONB；空 markup 序列化为 "{}"。
// callback data 是 []byte，json.Marshal 自动 base64（保证经 JSONB 字节级 round-trip）。
func encodeReplyMarkup(m *domain.MessageReplyMarkup) ([]byte, error) {
	if m.IsZero() {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// decodeReplyMarkup 把消息行的 reply_markup JSONB 文本还原为 *MessageReplyMarkup；
// 空载荷返回 nil。
func decodeReplyMarkup(s string) (*domain.MessageReplyMarkup, error) {
	if s == "" || s == "{}" || s == "null" {
		return nil, nil
	}
	var m domain.MessageReplyMarkup
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	if m.IsZero() {
		return nil, nil
	}
	return &m, nil
}

// encodeRichMessage 把富文本消息快照序列化为 JSONB；空载荷序列化为 "{}"。
// Blocks 是 []byte（不透明 TL），json.Marshal 自动 base64，保证 JSONB 字节级 round-trip。
func encodeRichMessage(m *domain.MessageRichMessage) ([]byte, error) {
	if m.IsZero() {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// decodeRichMessage 把消息行的 rich_message JSONB 文本还原为 *MessageRichMessage；
// 空载荷返回 nil。
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
