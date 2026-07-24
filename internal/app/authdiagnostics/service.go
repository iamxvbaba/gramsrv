package authdiagnostics

import (
	"context"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type Service struct {
	codes   store.CodeStore
	reports store.AuthDeliveryReportStore
}

func NewService(codes store.CodeStore, reports store.AuthDeliveryReportStore) *Service {
	return &Service{codes: codes, reports: reports}
}

func (s *Service) ReportMissingCode(ctx context.Context, req domain.AuthMissingCodeReportRequest) (domain.AuthDeliveryReport, bool, error) {
	phone := domain.NormalizePhone(req.Phone)
	if s == nil || s.codes == nil || s.reports == nil ||
		!domain.ValidPhone(phone) || req.PhoneCodeHash == "" {
		return domain.AuthDeliveryReport{}, false, domain.ErrPhoneCodeInvalid
	}
	record, found, err := s.codes.Get(ctx, req.PhoneCodeHash)
	if err != nil {
		return domain.AuthDeliveryReport{}, false, err
	}
	if !found {
		return domain.AuthDeliveryReport{}, false, domain.ErrPhoneCodeExpired
	}
	if record.Version != store.PhoneCodeVersionCurrent || record.Purpose != "" ||
		record.Phone != phone || !store.LoginCodeChannelVerifiable(record.Channel) {
		return domain.AuthDeliveryReport{}, false, domain.ErrPhoneCodeInvalid
	}
	var channel domain.AuthCodeDeliveryKind
	switch record.Channel {
	case store.PhoneCodeChannelPhone:
		channel = domain.AuthCodeDeliveryPhone
	case store.PhoneCodeChannelSMS:
		channel = domain.AuthCodeDeliverySMS
	default:
		return domain.AuthDeliveryReport{}, false, domain.ErrPhoneCodeInvalid
	}
	report, err := domain.NewAuthDeliveryReport(
		req.AuthKeyID, req.SessionID, req.ClientType, phone, req.PhoneCodeHash,
		record.IssuedUserID, record.DeliveryID, channel, req.MNC, req.CreatedAt,
	)
	if err != nil {
		return domain.AuthDeliveryReport{}, false, err
	}
	return s.reports.CreateAuthDeliveryReport(ctx, report)
}
