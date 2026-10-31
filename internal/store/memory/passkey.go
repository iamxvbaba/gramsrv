package memory

import (
	"context"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// PasskeyStore is the in-memory implementation of store.PasskeyStore.
type PasskeyStore struct {
	mu   sync.RWMutex
	byID map[string]domain.PasskeyCredential // key = string(credentialID)
}

// NewPasskeyStore creates an in-memory passkey credential store.
func NewPasskeyStore() *PasskeyStore {
	return &PasskeyStore{byID: make(map[string]domain.PasskeyCredential)}
}

func (s *PasskeyStore) InsertPasskey(_ context.Context, cred domain.PasskeyCredential) error {
	if len(cred.CredentialID) == 0 || cred.UserID == 0 {
		return domain.ErrPasskeyInvalid
	}
	s.mu.Lock()
	s.byID[string(cred.CredentialID)] = cred.Clone()
	s.mu.Unlock()
	return nil
}

func (s *PasskeyStore) GetPasskeyByCredentialID(_ context.Context, credentialID []byte) (domain.PasskeyCredential, bool, error) {
	s.mu.RLock()
	cred, ok := s.byID[string(credentialID)]
	s.mu.RUnlock()
	if !ok {
		return domain.PasskeyCredential{}, false, nil
	}
	return cred.Clone(), true, nil
}

func (s *PasskeyStore) ListPasskeysByUser(_ context.Context, userID int64) ([]domain.PasskeyCredential, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.PasskeyCredential, 0)
	for _, cred := range s.byID {
		if cred.UserID == userID {
			out = append(out, cred.Clone())
		}
	}
	return out, nil
}

func (s *PasskeyStore) UpdatePasskeyUsage(_ context.Context, credentialID []byte, signCount uint32, lastUsedAt int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cred, ok := s.byID[string(credentialID)]
	if !ok {
		return domain.ErrPasskeyNotFound
	}
	cred.SignCount = signCount
	cred.LastUsedAt = lastUsedAt
	s.byID[string(credentialID)] = cred
	return nil
}

func (s *PasskeyStore) DeletePasskey(_ context.Context, userID int64, credentialID []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cred, ok := s.byID[string(credentialID)]
	if !ok || cred.UserID != userID {
		return false, nil
	}
	delete(s.byID, string(credentialID))
	return true, nil
}

// PasskeyChallengeStore is the in-memory implementation of store.PasskeyChallengeStore
// (in-process, short TTL). Like QR login tokens, it is an in-process one-shot credential and does not span instances.
type PasskeyChallengeStore struct {
	mu sync.Mutex
	m  map[string]challengeEntry // key = string(challenge)
}

type challengeEntry struct {
	c         domain.PasskeyChallenge
	expiresAt time.Time
}

// NewPasskeyChallengeStore creates an in-memory challenge store.
func NewPasskeyChallengeStore() *PasskeyChallengeStore {
	return &PasskeyChallengeStore{m: make(map[string]challengeEntry)}
}

func (s *PasskeyChallengeStore) SavePasskeyChallenge(_ context.Context, challenge []byte, c domain.PasskeyChallenge, ttl time.Duration) error {
	if len(challenge) == 0 {
		return domain.ErrPasskeyChallengeInvalid
	}
	s.mu.Lock()
	s.m[string(challenge)] = challengeEntry{c: c, expiresAt: time.Now().Add(ttl)}
	s.mu.Unlock()
	return nil
}

func (s *PasskeyChallengeStore) ConsumePasskeyChallenge(_ context.Context, challenge []byte) (domain.PasskeyChallenge, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.m[string(challenge)]
	if !ok {
		return domain.PasskeyChallenge{}, false, nil
	}
	delete(s.m, string(challenge)) // one-shot: delete regardless of expiry
	if time.Now().After(entry.expiresAt) {
		return domain.PasskeyChallenge{}, false, nil
	}
	return entry.c, true, nil
}
