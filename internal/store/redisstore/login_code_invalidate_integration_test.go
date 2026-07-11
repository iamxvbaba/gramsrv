package redisstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/store"
)

func TestRedisCodeStoreAtomicLoginInvalidation(t *testing.T) {
	codes, _, hash := newRedisLoginCodeHarness(t)
	ctx := context.Background()
	const phone = "15550016111"
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
		key := hash("invalidate-marker")
		if err := codes.Set(ctx, key, newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		verified, err := codes.VerifyLogin(ctx, key, phone, "12345", true, 5)
		if err != nil || verified.Status != store.LoginCodeVerifyAccepted || !verified.Record.SignUpVerified {
			t.Fatalf("mark sign-up=%+v err=%v", verified, err)
		}
		if removed, err := codes.InvalidateLoginCode(ctx, key, "15550016999"); err != nil || removed {
			t.Fatalf("cross-phone invalidate removed=%v err=%v", removed, err)
		}
		if removed, err := codes.InvalidateLoginCode(ctx, key, phone); err != nil || !removed {
			t.Fatalf("owner invalidate removed=%v err=%v", removed, err)
		}
		if _, found, err := codes.ConsumeSignUpVerified(ctx, key, phone); err != nil || found {
			t.Fatalf("consume after invalidate found=%v err=%v", found, err)
		}
	})

	t.Run("legacy records fail closed", func(t *testing.T) {
		key := hash("invalidate-legacy")
		legacy := newRecord()
		legacy.Version = 0
		if err := codes.Set(ctx, key, legacy, time.Minute); err != nil {
			t.Fatal(err)
		}
		if removed, err := codes.InvalidateLoginCode(ctx, key, phone); err != nil || removed {
			t.Fatalf("legacy invalidate removed=%v err=%v, want false", removed, err)
		}
		assertRedisCodeMissing(t, ctx, codes, key)
	})

	t.Run("invalidate and sign-up consume have one winner", func(t *testing.T) {
		key := hash("invalidate-race")
		if err := codes.Set(ctx, key, newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		if verified, err := codes.VerifyLogin(ctx, key, phone, "12345", true, 5); err != nil || verified.Status != store.LoginCodeVerifyAccepted {
			t.Fatalf("mark sign-up=%+v err=%v", verified, err)
		}

		const workers = 48
		results := make(chan bool, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(invalidate bool) {
				defer wg.Done()
				if invalidate {
					removed, err := codes.InvalidateLoginCode(ctx, key, phone)
					if err != nil {
						errs <- err
						return
					}
					results <- removed
					return
				}
				_, found, err := codes.ConsumeSignUpVerified(ctx, key, phone)
				if err != nil {
					errs <- err
					return
				}
				results <- found
			}(i%2 == 0)
		}
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			t.Fatalf("invalidate/consume race: %v", err)
		}
		if winners := countRedisTrue(results); winners != 1 {
			t.Fatalf("invalidate/consume winners=%d, want 1", winners)
		}
	})
}
