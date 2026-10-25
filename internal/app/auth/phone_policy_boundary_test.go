package auth

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestPhonePolicyReservedAndStrictLoginBoundaries(t *testing.T) {
	original := domain.PhoneFormatCheckEnabled()
	t.Cleanup(func() { domain.ConfigurePhoneFormatCheck(original) })
	ctx := context.Background()
	for _, strict := range []bool{false, true} {
		domain.ConfigurePhoneFormatCheck(strict)
		svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")
		for _, phone := range []string{domain.OfficialSystemPhone, "+42777"} {
			if _, err := svc.SendCode(ctx, phone); !errors.Is(err, ErrSystemUserLoginForbidden) {
				t.Fatalf("strict=%v SendCode reserved: %v", strict, err)
			}
			if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, phone, "not-issued", "12345", testAuthorizationDelivery); !errors.Is(err, ErrSystemUserLoginForbidden) {
				t.Fatalf("strict=%v SignIn reserved: %v", strict, err)
			}
			if _, _, _, err := svc.SignInWithEmail(ctx, domain.Authorization{}, phone, "not-issued", "12345", testAuthorizationDelivery); !errors.Is(err, ErrSystemUserLoginForbidden) {
				t.Fatalf("strict=%v SignInWithEmail reserved: %v", strict, err)
			}
			if _, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, "not-issued", "System", ""); !errors.Is(err, ErrSystemUserLoginForbidden) {
				t.Fatalf("strict=%v SignUp reserved: %v", strict, err)
			}
		}
		if strict {
			for _, phone := range []string{"+7 123", "+1488", "+999123456789", "09981679461"} {
				if _, err := svc.SendCode(ctx, phone); !errors.Is(err, ErrPhoneNumberInvalid) {
					t.Fatalf("strict SendCode(%q): %v", phone, err)
				}
			}
		}
	}
}
