package rpc

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"

	appphone "telesrv/internal/app/phone"
	"telesrv/internal/domain"
)

// 私聊 1:1 通话 RPC handler。状态机与密钥材料归 app/phone；本文件只做
// 鉴权、入参校验、隐私/拉黑闸门、推送编排与 TL 转换。
// 推送策略总表与硬契约见设计文档；标 ⚠ 的注释是对抗评审定下的修正，勿改回。

// phoneCallErr 把 app/phone 业务错误映射为 RPC_ERROR。
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

// phoneRequireUser 是 phone.* 的统一登录闸门。
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
	// 自呼 / bot / 不存在统一 USER_ID_INVALID。
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
	// 推被叫全部在线设备。sent==0（被叫全离线）不报错：主叫客户端按
	// callReceiveTimeoutMs(20s) 自行超时 discard(missed)，服务端兜底超时收尾。
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
		// 终态晚到的 receivedCall 无害：宽容返回 true（多设备竞态常见）。
		if errors.Is(err, appphone.ErrPeerInvalid) {
			if _, found := r.deps.Phone.Lookup(ctx, peer.ID, peer.AccessHash); found {
				return true, nil
			}
		}
		return false, phoneCallErr(err)
	}
	if transitioned {
		// ⚠ P1-2：receiveDate 推送必须在 P1 就位。主叫只有收到带 receive_date 的
		// phoneCallWaiting 才会把 20s receive 定时器换成 90s ring 定时器。
		r.pushPhoneCall(ctx, call.AdminID, call, "phone call ringing")
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
	// 推主叫全部设备：phoneCallAccepted{g_b, 协商 protocol}，主叫据此算 AuthKey 并 confirm。
	r.pushPhoneCall(ctx, call.AdminID, call, "phone call accepted")
	// ⚠ P0-1：被叫其它设备推合成 phoneCallDiscarded 停振铃（ctx except 排除接听设备）。
	// 绝不能推 phoneCallAccepted——incoming 侧收到 accepted 会 finish(Failed) 回发
	// discardCall（TDesktop）/按主叫流程发 confirmCall（DrKLO），杀死刚建立的通话。
	r.pushPhoneCallStopRinging(ctx, call)
	return &tg.PhonePhoneCall{
		PhoneCall: tgPhoneCallForViewer(call, userID), // 被叫视角：phoneCallWaiting，等 confirm
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
			// g_a 揭示与承诺不符：通话已被强制终结，推送双方防状态机卡死。
			r.pushPhoneCallDiscardedBoth(ctx, call)
			return nil, callPeerInvalidErr()
		}
		return nil, phoneCallErr(err)
	}
	// 推被叫全部设备：phoneCall{g_a_or_b=GA}。实际只有接听设备持有本 call，
	// 其余设备按 call_id 不识别而静默忽略。
	r.pushPhoneCall(ctx, call.ParticipantID, call, "phone call confirmed")
	return &tg.PhonePhoneCall{
		PhoneCall: tgPhoneCallForViewer(call, userID), // 主叫视角：g_a_or_b = g_b
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
		// 对端全部设备 + 发起者其它设备（ctx except 排除发起设备，其结果在 RPC 响应里）。
		r.pushPhoneCallDiscardedBoth(ctx, call)
		if reason != domain.PhoneCallDiscardReasonMigrateConference {
			// 落 messageActionPhoneCall 历史（带 pts 走 outbox，双方全部设备可靠收到）。
			r.sendPhoneCallServiceMessage(ctx, call)
		}
	}
	// 双方同时挂断的竞态：先到者定 reason，后到者拿终态快照（幂等成功）。
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
	// forward 在该通话的信令顺序锁内执行（连接层对同 session 的 RPC 是多 worker
	// 并发分发，无此锁则转发顺序可能与受理顺序不一致）；drop=true 的两种情况
	//（tombstone 尾包、超速率）都按契约静默返回 true——返回错误会让 TDesktop
	// 把正常挂断渲染成「通话失败」。
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
	// 宽容校验：评分常在通话回收后才提交，miss 也成功。不推送任何人。
	return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
}

func (r *Router) onPhoneSaveCallDebug(ctx context.Context, req *tg.PhoneSaveCallDebugRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	if _, err := r.phoneRequireUser(ctx); err != nil {
		return false, err
	}
	// 丢弃 debug JSON；phoneCallDiscarded 恒不置 need_debug，正常客户端不会主动上报。
	return true, nil
}
