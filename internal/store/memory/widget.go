package memory

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math"
	"sync"

	"telesrv/internal/domain"
)

// WidgetStore is the in-memory WidgetStore used by rpc tests.
type WidgetStore struct {
	mu      sync.Mutex
	widgets map[int64]domain.BotWidget
}

func NewWidgetStore() *WidgetStore {
	return &WidgetStore{widgets: make(map[int64]domain.BotWidget)}
}

// randomWidgetID returns an unpredictable non-zero widget id, matching the
// PostgreSQL store (ids are referenced by inputMediaWidgetID and must not be
// enumerable).
func randomWidgetID() (int64, error) {
	for i := 0; i < 8; i++ {
		var buf [8]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return 0, err
		}
		if id := int64(binary.LittleEndian.Uint64(buf[:]) & math.MaxInt64); id != 0 {
			return id, nil
		}
	}
	return 0, fmt.Errorf("random widget id")
}

func cloneBotWidget(w domain.BotWidget) domain.BotWidget {
	w.Payload = append([]byte(nil), w.Payload...)
	return w
}

func (s *WidgetStore) CreateWidget(_ context.Context, widget domain.BotWidget) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if widget.WidgetID != 0 {
		// Preassigned id (normally persisted by a committed send); keep an existing
		// definition rather than overwriting it.
		if _, exists := s.widgets[widget.WidgetID]; !exists {
			s.widgets[widget.WidgetID] = cloneBotWidget(widget)
		}
		return widget.WidgetID, nil
	}
	for attempt := 0; attempt < 32; attempt++ {
		id, err := randomWidgetID()
		if err != nil {
			return 0, err
		}
		if _, exists := s.widgets[id]; exists {
			continue
		}
		widget.WidgetID = id
		s.widgets[id] = cloneBotWidget(widget)
		return id, nil
	}
	return 0, fmt.Errorf("allocate widget id")
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
