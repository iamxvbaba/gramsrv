// Package bots 实现 bot 账号业务：BotFather 对话状态机、bot 创建、token 管理与
// botInfo 查询。bot 登录（auth.importBotAuthorization）在 app/auth 经 store.BotStore
// 直接校验 token，不依赖本包。
package bots

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/links"
	"telesrv/internal/store"
)

// blockChecker 报告 userID 是否 block 了 blockedUserID（store.ContactStore 子集）。
type blockChecker interface {
	IsBlocked(ctx context.Context, userID, blockedUserID int64) (bool, error)
}

type publicChannelUsernameResolver interface {
	ResolvePublicChannelUsername(ctx context.Context, viewerUserID int64, username string) (domain.Channel, bool, error)
}

type stickerSetCreator interface {
	CreateStickerSet(ctx context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error)
	ListCreatedStickerSets(ctx context.Context, userID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error)
	ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error)
	GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error)
	AddStickerToSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef, item domain.StickerSetItemInput) (domain.StickerSet, []domain.Document, error)
	RemoveStickerFromSet(ctx context.Context, actorUserID int64, documentID int64, accessHash int64) (domain.StickerSet, []domain.Document, error)
}

type userStickerSetInstaller interface {
	InstallUserStickerSet(ctx context.Context, userID int64, setID int64, kind domain.StickerSetKind, archived bool, installedDate int) error
}

type aiChatGenerator interface {
	GenerateTextStream(ctx context.Context, req domain.AITextGenerationRequest, emit func(domain.AIComposeText) error) (domain.AIComposeText, error)
}

// RouterHooks 是 rpc 层回调（router 创建后经 SetRouterHooks 延迟注入，打破
// router↔bots 的构造循环；这些能力都依赖 TL/连接层边界，不能在 app 层实现）：
//   - RevokeBotSessions：token revoke 后撤销 bot 的全部已登录 session（删
//     authorization + 强制断连）。
//   - PushBotCommandsChanged：命令变更后给在线相关用户推 updateBotCommands
//     （无 pts 的 ephemeral update，离线用户靠 bot_info_version bump 兜底）。
//   - PushStickerSetsChanged：@Stickers 发布后给 creator 当前在线 session 推
//     updateStickerSets；离线端靠持久化 install 状态 + 下次 getAllStickers 兜底。
type RouterHooks interface {
	RevokeBotSessions(ctx context.Context, botUserID int64) error
	PushBotCommandsChanged(ctx context.Context, botUserID int64, commands []domain.BotCommand)
	PushStickerSetsChanged(ctx context.Context, userID int64, kind domain.StickerSetKind)
}

// TextDraftPusher 推送 @ChatBot AI 流式回复的 transient 文本草稿，由 rpc 层转换为
// UpdateUserTyping/sendMessageTextDraftAction。它独立于普通 bot hooks，避免 BotFather
// 和 @Stickers 的测试/依赖被 AI 对话能力污染。
type TextDraftPusher interface {
	PushBotTextDraft(ctx context.Context, botUserID, userID, randomID int64, text string)
}

// replyLockStripes 是回复串行化条带数：同一用户的 BotFather 回复落同一条带、
// 串行执行（状态机 RMW 原子 + 回复保序），不同用户并发；固定大小不随用户数增长。
const replyLockStripes = 256

// Service 提供 bot 账号业务。
type Service struct {
	users                 store.UserStore
	bots                  store.BotStore
	messages              store.MessageStore
	blocker               blockChecker
	channels              publicChannelUsernameResolver
	stickers              stickerSetCreator
	installer             userStickerSetInstaller
	aiChat                aiChatGenerator
	hooks                 RouterHooks
	textDrafts            TextDraftPusher
	userCache             store.UserCache
	cache                 *botProfileCache
	log                   *zap.Logger
	now                   func() time.Time
	chatBotStreamThrottle time.Duration
	publicBaseURL         string
	// replySeq 是回复 randomID 在 crypto/rand 失败时的兜底单调序列。
	replySeq   atomic.Int64
	replyLocks [replyLockStripes]sync.Mutex
}

// Option 调整 bots 服务的可选依赖。
type Option func(*Service)

// WithLogger 注入日志器（缺省 zap.NewNop）。
func WithLogger(log *zap.Logger) Option {
	return func(s *Service) {
		if log != nil {
			s.log = log
		}
	}
}

// WithNow 注入时钟（测试用）。
func WithNow(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithBlockChecker 注入 block 关系查询：BotFather 回复前据此设置 RecipientBlocked，
// 用户 block 掉 BotFather 后不再向其收件箱投递（对齐 rpc 发送路径语义）。
func WithBlockChecker(c blockChecker) Option {
	return func(s *Service) {
		if c != nil {
			s.blocker = c
		}
	}
}

// WithPublicChannelUsernameResolver 注入公开频道 username 查询能力，用于 bot
// username 预检，避免 bot 与 public channel 产生同名可见入口。
func WithPublicChannelUsernameResolver(c publicChannelUsernameResolver) Option {
	return func(s *Service) {
		if c != nil {
			s.channels = c
		}
	}
}

// WithUserCache 注入 users 基础资料缓存：bot 元数据写入（first_name/about/
// bot_info_version bump）后必须失效该 bot 的缓存条目，否则 TTL 内 getUsers
// 返回旧 first_name 与旧 bot_info_version——version bump 被缓存遮蔽，客户端
// 感知不到变更、不会重拉 getFullUser。
func WithUserCache(c store.UserCache) Option {
	return func(s *Service) {
		if c != nil {
			s.userCache = c
		}
	}
}

// WithStickerSetCreator 注入 sticker set 创建/查询能力，供内置 @Stickers bot 使用。
func WithStickerSetCreator(c stickerSetCreator) Option {
	return func(s *Service) {
		if c != nil {
			s.stickers = c
		}
	}
}

// WithUserStickerSets 注入 per-user sticker set 安装状态写入能力。
func WithUserStickerSets(c userStickerSetInstaller) Option {
	return func(s *Service) {
		if c != nil {
			s.installer = c
		}
	}
}

// WithAIChatGenerator 注入内置 @ChatBot 使用的 AI 文本生成器。
func WithAIChatGenerator(g aiChatGenerator) Option {
	return func(s *Service) {
		if g != nil {
			s.aiChat = g
		}
	}
}

// WithAIChatStreamThrottle 调整 @ChatBot 流式草稿推送的最小时间间隔（测试用）。
func WithAIChatStreamThrottle(d time.Duration) Option {
	return func(s *Service) {
		if d >= 0 {
			s.chatBotStreamThrottle = d
		}
	}
}

func WithPublicBaseURL(baseURL string) Option {
	return func(s *Service) {
		s.publicBaseURL = links.NormalizeBaseURL(baseURL)
	}
}

// invalidateUserCache 在 bot 的 users 行变更（含 version bump）后清缓存。
// 失效失败只记日志：缓存最长 TTL 后自愈，不阻塞写路径。
func (s *Service) invalidateUserCache(ctx context.Context, botUserID int64) {
	if s.userCache == nil {
		return
	}
	if err := s.userCache.Delete(ctx, []int64{botUserID}); err != nil {
		s.log.Warn("invalidate bot user cache", zap.Int64("bot_user_id", botUserID), zap.Error(err))
	}
}

func (s *Service) invalidateBotProfileCache(botUserID int64) {
	if s.cache != nil {
		s.cache.delete(botUserID)
	}
}

func (s *Service) invalidateBotReadCaches(ctx context.Context, botUserID int64) {
	s.invalidateBotProfileCache(botUserID)
	s.invalidateUserCache(ctx, botUserID)
}

// InvalidateBotProfileReadModel 供 ReadModelChangeListener 在 user_base 事件(bot 写会
// bump bot_info_version)时跨实例失效本进程 bot 资料缓存。
func (s *Service) InvalidateBotProfileReadModel(userID int64) {
	if s == nil {
		return
	}
	s.invalidateBotProfileCache(userID)
}

// FlushBotProfileReadModel 供 listener 重连时整表 flush，兜住断连窗口内丢失的 user_base 通知。
func (s *Service) FlushBotProfileReadModel() {
	if s == nil || s.cache == nil {
		return
	}
	s.cache.flush()
}

// SetRouterHooks 注入 rpc 层回调（router 创建后装配，与 P1 的
// SetLifecycleObserver 同款延迟注入）。
func (s *Service) SetRouterHooks(h RouterHooks) {
	if s != nil {
		s.hooks = h
	}
}

// SetTextDraftPusher 注入 @ChatBot 流式草稿推送边界。
func (s *Service) SetTextDraftPusher(p TextDraftPusher) {
	if s != nil {
		s.textDrafts = p
	}
}

func (s *Service) SetAIChatGenerator(g aiChatGenerator) {
	if s != nil {
		s.aiChat = g
	}
}

// NewService 创建 bots 服务。
func NewService(users store.UserStore, bots store.BotStore, messages store.MessageStore, opts ...Option) *Service {
	s := &Service{
		users:                 users,
		bots:                  bots,
		messages:              messages,
		cache:                 newBotProfileCache(botProfileCacheMaxEntries, botProfileCacheTTL),
		log:                   zap.NewNop(),
		now:                   time.Now,
		chatBotStreamThrottle: defaultChatBotStreamThrottle,
		publicBaseURL:         links.DefaultPublicBaseURL,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) botProfile(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	if s == nil || s.bots == nil || botUserID == 0 {
		return domain.BotProfile{}, false, nil
	}
	if s.cache != nil {
		return s.cache.getOrLoad(ctx, botUserID, func() (domain.BotProfile, bool, error) {
			return s.bots.GetBot(ctx, botUserID)
		})
	}
	return s.bots.GetBot(ctx, botUserID)
}

func (s *Service) botProfiles(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	if s == nil || s.bots == nil || len(botUserIDs) == 0 {
		return nil, nil
	}
	ids := uniqueBotUserIDs(botUserIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	if s.cache == nil {
		return s.loadBotProfiles(ctx, ids)
	}
	return s.cache.getMany(ctx, ids, s.loadBotProfiles)
}

func (s *Service) loadBotProfiles(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	if batch, ok := s.bots.(botBatchStore); ok {
		return batch.GetBots(ctx, botUserIDs)
	}
	out := make(map[int64]domain.BotProfile)
	for _, id := range uniqueBotUserIDs(botUserIDs) {
		profile, found, err := s.bots.GetBot(ctx, id)
		if err != nil {
			return nil, err
		}
		if found {
			out[id] = profile
		}
	}
	return out, nil
}

// BotInfo 返回 bot 的元数据（userFull.bot_info hydrate 用）。
func (s *Service) BotInfo(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	return s.botProfile(ctx, botUserID)
}

type botBatchStore interface {
	GetBots(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error)
}

// BotInfos 批量返回 bot 元数据，供频道 full info / participants 这类高频富化路径避免逐 bot 点查。
func (s *Service) BotInfos(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	return s.botProfiles(ctx, botUserIDs)
}

func uniqueBotUserIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// CheckUsername 校验 bot username 语法与全局可见入口占用（users + public channels）。
func (s *Service) CheckUsername(ctx context.Context, ownerUserID int64, username string) (bool, error) {
	if s == nil || s.users == nil || ownerUserID == 0 {
		return false, domain.ErrBotUsernameInvalid
	}
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	if !domain.ValidBotUsername(username) {
		return false, domain.ErrBotUsernameInvalid
	}
	if _, found, err := s.users.ByUsername(ctx, username); err != nil {
		return false, err
	} else if found {
		return false, nil
	}
	if s.channels != nil {
		if _, found, err := s.channels.ResolvePublicChannelUsername(ctx, ownerUserID, username); err != nil {
			return false, err
		} else if found {
			return false, nil
		}
	}
	return true, nil
}

// CreateBot 创建一个新 bot 账号：users 行（is_bot, bot_info_version=1, 无 phone）+
// bots 行（owner、token）。返回新账号与完整 token（唯一一次返回明文的途径之一）。
func (s *Service) CreateBot(ctx context.Context, ownerUserID int64, name, username string) (domain.User, string, error) {
	if s == nil || s.users == nil || s.bots == nil || ownerUserID == 0 {
		return domain.User{}, "", domain.ErrBotNameInvalid
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > domain.MaxBotNameLength {
		return domain.User{}, "", domain.ErrBotNameInvalid
	}
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	if !domain.ValidBotUsername(username) {
		return domain.User{}, "", domain.ErrBotUsernameInvalid
	}
	ok, err := s.CheckUsername(ctx, ownerUserID, username)
	if err != nil {
		return domain.User{}, "", err
	}
	if !ok {
		return domain.User{}, "", domain.ErrUsernameOccupied
	}
	count, err := s.bots.CountBotsByOwner(ctx, ownerUserID)
	if err != nil {
		return domain.User{}, "", err
	}
	if count >= domain.MaxBotsPerOwner {
		return domain.User{}, "", domain.ErrBotsTooMany
	}
	accessHash, err := randomInt64()
	if err != nil {
		return domain.User{}, "", err
	}
	secret, err := randomTokenSecret()
	if err != nil {
		return domain.User{}, "", err
	}
	u, profile, err := s.bots.CreateBotAccount(ctx, domain.User{
		AccessHash:     accessHash,
		FirstName:      name,
		Username:       username,
		Bot:            true,
		BotInfoVersion: 1,
	}, domain.BotProfile{
		OwnerUserID: ownerUserID,
		TokenSecret: secret,
	})
	if err != nil {
		return domain.User{}, "", err
	}
	if s.cache != nil {
		s.cache.put(u.ID, profile, true)
	}
	return u, domain.FormatBotToken(u.ID, secret), nil
}

// ListOwnedBots 返回当前 owner 管理的 bot 用户列表（排除 BotFather 种子）。
func (s *Service) ListOwnedBots(ctx context.Context, ownerUserID int64) ([]domain.User, error) {
	owned, err := s.ownedBots(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.User, 0, len(owned))
	for _, item := range owned {
		out = append(out, item.user)
	}
	return out, nil
}

// ExportBotToken 返回 bot token；revoke=true 时先轮换 secret 并撤销已登录 session。
func (s *Service) ExportBotToken(ctx context.Context, ownerUserID, botUserID int64, revoke bool) (string, error) {
	if revoke {
		return s.RevokeBotToken(ctx, ownerUserID, botUserID)
	}
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return "", err
	}
	if !found || profile.OwnerUserID != ownerUserID || botUserID == domain.BotFatherUserID || profile.TokenSecret == "" {
		return "", domain.ErrBotNotFound
	}
	return domain.FormatBotToken(botUserID, profile.TokenSecret), nil
}

// RevokeBotToken 生成新 token 随机段并落库；旧 token 立即不可登录，并踢掉所有
// 已凭旧 token 登录的 session（经注入的 SessionRevoker）。
func (s *Service) RevokeBotToken(ctx context.Context, ownerUserID, botUserID int64) (string, error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return "", err
	}
	if !found || profile.OwnerUserID != ownerUserID || botUserID == domain.BotFatherUserID {
		return "", domain.ErrBotNotFound
	}
	secret, err := randomTokenSecret()
	if err != nil {
		return "", err
	}
	if err := s.bots.UpdateBotTokenSecret(ctx, botUserID, secret); err != nil {
		return "", err
	}
	s.invalidateBotProfileCache(botUserID)
	token := domain.FormatBotToken(botUserID, secret)
	// 撤销已登录 session：旧 token 已不可重新登录，但已建立的连接仍持有 auth_key，
	// 必须主动失效（删 authorization + 断连），否则旧持有者继续以 bot 身份操作。
	// secret 已轮换不可回滚，故失败时仍返回新 token，但透出 ErrBotSessionsNotRevoked
	// 让调用方诚实告知用户「需重试以确保旧 session 终止」，绝不谎称已止血。
	if s.hooks != nil {
		if err := s.hooks.RevokeBotSessions(ctx, botUserID); err != nil {
			s.log.Warn("revoke bot sessions", zap.Int64("bot_user_id", botUserID), zap.Error(err))
			return token, domain.ErrBotSessionsNotRevoked
		}
	}
	return token, nil
}

// SetBotCommands 覆盖式写入 bot 的 default scope 命令（bots.setBotCommands /
// BotFather /setcommands 共用收口）。校验命令名/描述/数量；写库（含 version bump）
// 成功后给在线相关用户推 updateBotCommands。返回 bump 后的 bot_info_version。
func (s *Service) SetBotCommands(ctx context.Context, botUserID int64, commands []domain.BotCommand) (int, error) {
	if len(commands) > domain.MaxBotCommands {
		return 0, domain.ErrBotCommandInvalid
	}
	clean := make([]domain.BotCommand, 0, len(commands))
	for _, c := range commands {
		cmd := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(c.Command, "/")))
		desc := strings.TrimSpace(c.Description)
		if !domain.ValidBotCommandName(cmd) || desc == "" || len(desc) > domain.MaxBotCommandDescriptionLen {
			return 0, domain.ErrBotCommandInvalid
		}
		clean = append(clean, domain.BotCommand{Command: cmd, Description: desc})
	}
	// 同值短路：bot 框架启动时普遍无条件重发相同命令集，跳过可避免无意义的
	// bot_info_version bump（驱动全体客户端多打一轮 getFullUser）与多余推送。
	// 非原子（读后他写不影响正确性：要么对方已 bump、要么我们多 bump 一次）。
	cur, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, domain.ErrBotNotFound
	}
	if botCommandsEqual(cur.Commands, clean) {
		return 0, nil // 无变更；调用方忽略返回的 version
	}
	version, err := s.bots.UpdateBotCommands(ctx, botUserID, clean)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	if s.hooks != nil {
		s.hooks.PushBotCommandsChanged(ctx, botUserID, clean)
	}
	return version, nil
}

func botCommandsEqual(a, b []domain.BotCommand) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Command != b[i].Command || a[i].Description != b[i].Description {
			return false
		}
	}
	return true
}

// GetBotCommands 返回 bot 的 default scope 命令。
func (s *Service) GetBotCommands(ctx context.Context, botUserID int64) ([]domain.BotCommand, error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, domain.ErrBotNotFound
	}
	return profile.Commands, nil
}

// SetBotInfo 更新 bot 的 name（users.first_name）/about（users.about）/description
// （bots.description），返回 bump 后的 bot_info_version。
func (s *Service) SetBotInfo(ctx context.Context, botUserID int64, upd domain.BotInfoUpdate) (int, error) {
	if upd.SetName {
		upd.Name = strings.TrimSpace(upd.Name)
		if upd.Name == "" || utf8.RuneCountInString(upd.Name) > domain.MaxBotNameLength {
			return 0, domain.ErrBotInfoInvalid
		}
	}
	if upd.SetAbout && utf8.RuneCountInString(upd.About) > domain.MaxBotAboutLen {
		return 0, domain.ErrBotInfoInvalid
	}
	if upd.SetDescription && utf8.RuneCountInString(upd.Description) > domain.MaxBotDescriptionLen {
		return 0, domain.ErrBotInfoInvalid
	}
	if !upd.SetName && !upd.SetAbout && !upd.SetDescription {
		return 0, domain.ErrBotInfoInvalid
	}
	version, err := s.bots.UpdateBotInfo(ctx, botUserID, upd)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// GetBotInfo 返回 bot 的 name/about/description（name=users.first_name、
// about=users.about、description=bots.description）。
func (s *Service) GetBotInfo(ctx context.Context, botUserID int64) (name, about, description string, err error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return "", "", "", err
	}
	if !found {
		return "", "", "", domain.ErrBotNotFound
	}
	u, found, err := s.users.ByID(ctx, botUserID)
	if err != nil {
		return "", "", "", err
	}
	if !found {
		return "", "", "", domain.ErrBotNotFound
	}
	return u.FirstName, u.About, profile.Description, nil
}

// SetBotMenuButton 设置 bot 的 menu button（per-bot 全局），返回新 bot_info_version。
func (s *Service) SetBotMenuButton(ctx context.Context, botUserID int64, button domain.BotMenuButton) (int, error) {
	switch button.Type {
	case domain.BotMenuButtonDefault, domain.BotMenuButtonCommands:
		button.Text, button.URL = "", ""
	case domain.BotMenuButtonWebView:
		button.Text = strings.TrimSpace(button.Text)
		button.URL = strings.TrimSpace(button.URL)
		if button.Text == "" || len(button.Text) > domain.MaxBotMenuButtonTextLen ||
			button.URL == "" || len(button.URL) > domain.MaxBotMenuButtonURLLen {
			return 0, domain.ErrBotMenuButtonInvalid
		}
		// 强制 https（对齐官方 BUTTON_URL_INVALID）：menu button URL 经
		// userFull.bot_info.menu_button 下发给所有交互用户的客户端 webview 入口，
		// 拒绝 javascript:/file:/intent: 等非 https scheme，防 bot 投毒。
		if u, err := url.Parse(button.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			return 0, domain.ErrBotMenuButtonInvalid
		}
	default:
		return 0, domain.ErrBotMenuButtonInvalid
	}
	version, err := s.bots.UpdateBotMenuButton(ctx, botUserID, button)
	if err != nil {
		return 0, err
	}
	if button.Type == domain.BotMenuButtonWebView {
		if _, _, err := s.EnsureMenuBotApp(ctx, botUserID, button); err != nil {
			return 0, err
		}
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// GetBotMenuButton 返回 bot 的 menu button。
func (s *Service) GetBotMenuButton(ctx context.Context, botUserID int64) (domain.BotMenuButton, error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return domain.BotMenuButton{}, err
	}
	if !found {
		return domain.BotMenuButton{}, domain.ErrBotNotFound
	}
	return profile.MenuButton, nil
}

// SetInlinePlaceholder 设置 inline mode placeholder；空字符串表示关闭 inline mode。
func (s *Service) SetInlinePlaceholder(ctx context.Context, botUserID int64, placeholder string) (int, error) {
	placeholder = strings.TrimSpace(placeholder)
	if utf8.RuneCountInString(placeholder) > domain.MaxBotInlinePlaceholderLen {
		return 0, domain.ErrBotInlinePlaceholderInvalid
	}
	version, err := s.bots.SetBotInlinePlaceholder(ctx, botUserID, placeholder)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// SetInlineGeo 设置 bot 是否可在 inline query 中接收用户位置。
func (s *Service) SetInlineGeo(ctx context.Context, botUserID int64, enabled bool) (int, error) {
	version, err := s.bots.SetBotInlineGeo(ctx, botUserID, enabled)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// SetJoinGroups 设置 bot 能否被加入群组（allow=true → bot_nochats=false）。
func (s *Service) SetJoinGroups(ctx context.Context, botUserID int64, allow bool) (int, error) {
	version, err := s.bots.SetBotNochats(ctx, botUserID, !allow)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// SetPrivacy 设置 bot 群内 privacy mode（enabled=true 隐私模式开 → bot_chat_history=false，
// 即 bot 只看命令/回复；enabled=false → 关闭隐私 → bot_chat_history=true，能看全部消息）。
func (s *Service) SetPrivacy(ctx context.Context, botUserID int64, enabled bool) (int, error) {
	version, err := s.bots.SetBotChatHistory(ctx, botUserID, !enabled)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// CanSendMessage reports whether botUserID has explicit permission to initiate
// direct messages with userID.
func (s *Service) CanSendMessage(ctx context.Context, userID, botUserID int64) (bool, error) {
	if s == nil || s.bots == nil || userID == 0 || botUserID == 0 || userID == botUserID {
		return false, nil
	}
	return s.bots.CanBotSendMessage(ctx, botUserID, userID)
}

// AllowSendMessage records an explicit user grant for botUserID to message userID.
func (s *Service) AllowSendMessage(ctx context.Context, userID, botUserID int64, fromRequest bool) (bool, error) {
	if s == nil || s.bots == nil || userID == 0 || botUserID == 0 || userID == botUserID {
		return false, domain.ErrBotNotFound
	}
	return s.bots.AllowBotSendMessage(ctx, botUserID, userID, fromRequest)
}

// OwnsBot 报告 ownerUserID 是否为 botUserID 的 owner（非 BotFather 自身）。
func (s *Service) OwnsBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return false, err
	}
	return found && profile.OwnerUserID == ownerUserID && botUserID != domain.BotFatherUserID, nil
}

// tokenSecretAlphabet 对齐官方 token 随机段字符集。
const tokenSecretAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"

func randomTokenSecret() (string, error) {
	raw := make([]byte, domain.BotTokenSecretLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = tokenSecretAlphabet[int(b)%len(tokenSecretAlphabet)]
	}
	return string(out), nil
}

func randomInt64() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("rand: %w", err)
	}
	v := int64(uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7]))
	return v, nil
}
