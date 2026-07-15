package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appstories "telesrv/internal/app/stories"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestPhotosGetUserPhotosPreservesMaxIDRefreshOffset(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	user, err := userStore.Create(ctx, domain.User{AccessHash: 21, Phone: "15550002101", FirstName: "Photo"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	files := &fakeFiles{
		profilePhotos:      []domain.Photo{{ID: 9100000000000000301, AccessHash: 9, DCID: 2}},
		profilePhotosTotal: 1,
	}
	r := New(Config{}, Deps{
		Users: appusers.NewService(userStore),
		Files: files,
	}, zaptest.NewLogger(t), clock.System)

	got, err := r.onPhotosGetUserPhotos(WithUserID(ctx, user.ID), &tg.PhotosGetUserPhotosRequest{
		UserID: &tg.InputUser{UserID: user.ID, AccessHash: user.AccessHash},
		Offset: -1,
		MaxID:  9100000000000000301,
		Limit:  1,
	})
	if err != nil {
		t.Fatalf("get user photos: %v", err)
	}
	if _, ok := got.(*tg.PhotosPhotos); !ok {
		t.Fatalf("get user photos = %T, want *tg.PhotosPhotos", got)
	}
	if files.lastProfileOffset != -1 || files.lastProfileMaxID != 9100000000000000301 || files.lastProfileLimit != 1 {
		t.Fatalf("profile photo args offset=%d max_id=%d limit=%d, want -1/exact/1", files.lastProfileOffset, files.lastProfileMaxID, files.lastProfileLimit)
	}
}

func TestPhotosGetUserPhotosProjectsStoryPeerFlags(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	target, err := userStore.Create(ctx, domain.User{AccessHash: 22, Phone: "15550002102", FirstName: "StoryTarget"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 23, Phone: "15550002103", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	storyStore := memory.NewStoryStore()
	if _, err := storyStore.UpsertStory(ctx, domain.UpsertStoryRequest{Story: domain.Story{
		Owner:      domain.Peer{Type: domain.PeerTypeUser, ID: target.ID},
		ID:         4,
		Date:       1700000000,
		ExpireDate: 1700003600,
		Public:     true,
	}}); err != nil {
		t.Fatalf("upsert story: %v", err)
	}
	files := &fakeFiles{profilePhotosTotal: 0}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Stories: appstories.NewService(storyStore),
		Files:   files,
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000100, 0)})
	reqCtx := WithUserID(ctx, viewer.ID)
	if ok, err := r.onStoriesTogglePeerStoriesHidden(reqCtx, &tg.StoriesTogglePeerStoriesHiddenRequest{
		Peer:   &tg.InputPeerUser{UserID: target.ID, AccessHash: target.AccessHash},
		Hidden: true,
	}); err != nil || !ok {
		t.Fatalf("hide target stories = %v, %v", ok, err)
	}

	got, err := r.onPhotosGetUserPhotos(reqCtx, &tg.PhotosGetUserPhotosRequest{
		UserID: &tg.InputUser{UserID: target.ID, AccessHash: target.AccessHash},
		Limit:  1,
	})
	if err != nil {
		t.Fatalf("get user photos: %v", err)
	}
	photos, ok := got.(*tg.PhotosPhotos)
	if !ok {
		t.Fatalf("get user photos = %T, want *tg.PhotosPhotos", got)
	}
	if len(photos.Users) != 1 {
		t.Fatalf("photos users = %d, want target user", len(photos.Users))
	}
	user, ok := photos.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("photos user = %T, want *tg.User", photos.Users[0])
	}
	recent, ok := user.GetStoriesMaxID()
	maxID, hasMaxID := recent.GetMaxID()
	if !ok || !hasMaxID || maxID != 4 {
		t.Fatalf("photos user stories_max_id = %+v ok=%v hasMaxID=%v, want max_id 4", recent, ok, hasMaxID)
	}
	if !user.StoriesHidden {
		t.Fatalf("photos user stories_hidden = false, want true")
	}
}
