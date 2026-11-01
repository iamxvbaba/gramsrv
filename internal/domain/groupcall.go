package domain

import "errors"

// Group call (supergroup voice chat) domain model. The signaling truth lives in GroupCallStore (memory/postgres
// dual implementation), and the version protocol requires version to be persisted: the client ignores an
// updateGroupCallParticipants with a version lower than its local cache, so a rollback on restart would silently disconnect the whole room.

// GroupCallState is the group call state.
type GroupCallState string

const (
	GroupCallStateActive    GroupCallState = "active"
	GroupCallStateDiscarded GroupCallState = "discarded"
)

// GroupCallKind distinguishes regular channel group calls from ad-hoc
// conference calls. Conference calls have no channel membership scope; access is
// granted by creator/participant/invite/slug.
type GroupCallKind string

const (
	GroupCallKindChannel    GroupCallKind = "channel"
	GroupCallKindConference GroupCallKind = "conference"
)

// GroupCallInviteStatus is the durable state of a private conference invite.
type GroupCallInviteStatus string

const (
	GroupCallInvitePending  GroupCallInviteStatus = "pending"
	GroupCallInviteAccepted GroupCallInviteStatus = "accepted"
	GroupCallInviteDeclined GroupCallInviteStatus = "declined"
	GroupCallInviteMissed   GroupCallInviteStatus = "missed"
	GroupCallInviteRevoked  GroupCallInviteStatus = "revoked"
)

// Group call business errors; the rpc layer maps them to GROUPCALL_* RPC_ERROR.
var (
	ErrGroupCallInvalid        = errors.New("group call invalid")
	ErrGroupCallDiscarded      = errors.New("group call already discarded")
	ErrGroupCallAlreadyStarted = errors.New("group call already started")
	ErrGroupCallSSRCDuplicate  = errors.New("group call ssrc duplicate")
	ErrGroupCallNotJoined      = errors.New("group call participant missing")
	ErrConferenceChainInvalid  = errors.New("conference call chain invalid")
)

// GroupCall is the authoritative state of one group call.
type GroupCall struct {
	ID            int64
	AccessHash    int64
	ChannelID     int64
	CreatorUserID int64
	Kind          GroupCallKind
	State         GroupCallState
	Title         string
	JoinMuted     bool
	// RtmpStream=true means this is an RTMP live room (ingest goes through external RTMP, viewers pull
	// via broadcast); join creates no SFU connection and connection params return stream JSON.
	RtmpStream bool
	// ScheduleDate>0 means the scheduled call has not started (the client shows a countdown panel and does not join);
	// after startScheduledGroupCall clears it, the client automatically initialJoins. Persisted column.
	ScheduleDate int
	// ScheduleStartSubscribed is a per-viewer projection field (not a persisted column): whether the current viewer
	// subscribed to the start reminder. Filled per viewer before RPC/push egress (same kind of semantics as the Creator flag).
	ScheduleStartSubscribed bool
	// Version is the participant protocol version: +1 within the transaction on every participant change, monotonic and persisted.
	Version           int
	ParticipantsCount int
	CreatedAt         int
	DiscardedAt       int
	Duration          int
	// StartedMsgID is the channel message id of messageActionGroupCall(started) (on discard the
	// client uses it to locate the starting service message; currently only recorded).
	StartedMsgID int
	// InviteSlug/InviteLink are used only by conference. The slug must be stably parseable in the migrate reason and
	// InputGroupCallSlug; the link is the full URL used by the client UI for copy/share.
	InviteSlug string
	InviteLink string
	// RandomID is the idempotency key of a conference create (creator_user_id + random_id).
	RandomID int64
	// MigratedFromPhoneCallID records which P2P call it was upgraded from; 0 for a link-only create.
	MigratedFromPhoneCallID int64
}

// Active reports whether the call is still ongoing.
func (c GroupCall) Active() bool {
	return c.State == GroupCallStateActive
}

func (c GroupCall) Conference() bool {
	return c.Kind == GroupCallKindConference
}

// GroupCallParticipant is one participant in the room.
type GroupCallParticipant struct {
	CallID int64
	UserID int64
	// A non-zero JoinAsChannelID means joining as that channel/group identity (anonymous-admin semantics), with TL output
	// participant.peer=PeerChannel; 0 = join as the user's own identity. The identity's unique key is still UserID,
	// so rejoining with a different identity is a replacement.
	JoinAsChannelID int64
	// SSRC is the audio ssrc the client self-reports in the join JSON (uint32 range, stored as int64 to avoid sign pitfalls).
	SSRC       int64
	JoinDate   int
	ActiveDate int
	Muted      bool
	// When MutedByAdmin=true, can_self_unmute=false (admin mute/default-mute policy).
	MutedByAdmin bool
	// VolumeByAdmin is the global volume set by an admin (1..20000); 0=unset.
	VolumeByAdmin int
	// A non-zero RaiseHandRating means the hand is raised; the value increases monotonically for ordering.
	RaiseHandRating int64
	// VideoJSON / PresentationJSON are the raw JSON snapshots of tg.GroupCallParticipantVideo
	// (enabled in M3/M4; in M0/M1 they only transparently store self-edits and are not forwarded).
	VideoJSON        []byte
	PresentationJSON []byte
	// PublicKey/JoinBlock are used only by conference. The server does not parse E2E content; it only persists
	// opaque bytes for chain block backfill/forwarding.
	PublicKey []byte
	JoinBlock []byte
	Left      bool
	// LastCheckDate is the checkGroupCall keepalive watermark. Note: the client sends checkGroupCall
	// only in the Connecting state (the heartbeat stops once media is connected), so a disconnect decision must
	// combine this with SFU media-plane liveness for a double expiry (see sweeper); never rely on this field alone.
	LastCheckDate int
}

// CreateGroupCallRequest creates a group call.
type CreateGroupCallRequest struct {
	ChannelID     int64
	CreatorUserID int64
	RandomID      int64
	Title         string
	Now           int
}

// JoinGroupCallRequest joins/rejoins a group call (rejoin keeps the same key but takes a new ssrc).
type JoinGroupCallRequest struct {
	CallID int64
	UserID int64
	// Non-zero JoinAsChannelID = join as a channel identity (the caller has already checked permissions).
	JoinAsChannelID int64
	SSRC            int64
	Muted           bool
	IsAdmin         bool
	PublicKey       []byte
	JoinBlock       []byte
	// VideoJSON is the video internal state minted by this join (endpoint+source group+active); a rejoin
	// replaces it wholesale and **clears the old PresentationJSON** (after the client's main connection rejoins it re-sends
	// joinGroupCallPresentation, so the old screen registration must be invalidated).
	VideoJSON []byte
	Now       int
}

// GroupCallMutation is the result of one participant-dimension change: the updated call row (with the new version)
// and the affected participant row (for pushing updateGroupCallParticipants).
type GroupCallMutation struct {
	Call        GroupCall
	Participant GroupCallParticipant
}

// RemoveConferenceCallParticipantsRequest describes a conference participant
// removal from both the media participant set and the E2E member chain.
type RemoveConferenceCallParticipantsRequest struct {
	CallID        int64
	AuthorUserID  int64
	TargetUserIDs []int64
	OnlyLeft      bool
	Kick          bool
	Block         []byte
	Now           int
}

// RemoveConferenceCallParticipantsResult is the transactional result of
// accepting a conference E2E removal block and/or kicking active participants.
type RemoveConferenceCallParticipantsResult struct {
	Call                GroupCall
	ParticipantsChanged []GroupCallParticipant
	ChainBlock          GroupCallChainBlock
	ChainBlockAppended  bool
}

// GroupCallParticipantUpdate is a field-level update of editGroupCallParticipant (nil=unchanged).
type GroupCallParticipantUpdate struct {
	Muted            *bool
	MutedByAdmin     *bool
	VolumeByAdmin    *int
	RaiseHandRating  *int64
	VideoJSON        *[]byte
	PresentationJSON *[]byte
	Now              int
}

// GroupCallParticipantOverride is a per-viewer view override (setter→target):
// local mute/local volume, visible only to the setter, not entering the room-wide version.
type GroupCallParticipantOverride struct {
	MutedByYou bool
	Volume     int // 0=unset
}

// GroupCallParticipantPage is the paginated result of getGroupParticipants.
type GroupCallParticipantPage struct {
	Count        int
	Participants []GroupCallParticipant
	NextOffset   string
	Version      int
}

// GroupCallInvite is the index of an invite service message a conference call sent in a private chat.
type GroupCallInvite struct {
	CallID        int64
	InviterUserID int64
	InviteeUserID int64
	MessageID     int
	Status        GroupCallInviteStatus
	Video         bool
	CreatedAt     int
	UpdatedAt     int
}

// GroupCallChainBlockLatestOffset is the sentinel value clients use to request the current sub-chain's latest block.
const GroupCallChainBlockLatestOffset = -1

// GroupCallChainBlock is an opaque block of the conference E2E chain.
type GroupCallChainBlock struct {
	CallID       int64
	SubChainID   int
	Offset       int
	AuthorUserID int64
	Block        []byte
	CreatedAt    int
}

type GroupCallChainBlockPage struct {
	Blocks     []GroupCallChainBlock
	NextOffset int
}
