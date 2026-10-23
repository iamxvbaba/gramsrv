package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// NoPaidMessagesTokenStore is the in-memory store for Layer 232
// free-messages links, used by isolated RPC tests.
type NoPaidMessagesTokenStore struct {
	mu      sync.Mutex
	active  map[noPaidTokenKey]domain.NoPaidMessagesToken
	byToken map[string]domain.NoPaidMessagesToken
}

type noPaidTokenKey struct {
	owner     int64
	temporary bool
}

func NewNoPaidMessagesTokenStore() *NoPaidMessagesTokenStore {
	return &NoPaidMessagesTokenStore{
		active:  make(map[noPaidTokenKey]domain.NoPaidMessagesToken),
		byToken: make(map[string]domain.NoPaidMessagesToken),
	}
}

var _ store.NoPaidMessagesTokenStore = (*NoPaidMessagesTokenStore)(nil)

func (s *NoPaidMessagesTokenStore) ExportNoPaidMessagesToken(_ context.Context, ownerUserID int64, temporary, revoke bool, candidate string, now time.Time) (domain.NoPaidMessagesToken, error) {
	if ownerUserID <= 0 || candidate == "" {
		return domain.NoPaidMessagesToken{}, fmt.Errorf("export no paid messages token: invalid owner or candidate")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := noPaidTokenKey{owner: ownerUserID, temporary: temporary}
	if current, ok := s.active[key]; ok {
		if !revoke && current.ActiveAt(now) {
			return current, nil
		}
		delete(s.byToken, current.Token)
		delete(s.active, key)
	}
	if _, taken := s.byToken[candidate]; taken {
		return domain.NoPaidMessagesToken{}, fmt.Errorf("export no paid messages token: duplicate token")
	}
	token := domain.NoPaidMessagesToken{OwnerUserID: ownerUserID, Token: candidate, Temporary: temporary, CreatedAt: now.UTC()}
	if temporary {
		token.ExpiresAt = now.Add(domain.NoPaidMessagesTokenTemporaryTTL).UTC()
	}
	s.active[key] = token
	s.byToken[candidate] = token
	return token, nil
}

func (s *NoPaidMessagesTokenStore) ResolveNoPaidMessagesToken(_ context.Context, ownerUserID int64, token string, now time.Time) (domain.NoPaidMessagesToken, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, ok := s.byToken[token]
	if !ok || found.OwnerUserID != ownerUserID || !found.ActiveAt(now) {
		return domain.NoPaidMessagesToken{}, false, nil
	}
	return found, true, nil
}
