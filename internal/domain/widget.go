package domain

import (
	"errors"
	"hash/fnv"
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
	BackgroundQuery bool   `json:"background_query,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	Payload         []byte `json:"payload,omitempty"`
	// PublicData/PublicDataHash are the per-message public data snapshot.
	PublicData     string `json:"public_data,omitempty"`
	PublicDataHash int64  `json:"public_data_hash,omitempty"`
	// ChatData is the per-chat data snapshot for this message.
	ChatData string `json:"chat_data,omitempty"`
}

// IsZero reports whether the widget snapshot carries no usable definition.
func (w *MessageWidget) IsZero() bool {
	return w == nil || (w.WidgetID == 0 && len(w.Payload) == 0)
}

// WidgetDataHash is the server-computed public_data hash
// (messageMediaWidget.public_data_hash). Clients echo it back to
// messages.getWidgetPublicData and get widgetPublicDataNotModified on a match.
func WidgetDataHash(data string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(data))
	return int64(h.Sum64())
}

// ApplyWidgetDataPatch mutates a widget media snapshot with a public/chat data
// patch and reports whether anything changed. It returns false when the media is
// not a widget or the requested fields already hold the given values.
func ApplyWidgetDataPatch(media *MessageMedia, patch *MessageWidgetDataPatch) bool {
	if media == nil || patch == nil || media.Widget == nil {
		return false
	}
	widget := media.Widget
	changed := false
	if patch.PublicData != nil && widget.PublicData != *patch.PublicData {
		widget.PublicData = *patch.PublicData
		widget.PublicDataHash = WidgetDataHash(*patch.PublicData)
		changed = true
	}
	if patch.ChatData != nil && widget.ChatData != *patch.ChatData {
		widget.ChatData = *patch.ChatData
		changed = true
	}
	return changed
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
