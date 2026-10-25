package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// registerPhotos 注册 photos.* RPC handler（头像上传 / 切换 / 查询 / 删除）。
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
	if err := r.requireAccountDelivery(userID, "photos.uploadProfilePhoto"); err != nil {
		return nil, err
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
	base, err := r.profilePhotoDeliveryBase(ctx, userID, botTarget)
	if err != nil {
		return nil, err
	}
	effects := r.profilePhotoDeliveryEffects(ctx, userID, base, botTarget.ID != 0, kind)
	mutation, found, err := r.deps.Files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, targetUserID, kind, photo.ID, int(r.clock.Now().Unix()), effects)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, photoInvalidErr()
	}
	photo = mutation.Current
	if botTarget.ID != 0 {
		return r.photosPhotoForBotTarget(ctx, userID, botTarget.ID, photo, kind)
	}
	return r.photosPhotoForSelf(ctx, userID, photo, kind)
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
	if err := r.requireAccountDelivery(userID, "photos.updateProfilePhoto"); err != nil {
		return nil, err
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
		base, err := r.profilePhotoDeliveryBase(ctx, userID, botTarget)
		if err != nil {
			return nil, err
		}
		effects := r.profilePhotoDeliveryEffects(ctx, userID, base, botTarget.ID != 0, kind)
		mutation, found, err := r.deps.Files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, targetUserID, kind, in.ID, int(r.clock.Now().Unix()), effects)
		if err != nil {
			return nil, internalErr()
		}
		if !found {
			return nil, photoInvalidErr()
		}
		photo := mutation.Current
		if botTarget.ID != 0 {
			return r.photosPhotoForBotTarget(ctx, userID, botTarget.ID, photo, kind)
		}
		return r.photosPhotoForSelf(ctx, userID, photo, kind)
	default:
		base, err := r.profilePhotoDeliveryBase(ctx, userID, botTarget)
		if err != nil {
			return nil, err
		}
		cur, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, targetUserID, kind)
		if err != nil {
			return nil, internalErr()
		}
		var photo domain.Photo
		if found {
			effects := r.profilePhotoDeliveryEffects(ctx, userID, base, botTarget.ID != 0, kind)
			mutation, err := r.deps.Files.DeleteProfilePhotosKind(ctx, domain.PeerTypeUser, targetUserID, kind, []int64{cur.ID}, effects)
			if err != nil {
				return nil, internalErr()
			}
			if mutation.HasCurrent {
				photo = mutation.Current
			}
		}
		if botTarget.ID != 0 {
			return r.photosPhotoForBotTarget(ctx, userID, botTarget.ID, photo, kind)
		}
		return r.photosPhotoForSelf(ctx, userID, photo, kind)
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
	if r.deps.Files == nil || r.deps.Users == nil || r.deps.Contacts == nil {
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
	if !req.GetSuggest() {
		if err := r.requireAccountDelivery(userID, "photos.uploadContactProfilePhoto"); err != nil {
			return nil, err
		}
	}
	if mediaFlags == 0 && req.GetSave() {
		if _, err := r.deps.Contacts.ClearPersonalPhotoWithDelivery(
			ctx, userID, target.ID, int(r.clock.Now().Unix()),
			r.contactPersonalPhotoDeliveryEffects(ctx, target.ID),
		); err != nil {
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
		_, err := r.sendSuggestedProfilePhotoMessage(ctx, userID, target.ID, photo)
		if err != nil {
			return nil, err
		}
		return r.photosPhotoForUser(ctx, userID, target.ID, photo), nil
	}
	if _, err := r.deps.Contacts.SetPersonalPhotoWithDelivery(
		ctx, userID, target.ID, photo, int(r.clock.Now().Unix()),
		r.contactPersonalPhotoDeliveryEffects(ctx, target.ID),
	); err != nil {
		return nil, contactErr(err)
	}
	r.invalidateRPCProjectionForPeer(userID, domain.Peer{Type: domain.PeerTypeUser, ID: target.ID})
	return r.photosPhotoForUserWithPersonalPhoto(ctx, userID, target.ID, photo), nil
}

func (r *Router) contactPersonalPhotoDeliveryEffects(ctx context.Context, targetUserID int64) store.DeliveryEffectsBuilder[store.ContactPersonalPhotoDeliverySnapshot] {
	excludeAuthKeyID, excludeSessionID := deliveryExclusionFromContext(ctx)
	return func(snapshot store.ContactPersonalPhotoDeliverySnapshot) ([]store.DeliveryEffect, error) {
		if snapshot.ContactUserID != targetUserID {
			return nil, store.ErrDeliveryOutboxRequired
		}
		payload, err := encodeDeliveryUpdate(&tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: targetUserID}},
			Users:   []tg.UserClass{}, Chats: []tg.ChatClass{},
			Date: int(r.clock.Now().Unix()), Seq: 0,
		})
		if err != nil {
			return nil, err
		}
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID:     snapshot.ViewerUserID,
			ExcludeAuthKeyID: excludeAuthKeyID, ExcludeSessionID: excludeSessionID,
			Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}
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
		Date: int(r.clock.Now().Unix()),
		// photos.photo does not echo the created sender-box message or its PTS.
		// Keep the origin in the transactional dispatch audience so the current
		// session consumes the same durable event as every other device.
		OriginAuthKeyID:  [8]byte{},
		OriginSessionID:  0,
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
	// 冻结/已删除账号对观察者呈 deleted 墓碑：主头像已塌缩为「已删除账号」占位，
	// 相册必须一并清空，否则资料页同时出现墓碑占位与用户真实照片两枚头像。
	// 相册数据保留在 Files 存储（drop 会破坏解冻恢复），墓碑是视图期投影，
	// 解冻后此处自然放行、真实照片原样恢复。相册数据不被删除。
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
	// 打开自己的头像相册（inputUserSelf 或显式自己 ID）时 target 即 viewer，须带 self
	// 标志，否则 self=false 的自己 user 会污染 DrKLO 账号缓存（Saved Messages 变身）。
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
	if err := r.requireAccountDelivery(userID, "photos.deletePhotos"); err != nil {
		return nil, err
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
	base, err := r.profilePhotoDeliveryBase(ctx, userID, domain.User{})
	if err != nil {
		return nil, err
	}
	effects := r.profilePhotoDeliveryEffects(ctx, userID, base, false, domain.ProfilePhotoKindProfile)
	mutation, err := r.deps.Files.DeleteProfilePhotos(ctx, domain.PeerTypeUser, userID, ids, effects)
	if err != nil {
		return nil, internalErr()
	}
	r.invalidateRPCProjectionForUser(userID)
	// 删除可能撤掉当前头像（回落到下一张或无头像）：与 uploadProfilePhoto 同一纪律，
	// 向全部在线 session（含当前）推 updateUser + fresh self（对齐参考实现 deletePhotos 的
	// SyncPushUpdates），否则其它设备与当前设备（DrKLO 本地删除逻辑同样不重建 has_video）
	// 头像停留在旧状态。
	_ = mutation
	return ids, nil
}

// photosPhotoForSelf 组装 photos.photo 响应（新照片 + 带头像的 self user），并在头像变更后
// 向该账号其它在线设备推送，使其即时刷新头像。仅由 uploadProfilePhoto / updateProfilePhoto
// 等头像变更路径调用（只读路径不得使用，否则会误触发推送）。
func (r *Router) photosPhotoForSelf(ctx context.Context, userID int64, photo domain.Photo, kind domain.ProfilePhotoKind) (*tg.PhotosPhoto, error) {
	out := &tg.PhotosPhoto{Photo: tgPhoto(photo), Users: []tg.UserClass{}}
	r.invalidateRPCProjectionForUser(userID)
	if r.deps.Users == nil {
		return out, nil
	}
	self, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return out, nil
	}
	if kind == domain.ProfilePhotoKindProfile {
		applyProfilePhotoToUser(&self, photo)
	}
	projected := r.tgSelfUser(self)
	r.applyUsernamesToPeerObjects(ctx, []tg.UserClass{projected}, nil)
	out.Users = append(out.Users, projected)
	return out, nil
}

func (r *Router) photosPhotoForBotTarget(ctx context.Context, viewerUserID, botUserID int64, photo domain.Photo, kind domain.ProfilePhotoKind) (*tg.PhotosPhoto, error) {
	out := &tg.PhotosPhoto{Photo: tgPhoto(photo), Users: []tg.UserClass{}}
	r.invalidateRPCProjectionForUser(botUserID)
	if r.deps.Users == nil {
		return out, nil
	}
	bot, found, err := r.deps.Users.ByID(ctx, viewerUserID, botUserID)
	if err != nil || !found {
		return out, nil
	}
	if kind == domain.ProfilePhotoKindProfile {
		applyProfilePhotoToUser(&bot, photo)
	}
	projected := r.tgUser(bot)
	projected.SetBotCanEdit(true)
	r.applyUsernamesToPeerObjects(ctx, []tg.UserClass{projected}, nil)
	out.Users = append(out.Users, projected)
	return out, nil
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

func (r *Router) profilePhotoDeliveryBase(ctx context.Context, viewerUserID int64, bot domain.User) (domain.User, error) {
	if r.deps.Users == nil {
		return domain.User{}, internalErr()
	}
	if bot.ID != 0 {
		return bot, nil
	}
	self, err := r.deps.Users.Self(ctx, viewerUserID)
	if err != nil || self.ID == 0 {
		return domain.User{}, internalErr()
	}
	return self, nil
}

func (r *Router) profilePhotoDeliveryEffects(ctx context.Context, recipientUserID int64, base domain.User, botTarget bool, kind domain.ProfilePhotoKind) store.DeliveryEffectsBuilder[store.ProfilePhotoMutation] {
	var baseProjection *tg.User
	if botTarget {
		baseProjection = r.tgUser(base)
		baseProjection.SetBotCanEdit(true)
		r.applyUsernamesToPeerObjects(ctx, []tg.UserClass{baseProjection}, nil)
	} else {
		baseProjection = r.tgSelfUserWithUsernames(ctx, base)
	}
	usernames, hasUsernames := baseProjection.GetUsernames()
	usernames = append([]tg.Username(nil), usernames...)
	deliveryDate := int(r.clock.Now().Unix())
	return func(snapshot store.ProfilePhotoMutation) ([]store.DeliveryEffect, error) {
		if snapshot.OwnerType != domain.PeerTypeUser || snapshot.OwnerID != base.ID || snapshot.Kind != kind {
			return nil, errors.New("profile photo delivery snapshot does not match target")
		}
		projectedUser := base
		if kind == domain.ProfilePhotoKindProfile {
			if snapshot.HasCurrent {
				applyProfilePhotoToUser(&projectedUser, snapshot.Current)
			} else {
				applyProfilePhotoToUser(&projectedUser, domain.Photo{})
			}
		}
		var projected *tg.User
		if botTarget {
			projected = r.tgUser(projectedUser)
			projected.SetBotCanEdit(true)
		} else {
			projected = r.tgSelfUser(projectedUser)
		}
		if hasUsernames {
			projected.SetUsernames(append([]tg.Username(nil), usernames...))
		}
		payload, err := encodeDeliveryUpdate(selfPhotoUpdates(projectedUser, deliveryDate, projected))
		if err != nil {
			return nil, err
		}
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: recipientUserID,
			Payload:      payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}
}

// ProfilePhotoDeliveryEffects is the admin aggregate boundary. It returns the
// same immutable self-user projection used by photos.* without exposing TL
// types outside RPC.
func (r *Router) ProfilePhotoDeliveryEffects(ctx context.Context, user domain.User) store.DeliveryEffectsBuilder[store.ProfilePhotoMutation] {
	return r.profilePhotoDeliveryEffects(ctx, user.ID, user, false, domain.ProfilePhotoKindProfile)
}

// ProfilePhotoSeedEffects is the startup boundary for embedded avatars of
// built-in accounts. It reuses the admin self-user projection, so a seeded
// avatar and an operator-set avatar commit the same durable delivery fact.
func (r *Router) ProfilePhotoSeedEffects(ctx context.Context, userID int64) (store.DeliveryEffectsBuilder[store.ProfilePhotoMutation], error) {
	if r.deps.Users == nil {
		return nil, errors.New("profile photo seed: users dependency is not configured")
	}
	user, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user.ID != userID {
		return nil, domain.ErrUserNotFound
	}
	return r.ProfilePhotoDeliveryEffects(ctx, user), nil
}

func (r *Router) ProfilePhotoCommitted(userID int64) {
	r.invalidateRPCProjectionForUser(userID)
}

func selfPhotoUpdates(self domain.User, date int, user tg.UserClass) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateUser{UserID: self.ID}},
		Users:   []tg.UserClass{user},
		Date:    date,
	}
}

// uploadedFileRef 把 tg.InputFile / InputFileBig 转成 domain.UploadedFileRef。
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

// resolveInputChatPhoto 把 tg.InputChatPhoto 解析为 *domain.Photo（nil=清除头像）。
// 支持新上传（InputChatUploadedPhoto）与引用已有照片（InputChatPhoto{InputPhoto}）。
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
			return nil, nil // InputPhotoEmpty → 清除
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
