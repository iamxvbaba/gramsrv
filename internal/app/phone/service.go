package phone

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/iamxvbaba/td/clock"

	"telesrv/internal/domain"
)

// Business errors recognizable by clients; the rpc layer maps them to the corresponding RPC_ERROR (CALL_*, etc.).
var (
	ErrPeerInvalid                = errors.New("phone: call peer invalid")
	ErrAlreadyAccepted            = errors.New("phone: call already accepted")
	ErrAlreadyDeclined            = errors.New("phone: call already declined")
	ErrOccupyFailed               = errors.New("phone: too many active calls")
	ErrProtocolLayerInvalid       = errors.New("phone: protocol layer invalid")
	ErrProtocolCompatLayerInvalid = errors.New("phone: protocol compat layer invalid")
	ErrProtocolFlagsInvalid       = errors.New("phone: protocol flags invalid")
	// ErrGAHashMismatch: the g_a revealed by confirmCall does not match the SHA256 committed
	// by requestCall. The server also forces the call into discarded(disconnect), preventing an
	// attacker from wedging the state machine.
	ErrGAHashMismatch = errors.New("phone: g_a does not match committed hash")
)

// minSupportedLayer is the minimum protocol layer of libtgvoip/tgcalls (both TDesktop
// kMinLayer and DrKLO VoIPService.CALL_MIN_LAYER are 65).
const minSupportedLayer = 65

const (
	gaHashSize = sha256.Size // 32
	dhPubSize  = 256         // g_a / g_b are both 2048-bit
)

// Config holds the runtime parameters of the call service.
type Config struct {
	// RingTimeout is the server-side fallback timeout (same source as the callRingTimeoutMs sent to the client, default 90s).
	RingTimeout time.Duration
	// TombstoneTTL is the retention period of terminal tombstones (the idempotency window absorbing simultaneous hangups / late RPCs).
	TombstoneTTL time.Duration
	// MaxActivePerUser is the cap on concurrent non-terminal calls per user (prevents call-flooding self-lock).
	MaxActivePerUser int
	// MaxRegistryEntries is the hard cap of the in-process registry. When reached, new calls
	// are rejected; Confirmed calls that may still be in progress are not evicted.
	MaxRegistryEntries int
	// SignalingRatePerSecond is the per-call signaling forwarding cap per second; over-limit packets are silently dropped (without breaking the client state machine).
	SignalingRatePerSecond int
}

func (c Config) withDefaults() Config {
	if c.RingTimeout <= 0 {
		c.RingTimeout = 90 * time.Second
	}
	if c.TombstoneTTL <= 0 {
		c.TombstoneTTL = 60 * time.Second
	}
	if c.MaxActivePerUser <= 0 {
		c.MaxActivePerUser = 4
	}
	if c.MaxRegistryEntries <= 0 {
		c.MaxRegistryEntries = 10_000
	}
	if c.SignalingRatePerSecond <= 0 {
		c.SignalingRatePerSecond = 50
	}
	return c
}

// Service implements the private call signaling state machine. Every domain.PhoneCall returned by its methods is a snapshot at that moment.
type Service struct {
	cfg Config
	clk clock.Clock
	reg *registry
}

// Option configures Service.
type Option func(*Service)

// WithClock injects a test clock.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) { s.clk = clk }
}

// NewService creates the call service.
func NewService(cfg Config, opts ...Option) *Service {
	s := &Service{cfg: cfg.withDefaults(), clk: clock.System, reg: newRegistry()}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// RequestCall accepts a caller request: validates the protocol and quota, idempotently
// dedups by (callerID, randomID), and creates the record. Privacy/blocking/target-user
// validity are checked first by the rpc layer.
func (s *Service) RequestCall(ctx context.Context, callerID int64, in domain.PhoneCallRequest) (domain.PhoneCall, error) {
	if err := validateProtocol(in.Protocol); err != nil {
		return domain.PhoneCall{}, err
	}
	if len(in.GAHash) != gaHashSize {
		return domain.PhoneCall{}, ErrProtocolFlagsInvalid
	}
	now := s.clk.Now()
	nowUnix := now.Unix()

	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	s.reg.sweepTombstonesLocked(nowUnix, int64(s.cfg.TombstoneTTL/time.Second))

	// Idempotent: an unterminated call with the same (callerID, randomID) returns the snapshot directly, absorbing client retries.
	key := randomKey{callerID: callerID, randomID: in.RandomID}
	if id, ok := s.reg.byRandom[key]; ok {
		if e, ok := s.reg.byID[id]; ok && !e.call.Terminal() {
			return e.call, nil
		}
	}
	if len(s.reg.byID) >= s.cfg.MaxRegistryEntries {
		return domain.PhoneCall{}, ErrOccupyFailed
	}
	if s.reg.active[callerID] >= s.cfg.MaxActivePerUser {
		return domain.PhoneCall{}, ErrOccupyFailed
	}

	id, err := s.reg.newIDLocked()
	if err != nil {
		return domain.PhoneCall{}, err
	}
	accessHash, err := randomPositiveInt64()
	if err != nil {
		return domain.PhoneCall{}, err
	}
	call := domain.PhoneCall{
		ID:             id,
		AccessHash:     accessHash,
		AdminID:        callerID,
		ParticipantID:  in.CalleeID,
		Video:          in.Video,
		State:          domain.PhoneCallStateRequested,
		Date:           int(nowUnix),
		GAHash:         append([]byte(nil), in.GAHash...),
		CallerProtocol: in.Protocol,
		Protocol:       in.Protocol,
		RandomID:       in.RandomID,
		CallerDevice:   in.CallerDevice,
		PrivacyP2P:     in.PrivacyP2P,
		Connections:    append([]domain.PhoneCallConnection(nil), in.Connections...),
	}
	s.reg.byID[id] = &entry{call: call}
	s.reg.byRandom[key] = id
	s.reg.active[callerID]++
	return call, nil
}

// ReceivedCall marks that the callee device has received the incoming call. The first call
// (Requested→Ringing) returns transitioned=true; other states succeed idempotently (multiple
// devices each report, and late arrivals are harmless).
func (s *Service) ReceivedCall(ctx context.Context, userID, callID, accessHash int64) (domain.PhoneCall, bool, error) {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	e, err := s.lookupLocked(callID, accessHash)
	if err != nil {
		return domain.PhoneCall{}, false, err
	}
	if userID != e.call.ParticipantID {
		// receivedCall can only be reported by the callee.
		return domain.PhoneCall{}, false, ErrPeerInvalid
	}
	if e.call.State == domain.PhoneCallStateRequested {
		e.call.State = domain.PhoneCallStateRinging
		e.call.ReceiveDate = int(s.clk.Now().Unix())
		return e.call, true, nil
	}
	return e.call, false, nil
}

// AcceptCall accepts the callee answering. Concurrent races across devices are serialized
// by the registry lock: the first to complete the transition wins, and later ones get
// ErrAlreadyAccepted (their UI cleans up on its own).
func (s *Service) AcceptCall(ctx context.Context, userID, callID, accessHash int64, gb []byte, proto domain.PhoneCallProtocol, device domain.SessionRef) (domain.PhoneCall, error) {
	if err := validateProtocol(proto); err != nil {
		return domain.PhoneCall{}, err
	}
	if len(gb) != dhPubSize || allZero(gb) {
		return domain.PhoneCall{}, ErrProtocolFlagsInvalid
	}
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	e, err := s.lookupLocked(callID, accessHash)
	if err != nil {
		return domain.PhoneCall{}, err
	}
	if userID != e.call.ParticipantID {
		return domain.PhoneCall{}, ErrPeerInvalid
	}
	switch e.call.State {
	case domain.PhoneCallStateRequested, domain.PhoneCallStateRinging:
		// Valid path; accepting directly without receivedCall is also allowed.
	case domain.PhoneCallStateAccepted, domain.PhoneCallStateConfirmed:
		return domain.PhoneCall{}, ErrAlreadyAccepted
	case domain.PhoneCallStateDiscarded:
		return domain.PhoneCall{}, ErrAlreadyDeclined
	}
	negotiated, err := negotiateProtocol(e.call.CallerProtocol, proto)
	if err != nil {
		return domain.PhoneCall{}, err
	}
	e.call.GB = append([]byte(nil), gb...)
	e.call.CalleeProtocol = proto
	e.call.Protocol = negotiated
	e.call.CalleeDevice = device
	e.call.State = domain.PhoneCallStateAccepted
	// The timeout is not lifted: the same timer keeps running, and if the caller never confirms, the fallback timeout closes it out.
	return e.call, nil
}

// ConfirmCall accepts the caller's confirmation: after verifying SHA256(g_a) matches the
// commitment it enters Confirmed. On verification failure the call is forced into
// discarded(disconnect), returning (terminal snapshot, true, ErrGAHashMismatch); the caller
// must push the terminal state to both sides.
func (s *Service) ConfirmCall(ctx context.Context, userID, callID, accessHash int64, ga []byte, keyFingerprint int64, proto domain.PhoneCallProtocol) (domain.PhoneCall, bool, error) {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	e, err := s.lookupLocked(callID, accessHash)
	if err != nil {
		return domain.PhoneCall{}, false, err
	}
	if userID != e.call.AdminID {
		return domain.PhoneCall{}, false, ErrPeerInvalid
	}
	switch e.call.State {
	case domain.PhoneCallStateAccepted:
	case domain.PhoneCallStateConfirmed:
		return domain.PhoneCall{}, false, ErrAlreadyAccepted
	case domain.PhoneCallStateDiscarded:
		return domain.PhoneCall{}, false, ErrAlreadyDeclined
	default: // Requested / Ringing: the callee has not accepted yet, so confirm is illegal.
		return domain.PhoneCall{}, false, ErrPeerInvalid
	}
	// The third protocol carried by confirmCall is only validated for legality and does not
	// change the negotiation result from accept (official semantics: negotiation completes at
	// accept, and confirm's protocol is an echo).
	if err := validateProtocol(proto); err != nil {
		return domain.PhoneCall{}, false, err
	}
	if len(ga) != dhPubSize || sha256Mismatch(ga, e.call.GAHash) {
		now := int(s.clk.Now().Unix())
		s.reg.markDiscardedLocked(e, domain.PhoneCallDiscardReasonDisconnect, 0, now)
		return e.call, true, ErrGAHashMismatch
	}
	e.call.GA = append([]byte(nil), ga...)
	e.call.KeyFingerprint = keyFingerprint
	e.call.StartDate = int(s.clk.Now().Unix())
	// p2p_allowed = both protocols allow P2P ∧ phone_p2p privacy allows it in both
	// directions (since P3, PrivacyP2P is computed by the rpc layer, and forced relay also
	// sets it false through that). When false, tgcalls uses only relay candidates — provided a
	// usable TURN exists in connections.
	e.call.P2PAllowed = e.call.CallerProtocol.UDPP2P && e.call.CalleeProtocol.UDPP2P && e.call.PrivacyP2P
	e.call.State = domain.PhoneCallStateConfirmed
	return e.call, false, nil
}

// DiscardCall hangs up: reachable from any non-terminal state, idempotent. already=true
// means the call was already terminal (simultaneous hangups: the first sets the reason, the
// later one gets the snapshot).
func (s *Service) DiscardCall(ctx context.Context, userID, callID, accessHash int64, reason domain.PhoneCallDiscardReason, duration int) (domain.PhoneCall, bool, error) {
	return s.DiscardCallWithSlug(ctx, userID, callID, accessHash, reason, "", duration)
}

func (s *Service) DiscardCallWithSlug(ctx context.Context, userID, callID, accessHash int64, reason domain.PhoneCallDiscardReason, reasonSlug string, duration int) (domain.PhoneCall, bool, error) {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	e, err := s.lookupLocked(callID, accessHash)
	if err != nil {
		return domain.PhoneCall{}, false, err
	}
	if !e.call.HasParticipant(userID) {
		return domain.PhoneCall{}, false, ErrPeerInvalid
	}
	if e.call.Terminal() {
		return e.call, true, nil
	}
	if reason == "" {
		reason = domain.PhoneCallDiscardReasonHangup
	}
	if reason != domain.PhoneCallDiscardReasonMigrateConference {
		reasonSlug = ""
	}
	// duration is only recognized once the call is actually established (Confirmed), preventing the client from reporting the ringing time as call duration.
	if e.call.StartDate == 0 || duration < 0 {
		duration = 0
	}
	s.reg.markDiscardedWithSlugLocked(e, reason, reasonSlug, duration, int(s.clk.Now().Unix()))
	return e.call, false, nil
}

// ExpireDue moves timed-out non-terminal calls into a terminal state and returns snapshots
// (the caller is responsible for pushing and persisting history): Requested/Ringing timeout →
// missed (the source of "missed call"); a dangling Accepted (caller never confirms) →
// disconnect. Confirmed calls have no server-side duration limit and are not reclaimed here.
// It also does tombstone GC along the way.
func (s *Service) ExpireDue(ctx context.Context, now time.Time) []domain.PhoneCall {
	nowUnix := now.Unix()
	ringSec := int64(s.cfg.RingTimeout / time.Second)
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	var expired []domain.PhoneCall
	for _, e := range s.reg.byID {
		if e.call.Terminal() || e.call.State == domain.PhoneCallStateConfirmed {
			continue
		}
		if nowUnix-int64(e.call.Date) <= ringSec {
			continue
		}
		reason := domain.PhoneCallDiscardReasonMissed
		if e.call.State == domain.PhoneCallStateAccepted {
			reason = domain.PhoneCallDiscardReasonDisconnect
		}
		s.reg.markDiscardedLocked(e, reason, 0, int(nowUnix))
		expired = append(expired, e.call)
	}
	s.reg.sweepTombstonesLocked(nowUnix, int64(s.cfg.TombstoneTTL/time.Second))
	return expired
}

// Signal validates and serially forwards one piece of signaling. forward runs inside the
// call-specific signaling order lock (guaranteeing the forwarding order matches the receipt
// order); the state lock is not held across forward. peerDevice is the peer's receiving-device
// anchor (may be zero/stale) and only serves as a targeted-push fast-path hint. drop=true
// means it is silently swallowed per contract (tombstone tail packet / over the rate cap),
// and the caller should return success.
func (s *Service) Signal(ctx context.Context, userID, callID, accessHash int64, forward func(peerUserID int64, peerDevice domain.SessionRef)) (drop bool, err error) {
	s.reg.mu.Lock()
	e, lookupErr := s.lookupLocked(callID, accessHash)
	if lookupErr != nil {
		s.reg.mu.Unlock()
		return false, lookupErr
	}
	if !e.call.HasParticipant(userID) {
		s.reg.mu.Unlock()
		return false, ErrPeerInvalid
	}
	state := e.call.State
	peer := e.call.PeerOf(userID)
	peerDevice := e.call.CalleeDevice
	if peer == e.call.AdminID {
		peerDevice = e.call.CallerDevice
	}
	s.reg.mu.Unlock()

	switch state {
	case domain.PhoneCallStateAccepted, domain.PhoneCallStateConfirmed:
		// Forwardable. DrKLO may send signaling before or after confirm, so Accepted is let through.
	case domain.PhoneCallStateDiscarded:
		// Tail packet at the moment of hangup: returning an error would make TDesktop render a
		// normal hangup as "call failed" (its sendSignalingData's .done checks mtpIsTrue), so
		// it is silently dropped.
		return true, nil
	default:
		return false, ErrPeerInvalid
	}

	e.sigMu.Lock()
	defer e.sigMu.Unlock()
	nowSec := s.clk.Now().Unix()
	if e.sigWindowSec != nowSec {
		e.sigWindowSec = nowSec
		e.sigCount = 0
	}
	if e.sigCount >= s.cfg.SignalingRatePerSecond {
		return true, nil
	}
	e.sigCount++
	forward(peer, peerDevice)
	return false, nil
}

// Lookup returns a call snapshot (used by the rpc layer's lenient checks for late requests such as setCallRating/saveCallDebug).
func (s *Service) Lookup(ctx context.Context, callID, accessHash int64) (domain.PhoneCall, bool) {
	s.reg.mu.Lock()
	defer s.reg.mu.Unlock()
	e, err := s.lookupLocked(callID, accessHash)
	if err != nil {
		return domain.PhoneCall{}, false
	}
	return e.call, true
}

func (s *Service) lookupLocked(callID, accessHash int64) (*entry, error) {
	e, ok := s.reg.byID[callID]
	if !ok || e.call.AccessHash != accessHash {
		return nil, ErrPeerInvalid
	}
	return e, nil
}

func validateProtocol(p domain.PhoneCallProtocol) error {
	if p.MinLayer > p.MaxLayer {
		return ErrProtocolLayerInvalid
	}
	if p.MaxLayer < minSupportedLayer {
		return ErrProtocolCompatLayerInvalid
	}
	if !p.UDPP2P && !p.UDPReflector {
		return ErrProtocolFlagsInvalid
	}
	if len(p.LibraryVersions) == 0 {
		return ErrProtocolFlagsInvalid
	}
	return nil
}

// negotiateProtocol merges both protocols at accept (official semantics: the server returns
// only the single "best" library version). When the versions do not intersect it passes
// through the callee's list (the callee is the side that instantiates tgcalls first, and the
// caller side has kDefaultVersion as a fallback) — it never rejects a call over a version
// difference.
func negotiateProtocol(caller, callee domain.PhoneCallProtocol) (domain.PhoneCallProtocol, error) {
	out := domain.PhoneCallProtocol{
		UDPP2P:       caller.UDPP2P && callee.UDPP2P,
		UDPReflector: caller.UDPReflector || callee.UDPReflector,
		MinLayer:     maxInt(caller.MinLayer, callee.MinLayer),
		MaxLayer:     minInt(caller.MaxLayer, callee.MaxLayer),
	}
	if out.MinLayer > out.MaxLayer {
		return domain.PhoneCallProtocol{}, ErrProtocolCompatLayerInvalid
	}
	if best, ok := bestCommonVersion(caller.LibraryVersions, callee.LibraryVersions); ok {
		out.LibraryVersions = []string{best}
	} else {
		out.LibraryVersions = append([]string(nil), callee.LibraryVersions...)
	}
	return out, nil
}

// preferredVersions is the priority order when the intersection hits (a hard constraint
// derived from reading client source):
//   - "9.0.0"=InstanceV2Impl+V2 message-level signaling, present in both the TDesktop and
//     DrKLO registries, the most stable;
//   - ⚠ Never choose "10.0.0"/"11.0.0"/"12.0.0"/"13.0.0" as [0]: DrKLO's video availability
//     check is a **string lexicographic** comparison of
//     `"2.7.7".compareTo(versions[0]) <= 0` (VoIPService.java:3464); "1x.0.0" being
//     lexicographically less than "2.7.7" makes Android tear down the camera capture
//     outright (black video call); "12/13" are also V3 SCTP-over-signaling, which is
//     unfriendly to server-side signaling rate limiting;
//   - Never choose "2.7.7"/"5.0.0"/"2.4.4" either: the legacy implementation depends on
//     reflector endpoints we do not send, leaving no path.
var preferredVersions = []string{"9.0.0", "8.0.0", "7.0.0"}

// bestCommonVersion takes the intersection of both version sets: it prefers a hit in
// preferredVersions order, otherwise it falls back to the semantically highest (tolerating
// future unknown version sets).
func bestCommonVersion(a, b []string) (string, bool) {
	inB := make(map[string]struct{}, len(b))
	for _, v := range b {
		inB[v] = struct{}{}
	}
	inBoth := make(map[string]struct{}, len(a))
	for _, v := range a {
		if _, ok := inB[v]; ok {
			inBoth[v] = struct{}{}
		}
	}
	for _, v := range preferredVersions {
		if _, ok := inBoth[v]; ok {
			return v, true
		}
	}
	best, found := "", false
	for v := range inBoth {
		if !found || compareVersion(v, best) > 0 {
			best, found = v, true
		}
	}
	return best, found
}

// compareVersion compares dot-separated decimal components ("9.0.0" < "10.0.0"); a non-numeric component falls back to string comparison.
func compareVersion(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		an, aerr := strconv.Atoi(av)
		bn, berr := strconv.Atoi(bv)
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				return an - bn
			}
		default:
			if c := strings.Compare(av, bv); c != 0 {
				return c
			}
		}
	}
	return 0
}

func sha256Mismatch(data, want []byte) bool {
	got := sha256.Sum256(data)
	return !bytes.Equal(got[:], want)
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
