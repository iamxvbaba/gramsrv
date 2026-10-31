package rpc

import (
	"context"
	"fmt"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

const encryptedDifferencePageSize = 1000

// Private secret chat qts message send/receive RPC handler (P1). The server is a
// blind relay: the bytes of sendEncrypted* are a client-encrypted DecryptedMessage;
// the server blindly stores them into the [receiver device]'s qts queue and forwards
// them verbatim, never decrypting. Online it pushes updateNewEncryptedMessage
// (device-targeted; offline recovery via getDifference). Design: see
// docs/secret-chat-module.md §7/§8.

// pushEncryptedNewMessage targets updateNewEncryptedMessage at the receiving device.
func (r *Router) pushEncryptedNewMessage(ctx context.Context, msg domain.SecretChatMessage) {
	if msg.ReceiverUserID == 0 || msg.ReceiverAuthKeyID == 0 {
		return
	}
	now := int(r.clock.Now().Unix())
	upd := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNewEncryptedMessage{
			Message: tgEncryptedMessage(msg),
			Qts:     msg.Qts,
		}},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{},
		Date:  now,
		Seq:   0,
	}
	if targeted, ok := r.deps.Sessions.(AuthKeyTargetedSessionBinder); ok {
		_, _ = targeted.PushToUserAuthKey(ctx, msg.ReceiverUserID, deviceAuthKeyBytes(msg.ReceiverAuthKeyID), proto.MessageFromServer, upd)
		return
	}
	// Device isolation is a security boundary; without targeting capability only the durable qts is kept, recovered by offline difference.
	r.log.Error("secret chat targeted session binder unavailable",
		zap.Int64("target_user_id", msg.ReceiverUserID),
		zap.Int64("target_auth_key_id", msg.ReceiverAuthKeyID))
}

func (r *Router) sendEncryptedCommon(ctx context.Context, peer tg.InputEncryptedChat, randomID int64, data []byte, isService bool) (tg.MessagesSentEncryptedMessageClass, error) {
	if len(data) > domain.MaxSecretMessageDataBytes {
		return nil, dataTooLongErr()
	}
	if r.deps.SecretChats == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.secretChatRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	deviceAuthKeyID, ok := businessAuthKeyIDFrom(ctx)
	if !ok {
		return nil, internalErr()
	}
	_, stored, err := r.deps.SecretChats.SendEncrypted(ctx, peer.ChatID, userID, deviceAuthKeyID, peer.AccessHash, domain.SecretMessageDelivery{
		RandomID:  randomID,
		Bytes:     data,
		IsService: isService,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, secretChatErr(err)
	}
	r.pushEncryptedNewMessage(ctx, stored)
	// No server message id; an idempotent resend returns the first-persisted date (guaranteed by store dedup).
	return &tg.MessagesSentEncryptedMessage{Date: stored.Date}, nil
}

func (r *Router) onMessagesSendEncrypted(ctx context.Context, req *tg.MessagesSendEncryptedRequest) (tg.MessagesSentEncryptedMessageClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	return r.sendEncryptedCommon(ctx, req.Peer, req.RandomID, req.Data, false)
}

func (r *Router) onMessagesSendEncryptedService(ctx context.Context, req *tg.MessagesSendEncryptedServiceRequest) (tg.MessagesSentEncryptedMessageClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	return r.sendEncryptedCommon(ctx, req.Peer, req.RandomID, req.Data, true)
}

// onMessagesReceivedQueue acknowledges that the receiving device has processed up to
// max_qts (advance confirmed + mark acked so it can be GC'd). Returns an empty
// Vector<long>: the DrKLO call site ignores the response (sendRequest(req, null)), so
// an empty set is safe.
func (r *Router) onMessagesReceivedQueue(ctx context.Context, maxQts int) ([]int64, error) {
	if r.deps.SecretChats == nil {
		return nil, notImplementedErr()
	}
	if _, err := r.secretChatRequireUser(ctx); err != nil {
		return nil, err
	}
	deviceKey, ok := businessAuthKeyIDFrom(ctx)
	if !ok {
		return nil, internalErr()
	}
	if err := r.deps.SecretChats.AckQueue(ctx, deviceKey, maxQts); err != nil {
		return nil, internalErr()
	}
	return []int64{}, nil
}

// onMessagesReportEncryptedSpam persists an immutable chat-metadata snapshot;
// the server remains unable to inspect encrypted message plaintext. Reporting
// does not discard or block the chat and emits no update.
func (r *Router) onMessagesReportEncryptedSpam(ctx context.Context, peer tg.InputEncryptedChat) (bool, error) {
	if r.deps.SecretChats == nil || r.deps.Moderation == nil {
		return false, notImplementedErr()
	}
	userID, err := r.secretChatRequireUser(ctx)
	if err != nil {
		return false, err
	}
	chat, _, _, err := r.resolveSecretChatPeer(ctx, userID, peer)
	if err != nil {
		return false, err
	}
	if _, _, err := r.deps.Moderation.ReportEncryptedSpam(ctx, userID, chat, r.clock.Now()); err != nil {
		return false, moderationReportError(err)
	}
	return true, nil
}

// deviceEncryptedQts returns the highest qts allocated to the current device (for getState injection; 0 if none).
func (r *Router) deviceEncryptedQts(ctx context.Context) int {
	if r.deps.SecretChats == nil {
		return 0
	}
	deviceKey, ok := businessAuthKeyIDFrom(ctx)
	if !ok {
		return 0
	}
	qts, err := r.deps.SecretChats.DeviceReservedQts(ctx, deviceKey)
	if err != nil {
		return 0
	}
	return qts
}

// encryptedDifference returns the contiguous prefix of the current device's
// qts > sinceQts, the advanced qts, and whether another page remains. A storage
// error or qts gap must fail-fast; never advance the client watermark past a gap.
func (r *Router) encryptedDifference(ctx context.Context, sinceQts int) ([]tg.EncryptedMessageClass, int, bool, error) {
	if r.deps.SecretChats == nil {
		return nil, sinceQts, false, nil
	}
	deviceKey, ok := businessAuthKeyIDFrom(ctx)
	if !ok {
		return nil, sinceQts, false, nil
	}
	msgs, err := r.deps.SecretChats.ListNewMessages(ctx, deviceKey, sinceQts, encryptedDifferencePageSize+1)
	if err != nil {
		return nil, sinceQts, false, err
	}
	if len(msgs) == 0 {
		return nil, sinceQts, false, nil
	}
	partial := len(msgs) > encryptedDifferencePageSize
	if partial {
		msgs = msgs[:encryptedDifferencePageSize]
	}
	out := make([]tg.EncryptedMessageClass, 0, len(msgs))
	newQts := sinceQts
	for i, m := range msgs {
		expected := newQts + 1
		if m.Qts != expected {
			return nil, sinceQts, false, fmt.Errorf("secret chat qts gap at index %d: got %d want %d", i, m.Qts, expected)
		}
		out = append(out, tgEncryptedMessage(m))
		newQts = m.Qts
	}
	return out, newQts, partial, nil
}

// injectEncryptedMessages injects the encrypted messages and the advanced qts into
// the difference response (writing Qts of State / IntermediateState by type).
func injectEncryptedMessages(diff tg.UpdatesDifferenceClass, encMsgs []tg.EncryptedMessageClass, newQts int) tg.UpdatesDifferenceClass {
	switch v := diff.(type) {
	case *tg.UpdatesDifference:
		v.NewEncryptedMessages = append(v.NewEncryptedMessages, encMsgs...)
		v.State.Qts = newQts
	case *tg.UpdatesDifferenceSlice:
		v.NewEncryptedMessages = append(v.NewEncryptedMessages, encMsgs...)
		v.IntermediateState.Qts = newQts
	}
	return diff
}

// encryptedStateUpdates returns the updates rebuilt from the current device's
// undelivered handshake/read state events (into OtherUpdates), the peer user ids
// involved (to top up Users), and the event ids to register as delivered. The
// encryption event is rebuilt from the authoritative secret_chats state (no frozen
// key-material snapshot). An account-level invitation after accept is projected as
// discarded only to unbound devices; the winning device consumes and skips it and
// must never receive a normal leak.
func (r *Router) encryptedStateUpdates(ctx context.Context, userID int64) (updates []tg.UpdateClass, peerUserIDs []int64, eventIDs []int64, partial bool, err error) {
	if r.deps.SecretChats == nil {
		return nil, nil, nil, false, nil
	}
	deviceKey, ok := businessAuthKeyIDFrom(ctx)
	if !ok {
		return nil, nil, nil, false, nil
	}
	events, err := r.deps.SecretChats.ListStateEvents(ctx, userID, deviceKey, encryptedDifferencePageSize+1)
	if err != nil {
		return nil, nil, nil, false, err
	}
	if len(events) == 0 {
		return nil, nil, nil, false, nil
	}
	partial = len(events) > encryptedDifferencePageSize
	if partial {
		events = events[:encryptedDifferencePageSize]
	}
	seenEncryption := make(map[int]struct{})
	for _, ev := range events {
		switch ev.Type {
		case domain.EncryptedStateEventEncryption:
			chat, found, gerr := r.deps.SecretChats.GetSecretChat(ctx, ev.ChatID)
			if gerr != nil || !found {
				continue
			}
			eventIDs = append(eventIDs, ev.ID)
			if _, duplicate := seenEncryption[chat.ID]; duplicate {
				continue
			}
			seenEncryption[chat.ID] = struct{}{}

			chatView := tgEncryptedChatForViewer(chat, userID)
			if ev.TargetAuthKeyID == 0 && chat.State == domain.SecretChatStateNormal {
				// The account-level event only carries the pre-accept invitation. After
				// accept the winning device already has the synchronous response; other
				// devices must converge to discarded and must not leak access_hash/g_a
				// via the current normal authoritative state.
				if chat.AuthKeyOf(userID) == deviceKey {
					continue
				}
				chatView = &tg.EncryptedChatDiscarded{ID: chat.ID, HistoryDeleted: true}
			}
			updates = append(updates, &tg.UpdateEncryption{Chat: chatView, Date: ev.Date})
			peerUserIDs = append(peerUserIDs, chat.AdminUserID, chat.ParticipantUserID)
		case domain.EncryptedStateEventRead:
			updates = append(updates, &tg.UpdateEncryptedMessagesRead{
				ChatID:  ev.ChatID,
				MaxDate: ev.MaxDate,
				Date:    ev.Date,
			})
			eventIDs = append(eventIDs, ev.ID)
		}
	}
	return updates, peerUserIDs, eventIDs, partial, nil
}

// injectEncryptedOtherUpdates appends the handshake/read updates into the
// difference's OtherUpdates and the peer user objects into Users.
func (r *Router) injectEncryptedOtherUpdates(ctx context.Context, viewerUserID int64, diff tg.UpdatesDifferenceClass, updates []tg.UpdateClass, peerUserIDs []int64) tg.UpdatesDifferenceClass {
	if len(updates) == 0 {
		return diff
	}
	users := r.tgUsersForIDs(ctx, viewerUserID, peerUserIDs)
	return appendEncryptedOtherUpdates(diff, updates, users)
}

func (r *Router) injectEncryptedOtherUpdatesStrict(ctx context.Context, viewerUserID int64, diff tg.UpdatesDifferenceClass, updates []tg.UpdateClass, peerUserIDs []int64, cache *viewerPeerCache) (tg.UpdatesDifferenceClass, error) {
	if len(updates) == 0 {
		return diff, nil
	}
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	users, err := cache.usersForIDsStrict(ctx, viewerUserID, peerUserIDs)
	if err != nil {
		return nil, err
	}
	return appendEncryptedOtherUpdates(diff, updates, r.tgUsersForViewer(viewerUserID, users)), nil
}

func appendEncryptedOtherUpdates(diff tg.UpdatesDifferenceClass, updates []tg.UpdateClass, users []tg.UserClass) tg.UpdatesDifferenceClass {
	switch v := diff.(type) {
	case *tg.UpdatesDifference:
		v.OtherUpdates = append(v.OtherUpdates, updates...)
		v.Users = appendUniqueTGUsers(v.Users, users...)
	case *tg.UpdatesDifferenceSlice:
		v.OtherUpdates = append(v.OtherUpdates, updates...)
		v.Users = appendUniqueTGUsers(v.Users, users...)
	}
	return diff
}
