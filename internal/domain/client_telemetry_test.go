package domain

import (
	"errors"
	"testing"
	"time"
)

func TestNewClientTelemetryEventCanonicalizesSubjectsAndMinuteIdempotency(t *testing.T) {
	now := time.Unix(1_750_000_000, 0).UTC().Truncate(time.Minute).Add(time.Second)
	peer := Peer{Type: PeerTypeUser, ID: 22}
	first, err := NewClientTelemetryEvent(
		11, ClientTelemetryMessageDelivery, peer, []int64{3, 1, 2},
		map[string]any{"push": true}, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := NewClientTelemetryEvent(
		11, ClientTelemetryMessageDelivery, peer, []int64{2, 3, 1},
		struct {
			Push bool `json:"push"`
		}{Push: true},
		now.Add(30*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := first.SubjectIDs; len(got) != 3 ||
		got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("canonical subjects = %v", got)
	}
	if first.Fingerprint != retry.Fingerprint {
		t.Fatal("same telemetry inside one minute must have one fingerprint")
	}
	nextMinute, err := NewClientTelemetryEvent(
		11, ClientTelemetryMessageDelivery, peer, []int64{1, 2, 3},
		map[string]any{"push": true}, now.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if nextMinute.Fingerprint == first.Fingerprint {
		t.Fatal("a new minute bucket must produce a new fingerprint")
	}
}

func TestNewClientTelemetryEventRejectsDuplicateSubjectsAndInvalidPeer(t *testing.T) {
	now := time.Now().UTC()
	_, err := NewClientTelemetryEvent(
		11, ClientTelemetryReadMetrics,
		Peer{Type: PeerTypeUser, ID: 22},
		[]int64{1, 1}, map[string]any{"metrics": []int{1}}, now,
	)
	if !errors.Is(err, ErrClientTelemetryInvalid) {
		t.Fatalf("duplicate subjects err=%v", err)
	}
	_, err = NewClientTelemetryEvent(
		11, ClientTelemetryMessageDelivery, Peer{},
		[]int64{1}, map[string]any{"push": true}, now,
	)
	if !errors.Is(err, ErrClientTelemetryInvalid) {
		t.Fatalf("missing message peer err=%v", err)
	}
}
