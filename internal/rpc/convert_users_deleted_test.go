package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func TestDeletedUserTLProjectionContainsOnlyTombstoneIdentity(t *testing.T) {
	u := domain.User{
		ID: 42, AccessHash: 99, Phone: "secret", FirstName: "Alice", LastName: "Private",
		Username: "released", About: "hidden", Verified: true, PremiumUntil: 2_000_000_000,
		PhotoID: 123, Deleted: true, DeletedAt: 1_800_000_000,
	}
	got := tgUser(u)
	if got.ID != u.ID || !got.Deleted {
		t.Fatalf("deleted user = %+v", got)
	}
	if got.AccessHash != 0 || got.Phone != "" || got.FirstName != "" || got.LastName != "" || got.Username != "" || got.Verified || got.Premium || got.Photo != nil || got.Status != nil || len(got.Usernames) != 0 {
		t.Fatalf("deleted user leaked profile state: %+v", got)
	}
	self := tgSelfUser(u)
	if !self.Deleted || self.Self || self.ID != u.ID {
		t.Fatalf("deleted self projection = %+v", self)
	}
}

func TestHistoryHydrationReplacesStaleUserWithDeletedTombstone(t *testing.T) {
	viewer := domain.User{ID: 7, FirstName: "Viewer"}
	deleted := domain.User{ID: 42, AccessHash: 99, Deleted: true, DeletedAt: 1_800_000_000}
	r := New(Config{}, Deps{Users: mapUsersService{users: map[int64]domain.User{
		viewer.ID: viewer, deleted.ID: deleted,
	}}}, zaptest.NewLogger(t), clock.System)

	list := r.enrichMessageList(context.Background(), viewer.ID, domain.MessageList{
		Messages: []domain.Message{{
			OwnerUserID: viewer.ID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: deleted.ID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: deleted.ID},
			Body:        "retained history",
		}},
		// Simulate an old denormalized message query row. The authoritative
		// Users.ByIDs hydration must replace it, not keep an empty active user.
		Users: []domain.User{{ID: deleted.ID, Phone: "stale", FirstName: "Stale"}},
	})
	if len(list.Users) != 1 || !list.Users[0].Deleted || list.Users[0].Phone != "" || list.Users[0].FirstName != "" {
		t.Fatalf("history users = %+v, want authoritative tombstone", list.Users)
	}
	if got := tgUser(list.Users[0]); !got.Deleted || got.ID != deleted.ID {
		t.Fatalf("history TL user = %+v", got)
	}
}
