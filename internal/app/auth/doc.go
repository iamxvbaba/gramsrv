// Package auth implements the authentication application service: verification codes, login, sign-up, log-out,
// and the binding of auth keys to users. The first stage uses a fixed development verification code; 2FA
// configuration is persisted and queried by the account service.
//
// Inputs and outputs use iamxvbaba/td/tg types at the RPC boundary; internally this package only uses the
// internal/domain models.
package auth
