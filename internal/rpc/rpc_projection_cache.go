package rpc

import (
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

const (
	rpcProjectionCacheTTL        = 30 * time.Minute
	rpcProjectionCacheMaxEntries = 100000
	// Receiver projection is keyed by (viewer,target), so a 1000-account load
	// can legitimately retain more pairs than the heavier RPC object caches.
	// This is a reservation ceiling only; entries are allocated on demand and
	// evicted by the shared bounded LRU/TTL primitive.
	receiverUserProjectionCacheMaxEntries = 200000
)

// userFullProjectionCache / peerSettingsProjectionCache / channelFullProjectionCache 是三个
// 结构同形的 per-viewer TTL 投影缓存,统一收敛到 projectionCache[K,V](epoch 守卫 / LRU 单条
// 驱逐 / TTL / clone 由 readmodelcache 原语承载)。各自仅在键/值类型、clone、失效维度上不同。

type userFullProjectionKey struct {
	viewerUserID int64
	targetUserID int64
}

// receiverUserProjectionCache stores the stable, viewer-specific domain.User
// projection used by Durable Egress. It deliberately sits before the runtime
// presence overlay: online/offline status is recomputed for every TL build.
// Values are deep-cloned because domain.User contains slice-backed fields.
type receiverUserProjectionCache struct {
	*projectionCache[userFullProjectionKey, domain.User]
}

func newReceiverUserProjectionCache(clock func() time.Time) *receiverUserProjectionCache {
	return &receiverUserProjectionCache{
		newProjectionCache[userFullProjectionKey, domain.User](receiverUserProjectionCacheMaxEntries, rpcProjectionCacheTTL, clock, cloneDomainUser),
	}
}

func (c *receiverUserProjectionCache) Lookup(viewerUserID, targetUserID int64) (domain.User, bool) {
	if c == nil || viewerUserID == 0 || targetUserID == 0 {
		return domain.User{}, false
	}
	return c.lookup(userFullProjectionKey{viewerUserID: viewerUserID, targetUserID: targetUserID})
}

func (c *receiverUserProjectionCache) StoreIfEpoch(viewerUserID, targetUserID int64, user domain.User, loadEpoch uint64) {
	if c == nil || viewerUserID == 0 || targetUserID == 0 || user.ID != targetUserID {
		return
	}
	c.storeIfEpoch(userFullProjectionKey{viewerUserID: viewerUserID, targetUserID: targetUserID}, user, loadEpoch)
}

func (c *receiverUserProjectionCache) DeleteViewer(viewerUserID int64) {
	if c == nil || viewerUserID == 0 {
		return
	}
	c.deleteWhere(func(k userFullProjectionKey) bool { return k.viewerUserID == viewerUserID })
}

func (c *receiverUserProjectionCache) DeleteTarget(targetUserID int64) {
	if c == nil || targetUserID == 0 {
		return
	}
	c.deleteWhere(func(k userFullProjectionKey) bool { return k.targetUserID == targetUserID })
}

func (c *receiverUserProjectionCache) DeletePair(viewerUserID, targetUserID int64) {
	if c == nil || viewerUserID == 0 || targetUserID == 0 {
		return
	}
	c.deleteKey(userFullProjectionKey{viewerUserID: viewerUserID, targetUserID: targetUserID})
}

func cloneDomainUser(in domain.User) domain.User {
	out := in
	out.RestrictionReasons = append([]domain.UserRestrictionReason(nil), in.RestrictionReasons...)
	out.ContactNoteEntities = append([]domain.MessageEntity(nil), in.ContactNoteEntities...)
	out.PhotoStripped = append([]byte(nil), in.PhotoStripped...)
	return out
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

// StoreIfEpoch 仅在 epoch 未变(构建期间没有失效)时写入。
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
	// domain.PeerSettings 为扁平值(无共享可变字段),故 Clone=nil。
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

// StoreIfEpoch 仅在 epoch 未变(构建期间没有失效)时写入。
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

// StoreIfEpoch 仅在 epoch 未变(构建期间没有失效)时写入。
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
	if r.receiverUserProjectionCache != nil {
		r.receiverUserProjectionCache.DeleteViewer(viewerUserID)
	}
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
	if r.receiverUserProjectionCache != nil {
		r.receiverUserProjectionCache.DeleteTarget(userID)
		r.receiverUserProjectionCache.DeleteViewer(userID)
	}
	if r.userFullProjectionCache != nil {
		r.userFullProjectionCache.DeleteTarget(userID)
		r.userFullProjectionCache.DeleteViewer(userID)
	}
	if r.peerSettingsProjectionCache != nil {
		r.peerSettingsProjectionCache.DeleteViewer(userID)
		r.peerSettingsProjectionCache.DeletePeer(domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	}
}

// InvalidateStarGiftProfiles is the Core background-worker hook for hosted
// NFT attach/detach. These changes have no wire PTS constructor, but cached
// userFull.stargifts_count must still converge immediately in this process.
func (r *Router) InvalidateStarGiftProfiles(peers ...domain.Peer) {
	seen := make(map[int64]struct{}, len(peers))
	for _, peer := range peers {
		if peer.Type != domain.PeerTypeUser || peer.ID <= 0 {
			continue
		}
		if _, ok := seen[peer.ID]; ok {
			continue
		}
		seen[peer.ID] = struct{}{}
		r.invalidateRPCProjectionForUser(peer.ID)
	}
}

func (r *Router) invalidateRPCProjectionForPeer(ownerUserID int64, peer domain.Peer) {
	if r.receiverUserProjectionCache != nil && peer.Type == domain.PeerTypeUser {
		r.receiverUserProjectionCache.DeletePair(ownerUserID, peer.ID)
	}
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
	if r.receiverUserProjectionCache != nil {
		r.receiverUserProjectionCache.Clear()
	}
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

// InvalidateRPCProjectionReadModelForContactOwner invalidates both dimensions
// affected by one owner's aggregate contact snapshot. The inverse owner->viewer
// contact edge participates in privacy projection, while the read-model event
// intentionally does not enumerate the changed peer.
func (r *Router) InvalidateRPCProjectionReadModelForContactOwner(ownerUserID int64) {
	r.invalidateRPCProjectionForViewer(ownerUserID)
	if r.receiverUserProjectionCache != nil {
		r.receiverUserProjectionCache.DeleteTarget(ownerUserID)
	}
	if r.userFullProjectionCache != nil {
		r.userFullProjectionCache.DeleteTarget(ownerUserID)
	}
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
