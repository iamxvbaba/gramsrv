package rpc

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func (r *Router) onMessagesSaveDraft(ctx context.Context, req *tg.MessagesSaveDraftRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	peerTL := tgPeer(peer)
	if peerTL == nil {
		return true, nil
	}
	date := int(r.clock.Now().Unix())
	draft, err := r.dialogDraftFromSaveDraft(ctx, userID, peer, req, date)
	if err != nil {
		return false, err
	}
	var recorded domain.UpdateEvent
	changed := true
	if r.deps.Dialogs != nil {
		changed, err = r.deps.Dialogs.SaveDraft(ctx, userID, draft)
		if err != nil {
			return false, dialogDraftErr(err)
		}
		if !changed {
			return true, nil
		}
		// durable event: a draft is absolute state, so the event records only a
		// peer (+top_msg_id) marker, and difference/outbox replay reloads the
		// current value per peer (see enrichDraftMessageEvent).
		recorded = r.recordDraftMessageEvent(ctx, userID, peer, draft.TopMessageID, &date)
	}
	update := &tg.UpdateDraftMessage{
		Peer:  peerTL,
		Draft: tgDialogDraft(draft),
	}
	if draft.TopMessageID > 0 {
		update.SetTopMsgID(draft.TopMessageID)
	}
	users, chats := r.peerObjectsForDraftUpdate(ctx, userID, peer)
	updates := &tg.Updates{
		Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, recorded),
		Users:   users,
		Chats:   chats,
		Date:    date,
		Seq:     0,
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, updates)
	return true, nil
}

// recordDraftMessageEvent records a draft_message durable event (consuming account
// pts, no wire pts) and pushes it to other online sessions via the outbox
// dispatcher; when the Updates service is absent it returns the zero value
// (degrading to a direct push).
func (r *Router) recordDraftMessageEvent(ctx context.Context, userID int64, peer domain.Peer, topMsgID int, date *int) domain.UpdateEvent {
	if r.deps.Updates == nil {
		return domain.UpdateEvent{}
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	event, state, err := r.deps.Updates.RecordDraftMessage(ctx, authKeyID, userID, peer, topMsgID, rawAuthKeyIDForOrigin(ctx), sessionID)
	if err != nil {
		r.log.Warn("record draft message event", zap.Int64("user_id", userID), zap.Error(err))
		return domain.UpdateEvent{}
	}
	if date != nil && state.Date != 0 {
		*date = state.Date
	}
	return event
}

func (r *Router) onMessagesGetAllDrafts(ctx context.Context) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	date := int(r.clock.Now().Unix())
	if r.deps.Dialogs == nil {
		return &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: date, Seq: 0}, nil
	}
	drafts, err := r.deps.Dialogs.ListDrafts(ctx, userID, domain.MaxDialogDraftsPerUser)
	if err != nil {
		return nil, dialogDraftErr(err)
	}
	updates := make([]tg.UpdateClass, 0, len(drafts))
	users, chats := r.peerObjectsForDrafts(ctx, userID, drafts)
	for _, draft := range drafts {
		peer := tgPeer(draft.Peer)
		if peer == nil {
			continue
		}
		update := &tg.UpdateDraftMessage{Peer: peer, Draft: tgDialogDraft(draft)}
		if draft.TopMessageID > 0 {
			update.SetTopMsgID(draft.TopMessageID)
		}
		updates = append(updates, update)
	}
	return &tg.Updates{Updates: updates, Users: users, Chats: chats, Date: date, Seq: 0}, nil
}

func (r *Router) onMessagesClearAllDrafts(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Dialogs == nil {
		return true, nil
	}
	drafts, err := r.deps.Dialogs.ClearDrafts(ctx, userID, domain.MaxDialogDraftsPerUser)
	if err != nil {
		return false, dialogDraftErr(err)
	}
	if len(drafts) == 0 {
		return true, nil
	}
	date := int(r.clock.Now().Unix())
	updates := make([]tg.UpdateClass, 0, len(drafts))
	events := make([]domain.UpdateEvent, 0, len(drafts))
	for _, draft := range drafts {
		update := draftClearUpdate(draft.Peer, draft.TopMessageID, date)
		if update == nil {
			continue
		}
		updates = append(updates, update)
		if event := r.recordDraftMessageEvent(ctx, userID, draft.Peer, draft.TopMessageID, nil); event.Pts > 0 {
			events = append(events, event)
			updates = appendAuxPtsBookkeeping(updates, event)
		}
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, events...)
	users, chats := r.peerObjectsForDrafts(ctx, userID, drafts)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
		Updates: updates,
		Users:   users,
		Chats:   chats,
		Date:    date,
		Seq:     0,
	})
	return true, nil
}

func (r *Router) dialogDraftFromSaveDraft(ctx context.Context, userID int64, peer domain.Peer, req *tg.MessagesSaveDraftRequest, date int) (domain.DialogDraft, error) {
	if req == nil {
		return domain.DialogDraft{Peer: peer, Date: date}, nil
	}
	if utf8.RuneCountInString(req.Message) > maxSendMessageTextLength {
		return domain.DialogDraft{}, messageTooLongErr()
	}
	if len(req.Entities) > maxMessageEntityCount {
		return domain.DialogDraft{}, limitInvalidErr()
	}
	suggestedInput, hasSuggestedPost := req.GetSuggestedPost()
	suggestedPost, err := domainSuggestedPost(suggestedInput, hasSuggestedPost)
	if err != nil {
		return domain.DialogDraft{}, err
	}
	if hasSuggestedPost {
		if peer.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
			return domain.DialogDraft{}, suggestedPostPeerInvalidErr()
		}
		if _, _, resolveErr := r.deps.Channels.ResolveMonoforumSend(ctx, userID, peer.ID); resolveErr != nil {
			return domain.DialogDraft{}, suggestedPostPeerInvalidErr()
		}
	}
	replyTo, err := r.messageReplyFromInput(ctx, userID, peer, req.ReplyTo)
	if err != nil {
		return domain.DialogDraft{}, err
	}
	webpage, err := dialogDraftWebPageFromInput(req.Media)
	if err != nil {
		return domain.DialogDraft{}, err
	}
	var richMessage *domain.MessageRichMessage
	if req.RichMessage != nil {
		richMessage, err = r.domainRichMessageFromInput(ctx, req.RichMessage)
		if err != nil {
			return domain.DialogDraft{}, err
		}
	}
	topMessageID := 0
	if replyTo != nil && peer.Type == domain.PeerTypeChannel && replyTo.TopMessageID > 0 {
		topMessageID = replyTo.TopMessageID
	}
	return domain.DialogDraft{
		Peer:          peer,
		TopMessageID:  topMessageID,
		Date:          date,
		NoWebpage:     req.NoWebpage,
		InvertMedia:   req.InvertMedia,
		Message:       req.Message,
		Entities:      domainMessageEntities(req.Entities),
		ReplyTo:       replyTo,
		WebPage:       webpage,
		Effect:        req.Effect,
		SuggestedPost: suggestedPost,
		RichMessage:   richMessage,
	}, nil
}

func dialogDraftWebPageFromInput(media tg.InputMediaClass) (*domain.DialogDraftWebPage, error) {
	switch m := media.(type) {
	case nil, *tg.InputMediaEmpty:
		return nil, nil
	case *tg.InputMediaWebPage:
		if m.URL == "" {
			return nil, mediaInvalidErr()
		}
		return &domain.DialogDraftWebPage{
			URL:             m.URL,
			ForceLargeMedia: m.ForceLargeMedia,
			ForceSmallMedia: m.ForceSmallMedia,
			Optional:        m.Optional,
		}, nil
	default:
		return nil, mediaInvalidErr()
	}
}

func tgDraftMessageFromSaveDraft(req *tg.MessagesSaveDraftRequest, date int) tg.DraftMessageClass {
	if req == nil || saveDraftIsEmpty(req) {
		draft := &tg.DraftMessageEmpty{}
		draft.SetDate(date)
		return draft
	}
	return &tg.DraftMessage{
		NoWebpage:     req.NoWebpage,
		InvertMedia:   req.InvertMedia,
		ReplyTo:       req.ReplyTo,
		Message:       req.Message,
		Entities:      req.Entities,
		Media:         draftInputMedia(req.Media),
		Date:          date,
		Effect:        req.Effect,
		SuggestedPost: req.SuggestedPost,
	}
}

func saveDraftIsEmpty(req *tg.MessagesSaveDraftRequest) bool {
	return !req.NoWebpage &&
		!req.InvertMedia &&
		draftReplyIsEmpty(req.ReplyTo) &&
		req.Message == "" &&
		len(req.Entities) == 0 &&
		draftInputMedia(req.Media) == nil &&
		req.Effect == 0 &&
		req.SuggestedPost.Zero() &&
		req.RichMessage == nil
}

func (r *Router) usersForDraftUpdate(ctx context.Context, userID int64, peer domain.Peer) []tg.UserClass {
	if r.deps.Users == nil {
		return []tg.UserClass{}
	}
	users := make([]tg.UserClass, 0, 2)
	if self, err := r.deps.Users.Self(ctx, userID); err == nil && self.ID != 0 {
		users = append(users, r.tgSelfUser(self))
	}
	if peer.Type == domain.PeerTypeUser && peer.ID != 0 && peer.ID != userID {
		users = append(users, r.tgUsersForIDs(ctx, userID, []int64{peer.ID})...)
	}
	return users
}

func (r *Router) usersForDrafts(ctx context.Context, userID int64, drafts []domain.DialogDraft) []tg.UserClass {
	users := make([]tg.UserClass, 0, len(drafts)+1)
	seen := map[int64]struct{}{}
	if r.deps.Users != nil {
		if self, err := r.deps.Users.Self(ctx, userID); err == nil && self.ID != 0 {
			users = append(users, r.tgSelfUser(self))
			seen[self.ID] = struct{}{}
		}
	}
	peerUserIDs := make([]int64, 0, len(drafts))
	for _, draft := range drafts {
		if draft.Peer.Type != domain.PeerTypeUser || draft.Peer.ID == 0 {
			continue
		}
		if _, ok := seen[draft.Peer.ID]; ok {
			continue
		}
		seen[draft.Peer.ID] = struct{}{}
		peerUserIDs = append(peerUserIDs, draft.Peer.ID)
	}
	users = append(users, r.tgUsersForIDs(ctx, userID, peerUserIDs)...)
	return users
}

func (r *Router) chatsForDraftUpdate(ctx context.Context, userID int64, peer domain.Peer) []tg.ChatClass {
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 || r.deps.Channels == nil {
		return []tg.ChatClass{}
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, peer.ID)
	if err != nil || view.Channel.ID == 0 {
		return []tg.ChatClass{}
	}
	return []tg.ChatClass{tgChannelChatForView(userID, view)}
}

func (r *Router) chatsForDrafts(ctx context.Context, userID int64, drafts []domain.DialogDraft) []tg.ChatClass {
	if r.deps.Channels == nil || len(drafts) == 0 {
		return []tg.ChatClass{}
	}
	ids := make([]int64, 0, len(drafts))
	seen := map[int64]struct{}{}
	for _, draft := range drafts {
		if draft.Peer.Type != domain.PeerTypeChannel || draft.Peer.ID == 0 {
			continue
		}
		if _, ok := seen[draft.Peer.ID]; ok {
			continue
		}
		seen[draft.Peer.ID] = struct{}{}
		ids = append(ids, draft.Peer.ID)
	}
	if len(ids) == 0 {
		return []tg.ChatClass{}
	}
	views, err := r.deps.Channels.GetChannels(ctx, userID, ids)
	if err != nil {
		return []tg.ChatClass{}
	}
	byID := make(map[int64]domain.ChannelView, len(views))
	for _, view := range views {
		if view.Channel.ID != 0 {
			byID[view.Channel.ID] = view
		}
	}
	chats := make([]tg.ChatClass, 0, len(ids))
	for _, id := range ids {
		if view, ok := byID[id]; ok {
			chats = append(chats, tgChannelChatForView(userID, view))
		}
	}
	return chats
}

func (r *Router) peerObjectsForDraftUpdate(ctx context.Context, userID int64, peer domain.Peer) ([]tg.UserClass, []tg.ChatClass) {
	users := r.usersForDraftUpdate(ctx, userID, peer)
	chats := r.chatsForDraftUpdate(ctx, userID, peer)
	r.applyUsernamesToPeerObjects(ctx, users, chats)
	return users, chats
}

func (r *Router) peerObjectsForDrafts(ctx context.Context, userID int64, drafts []domain.DialogDraft) ([]tg.UserClass, []tg.ChatClass) {
	users := r.usersForDrafts(ctx, userID, drafts)
	chats := r.chatsForDrafts(ctx, userID, drafts)
	r.applyUsernamesToPeerObjects(ctx, users, chats)
	return users, chats
}

func dialogDraftErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrReplyMessageIDInvalid):
		return replyMessageIDInvalidErr()
	case errors.Is(err, domain.ErrChannelInvalid):
		return peerIDInvalidErr()
	default:
		return internalErr()
	}
}

func (r *Router) clearDraftAfterSend(ctx context.Context, userID int64, peer domain.Peer, replyTo *domain.MessageReply) {
	r.clearDraftAfterSendWithOptionalPeerObjects(ctx, userID, peer, replyTo, nil, nil, false)
}

func (r *Router) clearDraftAfterSendWithPeerObjects(ctx context.Context, userID int64, peer domain.Peer, replyTo *domain.MessageReply, users []tg.UserClass, chats []tg.ChatClass) {
	r.clearDraftAfterSendWithOptionalPeerObjects(ctx, userID, peer, replyTo, users, chats, true)
}

func (r *Router) clearDraftAfterSendWithOptionalPeerObjects(ctx context.Context, userID int64, peer domain.Peer, replyTo *domain.MessageReply, users []tg.UserClass, chats []tg.ChatClass, peerObjectsReady bool) {
	if r.deps.Dialogs == nil || userID == 0 || peer.ID == 0 {
		return
	}
	topMessageID := 0
	if peer.Type == domain.PeerTypeChannel && replyTo != nil && replyTo.TopMessageID > 0 {
		topMessageID = replyTo.TopMessageID
	}
	changed, err := r.deps.Dialogs.DeleteDraft(ctx, userID, peer, topMessageID)
	if err != nil {
		r.log.Debug("clear draft after send", zap.Int64("user_id", userID), zap.Error(err))
		return
	}
	if !changed {
		return
	}
	date := int(r.clock.Now().Unix())
	update := draftClearUpdate(peer, topMessageID, date)
	if update == nil {
		return
	}
	recorded := r.recordDraftMessageEvent(ctx, userID, peer, topMessageID, &date)
	r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
	if !peerObjectsReady {
		users, chats = r.peerObjectsForDraftUpdate(ctx, userID, peer)
	}
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
		Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, recorded),
		Users:   users,
		Chats:   chats,
		Date:    date,
		Seq:     0,
	})
}

func (r *Router) onMessagesGetDialogFilters(ctx context.Context) (*tg.MessagesDialogFilters, error) {
	if r.deps.Dialogs == nil {
		return tgDialogFilters(domain.DialogFolderList{}), nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	list, err := r.deps.Dialogs.GetDialogFolders(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return tgDialogFilters(list), nil
}

func (r *Router) onMessagesUpdateDialogFilter(ctx context.Context, req *tg.MessagesUpdateDialogFilterRequest) (bool, error) {
	if req.ID < domain.DialogCustomFolderMinID {
		return false, filterIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	filter, ok := req.GetFilter()
	var folder *domain.DialogFolder
	if ok {
		parsed, err := r.dialogFolderFromTG(ctx, userID, req.ID, filter)
		if err != nil {
			return false, err
		}
		folder = &parsed
		if r.deps.Dialogs != nil {
			if err := r.deps.Dialogs.SaveDialogFolder(ctx, userID, parsed); err != nil {
				return false, internalErr()
			}
		}
	} else if r.deps.Dialogs != nil {
		if err := r.deps.Dialogs.DeleteDialogFolder(ctx, userID, req.ID); err != nil {
			return false, internalErr()
		}
	}
	// Ordering convention: Save first (durable truth), then Record (incremental
	// notification). If Record fails the RPC returns an error, but the truth is
	// already persisted -- the only thing other devices lose is the immediate
	// update, and a restart/re-pull of getDialogFilters converges with no durable
	// drift. The reverse order (Record first) failing would let other devices apply
	// the new filter embedded in the update while the store still holds the old
	// value, then flap back on re-pull, so it is not acceptable.
	event := domain.UpdateEvent{
		Type:         domain.UpdateEventDialogFilter,
		FilterID:     req.ID,
		DialogFilter: folder,
		Date:         int(r.clock.Now().Unix()),
	}
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, _, err = r.deps.Updates.RecordDialogFilter(ctx, authKeyID, userID, req.ID, folder, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return false, internalErr()
		}
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, event)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, tgUpdateForOutboxEvent(event))
	return true, nil
}

func (r *Router) onMessagesUpdateDialogFiltersOrder(ctx context.Context, order []int) (bool, error) {
	if len(order) > domain.MaxDialogFolders {
		return false, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	clean := cleanDialogFilterOrder(order)
	if r.deps.Dialogs != nil {
		if err := r.deps.Dialogs.ReorderDialogFolders(ctx, userID, clean); err != nil {
			return false, internalErr()
		}
	}
	event := domain.UpdateEvent{
		Type:        domain.UpdateEventDialogFilterOrder,
		FilterOrder: clean,
		Date:        int(r.clock.Now().Unix()),
	}
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, _, err = r.deps.Updates.RecordDialogFilterOrder(ctx, authKeyID, userID, clean, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return false, internalErr()
		}
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, event)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, tgUpdateForOutboxEvent(event))
	return true, nil
}

func (r *Router) onMessagesToggleDialogFilterTags(ctx context.Context, enabled bool) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if r.deps.Dialogs != nil {
		if err := r.deps.Dialogs.ToggleDialogFolderTags(ctx, userID, enabled); err != nil {
			return false, internalErr()
		}
	}
	event := domain.UpdateEvent{
		Type:        domain.UpdateEventDialogFilters,
		TagsEnabled: enabled,
		Date:        int(r.clock.Now().Unix()),
	}
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, _, err = r.deps.Updates.RecordDialogFiltersReload(ctx, authKeyID, userID, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return false, internalErr()
		}
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, event)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, tgUpdateForOutboxEvent(event))
	return true, nil
}

func (r *Router) onMessagesGetPeerSettings(ctx context.Context, input tg.InputPeerClass) (*tg.MessagesPeerSettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	loadEpoch := r.peerSettingsProjectionCache.LoadEpoch()
	settings, ok := r.peerSettingsProjectionCache.Lookup(userID, peer)
	if !ok {
		settings, err = r.buildPeerSettingsProjection(ctx, userID, peer)
		if err != nil {
			return nil, err
		}
		r.peerSettingsProjectionCache.StoreIfEpoch(userID, peer, settings, loadEpoch)
	}
	users := r.peerSettingsUsers(ctx, userID, input)
	// messages.getPeerSettings is requested while a chat is opened and official
	// clients feed the returned peer objects into the same cache as getDialogs and
	// getFullUser. Keep these auxiliary objects on the common response-boundary
	// projection path: a full User without bot_verification_icon is authoritative
	// to TDesktop/Android/TWeb and would clear a badge learned from another RPC.
	r.applyPeerReadModels(ctx, userID, users, nil)
	return &tg.MessagesPeerSettings{
		Settings: tgPeerSettings(settings),
		Users:    users,
	}, nil
}

func (r *Router) buildPeerSettingsProjection(ctx context.Context, userID int64, peer domain.Peer) (domain.PeerSettings, error) {
	settings := domain.PeerSettings{}
	if r.deps.Contacts != nil {
		var err error
		settings, err = r.deps.Contacts.GetPeerSettings(ctx, userID, peer)
		if err != nil {
			return domain.PeerSettings{}, internalErr()
		}
	}
	if r.deps.Dialogs != nil {
		hidden, err := r.deps.Dialogs.PeerSettingsBarHidden(ctx, userID, peer)
		if err != nil {
			return domain.PeerSettings{}, internalErr()
		}
		settings.HiddenPeerSettingsBar = hidden
	}
	var err error
	settings, err = r.connectedBusinessBotPeerSettings(ctx, userID, peer, settings)
	if err != nil {
		return domain.PeerSettings{}, err
	}
	return settings, nil
}

func (r *Router) onMessagesToggleDialogPin(ctx context.Context, req *tg.MessagesToggleDialogPinRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	// Pinning of the archive folder row itself (TDesktop right-click Pin/Unpin on the archive row).
	if folderPeer, ok := req.Peer.(*tg.InputDialogPeerFolder); ok {
		return r.toggleArchiveFolderPin(ctx, userID, folderPeer.FolderID, req.GetPinned())
	}
	if community, ok, err := r.communityDialogPeerFromInput(ctx, userID, req.Peer); ok {
		if err != nil {
			return false, err
		}
		pinned := req.GetPinned()
		peer := domain.Peer{Type: domain.PeerTypeCommunity, ID: community.Community.ID}
		if pinned && !community.State.Pinned {
			if err := r.ensureCombinedPinCapacity(ctx, userID, domain.DialogMainFolderID, peer); err != nil {
				if errors.Is(err, domain.ErrPinnedDialogsTooMuch) {
					return false, pinnedTooMuchErr()
				}
				return false, internalErr()
			}
		}
		changed, err := r.deps.Communities.SetPinned(ctx, userID, community.Community.ID, pinned)
		if err != nil {
			return false, communityErr(err)
		}
		if !changed {
			return true, nil
		}
		if pinned {
			if err := r.promoteCombinedPinnedDialog(ctx, userID, domain.DialogMainFolderID, peer); err != nil {
				return false, internalErr()
			}
		}
		date := int(r.clock.Now().Unix())
		var recorded domain.UpdateEvent
		if r.deps.Updates != nil {
			authKeyID, _ := AuthKeyIDFrom(ctx)
			sessionID, _ := SessionIDFrom(ctx)
			event, state, err := r.deps.Updates.RecordDialogPinned(ctx, authKeyID, userID, peer, pinned, domain.DialogMainFolderID, rawAuthKeyIDForOrigin(ctx), sessionID)
			if err != nil {
				return false, internalErr()
			}
			date, recorded = state.Date, event
		}
		r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
		r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{&tg.UpdateDialogPinned{Pinned: pinned, Peer: tgDialogPeer(peer)}}, recorded),
			Chats:   []tg.ChatClass{tgCommunityChat(community)}, Date: date,
		})
		return true, nil
	}
	peers, err := r.dialogPeersFromInput(ctx, userID, []tg.InputDialogPeerClass{req.Peer})
	if err != nil {
		return false, err
	}
	if len(peers) != 1 {
		return false, peerIDInvalidErr()
	}
	pinned := req.GetPinned()
	if r.deps.Dialogs == nil {
		return true, nil
	}
	if pinned {
		folderID := domain.DialogMainFolderID
		current, err := r.deps.Dialogs.GetPeerDialogs(ctx, userID, peers)
		if err != nil {
			return false, internalErr()
		}
		for _, dialog := range current.Dialogs {
			if dialog.Peer == peers[0] {
				folderID = dialog.FolderID
				break
			}
		}
		if err := r.ensureCombinedPinCapacity(ctx, userID, folderID, peers[0]); err != nil {
			if errors.Is(err, domain.ErrPinnedDialogsTooMuch) {
				return false, pinnedTooMuchErr()
			}
			return false, internalErr()
		}
	}
	changed, folderID, err := r.deps.Dialogs.TogglePinned(ctx, userID, peers[0], pinned)
	if err != nil {
		if errors.Is(err, domain.ErrPinnedDialogsTooMuch) {
			return false, pinnedTooMuchErr()
		}
		return false, internalErr()
	}
	if changed {
		if pinned {
			if err := r.promoteCombinedPinnedDialog(ctx, userID, folderID, peers[0]); err != nil {
				return false, internalErr()
			}
		}
		date := int(r.clock.Now().Unix())
		var recorded domain.UpdateEvent
		if r.deps.Updates != nil {
			authKeyID, _ := AuthKeyIDFrom(ctx)
			sessionID, _ := SessionIDFrom(ctx)
			event, state, err := r.deps.Updates.RecordDialogPinned(ctx, authKeyID, userID, peers[0], pinned, folderID, rawAuthKeyIDForOrigin(ctx), sessionID)
			if err != nil {
				return false, internalErr()
			}
			date = state.Date
			recorded = event
		}
		update := &tg.UpdateDialogPinned{
			Pinned: pinned,
			Peer:   tgDialogPeer(peers[0]),
		}
		if folderID != domain.DialogMainFolderID {
			update.SetFolderID(folderID)
		}
		r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
		r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, recorded),
			Date:    date,
			Seq:     0,
		})
	}
	return true, nil
}

// toggleArchiveFolderPin handles toggleDialogPin(inputDialogPeerFolder):
// pin/unpin the archive folder row itself. The event peer is expressed as
// PeerTypeFolder, and the generated updateDialogPinned carries dialogPeerFolder
// without a folder_id flag.
func (r *Router) toggleArchiveFolderPin(ctx context.Context, userID int64, folderID int, pinned bool) (bool, error) {
	if folderID != domain.DialogArchiveFolderID {
		return false, folderIDInvalidErr()
	}
	if r.deps.Dialogs == nil {
		return true, nil
	}
	changed, err := r.deps.Dialogs.ToggleArchivePinned(ctx, userID, pinned)
	if err != nil {
		return false, internalErr()
	}
	if !changed {
		return true, nil
	}
	folderPeer := domain.Peer{Type: domain.PeerTypeFolder, ID: int64(folderID)}
	date := int(r.clock.Now().Unix())
	var recorded domain.UpdateEvent
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, state, err := r.deps.Updates.RecordDialogPinned(ctx, authKeyID, userID, folderPeer, pinned, 0, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return false, internalErr()
		}
		date = state.Date
		recorded = event
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
		Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{&tg.UpdateDialogPinned{
			Pinned: pinned,
			Peer:   &tg.DialogPeerFolder{FolderID: folderID},
		}}, recorded),
		Date: date,
		Seq:  0,
	})
	return true, nil
}

func (r *Router) onMessagesReorderPinnedDialogs(ctx context.Context, req *tg.MessagesReorderPinnedDialogsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	// The official server allows reordering pins separately in the main list (0)
	// and the archive (1); custom filters have no server-side pin order.
	if req.FolderID != 0 && req.FolderID != domain.DialogArchiveFolderID {
		return false, folderIDInvalidErr()
	}
	if len(req.Order) > maxDialogInputPeers {
		return false, limitInvalidErr()
	}
	peers, err := r.dialogPeersFromInput(ctx, userID, req.Order)
	if err != nil {
		return false, err
	}
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, peer := range peers {
		if _, duplicate := seen[peer]; duplicate {
			return false, peerIDInvalidErr()
		}
		seen[peer] = struct{}{}
		if req.FolderID != domain.DialogMainFolderID && peer.Type == domain.PeerTypeCommunity {
			return false, folderIDInvalidErr()
		}
	}
	if len(peers) > domain.PinnedDialogsLimit(req.FolderID, r.userIsPremium(ctx, userID)) {
		return false, pinnedTooMuchErr()
	}
	if r.deps.Dialogs == nil && r.deps.Communities == nil {
		return true, nil
	}
	changed := false
	if r.deps.Dialogs != nil {
		dialogsChanged, err := r.deps.Dialogs.ReorderPinned(ctx, userID, req.FolderID, peers, req.GetForce())
		if err != nil {
			return false, internalErr()
		}
		changed = dialogsChanged
	}
	if r.deps.Communities != nil && req.FolderID == domain.DialogMainFolderID {
		communitiesChanged, err := r.deps.Communities.ReorderPinned(ctx, userID, peers, req.GetForce())
		if err != nil {
			return false, communityErr(err)
		}
		changed = changed || communitiesChanged
	}
	if !changed {
		return true, nil
	}
	date := int(r.clock.Now().Unix())
	var recorded domain.UpdateEvent
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, state, err := r.deps.Updates.RecordPinnedDialogs(ctx, authKeyID, userID, req.FolderID, peers, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return false, internalErr()
		}
		date = state.Date
		recorded = event
	}
	update := &tg.UpdatePinnedDialogs{Order: tgDialogPeers(peers)}
	if req.FolderID != domain.DialogMainFolderID {
		update.SetFolderID(req.FolderID)
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
		Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, recorded),
		Date:    date,
		Seq:     0,
	})
	return true, nil
}

func (r *Router) onMessagesMarkDialogUnread(ctx context.Context, req *tg.MessagesMarkDialogUnreadRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if parentPeer, ok := req.GetParentPeer(); ok {
		if err := r.validateDialogUnreadParentPeer(ctx, userID, parentPeer); err != nil {
			return false, err
		}
		peers, err := r.dialogPeersFromInput(ctx, userID, []tg.InputDialogPeerClass{req.Peer})
		if err != nil {
			return false, err
		}
		if len(peers) != 1 {
			return false, peerIDInvalidErr()
		}
		return true, nil
	}
	peers, err := r.dialogPeersFromInput(ctx, userID, []tg.InputDialogPeerClass{req.Peer})
	if err != nil {
		return false, err
	}
	if len(peers) != 1 {
		return false, peerIDInvalidErr()
	}
	unread := req.GetUnread()
	if r.deps.Dialogs == nil {
		return true, nil
	}
	changed, err := r.deps.Dialogs.MarkUnread(ctx, userID, peers[0], unread)
	if err != nil {
		return false, internalErr()
	}
	if changed {
		date := int(r.clock.Now().Unix())
		var recorded domain.UpdateEvent
		if r.deps.Updates != nil {
			authKeyID, _ := AuthKeyIDFrom(ctx)
			sessionID, _ := SessionIDFrom(ctx)
			event, state, err := r.deps.Updates.RecordDialogUnreadMark(ctx, authKeyID, userID, peers[0], unread, rawAuthKeyIDForOrigin(ctx), sessionID)
			if err != nil {
				return false, internalErr()
			}
			date = state.Date
			recorded = event
		}
		r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
		r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{&tg.UpdateDialogUnreadMark{
				Unread: unread,
				Peer:   tgDialogPeer(peers[0]),
			}}, recorded),
			Date: date,
			Seq:  0,
		})
	}
	return true, nil
}

func (r *Router) onMessagesGetDialogUnreadMarks(ctx context.Context, req *tg.MessagesGetDialogUnreadMarksRequest) ([]tg.DialogPeerClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if parentPeer, ok := req.GetParentPeer(); ok {
		if err := r.validateDialogUnreadParentPeer(ctx, userID, parentPeer); err != nil {
			return nil, err
		}
		return []tg.DialogPeerClass{}, nil
	}
	if r.deps.Dialogs == nil {
		return nil, nil
	}
	peers, err := r.deps.Dialogs.UnreadMarks(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return tgDialogPeers(peers), nil
}

func (r *Router) validateDialogUnreadParentPeer(ctx context.Context, userID int64, parentPeer tg.InputPeerClass) error {
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, parentPeer)
	if err != nil {
		return parentPeerInvalidErr()
	}
	if peer.Type != domain.PeerTypeChannel {
		return parentPeerInvalidErr()
	}
	return nil
}

func (r *Router) onMessagesHidePeerSettingsBar(ctx context.Context, input tg.InputPeerClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return false, err
	}
	changed := true
	if r.deps.Dialogs != nil {
		var err error
		changed, err = r.deps.Dialogs.HidePeerSettingsBar(ctx, userID, peer)
		if err != nil {
			return false, internalErr()
		}
	}
	if !changed {
		return true, nil
	}
	r.invalidateRPCProjectionForPeer(userID, peer)
	date := int(r.clock.Now().Unix())
	var recorded domain.UpdateEvent
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, state, err := r.deps.Updates.RecordPeerSettings(ctx, authKeyID, userID, peer, domain.PeerSettings{HiddenPeerSettingsBar: true}, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return false, internalErr()
		}
		date = state.Date
		recorded = event
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
		Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{&tg.UpdatePeerSettings{
			Peer:     tgPeer(peer),
			Settings: tg.PeerSettings{},
		}}, recorded),
		Date: date,
		Seq:  0,
	})
	return true, nil
}

func (r *Router) dialogFilterFromRequest(ctx context.Context, userID int64, req *tg.MessagesGetDialogsRequest) (domain.DialogFilter, error) {
	limit := req.Limit
	if limit > 500 {
		limit = 500
	}
	filter := domain.DialogFilter{
		ExcludePinned: req.ExcludePinned,
		OffsetDate:    req.OffsetDate,
		OffsetID:      req.OffsetID,
		Limit:         limit,
		Hash:          req.Hash,
	}
	if folderID, ok := req.GetFolderID(); ok {
		filter.HasFolderID = true
		filter.FolderID = folderID
	}
	// offset_peer is only the stable tie-breaker paired with offset_date/id. It
	// neither grants access nor selects content, so resolving a channel view (and
	// validating its access_hash) here adds database work without a security
	// boundary. Content-bearing peer arguments continue through checkedDomainPeer.
	if _, empty := req.OffsetPeer.(*tg.InputPeerEmpty); !empty && !inputPeerClassNil(req.OffsetPeer) {
		peer, ok := r.domainPeerFromInputPeer(userID, req.OffsetPeer)
		if !ok || peer.ID <= 0 {
			return domain.DialogFilter{}, peerIDInvalidErr()
		}
		filter.HasOffsetPeer = true
		filter.OffsetPeer = peer
	}
	return filter, nil
}

func (r *Router) dialogPeersFromInput(ctx context.Context, userID int64, items []tg.InputDialogPeerClass) ([]domain.Peer, error) {
	if len(items) > maxDialogInputPeers {
		return nil, limitInvalidErr()
	}
	peers := make([]domain.Peer, 0, len(items))
	hasFolder := false
	for _, item := range items {
		switch p := item.(type) {
		case *tg.InputDialogPeerCommunity:
			view, ok, err := r.communityDialogPeerFromInput(ctx, userID, p)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, inputConstructorInvalidErr()
			}
			peers = append(peers, domain.Peer{Type: domain.PeerTypeCommunity, ID: view.Community.ID})
		case *tg.InputDialogPeer:
			peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, p.Peer)
			if err != nil {
				return nil, err
			}
			peers = append(peers, peer)
		case *tg.InputDialogPeerFolder:
			if hasFolder {
				return nil, folderIDInvalidErr()
			}
			hasFolder = true
			// Phase one does not maintain archived/folder dialogs. If the request
			// also contains ordinary peers, return ordinary peers preferentially per
			// the Telegram Business path; a pure folder request returns an empty summary.
		default:
			return nil, inputConstructorInvalidErr()
		}
	}
	return peers, nil
}

func (r *Router) dialogFolderFromTG(ctx context.Context, userID int64, id int, filter tg.DialogFilterClass) (domain.DialogFolder, error) {
	if id < domain.DialogCustomFolderMinID {
		return domain.DialogFolder{}, filterIDInvalidErr()
	}
	switch f := filter.(type) {
	case *tg.DialogFilter:
		title := f.Title.Text
		if title == "" {
			return domain.DialogFolder{}, filterTitleEmptyErr()
		}
		if utf8.RuneCountInString(title) > domain.MaxDialogFolderTitleRunes {
			return domain.DialogFolder{}, limitInvalidErr()
		}
		pinned, err := r.dialogFolderPeersFromInput(ctx, userID, f.PinnedPeers)
		if err != nil {
			return domain.DialogFolder{}, err
		}
		include, err := r.dialogFolderPeersFromInput(ctx, userID, f.IncludePeers)
		if err != nil {
			return domain.DialogFolder{}, err
		}
		exclude, err := r.dialogFolderPeersFromInput(ctx, userID, f.ExcludePeers)
		if err != nil {
			return domain.DialogFolder{}, err
		}
		emoticon, hasEmoticon := f.GetEmoticon()
		color, hasColor := f.GetColor()
		// The official server requires a filter to have at least one include
		// condition (a category switch or an include/pinned peer), otherwise it
		// never matches any dialog; the TDesktop UI never sends an empty filter, so
		// this is a fallback rejection (FILTER_INCLUDE_EMPTY).
		if !f.Contacts && !f.NonContacts && !f.Groups && !f.Broadcasts && !f.Bots &&
			len(include) == 0 && len(pinned) == 0 {
			return domain.DialogFolder{}, filterIncludeEmptyErr()
		}
		return domain.DialogFolder{
			ID:              id,
			Contacts:        f.Contacts,
			NonContacts:     f.NonContacts,
			Groups:          f.Groups,
			Broadcasts:      f.Broadcasts,
			Bots:            f.Bots,
			ExcludeMuted:    f.ExcludeMuted,
			ExcludeRead:     f.ExcludeRead,
			ExcludeArchived: f.ExcludeArchived,
			TitleNoanimate:  f.TitleNoanimate,
			Title:           title,
			TitleEntities:   domainMessageEntities(f.Title.Entities),
			Emoticon:        emoticon,
			HasEmoticon:     hasEmoticon,
			Color:           color,
			HasColor:        hasColor,
			PinnedPeers:     pinned,
			IncludePeers:    include,
			ExcludePeers:    exclude,
		}, nil
	case *tg.DialogFilterChatlist:
		title := f.Title.Text
		if title == "" {
			return domain.DialogFolder{}, filterTitleEmptyErr()
		}
		if utf8.RuneCountInString(title) > domain.MaxDialogFolderTitleRunes {
			return domain.DialogFolder{}, limitInvalidErr()
		}
		pinned, err := r.dialogFolderPeersFromInput(ctx, userID, f.PinnedPeers)
		if err != nil {
			return domain.DialogFolder{}, err
		}
		include, err := r.dialogFolderPeersFromInput(ctx, userID, f.IncludePeers)
		if err != nil {
			return domain.DialogFolder{}, err
		}
		emoticon, hasEmoticon := f.GetEmoticon()
		color, hasColor := f.GetColor()
		return domain.DialogFolder{
			ID:             id,
			HasMyInvites:   f.HasMyInvites,
			TitleNoanimate: f.TitleNoanimate,
			Title:          title,
			TitleEntities:  domainMessageEntities(f.Title.Entities),
			Emoticon:       emoticon,
			HasEmoticon:    hasEmoticon,
			Color:          color,
			HasColor:       hasColor,
			PinnedPeers:    pinned,
			IncludePeers:   include,
			IsChatlist:     true,
		}, nil
	default:
		return domain.DialogFolder{}, inputConstructorInvalidErr()
	}
}

func (r *Router) dialogFolderPeersFromInput(ctx context.Context, userID int64, peers []tg.InputPeerClass) ([]domain.DialogFolderPeer, error) {
	if len(peers) > domain.MaxDialogFolderPeers {
		return nil, limitInvalidErr()
	}
	out := make([]domain.DialogFolderPeer, 0, len(peers))
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, input := range peers {
		peer, accessHash, err := r.domainFolderPeerFromInputPeer(ctx, userID, input)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		out = append(out, domain.DialogFolderPeer{Peer: peer, AccessHash: accessHash})
	}
	return out, nil
}

func cleanDialogFilterOrder(order []int) []int {
	out := make([]int, 0, len(order))
	seen := make(map[int]struct{}, len(order))
	for _, id := range order {
		if id < domain.DialogCustomFolderMinID {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func (r *Router) peerSettingsUsers(ctx context.Context, userID int64, peer tg.InputPeerClass) []tg.UserClass {
	if r.deps.Users == nil {
		return nil
	}
	switch p := peer.(type) {
	case *tg.InputPeerSelf:
		u, err := r.deps.Users.Self(ctx, userID)
		if err == nil && u.ID != 0 {
			return []tg.UserClass{r.tgSelfUser(u)}
		}
	case *tg.InputPeerUser:
		u, found, err := r.deps.Users.ByID(ctx, userID, p.UserID)
		if err == nil && found {
			return []tg.UserClass{r.tgUser(u)}
		}
	}
	return nil
}
