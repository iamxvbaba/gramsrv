package userprojection

import (
	"container/list"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	// DefaultContactProjectionCacheTTL is a safety bound for out-of-band writes.
	// Normal correctness relies on write-path invalidation, not natural expiry.
	DefaultContactProjectionCacheTTL = 24 * time.Hour

	contactSnapshotMaxViewers       = 4096
	contactReversePairMaxEntries    = 262144
	contactPersonalPhotoSnapshotCap = 4096
)

type contactAccountSnapshot struct {
	contacts map[int64]domain.Contact
	ordered  []domain.Contact
	hash     int64
	expireAt time.Time
}

type personalPhotoSnapshot struct {
	refs     map[int64]domain.ProfilePhotoRef
	expireAt time.Time
}

type reverseContactKey struct {
	ownerUserID   int64
	contactUserID int64
}

type reverseContactSnapshot struct {
	contact  domain.Contact
	found    bool
	expireAt time.Time
}

type reverseContactEntry struct {
	key      reverseContactKey
	snapshot reverseContactSnapshot
}

type contactSnapshotLoadResult struct {
	snap   contactAccountSnapshot
	stored bool
}

type reverseContactLoadResult struct {
	contacts map[int64]domain.Contact
	stored   bool
}

type personalPhotoSnapshotLoadResult struct {
	snap   personalPhotoSnapshot
	stored bool
}

// CachedContactStore wraps ContactStore with account-level read model snapshots.
//
// Contact data is low-churn and high-read: TDesktop repeatedly asks for the same
// viewer-scoped user projection while switching dialogs. Pair-level short TTL
// caching still lets every RPC plan new SQL for another pair; this cache loads a
// viewer's whole contact projection once, filters it in memory, and relies on
// contact write methods to invalidate the affected account snapshots.
type CachedContactStore struct {
	inner store.ContactStore
	ttl   time.Duration
	now   func() time.Time

	mu             sync.RWMutex
	contacts       map[int64]contactAccountSnapshot
	personalPhotos map[int64]personalPhotoSnapshot
	reverse        map[reverseContactKey]*list.Element
	reverseLRU     *list.List
	reverseByOwner map[int64]map[int64]struct{}
	reverseCap     int
	epoch          uint64
	sf             singleflight.Group
}

func NewCachedContactStore(inner store.ContactStore, ttl time.Duration) *CachedContactStore {
	if inner == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = DefaultContactProjectionCacheTTL
	}
	return &CachedContactStore{
		inner:          inner,
		ttl:            ttl,
		now:            time.Now,
		contacts:       make(map[int64]contactAccountSnapshot, 1024),
		personalPhotos: make(map[int64]personalPhotoSnapshot, 1024),
		reverse:        make(map[reverseContactKey]*list.Element, 4096),
		reverseLRU:     list.New(),
		reverseByOwner: make(map[int64]map[int64]struct{}, 1024),
		reverseCap:     contactReversePairMaxEntries,
	}
}

func (c *CachedContactStore) ListByUser(ctx context.Context, userID int64) (domain.ContactList, error) {
	if userID == 0 {
		return domain.ContactList{}, nil
	}
	snap, err := c.contactSnapshot(ctx, userID)
	if err != nil {
		return domain.ContactList{}, err
	}
	return domain.ContactList{Contacts: cloneCachedContacts(snap.ordered), Hash: snap.hash}, nil
}

func (c *CachedContactStore) Get(ctx context.Context, userID, contactUserID int64) (domain.Contact, bool, error) {
	if userID == 0 || contactUserID == 0 {
		return domain.Contact{}, false, nil
	}
	got, err := c.GetMany(ctx, userID, []int64{contactUserID})
	if err != nil {
		return domain.Contact{}, false, err
	}
	contact, ok := got[contactUserID]
	return contact, ok, nil
}

func (c *CachedContactStore) GetMany(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.Contact, error) {
	out := make(map[int64]domain.Contact, len(contactUserIDs))
	if userID == 0 || len(contactUserIDs) == 0 {
		return out, nil
	}
	snap, err := c.contactSnapshot(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, ownerID := range contactUserIDs {
		if ownerID == 0 {
			continue
		}
		if contact, ok := snap.contacts[ownerID]; ok {
			out[ownerID] = cloneCachedContact(contact)
		}
	}
	return out, nil
}

func (c *CachedContactStore) GetReverseContacts(ctx context.Context, userID int64, ownerUserIDs []int64) (map[int64]domain.Contact, error) {
	out := make(map[int64]domain.Contact, len(ownerUserIDs))
	if userID == 0 || len(ownerUserIDs) == 0 {
		return out, nil
	}
	owners := dedupContactIDs(ownerUserIDs)
	if len(owners) == 0 {
		return out, nil
	}
	missing := make([]int64, 0, len(owners))
	now := c.now()
	for _, ownerID := range owners {
		// Reuse a full owner snapshot when another hot path already loaded it.
		// Do not cold-load one full list per owner: a large projection would turn
		// into N SQL queries.
		if snap, ok := c.lookupContactSnapshot(ownerID, now); ok {
			if contact, found := snap.contacts[userID]; found {
				out[ownerID] = cloneCachedContact(contact)
			}
			continue
		}
		if contact, found, cached := c.lookupReverseContact(ownerID, userID, now); cached {
			if found {
				out[ownerID] = contact
			}
			continue
		}
		missing = append(missing, ownerID)
	}
	if len(missing) == 0 {
		return out, nil
	}
	loaded, err := c.loadReverseContacts(ctx, userID, missing)
	if err != nil {
		return nil, err
	}
	for ownerID, contact := range loaded {
		if contact.User.ID != 0 {
			out[ownerID] = cloneCachedContact(contact)
		}
	}
	return out, nil
}

// loadReverseContacts performs at most one batched cold-store read for all
// missing owner→viewer pairs, then caches both hits and misses. Privacy
// projection therefore stays memory-only after warm-up instead of repeating a
// reverse-contact SQL query on every large user vector.
func (c *CachedContactStore) loadReverseContacts(ctx context.Context, userID int64, ownerUserIDs []int64) (map[int64]domain.Contact, error) {
	owners := append([]int64(nil), ownerUserIDs...)
	sort.Slice(owners, func(i, j int) bool { return owners[i] < owners[j] })
	sfKey := fmt.Sprintf("contact-reverse:%d:%v", userID, owners)
	for {
		v, err, _ := c.sf.Do(sfKey, func() (any, error) {
			loadEpoch := c.cacheEpoch()
			contacts, err := c.inner.GetReverseContacts(ctx, userID, owners)
			if err != nil {
				return reverseContactLoadResult{}, err
			}
			now := c.now()
			expireAt := now.Add(c.ttl)
			c.mu.Lock()
			stored := c.epoch == loadEpoch
			if stored {
				for _, ownerID := range owners {
					key := reverseContactKey{ownerUserID: ownerID, contactUserID: userID}
					contact, found := contacts[ownerID]
					c.storeReverseContactLocked(key, reverseContactSnapshot{
						contact:  cloneCachedContact(contact),
						found:    found,
						expireAt: expireAt,
					})
				}
			}
			c.mu.Unlock()
			return reverseContactLoadResult{
				contacts: cloneCachedContactMap(contacts),
				stored:   stored,
			}, nil
		})
		if err != nil {
			return nil, err
		}
		result := v.(reverseContactLoadResult)
		if result.stored {
			return result.contacts, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

func (c *CachedContactStore) Upsert(ctx context.Context, userID int64, input domain.ContactInput) (domain.Contact, error) {
	contact, err := c.inner.Upsert(ctx, userID, input)
	if err == nil {
		c.InvalidateViewers(userID, input.ContactUserID)
	}
	return contact, err
}

func (c *CachedContactStore) UpsertMany(ctx context.Context, userID int64, inputs []domain.ContactInput) ([]domain.Contact, error) {
	contacts, err := c.inner.UpsertMany(ctx, userID, inputs)
	if err == nil {
		ids := make([]int64, 0, len(inputs)+1)
		ids = append(ids, userID)
		for _, input := range inputs {
			ids = append(ids, input.ContactUserID)
		}
		c.InvalidateViewers(ids...)
	}
	return contacts, err
}

func (c *CachedContactStore) UpdateNote(ctx context.Context, userID, contactUserID int64, note string, entities []domain.MessageEntity) (domain.Contact, bool, error) {
	contact, found, err := c.inner.UpdateNote(ctx, userID, contactUserID, note, entities)
	if err == nil {
		c.InvalidateViewers(userID)
	}
	return contact, found, err
}

func (c *CachedContactStore) SetCloseFriends(ctx context.Context, userID int64, contactUserIDs []int64) (domain.CloseFriendsEditResult, error) {
	res, err := c.inner.SetCloseFriends(ctx, userID, contactUserIDs)
	if err == nil {
		c.InvalidateViewers(userID)
	}
	return res, err
}

func (c *CachedContactStore) SetPersonalPhoto(ctx context.Context, userID, contactUserID int64, photoID int64, date int) (domain.Contact, bool, error) {
	contact, found, err := c.inner.SetPersonalPhoto(ctx, userID, contactUserID, photoID, date)
	if err == nil {
		c.InvalidateViewers(userID)
	}
	return contact, found, err
}

func (c *CachedContactStore) PersonalPhotos(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	out := make(map[int64]domain.ProfilePhotoRef, len(contactUserIDs))
	if userID == 0 || len(contactUserIDs) == 0 {
		return out, nil
	}
	snap, err := c.personalPhotoSnapshot(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, ownerID := range contactUserIDs {
		if ownerID == 0 {
			continue
		}
		if ref, ok := snap.refs[ownerID]; ok {
			out[ownerID] = cloneCachedProfilePhotoRef(ref)
		}
	}
	return out, nil
}

func (c *CachedContactStore) Delete(ctx context.Context, userID int64, contactUserIDs []int64) (int, error) {
	count, err := c.inner.Delete(ctx, userID, contactUserIDs)
	if err == nil {
		ids := make([]int64, 0, len(contactUserIDs)+1)
		ids = append(ids, userID)
		ids = append(ids, contactUserIDs...)
		c.InvalidateViewers(ids...)
	}
	return count, err
}

func (c *CachedContactStore) Block(ctx context.Context, userID, blockedUserID int64, date int) (bool, error) {
	changed, err := c.inner.Block(ctx, userID, blockedUserID, date)
	if err == nil {
		c.InvalidateViewers(userID, blockedUserID)
	}
	return changed, err
}

func (c *CachedContactStore) Unblock(ctx context.Context, userID, blockedUserID int64) (bool, error) {
	changed, err := c.inner.Unblock(ctx, userID, blockedUserID)
	if err == nil {
		c.InvalidateViewers(userID, blockedUserID)
	}
	return changed, err
}

func (c *CachedContactStore) IsBlocked(ctx context.Context, userID, blockedUserID int64) (bool, error) {
	return c.inner.IsBlocked(ctx, userID, blockedUserID)
}

func (c *CachedContactStore) ListBlocked(ctx context.Context, userID int64, offset, limit int) (domain.BlockedContactList, error) {
	return c.inner.ListBlocked(ctx, userID, offset, limit)
}

func (c *CachedContactStore) contactSnapshot(ctx context.Context, userID int64) (contactAccountSnapshot, error) {
	for {
		if snap, ok := c.lookupContactSnapshot(userID, c.now()); ok {
			return snap, nil
		}
		v, err, _ := c.sf.Do(fmt.Sprintf("contact:%d", userID), func() (any, error) {
			now := c.now()
			if snap, ok := c.lookupContactSnapshot(userID, now); ok {
				return contactSnapshotLoadResult{snap: snap, stored: true}, nil
			}
			loadEpoch := c.cacheEpoch()
			list, err := c.inner.ListByUser(ctx, userID)
			if err != nil {
				return contactSnapshotLoadResult{}, err
			}
			snap := buildContactAccountSnapshot(list, now.Add(c.ttl))
			c.mu.Lock()
			stored := c.epoch == loadEpoch
			if stored {
				if len(c.contacts) >= contactSnapshotMaxViewers {
					c.contacts = make(map[int64]contactAccountSnapshot, 1024)
					c.personalPhotos = make(map[int64]personalPhotoSnapshot, 1024)
				}
				c.contacts[userID] = snap
			}
			c.mu.Unlock()
			return contactSnapshotLoadResult{snap: snap, stored: stored}, nil
		})
		if err != nil {
			return contactAccountSnapshot{}, err
		}
		result := v.(contactSnapshotLoadResult)
		if result.stored {
			return result.snap, nil
		}
		if err := ctx.Err(); err != nil {
			return contactAccountSnapshot{}, err
		}
	}
}

func (c *CachedContactStore) lookupContactSnapshot(userID int64, now time.Time) (contactAccountSnapshot, bool) {
	c.mu.RLock()
	snap, ok := c.contacts[userID]
	c.mu.RUnlock()
	if !ok || !snap.expireAt.After(now) {
		if ok {
			c.InvalidateViewers(userID)
		}
		return contactAccountSnapshot{}, false
	}
	return snap, true
}

func (c *CachedContactStore) personalPhotoSnapshot(ctx context.Context, userID int64) (personalPhotoSnapshot, error) {
	for {
		if snap, ok := c.lookupPersonalPhotoSnapshot(userID, c.now()); ok {
			return snap, nil
		}
		v, err, _ := c.sf.Do(fmt.Sprintf("contact-photo:%d", userID), func() (any, error) {
			now := c.now()
			if snap, ok := c.lookupPersonalPhotoSnapshot(userID, now); ok {
				return personalPhotoSnapshotLoadResult{snap: snap, stored: true}, nil
			}
			loadEpoch := c.cacheEpoch()
			contacts, err := c.contactSnapshot(ctx, userID)
			if err != nil {
				return personalPhotoSnapshotLoadResult{}, err
			}
			ids := make([]int64, 0, len(contacts.contacts))
			for id := range contacts.contacts {
				ids = append(ids, id)
			}
			refs := map[int64]domain.ProfilePhotoRef{}
			if len(ids) > 0 {
				refs, err = c.inner.PersonalPhotos(ctx, userID, ids)
				if err != nil {
					return personalPhotoSnapshotLoadResult{}, err
				}
			}
			snap := personalPhotoSnapshot{refs: cloneCachedProfilePhotoRefs(refs), expireAt: now.Add(c.ttl)}
			c.mu.Lock()
			stored := c.epoch == loadEpoch
			if stored {
				if len(c.personalPhotos) >= contactPersonalPhotoSnapshotCap {
					c.personalPhotos = make(map[int64]personalPhotoSnapshot, 1024)
				}
				c.personalPhotos[userID] = snap
			}
			c.mu.Unlock()
			return personalPhotoSnapshotLoadResult{snap: snap, stored: stored}, nil
		})
		if err != nil {
			return personalPhotoSnapshot{}, err
		}
		result := v.(personalPhotoSnapshotLoadResult)
		if result.stored {
			return result.snap, nil
		}
		if err := ctx.Err(); err != nil {
			return personalPhotoSnapshot{}, err
		}
	}
}

func (c *CachedContactStore) lookupPersonalPhotoSnapshot(userID int64, now time.Time) (personalPhotoSnapshot, bool) {
	c.mu.RLock()
	snap, ok := c.personalPhotos[userID]
	c.mu.RUnlock()
	if !ok || !snap.expireAt.After(now) {
		if ok {
			c.InvalidateViewers(userID)
		}
		return personalPhotoSnapshot{}, false
	}
	return snap, true
}

func (c *CachedContactStore) lookupReverseContact(ownerUserID, contactUserID int64, now time.Time) (domain.Contact, bool, bool) {
	key := reverseContactKey{ownerUserID: ownerUserID, contactUserID: contactUserID}
	c.mu.Lock()
	element, ok := c.reverse[key]
	if !ok {
		c.mu.Unlock()
		return domain.Contact{}, false, false
	}
	entry := element.Value.(*reverseContactEntry)
	snap := entry.snapshot
	if !snap.expireAt.After(now) {
		c.removeReverseElementLocked(element)
		c.mu.Unlock()
		return domain.Contact{}, false, false
	}
	c.reverseLRU.MoveToFront(element)
	c.mu.Unlock()
	return cloneCachedContact(snap.contact), snap.found, true
}

func (c *CachedContactStore) storeReverseContactLocked(key reverseContactKey, snapshot reverseContactSnapshot) {
	if element, ok := c.reverse[key]; ok {
		entry := element.Value.(*reverseContactEntry)
		entry.snapshot = snapshot
		c.reverseLRU.MoveToFront(element)
		return
	}
	element := c.reverseLRU.PushFront(&reverseContactEntry{key: key, snapshot: snapshot})
	c.reverse[key] = element
	if c.reverseByOwner[key.ownerUserID] == nil {
		c.reverseByOwner[key.ownerUserID] = make(map[int64]struct{})
	}
	c.reverseByOwner[key.ownerUserID][key.contactUserID] = struct{}{}
	for c.reverseLRU.Len() > c.reverseCap {
		c.removeReverseElementLocked(c.reverseLRU.Back())
	}
}

func (c *CachedContactStore) removeReverseElementLocked(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*reverseContactEntry)
	delete(c.reverse, entry.key)
	if viewers := c.reverseByOwner[entry.key.ownerUserID]; viewers != nil {
		delete(viewers, entry.key.contactUserID)
		if len(viewers) == 0 {
			delete(c.reverseByOwner, entry.key.ownerUserID)
		}
	}
	c.reverseLRU.Remove(element)
}

func (c *CachedContactStore) InvalidateViewers(ids ...int64) {
	if c == nil || len(ids) == 0 {
		return
	}
	c.mu.Lock()
	c.epoch++
	for _, id := range ids {
		if id == 0 {
			continue
		}
		delete(c.contacts, id)
		delete(c.personalPhotos, id)
		for contactUserID := range c.reverseByOwner[id] {
			if element, ok := c.reverse[reverseContactKey{ownerUserID: id, contactUserID: contactUserID}]; ok {
				c.removeReverseElementLocked(element)
			}
		}
	}
	c.mu.Unlock()
}

func (c *CachedContactStore) FlushReadModelCache() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.epoch++
	c.contacts = make(map[int64]contactAccountSnapshot, 1024)
	c.personalPhotos = make(map[int64]personalPhotoSnapshot, 1024)
	c.reverse = make(map[reverseContactKey]*list.Element, 4096)
	c.reverseLRU.Init()
	c.reverseByOwner = make(map[int64]map[int64]struct{}, 1024)
	c.mu.Unlock()
}

func (c *CachedContactStore) cacheEpoch() uint64 {
	c.mu.RLock()
	epoch := c.epoch
	c.mu.RUnlock()
	return epoch
}

func buildContactAccountSnapshot(list domain.ContactList, expireAt time.Time) contactAccountSnapshot {
	contacts := make(map[int64]domain.Contact, len(list.Contacts))
	ordered := make([]domain.Contact, 0, len(list.Contacts))
	for _, contact := range list.Contacts {
		if contact.User.ID == 0 {
			continue
		}
		clone := cloneCachedContact(contact)
		contacts[clone.User.ID] = clone
		ordered = append(ordered, clone)
	}
	return contactAccountSnapshot{contacts: contacts, ordered: ordered, hash: list.Hash, expireAt: expireAt}
}

func cloneCachedContactMap(in map[int64]domain.Contact) map[int64]domain.Contact {
	out := make(map[int64]domain.Contact, len(in))
	for id, contact := range in {
		out[id] = cloneCachedContact(contact)
	}
	return out
}

func dedupContactIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func cloneCachedContacts(in []domain.Contact) []domain.Contact {
	out := make([]domain.Contact, len(in))
	for i := range in {
		out[i] = cloneCachedContact(in[i])
	}
	return out
}

func cloneCachedContact(in domain.Contact) domain.Contact {
	in.User = cloneCachedUser(in.User)
	if in.NoteEntities != nil {
		in.NoteEntities = append([]domain.MessageEntity(nil), in.NoteEntities...)
	}
	return in
}

func cloneCachedUser(in domain.User) domain.User {
	if in.PhotoStripped != nil {
		in.PhotoStripped = append([]byte(nil), in.PhotoStripped...)
	}
	if in.ContactNoteEntities != nil {
		in.ContactNoteEntities = append([]domain.MessageEntity(nil), in.ContactNoteEntities...)
	}
	if in.RestrictionReasons != nil {
		in.RestrictionReasons = append([]domain.UserRestrictionReason(nil), in.RestrictionReasons...)
	}
	return in
}

func cloneCachedProfilePhotoRefs(in map[int64]domain.ProfilePhotoRef) map[int64]domain.ProfilePhotoRef {
	out := make(map[int64]domain.ProfilePhotoRef, len(in))
	for id, ref := range in {
		out[id] = cloneCachedProfilePhotoRef(ref)
	}
	return out
}

func cloneCachedProfilePhotoRef(in domain.ProfilePhotoRef) domain.ProfilePhotoRef {
	if in.Stripped != nil {
		in.Stripped = append([]byte(nil), in.Stripped...)
	}
	return in
}
