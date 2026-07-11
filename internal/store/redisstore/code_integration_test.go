package redisstore

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"telesrv/internal/store"
)

func TestRedisCodeStoreScopedRotationAndSingleConsume(t *testing.T) {
	addr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TELESRV_TEST_REDIS_ADDR to run redis integration test")
	}
	ctx := context.Background()
	c, err := Open(ctx, addr, "", 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	suffix := time.Now().UnixNano()
	oldHash := fmt.Sprintf("scope-old-%d", suffix)
	newHash := fmt.Sprintf("scope-new-%d", suffix)
	rec := store.PhoneCode{
		Version:   store.PhoneCodeVersionCurrent,
		Phone:     fmt.Sprintf("1555%d", suffix),
		Code:      "12345",
		Purpose:   store.PhoneCodePurposeChangePhone,
		UserID:    suffix,
		AuthKeyID: [8]byte{1, 2, 3, 4},
	}
	scopeKey := codeScopeKey(rec.Scope())
	t.Cleanup(func() { _ = c.Del(ctx, codeKey(oldHash), codeKey(newHash), scopeKey).Err() })
	codes := NewCodeStore(c)
	if err := codes.Set(ctx, oldHash, rec, time.Minute); err != nil {
		t.Fatalf("set old: %v", err)
	}
	if err := codes.Set(ctx, newHash, rec, time.Minute); err != nil {
		t.Fatalf("rotate new: %v", err)
	}
	if _, found, err := codes.Get(ctx, oldHash); err != nil || found {
		t.Fatalf("old hash found=%v err=%v", found, err)
	}

	const workers = 24
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, found, err := codes.ConsumeScoped(ctx, newHash, rec.Scope())
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
		t.Fatalf("consume: %v", err)
	}
	foundCount := 0
	for found := range results {
		if found {
			foundCount++
		}
	}
	if foundCount != 1 {
		t.Fatalf("successful consumes = %d, want 1", foundCount)
	}
	if exists, err := c.Exists(ctx, codeKey(newHash), scopeKey).Result(); err != nil || exists != 0 {
		t.Fatalf("remaining redis keys=%d err=%v", exists, err)
	}
}

func TestRedisCodeStoreConsumeScopedRejectsAndDeletesLegacyVersion(t *testing.T) {
	addr := os.Getenv("TELESRV_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TELESRV_TEST_REDIS_ADDR to run redis integration test")
	}
	ctx := context.Background()
	c, err := Open(ctx, addr, "", 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	hash := fmt.Sprintf("legacy-scope-%d", time.Now().UnixNano())
	rec := store.PhoneCode{
		Version: 0, Phone: "15550015004", Code: "12345",
		Purpose: store.PhoneCodePurposeChangePhone, UserID: 44, AuthKeyID: [8]byte{4},
	}
	scopeKey := codeScopeKey(rec.Scope())
	t.Cleanup(func() { _ = c.Del(ctx, codeKey(hash), scopeKey).Err() })
	codes := NewCodeStore(c)
	if err := codes.Set(ctx, hash, rec, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, found, err := codes.ConsumeScoped(ctx, hash, rec.Scope()); err != nil || found {
		t.Fatalf("legacy scoped consume found=%v err=%v, want false/nil", found, err)
	}
	if exists, err := c.Exists(ctx, codeKey(hash), scopeKey).Result(); err != nil || exists != 0 {
		t.Fatalf("legacy scoped keys remain=%d err=%v", exists, err)
	}
}
