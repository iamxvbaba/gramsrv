package mtprotoedge

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// rpcRewrapRegistry links only an explicit official-client transition:
// outstanding naked request -> invokeWithLayer(initConnection(the exact same
// request)). It is not a general content-dedup cache. Entries are hard-bounded
// and are retired by protocol events (client ACK, alias consumption, owner
// abort, or the first post-init naked request), never by client identity or by
// delaying request execution.
type rpcRewrapRegistry struct {
	mu        sync.Mutex
	max       int
	total     int
	byKey     map[rpcRewrapKey][]*rpcRewrapCandidate
	bySession map[rpcRewrapSessionKey]map[*rpcRewrapCandidate]struct{}
	byRequest map[rpcRewrapRequestKey]*rpcRewrapCandidate
}

type rpcRewrapSessionKey struct {
	authKeyID [8]byte
	sessionID int64
}

type rpcRewrapKey struct {
	rpcRewrapSessionKey
	fingerprint [sha256.Size]byte
	semantic    tlprofile.SemanticIdentity
	call        tlprofile.CallIdentity
	exact       bool
}

type rpcRewrapRequestKey struct {
	rpcRewrapSessionKey
	reqMsgID int64
}

type rpcRewrapCandidate struct {
	active   bool
	claimed  bool
	key      rpcRewrapKey
	source   *Conn
	reqMsgID int64
	method   string
	owner    *rpcResultOwnerLease
	waiter   *rpcResultWaiter
}

func newRPCRewrapRegistry(max int) *rpcRewrapRegistry {
	if max <= 0 {
		max = rpcResultFlightDefaultMaxPending
	}
	return &rpcRewrapRegistry{
		max:       max,
		byKey:     make(map[rpcRewrapKey][]*rpcRewrapCandidate),
		bySession: make(map[rpcRewrapSessionKey]map[*rpcRewrapCandidate]struct{}),
		byRequest: make(map[rpcRewrapRequestKey]*rpcRewrapCandidate),
	}
}

func (r *rpcRewrapRegistry) register(c *Conn, body []byte, reqMsgID int64, method string, owner *rpcResultOwnerLease) bool {
	if r == nil || c == nil || c.rpcRewrapInitialized.Load() || owner == nil {
		return false
	}
	session := rpcRewrapSessionKey{authKeyID: c.authKeyID, sessionID: c.sessionID}
	key := rpcRewrapKey{rpcRewrapSessionKey: session, fingerprint: sha256.Sum256(body)}
	candidate := &rpcRewrapCandidate{
		active: true, key: key, source: c, reqMsgID: reqMsgID, method: method,
		owner: owner, waiter: owner.Waiter(),
	}
	if candidate.waiter == nil {
		return false
	}
	r.mu.Lock()
	if r.total >= r.max {
		r.mu.Unlock()
		return false
	}
	r.byKey[key] = append(r.byKey[key], candidate)
	set := r.bySession[session]
	if set == nil {
		set = make(map[*rpcRewrapCandidate]struct{})
		r.bySession[session] = set
	}
	set[candidate] = struct{}{}
	r.byRequest[rpcRewrapRequestKey{rpcRewrapSessionKey: session, reqMsgID: reqMsgID}] = candidate
	r.total++
	r.mu.Unlock()
	if !owner.InstallAbortHook(func() { r.remove(candidate) }) {
		r.remove(candidate)
		return false
	}
	return true
}

func (r *rpcRewrapRegistry) registerSemantic(
	c *Conn,
	identity tlprofile.SemanticIdentity,
	call tlprofile.CallIdentity,
	reqMsgID int64,
	method string,
	owner *rpcResultOwnerLease,
) bool {
	if identity.Method() == 0 || identity.CanonicalSize() <= 0 {
		return false
	}
	return r.registerKey(c, rpcRewrapKey{
		rpcRewrapSessionKey: rpcRewrapSessionKey{authKeyID: c.authKeyID, sessionID: c.sessionID},
		semantic:            identity,
		call:                call,
		exact:               true,
	}, reqMsgID, method, owner)
}

func (r *rpcRewrapRegistry) registerKey(c *Conn, key rpcRewrapKey, reqMsgID int64, method string, owner *rpcResultOwnerLease) bool {
	if r == nil || c == nil || c.rpcRewrapInitialized.Load() || owner == nil {
		return false
	}
	candidate := &rpcRewrapCandidate{
		active: true, key: key, source: c, reqMsgID: reqMsgID, method: method,
		owner: owner, waiter: owner.Waiter(),
	}
	if candidate.waiter == nil {
		return false
	}
	session := key.rpcRewrapSessionKey
	r.mu.Lock()
	if r.total >= r.max {
		r.mu.Unlock()
		return false
	}
	r.byKey[key] = append(r.byKey[key], candidate)
	set := r.bySession[session]
	if set == nil {
		set = make(map[*rpcRewrapCandidate]struct{})
		r.bySession[session] = set
	}
	set[candidate] = struct{}{}
	r.byRequest[rpcRewrapRequestKey{rpcRewrapSessionKey: session, reqMsgID: reqMsgID}] = candidate
	r.total++
	r.mu.Unlock()
	if !owner.InstallAbortHook(func() { r.remove(candidate) }) {
		r.remove(candidate)
		return false
	}
	return true
}

func (r *rpcRewrapRegistry) claim(c *Conn, inner []byte) *rpcRewrapCandidate {
	if r == nil || c == nil {
		return nil
	}
	session := rpcRewrapSessionKey{authKeyID: c.authKeyID, sessionID: c.sessionID}
	key := rpcRewrapKey{rpcRewrapSessionKey: session, fingerprint: sha256.Sum256(inner)}
	r.mu.Lock()
	queue := r.byKey[key]
	for _, candidate := range queue {
		if !candidate.active || candidate.claimed {
			continue
		}
		candidate.claimed = true
		r.mu.Unlock()
		return candidate
	}
	r.mu.Unlock()
	return nil
}

func (r *rpcRewrapRegistry) claimSemantic(
	c *Conn,
	identity tlprofile.SemanticIdentity,
	call tlprofile.CallIdentity,
) *rpcRewrapCandidate {
	if r == nil || c == nil || identity.Method() == 0 || identity.CanonicalSize() <= 0 {
		return nil
	}
	return r.claimKey(rpcRewrapKey{
		rpcRewrapSessionKey: rpcRewrapSessionKey{authKeyID: c.authKeyID, sessionID: c.sessionID},
		semantic:            identity,
		call:                call,
		exact:               true,
	})
}

func (r *rpcRewrapRegistry) claimKey(key rpcRewrapKey) *rpcRewrapCandidate {
	r.mu.Lock()
	queue := r.byKey[key]
	for _, candidate := range queue {
		if !candidate.active || candidate.claimed {
			continue
		}
		candidate.claimed = true
		r.mu.Unlock()
		return candidate
	}
	r.mu.Unlock()
	return nil
}

func (r *rpcRewrapRegistry) commit(candidate *rpcRewrapCandidate) {
	if r == nil || candidate == nil {
		return
	}
	r.mu.Lock()
	r.removeLocked(candidate)
	r.mu.Unlock()
	candidate.owner.SetAbortHook(nil)
}

func (r *rpcRewrapRegistry) release(candidate *rpcRewrapCandidate) {
	if r == nil || candidate == nil {
		return
	}
	r.mu.Lock()
	if candidate.active {
		candidate.claimed = false
	}
	r.mu.Unlock()
}

func (r *rpcRewrapRegistry) remove(candidate *rpcRewrapCandidate) {
	if r == nil || candidate == nil {
		return
	}
	r.mu.Lock()
	r.removeLocked(candidate)
	r.mu.Unlock()
}

// acknowledge retires a candidate only after the client explicitly ACKs the
// physical rpc_result. A successful socket write alone is insufficient proof:
// the client may already have reassigned the request to a new msg_id without
// parsing that old result.
func (r *rpcRewrapRegistry) acknowledge(c *Conn, reqMsgID int64) {
	if r == nil || c == nil || reqMsgID == 0 {
		return
	}
	request := rpcRewrapRequestKey{
		rpcRewrapSessionKey: rpcRewrapSessionKey{authKeyID: c.authKeyID, sessionID: c.sessionID},
		reqMsgID:            reqMsgID,
	}
	r.mu.Lock()
	candidate := r.byRequest[request]
	r.removeLocked(candidate)
	r.mu.Unlock()
	if candidate != nil {
		candidate.owner.SetAbortHook(nil)
	}
}

func (r *rpcRewrapRegistry) removeLocked(candidate *rpcRewrapCandidate) {
	if candidate == nil || !candidate.active {
		return
	}
	candidate.active = false
	candidate.claimed = false
	r.total--
	session := candidate.key.rpcRewrapSessionKey
	delete(r.byRequest, rpcRewrapRequestKey{rpcRewrapSessionKey: session, reqMsgID: candidate.reqMsgID})
	if set := r.bySession[session]; set != nil {
		delete(set, candidate)
		if len(set) == 0 {
			delete(r.bySession, session)
		}
	}
	queue := r.byKey[candidate.key]
	for i, existing := range queue {
		if existing != candidate {
			continue
		}
		copy(queue[i:], queue[i+1:])
		queue[len(queue)-1] = nil
		queue = queue[:len(queue)-1]
		break
	}
	if len(queue) == 0 {
		delete(r.byKey, candidate.key)
	} else {
		r.byKey[candidate.key] = queue
	}
}

func (r *rpcRewrapRegistry) clearSession(c *Conn) {
	if r == nil || c == nil {
		return
	}
	session := rpcRewrapSessionKey{authKeyID: c.authKeyID, sessionID: c.sessionID}
	r.mu.Lock()
	set := r.bySession[session]
	owners := make([]*rpcResultOwnerLease, 0, len(set))
	for candidate := range set {
		owners = append(owners, candidate.owner)
		r.removeLocked(candidate)
	}
	r.mu.Unlock()
	for _, owner := range owners {
		owner.SetAbortHook(nil)
	}
}

type rpcRewrapInit struct {
	layer       int
	apiID       int
	deviceModel string
	system      string
	appVersion  string
	systemLang  string
	langPack    string
	langCode    string
	inner       []byte
}

type rpcRewrapRawObject struct {
	data []byte
}

func (o *rpcRewrapRawObject) Decode(b *bin.Buffer) error {
	if _, err := b.PeekID(); err != nil {
		return err
	}
	o.data = b.Buf
	b.Skip(len(b.Buf))
	return nil
}

func (o *rpcRewrapRawObject) Encode(b *bin.Buffer) error {
	b.Put(o.data)
	return nil
}

func decodeRPCRewrapInit(body []byte) (rpcRewrapInit, bool) {
	b := &bin.Buffer{Buf: body}
	if err := b.ConsumeID(tg.InvokeWithLayerRequestTypeID); err != nil {
		return rpcRewrapInit{}, false
	}
	layer, err := b.Int()
	if err != nil || layer <= 0 {
		return rpcRewrapInit{}, false
	}
	raw := &rpcRewrapRawObject{}
	req := tg.InitConnectionRequest{Query: raw}
	if err := req.Decode(b); err != nil || b.Len() != 0 || len(raw.data) < bin.Word {
		return rpcRewrapInit{}, false
	}
	return rpcRewrapInit{
		layer: layer, apiID: req.APIID, deviceModel: req.DeviceModel,
		system: req.SystemVersion, appVersion: req.AppVersion,
		systemLang: req.SystemLangCode, langPack: req.LangPack, langCode: req.LangCode,
		inner: raw.data,
	}, true
}

type rpcRewrapAlias struct {
	conn                    *Conn
	itemIndex               int
	newReqID                int64
	method                  string
	oldWaiter               *rpcResultWaiter
	newOwner                *rpcResultOwnerLease
	sourceConn              *Conn
	sourceOwner             *rpcResultOwnerLease
	retargeted              atomic.Bool
	observeInit             bool
	init                    rpcRewrapInit
	candidate               *rpcRewrapCandidate
	registry                *rpcRewrapRegistry
	afterSuccessfulDelivery func() error
	finishReplayRestore     func()
	afterOnce               sync.Once
	deliveredFinalizeOnce   sync.Once
	deliveredFinalizeErr    error
	resultStoreClaimed      atomic.Bool
	executionOK             atomic.Bool
	// bodyReservation pins the replay/retarget clone from before allocation
	// through queue residence. It is concurrency-safe because the watchdog and
	// outbound actor race to release or take the same one-shot ownership token.
	bodyReservation *outboundBodyReservation
}

func (a *rpcRewrapAlias) beginReplayRestore() {
	if a == nil || a.conn == nil || a.finishReplayRestore != nil {
		return
	}
	a.finishReplayRestore = a.conn.beginRPCReplayRestore()
}

func (a *rpcRewrapAlias) runAfterSuccessfulDelivery() (err error) {
	if a == nil || a.afterSuccessfulDelivery == nil {
		return nil
	}
	a.afterOnce.Do(func() {
		if a.executionOK.Load() {
			err = a.afterSuccessfulDelivery()
		}
	})
	return err
}

func (a *rpcRewrapAlias) finishReplayRestoreWithoutDelivery() {
	if a == nil {
		return
	}
	a.releaseBodyReservation()
	// Win or wait for any concurrent callback before dropping the barrier.
	a.afterOnce.Do(func() {})
	a.releaseReplayRestoreBarrier()
}

func (a *rpcRewrapAlias) releaseBodyReservation() {
	if a != nil && a.bodyReservation != nil {
		a.bodyReservation.release()
	}
}

func (a *rpcRewrapAlias) releaseReplayRestoreBarrier() {
	if a != nil && a.finishReplayRestore != nil {
		a.finishReplayRestore()
	}
}

func (a *rpcRewrapAlias) releaseDeferredLogicalHook() {
	if a == nil || a.sourceOwner == nil || a.sourceOwner.Delivery() == nil ||
		a.sourceOwner.Delivery().coordinator == nil {
		return
	}
	a.sourceOwner.Delivery().coordinator.releaseDeferredHook()
}

func (a *rpcRewrapAlias) storeResultOnce(s *Server, encoded *encodedOutboundMessage) {
	if a == nil || s == nil || encoded == nil || !a.resultStoreClaimed.CompareAndSwap(false, true) {
		return
	}
	s.storeRPCResult(a.conn, a.newReqID, encoded)
}

func claimRPCRewrapLogicalHook(
	ctx context.Context,
	encoded *encodedOutboundMessage,
) (*rpcResultDeliveryHookClaim, error) {
	if encoded == nil {
		return nil, nil
	}
	// Only this alias is allowed to consume a sticky TryRetarget deferral. If a
	// late physical success races another replacement replay, the coordinator
	// waits for its Claimed/InProgress hook to publish Done.
	return encoded.claimLogicalDeliveryHook(ctx, true)
}

// completeDeliveredRPCRewrapResult is safe after a watchdog has already fenced
// this physical generation. The caller has independent proof that the
// retargeted bytes reached the stream; deliveredFinalizeOnce, the shared hook
// coordinator and cache publication make late/concurrent invocations converge
// while preserving replacement -> logical -> cache -> barrier order.
func (s *Server) completeDeliveredRPCRewrapResult(
	ctx context.Context,
	a *rpcRewrapAlias,
	encoded *encodedOutboundMessage,
	source string,
) error {
	if s == nil || a == nil || encoded == nil {
		return ErrRPCResultFlightInvalid
	}
	a.deliveredFinalizeOnce.Do(func() {
		defer a.releaseBodyReservation()
		defer a.releaseReplayRestoreBarrier()
		restoreCtx, cancel := boundedRPCReplayRestoreContext(ctx)
		defer cancel()
		logical, claimErr := claimRPCRewrapLogicalHook(restoreCtx, encoded)
		encoded.markDelivered()
		if claimErr != nil {
			a.conn.fenceUndeliveredRPCResult()
			a.deliveredFinalizeErr = fmt.Errorf("wait for rewrapped rpc_result logical restore: %w", claimErr)
			a.storeResultOnce(s, encoded)
			return
		}
		a.deliveredFinalizeErr = s.runBoundedRPCReplayRestore(
			restoreCtx, a.conn, source, logical, a.runAfterSuccessfulDelivery,
		)
		a.storeResultOnce(s, encoded)
	})
	return a.deliveredFinalizeErr
}

var (
	rpcRewrapDeliveryOnce    sync.Once
	rpcRewrapDeliveryJobs    chan rpcRewrapDeliveryJob
	rpcRewrapObservationOnce sync.Once
	rpcRewrapObservationJobs chan rpcRewrapDeliveryJob
)

const (
	rpcRewrapDeliveryWorkers = 4
	rpcRewrapDeliveryQueue   = 256
	rpcRewrapObserverWorkers = 1
	rpcRewrapObserverQueue   = 64
	// Queue residence, physical delivery and ordered restore share one absolute
	// deadline. An admitted alias must never retain a Conn scheduler barrier for
	// minutes behind older slow jobs.
	rpcRewrapDeliveryQueueTimeout = 5 * time.Second
)

type rpcRewrapDeliveryJob struct {
	run      func(*rpcRewrapDeliveryControl, time.Time)
	fail     func(error)
	deadline time.Time
	control  *rpcRewrapDeliveryControl
}

type rpcRewrapDeliveryJobState uint32

const (
	rpcRewrapJobPending rpcRewrapDeliveryJobState = iota
	rpcRewrapJobRunning
	rpcRewrapJobCommitted
	rpcRewrapJobComplete
	rpcRewrapJobFailed
)

// rpcRewrapDeliveryControl lets an independent deadline timer retire queued,
// running and physically committed jobs. A late worker cannot enter run after
// the timer wins; a committed non-cooperative restore is fenced by fail, and
// its eventual return cannot report failure or finish the barrier a second time.
type rpcRewrapDeliveryControl struct {
	state   atomic.Uint32
	timerMu sync.Mutex
	timer   *time.Timer
}

type rpcRewrapPhysicalOutcome struct {
	err   error
	owned bool
}

// waitRPCRewrapPhysicalTerminal deliberately keeps one of the four bounded
// workers attached to an in-progress actor write even after the watchdog fences
// the Conn. A broken transport may therefore strand at most four workers, while
// queued jobs still time out independently. If that transport later reports
// success, the worker cannot lose the logical hook merely because timeout won
// before its goroutine resumed.
func waitRPCRewrapPhysicalTerminal(
	c *Conn,
	ctx context.Context,
	encoded *encodedOutboundMessage,
	reserved *outboundBodyReservation,
	control *rpcRewrapDeliveryControl,
) rpcRewrapPhysicalOutcome {
	terminal := make(chan rpcRewrapPhysicalOutcome, 1)
	_ = c.sendOutboundWithTerminalReserved(
		ctx, proto.MessageServerResponse, nil, encoded, false,
		func(err error) {
			terminal <- rpcRewrapPhysicalOutcome{err: err, owned: control.commit()}
		},
		reserved,
	)
	return <-terminal
}

func newRPCRewrapDeliveryControl() *rpcRewrapDeliveryControl {
	c := &rpcRewrapDeliveryControl{}
	c.state.Store(uint32(rpcRewrapJobPending))
	return c
}

func (c *rpcRewrapDeliveryControl) transition(from, to rpcRewrapDeliveryJobState) bool {
	return c != nil && c.state.CompareAndSwap(uint32(from), uint32(to))
}

func (c *rpcRewrapDeliveryControl) fail() bool {
	if c == nil {
		return true
	}
	for {
		state := rpcRewrapDeliveryJobState(c.state.Load())
		if state == rpcRewrapJobComplete || state == rpcRewrapJobFailed {
			return false
		}
		if c.transition(state, rpcRewrapJobFailed) {
			c.stopTimer()
			return true
		}
	}
}

func (c *rpcRewrapDeliveryControl) timeout() bool {
	if c == nil {
		return true
	}
	for {
		state := rpcRewrapDeliveryJobState(c.state.Load())
		if state != rpcRewrapJobPending && state != rpcRewrapJobRunning &&
			state != rpcRewrapJobCommitted {
			return false
		}
		if c.transition(state, rpcRewrapJobFailed) {
			c.stopTimer()
			return true
		}
	}
}

// commit records successful physical delivery (or an already-proven retarget)
// without disarming the watchdog. The same absolute deadline covers the
// replacement/logical restore and cache/barrier terminal path; complete is the
// only successful transition that stops the timer.
func (c *rpcRewrapDeliveryControl) commit() bool {
	if c == nil || !c.transition(rpcRewrapJobRunning, rpcRewrapJobCommitted) {
		return false
	}
	return true
}

func (c *rpcRewrapDeliveryControl) running() bool {
	return c != nil && rpcRewrapDeliveryJobState(c.state.Load()) == rpcRewrapJobRunning
}

func (c *rpcRewrapDeliveryControl) complete() {
	if c == nil {
		return
	}
	for {
		state := rpcRewrapDeliveryJobState(c.state.Load())
		if state != rpcRewrapJobRunning && state != rpcRewrapJobCommitted {
			return
		}
		if c.transition(state, rpcRewrapJobComplete) {
			c.stopTimer()
			return
		}
	}
}

func (c *rpcRewrapDeliveryControl) installTimer(timer *time.Timer) {
	if c == nil || timer == nil {
		return
	}
	c.timerMu.Lock()
	c.timer = timer
	state := rpcRewrapDeliveryJobState(c.state.Load())
	terminal := state == rpcRewrapJobComplete || state == rpcRewrapJobFailed
	c.timerMu.Unlock()
	if terminal {
		timer.Stop()
	}
}

func (c *rpcRewrapDeliveryControl) stopTimer() {
	if c == nil {
		return
	}
	c.timerMu.Lock()
	timer := c.timer
	c.timer = nil
	c.timerMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (j rpcRewrapDeliveryJob) reportFailure(err error) {
	if err == nil {
		err = fmt.Errorf("rpc rewrap delivery job failed")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("mtprotoedge: rpc rewrap delivery failure callback panicked: %v\n%s", recovered, debug.Stack())
		}
	}()
	if j.fail != nil {
		j.fail(err)
		return
	}
	log.Printf("mtprotoedge: rpc rewrap delivery job failed: %v", err)
}

func runRPCRewrapDeliveryJob(j rpcRewrapDeliveryJob) {
	if j.run == nil {
		j.reportFailure(fmt.Errorf("nil rpc rewrap delivery job"))
		return
	}
	control := j.control
	if control == nil {
		control = newRPCRewrapDeliveryControl()
	}
	if !control.transition(rpcRewrapJobPending, rpcRewrapJobRunning) {
		return
	}
	if !j.deadline.IsZero() && !time.Now().Before(j.deadline) {
		if control.fail() {
			j.reportFailure(context.DeadlineExceeded)
		}
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if control.fail() {
				j.reportFailure(fmt.Errorf("rpc rewrap delivery panic: %v", recovered))
			}
			log.Printf("mtprotoedge: rpc rewrap delivery job panicked: %v\n%s", recovered, debug.Stack())
			return
		}
		control.complete()
	}()
	j.run(control, j.deadline)
}

func scheduleRPCRewrapJob(
	job rpcRewrapDeliveryJob,
	once *sync.Once,
	jobs *chan rpcRewrapDeliveryJob,
	workers, queue int,
) bool {
	if job.run == nil {
		return false
	}
	if job.deadline.IsZero() {
		job.deadline = time.Now().Add(rpcRewrapDeliveryQueueTimeout)
	}
	job.control = newRPCRewrapDeliveryControl()
	once.Do(func() {
		*jobs = make(chan rpcRewrapDeliveryJob, queue)
		for range workers {
			go func() {
				for job := range *jobs {
					runRPCRewrapDeliveryJob(job)
				}
			}()
		}
	})
	delay := time.Until(job.deadline)
	if delay < 0 {
		delay = 0
	}
	timer := time.AfterFunc(delay, func() {
		if job.control.timeout() {
			job.reportFailure(context.DeadlineExceeded)
		}
	})
	job.control.installTimer(timer)
	select {
	case *jobs <- job:
		return true
	default:
		// If the independent timer already won, it owns the fail callback and the
		// caller must not report queue failure a second time.
		if job.control.transition(rpcRewrapJobPending, rpcRewrapJobComplete) {
			job.control.stopTimer()
			return false
		}
		return true
	}
}

func scheduleRPCRewrapDeliveryJob(job rpcRewrapDeliveryJob) bool {
	return scheduleRPCRewrapJob(job, &rpcRewrapDeliveryOnce, &rpcRewrapDeliveryJobs,
		rpcRewrapDeliveryWorkers, rpcRewrapDeliveryQueue)
}

func scheduleRPCRewrapObservation(fn func()) bool {
	if fn == nil {
		return false
	}
	return scheduleRPCRewrapJob(rpcRewrapDeliveryJob{
		deadline: time.Now().Add(rpcRewrapDeliveryQueueTimeout),
		run:      func(*rpcRewrapDeliveryControl, time.Time) { fn() },
	}, &rpcRewrapObservationOnce, &rpcRewrapObservationJobs,
		rpcRewrapObserverWorkers, rpcRewrapObserverQueue)
}

func (s *Server) rpcRewrapRestoreJob(
	a *rpcRewrapAlias,
	source string,
	run func(*rpcRewrapDeliveryControl, time.Time),
) rpcRewrapDeliveryJob {
	return rpcRewrapDeliveryJob{
		deadline: time.Now().Add(rpcRewrapDeliveryQueueTimeout),
		run:      run,
		fail: func(err error) {
			if a != nil && a.conn != nil {
				a.conn.fenceUndeliveredRPCResult()
			}
			if a != nil {
				a.releaseBodyReservation()
				a.releaseReplayRestoreBarrier()
			}
			if s != nil && s.log != nil {
				s.log.Warn("RPC rewrap delivery job failed",
					zap.String("source", source), zap.Error(err))
			}
		},
	}
}

func (s *Server) failRPCRewrapResultJob(
	a *rpcRewrapAlias,
	encoded *encodedOutboundMessage,
	err error,
) {
	if a != nil {
		defer a.releaseBodyReservation()
	}
	if a == nil || a.conn == nil || a.newOwner == nil || encoded == nil {
		return
	}
	a.conn.fenceUndeliveredRPCResult()
	publish := a.newOwner.HandOff()
	encoded.markReplayable()
	encoded.releaseDeferredLogicalDeliveryHook()
	// Release the connection-local scheduler before any defensive cache panic;
	// the physical generation is already fenced, so no following task can run.
	a.releaseReplayRestoreBarrier()
	if publish {
		a.storeResultOnce(s, encoded)
	}
	if s != nil && s.log != nil {
		s.log.Warn("RPC rewrap result job failed; exact result retained",
			zap.String("method", a.method), zap.Int64("req_msg_id", a.newReqID), zap.Error(err))
	}
}

func (a *rpcRewrapAlias) activate(s *Server) error {
	if a == nil || s == nil || a.conn == nil || a.oldWaiter == nil {
		return ErrRPCResultFlightInvalid
	}
	// Install the scheduler barrier synchronously, before this plan publishes
	// any following naked RPC tasks. The asynchronous physical replay below is
	// then free to use a bounded rewrap worker without an ordering race.
	a.beginReplayRestore()
	var executionSubscriber func(bool)
	if a.newOwner != nil || a.afterSuccessfulDelivery != nil {
		executionSubscriber = func(success bool) {
			a.executionOK.Store(success)
			if a.newOwner != nil {
				a.newOwner.CompleteExecution(success)
			}
		}
	}
	resultSubscriber := func(encoded *encodedOutboundMessage, ok bool) {
		if !ok || encoded == nil {
			if a.newOwner != nil {
				a.newOwner.Abort()
			}
			a.conn.fenceUndeliveredRPCResult()
			a.releaseDeferredLogicalHook()
			a.finishReplayRestoreWithoutDelivery()
			return
		}
		if a.newOwner == nil {
			attempt, reserved, cloneErr := a.conn.cloneRPCResultForRequestReserved(encoded, encoded.reqMsgID, false)
			if cloneErr != nil {
				a.conn.failOutboundBudget(cloneErr)
				a.conn.fenceUndeliveredRPCResult()
				a.finishReplayRestoreWithoutDelivery()
				return
			}
			a.bodyReservation = reserved
			job := s.rpcRewrapRestoreJob(a, "pending init rewrap replay", func(control *rpcRewrapDeliveryControl, deadline time.Time) {
				ctx, cancel := context.WithDeadline(context.Background(), deadline)
				defer cancel()
				outcome := waitRPCRewrapPhysicalTerminal(a.conn, ctx, attempt, a.bodyReservation, control)
				if outcome.err != nil {
					if !outcome.owned {
						return
					}
					a.conn.fenceUndeliveredRPCResult()
					attempt.markReplayable()
					a.releaseBodyReservation()
					a.releaseReplayRestoreBarrier()
					if !isClientDisconnect(outcome.err) {
						s.log.Debug("RPC init rewrap pending replay failed", zap.Error(outcome.err))
					}
					return
				}
				// A watchdog may win after the transport has already returned physical
				// success but before this goroutine resumes. Success is irrevocable: run
				// the once-only restore with a fresh bounded lifetime if timeout failure
				// already fenced/released this physical generation.
				restoreParent := ctx
				if !outcome.owned {
					restoreParent = context.Background()
				}
				restoreCtx, cancelRestore := boundedRPCReplayRestoreContext(restoreParent)
				defer cancelRestore()
				logical, claimErr := attempt.claimLogicalDeliveryHook(restoreCtx, false)
				attempt.markDelivered()
				if claimErr != nil {
					a.conn.fenceUndeliveredRPCResult()
					a.releaseBodyReservation()
					a.releaseReplayRestoreBarrier()
					return
				}
				restoreErr := s.runBoundedRPCReplayRestore(
					restoreCtx, a.conn, "pending init rewrap replay", logical, a.runAfterSuccessfulDelivery,
				)
				a.releaseBodyReservation()
				a.releaseReplayRestoreBarrier()
				if restoreErr != nil && !isClientDisconnect(restoreErr) {
					s.log.Debug("RPC init rewrap pending replay failed", zap.Error(restoreErr))
				}
			})
			if !scheduleRPCRewrapDeliveryJob(job) {
				a.conn.fenceUndeliveredRPCResult()
				a.finishReplayRestoreWithoutDelivery()
			}
			return
		}
		// A successful physical write under the retargeted req_msg_id is the only
		// proof that lets the alias reuse that attempt. A mere TryRetarget success
		// is not proof: the original socket may have failed before any bytes landed.
		retargetDelivered := a.retargeted.Load() &&
			encoded.deliveryState() == rpcResultDeliveryDelivered &&
			encoded.writtenRequestID() == a.newReqID
		clone, reserved, err := a.conn.cloneRPCResultForRequestReserved(encoded, a.newReqID, retargetDelivered)
		if err != nil {
			a.conn.failOutboundBudget(err)
			a.newOwner.Abort()
			a.conn.fenceUndeliveredRPCResult()
			a.finishReplayRestoreWithoutDelivery()
			return
		}
		a.bodyReservation = reserved
		if retargetDelivered {
			if !a.newOwner.HandOff() {
				a.conn.fenceUndeliveredRPCResult()
				a.finishReplayRestoreWithoutDelivery()
				return
			}
			job := s.rpcRewrapRestoreJob(a, "retargeted init rewrap result", func(control *rpcRewrapDeliveryControl, deadline time.Time) {
				if !control.commit() {
					return
				}
				restoreCtx, cancelRestore := context.WithDeadline(context.Background(), deadline)
				defer cancelRestore()
				restoreErr := s.completeDeliveredRPCRewrapResult(
					restoreCtx, a, clone, "retargeted init rewrap result",
				)
				if restoreErr != nil && !isClientDisconnect(restoreErr) {
					s.log.Debug("Retargeted RPC restore failed", zap.Error(restoreErr))
				}
			})
			job.fail = func(err error) {
				// Never enter deliveredFinalizeOnce from the timer goroutine: the worker
				// may already own a non-cooperative restore. Fence and release its Conn
				// barrier first, then retain the immutable delivered result for a later
				// replacement replay, which will wait on coordinator Claimed/InProgress.
				a.conn.fenceUndeliveredRPCResult()
				clone.markReplayable()
				clone.releaseDeferredLogicalDeliveryHook()
				a.releaseReplayRestoreBarrier()
				a.storeResultOnce(s, clone)
				a.releaseBodyReservation()
				s.log.Warn("Retargeted RPC restore watchdog expired",
					zap.String("method", a.method), zap.Int64("req_msg_id", a.newReqID), zap.Error(err))
			}
			if !scheduleRPCRewrapDeliveryJob(job) {
				job.fail(ErrOutboundQueueFull)
			}
			s.log.Info("RPC init rewrap result retargeted",
				zap.String("method", a.method), zap.Int64("new_req_msg_id", a.newReqID),
				zap.String("auth_key_id", a.conn.authKeyHex), zap.Int64("session_id", a.conn.sessionID))
			return
		}
		job := s.rpcRewrapRestoreJob(a, "pending init rewrap result", func(control *rpcRewrapDeliveryControl, deadline time.Time) {
			s.publishRewrappedRPCResult(a.conn, a.newReqID, a.method, a.newOwner, clone, a, control, deadline)
		})
		// Once this alias consumed the source candidate, expiration or panic of
		// the admitted worker job must still publish the immutable result under
		// the new msg_id. Otherwise the alias owner would remain pending forever
		// (or a reconnect could execute the business request a second time).
		job.fail = func(err error) { s.failRPCRewrapResultJob(a, clone, err) }
		if !scheduleRPCRewrapDeliveryJob(job) {
			// The completed result is durable in memory. Fence before publishing it
			// under the new msg_id so a replacement can replay without re-executing.
			s.failRPCRewrapResultJob(a, clone, ErrOutboundQueueFull)
		}
	}
	var err error
	if executionSubscriber != nil {
		err = a.oldWaiter.SubscribeResultAndExecution(resultSubscriber, executionSubscriber)
	} else {
		err = a.oldWaiter.Subscribe(resultSubscriber)
	}
	if err != nil {
		a.finishReplayRestoreWithoutDelivery()
		s.rpcRewrap.release(a.candidate)
		return err
	}
	// Subscribe first so every terminal owner event has a consumer. If completion
	// wins this race the callback replays under the new ID; if retarget wins, the
	// sole outbound actor snapshots the new ID before writing.
	if a.newOwner != nil && a.sourceConn == a.conn && a.sourceOwner != nil {
		a.retargeted.Store(a.sourceOwner.TryRetarget(a.newReqID))
	}
	if a.observeInit {
		s.scheduleRewrappedInitObservation(a.conn, a.init)
	}
	s.rpcRewrap.commit(a.candidate)
	a.candidate = nil
	return nil
}

func (a *rpcRewrapAlias) releaseCandidate() {
	if a == nil || a.candidate == nil {
		return
	}
	a.registry.release(a.candidate)
	a.candidate = nil
}

func (s *Server) scheduleRewrappedInitObservation(c *Conn, init rpcRewrapInit) {
	observer, ok := s.rpc.(RPCInitConnectionObserver)
	if !ok || c == nil {
		return
	}
	if !scheduleRPCRewrapObservation(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := observer.ObserveInitConnection(
			ctx, c.authKeyID, c.sessionID, init.layer, init.apiID,
			init.deviceModel, init.system, init.appVersion, init.systemLang,
			init.langPack, init.langCode,
		); err != nil {
			s.log.Debug("Observe rewrapped initConnection failed", zap.Error(err))
		}
	}) {
		s.log.Debug("Observe rewrapped initConnection dropped", zap.String("auth_key_id", c.authKeyHex))
	}
}

func (s *Server) publishRewrappedRPCResult(
	c *Conn,
	reqMsgID int64,
	method string,
	owner *rpcResultOwnerLease,
	encoded *encodedOutboundMessage,
	alias *rpcRewrapAlias,
	control *rpcRewrapDeliveryControl,
	deadline time.Time,
) {
	if s == nil || c == nil || owner == nil || encoded == nil {
		if alias != nil {
			alias.finishReplayRestoreWithoutDelivery()
		}
		return
	}
	if !owner.HandOff() {
		c.fenceUndeliveredRPCResult()
		alias.finishReplayRestoreWithoutDelivery()
		return
	}
	if control == nil || !control.running() {
		alias.releaseBodyReservation()
		return
	}
	priority := rpcResultPriority(method, encoded)
	encoded.priority = priority
	encoded.markQueued()
	if deadline.IsZero() {
		deadline = time.Now().Add(rpcRewrapDeliveryQueueTimeout)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	// Rewrap delivery is synchronous on this small bounded worker pool. This
	// makes the queue deadline cover the physical write and lets the pending
	// logical hook join the same per-Conn ordered restore, without touching the
	// process-wide asynchronous hook executor.
	outcome := waitRPCRewrapPhysicalTerminal(c, ctx, encoded, alias.bodyReservation, control)
	if outcome.err != nil {
		if !outcome.owned {
			return
		}
		encoded.markReplayable()
		encoded.releaseDeferredLogicalDeliveryHook()
		c.fenceUndeliveredRPCResult()
		alias.storeResultOnce(s, encoded)
		alias.releaseBodyReservation()
		alias.releaseReplayRestoreBarrier()
		return
	}
	// Physical success outranks an already-fired watchdog. The timeout path may
	// have fenced and cached a replayable clone, but it cannot revoke bytes; the
	// shared once/coordinator below still completes logical state exactly once.
	// Run replacement metadata then the original logical hook before publishing
	// the alias cache entry. Whole-finalization once also covers a watchdog racing
	// a late physical terminal, so completed metadata cannot be overwritten.
	restoreParent := ctx
	if !outcome.owned {
		restoreParent = context.Background()
	}
	restoreCtx, cancelRestore := boundedRPCReplayRestoreContext(restoreParent)
	defer cancelRestore()
	restoreErr := s.completeDeliveredRPCRewrapResult(
		restoreCtx, alias, encoded, "physically delivered init rewrap result",
	)
	if restoreErr != nil && !isClientDisconnect(restoreErr) {
		s.log.Debug("RPC init rewrap delivered-state restore failed", zap.Error(restoreErr))
	}
	s.log.Info("RPC init rewrap result replay delivered",
		zap.String("method", method), zap.Int64("req_msg_id", reqMsgID),
		zap.String("auth_key_id", c.authKeyHex), zap.Int64("session_id", c.sessionID),
		zap.Int("wire_bytes", len(encoded.body)))
}
