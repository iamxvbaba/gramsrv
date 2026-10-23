package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type shareThemeGifts struct {
	GiftsService
	saved   map[int]domain.SavedStarGift // by msg id
	uniques map[int64]domain.UniqueStarGift
}

func (g *shareThemeGifts) GetSaved(_ context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error) {
	saved, ok := g.saved[ref.MsgID]
	if !ok || saved.Owner != ref.Owner {
		return domain.SavedStarGift{}, false, nil
	}
	return saved, true, nil
}

func (g *shareThemeGifts) CountSaved(context.Context, domain.Peer) (int, error) { return 0, nil }

func (g *shareThemeGifts) UniqueByID(_ context.Context, id int64) (domain.UniqueStarGift, bool, error) {
	unique, ok := g.uniques[id]
	return unique, ok, nil
}

type shareThemeFixture struct {
	r      *Router
	themes *memory.ShareThemeStore
	gifts  *shareThemeGifts
	alice  domain.User
	bob    domain.User
}

func newShareThemeFixture(t *testing.T) shareThemeFixture {
	t.Helper()
	ctx := context.Background()
	usersStore := memory.NewUserStore()
	alice, err := usersStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550009201", FirstName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := usersStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550009202", FirstName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	themes := memory.NewShareThemeStore()
	themes.SetBackdrops([]domain.ShareThemeBackdrop{
		{BackdropID: 7, Name: "Midnight", CenterColor: 0x112233, EdgeColor: 0x445566, PatternColor: 0x778899, TextColor: 0xffffff},
		{BackdropID: 3, Name: "Coral", CenterColor: 0xff7f50, EdgeColor: 0xff6347, PatternColor: 0x000000, TextColor: 0xffffff},
	})
	aliceOwner := domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}
	gifts := &shareThemeGifts{
		saved: map[int]domain.SavedStarGift{
			10: {Owner: aliceOwner, UniqueGiftID: 501, LifecycleStatus: domain.StarGiftLifecycleActive},
			11: {Owner: aliceOwner, GiftID: 9, LifecycleStatus: domain.StarGiftLifecycleActive}, // not unique
		},
		uniques: map[int64]domain.UniqueStarGift{
			501: {ID: 501, Slug: "cake-1", Title: "Cake", Num: 1, Owner: aliceOwner},
		},
	}
	r := New(Config{}, Deps{Users: appusers.NewService(usersStore), Gifts: gifts, ShareThemes: themes}, zaptest.NewLogger(t), clock.System)
	return shareThemeFixture{r: r, themes: themes, gifts: gifts, alice: alice, bob: bob}
}

func (f shareThemeFixture) fullShareTheme(t *testing.T, viewer domain.User) tg.ShareThemeClass {
	t.Helper()
	full, err := f.r.onUsersGetFullUser(WithUserID(context.Background(), viewer.ID), &tg.InputUser{UserID: f.alice.ID, AccessHash: f.alice.AccessHash})
	if err != nil {
		t.Fatal(err)
	}
	theme, _ := full.FullUser.GetShareTheme()
	return theme
}

func TestShareThemeEmojiBackdropRoundTrip(t *testing.T) {
	f := newShareThemeFixture(t)
	ctx := WithUserID(context.Background(), f.alice.ID)
	theme := &tg.ShareThemeEmojiBackdrop{}
	theme.SetBackdropID(7)
	theme.SetEmojis([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "🔥"}, &tg.ReactionCustomEmoji{DocumentID: 99}})
	if ok, err := f.r.onMessagesSetShareTheme(ctx, &tg.MessagesSetShareThemeRequest{Peer: &tg.InputPeerSelf{}, Theme: theme}); err != nil || !ok {
		t.Fatalf("set = %v, %v", ok, err)
	}
	// Visible to another viewer, too.
	got, ok := f.fullShareTheme(t, f.bob).(*tg.ShareThemeEmojiBackdrop)
	if !ok {
		t.Fatalf("share theme = %T", f.fullShareTheme(t, f.bob))
	}
	if id, _ := got.GetBackdropID(); id != 7 {
		t.Fatalf("backdrop = %d", id)
	}
	emojis, _ := got.GetEmojis()
	if len(emojis) != 2 || emojis[0].(*tg.ReactionEmoji).Emoticon != "🔥" || emojis[1].(*tg.ReactionCustomEmoji).DocumentID != 99 {
		t.Fatalf("emojis = %#v", emojis)
	}

	if _, err := f.r.onMessagesSetShareTheme(ctx, &tg.MessagesSetShareThemeRequest{Peer: &tg.InputPeerSelf{}, Theme: &tg.ShareThemeDefault{}}); err != nil {
		t.Fatal(err)
	}
	if theme := f.fullShareTheme(t, f.bob); theme != nil {
		t.Fatalf("default theme still projected: %T", theme)
	}
}

func TestShareThemeRejectsInvalidInput(t *testing.T) {
	f := newShareThemeFixture(t)
	ctx := WithUserID(context.Background(), f.alice.ID)
	unknown := &tg.ShareThemeEmojiBackdrop{}
	unknown.SetBackdropID(404)
	tooMany := &tg.ShareThemeEmojiBackdrop{}
	var seven []tg.ReactionClass
	for i := 0; i < 7; i++ {
		seven = append(seven, &tg.ReactionCustomEmoji{DocumentID: int64(i + 1)})
	}
	tooMany.SetEmojis(seven)
	paid := &tg.ShareThemeEmojiBackdrop{}
	paid.SetEmojis([]tg.ReactionClass{&tg.ReactionPaid{}})
	for name, theme := range map[string]tg.ShareThemeClass{
		"unknown backdrop": unknown,
		"too many emoji":   tooMany,
		"paid reaction":    paid,
		"output gift":      &tg.ShareThemeGift{Stargift: &tg.StarGiftUnique{}},
	} {
		if _, err := f.r.onMessagesSetShareTheme(ctx, &tg.MessagesSetShareThemeRequest{Peer: &tg.InputPeerSelf{}, Theme: theme}); !tgerr.Is(err, "SHARE_THEME_INVALID") {
			t.Fatalf("%s err = %v", name, err)
		}
	}
	bobPeer := &tg.InputPeerUser{UserID: f.bob.ID, AccessHash: f.bob.AccessHash}
	if _, err := f.r.onMessagesSetShareTheme(ctx, &tg.MessagesSetShareThemeRequest{Peer: bobPeer, Theme: &tg.ShareThemeDefault{}}); !tgerr.Is(err, "PEER_ID_INVALID") {
		t.Fatalf("foreign user err = %v", err)
	}
	if _, err := f.r.onMessagesSetShareTheme(ctx, &tg.MessagesSetShareThemeRequest{Peer: &tg.InputPeerSelf{},
		Theme: &tg.InputShareThemeGift{Stargift: &tg.InputSavedStarGiftUser{MsgID: 11}}}); !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("non-unique gift err = %v", err)
	}
}

func TestShareThemeGiftFollowsOwnership(t *testing.T) {
	f := newShareThemeFixture(t)
	ctx := WithUserID(context.Background(), f.alice.ID)
	if _, err := f.r.onMessagesSetShareTheme(ctx, &tg.MessagesSetShareThemeRequest{Peer: &tg.InputPeerSelf{},
		Theme: &tg.InputShareThemeGift{Stargift: &tg.InputSavedStarGiftUser{MsgID: 10}}}); err != nil {
		t.Fatal(err)
	}
	got, ok := f.fullShareTheme(t, f.bob).(*tg.ShareThemeGift)
	if !ok {
		t.Fatalf("share theme = %T", f.fullShareTheme(t, f.bob))
	}
	if unique, ok := got.Stargift.(*tg.StarGiftUnique); !ok || unique.Slug != "cake-1" {
		t.Fatalf("gift = %#v", got.Stargift)
	}
	// Once the gift changes hands the theme silently falls back to default.
	gift := f.gifts.uniques[501]
	gift.Owner = domain.Peer{Type: domain.PeerTypeUser, ID: f.bob.ID}
	f.gifts.uniques[501] = gift
	if theme := f.fullShareTheme(t, f.bob); theme != nil {
		t.Fatalf("transferred gift still themes Alice: %T", theme)
	}
}

func TestShareThemeBackdropsHash(t *testing.T) {
	f := newShareThemeFixture(t)
	ctx := WithUserID(context.Background(), f.alice.ID)
	res, err := f.r.onMessagesGetShareThemeBackdrops(ctx, &tg.MessagesGetShareThemeBackdropsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	list, ok := res.(*tg.ShareThemeBackdrops)
	if !ok || len(list.Backdrops) != 2 || list.Backdrops[0].BackdropID != 3 || list.Backdrops[1].Name != "Midnight" || list.Hash == 0 {
		t.Fatalf("backdrops = %#v", res)
	}
	again, err := f.r.onMessagesGetShareThemeBackdrops(ctx, &tg.MessagesGetShareThemeBackdropsRequest{Hash: list.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.(*tg.ShareThemeBackdropsNotModified); !ok {
		t.Fatalf("same hash = %T, want notModified", again)
	}
}
