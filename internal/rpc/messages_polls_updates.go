package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// This file holds the response/push/event helpers for the poll pipeline,
// isomorphic to reactions (messages_reactions_send.go):
//   - Private: each side records one message_poll durable event (an
//     updateMessagePoll without pts + aux pts bookkeeping), and offline devices
//     get the message snapshot and latest poll state via getDifference;
//   - Channel: real-time fan-out to online viewers (the author goes into the
//     explicit recipients), with no durable event -- matching the current channel
//     reaction state (see compatibility-matrix.md for the offline gap; clients
//     refresh via getPollResults).

// pollMutationErr maps poll/message domain errors to RPC errors.
func pollMutationErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrPollClosed):
		return pollClosedErr()
	case errors.Is(err, domain.ErrPollRevoteNotAllowed):
		return revoteNotAllowedErr()
	case errors.Is(err, domain.ErrPollOptionInvalid):
		return optionInvalidErr()
	case errors.Is(err, domain.ErrPollNotCreator):
		return tgerr.New(403, "MESSAGE_AUTHOR_REQUIRED")
	case errors.Is(err, domain.ErrPollNotFound), errors.Is(err, domain.ErrPollInvalid), errors.Is(err, domain.ErrMessageIDInvalid):
		return messageIDInvalidErr()
	default:
		return channelInvalidErr(err)
	}
}

// loadMessagePoll loads the poll on a message from the viewer's perspective (already enriched).
func (r *Router) loadMessagePoll(ctx context.Context, userID int64, peer domain.Peer, msgID int) (*domain.MessagePoll, domain.Peer, bool) {
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil, peer, false
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{msgID})
		if err != nil {
			return nil, peer, false
		}
		for _, msg := range history.Messages {
			if msg.ID == msgID && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindPoll && msg.Media.Poll != nil {
				return msg.Media.Poll, peer, true
			}
		}
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, peer, false
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, []int{msgID})
		if err != nil {
			return nil, peer, false
		}
		for _, msg := range list.Messages {
			if msg.ID == msgID && msg.Peer == peer && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindPoll && msg.Media.Poll != nil {
				return msg.Media.Poll, peer, true
			}
		}
	}
	return nil, peer, false
}

// pollUpdateRefs collects the users/chats an updateMessagePoll response needs
// (recent voters' avatars + channel entity). The channel is passed in by the
// caller (channel polls use res.Channel from the result; private passes the zero
// value): like the reaction fan-out, a single channel is projected to tgChannels
// in one shot, avoiding a per-viewer GetChannel DB N+1 in channel poll fan-out
// (the poll aggregate N+1 was already eliminated by ChannelPollFanoutViews; this
// is the residual per-viewer DB call in poll fan-out).
func (r *Router) pollUpdateRefs(ctx context.Context, viewerUserID int64, peer domain.Peer, poll *domain.MessagePoll, channel domain.Channel) ([]tg.UserClass, []tg.ChatClass) {
	userIDs := make([]int64, 0, domain.MaxPollRecentVoters)
	if poll != nil && poll.Results != nil {
		userIDs = append(userIDs, poll.Results.RecentVoters...)
	}
	users := r.tgUsersForIDs(ctx, viewerUserID, userIDs)
	chats := []tg.ChatClass{}
	if peer.Type == domain.PeerTypeChannel {
		if channel.ID != 0 {
			// fan-out path: the caller passes the channel, projected in one shot
			// (like reactions), avoiding a per-viewer GetChannel.
			chats = tgChannels(viewerUserID, []domain.Channel{channel})
		} else if r.deps.Channels != nil {
			// Non-fan-out single-viewer path (getPollResults): no channel is
			// passed, keeping the original GetChannel behavior unchanged.
			if view, err := r.deps.Channels.GetChannel(ctx, viewerUserID, peer.ID); err == nil && view.Channel.ID != 0 {
				chats = []tg.ChatClass{tgChannelChatForView(viewerUserID, view)}
			}
		}
	}
	return users, chats
}

// privatePollUpdates records durable events for both sides, pushes to both
// sides' online sessions, and returns the voter-view response.
func (r *Router) privatePollUpdates(ctx context.Context, requestUserID int64, res domain.PrivateMessagePollResult) tg.UpdatesClass {
	recordedEvents := r.recordPrivateMessagePollEvents(ctx, requestUserID, res)
	var requesterUpdates *tg.Updates
	for _, msg := range res.Messages {
		if msg.ID <= 0 || msg.Media == nil || msg.Media.Poll == nil {
			continue
		}
		update := tgUpdateMessagePoll(msg.Peer, msg.ID, msg.Media.Poll)
		if update == nil {
			continue
		}
		users, chats := r.pollUpdateRefs(ctx, msg.OwnerUserID, msg.Peer, msg.Media.Poll, domain.Channel{})
		updates := &tg.Updates{
			Updates: []tg.UpdateClass{update},
			Users:   users,
			Chats:   chats,
			Date:    int(r.clock.Now().Unix()),
		}
		// The message_poll event consumes account pts but updateMessagePoll has no
		// pts field, so aux bookkeeping is attached to advance the watermark.
		updates.Updates = appendAuxPtsBookkeeping(updates.Updates, recordedEvents[msg.OwnerUserID])
		if msg.OwnerUserID == requestUserID {
			requesterUpdates = updates
		}
		r.pushUserUpdates(ctx, msg.OwnerUserID, updates)
	}
	if requesterUpdates == nil {
		return tgEmptyUpdates(int(r.clock.Now().Unix()))
	}
	return requesterUpdates
}

func (r *Router) recordPrivateMessagePollEvents(ctx context.Context, requestUserID int64, res domain.PrivateMessagePollResult) map[int64]domain.UpdateEvent {
	if r.deps.Updates == nil {
		return nil
	}
	recorder, ok := r.deps.Updates.(messagePollUpdateRecorder)
	if !ok {
		return nil
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	events := make(map[int64]domain.UpdateEvent, len(res.Messages))
	for _, msg := range res.Messages {
		if msg.OwnerUserID == 0 || msg.ID == 0 {
			continue
		}
		eventAuthKeyID := [8]byte{}
		if msg.OwnerUserID == requestUserID {
			eventAuthKeyID = authKeyID
		}
		event, _, err := recorder.RecordMessagePoll(ctx, eventAuthKeyID, msg.OwnerUserID, msg)
		if err != nil {
			r.log.Warn("record message poll event failed")
			continue
		}
		events[msg.OwnerUserID] = event
	}
	return events
}

// onEditMessageClosePoll handles editMessage + InputMediaPoll: the only poll edit
// supported today is closing (closed=true, poll creator only); changing the
// question/options has no official client path and is explicitly rejected.
func (r *Router) onEditMessageClosePoll(ctx context.Context, req *tg.MessagesEditMessageRequest, media *tg.InputMediaPoll) (tg.UpdatesClass, error) {
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if !media.Poll.Closed {
		return nil, mediaInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil, messageIDInvalidErr()
		}
		res, err := r.deps.Channels.CloseMessagePoll(ctx, userID, domain.CloseChannelMessagePollRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			MessageID: req.ID,
			Date:      now,
		})
		if err != nil {
			return nil, pollMutationErr(err)
		}
		return r.channelPollUpdates(ctx, userID, peer, req.ID, res, true), nil
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, messageIDInvalidErr()
		}
		res, err := r.deps.Messages.CloseMessagePoll(ctx, userID, domain.ClosePrivateMessagePollRequest{
			UserID:    userID,
			Peer:      peer,
			MessageID: req.ID,
			Date:      now,
		})
		if err != nil {
			return nil, pollMutationErr(err)
		}
		return r.privatePollUpdates(ctx, userID, res), nil
	default:
		return nil, peerIDInvalidErr()
	}
}

// channelPollUpdates assembles the voter-view response; when push is true it
// rebuilds per viewer and fans out to online members.
//
// Phase 4 templating: before fan-out, batch-load all recipients' per-viewer poll
// projections in one pass (ChannelPollFanoutViews computes the viewer-invariant
// aggregate once + batch viewerOptions/visibility), eliminating the previous
// per-viewer GetMessages N+1. dispatch stays synchronous (a poll is viewer-only
// with no durable event, so making it async would permanently lose queued items).
// Viewers not covered by the prefetch fall back to per-viewer loadMessagePoll to
// stay correct (byte-identical to the old path). The actor echo still uses the
// res poll (old behavior).
func (r *Router) channelPollUpdates(ctx context.Context, userID int64, peer domain.Peer, msgID int, res domain.ChannelMessagePollResult, push bool) tg.UpdatesClass {
	var batched map[int64]*domain.MessagePoll
	if push && peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		recipients := r.channelFanoutRecipients(ctx, channelFanoutViewers, res.Channel.ID, res.Recipients)
		if len(recipients) > 0 {
			views, err := r.deps.Channels.ChannelPollFanoutViews(ctx, res.Channel.ID, msgID, recipients, int(r.clock.Now().Unix()))
			if err != nil {
				r.log.Warn("channel poll fanout prefetch failed; falling back to per-viewer reload",
					zap.Int64("channel_id", res.Channel.ID), zap.Int("msg_id", msgID), zap.Error(err))
			} else {
				batched = views
			}
		}
	}
	build := func(viewerUserID int64) *tg.Updates {
		poll := res.Message.Media.Poll
		if viewerUserID != userID {
			// Other viewers have different chosen/correct/solution gating, so take
			// the projection for their view.
			if p, evaluated := batched[viewerUserID]; evaluated {
				if p == nil {
					return nil // prefetch already determined this viewer cannot see it
				}
				poll = p
			} else {
				// Not covered by the prefetch (rare: concurrency/recipient-set
				// drift) -> fall back to a per-viewer reload.
				reloaded, _, found := r.loadMessagePoll(ctx, viewerUserID, peer, msgID)
				if !found {
					return nil
				}
				poll = reloaded
			}
		}
		update := tgUpdateMessagePoll(peer, msgID, poll)
		if update == nil {
			return nil
		}
		users, chats := r.pollUpdateRefs(ctx, viewerUserID, peer, poll, res.Channel)
		return &tg.Updates{
			Updates: []tg.UpdateClass{update},
			Users:   users,
			Chats:   chats,
			Date:    int(r.clock.Now().Unix()),
		}
	}
	updates := build(userID)
	if push {
		r.pushChannelViewerUpdates(ctx, userID, res.Channel.ID, res.Recipients, build)
	}
	if updates == nil {
		return tgEmptyUpdates(int(r.clock.Now().Unix()))
	}
	return updates
}
