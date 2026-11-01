// Package webauthn implements the WebAuthn/FIDO2 server primitives needed for
// telesrv passkey login: generating registration/assertion challenge options,
// verifying registration attestation, and verifying login assertions.
//
// It only handles bytes and JSON and does not depend on tg/domain/store -- which
// makes it easy to unit test standalone (together with the software authenticator
// in internal/webauthn/webauthntest). In the MTProto scenario the client
// base64url-decodes each WebAuthn field and packs it into TL, so this package
// takes raw bytes directly (clientDataJSON, authenticatorData, signature,
// attestationObject, credentialID).
//
// Supported algorithms: ES256 (ECDSA P-256, mainstream for platform passkeys)
// and EdDSA (Ed25519).
package webauthn

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"

	"github.com/fxamacker/cbor/v2"

	"telesrv/internal/branding"
)

// Verification errors. The caller maps these to TL error codes / rejects the login.
var (
	ErrClientDataInvalid    = errors.New("webauthn: client data invalid")
	ErrChallengeMismatch    = errors.New("webauthn: challenge mismatch")
	ErrOriginNotAllowed     = errors.New("webauthn: origin not allowed")
	ErrRPIDMismatch         = errors.New("webauthn: rp id hash mismatch")
	ErrUserNotPresent       = errors.New("webauthn: user-present flag not set")
	ErrAuthDataInvalid      = errors.New("webauthn: authenticator data invalid")
	ErrAttestationInvalid   = errors.New("webauthn: attestation object invalid")
	ErrPublicKeyUnsupported = errors.New("webauthn: unsupported public key algorithm")
	ErrSignatureInvalid     = errors.New("webauthn: signature invalid")
	ErrCounterRegressed     = errors.New("webauthn: sign counter regressed (possible cloned authenticator)")
)

// authenticatorData flag bits (WebAuthn §6.1).
const (
	flagUserPresent      = 0x01
	flagUserVerified     = 0x04
	flagAttestedCredData = 0x40
	flagExtensionData    = 0x80
)

// COSE algorithm identifiers.
const (
	coseAlgES256 = -7
	coseAlgEdDSA = -8
)

// COSE key type / curve.
const (
	coseKtyOKP     = 1
	coseKtyEC2     = 2
	coseCrvP256    = 1
	coseCrvEd25519 = 6
)

// b64 is the unpadded base64url encoding WebAuthn uses.
var b64 = base64.RawURLEncoding

// decodeB64URL tolerates base64url with or without padding.
func decodeB64URL(s string) ([]byte, error) {
	if out, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return out, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// RegistrationParams is the input needed to construct publicKey creation options.
type RegistrationParams struct {
	RPID          string
	RPName        string
	UserID        []byte // usually the bytes of "dcId:userId"
	UserName      string
	UserDisplay   string
	Challenge     []byte
	TimeoutMillis int
	ExcludeIDs    [][]byte // existing credential ids, to avoid re-registering the same authenticator
}

// BuildRegistrationOptions returns the DataJSON content of
// account.passkeyRegistrationOptions (top-level publicKey field, matching the
// convention DrKLO's PasskeysController reads).
func BuildRegistrationOptions(p RegistrationParams) ([]byte, error) {
	if p.RPID == "" || len(p.Challenge) == 0 || len(p.UserID) == 0 {
		return nil, errors.New("webauthn: incomplete registration params")
	}
	timeout := p.TimeoutMillis
	if timeout == 0 {
		timeout = 120000
	}
	exclude := make([]map[string]any, 0, len(p.ExcludeIDs))
	for _, id := range p.ExcludeIDs {
		exclude = append(exclude, map[string]any{"type": "public-key", "id": b64.EncodeToString(id)})
	}
	pub := map[string]any{
		"rp":        map[string]any{"id": p.RPID, "name": orDefault(p.RPName, branding.ProductName())},
		"user":      map[string]any{"id": b64.EncodeToString(p.UserID), "name": p.UserName, "displayName": orDefault(p.UserDisplay, p.UserName)},
		"challenge": b64.EncodeToString(p.Challenge),
		"pubKeyCredParams": []map[string]any{
			{"type": "public-key", "alg": coseAlgES256},
			{"type": "public-key", "alg": coseAlgEdDSA},
		},
		"timeout":     timeout,
		"attestation": "none",
		"authenticatorSelection": map[string]any{
			"residentKey":      "required",
			"userVerification": "preferred",
		},
		"excludeCredentials": exclude,
	}
	return json.Marshal(map[string]any{"publicKey": pub})
}

// LoginParams is the input for constructing publicKey request options (assertion challenge).
type LoginParams struct {
	RPID          string
	Challenge     []byte
	TimeoutMillis int
}

// BuildLoginOptions returns the DataJSON content of auth.passkeyLoginOptions. No
// allowCredentials (discoverable/usernameless login; the user is looked up from
// user_handle).
func BuildLoginOptions(p LoginParams) ([]byte, error) {
	if p.RPID == "" || len(p.Challenge) == 0 {
		return nil, errors.New("webauthn: incomplete login params")
	}
	timeout := p.TimeoutMillis
	if timeout == 0 {
		timeout = 120000
	}
	pub := map[string]any{
		"challenge":        b64.EncodeToString(p.Challenge),
		"timeout":          timeout,
		"rpId":             p.RPID,
		"userVerification": "preferred",
		"allowCredentials": []any{},
	}
	return json.Marshal(map[string]any{"publicKey": pub})
}

// clientData is the parsed structure of clientDataJSON.
type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

// verifyClientData validates type/challenge/origin. An empty allowedOrigins means
// origin is not strictly validated (dev: the server usually cannot know the
// Android apk-key-hash origin in advance).
func verifyClientData(clientDataJSON []byte, wantType string, challenge []byte, allowedOrigins []string) error {
	var cd clientData
	if err := json.Unmarshal(clientDataJSON, &cd); err != nil {
		return ErrClientDataInvalid
	}
	if cd.Type != wantType {
		return fmt.Errorf("%w: type=%q want %q", ErrClientDataInvalid, cd.Type, wantType)
	}
	got, err := decodeB64URL(cd.Challenge)
	if err != nil {
		return ErrClientDataInvalid
	}
	if subtle.ConstantTimeCompare(got, challenge) != 1 {
		return ErrChallengeMismatch
	}
	if len(allowedOrigins) > 0 {
		ok := false
		for _, o := range allowedOrigins {
			if o == cd.Origin {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%w: %q", ErrOriginNotAllowed, cd.Origin)
		}
	}
	return nil
}

// ChallengeFromClientData extracts the raw challenge bytes from clientDataJSON, for
// the caller to look up/consume the one-time challenge store by challenge (at this
// point it is not yet verified and is only used as a lookup key).
func ChallengeFromClientData(clientDataJSON []byte) ([]byte, error) {
	var cd clientData
	if err := json.Unmarshal(clientDataJSON, &cd); err != nil {
		return nil, ErrClientDataInvalid
	}
	b, err := decodeB64URL(cd.Challenge)
	if err != nil || len(b) == 0 {
		return nil, ErrClientDataInvalid
	}
	return b, nil
}

// parsedAuthData is the parsed result of authenticatorData.
type parsedAuthData struct {
	rpIDHash   []byte
	flags      byte
	signCount  uint32
	aaguid     []byte
	credID     []byte
	credPubKey []byte // raw COSE public key bytes (present only during registration)
}

func parseAuthData(authData []byte) (parsedAuthData, error) {
	if len(authData) < 37 {
		return parsedAuthData{}, ErrAuthDataInvalid
	}
	out := parsedAuthData{
		rpIDHash:  authData[0:32],
		flags:     authData[32],
		signCount: binary.BigEndian.Uint32(authData[33:37]),
	}
	rest := authData[37:]
	if out.flags&flagAttestedCredData != 0 {
		if len(rest) < 18 {
			return parsedAuthData{}, ErrAuthDataInvalid
		}
		out.aaguid = rest[0:16]
		credIDLen := int(binary.BigEndian.Uint16(rest[16:18]))
		rest = rest[18:]
		if len(rest) < credIDLen {
			return parsedAuthData{}, ErrAuthDataInvalid
		}
		out.credID = rest[:credIDLen]
		rest = rest[credIDLen:]
		// Then comes the COSE public key (variable-length CBOR); UnmarshalFirst consumes exactly one CBOR item.
		var raw cbor.RawMessage
		remaining, err := cbor.UnmarshalFirst(rest, &raw)
		if err != nil {
			return parsedAuthData{}, ErrAuthDataInvalid
		}
		out.credPubKey = rest[:len(rest)-len(remaining)]
	}
	return out, nil
}

func rpIDHashFor(rpID string) []byte {
	h := sha256.Sum256([]byte(rpID))
	return h[:]
}

// Credential is the credential material produced by registration verification, which the caller persists.
type Credential struct {
	ID            []byte // raw credential id bytes
	COSEPublicKey []byte // raw COSE public key bytes
	SignCount     uint32
	AAGUID        []byte
}

// VerifyRegistrationInput is the registration verification input.
type VerifyRegistrationInput struct {
	ClientDataJSON    []byte
	AttestationObject []byte
	RPID              string
	Challenge         []byte
	AllowedOrigins    []string
}

type attestationObject struct {
	Fmt      string          `cbor:"fmt"`
	AuthData []byte          `cbor:"authData"`
	AttStmt  cbor.RawMessage `cbor:"attStmt"`
}

// VerifyRegistration validates the attestation and extracts the credential public
// key. The attestation format only needs to yield the public key (consumer
// passkeys are generally "none"; the attestation statement signature is not
// verified).
func VerifyRegistration(in VerifyRegistrationInput) (*Credential, error) {
	if err := verifyClientData(in.ClientDataJSON, "webauthn.create", in.Challenge, in.AllowedOrigins); err != nil {
		return nil, err
	}
	var att attestationObject
	if err := cbor.Unmarshal(in.AttestationObject, &att); err != nil {
		return nil, ErrAttestationInvalid
	}
	ad, err := parseAuthData(att.AuthData)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(ad.rpIDHash, rpIDHashFor(in.RPID)) != 1 {
		return nil, ErrRPIDMismatch
	}
	if ad.flags&flagUserPresent == 0 {
		return nil, ErrUserNotPresent
	}
	if ad.flags&flagAttestedCredData == 0 || len(ad.credID) == 0 || len(ad.credPubKey) == 0 {
		return nil, ErrAttestationInvalid
	}
	// The public key parsing successfully is enough to consider it valid (none attestation).
	if _, err := parseCOSEKey(ad.credPubKey); err != nil {
		return nil, err
	}
	return &Credential{
		ID:            append([]byte(nil), ad.credID...),
		COSEPublicKey: append([]byte(nil), ad.credPubKey...),
		SignCount:     ad.signCount,
		AAGUID:        append([]byte(nil), ad.aaguid...),
	}, nil
}

// VerifyAssertionInput is the login assertion verification input.
type VerifyAssertionInput struct {
	COSEPublicKey     []byte
	ClientDataJSON    []byte
	AuthenticatorData []byte
	Signature         []byte
	RPID              string
	Challenge         []byte
	AllowedOrigins    []string
	StoredSignCount   uint32
}

// VerifyAssertion validates the login assertion and returns the new signature counter on success (for persistence).
func VerifyAssertion(in VerifyAssertionInput) (uint32, error) {
	if err := verifyClientData(in.ClientDataJSON, "webauthn.get", in.Challenge, in.AllowedOrigins); err != nil {
		return 0, err
	}
	ad, err := parseAuthData(in.AuthenticatorData)
	if err != nil {
		return 0, err
	}
	if subtle.ConstantTimeCompare(ad.rpIDHash, rpIDHashFor(in.RPID)) != 1 {
		return 0, ErrRPIDMismatch
	}
	if ad.flags&flagUserPresent == 0 {
		return 0, ErrUserNotPresent
	}
	pub, err := parseCOSEKey(in.COSEPublicKey)
	if err != nil {
		return 0, err
	}
	// The signature covers authenticatorData || sha256(clientDataJSON).
	cdHash := sha256.Sum256(in.ClientDataJSON)
	signed := make([]byte, 0, len(in.AuthenticatorData)+len(cdHash))
	signed = append(signed, in.AuthenticatorData...)
	signed = append(signed, cdHash[:]...)
	if err := pub.verify(signed, in.Signature); err != nil {
		return 0, err
	}
	// Counter regression check: strict only when both sides are non-zero (passkeys often report 0).
	if ad.signCount != 0 && in.StoredSignCount != 0 && ad.signCount <= in.StoredSignCount {
		return 0, ErrCounterRegressed
	}
	return ad.signCount, nil
}

// publicKey abstracts ES256/EdDSA signature verification.
type publicKey struct {
	ec *ecdsa.PublicKey
	ed ed25519.PublicKey
}

func (p publicKey) verify(signed, sig []byte) error {
	switch {
	case p.ec != nil:
		digest := sha256.Sum256(signed)
		if !ecdsa.VerifyASN1(p.ec, digest[:], sig) {
			return ErrSignatureInvalid
		}
		return nil
	case p.ed != nil:
		if !ed25519.Verify(p.ed, signed, sig) {
			return ErrSignatureInvalid
		}
		return nil
	default:
		return ErrPublicKeyUnsupported
	}
}

type coseKey struct {
	Kty int    `cbor:"1,keyasint"`
	Alg int    `cbor:"3,keyasint"`
	Crv int    `cbor:"-1,keyasint"`
	X   []byte `cbor:"-2,keyasint"`
	Y   []byte `cbor:"-3,keyasint"`
}

func parseCOSEKey(raw []byte) (publicKey, error) {
	var k coseKey
	if err := cbor.Unmarshal(raw, &k); err != nil {
		return publicKey{}, ErrPublicKeyUnsupported
	}
	switch k.Kty {
	case coseKtyEC2:
		if k.Alg != coseAlgES256 || k.Crv != coseCrvP256 || len(k.X) != 32 || len(k.Y) != 32 {
			return publicKey{}, ErrPublicKeyUnsupported
		}
		pub := &ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(k.X),
			Y:     new(big.Int).SetBytes(k.Y),
		}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			return publicKey{}, ErrPublicKeyUnsupported
		}
		return publicKey{ec: pub}, nil
	case coseKtyOKP:
		if k.Alg != coseAlgEdDSA || k.Crv != coseCrvEd25519 || len(k.X) != ed25519.PublicKeySize {
			return publicKey{}, ErrPublicKeyUnsupported
		}
		return publicKey{ed: ed25519.PublicKey(append([]byte(nil), k.X...))}, nil
	default:
		return publicKey{}, ErrPublicKeyUnsupported
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
