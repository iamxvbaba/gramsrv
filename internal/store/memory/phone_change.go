package memory

import (
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PhoneChangeStore 是测试用内存实现。用户唯一性在 UserStore 锁内维护；事件写入
// 共享 UpdateEventStore 后可由 updates.getDifference 重放。
type PhoneChangeStore struct {
	users  *UserStore
	events store.UpdateEventStore
}

func NewPhoneChangeStore(users *UserStore, events store.UpdateEventStore) *PhoneChangeStore {
	return &PhoneChangeStore{users: users, events: events}
}

func (*PhoneChangeStore) UsesReliableDispatch() bool { return false }

func (s *PhoneChangeStore) ChangePhone(ctx context.Context, req domain.PhoneChangeRequest) (domain.PhoneChangeResult, error) {
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
	currentPhone := u.Phone
	u.Phone = req.Phone
	s.users.byID[req.UserID] = u

	date := req.Date
	if date == 0 {
		date = int(time.Now().Unix())
	}
	event := domain.UpdateEvent{
		UserID:   req.UserID,
		Type:     domain.UpdateEventUserPhone,
		Date:     date,
		Phone:    req.Phone,
		PtsCount: 1,
	}
	if s.events != nil {
		var err error
		event, err = s.events.AppendAllocated(ctx, req.UserID, event)
		if err != nil {
			// 保持内存替身与 PG 的 user+event 原子可见语义。
			u.Phone = currentPhone
			s.users.byID[req.UserID] = u
			s.users.mu.Unlock()
			return domain.PhoneChangeResult{}, err
		}
	}
	s.users.mu.Unlock()
	return domain.PhoneChangeResult{User: u, Event: event, Changed: true}, nil
}
