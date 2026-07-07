package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/admin"
)

func TestAdminAPIRequiresBearerToken(t *testing.T) {
	srv := &Server{token: "secret", svc: fakeService{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/accounts/freeze-send", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

func TestAdminAPIFreezeSend(t *testing.T) {
	srv := &Server{token: "secret", svc: fakeService{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/accounts/freeze-send", strings.NewReader(`{"command_id":"c1","actor":"ops","reason":"test","dry_run":true,"user_id":1001,"frozen":true}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"command_id":"c1"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAdminAPISetVerified(t *testing.T) {
	srv := &Server{token: "secret", svc: fakeService{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/accounts/set-verified", strings.NewReader(`{"command_id":"c2","actor":"ops","reason":"official","dry_run":true,"user_id":1001,"verified":true}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"command_id":"c2"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAdminAPIGrantStars(t *testing.T) {
	srv := &Server{token: "secret", svc: fakeService{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/accounts/grant-stars", strings.NewReader(`{"command_id":"c-stars","actor":"ops","reason":"manual grant","dry_run":true,"user_id":1001,"amount":500}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"command_id":"c-stars"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestAdminAPISetChannelVerified(t *testing.T) {
	srv := &Server{token: "secret", svc: fakeService{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/set-verified", strings.NewReader(`{"command_id":"c3","actor":"ops","reason":"official","dry_run":true,"channel_id":2001,"verified":true}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"command_id":"c3"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

type fakeService struct{}

func (fakeService) SetSendFrozen(_ context.Context, req admin.SetSendFrozenRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) GrantPremium(_ context.Context, req admin.GrantPremiumRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) GrantStars(_ context.Context, req admin.GrantStarsRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) SetVerified(_ context.Context, req admin.SetVerifiedRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) SetChannelVerified(_ context.Context, req admin.SetChannelVerifiedRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) RevokeSessions(context.Context, admin.RevokeSessionsRequest) (admin.CommandResult, error) {
	return admin.CommandResult{}, nil
}

func (fakeService) DeletePrivateMessages(context.Context, admin.DeletePrivateMessagesRequest) (admin.CommandResult, error) {
	return admin.CommandResult{}, nil
}

func (fakeService) DeletePrivateHistory(context.Context, admin.DeletePrivateHistoryRequest) (admin.CommandResult, error) {
	return admin.CommandResult{}, nil
}
