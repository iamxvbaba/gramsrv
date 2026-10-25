package rpc

import (
	"context"
	"strconv"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 私聊通话 P3：STUN/TURN 中继参数（phoneConnectionWebrtc）与 p2p_allowed 真值。
// 凭据在 requestCall 受理时一次性签发并存于 call 快照——同一通话的所有视角
//（RPC 响应与推送、主叫与被叫）看到同一份 connections，与官方行为一致。

// phoneP2PGate 是 privacy 服务提供的互认联系人硬门槛。privacy 服务缺席或未实现
// 该接口时一律拒绝 P2P（安全默认，fail closed）：P2P 会暴露双方真实 IP，宁可多走
// TURN 中继也不让陌生人拿到。
type phoneP2PGate interface {
	P2PAllowedBetween(ctx context.Context, callerID, calleeID int64) (bool, error)
}

// phoneCallPrivacyP2P 决定私聊通话能否 P2P 直连。安全默认：只有互认联系人之间才
// 放行 P2P（privacy 服务按双方 phone_p2p 规则 AND 且联系互为存录），无法确证
// 联系人关系时拒绝 P2P（p2p_allowed=false 时 tgcalls 丢弃全部非 relay candidate）。
// 旧实现失败放行（P1 兼容）：隐私服务缺席/出错即默认放行——这是 IP 泄露来源，已改为
// fail closed。CallForceRelay 仍强制全线中继（调试用）。
func (r *Router) phoneCallPrivacyP2P(ctx context.Context, callerID, calleeID int64) bool {
	if r.cfg.CallForceRelay {
		return false
	}
	gate, ok := r.deps.Privacy.(phoneP2PGate)
	if !ok {
		r.log.Warn("phone p2p denied: privacy service lacks reciprocal-contact gate")
		return false
	}
	allowed, err := gate.P2PAllowedBetween(ctx, callerID, calleeID)
	if err != nil {
		r.log.Warn("phone p2p denied: reciprocal-contact gate error", zap.Error(err))
		return false
	}
	return allowed
}

// phoneCallConnections 为一通通话签发 STUN/TURN 条目。TURN 未启用返回空列表
// （tgcalls 遍历空列表零次，退回纯信令交换 host candidates 的 LAN 直连）。
func (r *Router) phoneCallConnections(callerID int64) []domain.PhoneCallConnection {
	t := r.deps.TURN
	if t == nil || !t.Enabled() {
		return nil
	}
	username, password, err := t.Credentials(strconv.FormatInt(callerID, 10))
	if err != nil {
		r.log.Warn("phone call turn credentials", zap.Error(err))
		return nil
	}
	// ⚠ STUN 与 TURN 必须拆成两个条目（与官方一致）：DrKLO 的 JNI 层根本不读
	// stun flag——单条目 stun+turn 在 Android 上只会产出 TURN server、丢失 STUN
	//（org_telegram_messenger_voip_Instance.cpp:848-884）。TDesktop 两种写法都认。
	// TURN username 是 REST 格式 "<expiry>:<uid>"，天然避开 "reflector" 劫持禁区。
	return []domain.PhoneCallConnection{
		{ID: 1, IP: t.IP(), Port: t.Port(), Stun: true},
		{ID: 2, IP: t.IP(), Port: t.Port(), Username: username, Password: password, Turn: true},
	}
}
