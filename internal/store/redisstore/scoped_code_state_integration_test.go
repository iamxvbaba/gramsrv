package redisstore

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/store"
)

func TestRedisCodeStoreAtomicScopedVerification(t *testing.T) {
	codes, client, hash := newRedisLoginCodeHarness(t)
	ctx := context.Background()
	newRecord := func() store.PhoneCode {
		return store.PhoneCode{
			Version:     store.PhoneCodeVersionCurrent,
			Phone:       "15550016121",
			Code:        "12345",
			Channel:     store.PhoneCodeChannelPhone,
			Purpose:     store.PhoneCodePurposeChangePhone,
			UserID:      math.MaxInt64 - 121,
			AuthKeyID:   [8]byte{1, 2, 3, 4},
			SessionID:   math.MaxInt64 - 21,
			MaxAttempts: 2,
		}
	}
	recordForCleanup := newRecord()
	t.Cleanup(func() { _ = client.Del(context.Background(), codeScopeKey(recordForCleanup.Scope())).Err() })

	t.Run("only the active hash and exact scope can mutate", func(t *testing.T) {
		record := newRecord()
		oldHash := hash("scoped-old")
		currentHash := hash("scoped-current")
		if err := codes.Set(ctx, oldHash, record, time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := codes.Set(ctx, currentHash, record, time.Minute); err != nil {
			t.Fatal(err)
		}
		if result, err := codes.VerifyScoped(ctx, oldHash, record.Scope(), record.Code, 5); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("old-hash verify=%+v err=%v", result, err)
		}

		otherScope := record.Scope()
		otherScope.AuthKeyID = [8]byte{9}
		if result, err := codes.VerifyScoped(ctx, currentHash, otherScope, "00000", 5); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("cross-scope verify=%+v err=%v", result, err)
		}
		stored, found, err := codes.Get(ctx, currentHash)
		if err != nil || !found || stored.Attempts != 0 || stored.UserID != record.UserID {
			t.Fatalf("victim after cross-scope verify=%+v found=%v err=%v", stored, found, err)
		}
	})

	t.Run("wrong attempts preserve ttl then delete code and index", func(t *testing.T) {
		record := newRecord()
		key := hash("scoped-wrong")
		if err := codes.Set(ctx, key, record, 45*time.Second); err != nil {
			t.Fatal(err)
		}
		beforeTTL, err := client.PTTL(ctx, codeKey(key)).Result()
		if err != nil {
			t.Fatal(err)
		}
		before, found, err := codes.Get(ctx, key)
		if err != nil || !found {
			t.Fatalf("get before found=%v err=%v", found, err)
		}
		first, err := codes.VerifyScoped(ctx, key, record.Scope(), "00000", 9)
		if err != nil || first.Status != store.LoginCodeVerifyInvalid || first.Record.Attempts != 1 || first.Record.UserID != record.UserID || first.Record.SessionID != record.SessionID {
			t.Fatalf("first wrong=%+v err=%v", first, err)
		}
		afterTTL, err := client.PTTL(ctx, codeKey(key)).Result()
		if err != nil || afterTTL <= 0 || afterTTL > beforeTTL || beforeTTL-afterTTL > 2*time.Second {
			t.Fatalf("wrong-attempt TTL before=%v after=%v err=%v", beforeTTL, afterTTL, err)
		}
		after, found, err := codes.Get(ctx, key)
		if err != nil || !found || after.Revision == before.Revision || after.UserID != record.UserID || after.SessionID != record.SessionID {
			t.Fatalf("get after=%+v found=%v err=%v", after, found, err)
		}
		second, err := codes.VerifyScoped(ctx, key, record.Scope(), "00000", 9)
		if err != nil || second.Status != store.LoginCodeVerifyInvalid || second.Record.Attempts != 2 {
			t.Fatalf("threshold wrong=%+v err=%v", second, err)
		}
		assertRedisScopedMissing(t, ctx, client, key, record.Scope())
		if result, err := codes.VerifyScoped(ctx, key, record.Scope(), record.Code, 9); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("verify after exhaustion=%+v err=%v", result, err)
		}
	})

	t.Run("correct code consumes both keys exactly once", func(t *testing.T) {
		record := newRecord()
		key := hash("scoped-correct")
		if err := codes.Set(ctx, key, record, time.Minute); err != nil {
			t.Fatal(err)
		}
		expected, found, err := codes.Get(ctx, key)
		if err != nil || !found {
			t.Fatalf("get expected found=%v err=%v", found, err)
		}
		accepted, err := codes.VerifyScoped(ctx, key, record.Scope(), record.Code, 5)
		if err != nil || accepted.Status != store.LoginCodeVerifyAccepted || accepted.Record != expected || accepted.Record.UserID != record.UserID {
			t.Fatalf("accepted=%+v err=%v, want %+v", accepted, err, expected)
		}
		assertRedisScopedMissing(t, ctx, client, key, record.Scope())
		if repeated, err := codes.VerifyScoped(ctx, key, record.Scope(), record.Code, 5); err != nil || repeated.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("repeated verify=%+v err=%v", repeated, err)
		}
	})

	t.Run("legacy corrupt and inconsistent records fail closed", func(t *testing.T) {
		legacy := newRecord()
		legacy.Version = 0
		legacyHash := hash("scoped-legacy")
		if err := codes.Set(ctx, legacyHash, legacy, time.Minute); err != nil {
			t.Fatal(err)
		}
		if result, err := codes.VerifyScoped(ctx, legacyHash, legacy.Scope(), legacy.Code, 5); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("legacy verify=%+v err=%v", result, err)
		}
		assertRedisScopedMissing(t, ctx, client, legacyHash, legacy.Scope())

		corrupt := newRecord()
		corruptHash := hash("scoped-corrupt")
		if err := codes.Set(ctx, corruptHash, corrupt, time.Minute); err != nil {
			t.Fatal(err)
		}
		if err := client.Set(ctx, codeKey(corruptHash), `{`, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		if result, err := codes.VerifyScoped(ctx, corruptHash, corrupt.Scope(), corrupt.Code, 5); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("corrupt verify=%+v err=%v", result, err)
		}
		assertRedisScopedMissing(t, ctx, client, corruptHash, corrupt.Scope())

		inconsistent := newRecord()
		inconsistentHash := hash("scoped-inconsistent")
		if err := codes.Set(ctx, inconsistentHash, inconsistent, time.Minute); err != nil {
			t.Fatal(err)
		}
		stored, found, err := codes.Get(ctx, inconsistentHash)
		if err != nil || !found {
			t.Fatalf("get inconsistent seed found=%v err=%v", found, err)
		}
		stored.Phone = "15550016999"
		raw, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Set(ctx, codeKey(inconsistentHash), raw, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		if result, err := codes.VerifyScoped(ctx, inconsistentHash, inconsistent.Scope(), inconsistent.Code, 5); err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("inconsistent verify=%+v err=%v", result, err)
		}
		assertRedisScopedMissing(t, ctx, client, inconsistentHash, inconsistent.Scope())
	})
}

func TestRedisCodeStoreAtomicScopedConcurrency(t *testing.T) {
	codes, client, hash := newRedisLoginCodeHarness(t)
	ctx := context.Background()
	const workers = 48
	newRecord := func(maxAttempts int) store.PhoneCode {
		return store.PhoneCode{
			Version:     store.PhoneCodeVersionCurrent,
			Phone:       "15550016122",
			Code:        "12345",
			Channel:     store.PhoneCodeChannelPhone,
			Purpose:     store.PhoneCodePurposeChangePhone,
			UserID:      math.MaxInt64 - 122,
			AuthKeyID:   [8]byte{5, 6, 7, 8},
			SessionID:   math.MaxInt64 - 22,
			MaxAttempts: maxAttempts,
		}
	}
	cleanupRecord := newRecord(7)
	t.Cleanup(func() { _ = client.Del(context.Background(), codeScopeKey(cleanupRecord.Scope())).Err() })

	t.Run("correct verification has one winner", func(t *testing.T) {
		record := newRecord(7)
		key := hash("scoped-verify-race")
		if err := codes.Set(ctx, key, record, time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentRedisScopedVerify(t, codes, key, record.Scope(), record.Code, workers)
		if statuses[store.LoginCodeVerifyAccepted] != 1 || statuses[store.LoginCodeVerifyMissing] != workers-1 || statuses[store.LoginCodeVerifyInvalid] != 0 {
			t.Fatalf("correct race statuses=%+v", statuses)
		}
	})

	t.Run("wrong attempts cannot be lost", func(t *testing.T) {
		record := newRecord(7)
		key := hash("scoped-wrong-race")
		if err := codes.Set(ctx, key, record, time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentRedisScopedVerify(t, codes, key, record.Scope(), "00000", workers)
		if statuses[store.LoginCodeVerifyInvalid] != 7 || statuses[store.LoginCodeVerifyMissing] != workers-7 {
			t.Fatalf("wrong race statuses=%+v", statuses)
		}
		assertRedisScopedMissing(t, ctx, client, key, record.Scope())
	})

	t.Run("verification and cancellation share one winner", func(t *testing.T) {
		record := newRecord(7)
		key := hash("scoped-mixed-race")
		if err := codes.Set(ctx, key, record, time.Minute); err != nil {
			t.Fatal(err)
		}
		results := make(chan bool, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(cancel bool) {
				defer wg.Done()
				if cancel {
					_, found, err := codes.ConsumeScoped(ctx, key, record.Scope())
					if err != nil {
						errs <- err
						return
					}
					results <- found
					return
				}
				result, err := codes.VerifyScoped(ctx, key, record.Scope(), record.Code, 5)
				if err != nil {
					errs <- err
					return
				}
				results <- result.Status == store.LoginCodeVerifyAccepted
			}(i%2 == 0)
		}
		wg.Wait()
		close(results)
		close(errs)
		for err := range errs {
			t.Fatalf("verify/cancel race: %v", err)
		}
		if winners := countRedisTrue(results); winners != 1 {
			t.Fatalf("verify/cancel winners=%d, want 1", winners)
		}
	})
}

func concurrentRedisScopedVerify(t *testing.T, codes *CodeStore, hash string, scope store.PhoneCodeScope, code string, workers int) map[store.LoginCodeVerifyStatus]int {
	t.Helper()
	results := make(chan store.LoginCodeVerifyStatus, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := codes.VerifyScoped(context.Background(), hash, scope, code, 5)
			if err != nil {
				errs <- err
				return
			}
			results <- result.Status
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("VerifyScoped: %v", err)
	}
	statuses := make(map[store.LoginCodeVerifyStatus]int)
	for status := range results {
		statuses[status]++
	}
	return statuses
}

func assertRedisScopedMissing(t *testing.T, ctx context.Context, client *redis.Client, hash string, scope store.PhoneCodeScope) {
	t.Helper()
	exists, err := client.Exists(ctx, codeKey(hash), codeScopeKey(scope)).Result()
	if err != nil || exists != 0 {
		t.Fatalf("scoped keys remain=%d err=%v", exists, err)
	}
}
