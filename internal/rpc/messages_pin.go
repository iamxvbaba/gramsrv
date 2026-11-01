package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// updatePrivatePinnedMessage handles the private-chat branch of
// messages.updatePinnedMessage (including Saved Messages). Official semantics: a
// pin without pm_oneside flips both sides and generates a messageActionPinMessage
// service message (reply_to pointing at the pinned message); pm_oneside / Saved
// Messages / unpin generate no service message; the peer and the account's other
// devices receive updatePinnedMessages (account pts) via reliable delivery.
func (r *Router) updatePrivatePinnedMessage(ctx context.Context, userID int64, peer domain.Peer, req *tg.MessagesUpdatePinnedMessageRequest) (tg.UpdatesClass, error) {
	if r.deps.Messages == nil {
		return nil, notImplementedErr()
	}
	recipientBlocked := false
	if !req.Unpin && !req.PmOneside && peer.ID != userID {
		var err error
		recipientBlocked, err = r.peerBlocksUser(ctx, userID, peer.ID)
		if err != nil {
			return nil, internalErr()
		}
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.PinPrivateMessage(ctx, userID, domain.PinPrivateMessageRequest{
		OwnerUserID:      userID,
		Peer:             peer,
		MessageID:        req.ID,
		Pinned:           !req.Unpin,
		PmOneside:        req.PmOneside,
		Silent:           req.Silent,
		RecipientBlocked: recipientBlocked,
		Date:             int(r.clock.Now().Unix()),
		OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:  sessionID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return nil, messageIDInvalidErr()
		}
		return nil, messageSendErr(err)
	}
	for _, changed := range res.Updated {
		r.invalidateRPCProjectionForPeer(changed.UserID, changed.Peer)
	}
	self := res.Self()
	updates := []tg.UpdateClass(nil)
	if update := tgOtherUpdateFromEvent(self.Event); update != nil {
		updates = append(updates, update)
	}
	users := []tg.UserClass{}
	if sent := res.ServiceMessage; sent.SenderMessage.ID != 0 {
		if msg := tgMessage(sent.SenderMessage); msg != nil {
			updates = append(updates, &tg.UpdateNewMessage{Message: msg, Pts: sent.SenderEvent.Pts, PtsCount: sent.SenderEvent.PtsCount})
		}
		users = r.usersForMessageUpdate(ctx, userID, sent.SenderMessage)
	}
	if len(updates) == 0 {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	return &tg.Updates{
		Updates: updates,
		Users:   users,
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}, nil
}

// unpinAllPrivateMessages handles the private-chat branch of messages.unpinAllMessages.
func (r *Router) unpinAllPrivateMessages(ctx context.Context, userID int64, peer domain.Peer) (*tg.MessagesAffectedHistory, error) {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	if r.deps.Messages == nil {
		return nil, notImplementedErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.UnpinAllPrivateMessages(ctx, userID, domain.UnpinAllPrivateMessagesRequest{
		OwnerUserID:     userID,
		Peer:            peer,
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
	self := res.Self()
	if self.Event.Pts == 0 {
		// Nothing pinned to clear: return the current account pts so the client
		// does not misjudge a pts gap.
		return r.affectedHistory(ctx, authKeyID, userID, 0)
	}
	r.invalidateRPCProjectionForPeer(userID, peer)
	if peer.Type == domain.PeerTypeUser && peer.ID != userID {
		r.invalidateRPCProjectionForPeer(peer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	}
	return &tg.MessagesAffectedHistory{
		Pts:      self.Event.Pts,
		PtsCount: self.Event.PtsCount,
		// When Offset>0 the client keeps sending unpinAll per the official
		// affectedHistory semantics to clear the remaining batches.
		Offset: res.Offset,
	}, nil
}
