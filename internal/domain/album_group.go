package domain

import (
	"crypto/sha256"
	"errors"
)

// ErrAlbumGroupReservationInvalid 表示相册分组预留缺少发送者、目标、
// random_id 或 proposed grouped_id。RPC 边界通常会更早拦截这些输入；
// domain/store 仍 fail-fast，避免坏绑定进入持久层。
var ErrAlbumGroupReservationInvalid = errors.New("album group reservation invalid")

// AlbumGroupReservationRequest 在任何相册 item 落库或上传媒体解析前，原子地把
// 一组 random_id 绑定到同一个 grouped_id。Peer 是幂等作用域的一部分：同一发送者
// 可以在不同会话中复用 random_id，而不会互相污染相册分组。
type AlbumGroupReservationRequest struct {
	SenderUserID      int64
	Peer              Peer
	Items             []AlbumGroupReservationItem
	ProposedGroupedID int64
}

// AlbumGroupReservationItem 把 random_id 与该 item 的不可变客户端意图绑定。
// IntentHash 是在媒体解析/服务端派生字段产生前计算的 SHA-256；相同 random_id
// 若携带不同意图必须报冲突，不能借旧 album reservation 绕过发送幂等校验。
type AlbumGroupReservationItem struct {
	RandomID   int64
	IntentHash []byte
}

// Validate 校验持久层必须依赖的最小不变量。同一批内重复 random_id 与发送幂等
// 冲突同义，必须显式失败，不能静默去重后改变客户端请求的消息条数。
func (r AlbumGroupReservationRequest) Validate() error {
	if r.SenderUserID <= 0 || r.Peer.ID <= 0 || r.ProposedGroupedID == 0 || len(r.Items) == 0 {
		return ErrAlbumGroupReservationInvalid
	}
	if r.Peer.Type != PeerTypeUser && r.Peer.Type != PeerTypeChannel {
		return ErrAlbumGroupReservationInvalid
	}
	seen := make(map[int64]struct{}, len(r.Items))
	for _, item := range r.Items {
		if item.RandomID == 0 || len(item.IntentHash) != sha256.Size {
			return ErrAlbumGroupReservationInvalid
		}
		if _, exists := seen[item.RandomID]; exists {
			return ErrMessageRandomIDDuplicate
		}
		seen[item.RandomID] = struct{}{}
	}
	return nil
}
