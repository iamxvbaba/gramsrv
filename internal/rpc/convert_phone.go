package rpc

import (
	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// tgPhoneCallProtocol 把协商参数转回 TL。
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

// tgPhoneCallForViewer 按观察者视角把服务端状态映射为 TL phoneCall* 构造器。
// 这是客户端硬契约：access_hash / admin_id / participant_id 全生命周期一致；
// Confirmed 态主叫看到的 g_a_or_b 是对方的 g_b、被叫看到的是 g_a。
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
		gaOrB := call.GA // 被叫视角：拿主叫揭示的 g_a 验承诺、算 AuthKey
		if viewerID == call.AdminID {
			gaOrB = call.GB // 主叫视角：拿被叫的 g_b
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

// tgPhoneConnections 把签发的 STUN/TURN 条目转为 TL phoneConnectionWebrtc。
// 永远返回非 nil（gotd 对 nil vector 编码与空 vector 相同，但保持显式）。
// 不下发 legacy phoneConnection（Telegram 自有 reflector 协议）：现代 tgcalls
// 走 WebRTC ICE，只消费 webrtc 条目。
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
		// 主叫据 receive_date != 0 从「等待」切「对方振铃中」并换用 90s ring 定时器。
		out.SetReceiveDate(call.ReceiveDate)
	}
	return out
}

// tgPhoneCallDiscarded 构造终态视图。need_rating/need_debug 恒不置位：
// 客户端便不弹评分框、不上传 tgcalls debug 数据。
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

// tgPhoneCallStopRinging 构造「被叫其它设备停振铃」的合成终态。
//
// 硬契约（勿改回推 phoneCallAccepted）：TDesktop incoming 侧收到 phoneCallAccepted
// 会 finish(Failed) 并回发 discardCall、DrKLO 会按主叫流程发 confirmCall——任一都会
// 杀死刚建立的通话。无 busy/migrate reason 的 phoneCallDiscarded 才会让两端走
// EndedByOtherDevice / 停止前台服务且不回发 RPC。本构造不改服务端状态、不进 tombstone。
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
		// 含 nil（客户端未带 reason）：按 hangup 处理。
		return domain.PhoneCallDiscardReasonHangup
	}
}

func phoneCallDiscardReasonSlugFromTL(r tg.PhoneCallDiscardReasonClass) string {
	if migrate, ok := r.(*tg.PhoneCallDiscardReasonMigrateConferenceCall); ok {
		return migrate.Slug
	}
	return ""
}
