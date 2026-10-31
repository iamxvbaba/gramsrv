package store

import (
	"context"

	"telesrv/internal/domain"
)

// GroupCallStore persists the authoritative group call signaling state (dual memory/postgres
// implementations; the behavior contract is pinned by a shared contract test; see the
// domain/groupcall.go comment for version monotonicity).
type GroupCallStore interface {
	// CreateGroupCall creates a call; if the channel already has an active call it returns domain.ErrGroupCallAlreadyStarted.
	CreateGroupCall(ctx context.Context, call domain.GroupCall) (domain.GroupCall, error)
	// CreateConferenceCall creates an ad-hoc conference; the same creator+random_id idempotently returns the existing active call.
	CreateConferenceCall(ctx context.Context, call domain.GroupCall) (domain.GroupCall, error)
	GetGroupCall(ctx context.Context, callID int64) (domain.GroupCall, bool, error)
	GetGroupCallBySlug(ctx context.Context, slug string) (domain.GroupCall, bool, error)
	GetGroupCallByInviteMessage(ctx context.Context, userID int64, msgID int) (domain.GroupCall, domain.GroupCallInvite, bool, error)
	// JoinGroupCall joins/rejoins (upsert on the same primary key with a new ssrc); if the ssrc
	// collides with another active one under the uniqueness constraint it returns
	// domain.ErrGroupCallSSRCDuplicate; version++.
	JoinGroupCall(ctx context.Context, req domain.JoinGroupCallRequest) (domain.GroupCallMutation, error)
	// LeaveGroupCall sets left+version++; a conference transitions to discarded when its last
	// active participant leaves, while a regular channel group call may stay active with an
	// empty room; returns domain.ErrGroupCallNotJoined when not in the call.
	LeaveGroupCall(ctx context.Context, callID, userID int64, now int) (domain.GroupCallMutation, error)
	// RemoveConferenceCallParticipants accepts a conference E2E remove block, clears the
	// targets' E2E member markers, and sets active participants to left on kick, all within
	// one transaction.
	RemoveConferenceCallParticipants(ctx context.Context, req domain.RemoveConferenceCallParticipantsRequest) (domain.RemoveConferenceCallParticipantsResult, error)
	// DiscardGroupCall terminates the call and clears participants, returning the final call and the previously active participants.
	DiscardGroupCall(ctx context.Context, callID int64, now int) (domain.GroupCall, []domain.GroupCallParticipant, error)
	// TouchParticipant refreshes the checkGroupCall keepalive watermark and returns the user's
	// current active ssrc set (joined=false means not in the call, and the client auto-rejoins
	// based on the empty set).
	TouchParticipant(ctx context.Context, callID, userID int64, now int) (activeSSRCs []int64, joined bool, err error)
	GetParticipant(ctx context.Context, callID, userID int64) (domain.GroupCallParticipant, bool, error)
	// ListParticipants paginates by cursor over (join_date, user_id); offset is the
	// NextOffset returned last time (empty=from the start). The response carries the current
	// version (the client's full reload on a skipped number depends on it).
	ListParticipants(ctx context.Context, callID int64, offset string, limit int) (domain.GroupCallParticipantPage, error)
	// UpdateParticipant applies field-level updates; changed=false means no effective change (version unchanged).
	UpdateParticipant(ctx context.Context, callID, userID int64, update domain.GroupCallParticipantUpdate) (domain.GroupCallMutation, bool, error)
	// SetGroupCallTitle / SetGroupCallJoinMuted only touch the call row, not the participant version.
	SetGroupCallTitle(ctx context.Context, callID int64, title string) (domain.GroupCall, bool, error)
	SetGroupCallJoinMuted(ctx context.Context, callID int64, joinMuted bool) (domain.GroupCall, bool, error)
	SetStartedMessageID(ctx context.Context, callID int64, msgID int) error
	// SweepStaleParticipants clears active participants whose keepalive watermark is older
	// than checkOlderThan (each removed participant increments version). Note the caller must
	// also combine SFU media-plane liveness for a dual-expiry decision.
	SweepStaleParticipants(ctx context.Context, checkOlderThan, now int, limit int) ([]domain.GroupCallMutation, error)
	// ResetAllParticipants recovers from a server restart: it bulk-sets all active calls'
	// participants to left (each call increments version), synchronously transitions a
	// conference to discarded if it thereby becomes empty, and returns the affected calls.
	ResetAllParticipants(ctx context.Context, now int) ([]domain.GroupCall, error)
	// NextRaiseHandRating allocates a globally monotonic raise-hand sequence number (for raise-hand ordering).
	NextRaiseHandRating(ctx context.Context, callID int64) (int64, error)
	// SetParticipantOverride writes the setter's per-viewer override for target (local
	// mute/volume), affecting only the setter's own view. clear=true deletes the override.
	SetParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64, override domain.GroupCallParticipantOverride, clear bool) error
	// GetParticipantOverride fetches a setter's override for a target.
	GetParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64) (domain.GroupCallParticipantOverride, bool, error)
	// CreateConferenceInvite records a conference private-message invite and its message box id.
	CreateConferenceInvite(ctx context.Context, invite domain.GroupCallInvite) (domain.GroupCallInvite, error)
	SetConferenceInviteStatus(ctx context.Context, callID int64, inviteeUserID int64, msgID int, status domain.GroupCallInviteStatus, now int) (domain.GroupCallInvite, bool, error)
	// ListConferenceRecipientUserIDs returns the candidate recipients for a conference's online
	// push/access. The active state includes only the creator, current active participants, and
	// people involved in pending/accepted invites; the discarded state includes all historical
	// participants and invite-related people, letting the client poll for the final state at the end.
	ListConferenceRecipientUserIDs(ctx context.Context, callID int64) ([]int64, error)
	AppendGroupCallChainBlock(ctx context.Context, block domain.GroupCallChainBlock) (domain.GroupCallChainBlock, error)
	ListGroupCallChainBlocks(ctx context.Context, callID int64, subChainID, offset, limit int) (domain.GroupCallChainBlockPage, error)
	// GetRtmpStreamKey / SetRtmpStreamKey maintain the per-channel persistent RTMP stream key.
	// The revoke semantics are implemented by the caller: Set overwrites the old key, and
	// streaming with the old key fails immediately.
	GetRtmpStreamKey(ctx context.Context, channelID int64) (string, bool, error)
	SetRtmpStreamKey(ctx context.Context, channelID int64, key string, now int) error
	// StartScheduledGroupCall zeroes schedule_date (a scheduled call formally begins).
	// changed=false means it had already begun (idempotent); discarded returns ErrGroupCallDiscarded.
	StartScheduledGroupCall(ctx context.Context, callID int64) (domain.GroupCall, bool, error)
	// SetScheduleStartSubscription writes/clears userID's start-notification subscription.
	SetScheduleStartSubscription(ctx context.Context, callID, userID int64, subscribed bool) error
	// ListScheduleSubscriberIDs returns the userIDs subscribed to start notifications (ascending).
	ListScheduleSubscriberIDs(ctx context.Context, callID int64) ([]int64, error)
}
