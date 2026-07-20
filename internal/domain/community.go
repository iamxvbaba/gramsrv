package domain

import "errors"

const (
	MaxCommunityPeers        = 100
	MaxCommunityBotPeers     = 100
	MaxCommunityLinkRequests = 100
	MaxCommunityTitleRunes   = 128
	MaxCommunityAboutRunes   = 255
	MaxCommunityParticipants = 200
)

var (
	ErrCommunityInvalid            = errors.New("community invalid")
	ErrCommunityPrivate            = errors.New("community private")
	ErrCommunityAdminRequired      = errors.New("community admin required")
	ErrCommunityCreatorRequired    = errors.New("community creator required")
	ErrCommunityPeerInvalid        = errors.New("community peer invalid")
	ErrCommunityPeerLinked         = errors.New("community peer already linked")
	ErrCommunityPeersTooMuch       = errors.New("community peers too much")
	ErrCommunityRequestCreated     = errors.New("community request created")
	ErrCommunityRequestMissing     = errors.New("community request missing")
	ErrCommunityParticipantInvalid = errors.New("community participant invalid")
)

// Community is the Layer 228 aggregation container. It intentionally has no
// message/read/pts fields: linked dialogs remain the only message truth.
type Community struct {
	ID                  int64
	AccessHash          int64
	CreatorUserID       int64
	Title               string
	About               string
	Date                int
	Deleted             bool
	DefaultBannedRights ChannelBannedRights
	PhotoID             int64
	PhotoDCID           int
	PhotoStripped       []byte
}

type CommunityMemberRole string

const (
	CommunityRoleCreator CommunityMemberRole = "creator"
	CommunityRoleAdmin   CommunityMemberRole = "admin"
	CommunityRoleMember  CommunityMemberRole = "member"
)

type CommunityMemberStatus string

const (
	CommunityMemberActive CommunityMemberStatus = "active"
	CommunityMemberKicked CommunityMemberStatus = "kicked"
)

type CommunityMember struct {
	CommunityID int64
	UserID      int64
	Role        CommunityMemberRole
	Status      CommunityMemberStatus
	AdminRights ChannelAdminRights
	Rank        string
	Date        int
}

func (m CommunityMember) Active() bool { return m.Status == CommunityMemberActive }

func (m CommunityMember) CanManageLinkedPeers() bool {
	return m.Active() && (m.Role == CommunityRoleCreator ||
		(m.Role == CommunityRoleAdmin && m.AdminRights.ManageLinkedPeers))
}

func (m CommunityMember) CanChangeInfo() bool {
	return m.Active() && (m.Role == CommunityRoleCreator ||
		(m.Role == CommunityRoleAdmin && m.AdminRights.ChangeInfo))
}

func (m CommunityMember) CanAddAdmins() bool {
	return m.Active() && (m.Role == CommunityRoleCreator ||
		(m.Role == CommunityRoleAdmin && m.AdminRights.AddAdmins))
}

func (m CommunityMember) CanBanUsers() bool {
	return m.Active() && (m.Role == CommunityRoleCreator ||
		(m.Role == CommunityRoleAdmin && m.AdminRights.BanUsers))
}

type CommunityPeerVisibility string

const (
	CommunityPeerVisible CommunityPeerVisibility = "visible"
	CommunityPeerHidden  CommunityPeerVisibility = "hidden"
)

type CommunityPeerLink struct {
	CommunityID    int64
	Peer           Peer
	Visibility     CommunityPeerVisibility
	CanViewHistory bool
	CreatedBy      int64
	Date           int
}

func (l CommunityPeerLink) Visible() bool { return l.Visibility == CommunityPeerVisible }

type CommunityPeerLinkRequest struct {
	CommunityID int64
	Peer        Peer
	RequestedBy int64
	Visibility  CommunityPeerVisibility
	Date        int
}

type CommunityUserState struct {
	CommunityID    int64
	UserID         int64
	Collapsed      bool
	Pinned         bool
	PinnedOrder    int
	NotifySettings *PeerNotifySettings
}

type CommunityView struct {
	Community       Community
	Self            CommunityMember
	State           CommunityUserState
	Links           []CommunityPeerLink
	Channels        []Channel
	Users           []User
	ServiceMessages []SendChannelMessageResult
	AdminsCount     int
	KickedCount     int
	PendingRequests int
	Forbidden       bool
}

func (v CommunityView) Creator() bool {
	return v.Self.Active() && v.Self.Role == CommunityRoleCreator
}

type CreateCommunityRequest struct {
	CreatorUserID int64
	Title         string
	About         string
	InitialPeer   Peer
	Visibility    CommunityPeerVisibility
	Date          int
}

type CommunityTogglePeerLinkRequest struct {
	ActorUserID int64
	CommunityID int64
	Peer        Peer
	Visibility  CommunityPeerVisibility
	Deleted     bool
	RequestOnly bool
	Date        int
}

type CommunityTogglePeerLinkResult struct {
	Community      Community
	Peer           Peer
	RequestedBy    int64
	Link           *CommunityPeerLink
	ServiceMessage *SendChannelMessageResult
	Removed        bool
	RequestCreated bool
}

type CommunityPeerLinkRequestPage struct {
	TotalCount int
	Requests   []CommunityPeerLinkRequest
	NextOffset string
	Channels   []Channel
	Users      []User
}

type CommunityParticipantJoinedChats struct {
	CreatorChatIDs []int64
	JoinedChatIDs  []int64
	Channels       []Channel
	Users          []User
}

type CommunityParticipantList struct {
	Community    Community
	Count        int
	Participants []CommunityMember
	Users        []User
	Hash         int64
}

type CommunityParticipantBanResult struct {
	Changed      bool
	ChannelBans  []EditChannelBannedResult
	RemovedLinks []CommunityTogglePeerLinkResult
}

type CommunityEditAdminRequest struct {
	ActorUserID int64
	CommunityID int64
	UserID      int64
	Rights      ChannelAdminRights
	Rank        string
	Date        int
}

type CommunitySearchScope struct {
	CommunityID int64
	ChannelIDs  []int64
	BotUserIDs  []int64
}
