package rpc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// RTMP 直播（Live Stream）RPC：createGroupCall(rtmp_stream) 建房后，推流方（OBS）
// 用 getGroupCallStreamRtmpUrl 拿到的 url/key 推流，观众 join 后经
// upload.getFile(inputGroupCallStream) 拉 broadcast chunk。
//
// 与普通语音聊天（RTC/SFU）关键差异：RTMP join 不建 SFU 连接、不做 ssrc 唯一性
// 媒体面校验；updateGroupCallConnection.params 返回 {"stream":true,"rtmp":true}
// 让 TDesktop 切 broadcast 模式（ParseJoinResponse → JoinBroadcastStream）。

// rtmpConnectionParams 是 RTMP 观众 join 响应的下行 JSON（tgcalls broadcast 分支）。
// 可管理者附 rtmp_stream_url/key 供直播设置页展示（普通观众不下发，避免泄漏推流凭据）。
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

// joinRtmpGroupCall 处理 RTMP 直播房间的 joinGroupCall。
func (r *Router) joinRtmpGroupCall(ctx context.Context, scope *groupCallScope, req *tg.PhoneJoinGroupCallRequest) (tg.UpdatesClass, error) {
	// 房间上限：RTMP 观众也计入 participants_count；rejoin（已在会换 ssrc）不受限。
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
	// RTMP 观众的 join JSON 仍带 tgcalls ssrc（客户端为拉流也建了本地 controller）；
	// 解析失败/缺失不致命——直播不需要 SFU，用随机 ssrc 记账保证参与者行有效。
	ssrc := int64(0)
	if _, s, err := parseGroupCallJoinPayload(req.Params.Data); err == nil {
		ssrc = s
	}
	if ssrc == 0 {
		ssrc = randomSSRC()
	}
	// RTMP 观众（含创建者，其经 OBS 独立推流，在 group call 里同样是纯观众）一律
	// force-muted：IsAdmin=false 让 store 对 join_muted 房间置 muted+muted_by_admin，
	// self 行输出 muted=true / can_self_unmute=false，TDesktop 才会稳定停在 stream 模式。
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
	// 可管理者拿推流 url/key（直播设置页展示）；普通观众只得 stream:true。
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
	// updateGroupCall 必须先于 updateGroupCallConnection：TDesktop 按序 applyUpdates，
	// 处理 connection 时若还没从 groupCall 读到 stream_dc_id 会打
	// "Api Error: Empty stream_dc_id" 并 fallback 主 DC。
	callUpdate := &tg.UpdateGroupCall{Call: tgGroupCall(mut.Call, scope.userID, scope.canManage(), r.cfg.PublicBaseURL)}
	if channel.ID != 0 {
		callUpdate.SetPeer(&tg.PeerChannel{ChannelID: channel.ID})
	}
	out.Updates = append(out.Updates, callUpdate)
	out.Updates = append(out.Updates, &tg.UpdateGroupCallConnection{Params: tg.DataJSON{Data: params}})
	return out, nil
}

// onPhoneGetGroupCallStreamRtmpURL 返回频道的 RTMP 推流 url/key（创建前预览、
// 直播设置页展示、revoke 轮换）。仅频道管理员可调；revoke=true 生成新 key，旧 key
// 立即失效并断开正在进行的推流。
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
		// 旧 key 失效后仍在推的连接必须断开（否则用旧 key 的推流会继续被接收）。
		r.deps.LiveStreams.DropChannel(peer.ID)
	}
	return &tg.PhoneGroupCallStreamRtmpURL{URL: r.rtmpIngestURL(), Key: key}, nil
}

// onPhoneGetGroupCallStreamChannels 返回 RTMP 直播的当前时间轴（unified：channel=1
// scale=0），供 tgcalls 决定从哪个 time_ms 起拉 chunk。无活跃推流时返回空列表，
// TDesktop 据此显示"等待推流"占位并循环重试。
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

// rtmpIngestURL 返回展示给推流端的 RTMP 服务器地址。
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
