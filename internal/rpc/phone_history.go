package rpc

import (
	"context"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// sendPhoneCallServiceMessage records a terminated call as messageActionPhoneCall history (a full
// replica of the pin service-message pattern, messages_pin.go). Key points:
//   - the sender is always the caller's AdminID — no matter who initiates the discard (official
//     semantics: the caller side renders "outgoing", the callee side renders "missed call/call X
//     minutes", and missed counts as unread on the callee side);
//   - Origin is zeroed: all devices on both sides (including the discard-initiating device) receive
//     updateNewMessage (with pts) via the outbox — the initiating device's RPC response only holds
//     updatePhoneCall, so the service message must come from the update stream; an offline device
//     catches up via getDifference on reconnect, which exactly compensates for the unreliability of
//     direct signaling push (this is where "missed call" comes from);
//   - RandomID is derived deterministically from callID: the state machine guarantees a call records
//     history only once, and an extreme replay is absorbed by the send pipeline's random_id dedup;
//   - failure only Warns and does not roll back: the call state is the truth, history is decoration
//     (the same philosophy as pin).
//
// Note: this path bypasses the RPC layer's checkSendRateLimit (rate limiting sits on the handler
// side) — this is intentional; call-flood protection is carried by the call concurrency cap and the
// ringing timeout.
func (r *Router) sendPhoneCallServiceMessage(ctx context.Context, call domain.PhoneCall) {
	if r.deps.Messages == nil || !call.Terminal() {
		return
	}
	recipientBlocked, err := r.peerBlocksUser(ctx, call.AdminID, call.ParticipantID)
	if err != nil {
		recipientBlocked = false
	}
	_, err = r.deps.Messages.SendPrivateText(ctx, call.AdminID, domain.SendPrivateTextRequest{
		SenderUserID:    call.AdminID,
		RecipientUserID: call.ParticipantID,
		RandomID:        phoneCallServiceMessageRandomID(call.ID),
		Media: &domain.MessageMedia{
			Kind: domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{
				Kind: domain.MessageServiceActionPhoneCall,
				Call: &domain.MessagePhoneCallAction{
					CallID:   call.ID,
					Reason:   string(call.DiscardReason),
					Duration: call.Duration,
					Video:    call.Video,
				},
			},
		},
		Date:             int(r.clock.Now().Unix()),
		RecipientBlocked: recipientBlocked,
	})
	if err != nil {
		r.log.Warn("phone call service message",
			zap.Int64("call_id", call.ID),
			zap.Int64("admin_id", call.AdminID),
			zap.Int64("participant_id", call.ParticipantID),
			zap.Error(err))
	}
}

func phoneCallServiceMessageRandomID(callID int64) int64 {
	id := callID ^ 0x70686f6e6563616c // "phonecal"
	if id == 0 {
		return 1
	}
	return id
}
