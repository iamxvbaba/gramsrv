package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// accountNotifySettingsService 是 per-scope 通知设置持久化的可选扩展，由
// *app/account.Service 实现。未接通时各 handler 回落历史默认（tdesktop.NotifySettings）。
type accountNotifySettingsService interface {
	GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, error)
	SaveNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error
	SaveNotifySettingsWithDelivery(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings, delivery store.DeliveryOutboxEnqueue) error
	ResetNotifySettingsWithDelivery(ctx context.Context, ownerUserID int64, effects store.DeliveryEffectsBuilder[store.NotifySettingsResetSnapshot]) error
	PeerNotifySettings(ctx context.Context, ownerUserID int64, peers []domain.Peer) (map[domain.Peer]domain.PeerNotifySettings, error)
	AllPeerNotifySettings(ctx context.Context, ownerUserID int64) (map[domain.Peer]domain.PeerNotifySettings, error)
	ListNotifyExceptions(ctx context.Context, ownerUserID int64) ([]domain.NotifyException, error)
}

func (r *Router) accountNotifySvc() (accountNotifySettingsService, bool) {
	svc, ok := r.deps.Account.(accountNotifySettingsService)
	return svc, ok
}

// notifyScopeFromInput 把 InputNotifyPeer 解析为业务作用域。peer/forumTopic 需解析
// 出 domain.Peer（不校验访问权限——通知偏好是请求者自己对某 peer 的设置）。
func (r *Router) notifyScopeFromInput(userID int64, in tg.InputNotifyPeerClass) (domain.NotifyScope, bool) {
	switch p := in.(type) {
	case *tg.InputNotifyUsers:
		return domain.NotifyScope{Kind: domain.NotifyScopeUsers}, true
	case *tg.InputNotifyChats:
		return domain.NotifyScope{Kind: domain.NotifyScopeChats}, true
	case *tg.InputNotifyBroadcasts:
		return domain.NotifyScope{Kind: domain.NotifyScopeBroadcasts}, true
	case *tg.InputNotifyCommunity:
		ref, ok := inputChannelRef(p.Community)
		if !ok {
			return domain.NotifyScope{}, false
		}
		return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeCommunity, ID: ref.ID}}, true
	case *tg.InputNotifyPeer:
		peer, ok := r.domainPeerFromInputPeer(userID, p.Peer)
		if !ok {
			return domain.NotifyScope{}, false
		}
		return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer}, true
	case *tg.InputNotifyForumTopic:
		peer, ok := r.domainPeerFromInputPeer(userID, p.Peer)
		if !ok {
			return domain.NotifyScope{}, false
		}
		return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer, TopicID: p.TopMsgID}, true
	default:
		return domain.NotifyScope{}, false
	}
}

func (r *Router) onAccountGetNotifySettings(ctx context.Context, peer tg.InputNotifyPeerClass) (*tg.PeerNotifySettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	scope, ok := r.notifyScopeFromInput(userID, peer)
	svc, hasSvc := r.accountNotifySvc()
	if !ok || !hasSvc {
		return tdesktop.NotifySettings(), nil
	}
	settings, err := svc.GetNotifySettings(ctx, userID, scope)
	if err != nil {
		return nil, internalErr()
	}
	return tgScopeNotifySettings(scope, &settings), nil
}

func (r *Router) onAccountUpdateNotifySettings(ctx context.Context, req *tg.AccountUpdateNotifySettingsRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	scope, ok := r.notifyScopeFromInput(userID, req.Peer)
	if !ok {
		return false, peerIDInvalidErr()
	}
	settings := domainPeerNotifySettings(req.Settings)
	if v, ok := req.Settings.GetSound(); ok {
		sound, err := domainNotificationSound(v)
		if err != nil {
			return false, err
		}
		settings.Sound = &sound
	}
	if v, ok := req.Settings.GetStoriesSound(); ok {
		sound, err := domainNotificationSound(v)
		if err != nil {
			return false, err
		}
		settings.StoriesSound = &sound
	}
	if err := r.requireAccountDelivery(userID, "account.updateNotifySettings"); err != nil {
		return false, err
	}
	svc, ok := r.accountNotifySvc()
	if !ok {
		return false, internalErr()
	}
	payload, err := encodeDeliveryUpdate(&tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNotifySettings{
			Peer:           tgNotifyPeer(scope),
			NotifySettings: *tgScopeNotifySettings(scope, &settings),
		}},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{},
		Date:  int(r.clock.Now().Unix()),
	})
	if err != nil {
		return false, internalErr()
	}
	excludeAuthKeyID, excludeSessionID := deliveryExclusionFromContext(ctx)
	if err := svc.SaveNotifySettingsWithDelivery(ctx, userID, scope, settings, store.DeliveryOutboxEnqueue{
		TargetUserID: userID, ExcludeAuthKeyID: excludeAuthKeyID, ExcludeSessionID: excludeSessionID,
		Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
	}); err != nil {
		return false, internalErr()
	}
	r.notifySettings.Delete(userID)
	return true, nil
}

func (r *Router) onAccountResetNotifySettings(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if err := r.requireAccountDelivery(userID, "account.resetNotifySettings"); err != nil {
		return false, err
	}
	if r.deps.Account == nil {
		return false, internalErr()
	}
	if err := r.deps.Account.ResetNotifySettingsWithDelivery(ctx, userID, r.notifySettingsResetDeliveryEffects()); err != nil {
		return false, internalErr()
	}
	r.notifySettings.Delete(userID)
	return true, nil
}

func (r *Router) notifySettingsResetDeliveryEffects() store.DeliveryEffectsBuilder[store.NotifySettingsResetSnapshot] {
	return func(snapshot store.NotifySettingsResetSnapshot) ([]store.DeliveryEffect, error) {
		scopes := append([]domain.NotifyScope(nil), snapshot.Scopes...)
		scopes = append(scopes,
			domain.NotifyScope{Kind: domain.NotifyScopeUsers},
			domain.NotifyScope{Kind: domain.NotifyScopeChats},
			domain.NotifyScope{Kind: domain.NotifyScopeBroadcasts},
		)
		seen := make(map[domain.NotifyScope]struct{}, len(scopes))
		updates := make([]tg.UpdateClass, 0, len(scopes))
		for _, scope := range scopes {
			if _, exists := seen[scope]; exists {
				continue
			}
			if scope.Kind != domain.NotifyScopeUsers && scope.Kind != domain.NotifyScopeChats &&
				scope.Kind != domain.NotifyScopeBroadcasts &&
				(scope.Kind != domain.NotifyScopePeer || scope.Peer.ID <= 0 || scope.Peer.Type == "") {
				return nil, store.ErrDeliveryOutboxRequired
			}
			seen[scope] = struct{}{}
			updates = append(updates, &tg.UpdateNotifySettings{
				Peer: tgNotifyPeer(scope), NotifySettings: *tgPeerNotifySettings(nil),
			})
		}
		payload, err := encodeDeliveryUpdate(&tg.Updates{
			Updates: updates, Users: []tg.UserClass{}, Chats: []tg.ChatClass{},
			Date: int(r.clock.Now().Unix()), Seq: 0,
		})
		if err != nil {
			return nil, err
		}
		return []store.DeliveryEffect{store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
			TargetUserID: snapshot.OwnerUserID, Payload: payload,
			RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
		})}, nil
	}
}

// onAccountGetNotifyExceptions 返回"有自定义通知设置的会话"全局索引：每条异常一个
// updateNotifySettings，附带被引用 peer 的 Users/Chats。默认（无 compare 标志）只返
// 消息级异常（mute/silent/show_previews）；compare_stories 额外纳入仅 story 维度异常。
func (r *Router) onAccountGetNotifyExceptions(ctx context.Context, req *tg.AccountGetNotifyExceptionsRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	empty := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: int(r.clock.Now().Unix())}
	svc, ok := r.accountNotifySvc()
	if !ok {
		return empty, nil
	}
	exceptions, err := svc.ListNotifyExceptions(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	compareStories := req != nil && req.CompareStories
	var filterPeer *domain.Peer
	if req != nil {
		if p, ok := req.GetPeer(); ok {
			if scope, ok := r.notifyScopeFromInput(userID, p); ok && scope.Kind == domain.NotifyScopePeer {
				fp := scope.Peer
				filterPeer = &fp
			}
		}
	}
	updates := make([]tg.UpdateClass, 0, len(exceptions))
	userIDs := make([]int64, 0)
	channelIDs := make([]int64, 0)
	communityIDs := make([]int64, 0)
	for _, ex := range exceptions {
		if filterPeer != nil && ex.Peer != *filterPeer {
			continue
		}
		if !notifyExceptionQualifies(ex.Settings, compareStories) && !(req.CompareSound && (ex.Settings.Sound != nil || compareStories && ex.Settings.StoriesSound != nil)) {
			continue
		}
		scope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: ex.Peer, TopicID: ex.TopicID}
		s := ex.Settings
		updates = append(updates, &tg.UpdateNotifySettings{
			Peer:           tgNotifyPeer(scope),
			NotifySettings: *tgScopeNotifySettings(scope, &s),
		})
		switch ex.Peer.Type {
		case domain.PeerTypeUser:
			userIDs = append(userIDs, ex.Peer.ID)
		case domain.PeerTypeChannel:
			channelIDs = append(channelIDs, ex.Peer.ID)
		case domain.PeerTypeCommunity:
			communityIDs = append(communityIDs, ex.Peer.ID)
		}
	}
	if len(updates) == 0 {
		return empty, nil
	}
	chats := r.tgChatsForChannelIDs(ctx, userID, channelIDs)
	if r.deps.Communities != nil && len(communityIDs) > 0 {
		if views, err := r.deps.Communities.GetMany(ctx, userID, communityIDs); err == nil {
			chats = appendUniqueTGChats(chats, tgCommunityChats(views)...)
		}
	}
	out := &tg.Updates{
		Updates: updates,
		Users:   r.tgUsersForIDs(ctx, userID, userIDs),
		Chats:   chats,
		Date:    int(r.clock.Now().Unix()),
	}
	r.applyPeerReadModels(ctx, userID, out.Users, out.Chats)
	return out, nil
}

// notifyExceptionQualifies 判定一条异常是否纳入 getNotifyExceptions 结果。
// 声音比较由调用方按 compare_sound 独立处理。
func notifyExceptionQualifies(s domain.PeerNotifySettings, compareStories bool) bool {
	if s.MuteUntil != nil || s.Silent != nil || s.ShowPreviews != nil {
		return true
	}
	if compareStories && (s.StoriesMuted != nil || s.StoriesHideSender != nil) {
		return true
	}
	return false
}

// tgChatsForChannelIDs 把一组 channel id 解析为 tg.Chat（无权限/已失效者跳过）。
// perf：一次批量 GetChannels，而非逐个 GetChannel（N+1）。
func (r *Router) tgChatsForChannelIDs(ctx context.Context, viewerUserID int64, channelIDs []int64) []tg.ChatClass {
	out := make([]tg.ChatClass, 0, len(channelIDs))
	if r.deps.Channels == nil || len(channelIDs) == 0 {
		return out
	}
	views, err := r.deps.Channels.GetChannels(ctx, viewerUserID, channelIDs)
	if err != nil {
		return out
	}
	for _, view := range views {
		out = append(out, tgChannelChatForView(viewerUserID, view))
	}
	return out
}

// tgPeerNotifySettings 把存储的 per-peer 设置叠加在默认通知设置上：未设置的字段
// 保留默认（声音=default、show_previews=true 等），已设置字段覆盖。nil=纯默认，
// 与历史 tdesktop.NotifySettings() 行为一致。
func tgPeerNotifySettings(s *domain.PeerNotifySettings) *tg.PeerNotifySettings {
	return applyStoredNotifySettings(tdesktop.NotifySettings(), s)
}

func tgScopeNotifySettings(scope domain.NotifyScope, s *domain.PeerNotifySettings) *tg.PeerNotifySettings {
	if scope.TopicID != 0 {
		return applyStoredNotifySettings(&tg.PeerNotifySettings{}, s)
	}
	return tgPeerNotifySettings(s)
}

func applyStoredNotifySettings(out *tg.PeerNotifySettings, s *domain.PeerNotifySettings) *tg.PeerNotifySettings {
	if s == nil {
		return out
	}
	if s.ShowPreviews != nil {
		out.SetShowPreviews(*s.ShowPreviews)
	}
	if s.Silent != nil {
		out.SetSilent(*s.Silent)
	}
	if s.MuteUntil != nil {
		out.SetMuteUntil(*s.MuteUntil)
	}
	if s.StoriesMuted != nil {
		out.SetStoriesMuted(*s.StoriesMuted)
	}
	if s.StoriesHideSender != nil {
		out.SetStoriesHideSender(*s.StoriesHideSender)
	}
	if s.Sound != nil {
		out.SetIosSound(tgNotificationSound(*s.Sound))
		out.SetAndroidSound(tgNotificationSound(*s.Sound))
		out.SetOtherSound(tgNotificationSound(*s.Sound))
	}
	if s.StoriesSound != nil {
		out.SetStoriesIosSound(tgNotificationSound(*s.StoriesSound))
		out.SetStoriesAndroidSound(tgNotificationSound(*s.StoriesSound))
		out.SetStoriesOtherSound(tgNotificationSound(*s.StoriesSound))
	}

	return out
}

func domainPeerNotifySettings(in tg.InputPeerNotifySettings) domain.PeerNotifySettings {
	out := domain.PeerNotifySettings{}
	if v, ok := in.GetShowPreviews(); ok {
		out.ShowPreviews = &v
	}
	if v, ok := in.GetSilent(); ok {
		out.Silent = &v
	}
	if v, ok := in.GetMuteUntil(); ok {
		out.MuteUntil = &v
	}
	if v, ok := in.GetStoriesMuted(); ok {
		out.StoriesMuted = &v
	}
	if v, ok := in.GetStoriesHideSender(); ok {
		out.StoriesHideSender = &v
	}
	return out
}

func tgNotifyPeer(scope domain.NotifyScope) tg.NotifyPeerClass {
	switch scope.Kind {
	case domain.NotifyScopeUsers:
		return &tg.NotifyUsers{}
	case domain.NotifyScopeChats:
		return &tg.NotifyChats{}
	case domain.NotifyScopeBroadcasts:
		return &tg.NotifyBroadcasts{}
	case domain.NotifyScopePeer:
		if scope.Peer.Type == domain.PeerTypeCommunity {
			return &tg.NotifyCommunity{CommunityID: scope.Peer.ID}
		}
		peer := tgPeer(scope.Peer)
		if scope.TopicID != 0 {
			return &tg.NotifyForumTopic{Peer: peer, TopMsgID: scope.TopicID}
		}
		return &tg.NotifyPeer{Peer: peer}
	default:
		return &tg.NotifyUsers{}
	}
}

// withDialogNotifySettings 装配 dialog 列表的 per-peer 通知设置，让静音状态在列表正确
// 显示且跨重启恢复。perf：从 per-user notify 缓存读取（命中即 0 PG），而非每次 getDialogs
// 都查 notify_settings——绝大多数用户没有任何自定义静音，缓存命中后零数据库开销。
func (r *Router) withDialogNotifySettings(ctx context.Context, viewerUserID int64, list domain.DialogList) domain.DialogList {
	if len(list.Dialogs) == 0 && len(list.Communities) == 0 {
		return list
	}
	settings := r.userNotifySettings(ctx, viewerUserID)
	if len(settings) == 0 {
		return list
	}
	for i := range list.Dialogs {
		if s, ok := settings[list.Dialogs[i].Peer]; ok {
			sc := s.Clone()
			list.Dialogs[i].NotifySettings = &sc
		}
	}
	for i := range list.Communities {
		peer := domain.Peer{Type: domain.PeerTypeCommunity, ID: list.Communities[i].Community.ID}
		if s, ok := settings[peer]; ok {
			sc := s.Clone()
			list.Communities[i].State.NotifySettings = &sc
		}
	}
	return list
}

// applyNotifySettingsToUserFull 在缓存后 overlay userFull 的 notify_settings（避免
// 投影缓存使 notify 状态陈旧）。perf：从 per-user notify 缓存读取，不再每次 getFullUser
// 单 peer 查 PG。
func (r *Router) applyNotifySettingsToUserFull(ctx context.Context, viewerUserID, ownerUserID int64, full *tg.UserFull) {
	settings := r.userNotifySettings(ctx, viewerUserID)
	s, ok := settings[domain.Peer{Type: domain.PeerTypeUser, ID: ownerUserID}]
	if !ok || s.IsZero() {
		return
	}
	full.NotifySettings = *tgPeerNotifySettings(&s)
}

// applyNotifySettingsToChannelFull 在缓存后 overlay channelFull 的 notify_settings。
func (r *Router) applyNotifySettingsToChannelFull(ctx context.Context, viewerUserID, channelID int64, full *tg.ChannelFull) {
	settings := r.userNotifySettings(ctx, viewerUserID)
	s, ok := settings[domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}]
	if !ok || s.IsZero() {
		return
	}
	full.NotifySettings = *tgPeerNotifySettings(&s)
}
