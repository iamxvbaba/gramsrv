package domain

import (
	"errors"
	"time"
)

// ErrWidgetNotFound is returned by a widget store when the requested shared
// widget id does not exist.
var ErrWidgetNotFound = errors.New("widget not found")

// MessageWidget is the persisted snapshot of a bot web widget attached to a
// message (TL messageMediaWidget). Width/Height/HTML/Payload describe the
// rendered widget; PublicData/ChatData are the mutable data payloads edited via
// messages.editMessageWidgetData / messages.editWidgetData. Payload is opaque
// client data (base64 through the message media JSONB column).
type MessageWidget struct {
	// WidgetID references a shared widget definition (bot_widgets). Zero for a
	// one-off inline widget that only carries HTML/Payload on the message.
	WidgetID int64 `json:"widget_id,omitempty"`
	// AllowQueries mirrors inputMediaWidget.allow_queries: the widget may send
	// queries to its bot.
	AllowQueries bool `json:"allow_queries,omitempty"`
	// SharedWidget marks a widget definition that may be referenced by
	// inputMediaWidgetID from other messages.
	SharedWidget bool `json:"shared_widget,omitempty"`
	// IsCopy marks a widget message created via inputMediaWidgetID.
	IsCopy bool `json:"is_copy,omitempty"`
	// CanQuery mirrors messageMediaWidget.can_query.
	CanQuery bool `json:"can_query,omitempty"`
	// BackgroundQuery asks the client to run the query in the background.
	BackgroundQuery bool `json:"background_query,omitempty"`
	Width           int  `json:"width,omitempty"`
	Height          int  `json:"height,omitempty"`
	// HTML is the widget markup stored for widgets defined inline. It is never
	// re-exposed on the message (messageMediaWidget carries payload instead).
	HTML    string `json:"html,omitempty"`
	Payload []byte `json:"payload,omitempty"`
	// PublicData/PublicDataHash are the per-message public data snapshot.
	PublicData     string `json:"public_data,omitempty"`
	PublicDataHash int64  `json:"public_data_hash,omitempty"`
	// ChatData is the per-chat data snapshot for this message.
	ChatData string `json:"chat_data,omitempty"`
}

// IsZero reports whether the widget snapshot carries no usable definition.
func (w *MessageWidget) IsZero() bool {
	return w == nil || (w.WidgetID == 0 && w.HTML == "" && len(w.Payload) == 0)
}

// BotWidget is the persisted definition and mutable public data of a shared
// widget (bot_widgets). The WidgetID is server-assigned and referenced by
// inputMediaWidgetID and messageMediaWidget.
type BotWidget struct {
	WidgetID       int64
	BotUserID      int64
	HTML           string
	Payload        []byte
	Width          int
	Height         int
	AllowQueries   bool
	PublicData     string
	PublicDataHash int64
	CacheTime      int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// BotWidgetQueryAnswer is the asynchronous answer a bot returns from
// messages.setBotWidgetQueryAnswer.
type BotWidgetQueryAnswer struct {
	Result string
}
