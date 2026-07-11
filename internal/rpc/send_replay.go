package rpc

import (
	"context"
	"crypto/sha256"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// These optional capabilities keep the broad RPC service interfaces stable for compatibility
// fakes while production app services expose the read-only receipt lookup.
type privateSendReplayService interface {
	LookupPrivateSendReplay(ctx context.Context, userID int64, req domain.PrivateSendReplayRequest) (domain.SendPrivateTextResult, bool, error)
}

type channelSendReplayService interface {
	LookupChannelSendReplay(ctx context.Context, userID int64, req domain.ChannelSendReplayRequest) (domain.SendChannelMessageResult, bool, error)
}

type outgoingReplayLookup struct {
	private domain.SendPrivateTextResult
	channel domain.SendChannelMessageResult
	found   bool
	checked bool
}

// lookupOutgoingReplay is deliberately limited to authenticated sender + destination scope +
// immutable fingerprint. It does not resolve media/replies/send-as/source messages, consume rate
// budget, run a send permission gate or emit any realtime/durable side effect.
func (r *Router) lookupOutgoingReplay(ctx context.Context, userID int64, peer domain.Peer, randomID int64, fingerprint []byte) (outgoingReplayLookup, error) {
	if randomID == 0 || len(fingerprint) != sha256.Size {
		return outgoingReplayLookup{}, nil
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		service, ok := r.deps.Messages.(privateSendReplayService)
		if !ok {
			return outgoingReplayLookup{}, nil
		}
		res, found, err := service.LookupPrivateSendReplay(ctx, userID, domain.PrivateSendReplayRequest{
			SenderUserID:           userID,
			RecipientUserID:        peer.ID,
			RandomID:               randomID,
			IdempotencyFingerprint: fingerprint,
		})
		if err != nil {
			return outgoingReplayLookup{checked: true}, messageSendErr(err)
		}
		return outgoingReplayLookup{private: res, found: found, checked: true}, nil
	case domain.PeerTypeChannel:
		return r.lookupChannelSendReplay(ctx, userID, peer.ID, domain.Peer{}, randomID, fingerprint)
	default:
		return outgoingReplayLookup{}, peerIDInvalidErr()
	}
}

func (r *Router) lookupChannelSendReplay(ctx context.Context, userID, channelID int64, savedPeer domain.Peer, randomID int64, fingerprint []byte) (outgoingReplayLookup, error) {
	if randomID == 0 || len(fingerprint) != sha256.Size {
		return outgoingReplayLookup{}, nil
	}
	service, ok := r.deps.Channels.(channelSendReplayService)
	if !ok {
		return outgoingReplayLookup{}, nil
	}
	res, found, err := service.LookupChannelSendReplay(ctx, userID, domain.ChannelSendReplayRequest{
		ChannelID:              channelID,
		SenderUserID:           userID,
		SavedPeer:              savedPeer,
		RandomID:               randomID,
		IdempotencyFingerprint: fingerprint,
	})
	if err != nil {
		return outgoingReplayLookup{checked: true}, channelInvalidErr(err)
	}
	return outgoingReplayLookup{channel: res, found: found, checked: true}, nil
}

func (r *Router) outgoingReplayUpdates(ctx context.Context, userID int64, peer domain.Peer, randomID int64, replay outgoingReplayLookup) tg.UpdatesClass {
	if peer.Type == domain.PeerTypeChannel {
		return r.channelMessageUpdatesWithPeerCache(ctx, userID, replay.channel, randomID, newViewerPeerCache(r))
	}
	return tgPrivateSendResultUpdates(replay.private, randomID, true, nil, nil)
}

func combineSendUpdates(results []tg.UpdatesClass) *tg.Updates {
	combined := make([]tg.UpdateClass, 0, len(results)*2)
	usersByID := map[int64]tg.UserClass{}
	chatsByID := map[int64]tg.ChatClass{}
	date := 0
	for _, result := range results {
		upd, ok := result.(*tg.Updates)
		if !ok || upd == nil {
			continue
		}
		combined = append(combined, upd.Updates...)
		for _, user := range upd.Users {
			if id := userClassID(user); id != 0 {
				usersByID[id] = user
			}
		}
		for _, chat := range upd.Chats {
			if id := chatClassID(chat); id != 0 {
				chatsByID[id] = chat
			}
		}
		if upd.Date > date {
			date = upd.Date
		}
	}
	return &tg.Updates{
		Updates: combined,
		Users:   mapValuesUsers(usersByID),
		Chats:   mapValuesChats(chatsByID),
		Date:    date,
	}
}
