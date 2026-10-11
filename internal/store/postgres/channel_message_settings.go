package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
)

func (s *ChannelStore) SetPreHistoryHidden(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	var channel domain.Channel
	if err := withTx(ctx, s.db, "toggle channel prehistory", func(tx pgx.Tx) error {
		var err error
		var member domain.ChannelMember
		channel, member, err = s.getChannelForMember(ctx, tx, userID, channelID)
		if err != nil {
			return err
		}
		if member.Role != domain.ChannelRoleCreator {
			return domain.ErrChannelAdminRequired
		}
		prev := channel.PreHistoryHidden
		if _, err := tx.Exec(ctx, `UPDATE channels SET pre_history_hidden = $2, updated_at = now() WHERE id = $1`, channelID, enabled); err != nil {
			return fmt.Errorf("update channel prehistory: %w", err)
		}
		channel.PreHistoryHidden = enabled
		if prev != enabled {
			if err := s.insertChannelAdminLogTx(ctx, tx, domain.ChannelAdminLogEvent{
				ChannelID: channelID,
				UserID:    userID,
				Date:      nowUnix(),
				Type:      domain.ChannelAdminLogTogglePreHistoryHidden,
				PrevBool:  prev,
				NewBool:   enabled,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return domain.Channel{}, err
	}
	return channel, nil
}

func (s *ChannelStore) SetPaidMessagesPrice(ctx context.Context, userID, channelID int64, stars int64, broadcastMessagesAllowed bool) (domain.ChannelPaidMessagesPriceResult, error) {
	if userID == 0 || channelID == 0 || stars < 0 {
		return domain.ChannelPaidMessagesPriceResult{}, domain.ErrChannelInvalid
	}
	var channel domain.Channel
	services := make([]domain.SendChannelMessageResult, 0, 2)
	if err := withTx(ctx, s.db, "update channel paid messages price", func(tx pgx.Tx) error {
		var err error
		var member domain.ChannelMember
		channel, member, err = s.getChannelForMember(ctx, tx, userID, channelID)
		if err != nil {
			return err
		}
		if !canChangeChannelInfo(member) {
			return domain.ErrChannelAdminRequired
		}
		prevAllowed := channel.BroadcastMessagesAllowed
		prevStars := channel.SendPaidMessagesStars
		prevLinked := channel.LinkedMonoforumID
		broadcastAllowed := channel.Broadcast && broadcastMessagesAllowed
		if _, err := tx.Exec(ctx, `UPDATE channels SET send_paid_messages_stars = $2, broadcast_messages_allowed = $3, updated_at = now() WHERE id = $1`, channelID, stars, broadcastAllowed); err != nil {
			return fmt.Errorf("update channel paid messages price: %w", err)
		}
		channel.SendPaidMessagesStars = stars
		channel.BroadcastMessagesAllowed = broadcastAllowed
		changed := channel.Broadcast && (prevAllowed != broadcastAllowed || prevStars != stars || (broadcastAllowed && prevLinked == 0))
		// 开启频道私信(Direct Messages)时为母频道关联一个 monoforum 虚拟频道;再次开启复用既有
		// monoforum,仅同步价格。关闭(broadcastAllowed=false)保留 monoforum 以便重新开启时复用。
		var mono domain.Channel
		if broadcastAllowed {
			if channel.LinkedMonoforumID == 0 {
				monoID, err := s.allocateFreshChannelID(ctx)
				if err != nil {
					return err
				}
				accessHash, err := randomChannelAccessHash()
				if err != nil {
					return err
				}
				mono = domain.Channel{
					ID:            monoID,
					AccessHash:    accessHash,
					CreatorUserID: channel.CreatorUserID,
					Title:         channel.Title,
					// Keep monoforum as a megagroup-like saved sublist container. TDesktop hangs when
					// a monoforum peer is projected as both broadcast and megagroup.
					Megagroup:             true,
					Monoforum:             true,
					LinkedMonoforumID:     channelID,
					SendPaidMessagesStars: stars,
					Date:                  nowUnix(),
				}
				if err := insertChannelTx(ctx, tx, mono); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE channels SET linked_monoforum_id = $2, updated_at = now() WHERE id = $1`, channelID, monoID); err != nil {
					return fmt.Errorf("link channel monoforum: %w", err)
				}
				channel.LinkedMonoforumID = monoID
			} else {
				// 复用既有 monoforum 只同步价格。创建路径恒以 megagroup 形态建 mono,无任何路径会把它
				// 翻成 broadcast/forum,故此处无需(也不应)再规范化 kind——那是对从未发布的 broadcast+
				// monoforum 实验形态的历史数据修复,在无遗留数据的项目里是死代码。
				if _, err := tx.Exec(ctx, `UPDATE channels SET send_paid_messages_stars = $2, updated_at = now() WHERE id = $1`, channel.LinkedMonoforumID, stars); err != nil {
					return fmt.Errorf("sync monoforum paid messages price: %w", err)
				}
				mono, err = getChannelByID(ctx, tx, channel.LinkedMonoforumID)
				if err != nil {
					return fmt.Errorf("load monoforum for paid messages service: %w", err)
				}
			}
			// monoforum 的首条(也是唯一)服务消息是它的"创建"消息(messageActionChannelCreate):
			// 客户端对 monoforum 专门把创建消息渲染为 "Direct messages were enabled in this channel."
			// (TDesktop lng_action_created_monoforum)。开关/价格变更的 paid_messages_price 服务消息
			// 只进母广播频道(在 broadcast 历史里渲染为 "Channel enabled/disabled Direct Messages"),
			// **绝不进 mono**——mono 是 megagroup(非 broadcast),同一 action 会被渲染成"消息免费/
			// 设为 N 星"的错误文案。关闭仅靠 monoforumDisabled 状态(隐藏母频道 linked_monoforum_id)
			// 显示停用页脚,不再往 mono 追加气泡。
			if mono.ID != 0 && mono.TopMessageID == 0 {
				createMsg, createEvent, err := s.insertServiceMessage(ctx, tx, mono, userID, nowUnix(), domain.ChannelMessageAction{Type: domain.ChannelActionCreate, Title: mono.Title})
				if err != nil {
					return fmt.Errorf("append monoforum creation service: %w", err)
				}
				mono.TopMessageID = createMsg.ID
				mono.Pts = createEvent.Pts
				services = append(services, domain.SendChannelMessageResult{Channel: mono, Message: createMsg, Event: createEvent})
			}
		}
		// monoforum 把母频道的 DM 启用状态镜像到自己的 broadcast_messages_allowed 列。投影时据此决定
		// 是否下发 mono 自身的 linked_monoforum_id:开启=下发,关闭=隐藏。关闭时母频道与 mono 双方都
		// 隐藏 link,客户端打开 monoforum 重新拉取时不会重新 setMonoforumLink(parent) 而清掉 MonoforumDisabled,
		// 停用页脚得以保持。内部关联行(linked_monoforum_id)仍保留以便重新开启复用。
		// 仅在 mono 的 DM 启用状态实际变化时才写镜像列:避免每次 SetPaidMessagesPrice(含未变更的
		// 再次开启)都对 mono 行白写一遍并触发 channels 表多余的 LISTEN/NOTIFY 跨实例失效。开启分支已
		// 把 mono 经 getChannelByID 读入(mono.ID!=0,字段反映库内当前值);关闭分支不加载 mono(mono.ID==0)
		// 故无法廉价比对、保持照写以确保停用页脚状态正确。
		if channel.LinkedMonoforumID != 0 && (mono.ID == 0 || mono.BroadcastMessagesAllowed != broadcastAllowed) {
			if _, err := tx.Exec(ctx, `UPDATE channels SET broadcast_messages_allowed = $2, updated_at = now() WHERE id = $1`, channel.LinkedMonoforumID, broadcastAllowed); err != nil {
				return fmt.Errorf("sync monoforum dm-enabled state: %w", err)
			}
			if mono.ID != 0 {
				mono.BroadcastMessagesAllowed = broadcastAllowed
			}
		}
		if changed && channel.LinkedMonoforumID != 0 {
			action := domain.ChannelMessageAction{
				Type:                     domain.ChannelActionPaidMessagesPrice,
				BroadcastMessagesAllowed: broadcastAllowed,
				Stars:                    stars,
			}
			parentMsg, parentEvent, err := s.insertServiceMessage(ctx, tx, channel, userID, nowUnix(), action)
			if err != nil {
				return fmt.Errorf("append parent paid messages price service: %w", err)
			}
			channel.TopMessageID = parentMsg.ID
			channel.Pts = parentEvent.Pts
			services = append(services, domain.SendChannelMessageResult{Channel: channel, Message: parentMsg, Event: parentEvent})
		}
		return nil
	}); err != nil {
		return domain.ChannelPaidMessagesPriceResult{}, err
	}
	var first *domain.SendChannelMessageResult
	if len(services) > 0 {
		first = &services[0]
	}
	return domain.ChannelPaidMessagesPriceResult{Channel: channel, ServiceMessage: first, ServiceMessages: services}, nil
}
