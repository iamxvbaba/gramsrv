package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

// fakeService gains the verification surface here so the shared fake keeps
// satisfying Service without touching the existing test file.

func (fakeService) ClaimVerification(_ context.Context, req admin.ClaimVerificationRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) ApproveVerification(_ context.Context, req admin.ApproveVerificationRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) RejectVerification(_ context.Context, req admin.RejectVerificationRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) RevokeVerification(_ context.Context, req admin.RevokeVerificationRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) VerificationApplications(context.Context, domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error) {
	return nil, nil
}

func (fakeService) VerificationApplication(context.Context, int64) (domain.VerificationApplication, error) {
	return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
}

func (fakeService) VerificationApplicationEvents(context.Context, int64, int) ([]domain.VerificationApplicationEvent, error) {
	return nil, nil
}

func (fakeService) VerificationCounts(context.Context) (domain.VerificationStatusCounts, error) {
	return domain.VerificationStatusCounts{}, nil
}

func (fakeService) VerificationTargetSnapshot(context.Context, domain.VerificationTargetType, int64) (domain.VerificationTarget, error) {
	return domain.VerificationTarget{}, nil
}

type captureVerificationService struct {
	fakeService
	filter   domain.VerificationApplicationFilter
	claim    admin.ClaimVerificationRequest
	approve  admin.ApproveVerificationRequest
	reject   admin.RejectVerificationRequest
	revoke   admin.RevokeVerificationRequest
	app      domain.VerificationApplication
	events   []domain.VerificationApplicationEvent
	counts   domain.VerificationStatusCounts
	target   domain.VerificationTarget
	decideOn error
}

func (s *captureVerificationService) VerificationApplications(_ context.Context, filter domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error) {
	s.filter = filter
	return []domain.VerificationApplication{s.app}, nil
}

func (s *captureVerificationService) VerificationApplication(_ context.Context, applicationID int64) (domain.VerificationApplication, error) {
	if s.app.ID != applicationID {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return s.app, nil
}

func (s *captureVerificationService) VerificationApplicationEvents(context.Context, int64, int) ([]domain.VerificationApplicationEvent, error) {
	return s.events, nil
}

func (s *captureVerificationService) VerificationCounts(context.Context) (domain.VerificationStatusCounts, error) {
	return s.counts, nil
}

func (s *captureVerificationService) VerificationTargetSnapshot(context.Context, domain.VerificationTargetType, int64) (domain.VerificationTarget, error) {
	return s.target, nil
}

func (s *captureVerificationService) ClaimVerification(_ context.Context, req admin.ClaimVerificationRequest) (admin.CommandResult, error) {
	s.claim = req
	if s.decideOn != nil {
		return admin.CommandResult{CommandID: req.CommandID, Status: "failed", Error: s.decideOn.Error()}, s.decideOn
	}
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (s *captureVerificationService) ApproveVerification(_ context.Context, req admin.ApproveVerificationRequest) (admin.CommandResult, error) {
	s.approve = req
	if s.decideOn != nil {
		return admin.CommandResult{CommandID: req.CommandID, Status: "failed", Error: s.decideOn.Error()}, s.decideOn
	}
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (s *captureVerificationService) RejectVerification(_ context.Context, req admin.RejectVerificationRequest) (admin.CommandResult, error) {
	s.reject = req
	if s.decideOn != nil {
		return admin.CommandResult{CommandID: req.CommandID, Status: "failed", Error: s.decideOn.Error()}, s.decideOn
	}
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (s *captureVerificationService) RevokeVerification(_ context.Context, req admin.RevokeVerificationRequest) (admin.CommandResult, error) {
	s.revoke = req
	if s.decideOn != nil {
		return admin.CommandResult{CommandID: req.CommandID, Status: "failed", Error: s.decideOn.Error()}, s.decideOn
	}
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

// reviewOnlyServer is the deployment shape the permission model exists for: one
// unrestricted master token plus two bounded tokens, one able to review and one
// able to review and revoke.
func reviewOnlyServer(svc Service) *Server {
	return &Server{
		token: "master",
		scoped: []ScopedToken{
			{Name: "queue-bot", Token: "scoped-review", Permissions: []string{PermissionVerificationReview}},
			{Name: "trust-and-safety", Token: "scoped-revoke", Permissions: []string{
				PermissionVerificationReview, PermissionVerificationRevoke,
			}},
			{Name: "gift-importer", Token: "scoped-other", Permissions: []string{"gifts.import"}},
		},
		svc: svc,
	}
}

func verificationRequest(method, path, token, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestVerificationRoutesRejectMissingAndUnknownTokens(t *testing.T) {
	srv := reviewOnlyServer(fakeService{})
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/verification/applications", ""},
		{http.MethodGet, "/v1/verification/applications/7", ""},
		{http.MethodGet, "/v1/verification/counts", ""},
		{http.MethodPost, "/v1/verification/applications/7/claim", `{}`},
		{http.MethodPost, "/v1/verification/applications/7/approve", `{}`},
		{http.MethodPost, "/v1/verification/applications/7/reject", `{}`},
		{http.MethodPost, "/v1/verification/revoke", `{}`},
	}
	for _, item := range cases {
		for _, token := range []string{"", "not-a-configured-token"} {
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, verificationRequest(item.method, item.path, token, item.body))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s token=%q status=%d, want 401", item.method, item.path, token, rec.Code)
			}
		}
	}
}

func TestVerificationRoutesRefuseScopedTokenWithoutThePermission(t *testing.T) {
	srv := reviewOnlyServer(fakeService{})
	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/verification/applications", ""},
		{http.MethodGet, "/v1/verification/counts", ""},
		{http.MethodPost, "/v1/verification/applications/7/claim", `{}`},
	}
	for _, item := range cases {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(item.method, item.path, "scoped-other", item.body))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d body=%s, want 403", item.method, item.path, rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode 403 body: %v", err)
		}
		if body["code"] != CodeForbidden || body["permission"] != PermissionVerificationReview {
			t.Fatalf("403 body=%+v, want the missing permission named", body)
		}
	}
}

func TestVerificationRevokeNeedsTheRevokePermissionOnTopOfReview(t *testing.T) {
	svc := &captureVerificationService{}
	srv := reviewOnlyServer(svc)

	// A review-only token reaches the queue but not the revocation.
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/verification/counts", "scoped-review", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("review token on counts status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/verification/revoke", "scoped-review",
		`{"command_id":"c1","actor":"ops","reason":"impersonation","target_type":"channel","target_id":5005}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("review token on revoke status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 403 body: %v", err)
	}
	if body["permission"] != PermissionVerificationRevoke {
		t.Fatalf("403 body=%+v, want verification.revoke named", body)
	}

	// The token that carries both rights gets through.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/verification/revoke", "scoped-revoke",
		`{"command_id":"c2","actor":"ops","reason":"impersonation","target_type":"channel","target_id":5005}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke token status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.revoke.TargetType != domain.VerificationTargetChannel || svc.revoke.TargetID != 5005 ||
		svc.revoke.Reason != "impersonation" {
		t.Fatalf("forwarded revocation=%+v", svc.revoke)
	}
}

func TestMasterTokenKeepsEveryPermissionIncludingTheLegacySurface(t *testing.T) {
	svc := &captureVerificationService{app: domain.VerificationApplication{ID: 7, Version: 2}}
	srv := reviewOnlyServer(svc)

	// The new permissioned routes.
	for _, path := range []string{"/v1/verification/applications", "/v1/verification/counts"} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, path, "master", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("master token on %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/verification/revoke", "master",
		`{"command_id":"c1","actor":"ops","reason":"impersonation","target_type":"bot","target_id":2002}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("master token on revoke status=%d body=%s", rec.Code, rec.Body.String())
	}

	// And every route that predates permissions.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/accounts/set-verified", "master",
		`{"command_id":"c2","actor":"ops","reason":"official","dry_run":true,"user_id":1001,"verified":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("master token on the legacy surface status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// A bounded token must not inherit the routes that predate the permission model:
// that would turn "give the queue bot the review right" into "give it everything".
func TestScopedTokenCannotUseTheLegacySurfaceAsASideDoor(t *testing.T) {
	srv := reviewOnlyServer(fakeService{})
	for _, path := range []string{"/v1/accounts/set-verified", "/v1/accounts/set-frozen", "/v1/bots/delete"} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, path, "scoped-review",
			`{"command_id":"c1","actor":"ops","reason":"x","user_id":1001,"verified":true}`))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("scoped token on %s status=%d body=%s, want 403", path, rec.Code, rec.Body.String())
		}
	}
	// A scoped token that spells out the wildcard is the operator's explicit
	// choice and does reach it.
	wide := &Server{
		token:  "master",
		scoped: []ScopedToken{{Name: "everything", Token: "scoped-all", Permissions: []string{PermissionAll}}},
		svc:    fakeService{},
	}
	rec := httptest.NewRecorder()
	wide.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/accounts/set-verified", "scoped-all",
		`{"command_id":"c1","actor":"ops","reason":"x","dry_run":true,"user_id":1001,"verified":true}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("wildcard scoped token status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestVerificationQueueFilterAndInt64Rendering(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	svc := &captureVerificationService{app: domain.VerificationApplication{
		ID:              maxInt64,
		ApplicantUserID: maxInt64,
		TargetType:      domain.VerificationTargetChannel,
		TargetID:        maxInt64,
		TargetTitle:     "Example News",
		TargetUsername:  "examplenews",
		Category:        "media",
		Status:          domain.VerificationStatusSubmitted,
		SocialLinks:     []string{"https://example.test/social"},
		Version:         maxInt64,
		CreatedAt:       time.Unix(1_700_000_000, 0).UTC(),
	}}
	srv := reviewOnlyServer(svc)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(
		http.MethodGet,
		"/v1/verification/applications?status=submitted,in_review&target_type=channel&reviewer=alice&q=examplenews&limit=25&before_id=99",
		"scoped-review", "",
	))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(svc.filter.Statuses) != 2 ||
		svc.filter.Statuses[0] != domain.VerificationStatusSubmitted ||
		svc.filter.Statuses[1] != domain.VerificationStatusInReview ||
		svc.filter.TargetType != domain.VerificationTargetChannel ||
		svc.filter.Reviewer != "alice" || svc.filter.Query != "examplenews" ||
		svc.filter.Limit != 25 || svc.filter.BeforeID != 99 {
		t.Fatalf("filter=%+v", svc.filter)
	}
	var body struct {
		Applications []map[string]any `json:"applications"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode queue: %v", err)
	}
	if len(body.Applications) != 1 {
		t.Fatalf("applications=%+v", body.Applications)
	}
	for _, field := range []string{"id", "applicant_user_id", "target_id", "version"} {
		if body.Applications[0][field] != "9223372036854775807" {
			t.Fatalf("%s = %#v, want an exact decimal string", field, body.Applications[0][field])
		}
	}
}

func TestVerificationQueueRejectsUnmodelledFilters(t *testing.T) {
	srv := reviewOnlyServer(fakeService{})
	for _, query := range []string{"?status=pending", "?target_type=group"} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/verification/applications"+query, "scoped-review", ""))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s, want 400", query, rec.Code, rec.Body.String())
		}
	}
}

func TestVerificationApplicationDetailAndCounts(t *testing.T) {
	svc := &captureVerificationService{
		app: domain.VerificationApplication{ID: 7, TargetType: domain.VerificationTargetBot, TargetID: 2002, Version: 4},
		events: []domain.VerificationApplicationEvent{{
			ID: 11, ApplicationID: 7, Kind: domain.VerificationEventSubmitted,
			ToStatus: domain.VerificationStatusSubmitted, CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		}},
		target: domain.VerificationTarget{Type: domain.VerificationTargetBot, ID: 2002, Verified: true, Eligible: false, Reason: "already verified"},
		counts: domain.VerificationStatusCounts{domain.VerificationStatusSubmitted: 3},
	}
	srv := reviewOnlyServer(svc)

	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/verification/applications/7", "scoped-review", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Application map[string]any   `json:"application"`
		Events      []map[string]any `json:"events"`
		Target      map[string]any   `json:"target"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Application["id"] != "7" || len(detail.Events) != 1 || detail.Events[0]["id"] != "11" {
		t.Fatalf("detail=%+v", detail)
	}
	if detail.Target["verified"] != true || detail.Target["eligible"] != false {
		t.Fatalf("target=%+v, want the live snapshot alongside the record", detail.Target)
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/verification/applications/8", "scoped-review", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing application status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/verification/counts", "scoped-review", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("counts status=%d body=%s", rec.Code, rec.Body.String())
	}
	var counts struct {
		Counts map[string]string `json:"counts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &counts); err != nil {
		t.Fatalf("decode counts: %v", err)
	}
	// Every modelled status is present so the panel never tells "zero" from
	// "absent", and the values are decimal strings.
	if counts.Counts["submitted"] != "3" || counts.Counts["draft"] != "0" ||
		counts.Counts["cancelled"] != "0" || len(counts.Counts) != 6 {
		t.Fatalf("counts=%+v", counts.Counts)
	}
}

func TestVerificationDecisionTakesTheApplicationIDFromThePath(t *testing.T) {
	svc := &captureVerificationService{app: domain.VerificationApplication{ID: 7, Version: 4}}
	srv := reviewOnlyServer(svc)
	rec := httptest.NewRecorder()
	// The body names a different application on purpose: the path has to win, or
	// the URL would lie to the audit trail.
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/verification/applications/7/approve", "scoped-review",
		`{"command_id":"c1","actor":"alice","reason":"verified","application_id":99,"version":4,"internal_note":"handover"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.approve.ApplicationID != 7 || svc.approve.Version != 4 ||
		svc.approve.InternalNote != "handover" || svc.approve.Actor != "alice" {
		t.Fatalf("forwarded approval=%+v", svc.approve)
	}
}

func TestVerificationDecisionDryRunIsForwarded(t *testing.T) {
	svc := &captureVerificationService{app: domain.VerificationApplication{ID: 7, Version: 4}}
	srv := reviewOnlyServer(svc)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/verification/applications/7/reject", "scoped-review",
		`{"command_id":"dry-1","actor":"alice","reason":"press links are self-published","dry_run":true,"version":4}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.reject.DryRun || svc.reject.Reason != "press links are self-published" {
		t.Fatalf("forwarded rejection=%+v", svc.reject)
	}
	if !strings.Contains(rec.Body.String(), `"dry_run":true`) {
		t.Fatalf("body=%s, want the dry run echoed", rec.Body.String())
	}
}

func TestVerificationVersionConflictIsAnswered409(t *testing.T) {
	svc := &captureVerificationService{
		app: domain.VerificationApplication{ID: 7, Version: 5},
		// The shape admin.codedError produces for a lost race.
		decideOn: fmt.Errorf("%s: %w", admin.CodeVerificationConflict, domain.ErrVerificationVersionConflict),
	}
	srv := reviewOnlyServer(svc)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/verification/applications/7/approve", "scoped-review",
		`{"command_id":"c1","actor":"alice","reason":"verified","version":4}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409 for a lost optimistic-locking race", rec.Code, rec.Body.String())
	}
	var result admin.CommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if !strings.Contains(result.Error, admin.CodeVerificationConflict) {
		t.Fatalf("result=%+v, want the stable conflict code", result)
	}
	if !strings.Contains(result.Message, "reload") {
		t.Fatalf("result message=%q, want an actionable message", result.Message)
	}
}

func TestVerificationErrorStatusMapping(t *testing.T) {
	cases := map[string]int{
		admin.CodeVerificationNotFound:         http.StatusNotFound,
		admin.CodeVerificationConflict:         http.StatusConflict,
		admin.CodeVerificationTargetOccupied:   http.StatusConflict,
		admin.CodeVerificationTargetVerified:   http.StatusConflict,
		admin.CodeVerificationStatusInvalid:    http.StatusBadRequest,
		admin.CodeVerificationReasonRequired:   http.StatusBadRequest,
		admin.CodeVerificationTargetInvalid:    http.StatusBadRequest,
		admin.CodeVerificationTargetRestricted: http.StatusBadRequest,
		admin.CodeVerificationTargetSystem:     http.StatusBadRequest,
		admin.CodeVerificationNotOwner:         http.StatusBadRequest,
		admin.CodeVerificationInvalid:          http.StatusBadRequest,
		"":                                     http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := verificationErrorStatus(code); got != want {
			t.Fatalf("verificationErrorStatus(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestScopedTokenNameBecomesTheAuditActorWhenNoneIsStated(t *testing.T) {
	svc := &captureVerificationService{app: domain.VerificationApplication{ID: 7, Version: 4}}
	srv := reviewOnlyServer(svc)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/verification/applications/7/claim", "scoped-review",
		`{"command_id":"c1","reason":"queue sweep","version":4}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The scoped token's configured name is its audit identity.
	if svc.claim.Actor != "queue-bot" {
		t.Fatalf("actor=%q, want the scoped token name", svc.claim.Actor)
	}

	// A stated actor is never overwritten, which is how the panel attributes an
	// action to the signed-in operator.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/verification/applications/7/claim", "scoped-review",
		`{"command_id":"c2","actor":"alice","reason":"queue sweep","version":4}`))
	if rec.Code != http.StatusOK || svc.claim.Actor != "alice" {
		t.Fatalf("status=%d actor=%q", rec.Code, svc.claim.Actor)
	}

	// The master token has no name, so the caller keeps having to say who acts.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/verification/applications/7/claim", "master",
		`{"command_id":"c3","reason":"queue sweep","version":4}`))
	if rec.Code != http.StatusOK || svc.claim.Actor != "" {
		t.Fatalf("master token status=%d actor=%q, want no invented identity", rec.Code, svc.claim.Actor)
	}
}

func TestPermissionSetWildcardAndMembership(t *testing.T) {
	all := newPermissionSet([]string{PermissionAll})
	if !all.Has(PermissionVerificationReview) || !all.Has("anything.at.all") {
		t.Fatal("wildcard set refused a permission")
	}
	bounded := newPermissionSet([]string{" verification.review ", ""})
	if !bounded.Has(PermissionVerificationReview) {
		t.Fatal("bounded set dropped a padded permission")
	}
	if bounded.Has(PermissionVerificationRevoke) {
		t.Fatal("bounded set granted an unlisted permission")
	}
	if newPermissionSet(nil).Has(PermissionVerificationReview) {
		t.Fatal("empty set granted a permission")
	}
}
