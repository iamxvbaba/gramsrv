package main

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is deliberately above bcrypt.DefaultCost (10). A panel login is a
// once-per-shift operation, so the extra time is invisible to an operator and
// meaningful to anyone working through a stolen dump of the table.
const bcryptCost = 12

// dummyBcryptHash is compared against when no account matched, so a login
// attempt costs the same whether or not the username exists. Without it the
// response time alone answers "is there an operator called X" -- the exact
// question the uniform error message refuses to answer.
//
// Value is bcrypt of a random string at bcryptCost; nothing authenticates
// against it.
const dummyBcryptHash = "$2a$12$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"

// breakGlassUsername is the name of the built-in operator backed by
// TELESRV_ADMIN_UI_PASSWORD / _TOKEN rather than by a database row.
//
// It is a real name rather than "no name" so audit lines read as an operator
// instead of as a blank, and so signing in as it is an explicit act: a blank
// username authenticates nothing.
//
// A database account may not take this name: authenticateLogin resolves it to
// the environment credential before ever consulting the table, so a row called
// "admin" would be shadowed -- and a name that silently does nothing is a
// trap. createAdminConsoleUser rejects it outright.
const breakGlassUsername = "admin"

// loginIdentity is who a successful login turns out to be.
type loginIdentity struct {
	actor       string
	userID      int64
	epoch       int32
	permissions []string
}

// authenticateLogin resolves a login request to an identity, or reports
// failure. It never distinguishes its failure modes to the caller: every one
// of them is a plain false, so the handler cannot accidentally leak which.
func (s *server) authenticateLogin(ctx context.Context, req loginRequest) (loginIdentity, bool) {
	username := strings.TrimSpace(req.Username)

	// A username is always required. An empty one used to resolve to the
	// break-glass operator, which made a blank field an unnamed second route to
	// the highest-privilege login -- the sort of thing that does not belong in
	// an admin panel. The operator must now be asked for by name.
	if username == "" {
		return loginIdentity{}, false
	}

	// The break-glass operator. Intentionally not backed by the database so it
	// still works when the database does not.
	if strings.EqualFold(username, breakGlassUsername) {
		if !s.validSecret(req.Secret) {
			return loginIdentity{}, false
		}
		return loginIdentity{actor: breakGlassUsername, permissions: s.cfg.Permissions}, true
	}

	if s.read == nil {
		return loginIdentity{}, false
	}
	cred, err := s.read.AdminConsoleCredentialByUsername(ctx, username)
	if err != nil {
		if !errors.Is(err, errAdminUserNotFound) {
			return loginIdentity{}, false
		}
		// Burn the same work an existing account would have cost before
		// answering, so "no such user" and "wrong password" take equal time.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(req.Secret))
		return loginIdentity{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(cred.PasswordHash), []byte(req.Secret)) != nil {
		return loginIdentity{}, false
	}
	// Checked after the hash comparison on purpose: answering "disabled"
	// faster than "wrong password" would confirm the account exists to someone
	// who does not know its password.
	if !cred.Enabled {
		return loginIdentity{}, false
	}
	return loginIdentity{
		actor:       cred.Username,
		userID:      cred.ID,
		epoch:       cred.TokenEpoch,
		permissions: cred.Permissions,
	}, true
}

// hashAdminPassword validates a new password and returns its bcrypt hash.
func hashAdminPassword(password string) (string, error) {
	if err := validateAdminPassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// validateAdminPassword deliberately imposes no length floor and no
// composition rule: the operator picks the password.
//
// The two checks that remain are not policy. A blank password is not a weak
// password, it is no password -- anyone who learns the username is in. And
// bcrypt silently ignores everything past 72 bytes, so a longer one is refused
// rather than quietly truncated to something the operator did not choose and
// cannot reproduce.
func validateAdminPassword(password string) error {
	if strings.TrimSpace(password) == "" {
		return errPasswordBlank
	}
	if len([]byte(password)) > 72 {
		return errPasswordTooLong
	}
	return nil
}

var (
	errPasswordTooLong = errors.New("password must be at most 72 bytes")
	errPasswordBlank   = errors.New("password must not be blank")
	errUsernameInvalid = errors.New("username must be 3-64 characters: letters, digits, dot, dash or underscore")
)

// validateAdminUsername keeps usernames to a shape that reads the same
// everywhere it is displayed. Anything outside it -- spaces, control
// characters, look-alike unicode -- is refused rather than normalised, since a
// username that renders differently from what is stored is a way to be
// mistaken for another operator.
func validateAdminUsername(username string) error {
	if n := len([]rune(username)); n < 3 || n > 64 {
		return errUsernameInvalid
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', unicode.IsDigit(r):
		case r == '.', r == '-', r == '_':
		default:
			return errUsernameInvalid
		}
	}
	return nil
}
