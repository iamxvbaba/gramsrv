package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	accountapp "telesrv/internal/app/account"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestSignUpRequiresCorrectSignInAndConsumesMarkerOnce(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345")
	phone := "15550009301"

	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if _, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, hash, "Direct", "Bypass"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("direct SignUp err=%v, want ErrCodeInvalid", err)
	}
	if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, phone, hash, "00000"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("wrong SignIn err=%v, want ErrCodeInvalid", err)
	}
	if rec, found, err := codes.Get(ctx, hash); err != nil || !found || rec.SignUpVerified {
		t.Fatalf("wrong code marker=%v found=%v err=%v, want live/unverified", rec.SignUpVerified, found, err)
	}
	if _, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, hash, "Wrong", "Code"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("SignUp after wrong code err=%v, want ErrCodeInvalid", err)
	}

	verifyCodeForSignUp(t, svc, phone, hash, "12345")
	if rec, found, err := codes.Get(ctx, hash); err != nil || !found || !rec.SignUpVerified || rec.IssuedUserID != 0 {
		t.Fatalf("verified record=%+v found=%v err=%v", rec, found, err)
	}
	if _, msg, needSignUp, err := svc.SignIn(ctx, domain.Authorization{}, phone, hash, "12345"); err != nil || !needSignUp || msg.ID != 0 {
		t.Fatalf("idempotent SignIn needSignUp=%v message=%+v err=%v", needSignUp, msg, err)
	}
	u, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, hash, "Verified", "User")
	if err != nil || u.Phone != phone {
		t.Fatalf("verified SignUp user=%+v err=%v", u, err)
	}
	if _, _, err := svc.SignUp(ctx, domain.Authorization{}, phone, hash, "Replay", "User"); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("replayed SignUp err=%v, want ErrCodeExpired", err)
	}
}

func TestConcurrentSignUpConsumesVerifiedHashExactlyOnce(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	svc := NewService(users, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345")
	phone := "15550009302"
	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	verifyCodeForSignUp(t, svc, phone, hash, "12345")

	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			<-start
			var key [8]byte
			key[0] = byte(i + 1)
			_, _, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: key}, phone, hash, "Concurrent", "User")
			errs <- err
		}(i)
	}
	close(start)
	successes := 0
	for i := 0; i < workers; i++ {
		err := <-errs
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrCodeExpired), errors.Is(err, ErrCodeInvalid):
		default:
			t.Fatalf("concurrent SignUp err=%v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful SignUp calls=%d, want 1", successes)
	}
}

type afterVerifyCodeStore struct {
	store.CodeStore
	once        sync.Once
	afterVerify func()
}

type failingPasswordStore struct {
	store.PasswordStore
	err error
}

func (s *failingPasswordStore) GetByUser(context.Context, int64) (domain.PasswordSettings, bool, error) {
	return domain.PasswordSettings{}, false, s.err
}

type switchablePhoneOwnerStore struct {
	store.UserStore
	mu       sync.RWMutex
	phone    string
	override bool
	owner    domain.User
	found    bool
}

func (s *switchablePhoneOwnerStore) ByPhone(ctx context.Context, phone string) (domain.User, bool, error) {
	s.mu.RLock()
	if s.override && domain.NormalizePhone(phone) == s.phone {
		owner, found := s.owner, s.found
		s.mu.RUnlock()
		return owner, found, nil
	}
	s.mu.RUnlock()
	return s.UserStore.ByPhone(ctx, phone)
}

func (s *switchablePhoneOwnerStore) setOwnerView(phone string, owner domain.User, found bool) {
	s.mu.Lock()
	s.phone = domain.NormalizePhone(phone)
	s.owner = owner
	s.found = found
	s.override = true
	s.mu.Unlock()
}

func (s *switchablePhoneOwnerStore) resetOwnerView() {
	s.mu.Lock()
	s.override = false
	s.mu.Unlock()
}

func (s *afterVerifyCodeStore) VerifyLogin(ctx context.Context, hash, phone, code string, keep bool, maxAttempts int) (store.LoginCodeVerifyResult, error) {
	result, err := s.CodeStore.VerifyLogin(ctx, hash, phone, code, keep, maxAttempts)
	if err == nil && result.Status == store.LoginCodeVerifyAccepted && s.afterVerify != nil {
		s.once.Do(s.afterVerify)
	}
	return result, err
}

func TestOwnerTransferAcrossVerifyInvalidatesHashPermanently(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	baseCodes := memory.NewCodeStore()
	var createErr error
	codes := &afterVerifyCodeStore{CodeStore: baseCodes}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345")
	phone := "15550009303"
	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	codes.afterVerify = func() {
		_, createErr = users.Create(ctx, domain.User{Phone: phone, FirstName: "NewOwner"})
	}
	if _, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{}, phone, hash, "12345"); !errors.Is(err, ErrCodeInvalid) || needSignUp {
		t.Fatalf("SignIn across owner transfer needSignUp=%v err=%v, want invalid", needSignUp, err)
	}
	if createErr != nil {
		t.Fatalf("create concurrent owner: %v", createErr)
	}
	if _, found, err := baseCodes.Get(ctx, hash); err != nil || found {
		t.Fatalf("owner-drift hash found=%v err=%v, want invalidated", found, err)
	}
}

func TestPasswordLookupFailureNeverCreatesOrChangesAuthorization(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	target, err := users.Create(ctx, domain.User{Phone: "15550009320", FirstName: "Target"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	previous, err := users.Create(ctx, domain.User{Phone: "15550009321", FirstName: "Previous"})
	if err != nil {
		t.Fatalf("create previous: %v", err)
	}
	authz := memory.NewAuthorizationStore()
	lookupErr := errors.New("password store unavailable")
	passwords := &failingPasswordStore{PasswordStore: memory.NewPasswordStore(), err: lookupErr}
	svc := NewService(users, authz, memory.NewCodeStore(), nil, nil, "12345",
		WithPasswords(passwords),
		WithLoginCodeDelivery(&captureLoginCodeDelivery{}),
	)

	t.Run("unbound-key-remains-unbound", func(t *testing.T) {
		key := [8]byte{0xC1}
		hash, err := svc.SendCode(ctx, target.Phone)
		if err != nil {
			t.Fatalf("SendCode: %v", err)
		}
		if _, _, _, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, target.Phone, hash, "12345"); !errors.Is(err, lookupErr) {
			t.Fatalf("SignIn err=%v, want password lookup failure", err)
		}
		if got, found, err := authz.ByAuthKey(ctx, key); err != nil || found {
			t.Fatalf("authorization=%+v found=%v err=%v, want absent", got, found, err)
		}
	})

	t.Run("previous-binding-remains-unchanged", func(t *testing.T) {
		key := [8]byte{0xC2}
		original := domain.Authorization{AuthKeyID: key, UserID: previous.ID, Hash: 987654321}
		if err := authz.Bind(ctx, original); err != nil {
			t.Fatalf("bind previous authorization: %v", err)
		}
		hash, err := svc.SendCode(ctx, target.Phone)
		if err != nil {
			t.Fatalf("SendCode: %v", err)
		}
		if _, _, _, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: key}, target.Phone, hash, "12345"); !errors.Is(err, lookupErr) {
			t.Fatalf("SignIn err=%v, want password lookup failure", err)
		}
		got, found, err := authz.ByAuthKey(ctx, key)
		if err != nil || !found || got.UserID != previous.ID || got.Hash != original.Hash || got.PasswordPending != original.PasswordPending {
			t.Fatalf("authorization after failure=%+v found=%v err=%v, want unchanged %+v", got, found, err, original)
		}
	})
}

func TestOwnerTransferAwayAndBackCannotReviveLoginHash(t *testing.T) {
	ctx := context.Background()
	t.Run("unregistered-signin", func(t *testing.T) {
		baseUsers := memory.NewUserStore()
		other, err := baseUsers.Create(ctx, domain.User{Phone: "15550009311", FirstName: "Other"})
		if err != nil {
			t.Fatalf("create other owner: %v", err)
		}
		users := &switchablePhoneOwnerStore{UserStore: baseUsers}
		codes := memory.NewCodeStore()
		svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345")
		phone := "15550009310"
		hash, err := svc.SendCode(ctx, phone)
		if err != nil {
			t.Fatalf("SendCode: %v", err)
		}
		users.setOwnerView(phone, other, true)
		if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, phone, hash, "12345"); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("SignIn after 0->B owner transfer err=%v, want invalid", err)
		}
		users.resetOwnerView()
		if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, phone, hash, "12345"); !errors.Is(err, ErrCodeExpired) {
			t.Fatalf("SignIn after 0->B->0 err=%v, want expired", err)
		}
	})

	t.Run("existing-resend", func(t *testing.T) {
		baseUsers := memory.NewUserStore()
		ownerA, err := baseUsers.Create(ctx, domain.User{Phone: "15550009312", FirstName: "A"})
		if err != nil {
			t.Fatalf("create owner A: %v", err)
		}
		ownerB, err := baseUsers.Create(ctx, domain.User{Phone: "15550009313", FirstName: "B"})
		if err != nil {
			t.Fatalf("create owner B: %v", err)
		}
		users := &switchablePhoneOwnerStore{UserStore: baseUsers}
		codes := memory.NewCodeStore()
		svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(&captureLoginCodeDelivery{}))
		hash, err := svc.SendCode(ctx, ownerA.Phone)
		if err != nil {
			t.Fatalf("SendCode: %v", err)
		}
		users.setOwnerView(ownerA.Phone, ownerB, true)
		if _, err := svc.ResendCode(ctx, ownerA.Phone, hash); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("ResendCode after A->B err=%v, want invalid", err)
		}
		users.resetOwnerView()
		if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, ownerA.Phone, hash, "12345"); !errors.Is(err, ErrCodeExpired) {
			t.Fatalf("SignIn after A->B->A err=%v, want expired", err)
		}
	})

	t.Run("existing-cancel", func(t *testing.T) {
		baseUsers := memory.NewUserStore()
		ownerA, err := baseUsers.Create(ctx, domain.User{Phone: "15550009314", FirstName: "A"})
		if err != nil {
			t.Fatalf("create owner A: %v", err)
		}
		ownerB, err := baseUsers.Create(ctx, domain.User{Phone: "15550009315", FirstName: "B"})
		if err != nil {
			t.Fatalf("create owner B: %v", err)
		}
		users := &switchablePhoneOwnerStore{UserStore: baseUsers}
		codes := memory.NewCodeStore()
		svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(&captureLoginCodeDelivery{}))
		hash, err := svc.SendCode(ctx, ownerA.Phone)
		if err != nil {
			t.Fatalf("SendCode: %v", err)
		}
		users.setOwnerView(ownerA.Phone, ownerB, true)
		if err := svc.CancelCode(ctx, ownerA.Phone, hash); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("CancelCode after A->B err=%v, want invalid", err)
		}
		users.resetOwnerView()
		if _, _, _, err := svc.SignIn(ctx, domain.Authorization{}, ownerA.Phone, hash, "12345"); !errors.Is(err, ErrCodeExpired) {
			t.Fatalf("SignIn after canceled A->B->A err=%v, want expired", err)
		}
	})
}

func TestEmailSetupVerificationAuthorizesSignUpWithout777000Message(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	codes := memory.NewCodeStore()
	passwords := memory.NewPasswordStore()
	sender := &testMailSender{}
	accountSvc := accountapp.NewService(passwords,
		accountapp.WithUsers(users),
		accountapp.WithLoginEmailVerification(codes, sender, time.Minute, 3, 6),
	)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	authSvc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345",
		WithLoginMessages(messages, dialogs),
		WithLoginEmail(LoginEmailOptions{
			Enabled:      true,
			RequireSetup: true,
			CodeLength:   6,
			Store:        accountSvc,
			Sender:       sender,
		}),
	)
	phone := "15550009304"
	hash, err := authSvc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if _, _, err := authSvc.SignUp(ctx, domain.Authorization{}, phone, hash, "Direct", "Email"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("SignUp before email setup err=%v, want ErrCodeInvalid", err)
	}
	if _, _, _, err := authSvc.SignIn(ctx, domain.Authorization{}, phone, hash, "12345"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("WebK SignIn with setup-required placeholder err=%v, want ErrCodeInvalid", err)
	}
	if _, _, _, err := authSvc.SignInWithEmail(ctx, domain.Authorization{}, phone, hash, "12345"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("native SignInWithEmail with setup-required placeholder err=%v, want ErrCodeInvalid", err)
	}
	if _, _, err := accountSvc.SendLoginEmailCode(ctx, 0, phone, hash, "new@example.test", true); err != nil {
		t.Fatalf("SendLoginEmailCode: %v", err)
	}
	bad := wrongCode(sender.code, '0')
	if _, err := accountSvc.VerifyLoginEmail(ctx, 0, phone, hash, bad, true); !errors.Is(err, domain.ErrEmailCodeInvalid) {
		t.Fatalf("wrong VerifyLoginEmail err=%v, want ErrEmailCodeInvalid", err)
	}
	if rec, found, err := codes.Get(ctx, hash); err != nil || !found || rec.SignUpVerified {
		t.Fatalf("wrong SMTP code marker=%v found=%v err=%v", rec.SignUpVerified, found, err)
	}
	if _, err := accountSvc.VerifyLoginEmail(ctx, 0, phone, hash, sender.code, true); err != nil {
		t.Fatalf("VerifyLoginEmail: %v", err)
	}
	if rec, found, err := codes.Get(ctx, hash); err != nil || !found || !rec.SignUpVerified || rec.Channel != codeChannelEmailLogin {
		t.Fatalf("email-verified phone code=%+v found=%v err=%v", rec, found, err)
	}
	if _, msg, needSignUp, err := authSvc.SignInWithEmail(ctx, domain.Authorization{}, phone, hash, sender.code); err != nil || !needSignUp || msg.ID != 0 {
		t.Fatalf("SignInWithEmail after setup needSignUp=%v message=%+v err=%v", needSignUp, msg, err)
	}
	u, msg, err := authSvc.SignUp(ctx, domain.Authorization{}, phone, hash, "Email", "User")
	if err != nil {
		t.Fatalf("SignUp after email setup: %v", err)
	}
	if msg.ID != 0 || msg.Body != "" {
		t.Fatalf("email SignUp returned SMTP code message: %+v", msg)
	}
	list, err := dialogs.ListByUser(ctx, u.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list.Dialogs) != 0 || len(list.Messages) != 0 {
		t.Fatalf("email SignUp created 777000 bootstrap state: dialogs=%+v messages=%+v", list.Dialogs, list.Messages)
	}
	if email, found, err := accountSvc.LoginEmailByPhone(ctx, phone); err != nil || !found || email != "new@example.test" {
		t.Fatalf("LoginEmailByPhone email=%q found=%v err=%v", email, found, err)
	}
}

func TestConsumeLoginEmailResetRequiresExactIssuedHash(t *testing.T) {
	ctx := context.Background()
	baseUsers := memory.NewUserStore()
	owner, err := baseUsers.Create(ctx, domain.User{Phone: "15550009330", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	other, err := baseUsers.Create(ctx, domain.User{Phone: "15550009331", FirstName: "Other"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	users := &switchablePhoneOwnerStore{UserStore: baseUsers}
	codes := memory.NewCodeStore()
	delivery := &captureLoginCodeDelivery{}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))
	seed := func(hash, channel string) {
		t.Helper()
		if err := codes.Set(ctx, hash, store.PhoneCode{
			Version:      store.PhoneCodeVersionCurrent,
			IssuedUserID: owner.ID,
			Phone:        owner.Phone,
			Code:         "654321",
			Channel:      channel,
			MaxAttempts:  5,
		}, time.Minute); err != nil {
			t.Fatalf("seed %s: %v", hash, err)
		}
	}

	if _, err := svc.ConsumeLoginEmailReset(ctx, owner.Phone, "arbitrary-missing"); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("arbitrary hash err=%v, want expired", err)
	}
	seed("wrong-phone", codeChannelEmailLogin)
	if _, err := svc.ConsumeLoginEmailReset(ctx, other.Phone, "wrong-phone"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("wrong phone err=%v, want invalid", err)
	}
	if _, found, err := codes.Get(ctx, "wrong-phone"); err != nil || !found {
		t.Fatalf("wrong-phone probe destroyed valid hash found=%v err=%v", found, err)
	}
	seed("wrong-channel", codeChannelPhone)
	if _, err := svc.ConsumeLoginEmailReset(ctx, owner.Phone, "wrong-channel"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("wrong channel err=%v, want invalid", err)
	}

	seed("owner-drift", codeChannelEmailLogin)
	users.setOwnerView(owner.Phone, other, true)
	if _, err := svc.ConsumeLoginEmailReset(ctx, owner.Phone, "owner-drift"); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("A->B reset err=%v, want invalid", err)
	}
	users.resetOwnerView()
	if _, err := svc.ConsumeLoginEmailReset(ctx, owner.Phone, "owner-drift"); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("A->B->A reset err=%v, want expired", err)
	}

	seed("successful-reset", codeChannelEmailLogin)
	resetUserID, err := svc.ConsumeLoginEmailReset(ctx, owner.Phone, "successful-reset")
	if err != nil || resetUserID != owner.ID {
		t.Fatalf("successful reset consume uid=%d err=%v", resetUserID, err)
	}
	replacementHash, err := svc.SendPhoneCodeAfterLoginEmailReset(ctx, owner.Phone, resetUserID)
	if err != nil || replacementHash == "" {
		t.Fatalf("replacement hash=%q err=%v", replacementHash, err)
	}
	if len(delivery.requests) != 1 || delivery.requests[0].UserID != owner.ID || delivery.requests[0].PhoneCodeHash != replacementHash {
		t.Fatalf("replacement delivery=%+v", delivery.requests)
	}
	if rec, found, err := codes.Get(ctx, replacementHash); err != nil || !found || rec.Version != store.PhoneCodeVersionCurrent || rec.IssuedUserID != owner.ID || rec.Channel != codeChannelPhone {
		t.Fatalf("replacement code=%+v found=%v err=%v", rec, found, err)
	}
}

func TestConcurrentLoginEmailResetHasSingleConsumer(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{Phone: "15550009332", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	codes := memory.NewCodeStore()
	hash := "concurrent-email-reset"
	if err := codes.Set(ctx, hash, store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		IssuedUserID: owner.ID,
		Phone:        owner.Phone,
		Code:         "654321",
		Channel:      codeChannelEmailLogin,
		MaxAttempts:  5,
	}, time.Minute); err != nil {
		t.Fatalf("seed code: %v", err)
	}
	svc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345")
	const workers = 24
	start := make(chan struct{})
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			_, err := svc.ConsumeLoginEmailReset(ctx, owner.Phone, hash)
			errs <- err
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < workers; i++ {
		err := <-errs
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrCodeExpired) {
			t.Fatalf("concurrent reset err=%v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful reset consumers=%d, want 1", successes)
	}
}

func TestLoginEmailResetLocksUserAcrossOwnerTransfer(t *testing.T) {
	ctx := context.Background()
	baseUsers := memory.NewUserStore()
	ownerA, err := baseUsers.Create(ctx, domain.User{Phone: "15550009340", FirstName: "A"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	ownerB, err := baseUsers.Create(ctx, domain.User{Phone: "15550009341", FirstName: "B"})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	users := &switchablePhoneOwnerStore{UserStore: baseUsers}
	passwords := memory.NewPasswordStore()
	accountSvc := accountapp.NewService(passwords, accountapp.WithUsers(users))
	if err := accountSvc.SetLoginEmail(ctx, ownerA.ID, "a@example.test"); err != nil {
		t.Fatalf("SetLoginEmail A: %v", err)
	}
	if err := accountSvc.SetLoginEmail(ctx, ownerB.ID, "b@example.test"); err != nil {
		t.Fatalf("SetLoginEmail B: %v", err)
	}
	codes := memory.NewCodeStore()
	hash := "locked-reset-user"
	if err := codes.Set(ctx, hash, store.PhoneCode{
		Version:      store.PhoneCodeVersionCurrent,
		IssuedUserID: ownerA.ID,
		Phone:        ownerA.Phone,
		Code:         "654321",
		Channel:      codeChannelEmailLogin,
		MaxAttempts:  5,
	}, time.Minute); err != nil {
		t.Fatalf("seed reset code: %v", err)
	}
	delivery := &captureLoginCodeDelivery{}
	authSvc := NewService(users, memory.NewAuthorizationStore(), codes, nil, nil, "12345", WithLoginCodeDelivery(delivery))
	resetUserID, err := authSvc.ConsumeLoginEmailReset(ctx, ownerA.Phone, hash)
	if err != nil || resetUserID != ownerA.ID {
		t.Fatalf("ConsumeLoginEmailReset uid=%d err=%v", resetUserID, err)
	}
	users.setOwnerView(ownerA.Phone, ownerB, true)
	if err := accountSvc.ClearLoginEmail(ctx, resetUserID); err != nil {
		t.Fatalf("ClearLoginEmail exact A: %v", err)
	}
	if _, err := authSvc.SendPhoneCodeAfterLoginEmailReset(ctx, ownerA.Phone, resetUserID); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("SendPhoneCodeAfterLoginEmailReset across A->B err=%v, want invalid", err)
	}
	if _, found, err := accountSvc.LoginEmail(ctx, ownerA.ID); err != nil || found {
		t.Fatalf("A login email found=%v err=%v, want cleared", found, err)
	}
	if email, found, err := accountSvc.LoginEmail(ctx, ownerB.ID); err != nil || !found || email != "b@example.test" {
		t.Fatalf("B login email=%q found=%v err=%v, want unchanged", email, found, err)
	}
	if len(delivery.requests) != 0 {
		t.Fatalf("owner B received reset replacement code: %+v", delivery.requests)
	}
}
