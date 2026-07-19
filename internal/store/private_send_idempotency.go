package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"telesrv/internal/domain"
)

const privateSendFingerprintVersion = 1

const channelSendFingerprintVersion = 1

const sendSnapshotVersion = 1

type privateSendSnapshotEnvelope struct {
	Version int            `json:"version"`
	Message domain.Message `json:"message"`
}

type channelSendSnapshotEnvelope struct {
	Version int                   `json:"version"`
	Message domain.ChannelMessage `json:"message"`
}

// privateSendFingerprintPayload 只包含一次发送的客户端不可变意图。Date、origin
// auth/session、当前 block 状态与 automation 元数据均为执行环境，不得让同一请求在
// 重连或状态变化后变成另一条逻辑消息。sender/random_id 由幂等索引键单独约束。
type privateSendFingerprintPayload struct {
	Version         int                        `json:"version"`
	RecipientUserID int64                      `json:"recipient_user_id"`
	Message         string                     `json:"message"`
	Entities        []domain.MessageEntity     `json:"entities"`
	Media           *domain.MessageMedia       `json:"media"`
	Silent          bool                       `json:"silent"`
	NoForwards      bool                       `json:"noforwards"`
	ReplyTo         *domain.MessageReply       `json:"reply_to,omitempty"`
	Forward         *domain.MessageForward     `json:"forward,omitempty"`
	TTLPeriod       int                        `json:"ttl_period"`
	ViaBotID        int64                      `json:"via_bot_id"`
	GroupedID       int64                      `json:"grouped_id"`
	Effect          int64                      `json:"effect"`
	ReplyMarkup     *domain.MessageReplyMarkup `json:"reply_markup"`
	RichMessage     *domain.MessageRichMessage `json:"rich_message"`
}

// channelSendFingerprintPayload contains only the durable intent of an internal channel send.
// Operational projection fields (Date, recipient/mention lists, PostAuthor and lookup hints) are
// intentionally absent: they may change after the first commit and cannot redefine a replay.
type channelSendFingerprintPayload struct {
	Version     int                          `json:"version"`
	ChannelID   int64                        `json:"channel_id"`
	Message     string                       `json:"message"`
	Entities    []domain.MessageEntity       `json:"entities"`
	Media       *domain.MessageMedia         `json:"media"`
	Silent      bool                         `json:"silent"`
	NoForwards  bool                         `json:"noforwards"`
	ReplyTo     *domain.MessageReply         `json:"reply_to,omitempty"`
	Forward     *domain.MessageForward       `json:"forward,omitempty"`
	ViaBotID    int64                        `json:"via_bot_id"`
	GroupedID   int64                        `json:"grouped_id"`
	ReplyMarkup *domain.MessageReplyMarkup   `json:"reply_markup"`
	RichMessage *domain.MessageRichMessage   `json:"rich_message"`
	SendAs      *domain.Peer                 `json:"send_as,omitempty"`
	Action      *domain.ChannelMessageAction `json:"action,omitempty"`
	TTLPeriod   int                          `json:"ttl_period"`
}

type monoforumSendFingerprintPayload struct {
	Version        int                    `json:"version"`
	ChannelID      int64                  `json:"channel_id"`
	SavedPeer      domain.Peer            `json:"saved_peer"`
	Message        string                 `json:"message"`
	Entities       []domain.MessageEntity `json:"entities"`
	Media          *domain.MessageMedia   `json:"media"`
	ReplyTo        *domain.MessageReply   `json:"reply_to"`
	Silent         bool                   `json:"silent"`
	NoForwards     bool                   `json:"noforwards"`
	SuggestedPost  *domain.SuggestedPost  `json:"suggested_post,omitempty"`
	AllowPaidStars int64                  `json:"allow_paid_stars"`
}

// PrivateSendFingerprint returns a SHA-256 fingerprint of the original send
// intent. RPC callers should supply their precomputed raw-TL fingerprint;
// internal callers get a deterministic domain-level fallback.
func PrivateSendFingerprint(req domain.SendPrivateTextRequest) ([]byte, error) {
	if len(req.IdempotencyFingerprint) > 0 {
		if len(req.IdempotencyFingerprint) != sha256.Size {
			return nil, fmt.Errorf("private send idempotency fingerprint: got %d bytes, want %d", len(req.IdempotencyFingerprint), sha256.Size)
		}
		return append([]byte(nil), req.IdempotencyFingerprint...), nil
	}
	payload, err := json.Marshal(privateSendFingerprintPayload{
		Version:         privateSendFingerprintVersion,
		RecipientUserID: req.RecipientUserID,
		Message:         req.Message,
		Entities:        req.Entities,
		Media:           req.Media,
		Silent:          req.Silent,
		NoForwards:      req.NoForwards,
		ReplyTo:         req.ReplyTo,
		Forward:         req.Forward,
		TTLPeriod:       req.TTLPeriod,
		ViaBotID:        req.ViaBotID,
		GroupedID:       req.GroupedID,
		Effect:          req.Effect,
		ReplyMarkup:     req.ReplyMarkup,
		RichMessage:     req.RichMessage,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal private send fingerprint: %w", err)
	}
	sum := sha256.Sum256(payload)
	return sum[:], nil
}

// ChannelSendFingerprint returns the request-boundary SHA-256 value when present, otherwise a
// deterministic domain fallback for app/Bot API callers that do not originate from a TL request.
func ChannelSendFingerprint(req domain.SendChannelMessageRequest) ([]byte, error) {
	if len(req.IdempotencyFingerprint) > 0 {
		if err := ValidateSendFingerprint(req.IdempotencyFingerprint, "channel send"); err != nil {
			return nil, err
		}
		return append([]byte(nil), req.IdempotencyFingerprint...), nil
	}
	payload, err := json.Marshal(channelSendFingerprintPayload{
		Version:     channelSendFingerprintVersion,
		ChannelID:   req.ChannelID,
		Message:     req.Message,
		Entities:    req.Entities,
		Media:       req.Media,
		Silent:      req.Silent,
		NoForwards:  req.NoForwards,
		ReplyTo:     req.ReplyTo,
		Forward:     req.Forward,
		ViaBotID:    req.ViaBotID,
		GroupedID:   req.GroupedID,
		ReplyMarkup: req.ReplyMarkup,
		RichMessage: req.RichMessage,
		SendAs:      req.SendAs,
		Action:      req.Action,
		TTLPeriod:   req.TTLPeriod,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal channel send fingerprint: %w", err)
	}
	sum := sha256.Sum256(payload)
	return sum[:], nil
}

// MonoforumSendFingerprint is scoped to one subscriber sub-dialog. SavedPeer is also part of the
// lookup key, but retaining it in the fallback prevents a future index/scope regression from
// silently accepting a cross-dialog replay.
func MonoforumSendFingerprint(req domain.SendMonoforumMessageRequest) ([]byte, error) {
	if len(req.IdempotencyFingerprint) > 0 {
		if err := ValidateSendFingerprint(req.IdempotencyFingerprint, "monoforum send"); err != nil {
			return nil, err
		}
		return append([]byte(nil), req.IdempotencyFingerprint...), nil
	}
	payload, err := json.Marshal(monoforumSendFingerprintPayload{
		Version:        channelSendFingerprintVersion,
		ChannelID:      req.MonoforumID,
		SavedPeer:      req.SavedPeer,
		Message:        req.Message,
		Entities:       req.Entities,
		Media:          req.Media,
		ReplyTo:        req.ReplyTo,
		Silent:         req.Silent,
		NoForwards:     req.NoForwards,
		SuggestedPost:  req.SuggestedPost,
		AllowPaidStars: req.AllowPaidStars,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal monoforum send fingerprint: %w", err)
	}
	sum := sha256.Sum256(payload)
	return sum[:], nil
}

// ValidateSendFingerprint rejects empty, truncated and oversized receipts. Callers must never
// guess a legacy/corrupt fingerprint from a mutable message projection.
func ValidateSendFingerprint(fingerprint []byte, operation string) error {
	if len(fingerprint) != sha256.Size {
		return fmt.Errorf("%s idempotency fingerprint: got %d bytes, want %d", operation, len(fingerprint), sha256.Size)
	}
	return nil
}

// SameSendFingerprint requires two complete SHA-256 values.
func SameSendFingerprint(stored, expected []byte) bool {
	return len(stored) == sha256.Size && len(expected) == sha256.Size && bytes.Equal(stored, expected)
}

// SamePrivateSendFingerprint requires two complete SHA-256 values. Legacy or
// corrupt empty values are never guessed from mutable message projections.
func SamePrivateSendFingerprint(stored, expected []byte) bool {
	return SameSendFingerprint(stored, expected)
}

// EncodePrivateSendSnapshot freezes the sender-visible message returned by the
// first successful send. The snapshot is independent from mutable message-box
// projections so an edit or delete cannot erase the facts needed to acknowledge
// a later lost-response replay.
func EncodePrivateSendSnapshot(msg domain.Message) ([]byte, error) {
	if msg.ID <= 0 || msg.UID <= 0 || msg.RandomID == 0 || msg.OwnerUserID == 0 || msg.Pts <= 0 {
		return nil, fmt.Errorf("private send snapshot: invalid id=%d uid=%d random_id=%d owner=%d pts=%d", msg.ID, msg.UID, msg.RandomID, msg.OwnerUserID, msg.Pts)
	}
	raw, err := json.Marshal(privateSendSnapshotEnvelope{Version: sendSnapshotVersion, Message: msg})
	if err != nil {
		return nil, fmt.Errorf("marshal private send snapshot: %w", err)
	}
	return raw, nil
}

// DecodePrivateSendSnapshot returns a fresh object graph on every replay. Empty
// legacy rows are rejected rather than reconstructed from an edited projection.
func DecodePrivateSendSnapshot(raw []byte) (domain.Message, error) {
	var envelope privateSendSnapshotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return domain.Message{}, fmt.Errorf("unmarshal private send snapshot: %w", err)
	}
	msg := envelope.Message
	if envelope.Version != sendSnapshotVersion || msg.ID <= 0 || msg.UID <= 0 || msg.RandomID == 0 || msg.OwnerUserID == 0 || msg.Pts <= 0 {
		return domain.Message{}, fmt.Errorf("private send snapshot: invalid version=%d id=%d uid=%d random_id=%d owner=%d pts=%d", envelope.Version, msg.ID, msg.UID, msg.RandomID, msg.OwnerUserID, msg.Pts)
	}
	return msg, nil
}

// EncodeChannelSendSnapshot freezes the first sender echo for random_id replay.
func EncodeChannelSendSnapshot(msg domain.ChannelMessage) ([]byte, error) {
	if msg.ChannelID == 0 || msg.ID <= 0 || msg.RandomID == 0 || msg.SenderUserID == 0 || msg.Pts <= 0 {
		return nil, fmt.Errorf("channel send snapshot: invalid channel=%d id=%d random_id=%d sender=%d pts=%d", msg.ChannelID, msg.ID, msg.RandomID, msg.SenderUserID, msg.Pts)
	}
	raw, err := json.Marshal(channelSendSnapshotEnvelope{Version: sendSnapshotVersion, Message: msg})
	if err != nil {
		return nil, fmt.Errorf("marshal channel send snapshot: %w", err)
	}
	return raw, nil
}

// DecodeChannelSendSnapshot returns a fresh immutable first-send projection.
func DecodeChannelSendSnapshot(raw []byte) (domain.ChannelMessage, error) {
	var envelope channelSendSnapshotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return domain.ChannelMessage{}, fmt.Errorf("unmarshal channel send snapshot: %w", err)
	}
	msg := envelope.Message
	if envelope.Version != sendSnapshotVersion || msg.ChannelID == 0 || msg.ID <= 0 || msg.RandomID == 0 || msg.SenderUserID == 0 || msg.Pts <= 0 {
		return domain.ChannelMessage{}, fmt.Errorf("channel send snapshot: invalid version=%d channel=%d id=%d random_id=%d sender=%d pts=%d", envelope.Version, msg.ChannelID, msg.ID, msg.RandomID, msg.SenderUserID, msg.Pts)
	}
	return msg, nil
}
