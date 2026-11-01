package rpc

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

const storyPinnedStoriesCacheTTL = 30 * time.Minute

type storyPinnedStoriesCacheKey struct {
	viewerUserID int64
	peer         domain.Peer
	offsetID     int
	limit        int
}

// storyPinnedStoriesCache caches a viewer's paginated pinned-stories result for a
// peer, carried by the shared cache primitive. The key includes the (offsetID,limit)
// pagination dimension - LRU single-entry eviction finally gives this page-key an
// upper bound, eliminating its previous unbounded growth.
type storyPinnedStoriesCache struct {
	cache *readmodelcache.Cache[storyPinnedStoriesCacheKey, domain.StoryList]
}

func newStoryPinnedStoriesCache(now func() time.Time) *storyPinnedStoriesCache {
	return &storyPinnedStoriesCache{
		cache: readmodelcache.New[storyPinnedStoriesCacheKey, domain.StoryList](readmodelcache.Config[storyPinnedStoriesCacheKey, domain.StoryList]{
			MaxEntries: storyProjectionCacheMaxEntries,
			TTL:        storyPinnedStoriesCacheTTL,
			Now:        now,
			Clone:      cloneStoryListForCache,
		}),
	}
}

func storyPinnedStoriesKey(viewerUserID int64, peer domain.Peer, offsetID, limit int) storyPinnedStoriesCacheKey {
	if offsetID < 0 {
		offsetID = 0
	}
	if limit <= 0 || limit > domain.MaxStoryListLimit {
		limit = domain.MaxStoryListLimit
	}
	return storyPinnedStoriesCacheKey{
		viewerUserID: viewerUserID,
		peer:         peer,
		offsetID:     offsetID,
		limit:        limit,
	}
}

func (c *storyPinnedStoriesCache) getOrLoad(ctx context.Context, key storyPinnedStoriesCacheKey, load func() (domain.StoryList, error)) (domain.StoryList, error) {
	if c == nil || key.viewerUserID == 0 || key.peer.ID == 0 {
		return load()
	}
	return c.cache.GetOrLoad(ctx, key, load)
}

func (c *storyPinnedStoriesCache) Delete(viewerUserID int64, peer domain.Peer) {
	if c == nil || viewerUserID == 0 || peer.ID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(k storyPinnedStoriesCacheKey) bool {
		return k.viewerUserID == viewerUserID && k.peer == peer
	})
}

func (c *storyPinnedStoriesCache) DeleteViewer(viewerUserID int64) {
	if c == nil || viewerUserID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(k storyPinnedStoriesCacheKey) bool { return k.viewerUserID == viewerUserID })
}

func (c *storyPinnedStoriesCache) DeletePeer(peer domain.Peer) {
	if c == nil || peer.ID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(k storyPinnedStoriesCacheKey) bool { return k.peer == peer })
}

func (c *storyPinnedStoriesCache) Flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

func cloneStoryListForCache(in domain.StoryList) domain.StoryList {
	in.Stories = cloneStoriesForCache(in.Stories)
	in.PinnedToTop = append([]int(nil), in.PinnedToTop...)
	in.Peers = clonePeerStoriesForCache(in.Peers)
	in.Users = append([]domain.User(nil), in.Users...)
	in.Channels = append([]domain.Channel(nil), in.Channels...)
	return in
}

func clonePeerStoriesForCache(in []domain.PeerStories) []domain.PeerStories {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.PeerStories, len(in))
	for i, item := range in {
		out[i] = item
		out[i].Stories = cloneStoriesForCache(item.Stories)
		out[i].Users = append([]domain.User(nil), item.Users...)
		out[i].Channels = append([]domain.Channel(nil), item.Channels...)
	}
	return out
}

func cloneStoriesForCache(in []domain.Story) []domain.Story {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.Story, len(in))
	for i, story := range in {
		out[i] = cloneStoryForCache(story)
	}
	return out
}

func cloneStoryForCache(story domain.Story) domain.Story {
	story.PrivacyRules = clonePrivacyRulesForCache(story.PrivacyRules)
	story.AllowUserIDs = append([]int64(nil), story.AllowUserIDs...)
	story.DisallowUserIDs = append([]int64(nil), story.DisallowUserIDs...)
	story.Entities = append([]domain.MessageEntity(nil), story.Entities...)
	story.MediaAreas = cloneStoryMediaAreasForCache(story.MediaAreas)
	if story.Forward != nil {
		forward := *story.Forward
		story.Forward = &forward
	}
	story.Views.Reactions = append([]domain.ChannelMessageReactionCount(nil), story.Views.Reactions...)
	story.Views.RecentViewers = append([]int64(nil), story.Views.RecentViewers...)
	if story.SentReaction != nil {
		reaction := *story.SentReaction
		story.SentReaction = &reaction
	}
	return story
}

func clonePrivacyRulesForCache(in []domain.PrivacyRule) []domain.PrivacyRule {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.PrivacyRule, len(in))
	for i, rule := range in {
		out[i] = rule
		out[i].UserIDs = append([]int64(nil), rule.UserIDs...)
		out[i].ChatIDs = append([]int64(nil), rule.ChatIDs...)
	}
	return out
}

func cloneStoryMediaAreasForCache(in []domain.StoryMediaArea) []domain.StoryMediaArea {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.StoryMediaArea, len(in))
	for i, area := range in {
		out[i] = area
		if area.Reaction != nil {
			reaction := *area.Reaction
			out[i].Reaction = &reaction
		}
		if area.Geo != nil {
			geo := *area.Geo
			out[i].Geo = &geo
		}
		if area.GeoAddress != nil {
			address := *area.GeoAddress
			out[i].GeoAddress = &address
		}
		if area.Venue != nil {
			venue := *area.Venue
			out[i].Venue = &venue
		}
	}
	return out
}
