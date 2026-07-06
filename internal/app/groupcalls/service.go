// Package groupcalls 实现超级群语音聊天（group call）的信令业务层：
// ID/access_hash 分配与 store 编排。权限（admin/成员资格）由 rpc 层校验，
// version 单调性与并发串行化由 store 层事务保证。
package groupcalls

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Service 是群通话业务服务。
type Service struct {
	store store.GroupCallStore
}

// NewService 创建群通话服务。
func NewService(st store.GroupCallStore) *Service {
	return &Service{store: st}
}

// Create 分配 id/access_hash 并建会。rtmpStream=true 创建 RTMP 直播房间；
// joinMuted=true（广播频道直播）让非管理员入会即被静音且不可自解；
// scheduleDate>0 创建定时通话（客户端倒计时等待 startScheduled）。
func (s *Service) Create(ctx context.Context, channelID, creatorUserID int64, title string, rtmpStream, joinMuted bool, scheduleDate, now int) (domain.GroupCall, error) {
	id, err := randomPositiveInt64()
	if err != nil {
		return domain.GroupCall{}, err
	}
	accessHash, err := randomPositiveInt64()
	if err != nil {
		return domain.GroupCall{}, err
	}
	return s.store.CreateGroupCall(ctx, domain.GroupCall{
		ID:            id,
		AccessHash:    accessHash,
		ChannelID:     channelID,
		CreatorUserID: creatorUserID,
		Title:         title,
		RtmpStream:    rtmpStream,
		JoinMuted:     joinMuted,
		ScheduleDate:  scheduleDate,
		Version:       1,
		CreatedAt:     now,
	})
}

// StartScheduled 把定时通话转为进行中（清 schedule_date）；changed=false 幂等。
func (s *Service) StartScheduled(ctx context.Context, callID int64) (domain.GroupCall, bool, error) {
	return s.store.StartScheduledGroupCall(ctx, callID)
}

// SetScheduleSubscription 写入/清除开播提醒订阅。
func (s *Service) SetScheduleSubscription(ctx context.Context, callID, userID int64, subscribed bool) error {
	return s.store.SetScheduleStartSubscription(ctx, callID, userID, subscribed)
}

// ScheduleSubscriberIDs 返回订阅开播提醒的 userID。
func (s *Service) ScheduleSubscriberIDs(ctx context.Context, callID int64) ([]int64, error) {
	return s.store.ListScheduleSubscriberIDs(ctx, callID)
}

// RtmpStreamKey 返回 channel 的持久 RTMP 推流密钥；不存在或 rotate=true 时生成
// 新 key（覆盖写入，旧 key 即刻失效）。key 形如 "<channelID>_<hex>"，ingest 端
// 据前缀定位 channel、再整串比对鉴权。
func (s *Service) RtmpStreamKey(ctx context.Context, channelID int64, rotate bool, now int) (string, error) {
	if !rotate {
		key, found, err := s.store.GetRtmpStreamKey(ctx, channelID)
		if err != nil {
			return "", err
		}
		if found {
			return key, nil
		}
	}
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("groupcalls: random rtmp key: %w", err)
	}
	key := fmt.Sprintf("%d_%x", channelID, buf)
	if err := s.store.SetRtmpStreamKey(ctx, channelID, key, now); err != nil {
		return "", err
	}
	return key, nil
}

// VerifyRtmpStreamKey 校验推流密钥并返回其所属 channelID（RTMP ingest 鉴权入口）。
func (s *Service) VerifyRtmpStreamKey(ctx context.Context, key string) (int64, bool, error) {
	sep := strings.IndexByte(key, '_')
	if sep <= 0 {
		return 0, false, nil
	}
	channelID, err := strconv.ParseInt(key[:sep], 10, 64)
	if err != nil || channelID <= 0 {
		return 0, false, nil
	}
	stored, found, err := s.store.GetRtmpStreamKey(ctx, channelID)
	if err != nil {
		return 0, false, err
	}
	if !found || subtle.ConstantTimeCompare([]byte(stored), []byte(key)) != 1 {
		return 0, false, nil
	}
	return channelID, true, nil
}

// CreateConference 分配 id/access_hash/slug 并创建 ad-hoc conference call。
func (s *Service) CreateConference(ctx context.Context, creatorUserID, randomID, migratedFromPhoneCallID int64, now int) (domain.GroupCall, error) {
	for i := 0; i < 8; i++ {
		id, err := randomPositiveInt64()
		if err != nil {
			return domain.GroupCall{}, err
		}
		accessHash, err := randomPositiveInt64()
		if err != nil {
			return domain.GroupCall{}, err
		}
		slug, err := randomSlug()
		if err != nil {
			return domain.GroupCall{}, err
		}
		call, err := s.store.CreateConferenceCall(ctx, domain.GroupCall{
			ID:                      id,
			AccessHash:              accessHash,
			CreatorUserID:           creatorUserID,
			Kind:                    domain.GroupCallKindConference,
			Version:                 1,
			CreatedAt:               now,
			InviteSlug:              slug,
			InviteLink:              conferenceInviteLink(slug),
			RandomID:                randomID,
			MigratedFromPhoneCallID: migratedFromPhoneCallID,
		})
		if err == nil {
			return call, nil
		}
		if err != domain.ErrGroupCallInvalid {
			return domain.GroupCall{}, err
		}
	}
	return domain.GroupCall{}, fmt.Errorf("groupcalls: exhausted conference slug attempts")
}

func (s *Service) Get(ctx context.Context, callID int64) (domain.GroupCall, bool, error) {
	return s.store.GetGroupCall(ctx, callID)
}

func (s *Service) GetBySlug(ctx context.Context, slug string) (domain.GroupCall, bool, error) {
	return s.store.GetGroupCallBySlug(ctx, slug)
}

func (s *Service) GetByInviteMessage(ctx context.Context, userID int64, msgID int) (domain.GroupCall, domain.GroupCallInvite, bool, error) {
	return s.store.GetGroupCallByInviteMessage(ctx, userID, msgID)
}

func (s *Service) Join(ctx context.Context, req domain.JoinGroupCallRequest) (domain.GroupCallMutation, error) {
	return s.store.JoinGroupCall(ctx, req)
}

func (s *Service) Leave(ctx context.Context, callID, userID int64, now int) (domain.GroupCallMutation, error) {
	return s.store.LeaveGroupCall(ctx, callID, userID, now)
}

func (s *Service) RemoveConferenceParticipants(ctx context.Context, req domain.RemoveConferenceCallParticipantsRequest) (domain.RemoveConferenceCallParticipantsResult, error) {
	return s.store.RemoveConferenceCallParticipants(ctx, req)
}

func (s *Service) Discard(ctx context.Context, callID int64, now int) (domain.GroupCall, []domain.GroupCallParticipant, error) {
	return s.store.DiscardGroupCall(ctx, callID, now)
}

func (s *Service) Touch(ctx context.Context, callID, userID int64, now int) ([]int64, bool, error) {
	return s.store.TouchParticipant(ctx, callID, userID, now)
}

func (s *Service) Participant(ctx context.Context, callID, userID int64) (domain.GroupCallParticipant, bool, error) {
	return s.store.GetParticipant(ctx, callID, userID)
}

func (s *Service) Participants(ctx context.Context, callID int64, offset string, limit int) (domain.GroupCallParticipantPage, error) {
	return s.store.ListParticipants(ctx, callID, offset, limit)
}

func (s *Service) UpdateParticipant(ctx context.Context, callID, userID int64, update domain.GroupCallParticipantUpdate) (domain.GroupCallMutation, bool, error) {
	return s.store.UpdateParticipant(ctx, callID, userID, update)
}

func (s *Service) SetTitle(ctx context.Context, callID int64, title string) (domain.GroupCall, bool, error) {
	return s.store.SetGroupCallTitle(ctx, callID, title)
}

func (s *Service) SetJoinMuted(ctx context.Context, callID int64, joinMuted bool) (domain.GroupCall, bool, error) {
	return s.store.SetGroupCallJoinMuted(ctx, callID, joinMuted)
}

func (s *Service) SetStartedMessageID(ctx context.Context, callID int64, msgID int) error {
	return s.store.SetStartedMessageID(ctx, callID, msgID)
}

func (s *Service) SweepStale(ctx context.Context, checkOlderThan, now, limit int) ([]domain.GroupCallMutation, error) {
	return s.store.SweepStaleParticipants(ctx, checkOlderThan, now, limit)
}

func (s *Service) ResetAllParticipants(ctx context.Context, now int) ([]domain.GroupCall, error) {
	return s.store.ResetAllParticipants(ctx, now)
}

func (s *Service) NextRaiseHandRating(ctx context.Context, callID int64) (int64, error) {
	return s.store.NextRaiseHandRating(ctx, callID)
}

func (s *Service) SetParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64, override domain.GroupCallParticipantOverride, clear bool) error {
	return s.store.SetParticipantOverride(ctx, callID, setterUserID, targetUserID, override, clear)
}

func (s *Service) ParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64) (domain.GroupCallParticipantOverride, bool, error) {
	return s.store.GetParticipantOverride(ctx, callID, setterUserID, targetUserID)
}

func (s *Service) CreateConferenceInvite(ctx context.Context, invite domain.GroupCallInvite) (domain.GroupCallInvite, error) {
	return s.store.CreateConferenceInvite(ctx, invite)
}

func (s *Service) SetConferenceInviteStatus(ctx context.Context, callID, inviteeUserID int64, msgID int, status domain.GroupCallInviteStatus, now int) (domain.GroupCallInvite, bool, error) {
	return s.store.SetConferenceInviteStatus(ctx, callID, inviteeUserID, msgID, status, now)
}

func (s *Service) ConferenceRecipients(ctx context.Context, callID int64) ([]int64, error) {
	return s.store.ListConferenceRecipientUserIDs(ctx, callID)
}

func (s *Service) AppendChainBlock(ctx context.Context, block domain.GroupCallChainBlock) (domain.GroupCallChainBlock, error) {
	return s.store.AppendGroupCallChainBlock(ctx, block)
}

func (s *Service) ChainBlocks(ctx context.Context, callID int64, subChainID, offset, limit int) (domain.GroupCallChainBlockPage, error) {
	return s.store.ListGroupCallChainBlocks(ctx, callID, subChainID, offset, limit)
}

func randomPositiveInt64() (int64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, fmt.Errorf("groupcalls: random id: %w", err)
	}
	v := int64(binary.BigEndian.Uint64(buf[:]) >> 1)
	if v == 0 {
		v = 1
	}
	return v, nil
}

func randomSlug() (string, error) {
	var buf [18]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("groupcalls: random slug: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func conferenceInviteLink(slug string) string {
	return "https://telesrv.net/call/" + slug + "?slug=" + slug
}
