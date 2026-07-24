package updates

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service 提供 update 状态查询。
type Service struct {
	states store.UpdateStateStore
	events store.UpdateEventStore
	log    *zap.Logger
}

type dispatchingEventAppender interface {
	AppendAllocatedWithDispatch(ctx context.Context, userID int64, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, error)
}

type newMessageEventFinder interface {
	FindNewMessageEvent(ctx context.Context, userID int64, messageBoxID int) (domain.UpdateEvent, bool, error)
}

type userUpdateRetentionCheckpointStore interface {
	UserUpdateRetentionCheckpoint(ctx context.Context, authKeyID [8]byte, userID int64) (pts, date int, ok bool, err error)
}

// ServiceOption 调整 updates 服务的运行时依赖。
type ServiceOption func(*Service)

// WithLogger 注入 update 状态机日志器，用于追踪 pts 分配、append 失败和 difference gap。
func WithLogger(log *zap.Logger) ServiceOption {
	return func(s *Service) {
		s.log = log
	}
}

// NewService 创建 updates 服务。
func NewService(states store.UpdateStateStore, events store.UpdateEventStore, opts ...ServiceOption) *Service {
	s := &Service{states: states}
	s.events = events
	for _, opt := range opts {
		opt(s)
	}
	if s.log == nil {
		s.log = zap.NewNop()
	}
	return s
}

// UsesReliableDispatch 表示设置类 update 已写入 transactional outbox，由 outbox worker 投递在线 session。
func (s *Service) UsesReliableDispatch() bool {
	if s == nil || s.events == nil {
		return false
	}
	_, ok := s.events.(dispatchingEventAppender)
	return ok
}

// GetState 返回当前 auth_key + user 维度已确认的 update 状态。
// user_update_events 是账号级 durable log；auth_key 维度只保存设备已经通过
// getDifference 确认到的状态，不能在 getState 中直接推进到账号最新水位。
func (s *Service) GetState(ctx context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, error) {
	now := int(time.Now().Unix())
	// 私聊阶段不维护账号级 seq：对外 UpdateState.Seq 恒为 0，客户端仅靠 pts 同步、
	// 跳过 seq gap 检测（推送信封 seq 同样恒 0）。
	if s.states == nil {
		current, err := s.currentPts(ctx, userID)
		if err != nil {
			return domain.UpdateState{}, err
		}
		return domain.UpdateState{Pts: current, Date: now, Seq: 0}, nil
	}
	st, found, err := s.states.Get(ctx, authKeyID, userID)
	if err != nil {
		return domain.UpdateState{}, err
	}
	if found {
		st.Seq = 0
		if st.Date == 0 {
			st.Date = now
		}
		return st, nil
	}
	current, err := s.currentPts(ctx, userID)
	if err != nil {
		return domain.UpdateState{}, err
	}
	st = domain.UpdateState{Pts: current, Date: now, Seq: 0}
	if err := s.states.Save(ctx, authKeyID, userID, st); err != nil {
		return domain.UpdateState{}, err
	}
	return st, nil
}

// CurrentState 返回账号当前最大连续 update 状态，不修改任何设备已确认水位。
func (s *Service) CurrentState(ctx context.Context, userID int64) (domain.UpdateState, error) {
	return s.currentState(ctx, userID)
}

// ConfirmedState returns the device-local confirmed update state, if any,
// without bootstrapping it to the account-current pts.
func (s *Service) ConfirmedState(ctx context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, bool, error) {
	if s == nil || s.states == nil {
		return domain.UpdateState{}, false, nil
	}
	st, found, err := s.states.Get(ctx, authKeyID, userID)
	if err != nil {
		return domain.UpdateState{}, false, err
	}
	st.Seq = 0
	return st, found, nil
}

// ConfirmEvent 把一个已在其它业务事务中原子提交的事件标记为当前设备已消费。
// 典型调用是 account.changePhone：RPC result 已携最新 User，当前设备不应再收
// updateUserPhone，但仍需把设备确认水位推进到该事件 pts。
func (s *Service) ConfirmEvent(ctx context.Context, authKeyID [8]byte, userID int64, event domain.UpdateEvent) error {
	if event.UserID != 0 && event.UserID != userID {
		return fmt.Errorf("confirm update event user mismatch: event=%d caller=%d", event.UserID, userID)
	}
	if event.Pts <= 0 {
		return nil
	}
	date := event.Date
	if date == 0 {
		date = int(time.Now().Unix())
	}
	return s.saveConfirmedState(ctx, authKeyID, userID, domain.UpdateState{Pts: event.Pts, Date: date, Seq: 0})
}

// ObserveDifferenceRequest records only the cursor a client carried into this
// request. It is deliberately independent from response delivery: even when
// encoding or the socket write later fails, the request still proves the client
// already owned this (clamped) cursor before contacting us.
func (s *Service) ObserveDifferenceRequest(ctx context.Context, authKeyID [8]byte, userID int64, from domain.UpdateState) (domain.UpdateState, error) {
	current, err := s.currentState(ctx, userID)
	if err != nil {
		return domain.UpdateState{}, err
	}
	from = clampDifferenceState(from, current)
	if err := s.observeClientState(ctx, authKeyID, userID, from); err != nil {
		return domain.UpdateState{}, err
	}
	return from, nil
}

// CommitDeliveredState persists the exact cursor justified by a physically
// delivered RPC result. The store owns the atomic/monotonic invariant because
// delivery callbacks from different responses may complete out of order.
func (s *Service) CommitDeliveredState(ctx context.Context, authKeyID [8]byte, userID int64, st domain.UpdateState, mode domain.UpdateStateCommitMode) error {
	if s.states == nil {
		return nil
	}
	if mode != domain.UpdateStateCommitDeliveredOnly && mode != domain.UpdateStateCommitDeliveredAndObservedBaseline {
		return fmt.Errorf("invalid delivered update state commit mode %d", mode)
	}
	st.Seq = 0
	return s.states.CommitDeliveredState(ctx, authKeyID, userID, st, mode)
}

// getDifferenceLimit 是单次 getDifference 返回的最大连续事件数；超出置 Partial 让客户端翻页。
const getDifferenceLimit = 100

// GetDifference 返回当前 user 从 from 状态之后的增量事件。
//
// 对齐 MTProto：只返回从 from.Pts 起「连续」的事件（遇空洞即截断），State.Pts 取最后连续值，
// 绝不让客户端跳过异常空洞而丢消息；正常写路径在 PG 事务内推进 pts 和 durable event。
// 连续事件填满 limit 时置 Partial（映射 differenceSlice），客户端据返回 State 继续翻页。
func (s *Service) GetDifference(ctx context.Context, authKeyID [8]byte, userID int64, from domain.UpdateState) (domain.UpdateDifference, error) {
	st, err := s.currentState(ctx, userID)
	if err != nil {
		return domain.UpdateDifference{}, err
	}
	// Computation is pure with respect to device confirmed/observed state. The
	// request observer and physical-delivery commit are explicit caller phases.
	from = clampDifferenceState(from, st)
	// TDesktop 不支持账号级 updates.differenceTooLong。retention 只能删除所有授权
	// 设备都已确认的共同前缀；当前设备若仍带更旧 pts，用一个空的普通
	// differenceSlice 把 IntermediateState 推进到已确认 checkpoint，再从 live tail 续拉。
	if checkpoint, found, err := s.retainedPrefixCheckpoint(ctx, authKeyID, userID, from, st); err != nil {
		return domain.UpdateDifference{}, err
	} else if found {
		return checkpoint, nil
	}
	if s.events == nil || from.Pts >= st.Pts {
		if from.Date != 0 {
			st.Date = from.Date
		}
		return domain.UpdateDifference{State: st}, nil
	}
	events, err := s.events.ListAfter(ctx, userID, from.Pts, getDifferenceLimit)
	if err != nil {
		return domain.UpdateDifference{}, err
	}
	contiguous, gapEvent, expectedPts := contiguousPrefixAndGap(events, from.Pts)
	// Retention may advance after the pre-read checkpoint probe and before ListAfter obtains its
	// statement snapshot. If it removed the whole requested prefix, the read is empty or starts at a
	// gap. Re-read the checkpoint before returning a non-advancing empty difference; otherwise a
	// client can believe synchronization completed while retaining a cursor below deleted history.
	if len(contiguous) == 0 && from.Pts < st.Pts {
		if checkpoint, found, err := s.retainedPrefixCheckpoint(ctx, authKeyID, userID, from, st); err != nil {
			return domain.UpdateDifference{}, err
		} else if found {
			return checkpoint, nil
		}
	}
	last := from.Pts
	if len(contiguous) > 0 {
		last = contiguous[len(contiguous)-1].Pts
	}
	if gapEvent != nil {
		ptsCount := gapEvent.PtsCount
		if ptsCount <= 0 {
			ptsCount = 1
		}
		s.log.Warn("difference_stopped_at_gap",
			zap.String("scope", "user"),
			zap.Int64("user_id", userID),
			zap.Int("request_pts", from.Pts),
			zap.Int("current_pts", st.Pts),
			zap.Int("returned_pts", last),
			zap.Int("expected_pts", expectedPts),
			zap.Int("got_pts", gapEvent.Pts),
			zap.Int("got_pts_count", ptsCount),
			zap.String("event_type", string(gapEvent.Type)),
			zap.Int("events_read", len(events)),
			zap.Int("events_returned", len(contiguous)),
		)
	}
	out := st
	out.Pts = last
	out.Seq = 0 // seq 恒 0，见 GetState 注释
	if len(contiguous) > 0 {
		out.Date = contiguous[len(contiguous)-1].Date
	}
	return domain.UpdateDifference{
		State:   out,
		Events:  contiguous,
		Partial: len(contiguous) == getDifferenceLimit,
	}, nil
}

func (s *Service) retainedPrefixCheckpoint(ctx context.Context, authKeyID [8]byte, userID int64, from, current domain.UpdateState) (domain.UpdateDifference, bool, error) {
	checkpoints, ok := s.events.(userUpdateRetentionCheckpointStore)
	if !ok {
		return domain.UpdateDifference{}, false, nil
	}
	pts, date, found, err := checkpoints.UserUpdateRetentionCheckpoint(ctx, authKeyID, userID)
	if err != nil {
		return domain.UpdateDifference{}, false, err
	}
	if !found || from.Pts >= pts {
		return domain.UpdateDifference{}, false, nil
	}
	checkpoint := from
	checkpoint.Pts = pts
	checkpoint.Seq = 0
	if date > 0 {
		checkpoint.Date = date
	} else if checkpoint.Date == 0 {
		checkpoint.Date = current.Date
	}
	return domain.UpdateDifference{State: checkpoint, Partial: true}, true, nil
}

func clampDifferenceState(from, current domain.UpdateState) domain.UpdateState {
	if from.Pts < 0 {
		from.Pts = 0
	}
	if from.Pts > current.Pts {
		from.Pts = current.Pts
	}
	from.Seq = 0
	return from
}

func (s *Service) currentState(ctx context.Context, userID int64) (domain.UpdateState, error) {
	current, err := s.currentPts(ctx, userID)
	if err != nil {
		return domain.UpdateState{}, err
	}
	return domain.UpdateState{
		Pts:  current,
		Date: int(time.Now().Unix()),
		Seq:  0,
	}, nil
}

func (s *Service) saveConfirmedState(ctx context.Context, authKeyID [8]byte, userID int64, st domain.UpdateState) error {
	if s.states == nil {
		return nil
	}
	st.Seq = 0
	return s.states.Save(ctx, authKeyID, userID, st)
}

func (s *Service) observeClientState(ctx context.Context, authKeyID [8]byte, userID int64, st domain.UpdateState) error {
	if s.states == nil {
		return nil
	}
	st.Seq = 0
	return s.states.ObserveClientState(ctx, authKeyID, userID, st)
}

// contiguousPrefix 返回从 from 起 pts 严格连续（from+1, from+2, ...）的事件前缀。
// 先按 pts 升序排序以兼容存储返回顺序，遇到空洞即停。
func contiguousPrefix(events []domain.UpdateEvent, from int) []domain.UpdateEvent {
	out, _, _ := contiguousPrefixAndGap(events, from)
	return out
}

func contiguousPrefixAndGap(events []domain.UpdateEvent, from int) ([]domain.UpdateEvent, *domain.UpdateEvent, int) {
	if len(events) == 0 {
		return nil, nil, 0
	}
	sorted := make([]domain.UpdateEvent, len(events))
	copy(sorted, events)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Pts < sorted[j].Pts })
	cursor := from
	out := make([]domain.UpdateEvent, 0, len(sorted))
	for _, event := range sorted {
		ptsCount := event.PtsCount
		if ptsCount <= 0 {
			ptsCount = 1
		}
		expected := cursor + ptsCount
		if event.Pts != expected {
			gap := event
			return out, &gap, expected
		}
		out = append(out, event)
		cursor = event.Pts
	}
	return out, nil, 0
}

// ClearAuthKey 清理某 auth_key 的设备状态。
// user_update_events 是账号级 durable log，不能因设备退出登录被删除。
func (s *Service) ClearAuthKey(ctx context.Context, authKeyID [8]byte) error {
	if s.states != nil {
		if err := s.states.DeleteAuthKey(ctx, authKeyID); err != nil {
			return err
		}
	}
	return nil
}

// RecordNewMessage 推进 update 状态并追加一条 new_message 事件。
func (s *Service) RecordNewMessage(ctx context.Context, authKeyID [8]byte, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 {
		userID = msg.OwnerUserID
	}
	date := msg.Date
	if date == 0 {
		date = int(time.Now().Unix())
	}
	return s.recordEvent(ctx, authKeyID, [8]byte{}, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventNewMessage,
		Date:     date,
		Message:  msg,
		PtsCount: 1,
	}, false, 0)
}

// PublishNewMessage appends an account-visible message update and enqueues
// online dispatch without acknowledging any device-local update state.
func (s *Service) PublishNewMessage(ctx context.Context, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 {
		userID = msg.OwnerUserID
	}
	if finder, ok := s.events.(newMessageEventFinder); ok && msg.ID > 0 {
		event, found, err := finder.FindNewMessageEvent(ctx, userID, msg.ID)
		if err != nil {
			return domain.UpdateEvent{}, domain.UpdateState{}, err
		}
		if found {
			st, err := s.currentState(ctx, userID)
			if err != nil {
				return domain.UpdateEvent{}, domain.UpdateState{}, err
			}
			return event, st, nil
		}
	}
	date := msg.Date
	if date == 0 {
		date = int(time.Now().Unix())
	}
	return s.recordEventCore(ctx, [8]byte{}, [8]byte{}, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventNewMessage,
		Date:     date,
		Message:  msg,
		PtsCount: 1,
	}, true, 0, false)
}

// RecordMessageReactions records a durable marker for message reaction changes.
//
// updateMessageReactions has no pts fields in Layer 225, but TDesktop still
// needs getDifference to advance account pts and carry the latest reaction
// aggregate for offline devices.
func (s *Service) RecordMessageReactions(ctx context.Context, authKeyID [8]byte, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 {
		userID = msg.OwnerUserID
	}
	date := msg.Date
	if date == 0 {
		date = int(time.Now().Unix())
	}
	return s.recordEventWithoutState(ctx, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventMessageReactions,
		Date:     date,
		Message:  msg,
		Peer:     msg.Peer,
		PtsCount: 1,
	})
}

// RecordMessagePoll records a durable marker for message poll state changes
// (vote / close). updateMessagePoll has no pts fields in Layer 225 — same
// bookkeeping shape as RecordMessageReactions.
func (s *Service) RecordMessagePoll(ctx context.Context, authKeyID [8]byte, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 {
		userID = msg.OwnerUserID
	}
	date := msg.Date
	if date == 0 {
		date = int(time.Now().Unix())
	}
	return s.recordEventWithoutState(ctx, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventMessagePoll,
		Date:     date,
		Message:  msg,
		Peer:     msg.Peer,
		PtsCount: 1,
	})
}

// RecordStory records a story snapshot change for offline difference replay.
func (s *Service) RecordStory(ctx context.Context, stateAuthKeyID [8]byte, userID int64, story domain.Story, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 && story.Owner.Type == domain.PeerTypeUser {
		userID = story.Owner.ID
	}
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventStory,
		Date:     story.Date,
		Peer:     story.Owner,
		Story:    story,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordStoryFanout records a story visibility change for a user who did not
// initiate the RPC that caused it. It writes durable updates/outbox but does
// not acknowledge any device-local update state.
func (s *Service) RecordStoryFanout(ctx context.Context, userID int64, story domain.Story) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 {
		return domain.UpdateEvent{}, domain.UpdateState{}, domain.ErrStoryPeerInvalid
	}
	return s.recordEventCore(ctx, [8]byte{}, [8]byte{}, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventStory,
		Date:     story.Date,
		Peer:     story.Owner,
		Story:    story,
		PtsCount: 1,
	}, true, 0, false)
}

// RecordReadStories records a read boundary update for multi-device sync.
func (s *Service) RecordReadStories(ctx context.Context, stateAuthKeyID [8]byte, userID int64, read domain.StoryReadResult, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 {
		userID = read.ViewerID
	}
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventReadStories,
		Date:     read.Date,
		Peer:     read.Peer,
		MaxID:    read.MaxReadID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordSentStoryReaction records the current user's story reaction for multi-device sync.
func (s *Service) RecordSentStoryReaction(ctx context.Context, stateAuthKeyID [8]byte, userID int64, reaction domain.StoryReactionResult, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 {
		userID = reaction.ViewerID
	}
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventSentStoryReaction,
		Date:     reaction.Date,
		Peer:     reaction.Peer,
		MaxID:    reaction.StoryID,
		Story:    reaction.Story,
		Reaction: reaction.Reaction,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordNewStoryReaction records the story owner's notification for a reaction
// sent by another user. It does not advance any owner device confirmation state:
// the owner did not initiate the RPC, but online outbox and offline difference
// must still see the durable event.
func (s *Service) RecordNewStoryReaction(ctx context.Context, stateAuthKeyID [8]byte, ownerUserID int64, reaction domain.StoryReactionResult, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	if ownerUserID == 0 && reaction.Story.Owner.Type == domain.PeerTypeUser {
		ownerUserID = reaction.Story.Owner.ID
	}
	if ownerUserID == 0 && reaction.Peer.Type == domain.PeerTypeUser {
		ownerUserID = reaction.Peer.ID
	}
	if ownerUserID == 0 || reaction.ViewerID == 0 || reaction.Reaction == nil {
		return domain.UpdateEvent{}, domain.UpdateState{}, domain.ErrStoryPeerInvalid
	}
	return s.recordEventCore(ctx, stateAuthKeyID, excludeAuthKeyID, ownerUserID, domain.UpdateEvent{
		Type:     domain.UpdateEventNewStoryReaction,
		Date:     reaction.Date,
		Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: reaction.ViewerID},
		MaxID:    reaction.StoryID,
		Story:    reaction.Story,
		Reaction: reaction.Reaction,
		PtsCount: 1,
	}, true, excludeSessionID, false)
}

// RecordQuickReplyMutation records account-local quick reply state changes for
// multi-device sync. Quick-reply TL updates do not carry pts, so outbox appends
// auxiliary pts bookkeeping just like other account settings events.
func (s *Service) RecordQuickReplyMutation(ctx context.Context, stateAuthKeyID [8]byte, userID int64, mutation domain.QuickReplyMutation, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 {
		userID = mutation.List.OwnerUserID
	}
	event := domain.UpdateEvent{
		Date:              mutation.Date,
		PtsCount:          1,
		QuickReplies:      append([]domain.QuickReply(nil), mutation.List.QuickReplies...),
		QuickReply:        mutation.QuickReply,
		QuickReplyMessage: mutation.Message,
		MessageIDs:        append([]int(nil), mutation.MessageIDs...),
		MaxID:             mutation.ShortcutID,
	}
	switch mutation.Kind {
	case domain.QuickReplyMutationNew:
		event.Type = domain.UpdateEventNewQuickReply
	case domain.QuickReplyMutationDelete:
		event.Type = domain.UpdateEventDeleteQuickReply
	case domain.QuickReplyMutationMessage:
		event.Type = domain.UpdateEventQuickReplyMessage
	case domain.QuickReplyMutationIDs:
		event.Type = domain.UpdateEventDeleteQuickReplyMessages
	default:
		event.Type = domain.UpdateEventQuickReplies
	}
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, event, true, excludeSessionID)
}

// RecordReadHistory 推进 update 状态并追加一条 read_history_inbox 事件。
func (s *Service) RecordReadHistory(ctx context.Context, stateAuthKeyID [8]byte, userID int64, read domain.ReadHistoryResult, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	if userID == 0 {
		userID = read.OwnerUserID
	}
	date := int(time.Now().Unix())
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:             domain.UpdateEventReadHistoryInbox,
		Date:             date,
		Peer:             read.Peer,
		MaxID:            read.MaxID,
		StillUnreadCount: read.StillUnreadCount,
		ChannelPts:       read.ChannelPts,
		PtsCount:         1,
	}, true, excludeSessionID)
}

// RecordChannelState 记录当前账号与某频道成员关系变化（leave/kick），
// 离线设备经 difference 收到 updateChannel 后重拉 channel 状态。
func (s *Service) RecordChannelState(ctx context.Context, stateAuthKeyID [8]byte, userID, channelID int64, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventChannelState,
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordContactsReset 记录通讯录视角变化，供离线设备通过 updates.getDifference 触发重拉。
func (s *Service) RecordContactsReset(ctx context.Context, stateAuthKeyID [8]byte, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventContactsReset,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordUserEmojiStatus durably synchronizes an absolute emoji-status snapshot
// to the account's other sessions and offline difference stream.
func (s *Service) RecordUserEmojiStatus(ctx context.Context, stateAuthKeyID [8]byte, userID int64, status domain.UserEmojiStatus, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	if !status.Valid() {
		return domain.UpdateEvent{}, domain.UpdateState{}, domain.ErrStarGiftCollectibleInvalid
	}
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:        domain.UpdateEventUserEmojiStatus,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: userID},
		EmojiStatus: status,
		PtsCount:    1,
	}, true, excludeSessionID)
}

// RecordPrivacy durably synchronizes the exact immutable privacy snapshot to
// the account's other sessions and offline difference stream.
func (s *Service) RecordPrivacy(ctx context.Context, stateAuthKeyID [8]byte, userID int64, rules domain.PrivacyRules, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	if rules.OwnerUserID != userID || rules.Key == "" || len(rules.Rules) == 0 {
		return domain.UpdateEvent{}, domain.UpdateState{}, domain.ErrPrivacyRuleInvalid
	}
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventPrivacy,
		Privacy:  rules,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordDraftMessage 记录某会话云草稿变化（保存/清空都是同一事件——草稿是绝对
// 状态，重放时按 peer 重载当前值）。updateDraftMessage 无 pts 字段，走 LacksWirePts
// aux 簿记；topMsgID 是 forum 话题草稿键（复用 MaxID 列持久化）。
func (s *Service) RecordDraftMessage(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, topMsgID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventDraftMessage,
		Peer:     peer,
		MaxID:    topMsgID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordDialogPinned 记录单个会话置顶状态变化；folderID 是会话所在 folder
// （0 主列表/1 归档），缺失会让离线设备把归档内置顶重放到主列表。
func (s *Service) RecordDialogPinned(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, folderID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventDialogPinned,
		Peer:     peer,
		Bool:     pinned,
		FolderID: folderID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordPinnedDialogs 记录指定 folder 内置顶顺序变化，并把新顺序持久化给 getDifference/outbox。
func (s *Service) RecordPinnedDialogs(ctx context.Context, stateAuthKeyID [8]byte, userID int64, folderID int, order []domain.Peer, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventPinnedDialogs,
		Peers:    append([]domain.Peer(nil), order...),
		FolderID: folderID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordSavedDialogPinned 记录收藏夹单个子会话置顶状态变化。
func (s *Service) RecordSavedDialogPinned(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventSavedDialogPinned,
		Peer:     peer,
		Bool:     pinned,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordPinnedSavedDialogs 记录收藏夹置顶顺序变化，新顺序持久化给 getDifference/outbox。
func (s *Service) RecordPinnedSavedDialogs(ctx context.Context, stateAuthKeyID [8]byte, userID int64, order []domain.Peer, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventPinnedSavedDialogs,
		Peers:    append([]domain.Peer(nil), order...),
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordDialogUnreadMark 记录手动未读标记变化。
func (s *Service) RecordDialogUnreadMark(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, unread bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventDialogUnreadMark,
		Peer:     peer,
		Bool:     unread,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordChannelViewForumAsMessages records a per-account forum presentation state change.
func (s *Service) RecordChannelViewForumAsMessages(ctx context.Context, stateAuthKeyID [8]byte, userID, channelID int64, enabled bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventChannelViewForum,
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		Bool:     enabled,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordChannelDiscussionInbox 记录 forum 话题级已读（updateReadChannelDiscussionInbox），
// 占一个账号 pts（LacksWirePts），供自己其它设备在线同步与离线差分恢复。
func (s *Service) RecordChannelDiscussionInbox(ctx context.Context, stateAuthKeyID [8]byte, userID, channelID int64, topicID, maxID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventReadChannelDiscussionInbox,
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		TopMsgID: topicID,
		MaxID:    maxID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordPeerSettings 记录 peer settings 变化。
func (s *Service) RecordPeerSettings(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, settings domain.PeerSettings, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventPeerSettings,
		Peer:     peer,
		Settings: settings,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordPeerStoryBlocked 记录当前账号 story blocklist 对某个 peer 的可见状态变化。
func (s *Service) RecordPeerStoryBlocked(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, blocked bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventPeerStoryBlocked,
		Peer:     peer,
		Bool:     blocked,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordDialogFilter 记录单个 filter 的创建、更新或删除；folder 为 nil 表示删除。
func (s *Service) RecordDialogFilter(ctx context.Context, stateAuthKeyID [8]byte, userID int64, folderID int, folder *domain.DialogFolder, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	var copyFolder *domain.DialogFolder
	if folder != nil {
		f := *folder
		copyFolder = &f
	}
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:         domain.UpdateEventDialogFilter,
		FilterID:     folderID,
		DialogFilter: copyFolder,
		PtsCount:     1,
	}, true, excludeSessionID)
}

// RecordDialogFilterOrder 记录 filter 顺序变化。
func (s *Service) RecordDialogFilterOrder(ctx context.Context, stateAuthKeyID [8]byte, userID int64, order []int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:        domain.UpdateEventDialogFilterOrder,
		FilterOrder: append([]int(nil), order...),
		PtsCount:    1,
	}, true, excludeSessionID)
}

// RecordDialogFiltersReload 通知其他设备重新拉取 filter 列表。
func (s *Service) RecordDialogFiltersReload(ctx context.Context, stateAuthKeyID [8]byte, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventDialogFilters,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordFolderPeers 记录归档/还原会话的 folder_id 变化。
func (s *Service) RecordFolderPeers(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peers []domain.FolderPeerUpdate, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:        domain.UpdateEventFolderPeers,
		FolderPeers: append([]domain.FolderPeerUpdate(nil), peers...),
		PtsCount:    1,
	}, true, excludeSessionID)
}

// RecordChannelAvailableMessages records a local channel history clear for multi-device sync.
func (s *Service) RecordChannelAvailableMessages(ctx context.Context, stateAuthKeyID [8]byte, userID, channelID int64, availableMinID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventChannelAvailable,
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		MaxID:    availableMinID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

func (s *Service) recordEvent(ctx context.Context, stateAuthKeyID, excludeAuthKeyID [8]byte, userID int64, event domain.UpdateEvent, dispatch bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEventCore(ctx, stateAuthKeyID, excludeAuthKeyID, userID, event, dispatch, excludeSessionID, true)
}

func (s *Service) recordEventWithoutState(ctx context.Context, userID int64, event domain.UpdateEvent) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEventCore(ctx, [8]byte{}, [8]byte{}, userID, event, false, 0, false)
}

func (s *Service) recordEventCore(ctx context.Context, stateAuthKeyID, excludeAuthKeyID [8]byte, userID int64, event domain.UpdateEvent, dispatch bool, excludeSessionID int64, saveState bool) (domain.UpdateEvent, domain.UpdateState, error) {
	date := event.Date
	if date == 0 {
		date = int(time.Now().Unix())
	}
	if event.PtsCount == 0 {
		event.PtsCount = 1
	}
	event.UserID = userID
	event.Date = date
	st := domain.UpdateState{Date: date, Seq: 0}
	if s.events != nil {
		var err error
		if dispatch {
			if appender, ok := s.events.(dispatchingEventAppender); ok {
				event, err = appender.AppendAllocatedWithDispatch(ctx, userID, event, excludeAuthKeyID, excludeSessionID)
			} else {
				event, err = s.events.AppendAllocated(ctx, userID, event)
			}
		} else {
			event, err = s.events.AppendAllocated(ctx, userID, event)
		}
		if err != nil {
			s.log.Warn("update_event_append_failed",
				zap.String("scope", "user"),
				zap.Int64("user_id", userID),
				zap.Int("pts", event.Pts),
				zap.Int("pts_count", event.PtsCount),
				zap.String("event_type", string(event.Type)),
				zap.Error(err),
				zap.Error(ctx.Err()),
			)
			return domain.UpdateEvent{}, domain.UpdateState{}, err
		}
		st.Pts = event.Pts
		s.log.Debug("update_event_appended",
			zap.String("scope", "user"),
			zap.Int64("user_id", userID),
			zap.Int("pts", event.Pts),
			zap.Int("pts_count", event.PtsCount),
			zap.String("event_type", string(event.Type)),
			zap.Bool("dispatch", dispatch),
		)
	} else {
		current, err := s.currentPts(ctx, userID)
		if err != nil {
			return domain.UpdateEvent{}, domain.UpdateState{}, err
		}
		event.Pts = current + event.PtsCount
		st.Pts = event.Pts
	}
	if saveState && s.states != nil {
		if err := s.states.Save(ctx, stateAuthKeyID, userID, st); err != nil {
			return domain.UpdateEvent{}, domain.UpdateState{}, err
		}
	}
	return event, st, nil
}

// currentPts 供 GetState 报告「当前 pts」。对齐 MTProto：报告最大连续已提交 pts，
// PG 实现中该值由同一事务内的 pts 分配 + durable event 写入共同维护。
func (s *Service) currentPts(ctx context.Context, userID int64) (int, error) {
	if s.events != nil {
		return s.events.MaxContiguousPts(ctx, userID)
	}
	return 0, nil
}
