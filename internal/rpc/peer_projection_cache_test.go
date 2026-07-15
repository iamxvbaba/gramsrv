package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appdialogs "telesrv/internal/app/dialogs"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestViewerPeerCacheChannelsForIDsUsesBatchAndCachesMissing(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 91, Phone: "15550009101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelService := appchannels.NewService(channelStore)
	first, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Cache One",
		Broadcast: true,
		Date:      1700001900,
	})
	if err != nil {
		t.Fatalf("create first channel: %v", err)
	}
	second, err := channelService.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{
		Title:     "Cache Two",
		Megagroup: true,
		Date:      1700001910,
	})
	if err != nil {
		t.Fatalf("create second channel: %v", err)
	}

	counting := &countingChannelsService{Service: channelService}
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: counting,
		Dialogs:  appdialogs.NewService(memory.NewDialogStore(), channelStore),
	}, zaptest.NewLogger(t), clock.System)
	cache := newViewerPeerCache(r)

	got := cache.channelsForIDs(ctx, owner.ID, []int64{first.Channel.ID, second.Channel.ID, first.Channel.ID, 0})
	if len(got) != 2 {
		t.Fatalf("first channelsForIDs returned %d channels, want 2", len(got))
	}
	if counting.getChannelsCalls != 1 || counting.getChannelCalls != 0 {
		t.Fatalf("first load calls: GetChannels=%d GetChannel=%d, want one batch only", counting.getChannelsCalls, counting.getChannelCalls)
	}

	again := cache.channelsForIDs(ctx, owner.ID, []int64{second.Channel.ID, first.Channel.ID})
	if len(again) != 2 {
		t.Fatalf("cached channelsForIDs returned %d channels, want 2", len(again))
	}
	if counting.getChannelsCalls != 1 || counting.getChannelCalls != 0 {
		t.Fatalf("cached load calls: GetChannels=%d GetChannel=%d, want no extra calls", counting.getChannelsCalls, counting.getChannelCalls)
	}

	missingID := second.Channel.ID + 9999
	missing := cache.channelsForIDs(ctx, owner.ID, []int64{missingID})
	if len(missing) != 0 {
		t.Fatalf("missing channelsForIDs returned %d channels, want 0", len(missing))
	}
	if counting.getChannelsCalls != 2 || counting.getChannelCalls != 0 {
		t.Fatalf("missing load calls: GetChannels=%d GetChannel=%d, want second batch only", counting.getChannelsCalls, counting.getChannelCalls)
	}

	missingAgain := cache.channelsForIDs(ctx, owner.ID, []int64{missingID})
	if len(missingAgain) != 0 {
		t.Fatalf("cached missing channelsForIDs returned %d channels, want 0", len(missingAgain))
	}
	if counting.getChannelsCalls != 2 || counting.getChannelCalls != 0 {
		t.Fatalf("cached missing calls: GetChannels=%d GetChannel=%d, want no extra calls", counting.getChannelsCalls, counting.getChannelCalls)
	}
}
