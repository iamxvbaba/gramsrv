package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/store"
)

func TestCodeStoreAtomicLoginInvalidation(t *testing.T) {
	ctx := context.Background()
	const phone = "15550016011"
	newRecord := func() store.PhoneCode {
		return store.PhoneCode{
			Version:     store.PhoneCodeVersionCurrent,
			Phone:       phone,
			Code:        "12345",
			Channel:     store.PhoneCodeChannelPhone,
			MaxAttempts: 5,
		}
	}

	t.Run("owner cleanup may delete a terminal sign-up marker", func(t *testing.T) {
		codes := NewCodeStore()
		if err := codes.Set(ctx, "invalidate-marker", newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		verified, err := codes.VerifyLogin(ctx, "invalidate-marker", phone, "12345", true, 5)
		if err != nil || verified.Status != store.LoginCodeVerifyAccepted || !verified.Record.SignUpVerified {
			t.Fatalf("mark sign-up = %+v err=%v", verified, err)
		}
		if removed, err := codes.InvalidateLoginCode(ctx, "invalidate-marker", "15550016999"); err != nil || removed {
			t.Fatalf("cross-phone invalidate removed=%v err=%v", removed, err)
		}
		if _, found, err := codes.ConsumeSignUpVerified(ctx, "invalidate-marker", "15550016999"); err != nil || found {
			t.Fatalf("cross-phone consume found=%v err=%v", found, err)
		}
		if removed, err := codes.InvalidateLoginCode(ctx, "invalidate-marker", phone); err != nil || !removed {
			t.Fatalf("owner invalidate removed=%v err=%v", removed, err)
		}
		if _, found, err := codes.ConsumeSignUpVerified(ctx, "invalidate-marker", phone); err != nil || found {
			t.Fatalf("consume after invalidate found=%v err=%v", found, err)
		}
	})

	t.Run("legacy records fail closed", func(t *testing.T) {
		codes := NewCodeStore()
		legacy := newRecord()
		legacy.Version = 0
		if err := codes.Set(ctx, "invalidate-legacy", legacy, time.Minute); err != nil {
			t.Fatal(err)
		}
		if removed, err := codes.InvalidateLoginCode(ctx, "invalidate-legacy", phone); err != nil || removed {
			t.Fatalf("legacy invalidate removed=%v err=%v, want false", removed, err)
		}
		if _, found, err := codes.Get(ctx, "invalidate-legacy"); err != nil || found {
			t.Fatalf("legacy record found=%v err=%v after fail-closed invalidate", found, err)
		}
	})

	t.Run("invalidate and sign-up consume have one winner", func(t *testing.T) {
		codes := NewCodeStore()
		if err := codes.Set(ctx, "invalidate-race", newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		if verified, err := codes.VerifyLogin(ctx, "invalidate-race", phone, "12345", true, 5); err != nil || verified.Status != store.LoginCodeVerifyAccepted {
			t.Fatalf("mark sign-up = %+v err=%v", verified, err)
		}

		const workers = 64
		results := make(chan bool, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(invalidate bool) {
				defer wg.Done()
				if invalidate {
					removed, err := codes.InvalidateLoginCode(ctx, "invalidate-race", phone)
					if err != nil {
						t.Errorf("InvalidateLoginCode: %v", err)
					}
					results <- removed
					return
				}
				_, found, err := codes.ConsumeSignUpVerified(ctx, "invalidate-race", phone)
				if err != nil {
					t.Errorf("ConsumeSignUpVerified: %v", err)
				}
				results <- found
			}(i%2 == 0)
		}
		wg.Wait()
		close(results)
		winners := 0
		for won := range results {
			if won {
				winners++
			}
		}
		if winners != 1 {
			t.Fatalf("invalidate/consume winners=%d, want 1", winners)
		}
	})
}
