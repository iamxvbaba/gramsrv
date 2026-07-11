package redisstore

import (
	"context"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/store"
)

func TestRedisCodeStoreAtomicLoginStateMachine(t *testing.T) {
	codes, client, hash := newRedisLoginCodeHarness(t)
	ctx := context.Background()
	const phone = "15550016101"
	newRecord := func() store.PhoneCode {
		return store.PhoneCode{
			Version:      store.PhoneCodeVersionCurrent,
			IssuedUserID: 1000000101,
			Phone:        phone,
			Code:         "12345",
			Channel:      store.PhoneCodeChannelPhone,
			MaxAttempts:  2,
		}
	}

	t.Run("version and corrupt records fail closed", func(t *testing.T) {
		legacy := newRecord()
		legacy.Version = 0
		verifyHash := hash("legacy-verify")
		if err := codes.Set(ctx, verifyHash, legacy, time.Minute); err != nil {
			t.Fatal(err)
		}
		result, err := codes.VerifyLogin(ctx, verifyHash, phone, legacy.Code, false, 5)
		if err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("legacy verify = %+v err=%v", result, err)
		}
		assertRedisCodeMissing(t, ctx, codes, verifyHash)

		// This is the actual pre-state-machine JSON shape: int64 fields were
		// numbers, not the quoted strings emitted by current Set.
		legacyRaw := fmt.Sprintf(
			`{"Version":0,"IssuedUserID":1000000101,"Phone":%q,"Code":"12345","Channel":"phone","UserID":42,"SessionID":9007199254740993}`,
			phone,
		)
		getLegacyHash := hash("legacy-raw-number-get")
		if err := client.Set(ctx, codeKey(getLegacyHash), legacyRaw, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		if got, found, err := codes.Get(ctx, getLegacyHash); err != nil || found {
			t.Fatalf("legacy raw-number Get=%+v found=%v err=%v, want Missing", got, found, err)
		}
		if exists, err := client.Exists(ctx, codeKey(getLegacyHash)).Result(); err != nil || exists != 0 {
			t.Fatalf("legacy raw-number Get left key exists=%d err=%v", exists, err)
		}

		verifyLegacyHash := hash("legacy-raw-number-verify")
		if err := client.Set(ctx, codeKey(verifyLegacyHash), legacyRaw, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		result, err = codes.VerifyLogin(ctx, verifyLegacyHash, phone, "12345", false, 5)
		if err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("legacy raw-number VerifyLogin=%+v err=%v, want Missing", result, err)
		}
		if exists, err := client.Exists(ctx, codeKey(verifyLegacyHash)).Result(); err != nil || exists != 0 {
			t.Fatalf("legacy raw-number VerifyLogin left key exists=%d err=%v", exists, err)
		}

		unknown := newRecord()
		unknown.Version++
		takeHash := hash("unknown-take")
		if err := codes.Set(ctx, takeHash, unknown, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, found, err := codes.TakeLoginCode(ctx, takeHash, phone); err != nil || found {
			t.Fatalf("unknown take found=%v err=%v", found, err)
		}
		assertRedisCodeMissing(t, ctx, codes, takeHash)

		legacy.SignUpVerified = true
		consumeHash := hash("legacy-consume")
		if err := codes.Set(ctx, consumeHash, legacy, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, found, err := codes.ConsumeSignUpVerified(ctx, consumeHash, phone); err != nil || found {
			t.Fatalf("legacy sign-up consume found=%v err=%v", found, err)
		}
		assertRedisCodeMissing(t, ctx, codes, consumeHash)

		corruptHash := hash("corrupt")
		if err := client.Set(ctx, codeKey(corruptHash), `{`, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
		result, err = codes.VerifyLogin(ctx, corruptHash, phone, "12345", false, 5)
		if err != nil || result.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("corrupt verify = %+v err=%v", result, err)
		}
		assertRedisCodeMissing(t, ctx, codes, corruptHash)
	})

	t.Run("scope channel and issued-user gates", func(t *testing.T) {
		record := newRecord()
		scopeHash := hash("scope")
		if err := codes.Set(ctx, scopeHash, record, time.Minute); err != nil {
			t.Fatal(err)
		}
		result, err := codes.VerifyLogin(ctx, scopeHash, "15550016999", record.Code, false, 5)
		if err != nil || result.Status != store.LoginCodeVerifyInvalid || result.Record.Attempts != 0 {
			t.Fatalf("cross-phone verify = %+v err=%v", result, err)
		}
		stored, found, err := codes.Get(ctx, scopeHash)
		if err != nil || !found || stored.Attempts != 0 {
			t.Fatalf("cross-phone stored=%+v found=%v err=%v", stored, found, err)
		}

		wrongChannel := record
		wrongChannel.Channel = "email_setup"
		channelHash := hash("channel")
		if err := codes.Set(ctx, channelHash, wrongChannel, time.Minute); err != nil {
			t.Fatal(err)
		}
		result, err = codes.VerifyLogin(ctx, channelHash, phone, wrongChannel.Code, false, 5)
		if err != nil || result.Status != store.LoginCodeVerifyInvalid {
			t.Fatalf("wrong-channel verify = %+v err=%v", result, err)
		}
		if _, found, err := codes.TakeLoginCode(ctx, channelHash, phone); err != nil || found {
			t.Fatalf("wrong-channel take found=%v err=%v", found, err)
		}

		issuedHash := hash("issued-existing")
		record.IssuedUserID = math.MaxInt64 - 1
		if err := codes.Set(ctx, issuedHash, record, time.Minute); err != nil {
			t.Fatal(err)
		}
		result, err = codes.VerifyLogin(ctx, issuedHash, phone, record.Code, true, 5)
		if err != nil || result.Status != store.LoginCodeVerifyInvalid || result.Record.IssuedUserID != record.IssuedUserID {
			t.Fatalf("issued-existing keep = %+v err=%v, want exact int64 Invalid", result, err)
		}
		taken, found, err := codes.TakeLoginCode(ctx, issuedHash, phone)
		if err != nil || !found || taken.IssuedUserID != record.IssuedUserID {
			t.Fatalf("issued-existing take=%+v found=%v err=%v", taken, found, err)
		}

		setupRequired := newRecord()
		setupRequired.Channel = store.PhoneCodeChannelEmailSetupRequired
		setupRequired.Code = ""
		setupHash := hash("setup-required")
		if err := codes.Set(ctx, setupHash, setupRequired, time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, found, err := codes.TakeLoginCode(ctx, setupHash, phone); err != nil || !found {
			t.Fatalf("setup-required take found=%v err=%v, want true", found, err)
		}
	})

	t.Run("wrong attempts and ttl are atomic", func(t *testing.T) {
		record := newRecord()
		wrongHash := hash("wrong")
		if err := codes.Set(ctx, wrongHash, record, 45*time.Second); err != nil {
			t.Fatal(err)
		}
		before, err := client.PTTL(ctx, codeKey(wrongHash)).Result()
		if err != nil {
			t.Fatal(err)
		}
		first, err := codes.VerifyLogin(ctx, wrongHash, phone, "00000", false, 9)
		if err != nil || first.Status != store.LoginCodeVerifyInvalid || first.Record.Attempts != 1 {
			t.Fatalf("first wrong = %+v err=%v", first, err)
		}
		after, err := client.PTTL(ctx, codeKey(wrongHash)).Result()
		if err != nil || after <= 0 || after > before || before-after > 2*time.Second {
			t.Fatalf("wrong-code TTL before=%v after=%v err=%v, want KEEPTTL", before, after, err)
		}
		second, err := codes.VerifyLogin(ctx, wrongHash, phone, "00000", false, 9)
		if err != nil || second.Status != store.LoginCodeVerifyInvalid || second.Record.Attempts != 2 {
			t.Fatalf("threshold wrong = %+v err=%v", second, err)
		}
		assertRedisCodeMissing(t, ctx, codes, wrongHash)
	})

	t.Run("consume and sign-up marker terminal states", func(t *testing.T) {
		record := newRecord()
		consumeHash := hash("verify-consume")
		if err := codes.Set(ctx, consumeHash, record, time.Minute); err != nil {
			t.Fatal(err)
		}
		accepted, err := codes.VerifyLogin(ctx, consumeHash, phone, record.Code, false, 5)
		if err != nil || accepted.Status != store.LoginCodeVerifyAccepted {
			t.Fatalf("consume verify = %+v err=%v", accepted, err)
		}
		assertRedisCodeMissing(t, ctx, codes, consumeHash)

		signUp := record
		signUp.IssuedUserID = 0
		signUpHash := hash("signup")
		if err := codes.Set(ctx, signUpHash, signUp, 45*time.Second); err != nil {
			t.Fatal(err)
		}
		before, err := client.PTTL(ctx, codeKey(signUpHash)).Result()
		if err != nil {
			t.Fatal(err)
		}
		marked, err := codes.VerifyLogin(ctx, signUpHash, phone, signUp.Code, true, 5)
		if err != nil || marked.Status != store.LoginCodeVerifyAccepted || !marked.Record.SignUpVerified {
			t.Fatalf("mark sign-up = %+v err=%v", marked, err)
		}
		after, err := client.PTTL(ctx, codeKey(signUpHash)).Result()
		if err != nil || after <= 0 || after > before || before-after > 2*time.Second {
			t.Fatalf("marker TTL before=%v after=%v err=%v, want KEEPTTL", before, after, err)
		}
		repeated, err := codes.VerifyLogin(ctx, signUpHash, phone, signUp.Code, true, 5)
		if err != nil || repeated.Status != store.LoginCodeVerifyMissing {
			t.Fatalf("repeated marker verify = %+v err=%v", repeated, err)
		}
		if _, found, err := codes.TakeLoginCode(ctx, signUpHash, phone); err != nil || found {
			t.Fatalf("terminal marker take found=%v err=%v, want false", found, err)
		}
		consumed, found, err := codes.ConsumeSignUpVerified(ctx, signUpHash, phone)
		if err != nil || !found || !consumed.SignUpVerified || consumed.IssuedUserID != 0 {
			t.Fatalf("consume marker=%+v found=%v err=%v", consumed, found, err)
		}
		if _, found, err := codes.ConsumeSignUpVerified(ctx, signUpHash, phone); err != nil || found {
			t.Fatalf("second marker consume found=%v err=%v", found, err)
		}
	})
}

func TestRedisCodeStoreAtomicLoginConcurrency(t *testing.T) {
	codes, _, hash := newRedisLoginCodeHarness(t)
	ctx := context.Background()
	const (
		phone   = "15550016102"
		workers = 48
	)
	newRecord := func() store.PhoneCode {
		return store.PhoneCode{
			Version:     store.PhoneCodeVersionCurrent,
			Phone:       phone,
			Code:        "12345",
			Channel:     store.PhoneCodeChannelPhone,
			MaxAttempts: 7,
		}
	}

	t.Run("consume verify one accepted", func(t *testing.T) {
		key := hash("verify-race")
		if err := codes.Set(ctx, key, newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentRedisVerify(t, codes, key, phone, "12345", false, workers)
		if statuses[store.LoginCodeVerifyAccepted] != 1 || statuses[store.LoginCodeVerifyMissing] != workers-1 || statuses[store.LoginCodeVerifyInvalid] != 0 {
			t.Fatalf("verify race statuses=%+v", statuses)
		}
	})

	t.Run("mark and consume each one winner", func(t *testing.T) {
		key := hash("signup-race")
		if err := codes.Set(ctx, key, newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentRedisVerify(t, codes, key, phone, "12345", true, workers)
		if statuses[store.LoginCodeVerifyAccepted] != 1 || statuses[store.LoginCodeVerifyMissing] != workers-1 {
			t.Fatalf("sign-up verify race statuses=%+v", statuses)
		}
		if found := concurrentRedisConsume(t, codes, key, phone, workers); found != 1 {
			t.Fatalf("sign-up consume winners=%d, want 1", found)
		}
	})

	t.Run("take one winner", func(t *testing.T) {
		key := hash("take-race")
		if err := codes.Set(ctx, key, newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		if found := concurrentRedisTake(t, codes, key, phone, workers); found != 1 {
			t.Fatalf("take winners=%d, want 1", found)
		}
	})

	t.Run("wrong attempts cannot be lost", func(t *testing.T) {
		key := hash("wrong-race")
		if err := codes.Set(ctx, key, newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		statuses := concurrentRedisVerify(t, codes, key, phone, "00000", false, workers)
		if statuses[store.LoginCodeVerifyInvalid] != 7 || statuses[store.LoginCodeVerifyMissing] != workers-7 {
			t.Fatalf("wrong race statuses=%+v", statuses)
		}
		assertRedisCodeMissing(t, ctx, codes, key)
	})

	t.Run("verify and take share one winner", func(t *testing.T) {
		key := hash("mixed-race")
		if err := codes.Set(ctx, key, newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		results := make(chan bool, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(take bool) {
				defer wg.Done()
				if take {
					_, found, err := codes.TakeLoginCode(ctx, key, phone)
					if err != nil {
						errs <- err
						return
					}
					results <- found
					return
				}
				result, err := codes.VerifyLogin(ctx, key, phone, "12345", false, 5)
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
			t.Fatalf("mixed race: %v", err)
		}
		winners := 0
		for won := range results {
			if won {
				winners++
			}
		}
		if winners != 1 {
			t.Fatalf("mixed race winners=%d, want 1", winners)
		}
	})

	t.Run("sign-up mark and take share one winner", func(t *testing.T) {
		key := hash("mixed-signup-race")
		if err := codes.Set(ctx, key, newRecord(), time.Minute); err != nil {
			t.Fatal(err)
		}
		results := make(chan bool, workers)
		errs := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(take bool) {
				defer wg.Done()
				if take {
					_, found, err := codes.TakeLoginCode(ctx, key, phone)
					if err != nil {
						errs <- err
						return
					}
					results <- found
					return
				}
				result, err := codes.VerifyLogin(ctx, key, phone, "12345", true, 5)
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
			t.Fatalf("mixed sign-up race: %v", err)
		}
		if winners := countRedisTrue(results); winners != 1 {
			t.Fatalf("mixed sign-up race winners=%d, want 1", winners)
		}
	})
}

func newRedisLoginCodeHarness(t *testing.T) (*CodeStore, *redis.Client, func(string) string) {
	t.Helper()
	addr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TELESRV_TEST_REDIS_ADDR to run redis login-code integration tests")
	}
	ctx := context.Background()
	client, err := Open(ctx, addr, "", 0)
	if err != nil {
		t.Fatalf("open redis: %v", err)
	}
	prefix := fmt.Sprintf("atomic-login-%d-", time.Now().UnixNano())
	var mu sync.Mutex
	keys := make([]string, 0)
	newHash := func(label string) string {
		value := prefix + label
		mu.Lock()
		keys = append(keys, codeKey(value))
		mu.Unlock()
		return value
	}
	t.Cleanup(func() {
		mu.Lock()
		cleanup := append([]string(nil), keys...)
		mu.Unlock()
		if len(cleanup) > 0 {
			_ = client.Del(context.Background(), cleanup...).Err()
		}
		_ = client.Close()
	})
	return NewCodeStore(client), client, newHash
}

func assertRedisCodeMissing(t *testing.T, ctx context.Context, codes *CodeStore, hash string) {
	t.Helper()
	if _, found, err := codes.Get(ctx, hash); err != nil || found {
		t.Fatalf("code %q found=%v err=%v, want missing", hash, found, err)
	}
}

func concurrentRedisVerify(t *testing.T, codes *CodeStore, hash, phone, code string, keep bool, workers int) map[store.LoginCodeVerifyStatus]int {
	t.Helper()
	ctx := context.Background()
	results := make(chan store.LoginCodeVerifyStatus, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := codes.VerifyLogin(ctx, hash, phone, code, keep, 5)
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
		t.Fatalf("VerifyLogin: %v", err)
	}
	counts := make(map[store.LoginCodeVerifyStatus]int)
	for status := range results {
		counts[status]++
	}
	return counts
}

func concurrentRedisTake(t *testing.T, codes *CodeStore, hash, phone string, workers int) int {
	t.Helper()
	ctx := context.Background()
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, found, err := codes.TakeLoginCode(ctx, hash, phone)
			if err != nil {
				errs <- err
				return
			}
			results <- found
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("TakeLoginCode: %v", err)
	}
	return countRedisTrue(results)
}

func concurrentRedisConsume(t *testing.T, codes *CodeStore, hash, phone string, workers int) int {
	t.Helper()
	ctx := context.Background()
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, found, err := codes.ConsumeSignUpVerified(ctx, hash, phone)
			if err != nil {
				errs <- err
				return
			}
			results <- found
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("ConsumeSignUpVerified: %v", err)
	}
	return countRedisTrue(results)
}

func countRedisTrue(results <-chan bool) int {
	count := 0
	for found := range results {
		if found {
			count++
		}
	}
	return count
}
