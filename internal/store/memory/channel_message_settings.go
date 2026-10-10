package memory

import (
	"context"
	"telesrv/internal/domain"
	"time"
)

func (s *ChannelStore) SetPreHistoryHidden(_ context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if member.Role != domain.ChannelRoleCreator {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	prev := channel.PreHistoryHidden
	channel.PreHistoryHidden = enabled
	s.channels[channelID] = channel
	if prev != enabled {
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      int(time.Now().Unix()),
			Type:      domain.ChannelAdminLogTogglePreHistoryHidden,
			PrevBool:  prev,
			NewBool:   enabled,
		})
	}
	return channel, nil
}

func (s *ChannelStore) SetPaidMessagesPrice(_ context.Context, userID, channelID int64, stars int64, broadcastMessagesAllowed bool) (domain.ChannelPaidMessagesPriceResult, error) {
	if userID == 0 || channelID == 0 || stars < 0 {
		return domain.ChannelPaidMessagesPriceResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.ChannelPaidMessagesPriceResult{}, err
	}
	member := s.members[channelID][userID]
	if !canChangeChannelInfo(member) {
		return domain.ChannelPaidMessagesPriceResult{}, domain.ErrChannelAdminRequired
	}
	out := domain.ChannelPaidMessagesPriceResult{}
	prevAllowed := channel.BroadcastMessagesAllowed
	prevStars := channel.SendPaidMessagesStars
	prevLinked := channel.LinkedMonoforumID
	channel.SendPaidMessagesStars = stars
	channel.BroadcastMessagesAllowed = channel.Broadcast && broadcastMessagesAllowed
	changed := channel.Broadcast && (prevAllowed != channel.BroadcastMessagesAllowed || prevStars != stars || (channel.BroadcastMessagesAllowed && prevLinked == 0))
	// 开启频道私信时关联 monoforum 虚拟频道(与 postgres 行为一致);复用既有则只同步价格。
	if channel.BroadcastMessagesAllowed {
		if channel.LinkedMonoforumID == 0 {
			monoID := s.nextChannelIDLocked()
			s.channels[monoID] = domain.Channel{
				ID:            monoID,
				AccessHash:    s.nextAccessHashLocked(),
				CreatorUserID: channel.CreatorUserID,
				Title:         channel.Title,
				// Keep monoforum as a megagroup-like saved sublist container. TDesktop hangs when
				// a monoforum peer is projected as both broadcast and megagroup.
				Megagroup:             true,
				Monoforum:             true,
				LinkedMonoforumID:     channelID,
				SendPaidMessagesStars: stars,
				Date:                  int(time.Now().Unix()),
			}
			channel.LinkedMonoforumID = monoID
		} else if mono, ok := s.channels[channel.LinkedMonoforumID]; ok {
			// 复用既有 monoforum 只同步价格。mono 恒以 megagroup 形态创建、无路径会翻成 broadcast/forum,
			// 故无需再规范化 kind(那是对从未发布的 broadcast+monoforum 实验形态的历史数据修复死代码)。
			// 与 postgres 保持一致。
			mono.SendPaidMessagesStars = stars
			s.channels[channel.LinkedMonoforumID] = mono
		}
		// monoforum 首条(也是唯一)服务消息=创建消息(messageActionChannelCreate),客户端对
		// monoforum 渲染为 "Direct messages were enabled in this channel."。开关/价格变更的
		// paid_messages_price 只进母广播频道(渲染 "Channel enabled/disabled Direct Messages"),
		// **绝不进 mono**——mono 是 megagroup(非 broadcast),同一 action 会被渲染成"消息免费/
		// 设为 N 星"的错误文案;关闭只靠 monoforumDisabled 状态显示停用页脚。与 postgres 一致。
		if existing := s.channels[channel.LinkedMonoforumID]; existing.ID != 0 && existing.TopMessageID == 0 {
			createMsg, createEvent := s.appendChannelServiceMessageLocked(channel.LinkedMonoforumID, userID, int(time.Now().Unix()), domain.ChannelMessageAction{Type: domain.ChannelActionCreate, Title: existing.Title})
			mono := s.channels[channel.LinkedMonoforumID]
			mono.TopMessageID = createMsg.ID
			mono.Pts = createEvent.Pts
			s.channels[channel.LinkedMonoforumID] = mono
			out.ServiceMessages = append(out.ServiceMessages, domain.SendChannelMessageResult{Channel: cloneChannel(mono), Message: cloneChannelMessage(createMsg), Event: cloneChannelEvent(createEvent)})
		}
	}
	// monoforum 镜像母频道 DM 启用状态到自己的 BroadcastMessagesAllowed(与 postgres 一致):投影时据此
	// 决定是否下发 mono 的 linked_monoforum_id,关闭时双方都隐藏 link → 打开 monoforum 重拉不会清掉
	// MonoforumDisabled,停用页脚保持。内部关联行仍保留以便重新开启复用。
	if channel.LinkedMonoforumID != 0 {
		if mono, ok := s.channels[channel.LinkedMonoforumID]; ok {
			mono.BroadcastMessagesAllowed = channel.BroadcastMessagesAllowed
			s.channels[channel.LinkedMonoforumID] = mono
		}
	}
	if changed && channel.LinkedMonoforumID != 0 {
		action := domain.ChannelMessageAction{
			Type:                     domain.ChannelActionPaidMessagesPrice,
			BroadcastMessagesAllowed: channel.BroadcastMessagesAllowed,
			Stars:                    stars,
		}
		parentMsg, parentEvent := s.appendChannelServiceMessageLocked(channelID, userID, int(time.Now().Unix()), action)
		channel.TopMessageID = parentMsg.ID
		channel.Pts = parentEvent.Pts
		out.ServiceMessages = append(out.ServiceMessages, domain.SendChannelMessageResult{Channel: cloneChannel(channel), Message: cloneChannelMessage(parentMsg), Event: cloneChannelEvent(parentEvent)})
	}
	if len(out.ServiceMessages) > 0 {
		out.ServiceMessage = &out.ServiceMessages[0]
	}
	s.channels[channelID] = channel
	out.Channel = cloneChannel(channel)
	return out, nil
}
