// Package identity 存储 operator 可在管理面板 Server Settings → Server identity
// 中编辑的服务器身份：名称、简介、图标，以及登录通知（登录码投递与每次登录成功后的
// 欢迎消息）的可覆盖模板。它刻意不属于 internal/config 的 Config：config 只在进程
// 启动时从 .env 加载一次，而 identity 由管理面板随时修改、主服务器立即生效、无需重启
// —— 因此它以纯文本文件落在磁盘上（目录由 Config.IdentityDir 指定），每次读取都现取
// 现用，不缓存进内存。
//
// 两个进程协作：cmd/telesrv-admin（管理二进制）写，cmd/telesrv（主服务器二进制）读。
// 读写均为原子操作（临时文件 + rename），可安全并发。
package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	metaFileName = "identity.json"
	iconBaseName = "icon"
)

var writeMu sync.Mutex

// Info 是展示给客户端、可编辑的服务器身份。Name 经
// domain.SetOfficialSystemUserDisplayName 影响 777000 展示名（启动时应用一次，
// 之后由 internal/app/systemidentity.Watcher 轮询本文件在运行期持续跟随），模板
// 覆盖则在每次发送登录通知时实时读取。
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// IconExt 是图标文件的扩展名（如 ".png"），未上传图标时为空。与 Name/Description
	// 并列存放，使 Store 无需扫描目录即可定位图标文件。
	IconExt string `json:"icon_ext,omitempty"`
	// WelcomeMessagePhoneTemplate/WelcomeMessageEmailTemplate 是管理面板对手机号/邮箱
	// 登录成功后的欢迎消息（由官方系统账号 777000 发送）的原始覆盖——对应
	// domain.ResolveWelcomeMessageTemplate。空表示「未配置」：解析器落到
	// TELESRV_WELCOME_MESSAGE_* 环境变量，再落到编译期内置文案。刻意存原始值（而非
	// 预解析结果），使从未碰过面板的部署始终跟着兜底链最新值走（含未来内置文案的调整）。
	WelcomeMessagePhoneTemplate string `json:"welcome_message_phone_template,omitempty"`
	WelcomeMessageEmailTemplate string `json:"welcome_message_email_template,omitempty"`
	// LoginCodeMessageTemplate 是管理面板对 777000 登录码投递消息的原始覆盖（见
	// domain.ResolveLoginCodeMessageTemplate）。与欢迎消息不同，它只有一份——消息不随
	// 投递渠道变化。空表示「未配置」，语义同上。
	LoginCodeMessageTemplate string `json:"login_code_message_template,omitempty"`
}

// Store 在指定目录（通常为 Config.IdentityDir）下读写 Info 与图标文件。所有方法均可
// 多协程、跨进程并发调用（管理二进制写、主服务器二进制读）。
type Store struct {
	dir string
}

func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

func (s *Store) metaPath() string {
	return filepath.Join(s.dir, metaFileName)
}

func (s *Store) iconPath(ext string) string {
	return filepath.Join(s.dir, iconBaseName+ext)
}

// Get 读取当前身份。文件不存在不算错误——只是尚未配置过，返回 Info{}（全空）。
func (s *Store) Get() (Info, error) {
	data, err := os.ReadFile(s.metaPath())
	if os.IsNotExist(err) {
		return Info{}, nil
	}
	if err != nil {
		return Info{}, fmt.Errorf("identity: read: %w", err)
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("identity: decode: %w", err)
	}
	return info, nil
}

// SetText 更新名称与简介，保留已配置的图标与模板覆盖。
func (s *Store) SetText(name, description string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	info, err := s.Get()
	if err != nil {
		return err
	}
	info.Name = strings.TrimSpace(name)
	info.Description = strings.TrimSpace(description)
	return s.save(info)
}

// SetWelcomeMessageTemplates 更新登录成功后欢迎消息的模板覆盖，保留其余身份字段。
// 任一参数为空串即清除该渠道的覆盖（回退到环境变量 / 内置文案——沿用 Info 各处
// 「空 = 未设置」的约定），且互不影响。
func (s *Store) SetWelcomeMessageTemplates(phone, email string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	info, err := s.Get()
	if err != nil {
		return err
	}
	info.WelcomeMessagePhoneTemplate = strings.TrimSpace(phone)
	info.WelcomeMessageEmailTemplate = strings.TrimSpace(email)
	return s.save(info)
}

// SetLoginCodeMessageTemplate 更新登录码投递消息的管理面板覆盖，保留其余身份字段。
// 空串即清除覆盖（回退链路见 Info 字段注释）。登录码模板不分渠道，只有一份。
//
// 调用方必须先用 domain.ValidateLoginCodeMessageTemplate 校验模板——
// 本方法不自行拒绝缺失 {{code}} 占位符的模板，因为 internal/identity 不依赖
// internal/domain（见包注释）。
func (s *Store) SetLoginCodeMessageTemplate(template string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	info, err := s.Get()
	if err != nil {
		return err
	}
	info.LoginCodeMessageTemplate = strings.TrimSpace(template)
	return s.save(info)
}

// SetIcon 替换图标文件（移除旧扩展名下的既有图标）并把扩展名写回 identity.json。
// ext 必须带前导点（如 ".png"）。
func (s *Store) SetIcon(data []byte, ext string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	info, err := s.Get()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("identity: mkdir: %w", err)
	}
	if err := writeFileAtomic(s.iconPath(ext), data, 0o644); err != nil {
		return fmt.Errorf("identity: write icon: %w", err)
	}
	oldExt := info.IconExt
	info.IconExt = ext
	if err := s.save(info); err != nil {
		return err
	}
	if oldExt != "" && oldExt != ext {
		_ = os.Remove(s.iconPath(oldExt))
	}
	return nil
}

// RemoveIcon 删除已配置的图标（若有）。
func (s *Store) RemoveIcon() error {
	writeMu.Lock()
	defer writeMu.Unlock()
	info, err := s.Get()
	if err != nil {
		return err
	}
	if info.IconExt == "" {
		return nil
	}
	oldExt := info.IconExt
	info.IconExt = ""
	if err := s.save(info); err != nil {
		return err
	}
	_ = os.Remove(s.iconPath(oldExt))
	return nil
}

// Icon 返回图标的原始字节与扩展名；未配置图标时返回 ("", nil, false)。
func (s *Store) Icon() (data []byte, ext string, ok bool) {
	info, err := s.Get()
	if err != nil || info.IconExt == "" {
		return nil, "", false
	}
	raw, err := os.ReadFile(s.iconPath(info.IconExt))
	if err != nil {
		return nil, "", false
	}
	return raw, info.IconExt, true
}

// IconFingerprint 返回一个仅随「图标内容」变化的短字符串，供主服务器轮询判断
// operator 是否新增/替换/移除了 Server identity 图标。同名扩展覆盖上传时靠图标的
// mtime+size 区分；未配置图标返回 ""；读不到图标文件时退化为扩展名。名称的变化由
// 调用方直接比较 Info.Name，不需要掺进这里。
func (s *Store) IconFingerprint() (string, error) {
	info, err := s.Get()
	if err != nil {
		return "", err
	}
	if info.IconExt == "" {
		return "", nil
	}
	if st, statErr := os.Stat(s.iconPath(info.IconExt)); statErr == nil {
		return fmt.Sprintf("%s:%d:%d", info.IconExt, st.ModTime().UnixNano(), st.Size()), nil
	}
	return info.IconExt, nil
}

func (s *Store) save(info Info) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("identity: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("identity: encode: %w", err)
	}
	if err := writeFileAtomic(s.metaPath(), data, 0o644); err != nil {
		return fmt.Errorf("identity: write: %w", err)
	}
	return nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return renameWithRetry(f.Name(), path)
}

// renameWithRetry 尝试原子替换目标文件。Windows 上无法在目标文件正被其他进程/协程
// 打开读取时完成替换（MoveFileEx 报 sharing violation / access denied）；主服务器
// 会持续轮询读取 identity.json，管理面板的每一次保存都可能撞上这个窗口。读取方
// 只在 ReadFile 期间短暂持有句柄，因此短暂重试即可完成替换，不需要跨平台加锁。
func renameWithRetry(oldpath, newpath string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		if err = os.Rename(oldpath, newpath); err == nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return err
}
