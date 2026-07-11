package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/store"
)

func TestCodeStoreAtomicScopedVerification(t *testing.T) {
	ctx := context.Background()
	newRecord := func() store.PhoneCode {
		return store.PhoneCode{
			Version:     store.PhoneCodeVersionCurrent,
			Phone:       "15550016021",
			Code:        "12345",
			Channel:     store.PhoneCodeChannelPhone,
			Purpose:     store.PhoneCodePurposeChangePhone,
			UserID:      420021,
			AuthKeyID:   [8]byte{1, 2, 3, 4},
			MaxAttempts: 2,
		}
	}

	t.Run("only the active hash and exact scope can mutate", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord()
		if err := codes.Set(ctx, "scoped-old", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := codes.Set(ctx, "scoped-current", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		if result, err := codes.VerifyScoped(ctx, "scoped-old", record.Scope(), record.Code, 5); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("old-hash verify=%+v err=%v", result, err)
		}

		otherScope := record.Scope()
		otherScope.AuthKeyID = [8]byte{9}
		if result, err := codes.VerifyScoped(ctx, "scoped-current", otherScope, "00000", 5); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("cross-scope verify=%+v err=%v", result, err)
		}
		stored, found, err := codes.Get(ctx, "scoped-current")
		if err != nil || !found || stored.Attempts != 0 {
			t.Fatalf("victim after cross-scope verify=%+v found=%v err=%v", stored, found, err)
		}
	})

	t.Run("wrong attempts preserve ttl then delete code and index", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord()
		if err := codes.Set(ctx, "scoped-wrong", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		before := codes.m["scoped-wrong"]
		first, err := codes.VerifyScoped(ctx, "scoped-wrong", record.Scope(), "00000", 9)
		if err != nil || first.Status != store.LoginCodeVerifyInvalid || first.Record.Attempts != 1 {
			t.Fatalf("first wrong=%+v err=%v", first, err)
		}
		after := codes.m["scoped-wrong"]
		if !after.expires.Equal(before.expires) || after.code.Revision == before.code.Revision {
			t.Fatalf("wrong attempt expiry/revision before=%+v after=%+v", before, after)
		}
		second, err := codes.VerifyScoped(ctx, "scoped-wrong", record.Scope(), "00000", 9)
		if err != nil || second.Status != store.LoginCodeVerifyInvalid || second.Record.Attempts != 2 {
			t.Fatalf("threshold wrong=%+v err=%v", second, err)
		}
		if _, found, _ := codes.Get(ctx, "scoped-wrong"); found {
			t.Fatal("threshold-exhausted scoped code remains")
		}
		if got := codes.scopes[record.Scope()]; got != "" {
			t.Fatalf("threshold-exhausted scope index=%q, want missing", got)
		}
		if result, err := codes.VerifyScoped(ctx, "scoped-wrong", record.Scope(), record.Code, 9); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("verify after exhaustion=%+v err=%v", result, err)
		}
	})

	t.Run("correct code consumes both keys exactly once", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord()
		if err := codes.Set(ctx, "scoped-correct", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		expected, found, err := codes.Get(ctx, "scoped-correct")
		if err != nil || !found {
			t.Fatalf("get expected found=%v err=%v", found, err)
		}
		accepted, err := codes.VerifyScoped(ctx, "scoped-correct", record.Scope(), record.Code, 5)
		if err != nil || accepted.Status != store.LoginCodeVerifyAccepted || accepted.Record != expected {
			t.Fatalf("accepted=%+v err=%v, want %+v", accepted, err, expected)
		}
		if _, found, _ := codes.Get(ctx, "scoped-correct"); found || codes.scopes[record.Scope()] != "" {
			t.Fatal("accepted scoped code or index remains")
		}
		if repeated, err := codes.VerifyScoped(ctx, "scoped-correct", record.Scope(), record.Code, 5); err != nil || repeated.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("repeated verify=%+v err=%v", repeated, err)
		}
	})

	t.Run("legacy and inconsistent records fail closed", func(t *testing.T) {
		codes := NewCodeStore()
		legacy := newRecord()
		legacy.Version = 0
		if err := codes.Set(ctx, "scoped-legacy", legacy, time.Minute); err != nil {
			t.Fatal(err)
		}
		if result, err := codes.VerifyScoped(ctx, "scoped-legacy", legacy.Scope(), legacy.Code, 5); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("legacy verify=%+v err=%v", result, err)
		}
		if _, found, _ := codes.Get(ctx, "scoped-legacy"); found || codes.scopes[legacy.Scope()] != "" {
			t.Fatal("legacy code or index remains")
		}

		inconsistent := newRecord()
		if err := codes.Set(ctx, "scoped-inconsistent", inconsistent, time.Minute); err != nil {
			t.Fatal(err)
		}
		entry := codes.m["scoped-inconsistent"]
		entry.code.Phone = "15550016999"
		codes.m["scoped-inconsistent"] = entry
		if result, err := codes.VerifyScoped(ctx, "scoped-inconsistent", inconsistent.Scope(), inconsistent.Code, 5); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("inconsistent verify=%+v err=%v", result, err)
		}
		if _, found, _ := codes.Get(ctx, "scoped-inconsistent"); found || codes.scopes[inconsistent.Scope()] != "" {
			t.Fatal("inconsistent code or selecting index remains")
		}
	})
}

func TestCodeStoreAtomicScopedConcurrency(t *testing.T) {
	ctx := context.Background()
	const workers = 64
	newRecord := func(maxAttempts int) store.PhoneCode {
		return store.PhoneCode{
			Version:     store.PhoneCodeVersionCurrent,
			Phone:       "15550016022",
			Code:        "12345",
			Channel:     store.PhoneCodeChannelPhone,
			Purpose:     store.PhoneCodePurposeChangePhone,
			UserID:      420022,
			AuthKeyID:   [8]byte{5, 6, 7, 8},
			MaxAttempts: maxAttempts,
		}
	}

	t.Run("correct verification has one winner", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord(7)
		if err := codes.Set(ctx, "scoped-verify-race", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentMemoryScopedVerify(t, codes, "scoped-verify-race", record.Scope(), record.Code, workers)
		if statuses[store.LoginCodeVerifyAccepted] != 1 || statuses[store.LoginCodeVerifyMissing] != workers-1 || statuses[store.LoginCodeVerifyInvalid] != 0 {
			t.Fatalf("correct race statuses=%+v", statuses)
		}
	})

	t.Run("wrong attempts cannot be lost", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord(7)
		if err := codes.Set(ctx, "scoped-wrong-race", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentMemoryScopedVerify(t, codes, "scoped-wrong-race", record.Scope(), "00000", workers)
		if statuses[store.LoginCodeVerifyInvalid] != 7 || statuses[store.LoginCodeVerifyMissing] != workers-7 {
			t.Fatalf("wrong race statuses=%+v", statuses)
		}
		if _, found, _ := codes.Get(ctx, "scoped-wrong-race"); found || codes.scopes[record.Scope()] != "" {
			t.Fatal("wrong race left code or scope index")
		}
	})

	t.Run("verification and cancellation share one winner", func(t *testing.T) {
		codes := NewCodeStore()
		record := newRecord(7)
		if err := codes.Set(ctx, "scoped-mixed-race", record, time.Minute); err != nil {
			t.Fatal(err)
		}
		results := make(chan bool, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(cancel bool) {
				defer wg.Done()
				if cancel {
					_, found, err := codes.ConsumeScoped(ctx, "scoped-mixed-race", record.Scope())
					if err != nil {
						t.Errorf("ConsumeScoped: %v", err)
					}
					results <- found
					return
				}
				result, err := codes.VerifyScoped(ctx, "scoped-mixed-race", record.Scope(), record.Code, 5)
				if err != nil {
					t.Errorf("VerifyScoped: %v", err)
				}
				results <- result.Status == store.LoginCodeVerifyAccepted
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
			t.Fatalf("verify/cancel winners=%d, want 1", winners)
		}
	})
}

func concurrentMemoryScopedVerify(t *testing.T, codes *CodeStore, hash string, scope store.PhoneCodeScope, code string, workers int) map[store.LoginCodeVerifyStatus]int {
	t.Helper()
	results := make(chan store.LoginCodeVerifyStatus, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := codes.VerifyScoped(context.Background(), hash, scope, code, 5)
			if err != nil {
				t.Errorf("VerifyScoped: %v", err)
				return
			}
			results <- result.Status
		}()
	}
	wg.Wait()
	close(results)
	statuses := make(map[store.LoginCodeVerifyStatus]int)
	for status := range results {
		statuses[status]++
	}
	return statuses
}
