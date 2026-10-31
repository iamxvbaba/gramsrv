package rpc

import (
	"encoding/base64"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// decodePasskeyID base64url-decodes the credential id, tolerating presence or
// absence of padding (takes the first non-empty decodable value).
func decodePasskeyID(vals ...string) ([]byte, bool) {
	for _, v := range vals {
		if v == "" {
			continue
		}
		if b, err := base64.RawURLEncoding.DecodeString(v); err == nil && len(b) > 0 {
			return b, true
		}
		if b, err := base64.URLEncoding.DecodeString(v); err == nil && len(b) > 0 {
			return b, true
		}
	}
	return nil, false
}

// passkeyLoginFromCredential extracts the login assertion fields from
// inputPasskeyCredentialPublicKey.
func passkeyLoginFromCredential(pk tg.InputPasskeyCredentialPublicKey) (credID []byte, login *tg.InputPasskeyResponseLogin, ok bool) {
	login, ok = pk.Response.(*tg.InputPasskeyResponseLogin)
	if !ok {
		return nil, nil, false
	}
	credID, ok = decodePasskeyID(pk.RawID, pk.ID)
	if !ok {
		return nil, nil, false
	}
	return credID, login, true
}

// passkeyRegisterFromCredential extracts the registration attestation fields
// from inputPasskeyCredentialPublicKey.
func passkeyRegisterFromCredential(pk tg.InputPasskeyCredentialPublicKey) (credID []byte, reg *tg.InputPasskeyResponseRegister, ok bool) {
	reg, ok = pk.Response.(*tg.InputPasskeyResponseRegister)
	if !ok {
		return nil, nil, false
	}
	credID, ok = decodePasskeyID(pk.RawID, pk.ID)
	if !ok {
		return nil, nil, false
	}
	return credID, reg, true
}

// tgPasskey projects a domain credential into tg.Passkey (ID encoded as base64url).
func tgPasskey(c domain.PasskeyCredential) tg.Passkey {
	return tg.Passkey{
		ID:            base64.RawURLEncoding.EncodeToString(c.CredentialID),
		Name:          c.Name,
		Date:          int(c.CreatedAt),
		LastUsageDate: int(c.LastUsedAt),
	}
}

func tgPasskeys(creds []domain.PasskeyCredential) []tg.Passkey {
	out := make([]tg.Passkey, 0, len(creds))
	for _, c := range creds {
		out = append(out, tgPasskey(c))
	}
	return out
}
