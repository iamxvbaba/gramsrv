package memory

import (
	"context"
	"sync"

	"telesrv/internal/domain"
)

// WidgetStore is the in-memory WidgetStore used by rpc tests.
type WidgetStore struct {
	mu      sync.Mutex
	nextID  int64
	widgets map[int64]domain.BotWidget
}

func NewWidgetStore() *WidgetStore {
	return &WidgetStore{widgets: make(map[int64]domain.BotWidget)}
}

func cloneBotWidget(w domain.BotWidget) domain.BotWidget {
	w.Payload = append([]byte(nil), w.Payload...)
	return w
}

func (s *WidgetStore) CreateWidget(_ context.Context, widget domain.BotWidget) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextID == 0 {
		s.nextID = 1
	}
	for s.widgets[s.nextID].WidgetID != 0 {
		s.nextID++
	}
	id := s.nextID
	s.nextID++
	widget.WidgetID = id
	s.widgets[id] = cloneBotWidget(widget)
	return id, nil
}

func (s *WidgetStore) GetWidget(_ context.Context, widgetID int64) (domain.BotWidget, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	widget, ok := s.widgets[widgetID]
	if !ok {
		return domain.BotWidget{}, false, nil
	}
	return cloneBotWidget(widget), true, nil
}

func (s *WidgetStore) SetWidgetPublicData(_ context.Context, widgetID int64, publicData string, hash int64, cacheTime int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	widget, ok := s.widgets[widgetID]
	if !ok {
		return domain.ErrWidgetNotFound
	}
	widget.PublicData = publicData
	widget.PublicDataHash = hash
	widget.CacheTime = cacheTime
	s.widgets[widgetID] = widget
	return nil
}
