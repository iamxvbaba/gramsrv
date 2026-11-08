package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/store/memory"
)

// widgetBotUsers is a minimal UsersService whose BotStatus reports the given
// accounts as bots. The embedded interface is nil and only BotStatus is used on
// the widget paths under test.
type widgetBotUsers struct {
	UsersService
	bots map[int64]bool
}

func (u widgetBotUsers) BotStatus(_ context.Context, userID int64) (bool, bool, error) {
	bot, ok := u.bots[userID]
	return bot, ok, nil
}

// newWidgetTestRouter builds a router with a bot account (botIDs marked as bots)
// and an in-memory widget store.
func newWidgetTestRouter(t *testing.T, botIDs ...int64) *Router {
	t.Helper()
	bots := make(map[int64]bool, len(botIDs))
	for _, id := range botIDs {
		bots[id] = true
	}
	r := New(Config{}, Deps{Users: widgetBotUsers{bots: bots}}, zaptest.NewLogger(t), clock.System)
	r.deps.Widgets = memory.NewWidgetStore()
	return r
}
