package rpc

import (
	"time"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

// tgSelfUser converts domain.User into a self-marked tg.User (optional fields are SetFlags'd automatically by Encode).
func tgSelfUser(u domain.User) *tg.User {
	u = officialSystemUserPresentation(u)
	if u.Deleted {
		return tgDeletedUser(u)
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
		Scam:          u.Scam,
		Fake:          u.Fake,
		Support:       u.Support,
		Contact:       u.Contact,
		MutualContact: u.Mutual,
		CloseFriend:   u.CloseFriend,
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
	u = officialSystemUserPresentation(u)
	if u.Deleted {
		return tgDeletedUser(u)
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
		Fake:          u.Fake,
		Support:       u.Support,
		Contact:       u.Contact,
		MutualContact: u.Mutual,
		CloseFriend:   u.CloseFriend,
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

// applyFrozenUserMark stamps user#b1b8cc83 bot_verification_icon:flags2.14 on a
// peer tombstone when the deleted presentation comes from an account freeze. The
// icon id must name a real custom emoji document in the operator catalogue,
// otherwise the client resolves it through messages.getCustomEmojiDocuments as
// empty and draws no badge; it is a synthetic projection only, never persisted.
func applyFrozenUserMark(out *tg.User, u domain.User) {
	if out == nil || !u.FrozenForViewer {
		return
	}
	out.SetBotVerificationIcon(accountFrozenMarkIcon)
}

// officialSystemUserPresentation keeps the durable reserved identity stable
// while projecting the deployment brand from configuration. Older databases
// may still contain "Telesrv" in users.first_name; without this boundary
// normalization clients alternate between the database row and the synthetic
// system-user snapshot as caches are refreshed.
func officialSystemUserPresentation(u domain.User) domain.User {
	if u.ID != domain.OfficialSystemUserID {
		return u
	}
	official := domain.OfficialSystemUser()
	u.FirstName = official.FirstName
	u.LastName = official.LastName
	u.Username = official.Username
	return u
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

// applyTgUserPremiumFields derives the premium flag (bit28, an independent bit) and
// emoji status live from the expiry time. The check uses the real clock: the
// authoritative source of premium is premium_expires_at itself, so it stops sending
// at expiry and correctness does not depend on a background sweeper (which only
// handles cleanup and updateUser notification); a bot is never Premium (excluded
// inside PremiumActiveAt).
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

// tgUserEmojiStatus converts a user emoji status into TL: unset/expired returns
// emojiStatusEmpty (used for updateUserEmojiStatus clear semantics). Whether it is
// inlined in the user TL is still controlled by applyTgUserPremiumFields (the field
// is omitted entirely when there is no status, matching the official user encoding).
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

// applyTgUserBotFields applies the mutually exclusive output for bot/regular users:
//   - bot: must set both the bot flag and bot_info_version (in Layer 225 both share
//     flags bit14; TDesktop only recognizes whether the bot_info_version field is
//     present); no phone, no status (the client shows "bot" instead of last seen).
//   - regular user: emit the presence status as usual.
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

// isSystemUserID reports whether id is a built-in system account (777000 / BotFather).
func isSystemUserID(id int64) bool {
	_, ok := domain.SystemUserByID(id)
	return ok
}

// deletedAccountDisplayName is what a tombstone presents as on the wire. Clients
// derive the same string from the deleted flag, but sending it explicitly keeps
// the name stable across clients whose updateUser handling only merges the photo
// and never re-reads first_name.
const deletedAccountDisplayName = "Deleted Account"

// tgDeletedUser builds the tombstone a deleted account is projected as: the id,
// the deleted flag and an explicit empty avatar. Every other field is stripped.
//
// The explicit userProfilePhotoEmpty is what makes the account render as a ghost
// instead of an avatarless stranger: the client is told "no photo" in the shape it
// expects, rather than being left to infer it from a missing optional field, and
// deletion already deactivated the profile_photos rows so no real avatar can be
// projected for this id again.
func tgDeletedUser(u domain.User) *tg.User {
	out := &tg.User{
		ID:        u.ID,
		Deleted:   true,
		FirstName: deletedAccountDisplayName,
		Photo:     &tg.UserProfilePhotoEmpty{},
		// An absent status field is not the same as an empty one: clients keep the
		// cached presence when the flag is missing, which is how a deleted account
		// kept showing "last seen recently" right after it appeared as deleted.
		// An explicit userStatusEmpty is what actually clears it.
		Status: &tg.UserStatusEmpty{},
	}
	applyFrozenUserMark(out, u)
	return out
}

// tgUserProfilePhoto builds a UserProfilePhoto from the denormalized avatar fields of domain.User; returns nil when there is no avatar (encoded as empty).
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

// tgUsernames builds the vector carried by updateUserName. Ordinary user/channel
// constructors keep using their scalar username until at least one collectible
// is associated with the peer.
func tgUsernames(username string) []tg.Username {
	if username == "" {
		return nil
	}
	return []tg.Username{{Editable: true, Active: true, Username: username}}
}

// tgUsernamesFromRegistry is the single builder of the full username#b4073647
// vector in stored order (see domain.SortUsernames), with editable/active taken
// from the registry row rather than assumed.
//
// It degrades to tgUsernames(fallback) whenever the registry contributed nothing
// usable, which is what keeps a missing/failing registry service byte-identical
// to the pre-collectible wire shape.
func tgUsernamesFromRegistry(list []domain.Username, fallback string) []tg.Username {
	if len(list) == 0 {
		return tgUsernames(fallback)
	}
	sorted := domain.SortUsernames(list)
	out := make([]tg.Username, 0, len(sorted))
	for _, item := range sorted {
		name := domain.NormalizeUsername(item.Username)
		if name == "" {
			continue
		}
		out = append(out, tg.Username{
			Editable: item.Editable,
			Active:   item.Active,
			Username: name,
		})
	}
	if len(out) == 0 {
		return tgUsernames(fallback)
	}
	return out
}

func hasCollectibleUsername(list []domain.Username) bool {
	for _, item := range list {
		if item.Collectible() {
			return true
		}
	}
	return false
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
		// The viewer's own user in a difference must carry the self flag: the client
		// persistently overwrites the current account profile with that object, and
		// self=false would break Saved Messages semantics.
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
