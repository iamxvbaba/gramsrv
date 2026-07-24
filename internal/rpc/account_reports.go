package rpc

import (
	"context"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func (r *Router) onAccountReportPeer(ctx context.Context, req *tg.AccountReportPeerRequest) (bool, error) {
	if req == nil || req.Reason == nil {
		return false, inputRequestInvalidErr()
	}
	if !utf8.ValidString(req.Message) || utf8.RuneCountInString(req.Message) > domain.MaxModerationCommentRunes {
		return false, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	target, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	reason, ok := moderationReasonFromReportReason(req.Reason)
	if !ok {
		return false, tgerr.New(400, "REASON_INVALID")
	}
	if r.deps.Moderation == nil {
		return false, internalErr()
	}
	if _, _, err := r.deps.Moderation.ReportPeer(
		ctx, userID, domain.ModerationSourceAccountPeer, target,
		reason, string(reason), req.Message, r.clock.Now(),
	); err != nil {
		return false, moderationReportError(err)
	}
	return true, nil
}

func (r *Router) onAccountReportProfilePhoto(ctx context.Context, req *tg.AccountReportProfilePhotoRequest) (bool, error) {
	if req == nil || req.Reason == nil {
		return false, inputRequestInvalidErr()
	}
	if !utf8.ValidString(req.Message) || utf8.RuneCountInString(req.Message) > domain.MaxModerationCommentRunes {
		return false, limitInvalidErr()
	}
	photo, ok := req.PhotoID.(*tg.InputPhoto)
	if !ok || photo == nil || photo.ID <= 0 {
		return false, photoInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	target, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	reason, ok := moderationReasonFromReportReason(req.Reason)
	if !ok {
		return false, tgerr.New(400, "REASON_INVALID")
	}
	if r.deps.Moderation == nil {
		return false, internalErr()
	}
	if _, _, err := r.deps.Moderation.ReportProfilePhoto(ctx, domain.ModerationProfilePhotoReportRequest{
		ReporterUserID: userID, Target: target, PhotoID: photo.ID,
		AccessHash: photo.AccessHash, FileReference: append([]byte(nil), photo.FileReference...),
		Reason: reason, Comment: req.Message, CreatedAt: r.clock.Now(),
	}); err != nil {
		if err == domain.ErrModerationEvidenceNotFound {
			return false, photoInvalidErr()
		}
		return false, moderationReportError(err)
	}
	return true, nil
}

func moderationReasonFromReportReason(reason tg.ReportReasonClass) (domain.ModerationReason, bool) {
	switch reason.(type) {
	case *tg.InputReportReasonSpam:
		return domain.ModerationReasonSpam, true
	case *tg.InputReportReasonViolence:
		return domain.ModerationReasonViolence, true
	case *tg.InputReportReasonPornography:
		return domain.ModerationReasonPornography, true
	case *tg.InputReportReasonChildAbuse:
		return domain.ModerationReasonChildAbuse, true
	case *tg.InputReportReasonOther:
		return domain.ModerationReasonOther, true
	case *tg.InputReportReasonCopyright:
		return domain.ModerationReasonCopyright, true
	case *tg.InputReportReasonGeoIrrelevant:
		return domain.ModerationReasonGeoIrrelevant, true
	case *tg.InputReportReasonFake:
		return domain.ModerationReasonFake, true
	case *tg.InputReportReasonIllegalDrugs:
		return domain.ModerationReasonIllegalDrugs, true
	case *tg.InputReportReasonPersonalDetails:
		return domain.ModerationReasonPersonalDetails, true
	default:
		return "", false
	}
}
