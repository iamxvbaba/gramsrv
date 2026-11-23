package push

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// errUnsupportedPushType is returned for token types this server cannot deliver
// to (stored, but never pushed).
var errUnsupportedPushType = errors.New("unsupported push token type")

// Notification is the JSON object the official clients render after decrypting
// the FCM `p` field, and (for web push) the object the service worker reads
// directly. Field names and meaning come from
// https://core.telegram.org/api/push-updates#notification-structure plus the
// Telegram Web K PushNotificationObject type.
type Notification struct {
	LocKey  string         `json:"loc_key"`
	LocArgs []string       `json:"loc_args,omitempty"`
	Custom  map[string]any `json:"custom,omitempty"`
	UserID  int64          `json:"user_id,omitempty"`
	Sound   string         `json:"sound,omitempty"`
	// Title/Description are used by the web service worker to render the
	// notification text directly (Android derives it from loc_key/loc_args).
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	RandomID    int64  `json:"random_id,omitempty"`
	// Muted marks a MESSAGE_MUTED badge refresh: the web client suppresses the
	// visible notification. Not serialized in the FCM payload.
	Muted bool `json:"-"`
}

// plaintext serialises the notification as the client expects: a 32-bit
// little-endian length followed by the JSON body.
func (n Notification) plaintext() ([]byte, error) {
	body, err := json.Marshal(n)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 4+len(body))
	binary.LittleEndian.PutUint32(out[:4], uint32(len(body)))
	copy(out[4:], body)
	return out, nil
}

// webPushPayload renders the object Telegram Web K's service worker expects
// (PushNotificationObject). The browser has already decrypted the RFC 8291
// payload by the time the worker sees it.
func (n Notification) webPushPayload(userID int64) ([]byte, error) {
	mute := "0"
	if n.Muted {
		mute = "1"
	}
	obj := map[string]any{
		"loc_key":     n.LocKey,
		"loc_args":    n.LocArgs,
		"custom":      n.Custom,
		"random_id":   n.RandomID,
		"description": n.Description,
		"title":       n.Title,
		"mute":        mute,
		"user_id":     userID,
	}
	if n.Sound != "" {
		obj["sound"] = n.Sound
	}
	return json.Marshal(obj)
}

// Service seals and delivers a notification to a user's registered devices,
// dispatching by token type (FCM or web push).
type Service struct {
	fcm     Sender
	webPush *WebPushSender
	log     *zap.Logger
}

// NewService builds a push Service. Either sender may be nil; a token for a
// provider without a sender is skipped.
func NewService(fcm Sender, webPush *WebPushSender, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{fcm: fcm, webPush: webPush, log: log}
}

// Send delivers a notification to one device token.
func (s *Service) Send(ctx context.Context, token domain.PushToken, n Notification) error {
	if s == nil {
		return errors.New("push: no service configured")
	}
	switch token.TokenType {
	case domain.PushTokenTypeFCM:
		if s.fcm == nil {
			return errors.New("push: no FCM sender configured")
		}
		if len(token.Secret) < 128 {
			return errSecretTooShort
		}
		plaintext, err := n.plaintext()
		if err != nil {
			return err
		}
		sealed, err := encryptPushPayload(token.Secret, plaintext)
		if err != nil {
			return err
		}
		return s.fcm.Send(ctx, token.Token, map[string]string{"p": sealed})
	case domain.PushTokenTypeWebPush:
		if s.webPush == nil {
			return errNoWebPush
		}
		payload, err := n.webPushPayload(token.UserID)
		if err != nil {
			return err
		}
		return s.webPush.Send(ctx, token.Token, payload)
	default:
		return errUnsupportedPushType
	}
}
