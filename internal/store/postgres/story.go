package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// StoryStore persists Telegram story snapshots and per-viewer state in PG.
type StoryStore struct {
	db sqlcgen.DBTX
}

// NewStoryStore creates a PostgreSQL-backed story store.
func NewStoryStore(db sqlcgen.DBTX) *StoryStore {
	return &StoryStore{db: db}
}

const storySelectColumns = `
    s.owner_peer_type,
    s.owner_peer_id,
    s.story_id,
    s.random_id,
    s.date,
    s.expire_date,
    s.deleted,
    s.pinned,
    s.pinned_to_top_order,
    s.public,
    s.close_friends,
    s.contacts,
    s.selected_contacts,
    s.noforwards,
    s.edited,
    COALESCE(s.privacy_rules::text, '[]')::text,
    s.allow_user_ids,
    s.disallow_user_ids,
    s.caption,
    COALESCE(s.entities::text, '[]')::text,
    COALESCE(s.media::text, '{}')::text,
    COALESCE(s.media_areas::text, '[]')::text,
    COALESCE(s.fwd_from::text, '{}')::text`

const storyReturningColumns = `
    owner_peer_type,
    owner_peer_id,
    story_id,
    random_id,
    date,
    expire_date,
    deleted,
    pinned,
    pinned_to_top_order,
    public,
    close_friends,
    contacts,
    selected_contacts,
    noforwards,
    edited,
    COALESCE(privacy_rules::text, '[]')::text,
    allow_user_ids,
    disallow_user_ids,
    caption,
    COALESCE(entities::text, '[]')::text,
    COALESCE(media::text, '{}')::text,
    COALESCE(media_areas::text, '[]')::text,
    COALESCE(fwd_from::text, '{}')::text`

func storyVisiblePredicate(viewerParam string) string {
	return storyVisiblePredicateFor("s", viewerParam)
}

func storyPublicRepostVisiblePredicateFor(alias, viewerParam string) string {
	return storyBaseVisiblePredicateFor(alias, viewerParam)
}

// storyForwardSourceTypeSQL / storyForwardSourceIDSQL 复刻 memory store 的 repost 源解析
// （memory story.go effectiveForwardSource：forward.Source.ID != 0 用 Source，否则回退 From）。
// 原 postgres 写法只在 Source 字段为空串时回退 From，但零值 Peer 的 Source.ID 会序列化成 "0"
// （非空串），使「只设了 From 的 repost」被计成 source_id=0 → forwards_count/列表错位为 0，
// 与 memory 行为漂移。两 store 必须一致：按 Source.ID 是否为 0 在 Source / From 间整体切换。
func storyForwardSourceTypeSQL(alias string) string {
	return `(CASE WHEN COALESCE(NULLIF(` + alias + `.fwd_from->'Source'->>'ID', ''), '0')::bigint <> 0
        THEN ` + alias + `.fwd_from->'Source'->>'Type'
        ELSE ` + alias + `.fwd_from->'From'->>'Type' END)`
}

func storyForwardSourceIDSQL(alias string) string {
	return `(CASE WHEN COALESCE(NULLIF(` + alias + `.fwd_from->'Source'->>'ID', ''), '0')::bigint <> 0
        THEN (` + alias + `.fwd_from->'Source'->>'ID')::bigint
        ELSE (` + alias + `.fwd_from->'From'->>'ID')::bigint END)`
}

func storyVisiblePredicateFor(alias, viewerParam string) string {
	return `(
    (` + alias + `.owner_peer_type <> 'channel'
      OR EXISTS (
        SELECT 1
        FROM channel_members cm
        WHERE cm.channel_id = ` + alias + `.owner_peer_id
          AND cm.user_id = ` + viewerParam + `
          AND cm.status = 'active'
          AND NOT COALESCE((cm.banned_rights->>'ViewMessages')::boolean, false)
      )
    )
    AND ` + storyBaseVisiblePredicateFor(alias, viewerParam) + `
)`
}

func storyBaseVisiblePredicateFor(alias, viewerParam string) string {
	return `(
    (` + alias + `.owner_peer_type = 'user' AND ` + alias + `.owner_peer_id = ` + viewerParam + `)
    OR (
      NOT (
        ` + alias + `.owner_peer_type = 'user'
        AND EXISTS (
          SELECT 1
          FROM contact_blocks b
          WHERE b.owner_user_id = ` + alias + `.owner_peer_id
            AND b.blocked_user_id = ` + viewerParam + `
        )
      )
      AND
      NOT (` + viewerParam + ` = ANY(` + alias + `.disallow_user_ids))
      AND (
        ` + alias + `.public
        OR ` + viewerParam + ` = ANY(` + alias + `.allow_user_ids)
        OR (
          ` + alias + `.owner_peer_type = 'user'
          AND ` + alias + `.contacts
          AND EXISTS (
            SELECT 1
            FROM contacts c
            WHERE c.user_id = ` + alias + `.owner_peer_id
              AND c.contact_user_id = ` + viewerParam + `
          )
        )
        OR (
          ` + alias + `.owner_peer_type = 'user'
          AND ` + alias + `.close_friends
          AND EXISTS (
            SELECT 1
            FROM contacts c
            WHERE c.user_id = ` + alias + `.owner_peer_id
              AND c.contact_user_id = ` + viewerParam + `
              AND c.close_friend
          )
        )
      )
    )
)`
}

func (s *StoryStore) CreateStory(ctx context.Context, req domain.StoryCreateRequest) (domain.StoryCreateResult, error) {
	if err := validatePGStoryPeer(req.Owner); err != nil {
		return domain.StoryCreateResult{}, err
	}
	if req.RandomID == 0 {
		return domain.StoryCreateResult{}, domain.ErrStoryIDInvalid
	}
	if existing, ok, err := s.storyByRandomID(ctx, req.Owner, req.RandomID); err != nil {
		return domain.StoryCreateResult{}, err
	} else if ok {
		return domain.StoryCreateResult{Story: existing, Duplicate: true}, nil
	}
	entities, err := encodeMessageEntities(req.Entities)
	if err != nil {
		return domain.StoryCreateResult{}, fmt.Errorf("encode story entities: %w", err)
	}
	media, err := encodeMessageMedia(req.Media)
	if err != nil {
		return domain.StoryCreateResult{}, fmt.Errorf("encode story media: %w", err)
	}
	mediaAreas, err := encodeStoryMediaAreas(req.MediaAreas)
	if err != nil {
		return domain.StoryCreateResult{}, fmt.Errorf("encode story media areas: %w", err)
	}
	forward, err := encodeStoryForward(req.Forward)
	if err != nil {
		return domain.StoryCreateResult{}, fmt.Errorf("encode story forward: %w", err)
	}
	privacyRules, err := encodePrivacyRules(req.PrivacyRules)
	if err != nil {
		return domain.StoryCreateResult{}, fmt.Errorf("encode story privacy rules: %w", err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		row := s.db.QueryRow(ctx, `
WITH next_id AS (
  SELECT COALESCE(MAX(story_id), 0) + 1 AS story_id
  FROM stories
  WHERE owner_peer_type = $1
    AND owner_peer_id = $2
),
inserted AS (
  INSERT INTO stories (
      owner_peer_type, owner_peer_id, story_id, random_id, date, expire_date,
      deleted, pinned, pinned_to_top_order, public, close_friends, contacts, selected_contacts,
      noforwards, edited, privacy_rules, allow_user_ids, disallow_user_ids, caption, entities, media, media_areas, fwd_from
  )
  SELECT
      $1, $2, next_id.story_id, $3, $4, $5,
      false, $6, 0, $7, $8, $9, $10,
      $11, false, $12::jsonb, $13::bigint[], $14::bigint[], $15, $16::jsonb, $17::jsonb, $18::jsonb, $19::jsonb
  FROM next_id
  WHERE next_id.story_id <= $20
  RETURNING `+storyReturningColumns+`
),
self_read AS (
  INSERT INTO story_read_states (viewer_user_id, owner_peer_type, owner_peer_id, max_read_id, date)
  SELECT owner_peer_id, owner_peer_type, owner_peer_id, story_id, date
  FROM inserted
  WHERE owner_peer_type = 'user'
  ON CONFLICT (viewer_user_id, owner_peer_type, owner_peer_id) DO UPDATE SET
    max_read_id = GREATEST(story_read_states.max_read_id, EXCLUDED.max_read_id),
    date = CASE
      WHEN EXCLUDED.max_read_id > story_read_states.max_read_id THEN EXCLUDED.date
      ELSE story_read_states.date
    END,
    updated_at = CASE
      WHEN EXCLUDED.max_read_id > story_read_states.max_read_id THEN now()
      ELSE story_read_states.updated_at
    END
  RETURNING 1
)
-- inserted 的列已是 storyReturningColumns（含 COALESCE 等表达式求值后的结果），外层不能再按
-- 原列名（如 privacy_rules）重复套表达式——那些列在 CTE 里是表达式结果而非原始列，会报
-- "column does not exist"。直接 SELECT * 取 CTE 行（顺序与 storyReturningColumns 一致，按位扫描）。
SELECT * FROM inserted`,
			string(req.Owner.Type), req.Owner.ID, req.RandomID, int32(req.Date), int32(req.Date+req.Period),
			req.Pinned, req.Public, req.CloseFriends, req.Contacts, req.SelectedContacts,
			req.NoForwards, privacyRules, nonNullInt64s(req.AllowUserIDs), nonNullInt64s(req.DisallowUserIDs), req.Caption, entities, media, mediaAreas, forward, int32(domain.MaxStoryID))
		story, err := scanPGStory(row, req.Owner.ID)
		if err == nil {
			return domain.StoryCreateResult{Story: story}, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.StoryCreateResult{}, domain.ErrStoryIDInvalid
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if existing, ok, loadErr := s.storyByRandomID(ctx, req.Owner, req.RandomID); loadErr != nil {
				return domain.StoryCreateResult{}, loadErr
			} else if ok {
				return domain.StoryCreateResult{Story: existing, Duplicate: true}, nil
			}
			continue
		}
		return domain.StoryCreateResult{}, fmt.Errorf("create story: %w", err)
	}
	return domain.StoryCreateResult{}, fmt.Errorf("create story: exhausted id allocation retries")
}

func (s *StoryStore) UpsertStory(ctx context.Context, req domain.UpsertStoryRequest) (domain.Story, error) {
	story := clonePGStory(req.Story)
	if err := validatePGStoryIdentity(story.Owner, story.ID); err != nil {
		return domain.Story{}, err
	}
	if story.Deleted || !story.Pinned {
		story.PinnedToTopOrder = 0
	}
	entities, err := encodeMessageEntities(story.Entities)
	if err != nil {
		return domain.Story{}, fmt.Errorf("encode story entities: %w", err)
	}
	media, err := encodeMessageMedia(story.Media)
	if err != nil {
		return domain.Story{}, fmt.Errorf("encode story media: %w", err)
	}
	mediaAreas, err := encodeStoryMediaAreas(story.MediaAreas)
	if err != nil {
		return domain.Story{}, fmt.Errorf("encode story media areas: %w", err)
	}
	forward, err := encodeStoryForward(story.Forward)
	if err != nil {
		return domain.Story{}, fmt.Errorf("encode story forward: %w", err)
	}
	privacyRules, err := encodePrivacyRules(story.PrivacyRules)
	if err != nil {
		return domain.Story{}, fmt.Errorf("encode story privacy rules: %w", err)
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO stories (
    owner_peer_type, owner_peer_id, story_id, random_id, date, expire_date,
    deleted, pinned, pinned_to_top_order, public, close_friends, contacts, selected_contacts,
    noforwards, edited, privacy_rules, allow_user_ids, disallow_user_ids, caption, entities, media, media_areas, fwd_from
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, $11, $12, $13,
    $14, $15, $16::jsonb, $17::bigint[], $18::bigint[], $19, $20::jsonb, $21::jsonb, $22::jsonb, $23::jsonb
)
ON CONFLICT (owner_peer_type, owner_peer_id, story_id) DO UPDATE SET
    random_id = EXCLUDED.random_id,
    date = EXCLUDED.date,
    expire_date = EXCLUDED.expire_date,
    deleted = EXCLUDED.deleted,
    pinned = EXCLUDED.pinned,
    pinned_to_top_order = EXCLUDED.pinned_to_top_order,
    public = EXCLUDED.public,
    close_friends = EXCLUDED.close_friends,
    contacts = EXCLUDED.contacts,
    selected_contacts = EXCLUDED.selected_contacts,
    noforwards = EXCLUDED.noforwards,
    edited = EXCLUDED.edited,
    privacy_rules = EXCLUDED.privacy_rules,
    allow_user_ids = EXCLUDED.allow_user_ids,
    disallow_user_ids = EXCLUDED.disallow_user_ids,
    caption = EXCLUDED.caption,
    entities = EXCLUDED.entities,
    media = EXCLUDED.media,
    media_areas = EXCLUDED.media_areas,
    fwd_from = EXCLUDED.fwd_from,
    updated_at = now()
RETURNING `+storyReturningColumns,
		string(story.Owner.Type), story.Owner.ID, int32(story.ID), story.RandomID, int32(story.Date), int32(story.ExpireDate),
		story.Deleted, story.Pinned, story.PinnedToTopOrder, story.Public, story.CloseFriends, story.Contacts, story.SelectedContacts,
		story.NoForwards, story.Edited, privacyRules, nonNullInt64s(story.AllowUserIDs), nonNullInt64s(story.DisallowUserIDs), story.Caption, entities, media, mediaAreas, forward)
	out, err := scanPGStory(row, 0)
	if err != nil {
		return domain.Story{}, fmt.Errorf("upsert story: %w", err)
	}
	return out, nil
}

func (s *StoryStore) ListActiveStories(ctx context.Context, viewerUserID int64, hidden bool, now, limit int) (domain.StoryList, error) {
	return s.ListActiveStoriesPage(ctx, viewerUserID, hidden, now, domain.StoryListCursor{}, limit)
}

func (s *StoryStore) ListActiveStoriesPage(ctx context.Context, viewerUserID int64, hidden bool, now int, cursor domain.StoryListCursor, limit int) (domain.StoryList, error) {
	if viewerUserID == 0 {
		return domain.StoryList{}, nil
	}
	limit = clampPGStoryLimit(limit)
	total, err := s.countActiveStoryPeers(ctx, viewerUserID, hidden, now)
	if err != nil {
		return domain.StoryList{}, err
	}
	owners, err := s.listActiveStoryPeerPage(ctx, viewerUserID, hidden, now, cursor, limit+1)
	if err != nil {
		return domain.StoryList{}, err
	}
	hasMore := len(owners) > limit
	if hasMore {
		owners = owners[:limit]
	}
	if len(owners) == 0 {
		return domain.StoryList{Count: total}, nil
	}
	stories, err := s.listActiveStoriesForPeers(ctx, viewerUserID, now, owners)
	if err != nil {
		return domain.StoryList{}, err
	}
	if err := s.populateStoryViewState(ctx, viewerUserID, stories); err != nil {
		return domain.StoryList{}, err
	}
	if err := s.recordStoryExposures(ctx, viewerUserID, stories); err != nil {
		return domain.StoryList{}, err
	}
	reads, err := s.ListReadStates(ctx, viewerUserID)
	if err != nil {
		return domain.StoryList{}, err
	}
	peers := groupPGPeerStories(stories, reads)
	return domain.StoryList{
		Count:   total,
		HasMore: hasMore,
		Stories: stories,
		Peers:   peers,
	}, nil
}

func (s *StoryStore) ActiveStoriesDigest(ctx context.Context, viewerUserID int64, hidden bool, now int) (domain.StoryListDigest, error) {
	if viewerUserID == 0 {
		return domain.StoryListDigest{}, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
LEFT JOIN story_hidden_peers h
  ON h.viewer_user_id = $1
 AND h.owner_peer_type = s.owner_peer_type
 AND h.owner_peer_id = s.owner_peer_id
WHERE s.deleted = false
  AND s.expire_date > $2
  AND `+storyVisiblePredicate("$1")+`
  AND (($3::boolean AND h.viewer_user_id IS NOT NULL) OR (NOT $3::boolean AND h.viewer_user_id IS NULL))
ORDER BY s.date DESC, s.owner_peer_type ASC, s.owner_peer_id ASC, s.story_id DESC`, viewerUserID, int32(now), hidden)
	if err != nil {
		return domain.StoryListDigest{}, fmt.Errorf("digest active stories: %w", err)
	}
	stories, err := scanPGStories(rows, viewerUserID)
	if err != nil {
		return domain.StoryListDigest{}, err
	}
	if len(stories) == 0 {
		return domain.StoryListDigest{}, nil
	}
	if err := s.populateStoryViewState(ctx, viewerUserID, stories); err != nil {
		return domain.StoryListDigest{}, err
	}
	reads, err := s.ListReadStates(ctx, viewerUserID)
	if err != nil {
		return domain.StoryListDigest{}, err
	}
	return domain.DigestStoryPeerList(groupPGPeerStories(stories, reads)), nil
}

type activeStoryPeerOwner struct {
	peer    domain.Peer
	maxDate int
}

func (s *StoryStore) countActiveStoryPeers(ctx context.Context, viewerUserID int64, hidden bool, now int) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)
FROM (
  SELECT s.owner_peer_type, s.owner_peer_id
  FROM stories s
  LEFT JOIN story_hidden_peers h
    ON h.viewer_user_id = $1
   AND h.owner_peer_type = s.owner_peer_type
   AND h.owner_peer_id = s.owner_peer_id
  WHERE s.deleted = false
    AND s.expire_date > $2
    AND `+storyVisiblePredicate("$1")+`
    AND (($3::boolean AND h.viewer_user_id IS NOT NULL) OR (NOT $3::boolean AND h.viewer_user_id IS NULL))
  GROUP BY s.owner_peer_type, s.owner_peer_id
) peers`, viewerUserID, int32(now), hidden).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active story peers: %w", err)
	}
	return count, nil
}

func (s *StoryStore) listActiveStoryPeerPage(ctx context.Context, viewerUserID int64, hidden bool, now int, cursor domain.StoryListCursor, limit int) ([]activeStoryPeerOwner, error) {
	limit = clampPGStoryProbeLimit(limit)
	args := []any{viewerUserID, int32(now), hidden}
	having := ""
	if cursor.Set {
		args = append(args, int32(cursor.Date), string(cursor.Peer.Type), cursor.Peer.ID)
		having = `
HAVING MAX(s.date) < $4
    OR (MAX(s.date) = $4 AND (s.owner_peer_type > $5 OR (s.owner_peer_type = $5 AND s.owner_peer_id > $6)))`
	}
	args = append(args, int32(limit))
	limitParam := len(args)
	rows, err := s.db.Query(ctx, `
SELECT s.owner_peer_type, s.owner_peer_id, MAX(s.date)::int AS max_date
FROM stories s
LEFT JOIN story_hidden_peers h
  ON h.viewer_user_id = $1
 AND h.owner_peer_type = s.owner_peer_type
 AND h.owner_peer_id = s.owner_peer_id
WHERE s.deleted = false
  AND s.expire_date > $2
  AND `+storyVisiblePredicate("$1")+`
  AND (($3::boolean AND h.viewer_user_id IS NOT NULL) OR (NOT $3::boolean AND h.viewer_user_id IS NULL))
GROUP BY s.owner_peer_type, s.owner_peer_id`+having+`
ORDER BY max_date DESC, s.owner_peer_type ASC, s.owner_peer_id ASC
LIMIT $`+fmt.Sprint(limitParam), args...)
	if err != nil {
		return nil, fmt.Errorf("list active story peer page: %w", err)
	}
	defer rows.Close()
	owners := make([]activeStoryPeerOwner, 0, limit)
	for rows.Next() {
		var peerType string
		var owner activeStoryPeerOwner
		if err := rows.Scan(&peerType, &owner.peer.ID, &owner.maxDate); err != nil {
			return nil, fmt.Errorf("scan active story peer page: %w", err)
		}
		owner.peer.Type = domain.PeerType(peerType)
		owners = append(owners, owner)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active story peer page: %w", err)
	}
	return owners, nil
}

func (s *StoryStore) listActiveStoriesForPeers(ctx context.Context, viewerUserID int64, now int, owners []activeStoryPeerOwner) ([]domain.Story, error) {
	args := []any{viewerUserID, int32(now)}
	clauses := make([]string, 0, len(owners))
	for _, owner := range owners {
		args = append(args, string(owner.peer.Type), owner.peer.ID)
		clauses = append(clauses, fmt.Sprintf("(s.owner_peer_type = $%d AND s.owner_peer_id = $%d)", len(args)-1, len(args)))
	}
	rows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.deleted = false
  AND s.expire_date > $2
  AND `+storyVisiblePredicate("$1")+`
  AND (`+strings.Join(clauses, " OR ")+`)
ORDER BY s.date DESC, s.owner_peer_type ASC, s.owner_peer_id ASC, s.story_id DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("list active stories for peers: %w", err)
	}
	stories, err := scanPGStories(rows, viewerUserID)
	if err != nil {
		return nil, err
	}
	return stories, nil
}

func (s *StoryStore) ListOwnerActiveStories(ctx context.Context, owner domain.Peer, now, limit int) (domain.StoryList, error) {
	if err := validatePGStoryPeer(owner); err != nil {
		return domain.StoryList{}, err
	}
	limit = clampPGStoryLimit(limit)
	rows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.deleted = false
  AND s.expire_date > $3
ORDER BY s.story_id ASC
LIMIT $4`, string(owner.Type), owner.ID, int32(now), int32(limit))
	if err != nil {
		return domain.StoryList{}, fmt.Errorf("list owner active stories: %w", err)
	}
	stories, err := scanPGStories(rows, 0)
	if err != nil {
		return domain.StoryList{}, err
	}
	for i := range stories {
		stories[i] = fanoutPGStorySnapshot(stories[i])
	}
	return domain.StoryList{Count: len(stories), Stories: stories}, nil
}

func (s *StoryStore) GetPeerStories(ctx context.Context, viewerUserID int64, peer domain.Peer, now int) (domain.PeerStories, error) {
	if err := validatePGStoryPeer(peer); err != nil {
		return domain.PeerStories{}, err
	}
	rows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.deleted = false
  AND s.expire_date > $3
  AND `+storyVisiblePredicate("$4")+`
ORDER BY s.story_id ASC`, string(peer.Type), peer.ID, int32(now), viewerUserID)
	if err != nil {
		return domain.PeerStories{}, fmt.Errorf("get peer stories: %w", err)
	}
	stories, err := scanPGStories(rows, viewerUserID)
	if err != nil {
		return domain.PeerStories{}, err
	}
	if err := s.populateStoryViewState(ctx, viewerUserID, stories); err != nil {
		return domain.PeerStories{}, err
	}
	if err := s.recordStoryExposures(ctx, viewerUserID, stories); err != nil {
		return domain.PeerStories{}, err
	}
	read, err := s.getReadState(ctx, viewerUserID, peer)
	if err != nil {
		return domain.PeerStories{}, err
	}
	return domain.PeerStories{Peer: peer, MaxReadID: read.MaxReadID, Stories: stories}, nil
}

func (s *StoryStore) GetStoriesByID(ctx context.Context, viewerUserID int64, peer domain.Peer, ids []int, now int) (domain.StoryList, error) {
	_ = now
	if err := validatePGStoryPeer(peer); err != nil {
		return domain.StoryList{}, err
	}
	ids, err := normalizePGStoryIDsNonEmpty(ids)
	if err != nil {
		return domain.StoryList{}, err
	}
	rows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.story_id = ANY($3::int[])
  AND s.deleted = false
  AND `+storyVisiblePredicate("$4")+`
ORDER BY array_position($3::int[], s.story_id)`, string(peer.Type), peer.ID, int32s(ids), viewerUserID)
	if err != nil {
		return domain.StoryList{}, fmt.Errorf("get stories by id: %w", err)
	}
	stories, err := scanPGStories(rows, viewerUserID)
	if err != nil {
		return domain.StoryList{}, err
	}
	if err := s.populateStoryViewState(ctx, viewerUserID, stories); err != nil {
		return domain.StoryList{}, err
	}
	if err := s.recordStoryExposures(ctx, viewerUserID, stories); err != nil {
		return domain.StoryList{}, err
	}
	return domain.StoryList{Count: len(stories), Stories: stories}, nil
}

func (s *StoryStore) ListPinnedStories(ctx context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit, now int) (domain.StoryList, error) {
	_ = now
	if err := validatePGStoryPeer(peer); err != nil {
		return domain.StoryList{}, err
	}
	if offsetID < 0 {
		offsetID = 0
	}
	limit = clampPGStoryLimit(limit)
	var count int
	var pinnedToTop32 []int32
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int,
       COALESCE(
         array_agg(s.story_id ORDER BY s.pinned_to_top_order ASC, s.story_id DESC)
           FILTER (WHERE s.pinned_to_top_order > 0),
         '{}'::int[]
       )
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.deleted = false
  AND s.pinned = true
  AND `+storyVisiblePredicate("$3"), string(peer.Type), peer.ID, viewerUserID).Scan(&count, &pinnedToTop32); err != nil {
		return domain.StoryList{}, fmt.Errorf("summarize pinned stories: %w", err)
	}
	pinnedToTop := make([]int, 0, len(pinnedToTop32))
	for _, id := range pinnedToTop32 {
		pinnedToTop = append(pinnedToTop, int(id))
	}
	if count == 0 {
		return domain.StoryList{Count: 0, PinnedToTop: pinnedToTop}, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.deleted = false
  AND s.pinned = true
  AND ($3::int = 0 OR s.story_id < $3)
  AND `+storyVisiblePredicate("$5")+`
ORDER BY s.story_id DESC
LIMIT $4`, string(peer.Type), peer.ID, int32(offsetID), int32(limit), viewerUserID)
	if err != nil {
		return domain.StoryList{}, fmt.Errorf("list pinned stories: %w", err)
	}
	stories, err := scanPGStories(rows, viewerUserID)
	if err != nil {
		return domain.StoryList{}, err
	}
	if err := s.populateStoryViewState(ctx, viewerUserID, stories); err != nil {
		return domain.StoryList{}, err
	}
	if err := s.recordStoryExposures(ctx, viewerUserID, stories); err != nil {
		return domain.StoryList{}, err
	}
	return domain.StoryList{Count: count, Stories: stories, PinnedToTop: pinnedToTop}, nil
}

func (s *StoryStore) HasPinnedStories(ctx context.Context, viewerUserID int64, peer domain.Peer, now int) (bool, error) {
	_ = now
	if err := validatePGStoryPeer(peer); err != nil {
		return false, err
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM stories s
  WHERE s.owner_peer_type = $1
    AND s.owner_peer_id = $2
    AND s.deleted = false
    AND s.pinned = true
    AND `+storyVisiblePredicate("$3")+`
)`, string(peer.Type), peer.ID, viewerUserID).Scan(&exists); err != nil {
		return false, fmt.Errorf("has pinned stories: %w", err)
	}
	return exists, nil
}

func (s *StoryStore) ListStoriesArchive(ctx context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit, now int) (domain.StoryList, error) {
	if err := validatePGStoryPeer(peer); err != nil {
		return domain.StoryList{}, err
	}
	if offsetID < 0 {
		offsetID = 0
	}
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT count(*)::int
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.deleted = false
  AND s.expire_date <= $3`, string(peer.Type), peer.ID, int32(now)).Scan(&count); err != nil {
		return domain.StoryList{}, fmt.Errorf("count story archive: %w", err)
	}
	if limit == 0 {
		return domain.StoryList{Count: count}, nil
	}
	limit = clampPGStoryLimit(limit)
	rows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.deleted = false
  AND s.expire_date <= $3
  AND ($4::int = 0 OR s.story_id < $4)
ORDER BY s.story_id DESC
LIMIT $5`, string(peer.Type), peer.ID, int32(now), int32(offsetID), int32(limit))
	if err != nil {
		return domain.StoryList{}, fmt.Errorf("list story archive: %w", err)
	}
	stories, err := scanPGStories(rows, viewerUserID)
	if err != nil {
		return domain.StoryList{}, err
	}
	if err := s.populateStoryViewState(ctx, viewerUserID, stories); err != nil {
		return domain.StoryList{}, err
	}
	if err := s.recordStoryExposures(ctx, viewerUserID, stories); err != nil {
		return domain.StoryList{}, err
	}
	return domain.StoryList{Count: count, Stories: stories}, nil
}

func (s *StoryStore) ListReadStates(ctx context.Context, viewerUserID int64) ([]domain.StoryReadState, error) {
	if viewerUserID == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `
SELECT viewer_user_id, owner_peer_type, owner_peer_id, max_read_id, date
FROM story_read_states
WHERE viewer_user_id = $1
ORDER BY owner_peer_type ASC, owner_peer_id ASC`, viewerUserID)
	if err != nil {
		return nil, fmt.Errorf("list story read states: %w", err)
	}
	defer rows.Close()
	out := make([]domain.StoryReadState, 0)
	for rows.Next() {
		var state domain.StoryReadState
		var peerType string
		if err := rows.Scan(&state.ViewerID, &peerType, &state.Peer.ID, &state.MaxReadID, &state.Date); err != nil {
			return nil, fmt.Errorf("scan story read state: %w", err)
		}
		state.Peer.Type = domain.PeerType(peerType)
		out = append(out, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan story read states: %w", err)
	}
	return out, nil
}

func (s *StoryStore) GetPeerMaxIDs(ctx context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.RecentStory, error) {
	if len(peers) > domain.MaxStoryIDs {
		return nil, domain.ErrStoryIDInvalid
	}
	if len(peers) == 0 {
		return nil, nil
	}
	peerTypes := make([]string, 0, len(peers))
	peerIDs := make([]int64, 0, len(peers))
	for _, peer := range peers {
		if err := validatePGStoryPeer(peer); err != nil {
			return nil, err
		}
		peerTypes = append(peerTypes, string(peer.Type))
		peerIDs = append(peerIDs, peer.ID)
	}
	rows, err := s.db.Query(ctx, `
WITH input AS (
  SELECT p.peer_type, i.peer_id, p.ord
  FROM unnest($1::text[]) WITH ORDINALITY AS p(peer_type, ord)
  JOIN unnest($2::bigint[]) WITH ORDINALITY AS i(peer_id, ord) USING (ord)
)
SELECT input.peer_type, input.peer_id, COALESCE(MAX(s.story_id), 0)::int
FROM input
LEFT JOIN stories s
  ON s.owner_peer_type = input.peer_type
 AND s.owner_peer_id = input.peer_id
 AND s.deleted = false
 AND s.expire_date > $3
 AND `+storyVisiblePredicate("$4")+`
GROUP BY input.ord, input.peer_type, input.peer_id
ORDER BY input.ord ASC`, peerTypes, peerIDs, int32(now), viewerUserID)
	if err != nil {
		return nil, fmt.Errorf("get story peer max ids: %w", err)
	}
	defer rows.Close()
	out := make([]domain.RecentStory, 0, len(peers))
	for rows.Next() {
		var peerType string
		var item domain.RecentStory
		if err := rows.Scan(&peerType, &item.Peer.ID, &item.MaxID); err != nil {
			return nil, fmt.Errorf("scan story peer max id: %w", err)
		}
		item.Peer.Type = domain.PeerType(peerType)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan story peer max ids: %w", err)
	}
	return out, nil
}

func (s *StoryStore) GetPeerHiddenStates(ctx context.Context, viewerUserID int64, peers []domain.Peer) (map[domain.Peer]bool, error) {
	if len(peers) > domain.MaxStoryIDs {
		return nil, domain.ErrStoryIDInvalid
	}
	if viewerUserID == 0 {
		return nil, domain.ErrStoryPeerInvalid
	}
	if len(peers) == 0 {
		return map[domain.Peer]bool{}, nil
	}
	peerTypes := make([]string, 0, len(peers))
	peerIDs := make([]int64, 0, len(peers))
	for _, peer := range peers {
		if err := validatePGStoryPeer(peer); err != nil {
			return nil, err
		}
		peerTypes = append(peerTypes, string(peer.Type))
		peerIDs = append(peerIDs, peer.ID)
	}
	rows, err := s.db.Query(ctx, `
WITH input AS (
  SELECT p.peer_type, i.peer_id, p.ord
  FROM unnest($1::text[]) WITH ORDINALITY AS p(peer_type, ord)
  JOIN unnest($2::bigint[]) WITH ORDINALITY AS i(peer_id, ord) USING (ord)
)
SELECT input.peer_type, input.peer_id, (h.viewer_user_id IS NOT NULL) AS hidden
FROM input
LEFT JOIN story_hidden_peers h
  ON h.viewer_user_id = $3
 AND h.owner_peer_type = input.peer_type
 AND h.owner_peer_id = input.peer_id
ORDER BY input.ord ASC`, peerTypes, peerIDs, viewerUserID)
	if err != nil {
		return nil, fmt.Errorf("get story peer hidden states: %w", err)
	}
	defer rows.Close()
	out := make(map[domain.Peer]bool, len(peers))
	for rows.Next() {
		var peerType string
		var peer domain.Peer
		var hidden bool
		if err := rows.Scan(&peerType, &peer.ID, &hidden); err != nil {
			return nil, fmt.Errorf("scan story peer hidden state: %w", err)
		}
		peer.Type = domain.PeerType(peerType)
		out[peer] = hidden
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan story peer hidden states: %w", err)
	}
	return out, nil
}

func (s *StoryStore) GetPeerStoryProjections(ctx context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.PeerStoryProjection, error) {
	if len(peers) > domain.MaxStoryIDs {
		return nil, domain.ErrStoryIDInvalid
	}
	if viewerUserID == 0 {
		return nil, domain.ErrStoryPeerInvalid
	}
	if len(peers) == 0 {
		return nil, nil
	}
	peerTypes := make([]string, 0, len(peers))
	peerIDs := make([]int64, 0, len(peers))
	for _, peer := range peers {
		if err := validatePGStoryPeer(peer); err != nil {
			return nil, err
		}
		peerTypes = append(peerTypes, string(peer.Type))
		peerIDs = append(peerIDs, peer.ID)
	}
	rows, err := s.db.Query(ctx, `
WITH input AS (
  SELECT p.peer_type, i.peer_id, p.ord
  FROM unnest($1::text[]) WITH ORDINALITY AS p(peer_type, ord)
  JOIN unnest($2::bigint[]) WITH ORDINALITY AS i(peer_id, ord) USING (ord)
),
recent AS (
  SELECT input.peer_type, input.peer_id, input.ord, COALESCE(MAX(s.story_id), 0)::int AS max_story_id
  FROM input
  LEFT JOIN stories s
    ON s.owner_peer_type = input.peer_type
   AND s.owner_peer_id = input.peer_id
   AND s.deleted = false
   AND s.expire_date > $3
   AND `+storyVisiblePredicate("$4")+`
  GROUP BY input.ord, input.peer_type, input.peer_id
)
SELECT recent.peer_type, recent.peer_id, recent.max_story_id, (h.viewer_user_id IS NOT NULL) AS hidden
FROM recent
LEFT JOIN story_hidden_peers h
  ON h.viewer_user_id = $4
 AND h.owner_peer_type = recent.peer_type
 AND h.owner_peer_id = recent.peer_id
ORDER BY recent.ord ASC`, peerTypes, peerIDs, int32(now), viewerUserID)
	if err != nil {
		return nil, fmt.Errorf("get story peer projections: %w", err)
	}
	defer rows.Close()
	out := make([]domain.PeerStoryProjection, 0, len(peers))
	for rows.Next() {
		var peerType string
		var item domain.PeerStoryProjection
		if err := rows.Scan(&peerType, &item.Peer.ID, &item.Recent.MaxID, &item.Hidden); err != nil {
			return nil, fmt.Errorf("scan story peer projection: %w", err)
		}
		item.Peer.Type = domain.PeerType(peerType)
		item.Recent.Peer = item.Peer
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan story peer projections: %w", err)
	}
	return out, nil
}

func (s *StoryStore) MarkRead(ctx context.Context, viewerUserID int64, peer domain.Peer, maxID, date int) (domain.StoryReadResult, error) {
	if viewerUserID == 0 {
		return domain.StoryReadResult{}, domain.ErrStoryPeerInvalid
	}
	if err := validatePGStoryIdentity(peer, maxID); err != nil {
		return domain.StoryReadResult{}, err
	}
	var gotMax, gotDate int
	var advanced bool
	err := s.db.QueryRow(ctx, `
WITH existing AS (
  SELECT max_read_id
  FROM story_read_states
  WHERE viewer_user_id = $1
    AND owner_peer_type = $2
    AND owner_peer_id = $3
),
upsert AS (
  INSERT INTO story_read_states (viewer_user_id, owner_peer_type, owner_peer_id, max_read_id, date)
  VALUES ($1, $2, $3, $4, $5)
  ON CONFLICT (viewer_user_id, owner_peer_type, owner_peer_id) DO UPDATE SET
    max_read_id = GREATEST(story_read_states.max_read_id, EXCLUDED.max_read_id),
    date = CASE
      WHEN EXCLUDED.max_read_id > story_read_states.max_read_id THEN EXCLUDED.date
      ELSE story_read_states.date
    END,
    updated_at = CASE
      WHEN EXCLUDED.max_read_id > story_read_states.max_read_id THEN now()
      ELSE story_read_states.updated_at
    END
  RETURNING max_read_id, date
)
SELECT upsert.max_read_id, upsert.date, COALESCE((SELECT max_read_id FROM existing), 0) < $4
FROM upsert`, viewerUserID, string(peer.Type), peer.ID, int32(maxID), int32(date)).Scan(&gotMax, &gotDate, &advanced)
	if err != nil {
		return domain.StoryReadResult{}, fmt.Errorf("mark story read: %w", err)
	}
	return domain.StoryReadResult{ViewerID: viewerUserID, Peer: peer, MaxReadID: gotMax, Advanced: advanced, Date: gotDate}, nil
}

func (s *StoryStore) IncrementViews(ctx context.Context, viewerUserID int64, peer domain.Peer, ids []int, date int) (int, error) {
	if viewerUserID == 0 {
		return 0, domain.ErrStoryPeerInvalid
	}
	if err := validatePGStoryPeer(peer); err != nil {
		return 0, err
	}
	ids, err := normalizePGStoryIDsNonEmpty(ids)
	if err != nil {
		return 0, err
	}
	if peer.IsSelfUser(viewerUserID) {
		return 0, nil
	}
	var created int
	if err := s.db.QueryRow(ctx, `
WITH input AS (
  SELECT DISTINCT unnest($5::int[]) AS story_id
),
visible AS (
  SELECT s.story_id
  FROM stories s
  JOIN input i ON i.story_id = s.story_id
  WHERE s.owner_peer_type = $2
    AND s.owner_peer_id = $3
    AND s.deleted = false
    AND (s.expire_date > $4 OR s.pinned)
    AND `+storyVisiblePredicate("$1")+`
),
inserted AS (
  INSERT INTO story_views (owner_peer_type, owner_peer_id, story_id, viewer_user_id, date)
  SELECT $2, $3, visible.story_id, $1, $4
  FROM visible
  ON CONFLICT DO NOTHING
  RETURNING story_id
)
SELECT count(*)::int FROM inserted`, viewerUserID, string(peer.Type), peer.ID, int32(date), int32s(ids)).Scan(&created); err != nil {
		return 0, fmt.Errorf("increment story views: %w", err)
	}
	return created, nil
}

func (s *StoryStore) SetReaction(ctx context.Context, viewerUserID int64, peer domain.Peer, storyID int, reaction *domain.MessageReaction, date int) (domain.StoryReactionResult, error) {
	if viewerUserID == 0 {
		return domain.StoryReactionResult{}, domain.ErrStoryPeerInvalid
	}
	if err := validatePGStoryIdentity(peer, storyID); err != nil {
		return domain.StoryReactionResult{}, err
	}
	if peer.IsSelfUser(viewerUserID) {
		return domain.StoryReactionResult{}, domain.ErrStoryPeerInvalid
	}
	encodedReaction, err := encodeStoryReaction(reaction)
	if err != nil {
		return domain.StoryReactionResult{}, err
	}
	story, err := s.getVisibleStory(ctx, viewerUserID, peer, storyID)
	if err != nil {
		return domain.StoryReactionResult{}, err
	}
	if !story.Interactable(date) {
		return domain.StoryReactionResult{}, domain.ErrStoryNotFound
	}
	var priorRaw string
	var priorDate int
	priorErr := s.db.QueryRow(ctx, `
SELECT COALESCE(reaction::text, '{}')::text, date
FROM story_views
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = $3
  AND viewer_user_id = $4`, string(peer.Type), peer.ID, int32(storyID), viewerUserID).Scan(&priorRaw, &priorDate)
	if priorErr != nil && !errors.Is(priorErr, pgx.ErrNoRows) {
		return domain.StoryReactionResult{}, fmt.Errorf("load prior story reaction: %w", priorErr)
	}
	priorExists := priorErr == nil
	prior, err := decodeStoryReaction(priorRaw)
	if err != nil {
		return domain.StoryReactionResult{}, fmt.Errorf("decode prior story reaction: %w", err)
	}
	changed := !samePGReaction(prior, reaction)
	resultDate := date
	if priorExists && !changed {
		resultDate = priorDate
	} else if _, err := s.db.Exec(ctx, `
INSERT INTO story_views (owner_peer_type, owner_peer_id, story_id, viewer_user_id, date, reaction)
VALUES ($1, $2, $3, $4, $5, $6::jsonb)
ON CONFLICT (owner_peer_type, owner_peer_id, story_id, viewer_user_id) DO UPDATE SET
  date = EXCLUDED.date,
  reaction = EXCLUDED.reaction,
  updated_at = now()`, string(peer.Type), peer.ID, int32(storyID), viewerUserID, int32(date), encodedReaction); err != nil {
		return domain.StoryReactionResult{}, fmt.Errorf("set story reaction: %w", err)
	}
	stories := []domain.Story{story}
	if err := s.populateStoryViewState(ctx, viewerUserID, stories); err != nil {
		return domain.StoryReactionResult{}, err
	}
	return domain.StoryReactionResult{
		ViewerID: viewerUserID,
		Peer:     peer,
		StoryID:  storyID,
		Reaction: clonePGReactionPtr(reaction),
		Story:    stories[0],
		Changed:  changed,
		Date:     resultDate,
	}, nil
}

func (s *StoryStore) ListStoryViews(ctx context.Context, req domain.StoryViewListRequest) (domain.StoryViewList, error) {
	if req.ViewerUserID == 0 {
		return domain.StoryViewList{}, domain.ErrStoryPeerInvalid
	}
	if err := validatePGStoryIdentity(req.Owner, req.StoryID); err != nil {
		return domain.StoryViewList{}, err
	}
	if err := domain.ValidateStoryInteractionOffset(req.Offset, false); err != nil {
		return domain.StoryViewList{}, err
	}
	if _, err := s.getOwnerStory(ctx, req.Owner, req.StoryID); err != nil {
		return domain.StoryViewList{}, err
	}
	viewsCount, reactionsCount, err := s.storyViewCounts(ctx, req.Owner, req.StoryID)
	if err != nil {
		return domain.StoryViewList{}, err
	}
	forwardsCount, err := s.storyForwardCount(ctx, req.Owner, req.StoryID, req.ViewerUserID)
	if err != nil {
		return domain.StoryViewList{}, err
	}
	limit := clampPGStoryInteractionLimit(req.Limit)
	cursor := parsePGStoryInteractionCursor(req.Offset)
	interactionsFirst := req.ReactionsFirst || req.ForwardsFirst
	query := strings.ToLower(strings.TrimSpace(req.Query))
	querySet := query != ""
	queryLike := "%" + escapeLike(query) + "%"
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT (count(*))::int
FROM story_views sv
JOIN users u ON u.id = sv.viewer_user_id
LEFT JOIN contacts c
  ON c.user_id = $4
 AND c.contact_user_id = sv.viewer_user_id
WHERE sv.owner_peer_type = $1
  AND sv.owner_peer_id = $2
  AND sv.story_id = $3
  AND (NOT $5::boolean OR c.contact_user_id IS NOT NULL)
  AND (
    NOT $6::boolean
    OR lower(COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)) LIKE $7 ESCAPE '\'
    OR lower(COALESCE(c.contact_last_name, u.last_name)) LIKE $7 ESCAPE '\'
    OR lower(trim(COALESCE(NULLIF(c.contact_first_name, ''), u.first_name) || ' ' || COALESCE(c.contact_last_name, u.last_name))) LIKE $7 ESCAPE '\'
    OR lower(u.username) LIKE $7 ESCAPE '\'
    OR lower(c.contact_phone) LIKE $7 ESCAPE '\'
  )`, string(req.Owner.Type), req.Owner.ID, int32(req.StoryID), req.ViewerUserID, req.JustContacts, querySet, queryLike).Scan(&count); err != nil {
		return domain.StoryViewList{}, fmt.Errorf("count story views: %w", err)
	}
	rows, err := s.db.Query(ctx, `
SELECT
  sv.viewer_user_id,
  sv.date,
  COALESCE(sv.reaction::text, '{}')::text,
  false,
  (
    $1 = 'user'
    AND EXISTS (
      SELECT 1
      FROM contact_blocks b
      WHERE b.owner_user_id = $2
        AND b.blocked_user_id = sv.viewer_user_id
    )
  )
FROM story_views sv
JOIN users u ON u.id = sv.viewer_user_id
LEFT JOIN contacts c
  ON c.user_id = $4
 AND c.contact_user_id = sv.viewer_user_id
WHERE sv.owner_peer_type = $1
  AND sv.owner_peer_id = $2
  AND sv.story_id = $3
  AND (NOT $5::boolean OR c.contact_user_id IS NOT NULL)
  AND (
    NOT $6::boolean
    OR lower(COALESCE(NULLIF(c.contact_first_name, ''), u.first_name)) LIKE $7 ESCAPE '\'
    OR lower(COALESCE(c.contact_last_name, u.last_name)) LIKE $7 ESCAPE '\'
    OR lower(trim(COALESCE(NULLIF(c.contact_first_name, ''), u.first_name) || ' ' || COALESCE(c.contact_last_name, u.last_name))) LIKE $7 ESCAPE '\'
    OR lower(u.username) LIKE $7 ESCAPE '\'
    OR lower(c.contact_phone) LIKE $7 ESCAPE '\'
  )
  AND (
    NOT $9::boolean
    OR (CASE WHEN $8::boolean AND sv.reaction <> '{}'::jsonb AND sv.reaction <> 'null'::jsonb THEN 0 WHEN $8::boolean THEN 1 ELSE 0 END) > $10
    OR (
      (CASE WHEN $8::boolean AND sv.reaction <> '{}'::jsonb AND sv.reaction <> 'null'::jsonb THEN 0 WHEN $8::boolean THEN 1 ELSE 0 END) = $10
      AND (sv.date < $11 OR (sv.date = $11 AND sv.viewer_user_id < $12))
    )
  )
ORDER BY
  (CASE WHEN $8::boolean AND sv.reaction <> '{}'::jsonb AND sv.reaction <> 'null'::jsonb THEN 0 WHEN $8::boolean THEN 1 ELSE 0 END) ASC,
  sv.date DESC,
  sv.viewer_user_id DESC
LIMIT $13`, string(req.Owner.Type), req.Owner.ID, int32(req.StoryID), req.ViewerUserID, req.JustContacts, querySet, queryLike, interactionsFirst, cursor.set, int32(cursor.group), int32(cursor.date), cursor.viewerID, int32(limit+1))
	if err != nil {
		return domain.StoryViewList{}, fmt.Errorf("list story views: %w", err)
	}
	views, err := scanPGStoryViews(rows, req.Owner, req.StoryID)
	if err != nil {
		return domain.StoryViewList{}, err
	}
	if !querySet && !req.JustContacts {
		reposts, err := s.listStoryRepostViews(ctx, req, limit+1, cursor)
		if err != nil {
			return domain.StoryViewList{}, err
		}
		views = append(views, reposts...)
	}
	sortPGStoryViewsForList(views, req.ReactionsFirst, req.ForwardsFirst)
	nextOffset := ""
	if len(views) > limit {
		views = views[:limit]
		nextOffset = formatPGStoryInteractionCursor(views[len(views)-1], req.ReactionsFirst, req.ForwardsFirst)
	}
	if !querySet && !req.JustContacts {
		count += forwardsCount
	}
	return domain.StoryViewList{
		Count:          count,
		ViewsCount:     viewsCount,
		ForwardsCount:  forwardsCount,
		ReactionsCount: reactionsCount,
		Views:          views,
		NextOffset:     nextOffset,
	}, nil
}

func (s *StoryStore) ListStoryReactions(ctx context.Context, req domain.StoryReactionListRequest) (domain.StoryReactionList, error) {
	if req.ViewerUserID == 0 {
		return domain.StoryReactionList{}, domain.ErrStoryPeerInvalid
	}
	if err := validatePGStoryIdentity(req.Owner, req.StoryID); err != nil {
		return domain.StoryReactionList{}, err
	}
	if err := domain.ValidateStoryReactionInteractionOffset(req.Offset, req.ForwardsFirst); err != nil {
		return domain.StoryReactionList{}, err
	}
	if _, err := s.getOwnerStory(ctx, req.Owner, req.StoryID); err != nil {
		return domain.StoryReactionList{}, err
	}
	filterSet := req.Reaction != nil
	reactionFilter, err := encodeStoryReaction(req.Reaction)
	if err != nil {
		return domain.StoryReactionList{}, err
	}
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT (count(*))::int
FROM story_views
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = $3
  AND reaction <> '{}'::jsonb
  AND reaction <> 'null'::jsonb
  AND (NOT $4::boolean OR reaction = $5::jsonb)`,
		string(req.Owner.Type), req.Owner.ID, int32(req.StoryID), filterSet, reactionFilter).Scan(&count); err != nil {
		return domain.StoryReactionList{}, fmt.Errorf("count story reactions: %w", err)
	}
	if !filterSet {
		forwardsCount, err := s.storyForwardCount(ctx, req.Owner, req.StoryID, req.ViewerUserID)
		if err != nil {
			return domain.StoryReactionList{}, err
		}
		count += forwardsCount
	}
	limit := clampPGStoryInteractionLimit(req.Limit)
	cursor := parsePGStoryInteractionCursor(req.Offset)
	reactionGroup := pgStoryViewSortGroup(domain.StoryView{Reaction: &domain.MessageReaction{}}, false, req.ForwardsFirst)
	args := []any{string(req.Owner.Type), req.Owner.ID, int32(req.StoryID), filterSet, reactionFilter, int32(limit + 1)}
	cursorClause := ""
	if cursor.set {
		args = append(args, int32(reactionGroup), int32(cursor.group), int32(cursor.date), cursor.viewerID)
		cursorClause = `
  AND (
    $7::int > $8::int
    OR (
      $7::int = $8::int
      AND (
        date < $9::int
        OR (date = $9::int AND viewer_user_id < $10::bigint)
      )
    )
  )`
	}
	rows, err := s.db.Query(ctx, `
SELECT viewer_user_id, date, COALESCE(reaction::text, '{}')::text, false, false
FROM story_views
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = $3
  AND reaction <> '{}'::jsonb
  AND reaction <> 'null'::jsonb
  AND (NOT $4::boolean OR reaction = $5::jsonb)
`+cursorClause+`
ORDER BY date DESC, viewer_user_id DESC
LIMIT $6`, args...)
	if err != nil {
		return domain.StoryReactionList{}, fmt.Errorf("list story reactions: %w", err)
	}
	reactions, err := scanPGStoryViews(rows, req.Owner, req.StoryID)
	if err != nil {
		return domain.StoryReactionList{}, err
	}
	if !filterSet {
		reposts, err := s.listStoryRepostViews(ctx, domain.StoryViewListRequest{
			ViewerUserID:  req.ViewerUserID,
			Owner:         req.Owner,
			StoryID:       req.StoryID,
			ForwardsFirst: req.ForwardsFirst,
		}, limit+1, cursor)
		if err != nil {
			return domain.StoryReactionList{}, err
		}
		reactions = append(reactions, reposts...)
	}
	sortPGStoryViewsForList(reactions, false, req.ForwardsFirst)
	nextOffset := ""
	if len(reactions) > limit {
		reactions = reactions[:limit]
		nextOffset = formatPGStoryInteractionCursor(reactions[len(reactions)-1], false, req.ForwardsFirst)
	}
	return domain.StoryReactionList{Count: count, Reactions: reactions, NextOffset: nextOffset}, nil
}

func (s *StoryStore) ListStoryPublicForwards(ctx context.Context, req domain.StoryPublicForwardListRequest) (domain.StoryPublicForwardList, error) {
	if req.ViewerUserID == 0 {
		return domain.StoryPublicForwardList{}, domain.ErrStoryPeerInvalid
	}
	if err := validatePGStoryIdentity(req.Owner, req.StoryID); err != nil {
		return domain.StoryPublicForwardList{}, err
	}
	if err := domain.ValidateStoryInteractionOffset(req.Offset, false); err != nil {
		return domain.StoryPublicForwardList{}, err
	}
	if _, err := s.getOwnerStory(ctx, req.Owner, req.StoryID); err != nil {
		return domain.StoryPublicForwardList{}, err
	}
	count, err := s.storyForwardCount(ctx, req.Owner, req.StoryID, req.ViewerUserID)
	if err != nil {
		return domain.StoryPublicForwardList{}, err
	}
	limit := clampPGStoryInteractionLimit(req.Limit)
	cursor := parsePGStoryInteractionCursor(req.Offset)
	reposts, err := s.listStoryRepostViews(ctx, domain.StoryViewListRequest{
		ViewerUserID:  req.ViewerUserID,
		Owner:         req.Owner,
		StoryID:       req.StoryID,
		Offset:        req.Offset,
		Limit:         req.Limit,
		ForwardsFirst: true,
	}, limit+1, cursor)
	if err != nil {
		return domain.StoryPublicForwardList{}, err
	}
	sortPGStoryViewsForList(reposts, false, true)
	nextOffset := ""
	if len(reposts) > limit {
		reposts = reposts[:limit]
		nextOffset = formatPGStoryInteractionCursor(reposts[len(reposts)-1], false, true)
	}
	return domain.StoryPublicForwardList{Count: count, Forwards: reposts, NextOffset: nextOffset}, nil
}

func (s *StoryStore) ListStoryViewerIDs(ctx context.Context, owner domain.Peer, storyID, limit int) ([]int64, error) {
	if err := validatePGStoryIdentity(owner, storyID); err != nil {
		return nil, err
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM stories
  WHERE owner_peer_type = $1
    AND owner_peer_id = $2
    AND story_id = $3
)`, string(owner.Type), owner.ID, int32(storyID)).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check story for viewer ids: %w", err)
	}
	if !exists {
		return nil, domain.ErrStoryNotFound
	}
	limit = clampPGStoryPrivacyFanoutLimit(limit)
	rows, err := s.db.Query(ctx, `
SELECT viewer_user_id
FROM (
  SELECT viewer_user_id
  FROM story_views
  WHERE owner_peer_type = $1
    AND owner_peer_id = $2
    AND story_id = $3
  UNION
  SELECT viewer_user_id
  FROM story_exposures
  WHERE owner_peer_type = $1
    AND owner_peer_id = $2
    AND story_id = $3
) viewers
WHERE viewer_user_id <> 0
ORDER BY viewer_user_id ASC
LIMIT $4`, string(owner.Type), owner.ID, int32(storyID), int32(limit))
	if err != nil {
		return nil, fmt.Errorf("list story viewer ids: %w", err)
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan story viewer id: %w", err)
		}
		if id != 0 {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate story viewer ids: %w", err)
	}
	return ids, nil
}

func (s *StoryStore) recordStoryExposures(ctx context.Context, viewerUserID int64, stories []domain.Story) error {
	if viewerUserID == 0 || len(stories) == 0 {
		return nil
	}
	peerTypes := make([]string, 0, len(stories))
	peerIDs := make([]int64, 0, len(stories))
	storyIDs := make([]int32, 0, len(stories))
	dates := make([]int32, 0, len(stories))
	type exposureKey struct {
		peerType domain.PeerType
		peerID   int64
		storyID  int
	}
	seen := make(map[exposureKey]struct{}, len(stories))
	for _, story := range stories {
		if story.ID <= 0 || story.Owner.ID == 0 {
			continue
		}
		if story.Owner.Type == domain.PeerTypeUser && story.Owner.ID == viewerUserID {
			continue
		}
		key := exposureKey{peerType: story.Owner.Type, peerID: story.Owner.ID, storyID: story.ID}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		peerTypes = append(peerTypes, string(story.Owner.Type))
		peerIDs = append(peerIDs, story.Owner.ID)
		storyIDs = append(storyIDs, int32(story.ID))
		dates = append(dates, int32(story.Date))
	}
	if len(storyIDs) == 0 {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO story_exposures (owner_peer_type, owner_peer_id, story_id, viewer_user_id, date)
SELECT t.owner_peer_type, t.owner_peer_id, t.story_id, $1, t.date
FROM unnest($2::text[], $3::bigint[], $4::int[], $5::int[]) AS t(owner_peer_type, owner_peer_id, story_id, date)
ON CONFLICT (owner_peer_type, owner_peer_id, story_id, viewer_user_id) DO UPDATE SET
  date = GREATEST(story_exposures.date, EXCLUDED.date),
  updated_at = now()`, viewerUserID, peerTypes, peerIDs, storyIDs, dates); err != nil {
		return fmt.Errorf("record story exposures: %w", err)
	}
	return nil
}

func (s *StoryStore) EditStory(ctx context.Context, req domain.StoryEditRequest) (domain.StoryEditResult, error) {
	if err := validatePGStoryIdentity(req.Owner, req.ID); err != nil {
		return domain.StoryEditResult{}, err
	}
	current, err := s.getOwnerStory(ctx, req.Owner, req.ID)
	if err != nil {
		return domain.StoryEditResult{}, err
	}
	updated := clonePGStory(current)
	if req.UpdateMedia {
		updated.Media = req.Media
	}
	if req.UpdateCaption {
		updated.Caption = req.Caption
		updated.Entities = append([]domain.MessageEntity(nil), req.Entities...)
	}
	if req.UpdatePrivacy {
		updated.Public = req.Public
		updated.CloseFriends = req.CloseFriends
		updated.Contacts = req.Contacts
		updated.SelectedContacts = req.SelectedContacts
		updated.PrivacyRules = clonePGPrivacyRules(req.PrivacyRules)
		updated.AllowUserIDs = append([]int64(nil), req.AllowUserIDs...)
		updated.DisallowUserIDs = append([]int64(nil), req.DisallowUserIDs...)
	}
	if req.UpdateMediaAreas {
		updated.MediaAreas = clonePGStoryMediaAreas(req.MediaAreas)
	}
	if reflect.DeepEqual(current, updated) {
		return domain.StoryEditResult{}, domain.ErrStoryNotModified
	}
	updated.Edited = true
	entities, err := encodeMessageEntities(updated.Entities)
	if err != nil {
		return domain.StoryEditResult{}, fmt.Errorf("encode story entities: %w", err)
	}
	media, err := encodeMessageMedia(updated.Media)
	if err != nil {
		return domain.StoryEditResult{}, fmt.Errorf("encode story media: %w", err)
	}
	mediaAreas, err := encodeStoryMediaAreas(updated.MediaAreas)
	if err != nil {
		return domain.StoryEditResult{}, fmt.Errorf("encode story media areas: %w", err)
	}
	privacyRules, err := encodePrivacyRules(updated.PrivacyRules)
	if err != nil {
		return domain.StoryEditResult{}, fmt.Errorf("encode story privacy rules: %w", err)
	}
	row := s.db.QueryRow(ctx, `
UPDATE stories
SET public = $4,
    close_friends = $5,
    contacts = $6,
    selected_contacts = $7,
    edited = true,
    privacy_rules = $8::jsonb,
    allow_user_ids = $9::bigint[],
    disallow_user_ids = $10::bigint[],
    caption = $11,
    entities = $12::jsonb,
    media = $13::jsonb,
    media_areas = $14::jsonb,
    updated_at = now()
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = $3
  AND deleted = false
RETURNING `+storyReturningColumns,
		string(req.Owner.Type), req.Owner.ID, int32(req.ID),
		updated.Public, updated.CloseFriends, updated.Contacts, updated.SelectedContacts,
		privacyRules, nonNullInt64s(updated.AllowUserIDs), nonNullInt64s(updated.DisallowUserIDs),
		updated.Caption, entities, media, mediaAreas)
	out, err := scanPGStory(row, req.Owner.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StoryEditResult{}, domain.ErrStoryNotFound
	}
	if err != nil {
		return domain.StoryEditResult{}, fmt.Errorf("edit story: %w", err)
	}
	return domain.StoryEditResult{Story: out, Previous: clonePGStory(current)}, nil
}

func (s *StoryStore) DeleteStories(ctx context.Context, peer domain.Peer, ids []int, date int) (domain.StoryMutationResult, error) {
	_ = date
	if err := validatePGStoryPeer(peer); err != nil {
		return domain.StoryMutationResult{}, err
	}
	ids, err := normalizePGStoryIDsNonEmpty(ids)
	if err != nil {
		return domain.StoryMutationResult{}, err
	}
	beforeRows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = ANY($3::int[])
  AND deleted = false`, string(peer.Type), peer.ID, int32s(ids))
	if err != nil {
		return domain.StoryMutationResult{}, fmt.Errorf("load stories before delete: %w", err)
	}
	before, err := scanPGStories(beforeRows, peer.ID)
	if err != nil {
		return domain.StoryMutationResult{}, err
	}
	rows, err := s.db.Query(ctx, `
UPDATE stories
SET deleted = true,
    pinned = false,
    pinned_to_top_order = 0,
    updated_at = now()
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = ANY($3::int[])
  AND deleted = false
RETURNING `+storyReturningColumns, string(peer.Type), peer.ID, int32s(ids))
	if err != nil {
		return domain.StoryMutationResult{}, fmt.Errorf("delete stories: %w", err)
	}
	stories, err := scanPGStories(rows, peer.ID)
	if err != nil {
		return domain.StoryMutationResult{}, err
	}
	previousByID := make(map[int]domain.Story, len(before))
	for _, story := range before {
		previousByID[story.ID] = story
	}
	previous := make([]domain.Story, 0, len(stories))
	for _, story := range stories {
		if prev, ok := previousByID[story.ID]; ok {
			previous = append(previous, prev)
		}
	}
	return domain.StoryMutationResult{Peer: peer, IDs: append([]int(nil), ids...), Stories: stories, Previous: previous}, nil
}

func (s *StoryStore) TogglePinned(ctx context.Context, peer domain.Peer, ids []int, pinned bool, date int) (domain.StoryMutationResult, error) {
	_ = date
	if err := validatePGStoryPeer(peer); err != nil {
		return domain.StoryMutationResult{}, err
	}
	ids, err := normalizePGStoryIDs(ids)
	if err != nil {
		return domain.StoryMutationResult{}, err
	}
	beforeRows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = ANY($3::int[])
  AND deleted = false
  AND pinned <> $4`, string(peer.Type), peer.ID, int32s(ids), pinned)
	if err != nil {
		return domain.StoryMutationResult{}, fmt.Errorf("load stories before toggle pinned: %w", err)
	}
	before, err := scanPGStories(beforeRows, peer.ID)
	if err != nil {
		return domain.StoryMutationResult{}, err
	}
	rows, err := s.db.Query(ctx, `
UPDATE stories
SET pinned = $4,
    pinned_to_top_order = CASE WHEN $4::boolean THEN pinned_to_top_order ELSE 0 END,
    updated_at = now()
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = ANY($3::int[])
  AND deleted = false
  AND pinned <> $4
RETURNING `+storyReturningColumns, string(peer.Type), peer.ID, int32s(ids), pinned)
	if err != nil {
		return domain.StoryMutationResult{}, fmt.Errorf("toggle story pinned: %w", err)
	}
	stories, err := scanPGStories(rows, peer.ID)
	if err != nil {
		return domain.StoryMutationResult{}, err
	}
	previousByID := make(map[int]domain.Story, len(before))
	for _, story := range before {
		previousByID[story.ID] = story
	}
	previous := make([]domain.Story, 0, len(stories))
	for _, story := range stories {
		if prev, ok := previousByID[story.ID]; ok {
			previous = append(previous, prev)
		}
	}
	return domain.StoryMutationResult{Peer: peer, IDs: append([]int(nil), ids...), Stories: stories, Previous: previous}, nil
}

func (s *StoryStore) TogglePinnedToTop(ctx context.Context, peer domain.Peer, ids []int) error {
	if err := validatePGStoryPeer(peer); err != nil {
		return err
	}
	ids, err := normalizePGStoryPinnedToTopIDs(ids)
	if err != nil {
		return err
	}
	return withTx(ctx, s.db, "toggle story pinned to top", func(tx pgx.Tx) error {
		if len(ids) > 0 {
			var found int
			if err := tx.QueryRow(ctx, `
SELECT count(*)::int
FROM stories
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = ANY($3::int[])
  AND deleted = false
  AND pinned = true`, string(peer.Type), peer.ID, int32s(ids)).Scan(&found); err != nil {
				return fmt.Errorf("count pinned-to-top candidates: %w", err)
			}
			if found != len(ids) {
				return domain.ErrStoryIDInvalid
			}
		}
		if _, err := tx.Exec(ctx, `
UPDATE stories
SET pinned_to_top_order = 0,
    updated_at = now()
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND pinned_to_top_order <> 0`, string(peer.Type), peer.ID); err != nil {
			return fmt.Errorf("clear pinned-to-top order: %w", err)
		}
		for i, id := range ids {
			tag, err := tx.Exec(ctx, `
UPDATE stories
SET pinned_to_top_order = $4,
    updated_at = now()
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = $3
  AND deleted = false
  AND pinned = true`, string(peer.Type), peer.ID, int32(id), int32(i+1))
			if err != nil {
				return fmt.Errorf("set pinned-to-top order: %w", err)
			}
			if tag.RowsAffected() != 1 {
				return domain.ErrStoryIDInvalid
			}
		}
		return nil
	})
}

func (s *StoryStore) SetPeerHidden(ctx context.Context, viewerUserID int64, peer domain.Peer, hidden bool) error {
	if viewerUserID == 0 {
		return domain.ErrStoryPeerInvalid
	}
	if err := validatePGStoryPeer(peer); err != nil {
		return err
	}
	if hidden {
		_, err := s.db.Exec(ctx, `
INSERT INTO story_hidden_peers (viewer_user_id, owner_peer_type, owner_peer_id)
VALUES ($1, $2, $3)
ON CONFLICT (viewer_user_id, owner_peer_type, owner_peer_id) DO UPDATE SET updated_at = now()`,
			viewerUserID, string(peer.Type), peer.ID)
		if err != nil {
			return fmt.Errorf("set story peer hidden: %w", err)
		}
		return nil
	}
	if _, err := s.db.Exec(ctx, `
DELETE FROM story_hidden_peers
WHERE viewer_user_id = $1
  AND owner_peer_type = $2
  AND owner_peer_id = $3`, viewerUserID, string(peer.Type), peer.ID); err != nil {
		return fmt.Errorf("clear story peer hidden: %w", err)
	}
	return nil
}

func (s *StoryStore) getVisibleStory(ctx context.Context, viewerUserID int64, peer domain.Peer, storyID int) (domain.Story, error) {
	row := s.db.QueryRow(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.story_id = $3
  AND s.deleted = false
  AND `+storyVisiblePredicate("$4"), string(peer.Type), peer.ID, int32(storyID), viewerUserID)
	story, err := scanPGStory(row, viewerUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Story{}, domain.ErrStoryNotFound
	}
	if err != nil {
		return domain.Story{}, fmt.Errorf("get visible story: %w", err)
	}
	return story, nil
}

func (s *StoryStore) storyByRandomID(ctx context.Context, peer domain.Peer, randomID int64) (domain.Story, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.random_id = $3`, string(peer.Type), peer.ID, randomID)
	story, err := scanPGStory(row, peer.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Story{}, false, nil
	}
	if err != nil {
		return domain.Story{}, false, fmt.Errorf("load story by random id: %w", err)
	}
	return story, true, nil
}

func (s *StoryStore) getOwnerStory(ctx context.Context, peer domain.Peer, storyID int) (domain.Story, error) {
	row := s.db.QueryRow(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.owner_peer_type = $1
  AND s.owner_peer_id = $2
  AND s.story_id = $3
  AND s.deleted = false`, string(peer.Type), peer.ID, int32(storyID))
	story, err := scanPGStory(row, peer.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Story{}, domain.ErrStoryNotFound
	}
	if err != nil {
		return domain.Story{}, fmt.Errorf("get owner story: %w", err)
	}
	return story, nil
}

func (s *StoryStore) getReadState(ctx context.Context, viewerUserID int64, peer domain.Peer) (domain.StoryReadState, error) {
	if viewerUserID == 0 {
		return domain.StoryReadState{ViewerID: viewerUserID, Peer: peer}, nil
	}
	var state domain.StoryReadState
	var peerType string
	err := s.db.QueryRow(ctx, `
SELECT viewer_user_id, owner_peer_type, owner_peer_id, max_read_id, date
FROM story_read_states
WHERE viewer_user_id = $1
  AND owner_peer_type = $2
  AND owner_peer_id = $3`, viewerUserID, string(peer.Type), peer.ID).Scan(
		&state.ViewerID, &peerType, &state.Peer.ID, &state.MaxReadID, &state.Date,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.StoryReadState{ViewerID: viewerUserID, Peer: peer}, nil
	}
	if err != nil {
		return domain.StoryReadState{}, fmt.Errorf("get story read state: %w", err)
	}
	state.Peer.Type = domain.PeerType(peerType)
	return state, nil
}

func (s *StoryStore) populateStoryViewState(ctx context.Context, viewerUserID int64, stories []domain.Story) error {
	if len(stories) == 0 {
		return nil
	}
	peerTypes := make([]string, 0, len(stories))
	peerIDs := make([]int64, 0, len(stories))
	storyIDs := make([]int32, 0, len(stories))
	for _, story := range stories {
		peerTypes = append(peerTypes, string(story.Owner.Type))
		peerIDs = append(peerIDs, story.Owner.ID)
		storyIDs = append(storyIDs, int32(story.ID))
	}
	rows, err := s.db.Query(ctx, `
WITH input AS (
  SELECT p.peer_type, i.peer_id, sid.story_id, p.ord
  FROM unnest($1::text[]) WITH ORDINALITY AS p(peer_type, ord)
  JOIN unnest($2::bigint[]) WITH ORDINALITY AS i(peer_id, ord) USING (ord)
  JOIN unnest($3::int[]) WITH ORDINALITY AS sid(story_id, ord) USING (ord)
)
SELECT input.ord, v.viewer_user_id, v.date, COALESCE(v.reaction::text, '{}')::text
FROM input
JOIN story_views v
  ON v.owner_peer_type = input.peer_type
 AND v.owner_peer_id = input.peer_id
 AND v.story_id = input.story_id
ORDER BY input.ord ASC, v.date DESC, v.viewer_user_id DESC`, peerTypes, peerIDs, storyIDs)
	if err != nil {
		return fmt.Errorf("load story view state: %w", err)
	}
	defer rows.Close()
	for i := range stories {
		stories[i].Views = domain.StoryViews{}
		stories[i].SentReaction = nil
	}
	for rows.Next() {
		var ord int
		var viewerID int64
		var date int
		var reactionRaw string
		if err := rows.Scan(&ord, &viewerID, &date, &reactionRaw); err != nil {
			return fmt.Errorf("scan story view state: %w", err)
		}
		_ = date
		idx := ord - 1
		if idx < 0 || idx >= len(stories) {
			continue
		}
		view := &stories[idx].Views
		view.ViewsCount++
		view.HasViewers = true
		if len(view.RecentViewers) < 3 {
			view.RecentViewers = append(view.RecentViewers, viewerID)
		}
		reaction, err := decodeStoryReaction(reactionRaw)
		if err != nil {
			return fmt.Errorf("decode story view reaction: %w", err)
		}
		if reaction != nil {
			view.ReactionsCount++
			addPGStoryReactionCount(view, *reaction)
		}
		if viewerID == viewerUserID {
			stories[idx].SentReaction = reaction
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scan story view states: %w", err)
	}
	repostRows, err := s.db.Query(ctx, `
WITH input AS (
  SELECT p.peer_type, i.peer_id, sid.story_id, p.ord
  FROM unnest($1::text[]) WITH ORDINALITY AS p(peer_type, ord)
  JOIN unnest($2::bigint[]) WITH ORDINALITY AS i(peer_id, ord) USING (ord)
  JOIN unnest($3::int[]) WITH ORDINALITY AS sid(story_id, ord) USING (ord)
)
SELECT input.ord, COUNT(r.story_id)::int
FROM input
LEFT JOIN stories r
  ON r.deleted = false
 AND r.public = true
 AND `+storyForwardSourceTypeSQL("r")+` = input.peer_type
 AND `+storyForwardSourceIDSQL("r")+` = input.peer_id
 AND (r.fwd_from->>'StoryID')::int = input.story_id
 AND `+storyPublicRepostVisiblePredicateFor("r", "$4")+`
GROUP BY input.ord
ORDER BY input.ord ASC`, peerTypes, peerIDs, storyIDs, viewerUserID)
	if err != nil {
		return fmt.Errorf("load story forward state: %w", err)
	}
	defer repostRows.Close()
	for repostRows.Next() {
		var ord, count int
		if err := repostRows.Scan(&ord, &count); err != nil {
			return fmt.Errorf("scan story forward state: %w", err)
		}
		idx := ord - 1
		if idx < 0 || idx >= len(stories) {
			continue
		}
		stories[idx].Views.ForwardsCount = count
	}
	if err := repostRows.Err(); err != nil {
		return fmt.Errorf("scan story forward states: %w", err)
	}
	return nil
}

func (s *StoryStore) storyViewCounts(ctx context.Context, peer domain.Peer, storyID int) (viewsCount, reactionsCount int, err error) {
	err = s.db.QueryRow(ctx, `
SELECT
  (count(*))::int,
  (count(*) FILTER (WHERE reaction <> '{}'::jsonb AND reaction <> 'null'::jsonb))::int
FROM story_views
WHERE owner_peer_type = $1
  AND owner_peer_id = $2
  AND story_id = $3`, string(peer.Type), peer.ID, int32(storyID)).Scan(&viewsCount, &reactionsCount)
	if err != nil {
		return 0, 0, fmt.Errorf("count story views: %w", err)
	}
	return viewsCount, reactionsCount, nil
}

func (s *StoryStore) storyForwardCount(ctx context.Context, peer domain.Peer, storyID int, viewerUserID int64) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM stories r
WHERE r.deleted = false
  AND r.public = true
  AND `+storyForwardSourceTypeSQL("r")+` = $1
  AND `+storyForwardSourceIDSQL("r")+` = $2
  AND (r.fwd_from->>'StoryID')::int = $3
  AND `+storyPublicRepostVisiblePredicateFor("r", "$4"),
		string(peer.Type), peer.ID, int32(storyID), viewerUserID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count story forwards: %w", err)
	}
	return count, nil
}

func (s *StoryStore) listStoryRepostViews(ctx context.Context, req domain.StoryViewListRequest, limit int, cursor storyInteractionCursor) ([]domain.StoryView, error) {
	limit = clampPGStoryProbeLimit(limit)
	group := pgStoryViewSortGroup(domain.StoryView{Repost: &domain.Story{}}, req.ReactionsFirst, req.ForwardsFirst)
	args := []any{string(req.Owner.Type), req.Owner.ID, int32(req.StoryID), req.ViewerUserID, int32(limit)}
	cursorClause := ""
	if cursor.set {
		args = append(args, int32(group), int32(cursor.group), int32(cursor.date), cursor.viewerID, int32(cursor.messageID))
		cursorClause = fmt.Sprintf(`
  AND (
    $6::int > $7::int
    OR (
      $6::int = $7::int
      AND (
        s.date < $8::int
        OR (
          s.date = $8::int
          AND (
            (CASE WHEN s.owner_peer_type = 'channel' THEN -s.owner_peer_id ELSE s.owner_peer_id END) < $9::bigint
            OR (
              (CASE WHEN s.owner_peer_type = 'channel' THEN -s.owner_peer_id ELSE s.owner_peer_id END) = $9::bigint
              AND 0 < $10::int
            )
          )
        )
      )
    )
  )`)
	}
	rows, err := s.db.Query(ctx, `
SELECT `+storySelectColumns+`
FROM stories s
WHERE s.deleted = false
  AND s.public = true
  AND `+storyForwardSourceTypeSQL("s")+` = $1
  AND `+storyForwardSourceIDSQL("s")+` = $2
  AND (s.fwd_from->>'StoryID')::int = $3
  AND `+storyPublicRepostVisiblePredicateFor("s", "$4")+cursorClause+`
ORDER BY s.date DESC, (CASE WHEN s.owner_peer_type = 'channel' THEN -s.owner_peer_id ELSE s.owner_peer_id END) DESC
LIMIT $5`, args...)
	if err != nil {
		return nil, fmt.Errorf("list story repost views: %w", err)
	}
	reposts, err := scanPGStories(rows, req.ViewerUserID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.StoryView, 0, len(reposts))
	for _, repost := range reposts {
		item := clonePGStory(repost)
		out = append(out, domain.StoryView{
			Owner:    req.Owner,
			StoryID:  req.StoryID,
			ViewerID: pgStoryPeerCursorKey(item.Owner),
			Date:     item.Date,
			Repost:   &item,
		})
	}
	return out, nil
}

func scanPGStoryViews(rows pgx.Rows, owner domain.Peer, storyID int) ([]domain.StoryView, error) {
	defer rows.Close()
	out := make([]domain.StoryView, 0)
	for rows.Next() {
		var view domain.StoryView
		var reactionRaw string
		if err := rows.Scan(
			&view.ViewerID,
			&view.Date,
			&reactionRaw,
			&view.Blocked,
			&view.BlockedMyStoriesFrom,
		); err != nil {
			return nil, fmt.Errorf("scan story view: %w", err)
		}
		reaction, err := decodeStoryReaction(reactionRaw)
		if err != nil {
			return nil, fmt.Errorf("decode story list reaction: %w", err)
		}
		view.Owner = owner
		view.StoryID = storyID
		view.Reaction = reaction
		out = append(out, view)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan story views: %w", err)
	}
	return out, nil
}

func scanPGStories(rows pgx.Rows, viewerUserID int64) ([]domain.Story, error) {
	defer rows.Close()
	stories := make([]domain.Story, 0)
	for rows.Next() {
		story, err := scanPGStory(rows, viewerUserID)
		if err != nil {
			return nil, fmt.Errorf("scan story: %w", err)
		}
		stories = append(stories, story)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan stories: %w", err)
	}
	return stories, nil
}

func scanPGStory(row rowScanner, viewerUserID int64) (domain.Story, error) {
	var story domain.Story
	var peerType string
	var privacyRulesJSON, entitiesJSON, mediaJSON, mediaAreasJSON, forwardJSON string
	if err := row.Scan(
		&peerType,
		&story.Owner.ID,
		&story.ID,
		&story.RandomID,
		&story.Date,
		&story.ExpireDate,
		&story.Deleted,
		&story.Pinned,
		&story.PinnedToTopOrder,
		&story.Public,
		&story.CloseFriends,
		&story.Contacts,
		&story.SelectedContacts,
		&story.NoForwards,
		&story.Edited,
		&privacyRulesJSON,
		&story.AllowUserIDs,
		&story.DisallowUserIDs,
		&story.Caption,
		&entitiesJSON,
		&mediaJSON,
		&mediaAreasJSON,
		&forwardJSON,
	); err != nil {
		return domain.Story{}, err
	}
	story.Owner.Type = domain.PeerType(peerType)
	privacyRules, err := decodePrivacyRulesJSON(privacyRulesJSON)
	if err != nil {
		return domain.Story{}, fmt.Errorf("decode story privacy rules: %w", err)
	}
	story.PrivacyRules = privacyRules
	entities, err := decodeMessageEntities(entitiesJSON)
	if err != nil {
		return domain.Story{}, fmt.Errorf("decode story entities: %w", err)
	}
	story.Entities = entities
	media, err := decodeMessageMedia(mediaJSON)
	if err != nil {
		return domain.Story{}, fmt.Errorf("decode story media: %w", err)
	}
	story.Media = media
	mediaAreas, err := decodeStoryMediaAreas(mediaAreasJSON)
	if err != nil {
		return domain.Story{}, fmt.Errorf("decode story media areas: %w", err)
	}
	story.MediaAreas = mediaAreas
	forward, err := decodeStoryForward(forwardJSON)
	if err != nil {
		return domain.Story{}, fmt.Errorf("decode story forward: %w", err)
	}
	story.Forward = forward
	story.Out = story.Owner.Type == domain.PeerTypeUser && story.Owner.ID == viewerUserID
	return story, nil
}

func groupPGPeerStories(stories []domain.Story, reads []domain.StoryReadState) []domain.PeerStories {
	readByPeer := make(map[domain.Peer]int, len(reads))
	for _, read := range reads {
		readByPeer[read.Peer] = read.MaxReadID
	}
	index := make(map[domain.Peer]int)
	out := make([]domain.PeerStories, 0)
	for _, story := range stories {
		i, ok := index[story.Owner]
		if !ok {
			i = len(out)
			index[story.Owner] = i
			out = append(out, domain.PeerStories{Peer: story.Owner, MaxReadID: readByPeer[story.Owner]})
		}
		out[i].Stories = append(out[i].Stories, clonePGStory(story))
	}
	for i := range out {
		sort.Slice(out[i].Stories, func(a, b int) bool {
			if out[i].Stories[a].ID != out[i].Stories[b].ID {
				return out[i].Stories[a].ID < out[i].Stories[b].ID
			}
			return out[i].Stories[a].Date < out[i].Stories[b].Date
		})
	}
	return out
}

func validatePGStoryIdentity(peer domain.Peer, id int) error {
	if err := validatePGStoryPeer(peer); err != nil {
		return err
	}
	if id <= 0 || id > domain.MaxStoryID {
		return domain.ErrStoryIDInvalid
	}
	return nil
}

func validatePGStoryPeer(peer domain.Peer) error {
	switch peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		if peer.ID > 0 {
			return nil
		}
	}
	return domain.ErrStoryPeerInvalid
}

func validatePGStoryIDs(ids []int) error {
	if len(ids) > domain.MaxStoryIDs {
		return domain.ErrStoryIDInvalid
	}
	for _, id := range ids {
		if id <= 0 || id > domain.MaxStoryID {
			return domain.ErrStoryIDInvalid
		}
	}
	return nil
}

func validatePGStoryIDsNonEmpty(ids []int) error {
	if len(ids) == 0 {
		return domain.ErrStoryIDInvalid
	}
	return validatePGStoryIDs(ids)
}

func normalizePGStoryIDsNonEmpty(ids []int) ([]int, error) {
	if err := validatePGStoryIDsNonEmpty(ids); err != nil {
		return nil, err
	}
	return normalizePGStoryIDsUnchecked(ids), nil
}

func normalizePGStoryIDs(ids []int) ([]int, error) {
	if err := validatePGStoryIDs(ids); err != nil {
		return nil, err
	}
	return normalizePGStoryIDsUnchecked(ids), nil
}

func normalizePGStoryPinnedToTopIDs(ids []int) ([]int, error) {
	ids, err := normalizePGStoryIDs(ids)
	if err != nil {
		return nil, err
	}
	if len(ids) > domain.MaxStoryPinnedToTop {
		return nil, domain.ErrStoryIDInvalid
	}
	return ids, nil
}

func normalizePGStoryIDsUnchecked(ids []int) []int {
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func clampPGStoryLimit(limit int) int {
	if limit <= 0 || limit > domain.MaxStoryListLimit {
		return domain.MaxStoryListLimit
	}
	return limit
}

func clampPGStoryProbeLimit(limit int) int {
	max := domain.MaxStoryListLimit + 1
	if limit <= 0 || limit > max {
		return max
	}
	return limit
}

func clampPGStoryInteractionLimit(limit int) int {
	if limit <= 0 || limit > domain.MaxStoryInteractionListLimit {
		return domain.MaxStoryInteractionListLimit
	}
	return limit
}

func clampPGStoryPrivacyFanoutLimit(limit int) int {
	if limit <= 0 || limit > domain.MaxStoryPrivacyFanoutTargets {
		return domain.MaxStoryPrivacyFanoutTargets
	}
	return limit
}

type storyInteractionCursor struct {
	set       bool
	group     int
	date      int
	viewerID  int64
	messageID int
}

func parsePGStoryInteractionCursor(offset string) storyInteractionCursor {
	if offset == "" {
		return storyInteractionCursor{}
	}
	parts := strings.Split(offset, ":")
	if len(parts) != 3 && len(parts) != 4 {
		return storyInteractionCursor{}
	}
	group, err1 := strconv.Atoi(parts[0])
	date, err2 := strconv.Atoi(parts[1])
	viewerID, err3 := strconv.ParseInt(parts[2], 10, 64)
	var messageID int
	var err4 error
	if len(parts) == 4 {
		messageID, err4 = strconv.Atoi(parts[3])
	}
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil || group < 0 || viewerID == 0 || messageID < 0 {
		return storyInteractionCursor{}
	}
	return storyInteractionCursor{set: true, group: group, date: date, viewerID: viewerID, messageID: messageID}
}

func formatPGStoryInteractionCursor(view domain.StoryView, reactionsFirst, forwardsFirst bool) string {
	group := pgStoryViewSortGroup(view, reactionsFirst, forwardsFirst)
	out := strconv.Itoa(group) + ":" + strconv.Itoa(view.Date) + ":" + strconv.FormatInt(pgStoryViewCursorKey(view), 10)
	if id := pgStoryViewCursorMessageID(view); id > 0 {
		out += ":" + strconv.Itoa(id)
	}
	return out
}

func pgStoryViewSortGroup(view domain.StoryView, reactionsFirst, forwardsFirst bool) int {
	if forwardsFirst {
		if view.Repost != nil || view.PublicForward != nil {
			return 0
		}
		return 1
	}
	if reactionsFirst && view.Reaction == nil && view.Repost == nil && view.PublicForward == nil {
		return 1
	}
	return 0
}

func pgStoryViewCursorKey(view domain.StoryView) int64 {
	if view.PublicForward != nil {
		return pgStoryPeerCursorKey(domain.Peer{Type: domain.PeerTypeChannel, ID: view.PublicForward.Message.ChannelID})
	}
	if view.Repost != nil {
		return pgStoryPeerCursorKey(view.Repost.Owner)
	}
	return view.ViewerID
}

func pgStoryViewCursorMessageID(view domain.StoryView) int {
	if view.PublicForward != nil {
		return view.PublicForward.Message.ID
	}
	return 0
}

func pgStoryPeerCursorKey(peer domain.Peer) int64 {
	if peer.Type == domain.PeerTypeChannel {
		return -peer.ID
	}
	return peer.ID
}

func sortPGStoryViewsForList(views []domain.StoryView, reactionsFirst, forwardsFirst bool) {
	sort.Slice(views, func(i, j int) bool {
		gi := pgStoryViewSortGroup(views[i], reactionsFirst, forwardsFirst)
		gj := pgStoryViewSortGroup(views[j], reactionsFirst, forwardsFirst)
		if gi != gj {
			return gi < gj
		}
		if views[i].Date != views[j].Date {
			return views[i].Date > views[j].Date
		}
		if pgStoryViewCursorKey(views[i]) != pgStoryViewCursorKey(views[j]) {
			return pgStoryViewCursorKey(views[i]) > pgStoryViewCursorKey(views[j])
		}
		return pgStoryViewCursorMessageID(views[i]) > pgStoryViewCursorMessageID(views[j])
	})
}

func encodeStoryReaction(reaction *domain.MessageReaction) ([]byte, error) {
	if reaction == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(reaction)
}

func decodeStoryReaction(raw string) (*domain.MessageReaction, error) {
	if raw == "" || raw == "{}" || raw == "null" {
		return nil, nil
	}
	var reaction domain.MessageReaction
	if err := json.Unmarshal([]byte(raw), &reaction); err != nil {
		return nil, err
	}
	if reaction.Type == "" {
		return nil, nil
	}
	return &reaction, nil
}

func encodePrivacyRules(rules []domain.PrivacyRule) ([]byte, error) {
	if len(rules) == 0 {
		return []byte("[]"), nil
	}
	return json.Marshal(rules)
}

func encodeStoryMediaAreas(areas []domain.StoryMediaArea) ([]byte, error) {
	if len(areas) == 0 {
		return []byte("[]"), nil
	}
	raw, err := json.Marshal(areas)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func decodeStoryMediaAreas(raw string) ([]domain.StoryMediaArea, error) {
	if raw == "" || raw == "[]" || raw == "null" {
		return nil, nil
	}
	var areas []domain.StoryMediaArea
	if err := json.Unmarshal([]byte(raw), &areas); err != nil {
		return nil, err
	}
	return clonePGStoryMediaAreas(areas), nil
}

func encodeStoryForward(forward *domain.StoryForward) ([]byte, error) {
	if forward == nil {
		return []byte("{}"), nil
	}
	raw, err := json.Marshal(forward)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func decodeStoryForward(raw string) (*domain.StoryForward, error) {
	if raw == "" || raw == "{}" || raw == "null" {
		return nil, nil
	}
	var forward domain.StoryForward
	if err := json.Unmarshal([]byte(raw), &forward); err != nil {
		return nil, err
	}
	if forward.From.Type == "" && forward.FromName == "" && forward.StoryID == 0 {
		return nil, nil
	}
	return clonePGStoryForward(&forward), nil
}

func addPGStoryReactionCount(views *domain.StoryViews, reaction domain.MessageReaction) {
	for i := range views.Reactions {
		if views.Reactions[i].Reaction == reaction {
			views.Reactions[i].Count++
			return
		}
	}
	views.Reactions = append(views.Reactions, domain.ChannelMessageReactionCount{
		Reaction:    reaction,
		Count:       1,
		ChosenOrder: len(views.Reactions),
	})
}

func samePGReaction(a, b *domain.MessageReaction) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func clonePGStory(story domain.Story) domain.Story {
	story.PrivacyRules = clonePGPrivacyRules(story.PrivacyRules)
	story.AllowUserIDs = append([]int64(nil), story.AllowUserIDs...)
	story.DisallowUserIDs = append([]int64(nil), story.DisallowUserIDs...)
	story.Entities = append([]domain.MessageEntity(nil), story.Entities...)
	story.MediaAreas = clonePGStoryMediaAreas(story.MediaAreas)
	story.Forward = clonePGStoryForward(story.Forward)
	story.Views.Reactions = append([]domain.ChannelMessageReactionCount(nil), story.Views.Reactions...)
	story.Views.RecentViewers = append([]int64(nil), story.Views.RecentViewers...)
	story.SentReaction = clonePGReactionPtr(story.SentReaction)
	return story
}

func clonePGStoryForward(in *domain.StoryForward) *domain.StoryForward {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func clonePGStoryMediaAreas(in []domain.StoryMediaArea) []domain.StoryMediaArea {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.StoryMediaArea, len(in))
	for i, area := range in {
		out[i] = area
		out[i].Reaction = clonePGReactionPtr(area.Reaction)
		if area.Geo != nil {
			geo := *area.Geo
			out[i].Geo = &geo
		}
		if area.GeoAddress != nil {
			address := *area.GeoAddress
			out[i].GeoAddress = &address
		}
		if area.Venue != nil {
			venue := *area.Venue
			out[i].Venue = &venue
		}
	}
	return out
}

func fanoutPGStorySnapshot(story domain.Story) domain.Story {
	story = clonePGStory(story)
	story.Out = false
	story.Views = domain.StoryViews{}
	story.SentReaction = nil
	return story
}

func clonePGPrivacyRules(in []domain.PrivacyRule) []domain.PrivacyRule {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.PrivacyRule, len(in))
	for i, rule := range in {
		out[i] = rule
		out[i].UserIDs = append([]int64(nil), rule.UserIDs...)
		out[i].ChatIDs = append([]int64(nil), rule.ChatIDs...)
	}
	return out
}

func clonePGReactionPtr(in *domain.MessageReaction) *domain.MessageReaction {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
