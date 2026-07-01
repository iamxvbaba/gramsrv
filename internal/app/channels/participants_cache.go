package channels

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"strings"
	"time"

	"telesrv/internal/app/readmodel"
	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
	"telesrv/internal/store"
)

const (
	defaultParticipantsReadModelTTL = 30 * time.Minute
	participantsReadModelMaxEntries = 8192
)

type participantsCacheKey struct {
	userID    int64
	channelID int64
	kind      domain.ChannelParticipantsFilterKind
	query     string
	offset    int
	limit     int
}

// participantsReadModelCache 由统一缓存原语承载(版本闸门 / epoch 守卫 / LRU 单条驱逐 / clone)。
// LRU 终于给 (query,offset,limit) 维度的 page-key 上界,消掉原先无界 query-string 基数增长。
type participantsReadModelCache struct {
	cache *readmodelcache.Cache[participantsCacheKey, domain.ChannelParticipantList]
}

func newParticipantsReadModelCache(ttl time.Duration) *participantsReadModelCache {
	if ttl <= 0 {
		ttl = defaultParticipantsReadModelTTL
	}
	return &participantsReadModelCache{
		cache: readmodelcache.New[participantsCacheKey, domain.ChannelParticipantList](readmodelcache.Config[participantsCacheKey, domain.ChannelParticipantList]{
			MaxEntries: participantsReadModelMaxEntries,
			TTL:        ttl,
			Clone:      cloneParticipantList,
		}),
	}
}

func (c *participantsReadModelCache) getOrLoad(ctx context.Context, key participantsCacheKey, hash int64, load func() (domain.ChannelParticipantList, error)) (domain.ChannelParticipantList, error) {
	if c == nil {
		return load()
	}
	return c.cache.GetOrLoadVersioned(ctx, key, hash, load)
}

func (c *participantsReadModelCache) invalidateChannel(channelID int64) {
	if c == nil || channelID == 0 {
		return
	}
	c.cache.InvalidateWhere(func(key participantsCacheKey) bool {
		return key.channelID == channelID
	})
}

func (s *Service) cachedParticipants(ctx context.Context, userID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error) {
	filter, offset, limit = normalizeParticipantsRequest(filter, offset, limit)
	if s.participantCache == nil || s.versions == nil {
		return s.loadParticipants(ctx, userID, channelID, filter, offset, limit)
	}
	key := participantsCacheKey{
		userID:    userID,
		channelID: channelID,
		kind:      filter.Kind,
		query:     normalizeParticipantsQuery(filter.Query),
		offset:    offset,
		limit:     limit,
	}
	hash, err := s.channelParticipantsHash(ctx, userID, channelID, key)
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	if hash == 0 {
		return s.loadParticipants(ctx, userID, channelID, filter, offset, limit)
	}
	return s.participantCache.getOrLoad(ctx, key, hash, func() (domain.ChannelParticipantList, error) {
		list, err := s.loadParticipants(ctx, userID, channelID, filter, offset, limit)
		if err != nil {
			return domain.ChannelParticipantList{}, err
		}
		list.Hash = hash
		return list, nil
	})
}

func (s *Service) loadParticipants(ctx context.Context, userID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error) {
	if filter.Kind == domain.ChannelParticipantsBots && s.bots != nil {
		return s.getBotParticipants(ctx, userID, channelID, offset, limit)
	}
	return s.channels.GetParticipants(ctx, userID, channelID, filter, offset, limit)
}

func (s *Service) channelParticipantsHash(ctx context.Context, userID, channelID int64, key participantsCacheKey) (int64, error) {
	keys := []store.ReadModelKey{
		{Model: readmodel.ModelChannelBase, OwnerUserID: 0, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		{Model: readmodel.ModelChannelParticipants, OwnerUserID: 0, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		{Model: readmodel.ModelChannelMember, OwnerUserID: userID, PeerType: domain.PeerTypeChannel, PeerID: channelID},
		{Model: readmodel.ModelContactAccount, OwnerUserID: userID, PeerType: domain.PeerTypeUser, PeerID: userID},
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
	return readmodel.MixHashes(base, participants, rows[keys[2]], rows[keys[3]], participantsPageHash(key)), nil
}

func normalizeParticipantsRequest(filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantsFilter, int, int) {
	if filter.Kind == "" {
		filter.Kind = domain.ChannelParticipantsRecent
	}
	filter.Query = normalizeParticipantsQuery(filter.Query)
	if offset < 0 {
		offset = 0
	}
	if offset > domain.MaxChannelParticipantsOffset {
		offset = domain.MaxChannelParticipantsOffset
	}
	if limit <= 0 || limit > domain.MaxChannelParticipantsLimit {
		limit = domain.MaxChannelParticipantsLimit
	}
	return filter, offset, limit
}

func normalizeParticipantsQuery(query string) string {
	return strings.ToLower(strings.TrimSpace(query))
}

func participantsPageHash(key participantsCacheKey) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key.kind))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(key.query))
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(key.offset))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(key.limit))
	_, _ = h.Write(buf[:])
	sum := int64(h.Sum64() & 0x7fffffffffffffff)
	if sum == 0 {
		return 1
	}
	return sum
}

func cloneParticipantList(in domain.ChannelParticipantList) domain.ChannelParticipantList {
	in.Channel = cloneChannel(in.Channel)
	in.Participants = append([]domain.ChannelMember(nil), in.Participants...)
	if len(in.Users) > 0 {
		in.Users = make([]domain.User, len(in.Users))
		for i, user := range in.Users {
			user.PhotoStripped = append([]byte(nil), user.PhotoStripped...)
			in.Users[i] = user
		}
	}
	return in
}
