package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/store"
)

func TestCodeStoreScopedRotationAndSingleConsume(t *testing.T) {
	ctx := context.Background()
	codes := NewCodeStore()
	rec := store.PhoneCode{
		Version:   store.PhoneCodeVersionCurrent,
		Phone:     "15550015001",
		Code:      "12345",
		Purpose:   store.PhoneCodePurposeChangePhone,
		UserID:    42,
		AuthKeyID: [8]byte{1, 2, 3},
	}
	if err := codes.Set(ctx, "old-hash", rec, time.Minute); err != nil {
		t.Fatalf("set old: %v", err)
	}
	if err := codes.Set(ctx, "new-hash", rec, time.Minute); err != nil {
		t.Fatalf("rotate new: %v", err)
	}
	if _, found, err := codes.Get(ctx, "old-hash"); err != nil || found {
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
			_, found, err := codes.ConsumeScoped(ctx, "new-hash", rec.Scope())
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
	if _, found, _ := codes.Get(ctx, "new-hash"); found {
		t.Fatal("consumed hash remains")
	}
}

func TestCodeStoreScopedIsolation(t *testing.T) {
	ctx := context.Background()
	codes := NewCodeStore()
	a := store.PhoneCode{Version: store.PhoneCodeVersionCurrent, Phone: "15550015002", Code: "12345", Purpose: store.PhoneCodePurposeChangePhone, UserID: 42, AuthKeyID: [8]byte{1}}
	b := a
	b.AuthKeyID = [8]byte{2}
	if err := codes.Set(ctx, "hash-a", a, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := codes.Set(ctx, "hash-b", b, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := codes.ConsumeScoped(ctx, "hash-a", b.Scope()); found {
		t.Fatal("cross-scope consume succeeded")
	}
	if _, found, _ := codes.Get(ctx, "hash-a"); !found {
		t.Fatal("cross-scope consume removed victim code")
	}
	if _, found, err := codes.ConsumeScoped(ctx, "hash-a", a.Scope()); err != nil || !found {
		t.Fatalf("own-scope consume found=%v err=%v", found, err)
	}
	if _, found, _ := codes.Get(ctx, "hash-b"); !found {
		t.Fatal("other scope was removed")
	}
}

func TestCodeStoreConsumeScopedRejectsAndDeletesLegacyVersion(t *testing.T) {
	ctx := context.Background()
	codes := NewCodeStore()
	rec := store.PhoneCode{
		Version: 0, Phone: "15550015003", Code: "12345",
		Purpose: store.PhoneCodePurposeChangePhone, UserID: 43, AuthKeyID: [8]byte{3},
	}
	if err := codes.Set(ctx, "legacy-scope", rec, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, found, err := codes.ConsumeScoped(ctx, "legacy-scope", rec.Scope()); err != nil || found {
		t.Fatalf("legacy scoped consume found=%v err=%v, want false/nil", found, err)
	}
	if _, found, _ := codes.Get(ctx, "legacy-scope"); found {
		t.Fatal("legacy scoped code remains after fail-closed consume")
	}
	if _, ok := codes.scopes[rec.Scope()]; ok {
		t.Fatal("legacy scoped index remains after fail-closed consume")
	}
}
