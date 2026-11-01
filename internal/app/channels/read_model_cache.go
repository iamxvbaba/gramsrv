package channels

import (
	"context"
	"time"

	"telesrv/internal/app/readmodel"
	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
	"telesrv/internal/store"
)

const (
	// Full channel views are version-token guarded by channel_base,
	// channel_member, and dialog_light. Keep the snapshot long-lived; write-side
	// read-model bumps, not time, drive correctness.
	defaultChannelViewReadModelTTL = 24 * time.Hour
	channelViewReadModelMaxEntries = 8192
)

type channelViewCacheKey struct {
	userID    int64
	channelID int64
}

// channelViewReadModelCache and channelResolveReadModelCache both cache
// domain.ChannelView, backed by the shared cache primitive
// (version gate / epoch guard / LRU / clone).
type channelViewReadModelCache struct {
	cache *readmodelcache.Cache[channelViewCacheKey, domain.ChannelView]
}

func newChannelViewReadModelCache(ttl time.Duration) *channelViewReadModelCache {
	if ttl <= 0 {
		ttl = defaultChannelViewReadModelTTL
	}
	return &channelViewReadModelCache{
		cache: readmodelcache.New[channelViewCacheKey, domain.ChannelView](readmodelcache.Config[channelViewCacheKey, domain.ChannelView]{
			MaxEntries: channelViewReadModelMaxEntries,
			TTL:        ttl,
			Clone:      cloneChannelView,
		}),
	}
}

func (c *channelViewReadModelCache) getOrLoad(ctx context.Context, key channelViewCacheKey, hash int64, load func() (domain.ChannelView, error)) (domain.ChannelView, error) {
	if c == nil {
		return load()
	}
	return c.cache.GetOrLoadVersioned(ctx, key, hash, load)
}

func (s *Service) cachedChannelView(ctx context.Context, userID, channelID int64) (domain.ChannelView, error) {
	if s.viewCache == nil || s.versions == nil {
		return s.channels.GetChannel(ctx, userID, channelID)
	}
	hash, err := s.channelViewHash(ctx, userID, channelID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if hash == 0 {
		return s.channels.GetChannel(ctx, userID, channelID)
	}
	key := channelViewCacheKey{userID: userID, channelID: channelID}
	return s.viewCache.getOrLoad(ctx, key, hash, func() (domain.ChannelView, error) {
		return s.channels.GetChannel(ctx, userID, channelID)
	})
}

func (s *Service) channelViewHash(ctx context.Context, userID, channelID int64) (int64, error) {
	keys := []store.ReadModelKey{
		{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		{Model: readmodel.ModelChannelMember, OwnerUserID: userID, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		{Model: readmodel.ModelDialogLight, OwnerUserID: userID, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		// The snapshot includes SelfBoostsApplied: channel_self_boosts must be part of the
		// validation token, otherwise applying/revoking a boost would not invalidate this
		// long-TTL snapshot. Note: a boost expiring naturally is time-based and triggers no
		// write, so its residue is still bounded by the TTL.
		{Model: readmodel.ModelChannelSelfBoosts, OwnerUserID: userID, PeerType: domain.PeerTypeChannel, PeerID: channelID},
	}
	rows, err := s.versions.ReadModelHashes(ctx, keys)
	if err != nil {
		return 0, err
	}
	base := rows[keys[0]]
	if base == 0 {
		return 0, nil
	}
	return readmodel.MixHashes(base, rows[keys[1]], rows[keys[2]], rows[keys[3]]), nil
}

func cloneChannelView(in domain.ChannelView) domain.ChannelView {
	in.Channel = cloneChannel(in.Channel)
	if in.Dialog.DefaultSendAs != nil {
		peer := *in.Dialog.DefaultSendAs
		in.Dialog.DefaultSendAs = &peer
	}
	if in.ExportedInvite != nil {
		invite := *in.ExportedInvite
		in.ExportedInvite = &invite
	}
	return in
}

func cloneChannel(in domain.Channel) domain.Channel {
	in.PhotoStripped = append([]byte(nil), in.PhotoStripped...)
	in.ReactionPolicy.Emoticons = append([]string(nil), in.ReactionPolicy.Emoticons...)
	in.ReactionPolicy.CustomEmojiIDs = append([]int64(nil), in.ReactionPolicy.CustomEmojiIDs...)
	return in
}
