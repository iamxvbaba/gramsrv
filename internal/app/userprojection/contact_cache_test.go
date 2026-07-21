package userprojection

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type blockingFirstListContactStore struct {
	store.ContactStore
	started chan struct{}
	release chan struct{}
	first   domain.ContactList

	mu        sync.Mutex
	firstUsed bool
}

type blockingFirstPersonalPhotoStore struct {
	store.ContactStore
	started chan struct{}
	release chan struct{}
	first   map[int64]domain.ProfilePhotoRef

	mu        sync.Mutex
	firstUsed bool
}

func (s *blockingFirstListContactStore) ListByUser(ctx context.Context, userID int64) (domain.ContactList, error) {
	s.mu.Lock()
	if !s.firstUsed {
		s.firstUsed = true
		s.mu.Unlock()
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return domain.ContactList{}, ctx.Err()
		}
		return domain.ContactList{Contacts: cloneCachedContacts(s.first.Contacts), Hash: s.first.Hash}, nil
	}
	s.mu.Unlock()
	return s.ContactStore.ListByUser(ctx, userID)
}

func (s *blockingFirstPersonalPhotoStore) PersonalPhotos(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	s.mu.Lock()
	if !s.firstUsed {
		s.firstUsed = true
		first := cloneCachedProfilePhotoRefs(s.first)
		s.mu.Unlock()
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return first, nil
	}
	s.mu.Unlock()
	return s.ContactStore.PersonalPhotos(ctx, userID, contactUserIDs)
}

func (s *blockingFirstPersonalPhotoStore) SetPersonalPhoto(ctx context.Context, userID, contactUserID int64, photoID int64, date int) (domain.Contact, bool, error) {
	return s.ContactStore.SetPersonalPhoto(ctx, userID, contactUserID, photoID, date)
}

func waitForCacheTestSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cache test signal")
	}
}

type countingContactStore struct {
	store.ContactStore
	listCalls           int
	getManyCalls        int
	reverseCalls        int
	personalPhotoCalls  int
	setPersonalPhotoHit int
}

func (s *countingContactStore) ListByUser(ctx context.Context, userID int64) (domain.ContactList, error) {
	s.listCalls++
	return s.ContactStore.ListByUser(ctx, userID)
}

func (s *countingContactStore) GetMany(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.Contact, error) {
	s.getManyCalls++
	return s.ContactStore.GetMany(ctx, userID, contactUserIDs)
}

func (s *countingContactStore) GetReverseContacts(ctx context.Context, userID int64, ownerUserIDs []int64) (map[int64]domain.Contact, error) {
	s.reverseCalls++
	return s.ContactStore.GetReverseContacts(ctx, userID, ownerUserIDs)
}

func (s *countingContactStore) PersonalPhotos(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error) {
	s.personalPhotoCalls++
	return s.ContactStore.PersonalPhotos(ctx, userID, contactUserIDs)
}

func (s *countingContactStore) SetPersonalPhoto(ctx context.Context, userID, contactUserID int64, photoID int64, date int) (domain.Contact, bool, error) {
	s.setPersonalPhotoHit++
	return s.ContactStore.SetPersonalPhoto(ctx, userID, contactUserID, photoID, date)
}

func TestCachedContactStoreCachesProjectionReads(t *testing.T) {
	ctx := context.Background()
	base := memory.NewContactStore()
	if _, err := base.Upsert(ctx, 1, domain.ContactInput{
		ContactUserID: 2,
		FirstName:     "Alice",
		Phone:         "111",
		Note:          "private note",
		NoteEntities:  []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 7}},
	}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	counting := &countingContactStore{ContactStore: base}
	cached := NewCachedContactStore(counting, 0)

	first, err := cached.GetMany(ctx, 1, []int64{2, 3})
	if err != nil {
		t.Fatalf("get many first: %v", err)
	}
	if first[2].FirstName != "Alice" || first[2].Note != "private note" || len(first[2].NoteEntities) != 1 {
		t.Fatalf("first contact = %+v, want Alice with private note", first[2])
	}
	first[2].NoteEntities[0].Length = 99
	second, err := cached.GetMany(ctx, 1, []int64{2, 3})
	if err != nil {
		t.Fatalf("get many second: %v", err)
	}
	if second[2].FirstName != "Alice" || second[2].Note != "private note" || len(second[2].NoteEntities) != 1 || second[2].NoteEntities[0].Length != 7 {
		t.Fatalf("second contact = %+v, want isolated cached Alice note", second[2])
	}
	if counting.listCalls != 1 {
		t.Fatalf("ListByUser calls = %d, want 1 account snapshot load", counting.listCalls)
	}
	if counting.getManyCalls != 0 {
		t.Fatalf("GetMany calls = %d, want 0 with account snapshot", counting.getManyCalls)
	}

	reverse, err := cached.GetReverseContacts(ctx, 2, []int64{1})
	if err != nil {
		t.Fatalf("get reverse: %v", err)
	}
	if reverse[1].FirstName != "Alice" {
		t.Fatalf("reverse contact = %+v, want Alice", reverse[1])
	}
	if counting.reverseCalls != 0 {
		t.Fatalf("GetReverseContacts calls = %d, want 0 from shared contact cache", counting.reverseCalls)
	}
}

func TestCachedContactStoreInvalidatesAccountSnapshot(t *testing.T) {
	ctx := context.Background()
	base := memory.NewContactStore()
	if _, err := base.Upsert(ctx, 1, domain.ContactInput{ContactUserID: 2, FirstName: "Alice"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	counting := &countingContactStore{ContactStore: base}
	cached := NewCachedContactStore(counting, 0)

	first, err := cached.GetMany(ctx, 1, []int64{2})
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	if first[2].FirstName != "Alice" {
		t.Fatalf("first = %+v, want Alice", first[2])
	}
	if _, err := cached.Upsert(ctx, 1, domain.ContactInput{ContactUserID: 2, FirstName: "Alicia"}); err != nil {
		t.Fatalf("upsert through cache: %v", err)
	}
	second, err := cached.GetMany(ctx, 1, []int64{2})
	if err != nil {
		t.Fatalf("get second: %v", err)
	}
	if second[2].FirstName != "Alicia" {
		t.Fatalf("second = %+v, want Alicia after invalidation", second[2])
	}
	if counting.listCalls != 2 {
		t.Fatalf("ListByUser calls = %d, want 2 after write invalidation", counting.listCalls)
	}
}

func TestCachedContactStoreExternalInvalidationAndFlush(t *testing.T) {
	ctx := context.Background()
	base := memory.NewContactStore()
	if _, err := base.Upsert(ctx, 1, domain.ContactInput{ContactUserID: 2, FirstName: "Alice"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	counting := &countingContactStore{ContactStore: base}
	cached := NewCachedContactStore(counting, 0)

	if _, err := cached.GetMany(ctx, 1, []int64{2}); err != nil {
		t.Fatalf("prime get: %v", err)
	}
	if _, err := base.Upsert(ctx, 1, domain.ContactInput{ContactUserID: 2, FirstName: "Alicia"}); err != nil {
		t.Fatalf("direct upsert: %v", err)
	}
	cached.InvalidateViewers(1)
	got, err := cached.GetMany(ctx, 1, []int64{2})
	if err != nil {
		t.Fatalf("get after external invalidation: %v", err)
	}
	if got[2].FirstName != "Alicia" {
		t.Fatalf("after invalidation = %+v, want Alicia", got[2])
	}

	if _, err := base.Upsert(ctx, 1, domain.ContactInput{ContactUserID: 2, FirstName: "Ally"}); err != nil {
		t.Fatalf("direct upsert 2: %v", err)
	}
	cached.FlushReadModelCache()
	got, err = cached.GetMany(ctx, 1, []int64{2})
	if err != nil {
		t.Fatalf("get after flush: %v", err)
	}
	if got[2].FirstName != "Ally" {
		t.Fatalf("after flush = %+v, want Ally", got[2])
	}
	if counting.listCalls != 3 {
		t.Fatalf("ListByUser calls = %d, want 3 after prime+invalidate+flush", counting.listCalls)
	}
}

func TestCachedContactStoreDoesNotRefillStaleSnapshotAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	base := memory.NewContactStore()
	if _, err := base.Upsert(ctx, 1, domain.ContactInput{ContactUserID: 2, FirstName: "Alice"}); err != nil {
		t.Fatalf("seed contact: %v", err)
	}
	first, err := base.ListByUser(ctx, 1)
	if err != nil {
		t.Fatalf("snapshot first contact list: %v", err)
	}
	blocking := &blockingFirstListContactStore{
		ContactStore: base,
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		first:        first,
	}
	cached := NewCachedContactStore(blocking, 0)

	type readResult struct {
		contacts map[int64]domain.Contact
		err      error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		got, err := cached.GetMany(ctx, 1, []int64{2})
		resultCh <- readResult{contacts: got, err: err}
	}()
	waitForCacheTestSignal(t, blocking.started)

	if _, err := base.Upsert(ctx, 1, domain.ContactInput{ContactUserID: 2, FirstName: "Alicia"}); err != nil {
		t.Fatalf("update contact while first load is blocked: %v", err)
	}
	cached.InvalidateViewers(1)
	close(blocking.release)

	var result readResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for contact read")
	}
	if result.err != nil {
		t.Fatalf("contact read: %v", result.err)
	}
	if result.contacts[2].FirstName != "Alicia" {
		t.Fatalf("contact after concurrent invalidation = %+v, want Alicia", result.contacts[2])
	}

	cachedHit, err := cached.GetMany(ctx, 1, []int64{2})
	if err != nil {
		t.Fatalf("cached hit after stale load retry: %v", err)
	}
	if cachedHit[2].FirstName != "Alicia" {
		t.Fatalf("cached value after stale load retry = %+v, want Alicia", cachedHit[2])
	}
}

func TestCachedContactStoreInvalidatesPersonalPhoto(t *testing.T) {
	ctx := context.Background()
	base := memory.NewContactStore()
	if _, err := base.Upsert(ctx, 1, domain.ContactInput{ContactUserID: 2, FirstName: "Alice"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	if _, found, err := base.SetPersonalPhoto(ctx, 1, 2, 9001, 100); err != nil || !found {
		t.Fatalf("set personal photo: %v found=%v", err, found)
	}
	counting := &countingContactStore{ContactStore: base}
	cached := NewCachedContactStore(counting, 0)

	first, err := cached.PersonalPhotos(ctx, 1, []int64{2})
	if err != nil {
		t.Fatalf("personal photos first: %v", err)
	}
	if first[2].PhotoID != 9001 || !first[2].Personal {
		t.Fatalf("first personal photo = %+v, want 9001", first[2])
	}
	second, err := cached.PersonalPhotos(ctx, 1, []int64{2})
	if err != nil {
		t.Fatalf("personal photos second: %v", err)
	}
	if second[2].PhotoID != 9001 {
		t.Fatalf("second personal photo = %+v, want 9001", second[2])
	}
	if counting.listCalls != 1 {
		t.Fatalf("ListByUser calls = %d, want 1 personal-photo account snapshot load", counting.listCalls)
	}
	if counting.personalPhotoCalls != 1 {
		t.Fatalf("PersonalPhotos calls = %d, want 1", counting.personalPhotoCalls)
	}

	if _, found, err := cached.SetPersonalPhoto(ctx, 1, 2, 9002, 101); err != nil || !found {
		t.Fatalf("cached set personal photo: %v found=%v", err, found)
	}
	third, err := cached.PersonalPhotos(ctx, 1, []int64{2})
	if err != nil {
		t.Fatalf("personal photos third: %v", err)
	}
	if third[2].PhotoID != 9002 {
		t.Fatalf("third personal photo = %+v, want 9002 after invalidation", third[2])
	}
	if counting.personalPhotoCalls != 2 {
		t.Fatalf("PersonalPhotos calls after invalidation = %d, want 2", counting.personalPhotoCalls)
	}
	if counting.listCalls != 2 {
		t.Fatalf("ListByUser calls after invalidation = %d, want 2", counting.listCalls)
	}
}

func TestCachedContactStoreDoesNotRefillStalePersonalPhotoAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	base := memory.NewContactStore()
	if _, err := base.Upsert(ctx, 1, domain.ContactInput{ContactUserID: 2, FirstName: "Alice"}); err != nil {
		t.Fatalf("upsert contact: %v", err)
	}
	if _, found, err := base.SetPersonalPhoto(ctx, 1, 2, 9001, 100); err != nil || !found {
		t.Fatalf("seed personal photo: %v found=%v", err, found)
	}
	first, err := base.PersonalPhotos(ctx, 1, []int64{2})
	if err != nil {
		t.Fatalf("snapshot first personal photo: %v", err)
	}
	blocking := &blockingFirstPersonalPhotoStore{
		ContactStore: base,
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		first:        first,
	}
	cached := NewCachedContactStore(blocking, 0)

	type readResult struct {
		refs map[int64]domain.ProfilePhotoRef
		err  error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		refs, err := cached.PersonalPhotos(ctx, 1, []int64{2})
		resultCh <- readResult{refs: refs, err: err}
	}()
	waitForCacheTestSignal(t, blocking.started)

	if _, found, err := cached.SetPersonalPhoto(ctx, 1, 2, 9002, 101); err != nil || !found {
		t.Fatalf("update personal photo while first load is blocked: %v found=%v", err, found)
	}
	close(blocking.release)

	var result readResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for personal photo read")
	}
	if result.err != nil {
		t.Fatalf("personal photo read: %v", result.err)
	}
	if result.refs[2].PhotoID != 9002 {
		t.Fatalf("personal photo after concurrent invalidation = %+v, want 9002", result.refs[2])
	}
}
