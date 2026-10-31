package rpc

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/app/users"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

const maxSavedMusicLimit = 100
const maxRequirementsToContactUsers = 100

// registerUsers registers the users.* RPC handlers.
func (r *Router) registerUsers(d *tlprofile.Dispatcher) {
	registerRPC[*tg.UsersGetUsersRequest](d, tlprofile.SemanticMethodUsersGetUsers, func(ctx context.Context, layerRequest *tg.UsersGetUsersRequest) (any, error) {
		return r.onUsersGetUsers(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.UsersGetFullUserRequest](d, tlprofile.SemanticMethodUsersGetFullUser, func(ctx context.Context, layerRequest *tg.UsersGetFullUserRequest) (any, error) {
		return r.onUsersGetFullUser(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.UsersGetRequirementsToContactRequest](d, tlprofile.SemanticMethodUsersGetRequirementsToContact, func(ctx context.Context, layerRequest *tg.UsersGetRequirementsToContactRequest) (any, error) {
		return r.onUsersGetRequirementsToContact(ctx, layerRequest.
			ID)
	})
	registerRPC[*tg.UsersGetSavedMusicRequest](d, tlprofile.SemanticMethodUsersGetSavedMusic, func(ctx context.Context, layerRequest *tg.UsersGetSavedMusicRequest) (

		// onUsersGetUsers handles users.getUsers: supports self and known user peers
		// (including the 777000 official account).
		any, error) {
		return r.onUsersGetSavedMusic(ctx, layerRequest)
	})
	registerRPC[*tg.UsersGetSavedMusicByIDRequest](d, tlprofile.SemanticMethodUsersGetSavedMusicByID, func(ctx context.Context, layerRequest *tg.UsersGetSavedMusicByIDRequest) (any, error) {
		return r.onUsersGetSavedMusicByID(ctx, layerRequest)
	})
}

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
		// The client may also request with an explicit own ID (not inputUserSelf); it must
		// likewise carry the self flag, or a self=false own user would pollute the DrKLO
		// account cache (turning Saved Messages into a normal chat).
		if u.ID == currentUserID {
			out = append(out, r.tgSelfUser(u))
			continue
		}
		out = append(out, r.tgUser(u))
	}
	r.applyPeerReadModels(ctx, currentUserID, out, nil)
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
	// A deleted account is a durable peer tombstone. Its retained rows keep
	// message and membership references resolvable, but none of those private
	// read models belong in users.getFullUser after deletion. A frozen account is
	// projected as a peer tombstone too, but carries the synthetic "frozen"
	// third-party mark (icon + description) alongside the deleted presentation.
	if u.Deleted {
		out := deletedUserFull(u.ID, user)
		if u.FrozenForViewer {
			applyFrozenUserFullMark(&out.FullUser, u)
		}
		return out, nil
	}
	if err := r.applyBotCanEditToUser(ctx, currentUserID, u, user); err != nil {
		return nil, err
	}
	contactRestriction, err := r.privateContactRestrictionFor(ctx, currentUserID, u.ID)
	if err != nil {
		return nil, err
	}
	applyPrivateContactRestrictionToUser(user, contactRestriction)
	r.applyPeerReadModels(ctx, currentUserID, []tg.UserClass{user}, nil)
	loadEpoch := r.userFullProjectionCache.LoadEpoch()
	if full, ok := r.userFullProjectionCache.Lookup(currentUserID, u.ID); ok {
		if !applyContactNoteToUserFull(u, &full) {
			return nil, internalErr()
		}
		if err := r.applyContactPeerStateToUserFull(ctx, currentUserID, u.ID, &full); err != nil {
			return nil, err
		}
		if err := r.applyTranslationDisabledToUserFull(ctx, currentUserID, u.ID, &full); err != nil {
			return nil, err
		}
		r.applyStoriesPinnedAvailableToUserFull(ctx, currentUserID, u.ID, &full)
		r.applyNotifySettingsToUserFull(ctx, currentUserID, u.ID, &full)
		r.applyBotVerificationToUserFull(ctx, u.ID, &full)
		applyPrivateContactRestrictionToUserFull(&full, contactRestriction)
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
	if !applyContactNoteToUserFull(u, &full) {
		return nil, internalErr()
	}
	if err := r.applyContactPeerStateToUserFull(ctx, currentUserID, u.ID, &full); err != nil {
		return nil, err
	}
	if err := r.applyTranslationDisabledToUserFull(ctx, currentUserID, u.ID, &full); err != nil {
		return nil, err
	}
	r.applyStoriesPinnedAvailableToUserFull(ctx, currentUserID, u.ID, &full)
	r.applyNotifySettingsToUserFull(ctx, currentUserID, u.ID, &full)
	// Deliberately after StoreIfEpoch, like applyPersonalChannelToUserFull: the mark
	// is not baked into the per-(viewer,target) cache, so a revoked badge is gone on
	// the next response instead of lingering for the cache TTL.
	r.applyBotVerificationToUserFull(ctx, u.ID, &full)
	applyPrivateContactRestrictionToUserFull(&full, contactRestriction)
	chats := r.applyPersonalChannelToUserFull(ctx, currentUserID, u.PersonalChannelID, &full)
	return &tg.UsersUserFull{
		FullUser: full,
		Users:    []tg.UserClass{user},
		Chats:    chats,
	}, nil
}

// applyContactPeerStateToUserFull keeps users.getFullUser on the same
// owner-scoped contact read model as contacts.getBlocked and
// messages.getPeerSettings. Official clients treat UserFull.blocked as an
// authoritative replacement for their local block state, so omitting these
// fields can undo a block learned from contacts.getBlocked or updatePeerBlocked.
//
// The contact service guarantees BlockContact == !blocked for a non-self user.
// Reusing the peer-settings projection avoids a second block-store lookup while
// retaining the shared viewer/peer cache and its mutation invalidation rules.
func (r *Router) applyContactPeerStateToUserFull(ctx context.Context, viewerUserID, targetUserID int64, full *tg.UserFull) error {
	if full == nil {
		return internalErr()
	}
	full.SetBlocked(false)
	full.SetBlockedMyStoriesFrom(false)
	full.Settings = tg.PeerSettings{}
	if viewerUserID == 0 || targetUserID == 0 || viewerUserID == targetUserID {
		return nil
	}

	peer := domain.Peer{Type: domain.PeerTypeUser, ID: targetUserID}
	loadEpoch := r.peerSettingsProjectionCache.LoadEpoch()
	settings, ok := r.peerSettingsProjectionCache.Lookup(viewerUserID, peer)
	if !ok {
		var err error
		settings, err = r.buildPeerSettingsProjection(ctx, viewerUserID, peer)
		if err != nil {
			return err
		}
		r.peerSettingsProjectionCache.StoreIfEpoch(viewerUserID, peer, settings, loadEpoch)
	}
	full.Settings = tgPeerSettings(settings)
	if r.deps.Contacts != nil {
		blocked := !settings.BlockContact
		full.SetBlocked(blocked)
		// telesrv currently models the main and stories block switches as one
		// durable relation. Keep both wire flags consistent until independent
		// stories-only blocking is introduced.
		full.SetBlockedMyStoriesFrom(blocked)
	}
	return nil
}

// applyContactNoteToUserFull overlays the viewer-scoped contact note after the
// expensive UserFull projection cache. This keeps private notes out of the
// large LRU while reusing the contact projection already loaded by Users.ByID,
// so users.getFullUser adds neither a PostgreSQL query nor an N+1 read.
func applyContactNoteToUserFull(user domain.User, full *tg.UserFull) bool {
	if full == nil {
		return false
	}
	full.Flags2.Unset(22)
	full.Note = tg.TextWithEntities{}
	if !user.Contact {
		return user.ContactNote == "" && len(user.ContactNoteEntities) == 0
	}
	if user.ContactNote == "" {
		return len(user.ContactNoteEntities) == 0
	}
	if !utf8.ValidString(user.ContactNote) || utf8.RuneCountInString(user.ContactNote) > maxContactNoteLength ||
		len(user.ContactNoteEntities) > maxMessageEntityCount || !validEphemeralEntityBounds(user.ContactNote, user.ContactNoteEntities) {
		return false
	}
	entities := tgMessageEntities(user.ContactNoteEntities)
	if len(entities) != len(user.ContactNoteEntities) {
		return false
	}
	for _, entity := range user.ContactNoteEntities {
		switch entity.Type {
		case domain.MessageEntityCustomEmoji:
			if entity.DocumentID <= 0 {
				return false
			}
		case domain.MessageEntityMentionName:
			if entity.UserID <= 0 {
				return false
			}
		}
	}
	full.SetNote(tg.TextWithEntities{
		Text:     user.ContactNote,
		Entities: entities,
	})
	return true
}

func (r *Router) buildUserFullProjection(ctx context.Context, currentUserID int64, u domain.User) (tg.UserFull, error) {
	visibility, err := r.userFullPrivacyVisibility(ctx, currentUserID, u.ID)
	if err != nil {
		return tg.UserFull{}, internalErr()
	}
	about := u.About
	if !visibility[domain.PrivacyKeyAbout] {
		about = ""
	}
	// SCAM/FAKE: the warning is server-authored, so it rides the same privacy
	// gate as the owner's own text -- a viewer without About access sees the
	// mark's badge only, never the injected sentence. Prepended, never stored:
	// the warning lives in this projection alone (domain.ModerationProfileWarning).
	//
	// Skipped for the owner's own view: clients prefill the bio editor from this
	// same About, so injecting the line there would put our warning into the box
	// the owner is about to edit (and it would come back through updateProfile).
	if u.ID != currentUserID && visibility[domain.PrivacyKeyAbout] {
		about = domain.ModerationProfileWarning(u.Scam, u.Fake, about)
	}
	full := tg.UserFull{
		ID:             u.ID,
		About:          about,
		Settings:       tg.PeerSettings{},
		NotifySettings: *tdesktop.NotifySettings(),
	}
	if tab, ok := r.mainTabFor(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}); ok {
		full.SetMainTab(tab)
	}
	if u.ID != currentUserID {
		if svc, ok := r.deps.Messages.(PrivateNoForwardsService); ok {
			state, err := svc.GetPrivateNoForwards(ctx, currentUserID, u.ID)
			if err != nil {
				return tg.UserFull{}, internalErr()
			}
			myEnabled, peerEnabled := state.ForViewer(currentUserID)
			full.SetNoforwardsMyEnabled(myEnabled)
			full.SetNoforwardsPeerEnabled(peerEnabled)
		}
	}
	// Call entry point: without phone_calls_available=true the client does not show the
	// call button (a P1 prerequisite). phone_calls_private marks the peer as P2P-disabled
	// (the true p2p_allowed value is computed separately at call confirmation).
	if !u.Bot && u.ID != currentUserID {
		callsAllowed := visibility[domain.PrivacyKeyPhoneCall]
		p2pAllowed := visibility[domain.PrivacyKeyPhoneP2P]
		voiceAllowed := visibility[domain.PrivacyKeyVoiceMessages]
		full.PhoneCallsAvailable = callsAllowed
		full.VideoCallsAvailable = callsAllowed
		full.PhoneCallsPrivate = !p2pAllowed
		full.VoiceMessagesForbidden = !voiceAllowed
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
	if err := r.fillUserFullPhotos(ctx, currentUserID, u.ID, visibility[domain.PrivacyKeyProfilePhoto], &full); err != nil {
		return tg.UserFull{}, err
	}
	if r.deps.Account != nil {
		if visibility[domain.PrivacyKeySavedMusic] {
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
		// Pin-discovery chain: userFull.pinned_msg_id is the current top pin of this
		// private chat (including Saved Messages); the client uses it to trigger a
		// filterPinned search.
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
		// Both sides of a private chat can always pin (the official server sets
		// can_pin_message for a user peer).
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
	// Star gift count: the client gates the profile Gifts section/tab on
	// stargifts_count>0 (DrKLO ProfileActivity:10497 / TDesktop data_user.cpp:924), so
	// without it the received gifts are not shown on the profile. Counts the gifts
	// displayed on the profile (not converted, not hidden).
	if r.deps.Gifts != nil {
		if n, err := r.deps.Gifts.CountSaved(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}); err == nil && n > 0 {
			full.SetStargiftsCount(n)
		}
	}
	if svc, ok := r.accountSettingsSvc(); ok {
		settings, err := svc.GetAccountSettings(ctx, u.ID)
		if err != nil {
			return tg.UserFull{}, internalErr()
		}
		gifts := settings.GlobalPrivacy.DisallowedGifts
		if !gifts.Zero() {
			full.SetDisallowedGifts(tg.DisallowedGiftsSettings{
				DisallowUnlimitedStargifts:    gifts.UnlimitedStargifts,
				DisallowLimitedStargifts:      gifts.LimitedStargifts,
				DisallowUniqueStargifts:       gifts.UniqueStargifts,
				DisallowPremiumGifts:          gifts.PremiumGifts,
				DisallowStargiftsFromChannels: gifts.StargiftsFromChannel,
			})
		}
	}
	// Birthday (account.updateBirthday): stored in userFull.birthday, trimmed for others
	// per PrivacyKeyBirthday, and always visible to the user themself.
	if u.Birthday.IsSet() {
		if visibility[domain.PrivacyKeyBirthday] {
			full.SetBirthday(tgBirthday(u.Birthday))
		}
	}
	r.applyAccountRatingToUserFull(ctx, currentUserID, u, &full)
	// The personal channel (account.updatePersonalChannel) is not set here: it is
	// resolved live per viewer, handled as a post-cache overlay
	// (applyPersonalChannelToUserFull), avoiding baking it into the per-(viewer,target)
	// projection cache and resolving the same channel twice in build/chats.
	return full, nil
}

// userFullPrivacyVisibility evaluates every privacy-controlled UserFull field
// from one owner snapshot when the service supports batching. Older test or
// alternate implementations retain scalar parity; a missing batch key fails
// closed instead of exposing that field.
func (r *Router) userFullPrivacyVisibility(ctx context.Context, viewerUserID, ownerUserID int64) (map[domain.PrivacyKey]bool, error) {
	keys := []domain.PrivacyKey{
		domain.PrivacyKeyAbout,
		domain.PrivacyKeyPhoneCall,
		domain.PrivacyKeyPhoneP2P,
		domain.PrivacyKeyVoiceMessages,
		domain.PrivacyKeyProfilePhoto,
		domain.PrivacyKeySavedMusic,
		domain.PrivacyKeyBirthday,
	}
	out := make(map[domain.PrivacyKey]bool, len(keys))
	if r.deps.Privacy == nil || ownerUserID == viewerUserID {
		for _, key := range keys {
			out[key] = true
		}
		return out, nil
	}
	if batch, ok := r.deps.Privacy.(batchPrivacyEvaluator); ok {
		matrix, err := batch.CanSeeBatch(ctx, []int64{ownerUserID}, viewerUserID, keys)
		if err != nil {
			return nil, err
		}
		ownerVisibility, found := matrix[ownerUserID]
		if !found {
			return out, nil
		}
		for _, key := range keys {
			out[key] = ownerVisibility[key]
		}
		return out, nil
	}
	for _, key := range keys {
		allowed, err := r.deps.Privacy.CanSee(ctx, ownerUserID, viewerUserID, key)
		if err != nil {
			return nil, err
		}
		out[key] = allowed
	}
	return out, nil
}

// applyAccountRatingToUserFull projects gramsrv's stored composite rating through
// the rating fields official clients already render. This is a gramsrv policy
// score, not a promise that its inputs or thresholds match Telegram's service.
//
// The projection is built inside the existing per-(viewer,target) UserFull
// cache. Therefore a cache miss adds at most one primary-key read and a cache hit
// adds none. Recompute and writes remain exclusively in the bounded background
// worker/admin paths.
func (r *Router) applyAccountRatingToUserFull(ctx context.Context, viewerUserID int64, target domain.User, full *tg.UserFull) {
	targetUserID := target.ID
	if r.deps.AccountRatings == nil || full == nil || !domain.RatableAccount(targetUserID, target.Bot) {
		return
	}
	rating, err := r.deps.AccountRatings.Rating(ctx, targetUserID)
	if err != nil {
		// Missing, disabled and temporarily unavailable projections all preserve
		// the legacy wire shape instead of failing the surrounding profile read.
		return
	}
	full.SetStarsRating(tgAccountRatingLevel(rating.LevelSnapshot()))
	if viewerUserID == 0 || viewerUserID != targetUserID {
		return
	}
	pending, ok := rating.PendingLevel()
	if !ok {
		return
	}
	full.SetStarsMyPendingRating(tgAccountRatingLevel(pending))
	full.SetStarsMyPendingRatingDate(int(rating.PendingDate.Unix()))
}

// tgAccountRatingLevel maps the local level snapshot onto starsRating#1b0e4f07.
// next_level_stars remains absent at the configured maximum local level.
func tgAccountRatingLevel(in domain.AccountRatingLevel) tg.StarsRating {
	out := tg.StarsRating{
		Level:             in.Level,
		CurrentLevelStars: in.CurrentLevelStars,
		Stars:             in.Stars,
	}
	if in.HasNextLevelStars {
		out.SetNextLevelStars(in.NextLevelStars)
	}
	return out
}

// tgBirthday converts a domain birthday to tg.Birthday (Year is optional; 0 means no
// year).
func tgBirthday(b domain.Birthday) tg.Birthday {
	out := tg.Birthday{Day: b.Day, Month: b.Month}
	if b.Year != 0 {
		out.SetYear(b.Year)
	}
	return out
}

// applyPersonalChannelToUserFull resolves the profile personal channel once: it sets
// personal_channel_id + the latest post id on userFull and returns the channel object for
// the response chats; when the channel cannot be resolved/is deleted nothing is emitted.
// It runs after the userFull projection cache (isomorphic to the notify/stories overlay):
// personal-channel accessibility varies per viewer so it must be resolved live, and this
// avoids resolving the same channel twice in build (cache miss) and chats.
// personalChannelID comes from the owner user loaded for this request.
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
	// Set bot_can_edit (flags2.1) from the owner's perspective: TDesktop/DrKLO uses it to
	// show the bot management UI (rename/description/photo entry points). Only the owner
	// sees it.
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
	currentUserID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if !authorized || r.deps.Users == nil {
		out := make([]tg.RequirementToContactClass, len(ids))
		for i := range out {
			out[i] = &tg.RequirementToContactEmpty{}
		}
		return out, nil
	}
	type requirementInput struct {
		userID     int64
		accessHash int64
		self       bool
	}
	inputs := make([]requirementInput, len(ids))
	targetIDs := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for i, input := range ids {
		switch value := input.(type) {
		case *tg.InputUserSelf:
			inputs[i] = requirementInput{userID: currentUserID, self: true}
		case *tg.InputUser:
			if value == nil || value.UserID == 0 {
				continue
			}
			inputs[i] = requirementInput{userID: value.UserID, accessHash: value.AccessHash}
			if _, ok := seen[value.UserID]; !ok {
				seen[value.UserID] = struct{}{}
				targetIDs = append(targetIDs, value.UserID)
			}
		}
	}
	usersByID := make(map[int64]domain.User, len(targetIDs))
	if len(targetIDs) > 0 {
		list, err := r.deps.Users.ByIDs(ctx, currentUserID, targetIDs)
		if err != nil && !errors.Is(err, users.ErrNotAuthorized) {
			return nil, internalErr()
		}
		for _, user := range list {
			usersByID[user.ID] = user
		}
	}
	validTargetIDs := make([]int64, 0, len(targetIDs))
	for i, input := range inputs {
		if input.self {
			continue
		}
		user, ok := usersByID[input.userID]
		if !ok || input.accessHash != 0 && input.accessHash != user.AccessHash {
			inputs[i].userID = 0
			continue
		}
		validTargetIDs = append(validTargetIDs, user.ID)
	}
	settingsByUser := make(map[int64]domain.AccountSettings, len(validTargetIDs))
	if svc, ok := r.accountSettingsSvc(); ok {
		settingsByUser, err = r.accountSettings.getOrLoadBatch(ctx, validTargetIDs, svc)
		if err != nil {
			return nil, internalErr()
		}
	}
	freeByUser := make(map[int64]bool, len(validTargetIDs))
	if evaluator, ok := r.deps.Privacy.(contactRequirementPrivacyEvaluator); ok {
		freeByUser, err = evaluator.CanContactForFreeBatch(ctx, validTargetIDs, currentUserID)
		if err != nil {
			return nil, internalErr()
		}
	}
	out := make([]tg.RequirementToContactClass, len(inputs))
	for i, input := range inputs {
		out[i] = &tg.RequirementToContactEmpty{}
		if input.userID == 0 || input.self || input.userID == currentUserID || freeByUser[input.userID] {
			continue
		}
		global := settingsByUser[input.userID].GlobalPrivacy
		switch {
		case global.NoncontactPeersPaidStars > 0:
			out[i] = &tg.RequirementToContactPaidMessages{StarsAmount: global.NoncontactPeersPaidStars}
		case global.NewNoncontactPeersRequirePremium:
			out[i] = &tg.RequirementToContactPremium{}
		}
	}
	return out, nil
}

type contactRequirementPrivacyEvaluator interface {
	CanContactForFreeBatch(ctx context.Context, ownerUserIDs []int64, viewerUserID int64) (map[int64]bool, error)
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

func (r *Router) fillUserFullPhotos(ctx context.Context, viewerUserID, ownerUserID int64, profileAllowed bool, full *tg.UserFull) error {
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

// tgBotInfo constructs userFull.bot_info. A bot user must carry it (even with empty
// commands): without it TDesktop's botInfo.inited is never set, and every chat open /
// "/" input re-fetches getFullUser; user_id is mandatory and must equal the bot's id,
// and a mismatch is silently ignored entirely by the client.
func (r *Router) tgBotInfo(ctx context.Context, u domain.User) tg.BotInfo {
	info := tgBotInfoFromProfile(u.ID, domain.BotProfile{}, false)
	if r.deps.Bots != nil {
		if profile, found, err := r.deps.Bots.BotInfo(ctx, u.ID); err == nil && found {
			info = tgBotInfoFromProfile(u.ID, profile, true)
		}
	}
	// botInfo#4d8a0299 verifier_settings:flags.9 -- present only for a bot that is
	// itself an enabled verifier, and independent of the bot profile above.
	r.applyVerifierSettingsToOneBotInfo(ctx, &info, u.ID)
	return info
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
	// One verifier-settings query for the whole bot list, next to the profile batch
	// above: channelFull.bot_info must not turn into an N+1 just to carry
	// verifier_settings:flags.9.
	verifiers := r.verifierSettingsBatch(ctx, ids)
	out := make([]tg.BotInfo, 0, len(userIDs))
	for _, id := range userIDs {
		profile, found := profiles[id]
		info := tgBotInfoFromProfile(id, profile, found)
		if settings, ok := verifiers[id]; ok {
			applyVerifierSettingsToBotInfo(&info, id, settings)
		}
		out = append(out, info)
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
			cmds = append(cmds, tg.BotCommand{
				Command:     c.Command,
				Description: c.Description,
				Ephemeral:   c.Ephemeral,
			})
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

func deletedUserFull(userID int64, user tg.UserClass) *tg.UsersUserFull {
	out := emptyUserFull()
	out.FullUser.ID = userID
	out.Users = []tg.UserClass{user}
	out.Chats = []tg.ChatClass{}
	return out
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
