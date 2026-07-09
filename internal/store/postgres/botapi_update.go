package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// BotAPIUpdateStore persists Bot API getUpdates queues in PostgreSQL.
type BotAPIUpdateStore struct {
	db sqlcgen.DBTX
}

func NewBotAPIUpdateStore(db sqlcgen.DBTX) *BotAPIUpdateStore {
	return &BotAPIUpdateStore{db: db}
}

func (s *BotAPIUpdateStore) EnqueueBotAPIUpdate(ctx context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error) {
	if err := validateBotAPIUpdateRequest(req); err != nil {
		return domain.BotAPIUpdate{}, false, err
	}
	row, err := s.scanBotAPIUpdate(s.db.QueryRow(ctx, `
INSERT INTO bot_api_updates (
  bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts) DO NOTHING
RETURNING id, bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date
`, req.BotUserID, string(req.Kind), string(req.Peer.Type), req.Peer.ID, req.MessageID, req.SourcePts, req.Date))
	if err == nil {
		return row, true, nil
	}
	if err != pgx.ErrNoRows {
		return domain.BotAPIUpdate{}, false, fmt.Errorf("insert bot api update: %w", err)
	}
	row, err = s.scanBotAPIUpdate(s.db.QueryRow(ctx, `
SELECT id, bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date
FROM bot_api_updates
WHERE bot_user_id = $1
  AND update_kind = $2
  AND peer_type = $3
  AND peer_id = $4
  AND message_id = $5
  AND source_pts = $6
`, req.BotUserID, string(req.Kind), string(req.Peer.Type), req.Peer.ID, req.MessageID, req.SourcePts))
	if err != nil {
		return domain.BotAPIUpdate{}, false, fmt.Errorf("select existing bot api update: %w", err)
	}
	return row, false, nil
}

func (s *BotAPIUpdateStore) ListBotAPIUpdates(ctx context.Context, botUserID, fromUpdateID int64, limit int) ([]domain.BotAPIUpdate, error) {
	if botUserID == 0 {
		return nil, nil
	}
	if fromUpdateID <= 0 {
		fromUpdateID = 1
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `
SELECT id, bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date
FROM bot_api_updates
WHERE bot_user_id = $1 AND id >= $2
ORDER BY id
LIMIT $3
`, botUserID, fromUpdateID, limit)
	if err != nil {
		return nil, fmt.Errorf("list bot api updates: %w", err)
	}
	defer rows.Close()
	out := make([]domain.BotAPIUpdate, 0, limit)
	for rows.Next() {
		item, err := scanBotAPIUpdateRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list bot api updates rows: %w", err)
	}
	return out, nil
}

func (s *BotAPIUpdateStore) ConfirmBotAPIUpdates(ctx context.Context, botUserID, confirmedUpdateID int64) error {
	if botUserID == 0 || confirmedUpdateID <= 0 {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO bot_api_update_states (bot_user_id, confirmed_update_id)
VALUES ($1, $2)
ON CONFLICT (bot_user_id) DO UPDATE
SET confirmed_update_id = GREATEST(bot_api_update_states.confirmed_update_id, EXCLUDED.confirmed_update_id),
    updated_at = now()
WHERE bot_api_update_states.confirmed_update_id < EXCLUDED.confirmed_update_id
`, botUserID, confirmedUpdateID); err != nil {
		return fmt.Errorf("confirm bot api updates: %w", err)
	}
	return nil
}

// DeleteDeliveredOrExpired 回收 Bot API 投递队列的死行（性能审计 H1）：
//  1. 已确认（id <= bot_api_update_states.confirmed_update_id）且入队超过 confirmedGrace 的行——
//     官方 Bot API 语义下确认即弃，getUpdates 的 fromID 恒 > confirmed，删除不影响任何读路径；
//     宽限仅防御 offset 回拨调试场景。
//  2. 按消息 date 超过 maxAge 的行（无论确认与否）——对齐官方「updates 服务器最多保留 24 小时」
//     语义，同时封顶 MTProto-only bot（从不调 getUpdates、无 state 行）成员身份带来的无界增长。
//
// 与 user_update_events 的「永久保留」约束无关：那是 TDesktop 账号级 differenceTooLong 缺陷所迫，
// Bot API 队列没有该约束。返回两步合计删除行数。
func (s *BotAPIUpdateStore) DeleteDeliveredOrExpired(ctx context.Context, confirmedGrace, maxAge time.Duration, limit int) (int, error) {
	if limit <= 0 {
		limit = 10000
	}
	if limit > 100000 {
		limit = 100000
	}
	total := 0
	if confirmedGrace > 0 {
		// 从 states 小表出发，每 bot 走 bot_api_updates_bot_scan_idx(bot_user_id, id) 范围扫描。
		tag, err := s.db.Exec(ctx, `
DELETE FROM bot_api_updates
WHERE id IN (
    SELECT u.id
    FROM bot_api_update_states s
    JOIN bot_api_updates u ON u.bot_user_id = s.bot_user_id AND u.id <= s.confirmed_update_id
    WHERE u.created_at < now() - make_interval(secs => $1)
    LIMIT $2
)`, int64(confirmedGrace/time.Second), limit)
		if err != nil {
			return total, fmt.Errorf("delete confirmed bot api updates: %w", err)
		}
		total += int(tag.RowsAffected())
	}
	if maxAge > 0 {
		cutoff := time.Now().Add(-maxAge).Unix()
		// 走 bot_api_updates_retention_idx(date, id)。
		tag, err := s.db.Exec(ctx, `
DELETE FROM bot_api_updates
WHERE id IN (
    SELECT id
    FROM bot_api_updates
    WHERE date < $1
    ORDER BY date, id
    LIMIT $2
)`, cutoff, limit)
		if err != nil {
			return total, fmt.Errorf("delete expired bot api updates: %w", err)
		}
		total += int(tag.RowsAffected())
	}
	return total, nil
}

func (s *BotAPIUpdateStore) ConfirmedBotAPIUpdateID(ctx context.Context, botUserID int64) (int64, bool, error) {
	if botUserID == 0 {
		return 0, false, nil
	}
	var id int64
	if err := s.db.QueryRow(ctx, `
SELECT confirmed_update_id
FROM bot_api_update_states
WHERE bot_user_id = $1
`, botUserID).Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get bot api update state: %w", err)
	}
	return id, true, nil
}

func (s *BotAPIUpdateStore) scanBotAPIUpdate(row pgx.Row) (domain.BotAPIUpdate, error) {
	return scanBotAPIUpdateRows(row)
}

type botAPIUpdateScanner interface {
	Scan(dest ...any) error
}

func scanBotAPIUpdateRows(row botAPIUpdateScanner) (domain.BotAPIUpdate, error) {
	var item domain.BotAPIUpdate
	var kind, peerType string
	if err := row.Scan(&item.ID, &item.BotUserID, &kind, &peerType, &item.Peer.ID, &item.MessageID, &item.SourcePts, &item.Date); err != nil {
		return domain.BotAPIUpdate{}, err
	}
	item.Kind = domain.BotAPIUpdateKind(kind)
	item.Peer.Type = domain.PeerType(peerType)
	return item, nil
}

func validateBotAPIUpdateRequest(req domain.EnqueueBotAPIUpdateRequest) error {
	if req.BotUserID == 0 || req.MessageID <= 0 {
		return fmt.Errorf("invalid bot api update")
	}
	if req.Kind != domain.BotAPIUpdateMessage && req.Kind != domain.BotAPIUpdateEditedMessage {
		return fmt.Errorf("invalid bot api update kind %q", req.Kind)
	}
	switch req.Peer.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		if req.Peer.ID <= 0 {
			return fmt.Errorf("invalid bot api update peer")
		}
	default:
		return fmt.Errorf("invalid bot api update peer type %q", req.Peer.Type)
	}
	return nil
}
