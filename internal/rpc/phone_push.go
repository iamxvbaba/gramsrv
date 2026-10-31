package rpc

import (
	"context"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// All call signaling pushes go through pts-less direct push (pushUserMessage): signaling is valid
// for seconds, so catching up via getDifference on an offline device's reconnect to a long-expired
// incoming call is meaningless and even harmful. The only pts-bearing product is the post-call
// messageActionPhoneCall service message (P2, goes through the outbox to compensate offline
// devices).

// phoneCallUpdates wraps viewer-perspective phoneCall state into a directly pushable Updates
// container.
func (r *Router) phoneCallUpdates(ctx context.Context, call domain.PhoneCall, viewerID int64) *tg.Updates {
	return r.phoneCallUpdatesWith(ctx, tgPhoneCallForViewer(call, viewerID), call, viewerID)
}

func (r *Router) phoneCallUpdatesWith(ctx context.Context, view tg.PhoneCallClass, call domain.PhoneCall, viewerID int64) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePhoneCall{PhoneCall: view}},
		Users:   r.tgUsersForIDs(ctx, viewerID, []int64{call.AdminID, call.ParticipantID}),
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

// pushPhoneCall pushes the call's current state to all of targetUserID's online devices from their
// perspective (the initiating device carried in ctx is excluded by pushUserMessage's except
// semantics).
func (r *Router) pushPhoneCall(ctx context.Context, targetUserID int64, call domain.PhoneCall, logMessage string) int {
	return r.pushUserMessage(ctx, targetUserID, logMessage, r.phoneCallUpdates(ctx, call, targetUserID))
}

// pushPhoneCallToDevice pushes phoneCall state to exactly one physical session.
//
// This is a fail-closed path: when the target anchor is missing, the session is disconnected, or
// encoding/sending fails, it must never fall back to a user-level broadcast. The ringing update for
// an outgoing call belongs only to requestCall's origin device; widening the delivery scope would,
// when multiple DrKLO accounts share a device, pollute another account's global pending
// incoming-call object.
func (r *Router) pushPhoneCallToDevice(ctx context.Context, targetUserID int64, device domain.SessionRef, call domain.PhoneCall, logMessage string) {
	if targetUserID == 0 || device.RawAuthKeyID == ([8]byte{}) || device.SessionID == 0 || r.deps.Sessions == nil {
		if r.log != nil {
			r.log.Debug(logMessage,
				zap.Int64("target_user_id", targetUserID),
				zap.Int64("call_id", call.ID),
				zap.Int64("target_session_id", device.SessionID),
				zap.String("delivery", "skipped_invalid_device_anchor"),
			)
		}
		return
	}

	updates := r.phoneCallUpdates(ctx, call, targetUserID)
	if err := r.deps.Sessions.PushToSessionForAuthKey(ctx, device.RawAuthKeyID, device.SessionID, proto.MessageFromServer, updates); err != nil && r.log != nil {
		r.log.Debug(logMessage,
			zap.Int64("target_user_id", targetUserID),
			zap.Int64("call_id", call.ID),
			zap.Int64("target_session_id", device.SessionID),
			zap.Error(err),
		)
	}
}

// pushPhoneCallStopRinging pushes a synthetic phoneCallDiscarded to the callee's other devices to
// stop ringing (P0-1 fix). ctx must be the answering device's request context: the except semantics
// exactly exclude the winner.
func (r *Router) pushPhoneCallStopRinging(ctx context.Context, call domain.PhoneCall) int {
	upd := r.phoneCallUpdatesWith(ctx, tgPhoneCallStopRinging(call), call, call.ParticipantID)
	return r.pushUserMessage(ctx, call.ParticipantID, "phone call stop ringing", upd)
}

// pushPhoneCallDiscardedBoth pushes the terminal state to all devices on both sides (the initiating
// device is excluded by ctx except; its result comes from the RPC response).
func (r *Router) pushPhoneCallDiscardedBoth(ctx context.Context, call domain.PhoneCall) {
	r.pushPhoneCall(ctx, call.AdminID, call, "phone call discarded")
	r.pushPhoneCall(ctx, call.ParticipantID, call, "phone call discarded")
}

// pushPhoneSignalingData passes signaling bytes through to the peer. It prefers directed push to the
// device anchor (the device that handled requestCall/acceptCall); if the anchor is stale (the device
// reconnected with a new session) it falls back to user-level fan-out — non-participant devices
// silently drop it on a phone_call_id mismatch (TDesktop's handleSignalingData behavior), so the
// fan-out is harmless.
func (r *Router) pushPhoneSignalingData(ctx context.Context, targetUserID int64, device domain.SessionRef, callID int64, data []byte) {
	upd := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePhoneCallSignalingData{
			PhoneCallID: callID,
			Data:        data,
		}},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{},
		Date:  int(r.clock.Now().Unix()),
		Seq:   0,
	}
	if !device.Zero() && r.deps.Sessions != nil {
		if err := r.deps.Sessions.PushToSessionForAuthKey(ctx, device.RawAuthKeyID, device.SessionID, proto.MessageFromServer, upd); err == nil {
			return
		}
	}
	r.pushUserMessage(ctx, targetUserID, "phone call signaling", upd)
}
