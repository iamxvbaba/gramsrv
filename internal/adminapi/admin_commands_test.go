package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"telesrv/internal/domain"
)

type captureAuditService struct {
	fakeService
	limit  int
	actor  string
	cmds   []domain.AdminCommand
	err    error
}

func (s *captureAuditService) ListRecentAdminCommands(_ context.Context, limit int, actor string) ([]domain.AdminCommand, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.limit, s.actor = limit, actor
	return s.cmds, nil
}

func auditRequest(method, target, token string) *http.Request {
	req, _ := http.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestAdminCommandsListHonoursLimitActorAndTrimsFields(t *testing.T) {
	completed := time.Now().UTC().Truncate(time.Second)
	svc := &captureAuditService{cmds: []domain.AdminCommand{{
		CommandID: "bot-abc", Actor: "777", Action: "set_phone", TargetUserID: 42,
		DryRun: false, Status: domain.AdminCommandCompleted, Reason: "Admin number bind",
		Error: "", CreatedAt: completed,
	}}}
	srv := httptest.NewServer((&Server{token: "master", svc: svc}).routes())
	defer srv.Close()

	res, err := srv.Client().Do(auditRequest(http.MethodGet, srv.URL+"/v1/admin-commands?limit=5&actor=777", "master"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}
	if svc.limit != 5 {
		t.Fatalf("limit forwarded is %d, want 5", svc.limit)
	}
	if svc.actor != "777" {
		t.Fatalf("actor forwarded is %q, want 777", svc.actor)
	}
	var body struct {
		Commands []map[string]any `json:"commands"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Commands) != 1 {
		t.Fatalf("got %d commands, want 1", len(body.Commands))
	}
	item := body.Commands[0]
	for key, want := range map[string]any{
		"command_id":     "bot-abc",
		"actor":          "777",
		"action":         "set_phone",
		"dry_run":        false,
		"status":         "completed",
		"target_user_id": float64(42),
		"reason":         "Admin number bind",
		"created_at":     completed.Format(time.RFC3339),
	} {
		if item[key] != want {
			t.Fatalf("field %q = %#v, want %#v", key, item[key], want)
		}
	}
	if _, leaked := item["result"]; leaked {
		t.Fatal("audit response must not include the request/result payloads")
	}
	if _, leaked := item["request"]; leaked {
		t.Fatal("audit response must not include the request/result payloads")
	}
}

func TestAdminCommandsListRequiresAuth(t *testing.T) {
	srv := httptest.NewServer((&Server{token: "master", svc: &captureAuditService{}}).routes())
	defer srv.Close()
	res, err := srv.Client().Do(auditRequest(http.MethodGet, srv.URL+"/v1/admin-commands", ""))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", res.StatusCode)
	}
}

func TestAdminCommandsListServiceError(t *testing.T) {
	svc := &captureAuditService{err: context.DeadlineExceeded}
	srv := httptest.NewServer((&Server{token: "master", svc: svc}).routes())
	defer srv.Close()
	res, err := srv.Client().Do(auditRequest(http.MethodGet, srv.URL+"/v1/admin-commands", "master"))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", res.StatusCode)
	}
}