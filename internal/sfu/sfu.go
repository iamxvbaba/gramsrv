// Package sfu is the interface and implementation of the group-call media plane
// (Selective Forwarding Unit).
//
// Role contract (hard requirements from tgcalls GroupNetworkManager): the client is
// ICE=CONTROLLED and DTLS setup="passive" (waits for the handshake); the SFU must run
// full-ICE CONTROLLING (ICE-Lite is not usable) and actively initiate the handshake as
// a DTLS client (setup="active"). The upstream join JSON carries no ICE candidates, so
// the SFU can only learn the client address (peer-reflexive) from a STUN Binding Request
// authenticated with ufrag/pwd.
//
// M0 provides the Disabled implementation (signaling-only bring-up): it delivers
// syntactically complete ufrag/pwd/sha-256 fingerprint and empty candidates — the client
// parses them successfully and stays in the Connecting state (with a 4s checkGroupCall
// heartbeat), which is expected; after M1 swaps in the pion implementation the client
// switches over transparently.
package sfu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// SsrcGroup is an ssrc-groups entry of the upstream join JSON: semantics ∈ {"SIM","FID"}.
// A SIM's sources are ordered from lowest to highest quality; a FID is a
// [media ssrc, RTX retransmission ssrc] pair.
type SsrcGroup struct {
	Semantics string
	Sources   []uint32
}

// EndpointKind distinguishes a participant's two media connections (M4: screen sharing
// is a separate second connection with its own ICE/DTLS/ssrc set, but belongs to the same
// participant at the TL layer).
type EndpointKind int

const (
	EndpointMain EndpointKind = iota
	EndpointPresentation
)

// ClientOffer is parsed from the upstream params JSON of phone.joinGroupCall / joinGroupCallPresentation.
type ClientOffer struct {
	AudioSSRC         uint32
	Ufrag             string
	Pwd               string
	FingerprintSHA256 string // of the form "AA:BB:..." (RFC 4572 hex)
	// SsrcGroups are the video simulcast/RTX source groups (present on the main join
	// regardless of camera state). The SFU uses them for layer selection: the subscriber
	// creates a decode sink only for SIM[0] (the lowest layer), and packets sent to the
	// raw ssrc of other layers are silently dropped by the client — the v1 forwarding
	// policy is to forward only the SIM[0] layer and its FID RTX partner, dropping the
	// remaining layers at the SFU (saves downlink and behaves correctly).
	SsrcGroups []SsrcGroup
}

// Candidate is an ICE candidate delivered to the client (a transport.candidates entry
// of the updateGroupCallConnection JSON; field semantics copied from the tgcalls
// parsing code).
type Candidate struct {
	IP       string
	Port     int
	Protocol string // "udp"
	Type     string // "host"
}

// ServerAnswer holds the SFU-side transport parameters, assembled into the downstream JSON by the rpc layer.
type ServerAnswer struct {
	Ufrag             string
	Pwd               string
	FingerprintSHA256 string
	Candidates        []Candidate
}

// Service is the boundary between the signaling layer and the media plane (the
// replacement point for future horizontal scaling / remote SFUs).
type Service interface {
	// Enabled reports whether the media plane is actually available (false = M0 signaling-only mode).
	Enabled() bool
	// Join allocates/rebuilds a participant's media endpoint and returns the SFU-side transport parameters.
	// With kind=EndpointPresentation it is the participant's second connection (separate ICE/DTLS).
	Join(ctx context.Context, callID, userID int64, kind EndpointKind, offer ClientOffer) (ServerAnswer, error)
	// Leave tears down a participant's endpoint; with kind=EndpointMain it tears down the
	// presentation alongside it (a client leaving the call as a whole never sends a
	// follow-up leaveGroupCallPresentation).
	Leave(ctx context.Context, callID, userID int64, kind EndpointKind) error
	CloseRoom(ctx context.Context, callID int64) error
	// AliveUserIDs returns the participants in the callID room whose media plane is still
	// alive (ICE consent / recent SRTP receive). The sweeper's death condition = heartbeat
	// expired ∧ media plane not alive (double expiry).
	AliveUserIDs(callID int64) []int64
}

// disabled is the M0 signaling-only implementation.
type disabled struct{}

// Disabled returns a signaling-only SFU.
func Disabled() Service {
	return disabled{}
}

func (disabled) Enabled() bool { return false }

func (disabled) Join(_ context.Context, _, _ int64, _ EndpointKind, _ ClientOffer) (ServerAnswer, error) {
	ufrag, err := randomICEString(8)
	if err != nil {
		return ServerAnswer{}, err
	}
	pwd, err := randomICEString(24)
	if err != nil {
		return ServerAnswer{}, err
	}
	fp, err := randomFingerprint()
	if err != nil {
		return ServerAnswer{}, err
	}
	// Empty candidates: the client has no reachable address and stays Connecting (expected for M0).
	return ServerAnswer{Ufrag: ufrag, Pwd: pwd, FingerprintSHA256: fp, Candidates: nil}, nil
}

func (disabled) Leave(context.Context, int64, int64, EndpointKind) error { return nil }
func (disabled) CloseRoom(context.Context, int64) error                  { return nil }
func (disabled) AliveUserIDs(int64) []int64                              { return nil }

const iceAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomICEString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("sfu: random ice string: %w", err)
	}
	var b strings.Builder
	for _, v := range buf {
		b.WriteByte(iceAlphabet[int(v)%len(iceAlphabet)])
	}
	return b.String(), nil
}

// randomFingerprint generates a syntactically valid sha-256 fingerprint string
// (32 bytes hex, colon-separated). M0 has no DTLS endpoint, so the fingerprint value
// is never verified (the client cannot connect to any candidate).
func randomFingerprint() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("sfu: random fingerprint: %w", err)
	}
	parts := make([]string, len(buf))
	for i, v := range buf {
		parts[i] = strings.ToUpper(hex.EncodeToString([]byte{v}))
	}
	return strings.Join(parts, ":"), nil
}
