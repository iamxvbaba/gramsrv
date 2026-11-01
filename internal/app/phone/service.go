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
	ErrMissingActiveCallStore     = errors.New("phone: active call store is required")
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
	// MaxRegistryEntries is the hard cap of the active-call store. At the cap new calls are
	// refused rather than evicting Confirmed calls that may still be in progress.
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

// ActiveCallStore is the multi-Core state boundary for short-lived private call
// signaling state. Production Core must inject a shared implementation.
type ActiveCallStore interface {
	RequestCall(ctx context.Context, callerID int64, in domain.PhoneCallRequest, now time.Time, cfg Config) (domain.PhoneCall, error)
	ReceivedCall(ctx context.Context, userID, callID, accessHash int64, now time.Time) (domain.PhoneCall, bool, error)
	AcceptCall(ctx context.Context, userID, callID, accessHash int64, gb []byte, proto domain.PhoneCallProtocol, device domain.SessionRef) (domain.PhoneCall, error)
	ConfirmCall(ctx context.Context, userID, callID, accessHash int64, ga []byte, keyFingerprint int64, proto domain.PhoneCallProtocol, now time.Time, cfg Config) (domain.PhoneCall, bool, error)
	DiscardCallWithSlug(ctx context.Context, userID, callID, accessHash int64, reason domain.PhoneCallDiscardReason, reasonSlug string, duration int, now time.Time, cfg Config) (domain.PhoneCall, bool, error)
	ExpireDue(ctx context.Context, now time.Time, cfg Config) []domain.PhoneCall
	Signal(ctx context.Context, userID, callID, accessHash int64, now time.Time, rateLimit int, forward func(peerUserID int64, peerDevice domain.SessionRef)) (bool, error)
	Lookup(ctx context.Context, callID, accessHash int64) (domain.PhoneCall, bool)
}

// Service implements the private call signaling state machine. Every domain.PhoneCall returned by its methods is a snapshot at that moment.
type Service struct {
	cfg   Config
	clk   clock.Clock
	calls ActiveCallStore
}

// Option configures Service.
type Option func(*Service)

// WithClock injects a test clock.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) { s.clk = clk }
}

// NewService creates the call service.
func NewService(cfg Config, calls ActiveCallStore, opts ...Option) (*Service, error) {
	if calls == nil {
		return nil, ErrMissingActiveCallStore
	}
	s := &Service{cfg: cfg.withDefaults(), clk: clock.System, calls: calls}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
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
	return s.calls.RequestCall(ctx, callerID, in, s.clk.Now(), s.cfg)
}

// ReceivedCall marks that the callee device has received the incoming call. The first call
// (Requested→Ringing) returns transitioned=true; other states succeed idempotently (multiple
// devices each report, and late arrivals are harmless).
func (s *Service) ReceivedCall(ctx context.Context, userID, callID, accessHash int64) (domain.PhoneCall, bool, error) {
	return s.calls.ReceivedCall(ctx, userID, callID, accessHash, s.clk.Now())
}

// AcceptCall handles the callee answering. Concurrent races across devices/Cores are
// serialized by the active-call store: the first to complete the transition wins, and later
// ones get ErrAlreadyAccepted (their UI winds down on its own).
func (s *Service) AcceptCall(ctx context.Context, userID, callID, accessHash int64, gb []byte, proto domain.PhoneCallProtocol, device domain.SessionRef) (domain.PhoneCall, error) {
	if err := validateProtocol(proto); err != nil {
		return domain.PhoneCall{}, err
	}
	if len(gb) != dhPubSize || allZero(gb) {
		return domain.PhoneCall{}, ErrProtocolFlagsInvalid
	}
	return s.calls.AcceptCall(ctx, userID, callID, accessHash, gb, proto, device)
}

// ConfirmCall accepts the caller's confirmation: after verifying SHA256(g_a) matches the
// commitment it enters Confirmed. On verification failure the call is forced into
// discarded(disconnect), returning (terminal snapshot, true, ErrGAHashMismatch); the caller
// must push the terminal state to both sides.
func (s *Service) ConfirmCall(ctx context.Context, userID, callID, accessHash int64, ga []byte, keyFingerprint int64, proto domain.PhoneCallProtocol) (domain.PhoneCall, bool, error) {
	return s.calls.ConfirmCall(ctx, userID, callID, accessHash, ga, keyFingerprint, proto, s.clk.Now(), s.cfg)
}

// DiscardCall hangs up: reachable from any non-terminal state, idempotent. already=true
// means the call was already terminal (simultaneous hangups: the first sets the reason, the
// later one gets the snapshot).
func (s *Service) DiscardCall(ctx context.Context, userID, callID, accessHash int64, reason domain.PhoneCallDiscardReason, duration int) (domain.PhoneCall, bool, error) {
	return s.DiscardCallWithSlug(ctx, userID, callID, accessHash, reason, "", duration)
}

func (s *Service) DiscardCallWithSlug(ctx context.Context, userID, callID, accessHash int64, reason domain.PhoneCallDiscardReason, reasonSlug string, duration int) (domain.PhoneCall, bool, error) {
	if reason == "" {
		reason = domain.PhoneCallDiscardReasonHangup
	}
	if reason != domain.PhoneCallDiscardReasonMigrateConference {
		reasonSlug = ""
	}
	return s.calls.DiscardCallWithSlug(ctx, userID, callID, accessHash, reason, reasonSlug, duration, s.clk.Now(), s.cfg)
}

// ExpireDue moves timed-out non-terminal calls to a terminal state and returns snapshots (the
// caller pushes and records history): a Requested/Ringing timeout → missed (the source of
// "missed call"); a hanging Accepted call (the caller never confirms) → disconnect. Confirmed
// calls have no server-side duration limit and are not reclaimed here. The concrete store
// owns the terminal tombstone lifecycle.
func (s *Service) ExpireDue(ctx context.Context, now time.Time) []domain.PhoneCall {
	return s.calls.ExpireDue(ctx, now, s.cfg)
}

// Signal validates and forwards one signaling message in order. forward runs inside the
// call's own ordering boundary (so forwarding order matches acceptance order). peerDevice is
// the peer's accepting-device anchor (possibly zero/stale), used only as a fast-path hint for
// targeted pushes. drop=true means it is silently swallowed by contract (a tombstone tail
// packet / over the rate limit), and the caller should return success.
func (s *Service) Signal(ctx context.Context, userID, callID, accessHash int64, forward func(peerUserID int64, peerDevice domain.SessionRef)) (drop bool, err error) {
	return s.calls.Signal(ctx, userID, callID, accessHash, s.clk.Now(), s.cfg.SignalingRatePerSecond, forward)
}

// Lookup returns a call snapshot (used by the rpc layer's lenient checks for late requests such as setCallRating/saveCallDebug).
func (s *Service) Lookup(ctx context.Context, callID, accessHash int64) (domain.PhoneCall, bool) {
	return s.calls.Lookup(ctx, callID, accessHash)
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
