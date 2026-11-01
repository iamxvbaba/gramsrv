package rpc

import (
	"context"
	"strconv"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Private-call P3: STUN/TURN relay parameters (phoneConnectionWebrtc) and the p2p_allowed truth.
// Credentials are issued once when requestCall is accepted and stored in the call snapshot — every
// perspective of the same call (RPC response and push, caller and callee) sees the same connections,
// consistent with official behavior.

// phoneP2PGate is the reciprocal-contact hard gate provided by the privacy service. When the
// privacy service is absent or does not implement this interface, P2P is always denied (a secure
// default, fail closed): P2P exposes both parties' real IPs, so it is better to route through the
// TURN relay than to hand them to a stranger.
type phoneP2PGate interface {
	P2PAllowedBetween(ctx context.Context, callerID, calleeID int64) (bool, error)
}

// phoneCallPrivacyP2P decides whether a private call can connect P2P directly. Secure default: P2P
// is allowed only between reciprocal contacts (the privacy service ANDs both sides' phone_p2p rules
// and requires the contact relationship to be mutual); when the contact relationship cannot be
// confirmed, P2P is denied (with p2p_allowed=false tgcalls drops every non-relay candidate). The old
// implementation failed open (P1 compatibility): a missing/erroring privacy service defaulted to
// allow — an IP-leak source, now changed to fail closed. CallForceRelay still forces full relay
// (for debugging).
func (r *Router) phoneCallPrivacyP2P(ctx context.Context, callerID, calleeID int64) bool {
	if r.cfg.CallForceRelay {
		return false
	}
	gate, ok := r.deps.Privacy.(phoneP2PGate)
	if !ok {
		r.log.Warn("phone p2p denied: privacy service lacks reciprocal-contact gate")
		return false
	}
	allowed, err := gate.P2PAllowedBetween(ctx, callerID, calleeID)
	if err != nil {
		r.log.Warn("phone p2p denied: reciprocal-contact gate error", zap.Error(err))
		return false
	}
	return allowed
}

// phoneCallConnections issues STUN/TURN entries for a call. When TURN is not enabled it returns an
// empty list (tgcalls iterates the empty list zero times and falls back to pure signaling exchanging
// host candidates for a LAN-direct connection).
func (r *Router) phoneCallConnections(callerID int64) []domain.PhoneCallConnection {
	t := r.deps.TURN
	if t == nil || !t.Enabled() {
		return nil
	}
	username, password, err := t.Credentials(strconv.FormatInt(callerID, 10))
	if err != nil {
		r.log.Warn("phone call turn credentials", zap.Error(err))
		return nil
	}
	// ⚠ STUN and TURN must be split into two entries (consistent with official): DrKLO's JNI layer
	// never reads the stun flag — a single stun+turn entry on Android only produces a TURN server and
	// loses STUN (org_telegram_messenger_voip_Instance.cpp:848-884). TDesktop accepts both forms.
	// The TURN username is in REST format "<expiry>:<uid>", which naturally avoids the "reflector"
	// hijack reservation.
	return []domain.PhoneCallConnection{
		{ID: 1, IP: t.IP(), Port: t.Port(), Stun: true},
		{ID: 2, IP: t.IP(), Port: t.Port(), Username: username, Password: password, Turn: true},
	}
}
