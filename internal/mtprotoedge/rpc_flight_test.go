package mtprotoedge

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

func rpcFlightTestAuthID(seed byte) [8]byte {
	return [8]byte{seed, seed + 1, seed + 2, seed + 3}
}

func rpcFlightExactIdentity(t *testing.T, profile tlprofile.Profile, request bin.Object) tlprofile.PreparedIdentity {
	t.Helper()
	var body bin.Buffer
	if err := tlprofile.EncodeObject(profile, request, &body); err != nil {
		t.Fatalf("encode exact request: %v", err)
	}
	admitted, err := tlprofile.NewDispatcher().Admit(profile, &body, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("admit exact request: %v", err)
	}
	if body.Len() != 0 {
		t.Fatalf("exact admission left %d bytes", body.Len())
	}
	return admitted.Prepared().Identity()
}

func newRPCResultSubscriberTestCache(global, auth, session, perFlight int) *rpcResultCache {
	return newRPCResultCacheWithFairCapacity(time.Now, rpcResultCacheCapacity{
		maxPending:             64,
		maxPendingPerAuth:      64,
		globalMaxBytes:         rpcResultCacheMaxBytes,
		globalMaxEntries:       rpcResultCacheMaxEntries,
		authMaxBytes:           rpcResultCacheAuthMaxBytes,
		authMaxEntries:         rpcResultCacheAuthMaxEntries,
		sessionMaxBytes:        rpcResultCacheSessionMaxBytes,
		sessionMaxEntries:      rpcResultCacheSessionMaxEntries,
		subscriberMaxGlobal:    global,
		subscriberMaxAuth:      auth,
		subscriberMaxSession:   session,
		subscriberMaxPerFlight: perFlight,
	})
}

func TestRPCResultFlightSubscriberPairCapacityFailureIsAtomic(t *testing.T) {
	cache := newRPCResultSubscriberTestCache(8, 8, 8, 1)
	authKeyID := rpcFlightTestAuthID(70)
	claim, err := cache.Acquire(authKeyID, 70, 700)
	if err != nil || claim.owner == nil {
		t.Fatalf("Acquire owner: claim=%#v err=%v", claim, err)
	}
	var resultCalls, executionCalls int
	err = claim.owner.Waiter().SubscribeResultAndExecution(
		func(*encodedOutboundMessage, bool) { resultCalls++ },
		func(bool) { executionCalls++ },
	)
	if !errors.Is(err, ErrRPCResultSubscriberCapacity) {
		t.Fatalf("pair subscription err=%v, want %v", err, ErrRPCResultSubscriberCapacity)
	}
	s := cache.shard(rpcResultCacheKey{authKeyID: authKeyID, sessionID: 70, reqMsgID: 700})
	s.mu.Lock()
	if got := claim.owner.flight.subscriberSlots; got != 0 {
		t.Fatalf("failed pair retained %d subscriber slots", got)
	}
	if got := len(claim.owner.flight.subscribers) + len(claim.owner.flight.executionSubscribers); got != 0 {
		t.Fatalf("failed pair retained %d callbacks", got)
	}
	s.mu.Unlock()
	claim.owner.CompleteExecution(true)
	cache.Put(authKeyID, 70, 700, &encodedOutboundMessage{body: []byte{1}, reqMsgID: 700})
	if resultCalls != 0 || executionCalls != 0 {
		t.Fatalf("failed pair callbacks ran: result=%d execution=%d", resultCalls, executionCalls)
	}
	if got := cache.subscriberBudget.global.snapshot(); got != 0 {
		t.Fatalf("subscriber global usage=%d after failed pair", got)
	}
}

func TestRPCResultFlightSubscriberBudgetsIsolateSessionAndAuth(t *testing.T) {
	cache := newRPCResultSubscriberTestCache(3, 2, 1, 4)
	authA := rpcFlightTestAuthID(71)
	authB := rpcFlightTestAuthID(72)
	type ownerKey struct {
		auth    [8]byte
		session int64
		msgID   int64
		owner   *rpcResultOwnerLease
	}
	acquire := func(auth [8]byte, session, msgID int64) ownerKey {
		t.Helper()
		claim, err := cache.Acquire(auth, session, msgID)
		if err != nil || claim.owner == nil {
			t.Fatalf("Acquire(%d,%d): claim=%#v err=%v", session, msgID, claim, err)
		}
		return ownerKey{auth: auth, session: session, msgID: msgID, owner: claim.owner}
	}
	subscribe := func(owner ownerKey) error {
		return owner.owner.Waiter().Subscribe(func(*encodedOutboundMessage, bool) {})
	}

	a1 := acquire(authA, 1, 101)
	a2 := acquire(authA, 2, 102)
	a3 := acquire(authA, 3, 103)
	b1 := acquire(authB, 4, 104)
	b2 := acquire(authB, 5, 105)
	if err := subscribe(a1); err != nil {
		t.Fatalf("first session subscriber: %v", err)
	}
	if err := subscribe(a1); !errors.Is(err, ErrRPCResultSubscriberCapacity) {
		t.Fatalf("same session overflow err=%v", err)
	}
	if err := subscribe(a2); err != nil {
		t.Fatalf("second session subscriber: %v", err)
	}
	if err := subscribe(a3); !errors.Is(err, ErrRPCResultSubscriberCapacity) {
		t.Fatalf("same auth overflow err=%v", err)
	}
	if err := subscribe(b1); err != nil {
		t.Fatalf("other auth subscriber: %v", err)
	}
	if err := subscribe(b2); !errors.Is(err, ErrRPCResultSubscriberCapacity) {
		t.Fatalf("global overflow err=%v", err)
	}
	if got := cache.subscriberBudget.authSnapshot(authA); got != 2 {
		t.Fatalf("auth A usage=%d, want 2", got)
	}
	if got := cache.subscriberBudget.authSnapshot(authB); got != 1 {
		t.Fatalf("auth B usage=%d, want 1", got)
	}
	if got := cache.subscriberBudget.global.snapshot(); got != 3 {
		t.Fatalf("global usage=%d, want 3", got)
	}
	for _, owner := range []ownerKey{a1, a2, a3, b1, b2} {
		owner.owner.Abort()
	}
	if got := cache.subscriberBudget.global.snapshot(); got != 0 {
		t.Fatalf("global usage=%d after aborts", got)
	}
	if got := cache.subscriberBudget.authSnapshot(authA); got != 0 {
		t.Fatalf("auth A usage=%d after aborts", got)
	}
}

func TestRPCResultFlightSubscriberSlotsReleasePerTerminalHalf(t *testing.T) {
	cache := newRPCResultSubscriberTestCache(4, 4, 4, 4)
	authKeyID := rpcFlightTestAuthID(73)
	claim, err := cache.Acquire(authKeyID, 73, 730)
	if err != nil || claim.owner == nil {
		t.Fatalf("Acquire owner: claim=%#v err=%v", claim, err)
	}
	result := make(chan bool, 1)
	execution := make(chan bool, 1)
	if err := claim.owner.Waiter().SubscribeResultAndExecution(
		func(_ *encodedOutboundMessage, ok bool) { result <- ok },
		func(ok bool) { execution <- ok },
	); err != nil {
		t.Fatalf("pair subscription: %v", err)
	}
	if got := cache.subscriberBudget.sessionSnapshot(authKeyID, 73); got != 2 {
		t.Fatalf("initial subscriber usage=%d, want 2", got)
	}
	if !claim.owner.CompleteExecution(true) {
		t.Fatal("CompleteExecution lost")
	}
	if ok := <-execution; !ok {
		t.Fatal("execution callback reported failure")
	}
	if got := cache.subscriberBudget.sessionSnapshot(authKeyID, 73); got != 1 {
		t.Fatalf("post-execution subscriber usage=%d, want 1", got)
	}
	cache.Put(authKeyID, 73, 730, &encodedOutboundMessage{body: []byte{1}, reqMsgID: 730})
	if ok := <-result; !ok {
		t.Fatal("result callback reported failure")
	}
	if got := cache.subscriberBudget.sessionSnapshot(authKeyID, 73); got != 0 {
		t.Fatalf("post-result subscriber usage=%d, want 0", got)
	}
}

func TestRPCResultFlightRepeatedReplayJoinsStayBoundedAndPutCleansExecution(t *testing.T) {
	cache := newRPCResultSubscriberTestCache(2, 2, 2, 2)
	authKeyID := rpcFlightTestAuthID(74)
	claim, err := cache.Acquire(authKeyID, 74, 740)
	if err != nil || claim.owner == nil {
		t.Fatalf("Acquire owner: claim=%#v err=%v", claim, err)
	}
	var resultCalls, executionCalls int
	if err := claim.owner.Waiter().SubscribeResultAndExecution(
		func(*encodedOutboundMessage, bool) { resultCalls++ },
		func(success bool) {
			if success {
				t.Error("Put without execution proof reported dependency success")
			}
			executionCalls++
		},
	); err != nil {
		t.Fatalf("first pair: %v", err)
	}
	for i := 0; i < 100; i++ {
		err := claim.owner.Waiter().SubscribeResultAndExecution(
			func(*encodedOutboundMessage, bool) { resultCalls++ },
			func(bool) { executionCalls++ },
		)
		if !errors.Is(err, ErrRPCResultSubscriberCapacity) {
			t.Fatalf("join %d err=%v, want capacity", i, err)
		}
	}
	cache.Put(authKeyID, 74, 740, &encodedOutboundMessage{body: []byte{1}, reqMsgID: 740})
	if resultCalls != 1 || executionCalls != 1 {
		t.Fatalf("terminal callback counts result=%d execution=%d", resultCalls, executionCalls)
	}
	if got := cache.subscriberBudget.global.snapshot(); got != 0 {
		t.Fatalf("global usage=%d after Put", got)
	}
}

func TestRPCResultFlightExactIdentityGuardsPendingAndCompletedReuse(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 2)
	authKeyID := rpcFlightTestAuthID(90)
	firstIdentity := rpcFlightExactIdentity(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	otherIdentity := rpcFlightExactIdentity(t, tlprofile.Profile225, &tg.HelpGetNearestDCRequest{})

	owner, err := cache.AcquireIdentified(authKeyID, 90, 900, firstIdentity)
	if err != nil || owner.state != rpcResultAcquireOwner || owner.owner == nil {
		t.Fatalf("exact owner Acquire = state:%d err:%v", owner.state, err)
	}
	if _, err := cache.AcquireIdentified(authKeyID, 90, 900, otherIdentity); !errors.Is(err, ErrRPCResultIdentityMismatch) {
		t.Fatalf("pending mismatched Acquire err = %v, want %v", err, ErrRPCResultIdentityMismatch)
	}
	same, err := cache.AcquireIdentified(authKeyID, 90, 900, firstIdentity)
	if err != nil || same.state != rpcResultAcquirePending || same.waiter == nil {
		t.Fatalf("pending matched Acquire = state:%d err:%v", same.state, err)
	}

	want := &encodedOutboundMessage{body: []byte{1, 2, 3, 4}, reqMsgID: 900}
	cache.Put(authKeyID, 90, 900, want)
	if _, err := cache.AcquireIdentified(authKeyID, 90, 900, otherIdentity); !errors.Is(err, ErrRPCResultIdentityMismatch) {
		t.Fatalf("completed mismatched Acquire err = %v, want %v", err, ErrRPCResultIdentityMismatch)
	}
	completed, err := cache.AcquireIdentified(authKeyID, 90, 900, firstIdentity)
	if err != nil || completed.state != rpcResultAcquireCompleted || completed.encoded != want {
		t.Fatalf("completed matched Acquire = state:%d encoded:%p err:%v", completed.state, completed.encoded, err)
	}
	if encoded, ok, waitErr := same.waiter.Wait(context.Background()); waitErr != nil || !ok || encoded != want {
		t.Fatalf("matched waiter = encoded:%p ok:%v err:%v", encoded, ok, waitErr)
	}
}

func TestRPCResultFlightAdmissionSequenceAllocatedOnceAndReplayed(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 4)
	authKeyID := rpcFlightTestAuthID(89)
	identity := rpcFlightExactIdentity(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	owner, err := cache.AcquireLayerIdentified(authKeyID, 89, 890, tlprofile.Profile225, identity)
	if err != nil || owner.state != rpcResultAcquireOwner || owner.owner == nil || owner.admissionSeq == 0 {
		t.Fatalf("owner = state:%d seq:%d err:%v", owner.state, owner.admissionSeq, err)
	}
	pending, err := cache.AcquireLayerIdentified(authKeyID, 89, 890, tlprofile.Profile225, identity)
	if err != nil || pending.state != rpcResultAcquirePending || pending.admissionSeq != owner.admissionSeq {
		t.Fatalf("pending = state:%d seq:%d err:%v, want seq:%d", pending.state, pending.admissionSeq, err, owner.admissionSeq)
	}
	owner.owner.CompleteExecution(true)
	encoded := &encodedOutboundMessage{body: []byte{1}, reqMsgID: 890}
	cache.Put(authKeyID, 89, 890, encoded)
	completed, err := cache.AcquireLayerIdentified(authKeyID, 89, 890, tlprofile.Profile225, identity)
	if err != nil || completed.state != rpcResultAcquireCompleted || completed.admissionSeq != owner.admissionSeq {
		t.Fatalf("completed = state:%d seq:%d err:%v, want seq:%d", completed.state, completed.admissionSeq, err, owner.admissionSeq)
	}
	second, err := cache.AcquireLayerIdentified(authKeyID, 89, 894, tlprofile.Profile225, identity)
	if err != nil || second.admissionSeq <= owner.admissionSeq {
		t.Fatalf("second owner seq=%d err=%v, want > %d", second.admissionSeq, err, owner.admissionSeq)
	}
	second.owner.Abort()
	legacy, err := cache.Acquire(authKeyID, 89, 898)
	if err != nil || legacy.admissionSeq != 0 {
		t.Fatalf("legacy admission seq=%d err=%v, want 0", legacy.admissionSeq, err)
	}
	legacy.owner.Abort()
}

func TestRPCAdmissionSafeFloorTracksOwnersUntilPutOrAbort(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 4)
	authKeyID := rpcFlightTestAuthID(86)
	identity := rpcFlightExactIdentity(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	first, err := cache.AcquireLayerIdentified(authKeyID, 86, 860, tlprofile.Profile225, identity)
	if err != nil || first.owner == nil {
		t.Fatalf("first owner err=%v", err)
	}
	second, err := cache.AcquireLayerIdentified(authKeyID, 86, 864, tlprofile.Profile225, identity)
	if err != nil || second.owner == nil {
		t.Fatalf("second owner err=%v", err)
	}
	if floor := cache.stableAdmissionSafeFloor(); floor != first.admissionSeq {
		t.Fatalf("two-owner safe floor=%d, want %d", floor, first.admissionSeq)
	}
	if !first.owner.Abort() {
		t.Fatal("first owner abort failed")
	}
	if floor := cache.stableAdmissionSafeFloor(); floor != second.admissionSeq {
		t.Fatalf("post-abort safe floor=%d, want %d", floor, second.admissionSeq)
	}
	second.owner.CompleteExecution(true)
	cache.Put(authKeyID, 86, 864, &encodedOutboundMessage{body: []byte{1}, reqMsgID: 864})
	if floor := cache.stableAdmissionSafeFloor(); floor != second.admissionSeq+1 {
		t.Fatalf("terminal safe floor=%d, want %d", floor, second.admissionSeq+1)
	}
}

func TestRPCAdmissionSequenceExhaustionCannotWrap(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 2)
	cache.nextAdmissionSeq.Store(^uint64(0) - 1)
	authKeyID := rpcFlightTestAuthID(85)
	identity := rpcFlightExactIdentity(t, tlprofile.Profile225, &tg.HelpGetConfigRequest{})
	last, err := cache.AcquireLayerIdentified(authKeyID, 85, 850, tlprofile.Profile225, identity)
	if err != nil || last.admissionSeq != ^uint64(0) || last.owner == nil {
		t.Fatalf("last sequence=%d owner:%v err=%v", last.admissionSeq, last.owner != nil, err)
	}
	if _, err := cache.AcquireLayerIdentified(authKeyID, 85, 854, tlprofile.Profile225, identity); !errors.Is(err, ErrRPCAdmissionSeqExhausted) {
		t.Fatalf("post-max allocation err=%v, want %v", err, ErrRPCAdmissionSeqExhausted)
	}
	if got := cache.nextAdmissionSeq.Load(); got != ^uint64(0) {
		t.Fatalf("exhausted sequence wrapped to %d", got)
	}
	last.owner.Abort()
}

func TestRPCIdentityMismatchCarriesWinnerProfileAcrossAbort(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 2)
	authKeyID := rpcFlightTestAuthID(88)
	request := &tg.MessagesGetHistoryRequest{Peer: &tg.InputPeerSelf{}, Limit: 1}
	winnerIdentity := rpcFlightExactIdentity(t, tlprofile.Profile225, request)
	loserIdentity := rpcFlightExactIdentity(t, tlprofile.Profile227, request)
	winner, err := cache.AcquireLayerIdentified(authKeyID, 88, 880, tlprofile.Profile225, winnerIdentity)
	if err != nil || winner.owner == nil {
		t.Fatalf("winner owner err=%v", err)
	}
	_, err = cache.AcquireLayerIdentified(authKeyID, 88, 880, tlprofile.Profile227, loserIdentity)
	var mismatch *rpcResultIdentityMismatchError
	if !errors.As(err, &mismatch) || !mismatch.hasProfile || mismatch.profile != tlprofile.Profile225 {
		t.Fatalf("mismatch = %#v err=%v", mismatch, err)
	}
	if !winner.owner.Abort() {
		t.Fatal("winner abort failed")
	}
	replacement, err := cache.AcquireLayerIdentified(authKeyID, 88, 880, mismatch.profile, winnerIdentity)
	if err != nil || replacement.state != rpcResultAcquireOwner || replacement.owner == nil {
		t.Fatalf("replacement under retained winner profile = state:%d err:%v", replacement.state, err)
	}
	replacement.owner.Abort()
}

func TestRPCAdmissionProfileHintSurvivesCompletedEvictionWindow(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	cache := newRPCResultCacheWithFlightLimit(func() time.Time { return now }, 2)
	authKeyID := rpcFlightTestAuthID(87)
	identity := rpcFlightExactIdentity(t, tlprofile.Profile225, &tg.MessagesGetHistoryRequest{
		Peer: &tg.InputPeerSelf{}, Limit: 1,
	})
	claim, err := cache.AcquireLayerIdentified(authKeyID, 87, 870, tlprofile.Profile225, identity)
	if err != nil || claim.owner == nil {
		t.Fatalf("owner err=%v", err)
	}
	claim.owner.CompleteExecution(true)
	cache.Put(authKeyID, 87, 870, &encodedOutboundMessage{body: []byte{1}, reqMsgID: 870})
	profile, ok := cache.ExactAdmissionProfile(authKeyID, 87, 870)
	if !ok || profile != tlprofile.Profile225 {
		t.Fatalf("profile hint = (%d,%v)", profile, ok)
	}
	// Admission already copied the hint into its local decoder cursor. Expiry
	// between that probe and the atomic claim must not make it fall back to the
	// connection's newer default; it simply becomes a fresh owner under 225.
	now = now.Add(rpcResultCacheTTL + time.Second)
	replacement, err := cache.AcquireLayerIdentified(authKeyID, 87, 870, profile, identity)
	if err != nil || replacement.state != rpcResultAcquireOwner || replacement.owner == nil {
		t.Fatalf("post-eviction owner = state:%d err:%v", replacement.state, err)
	}
	replacement.owner.Abort()
}

func TestRPCInvariantIdentityDoesNotExposeCanonicalProfileHint(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 2)
	authKeyID := rpcFlightTestAuthID(84)
	identity := rpcFlightExactIdentity(t, tlprofile.Profile227, &tg.AuthBindTempAuthKeyRequest{
		PermAuthKeyID: 1, Nonce: 2, ExpiresAt: 3, EncryptedMessage: []byte("bind"),
	})
	claim, err := cache.AcquireLayerIdentified(authKeyID, 84, 840, 0, identity)
	if err != nil || claim.owner == nil {
		t.Fatalf("invariant owner err=%v", err)
	}
	if profile, ok := cache.ExactAdmissionProfile(authKeyID, 84, 840); ok || profile != 0 {
		t.Fatalf("pending invariant profile hint=(%d,%v), want absent", profile, ok)
	}
	claim.owner.CompleteExecution(true)
	cache.Put(authKeyID, 84, 840, &encodedOutboundMessage{body: []byte{1}, reqMsgID: 840})
	if profile, ok := cache.ExactAdmissionProfile(authKeyID, 84, 840); ok || profile != 0 {
		t.Fatalf("completed invariant profile hint=(%d,%v), want absent", profile, ok)
	}
}

func TestRPCResultExecutionCompletionIsExactlyOnceAndDurable(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 2)
	authKeyID := rpcFlightTestAuthID(91)
	claim, err := cache.Acquire(authKeyID, 91, 910)
	if err != nil || claim.state != rpcResultAcquireOwner || claim.owner == nil {
		t.Fatalf("owner Acquire = state:%d err:%v", claim.state, err)
	}
	waiter := claim.owner.Waiter()
	results := make(chan bool, 2)
	if err := waiter.SubscribeExecution(func(success bool) { results <- success }); err != nil {
		t.Fatal(err)
	}
	if !claim.owner.CompleteExecution(true) {
		t.Fatal("first execution completion lost")
	}
	if claim.owner.CompleteExecution(false) {
		t.Fatal("contradictory second execution completion won")
	}
	select {
	case success := <-results:
		if !success {
			t.Fatal("execution callback reported failure")
		}
	case <-time.After(time.Second):
		t.Fatal("execution callback did not run")
	}
	select {
	case success := <-results:
		t.Fatalf("execution callback ran twice: %v", success)
	default:
	}

	want := &encodedOutboundMessage{body: []byte{9, 1, 0, 0}, reqMsgID: 910}
	cache.Put(authKeyID, 91, 910, want)
	dependency, ok := cache.ObserveDependency(authKeyID, 91, 910)
	if !ok || !dependency.completed || !dependency.success || dependency.waiter != nil {
		t.Fatalf("completed dependency = %#v ok:%v", dependency, ok)
	}
	late := make(chan bool, 1)
	if err := waiter.SubscribeExecution(func(success bool) { late <- success }); err != nil {
		t.Fatal(err)
	}
	if success := <-late; !success {
		t.Fatal("late execution subscriber lost durable success")
	}
}

func TestRPCResultExecutionAbortPublishesFailure(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 1)
	authKeyID := rpcFlightTestAuthID(92)
	claim, err := cache.Acquire(authKeyID, 92, 920)
	if err != nil || claim.owner == nil {
		t.Fatalf("owner Acquire err = %v", err)
	}
	result := make(chan bool, 1)
	if err := claim.owner.Waiter().SubscribeExecution(func(success bool) { result <- success }); err != nil {
		t.Fatal(err)
	}
	if !claim.owner.Abort() {
		t.Fatal("Abort lost")
	}
	if success := <-result; success {
		t.Fatal("aborted execution reported success")
	}
	if _, ok := cache.ObserveDependency(authKeyID, 92, 920); ok {
		t.Fatal("aborted flight remained observable as a completed dependency")
	}
}

func TestRPCResultFlightConcurrentAcquireHasUniqueOwner(t *testing.T) {
	const callers = 64
	cache := newRPCResultCacheWithFlightLimit(time.Now, callers)
	authKeyID := rpcFlightTestAuthID(1)
	start := make(chan struct{})
	results := make(chan rpcResultAcquire, callers)
	errs := make(chan error, callers)

	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			<-start
			claim, err := cache.Acquire(authKeyID, 10, 100)
			if err != nil {
				errs <- err
				return
			}
			results <- claim
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("Acquire: %v", err)
	}
	owners := 0
	waiters := 0
	var owner *rpcResultOwnerLease
	for claim := range results {
		switch claim.state {
		case rpcResultAcquireOwner:
			owners++
			owner = claim.owner
		case rpcResultAcquirePending:
			waiters++
			if claim.waiter == nil {
				t.Fatal("pending claim has nil waiter")
			}
		default:
			t.Fatalf("unexpected claim state %d", claim.state)
		}
	}
	if owners != 1 || waiters != callers-1 {
		t.Fatalf("claims = owners:%d waiters:%d, want 1/%d", owners, waiters, callers-1)
	}
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("pending count = %d, want 1", got)
	}
	if owner == nil || !owner.Abort() {
		t.Fatal("unique owner did not abort its claim")
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("pending count after abort = %d, want 0", got)
	}
}

func TestRPCResultFlightPutPublishesAndWakesAllWaiters(t *testing.T) {
	const waiters = 24
	cache := newRPCResultCacheWithFlightLimit(time.Now, 32)
	authKeyID := rpcFlightTestAuthID(10)
	owner, err := cache.Acquire(authKeyID, 20, 200)
	if err != nil || owner.state != rpcResultAcquireOwner || owner.owner == nil {
		t.Fatalf("owner Acquire = state:%d err:%v", owner.state, err)
	}

	waiterClaims := make([]*rpcResultWaiter, 0, waiters)
	for i := 0; i < waiters; i++ {
		claim, acquireErr := cache.Acquire(authKeyID, 20, 200)
		if acquireErr != nil || claim.state != rpcResultAcquirePending || claim.waiter == nil {
			t.Fatalf("waiter %d Acquire = state:%d err:%v", i, claim.state, acquireErr)
		}
		waiterClaims = append(waiterClaims, claim.waiter)
	}

	type waiterResult struct {
		encoded *encodedOutboundMessage
		cached  *encodedOutboundMessage
		ok      bool
		err     error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan waiterResult, waiters)
	for _, waiter := range waiterClaims {
		go func(w *rpcResultWaiter) {
			encoded, ok, waitErr := w.Wait(ctx)
			cached, _ := cache.Get(authKeyID, 20, 200)
			results <- waiterResult{encoded: encoded, cached: cached, ok: ok, err: waitErr}
		}(waiter)
	}

	want := &encodedOutboundMessage{body: []byte{1, 2, 3, 4}, typeID: 42, reqMsgID: 200}
	cache.Put(authKeyID, 20, 200, want)
	for i := 0; i < waiters; i++ {
		got := <-results
		if got.err != nil || !got.ok {
			t.Fatalf("waiter %d result = ok:%v err:%v", i, got.ok, got.err)
		}
		if got.encoded != want || got.cached != want {
			t.Fatalf("waiter %d observed direct/cache pointers %p/%p, want %p", i, got.encoded, got.cached, want)
		}
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("pending count after Put = %d, want 0", got)
	}
	completed, err := cache.Acquire(authKeyID, 20, 200)
	if err != nil || completed.state != rpcResultAcquireCompleted || completed.encoded != want {
		t.Fatalf("completed Acquire = state:%d encoded:%p err:%v", completed.state, completed.encoded, err)
	}
	if owner.owner.Abort() {
		t.Fatal("completed owner's stale lease aborted a resolved claim")
	}
}

func TestRPCResultFlightAbortWakesAndAllowsReclaim(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 2)
	authKeyID := rpcFlightTestAuthID(20)
	first, err := cache.Acquire(authKeyID, 30, 300)
	if err != nil || first.state != rpcResultAcquireOwner {
		t.Fatalf("first Acquire = state:%d err:%v", first.state, err)
	}
	waiting, err := cache.Acquire(authKeyID, 30, 300)
	if err != nil || waiting.state != rpcResultAcquirePending {
		t.Fatalf("waiting Acquire = state:%d err:%v", waiting.state, err)
	}
	if !first.owner.Abort() {
		t.Fatal("first Abort lost")
	}
	if encoded, ok, waitErr := waiting.waiter.Wait(context.Background()); waitErr != nil || ok || encoded != nil {
		t.Fatalf("aborted Wait = encoded:%p ok:%v err:%v", encoded, ok, waitErr)
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("pending count after Abort = %d, want 0", got)
	}

	second, err := cache.Acquire(authKeyID, 30, 300)
	if err != nil || second.state != rpcResultAcquireOwner || second.owner == nil {
		t.Fatalf("reclaim = state:%d err:%v", second.state, err)
	}
	if first.owner.Abort() {
		t.Fatal("stale first lease aborted the replacement owner")
	}
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("pending count after reclaim = %d, want 1", got)
	}
	if !second.owner.Abort() || cache.flightLimit.snapshot() != 0 {
		t.Fatal("replacement owner did not release its claim")
	}
}

func TestRPCResultFlightCompletedCachePressureDoesNotEvictPending(t *testing.T) {
	now := time.Unix(1_000, 0)
	cache := newRPCResultCacheWithFlightLimit(func() time.Time { return now }, 4)
	authKeyID := rpcFlightTestAuthID(30)
	pending, err := cache.Acquire(authKeyID, 40, 400)
	if err != nil || pending.state != rpcResultAcquireOwner {
		t.Fatalf("pending Acquire = state:%d err:%v", pending.state, err)
	}

	key := rpcResultCacheKey{authKeyID: authKeyID, sessionID: 40, reqMsgID: 400}
	shard := cache.shard(key)
	shard.mu.Lock()
	shard.maxEntries = 2
	shard.mu.Unlock()
	for i := int64(0); i < 16; i++ {
		cache.Put(authKeyID, 40, 500+i, &encodedOutboundMessage{body: []byte{byte(i)}})
	}
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("completed trim changed pending count to %d", got)
	}
	joined, err := cache.Acquire(authKeyID, 40, 400)
	if err != nil || joined.state != rpcResultAcquirePending {
		t.Fatalf("Acquire after completed trim = state:%d err:%v", joined.state, err)
	}

	// Expire the independent completed cache and prove the pending owner remains.
	now = now.Add(rpcResultCacheTTL + time.Second)
	_, _ = cache.Get(authKeyID, 40, 515)
	joinedAfterTTL, err := cache.Acquire(authKeyID, 40, 400)
	if err != nil || joinedAfterTTL.state != rpcResultAcquirePending {
		t.Fatalf("Acquire after completed TTL = state:%d err:%v", joinedAfterTTL.state, err)
	}
	if !pending.owner.Abort() || cache.flightLimit.snapshot() != 0 {
		t.Fatal("pending claim did not survive pressure through explicit Abort")
	}
}

func TestRPCResultFlightCapacityAndCountReturn(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 2)
	authKeyID := rpcFlightTestAuthID(40)
	first, err := cache.Acquire(authKeyID, 50, 501)
	if err != nil || first.state != rpcResultAcquireOwner {
		t.Fatalf("first Acquire = state:%d err:%v", first.state, err)
	}
	second, err := cache.Acquire(authKeyID, 50, 502)
	if err != nil || second.state != rpcResultAcquireOwner {
		t.Fatalf("second Acquire = state:%d err:%v", second.state, err)
	}
	joined, err := cache.Acquire(authKeyID, 50, 501)
	if err != nil || joined.state != rpcResultAcquirePending {
		t.Fatalf("join at capacity = state:%d err:%v", joined.state, err)
	}
	if _, err := cache.Acquire(authKeyID, 50, 503); !errors.Is(err, ErrRPCResultFlightCapacity) {
		t.Fatalf("over-capacity Acquire err = %v, want %v", err, ErrRPCResultFlightCapacity)
	}
	if got := cache.flightLimit.snapshot(); got != 2 {
		t.Fatalf("pending count at capacity = %d, want 2", got)
	}

	want := &encodedOutboundMessage{body: []byte{9}, reqMsgID: 501}
	cache.Put(authKeyID, 50, 501, want)
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("pending count after Put = %d, want 1", got)
	}
	third, err := cache.Acquire(authKeyID, 50, 503)
	if err != nil || third.state != rpcResultAcquireOwner {
		t.Fatalf("Acquire after returned slot = state:%d err:%v", third.state, err)
	}
	completed, err := cache.Acquire(authKeyID, 50, 501)
	if err != nil || completed.state != rpcResultAcquireCompleted || completed.encoded != want {
		t.Fatalf("completed Acquire at capacity = state:%d err:%v", completed.state, err)
	}
	if encoded, ok, waitErr := joined.waiter.Wait(context.Background()); waitErr != nil || !ok || encoded != want {
		t.Fatalf("joined Wait = encoded:%p ok:%v err:%v", encoded, ok, waitErr)
	}
	if !second.owner.Abort() || !third.owner.Abort() {
		t.Fatal("owners failed to return remaining capacity")
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("final pending count = %d, want 0", got)
	}
}

func TestRPCResultFlightLargePutPublishesCompletedBeforeResolvingWaiters(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 1)
	authKeyID := rpcFlightTestAuthID(50)
	owner, err := cache.Acquire(authKeyID, 60, 600)
	if err != nil || owner.state != rpcResultAcquireOwner {
		t.Fatalf("owner Acquire = state:%d err:%v", owner.state, err)
	}
	joined, err := cache.Acquire(authKeyID, 60, 600)
	if err != nil || joined.state != rpcResultAcquirePending {
		t.Fatalf("joined Acquire = state:%d err:%v", joined.state, err)
	}

	// This is larger than the removed 4 MiB per-shard partition but remains a
	// legal outbound result and fits the global/auth/session fair byte budgets.
	largeSize := rpcResultCacheMaxBytes/rpcResultCacheShards + 1
	want := &encodedOutboundMessage{body: make([]byte, largeSize), reqMsgID: 600}
	cache.Put(authKeyID, 60, 600, want)
	if encoded, ok, waitErr := joined.waiter.Wait(context.Background()); waitErr != nil || !ok || encoded != want {
		t.Fatalf("large Wait = encoded:%p ok:%v err:%v", encoded, ok, waitErr)
	}
	if got, ok := cache.Get(authKeyID, 60, 600); !ok || got != want {
		t.Fatalf("large completed Get = encoded:%p ok:%v", got, ok)
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("large Put leaked pending count %d", got)
	}
	if owner.owner.Abort() {
		t.Fatal("large Put left its old owner abortable")
	}
	completed, err := cache.Acquire(authKeyID, 60, 600)
	if err != nil || completed.state != rpcResultAcquireCompleted || completed.encoded != want {
		t.Fatalf("Acquire after large completion = state:%d encoded:%p err:%v", completed.state, completed.encoded, err)
	}
	if completed.owner != nil {
		t.Fatal("large completed result incorrectly returned a new owner")
	}
	if got := cache.completedBytes.snapshot(); got != int64(largeSize) {
		t.Fatalf("completed byte budget = %d, want %d", got, largeSize)
	}
}

func TestRPCResultFlightWaitContextDoesNotReleaseOwner(t *testing.T) {
	cache := newRPCResultCacheWithFlightLimit(time.Now, 1)
	authKeyID := rpcFlightTestAuthID(60)
	owner, err := cache.Acquire(authKeyID, 70, 700)
	if err != nil {
		t.Fatalf("owner Acquire: %v", err)
	}
	joined, err := cache.Acquire(authKeyID, 70, 700)
	if err != nil {
		t.Fatalf("joined Acquire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if encoded, ok, waitErr := joined.waiter.Wait(ctx); !errors.Is(waitErr, context.Canceled) || ok || encoded != nil {
		t.Fatalf("canceled Wait = encoded:%p ok:%v err:%v", encoded, ok, waitErr)
	}
	if got := cache.flightLimit.snapshot(); got != 1 {
		t.Fatalf("waiter cancellation released owner count to %d", got)
	}
	if !owner.owner.Abort() || cache.flightLimit.snapshot() != 0 {
		t.Fatal("owner did not retain and release claim after waiter cancellation")
	}
}

func TestRPCResultFlightConcurrentCapacityReturnsAllSlots(t *testing.T) {
	const (
		limit   = 32
		callers = 512
	)
	cache := newRPCResultCacheWithFlightLimit(time.Now, limit)
	authKeyID := rpcFlightTestAuthID(70)
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		i := i
		go func() {
			defer wg.Done()
			<-start
			claim, err := cache.Acquire(authKeyID, 80+int64(i%4), 1_000+int64(i))
			if errors.Is(err, ErrRPCResultFlightCapacity) {
				return
			}
			if err != nil {
				errs <- err
				return
			}
			if claim.state != rpcResultAcquireOwner || claim.owner == nil {
				errs <- errors.New("unique-key claim did not become owner")
				return
			}
			if i%2 == 0 {
				cache.Put(authKeyID, 80+int64(i%4), 1_000+int64(i), &encodedOutboundMessage{body: []byte{1}})
			} else if !claim.owner.Abort() {
				errs <- errors.New("owner Abort lost")
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent claim: %v", err)
	}
	if got := cache.flightLimit.snapshot(); got != 0 {
		t.Fatalf("concurrent completion leaked %d pending slots", got)
	}
}

func TestQueuedRPCConnectionCloseAbortsOwnerClaim(t *testing.T) {
	s := New(Options{})
	c := newInboundTestConn(s.rpcScheduler, 1, 4, time.Second)
	claim, err := s.rpcResults.Acquire([8]byte{9}, 90, 900)
	if err != nil || claim.state != rpcResultAcquireOwner || claim.owner == nil {
		t.Fatalf("Acquire owner = state:%d err:%v", claim.state, err)
	}
	reservation, err := c.reserveInboundRPC(context.Background(), "test.queuedFlight", 4)
	if err != nil {
		t.Fatalf("reserve queued legacyRPC: %v", err)
	}
	task := s.newInboundRPCTask(c, 900, "test.queuedFlight", []byte{1, 2, 3, 4}, claim.owner)
	if err := reservation.commit(task); err != nil {
		t.Fatalf("commit queued legacyRPC: %v", err)
	}

	// The scheduler is intentionally not started, so close must drain the queued
	// task and invoke its independent flight-release callback.
	c.closeInboundRPCScheduler()
	if got := s.rpcResults.flightLimit.snapshot(); got != 0 {
		t.Fatalf("connection close leaked %d queued owner claims", got)
	}
	if tasks, bytes := s.rpcScheduler.budgetSnapshot(); tasks != 0 || bytes != 0 {
		t.Fatalf("connection close leaked scheduler budget %d/%d", tasks, bytes)
	}
	retry, err := s.rpcResults.Acquire([8]byte{9}, 90, 900)
	if err != nil || retry.state != rpcResultAcquireOwner {
		t.Fatalf("reclaim after queued close = state:%d err:%v", retry.state, err)
	}
	if !retry.owner.Abort() {
		t.Fatal("replacement owner did not abort")
	}
}
