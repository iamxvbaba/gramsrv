package postgres

import (
	"context"
	"fmt"
	"telesrv/internal/domain"
)

// Global search uses the same total ordering as the cross-peer RPC merge.
// Media predicates use the maintained index before applying the bounded seek.
func (s *MessageStore) searchGlobalMessages(ctx context.Context, owner int64, f domain.MessageFilter) (domain.MessageList, error) {
	limit := f.Limit
	if limit <= 0 || limit > domain.MaxChannelGlobalSearchLimit+1 {
		limit = domain.MaxChannelGlobalSearchLimit
	}
	args := []any{owner}
	where := ` FROM message_boxes mb WHERE mb.owner_user_id=$1 AND NOT mb.deleted AND mb.peer_type='user'`
	add := func(clause string, value any) { args = append(args, value); where += fmt.Sprintf(clause, len(args)) }
	if f.Query != "" {
		add(` AND mb.body ILIKE $%d ESCAPE '\'`, "%"+escapeLike(f.Query)+"%")
	}
	if f.RestrictPeerIDs {
		add(` AND mb.peer_id=ANY($%d::bigint[])`, f.PeerIDs)
	}
	if f.MinDate > 0 {
		add(` AND mb.message_date>$%d`, f.MinDate)
	}
	if f.MaxDate > 0 {
		add(` AND mb.message_date<$%d`, f.MaxDate)
	}
	if f.HasFolderID {
		add(` AND EXISTS (SELECT 1 FROM dialogs d WHERE d.user_id=mb.owner_user_id AND d.peer_type=mb.peer_type AND d.peer_id=mb.peer_id AND d.folder_id=$%d)`, f.FolderID)
	}
	if len(f.MediaCategories) > 0 {
		add(` AND EXISTS (SELECT 1 FROM message_box_media mi WHERE mi.owner_user_id=mb.owner_user_id AND mi.box_id=mb.box_id AND mi.category=ANY($%d::smallint[]))`, mediaCategoriesToInt16(f.MediaCategories))
	}
	if f.OffsetDate > 0 {
		if f.OffsetPeer.ID == 0 {
			add(` AND mb.message_date<$%d`, f.OffsetDate)
		} else {
			args = append(args, f.OffsetDate, f.OffsetPeer.ID)
			n := len(args)
			if f.OffsetPeer.Type == domain.PeerTypeChannel {
				where += fmt.Sprintf(` AND (mb.message_date,mb.peer_id)<=($%d,$%d)`, n-1, n)
			} else {
				args = append(args, f.OffsetID)
				where += fmt.Sprintf(` AND (mb.message_date,mb.peer_id,mb.box_id)<($%d,$%d,$%d)`, n-1, n, n+1)
			}
		}
	} else if f.OffsetID > 0 {
		add(` AND mb.box_id<$%d`, f.OffsetID)
	}
	args = append(args, limit+1)
	rows, err := s.db.Query(ctx, `SELECT mb.box_id`+where+` ORDER BY mb.message_date DESC,mb.peer_id DESC,mb.box_id DESC LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return domain.MessageList{}, fmt.Errorf("global private search: %w", err)
	}
	ids := make([]int, 0, limit+1)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return domain.MessageList{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return domain.MessageList{}, err
	}
	count := len(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	list, err := s.GetByIDs(ctx, owner, ids)
	if err != nil {
		return domain.MessageList{}, err
	}
	list.Messages = reorderMessagesByID(list.Messages, ids)
	list.Count = count
	return list, nil
}
