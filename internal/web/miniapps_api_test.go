package web

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

type miniAppBotManagerStub struct {
	owner   int64
	created int
	about   string
}

func (s *miniAppBotManagerStub) CreateBot(_ context.Context, owner int64, name, username string) (domain.User, string, error) {
	s.owner, s.created = owner, s.created+1
	return domain.User{ID: 9001, FirstName: name, Username: strings.TrimPrefix(username, "@")}, "9001:created-secret", nil
}

func (s *miniAppBotManagerStub) ListOwnedBots(context.Context, int64) ([]domain.User, error) {
	return []domain.User{{ID: 9001, FirstName: "Demo", Username: "demo_bot"}}, nil
}

func (s *miniAppBotManagerStub) SetBotInfo(_ context.Context, _ int64, update domain.BotInfoUpdate) (int, error) {
	s.about = update.About
	return 2, nil
}

type miniAppTokenStub struct{}

func (miniAppTokenStub) GetBot(_ context.Context, botID int64) (domain.BotProfile, bool, error) {
	return domain.BotProfile{BotUserID: botID, TokenSecret: "unused"}, true, nil
}

type miniAppStickerManagerStub struct {
	creator int64
	sets    []domain.StickerSet
}

func (s *miniAppStickerManagerStub) ListStickerSets(context.Context, domain.StickerSetKind) ([]domain.StickerSet, error) {
	return s.sets, nil
}

func (s *miniAppStickerManagerStub) ResolveStickerSet(context.Context, domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error) {
	return domain.StickerSet{}, nil, false, nil
}

func (s *miniAppStickerManagerStub) ListCreatedStickerSets(context.Context, int64, int64, int) ([]domain.StickerSet, int, error) {
	return s.sets, len(s.sets), nil
}

func (s *miniAppStickerManagerStub) CreateStickerSet(_ context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error) {
	s.creator = req.CreatorUserID
	set := domain.StickerSet{ID: 7, ShortName: "created_pack", Title: req.Title, Count: len(req.Items), Kind: req.Kind}
	s.sets = append(s.sets, set)
	return set, nil, nil
}

func miniAppInitDataForTest(t *testing.T, token string, userID int64) string {
	t.Helper()
	values := url.Values{}
	values.Set("query_id", "test-query")
	values.Set("auth_date", strconv.FormatInt(time.Now().Unix(), 10))
	values.Set("user", `{"id":`+strconv.FormatInt(userID, 10)+`,"first_name":"Test"}`)
	values.Set("hash", hex.EncodeToString(miniAppInitDataHash(values, token)))
	return values.Encode()
}

func TestMiniAppCreateUsesSignedIdentity(t *testing.T) {
	bots := &miniAppBotManagerStub{}
	stickers := &miniAppStickerManagerStub{}
	h := NewConfiguredMiniAppsHandler(MiniAppsConfig{
		Bots: bots, Stickers: stickers,
		BotFatherToken: "93372553:bot-secret",
		StickersToken:  "1063110917:sticker-secret",
	})
	initData := miniAppInitDataForTest(t, "93372553:bot-secret", 42)
	req := httptest.NewRequest(http.MethodPost, "/api/miniapps/botfather/bots", strings.NewReader(`{"name":"My Bot","about":"Local Gramsrv bot","username":"my_bot"}`))
	req.Header.Set("X-Telegram-Init-Data", initData)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusCreated || bots.owner != 42 || bots.created != 1 || bots.about != "Local Gramsrv bot" {
		t.Fatalf("create bot status=%d owner=%d calls=%d about=%q body=%s", res.Code, bots.owner, bots.created, bots.about, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || body["token"] != "9001:created-secret" {
		t.Fatalf("create bot response=%s", res.Body.String())
	}

	bad := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/api/miniapps/botfather/bots", strings.NewReader(`{"name":"Bad","username":"bad_bot"}`))
	h.ServeHTTP(bad, badReq)
	if bad.Code != http.StatusUnauthorized || bots.created != 1 {
		t.Fatalf("unauthenticated create status=%d calls=%d", bad.Code, bots.created)
	}
}

func TestMiniAppCreateStickerBindsCreatorToInitData(t *testing.T) {
	stickers := &miniAppStickerManagerStub{}
	h := NewConfiguredMiniAppsHandler(MiniAppsConfig{
		Stickers: stickers, StickersToken: "1063110917:sticker-secret",
	})
	body := `{"title":"My Pack","short_name":"my_pack","kind":"stickers","items":[{"document_id":10,"document_access_hash":20,"emoji":"🙂"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/miniapps/stickers", strings.NewReader(body))
	req.Header.Set("X-Telegram-Init-Data", miniAppInitDataForTest(t, "1063110917:sticker-secret", 77))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusCreated || stickers.creator != 77 {
		t.Fatalf("create sticker status=%d creator=%d body=%s", res.Code, stickers.creator, res.Body.String())
	}
}
