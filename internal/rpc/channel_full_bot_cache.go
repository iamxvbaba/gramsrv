package rpc

import (
	"context"
	"fmt"
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

const (
	channelFullBotInfoCacheTTL        = 30 * time.Minute
	channelFullBotInfoCacheMaxEntries = 4096
)

type channelFullBotInfoCacheKey struct {
	viewerUserID int64
	channelID    int64
}

// channelFullBotInfoCache 收敛到 projectionCache[K,V](epoch 守卫 / LRU / TTL / clone 由原语承载)。
// 它走 Router 级 singleflight + 外部构建再写回(LoadEpoch→loadChannelFullBotInfo→StoreIfEpoch)。
type channelFullBotInfoCache struct {
	*projectionCache[channelFullBotInfoCacheKey, channelFullBotInfoResult]
}

type channelFullBotInfoResult struct {
	userIDs  []int64
	botInfos []tg.BotInfo
}

func newChannelFullBotInfoCache(clock func() time.Time) *channelFullBotInfoCache {
	return &channelFullBotInfoCache{
		newProjectionCache[channelFullBotInfoCacheKey, channelFullBotInfoResult](channelFullBotInfoCacheMaxEntries, channelFullBotInfoCacheTTL, clock, cloneChannelFullBotInfoResult),
	}
}

func cloneChannelFullBotInfoResult(in channelFullBotInfoResult) channelFullBotInfoResult {
	return channelFullBotInfoResult{
		userIDs:  cloneInt64s(in.userIDs),
		botInfos: cloneBotInfos(in.botInfos),
	}
}

func channelFullBotInfoSingleflightKey(viewerUserID, channelID int64) string {
	return fmt.Sprintf("%d:%d", viewerUserID, channelID)
}

func (c *channelFullBotInfoCache) Lookup(viewerUserID, channelID int64) (channelFullBotInfoResult, bool) {
	if c == nil || viewerUserID == 0 || channelID == 0 {
		return channelFullBotInfoResult{}, false
	}
	return c.lookup(channelFullBotInfoCacheKey{viewerUserID: viewerUserID, channelID: channelID})
}

// StoreIfEpoch 仅在 epoch 未变(构建期间没有失效)时写入。
func (c *channelFullBotInfoCache) StoreIfEpoch(viewerUserID, channelID int64, value channelFullBotInfoResult, loadEpoch uint64) {
	if c == nil || viewerUserID == 0 || channelID == 0 {
		return
	}
	c.storeIfEpoch(channelFullBotInfoCacheKey{viewerUserID: viewerUserID, channelID: channelID}, value, loadEpoch)
}

func (c *channelFullBotInfoCache) DeleteChannel(channelID int64) {
	if c == nil || channelID == 0 {
		return
	}
	c.deleteWhere(func(k channelFullBotInfoCacheKey) bool { return k.channelID == channelID })
}

func (r *Router) channelFullBotInfo(ctx context.Context, viewerUserID, channelID int64) channelFullBotInfoResult {
	if r.deps.Channels == nil || viewerUserID == 0 || channelID == 0 {
		return channelFullBotInfoResult{}
	}
	if cached, ok := r.channelFullBotCache.Lookup(viewerUserID, channelID); ok {
		return cached
	}
	key := channelFullBotInfoSingleflightKey(viewerUserID, channelID)
	value, _, _ := r.channelFullBotSF.Do(key, func() (any, error) {
		if cached, ok := r.channelFullBotCache.Lookup(viewerUserID, channelID); ok {
			return cached, nil
		}
		loadEpoch := r.channelFullBotCache.LoadEpoch()
		result := r.loadChannelFullBotInfo(ctx, viewerUserID, channelID)
		r.channelFullBotCache.StoreIfEpoch(viewerUserID, channelID, result, loadEpoch)
		return result, nil
	})
	if result, ok := value.(channelFullBotInfoResult); ok {
		return result
	}
	return channelFullBotInfoResult{}
}

func (r *Router) loadChannelFullBotInfo(ctx context.Context, viewerUserID, channelID int64) channelFullBotInfoResult {
	list, err := r.deps.Channels.GetParticipants(ctx, viewerUserID, channelID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsBots}, 0, domain.MaxChannelParticipantsLimit)
	if err != nil {
		return channelFullBotInfoResult{}
	}
	userIDs := make([]int64, 0, len(list.Participants))
	seen := make(map[int64]struct{}, len(list.Participants))
	for _, member := range list.Participants {
		if member.UserID == 0 {
			continue
		}
		if _, ok := seen[member.UserID]; ok {
			continue
		}
		seen[member.UserID] = struct{}{}
		userIDs = append(userIDs, member.UserID)
	}
	return channelFullBotInfoResult{
		userIDs:  userIDs,
		botInfos: r.tgBotInfos(ctx, userIDs),
	}
}

func (r *Router) invalidateChannelFullBotInfoCacheForChannel(channelID int64) {
	if r.channelFullBotCache != nil {
		r.channelFullBotCache.DeleteChannel(channelID)
	}
	r.invalidateRPCProjectionForChannel(channelID)
}

func (r *Router) invalidateChannelFullBotInfoCache() {
	if r.channelFullBotCache != nil {
		r.channelFullBotCache.Clear()
	}
	if r.channelFullProjectionCache != nil {
		r.channelFullProjectionCache.Clear()
	}
}

func (r *Router) InvalidateChannelFullBotInfoReadModel(channelID int64) {
	r.invalidateChannelFullBotInfoCacheForChannel(channelID)
}

func (r *Router) FlushChannelFullBotInfoReadModel() {
	r.invalidateChannelFullBotInfoCache()
}

func cloneInt64s(in []int64) []int64 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int64, len(in))
	copy(out, in)
	return out
}

func cloneBotInfos(in []tg.BotInfo) []tg.BotInfo {
	if len(in) == 0 {
		return nil
	}
	out := make([]tg.BotInfo, len(in))
	copy(out, in)
	for i := range out {
		out[i].Commands = append([]tg.BotCommand(nil), out[i].Commands...)
	}
	return out
}
