package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

// verificationTestDraft is a payload that clears domain.ValidateForSubmission:
// a category, a long enough description, a website and two independent press
// links.
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

// TestMemoryVerificationDraftLifecycle covers the applicant path: a draft is
// opened once and resumed on the next /start, the payload is stored verbatim, and
// submission stamps the queue entry.
func TestMemoryVerificationDraftLifecycle(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()

	req := verificationTestRequest(1001, domain.VerificationTargetBot, 5001, "AlphaBot")
	req.Draft = domain.VerificationDraftInput{Category: "media"}
	app, created, err := s.CreateVerificationDraft(ctx, req)
	if err != nil || !created {
		t.Fatalf("create draft: created=%v err=%v", created, err)
	}
	if app.Status != domain.VerificationStatusDraft || app.Version != 1 {
		t.Fatalf("draft state = %s v%d, want draft v1", app.Status, app.Version)
	}
	if app.TargetUsername != "AlphaBot" || app.CorrelationID != "corr-5001" {
		t.Fatalf("draft snapshot = %q / %q", app.TargetUsername, app.CorrelationID)
	}
	if !app.SubmittedAt.IsZero() || !app.ReviewedAt.IsZero() || app.ReviewerAdminID != "" {
		t.Fatal("fresh draft carries review metadata")
	}

	// The bot dialog is one conversation: a second /start resumes the draft.
	resumed, created, err := s.CreateVerificationDraft(ctx, verificationTestRequest(1001, domain.VerificationTargetBot, 5001, "AlphaBot"))
	if err != nil || created {
		t.Fatalf("resume draft: created=%v err=%v", created, err)
	}
	if resumed.ID != app.ID || resumed.Version != app.Version {
		t.Fatalf("resumed draft = %d v%d, want %d v%d", resumed.ID, resumed.Version, app.ID, app.Version)
	}

	if _, err := s.SubmitVerificationApplication(ctx, app.ID, app.Version); !errors.Is(err, domain.ErrVerificationApplicationInvalid) {
		t.Fatalf("submit incomplete draft err = %v, want ErrVerificationApplicationInvalid", err)
	}

	saved, err := s.SaveVerificationDraft(ctx, app.ID, app.Version, verificationTestDraft())
	if err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if saved.Version != app.Version+1 || len(saved.PressLinks) != 2 || saved.OfficialWebsite != "https://example.com" {
		t.Fatalf("saved draft = v%d links=%v site=%q", saved.Version, saved.PressLinks, saved.OfficialWebsite)
	}
	if _, err := s.SaveVerificationDraft(ctx, app.ID, app.Version, verificationTestDraft()); !errors.Is(err, domain.ErrVerificationVersionConflict) {
		t.Fatalf("stale save err = %v, want ErrVerificationVersionConflict", err)
	}
	if _, err := s.SaveVerificationDraft(ctx, app.ID, saved.Version, domain.VerificationDraftInput{OfficialWebsite: "http://127.0.0.1/x"}); !errors.Is(err, domain.ErrVerificationURLInvalid) {
		t.Fatalf("private-host save err = %v, want ErrVerificationURLInvalid", err)
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
	if _, err := s.VerificationDraftForApplicant(ctx, 1001); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("draft after submit err = %v, want ErrVerificationApplicationNotFound", err)
	}
	active, err := s.ActiveVerificationApplicationForTarget(ctx, domain.VerificationTargetBot, 5001)
	if err != nil || active.ID != app.ID {
		t.Fatalf("active for target = %d err=%v", active.ID, err)
	}

	// Returned slices are copies: mutating them must not reach the store.
	submitted.PressLinks[0] = "https://evil.example.com"
	reread, err := s.VerificationApplication(ctx, app.ID)
	if err != nil || reread.PressLinks[0] != "https://press.example.com/story" {
		t.Fatalf("stored press links mutated through the caller: %v err=%v", reread.PressLinks, err)
	}
}

// TestMemoryVerificationActiveTargetUniqueness is the partial unique index: one
// live application per target, and a decided one no longer blocks a fresh
// attempt.
func TestMemoryVerificationActiveTargetUniqueness(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()
	first := submittedVerificationApplication(t, s, 1001, domain.VerificationTargetChannel, 6001, "beta")

	if _, _, err := s.CreateVerificationDraft(ctx, verificationTestRequest(1002, domain.VerificationTargetChannel, 6001, "beta")); !errors.Is(err, domain.ErrVerificationApplicationExists) {
		t.Fatalf("second application err = %v, want ErrVerificationApplicationExists", err)
	}
	// A different target is free for the same target id in another namespace.
	namespaced, _, err := s.CreateVerificationDraft(ctx, verificationTestRequest(1002, domain.VerificationTargetBot, 6001, "betabot"))
	if err != nil {
		t.Fatalf("other namespace draft: %v", err)
	}
	// One draft per applicant: naming another target resumes the same
	// conversation instead of opening a second draft.
	resumed, created, err := s.CreateVerificationDraft(ctx, verificationTestRequest(1002, domain.VerificationTargetBot, 6002, "betabot2"))
	if err != nil || created || resumed.ID != namespaced.ID {
		t.Fatalf("cross-target draft = %d created=%v err=%v, want draft %d", resumed.ID, created, err, namespaced.ID)
	}

	cancelled, err := s.CancelVerificationApplication(ctx, first.ID, first.Version, "changed my mind")
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != domain.VerificationStatusCancelled || cancelled.SubmittedAt.IsZero() {
		t.Fatalf("cancelled = %s at %v", cancelled.Status, cancelled.SubmittedAt)
	}
	if cancelled.DecisionReason != "" {
		t.Fatalf("cancel wrote the applicant reason into decision_reason: %q", cancelled.DecisionReason)
	}
	if _, err := s.CancelVerificationApplication(ctx, cancelled.ID, cancelled.Version, "again"); !errors.Is(err, domain.ErrVerificationStatusInvalid) {
		t.Fatalf("cancel of cancelled err = %v, want ErrVerificationStatusInvalid", err)
	}
	if _, _, err := s.CreateVerificationDraft(ctx, verificationTestRequest(1003, domain.VerificationTargetChannel, 6001, "beta")); err != nil {
		t.Fatalf("draft after cancellation: %v", err)
	}
}

// TestMemoryVerificationClaimAndApprove is the reviewer path, including the
// invariant that matters most: the peer flag is written by the callback and the
// application is only approved if that callback succeeded.
func TestMemoryVerificationClaimAndApprove(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()
	app := submittedVerificationApplication(t, s, 1001, domain.VerificationTargetBot, 7001, "gamma")

	claimed, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.Status != domain.VerificationStatusInReview || claimed.ReviewerAdminID != "admin-a" {
		t.Fatalf("claimed = %s by %q", claimed.Status, claimed.ReviewerAdminID)
	}
	if !claimed.ReviewedAt.IsZero() {
		t.Fatal("claim stamped reviewed_at, which the schema pairs with a decision")
	}
	if _, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: claimed.Version, Reviewer: "admin-b",
	}); !errors.Is(err, domain.ErrVerificationStatusInvalid) {
		t.Fatalf("re-claim err = %v, want ErrVerificationStatusInvalid", err)
	}

	// A callback failure must roll the whole decision back.
	failing := errors.New("peer store unavailable")
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: claimed.Version, Reviewer: "admin-a",
	}, true, func(context.Context, domain.VerificationApplication) error {
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
	if !rolled.ReviewedAt.IsZero() || rolled.DecisionReason != "" {
		t.Fatal("failed approve wrote decision metadata")
	}
	pending, err := s.PendingVerificationNotifications(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("outbox after failed approve = %d rows err=%v", len(pending), err)
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

	verified := make(map[domain.Peer]bool)
	applyVerified := func(_ context.Context, decided domain.VerificationApplication) error {
		if decided.Status != domain.VerificationStatusApproved {
			return fmt.Errorf("callback saw %s, want approved", decided.Status)
		}
		verified[decided.Target()] = true
		return nil
	}
	approved, changed, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: claimed.Version, Reviewer: "admin-a",
		InternalNote: "checked the press coverage", CorrelationID: "cmd-1",
	}, true, applyVerified)
	if err != nil || !changed {
		t.Fatalf("approve: changed=%v err=%v", changed, err)
	}
	if approved.Status != domain.VerificationStatusApproved || approved.ReviewedAt.IsZero() ||
		approved.ReviewerAdminID != "admin-a" || approved.Version != claimed.Version+1 {
		t.Fatalf("approved = %s v%d by %q at %v", approved.Status, approved.Version,
			approved.ReviewerAdminID, approved.ReviewedAt)
	}
	if !verified[approved.Target()] {
		t.Fatal("approved application whose target is not verified")
	}

	// Re-issuing the decision must not notify twice.
	repeat, changed, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: approved.Version, Reviewer: "admin-b",
	}, true, func(context.Context, domain.VerificationApplication) error {
		t.Fatal("idempotent approve invoked the callback")
		return nil
	})
	if err != nil || changed {
		t.Fatalf("repeat approve: changed=%v err=%v", changed, err)
	}
	if repeat.Version != approved.Version || repeat.ReviewerAdminID != "admin-a" {
		t.Fatalf("repeat approve mutated the record: v%d by %q", repeat.Version, repeat.ReviewerAdminID)
	}
	pending, err = s.PendingVerificationNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 || pending[0].Kind != "approved" || pending[0].RecipientUserID != 1001 {
		t.Fatalf("outbox = %+v, want one approved row for the applicant", pending)
	}
	if pending[0].Application.ID != app.ID || pending[0].Application.TargetUsername != "gamma" {
		t.Fatalf("outbox row carries no application context: %+v", pending[0].Application)
	}

	// A decided application is terminal.
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

// TestMemoryVerificationRejectRequiresReason keeps the audit trail honest: a
// rejection the applicant is told about always states why.
func TestMemoryVerificationRejectRequiresReason(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()
	app := submittedVerificationApplication(t, s, 1001, domain.VerificationTargetChannel, 8001, "delta")

	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
	}, false, nil); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("reject without reason err = %v, want ErrVerificationReasonRequired", err)
	}
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "   ", Reason: "not eligible",
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
	if rejected.Status != domain.VerificationStatusRejected || rejected.DecisionReason == "" {
		t.Fatalf("rejected = %s reason=%q", rejected.Status, rejected.DecisionReason)
	}

	cooldown, err := s.LastVerificationRejection(ctx, 1001, domain.VerificationTargetChannel, 8001)
	if err != nil || cooldown.ID != app.ID {
		t.Fatalf("cooldown lookup = %d err=%v", cooldown.ID, err)
	}
	if _, err := s.LastVerificationRejection(ctx, 1002, domain.VerificationTargetChannel, 8001); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("cooldown for another applicant err = %v, want ErrVerificationApplicationNotFound", err)
	}

	// The rejected application is history, so the target is free again, and the
	// newest rejection is the one the cooldown is measured from.
	second := submittedVerificationApplication(t, s, 1001, domain.VerificationTargetChannel, 8001, "delta")
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: second.ID, Version: second.Version, Reviewer: "admin-b", Reason: "still no",
	}, false, nil); err != nil {
		t.Fatalf("second reject: %v", err)
	}
	cooldown, err = s.LastVerificationRejection(ctx, 1001, domain.VerificationTargetChannel, 8001)
	if err != nil || cooldown.ID != second.ID {
		t.Fatalf("newest rejection = %d err=%v, want %d", cooldown.ID, err, second.ID)
	}
}

// TestMemoryVerificationConcurrentDecision is the two-reviewers case: both read
// the same version, exactly one decision lands and the loser is told.
func TestMemoryVerificationConcurrentDecision(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()
	app := submittedVerificationApplication(t, s, 1001, domain.VerificationTargetBot, 9001, "epsilon")

	calls := 0
	applyVerified := func(context.Context, domain.VerificationApplication) error {
		calls++
		return nil
	}
	if _, changed, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
	}, true, applyVerified); err != nil || !changed {
		t.Fatalf("first approve: changed=%v err=%v", changed, err)
	}
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-b",
	}, true, applyVerified); !errors.Is(err, domain.ErrVerificationVersionConflict) {
		t.Fatalf("second approve err = %v, want ErrVerificationVersionConflict", err)
	}
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-b", Reason: "no",
	}, false, nil); !errors.Is(err, domain.ErrVerificationVersionConflict) {
		t.Fatalf("losing reject err = %v, want ErrVerificationVersionConflict", err)
	}
	if calls != 1 {
		t.Fatalf("applyVerified called %d times, want exactly 1", calls)
	}
	pending, err := s.PendingVerificationNotifications(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("outbox = %d rows err=%v, want exactly one notification", len(pending), err)
	}
	final, err := s.VerificationApplication(ctx, app.ID)
	if err != nil || final.ReviewerAdminID != "admin-a" {
		t.Fatalf("final decision by %q err=%v, want admin-a", final.ReviewerAdminID, err)
	}
}

// TestMemoryVerificationRevoke covers taking the badge back: the flag is cleared
// through the callback, the application stays approved as history and the
// revocation notifies exactly once.
func TestMemoryVerificationRevoke(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()
	app := submittedVerificationApplication(t, s, 1001, domain.VerificationTargetChannel, 9101, "zeta")
	approved, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
	}, true, func(context.Context, domain.VerificationApplication) error { return nil })
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	req := domain.VerificationRevocation{
		TargetType: domain.VerificationTargetChannel, TargetID: 9101,
		Reviewer: "admin-b", Reason: "impersonation report upheld",
	}
	if _, _, err := s.RevokeVerification(ctx, domain.VerificationRevocation{
		TargetType: domain.VerificationTargetChannel, TargetID: 9101, Reviewer: "admin-b",
	}, func(context.Context, domain.Peer) error { return nil }); !errors.Is(err, domain.ErrVerificationReasonRequired) {
		t.Fatalf("revoke without reason err = %v, want ErrVerificationReasonRequired", err)
	}

	failing := errors.New("peer store unavailable")
	if _, _, err := s.RevokeVerification(ctx, req, func(context.Context, domain.Peer) error {
		return failing
	}); !errors.Is(err, failing) {
		t.Fatalf("failing revoke err = %v, want %v", err, failing)
	}
	events, err := s.VerificationApplicationEvents(ctx, app.ID, 20)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, event := range events {
		if event.Kind == domain.VerificationEventRevoked {
			t.Fatal("failed revoke appended a revoked event")
		}
	}

	var cleared []domain.Peer
	revoked, changed, err := s.RevokeVerification(ctx, req, func(_ context.Context, target domain.Peer) error {
		cleared = append(cleared, target)
		return nil
	})
	if err != nil || !changed {
		t.Fatalf("revoke: changed=%v err=%v", changed, err)
	}
	if len(cleared) != 1 || cleared[0] != (domain.Peer{Type: domain.PeerTypeChannel, ID: 9101}) {
		t.Fatalf("cleared = %v, want the channel peer", cleared)
	}
	if revoked.ID != approved.ID || revoked.Status != domain.VerificationStatusApproved {
		t.Fatalf("revoked application = %d %s, want %d approved", revoked.ID, revoked.Status, approved.ID)
	}

	// A second revocation is a no-op: one outbox row, one history entry.
	repeat, changed, err := s.RevokeVerification(ctx, req, func(context.Context, domain.Peer) error {
		t.Fatal("idempotent revoke invoked the callback")
		return nil
	})
	if err != nil || changed {
		t.Fatalf("repeat revoke: changed=%v err=%v", changed, err)
	}
	if repeat.ID != approved.ID {
		t.Fatalf("repeat revoke returned %d, want %d", repeat.ID, approved.ID)
	}

	pending, err := s.PendingVerificationNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	kinds := make([]string, 0, len(pending))
	for _, item := range pending {
		kinds = append(kinds, item.Kind)
	}
	if len(kinds) != 2 || kinds[0] != "approved" || kinds[1] != "revoked" {
		t.Fatalf("outbox kinds = %v, want [approved revoked] in that order", kinds)
	}

	// A target nobody ever applied for is still cleared: a standing flag is worse
	// than a missing audit row.
	orphan, changed, err := s.RevokeVerification(ctx, domain.VerificationRevocation{
		TargetType: domain.VerificationTargetBot, TargetID: 9999,
		Reviewer: "admin-b", Reason: "manual flag from an older deployment",
	}, func(context.Context, domain.Peer) error { return nil })
	if err != nil || !changed || orphan.ID != 0 {
		t.Fatalf("orphan revoke: app=%d changed=%v err=%v", orphan.ID, changed, err)
	}
}

// TestMemoryVerificationHistoryOrder pins the append-only timeline: newest first,
// with the from/to statuses of every transition.
func TestMemoryVerificationHistoryOrder(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()
	app := submittedVerificationApplication(t, s, 1001, domain.VerificationTargetBot, 9201, "eta")
	claimed, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: claimed.Version, Reviewer: "admin-a", CorrelationID: "cmd-9",
	}, true, func(context.Context, domain.VerificationApplication) error { return nil }); err != nil {
		t.Fatalf("approve: %v", err)
	}
	pending, err := s.PendingVerificationNotifications(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %d err=%v", len(pending), err)
	}
	if err := s.MarkVerificationNotificationDelivered(ctx, pending[0].ID); err != nil {
		t.Fatalf("deliver: %v", err)
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
		t.Fatalf("history = %d rows, want %d", len(events), len(wantKinds))
	}
	for i, kind := range wantKinds {
		if events[i].Kind != kind {
			t.Fatalf("history[%d] = %s, want %s", i, events[i].Kind, kind)
		}
		if i > 0 && events[i].ID >= events[i-1].ID {
			t.Fatalf("history is not newest-first at %d: %d >= %d", i, events[i].ID, events[i-1].ID)
		}
	}
	approvedEvent := events[1]
	if approvedEvent.FromStatus != domain.VerificationStatusInReview ||
		approvedEvent.ToStatus != domain.VerificationStatusApproved ||
		approvedEvent.Actor != "admin-a" || approvedEvent.CorrelationID != "cmd-9" {
		t.Fatalf("approved event = %+v", approvedEvent)
	}
	if events[3].FromStatus != domain.VerificationStatusDraft ||
		events[3].ToStatus != domain.VerificationStatusSubmitted {
		t.Fatalf("submitted event = %+v", events[3])
	}
}

// TestMemoryVerificationOutboxDelivery walks one notification from pending to
// delivered, including the failure trace a poisoned row leaves behind.
func TestMemoryVerificationOutboxDelivery(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()
	app := submittedVerificationApplication(t, s, 1001, domain.VerificationTargetBot, 9301, "theta")
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: app.ID, Version: app.Version, Reviewer: "admin-a", Reason: "no press",
	}, false, nil); err != nil {
		t.Fatalf("reject: %v", err)
	}
	pending, err := s.PendingVerificationNotifications(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 0 {
		t.Fatalf("pending = %+v err=%v", pending, err)
	}
	id := pending[0].ID

	if err := s.MarkVerificationNotificationFailed(ctx, id, "bot blocked by user"); err != nil {
		t.Fatalf("fail: %v", err)
	}
	pending, err = s.PendingVerificationNotifications(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 1 {
		t.Fatalf("after failure = %+v err=%v, want still pending with 1 attempt", pending, err)
	}
	if err := s.MarkVerificationNotificationDelivered(ctx, id); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	pending, err = s.PendingVerificationNotifications(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("after delivery = %d rows err=%v, want none", len(pending), err)
	}
	if err := s.MarkVerificationNotificationDelivered(ctx, id); err != nil {
		t.Fatalf("repeat deliver: %v", err)
	}
	if err := s.MarkVerificationNotificationFailed(ctx, id, "late error"); err != nil {
		t.Fatalf("fail after delivery: %v", err)
	}
	if err := s.MarkVerificationNotificationDelivered(ctx, id+1000); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("deliver unknown id err = %v, want ErrVerificationApplicationNotFound", err)
	}
	events, err := s.VerificationApplicationEvents(ctx, app.ID, 20)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if events[0].Kind != domain.VerificationEventNotified || events[0].Reason != "rejected" {
		t.Fatalf("notified event = %+v, want the rejected notification", events[0])
	}
}

// TestMemoryVerificationQueueQueries covers the review-queue projection: status
// and target filters, reviewer scoping, the three search shapes and keyset paging.
func TestMemoryVerificationQueueQueries(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()

	first := submittedVerificationApplication(t, s, 1001, domain.VerificationTargetBot, 9401, "AlphaBot")
	claimed, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: first.ID, Version: first.Version, Reviewer: "admin-a",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	second := submittedVerificationApplication(t, s, 1002, domain.VerificationTargetChannel, 9402, "BetaChannel")
	third, _, err := s.CreateVerificationDraft(ctx, verificationTestRequest(1003, domain.VerificationTargetSupergroup, 9403, "gammagroup"))
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

	all, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !equal(ids(all), third.ID, second.ID, first.ID) {
		t.Fatalf("queue order = %v, want newest first", ids(all))
	}

	got, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		Statuses: []domain.VerificationStatus{domain.VerificationStatusSubmitted, domain.VerificationStatusInReview},
	})
	if err != nil || !equal(ids(got), second.ID, first.ID) {
		t.Fatalf("status filter = %v err=%v", ids(got), err)
	}
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		TargetType: domain.VerificationTargetChannel,
	})
	if err != nil || !equal(ids(got), second.ID) {
		t.Fatalf("target type filter = %v err=%v", ids(got), err)
	}
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{Reviewer: "admin-a"})
	if err != nil || !equal(ids(got), first.ID) {
		t.Fatalf("reviewer filter = %v err=%v", ids(got), err)
	}
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{Reviewer: "admin-z"})
	if err != nil || len(got) != 0 {
		t.Fatalf("unknown reviewer = %v err=%v", ids(got), err)
	}
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		CreatedAt: second.CreatedAt,
	})
	if err != nil || !equal(ids(got), third.ID, second.ID) {
		t.Fatalf("since filter = %v err=%v", ids(got), err)
	}
	// BeforeID without a cursor timestamp still bounds the page.
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		BeforeID: second.ID,
	})
	if err != nil || !equal(ids(got), first.ID) {
		t.Fatalf("id-only cursor = %v err=%v", ids(got), err)
	}

	// Search: application id, peer id, username prefix (with and without @).
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		Query: fmt.Sprint(first.ID),
	})
	if err != nil || !equal(ids(got), first.ID) {
		t.Fatalf("id search = %v err=%v", ids(got), err)
	}
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{Query: "9402"})
	if err != nil || !equal(ids(got), second.ID) {
		t.Fatalf("peer id search = %v err=%v", ids(got), err)
	}
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{Query: "@beta"})
	if err != nil || !equal(ids(got), second.ID) {
		t.Fatalf("username search = %v err=%v", ids(got), err)
	}
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{Query: "ALPHA"})
	if err != nil || !equal(ids(got), first.ID) {
		t.Fatalf("case-insensitive username search = %v err=%v", ids(got), err)
	}
	got, err = s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{Query: "nobody"})
	if err != nil || len(got) != 0 {
		t.Fatalf("miss search = %v err=%v", ids(got), err)
	}

	// Keyset paging over (created_at DESC, id DESC).
	page, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{Limit: 2})
	if err != nil || !equal(ids(page), third.ID, second.ID) {
		t.Fatalf("first page = %v err=%v", ids(page), err)
	}
	last := page[len(page)-1]
	next, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		Limit: 2, Until: last.CreatedAt, BeforeID: last.ID,
	})
	if err != nil || !equal(ids(next), first.ID) {
		t.Fatalf("second page = %v err=%v", ids(next), err)
	}
	last = next[len(next)-1]
	tail, err := s.ListVerificationApplications(ctx, domain.VerificationApplicationFilter{
		Limit: 2, Until: last.CreatedAt, BeforeID: last.ID,
	})
	if err != nil || len(tail) != 0 {
		t.Fatalf("third page = %v err=%v, want empty", ids(tail), err)
	}

	counts, err := s.VerificationStatusCounts(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts[domain.VerificationStatusInReview] != 1 ||
		counts[domain.VerificationStatusSubmitted] != 1 ||
		counts[domain.VerificationStatusDraft] != 1 ||
		counts[domain.VerificationStatusApproved] != 0 {
		t.Fatalf("counts = %v", counts)
	}

	mine, err := s.VerificationApplicationsForApplicant(ctx, 1001, 10)
	if err != nil || !equal(ids(mine), first.ID) {
		t.Fatalf("applicant history = %v err=%v", ids(mine), err)
	}
	_ = claimed

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

// TestMemoryVerificationMissingApplication pins the not-found surface every
// mutation shares.
func TestMemoryVerificationMissingApplication(t *testing.T) {
	ctx := context.Background()
	s := NewVerificationStore()
	if _, err := s.VerificationApplication(ctx, 42); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("read err = %v", err)
	}
	if _, err := s.SubmitVerificationApplication(ctx, 42, 1); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("submit err = %v", err)
	}
	if _, err := s.ClaimVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: 42, Version: 1, Reviewer: "admin-a",
	}); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("claim err = %v", err)
	}
	if _, _, err := s.DecideVerificationApplication(ctx, domain.VerificationDecision{
		ApplicationID: 42, Version: 1, Reviewer: "admin-a",
	}, true, func(context.Context, domain.VerificationApplication) error {
		return nil
	}); !errors.Is(err, domain.ErrVerificationApplicationNotFound) {
		t.Fatalf("decide err = %v", err)
	}
}
