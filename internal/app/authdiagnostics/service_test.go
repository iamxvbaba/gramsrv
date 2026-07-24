package authdiagnostics

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestReportMissingCodeValidatesLiveDeliveryAndStoresOnlyHashes(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	reports := memory.NewAuthDeliveryReportStore()
	const (
		phone    = "15550001234"
		codeHash = "login-code-hash"
	)
	if err := codes.Set(ctx, codeHash, store.PhoneCode{
		Version: store.PhoneCodeVersionCurrent, Phone: phone, Code: "12345",
		DeliveryID: "delivery-1", Channel: store.PhoneCodeChannelSMS,
		IssuedUserID: 42,
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	service := NewService(codes, reports)
	now := time.Now().UTC()
	req := domain.AuthMissingCodeReportRequest{
		AuthKeyID: [8]byte{1, 2, 3}, SessionID: 99, ClientType: "tdesktop",
		Phone: "+1 (555) 000-1234", PhoneCodeHash: codeHash, MNC: "46000",
		CreatedAt: now,
	}
	first, created, err := service.ReportMissingCode(ctx, req)
	if err != nil || !created {
		t.Fatalf("first report created=%v err=%v", created, err)
	}
	second, created, err := service.ReportMissingCode(ctx, req)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("retry report=%+v created=%v err=%v", second, created, err)
	}
	stored := reports.Reports()
	if len(stored) != 1 {
		t.Fatalf("stored reports=%d, want 1", len(stored))
	}
	if stored[0].PhoneHash != sha256.Sum256([]byte(phone)) ||
		stored[0].CodeHash != sha256.Sum256([]byte(codeHash)) {
		t.Fatalf("stored hashes do not match normalized delivery identity: %+v", stored[0])
	}
	if stored[0].DeliveryID != "delivery-1" || stored[0].IssuedUserID != 42 ||
		stored[0].Channel != domain.AuthCodeDeliverySMS {
		t.Fatalf("stored delivery metadata=%+v", stored[0])
	}
}

func TestReportMissingCodeRejectsUnknownOrMismatchedLoginState(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	service := NewService(codes, memory.NewAuthDeliveryReportStore())
	now := time.Now().UTC()
	base := domain.AuthMissingCodeReportRequest{
		AuthKeyID: [8]byte{1}, SessionID: 10, Phone: "15550002222",
		PhoneCodeHash: "missing", CreatedAt: now,
	}
	if _, _, err := service.ReportMissingCode(ctx, base); !errors.Is(err, domain.ErrPhoneCodeExpired) {
		t.Fatalf("missing hash err=%v, want phone-code expired", err)
	}
	if err := codes.Set(ctx, "current", store.PhoneCode{
		Version: store.PhoneCodeVersionCurrent, Phone: "15550003333",
		Channel: store.PhoneCodeChannelPhone,
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	base.PhoneCodeHash = "current"
	if _, _, err := service.ReportMissingCode(ctx, base); !errors.Is(err, domain.ErrPhoneCodeInvalid) {
		t.Fatalf("mismatched phone err=%v, want phone-code invalid", err)
	}
}
