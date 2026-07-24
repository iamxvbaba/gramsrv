package rpc

import (
	"context"
	"errors"
	"sort"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

type readMetricTelemetry struct {
	MessageID                     int   `json:"message_id"`
	ViewID                        int64 `json:"view_id"`
	TimeInViewMS                  int   `json:"time_in_view_ms"`
	ActiveTimeInViewMS            int   `json:"active_time_in_view_ms"`
	HeightToViewportRatioPermille int   `json:"height_to_viewport_ratio_permille"`
	SeenRangeRatioPermille        int   `json:"seen_range_ratio_permille"`
}

func (r *Router) onMessagesReportSpam(ctx context.Context, peer tg.InputPeerClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	target, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil {
		return false, err
	}
	if r.deps.Moderation == nil {
		return false, internalErr()
	}
	if _, _, err := r.deps.Moderation.ReportPeer(
		ctx, userID, domain.ModerationSourceMessagesSpam, target,
		domain.ModerationReasonSpam, "spam", "", r.clock.Now(),
	); err != nil {
		return false, moderationReportError(err)
	}
	return true, nil
}

func (r *Router) onMessagesReport(ctx context.Context, req *tg.MessagesReportRequest) (tg.ReportResultClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	target, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if len(req.ID) == 0 {
		return nil, tgerr.New(400, "MESSAGE_ID_REQUIRED")
	}
	if len(req.ID) > maxGetMessagesIDs || len(req.Option) > maxReportOptionLength || utf8.RuneCountInString(req.Message) > maxReportCommentLength {
		return nil, limitInvalidErr()
	}
	for _, msgID := range req.ID {
		if msgID <= 0 || msgID > domain.MaxMessageBoxID {
			return nil, messageIDInvalidErr()
		}
	}
	result, err := reportResultForOption(string(req.Option))
	if err != nil {
		return nil, err
	}
	if _, final := result.(*tg.ReportResultReported); !final {
		return result, nil
	}
	reason, ok := moderationReasonForReportOption(string(req.Option))
	if !ok {
		return nil, tgerr.New(400, "OPTION_INVALID")
	}
	if r.deps.Moderation == nil {
		return nil, internalErr()
	}
	if _, _, err := r.deps.Moderation.ReportMessages(ctx, domain.ModerationMessageReportRequest{
		ReporterUserID: userID, Target: target, MessageIDs: req.ID,
		Reason: reason, Option: string(req.Option), Comment: req.Message,
		CreatedAt: r.clock.Now(),
	}); err != nil {
		return nil, moderationReportError(err)
	}
	return result, nil
}

func (r *Router) onMessagesReportReaction(ctx context.Context, req *tg.MessagesReportReactionRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return false, messageIDInvalidErr()
	}
	target, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	reactor, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.ReactionPeer)
	if err != nil {
		return false, err
	}
	if reactor.Type != domain.PeerTypeUser || reactor.ID <= 0 {
		return false, peerIDInvalidErr()
	}
	if r.deps.Moderation == nil {
		return false, internalErr()
	}
	if _, _, err := r.deps.Moderation.ReportReaction(ctx, domain.ModerationReactionReportRequest{
		ReporterUserID: userID, Target: target, MessageID: req.ID,
		ReactorUserID: reactor.ID, CreatedAt: r.clock.Now(),
	}); err != nil {
		return false, moderationReportError(err)
	}
	return true, nil
}

func moderationReportError(err error) error {
	switch {
	case errors.Is(err, domain.ErrModerationEvidenceNotFound):
		return messageIDInvalidErr()
	case errors.Is(err, domain.ErrModerationPermissionDenied):
		return tgerr.New(403, "CHAT_ADMIN_REQUIRED")
	case errors.Is(err, domain.ErrModerationRateLimited):
		return floodWaitErr(60)
	case errors.Is(err, domain.ErrModerationReportInvalid):
		return inputRequestInvalidErr()
	default:
		return internalErr()
	}
}

func (r *Router) onMessagesReportMessagesDelivery(ctx context.Context, req *tg.MessagesReportMessagesDeliveryRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if len(req.ID) == 0 || len(req.ID) > maxGetMessagesIDs {
		return false, limitInvalidErr()
	}
	for _, msgID := range req.ID {
		if msgID <= 0 || msgID > domain.MaxMessageBoxID {
			return false, messageIDInvalidErr()
		}
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if err := r.validateTelemetryMessageIDs(ctx, userID, peer, req.ID); err != nil {
		return false, err
	}
	ids := messageIDs64(req.ID)
	if r.deps.ClientTelemetry == nil {
		return false, internalErr()
	}
	if _, _, err := r.deps.ClientTelemetry.Record(
		ctx, userID, domain.ClientTelemetryMessageDelivery, peer, ids,
		struct {
			Push bool `json:"push"`
		}{Push: req.Push},
		r.clock.Now(),
	); err != nil {
		return false, clientTelemetryError(err)
	}
	return true, nil
}

func (r *Router) onMessagesReportReadMetrics(ctx context.Context, req *tg.MessagesReportReadMetricsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if len(req.Metrics) == 0 || len(req.Metrics) > maxReadMetrics {
		return false, limitInvalidErr()
	}
	ids := make([]int, 0, len(req.Metrics))
	payload := make([]readMetricTelemetry, 0, len(req.Metrics))
	for _, metric := range req.Metrics {
		if metric.MsgID <= 0 || metric.MsgID > domain.MaxMessageBoxID {
			return false, messageIDInvalidErr()
		}
		if metric.ViewID == 0 || metric.TimeInViewMs < 0 ||
			metric.TimeInViewMs > 24*60*60*1000 ||
			metric.ActiveTimeInViewMs < 0 ||
			metric.ActiveTimeInViewMs > metric.TimeInViewMs ||
			metric.HeightToViewportRatioPermille < 0 ||
			metric.HeightToViewportRatioPermille > 1_000_000 ||
			metric.SeenRangeRatioPermille < 0 ||
			metric.SeenRangeRatioPermille > 1000 {
			return false, limitInvalidErr()
		}
		ids = append(ids, metric.MsgID)
		payload = append(payload, readMetricTelemetry{
			MessageID: metric.MsgID, ViewID: metric.ViewID,
			TimeInViewMS:                  metric.TimeInViewMs,
			ActiveTimeInViewMS:            metric.ActiveTimeInViewMs,
			HeightToViewportRatioPermille: metric.HeightToViewportRatioPermille,
			SeenRangeRatioPermille:        metric.SeenRangeRatioPermille,
		})
	}
	sort.Slice(payload, func(i, j int) bool {
		return payload[i].MessageID < payload[j].MessageID
	})
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if err := r.validateTelemetryMessageIDs(ctx, userID, peer, ids); err != nil {
		return false, err
	}
	if r.deps.ClientTelemetry == nil {
		return false, internalErr()
	}
	if _, _, err := r.deps.ClientTelemetry.Record(
		ctx, userID, domain.ClientTelemetryReadMetrics, peer,
		messageIDs64(ids),
		struct {
			Metrics []readMetricTelemetry `json:"metrics"`
		}{Metrics: payload},
		r.clock.Now(),
	); err != nil {
		return false, clientTelemetryError(err)
	}
	return true, nil
}

func (r *Router) onMessagesReportMusicListen(ctx context.Context, req *tg.MessagesReportMusicListenRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req.ListenedDuration < 0 || req.ListenedDuration > 24*60*60 {
		return false, limitInvalidErr()
	}
	document, err := r.musicDocumentFromInput(ctx, req.ID)
	if err != nil {
		return false, err
	}
	if r.deps.ClientTelemetry == nil {
		return false, internalErr()
	}
	if _, _, err := r.deps.ClientTelemetry.Record(
		ctx, userID, domain.ClientTelemetryMusicListen, domain.Peer{},
		[]int64{document.ID},
		struct {
			ListenedDuration int `json:"listened_duration"`
		}{ListenedDuration: req.ListenedDuration},
		r.clock.Now(),
	); err != nil {
		return false, clientTelemetryError(err)
	}
	return true, nil
}

func (r *Router) onMessagesReportSponsoredMessage(ctx context.Context, req *tg.MessagesReportSponsoredMessageRequest) (tg.ChannelsSponsoredMessageReportResultClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if len(req.RandomID) == 0 || len(req.RandomID) > maxReportRandomIDLength || len(req.Option) > maxReportOptionLength {
		return nil, limitInvalidErr()
	}
	if r.deps.Moderation == nil {
		return nil, internalErr()
	}
	if _, err := r.deps.Moderation.SponsoredImpression(
		ctx, userID, req.RandomID, r.clock.Now(),
	); err != nil {
		if errors.Is(err, domain.ErrModerationImpressionExpired) ||
			errors.Is(err, domain.ErrModerationEvidenceNotFound) {
			return nil, tgerr.New(400, "RANDOM_ID_INVALID")
		}
		return nil, internalErr()
	}
	option := string(req.Option)
	if option == "" {
		return &tg.ChannelsSponsoredMessageReportResultChooseOption{
			Title: "Report sponsored message",
			Options: []tg.SponsoredMessageReportOption{
				{Text: "Scam or spam", Option: []byte("spam")},
				{Text: "Violence", Option: []byte("violence")},
				{Text: "Pornography", Option: []byte("pornography")},
				{Text: "Child abuse", Option: []byte("child_abuse")},
				{Text: "Illegal drugs", Option: []byte("illegal_drugs")},
				{Text: "Personal details", Option: []byte("personal_details")},
				{Text: "Fake or impersonation", Option: []byte("fake")},
				{Text: "Other", Option: []byte("other")},
			},
		}, nil
	}
	reason, ok := moderationReasonForReportOption(option)
	if option == "other" {
		reason, ok = domain.ModerationReasonOther, true
	}
	if !ok {
		return nil, tgerr.New(400, "OPTION_INVALID")
	}
	if _, _, err := r.deps.Moderation.ReportSponsored(
		ctx, userID, req.RandomID, reason, option, r.clock.Now(),
	); err != nil {
		if errors.Is(err, domain.ErrModerationImpressionExpired) ||
			errors.Is(err, domain.ErrModerationEvidenceNotFound) {
			return nil, tgerr.New(400, "RANDOM_ID_INVALID")
		}
		return nil, moderationReportError(err)
	}
	return &tg.ChannelsSponsoredMessageReportResultReported{}, nil
}

func (r *Router) validateTelemetryMessageIDs(ctx context.Context, userID int64, peer domain.Peer, ids []int) error {
	if len(ids) == 0 || len(ids) > domain.MaxGetMessageIDs {
		return limitInvalidErr()
	}
	needed := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return messageIDInvalidErr()
		}
		if _, duplicate := needed[id]; duplicate {
			return messageIDInvalidErr()
		}
		needed[id] = struct{}{}
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return internalErr()
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, ids)
		if err != nil {
			return internalErr()
		}
		for _, message := range list.Messages {
			if message.Peer == peer {
				delete(needed, message.ID)
			}
		}
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return internalErr()
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, ids)
		if err != nil {
			return internalErr()
		}
		for _, message := range history.Messages {
			delete(needed, message.ID)
		}
	default:
		return peerIDInvalidErr()
	}
	if len(needed) != 0 {
		return messageIDInvalidErr()
	}
	return nil
}

func messageIDs64(ids []int) []int64 {
	out := make([]int64, len(ids))
	for i, id := range ids {
		out[i] = int64(id)
	}
	return out
}

func clientTelemetryError(err error) error {
	switch {
	case errors.Is(err, domain.ErrClientTelemetryRateLimited):
		return floodWaitErr(60)
	case errors.Is(err, domain.ErrClientTelemetryInvalid):
		return inputRequestInvalidErr()
	default:
		return internalErr()
	}
}

func (r *Router) onMessagesGetSponsoredMessages(ctx context.Context, req *tg.MessagesGetSponsoredMessagesRequest) (tg.MessagesSponsoredMessagesClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return &tg.MessagesSponsoredMessagesEmpty{}, nil
}
