package domain

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"math"
	"time"
)

// ErrWidgetNotFound is returned by a widget store when the requested shared
// widget id does not exist.
var ErrWidgetNotFound = errors.New("widget not found")

// RandomWidgetID returns an unpredictable non-zero shared-widget id. Ids are
// referenced by inputMediaWidgetID without an access hash, so a sequential id
// would let anyone enumerate every shared widget.
func RandomWidgetID() int64 {
	for {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			// crypto/rand does not fail in practice; degrade to a time-seeded id
			// rather than refusing to create a widget.
			if id := time.Now().UnixNano() | 1; id != 0 {
				return id
			}
			continue
		}
		if id := int64(binary.LittleEndian.Uint64(buf[:]) & math.MaxInt64); id != 0 {
			return id
		}
	}
}

// MessageWidget is the persisted snapshot of a bot web widget attached to a
// message (TL messageMediaWidget). Width/Height/HTML/Payload describe the
// rendered widget; PublicData/ChatData are the mutable data payloads edited via
// messages.editMessageWidgetData / messages.editWidgetData. Payload is opaque
// client data (base64 through the message media JSONB column).
type MessageWidget struct {
	// WidgetID references a shared widget definition (bot_widgets). Zero for a
	// one-off inline widget that only carries HTML/Payload on the message.
	WidgetID int64 `json:"widget_id,omitempty"`
	// OwnerBotID is the bot that owns the shared definition (bot_widgets.bot_user_id).
	// It is carried on the media so the row can be created in the committed send
	// transaction instead of while the input is parsed. It is never projected to
	// clients.
	OwnerBotID int64 `json:"owner_bot_id,omitempty"`
	// AllowQueries mirrors inputMediaWidget.allow_queries: the widget may send
	// queries to its bot.
	AllowQueries bool `json:"allow_queries,omitempty"`
	// SharedWidget marks a widget definition that may be referenced by
	// inputMediaWidgetID from other messages.
	SharedWidget bool `json:"shared_widget,omitempty"`
	// IsCopy marks a widget message created via inputMediaWidgetID.
	IsCopy bool `json:"is_copy,omitempty"`
	// CanQuery mirrors messageMediaWidget.can_query.
	CanQuery bool   `json:"can_query,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Payload  []byte `json:"payload,omitempty"`
	// PublicData/PublicDataHash are the per-message public data snapshot.
	PublicData     string `json:"public_data,omitempty"`
	PublicDataHash int64  `json:"public_data_hash,omitempty"`
	// PublicDataSet distinguishes "no per-message public data" (fall back to the
	// shared definition) from an explicit override, including an explicit clear
	// to "". Without it, editWidgetData is invisible and "" cannot be cleared.
	PublicDataSet bool `json:"public_data_set,omitempty"`
	// ChatData is the per-chat data snapshot for this message.
	ChatData string `json:"chat_data,omitempty"`
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
	if patch.PublicData != nil && (!widget.PublicDataSet || widget.PublicData != *patch.PublicData) {
		widget.PublicData = *patch.PublicData
		widget.PublicDataHash = WidgetDataHash(*patch.PublicData)
		widget.PublicDataSet = true
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
