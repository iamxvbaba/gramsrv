package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// registerPhotos 注册 photos.* RPC handler（头像上传 / 切换 / 查询 / 删除）。
func (r *Router) registerPhotos(d *tg.ServerDispatcher) {
	d.OnPhotosUploadProfilePhoto(r.onPhotosUploadProfilePhoto)
	d.OnPhotosUpdateProfilePhoto(r.onPhotosUpdateProfilePhoto)
	d.OnPhotosUploadContactProfilePhoto(r.onPhotosUploadContactProfilePhoto)
	d.OnPhotosGetUserPhotos(r.onPhotosGetUserPhotos)
	d.OnPhotosDeletePhotos(r.onPhotosDeletePhotos)
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
	if bot, hasBot := req.GetBot(); hasBot && bot != nil {
		return nil, inputConstructorInvalidErr()
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
	photo, found, err := r.deps.Files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, userID, kind, photo.ID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, photoInvalidErr()
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
	if bot, hasBot := req.GetBot(); hasBot && bot != nil {
		return nil, inputConstructorInvalidErr()
	}
	kind := domain.ProfilePhotoKindProfile
	if req.GetFallback() {
		kind = domain.ProfilePhotoKindFallback
	}
	switch in := req.ID.(type) {
	case *tg.InputPhoto:
		photo, found, err := r.deps.Files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, userID, kind, in.ID, int(r.clock.Now().Unix()))
		if err != nil {
			return nil, internalErr()
		}
		if !found {
			return nil, photoInvalidErr()
		}
		return r.photosPhotoForSelf(ctx, userID, photo, kind), nil
	default:
		// InputPhotoEmpty：移除当前头像（停用现有当前照片）。
		if cur, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, userID, kind); err == nil && found {
			_, _ = r.deps.Files.DeleteProfilePhotosKind(ctx, domain.PeerTypeUser, userID, kind, []int64{cur.ID})
		}
		return r.photosPhotoForSelf(ctx, userID, domain.Photo{}, kind), nil
	}
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
	authKeyID, _ := AuthKeyIDFrom(ctx)
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
		OriginAuthKeyID:  authKeyID,
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
	photos, total, err := r.deps.Files.GetProfilePhotos(ctx, domain.PeerTypeUser, target.ID, offset, limit, req.MaxID)
	if err != nil {
		return nil, internalErr()
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
	r.applyStoryMaxIDsToPeerObjects(ctx, currentUserID, users, nil)
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
	// 删除可能撤掉当前头像（回落到下一张或无头像）：与 uploadProfilePhoto 同一纪律，
	// 向全部在线 session（含当前）推 updateUser + fresh self（对齐参考实现 deletePhotos 的
	// SyncPushUpdates），否则其它设备与当前设备（DrKLO 本地删除逻辑同样不重建 has_video）
	// 头像停留在旧状态。
	if deleted > 0 && r.deps.Users != nil {
		if self, err := r.deps.Users.Self(ctx, userID); err == nil {
			r.pushSelfPhotoUpdate(ctx, self)
		}
	}
	return ids, nil
}

// photosPhotoForSelf 组装 photos.photo 响应（新照片 + 带头像的 self user），并在头像变更后
// 向该账号其它在线设备推送，使其即时刷新头像。仅由 uploadProfilePhoto / updateProfilePhoto
// 等头像变更路径调用（只读路径不得使用，否则会误触发推送）。
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
	out.Users = append(out.Users, r.tgSelfUser(self))
	r.pushSelfPhotoUpdate(ctx, self)
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

// pushSelfPhotoUpdate 向该账号全部在线设备（含当前 session）推送头像变更，对齐参考实现
// SyncPushUpdates 的全 session 语义。updateUserName 不含 photo 无法刷新头像；updateUser 只是
// 「该 user 变了」的信号（TDesktop 仅当 peer 已 full-loaded 时才 forceFull 重拉）；最可靠是在
// Updates.Users 带上含新 userProfilePhoto 的完整 self user，TDesktop 经
// processUser→setPhoto→peerUpdated(Photo) 即时刷新。
//
// 当前 session 不能只依赖 RPC 返回：DrKLO Android 的 uploadProfilePhoto 响应回调
// （ProfileActivity/SettingsActivity.didUploadPhoto、PhotoUtilities）不消费
// photos.photo.users，而是手工重建 userProfilePhoto——只填 photo_id/photo_small/photo_big，
// 丢掉 has_video/stripped_thumb；emoji/sticker markup 头像的本地渲染
// （ImageReceiver.setForUserOrChat → VectorAvatarThumbDrawable）要求
// user.photo.has_video==true，缺推送时该设备要等到下一次拿到 fresh self user（通常是重启）
// 才会显示 emoji 头像。当前 session 的回显延迟发送，保证到达时客户端已处理完自己的
// RPC 响应回调（否则 Android 回调会把推送刚修好的 photo 再次覆盖回无 has_video 的形状）。
func (r *Router) pushSelfPhotoUpdate(ctx context.Context, self domain.User) {
	if self.ID == 0 {
		return
	}
	updates := selfPhotoUpdates(self, int(r.clock.Now().Unix()), r.tgSelfUser(self))
	r.pushUserUpdates(ctx, self.ID, updates)
	r.pushSelfPhotoUpdateToCurrentSession(ctx, updates)
}

// defaultSelfPhotoEchoPushDelay 是头像变更后向当前 session 回显 updateUser 的延迟：
// 必须晚于 RPC 结果写出与客户端响应回调，否则 DrKLO 的手工 photo 重建会覆盖回显内容。
// updateUser 无 pts，晚到/丢失不影响 difference 正确性。
const defaultSelfPhotoEchoPushDelay = 500 * time.Millisecond

// pushSelfPhotoUpdateToCurrentSession 延迟向当前 session 回显头像变更 updates。
// ctx 值在调度前捕获（请求 ctx 在 handler 返回后即失效）。
func (r *Router) pushSelfPhotoUpdateToCurrentSession(ctx context.Context, updates *tg.Updates) {
	if r.deps.Sessions == nil || updates == nil {
		return
	}
	sessionID, ok := SessionIDFrom(ctx)
	if !ok {
		return
	}
	rawAuthKeyID, hasRawAuthKeyID := RawAuthKeyIDFrom(ctx)
	push := func() {
		pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if scoped, ok := r.scopedSessions(); ok {
			if !hasRawAuthKeyID {
				return
			}
			if err := scoped.PushToSessionForAuthKey(pushCtx, rawAuthKeyID, sessionID, proto.MessageFromServer, updates); err != nil {
				r.log.Debug("push self photo update to current session", zap.Int64("session_id", sessionID), zap.Error(err))
			}
			return
		}
		if err := r.deps.Sessions.PushToSession(pushCtx, sessionID, proto.MessageFromServer, updates); err != nil {
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
		file, ok := in.GetFile()
		if !ok {
			return nil, photoInvalidErr()
		}
		if r.deps.Files == nil {
			return nil, photoInvalidErr()
		}
		ref, ok := uploadedFileRef(userID, file)
		if !ok {
			return nil, fileReferenceInvalidErr()
		}
		// 频道/群头像用 avatar 尺寸（'a'/'c'），匹配 InputPeerPhotoFileLocation 下载路径。
		photo, err := r.deps.Files.CreateAvatarFromUpload(ctx, ref)
		if err != nil {
			return nil, photoUploadErr(err)
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
	default:
		return internalErr()
	}
}
