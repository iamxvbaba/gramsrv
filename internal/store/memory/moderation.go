package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// ModerationReportStore is the deterministic in-memory implementation used by
// RPC and app tests. Returned reports are deep copies so accepted evidence
// cannot be mutated through a caller-owned byte slice.
type ModerationReportStore struct {
	mu                 sync.RWMutex
	nextID             int64
	byID               map[int64]domain.ModerationReport
	byFingerprint      map[[32]byte]int64
	nextCaseID         int64
	cases              map[int64]domain.ModerationCase
	activeCases        map[domain.Peer]int64
	caseReports        map[int64][]int64
	nextDecisionID     int64
	decisions          map[int64][]domain.ModerationDecision
	decisionCommands   map[string]domain.ModerationDecision
	nextActionID       int64
	actions            map[int64]domain.ModerationAction
	nextAppealID       int64
	appeals            map[int64]domain.ModerationAppeal
	appealFingerprints map[[32]byte]int64
	nextAppealLinkID   int64
	appealLinks        map[[32]byte]domain.ModerationAppealLink
	nextSponsoredID    int64
	sponsored          map[memorySponsoredKey]domain.SponsoredMessageImpression
	nextAntiSpamID     int64
	antiSpam           map[memoryAntiSpamKey]domain.ChannelAntiSpamDecision
}

type memorySponsoredKey struct {
	UserID       int64
	RandomIDHash [32]byte
}

type memoryAntiSpamKey struct {
	ChannelID int64
	MessageID int
}

func NewModerationReportStore() *ModerationReportStore {
	return &ModerationReportStore{
		nextID:        1,
		byID:          make(map[int64]domain.ModerationReport),
		byFingerprint: make(map[[32]byte]int64),
		nextCaseID:    1, cases: make(map[int64]domain.ModerationCase),
		activeCases:    make(map[domain.Peer]int64),
		caseReports:    make(map[int64][]int64),
		nextDecisionID: 1, decisions: make(map[int64][]domain.ModerationDecision),
		decisionCommands: make(map[string]domain.ModerationDecision),
		nextActionID:     1, actions: make(map[int64]domain.ModerationAction),
		nextAppealID: 1, appeals: make(map[int64]domain.ModerationAppeal),
		appealFingerprints: make(map[[32]byte]int64),
		nextAppealLinkID:   1,
		appealLinks:        make(map[[32]byte]domain.ModerationAppealLink),
		nextSponsoredID:    1,
		sponsored:          make(map[memorySponsoredKey]domain.SponsoredMessageImpression),
		nextAntiSpamID:     1,
		antiSpam:           make(map[memoryAntiSpamKey]domain.ChannelAntiSpamDecision),
	}
}

func (s *ModerationReportStore) CreateModerationReport(_ context.Context, report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	if err := report.Validate(); err != nil {
		return domain.ModerationReport{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createModerationReportLocked(report)
}

func (s *ModerationReportStore) createModerationReportLocked(report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	if id, exists := s.byFingerprint[report.Fingerprint]; exists {
		return domain.CloneModerationReport(s.byID[id]), false, nil
	}
	var hourly, daily int
	hourAgo := report.CreatedAt.Add(-time.Hour)
	dayAgo := report.CreatedAt.Add(-24 * time.Hour)
	for _, existing := range s.byID {
		if existing.ReporterUserID != report.ReporterUserID || existing.CreatedAt.After(report.CreatedAt) {
			continue
		}
		if !existing.CreatedAt.Before(dayAgo) {
			daily++
		}
		if !existing.CreatedAt.Before(hourAgo) {
			hourly++
		}
	}
	if hourly >= domain.MaxModerationReportsPerHour || daily >= domain.MaxModerationReportsPerDay {
		return domain.ModerationReport{}, false, domain.ErrModerationRateLimited
	}
	report.ID = s.nextID
	s.nextID++
	stored := domain.CloneModerationReport(report)
	s.byID[report.ID] = stored
	s.byFingerprint[report.Fingerprint] = report.ID
	s.attachReportToCaseLocked(stored)
	return domain.CloneModerationReport(stored), true, nil
}

func (s *ModerationReportStore) attachReportToCaseLocked(report domain.ModerationReport) {
	caseID, ok := s.activeCases[report.Target]
	if !ok {
		item := domain.ModerationCase{
			ID: s.nextCaseID, Target: report.Target,
			Status:   domain.ModerationCaseOpen,
			Severity: domain.ModerationSeverityForReason(report.Reason),
			Version:  1, ReportCount: 1, DistinctReporterCount: 1,
			FirstReportAt: report.CreatedAt, LastReportAt: report.CreatedAt,
			CreatedAt: report.CreatedAt, UpdatedAt: report.CreatedAt,
		}
		s.nextCaseID++
		s.cases[item.ID] = item
		s.activeCases[item.Target] = item.ID
		s.caseReports[item.ID] = []int64{report.ID}
		return
	}
	item := s.cases[caseID]
	s.caseReports[caseID] = append(s.caseReports[caseID], report.ID)
	item.ReportCount = len(s.caseReports[caseID])
	reporters := make(map[int64]struct{}, item.ReportCount)
	for _, reportID := range s.caseReports[caseID] {
		reporters[s.byID[reportID].ReporterUserID] = struct{}{}
	}
	item.DistinctReporterCount = len(reporters)
	severity := domain.ModerationSeverityForReason(report.Reason)
	if severity > item.Severity {
		item.Severity = severity
	}
	if report.CreatedAt.Before(item.FirstReportAt) {
		item.FirstReportAt = report.CreatedAt
	}
	if report.CreatedAt.After(item.LastReportAt) {
		item.LastReportAt = report.CreatedAt
	}
	if report.CreatedAt.After(item.UpdatedAt) {
		item.UpdatedAt = report.CreatedAt
	}
	item.Version++
	s.cases[caseID] = item
}

func (s *ModerationReportStore) CreateSponsoredMessageImpression(_ context.Context, impression domain.SponsoredMessageImpression) (domain.SponsoredMessageImpression, bool, error) {
	if err := impression.Validate(); err != nil || impression.ID != 0 ||
		impression.ReportID != 0 {
		return domain.SponsoredMessageImpression{}, false, domain.ErrModerationReportInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memorySponsoredKey{
		UserID: impression.UserID, RandomIDHash: impression.RandomIDHash,
	}
	if existing, found := s.sponsored[key]; found {
		return cloneSponsoredImpression(existing), false, nil
	}
	impression.ID = s.nextSponsoredID
	s.nextSponsoredID++
	s.sponsored[key] = cloneSponsoredImpression(impression)
	return cloneSponsoredImpression(impression), true, nil
}

func (s *ModerationReportStore) GetSponsoredMessageImpression(_ context.Context, userID int64, randomIDHash [32]byte, now time.Time) (domain.SponsoredMessageImpression, bool, error) {
	if userID <= 0 || randomIDHash == ([32]byte{}) || now.IsZero() {
		return domain.SponsoredMessageImpression{}, false, domain.ErrModerationReportInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	impression, found := s.sponsored[memorySponsoredKey{
		UserID: userID, RandomIDHash: randomIDHash,
	}]
	if !found || !impression.ExpiresAt.After(now) {
		return domain.SponsoredMessageImpression{}, false, nil
	}
	return cloneSponsoredImpression(impression), true, nil
}

func (s *ModerationReportStore) CreateSponsoredModerationReport(_ context.Context, impressionID int64, report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	if impressionID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, impression := range s.sponsored {
		if impression.ID != impressionID {
			continue
		}
		if err := domain.ValidateSponsoredModerationReport(impression, report); err != nil {
			return domain.ModerationReport{}, false, err
		}
		if impression.ReportID != 0 {
			existing, found := s.byID[impression.ReportID]
			if !found {
				return domain.ModerationReport{}, false, domain.ErrModerationReportNotFound
			}
			return domain.CloneModerationReport(existing), false, nil
		}
		stored, created, err := s.createModerationReportLocked(report)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		impression.ReportID = stored.ID
		s.sponsored[key] = impression
		return stored, created, nil
	}
	return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
}

func (s *ModerationReportStore) CreateChannelAntiSpamDecision(_ context.Context, decision domain.ChannelAntiSpamDecision) (domain.ChannelAntiSpamDecision, bool, error) {
	if err := decision.Validate(); err != nil || decision.ID != 0 ||
		decision.ReportID != 0 {
		return domain.ChannelAntiSpamDecision{}, false, domain.ErrModerationReportInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := memoryAntiSpamKey{ChannelID: decision.ChannelID, MessageID: decision.MessageID}
	if existing, found := s.antiSpam[key]; found {
		return cloneAntiSpamDecision(existing), false, nil
	}
	decision.ID = s.nextAntiSpamID
	s.nextAntiSpamID++
	s.antiSpam[key] = cloneAntiSpamDecision(decision)
	return cloneAntiSpamDecision(decision), true, nil
}

func (s *ModerationReportStore) GetChannelAntiSpamDecision(_ context.Context, channelID int64, messageID int) (domain.ChannelAntiSpamDecision, bool, error) {
	if channelID <= 0 || messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return domain.ChannelAntiSpamDecision{}, false, domain.ErrModerationReportInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	decision, found := s.antiSpam[memoryAntiSpamKey{
		ChannelID: channelID, MessageID: messageID,
	}]
	return cloneAntiSpamDecision(decision), found, nil
}

func (s *ModerationReportStore) CreateAntiSpamFalsePositiveReport(_ context.Context, decisionID int64, report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	if decisionID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, decision := range s.antiSpam {
		if decision.ID != decisionID {
			continue
		}
		if err := domain.ValidateAntiSpamFalsePositiveReport(decision, report); err != nil {
			return domain.ModerationReport{}, false, err
		}
		if decision.ReportID != 0 {
			existing, found := s.byID[decision.ReportID]
			if !found {
				return domain.ModerationReport{}, false, domain.ErrModerationReportNotFound
			}
			return domain.CloneModerationReport(existing), false, nil
		}
		stored, created, err := s.createModerationReportLocked(report)
		if err != nil {
			return domain.ModerationReport{}, false, err
		}
		decision.ReportID = stored.ID
		s.antiSpam[key] = decision
		return stored, created, nil
	}
	return domain.ModerationReport{}, false, domain.ErrModerationEvidenceNotFound
}

func (s *ModerationReportStore) DeleteExpiredSponsoredMessageImpressions(_ context.Context, olderThan time.Time, limit int) (int, error) {
	if olderThan.IsZero() || limit <= 0 || limit > 10000 {
		return 0, domain.ErrModerationReportInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for key, impression := range s.sponsored {
		if deleted >= limit {
			break
		}
		if impression.ExpiresAt.Before(olderThan) {
			delete(s.sponsored, key)
			deleted++
		}
	}
	return deleted, nil
}

func (s *ModerationReportStore) GetModerationReport(_ context.Context, reportID int64) (domain.ModerationReport, bool, error) {
	if reportID <= 0 {
		return domain.ModerationReport{}, false, domain.ErrModerationReportInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	report, ok := s.byID[reportID]
	if !ok {
		return domain.ModerationReport{}, false, nil
	}
	return domain.CloneModerationReport(report), true, nil
}

func (s *ModerationReportStore) Reports() []domain.ModerationReport {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ModerationReport, 0, len(s.byID))
	for _, report := range s.byID {
		out = append(out, domain.CloneModerationReport(report))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *ModerationReportStore) ListModerationCases(_ context.Context, filter domain.ModerationCaseFilter) ([]domain.ModerationCase, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	statuses := make(map[domain.ModerationCaseStatus]struct{}, len(filter.Statuses))
	for _, status := range filter.Statuses {
		statuses[status] = struct{}{}
	}
	out := make([]domain.ModerationCase, 0, filter.Limit)
	for _, item := range s.cases {
		if len(statuses) > 0 {
			if _, ok := statuses[item.Status]; !ok {
				continue
			}
		}
		if filter.AssignedTo != "" && item.AssignedTo != filter.AssignedTo {
			continue
		}
		if filter.Target.ID != 0 && item.Target != filter.Target {
			continue
		}
		if !filter.BeforeUpdate.IsZero() &&
			(!item.UpdatedAt.Before(filter.BeforeUpdate) &&
				(item.UpdatedAt != filter.BeforeUpdate || item.ID >= filter.BeforeID)) {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *ModerationReportStore) GetModerationCase(_ context.Context, caseID int64) (domain.ModerationCaseDetail, bool, error) {
	if caseID <= 0 {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.caseDetailLocked(caseID)
}

func (s *ModerationReportStore) caseDetailLocked(caseID int64) (domain.ModerationCaseDetail, bool, error) {
	item, ok := s.cases[caseID]
	if !ok {
		return domain.ModerationCaseDetail{}, false, nil
	}
	detail := domain.ModerationCaseDetail{
		Case: item,
	}
	for i := len(s.caseReports[caseID]) - 1; i >= 0 &&
		len(detail.ReportIDs) < domain.MaxModerationCaseDetailEntries; i-- {
		detail.ReportIDs = append(detail.ReportIDs, s.caseReports[caseID][i])
	}
	for i := len(s.decisions[caseID]) - 1; i >= 0 &&
		len(detail.Decisions) < domain.MaxModerationCaseDetailEntries; i-- {
		detail.Decisions = append(detail.Decisions, s.decisions[caseID][i])
	}
	for _, action := range s.actions {
		if action.CaseID == caseID {
			detail.Actions = append(detail.Actions, cloneModerationAction(action))
		}
	}
	sort.Slice(detail.Actions, func(i, j int) bool { return detail.Actions[i].ID > detail.Actions[j].ID })
	if len(detail.Actions) > domain.MaxModerationCaseDetailEntries {
		detail.Actions = detail.Actions[:domain.MaxModerationCaseDetailEntries]
	}
	for _, appeal := range s.appeals {
		if appeal.CaseID == caseID {
			detail.Appeals = append(detail.Appeals, appeal)
		}
	}
	sort.Slice(detail.Appeals, func(i, j int) bool { return detail.Appeals[i].ID > detail.Appeals[j].ID })
	if len(detail.Appeals) > domain.MaxModerationCaseDetailEntries {
		detail.Appeals = detail.Appeals[:domain.MaxModerationCaseDetailEntries]
	}
	return detail, true, nil
}

func (s *ModerationReportStore) ClaimModerationCase(_ context.Context, caseID, expectedVersion int64, actor string, now time.Time) (domain.ModerationCase, error) {
	if caseID <= 0 || expectedVersion <= 0 || actor == "" ||
		len(actor) > domain.MaxModerationActorBytes || now.IsZero() {
		return domain.ModerationCase{}, domain.ErrModerationCaseInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.cases[caseID]
	if !ok || item.Version != expectedVersion ||
		(item.Status != domain.ModerationCaseOpen &&
			item.Status != domain.ModerationCaseInReview &&
			item.Status != domain.ModerationCaseAppealReview) ||
		(item.AssignedTo != "" && item.AssignedTo != actor) {
		return domain.ModerationCase{}, domain.ErrModerationCaseConflict
	}
	if item.Status != domain.ModerationCaseAppealReview {
		item.Status = domain.ModerationCaseInReview
	}
	item.AssignedTo = actor
	item.Version++
	if now.After(item.UpdatedAt) {
		item.UpdatedAt = now
	}
	s.cases[caseID] = item
	return item, nil
}

func (s *ModerationReportStore) DecideModerationCase(_ context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error) {
	if err := request.Validate(); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if request.AppealID != 0 {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.decisionCommands[request.CommandID]; ok {
		if existing.CaseID != request.CaseID ||
			existing.Fingerprint != request.Fingerprint {
			return domain.ModerationCaseDetail{}, false, domain.ErrModerationActionConflict
		}
		detail, found, err := s.caseDetailLocked(existing.CaseID)
		return detail, false, requireMemoryCase(found, err)
	}
	item, ok := s.cases[request.CaseID]
	if !ok {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseNotFound
	}
	if item.Version != request.ExpectedVersion ||
		(item.Status != domain.ModerationCaseInReview &&
			!(item.Status == domain.ModerationCaseActionFailed &&
				request.Kind == domain.ModerationDecisionViolation)) ||
		item.AssignedTo != request.Actor ||
		!memoryModerationActionsValid(item.Target, request.Actions) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseConflict
	}
	decision := domain.ModerationDecision{
		ID: s.nextDecisionID, CaseID: request.CaseID, AppealID: request.AppealID,
		Kind:  request.Kind,
		Actor: request.Actor, Reason: request.Reason,
		CommandID: request.CommandID, Fingerprint: request.Fingerprint,
		CreatedAt: request.CreatedAt,
	}
	s.nextDecisionID++
	s.decisions[item.ID] = append(s.decisions[item.ID], decision)
	s.decisionCommands[decision.CommandID] = decision
	for i, draft := range request.Actions {
		action := domain.ModerationAction{
			ID: s.nextActionID, CaseID: item.ID, DecisionID: decision.ID,
			Kind: draft.Kind, Payload: append([]byte(nil), draft.Payload...),
			Status: domain.ModerationActionPending, AvailableAt: request.CreatedAt,
			CommandID: fmt.Sprintf("%s:%03d", request.CommandID, i),
			CreatedAt: request.CreatedAt, UpdatedAt: request.CreatedAt,
		}
		s.nextActionID++
		s.actions[action.ID] = action
	}
	delete(s.activeCases, item.Target)
	if request.Kind == domain.ModerationDecisionViolation {
		item.Status = domain.ModerationCaseActionPending
	} else {
		item.Status = domain.ModerationCaseDismissed
		item.AssignedTo = ""
	}
	item.Version++
	if request.CreatedAt.After(item.UpdatedAt) {
		item.UpdatedAt = request.CreatedAt
	}
	s.cases[item.ID] = item
	detail, _, err := s.caseDetailLocked(item.ID)
	return detail, true, err
}

func (s *ModerationReportStore) ReviewModerationAppeal(_ context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error) {
	if err := request.Validate(); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if request.AppealID <= 0 ||
		(request.Kind != domain.ModerationDecisionAppealGrant &&
			request.Kind != domain.ModerationDecisionAppealDeny) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.decisionCommands[request.CommandID]; ok {
		if existing.CaseID != request.CaseID ||
			existing.Fingerprint != request.Fingerprint {
			return domain.ModerationCaseDetail{}, false, domain.ErrModerationActionConflict
		}
		detail, found, err := s.caseDetailLocked(existing.CaseID)
		return detail, false, requireMemoryCase(found, err)
	}
	item, ok := s.cases[request.CaseID]
	if !ok {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseNotFound
	}
	appeal, ok := s.appeals[request.AppealID]
	if !ok || appeal.CaseID != item.ID {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseNotFound
	}
	if item.Version != request.ExpectedVersion ||
		item.Status != domain.ModerationCaseAppealReview ||
		item.AssignedTo != request.Actor ||
		appeal.Status != domain.ModerationAppealPending ||
		!memoryModerationActionsValid(item.Target, request.Actions) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseConflict
	}
	if request.Kind == domain.ModerationDecisionAppealGrant &&
		!s.memoryCaseOwnsRemedySanctionsLocked(item, request.Actions) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationActionConflict
	}
	decision := domain.ModerationDecision{
		ID: s.nextDecisionID, CaseID: request.CaseID, AppealID: request.AppealID,
		Kind: request.Kind, Actor: request.Actor, Reason: request.Reason,
		CommandID: request.CommandID, Fingerprint: request.Fingerprint,
		CreatedAt: request.CreatedAt,
	}
	s.nextDecisionID++
	s.decisions[item.ID] = append(s.decisions[item.ID], decision)
	s.decisionCommands[decision.CommandID] = decision
	for i, draft := range request.Actions {
		action := domain.ModerationAction{
			ID: s.nextActionID, CaseID: item.ID, DecisionID: decision.ID,
			Kind: draft.Kind, Payload: append([]byte(nil), draft.Payload...),
			Status: domain.ModerationActionPending, AvailableAt: request.CreatedAt,
			CommandID: fmt.Sprintf("%s:%03d", request.CommandID, i),
			CreatedAt: request.CreatedAt, UpdatedAt: request.CreatedAt,
		}
		s.nextActionID++
		s.actions[action.ID] = action
	}
	appeal.Reviewer = request.Actor
	appeal.ReviewReason = request.Reason
	appeal.ReviewedAt = request.CreatedAt
	if request.Kind == domain.ModerationDecisionAppealGrant {
		appeal.Status = domain.ModerationAppealGranted
		item.Status = domain.ModerationCaseDismissed
		if len(request.Actions) > 0 {
			item.Status = domain.ModerationCaseActionPending
		}
	} else {
		appeal.Status = domain.ModerationAppealRejected
		item.Status = appeal.PreviousCaseStatus
	}
	if item.Status != domain.ModerationCaseActionPending {
		item.AssignedTo = ""
	}
	item.Version++
	if request.CreatedAt.After(item.UpdatedAt) {
		item.UpdatedAt = request.CreatedAt
	}
	s.appeals[appeal.ID] = appeal
	s.cases[item.ID] = item
	detail, _, err := s.caseDetailLocked(item.ID)
	return detail, true, err
}

func (s *ModerationReportStore) CreateModerationAppeal(_ context.Context, appeal domain.ModerationAppeal) (domain.ModerationAppeal, bool, error) {
	if err := appeal.Validate(); err != nil || appeal.ID != 0 ||
		appeal.Status != domain.ModerationAppealPending {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.appealFingerprints[appeal.Fingerprint]; ok {
		return s.appeals[id], false, nil
	}
	item, ok := s.cases[appeal.CaseID]
	if !ok {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseNotFound
	}
	if item.Status != domain.ModerationCaseResolved &&
		item.Status != domain.ModerationCaseDismissed {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseConflict
	}
	if item.Target.Type == domain.PeerTypeUser &&
		item.Target.ID != appeal.AppellantUserID {
		return domain.ModerationAppeal{}, false, domain.ErrModerationPermissionDenied
	}
	for _, existing := range s.appeals {
		if existing.CaseID == appeal.CaseID &&
			existing.AppellantUserID == appeal.AppellantUserID &&
			existing.Status == domain.ModerationAppealPending {
			return domain.ModerationAppeal{}, false, domain.ErrModerationCaseConflict
		}
	}
	appeal.ID = s.nextAppealID
	s.nextAppealID++
	s.appeals[appeal.ID] = appeal
	s.appealFingerprints[appeal.Fingerprint] = appeal.ID
	item.Status = domain.ModerationCaseAppealReview
	item.AssignedTo = ""
	item.Version++
	if appeal.CreatedAt.After(item.UpdatedAt) {
		item.UpdatedAt = appeal.CreatedAt
	}
	s.cases[item.ID] = item
	return appeal, true, nil
}

func (s *ModerationReportStore) GetModerationAppeal(_ context.Context, appealID int64) (domain.ModerationAppeal, bool, error) {
	if appealID <= 0 {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	appeal, found := s.appeals[appealID]
	return appeal, found, nil
}

func (s *ModerationReportStore) IssueModerationAppealLink(_ context.Context, link domain.ModerationAppealLink) (domain.ModerationAppealLink, error) {
	if err := link.Validate(); err != nil || link.ID != 0 ||
		link.AppealID != 0 || !link.ConsumedAt.IsZero() {
		return domain.ModerationAppealLink{}, domain.ErrModerationAppealLinkInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.appealLinks[link.TokenHash]; exists {
		return domain.ModerationAppealLink{}, domain.ErrModerationActionConflict
	}
	item, ok := s.cases[link.CaseID]
	if !ok {
		return domain.ModerationAppealLink{}, domain.ErrModerationCaseNotFound
	}
	if item.Target.Type != domain.PeerTypeUser ||
		item.Target.ID != link.AppellantUserID {
		return domain.ModerationAppealLink{}, domain.ErrModerationPermissionDenied
	}
	if item.Status != domain.ModerationCaseActionPending &&
		item.Status != domain.ModerationCaseResolved {
		return domain.ModerationAppealLink{}, domain.ErrModerationCaseConflict
	}
	var caseLinks int
	for _, existing := range s.appealLinks {
		if existing.CaseID == link.CaseID {
			caseLinks++
		}
	}
	if caseLinks >= domain.MaxModerationAppealLinksPerCase {
		return domain.ModerationAppealLink{}, domain.ErrModerationActionConflict
	}
	link.ID = s.nextAppealLinkID
	s.nextAppealLinkID++
	s.appealLinks[link.TokenHash] = link
	return link, nil
}

func (s *ModerationReportStore) GetModerationAppealLink(_ context.Context, tokenHash [32]byte, now time.Time) (domain.ModerationAppealLink, bool, error) {
	if tokenHash == ([32]byte{}) || now.IsZero() {
		return domain.ModerationAppealLink{}, false, domain.ErrModerationAppealLinkInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	link, found := s.appealLinks[tokenHash]
	if !found || !link.ExpiresAt.After(now) {
		return domain.ModerationAppealLink{}, false, nil
	}
	return link, true, nil
}

func (s *ModerationReportStore) SubmitModerationAppealByLink(_ context.Context, tokenHash [32]byte, text string, now time.Time) (domain.ModerationAppeal, bool, error) {
	if tokenHash == ([32]byte{}) || now.IsZero() {
		return domain.ModerationAppeal{}, false, domain.ErrModerationAppealLinkInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	link, found := s.appealLinks[tokenHash]
	if !found || !link.ExpiresAt.After(now) {
		return domain.ModerationAppeal{}, false, domain.ErrModerationAppealLinkInvalid
	}
	if link.AppealID > 0 {
		appeal, ok := s.appeals[link.AppealID]
		if !ok {
			return domain.ModerationAppeal{}, false, domain.ErrModerationCaseInvalid
		}
		return appeal, false, nil
	}
	item, ok := s.cases[link.CaseID]
	if !ok {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseNotFound
	}
	if item.Target.Type != domain.PeerTypeUser ||
		item.Target.ID != link.AppellantUserID {
		return domain.ModerationAppeal{}, false, domain.ErrModerationPermissionDenied
	}
	for _, existing := range s.appeals {
		if existing.CaseID == link.CaseID &&
			existing.AppellantUserID == link.AppellantUserID &&
			existing.Status == domain.ModerationAppealPending {
			link.AppealID = existing.ID
			link.ConsumedAt = now
			s.appealLinks[tokenHash] = link
			return existing, false, nil
		}
	}
	if item.Status != domain.ModerationCaseResolved &&
		item.Status != domain.ModerationCaseDismissed {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseConflict
	}
	appeal, err := domain.NewModerationAppeal(
		item.ID, link.AppellantUserID, item.Status, text, now,
	)
	if err != nil {
		return domain.ModerationAppeal{}, false, err
	}
	appeal.ID = s.nextAppealID
	s.nextAppealID++
	s.appeals[appeal.ID] = appeal
	s.appealFingerprints[appeal.Fingerprint] = appeal.ID
	item.Status = domain.ModerationCaseAppealReview
	item.AssignedTo = ""
	item.Version++
	if now.After(item.UpdatedAt) {
		item.UpdatedAt = now
	}
	s.cases[item.ID] = item
	link.AppealID = appeal.ID
	link.ConsumedAt = now
	s.appealLinks[tokenHash] = link
	return appeal, true, nil
}

func (s *ModerationReportStore) DeleteExpiredModerationAppealLinks(_ context.Context, olderThan time.Time, limit int) (int, error) {
	if olderThan.IsZero() || limit <= 0 || limit > 10000 {
		return 0, domain.ErrModerationAppealLinkInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for tokenHash, link := range s.appealLinks {
		if deleted >= limit {
			break
		}
		if link.ExpiresAt.Before(olderThan) {
			delete(s.appealLinks, tokenHash)
			deleted++
		}
	}
	return deleted, nil
}

func (s *ModerationReportStore) ClaimModerationActions(_ context.Context, now time.Time, limit int, lease time.Duration) ([]domain.ModerationAction, error) {
	if now.IsZero() || limit <= 0 || limit > 100 || lease <= 0 {
		return nil, domain.ErrModerationActionInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidates := make([]domain.ModerationAction, 0)
	for _, action := range s.actions {
		if action.Attempts >= domain.MaxModerationActionAttempts {
			continue
		}
		if (action.Status == domain.ModerationActionPending ||
			action.Status == domain.ModerationActionRetry) &&
			!action.AvailableAt.After(now) ||
			action.Status == domain.ModerationActionProcessing &&
				!action.LeaseUntil.After(now) {
			candidates = append(candidates, action)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].AvailableAt.Equal(candidates[j].AvailableAt) {
			return candidates[i].AvailableAt.Before(candidates[j].AvailableAt)
		}
		return candidates[i].ID < candidates[j].ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	for i := range candidates {
		action := candidates[i]
		action.Status = domain.ModerationActionProcessing
		action.Attempts++
		action.LeaseUntil = now.Add(lease)
		action.UpdatedAt = now
		s.actions[action.ID] = action
		candidates[i] = cloneModerationAction(action)
	}
	return candidates, nil
}

func (s *ModerationReportStore) IsModerationActionCurrent(_ context.Context, action domain.ModerationAction) (bool, error) {
	if action.ID <= 0 || action.CaseID <= 0 {
		return false, domain.ErrModerationActionInvalid
	}
	family, scoped := action.Kind.SanctionFamily()
	if !scoped {
		return true, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.actions[action.ID]
	if !ok || item.CaseID != action.CaseID || item.Kind != action.Kind {
		return false, domain.ErrModerationActionConflict
	}
	targetCase, ok := s.cases[action.CaseID]
	if !ok {
		return false, domain.ErrModerationCaseNotFound
	}
	latest, found := s.latestSanctionActionLocked(targetCase.Target, family)
	return found && latest.ID == action.ID, nil
}

func (s *ModerationReportStore) SupersedeModerationAction(_ context.Context, actionID int64, expectedAttempts int, now time.Time) error {
	if actionID <= 0 || expectedAttempts <= 0 || now.IsZero() {
		return domain.ErrModerationActionInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	action, ok := s.actions[actionID]
	if !ok || action.Status != domain.ModerationActionProcessing ||
		action.Attempts != expectedAttempts {
		return domain.ErrModerationActionConflict
	}
	action.Status = domain.ModerationActionSuperseded
	action.LeaseUntil = time.Time{}
	action.LastError = ""
	action.UpdatedAt = now
	s.actions[action.ID] = action
	s.finishModerationDecisionLocked(action, now)
	return nil
}

func (s *ModerationReportStore) CompleteModerationAction(_ context.Context, actionID int64, expectedAttempts int, succeeded bool, errorText string, retryAt, now time.Time) error {
	if actionID <= 0 || expectedAttempts <= 0 || len(errorText) > 4000 ||
		now.IsZero() || (!succeeded && retryAt.Before(now)) {
		return domain.ErrModerationActionInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	action, ok := s.actions[actionID]
	if !ok || action.Status != domain.ModerationActionProcessing ||
		action.Attempts != expectedAttempts {
		return domain.ErrModerationActionConflict
	}
	action.LeaseUntil = time.Time{}
	action.UpdatedAt = now
	if succeeded {
		action.Status = domain.ModerationActionSucceeded
		action.LastError = ""
	} else {
		action.LastError = errorText
		action.AvailableAt = retryAt
		action.Status = domain.ModerationActionRetry
		if action.Attempts >= domain.MaxModerationActionAttempts {
			action.Status = domain.ModerationActionFailed
		}
	}
	s.actions[action.ID] = action
	item := s.cases[action.CaseID]
	if action.Status == domain.ModerationActionFailed {
		item.Status = domain.ModerationCaseActionFailed
		item.Version++
		item.UpdatedAt = now
		s.cases[item.ID] = item
		return nil
	}
	if !succeeded {
		return nil
	}
	s.finishModerationDecisionLocked(action, now)
	return nil
}

func (s *ModerationReportStore) finishModerationDecisionLocked(action domain.ModerationAction, now time.Time) {
	allTerminal := true
	for _, candidate := range s.actions {
		if candidate.DecisionID == action.DecisionID &&
			candidate.Status != domain.ModerationActionSucceeded &&
			candidate.Status != domain.ModerationActionSuperseded {
			allTerminal = false
			break
		}
	}
	item := s.cases[action.CaseID]
	if allTerminal && item.Status == domain.ModerationCaseActionPending {
		item.Status = domain.ModerationCaseResolved
		for _, decision := range s.decisions[item.ID] {
			if decision.ID == action.DecisionID &&
				decision.Kind == domain.ModerationDecisionAppealGrant {
				item.Status = domain.ModerationCaseDismissed
				break
			}
		}
		item.AssignedTo = ""
		item.Version++
		item.UpdatedAt = now
		s.cases[item.ID] = item
	}
}

func (s *ModerationReportStore) latestSanctionActionLocked(target domain.Peer, family domain.ModerationSanctionFamily) (domain.ModerationAction, bool) {
	var latest domain.ModerationAction
	for _, action := range s.actions {
		item, ok := s.cases[action.CaseID]
		if !ok || item.Target != target {
			continue
		}
		actionFamily, scoped := action.Kind.SanctionFamily()
		if !scoped || actionFamily != family || action.ID <= latest.ID {
			continue
		}
		latest = action
	}
	return latest, latest.ID > 0
}

func (s *ModerationReportStore) memoryCaseOwnsRemedySanctionsLocked(item domain.ModerationCase, actions []domain.ModerationActionDraft) bool {
	for _, action := range actions {
		family, scoped := action.Kind.SanctionFamily()
		if !scoped {
			continue
		}
		latest, found := s.latestSanctionActionLocked(item.Target, family)
		if !found || latest.CaseID != item.ID {
			return false
		}
		switch action.Kind {
		case domain.ModerationActionClearPeerFlags:
			if latest.Kind != domain.ModerationActionMarkScam &&
				latest.Kind != domain.ModerationActionMarkFake {
				return false
			}
		case domain.ModerationActionUnfreezeAccount:
			if latest.Kind != domain.ModerationActionFreezeAccount {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func memoryModerationActionsValid(target domain.Peer, actions []domain.ModerationActionDraft) bool {
	for _, action := range actions {
		switch action.Kind {
		case domain.ModerationActionMarkScam, domain.ModerationActionMarkFake,
			domain.ModerationActionClearPeerFlags:
		case domain.ModerationActionFreezeAccount,
			domain.ModerationActionUnfreezeAccount,
			domain.ModerationActionDeletePrivateMessage,
			domain.ModerationActionDeleteAccount:
			if target.Type != domain.PeerTypeUser {
				return false
			}
		case domain.ModerationActionDeleteChannelMessage:
			if target.Type != domain.PeerTypeChannel {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func cloneModerationAction(action domain.ModerationAction) domain.ModerationAction {
	action.Payload = append([]byte(nil), action.Payload...)
	return action
}

func cloneSponsoredImpression(impression domain.SponsoredMessageImpression) domain.SponsoredMessageImpression {
	impression.Evidence = append([]byte(nil), impression.Evidence...)
	return impression
}

func cloneAntiSpamDecision(decision domain.ChannelAntiSpamDecision) domain.ChannelAntiSpamDecision {
	decision.Evidence = append([]byte(nil), decision.Evidence...)
	return decision
}

func requireMemoryCase(found bool, err error) error {
	if err != nil {
		return err
	}
	if !found {
		return domain.ErrModerationCaseNotFound
	}
	return nil
}
