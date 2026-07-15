package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onMessagesReportSpam(ctx context.Context, peer tg.InputPeerClass) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onMessagesReport(ctx context.Context, req *tg.MessagesReportRequest) (tg.ReportResultClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	if len(req.ID) == 0 {
		return nil, tgerr.New(400, "MESSAGE_REQUIRED")
	}
	if len(req.ID) > maxGetMessagesIDs || len(req.Option) > maxReportOptionLength || utf8.RuneCountInString(req.Message) > maxReportCommentLength {
		return nil, limitInvalidErr()
	}
	for _, msgID := range req.ID {
		if msgID <= 0 || msgID > domain.MaxMessageBoxID {
			return nil, messageIDInvalidErr()
		}
	}
	return reportResultForOption(string(req.Option))
}

func (r *Router) onMessagesReportReaction(ctx context.Context, req *tg.MessagesReportReactionRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return false, messageIDInvalidErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return false, err
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.ReactionPeer); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onMessagesReportMessagesDelivery(ctx context.Context, req *tg.MessagesReportMessagesDeliveryRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if len(req.ID) > maxGetMessagesIDs {
		return false, limitInvalidErr()
	}
	for _, msgID := range req.ID {
		if msgID <= 0 || msgID > domain.MaxMessageBoxID {
			return false, messageIDInvalidErr()
		}
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onMessagesReportReadMetrics(ctx context.Context, req *tg.MessagesReportReadMetricsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if len(req.Metrics) > maxReadMetrics {
		return false, limitInvalidErr()
	}
	for _, metric := range req.Metrics {
		if metric.MsgID <= 0 || metric.MsgID > domain.MaxMessageBoxID {
			return false, messageIDInvalidErr()
		}
		if metric.TimeInViewMs < 0 || metric.ActiveTimeInViewMs < 0 || metric.HeightToViewportRatioPermille < 0 || metric.SeenRangeRatioPermille < 0 {
			return false, limitInvalidErr()
		}
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) onMessagesReportMusicListen(ctx context.Context, req *tg.MessagesReportMusicListenRequest) (bool, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return false, internalErr()
	}
	if req.ID == nil {
		return false, tgerr.New(400, "DOCUMENT_INVALID")
	}
	if req.ListenedDuration < 0 {
		return false, limitInvalidErr()
	}
	return true, nil
}

func (r *Router) onMessagesReportSponsoredMessage(ctx context.Context, req *tg.MessagesReportSponsoredMessageRequest) (tg.ChannelsSponsoredMessageReportResultClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if len(req.RandomID) == 0 || len(req.RandomID) > maxReportRandomIDLength || len(req.Option) > maxReportOptionLength {
		return nil, limitInvalidErr()
	}
	return &tg.ChannelsSponsoredMessageReportResultReported{}, nil
}

func (r *Router) onMessagesGetSponsoredMessages(ctx context.Context, req *tg.MessagesGetSponsoredMessagesRequest) (tg.MessagesSponsoredMessagesClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return &tg.MessagesSponsoredMessagesEmpty{}, nil
}
