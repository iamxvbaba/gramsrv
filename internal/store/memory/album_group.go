package memory

import (
	"bytes"
	"context"

	"telesrv/internal/domain"
)

type albumGroupKey struct {
	senderUserID int64
	peerType     domain.PeerType
	peerID       int64
	randomID     int64
}

type albumGroupRecord struct {
	groupedID  int64
	intentHash [32]byte
}

// ReserveAlbumGroup 在 MessageStore 的同一把互斥锁下完成读旧组、选胜者与补齐绑定，
// 因而并发重叠批次不会产生拆组。
func (s *MessageStore) ReserveAlbumGroup(_ context.Context, req domain.AlbumGroupReservationRequest) (int64, error) {
	if err := req.Validate(); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.albumGroups == nil {
		s.albumGroups = make(map[albumGroupKey]albumGroupRecord)
	}

	groupedID := int64(0)
	type pendingBinding struct {
		key        albumGroupKey
		intentHash [32]byte
	}
	bindings := make([]pendingBinding, 0, len(req.Items))
	for _, item := range req.Items {
		key := albumGroupKey{
			senderUserID: req.SenderUserID,
			peerType:     req.Peer.Type,
			peerID:       req.Peer.ID,
			randomID:     item.RandomID,
		}
		var intentHash [32]byte
		copy(intentHash[:], item.IntentHash)
		bindings = append(bindings, pendingBinding{key: key, intentHash: intentHash})
		if existing, exists := s.albumGroups[key]; exists {
			if !bytes.Equal(existing.intentHash[:], item.IntentHash) {
				return 0, domain.ErrMessageRandomIDDuplicate
			}
			if groupedID != 0 && groupedID != existing.groupedID {
				return 0, domain.ErrMessageRandomIDDuplicate
			}
			groupedID = existing.groupedID
		}
	}
	if groupedID == 0 {
		groupedID = req.ProposedGroupedID
	}
	for _, binding := range bindings {
		s.albumGroups[binding.key] = albumGroupRecord{groupedID: groupedID, intentHash: binding.intentHash}
	}
	return groupedID, nil
}
