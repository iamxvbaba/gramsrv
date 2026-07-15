package tdesktop

import (
	"time"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/links"
)

// BuildConfig 构造 help.getConfig 返回的 tg.Config，含自建 DC 的 DCOptions。
//
// 字段值取 Telegram 常见默认；TDesktop 联调阶段按客户端实际需要微调
// （记录于 docs/compatibility-matrix.md）。
func BuildConfig(dc int, ip string, port int, now time.Time, publicBaseURL string) *tg.Config {
	meURLPrefix := links.NormalizeBaseURL(publicBaseURL) + "/"
	config := &tg.Config{
		Date:     int(now.Unix()),
		Expires:  int(now.Add(time.Hour).Unix()),
		TestMode: false,
		ThisDC:   dc,
		// 不下发 DCOptions：客户端（TDesktop patch / drklo fork）已写死 static DC
		// 地址，空列表会让客户端保留它——drklo ConnectionsManager.cpp 的 processConfig
		// 在 dc_options 为空时整段跳过 replaceAddresses/saveConfig，既不覆盖也不持久化。
		// 服务端因此无需配置对外可达 IP，换网络/部署只改客户端写死地址即可。ip/port
		// 参数暂留，供未来需要显式 advertise 时改回。
		DCOptions:            nil,
		ChatSizeMax:          200,
		MegagroupSizeMax:     200000,
		ForwardedCountMax:    100,
		OnlineUpdatePeriodMs: 120000,
		OfflineBlurTimeoutMs: 5000,
		OfflineIdleTimeoutMs: 30000,
		OnlineCloudTimeoutMs: 300000,
		NotifyCloudDelayMs:   30000,
		NotifyDefaultDelayMs: 1500,
		PushChatPeriodMs:     60000,
		PushChatLimit:        2,
		EditTimeLimit:        172800,
		// 官方现行 revoke 三元组：无时限 + 允许撤回对方发来的私聊消息。
		// TDesktop 的私聊 "Also delete for X" 复选框要求
		// revoke_pm_inbox=true 且 revoke_pm_time_limit=0x7FFFFFFF，
		// 否则双向删除 UI 永不出现。
		RevokeTimeLimit:      2147483647,
		RevokePmTimeLimit:    2147483647,
		RevokePmInbox:        true,
		RatingEDecay:         2419200,
		StickersRecentLimit:  200,
		CallReceiveTimeoutMs: 20000,
		CallRingTimeoutMs:    90000,
		CallConnectTimeoutMs: 30000,
		CallPacketTimeoutMs:  10000,
		MeURLPrefix:          meURLPrefix,
		CaptionLengthMax:     1024,
		MessageLengthMax:     4096,
		WebfileDCID:          dc,
	}
	config.SetReactionsDefault(&tg.ReactionEmoji{Emoticon: DefaultReactionEmoticon})
	return config
}

// NearestDC 构造 help.getNearestDc 返回值。
func NearestDC(dc int) *tg.NearestDC {
	return &tg.NearestDC{
		// 默认国家=中国：DrKLO/TDesktop 登录页据此预选区号(+86)。
		Country:   "CN",
		ThisDC:    dc,
		NearestDC: dc,
	}
}
