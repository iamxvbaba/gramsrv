package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const totpStep = 30 * time.Second

var errTOTPSecret = errors.New("secret must be base32 (A-Z, 2-7), at least 16 characters")

type totpVerifier struct {
	key  []byte
	mu   sync.Mutex
	last int64
}

func newTOTPVerifier(secret string) (*totpVerifier, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	normalized = strings.TrimRight(normalized, "=")
	if len(normalized) < 16 {
		return nil, errTOTPSecret
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil {
		return nil, errTOTPSecret
	}
	return &totpVerifier{key: key}, nil
}

func (v *totpVerifier) Verify(code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	counter := now.Unix() / int64(totpStep/time.Second)
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, c := range []int64{counter - 1, counter, counter + 1} {
		if c <= v.last {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(totpCode(v.key, c)), []byte(code)) == 1 {
			v.last = c
			return true
		}
	}
	return false
}

func totpCode(key []byte, counter int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset]&0x7f) << 24) |
		(uint32(sum[offset+1]) << 16) |
		(uint32(sum[offset+2]) << 8) |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", value%1000000)
}
