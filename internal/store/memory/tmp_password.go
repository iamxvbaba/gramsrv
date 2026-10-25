package memory

import (
	"context"
	"sync"

	"telesrv/internal/domain"
)

// TmpPasswordStore is the in-memory store.TmpPasswordStore. It has no foreign
// key to authorizations, so a deleted authorization leaves its grant behind;
// the account service rejects such a grant through its authorization binding.
type TmpPasswordStore struct {
	mu     sync.RWMutex
	grants map[[8]byte]domain.TmpPasswordGrant
}

func NewTmpPasswordStore() *TmpPasswordStore {
	return &TmpPasswordStore{grants: make(map[[8]byte]domain.TmpPasswordGrant)}
}

func (s *TmpPasswordStore) PutTmpPassword(_ context.Context, grant domain.TmpPasswordGrant) error {
	s.mu.Lock()
	s.grants[grant.AuthKeyID] = grant
	s.mu.Unlock()
	return nil
}

func (s *TmpPasswordStore) TmpPasswordByAuthKey(_ context.Context, authKeyID [8]byte) (domain.TmpPasswordGrant, bool, error) {
	s.mu.RLock()
	grant, found := s.grants[authKeyID]
	s.mu.RUnlock()
	return grant, found, nil
}
