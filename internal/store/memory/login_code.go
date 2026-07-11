package memory

import (
	"context"
	"crypto/subtle"
	"time"

	"telesrv/internal/store"
)

func (s *CodeStore) VerifyLogin(_ context.Context, hash, phone, code string, keepForSignUp bool, defaultMaxAttempts int) (store.LoginCodeVerifyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.liveCodeLocked(hash)
	if !ok {
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyMissing}, nil
	}
	record := entry.code
	if record.Version != store.PhoneCodeVersionCurrent {
		s.deleteCodeLocked(hash, record)
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyMissing}, nil
	}
	// Sign-up verification is a terminal state for VerifyLogin. Keep the marker
	// for the one caller that already received signUpRequired, but do not report
	// a second Accepted result or let later wrong-code calls exhaust it.
	if record.SignUpVerified {
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyMissing}, nil
	}
	if record.Purpose != "" || record.Phone != phone || !loginCodeVerifiable(record) || record.Code == "" || code == "" {
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyInvalid, Record: record}, nil
	}
	if subtle.ConstantTimeCompare([]byte(record.Code), []byte(code)) != 1 {
		revision, err := store.NewPhoneCodeRevisionToken()
		if err != nil {
			return store.LoginCodeVerifyResult{}, err
		}
		record.Attempts++
		record.Revision = revision
		entry.code = record
		maxAttempts := record.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = defaultMaxAttempts
		}
		if maxAttempts <= 0 {
			maxAttempts = 1
		}
		if record.Attempts >= maxAttempts {
			s.deleteCodeLocked(hash, record)
		} else {
			s.m[hash] = entry
		}
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyInvalid, Record: record}, nil
	}

	if keepForSignUp {
		if record.IssuedUserID != 0 {
			return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyInvalid, Record: record}, nil
		}
		revision, err := store.NewPhoneCodeRevisionToken()
		if err != nil {
			return store.LoginCodeVerifyResult{}, err
		}
		record.SignUpVerified = true
		record.Revision = revision
		entry.code = record
		s.m[hash] = entry
	} else {
		s.deleteCodeLocked(hash, record)
	}
	return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyAccepted, Record: record}, nil
}

func (s *CodeStore) VerifyScoped(_ context.Context, hash string, scope store.PhoneCodeScope, code string, defaultMaxAttempts int) (store.LoginCodeVerifyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !scope.Valid() || s.scopes[scope] != hash {
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyMissing}, nil
	}
	entry, ok := s.liveCodeLocked(hash)
	if !ok {
		// liveCodeLocked removes the index when it can decode the stored scope;
		// also close the stale-index-only case.
		if s.scopes[scope] == hash {
			delete(s.scopes, scope)
		}
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyMissing}, nil
	}
	record := entry.code
	if record.Version != store.PhoneCodeVersionCurrent || record.Scope() != scope || record.SignUpVerified || record.Code == "" {
		// Fail closed on a legacy or internally inconsistent record. Clean both
		// the scope encoded in the record and the scope that selected this hash.
		s.deleteCodeLocked(hash, record)
		if s.scopes[scope] == hash {
			delete(s.scopes, scope)
		}
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyMissing}, nil
	}
	if code == "" {
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyInvalid, Record: record}, nil
	}
	if subtle.ConstantTimeCompare([]byte(record.Code), []byte(code)) != 1 {
		revision, err := store.NewPhoneCodeRevisionToken()
		if err != nil {
			return store.LoginCodeVerifyResult{}, err
		}
		record.Attempts++
		record.Revision = revision
		entry.code = record
		maxAttempts := record.MaxAttempts
		if maxAttempts <= 0 {
			maxAttempts = defaultMaxAttempts
		}
		if maxAttempts <= 0 {
			maxAttempts = 1
		}
		if record.Attempts >= maxAttempts {
			s.deleteCodeLocked(hash, record)
		} else {
			s.m[hash] = entry
		}
		return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyInvalid, Record: record}, nil
	}

	s.deleteCodeLocked(hash, record)
	return store.LoginCodeVerifyResult{Status: store.LoginCodeVerifyAccepted, Record: record}, nil
}

func (s *CodeStore) ConsumeSignUpVerified(_ context.Context, hash, phone string) (store.PhoneCode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.liveCodeLocked(hash)
	if !ok {
		return store.PhoneCode{}, false, nil
	}
	record := entry.code
	if record.Version != store.PhoneCodeVersionCurrent {
		s.deleteCodeLocked(hash, record)
		return store.PhoneCode{}, false, nil
	}
	if record.Purpose != "" || record.Phone != phone || !loginCodeVerifiable(record) || record.IssuedUserID != 0 || !record.SignUpVerified {
		return store.PhoneCode{}, false, nil
	}
	s.deleteCodeLocked(hash, record)
	return record, true, nil
}

func (s *CodeStore) TakeLoginCode(_ context.Context, hash, phone string) (store.PhoneCode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.liveCodeLocked(hash)
	if !ok {
		return store.PhoneCode{}, false, nil
	}
	record := entry.code
	if record.Version != store.PhoneCodeVersionCurrent {
		s.deleteCodeLocked(hash, record)
		return store.PhoneCode{}, false, nil
	}
	if record.SignUpVerified {
		return store.PhoneCode{}, false, nil
	}
	if record.Purpose != "" || record.Phone != phone || !loginCodeTakeable(record) {
		return store.PhoneCode{}, false, nil
	}
	s.deleteCodeLocked(hash, record)
	return record, true, nil
}

func (s *CodeStore) InvalidateLoginCode(_ context.Context, hash, phone string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.liveCodeLocked(hash)
	if !ok {
		return false, nil
	}
	record := entry.code
	if record.Version != store.PhoneCodeVersionCurrent {
		s.deleteCodeLocked(hash, record)
		return false, nil
	}
	if record.Purpose != "" || record.Phone != phone || !loginCodeTakeable(record) {
		return false, nil
	}
	s.deleteCodeLocked(hash, record)
	return true, nil
}

func loginCodeVerifiable(record store.PhoneCode) bool {
	return record.Channel == store.PhoneCodeChannelPhone || record.Channel == store.PhoneCodeChannelEmailLogin
}

func loginCodeTakeable(record store.PhoneCode) bool {
	return loginCodeVerifiable(record) || record.Channel == store.PhoneCodeChannelEmailSetupRequired
}

func (s *CodeStore) liveCodeLocked(hash string) (codeEntry, bool) {
	entry, ok := s.m[hash]
	if !ok {
		return codeEntry{}, false
	}
	if time.Now().After(entry.expires) {
		s.deleteCodeLocked(hash, entry.code)
		return codeEntry{}, false
	}
	return entry, true
}
