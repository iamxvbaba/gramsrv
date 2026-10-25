package redisstore

import (
	"encoding/json"
	"testing"

	"telesrv/internal/domain"
)

// A tombstone has to survive the base-user cache round trip. Without the flag the
// cache hands a deleted account back as a live one: empty name, no deleted bit,
// LastSeenAt zero -- which is exactly what a client renders as a stranger who was
// "last seen recently" instead of Deleted Account.
func TestUserBaseValueRoundTripsDeleted(t *testing.T) {
	tombstone := domain.User{ID: 42, Deleted: true, DeletedAt: 1_800_000_000}

	raw, err := json.Marshal(baseValueFromUser(tombstone))
	if err != nil {
		t.Fatalf("marshal tombstone: %v", err)
	}
	var decoded userBaseValue
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal tombstone: %v", err)
	}
	got := decoded.user()
	if !got.Deleted {
		t.Fatalf("cached tombstone lost the deleted flag: %+v (json %s)", got, raw)
	}
	if got.ID != tombstone.ID {
		t.Fatalf("cached tombstone id = %d, want %d", got.ID, tombstone.ID)
	}
	// The projection collapses every identifying field, so a cache hit that
	// returned one of these would be publishing a ghost with real identity.
	if got.FirstName != "" || got.LastName != "" || got.Username != "" || got.Phone != "" {
		t.Fatalf("cached tombstone kept identity fields: %+v", got)
	}
}

func TestUserBaseValueRoundTripsLiveUser(t *testing.T) {
	live := domain.User{ID: 43, FirstName: "Alice", LastSeenAt: 99}
	raw, err := json.Marshal(baseValueFromUser(live))
	if err != nil {
		t.Fatalf("marshal live user: %v", err)
	}
	var decoded userBaseValue
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal live user: %v", err)
	}
	if got := decoded.user(); got.Deleted || got.FirstName != "Alice" || got.LastSeenAt != 99 {
		t.Fatalf("live user round trip = %+v", got)
	}
}
