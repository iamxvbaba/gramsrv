package memory

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"telesrv/internal/domain"
)

// BotStore 是 store.BotStore 的内存实现。bot 的 users 行经注入的 UserStore 创建，
// 与 postgres 实现（单事务建 users+bots 两行）保持可见性一致。
// 内置 service bots 的 bots 行预置（token 为空 = 不可登录），对齐迁移种子。
type BotStore struct {
	mu             sync.RWMutex
	users          *UserStore
	byID           map[int64]domain.BotProfile
	states         map[[2]int64]domain.BotChatState
	permissions    map[[2]int64]bool
	appsByID       map[int64]domain.BotApp
	appShortNames  map[botAppShortKey]int64
	appSettings    map[int64]domain.BotAppSettings
	previewMedia   map[int64]map[int64]domain.BotAppPreviewMedia
	previewSeq     int64
	attachMenu     map[int64]domain.BotAttachMenuBot
	attachStates   map[[2]int64]domain.BotAttachMenuState
	requestButtons map[requestedButtonKey]domain.BotRequestedWebViewButton
	emojiPerms     map[[2]int64]bool
	customMethod   map[string]domain.BotWebViewCustomMethodQuery
}

type botAppShortKey struct {
	botUserID int64
	shortName string
}

type requestedButtonKey struct {
	botUserID int64
	userID    int64
	reqID     string
}

// NewBotStore 创建内存 BotStore。
func NewBotStore(users *UserStore) *BotStore {
	s := &BotStore{
		users:          users,
		byID:           make(map[int64]domain.BotProfile),
		states:         make(map[[2]int64]domain.BotChatState),
		permissions:    make(map[[2]int64]bool),
		appsByID:       make(map[int64]domain.BotApp),
		appShortNames:  make(map[botAppShortKey]int64),
		appSettings:    make(map[int64]domain.BotAppSettings),
		previewMedia:   make(map[int64]map[int64]domain.BotAppPreviewMedia),
		attachMenu:     make(map[int64]domain.BotAttachMenuBot),
		attachStates:   make(map[[2]int64]domain.BotAttachMenuState),
		requestButtons: make(map[requestedButtonKey]domain.BotRequestedWebViewButton),
		emojiPerms:     make(map[[2]int64]bool),
		customMethod:   make(map[string]domain.BotWebViewCustomMethodQuery),
	}
	s.byID[domain.BotFatherUserID] = botFatherSeedProfile()
	s.byID[domain.StickersBotUserID] = stickersSeedProfile()
	s.byID[domain.ChatBotUserID] = chatBotSeedProfile()
	return s
}

func botFatherSeedProfile() domain.BotProfile {
	return domain.BotProfile{
		BotUserID:   domain.BotFatherUserID,
		OwnerUserID: domain.BotFatherUserID,
		Description: "BotFather is the one bot to rule them all. Use it to create new bot accounts and manage your existing bots.",
		Commands: []domain.BotCommand{
			{Command: "newbot", Description: "create a new bot"},
			{Command: "mybots", Description: "list your bots"},
			{Command: "token", Description: "show a bot's token"},
			{Command: "revoke", Description: "revoke a bot's token"},
			{Command: "setlogin", Description: "configure Telegram Login"},
			{Command: "logininfo", Description: "show Telegram Login configuration"},
			{Command: "resetloginsecret", Description: "rotate an OIDC Client Secret"},
			{Command: "done", Description: "finish Telegram Login configuration"},
			{Command: "cancel", Description: "cancel the current operation"},
			{Command: "help", Description: "show help"},
		},
	}
}

func stickersSeedProfile() domain.BotProfile {
	return domain.BotProfile{
		BotUserID:   domain.StickersBotUserID,
		OwnerUserID: domain.StickersBotUserID,
		Description: "Create custom sticker and emoji packs for telesrv.",
		Commands: []domain.BotCommand{
			{Command: "start", Description: "start the sticker pack assistant"},
			{Command: "help", Description: "show help"},
			{Command: "newpack", Description: "create a sticker pack"},
			{Command: "newemoji", Description: "create a custom emoji pack"},
			{Command: "addsticker", Description: "add to one of your packs"},
			{Command: "delsticker", Description: "remove one item from a pack"},
			{Command: "publish", Description: "publish the current pack"},
			{Command: "cancel", Description: "cancel the current operation"},
			{Command: "packs", Description: "list your created packs"},
		},
	}
}

func chatBotSeedProfile() domain.BotProfile {
	return domain.BotProfile{
		BotUserID:   domain.ChatBotUserID,
		OwnerUserID: domain.ChatBotUserID,
		Description: "Chat with the configured telesrv AI provider.",
		Commands: []domain.BotCommand{
			{Command: "start", Description: "start chatting"},
			{Command: "help", Description: "show help"},
			{Command: "reset", Description: "clear local chat context"},
		},
	}
}

func (s *BotStore) CreateBotAccount(ctx context.Context, user domain.User, profile domain.BotProfile) (domain.User, domain.BotProfile, error) {
	user.Phone = ""
	user.Username = strings.TrimSpace(strings.TrimPrefix(user.Username, "@"))
	user.Bot = true
	if user.BotInfoVersion < 1 {
		user.BotInfoVersion = 1
	}
	// 持 BotStore.mu 跨「复核计数 → 建 users 行 → 写 profile」，与 postgres 的
	// advisory lock + 事务内复核对齐，封死 count-then-insert TOCTOU。锁顺序
	// BotStore.mu → UserStore.mu（UserStore 不反向引用 BotStore，无死锁）。
	s.mu.Lock()
	defer s.mu.Unlock()
	owned := 0
	for _, p := range s.byID {
		if p.OwnerUserID == profile.OwnerUserID && p.BotUserID != p.OwnerUserID {
			owned++
		}
	}
	if owned >= domain.MaxBotsPerOwner {
		return domain.User{}, domain.BotProfile{}, domain.ErrBotsTooMany
	}
	created, err := s.users.Create(ctx, user)
	if err != nil {
		return domain.User{}, domain.BotProfile{}, err
	}
	profile.BotUserID = created.ID
	s.byID[created.ID] = cloneBotProfile(profile)
	return created, profile, nil
}

func (s *BotStore) GetBot(_ context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	s.mu.RLock()
	p, ok := s.byID[botUserID]
	if ok {
		p = s.enrichBotProfileLocked(p)
	}
	s.mu.RUnlock()
	if !ok {
		return domain.BotProfile{}, false, nil
	}
	return cloneBotProfile(p), true, nil
}

func (s *BotStore) GetBots(_ context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	if len(botUserIDs) == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int64]domain.BotProfile, len(botUserIDs))
	for _, id := range botUserIDs {
		if id == 0 {
			continue
		}
		if _, ok := out[id]; ok {
			continue
		}
		if p, ok := s.byID[id]; ok {
			out[id] = cloneBotProfile(s.enrichBotProfileLocked(p))
		}
	}
	return out, nil
}

func (s *BotStore) ListBotsByOwner(_ context.Context, ownerUserID int64) ([]domain.BotProfile, error) {
	if ownerUserID == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.BotProfile, 0)
	for _, p := range s.byID {
		if p.OwnerUserID == ownerUserID && p.BotUserID != p.OwnerUserID {
			out = append(out, cloneBotProfile(s.enrichBotProfileLocked(p)))
		}
	}
	sortBotProfiles(out)
	return out, nil
}

func (s *BotStore) CountBotsByOwner(_ context.Context, ownerUserID int64) (int, error) {
	if ownerUserID == 0 {
		return 0, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, p := range s.byID {
		if p.OwnerUserID == ownerUserID && p.BotUserID != p.OwnerUserID {
			n++
		}
	}
	return n, nil
}

func (s *BotStore) UpdateBotTokenSecret(_ context.Context, botUserID int64, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byID[botUserID]
	if !ok {
		return domain.ErrBotNotFound
	}
	p.TokenSecret = secret
	s.byID[botUserID] = p
	return nil
}

// editProfile 在 BotStore.mu 内改 bots 行字段，随后 bump users 的 bot_info_version。
// 与 postgres 不同（非单事务），但 memory 仅测试替身、元数据更新低频，可接受。
func (s *BotStore) editProfile(botUserID int64, fn func(p *domain.BotProfile)) (int, error) {
	s.mu.Lock()
	p, ok := s.byID[botUserID]
	if !ok {
		s.mu.Unlock()
		return 0, domain.ErrBotNotFound
	}
	fn(&p)
	s.byID[botUserID] = p
	s.mu.Unlock()
	ver, ok := s.users.bumpBotInfoVersion(botUserID)
	if !ok {
		return 0, domain.ErrBotNotFound
	}
	return ver, nil
}

func (s *BotStore) UpdateBotCommands(_ context.Context, botUserID int64, commands []domain.BotCommand) (int, error) {
	return s.editProfile(botUserID, func(p *domain.BotProfile) {
		p.Commands = append([]domain.BotCommand(nil), commands...)
	})
}

func (s *BotStore) UpdateBotInfo(_ context.Context, botUserID int64, upd domain.BotInfoUpdate) (int, error) {
	if _, ok := s.GetBotProfile(botUserID); !ok {
		return 0, domain.ErrBotNotFound
	}
	if upd.SetName || upd.SetAbout {
		if !s.users.updateBotProfile(botUserID, upd.SetName, upd.Name, upd.SetAbout, upd.About) {
			return 0, domain.ErrBotNotFound
		}
	}
	return s.editProfile(botUserID, func(p *domain.BotProfile) {
		if upd.SetDescription {
			p.Description = upd.Description
		}
	})
}

func (s *BotStore) UpdateBotMenuButton(_ context.Context, botUserID int64, button domain.BotMenuButton) (int, error) {
	return s.editProfile(botUserID, func(p *domain.BotProfile) {
		p.MenuButton = button
	})
}

func (s *BotStore) SetBotInlinePlaceholder(_ context.Context, botUserID int64, placeholder string) (int, error) {
	return s.editProfile(botUserID, func(p *domain.BotProfile) {
		p.InlinePlaceholder = placeholder
	})
}

func (s *BotStore) SetBotInlineGeo(_ context.Context, botUserID int64, inlineGeo bool) (int, error) {
	return s.editProfile(botUserID, func(p *domain.BotProfile) { p.InlineGeo = inlineGeo })
}

func (s *BotStore) SetBotNochats(_ context.Context, botUserID int64, nochats bool) (int, error) {
	return s.editProfile(botUserID, func(p *domain.BotProfile) { p.Nochats = nochats })
}

func (s *BotStore) SetBotChatHistory(_ context.Context, botUserID int64, chatHistory bool) (int, error) {
	return s.editProfile(botUserID, func(p *domain.BotProfile) { p.ChatHistory = chatHistory })
}

func (s *BotStore) CanBotSendMessage(_ context.Context, botUserID, userID int64) (bool, error) {
	if botUserID == 0 || userID == 0 || botUserID == userID {
		return false, nil
	}
	s.mu.RLock()
	allowed := s.permissions[[2]int64{botUserID, userID}]
	s.mu.RUnlock()
	return allowed, nil
}

func (s *BotStore) AllowBotSendMessage(_ context.Context, botUserID, userID int64, _ bool) (bool, error) {
	if botUserID == 0 || userID == 0 || botUserID == userID {
		return false, domain.ErrBotNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[botUserID]; !ok {
		return false, domain.ErrBotNotFound
	}
	key := [2]int64{botUserID, userID}
	if s.permissions[key] {
		return false, nil
	}
	s.permissions[key] = true
	return true, nil
}

func (s *BotStore) UpsertBotApp(_ context.Context, app domain.BotApp) (domain.BotApp, int, error) {
	if app.BotUserID == 0 || app.ID == 0 || app.AccessHash == 0 || app.ShortName == "" || app.URL == "" {
		return domain.BotApp{}, 0, domain.ErrBotAppInvalid
	}
	app.ShortName = strings.ToLower(strings.TrimSpace(app.ShortName))
	s.mu.Lock()
	if _, ok := s.byID[app.BotUserID]; !ok {
		s.mu.Unlock()
		return domain.BotApp{}, 0, domain.ErrBotNotFound
	}
	if existing, ok := s.appShortNames[botAppShortKey{botUserID: app.BotUserID, shortName: app.ShortName}]; ok && existing != app.ID {
		s.mu.Unlock()
		return domain.BotApp{}, 0, domain.ErrBotAppInvalid
	}
	if old, ok := s.appsByID[app.ID]; ok && old.BotUserID != app.BotUserID {
		s.mu.Unlock()
		return domain.BotApp{}, 0, domain.ErrBotAppInvalid
	}
	s.appsByID[app.ID] = app
	s.appShortNames[botAppShortKey{botUserID: app.BotUserID, shortName: app.ShortName}] = app.ID
	p := s.byID[app.BotUserID]
	if app.Main {
		for id, item := range s.appsByID {
			if item.BotUserID == app.BotUserID && id != app.ID && item.Main {
				item.Main = false
				s.appsByID[id] = item
			}
		}
		p.HasMainApp = true
	}
	if app.HasSettings {
		p.HasMainApp = p.HasMainApp || app.Main
	}
	s.byID[app.BotUserID] = p
	s.mu.Unlock()
	ver, ok := s.users.bumpBotInfoVersion(app.BotUserID)
	if !ok {
		return domain.BotApp{}, 0, domain.ErrBotNotFound
	}
	return cloneBotApp(app), ver, nil
}

func (s *BotStore) GetBotAppByID(_ context.Context, appID, accessHash int64) (domain.BotApp, bool, error) {
	if appID == 0 || accessHash == 0 {
		return domain.BotApp{}, false, nil
	}
	s.mu.RLock()
	app, ok := s.appsByID[appID]
	s.mu.RUnlock()
	if !ok || app.AccessHash != accessHash {
		return domain.BotApp{}, false, nil
	}
	return cloneBotApp(app), true, nil
}

func (s *BotStore) GetBotAppByShortName(_ context.Context, botUserID int64, shortName string) (domain.BotApp, bool, error) {
	key := botAppShortKey{botUserID: botUserID, shortName: strings.ToLower(strings.TrimSpace(shortName))}
	s.mu.RLock()
	id, ok := s.appShortNames[key]
	var app domain.BotApp
	if ok {
		app, ok = s.appsByID[id]
	}
	s.mu.RUnlock()
	if !ok {
		return domain.BotApp{}, false, nil
	}
	return cloneBotApp(app), true, nil
}

func (s *BotStore) GetMainBotApp(_ context.Context, botUserID int64) (domain.BotApp, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, app := range s.appsByID {
		if app.BotUserID == botUserID && app.Main {
			return cloneBotApp(app), true, nil
		}
	}
	return domain.BotApp{}, false, nil
}

func (s *BotStore) ListBotApps(_ context.Context, botUserID int64) ([]domain.BotApp, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.BotApp, 0)
	for _, app := range s.appsByID {
		if app.BotUserID == botUserID {
			out = append(out, cloneBotApp(app))
		}
	}
	sortBotApps(out)
	return out, nil
}

func (s *BotStore) GetBotAppSettings(_ context.Context, botUserID int64) (domain.BotAppSettings, bool, error) {
	s.mu.RLock()
	settings, ok := s.appSettings[botUserID]
	s.mu.RUnlock()
	if !ok {
		return domain.BotAppSettings{}, false, nil
	}
	return cloneBotAppSettings(settings), true, nil
}

func (s *BotStore) UpsertBotAppSettings(_ context.Context, botUserID int64, settings domain.BotAppSettings) (int, error) {
	s.mu.Lock()
	if _, ok := s.byID[botUserID]; !ok {
		s.mu.Unlock()
		return 0, domain.ErrBotNotFound
	}
	s.appSettings[botUserID] = cloneBotAppSettings(settings)
	p := s.byID[botUserID]
	c := cloneBotAppSettings(settings)
	p.AppSettings = &c
	s.byID[botUserID] = p
	s.mu.Unlock()
	ver, ok := s.users.bumpBotInfoVersion(botUserID)
	if !ok {
		return 0, domain.ErrBotNotFound
	}
	return ver, nil
}

func (s *BotStore) ListBotAppPreviewMedia(_ context.Context, botUserID, appID int64) ([]domain.BotAppPreviewMedia, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := s.previewMedia[appID]
	out := make([]domain.BotAppPreviewMedia, 0, len(items))
	for _, media := range items {
		if media.BotUserID == botUserID {
			out = append(out, media)
		}
	}
	sortBotPreviewMedia(out)
	return out, nil
}

func (s *BotStore) UpsertBotAppPreviewMedia(_ context.Context, media domain.BotAppPreviewMedia) (domain.BotAppPreviewMedia, int, error) {
	if media.BotUserID == 0 || media.AppID == 0 || (media.PhotoID == 0 && media.DocumentID == 0) {
		return domain.BotAppPreviewMedia{}, 0, domain.ErrBotAppInvalid
	}
	s.mu.Lock()
	if app, ok := s.appsByID[media.AppID]; !ok || app.BotUserID != media.BotUserID {
		s.mu.Unlock()
		return domain.BotAppPreviewMedia{}, 0, domain.ErrBotAppInvalid
	}
	if s.previewMedia[media.AppID] == nil {
		s.previewMedia[media.AppID] = make(map[int64]domain.BotAppPreviewMedia)
	}
	if media.ID == 0 {
		s.previewSeq++
		media.ID = s.previewSeq
	}
	if media.Position <= 0 {
		media.Position = len(s.previewMedia[media.AppID]) + 1
	}
	s.previewMedia[media.AppID][media.ID] = media
	p := s.byID[media.BotUserID]
	p.HasPreviewMedias = true
	s.byID[media.BotUserID] = p
	s.mu.Unlock()
	ver, ok := s.users.bumpBotInfoVersion(media.BotUserID)
	if !ok {
		return domain.BotAppPreviewMedia{}, 0, domain.ErrBotNotFound
	}
	return media, ver, nil
}

func (s *BotStore) DeleteBotAppPreviewMedia(_ context.Context, botUserID, appID, mediaID int64) (int, error) {
	s.mu.Lock()
	items := s.previewMedia[appID]
	if mediaID == 0 || items == nil {
		s.mu.Unlock()
		return 0, domain.ErrBotAppInvalid
	}
	media, ok := items[mediaID]
	if !ok || media.BotUserID != botUserID {
		s.mu.Unlock()
		return 0, domain.ErrBotAppInvalid
	}
	delete(items, mediaID)
	has := false
	for _, items := range s.previewMedia {
		for _, media := range items {
			if media.BotUserID == botUserID {
				has = true
				break
			}
		}
	}
	p := s.byID[botUserID]
	p.HasPreviewMedias = has
	s.byID[botUserID] = p
	s.mu.Unlock()
	ver, ok := s.users.bumpBotInfoVersion(botUserID)
	if !ok {
		return 0, domain.ErrBotNotFound
	}
	return ver, nil
}

func (s *BotStore) ReorderBotAppPreviewMedia(_ context.Context, botUserID, appID int64, mediaIDs []int64) (int, error) {
	s.mu.Lock()
	items := s.previewMedia[appID]
	if items == nil {
		s.mu.Unlock()
		return 0, domain.ErrBotAppInvalid
	}
	for pos, id := range mediaIDs {
		media, ok := items[id]
		if !ok || media.BotUserID != botUserID {
			s.mu.Unlock()
			return 0, domain.ErrBotAppInvalid
		}
		media.Position = pos + 1
		items[id] = media
	}
	s.mu.Unlock()
	ver, ok := s.users.bumpBotInfoVersion(botUserID)
	if !ok {
		return 0, domain.ErrBotNotFound
	}
	return ver, nil
}

func (s *BotStore) UpsertAttachMenuBot(_ context.Context, bot domain.BotAttachMenuBot) (int, error) {
	if bot.BotUserID == 0 || bot.ShortName == "" {
		return 0, domain.ErrBotAttachMenuInvalid
	}
	bot.ShortName = strings.ToLower(strings.TrimSpace(bot.ShortName))
	s.mu.Lock()
	if _, ok := s.byID[bot.BotUserID]; !ok {
		s.mu.Unlock()
		return 0, domain.ErrBotNotFound
	}
	s.attachMenu[bot.BotUserID] = cloneAttachMenuBot(bot)
	p := s.byID[bot.BotUserID]
	p.HasAttachMenu = true
	s.byID[bot.BotUserID] = p
	s.mu.Unlock()
	ver, ok := s.users.bumpBotInfoVersion(bot.BotUserID)
	if !ok {
		return 0, domain.ErrBotNotFound
	}
	return ver, nil
}

func (s *BotStore) GetAttachMenuBot(_ context.Context, botUserID int64) (domain.BotAttachMenuBot, bool, error) {
	s.mu.RLock()
	bot, ok := s.attachMenu[botUserID]
	s.mu.RUnlock()
	if !ok {
		return domain.BotAttachMenuBot{}, false, nil
	}
	return cloneAttachMenuBot(bot), true, nil
}

func (s *BotStore) ListAttachMenuBots(_ context.Context) ([]domain.BotAttachMenuBot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.BotAttachMenuBot, 0, len(s.attachMenu))
	for _, bot := range s.attachMenu {
		out = append(out, cloneAttachMenuBot(bot))
	}
	sortAttachMenuBots(out)
	return out, nil
}

func (s *BotStore) GetAttachMenuState(_ context.Context, userID, botUserID int64) (domain.BotAttachMenuState, bool, error) {
	s.mu.RLock()
	state, ok := s.attachStates[[2]int64{userID, botUserID}]
	s.mu.RUnlock()
	if !ok {
		return domain.BotAttachMenuState{}, false, nil
	}
	return state, true, nil
}

func (s *BotStore) SetAttachMenuState(_ context.Context, state domain.BotAttachMenuState) (domain.BotAttachMenuState, error) {
	if state.UserID == 0 || state.BotUserID == 0 || state.UserID == state.BotUserID {
		return domain.BotAttachMenuState{}, domain.ErrBotAttachMenuInvalid
	}
	s.mu.Lock()
	if _, ok := s.attachMenu[state.BotUserID]; !ok {
		s.mu.Unlock()
		return domain.BotAttachMenuState{}, domain.ErrBotAttachMenuInvalid
	}
	s.attachStates[[2]int64{state.UserID, state.BotUserID}] = state
	s.mu.Unlock()
	return state, nil
}

func (s *BotStore) SaveRequestedWebViewButton(_ context.Context, button domain.BotRequestedWebViewButton) error {
	if button.BotUserID == 0 || button.UserID == 0 || button.WebAppReqID == "" || button.ButtonID == 0 {
		return domain.ErrBotRequestedButtonInvalid
	}
	s.mu.Lock()
	s.requestButtons[requestedButtonKey{botUserID: button.BotUserID, userID: button.UserID, reqID: button.WebAppReqID}] = button
	s.mu.Unlock()
	return nil
}

func (s *BotStore) GetRequestedWebViewButton(_ context.Context, botUserID, userID int64, webAppReqID string) (domain.BotRequestedWebViewButton, bool, error) {
	key := requestedButtonKey{botUserID: botUserID, userID: userID, reqID: webAppReqID}
	s.mu.Lock()
	button, ok := s.requestButtons[key]
	if ok && !button.ExpiresAt.IsZero() && !button.ExpiresAt.After(time.Now()) {
		delete(s.requestButtons, key)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		return domain.BotRequestedWebViewButton{}, false, nil
	}
	return button, true, nil
}

func (s *BotStore) DeleteRequestedWebViewButton(_ context.Context, botUserID, userID int64, webAppReqID string) error {
	s.mu.Lock()
	delete(s.requestButtons, requestedButtonKey{botUserID: botUserID, userID: userID, reqID: webAppReqID})
	s.mu.Unlock()
	return nil
}

func (s *BotStore) SetBotEmojiStatusPermission(_ context.Context, botUserID, userID int64, allowed bool) error {
	if botUserID == 0 || userID == 0 || botUserID == userID {
		return domain.ErrBotNotFound
	}
	s.mu.Lock()
	if _, ok := s.byID[botUserID]; !ok {
		s.mu.Unlock()
		return domain.ErrBotNotFound
	}
	if allowed {
		s.emojiPerms[[2]int64{botUserID, userID}] = true
	} else {
		delete(s.emojiPerms, [2]int64{botUserID, userID})
	}
	s.mu.Unlock()
	return nil
}

func (s *BotStore) BotEmojiStatusPermission(_ context.Context, botUserID, userID int64) (bool, error) {
	s.mu.RLock()
	allowed := s.emojiPerms[[2]int64{botUserID, userID}]
	s.mu.RUnlock()
	return allowed, nil
}

func (s *BotStore) PutWebViewCustomMethodQuery(_ context.Context, query domain.BotWebViewCustomMethodQuery) error {
	if query.ID == "" || query.BotUserID == 0 || query.UserID == 0 || query.CustomMethod == "" {
		return domain.ErrBotCustomMethodUnavailable
	}
	s.mu.Lock()
	s.customMethod[query.ID] = query
	s.mu.Unlock()
	return nil
}

// GetBotProfile 是 GetBot 的同步只读快照（editProfile 前置存在性检查用）。
func (s *BotStore) GetBotProfile(botUserID int64) (domain.BotProfile, bool) {
	s.mu.RLock()
	p, ok := s.byID[botUserID]
	s.mu.RUnlock()
	return p, ok
}

func (s *BotStore) GetBotChatState(_ context.Context, botUserID, userID int64) (domain.BotChatState, bool, error) {
	s.mu.RLock()
	state, ok := s.states[[2]int64{botUserID, userID}]
	s.mu.RUnlock()
	if !ok {
		return domain.BotChatState{}, false, nil
	}
	return cloneBotChatState(state), true, nil
}

func (s *BotStore) UpsertBotChatState(_ context.Context, state domain.BotChatState) error {
	s.mu.Lock()
	s.states[[2]int64{state.BotUserID, state.UserID}] = cloneBotChatState(state)
	s.mu.Unlock()
	return nil
}

func (s *BotStore) DeleteBotChatState(_ context.Context, botUserID, userID int64) error {
	s.mu.Lock()
	delete(s.states, [2]int64{botUserID, userID})
	s.mu.Unlock()
	return nil
}

func cloneBotProfile(p domain.BotProfile) domain.BotProfile {
	out := p
	out.Commands = append([]domain.BotCommand(nil), p.Commands...)
	if p.AppSettings != nil {
		settings := cloneBotAppSettings(*p.AppSettings)
		out.AppSettings = &settings
	}
	return out
}

func (s *BotStore) enrichBotProfileLocked(p domain.BotProfile) domain.BotProfile {
	if _, ok := s.appSettings[p.BotUserID]; ok {
		settings := cloneBotAppSettings(s.appSettings[p.BotUserID])
		p.AppSettings = &settings
	}
	for _, app := range s.appsByID {
		if app.BotUserID == p.BotUserID && app.Main {
			p.HasMainApp = true
			break
		}
	}
	if _, ok := s.attachMenu[p.BotUserID]; ok {
		p.HasAttachMenu = true
	}
	for _, items := range s.previewMedia {
		for _, media := range items {
			if media.BotUserID == p.BotUserID {
				p.HasPreviewMedias = true
				return p
			}
		}
	}
	return p
}

func cloneBotApp(app domain.BotApp) domain.BotApp {
	return app
}

func cloneBotAppSettings(settings domain.BotAppSettings) domain.BotAppSettings {
	out := settings
	out.PlaceholderPath = append([]byte(nil), settings.PlaceholderPath...)
	return out
}

func cloneAttachMenuBot(bot domain.BotAttachMenuBot) domain.BotAttachMenuBot {
	out := bot
	out.PeerTypes = append([]string(nil), bot.PeerTypes...)
	out.Icons = make([]domain.BotAttachMenuIcon, len(bot.Icons))
	for i, icon := range bot.Icons {
		out.Icons[i] = icon
		out.Icons[i].Colors = append([]domain.BotAttachMenuIconColor(nil), icon.Colors...)
	}
	return out
}

func cloneBotChatState(state domain.BotChatState) domain.BotChatState {
	out := state
	if state.Draft != nil {
		out.Draft = make(map[string]string, len(state.Draft))
		for k, v := range state.Draft {
			out.Draft[k] = v
		}
	}
	return out
}

func sortBotProfiles(list []domain.BotProfile) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].BotUserID < list[j-1].BotUserID; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func sortBotApps(list []domain.BotApp) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && appSortKey(list[j]) < appSortKey(list[j-1]); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func appSortKey(app domain.BotApp) string {
	return strconv.FormatInt(app.BotUserID, 10) + ":" + app.ShortName
}

func sortBotPreviewMedia(list []domain.BotAppPreviewMedia) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && (list[j].Position < list[j-1].Position || (list[j].Position == list[j-1].Position && list[j].ID < list[j-1].ID)); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

func sortAttachMenuBots(list []domain.BotAttachMenuBot) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].BotUserID < list[j-1].BotUserID; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
