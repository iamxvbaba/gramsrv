package rpc

import (
	"context"
	"fmt"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

const (
	safeLinkGetGroupPrivateChatForbiddenTypeID    = 0x06ebdea1
	safeLinkToggleGroupPrivateChatForbiddenTypeID = 0x94ba7b67
)

func (r *Router) trySafeLinkGroupPrivateChatRPC(ctx context.Context, b *bin.Buffer, id uint32) (bin.Encoder, bool, error) {
	switch id {
	case safeLinkGetGroupPrivateChatForbiddenTypeID:
		enc, err := r.onSafeLinkGetGroupPrivateChatForbidden(ctx, b)
		return enc, true, err
	case safeLinkToggleGroupPrivateChatForbiddenTypeID:
		enc, err := r.onSafeLinkToggleGroupPrivateChatForbidden(ctx, b)
		return enc, true, err
	default:
		return nil, false, nil
	}
}

func (r *Router) onSafeLinkGetGroupPrivateChatForbidden(ctx context.Context, b *bin.Buffer) (bin.Encoder, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if err := b.ConsumeID(safeLinkGetGroupPrivateChatForbiddenTypeID); err != nil {
		return nil, err
	}
	channel, err := tg.DecodeInputChannel(b)
	if err != nil {
		return nil, fmt.Errorf("decode safelink.getGroupPrivateChatForbidden channel: %w", err)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, channel)
	if err != nil {
		return nil, err
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, channelID)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	r.log.Info("SafeLink group private chat get",
		zap.Int64("user_id", userID),
		zap.Int64("channel_id", channelID),
		zap.Bool("enabled", view.Channel.PrivateChatForbidden))
	if view.Channel.PrivateChatForbidden {
		return &tg.BoolTrue{}, nil
	}
	return &tg.BoolFalse{}, nil
}

func (r *Router) onSafeLinkToggleGroupPrivateChatForbidden(ctx context.Context, b *bin.Buffer) (bin.Encoder, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if err := b.ConsumeID(safeLinkToggleGroupPrivateChatForbiddenTypeID); err != nil {
		return nil, err
	}
	channel, err := tg.DecodeInputChannel(b)
	if err != nil {
		return nil, fmt.Errorf("decode safelink.toggleGroupPrivateChatForbidden channel: %w", err)
	}
	enabledTL, err := tg.DecodeBool(b)
	if err != nil {
		return nil, fmt.Errorf("decode safelink.toggleGroupPrivateChatForbidden enabled: %w", err)
	}
	_, enabled := enabledTL.(*tg.BoolTrue)
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, channel)
	if err != nil {
		return nil, err
	}
	updated, err := r.deps.Channels.SetPrivateChatForbidden(ctx, userID, channelID, enabled)
	if err != nil {
		return nil, channelAdminErr(err)
	}
	r.log.Info("SafeLink group private chat toggle",
		zap.Int64("user_id", userID),
		zap.Int64("channel_id", channelID),
		zap.Bool("enabled", enabled),
		zap.Bool("stored", updated.PrivateChatForbidden))
	return r.channelStateMutationUpdates(ctx, userID, updated), nil
}
