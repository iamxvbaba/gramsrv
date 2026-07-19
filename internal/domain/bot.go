package domain

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxBotsPerOwner 是每个用户可创建的 bot 上限（对齐官方 appConfig bots_create_limit_default）。
	MaxBotsPerOwner = 20
	// BotTokenSecretLength 是 bot token 随机段长度（官方格式 <bot_id>:<35 位>）。
	BotTokenSecretLength = 35
	// BotUsernameSuffix 是 bot username 的强制后缀（大小写不敏感）。
	BotUsernameSuffix = "bot"
	// MaxBotNameLength 是 bot 显示名（first_name）上限。
	MaxBotNameLength = 64
	// MaxBotInlinePlaceholderLen 是 inline mode placeholder 上限（bots.inline_placeholder）。
	MaxBotInlinePlaceholderLen = 128
	// MaxBotAppShortNameLen 是 bot app short_name 上限。
	MaxBotAppShortNameLen = 64
	// MaxBotAppTitleLen 是 bot app title 上限。
	MaxBotAppTitleLen = 128
	// MaxBotAppDescriptionLen 是 bot app description 上限。
	MaxBotAppDescriptionLen = 512
	// MaxBotAppURLLen 是 bot app / attach menu webview URL 上限。
	MaxBotAppURLLen = 512
	// MaxBotAttachMenuPeerTypes 限制 attach menu peer_types 数量，避免无界响应。
	MaxBotAttachMenuPeerTypes = 8
	// MaxBotAttachMenuIcons 限制 attach menu icon 变体数量。
	MaxBotAttachMenuIcons = 8
	// MaxBotPreviewMedia 是单 bot main mini app preview media 上限。
	MaxBotPreviewMedia = 20
	// MaxBotRequestedPeerQuantity 限制 request peer 一次选择数量。
	MaxBotRequestedPeerQuantity = 10
	// MaxBotCustomMethodLen 限制 WebView custom method 名称长度。
	MaxBotCustomMethodLen = 128
	// MaxBotCustomMethodPayloadLen 限制 WebView custom method JSON 载荷长度。
	MaxBotCustomMethodPayloadLen = 4096
)

// bot 业务错误。
var (
	ErrBotTokenInvalid    = errors.New("bot token invalid")
	ErrBotsTooMany        = errors.New("bot create limit exceeded")
	ErrBotNameInvalid     = errors.New("bot name invalid")
	ErrBotUsernameInvalid = errors.New("bot username invalid")
	ErrBotNotFound        = errors.New("bot not found")
	// ErrBotCommandInvalid 表示命令名/描述非法（bots.setBotCommands）。
	ErrBotCommandInvalid = errors.New("bot command invalid")
	// ErrBotInfoInvalid 表示 setBotInfo 的 name/about/description 越界。
	ErrBotInfoInvalid = errors.New("bot info invalid")
	// ErrBotMenuButtonInvalid 表示 menu button 文本/URL 非法。
	ErrBotMenuButtonInvalid = errors.New("bot menu button invalid")
	// ErrBotInlinePlaceholderInvalid 表示 inline placeholder 为空以外的文本越界。
	ErrBotInlinePlaceholderInvalid = errors.New("bot inline placeholder invalid")
	// ErrBotAppInvalid 表示 bot app 元数据、URL 或 access hash 非法。
	ErrBotAppInvalid = errors.New("bot app invalid")
	// ErrBotAppShortNameInvalid 表示 bot app short_name 非法。
	ErrBotAppShortNameInvalid = errors.New("bot app short name invalid")
	// ErrBotAttachMenuInvalid 表示 attach menu catalog 或用户状态非法。
	ErrBotAttachMenuInvalid = errors.New("bot attach menu invalid")
	// ErrBotRequestedButtonInvalid 表示 request-peer button 上下文非法或已过期。
	ErrBotRequestedButtonInvalid = errors.New("bot requested button invalid")
	// ErrBotDownloadParamsInvalid 表示 mini app download 参数未通过安全策略。
	ErrBotDownloadParamsInvalid = errors.New("bot download params invalid")
	// ErrBotCustomMethodUnavailable 表示没有可完成 WebView custom method 的 bot 侧回答。
	ErrBotCustomMethodUnavailable = errors.New("bot custom method unavailable")
	// ErrBotSessionsNotRevoked 表示 token 已轮换但撤销已登录 session 失败
	//（token 不可回滚，调用方须告知用户重试以确保旧 session 终止）。
	ErrBotSessionsNotRevoked = errors.New("bot sessions not revoked")
)

const (
	// MaxBotCommands 是单个 bot 的命令上限（default scope）。
	MaxBotCommands = 100
	// MaxBotCommandLen 是命令名长度上限（不含前导 /）。
	MaxBotCommandLen = 32
	// MaxBotCommandDescriptionLen 是命令描述长度上限。
	MaxBotCommandDescriptionLen = 256
	// MaxBotAboutLen 是 bot about（users.about）长度上限。
	MaxBotAboutLen = 120
	// MaxBotDescriptionLen 是 bot description（"What can this bot do?"）长度上限。
	MaxBotDescriptionLen = 512
	// MaxBotMenuButtonTextLen / MaxBotMenuButtonURLLen 是菜单按钮文本/URL 上限。
	MaxBotMenuButtonTextLen = 64
	MaxBotMenuButtonURLLen  = 512
)

// BotCommand 是 bot 的一条命令（botInfo.commands 元素）。
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// BotMenuButtonType 标识菜单按钮类型。
type BotMenuButtonType int

const (
	// BotMenuButtonDefault 是默认（不显示特殊按钮，客户端按 commands 是否非空决定 '/' 圆钮）。
	BotMenuButtonDefault BotMenuButtonType = 0
	// BotMenuButtonCommands 显式让客户端展示命令菜单按钮。
	BotMenuButtonCommands BotMenuButtonType = 1
	// BotMenuButtonWebView 是带文本+URL 的 web view 按钮。
	BotMenuButtonWebView BotMenuButtonType = 2
)

// BotMenuButton 是 bot 的菜单按钮（per-bot 全局；per-user 维度 P2 不做）。
type BotMenuButton struct {
	Type BotMenuButtonType
	Text string
	URL  string
}

// BotInfoUpdate 是 bots.setBotInfo 的部分更新（name→users.first_name、
// about→users.about、description→bots.description）。各 SetXxx 为 false 时不动该字段。
type BotInfoUpdate struct {
	SetName        bool
	Name           string
	SetAbout       bool
	About          string
	SetDescription bool
	Description    string
}

// BotApp 是 mini app catalog 的一项。一个 bot 可拥有多个 app，ID/access_hash
// 在创建后稳定，short_name 在同一 bot 下唯一。
type BotApp struct {
	ID                 int64
	BotUserID          int64
	ShortName          string
	Title              string
	Description        string
	URL                string
	PhotoID            int64
	DocumentID         int64
	AccessHash         int64
	Hash               int64
	Inactive           bool
	RequestWriteAccess bool
	HasSettings        bool
	Main               bool
}

// BotAppSettings 是 BotInfo.app_settings 的协议中立表示。
type BotAppSettings struct {
	PlaceholderPath     []byte
	BackgroundColor     int
	BackgroundDarkColor int
	HeaderColor         int
	HeaderDarkColor     int
	HasBackgroundColor  bool
	HasBackgroundDark   bool
	HasHeaderColor      bool
	HasHeaderDarkColor  bool
}

// BotAppPreviewMedia 是 main mini app preview media 的持久快照。
type BotAppPreviewMedia struct {
	ID         int64
	BotUserID  int64
	AppID      int64
	Position   int
	PhotoID    int64
	DocumentID int64
}

// BotAttachMenuIconColor 描述 attach menu icon 的主题色覆盖。
type BotAttachMenuIconColor struct {
	Name  string
	Color int
}

// BotAttachMenuIcon 描述 attach menu 的平台 icon 文档引用。
type BotAttachMenuIcon struct {
	Name       string
	DocumentID int64
	Colors     []BotAttachMenuIconColor
}

// BotAttachMenuBot 是全局 attach/side menu catalog 的一项。
type BotAttachMenuBot struct {
	BotUserID                int64
	AppID                    int64
	ShortName                string
	Inactive                 bool
	HasSettings              bool
	RequestWriteAccess       bool
	ShowInAttachMenu         bool
	ShowInSideMenu           bool
	SideMenuDisclaimerNeeded bool
	PeerTypes                []string
	Icons                    []BotAttachMenuIcon
}

// BotAttachMenuState 是用户对某个 attach menu bot 的启用与写权限状态。
type BotAttachMenuState struct {
	UserID       int64
	BotUserID    int64
	Enabled      bool
	WriteAllowed bool
}

// BotRequestedWebViewButton 是 bots.requestWebViewButton 创建的 request-peer 上下文。
type BotRequestedWebViewButton struct {
	WebAppReqID       string
	BotUserID         int64
	UserID            int64
	ButtonID          int
	Text              string
	PeerType          string
	MaxQuantity       int
	PeerFilter        *BotRequestPeerFilter
	NameRequested     bool
	UsernameRequested bool
	PhotoRequested    bool
	CreatedAt         time.Time
	ExpiresAt         time.Time
}

// BotWebViewCustomMethodQuery 是 custom method 的 pending 记录。没有 bot 侧回答
// registry 时只记录查询并显式返回 blocked 错误，避免假成功。
type BotWebViewCustomMethodQuery struct {
	ID           string
	BotUserID    int64
	UserID       int64
	CustomMethod string
	ParamsJSON   string
	CreatedAt    time.Time
	ExpiresAt    time.Time
}

// ValidBotCommandName 校验命令名：1-32 位小写字母/数字/下划线（不含前导 /）。
func ValidBotCommandName(cmd string) bool {
	if len(cmd) == 0 || len(cmd) > MaxBotCommandLen {
		return false
	}
	for _, r := range cmd {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// BotProfile 是 bots 表一行：bot 账号的元数据与 token。
// 显示名/username/about 复用 users 行，不在此重复。
type BotProfile struct {
	BotUserID         int64
	OwnerUserID       int64
	TokenSecret       string
	Description       string
	Commands          []BotCommand
	ChatHistory       bool // privacy mode 关闭 = true（bot_chat_history flag）
	Nochats           bool // true = 不允许加群（bot_nochats flag）
	InlineGeo         bool
	InlinePlaceholder string
	MenuButton        BotMenuButton
	HasMainApp        bool
	HasAttachMenu     bool
	HasPreviewMedias  bool
	AppSettings       *BotAppSettings
}

// BotChatState 是内置 bot（当前仅 BotFather）与某用户的对话状态机持久态。
type BotChatState struct {
	BotUserID int64
	UserID    int64
	// Command 是进行中的主命令（newbot/token/revoke）。
	Command string `json:"command"`
	// Step 是主命令内的当前步骤（name/username/choose）。
	Step string `json:"step"`
	// Draft 暂存跨步骤的中间输入（如 newbot 已收的 name）。
	Draft map[string]string `json:"draft,omitempty"`
}

// FormatBotToken 拼装完整 bot token（<bot_user_id>:<secret>）。
func FormatBotToken(botUserID int64, secret string) string {
	return strconv.FormatInt(botUserID, 10) + ":" + secret
}

// ParseBotToken 拆解完整 bot token。格式非法时 ok=false（不区分具体原因，
// 调用方统一回 ACCESS_TOKEN_INVALID，避免泄漏存在性）。
func ParseBotToken(token string) (botUserID int64, secret string, ok bool) {
	idPart, secret, found := strings.Cut(token, ":")
	if !found || idPart == "" || secret == "" {
		return 0, "", false
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || id <= 0 {
		return 0, "", false
	}
	return id, secret, true
}

// ValidBotUsername 校验 bot username：5-32 位、字母开头、[A-Za-z0-9_]、
// 以 bot 结尾（大小写不敏感）。BotFather 等种子账号不经此校验。
func ValidBotUsername(username string) bool {
	if len(username) < 5 || len(username) > 32 {
		return false
	}
	for i, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9', r == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return strings.HasSuffix(strings.ToLower(username), BotUsernameSuffix)
}
