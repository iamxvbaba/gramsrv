package memory

import (
	"context"
	"sync"

	"telesrv/internal/domain"
)

type ephemeralReportKey struct {
	reporterUserID int64
	channelID      int64
	messageID      int
	option         string
	commentHash    [32]byte
}

// EphemeralReportStore is the deterministic in-memory test implementation.
type EphemeralReportStore struct {
	mu      sync.Mutex
	reports map[ephemeralReportKey]domain.EphemeralAbuseReport
}

func NewEphemeralReportStore() *EphemeralReportStore {
	return &EphemeralReportStore{reports: make(map[ephemeralReportKey]domain.EphemeralAbuseReport)}
}

func (s *EphemeralReportStore) CreateEphemeralReport(_ context.Context, report domain.EphemeralAbuseReport) (bool, error) {
	if err := report.Validate(); err != nil {
		return false, err
	}
	key := ephemeralReportKey{
		reporterUserID: report.ReporterUserID, channelID: report.Evidence.Peer.ID,
		messageID: report.Evidence.MessageID, option: report.Option, commentHash: report.CommentHash,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.reports[key]; exists {
		return false, nil
	}
	s.reports[key] = report
	return true, nil
}

func (s *EphemeralReportStore) Reports() []domain.EphemeralAbuseReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.EphemeralAbuseReport, 0, len(s.reports))
	for _, report := range s.reports {
		out = append(out, report)
	}
	return out
}
