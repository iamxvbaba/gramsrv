package account

import (
	"bytes"
	"context"
	"crypto/sha512"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/pbkdf2"

	"telesrv/internal/domain"
	"telesrv/internal/otpdelivery"
	"telesrv/internal/store/memory"
)

func TestPasswordSRPRoundTrip(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1001
	svc := NewService(memory.NewPasswordStore())

	initial, err := svc.GetPassword(ctx, userID)
	if err != nil {
		t.Fatalf("GetPassword initial: %v", err)
	}
	algo := initial.NewAlgo
	algo.Salt1 = append(append([]byte(nil), algo.Salt1...), bytes.Repeat([]byte{0xA5}, 32)...)
	input := domain.PasswordInputSettings{
		NewAlgo:         &algo,
		NewPasswordHash: verifierForPassword(algo, []byte("correct horse")),
		Hint:            "horse",
		HasHint:         true,
		Email:           "alice@example.com",
		HasEmail:        true,
	}
	if err := svc.UpdatePasswordSettings(ctx, userID, domain.PasswordCheck{Empty: true}, input); err != nil {
		t.Fatalf("UpdatePasswordSettings set password: %v", err)
	}

	challenge, err := svc.GetPassword(ctx, userID)
	if err != nil {
		t.Fatalf("GetPassword challenge: %v", err)
	}
	if !challenge.HasPassword || challenge.SRPID == 0 || len(challenge.SRPB) == 0 {
		t.Fatalf("challenge = %+v, want srp password challenge", challenge)
	}
	check := clientPasswordCheck(t, challenge, []byte("correct horse"))
	if err := svc.CheckPassword(ctx, userID, check); err != nil {
		t.Fatalf("CheckPassword valid SRP: %v", err)
	}

	private, err := svc.GetPasswordSettings(ctx, userID, check)
	if err != nil {
		t.Fatalf("GetPasswordSettings valid SRP: %v", err)
	}
	if private.Email != "alice@example.com" {
		t.Fatalf("private email = %q, want alice@example.com", private.Email)
	}

	bad := check
	bad.M1 = append([]byte(nil), check.M1...)
	bad.M1[0] ^= 0xFF
	if err := svc.CheckPassword(ctx, userID, bad); !errors.Is(err, domain.ErrPasswordHashInvalid) {
		t.Fatalf("CheckPassword bad M1 err = %v, want ErrPasswordHashInvalid", err)
	}
}

func TestRecoverPasswordClearsTwoFactorPassword(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1002
	sender := &captureMailSender{}
	svc := NewService(memory.NewPasswordStore(),
		WithLoginEmailVerification(memory.NewCodeStore(), sender, time.Minute, 3, 6))

	initial, err := svc.GetPassword(ctx, userID)
	if err != nil {
		t.Fatalf("GetPassword initial: %v", err)
	}
	algo := initial.NewAlgo
	algo.Salt1 = append(append([]byte(nil), algo.Salt1...), bytes.Repeat([]byte{0x5C}, 32)...)
	if err := svc.UpdatePasswordSettings(ctx, userID, domain.PasswordCheck{Empty: true}, domain.PasswordInputSettings{
		NewAlgo:         &algo,
		NewPasswordHash: verifierForPassword(algo, []byte("old password")),
		Email:           "bob@example.com",
		HasEmail:        true,
	}); err != nil {
		t.Fatalf("UpdatePasswordSettings set password: %v", err)
	}

	pattern, err := svc.RequestPasswordRecovery(ctx, userID)
	if err != nil {
		t.Fatalf("RequestPasswordRecovery: %v", err)
	}
	if pattern != "b***b@example.com" {
		t.Fatalf("recovery pattern = %q, want masked email", pattern)
	}
	if sender.to != "bob@example.com" || sender.code == "" || len(sender.requests) != 1 || sender.requests[0].Purpose != otpdelivery.PurposePasswordRecovery {
		t.Fatalf("recovery delivery = %+v, want one password-recovery email", sender)
	}
	if err := svc.CheckRecoveryPassword(ctx, userID, sender.code); err != nil {
		t.Fatalf("CheckRecoveryPassword: %v", err)
	}
	if err := svc.RecoverPassword(ctx, userID, sender.code, nil); err != nil {
		t.Fatalf("RecoverPassword clear: %v", err)
	}
	cleared, err := svc.GetPassword(ctx, userID)
	if err != nil {
		t.Fatalf("GetPassword cleared: %v", err)
	}
	if cleared.HasPassword || cleared.HasRecovery {
		t.Fatalf("cleared settings = %+v, want no password/recovery", cleared)
	}
}

func TestPasswordRecoveryFailsClosedWithoutSenderOrIssuedCode(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1012
	passwords := memory.NewPasswordStore()
	if err := passwords.Save(ctx, userID, domain.PasswordSettings{
		HasPassword: true, HasRecovery: true, RecoveryEmail: "owner@example.test",
		SRPID: 7, SRPVerifier: []byte{1, 2, 3},
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(passwords)
	if _, err := svc.RequestPasswordRecovery(ctx, userID); !errors.Is(err, domain.ErrPasswordRecoveryNA) {
		t.Fatalf("RequestPasswordRecovery err=%v, want unavailable", err)
	}
	if err := svc.RecoverPassword(ctx, userID, "12345", nil); !errors.Is(err, domain.ErrPasswordRecoveryExpired) {
		t.Fatalf("standing fixed code err=%v, want expired", err)
	}
	if err := NewService(nil).RecoverPassword(ctx, userID, "12345", nil); !errors.Is(err, domain.ErrPasswordRecoveryExpired) {
		t.Fatalf("missing password store err=%v, want fail-closed expiry", err)
	}
}

func TestPasswordRecoveryAttemptLimitAndStateBinding(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1013
	passwords := memory.NewPasswordStore()
	settings := domain.PasswordSettings{
		HasPassword: true, HasRecovery: true, RecoveryEmail: "owner@example.test",
		SRPID: 8, SRPVerifier: []byte{4, 5, 6},
	}
	if err := passwords.Save(ctx, userID, settings); err != nil {
		t.Fatal(err)
	}
	sender := &captureMailSender{}
	svc := NewService(passwords,
		WithLoginEmailVerification(memory.NewCodeStore(), sender, time.Minute, 3, 6))
	if _, err := svc.RequestPasswordRecovery(ctx, userID); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := svc.CheckRecoveryPassword(ctx, userID, "000000"); !errors.Is(err, domain.ErrRecoveryCodeInvalid) {
			t.Fatalf("wrong attempt %d err=%v, want invalid", attempt, err)
		}
	}
	if err := svc.CheckRecoveryPassword(ctx, userID, sender.code); !errors.Is(err, domain.ErrPasswordRecoveryExpired) {
		t.Fatalf("code after attempt limit err=%v, want expired", err)
	}

	if _, err := svc.RequestPasswordRecovery(ctx, userID); err != nil {
		t.Fatal(err)
	}
	issued := sender.code
	settings.SRPID++
	if err := passwords.Save(ctx, userID, settings); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckRecoveryPassword(ctx, userID, issued); !errors.Is(err, domain.ErrPasswordRecoveryExpired) {
		t.Fatalf("code after 2FA state change err=%v, want expired", err)
	}
}

func TestConcurrentPasswordRecoveryHasSingleConsumer(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1014
	passwords := memory.NewPasswordStore()
	if err := passwords.Save(ctx, userID, domain.PasswordSettings{
		HasPassword: true, HasRecovery: true, RecoveryEmail: "owner@example.test",
		SRPID: 9, SRPVerifier: []byte{7, 8, 9},
	}); err != nil {
		t.Fatal(err)
	}
	sender := &captureMailSender{}
	svc := NewService(passwords,
		WithLoginEmailVerification(memory.NewCodeStore(), sender, time.Minute, 5, 6))
	if _, err := svc.RequestPasswordRecovery(ctx, userID); err != nil {
		t.Fatal(err)
	}
	const workers = 24
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- svc.RecoverPassword(ctx, userID, sender.code, nil)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, domain.ErrPasswordRecoveryExpired) {
			t.Fatalf("concurrent recovery err=%v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful recoveries=%d, want 1", successes)
	}
}

func TestPasswordRecoveryDeliveryFailureRemovesIssuedCode(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1015
	passwords := memory.NewPasswordStore()
	if err := passwords.Save(ctx, userID, domain.PasswordSettings{
		HasPassword: true, HasRecovery: true, RecoveryEmail: "owner@example.test",
		SRPID: 10, SRPVerifier: []byte{10},
	}); err != nil {
		t.Fatal(err)
	}
	sender := &captureMailSender{err: errors.New("provider rejected request")}
	svc := NewService(passwords,
		WithLoginEmailVerification(memory.NewCodeStore(), sender, time.Minute, 5, 6))
	if _, err := svc.RequestPasswordRecovery(ctx, userID); err == nil {
		t.Fatal("RequestPasswordRecovery succeeded after known delivery failure")
	}
	if err := svc.CheckRecoveryPassword(ctx, userID, sender.code); !errors.Is(err, domain.ErrPasswordRecoveryExpired) {
		t.Fatalf("undelivered code err=%v, want expired", err)
	}
}

func TestPasswordRecoveryUnknownDeliveryOutcomeKeepsIssuedCode(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1016
	passwords := memory.NewPasswordStore()
	if err := passwords.Save(ctx, userID, domain.PasswordSettings{
		HasPassword: true, HasRecovery: true, RecoveryEmail: "owner@example.test",
		SRPID: 11, SRPVerifier: []byte{11},
	}); err != nil {
		t.Fatal(err)
	}
	sender := &captureMailSender{err: &otpdelivery.OutcomeUnknownError{Cause: errors.New("provider ACK lost")}}
	svc := NewService(passwords,
		WithLoginEmailVerification(memory.NewCodeStore(), sender, time.Minute, 5, 6))
	if _, err := svc.RequestPasswordRecovery(ctx, userID); err != nil {
		t.Fatalf("RequestPasswordRecovery outcome-unknown err=%v", err)
	}
	if err := svc.CheckRecoveryPassword(ctx, userID, sender.code); err != nil {
		t.Fatalf("outcome-unknown code was discarded: %v", err)
	}
}

func TestResetPasswordWaitAndDecline(t *testing.T) {
	ctx := context.Background()
	const userID int64 = 1003
	passwords := memory.NewPasswordStore()
	svc := NewService(passwords)

	if err := passwords.Save(ctx, userID, domain.PasswordSettings{HasPassword: true}); err != nil {
		t.Fatalf("save password settings: %v", err)
	}
	result, err := svc.ResetPassword(ctx, userID)
	if err != nil {
		t.Fatalf("ResetPassword request: %v", err)
	}
	if result.Kind != domain.PasswordResetRequestedWait || result.UntilDate <= int(time.Now().Unix()) {
		t.Fatalf("reset result = %+v, want requested future wait", result)
	}
	pending, _, err := passwords.GetByUser(ctx, userID)
	if err != nil || pending.PendingResetDate != result.UntilDate {
		t.Fatalf("pending reset = %+v found err=%v, want until date", pending, err)
	}

	if err := svc.DeclinePasswordReset(ctx, userID); err != nil {
		t.Fatalf("DeclinePasswordReset: %v", err)
	}
	declined, _, err := passwords.GetByUser(ctx, userID)
	if err != nil || declined.PendingResetDate != 0 {
		t.Fatalf("declined settings = %+v err=%v, want no pending reset", declined, err)
	}

	declined.PendingResetDate = int(time.Now().Add(-time.Second).Unix())
	if err := passwords.Save(ctx, userID, declined); err != nil {
		t.Fatalf("save expired reset: %v", err)
	}
	result, err = svc.ResetPassword(ctx, userID)
	if err != nil {
		t.Fatalf("ResetPassword finalize: %v", err)
	}
	if result.Kind != domain.PasswordResetOK {
		t.Fatalf("final reset result = %+v, want ok", result)
	}
	cleared, _, err := passwords.GetByUser(ctx, userID)
	if err != nil || cleared.HasPassword || cleared.PendingResetDate != 0 {
		t.Fatalf("cleared settings = %+v err=%v, want password cleared", cleared, err)
	}
}

func clientPasswordCheck(t *testing.T, settings domain.PasswordSettings, password []byte) domain.PasswordCheck {
	t.Helper()
	algo := settings.NewAlgo
	if settings.CurrentAlgo != nil {
		algo = *settings.CurrentAlgo
	}
	p := new(big.Int).SetBytes(algo.P)
	g := big.NewInt(int64(algo.G))
	a := new(big.Int).SetBytes(bytes.Repeat([]byte{0x23}, passwordHashSize))
	A := new(big.Int).Exp(g, a, p)
	aForHash := padToHash(A.Bytes())
	bForHash := padToHash(settings.SRPB)
	x := new(big.Int).SetBytes(passwordDigest(algo, password))
	u := new(big.Int).SetBytes(hashBytes(aForHash, bForHash))
	k := new(big.Int).SetBytes(hashBytes(padToHash(algo.P), padToHash(g.Bytes())))
	gx := new(big.Int).Exp(g, x, p)
	kgx := new(big.Int).Mul(k, gx)
	kgx.Mod(kgx, p)
	b := new(big.Int).SetBytes(settings.SRPB)
	base := new(big.Int).Sub(b, kgx)
	base.Mod(base, p)
	exp := new(big.Int).Mul(u, x)
	exp.Add(exp, a)
	s := new(big.Int).Exp(base, exp, p)
	kBytes := hashBytes(padToHash(s.Bytes()))
	m1 := hashBytes(
		xorBytes(hashBytes(padToHash(algo.P)), hashBytes(padToHash(g.Bytes()))),
		hashBytes(algo.Salt1),
		hashBytes(algo.Salt2),
		aForHash,
		bForHash,
		kBytes,
	)
	return domain.PasswordCheck{SRPID: settings.SRPID, A: aForHash, M1: m1}
}

// passwordDigest and verifierForPassword are client-side (plaintext password -> verifier) simulation helpers;
// the server never takes the plaintext-password path, so these only build test inputs for the client SRP helper here.
func passwordDigest(algo domain.PasswordKDFAlgo, password []byte) []byte {
	hash1 := hashBytes(algo.Salt1, password, algo.Salt1)
	hash2 := hashBytes(algo.Salt2, hash1, algo.Salt2)
	hash3 := pbkdf2.Key(hash2, algo.Salt1, 100000, 64, sha512.New)
	return hashBytes(algo.Salt2, hash3, algo.Salt2)
}

func verifierForPassword(algo domain.PasswordKDFAlgo, password []byte) []byte {
	p := new(big.Int).SetBytes(algo.P)
	g := big.NewInt(int64(algo.G))
	x := new(big.Int).SetBytes(passwordDigest(algo, password))
	return padToHash(new(big.Int).Exp(g, x, p).Bytes())
}
