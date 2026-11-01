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
	if r.deps.Messages == nil {
		return nil, internalErr()
	}
	read, err := r.deps.Messages.ReadMessageContents(ctx, userID, domain.ReadMessageContentsRequest{
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
	affected := &tg.MessagesAffectedMessages{Pts: read.Event.Pts, PtsCount: read.Event.PtsCount}
	if read.Event.Pts == 0 {
		affected, err = r.affectedMessages(ctx, id, userID)
		if err != nil {
			return nil, err
		}
	}
	return affected, nil
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
