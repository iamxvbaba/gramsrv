package push

import (
	"crypto/aes"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// errSecretTooShort means the client-provided push secret cannot be used with the
// MTProto-v2 push scheme, which reads auth_key bytes [0:128].
var errSecretTooShort = errors.New("push secret too short")

// encryptPushPayload seals a notification JSON with the client-provided secret,
// producing the value for the FCM/APNs `p` field.
//
// This mirrors the official client decryption (DrKLO Android
// PushListenerController.processRemoteMessage, and the equivalent iOS path):
//
//	blob = SHA1(secret)[len-8:] || msg_key(16) || AES-IGE(padded_plaintext)
//	msg_key = SHA256(secret[96:128] || padded_plaintext)[8:24]
//	aes key/iv = MTProto-v2 derivation with the server (incoming) offset x=8
//
// padded_plaintext is int32LE(len(json)) || json || random padding to a 16-byte
// block. The client reads len and ignores the padding.
//
// The payload is the final URL-safe base64 (padded) string.
func encryptPushPayload(secret, plaintext []byte) (string, error) {
	if len(secret) < 128 {
		return "", errSecretTooShort
	}
	padded, err := padPlaintext(plaintext)
	if err != nil {
		return "", err
	}

	// msg_key is derived from the padded plaintext, exactly as the client
	// recomputes it after decryption.
	mac := sha256.New()
	mac.Write(secret[96:128])
	mac.Write(padded)
	full := mac.Sum(nil)
	var msgKey [16]byte
	copy(msgKey[:], full[8:24])

	key, iv := deriveMessageKeyData(secret, msgKey[:], true)
	ciphertext, err := aesIGEEncrypt(key, iv, padded)
	if err != nil {
		return "", err
	}

	sum := sha1.Sum(secret)
	blob := make([]byte, 0, 8+16+len(ciphertext))
	blob = append(blob, sum[len(sum)-8:]...)
	blob = append(blob, msgKey[:]...)
	blob = append(blob, ciphertext...)
	return base64.URLEncoding.EncodeToString(blob), nil
}

// padPlaintext appends random bytes so the result is a non-zero multiple of the
// AES block size (the same trailing-padding approach MTProto uses, which the
// client ignores past the declared JSON length).
func padPlaintext(plaintext []byte) ([]byte, error) {
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+pad)
	copy(padded, plaintext)
	if _, err := rand.Read(padded[len(plaintext):]); err != nil {
		return nil, err
	}
	return padded, nil
}

// deriveMessageKeyData implements MessageKeyData.generateMessageKeyData(_, _, incoming, 2)
// from the official clients. incoming is true for the direction the client reads
// (server to client), which shifts the auth_key slice by 8 bytes.
func deriveMessageKeyData(authKey, messageKey []byte, incoming bool) (key, iv []byte) {
	x := 0
	if incoming {
		x = 8
	}
	sha256A := sha256.Sum256(concat(messageKey, authKey[x:x+36]))
	sha256B := sha256.Sum256(concat(authKey[40+x:76+x], messageKey))

	key = concat(sha256A[0:8], sha256B[8:24], sha256A[24:32])
	iv = concat(sha256B[0:8], sha256A[8:24], sha256B[24:32])
	return key, iv
}

func concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// aesIGEEncrypt applies AES-256-IGE (the mode MTProto uses; not in the standard
// library). The plaintext length must be a non-zero multiple of the block size.
func aesIGEEncrypt(key, iv, plaintext []byte) ([]byte, error) {
	if len(iv) != 2*aes.BlockSize {
		return nil, errors.New("aes-ige: iv must be 32 bytes")
	}
	if len(plaintext) == 0 || len(plaintext)%aes.BlockSize != 0 {
		return nil, errors.New("aes-ige: plaintext must be a non-zero multiple of the block size")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(plaintext))
	prevC := iv[:aes.BlockSize]
	prevP := iv[aes.BlockSize:]
	tmp := make([]byte, aes.BlockSize)
	for off := 0; off < len(plaintext); off += aes.BlockSize {
		p := plaintext[off : off+aes.BlockSize]
		for i := 0; i < aes.BlockSize; i++ {
			tmp[i] = p[i] ^ prevC[i]
		}
		block.Encrypt(out[off:off+aes.BlockSize], tmp)
		for i := 0; i < aes.BlockSize; i++ {
			out[off+i] ^= prevP[i]
		}
		prevC = out[off : off+aes.BlockSize]
		prevP = p
	}
	return out, nil
}
