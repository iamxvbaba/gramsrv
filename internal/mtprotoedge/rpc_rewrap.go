package mtprotoedge

import (
	"context"
	"crypto/sha256"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"
	"github.com/gotd/td/tg"
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
	conn        *Conn
	newReqID    int64
	method      string
	oldWaiter   *rpcResultWaiter
	newOwner    *rpcResultOwnerLease
	sourceConn  *Conn
	sourceOwner *rpcResultOwnerLease
	retargeted  atomic.Bool
	observeInit bool
	init        rpcRewrapInit
	candidate   *rpcRewrapCandidate
	registry    *rpcRewrapRegistry
}

var (
	rpcRewrapDeliveryOnce sync.Once
	rpcRewrapDeliveryJobs chan func()
)

const (
	rpcRewrapDeliveryWorkers = 4
	rpcRewrapDeliveryQueue   = 256
)

func scheduleRPCRewrapDelivery(fn func()) bool {
	if fn == nil {
		return false
	}
	rpcRewrapDeliveryOnce.Do(func() {
		rpcRewrapDeliveryJobs = make(chan func(), rpcRewrapDeliveryQueue)
		for range rpcRewrapDeliveryWorkers {
			go func() {
				for job := range rpcRewrapDeliveryJobs {
					job()
				}
			}()
		}
	})
	select {
	case rpcRewrapDeliveryJobs <- fn:
		return true
	default:
		return false
	}
}

func (a *rpcRewrapAlias) activate(s *Server) error {
	if a == nil || s == nil || a.conn == nil || a.oldWaiter == nil {
		return ErrRPCResultFlightInvalid
	}
	err := a.oldWaiter.Subscribe(func(encoded *encodedOutboundMessage, ok bool) {
		if !ok || encoded == nil {
			if a.newOwner != nil {
				a.newOwner.Abort()
			}
			a.conn.fenceUndeliveredRPCResult()
			return
		}
		if a.newOwner == nil {
			if !scheduleRPCRewrapDelivery(func() {
				ctx, cancel := context.WithTimeout(context.Background(), min(5*time.Second, max(time.Second, a.conn.writeTimeout)))
				defer cancel()
				if err := s.sendCachedRPCResult(ctx, a.conn, encoded); err != nil && !isClientDisconnect(err) {
					s.log.Debug("RPC init rewrap pending replay failed", zap.Error(err))
				}
			}) {
				a.conn.fenceUndeliveredRPCResult()
			}
			return
		}
		clone, err := cloneRPCResultForRequest(encoded, a.newReqID, false)
		if err != nil {
			a.newOwner.Abort()
			a.conn.fenceUndeliveredRPCResult()
			return
		}
		if a.retargeted.Load() {
			if !a.newOwner.HandOff() {
				a.conn.fenceUndeliveredRPCResult()
				return
			}
			clone.markDelivered()
			s.storeRPCResult(a.conn, a.newReqID, clone)
			s.log.Info("RPC init rewrap result retargeted",
				zap.String("method", a.method), zap.Int64("new_req_msg_id", a.newReqID),
				zap.String("auth_key_id", a.conn.authKeyHex), zap.Int64("session_id", a.conn.sessionID))
			return
		}
		if !scheduleRPCRewrapDelivery(func() {
			s.publishRewrappedRPCResult(a.conn, a.newReqID, a.method, a.newOwner, clone)
		}) {
			// The completed result is durable in memory. Fence before publishing it
			// under the new msg_id so a replacement can replay without re-executing.
			a.conn.fenceUndeliveredRPCResult()
			if a.newOwner.HandOff() {
				clone.markReplayable()
				s.storeRPCResult(a.conn, a.newReqID, clone)
			}
		}
	})
	if err != nil {
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
	if !scheduleRPCRewrapDelivery(func() {
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

func (s *Server) publishRewrappedRPCResult(c *Conn, reqMsgID int64, method string, owner *rpcResultOwnerLease, encoded *encodedOutboundMessage) {
	if s == nil || c == nil || owner == nil || encoded == nil {
		return
	}
	if !owner.HandOff() {
		c.fenceUndeliveredRPCResult()
		return
	}
	priority := rpcResultPriority(method, encoded)
	encoded.priority = priority
	terminal := func(deliveryErr error) {
		if deliveryErr != nil {
			encoded.markReplayable()
			c.fenceUndeliveredRPCResult()
		} else {
			encoded.markDelivered()
		}
		s.storeRPCResult(c, reqMsgID, encoded)
	}
	encoded.markQueued()
	ctx, cancel := context.WithTimeout(context.Background(), min(5*time.Second, max(time.Second, c.writeTimeout)))
	defer cancel()
	if err := c.enqueueEncodedDelivery(ctx, proto.MessageServerResponse, encoded, priority, terminal); err != nil {
		terminal(err)
		return
	}
	s.log.Info("RPC init rewrap result replay admitted",
		zap.String("method", method), zap.Int64("req_msg_id", reqMsgID),
		zap.String("auth_key_id", c.authKeyHex), zap.Int64("session_id", c.sessionID),
		zap.Int("wire_bytes", len(encoded.body)))
}
