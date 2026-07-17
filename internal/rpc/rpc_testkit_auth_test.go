package rpc

import (
	"context"
	"sync"
	"telesrv/internal/domain"
)

type captureAuthService struct {
	bindTempCalls         int
	bindTempLayer         int
	bindTempHook          func(domain.TempAuthKeyBinding) error
	resolvedAuthKeyID     [8]byte
	hasResolved           bool
	resolveCount          int
	userID                int64
	userIDCount           int
	signInUser            domain.User
	signUpPhone           string
	signUpHash            string
	signUpFirstName       string
	signUpLastName        string
	signUpAuth            domain.Authorization
	signUpUser            domain.User
	acceptedAuth          domain.Authorization
	acceptedUserID        int64
	authorizations        []domain.Authorization
	authorizationLookups  int
	authorizationLists    int
	authKeyClientInfos    map[[8]byte]domain.AuthKeyClientInfo
	authKeyInfoLookups    int
	loggedOutAuthKeyID    [8]byte
	pendingPasswordUserID int64
	pendingPassword       bool
	completedPasswordKey  [8]byte
	completePasswordCount int
	codeDelivery          domain.AuthCodeDelivery
	signInCount           int
	signInPhone           string
	signInHash            string
	signInCode            string
	signInWithEmailCount  int
	signInWithEmailPhone  string
	signInWithEmailHash   string
	signInWithEmailCode   string
}

type blockingUserAuthService struct {
	userID  int64
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	count   int
}

func (s *blockingUserAuthService) UserIDCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func (s *blockingUserAuthService) BindTempAuthKey(context.Context, int64, domain.TempAuthKeyBinding) error {
	return nil
}

func (s *blockingUserAuthService) ResolveAuthKey(context.Context, [8]byte) ([8]byte, bool, error) {
	return [8]byte{}, false, nil
}

func (s *blockingUserAuthService) UserID(ctx context.Context, _ [8]byte) (int64, bool, error) {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.userID, s.userID != 0, nil
	case <-ctx.Done():
		return 0, false, ctx.Err()
	}
}

func (s *blockingUserAuthService) SendCode(context.Context, string) (string, error) {
	return "", nil
}

func (s *blockingUserAuthService) CodeDelivery(context.Context, string) (domain.AuthCodeDelivery, bool, error) {
	return domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliveryPhone, Length: devCodeLength}, true, nil
}

func (s *blockingUserAuthService) ResendCode(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *blockingUserAuthService) CancelCode(context.Context, string, string) error {
	return nil
}

func (s *blockingUserAuthService) SignIn(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	return domain.User{}, domain.Message{}, false, nil
}

func (s *blockingUserAuthService) SignInWithEmail(context.Context, domain.Authorization, string, string, string) (domain.User, domain.Message, bool, error) {
	return domain.User{}, domain.Message{}, false, nil
}

func (s *blockingUserAuthService) BindVerifiedLogin(_ context.Context, _ domain.Authorization, userID int64) (domain.User, error) {
	return domain.User{ID: userID}, nil
}

func (s *blockingUserAuthService) SignUp(context.Context, domain.Authorization, string, string, string, string) (domain.User, domain.Message, error) {
	return domain.User{}, domain.Message{}, nil
}

func (s *blockingUserAuthService) AcceptLoginToken(context.Context, domain.Authorization, int64) (domain.Authorization, error) {
	return domain.Authorization{}, nil
}

func (s *blockingUserAuthService) SignInBot(context.Context, domain.Authorization, string) (domain.User, error) {
	return domain.User{}, domain.ErrBotTokenInvalid
}

func (s *blockingUserAuthService) LogOut(context.Context, [8]byte) error {
	return nil
}

func (s *blockingUserAuthService) Authorization(context.Context, [8]byte) (domain.Authorization, bool, error) {
	return domain.Authorization{}, false, nil
}

func (s *blockingUserAuthService) AuthKeyClientInfo(context.Context, [8]byte) (domain.AuthKeyClientInfo, bool, error) {
	return domain.AuthKeyClientInfo{}, false, nil
}

func (s *blockingUserAuthService) UpdateAuthKeyClientInfo(context.Context, [8]byte, domain.AuthKeyClientInfo) error {
	return nil
}

func (s *blockingUserAuthService) ListAuthorizations(context.Context, int64) ([]domain.Authorization, error) {
	return nil, nil
}

func (s *blockingUserAuthService) ResetAuthorization(context.Context, int64, int64) (domain.Authorization, bool, error) {
	return domain.Authorization{}, false, nil
}

func (s *blockingUserAuthService) ResetAuthorizations(context.Context, int64, [8]byte) ([]domain.Authorization, error) {
	return nil, nil
}

func (s *blockingUserAuthService) PendingPasswordUserID(context.Context, [8]byte) (int64, bool, error) {
	return 0, false, nil
}

func (s *blockingUserAuthService) CompletePasswordSignIn(context.Context, [8]byte) error {
	return nil
}

func (s *captureAuthService) BindTempAuthKey(ctx context.Context, _ int64, binding domain.TempAuthKeyBinding) error {
	s.bindTempCalls++
	s.bindTempLayer = LayerFrom(ctx)
	if s.bindTempHook != nil {
		return s.bindTempHook(binding)
	}
	return nil
}

func (s *captureAuthService) ResolveAuthKey(context.Context, [8]byte) ([8]byte, bool, error) {
	s.resolveCount++
	return s.resolvedAuthKeyID, s.hasResolved, nil
}

func (s *captureAuthService) UserID(context.Context, [8]byte) (int64, bool, error) {
	s.userIDCount++
	return s.userID, s.userID != 0, nil
}

func (s *captureAuthService) SendCode(context.Context, string) (string, error) {
	return "", nil
}

func (s *captureAuthService) CodeDelivery(context.Context, string) (domain.AuthCodeDelivery, bool, error) {
	if s.codeDelivery.Kind != "" {
		return s.codeDelivery, true, nil
	}
	return domain.AuthCodeDelivery{Kind: domain.AuthCodeDeliveryPhone, Length: devCodeLength}, true, nil
}

func (s *captureAuthService) ResendCode(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *captureAuthService) CancelCode(context.Context, string, string) error {
	return nil
}

func (s *captureAuthService) SignIn(_ context.Context, _ domain.Authorization, phone, hash, code string) (domain.User, domain.Message, bool, error) {
	s.signInCount++
	s.signInPhone = phone
	s.signInHash = hash
	s.signInCode = code
	if s.signInUser.ID != 0 {
		return s.signInUser, domain.Message{}, false, nil
	}
	return domain.User{}, domain.Message{}, false, nil
}

func (s *captureAuthService) SignInWithEmail(_ context.Context, _ domain.Authorization, phone, hash, code string) (domain.User, domain.Message, bool, error) {
	s.signInWithEmailCount++
	s.signInWithEmailPhone = phone
	s.signInWithEmailHash = hash
	s.signInWithEmailCode = code
	if s.signInUser.ID != 0 {
		return s.signInUser, domain.Message{}, false, nil
	}
	return domain.User{}, domain.Message{}, false, nil
}

func (s *captureAuthService) BindVerifiedLogin(_ context.Context, _ domain.Authorization, userID int64) (domain.User, error) {
	if s.signInUser.ID != 0 {
		return s.signInUser, nil
	}
	return domain.User{ID: userID}, nil
}

func (s *captureAuthService) SignUp(_ context.Context, a domain.Authorization, phone, hash, first, last string) (domain.User, domain.Message, error) {
	s.signUpAuth = a
	s.signUpPhone = phone
	s.signUpHash = hash
	s.signUpFirstName = first
	s.signUpLastName = last
	if s.signUpUser.ID != 0 {
		return s.signUpUser, domain.Message{}, nil
	}
	return domain.User{}, domain.Message{}, nil
}

func (s *captureAuthService) AcceptLoginToken(_ context.Context, a domain.Authorization, userID int64) (domain.Authorization, error) {
	a.UserID = userID
	if a.Hash == 0 {
		a.Hash = 77
	}
	s.acceptedAuth = a
	s.acceptedUserID = userID
	return a, nil
}

func (s *captureAuthService) SignInBot(context.Context, domain.Authorization, string) (domain.User, error) {
	if s.signInUser.ID != 0 {
		return s.signInUser, nil
	}
	return domain.User{}, domain.ErrBotTokenInvalid
}

func (s *captureAuthService) LogOut(_ context.Context, authKeyID [8]byte) error {
	s.loggedOutAuthKeyID = authKeyID
	return nil
}

func (s *captureAuthService) Authorization(_ context.Context, authKeyID [8]byte) (domain.Authorization, bool, error) {
	s.authorizationLookups++
	for _, item := range s.authorizations {
		if item.AuthKeyID == authKeyID {
			return item, true, nil
		}
	}
	return domain.Authorization{}, false, nil
}

func (s *captureAuthService) AuthKeyClientInfo(_ context.Context, authKeyID [8]byte) (domain.AuthKeyClientInfo, bool, error) {
	s.authKeyInfoLookups++
	info, ok := s.authKeyClientInfos[authKeyID]
	return info, ok, nil
}

func (s *captureAuthService) UpdateAuthKeyClientInfo(_ context.Context, authKeyID [8]byte, info domain.AuthKeyClientInfo) error {
	if s.authKeyClientInfos == nil {
		s.authKeyClientInfos = make(map[[8]byte]domain.AuthKeyClientInfo)
	}
	current := s.authKeyClientInfos[authKeyID]
	if info.Layer > 0 {
		current.Layer = info.Layer
	}
	if info.DeviceModel != "" {
		current.DeviceModel = info.DeviceModel
	}
	if info.Platform != "" {
		current.Platform = info.Platform
	}
	if info.SystemVersion != "" {
		current.SystemVersion = info.SystemVersion
	}
	if info.APIID != 0 {
		current.APIID = info.APIID
	}
	if info.AppVersion != "" {
		current.AppVersion = info.AppVersion
	}
	s.authKeyClientInfos[authKeyID] = current
	for i := range s.authorizations {
		if s.authorizations[i].AuthKeyID != authKeyID {
			continue
		}
		if info.DeviceModel != "" {
			s.authorizations[i].DeviceModel = info.DeviceModel
		}
		if info.Platform != "" {
			s.authorizations[i].Platform = info.Platform
		}
		if info.SystemVersion != "" {
			s.authorizations[i].SystemVersion = info.SystemVersion
		}
		if info.APIID != 0 {
			s.authorizations[i].APIID = info.APIID
		}
		if info.AppVersion != "" {
			s.authorizations[i].AppVersion = info.AppVersion
		}
	}
	return nil
}

func (s *captureAuthService) ListAuthorizations(context.Context, int64) ([]domain.Authorization, error) {
	s.authorizationLists++
	return append([]domain.Authorization(nil), s.authorizations...), nil
}

func (s *captureAuthService) ResetAuthorization(context.Context, int64, int64) (domain.Authorization, bool, error) {
	return domain.Authorization{}, false, nil
}

func (s *captureAuthService) ResetAuthorizations(context.Context, int64, [8]byte) ([]domain.Authorization, error) {
	return nil, nil
}

func (s *captureAuthService) PendingPasswordUserID(context.Context, [8]byte) (int64, bool, error) {
	return s.pendingPasswordUserID, s.pendingPassword, nil
}

func (s *captureAuthService) CompletePasswordSignIn(_ context.Context, authKeyID [8]byte) error {
	s.completedPasswordKey = authKeyID
	s.completePasswordCount++
	return nil
}
