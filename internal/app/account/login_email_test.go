package account

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func newLoginEmailService(t *testing.T) (*Service, *memory.UserStore) {
	t.Helper()
	users := memory.NewUserStore()
	svc := NewService(memory.NewPasswordStore(), WithUsers(users))
	return svc, users
}

func createUser(t *testing.T, users *memory.UserStore, phone string) domain.User {
	t.Helper()
	u, err := users.Create(context.Background(), domain.User{Phone: phone, FirstName: "Test"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

type captureMailSender struct {
	to   string
	code string
}

type blockingCodeCAS struct {
	store.CodeStore
	mu            sync.Mutex
	blockRevision string
	blockUpdate   bool
	blockDelete   bool
	entered       chan struct{}
	release       chan struct{}
	once          sync.Once
}

type switchableEmailOwnerStore struct {
	store.UserStore
	mu       sync.RWMutex
	phone    string
	override bool
	owner    domain.User
	found    bool
}

func (s *switchableEmailOwnerStore) ByPhone(ctx context.Context, phone string) (domain.User, bool, error) {
	s.mu.RLock()
	if s.override && domain.NormalizePhone(phone) == s.phone {
		owner, found := s.owner, s.found
		s.mu.RUnlock()
		return owner, found, nil
	}
	s.mu.RUnlock()
	return s.UserStore.ByPhone(ctx, phone)
}

func (s *switchableEmailOwnerStore) switchOwner(phone string, owner domain.User) {
	s.mu.Lock()
	s.phone = domain.NormalizePhone(phone)
	s.owner = owner
	s.found = true
	s.override = true
	s.mu.Unlock()
}

type afterSavePasswordStore struct {
	store.PasswordStore
	once      sync.Once
	afterSave func(userID int64, settings domain.PasswordSettings)
}

func (s *afterSavePasswordStore) Save(ctx context.Context, userID int64, settings domain.PasswordSettings) error {
	if err := s.PasswordStore.Save(ctx, userID, settings); err != nil {
		return err
	}
	if s.afterSave != nil {
		s.once.Do(func() { s.afterSave(userID, settings) })
	}
	return nil
}

func (s *blockingCodeCAS) shouldBlock(revision string, update bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return revision == s.blockRevision && ((update && s.blockUpdate) || (!update && s.blockDelete))
}

func (s *blockingCodeCAS) waitIfBlocked(revision string, update bool) {
	if !s.shouldBlock(revision, update) {
		return
	}
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
}

func (s *blockingCodeCAS) CompareAndUpdate(ctx context.Context, key, revision string, next store.PhoneCode) (bool, error) {
	s.waitIfBlocked(revision, true)
	return s.CodeStore.CompareAndUpdate(ctx, key, revision, next)
}

func (s *blockingCodeCAS) CompareAndDelete(ctx context.Context, key, revision string) (bool, error) {
	s.waitIfBlocked(revision, false)
	return s.CodeStore.CompareAndDelete(ctx, key, revision)
}

func (s *captureMailSender) SendLoginCode(_ context.Context, to, code string, _ time.Duration) error {
	s.to = to
	s.code = code
	return nil
}

// TestSetLoginEmailPersistsAndMasks 设置登录邮箱后，GetPassword 下发掩码 pattern，原始
// 地址只在 LoginEmail 读路径可见。
func TestSetLoginEmailPersistsAndMasks(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	u := createUser(t, users, "15550010001")

	if err := svc.SetLoginEmail(ctx, u.ID, "alice@example.com"); err != nil {
		t.Fatalf("SetLoginEmail: %v", err)
	}

	settings, err := svc.GetPassword(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetPassword: %v", err)
	}
	if got, want := settings.LoginEmailPattern, "a***e@example.com"; got != want {
		t.Fatalf("LoginEmailPattern = %q, want %q", got, want)
	}
	if settings.LoginEmail != "alice@example.com" {
		t.Fatalf("LoginEmail = %q, want raw address", settings.LoginEmail)
	}

	email, found, err := svc.LoginEmail(ctx, u.ID)
	if err != nil || !found || email != "alice@example.com" {
		t.Fatalf("LoginEmail = %q found=%v err=%v", email, found, err)
	}
}

// TestLoginEmailByPhoneAndClear 验证 sendCode 可按手机号读取，但 reset 只按已锁定 userID 清除。
func TestLoginEmailByPhoneAndClear(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	u := createUser(t, users, "15550010002")

	if err := svc.SetLoginEmail(ctx, u.ID, "bob@mail.com"); err != nil {
		t.Fatalf("SetLoginEmail: %v", err)
	}
	email, found, err := svc.LoginEmailByPhone(ctx, "15550010002")
	if err != nil || !found || email != "bob@mail.com" {
		t.Fatalf("LoginEmailByPhone = %q found=%v err=%v", email, found, err)
	}

	if err := svc.ClearLoginEmail(ctx, u.ID); err != nil {
		t.Fatalf("ClearLoginEmail: %v", err)
	}
	if _, found, _ := svc.LoginEmailByPhone(ctx, "15550010002"); found {
		t.Fatal("login email still present after clear")
	}
}

// TestSetLoginEmailRejectsInvalid 空/无 @ 的邮箱被拒。
func TestSetLoginEmailRejectsInvalid(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	u := createUser(t, users, "15550010003")

	for _, bad := range []string{"", "   ", "not-an-email"} {
		if err := svc.SetLoginEmail(ctx, u.ID, bad); !errors.Is(err, domain.ErrEmailInvalid) {
			t.Fatalf("SetLoginEmail(%q) err = %v, want ErrEmailInvalid", bad, err)
		}
	}
}

func TestSetLoginEmailRejectsDuplicateCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	u1 := createUser(t, users, "15550010103")
	u2 := createUser(t, users, "15550010104")

	if err := svc.SetLoginEmail(ctx, u1.ID, "Alice@Example.Test"); err != nil {
		t.Fatalf("SetLoginEmail user1: %v", err)
	}
	if err := svc.SetLoginEmail(ctx, u2.ID, "alice@example.test"); !errors.Is(err, domain.ErrEmailOccupied) {
		t.Fatalf("SetLoginEmail duplicate err = %v, want ErrEmailOccupied", err)
	}
	email, found, err := svc.LoginEmail(ctx, u1.ID)
	if err != nil || !found || email != "alice@example.test" {
		t.Fatalf("LoginEmail user1 = %q found=%v err=%v", email, found, err)
	}
}

// TestRecoveryEmailDoesNotLeakIntoLoginEmailPattern 是核心解耦回归：设置 2FA 恢复邮箱
// 不得把恢复邮箱掩码写进 login_email_pattern（历史 bug）。
func TestRecoveryEmailDoesNotLeakIntoLoginEmailPattern(t *testing.T) {
	ctx := context.Background()
	svc, users := newLoginEmailService(t)
	u := createUser(t, users, "15550010004")

	// 设置 2FA 恢复邮箱（email-only 路径即可触发历史 bug 的写入点）。
	if err := svc.UpdatePasswordSettings(ctx, u.ID, domain.PasswordCheck{Empty: true}, domain.PasswordInputSettings{
		Email:    "recovery@secret.com",
		HasEmail: true,
	}); err != nil {
		t.Fatalf("UpdatePasswordSettings: %v", err)
	}

	settings, err := svc.GetPassword(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetPassword: %v", err)
	}
	if settings.LoginEmailPattern != "" {
		t.Fatalf("LoginEmailPattern = %q, want empty (recovery email must not leak into login email)", settings.LoginEmailPattern)
	}
	if !settings.HasRecovery {
		t.Fatal("HasRecovery = false, want true after setting recovery email")
	}
}

func TestSendLoginEmailCodeRejectsDuplicateBeforeSending(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	passwords := memory.NewPasswordStore()
	sender := &captureMailSender{}
	svc := NewService(passwords,
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	u1 := createUser(t, users, "15550010105")
	u2 := createUser(t, users, "15550010106")
	if err := svc.SetLoginEmail(ctx, u1.ID, "taken@example.test"); err != nil {
		t.Fatalf("SetLoginEmail user1: %v", err)
	}

	if _, _, err := svc.SendLoginEmailCode(ctx, u2.ID, "", "", "TAKEN@example.test", false); !errors.Is(err, domain.ErrEmailOccupied) {
		t.Fatalf("SendLoginEmailCode duplicate err = %v, want ErrEmailOccupied", err)
	}
	if sender.to != "" || sender.code != "" {
		t.Fatalf("duplicate email sent to=%q code=%q, want no send", sender.to, sender.code)
	}
}

func TestLoginEmailSetupRejectsAlreadyOwnedEmailForNewPhone(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	passwords := memory.NewPasswordStore()
	sender := &captureMailSender{}
	svc := NewService(passwords,
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	owner := createUser(t, users, "15550010107")
	if err := svc.SetLoginEmail(ctx, owner.ID, "owner@example.test"); err != nil {
		t.Fatalf("SetLoginEmail owner: %v", err)
	}
	if err := codes.Set(ctx, "new-phone-hash", store.PhoneCode{Version: store.PhoneCodeVersionCurrent, Phone: "15550010108", Channel: "email_setup_required", MaxAttempts: 2}, time.Minute); err != nil {
		t.Fatalf("seed phone code: %v", err)
	}

	if _, _, err := svc.SendLoginEmailCode(ctx, 0, "+1 555 001 0108", "new-phone-hash", "OWNER@example.test", true); !errors.Is(err, domain.ErrEmailOccupied) {
		t.Fatalf("setup duplicate email err = %v, want ErrEmailOccupied", err)
	}
	if sender.to != "" || sender.code != "" {
		t.Fatalf("duplicate setup email sent to=%q code=%q, want no send", sender.to, sender.code)
	}
}

func TestSendVerifyLoginEmailPersistsOnlyAfterVerify(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	sender := &captureMailSender{}
	svc := NewService(memory.NewPasswordStore(),
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	u := createUser(t, users, "15550010005")

	pattern, length, err := svc.SendLoginEmailCode(ctx, u.ID, "", "", "alice@example.test", false)
	if err != nil {
		t.Fatalf("SendLoginEmailCode: %v", err)
	}
	if pattern != "a***e@example.test" || length != 6 || sender.to != "alice@example.test" || len(sender.code) != 6 {
		t.Fatalf("send result pattern=%q length=%d to=%q code=%q", pattern, length, sender.to, sender.code)
	}
	if _, found, err := svc.LoginEmail(ctx, u.ID); err != nil || found {
		t.Fatalf("LoginEmail before verify found=%v err=%v, want not found", found, err)
	}
	email, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", sender.code, false)
	if err != nil {
		t.Fatalf("VerifyLoginEmail: %v", err)
	}
	if email != "alice@example.test" {
		t.Fatalf("verified email = %q", email)
	}
	got, found, err := svc.LoginEmail(ctx, u.ID)
	if err != nil || !found || got != "alice@example.test" {
		t.Fatalf("LoginEmail after verify = %q found=%v err=%v", got, found, err)
	}
}

func TestLoginEmailSetupStoresPendingEmailOnPhoneCodeHash(t *testing.T) {
	ctx := context.Background()
	codes := memory.NewCodeStore()
	sender := &captureMailSender{}
	svc := NewService(memory.NewPasswordStore(),
		WithUsers(memory.NewUserStore()),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	if err := codes.Set(ctx, "phone-hash", store.PhoneCode{Version: store.PhoneCodeVersionCurrent, Phone: "15550010006", Channel: "email_setup_required", MaxAttempts: 2}, time.Minute); err != nil {
		t.Fatalf("seed phone code: %v", err)
	}

	if _, _, err := svc.SendLoginEmailCode(ctx, 0, "+1 555 001 0006", "phone-hash", "new@example.test", true); err != nil {
		t.Fatalf("SendLoginEmailCode setup: %v", err)
	}
	email, err := svc.VerifyLoginEmail(ctx, 0, "+1 555 001 0006", "phone-hash", sender.code, true)
	if err != nil {
		t.Fatalf("VerifyLoginEmail setup: %v", err)
	}
	if email != "new@example.test" {
		t.Fatalf("verified setup email = %q", email)
	}
	rec, found, err := codes.Get(ctx, "phone-hash")
	if err != nil || !found {
		t.Fatalf("phone code found=%v err=%v", found, err)
	}
	if rec.Channel != "email_login" || rec.Code != sender.code || rec.Email != "new@example.test" || !rec.VerifiedEmail || !rec.SignUpVerified || rec.PendingEmail != "new@example.test" {
		t.Fatalf("phone code after verify = %+v", rec)
	}
}

func TestVerifyLoginEmailDeletesCodeAfterMaxAttempts(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	sender := &captureMailSender{}
	svc := NewService(memory.NewPasswordStore(),
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	u := createUser(t, users, "15550010007")

	if _, _, err := svc.SendLoginEmailCode(ctx, u.ID, "", "", "limit@example.test", false); err != nil {
		t.Fatalf("SendLoginEmailCode: %v", err)
	}
	bad1 := "000000"
	if bad1 == sender.code {
		bad1 = "111111"
	}
	bad2 := "222222"
	if bad2 == sender.code {
		bad2 = "333333"
	}
	if _, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", bad1, false); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatalf("first bad VerifyLoginEmail err = %v, want ErrEmailCodeInvalid", err)
	}
	if _, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", bad2, false); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatalf("second bad VerifyLoginEmail err = %v, want ErrEmailCodeInvalid", err)
	}
	if _, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", sender.code, false); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatalf("VerifyLoginEmail after max attempts err = %v, want ErrEmailCodeInvalid", err)
	}
	if _, found, _ := svc.LoginEmail(ctx, u.ID); found {
		t.Fatal("login email was set after exhausted verification code")
	}
}

func TestStaleLoginEmailVerificationCannotMutateResentCode(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name             string
		blockUpdate      bool
		blockDelete      bool
		verificationCode func(old string) string
	}{
		{
			name:        "wrong-code-update",
			blockUpdate: true,
			verificationCode: func(old string) string {
				if old != "000000" {
					return "000000"
				}
				return "111111"
			},
		},
		{
			name:             "correct-code-delete",
			blockDelete:      true,
			verificationCode: func(old string) string { return old },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users := memory.NewUserStore()
			baseCodes := memory.NewCodeStore()
			codes := &blockingCodeCAS{CodeStore: baseCodes}
			passwords := memory.NewPasswordStore()
			sender := &captureMailSender{}
			svc := NewService(passwords,
				WithUsers(users),
				WithLoginEmailVerification(codes, sender, time.Minute, 3, 6))
			u := createUser(t, users, "155500102"+fmt.Sprint(10+len(tc.name)))
			if _, _, err := svc.SendLoginEmailCode(ctx, u.ID, "", "", "cas@example.test", false); err != nil {
				t.Fatalf("first SendLoginEmailCode: %v", err)
			}
			oldCode := sender.code
			key := loginEmailVerifyChangePrefix + fmt.Sprint(u.ID)
			oldSnapshot, found, err := baseCodes.GetSnapshot(ctx, key)
			if err != nil || !found {
				t.Fatalf("old snapshot found=%v err=%v", found, err)
			}
			codes.blockRevision = oldSnapshot.Revision
			codes.blockUpdate = tc.blockUpdate
			codes.blockDelete = tc.blockDelete
			codes.entered = make(chan struct{})
			codes.release = make(chan struct{})

			verifyErr := make(chan error, 1)
			go func() {
				_, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", tc.verificationCode(oldCode), false)
				verifyErr <- err
			}()
			<-codes.entered
			for attempts := 0; attempts < 5; attempts++ {
				if _, _, err := svc.SendLoginEmailCode(ctx, u.ID, "", "", "cas@example.test", false); err != nil {
					t.Fatalf("resent SendLoginEmailCode: %v", err)
				}
				if sender.code != oldCode {
					break
				}
			}
			newCode := sender.code
			if newCode == oldCode {
				t.Fatal("random resend repeatedly produced the old code")
			}
			newSnapshot, found, err := baseCodes.GetSnapshot(ctx, key)
			if err != nil || !found || newSnapshot.Revision == oldSnapshot.Revision {
				t.Fatalf("new snapshot=%+v found=%v err=%v", newSnapshot, found, err)
			}
			close(codes.release)
			if err := <-verifyErr; !errors.Is(err, domain.ErrEmailCodeInvalid) {
				t.Fatalf("stale verification err=%v, want ErrEmailCodeInvalid", err)
			}
			current, found, err := baseCodes.GetSnapshot(ctx, key)
			if err != nil || !found || current.Revision != newSnapshot.Revision || current.Record.Code != newCode || current.Record.Attempts != 0 {
				t.Fatalf("current code after stale verifier=%+v found=%v err=%v", current, found, err)
			}
			if _, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", newCode, false); err != nil {
				t.Fatalf("VerifyLoginEmail new code: %v", err)
			}
		})
	}
}

func TestConcurrentWrongLoginEmailCodesNeverAuthorize(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	passwords := memory.NewPasswordStore()
	sender := &captureMailSender{}
	svc := NewService(passwords,
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 2, 6))
	u := createUser(t, users, "15550010231")
	if _, _, err := svc.SendLoginEmailCode(ctx, u.ID, "", "", "wrong@example.test", false); err != nil {
		t.Fatalf("SendLoginEmailCode: %v", err)
	}
	correct := sender.code
	wrong := "000000"
	if wrong == correct {
		wrong = "111111"
	}
	const workers = 32
	start := make(chan struct{})
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			_, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", wrong, false)
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < workers; i++ {
		if err := <-errs; !errors.Is(err, domain.ErrEmailCodeInvalid) {
			t.Fatalf("wrong concurrent verification err=%v", err)
		}
	}
	if _, found, err := svc.LoginEmail(ctx, u.ID); err != nil || found {
		t.Fatalf("LoginEmail after wrong codes found=%v err=%v, want absent", found, err)
	}
	key := loginEmailVerifyChangePrefix + fmt.Sprint(u.ID)
	for attempts := 0; attempts < 3; attempts++ {
		if _, found, err := codes.GetSnapshot(ctx, key); err != nil {
			t.Fatalf("GetSnapshot after concurrent attempts: %v", err)
		} else if !found {
			break
		}
		if _, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", wrong, false); !errors.Is(err, domain.ErrEmailCodeInvalid) {
			t.Fatalf("final wrong verification err=%v", err)
		}
	}
	if _, err := svc.VerifyLoginEmail(ctx, u.ID, "", "", correct, false); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatalf("correct code after exhausted attempts err=%v, want invalid", err)
	}
}

func TestEmailSetupOwnerTransferDuringSaveNeverWritesFactorToNewOwner(t *testing.T) {
	ctx := context.Background()
	baseUsers := memory.NewUserStore()
	ownerA := createUser(t, baseUsers, "15550010241")
	ownerB := createUser(t, baseUsers, "15550010242")
	users := &switchableEmailOwnerStore{UserStore: baseUsers}
	basePasswords := memory.NewPasswordStore()
	passwords := &afterSavePasswordStore{PasswordStore: basePasswords}
	codes := memory.NewCodeStore()
	sender := &captureMailSender{}
	svc := NewService(passwords,
		WithUsers(users),
		WithLoginEmailVerification(codes, sender, time.Minute, 3, 6))
	hash := "owner-save-race"
	if err := codes.Set(ctx, hash, store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		IssuedUserID: ownerA.ID,
		Phone:        ownerA.Phone,
		Channel:      codeChannelEmailSetupRequired,
		MaxAttempts:  3,
	}, time.Minute); err != nil {
		t.Fatalf("seed phone code: %v", err)
	}
	if _, _, err := svc.SendLoginEmailCode(ctx, 0, ownerA.Phone, hash, "owner-a@example.test", true); err != nil {
		t.Fatalf("SendLoginEmailCode: %v", err)
	}
	passwords.afterSave = func(userID int64, settings domain.PasswordSettings) {
		if userID == ownerA.ID && settings.LoginEmail == "owner-a@example.test" {
			users.switchOwner(ownerA.Phone, ownerB)
		}
	}
	if _, err := svc.VerifyLoginEmail(ctx, 0, ownerA.Phone, hash, sender.code, true); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatalf("VerifyLoginEmail across save-time owner transfer err=%v, want invalid", err)
	}
	if settings, found, err := basePasswords.GetByUser(ctx, ownerB.ID); err != nil || (found && settings.LoginEmail != "") {
		t.Fatalf("new owner settings=%+v found=%v err=%v, SMTP factor leaked to B", settings, found, err)
	}
	if _, found, err := codes.GetSnapshot(ctx, hash); err != nil || found {
		t.Fatalf("owner-drift phone hash found=%v err=%v, want invalidated", found, err)
	}
}
