package memory

import (
	"context"

	"telesrv/internal/store"
)

func (s *CodeStore) GetSnapshot(_ context.Context, hash string) (store.PhoneCodeSnapshot, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.liveCodeLocked(hash)
	if !found {
		return store.PhoneCodeSnapshot{}, false, nil
	}
	if entry.code.Version != store.PhoneCodeVersionCurrent || entry.code.Revision == "" {
		s.deleteCodeLocked(hash, entry.code)
		return store.PhoneCodeSnapshot{}, false, nil
	}
	return store.PhoneCodeSnapshot{Record: entry.code, Revision: entry.code.Revision}, true, nil
}

func (s *CodeStore) CompareAndUpdate(_ context.Context, hash, expectedRevision string, next store.PhoneCode) (bool, error) {
	if expectedRevision == "" || next.Version != store.PhoneCodeVersionCurrent || next.Purpose != "" {
		return false, nil
	}
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return false, err
	}
	next.Revision = revision

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.liveCodeLocked(hash)
	if !found {
		return false, nil
	}
	if entry.code.Version != store.PhoneCodeVersionCurrent || entry.code.Revision == "" {
		s.deleteCodeLocked(hash, entry.code)
		return false, nil
	}
	if entry.code.Purpose != "" || entry.code.Revision != expectedRevision {
		return false, nil
	}
	entry.code = next
	s.m[hash] = entry
	return true, nil
}

func (s *CodeStore) CompareAndDelete(_ context.Context, hash, expectedRevision string) (bool, error) {
	if expectedRevision == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.liveCodeLocked(hash)
	if !found {
		return false, nil
	}
	if entry.code.Version != store.PhoneCodeVersionCurrent || entry.code.Revision == "" {
		s.deleteCodeLocked(hash, entry.code)
		return false, nil
	}
	if entry.code.Purpose != "" || entry.code.Revision != expectedRevision {
		return false, nil
	}
	s.deleteCodeLocked(hash, entry.code)
	return true, nil
}
