package moderation

import (
	"context"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service owns moderation submission invariants. RPC handlers provide
// domain-only snapshots; the service canonicalizes and persists them before a
// client may observe a successful report response.
type Service struct {
	reports         store.ModerationReportStore
	cases           store.ModerationCaseStore
	registry        store.ModerationEvidenceRegistryStore
	privateMessages privateMessageReader
	channelMessages channelMessageReader
	stories         storyReader
	users           userReader
	channels        channelPeerReader
	photos          profilePhotoReader
}

type Option func(*Service)

func WithMessageReaders(private privateMessageReader, channels channelMessageReader) Option {
	return func(service *Service) {
		service.privateMessages = private
		service.channelMessages = channels
	}
}

func WithStoryReader(stories storyReader) Option {
	return func(service *Service) {
		service.stories = stories
	}
}

func WithPeerReaders(users userReader, channels channelPeerReader) Option {
	return func(service *Service) {
		service.users = users
		service.channels = channels
	}
}

func WithProfilePhotoReader(photos profilePhotoReader) Option {
	return func(service *Service) {
		service.photos = photos
	}
}

func NewService(reports store.ModerationReportStore, opts ...Option) *Service {
	service := &Service{reports: reports}
	if cases, ok := reports.(store.ModerationCaseStore); ok {
		service.cases = cases
	}
	if registry, ok := reports.(store.ModerationEvidenceRegistryStore); ok {
		service.registry = registry
	}
	for _, opt := range opts {
		if opt != nil {
			opt(service)
		}
	}
	return service
}

func (s *Service) AcceptReport(ctx context.Context, draft domain.ModerationReportDraft) (domain.ModerationReport, bool, error) {
	if s == nil || s.reports == nil {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store is not configured")
	}
	report, err := domain.NewModerationReport(draft)
	if err != nil {
		return domain.ModerationReport{}, false, err
	}
	return s.reports.CreateModerationReport(ctx, report)
}

func (s *Service) Report(ctx context.Context, reportID int64) (domain.ModerationReport, bool, error) {
	if s == nil || s.reports == nil {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation report store is not configured")
	}
	if reportID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	return s.reports.GetModerationReport(ctx, reportID)
}
