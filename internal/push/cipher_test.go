package push

import (
	"bytes"
	"crypto/aes"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// decryptPushPayload is an independent implementation of the official client
// decryption (DrKLO Android PushListenerController.processRemoteMessage). It is
// deliberately written from the client logic, not from encryptPushPayload, so the
// round-trip test proves we produced the wire format the clients accept.
func decryptPushPayload(t *testing.T, secret []byte, encoded string) []byte {
	t.Helper()
	blob, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if len(blob) < 24 || len(blob)%aes.BlockSize != 8 {
		t.Fatalf("blob length %d is not framed", len(blob))
	}
	sum := sha1.Sum(secret)
	if !bytes.Equal(blob[0:8], sum[len(sum)-8:]) {
		t.Fatalf("auth_key_id mismatch: got %x want %x", blob[0:8], sum[len(sum)-8:])
	}
	msgKey := blob[8:24]
	key, iv := deriveMessageKeyData(secret, msgKey, true)
	plaintext, err := aesIGEDecrypt(key, iv, blob[24:])
	if err != nil {
		t.Fatalf("aes-ige decrypt: %v", err)
	}
	mac := sha256.New()
	mac.Write(secret[96:128])
	mac.Write(plaintext)
	full := mac.Sum(nil)
	if !bytes.Equal(msgKey, full[8:24]) {
		t.Fatalf("msg_key check failed")
	}
	length := int(binary.LittleEndian.Uint32(plaintext[0:4]))
	if length < 0 || 4+length > len(plaintext) {
		t.Fatalf("declared length %d exceeds plaintext %d", length, len(plaintext))
	}
	return plaintext[4 : 4+length]
}

// aesIGEDecrypt is the inverse of aesIGEEncrypt, implemented independently for
// the test.
func aesIGEDecrypt(key, iv, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ciphertext))
	prevC := iv[:aes.BlockSize]
	prevP := iv[aes.BlockSize:]
	tmp := make([]byte, aes.BlockSize)
	for off := 0; off < len(ciphertext); off += aes.BlockSize {
		c := ciphertext[off : off+aes.BlockSize]
		for i := 0; i < aes.BlockSize; i++ {
			tmp[i] = c[i] ^ prevP[i]
		}
		block.Decrypt(out[off:off+aes.BlockSize], tmp)
		for i := 0; i < aes.BlockSize; i++ {
			out[off+i] ^= prevC[i]
		}
		prevC = c
		prevP = out[off : off+aes.BlockSize]
	}
	return out, nil
}

func TestEncryptPushPayloadRoundTrip(t *testing.T) {
	secret := make([]byte, 256)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand secret: %v", err)
	}
	notif := Notification{
		LocKey:  "MESSAGE_TEXT",
		LocArgs: []string{"Alice", "hello"},
		Custom:  map[string]any{"from_id": int64(42), "msg_id": int64(7)},
		UserID:  1001,
	}
	plaintext, err := notif.plaintext()
	if err != nil {
		t.Fatalf("plaintext: %v", err)
	}
	sealed, err := encryptPushPayload(secret, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	decoded := decryptPushPayload(t, secret, sealed)
	var got Notification
	if err := json.Unmarshal(decoded, &got); err != nil {
		t.Fatalf("unmarshal decrypted: %v", err)
	}
	if got.LocKey != notif.LocKey || got.UserID != notif.UserID || len(got.LocArgs) != 2 || got.LocArgs[0] != "Alice" {
		t.Fatalf("round-trip notification = %+v", got)
	}
	if got.Custom["from_id"].(float64) != 42 {
		t.Fatalf("custom = %+v", got.Custom)
	}
}

func TestEncryptPushPayloadRejectsShortSecret(t *testing.T) {
	if _, err := encryptPushPayload(make([]byte, 32), []byte("x")); err != errSecretTooShort {
		t.Fatalf("short secret = %v, want errSecretTooShort", err)
	}
}

func TestEncryptPushPayloadIsUnique(t *testing.T) {
	secret := make([]byte, 256)
	_, _ = rand.Read(secret)
	first, err := encryptPushPayload(secret, []byte("same"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	second, err := encryptPushPayload(secret, []byte("same"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if first == second {
		t.Fatal("two encryptions of the same plaintext produced identical output (padding should randomise it)")
	}
}
