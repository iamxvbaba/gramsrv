package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	appphone "telesrv/internal/app/phone"
	"telesrv/internal/domain"
)

// Private 1:1 call RPC handlers. The state machine and key material live in app/phone; this file
// only does authentication, input validation, privacy/block gates, push orchestration, and TL
// conversion. See the design doc for the push-strategy summary and hard contracts; comments marked
// with ⚠ are fixes agreed during adversarial review — do not revert them.

// phoneCallErr maps app/phone business errors to RPC_ERROR.
func phoneCallErr(err error) error {
	switch {
	case errors.Is(err, appphone.ErrPeerInvalid):
		return callPeerInvalidErr()
	case errors.Is(err, appphone.ErrAlreadyAccepted):
		return callAlreadyAcceptedErr()
	case errors.Is(err, appphone.ErrAlreadyDeclined):
		return callAlreadyDeclinedErr()
	case errors.Is(err, appphone.ErrOccupyFailed):
		return callOccupyFailedErr()
	case errors.Is(err, appphone.ErrProtocolLayerInvalid):
		return callProtocolLayerInvalidErr()
	case errors.Is(err, appphone.ErrProtocolCompatLayerInvalid):
		return callProtocolCompatLayerInvalidErr()
	case errors.Is(err, appphone.ErrProtocolFlagsInvalid):
		return callProtocolFlagsInvalidErr()
	default:
		return internalErr()
	}
}

// phoneRequireUser is the unified login gate for phone.*.
func (r *Router) phoneRequireUser(ctx context.Context) (int64, error) {
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return 0, internalErr()
	}
	if !ok {
		return 0, authKeyUnregisteredErr()
	}
	return userID, nil
}

func sessionRefFrom(ctx context.Context) domain.SessionRef {
	ref := domain.SessionRef{}
	if raw, ok := RawAuthKeyIDFrom(ctx); ok {
		ref.RawAuthKeyID = raw
	}
	if sid, ok := SessionIDFrom(ctx); ok {
		ref.SessionID = sid
	}
	return ref
}

func (r *Router) onPhoneRequestCall(ctx context.Context, req *tg.PhoneRequestCallRequest) (*tg.PhonePhoneCall, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Phone == nil || r.deps.Users == nil {
		return nil, notImplementedErr()
	}
	callerID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	callee, found, err := r.userFromInput(ctx, callerID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	// Self-call / bot / non-existent all map to USER_ID_INVALID.
	if !found || callee.ID == 0 || callee.ID == callerID || callee.Bot {
		return nil, userIDInvalidErr()
	}
	if blocked, err := r.peerBlocksUser(ctx, callerID, callee.ID); err != nil {
		return nil, err
	} else if blocked {
		return nil, userIsBlockedErr()
	}
	if r.deps.Privacy != nil {
		allowed, err := r.deps.Privacy.CanSee(ctx, callee.ID, callerID, domain.PrivacyKeyPhoneCall)
		if err != nil {
			return nil, internalErr()
		}
		if !allowed {
			return nil, userPrivacyRestrictedErr()
		}
	}
	call, err := r.deps.Phone.RequestCall(ctx, callerID, domain.PhoneCallRequest{
		CalleeID:     callee.ID,
		RandomID:     int64(req.RandomID),
		GAHash:       req.GAHash,
		Video:        req.Video,
		Protocol:     phoneCallProtocolFromTL(req.Protocol),
		CallerDevice: sessionRefFrom(ctx),
		PrivacyP2P:   r.phoneCallPrivacyP2P(ctx, callerID, callee.ID),
		Connections:  r.phoneCallConnections(callerID),
	})
	if err != nil {
		return nil, phoneCallErr(err)
	}
	// Push to all the callee's online devices. sent==0 (callee fully offline) is not an error: the
	// caller's client times out on its own at callReceiveTimeoutMs(20s) and discards (missed), and
	// the server-side fallback timeout wraps it up.
	r.pushPhoneCall(ctx, call.ParticipantID, call, "phone call requested")
	return &tg.PhonePhoneCall{
		PhoneCall: tgPhoneCallForViewer(call, callerID),
		Users:     r.tgUsersForIDs(ctx, callerID, []int64{call.AdminID, call.ParticipantID}),
	}, nil
}

func (r *Router) onPhoneReceivedCall(ctx context.Context, peer tg.InputPhoneCall) (bool, error) {
	if r.deps.Phone == nil {
		return false, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return false, err
	}
	call, transitioned, err := r.deps.Phone.ReceivedCall(ctx, userID, peer.ID, peer.AccessHash)
	if err != nil {
		// A late receivedCall after the terminal state is harmless: return true leniently (a
		// multi-device race is common).
		if errors.Is(err, appphone.ErrPeerInvalid) {
			if _, found := r.deps.Phone.Lookup(ctx, peer.ID, peer.AccessHash); found {
				return true, nil
			}
		}
		return false, phoneCallErr(err)
	}
	if transitioned {
		// ⚠ P1-2: the receiveDate push must be in place at P1. Only after receiving a
		// phoneCallWaiting with receive_date does the caller swap its 20s receive timer for the 90s
		// ring timer. This caller-perspective update belongs only to requestCall's origin device; an
		// account-level broadcast would, when multiple DrKLO accounts share a device, overwrite the
		// callee's pending phoneCallRequested with the same call_id, losing g_a_hash and triggering a
		// Ga hash mismatch on answer.
		r.pushPhoneCallToDevice(ctx, call.AdminID, call.CallerDevice, call, "phone call ringing")
	}
	return true, nil
}

func (r *Router) onPhoneAcceptCall(ctx context.Context, req *tg.PhoneAcceptCallRequest) (*tg.PhonePhoneCall, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Phone == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	call, err := r.deps.Phone.AcceptCall(ctx, userID, req.Peer.ID, req.Peer.AccessHash,
		req.GB, phoneCallProtocolFromTL(req.Protocol), sessionRefFrom(ctx))
	if err != nil {
		return nil, phoneCallErr(err)
	}
	// Push to all the caller's devices: phoneCallAccepted{g_b, negotiated protocol}, from which the
	// caller computes the AuthKey and confirms.
	r.pushPhoneCall(ctx, call.AdminID, call, "phone call accepted")
	// ⚠ P0-1: push a synthetic phoneCallDiscarded to the callee's other devices to stop ringing
	// (ctx except excludes the answering device). Never push phoneCallAccepted — an incoming side
	// that receives accepted either finishes(Failed) and sends discardCall (TDesktop) or follows the
	// caller flow and sends confirmCall (DrKLO), killing the just-established call.
	r.pushPhoneCallStopRinging(ctx, call)
	return &tg.PhonePhoneCall{
		PhoneCall: tgPhoneCallForViewer(call, userID), // callee perspective: phoneCallWaiting, awaiting confirm
		Users:     r.tgUsersForIDs(ctx, userID, []int64{call.AdminID, call.ParticipantID}),
	}, nil
}

func (r *Router) onPhoneConfirmCall(ctx context.Context, req *tg.PhoneConfirmCallRequest) (*tg.PhonePhoneCall, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Phone == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	call, forcedDiscard, err := r.deps.Phone.ConfirmCall(ctx, userID, req.Peer.ID, req.Peer.AccessHash,
		req.GA, req.KeyFingerprint, phoneCallProtocolFromTL(req.Protocol))
	if err != nil {
		if forcedDiscard {
			// The revealed g_a does not match the commitment: the call has been force-terminated;
			// push both sides to prevent the state machine from hanging.
			r.pushPhoneCallDiscardedBoth(ctx, call)
			return nil, callPeerInvalidErr()
		}
		return nil, phoneCallErr(err)
	}
	// Push to all the callee's devices: phoneCall{g_a_or_b=GA}. In practice only the answering
	// device holds this call; the rest do not recognize it by call_id and silently ignore it.
	r.pushPhoneCall(ctx, call.ParticipantID, call, "phone call confirmed")
	return &tg.PhonePhoneCall{
		PhoneCall: tgPhoneCallForViewer(call, userID), // caller perspective: g_a_or_b = g_b
		Users:     r.tgUsersForIDs(ctx, userID, []int64{call.AdminID, call.ParticipantID}),
	}, nil
}

func (r *Router) onPhoneDiscardCall(ctx context.Context, req *tg.PhoneDiscardCallRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.Phone == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	reason := phoneCallDiscardReasonFromTL(req.Reason)
	reasonSlug := phoneCallDiscardReasonSlugFromTL(req.Reason)
	if reason == domain.PhoneCallDiscardReasonMigrateConference {
		if reasonSlug == "" || r.deps.GroupCalls == nil {
			return nil, groupCallInvalidErr()
		}
		if call, found, err := r.deps.GroupCalls.GetBySlug(ctx, reasonSlug); err != nil {
			return nil, internalErr()
		} else if !found || !call.Conference() || !call.Active() {
			return nil, groupCallInvalidErr()
		}
	}
	call, already, err := r.deps.Phone.DiscardCallWithSlug(ctx, userID, req.Peer.ID, req.Peer.AccessHash, reason, reasonSlug, req.Duration)
	if err != nil {
		return nil, phoneCallErr(err)
	}
	if !already {
		// All of the peer's devices + the initiator's other devices (ctx except excludes the
		// initiator device, whose result is in the RPC response).
		r.pushPhoneCallDiscardedBoth(ctx, call)
		if reason != domain.PhoneCallDiscardReasonMigrateConference {
			// Record messageActionPhoneCall history (goes through the outbox with pts, so all
			// devices on both sides receive it reliably).
			r.sendPhoneCallServiceMessage(ctx, call)
		}
	}
	// Race when both sides hang up simultaneously: the first to arrive sets the reason, the later
	// one gets the terminal snapshot (idempotent success).
	return r.phoneCallUpdates(ctx, call, userID), nil
}

func (r *Router) onPhoneSendSignalingData(ctx context.Context, req *tg.PhoneSendSignalingDataRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	if r.deps.Phone == nil {
		return false, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return false, err
	}
	if max := r.cfg.CallSignalingMaxBytes; max > 0 && len(req.Data) > max {
		return false, signalingDataInvalidErr()
	}
	// forward runs inside this call's signaling order lock (the connection layer dispatches RPCs
	// for the same session concurrently across multiple workers; without this lock the forward order
	// could differ from the accept order); both drop=true cases (tombstone tail packet, over-rate)
	// silently return true per contract — returning an error would make TDesktop render a normal
	// hang-up as "call failed".
	drop, err := r.deps.Phone.Signal(ctx, userID, req.Peer.ID, req.Peer.AccessHash, func(peerUserID int64, peerDevice domain.SessionRef) {
		r.pushPhoneSignalingData(ctx, peerUserID, peerDevice, req.Peer.ID, req.Data)
	})
	if err != nil {
		return false, phoneCallErr(err)
	}
	_ = drop
	return true, nil
}

func (r *Router) onPhoneSetCallRating(ctx context.Context, req *tg.PhoneSetCallRatingRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if _, err := r.phoneRequireUser(ctx); err != nil {
		return nil, err
	}
	// Lenient validation: ratings are often submitted only after the call is reclaimed, so a miss
	// also succeeds. Push to nobody.
	return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
}

func (r *Router) onPhoneSaveCallDebug(ctx context.Context, req *tg.PhoneSaveCallDebugRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	if _, err := r.phoneRequireUser(ctx); err != nil {
		return false, err
	}
	// Discard the debug JSON; phoneCallDiscarded never sets need_debug, and a normal client never
	// reports it on its own.
	return true, nil
}
