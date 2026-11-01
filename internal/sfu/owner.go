package sfu

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidOwnerRecord            = errors.New("sfu owner: invalid owner record")
	ErrRemoteOwnerDependencyRequired = errors.New("sfu owner: remote owner dependencies are required")
	ErrRemoteOwnerUnavailable        = errors.New("sfu owner: remote owner unavailable")
)

// OwnerRecord is the short lease record of callID -> SFU instance. It only describes the media
// plane owner; business permissions, participant state and version still come from the group call
// store.
type OwnerRecord struct {
	CallID        int64  `json:"call_id"`
	InstanceID    string `json:"instance_id"`
	ControlAddr   string `json:"control_addr,omitempty"`
	AdvertiseIP   string `json:"advertise_ip,omitempty"`
	UDPPort       int    `json:"udp_port,omitempty"`
	UpdatedAtUnix int64  `json:"updated_at_unix"`
	ExpiresAtUnix int64  `json:"expires_at_unix"`
}

type OwnerRegistry interface {
	Claim(ctx context.Context, record OwnerRecord, ttl time.Duration) (owner OwnerRecord, local bool, err error)
	Get(ctx context.Context, callID int64) (OwnerRecord, bool, error)
	Release(ctx context.Context, callID int64, instanceID string) error
	ListInstance(ctx context.Context, instanceID string) ([]OwnerRecord, error)
}

// RemoteService is the future replacement point for telesrv-core -> telesrv-sfu. Without a
// configured remote client, an owner on another instance must fail explicitly, so one call is
// never split into several media rooms.
type RemoteService interface {
	Join(ctx context.Context, owner OwnerRecord, callID, userID int64, kind EndpointKind, offer ClientOffer) (ServerAnswer, error)
	Leave(ctx context.Context, owner OwnerRecord, callID, userID int64, kind EndpointKind) error
	CloseRoom(ctx context.Context, owner OwnerRecord, callID int64) error
	AliveUserIDs(ctx context.Context, owner OwnerRecord, callID int64) ([]int64, error)
}

type ActiveCallProvider interface {
	ActiveCallIDs() []int64
}

type OwnerService struct {
	local       Service
	registry    OwnerRegistry
	remote      RemoteService
	selector    OwnerSelector
	remoteOnly  bool
	instanceID  string
	controlAddr string
	advertiseIP string
	udpPort     int
	ttl         time.Duration
}

type OwnerOption func(*OwnerService)

func WithRemoteService(remote RemoteService) OwnerOption {
	return func(s *OwnerService) {
		s.remote = remote
	}
}

func WithOwnerSelector(selector OwnerSelector) OwnerOption {
	return func(s *OwnerService) {
		s.selector = selector
	}
}

func WithOwnerControlAddr(addr string) OwnerOption {
	return func(s *OwnerService) {
		s.controlAddr = strings.TrimSpace(addr)
	}
}

func WithOwnerAdvertise(advertiseIP string, udpPort int) OwnerOption {
	return func(s *OwnerService) {
		s.advertiseIP = strings.TrimSpace(advertiseIP)
		s.udpPort = udpPort
	}
}

func NewOwnerService(local Service, registry OwnerRegistry, instanceID string, ttl time.Duration, opts ...OwnerOption) (*OwnerService, error) {
	instanceID = strings.TrimSpace(instanceID)
	if local == nil || registry == nil || instanceID == "" || ttl <= 0 {
		return nil, ErrInvalidOwnerRecord
	}
	s := &OwnerService{local: local, registry: registry, instanceID: instanceID, ttl: ttl}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s, nil
}

func NewRemoteOwnerService(registry OwnerRegistry, instanceID string, ttl time.Duration, opts ...OwnerOption) (*OwnerService, error) {
	instanceID = strings.TrimSpace(instanceID)
	if registry == nil || instanceID == "" || ttl <= 0 {
		return nil, ErrInvalidOwnerRecord
	}
	s := &OwnerService{registry: registry, instanceID: instanceID, ttl: ttl, remoteOnly: true}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.remote == nil || s.selector == nil {
		return nil, ErrRemoteOwnerDependencyRequired
	}
	return s, nil
}

func (s *OwnerService) Enabled() bool {
	if s == nil {
		return false
	}
	if s.remoteOnly {
		return s.registry != nil && s.remote != nil && s.selector != nil && s.instanceID != "" && s.ttl > 0
	}
	return s.local != nil && s.local.Enabled()
}

func (s *OwnerService) Join(ctx context.Context, callID, userID int64, kind EndpointKind, offer ClientOffer) (ServerAnswer, error) {
	if s == nil || (!s.remoteOnly && s.local == nil) {
		return ServerAnswer{}, ErrInvalidOwnerRecord
	}
	owner, local, err := s.claim(ctx, callID)
	if err != nil {
		return ServerAnswer{}, err
	}
	if local {
		if s.remoteOnly {
			return ServerAnswer{}, remoteOwnerError(owner)
		}
		return s.local.Join(ctx, callID, userID, kind, offer)
	}
	if s.remote == nil {
		return ServerAnswer{}, remoteOwnerError(owner)
	}
	return s.remote.Join(ctx, owner, callID, userID, kind, offer)
}

func (s *OwnerService) Leave(ctx context.Context, callID, userID int64, kind EndpointKind) error {
	if s == nil || (!s.remoteOnly && s.local == nil) {
		return ErrInvalidOwnerRecord
	}
	owner, found, err := s.registry.Get(ctx, callID)
	if err != nil {
		return err
	}
	if !found {
		if s.remoteOnly {
			return nil
		}
		return s.local.Leave(ctx, callID, userID, kind)
	}
	if owner.InstanceID == s.instanceID {
		if s.remoteOnly {
			return remoteOwnerError(owner)
		}
		return s.local.Leave(ctx, callID, userID, kind)
	}
	if s.remote == nil {
		return remoteOwnerError(owner)
	}
	return s.remote.Leave(ctx, owner, callID, userID, kind)
}

func (s *OwnerService) CloseRoom(ctx context.Context, callID int64) error {
	if s == nil || (!s.remoteOnly && s.local == nil) {
		return ErrInvalidOwnerRecord
	}
	owner, found, err := s.registry.Get(ctx, callID)
	if err != nil {
		return err
	}
	if !found {
		if s.remoteOnly {
			return nil
		}
		if err := s.local.CloseRoom(ctx, callID); err != nil {
			return err
		}
		return s.registry.Release(ctx, callID, s.instanceID)
	}
	if owner.InstanceID == s.instanceID {
		if s.remoteOnly {
			return remoteOwnerError(owner)
		}
		if err := s.local.CloseRoom(ctx, callID); err != nil {
			return err
		}
		return s.registry.Release(ctx, callID, s.instanceID)
	}
	if s.remote == nil {
		return remoteOwnerError(owner)
	}
	if err := s.remote.CloseRoom(ctx, owner, callID); err != nil {
		return err
	}
	return s.registry.Release(ctx, callID, owner.InstanceID)
}

func (s *OwnerService) AliveUserIDs(callID int64) []int64 {
	if s == nil || (!s.remoteOnly && s.local == nil) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	owner, found, err := s.registry.Get(ctx, callID)
	if err != nil || !found {
		if s.remoteOnly {
			return nil
		}
		return s.local.AliveUserIDs(callID)
	}
	if owner.InstanceID == s.instanceID {
		if s.remoteOnly {
			return nil
		}
		return s.local.AliveUserIDs(callID)
	}
	if s.remote == nil {
		return nil
	}
	alive, err := s.remote.AliveUserIDs(ctx, owner, callID)
	if err != nil {
		return nil
	}
	return alive
}

func (s *OwnerService) Refresh(ctx context.Context, callID int64) error {
	if s == nil {
		return ErrInvalidOwnerRecord
	}
	owner, local, err := s.claim(ctx, callID)
	if err != nil {
		return err
	}
	if s.remoteOnly && local {
		return remoteOwnerError(owner)
	}
	if !local {
		return nil
	}
	return nil
}

func (s *OwnerService) RunOwnerHeartbeat(ctx context.Context, interval time.Duration) {
	if s == nil || s.local == nil || s.registry == nil {
		return
	}
	active, ok := s.local.(ActiveCallProvider)
	if !ok {
		return
	}
	if interval <= 0 || interval >= s.ttl {
		interval = s.ttl / 3
	}
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	refresh := func() {
		for _, callID := range active.ActiveCallIDs() {
			_ = s.Refresh(ctx, callID)
		}
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func RunRemoteOwnerHeartbeat(ctx context.Context, owners OwnerRegistry, instances InstanceRegistry, health InstanceHealthChecker, ttl, interval, healthTimeout time.Duration) {
	if owners == nil || instances == nil || ttl <= 0 {
		return
	}
	if interval <= 0 || interval >= ttl {
		interval = ttl / 3
	}
	if interval <= 0 {
		interval = time.Second
	}
	refresh := func() {
		_, _ = RefreshRemoteOwners(ctx, owners, instances, health, ttl, healthTimeout)
	}
	refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func RefreshRemoteOwners(ctx context.Context, owners OwnerRegistry, instances InstanceRegistry, health InstanceHealthChecker, ttl, healthTimeout time.Duration) (int, error) {
	if owners == nil || instances == nil || ttl <= 0 {
		return 0, ErrInvalidOwnerRecord
	}
	records, err := instances.List(ctx)
	if err != nil {
		return 0, err
	}
	refreshed := 0
	var firstErr error
	for _, instance := range records {
		if !ownerHeartbeatInstanceValid(instance) {
			continue
		}
		if !ownerHeartbeatInstanceHealthy(ctx, health, healthTimeout, instance) {
			continue
		}
		current, err := owners.ListInstance(ctx, instance.InstanceID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, owner := range current {
			if owner.CallID <= 0 || strings.TrimSpace(owner.InstanceID) != strings.TrimSpace(instance.InstanceID) {
				continue
			}
			if strings.TrimSpace(owner.ControlAddr) == "" {
				owner.ControlAddr = instance.ControlAddr
			}
			if strings.TrimSpace(owner.AdvertiseIP) == "" {
				owner.AdvertiseIP = instance.AdvertiseIP
			}
			if owner.UDPPort <= 0 {
				owner.UDPPort = instance.UDPPort
			}
			if _, _, err := owners.Claim(ctx, owner, ttl); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			refreshed++
		}
	}
	return refreshed, firstErr
}

func ownerHeartbeatInstanceValid(record InstanceRecord) bool {
	if strings.TrimSpace(record.InstanceID) == "" || strings.TrimSpace(record.ControlAddr) == "" {
		return false
	}
	return record.ActiveCalls >= 0 && record.MaxCalls >= 0
}

func ownerHeartbeatInstanceHealthy(ctx context.Context, health InstanceHealthChecker, timeout time.Duration, record InstanceRecord) bool {
	if health == nil {
		return true
	}
	checkCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		checkCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	return health.CheckInstance(checkCtx, record) == nil
}

func (s *OwnerService) claim(ctx context.Context, callID int64) (OwnerRecord, bool, error) {
	if callID <= 0 || s.registry == nil || s.instanceID == "" || s.ttl <= 0 {
		return OwnerRecord{}, false, ErrInvalidOwnerRecord
	}
	if s.remoteOnly && s.selector == nil {
		return OwnerRecord{}, false, ErrRemoteOwnerDependencyRequired
	}
	record := OwnerRecord{
		CallID:      callID,
		InstanceID:  s.instanceID,
		ControlAddr: s.controlAddr,
		AdvertiseIP: s.advertiseIP,
		UDPPort:     s.udpPort,
	}
	if s.selector != nil {
		selected, err := s.selector.PickOwner(ctx, callID)
		if err != nil {
			return OwnerRecord{}, false, err
		}
		if selected.CallID == 0 {
			selected.CallID = callID
		}
		if selected.CallID != callID {
			return OwnerRecord{}, false, ErrInvalidOwnerRecord
		}
		record = selected
	}
	owner, _, err := s.registry.Claim(ctx, record, s.ttl)
	if err != nil {
		return OwnerRecord{}, false, err
	}
	return owner, owner.InstanceID == s.instanceID, nil
}

func remoteOwnerError(owner OwnerRecord) error {
	return fmt.Errorf("%w: call_id=%d owner=%s control_addr=%s", ErrRemoteOwnerUnavailable, owner.CallID, owner.InstanceID, owner.ControlAddr)
}

// MemoryOwnerRegistry is the owner registry for tests and in-process verification only.
type MemoryOwnerRegistry struct {
	mu     sync.Mutex
	owners map[int64]OwnerRecord
	now    func() time.Time
}

func NewMemoryOwnerRegistry() *MemoryOwnerRegistry {
	return &MemoryOwnerRegistry{owners: make(map[int64]OwnerRecord), now: time.Now}
}

func (r *MemoryOwnerRegistry) Claim(_ context.Context, record OwnerRecord, ttl time.Duration) (OwnerRecord, bool, error) {
	if r == nil || ttl <= 0 || record.CallID <= 0 || strings.TrimSpace(record.InstanceID) == "" {
		return OwnerRecord{}, false, ErrInvalidOwnerRecord
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	if existing, ok := r.owners[record.CallID]; ok {
		if existing.ExpiresAtUnix > now.Unix() && existing.InstanceID != record.InstanceID {
			return existing, false, nil
		}
	}
	record.InstanceID = strings.TrimSpace(record.InstanceID)
	record.UpdatedAtUnix = now.Unix()
	record.ExpiresAtUnix = now.Add(ttl).Unix()
	r.owners[record.CallID] = record
	return record, true, nil
}

func (r *MemoryOwnerRegistry) Get(_ context.Context, callID int64) (OwnerRecord, bool, error) {
	if r == nil || callID <= 0 {
		return OwnerRecord{}, false, ErrInvalidOwnerRecord
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record, ok := r.owners[callID]
	if !ok {
		return OwnerRecord{}, false, nil
	}
	if record.ExpiresAtUnix > 0 && record.ExpiresAtUnix <= r.now().UTC().Unix() {
		delete(r.owners, callID)
		return OwnerRecord{}, false, nil
	}
	return record, true, nil
}

func (r *MemoryOwnerRegistry) Release(_ context.Context, callID int64, instanceID string) error {
	if r == nil || callID <= 0 || strings.TrimSpace(instanceID) == "" {
		return ErrInvalidOwnerRecord
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if record, ok := r.owners[callID]; ok && record.InstanceID == strings.TrimSpace(instanceID) {
		delete(r.owners, callID)
	}
	return nil
}

func (r *MemoryOwnerRegistry) ListInstance(_ context.Context, instanceID string) ([]OwnerRecord, error) {
	if r == nil || strings.TrimSpace(instanceID) == "" {
		return nil, ErrInvalidOwnerRecord
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	instanceID = strings.TrimSpace(instanceID)
	now := r.now().UTC().Unix()
	out := make([]OwnerRecord, 0)
	for callID, record := range r.owners {
		if record.ExpiresAtUnix > 0 && record.ExpiresAtUnix <= now {
			delete(r.owners, callID)
			continue
		}
		if record.InstanceID == instanceID {
			out = append(out, record)
		}
	}
	return out, nil
}
