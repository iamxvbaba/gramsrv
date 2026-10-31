package memory

import (
	"context"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PhoneChangeStore is a test-only in-memory implementation. User uniqueness is maintained
// under the UserStore lock; updateUserPhone has no PTS, so it does not write a durable event to UpdateEventStore.
type PhoneChangeStore struct {
	users  *UserStore
	events store.UpdateEventStore
}

func NewPhoneChangeStore(users *UserStore, events store.UpdateEventStore) *PhoneChangeStore {
	return &PhoneChangeStore{users: users, events: events}
}

func (s *PhoneChangeStore) ChangePhone(_ context.Context, req domain.PhoneChangeRequest) (domain.PhoneChangeResult, error) {
	if s == nil || s.users == nil || req.UserID == 0 || !domain.ValidPhone(req.Phone) {
		return domain.PhoneChangeResult{}, domain.ErrPhoneNumberInvalid
	}
	s.users.mu.Lock()
	u, ok := s.users.byID[req.UserID]
	if !ok {
		s.users.mu.Unlock()
		return domain.PhoneChangeResult{}, domain.ErrUserNotFound
	}
	if u.Phone == req.Phone {
		s.users.mu.Unlock()
		return domain.PhoneChangeResult{User: u}, nil
	}
	for id, existing := range s.users.byID {
		if id != req.UserID && existing.Phone == req.Phone {
			s.users.mu.Unlock()
			return domain.PhoneChangeResult{}, domain.ErrPhoneNumberOccupied
		}
	}
	u.Phone = req.Phone
	s.users.byID[req.UserID] = u
	s.users.mu.Unlock()
	return domain.PhoneChangeResult{User: u, Changed: true}, nil
}
