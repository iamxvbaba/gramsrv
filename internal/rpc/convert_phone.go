package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// tgPhoneCallProtocol converts the negotiated parameters back to TL.
func tgPhoneCallProtocol(p domain.PhoneCallProtocol) tg.PhoneCallProtocol {
	return tg.PhoneCallProtocol{
		UDPP2P:          p.UDPP2P,
		UDPReflector:    p.UDPReflector,
		MinLayer:        p.MinLayer,
		MaxLayer:        p.MaxLayer,
		LibraryVersions: p.LibraryVersions,
	}
}

func phoneCallProtocolFromTL(p tg.PhoneCallProtocol) domain.PhoneCallProtocol {
	return domain.PhoneCallProtocol{
		UDPP2P:          p.UDPP2P,
		UDPReflector:    p.UDPReflector,
		MinLayer:        p.MinLayer,
		MaxLayer:        p.MaxLayer,
		LibraryVersions: p.LibraryVersions,
	}
}

// tgPhoneCallForViewer maps server state to TL phoneCall* constructors from the
// observer's viewpoint. This is a hard client contract: access_hash / admin_id /
// participant_id stay consistent for the whole lifetime; in the Confirmed state the
// caller sees the peer's g_b as g_a_or_b, and the callee sees g_a.
func tgPhoneCallForViewer(call domain.PhoneCall, viewerID int64) tg.PhoneCallClass {
	switch call.State {
	case domain.PhoneCallStateRequested, domain.PhoneCallStateRinging:
		if viewerID == call.ParticipantID {
			return &tg.PhoneCallRequested{
				Video:         call.Video,
				ID:            call.ID,
				AccessHash:    call.AccessHash,
				Date:          call.Date,
				AdminID:       call.AdminID,
				ParticipantID: call.ParticipantID,
				GAHash:        call.GAHash,
				Protocol:      tgPhoneCallProtocol(call.CallerProtocol),
			}
		}
		return phoneCallWaitingView(call)
	case domain.PhoneCallStateAccepted:
		if viewerID == call.AdminID {
			return &tg.PhoneCallAccepted{
				Video:         call.Video,
				ID:            call.ID,
				AccessHash:    call.AccessHash,
				Date:          call.Date,
				AdminID:       call.AdminID,
				ParticipantID: call.ParticipantID,
				GB:            call.GB,
				Protocol:      tgPhoneCallProtocol(call.Protocol),
			}
		}
		return phoneCallWaitingView(call)
	case domain.PhoneCallStateConfirmed:
		gaOrB := call.GA // Callee viewpoint: use the caller's revealed g_a to verify the commitment and compute AuthKey
		if viewerID == call.AdminID {
			gaOrB = call.GB // Caller viewpoint: use the callee's g_b
		}
		out := &tg.PhoneCall{
			P2PAllowed:          call.P2PAllowed,
			Video:               call.Video,
			ConferenceSupported: true,
			ID:                  call.ID,
			AccessHash:          call.AccessHash,
			Date:                call.Date,
			AdminID:             call.AdminID,
			ParticipantID:       call.ParticipantID,
			GAOrB:               gaOrB,
			KeyFingerprint:      call.KeyFingerprint,
			Protocol:            tgPhoneCallProtocol(call.Protocol),
			Connections:         tgPhoneConnections(call.Connections),
			StartDate:           call.StartDate,
		}
		return out
	case domain.PhoneCallStateDiscarded:
		return tgPhoneCallDiscarded(call)
	default:
		return &tg.PhoneCallEmpty{ID: call.ID}
	}
}

// tgPhoneConnections converts the issued STUN/TURN entries into TL
// phoneConnectionWebrtc. It always returns non-nil (gotd encodes a nil vector the
// same as an empty one, but we keep it explicit). It never emits legacy
// phoneConnection (Telegram's own reflector protocol): modern tgcalls use WebRTC
// ICE and only consume webrtc entries.
func tgPhoneConnections(conns []domain.PhoneCallConnection) []tg.PhoneConnectionClass {
	out := make([]tg.PhoneConnectionClass, 0, len(conns))
	for _, c := range conns {
		out = append(out, &tg.PhoneConnectionWebrtc{
			Turn:     c.Turn,
			Stun:     c.Stun,
			ID:       c.ID,
			IP:       c.IP,
			Ipv6:     "",
			Port:     c.Port,
			Username: c.Username,
			Password: c.Password,
		})
	}
	return out
}

func phoneCallWaitingView(call domain.PhoneCall) *tg.PhoneCallWaiting {
	out := &tg.PhoneCallWaiting{
		Video:         call.Video,
		ID:            call.ID,
		AccessHash:    call.AccessHash,
		Date:          call.Date,
		AdminID:       call.AdminID,
		ParticipantID: call.ParticipantID,
		Protocol:      tgPhoneCallProtocol(call.Protocol),
	}
	if call.ReceiveDate > 0 {
		// The caller uses receive_date != 0 to switch from "waiting" to "peer ringing" and switch to a 90s ring timer.
		out.SetReceiveDate(call.ReceiveDate)
	}
	return out
}

// tgPhoneCallDiscarded builds the terminal view. need_rating/need_debug are always
// left unset: that way the client neither shows the rating dialog nor uploads
// tgcalls debug data.
func tgPhoneCallDiscarded(call domain.PhoneCall) *tg.PhoneCallDiscarded {
	out := &tg.PhoneCallDiscarded{
		Video: call.Video,
		ID:    call.ID,
	}
	if reason := tgPhoneCallDiscardReasonWithSlug(call.DiscardReason, call.DiscardReasonSlug); reason != nil {
		out.SetReason(reason)
	}
	if call.Duration > 0 {
		out.SetDuration(call.Duration)
	}
	return out
}

// tgPhoneCallStopRinging builds the synthetic terminal state for "the callee's
// other device stopped ringing".
//
// Hard contract (do not change back to pushing phoneCallAccepted): on the incoming
// side, TDesktop calls finish(Failed) on phoneCallAccepted and sends discardCall
// back, while DrKLO sends confirmCall per the caller flow -- either would kill the
// just-established call. Only a phoneCallDiscarded with no busy/migrate reason lets
// both ends take EndedByOtherDevice / stop the foreground service without sending an
// RPC back. This constructor does not change server state or enter a tombstone.
func tgPhoneCallStopRinging(call domain.PhoneCall) *tg.PhoneCallDiscarded {
	return &tg.PhoneCallDiscarded{
		Video: call.Video,
		ID:    call.ID,
	}
}

func tgPhoneCallDiscardReason(r domain.PhoneCallDiscardReason) tg.PhoneCallDiscardReasonClass {
	return tgPhoneCallDiscardReasonWithSlug(r, "")
}

func tgPhoneCallDiscardReasonWithSlug(r domain.PhoneCallDiscardReason, slug string) tg.PhoneCallDiscardReasonClass {
	switch r {
	case domain.PhoneCallDiscardReasonMissed:
		return &tg.PhoneCallDiscardReasonMissed{}
	case domain.PhoneCallDiscardReasonDisconnect:
		return &tg.PhoneCallDiscardReasonDisconnect{}
	case domain.PhoneCallDiscardReasonHangup:
		return &tg.PhoneCallDiscardReasonHangup{}
	case domain.PhoneCallDiscardReasonBusy:
		return &tg.PhoneCallDiscardReasonBusy{}
	case domain.PhoneCallDiscardReasonMigrateConference:
		return &tg.PhoneCallDiscardReasonMigrateConferenceCall{Slug: slug}
	default:
		return nil
	}
}

func phoneCallDiscardReasonFromTL(r tg.PhoneCallDiscardReasonClass) domain.PhoneCallDiscardReason {
	switch r.(type) {
	case *tg.PhoneCallDiscardReasonMissed:
		return domain.PhoneCallDiscardReasonMissed
	case *tg.PhoneCallDiscardReasonDisconnect:
		return domain.PhoneCallDiscardReasonDisconnect
	case *tg.PhoneCallDiscardReasonBusy:
		return domain.PhoneCallDiscardReasonBusy
	case *tg.PhoneCallDiscardReasonMigrateConferenceCall:
		return domain.PhoneCallDiscardReasonMigrateConference
	case *tg.PhoneCallDiscardReasonHangup:
		return domain.PhoneCallDiscardReasonHangup
	default:
		// Includes nil (the client sent no reason): treat as hangup.
		return domain.PhoneCallDiscardReasonHangup
	}
}

func phoneCallDiscardReasonSlugFromTL(r tg.PhoneCallDiscardReasonClass) string {
	if migrate, ok := r.(*tg.PhoneCallDiscardReasonMigrateConferenceCall); ok {
		return migrate.Slug
	}
	return ""
}
