package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestUploadProfilePhotoPushesUpdateToOtherDevices 守护审计修复：换头像后必须向该账号其它在线
// 设备推送（updateUser 信号 + Updates.Users 带新 self user），否则其它设备头像不刷新。
// 原因：updateUserName 不含 photo 无法刷新头像，唯有带 user 对象最可靠（见 photos.go
// pushSelfPhotoUpdate 注释）。曾完全不推送，本测试防回归。
func TestUploadProfilePhotoPushesUpdateToOtherDevices(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001001", FirstName: "Owner"})
	sessions := &captureSessions{}
	files := &fakeFiles{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Files:    files,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	req := &tg.PhotosUploadProfilePhotoRequest{}
	req.SetFile(&tg.InputFile{ID: 42, Parts: 1, Name: "a.jpg"}) // File 是 flags 可选字段，须 SetFile 置位
	got, err := r.onPhotosUploadProfilePhoto(WithUserID(ctx, owner.ID), req)
	if err != nil {
		t.Fatalf("uploadProfilePhoto: %v", err)
	}
	returnedPhoto, ok := firstPhotosUser(t, got).Photo.(*tg.UserProfilePhoto)
	if !ok || returnedPhoto.PhotoID != 778 || returnedPhoto.DCID != 2 {
		t.Fatalf("returned self photo = %+v, want photo_id=778 dc_id=2", firstPhotosUser(t, got).Photo)
	}

	snap := sessions.snapshot()
	if snap.userID != owner.ID {
		t.Fatalf("push target user = %d, want %d", snap.userID, owner.ID)
	}
	updates, ok := snap.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", snap.message)
	}
	hasUserUpdate := false
	for _, u := range updates.Updates {
		if uu, ok := u.(*tg.UpdateUser); ok && uu.UserID == owner.ID {
			hasUserUpdate = true
		}
	}
	if !hasUserUpdate {
		t.Fatalf("updates = %+v, want UpdateUser for self", updates.Updates)
	}
	if len(updates.Users) == 0 {
		t.Fatal("pushed updates missing self user — other devices cannot refresh avatar")
	}
	pushedUser, ok := updates.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("pushed user = %T, want *tg.User", updates.Users[0])
	}
	pushedPhoto, ok := pushedUser.Photo.(*tg.UserProfilePhoto)
	if !ok || pushedPhoto.PhotoID != 778 || pushedPhoto.DCID != 2 {
		t.Fatalf("pushed self photo = %+v, want photo_id=778 dc_id=2", pushedUser.Photo)
	}
}

// TestUploadProfilePhotoEchoesUpdateToCurrentSession 守护 DrKLO Android emoji 头像回显修复：
// 换头像后除其它设备外，当前 session 也必须收到 updateUser + Updates.Users（带 has_video 的
// fresh self）。DrKLO 的 uploadProfilePhoto 响应回调不消费 photos.photo.users，手工重建的
// userProfilePhoto 丢 has_video/stripped，缺该回显时 emoji/sticker markup 头像在设置设备上
// 要重启才渲染（对齐参考实现 SyncPushUpdates 全 session 语义）。
func TestUploadProfilePhotoEchoesUpdateToCurrentSession(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001004", FirstName: "Owner"})
	sessions := &captureSessions{}
	files := &fakeFiles{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore, appusers.WithPhotoProvider(files)),
		Files:    files,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	r.selfPhotoEchoPushDelay = 0 // 测试同步回显

	const currentSessionID = int64(424242)
	reqCtx := WithSessionID(WithUserID(ctx, owner.ID), currentSessionID)
	req := &tg.PhotosUploadProfilePhotoRequest{}
	req.SetVideoEmojiMarkup(&tg.VideoSizeEmojiMarkup{EmojiID: 99, BackgroundColors: []int{0xffffff}})
	got, err := r.onPhotosUploadProfilePhoto(reqCtx, req)
	if err != nil {
		t.Fatalf("uploadProfilePhoto: %v", err)
	}
	returned, ok := got.Photo.(*tg.Photo)
	if !ok || len(returned.Sizes) < 1 {
		t.Fatalf("returned photo = %T %+v, want photo with s/a/c sizes", got.Photo, got.Photo)
	}
	transientSize, ok := returned.Sizes[0].(*tg.PhotoSize)
	if !ok || transientSize.Type != "s" || transientSize.W != 150 || transientSize.H != 150 {
		t.Fatalf("returned first size = %+v, want real s=150 transient response location", returned.Sizes[0])
	}

	// PushToSession 在 PushToUserExceptSession 之后调用，snapshot().message 即当前 session 回显。
	snap := sessions.snapshot()
	if snap.sessionID != currentSessionID {
		t.Fatalf("echo session = %d, want %d", snap.sessionID, currentSessionID)
	}
	echoed, ok := snap.message.(*tg.Updates)
	if !ok {
		t.Fatalf("echoed message = %T, want *tg.Updates", snap.message)
	}
	hasUserUpdate := false
	for _, u := range echoed.Updates {
		if uu, ok := u.(*tg.UpdateUser); ok && uu.UserID == owner.ID {
			hasUserUpdate = true
		}
	}
	if !hasUserUpdate {
		t.Fatalf("echoed updates = %+v, want UpdateUser for self", echoed.Updates)
	}
	if len(echoed.Users) == 0 {
		t.Fatal("echoed updates missing self user — current device cannot repair has_video")
	}
	echoedUser, ok := echoed.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("echoed user = %T, want *tg.User", echoed.Users[0])
	}
	echoedPhoto, ok := echoedUser.Photo.(*tg.UserProfilePhoto)
	if !ok || echoedPhoto.PhotoID != 780 || !echoedPhoto.HasVideo {
		t.Fatalf("echoed self photo = %+v, want photo 780 has_video=true", echoedUser.Photo)
	}
	// 其它设备推送同样发生（同一 updates）。
	if other, ok := sessions.lastUserPush().(*tg.Updates); !ok || len(other.Users) == 0 {
		t.Fatalf("other-device push = %T, want *tg.Updates with users", sessions.lastUserPush())
	}
}

// TestDeletePhotosPushesSelfUpdate 守护 deletePhotos 推送：删除生效（含撤掉当前头像回落）后
// 必须向全部在线 session 推 updateUser + fresh self（对齐参考实现 deletePhotos 的
// SyncPushUpdates），否则其它设备/当前设备头像停留旧状态。
func TestDeletePhotosPushesSelfUpdate(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001005", FirstName: "Owner"})
	sessions := &captureSessions{}
	files := &fakeFiles{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore, appusers.WithPhotoProvider(files)),
		Files:    files,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	r.selfPhotoEchoPushDelay = 0

	const currentSessionID = int64(434343)
	reqCtx := WithSessionID(WithUserID(ctx, owner.ID), currentSessionID)
	seedReq := &tg.PhotosUploadProfilePhotoRequest{}
	seedReq.SetFile(&tg.InputFile{ID: 42, Parts: 1, Name: "a.jpg"})
	if _, err := r.onPhotosUploadProfilePhoto(reqCtx, seedReq); err != nil {
		t.Fatalf("seed profile photo: %v", err)
	}
	sessions.clearMessages()

	got, err := r.onPhotosDeletePhotos(reqCtx, []tg.InputPhotoClass{&tg.InputPhoto{ID: 778}})
	if err != nil {
		t.Fatalf("deletePhotos: %v", err)
	}
	if len(got) != 1 || got[0] != 778 {
		t.Fatalf("deletePhotos result = %v, want [778]", got)
	}
	if _, found, err := files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, owner.ID, domain.ProfilePhotoKindProfile); err != nil || found {
		t.Fatalf("current profile photo after delete: found=%v err=%v, want removed", found, err)
	}
	pushed, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok {
		t.Fatalf("other-device push after delete = %T, want *tg.Updates", sessions.lastUserPush())
	}
	hasUserUpdate := false
	for _, u := range pushed.Updates {
		if uu, ok := u.(*tg.UpdateUser); ok && uu.UserID == owner.ID {
			hasUserUpdate = true
		}
	}
	if !hasUserUpdate || len(pushed.Users) == 0 {
		t.Fatalf("pushed updates after delete = %+v, want UpdateUser + self user", pushed)
	}
	pushedUser, ok := pushed.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("pushed user = %T, want *tg.User", pushed.Users[0])
	}
	if _, stillHasPhoto := pushedUser.Photo.(*tg.UserProfilePhoto); stillHasPhoto {
		t.Fatalf("pushed self photo after delete = %+v, want cleared", pushedUser.Photo)
	}
	// 当前 session 也收到回显（PushToSession 最后调用，覆盖 snapshot().message）。
	snap := sessions.snapshot()
	if snap.sessionID != currentSessionID {
		t.Fatalf("echo session = %d, want %d", snap.sessionID, currentSessionID)
	}
	if _, ok := snap.message.(*tg.Updates); !ok {
		t.Fatalf("echoed message = %T, want *tg.Updates", snap.message)
	}
}

func TestUploadProfilePhotoSupportsAnimatedVideoAndEmojiMarkup(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001002", FirstName: "Owner"})
	sessions := &captureSessions{}
	files := &fakeFiles{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore, appusers.WithPhotoProvider(files)),
		Files:    files,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	videoReq := &tg.PhotosUploadProfilePhotoRequest{}
	videoReq.SetVideo(&tg.InputFile{ID: 43, Parts: 1, Name: "avatar.mp4"})
	videoReq.SetVideoStartTs(0.25)
	videoGot, err := r.onPhotosUploadProfilePhoto(WithUserID(ctx, owner.ID), videoReq)
	if err != nil {
		t.Fatalf("upload animated profile video: %v", err)
	}
	videoPhoto, ok := videoGot.Photo.(*tg.Photo)
	if !ok || videoPhoto.ID != 779 || len(videoPhoto.VideoSizes) != 1 {
		t.Fatalf("video profile photo = %+v, want photo 779 with video_sizes", videoGot.Photo)
	}
	videoSize, ok := videoPhoto.VideoSizes[0].(*tg.VideoSize)
	if !ok || videoSize.Type != "u" || videoSize.VideoStartTs != 0.25 {
		t.Fatalf("video size = %+v, want videoSize u start 0.25", videoPhoto.VideoSizes[0])
	}
	selfPhoto := firstPhotosUser(t, videoGot).Photo
	userPhoto, ok := selfPhoto.(*tg.UserProfilePhoto)
	if !ok || userPhoto.PhotoID != 779 || userPhoto.DCID != 2 || !userPhoto.HasVideo {
		t.Fatalf("returned self photo = %+v, want photo 779 dc 2 has_video", selfPhoto)
	}
	if cur, ok, err := files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, owner.ID, domain.ProfilePhotoKindProfile); err != nil || !ok || cur.ID != 779 {
		t.Fatalf("current profile photo = %+v ok=%v err=%v, want 779", cur, ok, err)
	}

	markupReq := &tg.PhotosUploadProfilePhotoRequest{}
	markupReq.SetVideoEmojiMarkup(&tg.VideoSizeEmojiMarkup{EmojiID: 99, BackgroundColors: []int{0xffffff}})
	markupGot, err := r.onPhotosUploadProfilePhoto(WithUserID(ctx, owner.ID), markupReq)
	if err != nil {
		t.Fatalf("upload emoji profile photo: %v", err)
	}
	markupPhoto, ok := markupGot.Photo.(*tg.Photo)
	if !ok || markupPhoto.ID != 780 || len(markupPhoto.VideoSizes) != 1 {
		t.Fatalf("emoji profile photo = %+v, want photo 780 with video_sizes", markupGot.Photo)
	}
	if _, ok := markupPhoto.VideoSizes[0].(*tg.VideoSizeEmojiMarkup); !ok {
		t.Fatalf("emoji video size = %T, want *tg.VideoSizeEmojiMarkup", markupPhoto.VideoSizes[0])
	}
	selfPhoto = firstPhotosUser(t, markupGot).Photo
	userPhoto, ok = selfPhoto.(*tg.UserProfilePhoto)
	if !ok || userPhoto.PhotoID != 780 || userPhoto.DCID != 2 || !userPhoto.HasVideo {
		t.Fatalf("returned self photo after emoji = %+v, want photo 780 dc 2 has_video", selfPhoto)
	}

	stickerReq := &tg.PhotosUploadProfilePhotoRequest{}
	stickerReq.SetVideoEmojiMarkup(&tg.VideoSizeStickerMarkup{
		Stickerset:       &tg.InputStickerSetAnimatedEmoji{},
		StickerID:        123,
		BackgroundColors: []int{0x112233, 0x445566},
	})
	stickerGot, err := r.onPhotosUploadProfilePhoto(WithUserID(ctx, owner.ID), stickerReq)
	if err != nil {
		t.Fatalf("upload animated emoji sticker profile photo: %v", err)
	}
	stickerPhoto, ok := stickerGot.Photo.(*tg.Photo)
	if !ok || len(stickerPhoto.VideoSizes) != 1 {
		t.Fatalf("sticker profile photo = %+v, want photo with video_sizes", stickerGot.Photo)
	}
	stickerSize, ok := stickerPhoto.VideoSizes[0].(*tg.VideoSizeStickerMarkup)
	if !ok {
		t.Fatalf("sticker video size = %T, want *tg.VideoSizeStickerMarkup", stickerPhoto.VideoSizes[0])
	}
	if _, ok := stickerSize.Stickerset.(*tg.InputStickerSetAnimatedEmoji); !ok {
		t.Fatalf("sticker markup set = %T, want inputStickerSetAnimatedEmoji", stickerSize.Stickerset)
	}

	comboReq := &tg.PhotosUploadProfilePhotoRequest{}
	comboReq.SetVideo(&tg.InputFile{ID: 45, Parts: 9, Name: "avatar.mp4"})
	comboReq.SetVideoStartTs(0)
	comboReq.SetVideoEmojiMarkup(&tg.VideoSizeEmojiMarkup{
		EmojiID:          1258816259753929,
		BackgroundColors: []int{0x536dfe, 0x29b6f6, 0x26a69a, 0x66bb6a},
	})
	comboGot, err := r.onPhotosUploadProfilePhoto(WithUserID(ctx, owner.ID), comboReq)
	if err != nil {
		t.Fatalf("upload Android video+emoji profile photo: %v", err)
	}
	comboPhoto, ok := comboGot.Photo.(*tg.Photo)
	if !ok || len(comboPhoto.VideoSizes) != 2 {
		t.Fatalf("combo profile photo = %+v, want two video_sizes", comboGot.Photo)
	}
	if _, ok := comboPhoto.VideoSizes[0].(*tg.VideoSize); !ok {
		t.Fatalf("combo first video size = %T, want *tg.VideoSize", comboPhoto.VideoSizes[0])
	}
	if _, ok := comboPhoto.VideoSizes[1].(*tg.VideoSizeEmojiMarkup); !ok {
		t.Fatalf("combo second video size = %T, want *tg.VideoSizeEmojiMarkup", comboPhoto.VideoSizes[1])
	}
}

func TestUploadProfilePhotoFallbackEmojiAndInvalidFlags(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001003", FirstName: "Owner"})
	files := &fakeFiles{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users: appusers.NewService(userStore, appusers.WithPhotoProvider(files)),
		Files: files,
	}, zaptest.NewLogger(t), clock.System)

	profileReq := &tg.PhotosUploadProfilePhotoRequest{}
	profileReq.SetFile(&tg.InputFile{ID: 42, Parts: 1, Name: "avatar.jpg"})
	if _, err := r.onPhotosUploadProfilePhoto(WithUserID(ctx, owner.ID), profileReq); err != nil {
		t.Fatalf("seed profile photo: %v", err)
	}

	fallbackReq := &tg.PhotosUploadProfilePhotoRequest{}
	fallbackReq.SetFallback(true)
	fallbackReq.SetVideoEmojiMarkup(&tg.VideoSizeEmojiMarkup{EmojiID: 100, BackgroundColors: []int{0x112233}})
	fallbackGot, err := r.onPhotosUploadProfilePhoto(WithUserID(ctx, owner.ID), fallbackReq)
	if err != nil {
		t.Fatalf("upload fallback emoji profile photo: %v", err)
	}
	if photo, ok := fallbackGot.Photo.(*tg.Photo); !ok || photo.ID != 780 {
		t.Fatalf("fallback photo = %+v, want photo 780", fallbackGot.Photo)
	}
	if cur, ok, err := files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, owner.ID, domain.ProfilePhotoKindFallback); err != nil || !ok || cur.ID != 780 {
		t.Fatalf("current fallback photo = %+v ok=%v err=%v, want 780", cur, ok, err)
	}
	if cur, ok, err := files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, owner.ID, domain.ProfilePhotoKindProfile); err != nil || !ok || cur.ID != 778 {
		t.Fatalf("current profile photo after fallback = %+v ok=%v err=%v, want preserved 778", cur, ok, err)
	}

	invalidReq := &tg.PhotosUploadProfilePhotoRequest{}
	invalidReq.SetFile(&tg.InputFile{ID: 44, Parts: 1, Name: "avatar.jpg"})
	invalidReq.SetVideoEmojiMarkup(&tg.VideoSizeEmojiMarkup{EmojiID: 101, BackgroundColors: []int{0x445566}})
	if _, err := r.onPhotosUploadProfilePhoto(WithUserID(ctx, owner.ID), invalidReq); err == nil || !strings.Contains(err.Error(), "PHOTO_INVALID") {
		t.Fatalf("invalid mixed profile photo error = %v, want PHOTO_INVALID", err)
	}
	if cur, ok, err := files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, owner.ID, domain.ProfilePhotoKindProfile); err != nil || !ok || cur.ID != 778 {
		t.Fatalf("current profile photo after invalid = %+v ok=%v err=%v, want preserved 778", cur, ok, err)
	}
}
