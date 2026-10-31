package rpc

import (
	"context"
	"errors"
	"sort"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// resolveMonoforumForAdmin resolves the parent_peer of TDesktop
// messages.getSavedDialogs/getSavedHistory and verifies the current user can
// manage the parent broadcast channel's Direct Messages. TDesktop's SavedSublist
// actually passes parentChat()->input() as parent_peer; depending on the client
// materialization path it may be either the monoforum virtual channel or the
// parent broadcast channel linked to it. So both wire peers are normalized to the
// same monoforum here, and authorization only recognizes the parent channel's
// creator / ManageDirectMessages -- an ordinary admin must never be let into the
// manager view.
//
// Returns (monoforum channel, isMonoforum, err): when parent is a valid ordinary
// channel not linked to Direct Messages it returns (zero, false, nil), leaving a
// benign empty response to the caller; a non-manager of a linked channel gets
// CHAT_ADMIN_REQUIRED.
func (r *Router) resolveMonoforumForAdmin(ctx context.Context, userID int64, parent domain.Peer) (domain.Channel, bool, error) {
	if r.deps.Channels == nil {
		return domain.Channel{}, false, notImplementedErr()
	}
	if parent.Type != domain.PeerTypeChannel || parent.ID == 0 {
		return domain.Channel{}, false, parentPeerInvalidErr()
	}
	mono, isAdmin, err := r.deps.Channels.ResolveMonoforumSend(ctx, userID, parent.ID)
	if errors.Is(err, domain.ErrChannelInvalid) {
		// TDesktop's current Direct Messages subsection passes the parent
		// broadcast channel. Only an explicit linked_monoforum relation is
		// accepted; an arbitrary ordinary channel must not be guessed as a
		// monoforum.
		views, viewErr := r.deps.Channels.GetChannels(ctx, userID, []int64{parent.ID})
		if viewErr != nil {
			return domain.Channel{}, false, internalErr()
		}
		if len(views) != 1 {
			return domain.Channel{}, false, nil
		}
		parentChannel := views[0].Channel
		if parentChannel.ID != parent.ID || parentChannel.Deleted || parentChannel.Monoforum || parentChannel.LinkedMonoforumID == 0 {
			return domain.Channel{}, false, nil
		}
		mono, isAdmin, err = r.deps.Channels.ResolveMonoforumSend(ctx, userID, parentChannel.LinkedMonoforumID)
		if err != nil {
			// A visible parent that advertises linked_monoforum_id but cannot resolve
			// that target violates the durable channel-link invariant. Do not disguise
			// it as an ordinary channel probe.
			return domain.Channel{}, false, internalErr()
		}
	}
	if err != nil {
		if errors.Is(err, domain.ErrChannelInvalid) {
			return domain.Channel{}, false, nil
		}
		return domain.Channel{}, false, internalErr()
	}
	if !isAdmin {
		// It is a monoforum, but the caller is not a parent-channel manager:
		// reading the private-message list/history is managers-only.
		return domain.Channel{}, false, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	return mono, true, nil
}

// monoforumSavedDialogs returns the monoforum's subscriber sub-dialog list (the manager-view private-message list). mono is already resolved and authorized.
func (r *Router) monoforumSavedDialogs(ctx context.Context, userID int64, mono domain.Channel, limit, offsetID int) (tg.MessagesSavedDialogsClass, error) {
	list, err := r.deps.Channels.ListMonoforumDialogs(ctx, domain.MonoforumDialogsFilter{
		MonoforumID: mono.ID,
		Limit:       limit,
		OffsetID:    offsetID,
	})
	if err != nil {
		return nil, internalErr()
	}
	dialogs := make([]tg.SavedDialogClass, 0, len(list.Dialogs))
	for _, d := range list.Dialogs {
		peer := tgPeer(d.SavedPeer)
		if peer == nil {
			continue
		}
		md := &tg.MonoForumDialog{
			Peer:            peer,
			TopMessage:      d.TopMessageID,
			ReadInboxMaxID:  d.ReadInboxMaxID,
			ReadOutboxMaxID: d.ReadOutboxMaxID,
			UnreadCount:     d.UnreadCount,
		}
		dialogs = append(dialogs, md)
	}
	messages := make([]tg.MessageClass, 0, len(list.Messages))
	for _, m := range list.Messages {
		if item := tgChannelMessage(userID, m); item != nil {
			messages = append(messages, item)
		}
	}
	users, err := r.monoforumSubscriberUsersWithPeerCacheAndOverlaysStrict(ctx, userID, list.Dialogs, list.Messages, nil, nil)
	if err != nil {
		return nil, internalErr()
	}
	return &tg.MessagesSavedDialogs{
		Dialogs:  dialogs,
		Messages: messages,
		Chats:    r.monoforumChats(ctx, userID, mono),
		Users:    users,
	}, nil
}

// monoforumSavedHistory returns a subscriber's private-message history within the monoforum. mono is already resolved and authorized.
func (r *Router) monoforumSavedHistory(ctx context.Context, userID int64, mono domain.Channel, savedPeer domain.Peer, limit, offsetID int) (tg.MessagesMessagesClass, error) {
	if savedPeer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	hist, err := r.deps.Channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{
		MonoforumID: mono.ID,
		SavedPeer:   savedPeer,
		Limit:       limit,
		OffsetID:    offsetID,
	})
	if err != nil {
		return nil, internalErr()
	}
	messages := make([]tg.MessageClass, 0, len(hist.Messages))
	for _, m := range hist.Messages {
		if item := tgChannelMessage(userID, m); item != nil {
			messages = append(messages, item)
		}
	}
	users, err := r.monoforumSubscriberUsersWithPeerCacheAndOverlaysStrict(ctx, userID, nil, hist.Messages, nil, nil)
	if err != nil {
		return nil, internalErr()
	}
	result := &tg.MessagesMessagesSlice{
		Count:    hist.Count,
		Messages: messages,
		Chats:    r.monoforumChats(ctx, userID, mono),
		Users:    users,
	}
	r.applyPeerReadModelsToMessages(ctx, userID, result)
	return result, nil
}

// monoforumChats projects the channels a client needs to materialize monoforum
// private messages: the monoforum itself is projected directly, then the parent
// broadcast channel is added per viewer. Subscribers have no monoforum member
// row, and manager status only comes from the parent channel.
func (r *Router) monoforumChats(ctx context.Context, userID int64, mono domain.Channel) []tg.ChatClass {
	chats := []tg.ChatClass{tgChannelChatForView(userID, domain.ChannelView{Channel: mono})}
	if mono.LinkedMonoforumID != 0 && r.deps.Channels != nil {
		if views, err := r.deps.Channels.GetChannels(ctx, userID, []int64{mono.LinkedMonoforumID}); err == nil {
			for _, view := range views {
				if view.Channel.ID != 0 {
					chats = append(chats, tgChannelChatForView(userID, view))
				}
			}
		}
	}
	return chats
}

type monoforumPeerOverlays struct {
	usernames        map[domain.Peer][]domain.Username
	botProfiles      map[int64]domain.BotProfile
	botVerifications map[domain.Peer]domain.CustomVerification
}

func monoforumSubscriberUserIDs(dialogs []domain.MonoforumDialog, messages []domain.ChannelMessage) []int64 {
	ids := make([]int64, 0, len(dialogs)+len(messages))
	seen := map[int64]struct{}{}
	add := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, d := range dialogs {
		if d.SavedPeer.Type == domain.PeerTypeUser {
			add(d.SavedPeer.ID)
		}
	}
	for _, m := range messages {
		add(m.SenderUserID)
	}
	// tgChannelMessage can reference users beyond its sender (from/send_as,
	// forward, via_bot, reply/quote mention, contact/poll/todo/action/reaction).
	// Reuse the common channel-message closure so saved history, send echo and
	// online fan-out all materialize the same complete Users envelope.
	userIDs := make(map[int64]struct{})
	channelIDs := make(map[int64]struct{})
	for _, message := range messages {
		collectChannelMessagePeerRefs(message, message.ChannelID, userIDs, channelIDs)
	}
	extra := peerIDMapKeys(userIDs)
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	for _, id := range extra {
		add(id)
	}
	return ids
}

func monoforumProjectionPeers(monoforumID, parentID int64, userIDs []int64) []domain.Peer {
	peers := make([]domain.Peer, 0, len(userIDs)+2)
	seen := make(map[domain.Peer]struct{}, len(userIDs)+2)
	add := func(peer domain.Peer) {
		if peer.ID == 0 {
			return
		}
		if _, ok := seen[peer]; ok {
			return
		}
		seen[peer] = struct{}{}
		peers = append(peers, peer)
	}
	for _, userID := range userIDs {
		add(domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	}
	add(domain.Peer{Type: domain.PeerTypeChannel, ID: monoforumID})
	add(domain.Peer{Type: domain.PeerTypeChannel, ID: parentID})
	return peers
}

// loadMonoforumPeerOverlays resolves peer-wide facts once before a fan-out. The
// returned snapshot is immutable for the lifetime of that job and can therefore
// be reused by every viewer builder without turning overlays into N+1 reads.
func (r *Router) loadMonoforumPeerOverlays(ctx context.Context, peers []domain.Peer) *monoforumPeerOverlays {
	overlays := &monoforumPeerOverlays{
		usernames:        r.usernameRegistryMap(ctx, peers),
		botVerifications: r.botVerificationMap(ctx, peers),
	}
	if r.deps.Bots == nil {
		return overlays
	}
	userIDs := make([]int64, 0, len(peers))
	for _, peer := range peers {
		if peer.Type == domain.PeerTypeUser && peer.ID != 0 {
			userIDs = append(userIDs, peer.ID)
		}
	}
	if len(userIDs) == 0 {
		return overlays
	}
	if batch, ok := r.deps.Bots.(botProfileBatchResolver); ok {
		if profiles, err := batch.BotInfos(ctx, userIDs); err == nil {
			overlays.botProfiles = profiles
			return overlays
		}
	}
	overlays.botProfiles = make(map[int64]domain.BotProfile)
	for _, userID := range userIDs {
		if profile, found, err := r.deps.Bots.BotInfo(ctx, userID); err == nil && found {
			overlays.botProfiles[userID] = profile
		}
	}
	return overlays
}

func applyMonoforumPeerOverlays(users []tg.UserClass, chats []tg.ChatClass, overlays *monoforumPeerOverlays) {
	if overlays == nil {
		return
	}
	applyUsernamesFromRegistry(users, chats, overlays.usernames)
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok || u == nil || u.Deleted {
			continue
		}
		if u.Bot {
			if profile, found := overlays.botProfiles[u.ID]; found {
				applyBotProfileFlags(u, profile)
			}
		}
		if mark, found := overlays.botVerifications[domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}]; found && mark.IconDocumentID > 0 {
			u.SetBotVerificationIcon(mark.IconDocumentID)
		}
	}
	for _, item := range chats {
		ch, ok := item.(*tg.Channel)
		if !ok || ch == nil {
			continue
		}
		if mark, found := overlays.botVerifications[domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}]; found && mark.IconDocumentID > 0 {
			ch.SetBotVerificationIcon(mark.IconDocumentID)
		}
	}
}

// monoforumSubscriberUsers projects subscriber users (saved_peer + message
// senders) for one viewer. Fan-out callers pass their preheated peer cache and
// overlay snapshot; single-viewer callers retain the same output shape through a
// one-shot local cache. The pure TL conversion is viewer-aware so self is never
// lost for a subscriber viewing their own envelope.
func (r *Router) monoforumSubscriberUsersWithPeerCacheAndOverlaysStrict(ctx context.Context, userID int64, dialogs []domain.MonoforumDialog, messages []domain.ChannelMessage, cache *viewerPeerCache, overlays *monoforumPeerOverlays) ([]tg.UserClass, error) {
	ids := monoforumSubscriberUserIDs(dialogs, messages)
	if len(ids) == 0 {
		return []tg.UserClass{}, nil
	}
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	projected, err := cache.usersForIDsStrict(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	if overlays == nil {
		overlays = r.loadMonoforumPeerOverlays(ctx, monoforumProjectionPeers(0, 0, ids))
	}
	out := tgUsersForViewer(userID, projected)
	applyMonoforumPeerOverlays(out, nil, overlays)
	return out, nil
}

// monoforumReplyPresent reports whether sendMessage's reply_to explicitly carries
// monoforum_peer_id. An admin reply must carry the target subscriber; an ordinary
// subscriber does not carry reply_to per official TDesktop behavior, and the
// target is derived by the caller.
func monoforumReplyPresent(input tg.InputReplyToClass) bool {
	switch v := input.(type) {
	case *tg.InputReplyToMonoForum:
		return v != nil
	case *tg.InputReplyToMessage:
		if v == nil {
			return false
		}
		_, ok := v.GetMonoforumPeerID()
		return ok
	default:
		return false
	}
}

// monoforumReplyTargetPeer resolves the subscriber sub-dialog peer from reply_to's monoforum_peer_id.
func (r *Router) monoforumReplyTargetPeer(userID int64, input tg.InputReplyToClass) (domain.Peer, bool) {
	var inputPeer tg.InputPeerClass
	switch v := input.(type) {
	case *tg.InputReplyToMonoForum:
		if v != nil {
			inputPeer = v.MonoforumPeerID
		}
	case *tg.InputReplyToMessage:
		if v != nil {
			if p, ok := v.GetMonoforumPeerID(); ok {
				inputPeer = p
			}
		}
	}
	if inputPeer == nil {
		return domain.Peer{}, false
	}
	return r.domainPeerFromInputPeer(userID, inputPeer)
}

func (r *Router) monoforumSavedPeerForSender(userID int64, isAdmin bool, replyTo tg.InputReplyToClass) (domain.Peer, error) {
	savedPeer := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
	if monoforumReplyPresent(replyTo) {
		var valid bool
		savedPeer, valid = r.monoforumReplyTargetPeer(userID, replyTo)
		if !valid || savedPeer.Type != domain.PeerTypeUser || savedPeer.ID == 0 {
			return domain.Peer{}, replyToMonoforumPeerInvalidErr()
		}
	} else if isAdmin {
		return domain.Peer{}, replyToMonoforumPeerInvalidErr()
	}
	if !isAdmin && savedPeer.ID != userID {
		return domain.Peer{}, replyToMonoforumPeerInvalidErr()
	}
	return savedPeer, nil
}

// monoforumMessageReplyFromInput separates the sub-dialog selector from the actual message reply.
// InputReplyToMonoForum only selects a subscriber; InputReplyToMessage may carry both the selector
// and a real reply_to_msg_id, so clear flags.5 before reusing the common structural validator.
func (r *Router) monoforumMessageReplyFromInput(ctx context.Context, userID int64, peer domain.Peer, input tg.InputReplyToClass) (*domain.MessageReply, error) {
	switch value := input.(type) {
	case nil, *tg.InputReplyToMonoForum:
		return nil, nil
	case *tg.InputReplyToMessage:
		if value == nil {
			return nil, nil
		}
		clean := *value
		clean.Flags.Unset(5)
		clean.MonoforumPeerID = nil
		return r.messageReplyFromInput(ctx, userID, peer, &clean)
	default:
		return r.messageReplyFromInput(ctx, userID, peer, input)
	}
}

// sendMonoforumMessage handles sending to channel private messages (monoforum):
// a subscriber sends to their own sub-dialog, an admin replies to the target
// subscriber. saved_peer is derived by the caller for a subscriber and comes from
// reply_to for an admin; an admin may write any subscriber's sub-dialog, a
// subscriber only its own.
func (r *Router) sendMonoforumMessage(ctx context.Context, userID int64, peer domain.Peer, mono domain.Channel, isAdmin bool, req domain.SendMonoforumMessageRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	if mono.ID != peer.ID || !mono.Monoforum || req.SavedPeer.Type != domain.PeerTypeUser || req.SavedPeer.ID == 0 {
		return nil, replyToMonoforumPeerInvalidErr()
	}
	if !isAdmin && req.SavedPeer.ID != userID {
		// An ordinary subscriber may only write its own sub-dialog, not others'.
		return nil, replyToMonoforumPeerInvalidErr()
	}
	req.MonoforumID = mono.ID
	req.SenderUserID = userID
	if req.Date == 0 {
		req.Date = int(r.clock.Now().Unix())
	}
	res, err := r.deps.Channels.SendMonoforumMessage(ctx, req)
	if err != nil {
		return nil, messageSendErr(err)
	}
	if req.ClearDraft {
		r.clearDraftAfterSend(ctx, userID, peer, req.ReplyTo)
	}
	if !res.Duplicate {
		r.enqueueMonoforumMessageFanout(ctx, userID, mono, req.SavedPeer, res)
	}
	return r.monoforumSendUpdatesStrict(ctx, userID, mono, req.SavedPeer, res)
}

// monoforumSendUpdates builds the sender's echo Updates: updateMessageID (tying
// the random_id) + updateNewChannelMessage (monoforum uses channel pts). The
// other side receives this durable event via the monoforum channel's
// getChannelDifference.
func (r *Router) monoforumSendUpdatesStrict(ctx context.Context, userID int64, mono domain.Channel, savedPeer domain.Peer, res domain.SendChannelMessageResult) (tg.UpdatesClass, error) {
	return r.monoforumSendUpdatesWithPeerCacheAndOverlaysStrict(ctx, userID, mono, savedPeer, res, nil, nil)
}

func (r *Router) monoforumSendUpdatesWithPeerCacheAndOverlays(ctx context.Context, userID int64, mono domain.Channel, savedPeer domain.Peer, res domain.SendChannelMessageResult, cache *viewerPeerCache, overlays *monoforumPeerOverlays) tg.UpdatesClass {
	updates, _ := r.monoforumSendUpdatesWithPeerCacheAndOverlaysStrict(ctx, userID, mono, savedPeer, res, cache, overlays)
	return updates
}

func (r *Router) monoforumSendUpdatesWithPeerCacheAndOverlaysStrict(ctx context.Context, userID int64, mono domain.Channel, savedPeer domain.Peer, res domain.SendChannelMessageResult, cache *viewerPeerCache, overlays *monoforumPeerOverlays) (tg.UpdatesClass, error) {
	updates := make([]tg.UpdateClass, 0, 3)
	if res.Message.RandomID != 0 {
		updates = append(updates, &tg.UpdateMessageID{ID: res.Message.ID, RandomID: res.Message.RandomID})
	}
	newMsg := &tg.UpdateNewChannelMessage{Pts: res.Event.Pts, PtsCount: res.Event.PtsCount}
	if channelMsg := tgChannelMessage(userID, res.Message); channelMsg != nil {
		newMsg.Message = channelMsg
	} else {
		newMsg.Message = &tg.MessageEmpty{ID: res.Message.ID}
	}
	updates = append(updates, newMsg)
	if res.SenderStarsBalance != nil && res.Message.SenderUserID == userID {
		updates = append(updates, &tg.UpdateStarsBalance{
			Balance: &tg.StarsAmount{Amount: res.SenderStarsBalance.Balance},
		})
	}
	date := int(r.clock.Now().Unix())
	if res.Duplicate && res.ReplayDeleteEvent != nil {
		if deleted := tgChannelUpdate(userID, *res.ReplayDeleteEvent); deleted != nil {
			updates = append(updates, deleted)
		}
		if res.ReplayDeleteEvent.Date > date {
			date = res.ReplayDeleteEvent.Date
		}
	}
	dialogs := []domain.MonoforumDialog{{SavedPeer: savedPeer}}
	messages := []domain.ChannelMessage{res.Message}
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	if overlays == nil {
		ids := monoforumSubscriberUserIDs(dialogs, messages)
		overlays = r.loadMonoforumPeerOverlays(ctx, monoforumProjectionPeers(mono.ID, mono.LinkedMonoforumID, ids))
	}
	chats := r.monoforumChats(ctx, userID, mono)
	users, err := r.monoforumSubscriberUsersWithPeerCacheAndOverlaysStrict(ctx, userID, dialogs, messages, cache, overlays)
	if err != nil {
		return nil, err
	}
	applyMonoforumPeerOverlays(nil, chats, overlays)
	return &tg.Updates{
		Updates: updates,
		Chats:   chats,
		Users:   users,
		Date:    date,
	}, nil
}

func (r *Router) monoforumDeliveryUpdates(ctx context.Context, userID int64, mono domain.Channel, savedPeer domain.Peer, res domain.SendChannelMessageResult) *tg.Updates {
	return r.monoforumDeliveryUpdatesWithPeerCacheAndOverlays(ctx, userID, mono, savedPeer, res, nil, nil)
}

func (r *Router) monoforumDeliveryUpdatesWithPeerCacheAndOverlays(ctx context.Context, userID int64, mono domain.Channel, savedPeer domain.Peer, res domain.SendChannelMessageResult, cache *viewerPeerCache, overlays *monoforumPeerOverlays) *tg.Updates {
	updates, _ := r.monoforumSendUpdatesWithPeerCacheAndOverlays(ctx, userID, mono, savedPeer, res, cache, overlays).(*tg.Updates)
	if updates == nil {
		return nil
	}
	filtered := make([]tg.UpdateClass, 0, len(updates.Updates))
	for _, update := range updates.Updates {
		if _, randomMapping := update.(*tg.UpdateMessageID); !randomMapping {
			filtered = append(filtered, update)
		}
	}
	updates.Updates = filtered
	return updates
}
