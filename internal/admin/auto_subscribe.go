package admin

import (
	"context"
	"fmt"
	"strconv"

	"telesrv/internal/domain"
)

const (
	ActionAddAutoSubscribeChannel    = "channels.auto_subscribe.add"
	ActionRemoveAutoSubscribeChannel = "channels.auto_subscribe.remove"
)

// AutoSubscribeService backs the admin panel's auto-subscribe channel list --
// see internal/app/autosubscribe's own package doc for the full semantics.
type AutoSubscribeService interface {
	List(ctx context.Context) ([]domain.AutoSubscribeChannel, error)
	Add(ctx context.Context, channelID int64, addedBy string) (added bool, joined int, err error)
	Remove(ctx context.Context, channelID int64) (removed bool, err error)
}

// AddAutoSubscribeChannelRequest adds a channel/supergroup to the admin
// panel's auto-subscribe list -- see autosubscribe.Service.Add for the full
// "join everyone right now, join every future signup" semantics.
type AddAutoSubscribeChannelRequest struct {
	CommandMeta
	ChannelID int64 `json:"channel_id,string"`
}

// RemoveAutoSubscribeChannelRequest takes a channel off the list. It never
// removes anyone already joined -- see autosubscribe.Service.Remove.
type RemoveAutoSubscribeChannelRequest struct {
	CommandMeta
	ChannelID int64 `json:"channel_id,string"`
}

func (s *Service) ListAutoSubscribeChannels(ctx context.Context) ([]domain.AutoSubscribeChannel, error) {
	if s == nil || s.autoSubscribe == nil {
		return nil, fmt.Errorf("auto-subscribe dependency is not configured")
	}
	return s.autoSubscribe.List(ctx)
}

func (s *Service) AddAutoSubscribeChannel(ctx context.Context, req AddAutoSubscribeChannelRequest) (CommandResult, error) {
	if s == nil || s.autoSubscribe == nil {
		return CommandResult{}, fmt.Errorf("auto-subscribe dependency is not configured")
	}
	if req.ChannelID <= 0 {
		return CommandResult{}, domain.ErrAutoSubscribeChannelInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionAddAutoSubscribeChannel, 0, domain.Peer{}, req,
		func() (CommandResult, error) {
			details := map[string]any{"channel_id": strconv.FormatInt(req.ChannelID, 10)}
			if req.DryRun {
				return CommandResult{Message: "dry-run completed", Details: details}, nil
			}
			added, joined, err := s.autoSubscribe.Add(ctx, req.ChannelID, req.Actor)
			if err != nil {
				return CommandResult{Details: details}, err
			}
			details["added"] = added
			details["joined_existing_users"] = joined
			message := "channel added to auto-subscribe list"
			if !added {
				message = "channel was already on the auto-subscribe list"
			}
			return CommandResult{Message: message, Details: details}, nil
		})
}

func (s *Service) RemoveAutoSubscribeChannel(ctx context.Context, req RemoveAutoSubscribeChannelRequest) (CommandResult, error) {
	if s == nil || s.autoSubscribe == nil {
		return CommandResult{}, fmt.Errorf("auto-subscribe dependency is not configured")
	}
	if req.ChannelID <= 0 {
		return CommandResult{}, domain.ErrAutoSubscribeChannelInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRemoveAutoSubscribeChannel, 0, domain.Peer{}, req,
		func() (CommandResult, error) {
			details := map[string]any{"channel_id": strconv.FormatInt(req.ChannelID, 10)}
			if req.DryRun {
				return CommandResult{Message: "dry-run completed", Details: details}, nil
			}
			removed, err := s.autoSubscribe.Remove(ctx, req.ChannelID)
			if err != nil {
				return CommandResult{Details: details}, err
			}
			details["removed"] = removed
			message := "channel removed from auto-subscribe list"
			if !removed {
				message = "channel was not on the auto-subscribe list"
			}
			return CommandResult{Message: message, Details: details}, nil
		})
}
