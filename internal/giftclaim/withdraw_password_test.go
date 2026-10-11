package giftclaim

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
	"golang.org/x/crypto/pbkdf2"

	"telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type withdrawalClaimStore struct {
	Store
	gift     domain.UniqueStarGift
	owner    string
	ownerID  int64
	ownedBy  int64
	bindCall int
}

func (s *withdrawalClaimStore) ResolveGiftByRef(context.Context, string) (domain.UniqueStarGift, bool, error) {
	return s.gift, s.gift.ID != 0, nil
}

func (s *withdrawalClaimStore) SavedGiftOwner(context.Context, int64) (string, int64, bool, error) {
	return s.owner, s.ownerID, true, nil
}

func (s *withdrawalClaimStore) WalletBindGift(context.Context, domain.StarGiftWalletBind) (domain.UniqueStarGift, error) {
	s.bindCall++
	return domain.UniqueStarGift{}, nil
}

func (s *withdrawalClaimStore) WalletReleaseGift(context.Context, int64, int64) (domain.UniqueStarGift, error) {
	return domain.UniqueStarGift{}, nil
}

type withdrawalClaimWithdrawer struct {
	calls int
	err   error
	req   domain.StarGiftWithdrawalRequest
}

func (s *withdrawalClaimWithdrawer) Withdraw(
	_ context.Context,
	req domain.StarGiftWithdrawalRequest) (domain.StarGiftWithdrawal, error) {
	s.calls++
	s.req = req
	if s.err != nil {
		return domain.StarGiftWithdrawal{}, s.err
	}
	return domain.StarGiftWithdrawal{
		URL:       "https://links.example.test/gift-withdrawal/token",
		ExpiresAt: int(time.Now().Add(time.Hour).Unix()),
	}, nil
}

func signedClaimInitData(botToken string, userID int64, now time.Time) string {
	values := url.Values{}
	values.Set("auth_date", strconv.FormatInt(now.Unix(), 10))
	values.Set("query_id", "AAF-test")
	values.Set("user", fmt.Sprintf(`{"id":%d,"first_name":"Tester","username":"tester"}`, userID))
	values.Set("hash", initDataHash(values, botToken))
	return values.Encode()
}

// claimPasswordDigest mirrors the browser's x = SHA256(salt2 || PBKDF2(...) ||
// salt2) chain so the server re-derives the same exponent from the password.
func claimPasswordDigest(algo domain.PasswordKDFAlgo, password []byte) []byte {
	first := claimHash(algo.Salt1, password, algo.Salt1)
	second := claimHash(algo.Salt2, first, algo.Salt2)
	third := pbkdf2.Key(second, algo.Salt1, 100000, 64, sha512.New)
	return claimHash(algo.Salt2, third, algo.Salt2)
}

func padClaimHash(in []byte) []byte {
	if len(in) >= 256 {
		return append([]byte(nil), in[len(in)-256:]...)
	}
	out := make([]byte, 256)
	copy(out[256-len(in):], in)
	return out
}

func claimHash(parts ...[]byte) []byte {
	sum := sha256.New()
	for _, part := range parts {
		_, _ = sum.Write(part)
	}
	return sum.Sum(nil)
}

func claimModPow(base, exponent, modulus *big.Int) *big.Int {
	return new(big.Int).Exp(base, exponent, modulus)
}

// claimSRPProof is the browser half of the withdrawal 2FA flow: it turns a
// server challenge and a secret exponent into {srp_id, a, m1} exactly like the
// Mini App does in JavaScript, so the wire format is what gets tested here.
func claimSRPProof(
	t *testing.T,
	challenge PasswordChallenge,
	secret *big.Int) WithdrawalPasswordInput {
	t.Helper()
	if !challenge.HasPassword || challenge.SRPB == "" || challenge.P == "" {
		t.Fatalf("challenge = %+v, want SRP material", challenge)
	}
	modulus := new(big.Int).SetBytes(mustClaimHex(challenge.P))
	g := big.NewInt(int64(challenge.G))
	a, err := rand.Int(rand.Reader, modulus)
	if err != nil || a.Sign() <= 0 {
		t.Fatalf("random SRP a: %v", err)
	}
	bigA := claimModPow(g, a, modulus)
	A := padClaimHash(bigA.Bytes())
	B := padClaimHash(mustClaimHex(challenge.SRPB))
	u := new(big.Int).SetBytes(claimHash(A, B))
	if u.Sign() <= 0 || secret.Sign() <= 0 {
		t.Fatalf("degenerate SRP parameters")
	}
	k := new(big.Int).SetBytes(claimHash(padClaimHash(modulus.Bytes()), padClaimHash(g.Bytes())))
	verifier := claimModPow(g, secret, modulus)
	base := new(big.Int).Mul(k, verifier)
	base.Mod(base, modulus)
	base.Sub(new(big.Int).SetBytes(B), base)
	base.Mod(base, modulus)
	exponent := new(big.Int).Mul(u, secret)
	exponent.Add(exponent, a)
	shared := claimModPow(base, exponent, modulus)
	sessionKey := claimHash(padClaimHash(shared.Bytes()))
	salt1 := mustClaimHex(challenge.Salt1)
	salt2 := mustClaimHex(challenge.Salt2)
	masked := claimHash(padClaimHash(modulus.Bytes()))
	maskedG := claimHash(padClaimHash(g.Bytes()))
	for i := range masked {
		masked[i] ^= maskedG[i]
	}
	m1 := claimHash(masked, claimHash(salt1), claimHash(salt2), A, B, sessionKey)
	return WithdrawalPasswordInput{
		SRPID: challenge.SRPID,
		A:     fmt.Sprintf("%x", A),
		M1:    fmt.Sprintf("%x", m1),
	}
}

func mustClaimHex(value string) []byte {
	out, err := decodeClaimHex(value)
	if err != nil {
		panic(err)
	}
	return out
}

func decodeClaimHex(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if len(value)%2 != 0 {
		return nil, fmt.Errorf("odd hex length")
	}
	out := make([]byte, len(value)/2)
	for i := 0; i < len(out); i++ {
		var b byte
		if _, err := fmt.Sscanf(value[i*2:i*2+2], "%02x", &b); err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func TestWithdrawalPasswordChallengeAndSRPConfirm(t *testing.T) {
	const userID int64 = 424242
	const botToken = "999999:test-token-secret"
	now := time.Now()
	ctx := context.Background()

	accountSvc := account.NewService(memory.NewPasswordStore())
	initial, err := accountSvc.GetPassword(ctx, userID)
	if err != nil {
		t.Fatalf("initial password state: %v", err)
	}
	algo := initial.NewAlgo
	algo.Salt1 = append(append([]byte(nil), algo.Salt1...), bytes.Repeat([]byte{0x5A}, 32)...)

	secret := new(big.Int).SetBytes(claimPasswordDigest(algo, []byte("claim-withdrawal-password")))
	modulus := new(big.Int).SetBytes(algo.P)
	verifier := padClaimHash(claimModPow(big.NewInt(int64(algo.G)), secret, modulus).Bytes())
	if err := accountSvc.UpdatePasswordSettings(ctx, userID, domain.PasswordCheck{Empty: true},
		domain.PasswordInputSettings{
			NewAlgo: &algo, NewPasswordHash: verifier, Hint: "test", HasHint: true,
		}); err != nil {
		t.Fatalf("set password: %v", err)
	}

	service := &Service{
		publicBaseURL: "https://claim.example",
		proofDomain:   "claim.example",
		botToken:      botToken,
		basePath:      "/claim",
		botHandle:     "@claimtest",
		collection:    "test-collection",
		appName:       "InvGram Gifts",
		initDataTTL:   15 * time.Minute,
		store: &withdrawalClaimStore{
			gift:    domain.UniqueStarGift{ID: 11, Slug: "withdrawal-claim-gift"},
			owner:   "user",
			ownerID: userID,
		},
		withdrawer: &withdrawalClaimWithdrawer{},
		password:   accountSvc,
		logger:     zaptest.NewLogger(t),
	}
	initData := signedClaimInitData(botToken, userID, now)

	post := func(path string, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/claim"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Telegram-Init-Data", initData)
		recorder := httptest.NewRecorder()
		service.ServeHTTP(recorder, req)
		return recorder
	}

	unauthenticated := httptest.NewRecorder()
	service.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodPost, "/claim/api/wallet/password", strings.NewReader("{}")))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("challenge without initData = %d", unauthenticated.Code)
	}

	challengePage := post("/api/wallet/password", "{}")
	if challengePage.Code != http.StatusOK {
		t.Fatalf("challenge status = %d body=%s", challengePage.Code, challengePage.Body.String())
	}
	var challenge PasswordChallenge
	if err := json.Unmarshal(challengePage.Body.Bytes(), &challenge); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	if !challenge.HasPassword || challenge.SRPID == 0 || challenge.SRPB == "" || challenge.G == 0 ||
		challenge.P == "" || challenge.Salt1 == "" || challenge.Salt2 == "" {
		t.Fatalf("challenge = %+v, want full SRP parameters", challenge)
	}

	proof := claimSRPProof(t, challenge, secret)
	withoutPassword, err := json.Marshal(map[string]string{"gift": "withdrawal-claim-gift"})
	if err != nil {
		t.Fatal(err)
	}
	rejected := post("/api/wallet/withdraw", string(withoutPassword))
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), ErrPasswordInvalid.Error()) {
		t.Fatalf("withdraw without password = %d %s", rejected.Code, rejected.Body.String())
	}

	tampered := proof
	tampered.M1 = strings.Repeat("00", sha256.Size)
	badBody, err := json.Marshal(struct {
		Gift     string                   `json:"gift"`
		Password *WithdrawalPasswordInput `json:"password"`
	}{Gift: "withdrawal-claim-gift", Password: &tampered})
	if err != nil {
		t.Fatal(err)
	}
	badPage := post("/api/wallet/withdraw", string(badBody))
	if badPage.Code != http.StatusBadRequest {
		t.Fatalf("withdraw with wrong M1 = %d %s", badPage.Code, badPage.Body.String())
	}

	body, err := json.Marshal(struct {
		Gift     string                   `json:"gift"`
		Password *WithdrawalPasswordInput `json:"password"`
	}{Gift: "withdrawal-claim-gift", Password: &proof})
	if err != nil {
		t.Fatal(err)
	}
	okPage := post("/api/wallet/withdraw", string(body))
	if okPage.Code != http.StatusOK {
		t.Fatalf("withdraw status = %d body=%s", okPage.Code, okPage.Body.String())
	}
	var result WithdrawResult
	if err := json.Unmarshal(okPage.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode withdrawal: %v", err)
	}
	if result.URL == "" || result.GiftSlug != "withdrawal-claim-gift" {
		t.Fatalf("withdrawal = %+v", result)
	}
}
