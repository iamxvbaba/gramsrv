package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

type deletedFlagUsers struct {
	UsersService
	deleted map[int64]bool
	calls   int
}

func (s *deletedFlagUsers) ByID(_ context.Context, _, userID int64) (domain.User, bool, error) {
	s.calls++
	if s.deleted[userID] {
		return domain.User{ID: userID, Deleted: true, DeletedAt: 1_800_000_000}, true, nil
	}
	return domain.User{ID: userID, FirstName: "Live"}, true, nil
}

// A deleted peer must never render presence. It has no last-seen, so every
// branch that would fall back to the tracker or LastSeenAt has to collapse to the
// explicit empty status -- otherwise the account reads as recently online next to
// its own deleted presentation.
func TestDeletedAccountNeverRendersPresence(t *testing.T) {
	users := &deletedFlagUsers{deleted: map[int64]bool{42: true}}
	r := New(Config{}, Deps{Users: users}, zaptest.NewLogger(t), clock.System)

	// A tombstone whose status projection has not collapsed yet still carries the
	// row's LastSeenAt. That is exactly the shape that used to leak
	// "last seen recently" next to the deleted presentation.
	got := r.userPresenceStatusForUser(domain.User{ID: 42, Deleted: true, LastSeenAt: 1_800_000_000})
	if got.Kind != domain.UserStatusEmpty || got.WasOnline != 0 {
		t.Fatalf("deleted presence = %+v, want empty with no timestamp", got)
	}

	live := r.userPresenceStatusForUser(domain.User{ID: 43, LastSeenAt: 1_800_000_000})
	if live.Kind == domain.UserStatusEmpty {
		t.Fatalf("live peer must keep its presence, got %+v", live)
	}
	if !r.isDeletedAccount(42) {
		t.Fatal("isDeletedAccount must recognise the tombstone row")
	}
	if r.isDeletedAccount(43) {
		t.Fatal("isDeletedAccount must not fire for a live peer")
	}
}

// The tombstone itself has to carry an explicit empty status, not just no field:
// a missing field leaves the client showing its cached last-seen.
func TestDeletedTombstoneCarriesExplicitEmptyStatus(t *testing.T) {
	out := tgUser(domain.User{ID: 42, Deleted: true, LastSeenAt: 1_800_000_000})
	if !out.Deleted {
		t.Fatalf("tombstone lost the deleted flag: %+v", out)
	}
	if !isEmptyUserStatus(out.Status) {
		t.Fatalf("tombstone status = %+v, want userStatusEmpty", out.Status)
	}
}
