package memory

import (
	"context"
	"encoding/binary"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type authKeyState struct {
	mu       sync.RWMutex
	keys     map[[8]byte]store.AuthKeyData
	bindings map[[8]byte]domain.TempAuthKeyBinding
}

// AuthKeyStore 是 store.AuthKeyStore 的内存实现。
type AuthKeyStore struct {
	state *authKeyState
}

// NewAuthKeyStore 创建内存 AuthKeyStore。
func NewAuthKeyStore() *AuthKeyStore {
	return &AuthKeyStore{state: &authKeyState{
		keys:     make(map[[8]byte]store.AuthKeyData),
		bindings: make(map[[8]byte]domain.TempAuthKeyBinding),
	}}
}

func (s *AuthKeyStore) Save(_ context.Context, k store.AuthKeyData) error {
	if !store.ValidNewAuthKeyProtocolExpiry(k.ExpiresAt) {
		return store.ErrInvalidAuthKeyProtocolExpiry
	}
	s.state.mu.Lock()
	if current, ok := s.state.keys[k.ID]; ok && (current.Value != k.Value || current.ExpiresAt != k.ExpiresAt) {
		s.state.mu.Unlock()
		return store.ErrAuthKeyProtocolMetadataConflict
	}
	s.state.keys[k.ID] = k
	s.state.mu.Unlock()
	return nil
}

func (s *AuthKeyStore) Get(_ context.Context, id [8]byte) (store.AuthKeyData, bool, error) {
	s.state.mu.RLock()
	k, ok := s.state.keys[id]
	s.state.mu.RUnlock()
	return k, ok, nil
}

func (s *AuthKeyStore) UpdateClientInfo(_ context.Context, id [8]byte, info store.AuthKeyClientInfo) error {
	s.state.mu.Lock()
	k, ok := s.state.keys[id]
	if ok {
		mergeAuthKeyClientInfo(&k, info)
		s.state.keys[id] = k
	}
	s.state.mu.Unlock()
	return nil
}

func mergeAuthKeyClientInfo(k *store.AuthKeyData, info store.AuthKeyClientInfo) {
	if info.Layer > 0 {
		k.Layer = info.Layer
	}
	if info.DeviceModel != "" {
		k.DeviceModel = info.DeviceModel
	}
	if info.Platform != "" {
		k.Platform = info.Platform
	}
	if info.SystemVersion != "" {
		k.SystemVersion = info.SystemVersion
	}
	if info.APIID != 0 {
		k.APIID = info.APIID
	}
	if info.AppVersion != "" {
		k.AppVersion = info.AppVersion
	}
}

func (s *AuthKeyStore) Delete(_ context.Context, id [8]byte) error {
	s.state.mu.Lock()
	deleting, exists := s.state.keys[id]
	if !exists {
		s.state.mu.Unlock()
		return nil
	}
	if deleting.ExpiresAt > 0 {
		delete(s.state.bindings, id)
	} else {
		permID := int64(binary.LittleEndian.Uint64(id[:]))
		for tempID, binding := range s.state.bindings {
			if binding.PermAuthKeyID != permID {
				continue
			}
			delete(s.state.bindings, tempID)
			delete(s.state.keys, tempID)
		}
	}
	delete(s.state.keys, id)
	s.state.mu.Unlock()
	return nil
}

// TempAuthKeyBindingStore 是 store.TempAuthKeyBindingStore 的内存实现。
type TempAuthKeyBindingStore struct {
	state *authKeyState
}

// NewTempAuthKeyBindingStore 创建内存 TempAuthKeyBindingStore。
func NewTempAuthKeyBindingStore(authKeys *AuthKeyStore) *TempAuthKeyBindingStore {
	if authKeys == nil {
		panic("memory.NewTempAuthKeyBindingStore requires a non-nil AuthKeyStore")
	}
	return &TempAuthKeyBindingStore{state: authKeys.state}
}

func (s *TempAuthKeyBindingStore) Save(_ context.Context, b domain.TempAuthKeyBinding) error {
	b.EncryptedMessage = append([]byte(nil), b.EncryptedMessage...)
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if current, ok := s.state.bindings[b.TempAuthKeyID]; ok && current.PermAuthKeyID != b.PermAuthKeyID {
		return store.ErrTempAuthKeyAlreadyBound
	}
	temp, tempFound := s.state.keys[b.TempAuthKeyID]
	var permID [8]byte
	binary.LittleEndian.PutUint64(permID[:], uint64(b.PermAuthKeyID))
	perm, permFound := s.state.keys[permID]
	if !tempFound || !permFound || temp.ExpiresAt <= 0 || perm.ExpiresAt != 0 || b.ExpiresAt != temp.ExpiresAt {
		return store.ErrAuthKeyBindingInvalid
	}
	s.state.bindings[b.TempAuthKeyID] = b
	return nil
}

func (s *TempAuthKeyBindingStore) GetByTemp(_ context.Context, tempAuthKeyID [8]byte) (domain.TempAuthKeyBinding, bool, error) {
	s.state.mu.RLock()
	b, ok := s.state.bindings[tempAuthKeyID]
	s.state.mu.RUnlock()
	if !ok {
		return domain.TempAuthKeyBinding{}, false, nil
	}
	b.EncryptedMessage = append([]byte(nil), b.EncryptedMessage...)
	return b, true, nil
}

func (s *TempAuthKeyBindingStore) DeleteExpired(_ context.Context, expiredBefore int64, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	deleted := 0
	for id, key := range s.state.keys {
		if deleted >= limit {
			break
		}
		if key.ExpiresAt <= 0 || int64(key.ExpiresAt) >= expiredBefore {
			continue
		}
		delete(s.state.bindings, id)
		delete(s.state.keys, id)
		deleted++
	}
	return deleted, nil
}

// AuthorizationStore 是 store.AuthorizationStore 的内存实现。
type AuthorizationStore struct {
	mu sync.RWMutex
	m  map[[8]byte]domain.Authorization
}

// NewAuthorizationStore 创建内存 AuthorizationStore。
func NewAuthorizationStore() *AuthorizationStore {
	return &AuthorizationStore{m: make(map[[8]byte]domain.Authorization)}
}

func (s *AuthorizationStore) Bind(_ context.Context, a domain.Authorization) error {
	now := time.Now()
	if a.Hash == 0 {
		a.Hash = int64(binary.LittleEndian.Uint64(a.AuthKeyID[:]))
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.ActiveAt = now
	s.mu.Lock()
	if existing, ok := s.m[a.AuthKeyID]; ok && !existing.CreatedAt.IsZero() {
		a.CreatedAt = existing.CreatedAt
	}
	s.m[a.AuthKeyID] = a
	s.mu.Unlock()
	return nil
}

func (s *AuthorizationStore) ByAuthKey(_ context.Context, id [8]byte) (domain.Authorization, bool, error) {
	s.mu.RLock()
	a, ok := s.m[id]
	s.mu.RUnlock()
	return a, ok, nil
}

func (s *AuthorizationStore) UpdateLayer(_ context.Context, id [8]byte, layer int) error {
	if layer <= 0 {
		return nil
	}
	s.mu.Lock()
	if a, ok := s.m[id]; ok {
		a.Layer = layer
		a.ActiveAt = time.Now()
		s.m[id] = a
	}
	s.mu.Unlock()
	return nil
}

func (s *AuthorizationStore) UpdateClientInfo(_ context.Context, id [8]byte, info domain.AuthKeyClientInfo) error {
	s.mu.Lock()
	if a, ok := s.m[id]; ok {
		if info.Layer > 0 {
			a.Layer = info.Layer
		}
		if info.DeviceModel != "" {
			a.DeviceModel = info.DeviceModel
		}
		if info.Platform != "" {
			a.Platform = info.Platform
		}
		if info.SystemVersion != "" {
			a.SystemVersion = info.SystemVersion
		}
		if info.APIID != 0 {
			a.APIID = info.APIID
		}
		if info.AppVersion != "" {
			a.AppVersion = info.AppVersion
		}
		a.ActiveAt = time.Now()
		s.m[id] = a
	}
	s.mu.Unlock()
	return nil
}

func (s *AuthorizationStore) MarkPasswordPassed(_ context.Context, id [8]byte) error {
	s.mu.Lock()
	if a, ok := s.m[id]; ok {
		a.PasswordPending = false
		a.ActiveAt = time.Now()
		s.m[id] = a
	}
	s.mu.Unlock()
	return nil
}

func (s *AuthorizationStore) ListByUser(_ context.Context, userID int64) ([]domain.Authorization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Authorization, 0)
	for _, a := range s.m {
		if a.UserID == userID {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *AuthorizationStore) Delete(_ context.Context, id [8]byte) error {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
	return nil
}

func (s *AuthorizationStore) DeleteByHash(_ context.Context, userID, hash int64) (domain.Authorization, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, a := range s.m {
		if a.UserID == userID && a.Hash == hash {
			delete(s.m, id)
			return a, true, nil
		}
	}
	return domain.Authorization{}, false, nil
}

func (s *AuthorizationStore) DeleteByUserExcept(_ context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Authorization, 0)
	for id, a := range s.m {
		if a.UserID != userID || id == keepAuthKeyID {
			continue
		}
		delete(s.m, id)
		out = append(out, a)
	}
	return out, nil
}

// CodeStore 是 store.CodeStore 的内存实现（带 TTL）。
type CodeStore struct {
	mu     sync.Mutex
	m      map[string]codeEntry
	scopes map[store.PhoneCodeScope]string
}

// NewCodeStore 创建内存 CodeStore。
func NewCodeStore() *CodeStore {
	return &CodeStore{
		m:      make(map[string]codeEntry),
		scopes: make(map[store.PhoneCodeScope]string),
	}
}

func (s *CodeStore) Set(_ context.Context, hash string, code store.PhoneCode, ttl time.Duration) error {
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return err
	}
	code.Revision = revision
	s.mu.Lock()
	scope := code.Scope()
	if scope.Valid() {
		if oldHash, ok := s.scopes[scope]; ok && oldHash != hash {
			delete(s.m, oldHash)
		}
		s.scopes[scope] = hash
	}
	s.m[hash] = codeEntry{code: code, expires: time.Now().Add(ttl)}
	s.mu.Unlock()
	return nil
}

func (s *CodeStore) Get(_ context.Context, hash string) (store.PhoneCode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[hash]
	if !ok || time.Now().After(e.expires) {
		if ok {
			s.deleteCodeLocked(hash, e.code)
		}
		return store.PhoneCode{}, false, nil
	}
	return e.code, true, nil
}

func (s *CodeStore) Update(_ context.Context, hash string, code store.PhoneCode) error {
	revision, err := store.NewPhoneCodeRevisionToken()
	if err != nil {
		return err
	}
	code.Revision = revision
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[hash]
	if !ok || time.Now().After(e.expires) {
		if ok {
			s.deleteCodeLocked(hash, e.code)
		}
		return nil
	}
	e.code = code
	s.m[hash] = e
	return nil
}

func (s *CodeStore) Del(_ context.Context, hash string) error {
	s.mu.Lock()
	if e, ok := s.m[hash]; ok {
		s.deleteCodeLocked(hash, e.code)
	} else {
		delete(s.m, hash)
	}
	s.mu.Unlock()
	return nil
}

func (s *CodeStore) ConsumeScoped(_ context.Context, hash string, scope store.PhoneCodeScope) (store.PhoneCode, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !scope.Valid() || s.scopes[scope] != hash {
		return store.PhoneCode{}, false, nil
	}
	e, ok := s.m[hash]
	if !ok || time.Now().After(e.expires) {
		if ok {
			s.deleteCodeLocked(hash, e.code)
		} else {
			delete(s.scopes, scope)
		}
		return store.PhoneCode{}, false, nil
	}
	if e.code.Version != store.PhoneCodeVersionCurrent || e.code.Scope() != scope {
		delete(s.m, hash)
		delete(s.scopes, scope)
		actualScope := e.code.Scope()
		if actualScope.Valid() && s.scopes[actualScope] == hash {
			delete(s.scopes, actualScope)
		}
		return store.PhoneCode{}, false, nil
	}
	s.deleteCodeLocked(hash, e.code)
	return e.code, true, nil
}

func (s *CodeStore) deleteCodeLocked(hash string, code store.PhoneCode) {
	delete(s.m, hash)
	scope := code.Scope()
	if scope.Valid() && s.scopes[scope] == hash {
		delete(s.scopes, scope)
	}
}
