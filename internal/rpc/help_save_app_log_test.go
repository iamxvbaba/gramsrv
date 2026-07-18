package rpc

import (
	"context"
	"fmt"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestHelpSaveAppLogCompatibilityAckAcrossExactProfiles(t *testing.T) {
	r := New(Config{}, Deps{Auth: &captureAuthService{}}, zaptest.NewLogger(t), clock.System)
	requests := map[string]*tg.HelpSaveAppLogRequest{
		"empty": {},
		"android_device_stat": {
			Events: []tg.InputAppEvent{{
				Time: 1_721_234_567.25,
				Type: "android_sdcard_exists",
				Peer: 1,
				Data: &tg.JSONBool{Value: true},
			}},
		},
	}
	contexts := map[string]context.Context{
		"unauthenticated": context.Background(),
		"user":            WithUserID(context.Background(), 42),
	}

	for profile := tlprofile.Profile225; profile <= tlprofile.Profile228; profile++ {
		for contextName, ctx := range contexts {
			for requestName, req := range requests {
				name := fmt.Sprintf("layer_%d/%s/%s", profile, contextName, requestName)
				t.Run(name, func(t *testing.T) {
					for attempt := 1; attempt <= 2; attempt++ {
						result, method := dispatchExactLayerRPCTest(t, r, ctx, profile, req)
						if method != "help.saveAppLog" {
							t.Fatalf("attempt %d method = %q, want help.saveAppLog", attempt, method)
						}
						if value, ok := dispatchCanonicalValue(result).(bool); !ok || !value {
							t.Fatalf("attempt %d response = %#v (%T), want true", attempt, dispatchCanonicalValue(result), result)
						}
					}
				})
			}
		}
	}
}

func TestHelpSaveAppLogRejectsBot(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	bot, err := users.Create(ctx, domain.User{
		Phone:      "+10000000001",
		FirstName:  "TelemetryBot",
		AccessHash: 101,
		Bot:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{Users: appusers.NewService(users)}, zaptest.NewLogger(t), clock.System)

	if _, err := r.onHelpSaveAppLog(WithUserID(ctx, bot.ID)); !tgerr.Is(err, "BOT_METHOD_INVALID") {
		t.Fatalf("bot saveAppLog err = %v, want BOT_METHOD_INVALID", err)
	}
}
