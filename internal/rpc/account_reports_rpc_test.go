package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appmoderation "telesrv/internal/app/moderation"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestAccountReportPeerPersistsImmutableSnapshot(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	reporter, err := users.Create(ctx, domain.User{
		AccessHash: 101, Phone: "15550005001", FirstName: "Reporter",
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err := users.Create(ctx, domain.User{
		AccessHash: 202, Phone: "15550005002", FirstName: "Target",
		Username: "reported_target", About: "original bio",
	})
	if err != nil {
		t.Fatal(err)
	}
	userService := appusers.NewService(users)
	reports := memory.NewModerationReportStore()
	router := New(Config{}, Deps{
		Users: userService,
		Moderation: appmoderation.NewService(
			reports, appmoderation.WithPeerReaders(userService, nil),
		),
	}, zaptest.NewLogger(t), clock.System)
	ok, err := router.onAccountReportPeer(
		WithUserID(ctx, reporter.ID),
		&tg.AccountReportPeerRequest{
			Peer: &tg.InputPeerUser{
				UserID: target.ID, AccessHash: target.AccessHash,
			},
			Reason:  &tg.InputReportReasonFake{},
			Message: "This profile impersonates someone.",
		},
	)
	if err != nil || !ok {
		t.Fatalf("report peer ok=%v err=%v", ok, err)
	}
	stored := reports.Reports()
	if len(stored) != 1 ||
		stored[0].ReporterUserID != reporter.ID ||
		stored[0].Target != (domain.Peer{Type: domain.PeerTypeUser, ID: target.ID}) ||
		stored[0].Reason != domain.ModerationReasonFake ||
		len(stored[0].Items) != 1 ||
		stored[0].Items[0].Kind != domain.ModerationItemPeer {
		t.Fatalf("stored report=%+v", stored)
	}
	if _, err := users.UpdateProfile(ctx, target.ID, "Changed", "", "changed later"); err != nil {
		t.Fatal(err)
	}
	again, found, err := reports.GetModerationReport(ctx, stored[0].ID)
	if err != nil || !found ||
		string(again.Items[0].Evidence) != string(stored[0].Items[0].Evidence) {
		t.Fatalf("immutable snapshot=%+v found=%v err=%v", again, found, err)
	}
}
