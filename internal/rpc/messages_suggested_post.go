package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

const (
	minSuggestedPostStars   int64 = 5
	maxSuggestedPostStars   int64 = 100_000
	minSuggestedPostNanoTON int64 = 10_000_000
	maxSuggestedPostNanoTON int64 = 10_000_000_000_000
)

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
			if value == nil || value.Amount < minSuggestedPostStars || value.Amount > maxSuggestedPostStars ||
				value.Nanos < 0 || value.Nanos >= 1_000_000_000 || value.Amount == maxSuggestedPostStars && value.Nanos != 0 {
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
