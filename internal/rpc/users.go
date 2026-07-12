package rpc

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"

	"telesrv/internal/app/users"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

const maxSavedMusicLimit = 100
const maxRequirementsToContactUsers = 100

// registerUsers 注册 users.* RPC handler。
func (r *Router) registerUsers(d *tg.ServerDispatcher) {
	d.OnUsersGetUsers(r.onUsersGetUsers)
	d.OnUsersGetFullUser(r.onUsersGetFullUser)
	d.OnUsersGetRequirementsToContact(r.onUsersGetRequirementsToContact)
	d.OnUsersGetSavedMusic(r.onUsersGetSavedMusic)
	d.OnUsersGetSavedMusicByID(r.onUsersGetSavedMusicByID)
}

// onUsersGetUsers 处理 users.getUsers：支持 self 和已知 user peer（含 777000 官方账号）。
func (r *Router) onUsersGetUsers(ctx context.Context, ids []tg.InputUserClass) ([]tg.UserClass, error) {
	currentUserID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Users == nil {
		return []tg.UserClass{}, nil
	}
	type userInput struct {
		self       bool
		userID     int64
		accessHash int64
	}
	inputs := make([]userInput, 0, len(ids))
	uniqueUserIDs := make([]int64, 0, len(ids))
	seenUserIDs := make(map[int64]struct{}, len(ids))
	needSelf := false
	for _, in := range ids {
		switch v := in.(type) {
		case *tg.InputUserSelf:
			if !authorized {
				continue
			}
			needSelf = true
			inputs = append(inputs, userInput{self: true})
		case *tg.InputUser:
			if !authorized || v.UserID == 0 {
				continue
			}
			inputs = append(inputs, userInput{userID: v.UserID, accessHash: v.AccessHash})
			if _, ok := seenUserIDs[v.UserID]; ok {
				continue
			}
			seenUserIDs[v.UserID] = struct{}{}
			uniqueUserIDs = append(uniqueUserIDs, v.UserID)
		}
	}
	var selfUser domain.User
	if needSelf {
		selfUser, err = r.deps.Users.Self(ctx, currentUserID)
		if err != nil {
			if errors.Is(err, users.ErrNotAuthorized) {
				needSelf = false
			} else {
				return nil, internalErr()
			}
		} else if selfUser.ID == 0 {
			needSelf = false
		}
	}
	usersByID := make(map[int64]domain.User, len(uniqueUserIDs))
	if len(uniqueUserIDs) > 0 {
		list, err := r.deps.Users.ByIDs(ctx, currentUserID, uniqueUserIDs)
		if err != nil {
			if !errors.Is(err, users.ErrNotAuthorized) {
				return nil, internalErr()
			}
		}
		for _, u := range list {
			if u.ID != 0 {
				usersByID[u.ID] = u
			}
		}
	}
	out := make([]tg.UserClass, 0, len(ids))
	for _, in := range inputs {
		if in.self {
			if needSelf {
				out = append(out, r.tgSelfUser(selfUser))
			}
			continue
		}
		u, found := usersByID[in.userID]
		if !found || (in.accessHash != 0 && in.accessHash != u.AccessHash) {
			continue
		}
		// 客户端也可能用显式自己 ID（非 inputUserSelf）请求；同样须带 self 标志，
		// 否则 self=false 的自己 user 会污染 DrKLO 账号缓存（Saved Messages 变身）。
		if u.ID == currentUserID {
			out = append(out, r.tgSelfUser(u))
			continue
		}
		out = append(out, r.tgUser(u))
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, currentUserID, out, nil)
	return out, nil
}

func (r *Router) onUsersGetFullUser(ctx context.Context, id tg.InputUserClass) (*tg.UsersUserFull, error) {
	if r.deps.Users == nil {
		return emptyUserFull(), nil
	}
	currentUserID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	u, found, err := r.userFromInput(ctx, currentUserID, id)
	if err != nil {
		if errors.Is(err, users.ErrNotAuthorized) {
			return emptyUserFull(), nil
		}
		return nil, internalErr()
	}
	if !found {
		return emptyUserFull(), nil
	}
	user := r.tgUser(u)
	if _, ok := id.(*tg.InputUserSelf); ok || u.ID == currentUserID {
		user = r.tgSelfUser(u)
	}
	if err := r.applyBotCanEditToUser(ctx, currentUserID, u, user); err != nil {
		return nil, err
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, currentUserID, []tg.UserClass{user}, nil)
	loadEpoch := r.userFullProjectionCache.LoadEpoch()
	if full, ok := r.userFullProjectionCache.Lookup(currentUserID, u.ID); ok {
		if err := r.applyTranslationDisabledToUserFull(ctx, currentUserID, u.ID, &full); err != nil {
			return nil, err
		}
		r.applyStoriesPinnedAvailableToUserFull(ctx, currentUserID, u.ID, &full)
		r.applyNotifySettingsToUserFull(ctx, currentUserID, u.ID, &full)
		chats := r.applyPersonalChannelToUserFull(ctx, currentUserID, u.PersonalChannelID, &full)
		return &tg.UsersUserFull{
			FullUser: full,
			Users:    []tg.UserClass{user},
			Chats:    chats,
		}, nil
	}
	full, err := r.buildUserFullProjection(ctx, currentUserID, u)
	if err != nil {
		return nil, err
	}
	r.userFullProjectionCache.StoreIfEpoch(currentUserID, u.ID, full, loadEpoch)
	if err := r.applyTranslationDisabledToUserFull(ctx, currentUserID, u.ID, &full); err != nil {
		return nil, err
	}
	r.applyStoriesPinnedAvailableToUserFull(ctx, currentUserID, u.ID, &full)
	r.applyNotifySettingsToUserFull(ctx, currentUserID, u.ID, &full)
	chats := r.applyPersonalChannelToUserFull(ctx, currentUserID, u.PersonalChannelID, &full)
	return &tg.UsersUserFull{
		FullUser: full,
		Users:    []tg.UserClass{user},
		Chats:    chats,
	}, nil
}

func (r *Router) buildUserFullProjection(ctx context.Context, currentUserID int64, u domain.User) (tg.UserFull, error) {
	about := u.About
	if r.deps.Privacy != nil && u.ID != currentUserID {
		allowed, err := r.deps.Privacy.CanSee(ctx, u.ID, currentUserID, domain.PrivacyKeyAbout)
		if err != nil {
			return tg.UserFull{}, internalErr()
		}
		if !allowed {
			about = ""
		}
	}
	full := tg.UserFull{
		ID:             u.ID,
		About:          about,
		Settings:       tg.PeerSettings{},
		NotifySettings: *tdesktop.NotifySettings(),
	}
	// 通话入口：客户端不见 phone_calls_available=true 不显示通话按钮（P1 前置项）。
	// phone_calls_private 标记对端禁 P2P（p2p_allowed 真值在通话确认时另行计算）。
	if !u.Bot && u.ID != currentUserID {
		callsAllowed, p2pAllowed := true, true
		if r.deps.Privacy != nil {
			allowed, err := r.deps.Privacy.CanSee(ctx, u.ID, currentUserID, domain.PrivacyKeyPhoneCall)
			if err != nil {
				return tg.UserFull{}, internalErr()
			}
			callsAllowed = allowed
			allowed, err = r.deps.Privacy.CanSee(ctx, u.ID, currentUserID, domain.PrivacyKeyPhoneP2P)
			if err != nil {
				return tg.UserFull{}, internalErr()
			}
			p2pAllowed = allowed
		}
		full.PhoneCallsAvailable = callsAllowed
		full.VideoCallsAvailable = callsAllowed
		full.PhoneCallsPrivate = !p2pAllowed
	}
	if u.Bot {
		full.SetBotInfo(r.tgBotInfo(ctx, u))
	}
	if r.deps.Dialogs != nil && u.ID != currentUserID {
		list, err := r.deps.Dialogs.GetPeerDialogs(ctx, currentUserID, []domain.Peer{{Type: domain.PeerTypeUser, ID: u.ID}})
		if err != nil {
			return tg.UserFull{}, internalErr()
		}
		for _, dialog := range list.Dialogs {
			if dialog.Peer.Type != domain.PeerTypeUser || dialog.Peer.ID != u.ID {
				continue
			}
			if dialog.HasScheduled {
				full.SetHasScheduled(true)
			}
			if dialog.TTLPeriod > 0 {
				full.SetTTLPeriod(dialog.TTLPeriod)
			}
			if dialog.ThemeEmoticon != "" {
				full.SetTheme(&tg.ChatTheme{Emoticon: dialog.ThemeEmoticon})
			}
			break
		}
	}
	if err := r.fillUserFullPhotos(ctx, currentUserID, u.ID, &full); err != nil {
		return tg.UserFull{}, err
	}
	if r.deps.Account != nil {
		allowed, err := r.canSeeSavedMusic(ctx, currentUserID, u.ID)
		if err != nil {
			return tg.UserFull{}, err
		}
		if allowed {
			music, err := r.deps.Account.ListSavedMusic(ctx, u.ID, 0, 1)
			if err != nil {
				return tg.UserFull{}, internalErr()
			}
			if len(music.Documents) > 0 {
				full.SetSavedMusic(tgDocument(music.Documents[0]))
			}
		}
	}
	if svc, ok := r.accountBusinessAutomation(); ok {
		profile, found, err := svc.GetBusinessProfile(ctx, u.ID)
		if err != nil {
			return tg.UserFull{}, internalErr()
		}
		if found {
			r.applyBusinessProfileToUserFull(ctx, &full, profile)
		}
	}
	if r.deps.Messages != nil {
		// 置顶发现链路：userFull.pinned_msg_id 是该私聊（含 Saved
		// Messages）当前最顶置顶；客户端凭此触发 filterPinned 搜索。
		pinnedList, err := r.deps.Messages.Search(ctx, currentUserID, domain.MessageFilter{
			HasPeer:    true,
			Peer:       domain.Peer{Type: domain.PeerTypeUser, ID: u.ID},
			PinnedOnly: true,
			Limit:      1,
		})
		if err != nil {
			return tg.UserFull{}, internalErr()
		}
		if len(pinnedList.Messages) > 0 {
			full.SetPinnedMsgID(pinnedList.Messages[0].ID)
		}
		// 私聊双方恒可置顶（官方对 user peer 置位 can_pin_message）。
		full.SetCanPinMessage(true)
	}
	if r.deps.Channels != nil && u.ID != currentUserID {
		common, err := r.deps.Channels.CommonChannels(ctx, currentUserID, domain.CommonChannelsRequest{
			UserID:       currentUserID,
			TargetUserID: u.ID,
			Limit:        1,
			CountOnly:    true,
		})
		if err != nil {
			return tg.UserFull{}, internalErr()
		}
		full.CommonChatsCount = common.Count
	}
	// star gift 数量：客户端把资料页 Gifts 区段/标签页门控在 stargifts_count>0
	//（DrKLO ProfileActivity:10497 / TDesktop data_user.cpp:924），不下发则收到的礼物
	// 不在资料页展示。计展示在资料的礼物数（非转换、非隐藏）。
	if r.deps.Gifts != nil {
		if n, err := r.deps.Gifts.CountSaved(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}); err == nil && n > 0 {
			full.SetStargiftsCount(n)
		}
	}
	// 屏蔽 premium 礼物赠送：telesrv 未实现 payments.getPremiumGiftCodeOptions，
	// DrKLO GiftSheet 对个人送礼时总会渲染一个「Gift Premium」区段（fillItems:918），
	// premiumTiers 永远空 → 卡死成三个 flicker 占位骨架。设 disallow_premium_gifts=true
	// 会让该区段整体隐藏（GiftSheet:918 gate），而 star gift 礼物墙不受影响。
	// 仅对「他人视角」下发：送礼给自己时该区段本就被 !self 排除（无占位），故 self 投影
	// 保持干净，避免用户自己的礼物隐私设置里「接收 premium 礼物」被显示为关闭。
	// 务必只设这一个 flag——四个 disallow 全置会触发 GiftSheet:788 整个送礼弹窗自动 dismiss。
	if u.ID != currentUserID {
		var disallow tg.DisallowedGiftsSettings
		disallow.SetDisallowPremiumGifts(true)
		full.SetDisallowedGifts(disallow)
	}
	// 生日（account.updateBirthday）：落 userFull.birthday，按 PrivacyKeyBirthday 对他人裁剪，
	// 本人恒可见。
	if u.Birthday.IsSet() {
		birthdayVisible := true
		if r.deps.Privacy != nil && u.ID != currentUserID {
			allowed, err := r.deps.Privacy.CanSee(ctx, u.ID, currentUserID, domain.PrivacyKeyBirthday)
			if err != nil {
				return tg.UserFull{}, internalErr()
			}
			birthdayVisible = allowed
		}
		if birthdayVisible {
			full.SetBirthday(tgBirthday(u.Birthday))
		}
	}
	// 个人频道（account.updatePersonalChannel）不在此落地：它按 viewer 实时解析，作为缓存后的
	// overlay 处理（applyPersonalChannelToUserFull），避免烤进 per-(viewer,target) 投影缓存以及
	// build/chats 两次解析同一频道。
	return full, nil
}

// tgBirthday 把 domain 生日转 tg.Birthday（Year 可选，0 表示不含年份）。
func tgBirthday(b domain.Birthday) tg.Birthday {
	out := tg.Birthday{Day: b.Day, Month: b.Month}
	if b.Year != 0 {
		out.SetYear(b.Year)
	}
	return out
}

// applyPersonalChannelToUserFull 一次解析资料页个人频道：设 personal_channel_id + 最新一帖 id
// 到 userFull，并返回频道对象供响应 chats；频道不可解析/已删则不下发。放在 userFull 投影缓存
// 之后做（与 notify/stories overlay 同构）：个人频道可访问性随 viewer 变化须实时解析，且避免
// build（缓存 miss）与 chats 两次解析同一频道。personalChannelID 取自当次加载的 owner 用户。
func (r *Router) applyPersonalChannelToUserFull(ctx context.Context, viewerUserID, personalChannelID int64, full *tg.UserFull) []tg.ChatClass {
	if personalChannelID == 0 || r.deps.Channels == nil {
		return nil
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, viewerUserID, personalChannelID)
	if err != nil || view.Channel.ID == 0 || view.Channel.Deleted {
		return nil
	}
	full.SetPersonalChannelID(view.Channel.ID)
	full.SetPersonalChannelMessage(view.Channel.TopMessageID)
	return []tg.ChatClass{tgChannelChatForView(viewerUserID, view)}
}

func (r *Router) applyBotCanEditToUser(ctx context.Context, currentUserID int64, u domain.User, user *tg.User) error {
	if !u.Bot || r.deps.Bots == nil || currentUserID == 0 || currentUserID == u.ID || user == nil {
		return nil
	}
	// owner 视角置 bot_can_edit(flags2.1)：TDesktop/DrKLO 据此显示 bot 管理 UI
	//（改名/简介/头像入口）。仅 owner 看得到。
	owns, err := r.deps.Bots.OwnsBot(ctx, currentUserID, u.ID)
	if err != nil {
		return nil
	}
	if owns {
		user.SetBotCanEdit(true)
	}
	return nil
}

func (r *Router) onUsersGetRequirementsToContact(ctx context.Context, ids []tg.InputUserClass) ([]tg.RequirementToContactClass, error) {
	if len(ids) > maxRequirementsToContactUsers {
		return nil, limitInvalidErr()
	}
	out := make([]tg.RequirementToContactClass, 0, len(ids))
	for range ids {
		out = append(out, &tg.RequirementToContactEmpty{})
	}
	return out, nil
}

func (r *Router) onUsersGetSavedMusic(ctx context.Context, req *tg.UsersGetSavedMusicRequest) (tg.UsersSavedMusicClass, error) {
	if req == nil || req.Offset < 0 || req.Limit < 0 || req.Limit > maxSavedMusicLimit {
		return nil, limitInvalidErr()
	}
	currentUserID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	owner, found, err := r.userFromInput(ctx, currentUserID, req.ID)
	if err != nil {
		if errors.Is(err, users.ErrNotAuthorized) {
			return emptyUsersSavedMusic(), nil
		}
		return nil, internalErr()
	}
	if !found {
		return nil, userIDInvalidErr()
	}
	allowed, err := r.canSeeSavedMusic(ctx, currentUserID, owner.ID)
	if err != nil {
		return nil, err
	}
	if !allowed || r.deps.Account == nil {
		return emptyUsersSavedMusic(), nil
	}
	list, err := r.deps.Account.ListSavedMusic(ctx, owner.ID, req.Offset, req.Limit)
	if err != nil {
		return nil, internalErr()
	}
	if req.Hash != 0 && req.Offset == 0 && int64(tdesktopCountHash(savedMusicDocumentIDs(list.Documents))) == req.Hash {
		return &tg.UsersSavedMusicNotModified{Count: list.Count}, nil
	}
	return &tg.UsersSavedMusic{
		Count:     list.Count,
		Documents: tgDocuments(list.Documents),
	}, nil
}

func (r *Router) onUsersGetSavedMusicByID(ctx context.Context, req *tg.UsersGetSavedMusicByIDRequest) (tg.UsersSavedMusicClass, error) {
	if req == nil || len(req.Documents) > maxSavedMusicLimit {
		return nil, limitInvalidErr()
	}
	currentUserID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	owner, found, err := r.userFromInput(ctx, currentUserID, req.ID)
	if err != nil {
		if errors.Is(err, users.ErrNotAuthorized) {
			return emptyUsersSavedMusic(), nil
		}
		return nil, internalErr()
	}
	if !found {
		return nil, userIDInvalidErr()
	}
	allowed, err := r.canSeeSavedMusic(ctx, currentUserID, owner.ID)
	if err != nil {
		return nil, err
	}
	if !allowed || r.deps.Account == nil || len(req.Documents) == 0 {
		return emptyUsersSavedMusic(), nil
	}
	ids := make([]int64, 0, len(req.Documents))
	accessHashes := make(map[int64]int64, len(req.Documents))
	for _, input := range req.Documents {
		doc, ok := input.(*tg.InputDocument)
		if !ok || doc.ID == 0 {
			return nil, documentInvalidErr()
		}
		ids = append(ids, doc.ID)
		if _, seen := accessHashes[doc.ID]; !seen {
			accessHashes[doc.ID] = doc.AccessHash
		}
	}
	list, err := r.deps.Account.GetSavedMusicByIDs(ctx, owner.ID, ids)
	if err != nil {
		return nil, internalErr()
	}
	docs := list.Documents[:0]
	for _, doc := range list.Documents {
		if doc.AccessHash == accessHashes[doc.ID] && doc.IsMusic() {
			docs = append(docs, doc)
		}
	}
	return &tg.UsersSavedMusic{
		Count:     list.Count,
		Documents: tgDocuments(docs),
	}, nil
}

func emptyUsersSavedMusic() *tg.UsersSavedMusic {
	return &tg.UsersSavedMusic{
		Count:     0,
		Documents: []tg.DocumentClass{},
	}
}

func (r *Router) canSeeSavedMusic(ctx context.Context, viewerUserID, ownerUserID int64) (bool, error) {
	if viewerUserID == 0 || ownerUserID == 0 || viewerUserID == ownerUserID || r.deps.Privacy == nil {
		return true, nil
	}
	allowed, err := r.deps.Privacy.CanSee(ctx, ownerUserID, viewerUserID, domain.PrivacyKeySavedMusic)
	if err != nil {
		return false, internalErr()
	}
	return allowed, nil
}

func savedMusicDocumentIDs(docs []domain.Document) []int64 {
	ids := make([]int64, 0, len(docs))
	for _, doc := range docs {
		ids = append(ids, doc.ID)
	}
	return ids
}

func (r *Router) fillUserFullPhotos(ctx context.Context, viewerUserID, ownerUserID int64, full *tg.UserFull) error {
	if r.deps.Files == nil || full == nil || ownerUserID == 0 {
		return nil
	}
	if viewerUserID == ownerUserID {
		if photo, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, ownerUserID, domain.ProfilePhotoKindProfile); err != nil {
			return internalErr()
		} else if found {
			full.SetProfilePhoto(tgPhoto(photo))
		}
		if photo, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, ownerUserID, domain.ProfilePhotoKindFallback); err != nil {
			return internalErr()
		} else if found {
			full.SetFallbackPhoto(tgPhoto(photo))
		}
		return nil
	}
	if r.deps.Contacts != nil {
		refs, err := r.deps.Contacts.PersonalPhotos(ctx, viewerUserID, []int64{ownerUserID})
		if err != nil {
			return internalErr()
		}
		if ref, ok := refs[ownerUserID]; ok && ref.PhotoID != 0 {
			photo, found, err := r.deps.Files.GetPhoto(ctx, ref.PhotoID)
			if err != nil {
				return internalErr()
			}
			if found {
				full.SetPersonalPhoto(tgPhoto(photo))
			}
		}
	}
	profileAllowed := true
	if r.deps.Privacy != nil {
		var err error
		profileAllowed, err = r.deps.Privacy.CanSee(ctx, ownerUserID, viewerUserID, domain.PrivacyKeyProfilePhoto)
		if err != nil {
			return internalErr()
		}
	}
	if profileAllowed {
		if photo, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, ownerUserID, domain.ProfilePhotoKindProfile); err != nil {
			return internalErr()
		} else if found {
			full.SetProfilePhoto(tgPhoto(photo))
		}
		return nil
	}
	if photo, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, ownerUserID, domain.ProfilePhotoKindFallback); err != nil {
		return internalErr()
	} else if found {
		full.SetFallbackPhoto(tgPhoto(photo))
	}
	return nil
}

// tgBotInfo 构造 userFull.bot_info。bot 用户必须携带（哪怕命令为空）：缺失时
// TDesktop 的 botInfo.inited 永不置位，每次开聊/输 "/" 都会重拉 getFullUser；
// user_id 必填且必须等于该 bot 的 id，不匹配会被客户端整体静默忽略。
func (r *Router) tgBotInfo(ctx context.Context, u domain.User) tg.BotInfo {
	if r.deps.Bots == nil {
		return tgBotInfoFromProfile(u.ID, domain.BotProfile{}, false)
	}
	profile, found, err := r.deps.Bots.BotInfo(ctx, u.ID)
	if err != nil || !found {
		return tgBotInfoFromProfile(u.ID, domain.BotProfile{}, false)
	}
	return tgBotInfoFromProfile(u.ID, profile, true)
}

type botProfileBatchResolver interface {
	BotInfos(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error)
}

func (r *Router) tgBotInfos(ctx context.Context, userIDs []int64) []tg.BotInfo {
	if len(userIDs) == 0 {
		return nil
	}
	ids := uniquePeerIDs(userIDs)
	profiles := map[int64]domain.BotProfile{}
	if r.deps.Bots != nil {
		if batch, ok := r.deps.Bots.(botProfileBatchResolver); ok {
			if loaded, err := batch.BotInfos(ctx, ids); err == nil {
				profiles = loaded
			}
		} else {
			for _, id := range ids {
				if profile, found, err := r.deps.Bots.BotInfo(ctx, id); err == nil && found {
					profiles[id] = profile
				}
			}
		}
	}
	out := make([]tg.BotInfo, 0, len(userIDs))
	for _, id := range userIDs {
		profile, found := profiles[id]
		out = append(out, tgBotInfoFromProfile(id, profile, found))
	}
	return out
}

func tgBotInfoFromProfile(userID int64, profile domain.BotProfile, found bool) tg.BotInfo {
	info := tg.BotInfo{}
	info.SetUserID(userID)
	info.SetMenuButton(&tg.BotMenuButtonDefault{})
	if !found {
		return info
	}
	if profile.Description != "" {
		info.SetDescription(profile.Description)
	}
	if len(profile.Commands) > 0 {
		cmds := make([]tg.BotCommand, 0, len(profile.Commands))
		for _, c := range profile.Commands {
			cmds = append(cmds, tg.BotCommand{Command: c.Command, Description: c.Description})
		}
		info.SetCommands(cmds)
	}
	info.SetMenuButton(tgBotMenuButton(profile.MenuButton))
	if profile.HasPreviewMedias {
		info.SetHasPreviewMedias(true)
	}
	if profile.AppSettings != nil {
		info.SetAppSettings(tgBotAppSettings(*profile.AppSettings))
	}
	return info
}

func tgBotAppSettings(in domain.BotAppSettings) tg.BotAppSettings {
	out := tg.BotAppSettings{}
	if len(in.PlaceholderPath) > 0 {
		out.SetPlaceholderPath(append([]byte(nil), in.PlaceholderPath...))
	}
	if in.HasBackgroundColor {
		out.SetBackgroundColor(in.BackgroundColor)
	}
	if in.HasBackgroundDark {
		out.SetBackgroundDarkColor(in.BackgroundDarkColor)
	}
	if in.HasHeaderColor {
		out.SetHeaderColor(in.HeaderColor)
	}
	if in.HasHeaderDarkColor {
		out.SetHeaderDarkColor(in.HeaderDarkColor)
	}
	return out
}

func emptyUserFull() *tg.UsersUserFull {
	return &tg.UsersUserFull{
		FullUser: tg.UserFull{
			Settings:       tg.PeerSettings{},
			NotifySettings: *tdesktop.NotifySettings(),
		},
	}
}

func (r *Router) userFromInput(ctx context.Context, currentUserID int64, id tg.InputUserClass) (domain.User, bool, error) {
	switch v := id.(type) {
	case *tg.InputUserSelf:
		u, err := r.deps.Users.Self(ctx, currentUserID)
		return u, err == nil, err
	case *tg.InputUser:
		u, found, err := r.deps.Users.ByID(ctx, currentUserID, v.UserID)
		if err != nil || !found {
			return domain.User{}, found, err
		}
		if v.AccessHash != 0 && v.AccessHash != u.AccessHash {
			return domain.User{}, false, nil
		}
		return u, true, nil
	default:
		return domain.User{}, false, nil
	}
}

func (r *Router) validateInputUser(ctx context.Context, id tg.InputUserClass) error {
	if r.deps.Users == nil {
		return nil
	}
	currentUserID, _, err := r.currentUserID(ctx)
	if err != nil {
		return internalErr()
	}
	_, found, err := r.userFromInput(ctx, currentUserID, id)
	if err != nil {
		if errors.Is(err, users.ErrNotAuthorized) {
			return userIDInvalidErr()
		}
		return internalErr()
	}
	if !found {
		return userIDInvalidErr()
	}
	return nil
}
