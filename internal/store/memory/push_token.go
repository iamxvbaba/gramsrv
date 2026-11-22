package memory

import (
	"context"
	"sort"
	"sync"

	"telesrv/internal/domain"
)

// PushTokenStore is the in-memory PushTokenStore used by rpc tests.
type PushTokenStore struct {
	mu     sync.Mutex
	tokens map[pushTokenKey]domain.PushToken
}

type pushTokenKey struct {
	tokenType int
	token     string
}

func NewPushTokenStore() *PushTokenStore {
	return &PushTokenStore{tokens: make(map[pushTokenKey]domain.PushToken)}
}

func clonePushToken(t domain.PushToken) domain.PushToken {
	t.Secret = append([]byte(nil), t.Secret...)
	t.OtherUIDs = append([]int64(nil), t.OtherUIDs...)
	return t
}

func (s *PushTokenStore) UpsertPushToken(_ context.Context, token domain.PushToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[pushTokenKey{token.TokenType, token.Token}] = clonePushToken(token)
	return nil
}

func (s *PushTokenStore) DeletePushToken(_ context.Context, userID int64, tokenType int, token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pushTokenKey{tokenType, token}
	stored, ok := s.tokens[key]
	if !ok || stored.UserID != userID {
		return false, nil
	}
	delete(s.tokens, key)
	return true, nil
}

func (s *PushTokenStore) DeletePushTokenByValue(_ context.Context, tokenType int, token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := pushTokenKey{tokenType, token}
	if _, ok := s.tokens[key]; !ok {
		return false, nil
	}
	delete(s.tokens, key)
	return true, nil
}

func (s *PushTokenStore) ListPushTokensForUser(_ context.Context, userID int64) ([]domain.PushToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.PushToken, 0)
	for _, token := range s.tokens {
		if token.UserID == userID {
			out = append(out, clonePushToken(token))
		}
	}
	sortPushTokens(out)
	return out, nil
}

func (s *PushTokenStore) ListPushTokensForUsers(_ context.Context, userIDs []int64) (map[int64][]domain.PushToken, error) {
	wanted := make(map[int64]struct{}, len(userIDs))
	for _, id := range userIDs {
		wanted[id] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64][]domain.PushToken)
	for _, token := range s.tokens {
		if _, ok := wanted[token.UserID]; !ok {
			continue
		}
		out[token.UserID] = append(out[token.UserID], clonePushToken(token))
	}
	for _, list := range out {
		sortPushTokens(list)
	}
	return out, nil
}

// sortPushTokens gives list results a stable order for tests.
func sortPushTokens(tokens []domain.PushToken) {
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].TokenType != tokens[j].TokenType {
			return tokens[i].TokenType < tokens[j].TokenType
		}
		return tokens[i].Token < tokens[j].Token
	})
}
