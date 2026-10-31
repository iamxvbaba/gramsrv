package memory

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"sort"
	"sync"
	"telesrv/internal/domain"
)

// ContactStore is the in-memory implementation of store.ContactStore.
type ContactStore struct {
	mu                sync.RWMutex
	m                 map[int64]domain.ContactList
	blocks            map[int64]map[int64]domain.BlockedContact
	updateEvents      *UpdateEventStore
	privacy           *PrivacyStore
	blocklistStories  *StoryStore
	blocklistUsers    *UserStore
	blocklistChannels *ChannelStore
}

func (s *ContactStore) AttachUpdateEventStore(events *UpdateEventStore) {
	s.mu.Lock()
	s.updateEvents = events
	s.mu.Unlock()
}

func (s *ContactStore) AttachPrivacyStore(privacy *PrivacyStore) {
	s.mu.Lock()
	s.privacy = privacy
	s.mu.Unlock()
}

// NewContactStore creates an in-memory ContactStore.
func NewContactStore() *ContactStore {
	return &ContactStore{
		m:            make(map[int64]domain.ContactList),
		blocks:       make(map[int64]map[int64]domain.BlockedContact),
		updateEvents: NewUpdateEventStore(),
	}
}

func (s *ContactStore) ListByUser(_ context.Context, userID int64) (domain.ContactList, error) {
	s.mu.RLock()
	list := s.m[userID]
	s.mu.RUnlock()
	list.Contacts = cloneContacts(list.Contacts)
	list.Hash = contactListHash(list.Contacts)
	return list, nil
}

func (s *ContactStore) Get(_ context.Context, userID, contactUserID int64) (domain.Contact, bool, error) {
	s.mu.RLock()
	list := s.m[userID]
	s.mu.RUnlock()
	for _, contact := range list.Contacts {
		if contact.User.ID == contactUserID {
			return cloneContact(contact), true, nil
		}
	}
	return domain.Contact{}, false, nil
}

func (s *ContactStore) GetMany(_ context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.Contact, error) {
	out := make(map[int64]domain.Contact, len(contactUserIDs))
	if userID == 0 || len(contactUserIDs) == 0 {
		return out, nil
	}
	want := make(map[int64]struct{}, len(contactUserIDs))
	for _, id := range contactUserIDs {
		if id != 0 {
			want[id] = struct{}{}
		}
	}
	s.mu.RLock()
	list := s.m[userID]
	s.mu.RUnlock()
	for _, contact := range list.Contacts {
		if _, ok := want[contact.User.ID]; ok {
			out[contact.User.ID] = cloneContact(contact)
		}
	}
	return out, nil
}

func (s *ContactStore) GetReverseContacts(_ context.Context, userID int64, ownerUserIDs []int64) (map[int64]domain.Contact, error) {
	out := make(map[int64]domain.Contact, len(ownerUserIDs))
	if userID == 0 || len(ownerUserIDs) == 0 {
		return out, nil
	}
	want := make(map[int64]struct{}, len(ownerUserIDs))
	for _, id := range ownerUserIDs {
		if id != 0 {
			want[id] = struct{}{}
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for ownerID := range want {
		for _, contact := range s.m[ownerID].Contacts {
			if contact.User.ID == userID {
				out[ownerID] = cloneContact(contact)
				break
			}
		}
	}
	return out, nil
}

func (s *ContactStore) ContactProjectionForViewers(_ context.Context, viewerUserIDs, contactUserIDs []int64) (domain.ContactProjectionBatch, error) {
	out := domain.ContactProjectionBatch{
		Contacts:       make(map[int64]map[int64]domain.Contact, len(viewerUserIDs)),
		PersonalPhotos: make(map[int64]map[int64]domain.ProfilePhotoRef, len(viewerUserIDs)),
	}
	if len(viewerUserIDs) == 0 || len(contactUserIDs) == 0 {
		return out, nil
	}
	viewers := make(map[int64]struct{}, len(viewerUserIDs))
	for _, id := range viewerUserIDs {
		if id != 0 {
			viewers[id] = struct{}{}
		}
	}
	targets := make(map[int64]struct{}, len(contactUserIDs))
	for _, id := range contactUserIDs {
		if id != 0 {
			targets[id] = struct{}{}
		}
	}
	if len(viewers) == 0 || len(targets) == 0 {
		return out, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for viewerID := range viewers {
		list := s.m[viewerID]
		for _, contact := range list.Contacts {
			targetID := contact.User.ID
			if _, ok := targets[targetID]; !ok {
				continue
			}
			if out.Contacts[viewerID] == nil {
				out.Contacts[viewerID] = make(map[int64]domain.Contact, len(targets))
			}
			out.Contacts[viewerID][targetID] = domain.Contact{
				User:         domain.User{ID: targetID},
				FirstName:    contact.FirstName,
				LastName:     contact.LastName,
				Phone:        contact.Phone,
				Note:         contact.Note,
				NoteEntities: append([]domain.MessageEntity(nil), contact.NoteEntities...),
				Mutual:       contact.Mutual || contact.User.Mutual,
				CloseFriend:  contact.CloseFriend || contact.User.CloseFriend,
			}
			if contact.User.PhotoID == 0 {
				continue
			}
			if out.PersonalPhotos[viewerID] == nil {
				out.PersonalPhotos[viewerID] = make(map[int64]domain.ProfilePhotoRef, len(targets))
			}
			out.PersonalPhotos[viewerID][targetID] = cloneProfilePhotoRef(domain.ProfilePhotoRef{
				PhotoID:  contact.User.PhotoID,
				DCID:     contact.User.PhotoDCID,
				Stripped: contact.User.PhotoStripped,
				Personal: true,
				HasVideo: contact.User.PhotoHasVideo,
			})
		}
	}
	return out, nil
}

func (s *ContactStore) ContactProjectionForViewerUserIDs(_ context.Context, contactUserIDsByViewer map[int64][]int64) (domain.ContactProjectionBatch, error) {
	out := domain.ContactProjectionBatch{
		Contacts:       make(map[int64]map[int64]domain.Contact, len(contactUserIDsByViewer)),
		PersonalPhotos: make(map[int64]map[int64]domain.ProfilePhotoRef, len(contactUserIDsByViewer)),
	}
	if len(contactUserIDsByViewer) == 0 {
		return out, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for viewerID, contactUserIDs := range contactUserIDsByViewer {
		if viewerID == 0 || len(contactUserIDs) == 0 {
			continue
		}
		want := make(map[int64]struct{}, len(contactUserIDs))
		for _, id := range contactUserIDs {
			if id != 0 {
				want[id] = struct{}{}
			}
		}
		for _, contact := range s.m[viewerID].Contacts {
			targetID := contact.User.ID
			if _, ok := want[targetID]; !ok {
				continue
			}
			if out.Contacts[viewerID] == nil {
				out.Contacts[viewerID] = make(map[int64]domain.Contact, len(want))
			}
			out.Contacts[viewerID][targetID] = domain.Contact{
				User:         domain.User{ID: targetID},
				FirstName:    contact.FirstName,
				LastName:     contact.LastName,
				Phone:        contact.Phone,
				Note:         contact.Note,
				NoteEntities: append([]domain.MessageEntity(nil), contact.NoteEntities...),
				Mutual:       contact.Mutual || contact.User.Mutual,
				CloseFriend:  contact.CloseFriend || contact.User.CloseFriend,
			}
			if contact.User.PhotoID == 0 {
				continue
			}
			if out.PersonalPhotos[viewerID] == nil {
				out.PersonalPhotos[viewerID] = make(map[int64]domain.ProfilePhotoRef, len(want))
			}
			out.PersonalPhotos[viewerID][targetID] = cloneProfilePhotoRef(domain.ProfilePhotoRef{
				PhotoID: contact.User.PhotoID, DCID: contact.User.PhotoDCID,
				Stripped: contact.User.PhotoStripped, Personal: true, HasVideo: contact.User.PhotoHasVideo,
			})
		}
	}
	return out, nil
}

func (s *ContactStore) Upsert(_ context.Context, userID int64, input domain.ContactInput) (domain.Contact, error) {
	contact := domain.Contact{
		User: domain.User{
			ID:        input.ContactUserID,
			Phone:     input.Phone,
			FirstName: input.FirstName,
			LastName:  input.LastName,
			Contact:   true,
		},
		FirstName:    input.FirstName,
		LastName:     input.LastName,
		Phone:        input.Phone,
		Note:         input.Note,
		NoteEntities: append([]domain.MessageEntity(nil), input.NoteEntities...),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	reverse := s.m[input.ContactUserID]
	for i := range reverse.Contacts {
		if reverse.Contacts[i].User.ID == userID {
			reverse.Contacts[i].Mutual = true
			reverse.Contacts[i].User.Mutual = true
			contact.Mutual = true
			contact.User.Mutual = true
			s.m[input.ContactUserID] = reverse
			break
		}
	}
	for i, existing := range list.Contacts {
		if existing.User.ID != input.ContactUserID {
			continue
		}
		contact.User.AccessHash = existing.User.AccessHash
		contact.User.Username = existing.User.Username
		contact.User.CountryCode = existing.User.CountryCode
		contact.User.Verified = existing.User.Verified
		contact.User.Support = existing.User.Support
		// The premium/emoji/bot columns are preserved from the snapshot (the postgres path's
		// JOIN users always carries real values; the two stores' behaviour is aligned to keep
		// a repeated Upsert from wiping known state).
		contact.User.Bot = existing.User.Bot
		contact.User.BotInfoVersion = existing.User.BotInfoVersion
		contact.User.PremiumUntil = existing.User.PremiumUntil
		contact.User.EmojiStatusDocumentID = existing.User.EmojiStatusDocumentID
		contact.User.EmojiStatusUntil = existing.User.EmojiStatusUntil
		contact.CloseFriend = existing.CloseFriend
		contact.User.CloseFriend = existing.CloseFriend || existing.User.CloseFriend
		if contact.FirstName == "" {
			contact.User.FirstName = existing.User.FirstName
		}
		if contact.LastName == "" {
			contact.User.LastName = existing.User.LastName
		}
		list.Contacts[i] = contact
		list.Hash = contactListHash(list.Contacts)
		s.m[userID] = list
		return cloneContact(contact), nil
	}
	list.Contacts = append(list.Contacts, contact)
	list.Hash = contactListHash(list.Contacts)
	s.m[userID] = list
	return cloneContact(contact), nil
}

func (s *ContactStore) UpsertMany(ctx context.Context, userID int64, inputs []domain.ContactInput) ([]domain.Contact, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	out := make([]domain.Contact, 0, len(inputs))
	for _, input := range inputs {
		contact, err := s.Upsert(ctx, userID, input)
		if err != nil {
			return nil, err
		}
		out = append(out, contact)
	}
	return out, nil
}

func (s *ContactStore) UpdateNote(_ context.Context, userID, contactUserID int64, note string, entities []domain.MessageEntity) (domain.Contact, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	for i := range list.Contacts {
		if list.Contacts[i].User.ID != contactUserID {
			continue
		}
		list.Contacts[i].Note = note
		list.Contacts[i].NoteEntities = append([]domain.MessageEntity(nil), entities...)
		list.Hash = contactListHash(list.Contacts)
		s.m[userID] = list
		return cloneContact(list.Contacts[i]), true, nil
	}
	return domain.Contact{}, false, nil
}

func (s *ContactStore) SetCloseFriends(_ context.Context, userID int64, contactUserIDs []int64) (domain.CloseFriendsEditResult, error) {
	want := make(map[int64]struct{}, len(contactUserIDs))
	for _, id := range contactUserIDs {
		if id > 0 && id != userID {
			want[id] = struct{}{}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	changed := false
	var result domain.CloseFriendsEditResult
	for i := range list.Contacts {
		wasCloseFriend := list.Contacts[i].CloseFriend || list.Contacts[i].User.CloseFriend
		_, closeFriend := want[list.Contacts[i].User.ID]
		if list.Contacts[i].CloseFriend == closeFriend && list.Contacts[i].User.CloseFriend == closeFriend {
			continue
		}
		list.Contacts[i].CloseFriend = closeFriend
		list.Contacts[i].User.CloseFriend = closeFriend
		switch {
		case !wasCloseFriend && closeFriend:
			result.AddedUserIDs = append(result.AddedUserIDs, list.Contacts[i].User.ID)
		case wasCloseFriend && !closeFriend:
			result.RemovedUserIDs = append(result.RemovedUserIDs, list.Contacts[i].User.ID)
		}
		changed = true
	}
	if changed {
		list.Hash = contactListHash(list.Contacts)
		s.m[userID] = list
	}
	sort.Slice(result.AddedUserIDs, func(i, j int) bool { return result.AddedUserIDs[i] < result.AddedUserIDs[j] })
	sort.Slice(result.RemovedUserIDs, func(i, j int) bool { return result.RemovedUserIDs[i] < result.RemovedUserIDs[j] })
	return result, nil
}

func (s *ContactStore) SetPersonalPhoto(_ context.Context, userID, contactUserID int64, photoID int64, date int) (domain.Contact, bool, error) {
	_ = date
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	for i := range list.Contacts {
		if list.Contacts[i].User.ID != contactUserID {
			continue
		}
		list.Contacts[i].User.PhotoID = photoID
		list.Contacts[i].User.PhotoPersonal = photoID != 0
		list.Hash = contactListHash(list.Contacts)
		s.m[userID] = list
		return cloneContact(list.Contacts[i]), true, nil
	}
	return domain.Contact{}, false, nil
}

func (s *ContactStore) PersonalPhotos(_ context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	out := make(map[int64]domain.ProfilePhotoRef, len(contactUserIDs))
	if userID == 0 || len(contactUserIDs) == 0 {
		return out, nil
	}
	want := make(map[int64]struct{}, len(contactUserIDs))
	for _, id := range contactUserIDs {
		if id != 0 {
			want[id] = struct{}{}
		}
	}
	s.mu.RLock()
	list := s.m[userID]
	s.mu.RUnlock()
	for _, contact := range list.Contacts {
		if _, ok := want[contact.User.ID]; !ok || contact.User.PhotoID == 0 {
			continue
		}
		out[contact.User.ID] = domain.ProfilePhotoRef{
			PhotoID:  contact.User.PhotoID,
			DCID:     contact.User.PhotoDCID,
			Stripped: append([]byte(nil), contact.User.PhotoStripped...),
			Personal: true,
			HasVideo: contact.User.PhotoHasVideo,
		}
	}
	return out, nil
}

func (s *ContactStore) Delete(_ context.Context, userID int64, contactUserIDs []int64) (int, error) {
	remove := make(map[int64]struct{}, len(contactUserIDs))
	for _, id := range contactUserIDs {
		if id != 0 {
			remove[id] = struct{}{}
		}
	}
	if len(remove) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.m[userID]
	out := list.Contacts[:0]
	deleted := 0
	for _, contact := range list.Contacts {
		if _, ok := remove[contact.User.ID]; ok {
			deleted++
			if reverse := s.m[contact.User.ID]; len(reverse.Contacts) > 0 {
				for i := range reverse.Contacts {
					if reverse.Contacts[i].User.ID == userID {
						reverse.Contacts[i].Mutual = false
						reverse.Contacts[i].User.Mutual = false
					}
				}
				reverse.Hash = contactListHash(reverse.Contacts)
				s.m[contact.User.ID] = reverse
			}
			continue
		}
		out = append(out, contact)
	}
	list.Contacts = out
	list.Hash = contactListHash(list.Contacts)
	s.m[userID] = list
	return deleted, nil
}

func (s *ContactStore) IsBlocked(_ context.Context, userID, blockedUserID int64) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, blocked := s.blocks[userID][blockedUserID]
	return blocked, nil
}

func (s *ContactStore) ListBlocked(_ context.Context, userID int64, offset, limit int) (domain.BlockedContactList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]domain.BlockedContact, 0, len(s.blocks[userID]))
	for _, item := range s.blocks[userID] {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Date == items[j].Date {
			return items[i].User.ID > items[j].User.ID
		}
		return items[i].Date > items[j].Date
	})
	total := len(items)
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return domain.BlockedContactList{Count: total}, nil
	}
	if limit <= 0 || limit > len(items)-offset {
		limit = len(items) - offset
	}
	out := append([]domain.BlockedContact(nil), items[offset:offset+limit]...)
	return domain.BlockedContactList{Blocked: out, Count: total}, nil
}

// SaveList saves a user's contact list, for tests and local doubles.
func (s *ContactStore) SaveList(_ context.Context, userID int64, list domain.ContactList) error {
	list.Contacts = cloneContacts(list.Contacts)
	list.Hash = contactListHash(list.Contacts)
	s.mu.Lock()
	s.m[userID] = list
	s.mu.Unlock()
	return nil
}

func cloneContacts(contacts []domain.Contact) []domain.Contact {
	out := append([]domain.Contact(nil), contacts...)
	for i := range out {
		out[i] = cloneContact(out[i])
	}
	return out
}

func cloneContact(contact domain.Contact) domain.Contact {
	contact.NoteEntities = append([]domain.MessageEntity(nil), contact.NoteEntities...)
	contact.User.PhotoStripped = append([]byte(nil), contact.User.PhotoStripped...)
	return contact
}

func cloneProfilePhotoRef(ref domain.ProfilePhotoRef) domain.ProfilePhotoRef {
	ref.Stripped = append([]byte(nil), ref.Stripped...)
	return ref
}

func contactListHash(contacts []domain.Contact) int64 {
	if len(contacts) == 0 {
		return 0
	}
	h := fnv.New64a()
	var buf [16]byte
	for _, contact := range contacts {
		binary.LittleEndian.PutUint64(buf[:8], uint64(contact.User.ID))
		if contact.Mutual {
			buf[8] = 1
		} else {
			buf[8] = 0
		}
		if contact.CloseFriend || contact.User.CloseFriend {
			buf[9] = 1
		} else {
			buf[9] = 0
		}
		_, _ = h.Write(buf[:10])
		_, _ = h.Write([]byte(contact.FirstName))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(contact.LastName))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(contact.Phone))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(contact.Note))
		_, _ = h.Write([]byte{0})
	}
	return int64(h.Sum64())
}
