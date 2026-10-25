package storetest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"telesrv/internal/domain"
)

type botPreCheckoutPending struct {
	botUserID int64
	userID    int64
	answer    *domain.BotPreCheckoutAnswer
}

// BotPreCheckoutRegistryStore is an in-process store.BotPreCheckoutRegistryStore
// for tests; production wires the Redis implementation.
type BotPreCheckoutRegistryStore struct {
	mu      sync.Mutex
	pending map[int64]*botPreCheckoutPending
	waiters map[int64][]chan struct{}
}

func NewBotPreCheckoutRegistryStore() *BotPreCheckoutRegistryStore {
	return &BotPreCheckoutRegistryStore{
		pending: make(map[int64]*botPreCheckoutPending),
		waiters: make(map[int64][]chan struct{}),
	}
}

func (s *BotPreCheckoutRegistryStore) PutBotPreCheckoutPending(_ context.Context, queryID, botUserID, userID int64, ttl time.Duration) (bool, error) {
	if s == nil || queryID == 0 || botUserID <= 0 || userID <= 0 || ttl <= 0 {
		return false, fmt.Errorf("invalid bot pre-checkout pending")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.pending[queryID]; exists {
		return false, nil
	}
	s.pending[queryID] = &botPreCheckoutPending{botUserID: botUserID, userID: userID}
	return true, nil
}

func (s *BotPreCheckoutRegistryStore) ResolveBotPreCheckout(_ context.Context, botUserID, queryID int64, answer domain.BotPreCheckoutAnswer) (bool, error) {
	if s == nil || botUserID <= 0 || queryID == 0 {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, exists := s.pending[queryID]
	if !exists || pending.botUserID != botUserID || pending.answer != nil {
		return false, nil
	}
	pending.answer = &answer
	for _, ch := range s.waiters[queryID] {
		close(ch)
	}
	delete(s.waiters, queryID)
	return true, nil
}

func (s *BotPreCheckoutRegistryStore) WaitBotPreCheckoutAnswer(ctx context.Context, botUserID, queryID int64) (domain.BotPreCheckoutAnswer, bool, error) {
	if s == nil || botUserID <= 0 || queryID == 0 {
		return domain.BotPreCheckoutAnswer{}, false, nil
	}
	for {
		s.mu.Lock()
		pending, exists := s.pending[queryID]
		if !exists || pending.botUserID != botUserID {
			s.mu.Unlock()
			return domain.BotPreCheckoutAnswer{}, false, nil
		}
		if pending.answer != nil {
			answer := *pending.answer
			s.mu.Unlock()
			return answer, true, nil
		}
		ch := make(chan struct{})
		s.waiters[queryID] = append(s.waiters[queryID], ch)
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return domain.BotPreCheckoutAnswer{}, false, nil
		case <-ch:
		}
	}
}

func (s *BotPreCheckoutRegistryStore) TakeBotPreCheckout(_ context.Context, botUserID, queryID int64) (domain.BotPreCheckoutAnswer, bool, error) {
	if s == nil || botUserID <= 0 || queryID == 0 {
		return domain.BotPreCheckoutAnswer{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, exists := s.pending[queryID]
	if !exists || pending.botUserID != botUserID {
		return domain.BotPreCheckoutAnswer{}, false, nil
	}
	delete(s.pending, queryID)
	for _, ch := range s.waiters[queryID] {
		close(ch)
	}
	delete(s.waiters, queryID)
	if pending.answer == nil {
		return domain.BotPreCheckoutAnswer{}, false, nil
	}
	return *pending.answer, true, nil
}

// Pending reports the live query ids, for tests asserting a bot was asked.
func (s *BotPreCheckoutRegistryStore) Pending() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, 0, len(s.pending))
	for queryID := range s.pending {
		out = append(out, queryID)
	}
	return out
}
