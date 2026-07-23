package rpc

import (
	"time"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

// tgSelfUser 把 domain.User 转为 self 标记的 tg.User（optional 字段由 Encode 自动 SetFlags）。
func tgSelfUser(u domain.User) *tg.User {
	if u.Deleted {
		return &tg.User{ID: u.ID, Deleted: true}
	}
	out := &tg.User{
		ID:            u.ID,
		AccessHash:    u.AccessHash,
		FirstName:     u.FirstName,
		LastName:      u.LastName,
		Username:      u.Username,
		Phone:         u.Phone,
		Self:          true,
		Verified:      u.Verified,
		Support:       u.Support,
		Contact:       u.Contact,
		MutualContact: u.Mutual,
		CloseFriend:   u.CloseFriend,
		Usernames:     tgUsernames(u.Username),
	}
	applyTgUserBotFields(out, u)
	applyTgUserPremiumFields(out, u)
	applyTgUserColorFields(out, u)
	applyTgUserRestrictionFields(out, u)
	if u.LinkedCommunityID != 0 {
		out.SetLinkedCommunityID(u.LinkedCommunityID)
	}
	if photo := tgUserProfilePhoto(u); photo != nil {
		out.Photo = photo
	}
	return out
}

func tgUser(u domain.User) *tg.User {
	if u.Deleted {
		return &tg.User{ID: u.ID, Deleted: true}
	}
	out := &tg.User{
		ID:            u.ID,
		AccessHash:    u.AccessHash,
		FirstName:     u.FirstName,
		LastName:      u.LastName,
		Username:      u.Username,
		Phone:         u.Phone,
		Verified:      u.Verified,
		Scam:          u.Scam,
		// scam and fake are mutually exclusive in Telegram; a peer flagged as
		// both would render neither badge on clients. scam takes precedence.
		Fake:          u.Fake && !u.Scam,
		Support:       u.Support,
		Contact:       u.Contact,
		MutualContact: u.Mutual,
		CloseFriend:   u.CloseFriend,
		Usernames:     tgUsernames(u.Username),
	}
	applyTgUserBotFields(out, u)
	applyTgUserPremiumFields(out, u)
	applyTgUserColorFields(out, u)
	applyTgUserRestrictionFields(out, u)
	if u.LinkedCommunityID != 0 {
		out.SetLinkedCommunityID(u.LinkedCommunityID)
	}
	if photo := tgUserProfilePhoto(u); photo != nil {
		out.Photo = photo
	}
	return out
}

func applyTgUserRestrictionFields(out *tg.User, u domain.User) {
	if out == nil || len(u.RestrictionReasons) == 0 {
		return
	}
	reasons := make([]tg.RestrictionReason, 0, len(u.RestrictionReasons))
	for _, reason := range u.RestrictionReasons {
		if reason.Platform == "" || reason.Reason == "" || reason.Text == "" {
			continue
		}
		reasons = append(reasons, tg.RestrictionReason{
			Platform: reason.Platform,
			Reason:   reason.Reason,
			Text:     reason.Text,
		})
	}
	if len(reasons) == 0 {
		return
	}
	out.Restricted = true
	out.SetRestrictionReason(reasons)
}

// applyTgUserPremiumFields 由到期时间即时派生 premium flag（bit28，独立位）与
// emoji status。判断用真实时钟：premium 的权威来源是 premium_expires_at 本身，
// 到期即停发，正确性不依赖后台 sweeper（它只负责清理与 updateUser 通知）；
// bot 永不带 premium（PremiumActiveAt 内排除）。
func applyTgUserPremiumFields(out *tg.User, u domain.User) {
	now := time.Now().Unix()
	if !u.PremiumActiveAt(now) {
		return
	}
	out.Premium = true
	if u.EmojiStatusActiveAt(now) {
		out.SetEmojiStatus(tgUserEmojiStatus(u, now))
	}
}

// tgUserEmojiStatus 把用户 emoji status 转为 TL：未设置/已失效返回
// emojiStatusEmpty（updateUserEmojiStatus 清除语义用）。user TL 内联输出仍由
// applyTgUserPremiumFields 控制（无状态时直接省略字段，对齐官方 user 编码）。
func tgUserEmojiStatus(u domain.User, now int64) tg.EmojiStatusClass {
	if !u.EmojiStatusActiveAt(now) {
		return &tg.EmojiStatusEmpty{}
	}
	return tgUserEmojiStatusValue(u.EmojiStatus())
}

// tgUserEmojiStatusValue converts an already validated absolute snapshot. It
// is shared by inline user projections and durable updateUserEmojiStatus.
func tgUserEmojiStatusValue(value domain.UserEmojiStatus) tg.EmojiStatusClass {
	if !value.Valid() || value.Empty() {
		return &tg.EmojiStatusEmpty{}
	}
	if collectible := value.Collectible; !collectible.Empty() {
		status := &tg.EmojiStatusCollectible{
			CollectibleID:     collectible.CollectibleID,
			DocumentID:        collectible.DocumentID,
			Title:             collectible.Title,
			Slug:              collectible.Slug,
			PatternDocumentID: collectible.PatternDocumentID,
			CenterColor:       collectible.CenterColor,
			EdgeColor:         collectible.EdgeColor,
			PatternColor:      collectible.PatternColor,
			TextColor:         collectible.TextColor,
		}
		if value.Until > 0 {
			status.SetUntil(value.Until)
		}
		return status
	}
	status := &tg.EmojiStatus{DocumentID: value.DocumentID}
	if value.Until > 0 {
		status.SetUntil(value.Until)
	}
	return status
}

func applyTgUserColorFields(out *tg.User, u domain.User) {
	if color := tgUserPeerColor(u.Color); color != nil {
		out.SetColor(color)
	}
	if color := tgUserPeerColor(u.ProfileColor); color != nil {
		out.SetProfileColor(color)
	}
}

func tgUserPeerColor(color domain.PeerColor) tg.PeerColorClass {
	if color.Empty() {
		return nil
	}
	out := &tg.PeerColor{}
	if color.HasColor {
		out.SetColor(color.Color)
	}
	if color.BackgroundEmojiID != 0 {
		out.SetBackgroundEmojiID(color.BackgroundEmojiID)
	}
	return out
}

// applyTgUserBotFields 应用 bot/普通用户的互斥输出：
//   - bot：必须同时置 bot flag 与 bot_info_version（Layer 225 两者共用 flags
//     bit14，TDesktop 只认 bot_info_version 字段是否存在）；无 phone、无 status
//     （客户端显示 "bot" 而非 last seen）。
//   - 普通用户：照常输出 presence status。
func applyTgUserBotFields(out *tg.User, u domain.User) {
	if !u.Bot {
		out.Status = tgUserStatus(u.Status)
		return
	}
	out.Bot = true
	version := u.BotInfoVersion
	if version < 1 {
		version = 1
	}
	out.SetBotInfoVersion(version)
	if !domain.IsSystemUserID(u.ID) {
		out.SetBotBusiness(true)
	}
	out.Phone = ""
}

// isSystemUserID 报告 id 是否为内置系统账号（777000 / BotFather）。
func isSystemUserID(id int64) bool {
	_, ok := domain.SystemUserByID(id)
	return ok
}

// tgUserProfilePhoto 由 domain.User 反范式头像字段构造 UserProfilePhoto；无头像返回 nil（Encode 时为 empty）。
func tgUserProfilePhoto(u domain.User) tg.UserProfilePhotoClass {
	if u.PhotoID == 0 {
		return nil
	}
	photo := &tg.UserProfilePhoto{PhotoID: u.PhotoID, DCID: u.PhotoDCID, Personal: u.PhotoPersonal}
	if u.PhotoHasVideo {
		photo.SetHasVideo(true)
	}
	if len(u.PhotoStripped) > 0 {
		photo.SetStrippedThumb(u.PhotoStripped)
	}
	return photo
}

func tgUserStatus(status domain.UserStatus) tg.UserStatusClass {
	switch status.Kind {
	case domain.UserStatusOnline:
		if status.Expires > 0 {
			return &tg.UserStatusOnline{Expires: status.Expires}
		}
	case domain.UserStatusOffline:
		if status.WasOnline > 0 {
			return &tg.UserStatusOffline{WasOnline: status.WasOnline}
		}
	case domain.UserStatusLastWeek:
		return &tg.UserStatusLastWeek{}
	case domain.UserStatusLastMonth:
		return &tg.UserStatusLastMonth{}
	case domain.UserStatusEmpty:
		return &tg.UserStatusEmpty{}
	case domain.UserStatusRecently, domain.UserStatusUnknown:
		return &tg.UserStatusRecently{}
	}
	return &tg.UserStatusRecently{}
}

func tgUsernames(username string) []tg.Username {
	if username == "" {
		return nil
	}
	return []tg.Username{{Editable: true, Active: true, Username: username}}
}

func tgContacts(list domain.ContactList) tg.ContactsContactsClass {
	out := &tg.ContactsContacts{
		Contacts:   make([]tg.Contact, 0, len(list.Contacts)),
		Users:      make([]tg.UserClass, 0, len(list.Contacts)),
		SavedCount: len(list.Contacts),
	}
	for _, c := range list.Contacts {
		out.Contacts = append(out.Contacts, tg.Contact{UserID: c.User.ID, Mutual: c.Mutual})
		out.Users = append(out.Users, tgUser(c.User))
	}
	return out
}

func tgContactsFound(viewerUserID int64, res domain.UserSearchResult) *tg.ContactsFound {
	out := &tg.ContactsFound{
		MyResults: make([]tg.PeerClass, 0, len(res.MyResults)+len(res.MyChannelResults)),
		Results:   make([]tg.PeerClass, 0, len(res.Results)+len(res.ChannelResults)),
		Chats:     make([]tg.ChatClass, 0, len(res.MyChannelResults)+len(res.ChannelResults)),
		Users:     make([]tg.UserClass, 0, len(res.MyResults)+len(res.Results)),
	}
	seen := make(map[int64]struct{}, len(res.MyResults)+len(res.Results))
	appendUser := func(u domain.User) {
		if u.ID == 0 {
			return
		}
		if _, ok := seen[u.ID]; ok {
			return
		}
		seen[u.ID] = struct{}{}
		out.Users = append(out.Users, tgUser(u))
	}
	seenChannels := make(map[int64]struct{}, len(res.MyChannelResults)+len(res.ChannelResults))
	appendChannel := func(ch domain.Channel, self *domain.ChannelMember) {
		if ch.ID == 0 {
			return
		}
		if _, ok := seenChannels[ch.ID]; ok {
			return
		}
		seenChannels[ch.ID] = struct{}{}
		out.Chats = append(out.Chats, tgChannelChat(viewerUserID, ch, self))
	}
	for _, u := range res.MyResults {
		out.MyResults = append(out.MyResults, &tg.PeerUser{UserID: u.ID})
		appendUser(u)
	}
	for _, ch := range res.MyChannelResults {
		out.MyResults = append(out.MyResults, &tg.PeerChannel{ChannelID: ch.ID})
		appendChannel(ch, &domain.ChannelMember{ChannelID: ch.ID, UserID: viewerUserID, Status: domain.ChannelMemberActive})
	}
	for _, u := range res.Results {
		out.Results = append(out.Results, &tg.PeerUser{UserID: u.ID})
		appendUser(u)
	}
	for _, ch := range res.ChannelResults {
		out.Results = append(out.Results, &tg.PeerChannel{ChannelID: ch.ID})
		appendChannel(ch, &domain.ChannelMember{ChannelID: ch.ID, UserID: viewerUserID, Status: domain.ChannelMemberLeft})
	}
	return out
}

func tgUsers(users []domain.User) []tg.UserClass {
	out := make([]tg.UserClass, 0, len(users))
	for _, u := range users {
		out = append(out, tgUser(u))
	}
	return out
}

func tgUsersForViewer(viewerUserID int64, users []domain.User) []tg.UserClass {
	out := make([]tg.UserClass, 0, len(users))
	for _, u := range users {
		if viewerUserID != 0 && u.ID == viewerUserID {
			out = append(out, tgSelfUser(u))
			continue
		}
		out = append(out, tgUser(u))
	}
	return out
}

func addUsers(out *tg.UpdatesDifference, seen map[int64]struct{}, viewerUserID int64, users []domain.User) {
	for _, u := range users {
		if u.ID == 0 {
			continue
		}
		if _, ok := seen[u.ID]; ok {
			continue
		}
		seen[u.ID] = struct{}{}
		// difference 里自己的 user 必须带 self 标志：客户端会用该对象
		// 持久覆盖当前账号资料，self=false 会破坏 Saved Messages 语义。
		if viewerUserID != 0 && u.ID == viewerUserID {
			out.Users = append(out.Users, tgSelfUser(u))
			continue
		}
		out.Users = append(out.Users, tgUser(u))
	}
}

func addMessageUsers(out *tg.UpdatesDifference, seen map[int64]struct{}, msg domain.Message) {
	for _, peer := range []domain.Peer{msg.From, msg.Peer, domain.Peer{Type: domain.PeerTypeUser, ID: msg.ViaBotID}} {
		if peer.Type != domain.PeerTypeUser {
			continue
		}
		if _, ok := seen[peer.ID]; ok {
			continue
		}
		seen[peer.ID] = struct{}{}
		if u, ok := domain.SystemUserByID(peer.ID); ok {
			out.Users = append(out.Users, tgUser(u))
		}
	}
}

func tgChannelEmojiStatus(status domain.ChannelEmojiStatus) tg.EmojiStatusClass {
	if status.Empty() {
		return nil
	}
	out := &tg.EmojiStatus{DocumentID: status.DocumentID}
	if status.Until > 0 {
		out.SetUntil(status.Until)
	}
	return out
}
