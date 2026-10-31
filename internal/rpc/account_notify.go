package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// accountNotifySettingsService is the optional per-scope notification-settings persistence
// extension, implemented by *app/account.Service. When not wired, handlers fall back to the
// historical default (tdesktop.NotifySettings).
type accountNotifySettingsService interface {
	GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, error)
	SaveNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, settings domain.PeerNotifySettings) error
	ResetNotifySettings(ctx context.Context, ownerUserID int64) error
	PeerNotifySettings(ctx context.Context, ownerUserID int64, peers []domain.Peer) (map[domain.Peer]domain.PeerNotifySettings, error)
	AllPeerNotifySettings(ctx context.Context, ownerUserID int64) (map[domain.Peer]domain.PeerNotifySettings, error)
	ListNotifyExceptions(ctx context.Context, ownerUserID int64) ([]domain.NotifyException, error)
}

func (r *Router) accountNotifySvc() (accountNotifySettingsService, bool) {
	svc, ok := r.deps.Account.(accountNotifySettingsService)
	return svc, ok
}

// notifyScopeFromInput resolves an InputNotifyPeer into a business scope. For peer/forumTopic a
// domain.Peer must be resolved (access is not checked -- notification preferences are the
// requester's own settings for a peer).
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
	if svc, ok := r.accountNotifySvc(); ok {
		if err := svc.SaveNotifySettings(ctx, userID, scope, settings); err != nil {
			return false, internalErr()
		}
		r.notifySettings.Delete(userID)
	}
	// Push updateNotifySettings to the caller's other online devices (multi-device mute sync).
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNotifySettings{
			Peer:           tgNotifyPeer(scope),
			NotifySettings: *tgScopeNotifySettings(scope, &settings),
		}},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{},
		Date:  int(r.clock.Now().Unix()),
	})
	return true, nil
}

func (r *Router) onAccountResetNotifySettings(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if svc, ok := r.accountNotifySvc(); ok {
		if err := svc.ResetNotifySettings(ctx, userID); err != nil {
			return false, internalErr()
		}
		r.notifySettings.Delete(userID)
	}
	return true, nil
}

// onAccountGetNotifyExceptions returns a global index of "sessions with custom notification
// settings": one updateNotifySettings per exception, with the referenced peer's Users/Chats.
// By default (no compare flag) only message-level exceptions are returned (mute/silent/
// show_previews); compare_stories additionally includes story-only exceptions.
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
		if !notifyExceptionQualifies(ex.Settings, compareStories) {
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

// notifyExceptionQualifies decides whether an exception is included in the getNotifyExceptions result.
// Custom ringtones are not modeled, so compare_sound has no additional effect.
func notifyExceptionQualifies(s domain.PeerNotifySettings, compareStories bool) bool {
	if s.MuteUntil != nil || s.Silent != nil || s.ShowPreviews != nil {
		return true
	}
	if compareStories && (s.StoriesMuted != nil || s.StoriesHideSender != nil) {
		return true
	}
	return false
}

// tgChatsForChannelIDs resolves a set of channel ids into tg.Chat (skipping ones without access/expired).
// perf: one batched GetChannels instead of GetChannel per id (N+1).
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

// tgPeerNotifySettings overlays the stored per-peer settings on the default notification settings:
// unset fields keep the default (sound=default, show_previews=true, etc.), set fields override.
// nil = purely default, consistent with the historical tdesktop.NotifySettings() behavior.
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

// withDialogNotifySettings populates the per-peer notification settings of a dialog list so mute
// state displays correctly in the list and survives restarts. perf: reads from the per-user notify
// cache (a hit costs 0 PG) instead of querying notify_settings on every getDialogs -- the vast
// majority of users have no custom mutes, so a cache hit means zero database cost.
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

// applyNotifySettingsToUserFull overlays userFull's notify_settings after caching (to avoid the
// projection cache making notify state stale). perf: reads from the per-user notify cache instead
// of querying PG for a single peer on every getFullUser.
func (r *Router) applyNotifySettingsToUserFull(ctx context.Context, viewerUserID, ownerUserID int64, full *tg.UserFull) {
	settings := r.userNotifySettings(ctx, viewerUserID)
	s, ok := settings[domain.Peer{Type: domain.PeerTypeUser, ID: ownerUserID}]
	if !ok || s.IsZero() {
		return
	}
	full.NotifySettings = *tgPeerNotifySettings(&s)
}

// applyNotifySettingsToChannelFull overlays channelFull's notify_settings after caching.
func (r *Router) applyNotifySettingsToChannelFull(ctx context.Context, viewerUserID, channelID int64, full *tg.ChannelFull) {
	settings := r.userNotifySettings(ctx, viewerUserID)
	s, ok := settings[domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}]
	if !ok || s.IsZero() {
		return
	}
	full.NotifySettings = *tgPeerNotifySettings(&s)
}
