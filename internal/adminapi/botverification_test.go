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

// fakeService gains the third-party verification surface here so the shared fake
// keeps satisfying Service without touching the existing test files.

func (fakeService) GrantBotVerifier(_ context.Context, req admin.GrantBotVerifierRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) SetBotVerifierEnabled(_ context.Context, req admin.SetBotVerifierEnabledRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) RevokeBotVerifier(_ context.Context, req admin.RevokeBotVerifierRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) UpsertVerificationIcon(_ context.Context, req admin.UpsertVerificationIconRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) SetVerificationIconActive(_ context.Context, req admin.SetVerificationIconActiveRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) RevokeCustomVerification(_ context.Context, req admin.RevokeCustomVerificationRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) ApproveBotVerification(_ context.Context, req admin.ApproveBotVerificationRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) RejectBotVerification(_ context.Context, req admin.RejectBotVerificationRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) RevokeBotVerification(_ context.Context, req admin.RevokeBotVerificationRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) BotVerifiers(context.Context, bool, int) ([]domain.BotVerifierSettings, error) {
	return nil, nil
}

func (fakeService) BotVerifier(context.Context, int64) (domain.BotVerifierSettings, error) {
	return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
}

func (fakeService) VerificationIcons(context.Context, bool, int) ([]domain.VerificationIcon, error) {
	return nil, nil
}

func (fakeService) CustomVerifications(context.Context, domain.CustomVerificationFilter) ([]domain.CustomVerification, error) {
	return nil, nil
}

func (fakeService) CustomVerificationRequests(context.Context, domain.CustomVerificationRequestFilter) ([]domain.CustomVerificationRequest, error) {
	return nil, nil
}

func (fakeService) CustomVerificationRequest(context.Context, int64) (domain.CustomVerificationRequest, error) {
	return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
}

func (fakeService) CustomVerificationRequestCounts(context.Context) (map[domain.CustomVerificationRequestStatus]int64, error) {
	return nil, nil
}

func (fakeService) CustomVerificationMarkActive(context.Context, int64, domain.Peer) (bool, error) {
	return false, nil
}

type captureBotVerificationService struct {
	fakeService
	verifier   domain.BotVerifierSettings
	hasRow     bool
	icons      []domain.VerificationIcon
	marks      []domain.CustomVerification
	request    domain.CustomVerificationRequest
	counts     map[domain.CustomVerificationRequestStatus]int64
	markActive bool

	iconFilterActiveOnly  bool
	verifierFilterEnabled bool
	verifierFilterLimit   int
	markFilter            domain.CustomVerificationFilter
	requestFilter         domain.CustomVerificationRequestFilter
	grant                 admin.GrantBotVerifierRequest
	setEnabled            admin.SetBotVerifierEnabledRequest
	revokeVerifier        admin.RevokeBotVerifierRequest
	upsertIcon            admin.UpsertVerificationIconRequest
	setIconActive         admin.SetVerificationIconActiveRequest
	revokeMark            admin.RevokeCustomVerificationRequest
	approve               admin.ApproveBotVerificationRequest
	reject                admin.RejectBotVerificationRequest
	revokeRequest         admin.RevokeBotVerificationRequest
	commandErr            error
}

func (s *captureBotVerificationService) BotVerifiers(_ context.Context, enabledOnly bool, limit int) ([]domain.BotVerifierSettings, error) {
	s.verifierFilterEnabled = enabledOnly
	s.verifierFilterLimit = limit
	if !s.hasRow {
		return nil, nil
	}
	return []domain.BotVerifierSettings{s.verifier}, nil
}

func (s *captureBotVerificationService) BotVerifier(_ context.Context, botID int64) (domain.BotVerifierSettings, error) {
	if !s.hasRow || s.verifier.BotID != botID {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	return s.verifier, nil
}

func (s *captureBotVerificationService) VerificationIcons(_ context.Context, activeOnly bool, _ int) ([]domain.VerificationIcon, error) {
	s.iconFilterActiveOnly = activeOnly
	return s.icons, nil
}

func (s *captureBotVerificationService) CustomVerifications(_ context.Context, filter domain.CustomVerificationFilter) ([]domain.CustomVerification, error) {
	s.markFilter = filter
	return s.marks, nil
}

func (s *captureBotVerificationService) CustomVerificationRequests(_ context.Context, filter domain.CustomVerificationRequestFilter) ([]domain.CustomVerificationRequest, error) {
	s.requestFilter = filter
	return []domain.CustomVerificationRequest{s.request}, nil
}

func (s *captureBotVerificationService) CustomVerificationRequest(_ context.Context, requestID int64) (domain.CustomVerificationRequest, error) {
	if s.request.ID != requestID {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	return s.request, nil
}

func (s *captureBotVerificationService) CustomVerificationRequestCounts(context.Context) (map[domain.CustomVerificationRequestStatus]int64, error) {
	return s.counts, nil
}

func (s *captureBotVerificationService) CustomVerificationMarkActive(context.Context, int64, domain.Peer) (bool, error) {
	return s.markActive, nil
}

func (s *captureBotVerificationService) commandResult(commandID string, dryRun bool) (admin.CommandResult, error) {
	if s.commandErr != nil {
		return admin.CommandResult{CommandID: commandID, Status: "failed", Error: s.commandErr.Error()}, s.commandErr
	}
	return admin.CommandResult{CommandID: commandID, Status: "completed", DryRun: dryRun}, nil
}

func (s *captureBotVerificationService) GrantBotVerifier(_ context.Context, req admin.GrantBotVerifierRequest) (admin.CommandResult, error) {
	s.grant = req
	return s.commandResult(req.CommandID, req.DryRun)
}

func (s *captureBotVerificationService) SetBotVerifierEnabled(_ context.Context, req admin.SetBotVerifierEnabledRequest) (admin.CommandResult, error) {
	s.setEnabled = req
	return s.commandResult(req.CommandID, req.DryRun)
}

func (s *captureBotVerificationService) RevokeBotVerifier(_ context.Context, req admin.RevokeBotVerifierRequest) (admin.CommandResult, error) {
	s.revokeVerifier = req
	return s.commandResult(req.CommandID, req.DryRun)
}

func (s *captureBotVerificationService) UpsertVerificationIcon(_ context.Context, req admin.UpsertVerificationIconRequest) (admin.CommandResult, error) {
	s.upsertIcon = req
	return s.commandResult(req.CommandID, req.DryRun)
}

func (s *captureBotVerificationService) SetVerificationIconActive(_ context.Context, req admin.SetVerificationIconActiveRequest) (admin.CommandResult, error) {
	s.setIconActive = req
	return s.commandResult(req.CommandID, req.DryRun)
}

func (s *captureBotVerificationService) RevokeCustomVerification(_ context.Context, req admin.RevokeCustomVerificationRequest) (admin.CommandResult, error) {
	s.revokeMark = req
	return s.commandResult(req.CommandID, req.DryRun)
}

func (s *captureBotVerificationService) ApproveBotVerification(_ context.Context, req admin.ApproveBotVerificationRequest) (admin.CommandResult, error) {
	s.approve = req
	return s.commandResult(req.CommandID, req.DryRun)
}

func (s *captureBotVerificationService) RejectBotVerification(_ context.Context, req admin.RejectBotVerificationRequest) (admin.CommandResult, error) {
	s.reject = req
	return s.commandResult(req.CommandID, req.DryRun)
}

func (s *captureBotVerificationService) RevokeBotVerification(_ context.Context, req admin.RevokeBotVerificationRequest) (admin.CommandResult, error) {
	s.revokeRequest = req
	return s.commandResult(req.CommandID, req.DryRun)
}

// botVerificationServer is the deployment shape the permission model exists for:
// one master token plus bounded tokens that can review, manage, or neither.
func botVerificationServer(svc Service) *Server {
	return &Server{
		token: "master",
		scoped: []ScopedToken{
			{Name: "queue-bot", Token: "scoped-review", Permissions: []string{PermissionBotVerificationReview}},
			{Name: "trust-and-safety", Token: "scoped-manage", Permissions: []string{PermissionBotVerificationManage}},
			{Name: "both", Token: "scoped-both", Permissions: []string{
				PermissionBotVerificationReview, PermissionBotVerificationManage,
			}},
			// A token for the *official* review surface: it must not reach this one.
			{Name: "official-review", Token: "scoped-official", Permissions: []string{PermissionVerificationReview}},
		},
		svc: svc,
	}
}

// botVerificationRoute is one route with a body the handler accepts, so an
// authorisation test cannot pass by accident on a malformed payload.
type botVerificationRoute struct {
	method string
	path   string
	body   string
}

const decisionBody = `{"command_id":"c1","actor":"ops","reason":"decided","version":2}`

var botVerificationReadRoutes = []botVerificationRoute{
	{http.MethodGet, "/v1/botverification/verifiers", ""},
	{http.MethodGet, "/v1/botverification/icons", ""},
	{http.MethodGet, "/v1/botverification/marks", ""},
	{http.MethodGet, "/v1/botverification/requests", ""},
	{http.MethodGet, "/v1/botverification/requests/7", ""},
	{http.MethodGet, "/v1/botverification/counts", ""},
	{http.MethodPost, "/v1/botverification/requests/7/approve", decisionBody},
	{http.MethodPost, "/v1/botverification/requests/7/reject", decisionBody},
	{http.MethodPost, "/v1/botverification/requests/7/revoke", decisionBody},
}

var botVerificationManageRoutes = []botVerificationRoute{
	{http.MethodPost, "/v1/botverification/verifiers/grant",
		`{"command_id":"c1","actor":"ops","reason":"partner","bot_id":3003,"icon_document_id":900,"company_name":"Example Trust","version":4}`},
	{http.MethodPost, "/v1/botverification/verifiers/set-enabled",
		`{"command_id":"c1","actor":"ops","reason":"abuse","bot_id":3003,"enabled":false}`},
	{http.MethodPost, "/v1/botverification/verifiers/revoke",
		`{"command_id":"c1","actor":"ops","reason":"programme ended","bot_id":3003}`},
	{http.MethodPost, "/v1/botverification/icons/upsert",
		`{"command_id":"c1","actor":"ops","reason":"new icon","document_id":900,"name":"blue check"}`},
	{http.MethodPost, "/v1/botverification/icons/set-active",
		`{"command_id":"c1","actor":"ops","reason":"retired","icon_id":501,"active":false}`},
	{http.MethodPost, "/v1/botverification/marks/revoke",
		`{"command_id":"c1","actor":"ops","reason":"impersonation","verifier_bot_id":3003,"peer_type":"channel","peer_id":5005}`},
}

// botVerificationRoutes is every route in the section.
func botVerificationRoutes() []botVerificationRoute {
	out := make([]botVerificationRoute, 0, len(botVerificationReadRoutes)+len(botVerificationManageRoutes))
	out = append(out, botVerificationReadRoutes...)
	return append(out, botVerificationManageRoutes...)
}

func TestBotVerificationRoutesRejectMissingAndUnknownTokens(t *testing.T) {
	srv := botVerificationServer(fakeService{})
	for _, item := range botVerificationRoutes() {
		for _, token := range []string{"", "not-a-configured-token"} {
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, verificationRequest(item.method, item.path, token, item.body))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s token=%q status=%d, want 401", item.method, item.path, token, rec.Code)
			}
		}
	}
}

func TestBotVerificationRoutesRefuseScopedTokenWithoutThePermission(t *testing.T) {
	srv := botVerificationServer(fakeService{})
	// The official-verification token is the interesting negative: the two
	// mechanisms are separate, so verification.review must not open this surface.
	for _, token := range []string{"scoped-official", "scoped-manage"} {
		for _, item := range botVerificationReadRoutes {
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, verificationRequest(item.method, item.path, token, item.body))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s token=%q status=%d body=%s, want 403", item.method, item.path, token, rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode 403 body: %v", err)
			}
			if body["code"] != CodeForbidden || body["permission"] != PermissionBotVerificationReview {
				t.Fatalf("403 body=%+v, want botverification.review named", body)
			}
		}
	}
	// And the review right alone does not reach the configuration half.
	for _, token := range []string{"scoped-official", "scoped-review"} {
		for _, item := range botVerificationManageRoutes {
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, verificationRequest(item.method, item.path, token, item.body))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s token=%q status=%d body=%s, want 403", item.method, item.path, token, rec.Code, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode 403 body: %v", err)
			}
			if body["permission"] != PermissionBotVerificationManage {
				t.Fatalf("403 body=%+v, want botverification.manage named", body)
			}
		}
	}
}

// A token holding the third-party rights must not reach the official queue either:
// the separation is symmetric.
func TestBotVerificationTokenCannotReachTheOfficialVerificationSurface(t *testing.T) {
	srv := botVerificationServer(fakeService{})
	for _, path := range []string{"/v1/verification/applications", "/v1/verification/counts"} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, path, "scoped-both", ""))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s, want 403", path, rec.Code, rec.Body.String())
		}
	}
	// Nor the legacy surface that predates permissions.
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/accounts/set-verified", "scoped-both",
		`{"command_id":"c1","actor":"ops","reason":"x","user_id":1001,"verified":true}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("legacy surface status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
}

func TestBotVerificationScopedTokensReachTheirOwnHalf(t *testing.T) {
	svc := &captureBotVerificationService{request: domain.CustomVerificationRequest{ID: 7, Version: 2}}
	srv := botVerificationServer(svc)

	for _, item := range botVerificationReadRoutes {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(item.method, item.path, "scoped-review", item.body))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", item.method, item.path, rec.Code, rec.Body.String())
		}
	}
	for _, item := range botVerificationManageRoutes {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(item.method, item.path, "scoped-manage", item.body))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s status=%d body=%s", item.method, item.path, rec.Code, rec.Body.String())
		}
	}
}

func TestMasterTokenReachesTheBotVerificationSurface(t *testing.T) {
	svc := &captureBotVerificationService{request: domain.CustomVerificationRequest{ID: 7, Version: 2}}
	srv := botVerificationServer(svc)
	for _, item := range botVerificationRoutes() {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(item.method, item.path, "master", item.body))
		if rec.Code != http.StatusOK {
			t.Fatalf("master on %s %s status=%d body=%s", item.method, item.path, rec.Code, rec.Body.String())
		}
	}
}

func TestBotVerifierListRendersInt64AsDecimalStrings(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	svc := &captureBotVerificationService{
		hasRow: true,
		verifier: domain.BotVerifierSettings{
			BotID:                      maxInt64,
			IconDocumentID:             maxInt64,
			CompanyName:                "Example Trust",
			DefaultDescription:         "verified by Example Trust",
			CanModifyCustomDescription: true,
			Enabled:                    true,
			GrantedBy:                  "alice",
			GrantReason:                "partner programme",
			Version:                    maxInt64,
			CreatedAt:                  time.Unix(1_700_000_000, 0).UTC(),
			UpdatedAt:                  time.Unix(1_700_000_000, 0).UTC(),
		},
	}
	srv := botVerificationServer(svc)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet,
		"/v1/botverification/verifiers?enabled_only=1&limit=25", "scoped-review", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.verifierFilterEnabled || svc.verifierFilterLimit != 25 {
		t.Fatalf("enabledOnly=%v limit=%d, want the query honoured", svc.verifierFilterEnabled, svc.verifierFilterLimit)
	}
	var body struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode verifiers: %v", err)
	}
	if len(body.Rows) != 1 {
		t.Fatalf("rows=%+v", body.Rows)
	}
	for _, field := range []string{"BotID", "IconDocumentID", "Version"} {
		if body.Rows[0][field] != "9223372036854775807" {
			t.Fatalf("%s = %#v, want an exact decimal string", field, body.Rows[0][field])
		}
	}
	if body.Rows[0]["CanModifyCustomDescription"] != true || body.Rows[0]["Enabled"] != true {
		t.Fatalf("row=%+v, want the booleans as booleans", body.Rows[0])
	}
}

func TestVerificationIconAndMarkListingsRenderInt64AsDecimalStrings(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	svc := &captureBotVerificationService{
		icons: []domain.VerificationIcon{{
			ID: maxInt64, DocumentID: maxInt64, OwnerBotID: maxInt64, Name: "blue check", Active: true,
			CreatedAt: time.Unix(1_700_000_000, 0).UTC(),
		}},
		marks: []domain.CustomVerification{{
			ID: maxInt64, VerifierBotID: maxInt64,
			Peer:           domain.Peer{Type: domain.PeerTypeChannel, ID: maxInt64},
			IconDocumentID: maxInt64, Description: "verified partner", Version: maxInt64,
		}},
	}
	srv := botVerificationServer(svc)

	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet,
		"/v1/botverification/icons?active_only=true", "scoped-review", ""))
	if rec.Code != http.StatusOK || !svc.iconFilterActiveOnly {
		t.Fatalf("icons status=%d activeOnly=%v body=%s", rec.Code, svc.iconFilterActiveOnly, rec.Body.String())
	}
	var icons struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &icons); err != nil {
		t.Fatalf("decode icons: %v", err)
	}
	for _, field := range []string{"ID", "DocumentID", "OwnerBotID"} {
		if icons.Rows[0][field] != "9223372036854775807" {
			t.Fatalf("icon %s = %#v", field, icons.Rows[0][field])
		}
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet,
		"/v1/botverification/marks?verifier_bot_id=9223372036854775807&peer_type=channel&q=news&limit=1&before_id=99",
		"scoped-review", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("marks status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.markFilter.VerifierBotID != maxInt64 || svc.markFilter.PeerType != domain.PeerTypeChannel ||
		svc.markFilter.Query != "news" || svc.markFilter.Limit != 1 || svc.markFilter.BeforeID != 99 {
		t.Fatalf("mark filter=%+v", svc.markFilter)
	}
	var marks struct {
		Rows         []map[string]any `json:"rows"`
		HasMore      bool             `json:"has_more"`
		NextBeforeID string           `json:"next_before_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &marks); err != nil {
		t.Fatalf("decode marks: %v", err)
	}
	for _, field := range []string{"ID", "VerifierBotID", "PeerID", "IconDocumentID", "Version"} {
		if marks.Rows[0][field] != "9223372036854775807" {
			t.Fatalf("mark %s = %#v", field, marks.Rows[0][field])
		}
	}
	// A full page reports more, and the cursor is the last id as a decimal string.
	if !marks.HasMore || marks.NextBeforeID != "9223372036854775807" {
		t.Fatalf("paging hasMore=%v next=%q", marks.HasMore, marks.NextBeforeID)
	}
}

func TestBotVerificationQueueFilterAndUnmodelledValues(t *testing.T) {
	svc := &captureBotVerificationService{request: domain.CustomVerificationRequest{
		ID: 88, VerifierBotID: 3003, ApplicantUserID: 1001,
		Peer:   domain.Peer{Type: domain.PeerTypeChannel, ID: 5005},
		Status: domain.CustomVerificationPending, Version: 3,
	}}
	srv := botVerificationServer(svc)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet,
		"/v1/botverification/requests?status=pending,approved&verifier_bot_id=3003&peer_type=channel&q=news&limit=25&before_id=99",
		"scoped-review", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(svc.requestFilter.Statuses) != 2 ||
		svc.requestFilter.Statuses[0] != domain.CustomVerificationPending ||
		svc.requestFilter.Statuses[1] != domain.CustomVerificationApproved ||
		svc.requestFilter.VerifierBotID != 3003 || svc.requestFilter.PeerType != domain.PeerTypeChannel ||
		svc.requestFilter.Query != "news" || svc.requestFilter.Limit != 25 || svc.requestFilter.BeforeID != 99 {
		t.Fatalf("filter=%+v", svc.requestFilter)
	}

	// An unmodelled status or peer type is a 400 rather than an empty result, so a
	// typo is reported instead of silently returning nothing.
	for _, query := range []string{"?status=in_review", "?peer_type=chat", "?verifier_bot_id=abc", "?before_id=-1"} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/botverification/requests"+query, "scoped-review", ""))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s, want 400", query, rec.Code, rec.Body.String())
		}
	}
	for _, query := range []string{"?peer_type=chat"} {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/botverification/marks"+query, "scoped-review", ""))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("marks %s status=%d, want 400", query, rec.Code)
		}
	}
}

func TestBotVerificationRequestDetailAndCounts(t *testing.T) {
	svc := &captureBotVerificationService{
		hasRow: true,
		verifier: domain.BotVerifierSettings{
			BotID: 3003, IconDocumentID: 900, CompanyName: "Example Trust", Enabled: true, Version: 4,
		},
		request: domain.CustomVerificationRequest{
			ID: 88, VerifierBotID: 3003, ApplicantUserID: 1001,
			Peer:         domain.Peer{Type: domain.PeerTypeChannel, ID: 5005},
			PeerTitle:    "Example News",
			PeerUsername: "examplenews",
			InternalNote: "operator only",
			Status:       domain.CustomVerificationApproved, Version: 5,
			ApprovedAt: time.Unix(1_700_000_000, 0).UTC(),
		},
		markActive: true,
		counts:     map[domain.CustomVerificationRequestStatus]int64{domain.CustomVerificationPending: 3, domain.CustomVerificationApproved: 1},
	}
	srv := botVerificationServer(svc)

	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/botverification/requests/88", "scoped-review", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail struct {
		Request    map[string]any `json:"request"`
		Verifier   map[string]any `json:"verifier"`
		MarkActive bool           `json:"mark_active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Request["ID"] != "88" || detail.Request["PeerID"] != "5005" || detail.Request["Version"] != "5" ||
		detail.Request["InternalNote"] != "operator only" || detail.Request["ApprovedAt"] == nil {
		t.Fatalf("request=%+v", detail.Request)
	}
	if detail.Verifier["BotID"] != "3003" || detail.Verifier["CompanyName"] != "Example Trust" || !detail.MarkActive {
		t.Fatalf("verifier=%+v markActive=%v", detail.Verifier, detail.MarkActive)
	}

	// A verifier revoked since the application was filed must not turn the audit
	// record into a 500: the row is reported with only its id.
	svc.hasRow = false
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/botverification/requests/88", "scoped-review", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail without a verifier status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Verifier["BotID"] != "3003" || detail.Verifier["Enabled"] != false {
		t.Fatalf("verifier=%+v, want the bot named and no status claimed", detail.Verifier)
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/botverification/requests/89", "scoped-review", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing application status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodGet, "/v1/botverification/counts", "scoped-review", ""))
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
	if counts.Counts["pending"] != "3" || counts.Counts["approved"] != "1" ||
		counts.Counts["rejected"] != "0" || counts.Counts["revoked"] != "0" || len(counts.Counts) != 4 {
		t.Fatalf("counts=%+v", counts.Counts)
	}
}

func TestBotVerificationDecisionTakesTheRequestIDFromThePath(t *testing.T) {
	svc := &captureBotVerificationService{request: domain.CustomVerificationRequest{ID: 88, Version: 3}}
	srv := botVerificationServer(svc)
	// The body names a different application on purpose: the path has to win, or
	// the URL would lie to the audit trail.
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/requests/88/approve", "scoped-review",
		`{"command_id":"c1","actor":"alice","reason":"verified","request_id":99,"version":3,"internal_note":"handover"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.approve.RequestID != 88 || svc.approve.Version != 3 ||
		svc.approve.InternalNote != "handover" || svc.approve.Actor != "alice" {
		t.Fatalf("forwarded approval=%+v", svc.approve)
	}
}

func TestBotVerificationDryRunIsForwardedAndEchoed(t *testing.T) {
	svc := &captureBotVerificationService{request: domain.CustomVerificationRequest{ID: 88, Version: 3}}
	srv := botVerificationServer(svc)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/requests/88/reject", "scoped-review",
		`{"command_id":"dry-1","actor":"alice","reason":"not an outlet","dry_run":true,"version":3}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.reject.DryRun || svc.reject.Reason != "not an outlet" {
		t.Fatalf("forwarded rejection=%+v", svc.reject)
	}
	if !strings.Contains(rec.Body.String(), `"dry_run":true`) {
		t.Fatalf("body=%s, want the dry run echoed", rec.Body.String())
	}

	// Also on the manage half: appointing a verifier is rehearsable too.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/verifiers/grant", "scoped-manage",
		`{"command_id":"dry-2","actor":"alice","reason":"partner","dry_run":true,
		  "bot_id":3003,"icon_document_id":900,"company_name":"Example Trust","version":4}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("grant status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !svc.grant.DryRun || svc.grant.BotID != 3003 || svc.grant.IconDocumentID != 900 || svc.grant.Version != 4 {
		t.Fatalf("forwarded grant=%+v, want the exact int64s from decimal strings", svc.grant)
	}
}

func TestBotVerificationVersionConflictIsAnswered409(t *testing.T) {
	svc := &captureBotVerificationService{
		request: domain.CustomVerificationRequest{ID: 88, Version: 5},
		// The shape admin.codedError produces for a lost race.
		commandErr: fmt.Errorf("%s: %w", admin.CodeCustomVerificationConflict, domain.ErrCustomVerificationVersionConflict),
	}
	srv := botVerificationServer(svc)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/requests/88/approve", "scoped-review",
		`{"command_id":"c1","actor":"alice","reason":"verified","version":4}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409 for a lost optimistic-locking race", rec.Code, rec.Body.String())
	}
	var result admin.CommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode conflict: %v", err)
	}
	if !strings.Contains(result.Error, admin.CodeCustomVerificationConflict) {
		t.Fatalf("result=%+v, want the stable conflict code", result)
	}
	if !strings.Contains(result.Message, "reload") {
		t.Fatalf("result message=%q, want an actionable message", result.Message)
	}

	// The same on the manage half, where two operators can race a verifier row.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/verifiers/grant", "scoped-manage",
		`{"command_id":"c2","actor":"alice","reason":"partner","bot_id":3003,"icon_document_id":900,"company_name":"x","version":3}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("grant conflict status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
}

func TestBotVerificationErrorStatusMapping(t *testing.T) {
	cases := map[string]int{
		admin.CodeBotVerifierNotFound:               http.StatusNotFound,
		admin.CodeBotVerifierBotNotFound:            http.StatusNotFound,
		admin.CodeVerificationIconNotFound:          http.StatusNotFound,
		admin.CodeCustomVerificationNotFound:        http.StatusNotFound,
		admin.CodeCustomVerificationRequestNotFound: http.StatusNotFound,
		admin.CodeCustomVerificationConflict:        http.StatusConflict,
		admin.CodeCustomVerificationLimit:           http.StatusConflict,
		admin.CodeCustomVerificationRequestExists:   http.StatusConflict,
		admin.CodeCustomVerificationRateLimited:     http.StatusTooManyRequests,
		admin.CodeBotVerifierForbidden:              http.StatusBadRequest,
		admin.CodeBotVerifierInvalid:                http.StatusBadRequest,
		admin.CodeVerificationIconInactive:          http.StatusBadRequest,
		admin.CodeVerificationIconInvalid:           http.StatusBadRequest,
		admin.CodeCustomVerificationStatusInvalid:   http.StatusBadRequest,
		admin.CodeCustomVerificationReasonRequired:  http.StatusBadRequest,
		admin.CodeCustomVerificationTargetInvalid:   http.StatusBadRequest,
		admin.CodeCustomVerificationInvalid:         http.StatusBadRequest,
		"":                                          http.StatusInternalServerError,
	}
	for code, want := range cases {
		if got := botVerificationErrorStatus(code); got != want {
			t.Fatalf("botVerificationErrorStatus(%q) = %d, want %d", code, got, want)
		}
	}
}

func TestBotVerificationActionsForwardTheirPayloads(t *testing.T) {
	const maxInt64 = int64(9223372036854775807)
	svc := &captureBotVerificationService{}
	srv := botVerificationServer(svc)

	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/verifiers/set-enabled", "scoped-manage",
		`{"command_id":"c1","actor":"ops","reason":"abuse","bot_id":9223372036854775807,"enabled":false}`))
	if rec.Code != http.StatusOK || svc.setEnabled.BotID != maxInt64 || svc.setEnabled.Enabled {
		t.Fatalf("set-enabled status=%d req=%+v", rec.Code, svc.setEnabled)
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/verifiers/revoke", "scoped-manage",
		`{"command_id":"c2","actor":"ops","reason":"programme ended","bot_id":3003}`))
	if rec.Code != http.StatusOK || svc.revokeVerifier.BotID != 3003 {
		t.Fatalf("revoke-verifier status=%d req=%+v", rec.Code, svc.revokeVerifier)
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/icons/upsert", "scoped-manage",
		`{"command_id":"c3","actor":"ops","reason":"new icon","document_id":9223372036854775807,"name":"blue check","owner_bot_id":3003}`))
	if rec.Code != http.StatusOK || svc.upsertIcon.DocumentID != maxInt64 ||
		svc.upsertIcon.Name != "blue check" || svc.upsertIcon.OwnerBotID != 3003 {
		t.Fatalf("upsert-icon status=%d req=%+v", rec.Code, svc.upsertIcon)
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/icons/set-active", "scoped-manage",
		`{"command_id":"c4","actor":"ops","reason":"retired","icon_id":501,"active":false}`))
	if rec.Code != http.StatusOK || svc.setIconActive.IconID != 501 || svc.setIconActive.Active {
		t.Fatalf("set-icon-active status=%d req=%+v", rec.Code, svc.setIconActive)
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/marks/revoke", "scoped-manage",
		`{"command_id":"c5","actor":"ops","reason":"impersonation","verifier_bot_id":3003,"peer_type":"channel","peer_id":9223372036854775807}`))
	if rec.Code != http.StatusOK || svc.revokeMark.VerifierBotID != 3003 ||
		svc.revokeMark.PeerType != domain.PeerTypeChannel || svc.revokeMark.PeerID != maxInt64 {
		t.Fatalf("revoke-mark status=%d req=%+v", rec.Code, svc.revokeMark)
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/requests/88/revoke", "scoped-review",
		`{"command_id":"c6","actor":"ops","reason":"licence withdrawn","version":9223372036854775807}`))
	if rec.Code != http.StatusOK || svc.revokeRequest.RequestID != 88 || svc.revokeRequest.Version != maxInt64 {
		t.Fatalf("revoke-request status=%d req=%+v", rec.Code, svc.revokeRequest)
	}
}

func TestBotVerificationScopedTokenNameBecomesTheAuditActor(t *testing.T) {
	svc := &captureBotVerificationService{request: domain.CustomVerificationRequest{ID: 88, Version: 3}}
	srv := botVerificationServer(svc)

	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/requests/88/approve", "scoped-review",
		`{"command_id":"c1","reason":"queue sweep","version":3}`))
	if rec.Code != http.StatusOK || svc.approve.Actor != "queue-bot" {
		t.Fatalf("status=%d actor=%q, want the scoped token name", rec.Code, svc.approve.Actor)
	}

	// A stated actor is never overwritten, which is how the panel attributes an
	// action to the signed-in operator.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/requests/88/approve", "scoped-review",
		`{"command_id":"c2","actor":"alice","reason":"queue sweep","version":3}`))
	if rec.Code != http.StatusOK || svc.approve.Actor != "alice" {
		t.Fatalf("status=%d actor=%q", rec.Code, svc.approve.Actor)
	}

	// The master token has no name, so the caller keeps having to say who acts.
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/requests/88/approve", "master",
		`{"command_id":"c3","reason":"queue sweep","version":3}`))
	if rec.Code != http.StatusOK || svc.approve.Actor != "" {
		t.Fatalf("master token status=%d actor=%q, want no invented identity", rec.Code, svc.approve.Actor)
	}
}

func TestBotVerificationCommandsRejectUnknownFields(t *testing.T) {
	srv := botVerificationServer(&captureBotVerificationService{})
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, verificationRequest(http.MethodPost, "/v1/botverification/verifiers/grant", "scoped-manage",
		`{"command_id":"c1","actor":"ops","reason":"x","bot_id":3003,"icon_document_id":900,"company_name":"y","enabled":true}`))
	// enabled is not part of the grant payload: the kill switch is its own action,
	// and a silently ignored field would hide that from the operator.
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "enabled") {
		t.Fatalf("status=%d body=%s, want 400 naming the unknown field", rec.Code, rec.Body.String())
	}
}

func TestBotVerificationPermissionNamesAreDistinctFromTheOfficialOnes(t *testing.T) {
	// The permission model's whole point here: appointing verifiers is not implied
	// by reviewing the official queue, in either direction.
	bounded := newPermissionSet([]string{PermissionBotVerificationReview})
	if !bounded.Has(PermissionBotVerificationReview) {
		t.Fatal("bounded set dropped its own permission")
	}
	if bounded.Has(PermissionBotVerificationManage) || bounded.Has(PermissionVerificationReview) {
		t.Fatalf("botverification.review leaked into another right")
	}
	manage := newPermissionSet([]string{PermissionBotVerificationManage})
	if manage.Has(PermissionBotVerificationReview) {
		t.Fatal("botverification.manage implied the review right")
	}
	all := newPermissionSet([]string{PermissionAll})
	if !all.Has(PermissionBotVerificationReview) || !all.Has(PermissionBotVerificationManage) {
		t.Fatal("the wildcard refused a third-party permission")
	}
}
