package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appcontacts "telesrv/internal/app/contacts"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newContactProfilePhotoTestRouter(t *testing.T) (*Router, *memory.ContactStore, *captureSessions, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550002001", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	friend, err := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550002002", FirstName: "Friend"})
	if err != nil {
		t.Fatalf("create friend: %v", err)
	}
	contactStore := memory.NewContactStore()
	contacts := appcontacts.NewService(contactStore, userStore)
	if _, err := contacts.AddContact(ctx, owner.ID, domain.ContactInput{ContactUserID: friend.ID, FirstName: "Friend"}); err != nil {
		t.Fatalf("add contact: %v", err)
	}
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	users := appusers.NewService(userStore, appusers.WithContactStore(contactStore))
	files := &fakeFiles{photos: map[int64]domain.Photo{}}
	sessions := &captureSessions{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    users,
		Contacts: contacts,
		Files:    files,
		Messages: appmessages.NewService(messageStore, dialogStore, appmessages.WithContactStore(contactStore)),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	return r, contactStore, sessions, owner, friend
}

func contactProfilePhotoReq(friend domain.User) *tg.PhotosUploadContactProfilePhotoRequest {
	return &tg.PhotosUploadContactProfilePhotoRequest{
		UserID: &tg.InputUser{UserID: friend.ID, AccessHash: friend.AccessHash},
	}
}

func uploadedAvatarFile() *tg.InputFile {
	return &tg.InputFile{ID: 42, Parts: 1, Name: "avatar.jpg", MD5Checksum: "d41d8cd98f00b204e9800998ecf8427e"}
}

func personalPhotoID(t *testing.T, store *memory.ContactStore, ownerID, friendID int64) int64 {
	t.Helper()
	refs, err := store.PersonalPhotos(context.Background(), ownerID, []int64{friendID})
	if err != nil {
		t.Fatalf("personal photos: %v", err)
	}
	return refs[friendID].PhotoID
}

func firstPhotosUser(t *testing.T, got *tg.PhotosPhoto) *tg.User {
	t.Helper()
	if got == nil || len(got.Users) == 0 {
		t.Fatalf("photos.photo users missing: %+v", got)
	}
	user, ok := got.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("photos.photo user = %T, want *tg.User", got.Users[0])
	}
	return user
}

func TestUploadContactProfilePhotoSaveAndClearPersonalPhoto(t *testing.T) {
	ctx := context.Background()
	r, contacts, _, owner, friend := newContactProfilePhotoTestRouter(t)

	saveReq := contactProfilePhotoReq(friend)
	saveReq.SetSave(true)
	saveReq.SetFile(uploadedAvatarFile())
	got, err := r.onPhotosUploadContactProfilePhoto(WithUserID(ctx, owner.ID), saveReq)
	if err != nil {
		t.Fatalf("save contact profile photo: %v", err)
	}
	if photo, ok := got.Photo.(*tg.Photo); !ok || photo.ID != 778 || photo.DCID != 2 {
		t.Fatalf("returned photo = %+v, want photo 778/dc2", got.Photo)
	}
	if id := personalPhotoID(t, contacts, owner.ID, friend.ID); id != 778 {
		t.Fatalf("personal photo id = %d, want 778", id)
	}
	userPhoto, ok := firstPhotosUser(t, got).Photo.(*tg.UserProfilePhoto)
	if !ok || userPhoto.PhotoID != 778 || userPhoto.DCID != 2 || !userPhoto.Personal {
		t.Fatalf("returned user photo = %+v, want personal 778/dc2", firstPhotosUser(t, got).Photo)
	}

	clearReq := contactProfilePhotoReq(friend)
	clearReq.SetSave(true)
	cleared, err := r.onPhotosUploadContactProfilePhoto(WithUserID(ctx, owner.ID), clearReq)
	if err != nil {
		t.Fatalf("clear contact profile photo: %v", err)
	}
	if _, ok := cleared.Photo.(*tg.PhotoEmpty); !ok {
		t.Fatalf("cleared photo = %T, want *tg.PhotoEmpty", cleared.Photo)
	}
	if id := personalPhotoID(t, contacts, owner.ID, friend.ID); id != 0 {
		t.Fatalf("personal photo after clear = %d, want 0", id)
	}
}

func TestUploadContactProfilePhotoSuggestCreatesServiceMessage(t *testing.T) {
	ctx := context.Background()
	r, contacts, sessions, owner, friend := newContactProfilePhotoTestRouter(t)

	req := contactProfilePhotoReq(friend)
	req.SetSuggest(true)
	req.SetFile(uploadedAvatarFile())
	got, err := r.onPhotosUploadContactProfilePhoto(WithSessionID(WithUserID(ctx, owner.ID), 91), req)
	if err != nil {
		t.Fatalf("suggest contact profile photo: %v", err)
	}
	if photo, ok := got.Photo.(*tg.Photo); !ok || photo.ID != 778 {
		t.Fatalf("suggest returned photo = %+v, want photo 778", got.Photo)
	}
	if id := personalPhotoID(t, contacts, owner.ID, friend.ID); id != 0 {
		t.Fatalf("suggest set personal photo id = %d, want 0", id)
	}
	if firstPhotosUser(t, got).Photo != nil {
		t.Fatalf("suggest returned user photo = %+v, want unchanged user", firstPhotosUser(t, got).Photo)
	}

	assertSuggestProfilePhotoHistory(t, r, owner.ID, friend.ID, true)
	assertSuggestProfilePhotoHistory(t, r, friend.ID, owner.ID, false)
	snap := sessions.snapshot()
	pushed, ok := snap.message.(*tg.Updates)
	if !ok {
		t.Fatalf("current-session push = %T, want *tg.Updates", snap.message)
	}
	if len(pushed.Updates) != 1 {
		t.Fatalf("current-session updates = %+v, want UpdateNewMessage only", pushed.Updates)
	}
	update, ok := pushed.Updates[0].(*tg.UpdateNewMessage)
	if !ok {
		t.Fatalf("current-session update = %T, want *tg.UpdateNewMessage", pushed.Updates[0])
	}
	if service, ok := update.Message.(*tg.MessageService); !ok || !service.Out {
		t.Fatalf("current-session message = %T %+v, want outgoing MessageService", update.Message, update.Message)
	}
}

func TestUploadContactProfilePhotoAnimatedInputsSetPersonalPhoto(t *testing.T) {
	ctx := context.Background()
	r, contacts, _, owner, friend := newContactProfilePhotoTestRouter(t)

	videoReq := contactProfilePhotoReq(friend)
	videoReq.SetSave(true)
	videoReq.SetVideo(&tg.InputFile{ID: 43, Parts: 1, Name: "avatar.mp4"})
	videoReq.SetVideoStartTs(0.5)
	videoGot, err := r.onPhotosUploadContactProfilePhoto(WithUserID(ctx, owner.ID), videoReq)
	if err != nil {
		t.Fatalf("save animated video profile photo: %v", err)
	}
	videoPhoto, ok := videoGot.Photo.(*tg.Photo)
	if !ok || videoPhoto.ID != 779 || len(videoPhoto.VideoSizes) != 1 {
		t.Fatalf("video photo = %+v, want photo 779 with video_sizes", videoGot.Photo)
	}
	videoSize, ok := videoPhoto.VideoSizes[0].(*tg.VideoSize)
	if !ok || videoSize.Type != "u" || videoSize.VideoStartTs != 0.5 {
		t.Fatalf("video size = %+v, want videoSize u start 0.5", videoPhoto.VideoSizes[0])
	}
	if id := personalPhotoID(t, contacts, owner.ID, friend.ID); id != 779 {
		t.Fatalf("personal photo after video = %d, want 779", id)
	}

	markupReq := contactProfilePhotoReq(friend)
	markupReq.SetSave(true)
	markupReq.SetVideoEmojiMarkup(&tg.VideoSizeEmojiMarkup{EmojiID: 99, BackgroundColors: []int{0xffffff}})
	markupGot, err := r.onPhotosUploadContactProfilePhoto(WithUserID(ctx, owner.ID), markupReq)
	if err != nil {
		t.Fatalf("save emoji markup profile photo: %v", err)
	}
	markupPhoto, ok := markupGot.Photo.(*tg.Photo)
	if !ok || markupPhoto.ID != 780 || len(markupPhoto.VideoSizes) != 1 {
		t.Fatalf("markup photo = %+v, want photo 780 with video_sizes", markupGot.Photo)
	}
	if _, ok := markupPhoto.VideoSizes[0].(*tg.VideoSizeEmojiMarkup); !ok {
		t.Fatalf("markup video size = %T, want *tg.VideoSizeEmojiMarkup", markupPhoto.VideoSizes[0])
	}
	if id := personalPhotoID(t, contacts, owner.ID, friend.ID); id != 780 {
		t.Fatalf("personal photo after markup = %d, want 780", id)
	}
}

func TestUploadContactProfilePhotoInvalidAnimatedFlagsDoNotClear(t *testing.T) {
	ctx := context.Background()
	r, contacts, _, owner, friend := newContactProfilePhotoTestRouter(t)
	saveReq := contactProfilePhotoReq(friend)
	saveReq.SetSave(true)
	saveReq.SetFile(uploadedAvatarFile())
	if _, err := r.onPhotosUploadContactProfilePhoto(WithUserID(ctx, owner.ID), saveReq); err != nil {
		t.Fatalf("seed personal photo: %v", err)
	}

	cases := []struct {
		name  string
		setup func(*tg.PhotosUploadContactProfilePhotoRequest)
	}{
		{
			name: "file-and-video",
			setup: func(req *tg.PhotosUploadContactProfilePhotoRequest) {
				req.SetSave(true)
				req.SetFile(uploadedAvatarFile())
				req.SetVideo(&tg.InputFile{ID: 44, Parts: 1, Name: "avatar.mp4"})
			},
		},
		{
			name: "video-start-without-video",
			setup: func(req *tg.PhotosUploadContactProfilePhotoRequest) {
				req.SetSave(true)
				req.SetVideoStartTs(0.5)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := contactProfilePhotoReq(friend)
			tc.setup(req)
			if _, err := r.onPhotosUploadContactProfilePhoto(WithUserID(ctx, owner.ID), req); err == nil || !strings.Contains(err.Error(), "PHOTO_INVALID") {
				t.Fatalf("invalid animated request error = %v, want PHOTO_INVALID", err)
			}
			if id := personalPhotoID(t, contacts, owner.ID, friend.ID); id != 778 {
				t.Fatalf("personal photo after invalid request = %d, want preserved 778", id)
			}
		})
	}
}

func assertSuggestProfilePhotoHistory(t *testing.T, r *Router, ownerID, peerID int64, out bool) {
	t.Helper()
	list, err := r.deps.Messages.GetHistory(context.Background(), ownerID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: peerID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("get history owner %d peer %d: %v", ownerID, peerID, err)
	}
	if len(list.Messages) != 1 {
		t.Fatalf("history owner %d peer %d len = %d, want 1", ownerID, peerID, len(list.Messages))
	}
	msg := tgMessage(list.Messages[0])
	service, ok := msg.(*tg.MessageService)
	if !ok {
		t.Fatalf("history message = %T %+v, want *tg.MessageService", msg, msg)
	}
	if service.Out != out {
		t.Fatalf("history service out = %v, want %v", service.Out, out)
	}
	action, ok := service.Action.(*tg.MessageActionSuggestProfilePhoto)
	if !ok {
		t.Fatalf("service action = %T, want suggest profile photo", service.Action)
	}
	photo, ok := action.Photo.(*tg.Photo)
	if !ok || photo.ID != 778 {
		t.Fatalf("suggest action photo = %+v, want photo 778", action.Photo)
	}
}
