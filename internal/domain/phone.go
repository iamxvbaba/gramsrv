package domain

// PhoneCallState is the server state of the private 1:1 call signaling state machine.
// State transitions, TL perspective mapping and push strategy are in docs and internal/app/phone.
type PhoneCallState string

const (
	// PhoneCallStateRequested: requestCall has been accepted and phoneCallRequested has been pushed to the callee.
	PhoneCallStateRequested PhoneCallState = "requested"
	// PhoneCallStateRinging: at least one callee device reported receivedCall (the caller switches to the ringing UI based on receive_date).
	PhoneCallStateRinging PhoneCallState = "ringing"
	// PhoneCallStateAccepted: the callee's acceptCall has stored g_b, waiting for the caller's confirmCall.
	PhoneCallStateAccepted PhoneCallState = "accepted"
	// PhoneCallStateConfirmed: the caller's confirmCall completed the key exchange and the call is in progress.
	PhoneCallStateConfirmed PhoneCallState = "confirmed"
	// PhoneCallStateDiscarded: terminal state. Kept as a tombstone for a while to absorb late RPCs before being reclaimed.
	PhoneCallStateDiscarded PhoneCallState = "discarded"
)

// PhoneCallDiscardReason is the hangup reason, one-to-one with TL phoneCallDiscardReason*.
type PhoneCallDiscardReason string

const (
	PhoneCallDiscardReasonMissed     PhoneCallDiscardReason = "missed"
	PhoneCallDiscardReasonDisconnect PhoneCallDiscardReason = "disconnect"
	PhoneCallDiscardReasonHangup     PhoneCallDiscardReason = "hangup"
	PhoneCallDiscardReasonBusy       PhoneCallDiscardReason = "busy"
	// PhoneCallDiscardReasonMigrateConference is the migration hangup when upgrading to a conference call;
	// conference is currently unsupported and the reason is only echoed verbatim.
	PhoneCallDiscardReasonMigrateConference PhoneCallDiscardReason = "migrate_conference"
)

// PhoneCallProtocol is the tgcalls protocol negotiation parameters (TL phoneCallProtocol).
type PhoneCallProtocol struct {
	UDPP2P          bool
	UDPReflector    bool
	MinLayer        int
	MaxLayer        int
	LibraryVersions []string
}

// SessionRef identifies one specific device connection (the anchor for the signaling targeted-push fast path; allowed to go stale).
type SessionRef struct {
	RawAuthKeyID [8]byte
	SessionID    int64
}

// Zero reports whether the anchor is unrecorded.
func (s SessionRef) Zero() bool {
	return s == SessionRef{}
}

// PhoneCallConnection is a WebRTC STUN/TURN server entry issued to both sides
// (TL phoneConnectionWebrtc). Issued when requestCall is accepted and stable for the whole lifetime.
type PhoneCallConnection struct {
	ID       int64
	IP       string
	Port     int
	Username string
	Password string
	Stun     bool
	Turn     bool
}

// PhoneCallRequest is the acceptance input of phone.requestCall (privacy/block checks run first in the rpc layer).
type PhoneCallRequest struct {
	CalleeID     int64
	RandomID     int64
	GAHash       []byte
	Video        bool
	Protocol     PhoneCallProtocol
	CallerDevice SessionRef
	// PrivacyP2P is the two-way AND of phone_p2p privacy (computed by the rpc layer; set false when relay is forced).
	PrivacyP2P bool
	// Connections is the STUN/TURN list issued for this call (may be empty: when TURN is disabled it falls back to
	// a LAN direct connection that exchanges host candidates through pure signaling).
	Connections []PhoneCallConnection
}

// PhoneCall is the server-authoritative state of one private call. Key material (GAHash/GB/GA) lives only in an
// in-process registry and is destroyed with tombstone reclamation, never persisted to any storage.
type PhoneCall struct {
	ID         int64
	AccessHash int64
	// AdminID is the caller and ParticipantID the callee; named the same as the TL phoneCall fields and unchanged for the whole lifetime.
	AdminID       int64
	ParticipantID int64
	Video         bool
	State         PhoneCallState

	Date        int // requestCall acceptance time (unix seconds)
	ReceiveDate int // time of the first callee device's receivedCall; 0 means not yet reached
	StartDate   int // confirmCall completion time
	DiscardedAt int // time of entering the terminal state (basis for tombstone GC)

	GAHash         []byte // 32B, the commitment carried by requestCall
	GB             []byte // 256B, carried by acceptCall
	GA             []byte // 256B, revealed by confirmCall (the server verifies SHA256(GA)==GAHash)
	KeyFingerprint int64  // E2E fingerprint the server cannot verify and only relays

	// Protocol is the negotiated result (computed at accept); CallerProtocol/CalleeProtocol keep the two originals.
	Protocol       PhoneCallProtocol
	CallerProtocol PhoneCallProtocol
	CalleeProtocol PhoneCallProtocol

	P2PAllowed    bool
	DiscardReason PhoneCallDiscardReason
	// DiscardReasonSlug is set for migrate_conference and must resolve through
	// phone.getGroupCall(inputGroupCallSlug) before the discarded update is sent.
	DiscardReasonSlug string
	Duration          int

	// PrivacyP2P and Connections are preserved verbatim from PhoneCallRequest (see its comment).
	PrivacyP2P  bool
	Connections []PhoneCallConnection

	RandomID int64 // half of the caller-side idempotent dedup key (callerID+RandomID)

	// Device anchors: the originating device of requestCall / acceptCall, used only as a hint for targeted push.
	CallerDevice SessionRef
	CalleeDevice SessionRef
}

// Terminal reports whether the call has entered the terminal state.
func (c PhoneCall) Terminal() bool {
	return c.State == PhoneCallStateDiscarded
}

// PeerOf returns userID's peer in this call; returns 0 when userID is not a participant.
func (c PhoneCall) PeerOf(userID int64) int64 {
	switch userID {
	case c.AdminID:
		return c.ParticipantID
	case c.ParticipantID:
		return c.AdminID
	default:
		return 0
	}
}

// HasParticipant reports whether userID is a participant in this call.
func (c PhoneCall) HasParticipant(userID int64) bool {
	return userID != 0 && (userID == c.AdminID || userID == c.ParticipantID)
}
