package rpc

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) onChannelsExportMessageLink(ctx context.Context, req *tg.ChannelsExportMessageLinkRequest) (*tg.ExportedMessageLink, error) {
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	userID, view, err := r.channelView(ctx, req.Channel)
	if err != nil {
		return nil, err
	}
	history, err := r.deps.Channels.GetMessages(ctx, userID, view.Channel.ID, []int{req.ID})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	if len(history.Messages) != 1 || history.Messages[0].ID != req.ID {
		return nil, messageIDInvalidErr()
	}
	link := ""
	if view.Channel.Username != "" {
		link = r.publicLink(view.Channel.Username + "/" + strconv.Itoa(req.ID))
	} else {
		link = r.publicLink("c/" + strconv.FormatInt(view.Channel.ID, 10) + "/" + strconv.Itoa(req.ID))
	}
	if req.Thread {
		if rootID := channelMessageThreadRootID(history.Messages[0]); rootID > 0 && rootID != req.ID {
			link += "?thread=" + strconv.Itoa(rootID)
		}
	}
	return &tg.ExportedMessageLink{Link: link, HTML: ""}, nil
}

func channelMessageThreadRootID(msg domain.ChannelMessage) int {
	if msg.ReplyTo == nil {
		return 0
	}
	if msg.ReplyTo.TopMessageID > 0 {
		return msg.ReplyTo.TopMessageID
	}
	return msg.ReplyTo.MessageID
}

func readChannelMessageContentIDs(messages []domain.ChannelMessage) []int {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]int, 0, len(messages))
	seen := make(map[int]struct{}, len(messages))
	for _, msg := range messages {
		if msg.ID <= 0 {
			continue
		}
		if _, ok := seen[msg.ID]; ok {
			continue
		}
		seen[msg.ID] = struct{}{}
		ids = append(ids, msg.ID)
	}
	sort.Ints(ids)
	return ids
}

func (r *Router) onChannelsSearchPosts(ctx context.Context, req *tg.ChannelsSearchPostsRequest) (tg.MessagesMessagesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := validateChannelSearchPostsRequest(req); err != nil {
		return nil, err
	}
	offsetChannelID, err := r.searchPostsOffsetChannelID(ctx, userID, req.OffsetPeer)
	if err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return &tg.MessagesMessages{Messages: []tg.MessageClass{}, Chats: []tg.ChatClass{}, Users: []tg.UserClass{}}, nil
	}
	hashtag, query := channelSearchPostsTerms(req)
	history, err := r.deps.Channels.SearchPosts(ctx, userID, domain.ChannelSearchPostsRequest{
		Hashtag:         hashtag,
		Query:           query,
		OffsetRate:      req.OffsetRate,
		OffsetChannelID: offsetChannelID,
		OffsetID:        req.OffsetID,
		Limit:           req.Limit,
	})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	history = r.enrichChannelHistory(ctx, userID, history)
	r.maybeEnqueueExpiredChannelWebPageResolves(userID, history.Messages)
	result := tgChannelSearchPostsMessages(userID, history)
	r.applyPeerReadModelsToMessages(ctx, userID, result)
	return result, nil
}

func validateChannelSearchPostsRequest(req *tg.ChannelsSearchPostsRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if req.Limit < 0 || req.Limit > maxChannelSearchPostsLimit {
		return limitInvalidErr()
	}
	if req.OffsetRate < 0 {
		return limitInvalidErr()
	}
	if req.OffsetID < 0 || req.OffsetID > domain.MaxMessageBoxID {
		return messageIDInvalidErr()
	}
	if req.AllowPaidStars < 0 {
		return limitInvalidErr()
	}
	hashtag, hasHashtag, query, hasQuery := channelSearchPostsTermsWithFlags(req)
	if hasHashtag == hasQuery {
		return searchQueryEmptyErr()
	}
	if hasHashtag {
		if strings.TrimSpace(hashtag) == "" {
			return searchQueryEmptyErr()
		}
		if strings.Contains(hashtag, "#") || utf8.RuneCountInString(hashtag) > maxChannelSearchPostsQuery {
			return limitInvalidErr()
		}
	}
	if hasQuery {
		if strings.TrimSpace(query) == "" {
			return searchQueryEmptyErr()
		}
		if utf8.RuneCountInString(query) > maxChannelSearchPostsQuery {
			return limitInvalidErr()
		}
	}
	return nil
}

func channelSearchPostsTerms(req *tg.ChannelsSearchPostsRequest) (hashtag, query string) {
	hashtag, _, query, _ = channelSearchPostsTermsWithFlags(req)
	return strings.TrimSpace(hashtag), strings.TrimSpace(query)
}

func channelSearchPostsTermsWithFlags(req *tg.ChannelsSearchPostsRequest) (hashtag string, hasHashtag bool, query string, hasQuery bool) {
	hashtag, hasHashtag = req.GetHashtag()
	if !hasHashtag && req.Hashtag != "" {
		hashtag, hasHashtag = req.Hashtag, true
	}
	query, hasQuery = req.GetQuery()
	if !hasQuery && req.Query != "" {
		query, hasQuery = req.Query, true
	}
	return hashtag, hasHashtag, query, hasQuery
}

func (r *Router) searchPostsOffsetChannelID(ctx context.Context, userID int64, peer tg.InputPeerClass) (int64, error) {
	if peer == nil {
		return 0, nil
	}
	if _, ok := peer.(*tg.InputPeerEmpty); ok {
		return 0, nil
	}
	out, ok := r.domainPeerFromInputPeer(userID, peer)
	if !ok || out.ID == 0 {
		return 0, peerIDInvalidErr()
	}
	if out.Type != domain.PeerTypeChannel {
		return 0, peerIDInvalidErr()
	}
	ref, ok := inputPeerChannelRef(peer)
	if !ok || !ref.CheckAccessHash || r.deps.Channels == nil {
		return out.ID, nil
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, out.ID)
	if err == nil {
		if !inputChannelAccessHashMatches(ref, view.Channel) {
			return 0, channelInvalidErr(domain.ErrChannelPrivate)
		}
		return out.ID, nil
	}
	if !errors.Is(err, domain.ErrChannelPrivate) {
		return 0, channelInvalidErr(err)
	}
	channel, joinErr := r.deps.Channels.GetJoinableChannel(ctx, userID, ref.ID)
	if joinErr != nil || channel.Username == "" || !inputChannelAccessHashMatches(ref, channel) {
		return 0, channelInvalidErr(err)
	}
	return out.ID, nil
}

func tgChannelSearchPostsMessages(viewerUserID int64, history domain.ChannelHistory) tg.MessagesMessagesClass {
	messages := make([]tg.MessageClass, 0, len(history.Messages))
	for _, msg := range history.Messages {
		if item := tgChannelMessage(viewerUserID, msg); item != nil {
			messages = append(messages, item)
		}
	}
	chats := tgChannels(viewerUserID, history.Channels)
	users := tgUsersForViewer(viewerUserID, history.Users) // the viewer's own post author needs the self flag
	if history.Count > len(messages) {
		out := &tg.MessagesMessagesSlice{
			Count:    history.Count,
			Messages: messages,
			Topics:   []tg.ForumTopicClass{},
			Chats:    chats,
			Users:    users,
		}
		if len(history.Messages) > 0 {
			out.SetNextRate(history.Messages[len(history.Messages)-1].Date)
		}
		out.SetSearchFlood(tg.SearchPostsFlood{QueryIsFree: true, TotalDaily: 100, Remains: 100, StarsAmount: 0})
		return out
	}
	return &tg.MessagesMessages{Messages: messages, Topics: []tg.ForumTopicClass{}, Chats: chats, Users: users}
}

func (r *Router) onChannelsCheckSearchPostsFlood(ctx context.Context, req *tg.ChannelsCheckSearchPostsFloodRequest) (*tg.SearchPostsFlood, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if err := validateChannelCheckSearchPostsFloodRequest(req); err != nil {
		return nil, err
	}
	return &tg.SearchPostsFlood{QueryIsFree: true, TotalDaily: 100, Remains: 100, StarsAmount: 0}, nil
}

func validateChannelCheckSearchPostsFloodRequest(req *tg.ChannelsCheckSearchPostsFloodRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	query, hasQuery := req.GetQuery()
	if !hasQuery && req.Query != "" {
		query, hasQuery = req.Query, true
	}
	if !hasQuery || strings.TrimSpace(query) == "" {
		return searchQueryEmptyErr()
	}
	if utf8.RuneCountInString(query) > maxChannelSearchPostsQuery {
		return limitInvalidErr()
	}
	return nil
}

func (r *Router) onChannelsGetMessages(ctx context.Context, req *tg.ChannelsGetMessagesRequest) (tg.MessagesMessagesClass, error) {
	if len(req.ID) > domain.MaxGetMessageIDs {
		return nil, limitInvalidErr()
	}
	if r.deps.Channels == nil || len(req.ID) == 0 {
		return &tg.MessagesMessages{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	if err := r.checkFrozenChannelParticipants(ctx, userID, channelID); err != nil {
		return nil, err
	}
	trace := newGetMessagesInputTrace(req.ID)
	ids := trace.lookupIDs
	if len(ids) == 0 {
		r.logChannelGetMessagesTrace(ctx, channelID, trace, nil, &tg.MessagesMessages{})
		return &tg.MessagesMessages{}, nil
	}
	history, err := r.deps.Channels.GetMessages(ctx, userID, channelID, ids)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	history = r.enrichChannelHistory(ctx, userID, history)
	byID := make(map[int]domain.ChannelMessage, len(history.Messages))
	for _, msg := range history.Messages {
		byID[msg.ID] = msg
	}
	r.maybeEnqueueExpiredChannelWebPageResolves(userID, history.Messages)
	messages := make([]tg.MessageClass, 0, len(ids))
	for _, id := range ids {
		if msg, ok := byID[id]; ok {
			messages = append(messages, tgChannelMessage(userID, msg))
		} else {
			messages = append(messages, &tg.MessageEmpty{ID: id})
		}
	}
	result := &tg.MessagesMessages{
		Messages: messages,
		Chats:    tgChannels(userID, []domain.Channel{history.Channel}),
		Users:    r.tgUsersForViewer(userID, history.Users), // the viewer re-fetching its own messages (including pinned) needs self
	}
	r.applyPeerReadModelsToMessages(ctx, userID, result)
	r.logChannelGetMessagesTrace(ctx, channelID, trace, history.Messages, result)
	return result, nil
}

func (r *Router) onChannelsDeleteMessages(ctx context.Context, req *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	if len(req.ID) == 0 {
		return &tg.MessagesAffectedMessages{PtsCount: 0}, nil
	}
	if len(req.ID) > domain.MaxDeleteMessageIDs {
		return nil, limitInvalidErr()
	}
	for _, id := range req.ID {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return nil, messageIDInvalidErr()
		}
	}
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	res, err := r.deps.Channels.DeleteMessages(ctx, userID, domain.DeleteChannelMessagesRequest{
		UserID:    userID,
		ChannelID: channelID,
		IDs:       append([]int(nil), req.ID...),
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelDeleteErr(err)
	}
	if res.Event.Pts != 0 {
		// Delete fan-out is async-ified (design Phase 0). channelDeleteMessagesUpdates is a pure CPU
		// build (touches no PG, takes no ctx), so async has no race; serializing per channel keeps pts monotonic.
		r.enqueueChannelFanout(ctx, channelFanoutMessageBox, userID, res.Channel.ID, res.Event.Pts, res.Recipients, func(_ context.Context, viewerUserID int64) *tg.Updates {
			return r.channelDeleteMessagesUpdates(viewerUserID, res.Channel, res.Event)
		})
		// The discussion-group forward-root cascade delete of a deleted broadcast post must likewise converge discussion-group members.
		for _, cascade := range res.DiscussionDeletes {
			cascade := cascade
			r.enqueueChannelFanout(ctx, channelFanoutMessageBox, userID, cascade.Channel.ID, cascade.Event.Pts, cascade.Recipients, func(_ context.Context, viewerUserID int64) *tg.Updates {
				return r.channelDeleteMessagesUpdates(viewerUserID, cascade.Channel, cascade.Event)
			})
		}
		return &tg.MessagesAffectedMessages{Pts: res.Event.Pts, PtsCount: res.Event.PtsCount}, nil
	}
	return &tg.MessagesAffectedMessages{Pts: res.Channel.Pts, PtsCount: 0}, nil
}

// NotifyModerationChannelDeletion performs the online accelerator for a
// server-authority deletion already committed by the moderation action worker.
// Durable channel update events remain the offline recovery source.
func (r *Router) NotifyModerationChannelDeletion(ctx context.Context, res domain.DeleteChannelMessagesResult) {
	if r == nil || res.Event.Pts == 0 {
		return
	}
	r.enqueueChannelFanout(ctx, channelFanoutMessageBox, domain.OfficialSystemUserID, res.Channel.ID, res.Event.Pts, res.Recipients, func(_ context.Context, viewerUserID int64) *tg.Updates {
		return r.channelDeleteMessagesUpdates(viewerUserID, res.Channel, res.Event)
	})
	for _, cascade := range res.DiscussionDeletes {
		cascade := cascade
		r.enqueueChannelFanout(ctx, channelFanoutMessageBox, domain.OfficialSystemUserID, cascade.Channel.ID, cascade.Event.Pts, cascade.Recipients, func(_ context.Context, viewerUserID int64) *tg.Updates {
			return r.channelDeleteMessagesUpdates(viewerUserID, cascade.Channel, cascade.Event)
		})
	}
}

func (r *Router) onChannelsDeleteHistory(ctx context.Context, req *tg.ChannelsDeleteHistoryRequest) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return &tg.Updates{Date: int(r.clock.Now().Unix())}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	if req.MaxID < 0 || req.MaxID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	res, err := r.deps.Channels.DeleteHistory(ctx, userID, domain.DeleteChannelHistoryRequest{
		UserID:      userID,
		ChannelID:   channelID,
		MaxID:       req.MaxID,
		ForEveryone: req.GetForEveryone(),
		Date:        int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelDeleteErr(err)
	}
	if res.Event.Pts == 0 {
		updates := r.channelAvailableMessagesUpdates(userID, res.Channel, res.AvailableMinID)
		if res.AvailableMinChanged {
			// updateChannelAvailableMessages is an absolute owner-local boundary
			// with no account/channel pts. Other online sessions consume it
			// immediately; future cold/offline sessions discover the same
			// boundary through account/channel difference recovery.
			r.pushUserUpdates(ctx, userID, updates)
		}
		return updates, nil
	}
	pushBatch := func(batch domain.DeleteChannelHistoryResult) *tg.Updates {
		out := r.channelDeleteMessagesUpdates(userID, batch.Channel, batch.Event)
		// Each batch's fan-out is async-ified; batches enter the same channel shard in increasing pts order -> FIFO keeps it monotonic.
		r.enqueueChannelFanout(ctx, channelFanoutMessageBox, userID, batch.Channel.ID, batch.Event.Pts, batch.Recipients, func(_ context.Context, viewerUserID int64) *tg.Updates {
			return r.channelDeleteMessagesUpdates(viewerUserID, batch.Channel, batch.Event)
		})
		return out
	}
	updates := pushBatch(res)
	// channels.deleteHistory returns Updates, and the TL layer has no affectedHistory.offset
	// continuation protocol, so the client sends only one request; the remaining history beyond
	// the single-batch cap must be deleted by the server within this request, otherwise old
	// messages would remain after a "clear". Each batch advances pts in its own transaction and
	// pushes immediately; on a mid-way failure the already-deleted batches stay effective and the
	// last successful batch's result is returned.
	for res.Offset != 0 && ctx.Err() == nil {
		next, err := r.deps.Channels.DeleteHistory(ctx, userID, domain.DeleteChannelHistoryRequest{
			UserID:      userID,
			ChannelID:   channelID,
			MaxID:       req.MaxID,
			ForEveryone: req.GetForEveryone(),
			Date:        int(r.clock.Now().Unix()),
		})
		if err != nil || next.Event.Pts == 0 {
			break
		}
		res = next
		updates = pushBatch(res)
	}
	return updates, nil
}
