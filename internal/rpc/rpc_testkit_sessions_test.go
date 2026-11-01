package rpc

import (
	"context"
	"sync"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/edgecontrol"
)

type captureSessions struct {
	mu                     sync.Mutex
	rawAuthKeyID           [8]byte
	sessionID              int64
	userID                 int64
	userResolved           bool
	authKeyID              [8]byte
	authKeyResolved        bool
	receives               bool
	receivesCalls          int
	messageType            proto.MessageType
	message                bin.Encoder
	userMessage            bin.Encoder // the most recent PushToUser* message (distinct from message: message is also overwritten by PushToSession)
	pushUserIDs            []int64
	onlineUserIDs          []int64
	channelViewers         map[int64][]int64
	channelMembers         map[int64][]int64
	channelSubscribers     map[int64][]int64
	membershipSyncAcquired bool
	// channelViewersLimit records the limit received by the most recent
	// OnlineChannelUserIDs, verifying the fan-out cap parameter.
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

// onlineChannelMemberIDs returns a snapshot of the currently registered channel
// online-member index (for test assertions).
func (s *captureSessions) onlineChannelMemberIDs(channelID int64) []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.channelMembers[channelID]...)
}

// lastUserPush returns the most recent PushToUser* message, independent of message
// (the latter is also overwritten with peer status by
// pushOnlinePeerStatusesToCurrentSession via PushToSession).
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

func (s *captureSessions) PushToUserTransientExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass, _ time.Duration) (int, error) {
	return s.PushToUserExceptAuthKeySession(ctx, userID, excludeAuthKeyID, excludeSessionID, t, msg)
}

func (s *captureSessions) UserLocationRecordsForUsers(_ context.Context, userIDs []int64) (map[int64][]EdgeLocationRecord, error) {
	out := make(map[int64][]EdgeLocationRecord, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		out[userID] = []EdgeLocationRecord{{
			InstanceID:      "edge-test",
			UserID:          userID,
			ReceivesUpdates: true,
		}}
	}
	return out, nil
}

func (s *captureSessions) PushToUserLocationBatches(ctx context.Context, pushes []LocationTargetedUserPush) (int, error) {
	sent := 0
	var firstErr error
	for _, push := range pushes {
		select {
		case <-ctx.Done():
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			return sent, firstErr
		default:
		}
		if push.TargetUserID == 0 || len(push.Locations) == 0 || push.Update == nil {
			continue
		}
		messageType := push.MessageType
		if messageType == proto.MessageUnknown {
			messageType = proto.MessageFromServer
		}
		n, err := s.PushToUserExceptAuthKeySession(ctx, push.TargetUserID, push.ExcludeAuthKeyID, push.ExcludeSessionID, messageType, push.Update)
		sent += n
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return sent, firstErr
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

func (s *captureSessions) BeginSessionChannelMembershipSync(_ context.Context, rawAuthKeyID [8]byte, sessionID, _ int64) (int64, edgecontrol.ChannelMembershipSyncDisposition, error) {
	if s.membershipSyncAcquired {
		return 1, edgecontrol.ChannelMembershipSyncAcquired, nil
	}
	// Generic router tests do not model a durable channel authority. Treat their
	// explicit fake baseline as already prepared and mirror Edge activation.
	s.SetReceivesUpdatesForAuthKey(rawAuthKeyID, sessionID, true)
	return 0, edgecontrol.ChannelMembershipSyncPrepared, nil
}

func (s *captureSessions) AppendSessionChannelMembershipSync(_ context.Context, _ [8]byte, _ int64, userID, _ int64, channelIDs []int64) error {
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
	return nil
}

func (s *captureSessions) CommitSessionChannelMembershipSync(_ context.Context, rawAuthKeyID [8]byte, sessionID, _ int64, _ int64) (bool, error) {
	s.SetReceivesUpdatesForAuthKey(rawAuthKeyID, sessionID, true)
	return true, nil
}

func (s *captureSessions) AbortSessionChannelMembershipSync(context.Context, [8]byte, int64, int64, int64) {
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
