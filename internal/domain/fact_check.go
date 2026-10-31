package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strings"
)

// FactCheck is a fact-check tag (factCheck). The constructor together with messages.editFactCheck /
// messages.deleteFactCheck / messages.getFactCheck has existed in the protocol since Layer 225;
// it merely had no server implementation before.
//
// The client offers an edit entry only on media messages in a broadcast channel, and whether that entry
// appears is decided by the app config's global can_edit_factcheck switch; whether a write is allowed is adjudicated
// server-side per channel permission. The tag hangs on the message itself, so the same message is the same fact from every subscriber's view.
type FactCheck struct {
	// NeedCheck means the message has not been checked yet (the client renders a "pending check" state).
	NeedCheck bool
	// Country is the country of the fact-checking organization, ISO 3166-1 alpha-2; empty when unknown.
	Country string
	// Text is the verdict text.
	Text string
	// Entities are the rich-text entities of Text (text_url/mention/...).
	Entities []MessageEntity
	// Hash is a stable identifier: the same full verdict on the same message always gets the same hash,
	// which the client uses to tell "has this fact check been changed".
	Hash int64
}

// MaxFactCheckTextLength is the character limit of the verdict text, matching the client's
// default factcheck_length_limit.
const MaxFactCheckTextLength = 1024

// ValidateFactCheck checks whether a fact-check tag can be persisted.
// An empty text means deletion and is not allowed as a save action.
func ValidateFactCheck(check FactCheck) error {
	if strings.TrimSpace(check.Text) == "" {
		return ErrFactCheckInvalid
	}
	if len([]rune(check.Text)) > MaxFactCheckTextLength {
		return ErrFactCheckTooLong
	}
	if check.Country != "" && !isAlpha2Country(check.Country) {
		return ErrFactCheckInvalid
	}
	return nil
}

func isAlpha2Country(country string) bool {
	if len(country) != 2 {
		return false
	}
	for _, r := range country {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// FactCheckHash covers every visible field, including link targets and formatting.
// Only fixed primitive fields are encoded; Hash itself is deliberately excluded.
func FactCheckHash(channelID int64, messageID int, check FactCheck) int64 {
	entities := append([]MessageEntity{}, check.Entities...)
	payload, _ := json.Marshal(struct {
		ChannelID int64
		MessageID int
		NeedCheck bool
		Country   string
		Text      string
		Entities  []MessageEntity
	}{channelID, messageID, check.NeedCheck, check.Country, check.Text, entities})
	sum := sha256.Sum256(payload)
	hash := int64(binary.BigEndian.Uint64(sum[:8]) &^ (1 << 63))
	if hash == 0 {
		hash = 1
	}
	return hash
}

func FactChecksEqual(a, b *FactCheck) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Text != b.Text || a.Country != b.Country || a.NeedCheck != b.NeedCheck || len(a.Entities) != len(b.Entities) {
		return false
	}
	for i := range a.Entities {
		if a.Entities[i] != b.Entities[i] {
			return false
		}
	}
	return true
}

// EditFactCheckRequest is the business request to write one fact check.
type EditFactCheckRequest struct {
	UserID    int64
	ChannelID int64
	MessageID int
	// An empty Text means delete the fact check.
	Text      string
	Entities  []MessageEntity
	NeedCheck bool
	Country   string
	Date      int
}

// EditFactCheckResult returns the message snapshot and tag after the write.
type EditFactCheckResult struct {
	Channel    Channel
	Message    ChannelMessage
	Event      ChannelUpdateEvent
	FactCheck  *FactCheck
	Recipients []int64
}

// FactCheckList is the result set of messages.getFactCheck, in request order.
type FactCheckList struct {
	FactChecks []FactCheck
}

// FactChecksForMessages projects an already-authorized message batch without
// shifting positions when a message is missing, deleted or has no verdict.
func FactChecksForMessages(messages []ChannelMessage, ids []int) FactCheckList {
	byID := make(map[int]*FactCheck, len(messages))
	for _, msg := range messages {
		if !msg.Deleted && msg.Action == nil && msg.FactCheck != nil {
			byID[msg.ID] = msg.FactCheck
		}
	}
	out := FactCheckList{FactChecks: make([]FactCheck, len(ids))}
	for i, id := range ids {
		if check := byID[id]; check != nil {
			out.FactChecks[i] = *check
			out.FactChecks[i].Entities = append([]MessageEntity(nil), check.Entities...)
		}
	}
	return out
}
