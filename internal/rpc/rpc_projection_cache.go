package rpc

import (
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

const (
	rpcProjectionCacheTTL        = 30 * time.Minute
	rpcProjectionCacheMaxEntries = 100000
)

// userFullProjectionCache / peerSettingsProjectionCache / channelFullProjectionCache
// are three structurally identical per-viewer TTL projection caches, all converged
// onto projectionCache[K,V] (epoch guard / LRU single-entry eviction / TTL / clone
// are carried by the readmodelcache primitive). Each differs only in key/value
// type, clone and invalidation dimension.

type userFullProjectionKey struct {
	viewerUserID int64
	targetUserID int64
}

type userFullProjectionCache struct {
	*projectionCache[userFullProjectionKey, tg.UserFull]
}

func newUserFullProjectionCache(clock func() time.Time) *userFullProjectionCache {
	return &userFullProjectionCache{
		newProjectionCache[userFullProjectionKey, tg.UserFull](rpcProjectionCacheMaxEntries, rpcProjectionCacheTTL, clock, cloneUserFull),
	}
}

func (c *userFullProjectionCache) Lookup(viewerUserID, targetUserID int64) (tg.UserFull, bool) {
	if c == nil || viewerUserID == 0 || targetUserID == 0 {
		return tg.UserFull{}, false
	}
	return c.lookup(userFullProjectionKey{viewerUserID: viewerUserID, targetUserID: targetUserID})
}

// StoreIfEpoch writes only when the epoch is unchanged (no invalidation during the
// build).
func (c *userFullProjectionCache) StoreIfEpoch(viewerUserID, targetUserID int64, full tg.UserFull, loadEpoch uint64) {
	if c == nil || viewerUserID == 0 || targetUserID == 0 {
		return
	}
	c.storeIfEpoch(userFullProjectionKey{viewerUserID: viewerUserID, targetUserID: targetUserID}, full, loadEpoch)
}

func (c *userFullProjectionCache) DeleteViewer(viewerUserID int64) {
	if c == nil || viewerUserID == 0 {
		return
	}
	c.deleteWhere(func(k userFullProjectionKey) bool { return k.viewerUserID == viewerUserID })
}

func (c *userFullProjectionCache) DeleteTarget(targetUserID int64) {
	if c == nil || targetUserID == 0 {
		return
	}
	c.deleteWhere(func(k userFullProjectionKey) bool { return k.targetUserID == targetUserID })
}

func (c *userFullProjectionCache) DeletePair(viewerUserID, targetUserID int64) {
	if c == nil || viewerUserID == 0 || targetUserID == 0 {
		return
	}
	c.deleteKey(userFullProjectionKey{viewerUserID: viewerUserID, targetUserID: targetUserID})
}

type peerSettingsProjectionKey struct {
	viewerUserID int64
	peer         domain.Peer
}

type peerSettingsProjectionCache struct {
	// domain.PeerSettings is a flat value (no shared mutable fields), so Clone=nil.
	*projectionCache[peerSettingsProjectionKey, domain.PeerSettings]
}

func newPeerSettingsProjectionCache(clock func() time.Time) *peerSettingsProjectionCache {
	return &peerSettingsProjectionCache{
		newProjectionCache[peerSettingsProjectionKey, domain.PeerSettings](rpcProjectionCacheMaxEntries, rpcProjectionCacheTTL, clock, nil),
	}
}

func (c *peerSettingsProjectionCache) Lookup(viewerUserID int64, peer domain.Peer) (domain.PeerSettings, bool) {
	if c == nil || viewerUserID == 0 || peer.ID == 0 {
		return domain.PeerSettings{}, false
	}
	return c.lookup(peerSettingsProjectionKey{viewerUserID: viewerUserID, peer: peer})
}

// StoreIfEpoch writes only when the epoch is unchanged (no invalidation during the
// build).
func (c *peerSettingsProjectionCache) StoreIfEpoch(viewerUserID int64, peer domain.Peer, settings domain.PeerSettings, loadEpoch uint64) {
	if c == nil || viewerUserID == 0 || peer.ID == 0 {
		return
	}
	c.storeIfEpoch(peerSettingsProjectionKey{viewerUserID: viewerUserID, peer: peer}, settings, loadEpoch)
}

func (c *peerSettingsProjectionCache) DeleteViewer(viewerUserID int64) {
	if c == nil || viewerUserID == 0 {
		return
	}
	c.deleteWhere(func(k peerSettingsProjectionKey) bool { return k.viewerUserID == viewerUserID })
}

func (c *peerSettingsProjectionCache) DeletePeer(peer domain.Peer) {
	if c == nil || peer.ID == 0 {
		return
	}
	c.deleteWhere(func(k peerSettingsProjectionKey) bool { return k.peer == peer })
}

func (c *peerSettingsProjectionCache) DeletePair(viewerUserID int64, peer domain.Peer) {
	if c == nil || viewerUserID == 0 || peer.ID == 0 {
		return
	}
	c.deleteKey(peerSettingsProjectionKey{viewerUserID: viewerUserID, peer: peer})
}

type channelFullProjectionKey struct {
	viewerUserID int64
	channelID    int64
}

type channelFullProjection struct {
	defaultSendAsChannelID int64
	accessHash             int64
	canChangeInfo          bool
	full                   tg.ChannelFull
	chats                  []tg.ChatClass
	userIDs                []int64
}

type channelFullProjectionCache struct {
	*projectionCache[channelFullProjectionKey, channelFullProjection]
}

func newChannelFullProjectionCache(clock func() time.Time) *channelFullProjectionCache {
	return &channelFullProjectionCache{
		newProjectionCache[channelFullProjectionKey, channelFullProjection](rpcProjectionCacheMaxEntries, rpcProjectionCacheTTL, clock, cloneChannelFullProjection),
	}
}

func (c *channelFullProjectionCache) Lookup(viewerUserID, channelID int64) (channelFullProjection, bool) {
	if c == nil || viewerUserID == 0 || channelID == 0 {
		return channelFullProjection{}, false
	}
	return c.lookup(channelFullProjectionKey{viewerUserID: viewerUserID, channelID: channelID})
}

// StoreIfEpoch writes only when the epoch is unchanged (no invalidation during the
// build).
func (c *channelFullProjectionCache) StoreIfEpoch(viewerUserID, channelID int64, value channelFullProjection, loadEpoch uint64) {
	if c == nil || viewerUserID == 0 || channelID == 0 {
		return
	}
	c.storeIfEpoch(channelFullProjectionKey{viewerUserID: viewerUserID, channelID: channelID}, value, loadEpoch)
}

func (c *channelFullProjectionCache) DeleteViewer(viewerUserID int64) {
	if c == nil || viewerUserID == 0 {
		return
	}
	c.deleteWhere(func(k channelFullProjectionKey) bool { return k.viewerUserID == viewerUserID })
}

func (c *channelFullProjectionCache) DeleteChannel(channelID int64) {
	if c == nil || channelID == 0 {
		return
	}
	c.deleteWhere(func(k channelFullProjectionKey) bool { return k.channelID == channelID })
}

func (c *channelFullProjectionCache) DeletePair(viewerUserID, channelID int64) {
	if c == nil || viewerUserID == 0 || channelID == 0 {
		return
	}
	c.deleteKey(channelFullProjectionKey{viewerUserID: viewerUserID, channelID: channelID})
}

func cloneChannelFullProjection(in channelFullProjection) channelFullProjection {
	return channelFullProjection{
		defaultSendAsChannelID: in.defaultSendAsChannelID,
		accessHash:             in.accessHash,
		canChangeInfo:          in.canChangeInfo,
		full:                   cloneChannelFull(in.full),
		chats:                  cloneChatClasses(in.chats),
		userIDs:                cloneInt64s(in.userIDs),
	}
}

func cloneUserFull(in tg.UserFull) tg.UserFull {
	out := in
	out.BotInfo = cloneBotInfo(in.BotInfo)
	return out
}

func cloneChannelFull(in tg.ChannelFull) tg.ChannelFull {
	out := in
	out.BotInfo = cloneBotInfos(in.BotInfo)
	out.PendingSuggestions = append([]string(nil), in.PendingSuggestions...)
	out.RecentRequesters = cloneInt64s(in.RecentRequesters)
	out.ExportedInvite = cloneExportedChatInvite(in.ExportedInvite)
	return out
}

func cloneExportedChatInvite(in tg.ExportedChatInviteClass) tg.ExportedChatInviteClass {
	switch v := in.(type) {
	case *tg.ChatInviteExported:
		out := *v
		return &out
	default:
		return v
	}
}

func cloneBotInfo(in tg.BotInfo) tg.BotInfo {
	out := in
	out.Commands = append([]tg.BotCommand(nil), in.Commands...)
	return out
}

func cloneChatClass(in tg.ChatClass) tg.ChatClass {
	switch v := in.(type) {
	case *tg.Channel:
		out := *v
		out.RestrictionReason = append([]tg.RestrictionReason(nil), v.RestrictionReason...)
		out.Usernames = append([]tg.Username(nil), v.Usernames...)
		return &out
	case *tg.Chat:
		out := *v
		return &out
	case *tg.ChannelForbidden:
		out := *v
		return &out
	case *tg.ChatForbidden:
		out := *v
		return &out
	case *tg.ChatEmpty:
		out := *v
		return &out
	default:
		return in
	}
}

func cloneChatClasses(in []tg.ChatClass) []tg.ChatClass {
	if len(in) == 0 {
		return nil
	}
	out := make([]tg.ChatClass, 0, len(in))
	for _, chat := range in {
		out = append(out, cloneChatClass(chat))
	}
	return out
}

func (r *Router) invalidateRPCProjectionForViewer(viewerUserID int64) {
	if r.userFullProjectionCache != nil {
		r.userFullProjectionCache.DeleteViewer(viewerUserID)
	}
	if r.peerSettingsProjectionCache != nil {
		r.peerSettingsProjectionCache.DeleteViewer(viewerUserID)
	}
	if r.channelFullProjectionCache != nil {
		r.channelFullProjectionCache.DeleteViewer(viewerUserID)
	}
}

func (r *Router) invalidateRPCProjectionForUser(userID int64) {
	if r.userFullProjectionCache != nil {
		r.userFullProjectionCache.DeleteTarget(userID)
		r.userFullProjectionCache.DeleteViewer(userID)
	}
	if r.peerSettingsProjectionCache != nil {
		r.peerSettingsProjectionCache.DeleteViewer(userID)
		r.peerSettingsProjectionCache.DeletePeer(domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	}
}

func (r *Router) invalidateRPCProjectionForPeer(ownerUserID int64, peer domain.Peer) {
	if r.userFullProjectionCache != nil && peer.Type == domain.PeerTypeUser {
		r.userFullProjectionCache.DeletePair(ownerUserID, peer.ID)
	}
	if r.peerSettingsProjectionCache != nil {
		r.peerSettingsProjectionCache.DeletePair(ownerUserID, peer)
	}
	if r.channelFullProjectionCache != nil && peer.Type == domain.PeerTypeChannel {
		r.channelFullProjectionCache.DeletePair(ownerUserID, peer.ID)
	}
}

func (r *Router) invalidateRPCProjectionForChannel(channelID int64) {
	if r.channelFullProjectionCache != nil {
		r.channelFullProjectionCache.DeleteChannel(channelID)
	}
	if r.peerSettingsProjectionCache != nil {
		r.peerSettingsProjectionCache.DeletePeer(domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	}
}

func (r *Router) flushRPCProjectionCache() {
	if r.userFullProjectionCache != nil {
		r.userFullProjectionCache.Clear()
	}
	if r.peerSettingsProjectionCache != nil {
		r.peerSettingsProjectionCache.Clear()
	}
	if r.channelFullProjectionCache != nil {
		r.channelFullProjectionCache.Clear()
	}
}

func (r *Router) InvalidateRPCProjectionReadModelForViewer(viewerUserID int64) {
	r.invalidateRPCProjectionForViewer(viewerUserID)
}

func (r *Router) InvalidateRPCProjectionReadModelForUser(userID int64) {
	r.invalidateRPCProjectionForUser(userID)
}

func (r *Router) InvalidateRPCProjectionReadModelForPeer(ownerUserID int64, peer domain.Peer) {
	r.invalidateRPCProjectionForPeer(ownerUserID, peer)
}

func (r *Router) InvalidateRPCProjectionReadModelForChannel(channelID int64) {
	r.invalidateRPCProjectionForChannel(channelID)
}

func (r *Router) FlushRPCProjectionReadModel() {
	r.flushRPCProjectionCache()
}
