package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

// registerPhotos registers the photos.* RPC handlers (avatar upload / switch / query / delete).
func (r *Router) registerPhotos(d *tlprofile.Dispatcher) {
	registerRPC[*tg.PhotosUploadProfilePhotoRequest](d, tlprofile.SemanticMethodPhotosUploadProfilePhoto, func(ctx context.Context, layerRequest *tg.PhotosUploadProfilePhotoRequest) (any, error) {
		return r.onPhotosUploadProfilePhoto(ctx, layerRequest)
	})
	registerRPC[*tg.PhotosUpdateProfilePhotoRequest](d, tlprofile.SemanticMethodPhotosUpdateProfilePhoto, func(ctx context.Context, layerRequest *tg.PhotosUpdateProfilePhotoRequest) (any, error) {
		return r.onPhotosUpdateProfilePhoto(ctx, layerRequest)
	})
	registerRPC[*tg.PhotosUploadContactProfilePhotoRequest](d, tlprofile.SemanticMethodPhotosUploadContactProfilePhoto, func(ctx context.Context, layerRequest *tg.PhotosUploadContactProfilePhotoRequest) (any, error) {
		return r.onPhotosUploadContactProfilePhoto(ctx, layerRequest)
	})
	registerRPC[*tg.PhotosGetUserPhotosRequest](d, tlprofile.SemanticMethodPhotosGetUserPhotos, func(ctx context.Context, layerRequest *tg.PhotosGetUserPhotosRequest) (any, error) {
		return r.onPhotosGetUserPhotos(ctx, layerRequest)
	})
	registerRPC[*tg.PhotosDeletePhotosRequest](d, tlprofile.SemanticMethodPhotosDeletePhotos, func(ctx context.Context, layerRequest *tg.PhotosDeletePhotosRequest) (any, error) {
		return r.onPhotosDeletePhotos(ctx, layerRequest.
			ID)
	})

}

func (r *Router) onPhotosUploadProfilePhoto(ctx context.Context, req *tg.PhotosUploadProfilePhotoRequest) (*tg.PhotosPhoto, error) {
	if r.deps.Files == nil {
		return nil, notImplementedErr()
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !ok || userID == 0 {
		return nil, photoInvalidErr()
	}
	targetUserID := userID
	var botTarget domain.User
	bot, hasBot := req.GetBot()
	if target, isBotTarget, err := r.resolveProfilePhotoBotTarget(ctx, userID, bot, hasBot); err != nil {
		return nil, err
	} else if isBotTarget {
		botTarget = target
		targetUserID = target.ID
	}
	upload, mediaFlags, err := parseProfilePhotoUpload(req)
	if err != nil {
		return nil, err
	}
	if mediaFlags == 0 {
		return nil, photoInvalidErr()
	}
	kind := domain.ProfilePhotoKindProfile
	if req.GetFallback() {
		kind = domain.ProfilePhotoKindFallback
	}
	photo, err := r.createProfilePhoto(ctx, userID, upload)
	if err != nil {
		return nil, err
	}
	photo, found, err := r.deps.Files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, targetUserID, kind, photo.ID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, photoInvalidErr()
	}
	if botTarget.ID != 0 {
		return r.photosPhotoForBotTarget(ctx, userID, botTarget.ID, photo, kind), nil
	}
	return r.photosPhotoForSelf(ctx, userID, photo, kind), nil
}

func (r *Router) onPhotosUpdateProfilePhoto(ctx context.Context, req *tg.PhotosUpdateProfilePhotoRequest) (*tg.PhotosPhoto, error) {
	if r.deps.Files == nil {
		return nil, notImplementedErr()
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !ok || userID == 0 {
		return nil, photoInvalidErr()
	}
	targetUserID := userID
	var botTarget domain.User
	bot, hasBot := req.GetBot()
	if target, isBotTarget, err := r.resolveProfilePhotoBotTarget(ctx, userID, bot, hasBot); err != nil {
		return nil, err
	} else if isBotTarget {
		botTarget = target
		targetUserID = target.ID
	}
	kind := domain.ProfilePhotoKindProfile
	if req.GetFallback() {
		kind = domain.ProfilePhotoKindFallback
	}
	switch in := req.ID.(type) {
	case *tg.InputPhoto:
		photo, found, err := r.deps.Files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, targetUserID, kind, in.ID, int(r.clock.Now().Unix()))
		if err != nil {
			return nil, internalErr()
		}
		if !found {
			return nil, photoInvalidErr()
		}
		if botTarget.ID != 0 {
			return r.photosPhotoForBotTarget(ctx, userID, botTarget.ID, photo, kind), nil
		}
		return r.photosPhotoForSelf(ctx, userID, photo, kind), nil
	default:
		// InputPhotoEmpty: remove the current avatar (deactivate the existing current photo).
		if cur, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, targetUserID, kind); err == nil && found {
			_, _ = r.deps.Files.DeleteProfilePhotosKind(ctx, domain.PeerTypeUser, targetUserID, kind, []int64{cur.ID})
		}
		if botTarget.ID != 0 {
			return r.photosPhotoForBotTarget(ctx, userID, botTarget.ID, domain.Photo{}, kind), nil
		}
		return r.photosPhotoForSelf(ctx, userID, domain.Photo{}, kind), nil
	}
}

func (r *Router) resolveProfilePhotoBotTarget(ctx context.Context, ownerUserID int64, bot tg.InputUserClass, hasBot bool) (domain.User, bool, error) {
	if !hasBot {
		return domain.User{}, false, nil
	}
	if bot == nil || ownerUserID == 0 || r.deps.Users == nil || r.deps.Bots == nil {
		return domain.User{}, false, botInvalidErr()
	}
	target, found, err := r.userFromInput(ctx, ownerUserID, bot)
	if err != nil {
		return domain.User{}, false, internalErr()
	}
	if !found || target.ID == 0 {
		return domain.User{}, false, botInvalidErr()
	}
	owns, err := r.deps.Bots.OwnsBot(ctx, ownerUserID, target.ID)
	if err != nil {
		return domain.User{}, false, internalErr()
	}
	if !owns {
		return domain.User{}, false, botInvalidErr()
	}
	return target, true, nil
}

func (r *Router) onPhotosUploadContactProfilePhoto(ctx context.Context, req *tg.PhotosUploadContactProfilePhotoRequest) (*tg.PhotosPhoto, error) {
	if r.deps.Files == nil || r.deps.Users == nil {
		return nil, notImplementedErr()
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !ok || userID == 0 {
		return nil, photoInvalidErr()
	}
	target, found, err := r.userFromInput(ctx, userID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found || target.ID == 0 || target.ID == userID {
		return nil, userIDInvalidErr()
	}
	upload, mediaFlags, err := parseProfilePhotoUpload(req)
	if err != nil {
		return nil, err
	}
	if mediaFlags == 0 && req.GetSave() {
		if r.deps.Contacts == nil {
			return nil, notImplementedErr()
		}
		if _, err := r.deps.Contacts.ClearPersonalPhoto(ctx, userID, target.ID, int(r.clock.Now().Unix())); err != nil {
			return nil, contactErr(err)
		}
		r.invalidateRPCProjectionForPeer(userID, domain.Peer{Type: domain.PeerTypeUser, ID: target.ID})
		return r.photosPhotoForUser(ctx, userID, target.ID, domain.Photo{}), nil
	}
	if mediaFlags == 0 {
		return nil, photoInvalidErr()
	}
	photo, err := r.createProfilePhoto(ctx, userID, upload)
	if err != nil {
		return nil, err
	}
	if req.GetSuggest() {
		if r.deps.Messages == nil {
			return nil, notImplementedErr()
		}
		res, err := r.sendSuggestedProfilePhotoMessage(ctx, userID, target.ID, photo)
		if err != nil {
			return nil, err
		}
		r.pushSuggestedProfilePhotoCurrentSession(ctx, userID, res)
		return r.photosPhotoForUser(ctx, userID, target.ID, photo), nil
	}
	if r.deps.Contacts == nil {
		return nil, notImplementedErr()
	}
	if _, err := r.deps.Contacts.SetPersonalPhoto(ctx, userID, target.ID, photo, int(r.clock.Now().Unix())); err != nil {
		return nil, contactErr(err)
	}
	r.invalidateRPCProjectionForPeer(userID, domain.Peer{Type: domain.PeerTypeUser, ID: target.ID})
	return r.photosPhotoForUserWithPersonalPhoto(ctx, userID, target.ID, photo), nil
}

type profilePhotoUploadRequest interface {
	GetFile() (tg.InputFileClass, bool)
	GetVideo() (tg.InputFileClass, bool)
	GetVideoStartTs() (float64, bool)
	GetVideoEmojiMarkup() (tg.VideoSizeClass, bool)
}

type profilePhotoUpload struct {
	file         tg.InputFileClass
	hasFile      bool
	video        tg.InputFileClass
	hasVideo     bool
	videoStartTs float64
	markup       tg.VideoSizeClass
	hasMarkup    bool
}

func parseProfilePhotoUpload(req profilePhotoUploadRequest) (profilePhotoUpload, int, error) {
	file, hasFile := req.GetFile()
	video, hasVideo := req.GetVideo()
	videoStartTs, hasVideoStartTs := req.GetVideoStartTs()
	markup, hasMarkup := req.GetVideoEmojiMarkup()
	if hasFile && file == nil {
		return profilePhotoUpload{}, 0, photoInvalidErr()
	}
	if hasVideo && video == nil {
		return profilePhotoUpload{}, 0, photoInvalidErr()
	}
	if hasMarkup && markup == nil {
		return profilePhotoUpload{}, 0, photoInvalidErr()
	}
	if hasFile && (hasVideo || hasMarkup) {
		return profilePhotoUpload{}, 0, photoInvalidErr()
	}
	if hasVideoStartTs && !hasVideo {
		return profilePhotoUpload{}, 0, photoInvalidErr()
	}
	mediaFlags := 0
	if hasFile || hasVideo || hasMarkup {
		mediaFlags = 1
	}
	return profilePhotoUpload{
		file:         file,
		hasFile:      hasFile,
		video:        video,
		hasVideo:     hasVideo,
		videoStartTs: videoStartTs,
		markup:       markup,
		hasMarkup:    hasMarkup,
	}, mediaFlags, nil
}

func (r *Router) createProfilePhoto(ctx context.Context, userID int64, upload profilePhotoUpload) (domain.Photo, error) {
	switch {
	case upload.hasFile:
		ref, ok := uploadedFileRef(userID, upload.file)
		if !ok {
			return domain.Photo{}, fileReferenceInvalidErr()
		}
		photo, err := r.deps.Files.CreateAvatarFromUpload(ctx, ref)
		if err != nil {
			return domain.Photo{}, photoUploadErr(err)
		}
		return photo, nil
	case upload.hasVideo && upload.hasMarkup:
		ref, ok := uploadedFileRef(userID, upload.video)
		if !ok {
			return domain.Photo{}, fileReferenceInvalidErr()
		}
		size, ok := domainPhotoVideoMarkup(upload.markup)
		if !ok {
			return domain.Photo{}, photoInvalidErr()
		}
		photo, err := r.deps.Files.CreateAvatarVideoMarkupFromUpload(ctx, ref, upload.videoStartTs, size)
		if err != nil {
			return domain.Photo{}, photoUploadErr(err)
		}
		return photo, nil
	case upload.hasVideo:
		ref, ok := uploadedFileRef(userID, upload.video)
		if !ok {
			return domain.Photo{}, fileReferenceInvalidErr()
		}
		photo, err := r.deps.Files.CreateAvatarVideoFromUpload(ctx, ref, upload.videoStartTs)
		if err != nil {
			return domain.Photo{}, photoUploadErr(err)
		}
		return photo, nil
	case upload.hasMarkup:
		size, ok := domainPhotoVideoMarkup(upload.markup)
		if !ok {
			return domain.Photo{}, photoInvalidErr()
		}
		photo, err := r.deps.Files.CreateAvatarMarkup(ctx, size)
		if err != nil {
			return domain.Photo{}, photoUploadErr(err)
		}
		return photo, nil
	default:
		return domain.Photo{}, photoInvalidErr()
	}
}

func domainPhotoVideoMarkup(markup tg.VideoSizeClass) (domain.PhotoSize, bool) {
	switch v := markup.(type) {
	case *tg.VideoSizeEmojiMarkup:
		if v.EmojiID == 0 || len(v.BackgroundColors) == 0 {
			return domain.PhotoSize{}, false
		}
		return domain.PhotoSize{
			Kind:             domain.PhotoSizeKindVideoEmojiMarkup,
			EmojiID:          v.EmojiID,
			BackgroundColors: append([]int(nil), v.BackgroundColors...),
		}, true
	case *tg.VideoSizeStickerMarkup:
		if v.StickerID == 0 || len(v.BackgroundColors) == 0 {
			return domain.PhotoSize{}, false
		}
		size := domain.PhotoSize{
			Kind:             domain.PhotoSizeKindVideoStickerMarkup,
			StickerID:        v.StickerID,
			BackgroundColors: append([]int(nil), v.BackgroundColors...),
		}
		setRef, ok := stickerSetRefFromInput(v.Stickerset)
		if !ok {
			return domain.PhotoSize{}, false
		}
		switch setRef.Kind {
		case domain.StickerSetRefByID:
			if setRef.ID == 0 {
				return domain.PhotoSize{}, false
			}
			size.StickerSetID = setRef.ID
			size.StickerSetAccessHash = setRef.AccessHash
		case domain.StickerSetRefByShortName:
			if setRef.ShortName == "" {
				return domain.PhotoSize{}, false
			}
			size.StickerSetShortName = setRef.ShortName
		case domain.StickerSetRefBySystem:
			if setRef.SystemKey == "" {
				return domain.PhotoSize{}, false
			}
			size.StickerSetSystemKey = setRef.SystemKey
		default:
			return domain.PhotoSize{}, false
		}
		return size, true
	default:
		return domain.PhotoSize{}, false
	}
}

func (r *Router) sendSuggestedProfilePhotoMessage(ctx context.Context, userID, targetUserID int64, photo domain.Photo) (domain.SendPrivateTextResult, error) {
	if photo.ID == 0 {
		return domain.SendPrivateTextResult{}, photoInvalidErr()
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, userID, targetUserID)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	sessionID, _ := SessionIDFrom(ctx)
	photoCopy := photo
	res, err := r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:    userID,
		RecipientUserID: targetUserID,
		RandomID:        contactProfilePhotoSuggestRandomID(userID, targetUserID, photo.ID, r.clock.Now().UnixNano()),
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind:  domain.MessageServiceActionSuggestProfilePhoto,
				Photo: &photoCopy,
			},
		},
		Date:             int(r.clock.Now().Unix()),
		OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:  sessionID,
		RecipientBlocked: recipientBlocked,
	})
	if err != nil {
		return domain.SendPrivateTextResult{}, messageSendErr(err)
	}
	return res, nil
}

func contactProfilePhotoSuggestRandomID(userID, targetUserID, photoID, nowNano int64) int64 {
	id := nowNano ^ (userID << 17) ^ (targetUserID << 7) ^ photoID
	if id == 0 {
		return photoID
	}
	return id
}

func (r *Router) pushSuggestedProfilePhotoCurrentSession(ctx context.Context, userID int64, res domain.SendPrivateTextResult) {
	if res.SenderMessage.ID == 0 || res.SenderEvent.Pts == 0 {
		return
	}
	updates := tgPrivateMessageUpdates(
		res.SenderEvent,
		res.SenderMessage,
		0,
		false,
		r.usersForMessageUpdate(ctx, userID, res.SenderMessage),
		r.chatsForMessageUpdate(ctx, userID, res.SenderMessage),
	)
	r.pushCurrentSessionMessage(ctx, "push suggested profile photo service message", updates)
}

func (r *Router) onPhotosGetUserPhotos(ctx context.Context, req *tg.PhotosGetUserPhotosRequest) (tg.PhotosPhotosClass, error) {
	if r.deps.Files == nil {
		return &tg.PhotosPhotos{}, nil
	}
	currentUserID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !ok || currentUserID == 0 {
		return nil, userIDInvalidErr()
	}
	target, found, err := r.userFromInput(ctx, currentUserID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, userIDInvalidErr()
	}
	offset := req.Offset
	if offset < -1 {
		offset = -1
	}
	if offset < 0 && req.MaxID <= 0 {
		offset = 0
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	// A frozen/deleted account appears to observers as a deleted tombstone: the main avatar has
	// collapsed to the "deleted account" placeholder, so the album must be emptied too, otherwise the
	// profile page shows two avatars at once — the tombstone placeholder and the user's real photo.
	// The album data is kept in Files storage (dropping it would break restore-on-unfreeze); the
	// tombstone is a view-time projection, so after unfreezing this naturally allows through and the
	// real photos are restored verbatim. Album data is not deleted.
	var photos []domain.Photo
	var total int
	if target.ID != currentUserID && target.Deleted {
		// tombstone: empty album
	} else {
		photos, total, err = r.deps.Files.GetProfilePhotos(ctx, domain.PeerTypeUser, target.ID, offset, limit, req.MaxID)
		if err != nil {
			return nil, internalErr()
		}
	}
	tgPhotos := make([]tg.PhotoClass, 0, len(photos))
	for _, p := range photos {
		tgPhotos = append(tgPhotos, tgPhoto(p))
	}
	// When opening your own avatar album (inputUserSelf or an explicit self ID), target is the
	// viewer, so the self flag is required; otherwise a self=false user for yourself would pollute
	// DrKLO's account cache (Saved Messages transforms).
	selfUser := r.tgUser(target)
	if target.ID == currentUserID {
		selfUser = r.tgSelfUser(target)
	}
	users := []tg.UserClass{selfUser}
	r.applyPeerReadModels(ctx, currentUserID, users, nil)
	countOffset := offset
	if countOffset < 0 {
		countOffset = 0
	}
	if total > len(photos)+countOffset {
		return &tg.PhotosPhotosSlice{Count: total, Photos: tgPhotos, Users: users}, nil
	}
	return &tg.PhotosPhotos{Photos: tgPhotos, Users: users}, nil
}

func (r *Router) onPhotosDeletePhotos(ctx context.Context, id []tg.InputPhotoClass) ([]int64, error) {
	if r.deps.Files == nil {
		return []int64{}, nil
	}
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !ok || userID == 0 {
		return nil, photoInvalidErr()
	}
	ids := make([]int64, 0, len(id))
	for _, in := range id {
		if photo, isPhoto := in.(*tg.InputPhoto); isPhoto && photo.ID != 0 {
			ids = append(ids, photo.ID)
		}
	}
	if len(ids) == 0 {
		return []int64{}, nil
	}
	deleted, err := r.deps.Files.DeleteProfilePhotos(ctx, domain.PeerTypeUser, userID, ids)
	if err != nil {
		return nil, internalErr()
	}
	r.invalidateRPCProjectionForUser(userID)
	// Deletion may remove the current avatar (falling back to the next one or none): following the
	// same discipline as uploadProfilePhoto, push updateUser + a fresh self to all online sessions
	// (including the current one) (matching the reference implementation deletePhotos'
	// SyncPushUpdates); otherwise other devices and the current device (DrKLO's local delete logic
	// likewise does not rebuild has_video) leave the avatar in its old state.
	if deleted > 0 && r.deps.Users != nil {
		if self, err := r.deps.Users.Self(ctx, userID); err == nil {
			r.pushSelfPhotoUpdate(ctx, self)
		}
	}
	return ids, nil
}

// photosPhotoForSelf assembles the photos.photo response (new photo + a self user with the avatar)
// and, after an avatar change, pushes to the account's other online devices so they refresh the
// avatar immediately. Called only by avatar-changing paths such as uploadProfilePhoto /
// updateProfilePhoto (read-only paths must not use it, or it would wrongly trigger a push).
func (r *Router) photosPhotoForSelf(ctx context.Context, userID int64, photo domain.Photo, kind domain.ProfilePhotoKind) *tg.PhotosPhoto {
	out := &tg.PhotosPhoto{Photo: tgPhoto(photo), Users: []tg.UserClass{}}
	r.invalidateRPCProjectionForUser(userID)
	if r.deps.Users == nil {
		return out
	}
	self, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return out
	}
	if kind == domain.ProfilePhotoKindProfile {
		applyProfilePhotoToUser(&self, photo)
	}
	projected := r.tgSelfUser(self)
	pushed := r.tgSelfUser(self)
	r.applyUsernamesToPeerObjects(ctx, []tg.UserClass{projected, pushed}, nil)
	out.Users = append(out.Users, projected)
	r.pushSelfPhotoUpdateWithUser(ctx, self, pushed)
	return out
}

func (r *Router) photosPhotoForBotTarget(ctx context.Context, viewerUserID, botUserID int64, photo domain.Photo, kind domain.ProfilePhotoKind) *tg.PhotosPhoto {
	out := &tg.PhotosPhoto{Photo: tgPhoto(photo), Users: []tg.UserClass{}}
	r.invalidateRPCProjectionForUser(botUserID)
	if r.deps.Users == nil {
		return out
	}
	bot, found, err := r.deps.Users.ByID(ctx, viewerUserID, botUserID)
	if err != nil || !found {
		return out
	}
	if kind == domain.ProfilePhotoKindProfile {
		applyProfilePhotoToUser(&bot, photo)
	}
	projected := r.tgUser(bot)
	_ = r.applyBotCanEditToUser(ctx, viewerUserID, bot, projected)
	pushed := r.tgUser(bot)
	_ = r.applyBotCanEditToUser(ctx, viewerUserID, bot, pushed)
	r.applyUsernamesToPeerObjects(ctx, []tg.UserClass{projected, pushed}, nil)
	out.Users = append(out.Users, projected)
	r.pushBotPhotoUpdateToOwner(ctx, viewerUserID, bot, pushed)
	return out
}

func (r *Router) photosPhotoForUser(ctx context.Context, viewerUserID, targetUserID int64, photo domain.Photo) *tg.PhotosPhoto {
	out := &tg.PhotosPhoto{Photo: tgPhoto(photo), Users: []tg.UserClass{}}
	if r.deps.Users == nil {
		return out
	}
	user, found, err := r.deps.Users.ByID(ctx, viewerUserID, targetUserID)
	if err != nil || !found {
		return out
	}
	out.Users = append(out.Users, r.tgUser(user))
	r.applyUsernamesToPeerObjects(ctx, out.Users, nil)
	return out
}

func (r *Router) photosPhotoForUserWithPersonalPhoto(ctx context.Context, viewerUserID, targetUserID int64, photo domain.Photo) *tg.PhotosPhoto {
	out := r.photosPhotoForUser(ctx, viewerUserID, targetUserID, photo)
	if photo.ID == 0 || len(out.Users) == 0 {
		return out
	}
	user, ok := out.Users[0].(*tg.User)
	if !ok || user == nil {
		return out
	}
	user.Photo = &tg.UserProfilePhoto{PhotoID: photo.ID, DCID: photo.DCID, Personal: true}
	if domain.PhotoHasVideo(photo.Sizes) {
		user.Photo.(*tg.UserProfilePhoto).SetHasVideo(true)
	}
	if stripped := domain.StrippedFromSizes(photo.Sizes); len(stripped) > 0 {
		user.Photo.(*tg.UserProfilePhoto).SetStrippedThumb(stripped)
	}
	return out
}

func applyProfilePhotoToUser(user *domain.User, photo domain.Photo) {
	if user == nil {
		return
	}
	if photo.ID == 0 {
		user.PhotoID = 0
		user.PhotoDCID = 0
		user.PhotoStripped = nil
		user.PhotoPersonal = false
		user.PhotoHasVideo = false
		return
	}
	user.PhotoID = photo.ID
	user.PhotoDCID = photo.DCID
	user.PhotoStripped = domain.StrippedFromSizes(photo.Sizes)
	user.PhotoPersonal = false
	user.PhotoHasVideo = domain.PhotoHasVideo(photo.Sizes)
}

// pushSelfPhotoUpdate pushes the avatar change to all of the account's online devices (including
// the current session), matching the reference implementation SyncPushUpdates' all-session
// semantics. updateUserName carries no photo and cannot refresh an avatar; updateUser is merely a
// "this user changed" signal (TDesktop forceFull-reloads only when the peer is already full-loaded);
// the most reliable is to include the complete self user with the new userProfilePhoto in
// Updates.Users, so TDesktop refreshes immediately via processUser→setPhoto→peerUpdated(Photo).
//
// The current session cannot rely on the RPC return alone: DrKLO Android's uploadProfilePhoto
// response callbacks (ProfileActivity/SettingsActivity.didUploadPhoto, PhotoUtilities) do not consume
// photos.photo.users and instead rebuild userProfilePhoto by hand — filling only
// photo_id/photo_small/photo_big and dropping has_video/stripped_thumb; the local rendering of
// emoji/sticker markup avatars (ImageReceiver.setForUserOrChat → VectorAvatarThumbDrawable) requires
// user.photo.has_video==true, so without the push the device would show the emoji avatar only after
// the next fresh self user (usually a restart). The current session's echo is delayed so that by the
// time it arrives the client has finished its own RPC response callback (otherwise the Android
// callback would overwrite the photo the push just fixed back to the no-has_video shape).
func (r *Router) pushSelfPhotoUpdate(ctx context.Context, self domain.User) {
	if self.ID == 0 {
		return
	}
	r.pushSelfPhotoUpdateWithUser(ctx, self, r.tgSelfUserWithUsernames(ctx, self))
}

func (r *Router) pushSelfPhotoUpdateWithUser(ctx context.Context, self domain.User, projected *tg.User) {
	if self.ID == 0 || projected == nil {
		return
	}
	updates := selfPhotoUpdates(self, int(r.clock.Now().Unix()), projected)
	r.pushUserUpdates(ctx, self.ID, updates)
	r.pushSelfPhotoUpdateToCurrentSession(ctx, updates)
}

func (r *Router) pushBotPhotoUpdateToOwner(ctx context.Context, ownerUserID int64, bot domain.User, projected *tg.User) {
	if ownerUserID == 0 || bot.ID == 0 || projected == nil {
		return
	}
	updates := selfPhotoUpdates(bot, int(r.clock.Now().Unix()), projected)
	r.pushUserUpdates(ctx, ownerUserID, updates)
	r.pushSelfPhotoUpdateToCurrentSession(ctx, updates)
}

// defaultSelfPhotoEchoPushDelay is the delay for echoing updateUser to the current session after an
// avatar change: it must be later than the RPC result write and the client response callback,
// otherwise DrKLO's manual photo rebuild would overwrite the echoed content. updateUser has no pts,
// so a late/missed delivery does not affect difference correctness.
const defaultSelfPhotoEchoPushDelay = 500 * time.Millisecond

// pushSelfPhotoUpdateToCurrentSession delays echoing the avatar-change updates to the current
// session. The ctx value is captured before scheduling (the request ctx is invalid once the handler
// returns).
func (r *Router) pushSelfPhotoUpdateToCurrentSession(ctx context.Context, updates *tg.Updates) {
	if r.deps.Sessions == nil || updates == nil {
		return
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	rawAuthKeyID := rawAuthKeyIDForOrigin(ctx)
	push := func() {
		pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.deps.Sessions.PushToSessionForAuthKey(pushCtx, rawAuthKeyID, sessionID, proto.MessageFromServer, updates); err != nil {
			r.log.Debug("push self photo update to current session", zap.Int64("session_id", sessionID), zap.Error(err))
		}
	}
	if r.selfPhotoEchoPushDelay <= 0 {
		push()
		return
	}
	time.AfterFunc(r.selfPhotoEchoPushDelay, push)
}

func selfPhotoUpdates(self domain.User, date int, user tg.UserClass) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: self.ID}},
		Users:   []tg.UserClass{user},
		Date:    date,
	}
}

// uploadedFileRef converts tg.InputFile / InputFileBig into domain.UploadedFileRef.
func uploadedFileRef(ownerUserID int64, file tg.InputFileClass) (domain.UploadedFileRef, bool) {
	switch f := file.(type) {
	case *tg.InputFile:
		if f.ID == 0 || f.Parts <= 0 {
			return domain.UploadedFileRef{}, false
		}
		return domain.UploadedFileRef{OwnerUserID: ownerUserID, FileID: f.ID, Parts: f.Parts, Name: f.Name, MD5: f.MD5Checksum}, true
	case *tg.InputFileBig:
		if f.ID == 0 || f.Parts <= 0 {
			return domain.UploadedFileRef{}, false
		}
		return domain.UploadedFileRef{OwnerUserID: ownerUserID, FileID: f.ID, Parts: f.Parts, Name: f.Name, Big: true}, true
	default:
		return domain.UploadedFileRef{}, false
	}
}

// resolveInputChatPhoto resolves tg.InputChatPhoto into *domain.Photo (nil = clear the avatar).
// Supports a new upload (InputChatUploadedPhoto) and referencing an existing photo
// (InputChatPhoto{InputPhoto}).
func (r *Router) resolveInputChatPhoto(ctx context.Context, userID int64, input tg.InputChatPhotoClass) (*domain.Photo, error) {
	switch in := input.(type) {
	case *tg.InputChatPhotoEmpty:
		return nil, nil
	case *tg.InputChatUploadedPhoto:
		if r.deps.Files == nil {
			return nil, photoInvalidErr()
		}
		upload, mediaFlags, err := parseProfilePhotoUpload(in)
		if err != nil {
			return nil, err
		}
		if mediaFlags == 0 {
			return nil, photoInvalidErr()
		}
		photo, err := r.createProfilePhoto(ctx, userID, upload)
		if err != nil {
			return nil, err
		}
		return &photo, nil
	case *tg.InputChatPhoto:
		switch id := in.ID.(type) {
		case *tg.InputPhoto:
			if r.deps.Files == nil {
				return nil, photoInvalidErr()
			}
			photo, found, err := r.deps.Files.GetPhoto(ctx, id.ID)
			if err != nil {
				return nil, internalErr()
			}
			if !found {
				return nil, photoInvalidErr()
			}
			return &photo, nil
		default:
			return nil, nil // InputPhotoEmpty -> clear
		}
	default:
		return nil, photoInvalidErr()
	}
}

func photoUploadErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrFilePartsInvalid):
		return filePartsInvalidErr()
	case errors.Is(err, domain.ErrPhotoInvalid):
		return photoInvalidErr()
	case errors.Is(err, domain.ErrStorageFull):
		return storageFullErr()
	default:
		return internalErr()
	}
}
