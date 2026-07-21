package botapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

const maxBotAPIRichSourceBytes = 256 << 10

func richMessageInputFromAPI(raw string) (domain.BotAPIRichMessageInput, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxBotAPIRichSourceBytes {
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
	}
	var out domain.BotAPIRichMessageInput
	sources := 0
	if value, ok := fields["html"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if err := json.Unmarshal(value, &out.HTML); err != nil || out.HTML == "" {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
		sources++
	}
	if value, ok := fields["markdown"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if err := json.Unmarshal(value, &out.Markdown); err != nil || out.Markdown == "" {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
		sources++
	}
	if value, ok := fields["blocks"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		if len(bytes.TrimSpace(value)) == 0 {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
		out.BlocksJSON = append([]byte(nil), value...)
		sources++
	}
	if sources != 1 {
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
	}
	if len(out.BlocksJSON) != 0 {
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_BLOCKS_UNSUPPORTED")
	}
	if value, ok := fields["media"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !bytes.Equal(bytes.TrimSpace(value), []byte("[]")) {
		out.MediaJSON = append([]byte(nil), value...)
		return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_MEDIA_UNSUPPORTED")
	}
	if value, ok := fields["is_rtl"]; ok {
		if err := json.Unmarshal(value, &out.RTL); err != nil {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
	}
	if value, ok := fields["skip_entity_detection"]; ok {
		if err := json.Unmarshal(value, &out.SkipEntityDetection); err != nil {
			return domain.BotAPIRichMessageInput{}, errors.New("RICH_MESSAGE_INVALID")
		}
	}
	return out, nil
}

func richReplyMessageID(values map[string]string) (int, error) {
	legacy := apiInt(values["reply_to_message_id"], 0)
	raw := strings.TrimSpace(values["reply_parameters"])
	if raw == "" {
		if legacy < 0 {
			return 0, errors.New("REPLY_MESSAGE_ID_INVALID")
		}
		return legacy, nil
	}
	if legacy != 0 {
		return 0, errors.New("REPLY_PARAMETERS_INVALID")
	}
	var payload struct {
		MessageID int `json:"message_id"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil || payload.MessageID <= 0 {
		return 0, errors.New("REPLY_PARAMETERS_INVALID")
	}
	return payload.MessageID, nil
}

func apiInt64(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("VALUE_INVALID")
	}
	return value, nil
}
