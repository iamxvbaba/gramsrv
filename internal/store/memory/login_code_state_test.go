package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/store"
)

func TestCodeStoreAtomicLoginStateMachine(t *testing.T) {
	ctx := context.Background()
	const phone = "15550016001"
	newRecord := func() store.PhoneCode {
		return store.PhoneCode{
			Version:      store.PhoneCodeVersionCurrent,
			IssuedUserID: 1000000001,
			Phone:        phone,
			Code:         "12345",
			Channel:      "phone",
			MaxAttempts:  2,
		}
	}

	t.Run("version mismatch fails closed for every atomic entry", func(t *testing.T) {
		codes := NewCodeStore()
		legacy := newRecord()
		legacy.Version = 0
		if err := codes.Set(ctx, "legacy-verify", legacy, time.Minute); err != nil {
			t.Fatal(err)
		}
		result, err := codes.VerifyLogin(ctx, "legacy-verify", phone, legacy.Code, false, 5)
		if err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("legacy VerifyLogin = %+v err=%v, want Missing", result, err)
		}
		if _, found, _ := codes.Get(ctx, "legacy-verify"); found {
			t.Fatal("legacy VerifyLogin record was not deleted")
		}

		unknown := newRecord()
		unknown.Version = store.PhoneCodeVersionCurrent + 1
		if err := codes.Set(ctx, "unknown-take", unknown, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, found, err := codes.TakeLoginCode(ctx, "unknown-take", phone); err != nil || found {
			t.Fatalf("unknown TakeLoginCode found=%v err=%v, want false", found, err)
		}
		if _, found, _ := codes.Get(ctx, "unknown-take"); found {
			t.Fatal("unknown TakeLoginCode record was not deleted")
		}

		legacy.SignUpVerified = true
		if err := codes.Set(ctx, "legacy-signup", legacy, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, found, err := codes.ConsumeSignUpVerified(ctx, "legacy-signup", phone); err != nil || found {
			t.Fatalf("legacy ConsumeSignUpVerified found=%v err=%v, want false", found, err)
		}
		if _, found, _ := codes.Get(ctx, "legacy-signup"); found {
			t.Fatal("legacy sign-up marker was not deleted")
		}
	})

	t.Run("scope mismatch does not burn victim attempts", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord()
		if err := codes.Set(ctx, "scope", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		result, err := codes.VerifyLogin(ctx, "scope", "15550016999", record.Code, false, 5)
		if err != nil || result.Status != store.LoginCodeVerifyInvalid || result.Record.Attempts != 0 {
			t.Fatalf("wrong-phone VerifyLogin = %+v err=%v", result, err)
		}
		stored, found, err := codes.Get(ctx, "scope")
		if err != nil || !found || stored.Attempts != 0 {
			t.Fatalf("wrong-phone stored=%+v found=%v err=%v", stored, found, err)
		}
		if _, found, err := codes.TakeLoginCode(ctx, "scope", "15550016999"); err != nil || found {
			t.Fatalf("cross-phone TakeLoginCode found=%v err=%v", found, err)
		}

		scoped := newRecord()
		scoped.Purpose = store.PhoneCodePurposeChangePhone
		scoped.UserID = 42
		scoped.AuthKeyID = [8]byte{1}
		if err := codes.Set(ctx, "scoped", scoped, time.Minute); err != nil {
			t.Fatal(err)
		}
		result, err = codes.VerifyLogin(ctx, "scoped", phone, scoped.Code, false, 5)
		if err != nil || result.Status != store.LoginCodeVerifyInvalid {
			t.Fatalf("scoped VerifyLogin = %+v err=%v, want Invalid", result, err)
		}
		if _, found, err := codes.TakeLoginCode(ctx, "scoped", phone); err != nil || found {
			t.Fatalf("scoped TakeLoginCode found=%v err=%v", found, err)
		}
		if _, found, _ := codes.Get(ctx, "scoped"); !found {
			t.Fatal("login operations deleted a scoped change-phone code")
		}
	})

	t.Run("wrong code increments atomically and threshold deletes", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord()
		if err := codes.Set(ctx, "wrong", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		first, err := codes.VerifyLogin(ctx, "wrong", phone, "00000", false, 9)
		if err != nil || first.Status != store.LoginCodeVerifyInvalid || first.Record.Attempts != 1 {
			t.Fatalf("first wrong code = %+v err=%v", first, err)
		}
		stored, found, err := codes.Get(ctx, "wrong")
		if err != nil || !found || stored.Attempts != 1 {
			t.Fatalf("stored after first wrong = %+v found=%v err=%v", stored, found, err)
		}
		second, err := codes.VerifyLogin(ctx, "wrong", phone, "00000", false, 9)
		if err != nil || second.Status != store.LoginCodeVerifyInvalid || second.Record.Attempts != 2 {
			t.Fatalf("threshold wrong code = %+v err=%v", second, err)
		}
		if _, found, _ := codes.Get(ctx, "wrong"); found {
			t.Fatal("threshold-exhausted code remains")
		}
		after, err := codes.VerifyLogin(ctx, "wrong", phone, record.Code, false, 9)
		if err != nil || after.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("verify after exhaustion = %+v err=%v, want Missing", after, err)
		}

		fallback := newRecord()
		fallback.MaxAttempts = 0
		if err := codes.Set(ctx, "fallback", fallback, time.Minute); err != nil {
			t.Fatal(err)
		}
		if got, err := codes.VerifyLogin(ctx, "fallback", phone, "bad", false, 1); err != nil || got.Status != store.LoginCodeVerifyInvalid {
			t.Fatalf("default threshold verify = %+v err=%v", got, err)
		}
		if _, found, _ := codes.Get(ctx, "fallback"); found {
			t.Fatal("default threshold did not delete code")
		}
	})

	t.Run("accepted consume and sign-up marker are terminal", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord()
		if err := codes.Set(ctx, "consume", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		accepted, err := codes.VerifyLogin(ctx, "consume", phone, record.Code, false, 5)
		if err != nil || accepted.Status != store.LoginCodeVerifyAccepted || accepted.Record.SignUpVerified {
			t.Fatalf("consume verify = %+v err=%v", accepted, err)
		}
		if _, found, _ := codes.Get(ctx, "consume"); found {
			t.Fatal("accepted existing-user code remains")
		}

		if err := codes.Set(ctx, "issued-existing", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		wrongScope, err := codes.VerifyLogin(ctx, "issued-existing", phone, record.Code, true, 5)
		if err != nil || wrongScope.Status != store.LoginCodeVerifyInvalid {
			t.Fatalf("existing-issued keep-for-signup = %+v err=%v, want Invalid", wrongScope, err)
		}
		if _, found, err := codes.ConsumeSignUpVerified(ctx, "issued-existing", phone); err != nil || found {
			t.Fatalf("existing-issued sign-up consume found=%v err=%v", found, err)
		}

		signUpRecord := record
		signUpRecord.IssuedUserID = 0
		if err := codes.Set(ctx, "signup", signUpRecord, time.Minute); err != nil {
			t.Fatal(err)
		}
		expires := codes.m["signup"].expires
		marked, err := codes.VerifyLogin(ctx, "signup", phone, record.Code, true, 5)
		if err != nil || marked.Status != store.LoginCodeVerifyAccepted || !marked.Record.SignUpVerified {
			t.Fatalf("sign-up verify = %+v err=%v", marked, err)
		}
		if got := codes.m["signup"]; !got.code.SignUpVerified || !got.expires.Equal(expires) {
			t.Fatalf("sign-up marker=%+v expiry=%v, want marker with unchanged %v", got.code, got.expires, expires)
		}
		repeated, err := codes.VerifyLogin(ctx, "signup", phone, record.Code, true, 5)
		if err != nil || repeated.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("repeated sign-up verify = %+v err=%v, want terminal Missing", repeated, err)
		}
		if _, found, err := codes.ConsumeSignUpVerified(ctx, "signup", "15550016999"); err != nil || found {
			t.Fatalf("cross-phone sign-up consume found=%v err=%v", found, err)
		}
		if _, found, err := codes.TakeLoginCode(ctx, "signup", phone); err != nil || found {
			t.Fatalf("terminal marker take found=%v err=%v, want false", found, err)
		}
		consumed, found, err := codes.ConsumeSignUpVerified(ctx, "signup", phone)
		if err != nil || !found || !consumed.SignUpVerified || consumed.Code != signUpRecord.Code || consumed.IssuedUserID != 0 {
			t.Fatalf("sign-up consume = %+v found=%v err=%v", consumed, found, err)
		}
		if _, found, err := codes.ConsumeSignUpVerified(ctx, "signup", phone); err != nil || found {
			t.Fatalf("second sign-up consume found=%v err=%v", found, err)
		}
	})

	t.Run("take returns the removed record exactly once", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord()
		if err := codes.Set(ctx, "take", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		expected, found, err := codes.Get(ctx, "take")
		if err != nil || !found {
			t.Fatalf("load take record found=%v err=%v", found, err)
		}
		if _, found, err := codes.TakeLoginCode(ctx, "take", "15550016999"); err != nil || found {
			t.Fatalf("cross-phone take found=%v err=%v", found, err)
		}
		taken, found, err := codes.TakeLoginCode(ctx, "take", phone)
		if err != nil || !found || taken != expected {
			t.Fatalf("take = %+v found=%v err=%v, want %+v", taken, found, err, expected)
		}
		if _, found, err := codes.TakeLoginCode(ctx, "take", phone); err != nil || found {
			t.Fatalf("second take found=%v err=%v", found, err)
		}
	})
}

func TestCodeStoreAtomicLoginConcurrency(t *testing.T) {
	ctx := context.Background()
	const (
		phone   = "15550016002"
		workers = 64
	)
	newRecord := func() store.PhoneCode {
		return store.PhoneCode{
			Version:     store.PhoneCodeVersionCurrent,
			Phone:       phone,
			Code:        "12345",
			Channel:     "phone",
			MaxAttempts: 7,
		}
	}

	t.Run("consume verify has one accepted", func(t *testing.T) {
		codes := NewCodeStore()
		if err := codes.Set(ctx, "verify-race", newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentMemoryVerify(t, codes, "verify-race", phone, "12345", false, workers)
		if statuses[store.LoginCodeVerifyAccepted] != 1 || statuses[store.LoginCodeVerifyMissing] != workers-1 || statuses[store.LoginCodeVerifyInvalid] != 0 {
			t.Fatalf("verify race statuses = %+v", statuses)
		}
	})

	t.Run("mark and consume each have one winner", func(t *testing.T) {
		codes := NewCodeStore()
		if err := codes.Set(ctx, "signup-race", newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentMemoryVerify(t, codes, "signup-race", phone, "12345", true, workers)
		if statuses[store.LoginCodeVerifyAccepted] != 1 || statuses[store.LoginCodeVerifyMissing] != workers-1 {
			t.Fatalf("sign-up verify race statuses = %+v", statuses)
		}
		found := concurrentMemoryConsumeSignUp(t, codes, "signup-race", phone, workers)
		if found != 1 {
			t.Fatalf("sign-up consumes = %d, want 1", found)
		}
	})

	t.Run("take has one winner", func(t *testing.T) {
		codes := NewCodeStore()
		if err := codes.Set(ctx, "take-race", newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		found := concurrentMemoryTake(t, codes, "take-race", phone, workers)
		if found != 1 {
			t.Fatalf("takes = %d, want 1", found)
		}
	})

	t.Run("wrong attempts cannot be lost", func(t *testing.T) {
		codes := NewCodeStore()
		if err := codes.Set(ctx, "wrong-race", newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentMemoryVerify(t, codes, "wrong-race", phone, "00000", false, workers)
		if statuses[store.LoginCodeVerifyInvalid] != 7 || statuses[store.LoginCodeVerifyMissing] != workers-7 {
			t.Fatalf("wrong-code race statuses = %+v, want 7 Invalid then Missing", statuses)
		}
		if _, found, _ := codes.Get(ctx, "wrong-race"); found {
			t.Fatal("wrong-code race left an exhausted code")
		}
	})

	t.Run("verify and cancel-resend take share one winner", func(t *testing.T) {
		codes := NewCodeStore()
		if err := codes.Set(ctx, "mixed-race", newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		results := make(chan bool, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(take bool) {
				defer wg.Done()
				if take {
					_, found, err := codes.TakeLoginCode(ctx, "mixed-race", phone)
					if err != nil {
						t.Errorf("TakeLoginCode: %v", err)
					}
					results <- found
					return
				}
				verified, err := codes.VerifyLogin(ctx, "mixed-race", phone, "12345", false, 5)
				if err != nil {
					t.Errorf("VerifyLogin: %v", err)
				}
				results <- verified.Status == store.LoginCodeVerifyAccepted
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
			t.Fatalf("mixed verify/take winners = %d, want 1", winners)
		}
	})

	t.Run("sign-up mark and take share one winner", func(t *testing.T) {
		codes := NewCodeStore()
		if err := codes.Set(ctx, "mixed-signup-race", newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		results := make(chan bool, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(take bool) {
				defer wg.Done()
				if take {
					_, found, err := codes.TakeLoginCode(ctx, "mixed-signup-race", phone)
					if err != nil {
						t.Errorf("TakeLoginCode: %v", err)
					}
					results <- found
					return
				}
				verified, err := codes.VerifyLogin(ctx, "mixed-signup-race", phone, "12345", true, 5)
				if err != nil {
					t.Errorf("VerifyLogin: %v", err)
				}
				results <- verified.Status == store.LoginCodeVerifyAccepted
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
			t.Fatalf("mixed sign-up/take winners = %d, want 1", winners)
		}
	})
}

func concurrentMemoryVerify(t *testing.T, codes *CodeStore, hash, phone, code string, keep bool, workers int) map[store.LoginCodeVerifyStatus]int {
	t.Helper()
	ctx := context.Background()
	results := make(chan store.LoginCodeVerifyStatus, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := codes.VerifyLogin(ctx, hash, phone, code, keep, 5)
			if err != nil {
				t.Errorf("VerifyLogin: %v", err)
				return
			}
			results <- result.Status
		}()
	}
	wg.Wait()
	close(results)
	counts := make(map[store.LoginCodeVerifyStatus]int)
	for status := range results {
		counts[status]++
	}
	return counts
}

func concurrentMemoryTake(t *testing.T, codes *CodeStore, hash, phone string, workers int) int {
	t.Helper()
	ctx := context.Background()
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, found, err := codes.TakeLoginCode(ctx, hash, phone)
			if err != nil {
				t.Errorf("TakeLoginCode: %v", err)
				return
			}
			results <- found
		}()
	}
	wg.Wait()
	close(results)
	foundCount := 0
	for found := range results {
		if found {
			foundCount++
		}
	}
	return foundCount
}

func concurrentMemoryConsumeSignUp(t *testing.T, codes *CodeStore, hash, phone string, workers int) int {
	t.Helper()
	ctx := context.Background()
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, found, err := codes.ConsumeSignUpVerified(ctx, hash, phone)
			if err != nil {
				t.Errorf("ConsumeSignUpVerified: %v", err)
				return
			}
			results <- found
		}()
	}
	wg.Wait()
	close(results)
	foundCount := 0
	for found := range results {
		if found {
			foundCount++
		}
	}
	return foundCount
}
