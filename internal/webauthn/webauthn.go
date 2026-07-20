// Package webauthn 实现 telesrv passkey 登录所需的 WebAuthn/FIDO2 服务端原语:
// 生成注册/断言挑战选项、验证注册 attestation、验证登录 assertion。
//
// 它只处理字节与 JSON,不依赖 tg/domain/store——便于独立单测(配套
// internal/webauthn/webauthntest 的软件 authenticator)。MTProto 场景下,客户端把
// WebAuthn 各字段 base64url 解码后塞进 TL,故这里直接收原始字节(clientDataJSON、
// authenticatorData、signature、attestationObject、credentialID)。
//
// 支持算法:ES256(ECDSA P-256,平台 passkey 主流)与 EdDSA(Ed25519)。
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

// 验证错误。调用方据此映射到 TL 错误码/拒绝登录。
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

// authenticatorData 标志位(WebAuthn §6.1)。
const (
	flagUserPresent      = 0x01
	flagUserVerified     = 0x04
	flagAttestedCredData = 0x40
	flagExtensionData    = 0x80
)

// COSE algorithm identifiers。
const (
	coseAlgES256 = -7
	coseAlgEdDSA = -8
)

// COSE key type / curve。
const (
	coseKtyOKP     = 1
	coseKtyEC2     = 2
	coseCrvP256    = 1
	coseCrvEd25519 = 6
)

// b64 是 WebAuthn 用的 base64url 无填充编码。
var b64 = base64.RawURLEncoding

// decodeB64URL 容忍有/无填充的 base64url。
func decodeB64URL(s string) ([]byte, error) {
	if out, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return out, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

// RegistrationParams 是构造 publicKey creation options 所需的输入。
type RegistrationParams struct {
	RPID          string
	RPName        string
	UserID        []byte // 通常是 "dcId:userId" 的字节
	UserName      string
	UserDisplay   string
	Challenge     []byte
	TimeoutMillis int
	ExcludeIDs    [][]byte // 已有凭据 id,避免同一 authenticator 重复注册
}

// BuildRegistrationOptions 返回 account.passkeyRegistrationOptions 的 DataJSON 内容
// （顶层含 publicKey 字段,与 DrKLO PasskeysController 读取约定一致）。
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
		"rp":        map[string]any{"id": p.RPID, "name": orDefault(p.RPName, branding.ProductName)},
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

// LoginParams 是构造 publicKey request options（断言挑战）的输入。
type LoginParams struct {
	RPID          string
	Challenge     []byte
	TimeoutMillis int
}

// BuildLoginOptions 返回 auth.passkeyLoginOptions 的 DataJSON 内容。无 allowCredentials
// （discoverable/usernameless 登录,用户由 user_handle 反查)。
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

// clientData 是 clientDataJSON 的解析结构。
type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

// verifyClientData 校验 type/challenge/origin。allowedOrigins 为空表示不强校验 origin
// （dev:服务端通常不预知 Android apk-key-hash origin)。
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

// ChallengeFromClientData 提取 clientDataJSON 里的 challenge 原始字节,供调用方按挑战
// 反查/消费一次性挑战 store(此时尚未验证,仅做查表键)。
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

// parsedAuthData 是 authenticatorData 的解析结果。
type parsedAuthData struct {
	rpIDHash   []byte
	flags      byte
	signCount  uint32
	aaguid     []byte
	credID     []byte
	credPubKey []byte // COSE 公钥原始字节(仅注册时存在)
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
		// 之后是 COSE 公钥(变长 CBOR);用 UnmarshalFirst 只吃一个 CBOR item。
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

// Credential 是注册验证产出的凭据材料,调用方据此持久化。
type Credential struct {
	ID            []byte // credential id 原始字节
	COSEPublicKey []byte // COSE 公钥原始字节
	SignCount     uint32
	AAGUID        []byte
}

// VerifyRegistrationInput 是注册验证输入。
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

// VerifyRegistration 校验 attestation 并提取凭据公钥。attestation 格式仅要求能取出
// 公钥(消费级 passkey 一般是 "none",不校验 attestation statement 签名)。
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
	// 公钥能解析即认为有效(none attestation)。
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

// VerifyAssertionInput 是登录断言验证输入。
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

// VerifyAssertion 校验登录断言,成功返回新的签名计数器(供持久化)。
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
	// 签名覆盖 authenticatorData || sha256(clientDataJSON)。
	cdHash := sha256.Sum256(in.ClientDataJSON)
	signed := make([]byte, 0, len(in.AuthenticatorData)+len(cdHash))
	signed = append(signed, in.AuthenticatorData...)
	signed = append(signed, cdHash[:]...)
	if err := pub.verify(signed, in.Signature); err != nil {
		return 0, err
	}
	// 计数器回退检测:仅当双方都非 0 时严格(passkey 常报 0)。
	if ad.signCount != 0 && in.StoredSignCount != 0 && ad.signCount <= in.StoredSignCount {
		return 0, ErrCounterRegressed
	}
	return ad.signCount, nil
}

// publicKey 抽象 ES256/EdDSA 验签。
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
