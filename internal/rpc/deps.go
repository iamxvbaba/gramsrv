package rpc

import (
	"context"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/proto"

	"telesrv/internal/domain"
	"telesrv/internal/sfu"
	"telesrv/internal/store"
	"telesrv/internal/turnsrv"
)

// 本文件按「消费者定义接口」惯例，在 rpc 包定义 Router 依赖的业务服务接口。
// app/* 的 Service 实现它们；微服务化时 gRPC client 同样可实现，rpc 层无需改动。
// 接口方法只用 domain 类型与基本类型，不依赖 app 具体包——这是 rpc↔业务的契约边界。

// AuthService 抽象登录/注册业务。
type AuthService interface {
	BindTempAuthKey(ctx context.Context, sessionID int64, binding domain.TempAuthKeyBinding) error
	ResolveAuthKey(ctx context.Context, authKeyID [8]byte) ([8]byte, bool, error)
	UserID(ctx context.Context, authKeyID [8]byte) (int64, bool, error)
	PendingPasswordUserID(ctx context.Context, authKeyID [8]byte) (int64, bool, error)
	CompletePasswordSignIn(ctx context.Context, authKeyID [8]byte) error
	SendCode(ctx context.Context, phone string) (string, error)
	ResendCode(ctx context.Context, phone, phoneCodeHash string) (string, error)
	CancelCode(ctx context.Context, phone, phoneCodeHash string) error
	SignIn(ctx context.Context, a domain.Authorization, phone, phoneCodeHash, code string) (domain.User, domain.Message, bool, error)
	// SignInWithEmail 处理带 email_verification 的 auth.signIn（登录邮箱路径）。
	SignInWithEmail(ctx context.Context, a domain.Authorization, phone, phoneCodeHash, code string) (domain.User, domain.Message, bool, error)
	SignUp(ctx context.Context, a domain.Authorization, phone, phoneCodeHash, firstName, lastName string) (domain.User, domain.Message, error)
	AcceptLoginToken(ctx context.Context, a domain.Authorization, userID int64) (domain.Authorization, error)
	// BindVerifiedLogin 绑定一个已由外部强因子(passkey)验证身份的用户,直接完成授权。
	BindVerifiedLogin(ctx context.Context, a domain.Authorization, userID int64) (domain.User, error)
	// SignInBot 校验 bot token 并绑定授权（auth.importBotAuthorization）；
	// 校验失败返回 domain.ErrBotTokenInvalid。
	SignInBot(ctx context.Context, a domain.Authorization, token string) (domain.User, error)
	LogOut(ctx context.Context, authKeyID [8]byte) error
	Authorization(ctx context.Context, authKeyID [8]byte) (domain.Authorization, bool, error)
	UpdateAuthorizationLayer(ctx context.Context, authKeyID [8]byte, layer int) error
	ListAuthorizations(ctx context.Context, userID int64) ([]domain.Authorization, error)
	ResetAuthorization(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error)
	ResetAuthorizations(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
}

// SessionBinder 抽象登录后 session 与 user 的在线绑定。
type SessionBinder interface {
	BindAuthKey(sessionID int64, authKeyID [8]byte)
	AuthKeyID(sessionID int64) ([8]byte, bool)
	BindUser(sessionID, userID int64)
	UserID(sessionID int64) (int64, bool)
	UserIDResolved(sessionID int64) (userID int64, resolved bool)
	UnbindAuthKey(authKeyID [8]byte) int
	SetReceivesUpdates(sessionID int64, receives bool)
	PushToSession(ctx context.Context, sessionID int64, t proto.MessageType, msg bin.Encoder) error
	PushToUserExceptSession(ctx context.Context, userID, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error)
}

// ScopedSessionBinder 是 SessionBinder 的精确版本：所有定位都带 raw auth_key_id + session_id。
// 生产 mtprotoedge.SessionManager 实现它；测试替身和旧实现可以只实现 SessionBinder。
type ScopedSessionBinder interface {
	BindAuthKeyForSession(rawAuthKeyID [8]byte, sessionID int64, authKeyID [8]byte)
	AuthKeyIDForSession(rawAuthKeyID [8]byte, sessionID int64) ([8]byte, bool)
	BindUserForAuthKey(rawAuthKeyID [8]byte, sessionID, userID int64)
	UserIDForAuthKey(rawAuthKeyID [8]byte, sessionID int64) (int64, bool)
	UserIDResolvedForAuthKey(rawAuthKeyID [8]byte, sessionID int64) (userID int64, resolved bool)
	SetReceivesUpdatesForAuthKey(rawAuthKeyID [8]byte, sessionID int64, receives bool)
	PushToSessionForAuthKey(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64, t proto.MessageType, msg bin.Encoder) error
	PushToUserExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder) (int, error)
}

// ScopedImmediateSessionPusher 是可选的登录前信号直推能力。
// 它绕过登录后 updates-ready 队列，只能用于会解锁登录流程本身的握手消息，
// 例如 updateLoginToken。
type ScopedImmediateSessionPusher interface {
	PushToSessionForAuthKeyImmediate(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64, t proto.MessageType, msg bin.Encoder) error
}

// SessionUpdatesStateProvider 暴露连接当前的 updates 接收状态（可选能力）。
// 用于按 RPC 置位 receivesUpdates 时的幂等短路；不实现时每次都走完整置位（幂等，仅多余开销）。
type SessionUpdatesStateProvider interface {
	ReceivesUpdatesForAuthKey(rawAuthKeyID [8]byte, sessionID int64) bool
}

// SessionTerminator 暴露按业务 auth_key 强制断开活跃连接的能力（可选）。
// 授权撤销（被踢设备）必须断开连接：出站推送用连接持有的密钥加密、不回查授权，
// perm-key 连接的授权缓存也只有断开重连才会重新回查授权表。
type SessionTerminator interface {
	CloseSessionsForBusinessAuthKey(authKeyID [8]byte) int
}

// RawSessionTerminator 暴露按连接实际 raw auth_key 强制断开的能力（可选）。
// temp auth key 被撤销时，Router 会先从 temp→perm 缓存里找出同一授权的 raw temp key，
// 再用这个接口踢掉仍未完全解析到业务 key 的活跃连接，避免等缓存 TTL 或下一帧才失效。
type RawSessionTerminator interface {
	CloseSessionsForRawAuthKeyExcept(authKeyID [8]byte, exceptSessionID int64) int
}

// BestEffortSessionBinder 是 updates fanout 的短超时推送接口；不用于 RPC result/ack。
type BestEffortSessionBinder interface {
	PushToUserExceptSessionBestEffort(ctx context.Context, userID, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error)
}

// ScopedBestEffortSessionBinder 是带 raw auth_key_id 精确排除当前设备的 best-effort 版本。
type ScopedBestEffortSessionBinder interface {
	PushToUserExceptAuthKeySessionBestEffort(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error)
}

// TransientSessionBinder 推送短命、不写 durable log 的 update（typing / presence）。
// 与普通推送的关键区别：目标 session 未就绪时直接跳过、不进 pending——transient 数据
// getDifference 无法补，就绪后由 getState 快照/下次状态变化重建，囤积过期 transient 无意义。
type TransientSessionBinder interface {
	PushToUserTransientExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error)
}

// AuthKeyTargetedSessionBinder 把 update 定向投递给某用户【绑定到具体 business auth_key
// 这台设备】的就绪连接（密聊设备级投递）。SessionManager 实现；测试替身/未装配时
// rpc 层回退账号级推送。未就绪连接跳过、不进 pending（密聊离线靠 getDifference 补）。
type AuthKeyTargetedSessionBinder interface {
	PushToUserAuthKey(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg bin.Encoder) (int, error)
	PushToUserAuthKeyTransient(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg bin.Encoder, timeout time.Duration) (int, error)
}

// OnlineUserProvider exposes a bounded runtime snapshot for best-effort fanout.
type OnlineUserProvider interface {
	IsUserOnline(userID int64) bool
	OnlineUserIDsForCandidates(candidateUserIDs []int64, limit int) []int64
	TrackChannelInterest(rawAuthKeyID [8]byte, sessionID, userID int64, channelIDs []int64)
	ClearChannelInterest(rawAuthKeyID [8]byte, sessionID, userID int64)
	OnlineChannelUserIDs(channelID int64, limit int) []int64
	SetSessionChannelMemberships(rawAuthKeyID [8]byte, sessionID, userID int64, channelIDs []int64)
	AddUserChannelMembership(userID, channelID int64)
	RemoveUserChannelMembership(userID, channelID int64)
	OnlineChannelMemberUserIDs(channelID int64, limit int) []int64
}

// ChannelNudgeProvider 暴露「频道在线成员中排除已投递集合后的剩余 user id」，用于 >cap
// 在线成员的 UpdateChannelTooLong nudge（P0-8）。SessionManager 实现；测试/未装配 fake 可不实现
// （type-assert 失败时跳过 nudge，不影响完整 payload 投递）。
type ChannelNudgeProvider interface {
	OnlineChannelMemberUserIDsExcluding(channelID int64, exclude map[int64]struct{}, limit int) []int64
}

// RateLimiter 抽象 RPC 高频写操作限流。
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
	AllowN(ctx context.Context, key string, cost, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
}

// UsersService 抽象用户查询。
type UsersService interface {
	Self(ctx context.Context, userID int64) (domain.User, error)
	ByID(ctx context.Context, currentUserID, userID int64) (domain.User, bool, error)
	ByIDs(ctx context.Context, currentUserID int64, userIDs []int64) ([]domain.User, error)
}

// BatchViewerUsersResolver 是 UsersService 的可选能力：跨多个 viewer 一次性投影同一组 user
// （fan-out 模板化，把 per-recipient 的 ByIDs(=ForViewer) 折叠成 O(owner) 查询）。结果按 viewer
// 与 ByIDs(viewer, ids) 字节等价（personal photo overlay 除外，见 users.ByIDsForViewers）。
// 未实现时 fan-out 预热静默跳过，回退逐 viewer 解析（行为不变，仅退化为旧的 O(viewer) 成本）。
type BatchViewerUsersResolver interface {
	ByIDsForViewers(ctx context.Context, viewerUserIDs []int64, userIDs []int64) (map[int64][]domain.User, error)
}

// BotsService 抽象 bot 元数据查询与管理（bots.* RPC + userFull.bot_info hydrate）。
// 写方法返回 bump 后的 bot_info_version（客户端据此重拉）。
type BotsService interface {
	BotInfo(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error)
	OwnsBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error)
	CheckUsername(ctx context.Context, ownerUserID int64, username string) (bool, error)
	CreateBot(ctx context.Context, ownerUserID int64, name, username string) (domain.User, string, error)
	ListOwnedBots(ctx context.Context, ownerUserID int64) ([]domain.User, error)
	ExportBotToken(ctx context.Context, ownerUserID, botUserID int64, revoke bool) (string, error)
	SetBotCommands(ctx context.Context, botUserID int64, commands []domain.BotCommand) (int, error)
	GetBotCommands(ctx context.Context, botUserID int64) ([]domain.BotCommand, error)
	SetBotInfo(ctx context.Context, botUserID int64, upd domain.BotInfoUpdate) (int, error)
	GetBotInfo(ctx context.Context, botUserID int64) (name, about, description string, err error)
	SetBotMenuButton(ctx context.Context, botUserID int64, button domain.BotMenuButton) (int, error)
	GetBotMenuButton(ctx context.Context, botUserID int64) (domain.BotMenuButton, error)
	SetInlinePlaceholder(ctx context.Context, botUserID int64, placeholder string) (int, error)
	CanSendMessage(ctx context.Context, userID, botUserID int64) (bool, error)
	AllowSendMessage(ctx context.Context, userID, botUserID int64, fromRequest bool) (bool, error)
	UpsertBotApp(ctx context.Context, botUserID int64, app domain.BotApp) (domain.BotApp, int, error)
	GetBotAppByID(ctx context.Context, appID, accessHash int64) (domain.BotApp, bool, error)
	GetBotAppByShortName(ctx context.Context, botUserID int64, shortName string) (domain.BotApp, bool, error)
	GetMainBotApp(ctx context.Context, botUserID int64) (domain.BotApp, bool, error)
	ListBotApps(ctx context.Context, botUserID int64) ([]domain.BotApp, error)
	GetBotAppSettings(ctx context.Context, botUserID int64) (domain.BotAppSettings, bool, error)
	UpsertBotAppSettings(ctx context.Context, botUserID int64, settings domain.BotAppSettings) (int, error)
	ListBotAppPreviewMedia(ctx context.Context, botUserID, appID int64) ([]domain.BotAppPreviewMedia, error)
	UpsertBotAppPreviewMedia(ctx context.Context, media domain.BotAppPreviewMedia) (domain.BotAppPreviewMedia, int, error)
	DeleteBotAppPreviewMedia(ctx context.Context, botUserID, appID, mediaID int64) (int, error)
	ReorderBotAppPreviewMedia(ctx context.Context, botUserID, appID int64, mediaIDs []int64) (int, error)
	UpsertAttachMenuBot(ctx context.Context, botUserID int64, bot domain.BotAttachMenuBot) (int, error)
	GetAttachMenuBot(ctx context.Context, botUserID int64) (domain.BotAttachMenuBot, bool, error)
	ListAttachMenuBots(ctx context.Context) ([]domain.BotAttachMenuBot, error)
	GetAttachMenuState(ctx context.Context, userID, botUserID int64) (domain.BotAttachMenuState, bool, error)
	SetAttachMenuState(ctx context.Context, state domain.BotAttachMenuState) (domain.BotAttachMenuState, error)
	SaveRequestedWebViewButton(ctx context.Context, button domain.BotRequestedWebViewButton) (domain.BotRequestedWebViewButton, error)
	GetRequestedWebViewButton(ctx context.Context, botUserID, userID int64, reqID string) (domain.BotRequestedWebViewButton, bool, error)
	DeleteRequestedWebViewButton(ctx context.Context, botUserID, userID int64, reqID string) error
	SetBotEmojiStatusPermission(ctx context.Context, botUserID, userID int64, allowed bool) error
	BotEmojiStatusPermission(ctx context.Context, botUserID, userID int64) (bool, error)
	PutWebViewCustomMethodQuery(ctx context.Context, botUserID, userID int64, method, paramsJSON string) (domain.BotWebViewCustomMethodQuery, error)
}

// UserIdentityService 是 UsersService 的资料扩展能力，用于 username/phone 解析。
type UserIdentityService interface {
	CheckUsername(ctx context.Context, userID int64, username string) (bool, error)
	UpdateProfile(ctx context.Context, userID int64, update domain.UserProfileUpdate) (domain.User, error)
	UpdateUsername(ctx context.Context, userID int64, username string) (domain.User, error)
	UpdateBirthday(ctx context.Context, userID int64, birthday domain.Birthday) (domain.User, error)
	UpdatePersonalChannel(ctx context.Context, userID int64, channelID int64) (domain.User, error)
	ResolveUsername(ctx context.Context, currentUserID int64, username string) (domain.User, bool, error)
	ResolvePhone(ctx context.Context, currentUserID int64, phone string) (domain.User, bool, error)
}

// UserPremiumService 是 UsersService 的会员扩展能力：授予/续期、到期清理
// （PremiumSweeper）与 emoji status（premium 专属）。设计见 docs/premium-module.md。
type UserPremiumService interface {
	GrantPremium(ctx context.Context, userID int64, months int) (domain.User, error)
	SweepExpiredPremium(ctx context.Context, now int64, limit int) ([]domain.User, error)
	UpdateEmojiStatus(ctx context.Context, userID int64, documentID int64, until int) (domain.User, error)
}

// UserColorService 是 UsersService 的个人色板扩展能力。用于 account.updateColor
// 持久化当前账号的消息气泡 accent 或资料页背景色。
type UserColorService interface {
	UpdateColor(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error)
}

// UserPremiumStatusService 暴露轻量会员判断（基础用户缓存路径，不做 viewer
// 投影），供限额双档（reaction 上限等）低成本调用。
type UserPremiumStatusService interface {
	PremiumActive(ctx context.Context, userID int64) bool
}

// AccountService 抽象账号设置查询。
type AccountService interface {
	GetPassword(ctx context.Context, userID int64) (domain.PasswordSettings, error)
	GetPasswordSettings(ctx context.Context, userID int64, check domain.PasswordCheck) (domain.PrivatePasswordSettings, error)
	UpdatePasswordSettings(ctx context.Context, userID int64, check domain.PasswordCheck, input domain.PasswordInputSettings) error
	CheckPassword(ctx context.Context, userID int64, check domain.PasswordCheck) error
	RequestPasswordRecovery(ctx context.Context, userID int64) (string, error)
	CheckRecoveryPassword(ctx context.Context, userID int64, code string) error
	RecoverPassword(ctx context.Context, userID int64, code string, input *domain.PasswordInputSettings) error
	ConfirmPasswordEmail(ctx context.Context, userID int64, code string) error
	ResendPasswordEmail(ctx context.Context, userID int64) error
	CancelPasswordEmail(ctx context.Context, userID int64) error
	// 登录邮箱（独立于 2FA 恢复邮箱）：authed 走 userID，登录流程/重置走 phone。
	SetLoginEmail(ctx context.Context, userID int64, email string) error
	SetLoginEmailByPhone(ctx context.Context, phone, email string) error
	LoginEmail(ctx context.Context, userID int64) (string, bool, error)
	LoginEmailByPhone(ctx context.Context, phone string) (string, bool, error)
	ClearLoginEmailByPhone(ctx context.Context, phone string) error
	ResetPassword(ctx context.Context, userID int64) (domain.PasswordResetResult, error)
	DeclinePasswordReset(ctx context.Context, userID int64) error
	SaveMusic(ctx context.Context, userID int64, req domain.SaveMusicRequest) (bool, error)
	ListSavedMusicIDs(ctx context.Context, userID int64, limit int) ([]int64, error)
	ListSavedMusic(ctx context.Context, userID int64, offset, limit int) (domain.SavedMusicList, error)
	GetSavedMusicByIDs(ctx context.Context, userID int64, ids []int64) (domain.SavedMusicList, error)
}

// AccountBusinessAutomationService 是账号业务自动化的可选扩展。
// 只暴露 domain DTO，避免 Telegram TL 类型越过 rpc 边界。
type AccountBusinessAutomationService interface {
	GetBusinessProfile(ctx context.Context, userID int64) (domain.BusinessProfile, bool, error)
	UpdateBusinessWorkHours(ctx context.Context, userID int64, hours *domain.BusinessWorkHours) (domain.BusinessProfile, error)
	UpdateBusinessLocation(ctx context.Context, userID int64, location *domain.BusinessLocation) (domain.BusinessProfile, error)
	UpdateBusinessIntro(ctx context.Context, userID int64, intro *domain.BusinessIntro) (domain.BusinessProfile, error)
	UpdateBusinessGreetingMessage(ctx context.Context, userID int64, greeting *domain.BusinessGreetingMessage) (domain.BusinessProfile, error)
	UpdateBusinessAwayMessage(ctx context.Context, userID int64, away *domain.BusinessAwayMessage) (domain.BusinessProfile, error)
	ListBusinessChatLinks(ctx context.Context, userID int64) ([]domain.BusinessChatLink, error)
	CreateBusinessChatLink(ctx context.Context, userID int64, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error)
	EditBusinessChatLink(ctx context.Context, userID int64, slug string, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error)
	DeleteBusinessChatLink(ctx context.Context, userID int64, slug string) (bool, error)
	ResolveBusinessChatLink(ctx context.Context, slug string, bumpViews bool) (domain.BusinessChatLink, bool, error)
	ListQuickReplies(ctx context.Context, userID int64) (domain.QuickReplyList, error)
	CheckQuickReplyShortcut(ctx context.Context, userID int64, shortcut string) (bool, error)
	SaveQuickReplyText(ctx context.Context, userID int64, shortcut string, msg domain.QuickReplyMessage) (domain.QuickReplyMutation, error)
	GetQuickReplyMessages(ctx context.Context, userID int64, shortcutID int, ids []int) (domain.QuickReplyMessages, error)
	RenameQuickReplyShortcut(ctx context.Context, userID int64, shortcutID int, shortcut string) (domain.QuickReplyMutation, error)
	ReorderQuickReplies(ctx context.Context, userID int64, order []int) (domain.QuickReplyMutation, error)
	DeleteQuickReplyShortcut(ctx context.Context, userID int64, shortcutID int) (domain.QuickReplyMutation, error)
	DeleteQuickReplyMessages(ctx context.Context, userID int64, shortcutID int, ids []int) (domain.QuickReplyMutation, error)
	GetConnectedBusinessBot(ctx context.Context, ownerUserID int64) (domain.ConnectedBusinessBot, bool, error)
	SaveConnectedBusinessBot(ctx context.Context, ownerUserID int64, bot domain.ConnectedBusinessBot) (domain.ConnectedBusinessBot, error)
	DeleteConnectedBusinessBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error)
	SetConnectedBusinessBotPaused(ctx context.Context, ownerUserID, peerUserID int64, paused bool) (domain.ConnectedBusinessBotPeerState, error)
	DisableConnectedBusinessBotForPeer(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, error)
	GetConnectedBusinessBotPeerState(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, bool, error)
}

// PrivacyService owns account privacy rule storage/evaluation.
type PrivacyService interface {
	GetRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (domain.PrivacyRules, error)
	SetRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey, rules []domain.PrivacyRule) (domain.PrivacyRules, error)
	AddAllowUser(ctx context.Context, ownerUserID int64, key domain.PrivacyKey, targetUserID int64) (domain.PrivacyRules, bool, error)
	CanSee(ctx context.Context, ownerUserID, viewerUserID int64, key domain.PrivacyKey) (bool, error)
}

// HelpService 抽象启动配置与国家区号目录。
type HelpService interface {
	GetAppConfig(ctx context.Context, hash int) (domain.AppConfig, bool, error)
	GetCountries(ctx context.Context, langCode string, hash int) (domain.CountriesList, bool, error)
}

// UpdatesService 抽象 update 状态查询。
type UpdatesService interface {
	GetState(ctx context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, error)
	CurrentState(ctx context.Context, userID int64) (domain.UpdateState, error)
	AcknowledgeCurrentState(ctx context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, error)
	GetDifference(ctx context.Context, authKeyID [8]byte, userID int64, from domain.UpdateState) (domain.UpdateDifference, error)
	ClearAuthKey(ctx context.Context, authKeyID [8]byte) error
	RecordNewMessage(ctx context.Context, authKeyID [8]byte, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error)
	PublishNewMessage(ctx context.Context, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error)
	RecordStory(ctx context.Context, authKeyID [8]byte, userID int64, story domain.Story, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordStoryFanout(ctx context.Context, userID int64, story domain.Story) (domain.UpdateEvent, domain.UpdateState, error)
	RecordReadStories(ctx context.Context, authKeyID [8]byte, userID int64, read domain.StoryReadResult, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordSentStoryReaction(ctx context.Context, authKeyID [8]byte, userID int64, reaction domain.StoryReactionResult, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordNewStoryReaction(ctx context.Context, authKeyID [8]byte, ownerUserID int64, reaction domain.StoryReactionResult, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordQuickReplyMutation(ctx context.Context, authKeyID [8]byte, userID int64, mutation domain.QuickReplyMutation, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordReadHistory(ctx context.Context, authKeyID [8]byte, userID int64, read domain.ReadHistoryResult, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordContactsReset(ctx context.Context, authKeyID [8]byte, userID int64, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordChannelState(ctx context.Context, authKeyID [8]byte, userID, channelID int64, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogPinned(ctx context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, folderID int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordPinnedDialogs(ctx context.Context, authKeyID [8]byte, userID int64, folderID int, order []domain.Peer, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordSavedDialogPinned(ctx context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordPinnedSavedDialogs(ctx context.Context, authKeyID [8]byte, userID int64, order []domain.Peer, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogUnreadMark(ctx context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, unread bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordPeerSettings(ctx context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, settings domain.PeerSettings, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordPeerStoryBlocked(ctx context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, blocked bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogFilter(ctx context.Context, authKeyID [8]byte, userID int64, folderID int, folder *domain.DialogFolder, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogFilterOrder(ctx context.Context, authKeyID [8]byte, userID int64, order []int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogFiltersReload(ctx context.Context, authKeyID [8]byte, userID int64, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordFolderPeers(ctx context.Context, authKeyID [8]byte, userID int64, peers []domain.FolderPeerUpdate, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordChannelAvailableMessages(ctx context.Context, authKeyID [8]byte, userID, channelID int64, availableMinID int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordChannelViewForumAsMessages(ctx context.Context, authKeyID [8]byte, userID, channelID int64, enabled bool, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordChannelDiscussionInbox(ctx context.Context, authKeyID [8]byte, userID, channelID int64, topicID, maxID int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDraftMessage(ctx context.Context, authKeyID [8]byte, userID int64, peer domain.Peer, topMsgID int, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
}

// ContactsService 抽象通讯录查询。
type ContactsService interface {
	GetContacts(ctx context.Context, userID int64, hash int64) (domain.ContactList, bool, error)
	ContactIDs(ctx context.Context, userID int64, hash int64) ([]int, bool, error)
	AddContact(ctx context.Context, userID int64, input domain.ContactInput) (domain.Contact, error)
	AcceptContact(ctx context.Context, userID, contactUserID int64) (domain.Contact, error)
	ImportContacts(ctx context.Context, userID int64, inputs []domain.ContactInput) (domain.ImportContactsResult, error)
	Search(ctx context.Context, userID int64, query string, limit int) (domain.UserSearchResult, error)
	DeleteContacts(ctx context.Context, userID int64, contactUserIDs []int64) (int, error)
	EditCloseFriends(ctx context.Context, userID int64, contactUserIDs []int64) (domain.CloseFriendsEditResult, error)
	UpdateContactNote(ctx context.Context, userID, contactUserID int64, note string, entities []domain.MessageEntity) (domain.Contact, error)
	SetPersonalPhoto(ctx context.Context, userID, contactUserID int64, photo domain.Photo, date int) (domain.Contact, error)
	ClearPersonalPhoto(ctx context.Context, userID, contactUserID int64, date int) (domain.Contact, error)
	PersonalPhotos(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error)
	GetPeerSettings(ctx context.Context, userID int64, peer domain.Peer) (domain.PeerSettings, error)
	BlockContact(ctx context.Context, userID, peerUserID int64, date int) (bool, error)
	UnblockContact(ctx context.Context, userID, peerUserID int64) (bool, error)
	IsBlocked(ctx context.Context, userID, peerUserID int64) (bool, error)
	GetBlocked(ctx context.Context, userID int64, offset, limit int) (domain.BlockedContactList, error)
}

// DialogsService 抽象会话列表查询。
type DialogsService interface {
	GetDialogsHash(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogHashCheck, error)
	GetDialogs(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogList, error)
	GetPeerDialogs(ctx context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error)
	SaveDraft(ctx context.Context, userID int64, draft domain.DialogDraft) (bool, error)
	GetDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (domain.DialogDraft, bool, error)
	DeleteDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (bool, error)
	ListDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error)
	ClearDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error)
	TogglePinned(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, int, error)
	ToggleArchivePinned(ctx context.Context, userID int64, pinned bool) (bool, error)
	ReorderPinned(ctx context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error)
	MarkUnread(ctx context.Context, userID int64, peer domain.Peer, unread bool) (bool, error)
	UnreadMarks(ctx context.Context, userID int64) ([]domain.Peer, error)
	HidePeerSettingsBar(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
	PeerSettingsBarHidden(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
	GetDialogFolders(ctx context.Context, userID int64) (domain.DialogFolderList, error)
	SaveDialogFolder(ctx context.Context, userID int64, folder domain.DialogFolder) error
	DeleteDialogFolder(ctx context.Context, userID int64, folderID int) error
	ReorderDialogFolders(ctx context.Context, userID int64, order []int) error
	ToggleDialogFolderTags(ctx context.Context, userID int64, enabled bool) error
	EditPeerFolders(ctx context.Context, userID int64, peers []domain.FolderPeerUpdate) error
}

// MessagesService 抽象消息历史、搜索与已读。
type MessagesService interface {
	SendPrivateText(ctx context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error)
	SetChatTheme(ctx context.Context, userID int64, req domain.SetPrivateChatThemeRequest) (domain.SetPrivateChatThemeResult, error)
	ForwardPrivateMessages(ctx context.Context, userID int64, req domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error)
	GetMessages(ctx context.Context, userID int64, ids []int) (domain.MessageList, error)
	GetHistory(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error)
	Search(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error)
	SearchPrivateMedia(ctx context.Context, userID, peerID int64, req domain.MediaSearchRequest) (domain.MessageList, error)
	CountPrivateMediaCategories(ctx context.Context, userID, peerID int64) (domain.MediaCategoryCounts, error)
	ReadHistory(ctx context.Context, userID int64, req domain.ReadHistoryRequest) (domain.ReadHistoryResult, error)
	ReadMessageContents(ctx context.Context, userID int64, req domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error)
	GetOutboxReadDate(ctx context.Context, userID int64, req domain.OutboxReadDateRequest) (int, error)
	SetMessageReactions(ctx context.Context, userID int64, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error)
	GetMessageReactions(ctx context.Context, userID int64, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error)
	VoteMessagePoll(ctx context.Context, userID int64, req domain.VotePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error)
	CloseMessagePoll(ctx context.Context, userID int64, req domain.ClosePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error)
	ListUnreadReactionMessages(ctx context.Context, userID int64, peer domain.Peer, limit int) ([]domain.Message, error)
	ReadPeerReactions(ctx context.Context, userID int64, peer domain.Peer) (int, error)
	EditMessage(ctx context.Context, userID int64, req domain.EditMessageRequest) (domain.EditMessageResult, error)
	PinPrivateMessage(ctx context.Context, userID int64, req domain.PinPrivateMessageRequest) (domain.PinPrivateMessageResult, error)
	UnpinAllPrivateMessages(ctx context.Context, userID int64, req domain.UnpinAllPrivateMessagesRequest) (domain.PinPrivateMessageResult, error)
	DeleteMessages(ctx context.Context, userID int64, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error)
	DeleteHistory(ctx context.Context, userID int64, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error)
	GetSavedDialogs(ctx context.Context, userID int64, filter domain.SavedDialogsFilter) (domain.SavedDialogList, error)
	GetPinnedSavedDialogs(ctx context.Context, userID int64) (domain.SavedDialogList, error)
	GetSavedDialogsByPeers(ctx context.Context, userID int64, peers []domain.Peer) (domain.SavedDialogList, error)
	ToggleSavedDialogPin(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, error)
	ReorderPinnedSavedDialogs(ctx context.Context, userID int64, order []domain.Peer, force bool) error
	DeleteSavedHistory(ctx context.Context, userID int64, req domain.DeleteSavedHistoryRequest) (domain.DeleteSavedHistoryResult, error)
}

// StoriesService 抽象 story 读取、已读、观看与 reaction 状态。
type StoriesService interface {
	CreateStory(ctx context.Context, userID int64, req domain.StoryCreateRequest) (domain.StoryCreateResult, error)
	GetAllStories(ctx context.Context, viewerUserID int64, hidden bool, now, limit int) (domain.StoryList, error)
	GetAllStoriesPage(ctx context.Context, viewerUserID int64, hidden bool, now int, cursor domain.StoryListCursor, limit int) (domain.StoryList, error)
	GetAllStoriesDigest(ctx context.Context, viewerUserID int64, hidden bool, now int) (domain.StoryListDigest, error)
	ListOwnerActiveStories(ctx context.Context, userID int64, owner domain.Peer, now, limit int) (domain.StoryList, error)
	GetPeerStories(ctx context.Context, viewerUserID int64, peer domain.Peer, now int) (domain.PeerStories, error)
	GetStoriesByID(ctx context.Context, viewerUserID int64, peer domain.Peer, ids []int, now int) (domain.StoryList, error)
	GetStoriesArchive(ctx context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit, now int) (domain.StoryList, error)
	GetPinnedStories(ctx context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit, now int) (domain.StoryList, error)
	HasPinnedStories(ctx context.Context, viewerUserID int64, peer domain.Peer, now int) (bool, error)
	ListReadStates(ctx context.Context, viewerUserID int64) ([]domain.StoryReadState, error)
	GetPeerMaxIDs(ctx context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.RecentStory, error)
	GetPeerHiddenStates(ctx context.Context, viewerUserID int64, peers []domain.Peer) (map[domain.Peer]bool, error)
	GetPeerStoryProjections(ctx context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.PeerStoryProjection, error)
	ReadStories(ctx context.Context, viewerUserID int64, peer domain.Peer, maxID, date int) (domain.StoryReadResult, error)
	IncrementViews(ctx context.Context, viewerUserID int64, peer domain.Peer, ids []int, date int) (int, error)
	SendReaction(ctx context.Context, viewerUserID int64, peer domain.Peer, storyID int, reaction *domain.MessageReaction, date int) (domain.StoryReactionResult, error)
	GetStoryViewsList(ctx context.Context, viewerUserID int64, req domain.StoryViewListRequest) (domain.StoryViewList, error)
	GetStoryReactionsList(ctx context.Context, viewerUserID int64, req domain.StoryReactionListRequest) (domain.StoryReactionList, error)
	GetStoryPublicForwards(ctx context.Context, viewerUserID int64, req domain.StoryPublicForwardListRequest) (domain.StoryPublicForwardList, error)
	CanViewStoryStats(ctx context.Context, userID int64, peer domain.Peer) error
	ListStoryViewerIDs(ctx context.Context, userID int64, owner domain.Peer, storyID, limit int) ([]int64, error)
	EditStory(ctx context.Context, userID int64, req domain.StoryEditRequest) (domain.StoryEditResult, error)
	DeleteStories(ctx context.Context, userID int64, peer domain.Peer, ids []int, date int) (domain.StoryMutationResult, error)
	TogglePinned(ctx context.Context, userID int64, peer domain.Peer, ids []int, pinned bool, date int) (domain.StoryMutationResult, error)
	TogglePinnedToTop(ctx context.Context, userID int64, peer domain.Peer, ids []int) error
	TogglePeerStoriesHidden(ctx context.Context, viewerUserID int64, peer domain.Peer, hidden bool) error
	CanSendStory(ctx context.Context, viewerUserID int64, peer domain.Peer) (int, error)
}

// ChannelsService 抽象超级群/频道业务。
type ChannelsService interface {
	CreateMegagroupFromCreateChat(ctx context.Context, userID int64, req domain.CreateChannelRequest) (domain.CreateChannelResult, error)
	CreateChannel(ctx context.Context, userID int64, req domain.CreateChannelRequest) (domain.CreateChannelResult, error)
	GetChannel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
	// ResolveChannel 是 GetChannel 的轻量版（仅访问校验 + Channel/Self，跳过 dialog/boost），
	// 供 inputPeerFor 等只需 access_hash / 频道标志的解析路径用。
	ResolveChannel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
	GetChannels(ctx context.Context, userID int64, channelIDs []int64) ([]domain.ChannelView, error)
	GetJoinableChannel(ctx context.Context, userID, channelID int64) (domain.Channel, error)
	GetParticipants(ctx context.Context, userID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error)
	GetParticipant(ctx context.Context, userID, channelID, participantUserID int64) (domain.ChannelMember, error)
	FutureCreatorAfterLeave(ctx context.Context, userID, channelID int64) (domain.ChannelMember, error)
	InviteToChannel(ctx context.Context, userID, channelID int64, userIDs []int64, date int) (domain.CreateChannelResult, error)
	JoinChannel(ctx context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error)
	LeaveChannel(ctx context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error)
	EditTitle(ctx context.Context, userID int64, req domain.EditChannelTitleRequest) (domain.EditChannelTitleResult, error)
	SetWallpaper(ctx context.Context, userID int64, req domain.SetChannelWallpaperRequest) (domain.SetChannelWallpaperResult, error)
	EditAbout(ctx context.Context, userID int64, req domain.EditChannelAboutRequest) (domain.Channel, error)
	EditAdmin(ctx context.Context, userID int64, req domain.EditChannelAdminRequest) (domain.EditChannelAdminResult, error)
	TransferOwnership(ctx context.Context, userID int64, req domain.TransferChannelOwnershipRequest) (domain.TransferChannelOwnershipResult, error)
	EditMemberRank(ctx context.Context, userID int64, req domain.EditChannelMemberRankRequest) (domain.EditChannelAdminResult, error)
	EditBanned(ctx context.Context, userID int64, req domain.EditChannelBannedRequest) (domain.EditChannelBannedResult, error)
	EditDefaultBannedRights(ctx context.Context, userID int64, req domain.EditChannelDefaultBannedRightsRequest) (domain.Channel, error)
	DeleteChannel(ctx context.Context, userID int64, req domain.DeleteChannelRequest) (domain.DeleteChannelResult, error)
	CheckUsername(ctx context.Context, userID, channelID int64, username string) (bool, error)
	UpdateUsername(ctx context.Context, userID int64, req domain.UpdateChannelUsernameRequest) (domain.Channel, error)
	ListAdminedPublicChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListStoryPostableChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListSendAsChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ResolvePublicUsername(ctx context.Context, userID int64, username string) (domain.Channel, bool, error)
	SearchPublicChannels(ctx context.Context, userID int64, query string, limit int) (domain.PublicChannelSearchResult, error)
	SetSignatures(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetPhoto(ctx context.Context, userID, channelID int64, photo *domain.Photo) (domain.Channel, error)
	SetPreHistoryHidden(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetParticipantsHidden(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetForum(ctx context.Context, userID, channelID int64, enabled, tabs bool) (domain.Channel, error)
	SetAutotranslation(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetRestrictedSponsored(ctx context.Context, userID, channelID int64, restricted bool) (domain.Channel, error)
	SetPaidMessagesPrice(ctx context.Context, userID, channelID int64, stars int64, broadcastMessagesAllowed bool) (domain.ChannelPaidMessagesPriceResult, error)
	SetAntiSpam(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetSlowMode(ctx context.Context, userID, channelID int64, seconds int) (domain.Channel, error)
	SetBoostsToUnblockRestrictions(ctx context.Context, userID, channelID int64, boosts int) (domain.Channel, error)
	SetNoForwards(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetJoinToSend(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetJoinRequest(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetAvailableReactions(ctx context.Context, userID, channelID int64, policy domain.ChannelReactionPolicy) (domain.Channel, error)
	SetColor(ctx context.Context, userID, channelID int64, forProfile bool, color domain.ChannelPeerColor) (domain.Channel, error)
	SetEmojiStatus(ctx context.Context, userID, channelID int64, status domain.ChannelEmojiStatus) (domain.Channel, error)
	ListAdminLog(ctx context.Context, userID int64, req domain.ChannelAdminLogRequest) (domain.ChannelAdminLogResult, error)
	GetChannelForChangeInfo(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
	SaveDefaultSendAs(ctx context.Context, userID int64, req domain.SaveChannelDefaultSendAsRequest) (domain.ChannelView, error)
	GetMessageViews(ctx context.Context, userID int64, req domain.ChannelMessageViewsRequest) (domain.ChannelMessageViewsResult, error)
	SetMessageReactions(ctx context.Context, userID int64, req domain.SetChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error)
	GetMessageReactions(ctx context.Context, userID int64, req domain.ChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error)
	VoteMessagePoll(ctx context.Context, userID int64, req domain.VoteChannelMessagePollRequest) (domain.ChannelMessagePollResult, error)
	CloseMessagePoll(ctx context.Context, userID int64, req domain.CloseChannelMessagePollRequest) (domain.ChannelMessagePollResult, error)
	ListMessageReactions(ctx context.Context, userID int64, req domain.ChannelMessageReactionsListRequest) (domain.ChannelMessageReactionsList, error)
	TopReactions(ctx context.Context, userID int64, limit int) ([]domain.MessageReaction, error)
	RecentReactions(ctx context.Context, userID int64, limit int) ([]domain.MessageReaction, error)
	ClearRecentReactions(ctx context.Context, userID int64) error
	SavedReactionTags(ctx context.Context, userID int64, limit int) ([]domain.SavedReactionTag, error)
	UpdateSavedReactionTag(ctx context.Context, userID int64, tag domain.SavedReactionTag) error
	GetPremiumBoostStatus(ctx context.Context, userID, channelID int64, now int) (domain.PremiumBoostStatus, error)
	ListPremiumBoosts(ctx context.Context, userID, channelID int64, gifts bool, offset string, limit, now int) (domain.PremiumBoostList, error)
	GetPremiumMyBoosts(ctx context.Context, userID int64, now, premiumUntil int) (domain.PremiumMyBoosts, error)
	ApplyPremiumBoost(ctx context.Context, userID, channelID int64, slots []int, now, premiumUntil int) (domain.PremiumMyBoosts, error)
	GetPremiumUserBoosts(ctx context.Context, userID, channelID, targetUserID int64, now int) (domain.PremiumBoostList, error)
	ReadMessageContents(ctx context.Context, userID int64, req domain.ReadChannelMessageContentsRequest) (domain.ReadChannelMessageContentsResult, error)
	GetMessageAuthor(ctx context.Context, userID int64, req domain.GetChannelMessageAuthorRequest) (domain.GetChannelMessageAuthorResult, error)
	CreateForumTopic(ctx context.Context, userID int64, req domain.CreateChannelForumTopicRequest) (domain.CreateChannelForumTopicResult, error)
	EditForumTopic(ctx context.Context, userID int64, req domain.EditChannelForumTopicRequest) (domain.EditChannelForumTopicResult, error)
	UpdatePinnedForumTopic(ctx context.Context, userID int64, req domain.UpdateChannelForumTopicPinnedRequest) (domain.UpdateChannelForumTopicPinnedResult, error)
	ReorderPinnedForumTopics(ctx context.Context, userID int64, req domain.ReorderChannelPinnedForumTopicsRequest) (domain.ReorderChannelPinnedForumTopicsResult, error)
	DeleteForumTopicHistory(ctx context.Context, userID int64, req domain.DeleteChannelForumTopicHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	GetForumTopics(ctx context.Context, userID int64, filter domain.ChannelForumTopicFilter) (domain.ChannelForumTopicList, error)
	GetForumTopicsByID(ctx context.Context, userID, channelID int64, ids []int) (domain.ChannelForumTopicList, error)
	SendMessage(ctx context.Context, userID int64, req domain.SendChannelMessageRequest) (domain.SendChannelMessageResult, error)
	EditMessage(ctx context.Context, userID int64, req domain.EditChannelMessageRequest) (domain.EditChannelMessageResult, error)
	GetInlineBotMessage(ctx context.Context, botID, channelID int64, id int) (domain.Channel, domain.ChannelMessage, bool, error)
	ListStoryMessageForwards(ctx context.Context, userID int64, req domain.StoryMessageForwardListRequest) (domain.StoryMessageForwardList, error)
	EditInlineBotMessage(ctx context.Context, botID int64, req domain.EditChannelMessageRequest) (domain.EditChannelMessageResult, error)
	DeleteMessages(ctx context.Context, userID int64, req domain.DeleteChannelMessagesRequest) (domain.DeleteChannelMessagesResult, error)
	DeleteHistory(ctx context.Context, userID int64, req domain.DeleteChannelHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	DeleteParticipantHistory(ctx context.Context, userID int64, req domain.DeleteChannelParticipantHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	UpdatePinnedMessage(ctx context.Context, userID int64, req domain.UpdateChannelPinnedMessageRequest) (domain.UpdateChannelPinnedMessageResult, error)
	UnpinAllMessages(ctx context.Context, userID int64, req domain.UnpinAllChannelMessagesRequest) (domain.UpdateChannelPinnedMessageResult, error)
	ClearDanglingPinnedMessage(ctx context.Context, channelID int64, messageID int) error
	ExportInvite(ctx context.Context, userID int64, req domain.ExportChannelInviteRequest) (domain.ExportChannelInviteResult, error)
	CheckInvite(ctx context.Context, userID int64, hash string, date int) (domain.CheckChannelInviteResult, error)
	ImportInvite(ctx context.Context, userID int64, req domain.ImportChannelInviteRequest) (domain.CreateChannelResult, error)
	ListExportedInvites(ctx context.Context, userID int64, req domain.ChannelInviteListRequest) (domain.ChannelInviteList, error)
	GetExportedInvite(ctx context.Context, userID int64, req domain.GetChannelInviteRequest) (domain.ChannelInvite, error)
	EditExportedInvite(ctx context.Context, userID int64, req domain.EditChannelInviteRequest) (domain.EditChannelInviteResult, error)
	DeleteExportedInvite(ctx context.Context, userID int64, req domain.DeleteChannelInviteRequest) error
	DeleteRevokedExportedInvites(ctx context.Context, userID int64, req domain.DeleteRevokedChannelInvitesRequest) error
	ListAdminsWithInvites(ctx context.Context, userID, channelID int64) ([]domain.ChannelAdminInviteCount, error)
	ListInviteImporters(ctx context.Context, userID int64, req domain.ChannelInviteImportersRequest) (domain.ChannelInviteImporterList, error)
	PendingJoinRequests(ctx context.Context, channelID int64, limit int) (domain.ChannelPendingJoinRequests, error)
	HideChatJoinRequest(ctx context.Context, userID int64, req domain.HideChannelJoinRequestRequest) (domain.CreateChannelResult, error)
	HideAllChatJoinRequests(ctx context.Context, userID int64, req domain.HideChannelJoinRequestsRequest) (domain.CreateChannelResult, error)
	CommonChannels(ctx context.Context, userID int64, req domain.CommonChannelsRequest) (domain.CommonChannelsResult, error)
	LeftChannels(ctx context.Context, userID int64, offset, limit int) (domain.LeftChannelsResult, error)
	InactiveChannels(ctx context.Context, userID int64, limit int) (domain.ChannelDialogList, error)
	ChannelRecommendations(ctx context.Context, userID int64, req domain.ChannelRecommendationsRequest) (domain.ChannelRecommendationsResult, error)
	DiscussionGroups(ctx context.Context, userID int64, limit int) ([]domain.Channel, error)
	SetDiscussionGroup(ctx context.Context, userID, broadcastID, groupID int64) (domain.DiscussionGroupUpdateResult, error)
	SetViewForumAsMessages(ctx context.Context, userID, channelID int64, enabled bool) (bool, error)
	GetHistory(ctx context.Context, userID int64, filter domain.ChannelHistoryFilter) (domain.ChannelHistory, error)
	SearchChannelMedia(ctx context.Context, userID, channelID int64, req domain.MediaSearchRequest) (domain.ChannelHistory, error)
	CountChannelMediaCategories(ctx context.Context, userID, channelID int64) (domain.MediaCategoryCounts, error)
	SearchPosts(ctx context.Context, userID int64, req domain.ChannelSearchPostsRequest) (domain.ChannelHistory, error)
	SearchJoinedMessages(ctx context.Context, userID int64, req domain.ChannelGlobalSearchRequest) (domain.ChannelHistory, error)
	GetMessages(ctx context.Context, userID, channelID int64, ids []int) (domain.ChannelHistory, error)
	ChannelPollFanoutViews(ctx context.Context, channelID int64, msgID int, viewers []int64, now int) (map[int64]*domain.MessagePoll, error)
	GetReplies(ctx context.Context, userID int64, filter domain.ChannelRepliesFilter) (domain.ChannelHistory, error)
	GetUnreadMentions(ctx context.Context, userID int64, filter domain.ChannelUnreadMentionsFilter) (domain.ChannelHistory, error)
	ReadMentions(ctx context.Context, userID int64, req domain.ReadChannelMentionsRequest) (domain.ReadChannelMentionsResult, error)
	GetUnreadReactions(ctx context.Context, userID int64, filter domain.ChannelUnreadReactionsFilter) (domain.ChannelHistory, error)
	ReadReactions(ctx context.Context, userID int64, req domain.ReadChannelReactionsRequest) (domain.ReadChannelReactionsResult, error)
	GetDiscussionMessage(ctx context.Context, userID, channelID int64, msgID int) (domain.ChannelDiscussionMessage, error)
	SendMonoforumMessage(ctx context.Context, req domain.SendMonoforumMessageRequest) (domain.SendChannelMessageResult, error)
	ListMonoforumHistory(ctx context.Context, filter domain.MonoforumHistoryFilter) (domain.ChannelHistory, error)
	ListMonoforumDialogs(ctx context.Context, filter domain.MonoforumDialogsFilter) (domain.MonoforumDialogList, error)
	ResolveMonoforumSend(ctx context.Context, viewerUserID, monoforumID int64) (domain.Channel, bool, error)
	ReadHistory(ctx context.Context, userID int64, req domain.ReadChannelHistoryRequest) (domain.ReadChannelHistoryResult, error)
	ReadTopicHistory(ctx context.Context, userID int64, req domain.ReadChannelTopicHistoryRequest) (domain.ReadChannelTopicHistoryResult, error)
	GeneralForumTopic(ctx context.Context, userID, channelID int64) (domain.ChannelForumTopic, error)
	GetMessageReadParticipants(ctx context.Context, userID int64, req domain.ChannelReadParticipantsRequest) (domain.ChannelReadParticipantsResult, error)
	GetDifference(ctx context.Context, userID int64, req domain.ChannelDifferenceRequest) (domain.ChannelDifference, error)
	ActiveChannelIDsForUser(ctx context.Context, userID, afterChannelID int64, limit int) ([]int64, error)
	DirtyActiveChannelsForUser(ctx context.Context, userID int64, sinceDate int, afterChannelID int64, limit int) ([]domain.DirtyChannel, error)
	ActiveMemberIDs(ctx context.Context, userID, channelID int64, limit int) ([]int64, error)
	// SetActiveCall / AppendCallServiceMessage 是群通话模块的频道侧挂接点。
	SetActiveCall(ctx context.Context, channelID, callID, callAccessHash int64, notEmpty bool) (domain.Channel, error)
	AppendCallServiceMessage(ctx context.Context, channelID, senderUserID int64, date int, action domain.ChannelMessageAction) (domain.SendChannelMessageResult, error)
	AppendStarGiftAdminLog(ctx context.Context, channelID, senderUserID int64, savedID int64, date int, action domain.ChannelMessageAction) error
	InviteAdminMemberIDs(ctx context.Context, channelID int64, limit int) ([]int64, error)
	FilterActiveMemberIDs(ctx context.Context, channelID int64, userIDs []int64) ([]int64, error)
}

// FilesService 抽象文件上传分片、下载与媒体（document/photo）组装。
// 方法只用 domain 类型；rpc 层负责 tg.InputFileLocation / InputMedia ↔ domain 转换。
type FilesService interface {
	SaveFilePart(ctx context.Context, ownerUserID, fileID int64, part int, bytes []byte) (bool, error)
	SaveBigFilePart(ctx context.Context, ownerUserID, fileID int64, part, totalParts int, bytes []byte) (bool, error)
	GetFile(ctx context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error)
	// CreateEncryptedFileFromUpload 把密聊上传分片组装成盲 blob 并铸造 EncryptedFile 快照（P2）。
	CreateEncryptedFileFromUpload(ctx context.Context, file domain.UploadedFileRef, keyFingerprint int) (domain.EncryptedFileRef, error)
	// GeoMapTile 渲染 geo 消息地图缩略占位图（upload.getWebFile），确定性、无外部依赖。
	GeoMapTile(lat, long float64, w, h, zoom, scale int) ([]byte, string)
	// 资源读取（reaction / sticker / document）。
	ListAvailableReactions(ctx context.Context) ([]domain.AvailableReaction, error)
	AvailableEffects(ctx context.Context) ([]domain.AvailableEffect, int, error)
	GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error)
	ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (set domain.StickerSet, documents []domain.Document, found bool, err error)
	ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error)
	CheckStickerSetShortName(ctx context.Context, shortName string) (bool, error)
	SuggestStickerSetShortName(ctx context.Context, title string, userID int64) (string, error)
	CreateStickerSet(ctx context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error)
	ListCreatedStickerSets(ctx context.Context, userID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error)
	AddStickerToSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef, item domain.StickerSetItemInput) (domain.StickerSet, []domain.Document, error)
	RemoveStickerFromSet(ctx context.Context, actorUserID int64, documentID int64, accessHash int64) (domain.StickerSet, []domain.Document, error)
	ChangeStickerPosition(ctx context.Context, actorUserID int64, documentID int64, accessHash int64, position int) (domain.StickerSet, []domain.Document, error)
	RenameStickerSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef, title string) (domain.StickerSet, []domain.Document, error)
	DeleteStickerSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef) (domain.StickerSetKind, error)
	// 头像（profile photo）与消息媒体组装。
	CreatePhotoFromUpload(ctx context.Context, file domain.UploadedFileRef) (domain.Photo, error)
	CreatePhotoFromBytes(ctx context.Context, data []byte) (domain.Photo, error)
	// CreatePhotoFromURL / CreateDocumentFromURL 抓取外链媒体（inputMediaPhoto/DocumentExternal），
	// SSRF 安全；未启用返回 ErrExternalMediaDisabled。
	CreatePhotoFromURL(ctx context.Context, rawURL string) (domain.Photo, error)
	CreateDocumentFromURL(ctx context.Context, rawURL string) (domain.Document, error)
	// ResolveWebPage 解析链接预览（messages.getWebPagePreview / 发送挂卡片）；SSRF 安全，
	// 经 L1+L3 去重缓存。未启用返回 ErrWebPagePreviewDisabled；瞬时失败返回错误（调用方降级）。
	ResolveWebPage(ctx context.Context, rawURL string) (domain.MessageWebPage, error)
	// WebPagePreviewEnabled 报告是否启用链接预览；未启用时发送不挂 pending 占位（否则会永久 pending）。
	WebPagePreviewEnabled() bool
	// LookupWebPage 仅查缓存（不抓取）返回已解析的链接预览。发送时用它：若客户端输入时
	// getWebPagePreview 已解析过，则 echo 直接带 done 卡片（与官方一致），不依赖异步换卡。
	LookupWebPage(ctx context.Context, rawURL string) (domain.MessageWebPage, bool)
	CreateAvatarFromUpload(ctx context.Context, file domain.UploadedFileRef) (domain.Photo, error)
	CreateAvatarVideoFromUpload(ctx context.Context, file domain.UploadedFileRef, videoStartTs float64) (domain.Photo, error)
	CreateAvatarVideoMarkupFromUpload(ctx context.Context, file domain.UploadedFileRef, videoStartTs float64, markup domain.PhotoSize) (domain.Photo, error)
	CreateAvatarMarkup(ctx context.Context, size domain.PhotoSize) (domain.Photo, error)
	CreateDocumentFromUpload(ctx context.Context, file domain.UploadedFileRef, spec domain.DocumentSpec) (domain.Document, error)
	CreateDocumentFromBytes(ctx context.Context, data []byte, spec domain.DocumentSpec) (domain.Document, error)
	GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error)
	GetDocument(ctx context.Context, id int64) (domain.Document, bool, error)
	UploadProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID int64, file domain.UploadedFileRef, date int) (domain.Photo, error)
	UploadProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, file domain.UploadedFileRef, date int) (domain.Photo, error)
	SetCurrentProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID, photoID int64, date int) (domain.Photo, bool, error)
	SetCurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoID int64, date int) (domain.Photo, bool, error)
	CurrentProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID int64) (domain.Photo, bool, error)
	CurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (domain.Photo, bool, error)
	GetProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, offset, limit int, maxID int64) (photos []domain.Photo, total int, err error)
	GetProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) (photos []domain.Photo, total int, err error)
	DeleteProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, photoIDs []int64) (int, error)
	DeleteProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoIDs []int64) (int, error)
}

// LangPackService 抽象客户端语言包查询。
type LangPackService interface {
	GetLangPack(ctx context.Context, langPack, langCode string) (domain.LangPack, error)
	GetDifference(ctx context.Context, langPack, langCode string, fromVersion int) (domain.LangPack, error)
	GetStrings(ctx context.Context, langPack, langCode string, keys []string) (domain.LangPack, error)
}

// AIComposeService 抽象客户端输入框 AI 改写/润色与 aicompose tones 目录。
// 这里只使用 domain DTO；rpc 层负责 tg.TextWithEntities/InputAiComposeTone ↔ domain 转换。
type AIComposeService interface {
	ListTones(ctx context.Context, userID, hash int64) (domain.AIComposeTones, bool, error)
	GetTone(ctx context.Context, userID int64, ref domain.AIComposeToneRef) (domain.AIComposeTones, error)
	CreateTone(ctx context.Context, input domain.AIComposeToneInput) (domain.AIComposeTone, error)
	UpdateTone(ctx context.Context, update domain.AIComposeToneUpdate) (domain.AIComposeTone, error)
	SaveTone(ctx context.Context, userID int64, ref domain.AIComposeToneRef, unsave bool) error
	DeleteTone(ctx context.Context, userID int64, ref domain.AIComposeToneRef) error
	GetToneExample(ctx context.Context, userID int64, ref domain.AIComposeToneRef, num int) (domain.AIComposeToneExample, error)
	Compose(ctx context.Context, req domain.AIComposeRequest) (domain.AIComposeResult, error)
}

// Deps 按业务域注入服务接口。各域的 handler 注册见对应文件（auth.go / users.go / updates.go）。
type Deps struct {
	Auth             AuthService
	Account          AccountService
	Privacy          PrivacyService
	Help             HelpService
	AICompose        AIComposeService
	Users            UsersService
	Updates          UpdatesService
	BootstrapUpdates store.BootstrapUpdateJobStore
	Contacts         ContactsService
	Dialogs          DialogsService
	Messages         MessagesService
	Stories          StoriesService
	Channels         ChannelsService
	Files            FilesService
	Bots             BotsService
	Polls            PollsService
	Phone            PhoneService
	GroupCalls       GroupCallsService
	SFU              sfu.Service
	TURN             turnsrv.Service
	LangPack         LangPackService
	Sessions         SessionBinder
	Inline           store.InlineRegistryStore
	Limiter          RateLimiter
	Metrics          Metrics
	SecretChats      SecretChatService
	Stars            StarsService
	Gifts            GiftsService
	Passkey          PasskeyService
	Themes           ThemeService
}

// ThemeService 抽象自定义云主题(app/themes):创建/更新/查询主题 + 维护每用户已安装列表。
// 文件上传(uploadTheme)由 rpc 层直接经 Files 完成,不经此接口。只用 domain + 基本类型。
type ThemeService interface {
	Create(ctx context.Context, spec domain.ThemeSpec) (domain.Theme, error)
	Update(ctx context.Context, userID int64, ref domain.ThemeRef, upd domain.ThemeUpdate) (domain.Theme, error)
	Get(ctx context.Context, ref domain.ThemeRef) (domain.Theme, bool, error)
	Save(ctx context.Context, userID int64, ref domain.ThemeRef) error
	Unsave(ctx context.Context, userID int64, ref domain.ThemeRef) error
	Install(ctx context.Context, userID int64, ref domain.ThemeRef, dark bool) error
	ListInstalled(ctx context.Context, userID int64) ([]domain.Theme, error)
	ListForUser(ctx context.Context, userID int64) ([]domain.Theme, error)
}

// PasskeyService 抽象 passkey(WebAuthn)登录与管理(app/passkey)。挑战选项以 DataJSON
// 字节返回;注册/登录验证收原始字节(credentialID 已 base64url 解码)。FinishLogin 返回
// 已验证用户 id,auth_key 绑定由 rpc 经 Auth.BindVerifiedLogin 完成。
type PasskeyService interface {
	InitLogin(ctx context.Context) ([]byte, error)
	FinishLogin(ctx context.Context, credentialID, clientDataJSON, authenticatorData, signature []byte, userHandle string) (int64, error)
	InitRegistration(ctx context.Context, userID int64, displayName string) ([]byte, error)
	Register(ctx context.Context, userID int64, credentialID, clientDataJSON, attestationObject []byte, name string) (domain.PasskeyCredential, error)
	List(ctx context.Context, userID int64) ([]domain.PasskeyCredential, error)
	Delete(ctx context.Context, userID int64, credentialID []byte) (bool, error)
}

// GiftsService 抽象 Star 礼物（app/stargifts）：目录 + peer 收到的礼物实例 CRUD。
// 扣费/退款/服务消息投递由 rpc 层经 Stars 账本 + Messages.SendPrivateText 编排。
type GiftsService interface {
	Catalog(ctx context.Context) ([]domain.StarGift, error)
	CatalogHash(ctx context.Context) (int, error)
	GiftByID(ctx context.Context, id int64) (domain.StarGift, bool, error)
	RecordSavedGift(ctx context.Context, gift domain.SavedStarGift) (int64, error)
	ListSaved(ctx context.Context, owner domain.Peer, excludeUnsaved bool, offset string, limit int) (domain.SavedStarGiftPage, error)
	GetSaved(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error)
	CountSaved(ctx context.Context, owner domain.Peer) (int, error)
	ToggleSaved(ctx context.Context, ref domain.SavedStarGiftRef, unsaved bool) (bool, error)
	Convert(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, error)
}

// StarsService 抽象 Stars 本地账本（app/stars）：余额查询、贷记/借记、流水分页。
// 借记原子且永不为负；余额不足返回 domain.ErrStarsInsufficient（rpc 经 starsErr
// 映射为 BALANCE_TOO_LOW）。getStarsStatus 首读时惰性授予起始余额。
type StarsService interface {
	GetBalance(ctx context.Context, userID int64) (domain.StarsBalance, error)
	Credit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error)
	Debit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error)
	ListTransactions(ctx context.Context, userID int64, offset string, limit int) (domain.StarsTransactionPage, error)
}

// SecretChatService 抽象私聊端对端加密（Secret Chat）握手状态机（app/secretchat）。
// 服务端是盲中继；错误集合见 domain.ErrSecretChat* 与 app/secretchat.ErrGAInvalid
// （rpc 层经 secretChatErr 映射为 ENCRYPTION_* / CHAT_ID_INVALID / DH_G_A_INVALID）。
type SecretChatService interface {
	RequestEncryption(ctx context.Context, req domain.SecretChatRequest) (domain.SecretChat, error)
	AcceptEncryption(ctx context.Context, chatID int, viewerUserID, participantAuthKeyID, accessHash int64, gb []byte, keyFingerprint int64) (domain.SecretChat, error)
	DiscardEncryption(ctx context.Context, chatID int, viewerUserID int64, deleteHistory bool) (domain.SecretChat, bool, error)
	// DiscardForAuthKey 级联 discard 绑定该 perm auth_key 的全部活跃密聊（设备登出/授权撤销），
	// 返回实际迁移到 discarded 的密聊供通知对端。
	DiscardForAuthKey(ctx context.Context, authKeyID int64) ([]domain.SecretChat, error)
	GetSecretChat(ctx context.Context, chatID int) (domain.SecretChat, bool, error)
	SendEncrypted(ctx context.Context, chatID int, viewerUserID, accessHash int64, delivery domain.SecretMessageDelivery) (domain.SecretChat, domain.SecretChatMessage, error)
	ListNewMessages(ctx context.Context, deviceAuthKeyID int64, sinceQts, limit int) ([]domain.SecretChatMessage, error)
	DeviceReservedQts(ctx context.Context, deviceAuthKeyID int64) (int, error)
	AckQueue(ctx context.Context, deviceAuthKeyID int64, maxQts int) error
	RecordEncryptionEvent(ctx context.Context, chatID int, targetUserID, targetAuthKeyID int64, date int) error
	RecordReadEvent(ctx context.Context, chatID int, targetUserID, targetAuthKeyID int64, maxDate, date int) error
	ListStateEvents(ctx context.Context, userID, deviceAuthKeyID int64, limit int) ([]domain.EncryptedStateEvent, error)
	MarkStateEventsDelivered(ctx context.Context, deviceAuthKeyID int64, eventIDs []int64) error
	PutEncryptedFile(ctx context.Context, ownerUserID int64, ref domain.EncryptedFileRef) error
	GetEncryptedFile(ctx context.Context, id, accessHash int64) (domain.EncryptedFileRef, bool, error)
}

// PhoneService 抽象私聊 1:1 通话信令状态机（app/phone）。所有返回值都是状态快照；
// 错误集合见 app/phone 的 Err*（rpc 层经 phoneCallErr 映射为 CALL_* RPC_ERROR）。
type PhoneService interface {
	RequestCall(ctx context.Context, callerID int64, in domain.PhoneCallRequest) (domain.PhoneCall, error)
	ReceivedCall(ctx context.Context, userID, callID, accessHash int64) (domain.PhoneCall, bool, error)
	AcceptCall(ctx context.Context, userID, callID, accessHash int64, gb []byte, proto domain.PhoneCallProtocol, device domain.SessionRef) (domain.PhoneCall, error)
	ConfirmCall(ctx context.Context, userID, callID, accessHash int64, ga []byte, keyFingerprint int64, proto domain.PhoneCallProtocol) (call domain.PhoneCall, forcedDiscard bool, err error)
	DiscardCall(ctx context.Context, userID, callID, accessHash int64, reason domain.PhoneCallDiscardReason, duration int) (call domain.PhoneCall, already bool, err error)
	DiscardCallWithSlug(ctx context.Context, userID, callID, accessHash int64, reason domain.PhoneCallDiscardReason, reasonSlug string, duration int) (call domain.PhoneCall, already bool, err error)
	// Signal 在该通话的信令顺序锁内执行 forward；drop=true 表示按契约静默吞掉。
	// peerDevice 是对端受理设备锚点（可零值/失效），定向推送失败须回退 user 扇出。
	Signal(ctx context.Context, userID, callID, accessHash int64, forward func(peerUserID int64, peerDevice domain.SessionRef)) (drop bool, err error)
	Lookup(ctx context.Context, callID, accessHash int64) (domain.PhoneCall, bool)
	// ExpireDue 由 PhoneExpiryDispatcher 周期调用：迁移超时通话并返回快照。
	ExpireDue(ctx context.Context, now time.Time) []domain.PhoneCall
}

// GroupCallsService 抽象超级群语音聊天信令（app/groupcalls）。
// 错误集合见 domain.ErrGroupCall*（rpc 层映射为 GROUPCALL_* RPC_ERROR）。
type GroupCallsService interface {
	Create(ctx context.Context, channelID, creatorUserID int64, title string, now int) (domain.GroupCall, error)
	CreateConference(ctx context.Context, creatorUserID, randomID, migratedFromPhoneCallID int64, now int) (domain.GroupCall, error)
	Get(ctx context.Context, callID int64) (domain.GroupCall, bool, error)
	GetBySlug(ctx context.Context, slug string) (domain.GroupCall, bool, error)
	GetByInviteMessage(ctx context.Context, userID int64, msgID int) (domain.GroupCall, domain.GroupCallInvite, bool, error)
	Join(ctx context.Context, req domain.JoinGroupCallRequest) (domain.GroupCallMutation, error)
	Leave(ctx context.Context, callID, userID int64, now int) (domain.GroupCallMutation, error)
	RemoveConferenceParticipants(ctx context.Context, req domain.RemoveConferenceCallParticipantsRequest) (domain.RemoveConferenceCallParticipantsResult, error)
	Discard(ctx context.Context, callID int64, now int) (domain.GroupCall, []domain.GroupCallParticipant, error)
	Touch(ctx context.Context, callID, userID int64, now int) (activeSSRCs []int64, joined bool, err error)
	Participant(ctx context.Context, callID, userID int64) (domain.GroupCallParticipant, bool, error)
	Participants(ctx context.Context, callID int64, offset string, limit int) (domain.GroupCallParticipantPage, error)
	UpdateParticipant(ctx context.Context, callID, userID int64, update domain.GroupCallParticipantUpdate) (domain.GroupCallMutation, bool, error)
	SetTitle(ctx context.Context, callID int64, title string) (domain.GroupCall, bool, error)
	SetJoinMuted(ctx context.Context, callID int64, joinMuted bool) (domain.GroupCall, bool, error)
	SetStartedMessageID(ctx context.Context, callID int64, msgID int) error
	SweepStale(ctx context.Context, checkOlderThan, now, limit int) ([]domain.GroupCallMutation, error)
	ResetAllParticipants(ctx context.Context, now int) ([]domain.GroupCall, error)
	NextRaiseHandRating(ctx context.Context, callID int64) (int64, error)
	SetParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64, override domain.GroupCallParticipantOverride, clear bool) error
	ParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64) (domain.GroupCallParticipantOverride, bool, error)
	CreateConferenceInvite(ctx context.Context, invite domain.GroupCallInvite) (domain.GroupCallInvite, error)
	SetConferenceInviteStatus(ctx context.Context, callID, inviteeUserID int64, msgID int, status domain.GroupCallInviteStatus, now int) (domain.GroupCallInvite, bool, error)
	ConferenceRecipients(ctx context.Context, callID int64) ([]int64, error)
	AppendChainBlock(ctx context.Context, block domain.GroupCallChainBlock) (domain.GroupCallChainBlock, error)
	ChainBlocks(ctx context.Context, callID int64, subChainID, offset, limit int) (domain.GroupCallChainBlockPage, error)
}

// PollsService 抽象 poll 权威态的发送时创建与投票人列表（messages.getPollVotes）。
type PollsService interface {
	CreatePoll(ctx context.Context, def domain.PollDefinition) error
	GetPollDefinition(ctx context.Context, pollID int64) (domain.PollDefinition, bool, error)
	ListPollVotes(ctx context.Context, req domain.PollVotesListRequest) (domain.PollVotesList, error)
}
