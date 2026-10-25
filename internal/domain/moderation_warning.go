package domain

// 本文件实现 SCAM/FAKE 标记在个人资料描述（About）中的服务端文字化：
// 官方客户端依据 tg.User.scam / tg.Channel.scam 渲染本地化角标，但纯角标
// 在自建部署的受众里常被忽略；因此 getFullUser / channels.getFullChannel /
// 邀请预览返回的 About 会在首行注入一句平台警告，其后才是 peer 自己的描述。
//
// 警告文本来自 TELESRV_SCAM_WARNING / TELESRV_FAKE_WARNING（见 internal/config，
// 由 cmd/telesrv/main.go 启动时注入），留空时使用内置英文默认值——与
// .env.example 中 «Empty keeps built-in English defaults» 的说明一致。
//
// 存储层的描述永远不会被改写：警告只存在于读路径投影上，owner 每次编辑
// 描述后警告依旧重新出现在首行（语义守护见 rpc/moderation_flags_projection_test.go）。

import (
	"strings"
	"sync/atomic"
)

// 内置默认文案是完整的一句警告（而非官方客户端的 SCAM/FAKE 徽标词）：官方客户端只
// 在名字旁画一个两字母角标，描述里没有可读的解释；这里把「为什么被标记」直接写进
// 描述首行，与 operator 在 .env 中可覆盖的语义保持一致（空值 → 下面这段）。
// 两句都用官方措辞：fake 是冒充真人/知名机构，scam 是涉诈与索取钱款。
const (
	defaultScamProfileWarning = "⚠️ Warning: This account has received numerous complaints " +
		"related to fraud or attempts to impersonate an official account. Be cautious, " +
		"especially if you are asked for money."
	defaultFakeProfileWarning = "⚠️ Warning: Many users have reported that this account is " +
		"impersonating a well-known person or organization."
)

// atomic.Value 承载，与 officialSystemUserDisplayName 等启动期配置同一写法：
// 投影路径只读，避免未来接入运行期刷新时引入数据竞争。
var (
	scamProfileWarning atomic.Value // string
	fakeProfileWarning atomic.Value // string
)

// SetModerationProfileWarning 记录 operator 配置的警告文本，启动时调用一次。
// 空值（或纯空白）表示使用内置英文默认值。
func SetModerationProfileWarning(scam, fake string) {
	scamProfileWarning.Store(strings.TrimSpace(scam))
	fakeProfileWarning.Store(strings.TrimSpace(fake))
}

// moderationProfileWarning 返回当前生效的警告文本；未标记的 peer 返回空串。
// admin 拒绝同时置两个标记（ErrPeerModerationFlagsInvalid），因此 scam 优先即可。
func moderationProfileWarning(scam, fake bool) string {
	if scam {
		if text, ok := scamProfileWarning.Load().(string); ok && text != "" {
			return text
		}
		return defaultScamProfileWarning
	}
	if fake {
		if text, ok := fakeProfileWarning.Load().(string); ok && text != "" {
			return text
		}
		return defaultFakeProfileWarning
	}
	return ""
}

// ModerationProfileWarning 把平台警告拼在 peer 自己的描述前：
// 未被标记时原样返回 about（零分配）；被标记时返回「警告\n描述」，
// 描述为空时只返回警告。分隔符用单个换行——About 在客户端逐行渲染。
func ModerationProfileWarning(scam, fake bool, about string) string {
	warning := moderationProfileWarning(scam, fake)
	if warning == "" {
		return about
	}
	if about == "" {
		return warning
	}
	return warning + "\n" + about
}
