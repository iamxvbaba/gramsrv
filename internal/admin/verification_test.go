package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	verificationapp "telesrv/internal/app/verification"
	"telesrv/internal/domain"
)

// Compile-time proof that the shipped use-case service satisfies the admin port.
// cmd/telesrv wires *verification.Service into Dependencies.Verification
// directly, so a drifting method set has to fail here rather than at integration
// time.
var _ VerificationService = (*verificationapp.Service)(nil)

type fakeVerificationService struct {
	app    domain.VerificationApplication
	target domain.VerificationTarget
	events []domain.VerificationApplicationEvent
	counts domain.VerificationStatusCounts

	appErr    error
	targetErr error
	decideErr error

	claimCalls   int
	approveCalls int
	rejectCalls  int
	revokeCalls  int

	claimed  domain.VerificationDecision
	approved domain.VerificationDecision
	rejected domain.VerificationDecision
	revoked  domain.VerificationRevocation
}

func (f *fakeVerificationService) List(context.Context, domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error) {
	return []domain.VerificationApplication{f.app}, nil
}

func (f *fakeVerificationService) Counts(context.Context) (domain.VerificationStatusCounts, error) {
	return f.counts, nil
}

func (f *fakeVerificationService) Events(context.Context, int64, int) ([]domain.VerificationApplicationEvent, error) {
	return f.events, nil
}

func (f *fakeVerificationService) Application(_ context.Context, applicationID int64) (domain.VerificationApplication, error) {
	if f.appErr != nil {
		return domain.VerificationApplication{}, f.appErr
	}
	if f.app.ID != applicationID {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return f.app, nil
}

func (f *fakeVerificationService) TargetSnapshot(context.Context, domain.VerificationTargetType, int64) (domain.VerificationTarget, error) {
	if f.targetErr != nil {
		return domain.VerificationTarget{}, f.targetErr
	}
	return f.target, nil
}

func (f *fakeVerificationService) Claim(_ context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, error) {
	f.claimCalls++
	f.claimed = decision
	if f.decideErr != nil {
		return domain.VerificationApplication{}, f.decideErr
	}
	f.app.Status = domain.VerificationStatusInReview
	f.app.ReviewerAdminID = decision.Reviewer
	f.app.Version++
	f.app.CorrelationID = decision.CorrelationID
	return f.app, nil
}

func (f *fakeVerificationService) Approve(_ context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, bool, error) {
	f.approveCalls++
	f.approved = decision
	if f.decideErr != nil {
		return domain.VerificationApplication{}, false, f.decideErr
	}
	f.app.Status = domain.VerificationStatusApproved
	f.app.ReviewerAdminID = decision.Reviewer
	f.app.InternalNote = decision.InternalNote
	f.app.Version++
	f.app.CorrelationID = decision.CorrelationID
	return f.app, true, nil
}

func (f *fakeVerificationService) Reject(_ context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, bool, error) {
	f.rejectCalls++
	f.rejected = decision
	if f.decideErr != nil {
		return domain.VerificationApplication{}, false, f.decideErr
	}
	f.app.Status = domain.VerificationStatusRejected
	f.app.ReviewerAdminID = decision.Reviewer
	f.app.DecisionReason = decision.Reason
	f.app.InternalNote = decision.InternalNote
	f.app.Version++
	f.app.CorrelationID = decision.CorrelationID
	return f.app, true, nil
}

func (f *fakeVerificationService) Revoke(_ context.Context, req domain.VerificationRevocation) (domain.VerificationApplication, bool, error) {
	f.revokeCalls++
	f.revoked = req
	if f.decideErr != nil {
		return domain.VerificationApplication{}, false, f.decideErr
	}
	f.target.Verified = false
	return f.app, true, nil
}

func submittedVerificationApplication() domain.VerificationApplication {
	return domain.VerificationApplication{
		ID:              77,
		ApplicantUserID: 1001,
		TargetType:      domain.VerificationTargetChannel,
		TargetID:        5005,
		TargetTitle:     "Example News",
		TargetUsername:  "examplenews",
		Category:        "media",
		Status:          domain.VerificationStatusSubmitted,
		SubmittedAt:     fixedNow(),
		Version:         3,
	}
}

func newVerificationFixture() (*Service, *fakeVerificationService, *memoryCommandRepo) {
	repo := newMemoryCommandRepo()
	verification := &fakeVerificationService{
		app: submittedVerificationApplication(),
		target: domain.VerificationTarget{
			Type: domain.VerificationTargetChannel, ID: 5005,
			Title: "Example News", Username: "examplenews", Eligible: true,
		},
	}
	svc := NewService(Dependencies{Commands: repo, Verification: verification, Now: fixedNow})
	return svc, verification, repo
}

func TestClaimVerificationDryRunExecuteAndIdempotentReplay(t *testing.T) {
	ctx := context.Background()
	svc, verification, repo := newVerificationFixture()

	dry, err := svc.ClaimVerification(ctx, ClaimVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "dry-claim", Actor: "alice", Reason: "picking up the queue", DryRun: true},
		ApplicationID: 77,
		Version:       3,
	})
	if err != nil {
		t.Fatalf("dry-run claim: %v", err)
	}
	if !dry.DryRun || dry.Status != string(domain.AdminCommandCompleted) || verification.claimCalls != 0 {
		t.Fatalf("dry-run result=%+v claimCalls=%d, want a completed dry run without mutation", dry, verification.claimCalls)
	}
	if dry.Details["previous_status"] != string(domain.VerificationStatusSubmitted) ||
		dry.Details["next_status"] != string(domain.VerificationStatusInReview) ||
		dry.Details["application_id"] != "77" || dry.Details["target_id"] != "5005" ||
		dry.Details["target_type"] != string(domain.VerificationTargetChannel) ||
		dry.Details["correlation_id"] != "dry-claim" {
		t.Fatalf("dry-run details=%+v, want the audit facts seeded before execution", dry.Details)
	}

	execReq := ClaimVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "exec-claim", Actor: "alice", Reason: "picking up the queue"},
		ApplicationID: 77,
		Version:       3,
	}
	exec, err := svc.ClaimVerification(ctx, execReq)
	if err != nil {
		t.Fatalf("execute claim: %v", err)
	}
	if verification.claimCalls != 1 || exec.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("execute result=%+v claimCalls=%d", exec, verification.claimCalls)
	}
	if exec.Details["status"] != string(domain.VerificationStatusInReview) ||
		exec.Details["reviewer_admin_id"] != "alice" || exec.Details["version"] != "4" {
		t.Fatalf("execute details=%+v", exec.Details)
	}
	// The command id is the correlation id, so the journal entry and the
	// application event can be matched up afterwards.
	if verification.claimed.CorrelationID != "exec-claim" || verification.claimed.Reviewer != "alice" {
		t.Fatalf("claim decision=%+v", verification.claimed)
	}
	if _, ok := repo.items["exec-claim"]; !ok {
		t.Fatal("claim was not journalled")
	}

	replay, err := svc.ClaimVerification(ctx, execReq)
	if err != nil {
		t.Fatalf("replay claim: %v", err)
	}
	if !replay.AlreadyExecuted || verification.claimCalls != 1 {
		t.Fatalf("replay result=%+v claimCalls=%d, want an idempotent replay", replay, verification.claimCalls)
	}
}

func TestClaimVerificationRefusesAlreadyClaimedApplication(t *testing.T) {
	ctx := context.Background()
	svc, verification, _ := newVerificationFixture()
	verification.app.Status = domain.VerificationStatusInReview

	_, err := svc.ClaimVerification(ctx, ClaimVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "claim-taken", Actor: "bob", Reason: "second reviewer"},
		ApplicationID: 77,
		Version:       3,
	})
	if !errors.Is(err, domain.ErrVerificationStatusInvalid) {
		t.Fatalf("claim of a claimed application err=%v, want ErrVerificationStatusInvalid", err)
	}
	if !strings.Contains(err.Error(), CodeVerificationStatusInvalid) || verification.claimCalls != 0 {
		t.Fatalf("err=%v claimCalls=%d, want the stable code and no mutation", err, verification.claimCalls)
	}
}

func TestApproveVerificationVersionConflictIsNotAMutation(t *testing.T) {
	ctx := context.Background()
	svc, verification, _ := newVerificationFixture()
	verification.app.Status = domain.VerificationStatusInReview

	_, err := svc.ApproveVerification(ctx, ApproveVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "approve-stale", Actor: "alice", Reason: "docs check out"},
		ApplicationID: 77,
		// The reviewer read version 3 but somebody else already advanced the row.
		Version: 2,
	})
	if !errors.Is(err, domain.ErrVerificationVersionConflict) {
		t.Fatalf("stale approve err=%v, want ErrVerificationVersionConflict", err)
	}
	if !strings.Contains(err.Error(), CodeVerificationConflict) || verification.approveCalls != 0 {
		t.Fatalf("err=%v approveCalls=%d", err, verification.approveCalls)
	}
}

func TestApproveVerificationRefusesIneligibleTargetBeforeWriting(t *testing.T) {
	ctx := context.Background()
	svc, verification, _ := newVerificationFixture()
	verification.app.Status = domain.VerificationStatusInReview
	verification.target.Eligible = false
	verification.target.Verified = true
	verification.target.Reason = domain.ErrVerificationTargetAlreadyVerified.Error()

	result, err := svc.ApproveVerification(ctx, ApproveVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "approve-verified", Actor: "alice", Reason: "docs check out", DryRun: true},
		ApplicationID: 77,
		Version:       3,
	})
	if !errors.Is(err, domain.ErrVerificationTargetAlreadyVerified) {
		t.Fatalf("approve of a verified target err=%v, want ErrVerificationTargetAlreadyVerified", err)
	}
	if !strings.Contains(err.Error(), CodeVerificationTargetVerified) {
		t.Fatalf("err=%v, want the stable code", err)
	}
	if result.Details["target_eligible"] != false || result.Details["target_verified"] != true {
		t.Fatalf("details=%+v, want the snapshot recorded on the failed command", result.Details)
	}
	if verification.approveCalls != 0 {
		t.Fatalf("approveCalls=%d, want the dry run to predict the refusal", verification.approveCalls)
	}
}

func TestApproveVerificationKeepsInternalNoteOutOfTheApplicantReason(t *testing.T) {
	ctx := context.Background()
	svc, verification, repo := newVerificationFixture()
	verification.app.Status = domain.VerificationStatusInReview

	result, err := svc.ApproveVerification(ctx, ApproveVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "approve-77", Actor: "alice", Reason: "press coverage verified"},
		ApplicationID: 77,
		Version:       3,
		InternalNote:  "contact reached us through the press office; do not quote",
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if verification.approved.Reason != "press coverage verified" {
		t.Fatalf("applicant-facing reason=%q", verification.approved.Reason)
	}
	if verification.approved.InternalNote != "contact reached us through the press office; do not quote" {
		t.Fatalf("internal note=%q, want it carried in its own field", verification.approved.InternalNote)
	}
	if strings.Contains(verification.approved.Reason, "do not quote") {
		t.Fatal("internal note leaked into the applicant-facing reason")
	}
	// The note is operator-only but must still be auditable.
	if result.Details["internal_note"] != "contact reached us through the press office; do not quote" {
		t.Fatalf("details=%+v, want the internal note journalled", result.Details)
	}
	if stored := repo.items["approve-77"].ResultJSON; !strings.Contains(string(stored), "do not quote") {
		t.Fatalf("journalled result=%s, want the internal note persisted", stored)
	}
}

// A retried approval of an already approved application is a no-op in the
// use-case layer, so the dry run must not fail it on the "already verified"
// target gate the first approval passed.
func TestApproveVerificationReplayIsNotBlockedByTheTargetGate(t *testing.T) {
	ctx := context.Background()
	svc, verification, _ := newVerificationFixture()
	verification.app.Status = domain.VerificationStatusApproved
	verification.app.ReviewerAdminID = "alice"
	verification.target.Verified = true
	verification.target.Eligible = false
	verification.target.Reason = domain.ErrVerificationTargetAlreadyVerified.Error()

	dry, err := svc.ApproveVerification(ctx, ApproveVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "approve-replay-dry", Actor: "alice", Reason: "docs check out", DryRun: true},
		ApplicationID: 77,
		Version:       3,
	})
	if err != nil {
		t.Fatalf("dry-run replay approve: %v", err)
	}
	if verification.approveCalls != 0 || dry.Details["target_verified"] != true {
		t.Fatalf("dry-run replay details=%+v approveCalls=%d", dry.Details, verification.approveCalls)
	}

	exec, err := svc.ApproveVerification(ctx, ApproveVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "approve-replay", Actor: "alice", Reason: "docs check out"},
		ApplicationID: 77,
		Version:       3,
	})
	if err != nil {
		t.Fatalf("replay approve: %v", err)
	}
	if exec.Details["changed"] != true {
		// The fake always reports a change; the point is that the command reached
		// the service instead of being refused by the pre-check.
		t.Fatalf("replay approve details=%+v", exec.Details)
	}
	if verification.approveCalls != 1 {
		t.Fatalf("approveCalls=%d, want the replay handed to the use-case layer", verification.approveCalls)
	}
}

func TestRejectVerificationRequiresReason(t *testing.T) {
	ctx := context.Background()
	svc, verification, repo := newVerificationFixture()
	verification.app.Status = domain.VerificationStatusInReview

	_, err := svc.RejectVerification(ctx, RejectVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "reject-no-reason", Actor: "alice", Reason: "   "},
		ApplicationID: 77,
		Version:       3,
	})
	if !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("reject without reason err=%v, want ErrVerificationReasonRequired", err)
	}
	if !strings.Contains(err.Error(), CodeVerificationReasonRequired) {
		t.Fatalf("err=%v, want the stable code", err)
	}
	if verification.rejectCalls != 0 || len(repo.items) != 0 {
		t.Fatalf("rejectCalls=%d journalled=%d, want a refusal before the journal is touched",
			verification.rejectCalls, len(repo.items))
	}

	ok, err := svc.RejectVerification(ctx, RejectVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "reject-77", Actor: "alice", Reason: "press links are self-published"},
		ApplicationID: 77,
		Version:       3,
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if verification.rejectCalls != 1 || verification.rejected.Reason != "press links are self-published" {
		t.Fatalf("rejectCalls=%d decision=%+v", verification.rejectCalls, verification.rejected)
	}
	if ok.Details["status"] != string(domain.VerificationStatusRejected) ||
		ok.Details["decision_reason"] != "press links are self-published" {
		t.Fatalf("details=%+v", ok.Details)
	}
}

func TestRevokeVerificationRequiresReasonAndTargetShape(t *testing.T) {
	ctx := context.Background()
	svc, verification, repo := newVerificationFixture()
	verification.target.Verified = true

	_, err := svc.RevokeVerification(ctx, RevokeVerificationRequest{
		CommandMeta: CommandMeta{CommandID: "revoke-no-reason", Actor: "alice"},
		TargetType:  domain.VerificationTargetChannel,
		TargetID:    5005,
	})
	if !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("revoke without reason err=%v, want ErrVerificationReasonRequired", err)
	}

	_, err = svc.RevokeVerification(ctx, RevokeVerificationRequest{
		CommandMeta: CommandMeta{CommandID: "revoke-bad-target", Actor: "alice", Reason: "impersonation"},
		TargetType:  domain.VerificationTargetType("group"),
		TargetID:    5005,
	})
	if !errors.Is(err, domain.ErrVerificationTargetInvalid) {
		t.Fatalf("revoke of an unmodelled target err=%v, want ErrVerificationTargetInvalid", err)
	}
	if verification.revokeCalls != 0 || len(repo.items) != 0 {
		t.Fatalf("revokeCalls=%d journalled=%d, want refusals before the journal", verification.revokeCalls, len(repo.items))
	}

	dry, err := svc.RevokeVerification(ctx, RevokeVerificationRequest{
		CommandMeta: CommandMeta{CommandID: "dry-revoke", Actor: "alice", Reason: "impersonation confirmed", DryRun: true},
		TargetType:  domain.VerificationTargetChannel,
		TargetID:    5005,
	})
	if err != nil {
		t.Fatalf("dry-run revoke: %v", err)
	}
	if verification.revokeCalls != 0 || dry.Details["target_verified"] != true {
		t.Fatalf("dry-run revoke details=%+v revokeCalls=%d", dry.Details, verification.revokeCalls)
	}

	exec, err := svc.RevokeVerification(ctx, RevokeVerificationRequest{
		CommandMeta:  CommandMeta{CommandID: "exec-revoke", Actor: "alice", Reason: "impersonation confirmed"},
		TargetType:   domain.VerificationTargetChannel,
		TargetID:     5005,
		InternalNote: "legal asked for the takedown",
	})
	if err != nil {
		t.Fatalf("execute revoke: %v", err)
	}
	if verification.revokeCalls != 1 || verification.revoked.Reason != "impersonation confirmed" ||
		verification.revoked.InternalNote != "legal asked for the takedown" ||
		verification.revoked.CorrelationID != "exec-revoke" {
		t.Fatalf("revocation=%+v", verification.revoked)
	}
	if exec.Details["target_verified"] != false || exec.Details["changed"] != true ||
		exec.Details["application_id"] != "77" {
		t.Fatalf("execute revoke details=%+v", exec.Details)
	}
	if exec.TargetPeer != (domain.Peer{Type: domain.PeerTypeChannel, ID: 5005}) {
		t.Fatalf("journalled target peer=%+v", exec.TargetPeer)
	}
}

func TestRevokeVerificationRefusesSystemAccount(t *testing.T) {
	ctx := context.Background()
	svc, verification, _ := newVerificationFixture()

	result, err := svc.RevokeVerification(ctx, RevokeVerificationRequest{
		CommandMeta: CommandMeta{CommandID: "revoke-system", Actor: "alice", Reason: "cleanup"},
		TargetType:  domain.VerificationTargetBot,
		TargetID:    domain.BotFatherUserID,
	})
	if !errors.Is(err, domain.ErrVerificationTargetSystem) {
		t.Fatalf("revoke of a system account err=%v, want ErrVerificationTargetSystem", err)
	}
	if verification.revokeCalls != 0 || result.Details["target_id"] == nil {
		t.Fatalf("revokeCalls=%d details=%+v", verification.revokeCalls, result.Details)
	}
}

func TestVerificationDecisionShapeIsValidatedBeforeTheJournal(t *testing.T) {
	ctx := context.Background()
	svc, _, repo := newVerificationFixture()

	if _, err := svc.ClaimVerification(ctx, ClaimVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "claim-no-id", Actor: "alice", Reason: "queue"},
		ApplicationID: 0,
		Version:       3,
	}); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("claim without an application err=%v", err)
	}
	if _, err := svc.ClaimVerification(ctx, ClaimVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "claim-no-version", Actor: "alice", Reason: "queue"},
		ApplicationID: 77,
		Version:       0,
	}); !errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		t.Fatalf("claim without a version err=%v", err)
	}
	if _, err := svc.ApproveVerification(ctx, ApproveVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "approve-long-note", Actor: "alice", Reason: "queue"},
		ApplicationID: 77,
		Version:       3,
		InternalNote:  strings.Repeat("x", domain.MaxVerificationNoteLength+1),
	}); !errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		t.Fatalf("approve with an oversized note err=%v", err)
	}
	if len(repo.items) != 0 {
		t.Fatalf("journalled=%d, want malformed decisions refused before the journal", len(repo.items))
	}
}

func TestVerificationMissingApplicationIsJournalledAsNotFound(t *testing.T) {
	ctx := context.Background()
	svc, verification, repo := newVerificationFixture()
	verification.appErr = domain.ErrVerificationApplicationNotFound

	result, err := svc.RejectVerification(ctx, RejectVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "reject-missing", Actor: "alice", Reason: "not eligible"},
		ApplicationID: 77,
		Version:       3,
	})
	if !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("reject of a missing application err=%v", err)
	}
	if VerificationErrorCode(err) != CodeVerificationNotFound {
		t.Fatalf("code=%q", VerificationErrorCode(err))
	}
	if result.Status != string(domain.AdminCommandFailed) {
		t.Fatalf("result=%+v, want a failed command", result)
	}
	if cmd, ok := repo.items["reject-missing"]; !ok || cmd.Status != domain.AdminCommandFailed {
		t.Fatalf("journalled command=%+v ok=%v, want the failure recorded", cmd, ok)
	}
}

func TestVerificationReadsRequireTheDependency(t *testing.T) {
	ctx := context.Background()
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow})
	if _, err := svc.VerificationApplications(ctx, domain.VerificationApplicationFilter{}); err == nil {
		t.Fatal("listing without the dependency succeeded")
	}
	if _, err := svc.VerificationCounts(ctx); err == nil {
		t.Fatal("counting without the dependency succeeded")
	}
	if _, err := svc.ClaimVerification(ctx, ClaimVerificationRequest{ApplicationID: 1, Version: 1}); err == nil {
		t.Fatal("claiming without the dependency succeeded")
	}
}

func TestVerificationReadsPassThrough(t *testing.T) {
	ctx := context.Background()
	svc, verification, _ := newVerificationFixture()
	verification.counts = domain.VerificationStatusCounts{domain.VerificationStatusSubmitted: 3}
	verification.events = []domain.VerificationApplicationEvent{{
		ID: 9, ApplicationID: 77, Kind: domain.VerificationEventSubmitted,
		ToStatus: domain.VerificationStatusSubmitted, CreatedAt: fixedNow(),
	}}

	items, err := svc.VerificationApplications(ctx, domain.VerificationApplicationFilter{Limit: 10})
	if err != nil || len(items) != 1 || items[0].ID != 77 {
		t.Fatalf("applications=%+v err=%v", items, err)
	}
	counts, err := svc.VerificationCounts(ctx)
	if err != nil || counts[domain.VerificationStatusSubmitted] != 3 {
		t.Fatalf("counts=%+v err=%v", counts, err)
	}
	events, err := svc.VerificationApplicationEvents(ctx, 77, 10)
	if err != nil || len(events) != 1 || events[0].ID != 9 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	app, err := svc.VerificationApplication(ctx, 77)
	if err != nil || app.Version != 3 {
		t.Fatalf("application=%+v err=%v", app, err)
	}
	if _, err := svc.VerificationApplication(ctx, 0); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("application(0) err=%v", err)
	}
	target, err := svc.VerificationTargetSnapshot(ctx, domain.VerificationTargetChannel, 5005)
	if err != nil || target.ID != 5005 {
		t.Fatalf("target=%+v err=%v", target, err)
	}
}

func TestVerificationErrorCodeCoversTheDomainSentinels(t *testing.T) {
	cases := map[error]string{
		domain.ErrVerificationApplicationNotFound:          CodeVerificationNotFound,
		domain.ErrVerificationVersionConflict:              CodeVerificationConflict,
		domain.ErrVerificationApplicationExists:            CodeVerificationTargetOccupied,
		domain.ErrVerificationStatusInvalid:                CodeVerificationStatusInvalid,
		domain.ErrVerificationReasonRequired:               CodeVerificationReasonRequired,
		domain.ErrVerificationTargetAlreadyVerified:        CodeVerificationTargetVerified,
		domain.ErrVerificationTargetNotPublic:              CodeVerificationTargetNotPublic,
		domain.ErrVerificationTargetRestricted:             CodeVerificationTargetRestricted,
		domain.ErrVerificationTargetSystem:                 CodeVerificationTargetSystem,
		domain.ErrVerificationNotOwner:                     CodeVerificationNotOwner,
		domain.ErrVerificationUserTargetsDisabled:          CodeVerificationUserTargetsDisabled,
		domain.ErrVerificationTargetInvalid:                CodeVerificationTargetInvalid,
		domain.ErrVerificationApplicationInvalid:           CodeVerificationInvalid,
		errors.New("some transport failure nobody mapped"): "",
	}
	for err, want := range cases {
		if got := VerificationErrorCode(err); got != want {
			t.Fatalf("VerificationErrorCode(%v) = %q, want %q", err, got, want)
		}
	}
	if got := VerificationErrorCode(nil); got != "" {
		t.Fatalf("VerificationErrorCode(nil) = %q", got)
	}
}

func TestVerificationTargetReasonErrorRoundTripsTheSnapshotString(t *testing.T) {
	for _, sentinel := range []error{
		domain.ErrVerificationTargetAlreadyVerified,
		domain.ErrVerificationTargetRestricted,
		domain.ErrVerificationTargetNotPublic,
		domain.ErrVerificationTargetSystem,
		domain.ErrVerificationUserTargetsDisabled,
		domain.ErrVerificationTargetInvalid,
	} {
		if err := verificationTargetReasonError(sentinel.Error()); !errors.Is(err, sentinel) {
			t.Fatalf("verificationTargetReasonError(%q) = %v", sentinel.Error(), err)
		}
	}
	// An unrecognised reason must still be a target failure rather than a panic or
	// a silent pass.
	if err := verificationTargetReasonError("something new"); !errors.Is(err, domain.ErrVerificationTargetInvalid) {
		t.Fatalf("unknown reason mapped to %v", err)
	}
}

func TestVerificationClockIsNotRequiredForDetails(t *testing.T) {
	// The details are pure projections of the application, so a service without a
	// wall clock still produces a complete audit entry.
	svc, verification, _ := newVerificationFixture()
	verification.app.UpdatedAt = time.Time{}
	result, err := svc.ClaimVerification(context.Background(), ClaimVerificationRequest{
		CommandMeta:   CommandMeta{CommandID: "claim-clockless", Actor: "alice", Reason: "queue"},
		ApplicationID: 77,
		Version:       3,
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	for _, key := range []string{"application_id", "applicant_user_id", "target_type", "target_id", "previous_status", "status", "correlation_id"} {
		if _, ok := result.Details[key]; !ok {
			t.Fatalf("details %+v missing %q", result.Details, key)
		}
	}
}
