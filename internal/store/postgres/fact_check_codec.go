package postgres

import (
	"encoding/json"
	"fmt"

	"telesrv/internal/domain"
)

// factCheckJSON 是 channel_messages.factcheck 列的 JSON 形态。
// 空对象表示这条消息没有事实核查标签。
type factCheckJSON struct {
	NeedCheck bool                   `json:"need_check"`
	Country   string                 `json:"country"`
	Text      string                 `json:"text"`
	Entities  []domain.MessageEntity `json:"entities"`
	Hash      int64                  `json:"hash"`
}

func encodeFactCheck(check *domain.FactCheck) ([]byte, error) {
	if check == nil {
		return []byte("{}"), nil
	}
	payload, err := json.Marshal(factCheckJSON{
		NeedCheck: check.NeedCheck,
		Country:   check.Country,
		Text:      check.Text,
		Entities:  check.Entities,
		Hash:      check.Hash,
	})
	if err != nil {
		return nil, fmt.Errorf("encode fact check: %w", err)
	}
	return payload, nil
}

func decodeFactCheck(raw string) (*domain.FactCheck, error) {
	if raw == "" || raw == "{}" || raw == "null" {
		return nil, nil
	}
	var payload factCheckJSON
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("decode fact check: %w", err)
	}
	if payload.Text == "" && payload.Country == "" && !payload.NeedCheck && payload.Hash == 0 {
		return nil, nil
	}
	return &domain.FactCheck{
		NeedCheck: payload.NeedCheck,
		Country:   payload.Country,
		Text:      payload.Text,
		Entities:  payload.Entities,
		Hash:      payload.Hash,
	}, nil
}
