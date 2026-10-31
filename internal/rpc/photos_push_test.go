package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TestUploadProfilePhotoPushesUpdateToOtherDevices guards the audit fix: after changing the avatar
// it must push to the account's other online devices (an updateUser signal + Updates.Users with the
// new self user), otherwise the other devices' avatars do not refresh. Rationale: updateUserName
// carries no photo and cannot refresh an avatar, so including the user object is the most reliable
// (see the photos.go pushSelfPhotoUpdate comment). Nothing was pushed at all before; this test
// prevents regression.
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
	req.SetFile(&tg.InputFile{ID: 42, Parts: 1, Name: "a.jpg"}) // File is a flags-optional field, so SetFile must be used to set it
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

// TestUploadProfilePhotoEchoesUpdateToCurrentSession guards the DrKLO Android emoji-avatar echo
// fix: after changing the avatar, besides other devices the current session must also receive
// updateUser + Updates.Users (a fresh self with has_video). DrKLO's uploadProfilePhoto response
// callback does not consume photos.photo.users, and the hand-rebuilt userProfilePhoto drops
// has_video/stripped, so without this echo an emoji/sticker markup avatar renders on the setting
// device only after a restart (matching the reference implementation SyncPushUpdates' all-session
// semantics).
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
	r.selfPhotoEchoPushDelay = 0 // synchronous echo in tests

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

	// PushToSession is called after PushToUserExceptSession, so snapshot().message is the current
	// session's echo.
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
	// The other-device push happens as well (the same updates).
	if other, ok := sessions.lastUserPush().(*tg.Updates); !ok || len(other.Users) == 0 {
		t.Fatalf("other-device push = %T, want *tg.Updates with users", sessions.lastUserPush())
	}
}

// TestDeletePhotosPushesSelfUpdate guards the deletePhotos push: after a deletion takes effect
// (including removing the current avatar with a fallback), it must push updateUser + a fresh self
// to all online sessions (matching the reference implementation deletePhotos' SyncPushUpdates),
// otherwise other devices/the current device leave the avatar in its old state.
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
	// The current session also receives the echo (PushToSession is called last, overwriting
	// snapshot().message).
	snap := sessions.snapshot()
	if snap.sessionID != currentSessionID {
		t.Fatalf("echo session = %d, want %d", snap.sessionID, currentSessionID)
	}
	if _, ok := snap.message.(*tg.Updates); !ok {
		t.Fatalf("echoed message = %T, want *tg.Updates", snap.message)
	}
}

func TestUploadProfilePhotoBotTargetUpdatesOwnedBot(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	botStore := memory.NewBotStore(userStore)
	dialogStore := memory.NewDialogStore()
	bots := botsapp.NewService(userStore, botStore, memory.NewMessageStore(dialogStore))
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001006", FirstName: "Owner"})
	bot, _, err := bots.CreateBot(ctx, owner.ID, "Photo Bot", "photo_shape_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	sessions := &captureSessions{}
	files := &fakeFiles{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore, appusers.WithPhotoProvider(files)),
		Bots:     bots,
		Files:    files,
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	r.selfPhotoEchoPushDelay = 0

	const currentSessionID = int64(454545)
	reqCtx := WithSessionID(WithUserID(ctx, owner.ID), currentSessionID)
	req := &tg.PhotosUploadProfilePhotoRequest{}
	req.SetBot(&tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash})
	req.SetFile(&tg.InputFile{ID: 42, Parts: 1, Name: "bot.jpg"})
	got, err := r.onPhotosUploadProfilePhoto(reqCtx, req)
	if err != nil {
		t.Fatalf("upload bot profile photo: %v", err)
	}

	if cur, ok, err := files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, bot.ID, domain.ProfilePhotoKindProfile); err != nil || !ok || cur.ID != 778 {
		t.Fatalf("bot current profile photo = %+v ok=%v err=%v, want 778", cur, ok, err)
	}
	if _, ok, err := files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, owner.ID, domain.ProfilePhotoKindProfile); err != nil || ok {
		t.Fatalf("owner current profile photo after bot upload: ok=%v err=%v, want unchanged", ok, err)
	}

	returnedBot := firstPhotosUser(t, got)
	if returnedBot.ID != bot.ID || returnedBot.Self {
		t.Fatalf("returned user = id:%d self:%v, want bot id %d and self=false", returnedBot.ID, returnedBot.Self, bot.ID)
	}
	if !returnedBot.Bot || !returnedBot.GetBotCanEdit() {
		t.Fatalf("returned bot flags = bot:%v can_edit:%v, want both true", returnedBot.Bot, returnedBot.GetBotCanEdit())
	}
	returnedPhoto, ok := returnedBot.Photo.(*tg.UserProfilePhoto)
	if !ok || returnedPhoto.PhotoID != 778 || returnedPhoto.DCID != 2 {
		t.Fatalf("returned bot photo = %+v, want photo_id=778 dc_id=2", returnedBot.Photo)
	}

	if pushed := sessions.pushedUserIDs(); len(pushed) != 1 || pushed[0] != owner.ID {
		t.Fatalf("pushed user ids = %v, want owner %d", pushed, owner.ID)
	}
	snap := sessions.snapshot()
	if snap.sessionID != currentSessionID {
		t.Fatalf("echo session = %d, want %d", snap.sessionID, currentSessionID)
	}
	echoed, ok := snap.message.(*tg.Updates)
	if !ok {
		t.Fatalf("echoed message = %T, want *tg.Updates", snap.message)
	}
	hasBotUpdate := false
	for _, u := range echoed.Updates {
		if uu, ok := u.(*tg.UpdateUser); ok && uu.UserID == bot.ID {
			hasBotUpdate = true
		}
	}
	if !hasBotUpdate || len(echoed.Users) == 0 {
		t.Fatalf("echoed updates = %+v, want UpdateUser + bot user", echoed)
	}
	echoedBot, ok := echoed.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("echoed user = %T, want *tg.User", echoed.Users[0])
	}
	echoedPhoto, ok := echoedBot.Photo.(*tg.UserProfilePhoto)
	if echoedBot.ID != bot.ID || echoedBot.Self || !ok || echoedPhoto.PhotoID != 778 {
		t.Fatalf("echoed bot = %+v photo=%+v, want updated non-self bot", echoedBot, echoedBot.Photo)
	}

	sessions.clearMessages()
	clearReq := &tg.PhotosUpdateProfilePhotoRequest{ID: &tg.InputPhotoEmpty{}}
	clearReq.SetBot(&tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash})
	cleared, err := r.onPhotosUpdateProfilePhoto(reqCtx, clearReq)
	if err != nil {
		t.Fatalf("clear bot profile photo: %v", err)
	}
	if _, ok, err := files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, bot.ID, domain.ProfilePhotoKindProfile); err != nil || ok {
		t.Fatalf("bot current profile photo after clear: ok=%v err=%v, want removed", ok, err)
	}
	clearedBot := firstPhotosUser(t, cleared)
	if clearedBot.ID != bot.ID || clearedBot.Self || clearedBot.Photo != nil {
		t.Fatalf("cleared returned bot = id:%d self:%v photo:%+v, want non-self bot without photo", clearedBot.ID, clearedBot.Self, clearedBot.Photo)
	}
	echoed, ok = sessions.snapshot().message.(*tg.Updates)
	if !ok || len(echoed.Users) == 0 {
		t.Fatalf("clear echoed message = %T %+v, want *tg.Updates with users", sessions.snapshot().message, sessions.snapshot().message)
	}
	echoedBot, ok = echoed.Users[0].(*tg.User)
	if !ok || echoedBot.ID != bot.ID || echoedBot.Photo != nil {
		t.Fatalf("clear echoed bot = %T %+v, want bot without photo", echoed.Users[0], echoed.Users[0])
	}
}

func TestUploadProfilePhotoBotTargetRejectsForeignBot(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	botStore := memory.NewBotStore(userStore)
	dialogStore := memory.NewDialogStore()
	bots := botsapp.NewService(userStore, botStore, memory.NewMessageStore(dialogStore))
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550001007", FirstName: "Owner"})
	stranger, _ := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550001008", FirstName: "Stranger"})
	foreignBot, _, err := bots.CreateBot(ctx, stranger.ID, "Foreign Bot", "foreign_photo_shape_bot")
	if err != nil {
		t.Fatalf("create foreign bot: %v", err)
	}
	files := &fakeFiles{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users: appusers.NewService(userStore, appusers.WithPhotoProvider(files)),
		Bots:  bots,
		Files: files,
	}, zaptest.NewLogger(t), clock.System)

	req := &tg.PhotosUploadProfilePhotoRequest{}
	req.SetBot(&tg.InputUser{UserID: foreignBot.ID, AccessHash: foreignBot.AccessHash})
	req.SetFile(&tg.InputFile{ID: 42, Parts: 1, Name: "bot.jpg"})
	if _, err := r.onPhotosUploadProfilePhoto(WithUserID(ctx, owner.ID), req); err == nil || !strings.Contains(err.Error(), "BOT_INVALID") {
		t.Fatalf("foreign bot upload error = %v, want BOT_INVALID", err)
	}
	if _, ok, err := files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, foreignBot.ID, domain.ProfilePhotoKindProfile); err != nil || ok {
		t.Fatalf("foreign bot current profile photo after rejected upload: ok=%v err=%v, want unchanged", ok, err)
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
