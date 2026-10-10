package rpc

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appstories "telesrv/internal/app/stories"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// isEmptyUserStatus reports whether the tombstone status is the explicit empty
// one. A missing status field is not enough: clients keep the cached presence
// when the flag is absent, which is how a deleted account kept showing
// "last seen recently" after it had already appeared as deleted.
func isEmptyUserStatus(status tg.UserStatusClass) bool {
	_, ok := status.(*tg.UserStatusEmpty)
	return ok
}

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
	if got.AccessHash != 0 || got.Phone != "" || got.FirstName != "Deleted Account" || got.LastName != "" || got.Username != "" || got.Verified || got.Premium || !isEmptyUserStatus(got.Status) || len(got.Usernames) != 0 {
		t.Fatalf("deleted user leaked profile state: %+v", got)
	}
	// The ghost avatar is the one field a tombstone still carries: an explicit
	// userProfilePhotoEmpty, never the account's real photo id.
	if _, ok := got.Photo.(*tg.UserProfilePhotoEmpty); !ok {
		t.Fatalf("deleted user photo = %+v, want userProfilePhotoEmpty", got.Photo)
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

func TestDeletedUserReadModelOverlaysRemainMinimal(t *testing.T) {
	ctx := context.Background()
	viewerID := int64(7)
	deletedID := int64(42)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: deletedID}
	now := int(time.Now().Unix())

	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner: peer, ID: 1, Date: now, ExpireDate: now + 3600, Public: true,
	}}); err != nil {
		t.Fatalf("upsert retained story: %v", err)
	}
	stories := &countingStoriesService{StoriesService: appstories.NewService(storyStore)}
	usernames := newFakeUsernameRegistry()
	usernames.byPeer[peer] = []domain.Username{{Username: "retained_collectible", Active: true, CollectibleID: 9}}
	verifications := newFakeBotVerifications()
	verifications.marks[peer] = domain.CustomVerification{
		VerifierBotID: 9001, Peer: peer, IconDocumentID: 9002, Description: "retained mark",
	}
	r := New(Config{}, Deps{
		Stories: stories, Usernames: usernames, BotVerifications: verifications,
	}, zaptest.NewLogger(t), clock.System)

	users := []tg.UserClass{tgUser(domain.User{ID: deletedID, Deleted: true, DeletedAt: int64(now)})}
	r.applyPeerReadModels(ctx, viewerID, users, nil)
	u := users[0].(*tg.User)
	_, usernamesSet := u.GetUsernames()
	_, storiesSet := u.GetStoriesMaxID()
	_, verificationSet := u.GetBotVerificationIcon()
	if !u.Deleted || u.ID != deletedID || usernamesSet || storiesSet || u.GetStoriesHidden() || verificationSet {
		t.Fatalf("deleted user gained retained read-model overlays: %+v", u)
	}
	if stories.projectionCalls != 0 || usernames.peerCalls != 0 || usernames.batchCalls != 0 || verifications.peerCalls != 0 || verifications.batchCalls != 0 {
		t.Fatalf("deleted user triggered overlay reads: stories=%d usernames=(%d,%d) verifications=(%d,%d)",
			stories.projectionCalls, usernames.peerCalls, usernames.batchCalls, verifications.peerCalls, verifications.batchCalls)
	}
}

func TestGetFullDeletedUserReturnsOnlyTombstone(t *testing.T) {
	ctx := context.Background()
	viewer := domain.User{ID: 7, FirstName: "Viewer"}
	deleted := domain.User{
		ID: 42, AccessHash: 99, Phone: "secret", FirstName: "Alice", LastName: "Private",
		Username: "released", About: "retained about", PhotoID: 123, Deleted: true,
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: deleted.ID}
	now := int(time.Now().Unix())

	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner: peer, ID: 1, Date: now, ExpireDate: now + 3600, Public: true,
	}}); err != nil {
		t.Fatalf("upsert retained story: %v", err)
	}
	stories := &countingStoriesService{StoriesService: appstories.NewService(storyStore)}
	usernames := newFakeUsernameRegistry()
	usernames.byPeer[peer] = []domain.Username{{Username: "retained_collectible", Active: true, CollectibleID: 9}}
	verifications := newFakeBotVerifications()
	verifications.marks[peer] = domain.CustomVerification{
		VerifierBotID: 9001, Peer: peer, IconDocumentID: 9002, Description: "retained mark",
	}
	r := New(Config{}, Deps{
		Users: mapUsersService{users: map[int64]domain.User{
			viewer.ID:  viewer,
			deleted.ID: deleted,
		}},
		Stories: stories, Usernames: usernames, BotVerifications: verifications,
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onUsersGetFullUser(WithUserID(ctx, viewer.ID), &tg.InputUser{
		UserID: deleted.ID, AccessHash: deleted.AccessHash,
	})
	if err != nil {
		t.Fatalf("get full deleted user: %v", err)
	}
	wantFull := tg.UserFull{
		ID:             deleted.ID,
		Settings:       tg.PeerSettings{},
		NotifySettings: *tdesktop.NotifySettings(),
	}
	if !reflect.DeepEqual(got.FullUser, wantFull) {
		t.Fatalf("full deleted user = %+v, want minimal %+v", got.FullUser, wantFull)
	}
	if len(got.Users) != 1 {
		t.Fatalf("users = %+v, want one tombstone", got.Users)
	}
	u, ok := got.Users[0].(*tg.User)
	want := &tg.User{ID: deleted.ID, Deleted: true, FirstName: deletedAccountDisplayName,
		Photo: &tg.UserProfilePhotoEmpty{}, Status: &tg.UserStatusEmpty{}}
	if !ok || !reflect.DeepEqual(u, want) {
		t.Fatalf("deleted user envelope = %+v", got.Users[0])
	}
	if len(got.Chats) != 0 {
		t.Fatalf("deleted user chats = %+v, want none", got.Chats)
	}
	if stories.projectionCalls != 0 || usernames.peerCalls != 0 || usernames.batchCalls != 0 || verifications.peerCalls != 0 || verifications.batchCalls != 0 {
		t.Fatalf("deleted getFullUser triggered retained read-model reads: stories=%d usernames=(%d,%d) verifications=(%d,%d)",
			stories.projectionCalls, usernames.peerCalls, usernames.batchCalls, verifications.peerCalls, verifications.batchCalls)
	}
	wire := &tg.UsersUserFull{}
	tlRoundTrip(t, got, wire)
	if !reflect.DeepEqual(wire.FullUser, wantFull) || len(wire.Users) != 1 {
		t.Fatalf("wire deleted user full = %+v", wire)
	}
}
