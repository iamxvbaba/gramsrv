// Package webauthntest provides a pure-Go software authenticator that simulates a
// platform passkey device, generating WebAuthn-spec-compliant attestation
// (registration) and assertion (login).
//
// Together with the internal/webauthn under test it is "two independent
// implementations of the same spec": the authenticator constructs the bytes per
// the spec and signs with a real private key, and the server verifies per the
// spec. Passing e2e/unit tests means both sides agree on the spec. It is for test
// use only (imported by _test.go).
package webauthntest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"

	"github.com/fxamacker/cbor/v2"
)

const (
	flagUserPresent      = 0x01
	flagUserVerified     = 0x04
	flagAttestedCredData = 0x40
)

var b64 = base64.RawURLEncoding

// Authenticator is an ES256 (ECDSA P-256) software authenticator holding a key
// pair and a credential id, reusable for registration and multiple logins (the
// signature counter increments).
type Authenticator struct {
	priv      *ecdsa.PrivateKey
	credID    []byte
	aaguid    []byte
	signCount uint32
}

// New generates a new software authenticator (random ES256 key + random credential id).
func New() (*Authenticator, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	credID := make([]byte, 32)
	if _, err := rand.Read(credID); err != nil {
		return nil, err
	}
	aaguid := make([]byte, 16) // all-zero aaguid (consistent with most platform passkeys)
	return &Authenticator{priv: priv, credID: credID, aaguid: aaguid}, nil
}

// CredentialID returns the raw credential id bytes.
func (a *Authenticator) CredentialID() []byte { return append([]byte(nil), a.credID...) }

// CredentialIDB64 returns the base64url (unpadded) credential id (corresponding to TL's ID/RawID string).
func (a *Authenticator) CredentialIDB64() string { return b64.EncodeToString(a.credID) }

type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

func (a *Authenticator) clientDataJSON(typ, origin string, challenge []byte) []byte {
	out, _ := json.Marshal(clientData{Type: typ, Challenge: b64.EncodeToString(challenge), Origin: origin})
	return out
}

func (a *Authenticator) cosePublicKey() []byte {
	x := make([]byte, 32)
	y := make([]byte, 32)
	a.priv.PublicKey.X.FillBytes(x)
	a.priv.PublicKey.Y.FillBytes(y)
	key := struct {
		Kty int    `cbor:"1,keyasint"`
		Alg int    `cbor:"3,keyasint"`
		Crv int    `cbor:"-1,keyasint"`
		X   []byte `cbor:"-2,keyasint"`
		Y   []byte `cbor:"-3,keyasint"`
	}{Kty: 2, Alg: -7, Crv: 1, X: x, Y: y}
	out, _ := cbor.Marshal(key)
	return out
}

func (a *Authenticator) authData(rpID string, flags byte, includeCred bool) []byte {
	h := sha256.Sum256([]byte(rpID))
	buf := make([]byte, 0, 128)
	buf = append(buf, h[:]...)
	buf = append(buf, flags)
	var counter [4]byte
	binary.BigEndian.PutUint32(counter[:], a.signCount)
	buf = append(buf, counter[:]...)
	if includeCred {
		buf = append(buf, a.aaguid...)
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(a.credID)))
		buf = append(buf, l[:]...)
		buf = append(buf, a.credID...)
		buf = append(buf, a.cosePublicKey()...)
	}
	return buf
}

// Register simulates createCredential, returning clientDataJSON and attestationObject (fmt=none).
func (a *Authenticator) Register(rpID, origin string, challenge []byte) (clientDataJSON, attestationObject []byte, err error) {
	cd := a.clientDataJSON("webauthn.create", origin, challenge)
	ad := a.authData(rpID, flagUserPresent|flagUserVerified|flagAttestedCredData, true)
	att := struct {
		Fmt      string         `cbor:"fmt"`
		AttStmt  map[string]any `cbor:"attStmt"`
		AuthData []byte         `cbor:"authData"`
	}{Fmt: "none", AttStmt: map[string]any{}, AuthData: ad}
	obj, err := cbor.Marshal(att)
	if err != nil {
		return nil, nil, err
	}
	return cd, obj, nil
}

// Assert simulates getCredential, returning clientDataJSON, authenticatorData and
// signature. Each call increments the signature counter by 1.
func (a *Authenticator) Assert(rpID, origin string, challenge []byte) (clientDataJSON, authenticatorData, signature []byte, err error) {
	a.signCount++
	cd := a.clientDataJSON("webauthn.get", origin, challenge)
	ad := a.authData(rpID, flagUserPresent|flagUserVerified, false)
	cdHash := sha256.Sum256(cd)
	signed := append(append([]byte(nil), ad...), cdHash[:]...)
	digest := sha256.Sum256(signed)
	sig, err := ecdsa.SignASN1(rand.Reader, a.priv, digest[:])
	if err != nil {
		return nil, nil, nil, err
	}
	return cd, ad, sig, nil
}
