// Package walletbackup implements the wallet backup envelope recovered from
// Telegram macOS 12.10.283233 and TDLib's tde2e MessageEncryption. It does not
// implement holder authorization, token issuance, or wallet RPC policy.
package walletbackup

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"io"

	"filippo.io/edwards25519"
)

const (
	shareMagic    uint32 = 0x8b90dd08
	envelopeMagic uint32 = 0x1ea87158
	MaxShareSize         = 0xffffff
	maxBlobSize          = MaxShareSize + 128
)

// ErrInvalid intentionally contains no key, plaintext, or envelope material.
var ErrInvalid = errors.New("invalid wallet backup envelope")

// SharedSecret derives TDLib's shared secret from an Ed25519 seed and public key.
func SharedSecret(seed, publicKey []byte) ([]byte, error) {
	if len(seed) != ed25519.SeedSize || len(publicKey) != ed25519.PublicKeySize {
		return nil, ErrInvalid
	}
	p, err := new(edwards25519.Point).SetBytes(publicKey)
	if err != nil {
		return nil, ErrInvalid
	}
	h := sha512.Sum512(seed)
	defer clear(h[:])
	private, err := ecdh.X25519().NewPrivateKey(h[:32])
	if err != nil {
		return nil, ErrInvalid
	}
	public, err := ecdh.X25519().NewPublicKey(p.BytesMontgomery())
	if err != nil {
		return nil, ErrInvalid
	}
	dh, err := private.ECDH(public)
	if err != nil {
		return nil, ErrInvalid
	}
	defer clear(dh)
	derived := mac512([]byte("tde2e_shared_secret"), dh)
	defer clear(derived)
	return append([]byte(nil), derived[:32]...), nil
}

func mac512(key, data []byte) []byte {
	h := hmac.New(sha512.New, key)
	_, _ = h.Write(data)
	return h.Sum(nil)
}

func messageID(key, plain []byte) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(plain)
	_, _ = h.Write([]byte{0, 0, 0, 0}) // LE32 length of empty associated data.
	return h.Sum(nil)[:16]
}

func encryptMessage(random io.Reader, key, plain []byte) ([]byte, error) {
	n := ((len(plain) + 31) &^ 15) - len(plain)
	p := make([]byte, n+len(plain))
	defer clear(p)
	if _, err := io.ReadFull(random, p[:n]); err != nil {
		return nil, err
	}
	p[0] = byte(n)
	copy(p[n:], plain)
	l := mac512(key, []byte("tde2e_encrypt_data"))
	defer clear(l)
	id := messageID(l[32:], p)
	h := mac512(l[:32], id)
	defer clear(h)
	block, err := aes.NewCipher(h[:32])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16+len(p))
	copy(out, id)
	cipher.NewCBCEncrypter(block, h[32:48]).CryptBlocks(out[16:], p)
	return out, nil
}

func decryptMessage(key, encrypted []byte) ([]byte, error) {
	if len(encrypted) < 32 || len(encrypted)%16 != 0 || len(encrypted) > maxBlobSize {
		return nil, ErrInvalid
	}
	l := mac512(key, []byte("tde2e_encrypt_data"))
	defer clear(l)
	h := mac512(l[:32], encrypted[:16])
	defer clear(h)
	block, err := aes.NewCipher(h[:32])
	if err != nil {
		return nil, ErrInvalid
	}
	p := make([]byte, len(encrypted)-16)
	defer clear(p)
	cipher.NewCBCDecrypter(block, h[32:48]).CryptBlocks(p, encrypted[16:])
	if !hmac.Equal(messageID(l[32:], p), encrypted[:16]) {
		return nil, ErrInvalid
	}
	n := int(p[0])
	if n < 16 || n > len(p) {
		return nil, ErrInvalid
	}
	return append([]byte(nil), p[n:]...), nil
}

func packBytes(data []byte) []byte {
	header := 1
	if len(data) >= 254 {
		header = 4
	}
	out := make([]byte, (header+len(data)+3)&^3)
	if header == 1 {
		out[0] = byte(len(data))
	} else {
		binary.LittleEndian.PutUint32(out, uint32(len(data))<<8|254)
	}
	copy(out[header:], data)
	return out
}

func unpackBytes(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, ErrInvalid
	}
	n, header := int(data[0]), 1
	if n == 255 {
		return nil, ErrInvalid
	}
	if n == 254 {
		if len(data) < 4 {
			return nil, ErrInvalid
		}
		n = int(binary.LittleEndian.Uint32(data[:4]) >> 8)
		header = 4
		if n < 254 {
			return nil, ErrInvalid
		}
	}
	end := header + n
	if end > len(data) || (end+3)&^3 != len(data) {
		return nil, ErrInvalid
	}
	for _, b := range data[end:] {
		if b != 0 {
			return nil, ErrInvalid
		}
	}
	return data[header:end], nil
}

func packShare(share []byte) ([]byte, error) {
	if len(share) == 0 || len(share) > MaxShareSize {
		return nil, ErrInvalid
	}
	out := make([]byte, 4)
	binary.LittleEndian.PutUint32(out, shareMagic)
	framed := packBytes(share)
	defer clear(framed)
	return append(out, framed...), nil
}

func unpackShare(plain []byte) ([]byte, error) {
	if len(plain) < 8 || binary.LittleEndian.Uint32(plain) != shareMagic {
		return nil, ErrInvalid
	}
	s, err := unpackBytes(plain[4:])
	if err != nil || len(s) == 0 || len(s) > MaxShareSize {
		return nil, ErrInvalid
	}
	return append([]byte(nil), s...), nil
}

// EncryptShare encrypts one share to an Ed25519 holder/export public key. The
// returned bare blob contains a fresh ephemeral public key followed by ciphertext.
func EncryptShare(publicKey, share []byte) ([]byte, error) {
	return encryptShare(rand.Reader, publicKey, share)
}

func encryptShare(random io.Reader, publicKey, share []byte) ([]byte, error) {
	plain, err := packShare(share)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	seed := make([]byte, 32)
	defer clear(seed)
	if _, err = io.ReadFull(random, seed); err != nil {
		return nil, err
	}
	key, err := SharedSecret(seed, publicKey)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	priv := ed25519.NewKeyFromSeed(seed)
	defer clear(priv)
	pub := priv.Public().(ed25519.PublicKey)
	message, err := encryptMessage(random, key, plain)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), pub...), message...), nil
}

// DecryptShare authenticates and opens one bare blob. Caller owns and should
// clear the returned plaintext as soon as it has been consumed.
func DecryptShare(seed, blob []byte) ([]byte, error) {
	if len(blob) <= 32 || len(blob)%16 != 0 || len(blob) > maxBlobSize {
		return nil, ErrInvalid
	}
	key, err := SharedSecret(seed, blob[:32])
	if err != nil {
		return nil, err
	}
	defer clear(key)
	plain, err := decryptMessage(key, blob[32:])
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	return unpackShare(plain)
}

// EncryptSecret produces exactly three independently encrypted XOR shares.
func EncryptSecret(secret []byte, publicKeys [][]byte) ([][]byte, error) {
	if len(secret) == 0 || len(secret) > MaxShareSize || len(publicKeys) != 3 {
		return nil, ErrInvalid
	}
	for _, p := range publicKeys {
		if len(p) != 32 {
			return nil, ErrInvalid
		}
	}
	a, b, c := make([]byte, len(secret)), make([]byte, len(secret)), make([]byte, len(secret))
	defer clear(a)
	defer clear(b)
	defer clear(c)
	if _, err := io.ReadFull(rand.Reader, a); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, err
	}
	for i := range secret {
		c[i] = secret[i] ^ a[i] ^ b[i]
	}
	result := make([][]byte, 3)
	for i, s := range [][]byte{a, b, c} {
		var err error
		result[i], err = EncryptShare(publicKeys[i], s)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Combine opens the three export envelopes. Bare and indexed envelopes are
// supported, but a mixture, duplicate indices, or inconsistent tags is rejected.
func Combine(seed []byte, envelopes [][]byte) ([]byte, error) {
	if len(envelopes) != 3 {
		return nil, ErrInvalid
	}
	var blobs [3][]byte
	wrapped := len(envelopes[0]) >= 4 && binary.LittleEndian.Uint32(envelopes[0]) == envelopeMagic
	var tag uint32
	for i, e := range envelopes {
		isWrapped := len(e) >= 4 && binary.LittleEndian.Uint32(e) == envelopeMagic
		if isWrapped != wrapped || len(e) > maxBlobSize+32 {
			return nil, ErrInvalid
		}
		if !wrapped {
			blobs[i] = e
			continue
		}
		if len(e) < 20 {
			return nil, ErrInvalid
		}
		index, count, t := binary.LittleEndian.Uint32(e[4:]), binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:])
		if index >= 3 || count != 3 || blobs[index] != nil || (i > 0 && t != tag) {
			return nil, ErrInvalid
		}
		tag = t
		var err error
		blobs[index], err = unpackBytes(e[16:])
		if err != nil {
			return nil, err
		}
	}
	var shares [3][]byte
	defer func() {
		for _, s := range shares {
			clear(s)
		}
	}()
	for i, b := range blobs {
		var err error
		shares[i], err = DecryptShare(seed, b)
		if err != nil {
			return nil, err
		}
	}
	n := len(shares[0])
	if len(shares[1]) != n || len(shares[2]) != n {
		return nil, ErrInvalid
	}
	secret := make([]byte, n)
	for i := range secret {
		secret[i] = shares[0][i] ^ shares[1][i] ^ shares[2][i]
	}
	return secret, nil
}
