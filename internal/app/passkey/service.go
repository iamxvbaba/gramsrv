// Package passkey 实现 passkey(WebAuthn)登录与管理的业务编排:生成挑战选项、
// 验证注册 attestation、验证登录 assertion,并维护凭据/挑战持久化。
// 密码学验证委托 internal/webauthn;auth_key 绑定委托 auth.Service。
package passkey

import (
	"context"
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/webauthn"
)

const (
	defaultChallengeTTL = 5 * time.Minute
	challengeSize       = 32
)

// Service 提供 passkey 登录/注册业务。
type Service struct {
	creds          store.PasskeyStore
	challenges     store.PasskeyChallengeStore
	rpID           string
	rpName         string
	allowedOrigins []string
	dcID           int
	challengeTTL   time.Duration
	now            func() time.Time
}

// Option 调整 passkey 服务可选项。
type Option func(*Service)

// WithRPName 设置 relying-party 显示名(authenticator UI 展示)。
func WithRPName(name string) Option { return func(s *Service) { s.rpName = name } }

// WithAllowedOrigins 设置允许的 WebAuthn origin 白名单;为空表示不强校验 origin
// （服务端通常不预知 Android apk-key-hash origin)。
func WithAllowedOrigins(origins []string) Option {
	return func(s *Service) { s.allowedOrigins = append([]string(nil), origins...) }
}

// WithChallengeTTL 覆盖挑战有效期。
func WithChallengeTTL(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.challengeTTL = d
		}
	}
}

// WithClock 注入时钟(测试用)。
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// NewService 创建 passkey 服务。rpID 为 WebAuthn relying-party id(域名);dcID 写入
// user_handle 供客户端 DC 重路由(本部署单 DC,仅做回显)。
func NewService(creds store.PasskeyStore, challenges store.PasskeyChallengeStore, rpID string, dcID int, opts ...Option) *Service {
	s := &Service{
		creds:        creds,
		challenges:   challenges,
		rpID:         rpID,
		rpName:       branding.ProductName,
		dcID:         dcID,
		challengeTTL: defaultChallengeTTL,
		now:          time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) randomChallenge() ([]byte, error) {
	b := make([]byte, challengeSize)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Service) userHandle(userID int64) string {
	return fmt.Sprintf("%d:%d", s.dcID, userID)
}

func parseUserHandle(h string) (int64, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0, domain.ErrPasskeyUserHandleInvalid
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		h = h[i+1:]
	}
	id, err := strconv.ParseInt(h, 10, 64)
	if err != nil || id == 0 {
		return 0, domain.ErrPasskeyUserHandleInvalid
	}
	return id, nil
}

// InitRegistration 为已登录用户生成注册选项(creation options DataJSON)。
func (s *Service) InitRegistration(ctx context.Context, userID int64, displayName string) ([]byte, error) {
	if s == nil || s.creds == nil || s.challenges == nil || userID == 0 {
		return nil, domain.ErrPasskeyInvalid
	}
	challenge, err := s.randomChallenge()
	if err != nil {
		return nil, err
	}
	if err := s.challenges.SavePasskeyChallenge(ctx, challenge, domain.PasskeyChallenge{
		Purpose:   domain.PasskeyChallengeRegister,
		UserID:    userID,
		ExpiresAt: s.now().Add(s.challengeTTL).Unix(),
	}, s.challengeTTL); err != nil {
		return nil, err
	}
	existing, err := s.creds.ListPasskeysByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	exclude := make([][]byte, 0, len(existing))
	for _, c := range existing {
		exclude = append(exclude, c.CredentialID)
	}
	if displayName == "" {
		displayName = fmt.Sprintf("user%d", userID)
	}
	return webauthn.BuildRegistrationOptions(webauthn.RegistrationParams{
		RPID:        s.rpID,
		RPName:      s.rpName,
		UserID:      []byte(s.userHandle(userID)),
		UserName:    displayName,
		UserDisplay: displayName,
		Challenge:   challenge,
		ExcludeIDs:  exclude,
	})
}

// Register 验证注册 attestation 并持久化凭据。credentialID 为原始字节(rpc 已 base64url 解码)。
func (s *Service) Register(ctx context.Context, userID int64, credentialID, clientDataJSON, attestationObject []byte, name string) (domain.PasskeyCredential, error) {
	if s == nil || s.creds == nil || s.challenges == nil || userID == 0 {
		return domain.PasskeyCredential{}, domain.ErrPasskeyInvalid
	}
	challenge, err := webauthn.ChallengeFromClientData(clientDataJSON)
	if err != nil {
		return domain.PasskeyCredential{}, domain.ErrPasskeyInvalid
	}
	meta, found, err := s.challenges.ConsumePasskeyChallenge(ctx, challenge)
	if err != nil {
		return domain.PasskeyCredential{}, err
	}
	if !found || meta.Purpose != domain.PasskeyChallengeRegister || meta.UserID != userID {
		return domain.PasskeyCredential{}, domain.ErrPasskeyChallengeInvalid
	}
	cred, err := webauthn.VerifyRegistration(webauthn.VerifyRegistrationInput{
		ClientDataJSON:    clientDataJSON,
		AttestationObject: attestationObject,
		RPID:              s.rpID,
		Challenge:         challenge,
		AllowedOrigins:    s.allowedOrigins,
	})
	if err != nil {
		return domain.PasskeyCredential{}, fmt.Errorf("%w: %v", domain.ErrPasskeyInvalid, err)
	}
	// credentialID 来自 TL 的 ID/RawID;以 authData 内的 credID 为准(防客户端不一致)。
	now := s.now().Unix()
	record := domain.PasskeyCredential{
		CredentialID: cred.ID,
		UserID:       userID,
		PublicKey:    cred.COSEPublicKey,
		SignCount:    cred.SignCount,
		AAGUID:       cred.AAGUID,
		Name:         strings.TrimSpace(name),
		CreatedAt:    now,
	}
	if err := s.creds.InsertPasskey(ctx, record); err != nil {
		return domain.PasskeyCredential{}, err
	}
	return record, nil
}

// InitLogin 生成登录选项(request options DataJSON,discoverable,无 allowCredentials)。
func (s *Service) InitLogin(ctx context.Context) ([]byte, error) {
	if s == nil || s.challenges == nil {
		return nil, domain.ErrPasskeyInvalid
	}
	challenge, err := s.randomChallenge()
	if err != nil {
		return nil, err
	}
	if err := s.challenges.SavePasskeyChallenge(ctx, challenge, domain.PasskeyChallenge{
		Purpose:   domain.PasskeyChallengeLogin,
		ExpiresAt: s.now().Add(s.challengeTTL).Unix(),
	}, s.challengeTTL); err != nil {
		return nil, err
	}
	return webauthn.BuildLoginOptions(webauthn.LoginParams{RPID: s.rpID, Challenge: challenge})
}

// FinishLogin 验证登录 assertion,成功返回该 passkey 所属用户 id(auth_key 绑定由调用方完成)。
func (s *Service) FinishLogin(ctx context.Context, credentialID, clientDataJSON, authenticatorData, signature []byte, userHandle string) (int64, error) {
	if s == nil || s.creds == nil || s.challenges == nil {
		return 0, domain.ErrPasskeyInvalid
	}
	handleUserID, err := parseUserHandle(userHandle)
	if err != nil {
		return 0, err
	}
	cred, found, err := s.creds.GetPasskeyByCredentialID(ctx, credentialID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, domain.ErrPasskeyNotFound
	}
	// 交叉校验:credential 所属用户须与 user_handle 一致。
	if cred.UserID != handleUserID {
		return 0, domain.ErrPasskeyInvalid
	}
	challenge, err := webauthn.ChallengeFromClientData(clientDataJSON)
	if err != nil {
		return 0, domain.ErrPasskeyInvalid
	}
	meta, found, err := s.challenges.ConsumePasskeyChallenge(ctx, challenge)
	if err != nil {
		return 0, err
	}
	if !found || meta.Purpose != domain.PasskeyChallengeLogin {
		return 0, domain.ErrPasskeyChallengeInvalid
	}
	newCount, err := webauthn.VerifyAssertion(webauthn.VerifyAssertionInput{
		COSEPublicKey:     cred.PublicKey,
		ClientDataJSON:    clientDataJSON,
		AuthenticatorData: authenticatorData,
		Signature:         signature,
		RPID:              s.rpID,
		Challenge:         challenge,
		AllowedOrigins:    s.allowedOrigins,
		StoredSignCount:   cred.SignCount,
	})
	if err != nil {
		return 0, fmt.Errorf("%w: %v", domain.ErrPasskeyInvalid, err)
	}
	if err := s.creds.UpdatePasskeyUsage(ctx, cred.CredentialID, newCount, s.now().Unix()); err != nil {
		return 0, err
	}
	return cred.UserID, nil
}

// List 返回用户的全部 passkey(管理页)。
func (s *Service) List(ctx context.Context, userID int64) ([]domain.PasskeyCredential, error) {
	if s == nil || s.creds == nil || userID == 0 {
		return nil, nil
	}
	return s.creds.ListPasskeysByUser(ctx, userID)
}

// Delete 删除用户的某个 passkey。credentialID 为原始字节。
func (s *Service) Delete(ctx context.Context, userID int64, credentialID []byte) (bool, error) {
	if s == nil || s.creds == nil || userID == 0 {
		return false, nil
	}
	return s.creds.DeletePasskey(ctx, userID, credentialID)
}
