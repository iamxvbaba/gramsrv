package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// The reserved service identity is not a login identity. Auth must recognize
// it before any code is issued, and the refusal must not enumerate the
// difference between a reserved number and a free one.
func TestReservedSystemPhoneCannotLogIn(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")

	if _, err := svc.SendCode(ctx, "+"+domain.OfficialSystemPhone); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("SendCode(%q) error = %v, want ErrSystemUserLoginForbidden", domain.OfficialSystemPhone, err)
	}
	if _, err := svc.SendCode(ctx, domain.OfficialSystemPhone); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("SendCode(%q) error = %v, want ErrSystemUserLoginForbidden", domain.OfficialSystemPhone, err)
	}
	if _, _, err := svc.SignUp(ctx, domain.Authorization{}, domain.OfficialSystemPhone, "hash", "Sys", "Tem"); !errors.Is(err, ErrSystemUserLoginForbidden) {
		t.Fatalf("SignUp(%q) error = %v, want ErrSystemUserLoginForbidden", domain.OfficialSystemPhone, err)
	}
}

// The lenient phone format policy keeps short and unassigned international
// numbers usable: they are ordinary login identities from the client's point
// of view.
func TestLenientPhoneFormatAcceptsShortAndUnassignedNumbers(t *testing.T) {
	ctx := context.Background()
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")

	for _, phone := range []string{"+7 123", "+1488"} {
		hash, err := svc.SendCode(ctx, phone)
		if err != nil {
			t.Fatalf("SendCode(%q): %v", phone, err)
		}
		verifyCodeForSignUp(t, svc, phone, hash, "12345")
		u, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, hash, "Short", "Number")
		if err != nil {
			t.Fatalf("SignUp(%q): %v", phone, err)
		}
		if u.Phone != domain.NormalizePhone(phone) {
			t.Fatalf("user phone = %q, want %q", u.Phone, domain.NormalizePhone(phone))
		}
	}
}
