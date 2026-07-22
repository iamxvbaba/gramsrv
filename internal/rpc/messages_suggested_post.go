package rpc

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

const (
	minSuggestedPostStars   int64 = 5
	maxSuggestedPostStars   int64 = 100_000
	minSuggestedPostNanoTON int64 = 10_000_000
	maxSuggestedPostNanoTON int64 = 10_000_000_000_000
)

const (
	minSuggestedPostScheduleDelay = 5 * 60
	maxSuggestedPostScheduleDelay = 31 * 24 * 60 * 60
	maxSuggestedPostRejectComment = 1024
)

type suggestedPostApprovalService interface {
	ToggleSuggestedPostApproval(context.Context, domain.ToggleSuggestedPostApprovalRequest) (domain.ToggleSuggestedPostApprovalResult, error)
	ProcessSuggestedPostLifecycle(context.Context, domain.SuggestedPostLifecycleRequest) ([]domain.ToggleSuggestedPostApprovalResult, error)
}

func (r *Router) onMessagesToggleSuggestedPostApproval(ctx context.Context, req *tg.MessagesToggleSuggestedPostApprovalRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req == nil || req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	comment, hasComment := req.GetRejectComment()
	if (!req.Reject && hasComment) || utf8.RuneCountInString(comment) > maxSuggestedPostRejectComment {
		return nil, tgerr400("SUGGESTED_POST_INVALID")
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	service, ok := r.deps.Channels.(suggestedPostApprovalService)
	if !ok {
		return nil, notImplementedErr()
	}
	now := int(r.clock.Now().Unix())
	scheduleDate, hasScheduleDate := req.GetScheduleDate()
	if hasScheduleDate && (req.Reject || scheduleDate < now+minSuggestedPostScheduleDelay || scheduleDate > now+maxSuggestedPostScheduleDelay) {
		return nil, scheduleDateInvalidErr()
	}
	result, err := service.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{
		UserID: userID, MonoforumID: peer.ID, MessageID: req.MsgID, Reject: req.Reject,
		RejectComment: strings.TrimSpace(comment), ScheduleDate: scheduleDate, Date: now,
	})
	if err != nil {
		return nil, suggestedPostApprovalErr(err)
	}
	if !result.Duplicate {
		r.enqueueSuggestedPostApprovalFanout(ctx, userID, result)
	}
	return r.suggestedPostApprovalUpdates(ctx, userID, result), nil
}

func suggestedPostApprovalErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrSuggestedPostApprovalForbidden):
		return tgerr400("CHAT_ADMIN_REQUIRED")
	case errors.Is(err, domain.ErrSuggestedPostAlreadyHandled):
		return tgerr400("SUGGESTED_POST_ALREADY_HANDLED")
	case errors.Is(err, domain.ErrSuggestedPostInvalid), errors.Is(err, domain.ErrChannelInvalid), errors.Is(err, domain.ErrMessageIDInvalid):
		return tgerr400("SUGGESTED_POST_INVALID")
	default:
		return internalErr()
	}
}

func domainSuggestedPost(input tg.SuggestedPost, present bool) (*domain.SuggestedPost, error) {
	if !present {
		return nil, nil
	}
	if input.GetAccepted() || input.GetRejected() {
		return nil, tgerr400("SUGGESTED_POST_AMOUNT_INVALID")
	}
	out := &domain.SuggestedPost{}
	if date, ok := input.GetScheduleDate(); ok {
		if date <= 0 {
			return nil, scheduleDateInvalidErr()
		}
		out.ScheduleDate = date
	}
	if price, ok := input.GetPrice(); ok {
		switch value := price.(type) {
		case *tg.StarsAmount:
			if value == nil || value.Amount < minSuggestedPostStars || value.Amount > maxSuggestedPostStars || value.Nanos != 0 {
				return nil, tgerr400("SUGGESTED_POST_AMOUNT_INVALID")
			}
			out.Price = &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: value.Amount, Nanos: value.Nanos}
		case *tg.StarsTonAmount:
			if value == nil || value.Amount < minSuggestedPostNanoTON || value.Amount > maxSuggestedPostNanoTON {
				return nil, tgerr400("SUGGESTED_POST_AMOUNT_INVALID")
			}
			out.Price = &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceTON, Amount: value.Amount}
		default:
			return nil, tgerr400("SUGGESTED_POST_AMOUNT_INVALID")
		}
	}
	return out, nil
}

func tgSuggestedPost(input *domain.SuggestedPost) (tg.SuggestedPost, bool) {
	if input == nil {
		return tg.SuggestedPost{}, false
	}
	out := tg.SuggestedPost{}
	if input.Accepted {
		out.SetAccepted(true)
	}
	if input.Rejected {
		out.SetRejected(true)
	}
	if input.ScheduleDate > 0 {
		out.SetScheduleDate(input.ScheduleDate)
	}
	if input.Price != nil {
		switch input.Price.Kind {
		case domain.SuggestedPostPriceStars:
			out.SetPrice(&tg.StarsAmount{Amount: input.Price.Amount, Nanos: input.Price.Nanos})
		case domain.SuggestedPostPriceTON:
			out.SetPrice(&tg.StarsTonAmount{Amount: input.Price.Amount})
		}
	}
	return out, true
}
