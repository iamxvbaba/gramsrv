package contacts

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/readmodelcache"
)

const (
	contactAccountReadModel        = "contact_account"
	defaultContactListReadModelTTL = 24 * time.Hour
	contactListReadModelMaxUsers   = 4096
)

// contactListReadModelCache 是 contact list read-model 的 per-viewer 缓存,由统一缓存原语
// readmodelcache.Cache 承载(版本闸门 / epoch 守卫 / LRU 单条驱逐 / clone 内建)。
type contactListReadModelCache struct {
	cache *readmodelcache.Cache[int64, domain.ContactList]
}

func newContactListReadModelCache(ttl time.Duration) *contactListReadModelCache {
	if ttl <= 0 {
		ttl = defaultContactListReadModelTTL
	}
	return &contactListReadModelCache{
		cache: readmodelcache.New[int64, domain.ContactList](readmodelcache.Config[int64, domain.ContactList]{
			MaxEntries: contactListReadModelMaxUsers,
			TTL:        ttl,
			Clone:      cloneContactList,
		}),
	}
}

// getOrLoad 命中即返回 clone,否则经 singleflight load。版本闸门:hasHash 且 currentHash!=0
// 时仅复用 storedHash==currentHash 的快照,否则重载(对齐 contact_account 版本脊)。
func (c *contactListReadModelCache) getOrLoad(ctx context.Context, userID int64, currentHash int64, hasHash bool, load func() (domain.ContactList, error)) (domain.ContactList, error) {
	if c == nil {
		return load()
	}
	effectiveHash := int64(0)
	if hasHash {
		effectiveHash = currentHash
	}
	return c.cache.GetOrLoadVersioned(ctx, userID, effectiveHash, load)
}

func (c *contactListReadModelCache) invalidate(ids ...int64) {
	if c == nil {
		return
	}
	c.cache.Invalidate(ids...)
}

func (c *contactListReadModelCache) flush() {
	if c == nil {
		return
	}
	c.cache.Flush()
}

func (s *Service) contactAccountHash(ctx context.Context, userID int64) (int64, bool, error) {
	if s == nil || s.versions == nil || userID == 0 {
		return 0, false, nil
	}
	return s.versions.ReadModelHash(ctx, contactAccountReadModel, userID, domain.PeerTypeUser, userID)
}

func (s *Service) contactListReadModel(ctx context.Context, userID int64, currentHash int64, hasHash bool) (domain.ContactList, error) {
	if s == nil {
		return domain.ContactList{}, nil
	}
	if s.cache == nil {
		return s.loadContactListReadModel(ctx, userID, currentHash, hasHash)
	}
	return s.cache.getOrLoad(ctx, userID, currentHash, hasHash, func() (domain.ContactList, error) {
		return s.loadContactListReadModel(ctx, userID, currentHash, hasHash)
	})
}

func (s *Service) loadContactListReadModel(ctx context.Context, userID int64, currentHash int64, hasHash bool) (domain.ContactList, error) {
	list, err := s.contacts.ListByUser(ctx, userID)
	if err != nil {
		return domain.ContactList{}, err
	}
	if err := s.projectContactUsers(ctx, userID, &list); err != nil {
		return domain.ContactList{}, err
	}
	if hasHash && currentHash != 0 {
		list.Hash = currentHash
	}
	return list, nil
}

func (s *Service) InvalidateViewers(ids ...int64) {
	if s == nil || s.cache == nil {
		return
	}
	s.cache.invalidate(ids...)
}

func (s *Service) FlushReadModelCache() {
	if s == nil || s.cache == nil {
		return
	}
	s.cache.flush()
}

func cloneContactList(in domain.ContactList) domain.ContactList {
	in.Contacts = cloneContacts(in.Contacts)
	return in
}

func cloneContacts(in []domain.Contact) []domain.Contact {
	out := make([]domain.Contact, len(in))
	for i := range in {
		out[i] = cloneContact(in[i])
	}
	return out
}

func cloneContact(in domain.Contact) domain.Contact {
	in.User = cloneUser(in.User)
	if in.NoteEntities != nil {
		in.NoteEntities = append([]domain.MessageEntity(nil), in.NoteEntities...)
	}
	return in
}

func cloneUser(in domain.User) domain.User {
	if in.PhotoStripped != nil {
		in.PhotoStripped = append([]byte(nil), in.PhotoStripped...)
	}
	if in.RestrictionReasons != nil {
		in.RestrictionReasons = append([]domain.UserRestrictionReason(nil), in.RestrictionReasons...)
	}
	return in
}
