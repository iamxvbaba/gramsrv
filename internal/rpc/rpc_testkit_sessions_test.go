package rpc

import (
	"context"
	"sync"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
)

type captureSessions struct {
	mu                 sync.Mutex
	rawAuthKeyID       [8]byte
	sessionID          int64
	userID             int64
	userResolved       bool
	authKeyID          [8]byte
	authKeyResolved    bool
	receives           bool
	receivesCalls      int
	messageType        proto.MessageType
	message            bin.Encoder
	userMessage        bin.Encoder // 最近一次 PushToUser* 的消息（与 message 区分：message 也被 PushToSession 覆盖）
	pushUserIDs        []int64
	onlineUserIDs      []int64
	channelViewers     map[int64][]int64
	channelMembers     map[int64][]int64
	channelSubscribers map[int64][]int64
	// channelViewersLimit 记录最近一次 OnlineChannelUserIDs 收到的 limit，验证 fan-out 封顶传参。
	channelViewersLimit int
}

type captureSessionsSnapshot struct {
	sessionID       int64
	userID          int64
	userResolved    bool
	authKeyID       [8]byte
	authKeyResolved bool
	receives        bool
	receivesCalls   int
	messageType     proto.MessageType
	message         bin.Encoder
}

func (s *captureSessions) snapshot() captureSessionsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return captureSessionsSnapshot{
		sessionID:       s.sessionID,
		userID:          s.userID,
		userResolved:    s.userResolved,
		authKeyID:       s.authKeyID,
		authKeyResolved: s.authKeyResolved,
		receives:        s.receives,
		receivesCalls:   s.receivesCalls,
		messageType:     s.messageType,
		message:         s.message,
	}
}

func (s *captureSessions) pushedUserIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.pushUserIDs...)
}

// onlineChannelMemberIDs 返回当前登记的频道在线成员索引快照（测试断言用）。
func (s *captureSessions) onlineChannelMemberIDs(channelID int64) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.channelMembers[channelID]...)
}

// lastUserPush 返回最近一次 PushToUser* 的消息，独立于 message（后者也会被
// pushOnlinePeerStatusesToCurrentSession 经 PushToSession 覆盖成对端状态）。
func (s *captureSessions) lastUserPush() bin.Encoder {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userMessage
}

func (s *captureSessions) clearMessages() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.message = nil
	s.userMessage = nil
	s.pushUserIDs = nil
}

func (s *captureSessions) BindAuthKeyForSession(rawAuthKeyID [8]byte, sessionID int64, authKeyID [8]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authKeyResolved && s.authKeyID != authKeyID {
		s.userID = 0
		s.userResolved = false
	}
	s.rawAuthKeyID = rawAuthKeyID
	s.sessionID = sessionID
	s.authKeyID = authKeyID
	s.authKeyResolved = true
}

func (s *captureSessions) AuthKeyIDForSession([8]byte, int64) ([8]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authKeyID, s.authKeyResolved
}

// captureSessions models an ordinary permanent-key connection unless a test
// wraps/overrides it with temporary-key metadata.
func (s *captureSessions) AuthKeyExpiresAtForSession([8]byte, int64) (int, bool) {
	return 0, true
}

func (s *captureSessions) BindUserForAuthKey(rawAuthKeyID [8]byte, sessionID, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawAuthKeyID = rawAuthKeyID
	s.sessionID = sessionID
	s.userID = userID
	s.userResolved = true
}

func (s *captureSessions) UserIDResolvedForAuthKey([8]byte, int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.userID, s.userResolved
}

func (s *captureSessions) UnbindAuthKey(authKeyID [8]byte) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authKeyID == authKeyID {
		s.userID = 0
		s.userResolved = true
		return 1
	}
	return 0
}

func (s *captureSessions) SetReceivesUpdatesForAuthKey(rawAuthKeyID [8]byte, sessionID int64, receives bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawAuthKeyID = rawAuthKeyID
	s.sessionID = sessionID
	s.receives = receives
	s.receivesCalls++
}

func (s *captureSessions) PushToSessionForAuthKey(_ context.Context, rawAuthKeyID [8]byte, sessionID int64, t proto.MessageType, msg tg.UpdatesClass) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawAuthKeyID = rawAuthKeyID
	s.sessionID = sessionID
	s.messageType = t
	s.message = msg
	return nil
}

func (s *captureSessions) PushToUserExceptAuthKeySession(_ context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userID = userID
	s.rawAuthKeyID = excludeAuthKeyID
	s.sessionID = excludeSessionID
	s.messageType = t
	s.message = msg
	s.userMessage = msg
	s.pushUserIDs = append(s.pushUserIDs, userID)
	return 1, nil
}

func (s *captureSessions) IsUserOnline(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.onlineUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func (s *captureSessions) OnlineUserIDsForCandidates(candidateUserIDs []int64, limit int) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	online := make(map[int64]struct{}, len(s.onlineUserIDs))
	for _, id := range s.onlineUserIDs {
		online[id] = struct{}{}
	}
	out := make([]int64, 0, len(candidateUserIDs))
	seen := map[int64]struct{}{}
	for _, id := range candidateUserIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if _, ok := online[id]; !ok {
			continue
		}
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *captureSessions) TrackChannelInterest(_ [8]byte, _ int64, userID int64, channelIDs []int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelViewers == nil {
		s.channelViewers = make(map[int64][]int64)
	}
	for channelID, viewers := range s.channelViewers {
		out := viewers[:0]
		for _, viewerID := range viewers {
			if viewerID != userID {
				out = append(out, viewerID)
			}
		}
		if len(out) == 0 {
			delete(s.channelViewers, channelID)
			continue
		}
		s.channelViewers[channelID] = out
	}
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		s.channelViewers[channelID] = append(s.channelViewers[channelID], userID)
	}
}

func (s *captureSessions) ClearChannelInterest(_ [8]byte, _ int64, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for channelID, viewers := range s.channelViewers {
		out := viewers[:0]
		for _, viewerID := range viewers {
			if viewerID != userID {
				out = append(out, viewerID)
			}
		}
		if len(out) == 0 {
			delete(s.channelViewers, channelID)
			continue
		}
		s.channelViewers[channelID] = out
	}
}

func (s *captureSessions) OnlineChannelUserIDs(channelID int64, limit int) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channelViewersLimit = limit
	return limitIDs(s.channelViewers[channelID], limit)
}

func (s *captureSessions) RefreshChannelSubscription(_ [8]byte, _ int64, userID, channelID int64, _ time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelSubscribers == nil {
		s.channelSubscribers = make(map[int64][]int64)
	}
	for _, existing := range s.channelSubscribers[channelID] {
		if existing == userID {
			return
		}
	}
	s.channelSubscribers[channelID] = append(s.channelSubscribers[channelID], userID)
}

func (s *captureSessions) OnlineChannelSubscriberUserIDs(channelID int64, limit int) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return limitIDs(s.channelSubscribers[channelID], limit)
}

func (s *captureSessions) OnlineChannelSubscriberUserIDsExcluding(channelID int64, exclude map[int64]struct{}, limit int) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, 0, len(s.channelSubscribers[channelID]))
	for _, userID := range s.channelSubscribers[channelID] {
		if _, skip := exclude[userID]; skip {
			continue
		}
		out = append(out, userID)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *captureSessions) ChannelMembershipGeneration(_ [8]byte, _ int64) int64 { return 0 }

func (s *captureSessions) SetSessionChannelMemberships(_ [8]byte, _ int64, userID int64, channelIDs []int64, _ int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelMembers == nil {
		s.channelMembers = make(map[int64][]int64)
	}
	for _, channelID := range channelIDs {
		if channelID == 0 {
			continue
		}
		s.channelMembers[channelID] = append(s.channelMembers[channelID], userID)
	}
}

func (s *captureSessions) AddUserChannelMembership(userID, channelID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelMembers == nil {
		s.channelMembers = make(map[int64][]int64)
	}
	s.channelMembers[channelID] = append(s.channelMembers[channelID], userID)
}

func (s *captureSessions) RemoveUserChannelMembership(userID, channelID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	members := s.channelMembers[channelID]
	out := members[:0]
	for _, id := range members {
		if id != userID {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		delete(s.channelMembers, channelID)
		return
	}
	s.channelMembers[channelID] = out
}

func (s *captureSessions) OnlineChannelMemberUserIDs(channelID int64, limit int) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return limitIDs(s.channelMembers[channelID], limit)
}
