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

// Service provides update state queries.
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

// ServiceOption adjusts the updates service's runtime dependencies.
type ServiceOption func(*Service)

// WithLogger injects the update state machine logger, used to trace pts allocation, append failures, and difference gaps.
func WithLogger(log *zap.Logger) ServiceOption {
	return func(s *Service) {
		s.log = log
	}
}

// NewService creates the updates service.
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

// UsesReliableDispatch reports that settings-class updates are written to the transactional outbox and delivered to online sessions by the outbox worker.
func (s *Service) UsesReliableDispatch() bool {
	if s == nil || s.events == nil {
		return false
	}
	_, ok := s.events.(dispatchingEventAppender)
	return ok
}

// GetState returns the confirmed update state for the current auth_key + user scope.
// user_update_events is an account-level durable log; the auth_key scope only stores
// the state the device has confirmed via getDifference, so getState must not advance it
// directly to the account's latest watermark.
func (s *Service) GetState(ctx context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, error) {
	now := int(time.Now().Unix())
	// The private-chat phase maintains no account-level seq: UpdateState.Seq is always 0
	// externally, the client syncs purely via pts and skips seq gap detection (the push
	// envelope seq is likewise always 0).
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

// CurrentState returns the account's current maximum contiguous update state without modifying any device's confirmed watermark.
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

// ConfirmEvent marks an event already atomically committed in another business
// transaction as consumed by the current device. The typical caller is
// account.changePhone: the RPC result already carries the latest User, so the current
// device should not receive updateUserPhone again, but its confirmed watermark still
// needs to advance to that event's pts.
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

// getDifferenceLimit is the maximum number of contiguous events a single getDifference returns; exceeding it sets Partial so the client paginates.
const getDifferenceLimit = 100

// GetDifference returns the incremental events for the current user after the from state.
//
// Aligning with MTProto: it returns only the "contiguous" events starting at from.Pts
// (truncating at a hole), with State.Pts taking the last contiguous value, never letting a
// client skip an anomalous hole and lose messages; the normal write path advances pts and
// the durable event within a PG transaction. When contiguous events fill the limit it sets
// Partial (mapped to differenceSlice), and the client uses the returned State to keep paginating.
func (s *Service) GetDifference(ctx context.Context, authKeyID [8]byte, userID int64, from domain.UpdateState) (domain.UpdateDifference, error) {
	st, err := s.currentState(ctx, userID)
	if err != nil {
		return domain.UpdateDifference{}, err
	}
	// Computation is pure with respect to device confirmed/observed state. The
	// request observer and physical-delivery commit are explicit caller phases.
	from = clampDifferenceState(from, st)
	// TDesktop does not support account-level updates.differenceTooLong. Retention can only
	// delete the common prefix all authorized devices have confirmed; if the current device
	// still carries an older pts, an empty ordinary differenceSlice advances IntermediateState
	// to the confirmed checkpoint, then it continues from the live tail.
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
	out.Seq = 0 // seq is always 0, see GetState comment
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

// contiguousPrefix returns the prefix of events whose pts is strictly contiguous from
// from (from+1, from+2, ...). It first sorts by ascending pts to tolerate the store's
// return order, and stops at a hole.
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

// ClearAuthKey clears the device state for an auth_key.
// user_update_events is an account-level durable log and must not be deleted because a device logged out.
func (s *Service) ClearAuthKey(ctx context.Context, authKeyID [8]byte) error {
	if s.states != nil {
		if err := s.states.DeleteAuthKey(ctx, authKeyID); err != nil {
			return err
		}
	}
	return nil
}

// RecordNewMessage advances the update state and appends a new_message event.
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

// RecordMessagePoll records a durable marker for message poll state changes
// (vote / close). updateMessagePoll has no pts fields in Layer 225 — same
// historical bookkeeping shape pending its own audit.
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

// RecordReadHistory advances the update state and appends a read_history_inbox event.
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

// RecordChannelState records a change in the current account's channel membership
// (leave/kick); offline devices receive updateChannel via difference and re-fetch the
// channel state.
func (s *Service) RecordChannelState(ctx context.Context, stateAuthKeyID [8]byte, userID, channelID int64, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventChannelState,
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordContactsReset records a change in the contact-list view so offline devices re-fetch it via updates.getDifference.
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

// RecordDraftMessage records a dialog's cloud draft change (save/clear are the same
// event — the draft is absolute state, reloaded by peer on replay). updateDraftMessage
// has no pts field, so it uses LacksWirePts aux bookkeeping; topMsgID is the forum topic
// draft key (persisted by reusing the MaxID column).
func (s *Service) RecordDraftMessage(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, topMsgID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventDraftMessage,
		Peer:     peer,
		MaxID:    topMsgID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordDialogPinned records a single dialog's pin state change; folderID is the folder
// the dialog lives in (0 main list/1 archived), and omitting it would make offline devices
// replay an archived pin into the main list.
func (s *Service) RecordDialogPinned(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, folderID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventDialogPinned,
		Peer:     peer,
		Bool:     pinned,
		FolderID: folderID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordPinnedDialogs records a pin-order change within the given folder and persists the new order for getDifference/outbox.
func (s *Service) RecordPinnedDialogs(ctx context.Context, stateAuthKeyID [8]byte, userID int64, folderID int, order []domain.Peer, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventPinnedDialogs,
		Peers:    append([]domain.Peer(nil), order...),
		FolderID: folderID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordSavedDialogPinned records a single saved-dialog child's pin state change.
func (s *Service) RecordSavedDialogPinned(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventSavedDialogPinned,
		Peer:     peer,
		Bool:     pinned,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordPinnedSavedDialogs records a saved-dialogs pin-order change, persisting the new order for getDifference/outbox.
func (s *Service) RecordPinnedSavedDialogs(ctx context.Context, stateAuthKeyID [8]byte, userID int64, order []domain.Peer, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventPinnedSavedDialogs,
		Peers:    append([]domain.Peer(nil), order...),
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordDialogUnreadMark records a manual unread mark change.
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

// RecordChannelDiscussionInbox records a forum topic-level read
// (updateReadChannelDiscussionInbox), spending one account pts (LacksWirePts), for the
// account's other devices to sync online and recover offline via difference.
func (s *Service) RecordChannelDiscussionInbox(ctx context.Context, stateAuthKeyID [8]byte, userID, channelID int64, topicID, maxID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventReadChannelDiscussionInbox,
		Peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		TopMsgID: topicID,
		MaxID:    maxID,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordPeerSettings records a peer settings change.
func (s *Service) RecordPeerSettings(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, settings domain.PeerSettings, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventPeerSettings,
		Peer:     peer,
		Settings: settings,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordPeerStoryBlocked records a change in the current account's story blocklist visibility for a peer.
func (s *Service) RecordPeerStoryBlocked(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, blocked bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventPeerStoryBlocked,
		Peer:     peer,
		Bool:     blocked,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordDialogFilter records a single filter's creation, update or deletion; folder nil means deletion.
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

// RecordDialogFilterOrder records a filter order change.
func (s *Service) RecordDialogFilterOrder(ctx context.Context, stateAuthKeyID [8]byte, userID int64, order []int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:        domain.UpdateEventDialogFilterOrder,
		FilterOrder: append([]int(nil), order...),
		PtsCount:    1,
	}, true, excludeSessionID)
}

// RecordDialogFiltersReload notifies other devices to re-fetch the filter list.
func (s *Service) RecordDialogFiltersReload(ctx context.Context, stateAuthKeyID [8]byte, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:     domain.UpdateEventDialogFilters,
		PtsCount: 1,
	}, true, excludeSessionID)
}

// RecordFolderPeers records the folder_id change of archived/restored dialogs.
func (s *Service) RecordFolderPeers(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peers []domain.FolderPeerUpdate, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error) {
	return s.recordEvent(ctx, stateAuthKeyID, excludeAuthKeyID, userID, domain.UpdateEvent{
		Type:        domain.UpdateEventFolderPeers,
		FolderPeers: append([]domain.FolderPeerUpdate(nil), peers...),
		PtsCount:    1,
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

// currentPts supplies the "current pts" reported by GetState. Aligning with MTProto: it
// reports the maximum contiguous committed pts; in the PG implementation that value is
// maintained jointly by pts allocation + durable event write within the same transaction.
func (s *Service) currentPts(ctx context.Context, userID int64) (int, error) {
	if s.events != nil {
		return s.events.MaxContiguousPts(ctx, userID)
	}
	return 0, nil
}
