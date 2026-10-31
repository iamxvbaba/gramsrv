package rpc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// RTMP livestream (Live Stream) RPCs: after createGroupCall(rtmp_stream) creates the room, the
// streamer (OBS) pushes using the url/key from getGroupCallStreamRtmpUrl, and viewers pull
// broadcast chunks via upload.getFile(inputGroupCallStream) after joining.
//
// Key difference from an ordinary voice chat (RTC/SFU): an RTMP join builds no SFU connection and
// does no ssrc-uniqueness media-plane validation; updateGroupCallConnection.params returns
// {"stream":true,"rtmp":true} to make TDesktop switch to broadcast mode (ParseJoinResponse →
// JoinBroadcastStream).

// rtmpConnectionParams is the downlink JSON for an RTMP viewer's join response (the tgcalls
// broadcast branch). Managers get rtmp_stream_url/key appended for the livestream settings page
// (ordinary viewers get none, to avoid leaking stream credentials).
type rtmpConnectionParams struct {
	Stream        bool   `json:"stream"`
	Rtmp          bool   `json:"rtmp"`
	RtmpStreamURL string `json:"rtmp_stream_url,omitempty"`
	RtmpStreamKey string `json:"rtmp_stream_key,omitempty"`
}

func buildRtmpConnectionParams(url, key string) (string, error) {
	p := rtmpConnectionParams{Stream: true, Rtmp: true, RtmpStreamURL: url, RtmpStreamKey: key}
	out, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// joinRtmpGroupCall handles joinGroupCall for an RTMP livestream room.
func (r *Router) joinRtmpGroupCall(ctx context.Context, scope *groupCallScope, req *tg.PhoneJoinGroupCallRequest) (tg.UpdatesClass, error) {
	// Room cap: RTMP viewers also count toward participants_count; a rejoin (already in the call,
	// swapping ssrc) is not limited.
	if max := r.cfg.GroupCallMaxParticipants; max > 0 && scope.call.ParticipantsCount >= max {
		if p, found, _ := r.deps.GroupCalls.Participant(ctx, scope.call.ID, scope.userID); !found || p.Left {
			return nil, groupCallForbiddenErr()
		}
	}
	joinAsChannelID, err := r.groupCallJoinAsChannelID(scope, req.JoinAs)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	// An RTMP viewer's join JSON still carries a tgcalls ssrc (the client builds a local controller
	// for the fetch too); a parse failure/absence is not fatal — the livestream needs no SFU, so a
	// random ssrc is used for bookkeeping to keep the participant row valid.
	ssrc := int64(0)
	if _, s, err := parseGroupCallJoinPayload(req.Params.Data); err == nil {
		ssrc = s
	}
	if ssrc == 0 {
		ssrc = randomSSRC()
	}
	// RTMP viewers (including the creator, who streams independently via OBS and is likewise a pure
	// viewer within the group call) are all force-muted: IsAdmin=false makes the store set
	// muted+muted_by_admin for a join_muted room, and the self row outputs muted=true /
	// can_self_unmute=false, so TDesktop stays stably in stream mode.
	mut, err := r.deps.GroupCalls.Join(ctx, domain.JoinGroupCallRequest{
		CallID:          scope.call.ID,
		UserID:          scope.userID,
		JoinAsChannelID: joinAsChannelID,
		SSRC:            ssrc,
		Muted:           true,
		IsAdmin:         false,
		Now:             now,
	})
	if err != nil {
		return nil, groupCallErr(err)
	}
	// Managers get the stream url/key (shown on the livestream settings page); ordinary viewers only
	// get stream:true.
	var url, key string
	if scope.canManage() && r.deps.GroupCalls != nil {
		if k, kerr := r.deps.GroupCalls.RtmpStreamKey(ctx, scope.channel.ID, false, now); kerr == nil {
			key = k
			url = r.rtmpIngestURL()
		}
	}
	params, err := buildRtmpConnectionParams(url, key)
	if err != nil {
		return nil, internalErr()
	}
	channel := r.groupCallMutationFanout(ctx, scope.channel, mut)
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{scope.userID})
	// updateGroupCall must precede updateGroupCallConnection: TDesktop applies updates in order, and
	// if it has not yet read stream_dc_id from the groupCall when handling the connection it logs
	// "Api Error: Empty stream_dc_id" and falls back to the main DC.
	callUpdate := &tg.UpdateGroupCall{Call: tgGroupCall(mut.Call, scope.userID, scope.canManage(), r.cfg.PublicBaseURL)}
	if channel.ID != 0 {
		callUpdate.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
	}
	out.Updates = append(out.Updates, callUpdate)
	out.Updates = append(out.Updates, &tg.UpdateGroupCallConnection{Params: tg.DataJSON{Data: params}})
	return out, nil
}

// onPhoneGetGroupCallStreamRtmpURL returns the channel's RTMP stream url/key (pre-create preview,
// livestream settings page display, revoke rotation). Only a channel admin can call it; revoke=true
// generates a new key, immediately invalidating the old key and disconnecting an in-progress stream.
func (r *Router) onPhoneGetGroupCallStreamRtmpURL(ctx context.Context, req *tg.PhoneGetGroupCallStreamRtmpURLRequest) (*tg.PhoneGroupCallStreamRtmpURL, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.GroupCalls == nil || r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, peer.ID)
	if err != nil {
		return nil, peerIDInvalidErr()
	}
	if view.Self.Status != domain.ChannelMemberActive || !channelMemberIsAdmin(view.Self) {
		return nil, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	now := int(r.clock.Now().Unix())
	key, err := r.deps.GroupCalls.RtmpStreamKey(ctx, peer.ID, req.Revoke, now)
	if err != nil {
		return nil, groupCallErr(err)
	}
	if req.Revoke && r.deps.LiveStreams != nil {
		// Connections still streaming after the old key is invalidated must be disconnected
		// (otherwise a stream using the old key keeps being accepted).
		r.deps.LiveStreams.DropChannel(peer.ID)
	}
	return &tg.PhoneGroupCallStreamRtmpURL{URL: r.rtmpIngestURL(), Key: key}, nil
}

// onPhoneGetGroupCallStreamChannels returns the current timeline of the RTMP livestream (unified:
// channel=1, scale=0), letting tgcalls decide from which time_ms to fetch chunks. With no active
// stream it returns an empty list, and TDesktop shows a "waiting for stream" placeholder and
// retries in a loop.
func (r *Router) onPhoneGetGroupCallStreamChannels(ctx context.Context, call tg.InputGroupCallClass) (*tg.PhoneGroupCallStreamChannels, error) {
	scope, err := r.groupCallScopeFrom(ctx, call)
	if err != nil {
		return nil, err
	}
	out := &tg.PhoneGroupCallStreamChannels{Channels: []tg.GroupCallStreamChannel{}}
	if !scope.call.RtmpStream || r.deps.LiveStreams == nil || scope.channel.ID == 0 {
		return out, nil
	}
	for _, ch := range r.deps.LiveStreams.StreamChannels(scope.channel.ID) {
		out.Channels = append(out.Channels, tg.GroupCallStreamChannel{
			Channel:         ch.Channel,
			Scale:           ch.Scale,
			LastTimestampMs: ch.LastTimestampMs,
		})
	}
	return out, nil
}

// rtmpIngestURL returns the RTMP server address shown to the streaming side.
func (r *Router) rtmpIngestURL() string {
	if r.cfg.RtmpIngestURL != "" {
		return r.cfg.RtmpIngestURL
	}
	host := r.cfg.IP
	if host == "" {
		host = "127.0.0.1"
	}
	return "rtmp://" + host + ":2400/live"
}

func randomSSRC() int64 {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 1
	}
	v := int64(binary.BigEndian.Uint32(buf[:]))
	if v == 0 {
		v = 1
	}
	return v
}
