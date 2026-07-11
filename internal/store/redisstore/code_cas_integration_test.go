package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"telesrv/internal/store"
)

func TestRedisCodeStoreRevisionCAS(t *testing.T) {
	codes, client, hash := newRedisLoginCodeHarness(t)
	ctx := context.Background()
	record := store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		Phone:        "15550016301",
		Code:         "111111",
		Channel:      "email_setup",
		PendingEmail: "first@example.test",
		MaxAttempts:  5,
	}
	key := hash("email-fixed")
	if err := codes.Set(ctx, key, record, 45*time.Second); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := codes.GetSnapshot(ctx, key)
	if err != nil || !found || snapshot.Revision == "" || snapshot.Record.Revision != snapshot.Revision {
		t.Fatalf("snapshot=%+v found=%v err=%v", snapshot, found, err)
	}
	before, err := client.PTTL(ctx, codeKey(key)).Result()
	if err != nil {
		t.Fatal(err)
	}
	next := snapshot.Record
	next.Code = "222222"
	next.Attempts = 1
	if applied, err := codes.CompareAndUpdate(ctx, key, "stale-token", next); err != nil || applied {
		t.Fatalf("wrong-token update applied=%v err=%v", applied, err)
	}
	if applied, err := codes.CompareAndUpdate(ctx, key, snapshot.Revision, next); err != nil || !applied {
		t.Fatalf("current update applied=%v err=%v", applied, err)
	}
	updated, found, err := codes.GetSnapshot(ctx, key)
	if err != nil || !found || updated.Record.Code != next.Code || updated.Record.Attempts != 1 || updated.Revision == snapshot.Revision {
		t.Fatalf("updated=%+v found=%v err=%v", updated, found, err)
	}
	after, err := client.PTTL(ctx, codeKey(key)).Result()
	if err != nil || after <= 0 || after > before || before-after > 2*time.Second {
		t.Fatalf("CAS TTL before=%v after=%v err=%v", before, after, err)
	}
	if applied, err := codes.CompareAndDelete(ctx, key, snapshot.Revision); err != nil || applied {
		t.Fatalf("stale delete applied=%v err=%v", applied, err)
	}
	if applied, err := codes.CompareAndDelete(ctx, key, updated.Revision); err != nil || !applied {
		t.Fatalf("current delete applied=%v err=%v", applied, err)
	}
	assertRedisCodeMissing(t, ctx, codes, key)
}

func TestRedisCodeStoreRevisionCASFailClosedAndScopeIsolation(t *testing.T) {
	codes, client, hash := newRedisLoginCodeHarness(t)
	ctx := context.Background()
	legacyHash := hash("legacy")
	legacy := store.PhoneCode{
		Version:  0,
		Revision: "legacy-revision",
		Phone:    "15550016302",
		Code:     "12345",
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, codeKey(legacyHash), raw, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := codes.GetSnapshot(ctx, legacyHash); err != nil || found {
		t.Fatalf("legacy snapshot found=%v err=%v", found, err)
	}
	assertRedisCodeMissing(t, ctx, codes, legacyHash)

	noRevisionHash := hash("no-revision")
	legacy.Version = store.PhoneCodeVersionCurrent
	legacy.Revision = ""
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, codeKey(noRevisionHash), raw, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if _, found, err := codes.GetSnapshot(ctx, noRevisionHash); err != nil || found {
		t.Fatalf("revisionless snapshot found=%v err=%v", found, err)
	}
	assertRedisCodeMissing(t, ctx, codes, noRevisionHash)

	scopedHash := hash("scoped")
	scoped := store.PhoneCode{
		Version:   store.PhoneCodeVersionCurrent,
		Phone:     "15550016303",
		Code:      "12345",
		Purpose:   store.PhoneCodePurposeChangePhone,
		UserID:    42,
		AuthKeyID: [8]byte{1},
	}
	if err := codes.Set(ctx, scopedHash, scoped, time.Minute); err != nil {
		t.Fatal(err)
	}
	snapshot, found, err := codes.GetSnapshot(ctx, scopedHash)
	if err != nil || !found {
		t.Fatalf("scoped snapshot found=%v err=%v", found, err)
	}
	if applied, err := codes.CompareAndUpdate(ctx, scopedHash, snapshot.Revision, snapshot.Record); err != nil || applied {
		t.Fatalf("scoped update applied=%v err=%v", applied, err)
	}
	if applied, err := codes.CompareAndDelete(ctx, scopedHash, snapshot.Revision); err != nil || applied {
		t.Fatalf("scoped delete applied=%v err=%v", applied, err)
	}
	if _, found, _ := codes.Get(ctx, scopedHash); !found {
		t.Fatal("generic CAS mutated scoped record")
	}
}

func TestRedisCodeStoreRevisionCASPreventsABAAndHasSingleWinner(t *testing.T) {
	codes, _, hash := newRedisLoginCodeHarness(t)
	ctx := context.Background()
	record := store.PhoneCode{
		Version: store.PhoneCodeVersionCurrent,
		Phone:   "15550016304",
		Code:    "123456",
		Channel: "email_change",
	}
	key := hash("aba")
	if err := codes.Set(ctx, key, record, time.Minute); err != nil {
		t.Fatal(err)
	}
	old, found, err := codes.GetSnapshot(ctx, key)
	if err != nil || !found {
		t.Fatalf("old snapshot found=%v err=%v", found, err)
	}
	if err := codes.Set(ctx, key, record, time.Minute); err != nil {
		t.Fatal(err)
	}
	current, found, err := codes.GetSnapshot(ctx, key)
	if err != nil || !found || current.Revision == old.Revision {
		t.Fatalf("replacement current=%+v old=%+v found=%v err=%v", current, old, found, err)
	}
	if applied, err := codes.CompareAndDelete(ctx, key, old.Revision); err != nil || applied {
		t.Fatalf("ABA stale delete applied=%v err=%v", applied, err)
	}

	const workers = 48
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			next := current.Record
			next.Code = fmt.Sprintf("%06d", index)
			applied, err := codes.CompareAndUpdate(ctx, key, current.Revision, next)
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
	if winners := countRedisTrue(results); winners != 1 {
		t.Fatalf("concurrent update winners=%d, want 1", winners)
	}
	winner, found, err := codes.GetSnapshot(ctx, key)
	if err != nil || !found || winner.Revision == current.Revision {
		t.Fatalf("winner=%+v found=%v err=%v", winner, found, err)
	}

	results = make(chan bool, workers)
	errs = make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			applied, err := codes.CompareAndDelete(ctx, key, winner.Revision)
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
	if winners := countRedisTrue(results); winners != 1 {
		t.Fatalf("concurrent delete winners=%d, want 1", winners)
	}
}

func TestRedisCodeStoreLegacyUpdateCannotResurrectConsumedKey(t *testing.T) {
	codes, _, hash := newRedisLoginCodeHarness(t)
	ctx := context.Background()
	record := store.PhoneCode{
		Version: store.PhoneCodeVersionCurrent,
		Phone:   "15550016305",
		Code:    "12345",
		Channel: store.PhoneCodeChannelPhone,
	}
	key := hash("no-resurrection")
	if err := codes.Set(ctx, key, record, time.Minute); err != nil {
		t.Fatal(err)
	}
	stale, found, err := codes.Get(ctx, key)
	if err != nil || !found {
		t.Fatalf("load stale record found=%v err=%v", found, err)
	}
	if _, found, err := codes.TakeLoginCode(ctx, key, record.Phone); err != nil || !found {
		t.Fatalf("consume before stale update found=%v err=%v", found, err)
	}
	stale.Attempts++
	if err := codes.Update(ctx, key, stale); err != nil {
		t.Fatalf("stale legacy Update: %v", err)
	}
	assertRedisCodeMissing(t, ctx, codes, key)
}
