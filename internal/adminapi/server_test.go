package adminapi

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
)

func TestAdminAPIRequiresBearerToken(t *testing.T) {
	srv := &Server{token: "secret", svc: fakeService{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/accounts/set-frozen", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", rec.Code)
	}
}

func TestAdminAPISetAccountFrozen(t *testing.T) {
	svc := &captureFreezeService{}
	srv := &Server{token: "secret", svc: svc}
	req := httptest.NewRequest(http.MethodPost, "/v1/accounts/set-frozen", strings.NewReader(`{"command_id":"c1","actor":"ops","reason":"test","dry_run":true,"user_id":1001,"frozen":true,"freeze_until":"2030-01-02T00:00:00Z","freeze_appeal_url":"https://appeals.example.test"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"command_id":"c1"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if svc.req.UserID != 1001 || !svc.req.Frozen || svc.req.Until.IsZero() || svc.req.AppealURL != "https://appeals.example.test" {
		t.Fatalf("decoded freeze request = %+v", svc.req)
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

func TestAdminAPIImportStarGiftMultipart(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("metadata", `{"command_id":"gift-1","actor":"ops","reason":"catalog","dry_run":true,"title":"Gift","stars":50,"convert_stars":25,"enabled":true,"sort_order":3}`); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "gift.lottie")
	if err != nil {
		t.Fatal(err)
	}
	animation := []byte(`{"v":"5.7","w":512,"h":512,"fr":30,"ip":0,"op":30,"layers":[{}]}`)
	if _, err := part.Write(animation); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	svc := &captureGiftService{}
	srv := &Server{token: "secret", svc: svc}
	req := httptest.NewRequest(http.MethodPost, "/v1/gifts/import", &body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.req.CommandID != "gift-1" || svc.req.FileName != "gift.lottie" || !bytes.Equal(svc.req.Data, animation) || svc.req.Stars != 50 || svc.req.ConvertStars != 25 {
		t.Fatalf("decoded gift request = %+v", svc.req)
	}
}

func TestAdminAPIPublishStarGiftCollectiblesMultipart(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata := `{"command_id":"pool-1","actor":"ops","reason":"pool","dry_run":true,"upgrade_stars":125,"supply_total":100,"slug_prefix":"cake","models":[{"name":"Ruby","rarity_permille":1000,"sort_order":0,"file_key":"model-0"}],"patterns":[{"name":"Stars","rarity_permille":1000,"sort_order":0,"file_key":"pattern-0"}],"backdrops":[{"name":"Night","backdrop_id":1,"center_color":1122867,"edge_color":2241348,"pattern_color":3359829,"text_color":16777215,"rarity_permille":1000,"sort_order":0}]}`
	if err := writer.WriteField("metadata", metadata); err != nil {
		t.Fatal(err)
	}
	for key, name := range map[string]string{"model-0": "ruby.lottie", "pattern-0": "stars.tgs"} {
		part, err := writer.CreateFormFile(key, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(key)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	svc := &captureCollectibleService{}
	srv := &Server{token: "secret", svc: svc}
	req := httptest.NewRequest(http.MethodPost, "/v1/gifts/11/collectibles/publish", &body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if svc.req.GiftID != 11 || len(svc.req.Models) != 1 || svc.req.Models[0].FileName != "ruby.lottie" ||
		string(svc.req.Patterns[0].Data) != "pattern-0" || len(svc.req.Backdrops) != 1 {
		t.Fatalf("decoded collectible request = %+v", svc.req)
	}
}

type fakeService struct{}

type captureFreezeService struct {
	fakeService
	req admin.SetAccountFrozenRequest
}

type captureGiftService struct {
	fakeService
	req admin.ImportStarGiftRequest
}

type captureCollectibleService struct {
	fakeService
	req admin.PublishStarGiftCollectiblesRequest
}

func (s *captureFreezeService) SetAccountFrozen(_ context.Context, req admin.SetAccountFrozenRequest) (admin.CommandResult, error) {
	s.req = req
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (s *captureGiftService) ImportStarGift(_ context.Context, req admin.ImportStarGiftRequest) (admin.CommandResult, error) {
	s.req = req
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (s *captureCollectibleService) PublishStarGiftCollectibles(_ context.Context, req admin.PublishStarGiftCollectiblesRequest) (admin.CommandResult, error) {
	s.req = req
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) SetAccountFrozen(_ context.Context, req admin.SetAccountFrozenRequest) (admin.CommandResult, error) {
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

func (fakeService) ImportStarGift(_ context.Context, req admin.ImportStarGiftRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) PublishStarGiftCollectibles(_ context.Context, req admin.PublishStarGiftCollectiblesRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) SetStarGiftEnabled(_ context.Context, req admin.SetStarGiftEnabledRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) SetStarGiftSortOrder(_ context.Context, req admin.SetStarGiftSortOrderRequest) (admin.CommandResult, error) {
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (fakeService) StarGiftAnimation(context.Context, int64) ([]byte, bool, error) {
	return []byte(`{"v":"5.7","w":512,"h":512}`), true, nil
}

func (fakeService) StarGiftCollectibles(context.Context, int64) (domain.StarGiftUpgradePreview, bool, error) {
	return domain.StarGiftUpgradePreview{}, false, nil
}

func (fakeService) StarGiftCollectibleAnimation(context.Context, int64, domain.StarGiftCollectibleAttributeKind, int64) ([]byte, bool, error) {
	return []byte(`{"v":"5.7","w":512,"h":512}`), true, nil
}
