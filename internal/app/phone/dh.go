// Package phone implements the signaling state machine and DH parameter delivery for
// private 1:1 calls.
//
// Server responsibility boundary: signaling relay, state machine, commit-reveal
// verification (SHA256(g_a)==g_a_hash), and connections delivery. The media plane runs
// through client tgcalls over P2P/TURN; key exchange is E2E, so the server neither knows
// nor can verify the shared key itself.
package phone

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// DHConfigVersion is the server-side config version for messages.getDhConfig. p/g are
// compile-time constants; on a client cache hit (same requested version) only
// dhConfigNotModified{random} is returned.
//
// Version 1 was the placeholder used before server-ready and cannot be reused: TDLib/DrKLO
// persist version together with p/g; if a private-DC client carries a cache that is also 1
// but comes from a different config profile, a server mistakenly returning NotModified makes
// the two secret-chat ends use different p/g, showing as the receiver Ready while the
// initiator immediately Closed due to a key fingerprint mismatch. This version must be
// incremented again whenever p/g changes.
const DHConfigVersion = 20260811

// DHG is the DH generator. It is 3, matching the official: TDesktop MTP::IsPrimeAndGood
// has a whitelist fast path for "official 2048-bit prime + g∈{3,4,5,7}", and DrKLO native
// is the same.
const DHG = 3

// dhPrimeHex is the official 2048-bit safe prime. Same value as baseP in
// internal/app/account/srp.go (SRP and call DH share the official parameters);
// changing either one requires syncing the other.
const dhPrimeHex = "c71caeb9c6b1c9048e6c522f70f13f73980d40238e3e21c14934d037563d930f48198a0aa7c14058229493d22530f4dbfa336f6e0ac925139543aed44cce7c3720fd51f69458705ac68cd4fe6b6b13abdc9746512969328454f18faf8c595f642477fe96bb2a941d5bcd1d4ac8cc49880708fa9b378e3c4f3a9060bee67cf9a4a4a695811051907e162753b56b0f6b410dba74d8a84b2a14b3144e0ef1284754fd17ed950d5965b4b9dd46582db1178d169c6bc465b0d6ff9ca3928fef5b9ae4e418fc15e83ebea0f87fa9ff5eed70050ded2849f47bf959d956850ce929851f0d8115f635b105ee2e4e15d04b2454bf6f4fadf034b10403119cd8e3b92fcc5b"

var dhPrime = mustDecodeHex(dhPrimeHex)

// maxDHRandomLength clamps the number of random bytes a client can request, defending against maliciously large requests.
const maxDHRandomLength = 1024

// DHPrime returns a copy of the official 2048-bit prime.
func DHPrime() []byte {
	return append([]byte(nil), dhPrime...)
}

// DHRandom generates exactly n bytes of cryptographic random data; n is clamped to
// [0, maxDHRandomLength]. The client contract requires the random size to match the
// request (TDesktop verifies random.size() equals its request).
func DHRandom(n int) ([]byte, error) {
	if n < 0 {
		n = 0
	}
	if n > maxDHRandomLength {
		n = maxDHRandomLength
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("phone: dh random: %w", err)
	}
	return buf, nil
}

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(fmt.Sprintf("phone: invalid dh prime hex: %v", err))
	}
	return b
}
