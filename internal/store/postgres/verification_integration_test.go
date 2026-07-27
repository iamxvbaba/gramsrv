package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// verificationTestUser inserts a throwaway user row and registers the cleanup for
// everything the verification tables may hang off it. Events reference the
// application with ON DELETE RESTRICT, so the timeline has to go first.
func verificationTestUser(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	ctx := context.Background()
	suffix := randomSuffix(t)
	var id int64
	if err := pool.QueryRow(ctx, `
INSERT INTO users (access_hash, phone, first_name)
VALUES ($1, $2, 'verification test')
RETURNING id`, time.Now().UnixNano()&0x7fffffffffffffff, "9"+suffix).Scan(&id); err != nil {
		t.Fatalf("insert verification test user: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM verification_application_events
WHERE application_id IN (
  SELECT id FROM verification_applications
  WHERE applicant_user_id = $1 OR target_id = $1
)`, id)
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM verification_notification_outbox
WHERE application_id IN (
  SELECT id FROM verification_applications
  WHERE applicant_user_id = $1 OR target_id = $1
)`, id)
		_, _ = pool.Exec(cleanupCtx, `
DELETE FROM verification_applications
WHERE applicant_user_id = $1 OR target_id = $1`, id)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE id = $1`, id)
	})
	return id
}

// verificationTestDraft is a payload that clears domain.ValidateForSubmission.
func verificationTestDraft() domain.VerificationDraftInput {
	return domain.VerificationDraftInput{
		Category:        "media",
		Description:     strings.Repeat("independent newsroom covering the region ", 2),
		OfficialWebsite: "https://example.com",
		SocialLinks:     []string{"https://t.me/example"},
		PressLinks: []string{
			"https://press.example.com/story",
			"https://press.example.org/profile",
		},
		AdditionalNote: "filed through the bot dialog",
	}
}

func verificationTestRequest(applicant int64, targetType domain.VerificationTargetType, targetID int64, username string) domain.SubmitVerificationApplicationRequest {
	return domain.SubmitVerificationApplicationRequest{
		ApplicantUserID: applicant,
		TargetType:      targetType,
		TargetID:        targetID,
		TargetTitle:     "Target " + username,
		TargetUsername:  username,
		Draft:           verificationTestDraft(),
		CorrelationID:   fmt.Sprintf("corr-%d", targetID),
	}
}

// submittedVerificationApplication drives the applicant path up to the review
// queue, which is the state every reviewer test starts from.
func submittedVerificationApplication(t *testing.T, s *VerificationStore, applicant int64, targetType domain.VerificationTargetType, targetID int64, username string) domain.VerificationApplication {
	t.Helper()
	ctx := context.Background()
	app, created, err := s.CreateVerificationDraft(ctx, verificationTestRequest(applicant, targetType, targetID, username))
	if err != nil || !created {
		t.Fatalf("create draft: created=%v err=%v", created, err)
	}
	app, err = s.SubmitVerificationApplication(ctx, app.ID, app.Version)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return app
}

func verificationUserVerified(t *testing.T, pool *pgxpool.Pool, userID int64) bool {
	t.Helper()
	var verified bool
	if err := pool.QueryRow(context.Background(),
		`SELECT verified FROM users WHERE id = $1`, userID).Scan(&verified); err != nil {
		t.Fatalf("read verified flag: %v", err)
	}
	return verified
}

// verificationTxApply is the callback shape the app layer is expected to use: the
// peer flag is written through the transaction that is deciding the application,
// so the two writes commit or roll back together.
func verificationTxApply(_ *testing.T) func(context.Context, domain.VerificationApplication) error {
	return func(ctx context.Context, app domain.VerificationApplication) error {
		tx, ok := VerificationTxFromContext(ctx)
		if !ok {
			return fmt.Errorf("decision context carries no transaction")
		}
		if _, err := NewUserStore(tx).SetVerified(ctx, app.TargetID, true); err != nil {
			return err
		}
		return nil
	}
}

// TestVerificationDraftLifecyclePostgres covers the applicant path against the
// real schema: a draft is opened once and resumed on the next /start, the payload
// round-trips, and only a complete application reaches the queue.
func TestVerificationDraftLifecyclePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	applicant := verificationTestUser(t, pool)
	target := verificationTestUser(t, pool)

	req := verificationTestRequest(applicant, domain.VerificationTargetBot, target, "AlphaBot")
	req.Draft = domain.VerificationDraftInput{Category: "media"}
	app, created, err := s.CreateVerificationDraft(ctx, req)
	if err != nil || !created {
		t.Fatalf("create draft: created=%v err=%v", created, err)
	}
	if app.Status != domain.VerificationStatusDraft || app.Version != 1 {
		t.Fatalf("draft state = %s v%d, want draft v1", app.Status, app.Version)
	}
	if !app.SubmittedAt.IsZero() || !app.ReviewedAt.IsZero() || app.ReviewerAdminID != "" {
		t.Fatal("fresh draft carries review metadata")
	}

	resumed, created, err := s.CreateVerificationDraft(ctx,
		verificationTestRequest(applicant, domain.VerificationTargetBot, target, "AlphaBot"))
	if err != nil || created {
		t.Fatalf("resume draft: created=%v err=%v", created, err)
	}
	if resumed.ID != app.ID {
		t.Fatalf("resumed draft = %d, want %d", resumed.ID, app.ID)
	}

	if _, err := s.SubmitVerificationApplication(ctx, app.ID, app.Version); !errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		t.Fatalf("submit incomplete draft err = %v, want ErrVerificationApplicationInvalid", err)
	}

	saved, err := s.SaveVerificationDraft(ctx, app.ID, app.Version, verificationTestDraft())
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if saved.Version != app.Version+1 || len(saved.PressLinks) != 2 ||
		saved.SocialLinks[0] != "https://t.me/example" {
		t.Fatalf("saved draft = v%d social=%v press=%v", saved.Version, saved.SocialLinks, saved.PressLinks)
	}
	if !saved.UpdatedAt.Equal(saved.UpdatedAt.UTC()) || saved.UpdatedAt.Before(saved.CreatedAt) {
		t.Fatalf("updated_at = %v, created_at = %v", saved.UpdatedAt, saved.CreatedAt)
	}
	if _, err := s.SaveVerificationDraft(ctx, app.ID, app.Version, verificationTestDraft()); !errors.Is(err, domain.ErrVerificationVersionConflict) {
		t.Fatalf("stale save err = %v, want ErrVerificationVersionConflict", err)
	}
	if _, err := s.SaveVerificationDraft(ctx, app.ID, saved.Version,
		domain.VerificationDraftInput{OfficialWebsite: "http://127.0.0.1/x"}); !errors.Is(err, domain.ErrVerificationURLInvalid) {
		t.Fatalf("private-host save err = %v, want ErrVerificationURLInvalid", err)
	}

	reread, err := s.VerificationApplication(ctx, app.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if reread.Description != saved.Description || reread.AdditionalNote != saved.AdditionalNote ||
		reread.Category != "media" || reread.CorrelationID != fmt.Sprintf("corr-%d", target) {
		t.Fatalf("payload did not round-trip: %+v", reread)
	}

	submitted, err := s.SubmitVerificationApplication(ctx, saved.ID, saved.Version)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submitted.Status != domain.VerificationStatusSubmitted || submitted.SubmittedAt.IsZero() {
		t.Fatalf("submitted = %s at %v", submitted.Status, submitted.SubmittedAt)
	}
	if !submitted.ReviewedAt.IsZero() || submitted.ReviewerAdminID != "" {
		t.Fatal("submitted application carries a reviewer")
	}
	if _, err := s.VerificationDraftForApplicant(ctx, applicant); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("draft after submit err = %v, want ErrVerificationApplicationNotFound", err)
	}
	active, err := s.ActiveVerificationApplicationForTarget(ctx, domain.VerificationTargetBot, target)
	if err != nil || active.ID != app.ID {
		t.Fatalf("active for target = %d err=%v", active.ID, err)
	}
}

// TestVerificationActiveTargetUniquenessPostgres is the partial unique index at
// work, including the cancelled-draft case the submitted_at CHECK constrains.
func TestVerificationActiveTargetUniquenessPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	first := verificationTestUser(t, pool)
	second := verificationTestUser(t, pool)
	third := verificationTestUser(t, pool)
	target := verificationTestUser(t, pool)

	app := submittedVerificationApplication(t, s, first, domain.VerificationTargetChannel, target, "beta")
	if _, _, err := s.CreateVerificationDraft(ctx,
		verificationTestRequest(second, domain.VerificationTargetChannel, target, "beta")); !errors.Is(err, domain.ErrVerificationApplicationExists) {
		t.Fatalf("second application err = %v, want ErrVerificationApplicationExists", err)
	}
	// The same numeric id in the user namespace is a different target.
	namespaced, _, err := s.CreateVerificationDraft(ctx,
		verificationTestRequest(second, domain.VerificationTargetBot, target, "betabot"))
	if err != nil {
		t.Fatalf("other namespace draft: %v", err)
	}
	// One draft per applicant: naming another target resumes the same
	// conversation instead of tripping the applicant-draft unique index.
	resumed, created, err := s.CreateVerificationDraft(ctx,
		verificationTestRequest(second, domain.VerificationTargetBot, third, "betabot2"))
	if err != nil || created || resumed.ID != namespaced.ID {
		t.Fatalf("cross-target draft = %d created=%v err=%v, want draft %d", resumed.ID, created, err, namespaced.ID)
	}

	cancelled, err := s.CancelVerificationApplication(ctx, app.ID, app.Version, "changed my mind")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != domain.VerificationStatusCancelled || cancelled.SubmittedAt.IsZero() {
		t.Fatalf("cancelled = %s at %v", cancelled.Status, cancelled.SubmittedAt)
	}
	if cancelled.DecisionReason != "" || cancelled.ReviewerAdminID != "" || !cancelled.ReviewedAt.IsZero() {
		t.Fatalf("cancel wrote decision metadata: %+v", cancelled)
	}
	if _, err := s.CancelVerificationApplication(ctx, cancelled.ID, cancelled.Version, "again"); !errors.Is(err, domain.ErrVerificationStatusInvalid) {
		t.Fatalf("cancel of cancelled err = %v, want ErrVerificationStatusInvalid", err)
	}
	if _, _, err := s.CreateVerificationDraft(ctx,
		verificationTestRequest(third, domain.VerificationTargetChannel, target, "beta")); err != nil {
		t.Fatalf("draft after cancellation: %v", err)
	}
}

// TestVerificationCancelledDraftPostgres pins the one place the schema forces the
// store's hand: a draft that is withdrawn before submission still needs
// submitted_at, because "status <> 'draft'" requires it.
func TestVerificationCancelledDraftPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	applicant := verificationTestUser(t, pool)
	target := verificationTestUser(t, pool)

	draft, _, err := s.CreateVerificationDraft(ctx,
		verificationTestRequest(applicant, domain.VerificationTargetBot, target, "iota"))
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	cancelled, err := s.CancelVerificationApplication(ctx, draft.ID, draft.Version, "never mind")
	if err != nil {
		t.Fatalf("cancel draft: %v", err)
	}
	if cancelled.SubmittedAt.IsZero() {
		t.Fatal("cancelled draft has no submitted_at, which the CHECK forbids")
	}
	events, err := s.VerificationApplicationEvents(ctx, draft.ID, 10)
	if err != nil || len(events) != 2 {
		t.Fatalf("history = %d rows err=%v, want created + cancelled", len(events), err)
	}
	if events[0].Kind != domain.VerificationEventCancelled ||
		events[0].FromStatus != domain.VerificationStatusDraft ||
		events[0].ToStatus != domain.VerificationStatusCancelled ||
		events[0].Reason != "never mind" {
		t.Fatalf("cancelled event = %+v", events[0])
	}
}

// TestVerificationApproveWritesPeerFlagPostgres is the core invariant: the peer
// flag and the decision share one transaction, so a failing callback leaves
// neither behind and a successful one cannot be observed without the other.
func TestVerificationApproveWritesPeerFlagPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	applicant := verificationTestUser(t, pool)
	target := verificationTestUser(t, pool)
	app := submittedVerificationApplication(t, s, applicant, domain.VerificationTargetBot, target, "gamma")

	claimed, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.Status != domain.VerificationStatusInReview ||
		claimed.ReviewerAdminID != "admin-a" || !claimed.ReviewedAt.IsZero() {
		t.Fatalf("claimed = %+v", claimed)
	}
	if _, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: claimed.Version, Reviewer: "admin-b",
	}); !errors.Is(err, domain.ErrVerificationStatusInvalid) {
		t.Fatalf("re-claim err = %v, want ErrVerificationStatusInvalid", err)
	}

	// The callback sets the flag through the decision transaction and then fails,
	// so the rollback has to take the flag with it.
	failing := errors.New("notification pipeline unavailable")
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: claimed.Version, Reviewer: "admin-a",
	}, true, func(ctx context.Context, decided domain.VerificationApplication) error {
		if err := verificationTxApply(t)(ctx, decided); err != nil {
			return err
		}
		return failing
	}); !errors.Is(err, failing) {
		t.Fatalf("failing approve err = %v, want %v", err, failing)
	}
	rolled, err := s.VerificationApplication(ctx, app.ID)
	if err != nil {
		t.Fatalf("read after failed approve: %v", err)
	}
	if rolled.Status != domain.VerificationStatusInReview || rolled.Version != claimed.Version {
		t.Fatalf("failed approve left %s v%d, want in_review v%d", rolled.Status, rolled.Version, claimed.Version)
	}
	if verificationUserVerified(t, pool, target) {
		t.Fatal("rolled-back approval left the target verified")
	}
	pending, err := s.PendingVerificationNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(verificationRowsFor(pending, app.ID)) != 0 {
		t.Fatalf("outbox after failed approve = %+v, want empty", pending)
	}
	events, err := s.VerificationApplicationEvents(ctx, app.ID, 10)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, event := range events {
		if event.Kind == domain.VerificationEventApproved {
			t.Fatal("failed approve appended an approved event")
		}
	}

	approved, changed, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: claimed.Version, Reviewer: "admin-a",
		InternalNote: "checked the press coverage", CorrelationID: "cmd-1",
	}, true, verificationTxApply(t))
	if err != nil || !changed {
		t.Fatalf("approve: changed=%v err=%v", changed, err)
	}
	if approved.Status != domain.VerificationStatusApproved || approved.ReviewedAt.IsZero() ||
		approved.ReviewerAdminID != "admin-a" || approved.Version != claimed.Version+1 ||
		approved.InternalNote != "checked the press coverage" {
		t.Fatalf("approved = %+v", approved)
	}
	if !verificationUserVerified(t, pool, target) {
		t.Fatal("approved application whose target is not verified")
	}

	repeat, changed, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: approved.Version, Reviewer: "admin-b",
	}, true, func(context.Context, domain.VerificationApplication) error {
		t.Error("idempotent approve invoked the callback")
		return nil
	})
	if err != nil || changed {
		t.Fatalf("repeat approve: changed=%v err=%v", changed, err)
	}
	if repeat.Version != approved.Version || repeat.ReviewerAdminID != "admin-a" {
		t.Fatalf("repeat approve mutated the record: v%d by %q", repeat.Version, repeat.ReviewerAdminID)
	}
	pending, err = s.PendingVerificationNotifications(ctx, 100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	mine := verificationRowsFor(pending, app.ID)
	if len(mine) != 1 || mine[0].Kind != "approved" || mine[0].RecipientUserID != applicant {
		t.Fatalf("outbox = %+v, want exactly one approved row for the applicant", mine)
	}
	if mine[0].Application.ID != app.ID || mine[0].Application.TargetUsername != "gamma" ||
		mine[0].Application.Status != domain.VerificationStatusApproved {
		t.Fatalf("outbox row carries no application context: %+v", mine[0].Application)
	}

	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: approved.Version, Reviewer: "admin-a", Reason: "changed our mind",
	}, false, nil); !errors.Is(err, domain.ErrVerificationStatusInvalid) {
		t.Fatalf("reject after approve err = %v, want ErrVerificationStatusInvalid", err)
	}
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: approved.Version, Reviewer: "admin-a",
	}, true, nil); err == nil {
		t.Fatal("approve without a callback succeeded")
	}
}

// TestVerificationRejectAndCooldownPostgres covers the rejection path and the
// cooldown lookup the re-application check is measured from.
func TestVerificationRejectAndCooldownPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	applicant := verificationTestUser(t, pool)
	other := verificationTestUser(t, pool)
	target := verificationTestUser(t, pool)
	app := submittedVerificationApplication(t, s, applicant, domain.VerificationTargetChannel, target, "delta")

	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
	}, false, nil); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("reject without reason err = %v, want ErrVerificationReasonRequired", err)
	}
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "  ", Reason: "not eligible",
	}, false, nil); !errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		t.Fatalf("reject without reviewer err = %v, want ErrVerificationApplicationInvalid", err)
	}
	rejected, changed, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
		Reason: "press coverage is not independent", InternalNote: "second attempt this month",
	}, false, nil)
	if err != nil || !changed {
		t.Fatalf("reject: changed=%v err=%v", changed, err)
	}
	if rejected.Status != domain.VerificationStatusRejected || rejected.DecisionReason == "" ||
		rejected.ReviewedAt.IsZero() {
		t.Fatalf("rejected = %+v", rejected)
	}
	if verificationUserVerified(t, pool, target) {
		t.Fatal("rejection verified the target")
	}

	cooldown, err := s.LastVerificationRejection(ctx, applicant, domain.VerificationTargetChannel, target)
	if err != nil || cooldown.ID != app.ID {
		t.Fatalf("cooldown lookup = %d err=%v", cooldown.ID, err)
	}
	if _, err := s.LastVerificationRejection(ctx, other, domain.VerificationTargetChannel, target); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("cooldown for another applicant err = %v, want ErrVerificationApplicationNotFound", err)
	}

	second := submittedVerificationApplication(t, s, applicant, domain.VerificationTargetChannel, target, "delta")
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: second.ID, Version: second.Version, Reviewer: "admin-b", Reason: "still no",
	}, false, nil); err != nil {
		t.Fatalf("second reject: %v", err)
	}
	cooldown, err = s.LastVerificationRejection(ctx, applicant, domain.VerificationTargetChannel, target)
	if err != nil || cooldown.ID != second.ID {
		t.Fatalf("newest rejection = %d err=%v, want %d", cooldown.ID, err, second.ID)
	}
	history, err := s.VerificationApplicationsForApplicant(ctx, applicant, 10)
	if err != nil || len(history) != 2 || history[0].ID != second.ID || history[1].ID != app.ID {
		t.Fatalf("applicant history = %+v err=%v", history, err)
	}
}

// TestVerificationConcurrentDecisionPostgres runs two reviewers at the same time
// on the same version. Exactly one decision, exactly one notification, and the
// loser is told it lost instead of silently overwriting the winner.
func TestVerificationConcurrentDecisionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	applicant := verificationTestUser(t, pool)
	target := verificationTestUser(t, pool)
	app := submittedVerificationApplication(t, s, applicant, domain.VerificationTargetBot, target, "epsilon")

	var mu sync.Mutex
	calls := 0
	apply := func(ctx context.Context, decided domain.VerificationApplication) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return verificationTxApply(t)(ctx, decided)
	}
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i, reviewer := range []string{"admin-a", "admin-b"} {
		wg.Add(1)
		go func(i int, reviewer string) {
			defer wg.Done()
			_, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
				ApplicationID: app.ID, Version: app.Version, Reviewer: reviewer,
			}, true, apply)
			results[i] = err
		}(i, reviewer)
	}
	wg.Wait()

	winners, conflicts := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, domain.ErrVerificationVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent decision error: %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent decision = %d winners, %d conflicts, want 1 and 1", winners, conflicts)
	}
	if calls != 1 {
		t.Fatalf("applyVerified ran %d times, want exactly 1", calls)
	}
	final, err := s.VerificationApplication(ctx, app.ID)
	if err != nil || final.Status != domain.VerificationStatusApproved || final.Version != app.Version+1 {
		t.Fatalf("final application = %s v%d err=%v", final.Status, final.Version, err)
	}
	if !verificationUserVerified(t, pool, target) {
		t.Fatal("approved application whose target is not verified")
	}
	pending, err := s.PendingVerificationNotifications(ctx, 100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if got := verificationRowsFor(pending, app.ID); len(got) != 1 {
		t.Fatalf("outbox = %+v, want exactly one notification", got)
	}
	var events int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM verification_application_events
WHERE application_id = $1 AND kind = 'approved'`, app.ID).Scan(&events); err != nil {
		t.Fatalf("count approved events: %v", err)
	}
	if events != 1 {
		t.Fatalf("approved history rows = %d, want 1", events)
	}
}

// TestVerificationRevokePostgres takes the badge back: the flag is cleared in the
// same transaction, the application stays approved as history, and the revocation
// notifies exactly once.
func TestVerificationRevokePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	applicant := verificationTestUser(t, pool)
	target := verificationTestUser(t, pool)
	orphanTarget := verificationTestUser(t, pool)
	app := submittedVerificationApplication(t, s, applicant, domain.VerificationTargetBot, target, "zeta")
	approved, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
	}, true, verificationTxApply(t))
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	clear := func(ctx context.Context, peer domain.Peer) error {
		tx, ok := VerificationTxFromContext(ctx)
		if !ok {
			return fmt.Errorf("revocation context carries no transaction")
		}
		if peer.Type != domain.PeerTypeUser {
			return fmt.Errorf("unexpected peer type %q", peer.Type)
		}
		_, err := NewUserStore(tx).SetVerified(ctx, peer.ID, false)
		return err
	}
	req := domain.VerificationRevocation{
		TargetType: domain.VerificationTargetBot, TargetID: target,
		Reviewer: "admin-b", Reason: "impersonation report upheld", CorrelationID: "cmd-r",
	}
	if _, _, err := s.RevokeVerification(ctx, domain.VerificationRevocation{
		TargetType: domain.VerificationTargetBot, TargetID: target, Reviewer: "admin-b",
	}, clear); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("revoke without reason err = %v, want ErrVerificationReasonRequired", err)
	}

	failing := errors.New("peer store unavailable")
	if _, _, err := s.RevokeVerification(ctx, req, func(ctx context.Context, peer domain.Peer) error {
		if err := clear(ctx, peer); err != nil {
			return err
		}
		return failing
	}); !errors.Is(err, failing) {
		t.Fatalf("failing revoke err = %v, want %v", err, failing)
	}
	if !verificationUserVerified(t, pool, target) {
		t.Fatal("rolled-back revocation cleared the flag anyway")
	}

	revoked, changed, err := s.RevokeVerification(ctx, req, clear)
	if err != nil || !changed {
		t.Fatalf("revoke: changed=%v err=%v", changed, err)
	}
	if revoked.ID != approved.ID || revoked.Status != domain.VerificationStatusApproved {
		t.Fatalf("revoked application = %d %s, want %d approved", revoked.ID, revoked.Status, approved.ID)
	}
	if verificationUserVerified(t, pool, target) {
		t.Fatal("revocation left the target verified")
	}

	repeat, changed, err := s.RevokeVerification(ctx, req, func(context.Context, domain.Peer) error {
		t.Error("idempotent revoke invoked the callback")
		return nil
	})
	if err != nil || changed {
		t.Fatalf("repeat revoke: changed=%v err=%v", changed, err)
	}
	if repeat.ID != approved.ID {
		t.Fatalf("repeat revoke returned %d, want %d", repeat.ID, approved.ID)
	}

	var revokedEvents, revokedOutbox int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM verification_application_events
WHERE application_id = $1 AND kind = 'revoked'`, app.ID).Scan(&revokedEvents); err != nil {
		t.Fatalf("count revoked events: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM verification_notification_outbox
WHERE application_id = $1 AND kind = 'revoked'`, app.ID).Scan(&revokedOutbox); err != nil {
		t.Fatalf("count revoked outbox rows: %v", err)
	}
	if revokedEvents != 1 || revokedOutbox != 1 {
		t.Fatalf("revocation recorded %d events and %d outbox rows, want 1 and 1", revokedEvents, revokedOutbox)
	}

	// A flag with no application behind it is still cleared: leaving it standing
	// is worse than a missing audit row.
	if _, err := NewUserStore(pool).SetVerified(ctx, orphanTarget, true); err != nil {
		t.Fatalf("seed orphan flag: %v", err)
	}
	orphan, changed, err := s.RevokeVerification(ctx, domain.VerificationRevocation{
		TargetType: domain.VerificationTargetBot, TargetID: orphanTarget,
		Reviewer: "admin-b", Reason: "manual flag from an older deployment",
	}, clear)
	if err != nil || !changed || orphan.ID != 0 {
		t.Fatalf("orphan revoke: app=%d changed=%v err=%v", orphan.ID, changed, err)
	}
	if verificationUserVerified(t, pool, orphanTarget) {
		t.Fatal("orphan revocation left the flag standing")
	}
}

// TestVerificationHistoryAndOutboxPostgres pins the append-only timeline and
// walks one notification from pending to delivered.
func TestVerificationHistoryAndOutboxPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	applicant := verificationTestUser(t, pool)
	target := verificationTestUser(t, pool)
	app := submittedVerificationApplication(t, s, applicant, domain.VerificationTargetBot, target, "eta")
	claimed, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: claimed.Version, Reviewer: "admin-a", CorrelationID: "cmd-9",
	}, true, verificationTxApply(t)); err != nil {
		t.Fatalf("approve: %v", err)
	}

	pending, err := s.PendingVerificationNotifications(ctx, 100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	mine := verificationRowsFor(pending, app.ID)
	if len(mine) != 1 || mine[0].Attempts != 0 {
		t.Fatalf("pending = %+v, want one fresh row", mine)
	}
	id := mine[0].ID
	if err := s.MarkVerificationNotificationFailed(ctx, id, "bot blocked by user"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	pending, err = s.PendingVerificationNotifications(ctx, 100)
	if err != nil {
		t.Fatalf("pending after failure: %v", err)
	}
	mine = verificationRowsFor(pending, app.ID)
	if len(mine) != 1 || mine[0].Attempts != 1 {
		t.Fatalf("after failure = %+v, want still pending with one attempt", mine)
	}
	var lastError string
	if err := pool.QueryRow(ctx, `
SELECT last_error FROM verification_notification_outbox WHERE id = $1`, id).Scan(&lastError); err != nil {
		t.Fatalf("read last_error: %v", err)
	}
	if lastError != "bot blocked by user" {
		t.Fatalf("last_error = %q", lastError)
	}

	if err := s.MarkVerificationNotificationDelivered(ctx, id); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	pending, err = s.PendingVerificationNotifications(ctx, 100)
	if err != nil {
		t.Fatalf("pending after delivery: %v", err)
	}
	if got := verificationRowsFor(pending, app.ID); len(got) != 0 {
		t.Fatalf("delivered row is still pending: %+v", got)
	}
	if err := s.MarkVerificationNotificationDelivered(ctx, id); err != nil {
		t.Fatalf("repeat deliver: %v", err)
	}
	if err := s.MarkVerificationNotificationFailed(ctx, id, "late error"); err != nil {
		t.Fatalf("fail after delivery: %v", err)
	}
	if err := s.MarkVerificationNotificationDelivered(ctx, 0); !errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		t.Fatalf("deliver id=0 err = %v, want ErrVerificationApplicationInvalid", err)
	}

	events, err := s.VerificationApplicationEvents(ctx, app.ID, 20)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	wantKinds := []domain.VerificationApplicationEventKind{
		domain.VerificationEventNotified,
		domain.VerificationEventApproved,
		domain.VerificationEventClaimed,
		domain.VerificationEventSubmitted,
		domain.VerificationEventCreated,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("history = %d rows, want %d: %+v", len(events), len(wantKinds), events)
	}
	for i, kind := range wantKinds {
		if events[i].Kind != kind {
			t.Fatalf("history[%d] = %s, want %s", i, events[i].Kind, kind)
		}
		if i > 0 && events[i].ID >= events[i-1].ID {
			t.Fatalf("history is not newest-first at %d", i)
		}
	}
	if events[1].FromStatus != domain.VerificationStatusInReview ||
		events[1].ToStatus != domain.VerificationStatusApproved ||
		events[1].Actor != "admin-a" || events[1].CorrelationID != "cmd-9" {
		t.Fatalf("approved event = %+v", events[1])
	}
	if events[0].Reason != "approved" {
		t.Fatalf("notified event = %+v, want the approved notification", events[0])
	}

	// The timeline is append-only: nothing in the store rewrites a row, and the
	// delivered notification did not touch the earlier ones.
	var mutated int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM verification_application_events
WHERE application_id = $1 AND created_at < (
  SELECT created_at FROM verification_application_events
  WHERE application_id = $1 ORDER BY id LIMIT 1
)`, app.ID).Scan(&mutated); err != nil {
		t.Fatalf("check event ordering: %v", err)
	}
	if mutated != 0 {
		t.Fatalf("%d history rows predate the first one", mutated)
	}
}

// TestVerificationQueueQueriesPostgres covers the review-queue projection against
// the real indexes: status and target filters, reviewer scoping, the search shapes
// and keyset paging. Every assertion is scoped by the date lower bound so rows
// from other runs cannot leak into it.
func TestVerificationQueueQueriesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	suffix := randomSuffix(t)
	start := time.Now().UTC().Truncate(time.Microsecond)

	before, err := s.VerificationStatusCounts(ctx)
	if err != nil {
		t.Fatalf("counts before: %v", err)
	}

	firstApplicant := verificationTestUser(t, pool)
	secondApplicant := verificationTestUser(t, pool)
	thirdApplicant := verificationTestUser(t, pool)
	firstTarget := verificationTestUser(t, pool)
	secondTarget := verificationTestUser(t, pool)
	thirdTarget := verificationTestUser(t, pool)

	reviewer := "admin-" + suffix
	first := submittedVerificationApplication(t, s, firstApplicant, domain.VerificationTargetBot, firstTarget, "alpha"+suffix)
	if _, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: first.ID, Version: first.Version, Reviewer: reviewer,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	second := submittedVerificationApplication(t, s, secondApplicant, domain.VerificationTargetChannel, secondTarget, "Beta"+suffix)
	third, _, err := s.CreateVerificationDraft(ctx,
		verificationTestRequest(thirdApplicant, domain.VerificationTargetSupergroup, thirdTarget, "gamma"+suffix))
	if err != nil {
		t.Fatalf("third draft: %v", err)
	}

	ids := func(apps []domain.VerificationApplication) []int64 {
		out := make([]int64, 0, len(apps))
		for _, app := range apps {
			out = append(out, app.ID)
		}
		return out
	}
	equal := func(got []int64, want ...int64) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	list := func(filter domain.VerificationApplicationFilter) []int64 {
		t.Helper()
		if filter.CreatedAt.IsZero() {
			filter.CreatedAt = start
		}
		got, err := s.ListVerificationApplications(ctx, filter)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return ids(got)
	}

	if got := list(domain.VerificationApplicationFilter{}); !equal(got, third.ID, second.ID, first.ID) {
		t.Fatalf("queue order = %v, want newest first", got)
	}
	if got := list(domain.VerificationApplicationFilter{
		Statuses: []domain.VerificationStatus{domain.VerificationStatusSubmitted, domain.VerificationStatusInReview},
	}); !equal(got, second.ID, first.ID) {
		t.Fatalf("status filter = %v", got)
	}
	if got := list(domain.VerificationApplicationFilter{
		TargetType: domain.VerificationTargetChannel,
	}); !equal(got, second.ID) {
		t.Fatalf("target type filter = %v", got)
	}
	if got := list(domain.VerificationApplicationFilter{Reviewer: reviewer}); !equal(got, first.ID) {
		t.Fatalf("reviewer filter = %v", got)
	}
	if got := list(domain.VerificationApplicationFilter{Reviewer: "admin-nobody"}); len(got) != 0 {
		t.Fatalf("unknown reviewer = %v", got)
	}
	if got := list(domain.VerificationApplicationFilter{Query: fmt.Sprint(first.ID)}); !equal(got, first.ID) {
		t.Fatalf("application id search = %v", got)
	}
	if got := list(domain.VerificationApplicationFilter{Query: fmt.Sprint(secondTarget)}); !equal(got, second.ID) {
		t.Fatalf("peer id search = %v", got)
	}
	if got := list(domain.VerificationApplicationFilter{Query: "@beta" + suffix}); !equal(got, second.ID) {
		t.Fatalf("username search = %v", got)
	}
	if got := list(domain.VerificationApplicationFilter{Query: "ALPHA" + suffix}); !equal(got, first.ID) {
		t.Fatalf("case-insensitive username search = %v", got)
	}
	if got := list(domain.VerificationApplicationFilter{Query: "nobody" + suffix}); len(got) != 0 {
		t.Fatalf("miss search = %v", got)
	}
	if got := list(domain.VerificationApplicationFilter{CreatedAt: second.CreatedAt}); !equal(got, third.ID, second.ID) {
		t.Fatalf("since filter = %v", got)
	}
	// BeforeID without a cursor timestamp still bounds the page.
	if got := list(domain.VerificationApplicationFilter{BeforeID: second.ID}); !equal(got, first.ID) {
		t.Fatalf("id-only cursor = %v", got)
	}

	page, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		CreatedAt: start, Limit: 2,
	})
	if err != nil || !equal(ids(page), third.ID, second.ID) {
		t.Fatalf("first page = %v err=%v", ids(page), err)
	}
	last := page[len(page)-1]
	next, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		CreatedAt: start, Limit: 2, Until: last.CreatedAt, BeforeID: last.ID,
	})
	if err != nil || !equal(ids(next), first.ID) {
		t.Fatalf("second page = %v err=%v", ids(next), err)
	}
	last = next[len(next)-1]
	tail, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		CreatedAt: start, Limit: 2, Until: last.CreatedAt, BeforeID: last.ID,
	})
	if err != nil || len(tail) != 0 {
		t.Fatalf("third page = %v err=%v, want empty", ids(tail), err)
	}

	after, err := s.VerificationStatusCounts(ctx)
	if err != nil {
		t.Fatalf("counts after: %v", err)
	}
	for status, want := range map[domain.VerificationStatus]int64{
		domain.VerificationStatusDraft:     1,
		domain.VerificationStatusSubmitted: 1,
		domain.VerificationStatusInReview:  1,
	} {
		if got := after[status] - before[status]; got != want {
			t.Fatalf("count delta for %s = %d, want %d", status, got, want)
		}
	}

	if _, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		Statuses: []domain.VerificationStatus{"bogus"},
	}); !errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		t.Fatalf("bogus status filter err = %v, want ErrVerificationApplicationInvalid", err)
	}
	if _, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		TargetType: "bogus",
	}); !errors.Is(err, domain.ErrVerificationTargetInvalid) {
		t.Fatalf("bogus target filter err = %v, want ErrVerificationTargetInvalid", err)
	}
}

// TestVerificationMissingApplicationPostgres pins the not-found surface every
// mutation shares.
func TestVerificationMissingApplicationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewVerificationStore(pool)
	var missing int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(max(id), 0) + 1000 FROM verification_applications`).Scan(&missing); err != nil {
		t.Fatalf("pick missing id: %v", err)
	}
	if _, err := s.VerificationApplication(ctx, missing); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("read err = %v", err)
	}
	if _, err := s.SubmitVerificationApplication(ctx, missing, 1); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("submit err = %v", err)
	}
	if _, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: missing, Version: 1, Reviewer: "admin-a",
	}); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("claim err = %v", err)
	}
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: missing, Version: 1, Reviewer: "admin-a",
	}, true, func(context.Context, domain.VerificationApplication) error {
		return nil
	}); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("decide err = %v", err)
	}
	if _, err := s.VerificationApplicationEvents(ctx, missing, 10); err != nil {
		t.Fatalf("events of a missing application: %v", err)
	}
}

// verificationRowsFor narrows the shared outbox to one application, so a test
// never depends on what other rows the test database happens to hold.
func verificationRowsFor(rows []store.VerificationNotification, applicationID int64) []store.VerificationNotification {
	out := make([]store.VerificationNotification, 0, len(rows))
	for _, row := range rows {
		if row.ApplicationID == applicationID {
			out = append(out, row)
		}
	}
	return out
}
