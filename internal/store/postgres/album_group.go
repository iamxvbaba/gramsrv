package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"telesrv/internal/domain"
)

// ReserveAlbumGroup 先按稳定顺序获取整批 key 的事务级 advisory locks，再读取旧绑定
// 并一次性补齐缺失项。锁覆盖不存在的行，因此避免单靠 UNIQUE/ON CONFLICT 时两个实例
// 对重叠批次分别选出不同 grouped_id 的 write-skew。
func (s *MessageStore) ReserveAlbumGroup(ctx context.Context, req domain.AlbumGroupReservationRequest) (int64, error) {
	if err := req.Validate(); err != nil {
		return 0, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return 0, errors.New("reserve album group requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("reserve album group begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lockIDs := albumGroupAdvisoryLockIDs(req)
	for _, lockID := range lockIDs {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockID); err != nil {
			return 0, fmt.Errorf("reserve album group lock: %w", err)
		}
	}

	randomIDs := make([]int64, 0, len(req.Items))
	requestedIntents := make(map[int64][]byte, len(req.Items))
	for _, item := range req.Items {
		randomIDs = append(randomIDs, item.RandomID)
		requestedIntents[item.RandomID] = item.IntentHash
	}
	rows, err := tx.Query(ctx, `
SELECT random_id, grouped_id, intent_hash
FROM album_group_reservations
WHERE sender_user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND random_id = ANY($4::bigint[])
ORDER BY random_id`, req.SenderUserID, string(req.Peer.Type), req.Peer.ID, randomIDs)
	if err != nil {
		return 0, fmt.Errorf("reserve album group read existing: %w", err)
	}
	existingGroups := make(map[int64]struct{}, 2)
	for rows.Next() {
		var randomID int64
		var groupedID int64
		var intentHash []byte
		if err := rows.Scan(&randomID, &groupedID, &intentHash); err != nil {
			rows.Close()
			return 0, fmt.Errorf("reserve album group scan existing: %w", err)
		}
		if !bytes.Equal(intentHash, requestedIntents[randomID]) {
			rows.Close()
			return 0, fmt.Errorf("%w: album random_id %d intent changed", domain.ErrMessageRandomIDDuplicate, randomID)
		}
		existingGroups[groupedID] = struct{}{}
	}
	readErr := rows.Err()
	rows.Close()
	if readErr != nil {
		return 0, fmt.Errorf("reserve album group iterate existing: %w", readErr)
	}
	if len(existingGroups) > 1 {
		return 0, fmt.Errorf("%w: album request spans multiple grouped_id values", domain.ErrMessageRandomIDDuplicate)
	}

	groupedID := req.ProposedGroupedID
	for existingGroup := range existingGroups {
		groupedID = existingGroup
	}
	for _, item := range req.Items {
		if _, err := tx.Exec(ctx, `
INSERT INTO album_group_reservations (
    sender_user_id, peer_type, peer_id, random_id, intent_hash, grouped_id
)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (sender_user_id, peer_type, peer_id, random_id) DO NOTHING`,
			req.SenderUserID, string(req.Peer.Type), req.Peer.ID, item.RandomID, item.IntentHash, groupedID); err != nil {
			return 0, fmt.Errorf("reserve album group insert binding: %w", err)
		}
	}

	// 防御性复核：advisory lock 协议若被未来代码绕过，也不能把拆组状态作为成功返回。
	rows, err = tx.Query(ctx, `
SELECT random_id, grouped_id, intent_hash
FROM album_group_reservations
WHERE sender_user_id = $1
  AND peer_type = $2
  AND peer_id = $3
  AND random_id = ANY($4::bigint[])`,
		req.SenderUserID, string(req.Peer.Type), req.Peer.ID, randomIDs)
	if err != nil {
		return 0, fmt.Errorf("reserve album group verify: %w", err)
	}
	verified := 0
	for rows.Next() {
		var randomID, storedGroup int64
		var intentHash []byte
		if err := rows.Scan(&randomID, &storedGroup, &intentHash); err != nil {
			rows.Close()
			return 0, fmt.Errorf("reserve album group verify scan: %w", err)
		}
		if storedGroup != groupedID || !bytes.Equal(intentHash, requestedIntents[randomID]) {
			rows.Close()
			return 0, fmt.Errorf("%w: album reservation diverged for random_id %d", domain.ErrMessageRandomIDDuplicate, randomID)
		}
		verified++
	}
	verifyErr := rows.Err()
	rows.Close()
	if verifyErr != nil {
		return 0, fmt.Errorf("reserve album group verify iterate: %w", verifyErr)
	}
	if verified != len(req.Items) {
		return 0, fmt.Errorf("%w: album reservation count=%d/%d", domain.ErrMessageRandomIDDuplicate, verified, len(req.Items))
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("reserve album group commit: %w", err)
	}
	return groupedID, nil
}

// albumGroupAdvisoryLockIDs 对每个业务 key 派生一个 64-bit advisory lock，并按数值
// 排序、去重。hash 碰撞最多造成无害串行化；排序保证重叠批次不会互相反序死锁。
func albumGroupAdvisoryLockIDs(req domain.AlbumGroupReservationRequest) []int64 {
	ids := make([]int64, 0, len(req.Items))
	for _, item := range req.Items {
		h := sha256.New()
		_, _ = h.Write([]byte("telesrv:album-group:v1\x00"))
		var word [8]byte
		binary.BigEndian.PutUint64(word[:], uint64(req.SenderUserID))
		_, _ = h.Write(word[:])
		_, _ = h.Write([]byte(req.Peer.Type))
		binary.BigEndian.PutUint64(word[:], uint64(req.Peer.ID))
		_, _ = h.Write(word[:])
		binary.BigEndian.PutUint64(word[:], uint64(item.RandomID))
		_, _ = h.Write(word[:])
		sum := h.Sum(nil)
		ids = append(ids, int64(binary.BigEndian.Uint64(sum[:8])))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := ids[:0]
	for _, id := range ids {
		if len(out) == 0 || out[len(out)-1] != id {
			out = append(out, id)
		}
	}
	return out
}
