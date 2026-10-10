package giftclaim

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

type webAppUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

func verifyInitData(raw, botToken string, maxAge time.Duration, now time.Time) (webAppUser, error) {
	raw, botToken = strings.TrimSpace(raw), strings.TrimSpace(botToken)
	if raw == "" || botToken == "" {
		return webAppUser{}, errors.New("Mini App authorization is missing")
	}
	values, err := url.ParseQuery(raw)
	if err != nil || values.Get("hash") == "" {
		return webAppUser{}, errors.New("Mini App authorization is invalid")
	}
	want := initDataHash(values, botToken)
	if !hmac.Equal([]byte(values.Get("hash")), []byte(want)) {
		return webAppUser{}, errors.New("Mini App signature mismatch")
	}
	authDate, err := strconv.ParseInt(values.Get("auth_date"), 10, 64)
	if err != nil || authDate <= 0 {
		return webAppUser{}, errors.New("Mini App auth_date is invalid")
	}
	authTime := time.Unix(authDate, 0)
	if authTime.After(now.Add(time.Minute)) || (maxAge > 0 && now.Sub(authTime) > maxAge) {
		return webAppUser{}, errors.New("Mini App authorization expired")
	}
	var user webAppUser
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID <= 0 {
		return webAppUser{}, errors.New("Mini App user is invalid")
	}
	return user, nil
}

func initDataHash(values url.Values, botToken string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "hash" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var check strings.Builder
	for i, key := range keys {
		if i > 0 {
			check.WriteByte('\n')
		}
		check.WriteString(key)
		check.WriteByte('=')
		check.WriteString(values.Get(key))
	}
	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	_, _ = secretMAC.Write([]byte(botToken))
	dataMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
	_, _ = dataMAC.Write([]byte(check.String()))
	return hex.EncodeToString(dataMAC.Sum(nil))
}
