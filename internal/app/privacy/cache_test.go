package privacy

import (
	"context"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type blockingFirstPrivacyStore struct {
	store.PrivacyStore
	started chan struct{}
	release chan struct{}
	first   []domain.PrivacyRules

	mu        sync.Mutex
	firstUsed bool
}

func (s *blockingFirstPrivacyStore) ListPrivacyRules(ctx context.Context, ownerUserIDs []int64, keys []domain.PrivacyKey) ([]domain.PrivacyRules, error) {
	s.mu.Lock()
	if !s.firstUsed {
		s.firstUsed = true
		s.mu.Unlock()
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		out := make([]domain.PrivacyRules, len(s.first))
		for i := range s.first {
			out[i] = cloneRules(s.first[i])
		}
		return out, nil
	}
	s.mu.Unlock()
	return s.PrivacyStore.ListPrivacyRules(ctx, ownerUserIDs, keys)
}

func waitForPrivacyCacheTestSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cache test signal")
	}
}

type countingPrivacyStore struct {
	store.PrivacyStore
	getCalls  int
	listCalls int
	setCalls  int
}

func (s *countingPrivacyStore) GetPrivacyRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (domain.PrivacyRules, bool, error) {
	s.getCalls++
	return s.PrivacyStore.GetPrivacyRules(ctx, ownerUserID, key)
}

func (s *countingPrivacyStore) SetPrivacyRules(ctx context.Context, rules domain.PrivacyRules) error {
	s.setCalls++
	return s.PrivacyStore.SetPrivacyRules(ctx, rules)
}

func (s *countingPrivacyStore) ListPrivacyRules(ctx context.Context, ownerUserIDs []int64, keys []domain.PrivacyKey) ([]domain.PrivacyRules, error) {
	s.listCalls++
	return s.PrivacyStore.ListPrivacyRules(ctx, ownerUserIDs, keys)
}

func TestCachedPrivacyStoreUsesOwnerSnapshot(t *testing.T) {
	ctx := context.Background()
	base := memory.NewPrivacyStore()
	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyPhoneNumber,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}},
	}); err != nil {
		t.Fatalf("seed privacy: %v", err)
	}
	counting := &countingPrivacyStore{PrivacyStore: base}
	cached := NewCachedPrivacyStore(counting, 0)

	first, ok, err := cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyPhoneNumber)
	if err != nil || !ok {
		t.Fatalf("first get ok=%v err=%v", ok, err)
	}
	if first.Rules[0].Kind != domain.PrivacyRuleDisallowAll {
		t.Fatalf("first rules = %+v, want disallow all", first.Rules)
	}
	second, ok, err := cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyPhoneNumber)
	if err != nil || !ok {
		t.Fatalf("second get ok=%v err=%v", ok, err)
	}
	if second.Rules[0].Kind != domain.PrivacyRuleDisallowAll {
		t.Fatalf("second rules = %+v, want disallow all", second.Rules)
	}
	if counting.getCalls != 0 {
		t.Fatalf("GetPrivacyRules calls = %d, want 0", counting.getCalls)
	}
	if counting.listCalls != 1 {
		t.Fatalf("ListPrivacyRules calls = %d, want 1 owner snapshot load", counting.listCalls)
	}
}

func TestCachedPrivacyStoreWarmsCompleteOwnerSnapshotOnSet(t *testing.T) {
	ctx := context.Background()
	base := memory.NewPrivacyStore()
	counting := &countingPrivacyStore{PrivacyStore: base}
	cached := NewCachedPrivacyStore(counting, 0)
	if err := cached.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyPhoneNumber,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}},
	}); err != nil {
		t.Fatalf("set first: %v", err)
	}
	if _, ok, err := cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyPhoneNumber); err != nil || !ok {
		t.Fatalf("first memory get ok=%v err=%v", ok, err)
	}
	if err := cached.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyProfilePhoto,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}},
	}); err != nil {
		t.Fatalf("set second: %v", err)
	}
	got, ok, err := cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyPhoneNumber)
	if err != nil || !ok {
		t.Fatalf("phone after second set ok=%v err=%v", ok, err)
	}
	if got.Rules[0].Kind != domain.PrivacyRuleDisallowAll {
		t.Fatalf("phone rules after second set = %+v, want disallow all", got.Rules)
	}
	photo, ok, err := cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyProfilePhoto)
	if err != nil || !ok {
		t.Fatalf("photo after second set ok=%v err=%v", ok, err)
	}
	if photo.Rules[0].Kind != domain.PrivacyRuleDisallowAll {
		t.Fatalf("photo rules after second set = %+v, want disallow all", photo.Rules)
	}
	if counting.setCalls != 2 {
		t.Fatalf("SetPrivacyRules calls = %d, want 2", counting.setCalls)
	}
	if counting.listCalls != 2 {
		t.Fatalf("ListPrivacyRules calls = %d, want exactly one write-path warm per set and no read-path query", counting.listCalls)
	}
}

func TestCachedPrivacyStoreExternalInvalidationAndFlush(t *testing.T) {
	ctx := context.Background()
	base := memory.NewPrivacyStore()
	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyPhoneNumber,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}},
	}); err != nil {
		t.Fatalf("seed privacy: %v", err)
	}
	counting := &countingPrivacyStore{PrivacyStore: base}
	cached := NewCachedPrivacyStore(counting, 0)

	if _, ok, err := cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyPhoneNumber); err != nil || !ok {
		t.Fatalf("prime get ok=%v err=%v", ok, err)
	}
	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyPhoneNumber,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}},
	}); err != nil {
		t.Fatalf("direct set: %v", err)
	}
	cached.InvalidateOwners(1001)
	got, ok, err := cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyPhoneNumber)
	if err != nil || !ok {
		t.Fatalf("after external invalidation ok=%v err=%v", ok, err)
	}
	if got.Rules[0].Kind != domain.PrivacyRuleAllowAll {
		t.Fatalf("after invalidation = %+v, want allow all", got.Rules)
	}

	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyPhoneNumber,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}},
	}); err != nil {
		t.Fatalf("direct set 2: %v", err)
	}
	cached.FlushReadModelCache()
	got, ok, err = cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyPhoneNumber)
	if err != nil || !ok {
		t.Fatalf("after flush ok=%v err=%v", ok, err)
	}
	if got.Rules[0].Kind != domain.PrivacyRuleDisallowAll {
		t.Fatalf("after flush = %+v, want disallow all", got.Rules)
	}
	if counting.listCalls != 3 {
		t.Fatalf("ListPrivacyRules calls = %d, want 3 after prime+invalidate+flush", counting.listCalls)
	}
}

func TestCachedPrivacyStoreDoesNotRefillStaleSnapshotAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	base := memory.NewPrivacyStore()
	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyPhoneNumber,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}},
	}); err != nil {
		t.Fatalf("seed privacy: %v", err)
	}
	first, err := base.ListPrivacyRules(ctx, []int64{1001}, allPrivacyRuleKeys)
	if err != nil {
		t.Fatalf("snapshot first privacy rules: %v", err)
	}
	blocking := &blockingFirstPrivacyStore{
		PrivacyStore: base,
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		first:        first,
	}
	cached := NewCachedPrivacyStore(blocking, 0)

	type readResult struct {
		rules domain.PrivacyRules
		ok    bool
		err   error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		rules, ok, err := cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyPhoneNumber)
		resultCh <- readResult{rules: rules, ok: ok, err: err}
	}()
	waitForPrivacyCacheTestSignal(t, blocking.started)

	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyPhoneNumber,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}},
	}); err != nil {
		t.Fatalf("update privacy while first load is blocked: %v", err)
	}
	cached.InvalidateOwners(1001)
	close(blocking.release)

	var result readResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for privacy read")
	}
	if result.err != nil || !result.ok {
		t.Fatalf("privacy read ok=%v err=%v", result.ok, result.err)
	}
	if result.rules.Rules[0].Kind != domain.PrivacyRuleAllowAll {
		t.Fatalf("privacy after concurrent invalidation = %+v, want allow all", result.rules.Rules)
	}

	cachedHit, ok, err := cached.GetPrivacyRules(ctx, 1001, domain.PrivacyKeyPhoneNumber)
	if err != nil || !ok {
		t.Fatalf("cached hit after stale load retry ok=%v err=%v", ok, err)
	}
	if cachedHit.Rules[0].Kind != domain.PrivacyRuleAllowAll {
		t.Fatalf("cached privacy after stale load retry = %+v, want allow all", cachedHit.Rules)
	}
}

func TestCachedPrivacyStoreDoesNotRefillStaleBatchAfterInvalidation(t *testing.T) {
	ctx := context.Background()
	base := memory.NewPrivacyStore()
	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyProfilePhoto,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}},
	}); err != nil {
		t.Fatalf("seed privacy: %v", err)
	}
	first, err := base.ListPrivacyRules(ctx, []int64{1001}, allPrivacyRuleKeys)
	if err != nil {
		t.Fatalf("snapshot first privacy rules: %v", err)
	}
	blocking := &blockingFirstPrivacyStore{
		PrivacyStore: base,
		started:      make(chan struct{}),
		release:      make(chan struct{}),
		first:        first,
	}
	cached := NewCachedPrivacyStore(blocking, 0)

	type readResult struct {
		rules []domain.PrivacyRules
		err   error
	}
	resultCh := make(chan readResult, 1)
	go func() {
		rules, err := cached.ListPrivacyRules(ctx, []int64{1001}, []domain.PrivacyKey{domain.PrivacyKeyProfilePhoto})
		resultCh <- readResult{rules: rules, err: err}
	}()
	waitForPrivacyCacheTestSignal(t, blocking.started)

	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyProfilePhoto,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}},
	}); err != nil {
		t.Fatalf("update privacy while first batch load is blocked: %v", err)
	}
	cached.InvalidateOwners(1001)
	close(blocking.release)

	var result readResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for privacy batch read")
	}
	if result.err != nil {
		t.Fatalf("privacy batch read: %v", result.err)
	}
	if len(result.rules) != 1 || result.rules[0].Rules[0].Kind != domain.PrivacyRuleAllowAll {
		t.Fatalf("privacy batch after concurrent invalidation = %+v, want allow all", result.rules)
	}
}

func TestCachedPrivacyStoreListUsesBatchOwnerSnapshots(t *testing.T) {
	ctx := context.Background()
	base := memory.NewPrivacyStore()
	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1001,
		Key:         domain.PrivacyKeyPhoneNumber,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleDisallowAll}},
	}); err != nil {
		t.Fatalf("seed 1001: %v", err)
	}
	if err := base.SetPrivacyRules(ctx, domain.PrivacyRules{
		OwnerUserID: 1002,
		Key:         domain.PrivacyKeyProfilePhoto,
		Rules:       []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}},
	}); err != nil {
		t.Fatalf("seed 1002: %v", err)
	}
	counting := &countingPrivacyStore{PrivacyStore: base}
	cached := NewCachedPrivacyStore(counting, 0)

	keys := []domain.PrivacyKey{domain.PrivacyKeyPhoneNumber, domain.PrivacyKeyProfilePhoto}
	first, err := cached.ListPrivacyRules(ctx, []int64{1001, 1002}, keys)
	if err != nil {
		t.Fatalf("list first: %v", err)
	}
	second, err := cached.ListPrivacyRules(ctx, []int64{1001, 1002}, keys)
	if err != nil {
		t.Fatalf("list second: %v", err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("list sizes = %d/%d, want 2/2", len(first), len(second))
	}
	if counting.listCalls != 1 {
		t.Fatalf("ListPrivacyRules calls = %d, want 1 batch load", counting.listCalls)
	}
}
