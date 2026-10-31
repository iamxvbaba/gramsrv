package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) onMessagesReadMessageContents(ctx context.Context, ids []int) (*tg.MessagesAffectedMessages, error) {
	id, _ := AuthKeyIDFrom(ctx)
	sessionID, _ := SessionIDFrom(ctx)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if len(ids) > maxGetMessagesIDs {
		return nil, limitInvalidErr()
	}
	for _, msgID := range ids {
		if msgID <= 0 || msgID > domain.MaxMessageBoxID {
			return nil, messageIDInvalidErr()
		}
	}
	read := domain.ReadMessageContentsResult{OwnerUserID: userID}
	if r.deps.Messages != nil {
		read, err = r.deps.Messages.ReadMessageContents(ctx, userID, domain.ReadMessageContentsRequest{
			OwnerUserID:     userID,
			IDs:             ids,
			Date:            int(r.clock.Now().Unix()),
			OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
			OriginSessionID: sessionID,
		})
		if err != nil {
			if errors.Is(err, domain.ErrMessageIDInvalid) {
				return nil, messageIDInvalidErr()
			}
			return nil, internalErr()
		}
	}
	affected := &tg.MessagesAffectedMessages{Pts: read.Event.Pts, PtsCount: read.Event.PtsCount}
	if read.Event.Pts == 0 {
		affected, err = r.affectedMessages(ctx, id, userID)
		if err != nil {
			return nil, err
		}
	}
	now := int(r.clock.Now().Unix())
	if contentIDs := readMessageContentIDs(read.MessageIDs); len(contentIDs) > 0 {
		contents := &tg.UpdateReadMessagesContents{
			Messages: contentIDs,
			Pts:      affected.Pts,
			PtsCount: affected.PtsCount,
		}
		contents.SetDate(now)
		r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
			Updates: []tg.UpdateClass{contents},
			Date:    now,
			Seq:     0,
		})
	}
	// The peer sender of a voice/round message receives a content-read receipt
	// carrying its own-view box id, clearing the sender's "unheard" blue dot; under
	// a reliable outbox deployment it is delivered by the worker.
	for _, event := range read.SenderEvents {
		if update := tgOtherUpdateFromEvent(event); update != nil {
			r.pushUserUpdatesIfNoReliableDispatch(ctx, event.UserID, &tg.Updates{
				Updates: []tg.UpdateClass{update},
				Date:    now,
				Seq:     0,
			})
		}
	}
	return affected, nil
}

func readMessageContentIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int, 0, len(ids))
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
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

func (r *Router) onMessagesGetUnreadMentions(ctx context.Context, req *tg.MessagesGetUnreadMentionsRequest) (tg.MessagesMessagesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.Limit < 0 || req.Limit > domain.MaxChannelUnreadMentionsLimit {
		return nil, limitInvalidErr()
	}
	if req.TopMsgID < 0 || req.TopMsgID > domain.MaxMessageBoxID ||
		req.OffsetID < 0 || req.OffsetID > domain.MaxMessageBoxID ||
		req.MaxID < 0 || req.MaxID > domain.MaxMessageBoxID ||
		req.MinID < 0 || req.MinID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		history, err := r.deps.Channels.GetUnreadMentions(ctx, userID, domain.ChannelUnreadMentionsFilter{
			ChannelID: peer.ID,
			TopMsgID:  req.TopMsgID,
			OffsetID:  req.OffsetID,
			AddOffset: req.AddOffset,
			Limit:     req.Limit,
			MaxID:     req.MaxID,
			MinID:     req.MinID,
		})
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		return r.tgChannelHistoryMessages(ctx, userID, r.enrichChannelHistory(ctx, userID, history)), nil
	}
	out := &tg.MessagesMessages{
		Messages: []tg.MessageClass{},
		Chats:    r.chatsForInputPeer(ctx, userID, req.Peer),
		Users:    []tg.UserClass{},
	}
	r.applyPeerReadModelsToMessages(ctx, userID, out)
	return out, nil
}

func (r *Router) onMessagesReadMentions(ctx context.Context, req *tg.MessagesReadMentionsRequest) (*tg.MessagesAffectedHistory, error) {
	id, _ := AuthKeyIDFrom(ctx)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.TopMsgID < 0 || req.TopMsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		res, err := r.deps.Channels.ReadMentions(ctx, userID, domain.ReadChannelMentionsRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			TopMsgID:  req.TopMsgID,
			Limit:     domain.MaxChannelReadMentionsBatch,
		})
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		return &tg.MessagesAffectedHistory{Pts: res.ChannelPts, PtsCount: 0, Offset: res.Offset}, nil
	}
	return r.affectedHistory(ctx, id, userID, 0)
}

func (r *Router) affectedMessages(ctx context.Context, authKeyID [8]byte, userID int64) (*tg.MessagesAffectedMessages, error) {
	st := domain.UpdateState{Date: int(r.clock.Now().Unix())}
	if r.deps.Updates != nil {
		var err error
		st, err = r.deps.Updates.GetState(ctx, authKeyID, userID)
		if err != nil {
			return nil, internalErr()
		}
	}
	return &tg.MessagesAffectedMessages{Pts: st.Pts, PtsCount: 0}, nil
}

func (r *Router) affectedHistory(ctx context.Context, authKeyID [8]byte, userID int64, offset int) (*tg.MessagesAffectedHistory, error) {
	st := domain.UpdateState{Date: int(r.clock.Now().Unix())}
	if r.deps.Updates != nil {
		var err error
		st, err = r.deps.Updates.GetState(ctx, authKeyID, userID)
		if err != nil {
			return nil, internalErr()
		}
	}
	return &tg.MessagesAffectedHistory{Pts: st.Pts, PtsCount: 0, Offset: offset}, nil
}

func (r *Router) pushReadHistoryEvent(ctx context.Context, userID int64, event domain.UpdateEvent) {
	if r.hasReliableUpdateDispatch() {
		return
	}
	if r.deps.Sessions == nil || userID == 0 {
		return
	}
	var update tg.UpdateClass
	switch event.Type {
	case domain.UpdateEventReadHistoryInbox:
		update = tgReadHistoryInboxUpdate(event)
	case domain.UpdateEventReadHistoryOutbox:
		update = tgReadHistoryOutboxUpdate(event)
	case domain.UpdateEventReadChannelDiscussionInbox:
		if event.Peer.ID != 0 {
			update = &tg.UpdateReadChannelDiscussionInbox{ChannelID: event.Peer.ID, TopMsgID: event.TopMsgID, ReadMaxID: event.MaxID}
		}
	}
	if update == nil {
		return
	}
	updates := &tg.Updates{
		Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, event),
		Date:    event.Date,
		Seq:     0,
	}
	r.pushUserMessage(ctx, userID, "push read history", updates)
}

func (r *Router) pushCurrentReadHistoryEvent(ctx context.Context, event domain.UpdateEvent) {
	var update tg.UpdateClass
	switch event.Type {
	case domain.UpdateEventReadHistoryInbox:
		update = tgReadHistoryInboxUpdate(event)
	case domain.UpdateEventReadHistoryOutbox:
		update = tgReadHistoryOutboxUpdate(event)
	case domain.UpdateEventReadChannelDiscussionInbox:
		if event.Peer.ID != 0 {
			update = &tg.UpdateReadChannelDiscussionInbox{ChannelID: event.Peer.ID, TopMsgID: event.TopMsgID, ReadMaxID: event.MaxID}
		}
	}
	if update == nil {
		return
	}
	r.pushCurrentSessionMessage(ctx, "push current read history", &tg.Updates{
		Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, event),
		Date:    event.Date,
		Seq:     0,
	})
}

// bookkeepAuxPtsForCurrentSession syncs the account pts consumed by aux events
// to the initiating session: reliable outbox delivery excludes the current
// session, and these RPCs mostly return Bool/pts-less containers, so omitting
// this entry would leave the current device's watermark behind and misjudge later
// gaps.
func (r *Router) bookkeepAuxPtsForCurrentSession(ctx context.Context, events ...domain.UpdateEvent) {
	updates := make([]tg.UpdateClass, 0, len(events))
	date := 0
	for _, event := range events {
		before := len(updates)
		updates = appendAuxPtsBookkeeping(updates, event)
		if len(updates) > before && date == 0 {
			date = event.Date
		}
	}
	if len(updates) == 0 {
		return
	}
	if date == 0 {
		date = int(r.clock.Now().Unix())
	}
	r.pushCurrentSessionMessage(ctx, "aux pts bookkeeping", &tg.Updates{
		Updates: updates,
		Date:    date,
		Seq:     0,
	})
}

func tgReadHistoryInbox(event domain.UpdateEvent) *tg.UpdateReadHistoryInbox {
	peer := tgPeer(event.Peer)
	if peer == nil {
		return nil
	}
	return &tg.UpdateReadHistoryInbox{
		Peer:             peer,
		MaxID:            event.MaxID,
		StillUnreadCount: event.StillUnreadCount,
		Pts:              event.Pts,
		PtsCount:         event.PtsCount,
	}
}

func tgReadHistoryInboxUpdate(event domain.UpdateEvent) tg.UpdateClass {
	if event.Peer.Type == domain.PeerTypeChannel && event.Peer.ID != 0 {
		// updateReadChannelInbox.pts may only be channel pts; an account pts here
		// is wrong semantics (TDesktop compares it with its local channel pts), so
		// when missing it is better to fill 0 and let the client fall back to its
		// local estimate.
		update := &tg.UpdateReadChannelInbox{
			ChannelID:        event.Peer.ID,
			MaxID:            event.MaxID,
			StillUnreadCount: event.StillUnreadCount,
			Pts:              event.ChannelPts,
		}
		if event.FolderID > 0 {
			update.SetFolderID(event.FolderID)
		}
		return update
	}
	update := tgReadHistoryInbox(event)
	if update == nil {
		return nil
	}
	return update
}

func tgReadHistoryOutbox(event domain.UpdateEvent) *tg.UpdateReadHistoryOutbox {
	peer := tgPeer(event.Peer)
	if peer == nil {
		return nil
	}
	return &tg.UpdateReadHistoryOutbox{
		Peer:     peer,
		MaxID:    event.MaxID,
		Pts:      event.Pts,
		PtsCount: event.PtsCount,
	}
}

func tgReadHistoryOutboxUpdate(event domain.UpdateEvent) tg.UpdateClass {
	if event.Peer.Type == domain.PeerTypeChannel && event.Peer.ID != 0 {
		return &tg.UpdateReadChannelOutbox{
			ChannelID: event.Peer.ID,
			MaxID:     event.MaxID,
		}
	}
	update := tgReadHistoryOutbox(event)
	if update == nil {
		return nil
	}
	return update
}
