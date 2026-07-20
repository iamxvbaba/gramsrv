package memory

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// BotAPIUpdateStore is an in-memory implementation of store.BotAPIUpdateStore.
type BotAPIUpdateStore struct {
	mu                sync.RWMutex
	nextID            int64
	rows              []domain.BotAPIUpdate
	state             map[int64]int64
	cursorInitialized map[int64]bool
	allowed           map[int64]map[domain.BotAPIUpdateKind]struct{}
	byKey             map[string]int64
	pollLeases        map[int64]botAPIPollLease
	webhooks          map[int64]domain.BotAPIWebhook
	webhookLeases     map[int64]botAPIPollLease
}

type botAPIPollLease struct {
	owner     string
	expiresAt time.Time
}

// NewBotAPIUpdateStore creates an in-memory Bot API update queue.
func NewBotAPIUpdateStore() *BotAPIUpdateStore {
	return &BotAPIUpdateStore{
		nextID:            1,
		state:             make(map[int64]int64),
		cursorInitialized: make(map[int64]bool),
		allowed:           make(map[int64]map[domain.BotAPIUpdateKind]struct{}),
		byKey:             make(map[string]int64),
		pollLeases:        make(map[int64]botAPIPollLease),
		webhooks:          make(map[int64]domain.BotAPIWebhook),
		webhookLeases:     make(map[int64]botAPIPollLease),
	}
}

func (s *BotAPIUpdateStore) SetBotAPIWebhook(_ context.Context, config domain.BotAPIWebhook, dropPending bool) error {
	if config.BotUserID <= 0 || config.URL == "" || config.MaxConnections < 1 || config.MaxConnections > 100 {
		return fmt.Errorf("invalid bot api webhook")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !config.AllowedUpdatesSet {
		config.AllowedUpdates = allowedUpdateKinds(s.allowed[config.BotUserID])
	}
	config.AllowedUpdates = append([]domain.BotAPIUpdateKind(nil), config.AllowedUpdates...)
	config.FailureCount, config.LastErrorDate, config.LastErrorMessage = 0, 0, ""
	config.NextAttemptAt = time.Now()
	s.webhooks[config.BotUserID] = config
	if config.AllowedUpdates == nil {
		delete(s.allowed, config.BotUserID)
	} else {
		allowed := make(map[domain.BotAPIUpdateKind]struct{}, len(config.AllowedUpdates))
		for _, kind := range config.AllowedUpdates {
			allowed[kind] = struct{}{}
		}
		s.allowed[config.BotUserID] = allowed
	}
	if dropPending {
		s.dropPendingLocked(config.BotUserID)
	}
	return nil
}

func allowedUpdateKinds(items map[domain.BotAPIUpdateKind]struct{}) []domain.BotAPIUpdateKind {
	if len(items) == 0 {
		return nil
	}
	out := make([]domain.BotAPIUpdateKind, 0, len(items))
	for kind := range items {
		out = append(out, kind)
	}
	slices.Sort(out)
	return out
}

func (s *BotAPIUpdateStore) DeleteBotAPIWebhook(_ context.Context, botUserID int64, dropPending bool) error {
	s.mu.Lock()
	delete(s.webhooks, botUserID)
	delete(s.webhookLeases, botUserID)
	if dropPending {
		s.dropPendingLocked(botUserID)
	}
	s.mu.Unlock()
	return nil
}

func (s *BotAPIUpdateStore) BotAPIWebhook(_ context.Context, botUserID int64) (domain.BotAPIWebhook, bool, error) {
	s.mu.RLock()
	config, found := s.webhooks[botUserID]
	s.mu.RUnlock()
	config.AllowedUpdates = append([]domain.BotAPIUpdateKind(nil), config.AllowedUpdates...)
	return config, found, nil
}

func (s *BotAPIUpdateStore) ListDueBotAPIWebhooks(_ context.Context, limit int) ([]domain.BotAPIWebhook, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	now := time.Now()
	s.mu.RLock()
	out := make([]domain.BotAPIWebhook, 0, min(limit, len(s.webhooks)))
	for botID, config := range s.webhooks {
		lease := s.webhookLeases[botID]
		if config.NextAttemptAt.After(now) || (lease.owner != "" && lease.expiresAt.After(now)) {
			continue
		}
		config.AllowedUpdates = append([]domain.BotAPIUpdateKind(nil), config.AllowedUpdates...)
		out = append(out, config)
		if len(out) == limit {
			break
		}
	}
	s.mu.RUnlock()
	return out, nil
}

func (s *BotAPIUpdateStore) AcquireBotAPIWebhookLease(_ context.Context, botUserID int64, owner string, ttl time.Duration) (bool, error) {
	if botUserID <= 0 || owner == "" || ttl <= 0 {
		return false, fmt.Errorf("invalid bot api webhook lease")
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, found := s.webhooks[botUserID]; !found {
		return false, nil
	}
	current := s.webhookLeases[botUserID]
	if current.owner != "" && current.owner != owner && current.expiresAt.After(now) {
		return false, nil
	}
	s.webhookLeases[botUserID] = botAPIPollLease{owner: owner, expiresAt: now.Add(ttl)}
	return true, nil
}

func (s *BotAPIUpdateStore) ReleaseBotAPIWebhookLease(_ context.Context, botUserID int64, owner string) error {
	s.mu.Lock()
	if current := s.webhookLeases[botUserID]; current.owner == owner {
		delete(s.webhookLeases, botUserID)
	}
	s.mu.Unlock()
	return nil
}

func (s *BotAPIUpdateStore) RecordBotAPIWebhookFailure(_ context.Context, botUserID int64, owner string, nextAttempt time.Time, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.webhookLeases[botUserID]; current.owner != owner {
		return nil
	}
	config, found := s.webhooks[botUserID]
	if !found {
		delete(s.webhookLeases, botUserID)
		return nil
	}
	config.FailureCount++
	config.LastErrorDate = int(time.Now().Unix())
	if len(message) > 512 {
		message = message[:512]
	}
	config.LastErrorMessage = message
	config.NextAttemptAt = nextAttempt
	s.webhooks[botUserID] = config
	delete(s.webhookLeases, botUserID)
	return nil
}

func (s *BotAPIUpdateStore) RecordBotAPIWebhookSuccess(_ context.Context, botUserID int64, owner string, nextAttempt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.webhookLeases[botUserID]; current.owner != owner {
		return nil
	}
	if config, found := s.webhooks[botUserID]; found {
		config.FailureCount, config.LastErrorDate, config.LastErrorMessage = 0, 0, ""
		config.NextAttemptAt = nextAttempt
		s.webhooks[botUserID] = config
	}
	delete(s.webhookLeases, botUserID)
	return nil
}

func (s *BotAPIUpdateStore) dropPendingLocked(botUserID int64) {
	for _, row := range s.rows {
		if row.BotUserID == botUserID && row.ID > s.state[botUserID] {
			s.state[botUserID] = row.ID
		}
	}
	s.cursorInitialized[botUserID] = true
}

func (s *BotAPIUpdateStore) AcquireBotAPIPollLease(_ context.Context, botUserID int64, owner string, ttl time.Duration) (bool, error) {
	if botUserID <= 0 || owner == "" || ttl <= 0 {
		return false, fmt.Errorf("invalid bot api poll lease")
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.pollLeases[botUserID]
	if found && current.owner != owner && current.expiresAt.After(now) {
		return false, nil
	}
	s.pollLeases[botUserID] = botAPIPollLease{owner: owner, expiresAt: now.Add(ttl)}
	return true, nil
}

func (s *BotAPIUpdateStore) ReleaseBotAPIPollLease(_ context.Context, botUserID int64, owner string) error {
	if botUserID <= 0 || owner == "" {
		return nil
	}
	s.mu.Lock()
	if current, found := s.pollLeases[botUserID]; found && current.owner == owner {
		delete(s.pollLeases, botUserID)
	}
	s.mu.Unlock()
	return nil
}

func (s *BotAPIUpdateStore) EnqueueBotAPIUpdate(_ context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error) {
	if err := validateBotAPIUpdateRequest(req); err != nil {
		return domain.BotAPIUpdate{}, false, err
	}
	key := botAPIUpdateKey(req)
	s.mu.Lock()
	defer s.mu.Unlock()
	if allowed, configured := s.allowed[req.BotUserID]; configured {
		if _, ok := allowed[req.Kind]; !ok {
			return domain.BotAPIUpdate{}, false, nil
		}
	}
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
		Callback:  cloneBotAPICallback(req.Callback),
		Ephemeral: cloneBotAPIEphemeral(req.Ephemeral),
	}
	s.nextID++
	s.rows = append(s.rows, row)
	s.byKey[key] = row.ID
	if config, found := s.webhooks[req.BotUserID]; found {
		config.NextAttemptAt = time.Now()
		s.webhooks[req.BotUserID] = config
	}
	return cloneBotAPIUpdate(row), true, nil
}

func (s *BotAPIUpdateStore) ListTailBotAPIUpdates(_ context.Context, botUserID int64, tail, limit int) ([]domain.BotAPIUpdate, error) {
	if botUserID == 0 || tail <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	confirmed := s.state[botUserID]
	matching := make([]domain.BotAPIUpdate, 0, min(tail, limit))
	start := 0
	count := 0
	for _, row := range s.rows {
		if row.BotUserID == botUserID && row.ID > confirmed {
			count++
		}
	}
	if count > tail {
		start = count - tail
	}
	seen := 0
	for _, row := range s.rows {
		if row.BotUserID != botUserID || row.ID <= confirmed {
			continue
		}
		if seen < start {
			seen++
			continue
		}
		matching = append(matching, cloneBotAPIUpdate(row))
		if len(matching) >= limit {
			break
		}
	}
	return matching, nil
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
	maxExisting := int64(0)
	for _, row := range s.rows {
		if row.BotUserID == botUserID && row.ID > maxExisting {
			maxExisting = row.ID
		}
	}
	if confirmedUpdateID > maxExisting && s.cursorInitialized[botUserID] {
		s.mu.Unlock()
		return nil
	}
	if confirmedUpdateID > maxExisting {
		confirmedUpdateID = maxExisting
	}
	if confirmedUpdateID > s.state[botUserID] {
		s.state[botUserID] = confirmedUpdateID
	}
	s.cursorInitialized[botUserID] = true
	s.mu.Unlock()
	return nil
}

func (s *BotAPIUpdateStore) SetBotAPIAllowedUpdates(_ context.Context, botUserID int64, allowed []domain.BotAPIUpdateKind) error {
	if botUserID == 0 {
		return nil
	}
	s.mu.Lock()
	if len(allowed) == 0 {
		delete(s.allowed, botUserID)
	} else {
		set := make(map[domain.BotAPIUpdateKind]struct{}, len(allowed))
		for _, kind := range allowed {
			if kind != "" {
				set[kind] = struct{}{}
			}
		}
		s.allowed[botUserID] = set
	}
	s.mu.Unlock()
	return nil
}

func (s *BotAPIUpdateStore) DropPendingBotAPIUpdates(ctx context.Context, botUserID int64) error {
	if botUserID == 0 {
		return nil
	}
	s.mu.Lock()
	for _, row := range s.rows {
		if row.BotUserID == botUserID && row.ID > s.state[botUserID] {
			s.state[botUserID] = row.ID
		}
	}
	s.cursorInitialized[botUserID] = true
	s.mu.Unlock()
	return nil
}

func (s *BotAPIUpdateStore) PendingBotAPIUpdateCount(_ context.Context, botUserID int64) (int, error) {
	if botUserID == 0 {
		return 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	confirmed := s.state[botUserID]
	count := 0
	for _, row := range s.rows {
		if row.BotUserID == botUserID && row.ID > confirmed {
			count++
		}
	}
	return count, nil
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
	if req.BotUserID == 0 {
		return fmt.Errorf("invalid bot api update")
	}
	if req.Kind != domain.BotAPIUpdateMessage && req.Kind != domain.BotAPIUpdateEditedMessage && req.Kind != domain.BotAPIUpdateCallbackQuery {
		return fmt.Errorf("invalid bot api update kind %q", req.Kind)
	}
	switch req.Peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		if req.Peer.ID <= 0 || req.MessageID <= 0 {
			return fmt.Errorf("invalid bot api update peer")
		}
	case "":
		if req.Kind != domain.BotAPIUpdateCallbackQuery || req.Peer.ID != 0 || req.MessageID != 0 {
			return fmt.Errorf("invalid bot api update peer")
		}
	default:
		return fmt.Errorf("invalid bot api update peer type %q", req.Peer.Type)
	}
	if req.Ephemeral != nil {
		message := req.Ephemeral.Message
		if req.Ephemeral.Validate() != nil || message.ID != req.MessageID || message.Peer != req.Peer || message.Expired(time.Unix(int64(req.Date), 0)) ||
			req.Peer.Type != domain.PeerTypeChannel || req.SourcePts != 0 {
			return fmt.Errorf("invalid bot api ephemeral update")
		}
		if (req.Kind == domain.BotAPIUpdateCallbackQuery && message.SenderUserID != req.BotUserID) ||
			(req.Kind != domain.BotAPIUpdateCallbackQuery && message.ReceiverUserID != req.BotUserID) {
			return fmt.Errorf("invalid bot api ephemeral target")
		}
	}
	if req.Kind == domain.BotAPIUpdateCallbackQuery {
		cb := req.Callback
		if cb == nil || cb.ID == 0 || cb.BotUserID != req.BotUserID || cb.UserID <= 0 ||
			cb.Peer != req.Peer || cb.MessageID != req.MessageID || cb.ChatInstance == 0 ||
			len(cb.Data) > domain.MaxCallbackDataLen || req.SourcePts != 0 {
			return fmt.Errorf("invalid bot api callback query")
		}
		inline := cb.InlineMessage
		if req.MessageID == 0 && (inline == nil || inline.DCID <= 0 || inline.OwnerID <= 0 || inline.ID <= 0 || inline.AccessHash == 0) {
			return fmt.Errorf("invalid bot api inline callback query")
		}
		if req.MessageID > 0 && inline != nil {
			return fmt.Errorf("ambiguous bot api callback query")
		}
	} else if req.Callback != nil {
		return fmt.Errorf("unexpected bot api callback query")
	}
	return nil
}

func botAPIUpdateKey(req domain.EnqueueBotAPIUpdateRequest) string {
	if req.Kind == domain.BotAPIUpdateCallbackQuery && req.Callback != nil {
		return fmt.Sprintf("%d:%s:%d", req.BotUserID, req.Kind, req.Callback.ID)
	}
	if req.Ephemeral != nil {
		return fmt.Sprintf("%d:%s:ephemeral:%s:%d:%d:%d", req.BotUserID, req.Kind, req.Peer.Type, req.Peer.ID, req.MessageID, req.Ephemeral.Message.Version)
	}
	return fmt.Sprintf("%d:%s:%s:%d:%d:%d", req.BotUserID, req.Kind, req.Peer.Type, req.Peer.ID, req.MessageID, req.SourcePts)
}

func cloneBotAPIUpdate(row domain.BotAPIUpdate) domain.BotAPIUpdate {
	row.Callback = cloneBotAPICallback(row.Callback)
	row.Ephemeral = cloneBotAPIEphemeral(row.Ephemeral)
	return row
}

func cloneBotAPIEphemeral(in *domain.BotAPIEphemeralPayload) *domain.BotAPIEphemeralPayload {
	if in == nil {
		return nil
	}
	return domain.NewBotAPIEphemeralPayload(cloneEphemeralMessage(in.EphemeralMessage()))
}

func cloneBotAPICallback(in *domain.BotCallbackQuery) *domain.BotCallbackQuery {
	if in == nil {
		return nil
	}
	out := *in
	out.Data = append([]byte(nil), in.Data...)
	if in.InlineMessage != nil {
		inline := *in.InlineMessage
		out.InlineMessage = &inline
	}
	return &out
}
