package domain

import (
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrTelegramLoginClientInvalid         = errors.New("telegram login client invalid")
	ErrTelegramLoginClientDisabled        = errors.New("telegram login client disabled")
	ErrTelegramLoginURLInvalid            = errors.New("telegram login url invalid")
	ErrTelegramLoginRequestInvalid        = errors.New("telegram login request invalid")
	ErrTelegramLoginRequestExpired        = errors.New("telegram login request expired")
	ErrTelegramLoginRequestConflict       = errors.New("telegram login request conflict")
	ErrTelegramLoginMatchCodeInvalid      = errors.New("telegram login match code invalid")
	ErrTelegramLoginScopeInvalid          = errors.New("telegram login scope invalid")
	ErrTelegramLoginCodeInvalid           = errors.New("telegram login code invalid")
	ErrTelegramLoginCodeConsumed          = errors.New("telegram login code consumed")
	ErrTelegramLoginWebAuthHashInvalid    = errors.New("telegram login web authorization hash invalid")
	ErrTelegramLoginRedirectNotAllowed    = errors.New("telegram login redirect not allowed")
	ErrTelegramLoginOriginNotAllowed      = errors.New("telegram login origin not allowed")
	ErrTelegramLoginSecretInvalid         = errors.New("telegram login client secret invalid")
	ErrTelegramLoginPKCEInvalid           = errors.New("telegram login pkce invalid")
	ErrTelegramLoginAuthorizationsTooMany = errors.New("telegram login authorizations too many")
)

const MaxTelegramLoginWebAuthorizations = 1000

type TelegramLoginSigningAlgorithm string

const (
	TelegramLoginSigningRS256  TelegramLoginSigningAlgorithm = "RS256"
	TelegramLoginSigningES256  TelegramLoginSigningAlgorithm = "ES256"
	TelegramLoginSigningEdDSA  TelegramLoginSigningAlgorithm = "EdDSA"
	TelegramLoginSigningES256K TelegramLoginSigningAlgorithm = "ES256K"
)

func (a TelegramLoginSigningAlgorithm) Valid() bool {
	switch a {
	case TelegramLoginSigningRS256, TelegramLoginSigningES256, TelegramLoginSigningEdDSA, TelegramLoginSigningES256K:
		return true
	default:
		return false
	}
}

type TelegramLoginScope string

const (
	TelegramLoginScopeOpenID    TelegramLoginScope = "openid"
	TelegramLoginScopeProfile   TelegramLoginScope = "profile"
	TelegramLoginScopePhone     TelegramLoginScope = "phone"
	TelegramLoginScopeBotAccess TelegramLoginScope = "telegram:bot_access"
)

func (s TelegramLoginScope) Valid() bool {
	switch s {
	case TelegramLoginScopeOpenID, TelegramLoginScopeProfile, TelegramLoginScopePhone, TelegramLoginScopeBotAccess:
		return true
	default:
		return false
	}
}

type TelegramLoginClient struct {
	BotUserID        int64
	ClientID         string
	SecretHash       []byte
	SecretVersion    int64
	SigningAlgorithm TelegramLoginSigningAlgorithm
	Enabled          bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (c TelegramLoginClient) Clone() TelegramLoginClient {
	out := c
	out.SecretHash = append([]byte(nil), c.SecretHash...)
	return out
}

func (c TelegramLoginClient) Validate() error {
	if c.BotUserID <= 0 || c.ClientID == "" || len(c.SecretHash) != 32 || c.SecretVersion <= 0 || !c.SigningAlgorithm.Valid() {
		return ErrTelegramLoginClientInvalid
	}
	return nil
}

type TelegramLoginAllowedURLKind string

const (
	TelegramLoginAllowedWebOrigin   TelegramLoginAllowedURLKind = "web_origin"
	TelegramLoginAllowedRedirectURI TelegramLoginAllowedURLKind = "redirect_uri"
)

type TelegramLoginAllowedURL struct {
	ID            int64
	BotUserID     int64
	Kind          TelegramLoginAllowedURLKind
	NormalizedURL string
	CreatedAt     time.Time
}

type TelegramLoginNativePlatform string

const (
	TelegramLoginNativeIOS     TelegramLoginNativePlatform = "ios"
	TelegramLoginNativeAndroid TelegramLoginNativePlatform = "android"
)

type TelegramLoginNativeApp struct {
	ID            int64
	BotUserID     int64
	Platform      TelegramLoginNativePlatform
	ApplicationID string
	// VerificationID is the 10-character Apple Team ID on iOS and the
	// normalized 64-hex SHA-256 signing-certificate fingerprint on Android.
	VerificationID      string
	CallbackURI         string
	VerifiedDisplayName string
	Enabled             bool
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

const MaxTelegramLoginNativeApps = 20

func (p TelegramLoginNativePlatform) Valid() bool {
	return p == TelegramLoginNativeIOS || p == TelegramLoginNativeAndroid
}

func (a TelegramLoginNativeApp) Validate() error {
	if a.BotUserID <= 0 || !a.Platform.Valid() || a.ApplicationID == "" || len(a.ApplicationID) > 255 ||
		a.VerificationID == "" || a.CallbackURI == "" || len(a.CallbackURI) > 4096 ||
		a.VerifiedDisplayName == "" || len(a.VerifiedDisplayName) > 128 ||
		a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		return ErrTelegramLoginClientInvalid
	}
	return nil
}

type TelegramLoginRequestSource string

const (
	TelegramLoginRequestWeb           TelegramLoginRequestSource = "web"
	TelegramLoginRequestJavaScript    TelegramLoginRequestSource = "javascript"
	TelegramLoginRequestNative        TelegramLoginRequestSource = "native"
	TelegramLoginRequestMiniApp       TelegramLoginRequestSource = "mini_app"
	TelegramLoginRequestMessageButton TelegramLoginRequestSource = "message_button"
)

type TelegramLoginRequestState string

const (
	TelegramLoginRequestPending  TelegramLoginRequestState = "pending"
	TelegramLoginRequestApproved TelegramLoginRequestState = "approved"
	TelegramLoginRequestDeclined TelegramLoginRequestState = "declined"
	TelegramLoginRequestExpired  TelegramLoginRequestState = "expired"
)

func (s TelegramLoginRequestState) Terminal() bool {
	return s == TelegramLoginRequestApproved || s == TelegramLoginRequestDeclined || s == TelegramLoginRequestExpired
}

func CanTransitionTelegramLoginRequest(from, to TelegramLoginRequestState) bool {
	if from != TelegramLoginRequestPending {
		return false
	}
	return to == TelegramLoginRequestApproved || to == TelegramLoginRequestDeclined || to == TelegramLoginRequestExpired
}

type TelegramLoginRequest struct {
	ID                  int64
	RequestTokenHash    []byte
	BrowserTokenHash    []byte
	BotUserID           int64
	ClientID            string
	SigningAlgorithm    TelegramLoginSigningAlgorithm
	Source              TelegramLoginRequestSource
	ResponseType        string
	RedirectURI         string
	Origin              string
	Domain              string
	Scopes              []TelegramLoginScope
	State               string
	Nonce               string
	CodeChallenge       string
	CodeChallengeMethod string
	Browser             string
	Platform            string
	IP                  string
	Region              string
	InAppOrigin         string
	IsApp               bool
	VerifiedAppName     string
	MatchCodes          []string
	MatchCode           string
	MatchCodesFirst     bool
	UserIDHint          int64
	PeerType            PeerType
	PeerID              int64
	MessageID           int
	ButtonID            int
	Status              TelegramLoginRequestState
	AuthorizedUserID    int64
	ProfileName         string
	GivenName           string
	FamilyName          string
	PreferredUsername   string
	Picture             string
	PhoneNumber         string
	WriteAllowed        bool
	PhoneShared         bool
	CreatedAt           time.Time
	ExpiresAt           time.Time
	ApprovedAt          time.Time
	DeclinedAt          time.Time
}

func (r TelegramLoginRequest) Clone() TelegramLoginRequest {
	out := r
	out.RequestTokenHash = append([]byte(nil), r.RequestTokenHash...)
	out.BrowserTokenHash = append([]byte(nil), r.BrowserTokenHash...)
	out.Scopes = append([]TelegramLoginScope(nil), r.Scopes...)
	out.MatchCodes = append([]string(nil), r.MatchCodes...)
	return out
}

func (r TelegramLoginRequest) Requests(scope TelegramLoginScope) bool {
	return slices.Contains(r.Scopes, scope)
}

func (r TelegramLoginRequest) Validate() error {
	if len(r.RequestTokenHash) != 32 || len(r.BrowserTokenHash) != 32 || r.BotUserID <= 0 || r.ClientID == "" || r.ClientID != strings.TrimSpace(r.ClientID) || r.RedirectURI == "" || r.Domain == "" {
		return ErrTelegramLoginRequestInvalid
	}
	if !r.SigningAlgorithm.Valid() || !r.Source.Valid() || (r.ResponseType != "code" && r.ResponseType != "post_message" && r.ResponseType != "legacy_url") ||
		r.Status != TelegramLoginRequestPending || r.CreatedAt.IsZero() || !r.ExpiresAt.After(r.CreatedAt) {
		return ErrTelegramLoginRequestInvalid
	}
	switch r.Source {
	case TelegramLoginRequestWeb:
		if r.ResponseType != "code" {
			return ErrTelegramLoginRequestInvalid
		}
	case TelegramLoginRequestJavaScript:
		if r.ResponseType != "post_message" {
			return ErrTelegramLoginRequestInvalid
		}
	case TelegramLoginRequestNative:
		if r.ResponseType != "code" || !r.IsApp || r.VerifiedAppName == "" || r.Origin != "" {
			return ErrTelegramLoginRequestInvalid
		}
	case TelegramLoginRequestMiniApp:
		if r.ResponseType != "post_message" {
			return ErrTelegramLoginRequestInvalid
		}
	case TelegramLoginRequestMessageButton:
		if r.ResponseType != "legacy_url" {
			return ErrTelegramLoginRequestInvalid
		}
	default:
		return ErrTelegramLoginRequestInvalid
	}
	if r.Source != TelegramLoginRequestNative && (r.IsApp || r.VerifiedAppName != "" || r.Origin == "") {
		return ErrTelegramLoginRequestInvalid
	}
	if r.AuthorizedUserID != 0 || r.ProfileName != "" || r.GivenName != "" || r.FamilyName != "" ||
		r.PreferredUsername != "" || r.Picture != "" || r.PhoneNumber != "" || r.WriteAllowed || r.PhoneShared ||
		!r.ApprovedAt.IsZero() || !r.DeclinedAt.IsZero() {
		return ErrTelegramLoginRequestInvalid
	}
	if len(r.RedirectURI) > 4096 || len(r.Origin) > 4096 || len(r.Domain) > 255 || len(r.InAppOrigin) > 4096 ||
		len(r.State) > 2048 || len(r.Nonce) > 1024 || len(r.Browser) == 0 || len(r.Browser) > 255 ||
		len(r.Platform) == 0 || len(r.Platform) > 255 || len(r.IP) == 0 || len(r.IP) > 128 ||
		len(r.Region) == 0 || len(r.Region) > 255 || len(r.VerifiedAppName) > 128 || r.UserIDHint < 0 ||
		r.PeerID < 0 || r.MessageID < 0 || r.ButtonID < 0 || len(r.MatchCodes) > 8 {
		return ErrTelegramLoginRequestInvalid
	}
	if r.ResponseType == "legacy_url" {
		if r.Source != TelegramLoginRequestMessageButton || r.PeerID <= 0 || r.MessageID <= 0 ||
			(r.PeerType != PeerTypeUser && r.PeerType != PeerTypeChannel) || r.CodeChallenge != "" || r.CodeChallengeMethod != "" ||
			len(r.MatchCodes) != 0 || r.MatchCode != "" || r.MatchCodesFirst {
			return ErrTelegramLoginRequestInvalid
		}
		if !slices.Contains(r.Scopes, TelegramLoginScopeOpenID) || !slices.Contains(r.Scopes, TelegramLoginScopeProfile) {
			return ErrTelegramLoginScopeInvalid
		}
		seen := make(map[TelegramLoginScope]struct{}, len(r.Scopes))
		for _, scope := range r.Scopes {
			if !scope.Valid() || scope == TelegramLoginScopePhone {
				return ErrTelegramLoginScopeInvalid
			}
			if _, duplicate := seen[scope]; duplicate {
				return ErrTelegramLoginScopeInvalid
			}
			seen[scope] = struct{}{}
		}
	} else if r.ResponseType == "code" {
		if err := ValidateTelegramLoginScopes(r.Scopes, r.SigningAlgorithm); err != nil {
			return err
		}
		if r.CodeChallengeMethod != "S256" || r.CodeChallenge == "" {
			return ErrTelegramLoginPKCEInvalid
		}
	} else {
		if err := ValidateTelegramLoginScopes(r.Scopes, r.SigningAlgorithm); err != nil {
			return err
		}
		// Telegram's official JavaScript SDK returns an ID token directly and
		// therefore sends no authorization-code PKCE parameters. Accept a PKCE
		// pair for generic callers, but never a partial pair.
		if r.CodeChallenge == "" && r.CodeChallengeMethod == "" {
			// Official post_message/Mini App shape.
		} else if r.CodeChallengeMethod != "S256" || r.CodeChallenge == "" {
			return ErrTelegramLoginPKCEInvalid
		}
	}
	if r.Source == TelegramLoginRequestMiniApp {
		if r.ResponseType != "post_message" || r.InAppOrigin == "" || r.Origin != r.InAppOrigin {
			return ErrTelegramLoginRequestInvalid
		}
	} else if r.InAppOrigin != "" {
		return ErrTelegramLoginRequestInvalid
	}
	if r.MatchCodesFirst && len(r.MatchCodes) == 0 {
		return ErrTelegramLoginRequestInvalid
	}
	if len(r.MatchCodes) > 0 && (r.MatchCode == "" || !slices.Contains(r.MatchCodes, r.MatchCode)) {
		return ErrTelegramLoginRequestInvalid
	}
	return nil
}

// TelegramLoginMessageButtonAuthorization is the domain-only input for the
// legacy login_url consent path. BotToken is used transiently to produce the
// official HMAC response and is never persisted in the login aggregate.
type TelegramLoginMessageButtonAuthorization struct {
	UserID             int64
	BotUserID          int64
	BotToken           string
	URL                string
	RequestWriteAccess bool
	WriteAllowed       bool
	Peer               Peer
	MessageID          int
	ButtonID           int
	Browser            string
	Platform           string
	IP                 string
	Region             string
	Identity           TelegramLoginIdentitySnapshot
}

type TelegramLoginMessageButtonResult struct {
	URL              string
	Request          TelegramLoginRequest
	WebAuthorization TelegramLoginWebAuthorization
}

func (s TelegramLoginRequestSource) Valid() bool {
	switch s {
	case TelegramLoginRequestWeb, TelegramLoginRequestJavaScript, TelegramLoginRequestNative,
		TelegramLoginRequestMiniApp, TelegramLoginRequestMessageButton:
		return true
	default:
		return false
	}
}

// TelegramLoginIdentitySnapshot is the immutable identity presented on the
// approval screen and later signed into the ID token. It is written together
// with the pending->approved transition so a profile/phone mutation between
// approval and code exchange cannot change what the relying party receives.
type TelegramLoginIdentitySnapshot struct {
	UserID            int64
	Name              string
	GivenName         string
	FamilyName        string
	PreferredUsername string
	Picture           string
	PhoneNumber       string
}

func (s TelegramLoginIdentitySnapshot) Sanitized(includeProfile, includePhone bool) (TelegramLoginIdentitySnapshot, error) {
	if s.UserID <= 0 {
		return TelegramLoginIdentitySnapshot{}, ErrTelegramLoginRequestInvalid
	}
	out := TelegramLoginIdentitySnapshot{UserID: s.UserID}
	if includeProfile {
		out.Name = strings.TrimSpace(s.Name)
		out.GivenName = strings.TrimSpace(s.GivenName)
		out.FamilyName = strings.TrimSpace(s.FamilyName)
		out.PreferredUsername = strings.TrimSpace(s.PreferredUsername)
		out.Picture = strings.TrimSpace(s.Picture)
		if out.Name == "" || out.GivenName == "" {
			return TelegramLoginIdentitySnapshot{}, ErrTelegramLoginRequestInvalid
		}
	}
	if includePhone {
		out.PhoneNumber = NormalizePhone(s.PhoneNumber)
		if !ValidPhone(out.PhoneNumber) {
			return TelegramLoginIdentitySnapshot{}, ErrPhoneNumberInvalid
		}
	}
	if !boundedUTF8(out.Name, 255) || !boundedUTF8(out.GivenName, 255) || !boundedUTF8(out.FamilyName, 255) ||
		!boundedUTF8(out.PreferredUsername, 64) || !boundedUTF8(out.Picture, 4096) || len(out.PhoneNumber) > 32 {
		return TelegramLoginIdentitySnapshot{}, ErrTelegramLoginRequestInvalid
	}
	return out, nil
}

func boundedUTF8(value string, maxBytes int) bool {
	return utf8.ValidString(value) && len(value) <= maxBytes
}

func ValidateTelegramLoginScopes(scopes []TelegramLoginScope, alg TelegramLoginSigningAlgorithm) error {
	if !alg.Valid() || len(scopes) == 0 || !slices.Contains(scopes, TelegramLoginScopeOpenID) {
		return ErrTelegramLoginScopeInvalid
	}
	seen := make(map[TelegramLoginScope]struct{}, len(scopes))
	for _, scope := range scopes {
		if !scope.Valid() {
			return ErrTelegramLoginScopeInvalid
		}
		if _, duplicate := seen[scope]; duplicate {
			return ErrTelegramLoginScopeInvalid
		}
		seen[scope] = struct{}{}
	}
	if alg == TelegramLoginSigningEdDSA || alg == TelegramLoginSigningES256K {
		if len(scopes) != 1 || scopes[0] != TelegramLoginScopeOpenID {
			return ErrTelegramLoginScopeInvalid
		}
	}
	return nil
}

type TelegramLoginApproval struct {
	RequestID    int64
	Identity     TelegramLoginIdentitySnapshot
	WriteAllowed bool
	PhoneShared  bool
	MatchCode    string
	ApprovedAt   time.Time
}

type TelegramLoginAuthorizationCode struct {
	ID         int64
	RequestID  int64
	CodeHash   []byte
	SealedCode []byte
	SealNonce  []byte
	SealKeyID  string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	ConsumedAt time.Time
}

// TelegramLoginCodeExchange carries the values already normalized/hashed by
// the application service. The durable store compares them again while the
// code/request/client rows are locked, closing redirect, PKCE and secret-
// rotation TOCTOU gaps between HTTP validation and one-time consumption.
type TelegramLoginCodeExchange struct {
	CodeHash            []byte
	ClientID            string
	ClientSecretVersion int64
	RedirectURI         string
	CodeChallenge       string
	Now                 time.Time
}

func (c TelegramLoginAuthorizationCode) Clone() TelegramLoginAuthorizationCode {
	out := c
	out.CodeHash = append([]byte(nil), c.CodeHash...)
	out.SealedCode = append([]byte(nil), c.SealedCode...)
	out.SealNonce = append([]byte(nil), c.SealNonce...)
	return out
}

type TelegramLoginWebAuthorization struct {
	Hash             int64
	RequestID        int64
	UserID           int64
	BotUserID        int64
	Domain           string
	Browser          string
	Platform         string
	IP               string
	Region           string
	Scopes           []TelegramLoginScope
	PhoneShared      bool
	BotAccessGranted bool
	CreatedAt        time.Time
	LastActiveAt     time.Time
	RevokedAt        time.Time
}

func (a TelegramLoginWebAuthorization) Clone() TelegramLoginWebAuthorization {
	out := a
	out.Scopes = append([]TelegramLoginScope(nil), a.Scopes...)
	return out
}
