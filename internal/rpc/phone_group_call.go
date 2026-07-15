package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
)

// 超级群语音聊天（group call）核心 RPC。信令真值在 GroupCallStore（version 单调），
// 媒体面经 deps.SFU（M0 为 Disabled：纯信令，客户端停留在 Connecting 属预期）。

// groupCallErr 把 domain 群通话错误映射为 RPC_ERROR。
func groupCallErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrGroupCallInvalid):
		return groupCallInvalidErr()
	case errors.Is(err, domain.ErrGroupCallDiscarded):
		return groupCallAlreadyDiscardedErr()
	case errors.Is(err, domain.ErrGroupCallAlreadyStarted):
		return groupCallAlreadyStartedErr()
	case errors.Is(err, domain.ErrGroupCallSSRCDuplicate):
		return groupCallSSRCDuplicateErr()
	case errors.Is(err, domain.ErrGroupCallNotJoined):
		return groupCallJoinMissingErr()
	case errors.Is(err, domain.ErrConferenceChainInvalid):
		return confWriteChainInvalidErr()
	default:
		return internalErr()
	}
}

// groupCallScope 是群通话 handler 的通用前置解析结果。
type groupCallScope struct {
	userID  int64
	call    domain.GroupCall
	channel domain.Channel
	member  domain.ChannelMember
}

func (s *groupCallScope) canManage() bool {
	if s.call.Conference() {
		return s.userID != 0 && s.userID == s.call.CreatorUserID
	}
	return channelMemberIsAdmin(s.member)
}

// groupCallScopeFrom 解析 InputGroupCallClass 并校验访问权。普通 group call 继续
// 走频道成员资格；conference call 走 creator/participant/invite/slug 访问模型。
func (r *Router) groupCallScopeFrom(ctx context.Context, in tg.InputGroupCallClass) (*groupCallScope, error) {
	if r.deps.GroupCalls == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var call domain.GroupCall
	var found bool
	allowBySlug := false
	switch v := in.(type) {
	case *tg.InputGroupCall:
		call, found, err = r.deps.GroupCalls.Get(ctx, v.ID)
		if err != nil {
			return nil, internalErr()
		}
		if !found || call.AccessHash != v.AccessHash {
			return nil, groupCallInvalidErr()
		}
	case *tg.InputGroupCallSlug:
		call, found, err = r.deps.GroupCalls.GetBySlug(ctx, v.Slug)
		if err != nil {
			return nil, internalErr()
		}
		if !found || !call.Conference() {
			return nil, groupCallInvalidErr()
		}
		allowBySlug = true
	case *tg.InputGroupCallInviteMessage:
		call, _, found, err = r.deps.GroupCalls.GetByInviteMessage(ctx, userID, v.MsgID)
		if err != nil {
			return nil, internalErr()
		}
		if !found || !call.Conference() {
			return nil, groupCallInvalidErr()
		}
	default:
		return nil, groupCallInvalidErr()
	}
	if call.Conference() {
		if !allowBySlug {
			allowed, err := r.conferenceCallCanAccess(ctx, call.ID, userID)
			if err != nil {
				return nil, internalErr()
			}
			if !allowed {
				return nil, groupCallForbiddenErr()
			}
		}
		return &groupCallScope{userID: userID, call: call}, nil
	}
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, call.ChannelID)
	if err != nil {
		return nil, groupCallForbiddenErr()
	}
	if view.Self.Status != domain.ChannelMemberActive {
		return nil, groupCallForbiddenErr()
	}
	return &groupCallScope{userID: userID, call: call, channel: view.Channel, member: view.Self}, nil
}

// groupCallJoinAsChannelID 解析 joinGroupCall.join_as：self（缺省/自己）→0；
// 本频道本身且 viewer 是 admin（匿名管理员/创建者语义，TDesktop RTMP createBox
// 对 creator 硬编码 joinAs=peer）→ channelID；其余身份返回 JOIN_AS_PEER_INVALID。
func (r *Router) groupCallJoinAsChannelID(scope *groupCallScope, joinAs tg.InputPeerClass) (int64, error) {
	switch v := joinAs.(type) {
	case nil, *tg.InputPeerSelf, *tg.InputPeerEmpty:
		return 0, nil
	case *tg.InputPeerUser:
		if v.UserID == scope.userID {
			return 0, nil
		}
	case *tg.InputPeerChannel:
		if !scope.call.Conference() && v.ChannelID == scope.channel.ID && channelMemberIsAdmin(scope.member) {
			return v.ChannelID, nil
		}
	}
	return 0, tgerr400("JOIN_AS_PEER_INVALID")
}

// onPhoneGetGroupCallJoinAs 返回入会可选身份：所有人可用自己；频道 admin 额外
// 可用频道本身（匿名身份）。TDesktop 在候选 >1 时显示 "join as" 选择框。
func (r *Router) onPhoneGetGroupCallJoinAs(ctx context.Context, peer tg.InputPeerClass) (*tg.PhoneJoinAsPeers, error) {
	userID, err := r.phoneRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	out := &tg.PhoneJoinAsPeers{
		Peers: []tg.PeerClass{&tg.PeerUser{UserID: userID}},
		Chats: []tg.ChatClass{},
		Users: r.tgUsersForIDs(ctx, userID, []int64{userID}),
	}
	if r.deps.Channels == nil {
		return out, nil
	}
	dp, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil || dp.Type != domain.PeerTypeChannel || dp.ID == 0 {
		return out, nil
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, dp.ID)
	if err != nil || view.Self.Status != domain.ChannelMemberActive || !channelMemberIsAdmin(view.Self) {
		return out, nil
	}
	out.Peers = append(out.Peers, &tg.PeerChannel{ChannelID: view.Channel.ID})
	out.Chats = append(out.Chats, tgChannel(userID, view.Channel, &view.Self))
	return out, nil
}

func (r *Router) conferenceCallCanAccess(ctx context.Context, callID, userID int64) (bool, error) {
	recipients, err := r.deps.GroupCalls.ConferenceRecipients(ctx, callID)
	if err != nil {
		return false, err
	}
	for _, id := range recipients {
		if id == userID {
			return true, nil
		}
	}
	return false, nil
}

func (r *Router) onPhoneCreateGroupCall(ctx context.Context, req *tg.PhoneCreateGroupCallRequest) (tg.UpdatesClass, error) {
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
	now := int(r.clock.Now().Unix())
	scheduleDate, _ := req.GetScheduleDate()
	if scheduleDate < 0 || (scheduleDate > 0 && scheduleDate <= now) {
		// TDesktop 选择器只给未来时间；过去时间直接拒绝（容忍在途秒差由客户端保证）。
		return nil, tgerr400("SCHEDULE_DATE_INVALID")
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
	// 广播频道直播、以及任意 RTMP 直播：参与者都是纯观众（listener），入会即
	// 强制静音且不可自解（join_muted）。RTMP 尤其关键——TDesktop 在 stream 模式下
	// 若发现 self 行非 force-muted 会每 3s `Rejoin after unforcemute`，导致死循环。
	joinMuted := !view.Channel.Megagroup || req.RtmpStream
	call, err := r.deps.GroupCalls.Create(ctx, view.Channel.ID, userID, req.Title, req.RtmpStream, joinMuted, scheduleDate, now)
	if err != nil {
		return nil, groupCallErr(err)
	}
	// 服务消息（带频道 pts，离线成员经 channels difference 补收）：
	// 定时通话发 scheduled（"预约了视频聊天"），立即通话发 started。
	serviceAction := domain.ChannelMessageAction{
		Type:           domain.ChannelActionGroupCall,
		CallID:         call.ID,
		CallAccessHash: call.AccessHash,
	}
	if scheduleDate > 0 {
		serviceAction.Type = domain.ChannelActionGroupCallScheduled
		serviceAction.CallScheduleDate = scheduleDate
	}
	var serviceRes domain.SendChannelMessageResult
	if res, err := r.deps.Channels.AppendCallServiceMessage(ctx, view.Channel.ID, userID, now, serviceAction); err == nil {
		serviceRes = res
		_ = r.deps.GroupCalls.SetStartedMessageID(ctx, call.ID, res.Message.ID)
	} else {
		r.log.Warn("group call started service message", zap.Int64("channel_id", view.Channel.ID), zap.Error(err))
	}
	channel, err := r.deps.Channels.SetActiveCall(ctx, view.Channel.ID, call.ID, call.AccessHash, false)
	if err != nil {
		channel = view.Channel
		channel.ActiveCallID = call.ID
		channel.ActiveCallAccessHash = call.AccessHash
	}
	// 扇出：banner flag 刷新 + updateGroupCall + 服务消息。
	r.pushChannelStateToMembers(ctx, userID, channel)
	r.pushGroupCallUpdate(ctx, channel, call)
	if serviceRes.Event.Pts != 0 {
		r.pushGroupCallServiceMessage(ctx, userID, serviceRes)
	}
	// 响应：updateGroupCall + 服务消息（发起设备视角）。TDesktop 创建后自行 joinGroupCall。
	update := &tg.UpdateGroupCall{Call: tgGroupCall(call, userID, true, r.cfg.PublicBaseURL)}
	update.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
	out := r.groupCallUpdateContainer(ctx, userID, channel, update, []int64{userID})
	if serviceRes.Event.Pts != 0 {
		if msgUpdate := tgChannelUpdate(userID, serviceRes.Event); msgUpdate != nil {
			out.Updates = append(out.Updates, msgUpdate)
		}
	}
	return out, nil
}

func (r *Router) onPhoneJoinGroupCall(ctx context.Context, req *tg.PhoneJoinGroupCallRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.call.Active() {
		return nil, groupCallAlreadyDiscardedErr()
	}
	if scope.call.RtmpStream {
		return r.joinRtmpGroupCall(ctx, scope, req)
	}
	joinAsChannelID, err := r.groupCallJoinAsChannelID(scope, req.JoinAs)
	if err != nil {
		return nil, err
	}
	// 解析上行 join JSON（容忍 video_stopped 等 flag 与 ssrc-groups——TDesktop join 即带）。
	offer, ssrc, err := parseGroupCallJoinPayload(req.Params.Data)
	if err != nil {
		r.log.Warn("group call join payload", zap.Error(err))
		return nil, groupCallInvalidErr()
	}
	// 房间上限（演示规模）：rejoin（已在会换 ssrc）不受限。
	if max := r.cfg.GroupCallMaxParticipants; max > 0 && scope.call.ParticipantsCount >= max {
		if p, found, _ := r.deps.GroupCalls.Participant(ctx, scope.call.ID, scope.userID); !found || p.Left {
			return nil, groupCallForbiddenErr()
		}
	}
	now := int(r.clock.Now().Unix())
	var publicKey []byte
	if pk, ok := req.GetPublicKey(); ok {
		publicKey = append([]byte(nil), pk[:]...)
	}
	var joinBlock []byte
	if block, ok := req.GetBlock(); ok {
		joinBlock = append([]byte(nil), block...)
	}
	// 视频内部状态：endpoint 服务端铸造（join 响应 video.endpoint 与日后
	// participant.video.endpoint 必须逐字节一致）；ssrc-groups 无论摄像头开关都
	// 存档——video_stopped=false（join flag 或后续 self-edit）时原样回放。
	endpoint := groupCallEndpointID(sfu.EndpointMain, offer.AudioSSRC)
	videoState := participantVideoState{
		Endpoint:     endpoint,
		SourceGroups: groupCallSsrcGroupsFromOffer(offer),
		Active:       !req.VideoStopped && len(offer.SsrcGroups) > 0,
	}
	mut, err := r.deps.GroupCalls.Join(ctx, domain.JoinGroupCallRequest{
		CallID:          scope.call.ID,
		UserID:          scope.userID,
		JoinAsChannelID: joinAsChannelID,
		SSRC:            ssrc,
		Muted:           req.Muted,
		IsAdmin:         scope.canManage(),
		PublicKey:       publicKey,
		JoinBlock:       joinBlock,
		VideoJSON:       encodeVideoState(videoState),
		Now:             now,
	})
	if err != nil {
		return nil, groupCallErr(err)
	}
	// 媒体面：SFU 分配 endpoint（M0 Disabled：语法完备空 candidates，客户端保持
	// Connecting 并以 4s checkGroupCall 心跳维持保活——M0 sweeper 判据依赖该行为）。
	sfuService := r.deps.SFU
	if sfuService == nil {
		sfuService = sfu.Disabled()
	}
	answer, err := sfuService.Join(ctx, scope.call.ID, scope.userID, sfu.EndpointMain, offer)
	if err != nil {
		// 媒体面失败回滚信令侧（保持两面一致），返回 500。
		_, _ = r.deps.GroupCalls.Leave(ctx, scope.call.ID, scope.userID, now)
		r.log.Warn("group call sfu join", zap.Error(err))
		return nil, internalErr()
	}
	params, err := buildGroupCallConnectionParams(answer, endpoint)
	if err != nil {
		_ = sfuService.Leave(ctx, scope.call.ID, scope.userID, sfu.EndpointMain)
		_, _ = r.deps.GroupCalls.Leave(ctx, scope.call.ID, scope.userID, now)
		return nil, internalErr()
	}
	var conferenceJoinBlock domain.GroupCallChainBlock
	var hasConferenceJoinBlock bool
	if scope.call.Conference() && len(joinBlock) > 0 {
		block, err := r.deps.GroupCalls.AppendChainBlock(ctx, domain.GroupCallChainBlock{
			CallID:       scope.call.ID,
			SubChainID:   0,
			Offset:       -1,
			AuthorUserID: scope.userID,
			Block:        joinBlock,
			CreatedAt:    now,
		})
		if err != nil {
			_ = sfuService.Leave(ctx, scope.call.ID, scope.userID, sfu.EndpointMain)
			_, _ = r.deps.GroupCalls.Leave(ctx, scope.call.ID, scope.userID, now)
			return nil, groupCallErr(err)
		}
		conferenceJoinBlock = block
		hasConferenceJoinBlock = true
	}
	// 扇出给房间/在线群成员（操作者其它设备含其中；本设备从 RPC 返回拿）。
	channel := r.groupCallMutationFanout(ctx, scope.channel, mut)
	// 响应（TDesktop 从本 RPC 返回的 Updates 摘取 updateGroupCallConnection，不会等推送）：
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{scope.userID})
	out.Updates = append(out.Updates, &tg.UpdateGroupCallConnection{Params: tg.DataJSON{Data: params}})
	callUpdate := &tg.UpdateGroupCall{Call: tgGroupCall(mut.Call, scope.userID, scope.canManage(), r.cfg.PublicBaseURL)}
	if channel.ID != 0 {
		callUpdate.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
	}
	out.Updates = append(out.Updates, callUpdate)
	if hasConferenceJoinBlock {
		nextOffset := conferenceJoinBlock.Offset + 1
		blocks := [][]byte{conferenceJoinBlock.Block}
		r.pushConferenceChainBlocks(ctx, mut.Call, conferenceJoinBlock.SubChainID, blocks, nextOffset)
		out.Updates = append(out.Updates, conferenceChainBlocksUpdate(mut.Call, conferenceJoinBlock.SubChainID, blocks, nextOffset))
	}
	return out, nil
}

func (r *Router) onPhoneLeaveGroupCall(ctx context.Context, req *tg.PhoneLeaveGroupCallRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	mut, err := r.deps.GroupCalls.Leave(ctx, scope.call.ID, scope.userID, now)
	if errors.Is(err, domain.ErrGroupCallNotJoined) {
		// 幂等：重复 leave / sweeper 已清，返回当前快照。
		return r.groupCallUpdateContainer(ctx, scope.userID, scope.channel,
			groupCallUpdateFor(scope.channel, scope.call, scope.userID, false, r.cfg.PublicBaseURL), nil), nil
	}
	if err != nil {
		return nil, groupCallErr(err)
	}
	if r.deps.SFU != nil {
		_ = r.deps.SFU.Leave(ctx, scope.call.ID, scope.userID, sfu.EndpointMain)
	}
	channel := r.groupCallMutationFanout(ctx, scope.channel, mut)
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		&tg.UpdateGroupCallParticipants{
			Call:         &tg.InputGroupCall{ID: mut.Call.ID, AccessHash: mut.Call.AccessHash},
			Participants: tgGroupCallParticipants([]domain.GroupCallParticipant{mut.Participant}, scope.userID),
			Version:      mut.Call.Version,
		}, []int64{scope.userID})
	if mut.Call.Conference() && !mut.Call.Active() {
		out.Updates = append(out.Updates, groupCallUpdateFor(domain.Channel{}, mut.Call, scope.userID, scope.userID == mut.Call.CreatorUserID, r.cfg.PublicBaseURL))
	}
	return out, nil
}

func (r *Router) onPhoneDiscardGroupCall(ctx context.Context, in tg.InputGroupCallClass) (tg.UpdatesClass, error) {
	scope, err := r.groupCallScopeFrom(ctx, in)
	if err != nil {
		return nil, err
	}
	if !scope.canManage() {
		return nil, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	now := int(r.clock.Now().Unix())
	call, activeBeforeDiscard, err := r.deps.GroupCalls.Discard(ctx, scope.call.ID, now)
	if err != nil {
		return nil, groupCallErr(err)
	}
	if r.deps.SFU != nil {
		_ = r.deps.SFU.CloseRoom(ctx, call.ID)
	}
	if call.Conference() {
		r.pushConferenceGroupCallUpdateTo(ctx, call, groupCallParticipantUserIDs(activeBeforeDiscard))
		return r.groupCallUpdateContainer(ctx, scope.userID, domain.Channel{},
			groupCallUpdateFor(domain.Channel{}, call, scope.userID, true, r.cfg.PublicBaseURL), nil), nil
	}
	// RTMP 直播结束：断开推流并清空缓冲（观众后续拉流转 resync/停止）。
	if call.RtmpStream && r.deps.LiveStreams != nil {
		r.deps.LiveStreams.DropChannel(scope.channel.ID)
	}
	// 清 channel 关联 + ended 服务消息（带 duration）。
	channel := scope.channel
	if updated, err := r.deps.Channels.SetActiveCall(ctx, channel.ID, 0, 0, false); err == nil {
		channel = updated
	}
	var serviceRes domain.SendChannelMessageResult
	if res, err := r.deps.Channels.AppendCallServiceMessage(ctx, channel.ID, scope.userID, now, domain.ChannelMessageAction{
		Type:           domain.ChannelActionGroupCall,
		CallID:         call.ID,
		CallAccessHash: call.AccessHash,
		CallDuration:   call.Duration,
	}); err == nil {
		serviceRes = res
	} else {
		r.log.Warn("group call ended service message", zap.Int64("channel_id", channel.ID), zap.Error(err))
	}
	r.pushChannelStateToMembers(ctx, scope.userID, channel)
	r.pushGroupCallUpdate(ctx, channel, call)
	if serviceRes.Event.Pts != 0 {
		r.pushGroupCallServiceMessage(ctx, scope.userID, serviceRes)
	}
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		groupCallUpdateFor(channel, call, scope.userID, true, r.cfg.PublicBaseURL), nil)
	if serviceRes.Event.Pts != 0 {
		if msgUpdate := tgChannelUpdate(scope.userID, serviceRes.Event); msgUpdate != nil {
			out.Updates = append(out.Updates, msgUpdate)
		}
	}
	return out, nil
}

func (r *Router) onPhoneGetGroupCall(ctx context.Context, req *tg.PhoneGetGroupCallRequest) (*tg.PhoneGroupCall, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	page, err := r.deps.GroupCalls.Participants(ctx, scope.call.ID, "", limit)
	if err != nil {
		return nil, groupCallErr(err)
	}
	userIDs := make([]int64, 0, len(page.Participants))
	for _, p := range page.Participants {
		userIDs = append(userIDs, p.UserID)
	}
	chats := []tg.ChatClass{}
	if !scope.call.Conference() {
		chats = append(chats, tgChannel(scope.userID, scope.channel, &scope.member))
	}
	// 定时通话：回填 viewer 自己的开播提醒订阅（客户端 reload 全量重建本地状态）。
	call := r.applyScheduleSubscription(ctx, scope.call, scope.userID)
	return &tg.PhoneGroupCall{
		Call:                   tgGroupCall(call, scope.userID, scope.canManage(), r.cfg.PublicBaseURL),
		Participants:           tgGroupCallParticipants(page.Participants, scope.userID),
		ParticipantsNextOffset: page.NextOffset,
		Chats:                  chats,
		Users:                  r.tgUsersForIDs(ctx, scope.userID, userIDs),
	}, nil
}

func (r *Router) onPhoneGetGroupParticipants(ctx context.Context, req *tg.PhoneGetGroupParticipantsRequest) (*tg.PhoneGroupParticipants, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	page, err := r.deps.GroupCalls.Participants(ctx, scope.call.ID, req.Offset, limit)
	if err != nil {
		return nil, groupCallErr(err)
	}
	userIDs := make([]int64, 0, len(page.Participants))
	for _, p := range page.Participants {
		userIDs = append(userIDs, p.UserID)
	}
	// 响应 version=当前值：客户端 version 跳号后据此重建本地状态并恢复增量应用。
	chats := []tg.ChatClass{}
	if !scope.call.Conference() {
		chats = append(chats, tgChannel(scope.userID, scope.channel, &scope.member))
	}
	return &tg.PhoneGroupParticipants{
		Count:        page.Count,
		Participants: tgGroupCallParticipants(page.Participants, scope.userID),
		NextOffset:   page.NextOffset,
		Chats:        chats,
		Users:        r.tgUsersForIDs(ctx, scope.userID, userIDs),
		Version:      page.Version,
	}, nil
}

// onPhoneCheckGroupCall 是保活与「踢人/重启恢复」的统一出口：返回入参 sources 中
// 仍属于该用户活跃 endpoint 的子集；自己的 ssrc 不在 ⇒ 客户端自动 rejoin。
// 注意：客户端只在 Connecting 态调它（媒体连通后心跳停止），不可据此单独判死。
func (r *Router) onPhoneCheckGroupCall(ctx context.Context, req *tg.PhoneCheckGroupCallRequest) ([]int, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	now := int(r.clock.Now().Unix())
	active, joined, err := r.deps.GroupCalls.Touch(ctx, scope.call.ID, scope.userID, now)
	if err != nil {
		return nil, groupCallErr(err)
	}
	if !joined {
		return []int{}, nil
	}
	activeSet := make(map[int]struct{}, len(active))
	for _, ssrc := range active {
		activeSet[int(int32(uint32(ssrc)))] = struct{}{}
	}
	// presentation 的全部 ssrc（音频+视频层+RTX）也属于活跃 endpoint：TDesktop 把
	// screen ssrc 一并塞进 sources，DrKLO 用 presentation.audio_source（缺省退化
	// 取首个视频 ssrc）——任一不在返回集合都会触发 4s 循环重建屏幕实例。
	if p, found, err := r.deps.GroupCalls.Participant(ctx, scope.call.ID, scope.userID); err == nil && found && !p.Left {
		if st, ok := decodeVideoState(p.PresentationJSON); ok && st.Active {
			if st.AudioSource != 0 {
				activeSet[int(int32(uint32(st.AudioSource)))] = struct{}{}
			}
			for _, g := range st.SourceGroups {
				for _, src := range g.Sources {
					activeSet[int(int32(uint32(src)))] = struct{}{}
				}
			}
		}
	}
	out := make([]int, 0, len(req.Sources))
	for _, src := range req.Sources {
		if _, ok := activeSet[src]; ok {
			out = append(out, src)
		}
	}
	return out, nil
}

func groupCallUpdateFor(channel domain.Channel, call domain.GroupCall, viewerUserID int64, canManage bool, publicBaseURL ...string) *tg.UpdateGroupCall {
	update := &tg.UpdateGroupCall{Call: tgGroupCall(call, viewerUserID, canManage, publicBaseURL...)}
	if channel.ID != 0 {
		update.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
	}
	return update
}
