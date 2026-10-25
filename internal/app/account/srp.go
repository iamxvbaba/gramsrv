package account

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"

	"telesrv/internal/domain"
)

const (
	passwordHashSize = 256
)

var (
	baseSalt1 = []byte{0xEC, 0xF8, 0x73, 0x76, 0x65, 0xBC, 0x77, 0x5A}
	baseSalt2 = []byte{0xBE, 0xDE, 0x48, 0x88, 0x8C, 0x0F, 0x42, 0xAC, 0x34, 0xFF, 0xD1, 0xD4, 0x93, 0x5D, 0x8B, 0x21}
	baseP     = mustDecodeHex("c71caeb9c6b1c9048e6c522f70f13f73980d40238e3e21c14934d037563d930f48198a0aa7c14058229493d22530f4dbfa336f6e0ac925139543aed44cce7c3720fd51f69458705ac68cd4fe6b6b13abdc9746512969328454f18faf8c595f642477fe96bb2a941d5bcd1d4ac8cc49880708fa9b378e3c4f3a9060bee67cf9a4a4a695811051907e162753b56b0f6b410dba74d8a84b2a14b3144e0ef1284754fd17ed950d5965b4b9dd46582db1178d169c6bc465b0d6ff9ca3928fef5b9ae4e418fc15e83ebea0f87fa9ff5eed70050ded2849f47bf959d956850ce929851f0d8115f635b105ee2e4e15d04b2454bf6f4fadf034b10403119cd8e3b92fcc5b")
	baseG     = 3
)

func defaultPasswordAlgo() domain.PasswordKDFAlgo {
	return domain.PasswordKDFAlgo{
		Salt1: append([]byte(nil), baseSalt1...),
		Salt2: append([]byte(nil), baseSalt2...),
		G:     baseG,
		P:     append([]byte(nil), baseP...),
	}
}

func defaultSecureAlgo() domain.SecurePasswordKDFAlgo {
	return domain.SecurePasswordKDFAlgo{
		Kind: "pbkdf2_hmac_sha512_iter100000",
		Salt: []byte{0x7D, 0x04, 0xB3, 0x4B, 0x94, 0x82, 0x8C, 0x3D},
	}
}

func makeSRPChallenge(verifier []byte) (secret, b []byte, err error) {
	secret = make([]byte, passwordHashSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, nil, err
	}
	b, err = calcSRPB(secret, verifier)
	if err != nil {
		return nil, nil, err
	}
	return secret, b, nil
}

func calcSRPB(secret, verifier []byte) ([]byte, error) {
	p := new(big.Int).SetBytes(baseP)
	g := big.NewInt(int64(baseG))
	v := new(big.Int).SetBytes(verifier)
	b := new(big.Int).SetBytes(secret)
	if v.Sign() <= 0 || v.Cmp(p) >= 0 {
		return nil, domain.ErrPasswordHashInvalid
	}
	k := new(big.Int).SetBytes(hashBytes(padToHash(baseP), padToHash(g.Bytes())))
	kv := new(big.Int).Mul(k, v)
	kv.Mod(kv, p)
	gb := new(big.Int).Exp(g, b, p)
	out := new(big.Int).Add(kv, gb)
	out.Mod(out, p)
	return padToHash(out.Bytes()), nil
}

func checkSRP(settings domain.PasswordSettings, check domain.PasswordCheck) error {
	if check.Empty {
		if settings.HasPassword {
			return domain.ErrPasswordHashInvalid
		}
		return nil
	}
	if !settings.HasPassword {
		return domain.ErrPasswordHashInvalid
	}
	if settings.SRPID == 0 || settings.SRPID != check.SRPID {
		return domain.ErrSRPIDInvalid
	}
	if len(settings.SRPVerifier) == 0 || len(settings.SRPBSecret) == 0 || len(settings.SRPB) == 0 {
		return domain.ErrSRPPasswordChanged
	}
	got, err := calcSRPM1(settings, check.A)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, check.M1) {
		return domain.ErrPasswordHashInvalid
	}
	return nil
}

func calcSRPM1(settings domain.PasswordSettings, aBytes []byte) ([]byte, error) {
	p := new(big.Int).SetBytes(baseP)
	g := big.NewInt(int64(baseG))
	a := new(big.Int).SetBytes(aBytes)
	if !isGoodLarge(a, p) {
		return nil, domain.ErrPasswordHashInvalid
	}
	v := new(big.Int).SetBytes(settings.SRPVerifier)
	if !isGoodLarge(v, p) {
		return nil, domain.ErrPasswordHashInvalid
	}
	b := new(big.Int).SetBytes(settings.SRPBSecret)
	bForHash := padToHash(settings.SRPB)
	aForHash := padToHash(aBytes)
	u := new(big.Int).SetBytes(hashBytes(aForHash, bForHash))
	if u.Sign() <= 0 {
		return nil, domain.ErrPasswordHashInvalid
	}
	vu := new(big.Int).Exp(v, u, p)
	sBase := new(big.Int).Mul(a, vu)
	sBase.Mod(sBase, p)
	s := new(big.Int).Exp(sBase, b, p)
	k := hashBytes(padToHash(s.Bytes()))
	salt1 := settings.NewAlgo.Salt1
	if settings.CurrentAlgo != nil {
		salt1 = settings.CurrentAlgo.Salt1
	}
	return hashBytes(
		xorBytes(hashBytes(padToHash(baseP)), hashBytes(padToHash(g.Bytes()))),
		hashBytes(salt1),
		hashBytes(baseSalt2),
		aForHash,
		bForHash,
		k,
	), nil
}

func validateNewPasswordSettings(in domain.PasswordInputSettings) error {
	if in.NewAlgo == nil || len(in.NewPasswordHash) == 0 || len(in.NewPasswordHash) > passwordHashSize {
		return domain.ErrNewSettingsInvalid
	}
	algo := in.NewAlgo
	if algo.G != baseG || !bytes.Equal(algo.P, baseP) || !bytes.Equal(algo.Salt2, baseSalt2) {
		return domain.ErrNewSaltInvalid
	}
	if len(algo.Salt1) != len(baseSalt1)+32 || !bytes.Equal(algo.Salt1[:len(baseSalt1)], baseSalt1) {
		return domain.ErrNewSaltInvalid
	}
	v := new(big.Int).SetBytes(in.NewPasswordHash)
	if !isGoodLarge(v, new(big.Int).SetBytes(baseP)) {
		return domain.ErrPasswordHashInvalid
	}
	return nil
}

func hashBytes(parts ...[]byte) []byte {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

func padToHash(in []byte) []byte {
	if len(in) >= passwordHashSize {
		return append([]byte(nil), in[len(in)-passwordHashSize:]...)
	}
	out := make([]byte, passwordHashSize)
	copy(out[passwordHashSize-len(in):], in)
	return out
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func isGoodLarge(n, p *big.Int) bool {
	return n.Sign() > 0 && new(big.Int).Sub(p, n).Sign() > 0
}

func mustDecodeHex(s string) []byte {
	out, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return out
}
