package channels

import (
	"context"
	"encoding/binary"
	"hash/fnv"

	"telesrv/internal/app/readmodel"
	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
	"telesrv/internal/store"
)

const (
	activeBotMemberIDsMaxEntries = 8192
)

type activeBotMemberIDsCacheKey struct {
	viewerUserID int64
	channelID    int64
	limit        int
}

type activeBotMemberIDsCache struct {
	cache *readmodelcache.Cache[activeBotMemberIDsCacheKey, []int64]
}

func newActiveBotMemberIDsCache() *activeBotMemberIDsCache {
	return &activeBotMemberIDsCache{
		cache: readmodelcache.New[activeBotMemberIDsCacheKey, []int64](readmodelcache.Config[activeBotMemberIDsCacheKey, []int64]{
			MaxEntries: activeBotMemberIDsMaxEntries,
			Clone:      cloneInt64s,
		}),
	}
}

func (c *activeBotMemberIDsCache) getOrLoad(ctx context.Context, key activeBotMemberIDsCacheKey, load func() ([]int64, error)) ([]int64, error) {
	if c == nil {
		return load()
	}
	return c.cache.GetOrLoad(ctx, key, load)
}

func (c *activeBotMemberIDsCache) getOrLoadVersioned(ctx context.Context, key activeBotMemberIDsCacheKey, hash int64, load func() ([]int64, error)) ([]int64, error) {
	if c == nil {
		return load()
	}
	return c.cache.GetOrLoadVersioned(ctx, key, hash, load)
}

func (c *activeBotMemberIDsCache) invalidateChannel(channelID int64) {
	if c == nil || channelID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(key activeBotMemberIDsCacheKey) bool {
		return key.channelID == channelID
	})
}

func (c *activeBotMemberIDsCache) flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

func (s *Service) channelBotMemberIDsHash(ctx context.Context, viewerUserID, channelID int64, key activeBotMemberIDsCacheKey) (int64, error) {
	keys := []store.ReadModelKey{
		{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		{Model: readmodel.ModelChannelParticipants, OwnerUserID: 0, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		{Model: readmodel.ModelChannelMember, OwnerUserID: viewerUserID, PeerType: domain.PeerTypeChannel, PeerID: channelID},
	}
	rows, err := s.versions.ReadModelHashes(ctx, keys)
	if err != nil {
		return 0, err
	}
	base := rows[keys[0]]
	participants := rows[keys[1]]
	if base == 0 || participants == 0 {
		return 0, nil
	}
	return readmodel.MixHashes(base, participants, rows[keys[2]], botMemberIDsKeyHash(key)), nil
}

func botMemberIDsKeyHash(key activeBotMemberIDsCacheKey) int64 {
	h := fnv.New64a()
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(key.limit))
	_, _ = h.Write(buf[:])
	sum := int64(h.Sum64() & 0x7fffffffffffffff)
	if sum == 0 {
		return 1
	}
	return sum
}
