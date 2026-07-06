package memory

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"telesrv/internal/domain"
)

// GroupCallStore 是 store.GroupCallStore 的进程内实现（rpc 单测 fixture 用）。
// 行为契约与 postgres 实现由共享 contract test 钉死（groupcall contract test），
// 凡动 version/参与者语义两边必须同步并跑 PG 集成。
type overrideKey struct {
	callID, setter, target int64
}

type conferenceRandomKey struct {
	creatorID int64
	randomID  int64
}

type inviteMessageKey struct {
	userID int64
	msgID  int
}

type chainKey struct {
	callID     int64
	subChainID int
}

type GroupCallStore struct {
	mu              sync.Mutex
	calls           map[int64]domain.GroupCall
	activeByChan    map[int64]int64 // channelID → active callID
	bySlug          map[string]int64
	byConferenceRnd map[conferenceRandomKey]int64
	participants    map[int64]map[int64]domain.GroupCallParticipant // callID → userID → row
	invites         map[int64][]domain.GroupCallInvite
	inviteByMessage map[inviteMessageKey]domain.GroupCallInvite
	chainBlocks     map[chainKey][]domain.GroupCallChainBlock
	overrides       map[overrideKey]domain.GroupCallParticipantOverride
	raiseHandSeq    map[int64]int64              // callID → 单调举手序号
	rtmpKeys        map[int64]string             // channelID → RTMP 推流密钥
	scheduleSubs    map[int64]map[int64]struct{} // callID → 订阅开播提醒的 userID
	nextSyntheticID int64
}

// NewGroupCallStore 创建内存实现。
func NewGroupCallStore() *GroupCallStore {
	return &GroupCallStore{
		calls:           make(map[int64]domain.GroupCall),
		activeByChan:    make(map[int64]int64),
		bySlug:          make(map[string]int64),
		byConferenceRnd: make(map[conferenceRandomKey]int64),
		participants:    make(map[int64]map[int64]domain.GroupCallParticipant),
		invites:         make(map[int64][]domain.GroupCallInvite),
		inviteByMessage: make(map[inviteMessageKey]domain.GroupCallInvite),
		chainBlocks:     make(map[chainKey][]domain.GroupCallChainBlock),
		overrides:       make(map[overrideKey]domain.GroupCallParticipantOverride),
		raiseHandSeq:    make(map[int64]int64),
		rtmpKeys:        make(map[int64]string),
		scheduleSubs:    make(map[int64]map[int64]struct{}),
	}
}

func (s *GroupCallStore) CreateGroupCall(_ context.Context, call domain.GroupCall) (domain.GroupCall, error) {
	if call.ID == 0 || call.ChannelID == 0 || call.AccessHash == 0 {
		return domain.GroupCall{}, domain.ErrGroupCallInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if activeID, ok := s.activeByChan[call.ChannelID]; ok {
		if existing, ok := s.calls[activeID]; ok && existing.Active() {
			return domain.GroupCall{}, domain.ErrGroupCallAlreadyStarted
		}
	}
	if _, exists := s.calls[call.ID]; exists {
		return domain.GroupCall{}, domain.ErrGroupCallInvalid
	}
	call.Kind = domain.GroupCallKindChannel
	call.State = domain.GroupCallStateActive
	if call.Version <= 0 {
		call.Version = 1
	}
	call.ParticipantsCount = 0
	s.calls[call.ID] = call
	s.activeByChan[call.ChannelID] = call.ID
	s.participants[call.ID] = make(map[int64]domain.GroupCallParticipant)
	return call, nil
}

func (s *GroupCallStore) CreateConferenceCall(_ context.Context, call domain.GroupCall) (domain.GroupCall, error) {
	if call.ID == 0 || call.AccessHash == 0 || call.CreatorUserID == 0 || call.InviteSlug == "" || call.InviteLink == "" {
		return domain.GroupCall{}, domain.ErrGroupCallInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if call.RandomID != 0 {
		if id, ok := s.byConferenceRnd[conferenceRandomKey{creatorID: call.CreatorUserID, randomID: call.RandomID}]; ok {
			existing := s.calls[id]
			return existing, nil
		}
	}
	if _, exists := s.calls[call.ID]; exists {
		return domain.GroupCall{}, domain.ErrGroupCallInvalid
	}
	if _, exists := s.bySlug[call.InviteSlug]; exists {
		return domain.GroupCall{}, domain.ErrGroupCallInvalid
	}
	call.Kind = domain.GroupCallKindConference
	call.ChannelID = 0
	call.State = domain.GroupCallStateActive
	if call.Version <= 0 {
		call.Version = 1
	}
	call.ParticipantsCount = 0
	s.calls[call.ID] = call
	s.bySlug[call.InviteSlug] = call.ID
	if call.RandomID != 0 {
		s.byConferenceRnd[conferenceRandomKey{creatorID: call.CreatorUserID, randomID: call.RandomID}] = call.ID
	}
	s.participants[call.ID] = make(map[int64]domain.GroupCallParticipant)
	return call, nil
}

func (s *GroupCallStore) GetGroupCall(_ context.Context, callID int64) (domain.GroupCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	return call, ok, nil
}

func (s *GroupCallStore) GetGroupCallBySlug(_ context.Context, slug string) (domain.GroupCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.bySlug[slug]
	if !ok {
		return domain.GroupCall{}, false, nil
	}
	call, ok := s.calls[id]
	return call, ok, nil
}

func (s *GroupCallStore) GetGroupCallByInviteMessage(_ context.Context, userID int64, msgID int) (domain.GroupCall, domain.GroupCallInvite, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inv, ok := s.inviteByMessage[inviteMessageKey{userID: userID, msgID: msgID}]
	if !ok {
		return domain.GroupCall{}, domain.GroupCallInvite{}, false, nil
	}
	call, ok := s.calls[inv.CallID]
	return call, inv, ok, nil
}

func (s *GroupCallStore) JoinGroupCall(_ context.Context, req domain.JoinGroupCallRequest) (domain.GroupCallMutation, error) {
	if req.SSRC == 0 {
		return domain.GroupCallMutation{}, domain.ErrGroupCallInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[req.CallID]
	if !ok {
		return domain.GroupCallMutation{}, domain.ErrGroupCallInvalid
	}
	if !call.Active() {
		return domain.GroupCallMutation{}, domain.ErrGroupCallDiscarded
	}
	rows := s.participants[req.CallID]
	for userID, p := range rows {
		if !p.Left && p.SSRC == req.SSRC && userID != req.UserID {
			return domain.GroupCallMutation{}, domain.ErrGroupCallSSRCDuplicate
		}
	}
	existing, rejoining := rows[req.UserID]
	wasActive := rejoining && !existing.Left
	p := domain.GroupCallParticipant{
		CallID:          req.CallID,
		UserID:          req.UserID,
		JoinAsChannelID: req.JoinAsChannelID,
		SSRC:            req.SSRC,
		JoinDate:        req.Now,
		ActiveDate:      req.Now,
		LastCheckDate:   req.Now,
		// VideoJSON 整体替换、PresentationJSON 随全新行清空（rejoin 后客户端
		// 会重发 joinGroupCallPresentation，旧屏幕登记必须作废）。
		VideoJSON: append([]byte(nil), req.VideoJSON...),
		PublicKey: append([]byte(nil), req.PublicKey...),
		JoinBlock: append([]byte(nil), req.JoinBlock...),
	}
	if rejoining && wasActive {
		// 同设备换 ssrc 的 rejoin 保留原 join_date（列表排序稳定）。
		p.JoinDate = existing.JoinDate
	}
	// join_muted 策略：普通成员入会即静音且不可自行开麦（muted_by_admin）。
	if call.JoinMuted && !req.IsAdmin {
		p.Muted = true
		p.MutedByAdmin = true
	}
	rows[req.UserID] = p
	for i, inv := range s.invites[req.CallID] {
		if inv.InviteeUserID == req.UserID && inv.Status == domain.GroupCallInvitePending {
			inv.Status = domain.GroupCallInviteAccepted
			inv.UpdatedAt = req.Now
			s.invites[req.CallID][i] = inv
			s.inviteByMessage[inviteMessageKey{userID: inv.InviteeUserID, msgID: inv.MessageID}] = inv
		}
	}
	if !wasActive {
		call.ParticipantsCount++
	}
	call.Version++
	s.calls[req.CallID] = call
	return domain.GroupCallMutation{Call: call, Participant: p}, nil
}

func (s *GroupCallStore) LeaveGroupCall(_ context.Context, callID, userID int64, now int) (domain.GroupCallMutation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return domain.GroupCallMutation{}, domain.ErrGroupCallInvalid
	}
	p, ok := s.participants[callID][userID]
	if !ok || p.Left {
		return domain.GroupCallMutation{}, domain.ErrGroupCallNotJoined
	}
	p.Left = true
	p.ActiveDate = now
	s.participants[callID][userID] = p
	if call.ParticipantsCount > 0 {
		call.ParticipantsCount--
	}
	call.Version++
	discardEmptyConference(&call, now)
	s.calls[callID] = call
	return domain.GroupCallMutation{Call: call, Participant: p}, nil
}

func (s *GroupCallStore) RemoveConferenceCallParticipants(_ context.Context, req domain.RemoveConferenceCallParticipantsRequest) (domain.RemoveConferenceCallParticipantsResult, error) {
	if req.CallID == 0 || len(req.TargetUserIDs) == 0 || req.OnlyLeft == req.Kick {
		return domain.RemoveConferenceCallParticipantsResult{}, domain.ErrGroupCallInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[req.CallID]
	if !ok || !call.Conference() {
		return domain.RemoveConferenceCallParticipantsResult{}, domain.ErrGroupCallInvalid
	}
	if !call.Active() {
		return domain.RemoveConferenceCallParticipantsResult{}, domain.ErrGroupCallDiscarded
	}
	rows := s.participants[req.CallID]
	targets := uniqueNonZeroInt64s(req.TargetUserIDs...)
	e2eTargets := make([]int64, 0, len(targets))
	mediaTargets := make([]int64, 0, len(targets))
	for _, targetID := range targets {
		p, ok := rows[targetID]
		if !ok {
			continue
		}
		hasE2EMarker := len(p.JoinBlock) > 0
		if req.OnlyLeft {
			if p.Left && hasE2EMarker {
				e2eTargets = append(e2eTargets, targetID)
			}
			continue
		}
		if req.Kick {
			if hasE2EMarker {
				e2eTargets = append(e2eTargets, targetID)
			}
			if !p.Left {
				mediaTargets = append(mediaTargets, targetID)
			}
		}
	}
	out := domain.RemoveConferenceCallParticipantsResult{Call: call}
	if len(e2eTargets) > 0 {
		if len(req.Block) == 0 {
			return domain.RemoveConferenceCallParticipantsResult{}, domain.ErrConferenceChainInvalid
		}
		block, err := s.appendGroupCallChainBlockLocked(domain.GroupCallChainBlock{
			CallID:       req.CallID,
			SubChainID:   0,
			Offset:       -1,
			AuthorUserID: req.AuthorUserID,
			Block:        req.Block,
			CreatedAt:    req.Now,
		})
		if err != nil {
			return domain.RemoveConferenceCallParticipantsResult{}, err
		}
		out.ChainBlock = block
		out.ChainBlockAppended = true
		for _, targetID := range e2eTargets {
			p := rows[targetID]
			p.PublicKey = nil
			p.JoinBlock = nil
			rows[targetID] = p
		}
	}
	if len(mediaTargets) > 0 {
		out.ParticipantsChanged = make([]domain.GroupCallParticipant, 0, len(mediaTargets))
		for _, targetID := range mediaTargets {
			p := rows[targetID]
			if p.Left {
				continue
			}
			p.Left = true
			p.ActiveDate = req.Now
			rows[targetID] = p
			if call.ParticipantsCount > 0 {
				call.ParticipantsCount--
			}
			call.Version++
			out.ParticipantsChanged = append(out.ParticipantsChanged, p)
		}
		discardEmptyConference(&call, req.Now)
		s.calls[req.CallID] = call
		out.Call = call
	}
	return out, nil
}

func (s *GroupCallStore) DiscardGroupCall(_ context.Context, callID int64, now int) (domain.GroupCall, []domain.GroupCallParticipant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return domain.GroupCall{}, nil, domain.ErrGroupCallInvalid
	}
	if !call.Active() {
		return domain.GroupCall{}, nil, domain.ErrGroupCallDiscarded
	}
	var active []domain.GroupCallParticipant
	for userID, p := range s.participants[callID] {
		if p.Left {
			continue
		}
		active = append(active, p)
		p.Left = true
		p.ActiveDate = now
		s.participants[callID][userID] = p
	}
	call.State = domain.GroupCallStateDiscarded
	call.DiscardedAt = now
	call.Duration = max(0, now-call.CreatedAt)
	call.ParticipantsCount = 0
	call.Version++
	s.calls[callID] = call
	if s.activeByChan[call.ChannelID] == callID {
		delete(s.activeByChan, call.ChannelID)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].UserID < active[j].UserID })
	return call, active, nil
}

func (s *GroupCallStore) TouchParticipant(_ context.Context, callID, userID int64, now int) ([]int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return nil, false, domain.ErrGroupCallInvalid
	}
	if !call.Active() {
		return nil, false, nil
	}
	p, ok := s.participants[callID][userID]
	if !ok || p.Left {
		return nil, false, nil
	}
	p.LastCheckDate = now
	p.ActiveDate = now
	s.participants[callID][userID] = p
	return []int64{p.SSRC}, true, nil
}

func (s *GroupCallStore) GetParticipant(_ context.Context, callID, userID int64) (domain.GroupCallParticipant, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.participants[callID][userID]
	return p, ok, nil
}

func (s *GroupCallStore) ListParticipants(_ context.Context, callID int64, offset string, limit int) (domain.GroupCallParticipantPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return domain.GroupCallParticipantPage{}, domain.ErrGroupCallInvalid
	}
	var rows []domain.GroupCallParticipant
	for _, p := range s.participants[callID] {
		if !p.Left {
			rows = append(rows, p)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].JoinDate != rows[j].JoinDate {
			return rows[i].JoinDate < rows[j].JoinDate
		}
		return rows[i].UserID < rows[j].UserID
	})
	page := domain.GroupCallParticipantPage{Count: len(rows), Version: call.Version}
	offDate, offUser, hasOffset := parseGroupCallOffset(offset)
	for _, p := range rows {
		if hasOffset && (p.JoinDate < offDate || (p.JoinDate == offDate && p.UserID <= offUser)) {
			continue
		}
		page.Participants = append(page.Participants, p)
		if len(page.Participants) == limit {
			break
		}
	}
	if n := len(page.Participants); n == limit && n < page.Count {
		last := page.Participants[n-1]
		page.NextOffset = formatGroupCallOffset(last.JoinDate, last.UserID)
	}
	return page, nil
}

func (s *GroupCallStore) UpdateParticipant(_ context.Context, callID, userID int64, update domain.GroupCallParticipantUpdate) (domain.GroupCallMutation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return domain.GroupCallMutation{}, false, domain.ErrGroupCallInvalid
	}
	if !call.Active() {
		return domain.GroupCallMutation{}, false, domain.ErrGroupCallDiscarded
	}
	p, ok := s.participants[callID][userID]
	if !ok || p.Left {
		return domain.GroupCallMutation{}, false, domain.ErrGroupCallNotJoined
	}
	changed := applyGroupCallParticipantUpdate(&p, update)
	if !changed {
		return domain.GroupCallMutation{Call: call, Participant: p}, false, nil
	}
	if update.Now > 0 {
		p.ActiveDate = update.Now
	}
	s.participants[callID][userID] = p
	call.Version++
	s.calls[callID] = call
	return domain.GroupCallMutation{Call: call, Participant: p}, true, nil
}

func (s *GroupCallStore) SetGroupCallTitle(_ context.Context, callID int64, title string) (domain.GroupCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return domain.GroupCall{}, false, domain.ErrGroupCallInvalid
	}
	if call.Title == title {
		return call, false, nil
	}
	call.Title = title
	s.calls[callID] = call
	return call, true, nil
}

func (s *GroupCallStore) SetGroupCallJoinMuted(_ context.Context, callID int64, joinMuted bool) (domain.GroupCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return domain.GroupCall{}, false, domain.ErrGroupCallInvalid
	}
	if call.JoinMuted == joinMuted {
		return call, false, nil
	}
	call.JoinMuted = joinMuted
	s.calls[callID] = call
	return call, true, nil
}

func (s *GroupCallStore) SetStartedMessageID(_ context.Context, callID int64, msgID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return domain.ErrGroupCallInvalid
	}
	call.StartedMsgID = msgID
	s.calls[callID] = call
	return nil
}

func (s *GroupCallStore) SweepStaleParticipants(_ context.Context, checkOlderThan, now, limit int) ([]domain.GroupCallMutation, error) {
	if limit <= 0 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.GroupCallMutation
	for callID, rows := range s.participants {
		call := s.calls[callID]
		if !call.Active() {
			continue
		}
		userIDs := make([]int64, 0, len(rows))
		for userID := range rows {
			userIDs = append(userIDs, userID)
		}
		sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
		for _, userID := range userIDs {
			p := rows[userID]
			if p.Left || p.LastCheckDate >= checkOlderThan {
				continue
			}
			p.Left = true
			p.ActiveDate = now
			rows[userID] = p
			if call.ParticipantsCount > 0 {
				call.ParticipantsCount--
			}
			call.Version++
			out = append(out, domain.GroupCallMutation{Call: call, Participant: p})
			if len(out) == limit {
				s.calls[callID] = call
				return out, nil
			}
		}
		s.calls[callID] = call
	}
	return out, nil
}

func (s *GroupCallStore) ResetAllParticipants(_ context.Context, now int) ([]domain.GroupCall, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.GroupCall
	for callID, rows := range s.participants {
		call := s.calls[callID]
		if !call.Active() {
			continue
		}
		cleared := false
		for userID, p := range rows {
			if p.Left {
				continue
			}
			p.Left = true
			p.ActiveDate = now
			rows[userID] = p
			cleared = true
		}
		if !cleared {
			continue
		}
		call.ParticipantsCount = 0
		call.Version++
		discardEmptyConference(&call, now)
		s.calls[callID] = call
		out = append(out, call)
	}
	return out, nil
}

func discardEmptyConference(call *domain.GroupCall, now int) {
	if call == nil || !call.Conference() || !call.Active() || call.ParticipantsCount > 0 {
		return
	}
	call.State = domain.GroupCallStateDiscarded
	call.DiscardedAt = now
	call.Duration = max(0, now-call.CreatedAt)
}

func applyGroupCallParticipantUpdate(p *domain.GroupCallParticipant, u domain.GroupCallParticipantUpdate) bool {
	changed := false
	if u.Muted != nil && p.Muted != *u.Muted {
		p.Muted = *u.Muted
		changed = true
	}
	if u.MutedByAdmin != nil && p.MutedByAdmin != *u.MutedByAdmin {
		p.MutedByAdmin = *u.MutedByAdmin
		changed = true
	}
	if u.VolumeByAdmin != nil && p.VolumeByAdmin != *u.VolumeByAdmin {
		p.VolumeByAdmin = *u.VolumeByAdmin
		changed = true
	}
	if u.RaiseHandRating != nil && p.RaiseHandRating != *u.RaiseHandRating {
		p.RaiseHandRating = *u.RaiseHandRating
		changed = true
	}
	if u.VideoJSON != nil && string(p.VideoJSON) != string(*u.VideoJSON) {
		p.VideoJSON = append([]byte(nil), *u.VideoJSON...)
		changed = true
	}
	if u.PresentationJSON != nil && string(p.PresentationJSON) != string(*u.PresentationJSON) {
		p.PresentationJSON = append([]byte(nil), *u.PresentationJSON...)
		changed = true
	}
	return changed
}

func parseGroupCallOffset(offset string) (joinDate int, userID int64, ok bool) {
	if offset == "" {
		return 0, 0, false
	}
	parts := strings.Split(offset, ":")
	if len(parts) != 3 || parts[0] != "j" {
		return 0, 0, false
	}
	d, err1 := strconv.Atoi(parts[1])
	u, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return d, u, true
}

func formatGroupCallOffset(joinDate int, userID int64) string {
	return fmt.Sprintf("j:%d:%d", joinDate, userID)
}

func (s *GroupCallStore) NextRaiseHandRating(_ context.Context, callID int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raiseHandSeq[callID]++
	return s.raiseHandSeq[callID], nil
}

func (s *GroupCallStore) SetParticipantOverride(_ context.Context, callID, setterUserID, targetUserID int64, override domain.GroupCallParticipantOverride, clear bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := overrideKey{callID: callID, setter: setterUserID, target: targetUserID}
	if clear {
		delete(s.overrides, key)
		return nil
	}
	s.overrides[key] = override
	return nil
}

func (s *GroupCallStore) GetParticipantOverride(_ context.Context, callID, setterUserID, targetUserID int64) (domain.GroupCallParticipantOverride, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ov, ok := s.overrides[overrideKey{callID: callID, setter: setterUserID, target: targetUserID}]
	return ov, ok, nil
}

func (s *GroupCallStore) CreateConferenceInvite(_ context.Context, invite domain.GroupCallInvite) (domain.GroupCallInvite, error) {
	if invite.CallID == 0 || invite.InviterUserID == 0 || invite.InviteeUserID == 0 || invite.MessageID == 0 {
		return domain.GroupCallInvite{}, domain.ErrGroupCallInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[invite.CallID]
	if !ok || !call.Conference() {
		return domain.GroupCallInvite{}, domain.ErrGroupCallInvalid
	}
	if invite.Status == "" {
		invite.Status = domain.GroupCallInvitePending
	}
	key := inviteMessageKey{userID: invite.InviteeUserID, msgID: invite.MessageID}
	if existing, ok := s.inviteByMessage[key]; ok {
		return existing, nil
	}
	s.invites[invite.CallID] = append(s.invites[invite.CallID], invite)
	s.inviteByMessage[key] = invite
	return invite, nil
}

func (s *GroupCallStore) SetConferenceInviteStatus(_ context.Context, callID int64, inviteeUserID int64, msgID int, status domain.GroupCallInviteStatus, now int) (domain.GroupCallInvite, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := inviteMessageKey{userID: inviteeUserID, msgID: msgID}
	inv, ok := s.inviteByMessage[key]
	if !ok || inv.CallID != callID {
		return domain.GroupCallInvite{}, false, nil
	}
	if inv.Status == status {
		return inv, false, nil
	}
	inv.Status = status
	inv.UpdatedAt = now
	s.inviteByMessage[key] = inv
	for i, row := range s.invites[callID] {
		if row.InviteeUserID == inviteeUserID && row.MessageID == msgID {
			s.invites[callID][i] = inv
			break
		}
	}
	return inv, true, nil
}

func (s *GroupCallStore) ListConferenceRecipientUserIDs(_ context.Context, callID int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return nil, domain.ErrGroupCallInvalid
	}
	includeHistorical := !call.Active()
	seen := map[int64]struct{}{}
	if call.CreatorUserID != 0 {
		seen[call.CreatorUserID] = struct{}{}
	}
	for userID, p := range s.participants[callID] {
		if includeHistorical || !p.Left {
			seen[userID] = struct{}{}
		}
	}
	for _, inv := range s.invites[callID] {
		if includeHistorical || inv.Status == domain.GroupCallInvitePending || inv.Status == domain.GroupCallInviteAccepted {
			seen[inv.InviteeUserID] = struct{}{}
			seen[inv.InviterUserID] = struct{}{}
		}
	}
	out := make([]int64, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *GroupCallStore) AppendGroupCallChainBlock(_ context.Context, block domain.GroupCallChainBlock) (domain.GroupCallChainBlock, error) {
	if block.CallID == 0 || len(block.Block) == 0 {
		return domain.GroupCallChainBlock{}, domain.ErrGroupCallInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendGroupCallChainBlockLocked(block)
}

func (s *GroupCallStore) appendGroupCallChainBlockLocked(block domain.GroupCallChainBlock) (domain.GroupCallChainBlock, error) {
	if call, ok := s.calls[block.CallID]; !ok || !call.Conference() {
		return domain.GroupCallChainBlock{}, domain.ErrGroupCallInvalid
	}
	key := chainKey{callID: block.CallID, subChainID: block.SubChainID}
	rows := s.chainBlocks[key]
	for _, row := range rows {
		if bytes.Equal(row.Block, block.Block) {
			return domain.GroupCallChainBlock{}, domain.ErrConferenceChainInvalid
		}
	}
	nextOffset := 0
	if len(rows) > 0 {
		nextOffset = rows[len(rows)-1].Offset + 1
	}
	if block.Offset < 0 {
		block.Offset = nextOffset
	}
	if block.Offset != nextOffset {
		return domain.GroupCallChainBlock{}, domain.ErrConferenceChainInvalid
	}
	for _, row := range rows {
		if row.Offset == block.Offset {
			return domain.GroupCallChainBlock{}, domain.ErrConferenceChainInvalid
		}
	}
	block.Block = append([]byte(nil), block.Block...)
	s.chainBlocks[key] = append(rows, block)
	sort.Slice(s.chainBlocks[key], func(i, j int) bool {
		return s.chainBlocks[key][i].Offset < s.chainBlocks[key][j].Offset
	})
	return block, nil
}

func (s *GroupCallStore) ListGroupCallChainBlocks(_ context.Context, callID int64, subChainID, offset, limit int) (domain.GroupCallChainBlockPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if call, ok := s.calls[callID]; !ok || !call.Conference() {
		return domain.GroupCallChainBlockPage{}, domain.ErrGroupCallInvalid
	}
	rows := s.chainBlocks[chainKey{callID: callID, subChainID: subChainID}]
	if offset == domain.GroupCallChainBlockLatestOffset {
		page := domain.GroupCallChainBlockPage{NextOffset: 0}
		if len(rows) == 0 {
			return page, nil
		}
		block := rows[len(rows)-1]
		page.Blocks = append(page.Blocks, block)
		page.NextOffset = block.Offset + 1
		return page, nil
	}
	if offset < 0 {
		return domain.GroupCallChainBlockPage{}, domain.ErrGroupCallInvalid
	}
	page := domain.GroupCallChainBlockPage{NextOffset: offset}
	for _, row := range rows {
		if row.Offset < offset {
			continue
		}
		page.Blocks = append(page.Blocks, row)
		page.NextOffset = row.Offset + 1
		if len(page.Blocks) == limit {
			break
		}
	}
	return page, nil
}

func (s *GroupCallStore) StartScheduledGroupCall(_ context.Context, callID int64) (domain.GroupCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	call, ok := s.calls[callID]
	if !ok {
		return domain.GroupCall{}, false, domain.ErrGroupCallInvalid
	}
	if !call.Active() {
		return domain.GroupCall{}, false, domain.ErrGroupCallDiscarded
	}
	if call.ScheduleDate == 0 {
		return call, false, nil
	}
	call.ScheduleDate = 0
	s.calls[callID] = call
	return call, true, nil
}

func (s *GroupCallStore) SetScheduleStartSubscription(_ context.Context, callID, userID int64, subscribed bool) error {
	if callID == 0 || userID == 0 {
		return domain.ErrGroupCallInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	subs := s.scheduleSubs[callID]
	if subscribed {
		if subs == nil {
			subs = make(map[int64]struct{})
			s.scheduleSubs[callID] = subs
		}
		subs[userID] = struct{}{}
		return nil
	}
	delete(subs, userID)
	return nil
}

func (s *GroupCallStore) ListScheduleSubscriberIDs(_ context.Context, callID int64) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs := s.scheduleSubs[callID]
	out := make([]int64, 0, len(subs))
	for id := range subs {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *GroupCallStore) GetRtmpStreamKey(_ context.Context, channelID int64) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.rtmpKeys[channelID]
	return key, ok, nil
}

func (s *GroupCallStore) SetRtmpStreamKey(_ context.Context, channelID int64, key string, _ int) error {
	if channelID == 0 || key == "" {
		return domain.ErrGroupCallInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rtmpKeys[channelID] = key
	return nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
