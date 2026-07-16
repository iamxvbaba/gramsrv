package mtprotoedge

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

var ErrSessionAmbiguous = errors.New("session id is shared by multiple auth keys")

// ForceClose is intentionally test-only. Production shutdown paths use the
// narrower lifecycle primitives so callers cannot bypass ownership rules.
func (c *Conn) ForceClose() {
	c.beginTerminalShutdown()
	c.closeTransport()
	c.closeInboundRPCScheduler()
	c.waitOutboundShutdown()
}

// newRPCResultCache keeps older focused cache tests concise without exposing a
// second production constructor.
func newRPCResultCache(now func() time.Time) *rpcResultCache {
	return newRPCResultCacheWithFlightLimit(now, rpcResultFlightDefaultMaxPending)
}

// Conns is a white-box test accessor. Production wires the shared manager
// explicitly and does not need a second access path through Server.
func (s *Server) Conns() *SessionManager {
	return s.conns
}

// Register is a test fixture shortcut for tests that do not exercise the
// wire-level required-control barrier.
func (m *SessionManager) Register(c *Conn) error {
	if c == nil {
		return ErrSessionActivationSuperseded
	}
	if c.isActive() {
		m.mu.RLock()
		current := m.bySession[connSessionKey(c)]
		m.mu.RUnlock()
		if current == c {
			return nil
		}
		return ErrSessionActivationSuperseded
	}
	if err := m.BeginActivation(c); err != nil {
		return err
	}
	if err := m.PublishActivation(c); err != nil {
		m.AbortActivation(c)
		return err
	}
	return nil
}

func (m *SessionManager) uniqueSessionForTestLocked(sessionID int64) (*Conn, sessionKey, bool, bool) {
	var (
		found    *Conn
		foundKey sessionKey
	)
	for key, c := range m.bySession {
		if key.sessionID != sessionID {
			continue
		}
		if found != nil {
			return nil, sessionKey{}, false, true
		}
		found, foundKey = c, key
	}
	return found, foundKey, found != nil, false
}

// The session-id-only helpers below preserve focused legacy tests without
// carrying an ambiguous global index or API in production.
func (m *SessionManager) BindUser(sessionID, userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, key, ok, ambiguous := m.uniqueSessionForTestLocked(sessionID)
	if !ambiguous && ok {
		m.bindUserLocked(c, key, userID)
	}
}

func (m *SessionManager) UserID(sessionID int64) (int64, bool) {
	m.mu.RLock()
	c, _, ok, ambiguous := m.uniqueSessionForTestLocked(sessionID)
	m.mu.RUnlock()
	if ambiguous || !ok {
		return 0, false
	}
	userID := c.userID.Load()
	return userID, userID != 0
}

func (m *SessionManager) UserIDResolved(sessionID int64) (int64, bool) {
	m.mu.RLock()
	c, _, ok, ambiguous := m.uniqueSessionForTestLocked(sessionID)
	m.mu.RUnlock()
	if ambiguous || !ok {
		return 0, false
	}
	return c.UserIDResolved()
}

func (m *SessionManager) UserIDForAuthKey(authKeyID [8]byte, sessionID int64) (int64, bool) {
	m.mu.RLock()
	c, ok := m.bySession[sessionKey{authKeyID: authKeyID, sessionID: sessionID}]
	m.mu.RUnlock()
	if !ok {
		return 0, false
	}
	userID := c.userID.Load()
	return userID, userID != 0
}

func (m *SessionManager) BindAuthKey(sessionID int64, authKeyID [8]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, key, ok, ambiguous := m.uniqueSessionForTestLocked(sessionID)
	if !ambiguous && ok {
		m.bindAuthKeyLocked(c, key, authKeyID)
	}
}

func (m *SessionManager) AuthKeyID(sessionID int64) ([8]byte, bool) {
	m.mu.RLock()
	c, _, ok, ambiguous := m.uniqueSessionForTestLocked(sessionID)
	m.mu.RUnlock()
	if ambiguous || !ok {
		return [8]byte{}, false
	}
	return c.BusinessAuthKeyID()
}

func (m *SessionManager) SetReceivesUpdates(sessionID int64, receives bool) {
	m.mu.Lock()
	c, key, ok, ambiguous := m.uniqueSessionForTestLocked(sessionID)
	if ambiguous || !ok {
		m.mu.Unlock()
		return
	}
	owner, start := m.setReceivesUpdatesLocked(c, key, receives)
	m.mu.Unlock()
	if start {
		go m.runFlush(c, key, owner, 0)
	}
}

func (m *SessionManager) SetLayerProfile(sessionID int64, profile tlprofile.Profile) bool {
	m.mu.RLock()
	c, _, ok, ambiguous := m.uniqueSessionForTestLocked(sessionID)
	m.mu.RUnlock()
	if ambiguous || !ok {
		return false
	}
	return c.SeedLayerProfile(profile) == nil
}

func (m *SessionManager) PushToSession(ctx context.Context, sessionID int64, t proto.MessageType, msg tg.UpdatesClass) error {
	m.mu.RLock()
	c, key, ok, ambiguous := m.uniqueSessionForTestLocked(sessionID)
	if ambiguous {
		m.mu.RUnlock()
		return ErrSessionAmbiguous
	}
	if !ok {
		m.mu.RUnlock()
		return ErrSessionNotFound
	}
	ready := c.receivesUpdates.Load()
	m.mu.RUnlock()
	if ready {
		updates, err := newLayerUpdatesFanoutContext(ctx, msg)
		if err != nil {
			return err
		}
		encoded, err := updates.prepareForConn(ctx, c)
		if err != nil {
			return err
		}
		return c.SendEncoded(ctx, t, encoded)
	}
	return m.queueOrSendPrepared(ctx, key, t, msg)
}

func (m *SessionManager) PushToUser(ctx context.Context, userID int64, t proto.MessageType, msg tg.UpdatesClass) (int, error) {
	return m.PushToUserExceptAuthKeySession(ctx, userID, [8]byte{}, 0, t, msg)
}

func (m *SessionManager) PushToUserExceptSession(ctx context.Context, userID, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass) (int, error) {
	return m.pushToUser(ctx, userID, nil, excludeSessionID, t, msg)
}

func (m *SessionManager) PushToUserExceptSessionBestEffort(ctx context.Context, userID, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error) {
	return m.pushToUserBestEffort(ctx, userID, nil, excludeSessionID, t, msg, timeout)
}

func (m *SessionManager) Online() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.bySession)
}

func (m *SessionManager) OnlineChannelIDsAfter(afterChannelID int64, limit int) []int64 {
	if limit <= 0 {
		return nil
	}
	const maxRecoveryPage = 4096
	if limit > maxRecoveryPage {
		limit = maxRecoveryPage
	}
	all := m.OnlineChannelIDsSnapshot()
	start := sort.Search(len(all), func(i int) bool { return all[i] > afterChannelID })
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	return all[start:end]
}

func (m *SessionManager) queueLocked(key sessionKey, t proto.MessageType, msg tg.UpdatesClass) bool {
	updates, reservation, err := m.preparePendingPush(onceLayerUpdatesFanout(context.Background(), msg))
	if err != nil {
		m.log.Debug("Drop pending push outside byte budget",
			zap.String("auth_key_id", sessionKeyLog(key.authKeyID)),
			zap.Int64("session_id", key.sessionID),
			zap.Error(err),
		)
		return false
	}
	defer reservation.release()
	return m.queuePreparedLocked(key, t, updates, reservation)
}

func (cs *connState) track(msgID int64, seqNo int32, content bool, state byte) {
	cs.trackInbound(msgID, seqNo, content, false, state)
}

func (s *Server) dispatch(ctx context.Context, cs *connState, c *Conn, msgID int64, seqNo int32, b *bin.Buffer, acks *[]int64) error {
	plan, err := s.preflightInbound(cs, msgID, seqNo, b.Buf)
	if err != nil {
		var bad *dispatchBadMsgError
		if errors.As(err, &bad) && c != nil {
			return s.sendBadMsg(ctx, c, bad.msgID, bad.seqNo, bad.code)
		}
		return err
	}
	defer plan.close()
	plan.commitState(cs)
	*acks = append(*acks, plan.ackIDs...)
	return s.executeInboundPlan(ctx, cs, c, plan)
}

// inboundRPCReservation adapts legacy focused tests to the sole production
// reservation state machine: a batch with exactly one entry.
type inboundRPCReservation struct {
	batch *inboundRPCBatchReservation
}

func (c *Conn) reserveInboundRPC(ctx context.Context, method string, size int) (*inboundRPCReservation, error) {
	batch, err := c.reserveInboundRPCBatch(ctx, []inboundRPCSpec{{method: method, size: size}})
	if err != nil {
		return nil, err
	}
	return &inboundRPCReservation{batch: batch}, nil
}

func (r *inboundRPCReservation) commit(task inboundRPC) error {
	if r == nil || r.batch == nil {
		return ErrConnClosed
	}
	return r.batch.commit([]inboundRPC{task})
}

func (r *inboundRPCReservation) abort() {
	if r != nil && r.batch != nil {
		r.batch.abort()
	}
}

func (c *Conn) enqueueInboundRPC(ctx context.Context, task inboundRPC) error {
	reservation, err := c.reserveInboundRPC(ctx, task.method, task.size)
	if err != nil {
		return err
	}
	defer reservation.abort()
	return reservation.commit(task)
}

func (s *inboundRPCScheduler) budgetSnapshot() (tasks int, bytes int64) {
	s.budgetMu.Lock()
	defer s.budgetMu.Unlock()
	return s.tasks, s.bytes
}
