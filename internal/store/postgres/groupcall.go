package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// GroupCallStore 是 store.GroupCallStore 的 PostgreSQL 实现（迁移 0092）。
// 所有参与者维度变更在事务内对 group_calls 行 FOR UPDATE 后 version+1，
// 行锁天然串行化并发变更，保证 version 单调无跳号。
type GroupCallStore struct {
	db sqlcgen.DBTX
}

// NewGroupCallStore 基于 pgx 连接池创建 GroupCallStore。
func NewGroupCallStore(db sqlcgen.DBTX) *GroupCallStore {
	return &GroupCallStore{db: db}
}

const groupCallColumns = `call_id, access_hash, channel_id, creator_user_id, kind, state, title, join_muted,
version, participants_count, created_at, discarded_at, duration, started_msg_id,
invite_slug, invite_link, random_id, migrated_from_phone_call_id`

const groupCallParticipantColumns = `call_id, user_id, ssrc, join_date, active_date, muted, muted_by_admin,
volume_by_admin, raise_hand_rating, video_json, presentation_json, public_key, join_block, left_call, last_check_date`

func scanGroupCall(row rowScanner) (domain.GroupCall, error) {
	var c domain.GroupCall
	var kind, state string
	if err := row.Scan(
		&c.ID, &c.AccessHash, &c.ChannelID, &c.CreatorUserID, &kind, &state, &c.Title, &c.JoinMuted,
		&c.Version, &c.ParticipantsCount, &c.CreatedAt, &c.DiscardedAt, &c.Duration, &c.StartedMsgID,
		&c.InviteSlug, &c.InviteLink, &c.RandomID, &c.MigratedFromPhoneCallID,
	); err != nil {
		return domain.GroupCall{}, err
	}
	c.Kind = domain.GroupCallKind(kind)
	c.State = domain.GroupCallState(state)
	return c, nil
}

func scanGroupCallParticipant(row rowScanner) (domain.GroupCallParticipant, error) {
	var p domain.GroupCallParticipant
	if err := row.Scan(
		&p.CallID, &p.UserID, &p.SSRC, &p.JoinDate, &p.ActiveDate, &p.Muted, &p.MutedByAdmin,
		&p.VolumeByAdmin, &p.RaiseHandRating, &p.VideoJSON, &p.PresentationJSON, &p.PublicKey, &p.JoinBlock, &p.Left, &p.LastCheckDate,
	); err != nil {
		return domain.GroupCallParticipant{}, err
	}
	return p, nil
}

func prefixedGroupCallColumns(alias string) string {
	parts := strings.Split(groupCallColumns, ",")
	for i, part := range parts {
		parts[i] = alias + "." + strings.TrimSpace(part)
	}
	return strings.Join(parts, ", ")
}

func scanGroupCallInviteJoined(row rowScanner) (domain.GroupCall, domain.GroupCallInvite, error) {
	var c domain.GroupCall
	var inv domain.GroupCallInvite
	var kind, state, status string
	if err := row.Scan(
		&c.ID, &c.AccessHash, &c.ChannelID, &c.CreatorUserID, &kind, &state, &c.Title, &c.JoinMuted,
		&c.Version, &c.ParticipantsCount, &c.CreatedAt, &c.DiscardedAt, &c.Duration, &c.StartedMsgID,
		&c.InviteSlug, &c.InviteLink, &c.RandomID, &c.MigratedFromPhoneCallID,
		&inv.CallID, &inv.InviterUserID, &inv.InviteeUserID, &inv.MessageID, &status, &inv.Video, &inv.CreatedAt, &inv.UpdatedAt,
	); err != nil {
		return domain.GroupCall{}, domain.GroupCallInvite{}, err
	}
	c.Kind = domain.GroupCallKind(kind)
	c.State = domain.GroupCallState(state)
	inv.Status = domain.GroupCallInviteStatus(status)
	return c, inv, nil
}

func (s *GroupCallStore) begin(ctx context.Context, op string) (pgx.Tx, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return nil, fmt.Errorf("%s: db does not support transactions", op)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin %s: %w", op, err)
	}
	return tx, nil
}

// lockGroupCallTx 锁定 call 行（串行化参与者变更的唯一入口）。
func lockGroupCallTx(ctx context.Context, tx pgx.Tx, callID int64) (domain.GroupCall, error) {
	call, err := scanGroupCall(tx.QueryRow(ctx,
		`SELECT `+groupCallColumns+` FROM group_calls WHERE call_id = $1 FOR UPDATE`, callID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCall{}, domain.ErrGroupCallInvalid
	}
	if err != nil {
		return domain.GroupCall{}, fmt.Errorf("lock group call: %w", err)
	}
	return call, nil
}

func bumpGroupCallVersionTx(ctx context.Context, tx pgx.Tx, callID int64, countDelta int) (domain.GroupCall, error) {
	call, err := scanGroupCall(tx.QueryRow(ctx, `
UPDATE group_calls
SET version = version + 1,
    participants_count = GREATEST(0, participants_count + $2)
WHERE call_id = $1
RETURNING `+groupCallColumns, callID, countDelta))
	if err != nil {
		return domain.GroupCall{}, fmt.Errorf("bump group call version: %w", err)
	}
	return call, nil
}

func bumpGroupCallParticipantsTx(ctx context.Context, tx pgx.Tx, callID int64, countDelta, now int) (domain.GroupCall, error) {
	call, err := scanGroupCall(tx.QueryRow(ctx, `
UPDATE group_calls
SET version = version + 1,
    participants_count = GREATEST(0, participants_count + $2),
    state = CASE
        WHEN kind = 'conference' AND state = 'active' AND GREATEST(0, participants_count + $2) = 0 THEN 'discarded'
        ELSE state
    END,
    discarded_at = CASE
        WHEN kind = 'conference' AND state = 'active' AND GREATEST(0, participants_count + $2) = 0 THEN $3
        ELSE discarded_at
    END,
    duration = CASE
        WHEN kind = 'conference' AND state = 'active' AND GREATEST(0, participants_count + $2) = 0 THEN GREATEST(0, $3 - created_at)
        ELSE duration
    END
WHERE call_id = $1
RETURNING `+groupCallColumns, callID, countDelta, now))
	if err != nil {
		return domain.GroupCall{}, fmt.Errorf("bump group call participants: %w", err)
	}
	return call, nil
}

func (s *GroupCallStore) CreateGroupCall(ctx context.Context, call domain.GroupCall) (domain.GroupCall, error) {
	if call.ID == 0 || call.ChannelID == 0 || call.AccessHash == 0 {
		return domain.GroupCall{}, domain.ErrGroupCallInvalid
	}
	if call.Version <= 0 {
		call.Version = 1
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO group_calls (call_id, access_hash, channel_id, creator_user_id, kind, state, title, join_muted, version, participants_count, created_at)
VALUES ($1, $2, $3, $4, 'channel', 'active', $5, $6, $7, 0, $8)`,
		call.ID, call.AccessHash, call.ChannelID, call.CreatorUserID, call.Title, call.JoinMuted, call.Version, call.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == "group_calls_active_channel_uniq" {
				return domain.GroupCall{}, domain.ErrGroupCallAlreadyStarted
			}
			return domain.GroupCall{}, domain.ErrGroupCallInvalid
		}
		return domain.GroupCall{}, fmt.Errorf("insert group call: %w", err)
	}
	call.State = domain.GroupCallStateActive
	call.Kind = domain.GroupCallKindChannel
	call.ParticipantsCount = 0
	return call, nil
}

func (s *GroupCallStore) CreateConferenceCall(ctx context.Context, call domain.GroupCall) (domain.GroupCall, error) {
	if call.ID == 0 || call.AccessHash == 0 || call.CreatorUserID == 0 || call.InviteSlug == "" || call.InviteLink == "" {
		return domain.GroupCall{}, domain.ErrGroupCallInvalid
	}
	if call.Version <= 0 {
		call.Version = 1
	}
	call.ChannelID = 0
	call.Kind = domain.GroupCallKindConference
	call.State = domain.GroupCallStateActive
	call.ParticipantsCount = 0
	created, err := scanGroupCall(s.db.QueryRow(ctx, `
INSERT INTO group_calls (
    call_id, access_hash, channel_id, creator_user_id, kind, state, title, join_muted,
    version, participants_count, created_at, invite_slug, invite_link, random_id, migrated_from_phone_call_id
) VALUES ($1, $2, 0, $3, 'conference', 'active', $4, FALSE, $5, 0, $6, $7, $8, $9, $10)
ON CONFLICT DO NOTHING
RETURNING `+groupCallColumns,
		call.ID, call.AccessHash, call.CreatorUserID, call.Title, call.Version, call.CreatedAt,
		call.InviteSlug, call.InviteLink, call.RandomID, call.MigratedFromPhoneCallID))
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCall{}, fmt.Errorf("insert conference call: %w", err)
	}
	if call.RandomID != 0 {
		existing, found, getErr := s.getConferenceByRandom(ctx, call.CreatorUserID, call.RandomID)
		if getErr != nil {
			return domain.GroupCall{}, getErr
		}
		if found {
			return existing, nil
		}
	}
	if existing, found, getErr := s.GetGroupCallBySlug(ctx, call.InviteSlug); getErr != nil {
		return domain.GroupCall{}, getErr
	} else if found {
		return existing, nil
	}
	return domain.GroupCall{}, domain.ErrGroupCallInvalid
}

func (s *GroupCallStore) getConferenceByRandom(ctx context.Context, creatorID, randomID int64) (domain.GroupCall, bool, error) {
	call, err := scanGroupCall(s.db.QueryRow(ctx,
		`SELECT `+groupCallColumns+` FROM group_calls WHERE kind = 'conference' AND creator_user_id = $1 AND random_id = $2`,
		creatorID, randomID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCall{}, false, nil
	}
	if err != nil {
		return domain.GroupCall{}, false, fmt.Errorf("get conference by random: %w", err)
	}
	return call, true, nil
}

func (s *GroupCallStore) GetGroupCall(ctx context.Context, callID int64) (domain.GroupCall, bool, error) {
	call, err := scanGroupCall(s.db.QueryRow(ctx,
		`SELECT `+groupCallColumns+` FROM group_calls WHERE call_id = $1`, callID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCall{}, false, nil
	}
	if err != nil {
		return domain.GroupCall{}, false, fmt.Errorf("get group call: %w", err)
	}
	return call, true, nil
}

func (s *GroupCallStore) GetGroupCallBySlug(ctx context.Context, slug string) (domain.GroupCall, bool, error) {
	call, err := scanGroupCall(s.db.QueryRow(ctx,
		`SELECT `+groupCallColumns+` FROM group_calls WHERE invite_slug = $1`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCall{}, false, nil
	}
	if err != nil {
		return domain.GroupCall{}, false, fmt.Errorf("get group call by slug: %w", err)
	}
	return call, true, nil
}

func (s *GroupCallStore) GetGroupCallByInviteMessage(ctx context.Context, userID int64, msgID int) (domain.GroupCall, domain.GroupCallInvite, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT `+prefixedGroupCallColumns("c")+`, i.call_id, i.inviter_user_id, i.invitee_user_id, i.message_id, i.status, i.video, i.created_at, i.updated_at
FROM group_call_invites i
JOIN group_calls c ON c.call_id = i.call_id
WHERE i.invitee_user_id = $1 AND i.message_id = $2`, userID, msgID)
	call, inv, err := scanGroupCallInviteJoined(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCall{}, domain.GroupCallInvite{}, false, nil
	}
	if err != nil {
		return domain.GroupCall{}, domain.GroupCallInvite{}, false, fmt.Errorf("get group call by invite message: %w", err)
	}
	return call, inv, true, nil
}

func (s *GroupCallStore) JoinGroupCall(ctx context.Context, req domain.JoinGroupCallRequest) (domain.GroupCallMutation, error) {
	if req.SSRC == 0 {
		return domain.GroupCallMutation{}, domain.ErrGroupCallInvalid
	}
	tx, err := s.begin(ctx, "join group call")
	if err != nil {
		return domain.GroupCallMutation{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	call, err := lockGroupCallTx(ctx, tx, req.CallID)
	if err != nil {
		return domain.GroupCallMutation{}, err
	}
	if !call.Active() {
		return domain.GroupCallMutation{}, domain.ErrGroupCallDiscarded
	}
	existing, err := scanGroupCallParticipant(tx.QueryRow(ctx,
		`SELECT `+groupCallParticipantColumns+` FROM group_call_participants WHERE call_id = $1 AND user_id = $2`,
		req.CallID, req.UserID))
	wasActive := err == nil && !existing.Left
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCallMutation{}, fmt.Errorf("load group call participant: %w", err)
	}
	p := domain.GroupCallParticipant{
		CallID:        req.CallID,
		UserID:        req.UserID,
		SSRC:          req.SSRC,
		JoinDate:      req.Now,
		ActiveDate:    req.Now,
		LastCheckDate: req.Now,
		PublicKey:     append([]byte(nil), req.PublicKey...),
		JoinBlock:     append([]byte(nil), req.JoinBlock...),
	}
	if wasActive {
		// 同人换 ssrc 的 rejoin 保留原 join_date（列表排序稳定）。
		p.JoinDate = existing.JoinDate
	}
	if call.JoinMuted && !req.IsAdmin {
		p.Muted = true
		p.MutedByAdmin = true
	}
	p.VideoJSON = append([]byte(nil), req.VideoJSON...)
	// video_json 整体替换、presentation_json 清空（rejoin 后客户端会重发
	// joinGroupCallPresentation，旧屏幕登记必须作废）。
	if _, err := tx.Exec(ctx, `
INSERT INTO group_call_participants (call_id, user_id, ssrc, join_date, active_date, muted, muted_by_admin, volume_by_admin, raise_hand_rating, video_json, public_key, join_block, left_call, last_check_date)
VALUES ($1, $2, $3, $4, $5, $6, $7, 0, 0, $8, $9, $10, FALSE, $11)
ON CONFLICT (call_id, user_id) DO UPDATE SET
    ssrc = EXCLUDED.ssrc,
    join_date = EXCLUDED.join_date,
    active_date = EXCLUDED.active_date,
    muted = EXCLUDED.muted,
    muted_by_admin = EXCLUDED.muted_by_admin,
    volume_by_admin = 0,
    raise_hand_rating = 0,
    video_json = EXCLUDED.video_json,
    presentation_json = NULL,
    public_key = EXCLUDED.public_key,
    join_block = EXCLUDED.join_block,
    left_call = FALSE,
    last_check_date = EXCLUDED.last_check_date`,
		req.CallID, req.UserID, req.SSRC, p.JoinDate, p.ActiveDate, p.Muted, p.MutedByAdmin,
		nullableJSON(p.VideoJSON), nullableGroupCallBytes(p.PublicKey), nullableGroupCallBytes(p.JoinBlock), p.LastCheckDate); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.GroupCallMutation{}, domain.ErrGroupCallSSRCDuplicate
		}
		return domain.GroupCallMutation{}, fmt.Errorf("upsert group call participant: %w", err)
	}
	if _, err := tx.Exec(ctx, `
UPDATE group_call_invites
SET status = 'accepted', updated_at = $3
WHERE call_id = $1 AND invitee_user_id = $2 AND status = 'pending'`,
		req.CallID, req.UserID, req.Now); err != nil {
		return domain.GroupCallMutation{}, fmt.Errorf("accept conference invites: %w", err)
	}
	countDelta := 0
	if !wasActive {
		countDelta = 1
	}
	call, err = bumpGroupCallVersionTx(ctx, tx, req.CallID, countDelta)
	if err != nil {
		return domain.GroupCallMutation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.GroupCallMutation{}, fmt.Errorf("commit join group call: %w", err)
	}
	committed = true
	return domain.GroupCallMutation{Call: call, Participant: p}, nil
}

func (s *GroupCallStore) LeaveGroupCall(ctx context.Context, callID, userID int64, now int) (domain.GroupCallMutation, error) {
	tx, err := s.begin(ctx, "leave group call")
	if err != nil {
		return domain.GroupCallMutation{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err := lockGroupCallTx(ctx, tx, callID); err != nil {
		return domain.GroupCallMutation{}, err
	}
	p, err := scanGroupCallParticipant(tx.QueryRow(ctx, `
UPDATE group_call_participants
SET left_call = TRUE, active_date = $3
WHERE call_id = $1 AND user_id = $2 AND NOT left_call
RETURNING `+groupCallParticipantColumns, callID, userID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCallMutation{}, domain.ErrGroupCallNotJoined
	}
	if err != nil {
		return domain.GroupCallMutation{}, fmt.Errorf("leave group call participant: %w", err)
	}
	call, err := bumpGroupCallParticipantsTx(ctx, tx, callID, -1, now)
	if err != nil {
		return domain.GroupCallMutation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.GroupCallMutation{}, fmt.Errorf("commit leave group call: %w", err)
	}
	committed = true
	return domain.GroupCallMutation{Call: call, Participant: p}, nil
}

func (s *GroupCallStore) RemoveConferenceCallParticipants(ctx context.Context, req domain.RemoveConferenceCallParticipantsRequest) (domain.RemoveConferenceCallParticipantsResult, error) {
	if req.CallID == 0 || len(req.TargetUserIDs) == 0 || req.OnlyLeft == req.Kick {
		return domain.RemoveConferenceCallParticipantsResult{}, domain.ErrGroupCallInvalid
	}
	tx, err := s.begin(ctx, "remove conference call participants")
	if err != nil {
		return domain.RemoveConferenceCallParticipantsResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	call, err := lockGroupCallTx(ctx, tx, req.CallID)
	if err != nil {
		return domain.RemoveConferenceCallParticipantsResult{}, err
	}
	if !call.Conference() {
		return domain.RemoveConferenceCallParticipantsResult{}, domain.ErrGroupCallInvalid
	}
	if !call.Active() {
		return domain.RemoveConferenceCallParticipantsResult{}, domain.ErrGroupCallDiscarded
	}
	targets := uniqueNonZeroInt64s(req.TargetUserIDs...)
	if len(targets) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.RemoveConferenceCallParticipantsResult{}, fmt.Errorf("commit no-op conference participant removal: %w", err)
		}
		committed = true
		return domain.RemoveConferenceCallParticipantsResult{Call: call}, nil
	}
	rows, err := tx.Query(ctx, `
SELECT `+groupCallParticipantColumns+`
FROM group_call_participants
WHERE call_id = $1 AND user_id = ANY($2)
FOR UPDATE`, req.CallID, targets)
	if err != nil {
		return domain.RemoveConferenceCallParticipantsResult{}, fmt.Errorf("lock conference participants: %w", err)
	}
	byID := make(map[int64]domain.GroupCallParticipant, len(targets))
	for rows.Next() {
		p, err := scanGroupCallParticipant(rows)
		if err != nil {
			rows.Close()
			return domain.RemoveConferenceCallParticipantsResult{}, err
		}
		byID[p.UserID] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.RemoveConferenceCallParticipantsResult{}, err
	}
	e2eTargets := make([]int64, 0, len(targets))
	mediaTargets := make([]int64, 0, len(targets))
	for _, targetID := range targets {
		p, ok := byID[targetID]
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
		block, err := appendGroupCallChainBlockTx(ctx, tx, domain.GroupCallChainBlock{
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
		if _, err := tx.Exec(ctx, `
UPDATE group_call_participants
SET public_key = NULL, join_block = NULL
WHERE call_id = $1 AND user_id = ANY($2)`, req.CallID, e2eTargets); err != nil {
			return domain.RemoveConferenceCallParticipantsResult{}, fmt.Errorf("clear conference e2e participants: %w", err)
		}
	}
	if len(mediaTargets) > 0 {
		rows, err := tx.Query(ctx, `
UPDATE group_call_participants
SET left_call = TRUE, active_date = $3
WHERE call_id = $1 AND user_id = ANY($2) AND NOT left_call
RETURNING `+groupCallParticipantColumns, req.CallID, mediaTargets, req.Now)
		if err != nil {
			return domain.RemoveConferenceCallParticipantsResult{}, fmt.Errorf("leave kicked conference participants: %w", err)
		}
		for rows.Next() {
			p, err := scanGroupCallParticipant(rows)
			if err != nil {
				rows.Close()
				return domain.RemoveConferenceCallParticipantsResult{}, err
			}
			out.ParticipantsChanged = append(out.ParticipantsChanged, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return domain.RemoveConferenceCallParticipantsResult{}, err
		}
		if len(out.ParticipantsChanged) > 0 {
			call, err = bumpGroupCallParticipantsTx(ctx, tx, req.CallID, -len(out.ParticipantsChanged), req.Now)
			if err != nil {
				return domain.RemoveConferenceCallParticipantsResult{}, err
			}
			out.Call = call
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RemoveConferenceCallParticipantsResult{}, fmt.Errorf("commit conference participant removal: %w", err)
	}
	committed = true
	return out, nil
}

func (s *GroupCallStore) DiscardGroupCall(ctx context.Context, callID int64, now int) (domain.GroupCall, []domain.GroupCallParticipant, error) {
	tx, err := s.begin(ctx, "discard group call")
	if err != nil {
		return domain.GroupCall{}, nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	call, err := lockGroupCallTx(ctx, tx, callID)
	if err != nil {
		return domain.GroupCall{}, nil, err
	}
	if !call.Active() {
		return domain.GroupCall{}, nil, domain.ErrGroupCallDiscarded
	}
	rows, err := tx.Query(ctx, `
UPDATE group_call_participants
SET left_call = TRUE, active_date = $2
WHERE call_id = $1 AND NOT left_call
RETURNING `+groupCallParticipantColumns, callID, now)
	if err != nil {
		return domain.GroupCall{}, nil, fmt.Errorf("clear group call participants: %w", err)
	}
	var active []domain.GroupCallParticipant
	for rows.Next() {
		p, err := scanGroupCallParticipant(rows)
		if err != nil {
			rows.Close()
			return domain.GroupCall{}, nil, err
		}
		active = append(active, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.GroupCall{}, nil, err
	}
	call, err = scanGroupCall(tx.QueryRow(ctx, `
UPDATE group_calls
SET state = 'discarded', discarded_at = $2, duration = GREATEST(0, $2 - created_at),
    participants_count = 0, version = version + 1
WHERE call_id = $1
RETURNING `+groupCallColumns, callID, now))
	if err != nil {
		return domain.GroupCall{}, nil, fmt.Errorf("discard group call: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.GroupCall{}, nil, fmt.Errorf("commit discard group call: %w", err)
	}
	committed = true
	return call, active, nil
}

func (s *GroupCallStore) TouchParticipant(ctx context.Context, callID, userID int64, now int) ([]int64, bool, error) {
	call, found, err := s.GetGroupCall(ctx, callID)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, domain.ErrGroupCallInvalid
	}
	if !call.Active() {
		return nil, false, nil
	}
	var ssrc int64
	err = s.db.QueryRow(ctx, `
UPDATE group_call_participants
SET last_check_date = $3, active_date = $3
WHERE call_id = $1 AND user_id = $2 AND NOT left_call
RETURNING ssrc`, callID, userID, now).Scan(&ssrc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("touch group call participant: %w", err)
	}
	return []int64{ssrc}, true, nil
}

func (s *GroupCallStore) GetParticipant(ctx context.Context, callID, userID int64) (domain.GroupCallParticipant, bool, error) {
	p, err := scanGroupCallParticipant(s.db.QueryRow(ctx,
		`SELECT `+groupCallParticipantColumns+` FROM group_call_participants WHERE call_id = $1 AND user_id = $2`,
		callID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCallParticipant{}, false, nil
	}
	if err != nil {
		return domain.GroupCallParticipant{}, false, fmt.Errorf("get group call participant: %w", err)
	}
	return p, true, nil
}

func (s *GroupCallStore) ListParticipants(ctx context.Context, callID int64, offset string, limit int) (domain.GroupCallParticipantPage, error) {
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	call, found, err := s.GetGroupCall(ctx, callID)
	if err != nil {
		return domain.GroupCallParticipantPage{}, err
	}
	if !found {
		return domain.GroupCallParticipantPage{}, domain.ErrGroupCallInvalid
	}
	page := domain.GroupCallParticipantPage{Version: call.Version}
	if err := s.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM group_call_participants WHERE call_id = $1 AND NOT left_call`, callID,
	).Scan(&page.Count); err != nil {
		return domain.GroupCallParticipantPage{}, fmt.Errorf("count group call participants: %w", err)
	}
	offDate, offUser, hasOffset := parseGroupCallOffset(offset)
	query := `SELECT ` + groupCallParticipantColumns + ` FROM group_call_participants
WHERE call_id = $1 AND NOT left_call`
	args := []any{callID}
	if hasOffset {
		query += ` AND (join_date, user_id) > ($2, $3)`
		args = append(args, offDate, offUser)
	}
	query += fmt.Sprintf(` ORDER BY join_date ASC, user_id ASC LIMIT %d`, limit)
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return domain.GroupCallParticipantPage{}, fmt.Errorf("list group call participants: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		p, err := scanGroupCallParticipant(rows)
		if err != nil {
			return domain.GroupCallParticipantPage{}, err
		}
		page.Participants = append(page.Participants, p)
	}
	if err := rows.Err(); err != nil {
		return domain.GroupCallParticipantPage{}, err
	}
	if n := len(page.Participants); n == limit && n < page.Count {
		last := page.Participants[n-1]
		page.NextOffset = formatGroupCallOffset(last.JoinDate, last.UserID)
	}
	return page, nil
}

func (s *GroupCallStore) UpdateParticipant(ctx context.Context, callID, userID int64, update domain.GroupCallParticipantUpdate) (domain.GroupCallMutation, bool, error) {
	tx, err := s.begin(ctx, "update group call participant")
	if err != nil {
		return domain.GroupCallMutation{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	call, err := lockGroupCallTx(ctx, tx, callID)
	if err != nil {
		return domain.GroupCallMutation{}, false, err
	}
	if !call.Active() {
		return domain.GroupCallMutation{}, false, domain.ErrGroupCallDiscarded
	}
	p, err := scanGroupCallParticipant(tx.QueryRow(ctx,
		`SELECT `+groupCallParticipantColumns+` FROM group_call_participants WHERE call_id = $1 AND user_id = $2`,
		callID, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCallMutation{}, false, domain.ErrGroupCallNotJoined
	}
	if err != nil {
		return domain.GroupCallMutation{}, false, fmt.Errorf("load group call participant: %w", err)
	}
	if p.Left {
		return domain.GroupCallMutation{}, false, domain.ErrGroupCallNotJoined
	}
	changed := applyGroupCallParticipantUpdateRow(&p, update)
	if !changed {
		return domain.GroupCallMutation{Call: call, Participant: p}, false, nil
	}
	if update.Now > 0 {
		p.ActiveDate = update.Now
	}
	if _, err := tx.Exec(ctx, `
UPDATE group_call_participants
SET muted = $3, muted_by_admin = $4, volume_by_admin = $5, raise_hand_rating = $6,
    video_json = $7, presentation_json = $8, active_date = $9
WHERE call_id = $1 AND user_id = $2`,
		callID, userID, p.Muted, p.MutedByAdmin, p.VolumeByAdmin, p.RaiseHandRating,
		nullableJSON(p.VideoJSON), nullableJSON(p.PresentationJSON), p.ActiveDate); err != nil {
		return domain.GroupCallMutation{}, false, fmt.Errorf("update group call participant: %w", err)
	}
	call, err = bumpGroupCallVersionTx(ctx, tx, callID, 0)
	if err != nil {
		return domain.GroupCallMutation{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.GroupCallMutation{}, false, fmt.Errorf("commit update group call participant: %w", err)
	}
	committed = true
	return domain.GroupCallMutation{Call: call, Participant: p}, true, nil
}

func (s *GroupCallStore) SetGroupCallTitle(ctx context.Context, callID int64, title string) (domain.GroupCall, bool, error) {
	call, err := scanGroupCall(s.db.QueryRow(ctx, `
UPDATE group_calls SET title = $2 WHERE call_id = $1 RETURNING `+groupCallColumns, callID, title))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCall{}, false, domain.ErrGroupCallInvalid
	}
	if err != nil {
		return domain.GroupCall{}, false, fmt.Errorf("set group call title: %w", err)
	}
	return call, true, nil
}

func (s *GroupCallStore) SetGroupCallJoinMuted(ctx context.Context, callID int64, joinMuted bool) (domain.GroupCall, bool, error) {
	call, err := scanGroupCall(s.db.QueryRow(ctx, `
UPDATE group_calls SET join_muted = $2 WHERE call_id = $1 RETURNING `+groupCallColumns, callID, joinMuted))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCall{}, false, domain.ErrGroupCallInvalid
	}
	if err != nil {
		return domain.GroupCall{}, false, fmt.Errorf("set group call join muted: %w", err)
	}
	return call, true, nil
}

func (s *GroupCallStore) SetStartedMessageID(ctx context.Context, callID int64, msgID int) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE group_calls SET started_msg_id = $2 WHERE call_id = $1`, callID, msgID); err != nil {
		return fmt.Errorf("set group call started msg: %w", err)
	}
	return nil
}

func (s *GroupCallStore) SweepStaleParticipants(ctx context.Context, checkOlderThan, now, limit int) ([]domain.GroupCallMutation, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
SELECT p.call_id, p.user_id
FROM group_call_participants p
JOIN group_calls c ON c.call_id = p.call_id AND c.state = 'active'
WHERE NOT p.left_call AND p.last_check_date < $1
ORDER BY p.call_id, p.user_id
LIMIT $2`, checkOlderThan, limit)
	if err != nil {
		return nil, fmt.Errorf("list stale group call participants: %w", err)
	}
	type key struct{ callID, userID int64 }
	var stale []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.callID, &k.userID); err != nil {
			rows.Close()
			return nil, err
		}
		stale = append(stale, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []domain.GroupCallMutation
	for _, k := range stale {
		mut, err := s.LeaveGroupCall(ctx, k.callID, k.userID, now)
		if errors.Is(err, domain.ErrGroupCallNotJoined) || errors.Is(err, domain.ErrGroupCallInvalid) {
			continue // 并发 leave/discard 竞态，无害
		}
		if err != nil {
			return out, err
		}
		out = append(out, mut)
	}
	return out, nil
}

func (s *GroupCallStore) ResetAllParticipants(ctx context.Context, now int) ([]domain.GroupCall, error) {
	rows, err := s.db.Query(ctx, `
SELECT DISTINCT c.call_id
FROM group_calls c
JOIN group_call_participants p ON p.call_id = c.call_id AND NOT p.left_call
WHERE c.state = 'active'`)
	if err != nil {
		return nil, fmt.Errorf("list active group calls: %w", err)
	}
	var callIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		callIDs = append(callIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []domain.GroupCall
	for _, callID := range callIDs {
		tx, err := s.begin(ctx, "reset group call participants")
		if err != nil {
			return out, err
		}
		if _, err := lockGroupCallTx(ctx, tx, callID); err != nil {
			_ = tx.Rollback(ctx)
			continue
		}
		if _, err := tx.Exec(ctx, `
UPDATE group_call_participants SET left_call = TRUE, active_date = $2
WHERE call_id = $1 AND NOT left_call`, callID, now); err != nil {
			_ = tx.Rollback(ctx)
			return out, fmt.Errorf("reset group call participants: %w", err)
		}
		call, err := scanGroupCall(tx.QueryRow(ctx, `
UPDATE group_calls
SET participants_count = 0,
    version = version + 1,
    state = CASE
        WHEN kind = 'conference' AND state = 'active' THEN 'discarded'
        ELSE state
    END,
    discarded_at = CASE
        WHEN kind = 'conference' AND state = 'active' THEN $2
        ELSE discarded_at
    END,
    duration = CASE
        WHEN kind = 'conference' AND state = 'active' THEN GREATEST(0, $2 - created_at)
        ELSE duration
    END
WHERE call_id = $1
RETURNING `+groupCallColumns, callID, now))
		if err != nil {
			_ = tx.Rollback(ctx)
			return out, fmt.Errorf("reset group call version: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return out, fmt.Errorf("commit reset group call: %w", err)
		}
		out = append(out, call)
	}
	return out, nil
}

// applyGroupCallParticipantUpdateRow 与 memory 实现共享同一字段语义（contract test 钉死）。
func applyGroupCallParticipantUpdateRow(p *domain.GroupCallParticipant, u domain.GroupCallParticipantUpdate) bool {
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

func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func nullableGroupCallBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func parseGroupCallOffset(offset string) (joinDate int, userID int64, ok bool) {
	if offset == "" {
		return 0, 0, false
	}
	var d int
	var u int64
	if _, err := fmt.Sscanf(offset, "j:%d:%d", &d, &u); err != nil {
		return 0, 0, false
	}
	return d, u, true
}

func formatGroupCallOffset(joinDate int, userID int64) string {
	return fmt.Sprintf("j:%d:%d", joinDate, userID)
}

func (s *GroupCallStore) NextRaiseHandRating(ctx context.Context, callID int64) (int64, error) {
	// 全局单调序号用 sequence；group_calls.version 同步推进保证跨进程单调。
	var rating int64
	if err := s.db.QueryRow(ctx, `
UPDATE group_calls SET version = version + 1 WHERE call_id = $1 RETURNING version`, callID).Scan(&rating); err != nil {
		return 0, fmt.Errorf("next raise hand rating: %w", err)
	}
	return rating, nil
}

func (s *GroupCallStore) SetParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64, override domain.GroupCallParticipantOverride, clear bool) error {
	if clear {
		if _, err := s.db.Exec(ctx,
			`DELETE FROM group_call_participant_overrides WHERE call_id = $1 AND setter_user_id = $2 AND target_user_id = $3`,
			callID, setterUserID, targetUserID); err != nil {
			return fmt.Errorf("clear participant override: %w", err)
		}
		return nil
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO group_call_participant_overrides (call_id, setter_user_id, target_user_id, muted_by_you, volume)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (call_id, setter_user_id, target_user_id) DO UPDATE SET
    muted_by_you = EXCLUDED.muted_by_you,
    volume = EXCLUDED.volume`,
		callID, setterUserID, targetUserID, override.MutedByYou, override.Volume); err != nil {
		return fmt.Errorf("upsert participant override: %w", err)
	}
	return nil
}

func (s *GroupCallStore) GetParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64) (domain.GroupCallParticipantOverride, bool, error) {
	var ov domain.GroupCallParticipantOverride
	err := s.db.QueryRow(ctx,
		`SELECT muted_by_you, volume FROM group_call_participant_overrides WHERE call_id = $1 AND setter_user_id = $2 AND target_user_id = $3`,
		callID, setterUserID, targetUserID).Scan(&ov.MutedByYou, &ov.Volume)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCallParticipantOverride{}, false, nil
	}
	if err != nil {
		return domain.GroupCallParticipantOverride{}, false, fmt.Errorf("get participant override: %w", err)
	}
	return ov, true, nil
}

func (s *GroupCallStore) CreateConferenceInvite(ctx context.Context, invite domain.GroupCallInvite) (domain.GroupCallInvite, error) {
	if invite.CallID == 0 || invite.InviterUserID == 0 || invite.InviteeUserID == 0 || invite.MessageID == 0 {
		return domain.GroupCallInvite{}, domain.ErrGroupCallInvalid
	}
	if invite.Status == "" {
		invite.Status = domain.GroupCallInvitePending
	}
	var status string
	err := s.db.QueryRow(ctx, `
INSERT INTO group_call_invites (call_id, inviter_user_id, invitee_user_id, message_id, status, video, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (call_id, invitee_user_id, message_id) DO UPDATE SET
    inviter_user_id = EXCLUDED.inviter_user_id,
    video = EXCLUDED.video
RETURNING call_id, inviter_user_id, invitee_user_id, message_id, status, video, created_at, updated_at`,
		invite.CallID, invite.InviterUserID, invite.InviteeUserID, invite.MessageID, string(invite.Status), invite.Video, invite.CreatedAt, invite.UpdatedAt,
	).Scan(&invite.CallID, &invite.InviterUserID, &invite.InviteeUserID, &invite.MessageID, &status, &invite.Video, &invite.CreatedAt, &invite.UpdatedAt)
	if err != nil {
		return domain.GroupCallInvite{}, fmt.Errorf("create conference invite: %w", err)
	}
	invite.Status = domain.GroupCallInviteStatus(status)
	return invite, nil
}

func (s *GroupCallStore) SetConferenceInviteStatus(ctx context.Context, callID int64, inviteeUserID int64, msgID int, status domain.GroupCallInviteStatus, now int) (domain.GroupCallInvite, bool, error) {
	var inv domain.GroupCallInvite
	var newStatus string
	err := s.db.QueryRow(ctx, `
UPDATE group_call_invites
SET status = $4, updated_at = $5
WHERE call_id = $1 AND invitee_user_id = $2 AND message_id = $3
RETURNING call_id, inviter_user_id, invitee_user_id, message_id, status, video, created_at, updated_at`,
		callID, inviteeUserID, msgID, string(status), now,
	).Scan(&inv.CallID, &inv.InviterUserID, &inv.InviteeUserID, &inv.MessageID, &newStatus, &inv.Video, &inv.CreatedAt, &inv.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCallInvite{}, false, nil
	}
	if err != nil {
		return domain.GroupCallInvite{}, false, fmt.Errorf("set conference invite status: %w", err)
	}
	inv.Status = domain.GroupCallInviteStatus(newStatus)
	return inv, true, nil
}

func (s *GroupCallStore) ListConferenceRecipientUserIDs(ctx context.Context, callID int64) ([]int64, error) {
	rows, err := s.db.Query(ctx, `
WITH c AS (
    SELECT creator_user_id, state FROM group_calls WHERE call_id = $1
)
SELECT creator_user_id FROM c
UNION
SELECT p.user_id
FROM group_call_participants p CROSS JOIN c
WHERE p.call_id = $1 AND (c.state <> 'active' OR NOT p.left_call)
UNION
SELECT i.inviter_user_id
FROM group_call_invites i CROSS JOIN c
WHERE i.call_id = $1 AND (c.state <> 'active' OR i.status IN ('pending', 'accepted'))
UNION
SELECT i.invitee_user_id
FROM group_call_invites i CROSS JOIN c
WHERE i.call_id = $1 AND (c.state <> 'active' OR i.status IN ('pending', 'accepted'))
ORDER BY 1`, callID)
	if err != nil {
		return nil, fmt.Errorf("list conference recipients: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != 0 {
			out = append(out, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *GroupCallStore) AppendGroupCallChainBlock(ctx context.Context, block domain.GroupCallChainBlock) (domain.GroupCallChainBlock, error) {
	if block.CallID == 0 || len(block.Block) == 0 {
		return domain.GroupCallChainBlock{}, domain.ErrGroupCallInvalid
	}
	tx, err := s.begin(ctx, "append group call chain block")
	if err != nil {
		return domain.GroupCallChainBlock{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	call, err := lockGroupCallTx(ctx, tx, block.CallID)
	if err != nil {
		return domain.GroupCallChainBlock{}, err
	}
	if !call.Conference() {
		return domain.GroupCallChainBlock{}, domain.ErrGroupCallInvalid
	}
	block, err = appendGroupCallChainBlockTx(ctx, tx, block)
	if err != nil {
		return domain.GroupCallChainBlock{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.GroupCallChainBlock{}, fmt.Errorf("commit append group call chain block: %w", err)
	}
	committed = true
	return block, nil
}

func appendGroupCallChainBlockTx(ctx context.Context, tx pgx.Tx, block domain.GroupCallChainBlock) (domain.GroupCallChainBlock, error) {
	if block.CallID == 0 || len(block.Block) == 0 {
		return domain.GroupCallChainBlock{}, domain.ErrGroupCallInvalid
	}
	var existing domain.GroupCallChainBlock
	err := tx.QueryRow(ctx, `
SELECT call_id, sub_chain_id, block_offset, author_user_id, block, created_at
FROM group_call_chain_blocks
WHERE call_id = $1 AND sub_chain_id = $2 AND block = $3
ORDER BY block_offset ASC
LIMIT 1`, block.CallID, block.SubChainID, block.Block).Scan(
		&existing.CallID, &existing.SubChainID, &existing.Offset, &existing.AuthorUserID, &existing.Block, &existing.CreatedAt,
	)
	if err == nil {
		return domain.GroupCallChainBlock{}, domain.ErrConferenceChainInvalid
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.GroupCallChainBlock{}, fmt.Errorf("get existing group call chain block: %w", err)
	}
	var nextOffset int
	if err := tx.QueryRow(ctx, `
SELECT COALESCE(MAX(block_offset) + 1, 0)
FROM group_call_chain_blocks
WHERE call_id = $1 AND sub_chain_id = $2`, block.CallID, block.SubChainID).Scan(&nextOffset); err != nil {
		return domain.GroupCallChainBlock{}, fmt.Errorf("next chain block offset: %w", err)
	}
	if block.Offset < 0 {
		block.Offset = nextOffset
	}
	if block.Offset != nextOffset {
		return domain.GroupCallChainBlock{}, domain.ErrConferenceChainInvalid
	}
	err = tx.QueryRow(ctx, `
INSERT INTO group_call_chain_blocks (call_id, sub_chain_id, block_offset, author_user_id, block, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING call_id, sub_chain_id, block_offset, author_user_id, block, created_at`,
		block.CallID, block.SubChainID, block.Offset, block.AuthorUserID, block.Block, block.CreatedAt,
	).Scan(&block.CallID, &block.SubChainID, &block.Offset, &block.AuthorUserID, &block.Block, &block.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.GroupCallChainBlock{}, domain.ErrConferenceChainInvalid
		}
		return domain.GroupCallChainBlock{}, fmt.Errorf("append group call chain block: %w", err)
	}
	return block, nil
}

func (s *GroupCallStore) ListGroupCallChainBlocks(ctx context.Context, callID int64, subChainID, offset, limit int) (domain.GroupCallChainBlockPage, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if offset == domain.GroupCallChainBlockLatestOffset {
		var block domain.GroupCallChainBlock
		err := s.db.QueryRow(ctx, `
SELECT call_id, sub_chain_id, block_offset, author_user_id, block, created_at
FROM group_call_chain_blocks
WHERE call_id = $1 AND sub_chain_id = $2
ORDER BY block_offset DESC
LIMIT 1`, callID, subChainID).Scan(&block.CallID, &block.SubChainID, &block.Offset, &block.AuthorUserID, &block.Block, &block.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.GroupCallChainBlockPage{NextOffset: 0}, nil
		}
		if err != nil {
			return domain.GroupCallChainBlockPage{}, fmt.Errorf("get latest group call chain block: %w", err)
		}
		return domain.GroupCallChainBlockPage{
			Blocks:     []domain.GroupCallChainBlock{block},
			NextOffset: block.Offset + 1,
		}, nil
	}
	if offset < 0 {
		return domain.GroupCallChainBlockPage{}, domain.ErrGroupCallInvalid
	}
	rows, err := s.db.Query(ctx, `
SELECT call_id, sub_chain_id, block_offset, author_user_id, block, created_at
FROM group_call_chain_blocks
WHERE call_id = $1 AND sub_chain_id = $2 AND block_offset >= $3
ORDER BY block_offset ASC
LIMIT $4`, callID, subChainID, offset, limit)
	if err != nil {
		return domain.GroupCallChainBlockPage{}, fmt.Errorf("list group call chain blocks: %w", err)
	}
	defer rows.Close()
	page := domain.GroupCallChainBlockPage{NextOffset: offset}
	for rows.Next() {
		var block domain.GroupCallChainBlock
		if err := rows.Scan(&block.CallID, &block.SubChainID, &block.Offset, &block.AuthorUserID, &block.Block, &block.CreatedAt); err != nil {
			return domain.GroupCallChainBlockPage{}, err
		}
		page.Blocks = append(page.Blocks, block)
		page.NextOffset = block.Offset + 1
	}
	if err := rows.Err(); err != nil {
		return domain.GroupCallChainBlockPage{}, err
	}
	return page, nil
}
