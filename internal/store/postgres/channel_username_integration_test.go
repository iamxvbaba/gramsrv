package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
)

func TestChannelStoreResolvePublicUsernameRejectsStaleIndex(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1886"+suffix+"71", "ResolveOwner", "")
	viewer := createTestUser(t, ctx, users, "+1886"+suffix+"72", "ResolveViewer", "")
	var channelIDs []int64
	usernames := []string{"resolvepub" + suffix, "missing" + suffix}
	t.Cleanup(func() {
		lower := make([]string, 0, len(usernames))
		for _, username := range usernames {
			lower = append(lower, strings.ToLower(username))
		}
		_, _ = pool.Exec(ctx, "DELETE FROM peer_usernames WHERE (peer_type = 'channel' AND peer_id = ANY($1::bigint[])) OR username_lower = ANY($2::text[])", channelIDs, lower)
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, viewer.ID})
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "resolve public " + suffix,
		Megagroup:     true,
		Date:          1700000910,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelIDs = append(channelIDs, created.Channel.ID)
	publicUsername := usernames[0]
	publicChannel, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: created.Channel.ID,
		Username:  publicUsername,
	})
	if err != nil {
		t.Fatalf("set username: %v", err)
	}

	resolved, found, err := channels.ResolvePublicChannelUsername(ctx, viewer.ID, strings.ToUpper(publicUsername))
	if err != nil || !found || resolved.ID != publicChannel.ID {
		t.Fatalf("resolve public username = %+v found %v err %v, want channel %d", resolved, found, err, publicChannel.ID)
	}
	if _, found, err := channels.ResolvePublicChannelUsername(ctx, viewer.ID, usernames[1]); err != nil || found {
		t.Fatalf("resolve missing username found %v err %v, want not found", found, err)
	}
	anonymous, found, err := channels.ResolvePublicChannelUsername(ctx, 0, strings.ToUpper(publicUsername))
	if err != nil || !found || anonymous.ID != publicChannel.ID {
		t.Fatalf("anonymous resolve public username = %+v found=%v err=%v", anonymous, found, err)
	}
	if _, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: publicChannel.ID,
		Username:  "",
	}); err != nil {
		t.Fatalf("clear username: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO peer_usernames (username_lower, peer_type, peer_id)
VALUES ($1,'channel',$2)
ON CONFLICT (username_lower) DO UPDATE SET peer_type = EXCLUDED.peer_type, peer_id = EXCLUDED.peer_id, updated_at = now()
`, strings.ToLower(publicUsername), publicChannel.ID); err != nil {
		t.Fatalf("insert stale username index: %v", err)
	}

	stale, found, err := channels.ResolvePublicChannelUsername(ctx, viewer.ID, publicUsername)
	if err != nil || found {
		t.Fatalf("resolve stale username = %+v found %v err %v, want not found", stale, found, err)
	}
}

func TestPeerUsernameNamespaceIsGlobal(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	channels := NewChannelStore(pool)
	bots := NewBotStore(pool)

	owner := createTestUser(t, ctx, users, "+1886"+suffix+"81", "GlobalOwner", "")
	viewer := createTestUser(t, ctx, users, "+1886"+suffix+"82", "GlobalViewer", "")
	var channelIDs []int64
	t.Cleanup(func() {
		if len(channelIDs) != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = ANY($1::bigint[])", channelIDs)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, viewer.ID})
	})

	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "global namespace " + suffix,
		Broadcast:     true,
		Date:          1700000920,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelIDs = append(channelIDs, created.Channel.ID)
	channelUsername := "globalchan_" + suffix
	if _, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: created.Channel.ID,
		Username:  channelUsername,
	}); err != nil {
		t.Fatalf("set channel username: %v", err)
	}

	userService := appusers.NewService(users)
	ok, err := userService.CheckUsername(ctx, viewer.ID, strings.ToUpper(channelUsername))
	if err != nil || ok {
		t.Fatalf("account check channel username = ok %v err %v, want false/nil", ok, err)
	}
	if _, err := userService.UpdateUsername(ctx, viewer.ID, channelUsername); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("account update to channel username err = %v, want username occupied", err)
	}
	if _, err := users.Create(ctx, domain.User{
		AccessHash: 9001,
		Phone:      "+1886" + suffix + "83",
		FirstName:  "GlobalCreate",
		Username:   channelUsername,
	}); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("create user with channel username err = %v, want username occupied", err)
	}
	if _, _, err := bots.CreateBotAccount(ctx, domain.User{
		AccessHash:     9002,
		FirstName:      "GlobalBot",
		Username:       channelUsername,
		Bot:            true,
		BotInfoVersion: 1,
	}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "secret"}); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("create bot with channel username err = %v, want username occupied", err)
	}

	userUsername := "globaluser_" + suffix
	if _, err := userService.UpdateUsername(ctx, viewer.ID, userUsername); err != nil {
		t.Fatalf("set user username: %v", err)
	}
	second, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "global namespace second " + suffix,
		Megagroup:     true,
		Date:          1700000921,
	})
	if err != nil {
		t.Fatalf("create second channel: %v", err)
	}
	channelIDs = append(channelIDs, second.Channel.ID)
	ok, err = channels.CheckUsername(ctx, owner.ID, second.Channel.ID, strings.ToUpper(userUsername))
	if err != nil || ok {
		t.Fatalf("channel check user username = ok %v err %v, want false/nil", ok, err)
	}
	if _, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{
		UserID:    owner.ID,
		ChannelID: second.Channel.ID,
		Username:  userUsername,
	}); !errors.Is(err, domain.ErrUsernameOccupied) {
		t.Fatalf("channel update to user username err = %v, want username occupied", err)
	}

	if _, err := userService.UpdateUsername(ctx, viewer.ID, ""); err != nil {
		t.Fatalf("clear user username: %v", err)
	}
	ok, err = channels.CheckUsername(ctx, owner.ID, second.Channel.ID, userUsername)
	if err != nil || !ok {
		t.Fatalf("channel check released user username = ok %v err %v, want true/nil", ok, err)
	}
}
