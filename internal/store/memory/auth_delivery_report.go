package memory

import (
	"context"
	"sync"
	"time"

	"telesrv/internal/domain"
)

type AuthDeliveryReportStore struct {
	mu            sync.Mutex
	nextID        int64
	byFingerprint map[[32]byte]domain.AuthDeliveryReport
}

func NewAuthDeliveryReportStore() *AuthDeliveryReportStore {
	return &AuthDeliveryReportStore{
		nextID: 1, byFingerprint: make(map[[32]byte]domain.AuthDeliveryReport),
	}
}

func (s *AuthDeliveryReportStore) CreateAuthDeliveryReport(_ context.Context, report domain.AuthDeliveryReport) (domain.AuthDeliveryReport, bool, error) {
	if err := report.Validate(); err != nil {
		return domain.AuthDeliveryReport{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byFingerprint[report.Fingerprint]; ok {
		return existing, false, nil
	}
	var hourly, phoneDaily int
	hourAgo := report.CreatedAt.Add(-time.Hour)
	dayAgo := report.CreatedAt.Add(-24 * time.Hour)
	for _, existing := range s.byFingerprint {
		if existing.CreatedAt.After(report.CreatedAt) {
			continue
		}
		if existing.AuthKeyID == report.AuthKeyID && !existing.CreatedAt.Before(hourAgo) {
			hourly++
		}
		if existing.PhoneHash == report.PhoneHash && !existing.CreatedAt.Before(dayAgo) {
			phoneDaily++
		}
	}
	if hourly >= domain.MaxAuthDeliveryReportsPerHour ||
		phoneDaily >= domain.MaxAuthDeliveryReportsPerPhoneDay {
		return domain.AuthDeliveryReport{}, false, domain.ErrAuthDeliveryRateLimited
	}
	report.ID = s.nextID
	s.nextID++
	s.byFingerprint[report.Fingerprint] = report
	return report, true, nil
}

func (s *AuthDeliveryReportStore) Reports() []domain.AuthDeliveryReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.AuthDeliveryReport, 0, len(s.byFingerprint))
	for _, report := range s.byFingerprint {
		out = append(out, report)
	}
	return out
}

func (s *AuthDeliveryReportStore) DeleteExpiredAuthDeliveryReports(_ context.Context, olderThan time.Time, limit int) (int, error) {
	if olderThan.IsZero() || limit <= 0 || limit > 10000 {
		return 0, domain.ErrAuthDeliveryReportInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for fingerprint, report := range s.byFingerprint {
		if deleted >= limit {
			break
		}
		if report.CreatedAt.Before(olderThan) {
			delete(s.byFingerprint, fingerprint)
			deleted++
		}
	}
	return deleted, nil
}
