package walletbackup

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func vector(t *testing.T) map[string][]byte {
	t.Helper()
	data, err := os.ReadFile("testdata/vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var src map[string]string
	if err = json.Unmarshal(data, &src); err != nil {
		t.Fatal(err)
	}
	out := make(map[string][]byte)
	for k, s := range src {
		out[k], err = hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestIndependentKnownAnswer(t *testing.T) {
	v := vector(t)
	key, err := SharedSecret(v["ephemeral_seed"], v["public_key"])
	if err != nil || !bytes.Equal(key, v["shared_secret"]) {
		t.Fatal("shared-secret mismatch", err)
	}
	got, err := encryptShare(bytes.NewReader(v["random"]), v["public_key"], v["share"])
	if err != nil || !bytes.Equal(got, v["blob"]) {
		t.Fatal("ciphertext mismatch", err)
	}
	plain, err := DecryptShare(v["seed"], v["blob"])
	if err != nil || !bytes.Equal(plain, v["share"]) {
		t.Fatal("plaintext mismatch", err)
	}
}

func TestRejectTamperedTruncatedOrWrongKey(t *testing.T) {
	v := vector(t)
	blob := v["blob"]
	for i := range blob {
		bad := bytes.Clone(blob)
		bad[i] ^= 1
		if got, err := DecryptShare(v["seed"], bad); err == nil || got != nil {
			t.Fatalf("tampered byte %d accepted", i)
		}
		if got, err := DecryptShare(v["seed"], blob[:i]); err == nil || got != nil {
			t.Fatalf("truncation %d accepted", i)
		}
	}
	if _, err := DecryptShare(bytes.Repeat([]byte{7}, 32), blob); err == nil {
		t.Fatal("wrong recipient accepted")
	}
	for _, pub := range [][]byte{nil, make([]byte, 31), make([]byte, 32), append([]byte{1}, make([]byte, 31)...)} {
		if _, err := SharedSecret(v["seed"], pub); err == nil {
			t.Fatal("invalid/low-order peer key accepted")
		}
	}
}

func TestFramingAndExportRecovery(t *testing.T) {
	seeds := [][]byte{bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32)}
	pubs := make([][]byte, 3)
	for i, seed := range seeds {
		pubs[i] = ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	}
	exportSeed := bytes.Repeat([]byte{4}, 32)
	exportPub := ed25519.NewKeyFromSeed(exportSeed).Public().(ed25519.PublicKey)
	for _, n := range []int{1, 2, 3, 4, 253, 254, 255, 256, 1024} {
		secret := bytes.Repeat([]byte{42}, n)
		parts, err := EncryptSecret(secret, pubs)
		if err != nil {
			t.Fatal(err)
		}
		exported := make([][]byte, 3)
		for i, p := range parts {
			share, err := DecryptShare(seeds[i], p)
			if err != nil {
				t.Fatal(err)
			}
			exported[i], err = EncryptShare(exportPub, share)
			clear(share)
			if err != nil {
				t.Fatal(err)
			}
		}
		got, err := Combine(exportSeed, exported)
		if err != nil || !bytes.Equal(got, secret) {
			t.Fatal("bare recovery", n, err)
		}
		wrapped := [][]byte{wrap(exported[2], 2, 3, 7), wrap(exported[0], 0, 3, 7), wrap(exported[1], 1, 3, 7)}
		got, err = Combine(exportSeed, wrapped)
		if err != nil || !bytes.Equal(got, secret) {
			t.Fatal("indexed recovery", n, err)
		}
		for _, bad := range [][][]byte{
			{wrapped[0], wrapped[0], wrapped[2]},
			{wrapped[0], wrapped[1], exported[1]},
			{wrapped[0], wrapped[1], wrap(exported[1], 1, 3, 8)},
			{wrapped[0], wrapped[1], wrap(exported[1], 1, 2, 7)},
			{wrapped[0], wrapped[1]},
		} {
			if _, err := Combine(exportSeed, bad); err == nil {
				t.Fatal("invalid envelope set accepted")
			}
		}
	}
}

func wrap(blob []byte, index, count, tag uint32) []byte {
	h := make([]byte, 16)
	for i, v := range []uint32{envelopeMagic, index, count, tag} {
		binary.LittleEndian.PutUint32(h[i*4:], v)
	}
	return append(h, packBytes(blob)...)
}

func TestShareFramingStrict(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 253, 254, 255, 256, 1024} {
		p, err := packShare(bytes.Repeat([]byte{42}, n))
		if err != nil {
			t.Fatal(err)
		}
		for i := range p {
			if _, err := unpackShare(p[:i]); err == nil {
				t.Fatalf("truncated share accepted: %d/%d", i, n)
			}
		}
		if _, err := unpackShare(append(bytes.Clone(p), 0)); err == nil {
			t.Fatal("trailing data accepted")
		}
	}
	p, _ := packShare([]byte{1, 2})
	p[len(p)-1] = 1
	if _, err := unpackShare(p); err == nil {
		t.Fatal("nonzero padding accepted")
	}
	if _, err := packShare(nil); err == nil {
		t.Fatal("empty share accepted")
	}
}

func FuzzDecryptShare(f *testing.F) {
	f.Add([]byte{0})
	f.Add(make([]byte, 80))
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = DecryptShare(make([]byte, 32), b) })
}

func FuzzCombine(f *testing.F) {
	f.Add([]byte{0}, []byte{0}, []byte{0})
	f.Fuzz(func(t *testing.T, a, b, c []byte) { _, _ = Combine(make([]byte, 32), [][]byte{a, b, c}) })
}
