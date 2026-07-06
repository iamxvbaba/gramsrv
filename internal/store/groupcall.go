package store

import (
	"context"

	"telesrv/internal/domain"
)

// GroupCallStore 持久化群通话信令真值（memory/postgres 双实现，行为契约由
// 共享 contract test 钉死；version 单调性见 domain/groupcall.go 注释）。
type GroupCallStore interface {
	// CreateGroupCall 建会；同频道已有活跃通话返回 domain.ErrGroupCallAlreadyStarted。
	CreateGroupCall(ctx context.Context, call domain.GroupCall) (domain.GroupCall, error)
	// CreateConferenceCall 建 ad-hoc conference；同一 creator+random_id 幂等返回既有活跃会。
	CreateConferenceCall(ctx context.Context, call domain.GroupCall) (domain.GroupCall, error)
	GetGroupCall(ctx context.Context, callID int64) (domain.GroupCall, bool, error)
	GetGroupCallBySlug(ctx context.Context, slug string) (domain.GroupCall, bool, error)
	GetGroupCallByInviteMessage(ctx context.Context, userID int64, msgID int) (domain.GroupCall, domain.GroupCallInvite, bool, error)
	// JoinGroupCall 加入/重进（同主键 upsert 换新 ssrc）；ssrc 与他人撞活跃唯一
	// 约束返回 domain.ErrGroupCallSSRCDuplicate；version++。
	JoinGroupCall(ctx context.Context, req domain.JoinGroupCallRequest) (domain.GroupCallMutation, error)
	// LeaveGroupCall 置 left+version++；conference 最后一名活跃参与者离开时同步转
	// discarded，普通 channel group call 允许空房间继续 active；未在会返回
	// domain.ErrGroupCallNotJoined。
	LeaveGroupCall(ctx context.Context, callID, userID int64, now int) (domain.GroupCallMutation, error)
	// RemoveConferenceCallParticipants 在同一事务内接受 conference E2E remove block、
	// 清理目标的 E2E 成员标记，并在 kick 时把活跃参与者置 left。
	RemoveConferenceCallParticipants(ctx context.Context, req domain.RemoveConferenceCallParticipantsRequest) (domain.RemoveConferenceCallParticipantsResult, error)
	// DiscardGroupCall 终结通话并清空参与者，返回终态 call 与此前活跃的参与者。
	DiscardGroupCall(ctx context.Context, callID int64, now int) (domain.GroupCall, []domain.GroupCallParticipant, error)
	// TouchParticipant 刷新 checkGroupCall 保活水位，返回该用户当前活跃 ssrc 集合
	//（joined=false 表示未在会，客户端据空集自动 rejoin）。
	TouchParticipant(ctx context.Context, callID, userID int64, now int) (activeSSRCs []int64, joined bool, err error)
	GetParticipant(ctx context.Context, callID, userID int64) (domain.GroupCallParticipant, bool, error)
	// ListParticipants 按 (join_date, user_id) 游标分页；offset 为上次返回的
	// NextOffset（空=从头）。响应携带当前 version（客户端跳号全量 reload 依赖）。
	ListParticipants(ctx context.Context, callID int64, offset string, limit int) (domain.GroupCallParticipantPage, error)
	// UpdateParticipant 应用字段级更新；changed=false 表示无有效变化（version 不动）。
	UpdateParticipant(ctx context.Context, callID, userID int64, update domain.GroupCallParticipantUpdate) (domain.GroupCallMutation, bool, error)
	// SetGroupCallTitle / SetGroupCallJoinMuted 只动 call 行，不动参与者 version。
	SetGroupCallTitle(ctx context.Context, callID int64, title string) (domain.GroupCall, bool, error)
	SetGroupCallJoinMuted(ctx context.Context, callID int64, joinMuted bool) (domain.GroupCall, bool, error)
	SetStartedMessageID(ctx context.Context, callID int64, msgID int) error
	// SweepStaleParticipants 清理保活水位早于 checkOlderThan 的活跃参与者
	//（每清一人 version++）。注意调用方必须叠加 SFU 媒体面活性做双过期判定。
	SweepStaleParticipants(ctx context.Context, checkOlderThan, now int, limit int) ([]domain.GroupCallMutation, error)
	// ResetAllParticipants 服务端重启恢复：把全部活跃通话的参与者批量置 left
	//（每通话 version++），conference 若因此变空则同步转 discarded，返回受影响的通话。
	ResetAllParticipants(ctx context.Context, now int) ([]domain.GroupCall, error)
	// NextRaiseHandRating 分配全局单调递增的举手序号（举手排序用）。
	NextRaiseHandRating(ctx context.Context, callID int64) (int64, error)
	// SetParticipantOverride 写入 setter 对 target 的 per-viewer 覆盖（本地静音/音量），
	// 仅影响 setter 自己的视图。clear=true 删除覆盖。
	SetParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64, override domain.GroupCallParticipantOverride, clear bool) error
	// GetParticipantOverride 取某 setter 对某 target 的覆盖。
	GetParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64) (domain.GroupCallParticipantOverride, bool, error)
	// CreateConferenceInvite 记录一条 conference 私聊邀请与其 message box id。
	CreateConferenceInvite(ctx context.Context, invite domain.GroupCallInvite) (domain.GroupCallInvite, error)
	SetConferenceInviteStatus(ctx context.Context, callID int64, inviteeUserID int64, msgID int, status domain.GroupCallInviteStatus, now int) (domain.GroupCallInvite, bool, error)
	// ListConferenceRecipientUserIDs 返回 conference 在线推送/访问候选人。
	// active 态只包含 creator、当前活跃参与者、pending/accepted invite 相关人；
	// discarded 态包含所有历史参与者与 invite 相关人，允许客户端收尾轮询读取终态。
	ListConferenceRecipientUserIDs(ctx context.Context, callID int64) ([]int64, error)
	AppendGroupCallChainBlock(ctx context.Context, block domain.GroupCallChainBlock) (domain.GroupCallChainBlock, error)
	ListGroupCallChainBlocks(ctx context.Context, callID int64, subChainID, offset, limit int) (domain.GroupCallChainBlockPage, error)
	// GetRtmpStreamKey / SetRtmpStreamKey 维护 per-channel 的持久 RTMP 推流密钥。
	// revoke 语义由上层实现：Set 覆盖旧 key，旧 key 推流即刻失效。
	GetRtmpStreamKey(ctx context.Context, channelID int64) (string, bool, error)
	SetRtmpStreamKey(ctx context.Context, channelID int64, key string, now int) error
	// StartScheduledGroupCall 清零 schedule_date（定时通话正式开始）。
	// changed=false 表示本来就已开始（幂等）；discarded 返回 ErrGroupCallDiscarded。
	StartScheduledGroupCall(ctx context.Context, callID int64) (domain.GroupCall, bool, error)
	// SetScheduleStartSubscription 写入/清除 userID 的开播提醒订阅。
	SetScheduleStartSubscription(ctx context.Context, callID, userID int64, subscribed bool) error
	// ListScheduleSubscriberIDs 返回订阅了开播提醒的 userID（升序）。
	ListScheduleSubscriberIDs(ctx context.Context, callID int64) ([]int64, error)
}
