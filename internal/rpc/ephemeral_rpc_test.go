package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type ephemeralReportChannels struct {
	ChannelsService
	view domain.ChannelView
}

func (s *ephemeralReportChannels) ResolveChannel(context.Context, int64, int64) (domain.ChannelView, error) {
	return s.view, nil
}

type ephemeralReportService struct {
	EphemeralService
	target domain.EphemeralMessage
	calls  int
}

func (s *ephemeralReportService) ReportTarget(_ context.Context, userID int64, device domain.EphemeralDevice, peer domain.Peer, id int) (domain.EphemeralMessage, error) {
	s.calls++
	if userID != s.target.ReceiverUserID || device.UserID != userID || device.BusinessAuthKeyID != s.target.OriginDevice.BusinessAuthKeyID ||
		peer != s.target.Peer || id != s.target.ID {
		return domain.EphemeralMessage{}, domain.ErrEphemeralForbidden
	}
	return s.target, nil
}

func TestEphemeralReportPersistsOnlyFinalIdempotentEvidence(t *testing.T) {
	const userID int64 = 2001
	const channelID int64 = 3001
	now := time.Now()
	authKey := [8]byte{1, 2, 3}
	target := domain.EphemeralMessage{
		ID: 77, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: channelID},
		SenderUserID: 1001, ReceiverUserID: userID, Date: int(now.Unix()), RandomID: 78,
		Content:      domain.EphemeralContent{Message: "abuse"},
		OriginDevice: domain.EphemeralDevice{UserID: userID, BusinessAuthKeyID: authKey, SessionID: 99},
		PayloadHash:  [32]byte{9}, Version: 1, CreatedAt: now, ExpiresAt: now.Add(domain.EphemeralMessageRetention),
	}
	reports := memory.NewEphemeralReportStore()
	ephemeral := &ephemeralReportService{target: target}
	channels := &ephemeralReportChannels{view: domain.ChannelView{
		Channel: domain.Channel{ID: channelID, AccessHash: 42, Megagroup: true},
		Self:    domain.ChannelMember{ChannelID: channelID, UserID: userID, Status: domain.ChannelMemberActive},
	}}
	router := New(Config{}, Deps{Ephemeral: ephemeral, EphemeralReports: reports, Channels: channels}, zaptest.NewLogger(t), clock.System)
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), userID), authKey), 99)
	request := &tg.EphemeralReportMessageRequest{
		Peer: &tg.InputPeerChannel{ChannelID: channelID, AccessHash: 42}, ID: target.ID,
	}

	result, err := router.onEphemeralReportMessage(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(*tg.ReportResultChooseOption); !ok || len(reports.Reports()) != 0 {
		t.Fatalf("initial result=%T reports=%+v", result, reports.Reports())
	}
	request.Option = []byte("other")
	result, err = router.onEphemeralReportMessage(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(*tg.ReportResultAddComment); !ok || len(reports.Reports()) != 0 {
		t.Fatalf("comment result=%T reports=%+v", result, reports.Reports())
	}
	request.Option, request.Message = []byte("spam"), "evidence comment"
	for range 2 {
		result, err = router.onEphemeralReportMessage(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := result.(*tg.ReportResultReported); !ok {
			t.Fatalf("final result=%T", result)
		}
	}
	stored := reports.Reports()
	if len(stored) != 1 || stored[0].Evidence.Content.Message != "abuse" || stored[0].Comment != "evidence comment" {
		t.Fatalf("reports=%+v", stored)
	}
	if ephemeral.calls != 4 {
		t.Fatalf("ReportTarget calls=%d", ephemeral.calls)
	}
}
