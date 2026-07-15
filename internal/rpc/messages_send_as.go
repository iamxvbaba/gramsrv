package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) forumSendAsPeer(ctx context.Context, peerInput, sendAsInput tg.InputPeerClass) (*domain.Peer, error) {
	peer, err := r.forumTopicPeer(ctx, peerInput)
	if err != nil {
		return nil, err
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	// 复用统一的 send-as 校验：自己→nil（以自己发言），当前频道/群→匿名/广播自帖，
	// 自己拥有的其它广播频道→个人频道(需会员)。forum topic 与普通消息走同一套资格判定。
	return r.sendAsPeerFromInput(ctx, userID, peer, sendAsInput)
}

func (r *Router) onMessagesSaveDefaultSendAs(ctx context.Context, req *tg.MessagesSaveDefaultSendAsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if userID == 0 {
		return false, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return false, peerIDInvalidErr()
	}
	sendAs, err := r.sendAsPeerFromInput(ctx, userID, peer, req.SendAs)
	if err != nil {
		return false, err
	}
	if r.deps.Channels == nil {
		return false, channelInvalidErr(domain.ErrChannelInvalid)
	}
	if _, err := r.deps.Channels.SaveDefaultSendAs(ctx, userID, domain.SaveChannelDefaultSendAsRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		SendAs:    sendAs,
	}); err != nil {
		return false, channelInvalidErr(err)
	}
	r.invalidateRPCProjectionForPeer(userID, peer)
	return true, nil
}

func (r *Router) sendAsPeerFromInput(ctx context.Context, userID int64, to domain.Peer, input tg.InputPeerClass) (*domain.Peer, error) {
	if input == nil {
		return nil, nil
	}
	if to.Type != domain.PeerTypeChannel {
		return nil, sendAsPeerInvalidErr()
	}
	sendAs, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return nil, sendAsPeerInvalidErr()
	}
	switch sendAs.Type {
	case domain.PeerTypeUser:
		if sendAs.ID != userID {
			return nil, sendAsPeerInvalidErr()
		}
		return nil, nil
	case domain.PeerTypeChannel:
		if sendAs.ID == to.ID {
			// 以当前频道/群本身发言（广播自帖、匿名管理员）。
			if err := r.validateCurrentChannelSendAs(ctx, userID, to.ID); err != nil {
				return nil, err
			}
		} else {
			// 以自己拥有的其它广播频道身份在本群发言（个人频道需会员）。
			if err := r.validateForeignChannelSendAs(ctx, userID, to.ID, sendAs.ID); err != nil {
				return nil, err
			}
		}
		out := sendAs
		return &out, nil
	default:
		return nil, sendAsPeerInvalidErr()
	}
}

// validateForeignChannelSendAs 校验「以自己拥有的另一个（广播）频道身份在 toChannel 里发言」：
// 必须是该频道的 creator 或有 PostMessages 权的 admin；个人频道（非 toChannel 的关联讨论频道）
// 需有效会员。gotd 错误集无 CHAT_SEND_AS_DENIED，故一律收敛为 SEND_AS_PEER_INVALID（客户端在
// 选择阶段已按 premium_required 拦截，此为服务端兜底）。
func (r *Router) validateForeignChannelSendAs(ctx context.Context, userID, toChannelID, sendAsChannelID int64) error {
	if r.deps.Channels == nil {
		return sendAsPeerInvalidErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, sendAsChannelID)
	if err != nil || !canSendAsForeignChannel(view) {
		return sendAsPeerInvalidErr()
	}
	// 接受 iff 会员 或 该频道是本群关联讨论频道（免会员）。先查会员（带 user:base 缓存、且注册默认赠送
	// 故多数命中 true）即短路，省掉 linked 判定那次 ResolveChannel(toChannel)。
	if !r.userIsPremium(ctx, userID) && !r.foreignSendAsIsLinkedChannel(ctx, userID, toChannelID, sendAsChannelID) {
		return sendAsPeerInvalidErr()
	}
	return nil
}

// foreignSendAsIsLinkedChannel 报告 sendAsChannel 是否是 toChannel 的关联讨论频道（该情形免会员，
// 对齐官方：在自己频道的讨论组里以频道身份发言不需要会员）。
func (r *Router) foreignSendAsIsLinkedChannel(ctx context.Context, userID, toChannelID, sendAsChannelID int64) bool {
	if r.deps.Channels == nil {
		return false
	}
	toView, err := r.deps.Channels.ResolveChannel(ctx, userID, toChannelID)
	if err != nil {
		return false
	}
	return toView.Channel.LinkedChatID != 0 && toView.Channel.LinkedChatID == sendAsChannelID
}

// canSendAsForeignChannel 判定 view 描述的频道能否被当前用户「作为身份」在别的群里发言：
// 仅限广播频道，且用户是 creator 或持 PostMessages 权的 admin（以自己拥有的 megagroup 身份发言
// 不是真实的 Telegram 能力，故排除）。
func canSendAsForeignChannel(view domain.ChannelView) bool {
	if !view.Channel.Broadcast {
		return false
	}
	if view.Self.Role == domain.ChannelRoleCreator {
		return true
	}
	if view.Self.Role == domain.ChannelRoleAdmin && view.Self.AdminRights.PostMessages {
		return true
	}
	return false
}

// userIsPremium 报告用户当前是否为有效会员（PremiumActiveAt 即时派生）。
func (r *Router) userIsPremium(ctx context.Context, userID int64) bool {
	if r.deps.Users == nil || userID == 0 {
		return false
	}
	u, err := r.deps.Users.Self(ctx, userID)
	if err != nil {
		return false
	}
	return u.PremiumActiveAt(r.clock.Now().Unix())
}

func (r *Router) resolveSendAsPeer(ctx context.Context, userID int64, to domain.Peer, input tg.InputPeerClass) (*domain.Peer, error) {
	if input != nil {
		return r.sendAsPeerFromInput(ctx, userID, to, input)
	}
	if to.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return nil, nil
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, to.ID)
	if err != nil {
		return nil, nil
	}
	return r.effectiveDefaultSendAs(ctx, userID, view), nil
}

// effectiveDefaultSendAs 解析某频道会话保存的默认 send-as（无显式 send_as 时用）。每次发送都重新
// 校验：当前频道默认按 canCurrentChannelSendAs，外部频道默认按 validateForeignChannelSendAs；失效的
// 默认（被降权 / 不再拥有 / 会员过期）静默回落为「以自己发言」(nil)，对齐官方客户端的 reload-on-400。
func (r *Router) effectiveDefaultSendAs(ctx context.Context, userID int64, view domain.ChannelView) *domain.Peer {
	def := view.Dialog.DefaultSendAs
	if def == nil || def.ID == 0 || def.Type != domain.PeerTypeChannel {
		return nil
	}
	if def.ID == view.Channel.ID {
		if !canCurrentChannelSendAs(view) {
			return nil
		}
		out := *def
		return &out
	}
	if err := r.validateForeignChannelSendAs(ctx, userID, view.Channel.ID, def.ID); err != nil {
		return nil
	}
	out := *def
	return &out
}

// applyForeignDefaultSendAsToFull 在 getFullChannel 投影里补上「外部频道默认 send-as」：当某会话保存的
// 默认是用户自己拥有的另一个广播频道时，设置 channelFull.default_send_as 并把该频道对象放进 Chats
// （否则客户端无法渲染默认 chip）。当前频道默认由 tgChannelFull 处理，此处只管外部频道；按所有权（而非
// 会员）投影——会员到期等情形由发送侧 effectiveDefaultSendAs 兜底回落为自己。
func (r *Router) applyForeignDefaultSendAsToFull(ctx context.Context, userID int64, view domain.ChannelView, full *tg.ChannelFull, chats *[]tg.ChatClass) {
	def := view.Dialog.DefaultSendAs
	if def == nil || def.Type != domain.PeerTypeChannel || def.ID == 0 || def.ID == view.Channel.ID {
		return
	}
	if r.deps.Channels == nil {
		return
	}
	sendAsView, err := r.deps.Channels.ResolveChannel(ctx, userID, def.ID)
	if err != nil || !canSendAsForeignChannel(sendAsView) {
		return
	}
	full.SetDefaultSendAs(&tg.PeerChannel{ChannelID: def.ID})
	*chats = appendUniqueTGChats(*chats, tgChannels(userID, []domain.Channel{sendAsView.Channel})...)
}

func validDefaultSendAsPeer(view domain.ChannelView) *domain.Peer {
	if view.Dialog.DefaultSendAs == nil || view.Dialog.DefaultSendAs.ID == 0 {
		return nil
	}
	switch view.Dialog.DefaultSendAs.Type {
	case domain.PeerTypeUser:
		return nil
	case domain.PeerTypeChannel:
		if view.Dialog.DefaultSendAs.ID != view.Channel.ID || !canCurrentChannelSendAs(view) {
			return nil
		}
		out := *view.Dialog.DefaultSendAs
		return &out
	default:
		return nil
	}
}

func (r *Router) validateCurrentChannelSendAs(ctx context.Context, userID, channelID int64) error {
	if r.deps.Channels == nil {
		return sendAsPeerInvalidErr()
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, userID, channelID)
	if err != nil {
		return sendAsPeerInvalidErr()
	}
	if canCurrentChannelSendAs(view) {
		return nil
	}
	return sendAsPeerInvalidErr()
}

func canCurrentChannelSendAs(view domain.ChannelView) bool {
	if view.Self.Role == domain.ChannelRoleCreator {
		return true
	}
	if view.Channel.Broadcast && view.Self.Role == domain.ChannelRoleAdmin && view.Self.AdminRights.PostMessages {
		return true
	}
	if !view.Channel.Broadcast && view.Self.Role == domain.ChannelRoleAdmin && view.Self.AdminRights.Anonymous {
		return true
	}
	return false
}
