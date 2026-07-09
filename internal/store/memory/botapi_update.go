package memory

import (
	"context"
	"fmt"
	"sync"

	"telesrv/internal/domain"
)

// BotAPIUpdateStore is an in-memory implementation of store.BotAPIUpdateStore.
type BotAPIUpdateStore struct {
	mu     sync.RWMutex
	nextID int64
	rows   []domain.BotAPIUpdate
	state  map[int64]int64
	byKey  map[string]int64
}

// NewBotAPIUpdateStore creates an in-memory Bot API update queue.
func NewBotAPIUpdateStore() *BotAPIUpdateStore {
	return &BotAPIUpdateStore{
		nextID: 1,
		state:  make(map[int64]int64),
		byKey:  make(map[string]int64),
	}
}

func (s *BotAPIUpdateStore) EnqueueBotAPIUpdate(_ context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error) {
	if err := validateBotAPIUpdateRequest(req); err != nil {
		return domain.BotAPIUpdate{}, false, err
	}
	key := botAPIUpdateKey(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingID, ok := s.byKey[key]; ok {
		for _, row := range s.rows {
			if row.ID == existingID {
				return cloneBotAPIUpdate(row), false, nil
			}
		}
	}
	row := domain.BotAPIUpdate{
		ID:        s.nextID,
		BotUserID: req.BotUserID,
		Kind:      req.Kind,
		Peer:      req.Peer,
		MessageID: req.MessageID,
		SourcePts: req.SourcePts,
		Date:      req.Date,
	}
	s.nextID++
	s.rows = append(s.rows, row)
	s.byKey[key] = row.ID
	return cloneBotAPIUpdate(row), true, nil
}

func (s *BotAPIUpdateStore) ListBotAPIUpdates(_ context.Context, botUserID, fromUpdateID int64, limit int) ([]domain.BotAPIUpdate, error) {
	if botUserID == 0 {
		return nil, nil
	}
	if fromUpdateID <= 0 {
		fromUpdateID = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.BotAPIUpdate, 0, limit)
	for _, row := range s.rows {
		if row.BotUserID != botUserID || row.ID < fromUpdateID {
			continue
		}
		out = append(out, cloneBotAPIUpdate(row))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *BotAPIUpdateStore) ConfirmBotAPIUpdates(_ context.Context, botUserID, confirmedUpdateID int64) error {
	if botUserID == 0 || confirmedUpdateID <= 0 {
		return nil
	}
	s.mu.Lock()
	if confirmedUpdateID > s.state[botUserID] {
		s.state[botUserID] = confirmedUpdateID
	}
	s.mu.Unlock()
	return nil
}

func (s *BotAPIUpdateStore) ConfirmedBotAPIUpdateID(_ context.Context, botUserID int64) (int64, bool, error) {
	if botUserID == 0 {
		return 0, false, nil
	}
	s.mu.RLock()
	id, ok := s.state[botUserID]
	s.mu.RUnlock()
	return id, ok, nil
}

func validateBotAPIUpdateRequest(req domain.EnqueueBotAPIUpdateRequest) error {
	if req.BotUserID == 0 || req.MessageID <= 0 {
		return fmt.Errorf("invalid bot api update")
	}
	if req.Kind != domain.BotAPIUpdateMessage && req.Kind != domain.BotAPIUpdateEditedMessage {
		return fmt.Errorf("invalid bot api update kind %q", req.Kind)
	}
	switch req.Peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		if req.Peer.ID <= 0 {
			return fmt.Errorf("invalid bot api update peer")
		}
	default:
		return fmt.Errorf("invalid bot api update peer type %q", req.Peer.Type)
	}
	return nil
}

func botAPIUpdateKey(req domain.EnqueueBotAPIUpdateRequest) string {
	return fmt.Sprintf("%d:%s:%s:%d:%d:%d", req.BotUserID, req.Kind, req.Peer.Type, req.Peer.ID, req.MessageID, req.SourcePts)
}

func cloneBotAPIUpdate(row domain.BotAPIUpdate) domain.BotAPIUpdate {
	return row
}
