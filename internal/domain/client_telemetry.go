package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

const (
	MaxClientTelemetrySubjects      = 100
	MaxClientTelemetryPayloadBytes  = 64 << 10
	MaxClientTelemetryEventsPerHour = 1000
	MaxClientTelemetryEventsPerDay  = 10000
)

var (
	ErrClientTelemetryInvalid     = errors.New("client telemetry invalid")
	ErrClientTelemetryRateLimited = errors.New("client telemetry rate limited")
)

type ClientTelemetryKind string

const (
	ClientTelemetryMessageDelivery ClientTelemetryKind = "message_delivery"
	ClientTelemetryReadMetrics     ClientTelemetryKind = "read_metrics"
	ClientTelemetryMusicListen     ClientTelemetryKind = "music_listen"
)

func (k ClientTelemetryKind) Valid() bool {
	switch k {
	case ClientTelemetryMessageDelivery, ClientTelemetryReadMetrics,
		ClientTelemetryMusicListen:
		return true
	default:
		return false
	}
}

// ClientTelemetryEvent is operational product telemetry. It is deliberately
// isolated from moderation reports/cases and has TTL-based retention.
type ClientTelemetryEvent struct {
	ID          int64
	UserID      int64
	Kind        ClientTelemetryKind
	Peer        Peer
	SubjectIDs  []int64
	Payload     json.RawMessage
	Fingerprint [sha256.Size]byte
	CreatedAt   time.Time
}

func NewClientTelemetryEvent(userID int64, kind ClientTelemetryKind, peer Peer, subjectIDs []int64, payload any, createdAt time.Time) (ClientTelemetryEvent, error) {
	canonicalIDs := append([]int64(nil), subjectIDs...)
	sort.Slice(canonicalIDs, func(i, j int) bool { return canonicalIDs[i] < canonicalIDs[j] })
	for i, id := range canonicalIDs {
		if id <= 0 || (i > 0 && canonicalIDs[i-1] == id) {
			return ClientTelemetryEvent{}, ErrClientTelemetryInvalid
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ClientTelemetryEvent{}, ErrClientTelemetryInvalid
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return ClientTelemetryEvent{}, ErrClientTelemetryInvalid
	}
	raw, err = json.Marshal(object)
	if err != nil || len(raw) > MaxClientTelemetryPayloadBytes {
		return ClientTelemetryEvent{}, ErrClientTelemetryInvalid
	}
	event := ClientTelemetryEvent{
		UserID: userID, Kind: kind, Peer: peer,
		SubjectIDs: canonicalIDs, Payload: raw, CreatedAt: createdAt.UTC(),
	}
	fingerprintInput, err := json.Marshal(struct {
		Version    int
		UserID     int64
		Kind       ClientTelemetryKind
		Peer       Peer
		SubjectIDs []int64
		Payload    json.RawMessage
		Minute     int64
	}{
		Version: 1, UserID: event.UserID, Kind: event.Kind, Peer: event.Peer,
		SubjectIDs: event.SubjectIDs, Payload: event.Payload,
		Minute: event.CreatedAt.Truncate(time.Minute).Unix(),
	})
	if err != nil {
		return ClientTelemetryEvent{}, ErrClientTelemetryInvalid
	}
	event.Fingerprint = sha256.Sum256(fingerprintInput)
	if err := event.Validate(); err != nil {
		return ClientTelemetryEvent{}, err
	}
	return event, nil
}

func (e ClientTelemetryEvent) Validate() error {
	if e.ID < 0 || e.UserID <= 0 || !e.Kind.Valid() ||
		len(e.SubjectIDs) == 0 ||
		len(e.SubjectIDs) > MaxClientTelemetrySubjects ||
		len(e.Payload) == 0 || len(e.Payload) > MaxClientTelemetryPayloadBytes ||
		e.Fingerprint == ([sha256.Size]byte{}) || e.CreatedAt.IsZero() {
		return ErrClientTelemetryInvalid
	}
	if e.Peer.ID == 0 {
		if e.Peer.Type != "" || e.Kind != ClientTelemetryMusicListen {
			return ErrClientTelemetryInvalid
		}
	} else if !moderationPeerValid(e.Peer) {
		return ErrClientTelemetryInvalid
	}
	for i, id := range e.SubjectIDs {
		if id <= 0 || (i > 0 && e.SubjectIDs[i-1] >= id) {
			return ErrClientTelemetryInvalid
		}
	}
	var object map[string]any
	if err := json.Unmarshal(e.Payload, &object); err != nil || object == nil {
		return ErrClientTelemetryInvalid
	}
	canonical, err := json.Marshal(object)
	if err != nil || !bytes.Equal(canonical, e.Payload) {
		return ErrClientTelemetryInvalid
	}
	return nil
}
