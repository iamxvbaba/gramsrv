package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"telesrv/internal/store"
)

func TestCodeStoreRevisionCAS(t *testing.T) {
	ctx := context.Background()
	codes := NewCodeStore()
	record := store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		Phone:        "15550016201",
		Code:         "111111",
		Channel:      "email_setup",
		PendingEmail: "first@example.test",
		MaxAttempts:  5,
	}
	if err := codes.Set(ctx, "email-fixed", record, time.Minute); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := codes.GetSnapshot(ctx, "email-fixed")
	if err != nil || !found || snapshot.Revision == "" || snapshot.Record.Revision != snapshot.Revision {
		t.Fatalf("snapshot=%+v found=%v err=%v", snapshot, found, err)
	}
	originalExpiry := codes.m["email-fixed"].expires

	next := snapshot.Record
	next.Code = "222222"
	next.Attempts = 1
	if applied, err := codes.CompareAndUpdate(ctx, "email-fixed", "stale-token", next); err != nil || applied {
		t.Fatalf("wrong-token update applied=%v err=%v", applied, err)
	}
	unchanged, found, err := codes.GetSnapshot(ctx, "email-fixed")
	if err != nil || !found || unchanged.Revision != snapshot.Revision || unchanged.Record.Code != record.Code {
		t.Fatalf("after wrong-token snapshot=%+v found=%v err=%v", unchanged, found, err)
	}
	if applied, err := codes.CompareAndUpdate(ctx, "email-fixed", snapshot.Revision, next); err != nil || !applied {
		t.Fatalf("current-token update applied=%v err=%v", applied, err)
	}
	updated, found, err := codes.GetSnapshot(ctx, "email-fixed")
	if err != nil || !found || updated.Record.Code != next.Code || updated.Record.Attempts != 1 || updated.Revision == snapshot.Revision {
		t.Fatalf("updated snapshot=%+v found=%v err=%v", updated, found, err)
	}
	if expiry := codes.m["email-fixed"].expires; !expiry.Equal(originalExpiry) {
		t.Fatalf("CAS update expiry=%v, want unchanged %v", expiry, originalExpiry)
	}
	if applied, err := codes.CompareAndDelete(ctx, "email-fixed", snapshot.Revision); err != nil || applied {
		t.Fatalf("stale delete applied=%v err=%v", applied, err)
	}
	if applied, err := codes.CompareAndDelete(ctx, "email-fixed", updated.Revision); err != nil || !applied {
		t.Fatalf("current delete applied=%v err=%v", applied, err)
	}
	if _, found, _ := codes.GetSnapshot(ctx, "email-fixed"); found {
		t.Fatal("CAS-deleted code remains")
	}
}

func TestCodeStoreRevisionCASFailClosedAndScopeIsolation(t *testing.T) {
	ctx := context.Background()
	codes := NewCodeStore()
	now := time.Now().Add(time.Minute)
	codes.m["legacy"] = codeEntry{
		code:    store.PhoneCode{Version: 0, Phone: "15550016202", Code: "12345"},
		expires: now,
	}
	if _, found, err := codes.GetSnapshot(ctx, "legacy"); err != nil || found {
		t.Fatalf("legacy snapshot found=%v err=%v", found, err)
	}
	if _, found := codes.m["legacy"]; found {
		t.Fatal("legacy snapshot record was not deleted")
	}
	codes.m["no-revision"] = codeEntry{
		code: store.PhoneCode{
			Version: store.PhoneCodeVersionCurrent,
			Phone:   "15550016202",
			Code:    "12345",
		},
		expires: now,
	}
	if _, found, err := codes.GetSnapshot(ctx, "no-revision"); err != nil || found {
		t.Fatalf("revisionless snapshot found=%v err=%v", found, err)
	}
	if _, found := codes.m["no-revision"]; found {
		t.Fatal("revisionless snapshot record was not deleted")
	}

	scoped := store.PhoneCode{
		Version:   store.PhoneCodeVersionCurrent,
		Phone:     "15550016203",
		Code:      "12345",
		Purpose:   store.PhoneCodePurposeChangePhone,
		UserID:    42,
		AuthKeyID: [8]byte{1},
	}
	if err := codes.Set(ctx, "scoped-cas", scoped, time.Minute); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := codes.GetSnapshot(ctx, "scoped-cas")
	if err != nil || !found {
		t.Fatalf("scoped snapshot found=%v err=%v", found, err)
	}
	if applied, err := codes.CompareAndUpdate(ctx, "scoped-cas", snapshot.Revision, snapshot.Record); err != nil || applied {
		t.Fatalf("scoped update applied=%v err=%v", applied, err)
	}
	if applied, err := codes.CompareAndDelete(ctx, "scoped-cas", snapshot.Revision); err != nil || applied {
		t.Fatalf("scoped delete applied=%v err=%v", applied, err)
	}
	if _, found, _ := codes.Get(ctx, "scoped-cas"); !found {
		t.Fatal("generic CAS mutated scoped code")
	}
}

func TestCodeStoreRevisionCASPreventsABAAndHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	codes := NewCodeStore()
	record := store.PhoneCode{
		Version: store.PhoneCodeVersionCurrent,
		Phone:   "15550016204",
		Code:    "123456",
		Channel: "email_change",
	}
	if err := codes.Set(ctx, "aba", record, time.Minute); err != nil {
		t.Fatal(err)
	}
	old, found, err := codes.GetSnapshot(ctx, "aba")
	if err != nil || !found {
		t.Fatalf("old snapshot found=%v err=%v", found, err)
	}
	if err := codes.Set(ctx, "aba", record, time.Minute); err != nil {
		t.Fatal(err)
	}
	current, found, err := codes.GetSnapshot(ctx, "aba")
	if err != nil || !found || current.Revision == old.Revision {
		t.Fatalf("replacement snapshot=%+v old=%+v found=%v err=%v", current, old, found, err)
	}
	if applied, err := codes.CompareAndDelete(ctx, "aba", old.Revision); err != nil || applied {
		t.Fatalf("ABA stale delete applied=%v err=%v", applied, err)
	}

	const workers = 64
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			next := current.Record
			next.Code = fmt.Sprintf("%06d", index)
			applied, err := codes.CompareAndUpdate(ctx, "aba", current.Revision, next)
			if err != nil {
				errs <- err
				return
			}
			results <- applied
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent CAS update: %v", err)
	}
	winners := 0
	for applied := range results {
		if applied {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent CAS update winners=%d, want 1", winners)
	}
	winner, found, err := codes.GetSnapshot(ctx, "aba")
	if err != nil || !found || winner.Revision == current.Revision {
		t.Fatalf("winner snapshot=%+v found=%v err=%v", winner, found, err)
	}

	results = make(chan bool, workers)
	errs = make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			applied, err := codes.CompareAndDelete(ctx, "aba", winner.Revision)
			if err != nil {
				errs <- err
				return
			}
			results <- applied
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent CAS delete: %v", err)
	}
	winners = 0
	for applied := range results {
		if applied {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent CAS delete winners=%d, want 1", winners)
	}
}
