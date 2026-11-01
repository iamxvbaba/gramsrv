// Package secretchat implements the handshake state machine for private end-to-end
// encrypted chats (Secret Chat / EncryptedChat). The server is a blind relay:
// g_a/g_b/key_fingerprint/encrypted bytes are all stored opaquely and forwarded
// verbatim; the only point involved in cryptography is the DH range boundary check on
// g_a/g_b (to prevent weak DH/MITM). The shared key and plaintext are the E2E client's
// responsibility; the server neither knows nor can compute them.
// See docs/secret-chat-module.md for the design.
package secretchat

import (
	"errors"
	"math/big"

	appphone "telesrv/internal/app/phone"
)

// dhPubSize is the canonical byte length of g_a/g_b (2048-bit).
const dhPubSize = 256

// ErrGAInvalid: g_a/g_b is outside the valid DH range → the rpc layer maps it to DH_G_A_INVALID.
var ErrGAInvalid = errors.New("secretchat: dh parameter invalid")

var (
	// dhPrimeMinusOne/boundary values reuse the phone domain's official 2048-bit safe prime (same source as the p sent by getDhConfig).
	dhPrimeMinusOne = new(big.Int).Sub(new(big.Int).SetBytes(appphone.DHPrime()), big.NewInt(1))
	// 2^(2048-64) lower bound and p-2^(2048-64) upper bound (same bounds as the official isGoodPrime).
	dhLowerBound = new(big.Int).Lsh(big.NewInt(1), 2048-64)
	dhUpperBound = new(big.Int).Sub(new(big.Int).SetBytes(appphone.DHPrime()), new(big.Int).Lsh(big.NewInt(1), 2048-64))
	dhOne        = big.NewInt(1)
)

// validateDHParam performs range validation on g_a/g_b and, on success, returns it
// left-padded with zeros to 256 bytes (canonical wire format; a valid g_a whose leading
// zero byte was trimmed must not be wrongly rejected). Validation uses the big.Int value,
// independent of the zero padding.
func validateDHParam(g []byte) ([]byte, error) {
	if len(g) == 0 || len(g) > dhPubSize {
		return nil, ErrGAInvalid
	}
	x := new(big.Int).SetBytes(g)
	// 1 < x < p-1 and 2^(2048-64) < x < p-2^(2048-64)
	if x.Cmp(dhOne) <= 0 || x.Cmp(dhPrimeMinusOne) >= 0 {
		return nil, ErrGAInvalid
	}
	if x.Cmp(dhLowerBound) <= 0 || x.Cmp(dhUpperBound) >= 0 {
		return nil, ErrGAInvalid
	}
	return leftPad256(g), nil
}

// leftPad256 left-pads with zeros to 256 bytes (the input is guaranteed ≤256).
func leftPad256(b []byte) []byte {
	if len(b) == dhPubSize {
		return append([]byte(nil), b...)
	}
	out := make([]byte, dhPubSize)
	copy(out[dhPubSize-len(b):], b)
	return out
}
