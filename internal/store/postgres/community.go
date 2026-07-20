package postgres

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

type CommunityStore struct {
	db     sqlcgen.DBTX
	ids    store.ChannelIDAllocator
	msgIDs store.ChannelMessageIDAllocator
}

func NewCommunityStore(db sqlcgen.DBTX, ids store.ChannelIDAllocator, msgIDs store.ChannelMessageIDAllocator) *CommunityStore {
	if ids == nil {
		ids = pgChannelIDAllocator{db: db}
	}
	if msgIDs == nil {
		msgIDs = pgChannelMessageIDAllocator{db: db}
	}
	return &CommunityStore{db: db, ids: ids, msgIDs: msgIDs}
}

func (s *CommunityStore) appendCommunityServiceMessageTx(ctx context.Context, tx pgx.Tx, peer domain.Peer, actorUserID int64, date int, communityID int64) (*domain.SendChannelMessageResult, error) {
	if peer.Type != domain.PeerTypeChannel {
		return nil, nil
	}
	channel, err := getChannelByID(ctx, tx, peer.ID)
	if err != nil {
		return nil, err
	}
	channelStore := NewChannelStore(tx, WithChannelAllocators(s.ids, s.msgIDs))
	message, event, err := channelStore.insertServiceMessage(ctx, tx, channel, actorUserID, date, domain.ChannelMessageAction{
		Type:        domain.ChannelActionChangeCommunity,
		CommunityID: communityID,
	})
	if err != nil {
		return nil, err
	}
	channel.TopMessageID = message.ID
	channel.Pts = event.Pts
	// The Community transaction has already validated the actor and holds the
	// linked channel row. Use the internal membership scan here: the public
	// method treats viewerUserID=0 as an unauthenticated private-channel read.
	recipients, err := channelStore.listActiveChannelMemberIDs(ctx, tx, channel.ID, 0)
	if err != nil {
		return nil, err
	}
	return &domain.SendChannelMessageResult{Channel: channel, Message: message, Event: event, Recipients: recipients}, nil
}

const communityColumns = `id, access_hash, creator_user_id, title, about,
default_banned_rights::text, photo_id, photo_dc_id, photo_stripped, date, deleted`

func scanCommunity(row rowScanner) (domain.Community, error) {
	var c domain.Community
	var rights string
	if err := row.Scan(&c.ID, &c.AccessHash, &c.CreatorUserID, &c.Title, &c.About,
		&rights, &c.PhotoID, &c.PhotoDCID, &c.PhotoStripped, &c.Date, &c.Deleted); err != nil {
		return domain.Community{}, err
	}
	if err := json.Unmarshal([]byte(rights), &c.DefaultBannedRights); err != nil {
		return domain.Community{}, fmt.Errorf("decode community banned rights: %w", err)
	}
	c.PhotoStripped = append([]byte(nil), c.PhotoStripped...)
	return c, nil
}

func scanCommunityMember(row rowScanner) (domain.CommunityMember, error) {
	var m domain.CommunityMember
	var role, status, rights string
	if err := row.Scan(&m.CommunityID, &m.UserID, &role, &status, &rights, &m.Rank, &m.Date); err != nil {
		return domain.CommunityMember{}, err
	}
	m.Role = domain.CommunityMemberRole(role)
	m.Status = domain.CommunityMemberStatus(status)
	if err := json.Unmarshal([]byte(rights), &m.AdminRights); err != nil {
		return domain.CommunityMember{}, fmt.Errorf("decode community admin rights: %w", err)
	}
	return m, nil
}

func (s *CommunityStore) begin(ctx context.Context) (pgx.Tx, error) {
	b, ok := s.db.(txBeginner)
	if !ok {
		return nil, errors.New("community store requires transaction-capable db")
	}
	return b.Begin(ctx)
}

func withCommunityTx[T any](ctx context.Context, s *CommunityStore, fn func(pgx.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := s.begin(ctx)
	if err != nil {
		return zero, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	out, err := fn(tx)
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit community transaction: %w", err)
	}
	committed = true
	return out, nil
}

func communityByID(ctx context.Context, db sqlcgen.DBTX, id int64, forUpdate bool) (domain.Community, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	c, err := scanCommunity(db.QueryRow(ctx, `SELECT `+communityColumns+` FROM communities WHERE id=$1 AND NOT deleted`+lock, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Community{}, domain.ErrCommunityInvalid
		}
		return domain.Community{}, fmt.Errorf("get community: %w", err)
	}
	return c, nil
}

func explicitCommunityMember(ctx context.Context, db sqlcgen.DBTX, communityID, userID int64) (domain.CommunityMember, bool, error) {
	m, err := scanCommunityMember(db.QueryRow(ctx, `
SELECT community_id, user_id, role, status, admin_rights::text, rank, date
FROM community_members WHERE community_id=$1 AND user_id=$2`, communityID, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CommunityMember{}, false, nil
		}
		return domain.CommunityMember{}, false, fmt.Errorf("get community member: %w", err)
	}
	return m, true, nil
}

func derivedCommunityMember(ctx context.Context, db sqlcgen.DBTX, c domain.Community, userID int64) (domain.CommunityMember, bool, error) {
	if userID == 0 {
		return domain.CommunityMember{}, false, nil
	}
	if m, ok, err := explicitCommunityMember(ctx, db, c.ID, userID); err != nil || ok {
		return m, ok, err
	}
	var joined bool
	err := db.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM community_peer_links l
  JOIN channel_members cm ON l.peer_type='channel' AND cm.channel_id=l.peer_id
  WHERE l.community_id=$1 AND cm.user_id=$2 AND cm.status='active'
  UNION ALL
  SELECT 1
  FROM community_peer_links l
  JOIN dialogs d ON l.peer_type='user' AND d.peer_id=l.peer_id AND d.peer_type='user'
  WHERE l.community_id=$1 AND d.user_id=$2 AND d.top_message_id > 0
  LIMIT 1
)`, c.ID, userID).Scan(&joined)
	if err != nil {
		return domain.CommunityMember{}, false, fmt.Errorf("derive community member: %w", err)
	}
	if !joined {
		return domain.CommunityMember{}, false, nil
	}
	return domain.CommunityMember{
		CommunityID: c.ID, UserID: userID, Role: domain.CommunityRoleMember,
		Status: domain.CommunityMemberActive, Date: c.Date,
	}, true, nil
}

func communityState(ctx context.Context, db sqlcgen.DBTX, communityID, userID int64) (domain.CommunityUserState, error) {
	state := domain.CommunityUserState{CommunityID: communityID, UserID: userID}
	err := db.QueryRow(ctx, `
SELECT collapsed, pinned, pinned_order FROM community_user_states
WHERE community_id=$1 AND user_id=$2`, communityID, userID).
		Scan(&state.Collapsed, &state.Pinned, &state.PinnedOrder)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.CommunityUserState{}, fmt.Errorf("get community state: %w", err)
	}
	return state, nil
}

func communityLinkRows(ctx context.Context, db sqlcgen.DBTX, c domain.Community, viewer domain.CommunityMember) ([]domain.CommunityPeerLink, error) {
	rows, err := db.Query(ctx, `
SELECT l.peer_type, l.peer_id, l.visibility, l.created_by, l.date,
       CASE
         WHEN l.peer_type='channel' THEN EXISTS (
           SELECT 1 FROM channel_members cm
           WHERE cm.channel_id=l.peer_id AND cm.user_id=$2 AND cm.status='active')
         ELSE EXISTS (
           SELECT 1 FROM dialogs d
           WHERE d.user_id=$2 AND d.peer_type='user' AND d.peer_id=l.peer_id AND d.top_message_id > 0)
       END AS joined,
       CASE
         WHEN l.peer_type='user' THEN true
         ELSE EXISTS (
           SELECT 1 FROM channels c
           WHERE c.id=l.peer_id AND NOT c.deleted AND COALESCE(c.username,'') <> '')
       END AS inherently_viewable
FROM community_peer_links l
WHERE l.community_id=$1
ORDER BY l.date, l.peer_type, l.peer_id`, c.ID, viewer.UserID)
	if err != nil {
		return nil, fmt.Errorf("list community links: %w", err)
	}
	defer rows.Close()
	canManage := viewer.CanManageLinkedPeers()
	out := make([]domain.CommunityPeerLink, 0)
	for rows.Next() {
		var typ, visibility string
		var id, createdBy int64
		var date int
		var joined, inherentlyViewable bool
		if err := rows.Scan(&typ, &id, &visibility, &createdBy, &date, &joined, &inherentlyViewable); err != nil {
			return nil, fmt.Errorf("scan community link: %w", err)
		}
		if visibility == string(domain.CommunityPeerHidden) && !joined && !canManage {
			continue
		}
		out = append(out, domain.CommunityPeerLink{
			CommunityID: c.ID, Peer: domain.Peer{Type: domain.PeerType(typ), ID: id},
			Visibility: domain.CommunityPeerVisibility(visibility), CanViewHistory: joined || inherentlyViewable,
			CreatedBy: createdBy, Date: date,
		})
	}
	return out, rows.Err()
}

func communityView(ctx context.Context, db sqlcgen.DBTX, viewerUserID, communityID int64) (domain.CommunityView, error) {
	c, err := communityByID(ctx, db, communityID, false)
	if err != nil {
		return domain.CommunityView{}, err
	}
	self, joined, err := derivedCommunityMember(ctx, db, c, viewerUserID)
	if err != nil {
		return domain.CommunityView{}, err
	}
	if !joined || !self.Active() {
		return domain.CommunityView{Community: c, Self: self, Forbidden: true}, domain.ErrCommunityPrivate
	}
	state, err := communityState(ctx, db, c.ID, viewerUserID)
	if err != nil {
		return domain.CommunityView{}, err
	}
	links, err := communityLinkRows(ctx, db, c, self)
	if err != nil {
		return domain.CommunityView{}, err
	}
	view := domain.CommunityView{Community: c, Self: self, State: state, Links: links}
	channelIDs, userIDs := make([]int64, 0, len(links)), make([]int64, 0, len(links))
	for _, link := range links {
		if link.Peer.Type == domain.PeerTypeChannel {
			channelIDs = append(channelIDs, link.Peer.ID)
		} else {
			userIDs = append(userIDs, link.Peer.ID)
		}
	}
	view.Channels, err = listChannelsByIDs(ctx, db, channelIDs)
	if err != nil {
		return domain.CommunityView{}, err
	}
	view.Users, err = listUsersByIDs(ctx, db, userIDs)
	if err != nil {
		return domain.CommunityView{}, err
	}
	err = db.QueryRow(ctx, `
SELECT
  COUNT(*) FILTER (WHERE status='active' AND role IN ('creator','admin'))::int,
  COUNT(*) FILTER (WHERE status='kicked')::int,
  (SELECT COUNT(*)::int FROM community_peer_link_requests WHERE community_id=$1)
FROM community_members WHERE community_id=$1`, c.ID).
		Scan(&view.AdminsCount, &view.KickedCount, &view.PendingRequests)
	if err != nil {
		return domain.CommunityView{}, fmt.Errorf("get community counts: %w", err)
	}
	return view, nil
}

func (s *CommunityStore) GetCommunity(ctx context.Context, viewerUserID, communityID int64) (domain.CommunityView, error) {
	return communityView(ctx, s.db, viewerUserID, communityID)
}

func (s *CommunityStore) GetCommunities(ctx context.Context, viewerUserID int64, ids []int64) ([]domain.CommunityView, error) {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]domain.CommunityView, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		view, err := communityView(ctx, s.db, viewerUserID, id)
		if errors.Is(err, domain.ErrCommunityInvalid) || errors.Is(err, domain.ErrCommunityPrivate) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *CommunityStore) ListJoinedCommunities(ctx context.Context, viewerUserID int64) ([]domain.CommunityView, error) {
	rows, err := s.db.Query(ctx, `
SELECT DISTINCT c.id
FROM communities c
LEFT JOIN community_members explicit ON explicit.community_id=c.id AND explicit.user_id=$1
WHERE NOT c.deleted AND (
  (explicit.status='active')
  OR (explicit.user_id IS NULL AND EXISTS (
    SELECT 1 FROM community_peer_links l
    JOIN channel_members cm ON l.peer_type='channel' AND cm.channel_id=l.peer_id
    WHERE l.community_id=c.id AND cm.user_id=$1 AND cm.status='active'))
  OR (explicit.user_id IS NULL AND EXISTS (
    SELECT 1 FROM community_peer_links l
    JOIN dialogs d ON l.peer_type='user' AND d.peer_id=l.peer_id AND d.peer_type='user'
    WHERE l.community_id=c.id AND d.user_id=$1 AND d.top_message_id > 0))
)
ORDER BY c.id`, viewerUserID)
	if err != nil {
		return nil, fmt.Errorf("list joined communities: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.GetCommunities(ctx, viewerUserID, ids)
}

func validateCommunityPeerForLink(ctx context.Context, db sqlcgen.DBTX, actorUserID int64, peer domain.Peer, lock bool) error {
	lockSQL := ""
	if lock {
		lockSQL = " FOR UPDATE"
	}
	switch peer.Type {
	case domain.PeerTypeChannel:
		var linked int64
		var deleted, monoforum bool
		if err := db.QueryRow(ctx, `SELECT linked_community_id, deleted, monoforum FROM channels WHERE id=$1`+lockSQL, peer.ID).
			Scan(&linked, &deleted, &monoforum); err != nil {
			return domain.ErrCommunityPeerInvalid
		}
		if deleted || monoforum {
			return domain.ErrCommunityPeerInvalid
		}
		if linked != 0 {
			return domain.ErrCommunityPeerLinked
		}
		var allowed bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (
SELECT 1 FROM channel_members WHERE channel_id=$1 AND user_id=$2 AND status='active' AND role IN ('creator','admin'))`, peer.ID, actorUserID).Scan(&allowed); err != nil || !allowed {
			return domain.ErrCommunityAdminRequired
		}
	case domain.PeerTypeUser:
		var bot, deleted bool
		var linked int64
		if err := db.QueryRow(ctx, `SELECT is_bot, deleted_at IS NOT NULL, linked_community_id FROM users WHERE id=$1`+lockSQL, peer.ID).
			Scan(&bot, &deleted, &linked); err != nil || !bot || deleted {
			return domain.ErrCommunityPeerInvalid
		}
		if linked != 0 {
			return domain.ErrCommunityPeerLinked
		}
		var owned bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bots WHERE bot_user_id=$1 AND owner_user_id=$2)`, peer.ID, actorUserID).Scan(&owned); err != nil || !owned {
			return domain.ErrCommunityAdminRequired
		}
	default:
		return domain.ErrCommunityPeerInvalid
	}
	return nil
}

func setPeerLinkedCommunity(ctx context.Context, db sqlcgen.DBTX, peer domain.Peer, communityID int64) error {
	var tag string
	switch peer.Type {
	case domain.PeerTypeChannel:
		tag = "UPDATE channels SET linked_community_id=$2, updated_at=now() WHERE id=$1"
	case domain.PeerTypeUser:
		tag = "UPDATE users SET linked_community_id=$2, updated_at=now() WHERE id=$1"
	default:
		return domain.ErrCommunityPeerInvalid
	}
	cmd, err := db.Exec(ctx, tag, peer.ID, communityID)
	if err != nil {
		return fmt.Errorf("set linked community: %w", err)
	}
	if cmd.RowsAffected() != 1 {
		return domain.ErrCommunityPeerInvalid
	}
	return nil
}

func insertCommunityLink(ctx context.Context, db sqlcgen.DBTX, communityID, actorUserID int64, peer domain.Peer, visibility domain.CommunityPeerVisibility, date int) (domain.CommunityPeerLink, error) {
	if err := validateCommunityPeerForLink(ctx, db, actorUserID, peer, true); err != nil {
		return domain.CommunityPeerLink{}, err
	}
	var peers, bots int
	if err := db.QueryRow(ctx, `SELECT
COUNT(*) FILTER (WHERE peer_type='channel')::int,
COUNT(*) FILTER (WHERE peer_type='user')::int
FROM community_peer_links WHERE community_id=$1`, communityID).Scan(&peers, &bots); err != nil {
		return domain.CommunityPeerLink{}, err
	}
	if (peer.Type == domain.PeerTypeChannel && peers >= domain.MaxCommunityPeers) ||
		(peer.Type == domain.PeerTypeUser && bots >= domain.MaxCommunityBotPeers) {
		return domain.CommunityPeerLink{}, domain.ErrCommunityPeersTooMuch
	}
	if _, err := db.Exec(ctx, `
INSERT INTO community_peer_links(community_id,peer_type,peer_id,visibility,created_by,date)
VALUES($1,$2,$3,$4,$5,$6)`, communityID, string(peer.Type), peer.ID, string(visibility), actorUserID, date); err != nil {
		if isUniqueViolation(err) {
			return domain.CommunityPeerLink{}, domain.ErrCommunityPeerLinked
		}
		return domain.CommunityPeerLink{}, fmt.Errorf("insert community link: %w", err)
	}
	if err := setPeerLinkedCommunity(ctx, db, peer, communityID); err != nil {
		return domain.CommunityPeerLink{}, err
	}
	return domain.CommunityPeerLink{CommunityID: communityID, Peer: peer, Visibility: visibility, CanViewHistory: true, CreatedBy: actorUserID, Date: date}, nil
}

func (s *CommunityStore) CreateCommunity(ctx context.Context, req domain.CreateCommunityRequest) (domain.CommunityView, error) {
	return withCommunityTx(ctx, s, func(tx pgx.Tx) (domain.CommunityView, error) {
		if err := validateCommunityPeerForLink(ctx, tx, req.CreatorUserID, req.InitialPeer, true); err != nil {
			return domain.CommunityView{}, err
		}
		id, err := s.ids.NextChannelID(ctx)
		if err != nil {
			return domain.CommunityView{}, fmt.Errorf("allocate community id: %w", err)
		}
		hash, err := randomChannelAccessHash()
		if err != nil {
			return domain.CommunityView{}, fmt.Errorf("community access hash: %w", err)
		}
		c := domain.Community{ID: id, AccessHash: hash, CreatorUserID: req.CreatorUserID, Title: req.Title, About: req.About, Date: req.Date}
		rights, _ := json.Marshal(c.DefaultBannedRights)
		if _, err := tx.Exec(ctx, `
INSERT INTO communities(id,access_hash,creator_user_id,title,about,default_banned_rights,date)
VALUES($1,$2,$3,$4,$5,$6,$7)`, c.ID, c.AccessHash, c.CreatorUserID, c.Title, c.About, rights, c.Date); err != nil {
			return domain.CommunityView{}, fmt.Errorf("insert community: %w", err)
		}
		adminRights, _ := json.Marshal(domain.CreatorChannelAdminRights())
		if _, err := tx.Exec(ctx, `
INSERT INTO community_members(community_id,user_id,role,status,admin_rights,date)
VALUES($1,$2,'creator','active',$3,$4)`, c.ID, c.CreatorUserID, adminRights, c.Date); err != nil {
			return domain.CommunityView{}, fmt.Errorf("insert community creator: %w", err)
		}
		link, err := insertCommunityLink(ctx, tx, c.ID, c.CreatorUserID, req.InitialPeer, req.Visibility, req.Date)
		if err != nil {
			return domain.CommunityView{}, err
		}
		serviceMessage, err := s.appendCommunityServiceMessageTx(ctx, tx, req.InitialPeer, req.CreatorUserID, req.Date, c.ID)
		if err != nil {
			return domain.CommunityView{}, err
		}
		self := domain.CommunityMember{CommunityID: c.ID, UserID: c.CreatorUserID, Role: domain.CommunityRoleCreator, Status: domain.CommunityMemberActive, AdminRights: domain.CreatorChannelAdminRights(), Date: c.Date}
		view := domain.CommunityView{Community: c, Self: self, Links: []domain.CommunityPeerLink{link}, AdminsCount: 1}
		if serviceMessage != nil {
			view.ServiceMessages = append(view.ServiceMessages, *serviceMessage)
		}
		return view, nil
	})
}

func lockCommunityActor(ctx context.Context, tx pgx.Tx, actorUserID, communityID int64) (domain.Community, domain.CommunityMember, error) {
	c, err := communityByID(ctx, tx, communityID, true)
	if err != nil {
		return domain.Community{}, domain.CommunityMember{}, err
	}
	m, ok, err := derivedCommunityMember(ctx, tx, c, actorUserID)
	if err != nil {
		return domain.Community{}, domain.CommunityMember{}, err
	}
	if !ok || !m.Active() {
		return domain.Community{}, domain.CommunityMember{}, domain.ErrCommunityPrivate
	}
	return c, m, nil
}

func (s *CommunityStore) ToggleCommunityPeerLink(ctx context.Context, req domain.CommunityTogglePeerLinkRequest) (domain.CommunityTogglePeerLinkResult, error) {
	return withCommunityTx(ctx, s, func(tx pgx.Tx) (domain.CommunityTogglePeerLinkResult, error) {
		c, actor, err := lockCommunityActor(ctx, tx, req.ActorUserID, req.CommunityID)
		if err != nil {
			return domain.CommunityTogglePeerLinkResult{}, err
		}
		if req.Deleted {
			if !actor.CanManageLinkedPeers() {
				return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityAdminRequired
			}
			cmd, err := tx.Exec(ctx, `DELETE FROM community_peer_links WHERE community_id=$1 AND peer_type=$2 AND peer_id=$3`, c.ID, string(req.Peer.Type), req.Peer.ID)
			if err != nil {
				return domain.CommunityTogglePeerLinkResult{}, err
			}
			if cmd.RowsAffected() == 0 {
				return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityPeerInvalid
			}
			if err := setPeerLinkedCommunity(ctx, tx, req.Peer, 0); err != nil {
				return domain.CommunityTogglePeerLinkResult{}, err
			}
			serviceMessage, err := s.appendCommunityServiceMessageTx(ctx, tx, req.Peer, req.ActorUserID, req.Date, 0)
			if err != nil {
				return domain.CommunityTogglePeerLinkResult{}, err
			}
			return domain.CommunityTogglePeerLinkResult{Community: c, Peer: req.Peer, ServiceMessage: serviceMessage, Removed: true}, nil
		}
		if actor.CanManageLinkedPeers() {
			link, err := insertCommunityLink(ctx, tx, c.ID, req.ActorUserID, req.Peer, req.Visibility, req.Date)
			if err != nil {
				return domain.CommunityTogglePeerLinkResult{}, err
			}
			serviceMessage, err := s.appendCommunityServiceMessageTx(ctx, tx, req.Peer, req.ActorUserID, req.Date, c.ID)
			if err != nil {
				return domain.CommunityTogglePeerLinkResult{}, err
			}
			return domain.CommunityTogglePeerLinkResult{Community: c, Peer: req.Peer, Link: &link, ServiceMessage: serviceMessage}, nil
		}
		if c.DefaultBannedRights.ManageLinkedPeers {
			return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityAdminRequired
		}
		if err := validateCommunityPeerForLink(ctx, tx, req.ActorUserID, req.Peer, true); err != nil {
			return domain.CommunityTogglePeerLinkResult{}, err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO community_peer_link_requests(community_id,peer_type,peer_id,requested_by,visibility,date)
VALUES($1,$2,$3,$4,$5,$6)
ON CONFLICT(community_id,peer_type,peer_id) DO UPDATE SET requested_by=EXCLUDED.requested_by, visibility=EXCLUDED.visibility, date=EXCLUDED.date, created_at=now()`,
			c.ID, string(req.Peer.Type), req.Peer.ID, req.ActorUserID, string(req.Visibility), req.Date); err != nil {
			return domain.CommunityTogglePeerLinkResult{}, fmt.Errorf("save community link request: %w", err)
		}
		return domain.CommunityTogglePeerLinkResult{Community: c, Peer: req.Peer, RequestCreated: true}, nil
	})
}

func (s *CommunityStore) SetCommunityCollapsed(ctx context.Context, userID, communityID int64, collapsed bool) (domain.CommunityView, bool, error) {
	changed, err := withCommunityTx(ctx, s, func(tx pgx.Tx) (bool, error) {
		if _, _, err := lockCommunityActor(ctx, tx, userID, communityID); err != nil {
			return false, err
		}
		var old bool
		err := tx.QueryRow(ctx, `SELECT collapsed FROM community_user_states WHERE community_id=$1 AND user_id=$2 FOR UPDATE`, communityID, userID).Scan(&old)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		if err == nil && old == collapsed {
			return false, nil
		}
		_, err = tx.Exec(ctx, `
INSERT INTO community_user_states(community_id,user_id,collapsed,pinned,pinned_order)
VALUES($1,$2,$3,false,0)
ON CONFLICT(community_id,user_id) DO UPDATE SET collapsed=EXCLUDED.collapsed,
  pinned=CASE WHEN EXCLUDED.collapsed THEN community_user_states.pinned ELSE false END,
  pinned_order=CASE WHEN EXCLUDED.collapsed THEN community_user_states.pinned_order ELSE 0 END,
  updated_at=now()`, communityID, userID, collapsed)
		return true, err
	})
	if err != nil {
		return domain.CommunityView{}, false, err
	}
	view, err := s.GetCommunity(ctx, userID, communityID)
	return view, changed, err
}

type communityRequestCursor struct {
	Date int    `json:"d"`
	Type string `json:"t"`
	ID   int64  `json:"i"`
}

func decodeCommunityRequestCursor(raw string) (communityRequestCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return communityRequestCursor{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return communityRequestCursor{}, domain.ErrCommunityInvalid
	}
	var c communityRequestCursor
	if json.Unmarshal(b, &c) != nil || c.Date <= 0 || c.ID <= 0 {
		return communityRequestCursor{}, domain.ErrCommunityInvalid
	}
	return c, nil
}

func encodeCommunityRequestCursor(c communityRequestCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *CommunityStore) ListCommunityPeerLinkRequests(ctx context.Context, viewerUserID, communityID int64, offset string, limit int) (domain.CommunityPeerLinkRequestPage, error) {
	view, err := s.GetCommunity(ctx, viewerUserID, communityID)
	if err != nil {
		return domain.CommunityPeerLinkRequestPage{}, err
	}
	if !view.Self.CanManageLinkedPeers() {
		return domain.CommunityPeerLinkRequestPage{}, domain.ErrCommunityAdminRequired
	}
	cursor, err := decodeCommunityRequestCursor(offset)
	if err != nil {
		return domain.CommunityPeerLinkRequestPage{}, err
	}
	if limit <= 0 || limit > domain.MaxCommunityLinkRequests {
		limit = domain.MaxCommunityLinkRequests
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM community_peer_link_requests WHERE community_id=$1`, communityID).Scan(&total); err != nil {
		return domain.CommunityPeerLinkRequestPage{}, err
	}
	rows, err := s.db.Query(ctx, `
SELECT peer_type,peer_id,requested_by,visibility,date
FROM community_peer_link_requests
WHERE community_id=$1 AND ($2::int=0 OR (date,peer_type,peer_id) < ($2,$3,$4))
ORDER BY date DESC,peer_type DESC,peer_id DESC LIMIT $5`, communityID, cursor.Date, cursor.Type, cursor.ID, limit+1)
	if err != nil {
		return domain.CommunityPeerLinkRequestPage{}, err
	}
	defer rows.Close()
	page := domain.CommunityPeerLinkRequestPage{TotalCount: total}
	for rows.Next() {
		var typ, visibility string
		var peerID, requestedBy int64
		var date int
		if err := rows.Scan(&typ, &peerID, &requestedBy, &visibility, &date); err != nil {
			return domain.CommunityPeerLinkRequestPage{}, err
		}
		page.Requests = append(page.Requests, domain.CommunityPeerLinkRequest{CommunityID: communityID, Peer: domain.Peer{Type: domain.PeerType(typ), ID: peerID}, RequestedBy: requestedBy, Visibility: domain.CommunityPeerVisibility(visibility), Date: date})
	}
	if len(page.Requests) > limit {
		last := page.Requests[limit-1]
		page.NextOffset = encodeCommunityRequestCursor(communityRequestCursor{Date: last.Date, Type: string(last.Peer.Type), ID: last.Peer.ID})
		page.Requests = page.Requests[:limit]
	}
	channelIDs, userIDs := make([]int64, 0), make([]int64, 0)
	for _, req := range page.Requests {
		userIDs = append(userIDs, req.RequestedBy)
		if req.Peer.Type == domain.PeerTypeChannel {
			channelIDs = append(channelIDs, req.Peer.ID)
		} else {
			userIDs = append(userIDs, req.Peer.ID)
		}
	}
	page.Channels, err = listChannelsByIDs(ctx, s.db, channelIDs)
	if err != nil {
		return domain.CommunityPeerLinkRequestPage{}, err
	}
	page.Users, err = listUsersByIDs(ctx, s.db, uniqueInt64s(userIDs))
	return page, err
}

func requestForUpdate(ctx context.Context, tx pgx.Tx, communityID int64, peer domain.Peer) (domain.CommunityPeerLinkRequest, error) {
	var typ, visibility string
	var out domain.CommunityPeerLinkRequest
	err := tx.QueryRow(ctx, `
SELECT peer_type,peer_id,requested_by,visibility,date FROM community_peer_link_requests
WHERE community_id=$1 AND peer_type=$2 AND peer_id=$3 FOR UPDATE`, communityID, string(peer.Type), peer.ID).
		Scan(&typ, &out.Peer.ID, &out.RequestedBy, &visibility, &out.Date)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CommunityPeerLinkRequest{}, domain.ErrCommunityRequestMissing
		}
		return domain.CommunityPeerLinkRequest{}, err
	}
	out.CommunityID, out.Peer.Type, out.Visibility = communityID, domain.PeerType(typ), domain.CommunityPeerVisibility(visibility)
	return out, nil
}

func (s *CommunityStore) DecideCommunityPeerLinkRequest(ctx context.Context, actorUserID, communityID int64, peer domain.Peer, reject bool, date int) (domain.CommunityTogglePeerLinkResult, error) {
	return withCommunityTx(ctx, s, func(tx pgx.Tx) (domain.CommunityTogglePeerLinkResult, error) {
		c, actor, err := lockCommunityActor(ctx, tx, actorUserID, communityID)
		if err != nil {
			return domain.CommunityTogglePeerLinkResult{}, err
		}
		if !actor.CanManageLinkedPeers() {
			return domain.CommunityTogglePeerLinkResult{}, domain.ErrCommunityAdminRequired
		}
		req, err := requestForUpdate(ctx, tx, communityID, peer)
		if err != nil {
			return domain.CommunityTogglePeerLinkResult{}, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM community_peer_link_requests WHERE community_id=$1 AND peer_type=$2 AND peer_id=$3`, communityID, string(peer.Type), peer.ID); err != nil {
			return domain.CommunityTogglePeerLinkResult{}, err
		}
		if reject {
			return domain.CommunityTogglePeerLinkResult{Community: c, Peer: peer, RequestedBy: req.RequestedBy}, nil
		}
		link, err := insertCommunityLink(ctx, tx, communityID, req.RequestedBy, peer, req.Visibility, date)
		if err != nil {
			return domain.CommunityTogglePeerLinkResult{}, err
		}
		serviceMessage, err := s.appendCommunityServiceMessageTx(ctx, tx, peer, actorUserID, date, c.ID)
		if err != nil {
			return domain.CommunityTogglePeerLinkResult{}, err
		}
		return domain.CommunityTogglePeerLinkResult{Community: c, Peer: peer, RequestedBy: req.RequestedBy, Link: &link, ServiceMessage: serviceMessage}, nil
	})
}

func (s *CommunityStore) DecideAllCommunityPeerLinkRequests(ctx context.Context, actorUserID, communityID int64, reject bool, date int) ([]domain.CommunityTogglePeerLinkResult, error) {
	return withCommunityTx(ctx, s, func(tx pgx.Tx) ([]domain.CommunityTogglePeerLinkResult, error) {
		c, actor, err := lockCommunityActor(ctx, tx, actorUserID, communityID)
		if err != nil {
			return nil, err
		}
		if !actor.CanManageLinkedPeers() {
			return nil, domain.ErrCommunityAdminRequired
		}
		rows, err := tx.Query(ctx, `SELECT peer_type,peer_id,requested_by,visibility,date FROM community_peer_link_requests WHERE community_id=$1 ORDER BY date,peer_type,peer_id FOR UPDATE`, communityID)
		if err != nil {
			return nil, err
		}
		requests := make([]domain.CommunityPeerLinkRequest, 0)
		for rows.Next() {
			var typ, visibility string
			var r domain.CommunityPeerLinkRequest
			if err := rows.Scan(&typ, &r.Peer.ID, &r.RequestedBy, &visibility, &r.Date); err != nil {
				rows.Close()
				return nil, err
			}
			r.CommunityID, r.Peer.Type, r.Visibility = communityID, domain.PeerType(typ), domain.CommunityPeerVisibility(visibility)
			requests = append(requests, r)
		}
		rows.Close()
		if reject {
			_, err := tx.Exec(ctx, `DELETE FROM community_peer_link_requests WHERE community_id=$1`, communityID)
			return make([]domain.CommunityTogglePeerLinkResult, len(requests)), err
		}
		var currentChannels, currentBots int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FILTER(WHERE peer_type='channel')::int, COUNT(*) FILTER(WHERE peer_type='user')::int FROM community_peer_links WHERE community_id=$1`, communityID).Scan(&currentChannels, &currentBots); err != nil {
			return nil, err
		}
		for _, r := range requests {
			if r.Peer.Type == domain.PeerTypeChannel {
				currentChannels++
			} else {
				currentBots++
			}
			if currentChannels > domain.MaxCommunityPeers || currentBots > domain.MaxCommunityBotPeers {
				return nil, domain.ErrCommunityPeersTooMuch
			}
			if err := validateCommunityPeerForLink(ctx, tx, r.RequestedBy, r.Peer, true); err != nil {
				return nil, err
			}
		}
		out := make([]domain.CommunityTogglePeerLinkResult, 0, len(requests))
		for _, r := range requests {
			link, err := insertCommunityLink(ctx, tx, communityID, r.RequestedBy, r.Peer, r.Visibility, date)
			if err != nil {
				return nil, err
			}
			serviceMessage, err := s.appendCommunityServiceMessageTx(ctx, tx, r.Peer, actorUserID, date, c.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, domain.CommunityTogglePeerLinkResult{Community: c, Peer: r.Peer, RequestedBy: r.RequestedBy, Link: &link, ServiceMessage: serviceMessage})
		}
		_, err = tx.Exec(ctx, `DELETE FROM community_peer_link_requests WHERE community_id=$1`, communityID)
		return out, err
	})
}

func (s *CommunityStore) GetCommunityParticipantJoinedChats(ctx context.Context, viewerUserID, communityID, participantUserID int64) (domain.CommunityParticipantJoinedChats, error) {
	view, err := s.GetCommunity(ctx, viewerUserID, communityID)
	if err != nil {
		return domain.CommunityParticipantJoinedChats{}, err
	}
	if !view.Self.CanBanUsers() && viewerUserID != participantUserID {
		return domain.CommunityParticipantJoinedChats{}, domain.ErrCommunityAdminRequired
	}
	rows, err := s.db.Query(ctx, `
SELECT l.peer_id, cm.role
FROM community_peer_links l
JOIN channel_members cm ON cm.channel_id=l.peer_id AND cm.user_id=$2 AND cm.status='active'
WHERE l.community_id=$1 AND l.peer_type='channel'
ORDER BY l.peer_id`, communityID, participantUserID)
	if err != nil {
		return domain.CommunityParticipantJoinedChats{}, err
	}
	defer rows.Close()
	out := domain.CommunityParticipantJoinedChats{}
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		var role string
		if err := rows.Scan(&id, &role); err != nil {
			return out, err
		}
		ids = append(ids, id)
		out.JoinedChatIDs = append(out.JoinedChatIDs, id)
		if role == string(domain.ChannelRoleCreator) {
			out.CreatorChatIDs = append(out.CreatorChatIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Channels, err = listChannelsByIDs(ctx, s.db, ids)
	if err != nil {
		return out, err
	}
	out.Users, err = listUsersByIDs(ctx, s.db, []int64{participantUserID})
	return out, err
}

func (s *CommunityStore) banCommunityParticipantFromChannelTx(ctx context.Context, tx pgx.Tx, actorUserID, channelID, participantUserID int64, date int) (domain.EditChannelBannedResult, bool, error) {
	channelStore := NewChannelStore(tx, WithChannelAllocators(s.ids, s.msgIDs))
	channel, err := getChannelByID(ctx, tx, channelID)
	if err != nil {
		return domain.EditChannelBannedResult{}, false, err
	}
	previous, err := channelStore.getChannelMember(ctx, tx, channelID, participantUserID)
	if errors.Is(err, domain.ErrChannelPrivate) {
		return domain.EditChannelBannedResult{}, false, nil
	}
	if err != nil {
		return domain.EditChannelBannedResult{}, false, err
	}
	if previous.Status != domain.ChannelMemberActive {
		return domain.EditChannelBannedResult{}, false, nil
	}
	member := previous
	member.InviterUserID = actorUserID
	member.Role = domain.ChannelRoleMember
	member.Status = domain.ChannelMemberKicked
	member.LeftAt = date
	member.BannedRights = domain.ChannelBannedRights{ViewMessages: true, UntilDate: 0}
	if err := upsertChannelMemberTx(ctx, tx, channel, member); err != nil {
		return domain.EditChannelBannedResult{}, false, err
	}
	if err := channelStore.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
		ChannelID: channelID, UserID: actorUserID, Date: date, Type: domain.ChannelAdminLogParticipantKick,
		PrevParticipant: &previous, NewParticipant: &member,
	}); err != nil {
		return domain.EditChannelBannedResult{}, false, err
	}
	channel, err = refreshChannelCountsTx(ctx, tx, channel)
	if err != nil {
		return domain.EditChannelBannedResult{}, false, err
	}
	event := transientChannelParticipantEvent(channel.ID, actorUserID, previous, member, date)
	if err := clearChannelMentionsForUserTx(ctx, tx, channelID, participantUserID); err != nil {
		return domain.EditChannelBannedResult{}, false, err
	}
	var serviceMessage domain.ChannelMessage
	var serviceEvent domain.ChannelUpdateEvent
	if channel.Megagroup {
		serviceMessage, serviceEvent, err = channelStore.insertServiceMessage(ctx, tx, channel, actorUserID, date, domain.ChannelMessageAction{
			Type: domain.ChannelActionChatDelete, UserIDs: []int64{participantUserID},
		})
		if err != nil {
			return domain.EditChannelBannedResult{}, false, err
		}
		channel.TopMessageID, channel.Pts = serviceMessage.ID, serviceEvent.Pts
	}
	recipients, err := channelStore.listActiveChannelMemberIDs(ctx, tx, channelID, 0)
	if err != nil {
		return domain.EditChannelBannedResult{}, false, err
	}
	recipients = append(recipients, participantUserID)
	return domain.EditChannelBannedResult{
		Channel: channel, Previous: previous, Participant: member, Event: event,
		Recipients: recipients, Date: date, Message: serviceMessage, ServiceEvent: serviceEvent,
	}, true, nil
}

func (s *CommunityStore) ToggleCommunityParticipantBanned(ctx context.Context, actorUserID, communityID, participantUserID int64, unban bool, date int) (domain.CommunityParticipantBanResult, error) {
	return withCommunityTx(ctx, s, func(tx pgx.Tx) (domain.CommunityParticipantBanResult, error) {
		c, actor, err := lockCommunityActor(ctx, tx, actorUserID, communityID)
		if err != nil {
			return domain.CommunityParticipantBanResult{}, err
		}
		if !actor.CanBanUsers() || participantUserID == c.CreatorUserID {
			return domain.CommunityParticipantBanResult{}, domain.ErrCommunityAdminRequired
		}
		if unban {
			cmd, err := tx.Exec(ctx, `DELETE FROM community_members WHERE community_id=$1 AND user_id=$2 AND role='member' AND status='kicked'`, communityID, participantUserID)
			return domain.CommunityParticipantBanResult{Changed: cmd.RowsAffected() > 0}, err
		}
		participant, found, err := derivedCommunityMember(ctx, tx, c, participantUserID)
		if err != nil {
			return domain.CommunityParticipantBanResult{}, err
		}
		if !found || (participant.Status != domain.CommunityMemberActive && participant.Status != domain.CommunityMemberKicked) {
			return domain.CommunityParticipantBanResult{}, domain.ErrCommunityParticipantInvalid
		}
		var alreadyKicked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM community_members WHERE community_id=$1 AND user_id=$2 AND role='member' AND status='kicked')`, communityID, participantUserID).Scan(&alreadyKicked); err != nil {
			return domain.CommunityParticipantBanResult{}, err
		}
		// Chats owned by the banned participant (and their bots) leave the
		// Community; the participant is kicked from every remaining linked chat.
		rows, err := tx.Query(ctx, `
SELECT l.peer_type,l.peer_id
FROM community_peer_links l
LEFT JOIN channels c ON l.peer_type='channel' AND c.id=l.peer_id
LEFT JOIN bots b ON l.peer_type='user' AND b.bot_user_id=l.peer_id
WHERE l.community_id=$1 AND (c.creator_user_id=$2 OR b.owner_user_id=$2)
FOR UPDATE OF l`, communityID, participantUserID)
		if err != nil {
			return domain.CommunityParticipantBanResult{}, err
		}
		owned := make([]domain.Peer, 0)
		for rows.Next() {
			var typ string
			var id int64
			if err := rows.Scan(&typ, &id); err != nil {
				rows.Close()
				return domain.CommunityParticipantBanResult{}, err
			}
			owned = append(owned, domain.Peer{Type: domain.PeerType(typ), ID: id})
		}
		rows.Close()
		result := domain.CommunityParticipantBanResult{}
		for _, p := range owned {
			if _, err := tx.Exec(ctx, `DELETE FROM community_peer_links WHERE community_id=$1 AND peer_type=$2 AND peer_id=$3`, communityID, string(p.Type), p.ID); err != nil {
				return domain.CommunityParticipantBanResult{}, err
			}
			if err := setPeerLinkedCommunity(ctx, tx, p, 0); err != nil {
				return domain.CommunityParticipantBanResult{}, err
			}
			serviceMessage, err := s.appendCommunityServiceMessageTx(ctx, tx, p, actorUserID, date, 0)
			if err != nil {
				return domain.CommunityParticipantBanResult{}, err
			}
			result.RemovedLinks = append(result.RemovedLinks, domain.CommunityTogglePeerLinkResult{Community: c, Peer: p, Removed: true, ServiceMessage: serviceMessage})
		}
		channelRows, err := tx.Query(ctx, `SELECT peer_id FROM community_peer_links WHERE community_id=$1 AND peer_type='channel' ORDER BY peer_id FOR UPDATE`, communityID)
		if err != nil {
			return domain.CommunityParticipantBanResult{}, err
		}
		channelIDs := make([]int64, 0)
		for channelRows.Next() {
			var channelID int64
			if err := channelRows.Scan(&channelID); err != nil {
				channelRows.Close()
				return domain.CommunityParticipantBanResult{}, err
			}
			channelIDs = append(channelIDs, channelID)
		}
		channelRows.Close()
		for _, channelID := range channelIDs {
			ban, changed, err := s.banCommunityParticipantFromChannelTx(ctx, tx, actorUserID, channelID, participantUserID, date)
			if err != nil {
				return domain.CommunityParticipantBanResult{}, err
			}
			if changed {
				result.ChannelBans = append(result.ChannelBans, ban)
			}
		}
		if !alreadyKicked {
			if _, err := tx.Exec(ctx, `
INSERT INTO community_members(community_id,user_id,role,status,admin_rights,date)
VALUES($1,$2,'member','kicked','{}',$3)
ON CONFLICT(community_id,user_id) DO UPDATE SET role='member',status='kicked',admin_rights='{}',rank='',date=EXCLUDED.date,updated_at=now()`, communityID, participantUserID, date); err != nil {
				return domain.CommunityParticipantBanResult{}, err
			}
		}
		result.Changed = !alreadyKicked || len(result.ChannelBans) > 0 || len(result.RemovedLinks) > 0
		return result, nil
	})
}

func communityParticipantHash(items []domain.CommunityMember) int64 {
	h := fnv.New64a()
	for _, m := range items {
		fmt.Fprintf(h, "%d:%s:%s;", m.UserID, m.Role, m.Status)
	}
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

func (s *CommunityStore) ListCommunityParticipants(ctx context.Context, viewerUserID, communityID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.CommunityParticipantList, error) {
	view, err := s.GetCommunity(ctx, viewerUserID, communityID)
	if err != nil {
		return domain.CommunityParticipantList{}, err
	}
	restricted := filter.Kind == domain.ChannelParticipantsKicked || filter.Kind == domain.ChannelParticipantsBanned
	if restricted && !view.Self.CanManageLinkedPeers() {
		return domain.CommunityParticipantList{}, domain.ErrCommunityAdminRequired
	}
	where := "am.status='active'"
	switch filter.Kind {
	case domain.ChannelParticipantsAdmins:
		where = "am.status='active' AND am.role IN ('creator','admin')"
	case domain.ChannelParticipantsKicked, domain.ChannelParticipantsBanned:
		where = "am.status='kicked'"
	}
	query := `
WITH derived AS (
 SELECT cm.user_id FROM community_peer_links l JOIN channel_members cm ON l.peer_type='channel' AND cm.channel_id=l.peer_id
 WHERE l.community_id=$1 AND cm.status='active'
 UNION
 SELECT d.user_id FROM community_peer_links l JOIN dialogs d ON l.peer_type='user' AND d.peer_id=l.peer_id AND d.peer_type='user'
 WHERE l.community_id=$1 AND d.top_message_id>0
), all_members AS (
 SELECT community_id,user_id,role,status,admin_rights,rank,date FROM community_members WHERE community_id=$1
 UNION ALL
 SELECT $1,d.user_id,'member','active','{}'::jsonb,'',0 FROM derived d
 WHERE NOT EXISTS(SELECT 1 FROM community_members m WHERE m.community_id=$1 AND m.user_id=d.user_id)
)
SELECT am.community_id,am.user_id,am.role,am.status,am.admin_rights::text,am.rank,am.date,COUNT(*) OVER()::int
FROM all_members am
JOIN users u ON u.id=am.user_id
WHERE ` + where + `
  AND ($4='' OR strpos(lower(concat_ws(' ',am.user_id::text,u.first_name,u.last_name,u.username,u.phone)),lower($4))>0)
ORDER BY CASE am.role WHEN 'creator' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END,am.user_id OFFSET $2 LIMIT $3`
	rows, err := s.db.Query(ctx, query, communityID, offset, limit, strings.TrimSpace(filter.Query))
	if err != nil {
		return domain.CommunityParticipantList{}, err
	}
	defer rows.Close()
	out := domain.CommunityParticipantList{Community: view.Community}
	ids := make([]int64, 0)
	for rows.Next() {
		var m domain.CommunityMember
		var role, status, rights string
		var count int
		if err := rows.Scan(&m.CommunityID, &m.UserID, &role, &status, &rights, &m.Rank, &m.Date, &count); err != nil {
			return out, err
		}
		m.Role, m.Status = domain.CommunityMemberRole(role), domain.CommunityMemberStatus(status)
		_ = json.Unmarshal([]byte(rights), &m.AdminRights)
		out.Count = count
		out.Participants = append(out.Participants, m)
		ids = append(ids, m.UserID)
	}
	out.Users, err = listUsersByIDs(ctx, s.db, ids)
	out.Hash = communityParticipantHash(out.Participants)
	return out, err
}

func (s *CommunityStore) EditCommunityTitle(ctx context.Context, actorUserID, communityID int64, title string) (domain.CommunityView, bool, error) {
	changed, err := withCommunityTx(ctx, s, func(tx pgx.Tx) (bool, error) {
		c, m, e := lockCommunityActor(ctx, tx, actorUserID, communityID)
		if e != nil {
			return false, e
		}
		if !m.CanChangeInfo() {
			return false, domain.ErrCommunityAdminRequired
		}
		if c.Title == title {
			return false, nil
		}
		_, e = tx.Exec(ctx, `UPDATE communities SET title=$2,updated_at=now() WHERE id=$1`, communityID, title)
		return true, e
	})
	if err != nil {
		return domain.CommunityView{}, false, err
	}
	v, err := s.GetCommunity(ctx, actorUserID, communityID)
	return v, changed, err
}

func (s *CommunityStore) EditCommunityAbout(ctx context.Context, actorUserID, communityID int64, about string) (domain.CommunityView, bool, error) {
	changed, err := withCommunityTx(ctx, s, func(tx pgx.Tx) (bool, error) {
		c, m, e := lockCommunityActor(ctx, tx, actorUserID, communityID)
		if e != nil {
			return false, e
		}
		if !m.CanChangeInfo() {
			return false, domain.ErrCommunityAdminRequired
		}
		if c.About == about {
			return false, nil
		}
		_, e = tx.Exec(ctx, `UPDATE communities SET about=$2,updated_at=now() WHERE id=$1`, communityID, about)
		return true, e
	})
	if err != nil {
		return domain.CommunityView{}, false, err
	}
	v, err := s.GetCommunity(ctx, actorUserID, communityID)
	return v, changed, err
}

func zeroCommunityAdminRights(r domain.ChannelAdminRights) bool {
	return r == (domain.ChannelAdminRights{})
}

func (s *CommunityStore) EditCommunityAdmin(ctx context.Context, req domain.CommunityEditAdminRequest) (domain.CommunityView, bool, error) {
	changed, err := withCommunityTx(ctx, s, func(tx pgx.Tx) (bool, error) {
		c, actor, e := lockCommunityActor(ctx, tx, req.ActorUserID, req.CommunityID)
		if e != nil {
			return false, e
		}
		if req.UserID == req.ActorUserID && actor.Role == domain.CommunityRoleAdmin && zeroCommunityAdminRights(req.Rights) {
			cmd, e := tx.Exec(ctx, `DELETE FROM community_members WHERE community_id=$1 AND user_id=$2 AND role='admin'`, req.CommunityID, req.UserID)
			return cmd.RowsAffected() > 0, e
		}
		if !actor.CanAddAdmins() {
			return false, domain.ErrCommunityAdminRequired
		}
		if req.UserID == c.CreatorUserID {
			return false, domain.ErrCommunityCreatorRequired
		}
		if zeroCommunityAdminRights(req.Rights) {
			cmd, e := tx.Exec(ctx, `DELETE FROM community_members WHERE community_id=$1 AND user_id=$2 AND role='admin'`, req.CommunityID, req.UserID)
			return cmd.RowsAffected() > 0, e
		}
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND deleted_at IS NULL)`, req.UserID).Scan(&exists); e != nil || !exists {
			return false, domain.ErrCommunityParticipantInvalid
		}
		rights, _ := json.Marshal(req.Rights)
		_, e = tx.Exec(ctx, `
INSERT INTO community_members(community_id,user_id,role,status,admin_rights,rank,date)
VALUES($1,$2,'admin','active',$3,$4,$5)
ON CONFLICT(community_id,user_id) DO UPDATE SET role='admin',status='active',admin_rights=EXCLUDED.admin_rights,rank=EXCLUDED.rank,date=EXCLUDED.date,updated_at=now()`, req.CommunityID, req.UserID, rights, req.Rank, req.Date)
		return true, e
	})
	if err != nil {
		return domain.CommunityView{}, false, err
	}
	v, err := s.GetCommunity(ctx, req.ActorUserID, req.CommunityID)
	if changed && req.UserID == req.ActorUserID && errors.Is(err, domain.ErrCommunityPrivate) {
		c, loadErr := communityByID(ctx, s.db, req.CommunityID, false)
		if loadErr != nil {
			return domain.CommunityView{}, false, loadErr
		}
		return domain.CommunityView{Community: c, Forbidden: true}, true, nil
	}
	return v, changed, err
}

func (s *CommunityStore) EditCommunityDefaultBannedRights(ctx context.Context, actorUserID, communityID int64, rights domain.ChannelBannedRights) (domain.CommunityView, bool, error) {
	changed, err := withCommunityTx(ctx, s, func(tx pgx.Tx) (bool, error) {
		c, m, e := lockCommunityActor(ctx, tx, actorUserID, communityID)
		if e != nil {
			return false, e
		}
		if !m.CanChangeInfo() {
			return false, domain.ErrCommunityAdminRequired
		}
		if c.DefaultBannedRights == rights {
			return false, nil
		}
		raw, _ := json.Marshal(rights)
		_, e = tx.Exec(ctx, `UPDATE communities SET default_banned_rights=$2,updated_at=now() WHERE id=$1`, communityID, raw)
		return true, e
	})
	if err != nil {
		return domain.CommunityView{}, false, err
	}
	v, err := s.GetCommunity(ctx, actorUserID, communityID)
	return v, changed, err
}

func (s *CommunityStore) SetCommunityPhoto(ctx context.Context, actorUserID, communityID int64, photo *domain.Photo, date int) (domain.CommunityView, bool, error) {
	changed, err := withCommunityTx(ctx, s, func(tx pgx.Tx) (bool, error) {
		c, m, e := lockCommunityActor(ctx, tx, actorUserID, communityID)
		if e != nil {
			return false, e
		}
		if !m.CanChangeInfo() {
			return false, domain.ErrCommunityAdminRequired
		}
		id, dc := int64(0), 0
		var stripped []byte
		if photo != nil {
			id, dc = photo.ID, photo.DCID
			stripped = domain.StrippedFromSizes(photo.Sizes)
		}
		if c.PhotoID == id && c.PhotoDCID == dc && string(c.PhotoStripped) == string(stripped) {
			return false, nil
		}
		_, e = tx.Exec(ctx, `UPDATE communities SET photo_id=$2,photo_dc_id=$3,photo_stripped=$4,updated_at=now() WHERE id=$1`, communityID, id, dc, stripped)
		return true, e
	})
	if err != nil {
		return domain.CommunityView{}, false, err
	}
	v, err := s.GetCommunity(ctx, actorUserID, communityID)
	return v, changed, err
}

func (s *CommunityStore) DeleteCommunity(ctx context.Context, actorUserID, communityID int64, date int) (domain.CommunityView, []domain.Peer, error) {
	type result struct {
		view  domain.CommunityView
		peers []domain.Peer
	}
	r, err := withCommunityTx(ctx, s, func(tx pgx.Tx) (result, error) {
		c, m, e := lockCommunityActor(ctx, tx, actorUserID, communityID)
		if e != nil {
			return result{}, e
		}
		if m.Role != domain.CommunityRoleCreator {
			return result{}, domain.ErrCommunityCreatorRequired
		}
		links, e := communityLinkRows(ctx, tx, c, m)
		if e != nil {
			return result{}, e
		}
		peers := make([]domain.Peer, 0, len(links))
		serviceMessages := make([]domain.SendChannelMessageResult, 0, len(links))
		for _, l := range links {
			peers = append(peers, l.Peer)
			if e := setPeerLinkedCommunity(ctx, tx, l.Peer, 0); e != nil {
				return result{}, e
			}
			serviceMessage, e := s.appendCommunityServiceMessageTx(ctx, tx, l.Peer, actorUserID, date, 0)
			if e != nil {
				return result{}, e
			}
			if serviceMessage != nil {
				serviceMessages = append(serviceMessages, *serviceMessage)
			}
		}
		if _, e = tx.Exec(ctx, `DELETE FROM community_peer_links WHERE community_id=$1`, communityID); e != nil {
			return result{}, e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM community_peer_link_requests WHERE community_id=$1`, communityID); e != nil {
			return result{}, e
		}
		if _, e = tx.Exec(ctx, `DELETE FROM community_user_states WHERE community_id=$1`, communityID); e != nil {
			return result{}, e
		}
		if _, e = tx.Exec(ctx, `UPDATE communities SET deleted=true,title='',about='',updated_at=now() WHERE id=$1`, communityID); e != nil {
			return result{}, e
		}
		c.Deleted = true
		c.Title = ""
		c.About = ""
		return result{view: domain.CommunityView{Community: c, Self: m, Forbidden: true, ServiceMessages: serviceMessages}, peers: peers}, nil
	})
	return r.view, r.peers, err
}

func (s *CommunityStore) SetCommunityPinned(ctx context.Context, userID, communityID int64, pinned bool) (bool, error) {
	return withCommunityTx(ctx, s, func(tx pgx.Tx) (bool, error) {
		if _, _, e := lockCommunityActor(ctx, tx, userID, communityID); e != nil {
			return false, e
		}
		var collapsed, old bool
		var order int
		e := tx.QueryRow(ctx, `SELECT collapsed,pinned,pinned_order FROM community_user_states WHERE community_id=$1 AND user_id=$2 FOR UPDATE`, communityID, userID).Scan(&collapsed, &old, &order)
		if e != nil {
			return false, domain.ErrCommunityInvalid
		}
		if !collapsed {
			return false, domain.ErrCommunityInvalid
		}
		if old == pinned {
			return false, nil
		}
		if pinned {
			if e := tx.QueryRow(ctx, `SELECT GREATEST(1000000000,COALESCE(MAX(pinned_order),0))+1 FROM community_user_states WHERE user_id=$1 AND pinned`, userID).Scan(&order); e != nil {
				return false, e
			}
		} else {
			order = 0
		}
		_, e = tx.Exec(ctx, `UPDATE community_user_states SET pinned=$3,pinned_order=$4,updated_at=now() WHERE community_id=$1 AND user_id=$2`, communityID, userID, pinned, order)
		return true, e
	})
}

func (s *CommunityStore) ReorderCommunityPinned(ctx context.Context, userID int64, order []domain.Peer, force bool) (bool, error) {
	return withCommunityTx(ctx, s, func(tx pgx.Tx) (bool, error) {
		seen := map[int64]struct{}{}
		for _, peer := range order {
			if peer.Type != domain.PeerTypeCommunity {
				continue
			}
			if peer.ID == 0 {
				return false, domain.ErrCommunityInvalid
			}
			if _, ok := seen[peer.ID]; ok {
				return false, domain.ErrCommunityInvalid
			}
			seen[peer.ID] = struct{}{}
		}
		rows, e := tx.Query(ctx, `SELECT community_id,pinned_order FROM community_user_states WHERE user_id=$1 AND pinned FOR UPDATE`, userID)
		if e != nil {
			return false, e
		}
		old := map[int64]int{}
		for rows.Next() {
			var id int64
			var pinnedOrder int
			if e := rows.Scan(&id, &pinnedOrder); e != nil {
				rows.Close()
				return false, e
			}
			old[id] = pinnedOrder
		}
		rows.Close()
		changed := false
		if force {
			if _, e = tx.Exec(ctx, `UPDATE community_user_states SET pinned=false,pinned_order=0,updated_at=now() WHERE user_id=$1 AND pinned`, userID); e != nil {
				return false, e
			}
			changed = len(old) > 0
		}
		for i, peer := range order {
			if peer.Type != domain.PeerTypeCommunity {
				continue
			}
			pinnedOrder := len(order) - i
			if old[peer.ID] == pinnedOrder && !force {
				continue
			}
			cmd, e := tx.Exec(ctx, `UPDATE community_user_states SET pinned=true,pinned_order=$3,updated_at=now() WHERE user_id=$1 AND community_id=$2 AND collapsed`, userID, peer.ID, pinnedOrder)
			if e != nil {
				return false, e
			}
			if cmd.RowsAffected() != 1 {
				return false, domain.ErrCommunityInvalid
			}
			changed = true
		}
		return changed, nil
	})
}

func (s *CommunityStore) CommunitySearchScope(ctx context.Context, viewerUserID, communityID int64) (domain.CommunitySearchScope, error) {
	v, err := s.GetCommunity(ctx, viewerUserID, communityID)
	if err != nil {
		return domain.CommunitySearchScope{}, err
	}
	out := domain.CommunitySearchScope{CommunityID: communityID}
	for _, l := range v.Links {
		if l.Peer.Type == domain.PeerTypeChannel {
			if l.CanViewHistory {
				out.ChannelIDs = append(out.ChannelIDs, l.Peer.ID)
			}
		} else {
			out.BotUserIDs = append(out.BotUserIDs, l.Peer.ID)
		}
	}
	return out, nil
}

func uniqueInt64s(ids []int64) []int64 {
	seen := map[int64]struct{}{}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
